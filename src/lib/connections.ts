/**
 * Typed client for the engine's connection and session methods.
 *
 * Everything funnels through `request` in ./engine, which is the single
 * `engine_request` command. Do not add a second channel.
 */
import { EngineErrorCode, request, type EngineError } from './engine';

/**
 * The engine's error classification. The UI branches on this (spec §11).
 *
 * One member per `Kind` constant in internal/engine/dberr/dberr.go, in the
 * same order. `read_only` is a write refused because the connection itself
 * is read-only — deliberately not `constraint`, which means the data broke a
 * UNIQUE/FK/CHECK rule: opposite user actions, and the UI has to tell them
 * apart. connections.test.ts checks this list against the Go one by parsing
 * both, rather than by eye.
 */
export type DbErrorKind =
  | 'auth' | 'network' | 'syntax' | 'constraint' | 'read_only' | 'timeout'
  | 'canceled' | 'not_found' | 'unsupported' | 'invalid' | 'unknown';

/** A database failure in engine-neutral terms. */
export interface DbError {
  kind: DbErrorKind;
  message: string;
  /** The driver's own text, shown only on request. */
  native?: string;
  /** The statement that failed, when there was one. */
  query?: string;
}

export interface Connection {
  id: string;
  name: string;
  driver: string;
  host?: string;
  port?: number;
  user?: string;
  database?: string;
  file?: string;
  /** Tints the window and tabs. A safety feature, not decoration. */
  color: string;
  read_only: boolean;
}

/** A connection being created has no id yet. */
export type NewConnection = Omit<Connection, 'id'> & { id?: string };

export interface Column {
  name: string;
  data_type: string;
  nullable: boolean;
  primary_key: boolean;
  position: number;
}

export interface Table {
  name: string;
  kind: 'table' | 'view';
  /** Absent until the table is expanded — introspection is lazy. */
  columns?: Column[];
}

export interface Database {
  name: string;
  /**
   * Absent from session.open. Introspection is two-tiered (spec section 5):
   * session.open returns the DATABASE list alone and leaves this unset, and
   * a database's tables are read by `loadTables` when it is expanded. The
   * field stays declared because Go's `json:"tables"` still writes the key —
   * as the literal `null` — and a consumer that trusted the old shape would
   * otherwise be told by the type that it cannot be there at all.
   */
  tables?: Table[];
}

export interface Catalog {
  databases: Database[];
}

export interface TestResult {
  ok: boolean;
  kind?: DbErrorKind;
  error?: string;
}

/** What an engine can do, so the UI knows what to hide (spec §4). */
export interface Capabilities {
  transactions: boolean;
  multiple_databases: boolean;
  editable_rows: boolean;
}

export interface OpenResult {
  session_id: string;
  catalog: Catalog;
  capabilities: Capabilities;
}

/**
 * One driver, as the engine describes it.
 *
 * `required_fields` is the engine's answer to "what can this driver not dial
 * without", in `ConnConfig`'s own vocabulary — the same words `store.Saved`
 * uses as its JSON keys. The connection dialog builds its form from this
 * rather than keeping a list of its own: a second list here could only ever
 * be a guess at a decision Go makes, and the two would disagree the first
 * time a driver's requirements were not what this file assumed.
 */
export interface DriverInfo {
  id: string;
  required_fields: string[];
  capabilities: Capabilities;
}

/** The code the engine uses for every database error. */
const CODE_DATABASE = -32020;

/**
 * Pulls the engine's normalized error out of a rejection. Returns null when
 * the rejection is anything else — a shell failure, a string, a bug — so the
 * caller can tell "the database said no" apart from "the plumbing broke".
 */
export function asDbError(err: unknown): DbError | null {
  if (typeof err !== 'object' || err === null) return null;
  const e = err as { code?: unknown; data?: unknown };
  if (e.code !== CODE_DATABASE) return null;
  const d = e.data as Partial<DbError> | undefined;
  if (!d || typeof d.kind !== 'string' || typeof d.message !== 'string') return null;
  // `native` and `query` are optional on the wire, so "absent" is valid — but
  // present-and-not-a-string is the same untrusted payload `kind` and
  // `message` are checked for, and the UI renders both of these directly.
  if (d.native !== undefined && typeof d.native !== 'string') return null;
  if (d.query !== undefined && typeof d.query !== 'string') return null;
  return d as DbError;
}

/**
 * One entry of the drivers.list payload as it actually arrived, or null when
 * it is not one.
 *
 * Same shape of check as `asDbError` above, for the same reason: `request<T>`
 * casts, so the `<T>` on the call below is a claim about the engine rather
 * than a check of it, and the connection dialog renders both of these fields
 * directly — `id` as a React child, `required_fields` through `.map`. Every
 * shape refused here was driven through that dialog and unmounted the tree.
 *
 * `capabilities` is deliberately NOT checked. Nothing in the shell reads it
 * yet, so refusing an otherwise dialable driver over a field nobody renders
 * would cost the user a working driver to protect a `.map` that does not
 * exist. Add the check with the first thing that reads it.
 */
function asDriverInfo(value: unknown): DriverInfo | null {
  if (typeof value !== 'object' || value === null) return null;
  const d = value as { id?: unknown; required_fields?: unknown };
  if (typeof d.id !== 'string') return null;
  // Absent or null is the nil slice Go's encoding/json writes for a driver
  // that requires nothing to dial — a legitimate answer, and the same
  // marshalling that blanked this window once before (see `asTables` in
  // Sidebar.tsx). Present-but-not-an-array is a different thing entirely:
  // there is no form to build out of it.
  const fields = d.required_fields ?? [];
  if (!Array.isArray(fields) || fields.some((f) => typeof f !== 'string')) return null;
  return { ...(value as DriverInfo), required_fields: fields };
}

/**
 * Every driver this engine has linked in, in a stable order — and nothing
 * else, whatever the engine put on the wire.
 *
 * A malformed ENTRY is dropped and the rest of the list stands: the drivers
 * either side of it are still dialable, and one unreadable entry is not a
 * reason to leave the user with no picker. A malformed RESPONSE — anything
 * that is not an array — rejects instead of degrading to an empty list,
 * because an empty list is a claim the dialog then makes out loud ("this
 * engine has no drivers registered") and that claim would be false: the
 * engine answered, with something this shell cannot read. Both endings land
 * on a path the dialog already draws and already tests.
 */
export async function listDrivers(): Promise<DriverInfo[]> {
  const payload = await request<unknown>('drivers.list');
  if (!Array.isArray(payload)) {
    // The same object shape `request` itself rejects with, so describeError
    // renders this one the way it renders every other seam failure rather
    // than falling through to `[object Object]`.
    throw {
      code: EngineErrorCode.Malformed,
      message: 'The engine answered drivers.list with something that is not a list of drivers.',
      data: payload,
    } satisfies EngineError;
  }
  const drivers: DriverInfo[] = [];
  for (const entry of payload) {
    const driver = asDriverInfo(entry);
    if (driver) drivers.push(driver);
  }
  return drivers;
}

export const listConnections = () => request<Connection[]>('connections.list');

export const saveConnection = (connection: NewConnection, password: string) =>
  request<Connection>('connections.save', { connection, password });

export const testConnection = (connection: NewConnection, password: string) =>
  request<TestResult>('connections.test', { connection, password });

export const deleteConnection = (id: string) =>
  request<{ deleted: boolean }>('connections.delete', { id });

export const openSession = (connectionId: string) =>
  request<OpenResult>('session.open', { connection_id: connectionId });

/**
 * The second introspection tier: one database's tables, read when the user
 * expands it rather than when the connection is opened.
 */
export const loadTables = (sessionId: string, database: string) =>
  request<Table[]>('session.tables', { session_id: sessionId, database });

export const loadColumns = (sessionId: string, database: string, table: string) =>
  request<Column[]>('session.columns', { session_id: sessionId, database, table });

export const closeSession = (sessionId: string) =>
  request<{ closed: boolean }>('session.close', { session_id: sessionId });
