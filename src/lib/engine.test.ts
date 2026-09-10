import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('@tauri-apps/api/core', () => ({ invoke: vi.fn() }));
vi.mock('@tauri-apps/api/event', () => ({ listen: vi.fn() }));

import { invoke } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';
import { request, health, onStateChange } from './engine';

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

  it('propagates engine errors to the caller', async () => {
    invokeMock.mockRejectedValue('engine is not running');

    await expect(request('health')).rejects.toBe('engine is not running');
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
