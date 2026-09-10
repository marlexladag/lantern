// Package schema is the engine-neutral catalog every driver maps onto. The UI
// never sees an engine's native introspection shapes.
//
// Introspection is lazy (spec section 5): the table list loads on connect,
// columns only when a table is expanded. A nil Columns slice means "not read
// yet"; an empty non-nil slice means "read, and there are none".
//
// INVARIANT: The "not read yet" and "read but empty" states collapse to the same
// JSON representation when serialized. Go's json:"columns,omitempty" tag drops
// any zero-length slice (nil or non-nil empty alike), so Table{Columns: nil}
// and Table{Columns: []Column{}} both emit no "columns" key. This is safe only
// because no supported SQL engine can produce a zero-column table or view — the
// absence of a "columns" key always means "not read yet". If a later engine
// supports zero-column tables or views (e.g., a document store or key-value
// namespace), omitempty must be removed from Table.Columns, the TypeScript type
// must change from "columns?: Column[]" to "columns: Column[] | null", and the
// UI must distinguish the two states when drawing the tree. Without these changes,
// a lazy-expanding node will re-fetch forever upon expand.
package schema

// TableKind separates real tables from views.
type TableKind string

const (
	TableKindTable TableKind = "table"
	TableKindView  TableKind = "view"
)

// Column is one column of a table or view.
type Column struct {
	Name       string `json:"name"`
	DataType   string `json:"data_type"`
	Nullable   bool   `json:"nullable"`
	PrimaryKey bool   `json:"primary_key"`
	Position   int    `json:"position"`
}

// Table is a table or view. Columns is nil until they are read.
type Table struct {
	Name    string    `json:"name"`
	Kind    TableKind `json:"kind"`
	Columns []Column  `json:"columns,omitempty"`
}

// Loaded reports whether this table's columns have been read.
// It returns true only if Columns is not nil, distinguishing "not read yet"
// (Columns == nil) from "read but empty" (Columns is an empty non-nil slice).
// The distinction exists in memory but collapses in JSON (both states emit no
// "columns" key), which is safe because SQL tables cannot be zero-column.
// See package doc for the full invariant.
func (t *Table) Loaded() bool { return t.Columns != nil }

// Database is one database or schema within a connection.
type Database struct {
	Name   string  `json:"name"`
	Tables []Table `json:"tables"`
}

// Table finds a table by name.
func (d *Database) Table(name string) (*Table, bool) {
	for i := range d.Tables {
		if d.Tables[i].Name == name {
			return &d.Tables[i], true
		}
	}
	return nil, false
}

// Catalog is everything a connection can see.
type Catalog struct {
	Databases []Database `json:"databases"`
}

// Database finds a database by name.
func (c *Catalog) Database(name string) (*Database, bool) {
	for i := range c.Databases {
		if c.Databases[i].Name == name {
			return &c.Databases[i], true
		}
	}
	return nil, false
}
