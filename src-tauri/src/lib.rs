mod engine;

use std::sync::Arc;

use tauri::{Manager, RunEvent};

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            let eng = engine::Engine::new(app.handle().clone());
            // Deliberately NOT `eng.spawn()?`. Propagating here reaches
            // build()'s .expect(...) and aborts the process before a window
            // exists - and on a Windows release build, where
            // `windows_subsystem = "windows"` means there is no console,
            // that panic prints to nothing at all: the user's entire
            // diagnostic is "the app doesn't open".
            //
            // The likeliest cause of a first-spawn failure is also the one
            // that most needs explaining: a missing, corrupt, or
            // wrong-architecture sidecar in a fresh install. The UI already
            // has a terminal `down` state built for every *other* failure
            // path, so route this one into it too. mark_down records the
            // reason, which the next `engine_request` returns instead of a
            // bare "engine is not running" - so the window opens and says
            // what is wrong.
            if let Err(e) = eng.spawn() {
                eng.mark_down(format!("initial spawn failed: {e}"));
            }
            app.manage(eng);
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![engine::engine_request])
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(|app_handle, event| {
            // Belt-and-suspenders shutdown: dropping the child when this
            // process exits already closes the sidecar's stdin, which it
            // reads as its own clean-shutdown signal. This handler covers
            // the case where that race goes the other way - see
            // Engine::shutdown for why it's needed at all.
            if let RunEvent::Exit = event {
                app_handle.state::<Arc<engine::Engine>>().shutdown();
            }
        });
}
