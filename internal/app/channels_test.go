package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func createMultiProtocolChannel(t *testing.T, h http.Handler, cookie, base string, protocols []string) Channel {
	t.Helper()
	w := call(h, "POST", "/api/channels", jsonString(object{"name": "multi-protocol", "base_url": base, "protocols": protocols, "api_key": "private-upstream-key", "enabled": true, "timeout_seconds": 5, "cooldown_seconds": 60}), cookie)
	if w.Code != 200 {
		t.Fatalf("channel creation failed %d %s", w.Code, w.Body.String())
	}
	var channel Channel
	if err := json.Unmarshal(w.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channel.Protocols, protocols) {
		t.Fatalf("protocol selection lost %v", channel.Protocols)
	}
	return channel
}

func TestMultiProtocolNativeForwarding(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body object
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
			return
		}
		if body["model"] != "upstream-model" {
			t.Error("model mapping failed")
		}
		protocol := "openai"
		switch r.URL.Path {
		case "/v1/responses":
			protocol = "responses"
			if body["previous_response_id"] != "resp_prior" {
				t.Error("native Responses state lost")
			}
		case "/v1/messages":
			protocol = "anthropic"
			if obj(body["thinking"])["type"] != "enabled" {
				t.Error("native Anthropic field lost")
			}
		case "/v1/chat/completions":
			if body["reasoning_effort"] != "high" {
				t.Error("native OpenAI field lost")
			}
		default:
			t.Error("wrong upstream endpoint")
		}
		if protocol == "anthropic" {
			if r.Header.Get("x-api-key") != "private-upstream-key" || r.Header.Get("anthropic-version") == "" {
				t.Error("wrong Anthropic auth")
			}
		} else if r.Header.Get("Authorization") != "Bearer private-upstream-key" {
			t.Error("wrong bearer auth")
		}
		requests.Add(1)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, streamFixture(protocol))
			return
		}
		response, err := completionBody(completion{ID: "native", Text: "hello", Input: 12, Output: 7, Finish: "stop"}, protocol, "upstream-model")
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jsonString(response))
	}))
	defer upstream.Close()
	channel := createMultiProtocolChannel(t, h, cookie, upstream.URL, []string{"openai", "responses", "anthropic"})
	token := createGroup(t, h, cookie, []Channel{channel}, "priority")
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			body := object{"model": "my-model", "stream": stream, "messages": []any{object{"role": "user", "content": "hi"}}}
			switch protocol {
			case "openai":
				body["reasoning_effort"] = "high"
			case "responses":
				delete(body, "messages")
				body["input"], body["previous_response_id"] = "hi", "resp_prior"
			case "anthropic":
				body["max_tokens"], body["thinking"] = 4096, object{"type": "enabled", "budget_tokens": 1024}
			}
			w := call(h, "POST", requestPath(protocol), jsonString(body), token)
			if w.Code != 200 || (stream && !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream")) {
				t.Fatalf("%s stream=%v %d %s", protocol, stream, w.Code, w.Body.String())
			}
		}
	}
	if requests.Load() != 6 {
		t.Fatalf("missing native requests %d", requests.Load())
	}
}

func TestMultiProtocolConversionSelection(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("expected convertible Anthropic route, got %s", r.URL.Path)
		}
		var body object
		json.NewDecoder(r.Body).Decode(&body)
		if jsonString(body["stop_sequences"]) != `["END"]` {
			t.Error("stop condition lost")
		}
		response, _ := completionBody(completion{ID: "converted", Text: "ok", Finish: "stop"}, "anthropic", "upstream-model")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jsonString(response))
	}))
	defer upstream.Close()
	channel := createMultiProtocolChannel(t, h, cookie, upstream.URL, []string{"responses", "anthropic"})
	token := createGroup(t, h, cookie, []Channel{channel}, "priority")
	w := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hi"}],"stop":"END"}`, token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"content":"ok"`) {
		t.Fatal(w.Body.String())
	}
}

func TestMultiProtocolModelsAndValidation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	var deny atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if deny.Load() {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-api-key") != "" {
			fmt.Fprint(w, `{"data":[{"id":"shared"},{"id":"claude-model"}]}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"shared"},{"id":"chat-model"}]}`)
		}
	}))
	defer upstream.Close()
	channel := createMultiProtocolChannel(t, h, cookie, upstream.URL, []string{"responses", "openai", "anthropic"})
	w := call(h, "POST", fmt.Sprintf("/api/channels/%d/models", channel.ID), "", cookie)
	if w.Code != 200 || w.Body.String() != `["shared","chat-model","claude-model"]` || calls.Load() != 2 {
		t.Fatalf("model merge/auth dedup failed %d %s", calls.Load(), w.Body.String())
	}
	deny.Store(true)
	w = call(h, "POST", fmt.Sprintf("/api/channels/%d/models", channel.ID), "", cookie)
	if w.Code != 502 {
		t.Fatal("model sync errors hidden")
	}
	var stored Channel
	a.DB.First(&stored, channel.ID)
	if stored.Models != `["shared","chat-model","claude-model"]` {
		t.Fatal("failed sync replaced saved models")
	}
	for _, protocols := range [][]string{{}, {"openai", "openai"}, {"unknown"}} {
		w = call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(object{"name": channel.Name, "base_url": channel.BaseURL, "protocols": protocols, "enabled": true, "timeout_seconds": 5, "cooldown_seconds": 60}), cookie)
		if w.Code != 400 {
			t.Fatalf("invalid protocols accepted %v", protocols)
		}
	}
	w = call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(object{"name": channel.Name, "base_url": channel.BaseURL, "protocols": []string{"anthropic", "responses"}, "enabled": true, "timeout_seconds": 5, "cooldown_seconds": 60}), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	a.DB.First(&stored, channel.ID)
	if !reflect.DeepEqual(stored.Protocols, []string{"anthropic", "responses"}) || stored.APIKey == "" {
		t.Fatal("protocol update or key preservation failed")
	}
}

func TestLegacySingleProtocolChannel(t *testing.T) {
	a := testApp(t)
	legacy := Channel{Name: "legacy", BaseURL: "https://example.invalid", Protocol: "responses", APIKey: a.seal("legacy-key"), Enabled: true, TimeoutSeconds: 120, CooldownSeconds: 60}
	if err := a.DB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	var stored Channel
	if err := a.DB.First(&stored, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.supportedProtocols(), []string{"responses"}) {
		t.Fatal("legacy channel lost protocol")
	}
	h := a.Router()
	w := call(h, "GET", "/api/channels", "", loginForTest(t, h))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"protocols":["responses"]`) || strings.Contains(w.Body.String(), "legacy-key") {
		t.Fatal(w.Body.String())
	}
}

func TestExistingDatabaseUpgrade(t *testing.T) {
	dir := t.TempDir()
	if _, err := readOrCreate(filepath.Join(dir, "master.key"), 32); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "ledgergate.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE accounts (id integer PRIMARY KEY, name text, kind text, balance real, used real)`,
		`INSERT INTO accounts (id, name, kind, balance, used) VALUES (1, 'existing-account', 'sub2api', 69, 52)`,
		`CREATE TABLE channels (id integer PRIMARY KEY, name text, account_id integer, base_url text, protocol text, api_key text, enabled numeric, timeout_seconds integer, cooldown_seconds integer)`,
		`INSERT INTO channels (id, name, account_id, base_url, protocol, api_key, enabled, timeout_seconds, cooldown_seconds) VALUES (1, 'existing-channel', 1, 'https://example.invalid', 'responses', 'saved-ciphertext', 1, 120, 60)`,
		`CREATE TABLE groups (id integer PRIMARY KEY, name text, strategy text, active_route_id integer)`,
		`INSERT INTO groups (id, name, strategy, active_route_id) VALUES (1, 'existing-model', 'priority', 0)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(Config{DataDir: dir, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var account Account
	if err := upgraded.DB.First(&account, 1).Error; err != nil {
		t.Fatal(err)
	}
	if account.Used == nil || *account.Used != 52 || account.TodayUsed != nil {
		t.Fatal("account values corrupted during upgrade")
	}
	var channel Channel
	if err := upgraded.DB.First(&channel, 1).Error; err != nil {
		t.Fatal(err)
	}
	if channel.AccountID == nil || *channel.AccountID != account.ID || channel.APIKey != "saved-ciphertext" || !reflect.DeepEqual(channel.supportedProtocols(), []string{"responses"}) {
		t.Fatal("legacy channel corrupted during upgrade")
	}
	var group Group
	if err := upgraded.DB.First(&group, 1).Error; err != nil || group.Name != "existing-model" || group.Strategy != "priority" || group.RelayConfig != nil || group.relayConfig() != (GroupRelayConfig{2, 3, 60, 300}) {
		t.Fatal("legacy model or routing defaults corrupted during upgrade", err)
	}
}
