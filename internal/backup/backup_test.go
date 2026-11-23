package backup

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const testPassword = "backup-fixture-password"

func TestStreamingBackupAuthenticatesOrderEndAndHeader(t *testing.T) {
	content := make([]byte, 3*chunkSize)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	raw, err := Encrypt(map[string][]byte{"ledgergate.db": content, "master.key": []byte("key"), "admin-password": []byte("password")}, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:4]) != "LGB2" {
		t.Fatal("streaming format missing")
	}
	var plain bytes.Buffer
	if err := decryptTo(&plain, bytes.NewReader(raw), testPassword); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(plain.Bytes()), int64(plain.Len()))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(decoded, content) {
		t.Fatal("streaming round trip lost content", err)
	}
	firstEnd := 36 + int(binary.BigEndian.Uint32(raw[32:36]))
	secondEnd := firstEnd + 4 + int(binary.BigEndian.Uint32(raw[firstEnd:firstEnd+4]))
	swapped := append([]byte{}, raw[:32]...)
	swapped = append(swapped, raw[firstEnd:secondEnd]...)
	swapped = append(swapped, raw[32:firstEnd]...)
	swapped = append(swapped, raw[secondEnd:]...)
	for _, test := range []struct {
		name     string
		raw      []byte
		password string
	}{
		{"wrong password", raw, "wrong-backup-password"},
		{"missing final block", raw[:len(raw)-20], testPassword},
		{"truncated data", raw[:firstEnd-1], testPassword},
		{"reordered blocks", swapped, testPassword},
		{"trailing bytes", append(append([]byte{}, raw...), 1), testPassword},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := decryptTo(io.Discard, bytes.NewReader(test.raw), test.password); err == nil {
				t.Fatal("invalid backup accepted")
			}
		})
	}
	for _, offset := range []int{4, 20, 28, 36, firstEnd + 8} {
		copy := append([]byte{}, raw...)
		copy[offset] ^= 1
		if err := decryptTo(io.Discard, bytes.NewReader(copy), testPassword); err == nil {
			t.Fatalf("tampering at %d was accepted", offset)
		}
	}
}

func legacyArchiveForTest(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var plain bytes.Buffer
	archive := zip.NewWriter(&plain)
	for _, name := range filenames {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 32)
	copy(header, "LGB1")
	if _, err := rand.Read(header[4:]); err != nil {
		t.Fatal(err)
	}
	aead, err := derive(testPassword, header[4:20])
	if err != nil {
		t.Fatal(err)
	}
	return aead.Seal(header, header[20:32], plain.Bytes(), header[:20])
}

func TestLegacyBackupAndStreamingRestoreProtectDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE accounts (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	files := map[string][]byte{"ledgergate.db": data, "master.key": []byte(key), "admin-password": []byte("admin-password-123")}
	current, err := Encrypt(files, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"legacy": legacyArchiveForTest(t, files), "streaming": current} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(dir, name)
			if err := RestoreReader(bytes.NewReader(raw), testPassword, destination); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(destination, "ledgergate.db"))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("database changed", err)
			}
			if err := Restore(raw, testPassword, destination); err == nil {
				t.Fatal("existing directory overwritten")
			}
			bad := filepath.Join(dir, name+"-bad")
			if err := Restore(raw[:len(raw)-5], testPassword, bad); err == nil {
				t.Fatal("truncated archive accepted")
			}
			if _, err := os.Stat(bad); !os.IsNotExist(err) {
				t.Fatal("invalid backup left restored files")
			}
		})
	}
}

func TestStreamingBackupRestoresDatabaseOver256MiB(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "large.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE accounts (id INTEGER PRIMARY KEY); CREATE TABLE content (data BLOB)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for range 257 {
		if _, err := tx.Exec("INSERT INTO content VALUES (zeroblob(1048576))"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil || info.Size() <= legacyLimit {
		t.Fatal("fixture is not larger than the old limit", err)
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Create(filepath.Join(dir, "large.lgb"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if err := EncryptTo(output, map[string]io.Reader{"ledgergate.db": input, "master.key": bytes.NewBufferString(key), "admin-password": bytes.NewBufferString("admin-password-123")}, testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "restored")
	if err := RestoreReader(output, testPassword, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := os.Stat(filepath.Join(destination, "ledgergate.db"))
	if err != nil || restored.Size() != info.Size() {
		t.Fatal("large database size changed", err)
	}
	t.Logf("restored database: %d bytes (> 256 MiB)", restored.Size())
}
