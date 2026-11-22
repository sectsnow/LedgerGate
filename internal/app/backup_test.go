package app

import (
	"os"
	"path/filepath"
	"testing"

	"ledgergate/internal/backup"
)

func TestEncryptedBackupRestoresCredentialsAndRefusesOverwrite(t *testing.T) {
	a := testApp(t)
	account := Account{Name: "backup-account", Kind: "sub2api", Token: a.seal("saved-upstream-token")}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "POST", "/api/backup", `{"password":"archive-password-123"}`, cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal("backup was not a file")
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := backup.Restore(response.Body.Bytes(), "wrong-password-123", destination); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("invalid backup wrote files")
	}
	if err := backup.Restore(response.Body.Bytes(), "archive-password-123", destination); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(Config{DataDir: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var stored Account
	if err := restored.DB.First(&stored, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if secret, err := restored.unseal(stored.Token); err != nil || secret != "saved-upstream-token" {
		t.Fatal("credential round trip failed")
	}
	if err := backup.Restore(response.Body.Bytes(), "archive-password-123", destination); err == nil {
		t.Fatal("restore overwrote existing data")
	}
	if result := call(h, "POST", "/api/backup", `{"password":"short"}`, cookie); result.Code != 400 {
		t.Fatal("short password accepted")
	}
}
