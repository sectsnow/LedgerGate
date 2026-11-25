import {test} from "node:test";
import assert from "node:assert/strict";
import {budgetMicros} from "../src/moneyInput.ts";
import {qualityAdvice} from "../src/qualityAdvice.ts";
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
