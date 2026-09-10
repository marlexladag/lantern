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

// Regression test: an oversized line should corrupt the stream permanently.
// The decoder must distinguish between ErrParse (recoverable) and
// ErrStreamCorrupted (stream unusable).
func TestDecoderDetectsOversizedLine(t *testing.T) {
	// Create a line larger than the small buffer cap we'll set.
	// The scanner will hit bufio.ErrTooLong and the stream becomes unusable.
	oversized := strings.Repeat("x", 200*1024) + "\n"
	in := strings.NewReader(oversized)

	// Create decoder with small buffer cap to trigger the condition
	d := NewDecoder(in)
	// Override scanner buffer to make oversized line reachable (much smaller than default)
	d.sc.Buffer(make([]byte, 0, 4*1024), 4*1024)

	// Reading oversized line should return ErrStreamCorrupted, not ErrParse
	// This is the key distinction: ErrParse means "this line was bad JSON, try again",
	// but ErrStreamCorrupted means "stream is broken, stop reading".
	_, err := d.Decode()
	if !errors.Is(err, ErrStreamCorrupted) {
		t.Fatalf("oversized line: err = %v, want ErrStreamCorrupted", err)
	}
}

// Regression test: a nil Response ID must serialize with explicit "id":null.
// JSON-RPC 2.0 requires the id field to always be present in responses.
func TestResponseWithNilIDEncodesIDField(t *testing.T) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)

	// Encode a response with nil ID (for a parse error scenario)
	err := e.Encode(&Response{JSONRPC: "2.0", ID: nil, Result: json.RawMessage(`null`)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	out := buf.String()

	// Parse the output to verify it's valid JSON
	var back Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	// Verify the encoded JSON contains the "id" field explicitly
	if !strings.Contains(out, `"id":null`) {
		t.Fatalf("output %q should contain explicit \"id\":null for nil ID", out)
	}
}
