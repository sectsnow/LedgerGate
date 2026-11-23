import { useId, useState } from "react";
import { Eye, EyeOff, LoaderCircle } from "lucide-react";
import { api } from "./api";
import CopyButton from "./CopyButton";

export default function AccountTokenField({ label, hint, value, onChange, accountId, field, hasSaved, required, disabled, validationError }: {
  label: string;
  hint: string;
  value: string;
  onChange: (value: string) => void;
  accountId?: number;
  field: "token" | "refresh_token";
  hasSaved?: boolean;
  required?: boolean;
  disabled?: boolean;
  validationError?: string;
}) {
  const id = useId();
  const [visible, setVisible] = useState(false);
  const [savedValue, setSavedValue] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  async function readSaved() {
    const result = await api<{ token: string }>(`/accounts/${accountId}/tokens/reveal`, "POST", { field });
    return result.token;
  }
  async function toggle() {
    setError("");
    if (visible) { setVisible(false); setSavedValue(null); return; }
    if (!value && hasSaved && accountId) {
      setLoading(true);
      try {
        setSavedValue(await readSaved());
        setVisible(true);
      } catch (error) { setError((error as Error).message); }
      finally { setLoading(false); }
    } else { setVisible(true); }
  }
  return <div className="field">
    <label htmlFor={id}>{label}</label>
    <div className="account-token-input">
      <input id={id} type={visible ? "text" : "password"} autoComplete="off" spellCheck={false} required={required} disabled={disabled || loading}
        value={savedValue ?? value} aria-invalid={!!validationError} aria-describedby={`${id}-hint${validationError ? ` ${id}-validation` : ""}`} onChange={event => { setSavedValue(null); onChange(event.target.value); }} />
      <button type="button" className="icon-button" disabled={disabled || loading} aria-label={`${visible ? "隐藏" : "显示"}${label}`} aria-pressed={visible} onClick={toggle}>
        {loading ? <LoaderCircle size={16} className="spin" aria-hidden="true" /> : visible ? <EyeOff size={16} aria-hidden="true" /> : <Eye size={16} aria-hidden="true" />}
        {visible ? "隐藏" : "显示"}
      </button>
      <CopyButton label={`复制${label}`} disabled={disabled || loading || (!value && !hasSaved)} text={value || (hasSaved && accountId ? readSaved : "")} />
    </div>
    <small id={`${id}-hint`}>{savedValue !== null ? "正在查看已保存的令牌，直接查看不会修改；编辑后才会替换。" : hint}</small>
    {error && <small role="alert">{error}</small>}
    {validationError && <small id={`${id}-validation`} className="form-error" role="alert">{validationError}</small>}
  </div>;
}
