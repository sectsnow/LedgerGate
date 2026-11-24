import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { Download, FileSearch, FileText, RefreshCw, Search } from "lucide-react";
import { api, protocolNames, type ChannelRow, type RequestDetail, type RequestLog, type RequestListResult } from "./api";
import RequestMetrics from "./RequestMetrics";
import JsonBody from "./JsonBody";
import { attemptStatusLabel, requestEndLabel } from "./requestEnd";
import CopyButton from "./CopyButton";

type DialogComponent = ComponentType<{ title: string; children: ReactNode; close: () => void }>;
const active = (row: RequestLog) => row.state === "running" || row.state === "streaming";
const seconds = (ms: number) => `${(ms / 1000).toFixed(2)}s`;
const count = (value: number | undefined) => (value || 0).toLocaleString("zh-CN");

function Detail({ id, dialog: Dialog, close }: { id: number; dialog: DialogComponent; close: () => void }) {
  const [value, setValue] = useState<RequestDetail | null>(null);
  const [error, setError] = useState("");
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    let stopped = false;
    let terminal = false;
    let pending = false;
    async function load() {
      if (pending || terminal) return;
      pending = true;
      try {
        const next = await api<RequestDetail>(`/requests/${id}`);
        if (!stopped) { setValue(next); setError(""); terminal = !active(next.request); }
      } catch (error) {
        if (!stopped) {
          if (error instanceof Error && "status" in error && error.status === 404) {
            terminal = true; setExpired(true); setError("");
          } else { setError((error as Error).message); }
        }
      }
      finally { pending = false; }
    }
    void load();
    const timer = setInterval(() => { if (!document.hidden) void load(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
  }, [id]);
  const row = value?.request;
  const payload = value?.detail;
  function download() {
    if (!value) return;
    try {
      const body = JSON.stringify({ exported_at: new Date().toISOString(), source_unavailable: expired, note: "本文件为当前已获取的脱敏预览，请求和响应各最多 256 KiB；截断状态见 request_truncated、response_truncated，进行中的请求为导出时的快照。", request: value.request, detail: value.detail }, null, 2);
      const url = URL.createObjectURL(new Blob([body], { type: "application/json;charset=utf-8" }));
      const link = document.createElement("a");
      link.href = url; link.download = `ledgergate-${value.request.request_id.replace(/[^a-zA-Z0-9_-]/g, "_") || value.request.id}.json`;
      document.body.appendChild(link);
      try { link.click(); } finally { link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
    } catch (error) { setError((error as Error).message); }
  }
  return <Dialog title="请求详情" close={close}>
    <div className="request-detail">
      {error && <p className="form-error" role="alert">{error}</p>}
      {expired && <p className="field-hint" role="status">请求已移出内存缓存或服务已重启，已停止自动刷新。{row && "以下保留最后读取的快照，无法确认此后的请求结果。"}</p>}
      {!row ? !error && !expired && <p role="status">正在读取请求详情…</p> : <>
        <div className="request-detail-tools">
          <div className="request-detail-id"><span>请求编号：<code>{row.request_id}</code></span><CopyButton text={row.request_id} label="复制请求 ID" /></div>
          <button className="secondary" type="button" onClick={download}><Download size={15} aria-hidden="true" />{expired ? "导出已读取快照" : "导出脱敏详情"}</button>
        </div>
        <RequestMetrics row={row} detail />
        {payload?.attempts.length ? <section className="request-attempts"><h3>上游尝试</h3><ol>{payload.attempts.map((attempt, index) => <li key={index}>
          <div><strong>{attempt.channel_name || `渠道 #${attempt.channel_id}`}</strong><code>{attempt.model}</code><small>{protocolNames[attempt.protocol] || attempt.protocol}</small></div>
          <span>{attemptStatusLabel(attempt.status, attempt.upstream_status)}{attempt.status > 0 && ` · ${seconds(attempt.duration_ms)}`}</span>
          {attempt.upstream_request_id && <div className="request-detail-id field-hint"><span>上游请求 ID：<code>{attempt.upstream_request_id}</code></span><CopyButton text={attempt.upstream_request_id} label={`复制第 ${index + 1} 次上游请求 ID`} /></div>}
          {attempt.error && <p className="form-error">{attempt.error}</p>}
          {attempt.reason && <p className="field-hint">{attempt.reason}</p>}
          {attempt.finish_reason && <p className="field-hint">上游结束原因：{requestEndLabel(attempt.finish_reason, attempt.incomplete_reason)}</p>}
        </li>)}</ol></section> : null}
        {!payload?.available ? <p className="field-hint">本次请求未记录正文。</p> : <>
          {active(row) && !expired && <p className="field-hint" role="status">请求正在进行，每 2 秒更新。Token 和费用以实际返回的用量为准。</p>}
          <div className="request-body-columns">
            <JsonBody title="请求内容" text={payload.request_body} truncated={payload.request_truncated} placeholder="请求体尚未读取或未通过校验。" />
            <JsonBody title="响应内容" text={payload.response_body} truncated={payload.response_truncated} stream={row.stream} placeholder={active(row) ? "等待上游响应…" : "未收到响应内容。"} />
          </div>
          {(payload.request_truncated || payload.response_truncated) && <p className="field-hint">请求、响应各保留最多 256 KiB 预览，不影响实际转发内容。</p>}
        </>}
      </>}
    </div>
  </Dialog>;
}

export function RequestList({ rows, channels, small, dialog }: { rows: RequestLog[]; channels: ChannelRow[]; small?: boolean; dialog: DialogComponent }) {
  const [selected, setSelected] = useState<number | null>(null);
  return <>
    {!rows.length ? <div className="empty"><div className="empty-symbol"><FileSearch size={25} aria-hidden="true" /></div><p>暂无请求记录</p></div> : <div className="request-records">{rows.map(row => <div key={row.id} className="request-record" onClick={event => {
      if (!(event.target as Element).closest("button, a, details, input, select")) setSelected(row.id);
    }}>
      <RequestMetrics row={row} channel={channels.find(item => item.channel.id === row.channel_id)} small={small}>
        <button className="secondary" type="button" aria-label={`查看请求 ${row.request_id}`} onClick={() => setSelected(row.id)}>查看详情</button>
      </RequestMetrics>
    </div>)}</div>}
    {selected !== null && <Detail key={selected} id={selected} dialog={dialog} close={() => setSelected(null)} />}
  </>;
}

export default function RequestHistory({ channels, dialog }: { channels: ChannelRow[]; dialog: DialogComponent }) {
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [state, setState] = useState("");
  const [version, setVersion] = useState(0);
  const [data, setData] = useState<RequestListResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setQuery(draft.trim()), 250);
    return () => clearTimeout(timer);
  }, [draft]);
  useEffect(() => {
    let stopped = false;
    let pending = false;
    async function load() {
      if (pending) return;
      pending = true;
      setLoading(true);
      try {
        const params = new URLSearchParams({ q: query, state });
        const value = await api<RequestListResult>(`/requests?${params}`);
        if (!stopped) { setData(value); setError(""); }
      } catch (error) { if (!stopped) setError((error as Error).message); }
      finally { pending = false; if (!stopped) setLoading(false); }
    }
    void load();
    const timer = setInterval(() => { if (!document.hidden) void load(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
  }, [query, state, version]);
  return <>
    <div className="list-toolbar request-toolbar">
      <div className="search"><Search size={16} /><input aria-label="搜索请求记录" placeholder="搜索模型、渠道、密钥名称或请求编号" value={draft} onChange={event => setDraft(event.target.value)} /></div>
      <select aria-label="筛选请求状态" value={state} onChange={event => setState(event.target.value)}>
        <option value="">全部状态</option><option value="running">进行中</option><option value="completed">已完成</option><option value="failed">失败</option><option value="cancelled">客户端断开</option>
      </select>
      <button className="secondary" type="button" disabled={loading} onClick={() => setVersion(value => value + 1)}><RefreshCw size={14} />刷新请求记录</button>
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="panel request-history-panel">
      <div className="panel-heading"><div><h2 className="icon-heading"><FileText size={18} aria-hidden="true" />近期请求</h2><p>已结束请求及正文最多缓存 50 条、32 MiB，进行中请求另计。重启后清空，历史用量统计继续保留。</p></div><span>共 {count(data?.total)} 条 · 进行中 {count(data?.active)} 条</span></div>
      <RequestList rows={data?.items || []} channels={channels} dialog={dialog} />
      <div className="request-pagination">
        <span>每 2 秒自动刷新{loading ? " · 正在读取…" : ""}</span>
      </div>
    </div>
  </>;
}
