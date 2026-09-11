import type { AuthUser, SourceType, SourceTypeWire } from "../types";
import { isKnownSourceType, sourceName } from "./source-labels";
export function scopeBySource<T extends { source: SourceTypeWire }>(items: T[], platform: SourceType | null): T[] {
  return platform ? items.filter(item => item.source === platform || !isKnownSourceType(item.source)) : items;
}
export function accountIdentityLabel({ user }: {
  user: Pick<AuthUser, "displayName" | "email" | "platform" | "platformUserId" | "username">;
}): string {
  if (user.platform) return user.email || user.username || `${sourceName[user.platform]} · 用户 ${user.platformUserId ?? "?"}`;
  return user.displayName || user.email || "已登录用户";
}
