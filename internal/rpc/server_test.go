package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
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

	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader("{not json\n"+`{"jsonrpc":"2.0","id":2,"method":"health"}`+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2", len(lines))
	}

	// Verify first response is parse error with id:null (JSON-RPC 2.0 requirement).
	if !strings.Contains(lines[0], `"id":null`) {
		t.Errorf("parse error response must contain \"id\":null, got: %s", lines[0])
	}
	var r Response
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil {
		t.Fatalf("undecodable first response: %v", err)
	}
	if r.Error == nil || r.Error.Code != CodeParse {
		t.Errorf("first response = %+v, want parse error", r)
	}

	// Verify second response is successful. Use a fresh variable since
	// json.Unmarshal doesn't clear pointer fields when they're absent from JSON.
	var r2 Response
	if err := json.Unmarshal([]byte(lines[1]), &r2); err != nil {
		t.Fatalf("undecodable second response: %v, line: %q", err, lines[1])
	}
	if r2.Error != nil {
		t.Errorf("second response should have succeeded, got error: %+v", r2.Error)
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
		if r.ID == nil {
			t.Fatalf("response has nil ID, expected a request ID")
		}
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

// A handler that panics must not crash the server; a subsequent request succeeds.
func TestServePanicingHandler(t *testing.T) {
	s := NewServer()
	s.Register("panic", func(context.Context, json.RawMessage) (any, error) {
		panic("oops")
	})
	s.Register("health", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"status": "ok"}, nil
	})

	got := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"panic"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"health"}`+"\n")
	if len(got) != 2 {
		t.Fatalf("got %d responses, want 2", len(got))
	}
	byID := map[string]Response{}
	for _, r := range got {
		if r.ID == nil {
			t.Fatalf("response has nil ID, expected a request ID")
		}
		byID[string(*r.ID)] = r
	}
	if byID["1"].Error == nil || byID["1"].Error.Code != CodeInternal {
		t.Errorf("panic response = %+v, want CodeInternal error", byID["1"])
	}
	if byID["2"].Error != nil {
		t.Errorf("health response should have succeeded: %+v", byID["2"].Error)
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

	// Issue writes from a goroutine so the test's deadline branch is reachable.
	go func() {
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"slow"}` + "\n"))
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"fast"}` + "\n"))
	}()

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

// A stream error (other than parse) must be returned and stops reading.
func TestServeReturnsStreamError(t *testing.T) {
	s := NewServer()
	s.Register("echo", func(context.Context, json.RawMessage) (any, error) {
		return "ok", nil
	})

	reader := iotest.ErrReader(errors.New("boom"))
	if err := s.Serve(context.Background(), reader, io.Discard); err == nil {
		t.Error("want error on stream failure, got nil")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want to contain 'boom'", err)
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

// ctxCancelingReader cancels a context the instant its first byte is read,
// then serves the wrapped reader's content unchanged. This is what it takes
// to land inside the one window Serve's documented contract admits
// cancellation can be observed: after a request has already been decoded,
// but before the next read begins. Serve's own doc comment is explicit that
// a pending read will not wake up on cancellation, so there is no way to
// reach this window except by canceling from inside a Read call already in
// flight for the request that is about to be dispatched.
type ctxCancelingReader struct {
	cancel context.CancelFunc
	r      io.Reader
	once   sync.Once
}

func (r *ctxCancelingReader) Read(p []byte) (int, error) {
	r.once.Do(r.cancel)
	return r.r.Read(p)
}

// This is the most important gap this package had: the behavior was
// mandated by an explicit ruling (reply with an error instead of silently
// dropping an already-decoded request) and nothing exercised it. A request
// decoded right as the context is canceled must still get a reply, rather
// than being dropped on the floor while the caller waits forever for a
// response that will never come.
func TestServeRepliesWithErrorWhenContextCanceledAfterDecode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &ctxCancelingReader{
		cancel: cancel,
		r:      strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"health"}` + "\n"),
	}

	s := NewServer()
	var called bool
	s.Register("health", func(context.Context, json.RawMessage) (any, error) {
		called = true
		return map[string]string{"status": "ok"}, nil
	})

	var out strings.Builder
	if err := s.Serve(ctx, reader, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if called {
		t.Error("handler ran after context cancellation; Serve should reply and stop before dispatch")
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d response lines, want 1: %q", len(lines), out.String())
	}
	var resp Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("undecodable response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInternal {
		t.Errorf("response = %+v, want a CodeInternal error", resp)
	}
	if resp.ID == nil || string(*resp.ID) != "1" {
		t.Errorf("id = %v, want the decoded request's id (1)", resp.ID)
	}
}

// Same cancellation window, but for a notification: it must still get no
// response (a notification never does), and - unlike the parse-error path,
// which cannot know a notification's ID either way - the handler must not
// run, because that is the entire point of checking cancellation before
// dispatch at all.
func TestServeSendsNoResponseWhenContextCanceledAfterDecodingNotification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &ctxCancelingReader{
		cancel: cancel,
		r:      strings.NewReader(`{"jsonrpc":"2.0","method":"cancel"}` + "\n"),
	}

	s := NewServer()
	var called bool
	s.Register("cancel", func(context.Context, json.RawMessage) (any, error) {
		called = true
		return nil, nil
	})

	var out strings.Builder
	if err := s.Serve(ctx, reader, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if called {
		t.Error("handler ran after context cancellation")
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("notification produced output after cancellation: %q", out.String())
	}
}

// Regression test: the cancellation-reply write can itself fail (the shell
// vanished at the exact moment the server tried to tell it shutdown was in
// progress). That failure must propagate out of Serve like every other
// stream-write failure, not be swallowed on the way out during shutdown.
func TestServeReturnsErrorWhenCancellationReplyFailsToWrite(t *testing.T) {
	wantErr := errors.New("stdout is gone")
	ctx, cancel := context.WithCancel(context.Background())
	reader := &ctxCancelingReader{
		cancel: cancel,
		r:      strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"health"}` + "\n"),
	}

	s := NewServer()
	err := s.Serve(ctx, reader, errWriter{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// Regression test: if the shell is gone, writing the parse-error response
// fails, and that failure must propagate out of Serve (as it does for every
// other stream error) rather than being swallowed and read as "clean exit".
func TestServeReturnsErrorWhenParseErrorResponseFailsToWrite(t *testing.T) {
	wantErr := errors.New("stdout is gone")
	s := NewServer()

	err := s.Serve(context.Background(), strings.NewReader("{not json\n"), errWriter{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// Regression test: a handler's result that json.Marshal rejects (a chan, in
// this case a realistic stand-in for a result type that grew an
// unmarshalable field) must produce a normal CodeInternal error response,
// not a panic and not a hang.
func TestServeReportsInternalErrorWhenResultCannotBeMarshaled(t *testing.T) {
	s := NewServer()
	s.Register("bad", func(context.Context, json.RawMessage) (any, error) {
		return make(chan int), nil
	})

	got := serve(t, s, `{"jsonrpc":"2.0","id":1,"method":"bad"}`+"\n")
	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1", len(got))
	}
	if got[0].Error == nil || got[0].Error.Code != CodeInternal {
		t.Errorf("response = %+v, want a CodeInternal error", got[0])
	}
	if !strings.Contains(got[0].Error.Message, "cannot encode result") {
		t.Errorf("message = %q, want it to mention the encoding failure", got[0].Error.Message)
	}
}

// (*Error).Error() has no caller anywhere in this codebase; the only way it
// runs is when something formats an *Error as a string. fmt.Errorf("%w", …)
// does exactly that while building the wrapping error's own message, which
// is also the realistic way a real handler adds context without losing the
// JSON-RPC code the caller needs. dispatch uses errors.As rather than a
// plain type assertion specifically so this survives the wrap; a type
// assertion here would silently fall back to CodeInternal instead.
func TestServeUnwrapsWrappedErrorForCode(t *testing.T) {
	s := NewServer()
	s.Register("wrapped", func(context.Context, json.RawMessage) (any, error) {
		return nil, fmt.Errorf("querying users: %w", Errorf(CodeInvalidParams, "id must be positive"))
	})

	got := serve(t, s, `{"jsonrpc":"2.0","id":1,"method":"wrapped"}`+"\n")
	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1", len(got))
	}
	if got[0].Error == nil {
		t.Fatal("want an error response")
	}
	if got[0].Error.Code != CodeInvalidParams {
		t.Errorf("code = %d, want %d (the wrapped *Error's code survived, not CodeInternal)", got[0].Error.Code, CodeInvalidParams)
	}
	if got[0].Error.Message != "id must be positive" {
		t.Errorf("message = %q, want the wrapped *Error's own message", got[0].Error.Message)
	}
}
