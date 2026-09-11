package api

// Covers browse.page (browse.go): the stateless RPC seam over
// driver.Browser. api_test.go's harness registers it (see newHarness), so
// these tests call it exactly the way the seven behavioural tests in
// api_test.go call session.* — no second harness pattern invented.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

// seedItems adds a populated table to the harness's fixture database,
// alongside the empty `users` table newHarness already creates. `users`
// stays empty on purpose: TestBrowseOnAnEmptyTableSerializesRowsAsAnEmptyArray
// depends on a real table with zero rows, which `items` with n>0 cannot give
// it. Opened and closed on its own connection, exactly as the sqlite
// driver's own browseFixture (internal/engine/driver/sqlite/browse_test.go)
// seeds its fixtures, so a session opened afterward dials a file that is
// already fully written rather than racing a still-open handle.
func seedItems(t *testing.T, dbPath string, n int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("seedItems: open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("seedItems: ddl: %v", err)
	}
	for i := 1; i <= n; i++ {
		if _, err := db.Exec(`INSERT INTO items (id, name) VALUES (?, ?)`, i, fmt.Sprintf("item-%d", i)); err != nil {
			t.Fatalf("seedItems: insert: %v", err)
		}
	}
}

// openBrowseSession saves h.db as a connection and opens a session against
// it, returning the session id. Mirrors the save-then-open sequence every
// existing session.* test in api_test.go and coverage_test.go repeats
// inline; browse's tests need it just as often, so it is worth a helper
// here without touching those files' own established pattern.
func openBrowseSession(t *testing.T, h *harness) string {
	t.Helper()
	saved, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	var rec store.Saved
	if err := json.Unmarshal(saved, &rec); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	opened, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(opened, &open); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	return open.SessionID
}

// browsePage mirrors BrowsePage's wire shape field-for-field so tests can
// decode a browse.page result without reaching into driver.BrowsePage
// (which would defeat the point: this package is the transport boundary,
// and these tests exist to prove what actually crosses the wire).
type browsePage struct {
	Columns []struct {
		Name string `json:"name"`
	} `json:"columns"`
	Rows [][]struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"rows"`
	Keyset []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"keyset"`
	SortToken string `json:"sort_token"`
	Exhausted bool   `json:"exhausted"`
}

func TestBrowsePageReturnsColumnsRowsAndCellsAsKindText(t *testing.T) {
	h := newHarness(t)
	seedItems(t, h.db, 4)
	sessionID := openBrowseSession(t, h)

	out, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "items", "limit": 10,
	})
	if err != nil {
		t.Fatalf("browse.page: %v", err)
	}
	var page browsePage
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Columns) != 2 || page.Columns[0].Name != "id" || page.Columns[1].Name != "name" {
		t.Fatalf("columns = %+v", page.Columns)
	}
	if len(page.Rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(page.Rows))
	}
	if page.Rows[0][0].Kind != "int" || page.Rows[0][0].Text != "1" {
		t.Errorf("first cell = %+v, want {kind:int text:1}", page.Rows[0][0])
	}
	if page.Rows[0][1].Kind != "text" || page.Rows[0][1].Text != "item-1" {
		t.Errorf("second cell = %+v, want {kind:text text:item-1}", page.Rows[0][1])
	}
	if !page.Exhausted {
		t.Error("a page covering the whole table did not report Exhausted")
	}
}

// Paging with after must return the NEXT distinct rows, not the same page
// again and not an overlapping one.
func TestBrowseAfterReturnsTheNextDistinctRows(t *testing.T) {
	h := newHarness(t)
	seedItems(t, h.db, 6)
	sessionID := openBrowseSession(t, h)

	out1, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "items", "limit": 4,
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	var page1 browsePage
	if err := json.Unmarshal(out1, &page1); err != nil {
		t.Fatalf("decode page 1: %v", err)
	}
	if page1.Exhausted {
		t.Fatal("page 1 of 6 rows at limit 4 reported Exhausted")
	}
	if len(page1.Keyset) == 0 || page1.SortToken == "" {
		t.Fatalf("page 1 carried no keyset/sort_token: %+v", page1)
	}

	after := make([]map[string]any, len(page1.Keyset))
	for i, v := range page1.Keyset {
		after[i] = map[string]any{"kind": v.Kind, "text": v.Text}
	}
	out2, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "items", "limit": 4,
		"after": after, "sort_token": page1.SortToken,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	var page2 browsePage
	if err := json.Unmarshal(out2, &page2); err != nil {
		t.Fatalf("decode page 2: %v", err)
	}
	if !page2.Exhausted {
		t.Error("page 2, the remainder of a 6-row table, did not report Exhausted")
	}
	if len(page2.Rows) != 2 {
		t.Fatalf("page 2 has %d rows, want 2", len(page2.Rows))
	}

	seen := map[string]bool{}
	for _, r := range page1.Rows {
		seen[r[0].Text] = true
	}
	for _, r := range page2.Rows {
		if seen[r[0].Text] {
			t.Errorf("id %s appeared in both pages", r[0].Text)
		}
		seen[r[0].Text] = true
	}
	if len(seen) != 6 {
		t.Errorf("saw %d distinct rows across both pages, want 6", len(seen))
	}
}

// The single most important property of this transport seam: SortToken
// must survive the round trip through the WIRE FORMAT, not just through Go
// structs. A cursor is the keyset AND the sort it was issued for (see
// driver.BrowseRequest's own doc comment), and the sqlite driver now
// refuses a request carrying After without a matching SortToken. If
// browseParams ever drops sort_token in either direction — say, a
// hand-copied field with a typo'd json tag instead of an embedded
// BrowseRequest — every existing test built on Go values would keep
// passing while pagination silently broke for every real caller, because
// json.Marshal(struct) and json.Unmarshal(&struct) would round-trip
// through the SAME broken tag consistently. So this test takes the actual
// bytes the server wrote for sort_token and keyset out of page 1's raw
// JSON response and splices them, verbatim, into page 2's raw JSON
// request — never touching a Go struct in between.
func TestBrowseSortTokenRoundTripsThroughRawJSON(t *testing.T) {
	h := newHarness(t)
	seedItems(t, h.db, 5)
	sessionID := openBrowseSession(t, h)

	handler, ok := h.srv.Handler("browse.page")
	if !ok {
		t.Fatal("browse.page is not registered")
	}

	req1 := fmt.Sprintf(`{"session_id":%q,"database":"main","table":"items","limit":3}`, sessionID)
	res1, err := handler(context.Background(), json.RawMessage(req1))
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	raw1, err := json.Marshal(res1)
	if err != nil {
		t.Fatalf("marshal page 1 result: %v", err)
	}

	var fields1 map[string]json.RawMessage
	if err := json.Unmarshal(raw1, &fields1); err != nil {
		t.Fatalf("decode page 1 as raw fields: %v", err)
	}
	sortToken, ok := fields1["sort_token"]
	if !ok || len(sortToken) == 0 || string(sortToken) == `""` {
		t.Fatalf("page 1 JSON carried no usable sort_token: %s", raw1)
	}
	keyset, ok := fields1["keyset"]
	if !ok || len(keyset) == 0 || string(keyset) == "null" {
		t.Fatalf("page 1 JSON carried no usable keyset: %s", raw1)
	}

	// req2 is built by string-splicing the literal bytes lifted from raw1,
	// not by unmarshalling into and re-marshalling from a Go type. That is
	// deliberate: it is the only way to prove the WIRE round-trips, as
	// opposed to proving Go's json package is self-consistent.
	req2 := fmt.Sprintf(`{"session_id":%q,"database":"main","table":"items","limit":3,"after":%s,"sort_token":%s}`,
		sessionID, string(keyset), string(sortToken))
	res2, err := handler(context.Background(), json.RawMessage(req2))
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	raw2, err := json.Marshal(res2)
	if err != nil {
		t.Fatalf("marshal page 2 result: %v", err)
	}

	var page1, page2 browsePage
	if err := json.Unmarshal(raw1, &page1); err != nil {
		t.Fatalf("decode page 1: %v", err)
	}
	if err := json.Unmarshal(raw2, &page2); err != nil {
		t.Fatalf("decode page 2: %v", err)
	}
	if len(page1.Rows) == 0 || len(page2.Rows) == 0 {
		t.Fatalf("expected non-empty pages, got %d and %d rows", len(page1.Rows), len(page2.Rows))
	}
	if page1.Rows[0][0].Text == page2.Rows[0][0].Text {
		t.Fatalf("page 2 repeated page 1's first row (%s): the cursor did not continue", page1.Rows[0][0].Text)
	}
	if !page2.Exhausted {
		t.Error("page 2, the remainder of a 5-row table at limit 3, did not report Exhausted")
	}
	if len(page2.Rows) != 2 {
		t.Fatalf("page 2 has %d rows, want 2", len(page2.Rows))
	}
}

func TestBrowseReportsNotFoundForAnUnknownSessionID(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "browse.page", map[string]any{
		"session_id": "no-such-session", "database": "main", "table": "users", "limit": 10,
	})
	if err == nil {
		t.Fatal("browse.page succeeded for an unknown session id")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", kind, dberr.KindNotFound)
	}
}

func TestBrowseReportsInvalidForAZeroLimit(t *testing.T) {
	h := newHarness(t)
	sessionID := openBrowseSession(t, h)

	_, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "users", "limit": 0,
	})
	if err == nil {
		t.Fatal("browse.page succeeded with a zero limit")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", kind, dberr.KindInvalid)
	}
}

// Adversarial fixture: an unbounded limit is how a UI bug becomes an
// out-of-memory crash on a huge table (see driver.MaxBrowseLimit's own
// comment). The cap must reach the caller as an ordinary invalid-request
// error, not be silently clamped.
func TestBrowseReportsInvalidForALimitAboveMaxBrowseLimit(t *testing.T) {
	h := newHarness(t)
	sessionID := openBrowseSession(t, h)

	_, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "users", "limit": driver.MaxBrowseLimit + 1,
	})
	if err == nil {
		t.Fatal("browse.page succeeded with a limit above MaxBrowseLimit")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", kind, dberr.KindInvalid)
	}
}

func TestBrowseReportsNotFoundForAMissingTable(t *testing.T) {
	h := newHarness(t)
	sessionID := openBrowseSession(t, h)

	_, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "does_not_exist", "limit": 10,
	})
	if err == nil {
		t.Fatal("browse.page succeeded for a table that does not exist")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", kind, dberr.KindNotFound)
	}
}

// Adversarial fixture: malformed params (a JSON string, not an object) must
// surface as CodeInvalidParams, the same shape every other method in this
// package reports it under, rather than a 500-equivalent internal error.
func TestBrowseReportsInvalidParamsOnMalformedParams(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "browse.page", "not-an-object")
	if err == nil {
		t.Fatal("browse.page succeeded on malformed params")
	}
	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("err = %T, want *rpc.Error", err)
	}
	if re.Code != rpc.CodeInvalidParams {
		t.Errorf("code = %d, want %d (CodeInvalidParams)", re.Code, rpc.CodeInvalidParams)
	}
}

// Adversarial fixture: every real driver implements driver.Browser today
// (the capability check exists only because Redis and MongoDB will not), so
// this is the only way to reach the unsupported branch — a fake driver
// registered under a test-unique id, reusing coverage_test.go's fakeConn,
// which implements driver.Conn but deliberately has no Browse method.
func TestBrowseReportsUnsupportedForADriverWithoutBrowser(t *testing.T) {
	h := newHarness(t)
	conn := &fakeConn{}
	id := registerFakeDriver(t, conn, nil)

	rec, err := h.st.Save(store.Saved{Name: "x", Driver: id, File: "unused"}, "")
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	opened, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(opened, &open)

	_, err = h.call(t, "browse.page", map[string]any{
		"session_id": open.SessionID, "database": "main", "table": "whatever", "limit": 10,
	})
	if err == nil {
		t.Fatal("browse.page succeeded against a driver with no Browse method")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindUnsupported {
		t.Errorf("kind = %q, want %q", kind, dberr.KindUnsupported)
	}
}

// Adversarial fixture: a table with zero rows must serialize "rows":[], not
// "rows":null (see driver.BrowsePage.MarshalJSON's own comment for why: the
// TypeScript side declares Rows as an array, and null landing there has
// blanked the whole app before). Asserted directly against the JSON bytes,
// not through a decoded Go struct, because a nil slice and an empty slice
// decode identically into a Go []T — the bug lives entirely in the bytes.
func TestBrowseOnAnEmptyTableSerializesRowsAsAnEmptyArray(t *testing.T) {
	h := newHarness(t)
	sessionID := openBrowseSession(t, h) // `users`, from newHarness, has zero rows.

	out, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "users", "limit": 10,
	})
	if err != nil {
		t.Fatalf("browse.page: %v", err)
	}
	if !strings.Contains(string(out), `"rows":[]`) {
		t.Errorf("result did not contain literal \"rows\":[]: %s", out)
	}
}

// Adversarial fixture: After present with no SortToken must surface as
// KindInvalid, not a 500/CodeInternal. A cursor is the keyset AND the sort
// it was issued for (see driver.BrowseRequest.SortToken's own comment); the
// sqlite driver refuses this pairing rather than silently paging from a
// boundary no sort produced, and that refusal must reach the caller as an
// ordinary, well-shaped RPC error like any other validation failure.
func TestBrowseReportsInvalidWhenAfterHasNoSortToken(t *testing.T) {
	h := newHarness(t)
	seedItems(t, h.db, 3)
	sessionID := openBrowseSession(t, h)

	_, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "items", "limit": 10,
		"after": []map[string]any{{"kind": "int", "text": "1"}},
		// sort_token deliberately omitted.
	})
	if err == nil {
		t.Fatal("browse.page succeeded with After but no SortToken")
	}
	if kind := rpcErrorKind(t, err); kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", kind, dberr.KindInvalid)
	}
}

// -- fix wave D-1/D-2: values that only break in transit ------------------
//
// Everything above this line pages Go values. That is exactly the gap D-1
// lived in: a driver-level page walk over the same tables is correct, and
// the corruption appears only once the keyset has been through
// encoding/json and back. These tests page the WIRE, splicing the literal
// bytes the server wrote into the next request, the same technique
// TestBrowseSortTokenRoundTripsThroughRawJSON uses for sort_token.

// maxJSONPages bounds pageThroughJSON. A keyset that fails to advance
// repeats its page forever; a test that hangs reports nothing.
const maxJSONPages = 20

// seedMojibake creates fix wave D-1's confirmed repro table verbatim: a
// TEXT column holding bytes no UTF-8 decoder accepts. Not exotic — this is
// any SQLite file written by a latin-1 application, and CAST(x'..' AS TEXT)
// is the one-line constructor for it.
func seedMojibake(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("seedMojibake: open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE mojibake (id INTEGER PRIMARY KEY, x TEXT)`); err != nil {
		t.Fatalf("seedMojibake: ddl: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO mojibake (x) VALUES ('a'), (CAST(x'ff' AS TEXT)), (CAST(x'fe' AS TEXT))`,
	); err != nil {
		t.Fatalf("seedMojibake: insert: %v", err)
	}
}

// pageThroughJSON pages a table to termination the way the shell does:
// every continuation is built by splicing the literal keyset, sort_token
// and offset bytes the server just wrote into the next request, so each
// cursor value makes the full JSON round trip. It returns the pages it
// collected and whatever error ended the walk, and fails the test outright
// if the walk does not terminate.
func pageThroughJSON(t *testing.T, h *harness, sessionID, table, sortJSON string, limit int) ([]browsePage, error) {
	t.Helper()
	handler, ok := h.srv.Handler("browse.page")
	if !ok {
		t.Fatal("browse.page is not registered")
	}

	var pages []browsePage
	cont := ""
	for i := 0; ; i++ {
		if i > maxJSONPages {
			t.Fatalf("pagination did not terminate after %d pages", i)
		}
		req := fmt.Sprintf(`{"session_id":%q,"database":"main","table":%q,"limit":%d%s%s}`,
			sessionID, table, limit, sortJSON, cont)
		res, err := handler(context.Background(), json.RawMessage(req))
		if err != nil {
			return pages, err
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal page %d: %v", i, err)
		}
		var page browsePage
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatalf("decode page %d: %v", i, err)
		}
		pages = append(pages, page)
		if page.Exhausted {
			return pages, nil
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("decode page %d as raw fields: %v", i, err)
		}
		cont = `,"offset":` + string(fields["offset"])
		if keyset, ok := fields["keyset"]; ok {
			cont += `,"after":` + string(keyset) + `,"sort_token":` + string(fields["sort_token"])
		}
	}
}

// idsOf collects the first column of every row of every page, in order.
func idsOf(pages []browsePage) []string {
	var ids []string
	for _, page := range pages {
		for _, row := range page.Rows {
			ids = append(ids, row[0].Text)
		}
	}
	return ids
}

// D-1's data half. The keyset here is the id, so paging is never at risk —
// what is at risk is the VALUE: a TEXT cell holding bytes that are not
// UTF-8 cannot cross encoding/json as text, and used to arrive as the
// Unicode replacement character with kind "text", indistinguishable from a
// row that genuinely contains one.
func TestBrowseCarriesNonUTF8TextAcrossJSONWithoutManglingIt(t *testing.T) {
	h := newHarness(t)
	seedMojibake(t, h.db)
	sessionID := openBrowseSession(t, h)

	pages, err := pageThroughJSON(t, h, sessionID, "mojibake", "", 1)
	if err != nil {
		t.Fatalf("paging by the default sort: %v", err)
	}
	if got := idsOf(pages); len(got) != 3 || got[0] != "1" || got[1] != "2" || got[2] != "3" {
		t.Fatalf("ids = %v, want [1 2 3] exactly once each", got)
	}
	for i, want := range []struct{ kind, text string }{
		{"text", "a"},
		{"bytes", "1 bytes"},
		{"bytes", "1 bytes"},
	} {
		cell := pages[i].Rows[0][1]
		if cell.Kind != want.kind || cell.Text != want.text {
			t.Errorf("row %d x = %+v, want {kind:%s text:%s}", i+1, cell, want.kind, want.text)
		}
		if strings.ContainsRune(cell.Text, '�') {
			t.Errorf("row %d x came back as the replacement character: %q", i+1, cell.Text)
		}
	}
}

// D-1's paging half, and the exact repro from the brief: the same table
// sorted on the non-UTF-8 column, one row at a time, over the wire. The
// replacement character sorts BELOW the raw bytes it replaced under
// SQLite's memcmp collation, so the mangled cursor used to re-match its own
// row and serve it forever while the row before it became unreachable.
//
// Terminating with a clear error is the accepted outcome (see the report):
// the value genuinely cannot be carried in a cursor, and saying so beats
// both the silent loop and a silently wrong page.
func TestBrowseOnANonUTF8SortColumnTerminatesInsteadOfPagingForever(t *testing.T) {
	h := newHarness(t)
	seedMojibake(t, h.db)
	sessionID := openBrowseSession(t, h)

	pages, err := pageThroughJSON(t, h, sessionID, "mojibake", `,"sort":[{"column":"x"}]`, 1)
	if err == nil {
		// The other acceptable ending: it paged the whole table cleanly.
		if got := idsOf(pages); len(got) != 3 {
			t.Fatalf("paging finished but returned %v, want every row exactly once", got)
		}
		return
	}
	seen := map[string]int{}
	for _, id := range idsOf(pages) {
		seen[id]++
		if seen[id] > 1 {
			t.Errorf("id %s was served %d times before the walk ended: %v", id, seen[id], idsOf(pages))
		}
	}
}

// D-2. ValueBytes had never been produced by a real row: the cursor turned
// every []byte into a string before Normalize could classify it, so a BLOB
// arrived as text full of control characters and ResultGrid's `case 'bytes'`
// was unreachable in production. A real browse of a real BLOB column is the
// only test that can say otherwise.
func TestBrowseRendersABlobColumnAsBytes(t *testing.T) {
	h := newHarness(t)
	db, err := sql.Open("sqlite", h.db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE photos (id INTEGER PRIMARY KEY, data BLOB)`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	// A real JPEG's opening bytes: not valid UTF-8, which is what tells an
	// opaque blob from the CHAR/VARCHAR a networked driver also hands back
	// as []byte.
	if _, err := db.Exec(`INSERT INTO photos (id, data) VALUES (1, x'FFD8FFE000')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = db.Close()
	sessionID := openBrowseSession(t, h)

	out, err := h.call(t, "browse.page", map[string]any{
		"session_id": sessionID, "database": "main", "table": "photos", "limit": 10,
	})
	if err != nil {
		t.Fatalf("browse.page: %v", err)
	}
	var page browsePage
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(page.Rows))
	}
	cell := page.Rows[0][1]
	if cell.Kind != "bytes" || cell.Text != "5 bytes" {
		t.Errorf("blob cell = %+v, want {kind:bytes text:5 bytes}", cell)
	}
}
