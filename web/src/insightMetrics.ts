export type LatencyPercentiles = { samples: number; p50_ms: number | null; p95_ms: number | null };
export type CacheLatency = { samples: number; duration_ms: number; first_samples: number; first_ms: number };
export type CacheTrendPoint = { date: string; samples: number; input_tokens: number; read_tokens: number };
export type AdvancedReport = {
  requests?: number;
  samples: number; duration: LatencyPercentiles; first: LatencyPercentiles;
  retry_eligible: number; retry_rescued: number; wait_samples: number; wait_ms: number;
  savings_samples: number; baseline_micros: number; actual_micros: number;
  errors: { name: string; count: number }[];
  comparisons: { channel_id: number; channel: string; model: string; stream: boolean; hit: CacheLatency; miss: CacheLatency }[];
  comparison_total: number; trend: CacheTrendPoint[];
};

export function insightMetrics(report: AdvancedReport, requests: number) {
  requests=report.requests??requests;
  const ratio = (value: number, total: number) => total > 0 && value >= 0 && value <= total ? value / total * 100 : null;
  return {
    coverage: ratio(report.samples, requests),
    retryRate: ratio(report.retry_rescued, report.retry_eligible),
    waitAverage: report.wait_samples > 0 ? report.wait_ms / report.wait_samples : null,
    savings: report.savings_samples > 0 ? (report.baseline_micros - report.actual_micros) / 1e6 : null,
    savingsCoverage: ratio(report.savings_samples, requests),
  };
}

export function cacheTrendPlot(rows: CacheTrendPoint[]) {
  const points = rows.map((row, index) => ({ ...row, x: rows.length <= 1 ? 260 : 20 + index * 480 / (rows.length - 1), rate: row.samples > 0 && row.input_tokens > 0 && row.read_tokens >= 0 && row.read_tokens <= row.input_tokens ? row.read_tokens / row.input_tokens * 100 : null }));
  const segments: string[] = [];
  let path = "";
  for (const point of points) {
    if (point.rate == null) { if (path) segments.push(path); path = ""; continue; }
    path += `${path ? " L" : "M"}${point.x},${110 - point.rate}`;
  }
  if (path) segments.push(path);
  return { points, segments };
}

export const insightPercent = (value: number | null) => value == null ? "未记录" : `${value.toFixed(1)}%`;
export const insightDuration = (value: number | null) => value == null ? "未记录" : `${(value / 1000).toFixed(2)} 秒`;
export const insightMoney = (value: number | null) => value == null ? "未知" : `$${value.toLocaleString("en-US", { minimumFractionDigits: 4, maximumFractionDigits: 6 })}`;
