import type { ReactNode } from "react";
import { ArrowRight, BrainCircuit, CircleAlert, Clock3, Coins, FileInput, FileOutput, FileText, Gauge, HardDriveDownload, HardDriveUpload, Hourglass, KeyRound, Network, Percent, Radio, ReceiptText, TextCursorInput, Timer } from "lucide-react";
import { protocolNames, type ChannelRow, type RequestLog } from "./api";
import type { DataTone } from "./themes";
import { averageOutputTPS, requestTiming } from "./requestTiming";
import { requestEndLabel } from "./requestEnd";
import "./request-metrics.css";

const exact = (value: number) => value.toLocaleString("en-US", { maximumFractionDigits: 0 });
const money = (micros: number) => `$${(micros / 1e6).toFixed(6)}`;
const duration = (ms: number) => `${(ms / 1000).toFixed(2)}s`;
const date = (value: string) => new Date(value).toLocaleString("zh-CN", { hour12: false });

function Metric({ icon, label, value, title, tone = "traffic" }: { icon: ReactNode; label: string; value: ReactNode; title?: string; tone?: DataTone }) {
  return <span className="request-metric" title={title} data-tone={tone}><span className="request-metric-icon">{icon}</span><span><small>{label}</small><strong>{value}</strong></span></span>;
}

function CostDetails({ row }: { row: RequestLog }) {
  const detail = row.cost_detail;
  if (!detail) return <p className="request-log-note">{row.metrics_version ? "本次尚无可核算的价格或用量。" : "旧记录未保存计价明细，保留当时的费用估算。"}</p>;
  const read = Math.min(Math.max(row.cache_read_tokens, 0), Math.max(row.input_tokens, 0));
  const write = Math.min(Math.max(row.cache_write_tokens, 0), Math.max(row.input_tokens - read, 0));
  const write1h = Math.min(Math.max(row.cache_write_1h_tokens, 0), write);
  const lines = [
    { label: "普通输入", tokens: Math.max(row.input_tokens - read - write, 0), rate: detail.rates.input },
    { label: "缓存读取", tokens: read, rate: detail.rates.cache_read, fallback: true },
    { label: "缓存写入（5分钟）", tokens: write - write1h, rate: detail.rates.cache_write, fallback: true },
    { label: "缓存写入（1小时）", tokens: write1h, rate: detail.rates.cache_write_1h, fallback: true },
    { label: "输出", tokens: row.output_tokens, rate: detail.rates.output },
  ];
  return (
    <details className="request-fee-details">
      <summary><ReceiptText size={15} />费用明细{detail.tier && <span>{detail.tier}</span>}</summary>
      <div className="request-fee-content">
        <p className="request-log-note">{detail.source === "manual" ? "手动价格" : detail.source || "价格来源未记录"}{detail.reference ? " · 公开参考价" : ""}{detail.price_model && ` · ${detail.price_model}`}{detail.source_url && <> · <a href={detail.source_url} target="_blank" rel="noreferrer">查看来源</a></>}</p>
        {detail.mode === "token" ? (
          <div className="table-scroll"><table className="request-fee-table"><thead><tr><th>项目</th><th>Token 数</th><th>美元 / 百万 Token</th><th>基础估算</th></tr></thead><tbody>
            {lines.map(line => {
              const fallback = line.rate == null && line.fallback && line.tokens > 0;
              const rate = fallback ? detail.rates.input : line.rate;
              return <tr key={line.label}><td>{line.label}{fallback && <small>按普通输入价估算</small>}</td><td>{exact(line.tokens)}</td><td>{rate == null ? "未提供" : `$${(rate / 1e6).toLocaleString("en-US", { maximumFractionDigits: 6 })}`}</td><td>{line.tokens === 0 ? "$0.000000" : rate == null ? "待核算" : `$${(line.tokens * rate / 1e12).toFixed(9)}`}</td></tr>;
            })}
          </tbody></table></div>
        ) : <p>{detail.mode === "request" ? `按次计价 · ${detail.unit_price == null ? "单价未提供" : money(detail.unit_price) + " / 次"}` : "当前用量不足以核算图片或视频费用。"}</p>}
        <div className="request-fee-total">
          <span>{detail.rates_include_multiplier ? "单价合计（已含倍率）" : "基础估算"} <strong>{row.cost_status === "unknown" ? "待核算" : money(detail.base_cost_micros)}</strong></span>
          <span>渠道倍率 <strong>{detail.multiplier == null ? "未提供" : `${detail.multiplier}×`}</strong>{detail.rate_group && ` · ${detail.rate_group}`}</span>
          <span>{detail.rates_include_multiplier ? "单价已含倍率，不再重复相乘" : detail.multiplier_applied ? "已应用倍率" : detail.source === "manual" ? "手动单价，不额外乘倍率" : detail.multiplier_stale ? "倍率过期，暂用基础估算" : "未应用倍率"}</span>
          <span>本次费用估算 <strong>{row.cost_status === "unknown" ? "待核算" : money(row.cost_micros)}</strong></span>
        </div>
        <p className="request-log-note">分项保留更多小数，总费用统一四舍五入至 6 位美元小数。{detail.note}</p>
      </div>
    </details>
  );
}

export default function RequestMetrics({ row, channel, small = false, children }: { row: RequestLog; channel?: ChannelRow; small?: boolean; children?: ReactNode }) {
  const modern = row.metrics_version > 0;
  const known = modern && row.usage_recorded && !row.usage_warning;
  const missing = modern ? "未提供" : "未记录";
  const input = known || !modern ? exact(row.input_tokens) : "未提供";
  const output = known || !modern ? exact(row.output_tokens) : "未提供";
  const timing = requestTiming(row);
  const first = timing.firstWordMS == null ? row.stream ? "未记录" : "不适用" : duration(timing.firstWordMS);
  const firstContent = timing.firstContentMS == null ? row.stream ? "未记录" : "不适用" : duration(timing.firstContentMS);
  const generation = row.generation_ms == null ? row.stream ? "未记录" : "不适用" : duration(row.generation_ms);
  const tps = known ? averageOutputTPS(row.output_tokens, row.duration_ms) : null;
  const hit = known && row.cache_read_recorded && row.input_tokens > 0 ? Math.min(row.cache_read_tokens, row.input_tokens) / row.input_tokens * 100 : null;
  const channelName = row.channel_name || channel?.channel.name || (row.channel_id ? `渠道 #${row.channel_id}` : "未选择渠道");
  const status = row.state === "interrupted" ? "已中断" : row.status === 0 && row.retry_at ? "等待重试" : row.state === "streaming" ? timing.firstContentMS == null ? "已接收响应" : "输出中" : row.status === 0 ? "进行中" : row.status === 499 ? "已取消" : row.status < 400 ? "成功" : "失败";
  const cost = row.cost_status === "unknown" || modern && !known && row.cost_detail?.mode !== "request" ? "待核算" : money(row.cost_micros);
  return (
    <article className={`request-log-card${small ? " request-log-compact" : ""}`} aria-label={`${row.model || "无模型"} 请求 ${row.request_id}`}>
      <div className="request-log-heading">
        <div className="request-log-symbol"><Network size={21} /></div>
        <div className="request-log-chain">
          <span className="request-protocol">{protocolNames[row.protocol] || row.protocol || "未知协议"}</span><strong>{row.model || "无模型"}</strong>
          {row.reasoning_effort && <span className="request-reasoning" title="请求的推理强度"><BrainCircuit size={14} />{row.reasoning_effort}</span>}
          <ArrowRight size={16} className="request-chain-arrow" />
          <span className="request-protocol">{protocolNames[row.upstream_protocol] || row.upstream_protocol || "未记录协议"}</span>
          <span className="request-channel">{row.account_name ? `${row.account_name} / ${channelName}` : channelName}</span><code>{row.upstream_model || "未记录上游模型"}</code>
        </div>
        <span className={`request-result ${row.status >= 400 ? "failed" : row.status === 0 ? "running" : "completed"}`}>{status}{row.status > 0 && ` · ${row.status}`}</span>
        {children}
      </div>
      <div className="request-log-context">
        <span title={modern ? "请求开始时间" : "历史记录保存时间"}><Clock3 size={14} />{date(row.started_at || row.created_at)}</span>
        <span title="调用网关密钥的名称"><KeyRound size={14} />{row.key_name || (row.key_id ? `密钥 #${row.key_id} · 名称未记录` : "未记录密钥")}</span>
        <span>{row.stream ? "流式 SSE" : "非流式"} · 尝试 {row.attempts} 次</span>
      </div>
      <div className="request-log-metrics">
        <Metric icon={<Timer size={16} />} label="总耗时" value={duration(row.duration_ms)} />
        <Metric icon={<Radio size={16} />} label="首字" value={first} title="从收到请求到收到上游首个语义事件；Responses 思考项目开始也计入，初始化和心跳不计入。包含此前重试等待，旧口径记录不补造。" />
        {!small && <Metric icon={<TextCursorInput size={16} />} label="首内容" value={firstContent} title="从收到请求到收到首段正文、公开思考文本或工具内容；加密思考不等同于可见内容。" />}
        {!small && <Metric icon={<Hourglass size={16} />} label="输出阶段耗时" value={generation} title="从首内容到上游流结束；整包返回时无法据此推算模型生成速度。" />}
        {!small && <Metric tone="balance" icon={<FileInput size={16} />} label="输入总量" value={input} title="包含普通输入、缓存读取和缓存写入，以下缓存数量已包含在总量中。" />}
        {!small && <Metric tone="balance" icon={<FileText size={16} />} label="未缓存输入" value={known && row.cache_read_recorded ? exact(Math.max(row.input_tokens - row.cache_read_tokens, 0)) : missing} title="输入总量减去缓存读取，包含可能发生的缓存写入；无需上游提供缓存写入数量即可计算。" />}
        {!small && <Metric tone="cost" icon={<HardDriveDownload size={16} />} label="缓存读取" value={known && row.cache_read_recorded ? exact(row.cache_read_tokens) : missing} />}
        {!small && <Metric tone="cost" icon={<Percent size={16} />} label="缓存命中率" value={hit == null ? missing : `${hit.toFixed(2)}%`} title="缓存读取 Token ÷ 输入总 Token。" />}
        {!small && <Metric tone="cost" icon={<HardDriveUpload size={16} />} label="缓存写入" value={known && row.cache_write_recorded ? exact(row.cache_write_tokens) : missing} title="仅显示上游明确返回的缓存写入数量；未提供不代表 0，也不影响未缓存输入的计算。" />}
        {!small && <Metric tone="usage" icon={<FileOutput size={16} />} label="输出总量" value={output} title={row.reasoning_tokens == null ? "上游返回的输出 Token 总量。" : `包含 ${exact(row.reasoning_tokens)} 个推理 Token，不重复计费。`} />}
        <Metric tone="usage" icon={<Gauge size={16} />} label="平均 TPS" value={tps == null ? "未记录" : `${tps.toLocaleString("en-US", { maximumFractionDigits: 2 })} t/s`} title="输出 Token 总数 ÷ 完整请求耗时，包含排队、思考、网络及重试等待；适用于流式和非流式，不代表模型纯生成速度。" />
        <Metric tone="cost" icon={<Coins size={16} />} label={modern ? "费用估算" : "历史估算"} value={cost} title={row.cost_warning || undefined} />
      </div>
      {(row.error || row.cost_warning || row.usage_warning) && <p className="request-log-warning" role="status"><CircleAlert size={15} />{[row.error, row.cost_warning, row.usage_warning].filter((item, index, values) => item && values.indexOf(item) === index).join("；")}</p>}
      {row.routing_reason && <p className="request-log-note">选路说明：{row.routing_reason}{row.retry_at && ` · 预计重试于 ${date(row.retry_at)}`}</p>}
      {row.finish_reason && <p className="request-log-note">上游结束原因：{requestEndLabel(row.finish_reason, row.incomplete_reason)}</p>}
      {!small && <><CostDetails row={row} />{row.finished_at && <div className="request-log-footer"><span>完成于 {date(row.finished_at)}</span></div>}</>}
    </article>
  );
}
