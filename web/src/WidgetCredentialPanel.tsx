import { useEffect, useState } from "react";
import { Download, Monitor } from "lucide-react";
import { version } from "../package.json";
import { api } from "./api";
import CopyButton from "./CopyButton";

export default function WidgetCredentialPanel() {
  const [configured, setConfigured] = useState<boolean | null>(null);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    api<{ configured: boolean }>("/widget/credential", "GET", undefined, controller.signal)
      .then(result => setConfigured(result.configured))
      .catch(error => { if (!controller.signal.aborted) setError((error as Error).message); });
    return () => controller.abort();
  }, []);

  async function update(revoke: boolean) {
    if (configured && !confirm(revoke ? "撤销桌面小部件凭证？已连接的小部件将停止更新。" : "重新生成凭证？已连接的小部件需要填写新凭证。")) return;
    setBusy(true); setError("");
    try {
      if (revoke) {
        await api("/widget/credential", "DELETE");
        setConfigured(false); setToken("");
      } else {
        const result = await api<{ token: string }>("/widget/credential", "POST");
        setConfigured(true); setToken(result.token);
      }
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(false); }
  }

  return <section className="panel settings-panel">
    <div className="panel-heading"><div><h2 className="icon-heading"><Monitor size={18} aria-hidden="true" />桌面小部件</h2><p>生成只读凭证，供 Windows 小部件读取余额和今日用量。</p></div></div>
    <div className="settings-fields">
      <p className="field-hint">{configured === null ? "正在读取凭证状态…" : configured ? "已启用只读凭证。重新生成或撤销后，原凭证立即失效。" : "尚未生成只读凭证。"}</p>
      {token && <div className="field"><label htmlFor="widget-credential">小部件凭证（仅显示一次）</label><div className="inline-actions"><input id="widget-credential" value={token} readOnly autoComplete="off" /><CopyButton text={token} label="复制小部件凭证" /></div><small>请复制到小部件设置中；关闭页面后无法再次查看。</small></div>}
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="inline-actions"><button type="button" className="secondary" disabled={busy || configured === null} onClick={() => void update(false)}>{configured ? "重新生成凭证" : "生成凭证"}</button>{configured && <button type="button" className="secondary danger" disabled={busy} onClick={() => void update(true)}>撤销凭证</button>}</div>
      <div className="inline-actions"><a className="secondary widget-download" href={`https://github.com/sectsnow/LedgerGate/releases/download/v${version}/LedgerGateWidget-Setup-x64.exe`} target="_blank" rel="noopener noreferrer"><Download size={14} aria-hidden="true" />下载 Windows 安装包</a></div>
    </div>
  </section>;
}
