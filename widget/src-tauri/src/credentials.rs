use base64::{engine::general_purpose::STANDARD, Engine};

#[cfg(windows)]
fn crypt(data: &[u8], server: &str, decrypt: bool) -> Result<Vec<u8>, String> {
    use std::ffi::c_void;
    #[repr(C)]
    struct Blob {
        size: u32,
        data: *mut u8,
    }
    #[link(name = "crypt32")]
    unsafe extern "system" {
        fn CryptProtectData(
            input: *const Blob,
            description: *const u16,
            entropy: *const Blob,
            reserved: *mut c_void,
            prompt: *mut c_void,
            flags: u32,
            output: *mut Blob,
        ) -> i32;
        fn CryptUnprotectData(
            input: *const Blob,
            description: *mut *mut u16,
            entropy: *const Blob,
            reserved: *mut c_void,
            prompt: *mut c_void,
            flags: u32,
            output: *mut Blob,
        ) -> i32;
    }
    #[link(name = "kernel32")]
    unsafe extern "system" {
        fn LocalFree(memory: *mut c_void) -> *mut c_void;
    }
    let extra = format!("LedgerGateWidget:{server}");
    let input = Blob {
        size: data.len().try_into().map_err(|_| "凭证过长")?,
        data: data.as_ptr() as *mut u8,
    };
    let entropy = Blob {
        size: extra.len().try_into().map_err(|_| "地址过长")?,
        data: extra.as_ptr() as *mut u8,
    };
    let mut output = Blob {
        size: 0,
        data: std::ptr::null_mut(),
    };
    let success = unsafe {
        if decrypt {
            CryptUnprotectData(
                &input,
                std::ptr::null_mut(),
                &entropy,
                std::ptr::null_mut(),
                std::ptr::null_mut(),
                1,
                &mut output,
            )
        } else {
            CryptProtectData(
                &input,
                std::ptr::null(),
                &entropy,
                std::ptr::null_mut(),
                std::ptr::null_mut(),
                1,
                &mut output,
            )
        }
    };
    if success == 0 || output.data.is_null() {
        return Err("无法使用当前 Windows 用户解密凭证，请重新填写".into());
    }
    let result = unsafe { std::slice::from_raw_parts(output.data, output.size as usize).to_vec() };
    unsafe {
        LocalFree(output.data.cast());
    }
    Ok(result)
}

#[cfg(not(windows))]
fn crypt(_: &[u8], _: &str, _: bool) -> Result<Vec<u8>, String> {
    Err("小部件凭证仅支持在 Windows 用户目录中加密保存".into())
}

pub fn protect(token: &str, server: &str) -> Result<String, String> {
    Ok(STANDARD.encode(crypt(token.as_bytes(), server, false)?))
}

pub fn unprotect(value: &str, server: &str) -> Result<String, String> {
    let data = STANDARD.decode(value).map_err(|_| "凭证缓存格式无效")?;
    String::from_utf8(crypt(&data, server, true)?).map_err(|_| "凭证缓存内容无效".into())
}
