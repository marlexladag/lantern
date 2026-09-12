import { it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';

// Stubbed so this test is about the composition root, not about EngineStatus —
// which has its own suite and would otherwise drag the engine client in.
vi.mock('./components/EngineStatus', () => ({
  EngineStatus: () => <div data-testid="engine-status" />,
}));

// Sidebar has its own suite (including its own list-loading effects, which
// would otherwise drag ../lib/connections into this one). It is stubbed here
// down to what App actually depends on: that it mounts, the two module-level
// event names App coordinates through, and the selection seam — two buttons
// standing in for two table rows, plus a readback of the selection App hands
// straight back down.
//
// The real Sidebar driven by real clicks against the real grid is the
// subject of App.wiring.test.tsx; this file stays a test of the composition
// root.
vi.mock('./components/Sidebar', () => ({
  Sidebar: ({
    onSelectTable,
    selectedTable,
  }: {
    onSelectTable: (sessionId: string, database: string, table: string) => void;
    selectedTable: { sessionId: string; database: string; table: string } | null;
  }) => (
    <div
      data-testid="sidebar"
      data-selected={selectedTable ? `${selectedTable.sessionId}/${selectedTable.database}/${selectedTable.table}` : ''}
    >
      <button onClick={() => onSelectTable('s1', 'main', 'users')}>select-users</button>
      <button onClick={() => onSelectTable('s2', 'shop', 'orders')}>select-orders</button>
      {/* The one table the grid stub below refuses to render. */}
      <button onClick={() => onSelectTable('s3', 'main', 'boom')}>select-boom</button>
    </div>
  ),
  ADD_CONNECTION_EVENT: 'lantern:add-connection',
  CONNECTIONS_CHANGED_EVENT: 'lantern:connections-changed',
}));

// The grid draws to a canvas jsdom cannot provide, and has its own suite for
// what it does with a page. Stubbed down to the three props App is
// responsible for choosing.
vi.mock('./components/ResultGrid', () => ({
  ResultGrid: ({
    sessionId,
    database,
    table,
  }: {
    sessionId: string;
    database: string;
    table: string;
  }) => {
    // A render-time throw on demand, from a real child of App's own tree.
    // What the fallback SAYS is ErrorBoundary's own suite; what this proves
    // is that App is the thing standing between a child like this one and a
    // blank window.
    if (table === 'boom') throw new TypeError('rows.map is not a function');
    return (
      <div data-testid="result-grid" data-session={sessionId} data-database={database} data-table={table} />
    );
  },
}));

// ConnectionDialog has its own suite too. Stubbed down to the props App
// wires up, with buttons that let a test drive onSaved/onClose without
// touching the real form.
vi.mock('./components/ConnectionDialog', () => ({
  ConnectionDialog: ({
    open,
    onClose,
    onSaved,
  }: {
    open: boolean;
    onClose: () => void;
    onSaved: (c: unknown) => void;
  }) => (
    <div data-testid="connection-dialog" data-open={open}>
      <button onClick={onClose}>close-stub</button>
      <button onClick={() => onSaved({ id: 'a1' })}>save-stub</button>
    </div>
  ),
}));

import App from './App';
import { ADD_CONNECTION_EVENT, CONNECTIONS_CHANGED_EVENT } from './components/Sidebar';

it('renders the product name and mounts the engine status and sidebar', () => {
  render(<App />);
  expect(screen.getByRole('heading', { name: 'Lantern' })).toBeDefined();
  expect(screen.getByTestId('engine-status')).toBeDefined();
  expect(screen.getByTestId('sidebar')).toBeDefined();
});

it('keeps the connection dialog closed until Add connection is clicked', () => {
  render(<App />);
  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('false');

  fireEvent.click(screen.getByRole('button', { name: /add connection/i }));
  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('true');
});

it('closes the dialog when it reports onClose', () => {
  render(<App />);
  fireEvent.click(screen.getByRole('button', { name: /add connection/i }));
  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('true');

  fireEvent.click(screen.getByText('close-stub'));
  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('false');
});

it('closes the dialog and announces the new connection when it reports onSaved', () => {
  const handler = vi.fn();
  window.addEventListener(CONNECTIONS_CHANGED_EVENT, handler);

  render(<App />);
  fireEvent.click(screen.getByRole('button', { name: /add connection/i }));
  fireEvent.click(screen.getByText('save-stub'));

  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('false');
  expect(handler).toHaveBeenCalledTimes(1);

  window.removeEventListener(CONNECTIONS_CHANGED_EVENT, handler);
});

it('opens the dialog when the sidebar requests one via ADD_CONNECTION_EVENT', () => {
  render(<App />);
  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('false');

  fireEvent(window, new CustomEvent(ADD_CONNECTION_EVENT));

  expect(screen.getByTestId('connection-dialog').dataset.open).toBe('true');
});

it('stops listening for ADD_CONNECTION_EVENT after unmounting', () => {
  const { unmount } = render(<App />);
  unmount();

  // If the listener were still attached, this would throw trying to call
  // setState on an unmounted component instead of doing nothing quietly.
  expect(() => fireEvent(window, new CustomEvent(ADD_CONNECTION_EVENT))).not.toThrow();
});

const PLACEHOLDER = 'Select a table to see its data.';

it('shows the placeholder until a table is selected', () => {
  render(<App />);
  expect(screen.getByText(PLACEHOLDER)).toBeDefined();
  expect(screen.queryByTestId('result-grid')).toBeNull();
  expect(screen.getByTestId('sidebar').dataset.selected).toBe('');
});

it('replaces the placeholder with the grid for the table the sidebar selected', () => {
  render(<App />);
  fireEvent.click(screen.getByText('select-users'));

  const grid = screen.getByTestId('result-grid');
  expect(grid.dataset.session).toBe('s1');
  expect(grid.dataset.database).toBe('main');
  expect(grid.dataset.table).toBe('users');
  expect(screen.queryByText(PLACEHOLDER)).toBeNull();
});

// Replaced, not accumulated: one main pane, one table.
it('replaces the grid when a second table is selected', () => {
  render(<App />);
  fireEvent.click(screen.getByText('select-users'));
  fireEvent.click(screen.getByText('select-orders'));

  const grids = screen.getAllByTestId('result-grid');
  expect(grids.length).toBe(1);
  expect(grids[0].dataset.session).toBe('s2');
  expect(grids[0].dataset.database).toBe('shop');
  expect(grids[0].dataset.table).toBe('orders');
});

// The sidebar draws its highlight from this rather than from a copy of its
// own, so the marked row and the grid can never name different tables.
it('hands the selection back to the sidebar', () => {
  render(<App />);
  fireEvent.click(screen.getByText('select-orders'));

  expect(screen.getByTestId('sidebar').dataset.selected).toBe('s2/shop/orders');
});

/*
 * A regression pin, and it is worth saying exactly what it does and does not
 * prove. `.app-main` used to centre its content on both axes; a flex item
 * under that is sized to its own content, so the grid — which asks for the
 * height of its container and fills it — collapsed to nothing. It rendered,
 * occupied zero pixels, and looked like the click had done nothing at all.
 *
 * jsdom performs no layout, so this asserts the cascaded VALUE, not the
 * resulting height: it catches the centring coming back, and nothing more.
 * The height itself was measured in a real browser against the built bundle
 * (see the task report).
 */
it('lets the main pane stretch its content instead of centring it to content size', () => {
  render(<App />);
  const main = document.querySelector('.app-main') as HTMLElement;
  expect(getComputedStyle(main).alignItems).toBe('stretch');

  // The centring did hold something up: the placeholder was centred by it.
  // It has to keep being centred by something of its own.
  const empty = screen.getByText(PLACEHOLDER);
  expect(getComputedStyle(empty).alignItems).toBe('center');
  expect(getComputedStyle(empty).justifyContent).toBe('center');
});

/*
 * E2-1. This project has blanked its own window twice from a render-time
 * throw, and both times the type said the value could not be there. React
 * unmounts the whole tree for any of them, so the boundary is what decides
 * whether the next one is a readable failure or a white rectangle.
 */
it('shows a readable failure instead of blanking the window when a child throws', () => {
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  render(<App />);

  fireEvent.click(screen.getByText('select-boom'));

  expect(screen.getByRole('alert').textContent).toMatch(/rows\.map is not a function/);
  expect(screen.getByRole('button', { name: /reload/i })).toBeDefined();
  consoleError.mockRestore();
});
