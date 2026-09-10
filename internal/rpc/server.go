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
// on a clean close, which is how the shell signals shutdown. If the stream
// becomes unusable (e.g., due to an over-cap line), an error is returned and
// the stream cannot be recovered.
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
