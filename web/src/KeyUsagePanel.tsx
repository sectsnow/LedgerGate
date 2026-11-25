import { useEffect, useState } from "react";
import { api, type Key } from "./api";

export function KeyBudgetStatus({ item }: { item: Key }) {
  const money = (n: number) => `$${(n / 1e6).toFixed(4)}`;
  return <>{([
    ["今日", item.daily_used_micros ?? 0, item.daily_budget_micros, item.daily_reset_at],
    ["本月", item.monthly_used_micros ?? 0, item.monthly_budget_micros, item.monthly_reset_at],
  ] as const).map(([label, used, budget, reset]) => <small className="table-sub" key={label}>
    {label} {money(used)} / {budget == null ? "不限" : money(budget)}{budget != null && (budget === 0 || used >= budget * .8) && <strong className="form-error"> · {used >= budget ? "已达预算" : "接近预算"}</strong>}
    {budget != null && reset && <span> · {new Date(reset).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", hour12: false })} 重置</span>}
  </small>)}<small className="table-sub">并发 {item.active_requests ?? 0}/{item.max_concurrent || "不限"} · 每分钟 {item.requests_per_minute || "不限"} 次</small></>;
}
type Usage = {date: string; requests: number; cost_micros: number; unknown_costs: number};
export default function KeyUsagePanel({ item }: { item: Key }) {
  const [rows, setRows] = useState<Usage[] | null>(null), [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    api<{items: Usage[]}>(`/keys/${item.id}/usage`, "GET", undefined, controller.signal).then(value => setRows(value.items)).catch(error => { if (!controller.signal.aborted) setError(error.message); });
    return () => controller.abort();
  }, [item.id]);
  return <><KeyBudgetStatus item={item} /><p className="field-hint">最近 90 天，按北京时间请求开始日统计；仅包含启用周期统计后的请求，未知费用不计入预算。</p>{error && <p className="form-error" role="alert">{error}</p>}<div className="table-scroll"><table><thead><tr><th>日期</th><th>请求</th><th>费用估算</th><th>费用未知</th></tr></thead><tbody>{rows?.map(row => <tr key={row.date}><td>{row.date}</td><td>{row.requests}</td><td>{row.unknown_costs >= row.requests ? "—" : `$${(row.cost_micros / 1e6).toFixed(6)}`}</td><td>{row.unknown_costs}</td></tr>)}{!rows?.length && <tr><td colSpan={4}>{rows ? "暂无周期用量" : error ? "读取失败" : "正在读取…"}</td></tr>}</tbody></table></div></>;
}
