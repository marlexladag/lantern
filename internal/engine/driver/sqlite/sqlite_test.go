package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"

	_ "modernc.org/sqlite"
)

// fixture creates a real SQLite file with a known shape and returns its path.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL,
			nickname TEXT
		)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL)`,
		`CREATE VIEW active_users AS SELECT id, email FROM users`,
		`INSERT INTO users (id, email, nickname) VALUES (1, 'a@example.com', NULL)`,
		`INSERT INTO users (id, email, nickname) VALUES (2, 'b@example.com', 'bee')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture stmt %q: %v", s, err)
		}
	}
	return path
}

// openAt opens path with the given ReadOnly setting and closes it when the
// test ends. It is the one place every other open helper in this file routes
// through, so a test that needs both a read-write and a read-only handle on
// the SAME file — as the pool tests below do — gets them the same way every
// other test opens a connection.
func openAt(t *testing.T, path string, readOnly bool) driver.Conn {
	t.Helper()
	c, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path, ReadOnly: readOnly})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func open(t *testing.T, path string) driver.Conn {
	t.Helper()
	return openAt(t, path, false)
}

// openReadOnly opens path read-only and closes it when the test ends.
func openReadOnly(t *testing.T, path string) driver.Conn {
	t.Helper()
	return openAt(t, path, true)
}

// newTempDB returns the path to a fresh, empty, valid SQLite file that Open
// will accept. database/sql opens lazily and SQLite would happily create a
// missing file, so Open itself refuses one that does not exist yet (see
// sqlite.go) — seeding a zero-byte file first is what a test wanting an
// empty-but-real database has to do instead, and a zero-length file is a
// valid empty SQLite database in its own right.
func newTempDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "temp.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}
	return path
}

// connWith opens a fresh, empty SQLite file and runs each statement against
// it in turn, returning the live connection. Unlike fixture, it bakes in no
// schema of its own — every test using it states exactly the tables and
// views it needs.
func connWith(t *testing.T, stmts ...string) driver.Conn {
	t.Helper()
	c := open(t, newTempDB(t))
	for _, s := range stmts {
		cur, err := c.Query(context.Background(), s)
		if err != nil {
			t.Fatalf("connWith stmt %q: %v", s, err)
		}
		cur.Close()
	}
	return c
}

func TestIntrospectReturnsDatabasesWithoutTables(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`, `CREATE TABLE b (id INTEGER PRIMARY KEY)`)
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(cat.Databases) != 1 || cat.Databases[0].Name != "main" {
		t.Fatalf("databases = %+v, want exactly main", cat.Databases)
	}
	// The whole point of the tier: connecting must not read the table list.
	if cat.Databases[0].Tables != nil {
		t.Errorf("Introspect read the table list eagerly: %+v", cat.Databases[0].Tables)
	}
}

func TestTablesReadsOneDatabasesTables(t *testing.T) {
	c := connWith(t,
		`CREATE TABLE a (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW v AS SELECT id FROM a`,
	)
	got, err := c.Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	var names []string
	for _, tb := range got {
		names = append(names, tb.Name+":"+string(tb.Kind))
	}
	want := []string{"a:table", "b:table", "v:view"}
	if !slices.Equal(names, want) {
		t.Errorf("tables = %v, want %v", names, want)
	}
	// Columns stay unread: that is the third tier, and this is the second.
	for _, tb := range got {
		if tb.Columns != nil {
			t.Errorf("%s arrived with columns already read", tb.Name)
		}
	}
}

// Adversarial: a database with no user tables must return an EMPTY slice, not
// nil. A nil slice marshals to the JSON literal null, the TypeScript side
// declares an array, and that combination blanked the whole window once.
func TestTablesOnAnEmptyDatabaseReturnsAnEmptySlice(t *testing.T) {
	c := connWith(t)
	got, err := c.Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if got == nil {
		t.Fatal("nil slice; it will marshal to null and blank the sidebar")
	}
	if len(got) != 0 {
		t.Errorf("tables = %+v, want none", got)
	}
	raw, err := json.Marshal(map[string]any{"tables": got})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tables":[]`) {
		t.Errorf("marshalled as %s, want an empty array", raw)
	}
}

// Adversarial: an unknown database must be refused rather than silently
// returning main's tables, which is what a driver that ignores the parameter
// would do. SQLite ignores it today and no test anywhere would notice.
func TestTablesRejectsAnUnknownDatabase(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`)
	_, err := c.Tables(context.Background(), "nonesuch")
	if err == nil {
		t.Fatal("an unknown database was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want not_found", got.Kind)
	}
}

func TestPingSucceedsOnARealFile(t *testing.T) {
	if err := open(t, fixture(t)).Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestOpenReportsAMissingFileAsNotFound(t *testing.T) {
	_, err := New().Open(context.Background(), driver.ConnConfig{
		Driver: "sqlite",
		File:   filepath.Join(t.TempDir(), "does-not-exist.db"),
	})
	if err == nil {
		t.Fatal("opening a missing file succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindNotFound, err)
	}
}

// Tables lists tables and views but must NOT read columns — that is the
// third tier.
func TestTablesListsTablesAndViewsWithoutColumns(t *testing.T) {
	tables, err := open(t, fixture(t)).Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}

	want := map[string]schema.TableKind{
		"users":        schema.TableKindTable,
		"orders":       schema.TableKindTable,
		"active_users": schema.TableKindView,
	}
	if len(tables) != len(want) {
		t.Fatalf("got %d tables, want %d: %+v", len(tables), len(want), tables)
	}
	for _, tbl := range tables {
		if tbl.Kind != want[tbl.Name] {
			t.Errorf("%s kind = %q, want %q", tbl.Name, tbl.Kind, want[tbl.Name])
		}
		if tbl.Loaded() {
			t.Errorf("%s reported Loaded — table listing must not read columns", tbl.Name)
		}
	}
}

// SQLite's own bookkeeping tables must not appear in the tree.
func TestTablesHidesInternalTables(t *testing.T) {
	tables, err := open(t, fixture(t)).Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	for _, tbl := range tables {
		if len(tbl.Name) >= 7 && tbl.Name[:7] == "sqlite_" {
			t.Errorf("internal table %q leaked into the catalog", tbl.Name)
		}
	}
}

func TestColumnsReadsTypesNullabilityAndKeys(t *testing.T) {
	cols, err := open(t, fixture(t)).Columns(context.Background(), "main", "users")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("got %d columns, want 3: %+v", len(cols), cols)
	}

	byName := map[string]schema.Column{}
	for _, c := range cols {
		byName[c.Name] = c
	}

	id := byName["id"]
	if id.DataType != "INTEGER" || !id.PrimaryKey || id.Position != 0 {
		t.Errorf("id = %+v, want INTEGER primary key at position 0", id)
	}
	if email := byName["email"]; email.Nullable {
		t.Errorf("email reported nullable but is NOT NULL")
	}
	if nick := byName["nickname"]; !nick.Nullable {
		t.Errorf("nickname reported NOT NULL but is nullable")
	}
}

func TestColumnsOnAMissingTableIsNotFound(t *testing.T) {
	_, err := open(t, fixture(t)).Columns(context.Background(), "main", "no_such_table")
	if err == nil {
		t.Fatal("reading a missing table succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", got.Kind, dberr.KindNotFound)
	}
}

func TestQueryStreamsRowsAndReportsColumns(t *testing.T) {
	cur, err := open(t, fixture(t)).Query(context.Background(), "SELECT id, email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer cur.Close()

	cols := cur.Columns()
	if len(cols) != 2 || cols[0].Name != "id" || cols[1].Name != "email" {
		t.Fatalf("columns = %+v", cols)
	}

	rows, err := cur.Next(context.Background(), 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if got := rows[1][1]; got != "b@example.com" {
		t.Errorf("row 1 email = %v, want b@example.com", got)
	}

	// Exhaustion is an empty slice and a nil error, not an error.
	rest, err := cur.Next(context.Background(), 10)
	if err != nil {
		t.Fatalf("next after exhaustion: %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("got %d rows after exhaustion, want 0", len(rest))
	}
}

func TestQuerySyntaxErrorCarriesKindAndStatement(t *testing.T) {
	stmt := "SELEC 1"
	_, err := open(t, fixture(t)).Query(context.Background(), stmt)
	if err == nil {
		t.Fatal("a malformed statement succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindSyntax {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindSyntax, err)
	}
	if got.Query != stmt {
		t.Errorf("Query = %q, want %q", got.Query, stmt)
	}
}

func TestQuoteEscapesEmbeddedDoubleQuotes(t *testing.T) {
	c := open(t, fixture(t))
	if got := c.Quote("users"); got != `"users"` {
		t.Errorf("Quote(users) = %s", got)
	}
	if got := c.Quote(`we"ird`); got != `"we""ird"` {
		t.Errorf(`Quote(we"ird) = %s`, got)
	}
}

func TestDriverIsRegisteredAndDeclaresCapabilities(t *testing.T) {
	d, ok := driver.Lookup("sqlite")
	if !ok {
		t.Fatal("sqlite driver not registered")
	}
	caps := d.Capabilities()
	if caps.MultipleDatabases {
		t.Error("SQLite declared MultipleDatabases; it has exactly one")
	}
	if !caps.Transactions {
		t.Error("SQLite declared no transaction support")
	}
}

// Coordinator-flagged: a connection with no File used to save successfully
// and only fail much later, at Open, with a confusing "no database file
// given". RequiredFields is what lets connections.save catch this before
// the record is ever persisted.
func TestRequiredFieldsNamesFileWhenMissing(t *testing.T) {
	got := New().RequiredFields(driver.ConnConfig{Driver: "sqlite"})
	want := []string{"file"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("RequiredFields(no file) = %v, want %v", got, want)
	}
}

func TestRequiredFieldsIsSatisfiedWhenFileIsSet(t *testing.T) {
	got := New().RequiredFields(driver.ConnConfig{Driver: "sqlite", File: "/tmp/x.db"})
	if len(got) != 0 {
		t.Errorf("RequiredFields(file set) = %v, want none", got)
	}
}

// A database whose only table is sqlite_sequence (left behind once an
// AUTOINCREMENT table is created and then dropped) must also list to an
// empty, non-nil slice — tablesSQL's own `NOT LIKE 'sqlite_%'` filter hides
// it, and this is the case where the query's WHERE clause alone, without
// the explicit non-nil initialisation, would still leave the result nil.
func TestTablesOnADatabaseWithOnlySqliteSequenceReturnsAnEmptySlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seq-only.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	for _, stmt := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)",
		"INSERT INTO t DEFAULT VALUES",
		"DROP TABLE t",
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("fixture stmt %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	tables, err := open(t, path).Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if tables == nil {
		t.Fatal("nil slice; it will marshal to null and blank the sidebar")
	}
	if len(tables) != 0 {
		t.Errorf("tables = %+v, want none — sqlite_sequence must stay hidden", tables)
	}
}

// -- classify: Message is the fixed, engine-neutral phrase per Kind (A-2) --
//
// classify used to set Message = err.Error(), so Message and Native were
// identical for every classified failure — a raw SQLite error leaking the
// absolute database file path straight into the sidebar (MySQL's net.OpError
// would leak host and port the same way). Each test below covers one row of
// the classify table in sqlite.go's doc comment, asserting the message text
// exactly: "close enough" wording would silently reintroduce drift between
// what the table promises and what ships.

func TestClassifyConstraintViolationMessageIsEngineNeutral(t *testing.T) {
	c := open(t, fixture(t))
	// id=1 already exists in the fixture.
	_, err := c.Query(context.Background(), "INSERT INTO users (id, email) VALUES (1, 'dup@example.com')")
	if err == nil {
		t.Fatal("a duplicate primary key insert succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindConstraint {
		t.Fatalf("kind = %q, want %q", got.Kind, dberr.KindConstraint)
	}
	if got.Message != "the statement violates a constraint" {
		t.Errorf("message = %q", got.Message)
	}
}

func TestClassifyCantOpenMessageIsEngineNeutral(t *testing.T) {
	dir := t.TempDir()
	_, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: dir})
	if err == nil {
		t.Fatal("opening a directory as a database file succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindNotFound {
		t.Fatalf("kind = %q, want %q", got.Kind, dberr.KindNotFound)
	}
	if got.Message != "the database file could not be opened" {
		t.Errorf("message = %q", got.Message)
	}
}

func TestClassifySyntaxErrorMessageIsEngineNeutral(t *testing.T) {
	c := open(t, fixture(t))
	_, err := c.Query(context.Background(), "SELEC 1")
	if err == nil {
		t.Fatal("a malformed statement succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindSyntax {
		t.Fatalf("kind = %q, want %q", got.Kind, dberr.KindSyntax)
	}
	if got.Message != "the statement is not valid SQL" {
		t.Errorf("message = %q", got.Message)
	}
}

func TestClassifyMissingColumnMessageIsEngineNeutral(t *testing.T) {
	c := open(t, fixture(t))
	_, err := c.Query(context.Background(), "SELECT totally_missing_column FROM users")
	if err == nil {
		t.Fatal("querying a missing column succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindSyntax {
		t.Fatalf("kind = %q, want %q", got.Kind, dberr.KindSyntax)
	}
	if got.Message != "the statement is not valid SQL" {
		t.Errorf("message = %q", got.Message)
	}
}

func TestClassifyMissingTableMessageIsEngineNeutral(t *testing.T) {
	c := open(t, fixture(t))
	_, err := c.Query(context.Background(), "SELECT * FROM totally_missing_table")
	if err == nil {
		t.Fatal("querying a missing table succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindNotFound {
		t.Fatalf("kind = %q, want %q", got.Kind, dberr.KindNotFound)
	}
	if got.Message != "the table does not exist" {
		t.Errorf("message = %q", got.Message)
	}
}

// Coordinator-flagged, the defect this whole block exists for: a database
// path is exactly the kind of driver-native detail that must never reach
// Message. This is checked directly against classify rather than through a
// live driver failure: this version of modernc.org/sqlite's own CANTOPEN/
// IOERR text does not, in practice, embed the file path (confirmed
// empirically — a directory opened as a database file, a permission-denied
// file, and a corrupted file all produce path-free .Error() text), so there
// is no way to reproduce today's specific leak organically through the
// public API. The invariant classify must uphold — native driver text never
// substitutes for Message — does not depend on which driver or version
// happens to leak what (MySQL's net.OpError leaking host:port is the same
// defect in a different driver), so exercising classify directly against a
// stand-in error carrying the kind of sensitive substring a real driver
// might someday include is the faithful test: it fails against the pre-fix
// code (msg := err.Error() used as Message) exactly the way a live leak
// would, and is not "one negation away from passing vacuously" — it checks
// both that Message excludes the marker AND that Native carries it.
func TestClassifyNeverLetsNativeDriverTextReachMessage(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "lantern-secret-marker", "app.db")
	native := errors.New("unable to open database file: " + marker)

	got := dberr.From(classify(native, ""))
	if got.Kind != dberr.KindUnknown {
		t.Fatalf("kind = %q, want %q", got.Kind, dberr.KindUnknown)
	}
	if got.Message != "the database reported an error" {
		t.Errorf("message = %q", got.Message)
	}
	if strings.Contains(got.Message, "lantern-secret-marker") {
		t.Errorf("Message leaked the database path: %q", got.Message)
	}
	if !strings.Contains(got.Native, "lantern-secret-marker") {
		t.Errorf("Native did not carry the original driver text: %q", got.Native)
	}
}

// -- ReadOnly (A-3): the flag must actually be enforced, not merely drawn as
// -- a lock icon in the sidebar ------------------------------------------

// Coordinator-flagged (A2-1): opens the same file twice, once with ReadOnly
// and once without, and proves the difference by attempting the same write
// against both. It then goes one step further than "enforced, not ignored":
// it also forces a genuine UNIQUE violation on the read-write connection and
// requires that to still classify as Constraint. That second assertion is
// the one that matters — SQLITE_READONLY and SQLITE_CONSTRAINT used to share
// dberr.KindConstraint (wave A), which meant the UI could not tell "your
// data is bad" (fix the row) from "this connection refuses to write" (untick
// Production connection) apart. Both assertions failing to distinguish would
// prove they are still conflated; both succeeding proves they are not.
func TestReadOnlyConnectionRejectsWritesWhileReadWriteSucceeds(t *testing.T) {
	path := fixture(t)

	ro, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path, ReadOnly: true})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })

	rw, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path})
	if err != nil {
		t.Fatalf("open read-write: %v", err)
	}
	t.Cleanup(func() { _ = rw.Close() })

	cur, err := ro.Query(context.Background(), "CREATE TABLE should_not_exist (id INTEGER PRIMARY KEY)")
	if err == nil {
		cur.Close()
		t.Fatal("CREATE TABLE succeeded against a read-only connection")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindReadOnly {
		t.Errorf("read-only rejection classified as %q, want %q", got.Kind, dberr.KindReadOnly)
	}
	// The only row of classify's table that used to have no Message
	// assertion — it was logged, not checked — which is exactly why the
	// message could name a control in ConnectionDialog.tsx, and duplicate
	// ErrorText's own read_only hint in the rendered output, with every gate
	// green.
	if got.Message != "the connection is read-only" {
		t.Errorf("message = %q, want the engine-neutral phrase from classify's table", got.Message)
	}
	// Spec section 11: the engine says what happened in engine-neutral
	// terms; which control fixes it is the UI's to know. A Message naming
	// one couples this package to the vocabulary of a React component.
	if strings.Contains(got.Message, "Production connection") {
		t.Errorf("the engine names a UI control in its message: %q", got.Message)
	}

	cur2, err := rw.Query(context.Background(), "CREATE TABLE should_exist (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("CREATE TABLE failed against a read-write connection: %v", err)
	}
	cur2.Close()

	tables, err := rw.Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	db := schema.Database{Tables: tables}
	if _, ok := db.Table("should_exist"); !ok {
		t.Error("the read-write CREATE TABLE did not actually take effect")
	}

	// The adversarial half: a genuine constraint violation, on the
	// read-write connection sitting right next to the read-only one above,
	// must still classify as Constraint, not ReadOnly — proving the two
	// Kinds were not accidentally merged back together while telling them
	// apart above.
	dupCur, err := rw.Query(context.Background(), "INSERT INTO users (id, email) VALUES (1, 'dup@example.com')")
	if err == nil {
		dupCur.Close()
		t.Fatal("a duplicate primary key insert succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindConstraint {
		t.Errorf("UNIQUE violation on a read-write connection classified as %q, want %q", got.Kind, dberr.KindConstraint)
	}
}

// SELECT must keep working on a read-only connection — the flag blocks
// writes, not reads.
func TestReadOnlyConnectionStillAllowsReads(t *testing.T) {
	c, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: fixture(t), ReadOnly: true})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	cur, err := c.Query(context.Background(), "SELECT id FROM users")
	if err != nil {
		t.Fatalf("select against a read-only connection: %v", err)
	}
	defer cur.Close()
	rows, err := cur.Next(context.Background(), 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("got %d rows, want 2", len(rows))
	}
}

// -- ReadOnly (C-1): PRAGMA query_only is enforced against statements, and
// -- `PRAGMA query_only=0` is itself a statement ---------------------------

// tableExists reopens path with a second, read-write connection and asks the
// file itself. Asserting against the connection under test would prove only
// that it declined to report the table; asserting against the file proves
// the write never happened.
func tableExists(t *testing.T, path, table string) bool {
	t.Helper()
	tables, err := open(t, path).Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	db := schema.Database{Tables: tables}
	_, ok := db.Table(table)
	return ok
}

// The verified repro, in its one-call form: `PRAGMA query_only=0` re-enables
// writes on the connection, and SQLite executes both halves of the string.
// Before the guard this returned a nil error and the table was really there
// on reopening.
func TestReadOnlyRefusesAPragmaAndAWriteSmuggledIntoOneCall(t *testing.T) {
	path := fixture(t)
	c := openReadOnly(t, path)

	cur, err := c.Query(context.Background(), "PRAGMA query_only=0; CREATE TABLE defeated (id INTEGER)")
	if err == nil {
		cur.Close()
		t.Fatal("a PRAGMA-plus-write smuggled into one call succeeded against a read-only connection")
	}
	if got := dberr.From(err); got.Kind != dberr.KindReadOnly {
		t.Errorf("kind = %q, want %q", got.Kind, dberr.KindReadOnly)
	}
	if tableExists(t, path, "defeated") {
		t.Error("the write went through: the table is in the file")
	}
}

// The same repro in its two-call form. The first call must be refused; the
// second is then refused by query_only itself, still set because the first
// never ran. Both assertions matter: if the guard only caught the smuggled
// form, this would pass the first call and clear query_only for the life of
// that pooled connection.
func TestReadOnlyRefusesClearingQueryOnlyInItsOwnCall(t *testing.T) {
	path := fixture(t)
	c := openReadOnly(t, path)

	cur, err := c.Query(context.Background(), "PRAGMA query_only=0")
	if err == nil {
		cur.Close()
		t.Fatal("PRAGMA query_only=0 succeeded against a read-only connection")
	}
	if got := dberr.From(err); got.Kind != dberr.KindReadOnly {
		t.Errorf("kind = %q, want %q", got.Kind, dberr.KindReadOnly)
	}

	cur2, err := c.Query(context.Background(), "CREATE TABLE defeated (id INTEGER)")
	if err == nil {
		cur2.Close()
		t.Fatal("CREATE TABLE succeeded after an attempt to clear query_only")
	}
	if tableExists(t, path, "defeated") {
		t.Error("the write went through: the table is in the file")
	}
}

// Each evasion the guard claims to handle, run as the attack rather than
// argued. Case, whitespace, comments between tokens and the function-call
// assignment form are all things a tokenizer-free check (a `strings.HasPrefix`
// on the trimmed string, say) would let straight through.
func TestReadOnlyRefusesEveryPragmaSpelling(t *testing.T) {
	evasions := map[string]string{
		"lower case":                    "pragma query_only=0",
		"mixed case":                    "PrAgMa QuErY_oNlY = 0",
		"leading whitespace":            "\n\t   PRAGMA query_only = 0",
		"whitespace around the equals":  "PRAGMA   query_only   =   0",
		"function-call assignment":      "PRAGMA query_only(0)",
		"leading line comment":          "-- innocent\nPRAGMA query_only=0",
		"leading block comment":         "/* innocent */PRAGMA query_only=0",
		"comment between tokens":        "PRAGMA/* here */query_only/* and here */=0",
		"line comment between tokens":   "PRAGMA --here\n query_only=0",
		"smuggled behind a select":      "SELECT 1; PRAGMA query_only=0",
		"smuggled behind a comment":     "SELECT 1 /* ; */; PRAGMA query_only=0",
		"write after a bare select":     "SELECT 1; CREATE TABLE defeated (id INTEGER)",
		"write behind a null statement": "SELECT 1;; CREATE TABLE defeated (id INTEGER)",
	}

	for name, stmt := range evasions {
		t.Run(name, func(t *testing.T) {
			path := fixture(t)
			c := openReadOnly(t, path)

			cur, err := c.Query(context.Background(), stmt)
			if err == nil {
				cur.Close()
				t.Fatalf("%q succeeded against a read-only connection", stmt)
			}
			if got := dberr.From(err); got.Kind != dberr.KindReadOnly {
				t.Errorf("kind = %q, want %q", got.Kind, dberr.KindReadOnly)
			}

			// The evasion is only interesting if it left the connection
			// still refusing writes afterwards.
			cur2, err := c.Query(context.Background(), "CREATE TABLE defeated (id INTEGER)")
			if err == nil {
				cur2.Close()
				t.Fatal("the connection accepted a write after the refusal")
			}
			if tableExists(t, path, "defeated") {
				t.Error("the write went through: the table is in the file")
			}
		})
	}
}

// A guard that breaks reading would be a worse regression than the hole it
// closes, so every shape of ordinary read that the tokenizer could plausibly
// misread as a second statement is exercised here.
func TestReadOnlyStillAllowsOrdinaryReads(t *testing.T) {
	reads := map[string]string{
		"plain select":                   "SELECT id FROM users",
		"trailing semicolon":             "SELECT id FROM users;",
		"trailing semicolon and space":   "SELECT id FROM users;   \n",
		"trailing comment":               "SELECT id FROM users; -- done",
		"trailing block comment":         "SELECT id FROM users; /* done */",
		"semicolon inside a literal":     "SELECT id FROM users WHERE email <> ';'",
		"semicolon inside an identifier": `SELECT "id" FROM users WHERE email <> ''';'''`,
		"leading comment":                "/* the usual */ SELECT id FROM users",
		"pragma-shaped column name":      `SELECT id AS "pragma" FROM users`,
	}

	for name, stmt := range reads {
		t.Run(name, func(t *testing.T) {
			c := openReadOnly(t, fixture(t))
			cur, err := c.Query(context.Background(), stmt)
			if err != nil {
				t.Fatalf("%q was refused on a read-only connection: %v", stmt, err)
			}
			defer cur.Close()
			rows, err := cur.Next(context.Background(), 10)
			if err != nil {
				t.Fatalf("next: %v", err)
			}
			if len(rows) != 2 {
				t.Errorf("got %d rows, want 2", len(rows))
			}
		})
	}
}

// The guard is keyed to the flag, not to the driver: a read-write connection
// keeps running the statements a read-only one refuses. Without this, the
// cheapest way to pass every test above would be to refuse them everywhere.
func TestReadWriteConnectionKeepsRunningPragmasAndMultipleStatements(t *testing.T) {
	path := fixture(t)
	c := open(t, path)

	cur, err := c.Query(context.Background(), "PRAGMA query_only")
	if err != nil {
		t.Fatalf("PRAGMA on a read-write connection: %v", err)
	}
	cur.Close()

	cur2, err := c.Query(context.Background(), "SELECT 1; CREATE TABLE allowed (id INTEGER)")
	if err != nil {
		t.Fatalf("multi-statement on a read-write connection: %v", err)
	}
	cur2.Close()
	if !tableExists(t, path, "allowed") {
		t.Error("the read-write multi-statement write did not take effect")
	}
}

// SQLite's table-valued pragma form is left alone by the guard — it is a
// SELECT, and this proves it is not a way around query_only either. SQLite
// documents these functions as reading built-in pragmas that have no side
// effects; this asserts that documented property rather than trusting it,
// because if it ever stopped holding, the guard above would not be looking.
func TestReadOnlyIsNotDefeatedByTheTableValuedPragmaForm(t *testing.T) {
	path := fixture(t)
	c := openReadOnly(t, path)

	// The setting form is not even accepted as a function argument.
	if cur, err := c.Query(context.Background(), "SELECT * FROM pragma_query_only(0)"); err == nil {
		cur.Close()
		t.Error("pragma_query_only(0) was accepted as a table-valued function")
	}
	// The reading form is, and must leave the connection read-only.
	cur, err := c.Query(context.Background(), "SELECT * FROM pragma_query_only")
	if err != nil {
		t.Fatalf("reading pragma_query_only: %v", err)
	}
	cur.Close()

	cur2, err := c.Query(context.Background(), "CREATE TABLE defeated (id INTEGER)")
	if err == nil {
		cur2.Close()
		t.Fatal("CREATE TABLE succeeded after the table-valued pragma form")
	}
	if tableExists(t, path, "defeated") {
		t.Error("the write went through: the table is in the file")
	}
}

// Columns takes a database and must honour it. This went unasserted long
// enough for the same hole to exist in Tables and reach production, and it was
// the conformance suite that finally asked.
func TestColumnsRejectsAnUnknownDatabase(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT)`)
	_, err := c.Columns(context.Background(), "elsewhere", "a")
	if err == nil {
		t.Fatal("an unknown database was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want not_found", got.Kind)
	}
}

// The assertion whose absence let the hole exist. A kind check alone cannot
// see a parameter that is simply ignored: a driver that never reads `database`
// returns the SAME successful answer for every value, and every not_found
// assertion in the suite would still be about a missing TABLE. What proves the
// parameter is read is that two different values produce two different
// outcomes for the same table.
func TestColumnsAnswersDifferentlyForADifferentDatabase(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT)`)

	right, err := c.Columns(context.Background(), "main", "a")
	if err != nil {
		t.Fatalf("the real database was refused: %v", err)
	}
	if len(right) != 2 {
		t.Fatalf("columns = %d, want 2; the fixture is wrong and this test proves nothing", len(right))
	}

	wrong, wrongErr := c.Columns(context.Background(), "elsewhere", "a")
	if wrongErr == nil {
		t.Fatalf("the same table answered for a database that does not exist: %+v", wrong)
	}
}

// SQLite folds identifiers on ASCII only, so "MAIN" names this database.
func TestColumnsFoldsTheDatabaseNameOnASCII(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT)`)
	if _, err := c.Columns(context.Background(), "MAIN", "a"); err != nil {
		t.Errorf("MAIN names this database and was refused: %v", err)
	}
}

// An empty database name is not this database. Callers that permit one resolve
// it to the default BEFORE asking, so that the name a cursor is issued under is
// the name it is checked against — see Browse.
func TestColumnsRejectsAnEmptyDatabaseName(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT)`)
	if _, err := c.Columns(context.Background(), "", "a"); err == nil {
		t.Error("an empty database name was accepted")
	}
}

// -- Pool configuration ------------------------------------------------------

func TestOpenConfiguresThePool(t *testing.T) {
	c := connWith(t).(*conn)
	stats := c.db.Stats()
	if stats.MaxOpenConnections <= 0 {
		t.Error("the pool is unbounded; a driver that dials a server would open connections without limit")
	}
}

// Adversarial: SQLite's read-only enforcement is applied per connection via
// the DSN, so it must hold on EVERY connection the pool opens, not just the
// first. Force the pool to hand out several concurrently.
func TestReadOnlyHoldsAcrossEveryPooledConnection(t *testing.T) {
	path := newTempDB(t)
	rw := openAt(t, path, false)
	if _, err := rw.Query(context.Background(), `CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	ro := openAt(t, path, true)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = ro.Query(context.Background(),
				`INSERT INTO t (id) VALUES (`+strconv.Itoa(i)+`)`)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err == nil {
			t.Errorf("write %d succeeded on a read-only connection", i)
		}
	}
	// Check with an independent connection rather than the guarded one.
	var n int
	if err := rw.(*conn).db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows landed through a read-only connection", n)
	}
}
