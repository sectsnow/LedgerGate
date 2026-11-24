package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func runModelTest(t *testing.T, h http.Handler, cookie string, channel Channel, input object) modelTestResult {
	t.Helper()
	w := call(h, "POST", fmt.Sprintf("/api/channels/%d/model-tests", channel.ID), jsonString(input), cookie)
	var result modelTestResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("model test %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-upstream-key") || strings.Contains(w.Body.String(), "alternate-private-key") {
		t.Fatal("test response exposed upstream credentials")
	}
	return result
}

func TestModelTestingProtocolsAndKeys(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	var expectedKey atomic.Value
	expectedKey.Store("private-upstream-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body object
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "test-model" {
			t.Error("wrong model or request")
		}
		if r.URL.Path == "/v1/chat/completions" && body["max_tokens"] == nil && body["max_completion_tokens"] == nil {
			t.Error("output limit missing")
		}
		key := expectedKey.Load().(string)
		if r.URL.Path == "/v1/chat/completions" && key == "alternate-private-key" && (body["max_tokens"] != nil || integer(body["max_completion_tokens"]) != 512) {
			t.Error("new OpenAI output limit was not used exclusively")
		}
		if r.Header.Get("Authorization") != "Bearer "+key && r.Header.Get("x-api-key") != key {
			t.Error("wrong selected key")
		}
		stream := body["stream"] == true
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			if !stream {
				fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
				return
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\ndata: [DONE]\n\n")
		case "/v1/responses":
			if !stream {
				fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`)
				return
			}
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n")
		case "/v1/messages":
			if r.Header.Get("anthropic-version") == "" {
				t.Error("missing Anthropic version")
			}
			if !stream {
				fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":5}}`)
				return
			}
			fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "test target", upstream.URL, "openai")
	alternate := createChannel(t, h, cookie, "alternate key", upstream.URL, "openai")
	if a.DB.Model(&alternate).Update("api_key", a.seal("alternate-private-key")).Error != nil {
		t.Fatal("seed key")
	}
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				result := runModelTest(t, h, cookie, channel, object{"model": "test-model", "protocol": protocol, "prompt": "OK?", "max_tokens": 512, "stream": stream})
				if !result.Success || result.Text != "OK" || result.Status != 200 || result.InputTokens == nil || *result.InputTokens != 10 || result.OutputTokens == nil || *result.OutputTokens != 5 {
					t.Fatalf("wrong test result %+v", result)
				}
				if stream != (result.FirstTokenMS != nil) || stream != (result.FirstContentMS != nil) {
					t.Fatal("first token timing must only exist for streaming output")
				}
				if protocol == "openai" && stream && (*result.FirstTokenMS < 5 || result.CacheRead == nil || *result.CacheRead != 2) {
					t.Fatal("role-only chunk counted as first token or cache usage lost")
				}
			})
		}
	}
	expectedKey.Store("alternate-private-key")
	for _, keyInput := range []object{{"key_channel_id": alternate.ID}, {"api_key": "alternate-private-key"}} {
		input := object{"model": "test-model", "protocol": "openai", "prompt": "OK?", "max_tokens": 512, "limit_field": "max_completion_tokens"}
		for key, value := range keyInput {
			input[key] = value
		}
		if result := runModelTest(t, h, cookie, channel, input); !result.Success {
			t.Fatal(result.Error)
		}
		wantKeyName := "模型测试 · 临时 Key"
		if keyInput["key_channel_id"] != nil {
			wantKeyName = fmt.Sprintf("模型测试 · 渠道 #%d Key", alternate.ID)
		}
		if log := latestRequestForTest(t, a); log.KeyName != wantKeyName || log.CostStatus != "unknown" {
			t.Fatal("selected key was not identified or used another key's price", log)
		}
	}
	var saved Channel
	if a.DB.First(&saved, channel.ID).Error != nil {
		t.Fatal("read channel")
	}
	key, _ := a.unseal(saved.APIKey)
	if key != "private-upstream-key" || !saved.Enabled || len(a.health) != 0 || calls.Load() != 8 {
		t.Fatal("testing mutated the channel or used a fallback")
	}
	logs, _ := a.requestSummaries()
	var summary RequestDay
	if err := a.DB.First(&summary).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 8 || summary.Requests != 8 || summary.InputTokens != 80 || summary.OutputTokens != 40 || summary.CostUnknownRequests != 8 {
		t.Fatalf("model test usage missing from statistics: %+v", summary)
	}
}

func TestModelTestingCostAndRequestAccounting(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":600,"cache_creation_tokens":300,"cache_creation_1h_tokens":100}}}`)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "priced", upstream.URL, "openai")
	var stored Channel
	if err := a.DB.First(&stored, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	account := Account{Name: "priced account", Status: "ok"}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	stored.AccountID = &account.ID
	if err := a.DB.Model(&stored).Update("account_id", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	factor := 0.5
	rates := rateSnapshot{Channels: map[uint]channelRate{stored.ID: {upstreamRate: upstreamRate{Multiplier: &factor, Group: "fixture"}, Identity: channelRateIdentity(stored)}}}
	if err := a.DB.Model(&account).Update("rates", jsonString(rates)).Error; err != nil {
		t.Fatal(err)
	}
	price := cacheTestPrice()
	price.Reference, price.ChannelIdentity = true, pricingIdentity(stored)
	group := Group{Name: "alias", Strategy: "priority", PriceMode: "auto", Routes: []Route{{ChannelID: stored.ID, Model: "priced-model", Weight: 1, Pricing: price}}}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	input := object{"model": "priced-model", "protocol": "openai", "prompt": "secret test prompt", "max_tokens": 512}
	result := runModelTest(t, h, cookie, stored, input)
	if !result.Success || result.CostStatus != "estimated" || result.CostMicros != 1110 || result.CostWarning != "" || result.TrackingWarning != "" {
		t.Fatalf("priced test result incorrect: %+v", result)
	}
	log := latestRequestForTest(t, a)
	if log.KeyID != 0 || log.KeyName != "模型测试" || log.AccountName != account.Name || log.ChannelID != stored.ID || log.Model != "priced-model" || log.CostMicros != 1110 || log.CostDetail == nil || !log.CostDetail.MultiplierApplied || log.CacheWrite1hTokens != 100 {
		t.Fatalf("test request did not retain cost and identity: %+v", log)
	}
	_, detail, found := a.cachedRequestDetail(log.ID)
	if !found || strings.Contains(detail.RequestBody, "secret test prompt") || len(detail.Attempts) != 1 || detail.Attempts[0].Status != 200 {
		t.Fatalf("test request detail invalid: %+v", detail)
	}
	var summary RequestDay
	if err := a.DB.First(&summary).Error; err != nil || summary.Requests != 1 || summary.CostMicros != 1110 || summary.CacheReadTokens != 600 || summary.CacheWriteTokens != 300 || summary.CostUnknownRequests != 0 {
		t.Fatalf("priced test missing from daily statistics: %+v %v", summary, err)
	}

	input["api_key"] = "alternate-private-key"
	result = runModelTest(t, h, cookie, stored, input)
	if result.CostStatus != "unknown" || result.CostWarning == "" {
		t.Fatal("temporary key used the saved key's price", result)
	}
	if log := latestRequestForTest(t, a); log.KeyName != "模型测试 · 临时 Key" {
		t.Fatal("temporary key was not identified in request history", log.KeyName)
	}
	delete(input, "api_key")
	second := Group{Name: "other alias", Strategy: "priority", PriceMode: "manual", Routes: []Route{{ChannelID: stored.ID, Model: "priced-model", Weight: 1}}}
	if err := a.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	result = runModelTest(t, h, cookie, stored, input)
	if result.CostStatus != "unknown" || !strings.Contains(result.CostWarning, "多个路由") {
		t.Fatal("ambiguous routes used an arbitrary price", result)
	}
	if err := a.DB.First(&summary).Error; err != nil || summary.Requests != 3 || summary.CostMicros != 1110 || summary.CostUnknownRequests != 2 {
		t.Fatalf("unknown costs were not tracked: %+v %v", summary, err)
	}
}

func TestModelTestingIncompleteAndFailedAccounting(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body object
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt := str(obj(array(body["messages"])[0])["content"])
		switch prompt {
		case "partial usage":
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":10}}`)
		case "no text":
			fmt.Fprint(w, `{"choices":[{"message":{"content":""}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		case "partial stream":
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		case "auth":
			w.Header().Set("X-Request-ID", "private-upstream-key")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"invalid private-upstream-key"}}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		}
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "partial", upstream.URL, "openai")
	group := Group{Name: "partial alias", Strategy: "priority", PriceMode: "manual", InputPriceMicros: 2_000_000, OutputPriceMicros: 8_000_000, Routes: []Route{{ChannelID: channel.ID, Model: "partial-model", Weight: 1}}}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	input := object{"model": "partial-model", "protocol": "openai", "prompt": "partial usage", "max_tokens": 512}
	result := runModelTest(t, h, cookie, channel, input)
	if !result.Success || result.CostStatus != "unknown" || result.UsageWarning == "" || result.OutputTokens != nil {
		t.Fatal("incomplete token usage was priced as complete", result)
	}
	input["prompt"] = "no text"
	result = runModelTest(t, h, cookie, channel, input)
	if result.Success || result.CostStatus != "estimated" || result.CostMicros != 60 {
		t.Fatal("completed but failed test lost known usage cost", result)
	}
	input["prompt"], input["stream"] = "partial stream", true
	result = runModelTest(t, h, cookie, channel, input)
	if result.Success || result.CostStatus != "unknown" || !strings.Contains(result.CostWarning, "未完整结束") {
		t.Fatal("interrupted stream was priced as complete", result)
	}
	input["prompt"], input["stream"] = "auth", false
	result = runModelTest(t, h, cookie, channel, input)
	log := latestRequestForTest(t, a)
	_, detail, found := a.cachedRequestDetail(log.ID)
	if result.Success || result.CostStatus != "unknown" || !found || strings.Contains(jsonString(detail), "private-upstream-key") {
		t.Fatal("failed request exposed its key or invented a fee", result, detail)
	}
	if err := a.DB.Exec("CREATE TRIGGER reject_test_summary BEFORE INSERT ON request_days BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	input["prompt"] = "OK"
	result = runModelTest(t, h, cookie, channel, input)
	if !result.Success || result.TrackingWarning == "" {
		t.Fatal("statistics storage failure was hidden", result)
	}
	var summary RequestDay
	if err := a.DB.First(&summary).Error; err != nil || summary.Requests != 4 || summary.CostMicros != 60 || summary.CostUnknownRequests != 3 {
		t.Fatalf("failed or incomplete requests were miscounted: %+v %v", summary, err)
	}
}

func TestModelTestingConcurrentAccounting(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "concurrent", upstream.URL, "openai")
	group := Group{Name: "concurrent alias", Strategy: "priority", PriceMode: "manual", InputPriceMicros: 2_000_000, OutputPriceMicros: 8_000_000, Routes: []Route{{ChannelID: channel.ID, Model: "concurrent-model", Weight: 1}}}
	key := APIKey{Name: "gateway budget", Digest: "gateway-budget", UsedMicros: 7}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan modelTestResult, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := call(h, "POST", fmt.Sprintf("/api/channels/%d/model-tests", channel.ID), `{"model":"concurrent-model","protocol":"openai","prompt":"OK?","max_tokens":512}`, cookie)
			var result modelTestResult
			if w.Code == 200 {
				_ = json.Unmarshal(w.Body.Bytes(), &result)
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if !result.Success || result.CostStatus != "estimated" || result.CostMicros != 60 || result.TrackingWarning != "" {
			t.Fatal("concurrent test result incorrect", result)
		}
	}
	var day RequestDay
	var hour RequestHour
	if err := a.DB.First(&day).Error; err != nil || day.Requests != 10 || day.CostMicros != 600 || day.InputTokens != 100 || day.OutputTokens != 50 {
		t.Fatalf("concurrent daily totals incorrect: %+v %v", day, err)
	}
	if err := a.DB.First(&hour).Error; err != nil || hour.Requests != 10 || hour.CostMicros != 600 {
		t.Fatalf("concurrent hourly totals incorrect: %+v %v", hour, err)
	}
	if err := a.DB.First(&key, key.ID).Error; err != nil || key.UsedMicros != 7 {
		t.Fatal("model tests charged a gateway key budget", key, err)
	}
}

func TestModelTestingFailuresAndValidation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body object
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["model"] {
		case "bad-auth":
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"invalid private-upstream-key"}}`)
		case "invalid-json":
			fmt.Fprint(w, "<html>not JSON</html>")
		case "json-error":
			fmt.Fprint(w, `{"error":{"message":"unavailable model"}}`)
		case "empty":
			fmt.Fprint(w, `{"choices":[{"message":{"content":""}}]}`)
		case "early-end":
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		case "stream-error":
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"model not found\"}}\n\n")
		case "echo-key":
			fmt.Fprint(w, `{"choices":[{"message":{"content":"private-upstream-key"}}]}`)
		}
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "failure target", upstream.URL, "openai")
	for _, model := range []string{"bad-auth", "invalid-json", "json-error", "empty", "early-end", "stream-error"} {
		result := runModelTest(t, h, cookie, channel, object{"model": model, "prompt": "OK?", "protocol": "openai", "max_tokens": 512, "stream": model == "early-end" || model == "stream-error"})
		if result.Success || result.Error == "" {
			t.Fatal("failure was reported as success", model)
		}
		if model == "bad-auth" && (result.Status != 401 || !strings.Contains(result.Error, "[已隐藏 Key]")) {
			t.Fatal("HTTP error or redaction lost")
		}
	}
	if result := runModelTest(t, h, cookie, channel, object{"model": "echo-key", "prompt": "OK?", "protocol": "openai", "max_tokens": 512}); !result.Success || result.Text != "[已隐藏 Key]" {
		t.Fatal("response text exposed credentials")
	}
	path := fmt.Sprintf("/api/channels/%d/model-tests", channel.ID)
	valid := object{"model": "echo-key", "prompt": "OK?", "protocol": "openai", "max_tokens": 512}
	for field, value := range (object{"model": "bad\nmodel", "prompt": "", "protocol": "invalid", "max_tokens": 8193, "limit_field": "invalid", "api_key": "invalid\nkey", "key_channel_id": 999999}) {
		input := object{}
		for name, current := range valid {
			input[name] = current
		}
		input[field] = value
		if w := call(h, "POST", path, jsonString(input), cookie); w.Code != 400 {
			t.Fatal("invalid input accepted", field, w.Code)
		}
	}
	if w := call(h, "POST", path, jsonString(valid), ""); w.Code != 401 {
		t.Fatal("unauthenticated test allowed")
	}
	request := httptest.NewRequest("POST", path, strings.NewReader(jsonString(valid)))
	request.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("test without CSRF header allowed")
	}
	a.mu.Lock()
	a.modelTests = 10
	a.mu.Unlock()
	if w := call(h, "POST", path, jsonString(valid), cookie); w.Code != 429 {
		t.Fatal("concurrency limit ignored")
	}
	a.mu.Lock()
	a.modelTests = 0
	a.mu.Unlock()
	if calls.Load() != 7 || len(a.health) != 0 {
		t.Fatal("invalid request reached upstream or altered health")
	}
	foreign := createChannel(t, h, cookie, "different site", "https://different.invalid", "openai")
	if w := call(h, "POST", path, jsonString(object{"model": "echo-key", "protocol": "openai", "prompt": "OK?", "max_tokens": 512, "key_channel_id": foreign.ID}), cookie); w.Code != 400 || calls.Load() != 7 {
		t.Fatal("key could be sent to another upstream")
	}
}

func TestModelTestingRemovalRollback(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "rollback", "https://unused.invalid", "openai")
	if a.DB.Model(&channel).Update("models", `["bad"]`).Error != nil {
		t.Fatal("seed models")
	}
	group := Group{Name: "rollback", Strategy: "manual", Routes: []Route{{ChannelID: channel.ID, Model: "bad", Weight: 1}}}
	if a.DB.Create(&group).Error != nil || a.DB.Model(&group).Update("active_route_id", group.Routes[0].ID).Error != nil {
		t.Fatal("seed route")
	}
	var saved Channel
	a.DB.First(&saved, channel.ID)
	inventory, err := loadModelTestModels(a.DB, saved)
	if err != nil {
		t.Fatal(err)
	}
	if a.DB.Exec("CREATE TRIGGER reject_test_route_delete BEFORE DELETE ON routes BEGIN SELECT RAISE(ABORT, 'simulated failure'); END").Error != nil {
		t.Fatal("seed failure")
	}
	w := call(h, "POST", fmt.Sprintf("/api/channels/%d/models/remove", channel.ID), jsonString(object{"models": []string{"bad"}, "revision": inventory.Revision}), cookie)
	if w.Code != 500 {
		t.Fatal("database failure hidden", w.Body.String())
	}
	a.DB.First(&saved, channel.ID)
	var current Group
	a.DB.Preload("Routes").First(&current, group.ID)
	if saved.Models != `["bad"]` || current.ActiveRouteID != group.Routes[0].ID || len(current.Routes) != 1 {
		t.Fatal("failed removal left partial changes")
	}
}

func TestModelTestingCancellation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	entered, stopped := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(stopped)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "cancel", upstream.URL, "openai")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/channels/%d/model-tests", channel.ID), strings.NewReader(`{"model":"test","protocol":"openai","prompt":"OK?","max_tokens":512}`)).WithContext(ctx)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-LedgerGate-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	finished := make(chan struct{})
	go func() { h.ServeHTTP(httptest.NewRecorder(), req); close(finished) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not called")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("test did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request did not cancel")
	}
	a.mu.Lock()
	active := a.modelTests
	a.mu.Unlock()
	if active != 0 {
		t.Fatal("cancelled test did not release concurrency slot")
	}
	log := latestRequestForTest(t, a)
	if log.Status != 499 || log.State != "cancelled" || log.CostStatus != "unknown" {
		t.Fatalf("cancelled upstream request was not accounted for: %+v", log)
	}
	var summary RequestDay
	if err := a.DB.First(&summary).Error; err != nil || summary.Requests != 1 || summary.CostUnknownRequests != 1 {
		t.Fatalf("cancelled test missing from statistics: %+v %v", summary, err)
	}
}

func TestModelTestingInventoryAndRemoval(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer alternate-private-key" {
			t.Error("inventory used the wrong Key or endpoint")
		}
		fmt.Fprint(w, `{"data":[{"id":"remote"}]}`)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "target", upstream.URL, "openai")
	other := createChannel(t, h, cookie, "other", upstream.URL, "openai")
	a.DB.Model(&other).Update("api_key", a.seal("alternate-private-key"))
	if a.DB.Model(&channel).Update("models", `["bad","good"]`).Error != nil {
		t.Fatal("seed models")
	}
	group := Group{Name: "test-alias", Strategy: "manual", Routes: []Route{{ChannelID: channel.ID, Model: "bad", Weight: 1}, {ChannelID: other.ID, Model: "bad", Weight: 1}, {ChannelID: channel.ID, Model: "route-only", Weight: 1}}}
	if a.DB.Create(&group).Error != nil {
		t.Fatal("seed routes")
	}
	if a.DB.Model(&group).Update("active_route_id", group.Routes[0].ID).Error != nil {
		t.Fatal("seed selection")
	}
	base := fmt.Sprintf("/api/channels/%d", channel.ID)
	load := func() modelTestModels {
		t.Helper()
		w := call(h, "GET", base+"/model-tests/models", "", cookie)
		var inventory modelTestModels
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &inventory) != nil {
			t.Fatal(w.Body.String())
		}
		return inventory
	}
	inventory := load()
	if !reflect.DeepEqual(inventory.Models, []string{"bad", "good", "route-only"}) {
		t.Fatal("saved and routed models were not combined", inventory.Models)
	}
	w := call(h, "POST", base+"/model-tests/models", jsonString(object{"protocol": "openai", "key_channel_id": other.ID}), cookie)
	var preview modelTestModels
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || !slices.Contains(preview.Models, "remote") {
		t.Fatal("selected-key model retrieval failed", w.Body.String())
	}
	var before Channel
	a.DB.First(&before, channel.ID)
	if before.Models != `["bad","good"]` {
		t.Fatal("getting models changed the saved list")
	}
	request := object{"models": []string{"bad"}, "revision": inventory.Revision}
	if w := call(h, "POST", base+"/models/remove", jsonString(request), ""); w.Code != 401 {
		t.Fatal("unauthenticated removal allowed")
	}
	if w := call(h, "POST", base+"/models/remove", jsonString(request), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved Channel
	a.DB.First(&saved, channel.ID)
	if saved.Models != `["good"]` || saved.APIKey != before.APIKey || saved.Enabled != before.Enabled {
		t.Fatal("removed wrong model or changed channel configuration")
	}
	var current Group
	a.DB.Preload("Routes").First(&current, group.ID)
	if current.ActiveRouteID != 0 || len(current.Routes) != 2 || current.Routes[0].ChannelID != other.ID {
		t.Fatal("wrong routes removed or manual selection retained")
	}
	if w := call(h, "POST", base+"/models/remove", jsonString(request), cookie); w.Code != 409 {
		t.Fatal("stale inventory was accepted")
	}
	inventory = load()
	added := Route{GroupID: group.ID, ChannelID: channel.ID, Model: "new-route", Weight: 1}
	if a.DB.Create(&added).Error != nil {
		t.Fatal("seed concurrent route")
	}
	if w := call(h, "POST", base+"/models/remove", jsonString(object{"models": []string{"good"}, "revision": inventory.Revision}), cookie); w.Code != 409 {
		t.Fatal("concurrent route edit was overwritten")
	}
}
