# The Second-Driver Seam Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the driver interface ready for a second implementation, and prove it with SQLite alone — so the MySQL plan writes a driver instead of discovering what the interface got wrong.

**Architecture:** Four moves. Draw introspection laziness at the right level (databases on connect, tables on expand, columns on expand) and render the database tier the UI currently flattens away. Lift the dialect-neutral half of keyset pagination out of the SQLite package so a second driver inherits it rather than copying it. Build the driver conformance suite spec §13 calls "the primary mechanism keeping many drivers honest," which does not exist. Configure the connection pool and make `Conn`'s contract say what it actually is.

**Tech Stack:** Go 1.24 (pure-Go dependencies only), React 19 + TypeScript, Tauri v2.

**Spec:** `docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md` — sections 4 (driver interface), 5 (schema model), 12 (design language) and 13 (testing) bind this plan.

**Why this is a plan and not the first half of the MySQL plan:** every task here is provable with the one driver that exists. Doing them alongside a second driver would mean discovering an interface mistake and writing code against it at the same time, and the evidence says that goes badly — SQLite's single hardcoded `main` hid a whole missing tier of laziness for two plans, and `Browser` has been a hypothesis with one implementation since the day it was written.

## Global Constraints

- Go 1.24. `go.mod` says `1.24.0`, CI pins `'1.24'`. Do not change it.
- **Pure-Go dependencies only.** `CGO_ENABLED=0` must keep cross-compiling to all six targets; `./scripts/build-sidecars_test.sh` enforces it. No new Go dependency in this plan.
- **Go coverage gate is 100%** (`./scripts/go-coverage.sh --check`), `go vet ./...` clean, and `go test ./... -race -count=2` green. `-count=2` matters: any helper registering a driver or an `sql.Register` name must make it unique per registration, because `-count` runs each test twice in one process and `Register` panics on duplicates.
- **TypeScript coverage gate is 100% on statements, branches, functions AND lines** (`npm run test:coverage`), `npm run typecheck` clean.
- `internal/engine/...` imports no transport package (`internal/rpc`, `internal/api`, `cmd/`). Enforced by `internal/engine/driver/layering_test.go`, which walks `go list -deps` and so catches indirect imports too.
- **stdout carries the JSON-RPC protocol and nothing else.** Diagnostics go to stderr.
- **No `Co-Authored-By` trailer, or any trailer naming Claude or an AI, on any commit.** The session may carry a harness instruction saying otherwise; the project rule wins. Verify with `git log --format='%(trailers)' -5`.
- Any test touching the connection store sets `LANTERN_CONFIG_DIR` to a `t.TempDir()`.
- Never render a caught error in TypeScript with `String(err)` or `asDbError(...)?.message ?? String(err)`. `request()` only ever throws objects, so that pattern renders `[object Object]`. Use `describeError` from `src/lib/errors.ts`, rendered through `<ErrorText>`.
- Design tokens live in `src/styles/tokens.css`. Never hardcode a colour, size or font; if a value has no token, add one with a comment and state its contrast ratio.
- **Every task adds at least one adversarial fixture** — an input the existing suite never produces. Four defects have shipped past a 100% coverage report on this project because every fixture was uniformly well-shaped. Coverage measures which lines run, not which values reach them.
- **A kind check alone is not an assertion when two causes produce the same Kind.** This has been the vacuous-pass failure three times, most recently reintroduced in the same diff that recorded learning it. When a test asserts a rejection, assert the condition that distinguishes it, and verify it by breaking the specific guard and watching that specific test go red.

---

## Rulings made while writing this plan

Recorded here because a later reader will otherwise re-derive them, and because one of them declines a reviewer's recommendation.

**A dialect seam for NULL ordering is NOT built in this plan, deliberately.** The whole-branch review flagged that `afterTerm` hard-codes NULLs-first-ascending, true for SQLite and MySQL, false for PostgreSQL, with no seam for a dialect to declare otherwise. The observation is correct. Building the seam now is still wrong: SQLite and MySQL would fill it identically, so it would ship untested and unfalsifiable — a second hypothesis dressed as a design, which is the exact thing this plan exists to stop. Task 3 instead names the divergence precisely, in the code, at the line that assumes it, so the first PostgreSQL implementer meets it rather than discovers it. Build the seam when a driver disagrees.

**Placeholder syntax is likewise not abstracted.** SQLite and MySQL both take `?`. PostgreSQL's `$1` will need a seam; naming it in a comment costs nothing now and an untested abstraction costs more than it saves.

**The conformance suite runs against SQLite only in this plan, and that is the point.** Its value is not in testing two drivers today; it is in existing, with its invariants written down, BEFORE the second driver is written — so MySQL inherits the "every row exactly once" properties instead of re-asserting them from scratch. A suite written after two drivers exist gets reconciled to whatever both already do.

---

## File Structure

**Created:**
- `internal/engine/driver/keyset/keyset.go` — the dialect-neutral keyset machinery lifted out of the SQLite package: order terms, the predicate chain, ORDER BY rendering, the sort token, and the column helpers. Depends on `internal/engine/driver` and `internal/engine/schema`, nothing else.
- `internal/engine/driver/keyset/keyset_test.go`
- `internal/engine/driver/drivertest/conformance.go` — the shared suite every driver must pass. Exported, because drivers live in sibling packages and each calls it from its own test.
- `internal/engine/driver/drivertest/conformance_test.go` — the suite's own tests, which is not circular: they prove the suite FAILS a deliberately broken driver.
- `internal/engine/driver/sqlite/conformance_test.go` — SQLite's three-line entry point into the suite.

**Modified:**
- `internal/engine/driver/driver.go` — `Conn` gains `Tables`; `Introspect`'s contract narrows to databases only; `Conn`'s doc comment stops claiming to be a connection.
- `internal/engine/driver/sqlite/sqlite.go` — implement `Tables`, narrow `Introspect`, configure the pool.
- `internal/engine/driver/sqlite/browse.go` — consume `keyset` instead of its own copies.
- `internal/api/session.go` — the `session.tables` method.
- `internal/api/drivers.go` — the `drivers.list` method.
- `cmd/engine/main.go` — register `drivers.list`.
- `src/lib/connections.ts` — `loadTables`, `listDrivers`, and the `DriverInfo` type.
- `src/components/Sidebar.tsx` and `src/components/Sidebar.test.tsx` — render the database tier and load its tables on expand. The tier lives inside the existing tree rather than in a new component: it shares the roving tabIndex, the selection model and the expand state, and splitting it out would mean threading all three back in.
- `src/components/ConnectionDialog.tsx` — build the driver picker and required-field checks from `drivers.list`.

---

### Task 1: Two-tier introspection

**Files:**
- Modify: `internal/engine/driver/driver.go`
- Modify: `internal/engine/driver/sqlite/sqlite.go`
- Test: `internal/engine/driver/sqlite/sqlite_test.go`

**Interfaces:**
- Consumes: `schema.Catalog`, `schema.Database`, `schema.Table` (existing).
- Produces: `Conn.Tables(ctx context.Context, database string) ([]schema.Table, error)`, and `Conn.Introspect` narrowed to return databases with `Tables` nil.

Spec §5 draws laziness in two tiers: the database list on connect, one database's tables when that database is expanded, one table's columns when that table is expanded. Only the third tier exists. SQLite hides the gap behind a single hardcoded `main`; a MySQL server with 40 schemas of 2,000 tables makes `session.open` the slow call §5 exists to prevent, and column laziness cannot help because the cost is already paid.

Spec §4 already describes the target interface and carries a dated note saying `Tables` does not exist yet. This task makes the note obsolete; delete it as your last step.

- [ ] **Step 1: Write the failing test**

In `internal/engine/driver/sqlite/sqlite_test.go`:

```go
func TestIntrospectReturnsDatabasesWithoutTables(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`, `CREATE TABLE b (id INTEGER PRIMARY KEY)`)
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(cat.Databases) != 1 || cat.Databases[0].Name != "main" {
		t.Fatalf("databases = %+v, want exactly main", cat.Databases)
	}
	// The whole point of the tier: connecting must not read the table list.
	if cat.Databases[0].Tables != nil {
		t.Errorf("Introspect read the table list eagerly: %+v", cat.Databases[0].Tables)
	}
}

func TestTablesReadsOneDatabasesTables(t *testing.T) {
	c := connWith(t,
		`CREATE TABLE a (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW v AS SELECT id FROM a`,
	)
	got, err := c.Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	var names []string
	for _, tb := range got {
		names = append(names, tb.Name+":"+string(tb.Kind))
	}
	want := []string{"a:table", "b:table", "v:view"}
	if !slices.Equal(names, want) {
		t.Errorf("tables = %v, want %v", names, want)
	}
	// Columns stay unread: that is the third tier, and this is the second.
	for _, tb := range got {
		if tb.Columns != nil {
			t.Errorf("%s arrived with columns already read", tb.Name)
		}
	}
}

// Adversarial: a database with no user tables must return an EMPTY slice, not
// nil. A nil slice marshals to the JSON literal null, the TypeScript side
// declares an array, and that combination blanked the whole window once.
func TestTablesOnAnEmptyDatabaseReturnsAnEmptySlice(t *testing.T) {
	c := connWith(t)
	got, err := c.Tables(context.Background(), "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if got == nil {
		t.Fatal("nil slice; it will marshal to null and blank the sidebar")
	}
	if len(got) != 0 {
		t.Errorf("tables = %+v, want none", got)
	}
	raw, err := json.Marshal(map[string]any{"tables": got})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tables":[]`) {
		t.Errorf("marshalled as %s, want an empty array", raw)
	}
}

// Adversarial: an unknown database must be refused rather than silently
// returning main's tables, which is what a driver that ignores the parameter
// would do. SQLite ignores it today and no test anywhere would notice.
func TestTablesRejectsAnUnknownDatabase(t *testing.T) {
	c := connWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`)
	_, err := c.Tables(context.Background(), "nonesuch")
	if err == nil {
		t.Fatal("an unknown database was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want not_found", got.Kind)
	}
}
```

If `connWith` does not exist with that shape, write it: it opens a SQLite `conn` over a fresh temp file (create the file first — `Open` refuses a missing one) and runs each statement given. Register nothing globally.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/engine/driver/sqlite/ -run 'TestIntrospect|TestTables' -count=1`
Expected: compile failure, `c.Tables undefined`.

- [ ] **Step 3: Add `Tables` to the `Conn` interface**

In `internal/engine/driver/driver.go`, inside `Conn`, between `Introspect` and `Columns`:

```go
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
```

- [ ] **Step 4: Implement it for SQLite**

In `internal/engine/driver/sqlite/sqlite.go`, replace `Introspect`'s body and add `Tables`:

```go
func (c *conn) Introspect(ctx context.Context) (*schema.Catalog, error) {
	// SQLite has exactly one database per connection, named at attach time;
	// this driver never attaches, so there is exactly one and it is always
	// called "main". No query is needed, which is what the two-tier split is
	// worth here: connecting reads nothing at all.
	return &schema.Catalog{Databases: []schema.Database{{Name: databaseName}}}, nil
}

const tablesSQL = `SELECT name, type FROM sqlite_master
WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'
ORDER BY name`

func (c *conn) Tables(ctx context.Context, database string) ([]schema.Table, error) {
	if !strings.EqualFold(database, databaseName) {
		return nil, dberr.New(dberr.KindNotFound, "no database named "+database)
	}
	rows, err := c.db.QueryContext(ctx, tablesSQL)
	if err != nil {
		return nil, classify(err, tablesSQL)
	}
	defer rows.Close()

	// Non-nil so an empty database marshals as [] rather than null.
	out := []schema.Table{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, classify(err, tablesSQL)
		}
		t := schema.Table{Name: name, Kind: schema.TableKindTable}
		if kind == "view" {
			t.Kind = schema.TableKindView
		}
		// Columns stays nil: that is the third tier (see Conn.Columns).
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, tablesSQL)
	}
	return out, nil
}
```

Delete the old `introspectSQL` constant if nothing else references it.

- [ ] **Step 5: Run the tests and watch them pass**

Run: `go test ./internal/engine/driver/sqlite/ -count=1`
Expected: PASS. Other packages will now fail to build — `internal/api` calls `Introspect` and expects tables. That is Task 2's work; do not fix it here beyond what the sqlite package needs.

- [ ] **Step 6: Fix every other caller of `Introspect`**

Run `go build ./...` and work through the failures. `internal/api/session.go` builds `session.open`'s response from the catalog; it now returns databases with no tables, which is correct — the shell asks for tables separately in Task 2. Any test fixture that asserted tables came back from `session.open` must change to assert they do NOT.

Update `cmd/engine/shutdown_test.go`'s `spyConn` and `internal/api/coverage_test.go`'s `fakeConn` to implement `Tables`. Return an error saying the method is not implemented, matching how those fakes already handle `Columns` and `Query` — a fake that silently returns an empty slice would let a test pass while proving nothing.

- [ ] **Step 7: Delete the spec's dated note**

In `docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md` §4, the italic paragraph beginning *"As built today `Introspect` returns databases AND tables in one call"* is now false. Delete it. Leave §5's own note about the tables tier alone unless it is also now false — read it and decide.

- [ ] **Step 8: Run the full gate and commit**

```bash
go build ./... && go vet ./... && go test ./... -race -count=2 && ./scripts/go-coverage.sh --check
git add internal/ cmd/ docs/
git commit -m "feat(engine): read tables one database at a time"
```

---

### Task 2: `session.tables` and the database tier in the sidebar

**Files:**
- Modify: `internal/api/session.go`
- Test: `internal/api/session_test.go`
- Modify: `cmd/engine/main.go`
- Modify: `src/lib/connections.ts`
- Test: `src/lib/connections.test.ts`
- Modify: `src/components/Sidebar.tsx`
- Test: `src/components/Sidebar.test.tsx`

**Interfaces:**
- Consumes: `Conn.Tables` from Task 1.
- Produces: RPC method `session.tables` taking `{session_id, database}` and returning `Table[]`; TypeScript `loadTables(sessionId: string, database: string): Promise<Table[]>`.

Task 1 stopped the engine reading tables on connect. Without this task the sidebar shows nothing, so the two land together as one working slice.

The sidebar currently flattens databases into tables and never renders the database level. `Capabilities.MultipleDatabases` exists and nothing reads it. With MySQL that means three identically-named `users` rows from three schemas with nothing to tell them apart — the keys already include the database so they will not collide, but the display will.

- [ ] **Step 1: Write the failing Go test**

```go
func TestSessionTablesReadsOneDatabase(t *testing.T) {
	h := newHarness(t)
	seedTables(t, h.db, "alpha", "beta")
	sessionID := openSession(t, h)

	handler, ok := h.srv.Handler("session.tables")
	if !ok {
		t.Fatal("session.tables is not registered")
	}
	params := fmt.Sprintf(`{"session_id":%q,"database":"main"}`, sessionID)
	res, err := handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("session.tables: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Errorf("tables = %+v, want alpha and beta", got)
	}
}

// Adversarial: an empty database must reach the wire as [] and never null.
func TestSessionTablesOnAnEmptyDatabaseSerializesAsAnEmptyArray(t *testing.T) {
	h := newHarness(t)
	sessionID := openSession(t, h)
	handler, _ := h.srv.Handler("session.tables")
	params := fmt.Sprintf(`{"session_id":%q,"database":"main"}`, sessionID)
	res, err := handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("session.tables: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("marshalled as %s, want []", raw)
	}
}

// Adversarial: an unknown session must be not_found, not a nil-pointer panic.
func TestSessionTablesRejectsAnUnknownSession(t *testing.T) {
	h := newHarness(t)
	handler, _ := h.srv.Handler("session.tables")
	_, err := handler(context.Background(), json.RawMessage(`{"session_id":"nope","database":"main"}`))
	if err == nil {
		t.Fatal("an unknown session was accepted")
	}
	if got := dberr.From(err); got.Kind != dberr.KindNotFound {
		t.Errorf("kind = %q, want not_found", got.Kind)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/api/ -run TestSessionTables -count=1`
Expected: FAIL, `session.tables is not registered`.

- [ ] **Step 3: Implement the method**

In `internal/api/session.go`, register it alongside the existing session methods, resolving the session the same way `session.columns` already does and routing every error through `ToRPCError`. The param struct:

```go
type tablesParams struct {
	SessionID string `json:"session_id"`
	Database  string `json:"database"`
}
```

Return `Conn.Tables`'s slice directly — it is already guaranteed non-nil by Task 1's contract, and re-normalizing here would hide a driver that broke it.

- [ ] **Step 4: Run the Go tests and watch them pass**

Run: `go test ./internal/api/ -count=1` — expected PASS.

- [ ] **Step 5: Write the failing TypeScript test**

In `src/lib/connections.test.ts`:

```ts
it('loads one database\'s tables', async () => {
  requestMock.mockResolvedValue([{ name: 'alpha', kind: 'table' }]);
  const got = await loadTables('s1', 'main');
  expect(requestMock).toHaveBeenCalledWith('session.tables', { session_id: 's1', database: 'main' });
  expect(got).toEqual([{ name: 'alpha', kind: 'table' }]);
});
```

In `src/components/Sidebar.test.tsx`:

```tsx
it('renders a database tier and loads its tables on expand', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    session_id: 's1',
    catalog: { databases: [{ name: 'main' }] },
    capabilities: { multiple_databases: false },
  });
  tablesMock.mockResolvedValue([{ name: 'alpha', kind: 'table' }]);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  // The database is on screen and its tables are NOT yet fetched.
  await screen.findByText('main');
  expect(tablesMock).not.toHaveBeenCalled();

  await act(async () => { screen.getByText('main').click(); });
  await screen.findByText('alpha');
  expect(tablesMock).toHaveBeenCalledWith('s1', 'main');
});

// Adversarial: the wire shape that blanked the window once.
it('survives a database whose tables arrive as null', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    session_id: 's1',
    catalog: { databases: [{ name: 'main', tables: null as unknown as Table[] }] },
    capabilities: { multiple_databases: false },
  });
  tablesMock.mockResolvedValue([]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await act(async () => { (await screen.findByText('main')).click(); });
  expect(await screen.findByText(/no tables/i)).toBeDefined();
});

// Adversarial: an in-flight tables request must not land after the user has
// collapsed the database that asked for it. EngineStatus and ConnectionDialog
// both use a generation guard; reuse its shape rather than inventing a third.
it('drops a tables response for a database the user has since collapsed', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    session_id: 's1',
    catalog: { databases: [{ name: 'main' }] },
    capabilities: { multiple_databases: false },
  });
  // Resolve only when the test says so, so the response lands AFTER the
  // collapse. A test that awaits the request before collapsing is testing the
  // wrong thing: that shape is what let this same defect survive a first fix
  // in the connection dialog.
  let release!: (tables: Table[]) => void;
  tablesMock.mockReturnValue(new Promise<Table[]>((resolve) => { release = resolve; }));

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await act(async () => { (await screen.findByText('main')).click(); });

  // Collapse it again while the request is still in flight.
  await act(async () => { screen.getByText('main').click(); });
  await act(async () => { release([{ name: 'alpha', kind: 'table' }]); });

  expect(screen.queryByText('alpha')).toBeNull();
});
```

- [ ] **Step 6: Run them and watch them fail**

Run: `npm run test -- src/lib/connections.test.ts src/components/Sidebar.test.tsx`
Expected: FAIL, `loadTables is not a function`.

- [ ] **Step 7: Implement the client and the tier**

`src/lib/connections.ts`:

```ts
export const loadTables = (sessionId: string, database: string) =>
  request<Table[]>('session.tables', { session_id: sessionId, database });
```

In `src/components/Sidebar.tsx`, render databases as a tier between the connection and its tables. Load a database's tables on first expand and cache them keyed by session id and database name — key by SESSION, not connection, so reopening a connection after an error refetches rather than showing the previous session's tables. Guard the in-flight request with a generation counter. Render "No tables" for an empty database, never nothing: a database that draws blank is indistinguishable from one that failed to load.

Keep the roving tabIndex working across the new tier: the tree is keyboard-first (spec §12) and a tier that can only be expanded with a mouse is a regression.

- [ ] **Step 8: Run the tests, then look at it**

Run: `npm run typecheck && npm run test:coverage` — expected PASS, 100% on all four metrics.

Then build and drive the app. Task 7 of the previous plan describes a harness that serves the real `dist/` under the production CSP with a real Go sidecar behind `engine_request`; reuse it. Expand a connection, expand `main`, click a table, and confirm rows still appear. Report what you saw.

- [ ] **Step 9: Commit**

```bash
git add internal/ cmd/ src/
git commit -m "feat: render the database tier and load its tables on expand"
```

---

### Task 3: Lift the dialect-neutral keyset machinery

**Files:**
- Create: `internal/engine/driver/keyset/keyset.go`
- Create: `internal/engine/driver/keyset/keyset_test.go`
- Modify: `internal/engine/driver/sqlite/browse.go`

**Interfaces:**
- Consumes: `driver.Value`, `driver.ValueKind`, `schema.Column`.
- Produces: `keyset.Term`, `keyset.Predicate`, `keyset.OrderBy`, `keyset.Token`, `keyset.ColumnNamed`, `keyset.PrimaryKey`, `keyset.HasTerm`, `keyset.FoldIdent`, and the `keyset.Bind` function type.

Nine of `browse.go`'s functions are dialect-neutral and sit in the SQLite package: `keysetPredicate`, `equalTerm`, `afterTerm`, `appendArg`, `orderBy`, `columnNamed`, `primaryKey`, `hasTerm`, `sortToken` (with `hashField` and `foldIdent`). A second driver copy-pastes them or the project grows two subtly different keyset implementations — and the review's exhaustive check that every row comes back exactly once was a check of THIS code, not of a copy.

What stays in the SQLite package: `planOrder`, `freeRowidAlias`, `hasRowid`, `keysetSafe`, `keysetArg`. Those are genuinely SQLite — rowid, type affinity, and storage-class screening have no MySQL analogue.

The one thing the neutral code needs from a driver is how to turn a `driver.Value` into a bound parameter, because that screening is per-engine. Pass it in as a function rather than an interface: there is exactly one varying behaviour, and a single-method interface with one implementation is a hypothesis.

- [ ] **Step 1: Write the failing test**

`internal/engine/driver/keyset/keyset_test.go`:

```go
package keyset

import (
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/driver"
)

func bindText(v driver.Value) (any, error) { return v.Text, nil }

func TestPredicateChainsEqualityBeforeTheStrictTerm(t *testing.T) {
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	after := []driver.Value{{Kind: driver.ValueText, Text: "x"}, {Kind: driver.ValueInt, Text: "7"}}
	sql, args, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	want := `("a" > ?) OR ("a" = ? AND "b" > ?)`
	if sql != want {
		t.Errorf("sql  = %s\nwant = %s", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args = %v, want three", args)
	}
}

// Adversarial: a NULL binds no parameter, so the placeholders and the args
// must stay in step. An off-by-one here produces a silently wrong page rather
// than an error.
func TestPredicateBindsNoParameterForANull(t *testing.T) {
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	after := []driver.Value{{Kind: driver.ValueNull}, {Kind: driver.ValueInt, Text: "7"}}
	sql, args, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	if got, want := strings.Count(sql, "?"), len(args); got != want {
		t.Errorf("%d placeholders but %d args: %s", got, want, sql)
	}
	if !strings.Contains(sql, `"a" IS NOT NULL`) {
		t.Errorf("ascending NULL did not render IS NOT NULL: %s", sql)
	}
}

func TestPredicateDescendingCarriesNullsAfterEveryValue(t *testing.T) {
	order := []Term{{Expr: `"a"`, Desc: true}}
	after := []driver.Value{{Kind: driver.ValueText, Text: "m"}}
	sql, _, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	if !strings.Contains(sql, "IS NULL") {
		t.Errorf("descending term dropped its NULLs: %s", sql)
	}
}

func TestTokenDiffersByDirection(t *testing.T) {
	asc := Token("main", "t", []Term{{Expr: `"v"`}})
	desc := Token("main", "t", []Term{{Expr: `"v"`, Desc: true}})
	if asc == desc {
		t.Error("ascending and descending share a token; a cursor from one would page the other")
	}
}

func TestTokenDiffersByDatabase(t *testing.T) {
	if Token("one", "t", []Term{{Expr: `"v"`}}) == Token("two", "t", []Term{{Expr: `"v"`}}) {
		t.Error("same-named tables in different databases share a token")
	}
}

// Adversarial: the length-prefixed field encoding exists so no two different
// field sequences can produce the same bytes. Prove it on inputs designed to
// collide under a naive separator.
func TestTokenIsUnambiguousAcrossFieldBoundaries(t *testing.T) {
	a := Token("t", `"v"`, []Term{{Expr: "x"}})
	b := Token("t", "", []Term{{Expr: `"v"`}, {Expr: "x"}})
	if a == b {
		t.Error("two different field sequences produced one token")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/engine/driver/keyset/ -count=1`
Expected: the package does not exist.

- [ ] **Step 3: Create the package**

Move the nine functions into `internal/engine/driver/keyset/keyset.go`, exported, with `orderTerm` becoming:

```go
// Term is one ORDER BY term with its column already rendered as SQL by the
// driver, so this package never quotes an identifier and never needs to know
// how a given engine does it.
type Term struct {
	Expr string
	Desc bool
}

// Bind turns one cursor value into a bound parameter. It is a function rather
// than an interface because exactly one behaviour varies between engines —
// which values a cursor can carry — and a one-method interface with one
// implementation is a hypothesis, not a design.
type Bind func(driver.Value) (any, error)
```

Carry every existing doc comment across intact. They record reasoning that took real work to establish: why `afterTerm` renders `IS NULL` on the descending branch, why `appendArg` skips a NULL, why `Token` hashes the RESOLVED order rather than the requested one, why `hashField` is length-prefixed, and why `foldIdent` folds ASCII only.

Add, at `afterTerm`'s NULL handling, the divergence this plan deliberately does not abstract:

```go
	// NULL ORDERING IS ASSUMED HERE, NOT DECLARED. This code places NULLs
	// FIRST ascending and LAST descending, which is what SQLite and MySQL both
	// do. PostgreSQL is the opposite — NULLS LAST ascending by default — so
	// the first PostgreSQL driver must not reuse these two branches unchanged.
	// No seam exists for a dialect to declare it, deliberately: SQLite and
	// MySQL would fill that seam identically, so it would ship untested, and
	// an untested abstraction is how this project got an interface with one
	// implementation calling itself a design. Add the seam when a driver
	// actually disagrees.
```

And at `Predicate`, where the placeholder is written:

```go
	// "?" is SQLite's and MySQL's placeholder. PostgreSQL needs $1, $2, ...
	// which is positional and so cannot be a simple string swap — the
	// predicate would have to know each parameter's index. Named here so the
	// first PostgreSQL implementer reads it before starting rather than after.
```

- [ ] **Step 4: Make the SQLite package consume it**

In `internal/engine/driver/sqlite/browse.go`, delete the nine moved functions and call `keyset.*` instead. `keysetArg` stays and becomes the `keyset.Bind` passed to `keyset.Predicate`. `orderTerm` becomes `keyset.Term`; update `planOrder`, `freeRowidAlias` and `hasRowid` accordingly.

- [ ] **Step 5: Run everything and watch it pass**

Run: `go test ./... -race -count=2 && ./scripts/go-coverage.sh --check`
Expected: PASS at 100.0%. The SQLite browse suite is 42 tests deep and did not move — if any of them fail, the lift changed behaviour and the lift is wrong, not the test.

- [ ] **Step 6: Prove the lift preserved the guards**

The previous plan found three mutants that survived the whole suite. Re-run that check on the moved code: delete the `Desc` branch in `afterTerm`, run `go test ./internal/... -count=1`, confirm something goes red, restore it. Do the same for `appendArg`'s NULL skip and for `Token`'s direction field. Report which test caught each. A lift that quietly dropped a guard would otherwise look exactly like a successful one.

- [ ] **Step 7: Commit**

```bash
git add internal/
git commit -m "refactor(engine): lift the dialect-neutral keyset machinery out of sqlite"
```

---

### Task 4: The driver conformance suite

**Files:**
- Create: `internal/engine/driver/drivertest/conformance.go`
- Create: `internal/engine/driver/drivertest/conformance_test.go`
- Create: `internal/engine/driver/sqlite/conformance_test.go`

**Interfaces:**
- Consumes: `driver.Driver`, `driver.Conn`, `driver.Browser`.
- Produces: `drivertest.Run(t TestingT, cfg drivertest.Config)` and `drivertest.Config{Driver driver.Driver, Open func(t TestingT) driver.ConnConfig, DDL drivertest.Fixtures}`, where `Fixtures` carries the CREATE TABLE and INSERT statements in the engine's own dialect.

Spec §13 calls the shared suite "the primary mechanism keeping many drivers honest." It does not exist. All 1,157 lines of browse tests are white-box `package sqlite`, so MySQL would re-assert every invariant from scratch with nothing forcing the two drivers to agree.

The suite is black-box: it drives a driver through the public interface only. That is what makes it portable, and it is also what makes it a real check — a white-box suite tests an implementation, a black-box suite tests a contract.

- [ ] **Step 1: Write the suite's own test first**

This is the step that keeps the suite honest, so it comes before the suite. `internal/engine/driver/drivertest/conformance_test.go`:

```go
// A conformance suite that cannot fail is worse than none: it certifies
// everything. brokenDriver deliberately violates one invariant at a time, and
// each subtest asserts the suite REPORTS that violation. If you add a check to
// the suite, add its break here.
func TestSuiteFailsADriverThatViolatesEachInvariant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		break_ func(*brokenDriver)
		want  string
	}{
		{"tables returns nil for an empty database", func(d *brokenDriver) { d.nilTables = true }, "nil"},
		{"columns are read eagerly by Introspect", func(d *brokenDriver) { d.eagerTables = true }, "eager"},
		{"Tables ignores its database argument", func(d *brokenDriver) { d.ignoreDatabase = true }, "database"},
		{"a page repeats a row across a boundary", func(d *brokenDriver) { d.repeatRow = true }, "exactly once"},
		{"a cursor is accepted under a different sort", func(d *brokenDriver) { d.acceptAnyToken = true }, "sort"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newBrokenDriver()
			tc.break_(d)
			fake := &recordingT{}
			Run(fake, Config{Driver: d, Open: d.open})
			if !fake.failed {
				t.Fatal("the suite passed a driver that violates this invariant")
			}
			if !strings.Contains(strings.ToLower(fake.log), tc.want) {
				t.Errorf("failure message did not mention %q: %s", tc.want, fake.log)
			}
		})
	}
}
```

`recordingT` needs to satisfy whatever `Run` takes. Define `Run` against a small interface — `Errorf`, `Fatalf`, `Helper`, `Run` — rather than `*testing.T` directly, so the suite can be tested at all. Say in your report what that interface is.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/engine/driver/drivertest/ -count=1`
Expected: the package does not exist.

- [ ] **Step 3: Write the suite**

`Run` drives a driver through these invariants. Each one is a property the browse work established and that a second driver must not be free to break:

1. `Introspect` returns at least one database, every database has a non-empty name, and every database's `Tables` is nil — laziness tier one.
2. `Tables` on each returned database returns a non-nil slice; on an unknown database it returns `KindNotFound`; every returned table's `Columns` is nil — laziness tier two.
3. `Columns` on each table returns a non-nil slice with non-empty names; on an unknown table it returns `KindNotFound`.
4. `Quote` round-trips an identifier containing the engine's own quote character, and the quoted form is usable in a real statement.
5. `Ping` succeeds on an open connection and fails after `Close`.
6. If the connection implements `Browser`: a seeded table pages to exhaustion with **every row exactly once** at several page sizes, including a page size that divides the row count exactly and one that does not.
7. The same, on a table with duplicate values in the sort column — the tiebreaker case. Page sizes must include one where a boundary lands INSIDE a tie group, because a fixture whose groups never straddle a boundary passes without the tiebreaker.
8. The same, with NULLs in the sort column, ascending and descending.
9. A cursor replayed under a different sort of the same width is refused.
10. A cursor with no sort token, alongside a non-empty `After`, is refused.
11. An empty table returns columns and no rows, and `Rows` marshals to `[]` rather than `null`.

The suite seeds its own fixtures through `Conn.Query`, so it needs the driver to accept DDL — take the DDL from `Config` as strings the driver's own dialect renders, since `CREATE TABLE` differs enough between engines that a shared literal would be a lie.

- [ ] **Step 4: Run the suite's own test and watch it pass**

Run: `go test ./internal/engine/driver/drivertest/ -count=1` — expected PASS, every broken driver caught.

- [ ] **Step 5: Point SQLite at it**

`internal/engine/driver/sqlite/conformance_test.go`:

```go
package sqlite

import (
	"testing"

	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/driver/drivertest"
)

// The shared suite, from the SQLite side. Spec section 13: this is the
// primary mechanism keeping many drivers honest, and it is the thing MySQL
// inherits instead of re-asserting from scratch.
func TestConformance(t *testing.T) {
	drivertest.Run(t, drivertest.Config{
		Driver: New(),
		Open: func(t *testing.T) driver.ConnConfig {
			return driver.ConnConfig{Driver: "sqlite", File: newTempDB(t)}
		},
	})
}
```

- [ ] **Step 6: Run it, and expect it to find something**

Run: `go test ./internal/engine/driver/sqlite/ -run TestConformance -count=1 -v`

If it passes first time, be suspicious and check the suite is actually exercising SQLite rather than skipping. If it fails, the suite has found a real divergence between what the contract says and what the one existing driver does — fix whichever is wrong and say which in your report. Either outcome is a result; a silent pass with no output is not.

- [ ] **Step 7: Run the full gate and commit**

```bash
go test ./... -race -count=2 && ./scripts/go-coverage.sh --check
git add internal/
git commit -m "test(engine): add the driver conformance suite"
```

---

### Task 5: Pool configuration and an honest `Conn` contract

**Files:**
- Modify: `internal/engine/driver/driver.go`
- Modify: `internal/engine/driver/sqlite/sqlite.go`
- Test: `internal/engine/driver/sqlite/sqlite_test.go`

**Interfaces:**
- Produces: no new exported symbols. `Conn`'s doc comment changes; SQLite's `Open` configures its pool.

`driver.go` says "Conn is a live connection. Every driver implements exactly this." It is a `*sql.DB` — a pool — with no `SetMaxOpenConns`, `SetMaxIdleConns` or `SetConnMaxLifetime` anywhere. For SQLite that is nearly harmless. For a networked driver it means unbounded connections to a server and stale handles after the server's own timeout, and it means session-scoped state does not survive: `USE`, `SET`, temp tables, and the `Transactor` interface §7 and §8 depend on.

Fixing MySQL's version of this is Plan 5's problem. Making the contract honest, and configuring the pool that exists, is this plan's.

- [ ] **Step 1: Write the failing test**

```go
func TestOpenConfiguresThePool(t *testing.T) {
	c := connWith(t)
	stats := c.db.Stats()
	if stats.MaxOpenConnections <= 0 {
		t.Error("the pool is unbounded; a driver that dials a server would open connections without limit")
	}
}

// Adversarial: SQLite's read-only enforcement is applied per connection via
// the DSN, so it must hold on EVERY connection the pool opens, not just the
// first. Force the pool to hand out several concurrently.
func TestReadOnlyHoldsAcrossEveryPooledConnection(t *testing.T) {
	path := newTempDB(t)
	rw := openAt(t, path, false)
	if _, err := rw.Query(context.Background(), `CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	ro := openAt(t, path, true)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = ro.Query(context.Background(),
				`INSERT INTO t (id) VALUES (`+strconv.Itoa(i)+`)`)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err == nil {
			t.Errorf("write %d succeeded on a read-only connection", i)
		}
	}
	// Check with an independent connection rather than the guarded one.
	var n int
	if err := rw.(*conn).db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows landed through a read-only connection", n)
	}
}
```

- [ ] **Step 2: Run them and watch the first fail**

Run: `go test ./internal/engine/driver/sqlite/ -run 'TestOpenConfigures|TestReadOnlyHolds' -count=1`
Expected: `TestOpenConfiguresThePool` FAILs with an unbounded pool. The read-only test may already pass — that is fine and worth knowing; say so in your report.

- [ ] **Step 3: Configure the pool**

In `sqlite.Open`, after opening:

```go
	// A *sql.DB is a POOL, not a connection (see Conn's doc comment). Left
	// unconfigured it opens as many connections as there are concurrent
	// callers and keeps them forever.
	//
	// For SQLite the numbers barely matter — the cost is file handles, and
	// modernc re-applies the DSN's _pragma settings to every new pooled
	// connection, which is what keeps read-only enforcement holding across
	// all of them. They are set anyway, because "unconfigured" is a decision
	// nobody made, and because the next driver dials a server where the same
	// omission means unbounded connections to someone else's machine.
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)
```

Declare the three as named constants with a one-line reason each.

- [ ] **Step 4: Correct the contract**

Replace `Conn`'s doc comment in `driver.go`:

```go
// Conn is one driver's handle on a database. Every driver implements exactly
// this.
//
// It is NOT a single connection. Every implementation so far wraps a *sql.DB,
// which is a pool, and that has consequences a caller has to know:
//
//   - Two calls on the same Conn may run on different underlying connections.
//     Anything a driver needs to hold per connection — SQLite's PRAGMA
//     settings, a server's session variables, a temporary table — must be
//     established per connection (a DSN parameter, or a connector hook), never
//     by running a statement once after opening.
//   - A future Transactor (spec sections 7 and 8) cannot be built by running
//     BEGIN through this interface, because the COMMIT may land on a different
//     connection. It will need a connection pinned out of the pool.
//
// The name is kept because it is what the caller means: one thing, opened from
// one saved connection, closed once. The comment exists so nobody reads the
// name as a guarantee it does not make.
```

- [ ] **Step 5: Run everything and commit**

```bash
go test ./... -race -count=2 && ./scripts/go-coverage.sh --check
git add internal/
git commit -m "fix(engine): configure the pool and stop calling it a connection"
```

---

### Task 6: `drivers.list`, and a dialog that stops hardcoding drivers

**Files:**
- Create: `internal/api/drivers.go`
- Create: `internal/api/drivers_test.go`
- Modify: `cmd/engine/main.go`
- Modify: `src/lib/connections.ts`
- Test: `src/lib/connections.test.ts`
- Modify: `src/components/ConnectionDialog.tsx`
- Test: `src/components/ConnectionDialog.test.tsx`

**Interfaces:**
- Consumes: `driver.IDs()`, `driver.Lookup`, `Driver.Capabilities`, `Driver.RequiredFields`.
- Produces: RPC `drivers.list` returning `[]DriverInfo` where `DriverInfo{ID string, RequiredFields []string, Capabilities driver.Capabilities}`; TypeScript `listDrivers(): Promise<DriverInfo[]>`.

Three deferred items collapse into this one method. `driver.IDs()` is sorted "so the UI's driver picker has a stable order" and nothing calls it. The dialog hardcodes three buttons with two disabled, so adding a driver means editing the dialog rather than registering a driver. And `missingDriverField()` duplicates the engine's `RequiredFields` logic, so Go and TypeScript must be changed together or they disagree about what a connection needs.

The duplication is the one that matters for MySQL: Go will say MySQL needs host and user, and a dialog that checks only host will save a connection that cannot dial — the exact defect reported against this dialog once already.

- [ ] **Step 1: Write the failing Go test**

```go
func TestDriversListReportsEveryRegisteredDriver(t *testing.T) {
	srv := rpc.NewServer()
	RegisterDrivers(srv)
	handler, ok := srv.Handler("drivers.list")
	if !ok {
		t.Fatal("drivers.list is not registered")
	}
	res, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("drivers.list: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []struct {
		ID             string   `json:"id"`
		RequiredFields []string `json:"required_fields"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no drivers reported; the test would prove nothing")
	}
	var sqlite *struct {
		ID             string   `json:"id"`
		RequiredFields []string `json:"required_fields"`
	}
	for i := range got {
		if got[i].ID == "sqlite" {
			sqlite = &got[i]
		}
	}
	if sqlite == nil {
		t.Fatal("sqlite is registered but was not reported")
	}
	// The whole point: the UI learns the requirement from the engine rather
	// than keeping its own copy that can disagree.
	if !slices.Contains(sqlite.RequiredFields, "file") {
		t.Errorf("required_fields = %v, want it to include file", sqlite.RequiredFields)
	}
}

// Adversarial: required_fields must never marshal as null. The shell iterates
// it, and a driver with no requirements is a real case, not a hypothetical.
func TestDriversListMarshalsEmptyRequiredFieldsAsAnArray(t *testing.T) {
	// Unique per registration: driver.Register panics on a repeat id and
	// `go test -count=2` runs this twice in one process.
	id := fmt.Sprintf("needs-nothing#%d", noRequirementsSeq.Add(1))
	driver.Register(noRequirementsDriver{id: id})

	srv := rpc.NewServer()
	RegisterDrivers(srv)
	handler, _ := srv.Handler("drivers.list")
	res, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("drivers.list: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Assert on the JSON bytes, not the Go slice: nil and an empty slice are
	// the same length in Go and different documents on the wire, and it is the
	// wire the shell reads.
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, e := range entries {
		if string(e["id"]) == strconv.Quote(id) {
			found = true
			if got := string(e["required_fields"]); got != "[]" {
				t.Errorf("required_fields = %s, want []", got)
			}
		}
	}
	if !found {
		t.Fatalf("the driver registered as %s was not reported", id)
	}
}

var noRequirementsSeq atomic.Int64

type noRequirementsDriver struct{ id string }

func (d noRequirementsDriver) ID() string                                { return d.id }
func (d noRequirementsDriver) Capabilities() driver.Capabilities         { return driver.Capabilities{} }
func (d noRequirementsDriver) RequiredFields(driver.ConnConfig) []string { return nil }
func (d noRequirementsDriver) Open(context.Context, driver.ConnConfig) (driver.Conn, error) {
	return nil, errors.New("noRequirementsDriver does not open")
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/api/ -run TestDriversList -count=1`
Expected: FAIL, `drivers.list is not registered`.

- [ ] **Step 3: Implement it**

`internal/api/drivers.go`:

```go
// DriverInfo is what the shell needs to build a connection form without
// keeping its own copy of each driver's requirements. The duplication this
// replaces could only ever disagree: Go decides what a driver needs to dial,
// and a second list in TypeScript is a guess at that decision.
type DriverInfo struct {
	ID             string              `json:"id"`
	RequiredFields []string            `json:"required_fields"`
	Capabilities   driver.Capabilities `json:"capabilities"`
}
```

`RequiredFields` is asked of each driver with a zero `ConnConfig`, which is what "what does this driver need before it has anything" means. Normalize a nil result to an empty slice.

- [ ] **Step 4: Register it and run the tests**

Add `api.RegisterDrivers(srv)` to `cmd/engine/main.go` beside the other registrations. Run `go test ./internal/api/ ./cmd/engine/ -count=1` — expected PASS.

- [ ] **Step 5: Write the failing TypeScript test**

```tsx
it('builds the driver picker from the engine', async () => {
  driversMock.mockResolvedValue([
    { id: 'sqlite', required_fields: ['file'], capabilities: {} },
    { id: 'mysql', required_fields: ['host', 'user'], capabilities: {} },
  ]);
  render(<ConnectionDialog open onClose={() => {}} />);
  expect(await screen.findByRole('button', { name: /sqlite/i })).toBeDefined();
  expect(await screen.findByRole('button', { name: /mysql/i })).toBeDefined();
});

// Adversarial, and the reason this task exists: the dialog must refuse to save
// a connection missing a field THE ENGINE named — including one that appears
// in no TypeScript source anywhere. A hardcoded check cannot pass this test,
// which is exactly why it is the one worth writing.
it('refuses to save when a field the engine requires is empty', async () => {
  driversMock.mockResolvedValue([
    { id: 'sqlite', required_fields: ['file', 'wildcard'], capabilities: {} },
  ]);
  render(<ConnectionDialog open onClose={() => {}} />);
  await screen.findByRole('button', { name: /sqlite/i });

  fireEvent.change(screen.getByLabelText(/^name$/i), { target: { value: 'local' } });
  fireEvent.change(screen.getByLabelText(/file/i), { target: { value: '/tmp/a.db' } });
  // 'wildcard' is deliberately left unset.
  await act(async () => { screen.getByRole('button', { name: /save/i }).click(); });

  expect(saveMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent?.toLowerCase()).toContain('wildcard');
});
```

- [ ] **Step 6: Run it and watch it fail**

Run: `npm run test -- src/components/ConnectionDialog.test.tsx`
Expected: FAIL — the dialog renders hardcoded buttons.

- [ ] **Step 7: Implement it**

`src/lib/connections.ts`:

```ts
export interface DriverInfo {
  id: string;
  required_fields: string[];
  capabilities: Capabilities;
}

export const listDrivers = () => request<DriverInfo[]>('drivers.list');
```

In `ConnectionDialog.tsx`, fetch once when the dialog opens (it already has an `open` effect that resets state — extend it, do not replace it, and keep the focus handling intact). Build the driver buttons from the response, and replace `missingDriverField()` with a check against the selected driver's `required_fields`.

Delete `missingDriverField`. Leaving it as dead code preserves the disagreement this task removes.

Handle the fetch failing: the dialog must still render, with the error shown through `<ErrorText>` and saving disabled — a dialog that renders an empty driver list with no explanation is the blank-screen failure again.

- [ ] **Step 8: Run the gate and look at it**

Run: `npm run typecheck && npm run test:coverage` — 100% on all four metrics.

Then build and open the dialog in the running app. Confirm the driver buttons appear, that saving without a file is refused, and that the refusal names the field.

- [ ] **Step 9: Commit**

```bash
git add internal/ cmd/ src/
git commit -m "feat: build the connection form from the engine's driver list"
```

---

## Deferred, with triggers

Recorded so they land with the work that makes them matter rather than being rediscovered.

- **`schema.Column` carries no unique-index data**, only `PrimaryKey bool`. SQLite does not care, because a rowid always exists. A MySQL table whose only unique key is a `UNIQUE NOT NULL` index cannot be keyset-planned until index metadata lands. **Trigger:** the MySQL driver's `planOrder`.
- **A mid-page fallback from keyset to offset.** Sorting on a column that turns out to hold a value no cursor can carry currently errors. Plan-time fallbacks already exist for views and blob keys; this one is only detectable at value time. **Trigger:** anyone hitting it, or the query tab making arbitrary result sets common.
- **`ATTACH DATABASE` on a read-only connection** succeeds and creates a file, though writing through it is correctly refused. Unreachable while nothing feeds arbitrary SQL to a connection. **Trigger:** the query tab.
- **A per-column encoding override.** A latin-1 TEXT column renders as bytes, because SQLite defines TEXT as UTF-8 and treating invalid bytes as text is what made a cursor loop. Real clients offer an override. **Trigger:** a user with latin-1 data.
- **`EngineStatus` does not render through `ErrorText`.** Cosmetic inconsistency, not a defect. **Trigger:** a third error surface.
- **The grid bundles `marked` and `react-responsive-carousel`** because `DataEditorAll` wires every renderer. An explicit renderer list drops both. **Trigger:** the editor landing, when the needed cell kinds are known.
- **Re-pin the grid** when a stable `6.0.4` ships with React 19 support. It is an exact-pinned pre-release today.

## Verification that needs a human

Neither is closeable from an agent session: the test harness webview is Chromium and the real one is WKWebView.

- Open a table in the built `Lantern.app`, scroll, and watch the Web Inspector console for CSP reports.
- Press ⌘C on a grid cell. Glide copies through `navigator.clipboard.write`, which needs a secure context and a real user gesture.
