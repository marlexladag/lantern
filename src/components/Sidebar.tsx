import {
  Fragment,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
} from 'react';
import {
  closeSession,
  listConnections,
  loadColumns,
  loadTables,
  openSession,
  type Catalog,
  type Column,
  type Connection,
  type Table,
} from '../lib/connections';
import { describeError, type ErrorDescription } from '../lib/errors';
import { ErrorText } from './ErrorText';
import './Sidebar.css';

/**
 * Dispatched when the user asks to add a connection from the sidebar's
 * empty-state affordance. App.tsx listens for this to open the single
 * ConnectionDialog it owns — Sidebar takes no props and needs none to
 * request that.
 */
export const ADD_CONNECTION_EVENT = 'lantern:add-connection';

/**
 * Dispatched after a connection is saved elsewhere (the dialog App owns).
 * Sidebar listens for this to refetch its list, so adding a connection from
 * the app's toolbar is reflected here without prop drilling or a shared
 * store.
 */
export const CONNECTIONS_CHANGED_EVENT = 'lantern:connections-changed';

type SessionState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; error: ErrorDescription }
  | {
      status: 'open';
      sessionId: string;
      catalog: Catalog;
      /**
       * `Capabilities.MultipleDatabases`, straight off session.open.
       *
       * It decides the SHAPE of the tree below this connection, not merely
       * what to grey out: a driver with one database (SQLite always, and it
       * is always called `main`) gets its tables hung directly off the
       * connection, because a node with no siblings is a tier the user has
       * to open to get past. A driver with several gets the database row,
       * which is the only thing that can tell three identically-named
       * `users` tables apart.
       */
      multipleDatabases: boolean;
      expanded: boolean;
    };

type OpenSession = Extract<SessionState, { status: 'open' }>;

interface DatabaseUiState {
  expanded: boolean;
  loading: boolean;
  tables?: Table[];
  error?: ErrorDescription;
}

interface TableUiState {
  expanded: boolean;
  loading: boolean;
  columns?: Column[];
  error?: ErrorDescription;
}

/**
 * Both caches are keyed by the SESSION, never by the connection.
 *
 * A connection reopened after a failure — or after a collapse that closed
 * it — is a different session against a database that may have been altered
 * in between, so a cache keyed by connection id would hand the new session
 * the old one's table and column lists and never refetch. The session id is
 * also what every RPC these caches feed is addressed to, so keying by it is
 * keying by the thing the data actually came from.
 *
 * Known and left alone: entries for a session that has since closed are
 * never evicted, so they live as long as the sidebar does. One small entry
 * per reopened connection, bounded by how many times a person reopens one in
 * a single run — but whoever gives this structure a reason to grow (a
 * refresh-schema affordance, a reconnect) should give it an eviction too.
 */
function databaseKey(sessionId: string, databaseName: string) {
  return `${sessionId}\x00${databaseName}`;
}

function tableKey(sessionId: string, databaseName: string, tableName: string) {
  return `${sessionId}\x00${databaseName}\x00${tableName}`;
}

/**
 * A tables list as it actually arrives, not as the type promises.
 *
 * `Table[]` is what `loadTables` declares, but Go's encoding/json writes a
 * nil slice as `null`, and that is exactly what reached this component once
 * before: there is no error boundary in App.tsx, so `.map` on it took the
 * whole window blank. The engine's driver contract now guarantees a non-nil
 * slice and internal/api deliberately does NOT re-normalize it — so that a
 * driver breaking that contract is visible rather than papered over. This is
 * the other side of that decision: the engine tells the truth, and the UI
 * still refuses to blank the window over it. Do not delete this because the
 * type says it cannot happen — that is precisely what was believed last time.
 */
function asTables(tables: Table[]): Table[] {
  return tables ?? [];
}

/**
 * How far in a line sits, as a tier index rather than a length.
 *
 * The ladder itself — where tier 0 starts and how wide a step is — lives in
 * Sidebar.css, which is where lengths belong. What the component knows is
 * that the tiers are relative: a table's tables-tier depends on whether its
 * database got a row of its own, so an element cannot carry a fixed class
 * per tier without carrying two of them.
 */
function indent(depth: number): CSSProperties {
  return { '--depth': depth } as CSSProperties;
}

type Row =
  | { kind: 'connection'; id: string; connection: Connection }
  | { kind: 'database'; id: string; sessionId: string; databaseName: string }
  | {
      kind: 'table';
      id: string;
      sessionId: string;
      databaseName: string;
      table: Table;
    };

/**
 * Which table's rows the main pane is showing.
 *
 * The session id is part of the identity, not decoration: two connections
 * can both hold a `main` database with a `users` table in it, and only the
 * session tells those two apart.
 */
export interface TableSelection {
  sessionId: string;
  database: string;
  table: string;
}

export interface SidebarProps {
  /**
   * Raised when the user activates a table — by click or by Enter, which
   * are the same activation here. Expanding a table and asking to see its
   * rows are one gesture (Main.dc.html marks the expanded table as the one
   * filling the pane), so this fires alongside the expand rather than from
   * a second affordance.
   *
   * Optional, so `<Sidebar />` stays mountable on its own the way the
   * event-based add-connection affordance already keeps it.
   */
  onSelectTable?: (sessionId: string, database: string, table: string) => void;
  /**
   * The selection to mark, handed back down by whoever holds it.
   *
   * Deliberately NOT a second copy kept inside this component. The selection
   * drives the main pane, so the pane's owner is the one thing that knows
   * what is actually on screen; a sidebar highlighting from its own copy is
   * how a highlight comes to point at a different table than the grid is
   * showing. This is also distinct from `activeRowId` below, which is the
   * roving tabIndex owner — arrow keys move that without selecting anything.
   */
  selectedTable?: TableSelection | null;
}

function LockIcon() {
  return (
    <svg viewBox="0 0 16 16" width="11" height="11" aria-hidden="true">
      <rect x="3.5" y="7" width="9" height="6" rx="1.2" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <path d="M5.6 7V5.4a2.4 2.4 0 0 1 4.8 0V7" fill="none" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

export function Sidebar({ onSelectTable, selectedTable }: SidebarProps) {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [listError, setListError] = useState<ErrorDescription | null>(null);
  const [sessions, setSessions] = useState<Record<string, SessionState>>({});
  const [databases, setDatabases] = useState<Record<string, DatabaseUiState>>({});
  const [tables, setTables] = useState<Record<string, TableUiState>>({});
  const [activeRowId, setActiveRowId] = useState<string | null>(null);

  // Read at unmount time by the session-cleanup effect below, which must not
  // re-run (and so must not close sessions early) every time `sessions`
  // itself changes.
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;
  const rowRefs = useRef(new Map<string, HTMLElement>());

  /**
   * Generation counters for the in-flight `session.tables` requests, one per
   * database rather than one for the whole tree.
   *
   * Same mechanism and same shape as EngineStatus's `generation` and
   * ConnectionDialog's: capture before awaiting, compare after, drop the
   * result if it no longer matches. A Map rather than a single number
   * because these requests are genuinely concurrent — a user can expand
   * three databases in a row — and one shared counter would make each expand
   * silently retire the previous one's still-wanted response.
   *
   * Collapsing a database bumps its counter. That is the case the connection
   * dialog got wrong on its first attempt: resetting state on open without
   * cancelling what was already dispatched just means the stale answer
   * arrives a moment later and lands on a view that has moved on.
   */
  const tableGenerations = useRef(new Map<string, number>());

  function nextGeneration(key: string) {
    const next = (tableGenerations.current.get(key) ?? 0) + 1;
    tableGenerations.current.set(key, next);
    return next;
  }

  async function reload() {
    try {
      const list = await listConnections();
      setConnections(list);
      setListError(null);
    } catch (err) {
      const described = describeError(err);
      // A canceled request is not a failure to report (spec §11).
      if (described.kind !== 'canceled') {
        setListError(described);
      }
    } finally {
      setLoaded(true);
    }
  }

  useEffect(() => {
    void reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const handler = () => void reload();
    window.addEventListener(CONNECTIONS_CHANGED_EVENT, handler);
    return () => window.removeEventListener(CONNECTIONS_CHANGED_EVENT, handler);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Close every still-open session on unmount, so the engine is not left
  // holding a database handle for a sidebar nobody can see anymore.
  useEffect(() => {
    return () => {
      for (const session of Object.values(sessionsRef.current)) {
        if (session.status === 'open') {
          // Best-effort: closeSession's return value is not awaited by
          // anything, and a test double that has not stubbed a resolved
          // value returns undefined rather than a Promise.
          void Promise.resolve(closeSession(session.sessionId)).catch(() => {});
        }
      }
    };
  }, []);

  const rows: Row[] = useMemo(() => {
    const out: Row[] = [];
    for (const connection of connections) {
      out.push({ kind: 'connection', id: connection.id, connection });
      const session = sessions[connection.id];
      if (session?.status !== 'open' || !session.expanded) continue;
      // `?? []` is deliberately defensive and is NOT redundant with the
      // types: `Catalog` declares this an array, and Go's encoding/json
      // writes a nil slice as `null`. See asTables above for the full story.
      for (const database of session.catalog.databases ?? []) {
        const dbKey = databaseKey(session.sessionId, database.name);
        // The database row exists only where there is a choice to make. This
        // list is what the keyboard walks, so it has to agree with the
        // render below exactly — a row here that nothing draws would be a
        // tabIndex owner with no element to focus.
        if (session.multipleDatabases) {
          out.push({
            kind: 'database',
            id: dbKey,
            sessionId: session.sessionId,
            databaseName: database.name,
          });
        }
        const dbUi = databases[dbKey];
        if (!dbUi?.expanded) continue;
        for (const table of dbUi.tables ?? []) {
          out.push({
            kind: 'table',
            id: tableKey(session.sessionId, database.name, table.name),
            sessionId: session.sessionId,
            databaseName: database.name,
            table,
          });
        }
      }
    }
    return out;
  }, [connections, sessions, databases]);

  // Roving tabIndex: keep the active row valid as the tree grows and
  // shrinks, defaulting to the first row once there is one.
  useEffect(() => {
    if (rows.length === 0) {
      if (activeRowId !== null) setActiveRowId(null);
      return;
    }
    if (!rows.some((row) => row.id === activeRowId)) {
      setActiveRowId(rows[0].id);
    }
    // This only decides which row *would* receive focus if the user tabs
    // into the tree (the roving tabIndex owner) — it must never call
    // .focus() itself, or loading the connection list would steal keyboard
    // focus from wherever the user actually is.
  }, [rows, activeRowId]);

  /**
   * For a driver with a single database, the connection row IS that
   * database's disclosure control — there is no database row to click — so
   * expanding the connection is what reads its tables. The name comes off
   * the catalog rather than being assumed to be `main`: that is SQLite's
   * detail, not the UI's.
   *
   * Only ever called for an expand. Collapsing deliberately does not retire
   * an in-flight read, unlike the database row's own collapse: there, an
   * answer landing on a node the user closed would re-open it, whereas here
   * the connection's collapse already hides everything and a late answer
   * just fills the cache the next expand will use.
   */
  function revealFlattenedTables(session: OpenSession) {
    if (session.multipleDatabases) return;
    for (const database of session.catalog.databases ?? []) {
      setDatabaseExpanded(session.sessionId, database.name, true);
    }
  }

  function toggleConnection(connection: Connection) {
    const existing = sessions[connection.id];
    if (existing?.status === 'open') {
      const expanded = !existing.expanded;
      setSessions((prev) => ({ ...prev, [connection.id]: { ...existing, expanded } }));
      if (expanded) revealFlattenedTables(existing);
      return;
    }
    if (existing?.status === 'loading') return;
    setSessions((prev) => ({ ...prev, [connection.id]: { status: 'loading' } }));
    openSession(connection.id)
      .then((result) => {
        const session: OpenSession = {
          status: 'open',
          sessionId: result.session_id,
          catalog: result.catalog,
          multipleDatabases: result.capabilities.multiple_databases,
          expanded: true,
        };
        setSessions((prev) => ({ ...prev, [connection.id]: session }));
        revealFlattenedTables(session);
      })
      .catch((err) => {
        const described = describeError(err);
        if (described.kind === 'canceled') {
          setSessions((prev) => ({ ...prev, [connection.id]: { status: 'idle' } }));
          return;
        }
        setSessions((prev) => ({
          ...prev,
          [connection.id]: { status: 'error', error: described },
        }));
      });
  }

  /**
   * Show or hide one database's tables, reading them the first time.
   *
   * Takes the state it must end in rather than toggling, because the two
   * callers know different things: the database row toggles, while a
   * connection expanding in the single-database shape only ever means
   * "show", and must stay idempotent — it fires on every expand.
   *
   * Collapsing while a read is in flight retires that read rather than
   * ignoring the click: a request nobody can still want is a request whose
   * answer must not land, and leaving it live is how a node the user closed
   * springs back open a second later.
   */
  function setDatabaseExpanded(sessionId: string, databaseName: string, expanded: boolean) {
    const key = databaseKey(sessionId, databaseName);
    const existing = databases[key];
    if (!expanded) {
      nextGeneration(key);
      setDatabases((prev) => ({
        ...prev,
        [key]: { ...existing, expanded: false, loading: false },
      }));
      return;
    }
    // A read is already running and will land expanded. Reachable from the
    // single-database shape alone: collapsing the connection leaves the read
    // alive, so a quick collapse-and-re-expand arrives here mid-flight, and
    // without this it would retire a perfectly good request to start an
    // identical one.
    if (existing?.loading) return;
    // Already read: flip visibility only, never refetch. An empty database
    // is `[]`, which is still "read" — the reason this tests the property
    // rather than the length.
    if (existing?.tables) {
      setDatabases((prev) => ({ ...prev, [key]: { ...existing, expanded: true } }));
      return;
    }
    const gen = nextGeneration(key);
    setDatabases((prev) => ({ ...prev, [key]: { expanded: true, loading: true } }));
    loadTables(sessionId, databaseName)
      .then((loaded) => {
        if (gen !== tableGenerations.current.get(key)) return;
        setDatabases((prev) => ({
          ...prev,
          [key]: { expanded: true, loading: false, tables: asTables(loaded) },
        }));
      })
      .catch((err) => {
        if (gen !== tableGenerations.current.get(key)) return;
        const described = describeError(err);
        if (described.kind === 'canceled') {
          setDatabases((prev) => ({ ...prev, [key]: { expanded: false, loading: false } }));
          return;
        }
        setDatabases((prev) => ({
          ...prev,
          [key]: { expanded: true, loading: false, error: described },
        }));
      });
  }

  function activateDatabase(sessionId: string, databaseName: string) {
    const key = databaseKey(sessionId, databaseName);
    setDatabaseExpanded(sessionId, databaseName, !databases[key]?.expanded);
  }

  // Takes `sessionId` from the caller rather than looking it up, because
  // every call site (a click on a rendered table row, or Enter on one via
  // the keyboard) only exists in the first place because that table's
  // session is already open — there is no code path that reaches this
  // function without one, so there is nothing to defend against here.
  function activateTable(sessionId: string, databaseName: string, table: Table) {
    // Raised FIRST, ahead of every early return below, because activating a
    // table always means "show me this one" even when it means nothing else:
    // the second click on an expanded table collapses its column list, and
    // if that click stopped selecting it would take the table's rows out of
    // the main pane at the same time — one gesture hiding two different
    // things. A click while the columns are still loading is the same story.
    onSelectTable?.(sessionId, databaseName, table.name);
    const key = tableKey(sessionId, databaseName, table.name);
    const existing = tables[key];
    // Already loaded: flip visibility only, never refetch.
    if (existing?.columns) {
      setTables((prev) => ({ ...prev, [key]: { ...existing, expanded: !existing.expanded } }));
      return;
    }
    if (existing?.loading) return;
    setTables((prev) => ({ ...prev, [key]: { expanded: true, loading: true } }));
    loadColumns(sessionId, databaseName, table.name)
      .then((columns) => {
        setTables((prev) => ({ ...prev, [key]: { expanded: true, loading: false, columns } }));
      })
      .catch((err) => {
        const described = describeError(err);
        if (described.kind === 'canceled') {
          setTables((prev) => ({ ...prev, [key]: { expanded: false, loading: false } }));
          return;
        }
        setTables((prev) => ({
          ...prev,
          [key]: { expanded: true, loading: false, error: described },
        }));
      });
  }

  function activateRow(row: Row) {
    if (row.kind === 'connection') toggleConnection(row.connection);
    else if (row.kind === 'database') activateDatabase(row.sessionId, row.databaseName);
    else activateTable(row.sessionId, row.databaseName, row.table);
  }

  function handleTreeKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    // This handler only exists on the <div role="tree"> that replaces the
    // empty state once `connections.length > 0`, and `rows` always has at
    // least one entry per connection — so `rows` is never empty here.
    // `Math.max` (rather than a not-found branch) folds a hypothetical
    // "activeRowId matches nothing" into "treat it as the first row"
    // without a distinct branch to test for a case that cannot occur.
    const current = Math.max(0, rows.findIndex((row) => row.id === activeRowId));
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      const next = rows[Math.min(rows.length - 1, current + 1)];
      setActiveRowId(next.id);
      rowRefs.current.get(next.id)?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      const prev = rows[Math.max(0, current - 1)];
      setActiveRowId(prev.id);
      rowRefs.current.get(prev.id)?.focus();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      activateRow(rows[current]);
    }
  }

  function requestAddConnection() {
    window.dispatchEvent(new CustomEvent(ADD_CONNECTION_EVENT));
  }

  function setRowRef(id: string, el: HTMLElement | null) {
    if (el) rowRefs.current.set(id, el);
    else rowRefs.current.delete(id);
  }
  /**
   * The tables and error lines that hang off ONE database.
   *
   * `depth` is the tier those tables sit at, and it differs between the two
   * shapes by exactly one step — the database row is drawn in one and absent
   * in the other. Passing it in is what lets this be a single renderer
   * instead of two that have to be kept in step.
   */
  function renderDatabaseBody(sessionId: string, databaseName: string, depth: number) {
    const dbUi = databases[databaseKey(sessionId, databaseName)];
    return (
      <>
        {dbUi?.loading && (
          <div className="sidebar-status" style={indent(depth)}>
            Loading tables…
          </div>
        )}
        {dbUi?.error && (
          <div role="alert" className="sidebar-error" style={indent(depth)}>
            <ErrorText description={dbUi.error} />
          </div>
        )}
        {dbUi?.expanded &&
          dbUi.tables &&
          // A database that has been read and holds nothing says so. A
          // database that draws blank is indistinguishable from one that
          // failed to load.
          (dbUi.tables.length === 0 ? (
            <div className="sidebar-status" style={indent(depth)}>
              No tables
            </div>
          ) : (
            dbUi.tables.map((table) => {
              const rowId = tableKey(sessionId, databaseName, table.name);
              const ui = tables[rowId];
              const isSelected =
                selectedTable?.sessionId === sessionId &&
                selectedTable.database === databaseName &&
                selectedTable.table === table.name;
              return (
                <div key={rowId}>
                  <div
                    role="treeitem"
                    aria-expanded={ui?.expanded ?? false}
                    // Only table rows carry this: a connection or database
                    // row is not something the main pane can show, so
                    // claiming it is unselected would be a state it does not
                    // have.
                    aria-selected={isSelected}
                    tabIndex={activeRowId === rowId ? 0 : -1}
                    ref={(el) => setRowRef(rowId, el)}
                    className={`sidebar-row${isSelected ? ' is-selected' : ''}`}
                    style={indent(depth)}
                    onClick={() => {
                      setActiveRowId(rowId);
                      activateTable(sessionId, databaseName, table);
                    }}
                  >
                    <span className={`sidebar-caret${ui?.expanded ? ' is-open' : ''}`}>
                      &#9656;
                    </span>
                    <span>{table.name}</span>
                    {/*
                      `Table.Kind` has crossed the wire since the first
                      driver, and this tree drew the name alone — so a view
                      and a table were the same row twice, and only the user
                      remembering which was which told them apart.

                      A word rather than a tint, and not aria-hidden: colour
                      alone is no distinction for someone who cannot see the
                      difference, and a row read aloud has to carry it too.
                      `kind` is compared against the one value that changes
                      the row, so a Kind this shell has never heard of draws
                      as an ordinary table rather than as a blank badge.
                    */}
                    {table.kind === 'view' && <span className="sidebar-kind">View</span>}
                  </div>
                  {ui?.loading && (
                    <div className="sidebar-status" style={indent(depth + 1)}>
                      Loading columns…
                    </div>
                  )}
                  {ui?.error && (
                    <div role="alert" className="sidebar-error" style={indent(depth + 1)}>
                      <ErrorText description={ui.error} />
                    </div>
                  )}
                  {ui?.expanded &&
                    ui.columns?.map((column) => (
                      <div key={column.name} className="sidebar-column" style={indent(depth + 1)}>
                        <span className="sidebar-column-name">{column.name}</span>
                        {column.primary_key && <span className="sidebar-pk">PK</span>}
                        <span className="sidebar-column-type">{column.data_type}</span>
                      </div>
                    ))}
                </div>
              );
            })
          ))}
      </>
    );
  }

  if (!loaded) {
    return (
      <div className="sidebar">
        <div className="sidebar-section">Connections</div>
      </div>
    );
  }

  return (
    <div className="sidebar">
      <div className="sidebar-section">Connections</div>
      {listError && (
        <div role="alert" className="sidebar-error" style={indent(1)}>
          <ErrorText description={listError} />
        </div>
      )}
      {connections.length === 0 ? (
        <div className="sidebar-empty">
          <span>No connections yet.</span>
          <button type="button" className="sidebar-add" onClick={requestAddConnection}>
            Add connection
          </button>
        </div>
      ) : (
        <div role="tree" aria-label="Connections" onKeyDown={handleTreeKeyDown}>
          {connections.map((connection) => {
            const session = sessions[connection.id];
            const isOpen = session?.status === 'open' && session.expanded;
            const catalogDatabases =
              session?.status === 'open' ? session.catalog.databases ?? [] : [];
            return (
              <div key={connection.id}>
                <div
                  role="treeitem"
                  aria-expanded={session?.status === 'open' ? isOpen : undefined}
                  tabIndex={activeRowId === connection.id ? 0 : -1}
                  ref={(el) => setRowRef(connection.id, el)}
                  className="sidebar-row"
                  style={indent(0)}
                  onClick={() => {
                    setActiveRowId(connection.id);
                    toggleConnection(connection);
                  }}
                >
                  <span className={`sidebar-caret${isOpen ? ' is-open' : ''}`}>&#9656;</span>
                  <span className="sidebar-dot" style={{ background: connection.color }} />
                  <span className="sidebar-name">{connection.name}</span>
                  <span className="sidebar-driver">{connection.driver}</span>
                  <span className="sidebar-spacer" />
                  {connection.read_only && (
                    <span className="sidebar-lock" aria-label="Read only">
                      <LockIcon />
                    </span>
                  )}
                </div>
                {session?.status === 'loading' && (
                  <div className="sidebar-status" style={indent(1)}>
                    Opening…
                  </div>
                )}
                {session?.status === 'error' && (
                  <div role="alert" className="sidebar-error" style={indent(1)}>
                    <ErrorText description={session.error} />
                  </div>
                )}
                {isOpen &&
                  session.status === 'open' &&
                  // An open connection with nothing under it says so. Silence
                  // here is indistinguishable from a catalog that failed to
                  // load, which is the same defect in a different disguise.
                  (catalogDatabases.length === 0 ? (
                    <div className="sidebar-status" style={indent(1)}>
                      No databases
                    </div>
                  ) : (
                    catalogDatabases.map((database) => {
                      const dbKey = databaseKey(session.sessionId, database.name);
                      // One database, and the driver says so: the tables take
                      // the tier the database row would have occupied. See
                      // SessionState.multipleDatabases for why this is a shape
                      // decision rather than a cosmetic one.
                      if (!session.multipleDatabases) {
                        return (
                          <Fragment key={dbKey}>
                            {renderDatabaseBody(session.sessionId, database.name, 1)}
                          </Fragment>
                        );
                      }
                      return (
                        <div key={dbKey}>
                          <div
                            role="treeitem"
                            aria-expanded={databases[dbKey]?.expanded ?? false}
                            tabIndex={activeRowId === dbKey ? 0 : -1}
                            ref={(el) => setRowRef(dbKey, el)}
                            className="sidebar-row is-db"
                            style={indent(1)}
                            onClick={() => {
                              setActiveRowId(dbKey);
                              activateDatabase(session.sessionId, database.name);
                            }}
                          >
                            <span
                              className={`sidebar-caret${databases[dbKey]?.expanded ? ' is-open' : ''}`}
                            >
                              &#9656;
                            </span>
                            <span>{database.name}</span>
                          </div>
                          {renderDatabaseBody(session.sessionId, database.name, 2)}
                        </div>
                      );
                    })
                  ))}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
