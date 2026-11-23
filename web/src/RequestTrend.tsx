import { useEffect, useId, useRef, useState } from "react";
import { requestTrendPath, requestTrendSegments } from "./requestTrendPath";
import { requestTrendFormat, requestTrendMetrics, requestTrendValue } from "./requestTrendData";
import type { RequestTrendMetric, RequestTrendPoint } from "./requestTrendData";

export default function RequestTrend({ points }: { points: RequestTrendPoint[] }) {
  const container = useRef<HTMLDivElement>(null);
  const scroll = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);
  const [scrollLeft, setScrollLeft] = useState(0);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const [metric, setMetric] = useState<RequestTrendMetric>("requests");
  const gradientID = useId(), tooltipID = useId();
  const { label, series } = requestTrendMetrics[metric];
  useEffect(() => {
    if (!container.current) return;
    const observer = new ResizeObserver(entries => setWidth(Math.max(200, entries[0].contentRect.width)));
    observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  const minimumWidth = points.length > 31 ? points.length * 10 + 54 : 0;
  const chartWidth = Math.max(width, minimumWidth);
  const maxValue = Math.ceil(Math.max(2, ...points.flatMap(point => series.map(item => requestTrendValue(point, item.key) ?? 0))) / 2) * 2;
  const left = Math.max(36, requestTrendFormat(metric, maxValue).length * 7 + 12), right = 12, top = 12, bottom = 172;
  const plotWidth = chartWidth - left - right;
  const x = (index: number) => left + (index + 0.5) * plotWidth / points.length;
  const y = (value: number) => bottom - value / maxValue * (bottom - top);
  const curves = series.map(item => ({ ...item, segments: requestTrendSegments(points.map((point, i) => {
    const value = requestTrendValue(point, item.key);
    return { x: x(i), y: value === null ? null : y(value) };
  })) }));
  const labels = Math.min(points.length, 5, Math.max(2, Math.floor(plotWidth / 75) + 1));
  const labelIndices = Array.from({ length: labels }, (_, i) => Math.round(i * (points.length - 1) / Math.max(1, labels - 1)));
  const active = activeIndex === null ? null : points[activeIndex];
  const anchorX = activeIndex === null ? 0 : x(activeIndex) - scrollLeft;
  // 浏览器滚动位置按像素取整，边缘数据点允许一像素误差。
  const visible = active && anchorX >= left - 1 && anchorX <= width - right + 1;
  const tooltipWidth = Math.min(220, width - 16);
  const tooltipLeft = Math.max(8, Math.min(anchorX + 12 + tooltipWidth > width - 8 ? anchorX - tooltipWidth - 12 : anchorX + 12, width - tooltipWidth - 8));
  const unknownCosts = points.reduce((sum, point) => sum + (point.cost_unknown_requests ?? 0), 0);
  function select(index: number, reveal = false) {
    if (!points.length) return;
    index = Math.max(0, Math.min(points.length - 1, index));
    if (reveal && scroll.current) {
      const viewport = scroll.current;
      if (x(index) < viewport.scrollLeft + left) viewport.scrollLeft = Math.max(0, x(index) - left);
      else if (x(index) > viewport.scrollLeft + width - right) viewport.scrollLeft = x(index) - width + right;
    }
    setActiveIndex(index);
  }
  function inspect(event: { currentTarget: SVGSVGElement; clientX: number }) {
    const bounds = event.currentTarget.getBoundingClientRect();
    select(Math.floor(((event.clientX - bounds.left) * chartWidth / bounds.width - left) / plotWidth * points.length));
  }
  return <div className="request-trend" ref={container}>
    <div className="request-trend-toolbar">
      <div className="balance-period" role="group" aria-label="请求趋势指标">
        {(["requests", "tokens", "cost"] as const).map(value => <button key={value} aria-pressed={metric === value} onClick={() => { setMetric(value); setActiveIndex(null); }}>{requestTrendMetrics[value].label}</button>)}
      </div>
      <div className="request-trend-legends">{series.map(item => <span key={item.key}><i style={{ borderColor: item.color, borderTopStyle: item.dashed ? "dashed" : "solid" }} />{item.label}</span>)}</div>
    </div>
    <div className="request-trend-chart">
      <div className="traffic-chart-scroll" ref={scroll} role="region" aria-label={`${label}趋势，左右方向键查看时间点，Home 和 End 跳转首尾`} aria-describedby={visible ? tooltipID : undefined} tabIndex={points.length ? 0 : undefined}
        onFocus={() => select(points.length - 1, true)} onBlur={() => setActiveIndex(null)}
        onScroll={event => setScrollLeft(event.currentTarget.scrollLeft)}
        onKeyDown={event => {
          if (!points.length || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
          event.preventDefault();
          select(event.key === "Home" ? 0 : event.key === "End" ? points.length - 1 : (activeIndex ?? points.length - 1) + (event.key === "ArrowRight" ? 1 : -1), true);
        }}>
        <svg className="request-trend-svg" viewBox={`0 0 ${chartWidth} 204`} style={{ minWidth: minimumWidth || undefined }} role="img" aria-label={`${label}平滑曲线面积图`}
          onPointerMove={inspect} onClick={inspect} onPointerLeave={() => setActiveIndex(null)}>
          <desc>按小时或日期汇总{label}，曲线经过实际数据点且不叠加，纵轴从零开始，未知数据留空。</desc>
          <defs>{series.map(item => <linearGradient key={item.key} id={`${gradientID}-${item.key}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={item.color} stopOpacity={0.22} />
            <stop offset="100%" stopColor={item.color} stopOpacity={0.02} />
          </linearGradient>)}</defs>
          {[0, maxValue / 2, maxValue].map(value => <g key={value}>
            <line className="request-trend-grid" x1={left} x2={chartWidth - right} y1={y(value)} y2={y(value)} />
            <text x={left - 9} y={y(value) + 4} textAnchor="end">{requestTrendFormat(metric, value)}</text>
          </g>)}
          {curves.map(item => <g key={item.key} data-series={item.key}>
            {item.segments.map((segment, i) => {
              const path = requestTrendPath(segment);
              return <g key={i}>
                <path d={`${path} L${segment.at(-1)!.x},${bottom} L${segment[0].x},${bottom} Z`} fill={`url(#${gradientID}-${item.key})`} />
                <path className="request-trend-line" d={path} style={{ stroke: item.color }} strokeDasharray={item.dashed ? "5 4" : undefined} />
                {segment.length === 1 && <circle className="request-trend-point" cx={segment[0].x} cy={segment[0].y} r={4} style={{ stroke: item.color }} />}
              </g>;
            })}
          </g>)}
          {!points.length && <text x={chartWidth / 2} y={90} textAnchor="middle">暂无请求数据</text>}
          {points.length > 0 && curves.every(item => !item.segments.length) && <text x={(left + chartWidth - right) / 2} y={90} textAnchor="middle">{metric === "cost" ? "暂无可估算费用" : "暂无该指标数据"}</text>}
          {labelIndices.map(i => <text key={i} x={x(i)} y={196} textAnchor={points.length === 1 ? "middle" : i === 0 ? "start" : i === points.length - 1 ? "end" : "middle"}>{points[i].label}</text>)}
          {visible && activeIndex !== null && <>
            <line className="request-trend-cursor" x1={x(activeIndex)} x2={x(activeIndex)} y1={top} y2={bottom} />
            {series.map(item => {
              const value = requestTrendValue(active, item.key);
              return value === null ? null : <circle key={item.key} className="request-trend-point" cx={x(activeIndex)} cy={y(value)} r={4} style={{ stroke: item.color }} />;
            })}
          </>}
        </svg>
      </div>
      {visible && <div id={tooltipID} className="balance-tooltip request-trend-tooltip" role="status" style={{ left: tooltipLeft, top: 8, width: tooltipWidth }}>
        <strong>{active.title}</strong>{series.map(item => {
          const value = requestTrendValue(active, item.key);
          return <div key={item.key} className="balance-tooltip-row"><span style={{ color: item.color }}>{item.label}</span><b>{value === null ? metric === "cost" ? "费用未知" : "未提供" : `${requestTrendFormat(metric, value)}${metric === "requests" ? " 次" : ""}`}</b></div>;
        })}
        {metric === "cost" && !!active.cost_unknown_requests && <small>{active.cost_unknown_requests.toLocaleString("zh-CN")} 次费用未知，未计入估算</small>}
      </div>}
    </div>
    <p className="request-trend-note">{metric === "requests" ? "失败／取消计入总请求，两条曲线不叠加。" : metric === "tokens" ? "按网关记录的输入、输出 Token 汇总。" : `按美元估算，以上游实际结算为准。${unknownCosts ? `${unknownCosts.toLocaleString("zh-CN")} 次费用未知，未计入估算；全部未知的时段留空。` : ""}`}</p>
  </div>;
}
