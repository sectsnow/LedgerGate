import type { RuntimeOptions, Settings } from "./api";
export type SettingsSection="runtime"|"notifications"|"data";
export function settingsSection(current:Settings,draft:Settings,section:SettingsSection):Settings {
  const keys:Record<SettingsSection,(keyof RuntimeOptions)[]>={
    runtime:["first_output_seconds","stream_idle_seconds","request_timeout_seconds","max_attempts","monitor_concurrency","price_sync_hours","model_sync_hours"],
    notifications:["rate_change_percent","subscription_low_percent","subscription_expiry_hours","notification_retries"],
    data:["request_digest_days","request_digest_limit"],
  };
  const runtime={...current.runtime,...Object.fromEntries(keys[section].map(key=>[key,draft.runtime[key]]))};
  return {...current,runtime,...section==="notifications" ? {alert_cooldown_minutes:draft.alert_cooldown_minutes} : section==="data" ? {retention_days:draft.retention_days,request_summary_retention_days:draft.request_summary_retention_days} : {}};
}
