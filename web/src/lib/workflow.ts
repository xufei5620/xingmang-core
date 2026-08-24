import type { InvoiceRequest } from "../types";

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

export function invoicePDFSizeAllowed(size: number) {
  return Number.isSafeInteger(size) && size > 0 && size <= maxInvoicePDFBytes;
}

export function replaceDirectRequestAfterMutation<
  T extends { id: string },
>(current: T | null, updated: T) {
  return current?.id === updated.id ? updated : current;
}
