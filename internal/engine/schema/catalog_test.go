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
			Columns: []Column{{Name: "id", DataType: "INTEGER", PrimaryKey: true, Position: 0}},
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
					PrimaryKey bool   `json:"primary_key"`
				} `json:"columns"`
			} `json:"tables"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw.Databases[0].Tables[0].Columns[0].DataType != "INTEGER" {
		t.Errorf("data_type did not round trip: %s", b)
	}
	if !raw.Databases[0].Tables[0].Columns[0].PrimaryKey {
		t.Errorf("primary_key did not round trip: %s", b)
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
