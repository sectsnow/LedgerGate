package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGatewayPreflightsCompatibleMembers(t *testing.T) {
	for _, mode := range []string{"priority", "weighted", "manual", "all_incompatible", "preserve_priority"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var firstCalls, secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { firstCalls.Add(1); chatResponse(w) }))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				response, _ := completionBody(completion{ID: "native", Text: "ok", Finish: "stop"}, "responses", "upstream-model")
				fmt.Fprint(w, jsonString(response))
			}))
			defer second.Close()
			protocol := "responses"
			if mode == "all_incompatible" {
				protocol = "openai"
			}
			strategy := mode
			if mode == "all_incompatible" || mode == "preserve_priority" {
				strategy = "priority"
			}
			channels := []Channel{createChannel(t, h, cookie, "first", first.URL, "openai"), createChannel(t, h, cookie, "second", second.URL, protocol)}
			token := createGroup(t, h, cookie, channels, strategy)
			body := object{"model": "my-model", "input": "hello", "reasoning": object{"effort": "high"}}
			if mode == "preserve_priority" {
				delete(body, "reasoning")
			}
			response := call(h, "POST", "/v1/responses", jsonString(body), token)
			log := latestRequestForTest(t, a)
			if mode == "manual" || mode == "all_incompatible" {
				if response.Code != 400 || log.Attempts != 0 || firstCalls.Load() != 0 || secondCalls.Load() != 0 {
					t.Fatalf("unsupported member was called or manual selection changed: %+v", log)
				}
			} else if response.Code != 200 || log.Status != 200 || log.Attempts != 1 {
				t.Fatalf("compatible member was not used: %+v %s", log, response.Body.String())
			} else if mode == "preserve_priority" {
				if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
					t.Fatal("native member overrode compatible higher priority")
				}
			} else if firstCalls.Load() != 0 || secondCalls.Load() != 1 || !strings.Contains(log.RoutingReason, "不兼容") {
				t.Fatal("incompatible member used or skip reason missing")
			}
			if len(a.health) != 0 || len(a.routeHealth) != 0 {
				t.Fatal("local incompatibility cooled an upstream")
			}
		})
	}
}
