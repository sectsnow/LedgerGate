package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type requestClearFilter struct {
	Mode  string `json:"mode"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

type requestClearPlan struct {
	requestClearFilter
	Before    time.Time `json:"before"`
	MaxID     uint      `json:"max_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Session   string    `json:"session"`
	Revision  string    `json:"revision"`
}

type requestClearPreview struct {
	Token          string     `json:"token"`
	Requests       int64      `json:"requests"`
	Details        int64      `json:"details"`
	SummaryDays    int        `json:"summary_days"`
	KeptRequests   int64      `json:"kept_requests"`
	ActiveRequests int64      `json:"active_requests"`
	FirstAt        *time.Time `json:"first_at"`
	LastAt         *time.Time `json:"last_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

var errRequestClearChanged = errors.New("清理范围已变化，请重新预览后确认")
var errRequestClearEmpty = errors.New("所选范围没有可清除的数据")

func (filter requestClearFilter) validate(now time.Time) error {
	switch filter.Mode {
	case "all":
		if filter.Start != "" || filter.End != "" {
			return errors.New("所选清理方式不需要日期")
		}
	case "range":
		location := usageDay(now).Location()
		start, err := time.ParseInLocation("2006-01-02", filter.Start, location)
		if err != nil {
			return errors.New("开始日期无效")
		}
		end, err := time.ParseInLocation("2006-01-02", filter.End, location)
		if err != nil || end.Before(start) || end.After(usageDay(now)) {
			return errors.New("结束日期须不早于开始日期，且不能晚于今天")
		}
	default:
		return errors.New("请选择有效的清理方式")
	}
	return nil
}

func (plan requestClearPlan) requests(db *gorm.DB) *gorm.DB {
	query := db.Model(&RequestLog{}).
		Where("id <= ? AND julianday(created_at) <= julianday(?)", plan.MaxID, plan.Before).
		Where("COALESCE(state, '') NOT IN ('running', 'streaming')").
		Where("finished_at IS NULL OR julianday(finished_at) <= julianday(?)", plan.Before)
	if plan.Mode == "range" {
		query = query.Where("date(created_at, '+8 hours') >= ? AND date(created_at, '+8 hours') <= ?", plan.Start, plan.End)
	}
	return query
}

func (plan requestClearPlan) archived(db *gorm.DB) *gorm.DB {
	query := db.Model(&RequestDay{}).Where("date <= ?", usageDay(plan.Before).Format("2006-01-02")).
		Where("NOT EXISTS (SELECT 1 FROM request_logs WHERE date(request_logs.created_at, '+8 hours') = request_days.date)")
	if plan.Mode == "range" {
		query = query.Where("date >= ? AND date <= ?", plan.Start, plan.End)
	}
	return query
}

func (plan requestClearPlan) inspect(tx *gorm.DB) (requestClearPreview, []string, string, error) {
	preview := requestClearPreview{ExpiresAt: plan.ExpiresAt}
	dates := map[string]bool{}
	hash := sha256.New()
	rows, err := plan.requests(tx).Select("id, created_at").Order("id").Rows()
	if err != nil {
		return preview, nil, "", err
	}
	for rows.Next() {
		var id uint
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return preview, nil, "", err
		}
		fmt.Fprintf(hash, "%d:%s\n", id, at.UTC().Format(time.RFC3339Nano))
		preview.Requests++
		dates[usageDay(at).Format("2006-01-02")] = true
		if preview.FirstAt == nil || at.Before(*preview.FirstAt) {
			value := at
			preview.FirstAt = &value
		}
		if preview.LastAt == nil || at.After(*preview.LastAt) {
			value := at
			preview.LastAt = &value
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return preview, nil, "", err
	}
	if err := tx.Model(&RequestDetail{}).Where("request_log_id IN (?)", plan.requests(tx).Select("id")).Count(&preview.Details).Error; err != nil {
		return preview, nil, "", err
	}
	fmt.Fprintf(hash, "details:%d\n", preview.Details)
	var archived []RequestDay
	if err := plan.archived(tx).Order("date").Find(&archived).Error; err != nil {
		return preview, nil, "", err
	}
	for _, day := range archived {
		dates[day.Date] = true
		fmt.Fprintln(hash, jsonString(day))
	}
	var total int64
	if err := tx.Model(&RequestLog{}).Count(&total).Error; err != nil {
		return preview, nil, "", err
	}
	preview.KeptRequests = total - preview.Requests
	if err := tx.Model(&RequestLog{}).Where("state IN ('running', 'streaming')").Count(&preview.ActiveRequests).Error; err != nil {
		return preview, nil, "", err
	}
	ordered := make([]string, 0, len(dates))
	for date := range dates {
		ordered = append(ordered, date)
	}
	sort.Strings(ordered)
	preview.SummaryDays = len(ordered)
	return preview, ordered, hex.EncodeToString(hash.Sum(nil)), nil
}

func (a *App) previewRequestClear(c *gin.Context) {
	var input requestClearFilter
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "清理参数无效")
		return
	}
	now := time.Now().UTC()
	if err := input.validate(now); err != nil {
		fail(c, 400, err.Error())
		return
	}
	session, _ := c.Cookie("ledgergate_session")
	plan := requestClearPlan{requestClearFilter: input, Before: now, ExpiresAt: now.Add(10 * time.Minute), Session: digest(session)}
	var preview requestClearPreview
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&RequestLog{}).Select("COALESCE(MAX(id), 0)").Scan(&plan.MaxID).Error; err != nil {
			return err
		}
		var err error
		preview, _, plan.Revision, err = plan.inspect(tx)
		return err
	})
	if err != nil {
		fail(c, 500, "读取清理预览失败")
		return
	}
	preview.Token = a.seal(jsonString(plan))
	c.JSON(200, preview)
}

func (a *App) clearRequests(c *gin.Context) {
	var input struct {
		Token        string `json:"token"`
		Confirmation string `json:"confirmation"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Token) == 0 || len(input.Token) > 4096 {
		fail(c, 400, "请先预览清理范围")
		return
	}
	raw, err := a.unseal(input.Token)
	var plan requestClearPlan
	if err != nil || json.Unmarshal([]byte(raw), &plan) != nil || !plan.ExpiresAt.After(time.Now()) || plan.validate(plan.Before) != nil || plan.Revision == "" {
		fail(c, 400, "预览已失效，请重新预览")
		return
	}
	session, _ := c.Cookie("ledgergate_session")
	if plan.Session != digest(session) {
		fail(c, 403, "请在当前登录会话重新预览")
		return
	}
	if plan.Mode == "all" && strings.TrimSpace(input.Confirmation) != "清空" {
		fail(c, 400, "清空全部请求历史需要输入“清空”确认")
		return
	}
	var result requestClearPreview
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		preview, dates, revision, err := plan.inspect(tx)
		if err != nil {
			return err
		}
		if revision != plan.Revision {
			return errRequestClearChanged
		}
		if preview.Requests == 0 && len(dates) == 0 {
			return errRequestClearEmpty
		}
		if err := tx.Where("request_log_id IN (?)", plan.requests(tx).Select("id")).Delete(&RequestDetail{}).Error; err != nil {
			return err
		}
		if err := plan.requests(tx).Delete(&RequestLog{}).Error; err != nil {
			return err
		}
		for start := 0; start < len(dates); start += 200 {
			chunk := dates[start:min(start+200, len(dates))]
			if err := tx.Where("date IN ?", chunk).Delete(&RequestDay{}).Error; err != nil {
				return err
			}
			// 重建受影响整天的剩余记录，保留同日的新请求和正在进行的请求。
			rows, err := aggregateRequestDays(tx.Model(&RequestLog{}).Where("date(created_at, '+8 hours') IN ?", chunk))
			if err != nil {
				return err
			}
			if err := saveRequestDays(tx, rows); err != nil {
				return err
			}
		}
		result = preview
		return nil
	})
	if errors.Is(err, errRequestClearChanged) {
		fail(c, 409, err.Error())
		return
	}
	if errors.Is(err, errRequestClearEmpty) {
		fail(c, 400, err.Error())
		return
	}
	if err != nil {
		fail(c, 500, "清除失败，本次更改已回滚，请重新预览后重试")
		return
	}
	c.JSON(200, result)
}
