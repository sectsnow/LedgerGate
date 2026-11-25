import { amount, finances, type Overview } from "./summary.ts";
import { resolveWindowGeometry, windowMonitor, type MonitorGeometry, type WindowState } from "./windowState.ts";

export const BADGE_SIZE = { width: 72, height: 56 };
export const DISPLAY_PREFS_KEY = "ledgergate-widget-display-prefs";
export const BADGE_STATE_KEY = "ledgergate-widget-badge-state";

export function windowOperationQueue() {
  let pending = Promise.resolve();
  return <T>(operation: () => Promise<T>): Promise<T> => {
    const next = pending.then(operation);
    pending = next.then(() => undefined, () => undefined);
    return next;
  };
}
export const badgeMetrics = ["tokens", "requests", "balance", "today"] as const;
export type BadgeMetric = typeof badgeMetrics[number];
export type DockSide = "left" | "right" | null;
export type DisplayPrefs = { autoHide: boolean; keepExpanded: boolean; avoidFullscreen: boolean; metric: BadgeMetric; currency: string; rotate: boolean; metrics: BadgeMetric[] };
export const defaultDisplayPrefs: DisplayPrefs = { autoHide: true, keepExpanded: false, avoidFullscreen: true, metric: "tokens", currency: "", rotate: false, metrics: ["tokens", "requests"] };
export type BadgeState = { mode: "panel" | "badge"; geometry: WindowState | null; side: DockSide; offset: number };

export function parseDisplayPrefs(raw: string | null): DisplayPrefs {
  try {
    const value = JSON.parse(raw ?? "null");
    if (!value || typeof value !== "object" || Array.isArray(value)) return { ...defaultDisplayPrefs };
    const metrics = Array.isArray(value.metrics) ? badgeMetrics.filter(item => value.metrics.includes(item)) : defaultDisplayPrefs.metrics;
    return {
      autoHide: typeof value.autoHide === "boolean" ? value.autoHide : true,
      keepExpanded: value.keepExpanded === true, avoidFullscreen: value.avoidFullscreen !== false,
      metric: badgeMetrics.includes(value.metric) ? value.metric : "tokens",
      currency: typeof value.currency === "string" ? value.currency.trim().toUpperCase().slice(0, 24) : "",
      rotate: value.rotate === true, metrics: metrics.length ? metrics : ["tokens"],
    };
  } catch { return { ...defaultDisplayPrefs }; }
}

export function parseBadgeState(raw: string | null): BadgeState {
  const fallback: BadgeState = { mode: "panel", geometry: null, side: null, offset: .3 };
  try {
    const value = JSON.parse(raw ?? "null");
    if (!value || !value.geometry || ![value.geometry.x, value.geometry.y, value.geometry.scaleFactor].every((item: unknown) => typeof item === "number" && Number.isFinite(item)) || value.geometry.scaleFactor <= 0 || Math.abs(value.geometry.x) > 1_000_000 || Math.abs(value.geometry.y) > 1_000_000) return fallback;
    return { mode: value.mode === "badge" ? "badge" : "panel", geometry: { ...BADGE_SIZE, x: Math.round(value.geometry.x), y: Math.round(value.geometry.y), scaleFactor: value.geometry.scaleFactor }, side: value.side === "left" || value.side === "right" ? value.side : null, offset: typeof value.offset === "number" && Number.isFinite(value.offset) ? Math.max(0, Math.min(1, value.offset)) : .3 };
  } catch { return fallback; }
}

function externalEdge(monitor: MonitorGeometry, monitors: MonitorGeometry[], side: Exclude<DockSide, null>) {
  const edge = side === "left" ? monitor.position.x : monitor.position.x + monitor.size.width;
  return !monitors.some(other => other !== monitor && Math.abs((side === "left" ? other.position.x + other.size.width : other.position.x) - edge) <= 2 && other.position.y < monitor.position.y + monitor.size.height && other.position.y + other.size.height > monitor.position.y);
}

export function dockSide(state: WindowState, monitors: MonitorGeometry[], previous: DockSide = null): DockSide {
  const monitor = windowMonitor(state, monitors);
  if (!monitor) return null;
  const area = monitor.workArea;
  for (const side of ["left", "right"] as const) {
    const distance = side === "left" ? Math.abs(state.x - area.position.x) : Math.abs(state.x + state.width * monitor.scaleFactor - area.position.x - area.size.width);
    if (externalEdge(monitor, monitors, side) && distance <= (side === previous ? 32 : 16) * monitor.scaleFactor) return side;
  }
  return null;
}

export function badgeGeometry(source: WindowState, monitors: MonitorGeometry[], side: DockSide, offset?: number): WindowState {
  const monitor = windowMonitor(source, monitors);
  const scale = monitor?.scaleFactor ?? source.scaleFactor;
  if (!monitor) return { ...source, ...BADGE_SIZE };
  const area = monitor.workArea;
  const maxX = area.position.x + Math.max(0, area.size.width - BADGE_SIZE.width * scale);
  const maxY = area.position.y + Math.max(0, area.size.height - BADGE_SIZE.height * scale);
  return { ...BADGE_SIZE, scaleFactor: scale,
    x: Math.round(side === "left" ? area.position.x : side === "right" ? maxX : Math.max(area.position.x, Math.min(maxX, source.x))),
    y: Math.round(offset === undefined ? Math.max(area.position.y, Math.min(maxY, source.y)) : area.position.y + Math.max(0, maxY - area.position.y) * offset),
  };
}

export function badgeOffset(geometry: WindowState, monitors: MonitorGeometry[]) {
  const monitor = windowMonitor(geometry, monitors);
  if (!monitor) return .3;
  const area = monitor.workArea;
  return Math.max(0, Math.min(1, (geometry.y - area.position.y) / Math.max(1, area.size.height - BADGE_SIZE.height * monitor.scaleFactor)));
}

export function expandGeometry(panel: WindowState, badge: WindowState, monitors: MonitorGeometry[], side: DockSide) {
  const monitor = windowMonitor(badge, monitors);
  const scale = monitor?.scaleFactor ?? badge.scaleFactor;
  const x = side === "right" ? badge.x + badge.width * scale - panel.width * scale : badge.x;
  return resolveWindowGeometry({ ...panel, x, y: badge.y, scaleFactor: scale }, monitors, scale, monitor);
}

const beijingDate = (now: number) => new Date(now + 8 * 3600_000).toISOString().slice(0, 10);
export const metricLabels: Record<BadgeMetric, string> = { tokens: "今日 Token", requests: "今日请求", balance: "账户余额", today: "今日实际消费" };

export function badgeReading(overview: Overview | null, metric: BadgeMetric, currency: string, lastSuccess: number | null, error: string, now: number) {
  const stale = !!error || !lastSuccess || now - lastSuccess > 90_000 || overview?.date !== beijingDate(now);
  let value: number | null = null;
  let label = metricLabels[metric];
  let detail = "";
  let warning = false;
  let unit = "";
  if (overview) {
    if (metric === "tokens") value = overview.input_tokens + overview.output_tokens;
    else if (metric === "requests") value = overview.requests;
    else {
      const groups = finances(overview);
      const group = currency ? groups.find(item => item.currency === currency) : groups.length === 1 ? groups[0] : undefined;
      if (group) {
        value = metric === "balance" ? group.balance : group.today;
        unit = group.currency;
        label = `${metric === "balance" ? "余额" : "今日"} ${unit || "未设单位"}`;
        warning = !group.currency || group.attention || (metric === "balance" ? group.balanceCount : group.todayCount) < group.accounts || (group.oldestCheck != null && beijingDate(group.oldestCheck) !== beijingDate(now));
        detail = `已采集 ${metric === "balance" ? group.balanceCount : group.todayCount}/${group.accounts} 个账户${group.oldestCheck != null ? `；最早采集 ${new Date(group.oldestCheck).toLocaleString("zh-CN")}` : ""}`;
      } else { warning = true; detail = groups.length > 1 ? "请在浮标设置中选择币种" : "暂无账户数据"; }
    }
  }
  const valid = value != null && Number.isFinite(value);
  const compact = valid ? value!.toLocaleString("en-US", { notation: "compact", maximumFractionDigits: 1 }) : "—";
  const exact = valid ? (metric === "balance" || metric === "today" ? amount(value, unit) : value!.toLocaleString("zh-CN")) : "未提供";
  const status = !overview ? "empty" : stale ? "stale" : warning || !valid ? "warning" : "ok";
  const statusText = { empty: "等待数据", stale: "旧数据", warning: "采集提醒", ok: "正常" }[status];
  return { label, value: unit === "USD" && valid ? `$${compact}` : compact, status, statusText,
    title: `${metricLabels[metric]}：${exact}\n${statusText}${error ? ` · ${error}` : ""}${lastSuccess ? `\n获取于 ${new Date(lastSuccess).toLocaleString("zh-CN")}` : ""}${detail ? `\n${detail}` : ""}\n单击展开；右键设置`,
  };
}
