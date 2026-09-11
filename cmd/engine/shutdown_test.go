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
	"fmt"
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
//
// announces and awaits together let a test require that two Closes OVERLAP:
// one conn announces that its Close has begun, another refuses to finish
// until it hears that announcement. A sequential CloseAll cannot satisfy
// that pairing in either map-iteration order, which is what makes the C-3
// test below deterministic rather than a coin flip on Go's randomised map
// ordering.
type spyConn struct {
	closed atomic.Bool
	// block, when non-nil, is received from before Close returns. Leave it
	// nil for an ordinary, immediately-returning Close.
	block chan struct{}
	// announces, when non-nil, is closed the instant Close begins.
	announces chan struct{}
	// awaits, when non-nil, must be closed before Close will return —
	// normally another conn's announces.
	awaits chan struct{}
	// abandon releases a Close still parked on awaits or block when the test
	// ends, so a deliberately stranded goroutine unwinds during cleanup
	// instead of leaking for the rest of the binary's life. newSpyConn sets
	// it; a nil channel simply never fires, which is why every select below
	// can name it unconditionally.
	abandon chan struct{}
}

// spyDriverSeq makes every registered spy driver id unique for the life of
// the test binary. driver.Register panics on a repeat id — deliberately, so a
// real duplicate is caught at startup rather than silently shadowing — and
// `go test -count=2` runs each test twice in ONE process, so an id derived
// only from the test name collides with itself on the second pass. That made
// the whole package unrunnable under -count>1, which is how you would
// normally smoke out an order-dependent or state-leaking test.
var spyDriverSeq atomic.Int64

// newSpyConn returns a conn whose Close returns immediately. Set block,
// announces or awaits on the result to make it hang or rendezvous.
func newSpyConn(t *testing.T) *spyConn {
	t.Helper()
	c := &spyConn{abandon: make(chan struct{})}
	t.Cleanup(func() { close(c.abandon) })
	return c
}

func (c *spyConn) Ping(context.Context) error { return nil }

func (c *spyConn) Introspect(context.Context) (*schema.Catalog, error) {
	return &schema.Catalog{Databases: []schema.Database{{Name: "main", Tables: []schema.Table{}}}}, nil
}

func (c *spyConn) Tables(context.Context, string) ([]schema.Table, error) {
	return nil, errors.New("spyConn: Tables not implemented")
}

func (c *spyConn) Columns(context.Context, string, string) ([]schema.Column, error) {
	return nil, errors.New("spyConn: Columns not implemented")
}

func (c *spyConn) Query(context.Context, string, ...any) (driver.Cursor, error) {
	return nil, errors.New("spyConn: Query not implemented")
}

func (c *spyConn) Quote(ident string) string { return ident }

func (c *spyConn) Close() error {
	if c.announces != nil {
		close(c.announces)
	}
	if c.awaits != nil {
		select {
		case <-c.awaits:
		case <-c.abandon:
		}
	}
	if c.block != nil {
		select {
		case <-c.block:
		case <-c.abandon:
		}
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

func (d spyDriver) ID() string                                { return d.id }
func (d spyDriver) Capabilities() driver.Capabilities         { return driver.Capabilities{} }
func (d spyDriver) RequiredFields(driver.ConnConfig) []string { return nil }
func (d spyDriver) Open(context.Context, driver.ConnConfig) (driver.Conn, error) {
	return d.conn, nil
}

var _ driver.Driver = spyDriver{}

// openSpySessions registers each conn under a driver id unique to the
// calling test and its position (driver.Register panics on a repeat id),
// saves one connection per driver, and drives session.open for real
// (through rpc.Server, the same handler main() wires up) so every conn ends
// up genuinely held open inside one fresh *api.Sessions — not just placed
// there by reaching into an unexported field.
func openSpySessions(t *testing.T, conns ...*spyConn) *api.Sessions {
	t.Helper()
	st := store.New(filepath.Join(t.TempDir(), "connections.json"), store.NewMemoryKeyring())
	sess := api.NewSessions()
	srv := rpc.NewServer()
	api.RegisterSession(srv, st, sess)

	h, ok := srv.Handler("session.open")
	if !ok {
		t.Fatal("session.open is not registered")
	}
	for i, conn := range conns {
		id := fmt.Sprintf("spy:%s#%d.%d", t.Name(), i, spyDriverSeq.Add(1))
		driver.Register(spyDriver{id: id, conn: conn})
		rec, err := st.Save(store.Saved{Name: fmt.Sprintf("fixture-%d", i), Driver: id, Color: "#3d7d55"}, "")
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		params, err := json.Marshal(map[string]string{"connection_id": rec.ID})
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		if _, err := h(context.Background(), params); err != nil {
			t.Fatalf("session.open: %v", err)
		}
	}
	return sess
}

// Required adversarial test: a session open at signal time gets closed, and
// the close is proven to have actually happened — not merely that shutdown
// completed, which the pre-fix bare os.Exit(0) also achieves without
// closing anything.
func TestCloseSessionsOnSignalClosesAnOpenSession(t *testing.T) {
	conn := newSpyConn(t)
	sess := openSpySessions(t, conn)

	closeSessionsOnSignal(sess, time.Second)

	if !conn.closed.Load() {
		t.Error("a session open at signal time was not closed")
	}
}

// The timeout bound itself: a Close that never returns must not hold up
// shutdown. This is the adversarial fixture A2-2 asks for — no existing
// test before this one ever produces a Close call that hangs.
func TestCloseSessionsOnSignalDoesNotWaitForAHungClose(t *testing.T) {
	conn := newSpyConn(t)
	// Never closed by the test: newSpyConn's abandon channel releases the
	// parked Close during cleanup, once the test is done asserting.
	conn.block = make(chan struct{})
	sess := openSpySessions(t, conn)

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

// C-3 repro. CloseAll used to close sequentially, so the first Close that
// never returned stranded every session behind it: the process still exited
// on time (closeSessionsOnSignal bounds the whole loop), but the healthy
// connections it was supposed to close were simply never reached, and their
// handles died with the process instead of being flushed and unlocked.
//
// The pairing below is what makes this deterministic. Go randomises map
// iteration order, so the obvious fixture — one hung conn, one healthy one,
// assert the healthy one closed — passes half the time against the very bug
// it exists to catch, depending on which conn the sequential loop happens to
// reach first. Instead the healthy conn refuses to finish until it has heard
// the hung one's Close begin, which no sequential order can deliver:
//
//	hung first    the hung Close parks forever, the healthy one is never
//	              reached, nothing is closed
//	healthy first the healthy Close waits for an announcement that can only
//	              come after it returns, so both park, and nothing is closed
//
// Only overlapping Closes satisfy it.
func TestCloseSessionsOnSignalDoesNotLetOneHungCloseStrandTheRest(t *testing.T) {
	hung := newSpyConn(t)
	hung.announces = make(chan struct{})
	hung.block = make(chan struct{})

	healthy := newSpyConn(t)
	healthy.awaits = hung.announces

	sess := openSpySessions(t, hung, healthy)

	closeSessionsOnSignal(sess, 200*time.Millisecond)

	if !healthy.closed.Load() {
		t.Error("the healthy session was NOT closed — one hung Close stranded it")
	}
	// The hung one is still parked on block, which nothing has closed: the
	// timeout is what ended the wait, not the Close finishing. Without this,
	// a CloseAll that simply got lucky on timing would look the same.
	if hung.closed.Load() {
		t.Error("the hung Close cannot have returned; nothing has released it")
	}
}
