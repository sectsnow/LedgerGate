import { test } from "node:test";
import assert from "node:assert/strict";
import { clampWindowSize, parseWindowState, resolveWindowGeometry, serializeWindowState } from "../src/windowState.ts";

test("窗口尺寸限制能修复过小或过大的历史状态", () => {
  assert.deepEqual(clampWindowSize(120, 1200), { width: 360, height: 900 });
  assert.deepEqual(parseWindowState(JSON.stringify({ x: -1920.4, y: 30.8, width: 120, height: 1200, scaleFactor: 1.5 })), { x: -1920, y: 31, width: 360, height: 900, scaleFactor: 1.5 });
});

test("无效的窗口状态会被忽略，合法状态可往返保存", () => {
  assert.equal(parseWindowState(null), null);
  assert.equal(parseWindowState("{}"), null);
  const state = { x: -20, y: 40, width: 400, height: 408, scaleFactor: 1.25 };
  assert.deepEqual(parseWindowState(serializeWindowState(state)), state);
});

test("显示器消失或 DPI 改变时，窗口回到可见工作区并保持逻辑尺寸", () => {
  const primary = { position: { x: 0, y: 0 }, size: { width: 1920, height: 1080 }, workArea: { position: { x: 0, y: 0 }, size: { width: 1920, height: 1040 } }, scaleFactor: 1 };
  const secondary = { position: { x: 1920, y: 0 }, size: { width: 2560, height: 1440 }, workArea: { position: { x: 1920, y: 0 }, size: { width: 2560, height: 1400 } }, scaleFactor: 1.5 };
  assert.deepEqual(resolveWindowGeometry({ x: 2100, y: 100, width: 400, height: 408, scaleFactor: 1 }, [primary, secondary], 1), { x: 2100, y: 100, width: 400, height: 408, scaleFactor: 1.5 });
  assert.deepEqual(resolveWindowGeometry({ x: 5000, y: 500, width: 400, height: 408, scaleFactor: 1.5 }, [primary], 1), { x: 1508, y: 500, width: 400, height: 408, scaleFactor: 1 });
});

test("内容高度可伸缩，保留宽度并限制在显示器工作区内", () => {
  const monitor = { position: { x: 0, y: 0 }, size: { width: 1920, height: 1080 }, workArea: { position: { x: 0, y: 0 }, size: { width: 1920, height: 1040 } }, scaleFactor: 1.5 };
  const saved = { x: 100, y: 700, width: 440, height: 720, scaleFactor: 1.5 };
  const compact = resolveWindowGeometry(saved, [monitor], 1.5, monitor, 280);
  assert.equal(compact.width, 440);
  assert.equal(compact.height, 360);
  const expanded = resolveWindowGeometry(saved, [monitor], 1.5, monitor, 1500);
  assert.equal(expanded.width, 440);
  assert.equal(expanded.height, 669);
  assert.ok(expanded.y + expanded.height * expanded.scaleFactor <= 1040);
  assert.equal(resolveWindowGeometry(saved, [], 1.5, undefined, 2000).height, 900);
});
