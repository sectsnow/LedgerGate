package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ledgergate/internal/backup"
)

func uploadIconForTest(t *testing.T, h http.Handler, id uint, cookie string, raw []byte, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(raw)
	_ = form.Close()
	req := httptest.NewRequest("PUT", fmt.Sprintf("/api/accounts/%d/icon", id), &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Cookie", cookie)
	if csrf {
		req.Header.Set("X-LedgerGate-Request", "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAccountIconURLDownloadAndValidation(t *testing.T) {
	a := testApp(t)
	manual := strings.ReplaceAll(testAccountSVG, "2463eb", "ff6600")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "8.8.8.8" {
			t.Error("image source Host changed", r.Host)
		}
		for _, name := range []string{"Authorization", "Cookie", "New-Api-User", "Referer"} {
			if r.Header.Get(name) != "" {
				t.Error("credentials leaked to image source", name)
			}
		}
		switch r.URL.Path {
		case "/logo.svg":
			if r.URL.Query().Get("sig") != "keep" {
				t.Error("image query was lost")
			}
			fmt.Fprint(w, manual)
		case "/private-redirect":
			http.Redirect(w, r, "http://169.254.169.254/secret", 302)
		case "/too-large":
			fmt.Fprint(w, strings.Repeat("x", accountIconDownloadLimit+1))
		case "/missing":
			w.WriteHeader(404)
		default:
			fmt.Fprint(w, "<html>not an image</html>")
		}
	}))
	defer upstream.Close()
	transport := a.client.Transport.(*http.Transport).Clone()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "8.8.8.8:80" {
			t.Error("unexpected destination", address)
			return nil, fmt.Errorf("unexpected destination")
		}
		return dial(ctx, network, upstream.Listener.Addr().String())
	}
	a.client.Transport = transport
	account := tokenTestAccount(t, a, "https://upstream.invalid")
	h := a.Router()
	cookie := loginForTest(t, h)
	path := fmt.Sprintf("/api/accounts/%d/icon", account.ID)
	input := jsonString(object{"url": "  http://8.8.8.8/logo.svg?sig=keep  "})
	if w := call(h, "PUT", path, input, ""); w.Code != 401 || calls.Load() != 0 {
		t.Fatal("URL download bypassed authentication")
	}
	req := httptest.NewRequest("PUT", path, strings.NewReader(input))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 || calls.Load() != 0 {
		t.Fatal("URL download bypassed CSRF protection")
	}
	if w := call(h, "PUT", path, input, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	icon, err := a.storedAccountIcon(account)
	if err != nil || !icon.Manual || string(icon.Body) != manual || icon.TypeOf != "image/svg+xml" {
		t.Fatal("downloaded image was not saved as a manual logo", err)
	}
	before := transferDBState(t, a)
	for _, target := range []string{
		"", "file:///etc/passwd", "https://user:secret@8.8.8.8/logo.svg", "http://127.0.0.1/private", "http://[::1]/private",
		"http://8.8.8.8/private-redirect", "http://8.8.8.8/not-an-image", "http://8.8.8.8/too-large", "http://8.8.8.8/missing",
		"https://example.com/" + strings.Repeat("x", 8192),
	} {
		if w := call(h, "PUT", path, jsonString(object{"url": target}), cookie); w.Code != 400 {
			t.Fatal("invalid URL or image accepted", target, w.Code)
		}
		if transferDBState(t, a) != before {
			t.Fatal("failed URL download changed the saved logo")
		}
	}
	if w := call(h, "PUT", path, "{", cookie); w.Code != 400 || transferDBState(t, a) != before {
		t.Fatal("malformed URL input changed the saved logo")
	}
	if w := call(h, "GET", path, "", cookie); w.Code != 200 || w.Body.String() != manual {
		t.Fatal("downloaded logo not served locally", w.Code)
	}
}

func TestAccountIconPersistenceUploadBackupAndReset(t *testing.T) {
	a := testApp(t)
	var offline atomic.Bool
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<link rel="icon" href="/logo.svg">`)
			return
		}
		fmt.Fprint(w, testAccountSVG)
	}))
	defer upstream.Close()
	account := tokenTestAccount(t, a, upstream.URL)
	h := a.Router()
	cookie := loginForTest(t, h)
	path := fmt.Sprintf("/api/accounts/%d/icon", account.ID)
	if w := call(h, "GET", path, "", cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	before := calls.Load()
	config := Config{DataDir: a.dataDir, AdminPassword: "test-password-12345"}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h = reopened.Router()
	cookie = loginForTest(t, h)
	offline.Store(true)
	if w := call(h, "GET", path, "", cookie); w.Code != 200 || w.Body.String() != testAccountSVG || calls.Load() != before {
		t.Fatal("restart required a network fetch for the saved logo", w.Code)
	}
	manual := strings.ReplaceAll(testAccountSVG, "2463eb", "ff0000")
	if w := uploadIconForTest(t, h, account.ID, "", []byte(manual), true); w.Code != 401 {
		t.Fatal("upload bypassed authentication", w.Code)
	}
	if w := uploadIconForTest(t, h, account.ID, cookie, []byte(manual), false); w.Code != 403 {
		t.Fatal("upload bypassed CSRF protection", w.Code)
	}
	if w := uploadIconForTest(t, h, account.ID, cookie, []byte(manual), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, invalid := range [][]byte{[]byte("<html>not an image</html>"), []byte("\x89PNG\r\n\x1a\n"), make([]byte, accountIconDownloadLimit+1)} {
		if w := uploadIconForTest(t, h, account.ID, cookie, invalid, true); w.Code != 400 {
			t.Fatal("invalid image accepted", w.Code)
		}
	}
	if err := reopened.saveAutomaticAccountIcon(account, []byte(testAccountSVG), "image/svg+xml"); err != nil {
		t.Fatal(err)
	}
	if w := call(h, "GET", path+"?refresh=1", "", cookie); w.Code != 200 || w.Body.String() != manual || calls.Load() != before {
		t.Fatal("automatic result replaced a manual logo", w.Code, w.Body.String())
	}
	w := call(h, "POST", "/api/backup", jsonString(object{"password": transferTestPassword}), cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := backup.Restore(w.Body.Bytes(), transferTestPassword, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(Config{DataDir: destination, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	rh := restored.Router()
	if w := call(rh, "GET", path, "", loginForTest(t, rh)); w.Code != 200 || w.Body.String() != manual {
		t.Fatal("full backup lost the saved logo", w.Code)
	}
	if w := call(h, "DELETE", path, "", cookie); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	offline.Store(false)
	if w := call(h, "GET", path, "", cookie); w.Code != 200 || w.Body.String() != testAccountSVG || calls.Load() == before {
		t.Fatal("reset did not restore automatic fetching", w.Code)
	}
	if w := call(h, "DELETE", fmt.Sprintf("/api/accounts/%d", account.ID), "", cookie); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := reopened.saveAutomaticAccountIcon(account, []byte(testAccountSVG), "image/svg+xml"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := reopened.DB.Model(&accountIconRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("deleted account left an orphan icon", count, err)
	}
}

func TestAccountIconTransferPreviewRemappingAndCompatibility(t *testing.T) {
	source, _ := transferFixture(t)
	icon := accountIconRecord{AccountID: 101, Origin: "https://upstream.invalid/", Body: []byte(testAccountSVG), TypeOf: "image/svg+xml", Manual: true}
	if err := source.DB.Create(&icon).Error; err != nil {
		t.Fatal(err)
	}
	raw, doc := exportTransferForTest(t, source, []string{"accounts"}, false)
	if doc.Version != 2 || len(doc.Accounts) != 1 || doc.Accounts[0].Icon == nil || !bytes.Equal(doc.Accounts[0].Icon.Body, icon.Body) || !doc.Accounts[0].Icon.Manual {
		t.Fatal("export did not include the logo without credentials")
	}
	target := testApp(t)
	if err := target.DB.Create(&Account{ID: 101, Name: "unrelated"}).Error; err != nil {
		t.Fatal(err)
	}
	h := target.Router()
	cookie := loginForTest(t, h)
	before := transferDBState(t, target)
	preview := transferPreviewForTest(t, h, cookie, raw, nil)
	if transferDBState(t, target) != before {
		t.Fatal("preview persisted account or logo changes")
	}
	w := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var account Account
	if err := target.DB.Where("name = ?", "source-account").First(&account).Error; err != nil || account.ID == 101 {
		t.Fatal("account ID was not remapped", account.ID, err)
	}
	path := fmt.Sprintf("/api/accounts/%d/icon", account.ID)
	if w := call(h, "GET", path, "", cookie); w.Code != 200 || w.Body.String() != testAccountSVG {
		t.Fatal("remapped imported logo unavailable offline", w.Code)
	}
	manual := strings.ReplaceAll(testAccountSVG, "2463eb", "ff0000")
	if w := uploadIconForTest(t, h, account.ID, cookie, []byte(manual), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	preview = transferPreviewForTest(t, h, cookie, raw, nil)
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(h, "GET", path, "", cookie); w.Body.String() != manual {
		t.Fatal("duplicate import overwrote a local logo")
	}
	if err := target.DB.Delete(&accountIconRecord{}, "account_id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	preview = transferPreviewForTest(t, h, cookie, raw, nil)
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(h, "GET", path, "", cookie); w.Body.String() != testAccountSVG {
		t.Fatal("duplicate import did not fill a missing logo")
	}
	doc.Version, doc.Accounts[0].Icon = 1, nil
	plain, _ := json.Marshal(doc)
	legacy, err := backup.EncryptData(plain, transferTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	preview = transferPreviewForTest(t, h, cookie, legacy, nil)
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/import", legacy, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 200 {
		t.Fatal("old v1 package rejected", w.Code, w.Body.String())
	}
	doc.Version = 2
	doc.Accounts[0].Icon = &transferAccountIcon{Body: []byte("<html>invalid</html>")}
	plain, _ = json.Marshal(doc)
	invalid, _ := backup.EncryptData(plain, transferTestPassword)
	if w := transferUploadForTest(t, h, cookie, "/api/data-transfer/preview", invalid, transferTestPassword, nil, ""); w.Code != 400 {
		t.Fatal("invalid imported logo accepted", w.Code)
	}
	doc.Accounts[0].Name = "rollback-account"
	doc.Accounts[0].Icon.Body = []byte(testAccountSVG)
	plain, _ = json.Marshal(doc)
	failing, _ := backup.EncryptData(plain, transferTestPassword)
	preview = transferPreviewForTest(t, h, cookie, failing, nil)
	before = transferDBState(t, target)
	if err := target.DB.Exec("CREATE TRIGGER reject_icon_import BEFORE INSERT ON account_icon_records BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	w = transferUploadForTest(t, h, cookie, "/api/data-transfer/import", failing, transferTestPassword, preview.Selected, preview.Token)
	if w.Code != 409 || transferDBState(t, target) != before {
		t.Fatal("failed logo import partially committed accounts", w.Code)
	}
}

func TestAccountIconFetchCannotReplaceManualUpload(t *testing.T) {
	a := testApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<link rel="icon" href="/logo.svg">`)
			return
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, testAccountSVG)
	}))
	defer upstream.Close()
	account := tokenTestAccount(t, a, upstream.URL)
	h := a.Router()
	cookie := loginForTest(t, h)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- call(h, "GET", fmt.Sprintf("/api/accounts/%d/icon", account.ID), "", cookie) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic fetch did not start")
	}
	manual := strings.ReplaceAll(testAccountSVG, "2463eb", "ff0000")
	if w := uploadIconForTest(t, h, account.ID, cookie, []byte(manual), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if released.CompareAndSwap(false, true) {
		close(release)
	}
	select {
	case w := <-result:
		if w.Code != 200 || w.Body.String() != manual {
			t.Fatal("in-flight automatic result replaced the uploaded logo", w.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("automatic request did not complete")
	}
}
