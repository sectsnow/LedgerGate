package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ledgergate/internal/backup"

	"github.com/gin-gonic/gin"
)

func (a *App) createBackup(c *gin.Context) {
	var input struct {
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Password) < 12 || len(input.Password) > 256 {
		fail(c, 400, "备份密码须为 12–256 字节")
		return
	}
	if !a.backupMu.TryLock() {
		fail(c, 409, "正在创建备份，请稍后重试")
		return
	}
	defer a.backupMu.Unlock()
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	dir, err := os.MkdirTemp(a.dataDir, ".backup-")
	if err != nil {
		fail(c, 500, "创建备份临时目录失败")
		return
	}
	defer os.RemoveAll(dir)
	database := filepath.Join(dir, "ledgergate.db")
	if err := a.DB.WithContext(c.Request.Context()).Exec("VACUUM INTO ?", database).Error; err != nil {
		fail(c, 500, "创建一致性数据库快照失败")
		return
	}
	files := map[string][]byte{}
	for _, name := range []string{"ledgergate.db", "master.key", "admin-password"} {
		if name == "admin-password" && a.adminPassword != "" {
			files[name] = []byte(a.adminPassword + "\n")
			continue
		}
		file := filepath.Join(a.dataDir, name)
		if name == "ledgergate.db" {
			file = database
		}
		info, statErr := os.Stat(file)
		if statErr != nil || info.Size() > backup.Limit {
			fail(c, 500, "备份文件缺失或超过 256 MiB")
			return
		}
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			fail(c, 500, "读取备份文件失败")
			return
		}
		files[name] = data
	}
	raw, err := backup.Encrypt(files, input.Password)
	if err != nil {
		fail(c, 500, "加密备份失败")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"ledgergate-%s.lgb\"", time.Now().UTC().Format("20060102-150405")))
	c.Data(200, "application/octet-stream", raw)
}
