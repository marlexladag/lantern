mod engine;

use std::sync::Arc;

use tauri::{Manager, RunEvent};

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            let eng = engine::Engine::new(app.handle().clone());
            eng.spawn()?;
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
