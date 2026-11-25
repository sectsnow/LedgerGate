#[cfg(windows)]
mod windows {
    use std::ffi::c_void;
    type Handle = *mut c_void;
    #[repr(C)]
    #[derive(Default)]
    struct Rect {
        left: i32,
        top: i32,
        right: i32,
        bottom: i32,
    }
    #[repr(C)]
    struct MonitorInfo {
        size: u32,
        monitor: Rect,
        work: Rect,
        flags: u32,
    }
    #[repr(C)]
    struct GuiInfo {
        size: u32,
        flags: u32,
        active: Handle,
        focus: Handle,
        capture: Handle,
        menu_owner: Handle,
        move_size: Handle,
        caret: Handle,
        caret_rect: Rect,
    }
    #[link(name = "user32")]
    unsafe extern "system" {
        fn GetForegroundWindow() -> Handle;
        fn GetWindowRect(window: Handle, rect: *mut Rect) -> i32;
        fn GetClassNameW(window: Handle, name: *mut u16, max: i32) -> i32;
        fn GetWindowThreadProcessId(window: Handle, process: *mut u32) -> u32;
        fn MonitorFromWindow(window: Handle, flags: u32) -> Handle;
        fn GetMonitorInfoW(monitor: Handle, info: *mut MonitorInfo) -> i32;
        fn IsIconic(window: Handle) -> i32;
        fn IsWindowVisible(window: Handle) -> i32;
        fn GetAsyncKeyState(key: i32) -> i16;
        fn ShowWindow(window: Handle, command: i32) -> i32;
        fn GetGUIThreadInfo(thread: u32, info: *mut GuiInfo) -> i32;
    }
    pub fn pointer_down() -> bool {
        unsafe { GetAsyncKeyState(1) < 0 }
    }
    pub fn show_inactive(window: isize) {
        unsafe {
            ShowWindow(window as Handle, 4);
        }
    }
    pub fn menu_active(window: isize) -> bool {
        unsafe {
            let thread = GetWindowThreadProcessId(window as Handle, std::ptr::null_mut());
            let mut info: GuiInfo = std::mem::zeroed();
            info.size = std::mem::size_of::<GuiInfo>() as u32;
            thread != 0
                && GetGUIThreadInfo(thread, &mut info) != 0
                && (info.flags & 0x1c != 0 || !info.menu_owner.is_null())
        }
    }
    pub fn fullscreen(window: isize) -> bool {
        unsafe {
            let foreground = GetForegroundWindow();
            if foreground.is_null() || IsIconic(foreground) != 0 || IsWindowVisible(foreground) == 0
            {
                return false;
            }
            let mut process = 0;
            GetWindowThreadProcessId(foreground, &mut process);
            if process == std::process::id() {
                return false;
            }
            let mut class = [0u16; 128];
            let length = GetClassNameW(foreground, class.as_mut_ptr(), class.len() as i32);
            let name = String::from_utf16_lossy(&class[..length.max(0) as usize]);
            if matches!(
                name.as_str(),
                "Progman" | "WorkerW" | "Shell_TrayWnd" | "Shell_SecondaryTrayWnd"
            ) {
                return false;
            }
            let monitor = MonitorFromWindow(window as Handle, 2);
            if monitor.is_null() || monitor != MonitorFromWindow(foreground, 2) {
                return false;
            }
            let mut info = MonitorInfo {
                size: std::mem::size_of::<MonitorInfo>() as u32,
                monitor: Rect::default(),
                work: Rect::default(),
                flags: 0,
            };
            let mut rect = Rect::default();
            if GetMonitorInfoW(monitor, &mut info) == 0 || GetWindowRect(foreground, &mut rect) == 0
            {
                return false;
            }
            super::covers_monitor(
                [rect.left, rect.top, rect.right, rect.bottom],
                [
                    info.monitor.left,
                    info.monitor.top,
                    info.monitor.right,
                    info.monitor.bottom,
                ],
            )
        }
    }
}

#[cfg(any(windows, test))]
fn covers_monitor(window: [i32; 4], monitor: [i32; 4]) -> bool {
    window[2] > window[0]
        && window[3] > window[1]
        && window[0] <= monitor[0] + 2
        && window[1] <= monitor[1] + 2
        && window[2] >= monitor[2] - 2
        && window[3] >= monitor[3] - 2
}

#[cfg(windows)]
pub use windows::{fullscreen, menu_active, pointer_down, show_inactive};
#[cfg(not(windows))]
pub fn pointer_down() -> bool {
    false
}

#[cfg(test)]
mod tests {
    #[test]
    fn fullscreen_requires_monitor_coverage_not_just_a_maximized_work_area() {
        assert!(super::covers_monitor(
            [-1920, 0, 0, 1080],
            [-1920, 0, 0, 1080]
        ));
        assert!(!super::covers_monitor(
            [0, 0, 1920, 1040],
            [0, 0, 1920, 1080]
        ));
        assert!(!super::covers_monitor(
            [100, 100, 100, 100],
            [0, 0, 1920, 1080]
        ));
    }
}
