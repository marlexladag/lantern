package driver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRejectsAnEmptyTable(t *testing.T) {
	err := BrowseRequest{Limit: 100}.Validate()
	if err == nil {
		t.Fatal("an empty table name was accepted")
	}
	if !strings.Contains(err.Error(), "table") {
		t.Errorf("error does not name the field: %v", err)
	}
}

func TestValidateRejectsANonPositiveLimit(t *testing.T) {
	for _, n := range []int{0, -1} {
		if err := (BrowseRequest{Table: "users", Limit: n}).Validate(); err == nil {
			t.Errorf("limit %d was accepted", n)
		}
	}
}

// An unbounded limit is how a UI bug becomes an out-of-memory crash.
func TestValidateRejectsALimitAboveTheCap(t *testing.T) {
	err := BrowseRequest{Table: "users", Limit: MaxBrowseLimit + 1}.Validate()
	if err == nil {
		t.Fatalf("limit %d was accepted, cap is %d", MaxBrowseLimit+1, MaxBrowseLimit)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error does not name the field: %v", err)
	}
}

// Adversarial: the boundary itself. An off-by-one here (> vs >=) silently
// caps every page at MaxBrowseLimit-1 rows, one short, without ever failing
// a test that only checks values well inside or well outside the cap.
func TestValidateAcceptsTheCapItself(t *testing.T) {
	if err := (BrowseRequest{Table: "users", Limit: MaxBrowseLimit}).Validate(); err != nil {
		t.Errorf("the cap itself was rejected: %v", err)
	}
}

// Adversarial: Sort empty but After non-empty. This looks incoherent — After
// without a Sort to interpret it against — but it is in fact the normal
// shape of a second page when the caller never specified a sort: the driver
// applies its own stable default (its primary key) whenever Sort is empty,
// and the caller is never expected to know what that default is, only to
// echo back the Keyset it was handed. Rejecting this combination would force
// every call site to rediscover and resend the driver's default sort just to
// page past the first screen, which defeats the reason a default exists.
func TestValidateAcceptsEmptySortWithNonEmptyAfter(t *testing.T) {
	req := BrowseRequest{
		Table: "users",
		Limit: 50,
		After: []Value{{Kind: ValueInt, Text: "1"}},
	}
	if err := req.Validate(); err != nil {
		t.Errorf("empty Sort with non-empty After was rejected: %v", err)
	}
}

func TestBrowsePageJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(BrowsePage{
		Columns:   []ColumnMeta{{Name: "id", DataType: "INTEGER"}},
		Rows:      [][]Value{{{Kind: ValueInt, Text: "1"}}},
		Keyset:    []Value{{Kind: ValueInt, Text: "1"}},
		Exhausted: true,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"columns", "rows", "keyset", "exhausted", "offset"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing %q in %s", key, b)
		}
	}
}

// A page paginated by offset must not emit a keyset the UI would echo back.
func TestOffsetPagedPageOmitsKeyset(t *testing.T) {
	b, err := json.Marshal(BrowsePage{Columns: []ColumnMeta{}, Rows: [][]Value{}, Offset: 500})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["keyset"]; ok {
		t.Errorf("keyset present on an offset-paged page: %s", b)
	}
}

// Adversarial: a BrowsePage literal that never assigns Rows leaves it at its
// zero value, nil — exactly what a driver produces for an empty table or an
// After key past the last row. encoding/json's default rendering of a nil
// slice is the JSON literal null, not []; the TypeScript side declares Rows
// as an array, and null landing where an array is expected has blanked the
// whole app before. Assert on the actual bytes, not just that marshal
// succeeded — a struct-shape check would not have caught this.
func TestBrowsePageWithZeroRowsSerializesAsEmptyArrayNotNull(t *testing.T) {
	b, err := json.Marshal(BrowsePage{Columns: []ColumnMeta{{Name: "id", DataType: "INTEGER"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"rows":[]`) {
		t.Errorf("zero-row page did not emit rows:[] (got %s)", b)
	}
	if strings.Contains(string(b), `"rows":null`) {
		t.Errorf("zero-row page emitted rows:null: %s", b)
	}
}

func TestSortKeyJSON(t *testing.T) {
	b, _ := json.Marshal(SortKey{Column: "created_at", Desc: true})
	if string(b) != `{"column":"created_at","desc":true}` {
		t.Errorf("SortKey = %s", b)
	}
}
