package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayNativeHeadersAndExtensionFields(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body object
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "upstream-model" || obj(body["vendor_extension"])["opaque"] != "keep-me" || r.UserAgent() != "native-sdk-test" {
					t.Error("native fields or SDK identity were lost")
				}
				if protocol == "anthropic" {
					if r.Header.Get("Anthropic-Beta") != "test-beta" || r.Header.Get("Anthropic-Version") != "2024-01-01" || r.Header.Get("X-API-Key") != "private-upstream-key" || r.Header.Get("Authorization") != "" {
						t.Error("native Anthropic headers were lost or credentials replaced")
					}
				} else if r.Header.Get("OpenAI-Beta") != "test-beta" || r.Header.Get("Authorization") != "Bearer private-upstream-key" {
					t.Error("native OpenAI headers were lost or credentials replaced")
				}
				if r.Header.Get("X-Unrelated-Client-Header") != "" {
					t.Error("unrelated client headers were forwarded")
				}
				w.Header().Set("X-Request-ID", "upstream-native-id")
				w.Header().Set("X-RateLimit-Remaining-Requests", "10")
				w.Header().Set("Set-Cookie", "upstream-session=do-not-forward")
				if body["stream"] == true {
					w.Header().Set("Content-Type", "text/event-stream")
					_ = writeFrame(w, "vendor.extension", `{"type":"vendor.extension","payload":"native-extra"}`)
					fmt.Fprint(w, streamFixture(protocol))
					return
				}
				response, err := completionBody(completion{ID: "native", Text: "hello", Input: 12, Output: 7, UsageSeen: true, Finish: "stop"}, protocol, "upstream-model")
				if err != nil {
					t.Error(err)
					return
				}
				response["vendor_extension"] = "native-extra"
				fmt.Fprint(w, jsonString(response))
			}))
			defer upstream.Close()
			token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "native", upstream.URL, protocol)}, "priority")
			for _, stream := range []bool{false, true} {
				body := object{"model": "my-model", "stream": stream, "messages": []any{object{"role": "user", "content": "hello"}}, "vendor_extension": object{"opaque": "keep-me"}}
				if protocol == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
					body["reasoning"] = object{"effort": "high"}
				}
				if protocol == "anthropic" {
					body["max_tokens"] = 100
					body["thinking"] = object{"type": "adaptive"}
				}
				req := httptest.NewRequest("POST", requestPath(protocol), strings.NewReader(jsonString(body)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("User-Agent", "native-sdk-test")
				req.Header.Set("OpenAI-Beta", "test-beta")
				req.Header.Set("Anthropic-Version", "2024-01-01")
				req.Header.Set("Anthropic-Beta", "test-beta")
				req.Header.Set("X-Unrelated-Client-Header", "private-client-data")
				response := httptest.NewRecorder()
				h.ServeHTTP(response, req)
				log := latestRequestForTest(t, a)
				if response.Code != 200 || log.Status != 200 || !strings.Contains(response.Body.String(), "native-extra") {
					t.Fatalf("native response was changed: %+v %s", log, response.Body.String())
				}
				if response.Header().Get("X-Upstream-Request-ID") != "upstream-native-id" || response.Header().Get("X-Request-ID") == "upstream-native-id" || response.Header().Get("X-RateLimit-Remaining-Requests") != "10" || response.Header().Get("Set-Cookie") != "" {
					t.Fatal("response headers lost correlation or forwarded an upstream cookie")
				}
			}
		})
	}
}

func TestCrossProtocolDoesNotForwardProviderHeaders(t *testing.T) {
	incoming := httptest.NewRequest("POST", "/", nil)
	incoming.Header.Set("Authorization", "Bearer client-key")
	incoming.Header.Set("Anthropic-Beta", "private-beta")
	incoming.Header.Set("Anthropic-Version", "private-version")
	incoming.Header.Set("OpenAI-Beta", "private-beta")
	outgoing := httptest.NewRequest("POST", "/", nil)
	setUpstreamAuth(outgoing, "openai", "upstream-key")
	copyNativeRequestHeaders(outgoing, incoming, "anthropic", "openai")
	if outgoing.Header.Get("Authorization") != "Bearer upstream-key" || outgoing.Header.Get("Anthropic-Beta") != "" || outgoing.Header.Get("OpenAI-Beta") != "" {
		t.Fatal("cross-protocol forwarding copied client credentials or provider flags")
	}
}

func TestJSONOutputFormatRoundTrip(t *testing.T) {
	for _, format := range []object{{"type": "text"}, {"type": "json_object"}, {"type": "json_schema", "json_schema": object{"name": "answer", "description": "structured answer", "strict": true, "schema": object{"type": "object", "properties": object{"answer": object{"type": "string"}}, "required": []any{"answer"}, "additionalProperties": false}}}} {
		body := object{"model": "m", "messages": []any{object{"role": "user", "content": "hello"}}, "response_format": format}
		before := jsonString(body)
		converted, err := convertRequest(body, "openai", "responses")
		if err != nil {
			t.Fatal(err)
		}
		restored, err := convertRequest(converted, "responses", "openai")
		if err != nil || jsonString(restored["response_format"]) != jsonString(format) || jsonString(body) != before {
			t.Fatalf("JSON format semantics were lost: converted=%s restored=%s err=%v", jsonString(converted), jsonString(restored), err)
		}
	}
	for _, text := range []any{"invalid", object{"verbosity": "high"}, object{"format": object{"type": "unknown"}}} {
		if _, err := convertRequest(object{"model": "m", "input": "hello", "text": text}, "responses", "openai"); err == nil {
			t.Fatal("unsupported text options were silently discarded")
		}
	}
	if _, err := convertRequest(object{"model": "m", "messages": []any{object{"role": "user", "content": "hello"}}, "response_format": object{"type": "json_object"}}, "openai", "anthropic"); err == nil {
		t.Fatal("unrepresentable Anthropic JSON format was silently discarded")
	}
	for _, test := range []struct {
		from, to string
		body     object
	}{
		{"openai", "responses", object{"model": "m", "messages": []any{object{"role": "user", "content": "hello"}}, "text": object{"verbosity": "high"}}},
		{"responses", "openai", object{"model": "m", "input": "hello", "response_format": object{"type": "json_object"}}},
		{"openai", "responses", object{"model": "m", "messages": []any{object{"role": "user", "content": "hello"}}, "response_format": object{"type": "json_schema", "json_schema": object{}, "extension": true}}},
	} {
		if _, err := convertRequest(test.body, test.from, test.to); err == nil {
			t.Fatal("a misplaced or unrepresentable format option was silently discarded")
		}
	}
}

func TestParallelToolChoiceRoundTrip(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, choice := range []any{"auto", "required", object{"type": "function", "function": object{"name": "weather"}}} {
			body := object{"model": "m", "messages": []any{object{"role": "user", "content": "weather"}}, "parallel_tool_calls": parallel, "tool_choice": choice}
			anthropic, err := convertRequest(body, "openai", "anthropic")
			if err != nil || obj(anthropic["tool_choice"])["disable_parallel_tool_use"] != !parallel {
				t.Fatalf("parallel tool preference lost: %s %v", jsonString(anthropic), err)
			}
			responses, err := convertRequest(anthropic, "anthropic", "responses")
			if err != nil || responses["parallel_tool_calls"] != parallel {
				t.Fatalf("Anthropic preference lost: %s %v", jsonString(responses), err)
			}
			restored, err := convertRequest(responses, "responses", "openai")
			if err != nil || restored["parallel_tool_calls"] != parallel || jsonString(restored["tool_choice"]) != jsonString(choice) {
				t.Fatalf("tool choice changed during conversion: %s %v", jsonString(restored), err)
			}
		}
	}
}
