package driver

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestNormalizeNil(t *testing.T) {
	got := Normalize(nil)
	if got.Kind != ValueNull {
		t.Errorf("kind = %q, want %q", got.Kind, ValueNull)
	}
	if got.Text != "" {
		t.Errorf("text = %q, want empty", got.Text)
	}
}

func TestNormalizeStringsAndBytes(t *testing.T) {
	if got := Normalize("hello"); got.Kind != ValueText || got.Text != "hello" {
		t.Errorf("string -> %+v", got)
	}
	// Driver byte slices are text far more often than not; the driver decides
	// by column type, but an unhinted []byte is rendered as text when it is
	// valid UTF-8 and as bytes otherwise.
	if got := Normalize([]byte("hello")); got.Kind != ValueText || got.Text != "hello" {
		t.Errorf("utf8 bytes -> %+v", got)
	}
	if got := Normalize([]byte{0xff, 0xfe, 0x00}); got.Kind != ValueBytes {
		t.Errorf("binary bytes -> %+v, want kind bytes", got)
	}
}

// The whole reason Value carries text rather than a number.
func TestNormalizeLargeIntKeepsEveryDigit(t *testing.T) {
	const big = int64(9007199254740993) // 2^53 + 1, unrepresentable in float64
	got := Normalize(big)
	if got.Kind != ValueInt {
		t.Fatalf("kind = %q, want %q", got.Kind, ValueInt)
	}
	if got.Text != "9007199254740993" {
		t.Errorf("text = %q — precision was lost", got.Text)
	}
}

func TestNormalizeFloatsAreNotScientific(t *testing.T) {
	if got := Normalize(1234.5); got.Text != "1234.5" {
		t.Errorf("float -> %q", got.Text)
	}
	// A very large float must still be readable rather than 1.2345e+20.
	if got := Normalize(float64(123450000000000000000)); got.Text == "" ||
		got.Text[0] == '1' && len(got.Text) < 10 {
		t.Errorf("large float rendered as %q, want full digits", got.Text)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := Normalize(bad); got.Kind != ValueFloat || got.Text == "" {
			t.Errorf("%v -> %+v, want a float with readable text", bad, got)
		}
	}
}

func TestNormalizeBoolAndTime(t *testing.T) {
	if got := Normalize(true); got.Kind != ValueBool || got.Text != "true" {
		t.Errorf("bool -> %+v", got)
	}
	ts := time.Date(2026, 9, 11, 14, 30, 5, 0, time.UTC)
	got := Normalize(ts)
	if got.Kind != ValueTime {
		t.Fatalf("time kind = %q", got.Kind)
	}
	// RFC3339 so the UI can parse it, and so sorting a text column of
	// timestamps still orders correctly.
	if got.Text != "2026-09-11T14:30:05Z" {
		t.Errorf("time text = %q", got.Text)
	}
}

func TestNormalizeUnknownTypeFallsBackToText(t *testing.T) {
	type odd struct{ A int }
	got := Normalize(odd{A: 1})
	if got.Kind != ValueText || got.Text == "" {
		t.Errorf("unknown type -> %+v, want readable text", got)
	}
}

func TestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(Normalize(int64(7)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["kind"] != "int" || raw["text"] != "7" {
		t.Errorf("round trip = %s", b)
	}
}

// Adversarial: the value ISN'T null, its text just happens to spell the word
// "null". A driver that returns the four-character string "null" (a JSON
// column holding literal JSON null-as-text, a CHAR column, whatever) must
// still come back as ValueText, never collapse into the real NULL encoding
// (ValueNull with empty Text). The UI renders these two completely
// differently — italic "NULL" placeholder versus the literal text — so
// confusing them would misrepresent the data, not just its styling.
func TestNormalizeTextThatLooksLikeNull(t *testing.T) {
	got := Normalize("null")
	if got.Kind != ValueText || got.Text != "null" {
		t.Errorf(`string "null" -> %+v, want {kind text, text "null"}`, got)
	}
	if want := (Value{Kind: ValueNull}); got == want {
		t.Errorf("text \"null\" must not equal the real NULL encoding %+v", want)
	}
}

// Every numeric width a driver might realistically hand back, including
// uint64 values above math.MaxInt64 — a value FormatInt cannot represent at
// all, so a driver that mistakenly narrowed to int64 before calling Normalize
// would silently corrupt it (it wraps to a negative number).
func TestNormalizeOtherNumericWidths(t *testing.T) {
	if got := Normalize(int32(42)); got.Kind != ValueInt || got.Text != "42" {
		t.Errorf("int32 -> %+v", got)
	}
	if got := Normalize(7); got.Kind != ValueInt || got.Text != "7" {
		t.Errorf("int -> %+v", got)
	}
	const maxUint64 = uint64(18446744073709551615) // 2^64 - 1, beyond int64 range
	if got := Normalize(maxUint64); got.Kind != ValueInt || got.Text != "18446744073709551615" {
		t.Errorf("uint64 -> %+v", got)
	}
	if got := Normalize(float32(1.5)); got.Kind != ValueFloat || got.Text != "1.5" {
		t.Errorf("float32 -> %+v", got)
	}
}

// Adversarial: a value json.Marshal refuses outright (complex128 has no JSON
// representation), distinct from the struct case above which succeeds via
// json.Marshal. This is the one input that reaches the raw fmt.Sprintf
// fallback rather than the marshal-then-stringify path.
func TestNormalizeUnmarshalableFallsBackToSprintf(t *testing.T) {
	got := Normalize(complex(1, 2))
	if got.Kind != ValueText || got.Text != "(1+2i)" {
		t.Errorf("complex128 -> %+v, want readable text", got)
	}
}
