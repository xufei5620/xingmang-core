import type {
  AdminReviewPayload,
  AdminAccessSettingsInput,
  AuthSession,
  DashboardSummary,
  EligibilityFreeze,
  EligibilityFreezeFilters,
  EligibilityFreezePage,
  FundingOrder,
  InvoiceProfile,
  InvoiceDeliveryState,
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
  getAdminRequestPage(cursor?: string): Promise<InvoiceRequestPage>;
  getInvoiceRequestDetail(
    requestId: string,
    admin?: boolean,
  ): Promise<InvoiceRequest>;
  getSourceHealth(): Promise<SourceHealthReport>;
  getSummary(admin?: boolean): Promise<DashboardSummary>;
  submitInvoice(payload: SubmitInvoicePayload): Promise<InvoiceRequest>;
  cancelInvoice(request: InvoiceRequest): Promise<InvoiceRequest>;
  getPaymentCandidates(cursor?: string): Promise<PaymentCandidatePage>;
	verifyPayment(
		candidateId: string,
		input: VerifyPaymentInput,
	): Promise<"proposed" | "verified">;
  rejectPayment(candidateId: string, input: RejectPaymentInput): Promise<void>;
	applyManualPaymentCap(candidateId: string, input: ManualPaymentCapInput): Promise<void>;
  getRefundCases(
    status: RefundCaseStatus,
    cursor?: string,
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
  downloadInvoiceDocument(request: InvoiceRequest): Promise<void>;
  downloadAdminInvoiceDocument(request: InvoiceRequest): Promise<void>;
  getAdminSettings(): Promise<InvoiceSystemSettings>;
  saveInvoiceRules(input: InvoiceRuleSettingsInput): Promise<void>;
  saveSMTPSettings(input: SMTPSettingsInput): Promise<void>;
  sendSMTPTest(): Promise<void>;
  saveAdminAccess(input: AdminAccessSettingsInput): Promise<void>;
}
