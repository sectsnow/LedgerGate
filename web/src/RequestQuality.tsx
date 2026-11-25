import { useEffect, useState } from "react";
import { ChartNoAxesCombined } from "lucide-react";
import { api, type Overview } from "./api";
import { qualityAdvice } from "./qualityAdvice";

type Row = { id: string; name: string; requests: number; successes: number; input_tokens: number; output_tokens: number; cost_micros: number; unknown_costs: number; average_duration_ms: number | null; average_first_token_ms: number | null; output_per_second: number | null; observed_requests: number; first_attempt_successes: number; retried_successes: number; failed_requests: number; attempt_overhead_ms: number; last_request_unix: number };
export default function RequestQuality({ range, refreshed }: { range: string; refreshed: Overview }) {
  const [group, setGroup] = useState("channel"), [model, setModel] = useState(""), [sort, setSort] = useState("requests");
  const [models, setModels] = useState<string[]>([]), [rows, setRows] = useState<Row[] | null>(null), [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController(); setError(""); setRows(null);
    api<{items: Row[]; models: string[]}>(`/request-quality?group_by=${group}&model=${encodeURIComponent(model)}&${range}`, "GET", undefined, controller.signal)
      .then(result => { if (!controller.signal.aborted) {setRows(result.items); setModels(result.models || []);} })
      .catch(error => { if (!controller.signal.aborted) setError(error.message); });
    return () => controller.abort();
  }, [group, model, range, refreshed]);
  const duration = (value: number | null) => value == null ? "未记录" : `${(value / 1000).toFixed(2)} 秒`;
  const score = (row: Row) => sort === "success" ? row.successes / Math.max(1, row.requests) : sort === "latency" ? -(row.average_first_token_ms ?? Infinity) : sort === "cost" ? (row.unknown_costs === 0 && row.requests ? -row.cost_micros / row.requests : -Infinity) : row.requests;
  const ordered = rows && [...rows].sort((a, b) => score(b) - score(a));
  return <section className="panel quality-panel">
    <div className="panel-heading"><div><h2 className="icon-heading"><ChartNoAxesCombined size={18} aria-hidden="true" />渠道与模型表现</h2><p>与网关用量使用相同时段，保留原有调度。重试成功请求的完整耗时包含之前的等待。</p></div>
      <div className="balance-period" role="group" aria-label="表现统计维度">{[["channel", "渠道"], ["model", "模型"]].map(([value, label]) => <button key={value} aria-pressed={group === value} onClick={() => setGroup(value)}>{label}</button>)}</div>
    </div>
    <div className="list-toolbar"><label>统一模型 <select aria-label="比较统一模型" value={model} onChange={event => setModel(event.target.value)}><option value="">全部模型</option>{[...new Set([...models, ...(model ? [model] : [])])].map(name => <option key={name} value={name}>{name}</option>)}</select></label><label>排序 <select aria-label="渠道表现排序" value={sort} onChange={event => setSort(event.target.value)}><option value="requests">请求数</option><option value="success">成功率</option><option value="latency">平均首字</option><option value="cost">每请求费用</option></select></label></div>
    {group === "channel" && rows && <p className="quality-note">{qualityAdvice(rows, model)}</p>}
    {error ? <p className="quality-note form-error" role="alert">{error}</p> : <div className="table-scroll"><table><thead><tr><th>{group === "channel" ? "渠道" : "模型"}</th><th>样本与成功率</th><th>首次／重试成功／未成功</th><th>平均总耗时</th><th>平均首字</th><th>平均 TPS</th><th>费用与 Token</th><th>最近使用</th></tr></thead><tbody>
      {ordered?.map(row => <tr key={row.id}><td>{row.name}</td><td>{row.requests} 次 · {(row.successes / Math.max(1, row.requests) * 100).toFixed(1)}%{row.requests < 20 && <small className="table-sub">样本较少</small>}</td><td>{row.observed_requests ? `${row.first_attempt_successes} / ${row.retried_successes} / ${row.failed_requests}` : "未记录"}<small className="table-sub">{row.observed_requests || 0}/{row.requests} 次有尝试统计</small>{row.observed_requests > 0 && <small className="table-sub">重试前耗时合计 {duration(row.attempt_overhead_ms)}</small>}</td><td>{duration(row.average_duration_ms)}</td><td>{duration(row.average_first_token_ms)}</td><td>{row.output_per_second == null ? "未记录" : `${row.output_per_second.toFixed(1)} Token/秒`}</td><td>{row.unknown_costs >= row.requests ? "—" : `$${(row.cost_micros / 1e6).toFixed(4)}`}<small className="table-sub">{(row.input_tokens + row.output_tokens).toLocaleString("zh-CN")} Token</small><small className="table-sub">{row.unknown_costs ? `${row.unknown_costs} 次费用未知，未计入` : `平均 $${(row.cost_micros / Math.max(1, row.requests) / 1e6).toFixed(6)}/请求`}</small></td><td>{row.last_request_unix ? new Date(row.last_request_unix * 1000).toLocaleString("zh-CN", {hour12: false}) : "旧记录未提供"}</td></tr>)}
      {!ordered?.length && <tr><td colSpan={8}>{ordered ? "所选时段暂无已结束请求" : "正在读取…"}</td></tr>}
    </tbody></table></div>}
    <p className="quality-note">取消计入未成功请求；记录归属最终渠道，不能据此推断每个被尝试渠道的成功率。平均 TPS 为输出 Token ÷ 完整耗时之和；平均首字仅统计新语义口径。每请求费用受工作负载影响，不代表模型单价。最多展示请求量最高的 500 项。</p>
  </section>;
}
