import type { SourceType } from "../types";

// Single source of truth for the Chinese platform label shown across the
// user-facing invoice center (order source badges, admin instance labels,
// embedded-view account identity). Extracted out of App.tsx so it can be
// reused from lib/embedded-scope.ts without duplicating the strings.
export const sourceName: Record<SourceType, string> = {
  sub2api: "SoloV API",
  newapi: "SoloV 模型平台",
};
