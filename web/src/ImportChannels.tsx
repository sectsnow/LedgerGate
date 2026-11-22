import { useEffect, useState } from "react";
import { LoaderCircle, RefreshCw } from "lucide-react";
import {
  api,
  protocolNames,
  type Account,
  type ChannelImportResult,
  type UpstreamKeyPage,
} from "./api";

const keyStates: Record<string, string> = {
  active: "正常",
  disabled: "已停用",
  expired: "已过期",
  exhausted: "额度耗尽",
  unknown: "状态未知",
};

export default function ImportChannels({
  account,
  close,
  imported,
}: {
  account: Account;
  close: () => void;
  imported: (result: ChannelImportResult) => Promise<void>;
}) {
  const [page, setPage] = useState(1);
  const [reload, setReload] = useState(0);
  const [data, setData] = useState<UpstreamKeyPage | null>(null);
  const [selected, setSelected] = useState<number[]>([]);
  const [protocols, setProtocols] = useState(["openai"]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    api<UpstreamKeyPage>(`/accounts/${account.id}/api-keys?page=${page}`)
      .then((value) => {
        if (active) setData(value);
      })
      .catch((error) => {
        if (active) {
          setData(null);
          setError(error.message);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [account.id, page, reload]);

  const available =
    data?.items.filter((key) => key.importable && !key.imported_channel_id) ||
    [];
  const allSelected =
    available.length > 0 && available.every((key) => selected.includes(key.id));

  return (
    <form
      onSubmit={async (event) => {
        event.preventDefault();
        setBusy(true);
        setError("");
        try {
          const result = await api<ChannelImportResult>(
            `/accounts/${account.id}/import-channels`,
            "POST",
            { key_ids: selected, protocols },
          );
          await imported(result);
        } catch (error) {
          setError((error as Error).message);
        } finally {
          setBusy(false);
        }
      }}
    >
      <fieldset className="protocol-selector" disabled={busy}>
        <legend>上游支持的协议</legend>
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
        <small>所选协议应用于本次导入的渠道，转发时优先使用客户端协议。</small>
      </fieldset>
      <div className="import-toolbar">
        <span>
          已选 {selected.length} 把密钥{data ? ` · 共 ${data.total} 把` : ""}
        </span>
        <button
          className="text-button"
          type="button"
          disabled={loading || busy}
          onClick={() => {
            setSelected([]);
            setReload((value) => value + 1);
          }}
        >
          <RefreshCw size={14} />
          重新加载
        </button>
      </div>
      {loading ? (
        <div className="empty" role="status">
          <LoaderCircle className="spin" size={22} />
          正在读取上游密钥…
        </div>
      ) : (
        data && (
          <>
            <div className="table-scroll import-key-list">
              <table>
                <thead>
                  <tr>
                    <th>
                      <input
                        className="key-selection"
                        type="checkbox"
                        aria-label="选择本页可导入密钥"
                        disabled={!available.length || busy}
                        checked={allSelected}
                        onChange={(event) =>
                          setSelected((previous) =>
                            event.target.checked
                              ? [
                                  ...new Set([
                                    ...previous,
                                    ...available.map((key) => key.id),
                                  ]),
                                ]
                              : previous.filter(
                                  (id) =>
                                    !available.some((key) => key.id === id),
                                ),
                          )
                        }
                      />
                    </th>
                    <th>名称 / 分组</th>
                    <th>状态</th>
                    <th>有效期</th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((key) => (
                    <tr key={key.id}>
                      <td>
                        <input
                          className="key-selection"
                          type="checkbox"
                          aria-label={`选择密钥 ${key.name}`}
                          checked={selected.includes(key.id)}
                          disabled={
                            !key.importable || !!key.imported_channel_id || busy
                          }
                          onChange={(event) =>
                            setSelected((previous) =>
                              event.target.checked
                                ? [...previous, key.id]
                                : previous.filter((id) => id !== key.id),
                            )
                          }
                        />
                      </td>
                      <td>
                        <strong>{key.name}</strong>
                        <small className="table-sub">
                          {key.group || "默认分组"} · #{key.id}
                        </small>
                      </td>
                      <td>
                        <span
                          className={`badge ${key.imported_channel_id || key.importable ? "ok" : "paused"}`}
                        >
                          {key.imported_channel_id
                            ? "已导入"
                            : keyStates[key.status] || "状态未知"}
                        </span>
                      </td>
                      <td>
                        {key.expires_at
                          ? new Date(key.expires_at).toLocaleString("zh-CN", {
                              hour12: false,
                            })
                          : "长期有效"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {data.items.length === 0 && (
                <div className="empty">该账户暂无 API 密钥</div>
              )}
            </div>
            <div className="import-pagination">
              <span>第 {page} 页</span>
              <div className="inline-actions">
                <button
                  className="secondary"
                  type="button"
                  disabled={page === 1 || busy}
                  onClick={() => setPage((value) => value - 1)}
                >
                  上一页
                </button>
                <button
                  className="secondary"
                  type="button"
                  disabled={page * data.page_size >= data.total || busy}
                  onClick={() => setPage((value) => value + 1)}
                >
                  下一页
                </button>
              </div>
            </div>
          </>
        )
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-footer">
        <button
          className="secondary"
          type="button"
          disabled={busy}
          onClick={close}
        >
          取消
        </button>
        <button
          className="primary"
          type="submit"
          disabled={
            busy ||
            loading ||
            !selected.length ||
            selected.length > 100 ||
            !protocols.length
          }
        >
          {busy && <LoaderCircle size={16} className="spin" />}
          {busy ? "正在导入" : `导入 ${selected.length} 条渠道`}
        </button>
      </div>
    </form>
  );
}
