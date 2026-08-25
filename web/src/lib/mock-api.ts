import type {
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
  SubmitInvoicePayload,
  UserEligibilitySummary,
} from "../types";
import { InvoiceApiError, type InvoiceApiClient } from "./api-contract";

const delay = (ms = 220) => new Promise((resolve) => setTimeout(resolve, ms));
const now = new Date().toISOString();

const mockSession: AuthSession = {
  authenticated: true,
  user: {
    id: "demo-admin",
    displayName: "演示财务管理员",
    email: "admin@example.test",
    emailVerified: true,
    role: "admin",
  },
  csrfToken: "mock-csrf-token",
  adminStepUpRequired: false,
};

const sourceAccounts: SourceAccount[] = [
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
    profileSnapshot: profiles[0],
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
    profileSnapshot: profiles[0],
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
      ...profiles[0],
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
  },
  adminAccess: {
    cidrs: ["127.0.0.1/32", "::1/128"],
    currentIP: "127.0.0.1",
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
  SourceAccount["source"],
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
  },
];

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
  loginURL(returnTo) {
    return returnTo;
  },
  adminStepUpURL(returnTo) {
    return returnTo;
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

  async getAdminRequestPage(cursor) {
    await delay();
    const start = cursor ? Number(cursor) : 0;
    const items = requests.slice(start, start + 2);
    return {
      items: structuredClone(items),
      nextCursor: start + 2 < requests.length ? String(start + 2) : undefined,
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
          waitingDependencies: 0,
          ready: true,
          reasons: [],
        })),
      ),
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

  async getPaymentCandidates(cursor) {
    await delay(180);
    if (cursor) return { items: [] };
    return { items: structuredClone(paymentCandidates) };
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

  async getRefundCases(status: RefundCaseStatus, cursor?: string) {
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
            freeze.sourceInstanceId === filters.sourceInstanceId),
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
          code: "CONFLICT",
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
      testRecipientMasked: systemSettings.smtp.testRecipientMasked,
    };
    systemSettings.revision += 1;
  },

  async sendSMTPTest() {
    await delay(700);
    if (!systemSettings.smtp.credentialConfigured) {
      throw new Error("请先录入 QQ 邮箱授权码并保存配置");
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
