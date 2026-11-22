package app

import (
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
)

func TestAdminSetupPersistsAndRestoresBackup(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !a.setupRequired() {
		t.Fatal("new deployment did not require setup")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	status := call(h, "GET", "/api/setup", "", "")
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"setup_required":true`) || status.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("pending setup did not survive restart", status.Body.String())
	}
	if result := call(h, "GET", "/api/accounts", "", ""); result.Code != 401 {
		t.Fatal("uninitialized admin API was accessible")
	}
	if result := call(h, "POST", "/api/login", `{"password":"test-password-12345"}`, ""); result.Code != 409 {
		t.Fatal("login did not require setup")
	}
	result := call(h, "POST", "/api/setup", `{"password":"test-password-12345","confirm_password":"test-password-12345"}`, "")
	if result.Code != 200 {
		t.Fatalf("setup %d: %s", result.Code, result.Body.String())
	}
	cookies := result.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("setup did not create a protected session")
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	if response := call(h, "GET", "/api/accounts", "", cookie); response.Code != 200 {
		t.Fatal("setup did not log in")
	}
	if response := call(h, "POST", "/api/setup", `{"password":"overwrite-password","confirm_password":"overwrite-password"}`, cookie); response.Code != 409 {
		t.Fatal("repeated setup overwrote credentials")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "admin-password"))
	if err != nil || string(stored) != "test-password-12345\n" {
		t.Fatal("administrator password was not persisted")
	}
	stat, err := os.Stat(filepath.Join(dir, "admin-password"))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("password permissions were not private")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.setupRequired() {
		t.Fatal("restart reopened setup")
	}
	h = a.Router()
	cookie = loginForTest(t, h)
	archive := call(h, "POST", "/api/backup", `{"password":"backup-password-123"}`, cookie)
	if archive.Code != 200 {
		t.Fatal(archive.Body.String())
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := backup.Restore(archive.Body.Bytes(), "backup-password-123", restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(Config{DataDir: restoredDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.setupRequired() {
		t.Fatal("backup restore reopened setup")
	}
	loginForTest(t, restored.Router())
}

func TestAdminSetupValidationAndCSRF(t *testing.T) {
	a, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Router()
	request := httptest.NewRequest("POST", "/api/setup", strings.NewReader(`{"password":"valid-password-123","confirm_password":"valid-password-123"}`))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("setup accepted a request without its CSRF header")
	}
	for _, input := range []struct {
		name, password, confirmation string
	}{
		{"empty", "", ""},
		{"short", "1234567", "1234567"},
		{"long", strings.Repeat("a", 73), strings.Repeat("a", 73)},
		{"unicode-too-long", strings.Repeat("密", 25), strings.Repeat("密", 25)},
		{"mismatch", "valid-password-123", "different-password"},
		{"no-confirmation", "valid-password-123", ""},
		{"leading-space", " valid-password-123", " valid-password-123"},
		{"trailing-space", "valid-password-123 ", "valid-password-123 "},
	} {
		t.Run(input.name, func(t *testing.T) {
			body := jsonString(object{"password": input.password, "confirm_password": input.confirmation})
			if result := call(h, "POST", "/api/setup", body, ""); result.Code != 400 || !a.setupRequired() {
				t.Fatalf("invalid setup %d: %s", result.Code, result.Body.String())
			}
		})
	}
	for _, body := range []string{"{", strings.Repeat("x", 4097)} {
		if result := call(h, "POST", "/api/setup", body, ""); result.Code != 400 || !a.setupRequired() {
			t.Fatal("invalid JSON or oversized body changed the password")
		}
	}
	if result := call(h, "POST", "/api/setup", `{"password":"12345678","confirm_password":"12345678"}`, ""); result.Code != 200 {
		t.Fatal("minimum-length password rejected", result.Body.String())
	}
}

func TestConcurrentAdminSetupSetsPasswordOnce(t *testing.T) {
	a, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Router()
	var workers sync.WaitGroup
	start := make(chan struct{})
	winners := make(chan string, 8)
	for index := range 8 {
		workers.Go(func() {
			<-start
			password := fmt.Sprintf("concurrent-password-%d", index)
			body := jsonString(object{"password": password, "confirm_password": password})
			result := call(h, "POST", "/api/setup", body, "")
			if result.Code == 200 {
				winners <- password
			} else if result.Code != 409 {
				t.Errorf("concurrent setup %d: %s", result.Code, result.Body.String())
			}
		})
		workers.Go(func() {
			<-start
			call(h, "GET", "/api/setup", "", "")
			call(h, "GET", "/api/session", "", "")
		})
	}
	close(start)
	workers.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("setup succeeded %d times", len(winners))
	}
	password := <-winners
	stored, err := os.ReadFile(filepath.Join(a.dataDir, "admin-password"))
	if err != nil || string(stored) != password+"\n" {
		t.Fatal("winning password was overwritten")
	}
	if result := call(h, "POST", "/api/login", jsonString(object{"password": password}), ""); result.Code != 200 {
		t.Fatal("winning password cannot log in")
	}
}

func TestAdminSetupSlowBodyDoesNotBlockStatus(t *testing.T) {
	a, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Router()
	reader, writer := io.Pipe()
	defer writer.Close()
	request := httptest.NewRequest("POST", "/api/setup", reader)
	request.Header.Set("X-LedgerGate-Request", "1")
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	if _, err := writer.Write([]byte(`{"password":`)); err != nil {
		t.Fatal(err)
	}
	status := make(chan int, 1)
	go func() { status <- call(h, "GET", "/api/setup", "", "").Code }()
	select {
	case code := <-status:
		if code != 200 {
			t.Fatal("status unavailable while setup body is incomplete")
		}
	case <-time.After(2 * time.Second):
		t.Error("incomplete setup body blocked status")
	}
	writer.Close()
	<-done
}

func TestExistingAdminPasswordAndEnvironmentSkipSetup(t *testing.T) {
	for _, mode := range []string{"password-file", "environment", "environment-overrides-file", "environment-completes-pending"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			config := Config{DataDir: dir}
			if mode == "password-file" || mode == "environment-overrides-file" {
				if err := os.WriteFile(filepath.Join(dir, "admin-password"), []byte("existing-password-123\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "environment-completes-pending" {
				pending, err := Open(config)
				if err != nil {
					t.Fatal(err)
				}
				pending.Close()
			}
			password := "existing-password-123"
			if mode != "password-file" {
				config.AdminPassword = "environment-password-123"
				password = config.AdminPassword
			}
			a, err := Open(config)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if a.setupRequired() {
				t.Fatal("configured deployment reopened setup")
			}
			h := a.Router()
			if result := call(h, "POST", "/api/login", jsonString(object{"password": password}), ""); result.Code != 200 {
				t.Fatal("configured password cannot log in")
			}
			if result := call(h, "POST", "/api/setup", `{"password":"replacement-password","confirm_password":"replacement-password"}`, ""); result.Code != 409 {
				t.Fatal("configured password can be replaced without authentication")
			}
			if mode == "environment-overrides-file" {
				stored, _ := os.ReadFile(filepath.Join(dir, "admin-password"))
				if string(stored) != "existing-password-123\n" {
					t.Fatal("environment override replaced the existing password file")
				}
			}
		})
	}
}

func TestMissingOrDamagedAdminPasswordDoesNotReopenSetup(t *testing.T) {
	for _, damage := range []string{"missing", "empty", "short"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			a, err := Open(Config{DataDir: dir, AdminPassword: "existing-password-123"})
			if err != nil {
				t.Fatal(err)
			}
			a.Close()
			path := filepath.Join(dir, "admin-password")
			if damage == "missing" {
				err = os.Remove(path)
			} else {
				contents := ""
				if damage == "short" {
					contents = "short"
				}
				err = os.WriteFile(path, []byte(contents), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(Config{DataDir: dir}); err == nil {
				reopened.Close()
				t.Fatal("missing or damaged password reopened setup")
			}
		})
	}
}
