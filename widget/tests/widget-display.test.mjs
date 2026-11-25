import { test } from "node:test";
import assert from "node:assert/strict";
import { badgeGeometry, badgeOffset, badgeReading, dockSide, expandGeometry, parseBadgeState, parseDisplayPrefs, windowOperationQueue } from "../src/widgetDisplay.ts";

const primary = { position: { x: 0, y: 0 }, size: { width: 1920, height: 1080 }, workArea: { position: { x: 0, y: 0 }, size: { width: 1920, height: 1040 } }, scaleFactor: 1 };
const secondary = { position: { x: -2560, y: 0 }, size: { width: 2560, height: 1440 }, workArea: { position: { x: -2560, y: 0 }, size: { width: 2560, height: 1400 } }, scaleFactor: 1.5 };
const panel = { x: 1510, y: 850, width: 400, height: 720, scaleFactor: 1 };

test("只在显示器外边缘吸附，内部接缝不收起，退出阈值避免抖动", () => {
  assert.equal(dockSide(panel, [primary]), "right");
  assert.equal(dockSide({ ...panel, x: 1490 }, [primary]), null);
  assert.equal(dockSide({ ...panel, x: 1490 }, [primary], "right"), "right");
  assert.equal(dockSide({ ...panel, x: 1480 }, [primary], "right"), null);
  assert.equal(dockSide({ ...panel, x: 1 }, [primary, secondary]), null);
  assert.equal(dockSide({ ...panel, x: -2550, scaleFactor: 1.5 }, [primary, secondary]), "left");
});

test("浮标与展开面板分别计算边界，不把小窗尺寸写入面板", () => {
  const badge = badgeGeometry(panel, [primary], "right");
  assert.deepEqual(badge, { x: 1848, y: 850, width: 72, height: 56, scaleFactor: 1 });
  assert.equal(badgeOffset(badge, [primary]), 850 / 984);
  const expanded = expandGeometry(panel, badge, [primary], "right");
  assert.equal(expanded.width, 400); assert.equal(expanded.height, 720);
  assert.ok(expanded.y + expanded.height <= 1040);
  const restored = badgeGeometry({ ...badge, x: -5000 }, [primary], "right", .5);
  assert.equal(restored.x, 1848); assert.equal(restored.y, 492);
  const scaled = badgeGeometry({ ...panel, x: -2000, scaleFactor: 1.5 }, [primary, secondary], "left", .5);
  assert.equal(scaled.x, -2560); assert.equal(scaled.scaleFactor, 1.5); assert.equal(scaled.height, 56);
});

test("配置损坏安全回退，浮标的尺寸不受完整面板最小尺寸钳制", () => {
  assert.equal(parseBadgeState("bad").mode, "panel");
  assert.equal(parseBadgeState('{"mode":"badge"}').mode, "panel");
  assert.equal(parseBadgeState(JSON.stringify({ mode: "badge", geometry: { x: 1e12, y: 0, scaleFactor: 1 } })).mode, "panel");
  assert.equal(parseBadgeState(JSON.stringify({ mode: "badge", side: "right", offset: .6, geometry: { x: 0, y: 2, scaleFactor: 1.5 } })).geometry.width, 72);
  assert.deepEqual(parseDisplayPrefs('{"metric":"bad","metrics":[],"currency":" usd ","autoHide":false}').metrics, ["tokens"]);
  assert.equal(parseDisplayPrefs('{"autoHide":false}').autoHide, false);
  assert.equal(parseDisplayPrefs("bad").rotate, false);
});

const now = Date.parse("2026-11-25T02:00:00Z");
const overview = { date: "2026-11-25", requests: 99, input_tokens: 25908996, output_tokens: 18066, period: { days: 30, input_tokens: 999999999, output_tokens: 99999 }, accounts: [
  { currency: "USD", balance: 12.34, today_used: 1.2, today_date: "2026-11-25", status: "ok", enabled: true, checked_at: "2026-11-25T02:00:00Z", has_warning: false },
  { currency: "CNY", balance: 99, today_used: 20, today_date: "2026-11-24", status: "error", enabled: true, checked_at: null, has_warning: false },
] };
test("浮标今日数据不使用面板30天总量，多币种金额需要明确选择", () => {
  assert.equal(badgeReading(overview, "tokens", "", now, "", now).value, "25.9M");
  assert.equal(badgeReading(overview, "requests", "", now, "", now).value, "99");
  assert.equal(badgeReading(overview, "balance", "", now, "", now).value, "—");
  assert.equal(badgeReading(overview, "balance", "USD", now, "", now).value, "$12.3");
  assert.equal(badgeReading(overview, "today", "CNY", now, "", now).value, "—");
  assert.equal(badgeReading(overview, "balance", "USD", now, "", now).status, "ok");
});

test("断线、跨日和过期保留旧值并标记，未知不冒充零", () => {
  const stale = badgeReading(overview, "requests", "", now - 91_000, "", now);
  assert.equal(stale.value, "99"); assert.equal(stale.status, "stale");
  assert.equal(badgeReading(overview, "requests", "", now, "凭证失效", now).status, "stale");
  assert.equal(badgeReading({ ...overview, date: "2026-11-24" }, "tokens", "", now, "", now).status, "stale");
  assert.equal(badgeReading(null, "tokens", "", null, "", now).value, "—");
  assert.equal(badgeReading({ ...overview, requests: 0 }, "requests", "", now, "", now).value, "0");
});

test("窗口操作串行执行，失败不会阻塞恢复操作", async () => {
  const enqueue = windowOperationQueue(); const calls = [];
  let release; const waiting = new Promise(resolve => { release = resolve; });
  const first = enqueue(async () => { calls.push("shrink"); await waiting; throw Error("native failed"); });
  const recovered = enqueue(async () => { calls.push("restore"); return "panel"; });
  await Promise.resolve(); assert.deepEqual(calls, ["shrink"]);
  release(); await assert.rejects(first, /native failed/);
  assert.equal(await recovered, "panel"); assert.deepEqual(calls, ["shrink", "restore"]);
});
