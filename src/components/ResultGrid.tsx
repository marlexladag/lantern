import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import {
  DataEditor,
  GridCellKind,
  type GridCell,
  type GridColumn,
  type Item,
  type Rectangle,
  type Theme,
} from '@glideapps/glide-data-grid';
import '@glideapps/glide-data-grid/dist/index.css';
import { browsePage, type BrowsePage, type ColumnMeta, type Value } from '../lib/browse';
import { describeError, type ErrorDescription } from '../lib/errors';
import { ErrorText } from './ErrorText';
// Imported here, not left to App.tsx, because this component does not use the
// tokens through var() — it READS them and hands the resolved strings to a
// canvas, which cannot resolve a custom property itself. A missing token
// would therefore be a NaN row height rather than an unstyled div, so the
// stylesheet is this module's own dependency. Vite dedupes the import.
import '../styles/tokens.css';
import './ResultGrid.css';

/**
 * Rows per `browse.page` request.
 *
 * Below `MAX_BROWSE_LIMIT` (1000) by a wide margin and deliberately so: the
 * limit is the engine's ceiling on one round trip, not a target. 200 rows is
 * roughly eight screenfuls at the 26px density, which is enough that
 * scrolling stays ahead of the fetch, while keeping the first paint after
 * clicking a table down to one small query rather than one large one.
 */
export const BROWSE_PAGE_SIZE = 200;

/**
 * How close to the end of the loaded rows the viewport has to come before
 * the next page is requested. A quarter of a page: far enough ahead that a
 * round trip finishes before the user scrolls into empty space, near enough
 * that opening a table does not immediately fetch a second page nobody
 * asked for.
 */
const PREFETCH_ROWS = 50;

export interface ResultGridProps {
  sessionId: string;
  database: string;
  table: string;
}

/**
 * The design tokens this grid needs as literal values.
 *
 * A canvas has no cascade: `ctx.font` and `ctx.fillStyle` take resolved
 * strings, so `var(--color-dim)` is meaningless to it. Everything here is
 * read back out of tokens.css through getComputedStyle rather than
 * duplicated, so the grid changes when the tokens do — including under
 * `prefers-color-scheme`, which is why {@link ResultGrid} re-reads them on a
 * scheme change instead of reading once at mount.
 */
interface GridTokens {
  theme: Partial<Theme>;
  rowHeight: number;
  headerHeight: number;
  columnWidth: number;
  dim: string;
  /**
   * The italic form of the cell font, for NULL. Glide composes the canvas
   * font as `${baseFontStyle} ${fontFamily}`, so the italic keyword has to
   * ride in the style half — there is no separate slot for it.
   */
  nullFontStyle: string;
}

function readGridTokens(): GridTokens {
  const root = getComputedStyle(document.documentElement);
  const token = (name: string) => root.getPropertyValue(name).trim();
  const px = (name: string) => Number.parseFloat(token(name));

  const body = token('--text-body');
  const dim = token('--color-dim');
  const line = token('--color-line');
  const hair = token('--color-hair');

  return {
    dim,
    nullFontStyle: `italic ${body}`,
    rowHeight: px('--row-data'),
    headerHeight: px('--row-header'),
    columnWidth: px('--width-grid-column'),
    theme: {
      accentColor: token('--color-accent'),
      // The foreground ON the accent — a checkbox tick, a marker glyph.
      // Foundations has no token for it (the design's primary button
      // hardcodes #fff), so the lightest surface stands in: #fbfaf8 on
      // #24707a is 5.6:1, and in dark it is #212020 on #57aab2 at 7.4:1.
      accentFg: token('--color-surface'),
      accentLight: token('--color-sel'),
      textDark: token('--color-text'),
      textMedium: dim,
      textLight: token('--color-faint'),
      textBubble: token('--color-text'),
      bgIconHeader: dim,
      fgIconHeader: token('--color-surface'),
      textHeader: dim,
      textHeaderSelected: token('--color-text'),
      bgCell: token('--color-surface'),
      bgCellMedium: token('--color-alt'),
      bgHeader: token('--color-alt'),
      bgHeaderHasFocus: token('--color-alt'),
      bgHeaderHovered: hair,
      bgBubble: token('--color-alt'),
      bgBubbleSelected: token('--color-sel'),
      bgSearchResult: token('--color-warn-bg'),
      // The design draws the vertical rule between cells and the rule under
      // a row in --hair, and only the rule under the HEADER in --line.
      borderColor: hair,
      horizontalBorderColor: hair,
      headerBottomBorderColor: line,
      drilldownBorder: line,
      linkColor: token('--color-accent'),
      cellHorizontalPadding: px('--gutter-cell'),
      headerFontStyle: `500 ${token('--text-label')}`,
      baseFontStyle: body,
      markerFontStyle: token('--text-label'),
      // Mono for the whole grid, header included. Glide has one family for
      // both, and values are what the choice is for: Foundations calls mono
      // "not decorative — column alignment". Identifiers reading in mono is
      // already how the sidebar renders column names, so the header follows
      // rather than fights it.
      fontFamily: token('--font-mono'),
      editorFontSize: body,
    },
  };
}

/**
 * One engine value as the grid should draw it.
 *
 * Every cell is a Text cell, including the numeric ones. Glide's Number cell
 * carries its value as a JavaScript number, which would undo the whole
 * reason `Value.text` is a string: a JSON number is a float64, and an int64
 * primary key or a DECIMAL above 2^53 loses digits passing through one. The
 * alignment is the only thing the numeric kinds actually need, and
 * `contentAlign` gives that without touching the value.
 */
function cellFor(value: Value, tokens: GridTokens): GridCell {
  switch (value.kind) {
    case 'null':
      // Never an empty cell, and never the four-character text "null" —
      // both are values a column can genuinely hold, and `kind` is the only
      // thing that tells the three apart.
      return {
        kind: GridCellKind.Text,
        // `data`, not just `displayData`: glide draws `displayData` on the
        // canvas but feeds `data` to both the clipboard and the accessibility
        // tree it renders for screen readers (textCellRenderer's
        // getAccessibilityString is `c.data`). Leaving `data` empty made the
        // canvas say NULL while a screen reader read an empty cell — the very
        // confusion this branch exists to prevent, reintroduced one layer
        // down. Found by loading the built bundle in a real browser, not by a
        // test. An untyped clipboard cannot carry "this was NULL" either way,
        // so all three surfaces say the same word.
        data: 'NULL',
        displayData: 'NULL',
        allowOverlay: false,
        themeOverride: { textDark: tokens.dim, baseFontStyle: tokens.nullFontStyle },
      };
    case 'int':
    case 'float':
      return {
        kind: GridCellKind.Text,
        data: value.text,
        displayData: value.text,
        contentAlign: 'right',
        allowOverlay: true,
        readonly: true,
      };
    case 'bytes':
      // `text` is already the engine's summary ("1024 bytes"); the content
      // never crossed the wire, so there is nothing an overlay could reveal.
      return {
        kind: GridCellKind.Text,
        data: value.text,
        displayData: value.text,
        allowOverlay: false,
        themeOverride: { textDark: tokens.dim },
      };
    default:
      // The overlay is how a value wider than its column gets read in full,
      // and selected as real text — a canvas cell cannot be dragged over.
      return {
        kind: GridCellKind.Text,
        data: value.text,
        displayData: value.text,
        allowOverlay: true,
        readonly: true,
      };
  }
}

/**
 * What the grid has of a table so far.
 *
 * One object rather than five useStates so that every transition — a page
 * landing, a page failing, a table changing — is a single update that cannot
 * tear: "rows appended but `page` not yet advanced" is a state that would
 * refetch the same cursor twice.
 */
interface GridState {
  columns: ColumnMeta[];
  rows: Value[][];
  /**
   * The most recent page that came back, kept whole because that is what
   * {@link browsePage} continues from: `keyset` and the `sort_token` it was
   * issued for never exist here as two separable values.
   *
   * Undefined means no page has succeeded yet, which is also how "still
   * loading the first one" is told apart from "loaded, and the table is
   * empty" — a distinction the empty state depends on.
   */
  page?: BrowsePage;
  loading: boolean;
  error: ErrorDescription | null;
}

const INITIAL: GridState = { columns: [], rows: [], loading: true, error: null };

export function ResultGrid({ sessionId, database, table }: ResultGridProps) {
  const [state, setState] = useState<GridState>(INITIAL);
  const [widths, setWidths] = useState<Record<string, number>>({});
  const [tokens, setTokens] = useState<GridTokens>(readGridTokens);
  const portalRef = useRef<HTMLDivElement>(null);

  // Read by the viewport callback, which must not be rebuilt on every state
  // change: glide re-renders the whole grid when a prop identity moves.
  const stateRef = useRef(state);
  stateRef.current = state;

  /**
   * Which request the component is currently willing to accept. Bumped by
   * every fetch, so a page for the table the user just navigated away from
   * resolves into a generation nobody is listening to and is dropped
   * instead of appending itself to a different table's rows.
   */
  const generation = useRef(0);

  const fetchPage = useCallback(
    async (after: BrowsePage | undefined) => {
      const gen = ++generation.current;
      setState((prev) => ({ ...prev, loading: true, error: null }));
      try {
        const page = await browsePage(sessionId, {
          database,
          table,
          limit: BROWSE_PAGE_SIZE,
          after,
          // Echoed unconditionally: a key-paginating driver ignores it (and
          // leaves `offset` at 0 anyway), and an offset-paginating one
          // issues no keyset, so `after` reduces to nothing and this is the
          // only cursor there is. One expression covers both drivers.
          offset: after?.offset,
        });
        if (gen !== generation.current) return;
        setState((prev) => ({
          // `?? []` on both: Go marshals a nil slice as JSON null, and this
          // is the seam it crosses. BrowsePage.MarshalJSON now guarantees
          // `[]` for Rows — after a null there took the whole window blank
          // once — and guarantees nothing for Columns. Sidebar.tsx keeps the
          // same guard for the same reason: the contract has been proven not
          // to enforce itself.
          columns: page.columns ?? [],
          rows: after === undefined ? (page.rows ?? []) : [...prev.rows, ...(page.rows ?? [])],
          page,
          loading: false,
          error: null,
        }));
      } catch (err) {
        if (gen !== generation.current) return;
        const described = describeError(err);
        setState((prev) => ({
          ...prev,
          loading: false,
          // A canceled request is not a failure to report (spec §11). The
          // rows already on screen stay either way: losing a screenful of
          // data because page four failed is worse than the failure.
          error: described.kind === 'canceled' ? null : described,
        }));
      }
    },
    [sessionId, database, table],
  );

  // `fetchPage` changes identity exactly when the table being browsed does,
  // so it is the whole dependency list: a new table starts from an empty
  // grid and the first page, and the bump inside fetchPage orphans whatever
  // was in flight for the old one.
  useEffect(() => {
    setState(INITIAL);
    setWidths({});
    void fetchPage(undefined);
  }, [fetchPage]);

  // The canvas cannot inherit a media query. Without this the grid stays
  // painted in light colours the moment the OS flips to dark, while every
  // other surface in the app follows tokens.css automatically.
  useEffect(() => {
    const query = window.matchMedia('(prefers-color-scheme: dark)');
    const reread = () => setTokens(readGridTokens());
    query.addEventListener('change', reread);
    return () => query.removeEventListener('change', reread);
  }, []);

  const getCellContent = useCallback(
    ([col, row]: Item): GridCell => {
      const value = stateRef.current.rows[row]?.[col];
      // Glide can ask for a cell a shrinking row set no longer holds, and a
      // ragged row from a misbehaving driver lands here identically. A
      // skeleton is what "not here yet" looks like; throwing would blank the
      // window, since there is no error boundary above this.
      if (value === undefined) return { kind: GridCellKind.Loading, allowOverlay: false };
      return cellFor(value, tokens);
    },
    [tokens],
  );

  // Held in a ref so the viewport callback below can stay identity-stable
  // across a table change without ever calling a stale closure.
  const fetchPageRef = useRef(fetchPage);
  fetchPageRef.current = fetchPage;

  const onVisibleRegionChanged = useCallback((range: Rectangle) => {
    const { rows, page, loading, error } = stateRef.current;
    if (page === undefined) return; // nothing has come back to continue from
    if (page.exhausted) return; // the engine said there is no more
    if (loading) return; // one request at a time; the cursor is sequential
    if (error !== null) return; // a failing engine is not worth hammering — Retry is the way back
    if (range.y + range.height < rows.length - PREFETCH_ROWS) return;
    void fetchPageRef.current(page);
  }, []);

  const onColumnResize = useCallback((column: GridColumn, newSize: number) => {
    // `id` is optional on GridColumn in general; every column this component
    // builds carries the engine's column name as one, and glide hands back
    // the very object it was given.
    setWidths((prev) => ({ ...prev, [column.id as string]: newSize }));
  }, []);

  const columns: GridColumn[] = useMemo(
    () =>
      state.columns.map((column) => ({
        id: column.name,
        title: column.name,
        width: widths[column.name] ?? tokens.columnWidth,
      })),
    [state.columns, widths, tokens.columnWidth],
  );

  const retry = useCallback(() => {
    // state.page is the last page that succeeded, so this continues from
    // wherever the failure interrupted — or restarts from the first page
    // when it was the first page that failed.
    void fetchPage(stateRef.current.page);
  }, [fetchPage]);

  // Loaded, and there is genuinely nothing in the table. A grid that draws
  // nothing is indistinguishable from one that failed to load, so this says
  // so in words — over the top of the grid, which keeps the column headers
  // visible, because the table's shape is still worth seeing.
  const empty = state.page !== undefined && state.rows.length === 0;

  return (
    <div className="result-grid">
      <div className="result-grid-body" data-selectable>
        <DataEditor
          columns={columns}
          rows={state.rows.length}
          getCellContent={getCellContent}
          onVisibleRegionChanged={onVisibleRegionChanged}
          onColumnResize={onColumnResize}
          rowHeight={tokens.rowHeight}
          headerHeight={tokens.headerHeight}
          theme={tokens.theme}
          portalElementRef={portalRef as RefObject<HTMLElement>}
          // A canvas cell has no DOM text to drag across, so selection and
          // ⌘C are the grid's own; this is what lets it answer them.
          getCellsForSelection
          rowMarkers="none"
          smoothScrollX
          smoothScrollY
          width="100%"
          height="100%"
        />
        {empty && (
          <div className="result-grid-empty" role="status">
            No rows
          </div>
        )}
      </div>
      {state.loading && <div className="result-grid-status">Loading…</div>}
      {state.error && (
        <div role="alert" className="result-grid-error">
          <ErrorText description={state.error} />
          <button type="button" className="result-grid-retry" onClick={retry}>
            Retry
          </button>
        </div>
      )}
      {/*
        Glide portals its overlay editor into `document.getElementById("portal")`
        unless given somewhere else, and logs an error when there is none.
        Owning one here keeps the component mountable on its own rather than
        requiring a div in index.html that nothing else explains.
      */}
      <div className="result-grid-portal" ref={portalRef} data-selectable />
    </div>
  );
}
