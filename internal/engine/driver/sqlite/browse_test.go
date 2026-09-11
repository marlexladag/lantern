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
