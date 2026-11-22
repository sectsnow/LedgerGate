package backup

import (
	"crypto/rand"
	"errors"
)

const DataLimit = 16 << 20

func EncryptData(plain []byte, password string) ([]byte, error) {
	if len(plain) > DataLimit || len(password) < 12 || len(password) > 256 {
		return nil, errors.New("数据包限 16 MiB，密码须为 12–256 字节")
	}
	header := make([]byte, 32)
	copy(header, "LGD1")
	if _, err := rand.Read(header[4:]); err != nil {
		return nil, err
	}
	aead, err := derive(password, header[4:20])
	if err != nil {
		return nil, err
	}
	return aead.Seal(header, header[20:32], plain, header[:20]), nil
}

func DecryptData(raw []byte, password string) ([]byte, error) {
	if len(raw) < 48 || len(raw) > DataLimit+48 || string(raw[:4]) != "LGD1" || len(password) < 12 || len(password) > 256 {
		return nil, errors.New("数据包格式或密码无效，请选择 .lgd 文件")
	}
	aead, err := derive(password, raw[4:20])
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, raw[20:32], raw[32:], raw[:20])
	if err != nil {
		return nil, errors.New("数据包密码错误或文件已损坏")
	}
	return plain, nil
}
