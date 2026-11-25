import { useEffect, useState } from "react";
import { api } from "./api";
import type { AdvancedReport } from "./insightMetrics";
import RequestInsights from "./RequestInsights";

export default function AnalysisDetail({page,revision}:{page:string;revision:unknown}) {
  const [report,setReport]=useState<AdvancedReport|null>(null),[error,setError]=useState("");
  const [path,query=""]=page.split("?",2);
  const params=new URLSearchParams(query);
  const title=params.get("name")||params.get("detail")||"所选对象";
  useEffect(()=>{
    const controller=new AbortController();setReport(null);setError("");
    const search=new URLSearchParams(query);search.set("include_insights","1");search.delete("name");
    api<{insights?:AdvancedReport}>(`/request-quality?${search}`,"GET",undefined,controller.signal).then(result=>{if(!controller.signal.aborted){setReport(result.insights??null);if(!result.insights)setError("服务端未提供详细统计，请同步升级服务端。");}}).catch(error=>{if(!controller.signal.aborted)setError(error.message);});
    return()=>controller.abort();
  },[query,revision]);
  return <><a className="text-button analysis-back" href={`#${path.replace(/\/detail$/,"")}`}>返回统计列表</a><section className="panel analysis-detail"><div className="panel-heading"><div><h2>{title} · 详细分析</h2><p>{params.get("model") ? `统一模型：${params.get("model")} · ` : ""}{params.get("start") ? `${params.get("start")} 至 ${params.get("end")}` : "近 24 个小时"} · 仅统计有记录的样本</p></div></div>{error ? <p className="quality-note form-error" role="alert">{error}</p> : report ? <RequestInsights report={report} requests={report.requests??0}/> : <p className="quality-note" role="status">正在读取详细统计…</p>}</section></>;
}
