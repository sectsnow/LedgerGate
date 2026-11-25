export const WINDOW_STATE_STORAGE_KEY = "ledgergate-widget-window-state";

export const WINDOW_SIZE_LIMITS = {
  minWidth: 360,
  minHeight: 360,
  maxWidth: 720,
  maxHeight: 900,
} as const;

export type WindowState = {
  x: number;
  y: number;
  width: number;
  height: number;
  scaleFactor: number;
};

export type MonitorGeometry = {
  position: { x: number; y: number };
  size: { width: number; height: number };
  workArea: { position: { x: number; y: number }; size: { width: number; height: number } };
  scaleFactor: number;
};

const finite = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value);

export function clampWindowSize(width: number, height: number) {
  return {
    width: Math.round(Math.min(Math.max(width, WINDOW_SIZE_LIMITS.minWidth), WINDOW_SIZE_LIMITS.maxWidth)),
    height: Math.round(Math.min(Math.max(height, WINDOW_SIZE_LIMITS.minHeight), WINDOW_SIZE_LIMITS.maxHeight)),
  };
}

export function parseWindowState(raw: string | null): WindowState | null {
  if (!raw) return null;
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object") return null;
    const state = value as Record<string, unknown>;
    if (!finite(state.x) || !finite(state.y) || !finite(state.width) || !finite(state.height) || !finite(state.scaleFactor) || state.scaleFactor <= 0) return null;
    const size = clampWindowSize(state.width, state.height);
    return { x: Math.round(state.x), y: Math.round(state.y), ...size, scaleFactor: state.scaleFactor };
  } catch {
    return null;
  }
}

export function serializeWindowState(state: WindowState): string {
  return JSON.stringify({
    x: Math.round(state.x),
    y: Math.round(state.y),
    ...clampWindowSize(state.width, state.height),
    scaleFactor: state.scaleFactor,
  });
}

export function windowMonitor(saved: WindowState, monitors: MonitorGeometry[], fallbackMonitor?: MonitorGeometry) {
  const savedPhysicalWidth = saved.width * saved.scaleFactor;
  const savedPhysicalHeight = saved.height * saved.scaleFactor;
  const center = { x: saved.x + savedPhysicalWidth / 2, y: saved.y + savedPhysicalHeight / 2 };
  return monitors.find(value => center.x >= value.position.x && center.x < value.position.x + value.size.width && center.y >= value.position.y && center.y < value.position.y + value.size.height) || fallbackMonitor || monitors[0];
}

export function resolveWindowGeometry(saved: WindowState, monitors: MonitorGeometry[], fallbackScaleFactor: number, fallbackMonitor?: MonitorGeometry, contentHeight?: number) {
  const monitor = windowMonitor(saved, monitors, fallbackMonitor);
  const scaleFactor = monitor?.scaleFactor || fallbackScaleFactor;
  const height = contentHeight === undefined ? saved.height : Math.min(Math.ceil(contentHeight), monitor ? Math.floor(monitor.workArea.size.height / scaleFactor) - 24 : WINDOW_SIZE_LIMITS.maxHeight);
  const size = clampWindowSize(saved.width, height);
  if (!monitor) return { x: saved.x, y: saved.y, ...size, scaleFactor };
  const physicalWidth = size.width * scaleFactor;
  const physicalHeight = size.height * scaleFactor;
  const margin = 12;
  const area = monitor.workArea;
  const minX = area.position.x + margin;
  const minY = area.position.y + margin;
  const maxX = area.position.x + Math.max(margin, area.size.width - physicalWidth - margin);
  const maxY = area.position.y + Math.max(margin, area.size.height - physicalHeight - margin);
  return { x: Math.round(Math.min(Math.max(saved.x, minX), maxX)), y: Math.round(Math.min(Math.max(saved.y, minY), maxY)), ...size, scaleFactor };
}
