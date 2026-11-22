import { useState } from "react";
import { Check, Search } from "lucide-react";
import "./key-model-picker.css";

export default function KeyModelPicker({ available, selected, change, disabled }: {
  available: string[];
  selected: string[];
  change: (models: string[]) => void;
  disabled: boolean;
}) {
  const [search, setSearch] = useState("");
  const choices = [...new Set([...available, ...selected])];
  const visible = choices.filter(model => model.toLowerCase().includes(search.trim().toLowerCase()));
  return <div className="key-model-picker" role="group" aria-label="允许使用的模型">
    <div className="key-model-heading"><span>允许使用的模型</span><span>{selected.length ? `已选 ${selected.length} 个` : "全部可用"}</span></div>
    <div className="key-model-toolbar">
      <div className="search"><Search size={15} aria-hidden="true" /><input aria-label="搜索允许使用的模型" placeholder="搜索模型" value={search} disabled={disabled} onChange={event => setSearch(event.target.value)} onKeyDown={event => { if (event.key === "Enter") event.preventDefault(); }} /></div>
      <button className="text-button" type="button" disabled={disabled || !selected.length} onClick={() => change([])}>清空选择</button>
    </div>
    <div className="key-model-options">
      {visible.map(model => <button className="key-model-option" type="button" key={model} aria-pressed={selected.includes(model)} disabled={disabled} onClick={() => change(selected.includes(model) ? selected.filter(name => name !== model) : [...selected, model])}>
        {selected.includes(model) && <Check size={13} aria-hidden="true" />}<span>{model}</span>{!available.includes(model) && <small>未配置</small>}
      </button>)}
      {!visible.length && <p className="key-model-empty">{choices.length ? "没有匹配的模型" : "暂无已配置的统一模型，请先在模型路由中添加。"}</p>}
    </div>
    <p className="key-model-hint" role="status">{selected.length ? "仅允许使用选中的模型；清空选择后允许全部模型。" : "不选择则无限制，允许使用全部模型。"}</p>
  </div>;
}
