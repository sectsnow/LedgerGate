import { cacheTrendPlot, insightDuration, insightMetrics, insightMoney, insightPercent, type AdvancedReport } from "./insightMetrics";

export default function RequestInsights({ report, requests }: { report: AdvancedReport; requests: number }) {
  const metrics = insightMetrics(report, requests);
  const chart = cacheTrendPlot(report.trend);
  return <section className="request-insights" aria-label="详细统计">
    <h3>延迟、重试与缓存成本</h3>
    <div className="insight-grid">
      <div><span>总耗时 P50 / P95 ≈</span><strong>{insightDuration(report.duration.p50_ms)} / {insightDuration(report.duration.p95_ms)}</strong><small>{report.duration.samples} 个样本</small></div>
      <div><span>首字 P50 / P95 ≈</span><strong>{insightDuration(report.first.p50_ms)} / {insightDuration(report.first.p95_ms)}</strong><small>{report.first.samples} 个语义首字样本</small></div>
      <div><span>重试挽回率</span><strong>{insightPercent(metrics.retryRate)}</strong><small>{report.retry_rescued}/{report.retry_eligible} 个首次失败请求</small></div>
      <div><span>重试额外等待 · 样本平均</span><strong>{insightDuration(metrics.waitAverage)}</strong><small>合计 {insightDuration(report.wait_ms)} · {report.wait_samples} 个样本</small></div>
      <div><span>缓存净节省估算</span><strong>{insightMoney(metrics.savings)}</strong><small>负值表示净增费用 · 覆盖 {insightPercent(metrics.savingsCoverage)}</small></div>
      <div><span>同批输入成本对照</span><strong>{insightMoney(report.savings_samples ? report.baseline_micros / 1e6 : null)} → {insightMoney(report.savings_samples ? report.actual_micros / 1e6 : null)}</strong><small>全部按普通输入 → 实际读写计价 · {report.savings_samples} 个样本</small></div>
    </div>
    <p className="insight-note">分布样本覆盖 {insightPercent(metrics.coverage)}。P50/P95 使用毫秒分桶上界近似值（桶内误差小于 6.25%）；仅覆盖有记录的样本。挽回率以首次失败且未取消的请求为分母，未重试的失败也计入。</p>
    <p className="insight-note">节省估算按请求当时有效单价与倍率计算，包含缓存写入成本；价格、倍率或缓存字段不完整时不计入。仅比较输入成本，不推算上游实际账单。</p>
    <h3>错误分类分布</h3>
    {report.errors.length ? <ul className="insight-errors">{report.errors.map(item => <li key={item.name}><span>{item.name}</span><strong>{item.count} 次</strong></li>)}</ul> : <p className="insight-note">{report.samples ? "当前记录样本中没有错误。" : "尚无分类样本，旧历史不补造。"}</p>}
    <h3>缓存命中与未命中的延迟对比</h3>
    <p className="insight-note">仅比较成功且明确提供缓存字段的请求；按同渠道、同模型、同流式模式拆分，首字仅统计语义口径。不控制输入长度等因素，不能据此认定速度差异由缓存造成。展示样本最多的前 20 组，共 {report.comparison_total} 组。</p>
    <div className="table-scroll"><table><thead><tr><th>渠道 / 模型</th><th>模式</th><th>命中样本</th><th>未命中样本</th><th>平均总耗时 · 命中 / 未命中</th><th>平均首字 · 命中 / 未命中</th></tr></thead><tbody>
      {report.comparisons.map(row => <tr key={`${row.channel_id}-${row.model}-${row.stream}`}><td>{row.channel || `渠道 #${row.channel_id}`} / {row.model || "未指定模型"}</td><td>{row.stream ? "流式" : "非流式"}</td><td>{row.hit.samples}<small className="table-sub">首字 {row.hit.first_samples}</small></td><td>{row.miss.samples}<small className="table-sub">首字 {row.miss.first_samples}</small></td><td>{insightDuration(row.hit.samples ? row.hit.duration_ms / row.hit.samples : null)} / {insightDuration(row.miss.samples ? row.miss.duration_ms / row.miss.samples : null)}</td><td>{insightDuration(row.hit.first_samples ? row.hit.first_ms / row.hit.first_samples : null)} / {insightDuration(row.miss.first_samples ? row.miss.first_ms / row.miss.first_samples : null)}</td></tr>)}
      {!report.comparisons.length && <tr><td colSpan={6}>尚无对比样本</td></tr>}
    </tbody></table></div>
    <h3>缓存命中率趋势</h3>
    <svg className="cache-trend-svg" viewBox="0 0 520 134" role="img" aria-label="缓存命中率趋势，未知时段断开，纵轴 0 至 100%">
      {[0, 50, 100].map(value => <g key={value}><line x1="20" x2="500" y1={110 - value} y2={110 - value} /><text x="2" y={113 - value}>{value}</text></g>)}
      {chart.segments.map((path, index) => <path key={index} d={path} />)}
      {chart.points.map(point => point.rate != null && <circle key={point.date} cx={point.x} cy={110 - point.rate} r="2.5"><title>{point.date} · {insightPercent(point.rate)} · {point.samples} 个样本</title></circle>)}
    </svg>
    <p className="insight-note">{report.trend[0]?.date} — {report.trend.at(-1)?.date} · 未提供缓存字段或无输入量的时段留空。</p>
    <details><summary>查看缓存趋势数据</summary><div className="table-scroll"><table><thead><tr><th>时段</th><th>命中率</th><th>有效样本</th></tr></thead><tbody>{chart.points.map(point => <tr key={point.date}><td>{point.date}</td><td>{insightPercent(point.rate)}</td><td>{point.samples}</td></tr>)}</tbody></table></div></details>
  </section>;
}
