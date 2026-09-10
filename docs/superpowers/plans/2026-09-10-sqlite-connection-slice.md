# SQLite Connection Slice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a SQLite connection in the UI, test it, save it, connect, and see its tables in the sidebar — the thinnest slice that is genuinely clickable end to end.

**Architecture:** A `Driver`/`Conn` abstraction in `internal/engine/driver` with SQLite as its first implementation, a normalized schema catalog every future engine maps onto, a connection store that keeps config in a JSON file and secrets in the OS keychain, and RPC methods exposing all of it through the existing JSON-RPC sidecar. The UI reuses the connection dialog and sidebar already specified in `design/`.

**Tech Stack:** Go 1.23 (stdlib + `modernc.org/sqlite` + `zalando/go-keyring`), the existing `internal/rpc` sidecar, React 18 + TypeScript.

**Spec:** `docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md` — sections 4 (driver interface), 5 (schema model), 6 (connection storage and secrets), 11 (error model), 12 (design language).

**Design:** `design/` — `ConnectionDialog.dc.html` and `Main.dc.html` specify the dialog and the sidebar tree. `Foundations.dc.html` and `Keyboard.dc.html` are normative for density, type, colour and shortcuts. Build against those values rather than inventing new ones.

## Global Constraints

- Go 1.23 or later; the module's `go` directive is `go 1.23` — do not raise it.
- **`CGO_ENABLED=0` must keep working for all six targets.** This is why the SQLite driver is `modernc.org/sqlite` (pure Go) and not `mattn/go-sqlite3`. Never add a cgo dependency; `./scripts/build-sidecars_test.sh` will catch it.
- **Exactly two new Go dependencies are permitted by this plan:** `modernc.org/sqlite` and `github.com/zalando/go-keyring`. Anything else needs a ruling.
- **stdout carries the JSON-RPC protocol and nothing else.** Every log line and diagnostic goes to stderr. A single stray write corrupts the stream; a test guards this.
- Transport stays stdio pipes, never a TCP socket.
- **Passwords never touch the config file.** Config is JSON on disk; secrets go to the OS keychain (spec §6).
- **`internal/engine/...` imports nothing from `internal/rpc`, `cmd/`, or any transport.** It is a portable library; that boundary is what lets the engine move shells later (spec §3).
- No `Co-Authored-By` trailer on commits.
- Every task ends with a commit.

---

## File Structure

**Engine core (transport-agnostic):**

| File | Responsibility |
|---|---|
| `internal/engine/dberr/dberr.go` | The normalized error every driver maps onto: `Error{Kind, Message, Native, Query}` |
| `internal/engine/schema/catalog.go` | The engine-neutral catalog: `Catalog`, `Database`, `Table`, `Column` |
| `internal/engine/driver/driver.go` | `Driver`, `Conn`, `Cursor`, `Capabilities`, and the optional interfaces |
| `internal/engine/driver/registry.go` | `Register`/`Lookup` by driver id |
| `internal/engine/driver/sqlite/sqlite.go` | The SQLite driver |
| `internal/engine/store/config.go` | `ConnConfig` and its JSON persistence |
| `internal/engine/store/secret.go` | Keychain get/set/delete, isolated behind an interface for testing |

**Transport layer (RPC-aware):**

| File | Responsibility |
|---|---|
| `internal/api/connections.go` | `connections.list`, `connections.save`, `connections.test`, `connections.delete` |
| `internal/api/session.go` | `session.open`, `session.tables`, `session.close` |
| `cmd/engine/main.go` | Registers the new methods (modify) |

**UI:**

| File | Responsibility |
|---|---|
| `src/lib/connections.ts` | Typed client for the connection and session methods |
| `src/components/ConnectionDialog.tsx` | Add/edit a connection |
| `src/components/Sidebar.tsx` | Connection list and the lazy table tree |
| `src/App.tsx` | Composes sidebar + dialog (modify) |

---

### Task 1: The normalized error type

**Files:**
- Create: `internal/engine/dberr/dberr.go`
- Test: `internal/engine/dberr/dberr_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type Kind string` with constants `KindAuth`, `KindNetwork`, `KindSyntax`, `KindConstraint`, `KindTimeout`, `KindCanceled`, `KindNotFound`, `KindUnsupported`, `KindUnknown` (values are the lowercase names: `"auth"`, `"network"`, …)
  - `type Error struct { Kind Kind; Message string; Native string; Query string }` with JSON tags `kind`, `message`, `native,omitempty`, `query,omitempty`
  - `func (e *Error) Error() string`
  - `func New(kind Kind, message string) *Error`
  - `func Wrap(kind Kind, message string, native error) *Error` — sets `Native` to `native.Error()`, or leaves it empty when `native` is nil
  - `func (e *Error) WithQuery(q string) *Error` — returns a copy carrying the statement
  - `func From(err error) *Error` — returns `err` unchanged if it already is an `*Error` (via `errors.As`), maps `context.Canceled` to `KindCanceled` and `context.DeadlineExceeded` to `KindTimeout`, otherwise wraps as `KindUnknown`

Spec §11 is the authority: the UI branches on `Kind`, shows `Native` on demand, and **`KindCanceled` is not a failure** — pressing Stop must not paint the screen red.

- [ ] **Step 1: Write the failing test**

Create `internal/engine/dberr/dberr_test.go`:

```go
package dberr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestNewCarriesKindAndMessage(t *testing.T) {
	e := New(KindAuth, "access denied")
	if e.Kind != KindAuth {
		t.Errorf("kind = %q, want %q", e.Kind, KindAuth)
	}
	if e.Error() != "access denied" {
		t.Errorf("Error() = %q, want %q", e.Error(), "access denied")
	}
	if e.Native != "" {
		t.Errorf("Native = %q, want empty", e.Native)
	}
}

func TestWrapKeepsTheDriverText(t *testing.T) {
	e := Wrap(KindConstraint, "unique constraint violated", errors.New("UNIQUE constraint failed: users.email"))
	if e.Native != "UNIQUE constraint failed: users.email" {
		t.Errorf("Native = %q", e.Native)
	}
	if e.Message != "unique constraint violated" {
		t.Errorf("Message = %q", e.Message)
	}
}

func TestWrapToleratesANilCause(t *testing.T) {
	e := Wrap(KindUnknown, "something", nil)
	if e.Native != "" {
		t.Errorf("Native = %q, want empty", e.Native)
	}
}

func TestWithQueryDoesNotMutateTheOriginal(t *testing.T) {
	base := New(KindSyntax, "near SELECT")
	withQ := base.WithQuery("SELEC 1")

	if base.Query != "" {
		t.Errorf("original mutated: Query = %q", base.Query)
	}
	if withQ.Query != "SELEC 1" {
		t.Errorf("copy Query = %q", withQ.Query)
	}
	if withQ.Kind != KindSyntax || withQ.Message != "near SELECT" {
		t.Errorf("copy lost fields: %+v", withQ)
	}
}

// Cancellation must be distinguishable from failure — the UI must not paint
// the screen red when the user pressed Stop.
func TestFromMapsCancellationAndDeadline(t *testing.T) {
	if got := From(context.Canceled); got.Kind != KindCanceled {
		t.Errorf("context.Canceled -> %q, want %q", got.Kind, KindCanceled)
	}
	if got := From(context.DeadlineExceeded); got.Kind != KindTimeout {
		t.Errorf("context.DeadlineExceeded -> %q, want %q", got.Kind, KindTimeout)
	}
}

func TestFromPassesAnExistingErrorThrough(t *testing.T) {
	original := New(KindAuth, "access denied")
	if got := From(original); got != original {
		t.Errorf("From returned a different pointer for an *Error")
	}
}

// Drivers will wrap; errors.As must still find the Kind.
func TestFromUnwrapsAWrappedError(t *testing.T) {
	original := New(KindNetwork, "connection refused")
	got := From(fmt.Errorf("dialing: %w", original))
	if got.Kind != KindNetwork {
		t.Errorf("kind = %q, want %q", got.Kind, KindNetwork)
	}
}

func TestFromWrapsAnUnknownError(t *testing.T) {
	got := From(errors.New("boom"))
	if got.Kind != KindUnknown {
		t.Errorf("kind = %q, want %q", got.Kind, KindUnknown)
	}
	if got.Native != "boom" {
		t.Errorf("Native = %q, want boom", got.Native)
	}
}

func TestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(New(KindTimeout, "took too long"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["kind"] != "timeout" {
		t.Errorf("kind = %v, want timeout", raw["kind"])
	}
	if _, ok := raw["message"]; !ok {
		t.Errorf("missing message in %s", b)
	}
	// native and query are omitempty — absent when unset.
	if _, ok := raw["native"]; ok {
		t.Errorf("native should be omitted when empty: %s", b)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/dberr/ -v`
Expected: FAIL — build error, `undefined: New`, `undefined: KindAuth`, etc.

- [ ] **Step 3: Write the implementation**

Create `internal/engine/dberr/dberr.go`:

```go
// Package dberr is the engine's normalized error type. Every driver maps its
// native failures onto a Kind so the UI can react without knowing which
// database produced the error.
//
// Spec section 11 is the authority. The rule that matters most: Canceled is
// not a failure. A user pressing Stop must not see an error state.
package dberr

import (
	"context"
	"errors"
)

// Kind classifies a failure. The UI branches on this.
type Kind string

const (
	KindAuth        Kind = "auth"
	KindNetwork     Kind = "network"
	KindSyntax      Kind = "syntax"
	KindConstraint  Kind = "constraint"
	KindTimeout     Kind = "timeout"
	KindCanceled    Kind = "canceled"
	KindNotFound    Kind = "not_found"
	KindUnsupported Kind = "unsupported"
	KindUnknown     Kind = "unknown"
)

// Error is a driver failure in engine-neutral terms.
type Error struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
	// Native is the driver's own text, shown only on request.
	Native string `json:"native,omitempty"`
	// Query is the statement that failed, when there was one.
	Query string `json:"query,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// New builds an error with no underlying cause.
func New(kind Kind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}

// Wrap builds an error that keeps the driver's own text. A nil cause leaves
// Native empty rather than producing the string "<nil>".
func Wrap(kind Kind, message string, native error) *Error {
	e := &Error{Kind: kind, Message: message}
	if native != nil {
		e.Native = native.Error()
	}
	return e
}

// WithQuery returns a copy carrying the statement that failed. It copies so a
// shared sentinel cannot be mutated by whoever happens to report it.
func (e *Error) WithQuery(q string) *Error {
	c := *e
	c.Query = q
	return &c
}

// From normalizes any error. An *Error passes through unchanged, including one
// wrapped with %w. Context errors map to their own kinds so cancellation and
// timeout never read as unknown failures.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Wrap(KindCanceled, "canceled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(KindTimeout, "timed out", err)
	}
	return Wrap(KindUnknown, err.Error(), err)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/engine/dberr/ -race -v`
Expected: PASS — nine tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/dberr/
git commit -m "feat(engine): add the normalized driver error type"
```

---

### Task 2: The schema catalog model

**Files:**
- Create: `internal/engine/schema/catalog.go`
- Test: `internal/engine/schema/catalog_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type TableKind string` with `TableKindTable = "table"` and `TableKindView = "view"`
  - `type Column struct { Name string; DataType string; Nullable bool; PrimaryKey bool; Position int }` — JSON `name`, `data_type`, `nullable`, `primary_key`, `position`
  - `type Table struct { Name string; Kind TableKind; Columns []Column }` — JSON `name`, `kind`, `columns,omitempty`
  - `type Database struct { Name string; Tables []Table }` — JSON `name`, `tables`
  - `type Catalog struct { Databases []Database }` — JSON `databases`
  - `func (c *Catalog) Database(name string) (*Database, bool)`
  - `func (d *Database) Table(name string) (*Table, bool)`
  - `func (t *Table) Loaded() bool` — reports whether columns have been read yet

**Why `Loaded()` exists:** spec §5 requires lazy introspection — the table list loads on connect, columns only when a table is expanded. `Columns == nil` means "not read yet"; an empty non-nil slice means "read, and there are none". Those are different states and the UI needs to tell them apart.

- [ ] **Step 1: Write the failing test**

Create `internal/engine/schema/catalog_test.go`:

```go
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
	}})
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/schema/ -v`
Expected: FAIL — `undefined: Catalog`.

- [ ] **Step 3: Write the implementation**

Create `internal/engine/schema/catalog.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/engine/schema/ -race -v`
Expected: PASS — four tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/schema/
git commit -m "feat(engine): add the engine-neutral schema catalog"
```

---

### Task 3: Driver interfaces and registry

**Files:**
- Create: `internal/engine/driver/driver.go`
- Create: `internal/engine/driver/registry.go`
- Test: `internal/engine/driver/registry_test.go`

**Interfaces:**
- Consumes: `schema.Catalog`, `schema.Column` (Task 2); `dberr.Error` (Task 1)
- Produces:
  - `type Row []any`
  - `type ColumnMeta struct { Name string; DataType string }` — JSON `name`, `data_type`
  - `type Capabilities struct { Transactions bool; MultipleDatabases bool; EditableRows bool }` — JSON `transactions`, `multiple_databases`, `editable_rows`
  - `type ConnConfig struct { Driver, Host string; Port int; User, Password, Database, File string; Options map[string]string; Dialer DialFunc }`
  - `type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)`
  - `type Conn interface { Ping(context.Context) error; Introspect(context.Context) (*schema.Catalog, error); Columns(ctx context.Context, database, table string) ([]schema.Column, error); Query(ctx context.Context, sql string, args ...any) (Cursor, error); Quote(ident string) string; Close() error }`
  - `type Cursor interface { Columns() []ColumnMeta; Next(ctx context.Context, n int) ([]Row, error); Close() error }`
  - `type Driver interface { ID() string; Capabilities() Capabilities; Open(ctx context.Context, cfg ConnConfig) (Conn, error) }`
  - `func Register(d Driver)`, `func Lookup(id string) (Driver, bool)`, `func IDs() []string` (sorted)

**Two design points to preserve, both from spec §4:**

`Conn` is deliberately minimal. Execution, transactions and row editing are **optional interfaces added alongside the drivers that need them** — not methods on `Conn`. That is what stops Redis and MongoDB from later forcing stub methods onto every SQL driver. Do not add `Exec` or `Begin` to `Conn` in this task; nothing needs them yet.

`Quote` exists because identifier quoting differs per engine (backtick, double-quote, bracket) and generated SQL must never guess.

`ConnConfig.Dialer` is nil in this slice and always will be for SQLite. It exists now so SSH tunnelling later becomes a dialer swap rather than a redesign (spec §1, §6).

- [ ] **Step 1: Write the failing test**

Create `internal/engine/driver/registry_test.go`:

```go
package driver

import (
	"context"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/schema"
)

type stubDriver struct{ id string }

func (s stubDriver) ID() string             { return s.id }
func (s stubDriver) Capabilities() Capabilities { return Capabilities{} }
func (s stubDriver) Open(context.Context, ConnConfig) (Conn, error) {
	return nil, nil
}

func TestRegisterAndLookup(t *testing.T) {
	reset()
	Register(stubDriver{id: "stub"})

	got, ok := Lookup("stub")
	if !ok {
		t.Fatal("Lookup(stub) not found after Register")
	}
	if got.ID() != "stub" {
		t.Errorf("id = %q, want stub", got.ID())
	}
	if _, ok := Lookup("absent"); ok {
		t.Error("Lookup(absent) reported found")
	}
}

func TestIDsAreSorted(t *testing.T) {
	reset()
	Register(stubDriver{id: "sqlite"})
	Register(stubDriver{id: "mysql"})
	Register(stubDriver{id: "postgres"})

	got := IDs()
	want := []string{"mysql", "postgres", "sqlite"}
	if len(got) != len(want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs() = %v, want %v", got, want)
		}
	}
}

// The interfaces must be satisfiable — this fails to compile if a signature
// drifts, which is the point.
func TestStubSatisfiesDriver(t *testing.T) {
	var _ Driver = stubDriver{}
	var _ schema.TableKind = schema.TableKindTable
}
```

Add to `internal/engine/driver/registry.go` a test-only reset helper (it is unexported, so it stays out of the public surface):

```go
// reset clears the registry. Tests use it so registration order cannot leak
// between cases.
func reset() {
	mu.Lock()
	defer mu.Unlock()
	drivers = make(map[string]Driver)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/driver/ -v`
Expected: FAIL — `undefined: Register`, `undefined: Capabilities`.

- [ ] **Step 3: Write the interfaces**

Create `internal/engine/driver/driver.go`:

```go
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
```

- [ ] **Step 4: Write the registry**

Create `internal/engine/driver/registry.go`:

```go
package driver

import "sort"
import "sync"

var (
	mu      sync.RWMutex
	drivers = make(map[string]Driver)
)

// Register makes a driver available by id, replacing any previous
// registration. Drivers call this from an init function.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	drivers[d.ID()] = d
}

// Lookup finds a registered driver.
func Lookup(id string) (Driver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[id]
	return d, ok
}

// IDs lists every registered driver id, sorted, so the UI's driver picker has
// a stable order.
func IDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(drivers))
	for id := range drivers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// reset clears the registry. Tests use it so registration order cannot leak
// between cases.
func reset() {
	mu.Lock()
	defer mu.Unlock()
	drivers = make(map[string]Driver)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/engine/driver/ -race -v`
Expected: PASS — three tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/driver/
git commit -m "feat(engine): add the driver abstraction and registry"
```

---

### Task 4: The SQLite driver

**Files:**
- Create: `internal/engine/driver/sqlite/sqlite.go`
- Test: `internal/engine/driver/sqlite/sqlite_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: everything from Task 3, plus `schema` and `dberr`
- Produces: `sqlite.Driver{}` registered under the id `"sqlite"`, and `func New() driver.Driver`

**Dependency:** `modernc.org/sqlite`, which registers a `database/sql` driver named `"sqlite"`. It is **pure Go** — that is why it was chosen over `mattn/go-sqlite3`, and it is what keeps `CGO_ENABLED=0` cross-compilation working for all six targets. `./scripts/build-sidecars_test.sh` will fail if this ever regresses.

SQLite has exactly one database, conventionally named `main`. Report it that way so the UI's tree has a root.

- [ ] **Step 1: Add the dependency**

```bash
go get modernc.org/sqlite
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Create `internal/engine/driver/sqlite/sqlite_test.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"

	_ "modernc.org/sqlite"
)

// fixture creates a real SQLite file with a known shape and returns its path.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL,
			nickname TEXT
		)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL)`,
		`CREATE VIEW active_users AS SELECT id, email FROM users`,
		`INSERT INTO users (id, email, nickname) VALUES (1, 'a@example.com', NULL)`,
		`INSERT INTO users (id, email, nickname) VALUES (2, 'b@example.com', 'bee')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture stmt %q: %v", s, err)
		}
	}
	return path
}

func open(t *testing.T, path string) driver.Conn {
	t.Helper()
	c, err := New().Open(context.Background(), driver.ConnConfig{Driver: "sqlite", File: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestPingSucceedsOnARealFile(t *testing.T) {
	if err := open(t, fixture(t)).Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestOpenReportsAMissingFileAsNotFound(t *testing.T) {
	_, err := New().Open(context.Background(), driver.ConnConfig{
		Driver: "sqlite",
		File:   filepath.Join(t.TempDir(), "does-not-exist.db"),
	})
	if err == nil {
		t.Fatal("opening a missing file succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindNotFound, err)
	}
}

// Introspect lists tables and views but must NOT read columns — that is lazy.
func TestIntrospectListsTablesAndViewsWithoutColumns(t *testing.T) {
	cat, err := open(t, fixture(t)).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	db, ok := cat.Database("main")
	if !ok {
		t.Fatalf("no database named main in %+v", cat)
	}

	want := map[string]schema.TableKind{
		"users":        schema.TableKindTable,
		"orders":       schema.TableKindTable,
		"active_users": schema.TableKindView,
	}
	if len(db.Tables) != len(want) {
		t.Fatalf("got %d tables, want %d: %+v", len(db.Tables), len(want), db.Tables)
	}
	for _, tbl := range db.Tables {
		if tbl.Kind != want[tbl.Name] {
			t.Errorf("%s kind = %q, want %q", tbl.Name, tbl.Kind, want[tbl.Name])
		}
		if tbl.Loaded() {
			t.Errorf("%s reported Loaded — introspection must not read columns", tbl.Name)
		}
	}
}

// SQLite's own bookkeeping tables must not appear in the tree.
func TestIntrospectHidesInternalTables(t *testing.T) {
	cat, err := open(t, fixture(t)).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	db, _ := cat.Database("main")
	for _, tbl := range db.Tables {
		if len(tbl.Name) >= 7 && tbl.Name[:7] == "sqlite_" {
			t.Errorf("internal table %q leaked into the catalog", tbl.Name)
		}
	}
}

func TestColumnsReadsTypesNullabilityAndKeys(t *testing.T) {
	cols, err := open(t, fixture(t)).Columns(context.Background(), "main", "users")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("got %d columns, want 3: %+v", len(cols), cols)
	}

	byName := map[string]schema.Column{}
	for _, c := range cols {
		byName[c.Name] = c
	}

	id := byName["id"]
	if id.DataType != "INTEGER" || !id.PrimaryKey || id.Position != 0 {
		t.Errorf("id = %+v, want INTEGER primary key at position 0", id)
	}
	if email := byName["email"]; email.Nullable {
		t.Errorf("email reported nullable but is NOT NULL")
	}
	if nick := byName["nickname"]; !nick.Nullable {
		t.Errorf("nickname reported NOT NULL but is nullable")
	}
}

func TestColumnsOnAMissingTableIsNotFound(t *testing.T) {
	_, err := open(t, fixture(t)).Columns(context.Background(), "main", "no_such_table")
	if err == nil {
		t.Fatal("reading a missing table succeeded")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want %q", got.Kind, dberr.KindNotFound)
	}
}

func TestQueryStreamsRowsAndReportsColumns(t *testing.T) {
	cur, err := open(t, fixture(t)).Query(context.Background(), "SELECT id, email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer cur.Close()

	cols := cur.Columns()
	if len(cols) != 2 || cols[0].Name != "id" || cols[1].Name != "email" {
		t.Fatalf("columns = %+v", cols)
	}

	rows, err := cur.Next(context.Background(), 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if got := rows[1][1]; got != "b@example.com" {
		t.Errorf("row 1 email = %v, want b@example.com", got)
	}

	// Exhaustion is an empty slice and a nil error, not an error.
	rest, err := cur.Next(context.Background(), 10)
	if err != nil {
		t.Fatalf("next after exhaustion: %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("got %d rows after exhaustion, want 0", len(rest))
	}
}

func TestQuerySyntaxErrorCarriesKindAndStatement(t *testing.T) {
	stmt := "SELEC 1"
	_, err := open(t, fixture(t)).Query(context.Background(), stmt)
	if err == nil {
		t.Fatal("a malformed statement succeeded")
	}
	got := dberr.From(err)
	if got.Kind != dberr.KindSyntax {
		t.Errorf("kind = %q, want %q (err: %v)", got.Kind, dberr.KindSyntax, err)
	}
	if got.Query != stmt {
		t.Errorf("Query = %q, want %q", got.Query, stmt)
	}
}

func TestQuoteEscapesEmbeddedDoubleQuotes(t *testing.T) {
	c := open(t, fixture(t))
	if got := c.Quote("users"); got != `"users"` {
		t.Errorf("Quote(users) = %s", got)
	}
	if got := c.Quote(`we"ird`); got != `"we""ird"` {
		t.Errorf(`Quote(we"ird) = %s`, got)
	}
}

func TestDriverIsRegisteredAndDeclaresCapabilities(t *testing.T) {
	d, ok := driver.Lookup("sqlite")
	if !ok {
		t.Fatal("sqlite driver not registered")
	}
	caps := d.Capabilities()
	if caps.MultipleDatabases {
		t.Error("SQLite declared MultipleDatabases; it has exactly one")
	}
	if !caps.Transactions {
		t.Error("SQLite declared no transaction support")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/engine/driver/sqlite/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 4: Write the driver**

Create `internal/engine/driver/sqlite/sqlite.go`:

```go
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

	db, err := sql.Open("sqlite", cfg.File)
	if err != nil {
		return nil, dberr.Wrap(dberr.KindUnknown, "cannot open the database", err)
	}
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
	types, err := rows.ColumnTypes()
	if err != nil {
		_ = rows.Close()
		return nil, classify(err, stmt)
	}
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/engine/driver/sqlite/ -race -v`
Expected: PASS — ten tests green.

- [ ] **Step 6: Verify cgo is still off**

Run: `./scripts/build-sidecars_test.sh`
Expected: `sidecar build test PASSED: 6/6 targets`. If this fails, the SQLite dependency pulled in cgo and the driver choice is wrong — stop and report rather than enabling cgo.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/engine/driver/sqlite/
git commit -m "feat(engine): add the SQLite driver"
```

---

### Task 5: Connection store with OS keychain

**Files:**
- Create: `internal/engine/store/config.go`
- Create: `internal/engine/store/secret.go`
- Test: `internal/engine/store/store_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `driver.ConnConfig` (Task 3), `dberr` (Task 1)
- Produces:
  - `type Saved struct { ID, Name, Driver, Host string; Port int; User, Database, File, Color string; ReadOnly bool }` — JSON `id`, `name`, `driver`, `host,omitempty`, `port,omitempty`, `user,omitempty`, `database,omitempty`, `file,omitempty`, `color`, `read_only`. **No password field, ever.**
  - `func (s Saved) ConnConfig(password string) driver.ConnConfig`
  - `type Keyring interface { Get(service, user string) (string, error); Set(service, user, secret string) error; Delete(service, user string) error }`
  - `func OSKeyring() Keyring` — backed by `github.com/zalando/go-keyring`
  - `func NewMemoryKeyring() Keyring` — in-memory, for tests
  - `var ErrSecretNotFound = errors.New("secret not found")` — returned by `Get` when absent
  - `type Store struct{ ... }`, `func New(path string, kr Keyring) *Store`
  - `func DefaultPath() (string, error)` — `<os.UserConfigDir>/lantern/connections.json`
  - `func (s *Store) List() ([]Saved, error)` — empty slice, nil error when the file does not exist
  - `func (s *Store) Save(c Saved, password string) (Saved, error)` — assigns an ID when empty, returns the stored record
  - `func (s *Store) Delete(id string) error`
  - `func (s *Store) Password(id string) (string, error)` — empty string and nil error when none is stored

**Dependency:** `github.com/zalando/go-keyring` — pure Go (it shells out to `security` on macOS, D-Bus on Linux, syscalls on Windows), so CGO stays off.

**Spec §6 is strict here: passwords never touch the config file.** The `Saved` struct has no password field so that it is impossible to write one by accident, not merely discouraged.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/zalando/go-keyring
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Create `internal/engine/store/store_test.go`:

```go
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string, Keyring) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connections.json")
	kr := NewMemoryKeyring()
	return New(path, kr), path, kr
}

func TestListOnAMissingFileIsEmptyNotAnError(t *testing.T) {
	s, _, _ := newStore(t)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List on a missing file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d records, want 0", len(got))
	}
}

func TestSaveAssignsAnIDAndListReturnsIt(t *testing.T) {
	s, _, _ := newStore(t)

	saved, err := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("Save did not assign an ID")
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "local" || list[0].ID != saved.ID {
		t.Fatalf("list = %+v", list)
	}
}

func TestSaveWithAnExistingIDUpdatesInPlace(t *testing.T) {
	s, _, _ := newStore(t)
	first, _ := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")

	first.Name = "renamed"
	if _, err := s.Save(first, ""); err != nil {
		t.Fatalf("update: %v", err)
	}

	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("got %d records after an update, want 1", len(list))
	}
	if list[0].Name != "renamed" {
		t.Errorf("name = %q, want renamed", list[0].Name)
	}
}

// The single most important test in this package.
func TestThePasswordNeverReachesTheConfigFile(t *testing.T) {
	s, path, _ := newStore(t)
	if _, err := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2-super-secret"); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "hunter2-super-secret") {
		t.Fatalf("THE PASSWORD WAS WRITTEN TO DISK:\n%s", raw)
	}

	// And confirm the file is otherwise the record we expect, so this test
	// cannot pass simply because nothing was written at all.
	var records []Saved
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if len(records) != 1 || records[0].User != "app" {
		t.Fatalf("records = %+v", records)
	}
}

func TestPasswordRoundTripsThroughTheKeyring(t *testing.T) {
	s, _, _ := newStore(t)
	saved, _ := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2")

	got, err := s.Password(saved.ID)
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("password = %q, want hunter2", got)
	}
}

func TestPasswordIsEmptyWhenNoneWasStored(t *testing.T) {
	s, _, _ := newStore(t)
	saved, _ := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")

	got, err := s.Password(saved.ID)
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if got != "" {
		t.Errorf("password = %q, want empty", got)
	}
}

func TestDeleteRemovesTheRecordAndItsSecret(t *testing.T) {
	s, _, kr := newStore(t)
	saved, _ := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2")

	if err := s.Delete(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	list, _ := s.List()
	if len(list) != 0 {
		t.Errorf("got %d records after delete, want 0", len(list))
	}
	// A deleted connection must not leave its password behind in the keychain.
	if _, err := kr.Get(keyringService, saved.ID); err == nil {
		t.Error("the secret survived the connection being deleted")
	}
}

func TestDeleteOfAnUnknownIDIsAnError(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Delete("nope"); err == nil {
		t.Error("deleting an unknown id succeeded")
	}
}

func TestConnConfigCarriesThePasswordButSavedDoesNot(t *testing.T) {
	cfg := Saved{Driver: "sqlite", File: "/tmp/a.db"}.ConnConfig("secret")
	if cfg.Password != "secret" {
		t.Errorf("ConnConfig lost the password")
	}
	if cfg.File != "/tmp/a.db" || cfg.Driver != "sqlite" {
		t.Errorf("ConnConfig = %+v", cfg)
	}
}

func TestSavedHasNoPasswordField(t *testing.T) {
	b, err := json.Marshal(Saved{Name: "x", Driver: "sqlite"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"password", "secret", "pass"} {
		if strings.Contains(strings.ToLower(string(b)), forbidden) {
			t.Errorf("Saved serialises a %q-ish field: %s", forbidden, b)
		}
	}
}

func TestMemoryKeyringReportsAMissingSecret(t *testing.T) {
	kr := NewMemoryKeyring()
	if _, err := kr.Get("svc", "nobody"); err == nil {
		t.Error("Get on an absent secret succeeded")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/engine/store/ -v`
Expected: FAIL — `undefined: New`, `undefined: Saved`.

- [ ] **Step 4: Write the secret layer**

Create `internal/engine/store/secret.go`:

```go
package store

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// keyringService is the service name every Lantern secret is filed under.
const keyringService = "com.marlexladag.lantern"

// ErrSecretNotFound reports that no secret is stored for a key.
var ErrSecretNotFound = errors.New("secret not found")

// Keyring is the secret backend. It exists as an interface so tests never
// touch the developer's real keychain.
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

// OSKeyring returns the platform keychain: Keychain on macOS, Credential
// Manager on Windows, Secret Service on Linux.
func OSKeyring() Keyring { return osKeyring{} }

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	v, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return v, err
}

func (osKeyring) Set(service, user, secret string) error {
	return keyring.Set(service, user, secret)
}

func (osKeyring) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrSecretNotFound
	}
	return err
}

// NewMemoryKeyring returns an in-process keyring for tests.
func NewMemoryKeyring() Keyring {
	return &memoryKeyring{secrets: make(map[string]string)}
}

type memoryKeyring struct {
	mu      sync.Mutex
	secrets map[string]string
}

func (m *memoryKeyring) key(service, user string) string { return service + "\x00" + user }

func (m *memoryKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.secrets[m.key(service, user)]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (m *memoryKeyring) Set(service, user, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[m.key(service, user)] = secret
	return nil
}

func (m *memoryKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(service, user)
	if _, ok := m.secrets[k]; !ok {
		return ErrSecretNotFound
	}
	delete(m.secrets, k)
	return nil
}
```

- [ ] **Step 5: Write the config store**

Create `internal/engine/store/config.go`:

```go
// Package store persists connection settings.
//
// Spec section 6 is strict: configuration goes to a JSON file, passwords go to
// the OS keychain, and the two never mix. The Saved struct has no password
// field at all, so writing one to disk is impossible rather than merely
// discouraged.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
)

// Saved is one stored connection. It deliberately has no password field.
type Saved struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Database string `json:"database,omitempty"`
	File     string `json:"file,omitempty"`
	// Color tints the window and tabs. It is a safety feature, not decoration
	// (spec section 12): production is red.
	Color    string `json:"color"`
	ReadOnly bool   `json:"read_only"`
}

// ConnConfig turns a stored record plus a runtime password into a dial-ready
// config. The password is supplied by the caller; it is never held here.
func (s Saved) ConnConfig(password string) driver.ConnConfig {
	return driver.ConnConfig{
		Driver:   s.Driver,
		Host:     s.Host,
		Port:     s.Port,
		User:     s.User,
		Password: password,
		Database: s.Database,
		File:     s.File,
	}
}

// DefaultPath is where connections live on this machine.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", dberr.Wrap(dberr.KindUnknown, "cannot locate the user config directory", err)
	}
	return filepath.Join(dir, "lantern", "connections.json"), nil
}

// Store reads and writes the connection list.
type Store struct {
	mu      sync.Mutex
	path    string
	keyring Keyring
}

// New returns a store backed by path, with secrets in kr.
func New(path string, kr Keyring) *Store {
	return &Store{path: path, keyring: kr}
}

// List returns every stored connection. A missing file is an empty list, not
// an error — a first run is not a failure.
func (s *Store) List() ([]Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) readLocked() ([]Saved, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Saved{}, nil
	}
	if err != nil {
		return nil, dberr.Wrap(dberr.KindUnknown, "cannot read the connection file", err)
	}
	var out []Saved
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, dberr.Wrap(dberr.KindUnknown, "the connection file is not valid JSON", err)
	}
	if out == nil {
		out = []Saved{}
	}
	return out, nil
}

func (s *Store) writeLocked(records []Saved) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return dberr.Wrap(dberr.KindUnknown, "cannot create the config directory", err)
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return dberr.Wrap(dberr.KindUnknown, "cannot encode the connection list", err)
	}
	// Write to a sibling and rename, so an interrupted write cannot leave a
	// half-written connection list behind.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return dberr.Wrap(dberr.KindUnknown, "cannot write the connection file", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return dberr.Wrap(dberr.KindUnknown, "cannot replace the connection file", err)
	}
	return nil
}

// Save stores a connection, assigning an ID when it has none, and files the
// password in the keychain. An empty password stores nothing.
func (s *Store) Save(c Saved, password string) (Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readLocked()
	if err != nil {
		return Saved{}, err
	}

	if c.ID == "" {
		id, err := newID()
		if err != nil {
			return Saved{}, err
		}
		c.ID = id
		records = append(records, c)
	} else {
		found := false
		for i := range records {
			if records[i].ID == c.ID {
				records[i] = c
				found = true
				break
			}
		}
		if !found {
			records = append(records, c)
		}
	}

	if password != "" {
		if err := s.keyring.Set(keyringService, c.ID, password); err != nil {
			return Saved{}, dberr.Wrap(dberr.KindUnknown, "cannot store the password in the keychain", err)
		}
	}
	if err := s.writeLocked(records); err != nil {
		return Saved{}, err
	}
	return c, nil
}

// Delete removes a connection and its stored password.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readLocked()
	if err != nil {
		return err
	}
	out := records[:0]
	found := false
	for _, r := range records {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return dberr.New(dberr.KindNotFound, "no connection with id "+id)
	}
	// A deleted connection must not leave its password in the keychain. A
	// missing secret is fine — not every connection has one.
	if err := s.keyring.Delete(keyringService, id); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return dberr.Wrap(dberr.KindUnknown, "cannot remove the password from the keychain", err)
	}
	return s.writeLocked(out)
}

// Password returns the stored password, or an empty string when there is none.
func (s *Store) Password(id string) (string, error) {
	v, err := s.keyring.Get(keyringService, id)
	if errors.Is(err, ErrSecretNotFound) {
		return "", nil
	}
	if err != nil {
		return "", dberr.Wrap(dberr.KindAuth, "cannot read the password from the keychain", err)
	}
	return v, nil
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", dberr.Wrap(dberr.KindUnknown, "cannot generate an id", err)
	}
	return hex.EncodeToString(b), nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/engine/store/ -race -v`
Expected: PASS — eleven tests green, including `TestThePasswordNeverReachesTheConfigFile`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/engine/store/
git commit -m "feat(engine): store connections on disk and secrets in the keychain"
```

---

### Task 6: RPC methods for connections and sessions

**Files:**
- Create: `internal/api/errors.go`
- Create: `internal/api/connections.go`
- Create: `internal/api/session.go`
- Test: `internal/api/api_test.go`
- Modify: `internal/rpc/message.go` (reserve the code ranges)
- Modify: `cmd/engine/main.go` (register the methods)

**Interfaces:**
- Consumes: `rpc.Handler`, `rpc.Error`, `rpc.Errorf` (existing); `store.Store`, `store.Saved` (Task 5); `driver.Lookup`, `driver.Conn` (Task 3); `schema.Catalog`, `schema.Column` (Task 2); `dberr` (Task 1)
- Produces:
  - `const rpc.CodeDatabase = -32020` and range-reservation comments in `internal/rpc/message.go`
  - `func api.ToRPCError(err error) error` — wraps any error as `*rpc.Error{Code: CodeDatabase, Message: e.Message, Data: <the full dberr.Error>}`
  - `type Sessions struct{...}`, `func NewSessions() *Sessions`, `func (s *Sessions) CloseAll()`
  - `func RegisterConnections(srv *rpc.Server, st *store.Store)` — registers `connections.list`, `connections.save`, `connections.test`, `connections.delete`
  - `func RegisterSession(srv *rpc.Server, st *store.Store, sess *Sessions)` — registers `session.open`, `session.columns`, `session.close`

**This task also settles a known collision risk.** The Rust shell already claimed `-32000..-32005` inside JSON-RPC's implementation-defined *server* range, which is where engine errors belong. Nothing enforced the split. Reserve it explicitly in `internal/rpc/message.go`:

```go
// Reserved code ranges inside JSON-RPC's implementation-defined server range
// (-32000 to -32099):
//
//	-32000 .. -32019  the SHELL's own failures (spawn, IPC, timeout). The Rust
//	                  side owns these; the engine must never emit one.
//	-32020 .. -32099  the ENGINE's application errors. The shell must never
//	                  emit one.
//
// The engine uses a single code, CodeDatabase, and carries the specific Kind
// in the error's data member — the UI branches on Kind, not on the code
// (spec section 11).
const CodeDatabase = -32020
```

- [ ] **Step 1: Reserve the ranges**

Add the block above to `internal/rpc/message.go`, immediately after the existing `Code*` constants.

- [ ] **Step 2: Write the failing test**

Create `internal/api/api_test.go`:

```go
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"

	_ "github.com/marlexladag/lantern/internal/engine/driver/sqlite"
	_ "modernc.org/sqlite"
)

// harness wires a server, a store on a temp path, and an in-memory keyring.
type harness struct {
	srv  *rpc.Server
	st   *store.Store
	sess *Sessions
	db   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fixture.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`); err != nil {
		t.Fatalf("fixture ddl: %v", err)
	}
	_ = db.Close()

	st := store.New(filepath.Join(dir, "connections.json"), store.NewMemoryKeyring())
	sess := NewSessions()
	srv := rpc.NewServer()
	RegisterConnections(srv, st)
	RegisterSession(srv, st, sess)
	t.Cleanup(sess.CloseAll)

	return &harness{srv: srv, st: st, sess: sess, db: dbPath}
}

// call invokes a registered method directly, the way the dispatch loop would.
func (h *harness) call(t *testing.T, method string, params any) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	handler, ok := h.srv.Handler(method)
	if !ok {
		t.Fatalf("method %q is not registered", method)
	}
	result, err := handler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return out, nil
}

func TestSaveThenListRoundTrips(t *testing.T) {
	h := newHarness(t)

	saved, err := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	var savedRec store.Saved
	if err := json.Unmarshal(saved, &savedRec); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if savedRec.ID == "" {
		t.Fatal("save returned no id")
	}

	listed, err := h.call(t, "connections.list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var records []store.Saved
	if err := json.Unmarshal(listed, &records); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(records) != 1 || records[0].Name != "fixture" {
		t.Fatalf("list = %+v", records)
	}
}

func TestTestConnectionSucceedsOnARealFile(t *testing.T) {
	h := newHarness(t)
	out, err := h.call(t, "connections.test", map[string]any{
		"connection": map[string]any{"driver": "sqlite", "file": h.db},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Errorf("ok = false for a real database file")
	}
}

// A failed test is a normal result the dialog renders, not a transport error.
func TestTestConnectionReportsFailureAsAResultNotAnError(t *testing.T) {
	h := newHarness(t)
	out, err := h.call(t, "connections.test", map[string]any{
		"connection": map[string]any{"driver": "sqlite", "file": filepath.Join(t.TempDir(), "nope.db")},
		"password":   "",
	})
	if err != nil {
		t.Fatalf("test returned a transport error: %v", err)
	}
	var res struct {
		OK    bool   `json:"ok"`
		Kind  string `json:"kind"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true for a missing file")
	}
	if res.Kind != string(dberr.KindNotFound) {
		t.Errorf("kind = %q, want %q", res.Kind, dberr.KindNotFound)
	}
	if res.Error == "" {
		t.Error("no error message for the user")
	}
}

func TestOpenReturnsASessionAndTheCatalog(t *testing.T) {
	h := newHarness(t)
	saved, _ := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	var rec store.Saved
	_ = json.Unmarshal(saved, &rec)

	out, err := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
		Catalog   struct {
			Databases []struct {
				Name   string `json:"name"`
				Tables []struct {
					Name    string          `json:"name"`
					Columns json.RawMessage `json:"columns"`
				} `json:"tables"`
			} `json:"databases"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("no session id")
	}
	if len(res.Catalog.Databases) != 1 || len(res.Catalog.Databases[0].Tables) != 1 {
		t.Fatalf("catalog = %+v", res.Catalog)
	}
	if res.Catalog.Databases[0].Tables[0].Name != "users" {
		t.Errorf("table = %q, want users", res.Catalog.Databases[0].Tables[0].Name)
	}
	// Lazy: open must not have read columns.
	if res.Catalog.Databases[0].Tables[0].Columns != nil {
		t.Errorf("open read columns eagerly: %s", res.Catalog.Databases[0].Tables[0].Columns)
	}
}

func TestColumnsReadsOnDemandAndCloseEndsTheSession(t *testing.T) {
	h := newHarness(t)
	saved, _ := h.call(t, "connections.save", map[string]any{
		"connection": map[string]any{"name": "fixture", "driver": "sqlite", "file": h.db, "color": "#3d7d55"},
		"password":   "",
	})
	var rec store.Saved
	_ = json.Unmarshal(saved, &rec)
	opened, _ := h.call(t, "session.open", map[string]any{"connection_id": rec.ID})
	var open struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(opened, &open)

	cols, err := h.call(t, "session.columns", map[string]any{
		"session_id": open.SessionID, "database": "main", "table": "users",
	})
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var list []struct {
		Name       string `json:"name"`
		PrimaryKey bool   `json:"primary_key"`
	}
	if err := json.Unmarshal(cols, &list); err != nil {
		t.Fatalf("decode columns: %v", err)
	}
	if len(list) != 2 || list[0].Name != "id" || !list[0].PrimaryKey {
		t.Fatalf("columns = %+v", list)
	}

	if _, err := h.call(t, "session.close", map[string]any{"session_id": open.SessionID}); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The session is gone, so a further call must fail rather than succeed
	// against a closed connection.
	if _, err := h.call(t, "session.columns", map[string]any{
		"session_id": open.SessionID, "database": "main", "table": "users",
	}); err == nil {
		t.Error("columns succeeded on a closed session")
	}
}

// The Kind must survive the trip into a JSON-RPC error's data member, because
// the UI branches on Kind rather than on the numeric code (spec section 11).
func TestToRPCErrorCarriesTheKindInData(t *testing.T) {
	err := ToRPCError(dberr.New(dberr.KindAuth, "access denied"))

	var re *rpc.Error
	if !asRPCError(err, &re) {
		t.Fatalf("ToRPCError did not produce an *rpc.Error: %T", err)
	}
	if re.Code != rpc.CodeDatabase {
		t.Errorf("code = %d, want %d", re.Code, rpc.CodeDatabase)
	}
	if re.Message != "access denied" {
		t.Errorf("message = %q", re.Message)
	}
	var payload dberr.Error
	if err := json.Unmarshal(re.Data, &payload); err != nil {
		t.Fatalf("data is not a dberr.Error: %v (%s)", err, re.Data)
	}
	if payload.Kind != dberr.KindAuth {
		t.Errorf("kind = %q, want %q", payload.Kind, dberr.KindAuth)
	}
}

// The engine must never emit a code from the shell's reserved range.
func TestDatabaseCodeIsOutsideTheShellRange(t *testing.T) {
	if rpc.CodeDatabase > -32020 {
		t.Errorf("CodeDatabase = %d, which intrudes on the shell's -32000..-32019 range", rpc.CodeDatabase)
	}
	if rpc.CodeDatabase < -32099 {
		t.Errorf("CodeDatabase = %d, which is outside JSON-RPC's implementation-defined range", rpc.CodeDatabase)
	}
}

func asRPCError(err error, target **rpc.Error) bool {
	e, ok := err.(*rpc.Error)
	if ok {
		*target = e
	}
	return ok
}
```

**Note:** this test calls `h.srv.Handler(method)`, which does not exist yet. Add it to `internal/rpc/server.go` as an exported wrapper over the existing unexported lookup:

```go
// Handler returns the handler registered for a method. It exists so callers
// can exercise a method directly without going through the stdio loop.
func (s *Server) Handler(method string) (Handler, bool) { return s.handler(method) }
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/api/ -v`
Expected: FAIL — `undefined: NewSessions`, `undefined: RegisterConnections`.

- [ ] **Step 4: Write the error bridge**

Create `internal/api/errors.go`:

```go
// Package api exposes the engine over JSON-RPC. It is the only package that
// knows about both the engine and the transport; internal/engine/... must
// never import it.
package api

import (
	"encoding/json"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/rpc"
)

// ToRPCError turns any engine error into a JSON-RPC error carrying the full
// normalized error in its data member. The code is always CodeDatabase; the
// UI branches on the Kind inside data, not on the number (spec section 11).
func ToRPCError(err error) error {
	if err == nil {
		return nil
	}
	e := dberr.From(err)
	data, mErr := json.Marshal(e)
	if mErr != nil {
		// Losing the detail is bad, but losing the error entirely is worse.
		return rpc.Errorf(rpc.CodeDatabase, e.Message)
	}
	return &rpc.Error{Code: rpc.CodeDatabase, Message: e.Message, Data: data}
}
```

- [ ] **Step 5: Write the connection methods**

Create `internal/api/connections.go`:

```go
package api

import (
	"context"
	"encoding/json"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

type saveParams struct {
	Connection store.Saved `json:"connection"`
	Password   string      `json:"password"`
}

type idParams struct {
	ID string `json:"id"`
}

// testResult is a normal result, not an error: a failed connection test is
// something the dialog renders inline, not a transport failure.
type testResult struct {
	OK    bool   `json:"ok"`
	Kind  string `json:"kind,omitempty"`
	Error string `json:"error,omitempty"`
}

// RegisterConnections wires the connection CRUD methods onto srv.
func RegisterConnections(srv *rpc.Server, st *store.Store) {
	srv.Register("connections.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		records, err := st.List()
		if err != nil {
			return nil, ToRPCError(err)
		}
		return records, nil
	})

	srv.Register("connections.save", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p saveParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "connections.save: "+err.Error())
		}
		if p.Connection.Name == "" {
			return nil, ToRPCError(dberr.New(dberr.KindUnsupported, "a connection needs a name"))
		}
		saved, err := st.Save(p.Connection, p.Password)
		if err != nil {
			return nil, ToRPCError(err)
		}
		return saved, nil
	})

	srv.Register("connections.delete", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p idParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "connections.delete: "+err.Error())
		}
		if err := st.Delete(p.ID); err != nil {
			return nil, ToRPCError(err)
		}
		return map[string]bool{"deleted": true}, nil
	})

	srv.Register("connections.test", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p saveParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "connections.test: "+err.Error())
		}
		conn, err := dial(ctx, p.Connection, p.Password)
		if err != nil {
			e := dberr.From(err)
			return testResult{OK: false, Kind: string(e.Kind), Error: e.Message}, nil
		}
		defer conn.Close()
		if err := conn.Ping(ctx); err != nil {
			e := dberr.From(err)
			return testResult{OK: false, Kind: string(e.Kind), Error: e.Message}, nil
		}
		return testResult{OK: true}, nil
	})
}

// dial resolves the driver and opens a connection.
func dial(ctx context.Context, rec store.Saved, password string) (driver.Conn, error) {
	d, ok := driver.Lookup(rec.Driver)
	if !ok {
		return nil, dberr.New(dberr.KindUnsupported, "no driver named "+rec.Driver)
	}
	return d.Open(ctx, rec.ConnConfig(password))
}
```

- [ ] **Step 6: Write the session methods**

Create `internal/api/session.go`:

```go
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

// Sessions holds the live connections, keyed by an opaque session id.
type Sessions struct {
	mu    sync.Mutex
	conns map[string]driver.Conn
}

func NewSessions() *Sessions { return &Sessions{conns: make(map[string]driver.Conn)} }

func (s *Sessions) add(c driver.Conn) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", dberr.Wrap(dberr.KindUnknown, "cannot generate a session id", err)
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[id] = c
	return id, nil
}

func (s *Sessions) get(id string) (driver.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if !ok {
		return nil, dberr.New(dberr.KindNotFound, "no open session with id "+id)
	}
	return c, nil
}

func (s *Sessions) remove(id string) (driver.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if ok {
		delete(s.conns, id)
	}
	return c, ok
}

// CloseAll closes every live connection. The entrypoint calls this on shutdown.
func (s *Sessions) CloseAll() {
	s.mu.Lock()
	conns := s.conns
	s.conns = make(map[string]driver.Conn)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

type openParams struct {
	ConnectionID string `json:"connection_id"`
}

type openResult struct {
	SessionID string           `json:"session_id"`
	Catalog   *schema.Catalog  `json:"catalog"`
	Caps      driver.Capabilities `json:"capabilities"`
}

type columnsParams struct {
	SessionID string `json:"session_id"`
	Database  string `json:"database"`
	Table     string `json:"table"`
}

type sessionParams struct {
	SessionID string `json:"session_id"`
}

// RegisterSession wires the session methods onto srv.
func RegisterSession(srv *rpc.Server, st *store.Store, sess *Sessions) {
	srv.Register("session.open", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p openParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.open: "+err.Error())
		}

		records, err := st.List()
		if err != nil {
			return nil, ToRPCError(err)
		}
		var rec store.Saved
		found := false
		for _, r := range records {
			if r.ID == p.ConnectionID {
				rec, found = r, true
				break
			}
		}
		if !found {
			return nil, ToRPCError(dberr.New(dberr.KindNotFound, "no connection with id "+p.ConnectionID))
		}

		password, err := st.Password(rec.ID)
		if err != nil {
			return nil, ToRPCError(err)
		}
		conn, err := dial(ctx, rec, password)
		if err != nil {
			return nil, ToRPCError(err)
		}

		catalog, err := conn.Introspect(ctx)
		if err != nil {
			_ = conn.Close()
			return nil, ToRPCError(err)
		}
		id, err := sess.add(conn)
		if err != nil {
			_ = conn.Close()
			return nil, ToRPCError(err)
		}

		caps := driver.Capabilities{}
		if d, ok := driver.Lookup(rec.Driver); ok {
			caps = d.Capabilities()
		}
		return openResult{SessionID: id, Catalog: catalog, Caps: caps}, nil
	})

	srv.Register("session.columns", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p columnsParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.columns: "+err.Error())
		}
		conn, err := sess.get(p.SessionID)
		if err != nil {
			return nil, ToRPCError(err)
		}
		cols, err := conn.Columns(ctx, p.Database, p.Table)
		if err != nil {
			return nil, ToRPCError(err)
		}
		return cols, nil
	})

	srv.Register("session.close", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p sessionParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "session.close: "+err.Error())
		}
		conn, ok := sess.remove(p.SessionID)
		if !ok {
			return nil, ToRPCError(dberr.New(dberr.KindNotFound, "no open session with id "+p.SessionID))
		}
		if err := conn.Close(); err != nil {
			return nil, ToRPCError(err)
		}
		return map[string]bool{"closed": true}, nil
	})
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/api/ -race -v`
Expected: PASS — eight tests green.

- [ ] **Step 8: Wire the methods into the entrypoint**

In `cmd/engine/main.go`, add the imports and register the methods next to the existing `health` registration. Sessions must be closed on shutdown, so add the cleanup before `Serve` returns:

```go
	path, err := store.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine: %v\n", err)
		os.Exit(1)
	}
	st := store.New(path, store.OSKeyring())
	sess := api.NewSessions()
	defer sess.CloseAll()

	srv := rpc.NewServer()
	srv.Register("health", health.Handler(version, commit))
	api.RegisterConnections(srv, st)
	api.RegisterSession(srv, st, sess)
```

Add the blank import that registers the SQLite driver, next to the other imports:

```go
	_ "github.com/marlexladag/lantern/internal/engine/driver/sqlite"
```

**Note on the deferred cleanup:** the signal path calls `os.Exit(0)`, which skips defers by design (documented in `main.go`). Sessions are closed on the stdin-EOF path only. That is acceptable — the OS reclaims file handles on exit — and closing SQLite files on a signal is not worth reintroducing the shutdown hang the readiness work removed. Say so in a comment.

- [ ] **Step 9: Verify the whole engine still behaves**

Run: `go test ./... -race`
Expected: every package passes.

Run: `printf '{"jsonrpc":"2.0","id":1,"method":"connections.list"}\n' | go run ./cmd/engine 2>/dev/null`
Expected: one line of JSON containing `"result":[]` — an empty list on a machine with no saved connections, not an error.

- [ ] **Step 10: Commit**

```bash
git add internal/api/ internal/rpc/message.go internal/rpc/server.go cmd/engine/main.go
git commit -m "feat(api): expose connections and sessions over JSON-RPC"
```

---

### Task 7: Typed TypeScript client

**Files:**
- Create: `src/lib/connections.ts`
- Test: `src/lib/connections.test.ts`

**Interfaces:**
- Consumes: `request<T>` from `src/lib/engine.ts` (existing — every call funnels through the one `engine_request` command; do not add a second channel)
- Produces:
  - `type DbErrorKind = 'auth' | 'network' | 'syntax' | 'constraint' | 'timeout' | 'canceled' | 'not_found' | 'unsupported' | 'unknown'`
  - `interface DbError { kind: DbErrorKind; message: string; native?: string; query?: string }`
  - `interface Connection { id: string; name: string; driver: string; host?: string; port?: number; user?: string; database?: string; file?: string; color: string; read_only: boolean }`
  - `type NewConnection = Omit<Connection, 'id'> & { id?: string }`
  - `interface Column { name: string; data_type: string; nullable: boolean; primary_key: boolean; position: number }`
  - `interface Table { name: string; kind: 'table' | 'view'; columns?: Column[] }`
  - `interface Catalog { databases: { name: string; tables: Table[] }[] }`
  - `interface TestResult { ok: boolean; kind?: DbErrorKind; error?: string }`
  - `interface OpenResult { session_id: string; catalog: Catalog; capabilities: { transactions: boolean; multiple_databases: boolean; editable_rows: boolean } }`
  - `listConnections()`, `saveConnection(connection, password)`, `testConnection(connection, password)`, `deleteConnection(id)`, `openSession(connectionId)`, `loadColumns(sessionId, database, table)`, `closeSession(sessionId)`
  - `function asDbError(err: unknown): DbError | null` — pulls the engine's normalized error out of a rejected `EngineError`'s `data`

**The important one is `asDbError`.** The engine sends every database failure as a JSON-RPC error whose `data` member is the full normalized error. The UI must branch on `kind` — and `canceled` must never render as a failure (spec §11).

- [ ] **Step 1: Write the failing test**

Create `src/lib/connections.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('./engine', async () => {
  const actual = await vi.importActual<typeof import('./engine')>('./engine');
  return { ...actual, request: vi.fn() };
});

import { request } from './engine';
import {
  listConnections, saveConnection, testConnection, deleteConnection,
  openSession, loadColumns, closeSession, asDbError,
} from './connections';

const requestMock = vi.mocked(request);
beforeEach(() => requestMock.mockReset());

describe('method names and shapes', () => {
  it('lists connections', async () => {
    requestMock.mockResolvedValue([]);
    await listConnections();
    expect(requestMock).toHaveBeenCalledWith('connections.list');
  });

  it('saves a connection with its password alongside, never inside', async () => {
    requestMock.mockResolvedValue({ id: 'a1' });
    const conn = { name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
    await saveConnection(conn, 'hunter2');

    expect(requestMock).toHaveBeenCalledWith('connections.save', { connection: conn, password: 'hunter2' });
    const [, params] = requestMock.mock.calls[0];
    expect(JSON.stringify((params as { connection: unknown }).connection)).not.toContain('hunter2');
  });

  it('tests a connection', async () => {
    requestMock.mockResolvedValue({ ok: true });
    const conn = { name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
    const res = await testConnection(conn, '');
    expect(requestMock).toHaveBeenCalledWith('connections.test', { connection: conn, password: '' });
    expect(res.ok).toBe(true);
  });

  it('deletes by id', async () => {
    requestMock.mockResolvedValue({ deleted: true });
    await deleteConnection('a1');
    expect(requestMock).toHaveBeenCalledWith('connections.delete', { id: 'a1' });
  });

  it('opens a session by connection id', async () => {
    requestMock.mockResolvedValue({ session_id: 's1', catalog: { databases: [] }, capabilities: {} });
    const res = await openSession('a1');
    expect(requestMock).toHaveBeenCalledWith('session.open', { connection_id: 'a1' });
    expect(res.session_id).toBe('s1');
  });

  it('loads columns on demand', async () => {
    requestMock.mockResolvedValue([]);
    await loadColumns('s1', 'main', 'users');
    expect(requestMock).toHaveBeenCalledWith('session.columns', {
      session_id: 's1', database: 'main', table: 'users',
    });
  });

  it('closes a session', async () => {
    requestMock.mockResolvedValue({ closed: true });
    await closeSession('s1');
    expect(requestMock).toHaveBeenCalledWith('session.close', { session_id: 's1' });
  });
});

describe('asDbError', () => {
  it('extracts the normalized error from a rejected engine error', () => {
    const got = asDbError({
      code: -32020,
      message: 'access denied',
      data: { kind: 'auth', message: 'access denied', native: 'ERROR 1045' },
    });
    expect(got?.kind).toBe('auth');
    expect(got?.native).toBe('ERROR 1045');
  });

  it('returns null for something that is not an engine database error', () => {
    expect(asDbError('a plain string')).toBeNull();
    expect(asDbError({ code: -32001, message: 'engine timed out' })).toBeNull();
    expect(asDbError(null)).toBeNull();
  });

  it('returns null when data is present but not shaped like a DbError', () => {
    expect(asDbError({ code: -32020, message: 'x', data: { nope: true } })).toBeNull();
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `npm test`
Expected: FAIL — `Failed to resolve import "./connections"`.

- [ ] **Step 3: Write the client**

Create `src/lib/connections.ts`:

```ts
/**
 * Typed client for the engine's connection and session methods.
 *
 * Everything funnels through `request` in ./engine, which is the single
 * `engine_request` command. Do not add a second channel.
 */
import { request } from './engine';

/** The engine's error classification. The UI branches on this (spec §11). */
export type DbErrorKind =
  | 'auth' | 'network' | 'syntax' | 'constraint'
  | 'timeout' | 'canceled' | 'not_found' | 'unsupported' | 'unknown';

/** A database failure in engine-neutral terms. */
export interface DbError {
  kind: DbErrorKind;
  message: string;
  /** The driver's own text, shown only on request. */
  native?: string;
  /** The statement that failed, when there was one. */
  query?: string;
}

export interface Connection {
  id: string;
  name: string;
  driver: string;
  host?: string;
  port?: number;
  user?: string;
  database?: string;
  file?: string;
  /** Tints the window and tabs. A safety feature, not decoration. */
  color: string;
  read_only: boolean;
}

/** A connection being created has no id yet. */
export type NewConnection = Omit<Connection, 'id'> & { id?: string };

export interface Column {
  name: string;
  data_type: string;
  nullable: boolean;
  primary_key: boolean;
  position: number;
}

export interface Table {
  name: string;
  kind: 'table' | 'view';
  /** Absent until the table is expanded — introspection is lazy. */
  columns?: Column[];
}

export interface Catalog {
  databases: { name: string; tables: Table[] }[];
}

export interface TestResult {
  ok: boolean;
  kind?: DbErrorKind;
  error?: string;
}

export interface OpenResult {
  session_id: string;
  catalog: Catalog;
  capabilities: {
    transactions: boolean;
    multiple_databases: boolean;
    editable_rows: boolean;
  };
}

/** The code the engine uses for every database error. */
const CODE_DATABASE = -32020;

/**
 * Pulls the engine's normalized error out of a rejection. Returns null when
 * the rejection is anything else — a shell failure, a string, a bug — so the
 * caller can tell "the database said no" apart from "the plumbing broke".
 */
export function asDbError(err: unknown): DbError | null {
  if (typeof err !== 'object' || err === null) return null;
  const e = err as { code?: unknown; data?: unknown };
  if (e.code !== CODE_DATABASE) return null;
  const d = e.data as Partial<DbError> | undefined;
  if (!d || typeof d.kind !== 'string' || typeof d.message !== 'string') return null;
  return d as DbError;
}

export const listConnections = () => request<Connection[]>('connections.list');

export const saveConnection = (connection: NewConnection, password: string) =>
  request<Connection>('connections.save', { connection, password });

export const testConnection = (connection: NewConnection, password: string) =>
  request<TestResult>('connections.test', { connection, password });

export const deleteConnection = (id: string) =>
  request<{ deleted: boolean }>('connections.delete', { id });

export const openSession = (connectionId: string) =>
  request<OpenResult>('session.open', { connection_id: connectionId });

export const loadColumns = (sessionId: string, database: string, table: string) =>
  request<Column[]>('session.columns', { session_id: sessionId, database, table });

export const closeSession = (sessionId: string) =>
  request<{ closed: boolean }>('session.close', { session_id: sessionId });
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npm test && npm run typecheck`
Expected: PASS — eleven new tests green, typecheck clean.

- [ ] **Step 5: Commit**

```bash
git add src/lib/connections.ts src/lib/connections.test.ts
git commit -m "feat(ui): add the typed connection and session client"
```

---

### Task 8: The connection dialog and sidebar

**Files:**
- Create: `src/components/ConnectionDialog.tsx`
- Create: `src/components/Sidebar.tsx`
- Create: `src/components/ConnectionDialog.test.tsx`
- Create: `src/components/Sidebar.test.tsx`
- Create: `src/styles/tokens.css`
- Modify: `src/App.tsx`

**Interfaces:**
- Consumes: everything from Task 7
- Produces: `<ConnectionDialog open onClose onSaved />`, `<Sidebar />`

**Build against the design, not from scratch.** `design/Foundations.dc.html` is normative and `design/ConnectionDialog.dc.html` and `design/Main.dc.html` show the target. The values that matter here:

| Token | Value |
|---|---|
| Sidebar width | 244 px |
| Sidebar row height | 24 px |
| Dialog form control height | 28 px |
| Toolbar control height | 23 px |
| Body / data type | 12 px, IBM Plex Sans; values in IBM Plex Mono |
| Label | 11 px |
| Dividers | 1 px hairline, no shadows between panes |
| Radii | controls 4–5 px, panels 7–9 px |
| Light: bg / surface / line / text / dim | `#f2f1ee` / `#fbfaf8` / `#dedbd5` / `#23211e` / `#6f6a63` |
| Accent / danger / ok | `#24707a` / `#9e4436` / `#3d7d55` |

**Two behaviours the design makes load-bearing:**
- **Connection colour is a safety feature.** The swatch row is not decoration; red marks production. Carry the chosen colour through to the saved record.
- **Lazy expansion.** A table's columns load when it is expanded, not on connect. Show a distinct state while loading.

- [ ] **Step 1: Write the token stylesheet**

Create `src/styles/tokens.css` with the values from the table above as CSS custom properties on `:root`, and the dark equivalents under both `@media (prefers-color-scheme: dark)` guarded as `:root:not([data-theme="light"])` and `:root[data-theme="dark"]`. Take the dark values from `design/Foundations.dc.html`'s dark swatch row: bg `#1b1a18`, surface `#212020`, line `#35322e`, text `#e9e6e0`, dim `#9b948b`, accent `#57aab2`, danger `#c9705d`, ok `#6aab80`.

Import it once from `src/App.tsx`.

- [ ] **Step 2: Write the failing dialog test**

Create `src/components/ConnectionDialog.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act, fireEvent } from '@testing-library/react';

vi.mock('../lib/connections', () => ({
  testConnection: vi.fn(),
  saveConnection: vi.fn(),
}));

import { testConnection, saveConnection } from '../lib/connections';
import { ConnectionDialog } from './ConnectionDialog';

const testMock = vi.mocked(testConnection);
const saveMock = vi.mocked(saveConnection);

beforeEach(() => {
  testMock.mockReset();
  saveMock.mockReset();
});

// Use fireEvent.change, NOT `input.value = x`. React overrides the value
// setter on controlled inputs, so a direct assignment never fires onChange and
// the test would silently exercise an empty form while appearing to pass.
function fill(name: string, file: string) {
  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: name } });
  fireEvent.change(screen.getByLabelText(/file/i), { target: { value: file } });
}

it('renders nothing when closed', () => {
  const { container } = render(<ConnectionDialog open={false} onClose={() => {}} onSaved={() => {}} />);
  expect(container.firstChild).toBeNull();
});

it('reports a successful test inline', async () => {
  testMock.mockResolvedValue({ ok: true });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  await waitFor(() => expect(screen.getByText(/reachable/i)).toBeDefined());
});

// A failed test is information, not a crash.
it('reports a failed test with the engine message', async () => {
  testMock.mockResolvedValue({ ok: false, kind: 'not_found', error: 'database file does not exist' });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/nope.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  await waitFor(() => expect(screen.getByText(/database file does not exist/i)).toBeDefined());
});

it('saves and reports the stored record to its caller', async () => {
  const stored = {
    id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db',
    color: '#3d7d55', read_only: false,
  };
  saveMock.mockResolvedValue(stored);
  const onSaved = vi.fn();
  render(<ConnectionDialog open onClose={() => {}} onSaved={onSaved} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(onSaved).toHaveBeenCalledWith(stored));
  const [connection, password] = saveMock.mock.calls[0];
  expect(connection.name).toBe('local');
  expect(connection.driver).toBe('sqlite');
  expect(password).toBe('');
});

it('refuses to save without a name', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  expect(saveMock).not.toHaveBeenCalled();
  // Assert on the validation message specifically. A bare /name/i would also
  // match the field's own label and pass whether or not validation ran.
  expect(screen.getByRole('alert').textContent).toMatch(/name is required/i);
});
```

- [ ] **Step 3: Run it to verify it fails**

Run: `npm test`
Expected: FAIL — `Failed to resolve import "./ConnectionDialog"`.

- [ ] **Step 4: Write the dialog**

Create `src/components/ConnectionDialog.tsx`. Requirements, all testable from the test above:

- Renders `null` when `open` is false.
- A driver segmented control with `SQLite` selected; MySQL and MariaDB rendered but disabled, since no driver exists for them yet. Disabled beats hidden — it tells the user what is coming.
- Fields: Name (required), File (a path, since SQLite is file-backed). Each `<input>` is associated with its `<label>` via `htmlFor`/`id` so the tests can find them by label and so the dialog is keyboard-navigable, which the design makes non-negotiable.
- A colour swatch row of the six Foundations colours; the selected one gets a ring. Default `#3d7d55` (the green used for `local`).
- A "Production connection" block that, when toggled, switches the selected colour to `#9e4436`, sets `read_only`, and shows the danger-tinted panel from `design/ConnectionDialog.dc.html`.
- **Test Connection** calls `testConnection` and renders the outcome inline: `Reachable` in the ok colour, or the returned `error` text in the danger colour. Never throw for a failed test.
- **Connect** validates that Name is non-empty. When it is empty, render the message `Name is required` in an element with `role="alert"` and do not call `saveConnection`, then calls `saveConnection(connection, '')` — SQLite has no password — and passes the stored record to `onSaved`.
- Escape calls `onClose`. Cmd/Ctrl+Enter triggers Connect.

- [ ] **Step 5: Write the failing sidebar test**

Create `src/components/Sidebar.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';

vi.mock('../lib/connections', () => ({
  listConnections: vi.fn(),
  openSession: vi.fn(),
  loadColumns: vi.fn(),
  closeSession: vi.fn(),
}));

import { listConnections, openSession, loadColumns } from '../lib/connections';
import { Sidebar } from './Sidebar';

const listMock = vi.mocked(listConnections);
const openMock = vi.mocked(openSession);
const columnsMock = vi.mocked(loadColumns);

const conn = {
  id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db',
  color: '#3d7d55', read_only: false,
};

beforeEach(() => {
  listMock.mockReset();
  openMock.mockReset();
  columnsMock.mockReset();
});

it('shows an empty state when there are no connections', async () => {
  listMock.mockResolvedValue([]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText(/no connections/i)).toBeDefined());
});

it('lists saved connections', async () => {
  listMock.mockResolvedValue([conn]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
});

it('opens a session and shows the tables when a connection is clicked', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    session_id: 's1',
    catalog: { databases: [{ name: 'main', tables: [{ name: 'users', kind: 'table' }] }] },
    capabilities: { transactions: true, multiple_databases: false, editable_rows: true },
  });

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  expect(openMock).toHaveBeenCalledWith('a1');
});

// Columns must load on expand, not on connect.
it('loads a table’s columns only when it is expanded', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    session_id: 's1',
    catalog: { databases: [{ name: 'main', tables: [{ name: 'users', kind: 'table' }] }] },
    capabilities: { transactions: true, multiple_databases: false, editable_rows: true },
  });
  columnsMock.mockResolvedValue([
    { name: 'id', data_type: 'INTEGER', nullable: false, primary_key: true, position: 0 },
  ]);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  expect(columnsMock).not.toHaveBeenCalled();

  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(screen.getByText('id')).toBeDefined());
  expect(columnsMock).toHaveBeenCalledWith('s1', 'main', 'users');
});

it('shows the engine message when opening fails', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockRejectedValue({
    code: -32020,
    message: 'database file does not exist',
    data: { kind: 'not_found', message: 'database file does not exist' },
  });

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  await waitFor(() => expect(screen.getByText(/database file does not exist/i)).toBeDefined());
});
```

- [ ] **Step 6: Write the sidebar**

Create `src/components/Sidebar.tsx`. Requirements:

- Loads `listConnections()` on mount. Empty renders "No connections yet" plus an add affordance.
- Each connection row: a 7 px colour dot in its stored colour, the name at 12 px, the driver id at 11 px in the dim colour, 24 px row height. A lock glyph when `read_only`.
- Clicking a connection calls `openSession(id)` and renders the returned catalog as a tree: database → tables. Uses the returned `catalog` and never re-fetches it.
- Clicking a table calls `loadColumns(sessionId, database, table)` and renders the columns beneath it, indented. **Only on expand**, and only once — cache the result on the table node.
- Failures render inline in the danger colour, using `asDbError(err)?.message` and falling back to `String(err)`. A `canceled` kind renders nothing (spec §11).
- Keyboard: arrow keys move the selection, Enter expands or collapses.

- [ ] **Step 7: Compose them in the app**

Rewrite `src/App.tsx` to import `../styles/tokens.css`, render `<Sidebar />` beside the existing `<EngineStatus />` in a 244 px + fluid layout, and mount `<ConnectionDialog />` behind an "Add connection" button. Keep `EngineStatus` — it is how you can still see the engine is alive.

- [ ] **Step 8: Verify everything**

Run: `npm test && npm run typecheck`
Expected: PASS — all suites green, including the ten new component tests.

- [ ] **Step 9: End-to-end check against a real database**

```bash
sqlite3 /tmp/lantern-demo.db "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL);
INSERT INTO users VALUES (1,'a@example.com'),(2,'b@example.com');
CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL);"
./scripts/build-sidecars.sh
npm run tauri dev
```

In the running app: add a connection named `demo` pointing at `/tmp/lantern-demo.db`, press Test Connection and confirm it reports reachable, press Connect, confirm `users` and `orders` appear in the sidebar, expand `users` and confirm `id` and `email` appear with `id` marked as the primary key.

Report what you observed. If you cannot see the window, verify as far as you can mechanically — process inspection and the engine's stderr — and say exactly what you could not observe.

- [ ] **Step 10: Commit**

```bash
git add src/
git commit -m "feat(ui): add the connection dialog and the schema sidebar"
```

---

## Definition of Done

- [ ] `go test ./... -race` passes, including the new `dberr`, `schema`, `driver`, `sqlite`, `store` and `api` packages.
- [ ] `npm test` and `npm run typecheck` pass.
- [ ] `./scripts/build-sidecars_test.sh` reports 6/6 — proving neither new dependency pulled in cgo.
- [ ] `./scripts/ci-workflow_test.sh` passes.
- [ ] `cargo fmt --check`, `cargo clippy -- -D warnings` and `cargo test` pass (the Rust shell is untouched by this plan, but CI gates it).
- [ ] A SQLite connection can be added, tested, saved, opened, and its tables listed and expanded in the running app.
- [ ] `TestThePasswordNeverReachesTheConfigFile` passes — the single most important test in this plan.
- [ ] Grep confirms no password, secret or credential string is written to the connection file by any code path.

## Deferred

MySQL and MariaDB drivers, the result grid, query execution, SSH tunnelling, and the staged-edit change set. The `Driver`/`Conn` abstraction and the lazy catalog are built here specifically so those arrive without redesign.

`Execer`, `Transactor` and `RowEditor` are named in spec §4 as optional interfaces but are deliberately **not** defined in this plan — nothing implements or consumes them yet, and an interface with no implementer is a guess. They arrive with the first driver that needs them.
