/**
 * Typed client for the engine's connection and session methods.
 *
 * Everything funnels through `request` in ./engine, which is the single
 * `engine_request` command. Do not add a second channel.
 */
import { request } from './engine';

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

export interface Catalog {
  databases: { name: string; tables: Table[] }[];
}

export interface TestResult {
  ok: boolean;
  kind?: DbErrorKind;
  error?: string;
}

export interface OpenResult {
  session_id: string;
  catalog: Catalog;
  capabilities: {
    transactions: boolean;
    multiple_databases: boolean;
    editable_rows: boolean;
  };
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

export const listConnections = () => request<Connection[]>('connections.list');

export const saveConnection = (connection: NewConnection, password: string) =>
  request<Connection>('connections.save', { connection, password });

export const testConnection = (connection: NewConnection, password: string) =>
  request<TestResult>('connections.test', { connection, password });

export const deleteConnection = (id: string) =>
  request<{ deleted: boolean }>('connections.delete', { id });

export const openSession = (connectionId: string) =>
  request<OpenResult>('session.open', { connection_id: connectionId });

export const loadColumns = (sessionId: string, database: string, table: string) =>
  request<Column[]>('session.columns', { session_id: sessionId, database, table });

export const closeSession = (sessionId: string) =>
  request<{ closed: boolean }>('session.close', { session_id: sessionId });
