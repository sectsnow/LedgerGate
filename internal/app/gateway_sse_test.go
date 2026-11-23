package app

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStreamEventNameAndCompleteOutputDetection(t *testing.T) {
	for _, test := range []struct {
		name, protocol, event, data string
		output                      bool
	}{
		{"text delta", "responses", "response.output_text.delta", `{"delta":"hello"}`, true},
		{"reasoning delta", "responses", "response.reasoning_summary_text.delta", `{"delta":"thinking"}`, true},
		{"text snapshot", "responses", "response.output_text.done", `{"text":"hello"}`, true},
		{"tool snapshot", "responses", "response.function_call_arguments.done", `{"arguments":"{}"}`, true},
		{"content part", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":"hello"}}`, true},
		{"message snapshot", "responses", "response.output_item.done", `{"item":{"type":"message","content":[{"type":"output_text","text":"hello"}]}}`, true},
		{"reasoning snapshot", "responses", "response.output_item.done", `{"item":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}}`, true},
		{"anthropic delta", "anthropic", "content_block_delta", `{"delta":{"type":"text_delta","text":"hello"}}`, true},
		{"anthropic tool", "anthropic", "content_block_start", `{"content_block":{"type":"tool_use","name":"search"}}`, true},
		{"metadata", "responses", "response.in_progress", `{"response":{"id":"r"}}`, false},
		{"empty delta", "responses", "response.output_text.delta", `{"delta":""}`, false},
		{"empty message", "responses", "response.output_item.added", `{"item":{"type":"message","content":[]}}`, false},
		{"body type wins", "responses", "response.output_text.delta", `{"type":"response.in_progress","delta":"metadata"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := streamEventHasOutput(sseEvent{Event: test.event, Data: test.data}, test.protocol); got != test.output {
				t.Fatalf("output detection = %v, want %v", got, test.output)
			}
		})
	}
}

func TestGatewaySSEEventNames(t *testing.T) {
	for _, from := range []string{"responses", "anthropic"} {
		for _, to := range []string{from, "openai"} {
			t.Run(from+"/"+to, func(t *testing.T) {
				a := testApp(t)
				h := a.Router()
				cookie := loginForTest(t, h)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					scanner := bufio.NewScanner(strings.NewReader(streamFixture(from)))
					for {
						event, err := readEvent(scanner)
						if err == io.EOF {
							return
						}
						if err != nil {
							t.Error(err)
							return
						}
						value, _ := decodeJSON([]byte(event.Data))
						body := obj(value)
						kind := str(body["type"])
						delete(body, "type")
						if err := writeFrame(w, kind, jsonString(body)); err != nil {
							return
						}
					}
				}))
				defer upstream.Close()
				channel := createChannel(t, h, cookie, "event-names", upstream.URL, from)
				token := createGroup(t, h, cookie, []Channel{channel}, "priority")
				body := object{"model": "my-model", "stream": true, "messages": []any{object{"role": "user", "content": "hello"}}}
				if to == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
				}
				if to == "anthropic" {
					body["max_tokens"] = 100
				}
				response := call(h, "POST", requestPath(to), jsonString(body), token)
				var log RequestLog
				if err := a.DB.Last(&log).Error; err != nil {
					t.Fatal(err)
				}
				if response.Code != 200 || log.Status != 200 || log.Attempts != 1 || log.FirstTokenMS == nil || log.InputTokens != 12 || log.OutputTokens != 7 || !strings.Contains(response.Body.String(), "你好") {
					t.Fatalf("event-only type lost content, completion, timing or usage: status=%d log=%+v body=%s", response.Code, log, response.Body.String())
				}
			})
		}
	}
}

func TestGatewaySSEPrefaceKeepsChannel(t *testing.T) {
	for _, limit := range []string{"count", "bytes"} {
		for _, to := range []string{"responses", "openai"} {
			t.Run(limit+"/"+to, func(t *testing.T) {
				a := testApp(t)
				h := a.Router()
				cookie := loginForTest(t, h)
				count, padding := 1280, ""
				if limit == "bytes" {
					count, padding = 24, strings.Repeat("x", 64<<10)
				}
				var primaryCalls, backupCalls atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					primaryCalls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					for i := range count {
						if err := writeFrame(w, "response.in_progress", jsonString(object{"type": "response.in_progress", "sequence_number": i, "padding": padding})); err != nil {
							return
						}
					}
					fmt.Fprint(w, streamFixture("responses"))
				}))
				defer primary.Close()
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupCalls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, streamFixture("responses"))
				}))
				defer backup.Close()
				channel := createChannel(t, h, cookie, "primary", primary.URL, "responses")
				token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backup.URL, "responses")}, "priority")
				body := object{"model": "my-model", "stream": true, "messages": []any{object{"role": "user", "content": "hello"}}}
				if to == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
				}
				for request := range 2 {
					response := call(h, "POST", requestPath(to), jsonString(body), token)
					var log RequestLog
					if err := a.DB.Last(&log).Error; err != nil {
						t.Fatal(err)
					}
					if response.Code != 200 || log.Status != 200 || log.Attempts != 1 || log.ChannelID != channel.ID || log.FirstTokenMS == nil || log.InputTokens != 12 || log.OutputTokens != 7 || backupCalls.Load() != 0 {
						t.Fatalf("preface caused failure or failover: request=%d status=%d log=%+v backup_calls=%d", request, response.Code, log, backupCalls.Load())
					}
					if to == "responses" {
						scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
						scanner.Buffer(make([]byte, 4096), 4<<20)
						for i := range count {
							event, err := readEvent(scanner)
							value, _ := decodeJSON([]byte(event.Data))
							if err != nil || event.Event != "response.in_progress" || integer(obj(value)["sequence_number"]) != int64(i) {
								t.Fatalf("preface event %d was lost or reordered: %v", i, err)
							}
						}
					}
				}
				if primaryCalls.Load() != 2 {
					t.Fatal("next request did not retain the primary channel")
				}
			})
		}
	}
}

func TestGatewaySSELocalConversionFailureKeepsChannel(t *testing.T) {
	for _, test := range []struct{ name, protocol, bad string }{
		{"multiple choices", "openai", `{"choices":[{"index":1,"delta":{"content":"extra"}}]}`},
		{"reasoning", "openai", `{"choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`},
		{"thinking block", "anthropic", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"thinking"}}`},
		{"thinking delta", "anthropic", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking"}}`},
		{"native reasoning item", "responses", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"reasoning"}}`},
	} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed=%v", test.name, committed), func(t *testing.T) {
				a := testApp(t)
				h := a.Router()
				cookie := loginForTest(t, h)
				var primaryCalls, backupCalls atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					primaryCalls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					if committed {
						first := map[string]string{"openai": `{"choices":[{"index":0,"delta":{"content":"first"}}]}`, "anthropic": `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`, "responses": `{"type":"response.output_text.delta","delta":"first"}`}
						_ = writeFrame(w, "", first[test.protocol])
					}
					_ = writeFrame(w, "", test.bad)
					fmt.Fprint(w, streamFixture(test.protocol))
				}))
				defer primary.Close()
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupCalls.Add(1)
					fmt.Fprint(w, streamFixture(test.protocol))
				}))
				defer backup.Close()
				channel := createChannel(t, h, cookie, "native-primary", primary.URL, test.protocol)
				token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backup.URL, test.protocol)}, "priority")
				body := object{"model": "my-model", "stream": true, "messages": []any{object{"role": "user", "content": "hello"}}}
				to := "openai"
				if test.protocol == "openai" {
					to = "responses"
					delete(body, "messages")
					body["input"] = "hello"
				}
				response := call(h, "POST", requestPath(to), jsonString(body), token)
				var log RequestLog
				if err := a.DB.Last(&log).Error; err != nil {
					t.Fatal(err)
				}
				if log.Status != 500 || log.Attempts != 1 || backupCalls.Load() != 0 || !strings.Contains(response.Body.String(), "ledgergate_error") {
					t.Fatalf("local conversion was treated as upstream failure: log=%+v body=%s", log, response.Body.String())
				}
				if _, cooled := a.health[channel.ID]; cooled {
					t.Fatal("local conversion error cooled a healthy channel")
				}
				delete(body, "input")
				body["messages"] = []any{object{"role": "user", "content": "hello"}}
				if test.protocol == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
				}
				if test.protocol == "anthropic" {
					body["max_tokens"] = 100
				}
				response = call(h, "POST", requestPath(test.protocol), jsonString(body), token)
				log = RequestLog{}
				if err := a.DB.Last(&log).Error; err != nil {
					t.Fatal(err)
				}
				if response.Code != 200 || log.Status != 200 || log.ChannelID != channel.ID || primaryCalls.Load() != 2 || backupCalls.Load() != 0 {
					t.Fatalf("native request was diverted after a local conversion error: %+v", log)
				}
			})
		}
	}
}

func TestGatewaySSEPrefaceKeepsTimeoutAndNoReplay(t *testing.T) {
	for _, mode := range []string{"failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			if err := a.DB.Model(&Setting{}).Where("id=1").Update("first_output_seconds", 1).Error; err != nil {
				t.Fatal(err)
			}
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				for range 1280 {
					if err := writeFrame(w, "response.in_progress", `{"type":"response.in_progress"}`); err != nil {
						return
					}
				}
				if mode == "failure" {
					_ = writeFrame(w, "response.failed", `{"response":{"error":{"message":"unavailable"}}}`)
					return
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer primary.Close()
			var backupCalls atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				fmt.Fprint(w, streamFixture("responses"))
			}))
			defer backup.Close()
			channel := createChannel(t, h, cookie, "primary", primary.URL, "responses")
			token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backup.URL, "responses")}, "priority")
			response := call(h, "POST", "/v1/responses", `{"model":"my-model","stream":true,"input":"hello"}`, token)
			var log RequestLog
			if err := a.DB.Last(&log).Error; err != nil {
				t.Fatal(err)
			}
			message := "上游在流中报告错误"
			if mode == "timeout" {
				message = errFirstOutput.Error()
			}
			if log.Status != 502 || log.Attempts != 1 || log.FirstTokenMS != nil || backupCalls.Load() != 0 || !strings.Contains(response.Body.String(), message) {
				t.Fatalf("preface disabled protection or replayed output: %+v backup_calls=%d", log, backupCalls.Load())
			}
			var route Route
			if err := a.DB.Where("channel_id=?", channel.ID).First(&route).Error; err != nil {
				t.Fatal(err)
			}
			if _, cooled := a.routeHealth[route.ID]; !cooled {
				t.Fatal("real upstream failure did not cool the model route")
			}
		})
	}
}

func TestGatewaySSELocalSizeLimitKeepsChannel(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if primaryCalls.Add(1) == 1 {
			_ = writeFrame(w, "response.in_progress", jsonString(object{"type": "response.in_progress", "padding": strings.Repeat("x", 5<<20)}))
			return
		}
		fmt.Fprint(w, streamFixture("responses"))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		fmt.Fprint(w, streamFixture("responses"))
	}))
	defer backup.Close()
	channel := createChannel(t, h, cookie, "primary", primary.URL, "responses")
	token := createGroup(t, h, cookie, []Channel{channel, createChannel(t, h, cookie, "backup", backup.URL, "responses")}, "priority")
	for _, status := range []int{500, 200} {
		response := call(h, "POST", "/v1/responses", `{"model":"my-model","stream":true,"input":"hello"}`, token)
		var log RequestLog
		if err := a.DB.Last(&log).Error; err != nil {
			t.Fatal(err)
		}
		if response.Code != status || log.Status != status || log.Attempts != 1 || log.ChannelID != channel.ID || backupCalls.Load() != 0 {
			t.Fatalf("size limit caused failover or cooldown: %+v backup_calls=%d", log, backupCalls.Load())
		}
		if status == 500 && !strings.Contains(response.Body.String(), "ledgergate_error") {
			t.Fatal("local size limit was reported as an upstream error")
		}
	}
}
