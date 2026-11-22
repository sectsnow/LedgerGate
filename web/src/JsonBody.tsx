import { useMemo, useState, type SyntheticEvent } from "react";
import { parseJsonBody } from "./jsonBody";

function JsonNode({ value, name, depth = 0 }: { value: unknown; name?: string | number; depth?: number }) {
  const collection = value !== null && typeof value === "object";
  const entries = collection ? Object.entries(value) : [];
  const array = Array.isArray(value);
  const [expanded, setExpanded] = useState(depth === 0 && collection && entries.length <= 20);
  const [visible, setVisible] = useState(50);
  const label = name === undefined ? "JSON 根节点" : typeof name === "number" ? `JSON 项 ${name}` : `JSON 字段 ${name}`;
  const key = name === undefined ? null : <><span className="json-key">{typeof name === "number" ? name : JSON.stringify(name)}</span><span>: </span></>;
  function toggle(event: SyntheticEvent<HTMLDetailsElement>) {
    if (event.target === event.currentTarget) setExpanded(event.currentTarget.open);
  }
  if (collection) {
    const start = array ? "[" : "{";
    const end = array ? "]" : "}";
    if (!entries.length) return <div className="json-leaf">{key}<span>{start}{end}</span></div>;
    const kind = !array && "type" in value && typeof value.type === "string" ? value.type : "";
    return <details className="json-node" aria-label={label} open={expanded} onToggle={toggle}>
      <summary>{key}<span>{start}{expanded ? "" : " … " + end}</span><span className="json-hint">{entries.length} 项{kind ? ` · ${kind}` : ""}</span></summary>
      {expanded && <>
        <div className="json-children">
          {entries.slice(0, visible).map(([key, child]) => <JsonNode key={key} value={child} name={array ? Number(key) : key} depth={depth + 1} />)}
          {visible < entries.length && <button className="text-button json-more" type="button" onClick={() => setVisible(count => count + 50)}>显示后续 {Math.min(50, entries.length - visible)} 项（剩余 {entries.length - visible} 项）</button>}
        </div>
        <div className="json-close">{end}</div>
      </>}
    </details>;
  }
  const text = JSON.stringify(value) ?? "null";
  if (typeof value === "string" && (value.length > 160 || value.split("\n").length > 3)) {
    return <details className="json-node json-long-string" aria-label={label} open={expanded} onToggle={toggle}>
      <summary>{key}<span className="json-string">{JSON.stringify(value.slice(0, 100))}…</span><span className="json-hint">{value.length.toLocaleString("zh-CN")} 字符</span></summary>
      {expanded && <pre className="json-full-string">{text}</pre>}
    </details>;
  }
  return <div className="json-leaf">{key}<span className={typeof value === "string" ? "json-string" : typeof value === "number" ? "json-number" : "json-literal"}>{text}</span></div>;
}

function BodyContent({ text, stream, title }: { text: string; stream: boolean; title: string }) {
  const parsed = useMemo(() => parseJsonBody(text, stream), [text, stream]);
  return <>
    {parsed.format === "sse" && <p className="field-hint">SSE 事件已解析为 JSON，共 {(parsed.value as unknown[]).length} 个事件。</p>}
    {parsed.incomplete && <p className="field-hint">{parsed.format === "sse" ? "部分事件尚未完整，已保留对应文本片段。" : "内容未形成完整 JSON，显示已记录的文本片段。"}</p>}
    <div className="json-tree" role="group" aria-label={`${title} JSON`}><JsonNode value={parsed.value} /></div>
  </>;
}

export default function JsonBody({ title, text, truncated, stream = false, placeholder }: { title: string; text: string; truncated: boolean; stream?: boolean; placeholder: string }) {
  const [expanded, setExpanded] = useState(false);
  return <details className="request-body-section" aria-label={title} onToggle={event => {
    if (event.target === event.currentTarget) setExpanded(event.currentTarget.open);
  }}>
    <summary><strong>{title}</strong><span>{stream ? "SSE → JSON" : "JSON"}</span>{truncated && <span>已截断</span>}</summary>
    {expanded && <div className="request-body-content">{text ? <BodyContent text={text} stream={stream} title={title} /> : <p className="field-hint">{placeholder}</p>}</div>}
  </details>;
}
