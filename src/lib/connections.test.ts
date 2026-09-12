import { describe, it, expect, vi, beforeEach } from 'vitest';
// Read as text, not imported as modules: this test's whole job is to compare
// two declarations that live on opposite sides of the IPC seam and cannot
// import each other.
// The whole package, not just dberr.go. The Kind constants are a wire
// contract with no build-time link between its two declarations, and this
// test is now the only thing checking them against each other — so reading
// one file of a package that may grow another is a blind spot in the only
// check there is. Verified by planting `KindQuota Kind = "quota"` in a
// second file: green before this glob, red after it.
const goSources = import.meta.glob('../../internal/engine/dberr/*.go', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;
import tsSource from './connections.ts?raw';
// The Go declaration of the drivers.list payload, read as text for the same
// reason: two declarations of one wire shape, with nothing linking them.
import driversGoSource from '../../internal/api/drivers.go?raw';

vi.mock('./engine', async () => {
  const actual = await vi.importActual<typeof import('./engine')>('./engine');
  return { ...actual, request: vi.fn() };
});

import { request } from './engine';
// The renderable half of the contract: whatever listDrivers rejects with has
// to survive the trip through describeError that every failure surface makes.
import { describeError } from './errors';
import {
  listConnections, saveConnection, testConnection, deleteConnection,
  openSession, loadTables, loadColumns, closeSession, listDrivers, asDbError,
} from './connections';

const requestMock = vi.mocked(request);
// Braces, not a bare expression. `mockReset()` returns the mock, and an arrow
// with an expression body returns it too — which Vitest reads as a teardown
// callback and INVOKES after each test. Today that is harmless (the mock is
// reset and returns undefined), but the moment a test in this file configures
// mockRejectedValue, the teardown call produces an unhandled rejection and the
// test fails after its own assertions passed. Verified: the failure surfaces as
// the rejection's message with no reference to the beforeEach that caused it,
// which makes it expensive to diagnose from the symptom.
beforeEach(() => {
  requestMock.mockReset();
});

describe('method names and shapes', () => {
  it('lists the drivers the engine has registered', async () => {
    requestMock.mockResolvedValue([{ id: 'sqlite', required_fields: ['file'], capabilities: {} }]);
    const got = await listDrivers();
    expect(requestMock).toHaveBeenCalledWith('drivers.list');
    expect(got[0].required_fields).toEqual(['file']);
  });

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

  it('loads one database\'s tables', async () => {
    requestMock.mockResolvedValue([{ name: 'alpha', kind: 'table' }]);
    const got = await loadTables('s1', 'main');
    expect(requestMock).toHaveBeenCalledWith('session.tables', { session_id: 's1', database: 'main' });
    expect(got).toEqual([{ name: 'alpha', kind: 'table' }]);
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

  // `native` and `query` are rendered straight into the UI, so they get the
  // same typeof check `kind` and `message` already have. Absent is fine;
  // present-and-not-a-string is not.
  it('returns null when native or query is present but is not a string', () => {
    const base = { kind: 'unknown', message: 'boom' };
    expect(asDbError({ code: -32020, message: 'x', data: { ...base, native: 42 } })).toBeNull();
    expect(asDbError({ code: -32020, message: 'x', data: { ...base, query: { sql: 'SELECT 1' } } })).toBeNull();
  });

  it('accepts a database error carrying both native and query as strings', () => {
    const got = asDbError({
      code: -32020,
      message: 'x',
      data: { kind: 'syntax', message: 'the database reported an error', native: 'near "SELCT"', query: 'SELCT 1' },
    });
    expect(got?.native).toBe('near "SELCT"');
    expect(got?.query).toBe('SELCT 1');
  });
});

/*
 * The Kind taxonomy is one contract with two declarations — Go's constants in
 * internal/engine/dberr and the DbErrorKind union here — and nothing at build
 * time links them. It was previously verified by a reviewer reading both
 * lists side by side, which is a check that works exactly once.
 *
 * Both sides are parsed out of their own source here. Hardcoding the list in
 * the test would prove nothing: it would be the same mistake written a third
 * time, and it would agree with whichever side was wrong.
 */
describe('the DbErrorKind union and the engine`s Kind constants', () => {
  /**
   * `KindReadOnly Kind = "read_only"`, whether it sits in a `const (…)`
   * block or is declared on its own as `const KindQuota Kind = "quota"` —
   * the second form is how a Kind would most naturally arrive in a new
   * file, and is the exact shape this glob exists to catch.
   */
  function goKinds(): string[] {
    // _test.go files are excluded: a test may legitimately declare a Kind
    // of its own as a fixture, and that is not part of the wire contract.
    return Object.entries(goSources)
      .filter(([path]) => !path.endsWith('_test.go'))
      .flatMap(([, source]) => [...source.matchAll(/^\s*(?:const\s+)?Kind\w+\s+Kind\s*=\s*"([^"]+)"/gm)])
      .map((m) => m[1]);
  }

  /** The string literals in this file's own `export type DbErrorKind` declaration. */
  function tsKinds(): string[] {
    const declaration = /export type DbErrorKind =([\s\S]*?);/.exec(tsSource);
    expect(declaration, 'DbErrorKind declaration not found in connections.ts').not.toBeNull();
    return [...declaration![1].matchAll(/'([^']+)'/g)].map((m) => m[1]);
  }

  // Two empty lists compare equal, so a regex that has quietly stopped
  // matching would make the real assertion below pass while checking nothing.
  it('finds both declarations to compare', () => {
    // A glob that matched nothing would leave goKinds empty, and an empty
    // list agrees with anything the same way two empty lists compare equal.
    expect(Object.keys(goSources).length).toBeGreaterThan(0);
    expect(goKinds().length).toBeGreaterThan(5);
    expect(tsKinds().length).toBe(goKinds().length);
    expect(tsKinds()).toContain('read_only');
  });

  it('contains exactly the same set of kinds as the Go engine', () => {
    expect([...tsKinds()].sort()).toEqual([...goKinds()].sort());
  });
});

/*
 * DriverInfo is the second two-declaration contract in this file — Go's
 * struct tags in internal/api/drivers.go and the interface here — and the
 * dialog now builds its whole form out of it. A key renamed on one side and
 * not the other reads as `undefined` in the UI rather than as a failure, so
 * it is checked the same way the Kind taxonomy is: by parsing both.
 */
describe('the DriverInfo interface and the engine`s DriverInfo struct', () => {
  /** The `json:"…"` tags of the Go struct, in declaration order. */
  function goKeys(): string[] {
    const struct = /type DriverInfo struct \{([\s\S]*?)\n\}/.exec(driversGoSource);
    expect(struct, 'DriverInfo struct not found in drivers.go').not.toBeNull();
    return [...struct![1].matchAll(/json:"([^",]+)/g)].map((m) => m[1]);
  }

  /** The property names of this file's own `export interface DriverInfo`. */
  function tsKeys(): string[] {
    const declaration = /export interface DriverInfo \{([\s\S]*?)\n\}/.exec(tsSource);
    expect(declaration, 'DriverInfo interface not found in connections.ts').not.toBeNull();
    return [...declaration![1].matchAll(/^\s*(\w+)[?]?:/gm)].map((m) => m[1]);
  }

  // Two empty lists compare equal, so the real assertion below would pass
  // against a regex that has quietly stopped matching.
  it('finds both declarations to compare', () => {
    expect(goKeys()).toContain('required_fields');
    expect(tsKeys().length).toBe(goKeys().length);
  });

  it('declares exactly the same keys as the engine', () => {
    expect([...tsKeys()].sort()).toEqual([...goKeys()].sort());
  });
});

/*
 * The payload validation, driven with the shapes a reviewer actually put on
 * the wire. `request<T>` casts rather than checks, so every one of these
 * reached the connection dialog's `.map` unexamined and took the whole React
 * tree with it.
 *
 * The two halves of the decision are asserted separately, because they are
 * different answers to different questions: one unreadable ENTRY costs that
 * driver, an unreadable RESPONSE costs the call.
 */
describe('listDrivers validation', () => {
  const sqlite = {
    id: 'sqlite',
    required_fields: ['file'],
    capabilities: { transactions: true, multiple_databases: false, editable_rows: true },
  };

  it('keeps a well-formed list untouched', async () => {
    requestMock.mockResolvedValue([sqlite]);
    expect(await listDrivers()).toEqual([sqlite]);
  });

  /*
   * A `null` element is not a hypothetical: a Go `[]*DriverInfo` with a nil
   * in it marshals to exactly this, and `typeof null` is 'object', so the
   * object check alone would wave it through into `entry.id`.
   */
  it('drops an entry that is not an object at all', async () => {
    requestMock.mockResolvedValue([null, 'sqlite', sqlite]);
    expect(await listDrivers()).toEqual([sqlite]);
  });

  // Dropped, not fatal: the drivers either side of it are still dialable, and
  // a picker missing one entry is a smaller loss than a dialog with no
  // drivers at all.
  it('drops an entry whose id is not a string and keeps the rest', async () => {
    requestMock.mockResolvedValue([{ id: { a: 1 }, required_fields: ['file'] }, sqlite]);
    expect(await listDrivers()).toEqual([sqlite]);
  });

  /*
   * `required_fields: 'file'` is the shape that produced
   * `requiredFields.map is not a function`. A driver whose requirements
   * cannot be read is one this shell would build an incomplete form for and
   * save a connection that can never dial — so the whole entry goes, not
   * just the field.
   */
  it('drops an entry whose required_fields is not an array of strings', async () => {
    requestMock.mockResolvedValue([{ ...sqlite, required_fields: 'file' }, sqlite]);
    expect(await listDrivers()).toEqual([sqlite]);

    requestMock.mockResolvedValue([{ ...sqlite, required_fields: ['file', 7] }, sqlite]);
    expect(await listDrivers()).toEqual([sqlite]);
  });

  /*
   * The one malformed-looking shape that is not malformed. Go's encoding/json
   * writes a nil slice as `null`, so this is how a driver that needs nothing
   * to dial arrives — and dropping it would take a perfectly usable driver
   * off the picker over its own honest answer.
   */
  it('reads a null required_fields as a driver that requires nothing', async () => {
    requestMock.mockResolvedValue([{ id: 'memory', required_fields: null }]);
    expect(await listDrivers()).toEqual([{ id: 'memory', required_fields: [] }]);
  });

  /*
   * Not an empty list: that is a claim ("this engine registered no drivers")
   * and it would be false. The engine answered with something this shell
   * cannot read, which is what the Malformed code is for, and the dialog
   * already renders a rejection.
   */
  it('rejects a response that is not an array at all', async () => {
    requestMock.mockResolvedValue(null);
    await expect(listDrivers()).rejects.toMatchObject({ code: -32004 });

    requestMock.mockResolvedValue({ id: 'x' });
    await expect(listDrivers()).rejects.toMatchObject({ code: -32004 });
  });

  // Rejecting with a bare string, or with anything describeError cannot
  // recognize, is how a failure surface comes to read `[object Object]`.
  it('rejects with something describeError can render', async () => {
    requestMock.mockResolvedValue('nope');
    const failure = await listDrivers().then(() => null, (err: unknown) => err);
    expect(failure).not.toBeNull();
    expect(describeError(failure).message).not.toContain('object Object');
  });
});
