import { cacheTrendPlot, insightDuration, insightMetrics, insightMoney, insightPercent, type AdvancedReport } from "../../web/src/insightMetrics";
import OverviewSection from "./OverviewSection";
import type { SectionId } from "./reading";
import type { DashboardPage } from "./dashboard";

export default function AdvancedInsights({ report, requests, collapsed, onCollapse, openDashboard }: { report: AdvancedReport | null | undefined; requests: number; collapsed: SectionId[]; onCollapse: (id: SectionId, collapsed: boolean) => void; openDashboard: (page: DashboardPage) => void }) {
  if (!report) return <p className="detail-line">服务端未提供延迟分布、重试挽回、错误分类、缓存成本对照和命中率趋势，请同步升级服务端。</p>;
  const sectionProps = {collapsible: true, nested: true, onCollapse};
  const metrics = insightMetrics(report, requests);
  const chart = cacheTrendPlot(report.trend);
  return <div className="advanced-insights">
    <OverviewSection {...sectionProps} id="latency" title="延迟分布与重试" collapsed={collapsed.includes("latency")}>
    <div className="analysis-grid">
      <div><span>总耗时 P50 ≈</span><strong>{insightDuration(report.duration.p50_ms)}</strong></div>
      <div><span>总耗时 P95 ≈</span><strong>{insightDuration(report.duration.p95_ms)}</strong></div>
      <div><span>首字 P50 ≈</span><strong>{insightDuration(report.first.p50_ms)}</strong></div>
      <div><span>首字 P95 ≈</span><strong>{insightDuration(report.first.p95_ms)}</strong></div>
      <div><span>重试挽回率</span><strong>{insightPercent(metrics.retryRate)}</strong></div>
      <div><span>重试额外等待 · 平均</span><strong>{insightDuration(metrics.waitAverage)}</strong></div>
    </div>
    <p className="detail-line">分布覆盖 {insightPercent(metrics.coverage)} · 总耗时 {report.duration.samples}、首字 {report.first.samples} 个样本。P50/P95 为分桶上界近似值，桶内误差小于 6.25%。</p>
    <p className="detail-line">首次失败且未取消 {report.retry_eligible} 次，重试挽回 {report.retry_rescued} 次；分母包含未重试的首次失败。额外等待合计 {insightDuration(report.wait_ms)}，平均以 {report.wait_samples} 个尝试样本计算。</p>
    </OverviewSection>
    <OverviewSection {...sectionProps} id="cache-cost" title="缓存净节省估算" collapsed={collapsed.includes("cache-cost")}>
    <div className="analysis-grid">
      <div><span>净节省 · 负值为净增费用</span><strong>{insightMoney(metrics.savings)}</strong></div>
      <div><span>估算样本覆盖率</span><strong>{insightPercent(metrics.savingsCoverage)}</strong></div>
      <div><span>全部按普通输入估算</span><strong>{insightMoney(report.savings_samples ? report.baseline_micros / 1e6 : null)}</strong></div>
      <div><span>实际缓存读写计价</span><strong>{insightMoney(report.savings_samples ? report.actual_micros / 1e6 : null)}</strong></div>
    </div>
    <p className="detail-line">以请求当时的有效单价与倍率估算输入成本，包含写入成本；字段不完整时不计入，共 {report.savings_samples} 个样本，不代表上游实际账单。</p>
    <button type="button" className="section-open" onClick={()=>openDashboard("analytics/cost")}>在控制台查看费用分析 →</button>
    </OverviewSection>
    <OverviewSection {...sectionProps} id="error-breakdown" title="错误分类分布" collapsed={collapsed.includes("error-breakdown")}>
    {report.errors.length ? <ul className="insight-error-list">{report.errors.map(item => <li key={item.name}><span>{item.name}</span><strong>{item.count} 次</strong></li>)}</ul> : <p className="detail-line">{report.samples ? "当前样本没有错误。" : "尚无分类样本，旧历史不补造。"}</p>}
    <button type="button" className="section-open" onClick={()=>openDashboard("requests")}>在控制台查看请求记录 →</button>
    </OverviewSection>
    <OverviewSection {...sectionProps} id="cache-comparison" title="缓存与延迟对比" collapsed={collapsed.includes("cache-comparison")}>
    <p className="detail-line">成功请求按同渠道、同模型、同流式模式分组；不控制输入长度等因素，不能推断缓存造成速度差异。展示前 20 组，共 {report.comparison_total} 组。</p>
    {report.comparisons.map(row => <details className="cache-comparison" key={`${row.channel_id}-${row.model}-${row.stream}`}><summary>{row.channel || `渠道 #${row.channel_id}`} · {row.model || "未指定模型"} · {row.stream ? "流式" : "非流式"}</summary>
      <p className="detail-line">命中 {row.hit.samples} 次 / 未命中 {row.miss.samples} 次</p>
      <p className="detail-line">平均总耗时：{insightDuration(row.hit.samples ? row.hit.duration_ms / row.hit.samples : null)} / {insightDuration(row.miss.samples ? row.miss.duration_ms / row.miss.samples : null)}</p>
      <p className="detail-line">平均首字：{insightDuration(row.hit.first_samples ? row.hit.first_ms / row.hit.first_samples : null)} / {insightDuration(row.miss.first_samples ? row.miss.first_ms / row.miss.first_samples : null)}；首字样本 {row.hit.first_samples} / {row.miss.first_samples} 次</p>
    </details>)}
    {!report.comparisons.length && <p className="detail-line">尚无对比样本。</p>}
    </OverviewSection>
    <OverviewSection {...sectionProps} id="cache-trend" title="缓存命中率趋势" collapsed={collapsed.includes("cache-trend")}>
    <svg className="cache-hit-trend" viewBox="0 0 520 134" role="img" aria-label="缓存命中率趋势，未知时段断开，纵轴 0 至 100%">
      {[0, 50, 100].map(value => <g key={value}><line x1="20" x2="500" y1={110 - value} y2={110 - value} /><text x="2" y={113 - value}>{value}</text></g>)}
      {chart.segments.map((path, index) => <path key={index} d={path} />)}
      {chart.points.map(point => point.rate != null && <circle key={point.date} cx={point.x} cy={110 - point.rate} r="3"><title>{point.date} · {insightPercent(point.rate)} · {point.samples} 个样本</title></circle>)}
    </svg>
    <p className="detail-line">{report.trend[0]?.date} — {report.trend.at(-1)?.date} · 无样本或无输入量的时段留空。</p>
    <details className="cache-comparison"><summary>查看缓存趋势数据</summary><ul className="insight-error-list">{chart.points.map(point => <li key={point.date}><span>{point.date} · {point.samples} 次</span><strong>{insightPercent(point.rate)}</strong></li>)}</ul></details>
    </OverviewSection>
  </div>;
}
