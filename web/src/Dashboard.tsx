import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { Activity, Coins, Hash, Network, Receipt, Wallet } from "lucide-react";
import { channelEnabled, type Data } from "./api";
import { Stat, compact, financialAmount, money } from "./WorkspaceStats";
import RequestTrend from "./RequestTrend";
import { RequestPreview } from "./RequestHistory";
import { analysisSelection } from "./analysisPeriod";
import { dashboardIssues } from "./dashboardIssues";

export default function Dashboard({data,navigate,dialog,addAccount}:{data:Data;navigate:(page:string)=>void;dialog:ComponentType<{title:string;children:ReactNode;close:()=>void}>;addAccount:()=>void}) {
  const summaries=data.overview.account_summaries;
  const [currency,setCurrency]=useState(()=>{try{return localStorage.getItem("ledgergate-finance-currency")||"";}catch{return "";}});
  const summary=summaries.find(row=>row.currency===currency)||summaries[0];
  useEffect(()=>{if(summary)try{localStorage.setItem("ledgergate-finance-currency",summary.currency);}catch{/* 禁用存储时保留本页选择。 */}},[summary?.currency]);
  const now=Date.now(),selected=analysisSelection(data.overview,"today",now),totals=selected.totals;
  const enabled=data.channels.filter(channelEnabled),available=enabled.filter(row=>Date.parse(row.cooldown_until)<=now).length;
  const monitored=data.accounts.filter(row=>row.account.enabled),healthy=monitored.filter(row=>row.account.status==="ok" && !row.account.warnings).length;
  const issues=dashboardIssues(data,now);
  return <div className="dashboard">
    <div className="dashboard-context"><span>今日 · {new Date(now+8*3600000).toISOString().slice(0,10)} · 北京时间</span><div className="inline-actions">
      {summaries.length>1 && <label>账户币种 <select aria-label="概览账户币种" value={summary?.currency||""} onChange={event=>setCurrency(event.target.value)}>{summaries.map(row=><option key={row.currency} value={row.currency}>{row.currency||"未设置单位"}</option>)}</select></label>}
      <a className="text-button" href="#analytics/summary">查看完整统计 →</a>
    </div></div>
    <div className="stats-grid dashboard-stats">
      <Stat label="上游账户余额" tone="balance" icon={<Wallet size={18}/>} value={financialAmount(summary?.balance??null,summary?.currency||"")} note={`当前采集快照 · 已采集 ${summary?.balance_accounts??0}/${summary?.accounts??0}`} />
      <Stat label="今日上游实际消费" tone="cost" icon={<Receipt size={18}/>} value={financialAmount(summary?.today_used??null,summary?.currency||"")} note={`已采集 ${summary?.today_accounts??0}/${summary?.accounts??0} · ${summary?.today_collected_at ? new Date(summary.today_collected_at).toLocaleTimeString("zh-CN",{hour:"2-digit",minute:"2-digit",hour12:false,timeZone:"Asia/Shanghai"})+" 采集（非实时）" : "等待成功采集"}`} />
      <Stat label="今日请求" tone="traffic" icon={<Activity size={18}/>} value={compact(totals.requests)} note={`${totals.errors} 次失败／取消 · 仅已结束请求`} />
      <Stat label="今日 Token" tone="usage" icon={<Hash size={18}/>} value={compact(totals.input_tokens+totals.output_tokens)} note={`输入 ${compact(totals.input_tokens)} · 输出 ${compact(totals.output_tokens)}`} />
      <Stat label="今日网关费用估算" tone="cost" icon={<Coins size={18}/>} value={totals.requests>0 && totals.cost_unknown_requests>=totals.requests ? "—" : `$${money(totals.cost_micros/1e6)}`} note={`${totals.cost_unknown_requests} 次费用未知 · 与上游实际消费分开核对`} />
      <Stat label="可用渠道" tone="balance" icon={<Network size={18}/>} value={`${available} / ${enabled.length}`} note={`监控账户健康 ${healthy}/${monitored.length} · 暂停项不计异常`} />
    </div>
    {!data.accounts.length && !data.channels.length && <section className="panel dashboard-onboarding"><h2>开始接入 LedgerGate</h2><p>接入上游账户或直连渠道，再配置模型路由和网关密钥。</p><div className="inline-actions"><button className="primary" onClick={addAccount}>接入上游账户</button><button className="secondary" onClick={()=>navigate("channels")}>配置直连渠道</button><button className="secondary" onClick={()=>navigate("keys")}>客户端接入说明</button></div></section>}
    <div className="dashboard-middle">
      <section className="panel traffic"><div className="panel-heading"><div><h2>今日请求与用量</h2><p>北京时间零点至当前 · 按小时汇总</p></div></div><RequestTrend points={selected.points}/></section>
      <section className="panel dashboard-attention"><div className="panel-heading"><h2>需要处理的问题</h2><span>{issues.length ? `${issues.length} 项` : "状态正常"}</span></div>
        {issues.length ? <ul>{issues.slice(0,5).map(issue=><li key={issue.id}><button onClick={()=>navigate(issue.page)}><strong>{issue.title}</strong><span>{issue.detail}</span></button></li>)}</ul> : <p className="dashboard-healthy">当前没有需要处理的账户、渠道或通知异常。</p>}
        {issues.length>5 && <p className="field-hint dashboard-more">显示前 5 项，其余可进入账户、渠道和监控中心查看。</p>}
        <div className="dashboard-shortcuts"><a href="#accounts">上游账户</a><a href="#channels">转发渠道</a><a href="#monitor/alerts">监控中心</a></div>
      </section>
    </div>
    <section className="panel dashboard-requests"><div className="panel-heading"><div><h2>最近请求</h2><p>进行中优先 · 展示最近 5 条</p></div><a className="text-button" href="#requests">查看全部 →</a></div><RequestPreview rows={data.requests.slice(0,5)} dialog={dialog}/></section>
  </div>;
}
