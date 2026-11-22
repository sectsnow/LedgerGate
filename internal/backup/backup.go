package backup

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/glebarez/go-sqlite"
	"golang.org/x/crypto/scrypt"
)

const Limit = 256 << 20

var filenames = []string{"ledgergate.db", "master.key", "admin-password"}

func Encrypt(files map[string][]byte, password string) ([]byte, error) {
	if len(password) < 12 || len(password) > 256 {
		return nil, errors.New("备份密码须为 12–256 字节")
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range filenames {
		data, ok := files[name]
		if !ok || len(data) > Limit {
			return nil, errors.New("备份文件缺失或超过 256 MiB")
		}
		entry, err := archive.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > Limit {
		return nil, errors.New("备份超过 256 MiB")
	}
	header := make([]byte, 4+16+12)
	copy(header, "LGB1")
	if _, err := rand.Read(header[4:]); err != nil {
		return nil, err
	}
	aead, err := derive(password, header[4:20])
	if err != nil {
		return nil, err
	}
	return aead.Seal(header, header[20:32], buffer.Bytes(), header[:20]), nil
}

func derive(password string, salt []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Restore(raw []byte, password, destination string) (err error) {
	if len(raw) < 48 || len(raw) > Limit+48 || string(raw[:4]) != "LGB1" || len(password) < 12 || len(password) > 256 {
		return errors.New("备份格式或密码无效")
	}
	aead, err := derive(password, raw[4:20])
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, raw[20:32], raw[32:], raw[:20])
	if err != nil {
		return errors.New("备份密码错误或文件已损坏")
	}
	archive, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil || len(archive.File) != len(filenames) {
		return errors.New("备份内容无效")
	}
	files := map[string][]byte{}
	for _, entry := range archive.File {
		if entry.Name != "ledgergate.db" && entry.Name != "master.key" && entry.Name != "admin-password" || files[entry.Name] != nil || entry.UncompressedSize64 > Limit {
			return errors.New("备份包含重复或无效文件")
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, Limit+1))
		reader.Close()
		if readErr != nil || len(data) > Limit {
			return errors.New("备份文件损坏或过大")
		}
		files[entry.Name] = data
	}
	key, keyErr := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(files["master.key"])))
	passwordLength := len(bytes.TrimSpace(files["admin-password"]))
	if keyErr != nil || len(key) != 32 || passwordLength < 8 || passwordLength > 72 {
		return errors.New("备份中的密钥文件无效")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return errors.New("恢复目标必须是尚不存在的新目录")
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	for _, name := range filenames {
		if err = os.WriteFile(filepath.Join(destination, name), files[name], 0600); err != nil {
			return err
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(destination, "ledgergate.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	var count int
	if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("备份数据库完整性校验失败")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&count); err != nil {
		return errors.New("备份不包含 LedgerGate 数据表")
	}
	return nil
}
