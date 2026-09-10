package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildEngine compiles the sidecar into the test's temp dir and returns its path.
func buildEngine(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "engine")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func TestEngineAnswersHealthOverStdio(t *testing.T) {
	cmd := exec.Command(buildEngine(t))
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
	cmd := exec.Command(buildEngine(t))
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
