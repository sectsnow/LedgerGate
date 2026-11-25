import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, Coins, Hash, CircleAlert } from "lucide-react";
import type { Data } from "./api";
import { AccountFinance, RequestActivity } from "./DashboardParts";
import { Stat, compact, money } from "./WorkspaceStats";
import RequestTrend from "./RequestTrend";
import RequestQuality from "./RequestQuality";
import { ANALYSIS_PERIOD_KEY, analysisSelection, readAnalysisPeriod, type AnalysisPeriod } from "./analysisPeriod";

export default function Analytics({data,tab,navigate}:{data:Data;tab:string;navigate:(page:string)=>void}) {
  const [period,setPeriod]=useState<AnalysisPeriod>(()=>{try{return readAnalysisPeriod(localStorage.getItem(ANALYSIS_PERIOD_KEY));}catch{return 1;}});
  const restored=useRef(false);
  const restoreScroll=useCallback(()=>{if(restored.current)return;restored.current=true;let position=0;try{position=Number(sessionStorage.getItem(`ledgergate-analysis-scroll-${tab}`))||0;}catch{/* 禁用存储时从顶部显示。 */}requestAnimationFrame(()=>window.scrollTo(0,Math.max(0,position)));},[tab]);
  const selected=analysisSelection(data.overview,period,Date.now());
  const first=data.overview.activity[0]?.date||selected.lastDate;
  const [customOpen,setCustomOpen]=useState(false),[start,setStart]=useState(selected.start),[end,setEnd]=useState(selected.end),[error,setError]=useState("");
  useEffect(()=>{try{localStorage.setItem(ANALYSIS_PERIOD_KEY,JSON.stringify(period));}catch{/* 当前页面仍可切换。 */}},[period]);
  const view=tab==="cache" ? "cache" : tab==="stability" ? "stability" : tab==="cost" ? "cost" : "overview";
  const totals=selected.totals;
  return <>
    <div className="analysis-toolbar"><div><h2>{selected.label}</h2><p>网关请求、Token 与估算费用使用同一时段 · 北京时间</p></div><div className="balance-period" role="group" aria-label="网关用量统计时段">
      {(["today",1,7,14,30] as const).map(value=><button key={value} aria-pressed={period===value} onClick={()=>{setPeriod(value);setCustomOpen(false);setError("");}}>{value==="today" ? "今天" : value===1 ? "近24个小时" : `${value}天`}</button>)}
      <button aria-expanded={customOpen} aria-pressed={typeof period==="object"} onClick={()=>setCustomOpen(value=>!value)}>自定义</button>
    </div></div>
    {customOpen && <form className="request-date-range" onSubmit={event=>{event.preventDefault();if(!start || !end || start>end || start<first || end>selected.lastDate){setError(`请选择 ${first} 至 ${selected.lastDate} 内的有效日期范围。`);return;}setPeriod({start,end});setError("");}}>
      <label className="field">开始日期<input type="date" required min={first} max={selected.lastDate} value={start} onChange={event=>setStart(event.target.value)} /></label>
      <label className="field">结束日期<input type="date" required min={first} max={selected.lastDate} value={end} onChange={event=>setEnd(event.target.value)} /></label>
      <button className="secondary" type="submit">应用</button>{error && <p className="form-error" role="alert">{error}</p>}
    </form>}
    <div className="stats-grid">
      <Stat label="所选时段请求" tone="traffic" value={compact(totals.requests)} icon={<Activity size={18}/>} note={`${totals.errors} 次失败／取消`} />
      <Stat label="所选时段 Token" tone="usage" value={compact(totals.input_tokens+totals.output_tokens)} icon={<Hash size={18}/>} note={`输入 ${compact(totals.input_tokens)} · 输出 ${compact(totals.output_tokens)}`} />
      <Stat label="所选时段费用估算" tone="cost" value={totals.requests>0 && totals.cost_unknown_requests>=totals.requests ? "—" : `$${money(totals.cost_micros/1e6)}`} icon={<Coins size={18}/>} note={`${totals.cost_unknown_requests} 次费用未知，未计入估算`} />
      <Stat label="所选时段错误率" tone="traffic" value={totals.requests ? `${(totals.errors/totals.requests*100).toFixed(1)}%` : "—"} icon={<CircleAlert size={18}/>} note="按已结束请求统计，包含失败与客户端断开" />
    </div>
    {typeof period!=="object" && period!==1 && period!=="today" && data.settings.request_summary_retention_days<period && <p className="field-hint">汇总保留 {data.settings.request_summary_retention_days} 天，所选时段可能不完整。</p>}
    {(tab==="summary" || tab==="usage") && <section className="panel traffic analytics-trend"><div className="panel-heading"><div><h2>请求与用量趋势</h2><p>{selected.label} · {period===1 || period==="today" ? "按小时汇总" : "按自然日汇总"}</p></div></div><RequestTrend points={selected.points}/></section>}
    {tab==="usage" && <RequestActivity days={data.overview.activity} retentionDays={data.settings.request_summary_retention_days}/>}
    {tab==="cost" && <><div className="section-heading"><div><h2>上游资产与实际消费</h2><p className="field-hint">余额为当前采集快照，今日消费以采集结果为准；下方上游历史按各自自然日展示，与网关估算分开核对。</p></div></div><AccountFinance data={data}/></>}
    {tab!=="usage" && <RequestQuality view={view} range={selected.range} refreshed={data.overview} onLoaded={restoreScroll} onDetail={selection=>{
      try{sessionStorage.setItem(`ledgergate-analysis-scroll-${tab}`,String(window.scrollY));}catch{/* 禁用存储时仍可打开详情。 */}
      const params=new URLSearchParams(selected.range);params.set("group_by",selection.group);params.set("model",selection.model);params.set("detail",selection.id);params.set("name",selection.name);params.set("include_insights","1");
      navigate(`analytics/${tab}/detail?${params}`);
    }}/>}
  </>;
}
