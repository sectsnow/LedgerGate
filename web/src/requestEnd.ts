export function requestEndLabel(reason?: string, incompleteReason?: string): string {
  switch (reason) {
    case "stop": return "正常结束";
    case "length": return "达到 Token 上限";
    case "content_filter": return "内容过滤或拒绝";
    case "tool_calls": return "已请求工具调用";
    case "multiple": return "多个候选的结束原因不同";
    case "incomplete": return incompleteReason ? `输出未完成（${incompleteReason}）` : "输出未完成，原因未提供";
    default: return reason || "";
  }
}

export function attemptStatusLabel(status: number, upstreamStatus?: number): string {
  if (!status) return "进行中";
  if (upstreamStatus === undefined) return `结果 HTTP ${status}`;
  if (!upstreamStatus) return `未收到 HTTP 响应 · 转发结果 ${status}`;
  const result = status === upstreamStatus ? "" : ` · 转发结果 ${status}`;
  return `上游 HTTP ${upstreamStatus}${result}`;
}
