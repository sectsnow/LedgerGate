import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { ChevronLeft, ChevronRight, LoaderCircle, RefreshCw, Search } from "lucide-react";
import { api, protocolNames, type ChannelRow, type RequestDetail, type RequestLog, type RequestPage } from "./api";
import RequestMetrics from "./RequestMetrics";
import JsonBody from "./JsonBody";

type DialogComponent = ComponentType<{ title: string; children: ReactNode; close: () => void }>;
const stateNames: Record<string, string> = { running: "请求中", streaming: "响应中", completed: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断" };
const active = (row: RequestLog) => row.state === "running" || row.state === "streaming";
const seconds = (ms: number) => `${(ms / 1000).toFixed(2)}s`;
const date = (value: string | null) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
const count = (value: number | undefined) => (value || 0).toLocaleString("zh-CN");

function State({ row }: { row: RequestLog }) {
  return <span className={`request-state ${active(row) ? "active" : row.status >= 400 ? "error" : "success"}`}>
    {active(row) && <LoaderCircle size={13} className="spin" />}
    {stateNames[row.state] || (row.status >= 400 ? "失败" : "已完成")}{!active(row) && row.status > 0 ? ` · ${row.status}` : ""}
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
  return <Dialog title="请求详情" close={close}>
    <div className="request-detail">
      {error && <p className="form-error" role="alert">{error}</p>}
      {!row ? !error && <p role="status">正在读取请求详情…</p> : <>
        <div className="request-detail-heading"><div><strong>{row.model || "无模型"}</strong><code>请求编号：{row.request_id}</code></div><State row={row} /></div>
        <RequestMetrics row={row} />
        <p className="field-hint">首字节耗时：{row.first_byte_ms == null ? "—" : seconds(row.first_byte_ms)} · {row.started_at ? "开始" : "记录"}于 {date(row.started_at || row.created_at)} · 结束于 {date(row.finished_at)}</p>
        {row.error && <p className="form-error">{row.error}</p>}
        {row.cost_warning && <p className="field-hint">计价说明：{row.cost_warning}</p>}
        {payload?.attempts.length ? <section className="request-attempts"><h3>上游尝试</h3><ol>{payload.attempts.map((attempt, index) => <li key={index}>
          <div><strong>{attempt.channel_name || `渠道 #${attempt.channel_id}`}</strong><code>{attempt.model}</code><small>{protocolNames[attempt.protocol] || attempt.protocol}</small></div>
          <span>{attempt.status ? `HTTP ${attempt.status} · ${seconds(attempt.duration_ms)}` : "进行中"}</span>
          {attempt.error && <p className="form-error">{attempt.error}</p>}
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
    {!rows.length ? <div className="empty"><p>暂无请求记录</p></div> : <div className="request-records">{rows.map(row => <div key={row.id} className="request-record" onClick={event => {
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
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [state, setState] = useState("");
  const [version, setVersion] = useState(0);
  const [data, setData] = useState<RequestPage | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => { setQuery(draft.trim()); setPage(1); }, 250);
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
        const params = new URLSearchParams({ page: String(page), page_size: String(size), q: query, state });
        const value = await api<RequestPage>(`/requests?${params}`);
        if (!stopped) { setData(value); setError(""); }
      } catch (error) { if (!stopped) setError((error as Error).message); }
      finally { pending = false; if (!stopped) setLoading(false); }
    }
    void load();
    const timer = setInterval(() => { if (!document.hidden) void load(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
  }, [page, size, query, state, version]);
  const pages = Math.max(1, Math.ceil((data?.total || 0) / size));
  useEffect(() => { if (page > pages) setPage(pages); }, [page, pages]);
  return <>
    <div className="list-toolbar request-toolbar">
      <div className="search"><Search size={16} /><input aria-label="搜索请求记录" placeholder="搜索模型、渠道、密钥名称或请求编号" value={draft} onChange={event => setDraft(event.target.value)} /></div>
      <select aria-label="筛选请求状态" value={state} onChange={event => { setState(event.target.value); setPage(1); }}>
        <option value="">全部状态</option><option value="running">进行中</option><option value="completed">已完成</option><option value="failed">失败</option><option value="cancelled">已取消</option>
      </select>
      <button className="secondary" type="button" disabled={loading} onClick={() => setVersion(value => value + 1)}><RefreshCw size={14} />刷新请求记录</button>
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="panel">
      <div className="panel-heading"><div><h2>近期请求</h2><p>已结束请求及正文最多缓存 50 条、32 MiB，进行中请求另计。重启后清空，历史用量统计继续保留。</p></div><span>共 {count(data?.total)} 条 · 进行中 {count(data?.active)} 条</span></div>
      <RequestList rows={data?.items || []} channels={channels} dialog={dialog} />
      <div className="request-pagination">
        <span>每 2 秒自动刷新{loading ? " · 正在读取…" : ""}</span>
        <label>每页 <select aria-label="每页请求条数" value={size} onChange={event => { setSize(Number(event.target.value)); setPage(1); }}>{[25, 50, 100, 200].map(value => <option key={value} value={value}>{value}</option>)}</select> 条</label>
        <button className="secondary" aria-label="上一页请求" disabled={loading || page <= 1} onClick={() => setPage(value => value - 1)}><ChevronLeft size={15} />上一页</button>
        <span>第 {page} / {pages} 页</span>
        <button className="secondary" aria-label="下一页请求" disabled={loading || page >= pages} onClick={() => setPage(value => value + 1)}>下一页<ChevronRight size={15} /></button>
      </div>
    </div>
  </>;
}
