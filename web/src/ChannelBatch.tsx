import { useEffect, useRef, useState } from "react";
import {
  LoaderCircle,
  Pause,
  Play,
  RefreshCw,
  SlidersHorizontal,
  Trash2,
} from "lucide-react";
import {
  protocolNames,
  type Channel,
  type ChannelBatchInput,
  type ChannelBatchResult,
  type Group,
} from "./api";

export function ChannelSelection({
  selected,
  total,
  label,
  disabled,
  toggle,
}: {
  selected: number;
  total: number;
  label: string;
  disabled?: boolean;
  toggle: (checked: boolean) => void;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current)
      ref.current.indeterminate = selected > 0 && selected < total;
  }, [selected, total]);
  return (
    <input
      ref={ref}
      className="key-selection"
      type="checkbox"
      aria-label={label}
      disabled={disabled || total === 0}
      checked={total > 0 && selected === total}
      onChange={(event) => toggle(event.target.checked)}
    />
  );
}

export default function ChannelBatch({
  channels,
  sync,
  groups,
  busy,
  setBusy,
  execute,
  completed,
  dialog: Dialog,
}: {
  channels: Channel[];
  sync: () => void;
  groups: Group[];
  busy: boolean;
  setBusy: (value: boolean) => void;
  execute: (input: ChannelBatchInput) => Promise<ChannelBatchResult>;
  completed: (result: ChannelBatchResult) => void;
  dialog: React.ComponentType<{
    title: string;
    children: React.ReactNode;
    close: () => void;
  }>;
}) {
  const [action, setAction] = useState<"delete" | "protocols" | null>(null);
  const [protocols, setProtocols] = useState(["openai"]);
  const [error, setError] = useState("");
  const [result, setResult] = useState<ChannelBatchResult | null>(null);
  const unavailable = busy || channels.length === 0 || channels.length > 100;
  const ids = channels.map((channel) => channel.id);
  const affected = groups
    .filter((group) =>
      group.routes.some((route) => ids.includes(route.channel_id)),
    )
    .map((group) => ({
      name: group.name,
      remaining: group.routes.filter((route) => !ids.includes(route.channel_id))
        .length,
    }));

  async function apply(action: ChannelBatchInput["action"]) {
    setBusy(true);
    setError("");
    setResult(null);
    try {
      const result = await execute({
        ids,
        action,
        ...(action === "protocols" ? { protocols } : {}),
        ...(action === "delete" ? { detach_routes: true } : {}),
      });
      setResult(result);
      completed(result);
      setAction(null);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="channel-batch-actions" aria-label="渠道批量操作">
        <button
          className="secondary"
          disabled={unavailable}
          onClick={() => apply("enable")}
        >
          <Play size={14} />
          批量启用
        </button>
        <button
          className="secondary"
          disabled={unavailable}
          onClick={() => apply("disable")}
        >
          <Pause size={14} />
          批量停用
        </button>
        <button
          className="secondary"
          disabled={unavailable}
          onClick={() => {
            setProtocols(
              channels[0].protocols?.length
                ? [...channels[0].protocols]
                : [channels[0].protocol],
            );
            setError("");
            setAction("protocols");
          }}
        >
          <SlidersHorizontal size={14} />
          修改协议
        </button>
        <button
          className="secondary"
          disabled={unavailable}
          onClick={sync}
        >
          <RefreshCw size={14} />
          同步模型
        </button>
        <button
          className="secondary danger"
          disabled={unavailable}
          onClick={() => {
            setError("");
            setAction("delete");
          }}
        >
          <Trash2 size={14} />
          批量删除
        </button>
      </div>
      {channels.length > 100 && (
        <p className="form-error">单次最多操作 100 条渠道，请减少勾选。</p>
      )}
      {busy && (
        <p className="channel-batch-status" role="status">
          <LoaderCircle size={16} className="spin" />
          正在处理 {channels.length} 条渠道…
        </p>
      )}
      {error && !action && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {result && (
        <div className="channel-batch-result" role="status">
          <p>
            批量操作完成，成功 {result.succeeded} 条，失败 {result.failed} 条。
          </p>
          {result.failed > 0 && (
            <ul>
              {result.results
                .filter((item) => !item.success)
                .map((item) => (
                  <li key={item.id}>
                    {item.name || `渠道 #${item.id}`}
                    <span>{item.error}</span>
                  </li>
                ))}
            </ul>
          )}
        </div>
      )}
      {action && (
        <Dialog
          title={action === "delete" ? "批量删除渠道" : "批量修改协议"}
          close={() => {
            if (!busy) setAction(null);
          }}
        >
          {action === "delete" ? (
            <>
              <p className="delete-description">
                确认删除以下 {channels.length} 条本地渠道配置及其模型路由引用？
              </p>
              <ul className="channel-batch-targets">
                {channels.map((channel) => (
                  <li key={channel.id}>{channel.name}</li>
                ))}
              </ul>
              {affected.length > 0 && (
                <div className="delete-references">
                  <p>受影响的模型</p>
                  <ul>
                    {affected.map((group) => (
                      <li key={group.name}>
                        {group.name}
                        <span>删除后剩余 {group.remaining} 条路由</span>
                      </li>
                    ))}
                  </ul>
                  {affected.some((group) => group.remaining === 0) && (
                    <p className="form-error">
                      剩余 0 条路由的模型将暂时无法转发。
                    </p>
                  )}
                </div>
              )}
            </>
          ) : (
            <fieldset className="protocol-selector" disabled={busy}>
              <legend>设置所选 {channels.length} 条渠道的上游协议</legend>
              <div className="protocol-options">
                {Object.entries(protocolNames).map(([id, name]) => (
                  <label className="checkbox" key={id}>
                    <input
                      type="checkbox"
                      checked={protocols.includes(id)}
                      onChange={(event) =>
                        setProtocols((previous) =>
                          event.target.checked
                            ? [...previous, id]
                            : previous.filter((value) => value !== id),
                        )
                      }
                    />
                    {name}
                  </label>
                ))}
              </div>
              <small>保存后，所选渠道使用这里勾选的协议。</small>
            </fieldset>
          )}
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div className="form-footer">
            <button
              className="secondary"
              disabled={busy}
              onClick={() => setAction(null)}
            >
              取消
            </button>
            <button
              className={action === "delete" ? "destructive" : "primary"}
              disabled={
                unavailable ||
                (action === "protocols" && protocols.length === 0)
              }
              onClick={() => apply(action)}
            >
              {busy && <LoaderCircle size={16} className="spin" />}
              {action === "delete"
                ? `确认删除 ${channels.length} 条渠道`
                : "保存协议"}
            </button>
          </div>
        </Dialog>
      )}
    </>
  );
}
