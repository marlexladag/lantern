# Browse Table Rows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Click a table in the sidebar and see its rows in a grid that stays smooth at a million rows.

**Architecture:** A `Browser` optional interface lets each driver render its own dialect for a semantic browse request, so the caller never assembles SQL. A cursor registry in `internal/api` holds open result sets and serves windows of rows on demand. The UI renders them in a canvas-based grid that requests only the cells it can see.

**Tech Stack:** Go 1.24 (stdlib + `modernc.org/sqlite`), the existing JSON-RPC sidecar, React 18 + TypeScript, Glide Data Grid.

**Spec:** `docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md` — sections 4 (driver interface), 7 (query execution and streaming), 11 (error model), 12 (design language).

**Design:** `design/Main.dc.html` is the target — 26px rows, the column header treatment, the status bar. `design/Foundations.dc.html` is normative for every value.

## Global Constraints

- Go 1.24; `go.mod` says `1.24.0` and CI pins `'1.24'`. Do not change it.
- **`CGO_ENABLED=0` must keep working for all six targets.** `./scripts/build-sidecars_test.sh` enforces it.
- **No new Go dependencies.** The only new npm dependency permitted is the grid itself.
- **stdout carries the JSON-RPC protocol and nothing else.** Diagnostics go to stderr.
- `internal/engine/...` imports nothing from `internal/api`, `internal/rpc`, `cmd/`, or any transport.
- **The caller never writes SQL for browsing.** Requests are semantic; each driver renders its own dialect. This is what makes MySQL an added implementation rather than a rewrite.
- **Cancel must always be reachable** while a query runs (spec §7).
- 100% coverage gates on both sides. `LANTERN_CONFIG_DIR` for any hand-testing.
- No `Co-Authored-By` trailer on commits.
- Every task ends with a commit.

### Added after Plan 2's whole-branch review (binding on every task below)

These come from defects that 100% coverage on both sides did not catch,
because coverage measures which lines run, not which values reach them.

- **Every task adds at least one adversarial fixture** — an input the
  existing suite never produces. Two shipped bugs came from fixtures that
  were uniformly well-shaped: a table list that always had a table in it,
  and an error that arrived as a string when the real code can only throw an
  object. For this plan the obvious ones are: a table with **zero rows**, a
  page whose keyset continuation returns **nothing**, a column whose value is
  **NULL**, and a rejection that is an object rather than a string.
- **Never render a caught error with `String(err)` or
  `dbErr?.message ?? String(err)`.** `request()` throws only objects, so that
  fallback is provably `[object Object]` every time. Use `describeError(err)`
  from `src/lib/errors.ts` and render it through `<ErrorText>` — both exist
  as of the post-review fix wave. Task 5's step 1 mentions `asDbError`; it
  means `describeError` now.
- **An empty result renders an explicit empty state, never nothing.** A table
  that draws blank is indistinguishable from one that failed to load. This
  applies to the grid the same way it now applies to the sidebar.
- **The error taxonomy has eleven Kinds**, including `read_only`. Any Kind
  the grid branches on must be handled by name, and the Go/TS set-equality
  test must stay green.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/engine/driver/value.go` | `Value` — the engine-neutral cell type every driver normalizes into |
| `internal/engine/driver/browse.go` | `Browser`, `BrowseRequest`, `SortKey`, `Page` |
| `internal/engine/driver/sqlite/browse.go` | SQLite's dialect rendering |
| `internal/api/cursors.go` | The open-cursor registry and windowed fetch |
| `internal/api/browse.go` | `browse.open`, `browse.fetch`, `browse.close` |
| `src/lib/browse.ts` | Typed client |
| `src/components/ResultGrid.tsx` | The canvas grid |
| `src/components/Sidebar.tsx` | Selecting a table raises a browse (modify) |

---

### Task 1: The engine-neutral cell value

**Files:**
- Create: `internal/engine/driver/value.go`
- Test: `internal/engine/driver/value_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type ValueKind string` with `ValueNull`, `ValueText`, `ValueInt`, `ValueFloat`, `ValueBool`, `ValueBytes`, `ValueTime` (values `"null"`, `"text"`, `"int"`, `"float"`, `"bool"`, `"bytes"`, `"time"`)
  - `type Value struct { Kind ValueKind; Text string }` — JSON `kind`, `text`
  - `func Normalize(v any) Value`

**Why a struct and not `any`:** the wire has to carry the difference between a NULL and the string `"NULL"`, between the integer `1` and the text `"1"`, and between a 40MB BLOB and something printable. `any` marshals all of those ambiguously, and the grid then has to guess. `Text` is always the display form; `Kind` is what the UI branches on to right-align numbers, italicise NULL, and refuse to render a blob inline.

**Why `Text` rather than a typed payload:** JSON numbers are float64, which silently destroys int64 precision and `DECIMAL`. A money column rendered wrong is not a bug people forgive. The engine formats once, exactly, and the UI displays what it is given.

- [ ] **Step 1: Write the failing test**

Create `internal/engine/driver/value_test.go`:

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/driver/ -run TestNormalize -v`
Expected: FAIL — `undefined: Normalize`.

- [ ] **Step 3: Write the implementation**

Create `internal/engine/driver/value.go`:

```go
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
// Text is always the display form, formatted once by the engine. It is a
// string rather than a typed payload because JSON numbers are float64:
// marshalling an int64 or a DECIMAL through a number silently destroys
// precision, and a money column rendered wrong is not a bug anyone forgives.
type Value struct {
	Kind ValueKind `json:"kind"`
	Text string    `json:"text"`
}

// Normalize maps whatever a driver produced onto a Value.
func Normalize(v any) Value {
	switch t := v.(type) {
	case nil:
		return Value{Kind: ValueNull}
	case string:
		return Value{Kind: ValueText, Text: t}
	case []byte:
		// Most driver []byte is text. Treat valid UTF-8 as text and anything
		// else as opaque bytes the grid will not try to render inline.
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
		return Value{Kind: ValueInt, Text: strconv.FormatUint(t, 10)}
	case float64:
		// 'f' with -1 precision, never 'e': a column of scientific notation is
		// unreadable, and the grid is for reading.
		return Value{Kind: ValueFloat, Text: strconv.FormatFloat(t, 'f', -1, 64)}
	case float32:
		return Value{Kind: ValueFloat, Text: strconv.FormatFloat(float64(t), 'f', -1, 32)}
	case time.Time:
		// RFC3339 so the UI can parse it and so text sorting still orders.
		return Value{Kind: ValueTime, Text: t.UTC().Format(time.RFC3339)}
	}

	// An unrecognised type is still worth showing rather than dropping. JSON
	// gives a readable rendering for most shapes; %v covers the rest.
	if b, err := json.Marshal(v); err == nil {
		return Value{Kind: ValueText, Text: string(b)}
	}
	return Value{Kind: ValueText, Text: fmt.Sprintf("%v", v)}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/engine/driver/ -race -v`
Expected: PASS — the new tests plus the existing registry tests.

- [ ] **Step 5: Check the coverage gate**

Run: `./scripts/go-coverage.sh --check`
Expected: 100.0%. Add a case for anything flagged rather than lowering the threshold.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/driver/value.go internal/engine/driver/value_test.go
git commit -m "feat(engine): add the engine-neutral cell value"
```

---

### Task 2: The browse request and page

**Files:**
- Create: `internal/engine/driver/browse.go`
- Test: `internal/engine/driver/browse_test.go`

**Interfaces:**
- Consumes: `Value`, `ColumnMeta` (Task 1 and existing)
- Produces:
  - `type SortKey struct { Column string; Desc bool }` — JSON `column`, `desc`
  - `type BrowseRequest struct { Database, Table string; Sort []SortKey; After []Value; Offset, Limit int }` — JSON `database`, `table`, `sort,omitempty`, `after,omitempty`, `offset,omitempty`, `limit`
  - `type BrowsePage struct { Columns []ColumnMeta; Rows [][]Value; Keyset []Value; Exhausted bool; Offset int }` — JSON `columns`, `rows`, `keyset,omitempty`, `exhausted`, `offset`
  - `type Browser interface { Browse(ctx context.Context, req BrowseRequest) (*BrowsePage, error) }`
  - `func (r BrowseRequest) Validate() error` — rejects an empty table, a non-positive limit, and a limit above `MaxBrowseLimit`
  - `const MaxBrowseLimit = 1000`

**The design decision that shapes everything else: browsing is STATELESS.**

Keyset pagination needs no server-side cursor. Each page is a self-contained request carrying the previous page's last key, so there is nothing to hold open, nothing to time out, and nothing to leak when a UI tab closes. `BrowsePage.Keyset` is whatever the driver needs to fetch the next page; the UI echoes it back in `After` without understanding it.

That is deliberately narrower than spec §7's windowed cursor, which is for *arbitrary SQL* — a result set you cannot re-derive and must therefore hold. Table browsing can always be re-derived, so it should not pay a cursor's costs. The cursor registry arrives with the query editor.

**Why `Offset` exists alongside `After`:** keyset pagination requires a stable unique sort. A table with no primary key, or a sort on a non-unique column, cannot use it. The driver decides: it fills `Keyset` when it paginated by key and `Offset` when it fell back, and the UI sends back whichever it received.

- [ ] **Step 1: Write the failing test**

Create `internal/engine/driver/browse_test.go`:

```go
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

func TestValidateAcceptsTheCapItself(t *testing.T) {
	if err := (BrowseRequest{Table: "users", Limit: MaxBrowseLimit}).Validate(); err != nil {
		t.Errorf("the cap itself was rejected: %v", err)
	}
}

func TestJSONFieldNames(t *testing.T) {
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

func TestSortKeyJSON(t *testing.T) {
	b, _ := json.Marshal(SortKey{Column: "created_at", Desc: true})
	if string(b) != `{"column":"created_at","desc":true}` {
		t.Errorf("SortKey = %s", b)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/driver/ -run 'TestValidate|TestOffsetPaged|TestSortKey' -v`
Expected: FAIL — `undefined: BrowseRequest`.

- [ ] **Step 3: Write the implementation**

Create `internal/engine/driver/browse.go`:

```go
package driver

import (
	"context"

	"github.com/marlexladag/lantern/internal/engine/dberr"
)

// MaxBrowseLimit caps a single page. An unbounded limit is how a UI bug
// becomes an out-of-memory crash on a table with a hundred million rows.
const MaxBrowseLimit = 1000

// SortKey is one ORDER BY term.
type SortKey struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// BrowseRequest asks for one page of a table, semantically. The caller never
// writes SQL: each driver renders this in its own dialect, which is what makes
// a second engine an added implementation rather than a rewrite.
type BrowseRequest struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	// Sort is empty for the driver's stable default, which should be the
	// primary key so that pagination is deterministic.
	Sort []SortKey `json:"sort,omitempty"`
	// After is the previous page's Keyset, echoed back untouched. Nil for the
	// first page.
	After []Value `json:"after,omitempty"`
	// Offset is used only when the driver told the caller it could not
	// paginate by key.
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit"`
}

// Validate rejects a request no driver should be asked to run.
func (r BrowseRequest) Validate() error {
	if r.Table == "" {
		return dberr.New(dberr.KindInvalid, "browse: table is required")
	}
	if r.Limit <= 0 {
		return dberr.New(dberr.KindInvalid, "browse: limit must be positive")
	}
	if r.Limit > MaxBrowseLimit {
		return dberr.New(dberr.KindInvalid, "browse: limit exceeds the maximum page size")
	}
	return nil
}

// BrowsePage is one page of rows.
type BrowsePage struct {
	Columns []ColumnMeta `json:"columns"`
	Rows    [][]Value    `json:"rows"`
	// Keyset carries whatever the driver needs to fetch the next page, taken
	// from the last row. The caller echoes it back in After without
	// understanding it. Absent when the driver paginated by offset, or when
	// the result is exhausted.
	Keyset []Value `json:"keyset,omitempty"`
	// Exhausted is true when fewer rows than Limit came back, so the caller
	// can stop asking.
	Exhausted bool `json:"exhausted"`
	// Offset is the offset of the row after this page, for drivers that could
	// not paginate by key.
	Offset int `json:"offset"`
}

// Browser is an OPTIONAL interface. A driver implements it when it can page a
// table; the caller discovers it by type assertion, exactly as spec section 4
// requires, so a schemaless engine is never forced to stub it.
type Browser interface {
	Browse(ctx context.Context, req BrowseRequest) (*BrowsePage, error)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/engine/driver/ -race -v`
Expected: PASS.

- [ ] **Step 5: Check the coverage gate, then commit**

```bash
./scripts/go-coverage.sh --check
git add internal/engine/driver/browse.go internal/engine/driver/browse_test.go
git commit -m "feat(engine): add the semantic browse request and page"
```

---

### Task 3: SQLite's dialect rendering

**Files:**
- Create: `internal/engine/driver/sqlite/browse.go`
- Test: `internal/engine/driver/sqlite/browse_test.go`

**Interfaces:**
- Consumes: `driver.Browser`, `BrowseRequest`, `BrowsePage`, `Value`, `Normalize`, `SortKey` (Task 1, 2)
- Produces: `*conn` satisfies `driver.Browser`

**How SQLite paginates, in order of preference:**

1. **Keyset on the primary key.** Deterministic, and does not degrade at depth. SQLite supports row-value comparison (`(a,b) > (?,?)`) from 3.15, so a composite key works in one clause.
2. **Keyset on `rowid`.** Every ordinary SQLite table has one even without a declared primary key, so this covers almost everything the first case misses.
3. **`LIMIT ? OFFSET ?`.** Only for `WITHOUT ROWID` tables lacking a primary key, and for views. Correct but slower the deeper you scroll, which is why it is last.

A caller-supplied `Sort` on a non-unique column cannot be keyset-paginated safely — two rows sharing a value would straddle a page boundary and one would be skipped or repeated. In that case append the tiebreaker (the primary key or `rowid`) to the sort so the ordering becomes total, and keyset still works. **That is the whole trick**: make the sort unique rather than abandoning keyset.

- [ ] **Step 1: Write the failing test**

Create `internal/engine/driver/sqlite/browse_test.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/driver"

	_ "modernc.org/sqlite"
)

func browseFixture(t *testing.T, rows int) driver.Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "browse.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY, email TEXT NOT NULL, score REAL, note TEXT)`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE nokey (a TEXT, b TEXT)`); err != nil {
		t.Fatalf("ddl nokey: %v", err)
	}
	tx, _ := db.Begin()
	for i := 1; i <= rows; i++ {
		if _, err := tx.Exec(`INSERT INTO users VALUES (?,?,?,?)`,
			i, "u"+itoa(i)+"@example.com", float64(i)/2, nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, err := tx.Exec(`INSERT INTO nokey VALUES (?,?)`, "a"+itoa(i), "b"); err != nil {
			t.Fatalf("insert nokey: %v", err)
		}
	}
	_ = tx.Commit()
	_ = db.Close()

	c, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func browser(t *testing.T, c driver.Conn) driver.Browser {
	t.Helper()
	b, ok := c.(driver.Browser)
	if !ok {
		t.Fatal("the sqlite conn does not implement driver.Browser")
	}
	return b
}

func TestBrowseFirstPageReturnsColumnsAndRows(t *testing.T) {
	b := browser(t, browseFixture(t, 10))
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 4,
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Columns) != 4 || page.Columns[0].Name != "id" {
		t.Fatalf("columns = %+v", page.Columns)
	}
	if len(page.Rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(page.Rows))
	}
	if page.Rows[0][0].Kind != driver.ValueInt || page.Rows[0][0].Text != "1" {
		t.Errorf("first cell = %+v", page.Rows[0][0])
	}
	if page.Exhausted {
		t.Error("a full page reported Exhausted")
	}
	if len(page.Keyset) == 0 {
		t.Error("no keyset on a keyset-pagable table")
	}
}

// The property that makes keyset pagination worth having: paging all the way
// through returns every row exactly once, in order, with no gaps or repeats.
func TestBrowsePagesThroughEveryRowExactlyOnce(t *testing.T) {
	const total = 25
	b := browser(t, browseFixture(t, total))

	seen := map[string]int{}
	req := driver.BrowseRequest{Database: "main", Table: "users", Limit: 4}
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		page, err := b.Browse(context.Background(), req)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, row := range page.Rows {
			seen[row[0].Text]++
		}
		if page.Exhausted {
			break
		}
		req.After = page.Keyset
	}

	if len(seen) != total {
		t.Fatalf("saw %d distinct ids, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("id %s appeared %d times", id, n)
		}
	}
}

func TestBrowseExhaustedOnAShortPage(t *testing.T) {
	b := browser(t, browseFixture(t, 3))
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 10,
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if !page.Exhausted {
		t.Error("a short page did not report Exhausted")
	}
}

func TestBrowseHonoursDescendingSort(t *testing.T) {
	b := browser(t, browseFixture(t, 5))
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		Sort: []driver.SortKey{{Column: "id", Desc: true}},
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if page.Rows[0][0].Text != "5" || page.Rows[1][0].Text != "4" {
		t.Errorf("descending order wrong: %s then %s", page.Rows[0][0].Text, page.Rows[1][0].Text)
	}
}

// A non-unique sort must still page correctly, by appending a tiebreaker
// rather than abandoning keyset.
func TestBrowseOnANonUniqueSortStillPagesWithoutGaps(t *testing.T) {
	const total = 12
	b := browser(t, browseFixture(t, total))

	seen := map[string]int{}
	req := driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 5,
		Sort: []driver.SortKey{{Column: "note"}}, // every row is NULL here
	}
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate on a non-unique sort")
		}
		page, err := b.Browse(context.Background(), req)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, row := range page.Rows {
			seen[row[0].Text]++
		}
		if page.Exhausted {
			break
		}
		req.After = page.Keyset
		req.Offset = page.Offset
	}
	if len(seen) != total {
		t.Fatalf("saw %d of %d rows on a non-unique sort", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("id %s appeared %d times", id, n)
		}
	}
}

// A table with no declared primary key still has a rowid, so keyset works.
func TestBrowseUsesRowidWhenThereIsNoPrimaryKey(t *testing.T) {
	b := browser(t, browseFixture(t, 6))
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "nokey", Limit: 3,
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Rows) != 3 {
		t.Fatalf("got %d rows", len(page.Rows))
	}
	if len(page.Keyset) == 0 {
		t.Error("no keyset on a rowid table — it fell back to offset unnecessarily")
	}
	// rowid must not appear as a visible column.
	for _, c := range page.Columns {
		if c.Name == "rowid" {
			t.Error("rowid leaked into the visible columns")
		}
	}
}

func TestBrowseRejectsAnInvalidRequest(t *testing.T) {
	b := browser(t, browseFixture(t, 1))
	if _, err := b.Browse(context.Background(), driver.BrowseRequest{Table: "users"}); err == nil {
		t.Error("a zero limit was accepted")
	}
}

func TestBrowseOnAMissingTableIsNotFound(t *testing.T) {
	b := browser(t, browseFixture(t, 1))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "nope", Limit: 5,
	})
	if err == nil {
		t.Fatal("browsing a missing table succeeded")
	}
}

// An identifier containing a quote must not be able to change the statement.
func TestBrowseQuotesIdentifiers(t *testing.T) {
	b := browser(t, browseFixture(t, 1))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: `users" ; DROP TABLE users; --`, Limit: 5,
	})
	if err == nil {
		t.Fatal("an injected table name was accepted")
	}
	// The real table must still be there.
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 5,
	})
	if err != nil || len(page.Rows) == 0 {
		t.Fatalf("users was damaged by the injection attempt: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/driver/sqlite/ -run TestBrowse -v`
Expected: FAIL — the conn does not implement `driver.Browser`.

- [ ] **Step 3: Write the implementation**

Create `internal/engine/driver/sqlite/browse.go` implementing `Browse` on `*conn`. It must:

- Call `req.Validate()` first and return its error untouched.
- Resolve the sort: use `req.Sort` when given, otherwise the primary-key columns from `Columns()`, otherwise `rowid`.
- **Append a tiebreaker** — the primary key, or `rowid` — whenever the resolved sort is not already unique, so the ordering is total and keyset pagination stays correct.
- Select the table's real columns explicitly (never `SELECT *` plus rowid, which would leak `rowid` into the visible set); select the keyset columns in addition, and strip them from the returned rows.
- Build the keyset predicate with a row-value comparison — `(a, b) > (?, ?)` for ascending, `<` for descending — binding `req.After`'s values as parameters. Never interpolate a value into the SQL.
- Quote every identifier with `c.Quote`.
- Fall back to `LIMIT ? OFFSET ?` only when no unique sort can be formed, and in that case leave `Keyset` nil and set `Offset` to the offset of the next row.
- Normalize every cell through `driver.Normalize`.
- Set `Exhausted` when fewer rows than `Limit` came back.
- Route every error through the existing `classify`, so a missing table still reports `KindNotFound` and a cancelled context still reports `KindCanceled`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/engine/driver/sqlite/ -race -v`
Expected: PASS — the ten browse tests plus the existing suite.

- [ ] **Step 5: Verify cgo is still off, check coverage, commit**

```bash
./scripts/build-sidecars_test.sh     # 6/6
./scripts/go-coverage.sh --check     # 100.0%
git add internal/engine/driver/sqlite/browse.go internal/engine/driver/sqlite/browse_test.go
git commit -m "feat(sqlite): page a table by keyset, falling back to offset"
```

---

### Task 4: The browse RPC method

**Files:**
- Create: `internal/api/browse.go`
- Test: `internal/api/browse_test.go`
- Modify: `cmd/engine/main.go` (register the method)

**Interfaces:**
- Consumes: `Sessions` (existing), `driver.Browser`, `BrowseRequest`, `BrowsePage`
- Produces: `func RegisterBrowse(srv *rpc.Server, sess *Sessions)` registering `browse.page`

**Params:** `{session_id, database, table, sort?, after?, offset?, limit}` — the `BrowseRequest` fields plus the session.
**Result:** the `BrowsePage` verbatim.

**No cursor registry.** Browsing is stateless (Task 2), so this method opens nothing and holds nothing. That is why there is no `browse.close`: there is nothing to close, and therefore nothing to leak when a UI tab is shut, a window is closed, or the shell crashes.

**Capability check:** a driver that does not implement `Browser` must produce a clear `KindUnsupported` error naming the driver, not a nil-pointer panic. Today every driver implements it; the check exists because Redis and MongoDB will not.

- [ ] **Step 1: Write the failing test**

Create `internal/api/browse_test.go` covering:
- a happy page against the real SQLite fixture, asserting columns, rows, and that cells arrive as `{kind,text}` rather than bare JSON values
- paging with `after` returning the next distinct rows
- a bad `session_id` reporting `not_found`
- a validation failure (zero limit) reporting `invalid`
- a missing table reporting `not_found`
- malformed params reporting `CodeInvalidParams`
- a driver without `Browser` reporting `unsupported` — use a fake driver registered under a test id, since every real driver implements it

Reuse the existing `harness` pattern from `internal/api/api_test.go` rather than inventing a second one.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/api/ -run TestBrowse -v`
Expected: FAIL — `undefined: RegisterBrowse`.

- [ ] **Step 3: Write the handler**

Create `internal/api/browse.go`. It must resolve the session, type-assert the connection to `driver.Browser` (returning `KindUnsupported` naming the driver when it fails), build the `BrowseRequest` from the params, call `Browse`, and return the page. Route every error through `ToRPCError` so the `Kind` reaches the UI in the error's `data` member.

- [ ] **Step 4: Register it**

In `cmd/engine/main.go`, add `api.RegisterBrowse(srv, sess)` beside the existing registrations.

- [ ] **Step 5: Prove it over the wire**

Create a SQLite file with a known table, then drive the compiled engine over stdio with `LANTERN_CONFIG_DIR` pointed at a temp directory: save a connection, open a session, and call `browse.page` twice — once for the first page, once echoing back the returned `keyset`. Report the actual JSON, and confirm the second page's rows differ from the first.

**Await each response before sending the next request.** The dispatch loop is concurrent and answers out of order otherwise; a dependent request sent early will read stale state.

- [ ] **Step 6: Coverage and commit**

```bash
go test ./... -race && ./scripts/go-coverage.sh --check
git add internal/api/browse.go internal/api/browse_test.go cmd/engine/main.go
git commit -m "feat(api): expose stateless table browsing over JSON-RPC"
```

---

### Task 5: The browse client

**Files:**
- Create: `src/lib/browse.ts`
- Test: `src/lib/browse.test.ts`

**Interfaces:**
- Consumes: `request` from `src/lib/engine.ts` — the single channel, as always
- Produces:
  - `type ValueKind = 'null' | 'text' | 'int' | 'float' | 'bool' | 'bytes' | 'time'`
  - `interface Value { kind: ValueKind; text: string }`
  - `interface SortKey { column: string; desc: boolean }`
  - `interface BrowsePage { columns: ColumnMeta[]; rows: Value[][]; keyset?: Value[]; exhausted: boolean; offset: number }`
  - `interface ColumnMeta { name: string; data_type: string }`
  - `browsePage(sessionId, req): Promise<BrowsePage>`
  - `const MAX_BROWSE_LIMIT = 1000`

**Cross-check `ValueKind` against `internal/engine/driver/value.go`'s constants** the way `DbErrorKind` was checked — parse both and compare as sets. A drifted member is invisible to the compiler because the value arrives at runtime off a wire TypeScript does not police, and the symptom is a grid branch that silently never fires.

- [ ] **Step 1:** Write `src/lib/browse.test.ts` asserting the method name and parameter shape for a first page and for a keyset continuation, and that a rejected browse surfaces through `describeError` — NOT `asDbError`, whose fallback is what rendered `[object Object]` at five call sites before Plan 2's fix wave.

  **Check `ValueKind` and the limit mechanically, not by hand.** `ValueKind` is
  the second cross-language enum in this codebase. The first one, `DbErrorKind`,
  was introduced as a hand-matched union and drifted the moment an eleventh Kind
  was added. `src/lib/connections.test.ts` already solves this: it reads the Go
  source with Vite's `?raw` import, pulls the constants out with a regex, and
  compares sets — carrying its own negative control, because two empty lists
  compare equal and a regex that quietly stopped matching would make the real
  assertion pass while checking nothing. Copy that shape:

  - read `internal/engine/driver/value.go?raw`, extract the seven `ValueKind`
    string literals, and assert set equality with the TypeScript union
  - read `internal/engine/driver/browse.go?raw`, extract `MaxBrowseLimit`, and
    assert it equals `MAX_BROWSE_LIMIT`
  - include the negative control: assert the extracted list is non-empty and
    the expected length, so a broken regex fails loudly instead of silently

  Prove it goes red from both directions before moving on — add a fake member
  to the Go source, watch it fail, revert; then add one to the TS union, watch
  it fail, revert.
- [ ] **Step 2:** Run it, confirm it fails on the missing import.
- [ ] **Step 3:** Write `src/lib/browse.ts`.
- [ ] **Step 4:** `npm test && npm run test:coverage && npm run typecheck` — all clean at 100%.
- [ ] **Step 5:** Commit: `feat(ui): add the typed browse client`

---

### Task 6: The result grid

**Files:**
- Create: `src/components/ResultGrid.tsx`
- Create: `src/components/ResultGrid.css`
- Test: `src/components/ResultGrid.test.tsx`
- Modify: `package.json` (add the grid dependency)

**Interfaces:**
- Consumes: `browsePage`, `BrowsePage`, `Value` (Task 5)
- Produces: `<ResultGrid sessionId database table />`

**Use `@glideapps/glide-data-grid`.** The spec picked it for one reason: it renders to canvas and builds no DOM node per cell, which is the only way to stay smooth at a million rows. A DOM-based grid will feel fine in these tests and fall over on real data.

**Values render by `kind`, per `Foundations.dc.html`:**

| Kind | Treatment |
|---|---|
| `null` | the dim colour, italic, the literal text `NULL` — never an empty cell, which is a different thing |
| `int`, `float` | right-aligned, mono |
| `bytes` | the dim colour, the summary text (`"1024 bytes"`), never the content |
| everything else | left-aligned, mono for values |

Rows are **26px**, the header **28px**, per the density table. Fetch the next page when the viewport approaches the end of what is loaded, echoing back `keyset` (or `offset`). Stop when `exhausted`.

**A failure must not blank the grid.** Rows already fetched stay on screen; the error renders in the warn colour beneath them, with a retry. Losing a screenful of data because page four failed is worse than the failure.

- [ ] **Step 1:** `npm install @glideapps/glide-data-grid` and check `npm run tauri build` still succeeds — a canvas library that breaks the bundle is better discovered now than in CI.
- [ ] **Step 2:** Write `ResultGrid.test.tsx` covering: the first page renders its columns and rows; NULL renders as `NULL` and not as empty; a numeric cell is right-aligned; scrolling near the end requests the next page with the previous `keyset`; `exhausted` stops further requests; a failed page keeps existing rows and shows a retry; a `canceled` kind renders nothing.
- [ ] **Step 3:** Run it, confirm it fails.
- [ ] **Step 4:** Write the component.
- [ ] **Step 5:** `npm test && npm run test:coverage && npm run typecheck`.
- [ ] **Step 6:** Commit: `feat(ui): add the canvas result grid`

---

### Task 7: Selecting a table shows its rows

**Files:**
- Modify: `src/components/Sidebar.tsx` (raise a selection)
- Modify: `src/App.tsx` (hold the selection, render the grid)
- Modify: `src/components/Sidebar.test.tsx`, `src/App.test.tsx`

**Interfaces:**
- Consumes: `<ResultGrid>` (Task 6)
- Produces: `<Sidebar onSelectTable={(sessionId, database, table) => void}>`

Clicking a table currently only expands it. It must **also** raise a selection, and the main pane must replace `Select a table to see its data.` with the grid for that table. Expanding and selecting are the same click, matching `Main.dc.html`.

Selecting a different table replaces the grid, discarding the previous table's pages — there is no cursor to close, so nothing leaks.

- [ ] **Step 1:** Extend the sidebar and app tests: clicking a table calls `onSelectTable` with the right arguments, the app renders a grid for the selected table, and selecting another table replaces it.
- [ ] **Step 2:** Run, confirm they fail.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** All gates green.
- [ ] **Step 5: End-to-end against a real database.** Create a SQLite file with a few thousand rows, build the sidecars, run the app with `LANTERN_CONFIG_DIR` set, and verify: adding a connection, expanding a table, clicking it, and seeing rows. Scroll to trigger a second page. Report what you observed and what you could not — if you cannot see the window, say so and verify mechanically instead.
- [ ] **Step 6:** Commit: `feat(ui): show a table's rows when it is selected`

---

## Definition of Done

- [ ] `go test ./... -race` passes; Go coverage 100%.
- [ ] `npm test`, `npm run test:coverage` (100%), `npm run typecheck` all clean.
- [ ] `./scripts/build-sidecars_test.sh` 6/6 — neither the grid nor anything else pulled in cgo.
- [ ] `./scripts/ci-workflow_test.sh` passes.
- [ ] Clicking a table in the running app shows its rows.
- [ ] Paging through a table returns every row exactly once — the test that makes keyset pagination worth having.
- [ ] A failed page leaves already-fetched rows on screen.

## Deferred

The SQL editor, its cursor registry for arbitrary result sets, sorting from the column headers, filtering, and inline editing. `BrowseRequest.Sort` exists and is honoured by the driver, so header sorting is a UI change rather than an engine one when it comes.

MySQL is the next plan. `Browser` is deliberately an optional interface with one implementation today; the second one is what proves whether its shape is right.
