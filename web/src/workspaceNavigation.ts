export const analyticsTabs = [{id:"summary",name:"综合"},{id:"usage",name:"用量趋势"},{id:"cache",name:"缓存分析"},{id:"stability",name:"稳定性"},{id:"cost",name:"费用与资产"}] as const;
export const settingsTabs = [{id:"general",name:"基础设置"},{id:"runtime",name:"运行参数"},{id:"notifications",name:"通知推送"},{id:"widget",name:"桌面小组件"},{id:"data",name:"数据与备份"},{id:"about",name:"关于"}] as const;
export const monitorTabs = [{id:"alerts",name:"告警"},{id:"rates",name:"倍率变动"},{id:"announcements",name:"上游公告"}] as const;

export function workspacePage(raw: string) {
  const [path, query] = raw.replace(/^#/, "").split("?",2);
  const alias: Record<string,string> = {alerts:"monitor/alerts","rate-changes":"monitor/rates",announcements:"monitor/announcements",analytics:"analytics/summary",settings:"settings/general",monitor:"monitor/alerts"};
  const route = alias[path] ?? path;
  const valid = /^(overview|accounts(?:\/\d+\/channels)?|channels|models|model-tests|keys|requests)$/.test(route) || analyticsTabs.some(tab=>route===`analytics/${tab.id}` || route===`analytics/${tab.id}/detail`) || settingsTabs.some(tab=>route===`settings/${tab.id}`) || monitorTabs.some(tab=>route===`monitor/${tab.id}`);
  return valid ? route+(query ? `?${query}` : "") : "overview";
}

export const workspacePaths = {accounts:"/accounts",channels:"/channels",groups:"/groups",keys:"/keys",requests:"/requests",alerts:"/alerts?page=1&page_size=5",settings:"/settings",overview:"/overview",build:"/version"} as const;
export type WorkspaceResource = keyof typeof workspacePaths;
export function workspaceResources(page: string): WorkspaceResource[] {
  const route=workspacePage(page).split("?")[0];
  const root=route.split("/")[0];
  const resources: Record<string,WorkspaceResource[]> = {
    overview:["accounts","channels","overview","requests","alerts"],
    analytics:route.endsWith("/detail") ? [] : ["overview",...(route==="analytics/cost" ? ["accounts" as const] : [])],
    accounts:["accounts","channels","groups"],channels:["accounts","channels","groups"],models:["accounts","channels","groups"],
    "model-tests":["accounts","channels","groups"],keys:["keys","groups"],requests:["channels"],monitor:["accounts"],
    settings:route==="settings/notifications" ? ["accounts"] : [],
  };
  return ["settings","build",...(resources[root]??[])];
}
export function workspacePollInterval(page: string) {
  return ["overview","accounts","channels","models","keys","monitor"].includes(workspacePage(page).split("/")[0]) ? 15_000 : 0;
}
