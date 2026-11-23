package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountMonitoringPausesAndRestoresForwarding(t *testing.T) {
	for _, strategy := range []string{"manual", "priority", "weighted"} {
		t.Run(strategy, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			account := Account{Name: "monitored", Enabled: true}
			if err := a.DB.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				protocol := "openai"
				if r.URL.Path == "/v1/responses" {
					protocol = "responses"
				} else if r.URL.Path == "/v1/messages" {
					protocol = "anthropic"
				}
				body, err := completionBody(completion{ID: "account-pause", Text: "OK", Finish: "stop", Input: 1, Output: 1}, protocol, "upstream-model")
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, jsonString(body))
			}))
			defer server.Close()
			channels := []Channel{}
			for _, reason := range []string{"", "manual", "上游分组已下架"} {
				channel := createMultiProtocolChannel(t, h, cookie, server.URL, []string{"openai", "responses", "anthropic"})
				updates := map[string]any{"account_id": account.ID}
				if reason != "" {
					updates["enabled"] = false
					if reason != "manual" {
						updates["auto_disabled_reason"] = reason
					}
				}
				if err := a.DB.Model(&channel).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				channels = append(channels, channel)
			}
			key := createGroup(t, h, cookie, channels, strategy)
			var original Group
			if err := a.DB.Preload("Routes").First(&original).Error; err != nil {
				t.Fatal(err)
			}
			for _, enabled := range []bool{true, false, true} {
				response := call(h, "PATCH", fmt.Sprintf("/api/accounts/%d/monitoring", account.ID), jsonString(object{"enabled": enabled}), cookie)
				if response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				listing := call(h, "GET", "/v1/models", "", key)
				if listing.Code != 200 || strings.Contains(listing.Body.String(), `"my-model"`) != enabled {
					t.Fatalf("model list ignored monitoring=%v: %s", enabled, listing.Body.String())
				}
				listing = call(h, "GET", "/api/channels", "", cookie)
				var rows []struct {
					Channel Channel `json:"channel"`
					Paused  bool    `json:"monitoring_paused"`
				}
				if listing.Code != 200 || json.Unmarshal(listing.Body.Bytes(), &rows) != nil || len(rows) != 3 {
					t.Fatal("could not read channel availability")
				}
				for i, row := range rows {
					if row.Paused == enabled || row.Channel.Enabled != channels[i].Enabled || row.Channel.AutoDisabledReason != channels[i].AutoDisabledReason {
						t.Fatal("pause lost a channel's manual or automatic state")
					}
				}
				before := calls.Load()
				for _, protocol := range []string{"openai", "responses", "anthropic"} {
					body := object{"model": "my-model", "messages": []any{object{"role": "user", "content": "hello"}}, "max_tokens": 10}
					if protocol == "responses" {
						body = object{"model": "my-model", "input": "hello"}
					}
					response := call(h, "POST", requestPath(protocol), jsonString(body), key)
					want := 503
					if enabled {
						want = 200
					}
					if response.Code != want {
						t.Fatalf("monitoring=%v %s: %d %s", enabled, protocol, response.Code, response.Body.String())
					}
				}
				want := int32(0)
				if enabled {
					want = 3
				}
				if calls.Load()-before != want {
					t.Fatal("paused account reached upstream or resumed routes did not recover")
				}
				var saved Group
				if err := a.DB.Preload("Routes").First(&saved, original.ID).Error; err != nil || saved.ActiveRouteID != original.ActiveRouteID || jsonString(saved.Routes) != jsonString(original.Routes) {
					t.Fatal("monitoring switch changed routes or manual selection")
				}
			}
		})
	}
}

func TestPausedAccountPreservesSharedModelAndDirectChannels(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	paused, active := Account{Name: "paused", Enabled: false}, Account{Name: "active", Enabled: true}
	if a.DB.Create(&paused).Error != nil || a.DB.Create(&active).Error != nil {
		t.Fatal("could not seed accounts")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatResponse(w)
	}))
	defer server.Close()
	channels := []Channel{}
	for _, accountID := range []*uint{&paused.ID, &active.ID, nil} {
		channel := createChannel(t, h, cookie, "shared", server.URL, "openai")
		if err := a.DB.Model(&channel).Update("account_id", accountID).Error; err != nil {
			t.Fatal(err)
		}
		channels = append(channels, channel)
	}
	key := createGroup(t, h, cookie, channels, "priority")
	var group Group
	a.DB.Preload("Routes").First(&group)
	if len(a.candidates(group)) != 2 {
		t.Fatal("paused account should leave other-account and direct routes available")
	}
	if response := call(h, "GET", "/v1/models", "", key); response.Code != 200 || !strings.Contains(response.Body.String(), `"my-model"`) {
		t.Fatal("shared model was hidden")
	}
	if response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`, key); response.Code != 200 || calls.Load() != 1 {
		t.Fatal("shared model no longer forwards through its available account")
	}
	if response := call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": []uint{channels[0].ID}, "use_saved_models": true}), cookie); response.Code != 400 {
		t.Fatal("automatic generation accepted a paused account channel")
	}
}

func TestFailoverSkipsAccountPausedDuringRequest(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	account := Account{Name: "backup account", Enabled: true}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock, enteredOnce sync.Once
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		w.WriteHeader(503)
	}))
	defer first.Close()
	defer func() { unblock.Do(func() { close(release) }) }()
	var backupCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		chatResponse(w)
	}))
	defer backup.Close()
	one := createChannel(t, h, cookie, "first", first.URL, "openai")
	two := createChannel(t, h, cookie, "backup", backup.URL, "openai")
	if err := a.DB.Model(&two).Update("account_id", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	key := createGroup(t, h, cookie, []Channel{one, two}, "priority")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`, key)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not start")
	}
	if response := call(h, "PATCH", fmt.Sprintf("/api/accounts/%d/monitoring", account.ID), `{"enabled":false}`, cookie); response.Code != 200 {
		t.Fatal("pause failed")
	}
	unblock.Do(func() { close(release) })
	select {
	case response := <-done:
		if response.Code < 400 {
			t.Fatal("request unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not finish")
	}
	if backupCalls.Load() != 0 {
		t.Fatal("gateway called an account paused after candidate selection")
	}
}
