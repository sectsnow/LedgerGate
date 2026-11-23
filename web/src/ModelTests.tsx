import { useEffect, useRef, useState } from "react";
import { LoaderCircle, Play, Search, Square, Trash2 } from "lucide-react";
import { api, protocolNames, type AccountRow, type ChannelRow, type Group } from "./api";
import { ChannelSelection } from "./ChannelBatch";
import { channelModelNames } from "./ChannelModelEditor";
import { averageOutputTPS } from "./requestTiming";
import "./model-tests.css";

type Inventory = { models: string[]; revision: string; removed_routes?: number };
type Result = {
  success: boolean; status: number; duration_ms: number; first_token_ms: number | null;
  first_content_ms: number | null;
  generation_ms: number | null; input_tokens: number | null; output_tokens: number | null;
  cache_read_tokens: number | null; cache_write_tokens: number | null;
  usage_warning: string; text: string; error: string;
};
type Row = { state: "waiting" | "running" | "passed" | "failed" | "cancelled"; result?: Result; error?: string };
const stateNames = { waiting: "待测试", running: "测试中", passed: "通过", failed: "失败", cancelled: "已取消" };
const duration = (ms: number | null | undefined) => ms == null ? "—" : `${(ms / 1000).toFixed(2)}s`;
const count = (value: number | null | undefined) => value == null ? "—" : value.toLocaleString("zh-CN");
const protocolStorageKey = "ledgergate-model-test-protocol";

export default function ModelTests({ channels, accounts, groups, changed }: {
  channels: ChannelRow[]; accounts: AccountRow[]; groups: Group[]; changed: () => Promise<void>;
}) {
  const [accountID, setAccountID] = useState("");
  const [selectedChannelID, setChannelID] = useState(channels[0]?.channel.id || 0);
  const [keyChoice, setKeyChoice] = useState("saved");
  const [key, setKey] = useState("");
  const [protocol, setProtocol] = useState(() => {
    try {
      const saved = localStorage.getItem(protocolStorageKey);
      if (saved && Object.hasOwn(protocolNames, saved)) return saved;
    } catch { /* 浏览器禁用存储时使用渠道默认协议。 */ }
    return channels[0]?.channel.protocols?.[0] || channels[0]?.channel.protocol || "openai";
  });
  const [prompt, setPrompt] = useState("请只回复 OK。");
  const [stream, setStream] = useState(true);
  const [concurrency, setConcurrency] = useState(5);
  const [maxTokens, setMaxTokens] = useState(512);
  const [limitField, setLimitField] = useState("max_completion_tokens");
  const [inventory, setInventory] = useState<Inventory>({ models: [], revision: "" });
  const [models, setModels] = useState<string[]>([]);
  const [selected, setSelected] = useState(new Set<string>());
  const [rows, setRows] = useState<Record<string, Row>>({});
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [busy, setBusy] = useState<"loading" | "testing" | "removing" | null>("loading");
  const [message, setMessage] = useState("");
  const action = useRef<AbortController | null>(null);
  const filteredChannels = channels.filter(row => !accountID || (accountID === "direct" ? row.channel.account_id == null : row.channel.account_id === Number(accountID)));
  const channel = (filteredChannels.find(row => row.channel.id === selectedChannelID) || filteredChannels[0])?.channel;
  const channelID = channel?.id || 0;
  const channelExists = channel?.id;
  useEffect(() => {
    const controller = new AbortController();
    action.current?.abort(); action.current = controller;
    setChannelID(channelExists || 0); setKeyChoice("saved"); setKey(""); setFilter("all");
    setRows({}); setModels([]); setSelected(new Set()); setMessage("");
    setInventory({ models: [], revision: "" }); setBusy("loading");
    if (channelExists) {
      api<Inventory>(`/channels/${channelExists}/model-tests/models`, "GET", undefined, controller.signal)
        .then(value => { if (!controller.signal.aborted) { setInventory(value); setModels(value.models); setSelected(new Set(value.models)); } })
        .catch(error => { if (!controller.signal.aborted) setMessage(error.message); })
        .finally(() => { if (!controller.signal.aborted) setBusy(null); });
    } else setBusy(null);
    return () => controller.abort();
  }, [channelExists]);
  useEffect(() => () => action.current?.abort(), []);

  function resetResults() { setRows({}); setMessage(""); }
  function keyInput() {
    return keyChoice === "custom" ? { api_key: key.trim(), protocol }
      : { key_channel_id: keyChoice === "saved" ? 0 : Number(keyChoice), protocol };
  }
  function changeKey(value: string) {
    setKeyChoice(value); setKey(""); resetResults();
    setModels(inventory.models); setSelected(new Set(inventory.models));
  }
  const validKey = keyChoice !== "custom" || !!key.trim();
  const visible = models.filter(model => model.toLowerCase().includes(search.trim().toLowerCase()) && (filter === "all" || rows[model]?.state === filter));
  const failed = models.filter(model => rows[model]?.state === "failed");
  const passed = models.filter(model => rows[model]?.state === "passed").length;
  const ready = !!channel && !busy && validKey;
  const validTest = !!prompt.trim() && Number.isInteger(concurrency) && concurrency >= 1 && concurrency <= 10 && Number.isInteger(maxTokens) && maxTokens >= 1 && maxTokens <= 8192;
  function toggle(names: string[], checked: boolean) {
    setSelected(previous => {
      const next = new Set(previous);
      for (const name of names) { if (checked) next.add(name); else next.delete(name); }
      return next;
    });
  }
  async function getModels() {
    if (!ready) return;
    const controller = new AbortController(); action.current = controller;
    setBusy("loading"); setMessage("");
    try {
      const value = await api<Inventory>(`/channels/${channelID}/model-tests/models`, "POST", keyInput(), controller.signal);
      setInventory(value); setModels(value.models); setSelected(new Set(value.models)); setRows({}); setFilter("all");
      setMessage(`已获取 ${value.models.length} 个模型，仅更新测试列表。`);
    } catch (error) { if (!controller.signal.aborted) setMessage((error as Error).message); }
    finally { if (action.current === controller) setBusy(null); }
  }
  async function test(names: string[]) {
    if (!ready || !names.length || !validTest) return;
    const controller = new AbortController(); action.current = controller;
    setBusy("testing"); setMessage("");
    setRows(previous => ({ ...previous, ...Object.fromEntries(names.map(name => [name, { state: "waiting" }])) }));
    let next = 0;
    async function worker() {
      while (!controller.signal.aborted && next < names.length) {
        const name = names[next++];
        setRows(previous => ({ ...previous, [name]: { state: "running" } }));
        try {
          const result = await api<Result>(`/channels/${channelID}/model-tests`, "POST", {
            ...keyInput(), model: name, prompt, stream, max_tokens: maxTokens, limit_field: limitField,
          }, controller.signal);
          if (!controller.signal.aborted) setRows(previous => ({ ...previous, [name]: { state: result.success ? "passed" : "failed", result } }));
        } catch (error) {
          if (!controller.signal.aborted) setRows(previous => ({ ...previous, [name]: { state: "failed", error: (error as Error).message } }));
        }
      }
    }
    try { await Promise.all(Array.from({ length: Math.min(concurrency, names.length) }, worker)); }
    finally {
      if (action.current === controller) {
        setRows(previous => Object.fromEntries(Object.entries(previous).map(([name, row]) => [name,
          names.includes(name) && ["waiting", "running"].includes(row.state) ? { state: "cancelled" } : row])));
        setBusy(null);
        setMessage(controller.signal.aborted ? "已停止测试。已经发送的请求仍可能产生上游费用。" : "测试完成，可勾选失败项重试或移除。");
      }
    }
  }
  async function remove(names: string[]) {
    if (busy || !channel || !names.length || !inventory.revision) return;
    const affected = groups.flatMap(group => group.routes.filter(route => route.channel_id === channelID && names.includes(route.model)));
    if (!confirm(`从渠道“${channel.name}”移除 ${names.length} 个模型和 ${affected.length} 条对应路由？\n${names.slice(0, 5).join("、")}${names.length > 5 ? "…" : ""}\n若移除当前手动路由，需要重新选择路由才能转发。其他渠道和上游站点的模型不受影响。`)) return;
    const controller = new AbortController(); action.current = controller;
    setBusy("removing"); setMessage("");
    try {
      const value = await api<Inventory>(`/channels/${channelID}/models/remove`, "POST", { models: names, revision: inventory.revision }, controller.signal);
      setInventory(value); setModels(previous => previous.filter(name => !names.includes(name)));
      setSelected(previous => new Set([...previous].filter(name => !names.includes(name))));
      setRows(previous => Object.fromEntries(Object.entries(previous).filter(([name]) => !names.includes(name))));
      setMessage(`已从当前渠道移除 ${names.length} 个模型和 ${value.removed_routes || 0} 条路由。`);
      try { await changed(); } catch { setMessage("移除已完成，其他页面刷新失败，请点击刷新。"); }
    } catch (error) { if (!controller.signal.aborted) setMessage((error as Error).message); }
    finally { if (action.current === controller) setBusy(null); }
  }
  if (!channels.length) return <div className="panel empty"><p>请先添加转发渠道，再测试对应模型。</p></div>;
  return <div className="model-tests">
    <section className="panel model-test-controls" aria-label="模型测试设置">
      <div className="model-test-fields">
        <label className="field">上游账户<select aria-label="测试上游账户" value={accountID} disabled={!!busy} onChange={event => setAccountID(event.target.value)}>
          <option value="">全部账户</option>
          {accounts.map(row => <option key={row.account.id} value={row.account.id}>{row.account.name}</option>)}
          {channels.some(row => row.channel.account_id == null) && <option value="direct">直连供应商</option>}
        </select></label>
        <label className="field">渠道<select aria-label="测试渠道" value={channelID} disabled={!!busy || !filteredChannels.length} onChange={event => setChannelID(Number(event.target.value))}>
        {!filteredChannels.length && <option value={0}>该账户暂无渠道</option>}
        {filteredChannels.map(({ channel }) => {
          const modelCount = new Set([
            ...channelModelNames(channel.models),
            ...groups.flatMap(group => group.routes.filter(route => route.channel_id === channel.id).map(route => route.model)),
          ]).size;
          return <option key={channel.id} value={channel.id}>
            {accounts.find(row => row.account.id === channel.account_id)?.account.name || "直连"} / {channel.name}（{modelCount} 个模型）{channel.enabled ? "" : "（已停用）"}
          </option>;
        })}</select></label>
        <label className="field">API Key<select aria-label="API Key" value={keyChoice} disabled={!!busy || !channel} onChange={event => changeKey(event.target.value)}>
          <option value="saved">当前渠道已保存 Key{channel?.upstream_key_id ? ` #${channel.upstream_key_id}` : ""}</option>
          {channels.filter(row => row.has_key && row.channel.id !== channelID && row.channel.base_url === channel?.base_url).map(row => <option key={row.channel.id} value={row.channel.id}>{row.channel.name} 的 Key</option>)}
          <option value="custom">临时输入 Key</option>
        </select></label>
        <label className="field">请求协议<select aria-label="请求协议" value={protocol} disabled={!!busy} onChange={event => {
          const value = event.target.value;
          setProtocol(value); resetResults();
          try { localStorage.setItem(protocolStorageKey, value); } catch { /* 存储不可用时仍保留当前页面的选择。 */ }
        }}>
          {Object.entries(protocolNames).map(([id, name]) => <option key={id} value={id}>{name}</option>)}
        </select></label>
        <label className="field model-test-prompt">测试内容<input value={prompt} maxLength={4096} disabled={!!busy} onChange={event => { setPrompt(event.target.value); resetResults(); }} /></label>
        {keyChoice === "custom" && <label className="field model-test-secret">临时 API Key<input type="password" autoComplete="off" placeholder="仅用于本次测试，不保存" value={key} maxLength={8192} disabled={!!busy} onChange={event => { setKey(event.target.value); resetResults(); }} /></label>}
      </div>
      <div className="model-test-actions">
        <label className="checkbox"><input type="checkbox" checked={stream} disabled={!!busy} onChange={event => { setStream(event.target.checked); resetResults(); }} />流式</label>
        <label className="model-test-number">并发<input type="number" min={1} max={10} value={concurrency} disabled={!!busy} onChange={event => setConcurrency(Number(event.target.value))} /></label>
        <label className="model-test-number">输出上限<input type="number" min={1} max={8192} value={maxTokens} disabled={!!busy} onChange={event => { setMaxTokens(Number(event.target.value)); resetResults(); }} /><span>Token</span></label>
        <button className="secondary" disabled={!ready} onClick={() => void getModels()}>获取模型</button>
        <button className="primary" disabled={!ready || !selected.size || !validTest} onClick={() => void test(models.filter(name => selected.has(name)))}><Play size={15} />开始测试（{selected.size}）</button>
        {busy === "testing" && <button className="secondary" onClick={() => action.current?.abort()}><Square size={14} />停止测试</button>}
      </div>
      {protocol === "openai" && <details className="model-test-compatibility"><summary>兼容设置</summary>
        <label className="model-test-number">输出上限参数<select aria-label="输出上限参数" value={limitField} disabled={!!busy} onChange={event => { setLimitField(event.target.value); resetResults(); }}>
          <option value="max_completion_tokens">max_completion_tokens（新版 OpenAI）</option><option value="max_tokens">max_tokens（传统兼容接口）</option>
        </select></label>
      </details>}
      <p className="field-hint">按所选协议直接测试指定渠道和 Key，仅支持文本模型。请求协议记住本浏览器上次选择。测试会产生上游费用；失败后由你决定是否移除。结果暂存于当前页面。</p>
    </section>
    {message && <p className="notice" role="status">{message}</p>}
    <section className="panel" aria-label="模型测试结果">
      <div className="list-toolbar model-test-toolbar">
        <div className="search"><Search size={15} /><input aria-label="搜索测试模型" placeholder="搜索模型" value={search} onChange={event => setSearch(event.target.value)} /></div>
        <select aria-label="筛选测试结果" value={filter} onChange={event => setFilter(event.target.value)}><option value="all">全部（{models.length}）</option><option value="passed">通过（{passed}）</option><option value="failed">失败（{failed.length}）</option></select>
        <button className="secondary" disabled={!!busy || !failed.length} onClick={() => setSelected(new Set(failed))}>勾选失败项</button>
        <button className="secondary danger" disabled={!!busy || !selected.size || !inventory.revision} onClick={() => void remove(models.filter(name => selected.has(name)))}><Trash2 size={14} />移除所选（{selected.size}）</button>
        <span className="field-hint" role="status">{busy === "loading" ? "正在获取模型…" : `通过 ${passed} · 失败 ${failed.length} · 已选 ${selected.size}`}</span>
      </div>
      <div className="table-scroll model-test-table"><table>
        <thead><tr><th><ChannelSelection label="选择全部可见测试模型" selected={visible.filter(name => selected.has(name)).length} total={visible.length} disabled={!!busy} toggle={checked => toggle(visible, checked)} /></th><th>模型</th><th>状态</th><th title="Responses 首个语义事件，包含思考项目开始；初始化和心跳不计入。">首字</th><th title="首段正文、公开思考文本或工具内容。">首内容</th><th>总耗时</th><th>输入</th><th>输出</th><th title="输出总 Token ÷ 完整测试耗时，包含思考与等待。">平均 TPS</th><th>缓存读 / 写</th><th>响应 / 失败原因</th><th>操作</th></tr></thead>
        <tbody>{visible.map(name => {
          const row = rows[name]; const result = row?.result; const text = row?.error || result?.error || result?.text;
          const speed = averageOutputTPS(result?.output_tokens, result?.duration_ms)?.toFixed(1) ?? "—";
          return <tr key={name}><td><input type="checkbox" aria-label={`选择测试模型 ${name}`} checked={selected.has(name)} disabled={!!busy} onChange={event => toggle([name], event.target.checked)} /></td><td><code>{name}</code></td>
            <td><span className={`request-state ${row?.state === "passed" ? "success" : row?.state === "failed" ? "error" : row?.state === "running" ? "active" : ""}`}>{row?.state === "running" && <LoaderCircle size={13} className="spin" />}{row ? stateNames[row.state] : "未测试"}{result?.status ? ` · ${result.status}` : ""}</span></td>
            <td>{duration(result?.first_token_ms)}</td><td>{duration(result?.first_content_ms)}</td><td>{duration(result?.duration_ms)}</td><td>{count(result?.input_tokens)}</td><td>{count(result?.output_tokens)}</td><td>{speed}</td><td>{count(result?.cache_read_tokens)} / {count(result?.cache_write_tokens)}</td>
            <td className="model-test-response">{text ? <details><summary className={row?.state === "failed" ? "form-error" : ""}>{text.slice(0, 80)}{text.length > 80 ? "…" : ""}</summary><pre>{text}</pre></details> : "—"}{result?.usage_warning && <small>{result.usage_warning}</small>}</td>
            <td><div className="inline-actions"><button className="text-button" disabled={!ready || !validTest} onClick={() => void test([name])}>测试</button><button className="text-button danger" disabled={!!busy || !inventory.revision} onClick={() => void remove([name])}>移除</button></div></td>
          </tr>;
        })}{!visible.length && <tr><td colSpan={12}><p className="empty-routes">{busy === "loading" ? "正在读取模型…" : !channel ? "该账户暂无渠道，请选择其他账户或先添加渠道。" : models.length ? "没有符合条件的模型" : "暂无模型，可点击“获取模型”读取所选 Key 可用的模型。"}</p></td></tr>}</tbody>
      </table></div>
    </section>
  </div>;
}
