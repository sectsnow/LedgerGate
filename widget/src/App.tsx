import { useCallback, useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { availableMonitors, getCurrentWindow, LogicalSize, PhysicalPosition, primaryMonitor, type PhysicalSize } from "@tauri-apps/api/window";
import { ArrowUpRight, Check, LoaderCircle, Palette, Pause, Pin, Play, RefreshCw, Settings, X } from "lucide-react";
import logo from "../src-tauri/icons/icon.png";
import { amount, finances, type Overview, type Trend } from "./summary";
import { parseWindowState, resolveWindowGeometry, serializeWindowState, WINDOW_STATE_STORAGE_KEY } from "./windowState";

type Connection = { server: string; configured: boolean };
const themes = [
  { id: "system", label: "跟随系统" },
  { id: "light", label: "云白" },
  { id: "dark", label: "深海蓝" },
  { id: "emerald", label: "翡翠绿" },
  { id: "violet", label: "暮光紫" },
] as const;
type Theme = typeof themes[number]["id"];
const views = ["overview", "accounts", "analysis", "alerts", "ranking"] as const;
type WidgetView = typeof views[number];
const viewLabels: Record<WidgetView, string> = { overview: "概览", accounts: "账户", analysis: "分析", alerts: "告警", ranking: "排行" };
const count = (value: number) => value.toLocaleString("zh-CN");
const compact = (value: number) => value.toLocaleString("zh-CN", { notation: "compact", maximumFractionDigits: 1 });
const time = (value: number) => new Date(value).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });

async function restoreWindowState(window: ReturnType<typeof getCurrentWindow>) {
  let saved: ReturnType<typeof parseWindowState> = null;
  try { saved = parseWindowState(localStorage.getItem(WINDOW_STATE_STORAGE_KEY)); } catch { return; }
  if (!saved) return;
  const monitors = await availableMonitors();
  const geometry = resolveWindowGeometry(saved, monitors, await window.scaleFactor(), (await primaryMonitor()) ?? undefined);
  await window.setSize(new LogicalSize(geometry.width, geometry.height));
  if (monitors.length) await window.setPosition(new PhysicalPosition(geometry.x, geometry.y));
}

function TrendChart({ title, rows, value, format }: { title: string; rows: Trend[]; value: (row: Trend) => number; format: (value: number) => string }) {
  const trend = rows;
  const values = trend.filter(row => row.available).map(value);
  if (!values.length) return <div className="trend-card"><div className="trend-heading"><strong>{title}</strong></div><p className="empty">暂无已保留的趋势数据</p></div>;
  const width = 320;
  const height = 92;
  const pad = 9;
  const max = Math.max(...values, 1);
  const x = (index: number) => trend.length <= 1 ? width / 2 : pad + (index * (width - pad * 2)) / (trend.length - 1);
  const y = (item: number) => height - pad - (item / max) * (height - pad * 2);
  const segments: string[] = [];
  let segment: string[] = [];
  trend.forEach((row, index) => {
    if (!row.available) {
      if (segment.length) segments.push(segment.join(" "));
      segment = [];
      return;
    }
    const point = `${x(index)},${y(value(row))}`;
    segment.push(segment.length ? `L ${point}` : `M ${point}`);
  });
  if (segment.length) segments.push(segment.join(" "));
  const labels = [trend[0]?.date.slice(5), trend[Math.floor((trend.length - 1) / 2)]?.date.slice(5), trend[trend.length - 1]?.date.slice(5)];
  return <div className="trend-card">
    <div className="trend-heading"><strong>{title}</strong><span>峰值 {format(max)}</span></div>
    <svg className="trend-chart" viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${title}走势图`}>
      <line x1={pad} x2={width - pad} y1={height - pad} y2={height - pad} />
      <line x1={pad} x2={width - pad} y1={height / 2} y2={height / 2} />
      {segments.map((path, index) => <path key={index} d={path} />)}
      {trend.map((row, index) => row.available && <circle key={row.date} cx={x(index)} cy={y(value(row))} r="2.5" />)}
    </svg>
    <div className="trend-labels"><span>{labels[0]}</span><span>{labels[1]}</span><span>{labels[2]}</span></div>
  </div>;
}

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
  const [view, setView] = useState<WidgetView>(() => {
    try {
      const saved = localStorage.getItem("ledgergate-widget-view");
      return views.includes(saved as WidgetView) ? saved as WidgetView : "overview";
    }
    catch { return "overview"; }
  });
  const [autoRotate, setAutoRotate] = useState(() => {
    try { return localStorage.getItem("ledgergate-widget-auto-rotate") === "true"; }
    catch { return false; }
  });
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
    try { localStorage.setItem("ledgergate-widget-view", view); } catch { /* 系统禁用本地存储时仅保留当前选择。 */ }
  }, [view]);

  useEffect(() => {
    try { localStorage.setItem("ledgergate-widget-auto-rotate", String(autoRotate)); } catch { /* 系统禁用本地存储时仅保留当前选择。 */ }
  }, [autoRotate]);

  useEffect(() => {
    if (!autoRotate || !connection?.configured || editing) return;
    const timer = window.setInterval(() => {
      setView(current => views[(views.indexOf(current) + 1) % views.length]);
    }, 10_000);
    return () => window.clearInterval(timer);
  }, [autoRotate, connection?.configured, editing]);

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
    const appWindow = getCurrentWindow();
    let disposed = false;
    let timer: number | undefined;
    let unlistenMoved: (() => void) | undefined;
    let unlistenResized: (() => void) | undefined;
    const persist = async (position?: PhysicalPosition, size?: PhysicalSize) => {
      try {
        const [actualPosition, actualSize, scaleFactor] = await Promise.all([
          position ? Promise.resolve(position) : appWindow.outerPosition(),
          size ? Promise.resolve(size) : appWindow.innerSize(),
          appWindow.scaleFactor(),
        ]);
        if (disposed) return;
        const logicalSize = actualSize.toLogical(scaleFactor);
        localStorage.setItem(WINDOW_STATE_STORAGE_KEY, serializeWindowState({ x: actualPosition.x, y: actualPosition.y, width: logicalSize.width, height: logicalSize.height, scaleFactor }));
      } catch { /* 窗口关闭或系统禁用本地存储时保留当前窗口状态。 */ }
    };
    const queuePersist = (position?: PhysicalPosition, size?: PhysicalSize) => {
      if (timer !== undefined) globalThis.window.clearTimeout(timer);
      timer = globalThis.window.setTimeout(() => void persist(position, size), 150);
    };
    const setup = async () => {
      try { await restoreWindowState(appWindow); } catch { /* 显示器配置变化时使用系统默认位置。 */ }
      if (disposed) return;
      try {
        unlistenMoved = await appWindow.onMoved(({ payload }) => queuePersist(payload));
        unlistenResized = await appWindow.onResized(({ payload }) => queuePersist(undefined, payload));
        queuePersist();
      } catch { /* 浏览器预览没有 Tauri 窗口事件时仅跳过窗口状态监听。 */ }
    };
    void setup();
    return () => {
      disposed = true;
      if (timer !== undefined) globalThis.window.clearTimeout(timer);
      unlistenMoved?.();
      unlistenResized?.();
    };
  }, []);

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
  const averageTokens = period && period.requests > 0 ? Math.round((period.input_tokens + period.output_tokens) / period.requests) : null;
  const errorRate = period && period.requests > 0 ? `${((period.errors / period.requests) * 100).toLocaleString("zh-CN", { maximumFractionDigits: 1 })}%` : "—";
  const alerts: { tone: "danger" | "warning" | "info"; title: string; detail: string }[] = [];
  if (error) alerts.push({ tone: "danger", title: "连接异常", detail: error });
  const attentionCurrencies = groups.filter(group => group.attention).map(group => group.currency || "未设置单位");
  if (attentionCurrencies.length) alerts.push({ tone: "warning", title: "账户采集提醒", detail: `${attentionCurrencies.join("、")} 存在未采集、暂停或异常账户` });
  if (period?.errors) alerts.push({ tone: "warning", title: "请求错误", detail: `当前周期有 ${count(period.errors)} 次请求返回错误，错误率 ${errorRate}` });
  if (period?.cost_unknown_requests) alerts.push({ tone: "warning", title: "费用未完整核算", detail: `${count(period.cost_unknown_requests)} 次请求缺少有效费用或用量信息` });
  if (period && period.retention_days < days) alerts.push({ tone: "info", title: "统计周期不完整", detail: `当前只保留 ${period.retention_days} 天汇总，无法覆盖完整的近 ${days} 天` });
  if (legacy) alerts.push({ tone: "info", title: "服务版本较旧", detail: "升级 LedgerGate 后可获取趋势、分析和排行数据" });
  return <main className="widget" data-theme={theme}>
    <header className="titlebar" data-tauri-drag-region>
      <div className="brand" data-tauri-drag-region><img src={logo} alt="" draggable={false} data-tauri-drag-region /><span data-tauri-drag-region>LedgerGate</span></div>
      <div className="actions">
        <button type="button" aria-label="刷新数据" title="刷新数据" disabled={refreshing || !connection?.configured || editing} onClick={() => void refresh()}>{refreshing ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button>
        <button type="button" className={autoRotate ? "active" : ""} aria-label={autoRotate ? "暂停自动轮播" : "开启自动轮播"} title={autoRotate ? "暂停自动轮播" : "开启自动轮播"} onClick={() => setAutoRotate(value => !value)}>{autoRotate ? <Pause size={16} /> : <Play size={16} />}</button>
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
        <div className="view-tabs" role="tablist" aria-label="显示内容">{views.map(item => <button key={item} type="button" role="tab" className={view === item ? "selected" : ""} aria-selected={view === item} onClick={() => setView(item)}>{viewLabels[item]}</button>)}</div>
        {view !== "accounts" && <div className="period-bar"><div><span className="eyebrow">{view === "ranking" ? "排行统计" : view === "analysis" ? "用量分析" : "用量概览"}</span><small className="period-date">{overview ? `${overview.date} · 北京时间` : "北京时间"}</small></div><div className="period-tabs" role="group" aria-label="统计周期">{([[1, "今日"], [7, "近 7 天"], [30, "近 30 天"]] as const).map(([value, label]) => <button key={value} type="button" className={days === value ? "selected" : ""} aria-pressed={days === value} disabled={legacy && value !== 1} onClick={() => setDays(value)}>{label}</button>)}</div></div>}
        {view === "overview" && <>
          <div className="hero"><span>Token 总量</span><strong>{input != null && output != null ? count(input + output) : "—"}</strong><small>{input != null && output != null ? `输入 ${count(input)}  ·  输出 ${count(output)}` : "等待读取用量"}</small></div>
          <div className="quick-stats"><div><span>请求</span><strong>{requests != null ? count(requests) : "—"}</strong></div><div><span>错误</span><strong>{period ? count(period.errors) : "—"}</strong></div><div><span>费用估算</span><strong>{estimatedCost}</strong></div></div>
          {period && <p className="detail-line">缓存读取 {count(period.cache_read_tokens)} · 写入 {count(period.cache_write_tokens)}{period.cost_unknown_requests > 0 && ` · ${count(period.cost_unknown_requests)} 次费用未知`}</p>}
          {period && period.retention_days < days && <p className="notice">请求汇总保留 {period.retention_days} 天，当前周期可能不完整。</p>}
          {legacy && <p className="notice">当前服务仅支持今日概览，升级 LedgerGate 后可查看周期汇总和排行。</p>}
        </>}
        {view === "analysis" && <>
          {period ? <div className="analysis-grid">
            <div><span>输入 Token</span><strong>{count(period.input_tokens)}</strong></div>
            <div><span>输出 Token</span><strong>{count(period.output_tokens)}</strong></div>
            <div><span>缓存读取</span><strong>{count(period.cache_read_tokens)}</strong></div>
            <div><span>缓存写入</span><strong>{count(period.cache_write_tokens)}</strong></div>
            <div><span>平均 Token / 请求</span><strong>{averageTokens != null ? count(averageTokens) : "—"}</strong></div>
            <div><span>错误率</span><strong>{errorRate}</strong></div>
            <div><span>费用未知</span><strong>{count(period.cost_unknown_requests)} 次</strong></div>
            <div><span>费用估算</span><strong>{estimatedCost}</strong></div>
          </div> : <p className="empty">{legacy ? "升级服务后可查看用量分析" : "等待读取统计"}</p>}
          {period && <div className="trend-grid">
            <TrendChart title="请求趋势" rows={period.trend ?? []} value={row => row.requests} format={count} />
            <TrendChart title="Token 趋势" rows={period.trend ?? []} value={row => row.input_tokens + row.output_tokens} format={count} />
          </div>}
          {period && period.retention_days < days && <p className="notice">请求汇总保留 {period.retention_days} 天，当前周期可能不完整。</p>}
          {legacy && <p className="notice">当前服务仅支持今日概览，升级 LedgerGate 后可查看周期分析。</p>}
        </>}
        {view === "alerts" && <section className="alert-view" aria-label="告警">
          <div className="section-title"><h2>运行告警</h2><span>{alerts.length ? `${alerts.length} 项提醒` : "状态正常"}</span></div>
          {alerts.length ? <div className="alert-list">{alerts.map((alert, index) => <article className={`alert-card ${alert.tone}`} key={`${alert.title}-${index}`}><strong>{alert.title}</strong><p>{alert.detail}</p></article>)}</div> : <p className="empty">暂无告警，当前采集和统计状态正常。</p>}
          <p className="detail-line">告警来自当前小部件已读取的数据，不会触发额外采集。</p>
        </section>}
        {view === "accounts" && <section className="section finance account-view" aria-label="账户余额及今日实际消费">
          <div className="section-title"><h2>账户资产</h2><span>上游采集 · 今日消费</span></div>
          <div className="finance-head"><span>余额</span><span>今日实际消费</span></div>
          {groups.length ? groups.map(group => <div className="finance-row" key={group.currency}>
            <div><strong>{amount(group.balance, group.currency)}</strong>{group.balanceCount < group.accounts && <small>已采集 {group.balanceCount}/{group.accounts} 个账户</small>}</div>
            <div><strong>{amount(group.today, group.currency)}</strong>{group.todayCount < group.accounts && <small>已采集 {group.todayCount}/{group.accounts} 个账户</small>}</div>
            {group.attention && <p>部分账户未采集、已暂停或有提醒，数值可能沿用上次结果</p>}
            {group.oldestCheck != null && <p>最早检查 {time(group.oldestCheck)}</p>}
          </div>) : <div className="empty">{overview ? "暂无上游账户" : "等待账户数据"}</div>}
        </section>}
        {view === "ranking" && <>
          {period && period.retention_days < days && <p className="notice">请求汇总保留 {period.retention_days} 天，当前周期可能不完整。</p>}
          {legacy && <p className="notice">当前服务仅支持今日概览，升级 LedgerGate 后可查看周期汇总和排行。</p>}
          {([ ["模型排行", period?.models], ["渠道排行", period?.channels] ] as const).map(([title, items]) => <section className="section ranking" key={title}>
            <div className="section-title"><h2>{title}</h2><span>前 5 · 按 Token</span></div>
            {items?.length ? <ol>{items.map((item, index) => <li key={`${item.name}-${index}`}><span className="rank-index">{index + 1}</span><span className="rank-name" title={item.name}>{item.name}</span><span className="rank-value" title={`${count(item.tokens)} Token · ${count(item.requests)} 次请求`}>{compact(item.tokens)} <small>· {count(item.requests)} 次</small></span></li>)}</ol> : <p className="empty">{legacy ? "升级服务后可查看" : period ? "暂无统计数据" : "等待读取统计"}</p>}
          </section>)}
        </>}
      </section>
      <footer className="bottom"><span className={error ? "error" : "hint"} role="status"><i className={`status-dot${error ? " failed" : lastSuccess ? "" : " pending"}`} />{error ? `${lastSuccess ? `数据截至 ${time(lastSuccess)} · ` : ""}${error}` : lastSuccess ? `更新于 ${time(lastSuccess)}` : "等待连接"}</span><button type="button" className="open" onClick={() => void invoke("open_dashboard").catch(reason => setError(String(reason)))}>打开控制台 <ArrowUpRight size={14} /></button></footer>
    </>}
  </main>;
}
