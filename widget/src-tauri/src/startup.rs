#[cfg(any(windows, test))]
fn command(path: &std::path::Path) -> Result<String, String> {
    let path = path.to_str().ok_or("程序路径无法编码")?;
    if path.contains(['\0', '"']) {
        return Err("程序路径无效".into());
    }
    Ok(format!("\"{path}\""))
}

#[cfg(windows)]
mod windows {
    use std::ffi::c_void;
    type Key = *mut c_void;
    const CURRENT_USER: Key = -2147483647isize as Key;
    const RUN: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Run";
    const NAME: &str = "LedgerGateWidget";
    fn wide(value: &str) -> Vec<u16> {
        value.encode_utf16().chain(Some(0)).collect()
    }
    struct OwnedKey(Key);
    impl Drop for OwnedKey {
        fn drop(&mut self) {
            unsafe {
                RegCloseKey(self.0);
            }
        }
    }
    #[link(name = "advapi32")]
    unsafe extern "system" {
        fn RegOpenKeyExW(
            key: Key,
            path: *const u16,
            options: u32,
            access: u32,
            result: *mut Key,
        ) -> i32;
        fn RegCreateKeyExW(
            key: Key,
            path: *const u16,
            reserved: u32,
            class: *mut u16,
            options: u32,
            access: u32,
            security: *mut c_void,
            result: *mut Key,
            disposition: *mut u32,
        ) -> i32;
        fn RegQueryValueExW(
            key: Key,
            name: *const u16,
            reserved: *mut u32,
            kind: *mut u32,
            data: *mut u8,
            size: *mut u32,
        ) -> i32;
        fn RegSetValueExW(
            key: Key,
            name: *const u16,
            reserved: u32,
            kind: u32,
            data: *const u8,
            size: u32,
        ) -> i32;
        fn RegDeleteValueW(key: Key, name: *const u16) -> i32;
        fn RegCloseKey(key: Key) -> i32;
    }
    fn open(create: bool, access: u32) -> Result<Option<OwnedKey>, String> {
        let mut key = std::ptr::null_mut();
        let status = unsafe {
            if create {
                RegCreateKeyExW(
                    CURRENT_USER,
                    wide(RUN).as_ptr(),
                    0,
                    std::ptr::null_mut(),
                    0,
                    access,
                    std::ptr::null_mut(),
                    &mut key,
                    std::ptr::null_mut(),
                )
            } else {
                RegOpenKeyExW(CURRENT_USER, wide(RUN).as_ptr(), 0, access, &mut key)
            }
        };
        if status == 2 {
            return Ok(None);
        }
        if status != 0 {
            return Err(format!("无法访问当前用户启动项（{status}）"));
        }
        Ok(Some(OwnedKey(key)))
    }
    pub fn enabled() -> Result<bool, String> {
        let Some(key) = open(false, 1)? else {
            return Ok(false);
        };
        let mut data = vec![0u16; 16384];
        let mut size = (data.len() * 2) as u32;
        let mut kind = 0;
        let status = unsafe {
            RegQueryValueExW(
                key.0,
                wide(NAME).as_ptr(),
                std::ptr::null_mut(),
                &mut kind,
                data.as_mut_ptr().cast(),
                &mut size,
            )
        };
        if status == 2 {
            return Ok(false);
        }
        if status != 0 {
            return Err(format!("读取启动项失败（{status}）"));
        }
        if kind != 1 || size % 2 != 0 {
            return Ok(false);
        }
        data.truncate(size as usize / 2);
        while data.last() == Some(&0) {
            data.pop();
        }
        let actual = String::from_utf16(&data).map_err(|_| "启动项格式无效")?;
        Ok(actual == super::command(&std::env::current_exe().map_err(|_| "无法定位当前程序")?)?)
    }
    pub fn set(enabled: bool) -> Result<bool, String> {
        let Some(key) = open(enabled, 2)? else {
            return Ok(false);
        };
        let name = wide(NAME);
        let status = unsafe {
            if enabled {
                let data = wide(&super::command(
                    &std::env::current_exe().map_err(|_| "无法定位当前程序")?,
                )?);
                RegSetValueExW(
                    key.0,
                    name.as_ptr(),
                    0,
                    1,
                    data.as_ptr().cast(),
                    (data.len() * 2) as u32,
                )
            } else {
                RegDeleteValueW(key.0, name.as_ptr())
            }
        };
        if status != 0 && !(status == 2 && !enabled) {
            return Err(format!("修改启动项失败（{status}）"));
        }
        self::enabled()
    }
}
#[cfg(windows)]
pub use windows::{enabled, set};
#[cfg(not(windows))]
pub fn enabled() -> Result<bool, String> {
    Err("开机启动仅支持 Windows".into())
}
#[cfg(not(windows))]
pub fn set(_: bool) -> Result<bool, String> {
    enabled()
}

#[cfg(test)]
mod tests {
    #[test]
    fn startup_command_quotes_spaces_and_unicode_without_shell_arguments() {
        assert_eq!(
            super::command(std::path::Path::new("C:\\我的工具\\Ledger Gate.exe")).unwrap(),
            "\"C:\\我的工具\\Ledger Gate.exe\""
        );
        assert!(super::command(std::path::Path::new("a\"b.exe")).is_err());
    }
}
