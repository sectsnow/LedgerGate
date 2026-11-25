import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import WidgetDisplaySettings from "./WidgetDisplaySettings";
import type { DisplayPrefs } from "./widgetDisplay";
import type { ReadingPrefs } from "./reading";

type Update = { version: string; available: boolean };
export default function WidgetSettings({ version, reading, onReading, prefs, currencies, onPrefs, onReset, onConnection, onClose }: {
  version: string; reading: ReadingPrefs; onReading: (value: ReadingPrefs) => void; prefs: DisplayPrefs; currencies: string[];
  onPrefs: (value: Partial<DisplayPrefs>) => void; onReset: () => Promise<void>; onConnection: () => void; onClose: () => void;
}) {
  const [autostart, setAutostart] = useState<boolean | null>(null);
  const [startupBusy, setStartupBusy] = useState(false);
  const [startupError, setStartupError] = useState("");
  const [checking, setChecking] = useState(false);
  const [update, setUpdate] = useState<Update | null>(null);
  const [updateError, setUpdateError] = useState("");
  const [displayOpen, setDisplayOpen] = useState(false);
  useEffect(() => {
    let active = true;
    void invoke<boolean>("get_autostart").then(value => { if (active) setAutostart(value); }).catch(reason => { if (active) setStartupError(String(reason)); });
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    document.addEventListener("keydown", escape);
    return () => { active = false; document.removeEventListener("keydown", escape); };
  }, [onClose]);
  async function changeStartup(enabled: boolean) {
    setStartupBusy(true); setStartupError("");
    try { setAutostart(await invoke<boolean>("set_autostart", { enabled })); }
    catch (reason) { setStartupError(String(reason)); }
    finally { setStartupBusy(false); }
  }
  async function checkUpdate() {
    setChecking(true); setUpdateError(""); setUpdate(null);
    try { setUpdate(await invoke<Update>("check_widget_update")); }
    catch (reason) { setUpdateError(String(reason)); }
    finally { setChecking(false); }
  }
  function openRelease(asset: string | null = null) {
    if (update) void invoke("open_widget_release", { version: update.version, asset }).catch(reason => setUpdateError(String(reason)));
  }
  return <div className="widget-preferences" aria-label="小部件设置">
    <div className="section-title"><h2>小部件设置</h2><button type="button" className="secondary" onClick={onClose}>完成</button></div>
    <fieldset><legend>阅读方式</legend>
      <label>内容密度<select value={reading.density} onChange={event => onReading({ ...reading, density: event.target.value as ReadingPrefs["density"] })}><option value="comfortable">舒适</option><option value="compact">紧凑</option></select></label>
      <label>文字大小<select value={reading.textSize} onChange={event => onReading({ ...reading, textSize: event.target.value as ReadingPrefs["textSize"] })}><option value="standard">标准</option><option value="large">大字</option></select></label>
      <button type="button" className="secondary" onClick={() => onReading({ ...reading, collapsed: [] })}>展开总览全部分区</button>
      <p>总览及分析内部的小节可分别折叠并记忆；全部默认展开。金额默认显示两位小数，悬停查看完整精度。</p>
    </fieldset>
    <WidgetDisplaySettings prefs={prefs} currencies={currencies} open={displayOpen} onOpen={setDisplayOpen} onChange={onPrefs} onReset={onReset} />
    <fieldset><legend>启动与版本</legend>
      <label><input type="checkbox" checked={autostart === true} disabled={autostart === null || startupBusy} onChange={event => void changeStartup(event.target.checked)} />登录 Windows 后自动启动{startupBusy ? "（保存中）" : ""}</label>
      {startupError && <p className="error" role="alert">{startupError}</p>}
      <p>便携版请保留程序位置；移动后需重新开启启动项。</p>
      <p>当前小部件版本：{version ? `v${version}` : "读取中"}</p>
      <button type="button" className="secondary" disabled={checking || !version} onClick={() => void checkUpdate()}>{checking ? "正在检查…" : "检查更新"}</button>
      <p>手动向 GitHub 查询正式版本，不发送服务器地址或只读凭证；下载后由你决定是否安装。</p>
      <div role="status">{update && <p>{update.available ? `发现新版本 v${update.version}` : `未发现更新（远端 v${update.version}）`}</p>}</div>
      {update?.available && <div className="update-actions"><button type="button" className="secondary" onClick={() => openRelease()}>查看更新说明</button><button type="button" className="secondary" onClick={() => openRelease("installer")}>下载安装包</button><button type="button" className="secondary" onClick={() => openRelease("portable")}>下载便携版</button></div>}
      {updateError && <p className="error" role="alert">{updateError}</p>}
    </fieldset>
    <button type="button" className="secondary" onClick={onConnection}>连接设置</button>
  </div>;
}
