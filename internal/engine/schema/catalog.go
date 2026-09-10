// Package schema is the engine-neutral catalog every driver maps onto. The UI
// never sees an engine's native introspection shapes.
//
// Introspection is lazy (spec section 5): the table list loads on connect,
// columns only when a table is expanded. A nil Columns slice means "not read
// yet"; an empty non-nil slice means "read, and there are none".
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
