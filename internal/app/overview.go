package app

import (
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

type MinimumBalance struct {
	AccountID uint    `json:"account_id"`
	Name      string  `json:"name"`
	Balance   float64 `json:"balance"`
}

type BalanceDay struct {
	Date                string   `json:"date"`
	Balance             *float64 `json:"balance"`
	Consumption         *float64 `json:"consumption"`
	BalanceAccounts     int      `json:"balance_accounts"`
	ConsumptionAccounts int      `json:"consumption_accounts"`
}

func (a *App) requestActivity(now time.Time) ([]RequestDay, error) {
	start := usageDay(now).AddDate(0, 0, -364)
	counts := map[string]RequestDay{}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var archived []RequestDay
		if err := tx.Where("date >= ? AND date <= ?", start.Format("2006-01-02"), usageDay(now).Format("2006-01-02")).Find(&archived).Error; err != nil {
			return err
		}
		for _, row := range archived {
			counts[row.Date] = row
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	days := make([]RequestDay, 365)
	for i := range days {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		days[i] = counts[date]
		days[i].Date = date
	}
	return days, nil
}

type AccountSummary struct {
	Currency         string          `json:"currency"`
	Accounts         int             `json:"accounts"`
	Balance          *float64        `json:"balance"`
	TodayUsed        *float64        `json:"today_used"`
	TodayCollectedAt *time.Time      `json:"today_collected_at"`
	Used             *float64        `json:"used"`
	BalanceAccounts  int             `json:"balance_accounts"`
	TodayAccounts    int             `json:"today_accounts"`
	UsedAccounts     int             `json:"used_accounts"`
	Minimum          *MinimumBalance `json:"minimum"`
	History          []BalanceDay    `json:"history"`
}

func sumKnown(total *float64, value float64) *float64 {
	if total != nil {
		value += *total
	}
	return &value
}

func (a *App) accountOverview(now time.Time) ([]AccountSummary, error) {
	const historyDays = 30
	today := usageDay(now)
	start := today.AddDate(0, 0, 1-historyDays)
	accounts := []Account{}
	rows := []struct {
		AccountID uint
		Date      string
		Balance   *float64
		TodayUsed *float64
		TodayDate string
	}{}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Select("id", "name", "currency", "balance", "used", "today_used", "today_date", "today_collected_at").Order("id").Find(&accounts).Error; err != nil {
			return err
		}
		if len(accounts) == 0 {
			return nil
		}
		// shortcut: 趋势使用本地已采集快照，需要更早记录时接入历史导入。
		return tx.Raw(`
			WITH ranked AS (
				SELECT s.account_id, s.balance, s.today_used, s.today_date,
					date(s.created_at, '+8 hours') AS date,
					ROW_NUMBER() OVER (
						PARTITION BY s.account_id, date(s.created_at, '+8 hours')
						ORDER BY julianday(s.created_at) DESC, s.id DESC
					) AS position
				FROM snapshots AS s
				JOIN accounts AS a ON a.id = s.account_id
					AND upper(trim(COALESCE(a.currency, ''))) = upper(trim(COALESCE(s.currency, '')))
				WHERE julianday(s.created_at) >= julianday(?)
					AND julianday(s.created_at) <= julianday(?)
					AND s.status IN ('ok', 'low')
			)
			SELECT account_id, date, balance, today_used, today_date
			FROM ranked WHERE position = 1 ORDER BY date, account_id
		`, start, now).Scan(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	groups := map[string]*AccountSummary{}
	accountGroups := map[uint]*AccountSummary{}
	dateIndexes := map[string]int{}
	for i := range historyDays {
		dateIndexes[start.AddDate(0, 0, i).Format("2006-01-02")] = i
	}
	for _, account := range accounts {
		currency := strings.ToUpper(strings.TrimSpace(account.Currency))
		group := groups[currency]
		if group == nil {
			group = &AccountSummary{Currency: currency, History: make([]BalanceDay, historyDays)}
			for date, index := range dateIndexes {
				group.History[index].Date = date
			}
			groups[currency] = group
		}
		group.Accounts++
		accountGroups[account.ID] = group
		if account.Balance != nil {
			group.Balance = sumKnown(group.Balance, *account.Balance)
			group.BalanceAccounts++
			if group.Minimum == nil || *account.Balance < group.Minimum.Balance {
				group.Minimum = &MinimumBalance{AccountID: account.ID, Name: account.Name, Balance: *account.Balance}
			}
		}
		if account.TodayUsed != nil && account.TodayDate == today.Format("2006-01-02") {
			group.TodayUsed = sumKnown(group.TodayUsed, *account.TodayUsed)
			group.TodayAccounts++
			// 汇总展示最早成功采集时间；任一参与账户时间未知时不推测。
			if group.TodayAccounts == 1 || account.TodayCollectedAt == nil || group.TodayCollectedAt != nil && account.TodayCollectedAt.Before(*group.TodayCollectedAt) {
				group.TodayCollectedAt = account.TodayCollectedAt
			}
		}
		if account.Used != nil {
			group.Used = sumKnown(group.Used, *account.Used)
			group.UsedAccounts++
		}
	}
	for _, row := range rows {
		index, ok := dateIndexes[row.Date]
		if !ok {
			continue
		}
		group := accountGroups[row.AccountID]
		if group == nil {
			continue
		}
		point := &group.History[index]
		if row.Balance != nil {
			point.Balance = sumKnown(point.Balance, *row.Balance)
			point.BalanceAccounts++
		}
		if row.TodayUsed != nil && row.TodayDate == row.Date {
			point.Consumption = sumKnown(point.Consumption, *row.TodayUsed)
			point.ConsumptionAccounts++
		}
	}
	result := make([]AccountSummary, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Currency == result[j].Currency {
			return false
		}
		if result[i].Currency == "USD" || result[j].Currency == "" {
			return true
		}
		if result[j].Currency == "USD" || result[i].Currency == "" {
			return false
		}
		return result[i].Currency < result[j].Currency
	})
	return result, nil
}
