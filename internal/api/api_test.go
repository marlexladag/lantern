package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"

	_ "github.com/marlexladag/lantern/internal/engine/driver/sqlite"
	_ "modernc.org/sqlite"
)

// harness wires a server, a store on a temp path, and an in-memory keyring.
type harness struct {
	srv  *rpc.Server
	st   *store.Store
	sess *Sessions
	db   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fixture.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`); err != nil {
		t.Fatalf("fixture ddl: %v", err)
	}
	_ = db.Close()

	st := store.New(filepath.Join(dir, "connections.json"), store.NewMemoryKeyring())
	sess := NewSessions()
	srv := rpc.NewServer()
	RegisterConnections(srv, st)
	RegisterSession(srv, st, sess)
	RegisterBrowse(srv, sess)
	t.Cleanup(sess.CloseAll)

	return &harness{srv: srv, st: st, sess: sess, db: dbPath}
}

// call invokes a registered method directly, the way the dispatch loop would.
func (h *harness) call(t *testing.T, method string, params any) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	handler, ok := h.srv.Handler(method)
	if !ok {
		t.Fatalf("method %q is not registered", method)
	}
	result, err := handler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return out, nil
}

func TestSaveThenListRoundTrips(t *testing.T) {
	h := newHarness(t)

	saved, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	var savedRec store.Saved
	if err := json.Unmarshal(saved, &savedRec); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if savedRec.ID == "" {
		t.Fatal("save returned no id")
	}

	listed, err := h.call(t, "connections.list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var records []store.Saved
	if err := json.Unmarshal(listed, &records); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(records) != 1 || records[0].Name != "fixture" {
		t.Fatalf("list = %+v", records)
	}
}

func TestTestConnectionSucceedsOnARealFile(t *testing.T) {
	h := newHarness(t)
	out, err := h.call(t, "connections.test", map[string]any{
		"connection": map[string]any{"driver": "sqlite", "file": h.db},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Errorf("ok = false for a real database file")
	}
}

// A failed test is a normal result the dialog renders, not a transport error.
func TestTestConnectionReportsFailureAsAResultNotAnError(t *testing.T) {
	h := newHarness(t)
	out, err := h.call(t, "connections.test", map[string]any{
		"connection": map[string]any{"driver": "sqlite", "file": filepath.Join(t.TempDir(), "nope.db")},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("test returned a transport error: %v", err)
	}
	var res struct {
		OK    bool   `json:"ok"`
		Kind  string `json:"kind"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true for a missing file")
	}
	if res.Kind != string(dberr.KindNotFound) {
		t.Errorf("kind = %q, want %q", res.Kind, dberr.KindNotFound)
	}
	if res.Error == "" {
		t.Error("no error message for the user")
	}
}

// session.open reads the DATABASE list only, per the two-tier split in
// driver.Conn.Introspect's doc comment: a database's tables are read only
// when it is expanded (session.tables, a later RPC), never eagerly here.
func TestOpenReturnsASessionAndTheDatabaseListWithoutTables(t *testing.T) {
	h := newHarness(t)
	saved, _ := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	var rec store.Saved
	_ = json.Unmarshal(saved, &rec)

	out, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
		Catalog   struct {
			Databases []struct {
				Name   string          `json:"name"`
				Tables json.RawMessage `json:"tables"`
			} `json:"databases"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("no session id")
	}
	if len(res.Catalog.Databases) != 1 || res.Catalog.Databases[0].Name != "main" {
		t.Fatalf("catalog = %+v", res.Catalog)
	}
	// The whole point of the tier: session.open must not have read the table
	// list at all, so the wire value is the JSON literal null, not [].
	if string(res.Catalog.Databases[0].Tables) != "null" {
		t.Errorf("session.open read the table list eagerly: %s", res.Catalog.Databases[0].Tables)
	}
}

func TestColumnsReadsOnDemandAndCloseEndsTheSession(t *testing.T) {
	h := newHarness(t)
	saved, _ := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	var rec store.Saved
	_ = json.Unmarshal(saved, &rec)
	opened, _ := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	var open struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(opened, &open)

	cols, err := h.call(t, "session.columns", map[string]any{
		"session_id": open.SessionID, "database": "main", "table": "users",
	})
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var list []struct {
		Name       string `json:"name"`
		PrimaryKey bool   `json:"primary_key"`
	}
	if err := json.Unmarshal(cols, &list); err != nil {
		t.Fatalf("decode columns: %v", err)
	}
	if len(list) != 2 || list[0].Name != "id" || !list[0].PrimaryKey {
		t.Fatalf("columns = %+v", list)
	}

	if _, err := h.call(t, "session.close", map[string]any{"session_id": open.SessionID}); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The session is gone, so a further call must fail rather than succeed
	// against a closed connection.
	if _, err := h.call(t, "session.columns", map[string]any{
		"session_id": open.SessionID, "database": "main", "table": "users",
	}); err == nil {
		t.Error("columns succeeded on a closed session")
	}
}

// The Kind must survive the trip into a JSON-RPC error's data member, because
// the UI branches on Kind rather than on the numeric code (spec section 11).
func TestToRPCErrorCarriesTheKindInData(t *testing.T) {
	err := ToRPCError(dberr.New(dberr.KindAuth, "access denied"))

	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("ToRPCError did not produce an *rpc.Error: %T", err)
	}
	if re.Code != rpc.CodeDatabase {
		t.Errorf("code = %d, want %d", re.Code, rpc.CodeDatabase)
	}
	if re.Message != "access denied" {
		t.Errorf("message = %q", re.Message)
	}
	var payload dberr.Error
	if err := json.Unmarshal(re.Data, &payload); err != nil {
		t.Fatalf("data is not a dberr.Error: %v (%s)", err, re.Data)
	}
	if payload.Kind != dberr.KindAuth {
		t.Errorf("kind = %q, want %q", payload.Kind, dberr.KindAuth)
	}
}

// The engine must never emit a code from the shell's reserved range.
func TestDatabaseCodeIsOutsideTheShellRange(t *testing.T) {
	if rpc.CodeDatabase > -32020 {
		t.Errorf("CodeDatabase = %d, which intrudes on the shell's -32000..-32019 range", rpc.CodeDatabase)
	}
	if rpc.CodeDatabase < -32099 {
		t.Errorf("CodeDatabase = %d, which is outside JSON-RPC's implementation-defined range", rpc.CodeDatabase)
	}
}

func asRPCError(err error, target **rpc.Error) bool {
	e, ok := err.(*rpc.Error)
	if ok {
		*target = e
	}
	return ok
}
