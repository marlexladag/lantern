// Package driver is the abstraction every database engine implements.
//
// Conn is deliberately minimal. Execution, transactions and row editing are
// OPTIONAL interfaces, added alongside the drivers that need them and
// discovered by type assertion — that is what stops a schemaless engine from
// forcing stub methods onto every SQL driver (spec section 4). Do not grow
// Conn; add an optional interface instead.
package driver

import (
	"context"
	"net"

	"github.com/marlexladag/lantern/internal/engine/schema"
)

// Row is one result row, in column order.
type Row []any

// ColumnMeta describes one column of a result set.
type ColumnMeta struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
}

// Capabilities tells the UI what to hide for this engine.
type Capabilities struct {
	Transactions      bool `json:"transactions"`
	MultipleDatabases bool `json:"multiple_databases"`
	EditableRows      bool `json:"editable_rows"`
}

// DialFunc opens the underlying network connection. It exists so SSH
// tunnelling later becomes a dialer swap rather than a redesign; it is nil for
// file-backed engines and for a direct connection.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ConnConfig is everything needed to open one connection. Password is runtime
// only — it is never persisted here (spec section 6).
type ConnConfig struct {
	Driver   string
	Host     string
	Port     int
	User     string
	Password string
	Database string
	// File is the path for file-backed engines such as SQLite.
	File    string
	Options map[string]string
	Dialer  DialFunc
}

// Conn is a live connection. Every driver implements exactly this.
type Conn interface {
	Ping(ctx context.Context) error
	// Introspect reads the database and table lists, but NOT columns —
	// introspection is lazy (spec section 5).
	Introspect(ctx context.Context) (*schema.Catalog, error)
	// Columns reads one table's columns, on expand.
	Columns(ctx context.Context, database, table string) ([]schema.Column, error)
	Query(ctx context.Context, sql string, args ...any) (Cursor, error)
	// Quote escapes an identifier for this engine. Generated SQL must never
	// guess at quoting.
	Quote(ident string) string
	Close() error
}

// Cursor streams a result set.
type Cursor interface {
	Columns() []ColumnMeta
	// Next returns up to n rows. It returns fewer than n, with a nil error,
	// when the result is exhausted.
	Next(ctx context.Context, n int) ([]Row, error)
	Close() error
}

// Driver opens connections for one engine.
type Driver interface {
	ID() string
	Capabilities() Capabilities
	Open(ctx context.Context, cfg ConnConfig) (Conn, error)
}
