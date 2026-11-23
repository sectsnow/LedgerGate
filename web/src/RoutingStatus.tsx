import { useEffect, useState } from "react";
import { CircleCheck, CircleDot, CircleHelp, CirclePause, Hand, Hourglass, Link, ListOrdered, Radar, Scale, TimerReset, Workflow } from "lucide-react";
import { api, type ChannelRow, type Group, type GroupRuntime, type RouteRuntime } from "./api";
import "./routing-status.css";

const clock = (value: string) => new Date(value).toLocaleTimeString("zh-CN", { hour12: false });
const strategyIcons: Record<string, typeof Hand> = { manual: Hand, weighted: Scale, priority: ListOrdered };
const stateIcons = { available: CircleCheck, current: CircleDot, standby: CirclePause, disabled: CirclePause, cooling: Hourglass, probing: Radar, recovery: TimerReset };

export function RoutingStrategyIcon({ strategy, size = 16 }: { strategy: string; size?: number }) {
  const Icon = strategyIcons[strategy] || Workflow;
  return <Icon size={size} aria-hidden="true" />;
}

export function useGroupRouting() {
  const [states, setStates] = useState<Record<number, GroupRuntime>>({});
  const [error, setError] = useState("");
  useEffect(() => {
    let stopped = false;
    let pending = false;
    async function load() {
      if (pending) return;
      pending = true;
      try {
        const rows = await api<GroupRuntime[]>("/groups/routing");
        if (!stopped) { setStates(Object.fromEntries(rows.map(row => [row.group_id, row]))); setError(""); }
      } catch (error) { if (!stopped) setError((error as Error).message); }
      finally { pending = false; }
    }
    void load();
    const timer = setInterval(() => { if (!document.hidden) void load(); }, 2000);
    return () => { stopped = true; clearInterval(timer); };
  }, []);
  return { states, error };
}

export function GroupRoutingSummary({ group, runtime, channels }: { group: Group; runtime?: GroupRuntime; channels: ChannelRow[] }) {
  if (!runtime) return null;
  const name = (id: number) => {
    const route = group.routes.find(route => route.id === id);
    const channel = channels.find(row => row.channel.id === route?.channel_id)?.channel;
    return route ? `${channel?.name || `渠道 #${route.channel_id}`} / ${route.model}` : "尚未选择";
  };
  const cooling = runtime.routes.length > 0 && runtime.routes.every(route => ["cooling", "probing", "disabled", "standby"].includes(route.state));
  const temporary = runtime.routes.some(route => route.state === "cooling" || route.state === "probing");
  return <div className="route-runtime-summary" aria-label={`${group.name} 选路状态`}>
    <span><RoutingStrategyIcon strategy={group.strategy} size={14} />{group.strategy === "manual" ? `手动固定：${name(group.active_route_id)}` : group.strategy === "weighted" ? "按权重逐次分配" : runtime.current_route_id ? `当前：${name(runtime.current_route_id)}` : "尚无当前成员"}</span>
    {runtime.probe_route_id > 0 && <span><Radar size={14} aria-hidden="true" />恢复探测：{name(runtime.probe_route_id)}</span>}
    {runtime.affinity_until && <span><Link size={14} aria-hidden="true" />亲和保持至 {clock(runtime.affinity_until)}</span>}
    {cooling && <span><Hourglass size={14} aria-hidden="true" />{temporary ? "成员暂不可用；临时故障可在请求预算内等待恢复。" : "当前没有参与转发的成员。"}</span>}
  </div>;
}

export function RouteRoutingStatus({ runtime }: { runtime?: RouteRuntime }) {
  if (!runtime) return null;
  const names = { available: "可用", current: "当前成员", standby: "未选中", disabled: "暂停转发", cooling: runtime.scope === "channel" ? "渠道冷却" : "此模型冷却", probing: "恢复探测中", recovery: "等待恢复探测" };
  const Icon = stateIcons[runtime.state] || CircleHelp;
  return <div className="route-runtime" data-state={runtime.state}>
    <small className="icon-label"><Icon size={14} aria-hidden="true" />{names[runtime.state]}{runtime.state === "cooling" && runtime.cooldown_until && ` · 至 ${clock(runtime.cooldown_until)}`}</small>
    {runtime.error && <small className="table-sub">上次故障：{runtime.error}</small>}
  </div>;
}
