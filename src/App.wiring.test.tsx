/**
 * The wiring, end to end inside the app: a real Sidebar, a real App, and a
 * real ResultGrid, with only the two seams that leave the process stubbed —
 * `lib/connections` and `lib/browse` — plus glide's canvas, which jsdom
 * cannot host at all.
 *
 * `App.test.tsx` stubs both components and tests that App passes the right
 * props; `Sidebar.test.tsx` and `ResultGrid.test.tsx` test each side on its
 * own. None of those can fail if the three are wired together wrongly, which
 * is precisely what this task is. Hence a fourth file rather than a bigger
 * version of one of the three: a `vi.mock` is per-file and hoisted, so the
 * real Sidebar and the stubbed one cannot coexist in App.test.tsx.
 *
 * What this file still does NOT prove, and the report says so too: nothing
 * here paints. The DataEditor stand-in below projects the component's own
 * `getCellContent` into DOM, so the assertions read what the canvas WOULD
 * draw — not that a pixel was ever lit, and not that the grid has a non-zero
 * height, which is the exact failure `.app-main`'s centring used to cause.
 * jsdom performs no layout; that one was measured in a real browser.
 */
import { it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, act, fireEvent } from '@testing-library/react';

vi.mock('./components/EngineStatus', () => ({
  EngineStatus: () => <div data-testid="engine-status" />,
}));

const harness = vi.hoisted(() => ({ props: undefined as Record<string, any> | undefined }));

// Only DataEditor. Everything else stays glide's own, so GridCellKind cannot
// drift from a copy — the same arrangement ResultGrid.test.tsx documents.
vi.mock('@glideapps/glide-data-grid', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('@glideapps/glide-data-grid');
  return {
    ...actual,
    DataEditor: (props: any) => {
      harness.props = props;
      const cells = [];
      for (let row = 0; row < props.rows; row++) {
        for (let col = 0; col < props.columns.length; col++) {
          cells.push(
            <div key={`${col}.${row}`} data-testid={`cell-${col}-${row}`}>
              {props.getCellContent([col, row]).displayData}
            </div>,
          );
        }
      }
      return (
        <div data-testid="data-editor">
          {props.columns.map((c: any) => (
            <div key={c.id} data-testid={`header-${c.id}`}>{c.title}</div>
          ))}
          {cells}
        </div>
      );
    },
  };
});

vi.mock('./lib/connections', async () => {
  const actual = await vi.importActual<typeof import('./lib/connections')>('./lib/connections');
  return {
    ...actual,
    listConnections: vi.fn(),
    openSession: vi.fn(),
    loadTables: vi.fn(),
    loadColumns: vi.fn(),
    closeSession: vi.fn(),
  };
});

vi.mock('./lib/browse', async () => {
  const actual = await vi.importActual<typeof import('./lib/browse')>('./lib/browse');
  return { ...actual, browsePage: vi.fn() };
});

import { listConnections, openSession, loadTables, loadColumns, closeSession } from './lib/connections';
import { browsePage, type BrowsePage, type Value } from './lib/browse';
import App from './App';

const listMock = vi.mocked(listConnections);
const openMock = vi.mocked(openSession);
const tablesMock = vi.mocked(loadTables);
const columnsMock = vi.mocked(loadColumns);
const closeMock = vi.mocked(closeSession);
const browseMock = vi.mocked(browsePage);

const conn = {
  id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db',
  color: '#3d7d55', read_only: false,
};

// session.open carries the DATABASE list alone; the tables arrive from
// session.tables when `main` is expanded (spec §5's two tiers).
const catalog = {
  session_id: 's1',
  catalog: { databases: [{ name: 'main' }] },
  capabilities: { transactions: true, multiple_databases: false, editable_rows: true },
};

const mainTables = [
  { name: 'users', kind: 'table' as const },
  { name: 'orders', kind: 'table' as const },
];

function v(kind: Value['kind'], text: string): Value {
  return { kind, text };
}

function page(over: Partial<BrowsePage> = {}): BrowsePage {
  return { columns: [{ name: 'id', data_type: 'INTEGER' }], rows: [], exhausted: true, offset: 0, ...over };
}

const usersPage = page({
  columns: [{ name: 'id', data_type: 'INTEGER' }, { name: 'email', data_type: 'TEXT' }],
  rows: [[v('int', '1041'), v('text', 'ama.osei@example.com')]],
});

const ordersPage = page({
  columns: [{ name: 'sku', data_type: 'TEXT' }],
  rows: [[v('text', 'A-1')]],
});

// The engine's own shape for a session that is no longer there
// (internal/api/session.go), carried as a JSON-RPC error data member.
const SESSION_GONE = {
  code: -32020,
  message: 'no open session with id s1',
  data: { kind: 'not_found', message: 'no open session with id s1' },
};

beforeEach(() => {
  listMock.mockReset();
  openMock.mockReset();
  tablesMock.mockReset();
  columnsMock.mockReset();
  closeMock.mockReset();
  browseMock.mockReset();
  closeMock.mockResolvedValue({ closed: true });
  columnsMock.mockResolvedValue([
    { name: 'id', data_type: 'INTEGER', nullable: false, primary_key: true, position: 0 },
  ]);
  listMock.mockResolvedValue([conn]);
  openMock.mockResolvedValue(catalog);
  tablesMock.mockResolvedValue(mainTables);
  harness.props = undefined;
  // jsdom implements no matchMedia; ResultGrid's colour-scheme listener needs
  // one to attach to. See ResultGrid.test.tsx for why the fake lives here.
  vi.stubGlobal(
    'matchMedia',
    vi.fn(() => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * Renders the app and walks both lazy tiers — the connection, then the `main`
 * database — leaving both tables visible.
 */
async function openConnection() {
  const view = render(<App />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());
  await act(async () => { screen.getByText('local').click(); });
  await act(async () => { (await screen.findByText('main')).click(); });
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  return view;
}

const rowOf = (name: string) => screen.getByText(name).closest('[role="treeitem"]') as HTMLElement;

// The headline: this is the whole task in one assertion.
it('shows a table’s rows in the main pane when the table is clicked', async () => {
  browseMock.mockResolvedValue(usersPage);
  await openConnection();

  expect(screen.getByText('Select a table to see its data.')).toBeDefined();

  await act(async () => { screen.getByText('users').click(); });

  await waitFor(() => expect(screen.getByTestId('cell-1-0').textContent).toBe('ama.osei@example.com'));
  expect(screen.getByTestId('header-id').textContent).toBe('id');
  expect(screen.queryByText('Select a table to see its data.')).toBeNull();
  expect(browseMock).toHaveBeenCalledWith('s1', expect.objectContaining({
    database: 'main', table: 'users', after: undefined,
  }));
  // ...and the sidebar says which table that is.
  expect(rowOf('users').getAttribute('aria-selected')).toBe('true');
});

// Spec §12 is keyboard-first. A grid only a mouse can summon is a grid half
// the spec cannot reach.
it('shows a table’s rows when the table is chosen from the keyboard', async () => {
  browseMock.mockResolvedValue(usersPage);
  render(<App />);
  await waitFor(() => expect(screen.getByText('local')).toBeDefined());

  const tree = screen.getByRole('tree');
  await act(async () => { fireEvent.keyDown(tree, { key: 'Enter' }); }); // the connection
  await waitFor(() => expect(screen.getByText('main')).toBeDefined());
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowDown' }); });
  await act(async () => { fireEvent.keyDown(tree, { key: 'Enter' }); }); // the database
  await waitFor(() => expect(screen.getByText('users')).toBeDefined());
  act(() => { fireEvent.keyDown(tree, { key: 'ArrowDown' }); });
  await act(async () => { fireEvent.keyDown(tree, { key: 'Enter' }); }); // the table

  await waitFor(() => expect(screen.getByTestId('cell-1-0').textContent).toBe('ama.osei@example.com'));
});

// Adversarial: replaced, not appended, and not left stale. A grid that
// accumulates two tables' rows still "shows rows" to a shallow assertion.
it('replaces the first table’s rows when a second table is selected', async () => {
  browseMock.mockResolvedValueOnce(usersPage).mockResolvedValueOnce(ordersPage);
  await openConnection();

  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(screen.getByTestId('cell-1-0').textContent).toBe('ama.osei@example.com'));

  await act(async () => { screen.getByText('orders').click(); });

  await waitFor(() => expect(screen.getByTestId('header-sku')).toBeDefined());
  expect(screen.queryByTestId('header-email')).toBeNull();
  expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1');
  expect(screen.queryByTestId('cell-1-0')).toBeNull(); // one column now, not two
  expect(harness.props!.rows).toBe(1);
  expect(rowOf('orders').getAttribute('aria-selected')).toBe('true');
  expect(rowOf('users').getAttribute('aria-selected')).toBe('false');
});

// Adversarial: the slow first request. EngineStatus and ConnectionDialog both
// carry a generation guard for exactly this, and the dialog needed one after
// a first fix missed it.
it('drops a page that arrives for the table the user has already left', async () => {
  let landUsers: (p: BrowsePage) => void = () => {};
  browseMock
    .mockReturnValueOnce(new Promise<BrowsePage>((resolve) => (landUsers = resolve)))
    .mockResolvedValueOnce(ordersPage);

  await openConnection();
  await act(async () => { screen.getByText('users').click(); });
  await act(async () => { screen.getByText('orders').click(); });
  await waitFor(() => expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1'));

  await act(async () => { landUsers(usersPage); });

  expect(screen.getByTestId('header-sku')).toBeDefined();
  expect(screen.queryByTestId('header-email')).toBeNull();
  expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1');
  expect(harness.props!.rows).toBe(1);
});

// Adversarial: an empty table must say so. Drawing nothing is what a failure
// looks like.
it('says so when the selected table has no rows at all', async () => {
  browseMock.mockResolvedValue(page({ rows: [] }));
  await openConnection();

  await act(async () => { screen.getByText('users').click(); });

  await waitFor(() => expect(screen.getByRole('status').textContent).toBe('No rows'));
  // The shape of the table is still worth seeing.
  expect(screen.getByTestId('header-id')).toBeDefined();
});

// Adversarial: selecting the table already on screen. It must not refetch,
// and it must not blank what is there — App replaces the selection object
// every time, so this is proving the strings underneath it are what matter.
it('does not refetch when the same table is selected twice', async () => {
  browseMock.mockResolvedValue(usersPage);
  await openConnection();

  await act(async () => { screen.getByText('users').click(); });
  await waitFor(() => expect(screen.getByTestId('cell-0-0').textContent).toBe('1041'));

  await act(async () => { screen.getByText('users').click(); }); // collapses the columns

  expect(browseMock).toHaveBeenCalledTimes(1);
  expect(screen.getByTestId('cell-0-0').textContent).toBe('1041');
  expect(rowOf('users').getAttribute('aria-selected')).toBe('true');
});

// Adversarial: the session is gone by the time its table is asked for. The
// message is the engine's, never `[object Object]`, and the next table still
// works — the pane must not be poisoned by one failure.
it('shows the engine’s message when the session has gone, and recovers on the next table', async () => {
  browseMock.mockRejectedValueOnce(SESSION_GONE).mockResolvedValueOnce(ordersPage);
  await openConnection();

  await act(async () => { screen.getByText('users').click(); });

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toContain('no open session with id s1');
  expect(alert.textContent).not.toContain('[object');
  expect(screen.getByRole('button', { name: 'Retry' })).toBeDefined();

  await act(async () => { screen.getByText('orders').click(); });

  await waitFor(() => expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1'));
  expect(screen.queryByRole('alert')).toBeNull();
});
