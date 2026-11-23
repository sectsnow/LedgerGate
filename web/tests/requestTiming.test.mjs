import assert from "node:assert/strict";
import test from "node:test";
import { averageOutputTPS, requestTiming } from "../src/requestTiming.ts";

test("reasoning tokens use the full request duration for average TPS", () => {
  assert.equal(averageOutputTPS(5448, 172600)?.toFixed(2), "31.56");
  assert.equal(averageOutputTPS(1000, 20000), 50);
  for (const duration of [null, undefined, 0, -1, NaN, Infinity]) assert.equal(averageOutputTPS(5448, duration), null);
  for (const tokens of [null, undefined, 0, -1, NaN, Infinity]) assert.equal(averageOutputTPS(tokens, 172600), null);
});

test("semantic first word and visible content remain separate", () => {
  assert.deepEqual(requestTiming({ metrics_version: 2, first_token_ms: 16730, first_content_ms: 164340 }), { firstWordMS: 16730, firstContentMS: 164340 });
  assert.deepEqual(requestTiming({ metrics_version: 2, first_token_ms: 16730 }), { firstWordMS: 16730, firstContentMS: null });
});

test("legacy content timing is not presented as a measured semantic first word", () => {
  assert.deepEqual(requestTiming({ metrics_version: 1, first_token_ms: 164340 }), { firstWordMS: null, firstContentMS: 164340 });
  assert.deepEqual(requestTiming({ metrics_version: 0, first_token_ms: null }), { firstWordMS: null, firstContentMS: null });
});
