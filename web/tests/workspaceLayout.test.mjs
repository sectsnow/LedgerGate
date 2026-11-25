import {test} from "node:test";
import assert from "node:assert/strict";
import {workspacePage,workspaceResources,workspacePollInterval} from "../src/workspaceNavigation.ts";
import {analysisSelection,readAnalysisPeriod} from "../src/analysisPeriod.ts";
import {settingsSection} from "../src/settingsSection.ts";
import {dashboardIssues} from "../src/dashboardIssues.ts";
import {qualityPreferences} from "../src/qualityMetrics.ts";

test("旧监控链接归并，设置与分析子页可直接打开，无效路径回退",()=>{
  assert.equal(workspacePage("alerts"),"monitor/alerts");
  assert.equal(workspacePage("rate-changes"),"monitor/rates");
  assert.equal(workspacePage("announcements"),"monitor/announcements");
  assert.equal(workspacePage("settings"),"settings/general");
  assert.equal(workspacePage("analytics/cache/detail?group_by=model&detail=a%3Fb"),"analytics/cache/detail?group_by=model&detail=a%3Fb");
  assert.equal(workspacePage("accounts/7/channels"),"accounts/7/channels");
  for(const path of ["unknown","settings/unknown","analytics/unknown","accounts/bad/channels"])assert.equal(workspacePage(path),"overview");
});
test("各页面只取所需资源，分析详情不拉首页，请求页复用自己的轮询",()=>{
  assert.deepEqual(workspaceResources("settings/widget"),["settings","build"]);
  assert.deepEqual(workspaceResources("analytics/cache/detail?detail=1"),["settings","build"]);
  assert.deepEqual(workspaceResources("requests"),["settings","build","channels"]);
  assert.equal(workspaceResources("analytics/cache").includes("accounts"),false);
  assert.equal(workspaceResources("analytics/cost").includes("accounts"),true);
  assert.equal(workspaceResources("overview").includes("groups"),false);
  assert.equal(workspacePollInterval("overview"),15000);
  for(const path of ["analytics/summary","analytics/cache/detail","settings/runtime","requests"])assert.equal(workspacePollInterval(path),0);
});
test("今天与近24小时分别统计，筛选设置损坏或日期无效时安全回退",()=>{
  const now=Date.parse("2026-11-24T16:30:00Z");
  const day={requests:7,errors:1,input_tokens:100,output_tokens:20,cost_micros:50,cost_unknown_requests:0};
  const overview={...day,requests:20,account_summaries:[],activity:[{...day,date:"2026-11-24",requests:13},{...day,date:"2026-11-25"}],series:[]};
  assert.equal(analysisSelection(overview,"today",now).totals.requests,7);
  assert.equal(analysisSelection(overview,1,now).totals.requests,20);
  assert.equal(analysisSelection(overview,"today",now).points.length,1);
  assert.equal(analysisSelection(overview,7,now).range,"start=2026-11-24&end=2026-11-25");
  assert.equal(readAnalysisPeriod("broken"),1);
  assert.equal(readAnalysisPeriod('{"start":"2026-02-30","end":"2026-03-01"}'),1);
  assert.equal(readAnalysisPeriod('{"start":"2026-11-25","end":"2026-11-01"}'),1);
  assert.deepEqual(readAnalysisPeriod('{"start":"2026-11-01","end":"2026-11-25"}'),{start:"2026-11-01",end:"2026-11-25"});
  assert.deepEqual(qualityPreferences('{"group":"model","model":"x","sort":"cache"}'),{group:"model",model:"x",sort:"cache"});
  assert.deepEqual(qualityPreferences('{"group":"bad","model":{},"sort":"bad"}'),{group:"channel",model:"",sort:"requests"});
});
test("分区保存设置只覆盖当前区字段，保留服务器其他参数和通知配置",()=>{
  const current={runtime:{first_output_seconds:80,notification_retries:7,request_digest_days:14,notification_events:["low_balance"]},alert_cooldown_minutes:60,retention_days:30,request_summary_retention_days:365};
  const draft={runtime:{first_output_seconds:10,notification_retries:3,request_digest_days:7,notification_events:[]},alert_cooldown_minutes:90,retention_days:15,request_summary_retention_days:180};
  const notify=settingsSection(current,draft,"notifications");
  assert.equal(notify.runtime.first_output_seconds,80);assert.equal(notify.runtime.notification_retries,3);assert.equal(notify.alert_cooldown_minutes,90);assert.deepEqual(notify.runtime.notification_events,["low_balance"]);
  const data=settingsSection(current,draft,"data");
  assert.equal(data.retention_days,15);assert.equal(data.runtime.request_digest_days,7);assert.equal(data.runtime.notification_retries,7);
  const runtime=settingsSection(current,draft,"runtime");assert.equal(runtime.runtime.first_output_seconds,10);assert.equal(runtime.alert_cooldown_minutes,60);
});
test("首页只提示当前异常，暂停账户、手动禁用渠道和正常待发送不算问题",()=>{
  const data={accounts:[{account:{id:1,name:"暂停",enabled:false,status:"error",warnings:"old"}},{account:{id:2,name:"余额低",enabled:true,status:"low",warnings:""}}],channels:[{channel:{id:1,name:"禁用",enabled:false},monitoring_paused:false,cooldown_until:"2099-01-01T00:00:00Z"},{channel:{id:2,name:"冷却",enabled:true},monitoring_paused:false,cooldown_until:"2099-01-01T00:00:00Z"}],alerts:{items:[{id:1,title:"等待发送",delivery:"pending"},{id:2,title:"失败",delivery:"failed"}],pending:2}};
  assert.deepEqual(dashboardIssues(data,Date.parse("2026-11-25")).map(row=>row.id),["account-2","channel-2","alert-2"]);
  data.channels.push({channel:{id:3,name:"自动停用",enabled:false,auto_disabled_reason:"auth"},monitoring_paused:false,cooldown_until:"2000-01-01T00:00:00Z"});
  assert.equal(dashboardIssues(data,Date.parse("2026-11-25")).find(row=>row.id==="channel-3")?.detail,"渠道已自动停用，请检查后手动恢复");
  data.channels[2].monitoring_paused=true;
  assert.equal(dashboardIssues(data,Date.parse("2026-11-25")).some(row=>row.id==="channel-3"),false);
});
