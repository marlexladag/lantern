import { describe, it, expect } from 'vitest';
import { describeError } from './errors';
import { EngineErrorCode } from './engine';

// Every fixture here is an OBJECT, because that is the only thing `request()`
// can reject with — it throws the NoIpc literal on one path and
// `toEngineError(err)` on the other, and both are objects. A bare string is a
// shape the system cannot produce, and testing one is what let
// `String(err)` — `[object Object]` for every shell failure there is — survive
// this long.
describe('describeError', () => {
  it('prefers the engine database error and carries its native text through', () => {
    const got = describeError({
      code: -32020,
      message: 'boom',
      data: { kind: 'constraint', message: 'the database reported an error', native: 'UNIQUE constraint failed: users.email' },
    });

    expect(got.message).toBe('the database reported an error');
    expect(got.native).toBe('UNIQUE constraint failed: users.email');
    expect(got.kind).toBe('constraint');
  });

  it('leaves native undefined when the database error carries none', () => {
    const got = describeError({
      code: -32020,
      message: 'boom',
      data: { kind: 'canceled', message: 'canceled' },
    });

    expect(got.message).toBe('canceled');
    expect(got.native).toBeUndefined();
    expect(got.kind).toBe('canceled');
  });

  it('falls back to the engine error message when the engine died mid-request', () => {
    const got = describeError({ code: EngineErrorCode.Died, message: 'the engine died' });

    expect(got.message).toBe('the engine died');
    expect(got.message).not.toContain('[object');
    // A shell failure has no Kind: it never reached a database.
    expect(got.kind).toBeUndefined();
    expect(got.native).toBeUndefined();
  });

  it('describes the NoIpc rejection with the message request() gave it', () => {
    const got = describeError({
      code: EngineErrorCode.NoIpc,
      message: 'Lantern must be opened as the desktop app — this page has no connection to the engine in a browser tab.',
    });

    expect(got.message).toMatch(/desktop app/);
    expect(got.message).not.toContain('[object');
  });

  it('coerces a rejection that is not an engine error at all', () => {
    // Not reachable through request(), but describeError is the single
    // normalizer and must not be the thing that throws.
    expect(describeError(new Error('kaboom')).message).toBe('Error: kaboom');
  });
});
