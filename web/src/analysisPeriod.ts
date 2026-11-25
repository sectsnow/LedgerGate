import type { Overview } from "./api";
export type AnalysisPeriod = "today" | 1 | 7 | 14 | 30 | {start:string;end:string};
export const ANALYSIS_PERIOD_KEY="ledgergate-analysis-period";
export function readAnalysisPeriod(raw:string|null):AnalysisPeriod {
  try {
    const value=JSON.parse(raw??"null");
    if(value==="today" || [1,7,14,30].includes(value))return value;
    const valid=(date:unknown)=>typeof date==="string" && /^\d{4}-\d{2}-\d{2}$/.test(date) && Number.isFinite(Date.parse(date)) && new Date(date).toISOString().slice(0,10)===date;
    if(value && valid(value.start) && valid(value.end) && value.start<=value.end && Date.parse(value.end)-Date.parse(value.start)<=364*86400000)return {start:value.start,end:value.end};
  } catch { /* 禁用存储或旧设置损坏时使用默认时段。 */ }
  return 1;
}
export function analysisSelection(overview:Overview,period:AnalysisPeriod,now:number) {
  const activity=overview.activity||[];
  const today=new Date(now+8*3600000).toISOString().slice(0,10);
  const lastDate=activity.at(-1)?.date||today;
  const days=typeof period==="object" ? activity.filter(day=>day.date>=period.start && day.date<=period.end) : activity.slice(period==="today" ? -1 : -period);
  const totals=period===1 ? overview : days.reduce((sum,day)=>({requests:sum.requests+day.requests,errors:sum.errors+day.errors,input_tokens:sum.input_tokens+day.input_tokens,output_tokens:sum.output_tokens+day.output_tokens,cost_micros:sum.cost_micros+day.cost_micros,cost_unknown_requests:sum.cost_unknown_requests+day.cost_unknown_requests}),{requests:0,errors:0,input_tokens:0,output_tokens:0,cost_micros:0,cost_unknown_requests:0});
  const hours=Array.from({length:24},(_,i)=>Math.floor(now/3600000)*3600-(23-i)*3600).filter(hour=>period!=="today" || hour>=Date.parse(`${lastDate}T00:00:00+08:00`)/1000);
  const points=period===1 || period==="today" ? hours.map(hour=>({...overview.series?.find(row=>row.hour===hour)??{requests:0,errors:0,input_tokens:0,output_tokens:0,cost_micros:0,cost_unknown_requests:0},label:new Date(hour*1000).toLocaleTimeString("zh-CN",{timeZone:"Asia/Shanghai",hour:"2-digit",minute:"2-digit",hour12:false}),title:new Date(hour*1000).toLocaleString("zh-CN",{timeZone:"Asia/Shanghai",hour12:false})})) : days.map(day=>({...day,label:day.date.slice(5).replace("-","/"),title:day.date}));
  const start=typeof period==="object" ? period.start : days[0]?.date||lastDate;
  const end=typeof period==="object" ? period.end : lastDate;
  return {days,totals,points,lastDate,start,end,range:period===1 ? "" : `start=${start}&end=${end}`,label:typeof period==="object" ? `${period.start} 至 ${period.end}` : period==="today" ? "今日" : period===1 ? "近 24 个小时" : `近 ${period} 天`};
}
