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
		// A Go string is bytes, not characters, and a driver will hand one
		// over for any TEXT column — modernc.org/sqlite returns every one of
		// them as a string. Nothing obliges a TEXT column to hold UTF-8:
		// SQLite stores whatever it was handed, so a latin-1 or mojibake
		// column arrives here as a string no decoder accepts. Screened on
		// exactly the same terms as []byte below; see characters.
		return characters(t)
	case []byte:
		// Most driver []byte is text (MySQL in particular hands back CHAR
		// and VARCHAR as []byte), so it is screened the same way a string
		// is rather than assumed binary. The conversion copies, so nothing
		// here keeps a reference to the driver's slice.
		return characters(string(t))
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
		// RFC3339Nano, not RFC3339: RFC3339 has no fractional-seconds field,
		// so a TIMESTAMP(6) column would silently lose its microseconds —
		// the exact class of precision loss this whole type exists to
		// prevent, and it would be invisible until someone compared the grid
		// against the database. Nano omits the fraction entirely when there
		// is none, so whole seconds still render clean.
		//
		// .UTC() DEPENDS ON A DRIVER INVARIANT: every driver must return a
		// time whose wall-clock reading equals what is stored in the column,
		// labelled UTC. SQL DATETIME columns carry no zone, so the label is
		// the driver's choice — MySQL, for one, returns whatever `loc` its
		// DSN was given. A driver that hands back a local-zoned time will
		// have every timestamp in the grid shifted off what the column
		// actually says, with nothing to hint at it. Any networked driver
		// added here must pin its connection to UTC. The zoned-time test in
		// value_test.go exists to make that requirement visible and failing
		// rather than leave it as a comment nobody reads.
		return Value{Kind: ValueTime, Text: t.UTC().Format(time.RFC3339Nano)}
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

// characters classifies a value a driver handed over as characters —
// whether it picked string or []byte to carry them.
//
// The UTF-8 screen is the load-bearing part, and it belongs HERE rather than
// on one of the two call sites, because Text crosses the wire as JSON and
// encoding/json has no way to carry a byte that is not valid UTF-8: it
// substitutes U+FFFD silently. A value that reached the UI that way was
// indistinguishable from a row genuinely holding a replacement character,
// and — when it was a sort key — echoing the substitute back as a cursor
// re-matched the cursor's own row, because U+FFFD sorts below the raw bytes
// it replaced under a memcmp collation. That is fix wave D-1: one row
// repeated forever and the row before it unreachable.
//
// So anything that is not valid UTF-8 is opaque bytes, summarised by length
// and refused inline rendering by the grid, rather than text that lies about
// itself. It is also what makes ValueBytes reachable at all for a real row
// (D-2): a BLOB column's bytes are almost never valid UTF-8.
func characters(s string) Value {
	if utf8.ValidString(s) {
		return Value{Kind: ValueText, Text: s}
	}
	return Value{Kind: ValueBytes, Text: fmt.Sprintf("%d bytes", len(s))}
}
