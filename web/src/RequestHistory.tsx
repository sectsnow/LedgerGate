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

function Detail({ id, summaryOnly = false, dialog: Dialog, close }: { id: number; summaryOnly?: boolean; dialog: DialogComponent; close: () => void }) {
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
        const next = await api<RequestDetail>(`/${summaryOnly ? "request-summaries" : "requests"}/${id}`);
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
  }, [id, summaryOnly]);
  const row = value?.request;
  const payload = value?.detail;
  function download() {
    if (!value) return;
    try {
      const body = JSON.stringify({ exported_at: new Date().toISOString(), source_unavailable: expired, note: "本文件为已获取的脱敏数据；summary_only=true 表示持久化摘要，不含正文。内存详情的请求与响应各最多 256 KiB，进行中请求为导出时快照。", request: value.request, detail: value.detail }, null, 2);
      const url = URL.createObjectURL(new Blob([body], { type: "application/json;charset=utf-8" }));
      const link = document.createElement("a");
      link.href = url; link.download = `ledgergate-${value.request.request_id.replace(/[^a-zA-Z0-9_-]/g, "_") || value.request.id}.json`;
      document.body.appendChild(link);
      try { link.click(); } finally { link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
    } catch (error) { setError((error as Error).message); }
  }
  return <Dialog title={summaryOnly ? "历史请求摘要" : "请求详情"} close={close}>
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
        {!payload?.available ? <p className="field-hint">{summaryOnly ? "此处仅保留请求摘要与固定诊断分类，不保存正文或上游错误原文。" : "本次请求未记录正文。"}</p> : <>
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

export function RequestList({ rows, channels, small, dialog, summaryOnly = false }: { rows: RequestLog[]; summaryOnly?: boolean; channels: ChannelRow[]; small?: boolean; dialog: DialogComponent }) {
  const [selected, setSelected] = useState<number | null>(null);
  return <>
    {!rows.length ? <div className="empty"><div className="empty-symbol"><FileSearch size={25} aria-hidden="true" /></div><p>暂无请求记录</p></div> : <div className="request-records">{rows.map(row => <div key={row.id} className="request-record" onClick={event => {
      if (!(event.target as Element).closest("button, a, details, input, select")) setSelected(row.id);
    }}>
      <RequestMetrics row={row} channel={channels.find(item => item.channel.id === row.channel_id)} small={small}>
        <button className="secondary" type="button" aria-label={`查看请求 ${row.request_id}`} onClick={() => setSelected(row.id)}>查看详情</button>
      </RequestMetrics>
    </div>)}</div>}
    {selected !== null && <Detail key={`${summaryOnly}-${selected}`} id={selected} summaryOnly={summaryOnly} dialog={dialog} close={() => setSelected(null)} />}
  </>;
}

export function RequestPreview({rows,dialog:Dialog}:{rows:RequestLog[];dialog:DialogComponent}) {
  const [selected,setSelected]=useState<number|null>(null);
  return <><div className="table-scroll"><table className="request-preview"><thead><tr><th>开始时间</th><th>模型／渠道</th><th>状态</th><th>Token</th><th>总耗时</th><th>费用估算</th><th>详情</th></tr></thead><tbody>
    {rows.map(row=>{const known=!row.metrics_version || row.usage_recorded && !row.usage_warning;const costKnown=row.cost_status!=="unknown" && (known || row.cost_detail?.mode==="request");return <tr key={row.id}><td>{new Date(row.started_at||row.created_at).toLocaleTimeString("zh-CN",{hour12:false,timeZone:"Asia/Shanghai"})}</td><td><strong title={row.model}>{row.model||"未指定模型"}</strong><small className="table-sub" title={row.channel_name}>{row.channel_name||"未选择渠道"}</small></td><td>{row.state==="streaming" ? "输出中" : active(row) ? "进行中" : row.status===499 ? "客户端断开" : row.status>=400 ? "失败" : "已完成"}</td><td>{known ? (row.input_tokens+row.output_tokens).toLocaleString("zh-CN") : active(row) ? "等待用量" : "未提供"}</td><td>{(row.duration_ms/1000).toFixed(2)} 秒</td><td>{costKnown ? `$${(row.cost_micros/1e6).toFixed(6)}` : active(row) ? "等待核算" : "未知"}</td><td><button className="text-button" aria-label={`查看请求 ${row.request_id}`} onClick={()=>setSelected(row.id)}>查看</button></td></tr>})}
    {!rows.length && <tr><td colSpan={7}>暂无请求记录</td></tr>}
  </tbody></table></div>{selected!==null && <Detail key={selected} id={selected} dialog={Dialog} close={()=>setSelected(null)}/>}</>;
}

export default function RequestHistory({ channels, dialog }: { channels: ChannelRow[]; dialog: DialogComponent }) {
  const [summaryOnly, setSummaryOnly] = useState(false), [page, setPage] = useState(1);
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [state, setState] = useState("");
  const [version, setVersion] = useState(0);
  const [data, setData] = useState<RequestListResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    if (draft.trim() === query) return;
    const timer = setTimeout(() => {setQuery(draft.trim());setPage(1);}, 250);
    return () => clearTimeout(timer);
  }, [draft, query]);
  useEffect(() => {
    let stopped = false;
    let pending = false;
    async function load() {
      if (pending) return;
      pending = true;
      setLoading(true);
      try {
        const params = new URLSearchParams({ q: query, state, ...(summaryOnly ? {page: String(page)} : {}) });
        const value = await api<RequestListResult>(`/${summaryOnly ? "request-summaries" : "requests"}?${params}`);
        if (!stopped) { setData(value); setError(""); }
      } catch (error) { if (!stopped) setError((error as Error).message); }
      finally { pending = false; if (!stopped) setLoading(false); }
    }
    void load();
    const timer = setInterval(() => { if (!document.hidden) void load(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
  }, [query, state, version, summaryOnly, page]);
  return <>
    <div className="balance-period" role="group" aria-label="请求记录来源">{[[false,"近期与正文"],[true,"历史摘要"]].map(([flag,label]) => <button key={String(flag)} aria-pressed={summaryOnly === flag} onClick={() => {setSummaryOnly(Boolean(flag));setPage(1);setState("");setData(null);}}>{label}</button>)}</div>
    <div className="list-toolbar request-toolbar">
      <div className="search"><Search size={16} /><input aria-label="搜索请求记录" placeholder="搜索模型、渠道、密钥名称或请求编号" value={draft} onChange={event => setDraft(event.target.value)} /></div>
      <select aria-label="筛选请求状态" value={state} onChange={event => {setState(event.target.value);setPage(1);}}>
        <option value="">全部状态</option>{!summaryOnly && <option value="running">进行中</option>}<option value="completed">已完成</option><option value="failed">失败</option><option value="cancelled">客户端断开</option>
      </select>
      <button className="secondary" type="button" disabled={loading} onClick={() => setVersion(value => value + 1)}><RefreshCw size={14} />刷新请求记录</button>
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="panel request-history-panel">
      <div className="panel-heading"><div><h2 className="icon-heading"><FileText size={18} aria-hidden="true" />{summaryOnly ? "历史请求摘要" : "近期请求"}</h2><p>{summaryOnly ? "摘要跨重启保留，不含正文或上游错误原文；默认 7 天、5000 条，系统设置可调整。" : "已结束请求及正文最多缓存 50 条、32 MiB，进行中请求另计。正文重启后清空，摘要和用量统计保留。"}</p></div><span>共 {count(data?.total)} 条 · 进行中 {count(data?.active)} 条</span></div>
      <RequestList key={String(summaryOnly)} rows={data?.items || []} channels={channels} dialog={dialog} summaryOnly={summaryOnly} />
      <div className="request-pagination">
        {summaryOnly && <><button className="secondary" disabled={page <= 1 || loading} onClick={() => setPage(current => current - 1)}>上一页</button><span>第 {page} 页 · 每页 50 条</span><button className="secondary" disabled={page * 50 >= (data?.total || 0) || loading} onClick={() => setPage(current => current + 1)}>下一页</button></>}
        <span>每 2 秒自动刷新{loading ? " · 正在读取…" : ""}</span>
      </div>
    </div>
  </>;
}
