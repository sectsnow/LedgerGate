package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChannelSwitchBlocksAndRestoresForwarding(t *testing.T) {
	for _, strategy := range []string{"manual", "priority", "weighted"} {
		t.Run(strategy, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				protocol := "openai"
				if r.URL.Path == "/v1/responses" {
					protocol = "responses"
				}
				if r.URL.Path == "/v1/messages" {
					protocol = "anthropic"
				}
				body, err := completionBody(completion{ID: "enabled-test", Text: "hello", Finish: "stop", Input: 1, Output: 1}, protocol, "upstream-model")
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, jsonString(body))
			}))
			defer upstream.Close()
			channel := createMultiProtocolChannel(t, h, cookie, upstream.URL, []string{"openai", "responses", "anthropic"})
			key := createGroup(t, h, cookie, []Channel{channel}, strategy)
			var before Channel
			if err := a.DB.First(&before, channel.ID).Error; err != nil {
				t.Fatal(err)
			}
			for _, enabled := range []bool{false, true} {
				action := "disable"
				if enabled {
					action = "enable"
				}
				result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{channel.ID}, Action: action})
				if result.Succeeded != 1 {
					t.Fatal("switch failed")
				}
				for _, protocol := range []string{"openai", "responses", "anthropic"} {
					body := object{"model": "my-model", "messages": []any{object{"role": "user", "content": "hello"}}, "max_tokens": 100}
					if protocol == "responses" {
						body = object{"model": "my-model", "input": "hello"}
					}
					response := call(h, "POST", requestPath(protocol), jsonString(body), key)
					want := 503
					if enabled {
						want = 200
					}
					if response.Code != want {
						t.Fatalf("enabled=%v %s: %d %s", enabled, protocol, response.Code, response.Body.String())
					}
				}
				wantCalls := int32(0)
				if enabled {
					wantCalls = 3
				}
				if calls.Load() != wantCalls {
					t.Fatal("disabled channel reached upstream, or enabled channel failed")
				}
			}
			var after Channel
			if err := a.DB.First(&after, channel.ID).Error; err != nil {
				t.Fatal(err)
			}
			if before.APIKey != after.APIKey || before.Name != after.Name || before.BaseURL != after.BaseURL {
				t.Fatal("switch overwrote channel configuration")
			}
			var routes int64
			a.DB.Model(&Route{}).Where("channel_id = ?", channel.ID).Count(&routes)
			if routes != 1 {
				t.Fatal("switch deleted routing configuration")
			}
		})
	}
}

func TestFailoverSkipsChannelDisabledDuringRequest(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
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
	one := createChannel(t, h, cookie, "primary", first.URL, "openai")
	two := createChannel(t, h, cookie, "backup", backup.URL, "openai")
	key := createGroup(t, h, cookie, []Channel{one, two}, "priority")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`, key)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first upstream request never started")
	}
	result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{two.ID}, Action: "disable"})
	if result.Succeeded != 1 {
		t.Fatal("could not disable backup")
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
		t.Fatal("gateway called a backup disabled after candidate selection")
	}
}
