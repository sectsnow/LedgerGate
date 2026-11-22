package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"gorm.io/gorm"
)

type creationUpstream struct {
	t              *testing.T
	kind           string
	mu             sync.Mutex
	name           string
	posts          int
	created        int
	reject         bool
	malformed      bool
	revealFails    bool
	maskedDetail   bool
	wrongGroup     bool
	unknownFailure bool
	block          chan struct{}
	entered        chan struct{}
}

func (f *creationUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/chat/completions" {
		if r.Header.Get("Authorization") != "Bearer sk-created-secret" {
			f.t.Error("generated key was not used by gateway")
		}
		chatResponse(w)
		return
	}
	if r.Header.Get("Authorization") != "Bearer access-0" {
		f.t.Error("wrong account credential")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/keys" || r.URL.Path == "/api/token/") {
		f.posts++
		if f.unknownFailure {
			w.WriteHeader(503)
			return
		}
		if f.reject {
			w.WriteHeader(403)
			return
		}
		var payload object
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			f.t.Error("invalid create payload")
		}
		f.name = str(payload["name"])
		if f.name == "" || len(f.name) > 50 || !utf8.ValidString(f.name) {
			f.t.Error("invalid upstream key name")
		}
		if f.kind == "sub2api" {
			if integer(payload["group_id"]) != 7 {
				f.t.Error("wrong Sub2API group")
			}
		} else if payload["group"] != "pro" || payload["unlimited_quota"] != true || integer(payload["expired_time"]) != -1 {
			f.t.Error("wrong NewAPI group or quota configuration")
		}
		f.created++
		if f.entered != nil {
			close(f.entered)
			<-f.block
			f.entered = nil
		}
		if f.malformed {
			fmt.Fprint(w, "not-json")
			return
		}
		if f.kind == "newapi" {
			fmt.Fprint(w, `{"success":true,"message":""}`)
			return
		}
		fmt.Fprintf(w, `{"data":{"id":101,"name":%q,"status":"active","group_id":7,"key":"sk-created-secret"}}`, f.name)
		return
	}
	switch r.URL.Path {
	case "/api/v1/groups/available":
		fmt.Fprint(w, `{"data":[{"id":7,"name":"上游分组","rate_multiplier":0.12},{"id":8,"name":"未导入密钥的分组","rate_multiplier":0.3}]}`)
	case "/api/v1/groups/rates":
		fmt.Fprint(w, `{"data":{}}`)
	case "/api/user/self/groups":
		fmt.Fprint(w, `{"data":{"pro":{"ratio":0.2,"base_ratio":0.3},"spare":{"ratio":0.4}}}`)
	case "/api/v1/keys", "/api/token/":
		if f.created == 0 {
			fmt.Fprint(w, `{"data":{"total":0,"items":[]}}`)
			return
		}
		fmt.Fprintf(w, `{"data":{"total":1,"items":[{"id":101,"name":%q}]}}`, f.name)
	case "/api/v1/keys/101":
		secret := "sk-created-secret"
		if f.maskedDetail {
			secret = "sk-***"
		}
		group := 7
		if f.wrongGroup {
			group = 8
		}
		fmt.Fprintf(w, `{"data":{"id":101,"name":%q,"status":"active","group_id":%d,"group":{"name":"上游分组"},"key":%q}}`, f.name, group, secret)
	case "/api/token/101":
		group := "pro"
		if f.wrongGroup {
			group = "spare"
		}
		fmt.Fprintf(w, `{"data":{"id":101,"name":%q,"status":1,"group":%q,"key":"sk-***","expired_time":-1,"unlimited_quota":true}}`, f.name, group)
	case "/api/token/101/key":
		if f.revealFails {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"data":{"key":"sk-created-secret"}}`)
	default:
		w.WriteHeader(404)
	}
}

func creationFixture(t *testing.T, kind string) (*App, *creationUpstream, http.Handler, string, channelCreateInput, string) {
	t.Helper()
	a := testApp(t)
	upstream := &creationUpstream{t: t, kind: kind}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	account := tokenTestAccount(t, a, server.URL)
	if err := a.DB.Model(&account).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	group := "7"
	if kind == "newapi" {
		if err := a.DB.Model(&account).Updates(map[string]any{"kind": kind, "refresh_token": "", "user_id": "9"}).Error; err != nil {
			t.Fatal(err)
		}
		group = "pro"
	}
	h := a.Router()
	input := channelCreateInput{RequestID: "aabbccddeeff00112233445566778899", Name: "本地渠道名称", GroupID: group, Protocols: []string{"openai", "responses"}, Enabled: true, TimeoutSeconds: 120, CooldownSeconds: 60}
	return a, upstream, h, loginForTest(t, h), input, server.URL
}

func TestCreateUpstreamChannel(t *testing.T) {
	for _, kind := range []string{"sub2api", "newapi"} {
		t.Run(kind, func(t *testing.T) {
			a, upstream, h, cookie, input, base := creationFixture(t, kind)
			groups := call(h, "GET", "/api/accounts/1/upstream-groups", "", cookie)
			var choices []upstreamRate
			if groups.Code != 200 || json.Unmarshal(groups.Body.Bytes(), &choices) != nil || len(choices) != 2 {
				t.Fatal("group selection must include groups with no local channels")
			}
			response := call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie)
			var channel Channel
			if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &channel) != nil {
				t.Fatalf("create failed %d %s", response.Code, response.Body.String())
			}
			if channel.Name != input.Name || channel.BaseURL != base || channel.UpstreamKeyID == nil || *channel.UpstreamKeyID != 101 || channel.AccountID == nil || *channel.AccountID != 1 || !channel.Enabled || len(channel.Protocols) != 2 {
				t.Fatal("created channel configuration incorrect")
			}
			var stored Channel
			if err := a.DB.First(&stored, channel.ID).Error; err != nil {
				t.Fatal(err)
			}
			secret, err := a.unseal(stored.APIKey)
			if err != nil || secret != "sk-created-secret" || stored.APIKey == secret {
				t.Fatal("created key not encrypted")
			}
			var account Account
			a.DB.First(&account, 1)
			rate := readRateSnapshot(account.Rates).Channels[channel.ID]
			want := 0.12
			if kind == "newapi" {
				want = 0.2
			}
			if rate.Multiplier == nil || *rate.Multiplier != want || rate.Identity != channelRateIdentity(stored) {
				t.Fatal("new channel has no immediate rate association")
			}
			if strings.Contains(response.Body.String(), "sk-created-secret") || strings.Contains(groups.Body.String(), "access-0") {
				t.Fatal("credential exposed in response")
			}
			response = call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie)
			if response.Code != 200 || upstream.created != 1 || upstream.posts != 1 {
				t.Fatal("retry generated another upstream key")
			}
			input.Name = "changed request"
			if response := call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie); response.Code != 409 {
				t.Fatal("request ID reused for different parameters")
			}
			token := createGroup(t, h, cookie, []Channel{stored}, "priority")
			if response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`, token); response.Code != 200 {
				t.Fatal("generated key cannot forward", response.Body.String())
			}
		})
	}
}

func TestCreateUpstreamChannelRecovery(t *testing.T) {
	for _, scenario := range []string{"local_save", "unknown_200_response", "reveal_failure", "masked_sub2api_detail", "rejected"} {
		t.Run(scenario, func(t *testing.T) {
			kind := "sub2api"
			if scenario == "reveal_failure" {
				kind = "newapi"
			}
			a, upstream, h, cookie, input, _ := creationFixture(t, kind)
			upstream.malformed = scenario == "unknown_200_response"
			upstream.revealFails = scenario == "reveal_failure"
			upstream.maskedDetail = scenario == "masked_sub2api_detail"
			upstream.reject = scenario == "rejected"
			if scenario == "local_save" {
				if err := a.DB.Callback().Create().Before("gorm:create").Register("test:fail_created_channel", func(tx *gorm.DB) {
					if _, ok := tx.Statement.Dest.(*Channel); ok {
						tx.AddError(errors.New("injected local storage failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			response := call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie)
			if scenario == "masked_sub2api_detail" {
				if response.Code != 201 {
					t.Fatal("creation response key should recover a masked detail", response.Body.String())
				}
				return
			}
			if response.Code < 400 || strings.Contains(response.Body.String(), "sk-created-secret") {
				t.Fatal("unsafe or ignored create failure")
			}
			var count int64
			a.DB.Model(&Channel{}).Count(&count)
			if count != 0 {
				t.Fatal("partially persisted local channel")
			}
			var job channelCreation
			if err := a.DB.First(&job, "request_id = ?", input.RequestID).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "local_save" && (job.Secret == "" || job.Secret == "sk-created-secret") {
				t.Fatal("recovery key must be encrypted")
			}
			upstream.malformed, upstream.revealFails, upstream.reject = false, false, false
			if scenario == "local_save" {
				a.DB.Callback().Create().Remove("test:fail_created_channel")
			}
			response = call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), cookie)
			if response.Code != 201 || upstream.created != 1 {
				t.Fatalf("retry did not recover same key: %d %s", response.Code, response.Body.String())
			}
			wantPosts := 1
			if scenario == "rejected" {
				wantPosts = 2
			}
			if upstream.posts != wantPosts {
				t.Fatal("unsafe retry of uncertain creation")
			}
		})
	}
}

func TestCreateChannelValidationAndLock(t *testing.T) {
	a, upstream, h, cookie, input, _ := creationFixture(t, "sub2api")
	path := "/api/accounts/1/create-channel"
	if response := call(h, "POST", path, jsonString(input), ""); response.Code != 401 {
		t.Fatal("creation exposed without admin session")
	}
	for _, field := range []string{"request_id", "name", "group_id", "protocols", "timeout_seconds", "cooldown_seconds"} {
		var body object
		_ = json.Unmarshal([]byte(jsonString(input)), &body)
		delete(body, field)
		if response := call(h, "POST", path, jsonString(body), cookie); response.Code != 400 {
			t.Fatalf("missing %s accepted", field)
		}
	}
	input.GroupID = "999"
	if response := call(h, "POST", path, jsonString(input), cookie); response.Code != 400 {
		t.Fatal("unavailable upstream group accepted")
	}
	input.GroupID = "7"
	input.Protocols = []string{"openai", "openai"}
	if response := call(h, "POST", path, jsonString(input), cookie); response.Code != 400 {
		t.Fatal("duplicate protocol accepted")
	}
	input.Protocols = []string{"openai"}
	if !a.claimAccount(1) {
		t.Fatal("failed to reserve account")
	}
	if response := call(h, "POST", path, jsonString(input), cookie); response.Code != 409 {
		t.Fatal("creation raced with monitor/key operation")
	}
	a.releaseAccount(1)
	if upstream.posts != 0 {
		t.Fatal("invalid requests generated upstream keys")
	}
	upstream.wrongGroup = true
	if response := call(h, "POST", path, jsonString(input), cookie); response.Code != 409 {
		t.Fatal("different upstream group silently accepted")
	}
	var count int64
	a.DB.Model(&Channel{}).Count(&count)
	if count != 0 {
		t.Fatal("wrong upstream group was persisted")
	}
}

func TestCreateChannelProgressSurvivesReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	a, err := Open(Config{DataDir: dir, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	upstream := &creationUpstream{t: t, kind: "newapi", revealFails: true}
	server := httptest.NewServer(upstream)
	defer server.Close()
	account := tokenTestAccount(t, a, server.URL)
	a.DB.Model(&account).Updates(map[string]any{"kind": "newapi", "refresh_token": ""})
	input := channelCreateInput{RequestID: strings.Repeat("1", 32), Name: strings.Repeat("中文", 12), GroupID: "pro", Protocols: []string{"openai"}, Enabled: true, TimeoutSeconds: 120, CooldownSeconds: 60}
	h := a.Router()
	response := call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), loginForTest(t, h))
	if response.Code != 502 {
		t.Fatal("expected reveal failure")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(Config{DataDir: dir, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	h = a.Router()
	upstream.revealFails = false
	response = call(h, "POST", "/api/accounts/1/create-channel", jsonString(input), loginForTest(t, h))
	if response.Code != 201 || upstream.posts != 1 || upstream.created != 1 {
		t.Fatal("reopened process repeated upstream creation", strconv.Itoa(response.Code))
	}
}

func TestUncertainCreationDoesNotRepeatPost(t *testing.T) {
	_, upstream, h, cookie, input, _ := creationFixture(t, "sub2api")
	upstream.unknownFailure = true
	path := "/api/accounts/1/create-channel"
	if response := call(h, "POST", path, jsonString(input), cookie); response.Code != 502 {
		t.Fatal("expected unknown upstream failure")
	}
	upstream.unknownFailure = false
	if response := call(h, "POST", path, jsonString(input), cookie); response.Code != 409 {
		t.Fatal("unknown creation should query rather than create again")
	}
	if upstream.posts != 1 || upstream.created != 0 {
		t.Fatal("uncertain creation was repeated")
	}
}
