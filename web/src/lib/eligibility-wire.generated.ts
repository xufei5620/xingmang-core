// 由 contracts/invoice-eligibility-wire.v1.json 生成，请勿手工编辑。
// 重新生成：cd backend && go test ./internal/eligibilitywire/... -update
//
// XM-INV-LOT-REASON-CONTRACT：后端会回哪些 eligibility_status / reason_code 是
// 后端的事实，前端不再手抄。Go 侧的探针测试跑真实 emitter 的笛卡尔积、把实际
// 输出与该契约双向比对，任一侧漂移都会让 go test 先于前端门禁变红。
//
// 注意这只是「提交时」的闸。运行时（后端先上线、前端 bundle 还旧）另有一层降级：
// http-api.ts 对未知但形状合法的值放行并标记 degraded，让它可见、不可选、有中文
// 兜底文案，而不是整页 throw。两层防的是不同的事，不要因为有了这个文件就把降级删掉。

export const lotEligibilityStatuses = [
  "active",
  "syncing",
  "frozen",
  "not_invoiceable_pending_reconciliation",
  "missing",
  "source_unavailable",
] as const;

export const lotReasonCodes = [
  "SOURCE_NOT_READY",
  "SOURCE_REFUND",
  "BEFORE_ELIGIBILITY_START",
  "SUBSCRIPTION_USAGE_UNSUPPORTED",
  "NO_POST_START_CONSUMPTION",
  "LEDGER_SYNCING",
  "LEDGER_PENDING_RECONCILIATION",
  "LEDGER_FROZEN",
] as const;

export const summaryStatuses = [
  "active",
  "syncing",
  "frozen",
  "not_invoiceable_pending_reconciliation",
] as const;

export const summaryReasons = [
  "BINDING_NOT_VERIFIED",
  "ACCOUNT_FROZEN",
  "PENDING_RECONCILIATION",
  "PROJECTION_PENDING",
  "SOURCE_NOT_READY",
  "NO_CONSUMED_CASH",
  "READY",
] as const;

export const summaryReasonMaxCount = 5;

export type LotEligibilityStatusWire = (typeof lotEligibilityStatuses)[number];
export type LotReasonCodeWire = (typeof lotReasonCodes)[number];
export type SummaryStatusWire = (typeof summaryStatuses)[number];
export type SummaryReasonWire = (typeof summaryReasons)[number];
