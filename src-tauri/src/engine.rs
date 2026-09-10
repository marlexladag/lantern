//! Sidecar lifecycle and JSON-RPC correlation.
//!
//! The Go engine speaks newline-delimited JSON-RPC on stdio. This module owns
//! the child process, matches responses to requests by ID, and restarts the
//! engine if it dies.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde::Serialize;
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

/// Codes for failures that originate in the shell, before or instead of a
/// reply from the engine. JSON-RPC 2.0 reserves -32000..=-32099 for
/// implementation-defined server errors; these are ours, and they never
/// collide with a code the engine itself can produce.
///
/// They are deliberately five distinct values rather than one catch-all.
/// Section 11 of the spec requires every error to normalize to a `Kind`
/// (Auth, Network, Syntax, Constraint, Timeout, Canceled, Unknown) that the
/// UI branches on, and a classifier cannot recover "the engine is not
/// running" (retryable, closer to Network) from "the request timed out"
/// (Timeout) once both have been flattened into prose. Keep them distinct.
pub mod code {
    /// No child process to write to: it never started, or it died and the
    /// supervisor has not replaced it yet.
    pub const ENGINE_UNAVAILABLE: i64 = -32000;
    /// The child exists but the write to its stdin failed.
    pub const TRANSPORT: i64 = -32001;
    /// The child died with this request still in flight.
    pub const ENGINE_DIED: i64 = -32002;
    /// No reply within REQUEST_TIMEOUT.
    pub const TIMEOUT: i64 = -32003;
    /// The request could not be serialized, or the reply could not be
    /// understood as a JSON-RPC response.
    pub const MALFORMED: i64 = -32004;
}

/// One error, carried across the Rust -> TypeScript seam with its structure
/// intact.
///
/// The engine already produces a structured JSON-RPC error (see
/// `internal/rpc/server.go`, which maps handler errors to a code). Flattening
/// that to a string here is lossy in a way that cannot be undone downstream:
/// Section 11 of the spec has the UI branching on a `Kind`, and `Canceled`
/// specifically must not paint the screen red - a bare string has nowhere to
/// put that. This type is the shape with room for it.
///
/// Deliberately NOT here yet: `Kind` classification, `Native` driver text,
/// and any redaction policy. Those belong with the driver work that first
/// creates something to classify; inventing them now would mean guessing at
/// the categories real drivers produce.
#[derive(Debug, Clone, Serialize)]
pub struct EngineError {
    /// JSON-RPC code: the engine's own for a reply it produced, or one of
    /// the `code` constants above for a failure in the shell.
    pub code: i64,
    /// Human-readable summary. Safe to show; not safe to parse.
    pub message: String,
    /// The JSON-RPC `data` member, passed through untouched when the engine
    /// sends one. This is where a driver's structured detail will arrive.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl EngineError {
    fn new(code: i64, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            data: None,
        }
    }

    /// Reads a JSON-RPC `error` member into this type, keeping `code` and
    /// `data` rather than discarding them.
    fn from_wire(err: &Value) -> Self {
        Self {
            code: err
                .get("code")
                .and_then(Value::as_i64)
                .unwrap_or(code::MALFORMED),
            message: err
                .get("message")
                .and_then(Value::as_str)
                .unwrap_or("unknown engine error")
                .to_string(),
            data: err.get("data").cloned(),
        }
    }
}

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
    /// Why the engine is not running, when it isn't. Without this the UI's
    /// only diagnostic for a failed start is "engine is not running", and
    /// the actual reason - a missing, corrupt, or wrong-architecture sidecar
    /// in a fresh install - reaches nothing but stderr, which a Windows
    /// release build (windows_subsystem = "windows") does not even have.
    last_failure: Mutex<Option<String>>,
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
            last_failure: Mutex::new(None),
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
            // Shaped like a JSON-RPC error reply so `request` has exactly
            // one place that turns a wire error into an EngineError.
            let _ = tx.send(serde_json::json!({
                "error": { "code": code::ENGINE_DIED, "message": reason }
            }));
        }
    }

    /// Records why the engine is unavailable and tells the UI. Used for a
    /// failure that leaves no child process behind to retry with.
    pub fn mark_down(&self, reason: String) {
        eprintln!("engine: {reason}");
        *self.last_failure.lock().unwrap() = Some(reason);
        self.set_state("down");
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
        *self.last_failure.lock().unwrap() = None;
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
            self.mark_down(format!(
                "{attempt} consecutive failed starts, giving up (last attempt did not stay up {MIN_HEALTHY_UPTIME:?})"
            ));
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
                this.mark_down(format!("restart failed: {e}"));
            }
        });
    }

    /// Sends one request and awaits its response.
    ///
    /// Every failure - including the ones that never reach the engine -
    /// comes back as an `EngineError` carrying a code, so the caller never
    /// has to parse prose to find out what happened.
    pub async fn request(
        &self,
        method: String,
        params: Option<Value>,
    ) -> Result<Value, EngineError> {
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

        let mut line = serde_json::to_vec(&message).map_err(|e| {
            EngineError::new(code::MALFORMED, format!("cannot encode request: {e}"))
        })?;
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
                // Prefer the recorded reason the engine is not running (a
                // sidecar that could not be resolved or spawned) over the
                // generic symptom. On a fresh install that reason IS the
                // bug report, and stderr may be going nowhere.
                let reason = self
                    .last_failure
                    .lock()
                    .unwrap()
                    .clone()
                    .unwrap_or_else(|| "engine is not running".to_string());
                EngineError::new(code::ENGINE_UNAVAILABLE, reason)
            })?;
            if let Err(e) = child.write(&line) {
                self.pending.lock().unwrap().remove(&id);
                return Err(EngineError::new(
                    code::TRANSPORT,
                    format!("cannot write to engine: {e}"),
                ));
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
                return Err(EngineError::new(
                    code::ENGINE_DIED,
                    "engine died before responding",
                ));
            }
            Err(_) => {
                self.pending.lock().unwrap().remove(&id);
                return Err(EngineError::new(code::TIMEOUT, "engine timed out"));
            }
        };

        if let Some(err) = response.get("error") {
            return Err(EngineError::from_wire(err));
        }

        Ok(response.get("result").cloned().unwrap_or(Value::Null))
    }
}

#[tauri::command]
pub async fn engine_request(
    engine: tauri::State<'_, Arc<Engine>>,
    method: String,
    params: Option<Value>,
) -> Result<Value, EngineError> {
    engine.request(method, params).await
}

#[cfg(test)]
mod tests {
    use super::*;

    // The seam's whole purpose: a JSON-RPC error arrives as structure and
    // leaves as structure. If this ever flattens to a message again, the
    // `Kind` branch that spec section 11 requires has nowhere to read from.
    #[test]
    fn from_wire_keeps_code_message_and_data() {
        let err = serde_json::json!({
            "code": -32601,
            "message": "unknown method: query",
            "data": { "method": "query" },
        });

        let parsed = EngineError::from_wire(&err);

        assert_eq!(parsed.code, -32601);
        assert_eq!(parsed.message, "unknown method: query");
        assert_eq!(parsed.data, Some(serde_json::json!({ "method": "query" })));
    }

    #[test]
    fn from_wire_omits_absent_data() {
        let err = serde_json::json!({ "code": -32603, "message": "internal error" });

        let parsed = EngineError::from_wire(&err);

        assert_eq!(parsed.code, -32603);
        assert!(parsed.data.is_none());
        // `data` is skipped entirely rather than serialized as null, so the
        // TypeScript side sees an absent optional field.
        let json = serde_json::to_value(&parsed).unwrap();
        assert!(json.get("data").is_none());
    }

    // A reply we cannot read is itself an error, and it needs a code of its
    // own rather than borrowing a real one - otherwise a malformed frame is
    // indistinguishable from a genuine engine failure.
    #[test]
    fn from_wire_falls_back_when_the_error_object_is_unusable() {
        let parsed = EngineError::from_wire(&serde_json::json!({ "nonsense": true }));

        assert_eq!(parsed.code, code::MALFORMED);
        assert_eq!(parsed.message, "unknown engine error");
        assert!(parsed.data.is_none());
    }

    // The shell's own codes must never collide with the JSON-RPC reserved
    // range the engine draws from (-32700..=-32600), or a Kind classifier
    // would map a transport failure onto a protocol failure.
    #[test]
    fn shell_codes_are_distinct_and_inside_the_implementation_defined_range() {
        let codes = [
            code::ENGINE_UNAVAILABLE,
            code::TRANSPORT,
            code::ENGINE_DIED,
            code::TIMEOUT,
            code::MALFORMED,
        ];
        for c in codes {
            assert!(
                (-32099..=-32000).contains(&c),
                "{c} is outside the JSON-RPC implementation-defined server error range"
            );
        }
        let mut unique = codes.to_vec();
        unique.sort_unstable();
        unique.dedup();
        assert_eq!(
            unique.len(),
            codes.len(),
            "shell error codes must be unique"
        );
    }
}
