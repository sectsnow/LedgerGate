import { useId, useState } from "react";
import { Check, Download, RotateCcw, Search, Upload } from "lucide-react";
import { api, type Account } from "./api";
import AccountLogo from "./AccountLogo";

type IconCandidate = { url: string; data: string; mime: string };

export default function AccountLogoEditor({ account }: { account: Account }) {
  const id = useId();
  const [expanded, setExpanded] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [imageURL, setImageURL] = useState("");
  const [busy, setBusy] = useState<"fetch" | "save" | "url" | "reset" | null>(null);
  const [candidates, setCandidates] = useState<IconCandidate[] | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  function choose(value: File | null) {
    setError(""); setMessage("");
    setSelected(null);
    if (value && value.size > 4 * 1024 * 1024) { setError("图片不能超过 4 MiB。"); setFile(null); return; }
    setFile(value);
  }
  function updated() { window.dispatchEvent(new CustomEvent("account-logo-updated", { detail: account.id })); }
  async function saveIcon(body: FormData | { url: string }) {
    const upload = body instanceof FormData;
    setBusy(upload ? "save" : "url"); setError(""); setMessage("");
    try {
      const response = await fetch(`/api/accounts/${account.id}/icon`, { method: "PUT", headers: { "X-LedgerGate-Request": "1", ...(!upload && { "Content-Type": "application/json" }) }, body: upload ? body : JSON.stringify(body) });
      if (response.status === 401) window.dispatchEvent(new Event("session-expired"));
      if (!response.ok) { const result = await response.json().catch(() => ({})); throw new Error(result.error?.message || `保存失败（${response.status}）`); }
      updated(); setFile(null); setSelected(null); setMessage("Logo 已保存，重启和导入导出后均可保留。");
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(null); }
  }
  return <section className="account-logo-editor">
    <div className="account-logo-editor-heading"><AccountLogo account={account} /><h3>网站图标</h3><button type="button" className="secondary" aria-expanded={expanded} aria-controls={`${id}-settings`} onClick={() => setExpanded(value => !value)}>{expanded ? "收起" : "修改图标"}</button></div>
    <div id={`${id}-settings`} className="account-logo-editor-controls" hidden={!expanded}>
    <p>自动获取成功后会保存。手动设置优先使用，保存后立即生效。</p>
    <button type="button" className="secondary account-logo-fetch" disabled={!!busy} onClick={async () => {
      setBusy("fetch"); setError(""); setMessage(""); setSelected(null);
      try { const result = await api<{ items: IconCandidate[] }>(`/accounts/${account.id}/icon/candidates`, "POST"); setCandidates(result.items); }
      catch (error) { setError((error as Error).message); }
      finally { setBusy(null); }
    }}><Search size={14} />{busy === "fetch" ? "正在获取图标…" : "获取网站图标"}</button>
    {candidates !== null && <div className="account-logo-results">
      {!candidates.length ? <p role="status">未找到可用图标，可重试或手动上传。当前 Logo 保留。</p> : <>
        <p>找到 {candidates.length} 个图标，选择后保存。</p>
        <div className="account-logo-candidates" aria-label="可选网站图标">{candidates.map((icon, index) => <button key={icon.url} type="button" className="secondary account-logo-candidate" disabled={!!busy} aria-label={`选择图标 ${index + 1}`} aria-pressed={selected === index} title={icon.url} onClick={() => setSelected(index)}>
          <span className="account-logo-candidate-image"><img src={`data:${icon.mime};base64,${icon.data}`} alt="" width={44} height={44} /></span>
          <strong>图标 {index + 1}{selected === index && <Check size={12} aria-hidden="true" />}</strong>
          <small>{new URL(icon.url).hostname}</small>
          <small>{icon.mime.replace("image/", "").toUpperCase()} · {(icon.data.length * 3 / 4096).toFixed(1)} KiB</small>
        </button>)}</div>
        <button type="button" className="primary" disabled={!!busy || selected === null} onClick={() => {
          if (selected === null) return;
          const icon = candidates[selected];
          const body = new FormData();
          body.append("file", new Blob([Uint8Array.from(atob(icon.data), value => value.charCodeAt(0))], { type: icon.mime }), "website-logo");
          void saveIcon(body);
        }}>{busy === "save" ? "正在保存…" : "使用该图标"}</button>
      </>}
    </div>}
    <div className="account-logo-url">
      <label htmlFor={`${id}-url`}>图片地址</label>
      <div className="account-logo-url-row">
        <input id={`${id}-url`} type="url" placeholder="https://example.com/logo.png" maxLength={4096} value={imageURL} disabled={!!busy} onChange={event => setImageURL(event.target.value)} onKeyDown={event => {
          if (event.key === "Enter") { event.preventDefault(); if (!busy && imageURL.trim()) void saveIcon({ url: imageURL.trim() }); }
        }} />
        <button type="button" className="secondary" disabled={!!busy || !imageURL.trim()} onClick={() => void saveIcon({ url: imageURL.trim() })}><Download size={14} />{busy === "url" ? "正在下载并保存…" : "下载并保存"}</button>
      </div>
      <p>填写公网 HTTP(S) 图片直链，下载成功后替换当前 Logo 并保存到本地。</p>
    </div>
    <div className="account-logo-dropzone" onDragOver={event => event.preventDefault()} onDrop={event => { event.preventDefault(); if (!busy) choose(event.dataTransfer.files[0] || null); }}>
      <label htmlFor={id}>选择或拖入 Logo 图片</label>
      <input id={id} type="file" accept=".png,.jpg,.jpeg,.gif,.svg,.webp,.ico" disabled={!!busy} onChange={event => choose(event.target.files?.[0] || null)} />
      <small>最大 4 MiB；PNG、JPEG、GIF 大图会自动缩小，SVG、WebP、ICO 不超过 512 KiB。</small>
      {file && <p>{file.name} · {(file.size / 1024).toFixed(1)} KiB</p>}
    </div>
    <div className="inline-actions">
      <button type="button" className="secondary" disabled={!!busy || !file} onClick={() => {
        if (!file) return;
        const body = new FormData(); body.append("file", file);
        void saveIcon(body);
      }}><Upload size={14} />{busy === "save" ? "正在处理…" : "上传 Logo"}</button>
      <button type="button" className="secondary" disabled={!!busy} onClick={async () => {
        setBusy("reset"); setError(""); setMessage("");
        try { await api(`/accounts/${account.id}/icon`, "DELETE"); updated(); setMessage("已恢复自动获取；抓取成功后会保存新图标。"); }
        catch (error) { setError((error as Error).message); }
        finally { setBusy(null); }
      }}><RotateCcw size={14} />恢复自动获取</button>
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    {message && <p className="field-hint" role="status">{message}</p>}
    </div>
  </section>;
}
