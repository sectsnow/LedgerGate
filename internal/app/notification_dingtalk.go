package app

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
)

func dingtalkEscape(value string) string {
	value = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(value)
	value = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#", "!", "\\!", "|", "\\|", "+", "\\+", "-", "\\-").Replace(html.EscapeString(value))
	return strings.ReplaceAll(value, "\n", "  \n")
}

func dingtalkDecimal(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		return text + ".00"
	}
	if len(text)-strings.IndexByte(text, '.') == 2 {
		return text + "0"
	}
	return text
}

func dingtalkRateChanges(changes []rateChange) (string, string) {
	rate := func(value *float64) string {
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
			return "未提供"
		}
		return dingtalkDecimal(*value) + "×"
	}
	var up, down, added, removed int
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		name := strings.TrimSpace(change.Group)
		if name == "" {
			name = change.GroupID
		}
		if name == "" {
			name = "未命名分组"
		}
		line := "**" + dingtalkEscape(strings.Join(strings.Fields(name), " ")) + "**  \n"
		switch change.Kind {
		case "added":
			added++
			line += "新增分组 · 倍率 **" + rate(change.After) + "**"
		case "removed":
			removed++
			line += "已移除 · 原倍率 " + rate(change.Before)
		default:
			line += rate(change.Before) + " → **" + rate(change.After) + "**"
			if rate(change.Before) != "未提供" && rate(change.After) != "未提供" {
				arrow := ""
				if *change.After > *change.Before {
					up++
					arrow = "↑"
				} else if *change.After < *change.Before {
					down++
					arrow = "↓"
				}
				if *change.Before > 0 && arrow != "" {
					percent := math.Abs((*change.After / *change.Before - 1) * 100)
					if !math.IsInf(percent, 0) && !math.IsNaN(percent) {
						amount := fmt.Sprintf("%.1f%%", percent)
						if percent < 0.1 {
							amount = "小于 0.1%"
						}
						line += "　" + arrow + " " + amount
					}
				}
			}
		}
		lines = append(lines, line)
	}
	event := "倍率调整"
	switch {
	case added == len(changes):
		event = "分组新增"
	case removed == len(changes):
		event = "分组移除"
	case added+removed > 0:
		event = "分组更新"
	case up == len(changes):
		event = "倍率上调"
	case down == len(changes):
		event = "倍率下调"
	}
	return event, strings.Join(lines, "\n\n")
}

func dingtalkMarkdown(alert Alert, keyword bool) (string, string) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	item := describeAlert(alert)
	event, body := "", dingtalkEscape(item.Detail)
	switch alert.Kind {
	case "low_balance":
		event = "余额偏低"
		balance, threshold, ok := strings.Cut(alert.Detail, "，阈值 ")
		amount, currency, hasCurrency := strings.Cut(strings.TrimPrefix(balance, "余额 "), " ")
		current, currentErr := strconv.ParseFloat(amount, 64)
		minimum, minimumErr := strconv.ParseFloat(threshold, 64)
		if ok && hasCurrency && strings.HasPrefix(balance, "余额 ") && currentErr == nil && minimumErr == nil && !math.IsNaN(current) && !math.IsInf(current, 0) && !math.IsNaN(minimum) && !math.IsInf(minimum, 0) {
			money := func(value float64) string {
				if currency == "USD" {
					return "$" + dingtalkDecimal(value)
				}
				return dingtalkDecimal(value) + " " + dingtalkEscape(currency)
			}
			body = "当前余额：**" + money(current) + "**  \n告警阈值：" + money(minimum)
		}
	case "monitor_error":
		event = "采集失败"
		body = "异常原因：" + body + "\n\n本次未能更新账户余额。"
	case "recovered":
		event = "采集恢复"
		body = "余额采集已恢复，本次已成功获取账户余额。"
	case "rates_changed":
		event = "倍率变化"
		if len(item.Changes) > 0 {
			event, body = dingtalkRateChanges(item.Changes)
		}
	case "announcement":
		event = "公告更新"
		body = "检测到上游公告发生变化。\n\n" + body
	case "monitor_warning":
		event = "部分数据采集异常"
		body = "余额采集成功。\n\n异常详情：  \n" + dingtalkEscape(strings.ReplaceAll(alert.Detail, "；", "\n"))
	case "monitor_warning_recovered":
		event = "扩展数据采集恢复"
	case "channel_auto_disabled":
		event = "渠道已自动停用"
		body = strings.ReplaceAll(body, "  \n", "\n\n") + "\n\n当前状态：**已停用**"
	case "subscription_expiring":
		event = "订阅到期提醒"
		expires, err := time.Parse(time.RFC3339, strings.TrimPrefix(alert.Detail, "到期时间 "))
		if err == nil {
			if !alert.CreatedAt.IsZero() {
				if expires.After(alert.CreatedAt) {
					event = "订阅即将到期"
				} else {
					event = "订阅已到期"
				}
			}
			body = "到期时间：**" + expires.In(location).Format("2006-01-02 15:04:05") + "（北京时间）**"
		}
	case "subscription_daily", "subscription_weekly", "subscription_monthly":
		window := strings.TrimPrefix(alert.Kind, "subscription_")
		period := map[string]string{"daily": "日", "weekly": "周", "monthly": "月"}[window]
		event = period + "额度余量偏低"
		if remaining, ok := strings.CutPrefix(alert.Detail, window+" 剩余 "); ok {
			body = map[string]string{"daily": "当日", "weekly": "本周", "monthly": "本月"}[window] + "剩余额度：**" + dingtalkEscape(remaining) + "**"
		}
	case "test":
		event = "通知测试"
		body = "这是一条手动测试消息。  \n看到此消息，说明本次测试已到达接收端。"
	}
	title := alert.Title
	if event != "" {
		title = event
		if index := strings.LastIndex(alert.Title, " · "); index >= 0 && alert.Kind != "test" {
			title = alert.Title[:index] + " · " + event
		}
	}
	title = notificationText(strings.Join(strings.Fields(title), " "), 512)
	stamp := "未记录"
	if !alert.CreatedAt.IsZero() {
		stamp = alert.CreatedAt.In(location).Format("2006-01-02 15:04:05") + "（北京时间）"
	}
	heading, footer := "### "+dingtalkEscape(title)+"\n\n", "\n\n---\n\n检测时间："+stamp
	if keyword {
		footer += "\n\nLedgerGate"
	}
	return title, heading + notificationText(body, 20000-len(heading)-len(footer)) + footer
}
