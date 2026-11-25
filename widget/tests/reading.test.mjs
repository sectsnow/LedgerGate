import { test } from "node:test";
import assert from "node:assert/strict";
import { parseReadingPrefs } from "../src/reading.ts";

test("阅读设置缺失或损坏时显示完整总览，折叠只接受已知分区", () => {
  for (const raw of [null, "bad", "null", "[]"]) assert.deepEqual(parseReadingPrefs(raw), { density: "comfortable", textSize: "standard", collapsed: [] });
  const prefs = parseReadingPrefs('{"density":"compact","textSize":"large","collapsed":["ranking","unknown","analysis","ranking"]}');
  assert.deepEqual(prefs, { density: "compact", textSize: "large", collapsed: ["ranking", "analysis"] });
  assert.deepEqual(parseReadingPrefs(JSON.stringify(prefs)), prefs);
  const nested = parseReadingPrefs('{"collapsed":["analysis","cache","latency","cache-cost","error-breakdown","cache-comparison","cache-trend","quality","unknown"]}');
  assert.equal(nested.collapsed.includes("unknown"), false);
  assert.equal(nested.collapsed.length, 8);
  assert.deepEqual(parseReadingPrefs(JSON.stringify(nested)), nested);
});
