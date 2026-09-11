import type {
  AccountLedgerDetail,
  AccountLedgerFilters,
  AccountLedgerListItem,
  AdminReviewPayload,
  AuthSession,
  DashboardSummary,
  EligibilityFreeze,
  EligibilityFreezeFilters,
  FundingOrder,
  InvoiceSystemSettings,
  InvoiceProfile,
  InvoiceRequest,
  PaymentCandidate,
  RefundCase,
  RefundCaseStatus,
  SourceAccount,
  SourceHealthReport,
  SourceType,
  SubmitInvoicePayload,
  UserEligibilitySummary,
} from "../types";
import { InvoiceApiError, type InvoiceApiClient } from "./api-contract";

const delay = (ms = 220) => new Promise((resolve) => setTimeout(resolve, ms));

/** 与后端 adminsettings.MaskTestRecipient 同规则：本地部分留 3 位，不足 3 位留 1 位。
 *  演示态必须跟真实响应长得一样，否则设置页的说明行在两种模式下对不上。 */
const maskTestRecipient = (value: string): string => {
  const trimmed = value.trim();
  const at = trimmed.lastIndexOf("@");
  if (at <= 0 || at === trimmed.length - 1) return "";
  const local = trimmed.slice(0, at);
  return `${local.slice(0, local.length < 3 ? 1 : 3)}***@${trimmed.slice(at + 1)}`;
};

const now = new Date().toISOString();

const mockSession: AuthSession = {
  authenticated: true,
  user: {
    id: "demo-admin",
    displayName: "演示财务管理员",
    email: "admin@example.test",
    emailVerified: true,
    role: "admin",
    platform: null,
    platformUserId: null,
    username: null,
  },
  csrfToken: "mock-csrf-token",
  adminStepUpRequired: false,
};

// Demo rows are always one of the two platforms this bundle knows (the
// summaries below index mockUnitBalances by it); the wire type is wider.
const sourceAccounts: (SourceAccount & { source: SourceType })[] = [
  {
    id: "link-sub2-demo",
    source: "sub2api",
    sourceInstanceId: "sub2-main",
    sourceLabel: "SoloV API",
    externalUserIdMasked: "sub2…1001",
    status: "verified",
    verifiedAt: "2026-03-01T08:00:00.000Z",
    lastObservedAt: now,
  },
  {
    id: "link-newapi-demo",
    source: "newapi",
    sourceInstanceId: "newapi-main",
    sourceLabel: "SoloV 模型平台",
    externalUserIdMasked: "newapi…2048",
    status: "verified",
    verifiedAt: "2026-03-01T08:05:00.000Z",
    lastObservedAt: now,
  },
];

let orders: FundingOrder[] = [
  {
    id: "ord_101",
    source: "sub2api",
    sourceInstanceId: "sub2-main",
    sourceLabel: "SoloV API",
    tradeNo: "S2A2026031800218",
    paidAt: "2026-03-18T03:26:00.000Z",
    paidMinor: 50000,
    consumedCashMinor: 50000,
    reservedMinor: 0,
    issuedMinor: 0,
    availableMinor: 50000,
    currency: "CNY",
    eligibilityKind: "wallet",
    eligibilityStatus: "active",
    verification: "verified",
    refundFrozen: false,
    paymentMethod: "支付宝",
    description: "钱包充值（按已消费现金开票）",
  },
  {
    id: "ord_102",
    source: "newapi",
    sourceInstanceId: "newapi-main",
    sourceLabel: "SoloV 模型平台",
    tradeNo: "WAFFO-28-1771152288012",
    paidAt: "2026-02-15T09:25:00.000Z",
    paidMinor: 39900,
    consumedCashMinor: 39900,
    reservedMinor: 20000,
    issuedMinor: 0,
    availableMinor: 19900,
    currency: "CNY",
    eligibilityKind: "wallet",
    eligibilityStatus: "active",
    verification: "verified",
    refundFrozen: false,
    paymentMethod: "银行卡",
    description: "钱包充值（按已消费现金开票）",
  },
  {
    id: "ord_103",
    source: "newapi",
    sourceInstanceId: "newapi-main",
    sourceLabel: "SoloV 模型平台",
    tradeNo: "ref_9fa890ef72b8632a",
    paidAt: "2026-01-26T12:08:00.000Z",
    paidMinor: 10000,
    consumedCashMinor: 0,
    reservedMinor: 0,
    issuedMinor: 0,
    availableMinor: 0,
    currency: "CNY",
    eligibilityKind: "wallet",
    eligibilityStatus: "active",
    verification: "pending",
    refundFrozen: false,
    paymentMethod: "Stripe",
    description: "钱包充值（按已消费现金开票）",
  },
  {
    id: "ord_104",
    source: "sub2api",
    sourceInstanceId: "sub2-main",
    sourceLabel: "SoloV API",
    tradeNo: "S2A2025120208109",
    paidAt: "2025-12-02T06:11:00.000Z",
    paidMinor: 20000,
    consumedCashMinor: 20000,
    reservedMinor: 0,
    issuedMinor: 20000,
    availableMinor: 0,
    currency: "CNY",
    eligibilityKind: "wallet",
    eligibilityStatus: "active",
    verification: "verified",
    refundFrozen: false,
    paymentMethod: "微信支付",
    description: "钱包充值（按已消费现金开票）",
  },
];

let profiles: InvoiceProfile[] = [
  {
    id: "profile_company",
    revision: 1,
    type: "enterprise",
    title: "上海星河智能科技有限公司",
    taxId: "91310115MA1K4X8P2C",
    email: "finance@example.com",
    phone: "021-6088 8821",
    address: "上海市浦东新区张江路 88 号",
    bankName: "招商银行上海张江支行",
    bankAccount: "6225 8800 1023 4499",
    isDefault: true,
  },
  {
    id: "profile_personal",
    revision: 1,
    type: "personal",
    title: "陈远",
    taxId: "",
    email: "chenyuan@example.com",
    isDefault: false,
  },
];

let requests: InvoiceRequest[] = [
  {
    id: "req_1008",
    requestNo: "INV-20260318-008",
    userName: "陈远",
    userEmail: "chenyuan@example.com",
    source: "newapi",
    amountMinor: 20000,
    profileSnapshot: profiles[0]!,
    allocations: [
      {
        orderId: "ord_102",
        tradeNo: "WAFFO-28-1771152288012",
        allocatedMinor: 20000,
      },
    ],
    status: "reviewing",
    submittedAt: "2026-03-18T06:22:00.000Z",
    updatedAt: "2026-03-18T07:06:00.000Z",
    mailStatus: "not_sent",
    newApiVerification: "pending",
  },
  {
    id: "req_1002",
    requestNo: "INV-20260203-002",
    userName: "陈远",
    userEmail: "chenyuan@example.com",
    source: "sub2api",
    amountMinor: 20000,
    profileSnapshot: profiles[0]!,
    allocations: [
      {
        orderId: "ord_104",
        tradeNo: "S2A2025120208109",
        allocatedMinor: 20000,
      },
    ],
    status: "issued",
    submittedAt: "2026-02-03T02:10:00.000Z",
    updatedAt: "2026-02-04T09:20:00.000Z",
    reviewer: "财务管理员",
    invoiceNumber: "26312000000128483921",
    pdfName: "SoloV-电子发票-20260204.pdf",
    mailStatus: "sent",
    newApiVerification: "not_required",
  },
  {
    id: "req_0997",
    requestNo: "INV-20260111-997",
    userName: "林晓",
    userEmail: "linxiao@example.com",
    source: "newapi",
    amountMinor: 58000,
    profileSnapshot: {
      ...profiles[0]!,
      id: "snapshot_997",
      title: "杭州云帆网络有限公司",
      taxId: "91330106MA2AXY8K7Q",
      email: "billing@yunfan.example",
    },
    allocations: [
      {
        orderId: "external_997",
        tradeNo: "ref_ac0c642b9732",
        allocatedMinor: 58000,
      },
    ],
    status: "returned",
    submittedAt: "2026-01-11T08:45:00.000Z",
    updatedAt: "2026-01-12T03:30:00.000Z",
    reviewer: "财务管理员",
    reviewNote: "纳税人识别号与企业抬头不一致，请核对后重新提交。",
    mailStatus: "not_sent",
    newApiVerification: "failed",
  },
];

let systemSettings: InvoiceSystemSettings = {
  revision: 1,
  issuerConfigured: true,
  issuerName: "上海星河智能科技有限公司",
  serviceItem: "技术服务",
  minimumRequestMinor: 20_000,
  eligibilityStartAt: "2026-08-31T16:00:00Z",
  eligibilityPolicyVersion: 1,
  eligibilityTimezone: "Asia/Shanghai",
  eligibilityRule: "payment_and_usage_at_or_after",
  smtp: {
    fromAddress: "invoice@solov.cc",
    fromName: "SoloV 开票中心",
    host: "smtp.qq.com",
    port: 587,
    startTLS: true,
    credentialConfigured: false,
    testRecipientMasked: "tes***@example.com",
    testRecipientManaged: true,
  },
  adminAccess: {
    cidrs: ["127.0.0.1/32", "::1/128"],
    currentIP: "127.0.0.1",
  },
  noticeWebhook: {
    configured: false,
    fingerprint: "",
    updatedBy: "",
    updatedAt: null,
  },
};

let paymentCandidates: PaymentCandidate[] = [
  {
    id: "ord_103",
    source: "newapi",
    principalId: "demo-user",
    principalDisplayName: "陈远",
    sourceInstanceId: "newapi-main",
    externalOrderId: "ref_9fa890ef72b8632a",
    tradeNo: "ref_9fa890ef72b8632a",
    quotedMinor: 10_000,
	currentCapMinor: 0,
    currency: "CNY",
    verification: "pending",
	reviewStage: "unreviewed",
    sourceStatus: "SUCCESS",
    observedAt: "2026-01-26T12:09:00.000Z",
  },
];

let refundCases: RefundCase[] = [
  {
    id: "refund_case_open_1",
    requestId: "req_1002",
    fundingLotId: "ord_104",
    sourceRevision: "sub2-revision-refund-demo",
    observedRefundMinor: 5_000,
    issuedExposureMinor: 5_000,
    observedCapMinor: 15_000,
    status: "open",
    openedAt: "2026-03-20T05:20:00.000Z",
    updatedAt: "2026-03-20T05:20:00.000Z",
  },
  {
    id: "refund_case_closed_1",
    requestId: "req_1002",
    fundingLotId: "ord_104",
    sourceRevision: "sub2-revision-refund-old",
    observedRefundMinor: 2_000,
    issuedExposureMinor: 2_000,
    observedCapMinor: 18_000,
    status: "resolved_red_letter",
    resolvedBy: "demo-admin",
    openedAt: "2026-02-20T05:20:00.000Z",
    updatedAt: "2026-02-21T06:00:00.000Z",
    resolvedAt: "2026-02-21T06:00:00.000Z",
  },
];

const mockUnitBalances: Record<
  SourceType,
  Pick<UserEligibilitySummary, "legacyNoninvoiceable" | "noncash">
> = {
  sub2api: {
    legacyNoninvoiceable: {
      serviceUnits: "1200000000",
      unitCode: "SUB2_BALANCE_1E8",
    },
    noncash: {
      serviceUnits: "250000000",
      unitCode: "SUB2_BALANCE_1E8",
    },
  },
  newapi: {
    legacyNoninvoiceable: {
      serviceUnits: "500000",
      unitCode: "NEWAPI_QUOTA",
    },
    noncash: {
      serviceUnits: "750000",
      unitCode: "NEWAPI_QUOTA",
    },
  },
};

function demoUnavailableSource() {
  const value = new URL(window.location.href).searchParams.get(
    "demo_source_unavailable",
  );
  return value === "sub2api" || value === "newapi" ? value : undefined;
}

function mockEligibilitySummaries(): UserEligibilitySummary[] {
  return sourceAccounts.map((account) => {
    const eligibleOrders = orders.filter(
      (order) =>
        order.sourceInstanceId === account.sourceInstanceId &&
        order.verification === "verified" &&
        (order.eligibilityKind === "wallet" ||
          order.eligibilityKind === "subscription"),
    );
    const consumedMinor = eligibleOrders.reduce(
      (total, order) => total + order.consumedCashMinor,
      0,
    );
    const availableMinor = eligibleOrders.reduce(
      (total, order) => total + order.availableMinor,
      0,
    );
    const sourceUnavailable = demoUnavailableSource() === account.source;
    return {
      source: account.source,
      sourceInstanceId: account.sourceInstanceId,
      sourceLabel: account.sourceLabel,
      bindingStatus: account.status,
      status: sourceUnavailable ? "source_unavailable" : "active",
      currency: "CNY",
      availableMinor: sourceUnavailable ? 0 : availableMinor,
      consumedMinor,
      unconsumedMinor: eligibleOrders.reduce(
        (total, order) =>
          total + Math.max(0, order.paidMinor - order.consumedCashMinor),
        0,
      ),
      reservedMinor: eligibleOrders.reduce(
        (total, order) => total + order.reservedMinor,
        0,
      ),
      issuedMinor: eligibleOrders.reduce(
        (total, order) => total + order.issuedMinor,
        0,
      ),
      ...mockUnitBalances[account.source],
      reasons: sourceUnavailable
        ? ["SOURCE_NOT_READY"]
        : availableMinor > 0
          ? ["READY"]
          : ["NO_CONSUMED_CASH"],
    };
  });
}

let eligibilityFreezes: EligibilityFreeze[] = [
  {
    id: "31000000-0000-4000-8000-000000000001",
    source: "newapi",
    sourceInstanceId: "newapi-main",
    sourceLabel: "SoloV 模型平台",
    scope: "account",
    reason: "SOURCE_GAP",
    status: "open",
    eligibilityStatus: "frozen",
    openedAt: "2026-08-20T08:30:00.000Z",
    version: 1,
    externalUserId: "1147",
    accountEmail: "chen.yuan@example.com",
  },
  {
    id: "31000000-0000-4000-8000-000000000002",
    source: "sub2api",
    sourceInstanceId: "sub2-main",
    sourceLabel: "SoloV API",
    scope: "funding_lot",
    reason: "SOURCE_REFUND",
    status: "open",
    eligibilityStatus: "frozen",
    openedAt: "2026-08-19T04:10:00.000Z",
    version: 1,
    externalUserId: "88210",
    accountEmail: "lin.qi@example.net",
  },
  {
    id: "31000000-0000-4000-8000-000000000003",
    source: "sub2api",
    sourceInstanceId: "sub2-main",
    sourceLabel: "SoloV API",
    scope: "account",
    reason: "AMBIGUOUS_EVENT_ORDER",
    status: "resolved",
    eligibilityStatus: "active",
    openedAt: "2026-08-10T02:20:00.000Z",
    resolvedAt: "2026-08-11T05:00:00.000Z",
    version: 2,
    externalUserId: "30044",
    accountEmail: "zhao.min@example.org",
  },
];

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW): one fixture account per block_state,
// matching the design doc's own illustrative example numbers where
// possible. Detail is the single source of truth; the list view's own
// entries are derived from it below (mirroring how the real backend's list
// endpoint is a summary projection of the same underlying account row the
// detail endpoint reads in full).
const accountLedgerDetails: Record<string, AccountLedgerDetail> = {
  "41000000-0000-4000-8000-000000000001": {
    externalAccountId: "41000000-0000-4000-8000-000000000001",
    source: "sub2api",
    externalUserId: "1147",
    accountEmail: "chen.yuan@example.com",
    policyStartAt: "2026-08-31T16:00:00.000Z",
    rechargesSinceStartCount: 3,
    rechargesSinceStartMinor: 128_000,
    consumedSinceStartMinor: 96_000,
    invoiceableNowMinor: 31_800,
    issuedMinor: 0,
    thresholdReached: true,
    blockState: "invoiceable",
    lastCheckpointAt: "2026-09-03T06:25:11.000Z",
    cutoverAt: new Date(Date.now() - 86_400_000).toISOString(),
    openingBalance: { serviceUnits: "48200", unitCode: "SUB2_BALANCE_1E8" },
    recharges: [
      {
        fundingLotId: "51000000-0000-4000-8000-000000000001",
        completedAt: "2026-09-02T03:11:00.000Z",
        amountMinor: 50_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
      {
        fundingLotId: "51000000-0000-4000-8000-000000000002",
        completedAt: "2026-09-02T09:40:00.000Z",
        amountMinor: 48_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
      {
        fundingLotId: "51000000-0000-4000-8000-000000000003",
        completedAt: "2026-09-03T01:05:00.000Z",
        amountMinor: 30_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
    ],
    consumptionTimeline: [
      { date: "2026-09-02", consumedMinor: 60_000 },
      { date: "2026-09-03", consumedMinor: 36_000 },
    ],
    lastReconciledAt: "2026-09-03T06:25:11.000Z",
  },
  "41000000-0000-4000-8000-000000000002": {
    externalAccountId: "41000000-0000-4000-8000-000000000002",
    source: "newapi",
    externalUserId: "8821",
    accountEmail: "wu.tao@example.net",
    policyStartAt: "2026-08-31T16:00:00.000Z",
    rechargesSinceStartCount: 1,
    rechargesSinceStartMinor: 20_000,
    consumedSinceStartMinor: 20_000,
    invoiceableNowMinor: 20_000,
    issuedMinor: 0,
    thresholdReached: false,
    blockState: "frozen_manual_review",
    lastCheckpointAt: "2026-09-02T18:00:00.000Z",
    cutoverAt: new Date(Date.now() - 86_400_000).toISOString(),
    openingBalance: { serviceUnits: "0", unitCode: "NEWAPI_QUOTA" },
    recharges: [
      {
        fundingLotId: "51000000-0000-4000-8000-000000000004",
        completedAt: "2026-09-01T10:00:00.000Z",
        amountMinor: 20_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
    ],
    consumptionTimeline: [{ date: "2026-09-01", consumedMinor: 20_000 }],
    lastReconciledAt: "2026-09-02T18:00:00.000Z",
    blockReason:
      "2026-09-02 18:00（Asia/Shanghai）因来源退款核实中被冻结（原因代码 SOURCE_REFUND，关联对象 funding_lot:51000000-0000-4000-8000-000000000004），需人工核实后在“资格冻结”页签手动解除。",
  },
  "41000000-0000-4000-8000-000000000003": {
    externalAccountId: "41000000-0000-4000-8000-000000000003",
    source: "sub2api",
    externalUserId: "34",
    accountEmail: "he.lan@example.com",
    policyStartAt: "2026-08-31T16:00:00.000Z",
    rechargesSinceStartCount: 2,
    rechargesSinceStartMinor: 50_000,
    consumedSinceStartMinor: 32_000,
    invoiceableNowMinor: 18_000,
    issuedMinor: 0,
    thresholdReached: false,
    blockState: "not_invoiceable_pending_reconciliation",
    lastCheckpointAt: "2026-09-03T06:25:11.000Z",
    cutoverAt: new Date(Date.now() - 86_400_000).toISOString(),
    openingBalance: { serviceUnits: "48200", unitCode: "SUB2_BALANCE_1E8" },
    recharges: [
      {
        fundingLotId: "51000000-0000-4000-8000-000000000005",
        completedAt: "2026-09-01T08:00:00.000Z",
        amountMinor: 30_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
      {
        fundingLotId: "51000000-0000-4000-8000-000000000006",
        completedAt: "2026-09-02T03:11:00.000Z",
        amountMinor: 20_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
    ],
    consumptionTimeline: [{ date: "2026-09-02", consumedMinor: 32_000 }],
    lastReconciledAt: "2026-09-02T18:04:02.000Z",
    blockReason:
      "2026-09-03 06:25（Asia/Shanghai）balance_checkpoint ckpt-xxx 上报余额差额 -4（单位 SUB2_BALANCE_1E8，非人民币元），预期 380，上报余额与账本预期存在负向差额，等待下一次核对。",
  },
  "41000000-0000-4000-8000-000000000004": {
    externalAccountId: "41000000-0000-4000-8000-000000000004",
    source: "sub2api",
    externalUserId: "56",
    policyStartAt: "2026-08-31T16:00:00.000Z",
    rechargesSinceStartCount: 1,
    rechargesSinceStartMinor: 5_000,
    consumedSinceStartMinor: 1_000,
    invoiceableNowMinor: 4_000,
    issuedMinor: 0,
    thresholdReached: false,
    blockState: "below_threshold",
    lastCheckpointAt: "2026-09-03T01:00:00.000Z",
    cutoverAt: new Date(Date.now() - 86_400_000).toISOString(),
    openingBalance: { serviceUnits: "0", unitCode: "SUB2_BALANCE_1E8" },
    recharges: [
      {
        fundingLotId: "51000000-0000-4000-8000-000000000007",
        completedAt: "2026-09-01T12:00:00.000Z",
        amountMinor: 5_000,
        eligibilityKind: "WALLET_CASH",
        refundFrozen: false,
      },
    ],
    consumptionTimeline: [{ date: "2026-09-01", consumedMinor: 1_000 }],
    lastReconciledAt: "2026-09-03T01:00:00.000Z",
    blockReason: "当前可开票金额 40.00 元未达到起票门槛 200.00 元，还差 160.00 元。",
  },
};

const accountLedgerItems: AccountLedgerListItem[] = Object.values(
  accountLedgerDetails,
).map(
  ({
    recharges: _recharges,
    consumptionTimeline: _consumptionTimeline,
    cutoverAt: _cutoverAt,
    openingBalance: _openingBalance,
    blockReason: _blockReason,
    lastReconciledAt: _lastReconciledAt,
    ...listItem
  }) => listItem,
);

export const mockInvoiceApi: InvoiceApiClient = {
  mode: "mock",
  capabilities: {
    documentUpload: true,
    documentDownload: true,
    mailResend: true,
  },
  async getSession() {
    await delay(80);
    return structuredClone(mockSession);
  },
  async logout() {
    await delay(80);
    return null;
  },
  async getStaffSession() {
    return structuredClone(mockSession);
  },
  async platformLogin() {
    await delay(80);
    return { ok: true };
  },
  async verifyPlatformLoginTwoFA() {
    await delay(80);
    return { ok: true };
  },
  async getSourceAccounts() {
    await delay(100);
    if (
      new URL(window.location.href).searchParams.get("demo_empty_sources") ===
      "1"
    )
      return [];
    return structuredClone(sourceAccounts);
  },
  async getOrders() {
    await delay();
    const unavailable = demoUnavailableSource();
    return structuredClone(
      orders.map((order) =>
        unavailable === order.source
          ? {
              ...order,
              availableMinor: 0,
              eligibilityStatus: "source_unavailable" as const,
              reasonCode: "SOURCE_NOT_READY" as const,
            }
          : order,
      ),
    );
  },

  async getUserEligibilitySummary() {
    await delay(120);
    return structuredClone(mockEligibilitySummaries());
  },

  async getInvoicePolicy() {
    await delay(80);
    return {
      minimumRequestMinor: systemSettings.minimumRequestMinor,
      serviceItem: systemSettings.serviceItem,
      eligibilityStartAt: systemSettings.eligibilityStartAt,
      eligibilityPolicyVersion: systemSettings.eligibilityPolicyVersion,
      eligibilityTimezone: systemSettings.eligibilityTimezone,
      eligibilityRule: systemSettings.eligibilityRule,
    } as const;
  },

  async getProfiles() {
    await delay(120);
    return structuredClone(profiles);
  },

  async saveProfile(profile: Omit<InvoiceProfile, "id"> & { id?: string }) {
    await delay();
    const existing = profile.id
      ? profiles.find((item) => item.id === profile.id)
      : undefined;
    if (existing && existing.revision !== profile.revision) {
      throw new InvoiceApiError("资料已被更新，请刷新后重试。", {
        code: "VERSION_CONFLICT",
        status: 409,
      });
    }
    const saved: InvoiceProfile = {
      ...profile,
      id: profile.id ?? `profile_${Date.now()}`,
      revision: existing ? existing.revision + 1 : 1,
    };
    if (saved.isDefault)
      profiles = profiles.map((item) => ({ ...item, isDefault: false }));
    const index = profiles.findIndex((item) => item.id === saved.id);
    if (index >= 0) profiles[index] = saved;
    else profiles.push(saved);
    return structuredClone(saved);
  },

  async getUserRequests() {
    await delay();
    return structuredClone(
      requests.filter((request) => request.userName === "陈远"),
    );
  },

  async getAdminRequests() {
    await delay();
    return structuredClone(requests);
  },

  async getUserRequestPage(cursor) {
    await delay();
    const visible = requests.filter((request) => request.userName === "陈远");
    const start = cursor ? Number(cursor) : 0;
    const items = visible.slice(start, start + 1);
    return {
      items: structuredClone(items),
      nextCursor: start + 1 < visible.length ? String(start + 1) : undefined,
    };
  },

  async getAdminRequestPage(cursor, sourceInstanceId) {
    await delay();
    const scoped = sourceInstanceId
      ? requests.filter((request) => request.sourceInstanceId === sourceInstanceId)
      : requests;
    const start = cursor ? Number(cursor) : 0;
    const items = scoped.slice(start, start + 2);
    return {
      items: structuredClone(items),
      nextCursor: start + 2 < scoped.length ? String(start + 2) : undefined,
    };
  },

  async getInvoiceRequestDetail(requestId, admin = false) {
    await delay(100);
    const request = requests.find((item) => item.id === requestId);
    if (!request || (!admin && request.userName !== "陈远"))
      throw new Error("开票申请不存在或无权访问");
    return structuredClone(request);
  },

  async getSourceHealth(): Promise<SourceHealthReport> {
    await delay(100);
    return {
      ready: true,
      degradedHTTP: false,
      items: (["sub2api", "newapi"] as const).flatMap((source, sourceIndex) =>
        (["payments", "identities", "usage", "credits", "balances"] as const).map((stream) => ({
          sourceInstanceId: `10000000-0000-4000-8000-00000000000${sourceIndex + 1}`,
          sourceType: source,
          sourceName: source === "sub2api" ? "SoloV Sub2API" : "SoloV New API",
          sourceEnabled: true,
          streamId: stream,
          sequence: 128,
          approvedRuntimeVersion: source === "sub2api" ? "0.1.179" : "v1.0.0-rc.25",
          cutoverRuntimeVersion: source === "sub2api" ? "0.1.179" : "v1.0.0-rc.25",
          observedRuntimeVersion: source === "sub2api" ? "0.1.179" : "v1.0.0-rc.25",
          observedAgentVersion: "0.2.0",
          projectionStatus: "healthy" as const,
          lastAcceptedAt: new Date().toISOString(),
          economicWatermarkAt:
            stream === "identities" ? undefined : new Date().toISOString(),
          maximumAgeSeconds:
            stream === "identities" ? 900 : 300,
          economicWatermarkMaximumAgeSeconds:
            stream === "identities" ? undefined : 900,
          pendingEvents: 0,
          deadEvents: 0,
          containedDeadEvents: 0,
          waitingDependencies: 0,
          ready: true,
          reasons: [],
        })),
      ),
      eligibilityProjection: {
        queued: 2,
        processing: 1,
        retrying: 0,
        dead: 0,
        proofPending: 0,
        oldestPendingAt: new Date(Date.now() - 45_000).toISOString(),
      },
    };
  },

  async getSummary(admin = false): Promise<DashboardSummary> {
    await delay(100);
    const visibleRequests = admin
      ? requests
      : requests.filter((request) => request.userName === "陈远");
    return {
      totalAvailableMinor: orders.reduce(
        (total, order) => total + order.availableMinor,
        0,
      ),
      reviewingMinor: visibleRequests
        .filter(
          (request) =>
            request.status === "submitted" || request.status === "reviewing",
        )
        .reduce((total, request) => total + request.amountMinor, 0),
      issuedThisYearMinor: visibleRequests
        .filter((request) => request.status === "issued")
        .reduce((total, request) => total + request.amountMinor, 0),
      pendingCount: visibleRequests.filter(
        (request) =>
          request.status === "submitted" || request.status === "reviewing",
      ).length,
    };
  },

  async submitInvoice(payload: SubmitInvoicePayload) {
    await delay(520);
    const profile = profiles.find((item) => item.id === payload.profileId);
    if (!profile) throw new Error("开票资料不存在");
    const amountMinor = payload.allocations.reduce(
      (sum, item) => sum + item.allocatedMinor,
      0,
    );
    if (amountMinor <= 0) throw new Error("请选择可开票订单");
    if (amountMinor < systemSettings.minimumRequestMinor)
      throw new Error(
        `单次开票金额最低为 ¥${(systemSettings.minimumRequestMinor / 100).toFixed(2)}`,
      );
    for (const allocation of payload.allocations) {
      const order = orders.find((item) => item.id === allocation.orderId);
      if (!order || order.availableMinor < allocation.allocatedMinor) {
        throw new Error("订单可开票金额已发生变化，请刷新后重试");
      }
    }
    orders = orders.map((order) => {
      const allocation = payload.allocations.find(
        (item) => item.orderId === order.id,
      );
      return allocation
        ? {
            ...order,
            reservedMinor: order.reservedMinor + allocation.allocatedMinor,
            availableMinor: order.availableMinor - allocation.allocatedMinor,
          }
        : order;
    });
    const request: InvoiceRequest = {
      id: `req_${Date.now()}`,
      requestNo: `INV-${new Date().toISOString().slice(0, 10).replaceAll("-", "")}-${String(requests.length + 9).padStart(3, "0")}`,
      userName: "陈远",
      userEmail: "chenyuan@example.com",
      source: payload.source,
      sourceInstanceId: payload.sourceInstanceId ?? `${payload.source}-main`,
      version: 1,
      amountMinor,
      profileSnapshot: structuredClone(profile),
      allocations: payload.allocations,
      status: "submitted",
      submittedAt: now,
      updatedAt: now,
      mailStatus: "not_sent",
      newApiVerification:
        payload.source === "newapi" ? "pending" : "not_required",
    };
    requests = [request, ...requests];
    return structuredClone(request);
  },

  async cancelInvoice(target: InvoiceRequest) {
    await delay(280);
    const current = requests.find((request) => request.id === target.id);
    if (!current) throw new Error("开票申请不存在");
    if (
      current.workflowStatus !== "pending_review" &&
      current.workflowStatus !== "needs_changes" &&
      current.status !== "submitted"
    ) {
      throw new Error("当前申请状态不能取消");
    }
    orders = orders.map((order) => {
      const allocation = current.allocations.find(
        (item) => item.orderId === order.id,
      );
      if (!allocation) return order;
      return {
        ...order,
        reservedMinor: Math.max(
          0,
          order.reservedMinor - allocation.allocatedMinor,
        ),
        availableMinor: order.availableMinor + allocation.allocatedMinor,
      };
    });
    const cancelled: InvoiceRequest = {
      ...current,
      status: "returned",
      workflowStatus: "user_cancelled",
      reviewNote: "用户已取消；对应金额已经释放，可重新选择后申请。",
      version: (current.version ?? 1) + 1,
      updatedAt: new Date().toISOString(),
    };
    requests = requests.map((request) =>
      request.id === current.id ? cancelled : request,
    );
    return structuredClone(cancelled);
  },

  async getPaymentCandidates(cursor, sourceInstanceId) {
    await delay(180);
    if (cursor) return { items: [] };
    const scoped = sourceInstanceId
      ? paymentCandidates.filter((candidate) => candidate.sourceInstanceId === sourceInstanceId)
      : paymentCandidates;
    return { items: structuredClone(scoped) };
  },

  async verifyPayment(candidateId, input) {
    await delay(300);
    if (input.paidMinor <= 0 || input.currency !== "CNY")
      throw new Error("请输入实际到账金额和 CNY 币种");
    if (!input.evidenceReference.trim()) throw new Error("支付证据编号不能为空");
	const candidate = paymentCandidates.find((item) => item.id === candidateId);
	if (!candidate) throw new Error("支付候选不存在");
	if (input.paidMinor > candidate.quotedMinor)
		throw new Error("核验金额不能超过同步候选上限");
    paymentCandidates = paymentCandidates.filter(
      (candidate) => candidate.id !== candidateId,
    );
    orders = orders.map((order) =>
      order.id === candidateId
        ? {
            ...order,
            paidMinor: input.paidMinor,
            consumedCashMinor: 0,
            reservedMinor: 0,
            issuedMinor: 0,
            availableMinor: 0,
            eligibilityKind: "wallet",
            eligibilityStatus: "active",
            refundFrozen: false,
            verification: "verified",
          }
        : order,
    );
	return "verified";
  },

  async rejectPayment(candidateId, input) {
    await delay(300);
    if (!input.reason.trim() || !input.evidenceReference.trim())
      throw new Error("拒绝原因和支付证据编号均不能为空");
    paymentCandidates = paymentCandidates.filter(
      (candidate) => candidate.id !== candidateId,
    );
    orders = orders.map((order) =>
      order.id === candidateId
        ? { ...order, verification: "attention" }
        : order,
    );
  },

	async applyManualPaymentCap(candidateId, input) {
		await delay(300);
		if (input.newCapMinor < 0 || !input.evidenceReference.trim() || !input.reason.trim())
			throw new Error("退款后剩余金额、证据和原因均为必填");
		const candidate = paymentCandidates.find((item) => item.id === candidateId);
		if (!candidate || candidate.verification !== "verified" || input.newCapMinor >= candidate.currentCapMinor)
			throw new Error("只能降低已核验候选的可开票金额");
		paymentCandidates = paymentCandidates.map((item) =>
			item.id === candidateId
				? {...item, currentCapMinor: input.newCapMinor, quotedMinor: input.newCapMinor, verification: "frozen", reviewStage: "unreviewed"}
				: item,
		);
	},

  async getRefundCases(
    status: RefundCaseStatus,
    cursor?: string,
    // The mock RefundCase fixture carries no source/sourceInstanceId field
    // (mirroring the real DTO, which likewise omits it -- refund_cases has
    // no source column of its own; the backend filter reaches the source
    // instance through the case's funding lot instead). Accepted for
    // interface conformance; the demo dataset has nothing to scope by.
    _sourceInstanceId?: string,
  ) {
    await delay(180);
    if (cursor) return { items: [] };
    return {
      items: structuredClone(
        refundCases.filter((refundCase) => refundCase.status === status),
      ),
    };
  },

  async resolveRefundCase(caseId, input) {
    await delay(350);
    if (!input.evidenceReference.trim() || !input.note.trim())
      throw new Error("处理退款案例必须填写证据和说明");
    let resolved: RefundCase | undefined;
    refundCases = refundCases.map((refundCase) => {
      if (refundCase.id !== caseId) return refundCase;
      if (refundCase.status !== "open") throw new Error("退款案例已处理");
      resolved = {
        ...refundCase,
        status: input.resolutionStatus,
        resolvedBy: "demo-admin",
        resolvedAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      };
      return resolved;
    });
    if (!resolved) throw new Error("退款案例不存在");
    return structuredClone(resolved);
  },

  async getEligibilityFreezes(
    filters: EligibilityFreezeFilters,
    cursor?: string,
  ) {
    await delay(160);
    const visible = eligibilityFreezes
      .filter(
        (freeze) =>
          (filters.status === "all" || freeze.status === filters.status) &&
          (!filters.reason || freeze.reason === filters.reason) &&
          (!filters.sourceInstanceId ||
            freeze.sourceInstanceId === filters.sourceInstanceId) &&
          (!filters.externalUserId ||
            freeze.externalUserId === filters.externalUserId),
      )
      .sort(
        (left, right) =>
          right.openedAt.localeCompare(left.openedAt) ||
          right.id.localeCompare(left.id),
      );
    let start = 0;
    if (cursor) {
      try {
        const decoded = JSON.parse(cursor) as {
          beforeOpenedAt?: string;
          beforeId?: string;
        };
        start =
          visible.findIndex(
            (freeze) =>
              freeze.openedAt === decoded.beforeOpenedAt &&
              freeze.id === decoded.beforeId,
          ) + 1;
      } catch {
        throw new InvoiceApiError("资格冻结分页游标无效。", {
          code: "INVALID_CURSOR",
        });
      }
      if (start <= 0)
        throw new InvoiceApiError("资格冻结分页游标已失效。", {
          code: "INVALID_CURSOR",
        });
    }
    const pageSize = 2;
    const items = visible.slice(start, start + pageSize);
    const last = items.at(-1);
    return {
      items: structuredClone(items),
      nextCursor:
        last && start + pageSize < visible.length
          ? JSON.stringify({
              beforeOpenedAt: last.openedAt,
              beforeId: last.id,
            })
          : undefined,
    };
  },

  async resolveEligibilityFreeze(freezeId, input) {
    await delay(360);
    const evidence = input.evidenceReference.trim();
    const note = input.note.trim();
    if (!evidence || !note)
      throw new InvoiceApiError("资格解冻必须填写证据引用和处理说明。", {
        code: "ELIGIBILITY_RESOLUTION_EVIDENCE_REQUIRED",
        status: 422,
      });
    let resolved: EligibilityFreeze | undefined;
    eligibilityFreezes = eligibilityFreezes.map((freeze) => {
      if (freeze.id !== freezeId) return freeze;
      if (freeze.reason === "SOURCE_REFUND")
        throw new InvoiceApiError("退款冻结必须先在退款与红冲队列结案。", {
          code: "ELIGIBILITY_REFUND_EXPOSED",
          status: 409,
        });
      if (freeze.status !== "open" || freeze.version !== input.version)
        throw new InvoiceApiError("冻结记录已经变化，请刷新后重试。", {
          code: "CONFLICT",
          status: 409,
        });
      resolved = {
        ...freeze,
        status: "resolved",
        eligibilityStatus: "active",
        resolvedAt: new Date().toISOString(),
        version: freeze.version + 1,
      };
      return resolved;
    });
    if (!resolved)
      throw new InvoiceApiError("资格冻结记录不存在。", {
        code: "NOT_FOUND",
        status: 404,
      });
    return structuredClone(resolved);
  },

  async getAccountLedger(filters: AccountLedgerFilters, cursor?: string) {
    await delay(160);
    // sourceInstanceId is not part of the list contract's own fields (see
    // AccountLedgerListItem), matching the real endpoint -- the mock has
    // nothing to filter that by either, so only externalUserId is applied
    // here, same as every other mock filter in this file that mirrors a
    // real query-param the response itself doesn't echo back.
    const visible = accountLedgerItems
      .filter(
        (item) =>
          !filters.externalUserId ||
          item.externalUserId === filters.externalUserId,
      )
      .sort((left, right) => {
        if (filters.sort === "block_state") {
          // Mirrors postgresstore.accountBlockStateRank: settling shares
          // rank 1 with pending reconciliation -- both are "not invoiceable
          // yet" and sort as one group.
          const rank = {
            frozen_manual_review: 0,
            not_invoiceable_pending_reconciliation: 1,
            settling: 1,
            below_threshold: 2,
            invoiceable: 3,
          } as const;
          return (
            rank[left.blockState] - rank[right.blockState] ||
            right.externalAccountId.localeCompare(left.externalAccountId)
          );
        }
        return (
          right.invoiceableNowMinor - left.invoiceableNowMinor ||
          right.externalAccountId.localeCompare(left.externalAccountId)
        );
      });
    let start = 0;
    if (cursor) {
      try {
        const decoded = JSON.parse(cursor) as { beforeId?: string };
        start =
          visible.findIndex((item) => item.externalAccountId === decoded.beforeId) +
          1;
      } catch {
        throw new InvoiceApiError("用户账本分页游标无效。", {
          code: "INVALID_CURSOR",
        });
      }
      if (start <= 0)
        throw new InvoiceApiError("用户账本分页游标已失效。", {
          code: "INVALID_CURSOR",
        });
    }
    const pageSize = 2;
    const items = visible.slice(start, start + pageSize);
    const last = items.at(-1);
    return {
      items: structuredClone(items),
      nextCursor:
        last && start + pageSize < visible.length
          ? JSON.stringify({
              beforeInvoiceableMinor: last.invoiceableNowMinor,
              beforeId: last.externalAccountId,
            })
          : undefined,
    };
  },

  async getAccountLedgerDetail(externalAccountId: string) {
    await delay(160);
    const detail = accountLedgerDetails[externalAccountId];
    if (!detail)
      throw new InvoiceApiError("未找到该记录，可能已被删除或地址有误。", {
        code: "NOT_FOUND",
        status: 404,
      });
    return structuredClone(detail);
  },

  async adminReview(payload: AdminReviewPayload) {
    await delay(450);
    requests = requests.map((request) =>
      request.id === payload.requestId
        ? {
            ...request,
            status: payload.action === "approve" ? "reviewing" : "returned",
            workflowStatus:
              payload.action === "approve" ? "approved" : "needs_changes",
            version: (request.version ?? 1) + 1,
            reviewer: "财务管理员",
            reviewNote: payload.note,
            updatedAt: new Date().toISOString(),
          }
        : request,
    );
  },

  async adminUploadInvoice(
    target: InvoiceRequest,
    file: File,
    invoiceNumber: string,
    _issuedAt: string,
  ) {
    await delay(650);
    requests = requests.map((request) =>
      request.id === target.id
        ? {
            ...request,
            status: "issued",
            workflowStatus: "issued",
            pdfName: file.name,
            invoiceNumber,
            mailStatus: "queued",
            updatedAt: new Date().toISOString(),
          }
        : request,
    );
  },

  async resendMail(target: InvoiceRequest) {
    await delay(500);
    requests = requests.map((request) =>
      request.id === target.id ? { ...request, mailStatus: "queued" } : request,
    );
  },

  async getDeliveryState(target: InvoiceRequest) {
    await delay(80);
    const current = requests.find((request) => request.id === target.id) ?? target;
    return {
      documentAvailable: current.status === "issued",
      invoiceNumber: current.invoiceNumber,
      mailStatus: current.mailStatus,
      mailAttempts: current.mailStatus === "failed" ? 1 : 0,
    };
  },

  async listRequestNotices(target: InvoiceRequest) {
    await delay(60);
    const current = requests.find((request) => request.id === target.id) ?? target;
    // 演示数据里通知与申请同时产生（真实系统里也是同事务入队），所以
    // 只要有 submittedAt 就有一条；还没提交的申请没有通知。
    if (!current.submittedAt) {
      return [];
    }
    return [
      {
        id: `notice-${current.id}`,
        kind: "request.submitted",
        status: "sent",
        attemptCount: 1,
        deliveredAt: current.submittedAt,
        lastErrorCode: "",
        createdAt: current.submittedAt,
      },
    ];
  },

  async downloadInvoiceDocument(request: InvoiceRequest) {
    const body = `%PDF-1.4\n% SoloV invoice preview ${request.invoiceNumber ?? request.requestNo}\n%%EOF`;
    const url = URL.createObjectURL(
      new Blob([body], { type: "application/pdf" }),
    );
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = request.pdfName ?? `${request.requestNo}.pdf`;
    anchor.click();
    URL.revokeObjectURL(url);
  },

  async downloadAdminInvoiceDocument(request: InvoiceRequest) {
    return this.downloadInvoiceDocument(request);
  },

  async getAdminSettings() {
    await delay(160);
    return structuredClone(systemSettings);
  },

  async saveInvoiceRules(input) {
    await delay();
    if (input.revision !== systemSettings.revision) {
      throw new InvoiceApiError("设置已被其他管理员更新，请刷新后重试。", {
        code: "VERSION_CONFLICT",
        status: 409,
      });
    }
    if (input.minimumRequestMinor < 20_000) {
      throw new Error("最低开票金额不能低于 ¥200.00");
    }
    if (!input.issuerName.trim()) throw new Error("开票主体名称不能为空");
    systemSettings.issuerName = input.issuerName.trim();
    systemSettings.minimumRequestMinor = input.minimumRequestMinor;
    systemSettings.revision += 1;
  },

  async saveSMTPSettings(input) {
    await delay(380);
    if (
      input.testRecipient !== undefined &&
      input.testRecipient.trim() !== "" &&
      input.testRecipient.trim().toLowerCase() ===
        input.fromAddress.trim().toLowerCase()
    ) {
      throw new InvoiceApiError("测试收件人不能与发件人相同。", {
        code: "SMTP_TEST_RECIPIENT_CONFLICT",
        status: 422,
      });
    }
    if (input.revision !== systemSettings.revision) {
      throw new InvoiceApiError("设置已被其他管理员更新，请刷新后重试。", {
        code: "VERSION_CONFLICT",
        status: 409,
      });
    }
    if (
      !["smtp.qq.com", "smtp.exmail.qq.com", "smtp.gmail.com"].includes(
        input.host.trim().toLowerCase(),
      ) ||
      input.port !== 587 ||
      !input.startTLS
    ) {
      throw new Error("当前仅支持 QQ SMTP 587 + STARTTLS");
    }
    systemSettings.smtp = {
      fromAddress: input.fromAddress,
      fromName: input.fromName,
      host: input.host,
      port: input.port,
      startTLS: input.startTLS,
      credentialConfigured:
        Boolean(input.authorizationCode) ||
        systemSettings.smtp.credentialConfigured,
      testRecipientMasked:
        input.testRecipient === undefined
          ? systemSettings.smtp.testRecipientMasked
          : maskTestRecipient(input.testRecipient),
      testRecipientManaged:
        input.testRecipient === undefined
          ? systemSettings.smtp.testRecipientManaged
          : input.testRecipient.trim() !== "",
    };
    systemSettings.revision += 1;
  },

  async sendSMTPTest() {
    await delay(700);
    if (!systemSettings.smtp.credentialConfigured) {
      throw new Error("请先录入 QQ 邮箱授权码并保存配置");
    }
  },

  async saveNoticeWebhook(webhookURL: string) {
    await delay(120);
    // 演示态也不存地址：只留一个"配过了"的痕迹，与真实实现同一条纪律。
    systemSettings.noticeWebhook = {
      configured: true,
      // 演示指纹取地址长度做种，形状与真实一致但不可反推（本来也是假的）。
      fingerprint: `sha256:${webhookURL.length.toString(16).padStart(16, "0")}`,
      updatedBy: "demo-admin",
      updatedAt: new Date().toISOString(),
    };
  },

  async clearNoticeWebhook() {
    await delay(80);
    systemSettings.noticeWebhook = {
      configured: false,
      fingerprint: "",
      updatedBy: "",
      updatedAt: null,
    };
  },

  async sendNoticeWebhookTest() {
    await delay(150);
    if (!systemSettings.noticeWebhook.configured) {
      throw new Error("请先保存企业微信通知地址");
    }
  },

  async saveAdminAccess(input) {
    await delay();
    if (input.revision !== systemSettings.revision) {
      throw new InvoiceApiError("设置已被其他管理员更新，请刷新后重试。", {
        code: "VERSION_CONFLICT",
        status: 409,
      });
    }
    if (!input.cidrs.length) throw new Error("至少保留一条管理员 IP/CIDR");
    systemSettings.adminAccess.cidrs = [...input.cidrs];
    systemSettings.revision += 1;
  },
};

// Backward-compatible export for isolated imports; application code imports
// the mode-switching client from ./api.
export const invoiceApi = mockInvoiceApi;
