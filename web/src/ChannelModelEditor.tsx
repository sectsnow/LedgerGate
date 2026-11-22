import { useEffect, useId, useRef, useState } from "react";
import { Trash2 } from "lucide-react";
import { ChannelSelection } from "./ChannelBatch";

export function channelModelNames(raw = ""): string[] {
  try {
    const value = JSON.parse(raw || "[]");
    if (Array.isArray(value)) {
      return value.filter((item): item is string => typeof item === "string");
    }
  } catch {
    /* 旧记录无法解析时可重新填写模型列表。 */
  }
  return [];
}

function splitModels(text: string): string[] {
  return [...new Set(text.split(/[\n,，]+/).map((name) => name.trim()).filter(Boolean))];
}

export default function ChannelModelEditor({ models, change, disabled }: {
  models: string[];
  change: (models: string[]) => void;
  disabled: boolean;
}) {
  const id = useId();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<number[]>([]);
  const [bulkMode, setBulkMode] = useState<"add" | "replace">("add");
  const [bulkText, setBulkText] = useState("");
  const bulkInput = useRef<HTMLTextAreaElement>(null);
  const visible = models.map((name, index) => ({ name, index })).filter(
    ({ name }) => name.toLowerCase().includes(search.trim().toLowerCase()),
  );
  const editingAll = bulkMode === "replace";
  const locked = disabled || editingAll;
  useEffect(() => {
    bulkInput.current?.setCustomValidity(editingAll || bulkText.trim()
      ? "请先应用或取消待编辑的模型，再保存渠道。"
      : "");
  }, [editingAll, bulkText]);
  function replace(next: string[]) {
    change(next);
    setSelected([]);
  }
  return (
    <div className="channel-model-editor">
      <p className="field-hint">
        填写上游支持的模型 ID，保存渠道后生效。这里管理渠道模型列表，已有转发关系在“模型路由”中调整。重新同步上游会替换此列表。
      </p>
      <div className="channel-model-toolbar">
        <input
          aria-label="搜索渠道模型"
          placeholder="搜索模型"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
        <span>{visible.length} / {models.length} 个模型 · 已选 {selected.length}</span>
      </div>
      <div className="channel-model-actions">
        <label className="checkbox">
        <ChannelSelection
          label="选择筛选后的全部模型"
          selected={visible.filter(({ index }) => selected.includes(index)).length}
          total={visible.length}
          disabled={locked}
          toggle={(checked) => setSelected((previous) => checked
            ? [...new Set([...previous, ...visible.map(({ index }) => index)])]
            : previous.filter((index) => !visible.some((row) => row.index === index)))}
        />
          全选筛选结果
        </label>
        <button
          type="button"
          className="secondary danger"
          disabled={locked || !selected.length}
          onClick={() => replace(models.filter((_, index) => !selected.includes(index)))}
        >
          删除所选（{selected.length}）
        </button>
        <button
          type="button"
          className="secondary"
          disabled={disabled || editingAll}
          onClick={() => {
            setBulkMode("replace");
            setBulkText(models.join("\n"));
          }}
        >
          批量编辑全部
        </button>
      </div>
      <div className="channel-model-list" aria-label="渠道模型列表">
        {visible.map(({ name, index }) => (
          <div className="channel-model-row" key={index}>
            <input
              className="key-selection"
              type="checkbox"
              aria-label={`选择模型 ${name || index + 1}`}
              disabled={locked}
              checked={selected.includes(index)}
              onChange={(event) => setSelected((previous) => event.target.checked
                ? [...previous, index]
                : previous.filter((value) => value !== index))}
            />
            <input
              aria-label={`模型 ${index + 1} 名称`}
              value={name}
              disabled={locked}
              title={name}
              onChange={(event) => change(models.map((model, position) =>
                position === index ? event.target.value : model,
              ))}
            />
            <button
              type="button"
              className="icon-button danger"
              aria-label={`移除模型 ${name || index + 1}`}
              disabled={locked}
              onClick={() => replace(models.filter((_, position) => position !== index))}
            >
              <Trash2 size={15} />
            </button>
          </div>
        ))}
        {visible.length === 0 && <p className="field-hint">{models.length
          ? "没有匹配的模型。"
          : "暂无模型，可在下方添加，或保存渠道后同步上游模型。"}</p>}
      </div>
      <label className="channel-model-bulk-label" htmlFor={id}>
        {editingAll ? "批量编辑整个列表" : "添加模型"}
      </label>
      <textarea
        id={id}
        ref={bulkInput}
        rows={editingAll ? 8 : 3}
        value={bulkText}
        disabled={disabled}
        onChange={(event) => setBulkText(event.target.value)}
        placeholder="每行一个模型 ID，也可用逗号分隔"
      />
      <p className="field-hint">{editingAll
        ? "应用后替换当前列表；留空应用可清空列表。最后点击保存渠道生效。"
        : "支持单个或批量添加，点击“添加到列表”后再保存渠道，重复模型会自动合并。"}</p>
      <div className="channel-model-actions">
        {editingAll && (
          <button type="button" className="secondary" disabled={disabled} onClick={() => {
            setBulkMode("add");
            setBulkText("");
          }}>
            取消批量编辑
          </button>
        )}
        <button
          type="button"
          className="secondary"
          disabled={disabled || (!editingAll && !bulkText.trim())}
          onClick={() => {
            const names = splitModels(bulkText);
            replace(editingAll ? names : [...new Set([...models, ...names])]);
            setBulkText("");
            setBulkMode("add");
          }}
        >
          {editingAll ? "应用修改" : "添加到列表"}
        </button>
      </div>
    </div>
  );
}
