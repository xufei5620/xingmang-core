import type {
  AdminReviewPayload,
  AdminAccessSettingsInput,
  AccountLedgerDetail,
  AccountLedgerFilters,
  AccountLedgerPage,
  AuthSession,
  DashboardSummary,
  EligibilityFreeze,
  EligibilityFreezeFilters,
  EligibilityFreezePage,
  FundingOrder,
  InvoiceProfile,
  InvoiceDeliveryState,
  InvoiceNoticeDelivery,
  InvoicePolicy,
  InvoiceRequest,
  InvoiceRequestPage,
  InvoiceRuleSettingsInput,
  InvoiceSystemSettings,
  PaymentCandidatePage,
  PlatformLoginInput,
  PlatformLoginOutcome,
  PlatformLoginTwoFAInput,
  RefundCase,
  RefundCasePage,
  RefundCaseStatus,
  RejectPaymentInput,
	ManualPaymentCapInput,
  ResolveRefundCaseInput,
  ResolveEligibilityFreezeInput,
  SourceAccount,
  SourceHealthReport,
  SMTPSettingsInput,
  SubmitInvoicePayload,
  UserEligibilitySummary,
  VerifyPaymentInput,
} from "../types";

export type ApiMode = "mock" | "http";

export type ApiCapabilities = {
  documentUpload: boolean;
  documentDownload: boolean;
  mailResend: boolean;
};

export class InvoiceApiError extends Error {
  readonly code: string;
  readonly status?: number;
  readonly retryable: boolean;
  readonly requiresLogin: boolean;
  readonly requiresStepUp: boolean;

  constructor(
    message: string,
    options: {
      code: string;
      status?: number;
      retryable?: boolean;
      requiresLogin?: boolean;
      requiresStepUp?: boolean;
    },
  ) {
    super(message);
    this.name = "InvoiceApiError";
    this.code = options.code;
    this.status = options.status;
    this.retryable = options.retryable ?? false;
    this.requiresLogin = options.requiresLogin ?? false;
    this.requiresStepUp = options.requiresStepUp ?? false;
  }
}

export type AuthFailure = "login" | "step-up";
const authFailureListeners = new Set<(failure: AuthFailure) => void>();

export function subscribeAuthFailures(listener: (failure: AuthFailure) => void) {
  authFailureListeners.add(listener);
  return () => {
    authFailureListeners.delete(listener);
  };
}

export function publishAuthFailure(failure: AuthFailure) {
  for (const listener of authFailureListeners) listener(failure);
}

export interface InvoiceApiClient {
  readonly mode: ApiMode;
  readonly capabilities: ApiCapabilities;
  getSession(): Promise<AuthSession>;
  logout(): Promise<string | null>;
  loginURL(returnTo: string): string;
  adminStepUpURL(returnTo: string): string;
  // CR-0006 (XM-INV-CONSOLE-ASSERT): exchanges a signed console-assertion
  // credential (received via postMessage, see AuthProvider.tsx) for a
  // session, the same way loginURL's OIDC redirect does but without ever
  // leaving this page. Throws InvoiceApiError on any rejection -- the
  // caller does not get to distinguish which one (see the backend's
  // ASSERTION_INVALID error code, deliberately unified).
  exchangeConsoleAssertion(assertion: string): Promise<void>;
  platformLogin(input: PlatformLoginInput): Promise<PlatformLoginOutcome>;
  verifyPlatformLoginTwoFA(
    input: PlatformLoginTwoFAInput,
  ): Promise<PlatformLoginOutcome>;
  getSourceAccounts(): Promise<SourceAccount[]>;
  getOrders(): Promise<FundingOrder[]>;
  getUserEligibilitySummary(): Promise<UserEligibilitySummary[]>;
  getInvoicePolicy(): Promise<InvoicePolicy>;
  getProfiles(): Promise<InvoiceProfile[]>;
  saveProfile(
    profile: Omit<InvoiceProfile, "id"> & { id?: string },
  ): Promise<InvoiceProfile>;
  getUserRequests(): Promise<InvoiceRequest[]>;
  getAdminRequests(): Promise<InvoiceRequest[]>;
  getUserRequestPage(cursor?: string): Promise<InvoiceRequestPage>;
  // sourceInstanceId: embedded-admin platform scoping only (XM-INV-ADMIN-
  // EMBED); undefined is unscoped, unchanged behavior.
  getAdminRequestPage(
    cursor?: string,
    sourceInstanceId?: string,
  ): Promise<InvoiceRequestPage>;
  getInvoiceRequestDetail(
    requestId: string,
    admin?: boolean,
  ): Promise<InvoiceRequest>;
  getSourceHealth(): Promise<SourceHealthReport>;
  getSummary(admin?: boolean): Promise<DashboardSummary>;
  submitInvoice(payload: SubmitInvoicePayload): Promise<InvoiceRequest>;
  cancelInvoice(request: InvoiceRequest): Promise<InvoiceRequest>;
  getPaymentCandidates(
    cursor?: string,
    sourceInstanceId?: string,
  ): Promise<PaymentCandidatePage>;
	verifyPayment(
		candidateId: string,
		input: VerifyPaymentInput,
	): Promise<"proposed" | "verified">;
  rejectPayment(candidateId: string, input: RejectPaymentInput): Promise<void>;
	applyManualPaymentCap(candidateId: string, input: ManualPaymentCapInput): Promise<void>;
  getRefundCases(
    status: RefundCaseStatus,
    cursor?: string,
    sourceInstanceId?: string,
  ): Promise<RefundCasePage>;
  resolveRefundCase(
    caseId: string,
    input: ResolveRefundCaseInput,
  ): Promise<RefundCase>;
  getEligibilityFreezes(
    filters: EligibilityFreezeFilters,
    cursor?: string,
  ): Promise<EligibilityFreezePage>;
  resolveEligibilityFreeze(
    freezeId: string,
    input: ResolveEligibilityFreezeInput,
  ): Promise<EligibilityFreeze>;
  // CR-0009 (XM-INV-CR0009-LEDGER-VIEW): the operator "用户账本" view.
  getAccountLedger(
    filters: AccountLedgerFilters,
    cursor?: string,
  ): Promise<AccountLedgerPage>;
  getAccountLedgerDetail(externalAccountId: string): Promise<AccountLedgerDetail>;
  adminReview(payload: AdminReviewPayload): Promise<void>;
  adminUploadInvoice(
    request: InvoiceRequest,
    file: File,
    invoiceNumber: string,
    issuedAt: string,
  ): Promise<void>;
  resendMail(request: InvoiceRequest): Promise<void>;
  getDeliveryState(
    request: InvoiceRequest,
    admin?: boolean,
  ): Promise<InvoiceDeliveryState>;
  /** 一份申请的企业微信通知投递状态（**管理端专用**，没有 user 变体：
   *  这是运维事实，不是申请人的业务数据）。 */
  listRequestNotices(request: InvoiceRequest): Promise<InvoiceNoticeDelivery[]>;
  downloadInvoiceDocument(request: InvoiceRequest): Promise<void>;
  downloadAdminInvoiceDocument(request: InvoiceRequest): Promise<void>;
  getAdminSettings(): Promise<InvoiceSystemSettings>;
  saveInvoiceRules(input: InvoiceRuleSettingsInput): Promise<void>;
  saveSMTPSettings(input: SMTPSettingsInput): Promise<void>;
  sendSMTPTest(): Promise<void>;
  saveAdminAccess(input: AdminAccessSettingsInput): Promise<void>;
  /** 保存（或覆盖）企业微信通知地址。**保存后永不回读**——想确认配对没有，
   *  用 sendNoticeWebhookTest。 */
  saveNoticeWebhook(webhookURL: string): Promise<void>;
  clearNoticeWebhook(): Promise<void>;
  /** 往已保存的地址发一条测试消息。消息到没到那个群，比看一段前缀可靠。 */
  sendNoticeWebhookTest(): Promise<void>;
}
