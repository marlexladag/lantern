import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import {
  closeSession,
  listConnections,
  loadColumns,
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
  | { status: 'open'; sessionId: string; catalog: Catalog; expanded: boolean };

interface TableUiState {
  expanded: boolean;
  loading: boolean;
  columns?: Column[];
  error?: ErrorDescription;
}

function tableKey(connectionId: string, databaseName: string, tableName: string) {
  return `${connectionId}\x00${databaseName}\x00${tableName}`;
}

/**
 * Every table in a catalog, flattened, carrying the database it came from.
 *
 * The two `?? []` guards are deliberately defensive and are NOT redundant
 * with the types: `Catalog` declares both of these as arrays, but Go's
 * encoding/json writes a nil slice as `null`, and that is exactly what the
 * engine sent for a database with no user tables. There is no error boundary
 * in App.tsx, so `.map` on one of those took the whole window blank. The
 * engine has since been fixed to send `[]`, which is why this guard stays:
 * the contract has now been proven not to enforce itself, and this is the
 * seam it crosses. Do not delete these because the type says they cannot
 * happen — that is precisely what was believed last time.
 */
function flattenTables(catalog: Catalog): { databaseName: string; table: Table }[] {
  const out: { databaseName: string; table: Table }[] = [];
  for (const database of catalog.databases ?? []) {
    for (const table of database.tables ?? []) {
      out.push({ databaseName: database.name, table });
    }
  }
  return out;
}

type Row =
  | { kind: 'connection'; id: string; connection: Connection }
  | {
      kind: 'table';
      id: string;
      sessionId: string;
      connectionId: string;
      databaseName: string;
      table: Table;
    };

function LockIcon() {
  return (
    <svg viewBox="0 0 16 16" width="11" height="11" aria-hidden="true">
      <rect x="3.5" y="7" width="9" height="6" rx="1.2" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <path d="M5.6 7V5.4a2.4 2.4 0 0 1 4.8 0V7" fill="none" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

export function Sidebar() {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [listError, setListError] = useState<ErrorDescription | null>(null);
  const [sessions, setSessions] = useState<Record<string, SessionState>>({});
  const [tables, setTables] = useState<Record<string, TableUiState>>({});
  const [activeRowId, setActiveRowId] = useState<string | null>(null);

  // Read at unmount time by the session-cleanup effect below, which must not
  // re-run (and so must not close sessions early) every time `sessions`
  // itself changes.
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;
  const rowRefs = useRef(new Map<string, HTMLElement>());

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
      if (session?.status === 'open' && session.expanded) {
        for (const { databaseName, table } of flattenTables(session.catalog)) {
          out.push({
            kind: 'table',
            id: tableKey(connection.id, databaseName, table.name),
            sessionId: session.sessionId,
            connectionId: connection.id,
            databaseName,
            table,
          });
        }
      }
    }
    return out;
  }, [connections, sessions]);

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

  function toggleConnection(connection: Connection) {
    const existing = sessions[connection.id];
    if (existing?.status === 'open') {
      setSessions((prev) => ({
        ...prev,
        [connection.id]: { ...existing, expanded: !existing.expanded },
      }));
      return;
    }
    if (existing?.status === 'loading') return;
    setSessions((prev) => ({ ...prev, [connection.id]: { status: 'loading' } }));
    openSession(connection.id)
      .then((result) => {
        setSessions((prev) => ({
          ...prev,
          [connection.id]: {
            status: 'open',
            sessionId: result.session_id,
            catalog: result.catalog,
            expanded: true,
          },
        }));
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

  // Takes `sessionId` from the caller rather than looking it up, because
  // every call site (a click on a rendered table row, or Enter on one via
  // the keyboard) only exists in the first place because that table's
  // session is already open — there is no code path that reaches this
  // function without one, so there is nothing to defend against here.
  function toggleTable(sessionId: string, connectionId: string, databaseName: string, table: Table) {
    const key = tableKey(connectionId, databaseName, table.name);
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
    else toggleTable(row.sessionId, row.connectionId, row.databaseName, row.table);
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
        <div role="alert" className="sidebar-error">
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
            const catalogTables = session?.status === 'open' ? flattenTables(session.catalog) : [];
            return (
              <div key={connection.id}>
                <div
                  role="treeitem"
                  aria-expanded={session?.status === 'open' ? isOpen : undefined}
                  tabIndex={activeRowId === connection.id ? 0 : -1}
                  ref={(el) => setRowRef(connection.id, el)}
                  className="sidebar-row"
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
                {session?.status === 'loading' && <div className="sidebar-status">Opening…</div>}
                {session?.status === 'error' && (
                  <div role="alert" className="sidebar-error">
                    <ErrorText description={session.error} />
                  </div>
                )}
                {isOpen &&
                  session.status === 'open' &&
                  // An open connection with nothing under it says so. Silence
                  // here is indistinguishable from a catalog that failed to
                  // load, which is the same defect in a different disguise.
                  (catalogTables.length === 0 ? (
                    <div className="sidebar-status">No tables</div>
                  ) : (
                    catalogTables.map(({ databaseName, table }) => {
                      const key = tableKey(connection.id, databaseName, table.name);
                      const ui = tables[key];
                      const rowId = key;
                      return (
                        <div key={rowId}>
                          <div
                            role="treeitem"
                            aria-expanded={ui?.expanded ?? false}
                            tabIndex={activeRowId === rowId ? 0 : -1}
                            ref={(el) => setRowRef(rowId, el)}
                            className="sidebar-row is-table"
                            onClick={() => {
                              setActiveRowId(rowId);
                              toggleTable(session.sessionId, connection.id, databaseName, table);
                            }}
                          >
                            <span className={`sidebar-caret${ui?.expanded ? ' is-open' : ''}`}>&#9656;</span>
                            <span>{table.name}</span>
                          </div>
                          {ui?.loading && <div className="sidebar-status">Loading columns…</div>}
                          {ui?.error && (
                            <div role="alert" className="sidebar-error">
                              <ErrorText description={ui.error} />
                            </div>
                          )}
                          {ui?.expanded &&
                            ui.columns?.map((column) => (
                              <div key={column.name} className="sidebar-column">
                                <span className="sidebar-column-name">{column.name}</span>
                                {column.primary_key && <span className="sidebar-pk">PK</span>}
                                <span className="sidebar-column-type">{column.data_type}</span>
                              </div>
                            ))}
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
