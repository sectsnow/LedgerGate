import { useState } from "react";
import { Check, Copy, LoaderCircle } from "lucide-react";
import { copyText } from "./clipboard";

export default function CopyButton({
  text,
  label = "复制",
  disabled = false,
}: {
  text: string | (() => Promise<string>);
  label?: string;
  disabled?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <>
      <button
        type="button"
        className="icon-button copy-button"
        aria-label={label}
        disabled={disabled || busy}
        onClick={async () => {
          setBusy(true);
          setError("");
          try {
            const value = typeof text === "function" ? await text() : text;
            await copyText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? (
          <LoaderCircle size={16} className="spin" />
        ) : copied ? (
          <Check size={16} />
        ) : (
          <Copy size={16} />
        )}
        {copied ? "已复制" : label}
      </button>
      {error && <small role="alert">{error}</small>}
    </>
  );
}
