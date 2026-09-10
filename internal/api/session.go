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
// Today that can't happen only because of an invariant enforced entirely
// outside this type: cmd/engine's `defer sess.CloseAll()` runs only after
// rpc.Server.Serve has returned, and Serve's own `defer wg.Wait()` (see
// server.go) guarantees every in-flight dispatch — every handler goroutine,
// session.open's included — has already finished by the time Serve returns.
// So by the time CloseAll ever runs in this codebase, there is no concurrent
// add left to race. A future caller that invokes CloseAll without that same
// drain guarantee upstream (e.g. from a hypothetical "restart" RPC method
// that still has other requests in flight) would silently reintroduce this
// window; this type does nothing on its own to stop that.
func (s *Sessions) CloseAll() {
	s.mu.Lock()
	conns := s.conns
	s.conns = make(map[string]driver.Conn)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

type openParams struct {
	ConnectionID string `json:"connection_id"`
}

type openResult struct {
	SessionID string              `json:"session_id"`
	Catalog   *schema.Catalog     `json:"catalog"`
	Caps      driver.Capabilities `json:"capabilities"`
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
