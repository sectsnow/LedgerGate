import {test} from "node:test";
import assert from "node:assert/strict";
import {widgetAlerts} from "../src/alerts.ts";

const now = Date.parse("2026-11-25T03:00:00Z");
const overview = {date:"2026-11-25",accounts:[],period:{days:30,requests:1000,errors:1,cost_unknown_requests:2,retention_days:30}};
test("历史失败、费用未知和覆盖提示保留为周期说明，不增加当前关注",()=>{
  const value=widgetAlerts(overview,30,"",now,now);
  assert.deepEqual(value.issues,[]);
  assert.deepEqual(value.notes.map(item=>item.title),["周期请求错误","周期费用未完整核算"]);
  assert.match(value.notes[0].detail,/0.1%/);
  assert.equal(value.snapshotStale,false);
  assert.deepEqual(widgetAlerts(overview,7,"",now,now).notes,[]);
  assert.equal(widgetAlerts({...overview,period:{...overview.period,retention_days:7}},30,"",now,now).notes.length,3);
  assert.match(widgetAlerts({...overview,period:{...overview.period,requests:0}},30,"",now,now).notes[0].detail,/错误率 —/);
});
test("连接失败、低余额和旧快照仍需关注，暂停账户及已恢复状态不计异常",()=>{
  const account={enabled:false,status:"error",has_warning:true,balance:null,today_used:null,checked_at:null};
  assert.equal(widgetAlerts({...overview,accounts:[account]},30,"",now,now).issues.length,0);
  const value=widgetAlerts({...overview,accounts:[{...account,enabled:true,status:"low"}]},30,"连接失败",now-90001,now);
  assert.deepEqual(value.issues.map(item=>item.title),["连接或操作异常","账户状态提醒","旧数据"]);
  assert.equal(widgetAlerts({...overview,date:"2026-11-24"},30,"",now,now).snapshotStale,true);
  assert.equal(widgetAlerts(overview,30,"",now,now).issues.length,0);
  assert.equal(widgetAlerts(null,1,"连接失败",null,now).issues.length,1);
  assert.equal(widgetAlerts({date:overview.date,accounts:[]},1,"",now,now).notes[0].title,"服务版本较旧");
});
