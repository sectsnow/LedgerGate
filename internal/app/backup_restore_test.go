package app

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgergate/internal/backup"
)

const restoreTestPassword = "backup-password-123"

func restoreArchiveForTest(t *testing.T) ([]byte, string) {
	t.Helper()
	source := testApp(t)
	source.envPassword = false
	if err := source.DB.Create(&Account{Name: "archived-account", Kind: "sub2api", Token: source.seal("archived-token")}).Error; err != nil {
		t.Fatal(err)
	}
	row := RequestLog{RequestID: "archived-request", Model: "archived-model", Status: 200, State: "completed"}
	if err := source.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.DB.Create(&RequestDetail{RequestLogID: row.ID, Payload: source.seal(jsonString(requestPayload{Available: true, ResponseBody: "data: {\"type\":\"response.completed\"}\n\n"}))}).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.DB.Model(&Setting{}).Where("id = 1").Update("request_history_migrated", false).Error; err != nil {
		t.Fatal(err)
	}
	h := source.Router()
	cookie := loginForTest(t, h)
	w := call(h, "PUT", "/api/admin/password", `{"current_password":"test-password-12345","new_password":"restored-password-123","confirm_password":"restored-password-123"}`, cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(h, "POST", "/api/login", `{"password":"restored-password-123"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie = "ledgergate_session=" + w.Result().Cookies()[0].Value
	w = call(h, "POST", "/api/backup", `{"password":"backup-password-123"}`, cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return w.Body.Bytes(), cookie
}

func restoreUploadForTest(t *testing.T, h http.Handler, cookie string, raw []byte, password, filename, confirm string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = form.WriteField("password", password)
	_ = form.WriteField("confirm", confirm)
	_ = form.Close()
	req := httptest.NewRequest("POST", "/api/backup/restore", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Cookie", cookie)
	if csrf {
		req.Header.Set("X-LedgerGate-Request", "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestWebBackupRestoreHistoryCredentialsAndSessions(t *testing.T) {
	raw, archivedCookie := restoreArchiveForTest(t)
	for _, environmentManaged := range []bool{false, true} {
		t.Run(fmt.Sprint(environmentManaged), func(t *testing.T) {
			a := testApp(t)
			a.envPassword = environmentManaged
			if err := a.DB.Create(&Account{Name: "original-account", Token: a.seal("original-token")}).Error; err != nil {
				t.Fatal(err)
			}
			oldKey, err := os.ReadFile(filepath.Join(a.dataDir, "master.key"))
			if err != nil {
				t.Fatal(err)
			}
			h := a.Router()
			cookie := loginForTest(t, h)
			a.health[1] = channelState{Until: time.Now().Add(time.Hour)}
			w := restoreUploadForTest(t, h, cookie, raw, restoreTestPassword, "backup.lgb", "恢复", true)
			if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"environment_managed":%t`, environmentManaged)) {
				t.Fatal(w.Code, w.Body.String())
			}
			var account Account
			if err := a.DB.First(&account).Error; err != nil || account.Name != "archived-account" {
				t.Fatal("restored account missing", err)
			}
			if value, err := a.unseal(account.Token); err != nil || value != "archived-token" {
				t.Fatal("restored credential cannot be decrypted", err)
			}
			var summary RequestDay
			if err := a.DB.First(&summary).Error; err != nil || summary.Requests != 1 {
				t.Fatal("legacy request statistics missing", err)
			}
			var details int64
			if err := a.DB.Model(&RequestDetail{}).Count(&details).Error; err != nil || details != 0 {
				t.Fatal("legacy bodies kept in live DB", err)
			}
			archives, err := filepath.Glob(filepath.Join(a.dataDir, "backups", "restore-*", "request-history", "request-history-*", "ledgergate.db"))
			if err != nil || len(archives) != 1 {
				t.Fatal("legacy request archive lost during restore", archives, err)
			}
			if len(a.health) != 0 || len(a.loginAttempts) != 0 {
				t.Fatal("old runtime state survived restore")
			}
			for _, staleCookie := range []string{cookie, archivedCookie} {
				if w := call(h, "GET", "/api/session", "", staleCookie); w.Code != 401 {
					t.Fatal("pre-restore session remained valid")
				}
			}
			password := "restored-password-123"
			if environmentManaged {
				password = "test-password-12345"
			}
			if w := call(h, "POST", "/api/login", jsonString(object{"password": password}), ""); w.Code != 200 {
				t.Fatal("restored password did not work", w.Code)
			}
			previous, err := filepath.Glob(filepath.Join(a.dataDir, "backups", "restore-*", "previous"))
			if err != nil || len(previous) != 1 {
				t.Fatal("original data was not retained", err)
			}
			key, err := os.ReadFile(filepath.Join(previous[0], "master.key"))
			if err != nil || !bytes.Equal(key, oldKey) {
				t.Fatal("original master key was not retained", err)
			}
			original, err := Open(Config{DataDir: previous[0]})
			if err != nil {
				t.Fatal("original data cannot be reopened", err)
			}
			if err := original.DB.First(&account).Error; err != nil || account.Name != "original-account" {
				t.Fatal("original database lost", err)
			}
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(Config{DataDir: a.dataDir})
			if err != nil {
				t.Fatal("restored data does not survive restart", err)
			}
			defer reopened.Close()
			if err := reopened.DB.First(&account).Error; err != nil || account.Name != "archived-account" {
				t.Fatal("restart reverted restored data", err)
			}
		})
	}
}

func TestWebBackupRestoreRejectsInvalidUploadsWithoutChangingData(t *testing.T) {
	raw, _ := restoreArchiveForTest(t)
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	keyBefore, _ := os.ReadFile(filepath.Join(a.dataDir, "master.key"))
	for _, test := range []struct {
		name, cookie, password, filename, confirm string
		csrf                                      bool
		raw                                       []byte
		status                                    int
	}{
		{"unauthenticated", "", restoreTestPassword, "backup.lgb", "恢复", true, raw, 401},
		{"missing csrf", cookie, restoreTestPassword, "backup.lgb", "恢复", false, raw, 403},
		{"missing confirmation", cookie, restoreTestPassword, "backup.lgb", "", true, raw, 400},
		{"wrong password", cookie, "wrong-password-123", "backup.lgb", "恢复", true, raw, 400},
		{"oversized password", cookie, strings.Repeat("x", 257), "backup.lgb", "恢复", true, raw, 400},
		{"wrong file type", cookie, restoreTestPassword, "configuration.lgd", "恢复", true, raw, 400},
		{"tampered file", cookie, restoreTestPassword, "backup.lgb", "恢复", true, append(append([]byte{}, raw[:len(raw)-1]...), raw[len(raw)-1]^1), 400},
		{"invalid file", cookie, restoreTestPassword, "backup.lgb", "恢复", true, []byte("invalid backup"), 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := restoreUploadForTest(t, h, test.cookie, test.raw, test.password, test.filename, test.confirm, test.csrf)
			if w.Code != test.status {
				t.Fatal(w.Code, w.Body.String())
			}
			keyAfter, _ := os.ReadFile(filepath.Join(a.dataDir, "master.key"))
			if !bytes.Equal(keyAfter, keyBefore) || call(h, "GET", "/api/session", "", cookie).Code != 200 {
				t.Fatal("rejected upload changed live data or session")
			}
		})
	}
}

func TestWebBackupRestoreWaitsForActiveRequest(t *testing.T) {
	raw, _ := restoreArchiveForTest(t)
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		chatResponse(w)
	}))
	defer func() {
		once.Do(func() { close(release) })
		upstream.Close()
	}()
	token := createGroup(t, h, cookie, []Channel{createChannel(t, h, cookie, "active", upstream.URL, "openai")}, "priority")
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		requestDone <- call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`, token)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("forwarding did not start")
	}
	restoreDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		restoreDone <- restoreUploadForTest(t, h, cookie, raw, restoreTestPassword, "backup.lgb", "恢复", true)
	}()
	select {
	case <-restoreDone:
		t.Fatal("restored while forwarding was still running")
	case <-time.After(30 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case w := <-requestDone:
		if w.Code != 200 {
			t.Fatal("active request was interrupted", w.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active request did not finish")
	}
	select {
	case w := <-restoreDone:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not finish after forwarding")
	}
}

func TestBackupRestoreRollsBackFailedSwitch(t *testing.T) {
	raw, _ := restoreArchiveForTest(t)
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	workspace := filepath.Join(a.dataDir, "backups", "restore-failure")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(workspace, "incoming")
	if err := backup.Restore(raw, restoreTestPassword, incoming); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(incoming, "master.key")); err != nil {
		t.Fatal(err)
	}
	a.runtimeMu.Lock()
	err := a.replaceData(incoming, filepath.Join(workspace, "previous"))
	a.runtimeMu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "已回滚") {
		t.Fatal("failed switch was not rolled back", err)
	}
	if w := call(h, "GET", "/api/session", "", cookie); w.Code != 200 {
		t.Fatal("original session lost during rollback", w.Code)
	}
	if w := call(h, "POST", "/api/login", `{"password":"test-password-12345"}`, ""); w.Code != 200 {
		t.Fatal("original password lost", w.Code)
	}
}

func TestBackupRestoreRecoversInterruptedSwitchOnStartup(t *testing.T) {
	for moved := 0; moved <= len(restoreFiles); moved++ {
		t.Run(fmt.Sprint(moved), func(t *testing.T) {
			dir := t.TempDir()
			a, err := Open(Config{DataDir: dir, AdminPassword: "test-password-12345"})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.DB.Create(&Account{Name: "original-account", Token: a.seal("original-token")}).Error; err != nil {
				t.Fatal(err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			previous := filepath.Join(dir, "backups", "restore-interrupted", "previous")
			if err := os.MkdirAll(previous, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range restoreFiles[:moved] {
				if err := os.Rename(filepath.Join(dir, name), filepath.Join(previous, name)); err != nil {
					t.Fatal(err)
				}
			}
			if moved > 0 {
				for _, name := range []string{"ledgergate.db", "ledgergate.db-wal", "ledgergate.db-shm"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("interrupted replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := saveAdminPassword(filepath.Join(dir, restoreMarker), filepath.Join("backups", "restore-interrupted", "previous")); err != nil {
				t.Fatal(err)
			}
			recovered, err := Open(Config{DataDir: dir})
			if err != nil {
				t.Fatal("startup failed to recover original data", err)
			}
			defer recovered.Close()
			var account Account
			if err := recovered.DB.First(&account).Error; err != nil || account.Name != "original-account" {
				t.Fatal("original account lost", err)
			}
			if token, err := recovered.unseal(account.Token); err != nil || token != "original-token" {
				t.Fatal("original master key lost", err)
			}
			if _, err := os.Stat(filepath.Join(dir, restoreMarker)); !os.IsNotExist(err) {
				t.Fatal("pending restore marker not cleared", err)
			}
		})
	}
}
