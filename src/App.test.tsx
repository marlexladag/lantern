import { it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';

// Stubbed so this test is about the composition root, not about EngineStatus —
// which has its own suite and would otherwise drag the engine client in.
vi.mock('./components/EngineStatus', () => ({
  EngineStatus: () => <div data-testid="engine-status" />,
}));

// Sidebar has its own suite (including its own list-loading effects, which
// would otherwise drag ../lib/connections into this one). It is stubbed here
// down to the two things App actually depends on: that it mounts, and the
// two module-level event names App coordinates through.
vi.mock('./components/Sidebar', () => ({
  Sidebar: () => <div data-testid="sidebar" />,
  ADD_CONNECTION_EVENT: 'lantern:add-connection',
  CONNECTIONS_CHANGED_EVENT: 'lantern:connections-changed',
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
