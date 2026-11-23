package app

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDingtalkMarkdownEvents(t *testing.T) {
	created := time.Date(2026, 11, 23, 6, 5, 4, 0, time.UTC)
	cases := []struct {
		kind, detail, event string
		contains            []string
	}{
		{"low_balance", "余额 3.2800 USD，阈值 10.0000", "余额偏低", []string{"当前余额：**$3.28**", "告警阈值：$10.00"}},
		{"monitor_error", "余额接口请求超时", "采集失败", []string{"异常原因：余额接口请求超时", "本次未能更新账户余额。"}},
		{"recovered", "已重新获取账户余额", "采集恢复", []string{"本次已成功获取账户余额"}},
		{"rates_changed", "原有倍率变更详情", "倍率变化", []string{"原有倍率变更详情"}},
		{"announcement", "当前共 2 条公告：服务维护安排、分组调整说明", "公告更新", []string{"检测到上游公告发生变化", "当前共 2 条公告", "服务维护安排、分组调整说明"}},
		{"monitor_warning", "倍率采集失败：超时；公告采集失败：HTTP 503", "部分数据采集异常", []string{"余额采集成功", "异常详情", "倍率采集失败：超时  \n公告采集失败：HTTP 503"}},
		{"monitor_warning_recovered", "消费、倍率、公告和订阅等本次采集项目已正常返回", "扩展数据采集恢复", []string{"本次采集项目已正常返回"}},
		{"channel_auto_disabled", "渠道“DeepSeek”：连续两次未找到上游分组\n渠道“Claude”：连续两次未找到上游分组", "渠道已自动停用", []string{"渠道“DeepSeek”", "\n\n渠道“Claude”", "当前状态：**已停用**"}},
		{"subscription_expiring", "到期时间 2026-11-26T18:00:00+08:00", "订阅即将到期", []string{"到期时间：**2026-11-26 18:00:00（北京时间）**"}},
		{"subscription_daily", "daily 剩余 8.0%", "日额度余量偏低", []string{"当日剩余额度：**8.0%**"}},
		{"subscription_weekly", "weekly 剩余 6.5%", "周额度余量偏低", []string{"本周剩余额度：**6.5%**"}},
		{"subscription_monthly", "monthly 剩余 4.2%", "月额度余量偏低", []string{"本月剩余额度：**4.2%**"}},
		{"test", "告警通道连接正常", "通知测试", []string{"这是一条手动测试消息", "看到此消息，说明本次测试已到达接收端"}},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			seen[tc.kind] = true
			alert := Alert{Kind: tc.kind, Title: "睿云桥 · 原标题", Detail: tc.detail, CreatedAt: created}
			title, body := dingtalkMarkdown(alert, false)
			wantTitle := "睿云桥 · " + tc.event
			if tc.kind == "test" {
				wantTitle = "通知测试"
			}
			if title != wantTitle || !strings.HasPrefix(body, "### "+title+"\n\n") || strings.Contains(body, "LedgerGate") {
				t.Fatal("incorrect account/event heading", title, body)
			}
			for _, value := range append(tc.contains, "检测时间：2026-11-23 14:05:04（北京时间）") {
				if !strings.Contains(body, value) {
					t.Fatal("missing event detail", value, body)
				}
			}
			if tc.kind == "announcement" && strings.Contains(body, "新增 2 条") {
				t.Fatal("current announcement count was presented as new announcements")
			}
			if strings.HasPrefix(tc.kind, "subscription_") && tc.kind != "subscription_expiring" && strings.Contains(body, "$") {
				t.Fatal("subscription amount was invented")
			}
		})
	}
	for kind := range notificationKinds {
		if !seen[kind] {
			t.Error("notification kind has no formatting coverage", kind)
		}
	}
}

func TestDingtalkMarkdownRateChanges(t *testing.T) {
	change := func(before, after float64) rateChange {
		return rateChange{Group: "D003 deepseek 官K", Kind: "changed", Before: &before, After: &after}
	}
	cases := []struct {
		name, event string
		changes     []rateChange
		contains    []string
	}{
		{"down", "倍率下调", []rateChange{change(0.4, 0.35), {Group: "D002 deepseek 高缓", Kind: "changed", Before: floatPointer(0.3), After: floatPointer(0.28)}}, []string{"0.40× → **0.35×**　↓ 12.5%", "0.30× → **0.28×**　↓ 6.7%", "**D002 deepseek 高缓**"}},
		{"up", "倍率上调", []rateChange{change(0.3, 0.36)}, []string{"0.30× → **0.36×**　↑ 20.0%"}},
		{"mixed", "倍率调整", []rateChange{change(0.3, 0.36), change(0.4, 0.35)}, []string{"↑ 20.0%", "↓ 12.5%"}},
		{"added", "分组新增", []rateChange{{Kind: "added", GroupID: "new", After: floatPointer(0.5)}}, []string{"**new**", "新增分组 · 倍率 **0.50×**"}},
		{"removed", "分组移除", []rateChange{{Kind: "removed", Group: "旧分组", Before: floatPointer(0.28)}}, []string{"已移除 · 原倍率 0.28×"}},
		{"group-update", "分组更新", []rateChange{change(0.4, 0.35), {Kind: "added", Group: "新分组", After: floatPointer(0.5)}, {Kind: "removed", Group: "旧分组", Before: floatPointer(1)}}, []string{"新增分组", "已移除", "↓ 12.5%"}},
		{"zero-before", "倍率上调", []rateChange{change(0, 0.35)}, []string{"0.00× → **0.35×**"}},
		{"missing", "倍率调整", []rateChange{{Kind: "changed", Group: "缺失倍率", After: floatPointer(0.28)}}, []string{"未提供 → **0.28×**"}},
		{"unknown-added", "分组新增", []rateChange{{Kind: "added"}}, []string{"未命名分组", "未提供"}},
		{"precision", "倍率下调", []rateChange{change(0.0004, 0.00035)}, []string{"0.0004× → **0.00035×**", "↓ 12.5%"}},
		{"tiny-change", "倍率上调", []rateChange{change(1, 1.00001)}, []string{"↑ 小于 0.1%"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alert := Alert{Kind: "rates_changed", Title: "睿云桥 · 倍率发生变化", Detail: rateChangesDetail(tc.changes)}
			title, body := dingtalkMarkdown(alert, false)
			if title != "睿云桥 · "+tc.event {
				t.Fatal("wrong change classification", title)
			}
			for _, value := range tc.contains {
				if !strings.Contains(body, value) {
					t.Fatal("missing change detail", value, body)
				}
			}
			if (tc.name == "zero-before" || tc.name == "missing") && strings.Contains(body, "%") {
				t.Fatal("percent was invented for zero or missing rate", body)
			}
		})
	}
}

func TestDingtalkMarkdownHistoricalValuesAndLimits(t *testing.T) {
	created := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ detail, want string }{
		{"余额 0.0001 CNY，阈值 0.0010", "当前余额：**0.0001 CNY**"},
		{"余额 -2.5000 积分，阈值 10.0000", "当前余额：**-2.50 积分**"},
		{"旧版余额详情", "旧版余额详情"},
	} {
		_, body := dingtalkMarkdown(Alert{Kind: "low_balance", Detail: tc.detail}, false)
		if !strings.Contains(body, tc.want) || !strings.Contains(body, "检测时间：未记录") {
			t.Fatal("historical details or precision lost", body)
		}
	}
	for _, tc := range []struct {
		expires string
		created time.Time
		want    string
	}{
		{"2026-11-22T18:00:00+08:00", created, "订阅已到期"},
		{"2026-11-23T14:00:00+08:00", created, "订阅已到期"},
		{"2026-11-26T18:00:00+08:00", created, "订阅即将到期"},
		{"2026-11-26T18:00:00+08:00", time.Time{}, "订阅到期提醒"},
		{"原有到期时间", created, "订阅到期提醒"},
	} {
		title, body := dingtalkMarkdown(Alert{Kind: "subscription_expiring", Title: "睿云桥 · 主账号 · 订阅", Detail: "到期时间 " + tc.expires, CreatedAt: tc.created}, false)
		if title != "睿云桥 · 主账号 · "+tc.want {
			t.Fatal("expiration was not classified at event time", title)
		}
		if tc.expires == "原有到期时间" && !strings.Contains(body, tc.expires) {
			t.Fatal("old expiration detail lost")
		}
	}
	for _, keyword := range []bool{false, true} {
		_, body := dingtalkMarkdown(Alert{Title: "原有事件", Detail: strings.Repeat("中文", 20000), CreatedAt: created}, keyword)
		if len(body) > 20000 || !utf8.ValidString(body) || !strings.Contains(body, "检测时间：2026-11-23 14:00:00") || strings.Contains(body, "LedgerGate") != keyword {
			t.Fatal("size, timestamp or keyword compatibility broken", len(body))
		}
	}
}

func TestDingtalkMarkdownEscapesUpstreamContent(t *testing.T) {
	value := "**伪造告警** [链接](https://bad.invalid) ![跟踪图](https://bad.invalid/pixel) <img src=x> &\r\n# 注入标题"
	title, body := dingtalkMarkdown(Alert{Title: "上游\r\n### 注入 · 原标题", Detail: value}, false)
	for _, unsafe := range []string{"**伪造告警**", "[链接](", "![跟踪图](", "<img", "\n# 注入标题", "\r"} {
		if strings.Contains(body, unsafe) {
			t.Fatal("upstream content became Markdown markup", unsafe, body)
		}
	}
	if strings.ContainsAny(title, "\r\n") || !strings.Contains(body, "&lt;img src=x&gt;") || !strings.Contains(body, "\\*\\*伪造告警\\*\\*") {
		t.Fatal("escaped content lost or title is multiline", title, body)
	}
	alert := Alert{Kind: "rates_changed", Title: "上游 · 倍率", Detail: rateChangesDetail([]rateChange{{Kind: "added", Group: value, After: floatPointer(0.5)}})}
	_, body = dingtalkMarkdown(alert, false)
	if strings.Contains(body, "![跟踪图](") || strings.Contains(body, "<img") {
		t.Fatal("group name bypassed Markdown escaping", body)
	}
}
