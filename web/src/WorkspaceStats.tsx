import type React from "react";
import type {DataTone} from "./themes";

export const compact=(value:number)=>value.toLocaleString("zh-CN",{notation:"compact",maximumFractionDigits:1});
export const money=(value:number|null)=>value==null ? "—" : value.toLocaleString("zh-CN",{minimumFractionDigits:2,maximumFractionDigits:4});

export function Stat({
  label,
  value,
  note,
  icon,
  tone = "traffic",
}: {
  label: string;
  value: React.ReactNode;
  note: string;
  icon: React.ReactNode;
  tone?: DataTone;
}) {
  return (
    <div className="stat-card" data-tone={tone}>
      <div className="stat-label">
        {label}
        {icon}
      </div>
      <div className="stat-value">{value}</div>
      <div className="stat-note">{note}</div>
    </div>
  );
}
export function financialAmount(value: number | null, currency: string): string {
  if (value === null) return "—";
  const amount = value.toLocaleString("zh-CN", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 4,
  });
  return currency === "USD"
    ? `$${amount}`
    : `${amount} ${currency || "未设置单位"}`;
}
