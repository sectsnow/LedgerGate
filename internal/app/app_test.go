package app

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func testApp(t *testing.T) *App {
	t.Helper()
	a, err := Open(Config{DataDir: t.TempDir(), AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func call(handler http.Handler, method, path, body, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LedgerGate-Request", "1")
	if strings.HasPrefix(auth, "ledgergate_session=") {
		req.Header.Set("Cookie", auth)
	} else if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func loginForTest(t *testing.T, handler http.Handler) string {
	t.Helper()
	w := call(handler, "POST", "/api/login", `{"password":"test-password-12345"}`, "")
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie must be HTTP-only and SameSite Strict")
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

func createChannel(t *testing.T, h http.Handler, cookie, name, base, protocol string) Channel {
	t.Helper()
	w := call(h, "POST", "/api/channels", jsonString(object{"name": name, "base_url": base, "protocol": protocol, "api_key": "private-upstream-key", "enabled": true, "timeout_seconds": 5, "cooldown_seconds": 60}), cookie)
	if w.Code != 200 {
		t.Fatalf("create channel %d %s", w.Code, w.Body.String())
	}
	var channel Channel
	if err := json.Unmarshal(w.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	return channel
}

func createGroup(t *testing.T, h http.Handler, cookie string, channels []Channel, strategy string) string {
	t.Helper()
	routes := []Route{}
	for i, channel := range channels {
		weight := 1
		if i == 0 {
			weight = 3
		}
		routes = append(routes, Route{ChannelID: channel.ID, Model: "upstream-model", Weight: weight, Priority: i})
	}
	w := call(h, "POST", "/api/groups", jsonString(Group{Name: "my-model", Strategy: strategy, InputPriceMicros: 2_000_000, OutputPriceMicros: 8_000_000, Routes: routes}), cookie)
	if w.Code != 200 {
		t.Fatalf("create group %d %s", w.Code, w.Body.String())
	}
	w = call(h, "POST", "/api/keys", `{"name":"integration test"}`, cookie)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var value struct{ Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value.Token
}

func chatResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"id":"chat-1","object":"chat.completion","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50}}`)
}

func TestAdminAndGatewayIsolation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	if w := call(h, "GET", "/api/accounts", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie := loginForTest(t, h)
	if w := call(h, "GET", "/v1/models", "", cookie); w.Code != 401 {
		t.Fatal("admin cookie accepted as gateway key")
	}
	w := call(h, "POST", "/api/keys", `{"name":"personal"}`, cookie)
	var value struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &value)
	if w.Code != 201 || !strings.HasPrefix(value.Token, "sk-") {
		t.Fatal(w.Body.String())
	}
	if w := call(h, "GET", "/api/accounts", "", value.Token); w.Code != 401 {
		t.Fatal("gateway key accepted as admin")
	}
	if w := call(h, "GET", "/v1/models", "", value.Token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(h, "GET", "/api/keys", "", cookie); strings.Contains(w.Body.String(), value.Token) {
		t.Fatal("API key leaked on listing")
	}
	req := httptest.NewRequest("POST", "/api/keys", strings.NewReader(`{"name":"csrf"}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("mutation without CSRF header accepted")
	}
	call(h, "DELETE", "/api/keys/1", "", cookie)
	if w := call(h, "GET", "/v1/models", "", value.Token); w.Code != 401 {
		t.Fatal("revoked key still usable")
	}
}

func TestWeightedRoutingThroughAPI(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	counts := [2]atomic.Int32{}
	channels := []Channel{}
	for i := range 2 {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-upstream-key" {
				t.Error("wrong upstream path or auth")
			}
			var body object
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "upstream-model" {
				t.Error("model mapping failed")
			}
			counts[i].Add(1)
			chatResponse(w)
		}))
		t.Cleanup(server.Close)
		channels = append(channels, createChannel(t, h, cookie, fmt.Sprint("channel", i), server.URL+"/v1", "openai"))
	}
	token := createGroup(t, h, cookie, channels, "weighted")
	for range 40 {
		w := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, token)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"my-model"`) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if counts[0].Load() != 30 || counts[1].Load() != 10 {
		t.Fatalf("weighted distribution %d/%d", counts[0].Load(), counts[1].Load())
	}
	var records []RequestLog
	a.DB.Find(&records)
	if len(records) != 40 || records[0].CostMicros != 600 || records[0].Attempts != 1 {
		t.Fatalf("bad accounting %+v", records[0])
	}
	w := call(h, "GET", "/api/overview", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"requests":40`) {
		t.Fatal(w.Body.String())
	}
	var row Channel
	a.DB.First(&row, channels[0].ID)
	if row.APIKey == "private-upstream-key" || row.APIKey == "" {
		t.Fatal("upstream key not encrypted")
	}
	if w := call(h, "GET", "/api/channels", "", cookie); strings.Contains(w.Body.String(), "private-upstream-key") || strings.Contains(w.Body.String(), row.APIKey) {
		t.Fatal("channel listing leaked secret")
	}
}

func TestFailoverAndNoReplayAfterStreamCommit(t *testing.T) {
	for _, mode := range []string{"http_failure", "early_stream_error", "late_stream_error"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var backupCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "http_failure" {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "early_stream_error" {
					fmt.Fprint(w, "data: {\"error\":{\"message\":\"overloaded\"}}\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"id\":\"s1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				if mode == "http_failure" {
					chatResponse(w)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"s2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"complete\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer backup.Close()
			token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "primary", primary.URL, "openai"), createChannel(t, h, cookie, "backup", backup.URL, "openai")}, "priority")
			stream := mode != "http_failure"
			w := call(h, "POST", "/v1/chat/completions", jsonString(object{"model": "my-model", "stream": stream, "messages": []any{object{"role": "user", "content": "hello"}}}), token)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			var record RequestLog
			a.DB.Last(&record)
			if mode == "late_stream_error" {
				if backupCalls.Load() != 0 || record.Status != 502 || record.Attempts != 1 || !strings.Contains(w.Body.String(), "event: error") {
					t.Fatalf("stream replay or wrong log %+v %s", record, w.Body.String())
				}
			} else if backupCalls.Load() != 1 || record.Status != 200 || record.Attempts != 2 {
				t.Fatalf("failover failed %+v", record)
			}
		})
	}
}

func TestMonitorAlertCooldownAndFreshBalanceGate(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var unhealthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer account-token" {
			t.Error("wrong monitor token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			if unhealthy.Load() {
				w.WriteHeader(401)
			} else {
				fmt.Fprint(w, `{"success":true,"data":{"quota":0,"used_quota":1000000}}`)
			}
		case "/api/user/self/groups":
			fmt.Fprint(w, `{"success":true,"data":{"default":{"ratio":1}}}`)
		case "/api/notice":
			fmt.Fprint(w, `{"success":true,"data":"notice"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	w := call(h, "POST", "/api/accounts", jsonString(object{"name": "account", "kind": "newapi", "base_url": server.URL, "enabled": true, "auth_mode": "token", "token": "account-token", "currency": "USD", "quota_per_unit": 500000, "low_balance": 5, "block_empty": true, "interval_seconds": 300}), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for range 2 {
		if _, err := a.checkAccount(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	a.DB.Model(&Alert{}).Where("kind = ?", "low_balance").Count(&count)
	if count != 1 {
		t.Fatalf("cooldown did not deduplicate alerts: %d", count)
	}
	var account Account
	a.DB.First(&account, 1)
	if account.Used == nil || *account.Used != 2 || account.Balance == nil || *account.Balance != 0 {
		t.Fatal("quota conversion failed")
	}
	channel := createChannel(t, h, cookie, "linked", server.URL, "openai")
	a.DB.Model(&channel).Update("account_id", account.ID)
	group := Group{Routes: []Route{{ChannelID: channel.ID}}}
	if len(a.candidates(group)) != 0 {
		t.Fatal("confirmed empty balance not gated")
	}
	unhealthy.Store(true)
	if _, err := a.checkAccount(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(a.candidates(group)) != 1 {
		t.Fatal("monitor failure disabled working API")
	}
	var snapshots []Snapshot
	a.DB.Find(&snapshots)
	if len(snapshots) != 3 || snapshots[0].Currency != "USD" {
		t.Fatal("missing historical snapshots")
	}
}

func TestProtocolConversionMatrix(t *testing.T) {
	chat := object{"model": "m", "max_tokens": 300, "messages": []any{object{"role": "system", "content": "be helpful"}, object{"role": "user", "content": "weather?"}, object{"role": "assistant", "content": nil, "tool_calls": []any{object{"id": "call_1", "type": "function", "function": object{"name": "weather", "arguments": `{"city":"北京"}`}}}}, object{"role": "tool", "tool_call_id": "call_1", "content": "sunny"}}, "tools": []any{object{"type": "function", "function": object{"name": "weather", "parameters": object{"type": "object", "properties": object{}}}}}, "tool_choice": "auto"}
	bodies := map[string]object{"openai": chat}
	for _, target := range []string{"anthropic", "responses"} {
		value, err := convertRequest(chat, "openai", target)
		if err != nil {
			t.Fatal(err)
		}
		raw := []byte(jsonString(value))
		decoded, _ := decodeJSON(raw)
		bodies[target] = obj(decoded)
	}
	for _, from := range []string{"openai", "anthropic", "responses"} {
		for _, to := range []string{"openai", "anthropic", "responses"} {
			t.Run(from+"_to_"+to, func(t *testing.T) {
				value, err := convertRequest(bodies[from], from, to)
				if err != nil {
					t.Fatal(err)
				}
				text := jsonString(value)
				for _, want := range []string{"weather", "call_1", "sunny", "be helpful"} {
					if !strings.Contains(text, want) {
						t.Fatalf("lost %s: %s", want, text)
					}
				}
				result := completion{ID: "r1", Text: "你好", Tools: []toolCall{{"call_1", "weather", `{"city":"北京"}`}}, Input: 12, Output: 7, Finish: "tool_calls"}
				response, err := completionBody(result, from, "m")
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := parseCompletion(response, from)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Text != result.Text || len(parsed.Tools) != 1 || parsed.Tools[0] != result.Tools[0] || parsed.Input != 12 || parsed.Output != 7 {
					t.Fatalf("response round trip %+v", parsed)
				}
			})
		}
	}
	chat["reasoning_effort"] = "high"
	if _, err := convertRequest(chat, "openai", "anthropic"); err == nil {
		t.Fatal("unsupported semantic field silently dropped")
	}
}

func streamFixture(protocol string) string {
	events := []object{}
	switch protocol {
	case "openai":
		events = []object{{"id": "s1", "choices": []any{object{"index": 0, "delta": object{"content": "你好"}}}}, {"choices": []any{object{"index": 0, "delta": object{"tool_calls": []any{object{"index": 0, "id": "c1", "function": object{"name": "weather", "arguments": "{\"city\":"}}}}}}}, {"choices": []any{object{"index": 0, "delta": object{"tool_calls": []any{object{"index": 0, "function": object{"arguments": "\"北京\"}"}}}}, "finish_reason": "tool_calls"}}}, {"choices": []any{}, "usage": object{"prompt_tokens": 12, "completion_tokens": 7}}}
	case "anthropic":
		events = []object{{"type": "message_start", "message": object{"id": "s1", "usage": object{"input_tokens": 12, "output_tokens": 0}}}, {"type": "content_block_delta", "index": 0, "delta": object{"type": "text_delta", "text": "你好"}}, {"type": "content_block_start", "index": 1, "content_block": object{"type": "tool_use", "id": "c1", "name": "weather", "input": object{}}}, {"type": "content_block_delta", "index": 1, "delta": object{"type": "input_json_delta", "partial_json": `{"city":"北京"}`}}, {"type": "message_delta", "delta": object{"stop_reason": "tool_use"}, "usage": object{"output_tokens": 7}}, {"type": "message_stop"}}
	case "responses":
		events = []object{{"type": "response.created", "response": object{"id": "s1"}}, {"type": "response.output_text.delta", "output_index": 0, "delta": "你好"}, {"type": "response.output_item.added", "output_index": 1, "item": object{"type": "function_call", "call_id": "c1", "name": "weather", "arguments": ""}}, {"type": "response.function_call_arguments.delta", "output_index": 1, "delta": `{"city":"北京"}`}, {"type": "response.completed", "response": object{"status": "completed", "usage": object{"input_tokens": 12, "output_tokens": 7}}}}
	}
	var result strings.Builder
	for _, event := range events {
		kind := ""
		if protocol != "openai" {
			kind = str(event["type"])
		}
		writeFrame(&result, kind, jsonString(event))
	}
	if protocol == "openai" {
		writeFrame(&result, "", "[DONE]")
	}
	return result.String()
}

func TestStreamingMatrixThroughGateway(t *testing.T) {
	for _, from := range []string{"openai", "anthropic", "responses"} {
		for _, to := range []string{"openai", "anthropic", "responses"} {
			t.Run(from+"_to_"+to, func(t *testing.T) {
				a := testApp(t)
				h := a.Router()
				cookie := loginForTest(t, h)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != requestPath(from) {
						t.Error("wrong protocol endpoint")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, streamFixture(from))
				}))
				defer upstream.Close()
				token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "channel", upstream.URL, from)}, "priority")
				body := object{"model": "my-model", "stream": true, "messages": []any{object{"role": "user", "content": "hello"}}}
				if to == "openai" {
					body["stream_options"] = object{"include_usage": true}
				}
				if to == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
				}
				if to == "anthropic" {
					body["max_tokens"] = 100
				}
				w := call(h, "POST", requestPath(to), jsonString(body), token)
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				scanner := bufio.NewScanner(strings.NewReader(w.Body.String()))
				result := completion{}
				var text strings.Builder
				var arguments strings.Builder
				done := false
				for !done {
					event, err := readEvent(scanner)
					if err != nil {
						t.Fatalf("missing terminal event: %v %s", err, w.Body.String())
					}
					deltas, finished, err := inspectEvent(event, to, &result, true)
					if err != nil {
						t.Fatal(err, w.Body.String())
					}
					for _, delta := range deltas {
						text.WriteString(delta.Text)
						if delta.Tool != nil {
							arguments.WriteString(delta.Tool.Arguments)
						}
					}
					done = finished
				}
				if text.String() != "你好" || arguments.String() != `{"city":"北京"}` || result.Input != 12 || result.Output != 7 {
					t.Fatalf("stream semantics lost %s %s %+v", text.String(), arguments.String(), result)
				}
				var record RequestLog
				a.DB.Last(&record)
				if record.Status != 200 || record.InputTokens != 12 || record.OutputTokens != 7 {
					t.Fatalf("bad stream accounting %+v", record)
				}
			})
		}
	}
}

func TestPersistenceAndSecretPermissions(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := first.seal("a-secret")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	plain, err := second.unseal(ciphertext)
	if err != nil || plain != "a-secret" {
		t.Fatal("credentials not readable after restart")
	}
	for _, name := range []string{"master.key", "admin-password", "ledgergate.db"} {
		stat, err := os.Stat(filepath.Join(dir, name))
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatalf("insecure permissions on %s", name)
		}
	}
}

func TestProbeAndCostBounds(t *testing.T) {
	a := testApp(t)
	candidates := []candidate{{Route: Route{ID: 1, Weight: 1}, Channel: Channel{ID: 1}}}
	a.health[1] = channelState{Until: time.Now().Add(-time.Second)}
	if _, ok := a.pick(candidates, "priority", map[uint]bool{}); !ok {
		t.Fatal("recovery probe not allowed")
	}
	if _, ok := a.pick(candidates, "priority", map[uint]bool{}); ok {
		t.Fatal("more than one concurrent recovery probe")
	}
	a.releaseProbe(1)
	if _, ok := a.pick(candidates, "priority", map[uint]bool{}); !ok {
		t.Fatal("probe not released")
	}
	if estimateCost(1, 0, 500000, 0) != 1 || estimateCost(1_000_000_000, 0, 1_000_000_000_000, 0) != 1_000_000_000_000_000 {
		t.Fatal("cost rounding or multiplication overflow")
	}
}

func TestWebhookSignatureAndPersistentRetry(t *testing.T) {
	a := testApp(t)
	var attempts atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("webhook-secret"))
		mac.Write(raw)
		if r.Header.Get("X-LedgerGate-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("invalid webhook signature")
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	channel := NotificationChannel{Name: "Webhook", Type: "webhook", Enabled: true, URL: a.seal(webhook.URL), Secret: a.seal("webhook-secret"), Retries: 10}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	alert := Alert{AccountID: 1, Kind: "low_balance", Title: "余额提醒", Delivery: "pending", NextRetryAt: time.Now()}
	if err := a.DB.Create(&alert).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&NotificationDelivery{AlertID: &alert.ID, ChannelID: channel.ID, ChannelName: channel.Name, ChannelType: channel.Type, Title: alert.Title, Status: "pending", NextRetryAt: alert.NextRetryAt}).Error; err != nil {
		t.Fatal(err)
	}
	a.deliverAlerts(context.Background())
	a.DB.First(&alert, alert.ID)
	if alert.Delivery != "pending" || alert.Attempts != 1 || alert.NextRetryAt.Before(time.Now()) {
		t.Fatalf("failed delivery not persisted %+v", alert)
	}
	a.DB.Model(&alert).Update("next_retry_at", time.Now().Add(-time.Second))
	a.DB.Model(&NotificationDelivery{}).Where("alert_id = ?", alert.ID).Update("next_retry_at", time.Now().Add(-time.Second))
	a.deliverAlerts(context.Background())
	a.DB.First(&alert, alert.ID)
	if alert.Delivery != "sent" || alert.Attempts != 2 || alert.DeliveryError != "" {
		t.Fatalf("retry failed %+v", alert)
	}
}

func TestPasswordAccountAuthentication(t *testing.T) {
	for _, kind := range []string{"newapi", "sub2api"} {
		t.Run(kind, func(t *testing.T) {
			a := testApp(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/user/login":
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "login-cookie"})
					fmt.Fprint(w, `{"success":true,"data":{"id":12}}`)
				case "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"login-token"}}`)
				case "/api/user/self":
					if r.Header.Get("Cookie") != "session=login-cookie" || r.Header.Get("New-Api-User") != "12" {
						t.Error("login cookie or user id not forwarded")
					}
					fmt.Fprint(w, `{"success":true,"data":{"quota":5000000,"used_quota":1000000}}`)
				case "/api/v1/auth/me":
					if r.Header.Get("Authorization") != "Bearer login-token" {
						t.Error("login access token not forwarded")
					}
					fmt.Fprint(w, `{"code":0,"data":{"balance":10}}`)
				default:
					fmt.Fprint(w, `{"data":[]}`)
				}
			}))
			defer server.Close()
			account := Account{Name: "test", Kind: kind, BaseURL: server.URL, AuthMode: "password", Username: "test@example.com", Password: a.seal("login-password"), QuotaPerUnit: 500000, IntervalSeconds: 300}
			if _, err := a.fetchAccount(context.Background(), &account); err != nil {
				t.Fatal(err)
			}
			if account.Balance == nil || *account.Balance != 10 {
				t.Fatal("incorrect balance")
			}
		})
	}
}

func TestConcurrentWeightedSelection(t *testing.T) {
	a := testApp(t)
	candidates := []candidate{{Route: Route{ID: 1, Weight: 3}, Channel: Channel{ID: 1}}, {Route: Route{ID: 2, Weight: 1}, Channel: Channel{ID: 2}}}
	counts := [2]atomic.Int32{}
	var workers sync.WaitGroup
	for range 100 {
		workers.Go(func() {
			item, ok := a.pick(candidates, "weighted", map[uint]bool{})
			if !ok {
				t.Error("missing route")
				return
			}
			counts[item.Channel.ID-1].Add(1)
		})
	}
	workers.Wait()
	if counts[0].Load() != 75 || counts[1].Load() != 25 {
		t.Fatalf("concurrent distribution %d/%d", counts[0].Load(), counts[1].Load())
	}
}

func TestStreamUsageOptIn(t *testing.T) {
	for _, upstreamProtocol := range []string{"openai", "anthropic", "responses"} {
		for _, include := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_%v", upstreamProtocol, include), func(t *testing.T) {
				writer := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				response := &http.Response{Body: io.NopCloser(strings.NewReader(streamFixture(upstreamProtocol)))}
				application := &App{}
				result, err := application.pipeStream(c, response, upstreamProtocol, "openai", "my-model", include)
				if err != nil {
					t.Fatal(err)
				}
				if result.Input != 12 || result.Output != 7 {
					t.Fatal("server usage missing")
				}
				if strings.Contains(writer.Body.String(), `"usage"`) != include {
					t.Fatalf("client usage opt-in ignored: %s", writer.Body.String())
				}
			})
		}
	}
}
