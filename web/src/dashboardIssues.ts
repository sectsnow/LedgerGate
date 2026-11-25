import { channelEnabled, type Data } from "./api.ts";
export function dashboardIssues(data:Pick<Data,"accounts"|"channels"|"alerts">,now:number) {
  const issues:{id:string;title:string;detail:string;page:string}[]=[];
  for(const {account} of data.accounts) {
    if(!account.enabled)continue;
    const detail=account.status==="low" ? "余额低于已设置的门槛" : account.status==="error" ? "采集失败，请查看账户详情" : account.warnings?.trim() ? "部分项目采集异常" : "";
    if(detail)issues.push({id:`account-${account.id}`,title:account.name,detail,page:"accounts"});
  }
  for(const row of data.channels) {
    if(row.monitoring_paused)continue;
    if(row.channel.auto_disabled_reason?.trim())issues.push({id:`channel-${row.channel.id}`,title:row.channel.name,detail:"渠道已自动停用，请检查后手动恢复",page:"channels"});
    else if(channelEnabled(row) && Date.parse(row.cooldown_until)>now)issues.push({id:`channel-${row.channel.id}`,title:row.channel.name,detail:"暂不可用，正在冷却",page:"channels"});
  }
  for(const alert of data.alerts.items)if(["failed","partial"].includes(alert.delivery))issues.push({id:`alert-${alert.id}`,title:alert.title,detail:"通知未完全送达，请检查投递记录",page:"monitor/alerts"});
  return issues;
}
