package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Config struct {
	DataDir       string
	AdminPassword string
}

type channelState struct {
	Until   time.Time
	Error   string
	Probing bool
	Status  int
}

type App struct {
	DB             *gorm.DB
	client         *http.Client
	proxyMu        sync.Mutex
	proxyClient    *http.Client
	proxyIdentity  string
	aead           cipher.AEAD
	authMu         sync.RWMutex
	passwordHash   []byte
	dataDir        string
	adminPassword  string
	envPassword    bool
	runtimeMu      sync.RWMutex
	backupMu       sync.Mutex
	priceSyncMu    sync.Mutex
	modelSyncMu    sync.Mutex
	notificationMu sync.Mutex
	mu             sync.Mutex
	// shortcut: 轮询和冷却状态保存在当前进程，部署多实例时改用共享状态。
	weights              map[uint]int
	health               map[uint]channelState
	routeHealth          map[uint]channelState
	groupRouting         map[uint]*groupRouteState
	monitoring           map[uint]bool
	loginAttempts        map[string][]time.Time
	activeRequests       map[uint]*requestTrace
	finishedRequests     []cachedRequest
	finishedRequestBytes int
	nextRequestID        uint
	modelTests           int
	accountIcons         map[uint]*accountIconCache
}

func Open(config Config) (*App, error) {
	if err := recoverPendingRestore(config.DataDir); err != nil {
		return nil, err
	}
	return open(config)
}

func open(config Config) (*App, error) {
	if err := os.MkdirAll(config.DataDir, 0700); err != nil {
		return nil, err
	}
	_, databaseErr := os.Stat(filepath.Join(config.DataDir, "ledgergate.db"))
	if databaseErr != nil && !errors.Is(databaseErr, os.ErrNotExist) {
		return nil, databaseErr
	}
	if databaseErr == nil {
		if _, keyErr := os.Stat(filepath.Join(config.DataDir, "master.key")); keyErr != nil {
			return nil, errors.New("数据库已存在但 master.key 缺失，请恢复原密钥文件")
		}
	}
	key, err := readOrCreate(filepath.Join(config.DataDir, "master.key"), 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	password, hash, err := loadAdminPassword(config, databaseErr == nil)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(config.DataDir, "ledgergate.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	opened := false
	defer func() {
		if !opened {
			sqlDB.Close()
		}
	}()
	if err = db.Exec("PRAGMA foreign_keys=ON").Error; err != nil {
		return nil, err
	}
	if errors.Is(databaseErr, os.ErrNotExist) {
		if err = db.Exec("PRAGMA auto_vacuum=INCREMENTAL").Error; err != nil {
			return nil, err
		}
	}
	if err = db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return nil, err
	}
	if err = db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		return nil, err
	}
	if err = os.Chmod(filepath.Join(config.DataDir, "ledgergate.db"), 0600); err != nil {
		return nil, err
	}
	keyUsageExisted := db.Migrator().HasColumn(&APIKey{}, "used_micros")
	if err = db.AutoMigrate(&Account{}, &Channel{}, &Group{}, &Route{}, &APIKey{}, &Snapshot{}, &RequestLog{}, &RequestDetail{}, &RequestDay{}, &RequestHour{}, &Alert{}, &AlertState{}, &Session{}, &Setting{}, &channelCreation{}, &SyncTask{}, &NotificationChannel{}, &NotificationDelivery{}); err != nil {
		return nil, err
	}
	if !keyUsageExisted {
		if err = db.Exec("UPDATE api_keys SET used_micros = COALESCE((SELECT SUM(cost_micros) FROM request_logs WHERE key_id=api_keys.id AND cost_micros>0 AND state NOT IN ('running','streaming')),0)").Error; err != nil {
			return nil, err
		}
	}
	if err = db.Model(&RequestLog{}).Where("state IN ?", []string{"running", "streaming"}).Updates(map[string]any{
		"state": "interrupted", "status": 502, "error": "服务重启，请求结果未确认", "finished_at": time.Now(),
		"duration_ms": gorm.Expr("MAX(0, CAST((julianday(?) - julianday(created_at)) * 86400000 AS INTEGER))", time.Now()),
	}).Error; err != nil {
		return nil, err
	}
	if err = db.FirstOrCreate(&Setting{ID: 1, AlertCooldownMinutes: 60, RetentionDays: 30, RequestSummaryRetentionDays: 365}).Error; err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 20
	app := &App{DB: db, aead: aead, passwordHash: hash, dataDir: config.DataDir, adminPassword: password, envPassword: config.AdminPassword != "",
		nextRequestID: uint(time.Now().UnixMicro()),
		client:        &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		weights:       map[uint]int{}, health: map[uint]channelState{}, monitoring: map[uint]bool{}, loginAttempts: map[string][]time.Time{}}
	if err := app.migrateNotifications(); err != nil {
		return nil, err
	}
	if err := app.migrateRequestHistory(); err != nil {
		return nil, err
	}
	opened = true
	return app, nil
}

func readOrCreate(path string, size int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if size == 24 {
			return []byte(strings.TrimSpace(string(data))), nil
		}
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(decoded) != size {
			return nil, fmt.Errorf("密钥文件格式错误 %s", path)
		}
		return decoded, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.WriteString(encoded + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if size == 24 {
		return []byte(encoded), nil
	}
	return raw, nil
}

func randomToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (a *App) seal(value string) string {
	if value == "" {
		return ""
	}
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(a.aead.Seal(nonce, nonce, []byte(value), nil))
}

func (a *App) unseal(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(raw) < a.aead.NonceSize() {
		return "", errors.New("凭据密文无效")
	}
	n := a.aead.NonceSize()
	plain, err := a.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("凭据解密失败，请检查 master.key")
	}
	return string(plain), nil
}

func (a *App) Close() error {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	return a.close()
}

func (a *App) close() error {
	a.client.CloseIdleConnections()
	a.proxyMu.Lock()
	if a.proxyClient != nil {
		a.proxyClient.CloseIdleConnections()
	}
	a.proxyMu.Unlock()
	db, err := a.DB.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func fail(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "type": "ledgergate_error"}})
}

func (a *App) admin(c *gin.Context) {
	if a.requireAdmin(c) {
		c.Next()
	}
}

func (a *App) requireAdmin(c *gin.Context) bool {
	if a.setupRequired() {
		fail(c, 401, "请先设置管理员密码")
		return false
	}
	cookie, err := c.Cookie("ledgergate_session")
	var session Session
	if err != nil || a.DB.Where("digest = ? AND expires_at > ?", digest(cookie), time.Now()).First(&session).Error != nil {
		fail(c, 401, "请先登录")
		return false
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		if c.GetHeader("X-LedgerGate-Request") != "1" {
			fail(c, 403, "缺少请求校验头")
			return false
		}
	}
	return true
}

func (a *App) login(c *gin.Context) {
	if !a.allowPasswordAttempt(c, "login") {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&body) != nil {
		fail(c, 401, "密码错误")
		return
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	hash := a.passwordHash
	if len(hash) == 0 {
		fail(c, 409, "请先设置管理员密码")
		return
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(body.Password)) != nil {
		fail(c, 401, "密码错误")
		return
	}
	a.createAdminSession(c)
}

func (a *App) createAdminSession(c *gin.Context) {
	token := randomToken()
	if err := a.DB.Create(&Session{Digest: digest(token), ExpiresAt: time.Now().Add(12 * time.Hour)}).Error; err != nil {
		fail(c, 500, "会话保存失败")
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("ledgergate_session", token, 12*3600, "/", "", c.Request.TLS != nil, true)
	c.JSON(200, gin.H{"ok": true})
}

func (a *App) gatewayAuth(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if token == "" {
		token = c.GetHeader("x-api-key")
	}
	var key APIKey
	if token == "" || a.DB.Where("digest = ? AND enabled = ?", digest(token), true).First(&key).Error != nil {
		fail(c, 401, "网关密钥无效")
		return
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
		fail(c, 403, "网关密钥已到期")
		return
	}
	if key.BudgetMicros != nil && key.UsedMicros >= *key.BudgetMicros {
		fail(c, 429, "网关密钥已达到消费上限")
		return
	}
	c.Set("allowed_models", key.AllowedModels)
	c.Set("key_id", key.ID)
	c.Set("key_name", key.Name)
	c.Next()
}

func (a *App) RunTasks(ctx context.Context) {
	var workers sync.WaitGroup
	start := func(interval time.Duration, task func(context.Context)) {
		workers.Go(func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				a.runtimeMu.RLock()
				if ctx.Err() == nil {
					task(ctx)
				}
				a.runtimeMu.RUnlock()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	}
	start(5*time.Second, a.checkDueAccounts)
	start(5*time.Second, a.deliverAlerts)
	start(time.Minute, a.syncDueTasks)
	start(time.Hour, func(ctx context.Context) {
		if err := a.cleanupHistory(ctx, time.Now()); err != nil {
			slog.Error("历史记录清理失败", "error", err)
		}
	})
	workers.Wait()
}
