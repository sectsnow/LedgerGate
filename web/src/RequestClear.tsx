import { useState, type ComponentType, type ReactNode } from "react";
import { LoaderCircle, Trash2 } from "lucide-react";
import { api } from "./api";

export type RequestClearResult = {
  requests: number;
  details: number;
  summary_days: number;
  kept_requests: number;
  active_requests: number;
};
type Preview = RequestClearResult & {
  token: string;
  first_at: string | null;
  last_at: string | null;
  expires_at: string;
};

const modes = {
  range: { name: "按日期清除", description: "按北京时间清除起止日期内已结束的请求、正文和历史汇总，包含首尾两天。" },
  all: { name: "清空请求历史", description: "清除全部已结束请求、正文和历史汇总；后续请求继续正常记录。" },
} as const;
const date = (value: string) => new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", hour12: false });

export default function RequestClear({ dialog: Dialog, close, cleared }: {
  dialog: ComponentType<{ title: string; children: ReactNode; close: () => void }>;
  close: () => void;
  cleared: (result: RequestClearResult) => Promise<void>;
}) {
  const today = new Date(Date.now() + 8 * 3600000).toISOString().slice(0, 10);
  const [mode, setMode] = useState<keyof typeof modes>("range");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState(today);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState<"preview" | "clear" | null>(null);
  const [error, setError] = useState("");
  function reset() { setPreview(null); setConfirmation(""); setError(""); }
  const empty = preview != null && preview.requests === 0 && preview.summary_days === 0;
  return <Dialog title="清除请求数据" close={() => { if (!busy) close(); }}>
    <form className="request-clear-form" onSubmit={async event => {
      event.preventDefault(); setBusy("preview"); setError(""); setPreview(null); setConfirmation("");
      try { setPreview(await api<Preview>("/requests/clear/preview", "POST", { mode, ...(mode === "range" ? { start, end } : {}) })); }
      catch (error) { setError((error as Error).message); }
      finally { setBusy(null); }
    }}>
      <div className="field"><label htmlFor="request-clear-mode">清除范围</label><select id="request-clear-mode" value={mode} disabled={!!busy} onChange={event => { setMode(event.target.value as keyof typeof modes); reset(); }}>
        {Object.entries(modes).map(([id, value]) => <option key={id} value={id}>{value.name}</option>)}
      </select><small>{modes[mode].description}</small></div>
      {mode === "range" && <div className="request-clear-dates">
        <label className="field">开始日期<input type="date" required max={end || today} value={start} disabled={!!busy} onChange={event => { setStart(event.target.value); reset(); }} /></label>
        <label className="field">结束日期<input type="date" required min={start || undefined} max={today} value={end} disabled={!!busy} onChange={event => { setEnd(event.target.value); reset(); }} /></label>
      </div>}
      <p className="field-hint">清理范围不受列表搜索、状态筛选或分页限制。账户、渠道、模型、密钥和密钥已用额度均保留，上游余额及消费不变。</p>
      <p className="field-hint">预览会固定本次范围。新到达和预览时正在进行的请求会保留；请求、正文和统计一起更新。</p>
      <button className="secondary" type="submit" disabled={!!busy}>{busy === "preview" && <LoaderCircle size={15} className="spin" />}预览清除范围</button>
      {preview && <section className="request-clear-preview" aria-label="清除预览" aria-live="polite">
        <p>{empty ? "所选范围没有可清除的数据。" : <>将清除 <strong>{preview.requests.toLocaleString("zh-CN")}</strong> 条请求、<strong>{preview.details.toLocaleString("zh-CN")}</strong> 份正文，联动更新 <strong>{preview.summary_days}</strong> 天汇总。</>}</p>
        <p>保留 {preview.kept_requests.toLocaleString("zh-CN")} 条请求，其中进行中 {preview.active_requests.toLocaleString("zh-CN")} 条。新到达的请求也会保留。</p>
        {preview.first_at && preview.last_at && <p>请求时间：{date(preview.first_at)} 至 {date(preview.last_at)}（北京时间）。</p>}
        <p>预览有效至 {date(preview.expires_at)}。删除后需通过备份恢复。</p>
      </section>}
      {preview && !empty && mode === "all" && <div className="field"><label htmlFor="request-clear-confirm">输入“清空”以确认</label><input id="request-clear-confirm" value={confirmation} disabled={!!busy} autoComplete="off" onChange={event => setConfirmation(event.target.value)} /></div>}
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="request-clear-actions">
        <a href="#settings" onClick={event => { if (busy) event.preventDefault(); else close(); }}>前往备份与恢复</a>
        <button className="secondary" type="button" disabled={!!busy} onClick={close}>取消</button>
        <button className="destructive" type="button" disabled={!!busy || !preview || empty || mode === "all" && confirmation.trim() !== "清空"} onClick={async () => {
          if (!preview) return;
          setBusy("clear"); setError("");
          try {
            const result = await api<RequestClearResult>("/requests/clear", "POST", { token: preview.token, confirmation });
            await cleared(result); close();
          } catch (error) { setError((error as Error).message); setPreview(null); }
          finally { setBusy(null); }
        }}>{busy === "clear" ? <LoaderCircle size={15} className="spin" /> : <Trash2 size={15} />}{busy === "clear" ? "正在清除…" : "确认清除"}</button>
      </div>
    </form>
  </Dialog>;
}
