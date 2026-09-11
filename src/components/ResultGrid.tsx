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

/**
 * How many pages of rows the grid holds at once (spec section 7.3: the
 * buffer "evicts its oldest window ... rather than growing memory without
 * bound"). Five pages is 1000 rows.
 *
 * Chosen against the two things that can go wrong, not by taste. Too small
 * and the grid thrashes: a window has to survive the viewport leaving it and
 * coming back, so the cap must comfortably exceed the number of windows one
 * screen can span — at the 26px density a screen is well under one 200-row
 * window, so five is roughly forty screenfuls of slack, and no ordinary
 * scroll, however fast, evicts a window it is about to re-enter. Too large
 * and the cap stops being one: 1000 rows of a wide table is a few megabytes,
 * which is a ceiling a laptop does not notice, while the unbounded version
 * this replaces reached the same size after five pages and kept going.
 */
export const MAX_BUFFERED_PAGES = 5;

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
 * One page of rows the grid is holding, at its absolute position.
 *
 * Every page except the last is exactly {@link BROWSE_PAGE_SIZE} rows — the
 * engine only ever returns fewer when the table ran out — so a window's
 * index is all that is needed to place its rows: window `i` covers rows
 * `i * BROWSE_PAGE_SIZE` onward. That is what keeps every buffered row
 * addressable by ABSOLUTE index (spec section 7.3) even after the windows
 * before it have been evicted.
 */
interface LoadedWindow {
  index: number;
  rows: Value[][];
}

/**
 * What the grid has of a table so far.
 *
 * One object rather than eight useStates so that every transition — a page
 * landing, a page failing, a table changing — is a single update that cannot
 * tear: "rows appended but the cursor not yet advanced" is a state that
 * would refetch the same cursor twice.
 */
interface GridState {
  columns: ColumnMeta[];
  /**
   * The windows currently held, oldest first, never more than
   * {@link MAX_BUFFERED_PAGES}. This is the capped buffer.
   */
  windows: LoadedWindow[];
  /**
   * `cursors[i]` is the page to continue from to reach window `i`, kept
   * whole because that is what {@link browsePage} continues from: `keyset`
   * and the `sort_token` it was issued for never exist here as two
   * separable values. `cursors[0]` is undefined — window 0 starts at the
   * top of the table.
   *
   * Kept for EVERY window, including evicted ones, which is what makes
   * eviction survivable: a keyset cursor is forward-only, so the only way
   * back into a window whose rows are gone is the cursor that opened it.
   * It costs a few values and a token per page against a page of rows, so
   * this list is not itself the unbounded growth the cap exists to stop.
   */
  cursors: (BrowsePage | undefined)[];
  /**
   * How many windows have been fetched at the leading edge — so `fetched`
   * is the index of the next one, and `fetched === 0` means nothing has
   * come back yet. That last reading is how "still loading the first page"
   * is told apart from "loaded, and the table is empty", a distinction the
   * empty state depends on.
   */
  fetched: number;
  /**
   * Rows known to exist. NOT the number buffered: the grid keeps counting
   * rows it has evicted, because a scrollbar that shrank when a window was
   * dropped would move the ground under the user.
   */
  total: number;
  exhausted: boolean;
  loading: boolean;
  error: ErrorDescription | null;
}

const INITIAL: GridState = {
  columns: [],
  windows: [],
  cursors: [undefined],
  fetched: 0,
  total: 0,
  exhausted: false,
  loading: true,
  error: null,
};

/**
 * Folds a landed page into the state, evicting the oldest window if that
 * takes the buffer past the cap.
 *
 * A page at the LEADING edge extends what the grid knows; one behind it is
 * a re-read of a window that was evicted and scrolled back into, and must
 * change nothing but the buffer — re-counting its rows would double them,
 * and taking its `exhausted` would end pagination in the middle of a table.
 */
function absorb(prev: GridState, index: number, page: BrowsePage): GridState {
  // `?? []` on both: Go marshals a nil slice as JSON null, and this is the
  // seam it crosses. BrowsePage.MarshalJSON now guarantees `[]` for Rows —
  // after a null there took the whole window blank once — and guarantees
  // nothing for Columns. Sidebar.tsx keeps the same guard for the same
  // reason: the contract has been proven not to enforce itself.
  const rows = page.rows ?? [];
  const leading = index === prev.fetched;
  return {
    columns: page.columns ?? [],
    // `.slice(-N)` is the eviction: the newest window goes on the end, and
    // anything past the cap falls off the front, oldest first.
    windows: [...prev.windows.filter((w) => w.index !== index), { index, rows }].slice(
      -MAX_BUFFERED_PAGES,
    ),
    cursors: leading ? [...prev.cursors, page] : prev.cursors,
    fetched: leading ? index + 1 : prev.fetched,
    total: leading ? index * BROWSE_PAGE_SIZE + rows.length : prev.total,
    exhausted: leading ? page.exhausted : prev.exhausted,
    loading: false,
    error: null,
  };
}

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

  /**
   * Which window the last request was for, and the cursor it went out with,
   * so Retry repeats exactly that request. A ref rather than state: it is
   * read only when Retry is pressed, and nothing renders from it.
   */
  const pending = useRef<{ index: number; after: BrowsePage | undefined }>({
    index: 0,
    after: undefined,
  });

  const fetchWindow = useCallback(
    async (index: number, after: BrowsePage | undefined) => {
      const gen = ++generation.current;
      pending.current = { index, after };
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
        setState((prev) => absorb(prev, index, page));
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

  // `fetchWindow` changes identity exactly when the table being browsed
  // does, so it is the whole dependency list: a new table starts from an
  // empty grid and window 0, and the bump inside fetchWindow orphans
  // whatever was in flight for the old one. The cursor is passed in rather
  // than read from state, because state has not re-rendered yet here.
  useEffect(() => {
    setState(INITIAL);
    setWidths({});
    void fetchWindow(0, undefined);
  }, [fetchWindow]);

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
      // Absolute index into the table, resolved through the buffer rather
      // than into one flat array: the windows before this one may have been
      // evicted, and the rows that are held must still answer at the
      // positions they actually occupy.
      const index = Math.floor(row / BROWSE_PAGE_SIZE);
      const window = stateRef.current.windows.find((w) => w.index === index);
      const value = window?.rows[row - index * BROWSE_PAGE_SIZE]?.[col];
      // Three things land here: a cell a shrinking row set no longer holds,
      // a ragged row from a misbehaving driver, and a window the cap has
      // evicted — which onVisibleRegionChanged is already re-fetching. A
      // skeleton is what all three look like, and it is glide's own "not
      // here yet" rather than a blank row. Throwing would blank the window,
      // since there is no error boundary above this.
      if (value === undefined) return { kind: GridCellKind.Loading, allowOverlay: false };
      return cellFor(value, tokens);
    },
    [tokens],
  );

  // Held in a ref so the viewport callback below can stay identity-stable
  // across a table change without ever calling a stale closure.
  const fetchWindowRef = useRef(fetchWindow);
  fetchWindowRef.current = fetchWindow;

  const onVisibleRegionChanged = useCallback((range: Rectangle) => {
    const { windows, cursors, fetched, total, exhausted, loading, error } = stateRef.current;
    if (fetched === 0) return; // nothing has come back to continue from
    if (loading) return; // one request at a time; the cursor is sequential
    if (error !== null) return; // a failing engine is not worth hammering — Retry is the way back

    // Scrolled back into a window the cap evicted. Re-fetching it is what
    // spec section 7.3's windowing implies — "scrolling back into evicted
    // territory re-fetches" — and it takes priority over reading further
    // ahead, because these rows are the ones the user is looking at right
    // now. The cursor that opened the window is what makes it possible; a
    // keyset cannot seek backwards on its own.
    const firstWindow = Math.floor(range.y / BROWSE_PAGE_SIZE);
    const lastWindow = Math.floor((range.y + range.height) / BROWSE_PAGE_SIZE);
    for (let index = firstWindow; index <= lastWindow && index < fetched; index++) {
      if (!windows.some((w) => w.index === index)) {
        void fetchWindowRef.current(index, cursors[index]);
        return;
      }
    }

    if (exhausted) return; // the engine said there is no more
    if (range.y + range.height < total - PREFETCH_ROWS) return;
    void fetchWindowRef.current(fetched, cursors[fetched]);
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
    // The request that failed, repeated exactly — which is the first window
    // when it was the first window that failed, the next one when a
    // continuation failed, and an evicted one when a scroll-back failed.
    void fetchWindow(pending.current.index, pending.current.after);
  }, [fetchWindow]);

  // Loaded, and there is genuinely nothing in the table. A grid that draws
  // nothing is indistinguishable from one that failed to load, so this says
  // so in words — over the top of the grid, which keeps the column headers
  // visible, because the table's shape is still worth seeing.
  const empty = state.fetched > 0 && state.total === 0;

  return (
    <div className="result-grid">
      <div className="result-grid-body" data-selectable>
        <DataEditor
          columns={columns}
          // Every row the grid knows of, buffered or evicted: the scrollbar
          // describes the table, not the buffer.
          rows={state.total}
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
