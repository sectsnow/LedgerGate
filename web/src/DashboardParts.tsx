import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Activity, Coins, Receipt, Wallet } from "lucide-react";
import type { AccountRow, AccountSummary, Data, RequestDay } from "./api";

import { Stat, financialAmount } from "./WorkspaceStats";

function chartDomain(values: (number | null)[]): [number, number] {
  const known = values.filter((value): value is number => value !== null);
  const minimum = Math.min(0, ...known);
  const maximum = Math.max(0, ...known);
  const rough = Math.max(maximum - minimum, 0.01) / 4;
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  const normalized = rough / magnitude;
  let step = magnitude;
  if (normalized > 5) step *= 10;
  else if (normalized > 2) step *= 5;
  else if (normalized > 1) step *= 2;
  const lower = Math.floor(minimum / step) * step;
  const upper = Math.ceil(maximum / step) * step;
  return [lower, upper === lower ? lower + step * 4 : upper];
}

function BalanceOverview({
  summary,
  accounts,
}: {
  summary: AccountSummary;
  accounts: AccountRow[];
}) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const [hoverY, setHoverY] = useState<number | null>(null);
  const tooltip = useRef<HTMLDivElement>(null);
  const tooltipID = React.useId();
  const [tooltipSize, setTooltipSize] = useState({ width: 220, height: 120 });
  const [days, setDays] = useState<7 | 14 | 30>(7);
  useEffect(() => {
    if (!container.current) return;
    const observer = new ResizeObserver((entries) =>
      setWidth(Math.max(200, entries[0].contentRect.width)),
    );
    observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  const points = summary.history.slice(-days);
  const balances = points.map((point) => point.balance);
  const consumption = points.map((point) => point.consumption);
  const partialDays = points.filter((point) =>
    (point.balance !== null && point.balance_accounts < summary.accounts) ||
    (point.consumption !== null && point.consumption_accounts < summary.accounts),
  ).length;
  const [balanceMin, balanceMax] = chartDomain(balances);
  const [costMin, costMax] = chartDomain(consumption);
  const left = 56;
  const right = 56;
  const top = 26;
  const bottom = 222;
  const plotWidth = width - left - right;
  const x = (index: number) =>
    left + (index * plotWidth) / Math.max(1, points.length - 1);
  const halfStep = plotWidth / Math.max(1, points.length - 1) / 2;
  const labelCount = Math.min(
    points.length,
    Math.max(2, Math.floor(plotWidth / 55) + 1),
  );
  const labelIndexes = new Set(
    Array.from({ length: labelCount }, (_, index) =>
      Math.round((index * (points.length - 1)) / Math.max(1, labelCount - 1)),
    ),
  );
  const balanceY = (value: number) =>
    bottom -
    ((value - balanceMin) / (balanceMax - balanceMin)) * (bottom - top);
  const costY = (value: number) =>
    bottom - ((value - costMin) / (costMax - costMin)) * (bottom - top);
  const linePath = (
    values: (number | null)[],
    y: (value: number) => number,
  ) => {
    let connected = false;
    return values.map((value, index) => {
      if (value === null) { connected = false; return ""; }
      const command = connected ? "L" : "M";
      connected = true;
      return `${command}${x(index).toFixed(2)},${y(value).toFixed(2)}`;
    }).join(" ");
  };
  const active = activeIndex ?? points.length - 1;
  const point = points[active];
  const recordedDays = points.filter(
    (_, index) => balances[index] !== null || consumption[index] !== null,
  ).length;
  const currency = summary.currency;
  useLayoutEffect(() => {
    if (!tooltip.current) return;
    const { offsetWidth, offsetHeight } = tooltip.current;
    setTooltipSize((previous) => previous.width === offsetWidth && previous.height === offsetHeight
      ? previous
      : { width: offsetWidth, height: offsetHeight });
  }, [activeIndex, point, width, currency]);
  const anchorX = x(active);
  const anchorY = hoverY ?? (top + bottom) / 2;
  const tooltipLeft = Math.max(8, Math.min(
    anchorX + 12 + tooltipSize.width > width - 8 ? anchorX - tooltipSize.width - 12 : anchorX + 12,
    width - tooltipSize.width - 8,
  ));
  const tooltipTop = Math.max(8, Math.min(
    anchorY + 12 + tooltipSize.height > 252 ? anchorY - tooltipSize.height - 12 : anchorY + 12,
    252 - tooltipSize.height,
  ));
  const inspect = (index: number, clientY: number) => {
    setActiveIndex(index);
    setHoverY(clientY - (container.current?.getBoundingClientRect().top || 0));
  };
  const tick = (value: number) =>
    value.toLocaleString("zh-CN", {
      notation: "compact",
      maximumFractionDigits: 4,
    });
  return (
    <section className="panel balance-overview" data-tone="balance">
      <div className="panel-heading">
        <div><h2 className="icon-heading"><Wallet size={18} aria-hidden="true" />余额概览</h2>
          <p>每日按实际采集快照汇总，暂停监控仍保留历史记录。</p>
        </div>
        <div className="balance-heading-controls">
          <div
            className="balance-period"
            role="group"
            aria-label="余额概览时间范围"
          >
            {([7, 14, 30] as const).map((period) => (
              <button
                key={period}
                type="button"
                aria-pressed={days === period}
                onClick={() => {
                  setDays(period);
                  setActiveIndex(null);
                  setHoverY(null);
                }}
              >
                {period}天
              </button>
            ))}
          </div>
          <span>{currency || "未设置单位"}</span>
        </div>
      </div>
      <div className="balance-legend">
        <span>
          <i className="balance-dot" />
          余额
        </span>
        <span>
          <i className="consumption-dot" />
          消费趋势
        </span>
        {partialDays > 0 && <span><i className="partial-dot" />部分采集合计</span>}
      </div>
      <div className="balance-chart" ref={container}>
        <svg
          viewBox={`0 0 ${width} 260`}
          width="100%"
          height="260"
          role="group"
          aria-label={`最近${days}天账户余额与实际消费趋势`}
        >
          <text x={left} y="12" className="balance-axis-title">余额</text>
          <text x={width - right} y="12" textAnchor="end" className="consumption-axis-title">消费</text>
          {[0, 0.25, 0.5, 0.75, 1].map((fraction) => {
            const y = top + (bottom - top) * fraction;
            return (
              <g key={fraction}>
                <line
                  x1={left}
                  x2={width - right}
                  y1={y}
                  y2={y}
                  className="balance-grid"
                />
                <text x={left - 8} y={y + 3} textAnchor="end">
                  {tick(balanceMax - fraction * (balanceMax - balanceMin))}
                </text>
                <text x={width - right + 8} y={y + 3}>
                  {tick(costMax - fraction * (costMax - costMin))}
                </text>
              </g>
            );
          })}
          <path d={linePath(balances, balanceY)} className="balance-line" />
          <path d={linePath(consumption, costY)} className="consumption-line" />
          {points.map((day, index) => (
            <g key={day.date}>
              {balances[index] !== null && (
                <circle
                  cx={x(index)}
                  cy={balanceY(balances[index]!)}
                  r={activeIndex === index ? 5 : 4}
                  className={`balance-point${activeIndex === index ? " active" : ""}`}
                />
              )}
              {consumption[index] !== null && (
                <circle
                  cx={x(index)}
                  cy={costY(consumption[index]!)}
                  r={activeIndex === index ? 5 : 4}
                  className={`consumption-point${activeIndex === index ? " active" : ""}`}
                />
              )}
              {labelIndexes.has(index) && (
                <text x={x(index)} y="248" textAnchor="middle">
                  {day.date.slice(5).replace("-", "/")}
                </text>
              )}
              <g
                className="balance-day"
                tabIndex={0}
                role="img"
                aria-label={`${day.date}，余额${day.balance_accounts < summary.accounts && day.balance !== null ? "（部分合计）" : ""} ${balances[index] === null ? "未采集" : financialAmount(balances[index], currency)}，消费${day.consumption_accounts < summary.accounts && day.consumption !== null ? "（部分合计）" : ""} ${consumption[index] === null ? "未采集" : financialAmount(consumption[index], currency)}`}
                aria-describedby={activeIndex === index ? tooltipID : undefined}
                onPointerEnter={(event) => inspect(index, event.clientY)}
                onPointerMove={(event) => inspect(index, event.clientY)}
                onPointerLeave={(event) => {
                  if (event.pointerType !== "touch") setActiveIndex(null);
                }}
                onClick={(event) => inspect(index, event.clientY)}
                onFocus={() => { setActiveIndex(index); setHoverY(null); }}
                onBlur={() => setActiveIndex(null)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") setActiveIndex(null);
                }}
              >
                {activeIndex === index && (
                  <line
                    x1={x(index)}
                    x2={x(index)}
                    y1={top}
                    y2={bottom}
                    className="balance-cursor"
                  />
                )}
                <rect
                  x={index === 0 ? left : x(index) - halfStep}
                  y={top}
                  width={
                    index === 0 || index === points.length - 1
                      ? halfStep
                      : halfStep * 2
                  }
                  height={bottom - top}
                  fill="transparent"
                  className="balance-hit"
                />
              </g>
            </g>
          ))}
        </svg>
        {activeIndex !== null && point && (
          <div
            className="balance-tooltip"
            id={tooltipID}
            role="tooltip"
            aria-live="polite"
            ref={tooltip}
            style={{ left: tooltipLeft, top: tooltipTop }}
          >
            <strong className="balance-tooltip-date">
              <time dateTime={point.date}>{Number(point.date.slice(5, 7))}月{Number(point.date.slice(8, 10))}日</time>
            </strong>
            <div className="balance-tooltip-row balance-tooltip-balance">
              <span>{point.balance !== null && point.balance_accounts < summary.accounts ? "余额（部分）：" : "余额："}</span><b>{financialAmount(balances[active], currency)}</b>
            </div>
            <div className="balance-tooltip-row balance-tooltip-consumption">
              <span>{point.consumption !== null && point.consumption_accounts < summary.accounts ? "消费（部分）：" : "消费："}</span><b>{financialAmount(consumption[active], currency)}</b>
            </div>
            {(point.balance_accounts < summary.accounts || point.consumption_accounts < summary.accounts) && (
              <small>余额已采集 {point.balance_accounts}/{summary.accounts} · 消费已采集 {point.consumption_accounts}/{summary.accounts}</small>
            )}
          </div>
        )}
      </div>
      {(recordedDays < points.length || partialDays > 0) && (
        <p className="balance-history-note">
          已记录 {recordedDays}/{points.length} 天，无采集记录的日期留空。{partialDays > 0 && "相邻有数据的日期以实线连接，部分合计可查看采集覆盖数量。"}
        </p>
      )}
      <div className="balance-account-list">
        {accounts.map(({ account }) => (
          <div className="balance-account-item" key={account.id}>
            <i
              className={`account-status ${account.enabled ? account.status : "paused"}`}
            />
            <strong>{account.name}</strong>
            <span>
              {financialAmount(account.balance, account.currency)} · 今日 {financialAmount(account.today_used, account.currency)}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}

export function AccountFinance({ data }: { data: Data }) {
  const summaries = data.overview.account_summaries || [];
  const currencyId = React.useId();
  const [currency, setCurrency] = useState(()=>{try{return localStorage.getItem("ledgergate-finance-currency")||summaries[0]?.currency||"";}catch{return summaries[0]?.currency||"";}});
  const summary =
    summaries.find((item) => item.currency === currency) || summaries[0];
  const unit = summary?.currency || "";
  useEffect(()=>{if(summary)try{localStorage.setItem("ledgergate-finance-currency",summary.currency);}catch{/* 禁用存储时保留当前币种。 */}},[summary?.currency]);
  const coverage = (known: number, complete: string) =>
    summary && known < summary.accounts
      ? `已采集 ${known}/${summary.accounts} 个账户`
      : complete;
  const minimum = summary?.minimum;
  const accounts = data.accounts.filter(
    (row) => row.account.currency.trim().toUpperCase() === unit,
  );
  return (
    <>
      {summaries.length > 1 && (
        <div className="finance-currency">
          <label htmlFor={currencyId}>统计单位</label>
          <select
            id={currencyId}
            value={unit}
            onChange={(event) => setCurrency(event.target.value)}
          >
            {summaries.map((item) => (
              <option value={item.currency} key={item.currency}>
                {item.currency || "未设置单位"}
              </option>
            ))}
          </select>
        </div>
      )}
      <div className="stats-grid finance-stats">
        <Stat
          label="总余额"
          tone="balance"
          value={financialAmount(summary?.balance ?? null, unit)}
          icon={<Wallet size={18} />}
          note={
            minimum
              ? `最低 ${minimum.name} ${financialAmount(minimum.balance, unit)}${summary && summary.balance_accounts < summary.accounts ? ` · 已采集 ${summary.balance_accounts}/${summary.accounts}` : ""}`
              : "等待账户余额采集"
          }
        />
        <Stat
          label="今日总消费"
          tone="cost"
          value={financialAmount(summary?.today_used ?? null, unit)}
          icon={<Receipt size={18} aria-hidden="true" />}
          note={[
            coverage(summary?.today_accounts || 0, "按上游实际扣费统计"),
            summary?.today_collected_at
              ? `最早采集 ${new Date(summary.today_collected_at).toLocaleTimeString("zh-CN", { hour12: false })}（非实时）`
              : "成功采集后显示更新时间",
          ].join(" · ")}
        />
        <Stat
          label="累计消费"
          tone="cost"
          value={financialAmount(summary?.used ?? null, unit)}
          icon={<Coins size={18} aria-hidden="true" />}
          note={coverage(summary?.used_accounts || 0, "各账户累计实际扣费")}
        />
      </div>
      {summary && <BalanceOverview summary={summary} accounts={accounts} />}
    </>
  );
}

export function RequestActivity({
  days,
  retentionDays,
}: {
  days: RequestDay[];
  retentionDays: number;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const latest = days.at(-1)?.date;
  useEffect(() => {
    if (scroll.current) scroll.current.scrollLeft = scroll.current.scrollWidth;
  }, [latest]);
  if (!days.length) return null;
  const active =
    days.find((day) => day.date === selected) || days[days.length - 1];
  const maximum = Math.max(1, ...days.map((day) => day.requests));
  const total = days.reduce((sum, day) => sum + day.requests, 0);
  const activeDays = days.filter((day) => day.requests > 0).length;
  const offset = (new Date(`${days[0].date}T00:00:00Z`).getUTCDay() + 6) % 7;
  const cells: (RequestDay | null)[] = [...Array(offset).fill(null), ...days];
  while (cells.length % 7) cells.push(null);
  const describe = (day: RequestDay) => {
    if (!day.requests) return `${day.date}：无汇总记录`;
    const cost = (day.cost_unknown_requests || 0) >= day.requests ? "费用无法估算" : `费用估算 $${((day.cost_micros || 0) / 1e6).toFixed(6)}`;
    return `${day.date}：${day.requests.toLocaleString("zh-CN")} 次请求 · 输入 ${(day.input_tokens || 0).toLocaleString("zh-CN")} / 输出 ${(day.output_tokens || 0).toLocaleString("zh-CN")} Token · ${cost}${day.cost_unknown_requests ? ` · ${day.cost_unknown_requests} 次费用未知，未计入估算` : ""}`;
  };
  return (
    <section className="panel request-activity">
      <div className="panel-heading">
        <div>
          <h2 className="icon-heading"><Activity size={18} aria-hidden="true" />请求活跃度</h2>
          <p>近一年 · 每格代表一天</p>
        </div>
        <span>
          {total.toLocaleString("zh-CN")} 次请求 · {activeDays} 个活跃日
        </span>
      </div>
      <div className="activity-scroll" ref={scroll}>
        <div
          className="activity-grid"
          role="group"
          aria-label="每日网关请求活跃度"
        >
          {cells.map((day, cellIndex) => {
            if (!day)
              return (
                <span
                  key={`padding-${cellIndex}`}
                  className="activity-padding"
                  aria-hidden="true"
                />
              );
            const index = cellIndex - offset;
            const level =
              day.requests === 0 ? 0 : Math.ceil((day.requests / maximum) * 4);
            return (
              <button
                key={day.date}
                type="button"
                className="activity-day activity-level"
                data-date={day.date}
                data-level={level}
                title={describe(day)}
                aria-label={describe(day)}
                aria-pressed={active.date === day.date}
                tabIndex={active.date === day.date ? 0 : -1}
                onMouseEnter={() => setSelected(day.date)}
                onFocus={() => setSelected(day.date)}
                onClick={() => setSelected(day.date)}
                onKeyDown={(event) => {
                  const moves: Record<string, number> = {
                    ArrowLeft: -7,
                    ArrowRight: 7,
                    ArrowUp: -1,
                    ArrowDown: 1,
                  };
                  const move = moves[event.key];
                  if (move === undefined) return;
                  event.preventDefault();
                  const next =
                    days[Math.max(0, Math.min(days.length - 1, index + move))];
                  event.currentTarget.parentElement
                    ?.querySelector<HTMLButtonElement>(
                      `[data-date="${next.date}"]`,
                    )
                    ?.focus();
                }}
              />
            );
          })}
        </div>
      </div>
      <div className="activity-footer">
        <span className="activity-inspector" aria-live="polite">
          {describe(active)}
        </span>
        <div className="activity-legend" aria-label="颜色越深代表请求越多">
          少
          {[0, 1, 2, 3, 4].map((level) => (
            <i
              className="activity-level"
              data-level={level}
              key={level}
              aria-hidden="true"
            />
          ))}
          多
        </div>
      </div>
      <p className="activity-note">
        按北京时间自然日统计，每日汇总保留 {retentionDays} 天，逐条请求到期清理后仍保留汇总。
      </p>
    </section>
  );
}
