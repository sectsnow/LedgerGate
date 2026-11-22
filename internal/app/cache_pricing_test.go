package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func cachedUsageFixture(protocol string) object {
	switch protocol {
	case "openai":
		return object{"usage": object{"prompt_tokens": 1000, "completion_tokens": 100, "prompt_tokens_details": object{"cached_tokens": 600, "cache_creation_tokens": 300, "cache_creation_1h_tokens": 100}}}
	case "responses":
		return object{"usage": object{"input_tokens": 1000, "output_tokens": 100, "input_tokens_details": object{"cached_tokens": 600, "cache_creation_tokens": 300, "cache_creation_1h_tokens": 100}}}
	default:
		return object{"usage": object{"input_tokens": 100, "output_tokens": 100, "cache_read_input_tokens": 600, "cache_creation_input_tokens": 300, "cache_creation": object{"ephemeral_5m_input_tokens": 200, "ephemeral_1h_input_tokens": 100}}}
	}
}

func cacheTestPrice() *modelPricing {
	return &modelPricing{Mode: "token", Source: "fixture", priceRates: priceRates{Input: intPointer(2_000_000), Output: intPointer(10_000_000), CacheRead: intPointer(200_000), CacheWrite: intPointer(2_500_000), CacheWrite1h: intPointer(4_000_000)}}
}

func TestCacheUsageRoundTripAndCost(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var result completion
			result.applyUsage(cachedUsageFixture(protocol), protocol)
			if result.Input != 1000 || result.Output != 100 || result.CacheRead != 600 || result.CacheWrite != 300 || result.CacheWrite1h != 100 {
				t.Fatalf("cache usage lost or double counted: %+v", result)
			}
			if price, status, warning := pricedCost(result, cacheTestPrice()); price != 2220 || status != "estimated" || warning != "" {
				t.Fatalf("cache cost %d/%s/%s, want 2220 microdollars", price, status, warning)
			}
			for _, outputProtocol := range []string{"openai", "responses", "anthropic"} {
				var converted completion
				converted.applyUsage(object{"usage": result.usageBody(outputProtocol)}, outputProtocol)
				if converted.Input != result.Input || converted.Output != result.Output || converted.CacheRead != result.CacheRead || converted.CacheWrite != result.CacheWrite || converted.CacheWrite1h != result.CacheWrite1h {
					t.Fatalf("conversion %s -> %s lost cache usage", protocol, outputProtocol)
				}
			}
		})
	}
}

func TestStreamingCacheUsagePreservedAcrossPartialUpdates(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var result completion
			fixture := cachedUsageFixture(protocol)
			if protocol == "anthropic" {
				start := object{"type": "message_start", "message": fixture}
				if _, _, err := inspectEvent(sseEvent{Data: jsonString(start)}, protocol, &result, false); err != nil {
					t.Fatal(err)
				}
				if _, _, err := inspectEvent(sseEvent{Data: `{"type":"message_delta","usage":{"output_tokens":100},"delta":{"stop_reason":"end_turn"}}`}, protocol, &result, false); err != nil {
					t.Fatal(err)
				}
			} else if protocol == "responses" {
				if _, _, err := inspectEvent(sseEvent{Data: jsonString(object{"type": "response.completed", "response": fixture})}, protocol, &result, false); err != nil {
					t.Fatal(err)
				}
			} else {
				fixture["choices"] = []any{}
				if _, _, err := inspectEvent(sseEvent{Data: jsonString(fixture)}, protocol, &result, false); err != nil {
					t.Fatal(err)
				}
			}
			if result.Input != 1000 || result.CacheRead != 600 || result.CacheWrite1h != 100 || result.Output != 100 {
				t.Fatalf("stream lost cumulative usage: %+v", result)
			}
			if price, _, warning := pricedCost(result, cacheTestPrice()); price != 2220 || warning != "" {
				t.Fatal("stream cost differs from non-stream response")
			}
		})
	}
}

func TestCachePricingUnknownZeroAndTiers(t *testing.T) {
	var result completion
	result.applyUsage(cachedUsageFixture("openai"), "openai")
	price := cacheTestPrice()
	price.CacheRead = intPointer(0)
	if amount, _, warning := pricedCost(result, price); amount != 2100 || warning != "" {
		t.Fatal("explicit free cache price lost")
	}
	price.CacheRead = nil
	if amount, _, warning := pricedCost(result, price); amount != 3300 || !strings.Contains(warning, "缓存读取") {
		t.Fatal("missing cache price must use disclosed input fallback")
	}
	price.Tiers = []priceTier{{Min: 0, Max: intPointer(1001), priceRates: cacheTestPrice().priceRates}, {Min: 1001, priceRates: priceRates{Input: intPointer(4_000_000), Output: intPointer(20_000_000), CacheRead: intPointer(400_000), CacheWrite: intPointer(5_000_000), CacheWrite1h: intPointer(8_000_000)}}}
	if amount, _, _ := pricedCost(result, price); amount != 2220 {
		t.Fatal("lower tier boundary wrong")
	}
	result.Input++
	if amount, _, _ := pricedCost(result, price); amount != 4444 {
		t.Fatalf("upper tier boundary wrong: %d", amount)
	}
	if _, status, _ := pricedCost(completion{}, price); status != "unknown" {
		t.Fatal("missing usage displayed as a free request")
	}
	if _, status, _ := pricedCost(result, &modelPricing{Mode: "image", UnitPrice: intPointer(40000)}); status != "unknown" {
		t.Fatal("image request priced without image quantity")
	}
}

func TestGatewayPersistsCacheCostAcrossProtocolConversion(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	a := testApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := completionBody(completion{ID: "cached", Text: "ok", Input: 1000, Output: 100, CacheRead: 600, CacheWrite: 300, CacheWrite1h: 100, Finish: "stop"}, "anthropic", "upstream-model")
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprint(w, jsonString(body))
	}))
	defer upstream.Close()
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "cache", upstream.URL, "anthropic")
	key := createGroup(t, h, cookie, []Channel{channel}, "priority")
	var group Group
	a.DB.First(&group)
	manual := cacheTestPrice()
	if err := a.DB.Model(&group).Updates(map[string]any{"price_mode": "manual", "input_price_micros": 2_000_000, "output_price_micros": 10_000_000, "pricing": jsonString(manual)}).Error; err != nil {
		t.Fatal(err)
	}
	response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, key)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var converted object
	if json.Unmarshal(response.Body.Bytes(), &converted) != nil {
		t.Fatal("invalid gateway response")
	}
	var output completion
	output.applyUsage(converted, "openai")
	if output.Input != 1000 || output.CacheRead != 600 || output.CacheWrite1h != 100 {
		t.Fatal("client cache usage lost")
	}
	var record RequestLog
	if err := a.DB.Last(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.CostMicros != 2220 || record.CacheReadTokens != 600 || record.CacheWriteTokens != 300 || record.CacheWrite1hTokens != 100 || record.CostStatus != "estimated" {
		t.Fatalf("request cache accounting wrong: %+v", record)
	}
}

func TestManualCachePricesPersistAndRejectInvalidValues(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "manual cache", "https://example.invalid", "openai")
	input := Group{Name: "cache-manual", PriceMode: "manual", Strategy: "priority", InputPriceMicros: 2_000_000, OutputPriceMicros: 10_000_000,
		Pricing: &modelPricing{priceRates: priceRates{CacheRead: intPointer(0), CacheWrite: intPointer(2_500_000)}},
		Routes:  []Route{{ChannelID: channel.ID, Model: "upstream", Weight: 1}}}
	response := call(h, "POST", "/api/groups", jsonString(input), cookie)
	var saved Group
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &saved) != nil {
		t.Fatal(response.Body.String())
	}
	if saved.Pricing == nil || saved.Pricing.CacheRead == nil || *saved.Pricing.CacheRead != 0 || saved.Pricing.CacheWrite1h != nil || *saved.Pricing.CacheWrite != 2_500_000 || saved.Pricing.Source != "manual" {
		t.Fatal("manual cache zero, missing, or provenance fields lost")
	}
	saved.Pricing.CacheWrite1h = intPointer(4_000_000)
	response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", saved.ID), jsonString(saved), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var stored Group
	if err := a.DB.Preload("Routes").First(&stored, saved.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Pricing == nil || stored.Pricing.CacheWrite1h == nil || *stored.Pricing.CacheWrite1h != 4_000_000 {
		t.Fatal("manual cache write TTL not persisted")
	}
	for _, value := range []int64{-1, 1_000_000_000_001} {
		saved.Pricing.CacheRead = &value
		response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", saved.ID), jsonString(saved), cookie)
		if response.Code != 400 {
			t.Fatal("invalid cache price accepted")
		}
	}
}
