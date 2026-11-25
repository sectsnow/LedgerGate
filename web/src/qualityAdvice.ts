export type ComparableQuality = {name: string; requests: number; successes: number; unknown_costs: number; input_tokens: number; output_tokens: number; cost_micros: number; average_first_token_ms: number | null};
export function qualityAdvice(rows: ComparableQuality[], model: string): string {
  if (!model) return "选择同一个统一模型后，再比较不同渠道；混合模型的延迟和单价不能直接比较。";
  const candidates = rows.filter(row => row.requests >= 20);
  if (candidates.length < 2) return "至少两个渠道各有 20 次请求后再给出参考；少量样本不足以判断优劣。";
  const stable = [...candidates].sort((a, b) => b.successes / b.requests - a.successes / a.requests)[0];
  const fast = candidates.filter(row => row.average_first_token_ms != null).sort((a, b) => a.average_first_token_ms! - b.average_first_token_ms!)[0];
  return `当前样本成功率最高：${stable.name}（${(stable.successes / stable.requests * 100).toFixed(1)}%）${fast ? `；平均首字最短：${fast.name}` : ""}。仅供参考，未自动更改路由；价格受输入输出比例、缓存命中及费用完整度影响。`;
}
