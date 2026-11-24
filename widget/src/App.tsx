import { useCallback, useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { ArrowUpRight, LoaderCircle, Pin, RefreshCw, Settings, X } from "lucide-react";
import logo from "../src-tauri/icons/icon.png";
import { amount, finances, type Overview } from "./summary";

type Connection = { server: string; configured: boolean };
const count = (value: number) => value.toLocaleString("zh-CN");
const time = (value: number) => new Date(value).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });

export default function App() {
  const [connection, setConnection] = useState<Connection | null>(null);
  const [server, setServer] = useState("");
  const [token, setToken] = useState("");
  const [editing, setEditing] = useState(false);
  const [overview, setOverview] = useState<Overview | null>(null);
  const [lastSuccess, setLastSuccess] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [topmost, setTopmost] = useState(() => {
    try { return localStorage.getItem("ledgergate-widget-topmost") !== "false"; }
    catch { return true; }
  });
  const generation = useRef(0);

  useEffect(() => {
    let active = true;
    invoke<Connection>("get_connection").then(value => {
      if (active) { setConnection(value); setServer(value.server); setEditing(!value.configured); }
    }).catch(reason => { if (active) { setConnection({ server: "", configured: false }); setEditing(true); setError(String(reason)); } });
    return () => { active = false; generation.current++; };
  }, []);

  useEffect(() => {
    void getCurrentWindow().setAlwaysOnTop(topmost).catch(reason => setError(String(reason)));
    try { localStorage.setItem("ledgergate-widget-topmost", String(topmost)); } catch { /* 系统禁用本地存储时仅保留当前选择。 */ }
  }, [topmost]);

  const refresh = useCallback(async () => {
    const current = ++generation.current;
    setRefreshing(true);
    try {
      const value = await invoke<Overview>("get_overview");
      if (generation.current === current) { setOverview(value); setLastSuccess(Date.now()); setError(""); }
    } catch (reason) {
      if (generation.current === current) setError(String(reason));
    } finally {
      if (generation.current === current) setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    if (!connection?.configured) return;
    void refresh();
    const timer = window.setInterval(() => void refresh(), 30_000);
    return () => { window.clearInterval(timer); generation.current++; };
  }, [connection?.configured, refresh, refreshKey]);

  async function save(event: React.FormEvent) {
    event.preventDefault();
    generation.current++; setRefreshing(false); setSaving(true); setError("");
    try {
      const value = await invoke<Connection>("configure", { server, token });
      setConnection(value); setServer(value.server); setToken(""); setOverview(null); setLastSuccess(null); setEditing(false); setRefreshKey(current => current + 1);
    } catch (reason) { setError(String(reason)); }
    finally { setSaving(false); }
  }

  const groups = overview ? finances(overview) : [];
  return <main className="widget">
    <header className="titlebar" data-tauri-drag-region>
      <div className="brand" data-tauri-drag-region><img src={logo} alt="" draggable={false} data-tauri-drag-region /><span data-tauri-drag-region>LedgerGate</span></div>
      <div className="actions">
        <button type="button" className={topmost ? "active" : ""} aria-label={topmost ? "取消置顶" : "置顶窗口"} title={topmost ? "取消置顶" : "置顶窗口"} onClick={() => setTopmost(value => !value)}><Pin size={16} /></button>
        <button type="button" aria-label="连接设置" title="连接设置" onClick={() => { setEditing(value => !value); setError(""); setToken(""); }}><Settings size={16} /></button>
        <button type="button" aria-label="隐藏到托盘" title="隐藏到托盘" onClick={() => void getCurrentWindow().hide().catch(reason => setError(String(reason)))}><X size={16} /></button>
      </div>
    </header>
    {editing || !connection?.configured ? <form className="settings" onSubmit={event => void save(event)}>
      <div><h1>连接 LedgerGate</h1><p>在 LedgerGate 的系统设置中生成小部件只读凭证。</p></div>
      <label>服务地址<input type="url" required maxLength={2048} placeholder="http://192.168.1.10:8080" value={server} disabled={saving} onChange={event => setServer(event.target.value)} /></label>
      <label>只读凭证<input type="password" autoComplete="off" placeholder={connection?.configured ? "留空保留已有凭证" : "粘贴 lgw_ 开头的凭证"} value={token} disabled={saving} onChange={event => setToken(event.target.value)} /></label>
      <p className="hint">凭证加密保存在当前 Windows 用户目录；更换服务地址时需重新填写。远程连接建议使用 HTTPS。</p>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="settings-actions">{connection?.configured && <button type="button" className="secondary" disabled={saving} onClick={() => { setEditing(false); setError(""); setToken(""); }}>取消</button>}<button type="submit" className="primary" disabled={saving || !server.trim()}>{saving ? "正在保存…" : "保存并连接"}</button></div>
    </form> : <section className="content">
      <div className="heading"><div><h1>运行概览</h1><p>{overview ? `${overview.date} · 北京时间` : "正在读取今日数据"}</p></div><button type="button" className="refresh" aria-label="刷新数据" title="刷新数据" disabled={refreshing} onClick={() => void refresh()}>{refreshing ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}</button></div>
      <div className="finance" aria-label="账户余额及今日实际消费">
        <div className="finance-head"><span>账户余额</span><span>今日实际消费</span></div>
        {groups.length ? groups.map(group => <div className="finance-row" key={group.currency}>
          <div><strong>{amount(group.balance, group.currency)}</strong>{group.balanceCount < group.accounts && <small>已采集 {group.balanceCount}/{group.accounts} 个账户</small>}</div>
          <div><strong>{amount(group.today, group.currency)}</strong>{group.todayCount < group.accounts && <small>已采集 {group.todayCount}/{group.accounts} 个账户</small>}</div>
          {group.attention && <p>部分账户未采集、已暂停或有提醒，数值可能沿用上次结果</p>}
          {group.oldestCheck != null && <p>最早检查 {time(group.oldestCheck)}</p>}
        </div>) : <div className="empty">{overview ? "暂无上游账户" : "等待账户数据"}</div>}
      </div>
      <div className="metrics"><div><span>今日 Token</span><strong>{overview ? count(overview.input_tokens + overview.output_tokens) : "—"}</strong><small>{overview ? `输入 ${count(overview.input_tokens)} · 输出 ${count(overview.output_tokens)}` : "等待用量"}</small></div><div><span>今日请求</span><strong>{overview ? count(overview.requests) : "—"}</strong><small>已结束的请求</small></div></div>
      <div className="bottom"><span className={error ? "error" : "hint"} role="status">{error ? `${lastSuccess ? `数据截至 ${time(lastSuccess)} · ` : ""}${error}` : lastSuccess ? `更新于 ${time(lastSuccess)}` : "等待连接"}</span><button type="button" className="open" onClick={() => void invoke("open_dashboard").catch(reason => setError(String(reason)))}>打开控制台 <ArrowUpRight size={14} /></button></div>
    </section>}
  </main>;
}
