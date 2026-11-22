package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateChangeDetails(t *testing.T) {
	before := rateSnapshot{Groups: []upstreamRate{
		{GroupID: "up", Group: "上涨", Multiplier: floatPointer(0.35)},
		{GroupID: "down", Group: "下降", Multiplier: floatPointer(0.9)},
		{GroupID: "removed", Group: "移除", Multiplier: floatPointer(1)},
		{GroupID: "zero", Group: "免费", Multiplier: floatPointer(0)},
		{GroupID: "unknown", Group: "未知"},
	}}
	after := rateSnapshot{Groups: []upstreamRate{
		{GroupID: "up", Group: "上涨", Multiplier: floatPointer(0.4)},
		{GroupID: "down", Group: "下降", Multiplier: floatPointer(0.75)},
		{GroupID: "added", Group: "新增", Multiplier: floatPointer(0)},
		{GroupID: "zero", Group: "免费", Multiplier: floatPointer(0.1)},
		{GroupID: "unknown", Group: "未知", Multiplier: floatPointer(1)},
	}}
	changes := changedRates(jsonString(before), jsonString(after))
	if len(changes) != 5 || changes[0].Kind != "added" || changes[1].Percent == nil || *changes[1].Percent >= -16.6 || changes[2].Kind != "removed" || changes[4].Percent != nil {
		t.Fatalf("incorrect numeric changes: %+v", changes)
	}
	if len(changedRates("", jsonString(after))) != 0 || len(changedRates(`{"legacy":1}`, jsonString(after))) != 0 {
		t.Fatal("initial collection or legacy migration must not invent changes")
	}
	before.Groups[0].Group = "只修改名称"
	if len(changedRates(jsonString(before), jsonString(before))) != 0 {
		t.Fatal("unchanged multipliers must not emit changes")
	}
	item := describeAlert(Alert{Kind: "rates_changed", Detail: rateChangesDetail(changes)})
	if len(item.Changes) != 5 || !strings.Contains(item.Detail, "0.9× → 0.75×") || strings.Contains(item.Detail, `"rate_changes"`) {
		t.Fatal("API and webhook must expose readable details with structured changes")
	}
}

func TestMonitorActivityRecordsEventsDuringNotificationCooldown(t *testing.T) {
	a := testApp(t)
	if err := a.DB.Create(&NotificationChannel{Name: "通知", Type: "webhook", Enabled: true, URL: a.seal("https://notifications.invalid"), Retries: 10}).Error; err != nil {
		t.Fatal(err)
	}
	var step atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			fmt.Fprint(w, `{"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/log/self/stat":
			fmt.Fprint(w, `{"data":{"quota":0}}`)
		case "/api/user/self/groups":
			fmt.Fprintf(w, `{"data":{"default":{"ratio":%g}}}`, 1+float64(step.Load())/10)
		case "/api/notice":
			if step.Load() == 3 {
				w.WriteHeader(503)
			} else {
				fmt.Fprintf(w, `{"data":%q}`, fmt.Sprintf("公告 %d", min(step.Load(), 2)))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	account := Account{Name: "采集测试", Kind: "newapi", BaseURL: upstream.URL, AuthMode: "token", Token: a.seal("account-token"), QuotaPerUnit: 500000, IntervalSeconds: 300}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		step.Store(int32(index))
		if _, err := a.checkAccount(context.Background(), account.ID); err != nil {
			t.Fatal(err)
		}
	}
	var events []Alert
	if err := a.DB.Where("kind = ?", "rates_changed").Order("id").Find(&events).Error; err != nil || len(events) != 2 {
		t.Fatalf("separate rate changes lost within cooldown: %+v %v", events, err)
	}
	if events[0].Delivery != "pending" || events[1].Delivery != "cooldown" || len(describeAlert(events[1]).Changes) != 1 {
		t.Fatal("notification cooldown must preserve local rate detail without a second push")
	}
	var count int64
	a.DB.Model(&Alert{}).Where("kind = ?", "announcement").Count(&count)
	if count != 2 {
		t.Fatal("announcement changes were suppressed by notification cooldown")
	}
	step.Store(3)
	checked, err := a.checkAccount(context.Background(), account.ID)
	if err != nil || checked.Status != "ok" || !strings.Contains(checked.Warnings, "公告采集失败") || checked.Announcements != `"公告 2"` {
		t.Fatalf("optional failure lost last usable announcement: %+v %v", checked, err)
	}
	a.DB.Model(&Alert{}).Where("kind = ?", "monitor_warning").Count(&count)
	if count != 1 {
		t.Fatal("partial collection failure missing from alert history")
	}
	step.Store(2)
	if _, err := a.checkAccount(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	a.DB.Model(&Alert{}).Where("kind = ?", "monitor_warning_recovered").Count(&count)
	if count != 1 {
		t.Fatal("partial collection recovery missing from alert history")
	}
}

func TestAnnouncementPaginationAndSemanticChanges(t *testing.T) {
	a := testApp(t)
	var failPage atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			if failPage.Load() {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"data":{"items":[{"id":3,"title":"第三条","content":"完整正文"}],"total":3}}`)
		} else {
			fmt.Fprint(w, `{"data":{"items":[{"id":1,"title":"第一条","content":"正文"},{"id":2,"title":"第二条","content":"正文"}],"total":3}}`)
		}
	}))
	defer upstream.Close()
	account := Account{ID: 7, Kind: "sub2api", BaseURL: upstream.URL}
	raw, err := a.fetchAccountAnnouncements(context.Background(), &account)
	if err != nil {
		t.Fatal(err)
	}
	account.Announcements = raw
	items, err := parseAnnouncements(account)
	if err != nil || len(items) != 3 || items[2].Title != "第三条" {
		t.Fatal("announcement pagination lost items")
	}
	failPage.Store(true)
	if _, err := a.fetchAccountAnnouncements(context.Background(), &account); err == nil {
		t.Fatal("partial page must fail instead of replacing the last complete snapshot")
	}
	account.Announcements = `[{"id":1,"title":"公告","content":"正文","read_at":null},{"id":2,"title":"另一个","content":"文本"}]`
	signature := announcementSignature(account)
	account.Announcements = `[{"id":2,"title":"另一个","content":"文本"},{"id":1,"title":"公告","content":"正文","read_at":"2026-11-22T00:00:00Z"}]`
	if announcementSignature(account) != signature {
		t.Fatal("read status or list order caused a false announcement event")
	}
	account.Announcements = strings.Replace(account.Announcements, "正文", "新正文", 1)
	if announcementSignature(account) == signature {
		t.Fatal("changed content did not produce an announcement event")
	}
}

func TestActivityAPIPaginationAndFilters(t *testing.T) {
	a := testApp(t)
	alerts := make([]Alert, 205)
	for index := range alerts {
		alerts[index] = Alert{AccountID: 1, Kind: "monitor_error", Title: fmt.Sprintf("账户故障 %03d", index), Detail: "接口失败", Delivery: "local"}
	}
	alerts[0].Delivery = "failed"
	alerts[204] = Alert{AccountID: 2, Kind: "rates_changed", Title: "第二账户 · 倍率", Detail: rateChangesDetail([]rateChange{{GroupID: "claude", Group: "Claude 分组", Before: floatPointer(0.3), After: floatPointer(0.2), Kind: "changed"}}), Delivery: "pending"}
	if err := a.DB.CreateInBatches(&alerts, 100).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	if call(h, "GET", "/api/alerts?page=1", "", "").Code != 401 || call(h, "GET", "/api/announcements", "", "").Code != 401 {
		t.Fatal("activity API exposed without administrator login")
	}
	cookie := loginForTest(t, h)
	for _, test := range []struct {
		query string
		total int64
		count int
	}{
		{"page=1&page_size=100", 205, 100},
		{"page=3&page_size=100", 205, 5},
		{"page=1&kind=rates_changed&account_id=2&q=Claude", 1, 1},
		{"page=1&account_id=1", 204, 50},
	} {
		response := call(h, "GET", "/api/alerts?"+test.query, "", cookie)
		var result struct {
			Items   []activityAlert `json:"items"`
			Total   int64           `json:"total"`
			Pending int64           `json:"pending"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Total != test.total || len(result.Items) != test.count || result.Pending != 2 {
			t.Fatalf("incomplete history %s: %s", test.query, response.Body.String())
		}
	}
	var legacy []activityAlert
	if response := call(h, "GET", "/api/alerts", "", cookie); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &legacy) != nil || len(legacy) != 100 || len(legacy[0].Changes) != 1 {
		t.Fatal("legacy alert list or structured details changed incompatibly")
	}
	for _, query := range []string{"page=0", "page_size=201", "account_id=bad", "q=" + strings.Repeat("x", 201)} {
		if call(h, "GET", "/api/alerts?"+query, "", cookie).Code != 400 || call(h, "GET", "/api/announcements?"+query, "", cookie).Code != 400 {
			t.Fatalf("invalid activity query accepted: %s", query)
		}
	}
	checked := time.Now()
	accounts := []Account{
		{Name: "Sub2API", Kind: "sub2api", Status: "ok", CheckedAt: &checked, Announcements: `[{"id":2,"title":"最新通知","content":"完整公告","notify_mode":"popup","created_at":"2026-11-22T01:00:00+08:00"},{"id":1,"title":"先前公告","content":"第一条","created_at":"2026-11-20T01:00:00+08:00"}]`},
		{Name: "NewAPI", Kind: "newapi", Status: "ok", CheckedAt: &checked, Announcements: `"<p>站点通知</p>"`},
		{Name: "暂不可用", Kind: "sub2api", Warnings: "公告采集失败", Announcements: `[{"id":1,"title":"上次公告","content":"缓存正文"}]`},
	}
	if err := a.DB.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		total int
		count int
	}{
		{"page=1&page_size=2", 4, 2},
		{"page=2&page_size=2", 4, 2},
		{"account_id=1&q=最新", 1, 1},
		{"account_id=2", 1, 1},
	} {
		response := call(h, "GET", "/api/announcements?"+test.query, "", cookie)
		var result struct {
			Items []announcement `json:"items"`
			Total int            `json:"total"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Total != test.total || len(result.Items) != test.count {
			t.Fatalf("incomplete announcements %s: %s", test.query, response.Body.String())
		}
		if test.query == "account_id=2" && result.Items[0].PublishedAt != nil {
			t.Fatal("missing upstream publication time was fabricated")
		}
	}
}

func TestInvalidAnnouncementCollectionKeepsSnapshot(t *testing.T) {
	a := testApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[true]}`)
	}))
	defer upstream.Close()
	for _, kind := range []string{"newapi", "sub2api"} {
		account := Account{Kind: kind, BaseURL: upstream.URL, Announcements: `"上次完整公告"`}
		if _, err := a.fetchAccountAnnouncements(context.Background(), &account); err == nil || account.Announcements != `"上次完整公告"` {
			t.Fatal("invalid announcement response replaced the last usable snapshot")
		}
	}
}
