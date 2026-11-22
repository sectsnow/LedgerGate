package app

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgergate/internal/backup"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func passwordTestAdmin(t *testing.T) (*App, string) {
	t.Helper()
	a, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	w := call(a.Router(), "POST", "/api/setup", `{"password":"old-pass","confirm_password":"old-pass"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return a, passwordTestCookie(t, w)
}

func passwordTestCookie(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "ledgergate_session" && cookie.MaxAge > 0 {
			return cookie.Name + "=" + cookie.Value
		}
	}
	t.Fatal("missing administrator session")
	return ""
}

func passwordChangeBody(current, next string) string {
	return jsonString(object{"current_password": current, "new_password": next, "confirm_password": next})
}

func TestChangeAdminPasswordRevokesSessionsPersistsAndRestores(t *testing.T) {
	a, firstCookie := passwordTestAdmin(t)
	h := a.Router()
	second := call(h, "POST", "/api/login", `{"password":"old-pass"}`, "")
	if second.Code != 200 {
		t.Fatal(second.Body.String())
	}
	secondCookie := passwordTestCookie(t, second)
	masterPath := filepath.Join(a.dataDir, "master.key")
	master, _ := os.ReadFile(masterPath)
	account := Account{Name: "encrypted", Token: a.seal("upstream-secret")}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	w := call(h, "PUT", "/api/admin/password", passwordChangeBody("old-pass", "new-pass"), firstCookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, cookie := range []string{firstCookie, secondCookie} {
		if response := call(h, "GET", "/api/session", "", cookie); response.Code != 401 {
			t.Fatal("old session survived password change")
		}
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "ledgergate_session" && (!cookie.HttpOnly || cookie.MaxAge != -1 || cookie.SameSite != http.SameSiteStrictMode) {
			t.Fatal("old browser cookie was not cleared securely")
		}
	}
	if response := call(h, "POST", "/api/login", `{"password":"old-pass"}`, ""); response.Code != 401 {
		t.Fatal("old password remained valid")
	}
	logged := call(h, "POST", "/api/login", `{"password":"new-pass"}`, "")
	if logged.Code != 200 {
		t.Fatal("new eight-byte password cannot log in", logged.Body.String())
	}
	passwordPath := filepath.Join(a.dataDir, "admin-password")
	if stored, err := os.ReadFile(passwordPath); err != nil || string(stored) != "new-pass\n" {
		t.Fatal("new password was not saved")
	}
	if stat, err := os.Stat(passwordPath); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("saved password permissions are not private")
	}
	if current, _ := os.ReadFile(masterPath); !bytes.Equal(master, current) {
		t.Fatal("changing administrator password replaced the credential encryption key")
	}
	archive := call(h, "POST", "/api/backup", `{"password":"backup-password-123"}`, passwordTestCookie(t, logged))
	if archive.Code != 200 {
		t.Fatal(archive.Body.String())
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := backup.Restore(archive.Body.Bytes(), "backup-password-123", restoredDir); err != nil {
		t.Fatal("backup rejected eight-byte administrator password", err)
	}
	restored, err := Open(Config{DataDir: restoredDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if response := call(restored.Router(), "POST", "/api/login", `{"password":"new-pass"}`, ""); response.Code != 200 {
		t.Fatal("restored password cannot log in")
	}
	var saved Account
	if err := restored.DB.First(&saved, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if token, err := restored.unseal(saved.Token); err != nil || token != "upstream-secret" {
		t.Fatal("upstream credential was damaged")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{DataDir: a.dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if response := call(reopened.Router(), "POST", "/api/login", `{"password":"new-pass"}`, ""); response.Code != 200 {
		t.Fatal("restarted service lost the password change")
	}
}

func TestAdminPasswordEightBytePolicyAndStartupConfiguration(t *testing.T) {
	for _, password := range []string{"12345678", "密钥八", strings.Repeat("a", 72)} {
		t.Run(fmt.Sprintf("valid-%d-bytes", len(password)), func(t *testing.T) {
			if err := validateAdminPassword(password); err != nil {
				t.Fatal(err)
			}
			for _, env := range []bool{false, true} {
				dir := t.TempDir()
				config := Config{DataDir: dir}
				if env {
					config.AdminPassword = password
				} else if err := os.WriteFile(filepath.Join(dir, "admin-password"), []byte(password+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				loaded, hash, err := loadAdminPassword(config, false)
				if err != nil || loaded != password || bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
					t.Fatal("valid file/environment password rejected", err)
				}
			}
		})
	}
	for _, password := range []string{"", "1234567", strings.Repeat("a", 73), strings.Repeat("密", 25), " old-pass", "old-pass "} {
		if err := validateAdminPassword(password); err == nil {
			t.Fatal("invalid administrator password accepted")
		}
	}
}

func TestChangeAdminPasswordRejectsInvalidRequestsAndLimitsAttempts(t *testing.T) {
	a, cookie := passwordTestAdmin(t)
	h := a.Router()
	if w := call(h, "PUT", "/api/admin/password", passwordChangeBody("old-pass", "new-pass"), ""); w.Code != 401 {
		t.Fatal("unauthenticated change accepted")
	}
	req := httptest.NewRequest("PUT", "/api/admin/password", strings.NewReader(passwordChangeBody("old-pass", "new-pass")))
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("password change accepted without CSRF header")
	}
	for _, body := range []string{
		passwordChangeBody("old-pass", "1234567"),
		passwordChangeBody("old-pass", strings.Repeat("x", 73)),
		passwordChangeBody("old-pass", " new-pass"),
		`{"current_password":"old-pass","new_password":"new-pass","confirm_password":"different"}`,
		passwordChangeBody("wrong-pass", "new-pass"),
	} {
		if result := call(h, "PUT", "/api/admin/password", body, cookie); result.Code != 400 {
			t.Fatal("invalid change accepted", result.Code, result.Body.String())
		}
	}
	if result := call(h, "PUT", "/api/admin/password", passwordChangeBody("wrong-pass", "new-pass"), cookie); result.Code != 429 {
		t.Fatal("password-change guessing was not limited")
	}
	stored, err := os.ReadFile(filepath.Join(a.dataDir, "admin-password"))
	if err != nil || string(stored) != "old-pass\n" || call(h, "GET", "/api/session", "", cookie).Code != 200 {
		t.Fatal("invalid change affected password or session")
	}
	if response := call(h, "GET", "/api/admin/password", "", cookie); response.Code != 200 || strings.Contains(response.Body.String(), "old-pass") || !strings.Contains(response.Body.String(), `"min_bytes":8`) {
		t.Fatal("password status was incorrect or exposed credentials")
	}
}

func TestEnvironmentManagedAdminPasswordCannotBeChangedInWeb(t *testing.T) {
	a, err := Open(Config{DataDir: t.TempDir(), AdminPassword: "env-pass"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Router()
	logged := call(h, "POST", "/api/login", `{"password":"env-pass"}`, "")
	if logged.Code != 200 {
		t.Fatal(logged.Body.String())
	}
	cookie := passwordTestCookie(t, logged)
	if status := call(h, "GET", "/api/admin/password", "", cookie); status.Code != 200 || !strings.Contains(status.Body.String(), `"environment_managed":true`) {
		t.Fatal("environment-managed status was not disclosed")
	}
	if changed := call(h, "PUT", "/api/admin/password", passwordChangeBody("env-pass", "new-pass"), cookie); changed.Code != 409 {
		t.Fatal("web change would revert to startup password on restart")
	}
	if stored, _ := os.ReadFile(filepath.Join(a.dataDir, "admin-password")); string(stored) != "env-pass\n" {
		t.Fatal("environment password file was overwritten")
	}
}

func TestConcurrentOldPasswordLoginDoesNotSurviveChange(t *testing.T) {
	a, cookie := passwordTestAdmin(t)
	h := a.Router()
	var workers sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		workers.Go(func() { <-start; results <- call(h, "POST", "/api/login", `{"password":"old-pass"}`, "") })
	}
	changed := make(chan *httptest.ResponseRecorder, 1)
	workers.Go(func() {
		<-start
		changed <- call(h, "PUT", "/api/admin/password", passwordChangeBody("old-pass", "new-pass"), cookie)
	})
	archived := make(chan *httptest.ResponseRecorder, 1)
	workers.Go(func() {
		<-start
		archived <- call(h, "POST", "/api/backup", `{"password":"backup-password-123"}`, cookie)
	})
	close(start)
	workers.Wait()
	close(results)
	if result := <-changed; result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	for result := range results {
		if result.Code == 200 {
			if call(h, "GET", "/api/session", "", passwordTestCookie(t, result)).Code != 401 {
				t.Fatal("old password created a valid session after password change")
			}
		} else if result.Code != 401 && result.Code != 429 {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	if archive := <-archived; archive.Code == 200 {
		dir := filepath.Join(t.TempDir(), "concurrent-backup")
		if err := backup.Restore(archive.Body.Bytes(), "backup-password-123", dir); err != nil {
			t.Fatal("concurrent backup was damaged", err)
		}
		stored, err := os.ReadFile(filepath.Join(dir, "admin-password"))
		if err != nil || string(stored) != "old-pass\n" && string(stored) != "new-pass\n" {
			t.Fatal("concurrent backup did not capture a complete password")
		}
	} else if archive.Code != 401 {
		t.Fatal("concurrent backup failed unexpectedly", archive.Code, archive.Body.String())
	}
}

func TestChangeAdminPasswordWriteFailuresPreserveOldPassword(t *testing.T) {
	for _, failure := range []string{"session-delete", "password-file"} {
		t.Run(failure, func(t *testing.T) {
			a, cookie := passwordTestAdmin(t)
			if failure == "session-delete" {
				if err := a.DB.Exec("CREATE TRIGGER reject_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'test failure'); END").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := a.DB.Callback().Delete().After("gorm:delete").Register("test:password-file-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "sessions" {
						path := filepath.Join(a.dataDir, "admin-password")
						if err := os.Remove(path); err != nil {
							t.Error(err)
							return
						}
						if err := os.Mkdir(path, 0700); err != nil {
							t.Error(err)
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			w := call(a.Router(), "PUT", "/api/admin/password", passwordChangeBody("old-pass", "new-pass"), cookie)
			if w.Code != 500 || bcrypt.CompareHashAndPassword(a.passwordHash, []byte("old-pass")) != nil || a.adminPassword != "old-pass" {
				t.Fatal("failed write changed runtime password", w.Code, w.Body.String())
			}
			if failure == "session-delete" {
				if stored, _ := os.ReadFile(filepath.Join(a.dataDir, "admin-password")); string(stored) != "old-pass\n" {
					t.Fatal("session cleanup failure changed password file")
				}
			} else if !strings.Contains(w.Body.String(), `"reauthenticate":true`) {
				t.Fatal("save failure did not explain required login")
			}
		})
	}
}

func TestSlowPasswordChangeBodyDoesNotBlockAuthenticationStatus(t *testing.T) {
	a, cookie := passwordTestAdmin(t)
	h := a.Router()
	reader, writer := io.Pipe()
	defer writer.Close()
	req := httptest.NewRequest("PUT", "/api/admin/password", reader)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-LedgerGate-Request", "1")
	done := make(chan struct{})
	go func() { h.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	_, _ = writer.Write([]byte(`{"current_password":`))
	status := make(chan int, 1)
	go func() { status <- call(h, "GET", "/api/setup", "", "").Code }()
	select {
	case code := <-status:
		if code != 200 {
			t.Error("authentication status unavailable")
		}
	case <-time.After(2 * time.Second):
		t.Error("incomplete request body blocked authentication")
	}
	writer.Close()
	<-done
}
