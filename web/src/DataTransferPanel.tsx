import { useEffect, useId, useState } from "react";
import { ArrowLeftRight, Download, FileSearch, Upload } from "lucide-react";
import { api } from "./api";
import "./data-transfer.css";

type Section = "accounts" | "channels" | "models" | "keys" | "settings";
type Count = { id: Section; label: string; count: number; created: number; skipped: number; updated: number };
type Preview = { exported_at: string; include_credentials: boolean; sections: Count[]; selected: Section[]; warnings: string[]; token?: string };
const labels: Record<Section, string> = { accounts: "上游账户", channels: "转发渠道", models: "模型与路由", keys: "网关密钥", settings: "系统设置" };
const descriptions: Record<Section, string> = {
  accounts: "站点、认证方式、采集间隔和阈值",
  channels: "渠道协议、已选模型和关联账户",
  models: "统一模型、调度、路由和计价配置",
  keys: "网关密钥、允许模型、预算及累计费用",
  settings: "通知、运行参数和保留策略；导入将替换当前设置",
};
const sections = Object.keys(labels) as Section[];

function effectiveSections(selected: Section[]) {
  const result = new Set(selected);
  if (result.has("models")) result.add("channels");
  if (result.has("channels") || result.has("settings")) result.add("accounts");
  return sections.filter(section => result.has(section));
}

function Categories({ rows, selected, change, disabled }: { rows: Count[]; selected: Section[]; change: (sections: Section[]) => void; disabled: boolean }) {
  const effective = effectiveSections(selected);
  return <div className="transfer-categories">{rows.map(row => {
    const required = row.id === "accounts" && (effective.includes("channels") || effective.includes("settings")) || row.id === "channels" && effective.includes("models");
    return <label className={`transfer-category ${effective.includes(row.id) ? "chosen" : ""}`} key={row.id}>
      <input type="checkbox" aria-label={row.label} checked={effective.includes(row.id)} disabled={disabled || required} onChange={event => change(event.target.checked ? [...selected, row.id] : selected.filter(id => id !== row.id))} />
      <span><strong>{row.label}</strong><small>{descriptions[row.id]}</small></span><span className="transfer-count">{required ? "关联必需" : `${row.count} 项`}</span>
    </label>;
  })}</div>;
}

export default function DataTransferPanel({ changed }: { changed: (settingsImported: boolean) => Promise<void> }) {
  const id = useId();
  const [tab, setTab] = useState<"export" | "import">("export");
  const [catalog, setCatalog] = useState<Count[]>([]);
  const [selected, setSelected] = useState<Section[]>(["accounts", "channels", "models"]);
  const [includeCredentials, setIncludeCredentials] = useState(true);
  const [exportPassword, setExportPassword] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [importPassword, setImportPassword] = useState("");
  const [importSelected, setImportSelected] = useState<Section[] | null>(null);
  const [manifest, setManifest] = useState<Preview | null>(null);
  const [plan, setPlan] = useState<Preview | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [message, setMessage] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    api<{ sections: Count[] }>("/data-transfer", "GET", undefined, controller.signal).then(result => setCatalog(result.sections)).catch(error => { if (!controller.signal.aborted) setError(error.message); });
    return () => controller.abort();
  }, []);
  const clearPreview = () => { setManifest(null); setPlan(null); setImportSelected(null); setMessage(""); };
  function chooseFile(next: File | null) {
    clearPreview(); setError(""); setFile(null);
    if (next && (next.size > 16 * 1024 * 1024 + 48 || !next.name.toLowerCase().endsWith(".lgd"))) { setError("请选择不超过 16 MiB 的 .lgd 数据包。"); return; }
    setFile(next);
  }
  async function readFile(path: "preview" | "import") {
    if (!file) throw new Error("请选择数据包。");
    const body = new FormData(); body.append("file", file); body.append("password", importPassword);
    if (importSelected !== null) body.append("sections", JSON.stringify(effectiveSections(importSelected)));
    if (path === "import" && plan?.token) body.append("token", plan.token);
    const response = await fetch(`/api/data-transfer/${path}`, { method: "POST", credentials: "same-origin", headers: { "X-LedgerGate-Request": "1" }, body });
    if (response.status === 401) window.dispatchEvent(new Event("session-expired"));
    const result = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(result.error?.message || "读取或导入数据包失败");
    return result as Preview;
  }
  return <section className="panel data-transfer-panel">
    <div className="panel-heading"><div><h2 className="icon-heading"><ArrowLeftRight size={18} aria-hidden="true" />数据导入与导出</h2><p>按需迁移配置。消费、请求记录和监控历史通过完整备份恢复。</p></div></div>
    <div className="transfer-tabs" role="group" aria-label="数据迁移方式">{([["export", "导出数据", Download], ["import", "导入数据", Upload]] as const).map(([value, title, Icon]) => <button type="button" className="secondary" key={value} aria-pressed={tab === value} disabled={busy} onClick={() => { setTab(value); setError(""); setMessage(""); }}><Icon size={15} />{title}</button>)}</div>
    {tab === "export" ? <form className="transfer-form" onSubmit={async event => {
      event.preventDefault(); setBusy(true); setError(""); setMessage("");
      try {
        const response = await fetch("/api/data-transfer/export", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-LedgerGate-Request": "1" }, body: JSON.stringify({ sections: selected, password: exportPassword, include_credentials: includeCredentials }) });
        if (response.status === 401) window.dispatchEvent(new Event("session-expired"));
        if (!response.ok) { const result = await response.json().catch(() => ({})); throw new Error(result.error?.message || "导出失败"); }
        const blob = await response.blob(), url = URL.createObjectURL(blob), link = document.createElement("a");
        link.href = url; link.download = response.headers.get("Content-Disposition")?.match(/filename="([^"]+)"/)?.[1] || "ledgergate-data.lgd";
        document.body.appendChild(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
        setExportPassword(""); setMessage("已导出所选配置。恢复时需要此密码，请与文件分开保存。");
      } catch (error) { setError((error as Error).message); } finally { setBusy(false); }
    }}>
      <fieldset className="transfer-fieldset"><legend>选择导出内容</legend><Categories rows={catalog} selected={selected} disabled={busy} change={setSelected} /></fieldset>
      <p className="transfer-hint">导出模型会带上关联渠道，渠道会带上关联账户；系统设置也会带上通知筛选引用的账户。仅自动补充实际引用的记录。</p>
      <label className="checkbox"><input type="checkbox" checked={includeCredentials} disabled={busy} onChange={event => setIncludeCredentials(event.target.checked)} />包含访问凭据（账户密码、Token、渠道及网关 Key、通知密钥）</label>
      <div className="transfer-actions"><div className="field"><label htmlFor={`${id}-export-password`}>导出密码</label><input id={`${id}-export-password`} type="password" autoComplete="new-password" required minLength={12} maxLength={256} value={exportPassword} disabled={busy} onChange={event => setExportPassword(event.target.value)} /><small>12–256 字节，用于加密 .lgd 配置包，不包含管理员密码或主密钥。</small></div><button type="submit" className="primary" disabled={busy || selected.length === 0 || catalog.length === 0}><Download size={15} />{busy ? "正在导出…" : "导出所选内容"}</button></div>
    </form> : <form className="transfer-form" onSubmit={async event => {
      event.preventDefault(); setBusy(true); setError(""); setMessage(""); setPlan(null);
      try {
        const result = await readFile("preview"); setManifest(result); setPlan(result);
        if (importSelected === null) setImportSelected(result.selected);
      } catch (error) { setError((error as Error).message); } finally { setBusy(false); }
    }}>
      <div className="transfer-dropzone" onDragOver={event => event.preventDefault()} onDrop={event => { event.preventDefault(); if (!busy) chooseFile(event.dataTransfer.files[0] || null); }}><label htmlFor={`${id}-file`}>选择或拖入 .lgd 配置包</label><input id={`${id}-file`} type="file" accept=".lgd" disabled={busy} onChange={event => chooseFile(event.target.files?.[0] || null)} />{file && <p>{file.name} · {(file.size / 1024).toFixed(1)} KiB</p>}</div>
      <div className="field"><label htmlFor={`${id}-import-password`}>数据包密码</label><input id={`${id}-import-password`} type="password" autoComplete="off" required minLength={12} maxLength={256} disabled={busy} value={importPassword} onChange={event => { setImportPassword(event.target.value); clearPreview(); }} /></div>
      {manifest && <><p className="transfer-hint">导出于 {new Date(manifest.exported_at).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", hour12: false })} · {manifest.include_credentials ? "包含访问凭据" : "不含访问凭据"}</p><fieldset className="transfer-fieldset"><legend>选择导入内容</legend><Categories rows={manifest.sections} selected={importSelected || []} disabled={busy} change={value => { setImportSelected(value); setPlan(null); }} /></fieldset></>}
      {plan && <div className="transfer-preview"><h3>导入预览</h3><div className="table-scroll"><table><thead><tr><th>类别</th><th>新增</th><th>跳过</th><th>替换</th></tr></thead><tbody>{plan.sections.filter(row => plan.selected.includes(row.id)).map(row => <tr key={row.id}><td>{row.label}</td><td>{row.created}</td><td>{row.skipped}</td><td>{row.updated}</td></tr>)}</tbody></table></div>{plan.warnings.map(warning => <p className="transfer-hint" key={warning}>{warning}</p>)}</div>}
      <div className="transfer-import-actions"><button type="submit" className="secondary" disabled={busy || !file || importSelected !== null && importSelected.length === 0}><FileSearch size={15} aria-hidden="true" />{busy ? "正在处理…" : manifest ? "更新导入预览" : "预览导入"}</button><button type="button" className="primary" disabled={busy || !plan?.token} onClick={async () => {
        setBusy(true); setError(""); setMessage("");
        try {
          const result = await readFile("import"); setPlan(null); setImportPassword("");
          const totals = result.sections.reduce((sum, row) => ({ created: sum.created + row.created, skipped: sum.skipped + row.skipped, updated: sum.updated + row.updated }), { created: 0, skipped: 0, updated: 0 });
          setMessage(`导入完成：新增 ${totals.created} 项，跳过 ${totals.skipped} 项，替换 ${totals.updated} 项。新账户、渠道和网关密钥处于停用状态。`);
          try { await changed(result.selected.includes("settings")); } catch { setError("数据已导入，页面刷新失败，请手动刷新。"); }
          const catalog = await api<{ sections: Count[] }>("/data-transfer"); setCatalog(catalog.sections);
        } catch (error) { setError((error as Error).message); setPlan(null); } finally { setBusy(false); }
      }}><Upload size={15} />确认导入</button></div>
    </form>}
    {error && <p className="transfer-feedback form-error" role="alert">{error}</p>}{message && <p className="transfer-feedback" role="status">{message}</p>}
  </section>;
}
