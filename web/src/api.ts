export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-LedgerGate-Request": "1",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (response.status === 401 && path !== "/login")
    window.dispatchEvent(new Event("session-expired"));
  if (!response.ok) {
    const result = await response.json().catch(() => ({}));
    throw new Error(result.error?.message || `请求失败（${response.status}）`);
  }
  return response.status === 204 ? (undefined as T) : response.json();
}

export type Account = {
  id: number;
  name: string;
  kind: "newapi" | "sub2api";
  base_url: string;
  enabled: boolean;
  use_proxy: boolean;
  auth_mode: string;
  username: string;
  user_id: string;
  currency: string;
  quota_per_unit: number;
  low_balance: number;
  block_empty: boolean;
  interval_seconds: number;
  balance: number | null;
  used: number | null;
  today_used: number | null;
  today_date: string;
  status: string;
  last_error: string;
  warnings: string;
  checked_at: string | null;
  rates: string;
  subscriptions: string;
  announcements: string;
};
export type AccountRow = {
  account: Account;
  has_token: boolean;
  has_refresh_token: boolean;
  has_password: boolean;
  has_login_extra: boolean;
};
export type Channel = {
  id: number;
  name: string;
  account_id: number | null;
  upstream_key_id: number | null;
  base_url: string;
  protocol: string;
  protocols: string[];
  enabled: boolean;
  use_proxy: boolean;
  auto_disabled_reason: string;
  models: string;
  discovered_models: string;
  models_baseline: string;
  models_checked_at: string | null;
  models_sync_error: string;
  timeout_seconds: number;
  cooldown_seconds: number;
};
export type ChannelRow = {
  channel: Channel;
  models_revision: string;
  monitoring_paused: boolean;
  cooldown_until: string;
  last_error: string;
  has_key: boolean;
  rate: UpstreamRate | null;
  rate_stale: boolean;
};
export const channelEnabled = (row?: ChannelRow) => !!row?.channel.enabled && !row.monitoring_paused;
export type ChannelBatchInput = {
  ids: number[];
  action: "enable" | "disable" | "protocols" | "sync_models" | "delete";
  protocols?: string[];
  detach_routes?: boolean;
};
export type ChannelBatchResult = {
  results: { id: number; name: string; success: boolean; error?: string }[];
  succeeded: number;
  failed: number;
};
export type UpstreamRate = {
  group_id: string;
  group: string;
  multiplier: number | null;
  base_multiplier: number | null;
};
export const protocolNames: Record<string, string> = {
  openai: "OpenAI Chat",
  responses: "Responses",
  anthropic: "Anthropic",
};
export type UpstreamKey = {
  id: number;
  name: string;
  group: string;
  status: string;
  expires_at: string | null;
  importable: boolean;
  imported_channel_id: number;
};
export type UpstreamKeyPage = {
  items: UpstreamKey[];
  total: number;
  page: number;
  page_size: number;
};
export type ChannelImportResult = { created: Channel[]; existing: Channel[] };
export type ModelGenerationResult = {
  created_models: number;
  added_routes: number;
  existing_routes: number;
  synced_channels: number;
  failures: { channel_id: number; name: string; message: string }[];
};
export type Route = {
  id?: number;
  channel_id: number;
  model: string;
  weight: number;
  priority: number;
  pricing?: ModelPricing | null;
};
export type PriceRates = {
  input?: number | null;
  output?: number | null;
  cache_read?: number | null;
  cache_write?: number | null;
  cache_write_1h?: number | null;
  image_input?: number | null;
  image_output?: number | null;
};
export type ModelPricing = PriceRates & {
  mode: "token" | "image" | "video" | "request";
  unit_price?: number | null;
  tiers?: (PriceRates & { min: number; max?: number | null; label: string; unit_price?: number | null })[];
  source: string;
  source_url: string;
  model: string;
  reference: boolean;
  note?: string;
  updated_at?: string | null;
  stale?: boolean;
};
export type GroupRelayConfig = {
  member_max_attempts: number;
  member_retry_interval_seconds: number;
  member_cooldown_seconds: number;
  member_affinity_seconds: number;
};
export type Group = {
  id: number;
  name: string;
  vendor: string;
  strategy: string;
  active_route_id: number;
  relay_config: GroupRelayConfig | null;
  input_price_micros: number;
  output_price_micros: number;
  price_mode: "auto" | "manual";
  price_model: string;
  price_source: string;
  price_source_model: string;
  price_sync_error: string;
  price_updated_at: string | null;
  pricing: ModelPricing | null;
  routes: Route[];
};
export type PriceSyncResult = {
  synced: number;
  manual: number;
  source: string;
  synced_at: string;
  unmatched: { id: number; name: string; reason: string }[];
  warnings: string[];
};
export type Key = {
  id: number;
  name: string;
  prefix: string;
  can_copy: boolean;
  enabled: boolean;
  expires_at: string | null;
  allowed_models: string[] | null;
  budget_micros: number | null;
  used_micros: number;
  created_at: string;
};
export type RequestLog = {
  id: number;
  metrics_version: number;
  request_id: string;
  key_id: number;
  key_name: string;
  channel_id: number;
  channel_name: string;
  account_name: string;
  model: string;
  upstream_model: string;
  protocol: string;
  upstream_protocol: string;
  reasoning_effort: string;
  reasoning_tokens: number | null;
  usage_recorded: boolean;
  cache_read_recorded: boolean;
  cache_write_recorded: boolean;
  usage_warning: string;
  stream: boolean;
  status: number;
  attempts: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cache_write_1h_tokens: number;
  cost_micros: number;
  cost_status: string;
  cost_warning: string;
  cost_source: string;
  duration_ms: number;
  started_at: string | null;
  finished_at: string | null;
  first_token_ms: number | null;
  generation_ms: number | null;
  cost_detail: RequestCostDetail | null;
  error: string;
  created_at: string;
  state: string;
  first_byte_ms: number | null;
};
export type RequestPage = { items: RequestLog[]; total: number; page: number; page_size: number; active: number };
export type RequestDetail = {
  request: RequestLog;
  detail: {
    available: boolean;
    request_body: string;
    response_body: string;
    request_truncated: boolean;
    response_truncated: boolean;
    attempts: { channel_id: number; channel_name: string; model: string; protocol: string; started_at: string; duration_ms: number; status: number; error: string }[];
  };
};
export type Alert = {
  id: number;
  account_id: number;
  kind: string;
  title: string;
  detail: string;
  delivery: string;
  delivery_error: string;
  attempts: number;
  deliveries?: NotificationDelivery[];
  created_at: string;
  rate_changes?: {
    group_id: string;
    group: string;
    before: number | null;
    after: number | null;
    kind: "changed" | "added" | "removed";
    percent: number | null;
  }[];
};
export type ActivityPage<T> = { items: T[]; total: number; page: number; page_size: number };
export type AlertPage = ActivityPage<Alert> & { pending: number };
export type Announcement = {
  id: string;
  account_id: number;
  account_name: string;
  kind: "newapi" | "sub2api";
  title: string;
  content: string;
  mode: string;
  published_at: string | null;
  updated_at: string | null;
  collected_at: string | null;
  stale: boolean;
};
export type AnnouncementPage = ActivityPage<Announcement> & { warnings: string[] };
export type RequestCostDetail = {
  mode: string;
  rates: PriceRates;
  unit_price?: number | null;
  source: string;
  source_url: string;
  price_model: string;
  reference: boolean;
  note: string;
  tier: string;
  base_cost_micros: number;
  multiplier: number | null;
  multiplier_applied: boolean;
  rates_include_multiplier: boolean;
  multiplier_stale: boolean;
  rate_group: string;
};
export type RuntimeOptions = {
  first_output_seconds: number;
  stream_idle_seconds: number;
  request_timeout_seconds: number;
  max_attempts: number;
  monitor_concurrency: number;
  notification_type: "webhook" | "feishu" | "telegram";
  subscription_low_percent: number;
  subscription_expiry_hours: number;
  rate_change_percent: number;
  notification_retries: number;
  notification_events: string[] | null;
  notification_accounts: number[] | null;
  price_sync_hours: number;
  model_sync_hours: number;
};
export type Settings = {
  runtime: RuntimeOptions;
  sync_tasks: { name: string; started_at: string | null; finished_at: string | null; last_success_at: string | null; error: string; summary: string }[];
  has_telegram_token: boolean;
  telegram_chat_id: string;
  webhook_url: string;
  has_webhook_secret: boolean;
  alert_cooldown_minutes: number;
  retention_days: number;
  request_summary_retention_days: number;
};
export type NotificationChannel = {
  id: number;
  name: string;
  type: "webhook" | "feishu" | "telegram" | "email" | "wecom" | "dingtalk" | "serverchan";
  enabled: boolean;
  use_proxy: boolean;
  chat_id: string;
  smtp_host: string;
  smtp_port: number;
  smtp_tls: string;
  smtp_username: string;
  smtp_from: string;
  smtp_to: string[] | null;
  events: string[] | null;
  accounts: number[] | null;
  retries: number;
  has_url: boolean;
  has_secret: boolean;
  has_token: boolean;
  has_send_key: boolean;
  has_smtp_password: boolean;
  last_delivery: NotificationDelivery | null;
};
export type NotificationDelivery = {
  id: number;
  alert_id: number | null;
  channel_id: number;
  channel_name: string;
  channel_type: string;
  title: string;
  status: string;
  error: string;
  attempts: number;
  next_retry_at: string;
  sent_at: string | null;
  created_at: string;
  updated_at: string;
};
export type BalanceDay = {
  date: string;
  balance: number | null;
  consumption: number | null;
  balance_accounts: number;
  consumption_accounts: number;
};
export type AccountSummary = {
  currency: string;
  accounts: number;
  balance: number | null;
  today_used: number | null;
  used: number | null;
  balance_accounts: number;
  today_accounts: number;
  used_accounts: number;
  minimum: { account_id: number; name: string; balance: number } | null;
  history: BalanceDay[];
};
export type Overview = {
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cost_micros: number;
  cost_unknown_requests: number;
  account_summaries: AccountSummary[];
  activity: RequestDay[];
  series?: { hour: number; requests: number }[];
};
export type RequestDay = {
  date: string;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cache_write_1h_tokens: number;
  cost_micros: number;
  cost_unknown_requests: number;
};
export type Data = {
  accounts: AccountRow[];
  channels: ChannelRow[];
  groups: Group[];
  keys: Key[];
  requests: RequestLog[];
  alerts: AlertPage;
  rateChanges: AlertPage;
  announcements: AnnouncementPage;
  settings: Settings;
  overview: Overview;
};
