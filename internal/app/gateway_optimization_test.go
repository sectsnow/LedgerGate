package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func admissionContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	return c, w
}
func TestKeyPeriodsAdmissionAndSafeLimits(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 12, 31, 17, 0, 0, 0, time.UTC)
	key := APIKey{Name: "limits", Enabled: true, DailyBudgetMicros: intPointer(50), MonthlyBudgetMicros: intPointer(100), MaxConcurrent: 1}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []KeyUsageDay{{KeyID: key.ID, Date: "2026-12-31", CostMicros: 90}, {KeyID: key.ID, Date: "2027-01-01", CostMicros: 40}} {
		if err := a.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	day, month, err := keyUsage(a.DB, key.ID, now)
	if err != nil || day != 40 || month != 40 {
		t.Fatal("Beijing month boundary wrong", day, month, err)
	}
	keys := []APIKey{key}
	if err := a.populateKeyUsage(keys, now); err != nil || keys[0].MonthlyResetAt.Format("2006-01-02") != "2027-02-01" {
		t.Fatal("reset date wrong", keys, err)
	}
	daily := KeyUsageDay{KeyID: key.ID, Date: usageDay(time.Now()).Format("2006-01-02"), CostMicros: 50}
	a.DB.Create(&daily)
	c, w := admissionContext()
	if a.admitKeyRequest(c, key) || w.Code != 429 || !strings.Contains(w.Body.String(), "今日预算") || w.Header().Get("Retry-After") == "" {
		t.Fatal("daily budget not enforced", w.Body.String())
	}
	key.DailyBudgetMicros = nil
	key.MonthlyBudgetMicros = intPointer(0)
	c, w = admissionContext()
	if a.admitKeyRequest(c, key) || !strings.Contains(w.Body.String(), "本月预算") {
		t.Fatal("zero monthly budget not enforced")
	}
	key.MonthlyBudgetMicros = nil
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := admissionContext()
			if a.admitKeyRequest(c, key) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("concurrency reservation raced", accepted.Load())
	}
	a.releaseKeyRequest(key.ID)
	key.RequestsPerMinute = 1
	c, _ = admissionContext()
	if !a.admitKeyRequest(c, key) {
		t.Fatal("request never admitted after release")
	}
	a.releaseKeyRequest(key.ID)
	c, w = admissionContext()
	if a.admitKeyRequest(c, key) || !strings.Contains(w.Body.String(), "每分钟") {
		t.Fatal("rpm limit not enforced")
	}
	a.keyTrafficMu.Lock()
	a.keyTraffic[key.ID].Accepted[0] = time.Now().Add(-time.Minute - time.Second)
	a.keyTrafficMu.Unlock()
	c, _ = admissionContext()
	if !a.admitKeyRequest(c, key) {
		t.Fatal("expired rate window did not recover")
	}
	a.releaseKeyRequest(key.ID)
	for _, input := range []keyInput{{Name: "x", DailyBudgetMicros: intPointer(-1)}, {Name: "x", MonthlyBudgetMicros: intPointer(9_007_199_254_740_992)}, {Name: "x", MaxConcurrent: 1001}, {Name: "x", RequestsPerMinute: 10001}} {
		if input.validate() == nil {
			t.Fatal("invalid limit accepted", input)
		}
	}
}
func TestRequestSummaryRestartPrivacyBoundsAndKeyUsage(t *testing.T) {
	config := Config{DataDir: t.TempDir(), AdminPassword: "test-password-12345"}
	a, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Setting{}).Where("id = 1").Update("request_digest_limit", 2).Error; err != nil {
		t.Fatal(err)
	}
	key := APIKey{Name: "history", Enabled: true}
	a.DB.Create(&key)
	var last uint
	for i := range 3 {
		log := RequestLog{RequestID: fmt.Sprint("req_", i), Model: "model", KeyID: key.ID, KeyName: key.Name, ChannelID: 1, ChannelName: "channel", Status: 200, Attempts: 2, MetricsVersion: 2, UsageRecorded: true, CostMicros: 7, CostStatus: "known", InputTokens: 10, OutputTokens: 2, Error: "private-secret", CostWarning: "private-secret", ReasoningEffort: "private-secret"}
		if i == 2 {
			log.CostStatus = "unknown"
			log.CostMicros = 99
		}
		trace := a.beginRequestTrace(&log)
		trace.setRequest(log, object{"input": "private-secret"})
		trace.appendResponse([]byte(`{"output":"private-secret"}`))
		trace.attempts = []requestAttempt{{Status: 503, Error: "private-secret", Reason: "private-secret", StartedAt: log.CreatedAt}, {Status: 200, StartedAt: log.CreatedAt.Add(time.Second)}}
		if err := a.finishRequestTrace(trace, &log); err != nil {
			t.Fatal(err)
		}
		last = log.ID
	}
	var count int64
	a.DB.Model(&RequestSummary{}).Count(&count)
	if count != 2 {
		t.Fatal("summary limit failed", count)
	}
	var stored []RequestSummary
	a.DB.Find(&stored)
	for _, row := range stored {
		if strings.Contains(row.Data, "private-secret") {
			t.Fatal("body/error leaked into durable summary", row.Data)
		}
	}
	var usage KeyUsageDay
	if err := a.DB.First(&usage).Error; err != nil || usage.CostMicros != 14 || usage.Requests != 3 || usage.UnknownCosts != 1 {
		t.Fatal("summary eviction changed key totals", usage, err)
	}
	if err := a.DB.First(&key, key.ID).Error; err != nil || key.UsedMicros != 14 {
		t.Fatal("unknown fee debited cumulative budget", key.UsedMicros, err)
	}
	a.DB.Model(&RequestLog{}).Count(&count)
	if count != 0 {
		t.Fatal("body table was repopulated")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.nextRequestID < last {
		t.Fatal("request ids can collide after restart")
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	if w := call(h, "GET", "/api/request-summaries", "", ""); w.Code != 401 {
		t.Fatal("summary endpoint is public")
	}
	w := call(h, "GET", "/api/request-summaries?q=req_&page=1", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatal(w.Body.String())
	}
	w = call(h, "GET", fmt.Sprint("/api/request-summaries/", last), "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"summary_only":true`) {
		t.Fatal(w.Body.String())
	}
	if w := call(h, "GET", fmt.Sprint("/api/requests/", last), "", cookie); w.Code != 404 {
		t.Fatal("body survived restart")
	}
	w = call(h, "GET", "/api/request-quality?group_by=channel&model=model", "", cookie)
	var result struct{ Items []qualityRow }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 1 || result.Items[0].RetriedSuccesses != 3 || result.Items[0].AttemptOverheadMS != 3000 || result.Items[0].LastRequestUnix == 0 {
		t.Fatal("quality missing new observations", w.Body.String())
	}
	w = call(h, "GET", "/api/request-quality?model=missing", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("model filter ignored", w.Body.String())
	}
	if err := a.cleanupHistory(context.Background(), time.Now().AddDate(0, 0, 8)); err != nil {
		t.Fatal(err)
	}
	a.DB.Model(&RequestSummary{}).Count(&count)
	if count != 0 {
		t.Fatal("expired summaries retained")
	}
}
func TestCollectionResultsPreserveSuccessAndPrivacy(t *testing.T) {
	a := testApp(t)
	account := Account{Name: "parts", Kind: "sub2api", Revision: 1}
	a.DB.Create(&account)
	rows := collectionState(account)
	updateCollection(rows, "today", "ok", "已采集")
	first := *rows["today"].SucceededAt
	updateCollection(rows, "today", "failed", "接口失败，沿用旧数据")
	if !rows["today"].SucceededAt.Equal(first) || rows["today"].Status != "failed" {
		t.Fatal("failed attempt advanced success time")
	}
	a.DB.Model(&account).Update("collection_state", jsonString(rows))
	a.priceCollectionOutcome(account, "failed", "报价更新失败，保留旧报价，下次采集重试", true)
	cookie := loginForTest(t, a.Router())
	w := call(a.Router(), "GET", fmt.Sprint("/api/accounts/", account.ID, "/collection"), "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "报价更新失败") || !strings.Contains(w.Body.String(), "today") || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatal("collection view missing outcomes or leaked account", w.Body.String())
	}
	if w := call(a.Router(), "GET", fmt.Sprint("/api/accounts/", account.ID, "/collection"), "", ""); w.Code != 401 {
		t.Fatal("collection endpoint is public")
	}
	for range 102 {
		if err := appendCollectionEvent(a.DB, account.ID, "rates", "ok", "分组变化"); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	a.DB.Model(&CollectionEvent{}).Count(&count)
	if count != 100 {
		t.Fatal("event storage not bounded", count)
	}
}

func TestGatewayKeyControlsIntegration(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		chatResponse(w)
	}))
	defer func() { unblock(); server.Close() }()
	channel := createChannel(t, h, cookie, "limited", server.URL, "openai")
	token := createGroup(t, h, cookie, []Channel{channel}, "priority")
	input := keyInput{Name: "limited", DailyBudgetMicros: intPointer(1000000), MonthlyBudgetMicros: intPointer(10000000), MaxConcurrent: 1, RequestsPerMinute: 1}
	if w := call(h, "PUT", "/api/keys/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completed <- call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, token)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request not forwarded")
	}
	w := call(h, "POST", "/v1/chat/completions", `{"model":"my-model"}`, token)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "并发") {
		unblock()
		t.Fatal("concurrency middleware bypassed", w.Body.String())
	}
	unblock()
	select {
	case w = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(h, "POST", "/v1/chat/completions", `{"model":"my-model"}`, token)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "每分钟") {
		t.Fatal("rpm middleware bypassed", w.Body.String())
	}
	input.RequestsPerMinute = 0
	input.DailyBudgetMicros = intPointer(0)
	if w := call(h, "PUT", "/api/keys/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(h, "POST", "/v1/chat/completions", `{"model":"my-model"}`, token)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "今日预算") {
		t.Fatal("daily middleware bypassed", w.Body.String())
	}
	input.DailyBudgetMicros = nil
	input.MonthlyBudgetMicros = intPointer(0)
	if w := call(h, "PUT", "/api/keys/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(h, "POST", "/v1/chat/completions", `{"model":"my-model"}`, token)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "本月预算") {
		t.Fatal("monthly middleware bypassed", w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("rejected request reached upstream", calls.Load())
	}
	w = call(h, "GET", "/api/keys/1/usage", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"requests":1`) {
		t.Fatal(w.Body.String())
	}
	var key APIKey
	if err := a.DB.First(&key, 1).Error; err != nil {
		t.Fatal(err)
	}
	keys := []APIKey{key}
	if err := a.populateKeyUsage(keys, time.Now()); err != nil || keys[0].ActiveRequests != 0 || keys[0].DailyUsedMicros != 600 {
		t.Fatal("stream slot or accounting not finalized", keys, err)
	}
}

func TestGatewayControlsBackupRoundTrip(t *testing.T) {
	source := testApp(t)
	account := Account{Name: "backup-state", Kind: "sub2api", CollectionState: jsonString(map[string]collectionResult{"today": {Status: "failed", Message: "沿用旧值"}})}
	if err := source.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	key := APIKey{Name: "backup-periods", DailyBudgetMicros: intPointer(100), MaxConcurrent: 2, RequestsPerMinute: 10}
	if err := source.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	log := RequestLog{RequestID: "backup-summary", KeyID: key.ID, Model: "m", Status: 200, CostStatus: "known", CostMicros: 9, Attempts: 1}
	trace := source.beginRequestTrace(&log)
	if err := source.finishRequestTrace(trace, &log); err != nil {
		t.Fatal(err)
	}
	if err := appendCollectionEvent(source.DB, account.ID, "rates", "ok", "新快照"); err != nil {
		t.Fatal(err)
	}
	router := source.Router()
	response := call(router, "POST", "/api/backup", `{"password":"backup-password-123"}`, loginForTest(t, router))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	target := testApp(t)
	router = target.Router()
	cookie := loginForTest(t, router)
	restored := restoreUploadForTest(t, router, cookie, response.Body.Bytes(), restoreTestPassword, "backup.lgb", "恢复", true)
	if restored.Code != 200 {
		t.Fatal("restore failed", restored.Body.String())
	}
	var saved APIKey
	if err := target.DB.First(&saved, key.ID).Error; err != nil || saved.MaxConcurrent != 2 || saved.RequestsPerMinute != 10 || saved.DailyBudgetMicros == nil || *saved.DailyBudgetMicros != 100 {
		t.Fatal("key controls lost", saved, err)
	}
	keys := []APIKey{saved}
	if err := target.populateKeyUsage(keys, time.Now()); err != nil || keys[0].DailyUsedMicros != 9 {
		t.Fatal("period ledger lost", keys, err)
	}
	for _, model := range []any{&RequestSummary{}, &CollectionEvent{}} {
		var count int64
		if err := target.DB.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("persistent metadata lost", count, err)
		}
	}
	var current Account
	if err := target.DB.First(&current, account.ID).Error; err != nil || collectionState(current)["today"].Status != "failed" {
		t.Fatal("per-part status lost", current, err)
	}
}

func TestSummaryWriteFailurePreservesAccounting(t *testing.T) {
	a := testApp(t)
	key := APIKey{Name: "safe-accounting"}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Exec("CREATE TRIGGER reject_summary BEFORE INSERT ON request_summaries BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	log := RequestLog{RequestID: "write-failure", KeyID: key.ID, Status: 200, CostStatus: "known", CostMicros: 25}
	trace := a.beginRequestTrace(&log)
	if err := a.finishRequestTrace(trace, &log); err != nil {
		t.Fatal("optional diagnostics broke accounting", err)
	}
	var usage KeyUsageDay
	if err := a.DB.First(&usage).Error; err != nil || usage.CostMicros != 25 || usage.Requests != 1 {
		t.Fatal("summary failure rolled back usage", usage, err)
	}
	if err := a.DB.First(&key, key.ID).Error; err != nil || key.UsedMicros != 25 {
		t.Fatal("summary failure rolled back budget", key, err)
	}
	if _, _, found := a.cachedRequestDetail(log.ID); !found {
		t.Fatal("summary failure lost live detail")
	}
}
