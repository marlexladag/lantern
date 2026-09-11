# Lantern

A cross-platform desktop database client: a **Tauri v2 +
React** shell around a **Go engine sidecar**, which the shell drives over
newline-delimited **JSON-RPC 2.0 on stdio**.

The design lives in
[`docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md`](docs/superpowers/specs/2026-09-08-tableplus-like-client-design.md).

## Current state: walking skeleton

There is **no database code yet, by design**. The engine exposes exactly one
method, `health`, and the shell renders its answer. What this skeleton proves
is the plumbing that everything else will sit on:

- the sidecar builds for all six target triples and is packaged into the app
  bundle,
- the shell spawns it, correlates JSON-RPC requests by ID, and supervises it
  (restart with exponential backoff, then a terminal `down` state),
- stdout carries protocol traffic and nothing else — all diagnostics go to
  stderr,
- CI builds installers for macOS, Windows and Linux.

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/engine` | Sidecar entry point: stdio JSON-RPC server, exits on stdin EOF |
| `internal/rpc` | JSON-RPC 2.0 wire types, newline framing, method dispatch |
| `internal/health` | The `health` method |
| `internal/api` | The `connections.*` and `session.*` JSON-RPC methods — the only package that knows about both the engine and the transport |
| `internal/engine/driver` | The `Driver`/`Conn` abstraction every engine implements, plus the driver registry (`internal/engine/driver/sqlite` is the SQLite implementation) |
| `internal/engine/schema` | The engine-neutral catalog (databases, tables, columns) every driver maps onto |
| `internal/engine/store` | Connection persistence: settings to a JSON file, passwords to the OS keychain, never mixed |
| `internal/engine/dberr` | The normalized driver-error type every driver maps its native failures onto |
| `src-tauri/src/engine.rs` | Sidecar lifecycle, ID correlation, respawn backoff |
| `src/lib/engine.ts` | Typed client for the shell's `engine_request` command |
| `scripts/` | Sidecar cross-compilation and its tests, plus the CI validator |
| `scripts/harness/` | The browser harness — the real bundle under the real CSP against a real sidecar, driven by headless Chrome (see below) |

## Prerequisites

- **Go** 1.24+ (the version in `go.mod`)
- **Node** 20+
- **Rust** stable, plus the
  [Tauri v2 system dependencies](https://tauri.app/start/prerequisites/) for
  your platform (on Linux: `libwebkit2gtk-4.1-dev` and friends)
- **Python 3** with PyYAML, only to run `scripts/ci-workflow_test.sh`

## Build

```sh
npm install
./scripts/build-sidecars.sh   # REQUIRED before any Tauri build
npm run tauri dev             # or: npm run tauri build
```

**`./scripts/build-sidecars.sh` is not optional, and it is not run for you.**
Tauri resolves the `externalBin` entry `binaries/engine` by appending the host's
Rust target triple, and `src-tauri/binaries/` is gitignored — so on a fresh
clone that directory does not exist and the Tauri build fails to resolve the
sidecar. Run the script first; re-run it whenever you change anything under
`cmd/` or `internal/`, because neither `tauri dev` nor `tauri build` rebuilds
the Go side.

The script cross-compiles six binaries (darwin/linux/windows x amd64/arm64) with
`CGO_ENABLED=0`, so it needs no C toolchain, and stamps each with the version
and commit that the UI displays.

`npm run tauri build` writes installers to `src-tauri/target/release/bundle/`.
On macOS the sidecar is copied into `Lantern.app/Contents/MacOS/engine`
and resolved relative to the executable; in `tauri dev` it is resolved from
`src-tauri/binaries/` in the source tree instead. Those are different code
paths, so a change to sidecar packaging is only proven once you have launched
the **bundled** app, not the dev server.

## Test

```sh
go test ./... -race            # engine
npm test                       # UI (vitest)
npm run typecheck              # UI types (tsc --noEmit)
./scripts/build-sidecars_test.sh   # all six triples build and are correctly named
./scripts/ci-workflow_test.sh      # the GitHub Actions workflow is internally consistent
```

Rust checks mirror what CI runs:

```sh
cd src-tauri && cargo fmt --check && cargo clippy -- -D warnings && cargo test
```

## Browser harness

```sh
node scripts/harness/run.mjs                  # build, serve, drive, report
node scripts/harness/run.mjs --db ~/my.db     # against your own database
node scripts/harness/run.mjs --no-build --keep --out ./shots
```

Serves the production `dist/` under the **exact** `csp` string read out of
`src-tauri/tauri.conf.json`, proxies the UI's `engine_request` calls to a real
Go sidecar over its real JSON-RPC stdio, and drives the page with headless
Chrome: clicking, typing, screenshotting, reading pixels back off the grid's
canvas and names out of the accessibility tree. With no `--db` it builds a
fixture with the shapes that have broken something before — NULLs in a text
column, a table with zero rows, a view, and enough rows to scroll.

**What it is for.** Nothing in `npm test` paints or lays out. jsdom performs
no layout, so a grid that renders with zero height, a canvas that never draws,
and a cell that says `NULL` on screen while reading empty to a screen reader
all pass a green suite. Each of those was a real defect here, and each was
found this way.

**What it cannot tell you.** The webview here is Chromium and the one that
ships is WKWebView, so a clean run is evidence, not proof. Two checks in
particular cannot be made from here and have to be made by hand in the
packaged app:

1. **CSP reports in the shipping webview.** Tauri attaches the policy in its
   `tauri://localhost` asset handler, where `'self'` resolves to a custom
   scheme origin rather than this harness's `http://127.0.0.1` one. Build the
   app, open the Web Inspector, and watch the console while using it.
2. **Cmd-C on a grid cell.** The clipboard needs a secure context and a real
   user gesture; a synthesised key event in a loopback HTTP page has neither.

It is **not part of any gate**: it needs a browser that may not be installed,
so `npm test` and CI never call it. A missing browser exits 2 with a message
saying so (set `LANTERN_HARNESS_CHROME` to point at your own); a failed check
exits 1.

## Running the engine directly

The engine is a normal binary that speaks newline-delimited JSON-RPC on
stdin/stdout, so it is drivable by hand without Tauri at all — useful while
poking at a new RPC method:

```sh
go run ./cmd/engine
{"jsonrpc":"2.0","id":1,"method":"health"}
```
(paste a request line, press enter, read the response line; Ctrl-D closes
stdin, which is the engine's normal shutdown signal)

**Set `LANTERN_CONFIG_DIR` before experimenting with anything that touches
saved connections** (`connections.save`, `session.open`, …). Without it,
`connections.list`/`save`/`delete` resolve against your real
`connections.json` via `os.UserConfigDir()` — and on macOS that function
ignores `XDG_CONFIG_HOME` entirely, so setting *that* instead does nothing
to redirect it. `LANTERN_CONFIG_DIR` overrides it directly, on every
platform:

```sh
LANTERN_CONFIG_DIR=/tmp/lantern-scratch go run ./cmd/engine
```

This writes `/tmp/lantern-scratch/lantern/connections.json` instead of your
real one — same `lantern/connections.json` suffix underneath either way, so
the layout you see matches production.

## Content Security Policy

`src-tauri/tauri.conf.json` sets a strict `csp`. It is decided now, while the
UI is one paragraph, rather than after CodeMirror 6 and Glide Data Grid land
and the question becomes a negotiation under a deadline. Per directive:

| Directive | Why |
| --- | --- |
| `default-src 'self'` | Fallback floor. Everything below either narrows it or names an exception. |
| `script-src 'self'` | **The line that matters.** Only bundle code runs — no `'unsafe-inline'`, no `'unsafe-eval'`, no CDN. Vite's production output is a single same-origin module, and Tauri injects its own init scripts through the webview API, which CSP does not police. |
| `style-src 'self' 'unsafe-inline'` | Pre-authorized deliberately. CodeMirror 6 (via style-mod) injects a `<style>` element at runtime, and runtime-injected styling is normal for the grid too. It does not widen script execution, and the exfiltration channel CSS injection would use is closed by the `img-src`/`font-src`/`connect-src` limits. **Caveat:** if a hash or nonce ever lands in `style-src`, CSP ignores `'unsafe-inline'` and those libraries break. Tauri adds a style hash automatically when `index.html` contains an inline `<style>`; it has none today, so don't add one. |
| `img-src 'self' data: blob:` | Vite inlines small assets as `data:` URIs; `blob:` covers canvas-derived images from the grid. Neither can execute. |
| `font-src 'self' data:` | Same reason as images: bundled or inlined by the build, never fetched. |
| `connect-src 'self' ipc: http://ipc.localhost https://ipc.localhost` | The IPC transport itself. `@tauri-apps/api` sends every `invoke` as a `fetch` to `ipc://localhost/<cmd>` on macOS and Linux, and `http(s)://ipc.localhost/<cmd>` on Windows. Omit these and Tauri silently falls back to `postMessage` — it still works, which is exactly what makes the omission easy to miss. |
| `object-src 'none'`, `frame-src 'none'`, `frame-ancestors 'none'`, `worker-src 'self'`, `media-src 'none'` | Plugins, frames, framing, foreign workers and media have no role in this app. Denied explicitly rather than left to `default-src`. |
| `base-uri 'self'` | Stops an injected `<base>` from re-pointing every relative URL. |
| `form-action 'none'` | Nothing here submits a form; a form post is a credential-exfiltration path. |

Note where this applies: Tauri attaches the CSP header in the `tauri://localhost`
asset handler, so it governs the **bundled** app. In `tauri dev` the page is
served by Vite over `http://localhost:1420` and no CSP is attached at all — so
a policy change is only truly exercised by a bundled build. Test it there.

## Invariants

Two rules that the tests actively guard — breaking either breaks the protocol:

1. **stdout is the JSON-RPC stream and nothing else.** A single stray
   `fmt.Println` in the engine corrupts it for the shell. Diagnostics go to
   stderr, which the shell captures into its own log.
2. **Go dependencies are pure Go, not standard-library-only.** The engine has
   two — `modernc.org/sqlite` and `github.com/zalando/go-keyring` — and both
   are CGO-free by requirement, not accident: `CGO_ENABLED=0` cross-compiles
   the sidecar for all six targets (`scripts/build-sidecars.sh`), and a
   cgo-based dependency would break that. Do not add one.
