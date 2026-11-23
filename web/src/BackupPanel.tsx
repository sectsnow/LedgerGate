import { useId, useState, type ComponentType, type ReactNode } from "react";
import { Archive, ArchiveRestore, Download, LoaderCircle, Upload } from "lucide-react";
import "./data-transfer.css";

type DialogComponent = ComponentType<{ title: string; children: ReactNode; close: () => void }>;

export default function BackupPanel({ dialog: Dialog }: { dialog: DialogComponent }) {
  const id = useId();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [restorePassword, setRestorePassword] = useState("");
  const [restoreError, setRestoreError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [restoring, setRestoring] = useState(false);
  function chooseFile(next: File | null) {
    setRestoreError(""); setFile(null);
    if (next && (!next.name.toLowerCase().endsWith(".lgb") || next.size > 4 * 1024 ** 3 + 1024 ** 2)) {
      setRestoreError("请选择不超过 4 GiB 的 .lgb 备份文件。"); return;
    }
    setFile(next);
  }
  async function restore() {
    if (!file) return;
    setRestoring(true); setRestoreError("");
    try {
      const body = new FormData(); body.append("file", file); body.append("password", restorePassword); body.append("confirm", "恢复");
      const response = await fetch("/api/backup/restore", { method: "POST", credentials: "same-origin", headers: { "X-LedgerGate-Request": "1" }, body });
      const result = await response.json().catch(() => ({}));
      if (response.status === 401) { window.dispatchEvent(new Event("session-expired")); return; }
      if (!response.ok) throw new Error(result.error?.message || "恢复失败");
      setRestorePassword("");
      window.dispatchEvent(new CustomEvent("session-expired", { detail: result.environment_managed ? "备份已恢复，请使用部署配置指定的管理员密码登录。" : "备份已恢复，请使用备份中的管理员密码登录。" }));
    } catch (error) { setRestoreError((error as Error).message); }
    finally { setRestoring(false); setConfirming(false); }
  }
  return <section className="panel backup-panel"><div className="panel-heading"><div><h2 className="icon-heading"><Archive size={18} aria-hidden="true" />备份与恢复</h2><p>下载加密备份，包含配置、用量统计、解密主密钥与管理员密码。内存中的近期请求及正文不随备份保存。</p></div></div>
    <form className="backup-controls" onSubmit={async event => {
      event.preventDefault(); setBusy(true); setError("");
      try {
        const response = await fetch("/api/backup", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-LedgerGate-Request": "1" }, body: JSON.stringify({ password }) });
        if (!response.ok) { const result = await response.json().catch(() => ({})); throw new Error(result.error?.message || "备份失败"); }
        const blob = await response.blob();
        const url = URL.createObjectURL(blob), link = document.createElement("a");
        link.href = url; link.download = response.headers.get("Content-Disposition")?.match(/filename="([^"]+)"/)?.[1] || "ledgergate.lgb";
        document.body.appendChild(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); setPassword("");
      } catch (error) { setError((error as Error).message); }
      finally { setBusy(false); }
    }}>
      <div className="field"><label htmlFor={id}>备份密码</label><input id={id} type="password" autoComplete="new-password" required minLength={12} maxLength={256} disabled={busy || restoring} value={password} onChange={event => setPassword(event.target.value)} /><small>至少 12 字节。恢复时需要此密码，请自行保存。</small></div>
      <button className="secondary" disabled={busy || restoring}>{busy ? <LoaderCircle size={15} className="spin" aria-hidden="true" /> : <Download size={15} aria-hidden="true" />}{busy ? "正在创建…" : "下载加密备份"}</button>
      {error && <p className="form-error" role="alert">{error}</p>}
    </form>
    <details className="backup-help"><summary><span className="icon-label"><ArchiveRestore size={15} aria-hidden="true" />恢复备份</span></summary>
      <form className="backup-restore" onSubmit={event => {
        event.preventDefault(); setRestoreError("");
        const bytes = new TextEncoder().encode(restorePassword).length;
        if (bytes < 12 || bytes > 256) { setRestoreError("备份密码须为 12–256 字节。"); return; }
        setConfirming(true);
      }}>
        <p>选择或拖入不超过 4 GiB 的 .lgb 完整备份，输入创建备份时的密码。校验通过后替换当前数据，恢复前会保留原数据，成功后需要重新登录。旧版备份中的请求明细会先归档，再转换为用量统计。</p>
        <div className="transfer-dropzone" onDragOver={event => event.preventDefault()} onDrop={event => { event.preventDefault(); if (!busy && !restoring) chooseFile(event.dataTransfer.files[0] || null); }}>
          <label htmlFor={`${id}-file`}>选择或拖入 .lgb 备份文件</label><input id={`${id}-file`} type="file" accept=".lgb" disabled={busy || restoring} onChange={event => chooseFile(event.target.files?.[0] || null)} />
          {file && <p>{file.name} · {(file.size / 1024 / 1024).toFixed(2)} MiB</p>}
        </div>
        <div className="field"><label htmlFor={`${id}-restore-password`}>该备份的加密密码</label><input id={`${id}-restore-password`} type="password" autoComplete="off" required maxLength={256} disabled={busy || restoring} value={restorePassword} onChange={event => setRestorePassword(event.target.value)} /></div>
        <button className="secondary" type="submit" disabled={busy || restoring || !file}>{restoring ? <LoaderCircle size={15} className="spin" aria-hidden="true" /> : <Upload size={15} aria-hidden="true" />}{restoring ? "正在恢复…" : "上传并恢复"}</button>
        {restoreError && <p className="form-error" role="alert">{restoreError}</p>}
      </form>
    </details>
    {confirming && <Dialog title="确认恢复完整备份" close={() => { if (!restoring) setConfirming(false); }}>
      <p>将使用“{file?.name}”替换当前全部账户、渠道、模型、密钥、设置和用量统计，并清空内存中的近期请求。恢复前的原数据会自动保留。</p>
      <p className="field-hint">恢复会等待正在进行的请求和后台任务完成，期间暂停处理新请求。恢复成功后需要重新登录。旧版备份中的请求明细会先归档，再转换为用量统计。</p>
      <div className="form-footer"><button className="secondary" type="button" disabled={restoring} onClick={() => setConfirming(false)}>取消</button><button className="destructive" type="button" disabled={restoring} onClick={() => void restore()}>{restoring ? "正在校验并恢复…" : "确认替换并恢复"}</button></div>
    </Dialog>}
  </section>;
}
