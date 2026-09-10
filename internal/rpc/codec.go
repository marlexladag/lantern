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
