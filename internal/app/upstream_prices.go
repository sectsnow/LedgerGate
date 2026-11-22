package app

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type accountPrices map[string]map[string]*modelPricing

var tierExpression = regexp.MustCompile(`^tier\("([^"]+)",\s*(.+)\)$`)
var fixedExpression = regexp.MustCompile(`^fixed\(\s*([0-9.eE+-]+)\s*\)$`)
var priceTerm = regexp.MustCompile(`^(p|c|cr|cw|cw1h)\s*\*\s*([0-9.eE+-]+)$`)
var lengthCondition = regexp.MustCompile(`^len\s*<=\s*([0-9]+)$`)

func expressionTier(expression string) (priceTier, bool) {
	match := tierExpression.FindStringSubmatch(strings.TrimSpace(expression))
	if match == nil {
		return priceTier{}, false
	}
	tier := priceTier{Label: match[1]}
	if fixed := fixedExpression.FindStringSubmatch(match[2]); fixed != nil {
		tier.UnitPrice = sourcePrice(fixed[1], false)
		return tier, tier.UnitPrice != nil
	}
	for _, term := range strings.Split(match[2], "+") {
		part := priceTerm.FindStringSubmatch(strings.TrimSpace(term))
		if part == nil {
			return priceTier{}, false
		}
		amount := sourcePrice(part[2], false)
		if amount == nil {
			return priceTier{}, false
		}
		switch part[1] {
		case "p":
			if tier.Input != nil {
				return priceTier{}, false
			}
			tier.Input = amount
		case "c":
			if tier.Output != nil {
				return priceTier{}, false
			}
			tier.Output = amount
		case "cr":
			if tier.CacheRead != nil {
				return priceTier{}, false
			}
			tier.CacheRead = amount
		case "cw":
			if tier.CacheWrite != nil {
				return priceTier{}, false
			}
			tier.CacheWrite = amount
		case "cw1h":
			if tier.CacheWrite1h != nil {
				return priceTier{}, false
			}
			tier.CacheWrite1h = amount
		}
	}
	return tier, tier.Input != nil && tier.Output != nil
}

func parseBillingExpression(expression string) (*modelPricing, bool) {
	condition, rest, conditional := strings.Cut(expression, "?")
	if !conditional {
		tier, ok := expressionTier(expression)
		if !ok {
			return nil, false
		}
		price := &modelPricing{Mode: "token", priceRates: tier.priceRates}
		if tier.UnitPrice != nil {
			price.Mode, price.UnitPrice = "request", tier.UnitPrice
		}
		return price, true
	}
	conditionMatch := lengthCondition.FindStringSubmatch(strings.TrimSpace(condition))
	left, right, ok := strings.Cut(rest, ":")
	if !ok || conditionMatch == nil {
		return nil, false
	}
	limit, err := strconv.ParseInt(conditionMatch[1], 10, 64)
	if err != nil || limit < 0 || limit >= 1_000_000_000 {
		return nil, false
	}
	low, validLow := expressionTier(left)
	high, validHigh := expressionTier(right)
	if !validLow || !validHigh || low.UnitPrice != nil || high.UnitPrice != nil {
		return nil, false
	}
	low.Max, high.Min = intPointer(limit+1), limit+1
	return &modelPricing{Mode: "token", priceRates: low.priceRates, Tiers: []priceTier{low, high}}, true
}

func scalePrice(value *int64, factor float64) *int64 {
	if value == nil {
		return nil
	}
	multiplier, ok := new(big.Rat).SetString(strconv.FormatFloat(factor, 'g', -1, 64))
	if !ok || multiplier.Sign() < 0 {
		return nil
	}
	multiplier.Mul(multiplier, new(big.Rat).SetInt64(*value))
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(multiplier.Num(), multiplier.Denom(), remainder)
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(multiplier.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || whole.Int64() > 1_000_000_000_000 {
		return nil
	}
	amount := whole.Int64()
	return &amount
}

func scaleRates(rates priceRates, factor float64) priceRates {
	return priceRates{Input: scalePrice(rates.Input, factor), Output: scalePrice(rates.Output, factor), CacheRead: scalePrice(rates.CacheRead, factor), CacheWrite: scalePrice(rates.CacheWrite, factor), CacheWrite1h: scalePrice(rates.CacheWrite1h, factor), ImageInput: scalePrice(rates.ImageInput, factor), ImageOutput: scalePrice(rates.ImageOutput, factor)}
}

func scaledPricing(original *modelPricing, factor float64) *modelPricing {
	price := *original
	price.priceRates, price.UnitPrice = scaleRates(price.priceRates, factor), scalePrice(price.UnitPrice, factor)
	price.Tiers = append([]priceTier(nil), original.Tiers...)
	for i := range price.Tiers {
		price.Tiers[i].priceRates = scaleRates(price.Tiers[i].priceRates, factor)
		price.Tiers[i].UnitPrice = scalePrice(price.Tiers[i].UnitPrice, factor)
	}
	return &price
}

func (a *App) fetchAccountPrices(ctx context.Context, account Account) (accountPrices, error) {
	path := "/api/pricing"
	if account.Kind == "sub2api" {
		path = "/api/v1/model-plaza"
	}
	value, _, err := a.accountRequest(ctx, &account, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	result := accountPrices{}
	if account.Kind == "sub2api" {
		groups, ok := obj(value)["groups"].([]any)
		if !ok {
			return nil, errors.New("模型广场价格格式无效")
		}
		keys := [7]string{"input_price", "output_price", "cache_read_price", "cache_write_price", "cache_write_1h_price", "image_input_price", "image_output_price"}
		for _, rawGroup := range groups {
			group := obj(rawGroup)
			id := strconv.FormatInt(integer(group["id"]), 10)
			result[id] = map[string]*modelPricing{}
			for _, rawModel := range array(group["models"]) {
				model := obj(rawModel)
				data := obj(model["pricing"])
				if len(data) == 0 {
					continue
				}
				price := &modelPricing{Mode: str(data["billing_mode"]), Model: str(model["name"]), Source: account.Name, SourceURL: endpoint(account.BaseURL, path), priceRates: tokenSourceRates(data, keys)}
				if price.Mode == "per_request" {
					price.Mode = "request"
				}
				price.UnitPrice = sourcePrice(data["per_request_price"], false)
				if obj(model["time_pricing"])["periods"] != nil || group["peak_rate_enabled"] == true {
					price.Note = "上游有分时或峰时倍率，当前仅展示基础估算"
				}
				for _, rawTier := range array(data["intervals"]) {
					tier := obj(rawTier)
					if price.Mode == "token" && group["long_context_pricing_enabled"] != true {
						continue
					}
					entry := priceTier{Label: str(tier["tier_label"]), Min: integer(tier["min_tokens"]), priceRates: tokenSourceRates(tier, keys), UnitPrice: sourcePrice(tier["per_request_price"], false)}
					if tier["max_tokens"] != nil {
						entry.Max = intPointer(integer(tier["max_tokens"]))
					}
					price.Tiers = append(price.Tiers, entry)
				}
				if str(model["long_context_basis"]) == "marginal" {
					price.Note = "上游采用分段计价，当前价格仅供参考"
					price.Reference = true
				}
				if price.Mode == "image" && group["image_rate_independent"] == true {
					if factor := rateNumber(group["image_rate_multiplier"]); factor != nil {
						price = scaledPricing(price, *factor)
						price.Note += "；使用图片独立倍率"
					}
				}
				if price.Mode == "video" && group["video_rate_independent"] == true {
					if factor := rateNumber(group["video_rate_multiplier"]); factor != nil {
						price = scaledPricing(price, *factor)
						price.Note += "；使用视频独立倍率"
					}
				}
				result[id][strings.ToLower(price.Model)] = price
			}
		}
		return result, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("NewAPI 价格列表格式无效")
	}
	for _, raw := range items {
		model := obj(raw)
		var price *modelPricing
		if str(model["billing_mode"]) == "tiered_expr" {
			var ok bool
			price, ok = parseBillingExpression(str(model["billing_expr"]))
			if !ok {
				continue
			}
		} else if integer(model["quota_type"]) == 1 {
			price = &modelPricing{Mode: "request", UnitPrice: sourcePrice(model["model_price"], false)}
			if price.UnitPrice == nil {
				continue
			}
		} else {
			ratio := rateNumber(model["model_ratio"])
			completion := rateNumber(model["completion_ratio"])
			if ratio == nil || completion == nil {
				continue
			}
			base := intPointer(2_000_000)
			input := scalePrice(base, *ratio)
			price = &modelPricing{Mode: "token", priceRates: priceRates{Input: input, Output: scalePrice(input, *completion)}}
			if factor := rateNumber(model["cache_ratio"]); factor != nil {
				price.CacheRead = scalePrice(input, *factor)
			}
			if factor := rateNumber(model["create_cache_ratio"]); factor != nil {
				price.CacheWrite = scalePrice(input, *factor)
			}
		}
		price.Source, price.SourceURL, price.Model = account.Name, endpoint(account.BaseURL, path), str(model["model_name"])
		for _, rawGroup := range array(model["enable_groups"]) {
			id := str(rawGroup)
			if result[id] == nil {
				result[id] = map[string]*modelPricing{}
			}
			result[id][strings.ToLower(price.Model)] = price
		}
	}
	return result, nil
}

func upstreamRoutePrice(account Account, channel Channel, model string, catalog accountPrices) (*modelPricing, string) {
	if strings.TrimSuffix(channel.BaseURL, "/v1") != strings.TrimSuffix(account.BaseURL, "/v1") {
		return nil, "渠道地址与关联账户不同"
	}
	rate, ok := readRateSnapshot(account.Rates).Channels[channel.ID]
	if !ok || rate.Identity != channelRateIdentity(channel) || rate.Multiplier == nil {
		return nil, "尚未确认渠道所属分组和实际倍率"
	}
	price := catalog[rate.GroupID][strings.ToLower(model)]
	if price == nil {
		return nil, "上游分组未返回该模型计价规则"
	}
	if price.Mode == "token" && (price.Input == nil || price.Output == nil) {
		return nil, "上游未提供完整输入输出单价"
	}
	factor := *rate.Multiplier
	if strings.Contains(price.Note, "独立倍率") {
		factor = 1
	}
	price = scaledPricing(price, factor)
	price.MultiplierIncluded, price.AppliedMultiplier = true, &factor
	if strings.ToUpper(strings.TrimSpace(account.Currency)) != "USD" {
		price.Reference, price.Note = true, "上游账户币种非 USD，此处展示站点美元参考价"
	}
	price.Note = strings.TrimPrefix(price.Note, "；")
	if !price.Reference {
		price.Note = strings.TrimSuffix(fmt.Sprintf("%s · 已应用 %.8g× 倍率", rate.Group, factor)+"；"+price.Note, "；")
	}
	price.ChannelIdentity = pricingIdentity(channel)
	return price, ""
}
