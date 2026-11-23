import { useEffect, useState } from "react";
import { ChartNoAxesCombined } from "lucide-react";
import { api, type Overview } from "./api";

type Row = { id: string; name: string; requests: number; successes: number; input_tokens: number; output_tokens: number; cost_micros: number; unknown_costs: number; average_duration_ms: number | null; average_first_token_ms: number | null; output_per_second: number | null };

export default function RequestQuality({ range, refreshed }: { range: string; refreshed: Overview }) {
  const [group, setGroup] = useState("channel");
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    api<{ items: Row[] }>(`/request-quality?group_by=${group}&${range}`, "GET", undefined, controller.signal)
      .then(result => { if (!controller.signal.aborted) setRows(result.items); })
      .catch(error => { if (!controller.signal.aborted) setError(error.message); });
    return () => controller.abort();
  }, [group, range, refreshed]);
  const duration = (value: number | null) => value == null ? "未记录" : `${(value / 1000).toFixed(2)} 秒`;
  return <section className="panel quality-panel">
    <div className="panel-heading"><div><h2 className="icon-heading"><ChartNoAxesCombined size={18} aria-hidden="true" />渠道与模型表现</h2><p>与网关用量使用相同时段。使用已结束请求的持久化汇总，重启后仍可查看；取消计入未成功请求。</p></div>
      <div className="balance-period" role="group" aria-label="表现统计维度">{[["channel", "渠道"], ["model", "模型"]].map(([value, label]) => <button key={value} aria-pressed={group === value} onClick={() => { setGroup(value); setRows(null); }}>{label}</button>)}</div>
    </div>
    {error ? <p className="quality-note form-error" role="alert">{error}</p> : <div className="table-scroll"><table><thead><tr><th>{group === "channel" ? "渠道" : "模型"}</th><th>请求数</th><th>成功率</th><th>平均总耗时</th><th>平均首字</th><th>平均 TPS</th><th>Token</th><th>费用估算</th></tr></thead><tbody>
      {rows?.map(row => <tr key={row.id}><td>{row.name}</td><td>{row.requests}</td><td>{(row.successes / row.requests * 100).toFixed(1)}%</td><td>{duration(row.average_duration_ms)}</td><td>{duration(row.average_first_token_ms)}</td><td>{row.output_per_second == null ? "未记录" : `${row.output_per_second.toFixed(1)} Token/秒`}</td><td>{(row.input_tokens + row.output_tokens).toLocaleString("zh-CN")}</td><td>${(row.cost_micros / 1e6).toFixed(4)}{row.unknown_costs > 0 && <small className="table-sub">{row.unknown_costs} 次待核算</small>}</td></tr>)}
      {(!rows || rows.length === 0) && <tr><td colSpan={8}>{rows ? "所选时段暂无已结束请求" : "正在读取…"}</td></tr>}
    </tbody></table></div>}
    <p className="quality-note">平均 TPS 为输出总 Token ÷ 请求完整耗时之和，包含思考与等待；平均首字仅统计新语义口径记录，旧记录不补造。最多展示请求量最高的 500 项。</p>
  </section>;
}
