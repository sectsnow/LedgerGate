package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGroupRetryAtScopesAndProbeWait(t *testing.T) {
	for _, status := range []int{0, 400, 401, 403, 404, 408, 429, 502, 503} {
		a := &App{health: map[uint]channelState{}, routeHealth: map[uint]channelState{}}
		group := Group{ID: 1, Strategy: "priority", Routes: []Route{{ID: 1, ChannelID: 1}}}
		items := []candidate{{group.Routes[0], Channel{ID: 1}}}
		until := time.Now().Add(time.Second)
		a.routeHealth[1] = channelState{Until: until, Status: status}
		at, ok := a.groupRetryAt(group, items, nil)
		want := status == 0 || status >= 500 || status == 408 || status == 429
		if ok != want || ok && !at.Equal(until) {
			t.Fatalf("status %d: wait=%v until=%v", status, ok, at)
		}
		manual := group
		manual.Strategy = "manual"
		if _, wait := a.groupRetryAt(manual, items, nil); wait {
			t.Fatal("manual selection must not wait and retry a cooled member")
		}
		if _, ok := a.groupRetryAt(group, items, map[uint]bool{1: true}); ok {
			t.Fatal("request retried an excluded credential")
		}
		a.routeHealth[1] = channelState{Until: time.Now().Add(-time.Second), Probing: true, Status: 503}
		if at, ok := a.groupRetryAt(group, items, nil); !ok || time.Until(at) < 900*time.Millisecond {
			t.Fatal("busy probe caused an immediate retry loop")
		}
		a.routeHealth[1] = channelState{Until: time.Now().Add(-time.Second), Status: 503}
		a.groupRouteLocked(group).Probe = 2
		if at, ok := a.groupRetryAt(group, items, nil); !ok || time.Until(at) < 900*time.Millisecond {
			t.Fatal("another member's recovery probe caused an immediate retry loop")
		}
	}
}

func TestGroupRuntimeSnapshotDoesNotChangeRouting(t *testing.T) {
	a := &App{health: map[uint]channelState{}, routeHealth: map[uint]channelState{}}
	group := Group{ID: 1, Strategy: "priority", Routes: []Route{{ID: 1, ChannelID: 1}}}
	state := a.groupRouteLocked(group)
	state.Current, state.AffinityUntil = 1, time.Now().Add(time.Minute)
	row := a.groupRuntimeSnapshot(group, map[uint]Channel{})
	if row.CurrentRoute != 0 || row.AffinityUntil != nil || row.Routes[0].State != "disabled" {
		t.Fatal("disabled channel still appeared as a usable current member")
	}
	if state.Current != 1 || state.AffinityUntil.IsZero() {
		t.Fatal("reading runtime changed actual routing state")
	}
	row = a.groupRuntimeSnapshot(group, map[uint]Channel{1: {ID: 1}})
	if row.CurrentRoute != 1 || row.AffinityUntil == nil || row.Routes[0].State != "current" {
		t.Fatal("enabled current member or affinity was hidden")
	}
	group.Routes[0].Priority++
	row = a.groupRuntimeSnapshot(group, map[uint]Channel{1: {ID: 1}})
	if row.CurrentRoute != 0 || row.AffinityUntil != nil || state.Revision == groupRoutingRevision(group) {
		t.Fatal("stale routing state survived a revision change or was modified by a read")
	}
}

func TestGatewayWaitsForMemberRecoveryWithinBudget(t *testing.T) {
	for _, mode := range []string{"cooldown", "probe_busy", "failure_then_recovery", "attempt_limit", "cancel", "time_limit", "disabled", "deleted", "reconfigured"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if mode == "attempt_limit" || mode == "failure_then_recovery" && n == 1 {
					w.WriteHeader(503)
					return
				}
				chatResponse(w)
			}))
			defer upstream.Close()
			channel := createChannel(t, h, cookie, "recovering", upstream.URL, "openai")
			token := createGroup(t, h, cookie, []Channel{channel}, "priority")
			group := routingGroupForTest(t, a, &GroupRelayConfig{1, 1, 1, 0})
			total, maxAttempts := 5, 3
			if mode == "time_limit" {
				total = 1
			}
			if mode == "attempt_limit" {
				maxAttempts = 2
			}
			if err := a.DB.Model(&Setting{}).Where("id=1").Updates(map[string]any{"request_timeout_seconds": total, "max_attempts": maxAttempts}).Error; err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(250 * time.Millisecond)
			if mode == "cancel" || mode == "time_limit" || mode == "disabled" || mode == "deleted" || mode == "reconfigured" {
				until = time.Now().Add(10 * time.Second)
			}
			a.mu.Lock()
			if mode != "failure_then_recovery" && mode != "attempt_limit" {
				a.routeHealth = map[uint]channelState{group.Routes[0].ID: {Until: until, Status: 503}}
			}
			if mode == "probe_busy" {
				a.routeHealth[group.Routes[0].ID] = channelState{Until: time.Now().Add(-time.Second), Probing: true, Status: 503}
				a.groupRouteLocked(group).Probe = group.Routes[0].ID
			}
			a.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"my-model","messages":[]}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, req); done <- w }()
			if mode == "cancel" || mode == "disabled" || mode == "probe_busy" || mode == "deleted" || mode == "reconfigured" {
				waiting := false
				for range 200 {
					rows, _ := a.requestSummaries()
					if len(rows) > 0 && rows[0].RetryAt != nil {
						waiting = true
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !waiting || calls.Load() != 0 {
					t.Fatal("waiting was hidden or counted as an upstream attempt")
				}
				if mode == "cancel" {
					cancel()
				}
				if mode == "disabled" {
					if err := a.DB.Model(&channel).Update("enabled", false).Error; err != nil {
						t.Fatal(err)
					}
				}
				if mode == "deleted" {
					if response := call(h, "DELETE", fmt.Sprintf("/api/groups/%d", group.ID), "", cookie); response.Code != 204 {
						t.Fatal(response.Body.String())
					}
				}
				if mode == "reconfigured" {
					if err := a.DB.Create(&Route{GroupID: group.ID, ChannelID: channel.ID, Model: "replacement-model", Weight: 1, Priority: 1}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if mode == "probe_busy" {
					a.mu.Lock()
					state := a.routeHealth[group.Routes[0].ID]
					state.Probing = false
					a.routeHealth[group.Routes[0].ID] = state
					a.groupRouting[group.ID].Probe = 0
					a.mu.Unlock()
				}
			}
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Fatal("request did not stop within its recovery budget")
			}
			log := latestRequestForTest(t, a)
			status, attempts := 200, 1
			switch mode {
			case "failure_then_recovery":
				attempts = 2
			case "attempt_limit":
				status, attempts = 503, 2
			case "cancel":
				status, attempts = 499, 0
			case "time_limit":
				status, attempts = 504, 0
			case "disabled":
				status, attempts = 503, 0
			case "deleted":
				status, attempts = 404, 0
			}
			if log.Status != status || log.Attempts != attempts || int(calls.Load()) != attempts || log.RetryAt != nil || log.RoutingReason == "" {
				t.Fatalf("unexpected recovery result: %+v calls=%d", log, calls.Load())
			}
			_, detail := historyDetailForTest(t, h, cookie, log.ID)
			if len(detail.Attempts) != attempts {
				t.Fatal("wait polluted the upstream attempt history")
			}
			for _, attempt := range detail.Attempts {
				if attempt.Reason == "" {
					t.Fatal("selection reason was not recorded")
				}
			}
			if mode == "attempt_limit" && (!strings.Contains(log.Error, "HTTP 503") || !strings.Contains(log.Error, "尝试次数上限")) {
				t.Fatal("attempt budget hid the upstream failure")
			}
			if mode == "reconfigured" && log.UpstreamModel != "replacement-model" {
				t.Fatal("waiting ignored the updated model routes")
			}
		})
	}
}

func TestGroupRuntimeStatusReportsAffinityCooldownAndProbe(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channels := []Channel{createChannel(t, h, cookie, "primary", "https://primary.invalid", "openai"), createChannel(t, h, cookie, "backup", "https://backup.invalid", "openai")}
	createGroup(t, h, cookie, channels, "priority")
	group := routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 60, 300})
	items := a.candidates(group)
	primary, _, _ := a.pickGroup(group, items, nil, nil)
	a.recordMemberFailure(group, primary, false, nil, 503, "upstream busy")
	backup, _, _ := a.pickGroup(group, items, nil, nil)
	a.recordMemberSuccess(group, backup, false)
	read := func() groupRuntime {
		t.Helper()
		response := call(h, "GET", "/api/groups/routing", "", cookie)
		var rows []groupRuntime
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rows) != nil || len(rows) != 1 {
			t.Fatal(response.Body.String())
		}
		return rows[0]
	}
	row := read()
	if row.CurrentRoute != backup.Route.ID || row.AffinityUntil == nil || time.Until(*row.AffinityUntil) < 299*time.Second || row.Routes[0].State != "cooling" || row.Routes[0].Scope != "route" || row.Routes[0].Error != "upstream busy" {
		t.Fatalf("runtime hid active routing state: %+v", row)
	}
	a.mu.Lock()
	state := a.routeHealth[primary.Route.ID]
	state.Until = time.Now().Add(-time.Second)
	a.routeHealth[primary.Route.ID] = state
	a.groupRouting[group.ID].AffinityUntil = time.Now().Add(-time.Second)
	a.mu.Unlock()
	if _, ok, probe := a.pickGroup(group, items, nil, nil); !ok || !probe {
		t.Fatal("recovery probe not acquired")
	}
	row = read()
	if row.ProbeRoute != primary.Route.ID || row.Routes[0].State != "probing" || row.CurrentRoute != backup.Route.ID {
		t.Fatalf("runtime hid the recovery probe: %+v", row)
	}
	if _, ok, probe := a.pickGroup(group, items, nil, nil); !ok || probe {
		t.Fatal("reading runtime released another request's probe")
	}
	if response := call(h, "GET", "/api/groups/routing", "", ""); response.Code != 401 {
		t.Fatal("runtime status was exposed without admin authentication")
	}
}
