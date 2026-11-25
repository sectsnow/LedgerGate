import { accountNotes, type Overview } from "./summary.ts";

export type WidgetAlert = { tone: "danger" | "warning" | "info"; title: string; detail: string };
export function widgetAlerts(overview: Overview | null, days: number, error: string, lastSuccess: number | null, now: number) {
  const issues: WidgetAlert[] = [], notes: WidgetAlert[] = [];
  const today = new Date(now + 8 * 3600_000).toISOString().slice(0, 10);
  const snapshotStale = !!overview && (lastSuccess !== null && now - lastSuccess > 90_000 || overview.date !== today);
  const problemCount = overview?.accounts.filter(account => accountNotes(account, today, now).length).length ?? 0;
  if (error) issues.push({tone: "danger", title: "连接或操作异常", detail: error});
  if (problemCount) issues.push({tone: "warning", title: "账户状态提醒", detail: `${problemCount} 个监控中的账户存在低余额、未采集、超时或异常项目，可在账户资产中展开采集详情`});
  if (snapshotStale) issues.push({tone: "warning", title: "旧数据", detail: "超过 90 秒未成功获取或已跨日，当前展示上次快照"});
  const period = overview?.period?.days === days ? overview.period : null;
  const errorRate = period && period.requests > 0 ? `${(period.errors / period.requests * 100).toLocaleString("zh-CN", {maximumFractionDigits: 1})}%` : "—";
  if (period?.errors) notes.push({tone: "info", title: "周期请求错误", detail: `当前周期累计 ${period.errors.toLocaleString("zh-CN")} 次失败，错误率 ${errorRate}；历史失败不代表当前仍有故障`});
  if (period?.cost_unknown_requests) notes.push({tone: "info", title: "周期费用未完整核算", detail: `${period.cost_unknown_requests.toLocaleString("zh-CN")} 次请求缺少有效费用或用量信息，未计入费用估算`});
  if (period && period.retention_days < days) notes.push({tone: "info", title: "统计周期不完整", detail: `当前只保留 ${period.retention_days} 天汇总，无法覆盖完整的近 ${days} 天`});
  if (overview && !overview.period) notes.push({tone: "info", title: "服务版本较旧", detail: "升级 LedgerGate 后可获取趋势、分析和排行数据"});
  return {issues, notes, snapshotStale};
}
