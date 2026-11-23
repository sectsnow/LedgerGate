package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/glebarez/go-sqlite"
	"golang.org/x/crypto/scrypt"
)

const Limit = 4 << 30
const FileLimit = Limit + (1 << 20)
const chunkSize = 1 << 20
const legacyLimit = 256 << 20

var filenames = []string{"ledgergate.db", "master.key", "admin-password"}

func Encrypt(files map[string][]byte, password string) ([]byte, error) {
	readers := map[string]io.Reader{}
	for name, data := range files {
		readers[name] = bytes.NewReader(data)
	}
	var output bytes.Buffer
	if err := EncryptTo(&output, readers, password); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func EncryptTo(output io.Writer, files map[string]io.Reader, password string) error {
	if len(password) < 12 || len(password) > 256 {
		return errors.New("备份密码须为 12–256 字节")
	}
	header := make([]byte, 32)
	copy(header, "LGB2")
	if _, err := rand.Read(header[4:28]); err != nil {
		return err
	}
	binary.BigEndian.PutUint32(header[28:], chunkSize)
	aead, err := derive(password, header[4:20])
	if err != nil {
		return err
	}
	if _, err := output.Write(header); err != nil {
		return err
	}
	encrypted := encryptedWriter{output: output, aead: aead, header: header, buffer: make([]byte, 0, chunkSize+aead.Overhead())}
	archive := zip.NewWriter(&encrypted)
	for _, name := range filenames {
		file, ok := files[name]
		if !ok || file == nil {
			return fmt.Errorf("备份文件 %s 缺失", name)
		}
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		written, err := io.Copy(entry, io.LimitReader(file, Limit+1))
		if err != nil {
			return fmt.Errorf("打包 %s 失败：%w", name, err)
		}
		if written > Limit {
			return fmt.Errorf("备份文件 %s 超过 4 GiB", name)
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if len(encrypted.buffer) > 0 {
		if err := encrypted.flush(); err != nil {
			return err
		}
	}
	// 空的认证块标记完整结束，截断或丢失任何分块都不能作为有效备份恢复。
	return encrypted.flush()
}

type encryptedWriter struct {
	output         io.Writer
	aead           cipher.AEAD
	header, buffer []byte
	sequence       uint32
	written        int64
}

func (writer *encryptedWriter) Write(data []byte) (int, error) {
	if writer.written+int64(len(data)) > Limit {
		return 0, errors.New("压缩后的备份超过 4 GiB")
	}
	consumed := 0
	for len(data) > 0 {
		count := min(chunkSize-len(writer.buffer), len(data))
		writer.buffer = append(writer.buffer, data[:count]...)
		data, consumed = data[count:], consumed+count
		writer.written += int64(count)
		if len(writer.buffer) == chunkSize {
			if err := writer.flush(); err != nil {
				return consumed, err
			}
		}
	}
	return consumed, nil
}

func (writer *encryptedWriter) flush() error {
	nonce := make([]byte, 12)
	copy(nonce, writer.header[20:28])
	binary.BigEndian.PutUint32(nonce[8:], writer.sequence)
	sealed := writer.aead.Seal(writer.buffer[:0], nonce, writer.buffer, writer.header)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(sealed)))
	if _, err := writer.output.Write(size[:]); err != nil {
		return err
	}
	if _, err := writer.output.Write(sealed); err != nil {
		return err
	}
	writer.sequence++
	writer.buffer = writer.buffer[:0]
	return nil
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

func decryptTo(output io.Writer, source io.Reader, password string) error {
	if len(password) < 12 || len(password) > 256 {
		return errors.New("备份密码须为 12–256 字节")
	}
	header := make([]byte, 32)
	if _, err := io.ReadFull(source, header); err != nil || string(header[:4]) != "LGB1" && string(header[:4]) != "LGB2" {
		return errors.New("备份格式无效，请选择 .lgb 文件")
	}
	aead, err := derive(password, header[4:20])
	if err != nil {
		return err
	}
	if string(header[:4]) == "LGB1" {
		sealed, err := io.ReadAll(io.LimitReader(source, legacyLimit+17))
		if err != nil || len(sealed) > legacyLimit+16 {
			return errors.New("旧版 LGB1 备份超过 256 MiB 或读取失败")
		}
		plain, err := aead.Open(nil, header[20:32], sealed, header[:20])
		if err != nil {
			return errors.New("备份密码错误或文件已损坏")
		}
		_, err = output.Write(plain)
		return err
	}
	if binary.BigEndian.Uint32(header[28:]) != chunkSize {
		return errors.New("备份分块格式无效")
	}
	buffer := make([]byte, chunkSize+aead.Overhead())
	nonce := make([]byte, 12)
	copy(nonce, header[20:28])
	var total int64
	for sequence := uint32(0); ; sequence++ {
		var size [4]byte
		if _, err := io.ReadFull(source, size[:]); err != nil {
			return errors.New("备份不完整，缺少结束认证块")
		}
		length := binary.BigEndian.Uint32(size[:])
		if length < uint32(aead.Overhead()) || length > uint32(len(buffer)) {
			return errors.New("备份分块长度无效")
		}
		if _, err := io.ReadFull(source, buffer[:length]); err != nil {
			return errors.New("备份文件已截断或读取失败")
		}
		binary.BigEndian.PutUint32(nonce[8:], sequence)
		plain, err := aead.Open(buffer[:0], nonce, buffer[:length], header)
		if err != nil {
			return errors.New("备份密码错误或文件已损坏")
		}
		if len(plain) == 0 {
			var extra [1]byte
			if n, err := io.ReadFull(source, extra[:]); n != 0 || err != io.EOF {
				return errors.New("备份结束后存在额外数据")
			}
			return nil
		}
		total += int64(len(plain))
		if total > Limit {
			return errors.New("解密后的备份超过 4 GiB")
		}
		if _, err := output.Write(plain); err != nil {
			return err
		}
	}
}

func Restore(raw []byte, password, destination string) error {
	return RestoreReader(bytes.NewReader(raw), password, destination)
}

func RestoreReader(source io.Reader, password, destination string) (err error) {
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".lgb-restore-")
	if err != nil {
		return fmt.Errorf("创建恢复临时目录失败：%w", err)
	}
	defer os.RemoveAll(temporary)
	file, err := os.OpenFile(filepath.Join(temporary, "archive.zip"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := decryptTo(file, bufio.NewReader(io.LimitReader(source, FileLimit+1)), password); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil || len(archive.File) != len(filenames) {
		return errors.New("备份内容无效")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return errors.New("恢复目标必须是尚不存在的新目录")
	}
	defer func() {
		if err != nil {
			os.RemoveAll(destination)
		}
	}()
	seen := map[string]bool{}
	for _, entry := range archive.File {
		if entry.Name != "ledgergate.db" && entry.Name != "master.key" && entry.Name != "admin-password" || seen[entry.Name] || !entry.Mode().IsRegular() {
			return errors.New("备份包含重复或无效文件")
		}
		seen[entry.Name] = true
		limit := int64(Limit)
		if entry.Name != "ledgergate.db" {
			limit = 4096
		}
		if entry.UncompressedSize64 > uint64(limit) {
			return fmt.Errorf("备份中的 %s 超过大小限制", entry.Name)
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		target, err := os.OpenFile(filepath.Join(destination, entry.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			reader.Close()
			return err
		}
		count, copyErr := io.Copy(target, io.LimitReader(reader, limit+1))
		reader.Close()
		closeErr := target.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("恢复 %s 失败：%w", entry.Name, errors.Join(copyErr, closeErr))
		}
		if count > limit {
			return fmt.Errorf("备份中的 %s 超过大小限制", entry.Name)
		}
	}
	key, err := os.ReadFile(filepath.Join(destination, "master.key"))
	if err != nil {
		return err
	}
	passwordFile, err := os.ReadFile(filepath.Join(destination, "admin-password"))
	if err != nil {
		return err
	}
	decoded, keyErr := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(key)))
	passwordLength := len(bytes.TrimSpace(passwordFile))
	if keyErr != nil || len(decoded) != 32 || passwordLength < 8 || passwordLength > 72 {
		return errors.New("备份中的密钥文件无效")
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
