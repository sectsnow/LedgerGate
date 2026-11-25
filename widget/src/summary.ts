export type Account = {
  id?: number | null;
  name?: string | null;
  currency: string;
  balance: number | null;
  today_used: number | null;
  today_date: string;
  status: string;
  enabled: boolean;
  checked_at: string | null;
  today_collected_at?: string | null;
  interval_seconds?: number | null;
  collection_notes?: string[];
  has_warning: boolean;
};

export type Overview = {
  date: string;
  accounts: Account[];
  requests: number;
  input_tokens: number;
  output_tokens: number;
  period?: Period | null;
};

export type Rank = { name: string; requests: number; tokens: number };
export type Trend = {
  date: string;
  available: boolean;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cost_micros: number;
  cost_unknown_requests: number;
};

export type Period = {
  days: number;
  retention_days: number;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_micros: number;
  cost_unknown_requests: number;
  trend?: Trend[];
  models: Rank[];
  channels: Rank[];
};

export type Finance = {
  currency: string;
  balance: number | null;
  today: number | null;
  accounts: number;
  balanceCount: number;
  todayCount: number;
  attention: boolean;
  oldestCheck: number | null;
  oldestTodayCheck: number | null;
  todayCheckedCount: number;
  checkedCount: number;
};

export function finances(overview: Overview): Finance[] {
  const groups = new Map<string, Finance>();
  for (const account of overview.accounts) {
    const currency = account.currency.trim().toUpperCase();
    let group = groups.get(currency);
    if (!group) {
      group = { currency, balance: null, today: null, accounts: 0, balanceCount: 0, todayCount: 0, attention: false, oldestCheck: null, oldestTodayCheck: null, todayCheckedCount: 0, checkedCount: 0 };
      groups.set(currency, group);
    }
    group.accounts++;
    if (account.balance != null) {
      group.balance = (group.balance ?? 0) + account.balance;
      group.balanceCount++;
    }
    if (account.today_used != null && account.today_date === overview.date) {
      group.today = (group.today ?? 0) + account.today_used;
      group.todayCount++;
      const checked = account.today_collected_at ? Date.parse(account.today_collected_at) : NaN;
      if (Number.isFinite(checked)) {
        group.oldestTodayCheck = Math.min(group.oldestTodayCheck ?? checked, checked);
        group.todayCheckedCount++;
      }
    }
    const checked = account.checked_at ? Date.parse(account.checked_at) : NaN;
    if (Number.isFinite(checked)) { group.oldestCheck = Math.min(group.oldestCheck ?? checked, checked); group.checkedCount++; }
    if (!account.enabled || account.status !== "ok" || account.has_warning || !Number.isFinite(checked)) group.attention = true;
  }
  return [...groups.values()].sort((left, right) => left.currency === right.currency ? 0 : left.currency === "USD" ? -1 : right.currency === "USD" ? 1 : !left.currency ? 1 : !right.currency ? -1 : left.currency.localeCompare(right.currency));
}

export function amount(value: number | null, currency: string): string {
  if (value == null) return "—";
  const formatted = value.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 4 });
  return currency === "USD" ? `$${formatted}` : `${formatted} ${currency || "未设置单位"}`;
}

export function accountNotes(account: Account, date: string, now: number): string[] {
  const notes: string[] = [];
  if (!account.enabled) notes.push("监控已暂停，保留上次数据");
  else if (account.status === "error") notes.push("采集失败，请在控制台查看错误详情");
  else if (account.status === "low") notes.push("余额低于已设置的告警阈值");
  else if (!account.checked_at || account.status === "pending") notes.push("尚未完成采集");
  const checked = account.checked_at ? Date.parse(account.checked_at) : NaN;
  if (account.enabled && Number.isFinite(checked) && now - checked > Math.max(90, (account.interval_seconds || 300) * 2) * 1000) notes.push("超过两个采集周期未更新");
  if (account.balance == null) notes.push("余额未采集");
  if (account.today_used == null || account.today_date !== date) notes.push(account.today_date && account.today_date !== date ? "今日消费已跨日，等待重新采集" : "今日消费未采集");
  if (account.collection_notes?.length) notes.push(...account.collection_notes);
  else if (account.has_warning) notes.push("部分项目采集异常，请在控制台查看详情");
  return [...new Set(notes)];
}

export function displayAmount(value: number | null, currency: string, exact = false) {
  if (value == null) return "—";
  const formatted = value.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: exact ? 20 : 2 });
  return currency === "USD" ? `$${formatted}` : `${formatted} ${currency || "未设置单位"}`;
}
