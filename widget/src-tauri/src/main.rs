#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use ledgergate_widget::{
    connection_status, fetch_overview, load_connection, save_connection, ConnectionStatus,
    WidgetOverview,
};
use std::path::PathBuf;
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Manager, WindowEvent,
};
use tauri_plugin_opener::OpenerExt;

fn settings_path(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    Ok(app
        .path()
        .app_local_data_dir()
        .map_err(|_| "无法读取 Windows 用户目录")?
        .join("settings.json"))
}

#[tauri::command]
fn get_connection(app: tauri::AppHandle) -> Result<ConnectionStatus, String> {
    connection_status(&settings_path(&app)?)
}

#[tauri::command]
fn configure(
    app: tauri::AppHandle,
    server: String,
    token: String,
) -> Result<ConnectionStatus, String> {
    save_connection(&settings_path(&app)?, &server, &token)
}

#[tauri::command]
async fn get_overview(app: tauri::AppHandle, days: u8) -> Result<WidgetOverview, String> {
    let connection =
        load_connection(&settings_path(&app)?)?.ok_or("请先配置 LedgerGate 地址和只读凭证")?;
    fetch_overview(connection, days).await
}

#[tauri::command]
fn open_dashboard(app: tauri::AppHandle) -> Result<(), String> {
    let status = connection_status(&settings_path(&app)?)?;
    if status.server.is_empty() {
        return Err("请先配置 LedgerGate 地址".into());
    }
    app.opener()
        .open_url(status.server, None::<&str>)
        .map_err(|_| "无法打开 LedgerGate 控制台".into())
}

fn show_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.set_focus();
    }
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![
            get_connection,
            configure,
            get_overview,
            open_dashboard
        ])
        .setup(|app| {
            let show = MenuItem::with_id(app, "show", "显示小部件", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show, &quit])?;
            let mut tray = TrayIconBuilder::new()
                .menu(&menu)
                .tooltip("LedgerGate")
                .on_menu_event(|app, event| match event.id().as_ref() {
                    "show" => show_window(app),
                    "quit" => app.exit(0),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if matches!(
                        event,
                        TrayIconEvent::Click {
                            button: MouseButton::Left,
                            button_state: MouseButtonState::Up,
                            ..
                        }
                    ) {
                        show_window(tray.app_handle());
                    }
                });
            if let Some(icon) = app.default_window_icon() {
                tray = tray.icon(icon.clone());
            }
            tray.build(app)?;
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .run(tauri::generate_context!())
        .expect("LedgerGate 桌面小部件启动失败");
}
