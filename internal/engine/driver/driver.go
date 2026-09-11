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
	// ReadOnly is a safety mechanism, not decoration (spec section 12):
	// production connections default to it, tinted red, so an accidental
	// UPDATE or DELETE against a live database fails outright instead of
	// merely prompting for confirmation. Enforcing it is the driver's job —
	// Open must reject any statement that would modify the database, by
	// whatever mechanism the engine actually offers, not merely decline to
	// issue writes itself. A driver that cannot enforce this at all must
	// fail Open outright rather than silently accept the flag and allow
	// writes anyway; a flag that looks respected but is not is worse than no
	// flag.
	ReadOnly bool
}

// Conn is a live connection. Every driver implements exactly this.
type Conn interface {
	Ping(ctx context.Context) error
	// Introspect reads the DATABASE list only. It does NOT read table lists:
	// spec section 5 draws laziness in two tiers because a server with forty
	// schemas of two thousand tables makes connecting the slow call, and no
	// amount of column laziness helps once that cost is already paid.
	//
	// Databases arrive with Tables nil, which schema.Table.Loaded() reads as
	// "not read yet" — distinct from a non-nil empty slice, which means "read,
	// and there are none".
	Introspect(ctx context.Context) (*schema.Catalog, error)
	// Tables reads one database's tables, when that database is expanded.
	// It returns a non-nil empty slice for a database with no tables, never
	// nil: nil marshals to the JSON literal null, and the shell declares an
	// array. An unknown database is an error, not an empty result — a driver
	// that ignores this parameter and returns its only database's tables
	// would otherwise pass every test a single-database engine can write.
	Tables(ctx context.Context, database string) ([]schema.Table, error)
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
	// RequiredFields reports which ConnConfig fields must be non-empty for
	// this driver to have any chance of dialing successfully — for example
	// "file" for SQLite, or "host" for a networked driver. It returns the
	// struct's own lowercase field names (the same vocabulary a caller reads
	// them back as), so a caller can name what is missing without knowing
	// which driver it is talking to. The caller is expected to check this at
	// save time, before a connection that can never work is persisted; a
	// driver that has no required fields returns nil.
	//
	// This exists so adding a driver's own requirements (MySQL needs Host;
	// SQLite needs File) is implementing this method once, in that driver's
	// own package — not editing a shared condition in the API layer for
	// every driver that comes along.
	RequiredFields(cfg ConnConfig) []string
	Open(ctx context.Context, cfg ConnConfig) (Conn, error)
}
