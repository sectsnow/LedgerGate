package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountMonitoringControl(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	future := time.Now().Add(time.Hour)
	account := Account{Name: "monitored", Kind: "sub2api", BaseURL: "https://example.invalid", Enabled: true, AuthMode: "token", Token: a.seal("access"), RefreshToken: a.seal("refresh"), Balance: floatPointer(42), Rates: `{"groups":[]}`, NextCheckAt: future, Revision: 7}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/accounts/%d/monitoring", account.ID)
	if response := call(h, "PATCH", path, `{"enabled":false}`, ""); response.Code != 401 {
		t.Fatal("unauthenticated monitoring update accepted")
	}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `[]`} {
		if response := call(h, "PATCH", path, body, cookie); response.Code != 400 {
			t.Fatalf("invalid update accepted: %s", body)
		}
	}
	if response := call(h, "PATCH", "/api/accounts/999/monitoring", `{"enabled":false}`, cookie); response.Code != 404 {
		t.Fatal("unknown account accepted")
	}
	for range 2 {
		response := call(h, "PATCH", path, `{"enabled":false}`, cookie)
		if response.Code != 200 {
			t.Fatalf("pause failed: %d %s", response.Code, response.Body.String())
		}
		var saved Account
		if err := a.DB.First(&saved, account.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Enabled || saved.Token != account.Token || saved.RefreshToken != account.RefreshToken || saved.Revision != account.Revision || saved.Rates != account.Rates || saved.Balance == nil || *saved.Balance != 42 || !saved.NextCheckAt.Equal(future) {
			t.Fatal("pause changed credentials, data, or collection schedule")
		}
	}
	response := call(h, "PATCH", path, `{"enabled":true}`, cookie)
	var resumed Account
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &resumed) != nil || !resumed.Enabled || resumed.NextCheckAt.After(time.Now()) {
		t.Fatal("resume did not schedule an immediate check")
	}
	response = call(h, "PATCH", path, `{"enabled":true}`, cookie)
	var repeated Account
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &repeated) != nil || !repeated.NextCheckAt.Equal(resumed.NextCheckAt) {
		t.Fatal("repeated resume unexpectedly rescheduled collection")
	}
}

func TestPauseDuringCollectionAndQueuedChecks(t *testing.T) {
	a := testApp(t)
	// 单个工作线程保证第二个账户处于排队状态，另有测试覆盖并发采集。
	if err := a.DB.Model(&Setting{}).Where("id=1").Update("monitor_concurrency", 1).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, enteredOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var firstCalls, secondCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			if r.Header.Get("Authorization") == "Bearer first-token" {
				firstCalls.Add(1)
				enteredOnce.Do(func() { close(entered) })
				<-release
			} else {
				secondCalls.Add(1)
			}
			fmt.Fprint(w, `{"data":{"quota":5000000,"used_quota":0}}`)
		case "/api/log/self/stat":
			fmt.Fprint(w, `{"data":{"quota":0}}`)
		case "/api/user/self/groups":
			fmt.Fprint(w, `{"data":{}}`)
		case "/api/notice":
			fmt.Fprint(w, `{"data":""}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	defer unblock()
	accounts := []Account{
		{Name: "first", Kind: "newapi", BaseURL: server.URL, Enabled: true, AuthMode: "token", Token: a.seal("first-token"), QuotaPerUnit: 500000, IntervalSeconds: 300, NextCheckAt: time.Now().Add(-2 * time.Hour)},
		{Name: "second", Kind: "newapi", BaseURL: server.URL, Enabled: true, AuthMode: "token", Token: a.seal("second-token"), QuotaPerUnit: 500000, IntervalSeconds: 300, NextCheckAt: time.Now().Add(-time.Hour)},
	}
	for i := range accounts {
		if err := a.DB.Create(&accounts[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); a.checkDueAccounts(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first collection never started")
	}
	for _, account := range accounts {
		response := call(h, "PATCH", fmt.Sprintf("/api/accounts/%d/monitoring", account.ID), `{"enabled":false}`, cookie)
		if response.Code != 200 {
			t.Fatalf("pause while collection active failed: %d", response.Code)
		}
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collection did not finish")
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatal("paused queued account was still collected")
	}
	for _, account := range accounts {
		var stored Account
		a.DB.First(&stored, account.ID)
		if stored.Enabled || stored.Token != account.Token {
			t.Fatal("in-flight collection overwrote pause or credentials")
		}
	}
	if _, err := a.collectAccount(context.Background(), accounts[1].ID, true); err != nil || secondCalls.Load() != 0 {
		t.Fatal("scheduled check ignored paused state")
	}
	if _, err := a.checkAccount(context.Background(), accounts[1].ID); err != nil || secondCalls.Load() != 1 {
		t.Fatal("manual collection should remain available while paused")
	}
	response := call(h, "PATCH", fmt.Sprintf("/api/accounts/%d/monitoring", accounts[1].ID), `{"enabled":true}`, cookie)
	if response.Code != 200 {
		t.Fatal("resume failed")
	}
	a.checkDueAccounts(context.Background())
	if secondCalls.Load() != 2 || firstCalls.Load() != 1 {
		t.Fatal("resume did not restore automatic monitoring independently")
	}
	if _, err := a.collectAccount(context.Background(), accounts[1].ID, true); err != nil || secondCalls.Load() != 2 {
		t.Fatal("stale scheduled work ignored next check time")
	}
	channel := Channel{Name: "paused with account", AccountID: &accounts[0].ID, BaseURL: server.URL, APIKey: a.seal("upstream-secret"), Enabled: true}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if len(a.candidates(Group{Routes: []Route{{ChannelID: channel.ID}}})) != 0 {
		t.Fatal("paused account still has a forwarding candidate")
	}
}
