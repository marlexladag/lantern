import { it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act, fireEvent } from '@testing-library/react';

// `asDbError` is a pure, dependency-free helper, so — like
// EngineStatus.test.tsx does for `toEngineError` — it is kept real via
// importActual rather than stubbed. The brief's own mock (bare stubs for the
// four IPC-crossing functions) is a subset of this and remains compatible;
// this version additionally lets the Sidebar's asDbError-based failure
// handling (required by the brief's prose, and by the "shows the engine
// message" test below) be exercised and covered.
vi.mock('../lib/connections', async () => {
  const actual = await vi.importActual<typeof import('../lib/connections')>('../lib/connections');
  return {
    ...actual,
    listConnections: vi.fn(),
    openSession: vi.fn(),
    loadColumns: vi.fn(),
    closeSession: vi.fn(),
  };
});

import { listConnections, openSession, loadColumns, closeSession } from '../lib/connections';
import { Sidebar, ADD_CONNECTION_EVENT, CONNECTIONS_CHANGED_EVENT } from './Sidebar';

const listMock = vi.mocked(listConnections);
const openMock = vi.mocked(openSession);
const columnsMock = vi.mocked(loadColumns);
const closeMock = vi.mocked(closeSession);

const openResult = {
  session_id: 's1',
  catalog: { databases: [{ name: 'main', tables: [{ name: 'users', kind: 'table' as const }] }] },
  capabilities: { transactions: true, multiple_databases: false, editable_rows: true },
};

const oneColumn = [
  { name: 'id', data_type: 'INTEGER', nullable: false, primary_key: true, position: 0 },
];

const conn = {
  id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db',
  color: '#3d7d55', read_only: false,
};

beforeEach(() => {
  listMock.mockReset();
  openMock.mockReset();
  columnsMock.mockReset();
  closeMock.mockReset();
  closeMock.mockResolvedValue({ closed: true });
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

it('collapses and re-expands an already-open connection without re-opening the session', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  await act(async () => { screen.getByText('local').click(); }); // collapse
  expect(screen.queryByText('users')).toBeNull();

  await act(async () => { screen.getByText('local').click(); }); // re-expand
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  expect(openMock).toHaveBeenCalledTimes(1);
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

it('caches a table’s columns after the first load and never refetches on repeated expand/collapse', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);
  columnsMock.mockResolvedValue(oneColumn);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(screen.getByText('id')).toBeDefined());
  expect(columnsMock).toHaveBeenCalledTimes(1);

  // Collapse — the columns disappear, but the fetch is not repeated.
  await act(async () => { screen.getByText('users').click(); });
  expect(screen.queryByText('id')).toBeNull();

  // Expand again — still no second fetch.
  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(screen.getByText('id')).toBeDefined());
  expect(columnsMock).toHaveBeenCalledTimes(1);
});

it('retries session.open after a prior failure instead of getting stuck', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockRejectedValueOnce({
    code: -32020, message: 'boom', data: { kind: 'unknown', message: 'disk error' },
  });
  openMock.mockResolvedValueOnce(openResult);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText(/disk error/i)).toBeDefined());

  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  expect(openMock).toHaveBeenCalledTimes(2);
});

it('renders nothing for a canceled session.open and leaves the row retryable', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockRejectedValueOnce({
    code: -32020, message: 'canceled', data: { kind: 'canceled', message: 'canceled' },
  });
  openMock.mockResolvedValueOnce(openResult);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(openMock).toHaveBeenCalledTimes(1));

  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.queryByText('users')).toBeNull();

  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
});

it('shows the engine message inline when loading columns fails', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);
  columnsMock.mockRejectedValue({
    code: -32020, message: 'boom', data: { kind: 'unknown', message: 'table is locked' },
  });

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  await act(async () => { screen.getByText('users').click(); });

  await waitFor(() => expect(screen.getByText(/table is locked/i)).toBeDefined());
});

it('renders nothing for a canceled loadColumns and leaves the table collapsed, not stuck loading', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);
  columnsMock.mockRejectedValueOnce({
    code: -32020, message: 'canceled', data: { kind: 'canceled', message: 'canceled' },
  });
  columnsMock.mockResolvedValueOnce(oneColumn);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(columnsMock).toHaveBeenCalledTimes(1));
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.queryByText('id')).toBeNull();
  expect(screen.queryByText(/loading columns/i)).toBeNull();

  // Retryable: expanding again issues a fresh fetch, since the canceled one
  // never populated the cache.
  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(screen.getByText('id')).toBeDefined());
  expect(columnsMock).toHaveBeenCalledTimes(2);
});

// Every other fixture in this file has exactly one table, and that
// uniformity is what hid the blanking bug: `.map` on a database with no
// tables is fine, `.map` on a null one takes the whole window out, and
// neither case had a fixture.
it('renders an explicit empty state for a database with no tables', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    ...openResult,
    catalog: { databases: [{ name: 'main', tables: [] }] },
  });

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  // Not nothing: a database that renders blank is indistinguishable from one
  // that failed to load.
  await waitFor(() => expect(screen.getByText(/no tables/i)).toBeDefined());
});

// The exact wire shape: Go's encoding/json writes a nil slice as `null`,
// which arrives here as null however confidently `Catalog` declares
// `tables: Table[]`. There is no error boundary in App.tsx, so `.map` on it
// blanks the whole window.
it('survives a database whose tables arrive as null on the wire', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    ...openResult,
    catalog: { databases: [{ name: 'main', tables: null as unknown as [] }] },
  });

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  await waitFor(() => expect(screen.getByText(/no tables/i)).toBeDefined());
  // Still a live sidebar, not a blank window.
  expect(screen.getByText('local')).toBeDefined();
});

it('survives a catalog whose databases arrive as null on the wire', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue({
    ...openResult,
    catalog: { databases: null as unknown as [] },
  });

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  await waitFor(() => expect(screen.getByText(/no tables/i)).toBeDefined());
  expect(screen.getByText('local')).toBeDefined();
});

it('shows the engine message inline when listConnections fails', async () => {
  listMock.mockRejectedValue({
    code: -32020, message: 'boom', data: { kind: 'unknown', message: 'config file is corrupt' },
  });
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText(/config file is corrupt/i)).toBeDefined());
});

it('renders nothing for a canceled listConnections beyond the empty state', async () => {
  listMock.mockRejectedValue({
    code: -32020, message: 'canceled', data: { kind: 'canceled', message: 'canceled' },
  });
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText(/no connections/i)).toBeDefined());
  expect(screen.queryByRole('alert')).toBeNull();
});

// The engine's `message` is engine-neutral and its `native` is the driver's
// own text (spec §11). The sidebar must surface the second on demand, not
// swallow it — nothing in src/ rendered `native` at all before this.
it('discloses the driver text behind a collapsed Details control', async () => {
  listMock.mockRejectedValue({
    code: -32020,
    message: 'boom',
    data: {
      kind: 'unknown',
      message: 'the database reported an error',
      native: 'disk I/O error (SQLITE_IOERR)',
    },
  });
  render(<Sidebar />);

  await screen.findByText('the database reported an error');
  const details = document.querySelector('details') as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(screen.getByText('disk I/O error (SQLITE_IOERR)')).toBeDefined();
});

it('shows a lock glyph for a read-only connection', async () => {
  listMock.mockResolvedValue([{ ...conn, id: 'a2', name: 'prod', color: '#9e4436', read_only: true }]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('prod')).toBeDefined());
  expect(screen.getByLabelText(/read only/i)).toBeDefined();
});

it('renders no lock glyph for a non-read-only connection', async () => {
  listMock.mockResolvedValue([conn]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  expect(screen.queryByLabelText(/read only/i)).toBeNull();
});

it('dispatches a request to add a connection from the empty state', async () => {
  listMock.mockResolvedValue([]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText(/no connections/i)).toBeDefined());

  const handler = vi.fn();
  window.addEventListener(ADD_CONNECTION_EVENT, handler);
  fireEvent.click(screen.getByRole('button', { name: /add connection/i }));
  window.removeEventListener(ADD_CONNECTION_EVENT, handler);

  expect(handler).toHaveBeenCalledTimes(1);
});

it('refetches the list when notified that connections changed elsewhere', async () => {
  listMock.mockResolvedValueOnce([]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText(/no connections/i)).toBeDefined());

  listMock.mockResolvedValueOnce([conn]);
  await act(async () => { window.dispatchEvent(new CustomEvent(CONNECTIONS_CHANGED_EVENT)); });

  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  expect(listMock).toHaveBeenCalledTimes(2);
});

it('closes every open session on unmount so the engine is not left holding a handle', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);

  const { unmount } = render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  unmount();

  expect(closeMock).toHaveBeenCalledWith('s1');
});

it('does not call closeSession on unmount when no session was ever opened', async () => {
  listMock.mockResolvedValue([conn]);
  const { unmount } = render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());

  unmount();

  expect(closeMock).not.toHaveBeenCalled();
});

it('moves the roving selection with ArrowDown/ArrowUp, clamping at both ends, and Enter activates the focused row', async () => {
  const other = { ...conn, id: 'a2', name: 'other' };
  listMock.mockResolvedValue([conn, other]);
  openMock.mockResolvedValue(openResult);
  columnsMock.mockResolvedValue(oneColumn);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());

  const tree = screen.getByRole('tree');
  const localRow = screen.getByText('local').closest('[role="treeitem"]') as HTMLElement;
  const otherRow = screen.getByText('other').closest('[role="treeitem"]') as HTMLElement;

  // Enter on the initially-active first row opens its session.
  await act(async () => { fireEvent.keyDown(tree, { key: 'Enter' }); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  expect(openMock).toHaveBeenCalledWith('a1');

  // Move onto the newly-revealed table row and expand it with Enter.
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowDown' }); });
  const usersRow = screen.getByText('users').closest('[role="treeitem"]') as HTMLElement;
  expect(document.activeElement).toBe(usersRow);

  await act(async () => { fireEvent.keyDown(tree, { key: 'Enter' }); });
  await waitFor(() => expect(screen.getByText('id')).toBeDefined());
  expect(columnsMock).toHaveBeenCalledWith('s1', 'main', 'users');

  // Continue down onto the second connection, then clamp at the bottom.
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowDown' }); });
  expect(document.activeElement).toBe(otherRow);
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowDown' }); });
  expect(document.activeElement).toBe(otherRow);

  // ArrowUp walks back; clamp at the top.
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowUp' }); });
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowUp' }); });
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowUp' }); });
  expect(document.activeElement).toBe(localRow);
});

it('ignores a key on the tree that is not an arrow or Enter', async () => {
  listMock.mockResolvedValue([conn]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());

  fireEvent.keyDown(screen.getByRole('tree'), { key: 'a' });

  expect(openMock).not.toHaveBeenCalled();
});

it('does not open a second session for a connection while the first open is still in flight', async () => {
  let resolveOpen: ((r: typeof openResult) => void) | undefined;
  listMock.mockResolvedValue([conn]);
  openMock.mockReturnValue(new Promise((resolve) => { resolveOpen = resolve; }));

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await act(async () => { screen.getByText('local').click(); }); // a real click, no `disabled` to stop it

  await act(async () => { resolveOpen?.(openResult); });
  expect(openMock).toHaveBeenCalledTimes(1);
});

it('does not start a second columns fetch for a table while the first is still in flight', async () => {
  let resolveColumns: ((c: typeof oneColumn) => void) | undefined;
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);
  columnsMock.mockReturnValue(new Promise((resolve) => { resolveColumns = resolve; }));

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  await act(async () => { screen.getByText('users').click(); });
  await act(async () => { screen.getByText('users').click(); });

  await act(async () => { resolveColumns?.(oneColumn); });
  expect(columnsMock).toHaveBeenCalledTimes(1);
});

// The three non-database rejections below use the shape `request()` can
// actually produce: an OBJECT (the engine's own EngineError, or the NoIpc
// literal), never a bare string. A string fixture passes whether or not the
// code handles the real shape — which is exactly how every one of these
// surfaces came to render `[object Object]` in production.
const ENGINE_DIED = { code: -32002, message: 'the engine died' };
const NO_IPC = {
  code: -32006,
  message: 'Lantern must be opened as the desktop app — this page has no connection to the engine in a browser tab.',
};

it('renders the engine message when session.open rejects with a non-database engine error', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockRejectedValue(ENGINE_DIED);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toBe('the engine died');
  expect(alert.textContent).not.toContain('[object');
});

it('renders the engine message when loadColumns rejects with a non-database engine error', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);
  columnsMock.mockRejectedValue(ENGINE_DIED);

  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  await act(async () => { screen.getByText('users').click(); });

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toBe('the engine died');
  expect(alert.textContent).not.toContain('[object');
});

it('renders the engine message when listConnections rejects with a non-database engine error', async () => {
  listMock.mockRejectedValue(ENGINE_DIED);
  render(<Sidebar />);

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toBe('the engine died');
  expect(alert.textContent).not.toContain('[object');
});

// The other object request() throws: the literal it raises before invoke()
// when there is no Tauri bridge at all. Its whole point is a message a
// person can act on, which `String(err)` destroyed.
it('renders the no-IPC-bridge message rather than [object Object]', async () => {
  listMock.mockRejectedValue(NO_IPC);
  render(<Sidebar />);

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toMatch(/must be opened as the desktop app/);
  expect(alert.textContent).not.toContain('[object');
});

it('swallows a rejection from closeSession on unmount instead of crashing', async () => {
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(openResult);
  closeMock.mockRejectedValueOnce(new Error('already gone'));

  const { unmount } = render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());

  expect(() => unmount()).not.toThrow();
  // Let the rejected closeSession promise settle inside its own .catch so
  // it does not surface as an unhandled rejection after the test ends.
  await act(async () => { await Promise.resolve(); });
});

it('clears the active row when the connection list empties out after a refetch', async () => {
  listMock.mockResolvedValueOnce([conn]);
  render(<Sidebar />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());

  listMock.mockResolvedValueOnce([]);
  await act(async () => { window.dispatchEvent(new CustomEvent(CONNECTIONS_CHANGED_EVENT)); });

  await waitFor(() => expect(screen.getByText(/no connections/i)).toBeDefined());
});
