import { useCallback, useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { ArrowUpRight, Check, LoaderCircle, Palette, Pin, Minimize2, RefreshCw, Settings, X } from "lucide-react";
import logo from "../src-tauri/icons/icon.png";
import { finances, percentage, periodMetrics, type Overview, type Trend } from "./summary";
import { widgetAlerts } from "./alerts";
import { dashboardPages, type DashboardPage } from "./dashboard";
import useWidgetDisplay from "./useWidgetDisplay";
import WidgetBadge from "./WidgetBadge";
import WidgetSettings from "./WidgetSettings";
import AccountAssets from "./AccountAssets";
import UsageInsights from "./UsageInsights";
import OverviewSection from "./OverviewSection";
import { parseReadingPrefs, READING_KEY, type SectionId } from "./reading";

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
const viewLabels: Record<WidgetView, string> = { overview: "总览", accounts: "账户", analysis: "分析", alerts: "告警", ranking: "排行" };
const count = (value: number) => value.toLocaleString("zh-CN");
const compact = (value: number) => value.toLocaleString("zh-CN", { notation: "compact", maximumFractionDigits: 1 });
const time = (value: number) => new Date(value).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });

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
  const openDashboard = useCallback((page: DashboardPage) => {
    void invoke("open_dashboard", {page}).catch(reason => setError(String(reason)));
  }, []);
  const [refreshing, setRefreshing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const closeSettings = useCallback(() => setSettingsOpen(false), []);
  const display = useWidgetDisplay(connection?.configured ?? false, editing || settingsOpen);
  const [reading, setReading] = useState(() => {
    try { return parseReadingPrefs(localStorage.getItem(READING_KEY)); } catch { return parseReadingPrefs(null); }
  });
  const [version, setVersion] = useState("");
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    let active = true;
    void invoke<string>("widget_version").then(value => { if (active) setVersion(value); }).catch(() => {});
    return () => { active = false; };
  }, []);
  useEffect(() => {
    if (!display.shown || display.mode !== "panel") return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => window.clearInterval(timer);
  }, [display.shown, display.mode]);
  useEffect(() => {
    try { localStorage.setItem(READING_KEY, JSON.stringify(reading)); } catch { /* 禁用存储时保留当前阅读设置。 */ }
  }, [reading]);
  const collapseSection = useCallback((id: SectionId, collapsed: boolean) => {
    setReading(current => current.collapsed.includes(id) === collapsed ? current : { ...current, collapsed: collapsed ? [...current.collapsed, id] : current.collapsed.filter(value => value !== id) });
  }, []);
  const widgetRoot = useRef<HTMLElement>(null);
  const contentBody = useRef<HTMLDivElement>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [days, setDays] = useState<1 | 7 | 30>(1);
  const [view, setView] = useState<WidgetView>(() => {
    try {
      const saved = localStorage.getItem("ledgergate-widget-view");
      return views.includes(saved as WidgetView) ? saved as WidgetView : "overview";
    }
    catch { return "overview"; }
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

  useEffect(() => { display.setInteracting(settingsOpen || themeMenuOpen); }, [settingsOpen, themeMenuOpen, display.setInteracting]);

  useEffect(() => {
    try { localStorage.setItem("ledgergate-widget-theme", theme); } catch { /* 系统禁用本地存储时仅保留当前选择。 */ }
  }, [theme]);

  useEffect(() => {
    try { localStorage.setItem("ledgergate-widget-view", view); } catch { /* 系统禁用本地存储时仅保留当前选择。 */ }
  }, [view]);

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
    const root = widgetRoot.current;
    const body = contentBody.current;
    if (!display.ready || display.busy || display.mode !== "panel" || !display.shown || !root || !body || editing || !connection?.configured) return;
    const content = body.parentElement!;
    content.scrollTop = 0;
    let disposed = false;
    let revision = 0;
    let timer: number | undefined;
    const fit = (current: number) => {
      if (disposed || current !== revision) return;
      const style = getComputedStyle(content);
      const height = body.getBoundingClientRect().height + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + root.getBoundingClientRect().height - content.getBoundingClientRect().height;
      void display.fitHeight(height);
    };
    const observer = new ResizeObserver(() => {
      const current = ++revision;
      if (timer !== undefined) window.clearTimeout(timer);
      timer = window.setTimeout(() => void fit(current), 80);
    });
    observer.observe(body);
    root.querySelectorAll(".titlebar, .bottom").forEach(element => observer.observe(element));
    return () => { disposed = true; observer.disconnect(); if (timer !== undefined) window.clearTimeout(timer); };
  }, [display.ready, display.busy, display.mode, display.shown, display.fitHeight, view, settingsOpen, editing, connection?.configured]);

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
    if (!connection?.configured || !display.shown) return;
    void refresh();
    const timer = window.setInterval(() => void refresh(), 30_000);
    return () => { window.clearInterval(timer); generation.current++; };
  }, [connection?.configured, display.shown, refresh, refreshKey]);

  useEffect(() => {
    let disposed = false;
    let stop: (() => void) | undefined;
    void getCurrentWindow().listen("widget-resumed", () => {
      if (connection?.configured && display.shown) void refresh();
    }).then(value => { if (disposed) value(); else stop = value; }).catch(reason => setError(String(reason)));
    return () => { disposed = true; stop?.(); };
  }, [connection?.configured, display.shown, refresh]);

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
  const metrics = period ? periodMetrics(period) : null;
  const input = period?.input_tokens ?? (legacy && days === 1 ? overview?.input_tokens : null);
  const output = period?.output_tokens ?? (legacy && days === 1 ? overview?.output_tokens : null);
  const requests = period?.requests ?? (legacy && days === 1 ? overview?.requests : null);
  const estimatedCost = period && !(period.requests > 0 && period.cost_unknown_requests >= period.requests)
    ? `$${(period.cost_micros / 1_000_000).toLocaleString("en-US", { minimumFractionDigits: 4, maximumFractionDigits: 6 })}` : "—";
  const averageTokens = period && period.requests > 0 ? Math.round((period.input_tokens + period.output_tokens) / period.requests) : null;
  const errorRate = period && period.requests > 0 ? `${((period.errors / period.requests) * 100).toLocaleString("zh-CN", { maximumFractionDigits: 1 })}%` : "—";
  const {issues, notes, snapshotStale} = widgetAlerts(overview, days, error || display.error, lastSuccess, now);
  const attentionCount = issues.length;
  if (display.mode === "badge" && connection?.configured) return <WidgetBadge overview={overview} lastSuccess={lastSuccess} error={error || display.error} prefs={display.prefs} theme={theme} busy={display.busy}
    onPrefs={display.updatePrefs} onExpand={display.expand} onHide={display.hide} onReset={display.reset}
    onSettings={() => { void display.expand().then(() => setSettingsOpen(true)); }} />;
  return <main className="widget" data-theme={theme} data-density={reading.density} data-text-size={reading.textSize} ref={widgetRoot} onPointerEnter={() => display.setInside(true)} onPointerLeave={() => display.setInside(false)}>
    <header className="titlebar" data-tauri-drag-region>
      <div className="brand" data-tauri-drag-region><img src={logo} alt="" draggable={false} data-tauri-drag-region /><span data-tauri-drag-region>LedgerGate</span></div>
      <div className="actions">
        <button type="button" aria-label="收起为数据浮标" title="收起为数据浮标" disabled={display.busy || !display.ready || !connection?.configured || editing || settingsOpen} onClick={() => void display.collapse()}><Minimize2 size={15} /></button>
        <button type="button" aria-label="刷新数据" title="刷新数据" disabled={refreshing || !connection?.configured || editing} onClick={() => void refresh()}>{refreshing ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button>
        <div className="theme-control" ref={themeControl}>
          <button ref={themeButton} type="button" aria-label="颜色风格" title="颜色风格" aria-expanded={themeMenuOpen} aria-controls="widget-theme-menu" onClick={() => setThemeMenuOpen(value => !value)}><Palette size={16} /></button>
          {themeMenuOpen && <div className="theme-menu" id="widget-theme-menu" role="group" aria-label="颜色风格">
            <p>颜色风格</p>
            {themes.map(item => <button type="button" className="theme-option" key={item.id} aria-pressed={theme === item.id} onClick={() => { setTheme(item.id); setThemeMenuOpen(false); themeButton.current?.focus(); }}><span className={`theme-swatch ${item.id}`} aria-hidden="true" />{item.label}{theme === item.id && <Check size={14} aria-hidden="true" />}</button>)}
          </div>}
        </div>
        <button type="button" className={topmost ? "active" : ""} aria-label={topmost ? "取消置顶" : "置顶窗口"} title={topmost ? "取消置顶" : "置顶窗口"} onClick={() => setTopmost(value => !value)}><Pin size={16} /></button>
        <button type="button" aria-label="小部件设置" title="小部件设置" aria-expanded={settingsOpen} onClick={() => { setSettingsOpen(value => !value); setThemeMenuOpen(false); }}><Settings size={16} /></button>
        <button type="button" aria-label="隐藏到托盘" title="隐藏到托盘" onClick={() => void display.hide()}><X size={16} /></button>
      </div>
    </header>
    {(editing || !connection?.configured) && !settingsOpen ? <form className="settings" onSubmit={event => void save(event)}>
      <div><h1>连接 LedgerGate</h1><p>在 LedgerGate 的系统设置中生成小部件只读凭证。</p></div>
      <label>服务地址<input type="url" required maxLength={2048} placeholder="http://192.168.1.10:8080" value={server} disabled={saving} onChange={event => setServer(event.target.value)} /></label>
      <label>只读凭证<input type="password" autoComplete="off" placeholder={connection?.configured ? "留空保留已有凭证" : "粘贴 lgw_ 开头的凭证"} value={token} disabled={saving} onChange={event => setToken(event.target.value)} /></label>
      <p className="hint">凭证加密保存在当前 Windows 用户目录；更换服务地址时需重新填写。远程连接建议使用 HTTPS。</p>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="settings-actions">{connection?.configured && <button type="button" className="secondary" disabled={saving} onClick={() => { setEditing(false); setError(""); setToken(""); }}>取消</button>}<button type="submit" className="primary" disabled={saving || !server.trim()}>{saving ? "正在保存…" : "保存并连接"}</button></div>
    </form> : <>
      <section className="content" aria-label="运行概览">
        <div className="content-body" ref={contentBody}>
        {settingsOpen ? <WidgetSettings version={version} reading={reading} onReading={setReading} prefs={display.prefs} currencies={groups.map(group => group.currency)} onPrefs={display.updatePrefs} onReset={display.reset} onClose={closeSettings} onConnection={() => { setSettingsOpen(false); setEditing(true); setError(""); setToken(""); }} /> : <>
        <div className="view-tabs" role="tablist" aria-label="显示内容">{views.map(item => <button key={item} type="button" role="tab" className={view === item ? "selected" : ""} aria-selected={view === item} onClick={() => setView(item)}>{viewLabels[item]}{item === "alerts" && attentionCount > 0 && <span className="alert-count" aria-label={`${attentionCount} 项提醒`}>{attentionCount}</span>}</button>)}</div>
        {(view === "overview" || view === "accounts") && <OverviewSection id="assets" title="账户资产" meta={"上游采集 · 今日消费"} collapsible={view === "overview"} collapsed={reading.collapsed.includes("assets")} onCollapse={collapseSection}>
          <AccountAssets overview={overview} now={now} />
          <button type="button" className="section-open" onClick={()=>openDashboard("accounts")}>在控制台查看账户 →</button>
        </OverviewSection>}
        {view !== "accounts" && <div className="period-bar"><div><span className="eyebrow">{view === "ranking" ? "排行统计" : view === "analysis" ? "用量分析" : "统计周期"}</span><small className="period-date">{overview ? `${overview.date} · 北京时间` : "北京时间"}</small></div><div className="period-tabs" role="group" aria-label="统计周期">{([[1, "今日"], [7, "近 7 天"], [30, "近 30 天"]] as const).map(([value, label]) => <button key={value} type="button" className={days === value ? "selected" : ""} aria-pressed={days === value} disabled={legacy && value !== 1} onClick={() => setDays(value)}>{label}</button>)}</div></div>}
        {view === "overview" && <OverviewSection id="usage" title="用量概览" meta={days === 1 ? "今日" : `近 ${days} 天`} collapsible={view === "overview"} collapsed={reading.collapsed.includes("usage")} onCollapse={collapseSection}>
          <div className="hero"><span>Token 总量</span><strong>{input != null && output != null ? count(input + output) : "—"}</strong><small>{input != null && output != null ? `输入 ${count(input)}  ·  输出 ${count(output)}` : "等待读取用量"}</small></div>
          <div className="quick-stats"><div><span>请求</span><strong>{requests != null ? count(requests) : "—"}</strong></div><div><span>错误</span><strong>{period ? count(period.errors) : "—"}</strong></div><div><span>费用估算</span><strong>{estimatedCost}</strong></div></div>
          {metrics && <div className="quick-stats"><div title="仅代表有效缓存样本；覆盖率见下方分析区。"><span>缓存命中率</span><strong>{percentage(metrics.cacheHitRate)}</strong></div><div title="仅代表有尝试统计的请求；覆盖率见下方分析区。"><span>首次成功率</span><strong>{percentage(metrics.firstSuccessRate)}</strong></div><div><span>费用覆盖率</span><strong>{percentage(metrics.costCoverage)}</strong></div></div>}
          {period && <p className="detail-line">缓存读取 {count(period.cache_read_tokens)} · 写入 {count(period.cache_write_tokens)}{period.cost_unknown_requests > 0 && ` · ${count(period.cost_unknown_requests)} 次费用未知`}</p>}
          {period && period.retention_days < days && <p className="notice">请求汇总保留 {period.retention_days} 天，当前周期可能不完整。</p>}
          {legacy && <p className="notice">当前服务仅支持今日概览，升级 LedgerGate 后可查看周期汇总和排行。</p>}
        </OverviewSection>}
        {(view === "overview" || view === "alerts") && <OverviewSection id="alerts" title="运行告警" meta={issues.length ? `${issues.length} 项需处理` : "暂无当前异常"} collapsible={view === "overview"} collapsed={reading.collapsed.includes("alerts")} onCollapse={collapseSection}>
          {issues.length ? <div className="alert-list">{issues.map((alert, index) => <article className={`alert-card ${alert.tone}`} key={`${alert.title}-${index}`}><strong>{alert.title}</strong><p>{alert.detail}</p></article>)}</div> : <p className="empty">当前没有需要处理的连接、账户或数据时效异常。</p>}
          {notes.length > 0 && <div className="period-notes"><h3>周期统计与说明</h3>{notes.map(note=><article className="period-note" key={note.title}><strong>{note.title}</strong><p>{note.detail}</p></article>)}</div>}
          <p className="detail-line">仅当前异常计入关注数量；周期统计保留在此，不会触发额外采集。</p>
          <button type="button" className="section-open" onClick={()=>openDashboard("monitor/alerts")}>在控制台查看监控记录 →</button>
        </OverviewSection>}
        {(view === "overview" || view === "ranking") && <OverviewSection id="ranking" title="模型与渠道排行" meta={"前 5 · 按 Token"} collapsible={view === "overview"} collapsed={reading.collapsed.includes("ranking")} onCollapse={collapseSection}>
          {([ ["模型排行", period?.models], ["渠道排行", period?.channels] ] as const).map(([title, items]) => <section className="section ranking" key={title}>
            <div className="section-title"><h2>{title}</h2><span>前 5 · 按 Token</span></div>
            {items?.length ? <ol>{items.map((item, index) => <li key={`${item.name}-${index}`}><span className="rank-index">{index + 1}</span><span className="rank-name" title={item.name}>{item.name}</span><span className="rank-value" title={`${count(item.tokens)} Token · ${count(item.requests)} 次请求`}>{compact(item.tokens)} <small>· {count(item.requests)} 次</small></span></li>)}</ol> : <p className="empty">{legacy ? "升级服务后可查看" : period ? "暂无统计数据" : "等待读取统计"}</p>}
          </section>)}
          <button type="button" className="section-open" onClick={()=>openDashboard("analytics/summary")}>在控制台查看完整排行 →</button>
        </OverviewSection>}
        {(view === "overview" || view === "analysis") && <OverviewSection id="analysis" title="用量分析与趋势" meta={"已保留的周期统计"} collapsible={view === "overview"} collapsed={reading.collapsed.includes("analysis")} onCollapse={collapseSection}>
          {period ? <div className="analysis-grid">
            <div><span>输入 Token</span><strong>{count(period.input_tokens)}</strong></div>
            <div><span>输出 Token</span><strong>{count(period.output_tokens)}</strong></div>
            <div><span>平均 Token / 请求</span><strong>{averageTokens != null ? count(averageTokens) : "—"}</strong></div>
            <div><span>错误率</span><strong>{errorRate}</strong></div>
            <div><span>费用未知</span><strong>{count(period.cost_unknown_requests)} 次</strong></div>
            <div><span>费用估算</span><strong>{estimatedCost}</strong></div>
          </div> : <p className="empty">{legacy ? "升级服务后可查看用量分析" : "等待读取统计"}</p>}
          {period && <UsageInsights period={period} collapsed={reading.collapsed} onCollapse={collapseSection} openDashboard={openDashboard} />}
          {period && <div className="trend-grid">
            <TrendChart title="请求趋势" rows={period.trend ?? []} value={row => row.requests} format={count} />
            <TrendChart title="Token 趋势" rows={period.trend ?? []} value={row => row.input_tokens + row.output_tokens} format={count} />
          </div>}
        </OverviewSection>}
        {view !== "overview" && period && period.retention_days < days && <p className="notice">请求汇总保留 {period.retention_days} 天，当前周期可能不完整。</p>}
        {view !== "overview" && legacy && <p className="notice">当前服务仅支持今日概览，升级服务后可查看周期统计。</p>}
        </>}
        </div>
      </section>
      <footer className="bottom"><span className={error || display.error || snapshotStale ? "error" : "hint"} role="status"><i className={`status-dot${error || display.error || snapshotStale ? " failed" : lastSuccess ? "" : " pending"}`} />{error ? `${lastSuccess ? `数据截至 ${time(lastSuccess)} · ` : ""}${error}` : display.error || (lastSuccess ? `${snapshotStale ? "旧快照 · " : ""}获取于 ${time(lastSuccess)}` : "等待连接")}</span><button type="button" className="open" onClick={() => openDashboard(dashboardPages[view])}>打开控制台 <ArrowUpRight size={14} /></button></footer>
    </>}
  </main>;
}
