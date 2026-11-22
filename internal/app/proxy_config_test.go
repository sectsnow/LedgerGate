package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyChannelInheritanceAndEditing(t *testing.T) {
	a, _, h, cookie, input, base := creationFixture(t, "sub2api")
	if err := a.DB.Model(&Account{}).Where("id = 1").Update("use_proxy", true).Error; err != nil {
		t.Fatal(err)
	}
	w := call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie)
	var created Channel
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || !created.UseProxy {
		t.Fatal("upstream channel did not inherit proxy option", w.Code)
	}
	input.UseProxy = new(bool)
	if w := call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie); w.Code != 409 {
		t.Fatal("creation fingerprint ignored proxy option")
	}
	payload := object{"name": "manual", "base_url": base, "account_id": 1, "enabled": true, "protocol": "openai", "api_key": "other-key", "timeout_seconds": 5, "cooldown_seconds": 60}
	w = call(h, "POST", "/api/channels", jsonString(payload), cookie)
	var channel Channel
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &channel) != nil || !channel.UseProxy {
		t.Fatal("manual channel did not inherit proxy option")
	}
	payload["api_key"] = ""
	path := fmt.Sprintf("/api/channels/%d", channel.ID)
	if w := call(h, "PUT", path, jsonString(payload), cookie); w.Code != 200 {
		t.Fatal("legacy channel edit failed")
	}
	a.DB.First(&channel, channel.ID)
	if !channel.UseProxy {
		t.Fatal("legacy channel edit cleared proxy option")
	}
	payload["use_proxy"] = false
	if w := call(h, "PUT", path, jsonString(payload), cookie); w.Code != 200 {
		t.Fatal("explicit direct choice failed")
	}
	a.DB.First(&channel, channel.ID)
	if channel.UseProxy {
		t.Fatal("explicit direct choice not persisted")
	}
	accountInput := accountInput{Name: "renamed", Kind: "sub2api", BaseURL: base, AuthMode: "token", Currency: "USD", QuotaPerUnit: 500000, IntervalSeconds: 300}
	if w := call(h, "PUT", "/api/accounts/1", jsonString(accountInput), cookie); w.Code != 200 {
		t.Fatal("legacy account edit failed", w.Code)
	}
	var account Account
	a.DB.First(&account, 1)
	if !account.UseProxy {
		t.Fatal("legacy account edit cleared proxy option")
	}
	assertStoredTokens(t, a, account.ID, "access-0", "refresh-0")
}

func TestProxyImportChannelInheritance(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"id":11,"name":"imported","key":"sk-imported-proxy-key","status":"active","group_id":7}}`)
	}))
	defer server.Close()
	account := tokenTestAccount(t, a, server.URL)
	if err := a.DB.Model(&account).Update("use_proxy", true).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	w := call(h, "POST", "/api/accounts/1/import-channels", `{"key_ids":[11],"protocols":["openai"]}`, cookie)
	if w.Code != 200 {
		t.Fatal("channel import failed", w.Code, w.Body.String())
	}
	var channels []Channel
	a.DB.Find(&channels)
	if len(channels) != 1 || !channels[0].UseProxy {
		t.Fatal("imported channel did not inherit proxy option")
	}
}

func TestProxyPersistenceAndTransfer(t *testing.T) {
	source, _ := transferFixture(t)
	setTestProxy(t, source, "socks5://proxy-user:proxy-pass@127.0.0.1:1080")
	_, hidden := exportTransferForTest(t, source, []string{"settings"}, false)
	if hidden.Settings.ProxyAddress != "" || hidden.Settings.ProxyOn {
		t.Fatal("export without credentials included proxy config")
	}
	raw, doc := exportTransferForTest(t, source, []string{"settings"}, true)
	if !doc.Settings.ProxyOn || doc.Settings.ProxyAddress != "socks5://proxy-user:proxy-pass@127.0.0.1:1080" {
		t.Fatal("export lost proxy config")
	}
	target := testApp(t)
	h := target.Router()
	cookie := loginForTest(t, h)
	preview := transferPreviewForTest(t, h, cookie, raw, nil)
	w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal("proxy import failed", w.Code)
	}
	setting, address, err := target.readProxy()
	if err != nil || !setting.ProxyEnabled || address.String() != doc.Settings.ProxyAddress || strings.Contains(setting.ProxyURL, "proxy-pass") {
		t.Fatal("proxy import did not reencrypt config")
	}
	config := Config{DataDir: t.TempDir(), AdminPassword: "proxy-persistence-test-password"}
	a, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	setTestProxy(t, a, "http://127.0.0.1:7890")
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	setting, address, err = a.readProxy()
	if err != nil || !setting.ProxyEnabled || address.String() != "http://127.0.0.1:7890" {
		t.Fatal("proxy config did not survive restart")
	}
}
