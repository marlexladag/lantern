package sqlite

// This file exists solely to close the gap between the brief's ten
// behavioural tests and the repo's 100% statement coverage gate
// (scripts/go-coverage.sh). Every test here targets one specific error path
// that the real SQLite driver does not exercise on the fixture-based happy
// path used by sqlite_test.go.
//
// A few of these paths (a Scan-time type mismatch, a driver-level read
// failure mid-iteration) cannot be produced by the real modernc.org/sqlite
// driver through any legitimate SQL: `*any` destinations never fail to
// scan, and a healthy connection never drops mid-row. Those tests use a
// tiny scripted database/sql/driver — registered under a one-off name per
// test — that returns exactly the rows and errors the test wants. This is
// white-box testing (this file is `package sqlite`, sharing the unexported
// `conn` and `cursor` types), which is deliberate: it is the only way to
// reach these branches without relying on undefined driver behaviour.

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
)

// -- Open --------------------------------------------------------------

func TestOpenWithNoFileIsNotFound(t *testing.T) {
	_, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite"})
	if err == nil {
		t.Fatal("opening with no file given succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindNotFound, err)
	}
}

// A path that walks through a non-directory component fails Stat with
// ENOTDIR, not ErrNotExist — the "file exists but can't be statted for some
// other reason" branch, distinct from "file does not exist".
func TestOpenReportsANonMissingStatErrorAsUnknown(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	bogus := filepath.Join(notADir, "child.db")

	_, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: bogus})
	if err == nil {
		t.Fatal("opening through a non-directory path component succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindUnknown {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindUnknown, err)
	}
}

// A directory passes Stat (it exists) but cannot be opened as a SQLite
// database, so the failure surfaces from PingContext inside Open, not from
// Stat. This also exercises classify's "unable to open database file"
// substring match.
func TestOpenOnADirectoryFailsAsNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: dir})
	if err == nil {
		t.Fatal("opening a directory as a database file succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindNotFound, err)
	}
}

// -- Ping / Introspect / Columns against a closed connection ------------

func TestPingAfterCloseReportsUnknown(t *testing.T) {
	c := open(t, fixture(t))
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("ping on a closed connection succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindUnknown {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindUnknown, err)
	}
}

func TestIntrospectAfterCloseFails(t *testing.T) {
	c := open(t, fixture(t))
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := c.Introspect(context.Background())
	if err == nil {
		t.Fatal("introspect on a closed connection succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindUnknown {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindUnknown, err)
	}
}

func TestColumnsAfterCloseFails(t *testing.T) {
	c := open(t, fixture(t))
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := c.Columns(context.Background(), "main", "users")
	if err == nil {
		t.Fatal("columns on a closed connection succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindUnknown {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindUnknown, err)
	}
}

// -- cursor.Next: context cancellation and a Scan-count mismatch --------

// Next checks ctx.Err() itself on every iteration, independently of the
// context the query was originally issued under, so a caller can cancel
// mid-stream even though *sql.Rows has its own separate context.
func TestNextReturnsCanceledWhenContextIsAlreadyDone(t *testing.T) {
	c := open(t, fixture(t))
	cur, err := c.Query(context.Background(), "SELECT id FROM users")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer cur.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = cur.Next(ctx, 10)
	if err == nil {
		t.Fatal("Next with an already-canceled context succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindCanceled {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindCanceled, err)
	}
}

// Kept deliberately, not merely retained by default: this was one of two
// tests reviewed for removal alongside the classify nil-guard (see above),
// on the theory that an unreachable-through-the-public-API branch is dead
// weight. It is not the same shape of unreachable as the sql.Open /
// rows.ColumnTypes branches removed from Open and Query, or as classify's
// former nil guard — in every one of those, discarding the "impossible"
// error left the success path exactly as correct as before (db and types
// are already well-formed regardless; a nil err reaching classify would
// have panicked immediately and loudly, not corrupted anything). Discarding
// *this* Scan error would not: per database/sql's scanLocked, a
// destination-count mismatch is caught before a single value is converted,
// so `cells` would stay all-nil and Next would silently hand the caller a
// fabricated empty-looking row instead of an error. That failure mode is
// exactly what this driver's dberr classification exists to prevent, so the
// check — and this regression test for it — stays.
//
// A NULL always converts cleanly into a `*any` destination (see
// database/sql's convertAssignRows), so Scan can never fail on a type
// mismatch through this driver's cursor. The one way Scan does fail is a
// destination-count mismatch against the real result set — forced here via
// white-box access to cursor.meta, since the public API cannot produce one.
func TestNextReportsAScanErrorOnDestinationCountMismatch(t *testing.T) {
	c := open(t, fixture(t))
	got, err := c.Query(context.Background(), "SELECT id, email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer got.Close()

	cur := got.(*cursor)
	cur.meta = append(cur.meta, driver.ColumnMeta{Name: "phantom"})

	if _, err := cur.Next(context.Background(), 10); err == nil {
		t.Fatal("expected a scan error from a destination-count mismatch")
	}
}

// modernc.org/sqlite returns TEXT columns as Go strings already (see the
// email assertion in TestQueryStreamsRowsAndReportsColumns), so the []byte
// clone in Next is only exercised by a genuine BLOB column. Two distinct
// rows in the same Next call are the regression guard for that clone:
// database/sql's Scan already clones a []byte before it reaches Next's
// `any` destination (see convertAssignRows), so nothing here is working
// around a live aliasing bug today — but Next accumulates a whole page
// before returning it, so if the clone were dropped and some future driver
// reused one row buffer, a shared buffer would show up here as both rows
// reading back with the second row's content.
//
// The values arrive as []byte and must STAY []byte: converting them to
// string here is what made driver.Normalize's []byte case dead for the only
// shipped driver, so a BLOB rendered as control characters instead of the
// byte summary the grid refuses to render inline (fix wave D-2).
func TestNextCopiesBlobValuesIntoDistinctSlices(t *testing.T) {
	path := fixture(t)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE blobs (id INTEGER PRIMARY KEY, data BLOB)`); err != nil {
		t.Fatalf("create blobs: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO blobs (id, data) VALUES (1, ?), (2, ?)`, []byte("first"), []byte("second")); err != nil {
		t.Fatalf("seed blobs: %v", err)
	}

	c := open(t, path)
	cur, err := c.Query(context.Background(), "SELECT data FROM blobs ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer cur.Close()

	rows, err := cur.Next(context.Background(), 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}

	first, ok := rows[0][0].([]byte)
	if !ok {
		t.Fatalf("row 0 = %T, want []byte — a blob must reach Normalize as bytes", rows[0][0])
	}
	second, ok := rows[1][0].([]byte)
	if !ok {
		t.Fatalf("row 1 = %T, want []byte — a blob must reach Normalize as bytes", rows[1][0])
	}
	if string(first) != "first" || string(second) != "second" {
		t.Errorf(`got %q, %q, want "first", "second" -- a shared buffer would corrupt earlier rows`, first, second)
	}
}

// -- classify: direct and canceled/timeout paths ------------------------
//
// classify no longer has a nil-error guard to test: every call site already
// guards with `if err != nil`, so it was untestable dead code (see the
// removal in sqlite.go). Removed together with
// TestClassifyReturnsNilForNilError, which existed only to cover it.

// Query always has a non-empty statement, so a canceled context surfaces as
// Canceled carrying the statement that was in flight.
func TestQueryWithCanceledContextReportsCanceledWithStatement(t *testing.T) {
	c := open(t, fixture(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stmt := "SELECT 1"
	_, err := c.Query(ctx, stmt)
	if err == nil {
		t.Fatal("query with an already-canceled context succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindCanceled {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindCanceled, err)
	}
	if got.Query != stmt {
		t.Errorf("Query = %q, want %q", got.Query, stmt)
	}
}

// Ping and Open's inline ping both call classify with an empty statement, so
// a canceled context there must not carry one.
func TestPingWithCanceledContextReportsCanceledWithNoStatement(t *testing.T) {
	c := open(t, fixture(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Ping(ctx)
	if err == nil {
		t.Fatal("ping with an already-canceled context succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindCanceled {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindCanceled, err)
	}
	if got.Query != "" {
		t.Errorf("Query = %q, want empty", got.Query)
	}
}

// -- classify: substring branches ----------------------------------------

func TestQueryAgainstMissingTableReportsNotFound(t *testing.T) {
	c := open(t, fixture(t))
	stmt := "SELECT * FROM totally_missing_table"
	_, err := c.Query(context.Background(), stmt)
	if err == nil {
		t.Fatal("querying a missing table succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindNotFound, err)
	}
}

func TestQueryWithMissingColumnReportsSyntax(t *testing.T) {
	c := open(t, fixture(t))
	stmt := "SELECT totally_missing_column FROM users"
	_, err := c.Query(context.Background(), stmt)
	if err == nil {
		t.Fatal("querying a missing column succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindSyntax {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindSyntax, err)
	}
}

func TestQueryUniqueConstraintViolationReportsConstraint(t *testing.T) {
	c := open(t, fixture(t))
	// id=1 already exists in the fixture.
	stmt := "INSERT INTO users (id, email) VALUES (1, 'dup@example.com')"
	_, err := c.Query(context.Background(), stmt)
	if err == nil {
		t.Fatal("a duplicate primary key insert succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindConstraint {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindConstraint, err)
	}
}

func TestQueryNotNullConstraintViolationReportsConstraint(t *testing.T) {
	c := open(t, fixture(t))
	stmt := "INSERT INTO users (id, email) VALUES (99, NULL)"
	_, err := c.Query(context.Background(), stmt)
	if err == nil {
		t.Fatal("inserting a NULL into a NOT NULL column succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindConstraint {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindConstraint, err)
	}
}

func TestQueryForeignKeyConstraintViolationReportsConstraint(t *testing.T) {
	c := open(t, fixture(t))
	ctx := context.Background()

	// PRAGMA foreign_keys is per-connection, not per-database, and
	// database/sql pools connections — without pinning to one, the pragma
	// set below might land on a different connection than the INSERT that
	// needs to see it.
	c.(*conn).db.SetMaxOpenConns(1)

	for _, stmt := range []string{
		"PRAGMA foreign_keys = ON",
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		"CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id))",
	} {
		cur, err := c.Query(ctx, stmt)
		if err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
		// Each cursor holds the pool's one connection open until closed; with
		// SetMaxOpenConns(1) above, leaving one open deadlocks the next Query.
		cur.Close()
	}

	stmt := "INSERT INTO children (id, parent_id) VALUES (1, 404)"
	_, err := c.Query(ctx, stmt)
	if err == nil {
		t.Fatal("inserting a dangling foreign key succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindConstraint {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindConstraint, err)
	}
}

// classify must not let an identifier's own text steer classification: a
// table or column can legally be named after SQL-error vocabulary, and a
// genuine constraint violation on it must still report Constraint. Before
// the fix these reported NotFound and Syntax respectively, because
// substring matching on the error message saw "no such table" / "syntax
// error" inside the *identifier* and matched before ever reaching the
// constraint case.
func TestQueryConstraintViolationOnATableNamedLikeAnErrorMessage(t *testing.T) {
	c := open(t, fixture(t))
	ctx := context.Background()
	table := `"no such table thing"`

	for _, stmt := range []string{
		"CREATE TABLE " + table + " (id INTEGER PRIMARY KEY, email TEXT)",
		"INSERT INTO " + table + " (id, email) VALUES (1, 'a@example.com')",
	} {
		cur, err := c.Query(ctx, stmt)
		if err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
		cur.Close()
	}

	stmt := "INSERT INTO " + table + " (id, email) VALUES (1, 'b@example.com')"
	_, err := c.Query(ctx, stmt)
	if err == nil {
		t.Fatal("a duplicate primary key insert succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindConstraint {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindConstraint, err)
	}
}

func TestQueryConstraintViolationOnAColumnNamedLikeAnErrorMessage(t *testing.T) {
	c := open(t, fixture(t))
	ctx := context.Background()
	column := `"col with syntax error in it"`

	stmt := "CREATE TABLE t2 (id INTEGER PRIMARY KEY, " + column + " TEXT NOT NULL)"
	cur, err := c.Query(ctx, stmt)
	if err != nil {
		t.Fatalf("setup %q: %v", stmt, err)
	}
	cur.Close()

	stmt = "INSERT INTO t2 (id, " + column + ") VALUES (1, NULL)"
	_, err = c.Query(ctx, stmt)
	if err == nil {
		t.Fatal("inserting a NULL into a NOT NULL column succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindConstraint {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindConstraint, err)
	}
}

// -- scripted driver: Scan and Rows.Err() failures the real driver cannot
// -- produce --------------------------------------------------------------

// scriptedRows lets a test force two database/sql failure modes that the
// real SQLite driver never produces through a legitimate query: a NULL
// landing in a non-nullable destination (a Scan-time conversion error), and
// a driver-level read failure mid-iteration (Rows.Err() after Next has
// already returned real rows).
type scriptedRows struct {
	cols     []string
	rows     [][]sqldriver.Value
	errAfter error // returned once rows are exhausted, instead of io.EOF
	i        int
}

func (r *scriptedRows) Columns() []string { return r.cols }
func (r *scriptedRows) Close() error      { return nil }
func (r *scriptedRows) Next(dest []sqldriver.Value) error {
	if r.i >= len(r.rows) {
		if r.errAfter != nil {
			return r.errAfter
		}
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type scriptedConn struct{ rows *scriptedRows }

func (c *scriptedConn) Prepare(string) (sqldriver.Stmt, error) {
	return nil, errors.New("scriptedConn: Prepare not supported")
}
func (c *scriptedConn) Close() error { return nil }
func (c *scriptedConn) Begin() (sqldriver.Tx, error) {
	return nil, errors.New("scriptedConn: Begin not supported")
}
func (c *scriptedConn) Query(string, []sqldriver.Value) (sqldriver.Rows, error) {
	return c.rows, nil
}

type scriptedDriverImpl struct{ conn *scriptedConn }

func (d scriptedDriverImpl) Open(string) (sqldriver.Conn, error) { return d.conn, nil }

// scriptedConnWith registers a one-off database/sql driver under a unique
// name (sql.Register panics on a repeat, so every caller needs a name of
// its own) and returns an engine *conn wrapping it.
// scriptedDriverSeq keeps every sql.Register name unique for the life of the
// test binary. sql.Register panics on a repeat name, and `go test -count=2`
// runs each test twice in ONE process, so a fixed name collides with itself on
// the second pass — which made this package unrunnable under -count>1, the
// usual way to smoke out an order-dependent or state-leaking test. The caller's
// name is kept as a prefix so a panic still says which fixture it came from.
var scriptedDriverSeq atomic.Int64

func scriptedConnWith(t *testing.T, name string, rows *scriptedRows) *conn {
	t.Helper()
	name = fmt.Sprintf("%s#%d", name, scriptedDriverSeq.Add(1))
	sql.Register(name, scriptedDriverImpl{conn: &scriptedConn{rows: rows}})
	db, err := sql.Open(name, "x")
	if err != nil {
		t.Fatalf("open scripted driver %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &conn{db: db}
}

func TestIntrospectReportsAScanError(t *testing.T) {
	c := scriptedConnWith(t, "scripted-introspect-scan", &scriptedRows{
		cols: []string{"name", "type"},
		rows: [][]sqldriver.Value{{nil, "table"}}, // NULL name -> Scan fails
	})
	if _, err := c.Introspect(context.Background()); err == nil {
		t.Fatal("expected a scan error")
	}
}

func TestIntrospectReportsARowsError(t *testing.T) {
	c := scriptedConnWith(t, "scripted-introspect-err", &scriptedRows{
		cols:     []string{"name", "type"},
		rows:     [][]sqldriver.Value{{"users", "table"}},
		errAfter: errors.New("simulated read failure"),
	})
	if _, err := c.Introspect(context.Background()); err == nil {
		t.Fatal("expected a rows error")
	}
}

func TestColumnsReportsAScanError(t *testing.T) {
	c := scriptedConnWith(t, "scripted-columns-scan", &scriptedRows{
		cols: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"},
		rows: [][]sqldriver.Value{{int64(0), nil, "INTEGER", int64(0), nil, int64(1)}}, // NULL name
	})
	if _, err := c.Columns(context.Background(), "main", "t"); err == nil {
		t.Fatal("expected a scan error")
	}
}

func TestColumnsReportsARowsError(t *testing.T) {
	c := scriptedConnWith(t, "scripted-columns-err", &scriptedRows{
		cols:     []string{"cid", "name", "type", "notnull", "dflt_value", "pk"},
		rows:     [][]sqldriver.Value{{int64(0), "id", "INTEGER", int64(0), nil, int64(1)}},
		errAfter: errors.New("simulated read failure"),
	})
	if _, err := c.Columns(context.Background(), "main", "t"); err == nil {
		t.Fatal("expected a rows error")
	}
}

func TestQueryCursorNextReportsARowsError(t *testing.T) {
	c := scriptedConnWith(t, "scripted-query-err", &scriptedRows{
		cols:     []string{"id"},
		rows:     [][]sqldriver.Value{{int64(1)}},
		errAfter: errors.New("simulated read failure"),
	})
	cur, err := c.Query(context.Background(), "whatever")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer cur.Close()

	if _, err := cur.Next(context.Background(), 10); err == nil {
		t.Fatal("expected a rows error")
	}
}
