import { badgeMetrics, metricLabels, type DisplayPrefs } from "./widgetDisplay.ts";

export default function WidgetDisplaySettings({ prefs, currencies, open, onOpen, onChange, onReset }: {
  prefs: DisplayPrefs; currencies: string[]; open: boolean; onOpen: (value: boolean) => void;
  onChange: (value: Partial<DisplayPrefs>) => void; onReset: () => Promise<void>;
}) {
  return <details className="display-settings" open={open} onToggle={event => onOpen(event.currentTarget.open)}>
    <summary>浮标与贴边</summary>
    <div className="display-settings-fields">
      <label><input type="checkbox" checked={prefs.autoHide} onChange={event => onChange({ autoHide: event.target.checked })} />拖到左右边缘后自动收起</label>
      <label><input type="checkbox" checked={prefs.keepExpanded} onChange={event => onChange({ keepExpanded: event.target.checked })} />保持贴边面板展开</label>
      <label><input type="checkbox" checked={prefs.avoidFullscreen} onChange={event => onChange({ avoidFullscreen: event.target.checked })} />浮标避让当前屏幕的全屏应用</label>
      <label>默认浮标数据<select value={prefs.metric} onChange={event => onChange({ metric: event.target.value as DisplayPrefs["metric"] })}>{badgeMetrics.map(metric => <option key={metric} value={metric}>{metricLabels[metric]}</option>)}</select></label>
      <label>金额币种<select value={prefs.currency} onChange={event => onChange({ currency: event.target.value })}><option value="">单一币种时自动选择</option>{[...new Set([...currencies, ...(prefs.currency ? [prefs.currency] : [])])].map(currency => <option key={currency} value={currency}>{currency || "未设置单位"}</option>)}</select></label>
      <label><input type="checkbox" checked={prefs.rotate} onChange={event => onChange({ rotate: event.target.checked })} />每 8 秒切换浮标数据</label>
      <fieldset><legend>轮播内容</legend>{badgeMetrics.map(metric => <label key={metric}><input type="checkbox" checked={prefs.metrics.includes(metric)} disabled={prefs.metrics.length === 1 && prefs.metrics.includes(metric)} onChange={event => onChange({ metrics: event.target.checked ? [...prefs.metrics, metric] : prefs.metrics.filter(value => value !== metric) })} />{metricLabels[metric]}</label>)}</fieldset>
      <p>浮标只显示今日数据；多币种金额分别展示。单击浮标展开，右键可切换指标或隐藏到托盘。</p>
      <button type="button" className="secondary" onClick={() => void onReset()}>重置窗口位置</button>
    </div>
  </details>;
}
