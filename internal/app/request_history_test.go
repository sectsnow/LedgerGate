package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func historyDetailForTest(t *testing.T, h http.Handler, cookie string, id uint) (RequestLog, requestPayload) {
	t.Helper()
	w := call(h, "GET", fmt.Sprintf("/api/requests/%d", id), "", cookie)
	var value struct {
		Request RequestLog     `json:"request"`
		Detail  requestPayload `json:"detail"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil {
		t.Fatal("detail failed", w.Body.String())
	}
	return value.Request, value.Detail
}

func TestRequestHistoryLiveStreamAndMemoryDetail(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	started, first, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var firstOnce, finishOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-first:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"live\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":1}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "live-upstream", upstream.URL, "openai")}, "priority")
	gateway := httptest.NewServer(h)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		firstOnce.Do(func() { close(first) })
		finishOnce.Do(func() { close(finish) })
		gateway.Close()
		upstream.Close()
	}()
	req, _ := http.NewRequestWithContext(ctx, "POST", gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"my-model","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}],"metadata":{"api_key":"hidden-request-secret"}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	type result struct {
		response *http.Response
		err      error
	}
	response := make(chan result, 1)
	go func() { resp, err := http.DefaultClient.Do(req); response <- result{resp, err} }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not start")
	}
	row := latestRequestForTest(t, a)
	live, detail := historyDetailForTest(t, h, cookie, row.ID)
	if live.State != "running" || live.Status != 0 || live.Model != "my-model" || !detail.Available || !strings.Contains(detail.RequestBody, "[已隐藏]") || strings.Contains(detail.RequestBody, "hidden-request-secret") {
		t.Fatal("incorrect live request", live.State, detail.RequestBody)
	}
	stats := call(h, "GET", "/api/overview", "", cookie)
	var overview object
	if json.Unmarshal(stats.Body.Bytes(), &overview) != nil || integer(overview["errors"]) != 0 {
		t.Fatal("in-flight request counted as failure")
	}
	firstOnce.Do(func() { close(first) })
	var received result
	select {
	case received = <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("first stream frame did not arrive")
	}
	if received.err != nil {
		t.Fatal(received.err)
	}
	defer received.response.Body.Close()
	if _, err := bufio.NewReader(received.response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	live, detail = historyDetailForTest(t, h, cookie, row.ID)
	if live.State != "streaming" || live.FirstByteMS == nil || live.InputTokens != 17 || !strings.Contains(detail.ResponseBody, "hello") || len(detail.Attempts) != 1 {
		t.Fatal("live response was not updated", live, detail)
	}
	for range 10 {
		historyDetailForTest(t, h, cookie, row.ID)
	}
	finishOnce.Do(func() { close(finish) })
	if _, err := io.ReadAll(received.response.Body); err != nil {
		t.Fatal(err)
	}
	final, detail := historyDetailForTest(t, h, cookie, row.ID)
	if final.State != "completed" || final.Status != 200 || final.OutputTokens != 3 || final.FinishedAt == nil || detail.Attempts[0].Status != 200 {
		t.Fatal("stream completion was not finalized", final, detail)
	}
	for _, table := range []any{&RequestLog{}, &RequestDetail{}} {
		var count int64
		if err := a.DB.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("request or contents persisted", count, err)
		}
	}
}

func TestRequestHistoryPaginationFiltersAndEviction(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	rows := make([]RequestLog, 65)
	for i := range rows {
		rows[i] = RequestLog{RequestID: fmt.Sprintf("req-history-%03d", i), Model: fmt.Sprintf("model-%03d", i), Status: 200}
		if i%10 == 0 {
			rows[i].Status = 502
		}
		trace := a.beginRequestTrace(&rows[i])
		a.finishRequestTrace(trace, &rows[i])
	}
	for _, path := range []string{"/api/requests?page=1", "/api/requests/1"} {
		if w := call(h, "GET", path, "", ""); w.Code != 401 {
			t.Fatal("unauthenticated history exposed")
		}
	}
	var recent []RequestLog
	w := call(h, "GET", "/api/requests", "", cookie)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &recent) != nil || len(recent) != 50 {
		t.Fatal("dashboard cache response incorrect", w.Body.String())
	}
	var page struct {
		Items []RequestLog
		Total int64
	}
	w = call(h, "GET", "/api/requests?page=2&page_size=30", "", cookie)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 50 || len(page.Items) != 20 || page.Items[0].ID != rows[34].ID || page.Items[19].ID != rows[15].ID {
		t.Fatal("cache pagination incorrect", w.Body.String())
	}
	w = call(h, "GET", "/api/requests?page=1&q=req-history-020&state=completed", "", cookie)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 0 {
		t.Fatal("combined filters incorrect")
	}
	w = call(h, "GET", "/api/requests?page=1&q=REQ-HISTORY-020&state=failed", "", cookie)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 1 {
		t.Fatal("case-insensitive search incorrect")
	}
	for _, query := range []string{"page=0", "page=-1", "page=abc", "page_size=201", "page_size=0", "state=invalid"} {
		if w := call(h, "GET", "/api/requests?"+query, "", cookie); w.Code != 400 {
			t.Fatal("invalid query accepted", query)
		}
	}
	if w := call(h, "GET", fmt.Sprintf("/api/requests/%d", rows[0].ID), "", cookie); w.Code != 404 || !strings.Contains(w.Body.String(), "内存缓存") {
		t.Fatal("eviction not explained", w.Body.String())
	}
	for _, path := range []string{"/api/requests/clear", "/api/requests/clear/preview"} {
		if w := call(h, "POST", path, `{}`, cookie); w.Code != 404 {
			t.Fatal("obsolete clear API remains", w.Code)
		}
	}
}

func TestRequestHistoryListsAllCachedRequestsWithoutPaging(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	a.mu.Lock()
	for id := uint(1); id <= 50; id++ {
		a.cacheFinishedRequest(RequestLog{ID: id, RequestID: fmt.Sprintf("finished-%d", id), Model: "shared-model", State: "completed", Status: 200}, requestPayload{})
	}
	a.mu.Unlock()
	for i := range 51 {
		log := RequestLog{RequestID: fmt.Sprintf("active-%d", i), Model: "shared-model"}
		a.beginRequestTrace(&log)
	}
	response := call(h, "GET", "/api/requests", "", cookie)
	var all []RequestLog
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &all) != nil || len(all) != 101 {
		t.Fatal("unpaged request list omitted cached records", response.Body.String())
	}
	for _, test := range []struct {
		query string
		want  int
	}{
		{"q=&state=", 101},
		{"q=SHARED-MODEL&state=", 101},
		{"q=&state=running", 51},
		{"q=&state=completed", 50},
	} {
		response := call(h, "GET", "/api/requests?"+test.query, "", cookie)
		var result struct {
			Items  []RequestLog `json:"items"`
			Total  int          `json:"total"`
			Active int          `json:"active"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Items) != test.want || result.Total != test.want || result.Active != 51 {
			t.Fatalf("request list truncated or filters changed: %s: %s", test.query, response.Body.String())
		}
	}
}

func TestRequestHistoryCancellationAndAttempts(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer failure.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"cancel\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "failure", failure.URL, "openai"), createChannel(t, h, cookie, "working", upstream.URL, "openai")}, "priority")
	gateway := httptest.NewServer(h)
	defer gateway.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"my-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	var row RequestLog
	for i := 0; i < 100; i++ {
		row = latestRequestForTest(t, a)
		if row.State == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if row.State != "cancelled" || row.Status != 499 {
		t.Fatal("cancelled request remained running", row.State, row.Status)
	}
	_, detail := historyDetailForTest(t, h, cookie, row.ID)
	if len(detail.Attempts) != 3 || detail.Attempts[0].Status != 503 || detail.Attempts[1].Status != 503 || detail.Attempts[2].Status != 499 {
		t.Fatal("retry or cancellation history lost", detail.Attempts)
	}
}

func TestRequestHistoryRestartAndBodyBounds(t *testing.T) {
	dir := t.TempDir()
	config := Config{DataDir: dir, AdminPassword: "test-password-12345"}
	a, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	finished := RequestLog{RequestID: "finished", Status: 200, InputTokens: 100, CostMicros: 123}
	trace := a.beginRequestTrace(&finished)
	trace.setRequest(finished, object{"input": "memory-only"})
	a.finishRequestTrace(trace, &finished)
	ongoing := RequestLog{RequestID: "ongoing"}
	a.beginRequestTrace(&ongoing)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rows, active := a.requestSummaries()
	if len(rows) != 0 || active != 0 {
		t.Fatal("request cache survived restart")
	}
	if _, _, found := a.cachedRequestDetail(finished.ID); found {
		t.Fatal("request body survived restart")
	}
	var day RequestDay
	if err := a.DB.First(&day).Error; err != nil || day.Requests != 1 || day.InputTokens != 100 || day.CostMicros != 123 {
		t.Fatal("restart lost usage", day, err)
	}
	body, truncated := boundedLogJSON(object{"api_key": "never-expose", "nested": []any{object{"refresh_token": "never-expose"}}, "text": strings.Repeat("x", requestBodyLimit*2)})
	if !truncated || len(body) > requestBodyLimit || strings.Contains(body, "never-expose") {
		t.Fatal("request body bounds or redaction failed")
	}
	trace = &requestTrace{log: RequestLog{CreatedAt: time.Now(), Stream: true}}
	trace.appendResponse([]byte(strings.Repeat("z", requestBodyLimit*2)))
	if len(trace.response) > requestBodyLimit || !trace.payload().ResponseTruncated {
		t.Fatal("response capture is unbounded")
	}
}
