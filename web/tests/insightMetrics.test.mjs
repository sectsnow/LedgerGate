import { test } from "node:test";
import assert from "node:assert/strict";
import { cacheTrendPlot, insightMetrics } from "../src/insightMetrics.ts";

test("重试挽回、节省费用和覆盖率区分零值、未知及净增费用", () => {
  const report = { samples: 8, retry_eligible: 4, retry_rescued: 2, wait_samples: 8, wait_ms: 4000, savings_samples: 3, baseline_micros: 10000, actual_micros: 12000 };
  assert.deepEqual(insightMetrics(report, 10), { coverage: 80, retryRate: 50, waitAverage: 500, savings: -.002, savingsCoverage: 30 });
  assert.equal(insightMetrics({ ...report, retry_rescued: 0 }, 10).retryRate, 0);
  assert.equal(insightMetrics({ ...report, retry_eligible: 0 }, 10).retryRate, null);
  assert.equal(insightMetrics({ ...report, savings_samples: 0 }, 10).savings, null);
  assert.equal(insightMetrics(report, 0).coverage, null);
  assert.equal(insightMetrics({ ...report, requests: 20 }, 10).coverage, 40);
});

test("缓存趋势使用同批样本加权，未知日期断开，明确零命中保留", () => {
  const rows = [{ date:"1",samples:2,input_tokens:1000,read_tokens:900 },{ date:"2",samples:0,input_tokens:0,read_tokens:0 },{ date:"3",samples:1,input_tokens:100,read_tokens:0 }];
  const chart = cacheTrendPlot(rows);
  assert.deepEqual(chart.points.map(row => row.rate), [90,null,0]);
  assert.equal(chart.segments.length, 2);
  assert.equal(cacheTrendPlot([]).segments.length, 0);
  assert.equal(cacheTrendPlot([{...rows[0],input_tokens:0}]).points[0].rate, null);
});
