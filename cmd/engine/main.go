// Command engine is the database engine sidecar. It speaks newline-delimited
// JSON-RPC 2.0 on stdin and stdout and exits when stdin closes.
//
// stdout is reserved exclusively for the protocol. Diagnostics go to stderr.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/marlexladag/lantern/internal/api"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/health"
	"github.com/marlexladag/lantern/internal/rpc"

	// Blank-imported so the sqlite driver registers itself via its init
	// function (see driver.Register in registry.go). This is also what
	// closes the packaging gate's blind spot: before this import,
	// cmd/engine never linked modernc.org/sqlite, so
	// scripts/build-sidecars_test.sh's cross-compiles of this package were
	// never actually proving the pure-Go, CGO_ENABLED=0 property they exist
	// to police.
	_ "github.com/marlexladag/lantern/internal/engine/driver/sqlite"
)

// Set at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

// sessionCloseTimeout bounds how long the signal-handling goroutine below
// waits for closeSessionsOnSignal before exiting anyway. Short enough that a
// hung driver Close cannot make the engine ignore SIGTERM/SIGINT for any
// noticeable time, long enough that a real close (a SQLite handle flushing
// and releasing its file lock, say) has every realistic chance to finish
// first.
const sessionCloseTimeout = 2 * time.Second

// closeSessionsOnSignal closes every session still open in sess, bounded by
// timeout so a hung Close cannot block the process from exiting. It is
// deliberately not sess.CloseAll called inline: see the call site below for
// why this call lacks the drain guarantee CloseAll's own doc comment
// describes for main's ordinary `defer sess.CloseAll()`, and what that
// costs.
//
// If timeout elapses before every Close returns, this returns anyway and
// the goroutine still running sess.CloseAll is abandoned along with
// whatever handles were still mid-Close — leaked handles the OS reclaims on
// process exit, traded deliberately against a process that refuses to
// honour a termination signal, which is the entire reason the caller's
// signal-watcher goroutine exists (see its own comment). Only the handles
// still mid-Close are lost, not every handle the abandoned goroutine had
// yet to reach: CloseAll closes concurrently, precisely so that this
// timeout costs one slow driver rather than every session behind it (see
// CloseAll's own doc comment).
func closeSessionsOnSignal(sess *api.Sessions, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		sess.CloseAll()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	path, err := store.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine: %v\n", err)
		os.Exit(1)
	}
	st := store.New(path, store.OSKeyring())
	sess := api.NewSessions()

	// rpc.Serve only observes ctx cancellation between requests: a pending
	// read on an idle stdin will not wake up on its own (this is documented
	// on Serve). Without this goroutine, SIGINT/SIGTERM would cancel ctx but
	// the process would stay blocked reading stdin forever, making the
	// engine unkillable by Ctrl-C whenever the UI holds the pipe open
	// without writing to it.
	//
	// The obvious fix — close os.Stdin from this goroutine to unblock the
	// pending Read — was tried and measured to not work: on this platform,
	// Close from another goroutine does not interrupt a Read already
	// blocked on stdin (confirmed with a standalone repro against a pipe
	// stdin), and os.Stdin.SetReadDeadline fails outright with "file type
	// does not support deadline". Neither gives a dependable wakeup, so we
	// exit the process directly instead of trying to make Serve return on
	// its own.
	//
	// Failure mode: os.Exit skips every deferred cleanup in this function,
	// Serve's own deferred wg.Wait() included, so a request whose handler is
	// actively running at the moment the signal arrives is abandoned
	// mid-flight — its response, if any, may not reach stdout. That handler
	// is no longer hypothetical: session.open (internal/api/session.go)
	// dials a real database connection and keeps it open for the life of
	// the session. closeSessionsOnSignal below closes every session that is
	// already fully registered in sess.conns at the moment of signal, each
	// in its own goroutine, bounded by sessionCloseTimeout so a hung Close
	// cannot block this goroutine — and therefore the process — from
	// exiting. Concurrently and not in a loop for exactly that reason: the
	// timeout bounds the whole call, so a sequential loop would let the
	// first Close that never returns spend the entire budget while every
	// session behind it went unreached — and "closes every session" would
	// be false in precisely the scenario this code exists for.
	//
	// This is deliberately weaker than main's own `defer sess.CloseAll()`
	// further down: that defer only ever runs once rpc.Server.Serve has
	// returned, which is only possible once Serve's own `defer wg.Wait()`
	// has confirmed no dispatch — no session.open call included — is still
	// in flight (see sess.CloseAll's own doc comment for why that matters).
	// This goroutine cannot offer the same guarantee: Serve may never
	// return on its own while stdin sits idle, so there is nothing for it
	// to wait on before calling closeSessionsOnSignal. A session.open call
	// in flight when the signal lands can still leak its connection,
	// exactly the gap sess.CloseAll's own comment warns a
	// drain-guarantee-free caller would reopen — and the window is as wide
	// as the shutdown itself, not an instant. Two shapes of it:
	//
	//	a call between a successful dial and sess.add — which spans that
	//	call's own Introspect — adds its connection to the map CloseAll
	//	already swapped away, and nothing ever reaches it again;
	//
	//	a call that reaches sess.add at any point AFTER that swap — anywhere
	//	in the sessionCloseTimeout-wide span while the closes are running,
	//	or after they finish — registers into the fresh map, which nothing
	//	will close before os.Exit(0).
	//
	// Accepted deliberately: this closes every session that was registered
	// when the signal arrived, which is a strict improvement over the
	// previous behaviour of closing none of them.
	//
	// Note: this goroutine is not purely a signal-path mechanism. The
	// deferred stop() below unconditionally cancels ctx (that's how
	// signal.NotifyContext's stop works), so it also fires on the normal
	// stdin-close shutdown path, once Serve has already returned and main is
	// unwinding — racing harmlessly against main's own return, since by then
	// Serve's own defer wg.Wait() has already completed and there is nothing
	// left to lose. Worth remembering for whoever next reasons about
	// shutdown ordering here.
	go func() {
		<-ctx.Done()
		closeSessionsOnSignal(sess, sessionCloseTimeout)
		os.Exit(0)
	}()

	// Closed on the stdin-EOF shutdown path below (this defer runs when
	// Serve returns normally, unwinding main). The signal path above calls
	// os.Exit(0) directly and therefore also skips this defer, same as
	// every other deferred cleanup in this function — but that no longer
	// leaves every open session dangling: closeSessionsOnSignal, called
	// just before os.Exit(0) above, already closed what it could. Calling
	// sess.CloseAll a second time here would be harmless (it starts from an
	// already-empty map after the first call) but never happens, since
	// os.Exit(0) skips this defer entirely.
	defer sess.CloseAll()

	srv := rpc.NewServer()
	srv.Register("health", health.Handler(version, commit))
	api.RegisterConnections(srv, st)
	api.RegisterSession(srv, st, sess)
	api.RegisterBrowse(srv, sess)

	// Readiness marker: written once signal handling is registered and the
	// handler is bound, immediately before Serve starts reading. This is a
	// real happens-before edge that callers can synchronize on without
	// touching stdin — used by this package's own SIGTERM test, and by Task
	// 5's Rust shell, which pipes engine stderr into its own log. Always
	// stderr, never stdout: stdout is the JSON-RPC protocol stream.
	fmt.Fprintln(os.Stderr, "engine: ready")

	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "engine: %v\n", err)
		os.Exit(1)
	}
}
