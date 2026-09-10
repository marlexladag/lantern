package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildEngine compiles the sidecar into the test's temp dir and returns its
// path. It is built with -cover so that, combined with engineEnv's
// GOCOVERDIR, the subprocess this binary runs as can attribute coverage
// back to main.go — something `go test`'s own instrumentation cannot do,
// since main() only ever runs out-of-process here, driven over real pipes
// and real signals rather than called in-process. -coverpkg restricts
// instrumentation to this package alone: go build -cover's default is every
// package in the module, which would double-count internal/health and
// internal/rpc against the coverage go test already attributes to them
// in-process.
func buildEngine(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "engine")
	cmd := exec.Command("go", "build", "-cover", "-coverpkg=.", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

// engineEnv returns the environment a subprocess engine invocation should
// run with, directing the coverage data the -cover-built binary emits on
// exit to GOCOVERDIR.
//
// When the process running `go test` itself has GOCOVERDIR set — which a
// coverage-measuring caller (see scripts/go-coverage.sh) sets deliberately,
// since go test's own -coverprofile machinery does not propagate it to
// child processes on its own — that same directory is forwarded so every
// subprocess run in this package accumulates into it. Without that, a
// per-test t.TempDir() is used instead: the binary still runs identically,
// it just has nowhere durable to write counters, exactly like an
// uninstrumented run.
func engineEnv(t *testing.T) []string {
	t.Helper()
	dir := os.Getenv("GOCOVERDIR")
	if dir == "" {
		dir = t.TempDir()
	}
	return append(os.Environ(), "GOCOVERDIR="+dir)
}

func TestEngineAnswersHealthOverStdio(t *testing.T) {
	cmd := exec.Command(buildEngine(t))
	cmd.Env = engineEnv(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if _, err := stdin.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"health"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			Status  string `json:"status"`
			Version string `json:"version"`
			PID     int    `json:"pid"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("response %q is not valid JSON-RPC: %v", line, err)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
	if resp.Result.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Result.Status)
	}
	if resp.Result.PID == 0 {
		t.Error("pid was not reported")
	}
}

// Closing stdin is the shutdown signal. Without this the sidecar would
// outlive a crashed UI as an orphan process holding database connections.
func TestEngineExitsWhenStdinCloses(t *testing.T) {
	cmd := exec.Command(buildEngine(t))
	cmd.Env = engineEnv(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("engine exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("engine did not exit within 5s of stdin closing")
	}
}

// Serve only observes context cancellation between requests: a pending read
// on an idle stdin will not wake up on its own. Without a fix, SIGTERM (or
// Ctrl-C's SIGINT) would leave the engine blocked forever reading a stdin
// pipe that the UI holds open but never writes to, making it unkillable by
// anything short of SIGKILL. This test holds stdin open and silent, sends
// SIGTERM, and requires a prompt, clean (exit 0) shutdown with nothing
// written to stdout — stdout is the protocol stream and a shutdown must not
// contaminate it.
func TestEngineExitsOnSIGTERMWithIdleStdin(t *testing.T) {
	// syscall.SIGTERM is *defined* on Windows, so this file compiles there,
	// but Windows has no POSIX signal delivery: os.Process.Signal returns
	// "not supported by windows" for anything other than os.Kill. The test
	// would therefore always fail on a Windows developer's machine while
	// passing in CI (which runs Go tests on Linux only) - a failure that
	// says nothing about the code under test. Windows shutdown rides on
	// stdin EOF instead, which TestEngineExitsWhenStdinCloses covers on
	// every platform.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals are not deliverable on Windows; stdin EOF is the shutdown path there")
	}

	cmd := exec.Command(buildEngine(t))
	cmd.Env = engineEnv(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })

	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Wait for the engine's readiness marker on stderr before signaling,
	// rather than guessing at a fixed delay: main.go writes "engine: ready"
	// only after signal.NotifyContext has returned and the handler is
	// registered, immediately before it starts serving. That is a real
	// happens-before edge, so once we've read the line, SIGTERM is
	// guaranteed to hit an installed handler rather than racing Go's
	// default (process-terminating) disposition for it. stdin is never
	// touched to get this signal — only stderr.
	type readyResult struct {
		line string
		err  error
	}
	readyCh := make(chan readyResult, 1)
	go func() {
		line, err := bufio.NewReader(stderr).ReadString('\n')
		readyCh <- readyResult{line: line, err: err}
	}()

	select {
	case res := <-readyCh:
		if res.err != nil {
			t.Fatalf("reading readiness marker: %v", res.err)
		}
		if got := strings.TrimSpace(res.line); got != "engine: ready" {
			t.Fatalf("unexpected readiness line: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not print its readiness marker within 5s")
	}

	// stdin is deliberately never written to: the fix must not depend on
	// the stream producing any data.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("engine exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("engine did not exit within 5s of SIGTERM with idle stdin")
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout was not empty after signal shutdown: %q", stdout.Bytes())
	}
}

// A line past the protocol's 32 MiB cap desyncs the stream: rpc.Serve
// returns a non-nil error instead of the nil it returns on a clean
// stdin-close shutdown. main must tell the two apart — log the failure to
// stderr and exit non-zero — so the shell's supervisor can distinguish a
// crash from an intentional shutdown by the exit code alone, and must not
// let anything from that failure path leak onto stdout, which carries only
// the JSON-RPC protocol.
func TestEngineExitsNonZeroOnStreamCorruption(t *testing.T) {
	cmd := exec.Command(buildEngine(t))
	cmd.Env = engineEnv(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// internal/rpc caps a single protocol line at 32 MiB (maxMessageBytes
	// in codec.go); one byte past that, with no newline anywhere in sight,
	// forces bufio.Scanner into ErrTooLong, which the decoder maps to
	// ErrStreamCorrupted. Written from a goroutine: the pipe's kernel
	// buffer is far smaller than 32 MiB, so the write blocks on the engine
	// actually reading it, and the engine may exit (closing its end, and
	// thus our write) before every byte is consumed.
	go func() {
		oversized := bytes.Repeat([]byte("x"), 32<<20+1)
		_, _ = stdin.Write(oversized)
		_ = stdin.Close()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("engine exit error = %v, want *exec.ExitError", err)
		}
		if code := exitErr.ExitCode(); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("engine did not exit within 10s of an oversized line")
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout was not empty after a stream-corruption exit: %q", stdout.Bytes())
	}
}
