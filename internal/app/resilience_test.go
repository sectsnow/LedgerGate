package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoute404DoesNotCoolOtherModelsAndRetryAfter(t *testing.T) {
	a := testApp(t)
	channel := Channel{ID: 42, CooldownSeconds: 10}
	missing, healthy := Route{ID: 1, ChannelID: 42}, Route{ID: 2, ChannelID: 42}
	a.recordMemberFailure(Group{}, candidate{missing, channel}, false, http.Header{}, 404, "unknown model")
	if len(a.health) != 0 {
		t.Fatal("model failure cooled the whole channel")
	}
	if _, ok := a.pick([]candidate{{healthy, channel}}, "priority", nil); !ok {
		t.Fatal("other model was blocked")
	}
	if _, ok := a.pick([]candidate{{missing, channel}}, "priority", nil); ok {
		t.Fatal("missing model was not cooled")
	}
	a.recordMemberFailure(Group{}, candidate{healthy, channel}, false, http.Header{"Retry-After": []string{"120"}}, 429, "rate limited")
	if time.Until(a.routeHealth[healthy.ID].Until) < 119*time.Second {
		t.Fatal("Retry-After ignored")
	}
	if got := retryAfter("99999999", time.Now(), time.Second); time.Until(got) > time.Hour {
		t.Fatal("retry delay unbounded")
	}
}

func TestStreamIdleStopsWithoutReplayingCommittedOutput(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	a.DB.Model(&Setting{}).Where("id=1").Updates(map[string]any{"stream_idle_seconds": 1, "first_output_seconds": 5})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"p\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer primary.Close()
	var retries atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { retries.Add(1); chatResponse(w) }))
	defer backup.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "primary", primary.URL, "openai"), createChannel(t, h, cookie, "backup", backup.URL, "openai")}, "priority")
	response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","stream":true,"messages":[]}`, token)
	if retries.Load() != 0 || !strings.Contains(response.Body.String(), "partial") || !strings.Contains(response.Body.String(), errStreamIdle.Error()) {
		t.Fatal(response.Body.String())
	}
}

func TestRetryBudgetBoundsAttemptsAndTotalTime(t *testing.T) {
	for _, mode := range []string{"attempts", "time"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			changes := map[string]any{"max_attempts": 1}
			if mode == "time" {
				changes = map[string]any{"request_timeout_seconds": 1, "first_output_seconds": 5}
			}
			a.DB.Model(&Setting{}).Where("id=1").Updates(changes)
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "time" {
					<-r.Context().Done()
				} else {
					w.WriteHeader(503)
				}
			}))
			defer primary.Close()
			var backupCalls atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backupCalls.Add(1); chatResponse(w) }))
			defer backup.Close()
			token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "primary", primary.URL, "openai"), createChannel(t, h, cookie, "backup", backup.URL, "openai")}, "priority")
			response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[]}`, token)
			if response.Code < 400 || backupCalls.Load() != 0 {
				t.Fatal("request exceeded configured retry budget")
			}
		})
	}
}

func TestFirstOutputGuardIgnoresInitialRoleAndFailsOver(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	a.DB.Model(&Setting{}).Where("id=1").Update("first_output_seconds", 1)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"start\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"ok\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer backup.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "slow", primary.URL, "openai"), createChannel(t, h, cookie, "backup", backup.URL, "openai")}, "priority")
	response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","stream":true,"messages":[{"role":"user","content":"test"}]}`, token)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "recovered") || strings.Contains(response.Body.String(), "start") {
		t.Fatal(response.Body.String())
	}
}

func TestSlowAccountDoesNotBlockOtherAccount(t *testing.T) {
	a := testApp(t)
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	defer server.Close()
	accounts := []Account{{Name: "one", Kind: "newapi", BaseURL: server.URL, Enabled: true, IntervalSeconds: 300}, {Name: "two", Kind: "newapi", BaseURL: server.URL, Enabled: true, IntervalSeconds: 300}}
	if err := a.DB.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.checkDueAccounts(ctx); close(done) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("collection was serialized")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("collection did not stop")
	}
}
