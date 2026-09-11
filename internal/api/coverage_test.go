package api

// This file closes the gap between the brief's seven behavioural tests
// (api_test.go, left untouched) and the repo's 100% statement coverage gate
// (scripts/go-coverage.sh). It also carries the self-review checks the task
// asked for explicitly: a failed session.open must not leak a connection or
// a session-table entry, connections.test must report a failure as a normal
// result rather than a transport error, and concurrent session.open calls
// must not corrupt the session registry.
//
// package api (not api_test) so tests here can reach the unexported
// Sessions.conns map directly to prove the no-leak claims, the same
// white-box approach internal/engine/store's coverage_test.go and
// internal/engine/driver/sqlite's coverage_test.go already use in this repo.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

// -- test doubles ------------------------------------------------------

// fakeConn is a driver.Conn whose every method's behaviour is scripted, so
// tests can force failures (a Ping or Close error, an Introspect error) that
// the real SQLite driver has no legitimate way to produce on demand. This is
// the same "scripted driver" approach internal/engine/driver/sqlite's
// coverage_test.go uses for the failures the real driver can't reach.
type fakeConn struct {
	pingErr          error
	introspectErr    error
	introspectPanics bool
	closeErr         error
	closed           bool
}

func (c *fakeConn) Ping(context.Context) error { return c.pingErr }

func (c *fakeConn) Introspect(context.Context) (*schema.Catalog, error) {
	if c.introspectPanics {
		panic("fakeConn: Introspect panicked (simulated driver bug)")
	}
	if c.introspectErr != nil {
		return nil, c.introspectErr
	}
	return &schema.Catalog{}, nil
}

func (c *fakeConn) Tables(context.Context, string) ([]schema.Table, error) {
	return nil, errors.New("fakeConn: Tables not implemented")
}

func (c *fakeConn) Columns(context.Context, string, string) ([]schema.Column, error) {
	return nil, errors.New("fakeConn: Columns not implemented")
}

func (c *fakeConn) Query(context.Context, string, ...any) (driver.Cursor, error) {
	return nil, errors.New("fakeConn: Query not implemented")
}

func (c *fakeConn) Quote(ident string) string { return ident }

func (c *fakeConn) Close() error {
	c.closed = true
	return c.closeErr
}

var _ driver.Conn = (*fakeConn)(nil)

// fakeDriver opens a fixed *fakeConn, or fails outright when openErr is set.
type fakeDriver struct {
	id      string
	conn    *fakeConn
	openErr error
}

func (d fakeDriver) ID() string                        { return d.id }
func (d fakeDriver) Capabilities() driver.Capabilities { return driver.Capabilities{} }

// RequiredFields: none. The tests that use fakeDriver exercise dialing and
// session behaviour, not connections.save's field validation — that has its
// own tests, against the real sqlite driver's own RequiredFields.
func (d fakeDriver) RequiredFields(driver.ConnConfig) []string { return nil }

func (d fakeDriver) Open(context.Context, driver.ConnConfig) (driver.Conn, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	return d.conn, nil
}

var _ driver.Driver = fakeDriver{}

// fakeDriverSeq makes every registered fake driver id unique for the life of
// the test binary. driver.Register panics on a repeat id — deliberately, so a
// real duplicate is caught at startup rather than silently shadowing — and
// `go test -count=2` runs each test twice in ONE process, so an id derived
// only from the test name collides with itself on the second pass. That made
// the package unrunnable under -count>1, which is the usual way to smoke out
// an order-dependent or state-leaking test.
var fakeDriverSeq atomic.Int64

// registerFakeDriver registers a fakeDriver under an id unique to the
// calling test (driver.Register panics on a repeat id, and the registry has
// no test-visible way to unregister from outside internal/engine/driver) and
// returns that id.
func registerFakeDriver(t *testing.T, conn *fakeConn, openErr error) string {
	t.Helper()
	id := fmt.Sprintf("fake:%s#%d", t.Name(), fakeDriverSeq.Add(1))
	driver.Register(fakeDriver{id: id, conn: conn, openErr: openErr})
	return id
}

// brokenStore returns a store whose backing file can never be read or
// written: its parent path component is a regular file, not a directory, so
// every attempt to open or create the config file fails with ENOTDIR rather
// than the "file does not exist" case store.List treats as an empty list.
// Mirrors internal/engine/store's own TestListFailsWhenTheConfigFileCannotBeRead.
func brokenStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	return store.New(filepath.Join(blocker, "connections.json"), store.NewMemoryKeyring())
}

// skipIfRoot mirrors internal/engine/store's helper of the same name: root
// bypasses Unix permission bits, so permission-based failure injection would
// silently assert something false about this platform rather than the code.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit based failure injection does not apply on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks are bypassed")
	}
}

// rpcErrorKind decodes the Kind carried in a *rpc.Error's data member, the
// same way a real UI would (see TestToRPCErrorCarriesTheKindInData in
// api_test.go — dberr.From(err) is the wrong tool here: err is a *rpc.Error,
// not a *dberr.Error or anything wrapping one, so errors.As inside From
// never matches and it always falls back to KindUnknown).
func rpcErrorKind(t *testing.T, err error) dberr.Kind {
	t.Helper()
	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("err = %T, want *rpc.Error", err)
	}
	var payload dberr.Error
	if err := json.Unmarshal(re.Data, &payload); err != nil {
		t.Fatalf("data is not a dberr.Error: %v (%s)", err, re.Data)
	}
	return payload.Kind
}

// erroringGetKeyring answers every Get with a non-ErrSecretNotFound error,
// so Store.Password's own error-wrapping branch can be driven without
// touching a real keychain.
type erroringGetKeyring struct{}

func (erroringGetKeyring) Get(string, string) (string, error) {
	return "", errors.New("keychain locked")
}
func (erroringGetKeyring) Set(string, string, string) error { return nil }
func (erroringGetKeyring) Delete(string, string) error      { return nil }

// -- connections.list -----------------------------------------------------

func TestConnectionsListReturnsRPCErrorWhenTheStoreFails(t *testing.T) {
	srv := rpc.NewServer()
	RegisterConnections(srv, brokenStore(t))
	h := &harness{srv: srv}

	if _, err := h.call(t, "connections.list", nil); err == nil {
		t.Fatal("connections.list succeeded despite an unreadable store")
	}
}

// -- connections.save -------------------------------------------------------

func TestConnectionsSaveReturnsInvalidParamsOnBadJSON(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, "connections.save", "not-an-object"); err == nil {
		t.Fatal("connections.save succeeded on malformed params")
	}
}

func TestConnectionsSaveRejectsAnUnnamedConnection(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"driver": "sqlite", "file": h.db},
		"password":   "",
	})
	if err == nil {
		t.Fatal("connections.save succeeded with no name")
	}
	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("err = %T, want *rpc.Error", err)
	}
	if re.Code != rpc.CodeDatabase {
		t.Errorf("code = %d, want %d", re.Code, rpc.CodeDatabase)
	}
	// Coordinator-flagged: an empty name is a validation failure, not an
	// unsupported operation — KindUnsupported would read to the UI as "this
	// engine can't do that", which is not what happened.
	if kind := rpcErrorKind(t, err); kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", kind, dberr.KindInvalid)
	}
}

// User-found: a SQLite connection with no File saved successfully and only
// failed later, in the sidebar, as a confusing "no database file given" once
// something finally tried to open it. connections.save must reject this
// before the record is ever persisted, naming the field that is missing.
func TestConnectionsSaveRejectsAFilelessSqliteConnection(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "x", "driver": "sqlite"},
		"password":   "",
	})
	if err == nil {
		t.Fatal("connections.save succeeded with no file")
	}
	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("err = %T, want *rpc.Error", err)
	}
	if re.Code != rpc.CodeDatabase {
		t.Errorf("code = %d, want %d", re.Code, rpc.CodeDatabase)
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", kind, dberr.KindInvalid)
	}
	var payload dberr.Error
	if err := json.Unmarshal(re.Data, &payload); err != nil {
		t.Fatalf("data is not a dberr.Error: %v", err)
	}
	if !strings.Contains(payload.Message, "File") {
		t.Errorf("message = %q, want it to name File", payload.Message)
	}

	// The point of rejecting at save is that nothing was persisted to find
	// and delete later.
	listed, err := h.call(t, "connections.list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var records []store.Saved
	if err := json.Unmarshal(listed, &records); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("list = %+v, want no records saved", records)
	}
}

// Coordinator-flagged: the same defect already closed for a missing field
// (TestConnectionsSaveRejectsAFilelessSqliteConnection, above) — an
// unregistered driver id used to save successfully and only fail once
// something actually tried to dial it, by which point the unusable record
// was already durable. connections.save now rejects it outright. Both
// assertions matter: an error alone would not prove nothing was committed,
// which was the whole complaint the user reported in person.
func TestConnectionsSaveRejectsAnUnregisteredDriver(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "future", "driver": "nonesuch"},
		"password":   "",
	})
	if err == nil {
		t.Fatal("connections.save succeeded for an unregistered driver id")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", kind, dberr.KindInvalid)
	}

	listed, err := h.call(t, "connections.list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var records []store.Saved
	if err := json.Unmarshal(listed, &records); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("list = %+v, want no records saved", records)
	}
}

func TestConnectionsSaveReturnsRPCErrorWhenTheStoreFails(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	st := store.New(filepath.Join(dir, "connections.json"), store.NewMemoryKeyring())
	srv := rpc.NewServer()
	RegisterConnections(srv, st)
	h := &harness{srv: srv}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod config dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "x", "driver": "sqlite", "file": "/tmp/x.db"},
		"password":   "",
	})
	if err == nil {
		t.Fatal("connections.save succeeded despite an unwritable config directory")
	}
}

// -- connections.delete -----------------------------------------------------

func TestConnectionsDeleteReturnsInvalidParamsOnBadJSON(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, "connections.delete", "not-an-object"); err == nil {
		t.Fatal("connections.delete succeeded on malformed params")
	}
}

func TestConnectionsDeleteSucceeds(t *testing.T) {
	h := newHarness(t)
	saved, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	var rec store.Saved
	if err := json.Unmarshal(saved, &rec); err != nil {
		t.Fatalf("decode saved: %v", err)
	}

	out, err := h.call(t, "connections.delete", map[string]any{"id": rec.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	var res struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode delete result: %v", err)
	}
	if !res.Deleted {
		t.Error("deleted = false, want true")
	}

	listed, err := h.call(t, "connections.list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var records []store.Saved
	_ = json.Unmarshal(listed, &records)
	if len(records) != 0 {
		t.Errorf("list after delete = %+v, want empty", records)
	}
}

func TestConnectionsDeleteReturnsRPCErrorForAnUnknownID(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "connections.delete", map[string]any{"id": "no-such-id"})
	if err == nil {
		t.Fatal("connections.delete succeeded for an unknown id")
	}
	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("err = %T, want *rpc.Error", err)
	}
	if re.Code != rpc.CodeDatabase {
		t.Errorf("code = %d, want %d", re.Code, rpc.CodeDatabase)
	}
}

// -- connections.test --------------------------------------------------------

func TestConnectionsTestReturnsInvalidParamsOnBadJSON(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, "connections.test", "not-an-object"); err == nil {
		t.Fatal("connections.test succeeded on malformed params")
	}
}

// dial's own driver.Lookup failure (as opposed to a registered driver's Open
// failing) is reached only through an id nothing ever registered.
func TestConnectionsTestReportsAnUnknownDriverAsAResultNotAnError(t *testing.T) {
	h := newHarness(t)
	out, err := h.call(t, "connections.test", map[string]any{
		"connection": map[string]any{"driver": "no-such-driver", "file": h.db},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("test returned a transport error: %v", err)
	}
	var res testResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true for an unregistered driver")
	}
	if res.Kind != string(dberr.KindUnsupported) {
		t.Errorf("kind = %q, want %q", res.Kind, dberr.KindUnsupported)
	}
}

// The real SQLite driver can't be made to open successfully and then fail a
// second, explicit Ping — its own Open already pings once. A fake driver
// isolates that branch: Open succeeds, and the connections.test handler's
// own conn.Ping call is what fails.
func TestConnectionsTestReportsAPingFailureAsAResultNotAnError(t *testing.T) {
	h := newHarness(t)
	id := registerFakeDriver(t, &fakeConn{pingErr: dberr.New(dberr.KindNetwork, "connection reset")}, nil)

	out, err := h.call(t, "connections.test", map[string]any{
		"connection": map[string]any{"driver": id, "file": "unused"},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("test returned a transport error: %v", err)
	}
	var res testResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true despite a failing ping")
	}
	if res.Kind != string(dberr.KindNetwork) {
		t.Errorf("kind = %q, want %q", res.Kind, dberr.KindNetwork)
	}
}

// -- session.open -------------------------------------------------------

func TestSessionOpenReturnsInvalidParamsOnBadJSON(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, "session.open", "not-an-object"); err == nil {
		t.Fatal("session.open succeeded on malformed params")
	}
}

func TestSessionOpenReturnsRPCErrorWhenTheStoreFails(t *testing.T) {
	srv := rpc.NewServer()
	sess := NewSessions()
	RegisterSession(srv, brokenStore(t), sess)
	t.Cleanup(sess.CloseAll)
	h := &harness{srv: srv}

	if _, err := h.call(t, "session.open", map[string]any{"connection_id": "whatever"}); err == nil {
		t.Fatal("session.open succeeded despite an unreadable store")
	}
}

func TestSessionOpenReturnsNotFoundForAnUnknownConnectionID(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "session.open", map[string]any{"connection_id": "no-such-id"})
	if err == nil {
		t.Fatal("session.open succeeded for an unknown connection id")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", kind, dberr.KindNotFound)
	}
	if len(h.sess.conns) != 0 {
		t.Errorf("session table has %d entries after a failed open, want 0", len(h.sess.conns))
	}
}

func TestSessionOpenReturnsRPCErrorWhenThePasswordLookupFails(t *testing.T) {
	dir := t.TempDir()
	st := store.New(filepath.Join(dir, "connections.json"), erroringGetKeyring{})
	sess := NewSessions()
	srv := rpc.NewServer()
	RegisterConnections(srv, st)
	RegisterSession(srv, st, sess)
	t.Cleanup(sess.CloseAll)
	h := &harness{srv: srv, st: st, sess: sess}

	// erroringGetKeyring's Set is a no-op that reports success, so Save
	// itself succeeds even though every subsequent Get (i.e. every
	// Store.Password call) will fail.
	rec, err := st.Save(store.Saved{Name: "x", Driver: "sqlite", File: "/tmp/whatever.db"}, "hunter2")
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	_, err = h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err == nil {
		t.Fatal("session.open succeeded despite the keyring refusing to return the password")
	}
	if len(sess.conns) != 0 {
		t.Errorf("session table has %d entries after a failed open, want 0", len(sess.conns))
	}
}

func TestSessionOpenReturnsRPCErrorWhenDialFails(t *testing.T) {
	h := newHarness(t)
	rec, err := h.st.Save(store.Saved{Name: "x", Driver: "no-such-driver", File: "unused"}, "")
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	_, err = h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err == nil {
		t.Fatal("session.open succeeded against an unregistered driver")
	}
	if len(h.sess.conns) != 0 {
		t.Errorf("session table has %d entries after a failed open, want 0", len(h.sess.conns))
	}
}

// Self-review: a failed session.open must not leak a connection or a
// session-table entry. When Introspect fails after a successful Open, the
// handler must close the connection it just opened and must never register
// it under a session id.
func TestSessionOpenClosesTheConnectionAndLeaksNoSessionWhenIntrospectFails(t *testing.T) {
	h := newHarness(t)
	conn := &fakeConn{introspectErr: dberr.New(dberr.KindUnknown, "introspection blew up")}
	id := registerFakeDriver(t, conn, nil)

	rec, err := h.st.Save(store.Saved{Name: "x", Driver: id, File: "unused"}, "")
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	_, err = h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err == nil {
		t.Fatal("session.open succeeded despite a failing Introspect")
	}
	if len(h.sess.conns) != 0 {
		t.Errorf("session table has %d entries after a failed open, want 0", len(h.sess.conns))
	}
	if !conn.closed {
		t.Error("connection was not closed after Introspect failed")
	}
}

// Coordinator-flagged: session.open must close the connection it just
// opened even when Introspect panics rather than returning a normal error —
// a driver bug, not something a well-behaved driver does, but the dispatch
// loop's own recover (see rpc.Server.dispatch) turns that panic into an
// ordinary CodeInternal response and lets the process keep running, so a
// leaked handle here would accumulate for the rest of the engine's life.
// This calls the handler directly (bypassing dispatch's recover, the same
// way harness.call does) and recovers locally instead, so the assertion
// below runs only after the panic has unwound through session.open's own
// deferred close.
func TestSessionOpenClosesTheConnectionWhenIntrospectPanics(t *testing.T) {
	h := newHarness(t)
	conn := &fakeConn{introspectPanics: true}
	id := registerFakeDriver(t, conn, nil)

	rec, err := h.st.Save(store.Saved{Name: "x", Driver: id, File: "unused"}, "")
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler, ok := h.srv.Handler("session.open")
	if !ok {
		t.Fatal("session.open is not registered")
	}
	params, err := json.Marshal(map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	func() {
		defer func() { _ = recover() }()
		_, _ = handler(context.Background(), params)
	}()

	if !conn.closed {
		t.Error("connection was not closed after Introspect panicked")
	}
	if len(h.sess.conns) != 0 {
		t.Errorf("session table has %d entries after a panicking open, want 0", len(h.sess.conns))
	}
}

// -- session.columns ----------------------------------------------------

func TestSessionColumnsReturnsInvalidParamsOnBadJSON(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, "session.columns", "not-an-object"); err == nil {
		t.Fatal("session.columns succeeded on malformed params")
	}
}

func TestSessionColumnsReturnsRPCErrorForAMissingTable(t *testing.T) {
	h := newHarness(t)
	saved, _ := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	var rec store.Saved
	_ = json.Unmarshal(saved, &rec)
	opened, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(opened, &open)

	_, err = h.call(t, "session.columns", map[string]any{
		"session_id": open.SessionID, "database": "main", "table": "does_not_exist",
	})
	if err == nil {
		t.Fatal("session.columns succeeded for a table that does not exist")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", kind, dberr.KindNotFound)
	}
}

// -- session.close --------------------------------------------------------

func TestSessionCloseReturnsInvalidParamsOnBadJSON(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, "session.close", "not-an-object"); err == nil {
		t.Fatal("session.close succeeded on malformed params")
	}
}

func TestSessionCloseReturnsNotFoundForAnUnknownSessionID(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "session.close", map[string]any{"session_id": "no-such-session"})
	if err == nil {
		t.Fatal("session.close succeeded for an unknown session id")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", kind, dberr.KindNotFound)
	}
}

func TestSessionCloseReturnsRPCErrorWhenCloseFails(t *testing.T) {
	h := newHarness(t)
	conn := &fakeConn{closeErr: dberr.New(dberr.KindUnknown, "close blew up")}
	id := registerFakeDriver(t, conn, nil)

	rec, err := h.st.Save(store.Saved{Name: "x", Driver: id, File: "unused"}, "")
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	opened, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(opened, &open)

	if _, err := h.call(t, "session.close", map[string]any{"session_id": open.SessionID}); err == nil {
		t.Fatal("session.close succeeded despite the connection refusing to close")
	}
	// remove() already took the entry out of the table before Close was
	// attempted, so even a failing Close does not leave a stale entry
	// behind — confirmed directly against the unexported map.
	if len(h.sess.conns) != 0 {
		t.Errorf("session table has %d entries after a failed close, want 0", len(h.sess.conns))
	}
}

// Self-review: concurrent session.open calls against the same connection
// must not corrupt the session registry — every caller must get back a
// distinct, individually valid session id, and the table's final size must
// equal the number of successful opens. Sessions.add takes its own lock
// around the map write, so this is also a -race regression guard for that
// lock actually being held across the read-modify-write.
func TestConcurrentSessionOpenCallsDoNotCorruptTheRegistry(t *testing.T) {
	h := newHarness(t)
	saved, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	var rec store.Saved
	_ = json.Unmarshal(saved, &rec)

	const n = 20
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
			if err != nil {
				errs[i] = err
				return
			}
			var res struct {
				SessionID string `json:"session_id"`
			}
			errs[i] = json.Unmarshal(out, &res)
			ids[i] = res.SessionID
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatalf("open %d: %v", i, errs[i])
		}
		if id == "" {
			t.Fatalf("open %d: empty session id", i)
		}
		if seen[id] {
			t.Fatalf("session id %q was handed out twice", id)
		}
		seen[id] = true
	}
	if len(h.sess.conns) != n {
		t.Errorf("session table has %d entries, want %d", len(h.sess.conns), n)
	}
}
