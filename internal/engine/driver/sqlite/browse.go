package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
)

// Browse renders a BrowseRequest as SQLite and returns one page.
//
// Pagination is by key wherever a TOTAL order can be formed, and by
// LIMIT/OFFSET only where one cannot. The difference is not style: OFFSET
// makes SQLite walk and discard every skipped row, so scrolling to row
// 500,000 costs 500,000 discarded rows, while a keyset predicate seeks
// straight to the boundary. Offset paging is the fallback of last resort,
// used here only for views, for tables whose rowid is unreachable, and for
// tables whose key cannot survive the trip out through driver.Value and back
// (see keysetSafe).
//
// Totality is what makes keyset correct, and it is the whole trick. A sort
// on a non-unique column cannot be paged by key on its own: two rows sharing
// a value straddle a page boundary and one is skipped or repeated depending
// on which side the engine happened to put them. The answer is to APPEND a
// tiebreaker the engine guarantees unique — rowid, or the primary key of a
// WITHOUT ROWID table — so the ordering becomes total, not to give up on
// keyset. planOrder does that appending; every test that pages a whole table
// and counts each row exactly once is a test of it.
func (c *conn) Browse(ctx context.Context, req driver.BrowseRequest) (*driver.BrowsePage, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// SQLite opens exactly one file and this driver never ATTACHes another
	// (Capabilities.MultipleDatabases is false, and Introspect reports the
	// single database under the fixed name "main"). Naming anything else is
	// therefore a caller error, and saying so beats silently serving main's
	// table under another database's name.
	if req.Database != "" && req.Database != databaseName {
		return nil, dberr.New(dberr.KindNotFound, "no such database: "+req.Database)
	}
	// SQLite reads a negative OFFSET as zero rather than rejecting it, which
	// would turn a caller's arithmetic slip into a page that serves the top of
	// the table and reports a next-offset further below zero — the same page,
	// forever. BrowseRequest.Validate has no opinion on Offset because a
	// driver that does not paginate by offset has no use for one.
	if req.Offset < 0 {
		return nil, dberr.New(dberr.KindInvalid, "browse: offset cannot be negative")
	}

	// Columns is the source of the visible column set AND the reason a
	// missing table reports NotFound here: PRAGMA table_info returns zero
	// rows rather than an error for a table that does not exist, and Columns
	// already turns that into KindNotFound.
	cols, err := c.Columns(ctx, req.Database, req.Table)
	if err != nil {
		return nil, err
	}

	order, byKey, err := c.planOrder(ctx, req, cols)
	if err != nil {
		return nil, err
	}

	// The keyset columns are selected IN ADDITION to the table's real
	// columns, never instead of them, and stripped off again below. Selecting
	// them by expression rather than reaching for `SELECT *` plus rowid is
	// what keeps rowid — or any sort column that is not part of the table's
	// visible set — out of the user's grid.
	sel := make([]string, 0, len(cols)+2*len(order))
	for _, col := range cols {
		sel = append(sel, c.Quote(col.Name))
	}
	if byKey {
		for _, t := range order {
			sel = append(sel, t.expr)
		}
		// typeof() rides along so the keyset can be judged on the value's
		// STORAGE CLASS rather than on its column's declared type. It has to
		// come from SQLite because it cannot be recovered afterwards: the
		// cursor turns every []byte into a Go string on the way out (see
		// cursor.Next), so a blob and a text value are the same thing by the
		// time they reach driver.Normalize.
		for _, t := range order {
			sel = append(sel, "typeof("+t.expr+")")
		}
	}

	var where string
	var args []any
	if byKey && len(req.After) > 0 {
		if len(req.After) != len(order) {
			return nil, dberr.New(dberr.KindInvalid,
				"browse: the cursor does not match the sort")
		}
		// A width match is not enough: two single-column sorts are the same
		// width, so a cursor taken under one silently pages under the other
		// unless something also names WHICH sort it came from. SortToken is
		// that name, and it is REQUIRED rather than optional whenever After
		// is present.
		//
		// Optional verification would not be verification. A caller that
		// forgot the token would get exactly the silent corruption this check
		// exists to prevent — rows skipped or repeated in the grid with
		// nothing to indicate it — and "the caller should remember" is the
		// shape of two defects this project has already shipped. There is no
		// legitimate request with After and no token, either: every After
		// value came from a page, and every page issues a token alongside it.
		// So the only thing an omitted token can mean is a caller that built
		// the cursor itself or dropped a field, and both should hear about it
		// loudly, at the first request, rather than read a wrong page.
		if req.SortToken == "" {
			return nil, dberr.New(dberr.KindInvalid,
				"browse: this cursor is missing the sort it was issued for; start again from the first page")
		}
		if req.SortToken != sortToken(req.Table, order) {
			return nil, dberr.New(dberr.KindInvalid,
				"browse: this cursor was issued for a different sort; start again from the first page")
		}
		pred, pargs, err := keysetPredicate(order, req.After)
		if err != nil {
			return nil, err
		}
		where, args = " WHERE "+pred, pargs
	}

	stmt := "SELECT " + strings.Join(sel, ", ") +
		" FROM " + c.Quote(req.Table) + where + orderBy(order) + " LIMIT ?"
	args = append(args, req.Limit)
	if !byKey {
		stmt += " OFFSET ?"
		args = append(args, req.Offset)
	}

	// Routed through c.Query rather than c.db so the generated statement
	// meets a read-only connection's guard on the same terms a user's own
	// SQL does, and so rows arrive through the cursor that already classifies
	// driver errors and copies []byte out of the driver's buffers. The
	// statement is a single SELECT, which is what the guard permits.
	cur, err := c.Query(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer cur.Close()
	rows, err := cur.Next(ctx, req.Limit)
	if err != nil {
		return nil, err
	}

	page := &driver.BrowsePage{Columns: make([]driver.ColumnMeta, len(cols))}
	for i, col := range cols {
		// DataType comes from the DECLARED type rather than the result set's
		// reported one: SQLite's types live on values, not columns, so a
		// column that happens to hold only NULLs on this page reports no type
		// at all through database/sql. The grid needs the column's declared
		// type, which does not change page to page.
		page.Columns[i] = driver.ColumnMeta{Name: col.Name, DataType: col.DataType}
	}
	for _, row := range rows {
		cells := make([]driver.Value, len(cols))
		for i := range cols {
			cells[i] = driver.Normalize(row[i])
		}
		page.Rows = append(page.Rows, cells)
	}
	// Fewer rows than asked for can only mean the table ran out: LIMIT is the
	// only thing that truncates this statement.
	page.Exhausted = len(rows) < req.Limit

	switch {
	case !byKey:
		page.Offset = req.Offset + len(rows)
	case !page.Exhausted:
		last := rows[len(rows)-1]
		keyset := make([]driver.Value, len(order))
		for i := range order {
			// A Keyset is a promise that handing it back in After produces
			// the next page, and a blob cannot keep it. SQLite's affinities
			// are preferences, not constraints — a TEXT column stores a BLOB
			// handed to it verbatim — so keysetSafe's reading of the declared
			// type is not the last word, and this is. Binding a blob back as
			// the text it was rendered into compares TEXT against BLOB
			// storage, and SQLite sorts every blob above every string: the
			// predicate would match the cursor's own row, and the next page
			// would be the page before it, forever. Refusing is loud; that
			// loop is not.
			if class, _ := last[len(cols)+len(order)+i].(string); class == blobClass {
				return nil, dberr.New(dberr.KindUnsupported,
					"browse: a key column holds binary data")
			}
			keyset[i] = driver.Normalize(last[len(cols)+i])
		}
		page.Keyset = keyset
		page.SortToken = sortToken(req.Table, order)
	}
	return page, nil
}

// blobClass is what SQLite's typeof() calls a value stored as bytes. Its
// four siblings — null, integer, real, text — all map onto a driver.Value
// that keysetArg can bind back, which is why only this one is turned away.
const blobClass = "blob"

// orderTerm is one ORDER BY term with its column already rendered as SQL.
type orderTerm struct {
	expr string
	desc bool
}

// sortToken fingerprints the ordering a page was actually produced under, so
// a caller's cursor can be checked against the sort that is CURRENTLY being
// requested and not just trusted to still apply.
//
// It is built from order — the RESOLVED ordering planOrder returns, complete
// with whatever tiebreaker it appended — rather than from req.Sort. req.Sort
// is empty on the ordinary default-sort path (see planOrder's doc comment),
// so hashing it directly would produce the same token for every table's
// default sort; the resolved ordering is the thing that actually determines
// what "after" means for a given cursor, and it is what has to match.
//
// table is folded in for the same reason: two tables can share a column
// name and produce identical order terms, and a token that could not tell
// them apart would let a cursor from one page the other.
//
// crypto/sha256 is used here only for its collision resistance across the
// handful of orderings one table can produce — this is a consistency check
// against an accidental mismatch (the caller's own stale cursor), not a
// security boundary, so the digest is truncated: nothing is lost by a
// shorter token that a legitimate caller could still not have predicted, and
// nothing would be gained by a longer one that only an adversary deliberately
// searching for a collision would care about. A 0 byte separates every
// field fed into the hash, so table "ab" with no sort columns cannot be
// confused with table "a" sorted by a column named "b" — the two would
// otherwise concatenate to the same bytes. The separator itself cannot
// appear inside a field: table is caller text but only ever hashed, never
// executed, and every order[i].expr is either c.Quote(column) or one of the
// three fixed rowid spellings, none of which SQLite identifiers can spell
// with an embedded NUL.
func sortToken(table string, order []orderTerm) string {
	var b strings.Builder
	b.WriteString(databaseName)
	b.WriteByte(0)
	b.WriteString(table)
	for _, t := range order {
		b.WriteByte(0)
		b.WriteString(t.expr)
		b.WriteByte(0)
		if t.desc {
			b.WriteByte('d')
		} else {
			b.WriteByte('a')
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// planOrder resolves the request's sort into a total ordering where it can,
// and reports whether the result can be paged by key.
//
// The resolution is deterministic and depends on nothing the caller is
// expected to remember: req.Sort when given, otherwise the primary key,
// otherwise rowid. That is what lets page two continue page one even though
// BrowseRequest.After may arrive with an empty Sort — the ordinary shape of a
// second page, since a Keyset is opaque to the caller, who only echoes it
// back.
func (c *conn) planOrder(ctx context.Context, req driver.BrowseRequest, cols []schema.Column) ([]orderTerm, bool, error) {
	var order []orderTerm
	// safe tracks whether every ordering column's value survives the trip out
	// through driver.Value and back in as a bound parameter. A column that
	// fails this still gets to ORDER BY — the ordering is what makes offset
	// paging stable — it just cannot carry a keyset.
	safe := true
	add := func(col schema.Column, desc bool) {
		order = append(order, orderTerm{expr: c.Quote(col.Name), desc: desc})
		safe = safe && keysetSafe(col.DataType)
	}

	for _, k := range req.Sort {
		col, ok := columnNamed(cols, k.Column)
		if !ok {
			// Caught here rather than left to SQLite, which reports a bad
			// ORDER BY column as an ordinary syntax error — true of the SQL
			// but useless to a UI that never showed the user any SQL.
			return nil, false, dberr.New(dberr.KindInvalid,
				"browse: no such sort column: "+k.Column)
		}
		add(col, k.Desc)
	}
	if len(req.Sort) == 0 {
		for _, col := range primaryKey(cols) {
			add(col, false)
		}
	}

	alias, aliased := freeRowidAlias(cols)
	switch {
	case aliased && c.hasRowid(ctx, req.Table, alias):
		// rowid is the one column SQLite guarantees unique AND never NULL, so
		// appending it makes ANY ordering total. It is appended even when the
		// sort is already the primary key: a primary key is only a reliable
		// tiebreaker on a WITHOUT ROWID table. On an ordinary table, a
		// PRIMARY KEY column that is not exactly INTEGER PRIMARY KEY accepts
		// NULLs — and more than one of them, a documented SQLite
		// compatibility quirk — so the "unique" key can hold duplicates. The
		// redundancy costs nothing when the key IS the rowid: EXPLAIN QUERY
		// PLAN on `ORDER BY id, rowid` over an INTEGER PRIMARY KEY table
		// reports a plain SCAN, with no sorting step added.
		order = append(order, orderTerm{expr: alias})
	case aliased:
		// No rowid: a view, or a WITHOUT ROWID table. A WITHOUT ROWID table
		// must declare a PRIMARY KEY and SQLite enforces it NOT NULL, so
		// there the primary key IS a sound tiebreaker. A view has no key at
		// all and falls through to offset paging.
		pk := primaryKey(cols)
		if len(pk) == 0 {
			return order, false, nil
		}
		for _, col := range pk {
			if !hasTerm(order, c.Quote(col.Name)) {
				add(col, false)
			}
		}
	default:
		// Every spelling of rowid is shadowed by a real column of that name,
		// so the rowid is unreachable by any expression and nothing else here
		// is guaranteed unique.
		return order, false, nil
	}
	return order, safe, nil
}

// rowidAliases are SQLite's three names for the rowid, in the order this
// driver prefers them. Any of them can be shadowed by a real column of the
// same name, in which case it silently refers to that column instead — which
// is why the first UNSHADOWED one is chosen rather than always "rowid".
var rowidAliases = [...]string{"rowid", "_rowid_", "oid"}

func freeRowidAlias(cols []schema.Column) (string, bool) {
	for _, alias := range rowidAliases {
		if _, taken := columnNamed(cols, alias); !taken {
			return alias, true
		}
	}
	return "", false
}

// hasRowid reports whether alias resolves to a real rowid on this table.
//
// It asks SQLite instead of reading the table's DDL for "WITHOUT ROWID",
// because that text can also appear inside a column name, a CHECK expression
// or a default string, and a regexp over DDL would be guessing where a
// prepared statement can simply know.
//
// The alias is interpolated BARE, never through c.Quote, and that is
// load-bearing: SQLite's legacy double-quoted-string misfeature turns a
// double-quoted identifier that resolves to nothing into a STRING LITERAL
// rather than an error. `SELECT "rowid" FROM a_view` therefore succeeds and
// hands back the four-character text "rowid" for every row — so a quoted
// probe reports every view and every WITHOUT ROWID table as having a rowid,
// and a quoted ORDER BY term would sort by a constant. Verified against
// modernc.org/sqlite@v1.39.0: unquoted, the same probe fails with "no such
// column: rowid", which is the answer this needs. The aliases are fixed
// literals in this file, never caller input, so bare interpolation is safe.
//
// Any failure reads as "no rowid". A probe can only fail because the alias
// does not resolve, or because the connection or context is gone — and in
// that second case the page query issued immediately afterwards fails too,
// carrying its own statement, so nothing is hidden by answering false here.
// The cost of being wrong is offset paging on a table that could have used
// keyset, not a wrong page.
func (c *conn) hasRowid(ctx context.Context, table, alias string) bool {
	rows, err := c.db.QueryContext(ctx,
		"SELECT "+alias+" FROM "+c.Quote(table)+" LIMIT 0")
	if err != nil {
		return false
	}
	_ = rows.Close()
	return true
}

// keysetSafe reports whether a column declared as declType can carry a
// keyset value out through driver.Value and back in as a bound parameter.
//
// Value is text plus a kind tag, so only a value whose text reconstructs the
// original exactly can make the round trip. That maps onto SQLite's column
// affinity, computed here by its five documented rules
// (https://sqlite.org/datatype3.html#determination_of_column_affinity), and
// the two affinities left out are left out for concrete reasons:
//
//   - BLOB affinity (which is also what an undeclared type gets). Bytes have
//     no text form: driver.Normalize renders a non-UTF-8 BLOB as "N bytes",
//     a summary, and binding that summary back compares a TEXT value against
//     BLOB storage. SQLite sorts every BLOB above every TEXT, so `key > '2
//     bytes'` matches every row in the table and the "next" page repeats the
//     page before it, forever.
//   - NUMERIC affinity, which is what DATE, DATETIME and TIMESTAMP all
//     resolve to. modernc.org/sqlite@v1.39.0 converts exactly those three
//     declared types into time.Time (see its Rows.Next), Normalize then
//     renders them as RFC3339Nano, and that is not the text SQLite has
//     stored — binding it back compares against a string the column does not
//     contain.
//
// Both cases page by offset instead, which is correct and merely slower.
func keysetSafe(declType string) bool {
	t := strings.ToUpper(declType)
	switch {
	case strings.Contains(t, "INT"):
		return true // INTEGER
	case strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"):
		return true // TEXT
	case strings.Contains(t, "BLOB"), t == "":
		return false // BLOB
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUB"):
		return true // REAL
	}
	return false // NUMERIC
}

// keysetArg turns one keyset value back into a bound parameter.
//
// A NULL never arrives here: its comparison is expressed structurally, by IS
// NULL and IS NOT NULL, because `col > NULL` evaluates to NULL and would
// quietly drop every row rather than compare it. See afterTerm.
//
// What it accepts is the other half of the promise a Keyset makes. Every
// value this driver hands out has already been screened twice — keysetSafe
// on the column's declared affinity, typeof() on the value's storage class —
// so a cursor this driver issued is always one it accepts back, and anything
// it turns away is a cursor the caller made up.
func keysetArg(v driver.Value) (any, error) {
	switch v.Kind {
	case driver.ValueInt:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return nil, dberr.Wrap(dberr.KindInvalid, "browse: the cursor is not valid", err)
		}
		return n, nil
	case driver.ValueFloat:
		f, err := strconv.ParseFloat(v.Text, 64)
		if err != nil {
			return nil, dberr.Wrap(dberr.KindInvalid, "browse: the cursor is not valid", err)
		}
		return f, nil
	case driver.ValueText:
		return v.Text, nil
	}
	// Bytes have no faithful text form, times are re-rendered rather than
	// echoed, and a kind a future Normalize learns to produce is unknown
	// here by definition. Refusing is the point: binding a value that does
	// not reconstruct the original repeats or skips rows silently.
	return nil, dberr.New(dberr.KindUnsupported,
		"browse: this cursor cannot be paged by key")
}

// keysetPredicate renders "strictly after the row this cursor names", in the
// ordering order describes.
//
// It is an explicit lexicographic chain rather than SQLite's row-value
// comparison, `(a, b) > (?, ?)`, which is the obvious way to write it and is
// wrong here. A row value comparison yields NULL, not true or false, as soon
// as it meets a NULL it cannot decide on — so WHERE drops the row, and a
// sort on a nullable column loses every row past the first page boundary
// that lands in the NULLs. Verified rather than assumed: on a table whose
// sort column is entirely NULL, `WHERE (note, id) > (NULL, 0)` matches zero
// of three rows. The chain below says what row values cannot, that NULL
// sorts before every value ascending and after every value descending, which
// is exactly how SQLite's own ORDER BY treats it.
//
// The chain is the standard one — after the first key, or tied on it and
// after the second, or tied on both and after the third — which costs
// O(n²) terms in the number of sort keys. n is the caller's sort plus one
// tiebreaker, so it is two or three in practice.
func keysetPredicate(order []orderTerm, after []driver.Value) (string, []any, error) {
	clauses := make([]string, len(order))
	var args []any
	for i := range order {
		terms := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			term, arg := equalTerm(order[j], after[j])
			terms, args = append(terms, term), appendArg(args, arg)
		}
		term, arg, err := afterTerm(order[i], after[i])
		if err != nil {
			return "", nil, err
		}
		terms, args = append(terms, term), appendArg(args, arg)
		clauses[i] = "(" + strings.Join(terms, " AND ") + ")"
	}
	return strings.Join(clauses, " OR "), args, nil
}

// equalTerm renders "this column holds exactly the cursor's value".
//
// It cannot fail: every value reaching it was already accepted by afterTerm
// for an earlier position in this same cursor.
func equalTerm(t orderTerm, v driver.Value) (string, any) {
	if v.Kind == driver.ValueNull {
		return t.expr + " IS NULL", nil
	}
	arg, _ := keysetArg(v)
	return t.expr + " = ?", arg
}

// afterTerm renders "this column sorts strictly after the cursor's value",
// in the direction this term is ordered.
func afterTerm(t orderTerm, v driver.Value) (string, any, error) {
	if v.Kind == driver.ValueNull {
		if t.desc {
			// Descending, NULL sorts last, so no row follows it on this term
			// alone. The chain's later clauses still carry the rows tied at
			// NULL, matched by equalTerm's IS NULL and separated by the
			// tiebreaker.
			return "0", nil, nil
		}
		return t.expr + " IS NOT NULL", nil, nil
	}
	arg, err := keysetArg(v)
	if err != nil {
		return "", nil, err
	}
	if t.desc {
		// OR IS NULL because descending puts the NULLs after every value, and
		// `col < ?` alone evaluates to NULL for them and drops them.
		return "(" + t.expr + " < ? OR " + t.expr + " IS NULL)", arg, nil
	}
	return t.expr + " > ?", arg, nil
}

// appendArg keeps the bound parameters in step with the placeholders: a term
// built for a NULL has none.
func appendArg(args []any, arg any) []any {
	if arg == nil {
		return args
	}
	return append(args, arg)
}

func orderBy(order []orderTerm) string {
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, len(order))
	for i, t := range order {
		parts[i] = t.expr + " ASC"
		if t.desc {
			parts[i] = t.expr + " DESC"
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

// columnNamed matches the way SQLite does, which is case-insensitively.
func columnNamed(cols []schema.Column, name string) (schema.Column, bool) {
	for _, col := range cols {
		if strings.EqualFold(col.Name, name) {
			return col, true
		}
	}
	return schema.Column{}, false
}

func primaryKey(cols []schema.Column) []schema.Column {
	var pk []schema.Column
	for _, col := range cols {
		if col.PrimaryKey {
			pk = append(pk, col)
		}
	}
	return pk
}

func hasTerm(order []orderTerm, expr string) bool {
	for _, t := range order {
		if t.expr == expr {
			return true
		}
	}
	return false
}
