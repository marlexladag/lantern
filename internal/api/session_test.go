package api

// Covers session.tables (session.go): the second introspection tier, called
// when a database is expanded rather than when a connection is opened.
// api_test.go's harness registers it (see newHarness), so these tests call
// it exactly the way the behavioural tests in api_test.go call the other
// session.* methods — no second harness pattern invented.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
)

// seedTables makes the harness's fixture database contain EXACTLY the named
// tables, dropping whatever was there first.
//
// Dropping rather than only creating is the point: newHarness seeds `users`
// so the older session.columns and browse.page tests have a real table to
// read, and these tests assert on the whole table list rather than on one
// member of it. A helper that only added would make "the tables are alpha
// and beta" and "this database is empty" both assertions about a fixture
// that also happens to contain `users` — i.e. neither assertion at all.
//
// Opened and closed on its own connection, the way browse_test.go's
// seedItems already seeds its fixture, so a session opened afterward dials a
// file that is fully written rather than racing a still-open handle.
func seedTables(t *testing.T, dbPath string, names ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("seedTables: open: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("seedTables: list: %v", err)
	}
	var existing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("seedTables: scan: %v", err)
		}
		existing = append(existing, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("seedTables: rows: %v", err)
	}
	_ = rows.Close()
	for _, name := range existing {
		if _, err := db.Exec(`DROP TABLE "` + name + `"`); err != nil {
			t.Fatalf("seedTables: drop %s: %v", name, err)
		}
	}

	for _, name := range names {
		if _, err := db.Exec(`CREATE TABLE "` + name + `" (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatalf("seedTables: create %s: %v", name, err)
		}
	}
}

func TestSessionTablesReadsOneDatabase(t *testing.T) {
	h := newHarness(t)
	seedTables(t, h.db, "alpha", "beta")
	sessionID := openSession(t, h)

	handler, ok := h.srv.Handler("session.tables")
	if !ok {
		t.Fatal("session.tables is not registered")
	}
	params := fmt.Sprintf(`{"session_id":%q,"database":"main"}`, sessionID)
	res, err := handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("session.tables: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Errorf("tables = %+v, want alpha and beta", got)
	}
}

// Adversarial: an empty database must reach the wire as [] and never null.
func TestSessionTablesOnAnEmptyDatabaseSerializesAsAnEmptyArray(t *testing.T) {
	h := newHarness(t)
	// No names: the fixture's own `users` is dropped and nothing replaces
	// it, which is the only way to ask this question of a real database.
	seedTables(t, h.db)
	sessionID := openSession(t, h)
	handler, _ := h.srv.Handler("session.tables")
	params := fmt.Sprintf(`{"session_id":%q,"database":"main"}`, sessionID)
	res, err := handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("session.tables: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("marshalled as %s, want []", raw)
	}
}

// Adversarial: an unknown session must be not_found, not a nil-pointer panic.
func TestSessionTablesRejectsAnUnknownSession(t *testing.T) {
	h := newHarness(t)
	handler, _ := h.srv.Handler("session.tables")
	_, err := handler(context.Background(), json.RawMessage(`{"session_id":"nope","database":"main"}`))
	if err == nil {
		t.Fatal("an unknown session was accepted")
	}
	// rpcErrorKind, not dberr.From: ToRPCError puts the normalized error in
	// the JSON-RPC error's DATA member rather than wrapping it, precisely
	// because the UI branches on that member (spec section 11). dberr.From
	// on the returned *rpc.Error would report "unknown" for every handler
	// error in the package and so could never fail.
	if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want not_found", kind)
	}
}

// Adversarial: a database this connection does not have is not_found, not an
// empty list. A driver that ignored the parameter and returned its only
// database's tables would otherwise pass every test above (driver.Conn.Tables
// names this exact trap); the RPC seam must pass the parameter through
// rather than default it.
func TestSessionTablesRejectsAnUnknownDatabase(t *testing.T) {
	h := newHarness(t)
	sessionID := openSession(t, h)
	handler, _ := h.srv.Handler("session.tables")
	params := fmt.Sprintf(`{"session_id":%q,"database":"nosuchdb"}`, sessionID)
	if _, err := handler(context.Background(), json.RawMessage(params)); err == nil {
		t.Fatal("an unknown database was accepted")
	} else if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want not_found", kind)
	}
}

// Malformed params are a JSON-RPC invalid-params error, not a database one:
// the same distinction session.open and session.columns already draw.
func TestSessionTablesRejectsMalformedParams(t *testing.T) {
	h := newHarness(t)
	handler, _ := h.srv.Handler("session.tables")
	if _, err := handler(context.Background(), json.RawMessage(`{"session_id":5}`)); err == nil {
		t.Fatal("malformed params were accepted")
	}
}
