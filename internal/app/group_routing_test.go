package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ledgergate/internal/backup"
)

func routingGroupForTest(t *testing.T, a *App, config *GroupRelayConfig) Group {
	t.Helper()
	var group Group
	if err := a.DB.Preload("Routes").Where("name=?", "my-model").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	group.RelayConfig = config
	if err := a.DB.Omit("Routes").Save(&group).Error; err != nil {
		t.Fatal(err)
	}
	return group
}

func TestGroupRoutingAffinityAndSingleProbe(t *testing.T) {
	a := &App{health: map[uint]channelState{}, routeHealth: map[uint]channelState{}}
	group := Group{ID: 1, Strategy: "priority", Routes: []Route{{ID: 1, ChannelID: 1}, {ID: 2, ChannelID: 2, Priority: 1}}}
	items := []candidate{{group.Routes[0], Channel{ID: 1}}, {group.Routes[1], Channel{ID: 2}}}
	item, ok, probe := a.pickGroup(group, items, nil, nil)
	if !ok || probe || item.Route.ID != 1 {
		t.Fatal("primary was not selected")
	}
	a.recordMemberFailure(group, item, false, nil, 503, "temporary")
	item, ok, probe = a.pickGroup(group, items, nil, nil)
	if !ok || probe || item.Route.ID != 2 {
		t.Fatal("backup was not selected")
	}
	a.recordMemberSuccess(group, item, false)
	state := a.groupRouting[group.ID]
	if time.Until(state.AffinityUntil) < 299*time.Second {
		t.Fatal("default affinity not applied")
	}
	cooled := a.routeHealth[1]
	cooled.Until = time.Now().Add(-time.Second)
	a.routeHealth[1] = cooled
	for range 3 {
		picked, ok, probe := a.pickGroup(group, items, nil, nil)
		if !ok || probe || picked.Route.ID != 2 {
			t.Fatal("affinity prematurely switched to recovered primary")
		}
		a.recordMemberSuccess(group, picked, false)
	}
	state.AffinityUntil = time.Now().Add(-time.Second)
	var probes, backups atomic.Int32
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			picked, ok, probe := a.pickGroup(group, items, nil, nil)
			if !ok {
				t.Error("healthy backup unavailable")
				return
			}
			if picked.Route.ID == 1 && probe {
				probes.Add(1)
			} else if picked.Route.ID == 2 && !probe {
				backups.Add(1)
			} else {
				t.Error("invalid probe target")
			}
		})
	}
	workers.Wait()
	if probes.Load() != 1 || backups.Load() != 19 {
		t.Fatalf("recovery probe was not exclusive: probes=%d backups=%d", probes.Load(), backups.Load())
	}
	a.recordMemberSuccess(group, items[0], true)
	if picked, ok, probe := a.pickGroup(group, items, nil, nil); !ok || probe || picked.Route.ID != 1 {
		t.Fatal("successful recovery did not restore primary")
	}
}

func TestGroupRoutingAffinityInvalidationAndProbeRelease(t *testing.T) {
	for _, change := range []string{"disabled", "deleted", "reordered", "strategy", "config", "probe_cancel"} {
		t.Run(change, func(t *testing.T) {
			a := &App{health: map[uint]channelState{}, routeHealth: map[uint]channelState{}}
			group := Group{ID: 1, Strategy: "priority", Routes: []Route{{ID: 1, ChannelID: 1}, {ID: 2, ChannelID: 2, Priority: 1}}}
			items := []candidate{{group.Routes[0], Channel{ID: 1}}, {group.Routes[1], Channel{ID: 2}}}
			primary, _, _ := a.pickGroup(group, items, nil, nil)
			a.recordMemberFailure(group, primary, false, nil, 503, "temporary")
			backupItem, _, _ := a.pickGroup(group, items, nil, nil)
			a.recordMemberSuccess(group, backupItem, false)
			cooled := a.routeHealth[1]
			cooled.Until = time.Now().Add(-time.Second)
			a.routeHealth[1] = cooled
			switch change {
			case "disabled":
				items = items[:1]
			case "deleted":
				group.Routes = group.Routes[:1]
				items = items[:1]
			case "reordered":
				group.Routes[0].Priority = 2
				items[0].Route = group.Routes[0]
			case "strategy":
				group.Strategy = "manual"
				group.ActiveRouteID = 1
			case "config":
				config := group.relayConfig()
				config.MemberAffinitySeconds = 0
				group.RelayConfig = &config
			case "probe_cancel":
				a.groupRouting[1].AffinityUntil = time.Now().Add(-time.Second)
			}
			picked, ok, probe := a.pickGroup(group, items, nil, nil)
			if !ok {
				t.Fatal("available member blocked by stale affinity")
			}
			if (change == "disabled" || change == "deleted" || change == "config" || change == "probe_cancel") && picked.Route.ID != 1 {
				t.Fatal("unavailable backup retained affinity")
			}
			if change == "probe_cancel" {
				if !probe {
					t.Fatal("expected recovery probe")
				}
				a.releaseGroupProbe(group, picked)
				a.releaseRouteProbe(picked.Route.ID)
				a.releaseProbe(picked.Channel.ID)
				if next, ok, probe := a.pickGroup(group, items, nil, nil); !ok || !probe || next.Route.ID != 1 {
					t.Fatal("cancelled probe was not released")
				}
			}
		})
	}
}

func TestGroupRoutingRetriesBeforeSwitchAndKeepsAffinity(t *testing.T) {
	for _, mode := range []string{"retry_success", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var primaryCalls, backupCalls atomic.Int32
			var first, second atomic.Int64
			var recovered atomic.Bool
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := primaryCalls.Add(1)
				if n == 1 {
					first.Store(time.Now().UnixNano())
				}
				if n == 2 {
					second.Store(time.Now().UnixNano())
				}
				if recovered.Load() || mode == "retry_success" && n >= 3 {
					chatResponse(w)
					return
				}
				w.WriteHeader(503)
			}))
			defer primary.Close()
			backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backupCalls.Add(1); chatResponse(w) }))
			defer backupServer.Close()
			primaryChannel := createChannel(t, h, cookie, "primary", primary.URL, "openai")
			backupChannel := createChannel(t, h, cookie, "backup", backupServer.URL, "openai")
			token := createGroup(t, h, cookie, []Channel{primaryChannel, backupChannel}, "priority")
			group := routingGroupForTest(t, a, &GroupRelayConfig{3, 1, 60, 300})
			request := `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`
			response := call(h, "POST", "/v1/chat/completions", request, token)
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			_, detail := historyDetailForTest(t, h, cookie, log.ID)
			wantAttempts, wantBackup := 3, int32(0)
			if mode == "exhausted" {
				wantAttempts, wantBackup = 4, 1
			}
			if response.Code != 200 || log.Attempts != wantAttempts || primaryCalls.Load() != 3 || backupCalls.Load() != wantBackup || len(detail.Attempts) != wantAttempts {
				t.Fatalf("incorrect member retry budget: %+v attempts=%+v", log, detail.Attempts)
			}
			if time.Duration(second.Load()-first.Load()) < time.Second {
				t.Fatal("retry interval was not respected")
			}
			for i := 0; i < 3; i++ {
				if detail.Attempts[i].ChannelID != primaryChannel.ID {
					t.Fatal("switched before exhausting member attempts")
				}
			}
			if mode == "exhausted" {
				recovered.Store(true)
				a.mu.Lock()
				health := a.routeHealth[group.Routes[0].ID]
				health.Until = time.Now().Add(-time.Second)
				a.routeHealth[group.Routes[0].ID] = health
				a.mu.Unlock()
				if response := call(h, "POST", "/v1/chat/completions", request, token); response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				if primaryCalls.Load() != 3 || backupCalls.Load() != 2 {
					t.Fatal("affinity did not keep the backup")
				}
				a.mu.Lock()
				a.groupRouting[group.ID].AffinityUntil = time.Now().Add(-time.Second)
				a.mu.Unlock()
				if response := call(h, "POST", "/v1/chat/completions", request, token); response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				if primaryCalls.Load() != 4 || backupCalls.Load() != 2 {
					t.Fatal("recovery probe did not return to primary")
				}
			}
		})
	}
}

func TestGroupRoutingRetryStopsOnCancellationBudgetAndDisable(t *testing.T) {
	for _, mode := range []string{"cancel", "budget", "time", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var primaryCalls, backupCalls atomic.Int32
			failed := make(chan struct{})
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if primaryCalls.Add(1) == 1 {
					close(failed)
				}
				w.WriteHeader(503)
			}))
			defer primary.Close()
			backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backupCalls.Add(1); chatResponse(w) }))
			defer backupServer.Close()
			primaryChannel := createChannel(t, h, cookie, "primary", primary.URL, "openai")
			token := createGroup(t, h, cookie, []Channel{primaryChannel, createChannel(t, h, cookie, "backup", backupServer.URL, "openai")}, "priority")
			routingGroupForTest(t, a, &GroupRelayConfig{3, 1, 60, 300})
			if mode == "budget" {
				if err := a.DB.Model(&Setting{}).Where("id=1").Update("max_attempts", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "time" {
				if err := a.DB.Model(&Setting{}).Where("id=1").Update("request_timeout_seconds", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"my-model","messages":[]}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, req); done <- w }()
			select {
			case <-failed:
			case <-time.After(5 * time.Second):
				t.Fatal("primary never called")
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "disabled" {
				if err := a.DB.Model(&primaryChannel).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("retry wait ignored cancellation or budget")
			}
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			wantStatus, wantBackup := 503, int32(0)
			if mode == "cancel" {
				wantStatus = 499
			}
			if mode == "time" {
				wantStatus = 504
			}
			if mode == "disabled" {
				wantStatus, wantBackup = 200, 1
			}
			if log.Status != wantStatus || primaryCalls.Load() != 1 || backupCalls.Load() != wantBackup {
				t.Fatalf("unexpected continuation after %s: %+v primary=%d backup=%d", mode, log, primaryCalls.Load(), backupCalls.Load())
			}
			if mode != "disabled" && (len(a.health) != 0 || len(a.routeHealth) != 0) {
				t.Fatal("client cancellation or total budget cooled an upstream")
			}
		})
	}
}

func TestGroupRoutingHTTPFailureScopeAndSkipPolicy(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var badCalls, goodCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body object
				_ = json.NewDecoder(r.Body).Decode(&body)
				if str(body["model"]) == "good-model" {
					goodCalls.Add(1)
					chatResponse(w)
					return
				}
				badCalls.Add(1)
				if status == 429 {
					w.Header().Set("Retry-After", "120")
				}
				w.WriteHeader(status)
			}))
			defer upstream.Close()
			channel := createChannel(t, h, cookie, "shared", upstream.URL, "openai")
			token := createGroup(t, h, cookie, []Channel{channel}, "priority")
			group := routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 60, 300})
			goodRoute := Route{GroupID: group.ID, ChannelID: channel.ID, Model: "good-model", Weight: 1, Priority: 1}
			if err := a.DB.Create(&goodRoute).Error; err != nil {
				t.Fatal(err)
			}
			response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[]}`, token)
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			wantBad, wantGood, wantStatus := int32(1), int32(1), 200
			if status == 503 {
				wantBad = 2
			}
			if status == 401 || status == 403 {
				wantGood, wantStatus = 0, 502
			}
			if status == 400 || status == 413 {
				wantGood, wantStatus = 0, status
			}
			if badCalls.Load() != wantBad || goodCalls.Load() != wantGood || response.Code != wantStatus {
				t.Fatalf("status %d: bad=%d good=%d log=%+v", status, badCalls.Load(), goodCalls.Load(), log)
			}
			if status == 401 || status == 403 {
				if _, ok := a.health[channel.ID]; !ok {
					t.Fatal("authentication failure did not cool credential")
				}
			} else if len(a.health) != 0 {
				t.Fatal("model-scoped failure cooled every model on channel")
			}
			if status == 429 && time.Until(a.routeHealth[group.Routes[0].ID].Until) < 119*time.Second {
				t.Fatal("Retry-After was ignored")
			}
		})
	}
}

func TestGroupRelayConfigValidationPersistenceAndTransfer(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "config", "https://upstream.invalid", "openai")
	createGroup(t, h, cookie, []Channel{channel}, "priority")
	var group Group
	if err := a.DB.Preload("Routes").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if got := group.relayConfig(); got != (GroupRelayConfig{2, 3, 60, 300}) {
		t.Fatal("old configuration did not receive defaults", got)
	}
	config := GroupRelayConfig{3, 2, 90, 0}
	group.RelayConfig = &config
	path := fmt.Sprintf("/api/groups/%d", group.ID)
	if response := call(h, "PUT", path, jsonString(group), cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var saved Group
	if err := a.DB.Preload("Routes").First(&saved, group.ID).Error; err != nil || saved.RelayConfig == nil || *saved.RelayConfig != config {
		t.Fatal("routing config not persisted", err)
	}
	saved.RelayConfig = nil
	if response := call(h, "PUT", path, jsonString(saved), cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := a.DB.First(&saved, group.ID).Error; err != nil || saved.relayConfig() != config {
		t.Fatal("legacy edit reset routing settings", err)
	}
	for _, bad := range []GroupRelayConfig{{0, 1, 1, 0}, {33, 1, 1, 0}, {1, 0, 1, 0}, {1, 3601, 1, 0}, {1, 1, 0, 0}, {1, 1, 3601, 0}, {1, 1, 1, -1}, {1, 1, 1, 86401}} {
		saved.RelayConfig = &bad
		if response := call(h, "PUT", path, jsonString(saved), cookie); response.Code != 400 {
			t.Fatal("invalid routing config accepted", bad, response.Body.String())
		}
	}
	raw, doc := exportTransferForTest(t, a, []string{"models"}, true)
	if len(doc.Models) != 1 || doc.Models[0].relayConfig() != config {
		t.Fatal("export lost routing settings")
	}
	target := testApp(t)
	targetHandler := target.Router()
	targetCookie := loginForTest(t, targetHandler)
	preview := transferPreviewForTest(t, targetHandler, targetCookie, raw, nil)
	if response := transferUploadForTest(t, targetHandler, targetCookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var imported Group
	if err := target.DB.First(&imported).Error; err != nil || imported.relayConfig() != config {
		t.Fatal("import lost routing settings", err)
	}
	doc.Models[0].RelayConfig = nil
	legacy, err := backup.EncryptData([]byte(jsonString(doc)), transferTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	legacyTarget := testApp(t)
	legacyHandler := legacyTarget.Router()
	legacyCookie := loginForTest(t, legacyHandler)
	preview = transferPreviewForTest(t, legacyHandler, legacyCookie, legacy, nil)
	if response := transferUploadForTest(t, legacyHandler, legacyCookie, "/api/data-transfer/import", legacy, transferTestPassword, preview.Selected, preview.Token); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var legacyGroup Group
	if err := legacyTarget.DB.First(&legacyGroup).Error; err != nil || legacyGroup.relayConfig() != (GroupRelayConfig{2, 3, 60, 300}) {
		t.Fatal("legacy import defaults incorrect", err)
	}
	doc.Models[0].RelayConfig = &GroupRelayConfig{0, 1, 1, 0}
	if err := doc.validate(); err == nil {
		t.Fatal("import validator accepted invalid routing config")
	}
}

func TestGroupRoutingStreamExtensionsCommitWithoutInventingFirstToken(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var primaryCalls, backupCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				extension := map[string]string{
					"openai":    `{"choices":[{"index":0,"delta":{"audio":{"data":"extension-payload"}}}]}`,
					"responses": `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"extension-payload"}}`,
					"anthropic": `{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"extension-payload"}}`,
				}
				_ = writeFrame(w, "", extension[protocol])
				_ = writeFrame(w, "error", `{"type":"error","error":{"message":"upstream stopped"}}`)
			}))
			defer primary.Close()
			backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				fmt.Fprint(w, streamFixture(protocol))
			}))
			defer backupServer.Close()
			channel := createChannel(t, h, cookie, "primary", primary.URL, protocol)
			token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backupServer.URL, protocol)}, "priority")
			body := object{"model": "my-model", "stream": true, "messages": []any{object{"role": "user", "content": "hello"}}}
			if protocol == "responses" {
				delete(body, "messages")
				body["input"] = "hello"
			}
			if protocol == "anthropic" {
				body["max_tokens"] = 100
			}
			response := call(h, "POST", requestPath(protocol), jsonString(body), token)
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || log.Status != 502 || log.Attempts != 1 || log.FirstTokenMS != nil || primaryCalls.Load() != 1 || backupCalls.Load() != 0 || !strings.Contains(response.Body.String(), "extension-payload") {
				t.Fatalf("extension dropped, timed as first token, or replayed: %+v body=%s", log, response.Body.String())
			}
		})
	}
}

func TestGroupRoutingConnectionAndLocalErrorScope(t *testing.T) {
	for _, mode := range []string{"connection", "local"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var backupCalls atomic.Int32
			backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backupCalls.Add(1); chatResponse(w) }))
			defer backupServer.Close()
			unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("closed server was reached") }))
			address := unavailable.URL
			unavailable.Close()
			channel := createChannel(t, h, cookie, "primary", address, "openai")
			if mode == "local" {
				if err := a.DB.Model(&channel).Update("api_key", "invalid-cipher").Error; err != nil {
					t.Fatal(err)
				}
			}
			token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backupServer.URL, "openai")}, "priority")
			group := routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 60, 300})
			response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[]}`, token)
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "connection" {
				if response.Code != 200 || log.Attempts != 3 || backupCalls.Load() != 1 {
					t.Fatalf("connection retries incorrect: %+v", log)
				}
				if _, ok := a.health[channel.ID]; !ok {
					t.Fatal("verified connection failure did not cool channel")
				}
				if _, ok := a.routeHealth[group.Routes[0].ID]; ok {
					t.Fatal("connection failure incorrectly limited to one model")
				}
			} else if response.Code != 500 || backupCalls.Load() != 0 || len(a.health) != 0 || len(a.routeHealth) != 0 {
				t.Fatalf("local credential error caused failover or cooldown: %+v", log)
			}
		})
	}
}

func TestResponseConversionLimitsAreLocal(t *testing.T) {
	for _, test := range []struct {
		protocol, body string
		local          bool
	}{
		{"openai", `{"choices":[{"message":{"content":"one"}},{"message":{"content":"two"}}]}`, true},
		{"openai", `{"choices":[{"message":{"content":[{"type":"image_url"}]}}]}`, true},
		{"anthropic", `{"type":"message","content":[{"type":"thinking","thinking":"opaque"}]}`, true},
		{"responses", `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_image"}]}]}`, true},
		{"responses", `{"object":"response","status":"completed","output":[{"type":"reasoning"}]}`, true},
		{"openai", `{"error":{"message":"upstream failed"}}`, false},
	} {
		value, _ := decodeJSON([]byte(test.body))
		_, err := parseCompletion(obj(value), test.protocol)
		var local responseProcessingError
		if err == nil || errors.As(err, &local) != test.local {
			t.Errorf("incorrect conversion failure scope for %s: %v", test.body, err)
		}
	}
}

func TestGroupRoutingResponseConversionFailureKeepsChannel(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var primaryCalls, backupCalls atomic.Int32
	responseBody := `{"id":"thinking","type":"message","model":"upstream-model","content":[{"type":"thinking","thinking":"opaque"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		fmt.Fprint(w, responseBody)
	}))
	defer primary.Close()
	backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		fmt.Fprint(w, responseBody)
	}))
	defer backupServer.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "primary", primary.URL, "anthropic"), createChannel(t, h, cookie, "backup", backupServer.URL, "anthropic")}, "priority")
	response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[]}`, token)
	var log RequestLog
	if err := a.DB.Last(&log).Error; err != nil {
		t.Fatal(err)
	}
	if response.Code != 500 || log.Status != 500 || log.Attempts != 1 || primaryCalls.Load() != 1 || backupCalls.Load() != 0 || len(a.health) != 0 || len(a.routeHealth) != 0 {
		t.Fatalf("local response conversion triggered retries or cooldown: %+v body=%s", log, response.Body.String())
	}
	response = call(h, "POST", "/v1/messages", `{"model":"my-model","messages":[],"max_tokens":100}`, token)
	if response.Code != 200 || primaryCalls.Load() != 2 || backupCalls.Load() != 0 || !strings.Contains(response.Body.String(), "opaque") {
		t.Fatalf("native request lost the healthy primary: %s", response.Body.String())
	}
}

func TestStreamStartupClassification(t *testing.T) {
	for _, test := range []struct {
		protocol, data string
		starts         bool
	}{
		{"openai", `{"choices":[{"delta":{"role":"assistant","content":""}}]}`, false},
		{"openai", `{"choices":[{"delta":{"extension":{"value":1}}}]}`, true},
		{"openai", `{"choices":[{"delta":{"extension":[]}}]}`, false},
		{"responses", `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"opaque"}}`, true},
		{"responses", `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, false},
		{"responses", `{"type":"response.created","response":{"output":[]}}`, false},
		{"responses", `{"type":"response.custom_event","payload":"opaque"}`, true},
		{"anthropic", `{"type":"ping"}`, false},
		{"anthropic", `{"type":"content_block_start","content_block":{"type":"text","text":""}}`, false},
		{"anthropic", `{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"opaque"}}`, true},
	} {
		if got := streamEventStartsOutput(sseEvent{Data: test.data}, test.protocol); got != test.starts {
			t.Errorf("%s: starts=%v, want=%v", test.data, got, test.starts)
		}
	}
}

func TestGroupRoutingLateSuccessCannotReleaseAnotherProbe(t *testing.T) {
	a := &App{health: map[uint]channelState{}, routeHealth: map[uint]channelState{}}
	config := GroupRelayConfig{2, 1, 1, 0}
	group := Group{ID: 1, Strategy: "priority", RelayConfig: &config, Routes: []Route{{ID: 1, ChannelID: 1}, {ID: 2, ChannelID: 2, Priority: 1}}}
	items := []candidate{{group.Routes[0], Channel{ID: 1}}, {group.Routes[1], Channel{ID: 2}}}
	slow, _, _ := a.pickGroup(group, items, nil, nil)
	a.recordMemberFailure(group, slow, false, nil, 503, "temporary")
	backupItem, _, _ := a.pickGroup(group, items, nil, nil)
	a.recordMemberSuccess(group, backupItem, false)
	health := a.routeHealth[1]
	health.Until = time.Now().Add(-time.Second)
	a.routeHealth[1] = health
	probeItem, ok, probe := a.pickGroup(group, items, nil, nil)
	if !ok || !probe || probeItem.Route.ID != 1 {
		t.Fatal("recovery probe not acquired")
	}
	if a.recordMemberSuccess(group, slow, false) {
		t.Fatal("stale request consumed a recovery probe it did not acquire")
	}
	if !a.routeHealth[1].Probing || a.groupRouting[1].Probe != 1 {
		t.Fatal("late success released another request's probe")
	}
	if !a.recordMemberSuccess(group, probeItem, true) {
		t.Fatal("probe owner could not record success")
	}
}

func TestGroupRoutingAffinityStartsOnStreamCommit(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { primaryCalls.Add(1); w.WriteHeader(503) }))
	defer primary.Close()
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		var body object
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			chatResponse(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_ = writeFrame(w, "", `{"choices":[{"index":0,"delta":{"content":"first"}}]}`)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_ = writeFrame(w, "", "[DONE]")
	}))
	defer backupServer.Close()
	defer once.Do(func() { close(finish) })
	channel := createChannel(t, h, cookie, "primary", primary.URL, "openai")
	token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backupServer.URL, "openai")}, "priority")
	group := routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 1, 300})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- call(h, "POST", "/v1/chat/completions", `{"model":"my-model","stream":true,"messages":[]}`, token)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("backup never started streaming")
	}
	ready := false
	for range 100 {
		a.mu.Lock()
		state := a.groupRouting[group.ID]
		ready = state != nil && state.Current == group.Routes[1].ID && time.Until(state.AffinityUntil) > 299*time.Second
		a.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ready {
		t.Fatal("affinity waited until the entire stream finished")
	}
	a.mu.Lock()
	health := a.routeHealth[group.Routes[0].ID]
	health.Until = time.Now().Add(-time.Second)
	a.routeHealth[group.Routes[0].ID] = health
	a.mu.Unlock()
	if response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[]}`, token); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if primaryCalls.Load() != 2 || backupCalls.Load() != 2 {
		t.Fatal("active backup stream did not keep concurrent requests on the backup")
	}
	once.Do(func() { close(finish) })
	select {
	case response := <-done:
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not finish")
	}
}
