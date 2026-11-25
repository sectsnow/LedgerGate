import { useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { Menu } from "@tauri-apps/api/menu";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { badgeMetrics, badgeReading, metricLabels, type BadgeMetric, type DisplayPrefs } from "./widgetDisplay.ts";
import type { Overview } from "./summary";

type Props = {
  overview: Overview | null; lastSuccess: number | null; error: string; prefs: DisplayPrefs; theme: string; busy: boolean;
  onPrefs: (changes: Partial<DisplayPrefs>) => void;
  onExpand: () => Promise<void>; onHide: () => Promise<void>; onReset: () => Promise<void>; onSettings: () => void;
};

export default function WidgetBadge(props: Props) {
  const [now, setNow] = useState(Date.now);
  const [hovered, setHovered] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [metric, setMetric] = useState<BadgeMetric>(props.prefs.metric);
  const [localError, setLocalError] = useState("");
  const menu = useRef<Menu | null>(null);
  const pointer = useRef<{ x: number; y: number; dragged: boolean } | null>(null);
  const suppressClick = useRef(false);
  useEffect(() => setMetric(props.prefs.metric), [props.prefs.metric]);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => { window.clearInterval(timer); void menu.current?.close().catch(() => {}); };
  }, []);
  useEffect(() => {
    if (!props.prefs.rotate || hovered || dragging || menuOpen || props.busy || props.prefs.metrics.length < 2) return;
    const timer = window.setInterval(() => setMetric(current => props.prefs.metrics[(props.prefs.metrics.indexOf(current) + 1) % props.prefs.metrics.length]), 8_000);
    return () => window.clearInterval(timer);
  }, [props.prefs.rotate, props.prefs.metrics, hovered, dragging, menuOpen, props.busy]);
  useEffect(() => {
    if (!menuOpen) return;
    const timer = window.setInterval(() => {
      void invoke<boolean>("widget_menu_active").then(active => { if (!active) setMenuOpen(false); }).catch(() => setMenuOpen(false));
    }, 500);
    return () => window.clearInterval(timer);
  }, [menuOpen]);
  const reading = badgeReading(props.overview, metric, props.prefs.currency, props.lastSuccess, props.error || localError, now);
  const run = (action: () => Promise<void>) => { setMenuOpen(false); void action().catch(reason => setLocalError(String(reason))); };
  async function contextMenu() {
    if (props.busy) return;
    try {
      await menu.current?.close();
      menu.current = await Menu.new({ items: [
        { id: "expand", text: "展开完整面板", action: () => run(props.onExpand) },
        { text: "显示指标", items: badgeMetrics.map(value => ({ id: `metric-${value}`, text: metricLabels[value], checked: props.prefs.metric === value, action: () => { setMenuOpen(false); props.onPrefs({ metric: value }); } })) },
        { id: "rotate", text: "自动切换指标", checked: props.prefs.rotate, action: () => { setMenuOpen(false); props.onPrefs({ rotate: !props.prefs.rotate }); } },
        { id: "settings", text: "浮标与贴边设置", action: () => { setMenuOpen(false); props.onSettings(); } },
        { id: "reset", text: "重置窗口位置", action: () => run(props.onReset) },
        { id: "hide", text: "隐藏到托盘", action: () => run(props.onHide) },
      ] });
      setMenuOpen(true);
      await menu.current.popup();
    } catch (reason) { setMenuOpen(false); setLocalError(String(reason)); }
  }
  // shortcut: 原生 title 不支持移入阅读；需要交互式说明时再替换提示层。
  return <main className="widget widget-badge" data-theme={props.theme} data-status={reading.status}>
    <button type="button" className="badge-button" title={reading.title} aria-label={`${reading.label} ${reading.value}，${reading.statusText}，单击展开完整面板`} disabled={props.busy}
      onPointerEnter={() => setHovered(true)} onPointerLeave={() => setHovered(false)}
      onPointerDown={event => {
        if (event.button !== 0) return;
        suppressClick.current = false; pointer.current = { x: event.screenX, y: event.screenY, dragged: false };
        event.currentTarget.setPointerCapture(event.pointerId);
      }}
      onPointerMove={event => {
        const start = pointer.current;
        if (!start || start.dragged || Math.hypot(event.screenX - start.x, event.screenY - start.y) < 6) return;
        start.dragged = true; suppressClick.current = true; setDragging(true);
        event.currentTarget.releasePointerCapture(event.pointerId);
        void getCurrentWindow().startDragging().catch(reason => setLocalError(String(reason))).finally(() => { pointer.current = null; setDragging(false); });
      }}
      onPointerUp={() => { pointer.current = null; setDragging(false); }} onPointerCancel={() => { pointer.current = null; setDragging(false); }}
      onClick={() => { if (suppressClick.current) { suppressClick.current = false; return; } run(props.onExpand); }}
      onContextMenu={event => { event.preventDefault(); void contextMenu(); }}>
      <span className="badge-label">{reading.label}</span><strong>{reading.value}</strong>
      <span className="badge-status" aria-hidden="true">{reading.status === "ok" ? "●" : reading.status === "empty" ? "…" : "!"}</span>
    </button>
  </main>;
}
