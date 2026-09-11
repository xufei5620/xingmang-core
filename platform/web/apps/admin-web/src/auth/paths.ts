/** 只接受站内相对路径：`next` 来自 URL，放行 `https://…` 或 `//…` 就是一个
 *  开放重定向。不合法一律回工作台。 */
export function safeNextPath(raw: string | null | undefined, fallback = "/dashboard"): string {
  if (!raw) return fallback;
  if (!raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\")) return fallback;
  if (raw.startsWith("/login") || raw.startsWith("/auth/")) return fallback;
  return raw;
}
