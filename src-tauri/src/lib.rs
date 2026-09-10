mod engine;

use tauri::Manager;

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
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
