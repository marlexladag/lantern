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
