package app

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ledgergate/internal/backup"

	"github.com/gin-gonic/gin"
)

const restoreMarker = ".restore-pending"

var restoreFiles = []string{"ledgergate.db", "master.key", "admin-password"}

func (a *App) restoreBackup(c *gin.Context) {
	if !a.backupMu.TryLock() {
		fail(c, 409, "正在处理备份或数据包，请稍后重试")
		return
	}
	defer a.backupMu.Unlock()
	upload, err := c.Request.MultipartReader()
	if err != nil {
		fail(c, 400, "读取备份失败，请选择不超过 4 GiB 的 .lgb 文件")
		return
	}
	backupDir := filepath.Join(a.dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		fail(c, 500, "创建恢复目录失败，请检查数据目录权限和剩余空间")
		return
	}
	workspace, err := os.MkdirTemp(backupDir, "restore-"+time.Now().UTC().Format("20060102-150405")+"-")
	if err != nil {
		fail(c, 500, "创建恢复临时目录失败")
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(workspace)
		}
	}()
	archive, err := os.OpenFile(filepath.Join(workspace, "uploaded.lgb"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fail(c, 500, "创建上传临时文件失败，请检查剩余空间")
		return
	}
	defer archive.Close()
	fields := map[string]string{}
	seen := map[string]bool{}
	// 上传文件写入数据卷，避免占满内存和容器中仅有 64 MiB 的临时目录。
	for {
		part, err := upload.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(c, 400, "读取备份失败或文件超过 4 GiB")
			return
		}
		name := part.FormName()
		if seen[name] || name != "file" && name != "password" && name != "confirm" {
			fail(c, 400, "备份上传参数无效")
			return
		}
		seen[name] = true
		limit := int64(256)
		if name == "file" {
			if !strings.EqualFold(filepath.Ext(part.FileName()), ".lgb") {
				fail(c, 400, "请选择 .lgb 备份文件")
				return
			}
			limit = backup.FileLimit
			count, err := io.Copy(archive, io.LimitReader(part, limit+1))
			if err != nil {
				fail(c, 500, "保存上传备份失败，请检查数据卷剩余空间")
				return
			}
			if count > limit {
				fail(c, 400, "备份文件超过 4 GiB")
				return
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil || int64(len(data)) > limit {
			fail(c, 400, "备份密码或确认参数超过 256 字节")
			return
		}
		fields[name] = string(data)
	}
	if fields["confirm"] != "恢复" || !seen["file"] {
		fail(c, 400, "请选择 .lgb 文件并确认用备份替换当前数据")
		return
	}
	incoming := filepath.Join(workspace, "incoming")
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		fail(c, 500, "读取上传备份失败")
		return
	}
	if err := backup.RestoreReader(archive, fields["password"], incoming); err != nil {
		fail(c, 400, err.Error())
		return
	}
	archive.Close()
	os.Remove(archive.Name())
	config := Config{DataDir: incoming}
	if a.envPassword {
		config.AdminPassword = a.adminPassword
	}
	staged, err := Open(config)
	if err != nil {
		fail(c, 400, "备份数据库无法打开或与当前版本不兼容，当前数据未修改")
		return
	}
	err = staged.DB.Where("1=1").Delete(&Session{}).Error
	closeErr := staged.Close()
	if err != nil || closeErr != nil {
		fail(c, 500, "准备恢复数据失败，当前数据未修改")
		return
	}
	// 旧备份升级产生的原始详情归档随恢复工作区保留，不能随 incoming 一起清理。
	if _, err := os.Stat(filepath.Join(incoming, "backups")); err == nil {
		if err := os.Rename(filepath.Join(incoming, "backups"), filepath.Join(workspace, "request-history")); err != nil {
			fail(c, 500, "保留旧请求归档失败，当前数据未修改")
			return
		}
	}
	if c.Request.Context().Err() != nil {
		return
	}
	keep = true
	if err := a.replaceData(incoming, filepath.Join(workspace, "previous")); err != nil {
		slog.Error("备份恢复失败", "error", err)
		fail(c, 500, err.Error())
		return
	}
	os.RemoveAll(incoming)
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("ledgergate_session", "", -1, "/", "", c.Request.TLS != nil, true)
	c.JSON(200, gin.H{"ok": true, "environment_managed": a.envPassword})
}

// 调用方持有 runtimeMu 写锁；保留旧文件，并用标记支持进程中断后的回滚。
func (a *App) replaceData(incoming, previous string) (resultErr error) {
	var checkpoint struct{ Busy, Log, Checkpointed int }
	if err := a.DB.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&checkpoint).Error; err != nil || checkpoint.Busy != 0 {
		return errors.New("当前数据库仍被占用，恢复已取消，当前数据未修改")
	}
	if err := os.Mkdir(previous, 0700); err != nil {
		return errors.New("保存恢复前数据失败，当前数据未修改")
	}
	relative, err := filepath.Rel(a.dataDir, previous)
	if err != nil {
		return err
	}
	marker := filepath.Join(a.dataDir, restoreMarker)
	if err := saveAdminPassword(marker, relative+"\n"); err != nil {
		return errors.New("保存恢复状态失败，当前数据未修改")
	}
	config := Config{DataDir: a.dataDir}
	if a.envPassword {
		config.AdminPassword = a.adminPassword
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if err := recoverPendingRestore(a.dataDir); err != nil {
			resultErr = fmt.Errorf("恢复及回滚失败，原数据保留在 %s，请停止服务并检查：%w", previous, errors.Join(resultErr, err))
			return
		}
		original, err := Open(config)
		if err != nil {
			resultErr = fmt.Errorf("恢复失败，原文件已回滚但重新打开失败：%w", err)
			return
		}
		a.useRestoredData(original)
		resultErr = fmt.Errorf("恢复失败，已回滚到恢复前的数据：%w", resultErr)
	}()
	if err := a.close(); err != nil {
		return err
	}
	for _, name := range restoreFiles {
		if err := os.Rename(filepath.Join(a.dataDir, name), filepath.Join(previous, name)); err != nil {
			return err
		}
	}
	if err := removeDatabaseSidecars(a.dataDir); err != nil {
		return err
	}
	for _, name := range restoreFiles {
		if err := os.Rename(filepath.Join(incoming, name), filepath.Join(a.dataDir, name)); err != nil {
			return err
		}
	}
	restored, err := open(config)
	if err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil {
		restored.Close()
		return err
	}
	a.useRestoredData(restored)
	return nil
}

func (a *App) useRestoredData(restored *App) {
	a.DB, a.client, a.aead = restored.DB, restored.client, restored.aead
	a.passwordHash, a.adminPassword = restored.passwordHash, restored.adminPassword
	a.proxyClient, a.proxyIdentity = nil, ""
	a.weights, a.health, a.routeHealth = restored.weights, restored.health, restored.routeHealth
	a.groupRouting = nil
	a.monitoring, a.loginAttempts = restored.monitoring, restored.loginAttempts
	a.activeRequests, a.accountIcons = nil, nil
	a.finishedRequests, a.finishedRequestBytes = nil, 0
	a.nextRequestID = restored.nextRequestID
	a.keyTrafficMu.Lock()
	a.keyTraffic = nil
	a.keyTrafficMu.Unlock()
}

func recoverPendingRestore(dataDir string) error {
	marker := filepath.Join(dataDir, restoreMarker)
	raw, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	relative := strings.TrimSpace(string(raw))
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 3 || parts[0] != "backups" || !strings.HasPrefix(parts[1], "restore-") || parts[2] != "previous" || filepath.Clean(relative) != relative {
		return errors.New("恢复状态文件无效，请检查数据目录")
	}
	previous := filepath.Join(dataDir, relative)
	if info, err := os.Stat(previous); err != nil || !info.IsDir() {
		return errors.New("恢复前的数据目录不存在，请检查恢复状态")
	}
	if _, err := os.Stat(filepath.Join(previous, "ledgergate.db")); err == nil {
		if err := removeDatabaseSidecars(dataDir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range restoreFiles {
		source := filepath.Join(previous, name)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Rename(source, filepath.Join(dataDir, name)); err != nil {
			return err
		}
	}
	return os.Remove(marker)
}

func removeDatabaseSidecars(dataDir string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dataDir, "ledgergate.db"+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
