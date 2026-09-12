// Package drivertest is the conformance suite every driver must pass.
//
// Spec section 13 calls it the primary mechanism keeping many drivers honest,
// and honesty is the whole point: the browse work established a set of
// properties — every row exactly once, laziness in two tiers, a cursor that
// cannot be replayed under a sort it was not issued for — which a second
// driver would otherwise be free to break, quietly, in its own dialect. The
// suite exists BEFORE the second driver so those properties are inherited
// rather than re-argued.
//
// It is BLACK BOX. It drives a driver through internal/engine/driver's public
// interfaces and nothing else, and it must never import
// internal/engine/driver/keyset: a suite that reached into the shared keyset
// machinery would be testing an implementation two drivers happen to share,
// and would pass for a driver that shared the code while getting the
// behaviour wrong — and it would have nothing to say about a driver that
// reaches the same behaviour another way. What is asserted here is only what
// a caller can see.
//
// A driver's own white-box tests are not replaced by this. Coverage is
// measured per package with no -coverpkg, so nothing here contributes a line
// of coverage to sqlite or keyset; deleting a driver's tests on the theory
// that conformance covers them would delete both the tests and the coverage.
package drivertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
)

// TestingT is the reporting half of *testing.T.
//
// The suite is written against an interface rather than *testing.T so that it
// can be tested itself: conformance_test.go runs the whole suite against
// drivers that violate one invariant each and asserts the violation is
// reported. A suite that cannot be made to fail certifies everything, and
// there is no way to observe a *testing.T failing without failing the test
// that owns it.
type TestingT interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// SuiteT adds subtest grouping, which cannot be expressed in TestingT: a
// subtest's function takes the SAME type it was started from, and *testing.T
// spells that *testing.T. Naming it as a type parameter is what lets Run take
// a plain *testing.T with no adapter at the call site while still accepting a
// recording stand-in.
type SuiteT[T any] interface {
	TestingT
	Run(name string, f func(T)) bool
}

// Fixtures carries the DDL the suite seeds with, in the engine's own dialect.
//
// Only CREATE statements are here. The rows are not: the suite has to KNOW exactly
// what it seeded, because "every row exactly once" is a claim about a known
// multiset, so it renders its own INSERT statements from data it holds and
// keeps them to the literals every SQL dialect spells identically (integers,
// short single-quoted ASCII, NULL). CREATE TABLE is the part engines genuinely
// disagree about, and a shared literal there would be a lie.
//
// A driver whose dialect accepts the default says nothing. One that does not
// supplies its own, keyed by table name, and must keep the column names and
// nullability the suite asserts on: KeyColumn is a unique, non-null integer
// key and ValueColumn is a nullable text column. TableView must stay a VIEW
// over TableRows — see its own comment for what it is there to reach.
type Fixtures struct {
	Create map[string]string
}

func (f Fixtures) create(fx fixture) string {
	if stmt, ok := f.Create[fx.name]; ok {
		return stmt
	}
	if fx.viewOf != "" {
		return "CREATE VIEW " + fx.name + " AS SELECT " +
			KeyColumn + ", " + ValueColumn + " FROM " + fx.viewOf
	}
	return "CREATE TABLE " + fx.name + " (" +
		KeyColumn + " INTEGER PRIMARY KEY, " + ValueColumn + " TEXT)"
}

// Config is one driver's entry into the suite.
type Config struct {
	Driver driver.Driver
	// Open returns a connection config pointing at a database the suite may
	// create tables in. It is called more than once — each call must yield a
	// usable connection, and for a file-backed engine that means creating the
	// file, since a driver is entitled to refuse one that does not exist.
	Open func(TestingT) driver.ConnConfig
	// Database names the database the connection Open describes is pointed
	// at: the one seed's UNQUALIFIED CREATE TABLE statements land in, and
	// therefore the one every later check has to name when it asks for the
	// fixtures back.
	//
	// It is REQUIRED of any driver whose engine reports more than one, and
	// the fallback below says why. Left empty, the suite takes the FIRST
	// database Introspect reported, which is correct for an engine that has
	// exactly one and correct for nothing else: MySQL's SHOW DATABASES and
	// information_schema.SCHEMATA both answer in NAME order, so
	// information_schema leads and the fixtures are somewhere else entirely.
	// A suite that guessed would report thirty failures that all read like
	// "the driver lost the fixtures" for a driver that did nothing wrong.
	//
	// It is a field rather than something read off ConnConfig because a
	// driver is free to carry the database anywhere — ConnConfig.Database, a
	// DSN option, a file path — and the suite does not get to assume which.
	Database string
	DDL      Fixtures
	// Observe, when non-nil, is called once for every page the suite asks a
	// CONTINUATION of, naming the pagination the driver chose for it.
	//
	// It exists so a driver's own test can assert that its fallback is
	// actually REACHED through the suite. Keyset and offset are alternatives
	// and the suite follows whichever the driver picks, so a fixture set that
	// stopped holding anything a given driver cannot page by key would turn
	// that driver's offset path back into dead code — silently, with every
	// check still green, which is how it stood before TableView was added.
	// SQLite's own conformance test asserts both paths ran.
	Observe func(PagingPath)
}

// PagingPath names one of the two paginations a driver may answer with. They
// are ALTERNATIVES: a page carries a Keyset or an Offset, and which one it
// carries is the driver saying whether this table can be paged by key.
type PagingPath string

const (
	PathKeyset PagingPath = "keyset"
	PathOffset PagingPath = "offset"
)

// The tables the suite seeds. Exported so a driver supplying its own DDL
// writes the same names.
const (
	TableRows  = "lantern_conf_rows"
	TableTies  = "lantern_conf_ties"
	TableNulls = "lantern_conf_nulls"
	TableEmpty = "lantern_conf_empty"
	// TableView is a VIEW over TableRows, and it is here to be the thing a
	// driver CANNOT page by key. SQLite has no rowid for a view and no
	// primary key to fall back on, so it takes the offset path — which
	// without this fixture was dead code against every real driver, exercised
	// only by the suite's own fake. MySQL's view fallback would have been
	// uncovered the same way.
	TableView = "lantern_conf_view"

	// KeyColumn is unique and never null: it is what "every row exactly once"
	// is counted by, and what a driver is expected to fall back on as its
	// tiebreaker.
	KeyColumn = "id"
	// ValueColumn is nullable and holds duplicates. It is what the sort
	// invariants sort by, so a driver that cannot form a total order without
	// a tiebreaker fails them.
	ValueColumn = "val"
)

const (
	unknownDatabase = "lantern_conf_no_such_database"
	unknownTable    = "lantern_conf_no_such_table"
	// tieGroup is how many rows of TableTies share a value. Page sizes are
	// derived from it below rather than listed, so a boundary is guaranteed to
	// land inside a tie group however the fixture is edited: a fixture whose
	// groups never straddle a boundary passes without a tiebreaker at all,
	// which is the one thing these invariants exist to catch.
	tieGroup = 3
)

type fixtureRow struct {
	key int
	val string
	// null distinguishes a NULL from the empty string. They are different
	// rows to sort and different cursors to carry, and a driver that
	// collapses them repeats or drops a page.
	null bool
}

type fixture struct {
	name string
	rows []fixtureRow
	// viewOf names the table this fixture selects from, when it is a VIEW
	// rather than a table. A view is seeded by its CREATE alone — the rows
	// beneath it are already there — and its expected multiset is the source
	// table's, taken by reference below rather than transcribed.
	viewOf string
}

// fixtures is the whole of the suite's seed data.
var fixtures = withViewRows([]fixture{
	{name: TableRows, rows: distinctRows(7)},
	{name: TableTies, rows: tiedRows(3, tieGroup)},
	{name: TableNulls, rows: nullableRows(8)},
	{name: TableEmpty},
	// Last, so the table it selects from exists by the time it is created.
	{name: TableView, viewOf: TableRows},
})

// withViewRows gives every view the rows of the table beneath it. Taken from
// the source rather than written out again: "every row exactly once" is a
// claim about a known multiset, and a transcription would drift the moment
// the source fixture is edited — leaving the paging invariants checking the
// transcription, which is the failure Fixtures' own comment warns about one
// level up.
func withViewRows(fx []fixture) []fixture {
	for i := range fx {
		if fx[i].viewOf == "" {
			continue
		}
		for _, src := range fx {
			if src.name == fx[i].viewOf {
				fx[i].rows = src.rows
			}
		}
	}
	return fx
}

func distinctRows(n int) []fixtureRow {
	out := make([]fixtureRow, n)
	for i := range out {
		out[i] = fixtureRow{key: i + 1, val: fmt.Sprintf("r%d", i+1)}
	}
	return out
}

func tiedRows(groups, per int) []fixtureRow {
	out := make([]fixtureRow, 0, groups*per)
	for g := range groups {
		for range per {
			out = append(out, fixtureRow{key: len(out) + 1, val: fmt.Sprintf("g%d", g+1)})
		}
	}
	return out
}

// nullableRows alternates so the NULLs form a run of n/2 once sorted: a page
// size smaller than that run puts a boundary inside it, which is where a
// driver that compares NULL with an ordinary operator loses every row past
// the boundary.
func nullableRows(n int) []fixtureRow {
	out := make([]fixtureRow, n)
	for i := range out {
		out[i] = fixtureRow{key: i + 1, val: fmt.Sprintf("n%d", i+1), null: i%2 == 0}
	}
	return out
}

func fixtureNamed(name string) (fixture, bool) {
	for _, fx := range fixtures {
		if fx.name == name {
			return fx, true
		}
	}
	return fixture{}, false
}

// insert renders one INSERT per row. Per row rather than one multi-row
// VALUES: the multi-row form is portable enough in practice, but a failure
// then names a whole batch instead of the row that was refused.
//
// A view gets none: its rows are the source table's, already inserted, and
// an INSERT into a view is a different feature with a different answer in
// every dialect.
func (fx fixture) insert() []string {
	if fx.viewOf != "" {
		return nil
	}
	out := make([]string, len(fx.rows))
	for i, r := range fx.rows {
		val := "NULL"
		if !r.null {
			val = "'" + r.val + "'"
		}
		out[i] = fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES (%d, %s)",
			fx.name, KeyColumn, ValueColumn, r.key, val)
	}
	return out
}

// keys is the multiset every walk of this table must return, as the text a
// driver.Value carries.
func (fx fixture) keys() []string {
	out := make([]string, len(fx.rows))
	for i, r := range fx.rows {
		out[i] = strconv.Itoa(r.key)
	}
	return out
}

type pagingCase struct {
	name  string
	table string
	sort  []driver.SortKey
	// pages are the page sizes this table is walked at. One divides the row
	// count exactly and one does not, because the two exercise different ends
	// of the exhaustion condition: an exact division ends with a full page
	// followed by an empty one, an inexact division with a short page.
	pages []int
}

var ascending = []driver.SortKey{{Column: ValueColumn}}
var descending = []driver.SortKey{{Column: ValueColumn, Desc: true}}
var byKey = []driver.SortKey{{Column: KeyColumn}}

var pagingCases = []pagingCase{
	{name: "default_sort", table: TableRows, pages: []int{2, 3, 7}},
	{name: "sorted_by_value", table: TableRows, sort: ascending, pages: []int{2, 3, 7}},
	// tieGroup-1 and tieGroup+1 both put a boundary inside a group of equal
	// values; tieGroup itself aligns with them, which is the case that passes
	// without a tiebreaker and is here to be told apart from the others.
	{name: "ties_ascending", table: TableTies, sort: ascending, pages: []int{tieGroup - 1, tieGroup, tieGroup + 1, 3 * tieGroup}},
	{name: "ties_descending", table: TableTies, sort: descending, pages: []int{tieGroup - 1, tieGroup, tieGroup + 1, 3 * tieGroup}},
	// 3 lands inside the run of four NULLs ascending; 5 lands inside it
	// descending, where the run is at the far end.
	{name: "nulls_ascending", table: TableNulls, sort: ascending, pages: []int{3, 5, 8}},
	{name: "nulls_descending", table: TableNulls, sort: descending, pages: []int{3, 5, 8}},
	// The view, sorted by the key so the ordering is TOTAL without a
	// tiebreaker: a driver that falls back to offset paging here has no
	// keyset to make it total with, and an offset page over an ordering that
	// is not total loses rows for reasons that have nothing to do with the
	// driver.
	{name: "view_sorted_by_key", table: TableView, sort: byKey, pages: []int{2, 3, 7}},
}

// Run drives cfg's driver through every invariant the suite knows.
//
// It is generic over T so *testing.T satisfies it directly — a subtest's
// function takes the type it was started from, which no plain interface can
// name — and so the suite's own test can pass a recorder in its place.
func Run[T SuiteT[T]](t T, cfg Config) {
	t.Helper()
	ctx := context.Background()

	conn := connect(ctx, t, cfg)
	defer func() { _ = conn.Close() }()
	seed(ctx, t, conn, cfg.DDL)

	cat, err := conn.Introspect(ctx)
	if err != nil {
		t.Fatalf("Introspect on a connection that has just been used: %v", err)
	}
	if len(cat.Databases) == 0 {
		t.Fatalf("Introspect returned no databases; a driver must report at least one, " +
			"since the UI has nothing to draw under the connection otherwise")
	}
	// Introspect is checked BEFORE the fixture database is resolved, because
	// resolving it can abort the run — a driver reporting several databases
	// and naming none leaves the suite nothing to look in — and the catalog's
	// own invariants are exactly what a caller needs to see in that case.
	t.Run("introspect", func(t T) { checkIntrospect(t, cfg.Driver, cat) })
	database := fixtureDatabase(t, cfg, cat)

	t.Run("tables", func(t T) { checkTables(ctx, t, conn, cat, database) })
	t.Run("columns", func(t T) { checkColumns(ctx, t, conn, database) })
	t.Run("quote", func(t T) { checkQuote(ctx, t, conn) })
	t.Run("ping", func(t T) { checkPing(ctx, t, cfg) })
	t.Run("cursor_exhaustion", func(t T) { checkCursorExhaustion(ctx, t, conn) })
	t.Run("required_fields", func(t T) { checkRequiredFields(t, cfg) })
	t.Run("read_only", func(t T) { checkReadOnly(ctx, t, cfg, database) })

	// Browser is optional (spec section 4), discovered by type assertion so a
	// driver that cannot page a table is never forced to stub it.
	br, ok := conn.(driver.Browser)
	if !ok {
		return
	}
	observe := cfg.Observe
	if observe == nil {
		observe = func(PagingPath) {}
	}
	t.Run("paging", func(t T) {
		for _, c := range pagingCases {
			t.Run(c.name, func(t T) { checkPaging(ctx, t, br, database, c, observe) })
		}
	})
	t.Run("cursor", func(t T) { checkCursor(ctx, t, br, database) })
	t.Run("empty_table", func(t T) { checkEmptyTable(ctx, t, br, database) })
}

// fixtureDatabase resolves the database the fixtures were seeded into.
//
// Config.Database when the driver named one, and the first database
// Introspect reported otherwise. The fallback is only ever right for an
// engine with exactly one database (see Config.Database), so a driver that
// reports several and names none is told so here rather than left to read
// thirty downstream failures.
func fixtureDatabase(t TestingT, cfg Config, cat *schema.Catalog) string {
	t.Helper()
	if cfg.Database == "" {
		if len(cat.Databases) > 1 {
			t.Fatalf("Introspect reported %d databases (%v) and Config.Database names none; "+
				"the suite would look for its fixtures in %q simply because it is listed first",
				len(cat.Databases), databaseNames(cat.Databases), cat.Databases[0].Name)
		}
		return cat.Databases[0].Name
	}
	for _, db := range cat.Databases {
		if strings.EqualFold(db.Name, cfg.Database) {
			return cfg.Database
		}
	}
	t.Fatalf("Config.Database is %q but Introspect reported %v; the suite would look for its "+
		"fixtures in a database the driver does not admit to having",
		cfg.Database, databaseNames(cat.Databases))
	return ""
}

func databaseNames(dbs []schema.Database) []string {
	out := make([]string, len(dbs))
	for i, db := range dbs {
		out[i] = db.Name
	}
	return out
}

func connect(ctx context.Context, t TestingT, cfg Config) driver.Conn {
	t.Helper()
	conn, err := cfg.Driver.Open(ctx, cfg.Open(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return conn
}

// seed creates the fixture tables and fills them, through Conn.Query — the
// one way into a driver that takes a statement, and therefore the only way a
// suite that knows no dialect can write anything at all.
func seed(ctx context.Context, t TestingT, conn driver.Conn, ddl Fixtures) {
	t.Helper()
	for _, fx := range fixtures {
		for _, stmt := range append([]string{ddl.create(fx)}, fx.insert()...) {
			cur, err := conn.Query(ctx, stmt)
			if err != nil {
				t.Fatalf("seeding the fixtures, %q: %v", stmt, err)
			}
			_ = cur.Close()
		}
	}
}

// checkIntrospect is laziness, tier one: connecting reads the database list
// and nothing below it.
func checkIntrospect(t TestingT, d driver.Driver, cat *schema.Catalog) {
	t.Helper()
	for i, db := range cat.Databases {
		if db.Name == "" {
			t.Errorf("Introspect returned database %d with an empty name; "+
				"every later call names a database, so an unnamed one is unreachable", i)
		}
		if db.Tables != nil {
			t.Errorf("Introspect read database %q's table list eagerly (%d entries); "+
				"tier one reads the database list ONLY — a server with forty schemas of two "+
				"thousand tables makes connecting the slow call — so Tables must stay nil "+
				"until Conn.Tables is called", db.Name, len(db.Tables))
		}
	}
	if !d.Capabilities().MultipleDatabases && len(cat.Databases) > 1 {
		t.Errorf("the driver reports Capabilities.MultipleDatabases=false but Introspect "+
			"returned %d databases; the sidebar branches on that bit and draws tables "+
			"directly under the connection when it is false, so every database after the "+
			"first would be flattened into one unlabelled list", len(cat.Databases))
	}
}

// checkTables is laziness, tier two, and the database argument being real.
func checkTables(ctx context.Context, t TestingT, conn driver.Conn, cat *schema.Catalog, database string) {
	t.Helper()
	for _, db := range cat.Databases {
		tables, err := conn.Tables(ctx, db.Name)
		if !succeeded(t, err, "Tables(%q), a database Introspect itself reported", db.Name) {
			continue
		}
		if tables == nil {
			t.Errorf("Tables(%q) returned nil; a database with no tables must return a "+
				"non-nil empty slice, because nil marshals to the JSON literal null and "+
				"the shell declares an array", db.Name)
			continue
		}
		for _, tbl := range tables {
			if tbl.Columns != nil {
				t.Errorf("Tables(%q) read table %q's columns eagerly (%d of them); "+
					"tier two reads the table list only, and columns load on expand",
					db.Name, tbl.Name, len(tbl.Columns))
			}
		}
		if db.Name != database {
			continue
		}
		for _, fx := range fixtures {
			if !hasTable(tables, fx.name) {
				t.Errorf("Tables(%q) did not list the seeded table %q; it returned %v",
					db.Name, fx.name, tableNames(tables))
			}
		}
	}

	// The positive control for this rejection is the loop above: Tables
	// answered for a database that exists, so refusing one that does not is
	// the driver reading its argument rather than refusing everything.
	_, err := conn.Tables(ctx, unknownDatabase)
	refused(t, err, dberr.KindNotFound,
		"Tables(%q), naming a database that does not exist", unknownDatabase)
}

func hasTable(tables []schema.Table, name string) bool {
	for _, tbl := range tables {
		if strings.EqualFold(tbl.Name, name) {
			return true
		}
	}
	return false
}

func tableNames(tables []schema.Table) []string {
	out := make([]string, len(tables))
	for i, tbl := range tables {
		out[i] = tbl.Name
	}
	return out
}

// checkColumns is the third tier, read on expand.
func checkColumns(ctx context.Context, t TestingT, conn driver.Conn, database string) {
	t.Helper()
	for _, fx := range fixtures {
		cols, err := conn.Columns(ctx, database, fx.name)
		if !succeeded(t, err, "Columns(%q, %q)", database, fx.name) {
			continue
		}
		if len(cols) == 0 {
			t.Errorf("Columns(%q, %q) returned no columns for a table the suite "+
				"created with two", database, fx.name)
			continue
		}
		for i, col := range cols {
			if col.Name == "" {
				t.Errorf("Columns(%q, %q) returned column %d with an empty name",
					database, fx.name, i)
			}
		}
	}

	_, err := conn.Columns(ctx, database, unknownTable)
	refused(t, err, dberr.KindNotFound,
		"Columns(%q, %q), naming a table that does not exist", database, unknownTable)

	// Invariant 12. Columns takes a database and must honour it, exactly as
	// Tables does. A driver that declares the parameter and ignores it passes
	// every test a single-database engine can write — which is how the same
	// hole reached production in Tables and was only found by writing this
	// suite. On an engine where two schemas hold same-named tables, ignoring
	// it means answering about whichever table the connection's current
	// schema resolves to, with no error and no way for a caller to tell.
	_, err = conn.Columns(ctx, unknownDatabase, fixtures[0].name)
	refused(t, err, dberr.KindNotFound,
		"Columns(%q, %q), naming a database that does not exist while the TABLE does",
		unknownDatabase, fixtures[0].name)
}

// quoteProbe is quoted once to discover what this engine's quote character
// is, so the round-trip below can be built out of the engine's OWN character
// rather than a guess. SQLite doubles a double quote, MySQL a backtick.
const quoteProbe = "lantern"

// checkQuote asserts generated SQL never has to guess at quoting — including
// for the identifier that is hardest to quote, one containing the quote
// character itself.
func checkQuote(ctx context.Context, t TestingT, conn driver.Conn) {
	t.Helper()
	quoted := conn.Quote(quoteProbe)
	if quoted == quoteProbe {
		t.Errorf("Quote(%q) returned it unchanged; an identifier that needs no escaping "+
			"still needs delimiting, or a column named after a keyword is a syntax error",
			quoteProbe)
		return
	}
	mark, _ := utf8.DecodeRuneInString(quoted)
	ident := quoteProbe + string(mark) + "id"

	// An alias, because it needs no DDL and still proves the quoted form is
	// SQL rather than a string the engine merely tolerates: the engine has to
	// parse it as an identifier to name the column after it.
	stmt := "SELECT 1 AS " + conn.Quote(ident)
	cur, err := conn.Query(ctx, stmt)
	if !succeeded(t, err, "the quoted form of %q is not usable in a statement (%s)", ident, stmt) {
		return
	}
	defer func() { _ = cur.Close() }()
	if cols := cur.Columns(); len(cols) != 1 || cols[0].Name != ident {
		t.Errorf("Quote did not round-trip: %q rendered as %s came back as %v",
			ident, stmt, columnNames(cur.Columns()))
	}
}

func columnNames(cols []driver.ColumnMeta) []string {
	out := make([]string, len(cols))
	for i, col := range cols {
		out[i] = col.Name
	}
	return out
}

// checkPing uses a connection of its own: it closes what it pings, and the
// rest of the suite still needs the one it was given.
func checkPing(ctx context.Context, t TestingT, cfg Config) {
	t.Helper()
	conn, err := cfg.Driver.Open(ctx, cfg.Open(t))
	if !succeeded(t, err, "opening a second connection") {
		return
	}
	succeeded(t, conn.Ping(ctx), "Ping on a connection that is open")
	_ = conn.Close()
	if conn.Ping(ctx) == nil {
		t.Errorf("Ping succeeded after Close; a connection the UI has dropped would " +
			"report itself healthy forever")
	}
}

// checkCursorExhaustion is Cursor.Next's own contract, spec'd since the
// interface was written and never asserted: it returns FEWER than n rows,
// with a NIL error, when the result is exhausted — not io.EOF.
//
// The difference is not cosmetic. dberr.From classifies an io.EOF as an
// unknown failure, so a driver that signals exhaustion with it reports the
// ordinary end of every result set as a database error; and a caller written
// against the contract stops on the short read without ever reading the
// error, so the two conventions disagree about whether anything went wrong
// on literally every query.
func checkCursorExhaustion(ctx context.Context, t TestingT, conn driver.Conn) {
	t.Helper()
	fx, _ := fixtureNamed(TableRows)
	stmt := "SELECT " + conn.Quote(KeyColumn) + " FROM " + conn.Quote(TableRows)
	cur, err := conn.Query(ctx, stmt)
	if !succeeded(t, err, "reading %q back with %s", TableRows, stmt) {
		return
	}
	defer func() { _ = cur.Close() }()

	// One more than the table holds, so the very first read is the short one.
	n := len(fx.rows) + 1
	rows, err := cur.Next(ctx, n)
	if err != nil {
		t.Errorf("Cursor.Next(%d) over a table of %d rows reported %v; exhaustion is a short "+
			"read with a NIL error, never io.EOF — dberr classifies an io.EOF as an unknown "+
			"failure, so this driver reports the end of every result set as one",
			n, len(fx.rows), err)
		return
	}
	if len(rows) >= n {
		t.Errorf("Cursor.Next(%d) returned %d rows; n is a ceiling, and a caller sizing a "+
			"buffer from it is the one who finds out otherwise", n, len(rows))
	}
	// And again, past the end: a driver that signals with an error rather
	// than a short read most often starts here, where it has nothing left to
	// return at all.
	rows, err = cur.Next(ctx, n)
	if err != nil {
		t.Errorf("Cursor.Next on an already-exhausted cursor reported %v; want no rows and a "+
			"nil error", err)
		return
	}
	if len(rows) != 0 {
		t.Errorf("Cursor.Next returned %d rows from a cursor that was already exhausted; a "+
			"caller that stops on a short read would have stopped, and these rows are lost",
			len(rows))
	}
}

// checkRequiredFields is the contract the connection form is built from: a
// driver names the ConnConfig fields it cannot dial without, in the struct's
// own lowercase vocabulary, so connections.save can refuse to persist a
// connection that could never work without knowing which driver it is
// talking to.
//
// Neither assertion is "the driver requires the right fields" — the suite
// cannot know that, and a driver is the only thing that does. The first is
// that a config the suite has ALREADY connected with is not reported as
// incomplete, because a driver naming a field it does not need makes its own
// working connections unsavable. The second is that whatever it does name is
// spelled the way the caller reads the fields back; a name outside that
// vocabulary reaches the user as the name of a form field that does not
// exist.
func checkRequiredFields(t TestingT, cfg Config) {
	t.Helper()
	if missing := cfg.Driver.RequiredFields(cfg.Open(t)); len(missing) > 0 {
		t.Errorf("RequiredFields reports %v missing from the very config this suite connects "+
			"with; the connection form refuses to save a config a driver reports on, so a "+
			"driver that over-reports cannot save a connection that demonstrably works",
			missing)
	}
	for _, name := range cfg.Driver.RequiredFields(driver.ConnConfig{}) {
		if !connConfigField(name) {
			t.Errorf("RequiredFields named %q, which is not the lowercased name of any "+
				"driver.ConnConfig field; the caller hands these straight back to the user "+
				"and has only the struct's own vocabulary to render them with", name)
		}
	}
}

// connConfigField reports whether name is a driver.ConnConfig field's own
// name, lowercased. Read off the struct rather than listed here, so a field
// added to ConnConfig does not quietly make this check wrong.
func connConfigField(name string) bool {
	rt := reflect.TypeOf(driver.ConnConfig{})
	for i := range rt.NumField() {
		if strings.ToLower(rt.Field(i).Name) == name {
			return true
		}
	}
	return false
}

// The two tables checkReadOnly creates. Nothing seeds either, so whether
// they exist afterwards is entirely down to which connection was allowed to
// write. They are separate names because one is the assertion and the other
// is its control, and a shared name would let the control's own write
// satisfy the assertion.
const (
	readOnlyProbe   = "lantern_conf_readonly_probe"
	readOnlyControl = "lantern_conf_readonly_control"
)

// checkReadOnly is the one contract spec section 4 says a driver must FAIL
// OPEN over rather than accept and quietly not honour.
//
// ConnConfig.ReadOnly is a safety mechanism, not decoration: production
// connections default to it and a write against one must fail outright.
// Enforcing it is per-engine and, on a pooled *sql.DB, per CONNECTION —
// SQLite needs a DSN pragma, and MySQL's equivalent is session state, which
// is exactly what Conn's doc comment warns cannot be established by running
// one statement after opening. A driver with no engine-enforced mechanism
// must refuse the flag at Open, and that refusal is accepted here: a loud
// failure is a safety mechanism, a lock icon beside a connection that still
// writes is not.
//
// The verdict is read back over a SECOND, read-write connection to the same
// database rather than over the guarded one. A driver that refuses the write
// in its own code while the engine takes it underneath would otherwise pass:
// what is being asserted is that the database did not change, not that the
// user saw a refusal.
func checkReadOnly(ctx context.Context, t TestingT, cfg Config, database string) {
	t.Helper()
	// One config, opened twice, because the two connections must reach the
	// SAME database — which a second call to cfg.Open does not promise, since
	// a file-backed driver is expected to make a fresh file each time.
	cc := cfg.Open(t)
	rw, err := cfg.Driver.Open(ctx, cc)
	if !succeeded(t, err, "opening the read-write connection ReadOnly is checked against") {
		return
	}
	defer func() { _ = rw.Close() }()

	cc.ReadOnly = true
	ro, err := cfg.Driver.Open(ctx, cc)
	if err != nil {
		// The only permitted alternative, and spec section 4 names it: a
		// driver that cannot enforce read-only refuses the flag here rather
		// than accepting it and allowing writes anyway.
		return
	}
	defer func() { _ = ro.Close() }()

	// The positive control runs FIRST, on a table of its own: a connection
	// that cannot write at all, or a Columns that answers not-found for every
	// name, would otherwise satisfy the absence asserted below while proving
	// nothing about read-only.
	control := cfg.DDL.create(fixture{name: readOnlyControl})
	cur, err := rw.Query(ctx, control)
	if !succeeded(t, err, "creating %q over a read-write connection (%s)",
		readOnlyControl, control) {
		return
	}
	_ = cur.Close()
	if _, err := rw.Columns(ctx, database, readOnlyControl); err != nil {
		t.Errorf("%q was just created over this read-write connection and Columns still "+
			"cannot see it (%v), so an absence read back the same way proves nothing",
			readOnlyControl, err)
		return
	}

	stmt := cfg.DDL.create(fixture{name: readOnlyProbe})
	cur, roErr := ro.Query(ctx, stmt)
	if roErr == nil {
		_ = cur.Close()
		t.Errorf("a write (%s) was accepted on a connection opened with ConnConfig.ReadOnly; "+
			"a driver that cannot enforce read-only must fail Open rather than accept the "+
			"flag, because a flag that looks respected and is not is worse than no flag",
			stmt)
		return
	}
	refused(t, roErr, dberr.KindReadOnly,
		"a write (%s) issued on a connection opened with ConnConfig.ReadOnly", stmt)

	if _, err := rw.Columns(ctx, database, readOnlyProbe); err == nil {
		t.Errorf("%q exists when read back over an INDEPENDENT read-write connection, so the "+
			"refusal above was this driver declining to issue the write while the engine "+
			"took it anyway", readOnlyProbe)
	}
}

// checkPaging is the invariant the browse work exists for: a table walked to
// exhaustion yields every row exactly once, at every page size.
func checkPaging(ctx context.Context, t TestingT, br driver.Browser, database string, c pagingCase, observe func(PagingPath)) {
	t.Helper()
	fx, _ := fixtureNamed(c.table)
	want := fx.keys()
	for _, limit := range c.pages {
		got, complete := walk(ctx, t, br, database, c, limit, observe)
		if !complete {
			continue
		}
		if !sameMultiset(got, want) {
			t.Errorf("walking %s returned %d keys %v; want every row exactly once, %v",
				describe(c, limit), len(got), sorted(got), sorted(want))
		}
	}
}

// walk pages one table to exhaustion, echoing back whatever the previous page
// handed it — a keyset when the driver paginated by key, an offset when it
// could not — and returns the key of every row it saw.
func walk(ctx context.Context, t TestingT, br driver.Browser, database string, c pagingCase, limit int, observe func(PagingPath)) ([]string, bool) {
	t.Helper()
	fx, _ := fixtureNamed(c.table)
	what := describe(c, limit)
	// A driver that never reports exhaustion must be stopped by something.
	// One page per row is past generous: a correct driver needs ceil(n/limit).
	budget := len(fx.rows) + 2

	req := driver.BrowseRequest{Database: database, Table: c.table, Sort: c.sort, Limit: limit}
	var got []string
	for page := 1; ; page++ {
		p, err := br.Browse(ctx, req)
		if !succeeded(t, err, "walking %s", what) {
			return got, false
		}
		if p == nil {
			t.Errorf("walking %s returned a nil page and no error", what)
			return got, false
		}
		at := columnIndex(p.Columns, KeyColumn)
		if at < 0 || ragged(p.Rows, len(p.Columns)) {
			t.Errorf("walking %s produced a page that has no usable %q column: "+
				"columns %v, row widths %v", what, KeyColumn, columnNames(p.Columns), widths(p.Rows))
			return got, false
		}
		if len(p.Rows) > limit {
			t.Errorf("walking %s returned %d rows for a limit of %d; an unbounded page is "+
				"how a UI bug becomes an out-of-memory crash", what, len(p.Rows), limit)
			return got, false
		}
		for _, row := range p.Rows {
			got = append(got, row[at].Text)
		}
		if len(p.Keyset) > 0 && p.SortToken == "" {
			t.Errorf("walking %s issued a keyset with no sort token; the token is what "+
				"tells a legitimate continuation from a cursor replayed under a sort that "+
				"has since changed, and it is absent exactly when the keyset is", what)
		}
		if p.Exhausted {
			return got, true
		}
		if page >= budget {
			t.Errorf("walking %s did not terminate: still not exhausted after %d pages "+
				"and %d rows", what, page, len(got))
			return got, false
		}
		if len(p.Keyset) > 0 {
			observe(PathKeyset)
			req.After, req.SortToken = p.Keyset, p.SortToken
			continue
		}
		// No keyset: the driver paginated by offset and said so.
		observe(PathOffset)
		req.Offset = p.Offset
	}
}

// checkCursor asserts a cursor is only honoured for the sort it was issued
// under. Both halves are asserted against the SAME cursor, and the positive
// control runs first: a driver that refused every replay would otherwise pass
// two rejection checks while being unable to serve a second page at all.
func checkCursor(ctx context.Context, t TestingT, br driver.Browser, database string) {
	t.Helper()
	first := driver.BrowseRequest{
		Database: database, Table: TableTies, Sort: ascending, Limit: tieGroup,
	}
	p, err := br.Browse(ctx, first)
	if !succeeded(t, err, "browsing %q for a cursor to replay", TableTies) {
		return
	}
	if p == nil || len(p.Keyset) == 0 {
		// No cursor was issued — this driver paged by offset, and there is
		// nothing to replay under the wrong sort.
		return
	}

	replay := first
	replay.After, replay.SortToken = p.Keyset, p.SortToken
	if _, err := br.Browse(ctx, replay); !succeeded(t, err,
		"replaying a cursor under the very sort it was issued for") {
		return
	}

	different := replay
	different.Sort = descending
	_, err = br.Browse(ctx, different)
	refused(t, err, dberr.KindInvalid,
		"a cursor issued for the sort %v and replayed under %v, a different sort of the same width",
		ascending, descending)

	untokened := replay
	untokened.SortToken = ""
	_, err = br.Browse(ctx, untokened)
	refused(t, err, dberr.KindInvalid,
		"a cursor replayed with no sort token alongside a non-empty After")
}

// checkEmptyTable is the shape an empty result has to keep, including the
// shape it has on the wire.
//
// The wire assertion is on Columns, and it is the only one that can be. Rows
// arriving as null where the shell declares an array is the defect that
// blanked the whole app, but BrowsePage.MarshalJSON now rewrites a nil Rows
// to [] for EVERY driver alike — so asserting it here would assert the
// marshaller rather than the driver, and no driver could fail it. A check
// that cannot fail certifies everything, which is the exact failure this
// suite exists to avoid; the guarantee is owned by driver's own
// TestBrowsePageWithZeroRowsSerializesAsEmptyArrayNotNull, where deleting it
// actually reddens something.
//
// Columns has no such rewrite. A driver that leaves it nil sends the literal
// null, unaltered, to a shell that declares an array — the same defect, on
// the field where a driver's own choice still reaches the wire. That is why
// the two states a Go-side len() cannot tell apart are told apart here: a
// non-nil empty slice is a driver that answered "no columns" (wrong, but it
// marshals to []), a nil one is a driver that answered null.
func checkEmptyTable(ctx context.Context, t TestingT, br driver.Browser, database string) {
	t.Helper()
	p, err := br.Browse(ctx, driver.BrowseRequest{
		Database: database, Table: TableEmpty, Limit: 10,
	})
	if !succeeded(t, err, "browsing the empty table %q", TableEmpty) {
		return
	}
	if p == nil {
		t.Errorf("browsing the empty table %q returned a nil page and no error", TableEmpty)
		return
	}
	// json.Marshal cannot fail for a BrowsePage — every field is a string, a
	// bool or an int — so its error is dropped rather than branched on.
	b, _ := json.Marshal(p)
	switch {
	case bytes.Contains(b, []byte(`"columns":null`)):
		t.Errorf("the page for the empty table %q marshalled as %s; Columns must cross the "+
			"wire as an array and never as null — nothing rewrites it on the way out the way "+
			"BrowsePage.MarshalJSON rewrites Rows, so a nil slice here is exactly what the "+
			"shell receives", TableEmpty, b)
	case len(p.Columns) == 0:
		t.Errorf("browsing the empty table %q returned no columns; an empty table "+
			"still has a shape, and the grid draws it", TableEmpty)
	}
	if len(p.Rows) != 0 {
		t.Errorf("browsing the empty table %q returned %d rows; there are none to return",
			TableEmpty, len(p.Rows))
	}
	if !p.Exhausted {
		t.Errorf("browsing the empty table %q did not report Exhausted; a caller that "+
			"keeps asking never stops", TableEmpty)
	}
}

// succeeded reports a call the contract says must work, naming the call
// rather than leaving a bare driver error to be interpreted.
func succeeded(t TestingT, err error, format string, args ...any) bool {
	t.Helper()
	if err == nil {
		return true
	}
	t.Errorf("%s: %v", fmt.Sprintf(format, args...), err)
	return false
}

// refused reports a call the contract says must be rejected, and on what
// terms.
//
// The Kind is asserted because the UI branches on it, but the Kind alone is
// never the whole assertion: every caller of this pairs it with a positive
// control — the same call, made legitimately, succeeding — because a driver
// that refuses everything satisfies a Kind check while being useless, and a
// test that cannot tell those apart passes for the wrong reason.
func refused(t TestingT, err error, want dberr.Kind, format string, args ...any) {
	t.Helper()
	what := fmt.Sprintf(format, args...)
	if err == nil {
		t.Errorf("%s was accepted; the contract requires it be refused with kind %q", what, want)
		return
	}
	if got := dberr.From(err).Kind; got != want {
		t.Errorf("%s was refused with kind %q, want %q: %v", what, got, want, err)
	}
}

func describe(c pagingCase, limit int) string {
	return fmt.Sprintf("%q sorted by %s at a page size of %d", c.table, describeSort(c.sort), limit)
}

func describeSort(keys []driver.SortKey) string {
	if len(keys) == 0 {
		return "the driver's own default"
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k.Column + " ASC"
		if k.Desc {
			parts[i] = k.Column + " DESC"
		}
	}
	return strings.Join(parts, ", ")
}

func columnIndex(cols []driver.ColumnMeta, name string) int {
	for i, col := range cols {
		if strings.EqualFold(col.Name, name) {
			return i
		}
	}
	return -1
}

// ragged reports whether any row disagrees with the page's own column count.
func ragged(rows [][]driver.Value, n int) bool {
	for _, row := range rows {
		if len(row) != n {
			return true
		}
	}
	return false
}

func widths(rows [][]driver.Value) []int {
	out := make([]int, len(rows))
	for i, row := range rows {
		out[i] = len(row)
	}
	return out
}

func sameMultiset(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g, w := sorted(got), sorted(want)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

func sorted(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}
