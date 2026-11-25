import { useEffect, useState } from "react";
import { api, type Account } from "./api";

type Part = {status: string; attempted_at: string | null; succeeded_at: string | null; message: string};
type Event = {id: number; kind: string; status: string; message: string; created_at: string};
type Snapshot = {items: Record<string, Part>; events: Event[]; enabled: boolean; interval_seconds: number};
const names: Record<string, string> = {balance: "余额", used: "累计消费", today: "今日实际消费", rates: "倍率", subscriptions: "订阅", announcements: "公告", prices: "自动报价"};
const states: Record<string, string> = {ok: "成功", failed: "失败", skipped: "未执行", unsupported: "未接入", unknown: "未知", unchanged: "无需更新", pending: "等待重试"};
const date = (value: string | null | undefined) => value ? new Date(value).toLocaleString("zh-CN", {hour12: false}) : "尚无记录";
export default function AccountCollection({ account }: {account: Account}) {
  const [open, setOpen] = useState(false), [value, setValue] = useState<Snapshot | null>(null), [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController(); let pending = false;
    const load = async () => {
      if (pending || document.hidden) return; pending = true;
      try { const data = await api<Snapshot>(`/accounts/${account.id}/collection`, "GET", undefined, controller.signal); if (!controller.signal.aborted) {setValue(data); setError("");} }
      catch (error) { if (!controller.signal.aborted) setError((error as Error).message); }
      finally { pending = false; }
    };
    void load(); const timer = window.setInterval(() => void load(), 15_000);
    return () => {controller.abort(); window.clearInterval(timer);};
  }, [open, account.id, account.checked_at]);
  return <details className="account-collection" onToggle={event => setOpen(event.currentTarget.open)}><summary>分项采集状态与报价时间线</summary>
    {error && <p className="form-error" role="alert">{error}</p>}
    {value ? <><p className="field-hint">{value.enabled ? "定时采集已开启" : "监控已暂停，以下为历史结果"}；超过采集周期两倍未成功的结果标记过期。首次升级不会补造历史成功时间。</p>
      <dl>{Object.entries(names).map(([kind, label]) => {const part = value.items[kind]; const stale = part?.succeeded_at && Date.now() - Date.parse(part.succeeded_at) > value.interval_seconds * 2000; return <div key={kind}><dt><strong>{label}</strong> · <span className={part?.status === "failed" ? "form-error" : ""}>{states[part?.status] || "未知"}{stale && " · 旧数据"}</span></dt><dd>{part?.message}<small className="table-sub">最近尝试 {date(part?.attempted_at)} · 最近成功 {date(part?.succeeded_at)}</small></dd></div>;})}</dl>
      <p className="field-hint">最近 30 天，每个账户最多保留 100 条倍率／报价事件，展示最近 50 条。</p><ol>{value.events.map(event => <li key={event.id}><time>{date(event.created_at)}</time> · {names[event.kind]} · {states[event.status] || event.status}<p>{event.message}</p></li>)}</ol>{!value.events.length && <p className="field-hint">暂无倍率或报价变化事件。</p>}</> : open && !error && <p role="status">正在读取分项状态…</p>}
  </details>;
}
