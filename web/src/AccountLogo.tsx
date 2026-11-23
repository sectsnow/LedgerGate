import { useEffect, useState } from "react";
import type { Account } from "./api";
import "./account-logo.css";

export default function AccountLogo({ account }: { account: Account }) {
  const [status, setStatus] = useState("loading");
  const [attempt, setAttempt] = useState(0);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const refresh = (event: Event) => {
      if ((event as CustomEvent<number>).detail !== account.id) return;
      setRevision(value => value + 1); setAttempt(0); setStatus("loading");
    };
    window.addEventListener("account-logo-updated", refresh);
    return () => window.removeEventListener("account-logo-updated", refresh);
  }, [account.id]);
  const placeholder = account.kind === "newapi" ? "N" : "S";
  return <div className={`provider-icon ${account.kind}`}>
    {status === "loading" && <span aria-hidden="true">{placeholder}</span>}
    {status === "failed" && <button type="button" className="account-logo-retry" title="网站图标获取失败，点击重试" aria-label={`重新获取${account.name}的网站图标`} onClick={event => {
      event.stopPropagation(); setAttempt(value => value + 1); setStatus("loading");
    }}>{placeholder}</button>}
    {status !== "failed" && <img
      src={`/api/accounts/${account.id}/icon?v=${encodeURIComponent(account.base_url)}&store=1&revision=${revision}${attempt ? `&refresh=1&attempt=${attempt}` : ""}`}
      alt="" width={25} height={25} referrerPolicy="no-referrer"
      hidden={status !== "loaded"} onLoad={() => {
        setStatus("loaded");
        if (attempt) window.dispatchEvent(new CustomEvent("account-logo-updated", { detail: account.id }));
      }}
      onError={() => setStatus("failed")}
    />}
  </div>;
}
