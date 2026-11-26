use serde::{Deserialize, Serialize};
use std::sync::Mutex;
use tauri::{Emitter, LogicalSize, Manager, PhysicalPosition, WebviewWindow};

#[derive(Clone, Deserialize, Serialize)]
pub struct Geometry {
    x: i32,
    y: i32,
    width: f64,
    height: f64,
}

#[derive(Default)]
pub struct DisplayState(Mutex<Display>);
#[derive(Default)]
struct Display {
    badge: bool,
    panel: Option<Geometry>,
    hidden: bool,
    suppressed: bool,
    avoid_fullscreen: bool,
    monitor_layout: Option<String>,
}

fn snapshot(window: &WebviewWindow) -> tauri::Result<Geometry> {
    let position = window.outer_position()?;
    let size = window
        .inner_size()?
        .to_logical::<f64>(window.scale_factor()?);
    Ok(Geometry {
        x: position.x,
        y: position.y,
        width: size.width,
        height: size.height,
    })
}

fn apply(
    window: &WebviewWindow,
    badge: bool,
    geometry: &Geometry,
    activate: bool,
) -> tauri::Result<()> {
    window.set_resizable(true)?;
    window.set_max_size(None::<LogicalSize<f64>>)?;
    window.set_min_size(Some(LogicalSize::new(
        if badge { 72.0 } else { 360.0 },
        if badge { 56.0 } else { 360.0 },
    )))?;
    window.set_max_size(Some(LogicalSize::new(
        if badge { 72.0 } else { 720.0 },
        if badge { 56.0 } else { 900.0 },
    )))?;
    window.set_focusable(!badge)?;
    window.set_skip_taskbar(badge)?;
    // 透明窗口的系统阴影会留下浅色外沿，阴影交给前端主题绘制。
    window.set_shadow(false)?;
    window.set_size(LogicalSize::new(geometry.width, geometry.height))?;
    window.set_position(PhysicalPosition::new(geometry.x, geometry.y))?;
    window.set_resizable(!badge)?;
    if activate {
        window.show()?;
        window.set_focus()?;
    }
    Ok(())
}

#[tauri::command]
pub fn set_widget_display(
    app: tauri::AppHandle,
    badge: bool,
    geometry: Geometry,
    activate: bool,
    avoid_fullscreen: bool,
) -> Result<(), String> {
    let valid = geometry.width.is_finite()
        && geometry.height.is_finite()
        && geometry.x.abs_diff(0) <= 1_000_000
        && geometry.y.abs_diff(0) <= 1_000_000
        && if badge {
            geometry.width == 72.0 && geometry.height == 56.0
        } else {
            (360.0..=720.0).contains(&geometry.width) && (360.0..=900.0).contains(&geometry.height)
        };
    if !valid {
        return Err("窗口尺寸或位置无效".into());
    }
    let window = app.get_webview_window("main").ok_or("小部件窗口不存在")?;
    let state = app.state::<DisplayState>();
    let mut state = state.0.lock().map_err(|_| "窗口状态不可用")?;
    if badge && !state.badge {
        state.panel = snapshot(&window).ok();
    }
    if let Err(error) = apply(&window, badge, &geometry, activate) {
        let fallback = state.panel.clone().unwrap_or(Geometry {
            x: geometry.x,
            y: geometry.y,
            width: 400.0,
            height: 720.0,
        });
        let _ = apply(&window, false, &fallback, true);
        state.badge = false;
        state.hidden = false;
        state.suppressed = false;
        let _ = window.emit("widget-show-panel", false);
        return Err(format!("无法切换窗口，已尝试恢复完整面板：{error}"));
    }
    if !badge {
        state.panel = Some(geometry);
    }
    state.badge = badge;
    state.avoid_fullscreen = avoid_fullscreen;
    state.hidden = false;
    state.suppressed = false;
    Ok(())
}

pub fn show_panel(app: &tauri::AppHandle, reset: bool) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    let state = app.state::<DisplayState>();
    let Ok(mut state) = state.0.lock() else {
        return;
    };
    let saved = if state.badge {
        state.panel.clone()
    } else {
        snapshot(&window).ok()
    };
    let mut geometry = saved
        .or_else(|| state.panel.clone())
        .or_else(|| snapshot(&window).ok())
        .unwrap_or(Geometry {
            x: 0,
            y: 0,
            width: 400.0,
            height: 720.0,
        });
    geometry.width = geometry.width.clamp(360.0, 720.0);
    geometry.height = geometry.height.clamp(360.0, 900.0);
    if let Some(monitor) = window
        .current_monitor()
        .ok()
        .flatten()
        .or_else(|| window.primary_monitor().ok().flatten())
    {
        let area = monitor.work_area();
        let scale = monitor.scale_factor();
        geometry.x = geometry.x.clamp(
            area.position.x,
            area.position.x + (area.size.width as f64 - geometry.width * scale).max(0.0) as i32,
        );
        geometry.y = geometry.y.clamp(
            area.position.y,
            area.position.y + (area.size.height as f64 - geometry.height * scale).max(0.0) as i32,
        );
    }
    let result = apply(&window, false, &geometry, true);
    if reset {
        let _ = window.center();
    }
    if let Ok(geometry) = snapshot(&window) {
        state.panel = Some(geometry);
    }
    state.badge = false;
    state.hidden = false;
    state.suppressed = false;
    let _ = window.emit("widget-show-panel", reset);
    if let Err(error) = result {
        eprintln!("恢复完整面板失败：{error}");
    }
}

pub fn hide(app: &tauri::AppHandle) -> Result<(), String> {
    let window = app.get_webview_window("main").ok_or("小部件窗口不存在")?;
    let state = app.state::<DisplayState>();
    let mut state = state.0.lock().map_err(|_| "窗口状态不可用")?;
    window.hide().map_err(|_| "隐藏窗口失败")?;
    state.hidden = true;
    state.suppressed = false;
    let _ = window.emit("widget-tray-hidden", ());
    Ok(())
}

#[tauri::command]
pub fn hide_widget(app: tauri::AppHandle) -> Result<(), String> {
    hide(&app)
}
#[tauri::command]
pub fn widget_pointer_down() -> bool {
    ledgergate_widget::environment::pointer_down()
}
#[tauri::command]
pub fn widget_menu_active(app: tauri::AppHandle) -> bool {
    #[cfg(windows)]
    return app
        .get_webview_window("main")
        .and_then(|window| window.hwnd().ok())
        .map(|handle| ledgergate_widget::environment::menu_active(handle.0 as isize))
        .unwrap_or(false);
    #[cfg(not(windows))]
    {
        let _ = app;
        false
    }
}
#[tauri::command]
pub fn set_widget_fullscreen_avoidance(app: tauri::AppHandle, enabled: bool) -> Result<(), String> {
    app.state::<DisplayState>()
        .0
        .lock()
        .map_err(|_| "窗口状态不可用")?
        .avoid_fullscreen = enabled;
    Ok(())
}

pub fn watch_fullscreen(app: tauri::AppHandle) {
    #[cfg(windows)]
    std::thread::spawn(move || {
        let mut last = std::time::Instant::now();
        loop {
            std::thread::sleep(std::time::Duration::from_secs(1));
            let resumed = last.elapsed() > std::time::Duration::from_secs(4);
            last = std::time::Instant::now();
            let handle = app.clone();
            // 窗口操作和状态锁在 UI 线程内完成，避免后台持锁等待 UI 导致死锁。
            if app
                .run_on_main_thread(move || check_environment(&handle, resumed))
                .is_err()
            {
                break;
            }
        }
    });
    #[cfg(not(windows))]
    let _ = app;
}

#[cfg(windows)]
fn check_environment(app: &tauri::AppHandle, resumed: bool) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    let state = app.state::<DisplayState>();
    let Ok(mut state) = state.0.lock() else {
        return;
    };
    if let Ok(monitors) = window.available_monitors() {
        let layout = monitors
            .iter()
            .map(|monitor| {
                format!(
                    "{:?}:{:?}:{:?}:{}",
                    monitor.position(),
                    monitor.size(),
                    monitor.work_area(),
                    monitor.scale_factor()
                )
            })
            .collect::<Vec<_>>()
            .join(";");
        let changed = state
            .monitor_layout
            .as_ref()
            .is_some_and(|previous| previous != &layout);
        state.monitor_layout = Some(layout);
        if changed {
            let _ = window.emit("widget-environment-changed", ());
        }
    }
    if state.hidden {
        return;
    }
    if resumed {
        let _ = window.emit("widget-resumed", ());
    }
    let fullscreen = state.badge
        && state.avoid_fullscreen
        && window
            .hwnd()
            .map(|handle| ledgergate_widget::environment::fullscreen(handle.0 as isize))
            .unwrap_or(false);
    if fullscreen && !state.suppressed {
        if window.hide().is_ok() {
            state.suppressed = true;
        }
    } else if !fullscreen && state.suppressed {
        if let Ok(handle) = window.hwnd() {
            ledgergate_widget::environment::show_inactive(handle.0 as isize);
            state.suppressed = false;
            let _ = window.emit("widget-resumed", ());
        }
    }
}
