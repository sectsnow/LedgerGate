import { percentage, periodMetrics, type Period } from "./summary";
import AdvancedInsights from "./AdvancedInsights";
import OverviewSection from "./OverviewSection";
import type { SectionId } from "./reading";
import type { DashboardPage } from "./dashboard";

const count = (value: number) => value.toLocaleString("zh-CN");
export default function UsageInsights({ period, collapsed, onCollapse, openDashboard }: { period: Period; collapsed: SectionId[]; onCollapse: (id: SectionId, collapsed: boolean) => void; openDashboard: (page: DashboardPage) => void }) {
  const sectionProps = {collapsible: true, nested: true, onCollapse};
  const metrics = periodMetrics(period);
  const writeKnown = (period.cache_write_sample_requests ?? 0) > 0 || period.cache_write_tokens > 0;
  return <div className="usage-insights">
    <OverviewSection {...sectionProps} id="cache" title="缓存效率" collapsed={collapsed.includes("cache")}>
    <div className="analysis-grid">
      <div title="同批有效缓存样本的读取 Token 总量 ÷ 输入 Token 总量，不平均单次百分比。"><span>缓存 Token 命中率</span><strong>{percentage(metrics.cacheHitRate)}</strong></div>
      <div title="缓存读取大于 0 的请求数 ÷ 明确提供缓存读取字段的有效请求数。"><span>命中请求占比</span><strong>{percentage(metrics.cacheRequestRate)}</strong></div>
      <div><span>缓存读取 Token</span><strong>{period.cache_sample_requests || period.cache_read_tokens > 0 ? count(period.cache_read_tokens) : "—"}</strong></div>
      <div><span>缓存写入 Token</span><strong>{writeKnown ? count(period.cache_write_tokens) : "—"}</strong></div>
      <div><span>未缓存输入 · 有效样本</span><strong>{metrics.uncachedInput == null ? "—" : count(metrics.uncachedInput)}</strong></div>
      <div><span>缓存有效输入样本量</span><strong>{period.cache_sample_requests ? count(period.cache_sample_input_tokens ?? 0) : "—"}</strong></div>
      <div><span>缓存读取覆盖率</span><strong>{percentage(metrics.cacheCoverage)}</strong></div>
      <div><span>缓存写入覆盖率</span><strong>{percentage(metrics.writeCoverage)}</strong></div>
      <div><span>写入 · 5 分钟</span><strong>{writeKnown && period.cache_write_1h_tokens != null ? count(Math.max(0, period.cache_write_tokens - period.cache_write_1h_tokens)) : "—"}</strong></div>
      <div><span>写入 · 1 小时</span><strong>{writeKnown && period.cache_write_1h_tokens != null ? count(period.cache_write_1h_tokens) : "—"}</strong></div>
    </div>
    <p className="detail-line">缓存读取样本 {period.cache_sample_requests == null ? "未记录" : `${count(period.cache_sample_requests)}/${count(period.requests)} 次`}；未提供的字段不算零命中。命中率仅代表有效样本，历史读取/写入合计可能包含样本计数启用前的用量。</p>
    <button type="button" className="section-open" onClick={()=>openDashboard("analytics/cache")}>在控制台查看缓存分析 →</button>
    </OverviewSection>
    <OverviewSection {...sectionProps} id="quality" title="请求质量与费用" collapsed={collapsed.includes("quality")}>
    <div className="analysis-grid">
      <div title="首次即成功的请求数 ÷ 有尝试统计的请求数。"><span>首次成功率</span><strong>{percentage(metrics.firstSuccessRate)}</strong></div>
      <div><span>重试后成功</span><strong>{period.quality?.observed_requests ? `${count(period.quality.retried_successes)} 次` : "—"}</strong></div>
      <div><span>未成功 · 统计样本</span><strong>{period.quality?.observed_requests ? `${count(period.quality.failed_requests)} 次` : "—"}</strong></div>
      <div><span>尝试统计覆盖率</span><strong>{percentage(metrics.qualityCoverage)}</strong></div>
      <div><span>费用核算覆盖率</span><strong>{percentage(metrics.costCoverage)}</strong></div>
      <div title="已知费用总额 ÷ 费用已知的请求数；不包含费用未知的请求。"><span>平均费用 / 请求</span><strong>{metrics.averageCost == null ? "—" : `$${metrics.averageCost.toLocaleString("en-US", { minimumFractionDigits: 4, maximumFractionDigits: 6 })}`}</strong></div>
    </div>
    <p className="detail-line">尝试统计样本 {period.quality ? `${count(period.quality.observed_requests)}/${count(period.requests)} 次` : "未记录"}；费用为网关估算，以上游实际账单为准。</p>
    <div className="section-links"><button type="button" className="section-open" onClick={()=>openDashboard("analytics/stability")}>查看稳定性 →</button><button type="button" className="section-open" onClick={()=>openDashboard("analytics/cost")}>查看费用分析 →</button></div>
    </OverviewSection>
    <AdvancedInsights report={period.insights} requests={period.requests} collapsed={collapsed} onCollapse={onCollapse} openDashboard={openDashboard} />
  </div>;
}
