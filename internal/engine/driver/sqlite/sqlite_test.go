package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
