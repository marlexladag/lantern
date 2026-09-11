/**
 * One place that turns a rejection into something renderable, so every
 * failure surface in the UI tells the same story.
 *
 * Why this exists rather than `dbErr?.message ?? String(err)` at each call
 * site: `request()` in ./engine rejects with an **object** on both of its
 * paths — the `NoIpc` literal, or `toEngineError(err)` — and `asDbError`
 * returns null for everything except the engine's database code. So the
 * `String(err)` arm was not the rare case, it was every shell failure there
 * is ("the engine died", "the engine timed out", the IPC-bridge message),
 * and it rendered all of them as `[object Object]`.
 *
 * `EngineStatus.tsx` already gets this right by normalizing through
 * `toEngineError` before rendering (see the comment on its catch). This is
 * the same reasoning generalized: normalize once, and keep the database
 * error's structure — `kind` for the UI to branch on, `native` for the
 * on-demand detail of spec §11 — instead of flattening it to a string.
 */
import { asDbError, type DbErrorKind } from './connections';
import { toEngineError } from './engine';

export type ErrorDescription = {
  /** Engine-neutral, human-readable, and never `[object Object]`. */
  message: string;
  /** The driver's own text, shown only on demand (spec §11). */
  native?: string;
  /**
   * The engine's classification, present only when a database produced the
   * failure — a shell failure never reached one and so has no Kind. The UI
   * branches on this (spec §11): `canceled` is not a failure at all, and
   * `read_only` earns an actionable hint.
   */
  kind?: DbErrorKind;
};

/** Normalizes any rejection into a description the UI can render as-is. */
export function describeError(err: unknown): ErrorDescription {
  const dbErr = asDbError(err);
  if (dbErr) {
    return { message: dbErr.message, native: dbErr.native, kind: dbErr.kind };
  }
  // Not a database failure, so there is no Kind and no native driver text —
  // only the engine seam's own message, which request() guarantees is
  // meaningful for every code it raises.
  return { message: toEngineError(err).message };
}
