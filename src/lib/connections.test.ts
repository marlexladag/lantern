import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('./engine', async () => {
  const actual = await vi.importActual<typeof import('./engine')>('./engine');
  return { ...actual, request: vi.fn() };
});

import { request } from './engine';
import {
  listConnections, saveConnection, testConnection, deleteConnection,
  openSession, loadColumns, closeSession, asDbError,
} from './connections';

const requestMock = vi.mocked(request);
beforeEach(() => requestMock.mockReset());

describe('method names and shapes', () => {
  it('lists connections', async () => {
    requestMock.mockResolvedValue([]);
    await listConnections();
    expect(requestMock).toHaveBeenCalledWith('connections.list');
  });

  it('saves a connection with its password alongside, never inside', async () => {
    requestMock.mockResolvedValue({ id: 'a1' });
    const conn = { name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
    await saveConnection(conn, 'hunter2');

    expect(requestMock).toHaveBeenCalledWith('connections.save', { connection: conn, password: 'hunter2' });
    const [, params] = requestMock.mock.calls[0];
    expect(JSON.stringify((params as { connection: unknown }).connection)).not.toContain('hunter2');
  });

  it('tests a connection', async () => {
    requestMock.mockResolvedValue({ ok: true });
    const conn = { name: 'local', driver: 'sqlite', file: '/tmp/a.db', color: '#3d7d55', read_only: false };
    const res = await testConnection(conn, '');
    expect(requestMock).toHaveBeenCalledWith('connections.test', { connection: conn, password: '' });
    expect(res.ok).toBe(true);
  });

  it('deletes by id', async () => {
    requestMock.mockResolvedValue({ deleted: true });
    await deleteConnection('a1');
    expect(requestMock).toHaveBeenCalledWith('connections.delete', { id: 'a1' });
  });

  it('opens a session by connection id', async () => {
    requestMock.mockResolvedValue({ session_id: 's1', catalog: { databases: [] }, capabilities: {} });
    const res = await openSession('a1');
    expect(requestMock).toHaveBeenCalledWith('session.open', { connection_id: 'a1' });
    expect(res.session_id).toBe('s1');
  });

  it('loads columns on demand', async () => {
    requestMock.mockResolvedValue([]);
    await loadColumns('s1', 'main', 'users');
    expect(requestMock).toHaveBeenCalledWith('session.columns', {
      session_id: 's1', database: 'main', table: 'users',
    });
  });

  it('closes a session', async () => {
    requestMock.mockResolvedValue({ closed: true });
    await closeSession('s1');
    expect(requestMock).toHaveBeenCalledWith('session.close', { session_id: 's1' });
  });
});

describe('asDbError', () => {
  it('extracts the normalized error from a rejected engine error', () => {
    const got = asDbError({
      code: -32020,
      message: 'access denied',
      data: { kind: 'auth', message: 'access denied', native: 'ERROR 1045' },
    });
    expect(got?.kind).toBe('auth');
    expect(got?.native).toBe('ERROR 1045');
  });

  it('returns null for something that is not an engine database error', () => {
    expect(asDbError('a plain string')).toBeNull();
    expect(asDbError({ code: -32001, message: 'engine timed out' })).toBeNull();
    expect(asDbError(null)).toBeNull();
  });

  it('returns null when data is present but not shaped like a DbError', () => {
    expect(asDbError({ code: -32020, message: 'x', data: { nope: true } })).toBeNull();
  });
});
