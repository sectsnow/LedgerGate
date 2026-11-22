import { useState } from "react";
import { LoaderCircle } from "lucide-react";
import {
  api,
  channelEnabled,
  type AccountRow,
  type ChannelRow,
  type ModelGenerationResult,
} from "./api";

export default function GenerateModels({
  channels,
  accounts,
  close,
  generated,
}: {
  channels: ChannelRow[];
  accounts: AccountRow[];
  close: () => void;
  generated: (result: ModelGenerationResult) => Promise<void>;
}) {
  const available = channels.filter(channelEnabled);
  const [selected, setSelected] = useState(
    available.map((row) => row.channel.id),
  );
  const [busy, setBusy] = useState(false);
  const [resync, setResync] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<ModelGenerationResult | null>(null);
  const allSelected =
    available.length > 0 &&
    available.every((row) => selected.includes(row.channel.id));

  return (
    <form
      onSubmit={async (event) => {
        event.preventDefault();
        setBusy(true);
        setError("");
        setResult(null);
        try {
          const value = await api<ModelGenerationResult>(
            "/groups/generate",
            "POST",
            {
              channel_ids: selected,
              use_saved_models: !resync,
            },
          );
          setResult(value);
          await generated(value);
          if (value.failures.length === 0) close();
        } catch (error) {
          setError((error as Error).message);
        } finally {
          setBusy(false);
        }
      }}
    >
      <p className="field-hint generate-models-description">
        使用所选渠道已保存的模型列表，尚未获取过列表的渠道会自动同步。新模型默认手动选择第一条路由，可在模型列表切换使用的上游。
      </p>
      <label className="checkbox generate-resync">
        <input type="checkbox" checked={resync} disabled={busy} onChange={(event) => setResync(event.target.checked)} />
        生成前重新同步上游模型（会替换手工编辑的列表）
      </label>
      <div className="list-toolbar">
        <label className="checkbox">
          <input
            type="checkbox"
            checked={allSelected}
            disabled={busy || !available.length}
            onChange={(event) => {
              setSelected(
                event.target.checked
                  ? available.map((row) => row.channel.id)
                  : [],
              );
            }}
          />
          全选可用渠道
        </label>
        <span>
          已选 {selected.length} / {available.length}
        </span>
      </div>
      <div className="generate-channel-list">
        {channels.map(row => {
          const channel = row.channel;
          const account = accounts.find(
            (row) => row.account.id === channel.account_id,
          )?.account;
          const name = `${account?.name || "直连供应商"} / ${channel.name}`;
          return (
            <label className="checkbox generate-channel-row" key={channel.id}>
              <input
                type="checkbox"
                checked={selected.includes(channel.id)}
                disabled={busy || !channelEnabled(row)}
                onChange={(event) => {
                  setSelected((previous) =>
                    event.target.checked
                      ? [...previous, channel.id]
                      : previous.filter((id) => id !== channel.id),
                  );
                }}
              />
              <span>
                <strong>{name}</strong>
                <small>{row.monitoring_paused ? "账户监控已停止，关联渠道暂停" : channel.enabled ? channel.base_url : "已停用"}</small>
              </span>
            </label>
          );
        })}
        {channels.length === 0 && (
          <div className="empty">请先添加或导入转发渠道。</div>
        )}
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {result && result.failures.length > 0 && (
        <div className="generation-failures" role="status">
          <p>
            已新增 {result.created_models} 个模型、{result.added_routes}{" "}
            条路由。以下渠道可检查后重试。
          </p>
          <ul>
            {result.failures.map((failure) => (
              <li key={failure.channel_id}>
                <strong>{failure.name}</strong> · {failure.message}
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="form-footer">
        <button
          type="button"
          className="secondary"
          disabled={busy}
          onClick={close}
        >
          关闭
        </button>
        <button
          type="submit"
          className="primary"
          disabled={busy || selected.length === 0 || selected.length > 100}
        >
          {busy && <LoaderCircle size={16} className="spin" />}
          {busy ? "正在生成" : resync ? "同步模型并生成" : "生成模型路由"}
        </button>
      </div>
      {selected.length > 100 && (
        <p className="form-error">每次最多选择 100 条渠道。</p>
      )}
    </form>
  );
}
