package app

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type usageDetails struct {
	CacheReadSeen   bool
	CacheWriteSeen  bool
	ReasoningTokens *int64
	UsageWarning    string
}

func (result *completion) recordUsageDetails(usage object, protocol string) {
	read := func(value any) *int64 {
		count, ok := number(value)
		if !ok || math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || count > 1_000_000_000_000_000 || math.Trunc(count) != count {
			result.UsageWarning = "上游 Token 用量无效，无法核算费用"
			return nil
		}
		return intPointer(int64(count))
	}
	input, output, details := "input_tokens", "output_tokens", "input_tokens_details"
	if protocol == "openai" {
		input, output, details = "prompt_tokens", "completion_tokens", "prompt_tokens_details"
	}
	for _, field := range []string{input, output} {
		if value, exists := usage[field]; exists {
			read(value)
		}
	}
	cache := obj(usage[details])
	if protocol == "anthropic" {
		cache = usage
	}
	for _, field := range []string{"cached_tokens", "cache_read_input_tokens", "prompt_cache_hit_tokens"} {
		value, exists := cache[field]
		if !exists && field == "prompt_cache_hit_tokens" {
			value, exists = usage[field]
		}
		if exists && read(value) != nil {
			result.CacheReadSeen = true
		}
	}
	for _, field := range []string{"cache_creation_tokens", "cache_creation_1h_tokens", "cache_creation_input_tokens"} {
		if value, exists := cache[field]; exists && read(value) != nil {
			result.CacheWriteSeen = true
		}
	}
	for _, field := range []string{"ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens"} {
		if value, exists := obj(usage["cache_creation"])[field]; exists && read(value) != nil {
			result.CacheWriteSeen = true
		}
	}
	outputDetails := "output_tokens_details"
	if protocol == "openai" {
		outputDetails = "completion_tokens_details"
	}
	if value, exists := obj(usage[outputDetails])["reasoning_tokens"]; exists {
		result.ReasoningTokens = read(value)
	}
}

func reasoningEffort(body object) string {
	value := str(body["reasoning_effort"])
	if value == "" {
		value = str(obj(body["reasoning"])["effort"])
	}
	if value == "" {
		value = str(obj(body["output_config"])["effort"])
	}
	if value == "" {
		thinking := obj(body["thinking"])
		value = str(thinking["type"])
		if budget, ok := number(thinking["budget_tokens"]); ok && budget >= 0 && budget <= 1_000_000_000 && math.Trunc(budget) == budget {
			value += " · " + strconv.FormatInt(int64(budget), 10) + " Token"
		}
	}
	// 只保存简短参数，避免异常请求把整段内容写入日志。
	text := []rune(strings.TrimSpace(value))
	return string(text[:min(len(text), 80)])
}

type requestTimings struct {
	Started  time.Time
	First    time.Time
	Finished time.Time
}

func (t *requestTrace) recordFirstToken(ms int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.log.FirstTokenMS == nil {
		t.log.FirstTokenMS = intPointer(ms)
	}
}

func (timing *requestTimings) observe(event sseEvent, protocol string) bool {
	if !timing.First.IsZero() || !streamEventHasOutput(event, protocol) {
		return false
	}
	timing.First = time.Now()
	return true
}

func streamEventHasOutput(event sseEvent, protocol string) bool {
	value, err := decodeJSON([]byte(event.Data))
	if err != nil {
		return false
	}
	body := obj(value)
	if protocol == "openai" {
		for _, item := range array(body["choices"]) {
			delta := obj(obj(item)["delta"])
			for _, field := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
				if str(delta[field]) != "" {
					return true
				}
			}
			for _, call := range array(delta["tool_calls"]) {
				function := obj(obj(call)["function"])
				if str(function["name"]) != "" || str(function["arguments"]) != "" {
					return true
				}
			}
		}
		return false
	}
	if protocol == "anthropic" {
		if str(body["type"]) == "content_block_start" {
			block := obj(body["content_block"])
			return str(block["text"]) != "" || str(block["thinking"]) != "" || str(block["type"]) == "tool_use" && str(block["name"]) != ""
		}
		if str(body["type"]) == "content_block_delta" {
			delta := obj(body["delta"])
			return str(delta["text"]) != "" || str(delta["thinking"]) != "" || str(delta["partial_json"]) != ""
		}
		return false
	}
	switch str(body["type"]) {
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
		return str(body["delta"]) != ""
	case "response.output_item.added":
		item := obj(body["item"])
		return str(item["type"]) == "function_call" && str(item["name"]) != ""
	}
	return false
}

func (timing requestTimings) apply(log *RequestLog) {
	completed := time.Now()
	log.StartedAt, log.FinishedAt = &timing.Started, &completed
	log.DurationMS = completed.Sub(timing.Started).Milliseconds()
	if !timing.First.IsZero() {
		log.FirstTokenMS = intPointer(timing.First.Sub(timing.Started).Milliseconds())
		if !timing.Finished.IsZero() {
			log.GenerationMS = intPointer(max(timing.Finished.Sub(timing.First).Milliseconds(), 0))
		}
	}
}

type requestRate struct {
	Multiplier *float64
	Group      string
	Stale      bool
	Linked     bool
}

func (a *App) recordRequestRoute(log *RequestLog, route Route, channel Channel, protocol string) requestRate {
	log.ChannelName, log.UpstreamModel, log.UpstreamProtocol = channel.Name, route.Model, protocol
	log.AccountName = ""
	result := requestRate{Linked: channel.AccountID != nil}
	if !result.Linked {
		return result
	}
	var account Account
	if a.DB.Select("name", "rates", "status", "warnings").First(&account, *channel.AccountID).Error != nil {
		return result
	}
	log.AccountName = account.Name
	result.Stale = account.Status == "error" || strings.Contains(account.Warnings, "倍率采集失败")
	rate, exists := readRateSnapshot(account.Rates).Channels[channel.ID]
	if exists && rate.Identity == channelRateIdentity(channel) {
		result.Group = rate.Group
		if rate.Multiplier != nil && !math.IsNaN(*rate.Multiplier) && !math.IsInf(*rate.Multiplier, 0) && *rate.Multiplier >= 0 {
			result.Multiplier = rate.Multiplier
		}
	}
	return result
}

type requestCostDetail struct {
	Mode                   string     `json:"mode"`
	Rates                  priceRates `json:"rates"`
	UnitPrice              *int64     `json:"unit_price,omitempty"`
	Source                 string     `json:"source"`
	SourceURL              string     `json:"source_url"`
	PriceModel             string     `json:"price_model"`
	Reference              bool       `json:"reference"`
	Note                   string     `json:"note"`
	Tier                   string     `json:"tier"`
	BaseCostMicros         int64      `json:"base_cost_micros"`
	Multiplier             *float64   `json:"multiplier"`
	MultiplierApplied      bool       `json:"multiplier_applied"`
	RatesIncludeMultiplier bool       `json:"rates_include_multiplier"`
	MultiplierStale        bool       `json:"multiplier_stale"`
	RateGroup              string     `json:"rate_group"`
}

func (result completion) recordLogUsage(log *RequestLog) {
	log.InputTokens, log.OutputTokens = result.Input, result.Output
	log.CacheReadTokens, log.CacheWriteTokens, log.CacheWrite1hTokens = result.CacheRead, result.CacheWrite, result.CacheWrite1h
	log.UsageRecorded, log.CacheReadRecorded, log.CacheWriteRecorded = result.UsageSeen, result.CacheReadSeen, result.CacheWriteSeen
	log.ReasoningTokens, log.UsageWarning = result.ReasoningTokens, result.UsageWarning
}

func (a *App) recordRequestCost(log *RequestLog, result completion, group Group, route Route, channel Channel, rate requestRate) {
	log.CostSource, log.CostDetail = "", nil
	result.recordLogUsage(log)
	price, warning := a.requestPricing(group, route, channel)
	log.CostMicros, log.CostStatus, log.CostWarning = pricedCost(result, price)
	if warning != "" {
		log.CostWarning = warning
	}
	if result.UsageWarning != "" {
		log.CostMicros, log.CostStatus, log.CostWarning = 0, "unknown", result.UsageWarning
	}
	if price == nil {
		return
	}
	log.CostSource = price.Source
	if price.Stale { log.CostWarning = strings.Trim(log.CostWarning+"；价格采集失败，沿用上次报价", "；") }
	detail := &requestCostDetail{Mode: price.Mode, Rates: price.ratesFor(result.Input), UnitPrice: price.UnitPrice, Source: price.Source, SourceURL: price.SourceURL, PriceModel: price.Model, Reference: price.Reference, Note: price.Note, BaseCostMicros: log.CostMicros, Multiplier: rate.Multiplier, MultiplierStale: rate.Stale, RateGroup: rate.Group}
	if price.MultiplierIncluded {
		detail.RatesIncludeMultiplier, detail.MultiplierApplied = true, true
		detail.Multiplier = price.AppliedMultiplier
		if rate.Multiplier != nil && price.AppliedMultiplier != nil && *rate.Multiplier != *price.AppliedMultiplier {
			log.CostWarning = strings.Trim(log.CostWarning+"；计价倍率与当前渠道倍率不同，请重新同步价格", "；")
		}
	}
	for _, tier := range price.Tiers {
		if result.Input >= tier.Min && (tier.Max == nil || result.Input < *tier.Max) {
			detail.Tier = tier.Label
			break
		}
	}
	// 站点报价已包含分组倍率，只有公开参考价需要再应用渠道倍率。
	if group.priceMode() != "manual" && price.Reference && !price.MultiplierIncluded && log.CostStatus == "estimated" && rate.Linked {
		if rate.Multiplier != nil && !rate.Stale {
			log.CostMicros, log.CostStatus, warning = pricedCost(result, price, rate.Multiplier)
			detail.MultiplierApplied = true
			if warning != "" {
				log.CostWarning = warning
			}
		} else {
			message := "渠道倍率未知，暂按基础价格估算"
			if rate.Stale {
				message = "渠道倍率已过期，暂按基础价格估算"
			}
			log.CostWarning = strings.Trim(log.CostWarning+"；"+message, "；")
		}
	}
	log.CostDetail = detail
}

func roundedRequestCost(total *big.Int, divisor int64, multiplier *float64) int64 {
	amount := new(big.Rat).SetFrac(total, big.NewInt(divisor))
	if multiplier != nil {
		rate, ok := new(big.Rat).SetString(strconv.FormatFloat(*multiplier, 'f', -1, 64))
		if !ok || rate.Sign() < 0 {
			return 0
		}
		amount.Mul(amount, rate)
	}
	amount.Add(amount, big.NewRat(1, 2))
	whole := new(big.Int).Quo(amount.Num(), amount.Denom())
	if !whole.IsInt64() {
		return int64(^uint64(0) >> 1)
	}
	return whole.Int64()
}
