package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestMetricsUsagePresenceAndOutputDetection(t *testing.T) {
	for _, test := range []struct {
		protocol, body string
		input          int64
		read, write    bool
	}{
		{"openai", `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40,"cache_creation_tokens":10},"completion_tokens_details":{"reasoning_tokens":6}}}`, 100, true, true},
		{"responses", `{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40,"cache_creation_tokens":10}}}`, 100, true, true},
		{"anthropic", `{"usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":40,"cache_creation_input_tokens":10}}`, 100, true, true},
		{"openai", `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`, 100, false, false},
		{"openai", `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":0,"cache_creation_tokens":0}}}`, 100, true, true},
		{"openai", `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":40}}`, 100, true, false},
	} {
		value, _ := decodeJSON([]byte(test.body))
		result := completion{}
		result.applyUsage(obj(value), test.protocol)
		if result.Input != test.input || !result.UsageSeen || result.CacheReadSeen != test.read || result.CacheWriteSeen != test.write || result.UsageWarning != "" {
			t.Fatalf("incorrect usage presence for %s: %+v", test.body, result)
		}
	}
	for _, raw := range []string{`-1`, `1.5`, `"100"`, `1e40`, `null`} {
		value, _ := decodeJSON([]byte(`{"usage":{"prompt_tokens":` + raw + `,"completion_tokens":20}}`))
		result := completion{}
		result.applyUsage(obj(value), "openai")
		if result.UsageWarning == "" {
			t.Fatal("invalid usage accepted", raw)
		}
	}
	for _, test := range []struct {
		protocol, data string
		output         bool
	}{
		{"openai", `{"choices":[{"delta":{"role":"assistant","content":""}}]}`, false},
		{"openai", `{"usage":{"completion_tokens":20},"choices":[]}`, false},
		{"openai", `{"choices":[{"delta":{"reasoning_content":"think"}}]}`, true},
		{"openai", `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"search"}}]}}]}`, true},
		{"responses", `{"type":"response.created","response":{"id":"r"}}`, false},
		{"responses", `{"type":"response.output_text.delta","delta":"hello"}`, true},
		{"anthropic", `{"type":"message_start","message":{"role":"assistant"}}`, false},
		{"anthropic", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"think"}}`, true},
		{"anthropic", `{"type":"content_block_start","content_block":{"type":"tool_use","name":"search"}}`, true},
	} {
		if got := streamEventHasOutput(sseEvent{Data: test.data}, test.protocol); got != test.output {
			t.Fatalf("wrong first-content detection for %s", test.data)
		}
	}
	for _, test := range []struct {
		body object
		want string
	}{
		{object{"reasoning_effort": "max"}, "max"},
		{object{"reasoning": object{"effort": "high"}}, "high"},
		{object{"output_config": object{"effort": "medium"}}, "medium"},
		{object{"thinking": object{"type": "enabled", "budget_tokens": int64(1024)}}, "enabled · 1024 Token"},
		{object{"reasoning_effort": strings.Repeat("长", 100)}, strings.Repeat("长", 80)},
	} {
		if got := reasoningEffort(test.body); got != test.want {
			t.Fatalf("reasoning label %q, want %q", got, test.want)
		}
	}
}

func TestRequestMetricsCostSnapshotAndMultiplier(t *testing.T) {
	a := testApp(t)
	accountID := uint(1)
	channel := Channel{ID: 1, AccountID: &accountID, BaseURL: "https://example.invalid", APIKey: "fixture"}
	price := &modelPricing{Mode: "token", Source: "reference", Reference: true, ChannelIdentity: pricingIdentity(channel), priceRates: priceRates{Input: intPointer(2_000_000), Output: intPointer(8_000_000), CacheRead: intPointer(500_000), CacheWrite: intPointer(3_000_000)}}
	group := Group{PriceMode: "auto"}
	route := Route{Pricing: price}
	result := completion{Input: 100, Output: 20, CacheRead: 40, CacheWrite: 10, UsageSeen: true, usageDetails: usageDetails{CacheReadSeen: true, CacheWriteSeen: true}}
	factor := 0.1
	rate := requestRate{Linked: true, Multiplier: &factor, Group: "Pro"}
	var log RequestLog
	a.recordRequestCost(&log, result, group, route, channel, rate)
	if log.CostMicros != 31 || log.CostStatus != "estimated" || log.CostDetail == nil || log.CostDetail.BaseCostMicros != 310 || !log.CostDetail.MultiplierApplied || !log.UsageRecorded {
		t.Fatalf("cache pricing or multiplier incorrect: %+v", log)
	}
	// 先对完整金额乘倍率，最后只舍入一次。
	tiny := &modelPricing{Mode: "token", priceRates: priceRates{Input: intPointer(500_000), Output: intPointer(0)}}
	half := 0.5
	if cost, _, _ := pricedCost(completion{Input: 1, UsageSeen: true}, tiny, &half); cost != 0 {
		t.Fatal("multiplier applied after intermediate rounding", cost)
	}
	manual := Group{PriceMode: "manual", InputPriceMicros: 2_000_000, OutputPriceMicros: 8_000_000, Pricing: price}
	a.recordRequestCost(&log, result, manual, route, channel, rate)
	if log.CostMicros != 310 || log.CostDetail.MultiplierApplied {
		t.Fatal("manual final prices discounted a second time")
	}
	site := scaledPricing(price, factor)
	site.Reference, site.MultiplierIncluded, site.AppliedMultiplier = false, true, &factor
	a.recordRequestCost(&log, result, group, Route{Pricing: site}, channel, rate)
	if log.CostMicros != 31 || !log.CostDetail.RatesIncludeMultiplier {
		t.Fatal("site prices discounted twice")
	}
	rate.Stale = true
	a.recordRequestCost(&log, result, group, route, channel, rate)
	if log.CostMicros != 310 || log.CostWarning == "" || log.CostDetail.MultiplierApplied {
		t.Fatal("stale multiplier silently applied")
	}
	result.UsageWarning = "invalid usage"
	a.recordRequestCost(&log, result, group, route, channel, rate)
	if log.CostStatus != "unknown" || log.CostMicros != 0 {
		t.Fatal("invalid usage presented as a known charge")
	}
	a.recordRequestCost(&log, result, group, Route{}, channel, rate)
	if log.CostDetail != nil || log.CostSource != "" { t.Fatal("fallback to unpriced route kept previous attempt's price snapshot") }
	zero := 0.0
	if cost,status,_ := pricedCost(completion{Input:1,UsageSeen:true},tiny,&zero); cost != 0 || status != "estimated" { t.Fatal("explicit zero multiplier not treated as a known free price") }
	huge := 1e30
	if cost,_,warning := pricedCost(completion{Input:1,UsageSeen:true},tiny,&huge); cost <= 0 || warning == "" { t.Fatal("overflow fee did not preserve its warning") }
}

func TestRequestMetricsGatewayTimingsAndSnapshots(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "anthropic", "openai-json", "cross"} {
		t.Run(protocol, func(t *testing.T) {
			a := testApp(t)
			from, to, stream := protocol, protocol, true
			if protocol == "openai-json" {
				from, to, stream = "openai", "openai", false
			}
			if protocol == "cross" {
				from, to = "openai", "responses"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !stream {
					chatResponse(w)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(data string) { fmt.Fprintf(w, "data: %s\n\n", data); w.(http.Flusher).Flush() }
				switch from {
				case "openai":
					emit(`{"id":"upstream","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
				case "responses":
					emit(`{"type":"response.created","response":{"id":"upstream"}}`)
				case "anthropic":
					emit(`{"type":"message_start","message":{"id":"upstream","type":"message","usage":{"input_tokens":50,"output_tokens":0,"cache_read_input_tokens":40,"cache_creation_input_tokens":10}}}`)
				}
				time.Sleep(35 * time.Millisecond)
				switch from {
				case "openai":
					emit(`{"choices":[{"index":0,"delta":{"content":"hello"}}]}`)
				case "responses":
					emit(`{"type":"response.output_text.delta","delta":"hello"}`)
				case "anthropic":
					emit(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
				}
				time.Sleep(35 * time.Millisecond)
				switch from {
				case "openai":
					emit(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40,"cache_creation_tokens":10},"completion_tokens_details":{"reasoning_tokens":6}}}`)
					emit(`[DONE]`)
				case "responses":
					emit(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40,"cache_creation_tokens":10},"output_tokens_details":{"reasoning_tokens":6}}}}`)
				case "anthropic":
					emit(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)
					emit(`{"type":"message_stop"}`)
				}
			}))
			defer upstream.Close()
			h := a.Router()
			cookie := loginForTest(t, h)
			channel := createChannel(t, h, cookie, "original channel", upstream.URL, from)
			group := Group{Name: "client-alias", Strategy: "weighted", PriceMode: "manual", InputPriceMicros: 2_000_000, OutputPriceMicros: 8_000_000, Pricing: &modelPricing{priceRates: priceRates{CacheRead: intPointer(500_000), CacheWrite: intPointer(3_000_000)}}, Routes: []Route{{ChannelID: channel.ID, Model: "actual-upstream-model", Weight: 1}}}
			if err := a.DB.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			keyResponse := call(h, "POST", "/api/keys", `{"name":"Ubuntu"}`, cookie)
			var key struct{ Token string }
			if json.Unmarshal(keyResponse.Body.Bytes(), &key) != nil || key.Token == "" {
				t.Fatal("key creation failed")
			}
			body := object{"model": group.Name, "stream": stream, "messages": []any{object{"role": "user", "content": "hello"}}}
			if to == "responses" {
				delete(body, "messages")
				body["input"] = "hello"
			}
			if protocol != "cross" {
				switch to {
				case "openai":
					body["reasoning_effort"] = "max"
				case "responses":
					body["reasoning"] = object{"effort": "max"}
				case "anthropic":
					body["output_config"] = object{"effort": "max"}
				}
			}
			response := call(h, "POST", requestPath(to), jsonString(body), key.Token)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			if log.MetricsVersion != 1 || log.KeyName != "Ubuntu" || log.ChannelName != channel.Name || log.Model != group.Name || log.UpstreamModel != "actual-upstream-model" || log.Protocol != to || log.UpstreamProtocol != from || !log.UsageRecorded || log.StartedAt == nil || log.FinishedAt == nil {
				t.Fatalf("missing request snapshot: %+v", log)
			}
			if protocol != "cross" && log.ReasoningEffort != "max" {
				t.Fatal("reasoning effort not retained")
			}
			if stream {
				if log.FirstTokenMS == nil || *log.FirstTokenMS < 25 || log.GenerationMS == nil || *log.GenerationMS < 25 || log.FirstByteMS == nil || *log.FirstByteMS >= *log.FirstTokenMS || log.CostMicros != 310 || log.InputTokens != 100 || !log.CacheReadRecorded || log.CacheReadTokens != 40 {
					t.Fatalf("metadata counted as first content or invalid cost: %+v", log)
				}
			} else if log.FirstTokenMS != nil || log.GenerationMS != nil || log.CostMicros != 600 || log.CacheReadRecorded {
				t.Fatal("non-stream latency or unknown cache usage invented")
			}
			if err := a.DB.Model(&channel).Update("name", "renamed channel").Error; err != nil {
				t.Fatal(err)
			}
			var saved RequestLog
			if err := a.DB.First(&saved, log.ID).Error; err != nil || saved.ChannelName != "original channel" || saved.KeyName != "Ubuntu" || saved.CostDetail == nil {
				t.Fatal("history depends on current channel or missing price snapshot")
			}
		})
	}
}
