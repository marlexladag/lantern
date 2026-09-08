# TablePlus-like Database Client — Design

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
| Engine | Go 1.23+, standalone process |
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
    Open(ctx context.Context, cfg ConnConfig) (Conn, error)
}

// Required of every driver.
type Conn interface {
    Ping(ctx context.Context) error
    Introspect(ctx context.Context) (*schema.Catalog, error)
    Query(ctx context.Context, sql string, args ...any) (Cursor, error)
    Quote(ident string) string
    Close() error
}

// Optional capabilities.
type Execer     interface { Exec(ctx context.Context, sql string, args ...any) (Result, error) }
type Transactor interface { Begin(ctx context.Context) (Tx, error) }
type RowEditor  interface { ApplyChanges(ctx context.Context, cs *edit.ChangeSet) (*edit.Report, error) }

type Cursor interface {
    Columns() []ColumnMeta
    Next(ctx context.Context, n int) ([]Row, error) // returns io.EOF when exhausted
    Close() error
}
```

`Capabilities` reports what the UI should hide: transactions, DDL, schemas vs
databases, editable results, explain plans. Redis will report almost nothing
and the UI will adapt rather than crash.

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

## 5. Schema model

One normalized catalog shared by every engine:

```
Catalog -> Database/Schema -> Table | View -> Column, Index, ForeignKey
```

Each driver maps its native introspection into this shape (MySQL reads
`information_schema`, SQLite reads `pragma`). The UI never sees engine-specific
structures.

**Introspection is lazy.** On connect, load only the database and table lists.
Load columns, indexes, and foreign keys when a table is expanded.
`information_schema` is slow on servers with thousands of tables, and an eager
load would make connecting feel broken.

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
    Kind    Kind   // Auth, Network, Syntax, Constraint, Timeout, Canceled, Unknown
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
