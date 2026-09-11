/**
 * Typed client for the engine's table-browsing method.
 *
 * Everything funnels through `request` in ./engine, which is the single
 * `engine_request` command. Do not add a second channel.
 */
import { request } from './engine';

/**
 * One cell's engine-neutral kind, mirroring `driver.ValueKind` in
 * internal/engine/driver/value.go — what the grid branches on: numbers
 * right-align, NULL renders italic and dim, bytes refuse to render inline.
 * browse.test.ts checks this list against the Go one by parsing both,
 * rather than by eye, the same way connections.test.ts already does for
 * DbErrorKind.
 */
export type ValueKind = 'null' | 'text' | 'int' | 'float' | 'bool' | 'bytes' | 'time';

/**
 * One cell, in engine-neutral terms, mirroring `driver.Value`.
 *
 * `text` is always the display form — never a typed payload — because a
 * JSON number is a float64 and would silently destroy an int64 or a
 * DECIMAL's precision above 2^53. `kind` is what lets the grid tell a real
 * NULL apart from the empty string, and the text "null" apart from both.
 */
export interface Value {
  kind: ValueKind;
  text: string;
}

/** One ORDER BY term, mirroring `driver.SortKey`. */
export interface SortKey {
  column: string;
  desc: boolean;
}

/** One column of a result set, mirroring `driver.ColumnMeta`. */
export interface ColumnMeta {
  name: string;
  data_type: string;
}

/**
 * The engine's cap on a single page, mirroring `driver.MaxBrowseLimit`. An
 * unbounded limit is how a UI bug becomes an out-of-memory crash on a table
 * with a hundred million rows.
 */
export const MAX_BROWSE_LIMIT = 1000;

/**
 * One page of rows, exactly as `browse.page` returns it — mirrors
 * `driver.BrowsePage`.
 *
 * `sort_token` is issued alongside `keyset` and names — opaquely, this
 * client never decodes it — the sort that produced it. It stays on this
 * type, in full, rather than being stripped out for the UI: {@link
 * browsePage}'s `after` parameter takes this whole object back, precisely
 * so a caller continuing a page is never handed `keyset` and `sort_token`
 * as two separate values it could assemble incorrectly. The engine refuses
 * a keyset replayed under a mismatched sort_token — that pairing is what
 * turned eight Go tests red the day it became mandatory, every one of them
 * because a test helper built a continuation from the keyset alone. Both
 * are absent exactly when the driver paginated by offset instead, or when
 * the page is exhausted.
 */
export interface BrowsePage {
  columns: ColumnMeta[];
  rows: Value[][];
  keyset?: Value[];
  sort_token?: string;
  exhausted: boolean;
  offset: number;
}

/**
 * Asks for one page of a table, semantically — no SQL, mirroring
 * `driver.BrowseRequest`.
 *
 * `sort` is empty for the driver's stable default (its primary key); a
 * request may carry a non-empty `after` alongside an empty `sort` — that is
 * the ordinary shape of a second page when the caller never chose a sort,
 * not a contradiction, since the driver applies the same default sort both
 * times.
 *
 * `after` is the previous page in full, not a bare keyset array — see
 * {@link BrowsePage}'s own doc comment for why. Omit it for the first page.
 *
 * `offset` only matters to a driver that told the caller it could not
 * paginate by key, in which case the previous page's own `offset` is
 * echoed back directly: unlike a keyset, a bare offset carries no paired
 * secret a caller could separate it from.
 */
export interface BrowseRequest {
  database: string;
  table: string;
  sort?: SortKey[];
  after?: BrowsePage;
  offset?: number;
  limit: number;
}

/** Fetches one page of a table's rows through `browse.page`. */
export function browsePage(sessionId: string, req: BrowseRequest): Promise<BrowsePage> {
  const { after, ...rest } = req;
  return request<BrowsePage>('browse.page', {
    session_id: sessionId,
    ...rest,
    after: after?.keyset,
    sort_token: after?.sort_token,
  });
}
