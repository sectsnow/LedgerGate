package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		fail(c, 500, fmt.Sprintf("创建数据库快照失败，请检查剩余空间：%v", err))
		return
	}
	files := map[string]io.Reader{}
	for _, name := range []string{"ledgergate.db", "master.key", "admin-password"} {
		if name == "admin-password" && a.adminPassword != "" {
			files[name] = strings.NewReader(a.adminPassword + "\n")
			continue
		}
		file := filepath.Join(a.dataDir, name)
		if name == "ledgergate.db" {
			file = database
		}
		info, statErr := os.Stat(file)
		if statErr != nil {
			fail(c, 500, fmt.Sprintf("无法访问备份文件 %s：%v", name, statErr))
			return
		}
		if !info.Mode().IsRegular() {
			fail(c, 500, fmt.Sprintf("备份文件 %s 不是普通文件", name))
			return
		}
		if info.Size() > backup.Limit {
			fail(c, 500, fmt.Sprintf("备份文件 %s 当前 %.2f MiB，超过 4 GiB 上限", name, float64(info.Size())/(1<<20)))
			return
		}
		data, readErr := os.Open(file)
		if readErr != nil {
			fail(c, 500, fmt.Sprintf("读取备份文件 %s 失败：%v", name, readErr))
			return
		}
		defer data.Close()
		files[name] = data
	}
	archive := filepath.Join(dir, "backup.lgb")
	output, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fail(c, 500, "创建加密备份文件失败，请检查数据目录权限和剩余空间")
		return
	}
	err = backup.EncryptTo(output, files, input.Password)
	closeErr := output.Close()
	if err != nil || closeErr != nil {
		fail(c, 500, fmt.Sprintf("生成加密备份失败：%v", errors.Join(err, closeErr)))
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.FileAttachment(archive, fmt.Sprintf("ledgergate-%s.lgb", time.Now().UTC().Format("20060102-150405")))
}
