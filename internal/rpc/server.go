package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
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

// Handler returns the handler registered for a method. It exists so callers
// can exercise a method directly without going through the stdio loop.
func (s *Server) Handler(method string) (Handler, bool) { return s.handler(method) }

// Serve reads requests until the stream closes, dispatching each in its own
// goroutine so a slow method cannot stall the ones behind it. It returns nil
// on a clean close, which is how the shell signals shutdown. If the stream
// becomes unusable (e.g., due to an over-cap line), an error is returned and
// the stream cannot be recovered.
//
// Context cancellation is only observed between requests: a pending read from
// the stream will not wake up. To stop Serve, close the reader. A request that
// is decoded after cancellation receives an internal error response before
// Serve returns. This distinguishes cancellation from a clean close.
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
			if err := enc.Encode(&Response{JSONRPC: Version, Error: Errorf(CodeParse, "malformed JSON")}); err != nil {
				return err
			}
			continue
		case err != nil:
			return err
		}

		if ctx.Err() != nil {
			// Reply to this request before returning.
			if !req.IsNotification() {
				if err := enc.Encode(&Response{
					JSONRPC: Version,
					ID:      req.ID,
					Error:   Errorf(CodeInternal, "server shutting down"),
				}); err != nil {
					return err
				}
			}
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
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic in handler %q: %v\n%s\n", req.Method, r, debug.Stack())
			if !req.IsNotification() {
				resp := &Response{
					JSONRPC: Version,
					ID:      req.ID,
					Error:   Errorf(CodeInternal, "internal error"),
				}
				// Encode errors are deliberately dropped here and in
				// reply below, unlike in Serve, which returns them. The
				// asymmetry is intentional: Serve owns the read loop and
				// can turn a dead stdout into a process-level failure,
				// but dispatch runs on its own goroutine with no caller
				// to return to and no way to abort the loop. The only
				// realistic cause is stdout being closed - i.e. the shell
				// is already gone - and in that case Serve's next Decode
				// hits EOF and shuts the engine down anyway. Logging to
				// stderr would also be futile in the crashed-shell case
				// and noisy in the shutdown case, so this stays silent.
				_ = enc.Encode(resp)
			}
		}
	}()

	// See the encode-error note in the recover block above: a failed
	// write here has nowhere to go and no recovery worth attempting.
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
