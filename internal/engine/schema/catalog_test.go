package schema

import (
	"encoding/json"
	"testing"
)

func TestLookupsFindEntriesAndReportMisses(t *testing.T) {
	c := &Catalog{Databases: []Database{{
		Name:   "main",
		Tables: []Table{{Name: "users", Kind: TableKindTable}},
	}}}

	db, ok := c.Database("main")
	if !ok {
		t.Fatal("Database(main) not found")
	}
	if _, ok := c.Database("nope"); ok {
		t.Error("Database(nope) reported found")
	}

	tbl, ok := db.Table("users")
	if !ok {
		t.Fatal("Table(users) not found")
	}
	if tbl.Kind != TableKindTable {
		t.Errorf("kind = %q, want %q", tbl.Kind, TableKindTable)
	}
	if _, ok := db.Table("nope"); ok {
		t.Error("Table(nope) reported found")
	}
}

// Lazy introspection depends on telling "not read yet" apart from "read, none".
func TestLoadedDistinguishesUnreadFromEmpty(t *testing.T) {
	unread := Table{Name: "users"}
	if unread.Loaded() {
		t.Error("a table with nil Columns reported Loaded")
	}

	readButEmpty := Table{Name: "users", Columns: []Column{}}
	if !readButEmpty.Loaded() {
		t.Error("a table with an empty non-nil Columns reported not Loaded")
	}

	read := Table{Name: "users", Columns: []Column{{Name: "id"}}}
	if !read.Loaded() {
		t.Error("a populated table reported not Loaded")
	}
}

func TestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(Catalog{Databases: []Database{{
		Name: "main",
		Tables: []Table{{
			Name:    "users",
			Kind:    TableKindTable,
			Columns: []Column{{Name: "id", DataType: "INTEGER", Nullable: true, PrimaryKey: true, Position: 3}},
		}},
	}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw struct {
		Databases []struct {
			Name   string `json:"name"`
			Tables []struct {
				Name    string `json:"name"`
				Kind    string `json:"kind"`
				Columns []struct {
					Name       string `json:"name"`
					DataType   string `json:"data_type"`
					Nullable   bool   `json:"nullable"`
					PrimaryKey bool   `json:"primary_key"`
					Position   int    `json:"position"`
				} `json:"columns"`
			} `json:"tables"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Length guards run before any indexing below, so a renamed container
	// tag (databases, tables, or columns) fails here with a message naming
	// the tag, rather than surfacing as an index-out-of-range panic further
	// down the function.
	if len(raw.Databases) == 0 {
		t.Fatal("databases tag: expected at least 1 database, got 0")
	}
	db := raw.Databases[0]
	if len(db.Tables) == 0 {
		t.Fatal("tables tag: expected at least 1 table, got 0")
	}
	tbl := db.Tables[0]
	if len(tbl.Columns) == 0 {
		t.Fatal("columns tag: expected at least 1 column, got 0")
	}

	// Assert all Column tags
	col := tbl.Columns[0]
	if col.Name != "id" {
		t.Errorf("name did not round trip: want id, got %s", col.Name)
	}
	if col.DataType != "INTEGER" {
		t.Errorf("data_type did not round trip: want INTEGER, got %s", col.DataType)
	}
	if !col.Nullable {
		t.Errorf("nullable did not round trip: want true, got false")
	}
	if !col.PrimaryKey {
		t.Errorf("primary_key did not round trip: want true, got false")
	}
	if col.Position != 3 {
		t.Errorf("position did not round trip: want 3, got %d", col.Position)
	}

	// Assert Database and Table tags
	if db.Name != "main" {
		t.Errorf("database name did not round trip: want main, got %s", db.Name)
	}
	if tbl.Name != "users" {
		t.Errorf("table name did not round trip: want users, got %s", tbl.Name)
	}
	if tbl.Kind != "table" {
		t.Errorf("table kind did not round trip: want table, got %s", tbl.Kind)
	}
}

// An unread table must not emit a columns key at all, so the UI can tell it
// apart from one that genuinely has none.
func TestUnreadTableOmitsColumns(t *testing.T) {
	b, err := json.Marshal(Table{Name: "users", Kind: TableKindTable})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["columns"]; ok {
		t.Errorf("columns present for an unread table: %s", b)
	}
}

// Database.Table returns a pointer into the slice, not a copy. Later tasks
// will mutate this pointer to fill in columns. Verify that mutations are
// visible when re-looking-up from the original Catalog.
func TestTableLookupReturnsPointerToSliceEntry(t *testing.T) {
	cat := &Catalog{Databases: []Database{{
		Name:   "main",
		Tables: []Table{{Name: "users", Kind: TableKindTable}},
	}}}

	db, _ := cat.Database("main")
	tbl, _ := db.Table("users")

	// Mutate through the returned pointer.
	tbl.Columns = []Column{{Name: "id", DataType: "INTEGER", Position: 3}}

	// Re-lookup from the original catalog and verify the mutation is visible.
	db2, _ := cat.Database("main")
	tbl2, _ := db2.Table("users")
	if tbl2.Columns == nil {
		t.Error("mutation through returned pointer was not visible in re-lookup")
	}
	if len(tbl2.Columns) != 1 || tbl2.Columns[0].Name != "id" {
		t.Errorf("re-lookup did not see mutated columns: %+v", tbl2.Columns)
	}
}

// Catalog.Database returns a pointer into the slice, not a copy. Verify that
// mutations are visible when re-looking-up from the original Catalog.
func TestDatabaseLookupReturnsPointerToSliceEntry(t *testing.T) {
	cat := &Catalog{Databases: []Database{{
		Name:   "main",
		Tables: []Table{},
	}}}

	db, _ := cat.Database("main")

	// Mutate through the returned pointer.
	db.Tables = []Table{{Name: "users", Kind: TableKindTable}}

	// Re-lookup from the original catalog and verify the mutation is visible.
	db2, _ := cat.Database("main")
	if len(db2.Tables) != 1 || db2.Tables[0].Name != "users" {
		t.Errorf("mutation through returned pointer was not visible in re-lookup: %+v", db2.Tables)
	}
}
