import { useCallback, useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { getCurrentWindow, availableMonitors, primaryMonitor } from "@tauri-apps/api/window";
import { parseWindowState, resolveWindowGeometry, serializeWindowState, windowMonitor, WINDOW_STATE_STORAGE_KEY, type WindowState } from "./windowState.ts";
import { BADGE_STATE_KEY, DISPLAY_PREFS_KEY, badgeGeometry, badgeOffset, dockSide, expandGeometry, parseBadgeState, parseDisplayPrefs, windowOperationQueue, type DisplayPrefs, type DockSide } from "./widgetDisplay.ts";

const read = (key: string) => { try { return localStorage.getItem(key); } catch { return null; } };
const write = (key: string, value: string) => { try { localStorage.setItem(key, value); } catch { /* 禁用本地存储时仍允许当前会话使用浮标。 */ } };

async function placement() {
  const window = getCurrentWindow();
  const [size, position, scaleFactor, monitors, primary] = await Promise.all([window.innerSize(), window.outerPosition(), window.scaleFactor(), availableMonitors(), primaryMonitor()]);
  const logical = size.toLogical(scaleFactor);
  return { geometry: { x: position.x, y: position.y, width: logical.width, height: logical.height, scaleFactor }, monitors, primary: primary ?? undefined };
}

export default function useWidgetDisplay(configured: boolean, editing: boolean) {
  const [prefs, setPrefs] = useState(() => parseDisplayPrefs(read(DISPLAY_PREFS_KEY)));
  const prefsRef = useRef(prefs); prefsRef.current = prefs;
  const [mode, setMode] = useState<"panel" | "badge">("panel");
  const modeRef = useRef(mode);
  const [side, setSide] = useState<DockSide>(null);
  const sideRef = useRef<DockSide>(null);
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState(true);
  const hidden = useRef(false);
  const [error, setError] = useState("");
  const [inside, setInside] = useState(false);
  const [focused, setFocused] = useState(true);
  const [interacting, setInteracting] = useState(false);
  const panel = useRef<WindowState | null>(parseWindowState(read(WINDOW_STATE_STORAGE_KEY)));
  const badge = useRef(parseBadgeState(read(BADGE_STATE_KEY)));
  const enqueue = useRef(windowOperationQueue()).current;
  const applying = useRef(false);
  const ignoreMovesUntil = useRef(0);
  const requested = useRef(0);
  const fitRevision = useRef(0);
  const alive = useRef(true);

  const updatePrefs = useCallback((changes: Partial<DisplayPrefs>) => {
    setPrefs(current => parseDisplayPrefs(JSON.stringify({ ...current, ...changes })));
  }, []);
  const saveBadge = useCallback(() => write(BADGE_STATE_KEY, JSON.stringify({ ...badge.current, mode: modeRef.current, side: sideRef.current })), []);
  const assignSide = useCallback((value: DockSide) => { sideRef.current = value; setSide(value); }, []);
  const apply = useCallback(async (badgeMode: boolean, geometry: WindowState, activate = false) => {
    applying.current = true;
    try {
      await invoke("set_widget_display", { badge: badgeMode, geometry, activate, avoidFullscreen: prefsRef.current.avoidFullscreen });
    } finally { applying.current = false; ignoreMovesUntil.current = Date.now() + 300; }
  }, []);

  const switchMode = useCallback((target: "panel" | "badge", reset = false) => {
    const ticket = ++requested.current;
    return enqueue(async () => {
      if (!alive.current || ticket !== requested.current) return;
      setBusy(true); setInteracting(false);
      try {
        const current = await placement();
        if (ticket !== requested.current) return;
        let geometry: WindowState;
        if (target === "badge") {
          if (modeRef.current === "panel") {
            panel.current = current.geometry;
            write(WINDOW_STATE_STORAGE_KEY, serializeWindowState(current.geometry));
          }
          const source = badge.current.geometry ?? current.geometry;
          geometry = badgeGeometry(source, current.monitors, sideRef.current, sideRef.current && badge.current.side === sideRef.current ? badge.current.offset : undefined);
        } else {
          const saved = panel.current ?? { ...current.geometry, width: 400, height: 720 };
          geometry = modeRef.current === "badge" ? expandGeometry(saved, current.geometry, current.monitors, sideRef.current) : resolveWindowGeometry(current.geometry, current.monitors, current.geometry.scaleFactor, current.primary);
          if (reset) {
            const monitor = current.primary ?? current.monitors[0];
            if (monitor) geometry = resolveWindowGeometry({ ...geometry, x: monitor.workArea.position.x + 12, y: monitor.workArea.position.y + 12 }, current.monitors, monitor.scaleFactor, monitor);
            assignSide(null);
          }
        }
        await apply(target === "badge", geometry, target === "panel");
        if (ticket !== requested.current) return;
        modeRef.current = target; setMode(target); hidden.current = false; setShown(true);
        if (target === "badge") badge.current = { mode: target, geometry, side: sideRef.current, offset: badgeOffset(geometry, current.monitors) };
        else { panel.current = geometry; write(WINDOW_STATE_STORAGE_KEY, serializeWindowState(geometry)); }
        saveBadge(); setError("");
      } catch (reason) {
        modeRef.current = "panel"; setMode("panel"); assignSide(null);
        hidden.current = false; setShown(true); setError(String(reason)); saveBadge();
      } finally { if (alive.current) setBusy(false); }
    });
  }, [enqueue, apply, assignSide, saveBadge]);

  const fitHeight = useCallback((height: number) => {
    const revision = ++fitRevision.current;
    return enqueue(async () => {
      if (!alive.current || modeRef.current !== "panel" || hidden.current || applying.current || revision !== fitRevision.current) return;
      try {
        const current = await placement();
        const geometry = resolveWindowGeometry(current.geometry, current.monitors, current.geometry.scaleFactor, current.primary, height);
        if (revision !== fitRevision.current || modeRef.current !== "panel") return;
        if (Math.abs(current.geometry.height - geometry.height) > 1 || current.geometry.x !== geometry.x || current.geometry.y !== geometry.y) await apply(false, geometry);
        panel.current = geometry; write(WINDOW_STATE_STORAGE_KEY, serializeWindowState(geometry));
      } catch (reason) { setError(`自动调整窗口失败，可手动调整：${String(reason)}`); }
    });
  }, [enqueue, apply]);

  const hide = useCallback(async () => {
    ++requested.current;
    await enqueue(async () => {
      try { await invoke("hide_widget"); hidden.current = true; setShown(false); }
      catch (reason) { setError(String(reason)); }
    });
  }, [enqueue]);

  useEffect(() => {
    write(DISPLAY_PREFS_KEY, JSON.stringify(prefs));
  }, [prefs]);
  useEffect(() => {
    void invoke("set_widget_fullscreen_avoidance", { enabled: prefs.avoidFullscreen }).catch(reason => setError(String(reason)));
  }, [prefs.avoidFullscreen]);

  useEffect(() => {
    alive.current = true;
    let disposed = false;
    let timer: number | undefined;
    let pendingMoved = false;
    const stops: (() => void)[] = [];
    const watch = async (promise: Promise<() => void>) => { const stop = await promise; if (disposed) stop(); else stops.push(stop); };
    const queueGeometry = (moved: boolean) => {
      if (applying.current || Date.now() < ignoreMovesUntil.current) return;
      pendingMoved ||= moved;
      if (timer !== undefined) window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        void enqueue(async () => {
          if (disposed || applying.current || hidden.current) return;
          try {
            if (await invoke<boolean>("widget_pointer_down")) { queueGeometry(false); return; }
            const current = await placement();
            const nextSide = pendingMoved ? dockSide(current.geometry, current.monitors, sideRef.current) : sideRef.current;
            pendingMoved = false;
            if (modeRef.current === "badge") {
              const geometry = badgeGeometry(current.geometry, current.monitors, nextSide);
              if (geometry.x !== current.geometry.x || geometry.y !== current.geometry.y) await apply(true, geometry);
              badge.current = { mode: "badge", geometry, side: nextSide, offset: badgeOffset(geometry, current.monitors) };
            } else {
              const monitor = windowMonitor(current.geometry, current.monitors);
              const geometry = resolveWindowGeometry(current.geometry, current.monitors, current.geometry.scaleFactor, current.primary);
              if (monitor && nextSide) geometry.x = Math.round(nextSide === "left" ? monitor.workArea.position.x : monitor.workArea.position.x + monitor.workArea.size.width - geometry.width * monitor.scaleFactor);
              if (geometry.x !== current.geometry.x || geometry.y !== current.geometry.y || geometry.width !== current.geometry.width || geometry.height !== current.geometry.height) await apply(false, geometry);
              panel.current = geometry; write(WINDOW_STATE_STORAGE_KEY, serializeWindowState(geometry));
              if (nextSide) badge.current = { mode: "panel", geometry: badgeGeometry(geometry, current.monitors, nextSide), side: nextSide, offset: badgeOffset(geometry, current.monitors) };
            }
            assignSide(nextSide); saveBadge();
          } catch (reason) { if (!disposed) setError(String(reason)); }
        });
      }, 250);
    };
    const setup = async () => {
      const window = getCurrentWindow();
      try {
        await watch(window.onMoved(() => queueGeometry(true)));
        await watch(window.onResized(() => queueGeometry(false)));
        await watch(window.listen("widget-environment-changed", () => { ignoreMovesUntil.current = 0; queueGeometry(true); }));
        await watch(window.onFocusChanged(({ payload }) => setFocused(payload)));
        await watch(window.listen<boolean>("widget-show-panel", () => {
          ++requested.current; ++fitRevision.current;
          modeRef.current = "panel"; setMode("panel"); assignSide(null); hidden.current = false; setShown(true);
          void enqueue(async () => {
            const current = await placement();
            panel.current = current.geometry; write(WINDOW_STATE_STORAGE_KEY, serializeWindowState(current.geometry)); saveBadge();
          }).catch(reason => setError(String(reason)));
        }));
        await watch(window.listen("widget-tray-hidden", () => { hidden.current = true; setShown(false); }));
        const current = await placement();
        if (disposed) return;
        const geometry = resolveWindowGeometry(panel.current ?? current.geometry, current.monitors, current.geometry.scaleFactor, current.primary);
        await apply(false, geometry);
        panel.current = geometry;
        if (!disposed) { setFocused(await window.isFocused()); setReady(true); }
      } catch (reason) { if (!disposed) { setError(String(reason)); setReady(true); } }
    };
    void setup();
    return () => { disposed = true; alive.current = false; if (timer !== undefined) window.clearTimeout(timer); stops.forEach(stop => stop()); };
  }, [enqueue, apply, assignSide, saveBadge, switchMode]);

  const restoredBadge = useRef(false);
  useEffect(() => {
    if (!ready || !configured || restoredBadge.current) return;
    restoredBadge.current = true;
    if (badge.current.mode === "badge") { assignSide(badge.current.side); void switchMode("badge"); }
  }, [ready, configured, assignSide, switchMode]);

  useEffect(() => {
    if (!ready || !configured || mode !== "panel" || !side || !prefs.autoHide || prefs.keepExpanded || inside || focused || interacting || editing || busy || !shown) return;
    const timer = window.setTimeout(() => void switchMode("badge"), 800);
    return () => window.clearTimeout(timer);
  }, [ready, configured, mode, side, prefs.autoHide, prefs.keepExpanded, inside, focused, interacting, editing, busy, shown, switchMode]);

  return { mode, side, prefs, updatePrefs, ready, busy, shown, error, setInside, setInteracting, fitHeight, hide,
    collapse: () => switchMode("badge"), expand: () => switchMode("panel"), reset: () => switchMode("panel", true) };
}
