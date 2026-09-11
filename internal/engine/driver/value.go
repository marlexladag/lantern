package driver

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

// ValueKind is what the UI branches on: numbers right-align, NULL renders
// italic and dim, bytes refuse to render inline.
type ValueKind string

const (
	ValueNull  ValueKind = "null"
	ValueText  ValueKind = "text"
	ValueInt   ValueKind = "int"
	ValueFloat ValueKind = "float"
	ValueBool  ValueKind = "bool"
	ValueBytes ValueKind = "bytes"
	ValueTime  ValueKind = "time"
)

// Value is one cell, in engine-neutral terms.
//
// Text is always the display form, formatted once by the engine, rather than
// a typed payload, because JSON numbers are float64: marshalling an int64 or
// a DECIMAL through a number silently destroys precision above 2^53, and a
// money column or a primary key rendered wrong is not a bug anyone forgives.
// Kind is what lets the UI tell a real NULL apart from the empty string, and
// the four-character text "null" apart from both — the wire carries the
// tag, never lets the grid guess from the text alone.
type Value struct {
	Kind ValueKind `json:"kind"`
	Text string    `json:"text"`
}

// Normalize maps whatever a driver produced onto a Value. Every driver in
// this repo funnels its native row values through here exactly once, so a
// new Go type a future driver returns needs a case added here, not one in
// every driver.
func Normalize(v any) Value {
	switch t := v.(type) {
	case nil:
		return Value{Kind: ValueNull}
	case string:
		return Value{Kind: ValueText, Text: t}
	case []byte:
		// Most driver []byte is text (MySQL in particular hands back CHAR
		// and VARCHAR as []byte). Treat valid UTF-8 as text and anything
		// else — a BLOB, a corrupt column — as opaque bytes the grid will
		// not try to render inline.
		if utf8.Valid(t) {
			return Value{Kind: ValueText, Text: string(t)}
		}
		return Value{Kind: ValueBytes, Text: fmt.Sprintf("%d bytes", len(t))}
	case bool:
		return Value{Kind: ValueBool, Text: strconv.FormatBool(t)}
	case int64:
		return Value{Kind: ValueInt, Text: strconv.FormatInt(t, 10)}
	case int32:
		return Value{Kind: ValueInt, Text: strconv.FormatInt(int64(t), 10)}
	case int:
		return Value{Kind: ValueInt, Text: strconv.Itoa(t)}
	case uint64:
		// FormatUint, not a cast to int64: a uint64 above math.MaxInt64
		// would wrap to negative through int64.
		return Value{Kind: ValueInt, Text: strconv.FormatUint(t, 10)}
	case float64:
		// 'f' with -1 precision, never 'e': a column of scientific notation
		// is unreadable, and the grid exists to be read.
		return Value{Kind: ValueFloat, Text: strconv.FormatFloat(t, 'f', -1, 64)}
	case float32:
		return Value{Kind: ValueFloat, Text: strconv.FormatFloat(float64(t), 'f', -1, 32)}
	case time.Time:
		// RFC3339 so the UI can parse it and so a plain text sort of the
		// column still orders correctly.
		return Value{Kind: ValueTime, Text: t.UTC().Format(time.RFC3339)}
	}

	// An unrecognised type is still worth showing rather than dropping.
	// json.Marshal gives a readable rendering for most shapes (structs,
	// maps, slices); %v covers the handful it refuses outright (channels,
	// funcs, complex numbers).
	if b, err := json.Marshal(v); err == nil {
		return Value{Kind: ValueText, Text: string(b)}
	}
	return Value{Kind: ValueText, Text: fmt.Sprintf("%v", v)}
}
