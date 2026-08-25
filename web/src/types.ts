export type SourceType = "sub2api" | "newapi";
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
  eligibilityStatus:
    | "active"
    | "syncing"
    | "frozen"
    | "missing"
    | "source_unavailable";
  reasonCode?:
    | "SOURCE_REFUND"
    | "LEDGER_SYNCING"
    | "LEDGER_FROZEN"
    | "SOURCE_NOT_READY"
    | "BEFORE_ELIGIBILITY_START"
    | "NO_POST_START_CONSUMPTION"
    | "SUBSCRIPTION_USAGE_UNSUPPORTED";
  verification: VerificationState;
  refundFrozen: boolean;
  paymentMethod: string;
  description: string;
}

export interface AuthUser {
  id: string;
  displayName: string;
  email: string;
  emailVerified: boolean;
  role: UserRole;
}

export type AuthSession =
  | { authenticated: false; user?: never; csrfToken?: never }
  | {
      authenticated: true;
      user: AuthUser;
      csrfToken: string;
      adminStepUpRequired: boolean;
    };

export interface SourceAccount {
  id: string;
  source: SourceType;
  sourceInstanceId: string;
  sourceLabel: string;
  externalUserIdMasked: string;
  status: "pending" | "verified" | "frozen" | "revoked";
  verifiedAt?: string;
  lastObservedAt?: string;
}

export type EligibilitySummaryReason =
  | "BINDING_NOT_VERIFIED"
  | "ACCOUNT_FROZEN"
  | "PROJECTION_PENDING"
  | "SOURCE_NOT_READY"
  | "NO_CONSUMED_CASH"
  | "READY";

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
  reasons: EligibilitySummaryReason[];
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
  | "SOURCE_REFUND";

export interface EligibilityFreeze {
  id: string;
  source: SourceType;
  sourceInstanceId: string;
  sourceLabel: string;
  scope: "account" | "funding_lot";
  reason: EligibilityFreezeReason;
  status: "open" | "resolved";
  eligibilityStatus: FundingOrder["eligibilityStatus"];
  openedAt: string;
  resolvedAt?: string;
  version: number;
}

export interface EligibilityFreezePage {
  items: EligibilityFreeze[];
  nextCursor?: string;
}

export interface EligibilityFreezeFilters {
  status: "open" | "resolved" | "all";
  reason?: EligibilityFreezeReason;
  sourceInstanceId?: string;
}

export interface ResolveEligibilityFreezeInput {
  version: number;
  evidenceReference: string;
  note: string;
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
  waitingDependencies: number;
  ready: boolean;
  reasons: string[];
}

export interface SourceHealthReport {
  ready: boolean;
  degradedHTTP: boolean;
  items: SourceStreamHealth[];
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
  };
  adminAccess: {
    cidrs: string[];
    currentIP: string;
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
}

export interface AdminAccessSettingsInput {
  revision: number;
  cidrs: string[];
}
