mod credentials;
pub mod environment;

use reqwest::{redirect::Policy, Url};
use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::{Read, Write},
    path::Path,
    time::Duration,
};

#[derive(Clone)]
pub struct Connection {
    server: String,
    token: String,
}

#[derive(Serialize, Deserialize)]
struct SavedConnection {
    server: String,
    token_dpapi: String,
}

#[derive(Serialize)]
pub struct ConnectionStatus {
    pub server: String,
    configured: bool,
}

#[derive(Serialize, Deserialize)]
pub struct WidgetAccount {
    currency: String,
    balance: Option<f64>,
    today_used: Option<f64>,
    today_date: String,
    status: String,
    enabled: bool,
    checked_at: Option<String>,
    has_warning: bool,
}

#[derive(Serialize, Deserialize)]
pub struct WidgetRank {
    name: String,
    requests: i64,
    tokens: i64,
}

#[derive(Serialize, Deserialize)]
pub struct WidgetTrend {
    date: String,
    available: bool,
    requests: i64,
    errors: i64,
    input_tokens: i64,
    output_tokens: i64,
    cost_micros: i64,
    cost_unknown_requests: i64,
}

#[derive(Serialize, Deserialize)]
pub struct WidgetPeriod {
    days: u8,
    retention_days: i64,
    requests: i64,
    errors: i64,
    input_tokens: i64,
    output_tokens: i64,
    cache_read_tokens: i64,
    cache_write_tokens: i64,
    cost_micros: i64,
    cost_unknown_requests: i64,
    #[serde(default)]
    trend: Vec<WidgetTrend>,
    models: Vec<WidgetRank>,
    channels: Vec<WidgetRank>,
}

#[derive(Serialize, Deserialize)]
pub struct WidgetOverview {
    date: String,
    accounts: Vec<WidgetAccount>,
    requests: i64,
    input_tokens: i64,
    output_tokens: i64,
    #[serde(default)]
    period: Option<WidgetPeriod>,
}

fn normalize_server(server: &str) -> Result<String, String> {
    if server.len() > 2048 {
        return Err("LedgerGate 地址过长".into());
    }
    let url = Url::parse(server.trim()).map_err(|_| "请输入有效的 LedgerGate 地址")?;
    if !matches!(url.scheme(), "http" | "https")
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err("地址须为 HTTP 或 HTTPS，且不能包含用户名、密码、查询参数或片段".into());
    }
    Ok(url.as_str().trim_end_matches('/').to_owned())
}

fn validate_token(token: &str) -> Result<(), String> {
    let value = token
        .strip_prefix("lgw_")
        .ok_or("请填写 LedgerGate 小部件凭证")?;
    if value.len() != 43
        || !value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    {
        return Err("小部件凭证格式无效".into());
    }
    Ok(())
}

pub fn load_connection(path: &Path) -> Result<Option<Connection>, String> {
    let file = match fs::File::open(path) {
        Ok(value) => value,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(_) => return Err("无法读取小部件设置".into()),
    };
    let mut raw = Vec::new();
    file.take(16_385)
        .read_to_end(&mut raw)
        .map_err(|_| "无法读取小部件设置")?;
    if raw.len() > 16_384 {
        return Err("小部件设置文件过大，请重新配置".into());
    }
    let saved: SavedConnection =
        serde_json::from_slice(&raw).map_err(|_| "小部件设置格式无效，请重新配置")?;
    let server = normalize_server(&saved.server)?;
    let token = credentials::unprotect(&saved.token_dpapi, &server)?;
    validate_token(&token)?;
    Ok(Some(Connection { server, token }))
}

pub fn connection_status(path: &Path) -> Result<ConnectionStatus, String> {
    match load_connection(path)? {
        Some(value) => Ok(ConnectionStatus {
            server: value.server,
            configured: true,
        }),
        None => Ok(ConnectionStatus {
            server: String::new(),
            configured: false,
        }),
    }
}

pub fn save_connection(path: &Path, server: &str, token: &str) -> Result<ConnectionStatus, String> {
    let server = normalize_server(server)?;
    let token = if token.trim().is_empty() {
        let previous = load_connection(path)?.ok_or("请填写小部件凭证")?;
        if previous.server != server {
            return Err("更换地址时请重新填写小部件凭证".into());
        }
        previous.token
    } else {
        token.trim().to_owned()
    };
    validate_token(&token)?;
    let saved = SavedConnection {
        server: server.clone(),
        token_dpapi: credentials::protect(&token, &server)?,
    };
    let parent = path.parent().ok_or("设置目录无效")?;
    fs::create_dir_all(parent).map_err(|_| "无法创建小部件设置目录")?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent).map_err(|_| "无法创建临时设置")?;
    serde_json::to_writer(&mut temporary, &saved).map_err(|_| "无法生成小部件设置")?;
    temporary.flush().map_err(|_| "无法写入小部件设置")?;
    temporary
        .as_file()
        .sync_all()
        .map_err(|_| "无法保存小部件设置")?;
    temporary.persist(path).map_err(|_| "无法替换小部件设置")?;
    Ok(ConnectionStatus {
        server,
        configured: true,
    })
}

pub async fn fetch_overview(connection: Connection, days: u8) -> Result<WidgetOverview, String> {
    if !matches!(days, 1 | 7 | 30) {
        return Err("统计周期无效".into());
    }
    let client = reqwest::Client::builder()
        .redirect(Policy::none())
        .timeout(Duration::from_secs(12))
        .no_proxy()
        .build()
        .map_err(|_| "无法创建连接")?;
    let mut response = client
        .get(format!(
            "{}/api/widget/overview?days={days}",
            connection.server
        ))
        .bearer_auth(connection.token)
        .send()
        .await
        .map_err(|_| "无法连接 LedgerGate，请检查地址和网络")?;
    if response.status() == reqwest::StatusCode::UNAUTHORIZED {
        return Err("小部件凭证无效或已撤销，请重新配置".into());
    }
    if !response.status().is_success() {
        return Err(format!(
            "LedgerGate 返回 HTTP {}",
            response.status().as_u16()
        ));
    }
    let mut raw = Vec::new();
    while let Some(chunk) = response
        .chunk()
        .await
        .map_err(|_| "读取 LedgerGate 数据失败")?
    {
        if raw.len() + chunk.len() > 1024 * 1024 {
            return Err("LedgerGate 返回数据过大".into());
        }
        raw.extend_from_slice(&chunk);
    }
    serde_json::from_slice(&raw).map_err(|_| "LedgerGate 返回的数据格式无效".into())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::{Read, Write},
        net::TcpListener,
        thread,
    };

    #[test]
    fn connection_validation_rejects_unsafe_urls_and_tokens() {
        assert_eq!(
            normalize_server("https://example.test/prefix/").unwrap(),
            "https://example.test/prefix"
        );
        for url in [
            "file:///tmp/a",
            "https://user:pass@example.test",
            "http://host/?secret=x",
            "http://host/#x",
            "not a url",
        ] {
            assert!(normalize_server(url).is_err(), "{url}");
        }
        assert!(validate_token(&format!("lgw_{}", "a".repeat(43))).is_ok());
        for token in ["", "lgw_abc", "lgw_abc\r\nAuthorization: secret"] {
            assert!(validate_token(token).is_err());
        }
    }

    #[test]
    fn overview_decodes_period_totals_and_rankings() {
        let body = r#"{"date":"2026-11-24","accounts":[],"requests":1,"input_tokens":10,"output_tokens":5,"period":{"days":7,"retention_days":30,"requests":3,"errors":1,"input_tokens":100,"output_tokens":20,"cache_read_tokens":40,"cache_write_tokens":4,"cost_micros":1234,"cost_unknown_requests":1,"trend":[{"date":"2026-11-18","available":false,"requests":0,"errors":0,"input_tokens":0,"output_tokens":0,"cost_micros":0,"cost_unknown_requests":0},{"date":"2026-11-24","available":true,"requests":3,"errors":1,"input_tokens":100,"output_tokens":20,"cost_micros":1234,"cost_unknown_requests":1}],"models":[{"name":"model-a","requests":2,"tokens":90}],"channels":[{"name":"channel-a","requests":3,"tokens":120}]}}"#;
        let overview: WidgetOverview = serde_json::from_str(body).unwrap();
        let period = overview.period.unwrap();
        assert_eq!(period.days, 7);
        assert_eq!(period.cost_unknown_requests, 1);
        assert_eq!(period.trend.len(), 2);
        assert!(!period.trend[0].available);
        assert_eq!(period.trend[1].requests, 3);
        assert_eq!(period.models[0].name, "model-a");
        assert_eq!(period.channels[0].tokens, 120);
    }

    #[tokio::test]
    async fn overview_uses_only_read_only_endpoint_and_does_not_follow_redirects() {
        for status in [200, 302] {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            let addr = listener.local_addr().unwrap();
            let server = thread::spawn(move || {
                let (mut socket, _) = listener.accept().unwrap();
                socket
                    .set_read_timeout(Some(Duration::from_secs(5)))
                    .unwrap();
                let mut request = Vec::new();
                let mut buffer = [0; 1024];
                while !request.ends_with(b"\r\n\r\n") {
                    let count = socket.read(&mut buffer).unwrap();
                    assert!(count > 0 && request.len() + count <= 8192);
                    request.extend_from_slice(&buffer[..count]);
                }
                let request = String::from_utf8_lossy(&request).to_lowercase();
                assert!(request.starts_with("get /prefix/api/widget/overview?days=7 "));
                assert!(request.contains(&format!("authorization: bearer lgw_{}", "a".repeat(43))));
                let body = r#"{"date":"2026-11-24","accounts":[],"requests":3,"input_tokens":100,"output_tokens":20}"#;
                write!(socket, "HTTP/1.1 {status} test\r\nContent-Length: {}\r\nLocation: http://127.0.0.1:1/\r\nConnection: close\r\n\r\n{body}", body.len()).unwrap();
            });
            let connection = Connection {
                server: format!("http://{addr}/prefix"),
                token: format!("lgw_{}", "a".repeat(43)),
            };
            let result = fetch_overview(connection, 7).await;
            if status == 200 {
                let overview = result.unwrap();
                assert_eq!(overview.requests, 3);
                assert!(overview.period.is_none());
            } else {
                assert!(result.err().unwrap().contains("302"));
            }
            server.join().unwrap();
        }
    }

    #[cfg(windows)]
    #[test]
    fn credential_is_encrypted_and_bound_to_server() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("settings.json");
        let token = format!("lgw_{}", "a".repeat(43));
        save_connection(&path, "http://localhost:8080", &token).unwrap();
        assert!(!String::from_utf8(fs::read(&path).unwrap())
            .unwrap()
            .contains(&token));
        let saved = load_connection(&path).unwrap().unwrap();
        assert_eq!(saved.token, token);
        assert!(save_connection(&path, "http://other:8080", "").is_err());
        save_connection(&path, "http://localhost:8080", "").unwrap();
        assert_eq!(load_connection(&path).unwrap().unwrap().token, token);
    }
}
