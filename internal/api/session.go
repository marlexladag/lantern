package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

// Sessions holds the live connections, keyed by an opaque session id.
type Sessions struct {
	mu    sync.Mutex
	conns map[string]driver.Conn
}

func NewSessions() *Sessions { return &Sessions{conns: make(map[string]driver.Conn)} }

// add files c under a fresh session id and returns it. As of Go 1.24,
// crypto/rand.Read is guaranteed never to return an error (see newID's
// identical reasoning in internal/engine/store's config.go, which this
// mirrors) — it crashes the program irrecoverably instead if the OS entropy
// source ever fails. There is deliberately no error return here on the
// declared 1.24 floor: one would be untestable dead code.
func (s *Sessions) add(c driver.Conn) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[id] = c
	return id
}

func (s *Sessions) get(id string) (driver.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if !ok {
		return nil, dberr.New(dberr.KindNotFound, "no open session with id "+id)
	}
	return c, nil
}

func (s *Sessions) remove(id string) (driver.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if ok {
		delete(s.conns, id)
	}
	return c, ok
}

// CloseAll closes every live connection. The entrypoint calls this on
// shutdown.
//
// CloseAll's own critical section (swapping in a fresh map under s.mu) is
// safe against a concurrent add on its own terms — the two can't corrupt
// the map. But CloseAll swapping the map out from under a session.open call
// that is still between dial and sess.add is a real problem it does not
// protect against: that call would go on to add its connection to the
// *old*, already-abandoned map, and CloseAll would return having reported
// every connection closed while one is quietly still open with nothing left
// that can ever reach it again.
//
// That holds for cmd/engine's `defer sess.CloseAll()` specifically, only
// because of an invariant enforced entirely outside this type: that call
// runs only after rpc.Server.Serve has returned, and Serve's own `defer
// wg.Wait()` (see server.go) guarantees every in-flight dispatch — every
// handler goroutine, session.open's included — has already finished by the
// time Serve returns. So by the time *that* call runs, there is no
// concurrent add left to race.
//
// cmd/engine also calls CloseAll a second way, from its signal-handling
// goroutine, deliberately without this drain guarantee — Serve may never
// return on its own while stdin sits idle, so there is nothing to wait on
// there before calling it. See that goroutine's own comment in
// cmd/engine/main.go for the narrow, timing-dependent gap this reopens (a
// session.open call caught between a successful dial and sess.add at the
// exact instant a signal arrives can still leak) and why it is an accepted
// trade rather than an oversight. A future caller that invokes CloseAll
// without either drain guarantee — the deferred one, or an explicit,
// documented accepted trade like the signal path's — would silently
// reintroduce this window with no such justification; this type does
// nothing on its own to stop that.
//
// The Closes run concurrently, one goroutine per conn, because a caller
// that bounds this call bounds the WHOLE call: cmd/engine's
// closeSessionsOnSignal gives it sessionCloseTimeout and then abandons it.
// Closed sequentially, a single driver Close that never returned would
// consume that entire budget and every session behind it in the map would
// simply never be reached — the process still exited on time, but healthy
// connections that would have flushed and unlocked in microseconds died
// unclosed instead. Concurrently, one hung Close costs only itself. The
// alternative — bounding each Close individually in here — was not taken:
// it would put a timeout policy in a type that has no business choosing
// one, and would still serialise the wait.
func (s *Sessions) CloseAll() {
	s.mu.Lock()
	conns := s.conns
	s.conns = make(map[string]driver.Conn)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Close()
		}()
	}
	// CloseAll still returns only once every Close has returned, so the
	// deferred call in cmd/engine keeps meaning what it always meant. What
	// changed is that a hung Close no longer holds the others hostage while
	// it does.
	wg.Wait()
}

type openParams struct {
	ConnectionID string `json:"connection_id"`
}

type openResult struct {
	SessionID string              `json:"session_id"`
	Catalog   *schema.Catalog     `json:"catalog"`
	Caps      driver.Capabilities `json:"capabilities"`
}

// tablesParams names the database whose table list to read. Database is not
// optional and has no default: driver.Conn.Tables treats an unknown database
// as an error precisely so a driver that ignores the parameter cannot pass
// for one that honours it, and a seam that quietly substituted "the only
// database" here would hide exactly that.
type tablesParams struct {
	SessionID string `json:"session_id"`
	Database  string `json:"database"`
}

type columnsParams struct {
	SessionID string `json:"session_id"`
	Database  string `json:"database"`
	Table     string `json:"table"`
}

type sessionParams struct {
	SessionID string `json:"session_id"`
}

// RegisterSession wires the session methods onto srv.
func RegisterSession(srv *rpc.Server, st *store.Store, sess *Sessions) {
	srv.Register("session.open", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p openParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.open: "+err.Error())
		}

		records, err := st.List()
		if err != nil {
			return nil, ToRPCError(err)
		}
		var rec store.Saved
		found := false
		for _, r := range records {
			if r.ID == p.ConnectionID {
				rec, found = r, true
				break
			}
		}
		if !found {
			return nil, ToRPCError(dberr.New(dberr.KindNotFound, "no connection with id "+p.ConnectionID))
		}

		password, err := st.Password(rec.ID)
		if err != nil {
			return nil, ToRPCError(err)
		}
		conn, drv, err := dial(ctx, rec, password)
		if err != nil {
			return nil, ToRPCError(err)
		}
		// Closes conn on every return from here on unless keepOpen is set,
		// which only happens once sess.add has taken ownership of it below.
		// A plain `if err != nil { conn.Close() }` after Introspect (the
		// brief's original shape) only closes on a normal error return: a
		// panic inside Introspect — a driver bug, not a normal path — would
		// unwind straight past it and leak the handle for the life of the
		// process, since the dispatch loop's recover turns the panic into a
		// CodeInternal response rather than crashing. defer runs during a
		// panic's unwind, so this closes either way — the same reasoning
		// connections.test's `defer conn.Close()` already relies on.
		keepOpen := false
		defer func() {
			if !keepOpen {
				_ = conn.Close()
			}
		}()

		catalog, err := conn.Introspect(ctx)
		if err != nil {
			return nil, ToRPCError(err)
		}
		id := sess.add(conn)
		keepOpen = true

		return openResult{SessionID: id, Catalog: catalog, Caps: drv.Capabilities()}, nil
	})

	// The second introspection tier (spec section 5): session.open returns
	// the database list alone, and one database's tables are read here, when
	// the user expands it.
	srv.Register("session.tables", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p tablesParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.tables: "+err.Error())
		}
		conn, err := sess.get(p.SessionID)
		if err != nil {
			return nil, ToRPCError(err)
		}
		tables, err := conn.Tables(ctx, p.Database)
		if err != nil {
			return nil, ToRPCError(err)
		}
		// Returned as the driver handed it over, deliberately not
		// re-normalized to a non-nil slice here: Conn.Tables already
		// guarantees non-nil for an empty database, and a second guard at
		// this seam would turn a driver that broke that contract into a
		// silently passing one — the shell would see [] and nobody would
		// ever learn the driver sends null.
		return tables, nil
	})

	srv.Register("session.columns", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p columnsParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.columns: "+err.Error())
		}
		conn, err := sess.get(p.SessionID)
		if err != nil {
			return nil, ToRPCError(err)
		}
		cols, err := conn.Columns(ctx, p.Database, p.Table)
		if err != nil {
			return nil, ToRPCError(err)
		}
		return cols, nil
	})

	srv.Register("session.close", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p sessionParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.close: "+err.Error())
		}
		conn, ok := sess.remove(p.SessionID)
		if !ok {
			return nil, ToRPCError(dberr.New(dberr.KindNotFound, "no open session with id "+p.SessionID))
		}
		if err := conn.Close(); err != nil {
			return nil, ToRPCError(err)
		}
		return map[string]bool{"closed": true}, nil
	})
}
