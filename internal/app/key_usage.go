package app

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type KeyUsageDay struct {
	KeyID        uint   `json:"key_id" gorm:"primaryKey;autoIncrement:false"`
	Date         string `json:"date" gorm:"primaryKey;size:10"`
	Requests     int64  `json:"requests"`
	CostMicros   int64  `json:"cost_micros"`
	UnknownCosts int64  `json:"unknown_costs"`
}
type keyTraffic struct {
	Active   int
	Accepted []time.Time
}

func keyUsage(tx *gorm.DB, id uint, now time.Time) (daily, monthly int64, err error) {
	today := usageDay(now).Format("2006-01-02")
	var row struct{ Daily, Monthly int64 }
	err = tx.Model(&KeyUsageDay{}).Where("key_id = ? AND date >= ? AND date <= ?", id, today[:7]+"-01", today).
		Select("COALESCE(SUM(CASE WHEN date = ? THEN cost_micros ELSE 0 END),0) AS daily, COALESCE(SUM(cost_micros),0) AS monthly", today).Scan(&row).Error
	return row.Daily, row.Monthly, err
}
func recordKeyUsage(tx *gorm.DB, log *RequestLog) error {
	if log.KeyID == 0 {
		return nil
	}
	row := KeyUsageDay{KeyID: log.KeyID, Date: usageDay(log.CreatedAt).Format("2006-01-02"), Requests: 1, CostMicros: log.CostMicros}
	if log.CostStatus == "unknown" {
		row.CostMicros = 0
		row.UnknownCosts = 1
	}
	return incrementSummary(tx, &row, "key_usage_days", []clause.Column{{Name: "key_id"}, {Name: "date"}}, []string{"requests", "cost_micros", "unknown_costs"})
}
func (a *App) admitKeyRequest(c *gin.Context, key APIKey) bool {
	a.keyTrafficMu.Lock()
	defer a.keyTrafficMu.Unlock()
	now := time.Now()
	var daily, monthly int64
	if key.DailyBudgetMicros != nil || key.MonthlyBudgetMicros != nil {
		var err error
		daily, monthly, err = keyUsage(a.DB.WithContext(c.Request.Context()), key.ID, now)
		if err != nil {
			fail(c, 503, "读取周期预算失败，请稍后重试")
			return false
		}
	}
	for _, limit := range []struct {
		budget *int64
		used   int64
		reset  time.Time
		label  string
	}{
		{key.DailyBudgetMicros, daily, usageDay(now).AddDate(0, 0, 1), "今日"},
		{key.MonthlyBudgetMicros, monthly, time.Date(usageDay(now).Year(), usageDay(now).Month()+1, 1, 0, 0, 0, 0, usageDay(now).Location()), "本月"},
	} {
		if limit.budget != nil && limit.used >= *limit.budget {
			c.Header("Retry-After", strconv.Itoa(max(1, int(time.Until(limit.reset).Seconds()))))
			fail(c, 429, "网关密钥已达到"+limit.label+"预算上限（北京时间周期）")
			return false
		}
	}
	if a.keyTraffic == nil {
		a.keyTraffic = map[uint]*keyTraffic{}
	}
	for id, item := range a.keyTraffic {
		if item.Active == 0 && (len(item.Accepted) == 0 || now.Sub(item.Accepted[len(item.Accepted)-1]) >= time.Minute) {
			delete(a.keyTraffic, id)
		}
	}
	traffic := a.keyTraffic[key.ID]
	if traffic == nil {
		traffic = &keyTraffic{}
		a.keyTraffic[key.ID] = traffic
	}
	for len(traffic.Accepted) > 0 && now.Sub(traffic.Accepted[0]) >= time.Minute {
		traffic.Accepted = traffic.Accepted[1:]
	}
	if key.MaxConcurrent > 0 && traffic.Active >= key.MaxConcurrent {
		c.Header("Retry-After", "1")
		fail(c, 429, "网关密钥已达到并发请求上限")
		return false
	}
	if key.RequestsPerMinute > 0 && len(traffic.Accepted) >= key.RequestsPerMinute {
		c.Header("Retry-After", strconv.Itoa(max(1, int(time.Until(traffic.Accepted[0].Add(time.Minute)).Seconds())+1)))
		fail(c, 429, "网关密钥已达到每分钟请求上限")
		return false
	}
	traffic.Active++
	if key.RequestsPerMinute > 0 {
		traffic.Accepted = append(traffic.Accepted, now)
	}
	return true
}
func (a *App) releaseKeyRequest(id uint) {
	a.keyTrafficMu.Lock()
	defer a.keyTrafficMu.Unlock()
	if row := a.keyTraffic[id]; row != nil {
		row.Active = max(0, row.Active-1)
	}
}
func (a *App) populateKeyUsage(keys []APIKey, now time.Time) error {
	for i := range keys {
		daily, monthly, err := keyUsage(a.DB, keys[i].ID, now)
		if err != nil {
			return err
		}
		keys[i].DailyUsedMicros, keys[i].MonthlyUsedMicros = daily, monthly
		day := usageDay(now).AddDate(0, 0, 1)
		month := time.Date(usageDay(now).Year(), usageDay(now).Month()+1, 1, 0, 0, 0, 0, day.Location())
		keys[i].DailyResetAt, keys[i].MonthlyResetAt = &day, &month
		a.keyTrafficMu.Lock()
		if row := a.keyTraffic[keys[i].ID]; row != nil {
			keys[i].ActiveRequests = row.Active
		}
		a.keyTrafficMu.Unlock()
	}
	return nil
}
func (a *App) keyUsageHistory(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := pathID(c)
	if !ok {
		return
	}
	var key APIKey
	if a.DB.First(&key, id).Error != nil {
		fail(c, 404, "密钥不存在")
		return
	}
	rows := []KeyUsageDay{}
	if err := a.DB.Where("key_id = ? AND date >= ?", id, usageDay(time.Now()).AddDate(0, 0, -89).Format("2006-01-02")).Order("date DESC").Limit(90).Find(&rows).Error; err != nil {
		fail(c, 500, "读取密钥用量失败")
		return
	}
	c.JSON(200, gin.H{"items": rows, "note": "北京时间请求开始日；仅统计启用此功能后的用量，未知费用不计入预算"})
}
