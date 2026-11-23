import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { ArrowDownLeft, ArrowUpDown, ArrowUpRight, Bell, CalendarClock, CalendarDays, ChevronLeft, ChevronRight, CircleAlert, CircleCheck, CirclePause, Inbox, LoaderCircle, Megaphone, Search, Wallet } from "lucide-react";
import { api, type AccountRow, type ActivityPage, type Alert, type AlertPage, type Announcement, type AnnouncementPage, type Data } from "./api";
import "./monitor-activity.css";

type DialogComponent = ComponentType<{ title: string; children: ReactNode; close: () => void }>;
const kinds: Record<string, string> = { monitor_error: "账户采集异常", recovered: "账户采集恢复", monitor_warning: "部分采集异常", monitor_warning_recovered: "扩展采集恢复", low_balance: "余额偏低", rates_changed: "倍率变动", announcement: "公告更新", channel_auto_disabled: "渠道自动停用", subscription_expiring: "订阅到期", subscription_daily: "订阅日余量", subscription_weekly: "订阅周余量", subscription_monthly: "订阅月余量" };
const eventIcons: Record<string, typeof Bell> = {
  monitor_error: CircleAlert, monitor_warning: CircleAlert, recovered: CircleCheck, monitor_warning_recovered: CircleCheck,
  low_balance: Wallet, rates_changed: ArrowUpDown, announcement: Megaphone, channel_auto_disabled: CirclePause,
  subscription_expiring: CalendarClock, subscription_daily: CalendarDays, subscription_weekly: CalendarDays, subscription_monthly: CalendarDays,
};
const deliveryNames: Record<string, string> = { local: "本地记录", pending: "等待发送", sent: "已送达", failed: "发送失败", cooldown: "冷却期仅记录", partial: "部分送达", cancelled: "已取消" };
const date = (value: string | null) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "时间未提供";
const rate = (value: number | null) => value == null ? "未提供" : `${value.toLocaleString("zh-CN", { maximumFractionDigits: 10 })}×`;

function announcementText(content: string) {
  if (!/<[a-z][\s\S]*>/i.test(content)) return content;
  const template = document.createElement("template");
  template.innerHTML = content.replace(/<br\s*\/?>|<\/(?:p|div|li|h[1-6]|tr)>/gi, "$&\n");
  template.content.querySelectorAll("script, style, iframe, object").forEach(node => node.remove());
  return template.content.textContent?.trim() || "";
}

function Empty({ children, icon = <Inbox size={25} aria-hidden="true" /> }: { children: ReactNode; icon?: ReactNode }) {
  return <div className="empty monitor-empty"><div className="empty-symbol">{icon}</div>{children}</div>;
}

function RateChanges({ row }: { row: Alert }) {
  if (!row.rate_changes?.length) return <><p className="monitor-message">{row.detail}</p><p className="field-hint">旧记录未保存变动前后数值，后续变动会显示明细。</p></>;
  return <div className="rate-changes">{row.rate_changes.map(change => {
    const tone = change.kind === "changed" && change.before != null && change.after != null ? (change.after < change.before ? "decreased" : "increased") : "";
    return <div className={`rate-change ${tone}`} key={change.group_id}>
      <div><strong>{change.group || change.group_id}</strong><p>{change.kind === "added" ? <>新增分组 · {rate(change.after)}</> : change.kind === "removed" ? <>分组已移除 · 原倍率 {rate(change.before)}</> : <>{rate(change.before)} <span aria-hidden="true">→</span> <b>{rate(change.after)}</b></>}</p></div>
      {change.percent != null && <span className="rate-change-percent">{change.percent < 0 ? <ArrowDownLeft size={14} /> : <ArrowUpRight size={14} />}{change.percent > 0 ? "+" : ""}{change.percent.toFixed(1)}%</span>}
    </div>;
  })}</div>;
}

function AlertList({ rows, rates = false }: { rows: Alert[]; rates?: boolean }) {
  if (!rows.length) return <Empty icon={rates ? <ArrowUpDown size={25} aria-hidden="true" /> : <Bell size={25} aria-hidden="true" />}>{rates ? "暂无倍率变动。首次采集建立基线，后续真实变动会记录在这里。" : "暂无告警。余额、采集、倍率、公告及订阅事件会记录在这里。"}</Empty>;
  return <div className="monitor-list">{rows.map(row => {
    const Icon = eventIcons[row.kind] || Bell;
    return <article className="monitor-entry" key={row.id}>
    <span className={`monitor-symbol ${row.kind.includes("recovered") ? "good" : ""}`}><Icon size={16} aria-hidden="true" /></span>
    <div className="monitor-entry-body"><h3>{row.title}</h3>
      {row.kind === "rates_changed" ? <RateChanges row={row} /> : <p className="monitor-message">{row.detail}</p>}
      <div className="monitor-meta"><time dateTime={row.created_at}>{date(row.created_at)}</time><span>{kinds[row.kind] || row.kind}</span><span className={`badge ${row.delivery}`}>{deliveryNames[row.delivery] || row.delivery}</span></div>
      {row.delivery_error && <p className="card-error">{row.delivery_error} · 已尝试 {row.attempts} 次</p>}
      {!!row.deliveries?.length && <details className="notification-delivery-details"><summary>通知投递 · {row.deliveries.length} 个渠道</summary>{row.deliveries.map(delivery => <div className="notification-delivery-line" key={delivery.id}><strong>{delivery.channel_name}</strong><span className={`badge ${delivery.status}`}>{deliveryNames[delivery.status] || delivery.status}</span><span>已尝试 {delivery.attempts} 次</span>{delivery.error && <span className="card-error">{delivery.error}</span>}</div>)}</details>}
    </div>
  </article>;
  })}</div>;
}

export function AnnouncementList({ rows, dialog: Dialog }: { rows: Announcement[]; dialog: DialogComponent }) {
  const [selected, setSelected] = useState<Announcement | null>(null);
  return <>
    {!rows.length ? <Empty icon={<Megaphone size={25} aria-hidden="true" />}>暂无上游公告。这里只显示上游已返回的公告，请检查账户采集状态。</Empty> : <div className="monitor-list">{rows.map(row => <article className="monitor-entry" key={row.id}>
      <span className="monitor-symbol"><Megaphone size={16} aria-hidden="true" /></span>
      <div className="monitor-entry-body"><button className="announcement-title" onClick={() => setSelected(row)}>{row.title}</button><p className="announcement-preview">{announcementText(row.content)}</p>
        <div className="monitor-meta"><span>{row.account_name} · {row.kind === "newapi" ? "NewAPI" : "Sub2API"}</span><span>{row.published_at ? date(row.published_at) : "发布时间未提供"}</span>{row.mode && <span className="badge">{row.mode}</span>}{row.stale && <span className="badge low">上次可用数据</span>}</div>
        <button className="text-button" onClick={() => setSelected(row)} aria-label={`查看公告 ${row.title}`}>查看全文<ChevronRight size={13} /></button>
      </div>
    </article>)}</div>}
    {selected && <Dialog title={selected.title} close={() => setSelected(null)}><div className="announcement-detail">
      <p className="field-hint">{selected.account_name} · {selected.kind === "newapi" ? "NewAPI" : "Sub2API"}</p>
      <p className="field-hint">发布：{date(selected.published_at)}{selected.updated_at ? ` · 更新：${date(selected.updated_at)}` : ""}</p>
      <p className="field-hint">账户最近采集：{date(selected.collected_at)}{selected.stale ? " · 当前为上次可用数据" : ""}</p>
      <div className="announcement-content">{announcementText(selected.content) || "上游未提供正文。"}</div>
    </div></Dialog>}
  </>;
}

export function MonitoringPanels({ data, navigate, dialog }: { data: Data; navigate: (page: string) => void; dialog: DialogComponent }) {
  return <div className="monitoring-panels">
    <section className="panel monitor-rate-panel"><div className="panel-heading"><h2 className="icon-heading"><ArrowUpDown size={18} aria-hidden="true" />最近倍率变动</h2><div className="inline-actions"><span>共 {data.rateChanges.total} 次</span><button className="text-button" onClick={() => navigate("rate-changes")}>查看更多<ChevronRight size={14} /></button></div></div><div className="monitor-preview-scroll"><AlertList rows={data.rateChanges.items} rates /></div></section>
    <section className="panel"><div className="panel-heading"><h2 className="icon-heading"><Bell size={18} aria-hidden="true" />告警动态</h2><div className="inline-actions"><span>共 {data.alerts.total} 条</span><button className="text-button" onClick={() => navigate("alerts")}>查看更多<ChevronRight size={14} /></button></div></div><div className="monitor-preview-scroll"><AlertList rows={data.alerts.items} /></div></section>
    <section className="panel"><div className="panel-heading"><h2 className="icon-heading"><Megaphone size={18} aria-hidden="true" />上游公告</h2><div className="inline-actions"><span>共 {data.announcements.total} 条</span><button className="text-button" onClick={() => navigate("announcements")}>查看更多<ChevronRight size={14} /></button></div></div>{data.announcements.warnings.map(warning => <p className="monitor-warning" key={warning}>{warning}</p>)}<div className="monitor-preview-scroll"><AnnouncementList rows={data.announcements.items} dialog={dialog} /></div></section>
  </div>;
}

export function MonitorHistory({ mode, accounts, revision, dialog }: { mode: "alerts" | "rate-changes" | "announcements"; accounts: AccountRow[]; revision: Data; dialog: DialogComponent }) {
  const [page, setPage] = useState(1), [size, setSize] = useState(50), [query, setQuery] = useState(""), [draft, setDraft] = useState(""), [account, setAccount] = useState(""), [kind, setKind] = useState("");
  const [value, setValue] = useState<AlertPage | AnnouncementPage | null>(null), [error, setError] = useState(""), [loading, setLoading] = useState(false);
  const announcements = mode === "announcements", rates = mode === "rate-changes";
  const title = announcements ? "上游公告" : rates ? "倍率变动记录" : "告警动态";
  useEffect(() => { const timer = setTimeout(() => { setQuery(draft.trim()); setPage(1); }, 250); return () => clearTimeout(timer); }, [draft]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    const params = new URLSearchParams({ page: String(page), page_size: String(size), q: query });
    if (account) params.set("account_id", account);
    if (rates || kind) params.set("kind", rates ? "rates_changed" : kind);
    api<AlertPage | AnnouncementPage>(`/${announcements ? "announcements" : "alerts"}?${params}`, "GET", undefined, controller.signal)
      .then(next => { if (!controller.signal.aborted) { setValue(next); setError(""); } })
      .catch(error => { if (!controller.signal.aborted) setError(error.message); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [page, size, query, account, kind, mode, revision]);
  const pages = Math.max(1, Math.ceil((value?.total || 0) / size));
  const Icon = announcements ? Megaphone : rates ? ArrowUpDown : Bell;
  return <>
    <div className="list-toolbar monitor-toolbar">
      <div className="search"><Search size={16} /><input aria-label={`搜索${title}`} placeholder={announcements ? "搜索账户、公告标题或正文" : "搜索账户、标题、分组或详情"} value={draft} onChange={event => setDraft(event.target.value)} /></div>
      <select aria-label="筛选上游账户" value={account} onChange={event => { setAccount(event.target.value); setPage(1); }}><option value="">全部上游账户</option>{accounts.map(row => <option key={row.account.id} value={row.account.id}>{row.account.name}</option>)}</select>
      {mode === "alerts" && <select aria-label="筛选告警类型" value={kind} onChange={event => { setKind(event.target.value); setPage(1); }}><option value="">全部告警类型</option>{Object.entries(kinds).map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select>}
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="panel"><div className="panel-heading"><h2 className="icon-heading"><Icon size={18} aria-hidden="true" />{title}</h2><span>共 {value?.total || 0} {rates ? "次" : "条"}</span></div>
      {loading && !value ? <Empty icon={<LoaderCircle size={25} className="spin" aria-hidden="true" />}>正在读取…</Empty> : announcements ? <>{(value as AnnouncementPage | null)?.warnings?.map(warning => <p className="monitor-warning" key={warning}>{warning}</p>)}<AnnouncementList rows={(value as ActivityPage<Announcement> | null)?.items || []} dialog={dialog} /></> : <AlertList rows={(value as AlertPage | null)?.items || []} rates={rates} />}
      <div className="request-pagination"><span>{announcements ? "显示上游最近一次成功采集返回的公告" : `保留最近 ${revision.settings.retention_days} 天记录`}{loading ? " · 正在读取…" : ""}</span>
        <label>每页 <select aria-label={`每页${title}条数`} value={size} onChange={event => { setSize(Number(event.target.value)); setPage(1); }}>{[25, 50, 100, 200].map(count => <option key={count} value={count}>{count}</option>)}</select> 条</label>
        <button className="secondary" aria-label={`上一页${title}`} disabled={loading || page <= 1} onClick={() => setPage(value => value - 1)}><ChevronLeft size={15} />上一页</button><span>第 {page} / {pages} 页</span><button className="secondary" aria-label={`下一页${title}`} disabled={loading || page >= pages} onClick={() => setPage(value => value + 1)}>下一页<ChevronRight size={15} /></button>
      </div>
    </div>
  </>;
}
