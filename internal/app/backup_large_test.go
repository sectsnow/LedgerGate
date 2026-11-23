package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
	"ledgergate/internal/backup"
)

func TestBackupReportsTheActualMissingFile(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	key := filepath.Join(a.dataDir, "master.key")
	if err := os.Rename(key, key+".saved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(key+".saved", key)
	w := call(h, "POST", "/api/backup", `{"password":"backup-password-123"}`, cookie)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "master.key") || strings.Contains(w.Body.String(), "256 MiB") || strings.Contains(w.Body.String(), "超过") {
		t.Fatal("missing file incorrectly reported as a size problem", w.Code, w.Body.String())
	}
}

func TestBackupDownloadAcceptsDatabaseOver256MiB(t *testing.T) {
	a := testApp(t)
	if err := a.DB.Exec("CREATE TABLE backup_large_fixture (data BLOB)").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		for range 257 {
			if err := tx.Exec("INSERT INTO backup_large_fixture VALUES (zeroblob(1048576))").Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	w := call(h, "POST", "/api/backup", `{"password":"large-backup-password"}`, loginForTest(t, h))
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "LGB2") {
		t.Fatal("large backup download failed", w.Code)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := backup.RestoreReader(bytes.NewReader(w.Body.Bytes()), "large-backup-password", destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "ledgergate.db"))
	if err != nil || info.Size() <= 256<<20 {
		t.Fatal("large download lost database contents", err)
	}
	t.Logf("HTTP backup restored %d database bytes", info.Size())
}
