import type { Channel } from "./api";

export function channelModelNames(raw = ""): string[] {
  try {
    const value = JSON.parse(raw || "[]");
    if (Array.isArray(value)) return value.filter((item): item is string => typeof item === "string");
  } catch {
    /* 旧记录无法解析时可重新填写模型列表。 */
  }
  return [];
}

export function channelModelChanges(channel: Pick<Channel, "models" | "discovered_models" | "models_baseline" | "models_sync_error">) {
  if (!channel.discovered_models || channel.models_sync_error) return { added: [], removed: [], missing: [] };
  const latest = new Set(channelModelNames(channel.discovered_models));
  const baseline = new Set(channelModelNames(channel.models_baseline || channel.discovered_models));
  return {
    added: [...latest].filter(model => !baseline.has(model)),
    removed: [...baseline].filter(model => !latest.has(model)),
    missing: channelModelNames(channel.models).filter(model => !latest.has(model)),
  };
}

export function channelModelSyncSelection(available: string[], saved: string[]) {
  return { models: [...new Set([...available, ...saved])], selected: new Set(saved) };
}
