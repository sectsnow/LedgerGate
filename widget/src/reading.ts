export const READING_KEY = "ledgergate-widget-reading";
export const sectionIds = ["assets", "usage", "alerts", "ranking", "analysis", "cache", "quality", "latency", "cache-cost", "error-breakdown", "cache-comparison", "cache-trend"] as const;
export type SectionId = typeof sectionIds[number];
export type ReadingPrefs = { density: "comfortable" | "compact"; textSize: "standard" | "large"; collapsed: SectionId[] };
export function parseReadingPrefs(raw: string | null): ReadingPrefs {
  try {
    const value = JSON.parse(raw ?? "null");
    return { density: value?.density === "compact" ? "compact" : "comfortable", textSize: value?.textSize === "large" ? "large" : "standard", collapsed: sectionIds.filter(id => Array.isArray(value?.collapsed) && value.collapsed.includes(id)) };
  } catch { return { density: "comfortable", textSize: "standard", collapsed: [] }; }
}
