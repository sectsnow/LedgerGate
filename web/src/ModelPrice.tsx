import type { Group, ModelPricing, PriceRates } from "./api";

const format = (value: number | null | undefined) => value == null ? "未提供" : `$${(value / 1e6).toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 6 })}`;

function Rates({ rates, brief = false }: { rates: PriceRates; brief?: boolean }) {
  return (
    <>
      <p>输入 {format(rates.input)} / 输出 {format(rates.output)} 每百万 Token</p>
      {!brief && <small>缓存读 {format(rates.cache_read)} · 缓存写（5分钟）{format(rates.cache_write)} · 缓存写（1小时）{format(rates.cache_write_1h)}</small>}
      {!brief && (rates.image_input != null || rates.image_output != null) && <small>图片 Token：输入 {format(rates.image_input)} / 输出 {format(rates.image_output)} 每百万 Token</small>}
    </>
  );
}

export default function ModelPrice({ group, pricing, compact = false, brief = false }: { group: Group; pricing?: ModelPricing | null; compact?: boolean; brief?: boolean }) {
  const price = group.price_mode === "manual" ? group.pricing : pricing === undefined ? group.pricing : pricing;
  const known = price || (pricing === undefined && group.price_source) || group.price_mode === "manual";
  const source = price?.source || group.price_source;
  const updated = price?.updated_at || group.price_updated_at;
  const unit = price?.mode === "video" ? "秒视频" : price?.mode === "image" ? "张图片" : "次请求";
  return (
    <div className={`model-price${compact ? " compact" : ""}${brief ? " brief" : ""}`}>
      {!known ? <p>{group.price_sync_error ? "价格未匹配" : "价格待同步"}</p> : price && price.mode !== "token" ? (
        <>
          <p>{price.mode === "image" ? "图片计价" : price.mode === "video" ? "视频计价" : "按次计价"}{price.unit_price != null && ` · ${format(price.unit_price)} / ${unit}`}</p>
          {!brief && (price.input != null || price.output != null || price.image_output != null) && <Rates rates={price} />}
        </>
      ) : <Rates rates={price || { input: group.input_price_micros, output: group.output_price_micros }} brief={brief} />}
      {brief && price?.reference && <small>参考价</small>}
      {brief && !!price?.tiers?.length && <small>阶梯价</small>}
      {brief && !compact && group.price_mode !== "manual" && group.routes.length > 1 && <small>按路由计价</small>}
      {!brief && price?.tiers && price.tiers.length > 0 && (
        <details className="price-tiers">
          <summary>{price.mode === "token" ? "上下文阶梯" : "计价档位"} · {price.tiers.length} 档</summary>
          {price.tiers.map((tier, index) => (
            <div className="price-tier" key={index}>
              <strong>{tier.label}</strong>
              {price.mode === "token" ? <Rates rates={tier} /> : <p>{format(tier.unit_price)} / {unit}</p>}
            </div>
          ))}
        </details>
      )}
      {known && !brief && (
        <small>
          {group.price_mode === "manual" ? "手动价格" : price?.source_url ? (
            <a href={price.source_url} target="_blank" rel="noreferrer">{source} · {price.reference ? "公开参考价" : "渠道报价"}</a>
          ) : source}
          {(price?.model || group.price_source_model) && ` · ${price?.model || group.price_source_model}`}
          {updated && ` · ${new Date(updated).toLocaleString("zh-CN", { hour12: false })}`}
        </small>
      )}
      {price?.stale && <small className="price-match-error">{brief ? "沿用旧报价" : "最新采集失败，沿用上次报价"}</small>}
      {!brief && price?.note && <small>{price.note}</small>}
      {!compact && !brief && group.routes.length > 1 && <small>按实际转发的路由报价估算，详情见各路由。</small>}
      {!compact && group.price_sync_error && (!brief || (known && !price?.stale)) && <small className="price-match-error">{brief ? "价格采集异常" : group.price_sync_error}</small>}
    </div>
  );
}
