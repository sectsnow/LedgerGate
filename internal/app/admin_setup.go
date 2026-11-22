package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// 显式标记未初始化，避免旧数据库丢失密码文件后重新开放设置入口。
const pendingAdminPassword = "setup\n"

const adminPasswordMinBytes = 8
const adminPasswordMaxBytes = 72

func validateAdminPassword(password string) error {
	if len(password) < adminPasswordMinBytes || len(password) > adminPasswordMaxBytes {
		return errors.New("管理员密码长度必须为 8–72 字节")
	}
	if password != strings.TrimSpace(password) {
		return errors.New("管理员密码首尾不能包含空白字符")
	}
	return nil
}

func loadAdminPassword(config Config, existingDatabase bool) (string, []byte, error) {
	path := filepath.Join(config.DataDir, "admin-password")
	stored, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	missing := errors.Is(err, os.ErrNotExist)
	pending := string(stored) == pendingAdminPassword
	password := config.AdminPassword
	if password == "" {
		if missing {
			if existingDatabase {
				return "", nil, errors.New("数据库已存在但 admin-password 缺失，请恢复原密码文件或设置 LEDGERGATE_ADMIN_PASSWORD")
			}
			if err := saveAdminPassword(path, pendingAdminPassword); err != nil {
				return "", nil, err
			}
			return "", nil, nil
		}
		if pending {
			return "", nil, nil
		}
		password = strings.TrimSpace(string(stored))
	}
	if len(password) < adminPasswordMinBytes || len(password) > adminPasswordMaxBytes {
		return "", nil, errors.New("管理员密码长度必须为 8–72 字节")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}
	if config.AdminPassword != "" && (missing || pending) {
		if err := saveAdminPassword(path, password+"\n"); err != nil {
			return "", nil, err
		}
	}
	return password, hash, nil
}

func saveAdminPassword(path, contents string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".admin-password-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.WriteString(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (a *App) setupRequired() bool {
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	return len(a.passwordHash) == 0
}

func (a *App) setupStatus(c *gin.Context) {
	c.JSON(200, gin.H{"setup_required": a.setupRequired()})
}

func (a *App) setupAdmin(c *gin.Context) {
	if c.GetHeader("X-LedgerGate-Request") != "1" {
		fail(c, 403, "缺少请求校验头")
		return
	}
	if !a.setupRequired() {
		fail(c, 409, "管理员密码已设置，请使用现有密码登录")
		return
	}
	var input struct {
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "请输入管理员密码并确认")
		return
	}
	if err := validateAdminPassword(input.Password); err != nil {
		fail(c, 400, err.Error())
		return
	}
	if input.Password != input.ConfirmPassword {
		fail(c, 400, "两次输入的密码不一致")
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if len(a.passwordHash) != 0 {
		fail(c, 409, "管理员密码已设置，请使用现有密码登录")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(c, 500, "设置管理员密码失败")
		return
	}
	path := filepath.Join(a.dataDir, "admin-password")
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != pendingAdminPassword {
		fail(c, 409, "密码文件已变更，请重启服务后登录")
		return
	}
	if err := saveAdminPassword(path, input.Password+"\n"); err != nil {
		fail(c, 500, "保存管理员密码失败，请检查数据目录权限后重试")
		return
	}
	a.passwordHash = hash
	a.adminPassword = input.Password
	a.createAdminSession(c)
}
