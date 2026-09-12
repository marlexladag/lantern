package sqlite

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/driver/keyset"

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
		// Carry the token forward exactly as a real caller must: a cursor is
		// the keyset AND the sort it was issued for, and Browse refuses the
		// pair split up.
		req.SortToken = page.SortToken
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
		// Carry the token forward exactly as a real caller must: a cursor is
		// the keyset AND the sort it was issued for, and Browse refuses the
		// pair split up.
		req.SortToken = page.SortToken
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

// -- adversarial fixtures ------------------------------------------------
//
// Every fixture above is well-shaped: a rowid table, a declared key, no
// NULLs where they matter, a name that needs no quoting. The bugs this
// driver can actually ship are all in the shapes below — an empty table, a
// cursor that lands on the last row, a sort column that is entirely
// duplicates or entirely NULL, a table SQLite gives no rowid, a column
// literally named "rowid", a key whose value cannot survive being rendered
// as text. They earn their keep by being run through pageAll, which asserts
// the property that matters rather than the shape of one page: paging a
// table to exhaustion returns every row exactly once.

func browseOpen(t *testing.T, readOnly bool, stmts ...string) driver.Browser {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adversarial.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("stmt %q: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	c, err := New().Open(context.Background(), driver.ConnConfig{
		Driver: "sqlite", File: path, ReadOnly: readOnly,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return browser(t, c)
}

func browseOn(t *testing.T, stmts ...string) driver.Browser {
	t.Helper()
	return browseOpen(t, false, stmts...)
}

// pageAll pages req to exhaustion, echoing back exactly what the driver
// handed out, and returns every row it saw in order. The page-count guard is
// load-bearing: a keyset predicate that fails to advance repeats its page
// forever, and a test that hangs reports nothing.
func pageAll(t *testing.T, b driver.Browser, req driver.BrowseRequest) [][]driver.Value {
	t.Helper()
	var all [][]driver.Value
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
		page, err := b.Browse(context.Background(), req)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		all = append(all, page.Rows...)
		if page.Exhausted {
			return all
		}
		// A cursor is the keyset AND the sort it was issued for; Browse
		// refuses the pair split up, exactly as a real caller must not.
		req.After, req.Offset, req.SortToken = page.Keyset, page.Offset, page.SortToken
	}
}

func texts(rows [][]driver.Value, col int) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row[col].Text
	}
	return out
}

func wantEachOnce(t *testing.T, got []string, want []string) {
	t.Helper()
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	if len(got) != len(want) {
		t.Errorf("got %d rows %v, want %d", len(got), got, len(want))
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Errorf("%q appeared %d times, want exactly once (whole result: %v)", w, seen[w], got)
		}
	}
}

func wantOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rows %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestBrowseOnAnEmptyTableReturnsColumnsAndNoRows(t *testing.T) {
	b := browser(t, browseFixture(t, 0))
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 5,
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Columns) != 4 {
		t.Errorf("an empty table reported %d columns, want 4", len(page.Columns))
	}
	if len(page.Rows) != 0 {
		t.Errorf("an empty table returned %d rows", len(page.Rows))
	}
	if !page.Exhausted {
		t.Error("an empty table did not report Exhausted")
	}
	if page.Keyset != nil {
		t.Errorf("an empty table handed out a keyset: %+v", page.Keyset)
	}
}

// The page that lands exactly on the last row cannot know it is last: it is
// full, so it reports a keyset. The continuation must come back empty and
// exhausted rather than repeating anything.
func TestBrowseContinuationPastTheLastRowReturnsNothing(t *testing.T) {
	b := browser(t, browseFixture(t, 8))
	req := driver.BrowseRequest{Database: "main", Table: "users", Limit: 4}

	var last *driver.BrowsePage
	for i := 0; i < 2; i++ {
		page, err := b.Browse(context.Background(), req)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		if page.Exhausted {
			t.Fatalf("page %d of 4 rows out of 8 reported Exhausted", i)
		}
		req.After, req.SortToken, last = page.Keyset, page.SortToken, page
	}

	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 4, After: last.Keyset,
		SortToken: last.SortToken,
	})
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if len(page.Rows) != 0 {
		t.Errorf("a continuation past the last row returned %d rows", len(page.Rows))
	}
	if !page.Exhausted {
		t.Error("an empty continuation did not report Exhausted")
	}
}

const dupDDL = `CREATE TABLE dups (id INTEGER PRIMARY KEY, bucket TEXT NOT NULL)`

func dupRows() []string {
	// Twelve rows across three buckets of unequal size, so that a page of
	// five always cuts through the middle of a bucket — the boundary a
	// tiebreaker exists for.
	return []string{
		`INSERT INTO dups VALUES (1,'a'),(2,'a'),(3,'a'),(4,'a'),(5,'a'),
		 (6,'b'),(7,'b'),(8,'b'),(9,'b'),(10,'c'),(11,'c'),(12,'c')`,
	}
}

func allIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = itoa(i + 1)
	}
	return out
}

// The case the tiebreaker exists for: every page boundary falls inside a run
// of identical sort values, where a keyset on the sort column alone would
// skip or repeat whichever rows shared the boundary value.
func TestBrowseOnDuplicateSortValuesPagesEveryRowExactlyOnce(t *testing.T) {
	b := browseOn(t, append([]string{dupDDL}, dupRows()...)...)
	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: "dups", Limit: 5,
		Sort: []driver.SortKey{{Column: "bucket"}},
	})
	wantEachOnce(t, texts(rows, 0), allIDs(12))
	wantOrder(t, texts(rows, 1), []string{"a", "a", "a", "a", "a", "b", "b", "b", "b", "c", "c", "c"})
}

func TestBrowseOnDuplicateSortValuesDescendingPagesEveryRowExactlyOnce(t *testing.T) {
	b := browseOn(t, append([]string{dupDDL}, dupRows()...)...)
	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: "dups", Limit: 5,
		Sort: []driver.SortKey{{Column: "bucket", Desc: true}},
	})
	wantEachOnce(t, texts(rows, 0), allIDs(12))
	wantOrder(t, texts(rows, 1), []string{"c", "c", "c", "b", "b", "b", "b", "a", "a", "a", "a", "a"})
}

const nullsDDL = `CREATE TABLE nulls (id INTEGER PRIMARY KEY, tag TEXT)`

var nullsRows = []string{
	`INSERT INTO nulls VALUES (1,NULL),(2,'m'),(3,NULL),(4,'z'),(5,NULL),(6,'m'),(7,NULL)`,
}

// NULLs are where a row-value keyset silently loses rows: the comparison
// evaluates to NULL rather than true or false, so WHERE drops everything
// past the boundary. SQLite sorts NULLs first ascending, so the page
// boundary here lands inside the run of them.
func TestBrowseOnAPartlyNullSortColumnPagesEveryRowExactlyOnce(t *testing.T) {
	b := browseOn(t, append([]string{nullsDDL}, nullsRows...)...)
	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: "nulls", Limit: 2,
		Sort: []driver.SortKey{{Column: "tag"}},
	})
	wantEachOnce(t, texts(rows, 0), allIDs(7))
	wantOrder(t, texts(rows, 0), []string{"1", "3", "5", "7", "2", "6", "4"})
}

// Descending, the same NULLs sort LAST, so "after" means something different
// on both halves of the column and the boundary lands in the values instead.
func TestBrowseDescendingOnAPartlyNullSortColumnPagesEveryRowExactlyOnce(t *testing.T) {
	b := browseOn(t, append([]string{nullsDDL}, nullsRows...)...)
	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: "nulls", Limit: 2,
		Sort: []driver.SortKey{{Column: "tag", Desc: true}},
	})
	wantEachOnce(t, texts(rows, 0), allIDs(7))
	wantOrder(t, texts(rows, 0), []string{"4", "2", "6", "1", "3", "5", "7"})
}

// A descending sort on a column that is nothing but NULLs: every page
// boundary sits between two NULLs, where the "strictly after" term is
// vacuously false and only the tiebreaker can separate the rows.
func TestBrowseDescendingOnAnAllNullSortColumnPagesEveryRowExactlyOnce(t *testing.T) {
	const total = 9
	b := browser(t, browseFixture(t, total))
	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 4,
		Sort: []driver.SortKey{{Column: "note", Desc: true}},
	})
	wantEachOnce(t, texts(rows, 0), allIDs(total))
}

func TestBrowseOnARealSortColumnPagesEveryRowExactlyOnce(t *testing.T) {
	const total = 11
	b := browser(t, browseFixture(t, total))
	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 3,
		Sort: []driver.SortKey{{Column: "score"}},
	})
	wantEachOnce(t, texts(rows, 0), allIDs(total))
	wantOrder(t, texts(rows, 0), allIDs(total))
}

func TestBrowseAcceptsASortColumnInAnyCase(t *testing.T) {
	b := browser(t, browseFixture(t, 3))
	page, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 3,
		Sort: []driver.SortKey{{Column: "ID", Desc: true}},
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	wantOrder(t, texts(page.Rows, 0), []string{"3", "2", "1"})
}

// A DATETIME column is the one modernc.org/sqlite converts to time.Time, so
// its Value carries an RFC3339 rendering rather than the text SQLite stored.
// Binding that back would compare against a string the column does not hold,
// so the driver must page this by offset instead — and still see every row.
func TestBrowseOnADatetimeSortColumnPagesByOffset(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE events (id INTEGER PRIMARY KEY, at DATETIME NOT NULL)`,
		`INSERT INTO events VALUES (1,'2024-03-01 09:00:00'),(2,'2024-03-01 10:00:00'),
		 (3,'2024-03-02 09:00:00'),(4,'2024-03-02 10:00:00'),(5,'2024-03-03 09:00:00')`)

	req := driver.BrowseRequest{
		Database: "main", Table: "events", Limit: 2,
		Sort: []driver.SortKey{{Column: "at"}},
	}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if page.Keyset != nil {
		t.Errorf("a time-valued sort handed out a keyset it cannot accept back: %+v", page.Keyset)
	}
	if page.Offset != 2 {
		t.Errorf("Offset = %d, want 2", page.Offset)
	}
	wantOrder(t, texts(pageAll(t, b, req), 0), allIDs(5))
}

// A BLOB key has no faithful text form, and SQLite sorts every BLOB above
// every TEXT — so binding a rendered blob back would match every row and
// serve the same page forever. Offset paging is correct here, and the page
// guard inside pageAll is what proves the loop terminates.
func TestBrowseOnABlobPrimaryKeyPagesByOffset(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE keyed (k BLOB PRIMARY KEY, label TEXT NOT NULL)`,
		`INSERT INTO keyed VALUES (x'ff01','a'),(x'ff02','b'),(x'ff03','c'),(x'ff04','d'),(x'ff05','e')`)

	req := driver.BrowseRequest{Database: "main", Table: "keyed", Limit: 2}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if page.Keyset != nil {
		t.Errorf("a blob key handed out a keyset: %+v", page.Keyset)
	}
	wantOrder(t, texts(pageAll(t, b, req), 1), []string{"a", "b", "c", "d", "e"})
}

// SQLite's affinities are preferences, not constraints: a TEXT column stores
// a BLOB handed to it verbatim. The declared type said the key would page,
// the value says otherwise, and refusing beats handing back a cursor that
// would silently serve the wrong rows.
func TestBrowseRefusesAKeysetItCouldNotAcceptBack(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE smuggled (k TEXT PRIMARY KEY, label TEXT NOT NULL)`,
		`INSERT INTO smuggled VALUES (x'ff01','a'),(x'ff02','b'),(x'ff03','c')`)

	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "smuggled", Limit: 2,
	})
	if err == nil {
		t.Fatal("a keyset was built from a value that cannot be bound back")
	}
	if got := dberr.From(err); got.Kind != dberr.KindUnsupported {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindUnsupported, err)
	}
}

// A WITHOUT ROWID table has no rowid to fall back on, but SQLite requires it
// to declare a primary key and enforces that key NOT NULL — so there, and
// only there, the primary key is a sound tiebreaker.
func TestBrowseOnAWithoutRowidTableKeysetsOnThePrimaryKey(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE pairs (a TEXT, b TEXT, note TEXT, PRIMARY KEY (a,b)) WITHOUT ROWID`,
		`INSERT INTO pairs VALUES ('x','1','n'),('x','2','n'),('y','1','n'),('y','2','n'),('z','1','n')`)

	req := driver.BrowseRequest{Database: "main", Table: "pairs", Limit: 2}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Keyset) != 2 {
		t.Fatalf("keyset = %+v, want the two primary-key columns", page.Keyset)
	}
	wantOrder(t, texts(pageAll(t, b, req), 1), []string{"1", "2", "1", "2", "1"})
}

// The same table sorted by a column that is not part of the key: the whole
// key has to be appended, not just the part of it the sort happens to name.
//
// EVERY page size from 1 to 6, because the property only becomes observable
// when a page boundary falls INSIDE a tie group. This fixture ties in groups
// of two, so at limit 4 — the only size this test used to run — every
// boundary lands cleanly between groups, and deleting the appended key
// changes nothing: a mutation run removed the tiebreaker and left all
// packages green while coverage still read 100%. Limits 3 and 5 each lose a
// row without it, and limit 1 loses three. A test that names a property has
// to run the inputs that can see it; this one named it and did not.
func TestBrowseOnAWithoutRowidTableAppendsTheKeyToACallerSort(t *testing.T) {
	for limit := 1; limit <= 6; limit++ {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			b := browseOn(t,
				`CREATE TABLE pairs (a TEXT, b TEXT, note TEXT, PRIMARY KEY (a,b)) WITHOUT ROWID`,
				`INSERT INTO pairs VALUES ('x','1','same'),('x','2','same'),('y','1','same'),
				 ('y','2','same'),('z','1','same'),('z','2','same')`)

			rows := pageAll(t, b, driver.BrowseRequest{
				Database: "main", Table: "pairs", Limit: limit,
				Sort: []driver.SortKey{{Column: "note"}, {Column: "a"}},
			})
			// Both key columns: `a` alone cannot tell the two rows of a tie
			// group apart, which is the whole reason `b` has to be appended.
			wantOrder(t, texts(rows, 0), []string{"x", "x", "y", "y", "z", "z"})
			wantOrder(t, texts(rows, 1), []string{"1", "2", "1", "2", "1", "2"})
		})
	}
}

// A view has neither a rowid nor a key, so nothing can make its ordering
// total. Offset paging is the honest answer, and Keyset must stay absent so
// the caller never echoes back a cursor the driver cannot honour.
func TestBrowseOnAViewPagesByOffset(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE source (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`,
		`INSERT INTO source VALUES (1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e')`,
		`CREATE VIEW labels AS SELECT id, label FROM source`)

	req := driver.BrowseRequest{Database: "main", Table: "labels", Limit: 2}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if page.Keyset != nil {
		t.Errorf("a view handed out a keyset: %+v", page.Keyset)
	}
	if page.Offset != 2 {
		t.Errorf("Offset = %d, want 2", page.Offset)
	}
	wantEachOnce(t, texts(pageAll(t, b, req), 0), allIDs(5))
}

// "rowid" is a legal column name, and a column of that name SHADOWS the real
// rowid: an unqualified reference silently resolves to the user's column,
// which is under no obligation to be unique. SQLite keeps two more spellings
// for exactly this, and the driver has to reach for one of them.
func TestBrowseOnATableWithAColumnNamedRowidStillKeysets(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE shadowed (rowid TEXT, label TEXT NOT NULL)`,
		`INSERT INTO shadowed VALUES ('same','a'),('same','b'),('same','c'),('same','d'),('same','e')`)

	req := driver.BrowseRequest{Database: "main", Table: "shadowed", Limit: 2}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Keyset) == 0 {
		t.Error("a shadowed rowid fell back to offset when _rowid_ was available")
	}
	if len(page.Columns) != 2 {
		t.Errorf("columns = %+v, want the table's own two", page.Columns)
	}
	wantOrder(t, texts(pageAll(t, b, req), 1), []string{"a", "b", "c", "d", "e"})
}

// All three spellings shadowed, so the rowid is unreachable by any
// expression and nothing else on this table is guaranteed unique.
func TestBrowseWhenEveryRowidSpellingIsShadowedPagesByOffset(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE hidden (rowid TEXT, _rowid_ TEXT, oid TEXT, label TEXT NOT NULL)`,
		`INSERT INTO hidden VALUES ('1','1','1','a'),('1','1','1','b'),('1','1','1','c')`)

	req := driver.BrowseRequest{Database: "main", Table: "hidden", Limit: 2}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if page.Keyset != nil {
		t.Errorf("an unreachable rowid still produced a keyset: %+v", page.Keyset)
	}
	wantEachOnce(t, texts(pageAll(t, b, req), 3), []string{"a", "b", "c"})
}

// A quote inside an identifier has to survive both the table name and the
// sort column, in the SELECT list, the ORDER BY and the keyset predicate.
func TestBrowseOnIdentifiersThatNeedQuoting(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE "we""ird" (id INTEGER PRIMARY KEY, "co""l" TEXT NOT NULL)`,
		`INSERT INTO "we""ird" VALUES (1,'same'),(2,'same'),(3,'same'),(4,'same'),(5,'same')`)

	rows := pageAll(t, b, driver.BrowseRequest{
		Database: "main", Table: `we"ird`, Limit: 2,
		Sort: []driver.SortKey{{Column: `co"l`}},
	})
	wantOrder(t, texts(rows, 0), allIDs(5))
}

func TestBrowseWorksOnAReadOnlyConnection(t *testing.T) {
	b := browseOpen(t, true,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`,
		`INSERT INTO t VALUES (1,'a'),(2,'b'),(3,'c')`)

	rows := pageAll(t, b, driver.BrowseRequest{Database: "main", Table: "t", Limit: 2})
	wantOrder(t, texts(rows, 0), allIDs(3))
}

func TestBrowseRejectsAnUnknownDatabase(t *testing.T) {
	b := browser(t, browseFixture(t, 1))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "elsewhere", Table: "users", Limit: 5,
	})
	if err == nil {
		t.Fatal("browsing an unknown database succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindNotFound, err)
	}
}

func TestBrowseRejectsAnUnknownSortColumn(t *testing.T) {
	b := browser(t, browseFixture(t, 1))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 5,
		Sort: []driver.SortKey{{Column: "nope"}},
	})
	if err == nil {
		t.Fatal("an unknown sort column was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindInvalid, err)
	}
}

// A Keyset is opaque, but it is not unvalidated: a cursor of the wrong width
// cannot be matched against the sort it was supposedly taken from, and
// guessing would silently serve a page from the wrong place.
//
// THE GUARD IS LOAD-BEARING, not tidiness. keysetPredicate indexes After
// once per order term; a cursor narrower than the sort panics it with
// "index out of range [1] with length 1", and the engine's dispatch
// goroutine goes down with it. The token is a sha256 over public data —
// table name, resolved order — so it is freely recomputable by anyone who
// can send a request, which makes a crafted short cursor with a genuine
// token reachable rather than theoretical. Confirmed by deleting the check
// and running exactly this request.
//
// The cursor therefore carries a REAL token, taken from a real first page
// under this same sort. The version before this one sent no token at all,
// so the missing-token check caught it first and the width check was never
// reached — and because both report KindInvalid, the assertion could not
// tell which one had fired. That is this project's recurring defect, "a
// Kind check alone is not an assertion when two causes produce the same
// Kind", reintroduced in the very diff that recorded learning it. So the
// message is pinned too.
func TestBrowseRejectsACursorOfTheWrongWidth(t *testing.T) {
	b := browser(t, browseFixture(t, 5))
	first, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Keyset) != 2 || first.SortToken == "" {
		t.Fatalf("users' default sort should resolve to [id, rowid]; got keyset %+v token %q",
			first.Keyset, first.SortToken)
	}

	_, err = b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		// One value short of the sort's width, with the token that sort
		// really did issue: the width check is now the only thing that can
		// refuse this, and without it this call panics.
		After: first.Keyset[:1], SortToken: first.SortToken,
	})
	if err == nil {
		t.Fatal("a cursor narrower than the sort was accepted")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindInvalid, err)
	}
	if !strings.Contains(got.Message, "does not match the sort") {
		t.Errorf("message = %q, want the width refusal — a token check reports the same Kind", got.Message)
	}
}

// -- a cursor must name the sort it came from ----------------------------
//
// A Keyset says only "the row after this one" — it carries no description
// of the ordering that made "after" meaningful. TestBrowseRejectsACursorOfTheWrongWidth
// catches a cursor whose WIDTH no longer matches the current sort, but two
// single-column sorts are the same width, so that check is blind to exactly
// the case that corrupts a grid silently: sort by one column, page a while,
// switch to a different column of the same sort width, and a keyset alone
// cannot tell the new request its After no longer names a boundary that
// sort produces. BrowsePage.SortToken and BrowseRequest.SortToken close
// that gap.

// This is the test the whole task exists for. A cursor taken from a sort by
// email is replayed against a sort by score — same table, same width (both
// resolve to [sort column, rowid]), different column — and must be refused
// rather than silently served from a boundary the new sort never produced.
func TestBrowseRejectsACursorFromADifferentSort(t *testing.T) {
	b := browser(t, browseFixture(t, 10))
	byEmail, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 3,
		Sort: []driver.SortKey{{Column: "email"}},
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(byEmail.Keyset) == 0 || byEmail.SortToken == "" {
		t.Fatal("no keyset/token issued on a keyset-pagable sort")
	}

	_, err = b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 3,
		Sort:      []driver.SortKey{{Column: "score"}},
		After:     byEmail.Keyset,
		SortToken: byEmail.SortToken,
	})
	if err == nil {
		t.Fatal("a cursor taken from a sort by email was accepted by a sort by score")
	}
	if got := dberr.From(err); got.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindInvalid, err)
	}
}

// The other half of the same check: a legitimate continuation, where the
// sort has not changed, must still work. A verification that rejects its
// own driver's cursors is worse than the bug it was built to catch.
func TestBrowseAcceptsTheSameCursorUnderTheSameSort(t *testing.T) {
	b := browser(t, browseFixture(t, 7))
	req := driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 3,
		Sort: []driver.SortKey{{Column: "email"}},
	}
	page1, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	req.After, req.SortToken = page1.Keyset, page1.SortToken

	page2, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("a legitimate continuation under the same sort was rejected: %v", err)
	}
	if len(page2.Rows) == 0 {
		t.Error("a legitimate continuation returned no rows")
	}
}

// The ordinary default-sort path — Sort empty on both requests — must round
// trip too. The token has to be derived from the RESOLVED order (primary
// key plus rowid tiebreaker), not from req.Sort, which is empty here on
// every page and would otherwise collide across every table.
func TestBrowseDefaultSortRoundTripsWithSortToken(t *testing.T) {
	const total = 9
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
		req.After, req.SortToken = page.Keyset, page.SortToken
	}
	if len(seen) != total {
		t.Fatalf("saw %d distinct ids, want %d", len(seen), total)
	}
}

// Ascending and descending on the same column are the same columns, the
// same width, and opposite order — paging one with the other's cursor is
// exactly as wrong as paging with a different column's cursor, so the token
// must differ between them too.
func TestBrowseSortTokenDiffersByDirection(t *testing.T) {
	b := browser(t, browseFixture(t, 5))
	ascReq := driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		Sort: []driver.SortKey{{Column: "email"}},
	}
	asc, err := b.Browse(context.Background(), ascReq)
	if err != nil {
		t.Fatalf("ascending: %v", err)
	}
	descReq := driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		Sort: []driver.SortKey{{Column: "email", Desc: true}},
	}
	desc, err := b.Browse(context.Background(), descReq)
	if err != nil {
		t.Fatalf("descending: %v", err)
	}
	if asc.SortToken == "" || desc.SortToken == "" {
		t.Fatal("no sort token issued")
	}
	if asc.SortToken == desc.SortToken {
		t.Fatal("ascending and descending sorts on the same column produced the same token")
	}

	descReq.After, descReq.SortToken = asc.Keyset, asc.SortToken
	if _, err := b.Browse(context.Background(), descReq); err == nil {
		t.Fatal("an ascending cursor was accepted by a descending sort of the same column")
	} else if got := dberr.From(err); got.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindInvalid, err)
	}
}

// Two tables sorted the same way — same column name, same direction — must
// still produce different tokens: a token that named only the sort's shape,
// and not the table it applies to, would let a cursor from one table page
// another that happens to share a column name.
func TestBrowseSortTokenDiffersByTable(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE t1 (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t1 VALUES (1,'a'),(2,'b'),(3,'c')`,
		`CREATE TABLE t2 (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t2 VALUES (1,'a'),(2,'b'),(3,'c')`,
	)
	p1, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "t1", Limit: 2,
	})
	if err != nil {
		t.Fatalf("t1: %v", err)
	}
	p2, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "t2", Limit: 2,
	})
	if err != nil {
		t.Fatalf("t2: %v", err)
	}
	if p1.SortToken == "" || p2.SortToken == "" {
		t.Fatal("no sort token issued")
	}
	if p1.SortToken == p2.SortToken {
		t.Error("the same sort on two different tables produced the same token")
	}
}

// A stale token with no After to go with it is not a caller mistake: the
// caller may simply be starting over. Only After present with a SortToken
// that then fails to match is refused.
func TestBrowseIgnoresASortTokenWithNoAfter(t *testing.T) {
	b := browser(t, browseFixture(t, 3))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		SortToken: "stale-token-from-a-previous-session",
	})
	if err != nil {
		t.Fatalf("a first page carrying a stale sort token was rejected: %v", err)
	}
}

func TestBrowseRejectsACursorWithUnparseableValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sort  []driver.SortKey
		after []driver.Value
		kind  dberr.Kind
	}{
		{
			name:  "integer",
			after: []driver.Value{{Kind: driver.ValueInt, Text: "not a number"}, {Kind: driver.ValueInt, Text: "1"}},
			kind:  dberr.KindInvalid,
		},
		{
			name:  "float",
			sort:  []driver.SortKey{{Column: "score"}},
			after: []driver.Value{{Kind: driver.ValueFloat, Text: "not a number"}, {Kind: driver.ValueInt, Text: "1"}},
			kind:  dberr.KindInvalid,
		},
		{
			name:  "bytes",
			after: []driver.Value{{Kind: driver.ValueBytes, Text: "3 bytes"}, {Kind: driver.ValueInt, Text: "1"}},
			kind:  dberr.KindUnsupported,
		},
		{
			// The tiebreaker position, so the failure comes from a tied
			// clause rather than the leading one.
			name:  "tiebreaker",
			after: []driver.Value{{Kind: driver.ValueInt, Text: "1"}, {Kind: driver.ValueInt, Text: "oops"}},
			kind:  dberr.KindInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := browser(t, browseFixture(t, 5))
			// Take a REAL token from a real first page under this same sort,
			// so the request that follows is rejected for its malformed
			// values and not merely for arriving without a token. Both
			// failures are KindInvalid, so a hand-written token — or none —
			// would let three of these four cases pass while proving nothing
			// about the cursor parsing they are named for.
			first, err := b.Browse(context.Background(), driver.BrowseRequest{
				Database: "main", Table: "users", Limit: 2, Sort: tc.sort,
			})
			if err != nil {
				t.Fatalf("first page: %v", err)
			}
			if first.SortToken == "" {
				t.Fatal("first page issued no sort token; this test would prove nothing")
			}
			_, err = b.Browse(context.Background(), driver.BrowseRequest{
				Database: "main", Table: "users", Limit: 2,
				Sort: tc.sort, After: tc.after, SortToken: first.SortToken,
			})
			if err == nil {
				t.Fatal("a malformed cursor was accepted")
			}
			if got := dberr.From(err); got.Kind != tc.kind {
				t.Errorf("kind = %q, want %q (err: %v)", got.Kind, tc.kind, err)
			}
		})
	}
}

func TestBrowseWithACanceledContextReportsCanceled(t *testing.T) {
	b := browser(t, browseFixture(t, 3))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.Browse(ctx, driver.BrowseRequest{Database: "main", Table: "users", Limit: 2})
	if err == nil {
		t.Fatal("browse with an already-canceled context succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindCanceled {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindCanceled, err)
	}
}

// -- a scripted driver for the two failures a real database will not produce

// browseScript answers the PRAGMA and the rowid probe Browse issues first,
// then fails the page query. A real SQLite database cannot produce that
// pairing: PRAGMA table_info fails, or returns nothing, for anything the
// following SELECT could not also read, and a healthy connection does not
// drop halfway through a result set. Leaving the two paths untested is how
// an error ends up dropped rather than classified, so they are scripted —
// the same technique, and the same one-off driver naming, as
// coverage_test.go.
type browseScript struct {
	pageErr  error
	pageRows *scriptedRows
}

func (s browseScript) rowsFor(query string) (sqldriver.Rows, error) {
	switch {
	case strings.HasPrefix(query, "PRAGMA"):
		return &scriptedRows{
			cols: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"},
			rows: [][]sqldriver.Value{{int64(0), "id", "INTEGER", int64(1), nil, int64(1)}},
		}, nil
	case strings.Contains(query, "ORDER BY"):
		if s.pageErr != nil {
			return nil, s.pageErr
		}
		return s.pageRows, nil
	}
	// The rowid probe, which only has to not fail.
	return &scriptedRows{cols: []string{"rowid"}}, nil
}

type browseScriptConn struct{ script browseScript }

func (c browseScriptConn) Prepare(string) (sqldriver.Stmt, error) {
	return nil, errors.New("browseScript: Prepare not supported")
}
func (c browseScriptConn) Close() error { return nil }
func (c browseScriptConn) Begin() (sqldriver.Tx, error) {
	return nil, errors.New("browseScript: Begin not supported")
}
func (c browseScriptConn) Query(query string, _ []sqldriver.Value) (sqldriver.Rows, error) {
	return c.script.rowsFor(query)
}

type browseScriptDriver struct{ script browseScript }

func (d browseScriptDriver) Open(string) (sqldriver.Conn, error) {
	return browseScriptConn{script: d.script}, nil
}

func browseScripted(t *testing.T, script browseScript) driver.Browser {
	t.Helper()
	// Unique per registration: sql.Register panics on a repeated name and
	// `go test -count=2` runs this twice in one process.
	name := fmt.Sprintf("browse-script#%d", scriptedDriverSeq.Add(1))
	sql.Register(name, browseScriptDriver{script: script})
	db, err := sql.Open(name, "x")
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &conn{db: db}
}

func TestBrowseReportsAFailingPageQuery(t *testing.T) {
	b := browseScripted(t, browseScript{pageErr: errors.New("simulated page failure")})
	if _, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "t", Limit: 2,
	}); err == nil {
		t.Fatal("a failing page query was reported as a page")
	}
}

func TestBrowseReportsAReadFailurePartWayThroughAPage(t *testing.T) {
	b := browseScripted(t, browseScript{pageRows: &scriptedRows{
		// One visible column, then the two keyset columns and the two
		// typeof() terms that ride along with them.
		cols:     []string{"id", "id", "rowid", "t1", "t2"},
		rows:     [][]sqldriver.Value{{int64(1), int64(1), int64(1), "integer", "integer"}},
		errAfter: errors.New("simulated read failure"),
	}})
	if _, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "t", Limit: 2,
	}); err == nil {
		t.Fatal("a read failure part way through a page was reported as a page")
	}
}

func TestBrowseRejectsANegativeOffset(t *testing.T) {
	b := browser(t, browseFixture(t, 3))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2, Offset: -1,
	})
	if err == nil {
		t.Fatal("a negative offset was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindInvalid, err)
	}
}

// A cursor is the keyset AND the sort it was issued for. Splitting the pair —
// sending After with no token — used to be accepted and paged from wherever
// the values happened to land, which is the silent-corruption path this whole
// mechanism exists to close. There is no legitimate request of this shape:
// every After value came from a page, and every page issues a token with it.
func TestBrowseRefusesAKeysetWithNoSortToken(t *testing.T) {
	b := browser(t, browseFixture(t, 5))
	first, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	_, err = b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2, After: first.Keyset,
	})
	if err == nil {
		t.Fatal("a keyset with no sort token was accepted")
	}
	missing := dberr.From(err)
	if missing.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want invalid", missing.Kind)
	}

	// The Kind is not the assertion. Deleting the empty-token check leaves
	// the hash comparison below it to catch this same request — "" never
	// equals a real token — reporting the identical Kind, so a mutation run
	// removed the check and the whole suite stayed green. What distinguishes
	// the two is what the caller is TOLD: a cursor that arrived without its
	// token is a caller that dropped a field, while a cursor whose token no
	// longer matches is a sort that changed under it. Those are different
	// problems, so they must be different messages, and this is what keeps
	// them so.
	_, err = b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		After: first.Keyset, SortToken: "a-token-from-some-other-sort",
	})
	if err == nil {
		t.Fatal("a keyset with a mismatched sort token was accepted")
	}
	mismatch := dberr.From(err)
	if missing.Message == mismatch.Message {
		t.Errorf("a missing token and a mismatched one report the same message %q; "+
			"the empty-token check is then indistinguishable from its absence", missing.Message)
	}
	if !strings.Contains(missing.Message, "missing") {
		t.Errorf("the missing-token message %q does not say the token is missing", missing.Message)
	}
	// The same keyset WITH its token still works — a check that refused
	// legitimate continuations would be worse than the bug it closes.
	if _, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		After: first.Keyset, SortToken: first.SortToken,
	}); err != nil {
		t.Errorf("the cursor was refused with its own token: %v", err)
	}
}

// Fix wave D-1, at the driver. A TEXT column can hold bytes that are not
// UTF-8 — SQLite stores whatever it is handed — and such a value has no text
// form that survives JSON, so it cannot be a cursor. The driver has to say
// so when it ISSUES the keyset rather than hand out one that pages wrong:
// the mangled value that comes back sorts below the raw bytes it replaced,
// so the predicate re-matches the cursor's own row and the page repeats
// forever while the row before it becomes unreachable.
//
// This is the same refusal the blob check makes one line above it, for the
// same reason, and it must carry its own message: two causes that report the
// same Kind and the same words are one cause as far as any test can tell.
func TestBrowseRefusesAKeysetHoldingTextThatIsNotUTF8(t *testing.T) {
	b := browseOn(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, x TEXT)`,
		`INSERT INTO t (x) VALUES ('a'), (CAST(x'ff' AS TEXT)), (CAST(x'fe' AS TEXT))`)

	// Limit 1 sorted on x: page 0 is 'a', and page 1's last row is the
	// 0xfe value — the row whose keyset cannot be carried.
	req := driver.BrowseRequest{Database: "main", Table: "t", Limit: 1, Sort: []driver.SortKey{{Column: "x"}}}
	page, err := b.Browse(context.Background(), req)
	if err != nil {
		t.Fatalf("page 0: %v", err)
	}
	req.After, req.SortToken = page.Keyset, page.SortToken

	_, err = b.Browse(context.Background(), req)
	if err == nil {
		t.Fatal("a keyset holding bytes that are not UTF-8 was handed out")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindUnsupported {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindUnsupported, err)
	}
	if strings.Contains(got.Message, "binary data") {
		t.Errorf("message = %q, which is the BLOB refusal's words; this is a different cause", got.Message)
	}
}

// -- fix wave D-4: what the cursor is named after ------------------------

// The separator reasoning keyset.Token used to rest on was only accidentally
// sound. Fields were joined with a single NUL byte, and the argument for
// that being unambiguous was that a NUL cannot appear in a SQLite
// identifier — true, and unreachable through Browse, but the reasoning is
// what a MySQL or Postgres copy of this function inherits, and those have
// their own rules about what an identifier may hold.
//
// The collision is real and was executed: under a separator-only encoding,
// table "a" sorted by a column named "b" ascending hashes identically to a
// table literally named "a\0b\0a" with no sort. So the encoding is now
// length-prefixed, which is injective for ANY field content and needs no
// premise about what the fields can hold.
func TestSortTokenCannotBeCollidedByMovingASeparator(t *testing.T) {
	one := keyset.Token("main", "a", []keyset.Term{{Expr: "b"}})
	two := keyset.Token("main", "a\x00b\x00a", nil)
	if one == two {
		t.Errorf("two different (table, sort) pairs hash to the same token %q", one)
	}
	// The same shape one field to the left, so the fix cannot be a special
	// case for the table position.
	three := keyset.Token("main", "x", []keyset.Term{{Expr: "y"}, {Expr: "z"}})
	four := keyset.Token("main", "x", []keyset.Term{{Expr: "y\x00a\x00z"}})
	if three == four {
		t.Errorf("two different order lists hash to the same token %q", three)
	}
}

// The token folds in the database the REQUEST named, not this package's own
// constant. SQLite has exactly one database, so the two are equivalent here
// and no behaviour changes — but a driver copied from this one for an
// engine that has many would hash every schema's tables identically, and
// two same-named tables in different schemas would accept each other's
// cursors. The bug would be introduced by the copy and invisible in the
// original, which is the kind that ships.
func TestSortTokenFoldsInTheDatabaseItWasIssuedFor(t *testing.T) {
	order := []keyset.Term{{Expr: `"id"`}}
	if keyset.Token("main", "t", order) == keyset.Token("reporting", "t", order) {
		t.Error("the same table name in two databases produced the same token")
	}
}

// SQLite resolves an identifier case-insensitively — keyset.ColumnNamed already
// matches that way — so a cursor issued for `users` must keep working when
// the caller spells the table `USERS`. It did not: the token hashed the
// table name verbatim, so the continuation was refused as a cursor from a
// different sort. A false refusal rather than corruption, but the caller
// cannot tell those apart, and half of this driver folding case while the
// other half does not is the kind of inconsistency that gets noticed as a
// bug report rather than a defect.
func TestBrowseContinuesACursorUnderADifferentlyCasedTableName(t *testing.T) {
	b := browser(t, browseFixture(t, 6))
	first, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	second, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "USERS", Limit: 2,
		After: first.Keyset, SortToken: first.SortToken,
	})
	if err != nil {
		t.Fatalf("a cursor for `users` was refused by `USERS`, which SQLite resolves identically: %v", err)
	}
	if got := texts(second.Rows, 0); len(got) != 2 || got[0] != "3" {
		t.Errorf("page 2 = %v, want the two rows after id 2", got)
	}
}

// The same rule one level up: SQLite's schema names are case-insensitive
// too, so "MAIN" names the database this connection has open.
func TestBrowseAcceptsTheDatabaseNameInAnyCase(t *testing.T) {
	b := browser(t, browseFixture(t, 3))
	if _, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "MAIN", Table: "users", Limit: 2,
	}); err != nil {
		t.Errorf("`MAIN` was refused: %v", err)
	}
}

// A request may name no database at all: BrowseRequest.Database is
// optional, and this driver has exactly one database to mean.
func TestBrowseDefaultsToTheOnlyDatabaseWhenNoneIsNamed(t *testing.T) {
	b := browser(t, browseFixture(t, 6))
	first, err := b.Browse(context.Background(), driver.BrowseRequest{
		Table: "users", Limit: 2,
	})
	if err != nil {
		t.Fatalf("a request naming no database was refused: %v", err)
	}
	// And the token it issues has to be the same one "main" would issue,
	// or naming the database on the next page would break the cursor.
	named, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2,
		After: first.Keyset, SortToken: first.SortToken,
	})
	if err != nil {
		t.Fatalf("a cursor issued with no database named was refused by `main`: %v", err)
	}
	if got := texts(named.Rows, 0); len(got) != 2 || got[0] != "3" {
		t.Errorf("page 2 = %v, want the two rows after id 2", got)
	}
}

// Offset on the keyset path used to be accepted and then ignored: a request
// for `users` with Offset 4 returned the FIRST page and said nothing
// (executed). A negative offset was already rejected, so the only
// unhonoured value was a positive one — the one a caller is most likely to
// have meant.
//
// Rejected rather than honoured, deliberately. Honouring it would mean
// LIMIT/OFFSET on top of a keyset predicate, which is answerable but
// answers a question nobody asked: the offset a caller holds came from an
// offset-paged response, and replaying it against a keyset-paged table
// would count from a boundary that response never described. The two
// pagination modes are alternatives, and the driver says which one it used
// — Keyset present or Offset present, never both. Refusing keeps that
// promise symmetrical in both directions.
func TestBrowseRejectsAnOffsetItCannotHonour(t *testing.T) {
	b := browser(t, browseFixture(t, 10))
	_, err := b.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "users", Limit: 2, Offset: 4,
	})
	if err == nil {
		t.Fatal("an offset the keyset path cannot honour was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindInvalid, err)
	}

	// The offset path still honours it, which is the whole reason the field
	// exists: a view has no key, so this is the only way to page it.
	v := browseOn(t,
		`CREATE TABLE source (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`,
		`INSERT INTO source VALUES (1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e')`,
		`CREATE VIEW labels AS SELECT id, label FROM source`)
	page, err := v.Browse(context.Background(), driver.BrowseRequest{
		Database: "main", Table: "labels", Limit: 2, Offset: 3,
	})
	if err != nil {
		t.Fatalf("a view refused an offset: %v", err)
	}
	if got := texts(page.Rows, 0); len(got) != 2 || got[0] != "4" {
		t.Errorf("offset 3 returned %v, want the rows from id 4", got)
	}
}

// The same defect one layer up, where it is visible as a wrong ANSWER rather
// than a wrong lookup: sorting by "ſ" used to return the order for "s",
// because the column resolver folded the two together. Two rows are enough —
// the orders are exact reverses, so a resolver that confuses the columns
// cannot produce the right one by accident.
func TestBrowseSortsByTheColumnNamedNotOneUnicodeFoldingMergesIntoIt(t *testing.T) {
	const longS = "ſ"
	if !strings.EqualFold("s", longS) {
		t.Fatal("the fixture no longer collides under Unicode folding, so this test proves nothing")
	}
	b := browseOn(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, s TEXT, "`+longS+`" TEXT)`,
		`INSERT INTO t VALUES (1, 'b', 'a')`,
		`INSERT INTO t VALUES (2, 'a', 'b')`)

	order := func(column string) []string {
		t.Helper()
		rows := pageAll(t, b, driver.BrowseRequest{
			Database: "main", Table: "t", Sort: []driver.SortKey{{Column: column}}, Limit: 10,
		})
		out := make([]string, len(rows))
		for i, row := range rows {
			out[i] = row[0].Text
		}
		return out
	}

	bySmallS, byLongS := order("s"), order(longS)
	if want := []string{"2", "1"}; !equalStrings(bySmallS, want) {
		t.Errorf("sorted by %q = %v, want %v", "s", bySmallS, want)
	}
	if want := []string{"1", "2"}; !equalStrings(byLongS, want) {
		t.Errorf("sorted by %q = %v, want %v; the sort answered about the column "+
			"Unicode folding merges it with", longS, byLongS, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
