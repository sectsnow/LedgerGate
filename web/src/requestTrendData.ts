export type RequestTrendPoint = {
  label: string;
  title: string;
  requests: number;
  errors?: number;
  input_tokens?: number;
  output_tokens?: number;
  cost_micros?: number;
  cost_unknown_requests?: number;
};
export type RequestTrendMetric = "requests" | "tokens" | "cost";
type TrendKey = "requests" | "errors" | "input_tokens" | "output_tokens" | "cost_micros";
export const requestTrendMetrics: Record<RequestTrendMetric, { label: string; series: { key: TrendKey; label: string; color: string; dashed?: boolean }[] }> = {
  requests: { label: "请求量", series: [
    { key: "requests", label: "总请求", color: "var(--data-traffic)" },
    { key: "errors", label: "失败／取消", color: "var(--danger)", dashed: true },
  ] },
  tokens: { label: "Token 用量", series: [
    { key: "input_tokens", label: "输入 Token", color: "var(--data-usage)" },
    { key: "output_tokens", label: "输出 Token", color: "var(--data-balance)", dashed: true },
  ] },
  cost: { label: "费用估算", series: [
    { key: "cost_micros", label: "估算费用", color: "var(--data-cost)" },
  ] },
};

export function requestTrendValue(point: RequestTrendPoint, key: TrendKey): number | null {
  // 全部请求费用未知时留空，避免把未计费的小计画成确定的零费用。
  if (key === "cost_micros" && (point.cost_unknown_requests === undefined || (point.requests > 0 && point.cost_unknown_requests >= point.requests))) return null;
  return point[key] ?? null;
}

export function requestTrendFormat(metric: RequestTrendMetric, value: number): string {
  return metric === "cost" ? `$${(value / 1e6).toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 6 })}` : value.toLocaleString("zh-CN");
}
