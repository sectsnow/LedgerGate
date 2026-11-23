package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type completionCancelWriter struct {
	*httptest.ResponseRecorder
	cancel          context.CancelFunc
	terminal        string
	stage           string
	withoutCancel   bool
	finished        bool
	terminalFlushed bool
}

func (w *completionCancelWriter) Write(data []byte) (int, error) {
	terminal := strings.Contains(string(data), w.terminal)
	if terminal && w.stage == "write_failure" {
		if !w.withoutCancel {
			w.cancel()
		}
		return 0, io.ErrClosedPipe
	}
	n, err := w.ResponseRecorder.Write(data)
	if terminal {
		w.finished = true
		if w.stage == "after_json_completion" {
			w.cancel()
		}
	}
	return n, err
}

func (w *completionCancelWriter) Flush() { _ = w.FlushError() }

func (w *completionCancelWriter) FlushError() error {
	if w.terminalFlushed {
		return io.ErrClosedPipe
	}
	w.ResponseRecorder.Flush()
	if !w.withoutCancel && (w.finished || w.stage == "before_completion") {
		w.cancel()
	}
	if w.finished && w.stage == "flush_failure" {
		return io.ErrClosedPipe
	}
	w.terminalFlushed = w.finished
	return nil
}

func TestGatewayStreamCompletionCancellation(t *testing.T) {
	terminal := map[string]string{"openai": "[DONE]", "anthropic": `"type":"message_stop"`, "responses": `"type":"response.completed"`}
	cases := []struct {
		name          string
		stage         string
		withoutCancel bool
	}{
		{"after_completion", "after_completion", false},
		{"before_completion", "before_completion", false},
		{"write_failure", "write_failure", false},
		{"flush_failure", "flush_failure", false},
		{"write_failure_before_context_cancellation", "write_failure", true},
		{"flush_failure_before_context_cancellation", "flush_failure", true},
	}
	for _, from := range []string{"openai", "anthropic", "responses"} {
		t.Run(from, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, streamFixture(from))
			}))
			defer upstream.Close()
			token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "completion-upstream", upstream.URL, from)}, "priority")
			for _, to := range []string{"openai", "anthropic", "responses"} {
				for _, test := range cases {
					t.Run(to+"/"+test.name, func(t *testing.T) {
						body := object{"model": "my-model", "stream": true, "messages": []any{object{"role": "user", "content": "hello"}}}
						if to == "responses" {
							delete(body, "messages")
							body["input"] = "hello"
						}
						if to == "anthropic" {
							body["max_tokens"] = 100
						}
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						req := httptest.NewRequest("POST", requestPath(to), strings.NewReader(jsonString(body))).WithContext(ctx)
						req.Header.Set("Authorization", "Bearer "+token)
						req.Header.Set("Content-Type", "application/json")
						writer := &completionCancelWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, terminal: terminal[to], stage: test.stage, withoutCancel: test.withoutCancel}
						h.ServeHTTP(writer, req)
						if (ctx.Err() == nil) != test.withoutCancel {
							t.Fatal("client context cancellation did not match the scenario")
						}
						log := latestRequestForTest(t, a)
						log, detail := historyDetailForTest(t, h, cookie, log.ID)
						status, state := 499, "cancelled"
						if test.stage == "after_completion" {
							status, state = 200, "completed"
							if !writer.finished || log.Error != "" || log.InputTokens != 12 || log.OutputTokens != 7 {
								t.Fatalf("completed response lost its result or usage: %+v", log)
							}
						}
						if log.Status != status || log.State != state || len(detail.Attempts) != 1 || detail.Attempts[0].Status != status {
							t.Fatalf("want %d/%s, got %d/%s with attempts %+v", status, state, log.Status, log.State, detail.Attempts)
						}
						if test.stage == "before_completion" && writer.finished {
							t.Fatal("terminal event was sent after an earlier cancellation")
						}
						if len(a.health) != 0 || len(a.routeHealth) != 0 {
							t.Fatal("client disconnect cooled a working upstream")
						}
					})
				}
			}
		})
	}
}

func TestGatewayJSONCompletionCancellation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatResponse(w) }))
	defer upstream.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "json-upstream", upstream.URL, "openai")}, "priority")
	for _, to := range []string{"openai", "anthropic", "responses"} {
		for _, stage := range []string{"after_json_completion", "write_failure"} {
			t.Run(to+"/"+stage, func(t *testing.T) {
				body := object{"model": "my-model", "messages": []any{object{"role": "user", "content": "hello"}}}
				if to == "responses" {
					delete(body, "messages")
					body["input"] = "hello"
				}
				if to == "anthropic" {
					body["max_tokens"] = 100
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				req := httptest.NewRequest("POST", requestPath(to), strings.NewReader(jsonString(body))).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				writer := &completionCancelWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, terminal: `"model":`, stage: stage}
				h.ServeHTTP(writer, req)
				if ctx.Err() == nil {
					t.Fatal("client cancellation was not triggered")
				}
				log := latestRequestForTest(t, a)
				log, detail := historyDetailForTest(t, h, cookie, log.ID)
				status, state := 499, "cancelled"
				if stage == "after_json_completion" {
					status, state = 200, "completed"
				}
				if log.Status != status || log.State != state || len(detail.Attempts) != 1 || detail.Attempts[0].Status != status || log.InputTokens != 100 || log.OutputTokens != 50 {
					t.Fatalf("want %d/%s with usage, got %+v and attempts %+v", status, state, log, detail.Attempts)
				}
			})
		}
	}
}
