#!/usr/bin/env node
/*
 * Lantern's browser harness: the real bundle, the real CSP, a real engine.
 *
 *   node scripts/harness/run.mjs [--db FILE] [--out DIR] [--keep] [--no-build]
 *
 * It builds `dist/`, builds and runs the Go sidecar, serves the bundle under
 * the exact policy from src-tauri/tauri.conf.json, and drives the page with
 * headless Chrome over CDP — clicking, typing, screenshotting, and reading
 * back the canvas and the accessibility tree.
 *
 * WHY IT EXISTS. Nothing in `npm test` paints or lays out. jsdom performs no
 * layout, so a grid that renders with zero height, a canvas that never draws,
 * and a cell that says NULL on screen while reading empty to a screen reader
 * all pass a green suite. Each of those was a real defect here, and each was
 * found this way.
 *
 * WHAT IT IS NOT. It is evidence, not proof. The webview here is Chromium;
 * the one that ships is WKWebView, and they differ in the places this tool is
 * least able to tell. Two checks in particular CANNOT be made from here and
 * have to be made by hand in the packaged app:
 *
 *   1. CSP reports in the shipping webview. Tauri attaches the policy in its
 *      `tauri://localhost` asset handler, where `'self'` resolves to a custom
 *      scheme origin rather than the `http://127.0.0.1` origin below. Build
 *      the app, open the Web Inspector, and watch the console while using it.
 *   2. Cmd-C on a grid cell. The clipboard needs a secure context and a real
 *      user gesture; a CDP-synthesised key event in a loopback HTTP page has
 *      neither, so a pass here would mean nothing.
 *
 * It is also deliberately NOT part of any gate. It needs a browser that may
 * not be installed, so `npm test` and CI never call it. A missing browser
 * exits 2 with a message; a failed check exits 1.
 */
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { startSidecar, startServer, readCsp } from './server.mjs';

const REPO = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

const CHROMES = [
  process.env.LANTERN_HARNESS_CHROME,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean);

function flag(name) {
  return process.argv.includes(`--${name}`);
}
function option(name) {
  const i = process.argv.indexOf(`--${name}`);
  return i === -1 ? undefined : process.argv[i + 1];
}
function die(code, message) {
  console.error(message);
  process.exit(code);
}
function run(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { cwd: REPO, stdio: 'inherit', ...opts });
  if (r.status !== 0) die(2, `\n${cmd} ${args.join(' ')} failed (exit ${r.status}).`);
}

/*
 * A database with the shapes that have historically broken something: NULLs
 * in a text column (the accessibility defect), a table with zero rows (the
 * "renders blank" defect), a view alongside the tables, and enough rows that
 * the grid is scrolling rather than fitting.
 */
const FIXTURE_SQL = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  email TEXT NOT NULL,
  full_name TEXT,
  role TEXT NOT NULL,
  last_seen TEXT
);
INSERT INTO users (id, email, full_name, role, last_seen)
  SELECT 1040 + n,
         'person' || n || '@example.com',
         CASE WHEN n % 7 = 0 THEN NULL ELSE 'Person ' || n END,
         CASE WHEN n % 5 = 0 THEN 'admin' ELSE 'member' END,
         '2024-01-02 09:0' || (n % 10)
  FROM (WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM c WHERE n < 2000)
        SELECT n FROM c);
CREATE TABLE orders (id INTEGER PRIMARY KEY, sku TEXT NOT NULL, qty INTEGER NOT NULL);
INSERT INTO orders (sku, qty)
  SELECT 'SKU-' || n, n % 9 + 1
  FROM (WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM c WHERE n < 40)
        SELECT n FROM c);
CREATE TABLE audit_log (id INTEGER PRIMARY KEY, note TEXT);
CREATE VIEW active_users AS SELECT id, email FROM users WHERE role = 'member';
`;

function buildFixture(path) {
  const sqlite = spawnSync('sqlite3', [path], { input: FIXTURE_SQL, encoding: 'utf8' });
  if (sqlite.error || sqlite.status !== 0) {
    die(
      2,
      'Could not build the default fixture: the `sqlite3` command is not available.\n' +
        'Install it, or point the harness at your own database:\n' +
        '  node scripts/harness/run.mjs --db /path/to/your.db',
    );
  }
}

// --- CDP ------------------------------------------------------------------
// A few hundred bytes of client rather than a dependency: the harness needs
// evaluate, screenshot, key events and the accessibility tree, and pulling a
// browser automation library in would put a second Chromium on disk for it.

function connectCdp(url) {
  const ws = new WebSocket(url);
  const pending = new Map();
  let nextId = 1;
  const open = new Promise((done, fail) => {
    ws.onopen = done;
    ws.onerror = () => fail(new Error(`cannot reach ${url}`));
  });
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    const waiter = pending.get(msg.id);
    if (!waiter) return;
    pending.delete(msg.id);
    if (msg.error) waiter.fail(new Error(msg.error.message));
    else waiter.done(msg.result);
  };
  return {
    open,
    send(method, params = {}, sessionId) {
      const id = nextId++;
      return new Promise((done, fail) => {
        pending.set(id, { done, fail });
        ws.send(JSON.stringify({ id, method, params, ...(sessionId && { sessionId }) }));
      });
    },
    close: () => ws.close(),
  };
}

/** Every tree row paired with its caret-stripped label, evaluated in-page. */
const ROWS =
  `Array.from(document.querySelectorAll('[role="treeitem"]'))` +
  `.map((el) => ({ el, text: el.textContent.replace('\u25b8', '').trim() }))`;

function pageApi(cdp, sessionId) {
  const send = (method, params) => cdp.send(method, params, sessionId);
  const api = {
    send,
    async eval(expression) {
      const r = await send('Runtime.evaluate', {
        expression,
        awaitPromise: true,
        returnByValue: true,
      });
      if (r.exceptionDetails) {
        throw new Error(r.exceptionDetails.exception?.description ?? 'evaluate threw');
      }
      return r.result.value;
    },
    /** Polls in the page rather than sleeping for a guessed duration. */
    async until(expression, what, timeout = 15000) {
      const deadline = Date.now() + timeout;
      for (;;) {
        if (await api.eval(expression)) return;
        if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
        await new Promise((r) => setTimeout(r, 50));
      }
    },
    /**
     * Every tree row's label, caret stripped. The caret is a <span> inside
     * the row, so it lands in textContent and an exact match without this
     * would never find anything.
     */
    rows: () =>
      api.eval(`JSON.stringify(${ROWS}.map((r) => r.text))`).then(JSON.parse),
    /** Waits for a row to exist, then clicks it. */
    async clickRow(text) {
      const found = `${ROWS}.find((r) => r.text === ${JSON.stringify(text)})`;
      await api.until(`!!${found}`, `the "${text}" row`);
      await api.eval(`${found}.el.click()`);
    },
    /**
     * Waits for a control to exist, then clicks it. Matched on a prefix
     * because a button's label is not always its whole text: Connect carries
     * a ⌘⏎ hint span inside it.
     */
    async clickLabel(text) {
      const found = `Array.from(document.querySelectorAll('button')).find((b) => b.textContent.trim().startsWith(${JSON.stringify(text)}))`;
      await api.until(`!!${found}`, `the "${text}" button`);
      await api.eval(`${found}.click()`);
    },
    async key(key, code) {
      for (const type of ['rawKeyDown', 'keyUp']) {
        await send('Input.dispatchKeyEvent', { type, key, windowsVirtualKeyCode: code });
      }
    },
    async shot(dir, name) {
      const { data } = await send('Page.captureScreenshot', { format: 'png' });
      writeFileSync(join(dir, `${name}.png`), Buffer.from(data, 'base64'));
    },
  };
  return api;
}

// --- the drive ------------------------------------------------------------

async function drive(page, { url, dbPath, out }) {
  const checks = [];
  const record = (name, ok, detail) => {
    checks.push({ name, ok, detail });
    console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}  ${detail}`);
  };
  const rpc = () => page.eval('JSON.stringify(window.__lanternHarness.rpc)').then(JSON.parse);
  const tableCalls = async () => (await rpc()).filter((c) => c.method === 'session.tables');

  await page.send('Page.navigate', { url });
  await page.until('!!document.querySelector(".sidebar")', 'the app to mount');
  await page.shot(out, '01-launched');

  // React owns these inputs, so a value goes in through the native setter
  // with an input event behind it; setting `.value` alone updates the DOM and
  // leaves React's state untouched.
  const fill = (id, value) => page.eval(`
    (() => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      const el = document.querySelector(${JSON.stringify(id)});
      set.call(el, ${JSON.stringify(value)});
      el.dispatchEvent(new Event('input', { bubbles: true }));
    })()
  `);

  /*
   * The connection dialog. Its driver picker and its fields are drivers.list's
   * answer now, not a list in ConnectionDialog.tsx — so both are checked
   * against what the real sidecar actually replies, fetched here over the same
   * bridge the app itself uses. jsdom can only check this against a mock; this
   * is the half that proves the two ends agree.
   */
  await page.clickLabel('Add connection');
  await page.until('!!document.querySelector(".segmented button")', 'the driver picker');
  const engineDrivers = await page.eval(`
    fetch('/__harness/rpc', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ method: 'drivers.list', params: null }),
    }).then((r) => r.json()).then((f) => JSON.stringify(f.result))
  `).then(JSON.parse);
  const picker = await page
    .eval(`JSON.stringify(Array.from(document.querySelectorAll('.segmented button')).map((b) => b.textContent.trim()))`)
    .then(JSON.parse);
  const fieldIds = await page
    .eval(`JSON.stringify(Array.from(document.querySelectorAll('.dialog-body input[type=text]')).map((i) => i.id))`)
    .then(JSON.parse);
  await page.shot(out, '02-dialog');
  record(
    'the driver picker is the engine\'s own list',
    picker.length === engineDrivers.length &&
      engineDrivers.every((d) => picker.some((label) => label.toLowerCase() === d.id)),
    `engine ${JSON.stringify(engineDrivers.map((d) => d.id))}, picker ${JSON.stringify(picker)}`,
  );
  record(
    'the form has an input for every field the engine requires',
    engineDrivers[0].required_fields.length > 0 &&
      engineDrivers[0].required_fields.every((f) => fieldIds.includes(`conn-${f}`)),
    `required ${JSON.stringify(engineDrivers[0].required_fields)}, inputs ${JSON.stringify(fieldIds)}`,
  );

  // Saving with a required field empty. The refusal has to name the field and
  // has to happen HERE, before a record that can never dial is persisted —
  // the defect a user reported against this dialog, now enforced by what the
  // engine said rather than by a copy of it in the shell.
  await fill('#conn-name', 'harness');
  await page.clickLabel('Connect');
  await page.until(`!!document.querySelector('[role="alert"]')`, 'the refusal');
  const refusal = await page.eval(`document.querySelector('[role="alert"]').textContent`);
  const saves = async () => (await rpc()).filter((c) => c.method === 'connections.save');
  await page.shot(out, '03-refusal');
  record(
    'a missing required field is refused by name, with nothing saved',
    /file is required/i.test(refusal) && (await saves()).length === 0,
    `alert said ${JSON.stringify(refusal)}; connections.save calls: ${(await saves()).length}`,
  );

  await fill('#conn-file', dbPath);
  await page.clickLabel('Connect');
  await page.until(`${ROWS}.some((r) => r.text.startsWith('harness'))`, 'the saved connection');

  record(
    'tables are not read on connect',
    (await tableCalls()).length === 0,
    `session.tables calls before expanding: ${(await tableCalls()).length}; ` +
      `methods so far: ${(await rpc()).map((c) => c.method).join(', ')}`,
  );

  // Expanding the connection. SQLite reports multiple_databases: false, so
  // the tables hang straight off it and no database row is drawn.
  await page.eval(`${ROWS}.find((r) => r.text.startsWith('harness')).el.click()`);
  await page.until(`${ROWS}.some((r) => r.text === 'users')`, 'the tables');
  await page.shot(out, '04-tables');

  const rows = await page.rows();
  const calls = await tableCalls();
  record(
    'no database tier for a single-database driver',
    !rows.includes('main') && rows[0].startsWith('harness'),
    `tree rows: ${JSON.stringify(rows)}`,
  );
  record(
    'the tables were read for the database the catalog named',
    calls.length === 1 && calls[0].params.database === 'main',
    `session.tables x${calls.length} ${JSON.stringify(calls.map((c) => c.params.database))}`,
  );

  // A table's rows, and proof the canvas was actually painted: a blank canvas
  // reads back exactly one distinct colour.
  await page.clickRow('users');
  await page.until('!!document.querySelector("canvas")', 'the grid canvas');
  await new Promise((r) => setTimeout(r, 500)); // one paint, not a poll on pixels
  await page.shot(out, '05-rows');
  const canvas = await page.eval(`
    (() => {
      const c = Array.from(document.querySelectorAll('canvas'))
        .sort((a, b) => b.width * b.height - a.width * a.height)[0];
      const px = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
      const seen = new Set();
      for (let i = 0; i < px.length; i += 4) seen.add((px[i] << 16) | (px[i + 1] << 8) | px[i + 2]);
      return { width: c.width, height: c.height, colours: seen.size };
    })()
  `);
  record(
    'the grid painted',
    canvas.colours > 1 && canvas.height > 100,
    `canvas ${canvas.width}x${canvas.height}, ${canvas.colours} distinct colours`,
  );

  // The defect this check exists for: the canvas said NULL and the
  // accessibility tree said nothing. ResultGrid sets `data` as well as
  // `displayData` for exactly this, and this is what reads it back.
  await page.send('Accessibility.enable');
  const ax = await page.send('Accessibility.getFullAXTree');
  const names = ax.nodes.map((n) => n.name?.value).filter(Boolean);
  record(
    'a NULL cell reads NULL to a screen reader',
    names.includes('NULL'),
    `${names.filter((n) => n === 'NULL').length} NULL nodes in the AX tree of ${names.length} named nodes`,
  );

  // A table with no rows says so rather than drawing nothing.
  await page.clickRow('audit_log');
  await page.until(
    `!!Array.from(document.querySelectorAll('[role="status"]')).find((e) => e.textContent === 'No rows')`,
    '"No rows"',
  );
  await page.shot(out, '06-empty-table');
  record('an empty table says No rows', true, 'audit_log');

  // Collapse and re-expand the connection: the table list is cached, so this
  // must not go back to the engine.
  const before = (await tableCalls()).length;
  await page.eval(`${ROWS}.find((r) => r.text.startsWith('harness')).el.click()`);
  await page.until(`!${ROWS}.some((r) => r.text === 'users')`, 'the tables to collapse');
  await page.eval(`${ROWS}.find((r) => r.text.startsWith('harness')).el.click()`);
  await page.until(`${ROWS}.some((r) => r.text === 'users')`, 'the tables again');
  record(
    'collapsing and re-expanding does not refetch',
    (await tableCalls()).length === before,
    `session.tables x${(await tableCalls()).length} (was ${before})`,
  );

  // Spec §12 is keyboard-first. From a fully collapsed tree, Enter opens the
  // connection and one ArrowDown reaches a table — no database row in the way.
  await page.eval(`
    (() => {
      const row = ${ROWS}.find((r) => r.text.startsWith('harness')).el;
      row.click(); // collapse
      row.focus();
    })()
  `);
  await page.key('Enter', 13);
  await page.until(`${ROWS}.some((r) => r.text === 'users')`, 'the tables from the keyboard');
  await page.key('ArrowDown', 40);
  await page.key('Enter', 13);
  await page.until(`!!document.querySelector('[aria-selected="true"]')`, 'a selected table');
  await page.shot(out, '07-keyboard');
  const selected = await page.eval(
    `${ROWS}.filter((r) => r.el.getAttribute('aria-selected') === 'true').map((r) => r.text).join()`,
  );
  const focused = await page.eval('document.activeElement.textContent.trim()');
  record('the keyboard reaches a table', !!selected, `selected "${selected}", focus on "${focused}"`);

  const harness = await page.eval(
    'JSON.stringify({ csp: window.__lanternHarness.csp, errors: window.__lanternHarness.errors, rejections: window.__lanternHarness.rejections })',
  ).then(JSON.parse);
  record(
    'no CSP violation, page error or unhandled rejection',
    harness.csp.length === 0 && harness.errors.length === 0 && harness.rejections.length === 0,
    `csp ${harness.csp.length}, errors ${harness.errors.length}, rejections ${harness.rejections.length}`,
  );

  return { checks, harness, rpc: await rpc(), canvas };
}

// --- wiring ---------------------------------------------------------------

async function main() {
  const chrome = CHROMES.find((p) => existsSync(p));
  if (!chrome) {
    die(
      2,
      'No Chrome or Chromium found, so the harness has nothing to drive.\n' +
        'Install one, or point the harness at yours:\n' +
        '  LANTERN_HARNESS_CHROME=/path/to/chrome node scripts/harness/run.mjs\n' +
        `Looked in:\n  ${CHROMES.join('\n  ')}`,
    );
  }

  const work = mkdtempSync(join(tmpdir(), 'lantern-harness-'));
  const out = resolve(option('out') ?? join(work, 'shots'));
  mkdirSync(out, { recursive: true });
  const configDir = join(work, 'config');
  mkdirSync(configDir);

  if (!flag('no-build')) run('npm', ['run', 'build']);
  if (!existsSync(join(REPO, 'dist/index.html'))) {
    die(2, 'dist/index.html is missing. Drop --no-build, or run `npm run build` first.');
  }
  const enginePath = join(work, 'engine');
  run('go', ['build', '-o', enginePath, './cmd/engine']);

  const dbPath = option('db') ? resolve(option('db')) : join(work, 'harness.db');
  if (!option('db')) buildFixture(dbPath);
  if (!existsSync(dbPath)) die(2, `No such database: ${dbPath}`);

  const sidecar = startSidecar(enginePath, configDir);
  const csp = await readCsp(REPO);
  const server = await startServer({ repoRoot: REPO, dist: join(REPO, 'dist'), sidecar, csp });
  const url = `http://127.0.0.1:${server.port}/`;

  const browser = spawn(chrome, [
    '--headless=new',
    '--remote-debugging-port=0',
    `--user-data-dir=${join(work, 'chrome')}`,
    '--no-first-run',
    '--disable-gpu',
    '--window-size=1440,900',
    'about:blank',
  ]);
  const wsUrl = await new Promise((done, fail) => {
    const timer = setTimeout(() => fail(new Error('Chrome never announced a debugging port')), 20000);
    browser.stderr.on('data', (chunk) => {
      const m = /DevTools listening on (ws:\/\/\S+)/.exec(String(chunk));
      if (m) {
        clearTimeout(timer);
        done(m[1]);
      }
    });
  });

  const cdp = connectCdp(wsUrl);
  await cdp.open;
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  const page = pageApi(cdp, sessionId);
  await page.send('Page.enable');
  await page.send('Runtime.enable');

  let result;
  let failure;
  try {
    console.log(`\nserving ${url}  (CSP straight from tauri.conf.json)\n`);
    result = await drive(page, { url, dbPath, out });
  } catch (err) {
    failure = err;
    try {
      await page.shot(out, '99-failure');
    } catch {
      /* the page may be gone; the thrown error is the news, not this */
    }
  }

  writeFileSync(
    join(out, 'result.json'),
    JSON.stringify({ url, dbPath, csp, result, failure: failure?.message, engineStderr: sidecar.stderr }, null, 2),
  );

  cdp.close();
  browser.kill();
  sidecar.stop();
  server.stop();

  console.log(`\nscreenshots and result.json: ${out}`);
  if (!flag('keep')) rmSync(join(work, 'chrome'), { recursive: true, force: true });

  if (failure) {
    console.error(`\nharness failed: ${failure.stack ?? failure.message}`);
    process.exit(1);
  }
  const failed = result.checks.filter((c) => !c.ok);
  console.log(`\n${result.checks.length - failed.length}/${result.checks.length} checks passed`);
  process.exit(failed.length === 0 ? 0 : 1);
}

main();
