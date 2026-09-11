# Lantern — Database Client Design

Date: 2026-09-08
Status: Approved for planning

## 1. Overview

A cross-platform desktop database client in the spirit of TablePlus: fast,
small, native-feeling, with a spreadsheet-grade result grid and a staged-edit
workflow that never writes to the database until the user commits.

### Goals

- macOS, Windows, and Linux from one codebase.
- Smooth scrolling and editing on result sets of 100k+ rows.
- MySQL, MariaDB, and SQLite in v1; an engine architecture that absorbs
  Postgres, SQL Server, Oracle, MongoDB, Redis, ClickHouse, DuckDB,
  Snowflake, and BigQuery without redesign.
- Installer under ~30 MB.

### Non-goals for v1

ER diagrams, SSH tunnels, migrations, stored procedure and trigger editing,
plugins, themes, query history search, import/export beyond CSV.

SSH tunnelling is the one omission that materially hurts real-world use, so
the connection layer accepts an injectable dialer from day one (Section 6).
A tunnel later becomes a dialer swap, not a redesign.

## 2. Stack

| Layer | Choice |
|---|---|
| Engine | Go 1.24+, standalone process |
| Shell | Tauri v2 |
| UI | React 18 + TypeScript + Vite |
| Result grid | Glide Data Grid (canvas-rendered) |
| SQL editor | CodeMirror 6 + `@codemirror/lang-sql` |
| UI state | Zustand |
| MySQL/MariaDB driver | `go-sql-driver/mysql` |
| SQLite driver | `modernc.org/sqlite` (pure Go, no cgo) |
| Secrets | `zalando/go-keyring` |

### Why Go for the engine

The deciding factor is the *future* driver list, not the v1 list. SQL Server
and Oracle are where cross-platform database tools go to die, and Go is the
only mainstream candidate with a Microsoft-official SQL Server driver
(`microsoft/go-mssqldb`) *and* a pure-Go Oracle driver (`go-ora`) that
requires no Oracle Instant Client shipped alongside the app. That removes an
entire class of distribution pain from the roadmap. Go also cross-compiles to
static binaries for all six targets with no ceremony, and `context`
cancellation maps directly onto "user hit Stop on a runaway query".

`modernc.org/sqlite` is chosen over `mattn/go-sqlite3` specifically so that
v1 has **no cgo at all**, keeping cross-compilation trivial. It is measurably
slower; that trade is worth revisiting once SQLite performance is proven to
matter.

### Why Tauri with a Go sidecar rather than Wails

Wails would put the engine in-process and avoid all IPC work. It was rejected
for two reasons:

1. **Crash isolation.** Oracle and several future drivers require cgo. A
   segfault in a C client library, or a driver bug on a malformed result, kills
   the entire application if the engine is in-process. As a separate process,
   the engine dies, the UI observes the closed pipe, and it offers reconnect
   with editor buffers intact. For a tool kept open all day against production
   databases this is worth the plumbing.
2. **Shell maturity.** Tauri v2 is considerably more battle-tested than Wails v3.

The Rust surface is small: declare the sidecar in `tauri.conf.json`, spawn it,
forward messages. Configuration, not systems programming.

## 3. Architecture

```
+--------------------------------------------------+
|  Tauri shell (Rust, ~50 lines)                    |
|   - window/menu/tray, sidecar lifecycle           |
|  +--------------------------------------------+  |
|  |  React + TypeScript                         |  |
|  |   sidebar / tabs / Glide grid / CodeMirror  |  |
|  +--------------------------------------------+  |
+--------------------------------------------------+
                     |  JSON-RPC 2.0 over stdio
+--------------------------------------------------+
|  Go sidecar process                               |
|  +--------------------------------------------+  |
|  |  rpc/     method dispatch, cursor registry  |  |
|  +--------------------------------------------+  |
|  |  engine/  (imports nothing above this line) |  |
|  |    driver/  Driver + Conn interfaces        |  |
|  |    schema/  normalized catalog model        |  |
|  |    query/   execution, cancel, streaming    |  |
|  |    edit/    ChangeSet -> SQL                |  |
|  |    store/   connection config + keyring     |  |
|  +--------------------------------------------+  |
+--------------------------------------------------+
```

The load-bearing rule: **`engine/` imports nothing from `rpc/`, Tauri, or any
transport.** It is a plain Go library, testable without a UI and portable to a
different shell if Tauri ever becomes the wrong answer.

## 4. Driver interface

The interface must survive engines as different as MySQL and Redis. The
required surface is therefore minimal, with everything else expressed as
optional interfaces discovered by type assertion — idiomatic Go, and it avoids
a god-interface full of methods that half the drivers must stub out.

```go
type Driver interface {
    ID() string
    Capabilities() Capabilities
    // RequiredFields reports which ConnConfig fields must be non-empty for
    // this driver to have any chance of dialing. Checked at SAVE time, so a
    // connection that can never work is never persisted. Adding a driver's
    // requirements is implementing this method in that driver's package, not
    // editing a shared condition in the API layer.
    RequiredFields(cfg ConnConfig) []string
    Open(ctx context.Context, cfg ConnConfig) (Conn, error)
}

// Required of every driver.
type Conn interface {
    Ping(ctx context.Context) error
    // Introspect reads the DATABASE list only — the connect-time call.
    // Section 5 is the authority on why laziness is two tiers.
    Introspect(ctx context.Context) (*schema.Catalog, error)
    // Tables reads one database's tables, when that database is expanded.
    Tables(ctx context.Context, database string) ([]schema.Table, error)
    // Columns reads one table's columns, when that table is expanded.
    Columns(ctx context.Context, database, table string) ([]schema.Column, error)
    Query(ctx context.Context, sql string, args ...any) (Cursor, error)
    Quote(ident string) string
    Close() error
}

// ConnConfig is everything needed to open one connection. Password is
// runtime-only and never persisted here (Section 6).
type ConnConfig struct {
    Driver   string
    Host     string
    Port     int
    User     string
    Password string
    Database string
    File     string            // file-backed engines (SQLite)
    Options  map[string]string // TLS and per-engine settings
    ReadOnly bool              // see below
    Dialer   DialFunc          // nil for a direct connection; SSH tunnelling later
}

// Optional capabilities.
type Execer     interface { Exec(ctx context.Context, sql string, args ...any) (Result, error) }
type Transactor interface { Begin(ctx context.Context) (Tx, error) }
type RowEditor  interface { ApplyChanges(ctx context.Context, cs *edit.ChangeSet) (*edit.Report, error) }

type Cursor interface {
    Columns() []ColumnMeta
    // Next returns up to n rows. It returns FEWER than n with a nil error
    // when the result is exhausted — not io.EOF. A driver that has an EOF
    // sentinel of its own swallows it.
    Next(ctx context.Context, n int) ([]Row, error)
    Close() error
}
```

`Capabilities` reports what the UI should hide: transactions, DDL, schemas vs
databases, editable results, explain plans. Redis will report almost nothing
and the UI will adapt rather than crash.

*As built today `Introspect` returns databases AND tables in one call, and
`Tables` does not exist. SQLite has a single hardcoded `main`, so the two tiers
are indistinguishable there. The split lands with the MySQL driver, which is
the first engine that cannot hide it — see Section 5.*

`Quote` exists because identifier quoting differs per engine (backtick,
double-quote, bracket) and generated SQL in `edit/` must never guess.

`RowEditor` is an override, not the default path. Commits normally run through
the generic route: `edit/` generates SQL (Section 8) and `query/` executes it
via `Transactor` and `Execer`. A driver implements `RowEditor` only when that
route cannot express its writes — MongoDB and Redis being the motivating cases.
SQL drivers should not implement it.

`Introspect` is required even of schemaless engines. A driver with no catalog
to report returns an empty one and declares the absence through
`Capabilities`; the UI reads capabilities, never an empty result, to decide
whether to render a schema tree.

`ReadOnly` is enforced **by the driver**, not by the UI. A read-only flag the
shell merely draws as a lock icon is a safety mechanism the user believes in
and the system does not implement, which is worse than no mechanism at all. A
driver that cannot enforce read-only at the connection level must fail `Open`
rather than accept the flag and ignore it. SQLite enforces it with `PRAGMA
query_only`, which SQLite checks inside its own opcode dispatch. (Note for
implementers: modernc.org/sqlite hardcodes `SQLITE_OPEN_READWRITE` at every
open and silently ignores `mode=ro` in the DSN — its own test suite says so.)

What `ReadOnly` is, precisely: a guard against an accidental write. The
realistic failure it exists for is a user running an `UPDATE` or a `DELETE`
against a connection they marked production. It is **not** a security
boundary against someone who can author arbitrary SQL, and nothing in the UI
may describe it as one. `PRAGMA query_only` is enforced against statements,
and `PRAGMA query_only=0` is a statement — so a connection that will run
arbitrary SQL can be told to stop being read-only, in the same call as the
write it is clearing the way for. The SQLite driver therefore refuses, at its
`Conn.Query` chokepoint on a read-only connection, every `PRAGMA` and any
input carrying more than one statement. That is a tokenizer-level guard, and
a tokenizer-level guard is only ever as good as the evasions its author
thought of. The complete mechanism would be an authorizer callback
(`sqlite3_set_authorizer`), which decides per operation inside SQLite rather
than per statement outside it; modernc.org/sqlite exports none — the symbol
is compiled into the vendored amalgamation, but the raw connection handle it
needs is never exposed. A driver that does offer an authorizer should enforce
`ReadOnly` with it and delete its tokenizer.

A write refused by a read-only connection reports `KindReadOnly`, never
`KindConstraint`. They demand opposite things of the user — fix the row versus
reconnect read-write — and the UI cannot offer either if they share a Kind.

## 5. Schema model

One normalized catalog shared by every engine:

```
Catalog -> Database/Schema -> Table | View -> Column, Index, ForeignKey
```

Each driver maps its native introspection into this shape (MySQL reads
`information_schema`, SQLite reads `pragma`). The UI never sees engine-specific
structures.

**Introspection is lazy, in two tiers.** On connect, load only the DATABASE
list. Load a database's table list when that database is expanded. Load
columns, indexes and foreign keys when a table is expanded.

```go
Introspect(ctx)                 // databases only — the connect-time call
Tables(ctx, database)           // one database's tables, on expand
Columns(ctx, database, table)   // one table's columns, on expand
```

One tier is not enough. `information_schema` is slow on servers with thousands
of tables, and a server with 40 databases holding 2,000 tables each makes
`session.open` exactly the slow call this section exists to prevent — column
laziness cannot help, because the cost is already paid before any table is
expanded.

*SQLite hides this: it has a single hardcoded `main`, so returning every table
eagerly costs nothing and looks correct. The tables tier therefore lands with
the MySQL driver, which is the first engine that cannot hide it — and before
it, so MySQL is written against the split rather than forcing it.*

The UI renders the database tier whenever `Capabilities.MultipleDatabases` is
set. Flattening databases into one table list shows a user three identically
named `users` rows from three schemas with nothing to tell them apart.

## 6. Connection storage and secrets

- Non-secret config (host, port, user, database, TLS options, colour label)
  is stored as JSON in the OS config directory.
- **Passwords go to the OS keychain** via `zalando/go-keyring` (Keychain,
  Credential Manager, Secret Service). They are never written to the JSON file.
- On Linux, Secret Service may be absent on minimal or headless systems. The
  fallback is an explicit, clearly-worded error offering an encrypted local
  vault unlocked by a master password. Silently degrading to plaintext is
  never acceptable.
- `ConnConfig` carries an optional `Dialer func(ctx, network, addr) (net.Conn, error)`.
  v1 always passes the default dialer. SSH tunnelling later supplies a
  different one, with no change to any driver.

## 7. Query execution and streaming

Returning a fully materialized result to the UI fails at scale. The contract:

1. `Query` returns a **cursor ID**, column metadata, and the first ~500 rows.
2. Glide Data Grid is windowed — it requests cells by visible region. The UI
   calls `FetchRows(cursorID, offset, limit)` on demand.
3. The engine holds the open cursor with a materialized buffer capped at a
   configurable size (default 50k rows). Every row inside the buffer is
   addressable by absolute index, so scrolling backward within it never
   re-queries. Once the cap is reached the buffer evicts its oldest window;
   scrolling back into evicted territory re-fetches by the rules below rather
   than growing memory without bound.
4. Fetching outside the buffer — whether ahead of it or back into an evicted
   window — depends on the source:
   - **Table browsing** uses **keyset pagination** on the primary key
     (`WHERE pk > ? ORDER BY pk LIMIT n`). Unlike `LIMIT/OFFSET`, this does not
     degrade deep into a large table.
   - **Arbitrary SQL** without a usable key falls back to re-execution with
     `LIMIT/OFFSET`, and the UI warns that deep scrolling is expensive.
5. Every query runs under a `context.Context` cancelled by the Stop button.
   Cursors are closed and evicted on tab close, cancel, or connection loss.

## 8. Staged edit model

The signature behaviour. Editing a cell does not touch the database.

- A result set is **editable only if** it maps to a single table and includes a
  unique or primary key. Otherwise the grid is read-only. This determination
  happens in `engine/`, not in the UI.
- A cell edit appends to the tab's `ChangeSet`:
  `Change{Kind, Table, Key map[string]any, Column, OldValue, NewValue}` with
  `Kind` in `{Update, Insert, Delete}`. The grid highlights affected cells.
- **Commit** folds the ChangeSet into ordered statements — deletes, then
  updates, then inserts — inside one transaction.
- **The generated SQL is shown for review before it runs.** This is a trust
  feature, not a debugging aid.
- **Optimistic concurrency:** updates are emitted as
  `UPDATE t SET col = ? WHERE pk = ? AND col = <old value>`. Zero rows affected
  means the row changed underneath the user; the commit rolls back and reports a
  conflict per row rather than silently clobbering.
- Partial failure rolls the whole transaction back and reports which change
  failed and why. The ChangeSet survives so the user can correct and retry.

`edit/` is pure functions from ChangeSet to SQL strings, with no database
dependency.

## 9. IPC protocol

**JSON-RPC 2.0 over stdio pipes.** Not a local TCP socket: the engine holds
live database credentials and open connections, and a TCP listener is reachable
by any other process on the machine. stdio inherits the process boundary as the
security boundary.

- Requests carry a monotonic ID; responses and errors match on it.
- Long-running operations (query progress, introspection) push **notifications**
  on the same pipe rather than blocking a response.
- Row payloads are JSON. The windowed cursor in Section 7 keeps individual
  payloads small (hundreds of rows). If profiling later shows serialization
  dominating, the row-fetch path can move to a length-prefixed binary frame
  without touching any other method.

## 10. Process supervision

- The shell spawns the sidecar at startup and performs a `health` handshake
  before showing the main window.
- A heartbeat detects a hung engine; a closed pipe detects a dead one.
- On crash: restart the sidecar, mark all tabs stale, **preserve every editor
  buffer and ChangeSet in the UI**, and offer per-tab reconnect. Losing an
  unsaved query because a driver segfaulted is unacceptable.
- The sidecar exits when its stdin closes, so no orphan process survives a UI
  crash.

## 11. Error model

Every driver error normalizes to:

```go
type Error struct {
    Kind    Kind   // Auth, Network, Syntax, Constraint, ReadOnly, Timeout, Canceled, NotFound, Unsupported, Invalid, Unknown
    Message string // human-readable, engine-neutral
    Native  string // original driver text, shown on demand
    Query   string // the statement, when applicable
}
```

The UI branches on `Kind` and shows `Native` for detail. Two rules:

- **`Canceled` is not a failure.** Pressing Stop must not paint the screen red.
- **Connection loss marks the tab stale** and offers reconnect; it never
  discards the editor buffer.

## 12. UI structure

- **Sidebar** — connections, then the lazy schema tree for the active one.
- **Tabs** — each owns a connection handle, an editor buffer, a result set, and
  a ChangeSet. Tab state is independent; closing one releases its cursors.
- **Grid** — Glide Data Grid, canvas-rendered. It builds no DOM node per cell,
  which is the only way to hit TablePlus-class scrolling.
- **Editor** — CodeMirror 6 with SQL highlighting, multi-statement execution,
  and schema-aware autocomplete fed from the cached catalog.
- State lives in Zustand stores split per concern (connections, tabs, schema
  cache) so a grid re-render does not depend on unrelated state.

### Design language

The list above is component structure, not design. The visual and interaction
decisions live in `design/` — seven artboards generated by `design/generate.py`
and published as a canvas. `Foundations.dc.html` and `Keyboard.dc.html` are the
normative ones: build against those values rather than reinventing them.

The decisions that constrain implementation, and therefore belong here:

- **Density is fixed.** 26 px data rows, 28 px column headers, 24 px sidebar
  rows, 244 px sidebar, 10 px cell gutters, 1 px hairline dividers and no
  shadows between panes. Toolbar controls are 23 px; dialog form controls are
  28 px. These are two deliberate scales, not an inconsistency.
- **Type.** IBM Plex Sans for interface, IBM Plex Mono for every value,
  identifier and SQL token. Data cells never fall below 12 px — column
  alignment depends on the mono face, so it is not a stylistic choice.
- **Keyboard-first is a constraint, not a feature.** The app must be drivable
  without a mouse, which rules out component choices that cannot take focus or
  cannot be reached in tab order. Retrofitting this is not possible, so the map
  in `Keyboard.dc.html` is part of the v1 acceptance criteria.
- **Connection colour is a safety mechanism.** Each connection carries a colour
  that tints its window chrome and tabs. Production is red and defaults to
  read-only, with confirmation required before an `UPDATE` or `DELETE` that has
  no `WHERE`. This is why `ConnConfig` carries a colour and a read-only flag.
- **The staged-edit visual language** (Section 8's mechanics, made visible):
  modified cells tinted amber with the previous value struck through beside the
  new one; inserted rows tinted green; deleted rows struck through and tinted
  red; a commit bar carrying the change count, a SQL preview, discard and
  commit; and a dot on any tab holding uncommitted changes.
- **Cancel is always on screen** while a query runs, never behind a menu, and
  results stream into the grid as they arrive.
- **Native platform conventions, not web defaults.** This is where a webview app
  gives itself away, and it is a requirement rather than polish:
  - **Every right-click must hit an app-defined menu.** Left alone, macOS shows
    its text-selection menu — Look Up, Translate, Search with Google, Summarize —
    on a connection row, where the useful actions are Edit, Duplicate, Test
    Connection and Delete. A table row wants Copy, Copy as INSERT, and Refresh; a
    grid cell wants Copy and Set NULL. Suppress the default menu everywhere and
    provide a real one per surface.
  - **UI chrome is not selectable text.** Labels, section headings and driver
    names must carry `user-select: none`; only data cells and query text are
    selectable. A drag across a sidebar label highlighting it like prose is the
    single clearest tell that a desktop app is a web page.
  - **Developer tools must not reach a release build.** Confirm by inspecting a
    packaged bundle, not by assuming the framework default.

- **Both themes are first-class.** Every token has a light and dark value; the
  palette on the Foundations artboard is the source for both.

## 13. Testing

- **Driver conformance suite** — one shared suite every driver must pass,
  run against real MySQL and MariaDB via `testcontainers-go` and against
  in-memory SQLite. Adding an engine means passing the suite. This is the
  primary mechanism keeping many drivers honest.
- **`edit/` golden tests** — ChangeSet in, SQL string out. Pure, fast, no
  database.
- **Cursor/pagination tests** — keyset correctness at boundaries, buffer cap
  eviction, cancel mid-fetch.
- **RPC contract tests** — the sidecar driven over stdio by a test harness,
  independent of any UI.
- **UI** — Vitest for ChangeSet and grid-adapter logic; Playwright smoke tests
  against a dev build covering connect, browse, edit, commit.

Development follows TDD: tests precede implementation for each unit above.

## 14. Build and distribution

- Go cross-compiles to six targets: darwin/windows/linux x amd64/arm64.
  Tauri's sidecar naming convention selects the right binary at bundle time.
- GitHub Actions matrix builds all platforms; Linux builds run on the oldest
  supported glibc to avoid symbol-version breakage.
- **Code signing is a budgeted line item, not an afterthought** — Apple
  Developer membership plus notarization for macOS, and a Windows signing
  certificate. Unsigned builds are effectively undistributable on both.

## 15. Risks

1. **WebKitGTK on Linux** renders and behaves differently from WKWebView and
   WebView2. Mitigation: run the Linux build in CI and test manually from week
   one rather than discovering divergence at release.
2. **JSON serialization on the row path** could become the bottleneck.
   Mitigation: the binary-frame escape hatch in Section 9, adopted only if
   profiling justifies it.
3. **Sidecar packaging** across six targets is fiddly. Mitigation: build the
   packaging pipeline early, on a trivial engine, before it has features to
   obscure failures.
4. **Scope.** TablePlus represents many years of work. Mitigation: the v1 scope
   in Section 1 is deliberately small, and additions wait for a working v1.

## 16. v1 acceptance criteria

- Create, test, edit, and delete MySQL, MariaDB, and SQLite connections, with
  passwords in the OS keychain.
- Browse a schema tree that loads lazily and stays responsive on a database
  with 1000+ tables.
- Open a table of 1M rows and scroll it smoothly via keyset pagination.
- Run multi-statement SQL, see per-statement results, and cancel a long query
  without the UI freezing or reporting a false error.
- Edit cells, insert and delete rows, review the generated SQL, commit inside a
  transaction, and receive a conflict report when a row changed underneath.
- Kill the sidecar externally and observe the UI recover with editor buffers
  intact.
- Signed, notarized installers for macOS, Windows, and Linux produced by CI.
