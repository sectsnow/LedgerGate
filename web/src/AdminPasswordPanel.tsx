import { useEffect, useId, useState } from "react";
import { KeyRound } from "lucide-react";
import { api } from "./api";
import "./admin-password.css";

export function adminPasswordError(password: string, confirmation: string) {
  const bytes = new TextEncoder().encode(password).length;
  if (bytes < 8 || bytes > 72) return "管理员密码长度必须为 8–72 字节";
  if (password !== password.trim()) return "管理员密码首尾不能包含空白字符";
  if (password !== confirmation) return "两次输入的密码不一致";
  return "";
}

function requireLogin(message: string) {
  window.dispatchEvent(new CustomEvent("session-expired", { detail: message }));
}

export default function AdminPasswordPanel() {
  const id = useId();
  const [status, setStatus] = useState<{ environment_managed: boolean } | null>(null);
  const [current, setCurrent] = useState(""), [password, setPassword] = useState(""), [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    api<{ environment_managed: boolean }>("/admin/password", "GET", undefined, controller.signal)
      .then(setStatus).catch(error => { if (!controller.signal.aborted) setError(error.message); });
    return () => controller.abort();
  }, []);
  return <section className="panel admin-password-panel">
    <div className="panel-heading"><div><h2>更改管理员密码</h2><p>密码为 8–72 字节，区分大小写。更改后所有管理员登录失效，需要使用新密码重新登录。</p></div><KeyRound size={19} aria-hidden="true" /></div>
    {status?.environment_managed ? <p className="admin-password-managed">管理员密码由启动环境变量 LEDGERGATE_ADMIN_PASSWORD 管理，请在部署配置中更改；移除该变量并重启后可在这里修改。</p> : <form className="admin-password-form" onSubmit={async event => {
      event.preventDefault(); setError("");
      const invalid = adminPasswordError(password, confirmation);
      if (invalid) { setError(invalid); return; }
      setBusy(true);
      try {
        const response = await fetch("/api/admin/password", { method: "PUT", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-LedgerGate-Request": "1" }, body: JSON.stringify({ current_password: current, new_password: password, confirm_password: confirmation }) });
        const result = await response.json().catch(() => ({}));
        if (response.status === 401 || result.reauthenticate) { requireLogin(result.error?.message || "登录已失效，请重新登录"); return; }
        if (!response.ok) throw new Error(result.error?.message || "更改密码失败");
        setCurrent(""); setPassword(""); setConfirmation("");
        requireLogin("管理员密码已更改，请使用新密码登录。");
      } catch (error) { setError((error as Error).message); } finally { setBusy(false); }
    }}>
      <div className="admin-password-fields">{[
        { key: "current", label: "当前管理员密码", value: current, change: setCurrent, autocomplete: "current-password" },
        { key: "new", label: "新管理员密码", value: password, change: setPassword, autocomplete: "new-password" },
        { key: "confirmation", label: "确认新管理员密码", value: confirmation, change: setConfirmation, autocomplete: "new-password" },
      ].map(field => <div className="field" key={field.key}><label htmlFor={`${id}-${field.key}`}>{field.label}</label><input id={`${id}-${field.key}`} type="password" autoComplete={field.autocomplete} required disabled={busy || !status} value={field.value} onChange={event => field.change(event.target.value)} /></div>)}</div>
      <p className="admin-password-hint">英文、数字通常每个字符占 1 字节，中文通常占 3 字节；首尾不能有空白字符。</p>
      <div className="admin-password-actions"><button type="submit" className="primary" disabled={busy || !status}>{busy ? "正在更改…" : "更改密码并重新登录"}</button></div>
    </form>}
    {error && <p className="admin-password-error form-error" role="alert">{error}</p>}
  </section>;
}
