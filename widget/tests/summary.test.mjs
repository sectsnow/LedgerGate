import { test } from "node:test";
import assert from "node:assert/strict";
import { amount, finances, accountNotes, displayAmount } from "../src/summary.ts";

test("按币种汇总已知金额并保留缺失、过期和采集异常", () => {
  const result = finances({ date: "2026-11-24", requests: 4, input_tokens: 100, output_tokens: 20, accounts: [
    { currency: " usd ", balance: 10, today_used: 2, today_date: "2026-11-24", status: "ok", enabled: true, checked_at: "2026-11-24T08:00:00Z", has_warning: false },
    { currency: "USD", balance: 0, today_used: null, today_date: "2026-11-24", status: "error", enabled: true, checked_at: "2026-11-24T07:00:00Z", has_warning: false },
    { currency: "CNY", balance: null, today_used: 99, today_date: "2026-11-23", status: "ok", enabled: false, checked_at: null, has_warning: false },
  ] });
  assert.equal(result.length, 2);
  assert.deepEqual(result[0], { currency: "USD", balance: 10, today: 2, accounts: 2, balanceCount: 2, todayCount: 1, attention: true, oldestCheck: Date.parse("2026-11-24T07:00:00Z"), checkedCount: 2, todayCheckedCount: 0, oldestTodayCheck: null });
  assert.equal(result[1].currency, "CNY");
  assert.equal(result[1].balance, null);
  assert.equal(result[1].today, null);
  assert.equal(result[1].attention, true);
  assert.equal(amount(0, "USD"), "$0.00");
  assert.equal(amount(null, "CNY"), "—");
  assert.equal(amount(12.5, "CNY"), "12.50 CNY");
});

test("消费时间只来自成功采集字段，检查时间不能冒充消费时间", () => {
  const account = { currency: "USD", balance: 10, today_used: 2, today_date: "2026-11-25", status: "ok", enabled: true, checked_at: "2026-11-25T03:00:00Z", today_collected_at: "2026-11-25T02:00:00Z" };
  const overview = { date: "2026-11-25", accounts: [account, { ...account, today_collected_at: null }] };
  let group = finances(overview)[0];
  assert.equal(group.oldestTodayCheck, Date.parse(account.today_collected_at));
  assert.equal(group.todayCheckedCount, 1); assert.equal(group.todayCount, 2);
  overview.accounts[1].today_date = "2026-11-24";
  group = finances(overview)[0]; assert.equal(group.todayCount, 1); assert.equal(group.todayCheckedCount, 1);
});

test("采集详情区分暂停、失败、跨日、缺项和超时，金额零值与精度保留", () => {
  const now = Date.parse("2026-11-25T03:00:00Z");
  const account = { enabled: true, status: "ok", balance: 0, today_used: 0, today_date: "2026-11-25", checked_at: "2026-11-25T02:59:00Z", interval_seconds: 300 };
  assert.deepEqual(accountNotes(account, "2026-11-25", now), []);
  assert.match(accountNotes({ ...account, enabled: false }, "2026-11-25", now).join(), /暂停/);
  assert.match(accountNotes({ ...account, status: "error" }, "2026-11-25", now).join(), /失败/);
  assert.match(accountNotes({ ...account, checked_at: "2026-11-25T02:00:00Z" }, "2026-11-25", now).join(), /两个采集周期/);
  assert.match(accountNotes({ ...account, today_date: "2026-11-24" }, "2026-11-25", now).join(), /跨日/);
  assert.match(accountNotes({ ...account, balance: null, today_used: null }, "2026-11-25", now).join(), /余额未采集.*今日消费未采集/);
  assert.equal(displayAmount(154.3993, "USD"), "$154.40");
  assert.equal(displayAmount(154.3993, "USD", true), "$154.3993");
  assert.equal(displayAmount(0, "USD"), "$0.00"); assert.equal(displayAmount(null, "USD"), "—");
});

test("正常账户的单项采集提醒不会被忽略", () => {
  const account = { currency: "USD", balance: 10, today_used: 2, today_date: "2026-11-24", status: "ok", enabled: true, checked_at: "2026-11-24T08:00:00Z", has_warning: true };
  const overview = { date: "2026-11-24", requests: 0, input_tokens: 0, output_tokens: 0, accounts: [account] };
  assert.equal(finances(overview)[0].attention, true);
  account.has_warning = false;
  assert.equal(finances(overview)[0].attention, false);
});

test("低余额账户在数据完整且新鲜时仍需关注，不把零余额自行判为告警", () => {
  const now = Date.parse("2026-11-25T03:00:00Z");
  const account = { currency: "USD", balance: 1, today_used: 0, today_date: "2026-11-25", status: "low", enabled: true, checked_at: new Date(now).toISOString(), today_collected_at: new Date(now).toISOString(), has_warning: false, interval_seconds: 300 };
  assert.deepEqual(accountNotes(account, "2026-11-25", now), ["余额低于已设置的告警阈值"]);
  assert.equal(finances({ date: "2026-11-25", accounts: [account] })[0].attention, true);
  assert.deepEqual(accountNotes({ ...account, status: "ok", balance: 0 }, "2026-11-25", now), []);
  assert.match(accountNotes({ ...account, enabled: false }, "2026-11-25", now).join(), /监控已暂停/);
});
