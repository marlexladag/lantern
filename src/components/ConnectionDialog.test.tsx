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
    listDrivers: vi.fn(),
  };
});

// Mocked one layer deeper than the block above, and only for the adversarial
// payload fixtures at the end of this file: those drive a raw drivers.list
// answer through the REAL listDrivers, so what the dialog receives is what
// validation let through rather than what a stub decided to hand it.
vi.mock('../lib/engine', async () => {
  const actual = await vi.importActual<typeof import('../lib/engine')>('../lib/engine');
  return { ...actual, request: vi.fn() };
});

import { testConnection, saveConnection, listDrivers, type Connection, type DriverInfo } from '../lib/connections';
import { request } from '../lib/engine';
import { ConnectionDialog } from './ConnectionDialog';

// The real thing, validation included. The module mock above replaces
// listDrivers for every other test here; `engineAnswers` below puts this one
// back for the tests whose subject IS the validation.
const { listDrivers: validatedListDrivers } =
  await vi.importActual<typeof import('../lib/connections')>('../lib/connections');

const testMock = vi.mocked(testConnection);
const saveMock = vi.mocked(saveConnection);
const driversMock = vi.mocked(listDrivers);
const requestMock = vi.mocked(request);

/*
 * The engine's answer, as the dialog now receives it. Every test that opens
 * the dialog gets this one unless it says otherwise, because the form no
 * longer has fields of its own to render: `file` is here, and not in
 * ConnectionDialog.tsx, which is the whole point of the task.
 */
const SQLITE: DriverInfo = {
  id: 'sqlite',
  required_fields: ['file'],
  capabilities: { transactions: true, multiple_databases: false, editable_rows: true },
};
const MYSQL: DriverInfo = {
  id: 'mysql',
  required_fields: ['host', 'user'],
  capabilities: { transactions: true, multiple_databases: true, editable_rows: true },
};

beforeEach(() => {
  testMock.mockReset();
  saveMock.mockReset();
  driversMock.mockReset();
  requestMock.mockReset();
  driversMock.mockResolvedValue([SQLITE]);
});

/** Resolves once the driver list has landed and the picker is drawn. */
const driversLoaded = () => screen.findByRole('button', { name: 'SQLite' });

// Use fireEvent.change, NOT `input.value = x`. React overrides the value
// setter on controlled inputs, so a direct assignment never fires onChange and
// the test would silently exercise an empty form.
async function fill(name: string, file: string) {
  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: name } });
  // findBy, not getBy: the File input exists because SQLite's required_fields
  // named it, so it appears only once drivers.list has answered.
  fireEvent.change(await screen.findByLabelText(/file/i), { target: { value: file } });
}

it('renders nothing when closed', () => {
  const { container } = render(<ConnectionDialog open={false} onClose={() => {}} onSaved={() => {}} />);
  expect(container.firstChild).toBeNull();
});

// A modal that opens without moving focus into it leaves Tab walking into
// whatever is visually behind it first.
it('moves focus to the Name field when it opens', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  // Before the driver list has even landed: focus is the dialog's own job,
  // not something that waits on the engine.
  expect(document.activeElement).toBe(screen.getByLabelText(/name/i));
  await driversLoaded();
});

// A focus trap keeps Tab inside the dialog — without it, Tab from the last
// control escapes into the sidebar behind the (still open) dialog.
it('wraps Tab from the last focusable control to the first', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
  const dialog = screen.getByRole('dialog');
  const first = screen.getByRole('button', { name: /close/i });
  const last = screen.getByRole('button', { name: /^connect$/i });

  last.focus();
  fireEvent.keyDown(dialog, { key: 'Tab' });

  expect(document.activeElement).toBe(first);
});

it('wraps Shift+Tab from the first focusable control to the last', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
  const dialog = screen.getByRole('dialog');
  const first = screen.getByRole('button', { name: /close/i });
  const last = screen.getByRole('button', { name: /^connect$/i });

  first.focus();
  fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });

  expect(document.activeElement).toBe(last);
});

it('leaves a forward Tab alone when focus is not on the last control', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
  const dialog = screen.getByRole('dialog');
  const nameField = screen.getByLabelText(/name/i);

  nameField.focus();
  fireEvent.keyDown(dialog, { key: 'Tab' });

  // jsdom does not itself implement tab order, so an untrapped Tab leaves
  // focus exactly where it was — the trap only ever acts at the boundary.
  expect(document.activeElement).toBe(nameField);
});

it('leaves a backward Shift+Tab alone when focus is not on the first control', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
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

it('returns focus to the element that opened the dialog after Escape, Cancel, or the close button', async () => {
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
    await driversLoaded();

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
  await fill('local', '/tmp/a.db');

  await act(async () => {
    screen.getByRole('button', { name: /^connect$/i }).click();
  });

  await waitFor(() => expect(document.activeElement).toBe(opener));
});

// App.tsx keeps this component mounted for the life of the app and only
// flips `open`, so every piece of state survives a close. Reopening therefore
// used to show the last connection's name and file — and, worse, the previous
// "Reachable" verdict, which is now asserting reachability about a file the
// user is in the middle of replacing.
it('resets the form and the stale test verdict when it is reopened', async () => {
  testMock.mockResolvedValue({ ok: true });
  saveMock.mockResolvedValue({
    id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#9e4436', read_only: true,
  });
  render(<AddConnectionHarness />);
  const opener = screen.getByRole('button', { name: /add connection/i });

  fireEvent.click(opener);
  await fill('local', '/tmp/a.db');
  fireEvent.click(screen.getByRole('switch', { name: /production connection/i }));

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });
  await screen.findByText(/reachable/i);

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

  fireEvent.click(opener);
  await driversLoaded();

  expect((screen.getByLabelText(/name/i) as HTMLInputElement).value).toBe('');
  expect((screen.getByLabelText(/file/i) as HTMLInputElement).value).toBe('');
  // The assertion that matters: a verdict about the previous file must not
  // be standing over a blank form.
  expect(screen.queryByText(/reachable/i)).toBeNull();
  // Colour and the production flag come back to their defaults too, so the
  // next connection does not inherit a red, read-only one.
  expect(screen.getByRole('switch', { name: /production connection/i }).getAttribute('aria-checked')).toBe('false');
  expect(screen.getByRole('button', { name: 'Colour #3d7d55' }).getAttribute('aria-pressed')).toBe('true');
});

it('clears a validation error when it is reopened', async () => {
  render(<AddConnectionHarness />);
  const opener = screen.getByRole('button', { name: /add connection/i });

  fireEvent.click(opener);
  await driversLoaded();
  act(() => { screen.getByRole('button', { name: /^connect$/i }).click(); });
  expect(screen.getByRole('alert').textContent).toMatch(/name is required/i);

  fireEvent.click(screen.getByRole('button', { name: /^cancel$/i }));
  fireEvent.click(opener);
  await driversLoaded();

  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.getByLabelText(/name/i).getAttribute('aria-invalid')).toBeNull();
});

it('reports a successful test inline', async () => {
  testMock.mockResolvedValue({ ok: true });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  await waitFor(() => expect(screen.getByText(/reachable/i)).toBeDefined());
});

// A failed test is information, not a crash.
it('reports a failed test with the engine message', async () => {
  testMock.mockResolvedValue({ ok: false, kind: 'not_found', error: 'database file does not exist' });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/nope.db');

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
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(onSaved).toHaveBeenCalledWith(stored));
  const [connection, password] = saveMock.mock.calls[0];
  expect(connection.name).toBe('local');
  expect(connection.driver).toBe('sqlite');
  expect(password).toBe('');
});

it('refuses to save without a name', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('', '/tmp/a.db');

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
  await fill('local', '');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  expect(saveMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent).toMatch(/file is required/i);
});

// Test Connection round-trips to the engine; a request that cannot possibly
// succeed should never be sent in the first place.
it('refuses to test a connection without a file, without calling testConnection', async () => {
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  expect(testMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent).toMatch(/file is required/i);
});

it('falls back to a generic message when a failed test carries no error text', async () => {
  testMock.mockResolvedValue({ ok: false });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });

  const result = await screen.findByText(/must be opened as the desktop app/);
  expect(result.textContent).not.toContain('[object');
});

it('disables Test Connection while a test is in flight', async () => {
  let resolveTest: ((r: { ok: boolean }) => void) | undefined;
  testMock.mockReturnValue(new Promise((resolve) => { resolveTest = resolve; }));
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/disk is full/i));
  expect(onSaved).not.toHaveBeenCalled();
});

it('renders the engine message for a save rejection that is not a database error', async () => {
  saveMock.mockRejectedValue(ENGINE_DIED);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('the engine died'));
  expect(screen.getByRole('alert').textContent).not.toContain('[object');
});

it('renders the no-IPC-bridge message from a save rather than [object Object]', async () => {
  saveMock.mockRejectedValue(NO_IPC);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/must be opened as the desktop app/));
  expect(screen.getByRole('alert').textContent).not.toContain('[object');
});

// Same disclosure as the sidebar's, at the dialog's own failure surface.
it('discloses the driver text behind a collapsed Details control on a failed save', async () => {
  saveMock.mockRejectedValue({
    code: -32020,
    message: 'boom',
    data: {
      kind: 'unknown',
      message: 'the database reported an error',
      native: 'attempt to write a readonly database',
    },
  });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  await screen.findByText('the database reported an error');
  const details = document.querySelector('details') as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(screen.getByText('attempt to write a readonly database')).toBeDefined();
});

it('disables Connect while a save is in flight and ignores a second click', async () => {
  let resolveSave: ((c: typeof stored) => void) | undefined;
  const stored = { id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
  saveMock.mockReturnValue(new Promise((resolve) => { resolveSave = resolve; }));
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

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
  await fill('prod', '/tmp/p.db');

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

it('calls onClose when Escape is pressed inside the dialog', async () => {
  const onClose = vi.fn();
  render(<ConnectionDialog open onClose={onClose} onSaved={() => {}} />);
  await driversLoaded();

  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });

  expect(onClose).toHaveBeenCalledTimes(1);
});

it('calls onClose when the close button or Cancel is clicked', async () => {
  const onClose = vi.fn();
  render(<ConnectionDialog open onClose={onClose} onSaved={() => {}} />);
  await driversLoaded();

  fireEvent.click(screen.getByRole('button', { name: /close/i }));
  fireEvent.click(screen.getByRole('button', { name: /^cancel$/i }));

  expect(onClose).toHaveBeenCalledTimes(2);
});

it('triggers Connect on Cmd+Enter and on Ctrl+Enter', async () => {
  const stored = { id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
  saveMock.mockResolvedValue(stored);
  const onSaved = vi.fn();
  render(<ConnectionDialog open onClose={() => {}} onSaved={onSaved} />);
  await fill('local', '/tmp/a.db');

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
  await fill('local', '/tmp/a.db');

  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Enter' });
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'a' });

  expect(saveMock).not.toHaveBeenCalled();
});

/*
 * The driver picker, the fields, and what counts as missing all come from
 * drivers.list now. The test this replaces asserted the opposite — two
 * hardcoded, permanently disabled buttons — which is what made registering a
 * driver in Go a change to this file as well.
 */
it('builds the driver picker from the engine', async () => {
  driversMock.mockResolvedValue([SQLITE, MYSQL]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);

  expect(await screen.findByRole('button', { name: /sqlite/i })).toBeDefined();
  expect(await screen.findByRole('button', { name: /mysql/i })).toBeDefined();
  // The first the engine reported is selected, so the form is usable the
  // moment it is drawn.
  expect(screen.getByRole('button', { name: /sqlite/i }).getAttribute('aria-pressed')).toBe('true');
});

// Picking a driver swaps the form to that driver's own requirements. Nothing
// here knows what a MySQL connection needs; the engine said host and user.
it('renders the fields the picked driver requires, and only those', async () => {
  driversMock.mockResolvedValue([SQLITE, MYSQL]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
  expect(screen.queryByLabelText(/host/i)).toBeNull();

  fireEvent.click(screen.getByRole('button', { name: /mysql/i }));

  expect(screen.getByLabelText(/host/i)).toBeDefined();
  expect(screen.getByLabelText(/user/i)).toBeDefined();
  expect(screen.queryByLabelText(/file/i)).toBeNull();
  expect(screen.getByRole('button', { name: /mysql/i }).getAttribute('aria-pressed')).toBe('true');
  expect(screen.getByRole('button', { name: /sqlite/i }).getAttribute('aria-pressed')).toBe('false');
});

// A verdict about a SQLite file is not a verdict about a MySQL host. Same
// reasoning as the reset-on-reopen effect, at a boundary the user crosses
// without closing anything.
it('drops a reachability verdict when another driver is picked', async () => {
  driversMock.mockResolvedValue([SQLITE, MYSQL]);
  testMock.mockResolvedValue({ ok: true });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await fill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });
  await screen.findByText(/reachable/i);

  fireEvent.click(screen.getByRole('button', { name: /mysql/i }));

  expect(screen.queryByText(/reachable/i)).toBeNull();
});

// A connection saves with what the engine named, under the engine's own
// names — no mapping table in here to drift out of step with ConnConfig.
it('sends each engine-named field under that name', async () => {
  driversMock.mockResolvedValue([SQLITE, MYSQL]);
  saveMock.mockResolvedValue({
    id: 'a1', name: 'prod-db', driver: 'mysql', host: 'db.internal', user: 'root',
    color: '#3d7d55', read_only: false,
  });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
  fireEvent.click(screen.getByRole('button', { name: /mysql/i }));

  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'prod-db' } });
  fireEvent.change(screen.getByLabelText(/host/i), { target: { value: ' db.internal ' } });
  fireEvent.change(screen.getByLabelText(/user/i), { target: { value: 'root' } });
  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  const [connection] = saveMock.mock.calls[0];
  expect(connection.driver).toBe('mysql');
  expect(connection.host).toBe('db.internal');
  expect(connection.user).toBe('root');
  expect(connection.file).toBeUndefined();
});

/*
 * Adversarial, and the reason this task exists: the dialog must refuse to
 * save a connection missing a field THE ENGINE named — including one that
 * appears in no TypeScript source anywhere. A hardcoded check cannot pass
 * this test, which is exactly why it is the one worth writing.
 *
 * The concrete case behind it is MySQL: Go will say it needs host AND user,
 * and a dialog checking only host would save a connection that can never
 * dial — the same defect a user already reported here for SQLite and File.
 */
it('refuses to save when a field the engine requires is empty', async () => {
  driversMock.mockResolvedValue([{ ...SQLITE, required_fields: ['file', 'wildcard'] }]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();

  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'local' } });
  fireEvent.change(screen.getByLabelText(/file/i), { target: { value: '/tmp/a.db' } });
  // 'wildcard' is deliberately left unset.
  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  expect(saveMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent?.toLowerCase()).toContain('wildcard');
  expect(screen.getByLabelText(/wildcard/i).getAttribute('aria-invalid')).toBe('true');
});

// Every missing field at once, named the way the engine names them in its
// own refusal, so the two surfaces read identically.
it('names every missing field, not just the first', async () => {
  driversMock.mockResolvedValue([SQLITE, MYSQL]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();
  fireEvent.click(screen.getByRole('button', { name: /mysql/i }));
  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'prod' } });

  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  expect(saveMock).not.toHaveBeenCalled();
  expect(screen.getByRole('alert').textContent).toMatch(/host, user is required/i);
});

/*
 * The fetch can fail — the engine can be down, or the shell can be a browser
 * tab with no bridge at all. A dialog that renders an empty driver picker
 * with no explanation is the blank-screen failure this project has hit
 * before, so it must render, say why, and refuse to save.
 */
it('explains itself and refuses to save when the driver list cannot be fetched', async () => {
  driversMock.mockRejectedValue(ENGINE_DIED);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);

  expect((await screen.findByRole('alert')).textContent).toMatch(/the engine died/);
  expect(screen.getByRole('dialog')).toBeDefined();
  expect((screen.getByRole('button', { name: /^connect$/i }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole('button', { name: /test connection/i }) as HTMLButtonElement).disabled).toBe(true);

  // Cmd+Enter reaches handleConnect without passing the disabled attribute,
  // so the guard inside it is the only thing standing here.
  await act(async () => {
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Enter', metaKey: true });
  });
  expect(saveMock).not.toHaveBeenCalled();
});

// An engine that reports no drivers at all is the same failure wearing a
// success: there is nothing to connect to, and silence would look like a
// dialog that simply lost its buttons.
it('says so when the engine reports no drivers at all', async () => {
  driversMock.mockResolvedValue([]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);

  expect((await screen.findByRole('alert')).textContent).toMatch(/no drivers/i);
  expect((screen.getByRole('button', { name: /^connect$/i }) as HTMLButtonElement).disabled).toBe(true);
});

/*
 * E2-1. A drivers.list answer the engine should never produce, and four
 * shapes a reviewer produced anyway.
 *
 * Every one of these unmounted the React tree: `null` and a bare object took
 * `drivers.map` with them, `required_fields: 'file'` threw
 * `requiredFields.map is not a function`, and an object `id` threw "Objects
 * are not valid as a React child". With no error boundary above it, each one
 * was a blank window.
 *
 * The designed failure path — a rejection, and an empty list — is tested
 * above and is untouched; this is the malformed-but-RESOLVED path, which had
 * nothing standing on it at all.
 */

/** Puts a raw drivers.list payload on the wire, past no stub. */
function engineAnswers(payload: unknown) {
  requestMock.mockResolvedValue(payload);
  driversMock.mockImplementation(validatedListDrivers);
}

/**
 * The two things that must hold for every malformed payload: the dialog is
 * still on screen with something readable on it, and it will not save —
 * including through the Cmd+Enter chord, which never sees a `disabled`
 * attribute.
 */
async function stillDrawnAndUnsaveable() {
  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toBeTruthy();
  expect(screen.getByRole('dialog')).toBeDefined();
  expect((screen.getByRole('button', { name: /^connect$/i }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole('button', { name: /test connection/i }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => {
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Enter', metaKey: true });
  });
  expect(saveMock).not.toHaveBeenCalled();
}

it('survives a drivers.list that answers null', async () => {
  engineAnswers(null);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await stillDrawnAndUnsaveable();
});

it('survives a drivers.list that answers an object instead of a list', async () => {
  engineAnswers({ id: 'x' });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await stillDrawnAndUnsaveable();
});

it('survives a driver whose required_fields is a string', async () => {
  engineAnswers([{ ...SQLITE, required_fields: 'file' }]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await stillDrawnAndUnsaveable();
});

it('survives a driver whose id is an object', async () => {
  engineAnswers([{ ...SQLITE, id: { a: 1 } }]);
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await stillDrawnAndUnsaveable();
});

/*
 * The other half of the decision, on screen: one unreadable entry costs that
 * driver and nothing else. A validator that refused the whole list would
 * pass all four tests above and still take a working SQLite picker away from
 * the user.
 */
it('keeps the drivers it can read when one entry is unreadable', async () => {
  engineAnswers([{ id: { a: 1 }, required_fields: ['file'] }, SQLITE]);
  saveMock.mockResolvedValue({
    id: 'a1', name: 'local', driver: 'sqlite', file: '/tmp/a.db',
    color: '#3d7d55', read_only: false,
  });
  render(<ConnectionDialog open onClose={() => {}} onSaved={() => {}} />);
  await driversLoaded();

  await fill('local', '/tmp/a.db');
  await act(async () => { screen.getByRole('button', { name: /^connect$/i }).click(); });

  expect(saveMock.mock.calls[0][0].driver).toBe('sqlite');
});

/*
 * C-2. A request abandoned by a close must not land on the form that
 * replaced it.
 *
 * Reported shape: type a path, click Test Connection, press Escape while it
 * is still in flight, reopen via Add connection. The form is correctly blank
 * — and then the abandoned request resolves and plants "Reachable" on it, a
 * verdict asserting reachability about a file being replaced.
 *
 * Every test below leaves the request DELIBERATELY unresolved across the
 * close and the reopen, and settles it afterwards. Awaiting the request
 * before closing tests the reset instead, which is what let this survive its
 * first fix.
 */

/** A promise whose settlement the test decides, rather than the mock. */
function deferred<T>() {
  let settle!: { resolve: (v: T) => void; reject: (e: unknown) => void };
  const promise = new Promise<T>((resolve, reject) => { settle = { resolve, reject }; });
  return { promise, ...settle };
}

/** Opens the dialog, fills it, and returns the Add connection button. */
async function openAndFill(name: string, file: string): Promise<HTMLElement> {
  const opener = screen.getByRole('button', { name: /add connection/i });
  fireEvent.click(opener);
  await fill(name, file);
  return opener;
}

it('drops a reachability verdict that lands after a close and reopen', async () => {
  const inFlight = deferred<{ ok: boolean }>();
  testMock.mockReturnValue(inFlight.promise);
  render(<AddConnectionHarness />);
  const opener = await openAndFill('old', '/tmp/OLD-FILE.db');

  fireEvent.click(screen.getByRole('button', { name: /test connection/i }));
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  expect(screen.queryByRole('dialog')).toBeNull();

  fireEvent.click(opener);
  await driversLoaded();
  expect((screen.getByLabelText(/file/i) as HTMLInputElement).value).toBe('');

  await act(async () => { inFlight.resolve({ ok: true }); });

  expect(screen.queryByText(/reachable/i)).toBeNull();
  // The abandoned request must not leave the control it disabled disabled
  // either: a form that cannot be tested is a quieter version of the same
  // bug.
  expect((screen.getByRole('button', { name: /test connection/i }) as HTMLButtonElement).disabled).toBe(false);
});

it('drops a test-connection throw that lands after a close and reopen', async () => {
  const inFlight = deferred<{ ok: boolean }>();
  testMock.mockReturnValue(inFlight.promise);
  render(<AddConnectionHarness />);
  const opener = await openAndFill('old', '/tmp/OLD-FILE.db');

  fireEvent.click(screen.getByRole('button', { name: /test connection/i }));
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  fireEvent.click(opener);
  await driversLoaded();

  await act(async () => {
    inFlight.reject({ code: -32020, message: 'the engine died' });
    await inFlight.promise.catch(() => {});
  });

  expect(screen.queryByText(/the engine died/i)).toBeNull();
  expect(screen.queryByRole('alert')).toBeNull();
});

it('drops a save rejection that lands after a close and reopen', async () => {
  const inFlight = deferred<Connection>();
  saveMock.mockReturnValue(inFlight.promise);
  render(<AddConnectionHarness />);
  const opener = await openAndFill('old', '/tmp/OLD-FILE.db');

  fireEvent.click(screen.getByRole('button', { name: /^connect$/i }));
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  fireEvent.click(opener);
  await driversLoaded();

  await act(async () => {
    inFlight.reject({ code: -32020, message: 'boom', data: { kind: 'not_found', message: 'database file does not exist' } });
    await inFlight.promise.catch(() => {});
  });

  // The reported half of this one: an in-flight rejection planting a
  // role="alert" error on a freshly-blank form.
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.getByRole('dialog')).toBeDefined();
  // Connect must still work on the new form.
  expect((screen.getByRole('button', { name: /^connect$/i }) as HTMLButtonElement).disabled).toBe(false);
});

it('drops a save that succeeds after a close and reopen, leaving the new form open', async () => {
  const inFlight = deferred<Connection>();
  saveMock.mockReturnValue(inFlight.promise);
  render(<AddConnectionHarness />);
  const opener = await openAndFill('old', '/tmp/OLD-FILE.db');

  fireEvent.click(screen.getByRole('button', { name: /^connect$/i }));
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  fireEvent.click(opener);
  await driversLoaded();

  await act(async () => {
    inFlight.resolve({ id: 'a1', name: 'old', driver: 'sqlite', file: '/tmp/OLD-FILE.db', color: '#3d7d55', read_only: false });
  });

  // onSaved closes the dialog (App.tsx) — firing it here would shut the
  // form the user just opened.
  expect(screen.getByRole('dialog')).toBeDefined();
});

// Closing is a reset too, not only opening: a verdict left standing on a
// closed dialog is a verdict standing on whatever opens next, and every
// close path funnels through the same effect.
it('clears the form when it closes, not only when it opens', async () => {
  testMock.mockResolvedValue({ ok: true });
  render(<AddConnectionHarness />);
  const opener = await openAndFill('local', '/tmp/a.db');

  await act(async () => { screen.getByRole('button', { name: /test connection/i }).click(); });
  await screen.findByText(/reachable/i);

  fireEvent.click(screen.getByRole('button', { name: /^cancel$/i }));
  fireEvent.click(opener);
  await driversLoaded();

  expect((screen.getByLabelText(/name/i) as HTMLInputElement).value).toBe('');
  expect(screen.queryByText(/reachable/i)).toBeNull();
});

// The driver list is a request like any other, so it gets the same guard:
// one abandoned by a close must not repaint the picker of the form that
// replaced it.
it('drops a driver list that lands after a close and reopen', async () => {
  const inFlight = deferred<DriverInfo[]>();
  driversMock.mockReturnValueOnce(inFlight.promise);
  render(<AddConnectionHarness />);
  const opener = screen.getByRole('button', { name: /add connection/i });

  fireEvent.click(opener);
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  fireEvent.click(opener);
  await driversLoaded();

  await act(async () => { inFlight.resolve([MYSQL]); });

  expect(screen.queryByRole('button', { name: /mysql/i })).toBeNull();
});

it('drops a driver-list failure that lands after a close and reopen', async () => {
  const inFlight = deferred<DriverInfo[]>();
  driversMock.mockReturnValueOnce(inFlight.promise);
  render(<AddConnectionHarness />);
  const opener = screen.getByRole('button', { name: /add connection/i });

  fireEvent.click(opener);
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  fireEvent.click(opener);
  await driversLoaded();

  await act(async () => {
    inFlight.reject(ENGINE_DIED);
    await inFlight.promise.catch(() => {});
  });

  // The reopened form is working: an error from a request nobody is waiting
  // for any more must not disable it.
  expect(screen.queryByRole('alert')).toBeNull();
  expect((screen.getByRole('button', { name: /^connect$/i }) as HTMLButtonElement).disabled).toBe(false);
});
