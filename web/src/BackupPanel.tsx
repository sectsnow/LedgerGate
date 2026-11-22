import { useId, useState } from "react";

export default function BackupPanel() {
  const id = useId();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <section className="panel backup-panel"><div className="panel-heading"><div><h2>备份与恢复</h2><p>下载加密备份，包含一致性数据库快照、解密主密钥与管理员密码。</p></div></div>
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
      <div className="field"><label htmlFor={id}>备份密码</label><input id={id} type="password" autoComplete="new-password" required minLength={12} maxLength={256} value={password} onChange={event => setPassword(event.target.value)} /><small>至少 12 字节。恢复时需要此密码，请自行保存。</small></div>
      <button className="secondary" disabled={busy}>{busy ? "正在创建…" : "下载加密备份"}</button>
      {error && <p className="form-error" role="alert">{error}</p>}
    </form>
    <details className="backup-help"><summary>恢复备份</summary><p>在服务器将备份密码保存到仅自己可读的文件，再执行下面命令。恢复只写入尚不存在的新目录，校验通过后再停止服务并切换数据目录。</p><pre><code>{'./bin/ledgergate restore --file "/path/backup.lgb" --data-dir "/path/restored-data" --password-file "/path/password.txt"'}</code></pre></details>
  </section>;
}
