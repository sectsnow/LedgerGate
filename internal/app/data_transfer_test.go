package app

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ledgergate/internal/backup"
)

const transferTestPassword = "transfer-password-123"

func transferFixture(t *testing.T) (*App, string) {
	t.Helper()
	a := testApp(t)
	account := Account{ID: 101, Name: "source-account", Kind: "sub2api", BaseURL: "https://upstream.invalid", Enabled: true, AuthMode: "token", Username: "account", UserID: "71", Currency: "USD", QuotaPerUnit: 500000, IntervalSeconds: 300, Token: a.seal("access-secret"), RefreshToken: a.seal("refresh-secret"), Password: a.seal("account-password"), Cookie: a.seal("session=cookie-secret"), LoginExtra: a.seal(`{"tenant":"private"}`), Revision: 1}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	keyID := int64(73)
	channel := Channel{ID: 202, Name: "source-channel", AccountID: &account.ID, UpstreamKeyID: &keyID, BaseURL: account.BaseURL, APIKey: a.seal("upstream-secret"), Protocol: "responses", Protocols: []string{"responses"}, Enabled: true, Models: `["upstream-model"]`, TimeoutSeconds: 120, CooldownSeconds: 60}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	group := Group{ID: 404, Name: "unified-model", Strategy: "manual", PriceMode: "manual", InputPriceMicros: 123, OutputPriceMicros: 456}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	route := Route{ID: 303, GroupID: group.ID, ChannelID: channel.ID, Model: "upstream-model", Weight: 3, Priority: 2, Pricing: &modelPricing{Mode: "token", Source: "upstream", ChannelIdentity: pricingIdentity(channel), priceRates: priceRates{Input: intPointer(12), CacheRead: intPointer(4)}}}
	if err := a.DB.Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&group).Update("active_route_id", route.ID).Error; err != nil {
		t.Fatal(err)
	}
	token := "sk-" + randomToken()
	key := APIKey{ID: 505, Name: "source-key", Enabled: true, Prefix: token[:11], Digest: digest(token), TokenCipher: a.seal(token), AllowedModels: []string{group.Name}, BudgetMicros: intPointer(2500001), UsedMicros: 17}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Setting{}).Where("id=1").Updates(map[string]any{"notification_accounts": jsonString([]uint{account.ID}), "monitor_concurrency": 3}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&NotificationChannel{Name: "source-notification", Type: "webhook", Enabled: true, URL: a.seal("https://notify.invalid/hook"), Secret: a.seal("notification-secret"), Accounts: []uint{account.ID}, Retries: 10}).Error; err != nil {
		t.Fatal(err)
	}
	return a, token
}

func exportTransferForTest(t *testing.T, a *App, sections []string, credentials bool) ([]byte, transferDocument) {
	t.Helper()
	h := a.Router()
	w := call(h, "POST", "/api/data-transfer/export", jsonString(object{"password": transferTestPassword, "sections": sections, "include_credentials": credentials}), loginForTest(t, h))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	plain, err := backup.DecryptData(w.Body.Bytes(), transferTestPassword)
	var doc transferDocument
	if err != nil || json.Unmarshal(plain, &doc) != nil {
		t.Fatal("cannot decode exported document", err)
	}
	if err := doc.validate(); err != nil {
		t.Fatal(err)
	}
	return w.Body.Bytes(), doc
}

func transferUploadForTest(t *testing.T, h http.Handler, cookie, path string, raw []byte, password string, sections []string, token string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "configuration.lgd")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(raw)
	_ = form.WriteField("password", password)
	if sections != nil {
		_ = form.WriteField("sections", jsonString(sections))
	}
	_ = form.WriteField("token", token)
	_ = form.Close()
	req := httptest.NewRequest("POST", path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("X-LedgerGate-Request", "1")
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func transferPreviewForTest(t *testing.T, h http.Handler, cookie string, raw []byte, sections []string) transferPreview {
	t.Helper()
	w := transferUploadForTest(t, h, cookie, "/api/data-transfer/preview", raw, transferTestPassword, sections, "")
	var result transferPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "account-password", "cookie-secret", "upstream-secret", "notification-secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("preview exposed a credential")
		}
	}
	return result
}

func transferDBState(t *testing.T, a *App) string {
	t.Helper()
	state := object{}
	for _, table := range []string{"accounts", "channels", "groups", "routes", "api_keys", "settings", "notification_channels", "notification_deliveries", "sqlite_sequence"} {
		var rows []map[string]any
		if err := a.DB.Table(table).Order("1").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		state[table] = rows
	}
	return jsonString(state)
}

func TestDataTransferSelectiveExportDependenciesAndRedaction(t *testing.T) {
	source, _ := transferFixture(t)
	unrelated := Account{Name: "unrelated", Kind: "newapi", BaseURL: "https://other.invalid", AuthMode: "token", Currency: "USD", QuotaPerUnit: 500000, IntervalSeconds: 300}
	if err := source.DB.Create(&unrelated).Error; err != nil {
		t.Fatal(err)
	}
	raw, doc := exportTransferForTest(t, source, []string{"models"}, true)
	if !reflect.DeepEqual(doc.Sections, []string{"accounts", "channels", "models"}) || len(doc.Accounts) != 1 || len(doc.Channels) != 1 || len(doc.Models) != 1 || len(doc.Keys) != 0 || doc.Settings != nil {
		t.Fatal("selection leaked unrelated data", doc.Sections)
	}
	if bytes.Contains(raw, []byte("access-secret")) || bytes.Contains(raw, []byte("upstream-secret")) || doc.Accounts[0].Credentials.RefreshToken != "refresh-secret" {
		t.Fatal("encrypted export is incorrect")
	}
	_, doc = exportTransferForTest(t, source, []string{"keys"}, false)
	if !reflect.DeepEqual(doc.Sections, []string{"keys"}) || len(doc.Keys) != 1 || doc.Keys[0].Token != "" || len(doc.Accounts) != 0 {
		t.Fatal("keys-only redacted export is incorrect")
	}
	_, doc = exportTransferForTest(t, source, []string{"settings"}, false)
	if len(doc.Accounts) != 1 || doc.Accounts[0].Credentials != (transferCredentials{}) || doc.Settings.WebhookURL != "" || doc.Settings.WebhookSigningKey != "" {
		t.Fatal("credential exclusion failed")
	}
	h := source.Router()
	cookie := loginForTest(t, h)
	for _, sections := range [][]string{nil, {"unknown"}, {"keys", "keys"}} {
		w := call(h, "POST", "/api/data-transfer/export", jsonString(object{"password": transferTestPassword, "sections": sections}), cookie)
		if w.Code != 400 {
			t.Fatal("invalid selection accepted", w.Code)
		}
	}
}

func TestDataTransferCrossInstanceRoundTripPreviewAndDuplicateSkip(t *testing.T) {
	source, token := transferFixture(t)
	raw, _ := exportTransferForTest(t, source, transferSections, true)
	target := testApp(t)
	if err := target.DB.Create(&Account{ID: 101, Name: "local-account", Kind: "newapi", BaseURL: "https://local.invalid", AuthMode: "token", QuotaPerUnit: 500000, IntervalSeconds: 300}).Error; err != nil {
		t.Fatal(err)
	}
	master, _ := os.ReadFile(filepath.Join(target.dataDir, "master.key"))
	h := target.Router()
	cookie := loginForTest(t, h)
	before := transferDBState(t, target)
	preview := transferPreviewForTest(t, h, cookie, raw, nil)
	if transferDBState(t, target) != before {
		t.Fatal("preview persisted changes")
	}
	w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var account Account
	if err := target.DB.Where("name=?", "source-account").First(&account).Error; err != nil || account.ID == 101 || account.Enabled {
		t.Fatal("account ID was not remapped or was enabled", err)
	}
	for cipher, want := range map[string]string{account.Token: "access-secret", account.RefreshToken: "refresh-secret", account.Password: "account-password", account.Cookie: "session=cookie-secret", account.LoginExtra: `{"tenant":"private"}`} {
		if got, err := target.unseal(cipher); err != nil || got != want {
			t.Fatal("account credential was not reencrypted")
		}
	}
	var channel Channel
	target.DB.First(&channel)
	if channel.AccountID == nil || *channel.AccountID != account.ID || channel.Enabled {
		t.Fatal("channel account mapping or disabled state incorrect")
	}
	if got, err := target.unseal(channel.APIKey); err != nil || got != "upstream-secret" {
		t.Fatal("channel credential was not reencrypted")
	}
	var group Group
	target.DB.Preload("Routes").First(&group)
	if len(group.Routes) != 1 || group.Routes[0].ChannelID != channel.ID || group.ActiveRouteID != group.Routes[0].ID || group.Routes[0].Weight != 3 || group.Routes[0].Pricing.ChannelIdentity != "" || !group.Routes[0].Pricing.Stale || group.InputPriceMicros != 123 {
		t.Fatal("route mapping, order, active route or pricing was lost")
	}
	var key APIKey
	target.DB.First(&key)
	plain, err := target.unseal(key.TokenCipher)
	if err != nil || plain != token || key.Digest != digest(token) || key.Enabled || key.UsedMicros != 17 || key.BudgetMicros == nil || *key.BudgetMicros != 2500001 {
		t.Fatal("gateway key, budget or cumulative usage was lost")
	}
	var setting Setting
	target.DB.First(&setting, 1)
	if !reflect.DeepEqual(setting.NotificationAccounts, []uint{account.ID}) || setting.MonitorConcurrency != 3 {
		t.Fatal("settings references were not remapped")
	}
	var notification NotificationChannel
	if err := target.DB.First(&notification).Error; err != nil || notification.Enabled || !reflect.DeepEqual(notification.Accounts, []uint{account.ID}) {
		t.Fatal("notification was not remapped and disabled", err)
	}
	if got, err := target.unseal(notification.Secret); err != nil || got != "notification-secret" {
		t.Fatal("notification credential was not reencrypted")
	}
	if after, _ := os.ReadFile(filepath.Join(target.dataDir, "master.key")); !bytes.Equal(master, after) {
		t.Fatal("import replaced target master key")
	}
	if w := call(h, "GET", "/api/session", "", cookie); w.Code != 200 {
		t.Fatal("import invalidated administrator session")
	}
	preview = transferPreviewForTest(t, h, cookie, raw, nil)
	for _, row := range preview.Sections {
		if row.ID != "settings" && (row.Created != 0 || row.Skipped != row.Count) {
			t.Fatal("duplicate import was not skipped", row)
		}
	}
	before = transferDBState(t, target)
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 || transferDBState(t, target) != before {
		t.Fatal("duplicate import changed configuration", w.Body.String())
	}
}

func TestDataTransferRejectsInvalidOrChangedPreviewAndRollsBack(t *testing.T) {
	source, _ := transferFixture(t)
	raw, doc := exportTransferForTest(t, source, transferSections, true)
	target := testApp(t)
	h := target.Router()
	cookie := loginForTest(t, h)
	preview := transferPreviewForTest(t, h, cookie, raw, []string{"keys"})
	before := transferDBState(t, target)
	for _, test := range []struct {
		password string
		sections []string
		token    string
	}{{"wrong-password-123", []string{"keys"}, preview.Token}, {transferTestPassword, []string{"accounts"}, preview.Token}, {transferTestPassword, []string{"keys"}, "invalid"}} {
		w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, test.password, test.sections, test.token)
		if w.Code < 400 || transferDBState(t, target) != before {
			t.Fatal("invalid request changed data", w.Code)
		}
	}
	tampered := bytes.Clone(raw)
	tampered[len(tampered)-1] ^= 1
	if w := transferUploadForTest(t, h, cookie, "/api/data-transfer/preview", tampered, transferTestPassword, nil, ""); w.Code != 400 {
		t.Fatal("tampered encrypted data accepted")
	}
	otherCookie := loginForTest(t, h)
	if w := transferUploadForTest(t, h, otherCookie, "/api/data-transfer/import", raw, transferTestPassword, []string{"keys"}, preview.Token); w.Code != 409 {
		t.Fatal("preview accepted in another administrator session")
	}
	plain, err := target.unseal(preview.Token)
	var expired transferPlan
	if err != nil || json.Unmarshal([]byte(plain), &expired) != nil {
		t.Fatal("cannot read test preview")
	}
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, []string{"keys"}, target.seal(jsonString(expired))); w.Code != 409 {
		t.Fatal("expired preview accepted")
	}
	if transferDBState(t, target) != before {
		t.Fatal("rejected encrypted data or preview changed configuration")
	}
	if err := target.DB.Model(&Setting{}).Where("id=1").Update("monitor_concurrency", 2).Error; err != nil {
		t.Fatal(err)
	}
	before = transferDBState(t, target)
	w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, []string{"keys"}, preview.Token)
	if w.Code != 409 || transferDBState(t, target) != before {
		t.Fatal("stale preview accepted")
	}
	doc.Models[0].Routes[0].ChannelID = 999999
	broken, err := backup.EncryptData([]byte(jsonString(doc)), transferTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/preview", broken, transferTestPassword, nil, "")
	if w.Code != 400 || transferDBState(t, target) != before {
		t.Fatal("dangling route accepted")
	}
	preview = transferPreviewForTest(t, h, cookie, raw, nil)
	// 在已创建账户之后模拟数据库写入失败，整个事务必须回滚。
	if err := target.DB.Exec("CREATE TRIGGER fail_transfer_channel BEFORE INSERT ON channels BEGIN SELECT RAISE(ABORT,'test failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 409 || transferDBState(t, target) != before {
		t.Fatal("partial import persisted after apply failure", w.Body.String())
	}
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/preview", raw, transferTestPassword, nil, "")
	if w.Code != 409 || transferDBState(t, target) != before {
		t.Fatal("partial import persisted after failure", w.Body.String())
	}
}

func TestDataTransferImportSubsetAndMissingCredentials(t *testing.T) {
	source, _ := transferFixture(t)
	raw, _ := exportTransferForTest(t, source, transferSections, false)
	target := testApp(t)
	h := target.Router()
	cookie := loginForTest(t, h)
	preview := transferPreviewForTest(t, h, cookie, raw, []string{"channels"})
	if !reflect.DeepEqual(preview.Selected, []string{"accounts", "channels"}) {
		t.Fatal("import dependencies were not selected")
	}
	w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var account Account
	var channel Channel
	target.DB.First(&account)
	target.DB.First(&channel)
	if account.Token != "" || account.Enabled || channel.APIKey != "" || channel.Enabled {
		t.Fatal("missing credentials were fabricated or imported resources enabled")
	}
	for _, table := range []any{&Group{}, &APIKey{}} {
		var count int64
		target.DB.Model(table).Count(&count)
		if count != 0 {
			t.Fatal("unselected data imported")
		}
	}
	var setting Setting
	target.DB.First(&setting, 1)
	if setting.MonitorConcurrency != 4 {
		t.Fatal("unselected settings were changed")
	}
	for _, path := range []string{"/api/data-transfer/export", "/api/data-transfer/preview", "/api/data-transfer/import"} {
		if w := call(h, "POST", path, `{}`, ""); w.Code != 401 {
			t.Fatal("unauthenticated endpoint accepted", path)
		}
	}
}
