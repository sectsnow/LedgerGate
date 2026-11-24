export type Account = {
  currency: string;
  balance: number | null;
  today_used: number | null;
  today_date: string;
  status: string;
  enabled: boolean;
  checked_at: string | null;
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
};

export function finances(overview: Overview): Finance[] {
  const groups = new Map<string, Finance>();
  for (const account of overview.accounts) {
    const currency = account.currency.trim().toUpperCase();
    let group = groups.get(currency);
    if (!group) {
      group = { currency, balance: null, today: null, accounts: 0, balanceCount: 0, todayCount: 0, attention: false, oldestCheck: null };
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
    }
    const checked = account.checked_at ? Date.parse(account.checked_at) : NaN;
    if (Number.isFinite(checked)) group.oldestCheck = Math.min(group.oldestCheck ?? checked, checked);
    if (!account.enabled || account.status !== "ok" || account.has_warning || !Number.isFinite(checked)) group.attention = true;
  }
  return [...groups.values()].sort((left, right) => left.currency === right.currency ? 0 : left.currency === "USD" ? -1 : right.currency === "USD" ? 1 : !left.currency ? 1 : !right.currency ? -1 : left.currency.localeCompare(right.currency));
}

export function amount(value: number | null, currency: string): string {
  if (value == null) return "—";
  const formatted = value.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 4 });
  return currency === "USD" ? `$${formatted}` : `${formatted} ${currency || "未设置单位"}`;
}
