export function averageOutputTPS(tokens: number | null | undefined, durationMS: number | null | undefined): number | null {
  if (tokens == null || durationMS == null || !Number.isFinite(tokens) || !Number.isFinite(durationMS) || tokens <= 0 || durationMS <= 0) return null;
  return tokens * 1000 / durationMS;
}

export function requestTiming(row: { metrics_version: number; first_token_ms: number | null; first_content_ms?: number | null }) {
  return {
    firstWordMS: row.metrics_version >= 2 ? row.first_token_ms : null,
    firstContentMS: row.metrics_version >= 2 ? row.first_content_ms ?? null : row.first_token_ms,
  };
}
