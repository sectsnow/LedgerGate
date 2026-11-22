package app

import (
	"math"
	"math/big"
	"strings"
	"time"
)

// Token 单价使用微美元/百万 Token，按次、张、秒单价使用微美元/单位。
type priceRates struct {
	Input        *int64 `json:"input,omitempty"`
	Output       *int64 `json:"output,omitempty"`
	CacheRead    *int64 `json:"cache_read,omitempty"`
	CacheWrite   *int64 `json:"cache_write,omitempty"`
	CacheWrite1h *int64 `json:"cache_write_1h,omitempty"`
	ImageInput   *int64 `json:"image_input,omitempty"`
	ImageOutput  *int64 `json:"image_output,omitempty"`
}

type priceTier struct {
	priceRates
	Min       int64  `json:"min"`
	Max       *int64 `json:"max,omitempty"`
	Label     string `json:"label"`
	UnitPrice *int64 `json:"unit_price,omitempty"`
}

type modelPricing struct {
	priceRates
	Mode               string      `json:"mode"`
	UnitPrice          *int64      `json:"unit_price,omitempty"`
	Tiers              []priceTier `json:"tiers,omitempty"`
	Source             string      `json:"source"`
	SourceURL          string      `json:"source_url"`
	Model              string      `json:"model"`
	Reference          bool        `json:"reference"`
	Note               string      `json:"note,omitempty"`
	ChannelIdentity    string      `json:"channel_identity,omitempty"`
	UpdatedAt          *time.Time  `json:"updated_at,omitempty"`
	Stale              bool        `json:"stale,omitempty"`
	MultiplierIncluded bool        `json:"multiplier_included,omitempty"`
	AppliedMultiplier  *float64    `json:"applied_multiplier,omitempty"`
}

func intPointer(value int64) *int64 { return &value }

func pricingIdentity(channel Channel) string {
	return digest(jsonString(channel.AccountID) + "\x00" + channelRateIdentity(channel))
}

func validPrice(value *int64) bool {
	return value == nil || *value >= 0 && *value <= 1_000_000_000_000
}

func (rates priceRates) valid() bool {
	return validPrice(rates.Input) && validPrice(rates.Output) && validPrice(rates.CacheRead) && validPrice(rates.CacheWrite) && validPrice(rates.CacheWrite1h) && validPrice(rates.ImageInput) && validPrice(rates.ImageOutput)
}

func manualPricing(group Group) *modelPricing {
	price := &modelPricing{Mode: "token", Source: "manual", priceRates: priceRates{Input: intPointer(group.InputPriceMicros), Output: intPointer(group.OutputPriceMicros)}}
	if group.Pricing != nil {
		price.CacheRead, price.CacheWrite, price.CacheWrite1h = group.Pricing.CacheRead, group.Pricing.CacheWrite, group.Pricing.CacheWrite1h
	}
	return price
}

func overlayRates(base, extra priceRates) priceRates {
	if extra.Input != nil {
		base.Input = extra.Input
	}
	if extra.Output != nil {
		base.Output = extra.Output
	}
	if extra.CacheRead != nil {
		base.CacheRead = extra.CacheRead
	}
	if extra.CacheWrite != nil {
		base.CacheWrite = extra.CacheWrite
	}
	if extra.CacheWrite1h != nil {
		base.CacheWrite1h = extra.CacheWrite1h
	}
	if extra.ImageInput != nil {
		base.ImageInput = extra.ImageInput
	}
	if extra.ImageOutput != nil {
		base.ImageOutput = extra.ImageOutput
	}
	return base
}

func (price modelPricing) ratesFor(input int64) priceRates {
	rates := price.priceRates
	for _, tier := range price.Tiers {
		if input >= tier.Min && (tier.Max == nil || input < *tier.Max) {
			return overlayRates(rates, tier.priceRates)
		}
	}
	return rates
}

func (a *App) requestPricing(group Group, route Route, channel Channel) (*modelPricing, string) {
	if group.priceMode() == "manual" {
		return manualPricing(group), ""
	}
	if route.Pricing != nil {
		if route.Pricing.ChannelIdentity != pricingIdentity(channel) {
			return nil, "渠道配置已变化，请重新同步价格"
		}
		return route.Pricing, ""
	}
	if group.Pricing != nil {
		return nil, "此路由尚未同步价格"
	}
	if group.PriceSource != "" {
		return &modelPricing{Mode: "token", Source: group.PriceSource, Reference: true, priceRates: priceRates{Input: intPointer(group.InputPriceMicros), Output: intPointer(group.OutputPriceMicros)}}, ""
	}
	return nil, "模型价格尚未同步"
}

func pricedCost(result completion, price *modelPricing, multipliers ...*float64) (int64, string, string) {
	var multiplier *float64
	if len(multipliers) > 0 {
		multiplier = multipliers[0]
	}
	if multiplier != nil && (math.IsNaN(*multiplier) || math.IsInf(*multiplier, 0) || *multiplier < 0) {
		return 0, "unknown", "渠道倍率无效"
	}
	if price == nil {
		return 0, "unknown", "模型价格未匹配"
	}
	if price.Mode == "request" && price.UnitPrice != nil {
		cost := roundedRequestCost(big.NewInt(*price.UnitPrice), 1, multiplier)
		if cost == math.MaxInt64 {
			return cost, "estimated", "费用达到可存储上限"
		}
		return cost, "estimated", ""
	}
	if price.Mode != "token" {
		return 0, "unknown", "该模型按图片或视频用量计价，当前请求未提供相应用量"
	}
	if !result.UsageSeen {
		return 0, "unknown", "上游未返回可核算的 Token 用量"
	}
	rates := price.ratesFor(result.Input)
	if rates.Input == nil || rates.Output == nil {
		return 0, "unknown", "输入或输出单价缺失"
	}
	warnings := []string{}
	if price.Stale {
		warnings = append(warnings, "价格源暂不可用，使用上次报价估算")
	}
	if strings.Contains(price.Note, "分段计价") {
		return 0, "unknown", "上游采用分段计价，请查看上游实际消费"
	}
	if strings.Contains(price.Note, "分时") || strings.Contains(price.Note, "峰时") {
		warnings = append(warnings, price.Note)
	}
	read := min(max(result.CacheRead, 0), max(result.Input, 0))
	write := min(max(result.CacheWrite, 0), max(result.Input-read, 0))
	write1h := min(max(result.CacheWrite1h, 0), write)
	if read != result.CacheRead || write != result.CacheWrite || write1h != result.CacheWrite1h {
		warnings = append(warnings, "上游缓存用量超过输入总量，已限制为输入总量")
	}
	total := new(big.Int)
	add := func(tokens int64, rate *int64, label string) {
		if tokens <= 0 {
			return
		}
		if rate == nil {
			rate = rates.Input
			warnings = append(warnings, "缺少"+label+"价格，暂按普通输入价估算")
		}
		total.Add(total, new(big.Int).Mul(big.NewInt(tokens), big.NewInt(*rate)))
	}
	add(max(result.Input-read-write, 0), rates.Input, "输入")
	add(max(result.Output, 0), rates.Output, "输出")
	add(read, rates.CacheRead, "缓存读取")
	add(write-write1h, rates.CacheWrite, "缓存写入（5分钟）")
	add(write1h, rates.CacheWrite1h, "缓存写入（1小时）")
	cost := roundedRequestCost(total, 1_000_000, multiplier)
	if cost == math.MaxInt64 {
		warnings = append(warnings, "费用达到可存储上限")
	}
	return cost, "estimated", strings.Join(warnings, "；")
}
