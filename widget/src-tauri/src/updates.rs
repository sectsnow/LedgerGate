use serde::{Deserialize, Serialize};

#[derive(Deserialize)]
struct Release {
    tag_name: String,
    draft: bool,
    prerelease: bool,
}
#[derive(Serialize)]
pub struct Update {
    pub version: String,
    pub available: bool,
}

fn version(value: &str) -> Result<[u64; 3], String> {
    let parts: Vec<_> = value.split('.').collect();
    if parts.len() != 3
        || parts.iter().any(|part| {
            part.is_empty()
                || part.len() > 10
                || !part.bytes().all(|b| b.is_ascii_digit())
                || part.len() > 1 && part.starts_with('0')
        })
    {
        return Err("版本格式无效".into());
    }
    Ok([
        parts[0].parse().map_err(|_| "版本格式无效")?,
        parts[1].parse().map_err(|_| "版本格式无效")?,
        parts[2].parse().map_err(|_| "版本格式无效")?,
    ])
}
pub fn release_url(value: &str, asset: Option<&str>) -> Result<String, String> {
    version(value)?;
    match asset {
        None => Ok(format!("https://github.com/sectsnow/LedgerGate/releases/tag/v{value}")),
        Some("installer") => Ok(format!("https://github.com/sectsnow/LedgerGate/releases/download/v{value}/LedgerGateWidget-Setup-x64.exe")),
        Some("portable") => Ok(format!("https://github.com/sectsnow/LedgerGate/releases/download/v{value}/LedgerGateWidget.exe")),
        _ => Err("下载类型无效".into()),
    }
}
fn read_release(raw: &[u8], current: &str) -> Result<Update, String> {
    let release: Release = serde_json::from_slice(raw).map_err(|_| "版本服务返回格式无效")?;
    if release.draft || release.prerelease {
        return Err("暂未找到正式版本".into());
    }
    let latest = release.tag_name.strip_prefix('v').ok_or("版本格式无效")?;
    Ok(Update {
        version: latest.to_owned(),
        available: version(latest)? > version(current)?,
    })
}
pub async fn check(current: &str) -> Result<Update, String> {
    // 使用独立客户端，只请求固定公开版本接口，不携带 LedgerGate 凭证。
    let client = reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(std::time::Duration::from_secs(15))
        .user_agent("LedgerGateWidget")
        .build()
        .map_err(|_| "无法创建更新检查连接")?;
    let mut response = client
        .get("https://api.github.com/repos/sectsnow/LedgerGate/releases/latest")
        .send()
        .await
        .map_err(|_| "无法连接 GitHub，请检查网络后重试")?;
    if !response.status().is_success() {
        return Err(format!(
            "版本检查失败（HTTP {}），请稍后重试",
            response.status().as_u16()
        ));
    }
    let mut raw = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|_| "读取版本信息失败")? {
        if raw.len() + chunk.len() > 1024 * 1024 {
            return Err("版本信息过大".into());
        }
        raw.extend_from_slice(&chunk);
    }
    read_release(&raw, current)
}
#[cfg(test)]
mod tests {
    #[test]
    fn stable_version_comparison_and_fixed_download_destinations() {
        assert!(
            super::read_release(
                br#"{"tag_name":"v0.1.15","draft":false,"prerelease":false}"#,
                "0.1.14"
            )
            .unwrap()
            .available
        );
        assert!(
            !super::read_release(
                br#"{"tag_name":"v0.1.9","draft":false,"prerelease":false}"#,
                "0.1.14"
            )
            .unwrap()
            .available
        );
        assert!(super::read_release(
            br#"{"tag_name":"v0.2.0-rc.1","draft":false,"prerelease":true}"#,
            "0.1.14"
        )
        .is_err());
        for value in ["0.1.14/evil", "../file", "00.1.2", "1.2", "1.2.3?token=x"] {
            assert!(super::release_url(value, None).is_err());
        }
        assert!(super::release_url("0.1.15", Some("https://evil.invalid")).is_err());
        assert_eq!(
            super::release_url("0.1.15", Some("portable")).unwrap(),
            "https://github.com/sectsnow/LedgerGate/releases/download/v0.1.15/LedgerGateWidget.exe"
        );
    }
}
