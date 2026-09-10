//! Sidecar lifecycle and JSON-RPC correlation.
//!
//! The Go engine speaks newline-delimited JSON-RPC on stdio. This module owns
//! the child process, matches responses to requests by ID, and restarts the
//! engine if it dies.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde_json::Value;
use tauri::{AppHandle, Emitter};
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;
use tokio::sync::oneshot;

/// How long a single request may wait before the caller gives up.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);
/// Base delay before the first respawn attempt after a death. Doubles on
/// each consecutive failed start (500ms, 1s, 2s, 4s, 8s, ...), capped at
/// MAX_RESTART_DELAY, so a crash loop backs off instead of spinning.
const RESTART_DELAY: Duration = Duration::from_millis(500);
/// Ceiling on the exponential backoff between respawn attempts.
const MAX_RESTART_DELAY: Duration = Duration::from_secs(8);
/// A child that dies before staying up this long counts as a failed start
/// (bad binary, missing dylib, panic during init) rather than a healthy run
/// that happened to end. Without this distinction the failure counter would
/// reset on every successful `spawn()` call regardless of how quickly the
/// child then died, and a broken binary would respawn at a fixed interval
/// forever instead of backing off. The engine prints "engine: ready" to
/// stderr and is accepting connections within milliseconds of a real start,
/// so 2 seconds is a generous floor that only a start-time failure should
/// undercut.
const MIN_HEALTHY_UPTIME: Duration = Duration::from_secs(2);
/// Consecutive failed starts allowed before giving up and emitting "down"
/// instead of scheduling another attempt. Chosen to be enough to ride out a
/// flaky first launch (e.g. a slow filesystem on first access) without
/// looking hung, while still bounded: at these delays the five retries
/// together take under 16 seconds, well inside what a user will wait before
/// assuming the app is broken and needing an explicit "down" signal instead.
const MAX_RESTART_ATTEMPTS: u32 = 5;

/// Bookkeeping for the respawn backoff, updated together under one lock so
/// "how long was it up" and "how many times in a row has that failed" never
/// drift out of sync with each other.
#[derive(Default)]
struct RestartTracker {
    consecutive_failures: u32,
    spawned_at: Option<Instant>,
}

pub struct Engine {
    app: AppHandle,
    next_id: AtomicU64,
    pending: Mutex<HashMap<u64, oneshot::Sender<Value>>>,
    child: Mutex<Option<CommandChild>>,
    restart: Mutex<RestartTracker>,
    /// Set once, from the app's `RunEvent::Exit` handler. Checked by
    /// `handle_death` and `spawn` so a deliberate shutdown never races a
    /// replacement engine into existence during teardown.
    shutting_down: AtomicBool,
}

impl Engine {
    pub fn new(app: AppHandle) -> Arc<Self> {
        Arc::new(Self {
            app,
            next_id: AtomicU64::new(1),
            pending: Mutex::new(HashMap::new()),
            child: Mutex::new(None),
            restart: Mutex::new(RestartTracker::default()),
            shutting_down: AtomicBool::new(false),
        })
    }

    fn set_state(&self, state: &str) {
        let _ = self.app.emit("engine://status", state);
    }

    /// Rejects every in-flight request. Called when the engine dies, so the UI
    /// sees an error instead of hanging until the timeout.
    fn fail_all_pending(&self, reason: &str) {
        let mut pending = self.pending.lock().unwrap();
        for (_, tx) in pending.drain() {
            let _ = tx.send(serde_json::json!({
                "error": { "code": -32603, "message": reason }
            }));
        }
    }

    /// Marks the engine as intentionally stopping and kills the child
    /// process. Call this from the app's `RunEvent::Exit` handler.
    ///
    /// Dropping the child when the process exits already closes its stdin,
    /// which the engine reads as its own clean-shutdown signal - but
    /// tauri-plugin-shell only auto-kills children registered through its
    /// JS-side spawn command, and a sidecar launched from Rust `setup()`,
    /// as this one is, is never in that store. So without this, nothing
    /// kills the child on exit but stdin EOF, and `handle_death` would
    /// otherwise see the resulting `Terminated` event as an ordinary crash
    /// and spawn a replacement while the app is tearing down. This is
    /// meant as the belt to stdin EOF's braces today; it stops being
    /// optional once long-running query handlers land and the engine can
    /// no longer be relied on to exit promptly just because stdin closed.
    pub fn shutdown(&self) {
        self.shutting_down.store(true, Ordering::SeqCst);
        if let Some(child) = self.child.lock().unwrap().take() {
            if let Err(e) = child.kill() {
                eprintln!("engine: failed to kill child during shutdown: {e}");
            }
        }
    }

    /// Launches the sidecar and starts the reader task. Safe to call again
    /// after a crash.
    pub fn spawn(self: &Arc<Self>) -> Result<(), String> {
        if self.shutting_down.load(Ordering::SeqCst) {
            // Closes a narrow race: a backoff sleep from an earlier crash
            // can still be pending when RunEvent::Exit fires. Without this
            // check that sleep would wake up after shutdown() already ran
            // and spawn a brand new engine during teardown anyway.
            return Err("engine is shutting down".to_string());
        }

        let command = self
            .app
            .shell()
            .sidecar("engine")
            .map_err(|e| format!("cannot resolve sidecar: {e}"))?;

        let (mut rx, child) = command
            .spawn()
            .map_err(|e| format!("cannot spawn sidecar: {e}"))?;

        *self.child.lock().unwrap() = Some(child);
        self.restart.lock().unwrap().spawned_at = Some(Instant::now());
        // NOTE for whoever wires up connection/status UI later: on this
        // *first* call - from setup(), before the webview's JS bundle has
        // run far enough to attach an `engine://status` listener - this
        // "ready" emission is unobservable. Tauri does not buffer or replay
        // events, so a listener registered even a moment after this line
        // runs simply never sees it. That's fine by design: this event
        // reflects process-lifecycle state ("the child is spawned"), not
        // protocol readiness, which the UI establishes for itself via the
        // `health` handshake once it *is* listening. Later "ready"
        // emissions (after a crash + respawn) are observable, since by
        // then the webview has long since attached its listener.
        self.set_state("ready");

        let this = Arc::clone(self);
        tauri::async_runtime::spawn(async move {
            while let Some(event) = rx.recv().await {
                match event {
                    // Verified against tauri-plugin-shell 2.3.6's source
                    // (src/process/mod.rs): Command::spawn() defaults
                    // raw_out to false, and the sidecar path never calls
                    // set_raw_out(true). With raw_out false, each pipe is
                    // read on a dedicated thread via read_line(), which
                    // loops on BufReader::fill_buf()/consume() internally
                    // (tauri-utils' io::read_line) until it hits a '\n' or
                    // '\r' byte, buffering short reads across multiple
                    // syscalls before ever sending a CommandEvent. So this
                    // plugin version delivers one Stdout/Stderr event per
                    // complete line, never a partial chunk - no manual
                    // buffering is needed here. (The returned bytes still
                    // include the trailing terminator; serde_json tolerates
                    // trailing whitespace after a JSON value, so that is
                    // harmless for handle_line below.)
                    CommandEvent::Stdout(line) => this.handle_line(&line),
                    CommandEvent::Stderr(line) => {
                        eprintln!("engine: {}", String::from_utf8_lossy(&line));
                    }
                    CommandEvent::Terminated(_) | CommandEvent::Error(_) => {
                        this.handle_death();
                        break;
                    }
                    // CommandEvent is #[non_exhaustive]: future plugin
                    // versions may add variants we don't need to act on.
                    _ => {}
                }
            }
        });

        Ok(())
    }

    fn handle_line(&self, line: &[u8]) {
        let msg: Value = match serde_json::from_slice(line) {
            Ok(v) => v,
            Err(e) => {
                eprintln!(
                    "engine: undecodable line ({e}): {}",
                    String::from_utf8_lossy(line)
                );
                return;
            }
        };

        // The engine always includes an "id" key in every response - an
        // explicit `null` when a request was too malformed for the id to be
        // recovered, per JSON-RPC 2.0. A message we can't correlate to a
        // pending request (missing id, non-numeric id, or a legitimate
        // `id: null`) would otherwise vanish here with no trace anywhere,
        // and whoever sent the matching request would just wait out the
        // full REQUEST_TIMEOUT with no diagnostic. Log it before dropping.
        let Some(id) = msg.get("id").and_then(|v| v.as_u64()) else {
            eprintln!("engine: uncorrelatable message, dropping: {msg}");
            return;
        };

        if let Some(tx) = self.pending.lock().unwrap().remove(&id) {
            let _ = tx.send(msg);
        }
    }

    fn handle_death(self: &Arc<Self>) {
        if self.shutting_down.load(Ordering::SeqCst) {
            // Deliberate shutdown: shutdown() already set this flag and
            // killed the child before we got here. The app is tearing
            // down and there is nothing left to notify - never respawn.
            return;
        }

        self.fail_all_pending("engine process exited");
        *self.child.lock().unwrap() = None;

        let attempt = {
            let mut restart = self.restart.lock().unwrap();
            let uptime = restart.spawned_at.take().map(|t| t.elapsed());
            match uptime {
                // Stayed up long enough to count as a real session, so a
                // fresh crash right after this is a new problem, not a
                // continuation of an old one: the backoff resets.
                Some(uptime) if uptime >= MIN_HEALTHY_UPTIME => {
                    restart.consecutive_failures = 0;
                }
                // Died fast (or we somehow have no record of starting at
                // all) - count it as a failed start.
                _ => restart.consecutive_failures += 1,
            }
            restart.consecutive_failures
        };

        if attempt > MAX_RESTART_ATTEMPTS {
            eprintln!(
                "engine: {attempt} consecutive failed starts, giving up (last attempt did not stay up {MIN_HEALTHY_UPTIME:?})"
            );
            self.set_state("down");
            return;
        }

        self.set_state("restarting");

        // attempt == 0 means the previous run was healthy and this is a
        // fresh problem: retry promptly at the base delay rather than
        // carrying over backoff from an unrelated, already-resolved streak.
        let delay = if attempt == 0 {
            RESTART_DELAY
        } else {
            RESTART_DELAY
                .saturating_mul(1u32 << (attempt - 1).min(31))
                .min(MAX_RESTART_DELAY)
        };

        let this = Arc::clone(self);
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(delay).await;
            if let Err(e) = this.spawn() {
                eprintln!("engine: restart failed: {e}");
                this.set_state("down");
            }
        });
    }

    /// Sends one request and awaits its response.
    pub async fn request(&self, method: String, params: Option<Value>) -> Result<Value, String> {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = oneshot::channel();

        let mut message = serde_json::json!({
            "jsonrpc": "2.0",
            "id": id,
            "method": method,
        });
        if let Some(p) = params {
            message["params"] = p;
        }

        let mut line = serde_json::to_vec(&message).map_err(|e| e.to_string())?;
        line.push(b'\n');

        // Register before writing, so a fast response cannot arrive first.
        self.pending.lock().unwrap().insert(id, tx);

        // Scoped so the guard is dropped before the await below - holding a
        // std::sync::MutexGuard across an .await would block the executor
        // thread on itself if the task were ever polled elsewhere, and more
        // immediately would make the guard non-Send, which tauri::async_
        // runtime::spawn's `F: Send` bound would reject at compile time.
        {
            let mut guard = self.child.lock().unwrap();
            let child = guard.as_mut().ok_or_else(|| {
                self.pending.lock().unwrap().remove(&id);
                "engine is not running".to_string()
            })?;
            if let Err(e) = child.write(&line) {
                self.pending.lock().unwrap().remove(&id);
                return Err(format!("cannot write to engine: {e}"));
            }
        }

        let response = match tokio::time::timeout(REQUEST_TIMEOUT, rx).await {
            Ok(Ok(v)) => v,
            Ok(Err(_)) => {
                // The oneshot sender was dropped without sending. The only
                // path that drops a pending sender is fail_all_pending,
                // which always sends before dropping, so in practice this
                // means the Engine itself (and its pending map) is being
                // torn down. Remove defensively anyway: it is a no-op if
                // the entry is already gone, and correct if some future
                // code path ever drops a sender without a send.
                self.pending.lock().unwrap().remove(&id);
                return Err("engine died before responding".into());
            }
            Err(_) => {
                self.pending.lock().unwrap().remove(&id);
                return Err("engine timed out".into());
            }
        };

        if let Some(err) = response.get("error") {
            let message = err
                .get("message")
                .and_then(|m| m.as_str())
                .unwrap_or("unknown engine error");
            return Err(message.to_string());
        }

        Ok(response.get("result").cloned().unwrap_or(Value::Null))
    }
}

#[tauri::command]
pub async fn engine_request(
    engine: tauri::State<'_, Arc<Engine>>,
    method: String,
    params: Option<Value>,
) -> Result<Value, String> {
    engine.request(method, params).await
}
