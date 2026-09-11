import type { SourceType } from "../types";
export type NativeAdminMode = SourceType | "global";
export type AdminNavItemKey = "review" | "payment-candidates" | "eligibility-freezes" | "account-ledger" | "refund-cases" | "source-health" | "settings" | "return-to-user";
export function isAdminNavItemVisible(key: AdminNavItemKey, mode: NativeAdminMode | null): boolean {
  if (key === "return-to-user") return false;
  if (!mode) return true;
  if (mode === "global") return key === "settings" || key === "source-health";
  if (key === "settings") return false;
  if (key === "payment-candidates") return mode === "newapi";
  return true;
}
export function resolvePlatformSourceInstanceId(platform: SourceType, rows: readonly { sourceType: SourceType; sourceInstanceId: string }[]): string | null {
  const ids = new Set(rows.filter(row => row.sourceType === platform).map(row => row.sourceInstanceId));
  return ids.size === 1 ? [...ids][0] || null : null;
}
