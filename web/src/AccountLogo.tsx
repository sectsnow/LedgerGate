import { useState } from "react";
import type { Account } from "./api";
import "./account-logo.css";

export default function AccountLogo({ account }: { account: Account }) {
  const [status, setStatus] = useState("loading");
  return <div className={`provider-icon ${account.kind}`} aria-hidden="true">
    {status !== "loaded" && (account.kind === "newapi" ? "N" : "S")}
    {status !== "failed" && <img
      src={`/api/accounts/${account.id}/icon?v=${encodeURIComponent(account.base_url)}`}
      alt="" width={25} height={25} referrerPolicy="no-referrer"
      hidden={status !== "loaded"} onLoad={() => setStatus("loaded")}
      onError={() => setStatus("failed")}
    />}
  </div>;
}
