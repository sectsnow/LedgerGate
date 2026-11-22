import { useEffect, useState } from "react";
import { LoaderCircle, RefreshCw, Search } from "lucide-react";
import { api, type Channel } from "./api";
import { ChannelSelection } from "./ChannelBatch";

type Preview = { models: string[]; saved_models: string[]; revision: string };
type Row = {
  channel: Channel;
  models: string[];
  saved: string[];
  selected: Set<string>;
  revision: string;
  status: "loading" | "ready" | "error" | "saved";
  error: string;
};

export default function ChannelModelSync({ channels, busy, setBusy, close, saved }: {
  channels: Channel[];
  busy: boolean;
  setBusy: (value: boolean) => void;
  close: () => void;
  saved: (count: number) => Promise<void>;
}) {
  const [rows, setRows] = useState<Row[]>([]);
  const [search, setSearch] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [message, setMessage] = useState("");
  useEffect(() => {
    let cancelled = false;
    let next = 0;
    setMessage("");
    setRows(channels.map(channel => ({ channel, models: [], saved: [], selected: new Set(), revision: "", status: "loading", error: "" })));
    async function load() {
      while (!cancelled && next < channels.length) {
        const channel = channels[next++];
        try {
          const preview = await api<Preview>(`/channels/${channel.id}/models/available`);
          const previous = new Set(preview.saved_models);
          if (!cancelled) setRows(current => current.map(row => row.channel.id === channel.id ? {
            ...row, models: preview.models, saved: preview.saved_models, revision: preview.revision, status: "ready",
            selected: new Set(previous.size ? preview.models.filter(model => previous.has(model)) : preview.models),
          } : row));
        } catch (error) {
          if (!cancelled) setRows(current => current.map(row => row.channel.id === channel.id ? { ...row, status: "error", error: (error as Error).message } : row));
        }
      }
    }
    void Promise.all(Array.from({ length: Math.min(4, channels.length) }, load));
    return () => { cancelled = true; };
  }, [channels, attempt]);

  const loading = rows.length === 0 || rows.some(row => row.status === "loading");
  const targets = rows.filter(row => row.status === "ready" && row.selected.size > 0);
  const count = targets.reduce((sum, row) => sum + row.selected.size, 0);
  const query = search.trim().toLowerCase();
  function toggle(id: number, models: string[], checked: boolean) {
    setRows(current => current.map(row => {
      if (row.channel.id !== id) return row;
      const selected = new Set(row.selected);
      for (const model of models) {
        if (checked) selected.add(model);
        else selected.delete(model);
      }
      return { ...row, selected };
    }));
  }

  return (
    <form onSubmit={async event => {
      event.preventDefault();
      if (busy || loading || targets.length === 0) return;
      setBusy(true);
      setMessage("");
      let succeeded = 0;
      let failed = 0;
      try {
        for (const row of targets) {
          try {
            await api(`/channels/${row.channel.id}/models`, "PUT", {
              models: row.models.filter(model => row.selected.has(model)), revision: row.revision,
            });
            succeeded++;
            setRows(current => current.map(item => item.channel.id === row.channel.id ? { ...item, status: "saved", error: "" } : item));
          } catch (error) {
            failed++;
            setRows(current => current.map(item => item.channel.id === row.channel.id ? { ...item, error: (error as Error).message } : item));
          }
        }
        if (succeeded) await saved(succeeded);
        failed += rows.filter(row => row.status === "error").length;
        if (failed === 0) close();
        else setMessage(`已同步 ${succeeded} 条渠道，${failed} 条失败。可重试失败项或重新获取模型列表。`);
      } catch (error) {
        setMessage(`已同步 ${succeeded} 条渠道，页面刷新失败：${(error as Error).message}`);
      } finally {
        setBusy(false);
      }
    }}>
      <p className="field-hint">
        勾选要同步的模型，确认后替换该渠道已保存的模型列表，已有模型路由保留。已有模型默认勾选，首次同步默认全选；未勾选任何模型的渠道会跳过。
      </p>
      <div className="list-toolbar model-sync-toolbar">
        <div className="search"><Search size={16} /><input aria-label="搜索可同步模型" placeholder="搜索模型名称" value={search} disabled={busy} onChange={event => setSearch(event.target.value)} /></div>
        <button className="text-button" type="button" disabled={busy || loading} onClick={() => setRows(current => current.map(row => row.status === "ready" ? { ...row, selected: new Set() } : row))}>清空选择</button>
        <button className="icon-button" type="button" aria-label="重新获取可同步模型" disabled={busy || loading} onClick={() => setAttempt(value => value + 1)}><RefreshCw size={16} /></button>
      </div>
      <div className="model-sync-channels">
        {rows.map(row => {
          const visible = row.models.filter(model => model.toLowerCase().includes(query));
          const selectedCount = visible.filter(model => row.selected.has(model)).length;
          const available = new Set(row.models);
          const unavailable = row.saved.filter(model => !available.has(model)).length;
          return <section className="model-sync-channel" key={row.channel.id} aria-label={`${row.channel.name}可同步模型`}>
            <div className="model-sync-heading"><strong>{row.channel.name}</strong><span>{row.status === "saved" ? "已同步" : `${row.selected.size} / ${row.models.length} 个模型`}</span></div>
            {row.status === "loading" ? <p className="field-hint" role="status"><LoaderCircle className="spin" size={16} /> 正在获取上游模型…</p> : row.status !== "error" && <>
              <label className="checkbox model-sync-select-all">
                <ChannelSelection label={`选择${row.channel.name}的全部可见模型`} selected={selectedCount} total={visible.length} disabled={busy || row.status === "saved"} toggle={checked => toggle(row.channel.id, visible, checked)} />
                {query ? "全选筛选结果" : "全选模型"}<span>{visible.length} 个</span>
              </label>
              <div className="model-sync-list">
                {visible.map(model => <label className="checkbox" key={model}><input type="checkbox" aria-label={`${row.channel.name} / ${model}`} disabled={busy || row.status === "saved"} checked={row.selected.has(model)} onChange={event => toggle(row.channel.id, [model], event.target.checked)} /><code>{model}</code></label>)}
                {visible.length === 0 && <p className="field-hint">{row.models.length ? "没有匹配的模型" : "上游未返回可同步模型，原列表保留。"}</p>}
              </div>
              {unavailable > 0 && <p className="field-hint">原列表中有 {unavailable} 个模型未被上游返回，同步此渠道后将从列表移除。</p>}
            </>}
            {row.error && <p className="form-error" role="alert">{row.error}</p>}
          </section>;
        })}
      </div>
      {message && <p className="form-error" role="alert">{message}</p>}
      <div className="form-footer model-sync-footer">
        <span className="field-hint">已选 {count} 个模型，涉及 {targets.length} 条渠道</span>
        <button className="secondary" type="button" disabled={busy} onClick={close}>取消</button>
        <button className="primary" type="submit" disabled={busy || loading || targets.length === 0}>{busy ? "正在保存…" : "同步所选模型"}</button>
      </div>
    </form>
  );
}
