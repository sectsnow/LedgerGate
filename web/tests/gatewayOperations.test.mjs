import {test} from "node:test";
import assert from "node:assert/strict";
import {budgetMicros} from "../src/moneyInput.ts";
import {qualityAdvice} from "../src/qualityAdvice.ts";
import {qualityMetrics} from "../src/qualityMetrics.ts";
test("美元预算精确转换，空白不限而零为零，拒绝非法或过大金额",()=>{
 assert.equal(budgetMicros(""),null);assert.equal(budgetMicros("0"),0);assert.equal(budgetMicros("1.000001"),1000001);
 for(const value of ["-1","NaN","1e5",".1","0.0000001","9007199254.740992"])assert.throws(()=>budgetMicros(value));
 assert.equal(budgetMicros("9007199254.740991"),Number.MAX_SAFE_INTEGER);
});
test("渠道建议要求同模型和足够样本，不用缺失延迟推荐最快",()=>{
 const rows=[{name:"stable",requests:20,successes:20,average_first_token_ms:null},{name:"fast",requests:40,successes:38,average_first_token_ms:400}];
 assert.match(qualityAdvice(rows,""),/选择同一个/);assert.match(qualityAdvice([rows[0]],"m"),/至少两个/);
 const advice=qualityAdvice(rows,"m");assert.match(advice,/成功率最高：stable/);assert.match(advice,/平均首字最短：fast/);assert.match(advice,/未自动更改路由/);
});
test("网页缓存和费用使用有效样本，零命中与历史未记录分开",()=>{
 const row={requests:10,unknown_costs:2,cost_micros:80000,cache_sample_requests:4,cache_sample_input_tokens:1000,cache_sample_read_tokens:900,cache_hit_requests:2,cache_write_sample_requests:1,observed_requests:5,first_attempt_successes:3};
 assert.deepEqual(qualityMetrics(row),{uncachedInput:100,cacheHitRate:90,cacheRequestRate:50,cacheCoverage:40,writeCoverage:10,firstSuccessRate:60,attemptCoverage:50,costCoverage:80,averageCost:.01});
 assert.equal(qualityMetrics({...row,cache_sample_read_tokens:0}).cacheHitRate,0);
 assert.equal(qualityMetrics({...row,cache_sample_input_tokens:0}).cacheHitRate,null);
 assert.equal(qualityMetrics({...row,cache_sample_requests:11}).cacheCoverage,null);
 assert.equal(qualityMetrics({...row,unknown_costs:10}).averageCost,null);
 const old=qualityMetrics({requests:10,unknown_costs:2,cost_micros:80000,observed_requests:0,first_attempt_successes:0});
 assert.equal(old.cacheHitRate,null);assert.equal(old.cacheCoverage,null);assert.equal(old.firstSuccessRate,null);
 assert.equal(qualityMetrics({...row,requests:0,unknown_costs:0}).costCoverage,null);
});
