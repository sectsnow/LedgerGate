import { useEffect, useRef, useState } from "react";
import { ChartNoAxesCombined } from "lucide-react";
import { api, type Overview } from "./api";
import { qualityAdvice } from "./qualityAdvice";
import { qualityMetrics, qualityPreferences, type QualityMetricsRow } from "./qualityMetrics";

type Row = QualityMetricsRow & { id: string; name: string; successes: number; input_tokens: number; output_tokens: number; average_duration_ms: number | null; average_first_token_ms: number | null; output_per_second: number | null; retried_successes: number; failed_requests: number; attempt_overhead_ms: number; last_request_unix: number; cache_read_tokens?: number; cache_write_tokens?: number; cache_write_1h_tokens?: number };
type View = "overview" | "cache" | "stability" | "cost";
const percent = (value: number | null) => value == null ? "未记录" : `${value.toFixed(1)}%`;
const count = (value: number | null | undefined) => value == null ? "未记录" : value.toLocaleString("zh-CN");
const money = (value: number | null) => value == null ? "—" : `$${value.toFixed(6)}`;
const recent = (row: Row) => row.last_request_unix ? new Date(row.last_request_unix * 1000).toLocaleString("zh-CN", { hour12: false }) : "旧记录未提供";
export default function RequestQuality({ range, refreshed, view, onDetail, onLoaded }: { range: string; refreshed: Overview; view:View; onDetail:(selection:{id:string;name:string;group:string;model:string})=>void; onLoaded:()=>void }) {
  const [prefs,setPrefs]=useState(()=>{try{return qualityPreferences(localStorage.getItem("ledgergate-quality-filters"));}catch{return qualityPreferences(null);}});
  const {group,model,sort}=prefs;
  useEffect(()=>{try{localStorage.setItem("ledgergate-quality-filters",JSON.stringify(prefs));}catch{/* 禁用存储时保留当前筛选。 */}},[prefs]);
  const [models, setModels] = useState<string[]>([]), [rows, setRows] = useState<Row[] | null>(null), [error, setError] = useState("");
  const previousQuery=useRef("");
  useEffect(() => {
    const controller = new AbortController(); setError("");
    const query=`${group}\0${model}\0${range}`;
    if(previousQuery.current!==query){setRows(null);previousQuery.current=query;}
    api<{items: Row[]; models: string[]}>(`/request-quality?group_by=${group}&model=${encodeURIComponent(model)}&${range}`, "GET", undefined, controller.signal)
      .then(result => { if (!controller.signal.aborted) {setRows(result.items); setModels(result.models || []);} })
      .catch(error => { if (!controller.signal.aborted) setError(error.message); })
      .finally(()=>{if(!controller.signal.aborted)onLoaded();});
    return () => controller.abort();
  }, [group, model, range, refreshed, onLoaded]);
  const duration = (value: number | null) => value == null ? "未记录" : `${(value / 1000).toFixed(2)} 秒`;
  const score = (row: Row) => sort === "success" ? row.successes / Math.max(1, row.requests) : sort === "latency" ? -(row.average_first_token_ms ?? Infinity) : sort === "cost" ? -(qualityMetrics(row).averageCost ?? Infinity) : sort === "cache" ? qualityMetrics(row).cacheHitRate ?? -Infinity : sort === "first" ? qualityMetrics(row).firstSuccessRate ?? -Infinity : row.requests;
  const ordered = rows && [...rows].sort((a, b) => score(b) - score(a));
  const name = group === "channel" ? "渠道" : "模型";
  const headers: Record<View, string[]> = {
    overview: [name, "样本与成功率", "首次／重试成功／未成功", "平均总耗时", "平均首字", "平均 TPS", "费用与 Token", "最近使用"],
    cache: [name, "缓存 Token 命中率", "命中请求占比", "读取／写入 Token", "未缓存输入 · 样本", "写入明细", "数据覆盖率", "有效读取样本"],
    stability: [name, "样本与成功率", "首次成功率", "重试成功／未成功", "重试额外等待", "尝试统计覆盖率", "最近使用"],
    cost: [name, "已知费用估算", "平均费用／请求", "费用核算覆盖率", "费用未知", "输入／输出 Token", "最近使用"],
  };
  return <section className="panel quality-panel">
    <div className="panel-heading"><div><h2 className="icon-heading"><ChartNoAxesCombined size={18} aria-hidden="true" />渠道与模型表现</h2><p>与网关用量使用相同时段，保留原有调度。重试成功请求的完整耗时包含之前的等待。</p></div>
      <div className="balance-period" role="group" aria-label="表现统计维度">{[["channel", "渠道"], ["model", "模型"]].map(([value, label]) => <button key={value} aria-pressed={group === value} onClick={() => setPrefs(previous=>({...previous,group:value}))}>{label}</button>)}</div>
    </div>
    <div className="quality-filters" role="group" aria-label="表现筛选"><label className="field">统一模型 <select aria-label="比较统一模型" value={model} onChange={event => setPrefs(previous=>({...previous,model:event.target.value}))}><option value="">全部模型</option>{[...new Set([...models, ...(model ? [model] : [])])].map(name => <option key={name} value={name}>{name}</option>)}</select></label><label className="field">排序 <select aria-label="渠道表现排序" value={sort} onChange={event => setPrefs(previous=>({...previous,sort:event.target.value}))}><option value="requests">请求数</option><option value="success">成功率</option><option value="latency">平均首字</option><option value="cost">每请求费用</option><option value="cache">缓存命中率</option><option value="first">首次成功率</option></select></label></div>
    {group === "channel" && rows && <p className="quality-note">{qualityAdvice(rows, model)}</p>}
    {error ? <p className="quality-note form-error" role="alert">{error}</p> : <div className="table-scroll"><table><thead><tr>{headers[view].map(label => <th key={label}>{label}</th>)}</tr></thead><tbody>
      {ordered?.map(row => {
        const metrics = qualityMetrics(row);
        const writeKnown = (row.cache_write_sample_requests ?? 0) > 0 || (row.cache_write_tokens ?? 0) > 0;
        return <tr key={row.id}><td>{row.name}<button type="button" className="quality-detail-link" onClick={() => onDetail({id:row.id,name:row.name,group,model})}>详细分析</button></td>
          {view === "overview" && <><td>{row.requests} 次 · {(row.successes / Math.max(1, row.requests) * 100).toFixed(1)}%{row.requests < 20 && <small className="table-sub">样本较少</small>}</td><td>{row.observed_requests ? `${row.first_attempt_successes} / ${row.retried_successes} / ${row.failed_requests}` : "未记录"}<small className="table-sub">{row.observed_requests || 0}/{row.requests} 次有尝试统计</small>{row.observed_requests > 0 && <small className="table-sub">重试前耗时合计 {duration(row.attempt_overhead_ms)}</small>}</td><td>{duration(row.average_duration_ms)}</td><td>{duration(row.average_first_token_ms)}</td><td>{row.output_per_second == null ? "未记录" : `${row.output_per_second.toFixed(1)} Token/秒`}</td><td>{row.unknown_costs >= row.requests ? "—" : `$${(row.cost_micros / 1e6).toFixed(4)}`}<small className="table-sub">{count(row.input_tokens + row.output_tokens)} Token</small><small className="table-sub">{row.unknown_costs} 次费用未知，未计入</small><small className="table-sub">已知样本平均 {money(metrics.averageCost)}/请求</small></td><td>{recent(row)}</td></>}
          {view === "cache" && <><td>{percent(metrics.cacheHitRate)}</td><td>{percent(metrics.cacheRequestRate)}</td><td>读取 {row.cache_sample_requests || (row.cache_read_tokens ?? 0) > 0 ? count(row.cache_read_tokens) : "未记录"}<small className="table-sub">写入 {writeKnown ? count(row.cache_write_tokens) : "未记录"}</small></td><td>{count(metrics.uncachedInput)}</td><td>5 分钟 {writeKnown && row.cache_write_1h_tokens != null ? count(Math.max(0, row.cache_write_tokens! - row.cache_write_1h_tokens)) : "未记录"}<small className="table-sub">1 小时 {writeKnown ? count(row.cache_write_1h_tokens) : "未记录"}</small></td><td>读取 {percent(metrics.cacheCoverage)}<small className="table-sub">写入 {percent(metrics.writeCoverage)}</small></td><td>{count(row.cache_sample_requests)}/{count(row.requests)} 次</td></>}
          {view === "stability" && <><td>{row.requests} 次 · {(row.successes / Math.max(1, row.requests) * 100).toFixed(1)}%</td><td>{percent(metrics.firstSuccessRate)}</td><td>{row.observed_requests ? `${row.retried_successes} / ${row.failed_requests}` : "未记录"}</td><td>{row.observed_requests ? duration(row.attempt_overhead_ms / row.observed_requests) : "未记录"}<small className="table-sub">统计样本平均；合计 {duration(row.attempt_overhead_ms)}</small></td><td>{percent(metrics.attemptCoverage)}<small className="table-sub">{row.observed_requests || 0}/{row.requests} 次</small></td><td>{recent(row)}</td></>}
          {view === "cost" && <><td>{row.unknown_costs >= row.requests ? "—" : `$${(row.cost_micros / 1e6).toFixed(4)}`}</td><td>{money(metrics.averageCost)}<small className="table-sub">仅费用已知样本</small></td><td>{percent(metrics.costCoverage)}</td><td>{row.unknown_costs} 次</td><td>输入 {count(row.input_tokens)}<small className="table-sub">输出 {count(row.output_tokens)}</small></td><td>{recent(row)}</td></>}
        </tr>;
      })}
      {!ordered?.length && <tr><td colSpan={headers[view].length}>{ordered ? "所选时段暂无已结束请求" : "正在读取…"}</td></tr>}
    </tbody></table></div>}
    <p className="quality-note">取消计入未成功请求；记录归属最终渠道，不能据此推断每个被尝试渠道的成功率。平均 TPS 为输出 Token ÷ 完整耗时之和；平均首字仅统计新语义口径。每请求费用受工作负载影响，不代表模型单价。最多展示请求量最高的 500 项。</p>
    {view === "cache" && <p className="quality-note">缓存 Token 命中率＝同批有效样本的读取 Token 总量 ÷ 输入 Token 总量；命中请求占比＝读取大于 0 的请求数 ÷ 有效读取样本数。未提供字段不算零命中；历史合计可能包含样本计数启用前的用量，命中率需结合覆盖率查看。</p>}
  </section>;
}
