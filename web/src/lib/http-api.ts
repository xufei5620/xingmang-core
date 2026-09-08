import type {
  AccountBlockState,
  AccountLedgerConsumptionDay,
  AccountLedgerDetail,
  AccountLedgerFilters,
  AccountLedgerListItem,
  AccountLedgerPage,
  AccountLedgerRecharge,
  AuthSession,
  DashboardSummary,
  EligibilityFreeze,
  EligibilityFreezeFilters,
  EligibilityProjectionHealth,
  FundingOrder,
  InvoicePolicy,
  InvoiceProfile,
  InvoiceSystemSettings,
  InvoiceDeliveryState,
  InvoiceRequest,
  InvoiceStatus,
  PaymentCandidate,
  PlatformLoginInput,
  PlatformLoginOutcome,
  PlatformLoginTwoFAInput,
  RefundCase,
  RefundCaseStatus,
  SourceAccount,
  SourceHealthReport,
  UserEligibilitySummary,
  VerificationState,
} from "../types";
import {
  InvoiceApiError,
  publishAuthFailure,
  type InvoiceApiClient,
} from "./api-contract";
import { isUnobservedTimestamp } from "./format";

type RequestRole = "user" | "admin";

type ItemsResponse<T> = { items: T[] };
type BackendError = { error?: { code?: string; message?: string } };

type BackendSession = {
  authenticated: boolean;
  admin_step_up_required?: boolean;
  // CR-0006 (XM-INV-CONSOLE-ASSERT). Optional on the wire only so an older
  // cached response shape never hard-fails mapSession; treated as true
  // (today's actual default) when absent -- see mapSession below.
  oidc_admin_login_enabled?: boolean;
  user?: {
    id: string;
    display_name: string;
    email: string;
    email_verified: boolean;
    role: "user" | "admin";
    platform?: "sub2api" | "newapi" | "";
    platform_user_id?: string;
    username?: string;
  };
  csrf_token?: string;
};

type BackendPlatformLoginOutcome = {
  ok: boolean;
  requires_two_fa?: boolean;
  temp_token?: string;
};

export function mapPlatformLoginOutcome(
  value: BackendPlatformLoginOutcome,
): PlatformLoginOutcome {
  if (!value.requires_two_fa) return { ok: true };
  if (
    !value.temp_token ||
    value.temp_token.length < 16 ||
    /[\r\n\0]/.test(value.temp_token)
  ) {
    throw new InvoiceApiError("登录服务返回了无法识别的验证状态。", {
      code: "INVALID_PLATFORM_LOGIN_RESPONSE",
    });
  }
  return { ok: true, requiresTwoFA: true, tempToken: value.temp_token };
}

// `platform` is undefined in the normal auto-detect flow: JSON.stringify
// drops an undefined-valued property, so the wire body simply omits
// "platform" and the backend auto-detects it from the identifier. An
// explicit platform (ops/test override) is forwarded as-is.
export function platformLoginBody(input: PlatformLoginInput) {
  return {
    platform: input.platform,
    identifier: input.identifier,
    password: input.password,
  };
}

// Same reasoning as platformLoginBody: omitting `platform` here lets the
// backend use the platform it already remembered for this temp_token.
export function platformLoginTwoFABody(input: PlatformLoginTwoFAInput) {
  return {
    platform: input.platform,
    temp_token: input.tempToken,
    code: input.code,
  };
}

export type BackendSourceAccount = {
  id: string;
  source_type: "sub2api" | "newapi";
  source_instance_id: string;
  source_name: string;
  external_user_id_masked: string;
  binding_status: "pending" | "verified" | "frozen" | "revoked";
  verified_at?: string;
  last_observed_at?: string;
};

type BackendServiceUnitSummary = {
  service_units: string;
  unit_code: string;
};

type BackendEligibilitySummary = {
  source_instance_id: string;
  source_type: "sub2api" | "newapi";
  source_name: string;
  binding_status: "pending" | "verified" | "frozen" | "revoked";
  status: "active" | "syncing" | "frozen" | "missing" | "source_unavailable";
  currency: string;
  available_minor: number;
  consumed_minor: number;
  unconsumed_minor: number;
  reserved_minor: number;
  issued_minor: number;
  legacy_noninvoiceable: BackendServiceUnitSummary;
  noncash: BackendServiceUnitSummary;
  reasons: string[];
};

type BackendEligibilityFreeze = {
  id: string;
  principal_id: string;
  source_instance_id: string;
  source_type: "sub2api" | "newapi";
  source_name: string;
  funding_lot_id: string;
  scope: "account" | "funding_lot";
  freeze_reason: string;
  status: "open" | "resolved";
  eligibility_status: "active" | "syncing" | "frozen" | "missing" | "source_unavailable";
  opened_at: string;
  resolved_at?: string;
  version: number;
  // CR-0007 problem one: must stay byte-for-byte in sync with the backend
  // Go DTO's key set (eligibilityFreezeDTO) and mapEligibilityFreeze's
  // `allowed` list below -- see exactObjectKeys's callers.
  external_user_id: string;
  // Present only when the account has a verified address on file
  // (XM-INV-LEDGER-ACCOUNT-EMAIL); the backend omits the key otherwise.
  account_email?: string;
  // Which piece of evidence opened the freeze (XM-INV-FREEZE-TRIGGER-VISIBLE).
  // Omitted for a row that predates the columns carrying a value.
  trigger_object_type?: string;
  trigger_object_id?: string;
};

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW): must stay byte-for-byte in sync with
// the backend's accountLedgerListDTO/accountLedgerDetailDTO (httpapi/
// accounts_ledger.go) -- see exactObjectKeys's callers below.
type BackendAccountLedgerListItem = {
  external_account_id: string;
  source_type: "sub2api" | "newapi";
  external_user_id: string;
  policy_start_at: string;
  recharges_since_start_count: number;
  recharges_since_start_minor: number;
  consumed_since_start_minor: number;
  invoiceable_now_minor: number;
  issued_minor: number;
  threshold_reached: boolean;
  block_state: string;
  account_email?: string;
  last_checkpoint_at: string | null;
};

type BackendAccountLedgerRecharge = {
  funding_lot_id: string;
  completed_at: string;
  amount_minor: number;
  eligibility_kind: string;
  refund_frozen: boolean;
};

type BackendAccountLedgerConsumptionDay = {
  date: string;
  consumed_minor: number;
};

type BackendAccountLedgerDetail = BackendAccountLedgerListItem & {
  // 老后端没有这个字段时按空串处理（可选），不让整个详情拒绝解析。
  cutover_at?: string;
  opening_balance_units: { service_units: string; unit_code: string };
  recharges_since_start: BackendAccountLedgerRecharge[];
  consumption_timeline: BackendAccountLedgerConsumptionDay[];
  last_reconciled_at: string | null;
  block_reason: string | null;
};

type BackendSourceHealth = {
  ready: boolean;
  items: Array<{
    source_instance_id: string;
    source_type: "sub2api" | "newapi";
    source_name: string;
    source_enabled: boolean;
    stream_id: "payments" | "identities" | "usage" | "credits" | "balances";
    sequence: number;
    approved_runtime_version: string;
    cutover_runtime_version?: string;
    observed_runtime_version: string;
    observed_agent_version: string;
    projection_status: "healthy" | "blocked" | "unknown";
    last_accepted_at?: string;
    last_nonempty_batch_at?: string;
    economic_watermark_at?: string;
    maximum_age_seconds: number;
    economic_watermark_maximum_age_seconds?: number;
    pending_events: number;
    dead_events: number;
    // XM-INV-DEAD-CONTAINMENT. Optional: a server predating that slice omits
    // the field entirely, and the screen must render zero rather than NaN.
    contained_dead_events?: number;
    waiting_dependencies: number;
    ready: boolean;
    reasons: string[];
  }>;
  // XM-INV-PROJECTION-FAILURE-GRADING added this block; a server from before
  // that slice omits it, which is why it is optional here.
  eligibility_projection?: {
    queued: number;
    processing: number;
    retrying: number;
    dead: number;
    proof_pending: number;
    oldest_pending?: string;
    oldest_proof_pending?: string;
  };
};

type BackendPaymentCandidate = {
  id: string;
  source_type: "newapi";
  principal_id: string;
  principal_display_name?: string;
  source_instance_id: string;
  source_order_id: string;
  display_reference: string;
  quoted_minor: number;
  current_cap_minor: number;
  currency: string;
	verification: "pending" | "frozen" | "verified";
	manual_review_stage?: string;
  source_status: string;
  observed_at: string;
};

type BackendRefundCase = {
  id: string;
  request_id: string;
  funding_lot_id: string;
  source_revision: string;
  observed_refund_minor: number;
  issued_exposure_minor: number;
  observed_cap_minor: number;
  status: "open" | "resolved_red_letter" | "resolved_no_action";
  resolved_by?: string;
  opened_at: string;
  updated_at: string;
  resolved_at?: string;
};

export type BackendFundingLot = {
  id: string;
  source: "sub2api" | "newapi";
  source_instance_id: string;
  source_label: string;
  display_reference: string;
  completed_at: string;
  original_paid_minor: number;
  consumed_cash_minor: number;
  reserved_minor: number;
  issued_minor: number;
  available_minor: number;
  eligibility_kind: "wallet" | "subscription" | "legacy" | "noncash";
  eligibility_status:
    | "active"
    | "syncing"
    | "frozen"
    | "missing"
    | "source_unavailable";
  reason_code?:
    | "SOURCE_REFUND"
    | "LEDGER_SYNCING"
    | "LEDGER_FROZEN"
    | "SOURCE_NOT_READY"
    | "BEFORE_ELIGIBILITY_START"
    | "NO_POST_START_CONSUMPTION"
    | "SUBSCRIPTION_USAGE_UNSUPPORTED";
  verification: "pending" | "verified" | "frozen";
  refund_frozen: boolean;
};

export type BackendProfile = {
  id: string;
  principal_id: string;
  type: "personal" | "enterprise";
  title: string;
  tax_id?: string;
  email: string;
  email_verified: boolean;
  address?: string;
  phone?: string;
  bank_name?: string;
  bank_account?: string;
  is_default: boolean;
  revision: number;
};

type BackendAllocation = {
  funding_lot_id: string;
  external_order_id?: string;
  amount_minor: number;
};

type BackendInvoiceRequest = {
  id: string;
  request_no: string;
  principal_id: string;
  source_instance_id: string;
  source_type: "sub2api" | "newapi";
  currency: string;
  amount_minor: number;
  status:
    | "pending_review"
    | "needs_changes"
    | "approved"
    | "rejected"
    | "user_cancelled"
    | "manual_issuing"
    | "issued_awaiting_document"
    | "issued"
    | "refund_attention";
  profile: Omit<
    BackendProfile,
    "id" | "principal_id" | "email_verified" | "is_default"
  >;
  allocations: BackendAllocation[];
  review_note?: string;
  reviewed_by?: string;
  issued_by?: string;
  version: number;
  submitted_at: string;
  updated_at: string;
  principal_display_name?: string;
  principal_email?: string;
  payment_verification?: "not_required" | "pending" | "passed" | "failed";
};

export type BackendSystemSettings = {
  revision: number;
  issuer_configured: boolean;
  issuer_name: string;
  service_item: "技术服务";
  minimum_request_minor: number;
  eligibility_start_at: string;
  eligibility_policy_version: number;
  eligibility_timezone: "Asia/Shanghai";
  eligibility_rule: "payment_and_usage_at_or_after";
  smtp: {
    from_address: string;
    from_name: string;
    host: string;
    port: number;
    starttls: boolean;
    credential_configured: boolean;
    test_recipient_masked: string;
    test_recipient_managed?: boolean;
  };
  admin_access: {
    cidrs: string[];
    current_ip: string;
  };
  notice_webhook?: {
    configured?: boolean;
    fingerprint?: string;
    updated_by?: string;
    updated_at?: string | null;
  };
};

export type BackendInvoicePolicy = {
  minimum_request_minor: number;
  service_item: "技术服务";
  eligibility_start_at: string;
  eligibility_policy_version: number;
  eligibility_timezone: "Asia/Shanghai";
  eligibility_rule: "payment_and_usage_at_or_after";
};

const baseUrl = (import.meta.env.VITE_API_BASE_URL || "").replace(/\/$/, "");
const requestTimeoutMs = 15_000;
export const requiredEligibilityStartAt = "2026-08-31T16:00:00Z";
let sessionCSRFToken = "";
const documentUploadCheckpoints = new Map<string, number>();

if (
  import.meta.env.PROD &&
  baseUrl &&
  new URL(baseUrl, window.location.origin).origin !== window.location.origin
) {
  throw new Error(
    "VITE_API_BASE_URL must be same-origin in production; proxy /api at the invoice origin.",
  );
}

function endpointURL(path: string) {
  return `${baseUrl}${path}`;
}

function isMutation(method: string) {
  return !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
}

function csrfTokenForMutation() {
  if (sessionCSRFToken) return sessionCSRFToken;
  publishAuthFailure("login");
  throw new InvoiceApiError("安全会话尚未建立，请重新登录后再试。", {
    code: "CSRF_TOKEN_MISSING",
    requiresLogin: true,
  });
}

function isStepUpCode(code: string) {
  const normalized = code.toUpperCase();
  return (
    normalized.includes("STEP_UP") ||
    normalized.includes("MFA") ||
    normalized.includes("REAUTH")
  );
}

function responseError(
  status: number,
  payload: BackendError | undefined,
  fallback = "",
) {
  const code = payload?.error?.code ?? `HTTP_${status}`;
  const serverMessage = payload?.error?.message ?? fallback;
  const requiresLogin = status === 401;
  const requiresStepUp = status === 403 && isStepUpCode(code);
  if (requiresLogin) publishAuthFailure("login");
  if (requiresStepUp) publishAuthFailure("step-up");
  return new InvoiceApiError(
    requiresStepUp
      ? "此管理员操作需要重新验证身份。"
      : friendlyError(status, code, serverMessage),
    {
      code,
      status,
      retryable: status >= 500,
      requiresLogin,
      requiresStepUp,
    },
  );
}

// CR-0007 problem three: these four resolve-precondition codes each get a
// specific, correctly-actionable Chinese sentence instead of falling into
// the generic 409/5xx branches below (which read as "refresh and retry" --
// misleading when the server correctly blocked an unsafe resolution, since
// retrying does nothing until the real underlying condition changes). Every
// other status/code keeps the exact fallback behavior it had before.
function friendlyError(status: number, code: string, message: string) {
  if (code === "ELIGIBILITY_SOURCE_STALE")
    return "来源数据尚未同步新鲜，暂时无法判定是否可以安全解冻，请稍后重试。";
  if (code === "ELIGIBILITY_PROJECTION_PENDING")
    return "该账号存在尚未完成的资格重算任务，需等待任务结束后才能解冻。";
  if (code === "ELIGIBILITY_REFUND_EXPOSED")
    return "该账号存在未结案的退款或红冲风险，须先在退款与红冲队列处理后才能解冻。";
  if (code === "ELIGIBILITY_EVALUATION_UNMATCHED")
    return "最新余额对账结论尚未匹配，暂不满足安全解冻条件。";
  // CR-0009 (XM-INV-CR0009-LEDGER-VIEW): the account ledger detail endpoint
  // is this app's first GET-by-id consumer of the shared NOT_FOUND code --
  // added here rather than only in that one caller so every existing and
  // future 404 gets this same, correctly-actionable Chinese sentence
  // instead of the server's own raw (English) error text falling through
  // to the generic `message ||` fallback below.
  if (code === "NOT_FOUND") return "未找到该记录，可能已被删除或地址有误。";
  if (status === 401) return "登录状态已失效，请重新登录。";
  if (status === 403) return "当前账号没有执行此操作的权限。";
  if (status === 409) return "数据已经发生变化，请刷新后重试。";
  if (status === 422) return message || "提交的数据未通过业务校验。";
  if (status >= 500) return "开票服务暂时不可用，请稍后重试。";
  return message || "请求失败，请检查输入后重试。";
}

function requireSafeMinor(value: number, field: string) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new InvoiceApiError(`服务返回的${field}无效，已停止处理。`, {
      code: "INVALID_MONEY_RESPONSE",
    });
  }
  return value;
}

const eligibilitySummaryReasons = [
  "BINDING_NOT_VERIFIED",
  "ACCOUNT_FROZEN",
  "PROJECTION_PENDING",
  "SOURCE_NOT_READY",
  "NO_CONSUMED_CASH",
  "READY",
] as const;

// The known, labeled freeze reasons -- used to validate the `reason` filter
// query param (an operator can only ask to filter by a reason this UI
// actually knows how to label) and by mock-api.ts. Not used to validate the
// server's own freeze_reason on a *response* item any more -- see
// mapEligibilityFreeze's own freezeReasonPattern check below for why an
// eleventh-hour backend addition (this array was missing EVENT_DEAD/
// POLICY_ANCHOR_BLOCKED, both added by migration 0016 before this array was
// last updated) must not fail the whole list.
const eligibilityFreezeReasons = [
  "UNKNOWN_NEGATIVE_BALANCE",
  "LATE_FINALIZED_EVENT",
  "AMBIGUOUS_EVENT_ORDER",
  "EVENT_PAYLOAD_DRIFT",
  "UNIT_MISMATCH",
  "USAGE_EXCEEDS_LEDGER",
  "STREAM_WATERMARK_REGRESSION",
  "SOURCE_GAP",
  "SOURCE_REFUND",
  "EVENT_DEAD",
  "POLICY_ANCHOR_BLOCKED",
] as const;

// A freeze_reason on a response item only needs to be a well-formed,
// enum-shaped code (matching this codebase's own Go constant naming
// convention) -- not a member of the closed eligibilityFreezeReasons list
// above. This is deliberately looser than that list: a reason the backend's
// own CHECK constraint added before this frontend's known-reasons list was
// updated (exactly what happened with EVENT_DEAD/POLICY_ANCHOR_BLOCKED)
// must still render, with App.tsx's own label lookup falling back to the
// raw code, rather than throwing and blanking the entire "资格冻结" list
// over one unrecognized row.
const freezeReasonPattern = /^[A-Z][A-Z0-9_]{0,62}$/;

const eligibilityStatuses = [
  "active",
  "syncing",
  "frozen",
  "missing",
  "source_unavailable",
] as const;

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW), plus "settling"
// (XM-INV-LEDGER-SETTLING-STATE) -- see docs/ELIGIBILITY-OPERATIONS.md's
// "管理员账本视图" section.
const accountBlockStates = [
  "frozen_manual_review",
  "not_invoiceable_pending_reconciliation",
  "settling",
  "below_threshold",
  "invoiceable",
] as const;

const accountEligibilityKinds = ["WALLET_CASH", "SUBSCRIPTION_CASH"] as const;

const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const serviceUnitsPattern = /^(0|[1-9][0-9]{0,77})$/;

function exactObjectKeys(
  value: unknown,
  allowed: readonly string[],
  required: readonly string[],
  field: string,
): asserts value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new InvoiceApiError(`服务返回的${field}格式无效，已停止显示。`, {
      code: "INVALID_ELIGIBILITY_RESPONSE",
    });
  }
  const keys = Object.keys(value);
  if (
    keys.some((key) => !allowed.includes(key)) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key))
  ) {
    throw new InvoiceApiError(`服务返回的${field}字段超出安全白名单。`, {
      code: "INVALID_ELIGIBILITY_RESPONSE",
    });
  }
}

function validTimestamp(value: unknown) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 64 &&
    !/[\r\n\0]/.test(value) &&
    Number.isFinite(Date.parse(value))
  );
}

function fixedSourceLabel(source: "sub2api" | "newapi") {
  return source === "sub2api" ? "SoloV API" : "SoloV 模型平台";
}

function mapServiceUnitSummary(
  value: BackendServiceUnitSummary,
  source: "sub2api" | "newapi",
): UserEligibilitySummary["noncash"] {
  exactObjectKeys(
    value,
    ["service_units", "unit_code"],
    ["service_units", "unit_code"],
    "源服务单位",
  );
  if (
    typeof value.service_units !== "string" ||
    !serviceUnitsPattern.test(value.service_units) ||
    typeof value.unit_code !== "string"
  ) {
    throw new InvoiceApiError("服务返回的源服务单位无效，已停止显示。", {
      code: "INVALID_SERVICE_UNITS_RESPONSE",
    });
  }
  const expectedUnit =
    source === "sub2api" ? "SUB2_BALANCE_1E8" : "NEWAPI_QUOTA";
  if (value.unit_code === "" && value.service_units === "0") {
    return { serviceUnits: "0", unitCode: null };
  }
  if (value.unit_code !== expectedUnit) {
    throw new InvoiceApiError("源服务单位合同不匹配，已停止显示。", {
      code: "UNIT_CONTRACT_MISMATCH",
    });
  }
  return { serviceUnits: value.service_units, unitCode: expectedUnit };
}

function mapEligibilitySummary(value: BackendEligibilitySummary) {
  exactObjectKeys(
    value,
    [
      "source_instance_id",
      "source_type",
      "source_name",
      "binding_status",
      "status",
      "currency",
      "available_minor",
      "consumed_minor",
      "unconsumed_minor",
      "reserved_minor",
      "issued_minor",
      "legacy_noninvoiceable",
      "noncash",
      "reasons",
    ],
    [
      "source_instance_id",
      "source_type",
      "source_name",
      "binding_status",
      "status",
      "currency",
      "available_minor",
      "consumed_minor",
      "unconsumed_minor",
      "reserved_minor",
      "issued_minor",
      "legacy_noninvoiceable",
      "noncash",
      "reasons",
    ],
    "开票资格摘要",
  );
  if (
    !uuidPattern.test(value.source_instance_id) ||
    !["sub2api", "newapi"].includes(value.source_type) ||
    typeof value.source_name !== "string" ||
    value.source_name.length > 160 ||
    !["pending", "verified", "frozen", "revoked"].includes(
      value.binding_status,
    ) ||
    !eligibilityStatuses.includes(value.status) ||
    value.currency !== "CNY" ||
    !Array.isArray(value.reasons) ||
    value.reasons.length === 0 ||
    value.reasons.length > eligibilitySummaryReasons.length ||
    !value.reasons.every(
      (reason) =>
        typeof reason === "string" &&
        eligibilitySummaryReasons.includes(
          reason as (typeof eligibilitySummaryReasons)[number],
        ),
    ) ||
    new Set(value.reasons).size !== value.reasons.length
  ) {
    throw new InvoiceApiError("开票资格摘要包含未识别状态，已停止显示。", {
      code: "INVALID_ELIGIBILITY_RESPONSE",
    });
  }
  const availableMinor = requireSafeMinor(value.available_minor, "可开票金额");
  const consumedMinor = requireSafeMinor(value.consumed_minor, "已消费现金金额");
  const unconsumedMinor = requireSafeMinor(
    value.unconsumed_minor,
    "未消费现金金额",
  );
  const reservedMinor = requireSafeMinor(value.reserved_minor, "占用金额");
  const issuedMinor = requireSafeMinor(value.issued_minor, "已开票金额");
  const reasons = value.reasons as UserEligibilitySummary["reasons"];
  const ready = reasons.includes("READY");
  if (
    reservedMinor + issuedMinor > consumedMinor ||
    availableMinor > consumedMinor ||
    (ready &&
      (reasons.length !== 1 ||
        value.status !== "active" ||
        value.binding_status !== "verified")) ||
    (!ready && availableMinor !== 0) ||
    (value.status === "source_unavailable" &&
      !reasons.includes("SOURCE_NOT_READY"))
  ) {
    throw new InvoiceApiError("开票资格金额或安全状态不一致，已停止显示。", {
      code: "INCONSISTENT_ELIGIBILITY_RESPONSE",
    });
  }
  return {
    source: value.source_type,
    sourceInstanceId: value.source_instance_id,
    sourceLabel: fixedSourceLabel(value.source_type),
    bindingStatus: value.binding_status,
    status: value.status,
    currency: "CNY",
    availableMinor,
    consumedMinor,
    unconsumedMinor,
    reservedMinor,
    issuedMinor,
    legacyNoninvoiceable: mapServiceUnitSummary(
      value.legacy_noninvoiceable,
      value.source_type,
    ),
    noncash: mapServiceUnitSummary(value.noncash, value.source_type),
    reasons,
  } satisfies UserEligibilitySummary;
}

function mapEligibilityFreeze(value: BackendEligibilityFreeze) {
  const allowed = [
    "id",
    "principal_id",
    "source_instance_id",
    "source_type",
    "source_name",
    "funding_lot_id",
    "scope",
    "freeze_reason",
    "status",
    "eligibility_status",
    "opened_at",
    "resolved_at",
    "version",
    "external_user_id",
    "account_email",
    "trigger_object_type",
    "trigger_object_id",
  ] as const;
  const optional = new Set<string>([
    "resolved_at",
    "account_email",
    "trigger_object_type",
    "trigger_object_id",
  ]);
  exactObjectKeys(
    value,
    allowed,
    allowed.filter((key) => !optional.has(key)),
    "资格冻结记录",
  );
  if (
    !uuidPattern.test(value.id) ||
    !uuidPattern.test(value.principal_id) ||
    !uuidPattern.test(value.source_instance_id) ||
    !["sub2api", "newapi"].includes(value.source_type) ||
    typeof value.source_name !== "string" ||
    value.source_name.length > 160 ||
    !["account", "funding_lot"].includes(value.scope) ||
    typeof value.funding_lot_id !== "string" ||
    (value.scope === "account" && value.funding_lot_id !== "") ||
    (value.scope === "funding_lot" && !uuidPattern.test(value.funding_lot_id)) ||
    typeof value.freeze_reason !== "string" ||
    !freezeReasonPattern.test(value.freeze_reason) ||
    !["open", "resolved"].includes(value.status) ||
    !eligibilityStatuses.includes(value.eligibility_status) ||
    !validTimestamp(value.opened_at) ||
    !Number.isSafeInteger(value.version) ||
    value.version <= 0 ||
    (value.status === "open" && value.resolved_at !== undefined) ||
    (value.status === "resolved" && !validTimestamp(value.resolved_at)) ||
    typeof value.external_user_id !== "string" ||
    value.external_user_id.length === 0 ||
    value.external_user_id.length > 512 ||
    /[\r\n\0]/.test(value.external_user_id) ||
    !validOptionalAccountEmail(value.account_email)
  ) {
    throw new InvoiceApiError("资格冻结记录包含无效字段，已停止显示。", {
      code: "INVALID_ELIGIBILITY_FREEZE_RESPONSE",
    });
  }
  return {
    id: value.id,
    source: value.source_type,
    sourceInstanceId: value.source_instance_id,
    sourceLabel: fixedSourceLabel(value.source_type),
    scope: value.scope,
    reason: value.freeze_reason,
    status: value.status,
    eligibilityStatus: value.eligibility_status,
    openedAt: value.opened_at,
    resolvedAt: value.resolved_at,
    version: value.version,
    externalUserId: value.external_user_id,
    accountEmail: value.account_email,
    triggerObjectType: value.trigger_object_type,
    triggerObjectId: value.trigger_object_id,
  } satisfies EligibilityFreeze;
}

function eligibilityFreezeCursor(cursor: string) {
  try {
    const decoded = JSON.parse(cursor) as unknown;
    exactObjectKeys(
      decoded,
      ["beforeOpenedAt", "beforeId"],
      ["beforeOpenedAt", "beforeId"],
      "资格冻结分页游标",
    );
    if (!validTimestamp(decoded.beforeOpenedAt) || !uuidPattern.test(String(decoded.beforeId))) {
      throw new Error("invalid cursor values");
    }
    return {
      beforeOpenedAt: String(decoded.beforeOpenedAt),
      beforeId: String(decoded.beforeId),
    };
  } catch (error) {
    if (error instanceof InvoiceApiError) throw error;
    throw new InvoiceApiError("资格冻结分页游标无效。", {
      code: "INVALID_CURSOR",
    });
  }
}

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW) -------------------------------------

const accountLedgerListKeys = [
  "external_account_id",
  "source_type",
  "external_user_id",
  "policy_start_at",
  "recharges_since_start_count",
  "recharges_since_start_minor",
  "consumed_since_start_minor",
  "invoiceable_now_minor",
  "issued_minor",
  "threshold_reached",
  "block_state",
  "last_checkpoint_at",
] as const;

// account_email is optional on the wire (absent when the account has no
// verified address on file), so it is allowed but never required -- both key
// sets below admit it the same way (XM-INV-LEDGER-ACCOUNT-EMAIL).
const accountLedgerOptionalKeys = ["account_email"] as const;

// validOptionalAccountEmail accepts an absent key and otherwise applies the
// same shape bound the other free-text fields get: a non-empty, bounded,
// control-character-free string. It deliberately does not validate the
// address itself -- the server already canonicalized it, and a stricter
// client-side pattern could only ever reject a legitimate address.
function validOptionalAccountEmail(value: unknown): boolean {
  if (value === undefined) return true;
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 320 &&
    !/[\r\n\0]/.test(value)
  );
}

// Shared by mapAccountLedgerListItem (which first checks the narrower list
// key set) and mapAccountLedgerDetail (which already checked its own wider
// key set) -- validates and converts the list-shaped fields alone, so
// neither caller re-runs exactObjectKeys against the wrong key set.
function accountLedgerListFieldsToItem(
  value: BackendAccountLedgerListItem,
): AccountLedgerListItem {
  if (
    !uuidPattern.test(value.external_account_id) ||
    !["sub2api", "newapi"].includes(value.source_type) ||
    typeof value.external_user_id !== "string" ||
    value.external_user_id.length === 0 ||
    value.external_user_id.length > 512 ||
    /[\r\n\0]/.test(value.external_user_id) ||
    !validTimestamp(value.policy_start_at) ||
    !Number.isSafeInteger(value.recharges_since_start_count) ||
    value.recharges_since_start_count < 0 ||
    typeof value.threshold_reached !== "boolean" ||
    !accountBlockStates.includes(value.block_state as AccountBlockState) ||
    (value.last_checkpoint_at !== null && !validTimestamp(value.last_checkpoint_at)) ||
    !validOptionalAccountEmail(value.account_email)
  ) {
    throw new InvoiceApiError("用户账本记录包含无效字段，已停止显示。", {
      code: "INVALID_ACCOUNT_LEDGER_RESPONSE",
    });
  }
  return {
    externalAccountId: value.external_account_id,
    source: value.source_type,
    externalUserId: value.external_user_id,
    accountEmail: value.account_email,
    policyStartAt: value.policy_start_at,
    rechargesSinceStartCount: value.recharges_since_start_count,
    rechargesSinceStartMinor: requireSafeMinor(
      value.recharges_since_start_minor,
      "起点后充值金额",
    ),
    consumedSinceStartMinor: requireSafeMinor(
      value.consumed_since_start_minor,
      "起点后消耗金额",
    ),
    invoiceableNowMinor: requireSafeMinor(value.invoiceable_now_minor, "可开票金额"),
    issuedMinor: requireSafeMinor(value.issued_minor, "已开票金额"),
    thresholdReached: value.threshold_reached,
    blockState: value.block_state as AccountBlockState,
    lastCheckpointAt: value.last_checkpoint_at ?? undefined,
  } satisfies AccountLedgerListItem;
}

function mapAccountLedgerListItem(
  value: BackendAccountLedgerListItem,
): AccountLedgerListItem {
  exactObjectKeys(
    value,
    [...accountLedgerListKeys, ...accountLedgerOptionalKeys],
    accountLedgerListKeys,
    "用户账本列表项",
  );
  return accountLedgerListFieldsToItem(value);
}

function mapAccountLedgerRecharge(
  value: BackendAccountLedgerRecharge,
): AccountLedgerRecharge {
  const allowed = [
    "funding_lot_id",
    "completed_at",
    "amount_minor",
    "eligibility_kind",
    "refund_frozen",
  ] as const;
  exactObjectKeys(value, allowed, allowed, "用户账本充值明细");
  if (
    !uuidPattern.test(value.funding_lot_id) ||
    !validTimestamp(value.completed_at) ||
    !accountEligibilityKinds.includes(
      value.eligibility_kind as (typeof accountEligibilityKinds)[number],
    ) ||
    typeof value.refund_frozen !== "boolean"
  ) {
    throw new InvoiceApiError("用户账本充值明细包含无效字段，已停止显示。", {
      code: "INVALID_ACCOUNT_LEDGER_RESPONSE",
    });
  }
  return {
    fundingLotId: value.funding_lot_id,
    completedAt: value.completed_at,
    amountMinor: requireSafeMinor(value.amount_minor, "充值金额"),
    eligibilityKind: value.eligibility_kind as AccountLedgerRecharge["eligibilityKind"],
    refundFrozen: value.refund_frozen,
  } satisfies AccountLedgerRecharge;
}

const accountLedgerDatePattern = /^\d{4}-\d{2}-\d{2}$/;

function mapAccountLedgerConsumptionDay(
  value: BackendAccountLedgerConsumptionDay,
): AccountLedgerConsumptionDay {
  exactObjectKeys(value, ["date", "consumed_minor"], ["date", "consumed_minor"], "用户账本消耗时间线");
  if (
    typeof value.date !== "string" ||
    !accountLedgerDatePattern.test(value.date)
  ) {
    throw new InvoiceApiError("用户账本消耗时间线包含无效字段，已停止显示。", {
      code: "INVALID_ACCOUNT_LEDGER_RESPONSE",
    });
  }
  return {
    date: value.date,
    consumedMinor: requireSafeMinor(value.consumed_minor, "当日消耗金额"),
  } satisfies AccountLedgerConsumptionDay;
}

function mapAccountLedgerDetail(
  value: BackendAccountLedgerDetail,
): AccountLedgerDetail {
  const allowed = [
    ...accountLedgerListKeys,
    ...accountLedgerOptionalKeys,
    "opening_balance_units",
    "recharges_since_start",
    "consumption_timeline",
    "last_reconciled_at",
    "block_reason",
  ] as const;
  exactObjectKeys(
    value,
    allowed,
    allowed.filter((key) => key !== "account_email"),
    "用户账本详情",
  );
  const listItem = accountLedgerListFieldsToItem(value);
  exactObjectKeys(
    value.opening_balance_units,
    ["service_units", "unit_code"],
    ["service_units", "unit_code"],
    "用户账本期初余额",
  );
  const expectedUnit =
    value.source_type === "sub2api" ? "SUB2_BALANCE_1E8" : "NEWAPI_QUOTA";
  if (
    typeof value.opening_balance_units.service_units !== "string" ||
    !serviceUnitsPattern.test(value.opening_balance_units.service_units) ||
    value.opening_balance_units.unit_code !== expectedUnit ||
    !Array.isArray(value.recharges_since_start) ||
    !Array.isArray(value.consumption_timeline) ||
    (value.last_reconciled_at !== null && !validTimestamp(value.last_reconciled_at)) ||
    (value.block_reason !== null &&
      (typeof value.block_reason !== "string" ||
        value.block_reason.length === 0 ||
        value.block_reason.length > 4000))
  ) {
    throw new InvoiceApiError("用户账本详情包含无效字段，已停止显示。", {
      code: "INVALID_ACCOUNT_LEDGER_RESPONSE",
    });
  }
  // A blocked account must carry a concrete reason, and an invoiceable one
  // must not -- see buildAccountBlockReason's own doc comment on the
  // backend side for why "invoiceable" is the only null-block_reason state.
  if ((listItem.blockState === "invoiceable") !== (value.block_reason === null)) {
    throw new InvoiceApiError("用户账本阻断原因与状态不一致，已停止显示。", {
      code: "INCONSISTENT_ACCOUNT_LEDGER_RESPONSE",
    });
  }
  return {
    ...listItem,
    cutoverAt: String(value.cutover_at ?? ""),
    openingBalance: {
      serviceUnits: value.opening_balance_units.service_units,
      unitCode: value.opening_balance_units.unit_code,
    },
    recharges: value.recharges_since_start.map((item) =>
      mapAccountLedgerRecharge(item),
    ),
    consumptionTimeline: value.consumption_timeline.map((item) =>
      mapAccountLedgerConsumptionDay(item),
    ),
    blockReason: value.block_reason ?? undefined,
    lastReconciledAt: value.last_reconciled_at ?? undefined,
  } satisfies AccountLedgerDetail;
}

function accountLedgerCursor(cursor: string) {
  try {
    const decoded = JSON.parse(cursor) as unknown;
    exactObjectKeys(
      decoded,
      ["beforeInvoiceableMinor", "beforeId"],
      ["beforeId"],
      "用户账本分页游标",
    );
    if (
      !uuidPattern.test(String(decoded.beforeId)) ||
      (decoded.beforeInvoiceableMinor !== undefined &&
        decoded.beforeInvoiceableMinor !== null &&
        !Number.isSafeInteger(decoded.beforeInvoiceableMinor))
    ) {
      throw new Error("invalid cursor values");
    }
    return {
      beforeInvoiceableMinor:
        typeof decoded.beforeInvoiceableMinor === "number"
          ? decoded.beforeInvoiceableMinor
          : undefined,
      beforeId: String(decoded.beforeId),
    };
  } catch (error) {
    if (error instanceof InvoiceApiError) throw error;
    throw new InvoiceApiError("用户账本分页游标无效。", {
      code: "INVALID_CURSOR",
    });
  }
}

async function requestJSON<T>(
  path: string,
  options: {
    method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
    role?: RequestRole;
    body?: unknown;
    headers?: Record<string, string>;
    // Only for the pre-session platform-login endpoints, which by
    // definition cannot hold a synchronizer CSRF token yet.
    skipCSRF?: boolean;
  } = {},
): Promise<T> {
  const headers = new Headers(options.headers);
  const method = options.method ?? "GET";
  headers.set("Accept", "application/json");
  if (options.body !== undefined)
    headers.set("Content-Type", "application/json");

  if (isMutation(method) && !options.skipCSRF) {
    headers.set("X-CSRF-Token", csrfTokenForMutation());
  }
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), requestTimeoutMs);

  try {
    const response = await fetch(endpointURL(path), {
      method,
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "include",
      redirect: "error",
      signal: controller.signal,
    });
    const rotatedCSRF = response.headers.get("X-CSRF-Token");
    if (rotatedCSRF) sessionCSRFToken = rotatedCSRF;
    const contentType = response.headers.get("content-type") ?? "";
    const payload = contentType.includes("application/json")
      ? ((await response.json()) as T & BackendError)
      : undefined;
    if (!response.ok) {
      throw responseError(response.status, payload);
    }
    if (response.status === 204) return undefined as T;
    if (payload === undefined) {
      throw new InvoiceApiError("服务返回了无法识别的数据格式。", {
        code: "INVALID_RESPONSE",
      });
    }
    return payload;
  } catch (error) {
    if (error instanceof InvoiceApiError) throw error;
    if (error instanceof DOMException && error.name === "AbortError") {
      throw new InvoiceApiError("请求超时，请检查服务状态后重试。", {
        code: "TIMEOUT",
        retryable: true,
      });
    }
    throw new InvoiceApiError("无法连接开票服务，请确认后端已经启动。", {
      code: "NETWORK_ERROR",
      retryable: true,
    });
  } finally {
    window.clearTimeout(timeout);
  }
}

function mapVerification(
  value: BackendFundingLot["verification"],
): VerificationState {
  if (value === "verified") return "verified";
  if (value === "frozen") return "attention";
  return "pending";
}

function availableMinor(lot: BackendFundingLot) {
  requireSafeMinor(lot.original_paid_minor, "实际支付金额");
  requireSafeMinor(lot.consumed_cash_minor, "已消费现金金额");
  requireSafeMinor(lot.reserved_minor, "占用金额");
  requireSafeMinor(lot.issued_minor, "已开票金额");
  requireSafeMinor(lot.available_minor, "可开票金额");
  const canInvoice =
    (lot.eligibility_kind === "wallet" ||
      lot.eligibility_kind === "subscription") &&
    lot.eligibility_status === "active" &&
    lot.verification === "verified" &&
    !lot.refund_frozen;
  const expectedAvailable = canInvoice
    ? Math.max(0, lot.consumed_cash_minor - lot.reserved_minor - lot.issued_minor)
    : 0;
  if (
    lot.consumed_cash_minor > lot.original_paid_minor ||
    lot.reserved_minor + lot.issued_minor > lot.consumed_cash_minor ||
    lot.available_minor !== expectedAvailable
  ) {
    throw new InvoiceApiError("开票金额账本响应不一致，已停止显示。", {
      code: "INCONSISTENT_ELIGIBILITY_RESPONSE",
    });
  }
  return lot.available_minor;
}

export function mapLot(lot: BackendFundingLot): FundingOrder {
  if (
    ![
      "active",
      "syncing",
      "frozen",
      "missing",
      "source_unavailable",
    ].includes(
      lot.eligibility_status,
    ) ||
    (lot.reason_code !== undefined &&
      ![
        "SOURCE_REFUND",
        "LEDGER_SYNCING",
        "LEDGER_FROZEN",
        "SOURCE_NOT_READY",
        "BEFORE_ELIGIBILITY_START",
        "NO_POST_START_CONSUMPTION",
        "SUBSCRIPTION_USAGE_UNSUPPORTED",
      ].includes(lot.reason_code)) ||
    (lot.eligibility_status === "source_unavailable" &&
      lot.reason_code !== "SOURCE_NOT_READY")
  ) {
    throw new InvoiceApiError("充值记录包含无效的资金账本状态。", {
      code: "INVALID_ELIGIBILITY_STATUS",
    });
  }
  const available = availableMinor(lot);
  return {
    id: lot.id,
    source: lot.source,
    sourceInstanceId: lot.source_instance_id,
    sourceLabel: lot.source_label,
    tradeNo: lot.display_reference,
    paidAt: lot.completed_at,
    paidMinor: lot.original_paid_minor,
    consumedCashMinor: lot.consumed_cash_minor,
    reservedMinor: lot.reserved_minor,
    issuedMinor: lot.issued_minor,
    availableMinor: available,
    currency: "CNY",
    eligibilityKind: lot.eligibility_kind,
    eligibilityStatus: lot.eligibility_status,
    reasonCode: lot.reason_code,
    verification: mapVerification(lot.verification),
    refundFrozen: lot.refund_frozen,
    paymentMethod: "支付凭证已核验",
    description:
      lot.reason_code === "BEFORE_ELIGIBILITY_START"
        ? "开票生效日前充值（不可开票）"
        : lot.reason_code === "SUBSCRIPTION_USAGE_UNSUPPORTED"
        ? "订阅消费暂缺可核验关联证据（不可开票）"
        : lot.reason_code === "NO_POST_START_CONSUMPTION"
        ? "尚无开票生效日后的真实消费（暂不可开票）"
        : lot.eligibility_kind === "subscription"
        ? "订阅套餐支付"
        : "钱包充值（按已消费现金开票）",
  };
}

export function mapInvoicePolicy(policy: BackendInvoicePolicy): InvoicePolicy {
  if (
    policy.eligibility_start_at !== requiredEligibilityStartAt ||
    !Number.isSafeInteger(policy.eligibility_policy_version) ||
    policy.eligibility_policy_version !== 1 ||
    policy.eligibility_timezone !== "Asia/Shanghai" ||
    policy.eligibility_rule !== "payment_and_usage_at_or_after"
  ) {
    throw new InvoiceApiError("开票生效时间策略无效，已停止提交。", {
      code: "INVALID_ELIGIBILITY_POLICY",
    });
  }
  return {
    minimumRequestMinor: policy.minimum_request_minor,
    serviceItem: policy.service_item,
    eligibilityStartAt: policy.eligibility_start_at,
    eligibilityPolicyVersion: policy.eligibility_policy_version,
    eligibilityTimezone: policy.eligibility_timezone,
    eligibilityRule: policy.eligibility_rule,
  };
}

export function mapAdminSettings(
  settings: BackendSystemSettings,
): InvoiceSystemSettings {
  if (
    settings.eligibility_start_at !== requiredEligibilityStartAt ||
    !Number.isSafeInteger(settings.eligibility_policy_version) ||
    settings.eligibility_policy_version !== 1 ||
    settings.eligibility_timezone !== "Asia/Shanghai" ||
    settings.eligibility_rule !== "payment_and_usage_at_or_after"
  ) {
    throw new InvoiceApiError("系统开票生效策略无效。", {
      code: "INVALID_ELIGIBILITY_POLICY",
    });
  }
  // 空串是合法的「还没配」——收件人搬进后台之后（XM-INV-SMTP-TEST-RECIPIENT-SETTING）
  // 两个来源都可能为空，那时按钮会被禁用并提示去配置，而不是让整个设置页拒绝解析。
  if (
    typeof settings.smtp.test_recipient_masked !== "string" ||
    (settings.smtp.test_recipient_masked !== "" &&
      !settings.smtp.test_recipient_masked.includes("***@")) ||
    /[\r\n\0]/.test(settings.smtp.test_recipient_masked)
  ) {
    throw new InvoiceApiError("SMTP 测试收件地址配置无效。", {
      code: "INVALID_SMTP_TEST_RECIPIENT",
    });
  }
  return {
    revision: settings.revision,
    issuerConfigured: settings.issuer_configured,
    issuerName: settings.issuer_name,
    serviceItem: settings.service_item,
    minimumRequestMinor: settings.minimum_request_minor,
    eligibilityStartAt: settings.eligibility_start_at,
    eligibilityPolicyVersion: settings.eligibility_policy_version,
    eligibilityTimezone: settings.eligibility_timezone,
    eligibilityRule: settings.eligibility_rule,
    smtp: {
      fromAddress: settings.smtp.from_address,
      fromName: settings.smtp.from_name,
      host: settings.smtp.host,
      port: settings.smtp.port,
      startTLS: settings.smtp.starttls,
      credentialConfigured: settings.smtp.credential_configured,
      testRecipientMasked: settings.smtp.test_recipient_masked,
      testRecipientManaged: settings.smtp.test_recipient_managed === true,
    },
    adminAccess: {
      cidrs: settings.admin_access.cidrs,
      currentIP: settings.admin_access.current_ip,
    },
    // 整块缺失当成"未配置"：老版本后端没有这个字段，那时它确实没配。
    noticeWebhook: {
      configured: settings.notice_webhook?.configured === true,
      fingerprint: settings.notice_webhook?.fingerprint ?? "",
      updatedBy: settings.notice_webhook?.updated_by ?? "",
      updatedAt: settings.notice_webhook?.updated_at ?? null,
    },
  };
}

export function mapProfile(profile: BackendProfile): InvoiceProfile {
  if (!Number.isSafeInteger(profile.revision) || profile.revision <= 0) {
    throw new InvoiceApiError("开票资料版本无效，已停止编辑。", {
      code: "INVALID_PROFILE_REVISION",
    });
  }
  return {
    id: profile.id,
    revision: profile.revision,
    type: profile.type,
    title: profile.title,
    taxId: profile.tax_id ?? "",
    email: profile.email,
    phone: profile.phone,
    address: profile.address,
    bankName: profile.bank_name,
    bankAccount: profile.bank_account,
    isDefault: profile.is_default,
  };
}

export function profileMutationBody(
  profile: Omit<InvoiceProfile, "id"> & { id?: string },
) {
  return {
    id: profile.id,
    revision: profile.revision,
    type: profile.type,
    title: profile.title,
    tax_id: profile.taxId,
    email: profile.email,
    address: profile.address,
    phone: profile.phone,
    bank_name: profile.bankName,
    bank_account: profile.bankAccount,
    is_default: profile.isDefault,
  };
}

export function invoiceDocumentPath(requestId: string, admin = false) {
  const role = admin ? "admin" : "user";
  return `/api/v1/${role}/invoice-requests/${encodeURIComponent(requestId)}/document`;
}

function mapStatus(status: BackendInvoiceRequest["status"]): InvoiceStatus {
  if (status === "pending_review") return "submitted";
  if (
    status === "needs_changes" ||
    status === "rejected" ||
    status === "user_cancelled" ||
    status === "refund_attention"
  )
    return "returned";
  if (status === "issued") return "issued";
  return "reviewing";
}

function profileSnapshot(request: BackendInvoiceRequest): InvoiceProfile {
  return {
    id: `snapshot:${request.id}`,
    revision: request.profile.revision,
    type: request.profile.type,
    title: request.profile.title,
    taxId: request.profile.tax_id ?? "",
    email: request.profile.email,
    phone: request.profile.phone,
    address: request.profile.address,
    bankName: request.profile.bank_name,
    bankAccount: request.profile.bank_account,
    isDefault: false,
  };
}

function mapRequest(
  request: BackendInvoiceRequest,
  lots: Map<string, BackendFundingLot>,
): InvoiceRequest {
  if (request.currency !== "CNY") {
    throw new InvoiceApiError("开票申请包含不支持的币种，已停止显示。", {
      code: "UNSUPPORTED_CURRENCY",
    });
  }
  requireSafeMinor(request.amount_minor, "开票申请金额");
  const allocationTotal = request.allocations.reduce(
    (total, allocation) =>
      total + requireSafeMinor(allocation.amount_minor, "订单分配金额"),
    0,
  );
  if (
    !Number.isSafeInteger(allocationTotal) ||
    allocationTotal !== request.amount_minor
  ) {
    throw new InvoiceApiError("开票申请金额与订单分配快照不一致。", {
      code: "INCONSISTENT_ALLOCATION_RESPONSE",
    });
  }
  const verifications = request.allocations.map(
    (allocation) => lots.get(allocation.funding_lot_id)?.verification,
  );
  const newApiVerification = request.payment_verification
    ? request.payment_verification
    : request.source_type !== "newapi"
      ? "not_required"
      : verifications.length > 0 &&
          verifications.every((value) => value === "verified")
        ? "passed"
        : verifications.some((value) => value === "frozen")
          ? "failed"
          : "pending";
  return {
    id: request.id,
    requestNo: request.request_no,
    userName: request.principal_display_name || request.principal_id,
    userEmail: request.principal_email || request.profile.email,
    source: request.source_type,
    sourceInstanceId: request.source_instance_id,
    version: request.version,
    amountMinor: request.amount_minor,
    profileSnapshot: profileSnapshot(request),
    allocations: request.allocations.map((allocation) => ({
      orderId: allocation.funding_lot_id,
      tradeNo:
        lots.get(allocation.funding_lot_id)?.display_reference ||
        allocation.external_order_id ||
        "历史分配记录",
      allocatedMinor: allocation.amount_minor,
    })),
    status: mapStatus(request.status),
    workflowStatus: request.status,
    submittedAt: request.submitted_at,
    updatedAt: request.updated_at,
    reviewer: request.reviewed_by,
    reviewNote: request.review_note,
    // Delivery is a separate persisted aggregate. Never infer email success
    // merely because the invoice workflow reached `issued`.
    mailStatus: "not_sent",
    newApiVerification,
  };
}

async function getBackendLots() {
  const response = await requestJSON<ItemsResponse<BackendFundingLot>>(
    "/api/v1/user/funding-lots",
  );
  return response.items;
}

async function getRequestPage(
  admin: boolean,
  cursor?: string,
  sourceInstanceId?: string,
) {
  const query = new URLSearchParams({ limit: "100" });
  // sourceInstanceId is embedded-admin platform scoping only; the server
  // rejects it outright for a non-admin caller (server.go's
  // listRequestsPage), so it is only ever set here when admin is true.
  if (admin && sourceInstanceId) {
    if (!uuidPattern.test(sourceInstanceId)) {
      throw new InvoiceApiError("平台来源筛选无效。", { code: "INVALID_FILTER" });
    }
    query.set("source_instance_id", sourceInstanceId);
  }
  if (cursor) {
    try {
      const decoded = JSON.parse(cursor) as {
        beforeSubmittedAt?: unknown;
        beforeId?: unknown;
      };
      if (
        typeof decoded.beforeSubmittedAt !== "string" ||
        typeof decoded.beforeId !== "string"
      ) {
        throw new Error("invalid cursor fields");
      }
      query.set("before_submitted_at", decoded.beforeSubmittedAt);
      query.set("before_id", decoded.beforeId);
    } catch {
      throw new InvoiceApiError("开票申请分页游标无效。", {
        code: "INVALID_CURSOR",
      });
    }
  }
  const response = await requestJSON<{
    items: BackendInvoiceRequest[];
    has_more?: boolean;
    next_before_submitted_at?: string;
    next_before_id?: string;
  }>(
    `${admin ? "/api/v1/admin" : "/api/v1/user"}/invoice-requests?${query.toString()}`,
    { role: admin ? "admin" : "user" },
  );
  // An administrator must never read the funding lots belonging to their own
  // ordinary user principal just to decorate an admin list. Admin DTOs carry
  // their own immutable allocation/payment-verification snapshot.
  const lots = admin ? [] : await getBackendLots();
  const lotMap = new Map(lots.map((lot) => [lot.id, lot]));
  if (
    response.has_more === true &&
    (!response.next_before_submitted_at || !response.next_before_id)
  ) {
    throw new InvoiceApiError("开票申请分页响应缺少下一页游标。", {
      code: "INVALID_PAGINATION_RESPONSE",
    });
  }
  const nextCursor =
    response.has_more &&
    response.next_before_submitted_at &&
    response.next_before_id
      ? JSON.stringify({
          beforeSubmittedAt: response.next_before_submitted_at,
          beforeId: response.next_before_id,
        })
      : undefined;
  return {
    items: response.items.map((request) => mapRequest(request, lotMap)),
    nextCursor,
  };
}

async function getRequests(admin: boolean) {
  return (await getRequestPage(admin)).items;
}

async function getRequestDetail(requestId: string, admin: boolean) {
  const request = await requestJSON<BackendInvoiceRequest>(
    `${admin ? "/api/v1/admin" : "/api/v1/user"}/invoice-requests/${encodeURIComponent(requestId)}`,
    { role: admin ? "admin" : "user" },
  );
  const lots = admin ? [] : await getBackendLots();
  return mapRequest(request, new Map(lots.map((lot) => [lot.id, lot])));
}

function mapSession(value: BackendSession): AuthSession {
  const oidcAdminLoginEnabled = value.oidc_admin_login_enabled !== false;
  if (!value.authenticated) {
    sessionCSRFToken = "";
    return { authenticated: false, oidcAdminLoginEnabled };
  }
  if (!value.user || !value.csrf_token) {
    throw new InvoiceApiError("服务返回的安全会话不完整。", {
      code: "INVALID_SESSION_RESPONSE",
    });
  }
  if (
    !["user", "admin"].includes(value.user.role) ||
    !value.user.id.trim() ||
    value.csrf_token.length < 32 ||
    /[\r\n\0]/.test(value.csrf_token)
  ) {
    throw new InvoiceApiError("服务返回的安全会话字段无效。", {
      code: "INVALID_SESSION_RESPONSE",
    });
  }
  // Unlike an OIDC identity, a platform account is not guaranteed to carry an
  // email (e.g. a username-only New API account): display_name always has a
  // server-side fallback (see maskedEmailName in production_auth.go), so an
  // empty email here is not itself an invalid session.
  sessionCSRFToken = value.csrf_token;
  return {
    authenticated: true,
    user: {
      id: value.user.id,
      displayName: value.user.display_name,
      email: value.user.email,
      emailVerified: value.user.email_verified,
      role: value.user.role,
      platform: value.user.platform || null,
      platformUserId: value.user.platform_user_id || null,
      username: value.user.username || null,
    },
    csrfToken: value.csrf_token,
    adminStepUpRequired: value.admin_step_up_required === true,
    oidcAdminLoginEnabled,
  };
}

export function mapSourceAccount(value: BackendSourceAccount): SourceAccount {
  if (!(["sub2api", "newapi"] as string[]).includes(value.source_type)) {
    throw new InvoiceApiError("源账号包含无法识别的平台类型。", {
      code: "INVALID_SOURCE_ACCOUNT",
    });
  }
  return {
    id: value.id,
    source: value.source_type,
    sourceInstanceId: value.source_instance_id,
    sourceLabel:
      value.source_name ||
      (value.source_type === "newapi" ? "SoloV 模型平台" : "SoloV API"),
    externalUserIdMasked: value.external_user_id_masked,
    status: value.binding_status,
    verifiedAt: value.verified_at,
    // "Never synced yet" arrives on the wire as a real-looking sentinel
    // timestamp rather than an absent field -- normalize it to undefined here
    // so every renderer's existing `lastObservedAt ? ... : "等待首次同步"`
    // check (App.tsx) works without each call site re-deriving this.
    lastObservedAt:
      value.last_observed_at && !isUnobservedTimestamp(value.last_observed_at)
        ? value.last_observed_at
        : undefined,
  };
}

function mapPaymentCandidate(value: BackendPaymentCandidate): PaymentCandidate {
  if (value.source_type !== "newapi") {
    throw new InvoiceApiError("支付候选来源无效，已停止显示。", {
      code: "INVALID_PAYMENT_CANDIDATE_SOURCE",
    });
  }
  if (value.currency !== "CNY") {
    throw new InvoiceApiError("支付候选包含不支持的币种，已停止显示。", {
      code: "UNSUPPORTED_CURRENCY",
    });
  }
  if (
    !Number.isSafeInteger(value.quoted_minor) ||
    value.quoted_minor <= 0 ||
    !Number.isSafeInteger(value.current_cap_minor) ||
    value.current_cap_minor < 0 ||
    value.current_cap_minor > value.quoted_minor
  ) {
    throw new InvoiceApiError("支付候选金额无效，已停止显示。", {
      code: "INVALID_PAYMENT_CANDIDATE_AMOUNT",
    });
  }
	if (
		value.manual_review_stage !== undefined &&
		!["unreviewed", "proposed", "approved"].includes(value.manual_review_stage)
	) {
		throw new InvoiceApiError("支付候选复核状态无效，已停止显示。", {
			code: "INVALID_PAYMENT_REVIEW_STAGE",
		});
	}
  return {
    id: value.id,
    source: "newapi",
    principalId: value.principal_id,
    principalDisplayName: value.principal_display_name,
    sourceInstanceId: value.source_instance_id,
    externalOrderId: value.source_order_id,
    tradeNo: value.display_reference || value.source_order_id,
    quotedMinor: value.quoted_minor,
    currentCapMinor: value.current_cap_minor,
    currency: "CNY",
    verification: value.verification,
		reviewStage:
			value.manual_review_stage === "proposed"
				? "proposed"
				: value.manual_review_stage === "approved"
					? "approved"
					: "unreviewed",
    sourceStatus: value.source_status,
    observedAt: value.observed_at,
  };
}

function mapRefundCase(value: BackendRefundCase): RefundCase {
  if (
    !["open", "resolved_red_letter", "resolved_no_action"].includes(
      value.status,
    )
  ) {
    throw new InvoiceApiError("退款案例状态无效，已停止显示。", {
      code: "INVALID_REFUND_CASE_STATUS",
    });
  }
  requireSafeMinor(value.observed_refund_minor, "退款金额");
  requireSafeMinor(value.issued_exposure_minor, "已开票暴露金额");
  requireSafeMinor(value.observed_cap_minor, "退款后金额上限");
  if (!value.id || !value.request_id || !value.funding_lot_id) {
    throw new InvoiceApiError("退款案例身份字段不完整。", {
      code: "INVALID_REFUND_CASE_RESPONSE",
    });
  }
  return {
    id: value.id,
    requestId: value.request_id,
    fundingLotId: value.funding_lot_id,
    sourceRevision: value.source_revision,
    observedRefundMinor: value.observed_refund_minor,
    issuedExposureMinor: value.issued_exposure_minor,
    observedCapMinor: value.observed_cap_minor,
    status: value.status,
    resolvedBy: value.resolved_by || undefined,
    openedAt: value.opened_at,
    updatedAt: value.updated_at,
    resolvedAt: value.status === "open" ? undefined : value.resolved_at,
  };
}

function mapSourceHealth(
  value: BackendSourceHealth,
  degradedHTTP: boolean,
): SourceHealthReport {
  if (typeof value?.ready !== "boolean" || !Array.isArray(value?.items)) {
    throw new InvoiceApiError("服务未返回可安全展示的同步健康明细。", {
      code: "INVALID_SOURCE_HEALTH_RESPONSE",
    });
  }
  if (value.items.length > 32) {
    throw new InvoiceApiError("同步健康明细数量超出安全范围。", {
      code: "INVALID_SOURCE_HEALTH_RESPONSE",
    });
  }
  const items = value.items.map((item) => {
    const counters = [
      item.sequence,
      item.maximum_age_seconds,
      item.economic_watermark_maximum_age_seconds ?? 0,
      item.pending_events,
      item.dead_events,
      item.contained_dead_events ?? 0,
      item.waiting_dependencies,
    ];
    if (
      !["sub2api", "newapi"].includes(item.source_type) ||
      !["payments", "identities", "usage", "credits", "balances"].includes(
        item.stream_id,
      ) ||
      !["healthy", "blocked", "unknown"].includes(item.projection_status) ||
      typeof item.source_enabled !== "boolean" ||
      typeof item.ready !== "boolean" ||
      !item.source_instance_id ||
      item.source_instance_id.length > 128 ||
      ![
        item.approved_runtime_version,
        item.observed_runtime_version,
        item.observed_agent_version,
      ].every((version) => typeof version === "string" && version.length <= 128) ||
      ![
        item.last_accepted_at,
        item.last_nonempty_batch_at,
        item.economic_watermark_at,
      ].every(
        (timestamp) =>
          timestamp === undefined ||
          (typeof timestamp === "string" &&
            timestamp.length <= 64 &&
            (timestamp.startsWith("0001-") ||
              Number.isFinite(Date.parse(timestamp)))),
      ) ||
      !counters.every((counter) => Number.isSafeInteger(counter) && counter >= 0) ||
      item.maximum_age_seconds <= 0 ||
      (item.stream_id === "identities"
        ? item.economic_watermark_maximum_age_seconds !== undefined
        : !Number.isSafeInteger(item.economic_watermark_maximum_age_seconds) ||
          (item.economic_watermark_maximum_age_seconds ?? 0) <= 0) ||
      !Array.isArray(item.reasons) ||
      item.reasons.length > 10 ||
      !item.reasons.every(
        (reason) => typeof reason === "string" && reason.length <= 80,
      )
    ) {
      throw new InvoiceApiError("同步健康明细包含无效字段。", {
        code: "INVALID_SOURCE_HEALTH_RESPONSE",
      });
    }
    return {
      sourceInstanceId: item.source_instance_id,
      sourceType: item.source_type,
      // Display a fixed product label rather than a configurable database name;
      // the admin health screen must never become a channel for secrets or an
      // issuer name accidentally stored in source metadata.
      sourceName:
        item.source_type === "sub2api" ? "SoloV Sub2API" : "SoloV New API",
      sourceEnabled: item.source_enabled,
      streamId: item.stream_id,
      sequence: item.sequence,
      approvedRuntimeVersion: String(item.approved_runtime_version ?? ""),
      cutoverRuntimeVersion: String(item.cutover_runtime_version ?? ""),
      observedRuntimeVersion: String(item.observed_runtime_version ?? ""),
      observedAgentVersion: String(item.observed_agent_version ?? ""),
      projectionStatus: item.projection_status,
      lastAcceptedAt: item.last_accepted_at?.startsWith("0001-")
        ? undefined
        : item.last_accepted_at,
      lastNonemptyBatchAt: item.last_nonempty_batch_at?.startsWith("0001-")
        ? undefined
        : item.last_nonempty_batch_at,
      economicWatermarkAt: item.economic_watermark_at?.startsWith("0001-")
        ? undefined
        : item.economic_watermark_at,
      maximumAgeSeconds: item.maximum_age_seconds,
      economicWatermarkMaximumAgeSeconds:
        item.economic_watermark_maximum_age_seconds,
      pendingEvents: item.pending_events,
      deadEvents: item.dead_events,
      containedDeadEvents: item.contained_dead_events ?? 0,
      waitingDependencies: item.waiting_dependencies,
      ready: item.ready,
      reasons: item.reasons,
    };
  });
  const requiredStreamsReady = (["sub2api", "newapi"] as const).every(
    (sourceType) =>
      (["payments", "identities", "usage", "credits", "balances"] as const).every((streamId) =>
        items.some(
          (item) =>
            item.sourceType === sourceType &&
            item.streamId === streamId &&
            item.sourceEnabled &&
            item.ready,
        ),
      ),
  );
  return {
    ready: !degradedHTTP && value.ready && requiredStreamsReady,
    degradedHTTP,
    items,
    eligibilityProjection: mapEligibilityProjectionHealth(
      value.eligibility_projection,
    ),
  };
}

// mapEligibilityProjectionHealth validates the optional projection block with
// the same strictness the per-stream rows above get: a present-but-malformed
// block means the same broken server contract, and silently rendering part of
// it would be worse than failing the screen closed. An ABSENT block is not an
// error -- it is what a server predating this field returns -- and maps to
// undefined so the screen can say the counters were not reported instead of
// showing zeros that look like an idle, healthy queue.
function mapEligibilityProjectionHealth(
  value: BackendSourceHealth["eligibility_projection"],
): EligibilityProjectionHealth | undefined {
  if (value === undefined) return undefined;
  const counters = [
    value.queued,
    value.processing,
    value.retrying,
    value.dead,
    value.proof_pending,
  ];
  const timestamps = [value.oldest_pending, value.oldest_proof_pending];
  if (
    typeof value !== "object" ||
    value === null ||
    !counters.every(
      (counter) => Number.isSafeInteger(counter) && counter >= 0,
    ) ||
    !timestamps.every(
      (timestamp) =>
        timestamp === undefined ||
        (typeof timestamp === "string" &&
          timestamp.length <= 64 &&
          Number.isFinite(Date.parse(timestamp))),
    )
  ) {
    throw new InvoiceApiError("资格投影健康明细包含无效字段。", {
      code: "INVALID_SOURCE_HEALTH_RESPONSE",
    });
  }
  return {
    queued: value.queued,
    processing: value.processing,
    retrying: value.retrying,
    dead: value.dead,
    proofPending: value.proof_pending,
    oldestPendingAt: value.oldest_pending,
    oldestProofPendingAt: value.oldest_proof_pending,
  };
}

async function requestSourceHealth() {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), requestTimeoutMs);
  try {
    const response = await fetch(endpointURL("/api/v1/admin/source-health"), {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "include",
      redirect: "error",
      signal: controller.signal,
    });
    const rotatedCSRF = response.headers.get("X-CSRF-Token");
    if (rotatedCSRF) sessionCSRFToken = rotatedCSRF;
    const contentType = response.headers.get("content-type") ?? "";
    const payload = contentType.includes("application/json")
      ? ((await response.json()) as BackendSourceHealth & BackendError)
      : undefined;
    if (response.status !== 200 && response.status !== 503) {
      throw responseError(response.status, payload);
    }
    if (!payload) {
      throw new InvoiceApiError("同步健康接口返回了无法识别的数据格式。", {
        code: "INVALID_SOURCE_HEALTH_RESPONSE",
      });
    }
    return mapSourceHealth(payload, response.status === 503);
  } catch (error) {
    if (error instanceof InvoiceApiError) throw error;
    if (error instanceof DOMException && error.name === "AbortError") {
      throw new InvoiceApiError("同步健康状态读取超时。", {
        code: "TIMEOUT",
        retryable: true,
      });
    }
    throw new InvoiceApiError("无法连接开票服务读取同步健康状态。", {
      code: "NETWORK_ERROR",
      retryable: true,
    });
  } finally {
    window.clearTimeout(timeout);
  }
}

function unsupported(message: string): never {
  throw new InvoiceApiError(message, { code: "NOT_IMPLEMENTED" });
}

export const httpInvoiceApi: InvoiceApiClient = {
  mode: "http",
  capabilities: {
    documentUpload: true,
    documentDownload: true,
    mailResend: true,
  },

  async getSession() {
    try {
      const value = await requestJSON<BackendSession>("/api/v1/auth/session");
      return mapSession(value);
    } catch (error) {
      if (error instanceof InvoiceApiError && error.requiresLogin) {
        sessionCSRFToken = "";
        // The request itself failed, so there is no oidc_admin_login_enabled
        // to read -- default true (today's actual default): worst case an
        // operator briefly sees a login button that leads nowhere useful,
        // never a security issue either way.
        return { authenticated: false, oidcAdminLoginEnabled: true };
      }
      throw error;
    }
  },

  async logout() {
    const result = await requestJSON<{ ok: boolean; logout_url?: string }>(
      "/api/v1/auth/logout",
      { method: "POST" },
    );
    if (result.ok !== true) {
      throw new InvoiceApiError("退出登录响应无效。", {
        code: "INVALID_LOGOUT_RESPONSE",
      });
    }
    // A platform-password session (see AuthUser.platform) never went through
    // the identity provider, so logout omits logout_url entirely: there is
    // nowhere else to send the browser.
    if (result.logout_url === undefined) {
      sessionCSRFToken = "";
      documentUploadCheckpoints.clear();
      return null;
    }
    let logoutURL: URL;
    try {
      logoutURL = new URL(result.logout_url);
    } catch {
      throw new InvoiceApiError("身份提供商退出地址无效。", {
        code: "INVALID_LOGOUT_URL",
      });
    }
    if (
      logoutURL.protocol !== "https:" ||
      logoutURL.username !== "" ||
      logoutURL.password !== "" ||
      logoutURL.hash !== "" ||
      result.logout_url.length > 4096 ||
      /[\r\n\0]/.test(result.logout_url)
    ) {
      throw new InvoiceApiError("身份提供商退出地址无效。", {
        code: "INVALID_LOGOUT_URL",
      });
    }
    sessionCSRFToken = "";
    documentUploadCheckpoints.clear();
    return logoutURL.toString();
  },

  loginURL(returnTo) {
    return endpointURL(
      `/api/v1/auth/login?return_to=${encodeURIComponent(returnTo)}`,
    );
  },

  adminStepUpURL(returnTo) {
    return endpointURL(
      `/api/v1/auth/admin/step-up?return_to=${encodeURIComponent(returnTo)}`,
    );
  },

  // CR-0006 (XM-INV-CONSOLE-ASSERT): skipCSRF for the same reason
  // platformLogin below does -- there is no pre-existing session yet, so no
  // synchronizer token exists to send. The backend's own defense here is an
  // exact Origin check, not CSRF (see production_auth.go's
  // consoleAssertionExchange).
  async exchangeConsoleAssertion(assertion) {
    await requestJSON<{ ok: boolean }>("/api/v1/auth/console-assertion", {
      method: "POST",
      skipCSRF: true,
      body: { assertion },
    });
  },

  async platformLogin(input) {
    return mapPlatformLoginOutcome(
      await requestJSON<BackendPlatformLoginOutcome>(
        "/api/v1/auth/platform-login",
        {
          method: "POST",
          skipCSRF: true,
          body: platformLoginBody(input),
        },
      ),
    );
  },

  async verifyPlatformLoginTwoFA(input) {
    return mapPlatformLoginOutcome(
      await requestJSON<BackendPlatformLoginOutcome>(
        "/api/v1/auth/platform-login/2fa",
        {
          method: "POST",
          skipCSRF: true,
          body: platformLoginTwoFABody(input),
        },
      ),
    );
  },

  async getSourceAccounts() {
    const response = await requestJSON<ItemsResponse<BackendSourceAccount>>(
      "/api/v1/user/source-accounts",
    );
    return response.items.map(mapSourceAccount);
  },

  async getOrders() {
    return (await getBackendLots()).map(mapLot);
  },

  async getUserEligibilitySummary() {
    const response = await requestJSON<unknown>(
      "/api/v1/user/eligibility-summary",
    );
    exactObjectKeys(response, ["items"], ["items"], "开票资格摘要响应");
    if (!Array.isArray(response.items) || response.items.length > 32) {
      throw new InvoiceApiError("开票资格摘要数量无效，已停止显示。", {
        code: "INVALID_ELIGIBILITY_RESPONSE",
      });
    }
    return response.items.map((item) =>
      mapEligibilitySummary(item as BackendEligibilitySummary),
    );
  },

  async getInvoicePolicy() {
    const policy = await requestJSON<BackendInvoicePolicy>(
      "/api/v1/user/invoice-policy",
    );
    return mapInvoicePolicy(policy);
  },

  async getProfiles() {
    const response = await requestJSON<ItemsResponse<BackendProfile>>(
      "/api/v1/user/profiles",
    );
    return response.items.map(mapProfile);
  },

  async saveProfile(profile) {
    const saved = await requestJSON<BackendProfile>("/api/v1/user/profiles", {
      method: "POST",
      body: profileMutationBody(profile),
    });
    return mapProfile(saved);
  },

  getUserRequests() {
    return getRequests(false);
  },

  getAdminRequests() {
    return getRequests(true);
  },

  getUserRequestPage(cursor) {
    return getRequestPage(false, cursor);
  },

  getAdminRequestPage(cursor, sourceInstanceId) {
    return getRequestPage(true, cursor, sourceInstanceId);
  },

  getInvoiceRequestDetail(requestId, admin = false) {
    return getRequestDetail(requestId, admin);
  },

  async getSourceHealth(): Promise<SourceHealthReport> {
    return requestSourceHealth();
  },

  async getSummary(admin = false): Promise<DashboardSummary> {
    const requests = await getRequests(admin);
    const lots = admin ? [] : await getBackendLots();
    return {
      totalAvailableMinor: lots.reduce(
        (total, lot) => total + availableMinor(lot),
        0,
      ),
      reviewingMinor: requests
        .filter(
          (request) =>
            request.status === "submitted" || request.status === "reviewing",
        )
        .reduce((total, request) => total + request.amountMinor, 0),
      issuedThisYearMinor: requests
        .filter((request) => request.status === "issued")
        .reduce((total, request) => total + request.amountMinor, 0),
      pendingCount: requests.filter(
        (request) =>
          request.status === "submitted" || request.status === "reviewing",
      ).length,
    };
  },

  async submitInvoice(payload) {
    const lots = await getBackendLots();
    const firstLot = lots.find(
      (lot) => lot.id === payload.allocations[0]?.orderId,
    );
    const sourceInstanceId =
      payload.sourceInstanceId || firstLot?.source_instance_id;
    if (!sourceInstanceId) {
      throw new InvoiceApiError("无法确认订单所属的平台实例，请刷新后重试。", {
        code: "SOURCE_INSTANCE_MISSING",
      });
    }
    const created = await requestJSON<BackendInvoiceRequest>(
      "/api/v1/user/invoice-requests",
      {
        method: "POST",
        headers: { "Idempotency-Key": crypto.randomUUID() },
        body: {
          profile_id: payload.profileId,
          source_instance_id: sourceInstanceId,
          allocations: payload.allocations.map((allocation) => ({
            funding_lot_id: allocation.orderId,
            amount_minor: allocation.allocatedMinor,
          })),
        },
      },
    );
    return mapRequest(created, new Map(lots.map((lot) => [lot.id, lot])));
  },

  async cancelInvoice(request) {
    if (!request.version) {
      throw new InvoiceApiError("缺少申请版本，请刷新后重试。", {
        code: "VERSION_MISSING",
      });
    }
    const lots = await getBackendLots();
    const cancelled = await requestJSON<BackendInvoiceRequest>(
      `/api/v1/user/invoice-requests/${encodeURIComponent(request.id)}/cancel`,
      {
        method: "POST",
        body: { version: request.version },
      },
    );
    return mapRequest(
      cancelled,
      new Map(lots.map((lot) => [lot.id, lot])),
    );
  },

  async getPaymentCandidates(cursor, sourceInstanceId) {
		const query = new URLSearchParams({ limit: "50", include_verified: "true" });
    if (sourceInstanceId) {
      if (!uuidPattern.test(sourceInstanceId)) {
        throw new InvoiceApiError("平台来源筛选无效。", { code: "INVALID_FILTER" });
      }
      query.set("source_instance_id", sourceInstanceId);
    }
    if (cursor) {
      try {
        const decoded = JSON.parse(cursor) as {
          beforeObservedAt?: unknown;
          beforeId?: unknown;
        };
        if (
          typeof decoded.beforeObservedAt !== "string" ||
          typeof decoded.beforeId !== "string"
        ) {
          throw new Error("invalid cursor fields");
        }
        query.set("before_observed_at", decoded.beforeObservedAt);
        query.set("before_id", decoded.beforeId);
      } catch {
        throw new InvoiceApiError("支付候选分页游标无效。", {
          code: "INVALID_CURSOR",
        });
      }
    }
    const response = await requestJSON<{
      items: BackendPaymentCandidate[];
      has_more: boolean;
      next_before_observed_at?: string;
      next_before_id?: string;
    }>(`/api/v1/admin/payment-candidates?${query.toString()}`, {
      role: "admin",
    });
    const nextCursor =
      response.has_more &&
      response.next_before_observed_at &&
      response.next_before_id
        ? JSON.stringify({
            beforeObservedAt: response.next_before_observed_at,
            beforeId: response.next_before_id,
          })
        : undefined;
    return {
      items: response.items.map(mapPaymentCandidate),
      nextCursor,
    };
  },

  async verifyPayment(candidateId, input) {
		const response = await requestJSON<BackendFundingLot>(
      `/api/v1/admin/funding-lots/${encodeURIComponent(candidateId)}/verify-payment`,
      {
        method: "POST",
        role: "admin",
        body: {
          paid_minor: input.paidMinor,
          currency: input.currency,
          evidence_reference: input.evidenceReference,
        },
      },
    );
		return response.verification === "verified" ? "verified" : "proposed";
  },

  async rejectPayment(candidateId, input) {
    await requestJSON(
      `/api/v1/admin/funding-lots/${encodeURIComponent(candidateId)}/reject-payment`,
      {
        method: "POST",
        role: "admin",
        body: {
          reason: input.reason,
          evidence_reference: input.evidenceReference,
        },
      },
    );
  },

	async applyManualPaymentCap(candidateId, input) {
		await requestJSON(
			`/api/v1/admin/funding-lots/${encodeURIComponent(candidateId)}/manual-cap-adjustment`,
			{
				method: "POST",
				role: "admin",
				body: {
					new_cap_minor: input.newCapMinor,
					evidence_reference: input.evidenceReference,
					reason: input.reason,
				},
			},
		);
	},

  async getRefundCases(
    status: RefundCaseStatus,
    cursor?: string,
    sourceInstanceId?: string,
  ) {
    const query = new URLSearchParams({ limit: "50", status });
    if (sourceInstanceId) {
      if (!uuidPattern.test(sourceInstanceId)) {
        throw new InvoiceApiError("平台来源筛选无效。", { code: "INVALID_FILTER" });
      }
      query.set("source_instance_id", sourceInstanceId);
    }
    if (cursor) {
      try {
        const decoded = JSON.parse(cursor) as {
          beforeOpenedAt?: unknown;
          beforeId?: unknown;
        };
        if (
          typeof decoded.beforeOpenedAt !== "string" ||
          typeof decoded.beforeId !== "string"
        ) {
          throw new Error("invalid cursor fields");
        }
        query.set("before_opened_at", decoded.beforeOpenedAt);
        query.set("before_id", decoded.beforeId);
      } catch {
        throw new InvoiceApiError("退款案例分页游标无效。", {
          code: "INVALID_CURSOR",
        });
      }
    }
    const response = await requestJSON<{
      items: BackendRefundCase[];
      has_more: boolean;
      next_before_opened_at?: string;
      next_before_id?: string;
    }>(`/api/v1/admin/refund-cases?${query.toString()}`, {
      role: "admin",
    });
    const nextCursor =
      response.has_more &&
      response.next_before_opened_at &&
      response.next_before_id
        ? JSON.stringify({
            beforeOpenedAt: response.next_before_opened_at,
            beforeId: response.next_before_id,
          })
        : undefined;
    return {
      items: response.items.map(mapRefundCase),
      nextCursor,
    };
  },

  async resolveRefundCase(caseId, input) {
    const resolved = await requestJSON<BackendRefundCase>(
      `/api/v1/admin/refund-cases/${encodeURIComponent(caseId)}/resolve`,
      {
        method: "POST",
        role: "admin",
        body: {
          resolution_status: input.resolutionStatus,
          evidence_reference: input.evidenceReference,
          note: input.note,
        },
      },
    );
    return mapRefundCase(resolved);
  },

  async getEligibilityFreezes(
    filters: EligibilityFreezeFilters,
    cursor?: string,
  ) {
    if (
      !["open", "resolved", "all"].includes(filters.status) ||
      (filters.reason !== undefined &&
        !eligibilityFreezeReasons.includes(filters.reason)) ||
      (filters.sourceInstanceId !== undefined &&
        !uuidPattern.test(filters.sourceInstanceId)) ||
      (filters.externalUserId !== undefined &&
        (filters.externalUserId.length === 0 ||
          filters.externalUserId.length > 512 ||
          /[\r\n\0]/.test(filters.externalUserId)))
    ) {
      throw new InvoiceApiError("资格冻结筛选条件无效。", {
        code: "INVALID_FILTER",
      });
    }
    const query = new URLSearchParams({ limit: "50", status: filters.status });
    if (filters.reason) query.set("reason", filters.reason);
    if (filters.sourceInstanceId)
      query.set("source_instance_id", filters.sourceInstanceId);
    if (filters.externalUserId)
      query.set("external_user_id", filters.externalUserId);
    if (cursor) {
      const decoded = eligibilityFreezeCursor(cursor);
      query.set("before_opened_at", decoded.beforeOpenedAt);
      query.set("before_id", decoded.beforeId);
    }
    const response = await requestJSON<unknown>(
      `/api/v1/admin/eligibility-freezes?${query.toString()}`,
      { role: "admin" },
    );
    exactObjectKeys(
      response,
      [
        "items",
        "has_more",
        "next_before_opened_at",
        "next_before_id",
      ],
      [
        "items",
        "has_more",
        "next_before_opened_at",
        "next_before_id",
      ],
      "资格冻结分页响应",
    );
    if (
      !Array.isArray(response.items) ||
      response.items.length > 50 ||
      typeof response.has_more !== "boolean" ||
      typeof response.next_before_opened_at !== "string" ||
      response.next_before_opened_at.length > 64 ||
      typeof response.next_before_id !== "string" ||
      response.next_before_id.length > 64 ||
      (response.has_more &&
        (!validTimestamp(response.next_before_opened_at) ||
          !uuidPattern.test(response.next_before_id)))
    ) {
      throw new InvoiceApiError("资格冻结分页响应无效，已停止显示。", {
        code: "INVALID_ELIGIBILITY_FREEZE_RESPONSE",
      });
    }
    return {
      items: response.items.map((item) =>
        mapEligibilityFreeze(item as BackendEligibilityFreeze),
      ),
      nextCursor: response.has_more
        ? JSON.stringify({
            beforeOpenedAt: response.next_before_opened_at,
            beforeId: response.next_before_id,
          })
        : undefined,
    };
  },

  async resolveEligibilityFreeze(freezeId, input) {
    const evidenceReference = input.evidenceReference.trim();
    const note = input.note.trim();
    if (
      !uuidPattern.test(freezeId) ||
      !Number.isSafeInteger(input.version) ||
      input.version <= 0 ||
      !evidenceReference ||
      evidenceReference.length > 2048 ||
      /[\r\n\0]/.test(evidenceReference) ||
      !note ||
      note.length > 2000 ||
      /\0/.test(note)
    ) {
      throw new InvoiceApiError("资格解冻需要有效版本、证据引用和处理说明。", {
        code: "ELIGIBILITY_RESOLUTION_EVIDENCE_REQUIRED",
      });
    }
    const response = await requestJSON<unknown>(
      `/api/v1/admin/eligibility-freezes/${encodeURIComponent(freezeId)}/resolve`,
      {
        method: "POST",
        role: "admin",
        body: {
          version: input.version,
          evidence_reference: evidenceReference,
          note,
        },
      },
    );
    return mapEligibilityFreeze(response as BackendEligibilityFreeze);
  },

  async getAccountLedger(filters: AccountLedgerFilters, cursor?: string) {
    if (
      (filters.sourceInstanceId !== undefined &&
        !uuidPattern.test(filters.sourceInstanceId)) ||
      (filters.externalUserId !== undefined &&
        (filters.externalUserId.length === 0 ||
          filters.externalUserId.length > 512 ||
          /[\r\n\0]/.test(filters.externalUserId))) ||
      (filters.sort !== undefined && filters.sort !== "block_state")
    ) {
      throw new InvoiceApiError("用户账本筛选条件无效。", {
        code: "INVALID_FILTER",
      });
    }
    const query = new URLSearchParams({ limit: "50" });
    if (filters.sort) query.set("sort", filters.sort);
    if (filters.sourceInstanceId)
      query.set("source_instance_id", filters.sourceInstanceId);
    if (filters.externalUserId)
      query.set("external_user_id", filters.externalUserId);
    if (cursor) {
      const decoded = accountLedgerCursor(cursor);
      if (decoded.beforeInvoiceableMinor !== undefined) {
        query.set(
          "before_invoiceable_minor",
          String(decoded.beforeInvoiceableMinor),
        );
      }
      query.set("before_id", decoded.beforeId);
    }
    const response = await requestJSON<unknown>(
      `/api/v1/admin/accounts/ledger?${query.toString()}`,
      { role: "admin" },
    );
    exactObjectKeys(
      response,
      ["items", "has_more", "next_before_invoiceable_minor", "next_before_id"],
      ["items", "has_more", "next_before_invoiceable_minor", "next_before_id"],
      "用户账本分页响应",
    );
    if (
      !Array.isArray(response.items) ||
      response.items.length > 50 ||
      typeof response.has_more !== "boolean" ||
      !(
        response.next_before_invoiceable_minor === null ||
        Number.isSafeInteger(response.next_before_invoiceable_minor)
      ) ||
      typeof response.next_before_id !== "string" ||
      response.next_before_id.length > 64 ||
      (response.has_more && !uuidPattern.test(response.next_before_id))
    ) {
      throw new InvoiceApiError("用户账本分页响应无效，已停止显示。", {
        code: "INVALID_ACCOUNT_LEDGER_RESPONSE",
      });
    }
    return {
      items: response.items.map((item) =>
        mapAccountLedgerListItem(item as BackendAccountLedgerListItem),
      ),
      nextCursor: response.has_more
        ? JSON.stringify({
            beforeInvoiceableMinor: response.next_before_invoiceable_minor,
            beforeId: response.next_before_id,
          })
        : undefined,
    } satisfies AccountLedgerPage;
  },

  async getAccountLedgerDetail(externalAccountId: string) {
    if (!uuidPattern.test(externalAccountId)) {
      throw new InvoiceApiError("账号地址无效。", {
        code: "INVALID_ACCOUNT_ID",
      });
    }
    const response = await requestJSON<unknown>(
      `/api/v1/admin/accounts/${encodeURIComponent(externalAccountId)}/ledger`,
      { role: "admin" },
    );
    return mapAccountLedgerDetail(response as BackendAccountLedgerDetail);
  },

  async adminReview(payload) {
    if (!payload.version) {
      throw new InvoiceApiError("缺少申请版本，请刷新后重试。", {
        code: "VERSION_MISSING",
      });
    }
    await requestJSON(
      `/api/v1/admin/invoice-requests/${encodeURIComponent(payload.requestId)}/review`,
      {
        method: "POST",
        role: "admin",
        body: {
          action: payload.action,
          note: payload.note,
          version: payload.version,
        },
      },
    );
  },

  async adminUploadInvoice(request, file, invoiceNumber, issuedAt) {
    if (!request.version) {
      throw new InvoiceApiError("缺少申请版本，请刷新后重试。", {
        code: "VERSION_MISSING",
      });
    }
    let uploadVersion =
      documentUploadCheckpoints.get(request.id) ?? request.version;
    if (request.workflowStatus === "issued_awaiting_document") {
      uploadVersion = request.version;
      documentUploadCheckpoints.set(request.id, uploadVersion);
    } else if (!documentUploadCheckpoints.has(request.id)) {
      const confirmed = await requestJSON<BackendInvoiceRequest>(
        `/api/v1/admin/invoice-requests/${encodeURIComponent(request.id)}/confirm-manual-issue`,
        { method: "POST", role: "admin", body: { version: request.version } },
      );
      uploadVersion = confirmed.version;
      // Preserve the post-confirm version before attempting the upload. If the
      // PDF transfer fails, retrying this browser session resumes at the
      // document step and never confirms manual issuance a second time.
      documentUploadCheckpoints.set(request.id, uploadVersion);
    }
    const form = new FormData();
    form.set("version", String(uploadVersion));
    form.set("invoice_number", invoiceNumber);
    form.set("issued_at", issuedAt);
    form.set("file", file, file.name);
    await requestMultipart(
      `/api/v1/admin/invoice-requests/${encodeURIComponent(request.id)}/documents/upload`,
      form,
      "admin",
    );
    documentUploadCheckpoints.delete(request.id);
  },

  async resendMail(request) {
    await requestJSON(
      `/api/v1/admin/invoice-requests/${encodeURIComponent(request.id)}/email/requeue`,
      {
        method: "POST",
        role: "admin",
        body: { reason: "管理员在发票详情中明确请求重新发送" },
      },
    );
  },

  async getDeliveryState(request, admin = false) {
    const prefix = admin ? "/api/v1/admin" : "/api/v1/user";
    const value = await requestJSON<{
      document_available: boolean;
      invoice_number?: string;
      issued_at?: string;
      mail_status?: string;
      mail_attempts?: number;
      next_mail_attempt_at?: string;
    }>(
      `${prefix}/invoice-requests/${encodeURIComponent(request.id)}/delivery`,
      { role: admin ? "admin" : "user" },
    );
    const rawMailStatus = value.mail_status ?? "";
    if (
      rawMailStatus &&
      !["queued", "sending", "sent", "failed", "dead"].includes(
        rawMailStatus,
      )
    ) {
      throw new InvoiceApiError("邮件交付状态无效，已停止显示。", {
        code: "INVALID_DELIVERY_RESPONSE",
      });
    }
    if (
      value.mail_attempts !== undefined &&
      (!Number.isSafeInteger(value.mail_attempts) || value.mail_attempts < 0)
    ) {
      throw new InvoiceApiError("邮件尝试次数无效，已停止显示。", {
        code: "INVALID_DELIVERY_RESPONSE",
      });
    }
    const mailStatus: InvoiceDeliveryState["mailStatus"] =
      rawMailStatus === "sent"
        ? "sent"
        : rawMailStatus === "queued" || rawMailStatus === "sending"
          ? "queued"
          : rawMailStatus === "failed" || rawMailStatus === "dead"
            ? "failed"
            : "not_sent";
    return {
      documentAvailable: value.document_available === true,
      invoiceNumber: value.document_available ? value.invoice_number : undefined,
      issuedAt: value.document_available ? value.issued_at : undefined,
      mailStatus,
      mailAttempts: value.mail_attempts ?? 0,
      nextMailAttemptAt:
        mailStatus === "queued" || mailStatus === "failed"
          ? value.next_mail_attempt_at
          : undefined,
    };
  },

  async listRequestNotices(request) {
    // **只有 admin 路径**：后端没有 user 变体（这是运维事实，不是申请人的
    // 业务数据），前端也不该造一个会 404 的调用。
    const value = await requestJSON<{
      items?: Array<{
        id?: string;
        kind?: string;
        status?: string;
        attempt_count?: number;
        next_attempt_at?: string | null;
        delivered_at?: string | null;
        last_error_code?: string;
        created_at?: string | null;
      }> | null;
    }>(
      `/api/v1/admin/invoice-requests/${encodeURIComponent(request.id)}/notices`,
      { role: "admin" },
    );
    return (value.items ?? []).map((item) => {
      if (
        item.attempt_count !== undefined &&
        (!Number.isSafeInteger(item.attempt_count) || item.attempt_count < 0)
      ) {
        // 与邮件交付同一条纪律：宁可整块停掉，也不显示一个说不通的数字。
        throw new InvoiceApiError("通知尝试次数无效，已停止显示。", {
          code: "INVALID_NOTICE_RESPONSE",
        });
      }
      return {
        id: item.id ?? "",
        kind: item.kind ?? "",
        // 未知状态原样带出：后端加了新状态时显示原值仍然有用，
        // 比藏起来强（与卡片推送对未知交易类型的处理同一条）。
        status: item.status ?? "",
        attemptCount: item.attempt_count ?? 0,
        // null 与缺失都归成 undefined：界面只需要区分"有"和"没有"。
        nextAttemptAt: item.next_attempt_at ?? undefined,
        deliveredAt: item.delivered_at ?? undefined,
        lastErrorCode: item.last_error_code ?? "",
        createdAt: item.created_at ?? undefined,
      };
    });
  },

  async downloadInvoiceDocument(request) {
    const response = await requestBinary(
      invoiceDocumentPath(request.id),
      "user",
    );
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = request.pdfName ?? `${request.requestNo}.pdf`;
    anchor.click();
    URL.revokeObjectURL(url);
  },

  async downloadAdminInvoiceDocument(request) {
    const response = await requestBinary(
      invoiceDocumentPath(request.id, true),
      "admin",
    );
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = request.pdfName ?? `${request.requestNo}.pdf`;
    anchor.click();
    URL.revokeObjectURL(url);
  },

  async saveNoticeWebhook(webhookURL) {
    await requestJSON<unknown>("/api/v1/admin/settings/notice-webhook", {
      method: "PUT",
      role: "admin",
      body: { webhook_url: webhookURL },
    });
  },

  async clearNoticeWebhook() {
    await requestJSON<unknown>("/api/v1/admin/settings/notice-webhook", {
      method: "DELETE",
      role: "admin",
    });
  },

  async sendNoticeWebhookTest() {
    await requestJSON<unknown>("/api/v1/admin/settings/notice-webhook/test", {
      method: "POST",
      role: "admin",
      body: {},
    });
  },

  async getAdminSettings() {
    const settings = await requestJSON<BackendSystemSettings>(
      "/api/v1/admin/settings",
      { role: "admin" },
    );
    return mapAdminSettings(settings);
  },

  async saveInvoiceRules(input) {
    await requestJSON("/api/v1/admin/settings/invoice", {
      method: "PUT",
      role: "admin",
      body: {
        revision: input.revision,
        issuer_name: input.issuerName,
        minimum_request_minor: input.minimumRequestMinor,
      },
    });
  },

  async saveSMTPSettings(input) {
    await requestJSON("/api/v1/admin/settings/smtp", {
      method: "PUT",
      role: "admin",
      body: {
        revision: input.revision,
        from_address: input.fromAddress,
        from_name: input.fromName,
        host: input.host,
        port: input.port,
        starttls: input.startTLS,
        ...(input.authorizationCode
          ? { authorization_code: input.authorizationCode }
          : {}),
        // undefined 与空串在这里含义不同，所以只能判 undefined，不能判真值：
        // 传空串是「清空收件人」，被真值判断吃掉就变成了「保持不变」。
        ...(input.testRecipient === undefined
          ? {}
          : { test_recipient: input.testRecipient }),
      },
    });
  },

  async sendSMTPTest() {
    try {
      await requestJSON("/api/v1/admin/settings/smtp/test", {
        method: "POST",
        role: "admin",
        body: {},
      });
    } catch (error) {
      if (error instanceof InvoiceApiError && error.status === 503) {
        throw new InvoiceApiError("需配置 SMTP 凭据和独立测试收件邮箱后启用测试邮件。", {
          code: error.code,
          status: 503,
          retryable: true,
        });
      }
      throw error;
    }
  },

  async saveAdminAccess(input) {
    await requestJSON("/api/v1/admin/settings/admin-access", {
      method: "PUT",
      role: "admin",
      body: { revision: input.revision, cidrs: input.cidrs },
    });
  },
};

async function requestMultipart(
  path: string,
  body: FormData,
  _role: RequestRole,
) {
  const headers = new Headers({
    Accept: "application/json",
    "X-CSRF-Token": csrfTokenForMutation(),
  });
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), requestTimeoutMs);
  try {
    const response = await fetch(endpointURL(path), {
      method: "POST",
      headers,
      body,
      credentials: "include",
      redirect: "error",
      signal: controller.signal,
    });
    const rotatedCSRF = response.headers.get("X-CSRF-Token");
    if (rotatedCSRF) sessionCSRFToken = rotatedCSRF;
    if (!response.ok) {
      const payload = (await response.json().catch(() => undefined)) as
        | BackendError
        | undefined;
      throw responseError(response.status, payload);
    }
  } catch (error) {
    if (error instanceof InvoiceApiError) throw error;
    if (error instanceof DOMException && error.name === "AbortError") {
      throw new InvoiceApiError("上传超时，请检查网络后重试。", {
        code: "TIMEOUT",
        retryable: true,
      });
    }
    throw new InvoiceApiError("无法连接开票服务，请稍后重试。", {
      code: "NETWORK_ERROR",
      retryable: true,
    });
  } finally {
    window.clearTimeout(timeout);
  }
}

async function requestBinary(
  path: string,
  _role: RequestRole,
  method: "GET" | "POST" = "GET",
) {
  const headers = new Headers();
  if (isMutation(method)) {
    headers.set("X-CSRF-Token", csrfTokenForMutation());
  }
  const response = await fetch(endpointURL(path), {
    method,
    headers,
    credentials: "include",
    redirect: "error",
  });
  const rotatedCSRF = response.headers.get("X-CSRF-Token");
  if (rotatedCSRF) sessionCSRFToken = rotatedCSRF;
  if (
    !response.ok ||
    !response.headers.get("content-type")?.startsWith("application/pdf")
  ) {
    if (!response.ok) {
      const payload = (await response.json().catch(() => undefined)) as
        | BackendError
        | undefined;
      throw responseError(response.status, payload);
    }
    throw new InvoiceApiError("发票 PDF 暂时无法下载，请稍后重试。", {
      code: "INVALID_PDF_RESPONSE",
      status: response.status,
    });
  }
  return response;
}
