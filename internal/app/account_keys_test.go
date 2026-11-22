package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

func TestImportSub2APIChannels(t *testing.T) {
	a := testApp(t)
	var detailCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions" {
			if r.Header.Get("Authorization") != "Bearer sk-imported-secret-11" && r.Header.Get("Authorization") != "Bearer sk-imported-secret-12" {
				t.Error("imported credential not used for forwarding")
			}
			chatResponse(w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access-0" {
			t.Error("missing account authorization")
		}
		if r.URL.Path == "/api/v1/keys" {
			if r.URL.Query().Get("page_size") != "20" || r.URL.Query().Get("page") != "1" {
				t.Error("incorrect pagination")
			}
			fmt.Fprint(w, `{"data":{"total":2,"items":[{"id":11,"name":"主力密钥","status":"active","key":"sk-imported-secret-11","group":{"name":"默认"}},{"id":12,"name":"备用密钥","status":"active","key":"sk-imported-secret-12","group":{"name":"备用"}}]}}`)
			return
		}
		detailCalls.Add(1)
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/keys/"))
		if id != 11 && id != 12 {
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"data":{"id":%d,"name":"上游密钥-%d","key":"sk-imported-secret-%d","status":"active","group_id":3,"quota":0}}`, id, id, id)
	}))
	defer upstream.Close()
	account := tokenTestAccount(t, a, upstream.URL)
	if err := a.DB.Model(&account).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	if w := call(h, "GET", "/api/accounts/1/api-keys", "", ""); w.Code != 401 {
		t.Fatal("key list exposed without admin session")
	}
	response := call(h, "GET", "/api/accounts/1/api-keys", "", cookie)
	var page struct {
		Items []upstreamKey
		Total int
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Items) != 2 || page.Total != 2 {
		t.Fatal(response.Body.String())
	}
	if strings.Contains(response.Body.String(), "sk-imported-secret") || strings.Contains(response.Body.String(), `"key":`) {
		t.Fatal("upstream credentials exposed in key listing")
	}
	if page.Items[0].Group != "默认" || !page.Items[0].Importable {
		t.Fatal("key metadata missing")
	}
	request := `{"key_ids":[11,12],"protocols":["openai","responses","anthropic"]}`
	response = call(h, "POST", "/api/accounts/1/import-channels", request, cookie)
	var result struct {
		Created  []Channel
		Existing []Channel
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Created) != 2 || len(result.Existing) != 0 {
		t.Fatal(response.Body.String())
	}
	if strings.Contains(response.Body.String(), "sk-imported-secret") || strings.Contains(response.Body.String(), `"api_key":`) {
		t.Fatal("import response exposed a credential")
	}
	var channels []Channel
	if err := a.DB.Order("id").Find(&channels).Error; err != nil {
		t.Fatal(err)
	}
	for _, channel := range channels {
		if channel.AccountID == nil || *channel.AccountID != account.ID || channel.UpstreamKeyID == nil || channel.BaseURL != upstream.URL || len(channel.Protocols) != 3 || !channel.Enabled {
			t.Fatal("channel ownership or protocol not imported")
		}
		secret, err := a.unseal(channel.APIKey)
		if err != nil || secret != fmt.Sprintf("sk-imported-secret-%d", *channel.UpstreamKeyID) || secret == channel.APIKey {
			t.Fatal("channel credential not encrypted")
		}
	}
	response = call(h, "POST", "/api/accounts/1/import-channels", request, cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Created) != 0 || len(result.Existing) != 2 {
		t.Fatal("repeat import created duplicates", response.Body.String())
	}
	if detailCalls.Load() != 2 {
		t.Fatal("already imported credentials unnecessarily retrieved")
	}
	response = call(h, "GET", "/api/accounts/1/api-keys", "", cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Items[0].ImportedChannelID == 0 {
		t.Fatal("imported key not marked")
	}
	token := createGroup(t, h, cookie, channels, "weighted")
	response = call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`, token)
	if response.Code != 200 {
		t.Fatal("imported channel cannot forward", response.Body.String())
	}
}

func TestImportNewAPIKeys(t *testing.T) {
	a := testApp(t)
	var revealCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer access-0" || r.Header.Get("New-Api-User") != "9" {
			t.Error("incorrect NewAPI account authentication")
		}
		switch r.URL.Path {
		case "/api/token/":
			if r.URL.Query().Get("p") != "2" {
				t.Error("NewAPI pagination not forwarded")
			}
			fmt.Fprint(w, `{"success":true,"data":{"total":21,"items":[{"id":11,"name":"官方渠道","group":"default","status":1,"expired_time":-1,"unlimited_quota":true,"remain_quota":0}]}}`)
		case "/api/token/11":
			fmt.Fprint(w, `{"success":true,"data":{"id":11,"name":"官方渠道","group":"default","status":1,"key":"sk-***","expired_time":-1,"unlimited_quota":true,"remain_quota":0}}`)
		case "/api/token/11/key":
			if r.Method != "POST" {
				t.Error("wrong reveal method")
			}
			revealCalls.Add(1)
			fmt.Fprint(w, `{"success":true,"data":{"key":"sk-full-newapi-secret"}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	account := tokenTestAccount(t, a, server.URL)
	if err := a.DB.Model(&account).Updates(map[string]any{"kind": "newapi", "user_id": "9", "refresh_token": ""}).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "GET", "/api/accounts/1/api-keys?page=2", "", cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"total":21`) || !strings.Contains(response.Body.String(), `"group":"default"`) {
		t.Fatal(response.Body.String())
	}
	response = call(h, "POST", "/api/accounts/1/import-channels", `{"key_ids":[11],"protocols":["openai"]}`, cookie)
	if response.Code != 200 || revealCalls.Load() != 1 {
		t.Fatal(response.Body.String())
	}
	var channel Channel
	if a.DB.First(&channel).Error != nil {
		t.Fatal("missing imported channel")
	}
	key, err := a.unseal(channel.APIKey)
	if err != nil || key != "sk-full-newapi-secret" {
		t.Fatal("revealed NewAPI key not used")
	}
}

func TestImportRejectsUnavailableAndIncompleteKeys(t *testing.T) {
	for _, test := range []struct {
		name, body string
		code       int
	}{
		{name: "disabled", body: `{"id":12,"name":"disabled","status":"disabled","key":"sk-full-secret"}`, code: 200},
		{name: "expired", body: `{"id":12,"name":"expired","status":"active","key":"sk-full-secret","expires_at":"2020-01-01T00:00:00Z"}`, code: 200},
		{name: "quota_exhausted", body: `{"id":12,"name":"quota","status":"active","key":"sk-full-secret","quota":10,"quota_used":10}`, code: 200},
		{name: "masked", body: `{"id":12,"name":"masked","status":"active","key":"sk-********"}`, code: 200},
		{name: "wrong_id", body: `{"id":13,"name":"wrong","status":"active","key":"sk-full-secret"}`, code: 200},
		{name: "not_owned", body: `{}`, code: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := testApp(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/keys/11" {
					fmt.Fprint(w, `{"data":{"id":11,"name":"valid","status":"active","key":"sk-valid-secret"}}`)
					return
				}
				w.WriteHeader(test.code)
				fmt.Fprint(w, `{"data":`+test.body+`}`)
			}))
			defer server.Close()
			tokenTestAccount(t, a, server.URL)
			h := a.Router()
			cookie := loginForTest(t, h)
			response := call(h, "POST", "/api/accounts/1/import-channels", `{"key_ids":[11,12],"protocols":["openai"]}`, cookie)
			if response.Code == 200 {
				t.Fatal("unavailable key imported")
			}
			var count int64
			a.DB.Model(&Channel{}).Count(&count)
			if count != 0 {
				t.Fatal("partial batch persisted after validation failure")
			}
			if strings.Contains(response.Body.String(), "sk-full-secret") {
				t.Fatal("secret leaked in error")
			}
		})
	}
}

func TestImportDuplicateManualChannelAndRollback(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/keys/"))
		fmt.Fprintf(w, `{"data":{"id":%d,"name":%q,"status":"active","key":"sk-secret-%d"}}`, id, strings.Repeat("中文密钥", 100), id)
	}))
	defer server.Close()
	account := tokenTestAccount(t, a, server.URL)
	manual := Channel{Name: "manual", AccountID: &account.ID, BaseURL: server.URL, APIKey: a.seal("sk-secret-11")}
	if a.DB.Create(&manual).Error != nil {
		t.Fatal("failed to create manual channel")
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "POST", "/api/accounts/1/import-channels", `{"key_ids":[11,12],"protocols":["openai"]}`, cookie)
	var result struct {
		Created  []Channel
		Existing []Channel
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Created) != 1 || len(result.Existing) != 1 || result.Existing[0].ID != manual.ID {
		t.Fatal(response.Body.String())
	}
	if len(result.Created[0].Name) > 100 || !utf8.ValidString(result.Created[0].Name) {
		t.Fatal("generated name is invalid")
	}
	if err := a.DB.Callback().Create().Before("gorm:create").Register("test:fail_second_import", func(tx *gorm.DB) {
		if channel, ok := tx.Statement.Dest.(*Channel); ok && channel.UpstreamKeyID != nil && *channel.UpstreamKeyID == 14 {
			tx.AddError(errors.New("simulated storage failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.DB.Callback().Create().Remove("test:fail_second_import")
	response = call(h, "POST", "/api/accounts/1/import-channels", `{"key_ids":[13,14],"protocols":["openai"]}`, cookie)
	if response.Code == 200 {
		t.Fatal("storage failure ignored")
	}
	var count int64
	a.DB.Model(&Channel{}).Count(&count)
	if count != 2 {
		t.Fatal("partially created channel survived transaction rollback")
	}
}

func TestImportValidatesInputAndHonorsAccountLock(t *testing.T) {
	a := testApp(t)
	tokenTestAccount(t, a, "http://127.0.0.1:1")
	h := a.Router()
	cookie := loginForTest(t, h)
	for _, body := range []string{
		`{"key_ids":[],"protocols":["openai"]}`,
		`{"key_ids":[11],"protocols":[]}`,
		`{"key_ids":[11,11],"protocols":["openai"]}`,
		`{"key_ids":[-1],"protocols":["openai"]}`,
		`{"key_ids":[11],"protocols":["openai","openai"]}`,
		`{"key_ids":[11],"protocols":["unknown"]}`,
	} {
		if response := call(h, "POST", "/api/accounts/1/import-channels", body, cookie); response.Code != 400 {
			t.Fatal("invalid input accepted", response.Body.String())
		}
	}
	if response := call(h, "GET", "/api/accounts/1/api-keys?page=0", "", cookie); response.Code != 400 {
		t.Fatal("invalid pagination accepted")
	}
	if !a.claimAccount(1) {
		t.Fatal("could not reserve account")
	}
	for _, path := range []string{"/api/accounts/1/api-keys", "/api/accounts/1/import-channels"} {
		method, body := "GET", ""
		if strings.HasSuffix(path, "import-channels") {
			method, body = "POST", `{"key_ids":[11],"protocols":["openai"]}`
		}
		if response := call(h, method, path, body, cookie); response.Code != 409 {
			t.Fatal("concurrent credential operation allowed")
		}
	}
	a.releaseAccount(1)
}

func TestImportedChannelUniqueIndex(t *testing.T) {
	a := testApp(t)
	account := tokenTestAccount(t, a, "http://127.0.0.1:1")
	id := int64(11)
	first := Channel{AccountID: &account.ID, UpstreamKeyID: &id, Name: "first"}
	if a.DB.Create(&first).Error != nil {
		t.Fatal("first import failed")
	}
	second := Channel{AccountID: &account.ID, UpstreamKeyID: &id, Name: "second"}
	if a.DB.Create(&second).Error == nil {
		t.Fatal("duplicate imported key accepted by database")
	}
	for range 2 {
		if a.DB.Create(&Channel{AccountID: &account.ID, Name: "manual"}).Error != nil {
			t.Fatal("legacy manual channels conflict")
		}
	}
}

func TestUpstreamKeyAvailability(t *testing.T) {
	now := time.Now()
	key, err := parseUpstreamKey(object{"id": 1, "status": 1, "unlimited_quota": true, "remain_quota": 0, "expired_time": -1}, "newapi", now)
	if err != nil || !key.Importable || key.ExpiresAt != nil {
		t.Fatal("unlimited non-expiring NewAPI key rejected")
	}
	key, err = parseUpstreamKey(object{"id": 1, "status": 1, "unlimited_quota": false, "remain_quota": 0}, "newapi", now)
	if err != nil || key.Importable {
		t.Fatal("exhausted NewAPI key accepted")
	}
}
