import { test } from "node:test";
import assert from "node:assert/strict";
import { amount, finances } from "../src/summary.ts";

test("按币种汇总已知金额并保留缺失、过期和采集异常", () => {
  const result = finances({ date: "2026-11-24", requests: 4, input_tokens: 100, output_tokens: 20, accounts: [
    { currency: " usd ", balance: 10, today_used: 2, today_date: "2026-11-24", status: "ok", enabled: true, checked_at: "2026-11-24T08:00:00Z", has_warning: false },
    { currency: "USD", balance: 0, today_used: null, today_date: "2026-11-24", status: "error", enabled: true, checked_at: "2026-11-24T07:00:00Z", has_warning: false },
    { currency: "CNY", balance: null, today_used: 99, today_date: "2026-11-23", status: "ok", enabled: false, checked_at: null, has_warning: false },
  ] });
  assert.equal(result.length, 2);
  assert.deepEqual(result[0], { currency: "USD", balance: 10, today: 2, accounts: 2, balanceCount: 2, todayCount: 1, attention: true, oldestCheck: Date.parse("2026-11-24T07:00:00Z") });
  assert.equal(result[1].currency, "CNY");
  assert.equal(result[1].balance, null);
  assert.equal(result[1].today, null);
  assert.equal(result[1].attention, true);
  assert.equal(amount(0, "USD"), "$0.00");
  assert.equal(amount(null, "CNY"), "—");
  assert.equal(amount(12.5, "CNY"), "12.50 CNY");
});

test("正常账户的单项采集提醒不会被忽略", () => {
  const account = { currency: "USD", balance: 10, today_used: 2, today_date: "2026-11-24", status: "ok", enabled: true, checked_at: "2026-11-24T08:00:00Z", has_warning: true };
  const overview = { date: "2026-11-24", requests: 0, input_tokens: 0, output_tokens: 0, accounts: [account] };
  assert.equal(finances(overview)[0].attention, true);
  account.has_warning = false;
  assert.equal(finances(overview)[0].attention, false);
});
