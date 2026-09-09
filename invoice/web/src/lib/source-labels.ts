import type { SourceType, SourceTypeWire } from "../types";

// Single source of truth for the Chinese platform label shown across the
// user-facing invoice center (order source badges, admin instance labels,
// embedded-view account identity). Extracted out of App.tsx so it can be
// reused from lib/embedded-scope.ts without duplicating the strings.
export const sourceName: Record<SourceType, string> = {
  sub2api: "SoloV API",
  newapi: "SoloV 模型平台",
};

// The platforms this bundle knows. Derived from the label table rather than
// listed a second time, so "known" and "has a Chinese label" cannot drift apart.
export function isKnownSourceType(value: SourceTypeWire): value is SourceType {
  return Object.prototype.hasOwnProperty.call(sourceName, value);
}

// Fallback wording for a platform code this bundle predates. Same role as
// eligibilityStatusLabel(): the label table stays keyed on the exact union, and
// anything that arrived from the wire goes through here instead of indexing
// the table directly (which is how an unknown code becomes an empty badge).
export const unknownSourceTypeLabel = "未识别的平台";

export function sourceTypeLabel(value: SourceTypeWire): string {
  return isKnownSourceType(value) ? sourceName[value] : unknownSourceTypeLabel;
}
