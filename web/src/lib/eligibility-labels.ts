import type {
  EligibilityStatusKnown,
  EligibilitySummaryReason,
} from "../types";

// XM-INV-LOT-REASON-CONTRACT. The Chinese copy for every eligibility value a
// user can see, extracted out of App.tsx so the wording itself is testable --
// the product owner does not read English, so "the page rendered without
// throwing" is not the bar; the sentence has to be there and has to be right.
//
// Both tables key on the EXACT generated unions from
// contracts/invoice-eligibility-wire.v1.json. That buys two compile-time
// guarantees:
//   1. a value added to the contract with no Chinese copy fails typecheck; and
//   2. indexing a *widened* wire value straight into a table fails typecheck,
//      which is what forces callers through the fallback accessors below.
// Without (2) an unrecognised status types fine and renders as an empty node --
// a blank badge, which looks like a styling bug rather than missing data.

export const eligibilityReasonLabels: Record<EligibilitySummaryReason, string> =
  {
    BINDING_NOT_VERIFIED: "平台账号尚未完成可信绑定",
    ACCOUNT_FROZEN: "资金资格已安全冻结",
    // XM-INV-ELIG-AUTO-RECONCILE. Deliberately worded apart from
    // ACCOUNT_FROZEN: no admin queue entry exists for these accounts and no
    // human action makes it finish sooner, so the copy has to say it resolves
    // on its own -- otherwise people raise tickets against a state nobody can
    // act on. The accounts currently in it have been there since early
    // September.
    PENDING_RECONCILIATION: "来源账本正在自动对账，完成后会自动恢复",
    PROJECTION_PENDING: "资金账本正在重新计算",
    SOURCE_NOT_READY: "来源五条同步流尚未全部就绪",
    NO_CONSUMED_CASH: "当前没有已消费的现金金额",
    READY: "可提交开票申请",
  };

// The fallbacks quote the raw code on purpose: someone looking at a screenshot
// of a degraded page has to be able to say which value the backend actually
// sent, without asking the user to reproduce anything.
export function eligibilityReasonLabel(reason: string) {
  return (
    (eligibilityReasonLabels as Record<string, string>)[reason] ??
    `原因待确认（${reason}）`
  );
}

export const eligibilityStatusLabels: Record<EligibilityStatusKnown, string> = {
  active: "资格正常",
  syncing: "账本同步中",
  frozen: "资格已冻结",
  missing: "资格账本未建立",
  source_unavailable: "来源同步不可用",
  // Byte-for-byte the wording the admin ledger view already uses for this same
  // state (accountBlockStateLabels in App.tsx). One state, one sentence -- an
  // operator on the phone to a user must not be reading different words off a
  // different page.
  not_invoiceable_pending_reconciliation: "对账中暂不可开票",
};

export function eligibilityStatusLabel(status: string) {
  return (
    (eligibilityStatusLabels as Record<string, string>)[status] ??
    `账本状态待确认（${status}）`
  );
}
