import assert from "node:assert/strict";
import test from "node:test";
import { requestTrendFormat, requestTrendMetrics, requestTrendValue } from "../src/requestTrendData.ts";

test("trend metrics preserve independent units and recorded zero values", () => {
  assert.deepEqual(Object.values(requestTrendMetrics).map(metric => metric.series.map(item => item.key)), [
    ["requests", "errors"], ["input_tokens", "output_tokens"], ["cost_micros"],
  ]);
  const point = { requests: 5, errors: 2, input_tokens: 1001, output_tokens: 0, cost_micros: 1, cost_unknown_requests: 0 };
  for (const key of ["requests", "errors", "input_tokens", "output_tokens", "cost_micros"]) assert.equal(requestTrendValue(point, key), point[key]);
  assert.equal(requestTrendFormat("requests", 1234), "1,234");
  assert.equal(requestTrendFormat("tokens", 1001), "1,001");
  assert.equal(requestTrendFormat("cost", 1), "$0.000001");
  assert.equal(requestTrendFormat("cost", 0), "$0.00");
  assert.equal(requestTrendFormat("cost", 1234567), "$1.234567");
});

test("unknown costs stay distinct from zero and partial estimated subtotals", () => {
  const point = { requests: 2, cost_micros: 0, cost_unknown_requests: 2 };
  assert.equal(requestTrendValue(point, "cost_micros"), null);
  assert.equal(requestTrendValue({ ...point, cost_unknown_requests: 3 }, "cost_micros"), null);
  assert.equal(requestTrendValue({ ...point, cost_micros: 120, cost_unknown_requests: 1 }, "cost_micros"), 120);
  assert.equal(requestTrendValue({ ...point, requests: 0, cost_unknown_requests: 0 }, "cost_micros"), 0);
  assert.equal(requestTrendValue({ ...point, cost_unknown_requests: 0 }, "cost_micros"), 0);
  assert.equal(requestTrendValue({ requests: 3, cost_micros: 0 }, "cost_micros"), null);
  for (const key of ["errors", "input_tokens", "output_tokens", "cost_micros"]) assert.equal(requestTrendValue({ requests: 3 }, key), null);
});
