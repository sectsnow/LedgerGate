package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIncompleteReasonsRemainDistinct(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter", "vendor_reason", ""} {
		body := object{"id": "r", "object": "response", "status": "incomplete", "output": []any{}}
		if reason != "" {
			body["incomplete_details"] = object{"reason": reason}
		}
		want := "incomplete"
		if reason == "max_output_tokens" {
			want = "length"
		}
		if reason == "content_filter" {
			want = "content_filter"
		}
		result, err := parseCompletion(body, "responses")
		if err != nil || result.Finish != want || result.IncompleteReason != reason {
			t.Fatalf("JSON reason collapsed: %+v %v", result, err)
		}
		stream := completion{}
		_, done, err := inspectEvent(sseEvent{Event: "response.incomplete", Data: jsonString(object{"response": body})}, "responses", &stream, false)
		if err != nil || !done || stream.Finish != want || stream.IncompleteReason != reason {
			t.Fatalf("SSE reason collapsed: %+v %v", stream, err)
		}
		restored, err := completionBody(result, "responses", "m")
		if err != nil || restored["status"] != "incomplete" || str(obj(restored["incomplete_details"])["reason"]) != reason {
			t.Fatal("Responses incomplete reason did not survive conversion")
		}
		for _, target := range []string{"openai", "anthropic"} {
			converted, err := completionBody(result, target, "m")
			if want == "incomplete" {
				if err == nil {
					t.Fatal("unknown incomplete reason silently mapped to a token limit")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseCompletion(converted, target)
			if err != nil || parsed.Finish != want {
				t.Fatalf("reason changed for %s: %+v %v", target, parsed, err)
			}
		}
	}
	for _, status := range []string{"failed", "cancelled"} {
		result := completion{}
		_, done, err := inspectEvent(sseEvent{Event: "response.completed", Data: jsonString(object{"response": object{"status": status}})}, "responses", &result, false)
		if err == nil || done {
			t.Fatal("failed or cancelled upstream was counted as completed")
		}
	}
	if text := boundedDiagnostic(strings.Repeat("长", 512)); len(text) > 515 || !strings.HasSuffix(text, "…") {
		t.Fatal("upstream reason bypassed the diagnostic preview bound")
	}
}

func TestGatewayRecordsTerminalReasonAndUpstreamIdentity(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body object
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		reason := str(body["user"])
		w.Header().Set("Request-ID", "provider-"+reason)
		response := object{"id": "response-test", "object": "response", "status": "incomplete", "incomplete_details": object{"reason": reason}, "output": []any{object{"type": "message", "content": []any{object{"type": "output_text", "text": "partial"}}}}, "usage": object{"input_tokens": 10, "output_tokens": 5}}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_ = writeFrame(w, "response.output_text.delta", `{"delta":"partial"}`)
			_ = writeFrame(w, "response.incomplete", jsonString(object{"response": response}))
		} else {
			fmt.Fprint(w, jsonString(response))
		}
	}))
	defer upstream.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "terminal", upstream.URL, "responses")}, "priority")
	for _, protocol := range []string{"responses", "openai", "anthropic"} {
		for _, reason := range []string{"max_output_tokens", "content_filter", "vendor_reason"} {
			if reason == "vendor_reason" && protocol != "responses" {
				continue
			}
			for _, stream := range []bool{false, true} {
				body := object{"model": "my-model", "stream": stream, "messages": []any{object{"role": "user", "content": "hello"}}, "user": reason}
				if protocol == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
				}
				if protocol == "anthropic" {
					delete(body, "user")
					body["metadata"], body["max_tokens"] = object{"user_id": reason}, 100
				}
				response := call(h, "POST", requestPath(protocol), jsonString(body), token)
				log := latestRequestForTest(t, a)
				_, detail := historyDetailForTest(t, h, cookie, log.ID)
				want := "incomplete"
				if reason == "max_output_tokens" {
					want = "length"
				}
				if reason == "content_filter" {
					want = "content_filter"
				}
				if response.Code != 200 || log.Status != 200 || log.State != "completed" || log.FinishReason != want || log.IncompleteReason != reason || len(detail.Attempts) != 1 {
					t.Fatalf("terminal result changed for %s/%s/%v: %+v %s", protocol, reason, stream, log, response.Body.String())
				}
				attempt := detail.Attempts[0]
				if attempt.UpstreamStatus != 200 || attempt.UpstreamRequestID != "provider-"+reason || attempt.FinishReason != want || attempt.IncompleteReason != reason {
					t.Fatalf("attempt diagnostic fields missing: %+v", attempt)
				}
				if len(a.health) != 0 || len(a.routeHealth) != 0 {
					t.Fatal("valid terminal reason cooled an upstream")
				}
				if protocol == "openai" && !strings.Contains(response.Body.String(), `"finish_reason":"`+want+`"`) {
					t.Fatal("Chat finish reason was not preserved")
				}
			}
		}
	}
}

func TestEveryAttemptKeepsItsUpstreamIdentity(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("X-Request-ID", fmt.Sprintf("provider-%d", n))
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		chatResponse(w)
	}))
	defer upstream.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "retry", upstream.URL, "openai")}, "priority")
	routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 60, 300})
	response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[]}`, token)
	log := latestRequestForTest(t, a)
	_, detail := historyDetailForTest(t, h, cookie, log.ID)
	if response.Code != 200 || len(detail.Attempts) != 2 || detail.Attempts[0].UpstreamRequestID != "provider-1" || detail.Attempts[1].UpstreamRequestID != "provider-2" || detail.Attempts[0].UpstreamStatus != 503 || detail.Attempts[1].UpstreamStatus != 200 {
		t.Fatalf("retry overwrote upstream correlation: %+v", detail.Attempts)
	}
	for _, invalid := range []string{strings.Repeat("x", 513), "id\nsecret", "id\x7f"} {
		if upstreamRequestID(http.Header{"X-Request-Id": []string{invalid}}) != "" {
			t.Fatal("unbounded or malformed upstream ID was logged")
		}
	}
}
