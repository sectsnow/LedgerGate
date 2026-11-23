package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNotificationAdapterPayloadsAndBusinessResults(t *testing.T) {
	a := testApp(t)
	alert := Alert{ID: 42, AccountID: 9, Kind: "low_balance", Title: "账户 · 余额提醒", Detail: strings.Repeat("中文内容", 600), CreatedAt: time.Now()}
	for _, kind := range []string{"webhook", "feishu", "wecom", "dingtalk", "telegram", "serverchan"} {
		t.Run(kind, func(t *testing.T) {
			channel := NotificationChannel{Type: kind, URL: a.seal("https://notify.invalid/hook?key=private"), Secret: a.seal("signing-secret"), Token: a.seal("123:abcdefghijklmno"), ChatID: "-10001", SendKey: a.seal("SCT12345abc")}
			response := `{"code":0,"errcode":0,"ok":true}`
			a.client.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(r.Body)
				var body object
				if json.Unmarshal(raw, &body) != nil || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("invalid notification request")
				}
				switch kind {
				case "webhook":
					mac := hmac.New(sha256.New, []byte("signing-secret"))
					mac.Write(raw)
					if r.Header.Get("X-LedgerGate-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || body["alert_id"] != float64(42) || body["detail"] != alert.Detail {
						t.Error("webhook event or signature changed")
					}
				case "feishu":
					stamp, _ := body["timestamp"].(string)
					mac := hmac.New(sha256.New, []byte(stamp+"\nsigning-secret"))
					if body["msg_type"] != "text" || body["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) || !strings.Contains(str(obj(body["content"])["text"]), alert.Title) {
						t.Error("invalid Feishu body or signing")
					}
				case "wecom":
					text := str(obj(body["text"])["content"])
					if body["msgtype"] != "text" || len(text) > 2048 || !utf8.ValidString(text) || !strings.HasPrefix(text, "LedgerGate") {
						t.Error("invalid WeCom text or byte limit")
					}
				case "dingtalk":
					stamp := r.URL.Query().Get("timestamp")
					mac := hmac.New(sha256.New, []byte("signing-secret"))
					mac.Write([]byte(stamp + "\nsigning-secret"))
					markdown := obj(body["markdown"])
					if body["msgtype"] != "markdown" || !strings.Contains(str(markdown["title"]), "余额偏低") || !strings.Contains(str(markdown["text"]), "检测时间") || strings.Contains(str(markdown["text"]), "LedgerGate") || stamp == "" || r.URL.Query().Get("sign") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) || r.URL.Query().Get("key") != "private" {
						t.Error("invalid DingTalk signing or query encoding")
					}
				case "telegram":
					if r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot123:abcdefghijklmno/sendMessage" || body["chat_id"] != "-10001" || len(str(body["text"])) > 3800 {
						t.Error("invalid Telegram request")
					}
				case "serverchan":
					if r.URL.String() != "https://sctapi.ftqq.com/SCT12345abc.send" || body["title"] != alert.Title || body["desp"] != alert.Detail {
						t.Error("invalid ServerChan request")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
			})
			if err := a.sendNotification(context.Background(), channel, alert); err != nil {
				t.Fatal(err)
			}
			if kind != "webhook" {
				response = `{"code":123,"errcode":123,"ok":false}`
				if err := a.sendNotification(context.Background(), channel, alert); err == nil {
					t.Fatal("HTTP 200 business failure accepted")
				}
				response = `{}`
				if err := a.sendNotification(context.Background(), channel, alert); err == nil {
					t.Fatal("missing business result accepted")
				}
			}
		})
	}
	if address, err := serverChanAddress("sctp123tabcdef"); err != nil || address != "https://123.push.ft07.com/send/sctp123tabcdef.send" {
		t.Fatal("ServerChan3 routing incorrect", address, err)
	}
	for _, key := range []string{"sctpwrong", "sctp../tkey", "SCTkey/secret", "wrong-prefix"} {
		if _, err := serverChanAddress(key); err == nil {
			t.Fatal("invalid SendKey accepted", key)
		}
	}
}

func TestNotificationChannelCRUDSecretsAndValidation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	input := object{"name": "群通知", "type": "dingtalk", "enabled": true, "url": "https://notify.invalid/?access_token=private-url-token", "secret": "private-signature", "events": []string{"low_balance"}, "retries": 3}
	if response := call(h, "GET", "/api/notification-channels", "", ""); response.Code != 401 {
		t.Fatal("notification settings require admin authentication")
	}
	response := call(h, "POST", "/api/notification-channels", jsonString(input), cookie)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var channel NotificationChannel
	a.DB.First(&channel)
	if channel.URL == input["url"] || channel.Secret == input["secret"] {
		t.Fatal("notification secrets stored in plaintext")
	}
	for _, response := range []*httptest.ResponseRecorder{response, call(h, "GET", "/api/notification-channels", "", cookie)} {
		for _, secret := range []string{"private-url-token", "private-signature", channel.URL, channel.Secret} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("secret leaked in channel API")
			}
		}
	}
	input["name"], input["url"], input["secret"] = "新名称", "", ""
	response = call(h, "PUT", fmt.Sprintf("/api/notification-channels/%d", channel.ID), jsonString(input), cookie)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var edited NotificationChannel
	a.DB.First(&edited, channel.ID)
	if edited.URL != channel.URL || edited.Secret != channel.Secret || edited.Name != "新名称" {
		t.Fatal("blank fields lost stored credentials")
	}
	input["clear_secret"] = true
	if response := call(h, "PUT", fmt.Sprintf("/api/notification-channels/%d", channel.ID), jsonString(input), cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	a.DB.First(&edited, channel.ID)
	if edited.Secret != "" {
		t.Fatal("clear secret did not clear")
	}
	input["clear_url"] = true
	if response := call(h, "PUT", fmt.Sprintf("/api/notification-channels/%d", channel.ID), jsonString(input), cookie); response.Code != 400 {
		t.Fatal("enabled webhook accepted missing URL")
	}
	input["clear_url"], input["accounts"] = false, []uint{99999}
	if response := call(h, "PUT", fmt.Sprintf("/api/notification-channels/%d", channel.ID), jsonString(input), cookie); response.Code != 400 {
		t.Fatal("unknown account accepted")
	}
	input["accounts"], input["events"] = []uint{}, []string{"unknown-event"}
	if response := call(h, "PUT", fmt.Sprintf("/api/notification-channels/%d", channel.ID), jsonString(input), cookie); response.Code != 400 {
		t.Fatal("unknown event accepted")
	}
	for _, row := range []NotificationChannel{
		{Name: "email", Type: "email", Retries: 2, SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPTLS: "none", SMTPFrom: "a@example.com", SMTPTo: []string{"b@example.com"}},
		{Name: "email", Type: "email", Retries: 2, SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPTLS: "starttls", SMTPFrom: "a@example.com\r\nBcc: victim@example.com", SMTPTo: []string{"b@example.com"}},
	} {
		if err := a.validateNotification(row, true); err == nil {
			t.Fatal("unsafe SMTP configuration accepted")
		}
	}
}

func TestNotificationIndependentRetriesPauseAndDeletion(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var sent, failed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good" {
			sent.Add(1)
			w.WriteHeader(204)
		} else {
			failed.Add(1)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	account := Account{Name: "测试账户"}
	a.DB.Create(&account)
	channels := []NotificationChannel{
		{Name: "正常", Type: "webhook", Enabled: true, URL: a.seal(server.URL + "/good"), Retries: 2},
		{Name: "失败", Type: "webhook", Enabled: true, URL: a.seal(server.URL + "/failed"), Retries: 2},
		{Name: "其他事件", Type: "webhook", Enabled: true, URL: a.seal(server.URL + "/good"), Events: []string{"announcement"}, Retries: 2},
		{Name: "其他账户", Type: "webhook", Enabled: true, URL: a.seal(server.URL + "/good"), Accounts: []uint{999}, Retries: 2},
		{Name: "已暂停", Type: "webhook", Enabled: false, URL: a.seal(server.URL + "/good"), Retries: 2},
	}
	for i := range channels {
		if err := a.DB.Create(&channels[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DB.Transaction(func(tx *gorm.DB) error { return emitAlert(tx, account, "low_balance", "余额提醒", "余额 1") }); err != nil {
		t.Fatal(err)
	}
	var deliveries []NotificationDelivery
	a.DB.Order("id").Find(&deliveries)
	if len(deliveries) != 2 {
		t.Fatal("account/event/enabled filters ignored", len(deliveries))
	}
	a.deliverAlerts(context.Background())
	if sent.Load() != 1 || failed.Load() != 1 {
		t.Fatal("missing initial fanout")
	}
	a.DB.Model(&NotificationDelivery{}).Where("status = ?", "pending").Update("next_retry_at", time.Now().Add(-time.Second))
	path := fmt.Sprintf("/api/notification-channels/%d", channels[1].ID)
	if response := call(h, "PATCH", path+"/enabled", `{"enabled":false}`, cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	a.deliverAlerts(context.Background())
	if failed.Load() != 1 {
		t.Fatal("paused channel still delivered")
	}
	if response := call(h, "PATCH", path+"/enabled", `{"enabled":true}`, cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	a.deliverAlerts(context.Background())
	if sent.Load() != 1 || failed.Load() != 2 {
		t.Fatal("successful recipient was retried or paused recipient did not resume")
	}
	var alert Alert
	a.DB.First(&alert)
	if alert.Delivery != "partial" {
		t.Fatal("mixed final result not recorded", alert.Delivery)
	}
	response := call(h, "GET", "/api/alerts?page=1", "", cookie)
	var page struct {
		Items []activityAlert `json:"items"`
	}
	json.Unmarshal(response.Body.Bytes(), &page)
	if response.Code != 200 || len(page.Items) != 1 || len(page.Items[0].Deliveries) != 2 {
		t.Fatal("per-channel results missing from alert API", response.Body.String())
	}
	// 删除渠道保留投递历史，并取消尚未发送的任务。
	a.DB.Model(&NotificationDelivery{}).Where("channel_id = ?", channels[1].ID).Update("status", "pending")
	if response := call(h, "DELETE", path, "", cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var delivery NotificationDelivery
	a.DB.Where("channel_id = ?", channels[1].ID).First(&delivery)
	if delivery.Status != "cancelled" {
		t.Fatal("deleted channel left pending notification")
	}
	a.DB.First(&alert, alert.ID)
	if alert.Delivery != "partial" {
		t.Fatal("one successful recipient hid a cancelled recipient", alert.Delivery)
	}
	response = call(h, "GET", "/api/notification-deliveries?page=1", "", cookie)
	if !strings.Contains(response.Body.String(), "已删除") || response.Code != 200 {
		t.Fatal("deletion erased history")
	}
}

func TestNotificationLegacyMigrationPreservesQueueAndIsIdempotent(t *testing.T) {
	a := testApp(t)
	options := object{"notifications_migrated": false, "notification_type": "telegram", "telegram_token": a.seal("123:abcdefghijklmno"), "telegram_chat_id": "-100", "notification_events": jsonString([]string{"low_balance"}), "notification_accounts": jsonString([]uint{7})}
	if err := a.DB.Model(&Setting{}).Where("id=1").Updates(options).Error; err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(time.Hour)
	alerts := []Alert{{Title: "待重试", Delivery: "pending", Attempts: 3, NextRetryAt: next}, {Title: "已发送", Delivery: "sent", Attempts: 1}}
	if err := a.DB.Create(&alerts).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.migrateNotifications(); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateNotifications(); err != nil {
		t.Fatal(err)
	}
	var channels []NotificationChannel
	a.DB.Find(&channels)
	var deliveries []NotificationDelivery
	a.DB.Order("id").Find(&deliveries)
	if len(channels) != 1 || !reflect.DeepEqual(channels[0].Accounts, []uint{7}) || !reflect.DeepEqual(channels[0].Events, []string{"low_balance"}) || channels[0].Token != options["telegram_token"] || len(deliveries) != 2 || deliveries[0].Attempts != 3 || !deliveries[0].NextRetryAt.Equal(next) || deliveries[1].Status != "sent" {
		t.Fatal("migration lost configuration or queue state", channels, deliveries)
	}
	var setting Setting
	a.DB.First(&setting, 1)
	if !setting.NotificationsMigrated || setting.TelegramToken != "" || setting.WebhookURL != "" {
		t.Fatal("legacy credentials not migrated")
	}
}

func TestNotificationManualTestsAreRecordedAndNotRetried(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	channel := NotificationChannel{Name: "手动测试", Type: "webhook", URL: a.seal(server.URL), Retries: 10}
	a.DB.Create(&channel)
	response := call(h, "POST", fmt.Sprintf("/api/notification-channels/%d/test", channel.ID), "", cookie)
	if response.Code != 502 {
		t.Fatal(response.Code, response.Body.String())
	}
	a.deliverAlerts(context.Background())
	var rows []NotificationDelivery
	a.DB.Find(&rows)
	if calls.Load() != 1 || len(rows) != 1 || rows[0].Status != "failed" || rows[0].AlertID != nil || rows[0].Attempts != 1 {
		t.Fatal("manual failure was not logged or retried automatically")
	}
}

func TestNotificationQueueSurvivesRestartWithoutRecreatingDeletedChannels(t *testing.T) {
	config := Config{DataDir: t.TempDir(), AdminPassword: "test-password-12345"}
	a, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a != nil {
			a.Close()
		}
	})
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	channel := NotificationChannel{Name: "可恢复队列", Type: "webhook", Enabled: true, URL: a.seal(server.URL), Secret: a.seal("persistent-secret"), Retries: 3}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	alert := Alert{Title: "重启前告警", Delivery: "pending"}
	if err := a.DB.Create(&alert).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&NotificationDelivery{AlertID: &alert.ID, ChannelID: channel.ID, ChannelName: channel.Name, ChannelType: channel.Type, Title: alert.Title, Status: "pending", Attempts: 1, NextRetryAt: time.Now().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = nil
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	a.deliverAlerts(context.Background())
	var delivery NotificationDelivery
	a.DB.First(&delivery)
	if count.Load() != 1 || delivery.Status != "sent" || delivery.Attempts != 2 {
		t.Fatal("pending queue or encrypted credential did not survive restart", delivery)
	}
	if err := a.DB.Delete(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = nil
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	var channels int64
	a.DB.Model(&NotificationChannel{}).Count(&channels)
	if channels != 0 {
		t.Fatal("deleted channel recreated during startup")
	}
}

func TestNotificationAccountDeletionPreservesSubscriptionScope(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	accounts := []Account{{Name: "删除的账户"}, {Name: "保留的账户"}}
	if err := a.DB.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	channels := []NotificationChannel{
		{Name: "仅被删除账户", Type: "webhook", Enabled: true, Accounts: []uint{accounts[0].ID}, Retries: 2},
		{Name: "多个账户", Type: "webhook", Enabled: true, Accounts: []uint{accounts[0].ID, accounts[1].ID}, Retries: 2},
		{Name: "所有账户", Type: "webhook", Enabled: true, Retries: 2},
	}
	if err := a.DB.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}
	if response := call(h, "DELETE", fmt.Sprintf("/api/accounts/%d", accounts[0].ID), "", cookie); response.Code != 204 {
		t.Fatal(response.Body.String())
	}
	var rows []NotificationChannel
	a.DB.Order("id").Find(&rows)
	if rows[0].Enabled || len(rows[0].Accounts) != 0 || !rows[1].Enabled || !reflect.DeepEqual(rows[1].Accounts, []uint{accounts[1].ID}) || !rows[2].Enabled || len(rows[2].Accounts) != 0 {
		t.Fatal("deleting account broadened notification scope", rows)
	}
}

func TestNotificationUsesConfiguredHTTPProxy(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "notify.invalid" || r.URL.Path != "/notify" || r.Method != "POST" {
			t.Error("notification did not preserve target through proxy")
		}
		calls.Add(1)
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	if err := a.DB.Model(&Setting{}).Where("id=1").Updates(object{"proxy_enabled": true, "proxy_url": a.seal(proxy.URL)}).Error; err != nil {
		t.Fatal(err)
	}
	channel := NotificationChannel{Type: "webhook", UseProxy: true, URL: a.seal("http://notify.invalid/notify")}
	if err := a.sendNotification(context.Background(), channel, Alert{Title: "仅本地代理测试"}); err != nil || calls.Load() != 1 {
		t.Fatal("notification bypassed configured proxy", err)
	}
}
