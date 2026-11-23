import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { Bell, ChevronLeft, ChevronRight, History, LoaderCircle, Mail, MessagesSquare, Pause, Pencil, Play, Plus, Send, Trash2, Webhook } from "lucide-react";
import { api, type AccountRow, type ActivityPage, type NotificationChannel, type NotificationDelivery } from "./api";
import "./notifications.css";

type DialogComponent = ComponentType<{ title: string; children: ReactNode; close: () => void }>;
type FieldComponent = ComponentType<{ label: string; children: ReactNode; hint?: string }>;
const notificationTypes: Record<string, string> = { telegram: "Telegram", webhook: "通用 Webhook", email: "Email", wecom: "企业微信", dingtalk: "钉钉", feishu: "飞书", serverchan: "Server酱" };
const notificationIcons: Record<string, typeof Bell> = { telegram: Send, webhook: Webhook, email: Mail, wecom: MessagesSquare, dingtalk: MessagesSquare, feishu: MessagesSquare, serverchan: Bell };
const deliveryNames: Record<string, string> = { pending: "等待发送", sent: "已送达", failed: "发送失败", cancelled: "已取消", partial: "部分送达" };
const notificationEvents = [
  ["low_balance", "余额偏低"], ["monitor_error", "采集失败"], ["recovered", "采集恢复"], ["rates_changed", "倍率变化"], ["announcement", "公告更新"], ["monitor_warning", "部分采集失败"], ["monitor_warning_recovered", "部分采集恢复"], ["channel_auto_disabled", "渠道自动停用"], ["subscription_expiring", "订阅到期"], ["subscription_daily", "日余量偏低"], ["subscription_weekly", "周余量偏低"], ["subscription_monthly", "月余量偏低"],
];
const date = (value: string) => new Date(value).toLocaleString("zh-CN", { hour12: false });

function NotificationTypeLabel({ type }: { type: string }) {
  const Icon = notificationIcons[type] || Bell;
  return <span className="icon-label"><Icon size={14} aria-hidden="true" />{notificationTypes[type] || type}</span>;
}

export default function NotificationPanel({ accounts, defaultRetries, dialog: Dialog, field: Field }: { accounts: AccountRow[]; defaultRetries: number; dialog: DialogComponent; field: FieldComponent }) {
  const [rows, setRows] = useState<NotificationChannel[] | null>(null);
  const [revision, setRevision] = useState(0), [error, setError] = useState(""), [message, setMessage] = useState(""), [busy, setBusy] = useState("");
  const [editing, setEditing] = useState<NotificationChannel | "new" | null>(null), [removing, setRemoving] = useState<NotificationChannel | null>(null), [history, setHistory] = useState<number | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    api<NotificationChannel[]>("/notification-channels", "GET", undefined, controller.signal).then(setRows).catch(error => { if (!controller.signal.aborted) setError((error as Error).message); });
    return () => controller.abort();
  }, [revision]);
  useEffect(() => { const timer = window.setInterval(() => setRevision(value => value + 1), 30_000); return () => window.clearInterval(timer); }, []);
  const act = async (key: string, action: () => Promise<unknown>, success: string) => {
    setBusy(key); setError(""); setMessage("");
    try { await action(); setMessage(success); setRemoving(null); }
    catch (error) { setError((error as Error).message); }
    finally { setBusy(""); setRevision(value => value + 1); }
  };
  return <section className="panel notification-panel">
    <div className="panel-heading"><div><h2 className="icon-heading"><Bell size={18} aria-hidden="true" />通知渠道 {rows && <span className="muted">{rows.length}</span>}</h2><p>为不同账户和告警配置接收方式，各渠道独立发送与重试。</p></div><div className="inline-actions"><button className="secondary" onClick={() => setHistory(0)}><History size={15} />投递记录</button><button className="primary" disabled={!!busy || rows === null} onClick={() => setEditing("new")}><Plus size={16} />新增通知渠道</button></div></div>
    <div className="notification-panel-body">
    {error && <p className="form-error" role="alert">{error}</p>}
    {message && <p className="field-hint" role="status">{message}</p>}
    {rows === null ? <div className="empty">{error ? <button className="secondary" onClick={() => { setError(""); setRevision(value => value + 1); }}>重新加载</button> : "正在读取通知渠道…"}</div> : !rows.length ? <div className="empty notification-empty"><Bell size={26} /><p>尚未添加通知渠道</p><small>支持 Telegram、Webhook、Email、企业微信、钉钉、飞书和 Server酱。</small></div> : <div className="notification-list">{rows.map(row => <article className={`notification-card ${row.enabled ? "" : "is-paused"}`} key={row.id}>
      <div className="notification-card-body"><div className="notification-card-title"><strong>{row.name}</strong><span className="badge"><NotificationTypeLabel type={row.type} /></span><span className={`badge ${row.enabled ? "good" : ""}`}>{row.enabled ? "已启用" : "已暂停"}</span></div>
        <p className="field-hint">{row.accounts?.length ? `${row.accounts.length} 个账户` : "全部账户"} · {row.events?.length ? `${row.events.length} 类事件` : "全部事件"} · 最多尝试 {row.retries} 次{row.use_proxy && " · 使用全局代理"}</p>
        {row.last_delivery ? <p className="notification-last">最近投递：<span className={`badge ${row.last_delivery.status}`}>{deliveryNames[row.last_delivery.status]}</span><time dateTime={row.last_delivery.updated_at}>{date(row.last_delivery.updated_at)}</time>{row.last_delivery.error && <span className="card-error">{row.last_delivery.error}</span>}</p> : <p className="field-hint">尚无投递记录</p>}
      </div>
      <div className="notification-actions">
        <button className="secondary" disabled={!!busy} aria-label={`${row.name}：${row.enabled ? "暂停" : "启用"}`} onClick={() => void act(`enabled-${row.id}`, () => api(`/notification-channels/${row.id}/enabled`, "PATCH", { enabled: !row.enabled }), row.enabled ? "渠道已暂停，待发任务将在恢复后继续" : "通知渠道已启用")}>{row.enabled ? <Pause size={14} aria-hidden="true" /> : <Play size={14} aria-hidden="true" />}{row.enabled ? "暂停" : "启用"}</button>
        <button className="secondary" disabled={!!busy} aria-label={`${row.name}：测试通知`} onClick={() => void act(`test-${row.id}`, () => api(`/notification-channels/${row.id}/test`, "POST"), "测试通知已送达")}>
          {busy === `test-${row.id}` ? <LoaderCircle size={14} className="spin" /> : <Send size={14} />}测试
        </button>
        <button className="icon-button" disabled={!!busy} aria-label={`${row.name}：投递记录`} onClick={() => setHistory(row.id)}><History size={16} /></button>
        <button className="icon-button" disabled={!!busy} aria-label={`${row.name}：编辑`} onClick={() => setEditing(row)}><Pencil size={16} /></button>
        <button className="icon-button danger" disabled={!!busy} aria-label={`${row.name}：删除`} onClick={() => setRemoving(row)}><Trash2 size={16} /></button>
      </div>
    </article>)}</div>}
    <p className="field-hint notification-note">暂停后不接收新告警，已排队的通知等待恢复。测试按钮会立即发送一条测试通知，暂停时也可测试。</p>
    </div>
    {editing && <NotificationForm channel={editing === "new" ? null : editing} accounts={accounts} defaultRetries={defaultRetries} dialog={Dialog} field={Field} close={() => setEditing(null)} saved={() => { setEditing(null); setMessage("通知渠道已保存"); setError(""); setRevision(value => value + 1); }} />}
    {removing && <Dialog title="删除通知渠道" close={() => { if (!busy) setRemoving(null); }}><p>删除“{removing.name}”并取消其待发通知？历史投递记录会保留。</p>{error && <p className="form-error" role="alert">{error}</p>}<div className="form-footer"><button className="secondary" disabled={!!busy} onClick={() => setRemoving(null)}>取消</button><button className="destructive" disabled={!!busy} onClick={() => void act(`delete-${removing.id}`, () => api(`/notification-channels/${removing.id}`, "DELETE"), "通知渠道已删除")}>{busy ? "正在删除…" : "确认删除"}</button></div></Dialog>}
    {history !== null && <DeliveryHistory channelID={history} channels={rows || []} dialog={Dialog} close={() => setHistory(null)} />}
  </section>;
}

function NotificationForm({ channel, accounts, defaultRetries, dialog: Dialog, field: Field, close, saved }: { channel: NotificationChannel | null; accounts: AccountRow[]; defaultRetries: number; dialog: DialogComponent; field: FieldComponent; close: () => void; saved: () => void }) {
  const [form, setForm] = useState({ name: channel?.name || "", type: channel?.type || "telegram", enabled: channel?.enabled ?? true, use_proxy: channel?.use_proxy ?? false,
    chat_id: channel?.chat_id || "", smtp_host: channel?.smtp_host || "", smtp_port: channel?.smtp_port || 587, smtp_tls: channel?.smtp_tls || "starttls", smtp_username: channel?.smtp_username || "", smtp_from: channel?.smtp_from || "",
    events: channel?.events || [], accounts: channel?.accounts || [], retries: channel?.retries || defaultRetries,
    url: "", secret: "", token: "", send_key: "", smtp_password: "", clear_url: false, clear_secret: false, clear_token: false, clear_send_key: false, clear_smtp_password: false });
  const [recipients, setRecipients] = useState((channel?.smtp_to || []).join("\n")), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const set = (value: Partial<typeof form>) => setForm(previous => ({ ...previous, ...value }));
  const sameType = channel?.type === form.type;
  const secretField = (key: "url" | "secret" | "token" | "send_key" | "smtp_password", label: string, has: boolean, hint: string, required = false) => {
    const clearKey = `clear_${key}` as const;
    return <div key={key}><Field label={label} hint={sameType && has ? "已加密保存，留空保留；填写可替换。" : hint}><input type={key === "url" ? "url" : "password"} maxLength={8192} autoComplete="new-password" required={form.enabled && required && !(sameType && has && !form[clearKey])} disabled={form[clearKey]} value={form[key]} onChange={event => set({ [key]: event.target.value })} /></Field>{sameType && has && <label className="checkbox notification-clear"><input type="checkbox" checked={form[clearKey]} onChange={event => set({ [clearKey]: event.target.checked, [key]: "" })} />清除已保存的{label}</label>}</div>;
  };
  return <Dialog title={channel ? "编辑通知渠道" : "新增通知渠道"} close={() => { if (!busy) close(); }}><form className="notification-form" onSubmit={async event => {
    event.preventDefault(); setBusy(true); setError("");
    try {
      const smtp_to = [...new Set(recipients.split(/[\n,;，；]+/).map(value => value.trim()).filter(Boolean))];
      await api(`/notification-channels${channel ? `/${channel.id}` : ""}`, channel ? "PUT" : "POST", { ...form, smtp_to }); saved();
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(false); }
  }}><fieldset className="notification-form-fields" disabled={busy}>
    <div className="form-grid"><Field label="渠道名称"><input required maxLength={100} value={form.name} placeholder="例如：运维告警群" onChange={event => set({ name: event.target.value })} /></Field><Field label="通知类型"><select value={form.type} onChange={event => set({ type: event.target.value as NotificationChannel["type"], url: "", secret: "", token: "", send_key: "", smtp_password: "", clear_url: false, clear_secret: false, clear_token: false, clear_send_key: false, clear_smtp_password: false, use_proxy: false })}>{Object.entries(notificationTypes).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field></div>
    <label className="checkbox"><input type="checkbox" checked={form.enabled} onChange={event => set({ enabled: event.target.checked })} />启用此通知渠道</label>
    {form.type === "telegram" && <>{secretField("token", "Bot Token", !!channel?.has_token, "由 BotFather 创建机器人后取得。", true)}<Field label="Chat ID" hint="填写用户、群组或频道的 Chat ID。"><input maxLength={100} required={form.enabled} value={form.chat_id} onChange={event => set({ chat_id: event.target.value })} /></Field></>}
    {["webhook", "feishu", "wecom", "dingtalk"].includes(form.type) && <>{secretField("url", "通知地址", !!channel?.has_url, "填写完整的 Webhook 地址，地址中的令牌也会加密保存。", true)}{["webhook", "feishu", "dingtalk"].includes(form.type) && secretField("secret", "签名密钥", !!channel?.has_secret, form.type === "webhook" ? "可选，接收端使用 X-LedgerGate-Signature 校验 HMAC-SHA256。" : "如机器人启用了加签，请填写对应的签名密钥。")}{form.type === "dingtalk" && <p className="field-hint">如使用关键词校验，可将关键词设为 LedgerGate；选择加签时需填写签名密钥。</p>}</>}
    {form.type === "serverchan" && secretField("send_key", "SendKey", !!channel?.has_send_key, "支持 SCT 与 sctp 开头的 SendKey。", true)}
    {form.type === "email" && <><div className="form-grid"><Field label="SMTP 主机"><input required={form.enabled} maxLength={253} placeholder="smtp.example.com" value={form.smtp_host} onChange={event => set({ smtp_host: event.target.value })} /></Field><Field label="SMTP 端口"><input required type="number" min={1} max={65535} value={form.smtp_port} onChange={event => set({ smtp_port: Number(event.target.value) })} /></Field><Field label="连接加密"><select value={form.smtp_tls} onChange={event => set({ smtp_tls: event.target.value })}><option value="starttls">STARTTLS（常用 587）</option><option value="tls">TLS（常用 465）</option></select></Field><Field label="SMTP 用户名" hint="通常为发件邮箱；无需认证的邮件中继可留空。"><input maxLength={250} autoComplete="off" value={form.smtp_username} onChange={event => set({ smtp_username: event.target.value })} /></Field></div>{secretField("smtp_password", "SMTP 密码或授权码", !!channel?.has_smtp_password, "按邮箱服务商要求填写授权码或密码。", !!form.smtp_username)}<Field label="发件邮箱"><input required={form.enabled} type="email" maxLength={320} value={form.smtp_from} onChange={event => set({ smtp_from: event.target.value })} /></Field><Field label="收件邮箱" hint="每行一个邮箱地址，也可用逗号或分号分隔，最多 50 个。"><textarea required={form.enabled} rows={3} value={recipients} onChange={event => setRecipients(event.target.value)} /></Field></>}
    {form.type !== "email" && <label className="checkbox"><input type="checkbox" checked={form.use_proxy} onChange={event => set({ use_proxy: event.target.checked })} />使用系统设置中的全局代理</label>}
    <details className="runtime-options notification-subscriptions"><summary>账户与事件订阅<span>{form.accounts.length ? `${form.accounts.length} 个账户` : "全部账户"} · {form.events.length ? `${form.events.length} 类事件` : "全部事件"}</span></summary>
      <fieldset><legend>通知账户（不勾选表示全部）</legend>{accounts.length ? <div className="notification-options">{accounts.map(({ account }) => <label className="checkbox" key={account.id}><input type="checkbox" checked={form.accounts.includes(account.id)} onChange={event => set({ accounts: event.target.checked ? [...form.accounts, account.id] : form.accounts.filter(id => id !== account.id) })} />{account.name}</label>)}</div> : <p className="field-hint">尚无上游账户，添加账户后可在此选择。</p>}</fieldset>
      <fieldset><legend>通知事件（不勾选表示全部）</legend><div className="notification-options">{notificationEvents.map(([value, label]) => <label className="checkbox" key={value}><input type="checkbox" checked={form.events.includes(value)} onChange={event => set({ events: event.target.checked ? [...form.events, value] : form.events.filter(item => item !== value) })} />{label}</label>)}</div></fieldset>
    </details>
    <Field label="最多尝试次数" hint="包含首次发送；失败后逐步延长重试间隔。"><input required type="number" min={1} max={30} value={form.retries} onChange={event => set({ retries: Number(event.target.value) })} /></Field>
  </fieldset>{error && <p className="form-error" role="alert">{error}</p>}<div className="form-footer"><button type="button" className="secondary" disabled={busy} onClick={close}>取消</button><button type="submit" className="primary" disabled={busy}>{busy ? "正在保存…" : "保存渠道"}</button></div></form></Dialog>;
}

function DeliveryHistory({ channelID, channels, dialog: Dialog, close }: { channelID: number; channels: NotificationChannel[]; dialog: DialogComponent; close: () => void }) {
  const [selected, setSelected] = useState(channelID), [page, setPage] = useState(1), [revision, setRevision] = useState(0), [value, setValue] = useState<ActivityPage<NotificationDelivery> | null>(null), [error, setError] = useState(""), [loading, setLoading] = useState(false);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError("");
    api<ActivityPage<NotificationDelivery>>(`/notification-deliveries?page=${page}&page_size=20${selected ? `&channel_id=${selected}` : ""}`, "GET", undefined, controller.signal).then(setValue).catch(error => { if (!controller.signal.aborted) setError((error as Error).message); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [page, selected, revision]);
  return <Dialog title="通知投递记录" close={close}><div className="notification-history"><div className="notification-history-controls"><label>通知渠道<select value={selected} onChange={event => { setSelected(Number(event.target.value)); setPage(1); setValue(null); }}><option value={0}>全部渠道（含已删除）</option>{channels.map(row => <option value={row.id} key={row.id}>{row.name}</option>)}</select></label><button className="secondary" disabled={loading} onClick={() => setRevision(value => value + 1)}>{loading ? "正在刷新…" : "刷新记录"}</button></div>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!value ? <div className="empty">{loading ? "正在读取…" : "暂无记录"}</div> : !value.items.length ? <div className="empty">暂无投递记录</div> : <div className="notification-history-list">{value.items.map(row => <article className="notification-history-entry" key={row.id}><div><strong>{row.title}</strong><span className={`badge ${row.status}`}>{deliveryNames[row.status]}</span></div><p className="field-hint">{row.channel_name} · <NotificationTypeLabel type={row.channel_type} /> · 已尝试 {row.attempts} 次{row.alert_id === null && " · 手动测试"}</p><time dateTime={row.updated_at}>{date(row.updated_at)}</time>{row.error && <p className="card-error">{row.error}</p>}{row.status === "pending" && <p className="field-hint">下次尝试：{date(row.next_retry_at)}，暂停期间等待恢复。</p>}</article>)}</div>}
    {value && <div className="notification-history-pagination"><span>共 {value.total} 条 · 第 {page} / {Math.max(1, Math.ceil(value.total / 20))} 页</span><div className="inline-actions"><button className="icon-button" aria-label="上一页" disabled={loading || page <= 1} onClick={() => setPage(page - 1)}><ChevronLeft size={18} /></button><button className="icon-button" aria-label="下一页" disabled={loading || page * 20 >= value.total} onClick={() => setPage(page + 1)}><ChevronRight size={18} /></button></div></div>}
  </div></Dialog>;
}
