import { describe, it, expect, vi, beforeEach } from 'vitest';
// Read as text, not imported as modules: this test's whole job is to compare
// two declarations that live on opposite sides of the IPC seam and cannot
// import each other. The whole package, not just value.go — see
// connections.test.ts, which this shape is copied from, for why a glob
// rather than one file matters.
const goSources = import.meta.glob('../../internal/engine/driver/*.go', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;
import tsSource from './browse.ts?raw';

vi.mock('./engine', async () => {
  const actual = await vi.importActual<typeof import('./engine')>('./engine');
  return { ...actual, request: vi.fn() };
});

import { request } from './engine';
import { describeError } from './errors';
import { browsePage, MAX_BROWSE_LIMIT, type BrowsePage } from './browse';

const requestMock = vi.mocked(request);
// Braces, not a bare expression: `mockReset()` returns the mock itself, and
// a beforeEach hook that returns a function has that function invoked again
// as teardown after the test — re-triggering a mock a rejection test just
// configured, as an unhandled rejection nothing catches. Confirmed with a
// throwaway repro; connections.test.ts's `() => requestMock.mockReset()`
// only escapes this because it never configures a rejection.
beforeEach(() => {
  requestMock.mockReset();
});

describe('browsePage', () => {
  it('requests a first page with no cursor', async () => {
    const page: BrowsePage = { columns: [], rows: [], exhausted: true, offset: 0 };
    requestMock.mockResolvedValue(page);

    const res = await browsePage('s1', { database: 'main', table: 'users', limit: 50 });

    expect(requestMock).toHaveBeenCalledWith('browse.page', {
      session_id: 's1',
      database: 'main',
      table: 'users',
      limit: 50,
    });
    expect(res).toBe(page);
  });

  // The load-bearing case. `after` takes the WHOLE previous page, not a bare
  // keyset array: a caller who only had the keyset to hand could build a
  // request missing sort_token (exactly the mistake that turned eight Go
  // tests red the day the pairing became mandatory — see BrowsePage's own
  // doc comment). Passing the previous page back makes that mistake
  // structurally hard to make, because keyset and sort_token never exist as
  // two separate values the caller could assemble wrong.
  it('continues from a previous page by passing the page itself, never a bare keyset', async () => {
    const prev: BrowsePage = {
      columns: [{ name: 'id', data_type: 'INTEGER' }],
      rows: [[{ kind: 'int', text: '1' }]],
      keyset: [{ kind: 'int', text: '1' }],
      sort_token: 'tok-abc123',
      exhausted: false,
      offset: 0,
    };
    const next: BrowsePage = { columns: prev.columns, rows: [], exhausted: true, offset: 0 };
    requestMock.mockResolvedValue(next);

    const res = await browsePage('s1', {
      database: 'main',
      table: 'users',
      sort: [{ column: 'id', desc: false }],
      limit: 50,
      after: prev,
    });

    expect(requestMock).toHaveBeenCalledWith('browse.page', {
      session_id: 's1',
      database: 'main',
      table: 'users',
      sort: [{ column: 'id', desc: false }],
      limit: 50,
      after: prev.keyset,
      sort_token: prev.sort_token,
    });
    expect(res).toBe(next);
  });

  // Adversarial: an empty table, or a keyset past the last row, comes back
  // as zero rows. Nothing in browsePage should special-case this — it is
  // just a BrowsePage like any other — but it is worth pinning so a future
  // change cannot start assuming rows is always non-empty.
  it('passes through a page with zero rows', async () => {
    const page: BrowsePage = { columns: [{ name: 'id', data_type: 'INTEGER' }], rows: [], exhausted: true, offset: 0 };
    requestMock.mockResolvedValue(page);

    const res = await browsePage('s1', { database: 'main', table: 'empty', limit: 50 });

    expect(res.rows).toEqual([]);
    expect(res.exhausted).toBe(true);
  });

  // Adversarial: a table smaller than one page is exhausted on the very
  // first request, with no keyset or sort_token at all. A caller that
  // assumed "exhausted" only ever shows up on a later page would stop
  // rendering rows, not stop asking for more.
  it('passes through a page that is exhausted on the first request', async () => {
    const page: BrowsePage = {
      columns: [{ name: 'id', data_type: 'INTEGER' }],
      rows: [[{ kind: 'int', text: '1' }]],
      exhausted: true,
      offset: 0,
    };
    requestMock.mockResolvedValue(page);

    const res = await browsePage('s1', { database: 'main', table: 'tiny', limit: 50 });

    expect(res.exhausted).toBe(true);
    expect(res.keyset).toBeUndefined();
    expect(res.sort_token).toBeUndefined();
  });

  // Adversarial: the rejection is an OBJECT, never a bare string — a bare
  // string is a shape `request()` cannot produce (see errors.test.ts), and
  // testing one is exactly what let `?? String(err)` render `[object
  // Object]` at five call sites before it was removed. describeError, not
  // asDbError, is how a caller of this client is meant to read a rejection.
  it('surfaces a rejected browse through describeError, never as [object Object]', async () => {
    const rejection = { code: -32001, message: 'the write to the child process failed' };
    requestMock.mockRejectedValue(rejection);

    let caught: unknown;
    try {
      await browsePage('s1', { database: 'main', table: 'users', limit: 50 });
    } catch (err) {
      caught = err;
    }

    const described = describeError(caught);
    expect(described.message).toBe('the write to the child process failed');
    expect(described.message).not.toBe('[object Object]');
    expect(described.kind).toBeUndefined();
  });

  // Same adversarial shape, but the rejection this time is a genuine
  // database error — the engine refusing a request whose limit exceeds
  // MaxBrowseLimit. describeError must still carry `kind` through so the UI
  // can branch on it.
  it('surfaces a database rejection with its kind intact', async () => {
    const rejection = {
      code: -32020,
      message: 'boom',
      data: { kind: 'invalid', message: 'browse: limit exceeds the maximum page size' },
    };
    requestMock.mockRejectedValue(rejection);

    let caught: unknown;
    try {
      await browsePage('s1', { database: 'main', table: 'users', limit: 5000 });
    } catch (err) {
      caught = err;
    }

    const described = describeError(caught);
    expect(described.message).toBe('browse: limit exceeds the maximum page size');
    expect(described.kind).toBe('invalid');
  });
});

/*
 * ValueKind and MaxBrowseLimit are each one contract with two declarations —
 * Go's constants in internal/engine/driver and this file's own union and
 * constant — and nothing at build time links them. Both sides are parsed
 * out of their own source here, the same way connections.test.ts already
 * does for DbErrorKind. Hardcoding either list in the test would prove
 * nothing: it would be the same mistake written a third time, and it would
 * agree with whichever side was wrong.
 */
describe('ValueKind and the driver package`s ValueKind constants', () => {
  /**
   * `ValueNull ValueKind = "null"`, whether it sits in a `const (…)` block
   * or is declared on its own as `const ValueQuux ValueKind = "quux"` — the
   * second form is how a member would most naturally arrive in a new file,
   * and is the exact shape this glob exists to catch. Matched on the type
   * name `ValueKind`, not on any prefix of the identifier, since — unlike
   * `Kind`'s `KindXxx` members — these are named `ValueXxx`, not
   * `ValueKindXxx`.
   */
  function goValueKinds(): string[] {
    // _test.go files are excluded: a test may legitimately declare a
    // ValueKind of its own as a fixture, and that is not part of the wire
    // contract.
    return Object.entries(goSources)
      .filter(([path]) => !path.endsWith('_test.go'))
      .flatMap(([, source]) => [...source.matchAll(/^\s*(?:const\s+)?\w+\s+ValueKind\s*=\s*"([^"]+)"/gm)])
      .map((m) => m[1]);
  }

  /** The string literals in this file's own `export type ValueKind` declaration. */
  function tsValueKinds(): string[] {
    const declaration = /export type ValueKind =([\s\S]*?);/.exec(tsSource);
    expect(declaration, 'ValueKind declaration not found in browse.ts').not.toBeNull();
    return [...declaration![1].matchAll(/'([^']+)'/g)].map((m) => m[1]);
  }

  function goMaxBrowseLimit(): number | undefined {
    const match = Object.entries(goSources)
      .filter(([path]) => !path.endsWith('_test.go'))
      .map(([, source]) => /const\s+MaxBrowseLimit\s*=\s*(\d+)/.exec(source))
      .find((m) => m !== null);
    return match ? Number(match[1]) : undefined;
  }

  // Two empty lists compare equal, so a regex that has quietly stopped
  // matching would make the real assertions below pass while checking
  // nothing.
  it('finds both declarations to compare', () => {
    // A glob that matched nothing would leave goValueKinds empty, and an
    // empty list agrees with anything the same way two empty lists compare
    // equal.
    expect(Object.keys(goSources).length).toBeGreaterThan(0);
    expect(goValueKinds().length).toBe(7);
    expect(tsValueKinds().length).toBe(goValueKinds().length);
    expect(tsValueKinds()).toContain('bytes');
  });

  it('contains exactly the same set of kinds as the Go driver package', () => {
    expect([...tsValueKinds()].sort()).toEqual([...goValueKinds()].sort());
  });

  it('agrees with the Go driver package on MaxBrowseLimit', () => {
    const goLimit = goMaxBrowseLimit();
    expect(goLimit).not.toBeUndefined();
    expect(goLimit).toBe(MAX_BROWSE_LIMIT);
  });
});
