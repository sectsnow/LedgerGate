package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestResponseTimingsSeparateSemanticAndContent(t *testing.T) {
	for _, test := range []struct {
		name, event, data string
		semantic, content bool
	}{
		{"created", "response.created", `{"response":{"output":[]}}`, false, false},
		{"progress", "", `{"type":"response.in_progress"}`, false, false},
		{"metadata", "", `{"type":"response.metadata"}`, false, false},
		{"keepalive", "", `{"type":"keepalive"}`, false, false},
		{"empty reasoning", "response.output_item.added", `{"item":{"type":"reasoning","summary":[]}}`, true, false},
		{"encrypted reasoning", "", `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"opaque"}}`, true, false},
		{"text", "", `{"type":"response.output_text.delta","delta":"hello"}`, true, true},
		{"custom tool", "", `{"type":"response.custom_tool_call_input.delta","delta":"command"}`, true, true},
		{"completed snapshot", "", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`, true, true},
		{"usage only", "", `{"type":"response.completed","response":{"usage":{"output_tokens":100}}}`, true, false},
		{"failed", "", `{"type":"response.failed"}`, false, false},
		{"done marker", "", `[DONE]`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := sseEvent{Event: test.event, Data: test.data}
			if got := streamEventStartsFirstToken(event, "responses"); got != test.semantic {
				t.Fatalf("semantic first word=%v, want=%v", got, test.semantic)
			}
			if got := streamEventHasOutput(event, "responses"); got != test.content {
				t.Fatalf("visible content=%v, want=%v", got, test.content)
			}
		})
	}
	started := time.Now().Add(-173 * time.Second)
	timing := requestTimings{Started: started, Finished: started.Add(172600 * time.Millisecond)}
	timing.observe(sseEvent{Data: `{"type":"response.created"}`}, "responses", started.Add(time.Second))
	timing.observe(sseEvent{Data: `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`}, "responses", started.Add(16730*time.Millisecond))
	timing.observe(sseEvent{Data: `{"type":"response.output_text.delta","delta":"hello"}`}, "responses", started.Add(164340*time.Millisecond))
	var log RequestLog
	timing.apply(&log)
	if log.FirstTokenMS == nil || *log.FirstTokenMS != 16730 || log.FirstContentMS == nil || *log.FirstContentMS != 164340 || log.GenerationMS == nil || *log.GenerationMS != 8260 {
		t.Fatalf("reasoning time mixed with content or generation: %+v", log)
	}
}

type timingFlushWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *timingFlushWriter) Flush() { _ = w.FlushError() }

func (w *timingFlushWriter) FlushError() error {
	w.once.Do(func() { close(w.entered); <-w.release })
	w.ResponseRecorder.Flush()
	return nil
}

func TestResponseTimingsRecordedBeforeClientFlush(t *testing.T) {
	reader, upstream := io.Pipe()
	defer reader.Close()
	defer upstream.Close()
	blocked := &timingFlushWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(blocked.release) })
	c, _ := gin.CreateTestContext(blocked)
	started := time.Now()
	trace := &requestTrace{log: RequestLog{MetricsVersion: 2, Stream: true, State: "running", CreatedAt: started}}
	c.Set("request_trace", trace)
	c.Writer = &requestCaptureWriter{ResponseWriter: c.Writer, trace: trace, ctx: context.Background()}
	timing := requestTimings{Started: started}
	done := make(chan error, 1)
	go func() {
		_, err := (&App{}).pipeStream(c, &http.Response{Body: reader}, "responses", "responses", "test", true, &timing)
		done <- err
	}()
	if _, err := io.WriteString(upstream, "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"summary\":[]}}\n\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for trace.summary().FirstTokenMS == nil {
		select {
		case <-deadline:
			t.Fatal("semantic first word not recorded while content was buffered")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if trace.summary().FirstContentMS != nil {
		t.Fatal("empty reasoning counted as visible content")
	}
	if _, err := io.WriteString(upstream, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("downstream flush never started")
	}
	if trace.summary().FirstContentMS == nil {
		t.Fatal("content time incorrectly waits for downstream flush")
	}
	beforeRelease := time.Now()
	release.Do(func() { close(blocked.release) })
	_, _ = io.WriteString(upstream, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream failed to complete")
	}
	if timing.FirstContent.After(beforeRelease) || timing.First.After(timing.FirstContent) {
		t.Fatal("downstream delay changed upstream content timing")
	}
}

func TestResponseTimingsRetryDiscardsFailedAttempt(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"summary\":[]}}\n\n")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"temporary\"}}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "timing", upstream.URL, "responses")
	token := createGroup(t, h, cookie, []Channel{channel}, "priority")
	routingGroupForTest(t, a, &GroupRelayConfig{2, 1, 60, 300})
	response := call(h, "POST", "/v1/responses", `{"model":"my-model","input":"hello","stream":true}`, token)
	log := latestRequestForTest(t, a)
	if response.Code != 200 || log.Status != 200 || calls.Load() != 2 || log.FirstTokenMS == nil || *log.FirstTokenMS < 1000 || log.FirstContentMS == nil || *log.FirstContentMS < *log.FirstTokenMS {
		t.Fatalf("failed attempt polluted final timings or changed replay policy: %+v body=%s", log, response.Body.String())
	}
}

func TestResponseTimingsModelTestUsesBothClocks(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"summary\":[]}}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n")
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "timing model test", upstream.URL, "responses")
	result := runModelTest(t, h, cookie, channel, object{"model": "test-model", "protocol": "responses", "prompt": "OK?", "max_tokens": 512, "stream": true})
	if !result.Success || result.FirstTokenMS == nil || result.FirstContentMS == nil || *result.FirstContentMS-*result.FirstTokenMS < 50 || result.OutputTokens == nil || *result.OutputTokens != 5 {
		t.Fatalf("model test lost semantic or completed snapshot timing: %+v", result)
	}
}
