import { useState } from 'react';
import { it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act, fireEvent } from '@testing-library/react';

// `asDbError` is a pure, dependency-free helper (no engine call inside it),
// so — like EngineStatus.test.tsx does for `toEngineError` — it is kept real
// via importActual rather than stubbed. Only the two functions that cross
// the IPC boundary are replaced. The brief's own mock factory (bare
// testConnection/saveConnection stubs) is a subset of this and remains
// compatible with it; this version additionally lets the dialog's own
// engine-rejection handling (which the brief's prose requires, via
// asDbError) be exercised and covered.
vi.mock('../lib/connections', async () => {
  const actual = await vi.importActual<typeof import('../lib/connections')>('../lib/connections');
  return {
    ...actual,
    testConnection: vi.fn(),
    saveConnection: vi.fn(),
  };
});

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
// the test would silently exercise an empty form.
function fill(name: string, file: string) {
  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: name } });
  fireEvent.change(screen.getByLabelText(/file/i), { target: { value: file } });
}

it('renders nothing when closed', () => {
  const { container } = render(<ConnectionDialog open={false} onClose={() => {}} onSaved={() => {}} />);
  expect(container.firstChild).toBeNull();
});

// A modal that opens without moving focus into it leaves Tab walking into
// whatever is visually behind it first.
it('moves focus to the Name field when it opens', () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  expect(document.activeElement).toBe(screen.getByLabelText(/name/i));
});

// A focus trap keeps Tab inside the dialog — without it, Tab from the last
// control escapes into the sidebar behind the (still open) dialog.
it('wraps Tab from the last focusable control to the first', () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  const dialog = screen.getByRole('dialog');
  const first = screen.getByRole('button', { name: /close/i });
  const last = screen.getByRole('button', { name: /^connect$/i });

  last.focus();
  fireEvent.keyDown(dialog, { key: 'Tab' });

  expect(document.activeElement).toBe(first);
});

it('wraps Shift+Tab from the first focusable control to the last', () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  const dialog = screen.getByRole('dialog');
  const first = screen.getByRole('button', { name: /close/i });
  const last = screen.getByRole('button', { name: /^connect$/i });

  first.focus();
  fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });

  expect(document.activeElement).toBe(last);
});

it('leaves a forward Tab alone when focus is not on the last control', () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  const dialog = screen.getByRole('dialog');
  const nameField = screen.getByLabelText(/name/i);

  nameField.focus();
  fireEvent.keyDown(dialog, { key: 'Tab' });

  // jsdom does not itself implement tab order, so an untrapped Tab leaves
  // focus exactly where it was — the trap only ever acts at the boundary.
  expect(document.activeElement).toBe(nameField);
});

it('leaves a backward Shift+Tab alone when focus is not on the first control', () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  const dialog = screen.getByRole('dialog');
  const nameField = screen.getByLabelText(/name/i);

  nameField.focus();
  fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });

  expect(document.activeElement).toBe(nameField);
});

// Harness matching how App.tsx actually wires the dialog up: a real
// trigger button, real open/close state. Focus restoration is tested here
// rather than against App's own suite (which stubs ConnectionDialog out)
// so it exercises the dialog's real close paths.
function AddConnectionHarness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Add connection
      </button>
      <ConnectionDialog open={open} onClose={() => setOpen(false)} onSaved={() => setOpen(false)} />
    </>
  );
}

it('returns focus to the element that opened the dialog after Escape, Cancel, or the close button', () => {
  render(<AddConnectionHarness />);
  const opener = screen.getByRole('button', { name: /add connection/i });

  // Escape, Cancel, and the × button — three different close paths, one
  // shared restore-focus effect.
  const closeActions: Array<() => void> = [
    () => fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' }),
    () => fireEvent.click(screen.getByRole('button', { name: /^cancel$/i })),
    () => fireEvent.click(screen.getByRole('button', { name: /close/i })),
  ];

  for (const close of closeActions) {
    opener.focus();
    fireEvent.click(opener);
    expect(document.activeElement).toBe(screen.getByLabelText(/name/i));

    close();
    expect(document.activeElement).toBe(opener);
  }
});

it('returns focus to the element that opened the dialog after a successful save', async () => {
  const stored = { id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
  saveMock.mockResolvedValue(stored);
  render(<AddConnectionHarness />);
  const opener = screen.getByRole('button', { name: /add connection/i });

  opener.focus();
  fireEvent.click(opener);
  fill('local', '/tmp/a.db');

  await act(async () => {
    screen.getByRole('button', { name: /^connect$/i }).click();
  });

  await waitFor(() => expect(document.activeElement).toBe(opener));
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

// User-found: a connection with a Name and no File used to save
// successfully, and only failed much later, in the sidebar, as "no database
// file given". SQLite cannot dial without a File, so Connect must refuse
// before ever calling saveConnection — the same way it already refuses a
// missing Name.
it('refuses to save without a file', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  expect(saveMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent).toMatch(/file is required/i);
});

// Test Connection round-trips to the engine; a request that cannot possibly
// succeed should never be sent in the first place.
it('refuses to test a connection without a file, without calling testConnection', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  expect(testMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent).toMatch(/file is required/i);
});

it('falls back to a generic message when a failed test carries no error text', async () => {
  testMock.mockResolvedValue({ ok: false });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  await waitFor(() => expect(screen.getByText(/connection failed/i)).toBeDefined());
});

// The engine call itself can reject (a shell failure, not a {ok:false} result).
// Test Connection must never let that escape as an unhandled throw.
it('reports an engine-level rejection from Test Connection using the db error message', async () => {
  testMock.mockRejectedValue({
    code: -32020,
    message: 'boom',
    data: { kind: 'unknown', message: 'the engine went sideways' },
  });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  await waitFor(() => expect(screen.getByText(/the engine went sideways/i)).toBeDefined());
});

// A rejection that is not a recognizable DbError still has to render
// something a person can read. The fixture is an OBJECT because that is the
// only thing `request()` can reject with — the engine's own EngineError, or
// the NoIpc literal. A bare-string fixture passes whether or not the code
// handles the real shape, which is how `String(err)` came to render
// `[object Object]` for every shell failure in production.
const ENGINE_DIED = { code: -32002, message: 'the engine died' };
const NO_IPC = {
  code: -32006,
  message: 'Lantern must be opened as the desktop app — this page has no connection to the engine in a browser tab.',
};

it('renders the engine message for a Test Connection rejection that is not a database error', async () => {
  testMock.mockRejectedValue(ENGINE_DIED);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  const result = await screen.findByText(/the engine died/);
  expect(result.textContent).not.toContain('[object');
});

// The other object request() throws: the literal it raises before invoke()
// when there is no Tauri bridge at all. Its whole point is a message a
// person can act on, which `String(err)` destroyed.
it('renders the no-IPC-bridge message from Test Connection rather than [object Object]', async () => {
  testMock.mockRejectedValue(NO_IPC);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  const result = await screen.findByText(/must be opened as the desktop app/);
  expect(result.textContent).not.toContain('[object');
});

it('disables Test Connection while a test is in flight', async () => {
  let resolveTest: ((r: { ok: boolean }) => void) | undefined;
  testMock.mockReturnValue(new Promise((resolve) => { resolveTest = resolve; }));
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  const button = screen.getByRole('button', { name: /test connection/i });
  act(() => { button.click(); });
  expect((button as HTMLButtonElement).disabled).toBe(true);

  await act(async () => { resolveTest?.({ ok: true }); });
  expect((button as HTMLButtonElement).disabled).toBe(false);
});

it('does not start a second test while one is already running, because the button disables itself', async () => {
  let resolveTest: ((r: { ok: boolean }) => void) | undefined;
  testMock.mockReturnValue(new Promise((resolve) => { resolveTest = resolve; }));
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  const button = screen.getByRole('button', { name: /test connection/i });
  act(() => { button.click(); });
  act(() => { button.click(); }); // a disabled <button> never dispatches this click

  await act(async () => { resolveTest?.({ ok: true }); });
  expect(testMock).toHaveBeenCalledTimes(1);
});

// Cmd/Ctrl+Enter calls handleConnect() directly from a keydown handler,
// which does not go through the Connect button's `disabled` attribute — so
// the in-flight guard inside handleConnect is the only thing standing
// between a fast double chord and two concurrent saves.
it('ignores a second Cmd+Enter while a save from the first is still in flight', async () => {
  const stored = { id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
  let resolveSave: ((c: typeof stored) => void) | undefined;
  saveMock.mockReturnValue(new Promise((resolve) => { resolveSave = resolve; }));
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  const dialog = screen.getByRole('dialog');
  act(() => { fireEvent.keyDown(dialog, { key: 'Enter', metaKey: true }); });
  act(() => { fireEvent.keyDown(dialog, { key: 'Enter', metaKey: true }); });

  await act(async () => {
    resolveSave?.(stored);
  });
  expect(saveMock).toHaveBeenCalledTimes(1);
});

// A rejected save is information, not a crash — same principle as Test Connection.
it('reports a failed save using the db error message and does not call onSaved', async () => {
  saveMock.mockRejectedValue({
    code: -32020,
    message: 'boom',
    data: { kind: 'unknown', message: 'disk is full' },
  });
  const onSaved = vi.fn();
  render(<ConnectionDialog open onClose={() => {}} onSaved={onSaved} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/disk is full/i));
  expect(onSaved).not.toHaveBeenCalled();
});

it('renders the engine message for a save rejection that is not a database error', async () => {
  saveMock.mockRejectedValue(ENGINE_DIED);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('the engine died'));
  expect(screen.getByRole('alert').textContent).not.toContain('[object');
});

it('renders the no-IPC-bridge message from a save rather than [object Object]', async () => {
  saveMock.mockRejectedValue(NO_IPC);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/must be opened as the desktop app/));
  expect(screen.getByRole('alert').textContent).not.toContain('[object');
});

it('disables Connect while a save is in flight and ignores a second click', async () => {
  let resolveSave: ((c: typeof stored) => void) | undefined;
  const stored = { id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
  saveMock.mockReturnValue(new Promise((resolve) => { resolveSave = resolve; }));
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  const button = screen.getByRole('button', { name: /^connect$/i });
  act(() => { button.click(); });
  expect((button as HTMLButtonElement).disabled).toBe(true);
  act(() => { button.click(); });

  await act(async () => { resolveSave?.(stored); });
  expect(saveMock).toHaveBeenCalledTimes(1);
});

it('lets a swatch be picked explicitly, overriding the default colour', async () => {
  saveMock.mockResolvedValue({
    id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#24707a', read_only: false,
  });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  const teal = screen.getByRole('button', { name: 'Colour #24707a' });
  fireEvent.click(teal);
  expect(teal.getAttribute('aria-pressed')).toBe('true');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  const [connection] = saveMock.mock.calls[0];
  expect(connection.color).toBe('#24707a');
});

it('toggling Production forces the danger colour and read-only, and reverts when toggled off', async () => {
  saveMock.mockResolvedValue({
    id: 'a1', name: 'prod', driver: 'sqlite', file: '/tmp/p.db', color: '#9e4436', read_only: true,
  });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('prod', '/tmp/p.db');

  const toggle = screen.getByRole('switch', { name: /production connection/i });
  expect(toggle.getAttribute('aria-checked')).toBe('false');
  expect(screen.queryByText(/new sessions on this connection open read-only/i)).toBeNull();

  fireEvent.click(toggle);
  expect(toggle.getAttribute('aria-checked')).toBe('true');
  expect(screen.getByText(/new sessions on this connection open read-only/i)).toBeDefined();

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });
  const [connection] = saveMock.mock.calls[0];
  expect(connection.color).toBe('#9e4436');
  expect(connection.read_only).toBe(true);

  // Toggle back off: colour reverts to the default and the note disappears.
  fireEvent.click(toggle);
  expect(toggle.getAttribute('aria-checked')).toBe('false');
  expect(screen.queryByText(/new sessions on this connection open read-only/i)).toBeNull();
});

it('calls onClose when Escape is pressed inside the dialog', () => {
  const onClose = vi.fn();
  render(<ConnectionDialog open onClose={onClose} onSaved={() => {}} />);

  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });

  expect(onClose).toHaveBeenCalledTimes(1);
});

it('calls onClose when the close button or Cancel is clicked', () => {
  const onClose = vi.fn();
  render(<ConnectionDialog open onClose={onClose} onSaved={() => {}} />);

  fireEvent.click(screen.getByRole('button', { name: /close/i }));
  fireEvent.click(screen.getByRole('button', { name: /^cancel$/i }));

  expect(onClose).toHaveBeenCalledTimes(2);
});

it('triggers Connect on Cmd+Enter and on Ctrl+Enter', async () => {
  const stored = { id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
  saveMock.mockResolvedValue(stored);
  const onSaved = vi.fn();
  render(<ConnectionDialog open onClose={() => {}} onSaved={onSaved} />);
  fill('local', '/tmp/a.db');

  await act(async () => {
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Enter', metaKey: true });
  });
  await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));

  await act(async () => {
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Enter', ctrlKey: true });
  });
  await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(2));
});

it('ignores a bare Enter with no modifier, and any other key', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  fill('local', '/tmp/a.db');

  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Enter' });
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'a' });

  expect(saveMock).not.toHaveBeenCalled();
});

it('renders MySQL and MariaDB as disabled, and SQLite as the only selectable driver', () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);

  expect((screen.getByRole('button', { name: 'MySQL' }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole('button', { name: 'MariaDB' }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'SQLite' }).getAttribute('aria-pressed')).toBe('true');
});
