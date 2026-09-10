import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('@tauri-apps/api/core', () => ({ invoke: vi.fn() }));
vi.mock('@tauri-apps/api/event', () => ({ listen: vi.fn() }));

import { invoke } from '@tauri-apps/api/core';
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
const listenMock = vi.mocked(listen);

beforeEach(() => {
  invokeMock.mockReset();
  listenMock.mockReset();
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
