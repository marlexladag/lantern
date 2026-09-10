//! Sidecar lifecycle and JSON-RPC correlation.
//!
//! The Go engine speaks newline-delimited JSON-RPC on stdio. This module owns
//! the child process, matches responses to requests by ID, and restarts the
//! engine if it dies.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::Value;
use tauri::{AppHandle, Emitter};
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;
use tokio::sync::oneshot;

/// How long a single request may wait before the caller gives up.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);
/// Pause before relaunching a crashed engine, so a crash loop cannot spin.
const RESTART_DELAY: Duration = Duration::from_millis(500);

pub struct Engine {
    app: AppHandle,
    next_id: AtomicU64,
    pending: Mutex<HashMap<u64, oneshot::Sender<Value>>>,
    child: Mutex<Option<CommandChild>>,
}

impl Engine {
    pub fn new(app: AppHandle) -> Arc<Self> {
        Arc::new(Self {
            app,
            next_id: AtomicU64::new(1),
            pending: Mutex::new(HashMap::new()),
            child: Mutex::new(None),
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

    /// Launches the sidecar and starts the reader task. Safe to call again
    /// after a crash.
    pub fn spawn(self: &Arc<Self>) -> Result<(), String> {
        let command = self
            .app
            .shell()
            .sidecar("engine")
            .map_err(|e| format!("cannot resolve sidecar: {e}"))?;

        let (mut rx, child) = command
            .spawn()
            .map_err(|e| format!("cannot spawn sidecar: {e}"))?;

        *self.child.lock().unwrap() = Some(child);
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

        // Notifications carry no id and are not responses to anything.
        let Some(id) = msg.get("id").and_then(|v| v.as_u64()) else {
            return;
        };

        if let Some(tx) = self.pending.lock().unwrap().remove(&id) {
            let _ = tx.send(msg);
        }
    }

    fn handle_death(self: &Arc<Self>) {
        self.fail_all_pending("engine process exited");
        *self.child.lock().unwrap() = None;
        self.set_state("restarting");

        let this = Arc::clone(self);
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(RESTART_DELAY).await;
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
