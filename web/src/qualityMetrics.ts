export type QualityMetricsRow = {
  requests: number; unknown_costs: number; cost_micros: number;
  cache_sample_requests?: number | null; cache_sample_input_tokens?: number | null; cache_sample_read_tokens?: number | null;
  cache_hit_requests?: number | null; cache_write_sample_requests?: number | null;
  observed_requests: number; first_attempt_successes: number;
};
export function qualityPreferences(raw:string|null) {
  try {
    const value=JSON.parse(raw??"null");
    return {group:value?.group==="model" ? "model" : "channel",model:typeof value?.model==="string" && value.model.length<=200 ? value.model : "",sort:["requests","success","latency","cost","cache","first"].includes(value?.sort) ? value.sort as string : "requests"};
  } catch {return {group:"channel",model:"",sort:"requests"};}
}
export function qualityMetrics(row: QualityMetricsRow) {
  const ratio = (value: number | null | undefined, total: number | null | undefined) => value != null && total != null && Number.isFinite(value) && Number.isFinite(total) && total > 0 && value >= 0 && value <= total ? value / total * 100 : null;
  const knownCosts = row.requests - row.unknown_costs;
  return {
    uncachedInput: (row.cache_sample_requests ?? 0) > 0 && row.cache_sample_input_tokens != null && row.cache_sample_read_tokens != null && row.cache_sample_input_tokens >= row.cache_sample_read_tokens ? row.cache_sample_input_tokens - row.cache_sample_read_tokens : null,
    cacheHitRate: ratio(row.cache_sample_read_tokens, row.cache_sample_input_tokens),
    cacheRequestRate: ratio(row.cache_hit_requests, row.cache_sample_requests),
    cacheCoverage: ratio(row.cache_sample_requests, row.requests),
    writeCoverage: ratio(row.cache_write_sample_requests, row.requests),
    firstSuccessRate: ratio(row.first_attempt_successes, row.observed_requests),
    attemptCoverage: ratio(row.observed_requests, row.requests),
    costCoverage: ratio(knownCosts, row.requests),
    averageCost: knownCosts > 0 && knownCosts <= row.requests ? row.cost_micros / knownCosts / 1e6 : null,
  };
}
