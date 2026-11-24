import { useCallback, useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { ArrowUpRight, Check, LoaderCircle, Palette, Pin, RefreshCw, Settings, X } from "lucide-react";
import logo from "../src-tauri/icons/icon.png";
import { amount, finances, type Overview } from "./summary";

type Connection = { server: string; configured: boolean };
const themes = [
  { id: "system", label: "跟随系统" },
  { id: "light", label: "云白" },
  { id: "dark", label: "深海蓝" },
  { id: "emerald", label: "翡翠绿" },
  { id: "violet", label: "暮光紫" },
] as const;
type Theme = typeof themes[number]["id"];
const count = (value: number) => value.toLocaleString("zh-CN");
const compact = (value: number) => value.toLocaleString("zh-CN", { notation: "compact", maximumFractionDigits: 1 });
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
  const [days, setDays] = useState<1 | 7 | 30>(1);
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      const saved = localStorage.getItem("ledgergate-widget-theme");
      return themes.find(item => item.id === saved)?.id ?? "system";
    }
    catch { return "system"; }
  });
  const [themeMenuOpen, setThemeMenuOpen] = useState(false);
  const themeControl = useRef<HTMLDivElement>(null);
  const themeButton = useRef<HTMLButtonElement>(null);
  const [topmost, setTopmost] = useState(() => {
    try { return localStorage.getItem("ledgergate-widget-topmost") !== "false"; }
    catch { return true; }
  });
  const generation = useRef(0);

  useEffect(() => {
    try { localStorage.setItem("ledgergate-widget-theme", theme); } catch { /* 系统禁用本地存储时仅保留当前选择。 */ }
  }, [theme]);

  useEffect(() => {
    if (!themeMenuOpen) return;
    const closeOnOutside = (event: PointerEvent) => {
      if (event.target instanceof Node && !themeControl.current?.contains(event.target)) setThemeMenuOpen(false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setThemeMenuOpen(false); themeButton.current?.focus(); }
    };
    document.addEventListener("pointerdown", closeOnOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => { document.removeEventListener("pointerdown", closeOnOutside); document.removeEventListener("keydown", closeOnEscape); };
  }, [themeMenuOpen]);

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
      const value = await invoke<Overview>("get_overview", { days });
      if (generation.current === current) { setOverview(value); setLastSuccess(Date.now()); setError(""); }
    } catch (reason) {
      if (generation.current === current) setError(String(reason));
    } finally {
      if (generation.current === current) setRefreshing(false);
    }
  }, [days]);

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
  const legacy = !!overview && !overview.period;
  const period = overview?.period?.days === days ? overview.period : null;
  const input = period?.input_tokens ?? (legacy && days === 1 ? overview?.input_tokens : null);
  const output = period?.output_tokens ?? (legacy && days === 1 ? overview?.output_tokens : null);
  const requests = period?.requests ?? (legacy && days === 1 ? overview?.requests : null);
  const estimatedCost = period && !(period.requests > 0 && period.cost_unknown_requests >= period.requests)
    ? `$${(period.cost_micros / 1_000_000).toLocaleString("en-US", { minimumFractionDigits: 4, maximumFractionDigits: 6 })}` : "—";
  return <main className="widget" data-theme={theme}>
    <header className="titlebar" data-tauri-drag-region>
      <div className="brand" data-tauri-drag-region><img src={logo} alt="" draggable={false} data-tauri-drag-region /><span data-tauri-drag-region>LedgerGate</span></div>
      <div className="actions">
        <button type="button" aria-label="刷新数据" title="刷新数据" disabled={refreshing || !connection?.configured || editing} onClick={() => void refresh()}>{refreshing ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button>
        <div className="theme-control" ref={themeControl}>
          <button ref={themeButton} type="button" aria-label="颜色风格" title="颜色风格" aria-expanded={themeMenuOpen} aria-controls="widget-theme-menu" onClick={() => setThemeMenuOpen(value => !value)}><Palette size={16} /></button>
          {themeMenuOpen && <div className="theme-menu" id="widget-theme-menu" role="group" aria-label="颜色风格">
            <p>颜色风格</p>
            {themes.map(item => <button type="button" className="theme-option" key={item.id} aria-pressed={theme === item.id} onClick={() => { setTheme(item.id); setThemeMenuOpen(false); themeButton.current?.focus(); }}><span className={`theme-swatch ${item.id}`} aria-hidden="true" />{item.label}{theme === item.id && <Check size={14} aria-hidden="true" />}</button>)}
          </div>}
        </div>
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
    </form> : <>
      <section className="content" aria-label="运行概览">
        <div className="period-bar"><div><span className="eyebrow">用量概览</span><small className="period-date">{overview ? `${overview.date} · 北京时间` : "北京时间"}</small></div><div className="period-tabs" role="group" aria-label="统计周期">{([[1, "今日"], [7, "近 7 天"], [30, "近 30 天"]] as const).map(([value, label]) => <button key={value} type="button" className={days === value ? "selected" : ""} aria-pressed={days === value} disabled={legacy && value !== 1} onClick={() => setDays(value)}>{label}</button>)}</div></div>
        <div className="hero"><span>Token 总量</span><strong>{input != null && output != null ? count(input + output) : "—"}</strong><small>{input != null && output != null ? `输入 ${count(input)}  ·  输出 ${count(output)}` : "等待读取用量"}</small></div>
        <div className="quick-stats"><div><span>请求</span><strong>{requests != null ? count(requests) : "—"}</strong></div><div><span>错误</span><strong>{period ? count(period.errors) : "—"}</strong></div><div><span>费用估算</span><strong>{estimatedCost}</strong></div></div>
        {period && <p className="detail-line">缓存读取 {count(period.cache_read_tokens)} · 写入 {count(period.cache_write_tokens)}{period.cost_unknown_requests > 0 && ` · ${count(period.cost_unknown_requests)} 次费用未知`}</p>}
        {period && period.retention_days < days && <p className="notice">请求汇总保留 {period.retention_days} 天，当前周期可能不完整。</p>}
        {legacy && <p className="notice">当前服务仅支持今日概览，升级 LedgerGate 后可查看周期汇总和排行。</p>}
        <section className="section finance" aria-label="账户余额及今日实际消费">
          <div className="section-title"><h2>账户</h2><span>上游采集 · 今日消费</span></div>
          <div className="finance-head"><span>余额</span><span>今日实际消费</span></div>
          {groups.length ? groups.map(group => <div className="finance-row" key={group.currency}>
            <div><strong>{amount(group.balance, group.currency)}</strong>{group.balanceCount < group.accounts && <small>已采集 {group.balanceCount}/{group.accounts} 个账户</small>}</div>
            <div><strong>{amount(group.today, group.currency)}</strong>{group.todayCount < group.accounts && <small>已采集 {group.todayCount}/{group.accounts} 个账户</small>}</div>
            {group.attention && <p>部分账户未采集、已暂停或有提醒，数值可能沿用上次结果</p>}
            {group.oldestCheck != null && <p>最早检查 {time(group.oldestCheck)}</p>}
          </div>) : <div className="empty">{overview ? "暂无上游账户" : "等待账户数据"}</div>}
        </section>
        {([ ["模型排行", period?.models], ["渠道排行", period?.channels] ] as const).map(([title, items]) => <section className="section ranking" key={title}>
          <div className="section-title"><h2>{title}</h2><span>前 5 · 按 Token</span></div>
          {items?.length ? <ol>{items.map((item, index) => <li key={`${item.name}-${index}`}><span className="rank-index">{index + 1}</span><span className="rank-name" title={item.name}>{item.name}</span><span className="rank-value" title={`${count(item.tokens)} Token · ${count(item.requests)} 次请求`}>{compact(item.tokens)} <small>· {count(item.requests)} 次</small></span></li>)}</ol> : <p className="empty">{legacy ? "升级服务后可查看" : period ? "暂无统计数据" : "等待读取统计"}</p>}
        </section>)}
      </section>
      <footer className="bottom"><span className={error ? "error" : "hint"} role="status"><i className={`status-dot${error ? " failed" : lastSuccess ? "" : " pending"}`} />{error ? `${lastSuccess ? `数据截至 ${time(lastSuccess)} · ` : ""}${error}` : lastSuccess ? `更新于 ${time(lastSuccess)}` : "等待连接"}</span><button type="button" className="open" onClick={() => void invoke("open_dashboard").catch(reason => setError(String(reason)))}>打开控制台 <ArrowUpRight size={14} /></button></footer>
    </>}
  </main>;
}
