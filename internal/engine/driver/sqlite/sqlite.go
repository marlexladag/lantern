// Package sqlite implements the SQLite driver.
//
// It uses modernc.org/sqlite, which is PURE GO. That is deliberate: it keeps
// CGO_ENABLED=0 cross-compilation working for all six targets. Do not swap in
// a cgo-based SQLite driver.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"

	_ "modernc.org/sqlite"
)

// databaseName is what SQLite's single database is called in the catalog.
const databaseName = "main"

func init() { driver.Register(New()) }

// New returns the SQLite driver.
func New() driver.Driver { return drv{} }

type drv struct{}

func (drv) ID() string { return "sqlite" }

func (drv) Capabilities() driver.Capabilities {
	return driver.Capabilities{
		Transactions:      true,
		MultipleDatabases: false,
		EditableRows:      true,
	}
}

func (drv) Open(ctx context.Context, cfg driver.ConnConfig) (driver.Conn, error) {
	if cfg.File == "" {
		return nil, dberr.New(dberr.KindNotFound, "no database file given")
	}
	// database/sql opens lazily and SQLite would happily create the file, so
	// check first — silently creating an empty database when the user typed a
	// wrong path is worse than an error.
	if _, err := os.Stat(cfg.File); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, dberr.Wrap(dberr.KindNotFound, "database file does not exist", err)
		}
		return nil, dberr.Wrap(dberr.KindUnknown, "cannot read the database file", err)
	}

	// sql.Open cannot fail for this driver: modernc.org/sqlite does not
	// implement database/sql/driver.DriverContext, so the DSN is not parsed
	// until first use, and this package's own blank import above guarantees
	// "sqlite" is always a registered driver name — the only condition under
	// which sql.Open itself ever returns an error. Real failures to open
	// cfg.File surface at first use, caught by PingContext right below.
	db, _ := sql.Open("sqlite", cfg.File)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, classify(err, "")
	}
	return &conn{db: db}, nil
}

type conn struct{ db *sql.DB }

func (c *conn) Ping(ctx context.Context) error {
	if err := c.db.PingContext(ctx); err != nil {
		return classify(err, "")
	}
	return nil
}

func (c *conn) Close() error { return c.db.Close() }

// Quote wraps an identifier in double quotes, doubling any embedded quote.
func (c *conn) Quote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

const introspectSQL = `SELECT name, type FROM sqlite_master
WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'
ORDER BY name`

func (c *conn) Introspect(ctx context.Context) (*schema.Catalog, error) {
	rows, err := c.db.QueryContext(ctx, introspectSQL)
	if err != nil {
		return nil, classify(err, introspectSQL)
	}
	defer rows.Close()

	db := schema.Database{Name: databaseName}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, classify(err, introspectSQL)
		}
		t := schema.Table{Name: name, Kind: schema.TableKindTable}
		if kind == "view" {
			t.Kind = schema.TableKindView
		}
		// Columns stays nil: introspection is lazy (spec section 5).
		db.Tables = append(db.Tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, introspectSQL)
	}
	return &schema.Catalog{Databases: []schema.Database{db}}, nil
}

func (c *conn) Columns(ctx context.Context, database, table string) ([]schema.Column, error) {
	// PRAGMA does not accept a bound parameter for the table name, so the
	// identifier is quoted rather than parameterised.
	stmt := "PRAGMA table_info(" + c.Quote(table) + ")"
	rows, err := c.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, classify(err, stmt)
	}
	defer rows.Close()

	// A non-nil empty slice means "read, and there are none" — distinct from
	// nil, which means "not read yet".
	cols := []schema.Column{}
	for rows.Next() {
		var (
			cid       int
			name      string
			dataType  string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &dfltValue, &pk); err != nil {
			return nil, classify(err, stmt)
		}
		cols = append(cols, schema.Column{
			Name:       name,
			DataType:   dataType,
			Nullable:   notNull == 0,
			PrimaryKey: pk > 0,
			Position:   cid,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, stmt)
	}
	// PRAGMA on a table that does not exist returns zero rows rather than an
	// error, so an empty result here means the table is missing.
	if len(cols) == 0 {
		return nil, dberr.New(dberr.KindNotFound, "no such table: "+table).WithQuery(stmt)
	}
	return cols, nil
}

func (c *conn) Query(ctx context.Context, stmt string, args ...any) (driver.Cursor, error) {
	rows, err := c.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, classify(err, stmt)
	}
	// rows.ColumnTypes cannot fail here: database/sql only returns an error
	// from it when the Rows is already closed or was never populated, and
	// rows was just obtained successfully above with neither having
	// happened yet.
	types, _ := rows.ColumnTypes()
	meta := make([]driver.ColumnMeta, len(types))
	for i, ct := range types {
		meta[i] = driver.ColumnMeta{Name: ct.Name(), DataType: ct.DatabaseTypeName()}
	}
	return &cursor{rows: rows, meta: meta, stmt: stmt}, nil
}

type cursor struct {
	rows *sql.Rows
	meta []driver.ColumnMeta
	stmt string
}

func (c *cursor) Columns() []driver.ColumnMeta { return c.meta }
func (c *cursor) Close() error                 { return c.rows.Close() }

func (c *cursor) Next(ctx context.Context, n int) ([]driver.Row, error) {
	out := make([]driver.Row, 0, n)
	for len(out) < n && c.rows.Next() {
		if err := ctx.Err(); err != nil {
			return out, dberr.From(err)
		}
		cells := make([]any, len(c.meta))
		ptrs := make([]any, len(c.meta))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := c.rows.Scan(ptrs...); err != nil {
			return out, classify(err, c.stmt)
		}
		// Driver byte slices are reused between scans, so copy anything that
		// aliases them into a string before handing it to the caller.
		for i, v := range cells {
			if b, ok := v.([]byte); ok {
				cells[i] = string(b)
			}
		}
		out = append(out, driver.Row(cells))
	}
	if err := c.rows.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, classify(err, c.stmt)
	}
	return out, nil
}

// classify maps a SQLite error onto a Kind. SQLite reports through message
// text rather than typed errors, so this matches on substrings — narrowly, and
// falling through to Unknown rather than guessing.
func classify(err error, stmt string) error {
	if err == nil {
		return nil
	}
	if e := dberr.From(err); e.Kind == dberr.KindCanceled || e.Kind == dberr.KindTimeout {
		if stmt != "" {
			return e.WithQuery(stmt)
		}
		return e
	}

	msg := err.Error()
	kind := dberr.KindUnknown
	switch {
	case strings.Contains(msg, "syntax error"), strings.Contains(msg, "no such column"):
		kind = dberr.KindSyntax
	case strings.Contains(msg, "no such table"):
		kind = dberr.KindNotFound
	case strings.Contains(msg, "UNIQUE constraint failed"),
		strings.Contains(msg, "NOT NULL constraint failed"),
		strings.Contains(msg, "FOREIGN KEY constraint failed"):
		kind = dberr.KindConstraint
	case strings.Contains(msg, "unable to open database file"):
		kind = dberr.KindNotFound
	}

	out := dberr.Wrap(kind, msg, err)
	if stmt != "" {
		out = out.WithQuery(stmt)
	}
	return out
}
