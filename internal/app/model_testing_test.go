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
	var requests int64
	if err := a.DB.Model(&RequestHour{}).Select("COALESCE(SUM(requests),0)").Scan(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 || requests != 0 {
		t.Fatal("tests polluted gateway request records")
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
