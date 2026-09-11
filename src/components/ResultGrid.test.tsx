import { it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
// Pulls the real user-select policy into this document — the rule under test
// is a cascade, not a value a component sets, the same reason
// Sidebar.test.tsx imports it. ResultGrid.tsx imports tokens.css itself,
// because it reads those tokens programmatically rather than through var().
import '../styles/chrome.css';

/**
 * The grid's last-rendered props, captured by the stand-in below.
 *
 * `vi.hoisted` because `vi.mock` factories are lifted above this file's own
 * top-level declarations: a plain `const` would still be in its temporal
 * dead zone when the factory runs.
 */
const harness = vi.hoisted(() => ({ props: undefined as Record<string, any> | undefined }));

/**
 * WHAT THIS MOCK GIVES UP, stated plainly because the coverage number will
 * not say it: glide-data-grid draws to a canvas and creates no DOM node per
 * cell, and jsdom has no 2D context at all (`getContext` is "not
 * implemented"). So the real DataEditor cannot run in these tests, and
 * nothing below proves that anything is *painted*, that hit-testing works,
 * that scrolling is smooth, or that ⌘C reaches the clipboard.
 *
 * What it does prove is the seam this component owns: the GridCell objects
 * it derives from engine `Value`s, the column list, the theme and row
 * metrics it hands the grid, and the paging/error state machine driven
 * through `onVisibleRegionChanged`. The stand-in calls the component's own
 * `getCellContent` for every cell and projects the result into DOM, so the
 * assertions read against what the canvas WOULD paint.
 *
 * Everything else in the module is the real one (`importActual`), so
 * `GridCellKind` is glide's own enum rather than a copy that could drift.
 */
vi.mock('@glideapps/glide-data-grid', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('@glideapps/glide-data-grid');
  return {
    ...actual,
    DataEditor: (props: any) => {
      harness.props = props;
      const cells = [];
      for (let row = 0; row < props.rows; row++) {
        for (let col = 0; col < props.columns.length; col++) {
          const cell = props.getCellContent([col, row]);
          cells.push(
            <div
              key={`${col}.${row}`}
              data-testid={`cell-${col}-${row}`}
              data-cell-kind={cell.kind}
              data-align={cell.contentAlign ?? 'left'}
              data-color={cell.themeOverride?.textDark ?? ''}
              data-font={cell.themeOverride?.baseFontStyle ?? ''}
              data-value={cell.data ?? ''}
              data-copy={cell.copyData ?? cell.data ?? ''}
              data-overlay={String(cell.allowOverlay)}
            >
              {cell.displayData}
            </div>,
          );
        }
      }
      return (
        <div data-testid="data-editor">
          {props.columns.map((c: any) => (
            <div key={c.id} data-testid={`header-${c.id}`} data-width={String(c.width)}>
              {c.title}
            </div>
          ))}
          {cells}
        </div>
      );
    },
  };
});

vi.mock('../lib/browse', async () => {
  const actual = await vi.importActual<typeof import('../lib/browse')>('../lib/browse');
  return { ...actual, browsePage: vi.fn() };
});

import { browsePage, type BrowsePage, type ColumnMeta, type Value, type ValueKind } from '../lib/browse';
import { ResultGrid, BROWSE_PAGE_SIZE } from './ResultGrid';

const browsePageMock = vi.mocked(browsePage);

/**
 * jsdom implements no `window.matchMedia` at all, so the component's
 * colour-scheme listener has nothing to attach to here. This is the test
 * environment falling short of a browser API every browser has, not a case
 * the component should defend against — so the fake goes in the tests
 * rather than an optional-chain in the component. Its captured listeners
 * are what the colour-scheme test fires.
 */
const media = { listeners: [] as (() => void)[], removed: 0 };

// Braces, not a bare expression: a beforeEach that RETURNS a value has it
// treated as teardown, and `mockReset()` returns the mock itself — the trap
// browse.test.ts already documents.
beforeEach(() => {
  browsePageMock.mockReset();
  harness.props = undefined;
  media.listeners = [];
  media.removed = 0;
  vi.stubGlobal(
    'matchMedia',
    vi.fn(() => ({
      matches: false,
      addEventListener: (_: string, listener: () => void) => media.listeners.push(listener),
      removeEventListener: () => (media.removed += 1),
    })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.documentElement.removeAttribute('style');
});

function v(kind: ValueKind, text = ''): Value {
  return { kind, text };
}

const ID_COLUMN: ColumnMeta = { name: 'id', data_type: 'INTEGER' };

function makePage(over: Partial<BrowsePage> = {}): BrowsePage {
  return { columns: [ID_COLUMN], rows: [], exhausted: true, offset: 0, ...over };
}

/** A full page of `id` values, so the prefetch threshold can be crossed. */
function fullPage(over: Partial<BrowsePage> = {}): BrowsePage {
  return makePage({
    rows: Array.from({ length: BROWSE_PAGE_SIZE }, (_, i) => [v('int', String(i))]),
    keyset: [v('int', String(BROWSE_PAGE_SIZE - 1))],
    sort_token: 'tok-abc123',
    exhausted: false,
    ...over,
  });
}

function mount(over: { sessionId?: string; database?: string; table?: string } = {}) {
  return render(
    <ResultGrid
      sessionId={over.sessionId ?? 's1'}
      database={over.database ?? 'main'}
      table={over.table ?? 'users'}
    />,
  );
}

/** The token's own value, so no assertion below hardcodes a colour. */
function token(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

async function settled() {
  await waitFor(() => expect(harness.props).toBeDefined());
}

/** Drives the grid's viewport callback as glide would while scrolling. */
function scrollTo(firstRow: number, height = 20) {
  act(() => {
    harness.props!.onVisibleRegionChanged({ x: 0, y: firstRow, width: 1, height });
  });
}

it('asks for the first page with no cursor and renders its columns and rows', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [ID_COLUMN, { name: 'email', data_type: 'TEXT' }],
      rows: [[v('int', '1041'), v('text', 'ama.osei@example.com')]],
    }),
  );

  mount();

  expect((await screen.findByTestId('header-id')).textContent).toBe('id');
  expect(screen.getByTestId('header-email').textContent).toBe('email');
  expect(screen.getByTestId('cell-0-0').textContent).toBe('1041');
  expect(screen.getByTestId('cell-1-0').textContent).toBe('ama.osei@example.com');
  expect(browsePageMock).toHaveBeenCalledWith('s1', {
    database: 'main',
    table: 'users',
    limit: BROWSE_PAGE_SIZE,
  });
});

it('renders a NULL as the literal NULL, dim and italic — never as an empty cell', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('null')]] }));

  mount();
  await settled();

  const cell = screen.getByTestId('cell-0-0');
  expect(cell.textContent).toBe('NULL');
  expect(cell.textContent).not.toBe('');
  expect(cell.getAttribute('data-color')).toBe(token('--color-dim'));
  expect(cell.getAttribute('data-font')).toContain('italic');
});

// The whole reason ValueKind exists. Three cells that a grid guessing from
// the text alone would render identically, or wrongly.
it('tells a NULL apart from an empty string and from the four-character text "null"', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [ID_COLUMN, { name: 'a', data_type: 'TEXT' }, { name: 'b', data_type: 'TEXT' }],
      rows: [[v('null'), v('text', ''), v('text', 'null')]],
    }),
  );

  mount();
  await settled();

  const nul = screen.getByTestId('cell-0-0');
  const empty = screen.getByTestId('cell-1-0');
  const literal = screen.getByTestId('cell-2-0');

  expect(nul.textContent).toBe('NULL');
  expect(empty.textContent).toBe('');
  expect(literal.textContent).toBe('null');
  // Distinct in treatment too, not only in text: only the real NULL is dim
  // and italic.
  expect(empty.getAttribute('data-color')).toBe('');
  expect(literal.getAttribute('data-color')).toBe('');
  expect(literal.getAttribute('data-font')).toBe('');
});

it('right-aligns ints and floats and leaves everything else alone', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [
        ID_COLUMN,
        { name: 'ratio', data_type: 'REAL' },
        { name: 'name', data_type: 'TEXT' },
        { name: 'ok', data_type: 'BOOLEAN' },
        { name: 'at', data_type: 'TIMESTAMP' },
      ],
      rows: [[v('int', '1041'), v('float', '0.5'), v('text', 'Ama'), v('bool', 'true'), v('time', '2024-01-14T09:02:00Z')]],
    }),
  );

  mount();
  await settled();

  expect(screen.getByTestId('cell-0-0').getAttribute('data-align')).toBe('right');
  expect(screen.getByTestId('cell-1-0').getAttribute('data-align')).toBe('right');
  expect(screen.getByTestId('cell-2-0').getAttribute('data-align')).toBe('left');
  expect(screen.getByTestId('cell-3-0').getAttribute('data-align')).toBe('left');
  expect(screen.getByTestId('cell-4-0').getAttribute('data-align')).toBe('left');
});

it('renders a bytes cell as the engine summary, dim, and never opens it', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('bytes', '1024 bytes')]] }));

  mount();
  await settled();

  const cell = screen.getByTestId('cell-0-0');
  expect(cell.textContent).toBe('1024 bytes');
  expect(cell.getAttribute('data-color')).toBe(token('--color-dim'));
  // Nothing behind the summary worth opening: the content never crossed the
  // wire in the first place.
  expect(cell.getAttribute('data-overlay')).toBe('false');
});

// Adversarial: one 8 KB value must not widen its column or leak into the
// next one. The grid's column widths come from the token, never from the
// content, so this is a real assertion rather than a screenshot.
it('passes a very long value through intact without letting it resize the column', async () => {
  const long = 'x'.repeat(8192);
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [{ name: 'blob_text', data_type: 'TEXT' }],
      rows: [[v('text', long)]],
    }),
  );

  mount();
  await settled();

  expect(screen.getByTestId('cell-0-0').textContent).toBe(long);
  expect(screen.getByTestId('header-blob_text').getAttribute('data-width')).toBe(
    String(Number.parseFloat(token('--width-grid-column'))),
  );
});

// Adversarial: a column whose every value is NULL. The column must still be
// there — a driver that reports no declared type for such a column is
// exactly why ColumnMeta comes from the catalog rather than the page.
it('renders a column whose every value is NULL', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [ID_COLUMN, { name: 'last_login_at', data_type: 'TIMESTAMP' }],
      rows: [
        [v('int', '1'), v('null')],
        [v('int', '2'), v('null')],
        [v('int', '3'), v('null')],
      ],
    }),
  );

  mount();
  await settled();

  expect(screen.getByTestId('header-last_login_at').textContent).toBe('last_login_at');
  for (const row of [0, 1, 2]) {
    expect(screen.getByTestId(`cell-1-${row}`).textContent).toBe('NULL');
  }
});

// Adversarial: a grid that draws nothing is indistinguishable from one that
// failed to load, so zero rows must say so in words — while still showing
// the columns, which are what tell you the table's shape.
it('says so explicitly when a page has zero rows, and keeps the columns visible', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [] }));

  mount();

  expect((await screen.findByText('No rows')).textContent).toBe('No rows');
  expect(screen.getByTestId('header-id').textContent).toBe('id');
});

it('shows no empty state while the first page is still in flight', async () => {
  browsePageMock.mockReturnValue(new Promise(() => {}));

  mount();

  await waitFor(() => expect(screen.getByText('Loading…')).toBeDefined());
  expect(screen.queryByText('No rows')).toBeNull();
});

it('fetches the next page with the previous page itself once the viewport nears the end', async () => {
  const first = fullPage();
  const second = makePage({ rows: [[v('int', '999')]], exhausted: true, offset: 0 });
  browsePageMock.mockResolvedValueOnce(first).mockResolvedValueOnce(second);

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 30);

  await waitFor(() => expect(browsePageMock).toHaveBeenCalledTimes(2));
  // The WHOLE page, not a bare keyset: browsePage pairs `keyset` with the
  // `sort_token` it was issued for, and the engine refuses a cursor replayed
  // under a mismatched token.
  expect(browsePageMock.mock.calls[1]).toEqual([
    's1',
    { database: 'main', table: 'users', limit: BROWSE_PAGE_SIZE, after: first, offset: first.offset },
  ]);
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE + 1));
});

it('does not fetch while the viewport is still far from the end', async () => {
  browsePageMock.mockResolvedValue(fullPage());

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(0);

  expect(browsePageMock).toHaveBeenCalledTimes(1);
});

it('stops asking once a page comes back exhausted', async () => {
  browsePageMock.mockResolvedValue(fullPage({ exhausted: true }));

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 10);
  scrollTo(BROWSE_PAGE_SIZE - 5);

  expect(browsePageMock).toHaveBeenCalledTimes(1);
});

it('keeps one request in flight at a time however hard the viewport moves', async () => {
  browsePageMock.mockResolvedValueOnce(fullPage()).mockReturnValue(new Promise(() => {}));

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 30);
  scrollTo(BROWSE_PAGE_SIZE - 20);
  scrollTo(BROWSE_PAGE_SIZE - 10);

  await waitFor(() => expect(browsePageMock).toHaveBeenCalledTimes(2));
  expect(browsePageMock).toHaveBeenCalledTimes(2);
});

// Losing a screenful of data because page four failed is worse than the
// failure itself.
it('keeps the rows it already has when a later page fails, and offers a retry', async () => {
  const first = fullPage();
  browsePageMock
    .mockResolvedValueOnce(first)
    .mockRejectedValueOnce({ code: -32001, message: 'the engine stopped responding' })
    .mockResolvedValueOnce(makePage({ rows: [[v('int', '999')]], exhausted: true }));

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 10);

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toContain('the engine stopped responding');
  // Never `[object Object]`: request() only ever rejects with an object, and
  // describeError is the only thing that renders one correctly.
  expect(alert.textContent).not.toContain('[object Object]');
  expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE);
  expect(screen.getByTestId('cell-0-0').textContent).toBe('0');

  screen.getByRole('button', { name: 'Retry' }).click();

  await waitFor(() => expect(browsePageMock).toHaveBeenCalledTimes(3));
  expect(browsePageMock.mock.calls[2][1]).toEqual({
    database: 'main',
    table: 'users',
    limit: BROWSE_PAGE_SIZE,
    after: first,
    offset: first.offset,
  });
  await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
});

it('does not keep asking after a failure until the retry is pressed', async () => {
  browsePageMock
    .mockResolvedValueOnce(fullPage())
    .mockRejectedValue({ code: -32001, message: 'the engine stopped responding' });

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 10);
  await screen.findByRole('alert');

  scrollTo(BROWSE_PAGE_SIZE - 5);
  scrollTo(BROWSE_PAGE_SIZE - 1);

  expect(browsePageMock).toHaveBeenCalledTimes(2);
});

it('retries the first page when it is the first page that failed', async () => {
  browsePageMock
    .mockRejectedValueOnce({ code: -32001, message: 'the engine died' })
    .mockResolvedValueOnce(makePage({ rows: [[v('int', '1')]] }));

  mount();

  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toContain('the engine died');
  // A failed first page is NOT an empty table, and must not claim to be one.
  expect(screen.queryByText('No rows')).toBeNull();

  screen.getByRole('button', { name: 'Retry' }).click();

  await waitFor(() => expect(screen.getByTestId('cell-0-0').textContent).toBe('1'));
});

it('renders the driver`s own text behind a disclosure when it differs', async () => {
  browsePageMock.mockRejectedValue({
    code: -32020,
    message: 'boom',
    data: {
      kind: 'invalid',
      message: 'the database reported an error',
      native: 'SQLITE_ERROR: no such table: users',
    },
  });

  mount();

  await screen.findByRole('alert');
  expect(screen.getByText('Details').tagName).toBe('SUMMARY');
  expect(screen.getByText('SQLITE_ERROR: no such table: users')).toBeDefined();
});

// A canceled request is not a failure to report (spec §11).
it('renders nothing at all for a canceled page', async () => {
  browsePageMock
    .mockResolvedValueOnce(fullPage())
    .mockRejectedValueOnce({
      code: -32020,
      message: 'boom',
      data: { kind: 'canceled', message: 'the request was canceled' },
    });

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 10);

  await waitFor(() => expect(browsePageMock).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.queryByText('the request was canceled')).toBeNull();
  expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE);
});

it('starts over when the table changes and ignores the page still in flight for the old one', async () => {
  let resolveFirst: (page: BrowsePage) => void = () => {};
  browsePageMock
    .mockReturnValueOnce(new Promise<BrowsePage>((resolve) => (resolveFirst = resolve)))
    .mockResolvedValueOnce(
      makePage({ columns: [{ name: 'sku', data_type: 'TEXT' }], rows: [[v('text', 'A-1')]] }),
    );

  const { rerender } = mount();
  rerender(<ResultGrid sessionId="s1" database="main" table="orders" />);

  await waitFor(() => expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1'));

  // The users page lands late. It must not append itself to the orders grid.
  await act(async () => {
    resolveFirst(makePage({ columns: [ID_COLUMN], rows: [[v('int', '1041')]] }));
  });

  expect(screen.getByTestId('header-sku').textContent).toBe('sku');
  expect(screen.queryByTestId('header-id')).toBeNull();
  expect(harness.props!.rows).toBe(1);
});

it('survives a page whose rows and columns arrive as null', async () => {
  // The engine guarantees `[]` for rows today — and it did not the day the
  // whole window went blank. Sidebar.tsx keeps the same guard for the same
  // reason: the contract has been proven not to enforce itself.
  browsePageMock.mockResolvedValue({
    columns: null,
    rows: null,
    exhausted: true,
    offset: 0,
  } as unknown as BrowsePage);

  mount();

  expect(await screen.findByText('No rows')).toBeDefined();
  expect(harness.props!.rows).toBe(0);
  expect(harness.props!.columns).toEqual([]);
});

it('renders a skeleton rather than crashing for a cell the grid asks for out of range', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('int', '1')]] }));

  mount();
  await settled();

  // Glide can ask for a cell that a shrinking row set no longer holds, and a
  // ragged row from a misbehaving driver is the same shape of problem.
  expect(harness.props!.getCellContent([0, 9999]).kind).toBe('loading');
  expect(harness.props!.getCellContent([9, 0]).kind).toBe('loading');
});

it('takes its row and header heights from the density tokens', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('int', '1')]] }));

  mount();
  await settled();

  expect(harness.props!.rowHeight).toBe(26);
  expect(harness.props!.headerHeight).toBe(28);
  expect(harness.props!.rowHeight).toBe(Number.parseFloat(token('--row-data')));
  expect(harness.props!.headerHeight).toBe(Number.parseFloat(token('--row-header')));
});

it('builds its canvas theme out of the tokens rather than literals', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('int', '1')]] }));

  mount();
  await settled();

  const theme = harness.props!.theme;
  expect(theme.textDark).toBe(token('--color-text'));
  expect(theme.textMedium).toBe(token('--color-dim'));
  expect(theme.bgCell).toBe(token('--color-surface'));
  expect(theme.bgHeader).toBe(token('--color-alt'));
  expect(theme.accentColor).toBe(token('--color-accent'));
  // Canvas cannot read a CSS variable, so the family has to be the resolved
  // stack. Mono is normative here: column alignment depends on it.
  expect(theme.fontFamily).toBe(token('--font-mono'));
  expect(theme.baseFontStyle).toBe(token('--text-body'));
});

// A canvas cannot inherit a media query, so the tokens have to be re-read.
// Without this the grid stays painted in light colours on a dark chrome.
it('re-reads the tokens when the system colour scheme changes', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('int', '1')]] }));

  const { unmount } = mount();
  await settled();
  expect(media.listeners.length).toBe(1);

  document.documentElement.style.setProperty('--color-dim', '#010203');
  act(() => {
    for (const listener of media.listeners) listener();
  });

  expect(harness.props!.theme.textMedium).toBe('#010203');
  expect(screen.getByTestId('cell-0-0').getAttribute('data-color')).toBe('');

  // And it lets go again, rather than leaving a listener holding a dead
  // component every time the user opens another table.
  unmount();
  expect(media.removed).toBe(1);
});

// Chrome is unselectable; a data cell is NOT chrome. Copying a value out of
// the grid is the entire point of it.
it('leaves cell text selectable while its own chrome stays unselectable', async () => {
  browsePageMock
    .mockResolvedValueOnce(fullPage())
    .mockRejectedValueOnce({ code: -32001, message: 'the engine stopped responding' });

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  expect(getComputedStyle(screen.getByTestId('cell-0-0')).userSelect).toBe('text');

  scrollTo(BROWSE_PAGE_SIZE - 10);
  await screen.findByRole('alert');

  expect(getComputedStyle(screen.getByRole('button', { name: 'Retry' })).userSelect).toBe('none');
});

it('copies a cell as the text it displays, NULL included', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [ID_COLUMN, { name: 'note', data_type: 'TEXT' }],
      rows: [[v('null'), v('text', 'Ama Osei')]],
    }),
  );

  mount();
  await settled();

  expect(harness.props!.getCellsForSelection).toBe(true);
  expect(screen.getByTestId('cell-1-0').getAttribute('data-copy')).toBe('Ama Osei');
  expect(screen.getByTestId('cell-0-0').getAttribute('data-copy')).toBe('NULL');
});

// Glide feeds `data` — not `displayData` — to the <td role="gridcell"> tree
// it renders for screen readers. A NULL with an empty `data` drew as NULL on
// the canvas and announced as an empty cell, which is the same defect one
// layer down.
it('announces a NULL as NULL to the accessibility tree, not as an empty cell', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [ID_COLUMN, { name: 'note', data_type: 'TEXT' }],
      rows: [[v('null'), v('text', '')]],
    }),
  );

  mount();
  await settled();

  expect(screen.getByTestId('cell-0-0').getAttribute('data-value')).toBe('NULL');
  expect(screen.getByTestId('cell-1-0').getAttribute('data-value')).toBe('');
});

it('lets a column be widened without changing the others', async () => {
  browsePageMock.mockResolvedValue(
    makePage({
      columns: [ID_COLUMN, { name: 'email', data_type: 'TEXT' }],
      rows: [[v('int', '1'), v('text', 'a@example.com')]],
    }),
  );

  mount();
  await settled();

  const base = Number.parseFloat(token('--width-grid-column'));
  act(() => {
    harness.props!.onColumnResize({ id: 'email' }, 420);
  });

  expect(screen.getByTestId('header-email').getAttribute('data-width')).toBe('420');
  expect(screen.getByTestId('header-id').getAttribute('data-width')).toBe(String(base));
});

it('gives the overlay editor a portal of its own rather than needing one in the document body', async () => {
  browsePageMock.mockResolvedValue(makePage({ rows: [[v('int', '1')]] }));

  const { container } = mount();
  await settled();

  const portal = container.querySelector('.result-grid-portal') as HTMLElement;
  expect(portal).not.toBeNull();
  expect(harness.props!.portalElementRef.current).toBe(portal);
  // The overlay is where a long value is read in full and selected, so it is
  // on the selectable side of the policy.
  expect(getComputedStyle(portal).userSelect).toBe('text');
});

it('asks for nothing when the viewport moves before the first page has landed', async () => {
  browsePageMock.mockReturnValue(new Promise(() => {}));

  mount();
  await settled();

  scrollTo(0, 40);

  expect(browsePageMock).toHaveBeenCalledTimes(1);
});

it('keeps the rows it has when a continuation page arrives with null rows', async () => {
  browsePageMock.mockResolvedValueOnce(fullPage()).mockResolvedValueOnce({
    columns: [ID_COLUMN],
    rows: null,
    exhausted: true,
    offset: 0,
  } as unknown as BrowsePage);

  mount();
  await settled();
  await waitFor(() => expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE));

  scrollTo(BROWSE_PAGE_SIZE - 10);

  await waitFor(() => expect(browsePageMock).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull());
  expect(harness.props!.rows).toBe(BROWSE_PAGE_SIZE);
  expect(screen.queryByRole('alert')).toBeNull();
});

it('ignores a rejection that arrives for the table the user has already left', async () => {
  let rejectFirst: (err: unknown) => void = () => {};
  browsePageMock
    .mockReturnValueOnce(new Promise<BrowsePage>((_resolve, reject) => (rejectFirst = reject)))
    .mockResolvedValueOnce(
      makePage({ columns: [{ name: 'sku', data_type: 'TEXT' }], rows: [[v('text', 'A-1')]] }),
    );

  const { rerender } = mount();
  rerender(<ResultGrid sessionId="s1" database="main" table="orders" />);
  await waitFor(() => expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1'));

  // The users request fails late. Reporting it now would put an error under
  // a grid that loaded perfectly well.
  await act(async () => {
    rejectFirst({ code: -32001, message: 'the engine died' });
  });

  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.getByTestId('cell-0-0').textContent).toBe('A-1');
});
