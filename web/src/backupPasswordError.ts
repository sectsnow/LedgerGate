export function backupPasswordError(password: string): string {
  const bytes = new TextEncoder().encode(password).length;
  return bytes >= 12 && bytes <= 256 ? "" : "加密密码须为 12–256 字节。";
}
