# Walking Skeleton: Sidecar IPC and Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce an installable desktop application on macOS, Windows, and Linux whose Tauri shell spawns a Go sidecar, completes a JSON-RPC health handshake over stdio, displays the engine version, and visibly recovers when the sidecar is killed.

**Architecture:** A Go binary reads newline-delimited JSON-RPC 2.0 from stdin and writes responses to stdout. A Tauri v2 shell bundles that binary as an `externalBin` sidecar, spawns it at startup, correlates requests to responses by ID, and supervises the process. The React UI calls the engine through a single typed `request()` helper. No database code exists in this plan — the only RPC method is `health`.

**Tech Stack:** Go 1.23+ (stdlib only), Tauri v2 + `tauri-plugin-shell`, React 18 + TypeScript + Vite, Vitest, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md`

## Global Constraints

- Go 1.23 or later. **Standard library only in this plan** — no third-party Go dependencies.
- `CGO_ENABLED=0` for every Go build. v1 is pure Go so cross-compilation stays trivial (spec §2).
- **stdout carries the JSON-RPC protocol and nothing else.** All logging, diagnostics, and panics go to stderr. A stray `fmt.Println` corrupts the stream (spec §9).
- Transport is **stdio pipes, never a TCP socket** — the engine holds live database credentials, and a local listener is reachable by any process on the machine (spec §9).
- JSON-RPC 2.0 framing, newline-delimited (one JSON object per line, no embedded raw newlines since JSON escapes them).
- Six build targets: `{darwin,windows,linux} x {amd64,arm64}`.
- Tauri sidecar binaries must be named `engine-<rust-target-triple>` to match Tauri's resolution convention.
- Module path: `github.com/marlexladag/tablepluslike` (change once, in Task 1, if the repo lands elsewhere).
- Every task ends with a commit.

---

## File Structure

**Go engine (the sidecar):**

| File | Responsibility |
|---|---|
| `go.mod` | Module definition |
| `internal/rpc/message.go` | JSON-RPC 2.0 wire types and error codes |
| `internal/rpc/codec.go` | Newline-delimited framing: `Decoder`, `Encoder` |
| `internal/rpc/server.go` | Method registry, dispatch loop, error mapping |
| `internal/health/health.go` | The single `health` method handler |
| `cmd/engine/main.go` | Entrypoint: wires stdin/stdout, exits on stdin close |
| `scripts/build-sidecars.sh` | Cross-compiles all six targets with Tauri naming |

**Tauri shell:**

| File | Responsibility |
|---|---|
| `src-tauri/tauri.conf.json` | Bundle config, `externalBin` declaration |
| `src-tauri/capabilities/default.json` | Shell permission for the sidecar |
| `src-tauri/src/engine.rs` | Sidecar lifecycle, request/response correlation, supervision |
| `src-tauri/src/lib.rs` | Tauri app setup, command registration |

**React UI:**

| File | Responsibility |
|---|---|
| `src/lib/engine.ts` | Typed RPC client over the Tauri command |
| `src/lib/engine.test.ts` | Vitest coverage for the client |
| `src/components/EngineStatus.tsx` | Renders health, version, supervision state |
| `src/App.tsx` | Mounts `EngineStatus` |

**CI:**

| File | Responsibility |
|---|---|
| `.github/workflows/build.yml` | Test + six-target matrix build |

---

### Task 1: JSON-RPC wire types and newline framing

**Files:**
- Create: `go.mod`
- Create: `internal/rpc/message.go`
- Create: `internal/rpc/codec.go`
- Test: `internal/rpc/codec_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  - `rpc.Request{JSONRPC string, ID *json.RawMessage, Method string, Params json.RawMessage}`
  - `rpc.Response{JSONRPC string, ID *json.RawMessage, Result json.RawMessage, Error *rpc.Error}`
  - `rpc.Error{Code int, Message string, Data json.RawMessage}`
  - Constants `rpc.CodeParse = -32700`, `CodeInvalidRequest = -32600`, `CodeMethodNotFound = -32601`, `CodeInvalidParams = -32602`, `CodeInternal = -32603`
  - `rpc.NewDecoder(io.Reader) *Decoder` with `Decode() (*Request, error)`
  - `rpc.NewEncoder(io.Writer) *Encoder` with `Encode(v any) error` — safe for concurrent use
  - `rpc.ErrParse` sentinel, returned by `Decode` on malformed JSON

- [ ] **Step 1: Initialise the Go module**

```bash
go mod init github.com/marlexladag/tablepluslike
```

- [ ] **Step 2: Write the failing test**

Create `internal/rpc/codec_test.go`:

```go
package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestDecodeReadsOneRequestPerLine(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"health"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping","params":{"n":3}}` + "\n")
	d := NewDecoder(in)

	first, err := d.Decode()
	if err != nil {
		t.Fatalf("first decode: %v", err)
	}
	if first.Method != "health" {
		t.Errorf("method = %q, want health", first.Method)
	}
	if string(*first.ID) != "1" {
		t.Errorf("id = %s, want 1", *first.ID)
	}

	second, err := d.Decode()
	if err != nil {
		t.Fatalf("second decode: %v", err)
	}
	if second.Method != "ping" {
		t.Errorf("method = %q, want ping", second.Method)
	}
	if string(second.Params) != `{"n":3}` {
		t.Errorf("params = %s, want {\"n\":3}", second.Params)
	}
}

func TestDecodeSkipsBlankLines(t *testing.T) {
	in := strings.NewReader("\n  \n" + `{"jsonrpc":"2.0","id":1,"method":"health"}` + "\n")
	got, err := NewDecoder(in).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Method != "health" {
		t.Errorf("method = %q, want health", got.Method)
	}
}

func TestDecodeReturnsErrParseOnMalformedJSON(t *testing.T) {
	_, err := NewDecoder(strings.NewReader("{not json\n")).Decode()
	if !errors.Is(err, ErrParse) {
		t.Fatalf("err = %v, want ErrParse", err)
	}
}

func TestDecodeReturnsEOFWhenStreamCloses(t *testing.T) {
	_, err := NewDecoder(strings.NewReader("")).Decode()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

// A notification has no ID and must decode with ID == nil, so the server can
// tell it apart from a request that requires a response.
func TestDecodeNotificationHasNilID(t *testing.T) {
	got, err := NewDecoder(strings.NewReader(`{"jsonrpc":"2.0","method":"cancel"}` + "\n")).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != nil {
		t.Errorf("ID = %v, want nil", got.ID)
	}
}

func TestEncodeWritesOneJSONObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)
	id := json.RawMessage("7")
	if err := e.Encode(&Response{JSONRPC: "2.0", ID: &id, Result: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output %q does not end in a newline", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("output %q contains more than one line", out)
	}
	var back Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if string(back.Result) != `{"ok":true}` {
		t.Errorf("result = %s", back.Result)
	}
}

// Notifications are pushed from query goroutines while responses are being
// written. Every line must still be a complete, parseable JSON object.
func TestEncodeIsSafeForConcurrentUse(t *testing.T) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := json.RawMessage(json.Number(string(rune('0' + n%10))).String())
			_ = e.Encode(&Response{JSONRPC: "2.0", ID: &id, Result: json.RawMessage(`{"ok":true}`)})
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines, want 50", len(lines))
	}
	for i, line := range lines {
		var back Response
		if err := json.Unmarshal([]byte(line), &back); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/rpc/ -v`
Expected: FAIL — build error, `undefined: NewDecoder`, `undefined: Response`, etc.

- [ ] **Step 4: Write the wire types**

Create `internal/rpc/message.go`:

```go
// Package rpc implements newline-delimited JSON-RPC 2.0 over a byte stream.
//
// The engine speaks this protocol on stdin and stdout. Nothing else may be
// written to stdout: a single stray write corrupts the stream for the shell.
package rpc

import "encoding/json"

// Version is the only JSON-RPC version this implementation accepts.
const Version = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// Request is an incoming call. A nil ID marks a notification, which must not
// receive a response.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// IsNotification reports whether the request expects no response.
func (r *Request) IsNotification() bool { return r.ID == nil }

// Response is an outgoing reply. Exactly one of Result or Error is set.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Errorf builds an Error with no data payload.
func Errorf(code int, msg string) *Error {
	return &Error{Code: code, Message: msg}
}
```

- [ ] **Step 5: Write the codec**

Create `internal/rpc/codec.go`:

```go
package rpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// maxMessageBytes caps a single protocol line. Result pages are the largest
// realistic message; 32 MiB leaves generous headroom over the windowed page
// sizes the engine actually sends.
const maxMessageBytes = 32 << 20

// ErrParse reports a line that was not valid JSON. The connection stays usable:
// the server replies with a parse error and reads the next line.
var ErrParse = errors.New("rpc: malformed JSON")

// Decoder reads newline-delimited requests from a stream.
type Decoder struct {
	sc *bufio.Scanner
}

func NewDecoder(r io.Reader) *Decoder {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxMessageBytes)
	return &Decoder{sc: sc}
}

// Decode returns the next request. It returns io.EOF when the stream closes,
// which is the engine's shutdown signal, and ErrParse on a malformed line.
func (d *Decoder) Decode() (*Request, error) {
	for d.sc.Scan() {
		line := bytes.TrimSpace(d.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			return nil, ErrParse
		}
		return &req, nil
	}
	if err := d.sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// Encoder writes newline-delimited messages. It is safe for concurrent use so
// that pushed notifications cannot interleave mid-line with a response.
type Encoder struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: bufio.NewWriter(w)}
}

// Encode marshals v and writes it as one line, flushing before it returns.
func (e *Encoder) Encode(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(b); err != nil {
		return err
	}
	return e.w.Flush()
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/rpc/ -v`
Expected: PASS — all seven tests green.

- [ ] **Step 7: Commit**

```bash
git add go.mod internal/rpc/
git commit -m "feat(rpc): add JSON-RPC 2.0 wire types and newline framing"
```

---

### Task 2: Method registry and dispatch loop

**Files:**
- Create: `internal/rpc/server.go`
- Test: `internal/rpc/server_test.go`

**Interfaces:**
- Consumes: `rpc.Request`, `rpc.Response`, `rpc.Error`, `rpc.NewDecoder`, `rpc.NewEncoder`, `rpc.ErrParse`, the `Code*` constants (Task 1)
- Produces:
  - `type Handler func(ctx context.Context, params json.RawMessage) (any, error)`
  - `rpc.NewServer() *Server`
  - `(*Server).Register(method string, h Handler)`
  - `(*Server).Serve(ctx context.Context, r io.Reader, w io.Writer) error` — returns nil on clean stream close

- [ ] **Step 1: Write the failing test**

Create `internal/rpc/server_test.go`:

```go
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// serve runs the server over an in-memory stream and returns the decoded
// responses, in the order they were written.
func serve(t *testing.T, s *Server, input string) []Response {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []Response
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var r Response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("undecodable response %q: %v", line, err)
		}
		got = append(got, r)
	}
	return got
}

func TestServeDispatchesToRegisteredHandler(t *testing.T) {
	s := NewServer()
	s.Register("echo", func(_ context.Context, params json.RawMessage) (any, error) {
		var in struct{ Text string }
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		return map[string]string{"text": in.Text}, nil
	})

	got := serve(t, s, `{"jsonrpc":"2.0","id":1,"method":"echo","params":{"text":"hi"}}`+"\n")
	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1", len(got))
	}
	if string(got[0].Result) != `{"text":"hi"}` {
		t.Errorf("result = %s", got[0].Result)
	}
	if got[0].Error != nil {
		t.Errorf("unexpected error: %v", got[0].Error)
	}
}

func TestServeReturnsMethodNotFound(t *testing.T) {
	got := serve(t, NewServer(), `{"jsonrpc":"2.0","id":1,"method":"nope"}`+"\n")
	if len(got) != 1 || got[0].Error == nil {
		t.Fatalf("want one error response, got %+v", got)
	}
	if got[0].Error.Code != CodeMethodNotFound {
		t.Errorf("code = %d, want %d", got[0].Error.Code, CodeMethodNotFound)
	}
}

func TestServeReportsParseErrorAndKeepsReading(t *testing.T) {
	s := NewServer()
	s.Register("health", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"status": "ok"}, nil
	})

	got := serve(t, s, "{not json\n"+`{"jsonrpc":"2.0","id":2,"method":"health"}`+"\n")
	if len(got) != 2 {
		t.Fatalf("got %d responses, want 2", len(got))
	}
	if got[0].Error == nil || got[0].Error.Code != CodeParse {
		t.Errorf("first response = %+v, want parse error", got[0])
	}
	if got[1].Error != nil {
		t.Errorf("second response should have succeeded: %+v", got[1].Error)
	}
}

// A handler returning a plain error is an internal error; returning an *Error
// passes the caller's code through unchanged.
func TestServeMapsHandlerErrors(t *testing.T) {
	s := NewServer()
	s.Register("boom", func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("exploded")
	})
	s.Register("badparams", func(context.Context, json.RawMessage) (any, error) {
		return nil, Errorf(CodeInvalidParams, "n must be positive")
	})

	got := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"boom"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"badparams"}`+"\n")
	if len(got) != 2 {
		t.Fatalf("got %d responses, want 2", len(got))
	}
	byID := map[string]Response{}
	for _, r := range got {
		byID[string(*r.ID)] = r
	}
	if byID["1"].Error.Code != CodeInternal {
		t.Errorf("boom code = %d, want %d", byID["1"].Error.Code, CodeInternal)
	}
	if byID["2"].Error.Code != CodeInvalidParams {
		t.Errorf("badparams code = %d, want %d", byID["2"].Error.Code, CodeInvalidParams)
	}
	if byID["2"].Error.Message != "n must be positive" {
		t.Errorf("badparams message = %q", byID["2"].Error.Message)
	}
}

func TestServeSendsNoResponseToNotification(t *testing.T) {
	s := NewServer()
	var called bool
	var mu sync.Mutex
	s.Register("cancel", func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		called = true
		mu.Unlock()
		return nil, nil
	})

	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","method":"cancel"}`+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Error("handler was not invoked")
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("notification produced output: %q", out.String())
	}
}

// A slow handler must not block later requests: dispatch is concurrent.
func TestServeDispatchesConcurrently(t *testing.T) {
	s := NewServer()
	release := make(chan struct{})
	s.Register("slow", func(context.Context, json.RawMessage) (any, error) {
		<-release
		return "slow", nil
	})
	s.Register("fast", func(context.Context, json.RawMessage) (any, error) {
		return "fast", nil
	})

	pr, pw := io.Pipe()
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), pr, &out) }()

	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"slow"}` + "\n"))
	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"fast"}` + "\n"))

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(out.String(), `"fast"`) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fast response did not arrive while slow handler was blocked")
		case <-time.After(5 * time.Millisecond):
		}
	}

	close(release)
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rpc/ -run TestServe -v`
Expected: FAIL — `undefined: NewServer`.

- [ ] **Step 3: Write the server**

Create `internal/rpc/server.go`:

```go
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Handler executes one method. Returning an *Error controls the wire code;
// any other error becomes CodeInternal.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server dispatches JSON-RPC requests to registered handlers.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewServer() *Server {
	return &Server{handlers: make(map[string]Handler)}
}

// Register binds a handler to a method name, replacing any previous binding.
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

func (s *Server) handler(method string) (Handler, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.handlers[method]
	return h, ok
}

// Serve reads requests until the stream closes, dispatching each in its own
// goroutine so a slow method cannot stall the ones behind it. It returns nil
// on a clean close, which is how the shell signals shutdown.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	dec := NewDecoder(r)
	enc := NewEncoder(w)

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		req, err := dec.Decode()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, ErrParse):
			// The ID is unknowable on a malformed line, so the response
			// carries a null ID, as JSON-RPC 2.0 requires.
			_ = enc.Encode(&Response{JSONRPC: Version, Error: Errorf(CodeParse, "malformed JSON")})
			continue
		case err != nil:
			return err
		}

		if ctx.Err() != nil {
			return nil
		}

		wg.Add(1)
		go func(req *Request) {
			defer wg.Done()
			s.dispatch(ctx, enc, req)
		}(req)
	}
}

func (s *Server) dispatch(ctx context.Context, enc *Encoder, req *Request) {
	reply := func(resp *Response) {
		if req.IsNotification() {
			return
		}
		resp.JSONRPC = Version
		resp.ID = req.ID
		_ = enc.Encode(resp)
	}

	h, ok := s.handler(req.Method)
	if !ok {
		reply(&Response{Error: Errorf(CodeMethodNotFound, "unknown method: "+req.Method)})
		return
	}

	result, err := h(ctx, req.Params)
	if err != nil {
		var rerr *Error
		if !errors.As(err, &rerr) {
			rerr = Errorf(CodeInternal, err.Error())
		}
		reply(&Response{Error: rerr})
		return
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		reply(&Response{Error: Errorf(CodeInternal, "cannot encode result: "+err.Error())})
		return
	}
	reply(&Response{Result: encoded})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/rpc/ -race -v`
Expected: PASS — all tests green, no data races reported.

- [ ] **Step 5: Commit**

```bash
git add internal/rpc/server.go internal/rpc/server_test.go
git commit -m "feat(rpc): add method registry and concurrent dispatch loop"
```

---

### Task 3: The health method and the sidecar entrypoint

**Files:**
- Create: `internal/health/health.go`
- Create: `cmd/engine/main.go`
- Test: `internal/health/health_test.go`
- Test: `cmd/engine/main_test.go`

**Interfaces:**
- Consumes: `rpc.NewServer`, `rpc.Handler`, `(*Server).Register`, `(*Server).Serve` (Task 2)
- Produces:
  - `health.Info{Status string, Version string, Commit string, PID int}` serialised as `{"status","version","commit","pid"}`
  - `health.Handler(version, commit string) rpc.Handler`
  - A `cmd/engine` binary that serves on stdin/stdout and exits 0 when stdin closes
  - Build-time variables `main.version` and `main.commit`, set via `-ldflags -X`

- [ ] **Step 1: Write the failing handler test**

Create `internal/health/health_test.go`:

```go
package health

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestHandlerReportsVersionAndPID(t *testing.T) {
	result, err := Handler("1.2.3", "abc123")(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Info
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got.Version)
	}
	if got.Commit != "abc123" {
		t.Errorf("commit = %q, want abc123", got.Commit)
	}
	if got.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", got.PID, os.Getpid())
	}
}

func TestHandlerJSONFieldNames(t *testing.T) {
	result, _ := Handler("v", "c")(context.Background(), nil)
	encoded, _ := json.Marshal(result)

	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"status", "version", "commit", "pid"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in %s", key, encoded)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/health/ -v`
Expected: FAIL — `undefined: Handler`.

- [ ] **Step 3: Write the health handler**

Create `internal/health/health.go`:

```go
// Package health implements the engine's liveness method. The shell calls it
// as a startup handshake before showing the main window, and periodically
// afterwards to detect a hung engine.
package health

import (
	"context"
	"encoding/json"
	"os"

	"github.com/marlexladag/tablepluslike/internal/rpc"
)

// Info is the health method's result.
type Info struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	PID     int    `json:"pid"`
}

// Handler returns the health method, closing over the build metadata. It
// ignores its params so that callers may send none.
func Handler(version, commit string) rpc.Handler {
	return func(context.Context, json.RawMessage) (any, error) {
		return Info{
			Status:  "ok",
			Version: version,
			Commit:  commit,
			PID:     os.Getpid(),
		}, nil
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/health/ -v`
Expected: PASS — both tests green.

- [ ] **Step 5: Write the failing entrypoint test**

This drives the real compiled binary over real pipes, which is the only way to
catch a stdout contamination bug.

Create `cmd/engine/main_test.go`:

```go
package main_test

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
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
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./cmd/engine/ -v`
Expected: FAIL — the build step fails because `cmd/engine/main.go` does not exist.

- [ ] **Step 7: Write the entrypoint**

Create `cmd/engine/main.go`:

```go
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

	srv := rpc.NewServer()
	srv.Register("health", health.Handler(version, commit))

	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "engine: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 8: Run the full Go suite to verify it passes**

Run: `go test ./... -race -v`
Expected: PASS — rpc, health, and both engine integration tests green.

- [ ] **Step 9: Commit**

```bash
git add internal/health/ cmd/engine/
git commit -m "feat(engine): add health method and stdio sidecar entrypoint"
```

---

### Task 4: Cross-compile all six sidecar targets

**Files:**
- Create: `scripts/build-sidecars.sh`
- Test: `scripts/build-sidecars_test.sh`

**Interfaces:**
- Consumes: the `cmd/engine` package (Task 3)
- Produces: six binaries at `src-tauri/binaries/engine-<triple>`, where `<triple>` is the Rust target triple Tauri appends when resolving a sidecar. Task 5's `tauri.conf.json` declares `binaries/engine` and relies on exactly these names.

The triple mapping this script must implement:

| GOOS | GOARCH | Output name |
|---|---|---|
| darwin | amd64 | `engine-x86_64-apple-darwin` |
| darwin | arm64 | `engine-aarch64-apple-darwin` |
| linux | amd64 | `engine-x86_64-unknown-linux-gnu` |
| linux | arm64 | `engine-aarch64-unknown-linux-gnu` |
| windows | amd64 | `engine-x86_64-pc-windows-msvc.exe` |
| windows | arm64 | `engine-aarch64-pc-windows-msvc.exe` |

- [ ] **Step 1: Write the failing test**

Create `scripts/build-sidecars_test.sh`:

```bash
#!/usr/bin/env bash
# Verifies that the sidecar build produces exactly the six binaries Tauri
# expects, correctly named, non-empty, and executable.
set -euo pipefail

cd "$(dirname "$0")/.."
OUT_DIR="src-tauri/binaries"

rm -rf "$OUT_DIR"
./scripts/build-sidecars.sh

expected=(
  "engine-x86_64-apple-darwin"
  "engine-aarch64-apple-darwin"
  "engine-x86_64-unknown-linux-gnu"
  "engine-aarch64-unknown-linux-gnu"
  "engine-x86_64-pc-windows-msvc.exe"
  "engine-aarch64-pc-windows-msvc.exe"
)

fail=0
for name in "${expected[@]}"; do
  path="$OUT_DIR/$name"
  if [[ ! -f "$path" ]]; then
    echo "FAIL: missing $path"
    fail=1
    continue
  fi
  if [[ ! -s "$path" ]]; then
    echo "FAIL: $path is empty"
    fail=1
  fi
  if [[ ! -x "$path" ]]; then
    echo "FAIL: $path is not executable"
    fail=1
  fi
done

actual_count=$(find "$OUT_DIR" -maxdepth 1 -type f | wc -l | tr -d ' ')
if [[ "$actual_count" != "6" ]]; then
  echo "FAIL: expected 6 binaries, found $actual_count"
  fail=1
fi

if [[ "$fail" != "0" ]]; then
  echo "sidecar build test FAILED"
  exit 1
fi
echo "sidecar build test PASSED: 6/6 targets"
```

Make both scripts executable up front:

```bash
chmod +x scripts/build-sidecars_test.sh
```

- [ ] **Step 2: Run it to verify it fails**

Run: `./scripts/build-sidecars_test.sh`
Expected: FAIL — `./scripts/build-sidecars.sh: No such file or directory`.

- [ ] **Step 3: Write the build script**

Create `scripts/build-sidecars.sh`:

```bash
#!/usr/bin/env bash
# Cross-compiles the engine sidecar for every target Tauri bundles.
#
# Tauri resolves an externalBin entry "binaries/engine" by appending the host's
# Rust target triple, so the file names below are a hard contract with
# src-tauri/tauri.conf.json — do not rename them independently.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT_DIR="src-tauri/binaries"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"

# goos goarch rust-triple suffix
TARGETS=(
  "darwin  amd64 x86_64-apple-darwin         "
  "darwin  arm64 aarch64-apple-darwin        "
  "linux   amd64 x86_64-unknown-linux-gnu    "
  "linux   arm64 aarch64-unknown-linux-gnu   "
  "windows amd64 x86_64-pc-windows-msvc  .exe"
  "windows arm64 aarch64-pc-windows-msvc .exe"
)

mkdir -p "$OUT_DIR"

for target in "${TARGETS[@]}"; do
  read -r goos goarch triple suffix <<<"$target"
  suffix="${suffix:-}"
  out="${OUT_DIR}/engine-${triple}${suffix}"

  echo "building ${goos}/${goarch} -> ${out}"
  # CGO stays off so these cross-compile from any host with no toolchain setup.
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/engine
  chmod +x "$out"
done

echo "built ${#TARGETS[@]} sidecar binaries into ${OUT_DIR}"
```

Make it executable:

```bash
chmod +x scripts/build-sidecars.sh
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/build-sidecars_test.sh`
Expected: PASS — `sidecar build test PASSED: 6/6 targets`.

- [ ] **Step 5: Verify the native binary actually runs**

Run:
```bash
printf '{"jsonrpc":"2.0","id":1,"method":"health"}\n' | \
  ./src-tauri/binaries/engine-$(uname -m | sed 's/x86_64/x86_64/;s/arm64/aarch64/')-apple-darwin
```
(On Linux substitute the matching `-unknown-linux-gnu` binary.)
Expected: one line of JSON containing `"status":"ok"` and a non-zero `"pid"`.

- [ ] **Step 6: Ignore build output, then commit**

```bash
printf 'src-tauri/binaries/\n' >> .gitignore
git add .gitignore scripts/
git commit -m "build: cross-compile engine sidecar for six Tauri targets"
```

---

### Task 5: Tauri scaffold with the sidecar bundled and spawned

**Files:**
- Create: `package.json`, `vite.config.ts`, `index.html`, `src/main.tsx`, `src/App.tsx` (scaffolded)
- Create: `src-tauri/Cargo.toml`, `src-tauri/tauri.conf.json`, `src-tauri/src/main.rs` (scaffolded)
- Create: `src-tauri/src/engine.rs`
- Modify: `src-tauri/src/lib.rs`
- Modify: `src-tauri/tauri.conf.json` (add `externalBin`)

**Interfaces:**
- Consumes: the six binaries in `src-tauri/binaries/` (Task 4)
- Produces:
  - Rust `engine::Engine` with `Engine::new(AppHandle) -> Arc<Engine>`, `(&Arc<Engine>).spawn() -> Result<(), String>`, `(&Engine).request(String, Option<Value>) -> Result<Value, String>`
  - Tauri command `engine_request(method: String, params: Option<Value>) -> Result<Value, String>`, invoked from TS as `invoke('engine_request', { method, params })`
  - Tauri event `engine://status` with payload `"ready" | "restarting" | "down"`

**Note on capabilities:** Tauri v2 capability files gate the *JavaScript* shell API. The sidecar is spawned from Rust in `setup()`, which is not gated, so no `shell:` permission entry is required. Do not add one.

- [ ] **Step 1: Scaffold the Tauri app into the repo root**

```bash
npm create tauri-app@latest .scaffold -- --template react-ts --manager npm --yes
rsync -a --exclude .gitignore --exclude .git .scaffold/ ./
cat .scaffold/.gitignore >> .gitignore
rm -rf .scaffold
npm install
```

- [ ] **Step 2: Add the Rust dependencies**

```bash
cd src-tauri
cargo add tauri-plugin-shell
cargo add tokio --features sync,time
cargo add serde_json
cd ..
```

- [ ] **Step 3: Declare the sidecar in the bundle config**

Edit `src-tauri/tauri.conf.json` and add `externalBin` inside the `bundle` object, leaving every other key as scaffolded:

```json
{
  "bundle": {
    "active": true,
    "targets": "all",
    "externalBin": ["binaries/engine"]
  }
}
```

- [ ] **Step 4: Write the engine module**

Create `src-tauri/src/engine.rs`:

```rust
//! Sidecar lifecycle and JSON-RPC correlation.
//!
//! The Go engine speaks newline-delimited JSON-RPC on stdio. This module owns
//! the child process, matches responses to requests by ID, and restarts the
//! engine if it dies.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::Value;
use tauri::{AppHandle, Emitter};
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;
use tokio::sync::oneshot;

/// How long a single request may wait before the caller gives up.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);
/// Pause before relaunching a crashed engine, so a crash loop cannot spin.
const RESTART_DELAY: Duration = Duration::from_millis(500);

pub struct Engine {
    app: AppHandle,
    next_id: AtomicU64,
    pending: Mutex<HashMap<u64, oneshot::Sender<Value>>>,
    child: Mutex<Option<CommandChild>>,
}

impl Engine {
    pub fn new(app: AppHandle) -> Arc<Self> {
        Arc::new(Self {
            app,
            next_id: AtomicU64::new(1),
            pending: Mutex::new(HashMap::new()),
            child: Mutex::new(None),
        })
    }

    fn set_state(&self, state: &str) {
        let _ = self.app.emit("engine://status", state);
    }

    /// Rejects every in-flight request. Called when the engine dies, so the UI
    /// sees an error instead of hanging until the timeout.
    fn fail_all_pending(&self, reason: &str) {
        let mut pending = self.pending.lock().unwrap();
        for (_, tx) in pending.drain() {
            let _ = tx.send(serde_json::json!({
                "error": { "code": -32603, "message": reason }
            }));
        }
    }

    /// Launches the sidecar and starts the reader task. Safe to call again
    /// after a crash.
    pub fn spawn(self: &Arc<Self>) -> Result<(), String> {
        let command = self
            .app
            .shell()
            .sidecar("engine")
            .map_err(|e| format!("cannot resolve sidecar: {e}"))?;

        let (mut rx, child) = command
            .spawn()
            .map_err(|e| format!("cannot spawn sidecar: {e}"))?;

        *self.child.lock().unwrap() = Some(child);
        self.set_state("ready");

        let this = Arc::clone(self);
        tauri::async_runtime::spawn(async move {
            while let Some(event) = rx.recv().await {
                match event {
                    // tauri-plugin-shell emits one event per line, with the
                    // terminator stripped. If a future version switches to raw
                    // chunks, the health handshake will simply never resolve —
                    // the fix would be to buffer until a newline arrives.
                    CommandEvent::Stdout(line) => this.handle_line(&line),
                    CommandEvent::Stderr(line) => {
                        eprintln!("engine: {}", String::from_utf8_lossy(&line));
                    }
                    CommandEvent::Terminated(_) | CommandEvent::Error(_) => {
                        this.handle_death();
                        break;
                    }
                    _ => {}
                }
            }
        });

        Ok(())
    }

    fn handle_line(&self, line: &[u8]) {
        let msg: Value = match serde_json::from_slice(line) {
            Ok(v) => v,
            Err(e) => {
                eprintln!(
                    "engine: undecodable line ({e}): {}",
                    String::from_utf8_lossy(line)
                );
                return;
            }
        };

        // Notifications carry no id and are not responses to anything.
        let Some(id) = msg.get("id").and_then(|v| v.as_u64()) else {
            return;
        };

        if let Some(tx) = self.pending.lock().unwrap().remove(&id) {
            let _ = tx.send(msg);
        }
    }

    fn handle_death(self: &Arc<Self>) {
        self.fail_all_pending("engine process exited");
        *self.child.lock().unwrap() = None;
        self.set_state("restarting");

        let this = Arc::clone(self);
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(RESTART_DELAY).await;
            if let Err(e) = this.spawn() {
                eprintln!("engine: restart failed: {e}");
                this.set_state("down");
            }
        });
    }

    /// Sends one request and awaits its response.
    pub async fn request(&self, method: String, params: Option<Value>) -> Result<Value, String> {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = oneshot::channel();

        let mut message = serde_json::json!({
            "jsonrpc": "2.0",
            "id": id,
            "method": method,
        });
        if let Some(p) = params {
            message["params"] = p;
        }

        let mut line = serde_json::to_vec(&message).map_err(|e| e.to_string())?;
        line.push(b'\n');

        // Register before writing, so a fast response cannot arrive first.
        self.pending.lock().unwrap().insert(id, tx);

        // Scoped so the guard is dropped before the await below.
        {
            let mut guard = self.child.lock().unwrap();
            let child = guard.as_mut().ok_or_else(|| {
                self.pending.lock().unwrap().remove(&id);
                "engine is not running".to_string()
            })?;
            if let Err(e) = child.write(&line) {
                self.pending.lock().unwrap().remove(&id);
                return Err(format!("cannot write to engine: {e}"));
            }
        }

        let response = match tokio::time::timeout(REQUEST_TIMEOUT, rx).await {
            Ok(Ok(v)) => v,
            Ok(Err(_)) => return Err("engine died before responding".into()),
            Err(_) => {
                self.pending.lock().unwrap().remove(&id);
                return Err("engine timed out".into());
            }
        };

        if let Some(err) = response.get("error") {
            let message = err
                .get("message")
                .and_then(|m| m.as_str())
                .unwrap_or("unknown engine error");
            return Err(message.to_string());
        }

        Ok(response.get("result").cloned().unwrap_or(Value::Null))
    }
}

#[tauri::command]
pub async fn engine_request(
    engine: tauri::State<'_, Arc<Engine>>,
    method: String,
    params: Option<Value>,
) -> Result<Value, String> {
    engine.request(method, params).await
}
```

- [ ] **Step 5: Wire it into the app**

Replace the contents of `src-tauri/src/lib.rs`:

```rust
mod engine;

use tauri::Manager;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            let eng = engine::Engine::new(app.handle().clone());
            eng.spawn()?;
            app.manage(eng);
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![engine::engine_request])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
```

- [ ] **Step 6: Build the sidecars, then verify the Rust compiles**

Run:
```bash
./scripts/build-sidecars.sh
cd src-tauri && cargo check && cd ..
```
Expected: `cargo check` finishes with no errors.

- [ ] **Step 7: Commit**

```bash
git add package.json package-lock.json vite.config.ts index.html src/ src-tauri/ .gitignore
git commit -m "feat(shell): scaffold Tauri app and spawn the engine sidecar"
```

---

### Task 6: Typed RPC client in TypeScript

**Files:**
- Create: `src/lib/engine.ts`
- Test: `src/lib/engine.test.ts`
- Modify: `package.json` (test script and dev dependencies)
- Create: `vitest.config.ts`

**Interfaces:**
- Consumes: the `engine_request` Tauri command and the `engine://status` event (Task 5)
- Produces:
  - `request<T>(method: string, params?: unknown): Promise<T>`
  - `interface Health { status: string; version: string; commit: string; pid: number }`
  - `health(): Promise<Health>`
  - `type EngineState = 'ready' | 'restarting' | 'down'`
  - `onStateChange(cb: (s: EngineState) => void): Promise<UnlistenFn>`

- [ ] **Step 1: Install the test tooling**

```bash
npm install -D vitest jsdom @testing-library/react @testing-library/dom
npm pkg set scripts.test="vitest run"
npm pkg set scripts.test:watch="vitest"
```

- [ ] **Step 2: Configure Vitest**

Create `vitest.config.ts`:

```ts
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{ts,tsx}'],
  },
});
```

- [ ] **Step 3: Write the failing test**

Create `src/lib/engine.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('@tauri-apps/api/core', () => ({ invoke: vi.fn() }));
vi.mock('@tauri-apps/api/event', () => ({ listen: vi.fn() }));

import { invoke } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';
import { request, health, onStateChange } from './engine';

const invokeMock = vi.mocked(invoke);
const listenMock = vi.mocked(listen);

beforeEach(() => {
  invokeMock.mockReset();
  listenMock.mockReset();
});

describe('request', () => {
  it('forwards the method and params to the engine_request command', async () => {
    invokeMock.mockResolvedValue({ ok: true });

    const result = await request<{ ok: boolean }>('query', { sql: 'SELECT 1' });

    expect(invokeMock).toHaveBeenCalledWith('engine_request', {
      method: 'query',
      params: { sql: 'SELECT 1' },
    });
    expect(result).toEqual({ ok: true });
  });

  it('sends null params when none are given', async () => {
    invokeMock.mockResolvedValue(null);

    await request('health');

    expect(invokeMock).toHaveBeenCalledWith('engine_request', {
      method: 'health',
      params: null,
    });
  });

  it('propagates engine errors to the caller', async () => {
    invokeMock.mockRejectedValue('engine is not running');

    await expect(request('health')).rejects.toBe('engine is not running');
  });
});

describe('health', () => {
  it('returns the engine health payload', async () => {
    invokeMock.mockResolvedValue({
      status: 'ok',
      version: '1.2.3',
      commit: 'abc123',
      pid: 4242,
    });

    const info = await health();

    expect(invokeMock).toHaveBeenCalledWith('engine_request', {
      method: 'health',
      params: null,
    });
    expect(info.status).toBe('ok');
    expect(info.version).toBe('1.2.3');
    expect(info.pid).toBe(4242);
  });
});

describe('onStateChange', () => {
  it('subscribes to the engine status event and unwraps the payload', async () => {
    let captured: ((event: { payload: string }) => void) | undefined;
    listenMock.mockImplementation((_name: string, handler: any) => {
      captured = handler;
      return Promise.resolve(() => {});
    });

    const seen: string[] = [];
    await onStateChange((s) => seen.push(s));

    expect(listenMock).toHaveBeenCalledWith('engine://status', expect.any(Function));
    captured?.({ payload: 'restarting' });
    expect(seen).toEqual(['restarting']);
  });
});
```

- [ ] **Step 4: Run it to verify it fails**

Run: `npm test`
Expected: FAIL — `Failed to resolve import "./engine"`.

- [ ] **Step 5: Write the client**

Create `src/lib/engine.ts`:

```ts
/**
 * Typed client for the Go engine sidecar.
 *
 * Every call funnels through the single `engine_request` Tauri command, which
 * handles JSON-RPC framing and ID correlation on the Rust side.
 */
import { invoke } from '@tauri-apps/api/core';
import { listen, type UnlistenFn } from '@tauri-apps/api/event';

export interface Health {
  status: string;
  version: string;
  commit: string;
  pid: number;
}

/** Lifecycle of the sidecar process, pushed by the shell's supervisor. */
export type EngineState = 'ready' | 'restarting' | 'down';

/** Calls one engine method. Rejects with the engine's error message. */
export async function request<T>(method: string, params?: unknown): Promise<T> {
  return invoke<T>('engine_request', { method, params: params ?? null });
}

export function health(): Promise<Health> {
  return request<Health>('health');
}

export function onStateChange(cb: (state: EngineState) => void): Promise<UnlistenFn> {
  return listen<EngineState>('engine://status', (event) => cb(event.payload));
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `npm test`
Expected: PASS — six tests green.

- [ ] **Step 7: Commit**

```bash
git add src/lib/ vitest.config.ts package.json package-lock.json
git commit -m "feat(ui): add typed RPC client for the engine sidecar"
```

---

### Task 7: Engine status UI and crash recovery

**Files:**
- Create: `src/components/EngineStatus.tsx`
- Test: `src/components/EngineStatus.test.tsx`
- Modify: `src/App.tsx`

**Interfaces:**
- Consumes: `health()`, `onStateChange()`, `Health`, `EngineState` (Task 6)
- Produces: `<EngineStatus />`, rendering `Connecting…`, the version and PID on success, `Engine restarting…` while supervised, or an error message

- [ ] **Step 1: Write the failing component test**

Create `src/components/EngineStatus.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';

vi.mock('../lib/engine', () => ({
  health: vi.fn(),
  onStateChange: vi.fn(),
}));

import { health, onStateChange } from '../lib/engine';
import { EngineStatus } from './EngineStatus';

const healthMock = vi.mocked(health);
const onStateChangeMock = vi.mocked(onStateChange);

beforeEach(() => {
  healthMock.mockReset();
  onStateChangeMock.mockReset();
  onStateChangeMock.mockResolvedValue(() => {});
});

it('shows a connecting state before the handshake completes', () => {
  healthMock.mockReturnValue(new Promise(() => {}));

  render(<EngineStatus />);

  expect(screen.getByText(/connecting/i)).toBeDefined();
});

it('shows the engine version and pid after a successful handshake', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/1\.2\.3/)).toBeDefined();
    expect(screen.getByText(/4242/)).toBeDefined();
  });
});

it('shows the error message when the handshake fails', async () => {
  healthMock.mockRejectedValue('engine is not running');

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/engine is not running/i)).toBeDefined();
  });
});

it('reports a restart and re-runs the handshake when the engine recovers', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  let emit: ((s: 'ready' | 'restarting' | 'down') => void) | undefined;
  onStateChangeMock.mockImplementation(async (cb) => {
    emit = cb;
    return () => {};
  });

  render(<EngineStatus />);
  await waitFor(() => expect(screen.getByText(/1\.2\.3/)).toBeDefined());

  await act(async () => {
    emit?.('restarting');
  });
  expect(screen.getByText(/restarting/i)).toBeDefined();

  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 5555,
  });
  await act(async () => {
    emit?.('ready');
  });

  await waitFor(() => expect(screen.getByText(/5555/)).toBeDefined());
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `npm test`
Expected: FAIL — `Failed to resolve import "./EngineStatus"`.

- [ ] **Step 3: Write the component**

Create `src/components/EngineStatus.tsx`:

```tsx
import { useCallback, useEffect, useState } from 'react';
import { health, onStateChange, type EngineState, type Health } from '../lib/engine';

type View =
  | { kind: 'connecting' }
  | { kind: 'ready'; info: Health }
  | { kind: 'restarting' }
  | { kind: 'error'; message: string };

export function EngineStatus() {
  const [view, setView] = useState<View>({ kind: 'connecting' });

  const handshake = useCallback(async () => {
    setView({ kind: 'connecting' });
    try {
      setView({ kind: 'ready', info: await health() });
    } catch (err) {
      setView({ kind: 'error', message: String(err) });
    }
  }, []);

  useEffect(() => {
    void handshake();
  }, [handshake]);

  useEffect(() => {
    let unlisten: (() => void) | undefined;
    let cancelled = false;

    void onStateChange((state: EngineState) => {
      if (state === 'restarting') {
        setView({ kind: 'restarting' });
      } else if (state === 'ready') {
        // A fresh process means a fresh PID, so re-handshake rather than
        // trusting the values from the process that just died.
        void handshake();
      } else {
        setView({ kind: 'error', message: 'Engine is down.' });
      }
    }).then((fn) => {
      if (cancelled) fn();
      else unlisten = fn;
    });

    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, [handshake]);

  switch (view.kind) {
    case 'connecting':
      return <p>Connecting to engine…</p>;
    case 'restarting':
      return <p>Engine restarting…</p>;
    case 'error':
      return <p role="alert">Engine error: {view.message}</p>;
    case 'ready':
      return (
        <p>
          Engine {view.info.version} ({view.info.commit}) — pid {view.info.pid}
        </p>
      );
  }
}
```

- [ ] **Step 4: Mount it in the app**

Replace the contents of `src/App.tsx`:

```tsx
import { EngineStatus } from './components/EngineStatus';

export default function App() {
  return (
    <main>
      <h1>tablepluslike</h1>
      <EngineStatus />
    </main>
  );
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `npm test`
Expected: PASS — ten tests green across both test files.

- [ ] **Step 6: Verify crash recovery in the real app**

Run: `npm run tauri dev`

Then, with the window open:
1. Confirm the window shows `Engine dev (none) — pid <N>`.
2. Kill the sidecar: `kill <N>` (or `taskkill /PID <N> /F` on Windows).
3. Confirm the UI shows `Engine restarting…`, then returns to a ready line with a **different** PID, within roughly a second.

Expected: recovery is automatic and the window never goes blank. If the PID is unchanged, the restart did not happen — check stderr for `engine: restart failed`.

- [ ] **Step 7: Commit**

```bash
git add src/components/ src/App.tsx
git commit -m "feat(ui): show engine health and recover from sidecar crashes"
```

---

### Task 8: CI matrix build for all three platforms

**Files:**
- Create: `.github/workflows/build.yml`

**Interfaces:**
- Consumes: `go test ./...`, `./scripts/build-sidecars.sh`, `npm test`, `npm run tauri build` (Tasks 1–7)
- Produces: uploaded installer artifacts per platform

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/build.yml`:

```yaml
name: build

on:
  push:
    branches: [master, main]
  pull_request:

jobs:
  engine:
    name: engine tests
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - name: Vet
        run: go vet ./...
      - name: Test with race detector
        run: go test ./... -race
      - name: Cross-compile all sidecar targets
        run: ./scripts/build-sidecars.sh
      - name: Verify sidecar naming contract
        run: ./scripts/build-sidecars_test.sh

  ui:
    name: ui tests
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: npm
      - run: npm ci
      - run: npm test

  bundle:
    name: bundle (${{ matrix.os }})
    needs: [engine, ui]
    strategy:
      fail-fast: false
      matrix:
        os: [macos-latest, windows-latest, ubuntu-22.04]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4

      # ubuntu-22.04 rather than latest: bundling against an older glibc keeps
      # the AppImage usable on distributions that ship an older runtime.
      - name: Install Linux system dependencies
        if: matrix.os == 'ubuntu-22.04'
        run: |
          sudo apt-get update
          sudo apt-get install -y \
            libwebkit2gtk-4.1-dev libappindicator3-dev librsvg2-dev patchelf

      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: npm
      - uses: dtolnay/rust-toolchain@stable

      - name: Cache cargo
        uses: actions/cache@v4
        with:
          path: |
            ~/.cargo/registry
            ~/.cargo/git
            src-tauri/target
          key: ${{ matrix.os }}-cargo-${{ hashFiles('src-tauri/Cargo.lock') }}

      - name: Build sidecars
        shell: bash
        run: ./scripts/build-sidecars.sh

      - run: npm ci
      - run: npm run tauri build

      - name: Upload installers
        uses: actions/upload-artifact@v4
        with:
          name: installers-${{ matrix.os }}
          path: |
            src-tauri/target/release/bundle/**/*.dmg
            src-tauri/target/release/bundle/**/*.AppImage
            src-tauri/target/release/bundle/**/*.deb
            src-tauri/target/release/bundle/**/*.msi
            src-tauri/target/release/bundle/**/*.exe
          if-no-files-found: error
```

- [ ] **Step 2: Verify the workflow parses**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/build.yml')); print('valid YAML')"`
Expected: `valid YAML`

- [ ] **Step 3: Verify locally that the bundle step would succeed**

Run:
```bash
./scripts/build-sidecars.sh && npm ci && npm run tauri build
```
Expected: a platform installer under `src-tauri/target/release/bundle/`. Launch it and confirm the engine line renders — this proves the sidecar was bundled and found by its triple-suffixed name, which `tauri dev` does not fully exercise.

- [ ] **Step 4: Commit and push**

```bash
git add .github/workflows/build.yml
git commit -m "ci: test engine and UI, bundle installers for three platforms"
git push -u origin master
```

- [ ] **Step 5: Confirm CI is green**

Run: `gh run watch`
Expected: all three jobs pass and `installers-*` artifacts are attached. **A red `bundle` job here is the whole point of Task 8** — it is far cheaper to find sidecar packaging problems now than after the engine has features.

---

## Definition of Done

- [ ] `go test ./... -race` passes.
- [ ] `npm test` passes.
- [ ] `./scripts/build-sidecars_test.sh` reports 6/6 targets.
- [ ] `npm run tauri build` produces an installer on the host platform.
- [ ] The installed app shows the engine version and PID.
- [ ] Killing the sidecar shows `Engine restarting…` and recovers with a new PID.
- [ ] CI is green on macOS, Windows, and Linux with installer artifacts attached.

## Deferred to Plan 2

Everything database-related: the `engine/` package, the `Driver`/`Conn` interfaces, the schema catalog, connection storage and keychain access, and the driver conformance suite. This plan deliberately ships a sidecar that can only answer `health`.

Code signing and notarization are also deferred. CI produces unsigned artifacts here; signing is set up once there is something worth distributing.
