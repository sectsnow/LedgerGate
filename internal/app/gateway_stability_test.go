package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayTimeoutPhases(t *testing.T) {
	for _, mode := range []string{"active_stream", "channel_first_output", "global_first_output", "idle_stream", "total_budget", "json_response"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			first, idle, total, channelTimeout := 3, 1, 5, 1
			if mode == "global_first_output" {
				first, channelTimeout = 1, 3
			}
			if mode == "total_budget" {
				total = 1
			}
			if mode == "json_response" {
				channelTimeout = 2
			}
			if err := a.DB.Model(&Setting{}).Where("id=1").Updates(map[string]any{"first_output_seconds": first, "stream_idle_seconds": idle, "request_timeout_seconds": total, "max_attempts": 1}).Error; err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "json_response" {
					fmt.Fprint(w, `{"id":"partial"`)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				event := `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`
				if strings.Contains(mode, "first_output") {
					event = `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`
				}
				_ = writeFrame(w, "", event)
				w.(http.Flusher).Flush()
				if mode == "idle_stream" {
					<-r.Context().Done()
					return
				}
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				finish := time.NewTimer(1500 * time.Millisecond)
				defer finish.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						if err := writeFrame(w, "", event); err != nil {
							return
						}
						w.(http.Flusher).Flush()
					case <-finish.C:
						_, _ = fmt.Fprint(w, streamFixture("openai"))
						return
					}
				}
			}))
			defer upstream.Close()
			channel := createChannel(t, h, cookie, "phases", upstream.URL, "openai")
			if err := a.DB.Model(&channel).Update("timeout_seconds", channelTimeout).Error; err != nil {
				t.Fatal(err)
			}
			token := createGroup(t, h, cookie, []Channel{channel}, "priority")
			response := call(h, "POST", "/v1/chat/completions", jsonString(object{"model": "my-model", "stream": mode != "json_response", "messages": []any{}}), token)
			log := latestRequestForTest(t, a)
			_, detail := historyDetailForTest(t, h, cookie, log.ID)
			status, message := 502, errChannelResponseTimeout.Error()
			switch mode {
			case "active_stream":
				status, message = 200, ""
			case "channel_first_output", "global_first_output":
				message = errFirstOutput.Error()
			case "idle_stream":
				message = errStreamIdle.Error()
			case "total_budget":
				status, message = 504, "请求已达到总时间上限"
			}
			if log.Status != status || log.Attempts != 1 || len(detail.Attempts) != 1 || detail.Attempts[0].Error != message {
				t.Fatalf("unexpected timeout phase: %+v body=%s", log, response.Body.String())
			}
			if status == 200 && (!strings.Contains(response.Body.String(), "[DONE]") || log.OutputTokens != 7) {
				t.Fatalf("active stream lost completion or usage: %+v body=%s", log, response.Body.String())
			}
			if len(a.health) != 0 || len(a.routeHealth) != 0 {
				t.Fatal("committed stream or request budget failure cooled the upstream")
			}
		})
	}
}

func TestGatewayRecoveryProbeRetryStopsAndReleases(t *testing.T) {
	for _, mode := range []string{"cancel", "time", "attempts", "disabled"} {
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
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				chatResponse(w)
			}))
			defer backup.Close()
			channel := createChannel(t, h, cookie, "primary", primary.URL, "openai")
			token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backup.URL, "openai")}, "priority")
			group := routingGroupForTest(t, a, &GroupRelayConfig{3, 2, 60, 300})
			if mode == "time" {
				if err := a.DB.Model(&Setting{}).Where("id=1").Update("request_timeout_seconds", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "attempts" {
				if err := a.DB.Model(&Setting{}).Where("id=1").Update("max_attempts", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			expired := time.Now().Add(-time.Second)
			a.mu.Lock()
			state := a.groupRouteLocked(group)
			state.Current = group.Routes[1].ID
			a.routeHealth = map[uint]channelState{group.Routes[0].ID: {Until: expired}}
			a.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"my-model","messages":[]}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			done := make(chan struct{})
			go func() { h.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
			select {
			case <-failed:
			case <-time.After(5 * time.Second):
				t.Fatal("recovery probe never started")
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "disabled" {
				if err := a.DB.Model(&channel).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("probe retry ignored cancellation, budget or channel disable")
			}
			log := latestRequestForTest(t, a)
			status, backups := 503, int32(0)
			switch mode {
			case "cancel":
				status = 499
			case "time":
				status = 504
			case "disabled":
				status, backups = 200, 1
			}
			if log.Status != status || primaryCalls.Load() != 1 || backupCalls.Load() != backups {
				t.Fatalf("unexpected continuation after %s: %+v primary=%d backup=%d", mode, log, primaryCalls.Load(), backupCalls.Load())
			}
			a.mu.Lock()
			health := a.routeHealth[group.Routes[0].ID]
			released := !health.Probing && state.Probe == 0 && health.Until.Equal(expired) && len(a.health) == 0
			a.mu.Unlock()
			if !released {
				t.Fatal("stopped probe retained its reservation or renewed cooldown")
			}
		})
	}
}

func TestGatewayCommittedFailureKeepsNextRoute(t *testing.T) {
	for _, strategy := range []string{"priority", "manual", "weighted"} {
		for _, mode := range []string{"error_event", "missing_terminal", "idle"} {
			t.Run(strategy+"/"+mode, func(t *testing.T) {
				a := testApp(t)
				h := a.Router()
				cookie := loginForTest(t, h)
				if err := a.DB.Model(&Setting{}).Where("id=1").Update("stream_idle_seconds", 1).Error; err != nil {
					t.Fatal(err)
				}
				var primaryCalls, backupCalls atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					if primaryCalls.Add(1) > 1 {
						fmt.Fprint(w, streamFixture("openai"))
						return
					}
					_ = writeFrame(w, "", `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`)
					w.(http.Flusher).Flush()
					if mode == "error_event" {
						_ = writeFrame(w, "error", `{"error":{"message":"upstream stopped"}}`)
					}
					if mode == "idle" {
						<-r.Context().Done()
					}
				}))
				defer primary.Close()
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupCalls.Add(1)
					fmt.Fprint(w, streamFixture("openai"))
				}))
				defer backup.Close()
				channel := createChannel(t, h, cookie, "primary", primary.URL, "openai")
				token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backup.URL, "openai")}, strategy)
				body := `{"model":"my-model","stream":true,"messages":[]}`
				response := call(h, "POST", "/v1/chat/completions", body, token)
				log := latestRequestForTest(t, a)
				if response.Code != 200 || log.Status != 502 || log.Attempts != 1 || log.ChannelID != channel.ID || !strings.Contains(response.Body.String(), "partial") || !strings.Contains(response.Body.String(), "event: error") {
					t.Fatalf("committed failure was hidden or replayed: %+v body=%s", log, response.Body.String())
				}
				if len(a.health) != 0 || len(a.routeHealth) != 0 {
					t.Fatal("one committed failure cooled the upstream")
				}
				response = call(h, "POST", "/v1/chat/completions", body, token)
				log = latestRequestForTest(t, a)
				if response.Code != 200 || log.Status != 200 || log.Attempts != 1 || log.ChannelID != channel.ID || primaryCalls.Load() != 2 || backupCalls.Load() != 0 {
					t.Fatalf("next request switched after a committed failure: %+v primary=%d backup=%d", log, primaryCalls.Load(), backupCalls.Load())
				}
			})
		}
	}
}

func TestGatewayRecoveryProbeRetriesBeforeSwitch(t *testing.T) {
	for _, mode := range []string{"recovered", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var primaryCalls, backupCalls atomic.Int32
			failed, finishSecond := make(chan struct{}), make(chan struct{})
			var finishOnce sync.Once
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := primaryCalls.Add(1)
				if n == 1 {
					w.WriteHeader(503)
					close(failed)
					return
				}
				select {
				case <-finishSecond:
				case <-r.Context().Done():
					return
				}
				if mode == "recovered" {
					chatResponse(w)
				} else {
					w.WriteHeader(503)
				}
			}))
			defer primary.Close()
			defer finishOnce.Do(func() { close(finishSecond) })
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				chatResponse(w)
			}))
			defer backup.Close()
			token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "primary", primary.URL, "openai"), createChannel(t, h, cookie, "backup", backup.URL, "openai")}, "priority")
			group := routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 60, 300})
			a.mu.Lock()
			state := a.groupRouteLocked(group)
			state.Current = group.Routes[1].ID
			state.AffinityUntil = time.Now().Add(-time.Second)
			a.routeHealth = map[uint]channelState{group.Routes[0].ID: {Until: time.Now().Add(-time.Second), Error: "previous failure"}}
			if mode == "exhausted" {
				a.health[group.Routes[0].ChannelID] = a.routeHealth[group.Routes[0].ID]
				delete(a.routeHealth, group.Routes[0].ID)
			}
			a.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := `{"model":"my-model","messages":[]}`
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, req); done <- w }()
			select {
			case <-failed:
			case <-time.After(5 * time.Second):
				t.Fatal("recovery probe never started")
			}
			select {
			case response := <-done:
				t.Fatalf("recovery probe skipped member retry: %s", response.Body.String())
			case <-time.After(100 * time.Millisecond):
			}
			a.mu.Lock()
			health := a.routeHealth[group.Routes[0].ID]
			if mode == "exhausted" {
				health = a.health[group.Routes[0].ChannelID]
			}
			reserved := state.Probe == group.Routes[0].ID && health.Probing && !time.Now().Before(health.Until)
			a.mu.Unlock()
			if !reserved {
				t.Fatal("retry released the probe or cooled before exhausting attempts")
			}
			probeID := latestRequestForTest(t, a).ID
			if response := call(h, "POST", "/v1/chat/completions", body, token); response.Code != 200 {
				t.Fatal("concurrent request could not use the backup", response.Body.String())
			}
			if backupCalls.Load() != 1 {
				t.Fatal("concurrent request acquired the reserved recovery probe")
			}
			finishOnce.Do(func() { close(finishSecond) })
			select {
			case response := <-done:
				if response.Code != 200 {
					t.Fatal(response.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("recovery probe retry did not finish")
			}
			log, detail := historyDetailForTest(t, h, cookie, probeID)
			attempts, backups := 2, int32(1)
			if mode == "exhausted" {
				attempts, backups = 3, 2
			}
			if log.Status != 200 || log.Attempts != attempts || len(detail.Attempts) != attempts || primaryCalls.Load() != 2 || backupCalls.Load() != backups {
				t.Fatalf("probe retry or failover incorrect: %+v primary=%d backup=%d", log, primaryCalls.Load(), backupCalls.Load())
			}
			for _, attempt := range detail.Attempts[:2] {
				if attempt.ChannelID != group.Routes[0].ChannelID {
					t.Fatal("probe switched before exhausting member attempts")
				}
			}
		})
	}
}
