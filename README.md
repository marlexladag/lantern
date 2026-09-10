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
| `src-tauri/src/engine.rs` | Sidecar lifecycle, ID correlation, respawn backoff |
| `src/lib/engine.ts` | Typed client for the shell's `engine_request` command |
| `scripts/` | Sidecar cross-compilation and its tests, plus the CI validator |

## Prerequisites

- **Go** 1.24+ (the version in `go.mod`; the engine is standard library only)
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
2. **The engine is standard library only.** No third-party Go dependencies.
