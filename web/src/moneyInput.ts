export function budgetMicros(value: string): number | null {
  const text = value.trim();
  if (!text) return null;
  if (!/^\d+(\.\d{1,6})?$/.test(text)) throw new Error("预算须为非负美元金额，最多 6 位小数。");
  const [whole, fraction = ""] = text.split(".");
  const amount = BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, "0"));
  if (amount > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("预算金额过大。");
  return Number(amount);
}
