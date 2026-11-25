import { accountNotes, displayAmount, finances, type Overview } from "./summary";

export const collectionTime = (value: number | string | null | undefined) => {
  const time = typeof value === "number" ? value : value ? Date.parse(value) : NaN;
  return Number.isFinite(time) ? new Date(time).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "Asia/Shanghai" }) : "时间未知";
};
export function Money({ value, currency }: { value: number | null; currency: string }) {
  const exact = displayAmount(value, currency, true);
  return <strong title={exact} aria-label={exact}>{displayAmount(value, currency)}</strong>;
}
export default function AccountAssets({ overview, now }: { overview: Overview | null; now: number }) {
  const groups = overview ? finances(overview) : [];
  const today = new Date(now + 8 * 3600_000).toISOString().slice(0, 10);
  if (!overview || !groups.length) return <p className="empty">{overview ? "暂无上游账户" : "等待账户数据"}</p>;
  return <>
    {overview.accounts.some(account => account.id == null) && <p className="detail-line">服务端未提供完整采集详情字段；账户暂以序号显示，消费采集时间保持未知。升级服务端后可查看完整详情。</p>}
    <div className="finance-head"><span>余额</span><span>今日实际消费</span></div>
    {groups.map(group => {
      const accounts = overview.accounts.filter(account => account.currency.trim().toUpperCase() === group.currency);
      const problems = accounts.filter(account => accountNotes(account, today, now).length > 0).length;
      return <div className="finance-row" key={group.currency}>
        <div><Money value={group.balance} currency={group.currency} /><small>余额已采集 {group.balanceCount}/{group.accounts}</small></div>
        <div><Money value={group.today} currency={group.currency} /><small>消费已采集 {group.todayCount}/{group.accounts}</small></div>
        <p>上游检查：{group.checkedCount === group.accounts ? `最早 ${collectionTime(group.oldestCheck)}` : `${group.checkedCount}/${group.accounts} 个账户时间已知`}</p>
        <p>消费成功采集：{group.todayCount > 0 && group.todayCheckedCount === group.todayCount ? `最早 ${collectionTime(group.oldestTodayCheck)}` : "时间未知，等待成功采集"}（非实时）</p>
        <details className="collection-details"><summary>查看 {group.accounts} 个账户采集详情{problems > 0 ? ` · ${problems} 个需关注` : " · 正常"}</summary>
          <ul>{accounts.map((account, index) => {
            const notes = accountNotes(account, today, now);
            return <li key={account.id ?? index}><div className="collection-heading"><b>{account.name || `账户 ${index + 1}`}</b><span className={notes.length ? "notice" : "hint"}>{notes.length ? "需关注" : "正常"}</span></div>
              <p>{notes.length ? notes.join("；") : "余额与今日消费均已采集"}</p>
              <p>上游检查 {collectionTime(account.checked_at)} · 消费采集 {account.today_date === overview.date ? collectionTime(account.today_collected_at) : "等待今日采集"}</p>
            </li>;
          })}</ul>
        </details>
      </div>;
    })}
  </>;
}
