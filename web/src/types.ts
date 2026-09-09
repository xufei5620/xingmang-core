import type {
  LotEligibilityStatusWire,
  LotReasonCodeWire as GeneratedLotReasonCode,
  SummaryReasonWire,
} from "./lib/eligibility-wire.generated";

// XM-INV-LOT-REASON-CONTRACT. The values the backend can put on the wire are
// the backend's fact; they are declared once in
// contracts/invoice-eligibility-wire.v1.json, pinned there by Go probes that
// run the real emitters, and generated into ./lib/eligibility-wire.generated.
// Nothing in this repository may hand-copy them again -- four separate copies
// in this file and http-api.ts are what took the user's invoice page down when
// XM-INV-ELIG-AUTO-RECONCILE added a fourth persisted status.
//
// Each wire type is widened with `(string & {})`, the same way
// EligibilityFreezeReason already was. That widening is not laziness about
// types: it is the type-level admission that "the backend is one deploy ahead
// of this bundle" is a normal, recurring state rather than an error. The
// generated literals still drive autocomplete and still make every
// `Record<..., string>` label table exhaustive, so forgetting a Chinese label
// for a newly added value is a typecheck failure.
export type EligibilityStatusWire = LotEligibilityStatusWire | (string & {});
export type LotReasonCodeWire = GeneratedLotReasonCode | (string & {});
export type EligibilitySummaryReasonWire = SummaryReasonWire | (string & {});

// The exact, non-widened counterparts. Label tables key on THESE, which makes
// the compiler reject `labels[someWireValue]` -- indexing a widened value into
// a table of known keys is precisely how an unrecognised status turns into an
// empty badge instead of a sentence. Callers are pushed to the `...Label()`
// accessors below the tables, which supply the fallback wording.
export type EligibilityStatusKnown = LotEligibilityStatusWire;
export type LotReasonCodeKnown = GeneratedLotReasonCode;

export type SourceType = "sub2api" | "newapi";
// Widened for the one place a platform code arrives from the network without
// the frontend having a say in it: a bound source account's `source_type`.
// A third platform, or a backend that ships one deploy ahead of this bundle,
// used to be a hard INVALID_SOURCE_ACCOUNT throw that rejected the WHOLE
// accounts request -- and an empty accounts list is the binding wizard (see
// user-data-load.ts). Same shape as EligibilityStatusWire: the known union
// keeps its label table exhaustive, the wire value may be something newer.
export type SourceTypeWire = SourceType | (string & {});
export type UserRole = "user" | "admin";
export type VerificationState = "verified" | "pending" | "attention";
export type InvoiceProfileType = "personal" | "enterprise";
export type InvoiceStatus = "submitted" | "reviewing" | "returned" | "issued";
export type InvoiceWorkflowStatus =
  | "pending_review"
  | "needs_changes"
  | "approved"
  | "rejected"
  | "user_cancelled"
  | "manual_issuing"
  | "issued_awaiting_document"
  | "issued"
  | "refund_attention";

export interface FundingOrder {
  id: string;
  source: SourceType;
  sourceInstanceId: string;
  sourceLabel: string;
  tradeNo: string;
  paidAt: string;
  paidMinor: number;
  consumedCashMinor: number;
  reservedMinor: number;
  issuedMinor: number;
  availableMinor: number;
  currency: "CNY";
  eligibilityKind: "wallet" | "subscription" | "legacy" | "noncash";
  eligibilityStatus: EligibilityStatusWire;
  reasonCode?: LotReasonCodeWire;
  verification: VerificationState;
  refundFrozen: boolean;
  paymentMethod: string;
  description: string;
  // True when this lot carried an eligibility_status or reason_code this
  // bundle does not know. The lot still renders (with a fallback Chinese
  // label that quotes the raw code) and is still unselectable, because a
  // non-active status can never carry a positive availableMinor. It is a
  // distinct flag rather than a special status value so the "we are behind
  // the backend" case stays visible to operators instead of being laundered
  // into a state the UI already had a meaning for.
  eligibilityDegraded?: boolean;
}

export interface AuthUser {
  id: string;
  displayName: string;
  email: string;
  emailVerified: boolean;
  role: UserRole;
  platform: SourceType | null;
  // Non-null only alongside `platform` (a platform-password session): the
  // account's ID on that source platform, e.g. New API's numeric user ID.
  platformUserId: string | null;
  // The raw account name captured from the platform's own login response
  // (Sub2API/New API), unlike `displayName` above which the backend always
  // backfills with a generic placeholder when there is nothing better to
  // show. Persisted encrypted on the session row (backend migration 0017),
  // so it survives a reload; null only for an OIDC session, or a
  // platform-password session issued before that migration.
  username: string | null;
}

export interface PlatformLoginInput {
  // Omitted in the normal user-facing flow: the backend auto-detects which
  // platform an identifier belongs to (email-shaped -> try Sub2API, then New
  // API; otherwise New API only). An explicit value is still accepted for
  // ops/test callers that want the old single-platform-only behavior.
  platform?: SourceType;
  identifier: string;
  password: string;
}

export interface PlatformLoginTwoFAInput {
  // Omitted for the same reason as PlatformLoginInput.platform: the backend
  // already remembers which platform issued `tempToken` from the initial
  // login call.
  platform?: SourceType;
  tempToken: string;
  code: string;
}

export type PlatformLoginOutcome =
  | { ok: true; requiresTwoFA?: false; tempToken?: never }
  | { ok: true; requiresTwoFA: true; tempToken: string };

export type AuthSession =
  | {
      authenticated: false;
      user?: never;
      csrfToken?: never;
      // CR-0006 (XM-INV-CONSOLE-ASSERT): whether the OIDC administrator
      // login entry should be offered at all. False once an operator turns
      // OIDC_ADMIN_LOGIN_ENABLED off (phase 2 -- default stays true today).
      oidcAdminLoginEnabled: boolean;
    }
  | {
      authenticated: true;
      user: AuthUser;
      csrfToken: string;
      adminStepUpRequired: boolean;
      oidcAdminLoginEnabled: boolean;
    };

export interface SourceAccount {
  id: string;
  // Widened (see SourceTypeWire): a platform this bundle does not know still
  // renders as a row -- labelled 「未识别的平台」 -- instead of rejecting the
  // whole accounts request and dropping the user into the binding wizard.
  source: SourceTypeWire;
  sourceInstanceId: string;
  sourceLabel: string;
  externalUserIdMasked: string;
  status: "pending" | "verified" | "frozen" | "revoked";
  verifiedAt?: string;
  lastObservedAt?: string;
  // There is deliberately no `sourceDegraded` flag next to this. Unlike a
  // summary (whose status can be known while an unknown envelope key still
  // degrades it), "this row's platform is unknown" is exactly
  // `!isKnownSourceType(source)` -- a second field would be a copy that can
  // drift. Renderers derive the 「未识别的平台」 badge from `source` itself
  // (see SourceBadge in App.tsx and sourceTypeLabel in lib/source-labels.ts).
}

// Another hand-copy of a backend enum this slice removed: this list was
// missing PENDING_RECONCILIATION, which the summary endpoint has emitted since
// XM-INV-ELIG-AUTO-RECONCILE. It now derives from the generated contract.
//
// Deliberately the EXACT generated union, not the widened one. Label tables key
// on this type, and `Record<string, string>` would accept a table missing any
// number of entries -- the widening belongs on the data that arrives from the
// network, never on the set we promise to have Chinese labels for.
export type EligibilitySummaryReason = SummaryReasonWire;

export interface ServiceUnitSummary {
  serviceUnits: string;
  unitCode: "SUB2_BALANCE_1E8" | "NEWAPI_QUOTA" | null;
}

export interface UserEligibilitySummary {
  source: SourceType;
  sourceInstanceId: string;
  sourceLabel: string;
  bindingStatus: "pending" | "verified" | "frozen" | "revoked";
  status: FundingOrder["eligibilityStatus"];
  currency: "CNY";
  availableMinor: number;
  consumedMinor: number;
  unconsumedMinor: number;
  reservedMinor: number;
  issuedMinor: number;
  legacyNoninvoiceable: ServiceUnitSummary;
  noncash: ServiceUnitSummary;
  // Widened: a summary that arrives carrying a reason this bundle predates
  // renders with a fallback label rather than blanking the panel.
  reasons: EligibilitySummaryReasonWire[];
  // See FundingOrder.eligibilityDegraded.
  eligibilityDegraded?: boolean;
}

export type EligibilityFreezeReason =
  | "UNKNOWN_NEGATIVE_BALANCE"
  | "LATE_FINALIZED_EVENT"
  | "AMBIGUOUS_EVENT_ORDER"
  | "EVENT_PAYLOAD_DRIFT"
  | "UNIT_MISMATCH"
  | "USAGE_EXCEEDS_LEDGER"
  | "STREAM_WATERMARK_REGRESSION"
  | "SOURCE_GAP"
  | "SOURCE_REFUND"
  | "EVENT_DEAD"
  | "POLICY_ANCHOR_BLOCKED";

export interface EligibilityFreeze {
  id: string;
  source: SourceType;
  sourceInstanceId: string;
  sourceLabel: string;
  scope: "account" | "funding_lot";
  // A known EligibilityFreezeReason, or any other well-formed (but not yet
  // catalogued/labeled) reason code the backend's own freeze_reason enum
  // may add in the future -- `& { readonly brand?: unique symbol }`-free
  // widening (`string & {}`) keeps literal-type autocomplete for the known
  // values while still accepting an unrecognized one at the type level, to
  // match mapEligibilityFreeze's own tolerant runtime validation (an
  // unrecognized-but-well-formed code must not fail the whole list).
  reason: EligibilityFreezeReason | (string & {});
  status: "open" | "resolved";
  eligibilityStatus: FundingOrder["eligibilityStatus"];
  openedAt: string;
  resolvedAt?: string;
  version: number;
  // CR-0007 problem one: the upstream platform's own (digital) user ID for
  // this account, plain and unmasked -- see docs/ELIGIBILITY-OPERATIONS.md.
  externalUserId: string;
  // The account's latest verified email, when it has one. Absent means the
  // account has no verified address on file -- render nothing rather than a
  // placeholder, which would read as an empty mailbox
  // (XM-INV-LEDGER-ACCOUNT-EMAIL).
  accountEmail?: string;
  // Which piece of evidence opened this freeze, when the row carries it
  // (XM-INV-FREEZE-TRIGGER-VISIBLE). Several freezes on one account can share
  // a reason, a scope and a second, and this is the only thing that tells
  // them apart.
  triggerObjectType?: string;
  triggerObjectId?: string;
}

export interface EligibilityFreezePage {
  items: EligibilityFreeze[];
  nextCursor?: string;
}

export interface EligibilityFreezeFilters {
  status: "open" | "resolved" | "all";
  reason?: EligibilityFreezeReason;
  sourceInstanceId?: string;
  // CR-0007 problem one: exact match on the upstream platform's external
  // user ID, optionally combined with sourceInstanceId to disambiguate.
  externalUserId?: string;
}

export interface ResolveEligibilityFreezeInput {
  version: number;
  evidenceReference: string;
  note: string;
}

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW): the operator "用户账本" view --
// GET /api/v1/admin/accounts/ledger (list) and
// GET /api/v1/admin/accounts/{external_account_id}/ledger (detail).
// block_state is a read-only composition of existing signals server-side
// (see docs/ELIGIBILITY-OPERATIONS.md's "管理员账本视图" section); this
// frontend never computes it, only displays it.
export type AccountBlockState =
  | "frozen_manual_review"
  | "not_invoiceable_pending_reconciliation"
  // XM-INV-LEDGER-SETTLING-STATE: the account is only waiting for its own
  // projection to finish, which is the ordinary rhythm of a busy account --
  // not a disagreement between the books and the source.
  | "settling"
  | "below_threshold"
  | "invoiceable";

export interface AccountLedgerListItem {
  externalAccountId: string;
  source: SourceType;
  externalUserId: string;
  policyStartAt: string;
  rechargesSinceStartCount: number;
  rechargesSinceStartMinor: number;
  consumedSinceStartMinor: number;
  invoiceableNowMinor: number;
  issuedMinor: number;
  thresholdReached: boolean;
  blockState: AccountBlockState;
  // Absent when this account has never had a reconciliation checkpoint or
  // carry-forward proof evaluated at all.
  lastCheckpointAt?: string;
  // Same optional display address as EligibilityFreeze.accountEmail above.
  accountEmail?: string;
}

export interface AccountLedgerRecharge {
  fundingLotId: string;
  completedAt: string;
  amountMinor: number;
  eligibilityKind: "WALLET_CASH" | "SUBSCRIPTION_CASH";
  refundFrozen: boolean;
}

export interface AccountLedgerConsumptionDay {
  // Asia/Shanghai calendar date, "YYYY-MM-DD".
  date: string;
  consumedMinor: number;
}

export interface AccountLedgerDetail extends AccountLedgerListItem {
  openingBalance: { serviceUnits: string; unitCode: string };
  /** 系统第一次为这个账号建立可信余额基线的时刻（每个账号各不相同）。
   *  **不是**开票起点——那是全局策略起点，对所有账号一致、由库级约束保证。
   *  这两件事以前被「起点后充值」那一列悄悄混在一起，见
   *  XM-INV-LEDGER-RECHARGE-POLICY-START。ISO 8601，空串表示后端未提供。 */
  cutoverAt: string;
  recharges: AccountLedgerRecharge[];
  consumptionTimeline: AccountLedgerConsumptionDay[];
  // A concrete Chinese sentence naming the blocking fact, present unless
  // blockState is "invoiceable" (nothing to explain).
  blockReason?: string;
  // Absent when no evaluation for this account has ever been 'matched'.
  lastReconciledAt?: string;
}

export interface AccountLedgerPage {
  items: AccountLedgerListItem[];
  nextCursor?: string;
}

export interface AccountLedgerFilters {
  externalUserId?: string;
  sourceInstanceId?: string;
  // Default (undefined) sorts by invoiceable_now_minor descending;
  // "block_state" sorts frozen_manual_review first (see the backend's own
  // accountBlockStateRankExpr).
  sort?: "block_state";
}

export interface PaymentCandidate {
  id: string;
  source: "newapi";
  principalId: string;
  principalDisplayName?: string;
  sourceInstanceId: string;
  externalOrderId: string;
  tradeNo: string;
  quotedMinor: number;
  currentCapMinor: number;
  currency: "CNY";
  verification: "pending" | "frozen" | "verified";
	reviewStage: "unreviewed" | "proposed" | "approved";
  sourceStatus: string;
  observedAt: string;
}

export interface PaymentCandidatePage {
  items: PaymentCandidate[];
  nextCursor?: string;
}

export interface VerifyPaymentInput {
  paidMinor: number;
  currency: "CNY";
  evidenceReference: string;
}

export interface RejectPaymentInput {
  reason: string;
  evidenceReference: string;
}

export interface ManualPaymentCapInput {
	newCapMinor: number;
	evidenceReference: string;
	reason: string;
}

export type RefundCaseStatus =
  | "open"
  | "resolved_red_letter"
  | "resolved_no_action";

export interface RefundCase {
  id: string;
  requestId: string;
  fundingLotId: string;
  sourceRevision: string;
  observedRefundMinor: number;
  issuedExposureMinor: number;
  observedCapMinor: number;
  status: RefundCaseStatus;
  resolvedBy?: string;
  openedAt: string;
  updatedAt: string;
  resolvedAt?: string;
}

export interface RefundCasePage {
  items: RefundCase[];
  nextCursor?: string;
}

export interface ResolveRefundCaseInput {
  resolutionStatus: Exclude<RefundCaseStatus, "open">;
  evidenceReference: string;
  note: string;
}

export interface InvoiceProfile {
  id: string;
  revision: number;
  type: InvoiceProfileType;
  title: string;
  taxId: string;
  email: string;
  phone?: string;
  address?: string;
  bankName?: string;
  bankAccount?: string;
  isDefault: boolean;
}

export interface InvoiceAllocation {
  orderId: string;
  tradeNo: string;
  allocatedMinor: number;
}

export interface InvoiceRequest {
  id: string;
  requestNo: string;
  userName: string;
  userEmail: string;
  source: SourceType;
  sourceInstanceId?: string;
  version?: number;
  amountMinor: number;
  profileSnapshot: InvoiceProfile;
  allocations: InvoiceAllocation[];
  status: InvoiceStatus;
  workflowStatus?: InvoiceWorkflowStatus;
  submittedAt: string;
  updatedAt: string;
  reviewer?: string;
  reviewNote?: string;
  invoiceNumber?: string;
  pdfName?: string;
  mailStatus: "not_sent" | "queued" | "sent" | "failed";
  newApiVerification: "not_required" | "pending" | "passed" | "failed";
}

export interface InvoiceRequestPage {
  items: InvoiceRequest[];
  nextCursor?: string;
}

export interface InvoiceDeliveryState {
  documentAvailable: boolean;
  invoiceNumber?: string;
  issuedAt?: string;
  mailStatus: InvoiceRequest["mailStatus"];
  mailAttempts: number;
  nextMailAttemptAt?: string;
}

/** 一条企业微信通知的投递事实（XM-INV-NOTICE-UI）。
 *
 *  **不含任何申请内容**：单号、金额、抬头这些在申请本身里，抄第二份只会
 *  多出一份可能不一致的事实。这里只回答"那条通知发出去了没有"。 */
export interface InvoiceNoticeDelivery {
  id: string;
  /** 事件键，今天只有 "request.submitted"。 */
  kind: string;
  /** "queued" | "sending" | "sent" | "failed"；未知取值原样带出，由界面兜底。 */
  status: string;
  attemptCount: number;
  nextAttemptAt?: string;
  /** 未送达时是 undefined，**不是零时刻**。 */
  deliveredAt?: string;
  /** 我方分类过的短码，不含 Webhook 地址与上游原文（后端保证）。 */
  lastErrorCode: string;
  createdAt?: string;
}

export interface DashboardSummary {
  totalAvailableMinor: number;
  reviewingMinor: number;
  issuedThisYearMinor: number;
  pendingCount: number;
}

export interface InvoicePolicy {
  minimumRequestMinor: number;
  serviceItem: "技术服务";
  eligibilityStartAt: string;
  eligibilityPolicyVersion: number;
  eligibilityTimezone: "Asia/Shanghai";
  eligibilityRule: "payment_and_usage_at_or_after";
}

export interface SourceStreamHealth {
  sourceInstanceId: string;
  sourceType: SourceType;
  sourceName: string;
  sourceEnabled: boolean;
  streamId: "payments" | "identities" | "usage" | "credits" | "balances";
  sequence: number;
  approvedRuntimeVersion: string;
  // XM-INV-SOURCE-RUNTIME-PIN: the runtime the sealed cutover manifest was captured under.
  cutoverRuntimeVersion: string;
  observedRuntimeVersion: string;
  observedAgentVersion: string;
  projectionStatus: "healthy" | "blocked" | "unknown";
  lastAcceptedAt?: string;
  lastNonemptyBatchAt?: string;
  economicWatermarkAt?: string;
  maximumAgeSeconds: number;
  economicWatermarkMaximumAgeSeconds?: number;
  pendingEvents: number;
  deadEvents: number;
  // XM-INV-DEAD-CONTAINMENT: the subset of deadEvents already answered for by
  // an open eligibility freeze. deadEvents stays the honest total, so the
  // difference is what still has nobody accountable for it -- and it is the
  // difference, not the total, that makes the stream not ready.
  containedDeadEvents: number;
  waitingDependencies: number;
  ready: boolean;
  reasons: string[];
}

// EligibilityProjectionHealth is the eligibility-projection worker's own
// queue grading (XM-INV-PROJECTION-FAILURE-GRADING), reported alongside the
// per-stream rows by the same admin endpoint. It is global rather than
// per-source: one queue serves every account on both platforms.
export interface EligibilityProjectionHealth {
  queued: number;
  processing: number;
  retrying: number;
  dead: number;
  proofPending: number;
  oldestPendingAt?: string;
  oldestProofPendingAt?: string;
}

export interface SourceHealthReport {
  ready: boolean;
  degradedHTTP: boolean;
  items: SourceStreamHealth[];
  // Optional because a server predating XM-INV-PROJECTION-FAILURE-GRADING
  // omits the block entirely; the screen says so rather than showing zeros
  // that would read as a healthy queue.
  eligibilityProjection?: EligibilityProjectionHealth;
}

export interface SubmitInvoicePayload {
  source: SourceType;
  sourceInstanceId?: string;
  profileId: string;
  allocations: InvoiceAllocation[];
}

export interface AdminReviewPayload {
  requestId: string;
  version?: number;
  action: "approve" | "return";
  note: string;
}

export interface InvoiceSystemSettings {
  revision: number;
  issuerConfigured: boolean;
  issuerName: string;
  serviceItem: "技术服务";
  minimumRequestMinor: number;
  eligibilityStartAt: string;
  eligibilityPolicyVersion: number;
  eligibilityTimezone: "Asia/Shanghai";
  eligibilityRule: "payment_and_usage_at_or_after";
  smtp: {
    fromAddress: string;
    fromName: string;
    host: string;
    port: number;
    startTLS: boolean;
    credentialConfigured: boolean;
    /** 测试邮件收件人的遮蔽形式；未配置时为空串。**没有明文**——与 SMTP
     *  授权码、企业微信地址同一条纪律：设置响应只回遮蔽值，改地址靠输入即覆盖。 */
    testRecipientMasked: string;
    /** true = 地址存在库里（管理员在后台设过）；false = 仍在用服务器环境变量
     *  兜底的那个（过渡期，见迁移 0030），或者两处都没有。 */
    testRecipientManaged: boolean;
  };
  adminAccess: {
    cidrs: string[];
    currentIP: string;
  };
  /** 企业微信通知地址（XM-INV-NOTICE-WEBHOOK-SETTING）。
   *  **没有地址本身**：整个 URL 是凭据，页面只拿得到"配没配"与指纹。 */
  noticeWebhook: {
    configured: boolean;
    /** sha256 前缀，可与"我刚才粘的那个"比对，反推不出地址。 */
    fingerprint: string;
    updatedBy: string;
    /** 未配置时为 null——不是零时刻。 */
    updatedAt: string | null;
  };
}

export interface InvoiceRuleSettingsInput {
  revision: number;
  issuerName: string;
  minimumRequestMinor: number;
}

export interface SMTPSettingsInput {
  revision: number;
  fromAddress: string;
  fromName: string;
  host: string;
  port: number;
  startTLS: boolean;
  authorizationCode?: string;
  /** 测试邮件收件人。**不传=保持库里现值不变**（不会因为一次普通保存被清掉）；
   *  传空串=明确清空，回到环境变量兜底。 */
  testRecipient?: string;
}

export interface AdminAccessSettingsInput {
  revision: number;
  cidrs: string[];
}
