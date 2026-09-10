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

	"github.com/marlexladag/tablepluslike/internal/health"
	"github.com/marlexladag/tablepluslike/internal/rpc"
)

// Set at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	// Failure mode: os.Exit skips Serve's deferred wg.Wait(), so a request
	// whose handler is actively running at the moment the signal arrives is
	// abandoned mid-flight — its response, if any, may not reach stdout.
	// Today's only handler (health) is synchronous and effectively
	// instantaneous, so the window is negligible; a future long-running
	// handler that must clean up (e.g. close a DB transaction) on shutdown
	// would need this revisited.
	go func() {
		<-ctx.Done()
		os.Exit(0)
	}()

	srv := rpc.NewServer()
	srv.Register("health", health.Handler(version, commit))

	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "engine: %v\n", err)
		os.Exit(1)
	}
}
