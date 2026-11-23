import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { FileSearch, FileText, LoaderCircle, RefreshCw, Search } from "lucide-react";
import { api, protocolNames, type ChannelRow, type RequestDetail, type RequestLog, type RequestListResult } from "./api";
import RequestMetrics from "./RequestMetrics";
import JsonBody from "./JsonBody";
import { attemptStatusLabel, requestEndLabel } from "./requestEnd";
import { requestTiming } from "./requestTiming";

type DialogComponent = ComponentType<{ title: string; children: ReactNode; close: () => void }>;
const stateNames: Record<string, string> = { running: "请求中", streaming: "响应中", completed: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断" };
const active = (row: RequestLog) => row.state === "running" || row.state === "streaming";
const seconds = (ms: number) => `${(ms / 1000).toFixed(2)}s`;
const date = (value: string | null) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
const count = (value: number | undefined) => (value || 0).toLocaleString("zh-CN");

function State({ row }: { row: RequestLog }) {
  return <span className={`request-state ${active(row) ? "active" : row.status >= 400 ? "error" : "success"}`}>
    {active(row) && <LoaderCircle size={13} className="spin" />}
    {active(row) && row.retry_at ? "等待重试" : stateNames[row.state] || (row.status >= 400 ? "失败" : "已完成")}{!active(row) && row.status > 0 ? ` · ${row.status}` : ""}
  </span>;
}

function Detail({ id, dialog: Dialog, close }: { id: number; dialog: DialogComponent; close: () => void }) {
  const [value, setValue] = useState<RequestDetail | null>(null);
  const [error, setError] = useState("");
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
      } catch (error) { if (!stopped) setError((error as Error).message); }
      finally { pending = false; }
    }
    void load();
    const timer = setInterval(() => { if (!document.hidden) void load(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
  }, [id]);
  const row = value?.request;
  const payload = value?.detail;
  const firstContent = row ? requestTiming(row).firstContentMS : null;
  return <Dialog title="请求详情" close={close}>
    <div className="request-detail">
      {error && <p className="form-error" role="alert">{error}</p>}
      {!row ? !error && <p role="status">正在读取请求详情…</p> : <>
        <div className="request-detail-heading"><div><strong>{row.model || "无模型"}</strong><code>请求编号：{row.request_id}</code></div><State row={row} /></div>
        <RequestMetrics row={row} />
        <p className="field-hint">首字节耗时：{row.first_byte_ms == null ? "—" : seconds(row.first_byte_ms)} · <span title="从收到请求到收到首段正文、公开思考文本或工具内容；加密思考不等同于可见内容。">首内容：{firstContent == null ? row.stream ? "未记录" : "不适用" : seconds(firstContent)}</span> · <span title="从首内容到上游流结束；整包返回时无法据此推算模型生成速度。">输出阶段耗时：{row.generation_ms == null ? row.stream ? "未记录" : "不适用" : seconds(row.generation_ms)}</span></p>
        <p className="field-hint">{row.started_at ? "开始" : "记录"}于 {date(row.started_at || row.created_at)} · 结束于 {date(row.finished_at)}</p>
        {row.error && <p className="form-error">{row.error}</p>}
        {row.routing_reason && <p className="field-hint">选路说明：{row.routing_reason}{row.retry_at && ` · 预计重试于 ${date(row.retry_at)}`}</p>}
        {row.finish_reason === "stop" && <p className="field-hint">上游结束原因：{requestEndLabel(row.finish_reason, row.incomplete_reason)}</p>}
        {row.cost_warning && <p className="field-hint">计价说明：{row.cost_warning}</p>}
        {payload?.attempts.length ? <section className="request-attempts"><h3>上游尝试</h3><ol>{payload.attempts.map((attempt, index) => <li key={index}>
          <div><strong>{attempt.channel_name || `渠道 #${attempt.channel_id}`}</strong><code>{attempt.model}</code><small>{protocolNames[attempt.protocol] || attempt.protocol}</small></div>
          <span>{attemptStatusLabel(attempt.status, attempt.upstream_status)}{attempt.status > 0 && ` · ${seconds(attempt.duration_ms)}`}</span>
          {attempt.upstream_request_id && <p className="field-hint">上游请求 ID：<code style={{ overflowWrap: "anywhere" }}>{attempt.upstream_request_id}</code></p>}
          {attempt.error && <p className="form-error">{attempt.error}</p>}
          {attempt.reason && <p className="field-hint">{attempt.reason}</p>}
          {attempt.finish_reason && <p className="field-hint">上游结束原因：{requestEndLabel(attempt.finish_reason, attempt.incomplete_reason)}</p>}
        </li>)}</ol></section> : null}
        {!payload?.available ? <p className="field-hint">本次请求未记录正文。</p> : <>
          {active(row) && <p className="field-hint" role="status">请求正在进行，每 2 秒更新。Token 和费用以实际返回的用量为准。</p>}
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
        <option value="">全部状态</option><option value="running">进行中</option><option value="completed">已完成</option><option value="failed">失败</option><option value="cancelled">已取消</option>
      </select>
      <button className="secondary" type="button" disabled={loading} onClick={() => setVersion(value => value + 1)}><RefreshCw size={14} />刷新请求记录</button>
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="panel">
      <div className="panel-heading"><div><h2 className="icon-heading"><FileText size={18} aria-hidden="true" />近期请求</h2><p>已结束请求及正文最多缓存 50 条、32 MiB，进行中请求另计。重启后清空，历史用量统计继续保留。</p></div><span>共 {count(data?.total)} 条 · 进行中 {count(data?.active)} 条</span></div>
      <RequestList rows={data?.items || []} channels={channels} dialog={dialog} />
      <div className="request-pagination">
        <span>每 2 秒自动刷新{loading ? " · 正在读取…" : ""}</span>
      </div>
    </div>
  </>;
}
