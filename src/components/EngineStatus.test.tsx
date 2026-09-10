import { it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';

vi.mock('../lib/engine', async () => {
  // toEngineError and EngineErrorCode are pure helpers with no Tauri
  // dependency, so the component gets the real ones; only the two functions
  // that cross the IPC boundary are stubbed.
  const actual = await vi.importActual<typeof import('../lib/engine')>('../lib/engine');
  return {
    ...actual,
    health: vi.fn(),
    onStateChange: vi.fn(),
  };
});

import { health, onStateChange, EngineErrorCode, type Health } from '../lib/engine';
import { EngineStatus } from './EngineStatus';

const healthMock = vi.mocked(health);
const onStateChangeMock = vi.mocked(onStateChange);

beforeEach(() => {
  healthMock.mockReset();
  onStateChangeMock.mockReset();
  onStateChangeMock.mockResolvedValue(() => {});
});

it('shows a connecting state before the handshake completes', () => {
  healthMock.mockReturnValue(new Promise(() => {}));

  render(<EngineStatus />);

  expect(screen.getByText(/connecting/i)).toBeDefined();
  // Not just the right text - the component must actually be talking to
  // the engine client, not rendering a hardcoded string.
  expect(healthMock).toHaveBeenCalledTimes(1);
  expect(healthMock).toHaveBeenCalledWith();
  expect(onStateChangeMock).toHaveBeenCalledWith(expect.any(Function));
});

it('shows the engine version and pid after a successful handshake', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/1\.2\.3/)).toBeDefined();
    expect(screen.getByText(/4242/)).toBeDefined();
  });
  expect(healthMock).toHaveBeenCalledTimes(1);
  expect(healthMock).toHaveBeenCalledWith();
});

it('shows the message and the code when the handshake fails', async () => {
  healthMock.mockRejectedValue({
    code: EngineErrorCode.Unavailable,
    message: 'cannot resolve sidecar: program not found',
  });

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/cannot resolve sidecar/i)).toBeDefined();
  });
  // The code is what a future Kind branch will key on, so it has to survive
  // all the way to the rendered output, not just the type.
  expect(screen.getByText(/-32000/)).toBeDefined();
  expect(healthMock).toHaveBeenCalledTimes(1);
  expect(healthMock).toHaveBeenCalledWith();
});

// User-found: this is exactly the shape `request()` rejects with when
// there is no Tauri IPC bridge (dev server opened in a browser tab, or a
// genuinely dead engine in the packaged app) — it must render as an
// actionable message with a distinct Kind badge, never a raw exception.
it('shows the DESKTOP badge and the actionable message when the IPC bridge is absent', async () => {
  healthMock.mockRejectedValue({
    code: EngineErrorCode.NoIpc,
    message: 'Lantern must be opened as the desktop app — this page has no connection to the engine in a browser tab.',
  });

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/must be opened as the desktop app/i)).toBeDefined();
  });
  expect(screen.getByText('DESKTOP')).toBeDefined();
});

it('still renders a readable error when something throws a bare string', async () => {
  healthMock.mockRejectedValue('something went sideways');

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/something went sideways/i)).toBeDefined();
  });
  expect(screen.getByText(new RegExp(String(EngineErrorCode.Ipc)))).toBeDefined();
});

it('reports a restart and re-runs the handshake when the engine recovers', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  let emit: ((s: 'ready' | 'restarting' | 'down') => void) | undefined;
  onStateChangeMock.mockImplementation(async (cb) => {
    emit = cb;
    return () => {};
  });

  render(<EngineStatus />);
  await waitFor(() => expect(screen.getByText(/1\.2\.3/)).toBeDefined());

  await act(async () => {
    emit?.('restarting');
  });
  expect(screen.getByText(/restarting/i)).toBeDefined();

  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 5555,
  });
  await act(async () => {
    emit?.('ready');
  });

  await waitFor(() => expect(screen.getByText(/5555/)).toBeDefined());
});

it('ignores a stale handshake result when the engine crashes again before it resolves', async () => {
  // Engine A comes up.
  healthMock.mockResolvedValueOnce({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  let emit: ((s: 'ready' | 'restarting' | 'down') => void) | undefined;
  onStateChangeMock.mockImplementation(async (cb) => {
    emit = cb;
    return () => {};
  });

  render(<EngineStatus />);
  await waitFor(() => expect(screen.getByText(/4242/)).toBeDefined());

  // Engine A crashes.
  await act(async () => {
    emit?.('restarting');
  });
  expect(screen.getByText(/restarting/i)).toBeDefined();

  // Engine B comes up: a re-handshake is dispatched but deliberately left
  // unresolved, so we can crash B again before it settles.
  let resolveStaleHandshake: ((info: Health) => void) | undefined;
  healthMock.mockReturnValueOnce(
    new Promise<Health>((resolve) => {
      resolveStaleHandshake = resolve;
    }),
  );
  await act(async () => {
    emit?.('ready');
  });

  // Engine B crashes before that handshake resolves.
  await act(async () => {
    emit?.('restarting');
  });
  expect(screen.getByText(/restarting/i)).toBeDefined();

  // The stale handshake for the now-dead engine B finally resolves. It
  // must not overwrite the restarting view with B's outdated pid.
  await act(async () => {
    resolveStaleHandshake?.({
      status: 'ok',
      version: '1.2.3',
      commit: 'abc123',
      pid: 5555,
    });
  });

  expect(screen.getByText(/restarting/i)).toBeDefined();
  expect(screen.queryByText(/5555/)).toBeNull();
});

it('shows a distinct message when the supervisor gives up', async () => {
  healthMock.mockResolvedValueOnce({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  let emit: ((s: 'ready' | 'restarting' | 'down') => void) | undefined;
  onStateChangeMock.mockImplementation(async (cb) => {
    emit = cb;
    return () => {};
  });

  const { container } = render(<EngineStatus />);
  await waitFor(() => expect(screen.getByText(/4242/)).toBeDefined());

  await act(async () => {
    emit?.('down');
  });

  expect(screen.getByText(/failed to restart/i)).toBeDefined();
  // Not the error view's markup — "down" is its own distinct case.
  expect(container.querySelector('.engine-error')).toBeNull();
});

it('ignores a stale handshake failure when the engine crashes again before it rejects', async () => {
  // The first handshake attempt is left unresolved so we can control when
  // (and whether) it settles relative to the restart below.
  let rejectFirstHandshake: ((err: unknown) => void) | undefined;
  healthMock.mockReturnValueOnce(
    new Promise((_resolve, reject) => {
      rejectFirstHandshake = reject;
    }),
  );

  let emit: ((s: 'ready' | 'restarting' | 'down') => void) | undefined;
  onStateChangeMock.mockImplementation(async (cb) => {
    emit = cb;
    return () => {};
  });

  const { container } = render(<EngineStatus />);
  expect(screen.getByText(/connecting/i)).toBeDefined();

  await act(async () => {
    emit?.('restarting');
  });
  expect(screen.getByText(/restarting/i)).toBeDefined();

  // The now-stale rejection arrives after the restart. It must not
  // overwrite the restarting view with an error for an engine incarnation
  // nothing is waiting on anymore.
  await act(async () => {
    rejectFirstHandshake?.({ code: EngineErrorCode.Unavailable, message: 'boom' });
  });

  expect(screen.getByText(/restarting/i)).toBeDefined();
  expect(container.querySelector('.engine-error')).toBeNull();
});

it('unregisters the state-change listener if the component unmounts before onStateChange resolves', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  let resolveOnStateChange: ((fn: () => void) => void) | undefined;
  onStateChangeMock.mockImplementation(
    () =>
      new Promise((resolve) => {
        resolveOnStateChange = resolve;
      }),
  );

  const { unmount } = render(<EngineStatus />);
  // Unmount while onStateChange's promise is still pending, before an
  // unlisten function exists to call.
  unmount();

  const unlistenSpy = vi.fn();
  await act(async () => {
    resolveOnStateChange?.(unlistenSpy);
  });

  // A subscription that resolves after unmount must be torn down
  // immediately rather than stored, or it would leak: nothing is left to
  // ever call unlisten() on it otherwise.
  expect(unlistenSpy).toHaveBeenCalledTimes(1);
});
