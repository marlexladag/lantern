package drivertest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
)

// A conformance suite that cannot fail is worse than none: it certifies
// everything. brokenDriver deliberately violates one invariant at a time, and
// each subtest asserts the suite REPORTS that violation. If you add a check to
// the suite, add its break here.
//
// The want strings are chosen to name the VIOLATION rather than the shape of
// the failure. Two breaks that both end in "the driver returned an error"
// would be indistinguishable, and a suite whose every failure reads the same
// is one step from a suite that passes for the wrong reason.
func TestSuiteFailsADriverThatViolatesEachInvariant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(*brokenDriver)
		want    string
	}{
		{"Open fails", func(d *brokenDriver) { d.failOpen = true }, "open"},
		{"a second connection cannot be opened", func(d *brokenDriver) { d.failSecondOpen = true }, "second connection"},
		{"the fixtures cannot be seeded", func(d *brokenDriver) { d.failSeed = true }, "seeding"},

		{"Introspect fails", func(d *brokenDriver) { d.failIntrospect = true }, "introspect"},
		{"Introspect returns no databases", func(d *brokenDriver) { d.noDatabases = true }, "at least one"},
		{"a database has an empty name", func(d *brokenDriver) { d.emptyDatabaseName = true }, "empty name"},
		{"tables are read eagerly by Introspect", func(d *brokenDriver) { d.eagerTables = true }, "tier one"},
		{"a single-database driver returns several", func(d *brokenDriver) { d.manyDatabases = true }, "multipledatabases"},

		{"Tables fails", func(d *brokenDriver) { d.failTables = true }, "tables"},
		{"Tables returns nil for an empty database", func(d *brokenDriver) { d.nilTables = true }, "nil"},
		{"Tables omits a seeded table", func(d *brokenDriver) { d.hideTables = true }, "lantern_conf_something_else"},
		{"columns are read eagerly by Tables", func(d *brokenDriver) { d.eagerColumns = true }, "tier two"},
		{"Tables ignores its database argument", func(d *brokenDriver) { d.ignoreDatabase = true }, "database"},
		{"an unknown database is refused with the wrong kind", func(d *brokenDriver) { d.wrongKindNotFound = true }, "not_found"},

		{"Columns fails", func(d *brokenDriver) { d.failColumns = true }, "columns"},
		{"Columns returns no columns", func(d *brokenDriver) { d.noColumns = true }, "no columns"},
		{"a column has an empty name", func(d *brokenDriver) { d.emptyColumnName = true }, "empty name"},
		{"an unknown table is accepted", func(d *brokenDriver) { d.acceptUnknownTable = true }, "table"},

		{"Quote returns the identifier unchanged", func(d *brokenDriver) { d.identityQuote = true }, "unchanged"},
		{"the quoted form is not usable in a statement", func(d *brokenDriver) { d.failQuotedStatement = true }, "usable"},
		{"Quote does not round-trip", func(d *brokenDriver) { d.badQuote = true }, "round-trip"},

		{"Ping fails on an open connection", func(d *brokenDriver) { d.failPing = true }, "ping"},
		{"Ping succeeds after Close", func(d *brokenDriver) { d.pingAfterClose = true }, "after close"},

		{"Browse fails", func(d *brokenDriver) { d.failBrowse = true }, "browse"},
		{"Browse returns a nil page and no error", func(d *brokenDriver) { d.nilPage = true }, "nil page"},
		{"a page drops the key column", func(d *brokenDriver) { d.dropKeyColumn = true }, "has no"},
		{"a page's rows are narrower than its columns", func(d *brokenDriver) { d.raggedRows = true }, "row widths"},
		{"a page returns more rows than the limit", func(d *brokenDriver) { d.overLimit = true }, "for a limit of"},
		{"a keyset is issued with no sort token", func(d *brokenDriver) { d.keysetNoToken = true }, "issued a keyset"},
		{"paging never reports exhaustion", func(d *brokenDriver) { d.neverExhaust = true }, "did not terminate"},
		{"a page repeats a row across a boundary", func(d *brokenDriver) { d.repeatRow = true }, "exactly once"},
		{"a page skips a row across a boundary", func(d *brokenDriver) { d.skipRow = true }, "exactly once"},
		{"a page repeats one row and skips another, keeping the count", func(d *brokenDriver) { d.repeatAndSkipRow = true }, "exactly once"},

		{"a cursor is accepted under a different sort", func(d *brokenDriver) { d.acceptAnyToken = true }, "sort"},
		{"a cursor is accepted with no sort token", func(d *brokenDriver) { d.acceptNoToken = true }, "no sort token"},
		{"a cursor is refused under the sort it was issued for", func(d *brokenDriver) { d.refuseOwnCursor = true }, "replaying"},
		{"a bad cursor is refused with the wrong kind", func(d *brokenDriver) { d.wrongKindInvalid = true }, "invalid"},

		{"an empty table returns no columns", func(d *brokenDriver) { d.emptyHasNoColumns = true }, "still has a shape"},
		{"an empty table returns rows", func(d *brokenDriver) { d.emptyHasRows = true }, "there are none"},
		{"an empty table does not report exhaustion", func(d *brokenDriver) { d.emptyNotExhausted = true }, "exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newBrokenDriver()
			tc.breakIt(d)
			fake := runSuite(d)
			if !fake.failed {
				t.Fatal("the suite passed a driver that violates this invariant")
			}
			if !strings.Contains(strings.ToLower(fake.text()), tc.want) {
				t.Errorf("failure message did not mention %q:\n%s", tc.want, fake.text())
			}
		})
	}
}

// The negative control for the table above. If the suite failed every driver
// it would "catch" all thirty-odd violations while testing nothing, which is
// the same vacuous pass in a different costume.
func TestSuitePassesADriverThatViolatesNothing(t *testing.T) {
	fake := runSuite(newBrokenDriver())
	if fake.failed {
		t.Fatalf("the suite failed a driver that honours every invariant:\n%s", fake.text())
	}
}

// Browse is an optional interface (spec section 4), so a driver that cannot
// page a table must reach the end of the suite rather than fail it.
func TestSuiteSkipsPagingForADriverThatCannotBrowse(t *testing.T) {
	d := newBrokenDriver()
	d.noBrowser = true
	fake := runSuite(d)
	if fake.failed {
		t.Fatalf("the suite failed a driver that simply does not implement Browser:\n%s", fake.text())
	}
}

// Keyset and offset paging are alternatives, and a driver that answers with
// an Offset and no Keyset has no cursor to replay — so the two cursor
// invariants have nothing to say about it. Every row still has to come back
// exactly once.
func TestSuiteStillCountsRowsForAnOffsetPagingDriver(t *testing.T) {
	d := newBrokenDriver()
	d.offsetPaging = true
	fake := runSuite(d)
	if fake.failed {
		t.Fatalf("the suite failed a correct offset-paging driver:\n%s", fake.text())
	}

	// And the skip is a skip, not a blind spot: the same driver paging by
	// offset WRONGLY is still caught.
	d = newBrokenDriver()
	d.offsetPaging, d.skipRow = true, true
	if fake := runSuite(d); !fake.failed {
		t.Fatal("the suite passed an offset-paging driver that loses a row")
	}
}

// CREATE TABLE differs enough between engines that a shared literal would be
// a lie, so a driver may supply its own. This asserts the supplied statement
// is the one actually issued — a Fixtures field that were quietly ignored
// would leave the second driver seeding nothing and every paging invariant
// passing over an empty table.
func TestFixturesFromTheConfigAreIssued(t *testing.T) {
	d := newBrokenDriver()
	mine := "CREATE TABLE " + TableRows + " (id INT, val VARCHAR(8)) ENGINE=InnoDB"
	fake := &recordingT{}
	Run(fake, Config{
		Driver: d,
		Open:   d.open,
		DDL:    Fixtures{Create: map[string]string{TableRows: mine}},
	})
	if fake.failed {
		t.Fatalf("suite failed:\n%s", fake.text())
	}
	if !containsString(d.stmts, mine) {
		t.Errorf("the driver's own CREATE TABLE was never issued; got:\n%s", strings.Join(d.stmts, "\n"))
	}
	for _, s := range d.stmts {
		if strings.HasPrefix(s, "CREATE TABLE "+TableRows+" (") && s != mine {
			t.Errorf("the default CREATE TABLE was issued alongside the driver's own: %q", s)
		}
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// recordingT

// errFatal unwinds a Fatalf. *testing.T uses runtime.Goexit, which needs a
// goroutine per subtest to survive; a panic recovered at the same boundary
// testing.T's Goexit is recovered at — the subtest, and the top-level Run —
// gets the same "Fatalf does not return" semantics without one, which is
// what lets these tests run the suite inline.
var errFatal = errors.New("drivertest: fatal")

// recordingT stands in for *testing.T so the suite itself can be tested. It
// satisfies exactly what Run's type parameter requires; that it can is the
// reason Run is defined against an interface at all.
type recordingT struct {
	failed bool
	sb     strings.Builder
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.failed = true
	r.sb.WriteString(strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
	r.sb.WriteByte('\n')
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(errFatal)
}

func (r *recordingT) Run(name string, f func(*recordingT)) bool {
	before := r.failed
	func() {
		defer catchFatal()
		f(r)
	}()
	return r.failed == before
}

func (r *recordingT) text() string { return r.sb.String() }

// catchFatal swallows exactly the sentinel Fatalf raises and nothing else, so
// a genuine panic inside the suite still reaches the test framework instead
// of being reported as an ordinary conformance failure.
func catchFatal() {
	if v := recover(); v != nil && v != errFatal {
		panic(v)
	}
}

// runSuite drives the whole suite against d and returns what it reported.
func runSuite(d *brokenDriver) *recordingT {
	r := &recordingT{}
	func() {
		defer catchFatal()
		Run(r, Config{Driver: d, Open: d.open})
	}()
	return r
}

// ---------------------------------------------------------------------------
// brokenDriver

const brokenDatabase = "fake"

// brokenDriver is an in-memory driver that honours every invariant until one
// of its fields is set. Its rows are the suite's OWN fixture data rather than
// a second copy of it: a fake seeded from a transcription would drift from
// what the suite seeds, and every paging assertion would then be checking the
// transcription.
type brokenDriver struct {
	failOpen          bool
	failSecondOpen    bool
	opens             int
	failSeed          bool
	failIntrospect    bool
	noDatabases       bool
	emptyDatabaseName bool
	eagerTables       bool
	manyDatabases     bool

	failTables        bool
	nilTables         bool
	hideTables        bool
	eagerColumns      bool
	ignoreDatabase    bool
	wrongKindNotFound bool

	failColumns        bool
	noColumns          bool
	emptyColumnName    bool
	acceptUnknownTable bool

	identityQuote       bool
	failQuotedStatement bool
	badQuote            bool

	failPing       bool
	pingAfterClose bool

	noBrowser    bool
	offsetPaging bool

	failBrowse       bool
	nilPage          bool
	dropKeyColumn    bool
	raggedRows       bool
	repeatAndSkipRow bool
	overLimit        bool
	keysetNoToken    bool
	neverExhaust     bool
	repeatRow        bool
	skipRow          bool

	acceptAnyToken   bool
	acceptNoToken    bool
	refuseOwnCursor  bool
	wrongKindInvalid bool

	emptyHasNoColumns bool
	emptyHasRows      bool
	emptyNotExhausted bool

	// stmts records every statement the suite issued, so a test can assert
	// which DDL was used.
	stmts []string
}

func newBrokenDriver() *brokenDriver { return &brokenDriver{} }

func (d *brokenDriver) ID() string { return "broken" }

func (d *brokenDriver) Capabilities() driver.Capabilities {
	return driver.Capabilities{MultipleDatabases: false}
}

func (d *brokenDriver) RequiredFields(driver.ConnConfig) []string { return nil }

func (d *brokenDriver) open(TestingT) driver.ConnConfig {
	return driver.ConnConfig{Driver: d.ID()}
}

func (d *brokenDriver) Open(context.Context, driver.ConnConfig) (driver.Conn, error) {
	d.opens++
	if d.failOpen || (d.failSecondOpen && d.opens > 1) {
		return nil, dberr.New(dberr.KindNetwork, "broken: refusing to open")
	}
	c := &brokenConn{d: d}
	if d.noBrowser {
		return noBrowseConn{c}, nil
	}
	return c, nil
}

func (d *brokenDriver) notFound(msg string) error {
	if d.wrongKindNotFound {
		return dberr.New(dberr.KindUnknown, msg)
	}
	return dberr.New(dberr.KindNotFound, msg)
}

func (d *brokenDriver) invalid(msg string) error {
	if d.wrongKindInvalid {
		return dberr.New(dberr.KindUnknown, msg)
	}
	return dberr.New(dberr.KindInvalid, msg)
}

type brokenConn struct {
	d      *brokenDriver
	closed bool
}

func (c *brokenConn) Ping(context.Context) error {
	switch {
	case c.d.failPing:
		return dberr.New(dberr.KindNetwork, "broken: ping")
	case c.closed && !c.d.pingAfterClose:
		return dberr.New(dberr.KindNetwork, "broken: connection is closed")
	}
	return nil
}

func (c *brokenConn) Close() error {
	c.closed = true
	return nil
}

// Quote doubles an embedded quote, the way SQLite and PostgreSQL do.
func (c *brokenConn) Quote(ident string) string {
	if c.d.identityQuote {
		return ident
	}
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func unquote(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	return strings.ReplaceAll(s, `""`, `"`)
}

func (c *brokenConn) Introspect(context.Context) (*schema.Catalog, error) {
	if c.d.failIntrospect {
		return nil, dberr.New(dberr.KindNetwork, "broken: introspect")
	}
	if c.d.noDatabases {
		return &schema.Catalog{}, nil
	}
	db := schema.Database{Name: brokenDatabase}
	if c.d.emptyDatabaseName {
		db.Name = ""
	}
	if c.d.eagerTables {
		db.Tables = []schema.Table{}
	}
	cat := &schema.Catalog{Databases: []schema.Database{db}}
	if c.d.manyDatabases {
		cat.Databases = append(cat.Databases, schema.Database{Name: "other"})
	}
	return cat, nil
}

func (c *brokenConn) knownDatabase(name string) bool {
	if c.d.ignoreDatabase {
		return true
	}
	if c.d.emptyDatabaseName && name == "" {
		return true
	}
	return strings.EqualFold(name, brokenDatabase) ||
		(c.d.manyDatabases && strings.EqualFold(name, "other"))
}

func (c *brokenConn) Tables(_ context.Context, database string) ([]schema.Table, error) {
	if c.d.failTables {
		return nil, dberr.New(dberr.KindNetwork, "broken: tables")
	}
	if !c.knownDatabase(database) {
		return nil, c.d.notFound("broken: no such database: " + database)
	}
	if c.d.nilTables {
		return nil, nil
	}
	out := []schema.Table{}
	if strings.EqualFold(database, "other") {
		return out, nil
	}
	if c.d.hideTables {
		// A table, just not the seeded ones: a driver that returned nothing
		// at all would be caught by the nil check instead.
		return append(out, schema.Table{Name: "lantern_conf_something_else"}), nil
	}
	for _, fx := range fixtures {
		t := schema.Table{Name: fx.name, Kind: schema.TableKindTable}
		if c.d.eagerColumns {
			t.Columns = brokenColumns()
		}
		out = append(out, t)
	}
	return out, nil
}

func brokenColumns() []schema.Column {
	return []schema.Column{
		{Name: KeyColumn, DataType: "INTEGER", PrimaryKey: true, Position: 0},
		{Name: ValueColumn, DataType: "TEXT", Nullable: true, Position: 1},
	}
}

func (c *brokenConn) Columns(_ context.Context, _, table string) ([]schema.Column, error) {
	if c.d.failColumns {
		return nil, dberr.New(dberr.KindNetwork, "broken: columns")
	}
	if _, ok := fixtureNamed(table); !ok && !c.d.acceptUnknownTable {
		return nil, c.d.notFound("broken: no such table: " + table)
	}
	if c.d.noColumns {
		return []schema.Column{}, nil
	}
	cols := brokenColumns()
	if c.d.emptyColumnName {
		cols[0].Name = ""
	}
	return cols, nil
}

// Query records what it was asked to run. The fixture statements are not
// executed — the tables are already in memory — but a driver that refuses
// them still has to look like one, which is what failSeed models.
func (c *brokenConn) Query(_ context.Context, stmt string, _ ...any) (driver.Cursor, error) {
	c.d.stmts = append(c.d.stmts, stmt)
	if alias, ok := strings.CutPrefix(stmt, "SELECT 1 AS "); ok {
		if c.d.failQuotedStatement {
			return nil, dberr.New(dberr.KindSyntax, "broken: cannot parse "+alias)
		}
		name := unquote(alias)
		if c.d.badQuote {
			name = alias
		}
		return &brokenCursor{meta: []driver.ColumnMeta{{Name: name}}}, nil
	}
	if c.d.failSeed {
		return nil, dberr.New(dberr.KindSyntax, "broken: refusing "+stmt)
	}
	return &brokenCursor{}, nil
}

type brokenCursor struct{ meta []driver.ColumnMeta }

func (c *brokenCursor) Columns() []driver.ColumnMeta                    { return c.meta }
func (c *brokenCursor) Next(context.Context, int) ([]driver.Row, error) { return nil, nil }
func (c *brokenCursor) Close() error                                    { return nil }

// noBrowseConn forwards Conn and deliberately does not promote Browse, so the
// suite sees a driver whose type assertion to driver.Browser fails.
type noBrowseConn struct{ c *brokenConn }

func (n noBrowseConn) Ping(ctx context.Context) error { return n.c.Ping(ctx) }
func (n noBrowseConn) Close() error                   { return n.c.Close() }
func (n noBrowseConn) Quote(ident string) string      { return n.c.Quote(ident) }
func (n noBrowseConn) Introspect(ctx context.Context) (*schema.Catalog, error) {
	return n.c.Introspect(ctx)
}
func (n noBrowseConn) Tables(ctx context.Context, db string) ([]schema.Table, error) {
	return n.c.Tables(ctx, db)
}
func (n noBrowseConn) Columns(ctx context.Context, db, t string) ([]schema.Column, error) {
	return n.c.Columns(ctx, db, t)
}
func (n noBrowseConn) Query(ctx context.Context, s string, args ...any) (driver.Cursor, error) {
	return n.c.Query(ctx, s, args...)
}

// ---------------------------------------------------------------------------
// brokenConn.Browse

func (c *brokenConn) Browse(_ context.Context, req driver.BrowseRequest) (*driver.BrowsePage, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if c.d.failBrowse {
		return nil, dberr.New(dberr.KindNetwork, "broken: browse")
	}
	if c.d.nilPage {
		return nil, nil
	}
	fx, ok := fixtureNamed(req.Table)
	if !ok {
		return nil, c.d.notFound("broken: no such table: " + req.Table)
	}
	if !c.knownDatabase(req.Database) {
		return nil, c.d.notFound("broken: no such database: " + req.Database)
	}

	order := brokenOrder(req.Sort)
	token := brokenToken(req.Database, req.Table, order)
	if len(req.After) > 0 {
		switch {
		case req.SortToken == "":
			if !c.d.acceptNoToken {
				return nil, c.d.invalid("broken: the cursor carries no sort token")
			}
		case req.SortToken != token && !c.d.acceptAnyToken:
			return nil, c.d.invalid("broken: the cursor was issued for a different sort")
		}
		if c.d.refuseOwnCursor {
			return nil, c.d.invalid("broken: refusing every cursor")
		}
	}

	rows := append([]fixtureRow(nil), fx.rows...)
	sort.SliceStable(rows, func(i, j int) bool { return brokenLess(rows[i], rows[j], order) })

	start, continuing := 0, false
	if c.d.offsetPaging {
		start, continuing = req.Offset, req.Offset > 0
	} else if len(req.After) > 0 {
		start, continuing = brokenSeek(rows, order, req.After, c.d.repeatRow), true
	}
	// Losing a row is a property of the BOUNDARY, so it only bites on a
	// continuation — which is as true of an offset page as of a keyset one.
	if c.d.skipRow && continuing {
		start++
	}
	end := min(start+req.Limit, len(rows))
	if c.d.overLimit {
		end = min(end+1, len(rows))
	}
	taken := rows[min(start, len(rows)):max(end, min(start, len(rows)))]

	page := &driver.BrowsePage{Columns: []driver.ColumnMeta{
		{Name: KeyColumn, DataType: "INTEGER"},
		{Name: ValueColumn, DataType: "TEXT"},
	}}
	if c.d.dropKeyColumn {
		page.Columns = page.Columns[1:]
	}
	if c.d.repeatAndSkipRow && continuing && len(taken) > 0 {
		// The boundary row comes back AND the row after it is lost, so the
		// count still adds up and only the multiset gives it away.
		taken[0] = rows[start-1]
	}
	for _, r := range taken {
		cells := []driver.Value{brokenValue(r, KeyColumn), brokenValue(r, ValueColumn)}
		switch {
		case c.d.dropKeyColumn:
			cells = cells[1:]
		case c.d.raggedRows:
			// Narrower than the page says it is, with the columns left alone.
			cells = cells[:1]
		}
		page.Rows = append(page.Rows, cells)
	}
	page.Exhausted = len(taken) < req.Limit

	if len(fx.rows) == 0 {
		if c.d.emptyHasNoColumns {
			page.Columns = nil
		}
		if c.d.emptyHasRows {
			page.Rows = [][]driver.Value{{brokenValue(fixtureRow{key: 1}, KeyColumn), brokenValue(fixtureRow{null: true}, ValueColumn)}}
		}
		if c.d.emptyNotExhausted {
			page.Exhausted = false
		}
	}
	if c.d.neverExhaust {
		page.Exhausted = false
	}

	switch {
	case c.d.offsetPaging:
		page.Offset = start + len(taken)
	case !page.Exhausted && len(taken) > 0:
		last := taken[len(taken)-1]
		for _, col := range order {
			page.Keyset = append(page.Keyset, brokenValue(last, col.Column))
		}
		page.SortToken = token
		if c.d.keysetNoToken {
			page.SortToken = ""
		}
	}
	return page, nil
}

// brokenOrder resolves a request's sort the way a real driver must: the
// caller's terms, then the unique key appended so the ordering is TOTAL.
func brokenOrder(req []driver.SortKey) []driver.SortKey {
	order := append([]driver.SortKey(nil), req...)
	for _, k := range order {
		if k.Column == KeyColumn {
			return order
		}
	}
	return append(order, driver.SortKey{Column: KeyColumn})
}

func brokenToken(database, table string, order []driver.SortKey) string {
	var b strings.Builder
	b.WriteString(database + "\x00" + table)
	for _, k := range order {
		b.WriteString("\x00" + k.Column + strconv.FormatBool(k.Desc))
	}
	return b.String()
}

func brokenValue(r fixtureRow, column string) driver.Value {
	if column == KeyColumn {
		return driver.Value{Kind: driver.ValueInt, Text: strconv.Itoa(r.key)}
	}
	if r.null {
		return driver.Value{Kind: driver.ValueNull}
	}
	return driver.Value{Kind: driver.ValueText, Text: r.val}
}

// brokenCmp orders two rows on one term. NULL sorts before every value
// ascending and after every value descending, which is what SQLite and MySQL
// both do and what keyset.Predicate is written against.
func brokenCmp(a, b fixtureRow, k driver.SortKey) int {
	var c int
	switch {
	case k.Column == KeyColumn:
		c = a.key - b.key
	case a.null && b.null:
		c = 0
	case a.null:
		c = -1
	case b.null:
		c = 1
	default:
		c = strings.Compare(a.val, b.val)
	}
	if k.Desc {
		return -c
	}
	return c
}

func brokenLess(a, b fixtureRow, order []driver.SortKey) bool {
	for _, k := range order {
		if c := brokenCmp(a, b, k); c != 0 {
			return c < 0
		}
	}
	return false
}

// brokenSeek finds the first row strictly after the cursor. inclusive is the
// classic off-by-one — ">=" where the predicate must say ">" — and is what
// repeats the boundary row on every page.
func brokenSeek(rows []fixtureRow, order []driver.SortKey, after []driver.Value, inclusive bool) int {
	cursor := fixtureRow{}
	for i, k := range order {
		if i >= len(after) {
			break
		}
		switch {
		case k.Column == KeyColumn:
			cursor.key, _ = strconv.Atoi(after[i].Text)
		case after[i].Kind == driver.ValueNull:
			cursor.null = true
		default:
			cursor.val = after[i].Text
		}
	}
	for i, r := range rows {
		c := 0
		for _, k := range order {
			if c = brokenCmp(r, cursor, k); c != 0 {
				break
			}
		}
		if c > 0 || (inclusive && c == 0) {
			return i
		}
	}
	return len(rows)
}
