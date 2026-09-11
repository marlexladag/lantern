package main

// package main (not main_test): this file proves closeSessionsOnSignal
// itself is correct by injecting a scripted driver.Conn, the same white-box
// approach internal/api/coverage_test.go uses for fakeConn/fakeDriver. That
// is necessary here, not just convenient: main_test.go's black-box tests
// drive the engine as a real subprocess with only the real sqlite driver
// linked (see main.go's blank import), which has no legitimate way to
// produce a session whose Close call never returns — the exact adversarial
// fixture A2-2 requires. Statement coverage of closeSessionsOnSignal itself
// does not depend on these tests, though: its select's two cases both have
// empty bodies, so there is nothing case-specific for `go tool cover` to
// track, and every statement that does exist (spawning the closer goroutine,
// calling sess.CloseAll, closing done) already runs on every call regardless
// of which case wins the race — including the ordinary SIGTERM path
// TestEngineExitsOnSIGTERMWithIdleStdin in main_test.go already exercises
// end-to-end through the real subprocess, which is what satisfies
// scripts/go-coverage.sh's subprocess-only coverage measurement for this
// package. These tests exist to prove behaviour, not to chase coverage.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marlexladag/lantern/internal/api"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

// spyConn is a driver.Conn whose Close records that it ran, and can
// optionally be made to hang until the test releases it — simulating a
// database handle whose Close never returns, which closeSessionsOnSignal
// must not wait out.
type spyConn struct {
	closed atomic.Bool
	// block, when non-nil, is received from before Close returns. Leave it
	// nil for an ordinary, immediately-returning Close.
	block chan struct{}
}

func (c *spyConn) Ping(context.Context) error { return nil }

func (c *spyConn) Introspect(context.Context) (*schema.Catalog, error) {
	return &schema.Catalog{Databases: []schema.Database{{Name: "main", Tables: []schema.Table{}}}}, nil
}

func (c *spyConn) Columns(context.Context, string, string) ([]schema.Column, error) {
	return nil, errors.New("spyConn: Columns not implemented")
}

func (c *spyConn) Query(context.Context, string, ...any) (driver.Cursor, error) {
	return nil, errors.New("spyConn: Query not implemented")
}

func (c *spyConn) Quote(ident string) string { return ident }

func (c *spyConn) Close() error {
	if c.block != nil {
		<-c.block
	}
	c.closed.Store(true)
	return nil
}

var _ driver.Conn = (*spyConn)(nil)

// spyDriver always hands back the one *spyConn it was built with.
type spyDriver struct {
	id   string
	conn *spyConn
}

func (d spyDriver) ID() string                        { return d.id }
func (d spyDriver) Capabilities() driver.Capabilities { return driver.Capabilities{} }
func (d spyDriver) RequiredFields(driver.ConnConfig) []string { return nil }
func (d spyDriver) Open(context.Context, driver.ConnConfig) (driver.Conn, error) {
	return d.conn, nil
}

var _ driver.Driver = spyDriver{}

// openSpySession registers conn under a driver id unique to the calling
// test (driver.Register panics on a repeat id), saves one connection using
// it, and drives session.open for real (through rpc.Server, the same
// handler main() wires up) so conn ends up genuinely held open inside a
// fresh *api.Sessions — not just placed there by reaching into an
// unexported field.
func openSpySession(t *testing.T, conn *spyConn) *api.Sessions {
	t.Helper()
	id := "spy:" + t.Name()
	driver.Register(spyDriver{id: id, conn: conn})

	dir := t.TempDir()
	st := store.New(filepath.Join(dir, "connections.json"), store.NewMemoryKeyring())
	rec, err := st.Save(store.Saved{Name: "fixture", Driver: id, Color: "#3d7d55"}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	sess := api.NewSessions()
	srv := rpc.NewServer()
	api.RegisterSession(srv, st, sess)

	h, ok := srv.Handler("session.open")
	if !ok {
		t.Fatal("session.open is not registered")
	}
	params, err := json.Marshal(map[string]string{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := h(context.Background(), params); err != nil {
		t.Fatalf("session.open: %v", err)
	}
	return sess
}

// Required adversarial test: a session open at signal time gets closed, and
// the close is proven to have actually happened — not merely that shutdown
// completed, which the pre-fix bare os.Exit(0) also achieves without
// closing anything.
func TestCloseSessionsOnSignalClosesAnOpenSession(t *testing.T) {
	conn := &spyConn{}
	sess := openSpySession(t, conn)

	closeSessionsOnSignal(sess, time.Second)

	if !conn.closed.Load() {
		t.Error("a session open at signal time was not closed")
	}
}

// The timeout bound itself: a Close that never returns must not hold up
// shutdown. This is the adversarial fixture A2-2 asks for — no existing
// test before this one ever produces a Close call that hangs.
func TestCloseSessionsOnSignalDoesNotWaitForAHungClose(t *testing.T) {
	conn := &spyConn{block: make(chan struct{})}
	// Released only once the test is done asserting, so the background
	// Close this leaves running unblocks during cleanup instead of leaking
	// for the rest of the test binary's life.
	t.Cleanup(func() { close(conn.block) })
	sess := openSpySession(t, conn)

	start := time.Now()
	closeSessionsOnSignal(sess, 50*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("closeSessionsOnSignal took %s, want it bounded near its 50ms timeout", elapsed)
	}
	// conn.Close is still parked on <-c.block at this point (nothing has
	// closed that channel yet), so closed must still read false — proving
	// closeSessionsOnSignal returned without waiting for Close, not that it
	// happened to finish quickly.
	if conn.closed.Load() {
		t.Error("conn.Close cannot have returned yet; block is still open")
	}
}
