import type { EligibilityFreeze, InvoiceRequest } from "../types";

export const maxInvoicePDFBytes = 20 * 1024 * 1024;

export function canUserCancelInvoice(
  request: Pick<InvoiceRequest, "status" | "workflowStatus">,
) {
  return (
    request.workflowStatus === "pending_review" ||
    request.workflowStatus === "needs_changes" ||
    (!request.workflowStatus && request.status === "submitted")
  );
}

export function userCancellationLabel(
  request: Pick<InvoiceRequest, "workflowStatus">,
) {
  return request.workflowStatus === "needs_changes"
    ? "取消并重新申请"
    : "取消申请";
}

export function normalizeIssuedAt(value: string) {
  const parsed = new Date(value);
  if (!value.trim() || !Number.isFinite(parsed.getTime())) {
    throw new Error("请输入有效的实际开票时间。");
  }
  return parsed.toISOString();
}

export function currentLocalDateTimeValue(now = new Date()) {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

export function eligibilityStartLabel(value: string) {
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(new Date(value));
}

export function invoicePDFSizeAllowed(size: number) {
  return Number.isSafeInteger(size) && size > 0 && size <= maxInvoicePDFBytes;
}

export function replaceDirectRequestAfterMutation<
  T extends { id: string },
>(current: T | null, updated: T) {
  return current?.id === updated.id ? updated : current;
}

// CR-0009 "变更范围" item 5: after XM-INV-ELIG-AUTO-RECONCILE/
// XM-INV-ELIG-QUEUE-NARROW, UNKNOWN_NEGATIVE_BALANCE/USAGE_EXCEEDS_LEDGER
// freezes and a generalized (account-scoped) LATE_FINALIZED_EVENT freeze
// are all mechanically self-healing on the next reconciliation cycle -- any
// that still appear on /admin/eligibility-freezes are legacy rows from
// before those slices shipped (or before the queue-narrowing migration tool
// has actually been run in production), not something an operator needs to
// act on. Used by App.tsx's EligibilityFreezesPage as a client-side display
// filter only: the server's own /admin/eligibility-freezes contract and the
// resolve flow are unchanged (CR-0009 "契约变化").
export function isMechanicalReconciliationFreeze(item: EligibilityFreeze) {
  return (
    item.reason === "UNKNOWN_NEGATIVE_BALANCE" ||
    item.reason === "USAGE_EXCEEDS_LEDGER" ||
    (item.reason === "LATE_FINALIZED_EVENT" && item.scope === "account")
  );
}
