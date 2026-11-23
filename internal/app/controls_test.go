package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKeyRestrictionsBudgetAndExpiry(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatResponse(w) }))
	defer server.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "channel", server.URL, "openai")}, "priority")
	input := object{"name": "restricted", "enabled": true, "allowed_models": []string{"my-model"}, "budget_micros": 600}
	if w := call(h, "PUT", "/api/keys/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	response := call(h, "POST", "/v1/chat/completions", `{"model":"forbidden","messages":[]}`, token)
	if response.Code != 403 {
		t.Fatal("model restriction ignored")
	}
	response = call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"test"}]}`, token)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var key APIKey
	a.DB.First(&key, 1)
	if key.UsedMicros != 600 {
		t.Fatalf("budget accounting = %d", key.UsedMicros)
	}
	if response := call(h, "GET", "/v1/models", "", token); response.Code != 429 {
		t.Fatal("exhausted budget accepted")
	}
	input["budget_micros"], input["expires_at"] = nil, time.Now().Add(-time.Hour)
	if response := call(h, "PUT", "/api/keys/1", jsonString(input), cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response := call(h, "GET", "/v1/models", "", token); response.Code != 403 {
		t.Fatal("expired key accepted")
	}
	input["expires_at"], input["enabled"] = nil, false
	call(h, "PUT", "/api/keys/1", jsonString(input), cookie)
	if response := call(h, "GET", "/v1/models", "", token); response.Code != 401 {
		t.Fatal("disabled key accepted")
	}
	a.DB.First(&key, 1)
	if key.UsedMicros != 600 {
		t.Fatal("editing reset usage")
	}
}

func TestNotificationFiltersAndBusinessFailure(t *testing.T) {
	a := testApp(t)
	account := Account{ID: 9, Name: "chosen"}
	setting := Setting{RuntimeOptions: RuntimeOptions{NotificationEvents: []string{"rates_changed"}, NotificationAccounts: []uint{9}, RateChangePercent: 5}}
	if notificationAllowed(setting, account, "low_balance", "") {
		t.Fatal("event filter ignored")
	}
	before, after, percent := 1.0, 1.01, 1.0
	detail := rateChangesDetail([]rateChange{{GroupID: "g", Before: &before, After: &after, Kind: "changed", Percent: &percent}})
	if notificationAllowed(setting, account, "rates_changed", detail) {
		t.Fatal("small change pushed")
	}
	percent = 6
	detail = rateChangesDetail([]rateChange{{GroupID: "g", Before: &before, After: &after, Kind: "changed", Percent: &percent}})
	if !notificationAllowed(setting, account, "rates_changed", detail) {
		t.Fatal("meaningful change filtered")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body object
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["msg_type"] != "text" || body["sign"] == nil {
			t.Error("Feishu body or signature missing")
		}
		fmt.Fprint(w, `{"code":123,"msg":"denied"}`)
	}))
	defer server.Close()
	setting.NotificationType, setting.WebhookURL, setting.WebhookSecret = "feishu", server.URL, a.seal("test-secret")
	if err := a.sendWebhook(context.Background(), setting, Alert{Title: "test"}); err == nil {
		t.Fatal("HTTP 200 business failure accepted")
	}
}

func TestDiscoveryDoesNotReplaceSelectedModelsAndQualityUsesSummaries(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"new-model"},{"id":"kept-model"}]}`)
	}))
	defer server.Close()
	channel := Channel{Name: "discover", BaseURL: server.URL, Protocol: "openai", APIKey: a.seal("key"), Enabled: true, Models: `["manual-model","kept-model"]`}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.discoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	var current Channel
	a.DB.First(&current, channel.ID)
	if current.Models != channel.Models || !strings.Contains(current.DiscoveredModels, "new-model") || current.ModelsCheckedAt == nil {
		t.Fatal("automatic discovery replaced curated selection")
	}
	now := time.Now().Add(-time.Minute)
	rows := []RequestLog{{MetricsVersion: 2, ChannelID: channel.ID, ChannelName: channel.Name, Model: "public", Status: 200, State: "completed", CreatedAt: now, InputTokens: 20, OutputTokens: 10, CostMicros: 40, FirstTokenMS: intPointer(100), GenerationMS: intPointer(1000), DurationMS: 1100}, {ChannelID: channel.ID, ChannelName: channel.Name, Model: "public", Status: 500, State: "failed", CreatedAt: now, DurationMS: 100}, {ChannelID: channel.ID, Model: "public", State: "running", CreatedAt: now}}
	requestUsageForTest(t, a, rows)
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "GET", "/api/request-quality?group_by=model", "", cookie)
	var result struct {
		Items []qualityRow `json:"items"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Items) != 1 {
		t.Fatal(response.Body.String())
	}
	row := result.Items[0]
	if row.Requests != 2 || row.Successes != 1 || row.AverageFirstTokenMS == nil || *row.AverageFirstTokenMS != 100 || row.OutputPerSecond == nil || *row.OutputPerSecond != 10*1000.0/1200 {
		t.Fatalf("incorrect quality metrics %+v", row)
	}
	requestUsageForTest(t, a, []RequestLog{{MetricsVersion: 1, ChannelID: channel.ID, ChannelName: channel.Name, Model: "public", Status: 200, State: "completed", CreatedAt: now, OutputTokens: 10, FirstTokenMS: intPointer(900), DurationMS: 1000}})
	response = call(h, "GET", "/api/request-quality?group_by=model", "", cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Items) != 1 {
		t.Fatal(response.Body.String())
	}
	row = result.Items[0]
	if row.Requests != 3 || row.AverageFirstTokenMS == nil || *row.AverageFirstTokenMS != 100 || row.OutputPerSecond == nil || *row.OutputPerSecond != 20*1000.0/2200 {
		t.Fatalf("legacy content timing polluted semantic latency or full-duration TPS: %+v", row)
	}
}
