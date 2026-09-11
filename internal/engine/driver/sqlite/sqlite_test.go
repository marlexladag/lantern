package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func open(t *testing.T, path string) driver.Conn {
	t.Helper()
	c, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
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

// Introspect lists tables and views but must NOT read columns — that is lazy.
func TestIntrospectListsTablesAndViewsWithoutColumns(t *testing.T) {
	cat, err := open(t, fixture(t)).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	db, ok := cat.Database("main")
	if !ok {
		t.Fatalf("no database named main in %+v", cat)
	}

	want := map[string]schema.TableKind{
		"users":        schema.TableKindTable,
		"orders":       schema.TableKindTable,
		"active_users": schema.TableKindView,
	}
	if len(db.Tables) != len(want) {
		t.Fatalf("got %d tables, want %d: %+v", len(db.Tables), len(want), db.Tables)
	}
	for _, tbl := range db.Tables {
		if tbl.Kind != want[tbl.Name] {
			t.Errorf("%s kind = %q, want %q", tbl.Name, tbl.Kind, want[tbl.Name])
		}
		if tbl.Loaded() {
			t.Errorf("%s reported Loaded — introspection must not read columns", tbl.Name)
		}
	}
}

// SQLite's own bookkeeping tables must not appear in the tree.
func TestIntrospectHidesInternalTables(t *testing.T) {
	cat, err := open(t, fixture(t)).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	db, _ := cat.Database("main")
	for _, tbl := range db.Tables {
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

// Coordinator-flagged: a database with zero user tables must marshal
// "tables":[], not "tables":null. The TypeScript side declares tables:
// Table[] and calls .map on it while rendering the sidebar, which throws on
// null. Asserting on the marshalled bytes rather than the struct is
// deliberate — the struct was never the thing that broke; a nil slice and an
// empty non-nil slice are indistinguishable by reflection-based struct
// comparison but marshal to different wire output, and it's the wire output
// the UI actually consumes.
func TestIntrospectOnAnEmptyDatabaseMarshalsTablesAsAnEmptyArray(t *testing.T) {
	// A zero-byte file is itself a valid, empty SQLite database — exactly
	// the shape of a brand-new .db the user has not put anything in yet.
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}

	cat, err := open(t, path).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tables":[]`) {
		t.Errorf("marshalled catalog = %s, want it to contain \"tables\":[]", raw)
	}
}

// A database whose only table is sqlite_sequence (left behind once an
// AUTOINCREMENT table is created and then dropped) must also introspect to
// an empty, non-nil Tables — introspectSQL's own `NOT LIKE 'sqlite_%'` filter
// hides it, and this is the case where the query's WHERE clause alone,
// without this fix, would still leave Tables nil.
func TestIntrospectOnADatabaseWithOnlySqliteSequenceMarshalsTablesAsAnEmptyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seq-only.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	for _, stmt := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)",
		"INSERT INTO t DEFAULT VALUES",
		"DROP TABLE t",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture stmt %q: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	cat, err := open(t, path).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tables":[]`) {
		t.Errorf("marshalled catalog = %s, want it to contain \"tables\":[]", raw)
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
	t.Logf("read-only CREATE TABLE rejection classifies as Kind %q, Message %q", got.Kind, got.Message)

	cur2, err := rw.Query(context.Background(), "CREATE TABLE should_exist (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("CREATE TABLE failed against a read-write connection: %v", err)
	}
	cur2.Close()

	cat, err := rw.Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	db, _ := cat.Database("main")
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

// openReadOnly opens path read-only and closes it when the test ends.
func openReadOnly(t *testing.T, path string) driver.Conn {
	t.Helper()
	c, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path, ReadOnly: true})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// tableExists reopens path with a second, read-write connection and asks the
// file itself. Asserting against the connection under test would prove only
// that it declined to report the table; asserting against the file proves
// the write never happened.
func tableExists(t *testing.T, path, table string) bool {
	t.Helper()
	cat, err := open(t, path).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	db, _ := cat.Database("main")
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
