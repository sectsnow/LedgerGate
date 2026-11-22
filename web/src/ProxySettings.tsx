import { useEffect, useId, useState } from "react";
import { api } from "./api";

type ProxyConfig = {
  enabled: boolean;
  protocol: string;
  host: string;
  port: number;
  username: string;
  has_password: boolean;
};

export default function ProxySettings() {
  const id = useId();
  const [config, setConfig] = useState<ProxyConfig | null>(null);
  const [password, setPassword] = useState("");
  const [clearPassword, setClearPassword] = useState(false);
  const [target, setTarget] = useState("https://example.com");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    api<ProxyConfig>("/proxy", "GET", undefined, controller.signal)
      .then(setConfig)
      .catch(error => { if (!controller.signal.aborted) setError((error as Error).message); });
    return () => controller.abort();
  }, [reload]);
  const set = (value: Partial<ProxyConfig>) => {
    setConfig(previous => previous && { ...previous, ...value });
    setMessage("");
  };
  const submit = async (testing: boolean) => {
    if (!config) return;
    setBusy(testing ? "test" : "save"); setError(""); setMessage("");
    const body = { ...config, password, clear_password: clearPassword, target_url: target };
    try {
      if (testing) {
        const result = await api<{status: number; duration_ms: number}>("/proxy/test", "POST", body);
        setMessage(`代理已连通，目标返回 HTTP ${result.status}，耗时 ${result.duration_ms} ms。测试未保存配置。`);
      } else {
        setConfig(await api<ProxyConfig>("/proxy", "PUT", body));
        setPassword(""); setClearPassword(false);
        setMessage("代理设置已保存。账户与渠道按各自开关使用代理。");
      }
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(""); }
  };
  return <form className="panel settings-panel" onSubmit={event => { event.preventDefault(); void submit(false); }}>
    <div className="panel-heading"><div><h2>网络代理</h2><p>为需要代理的上游账户和转发渠道提供统一的出站连接。</p></div></div>
    {config && <fieldset disabled={!!busy} style={{ border: 0, margin: 0, padding: 0, minWidth: 0 }}>
      <div className="settings-fields">
        <label className="checkbox"><input type="checkbox" checked={config.enabled} onChange={event => set({enabled: event.target.checked})} />启用全局代理</label>
        <div className="form-grid">
          <div className="field"><label htmlFor={`${id}-protocol`}>代理协议</label><select id={`${id}-protocol`} value={config.protocol} onChange={event => set({protocol: event.target.value})}><option value="http">HTTP</option><option value="https">HTTPS</option><option value="socks5">SOCKS5</option></select></div>
          <div className="field"><label htmlFor={`${id}-host`}>代理主机</label><input id={`${id}-host`} required={config.enabled} value={config.host} maxLength={253} placeholder="例如 10.0.0.2" onChange={event => set({host: event.target.value})} /></div>
          <div className="field"><label htmlFor={`${id}-port`}>代理端口</label><input id={`${id}-port`} type="number" required min={1} max={65535} value={config.port} onChange={event => set({port: Number(event.target.value)})} /></div>
          <div className="field"><label htmlFor={`${id}-username`}>代理用户名（可选）</label><input id={`${id}-username`} autoComplete="off" maxLength={255} value={config.username} onChange={event => set({username: event.target.value})} /></div>
          <div className="field"><label htmlFor={`${id}-password`}>代理密码（可选）</label><input id={`${id}-password`} type="password" autoComplete="new-password" maxLength={255} disabled={clearPassword} value={password} onChange={event => { setPassword(event.target.value); setMessage(""); }} /><small>{config.has_password ? "已加密保存，留空保留原密码。" : "按代理服务要求填写，无认证时留空。"}</small></div>
          {config.has_password && <label className="checkbox"><input type="checkbox" checked={clearPassword} onChange={event => { setClearPassword(event.target.checked); setPassword(""); setMessage(""); }} />清除已保存的代理密码</label>}
        </div>
        <p className="field-hint">地址须能从 LedgerGate 服务器或容器访问，127.0.0.1 指服务器或容器自身。开启后，仅勾选“使用全局代理”的账户和渠道走代理；关闭后均直连。代理失败会报错，不会自动改走直连。</p>
        <div className="field"><label htmlFor={`${id}-target`}>连通性测试地址</label><input id={`${id}-target`} type="url" value={target} maxLength={2048} onChange={event => { setTarget(event.target.value); setMessage(""); }} /><small>测试使用当前填写的配置，不需要先保存，也不会调用账户接口。</small></div>
      </div>
      <div className="form-footer"><button type="button" className="secondary" onClick={() => void submit(true)}>{busy === "test" ? "正在测试…" : "测试代理连接"}</button><button className="primary" type="submit">{busy === "save" ? "正在保存…" : "保存代理设置"}</button></div>
    </fieldset>}
    {!config && !error && <p className="settings-fields" role="status">正在读取代理设置…</p>}
    {error && <div className="settings-fields"><p className="form-error" role="alert">{error}</p>{!config && <button type="button" className="secondary" onClick={() => setReload(value => value + 1)}>重新加载</button>}</div>}
    {message && <p className="settings-fields" role="status">{message}</p>}
  </form>;
}
