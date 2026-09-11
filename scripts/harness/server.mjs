/*
 * The page under the real policy, talking to a real engine.
 *
 * Serves the production `dist/` over HTTP with the EXACT `csp` string read
 * out of src-tauri/tauri.conf.json — read, never transcribed, because a
 * transcribed copy drifts and a harness that tests yesterday's policy is
 * worse than no harness at all. It also proxies the UI's `engine_request`
 * calls to a real Go sidecar over its real newline-delimited JSON-RPC stdio,
 * so what answers the UI is the engine, not a fixture of what the engine is
 * believed to say.
 *
 * See ./run.mjs for what this arrangement can and cannot prove.
 */
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { createInterface } from 'node:readline';
import { extname, join, normalize, resolve } from 'node:path';

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
};

/**
 * Runs the engine binary the way the Tauri shell runs it: one long-lived
 * child, newline-delimited JSON-RPC on stdin/stdout, replies matched to
 * requests by id. stderr is collected rather than discarded — the engine's
 * diagnostics are the first place to look when a call comes back wrong, and
 * a harness that swallowed them would hand you a bare error code instead.
 */
export function startSidecar(enginePath, configDir) {
  const child = spawn(enginePath, [], {
    env: { ...process.env, LANTERN_CONFIG_DIR: configDir },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const pending = new Map();
  const stderr = [];
  let nextId = 1;

  createInterface({ input: child.stdout }).on('line', (line) => {
    if (!line.trim()) return;
    let frame;
    try {
      frame = JSON.parse(line);
    } catch {
      // stdout is the protocol stream and nothing else (README, Invariants).
      // A line that is not a frame is a bug worth surfacing, not skipping.
      stderr.push(`NON-JSON ON STDOUT: ${line}`);
      return;
    }
    const waiter = pending.get(frame.id);
    if (!waiter) return;
    pending.delete(frame.id);
    waiter(frame);
  });
  createInterface({ input: child.stderr }).on('line', (line) => stderr.push(line));

  return {
    stderr,
    call(method, params) {
      const id = nextId++;
      return new Promise((done) => {
        pending.set(id, done);
        child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`);
      });
    },
    stop() {
      child.stdin.end(); // the engine's normal shutdown signal
      child.kill();
    },
  };
}

/** The production CSP, read from the config the bundler reads. */
export async function readCsp(repoRoot) {
  const conf = JSON.parse(await readFile(join(repoRoot, 'src-tauri/tauri.conf.json'), 'utf8'));
  return conf.app.security.csp;
}

export async function startServer({ repoRoot, dist, sidecar, csp }) {
  const shim = await readFile(join(repoRoot, 'scripts/harness/shim.js'), 'utf8');
  const indexHtml = await readFile(join(dist, 'index.html'), 'utf8');
  // First element of <head>: see shim.js for why that position is load-bearing.
  const page = indexHtml.replace('<head>', '<head>\n    <script src="/__harness/shim.js"></script>');

  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    const send = (status, type, body) => {
      res.writeHead(status, { 'content-type': type, 'content-security-policy': csp });
      res.end(body);
    };

    if (req.method === 'POST' && url.pathname === '/__harness/rpc') {
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const { method, params } = JSON.parse(Buffer.concat(chunks).toString());
      const frame = await sidecar.call(method, params);
      send(200, TYPES['.json'], JSON.stringify({ result: frame.result, error: frame.error }));
      return;
    }
    if (url.pathname === '/__harness/shim.js') {
      send(200, TYPES['.js'], shim);
      return;
    }
    if (url.pathname === '/' || url.pathname === '/index.html') {
      send(200, TYPES['.html'], page);
      return;
    }
    // Everything else comes out of dist/ verbatim. `normalize` + the prefix
    // check keeps a crafted path from reaching outside it; this server is
    // loopback-only and short-lived, but a path traversal in a tool that
    // runs on a developer's machine is still a path traversal.
    const file = resolve(join(dist, normalize(url.pathname)));
    if (!file.startsWith(resolve(dist))) {
      send(403, TYPES['.html'], 'forbidden');
      return;
    }
    try {
      send(200, TYPES[extname(file)] ?? 'application/octet-stream', await readFile(file));
    } catch {
      send(404, TYPES['.html'], 'not found');
    }
  });

  await new Promise((done) => server.listen(0, '127.0.0.1', done));
  return { server, port: server.address().port, stop: () => server.close() };
}
