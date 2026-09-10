import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('@tauri-apps/api/core', () => ({ invoke: vi.fn(), isTauri: vi.fn() }));
vi.mock('@tauri-apps/api/event', () => ({ listen: vi.fn() }));

import { invoke, isTauri } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';
import {
  request,
  health,
  onStateChange,
  isEngineError,
  toEngineError,
  EngineErrorCode,
} from './engine';

const invokeMock = vi.mocked(invoke);
const isTauriMock = vi.mocked(isTauri);
const listenMock = vi.mocked(listen);

beforeEach(() => {
  invokeMock.mockReset();
  listenMock.mockReset();
  // Every existing test exercises the real desktop-app path; only the
  // dedicated "no IPC bridge" test below overrides this.
  isTauriMock.mockReset();
  isTauriMock.mockReturnValue(true);
});

describe('request', () => {
  it('forwards the method and params to the engine_request command', async () => {
    invokeMock.mockResolvedValue({ ok: true });

    const result = await request<{ ok: boolean }>('query', { sql: 'SELECT 1' });

    expect(invokeMock).toHaveBeenCalledWith('engine_request', {
      method: 'query',
      params: { sql: 'SELECT 1' },
    });
    expect(result).toEqual({ ok: true });
  });

  it('sends null params when none are given', async () => {
    invokeMock.mockResolvedValue(null);

    await request('health');

    expect(invokeMock).toHaveBeenCalledWith('engine_request', {
      method: 'health',
      params: null,
    });
  });

  it('rejects with the structured error the shell sent, code and data intact', async () => {
    invokeMock.mockRejectedValue({
      code: -32601,
      message: 'unknown method: query',
      data: { method: 'query' },
    });

    // The whole point of the seam: `code` and `data` survive the trip. A
    // bare string would have nowhere to put the `Kind` that spec §11 needs.
    await expect(request('query')).rejects.toEqual({
      code: -32601,
      message: 'unknown method: query',
      data: { method: 'query' },
    });
  });

  it('rejects with a shell code when the engine is not running', async () => {
    invokeMock.mockRejectedValue({
      code: EngineErrorCode.Unavailable,
      message: 'cannot resolve sidecar: program not found',
    });

    await expect(request('health')).rejects.toMatchObject({
      code: EngineErrorCode.Unavailable,
    });
  });

  it('wraps a bare IPC-layer rejection so callers always get an EngineError', async () => {
    // Tauri itself can reject with a plain string (unknown command, bad
    // argument deserialization). That never reached our seam, so it has no
    // code of its own — give it one rather than leaking a raw string.
    invokeMock.mockRejectedValue('command engine_request not found');

    await expect(request('health')).rejects.toEqual({
      code: EngineErrorCode.Ipc,
      message: 'command engine_request not found',
      data: 'command engine_request not found',
    });
  });

  // User-found: opening the dev-server URL in a plain browser tab (rather
  // than the Tauri window) used to reach `invoke()` anyway, which reads
  // into a bridge Tauri never injects there — a bare
  // "TypeError: Cannot read properties of undefined (reading 'invoke')"
  // reaching the user as "Engine error: ...". The same code path is what a
  // genuinely dead engine would hit in the packaged app, so this has to
  // fail with an actionable message, not a raw exception, and never call
  // `invoke` at all.
  it('rejects with a distinct code and an actionable message when there is no Tauri IPC bridge', async () => {
    isTauriMock.mockReturnValue(false);

    await expect(request('health')).rejects.toEqual({
      code: EngineErrorCode.NoIpc,
      message: 'Lantern must be opened as the desktop app — this page has no connection to the engine in a browser tab.',
    });
    expect(invokeMock).not.toHaveBeenCalled();
  });
});

describe('isEngineError / toEngineError', () => {
  it('recognizes a structured error', () => {
    expect(isEngineError({ code: -32603, message: 'boom' })).toBe(true);
  });

  it('rejects values that are missing the structure', () => {
    expect(isEngineError('boom')).toBe(false);
    expect(isEngineError(null)).toBe(false);
    expect(isEngineError({ message: 'boom' })).toBe(false);
    expect(isEngineError({ code: -1 })).toBe(false);
  });

  it('passes a structured error through untouched', () => {
    const err = { code: -32000, message: 'engine is not running' };
    expect(toEngineError(err)).toBe(err);
  });

  it('wraps a non-Error throw with a readable message', () => {
    expect(toEngineError(new Error('kaboom'))).toEqual({
      code: EngineErrorCode.Ipc,
      message: 'Error: kaboom',
      data: new Error('kaboom'),
    });
  });
});

describe('health', () => {
  it('returns the engine health payload', async () => {
    invokeMock.mockResolvedValue({
      status: 'ok',
      version: '1.2.3',
      commit: 'abc123',
      pid: 4242,
    });

    const info = await health();

    expect(invokeMock).toHaveBeenCalledWith('engine_request', {
      method: 'health',
      params: null,
    });
    expect(info.status).toBe('ok');
    expect(info.version).toBe('1.2.3');
    expect(info.commit).toBe('abc123');
    expect(info.pid).toBe(4242);
  });
});

describe('onStateChange', () => {
  it('subscribes to the engine status event and unwraps the payload', async () => {
    let captured: ((event: { payload: string }) => void) | undefined;
    listenMock.mockImplementation((_name: string, handler: any) => {
      captured = handler;
      return Promise.resolve(() => {});
    });

    const seen: string[] = [];
    await onStateChange((s) => seen.push(s));

    expect(listenMock).toHaveBeenCalledWith('engine://status', expect.any(Function));
    captured?.({ payload: 'restarting' });
    expect(seen).toEqual(['restarting']);
  });
});
