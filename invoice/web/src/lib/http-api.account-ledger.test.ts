import { afterEach, describe, expect, it, vi } from "vitest";

import { InvoiceApiError } from "./api-contract";
import { httpInvoiceApi } from "./http-api";

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW): the operator "用户账本" view's HTTP
// layer. Follows the same mocked-fetch style as
// http-api.eligibility-external-user-id.test.ts (this codebase's own
// established precedent for testing a new admin list/detail endpoint's wire
// contract) rather than rendering App.tsx's AccountLedgerPage/
// AccountLedgerDetailDrawer components directly: this project has no
// jsdom/happy-dom test environment or @testing-library/react installed (see
// package-lock.json / vite.config.ts -- vitest here runs in vitest's default
// Node environment), so a fetch-mocked test of the data every rendered row,
// badge and drawer field is built from is this repo's actual available
// substitute for a component-rendering test, exercising the identical logic
// paths (block_state -> badge tone/label lookup keys, mapped detail fields,
// an empty items[] array, and friendlyError's status/code branches) a
// rendering test would.

const VALID_LIST_ITEM = {
  external_account_id: "41000000-0000-4000-8000-000000000001",
  source_type: "sub2api" as const,
  external_user_id: "1147",
  policy_start_at: "2026-08-31T16:00:00.000Z",
  recharges_since_start_count: 3,
  recharges_since_start_minor: 128_000,
  consumed_since_start_minor: 96_000,
  invoiceable_now_minor: 31_800,
  issued_minor: 0,
  threshold_reached: true,
  block_state: "invoiceable",
  last_checkpoint_at: "2026-09-03T06:25:11.000Z",
};

const VALID_DETAIL = {
  ...VALID_LIST_ITEM,
  block_state: "not_invoiceable_pending_reconciliation",
  threshold_reached: true,
  opening_balance_units: { service_units: "48200", unit_code: "SUB2_BALANCE_1E8" },
  recharges_since_start: [
    {
      funding_lot_id: "51000000-0000-4000-8000-000000000001",
      completed_at: "2026-09-02T03:11:00.000Z",
      amount_minor: 50_000,
      eligibility_kind: "WALLET_CASH",
      refund_frozen: false,
    },
  ],
  consumption_timeline: [{ date: "2026-09-02", consumed_minor: 96_000 }],
  last_reconciled_at: "2026-09-02T18:04:02.000Z",
  block_reason:
    "2026-09-03 06:25（Asia/Shanghai）balance_checkpoint ckpt-xxx 上报余额差额 -4（单位 SUB2_BALANCE_1E8，非人民币元），预期 380，上报余额与账本预期存在负向差额，等待下一次核对。",
};

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function stubFetchJSON(status: number, body: unknown) {
  stubWindowTimers();
  const calls: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    calls.push(typeof input === "string" ? input : input.toString());
    return new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

function listPageBody(items: unknown[], overrides: Record<string, unknown> = {}) {
  return {
    items,
    has_more: false,
    next_before_invoiceable_minor: null,
    next_before_id: "",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("getAccountLedger: list mapping", () => {
  it("maps a valid item's every field, including the block_state a badge renders from", async () => {
    stubFetchJSON(200, listPageBody([VALID_LIST_ITEM]));
    const page = await httpInvoiceApi.getAccountLedger({});
    expect(page.items).toHaveLength(1);
    const item = page.items[0];
    expect(item.externalAccountId).toBe(VALID_LIST_ITEM.external_account_id);
    expect(item.source).toBe("sub2api");
    expect(item.externalUserId).toBe("1147");
    expect(item.rechargesSinceStartCount).toBe(3);
    expect(item.rechargesSinceStartMinor).toBe(128_000);
    expect(item.consumedSinceStartMinor).toBe(96_000);
    expect(item.invoiceableNowMinor).toBe(31_800);
    expect(item.issuedMinor).toBe(0);
    expect(item.thresholdReached).toBe(true);
    expect(item.blockState).toBe("invoiceable");
    expect(item.lastCheckpointAt).toBe("2026-09-03T06:25:11.000Z");
  });

  it.each([
    "frozen_manual_review",
    "not_invoiceable_pending_reconciliation",
    "below_threshold",
    "invoiceable",
  ] as const)("accepts block_state=%s (one of the four states a badge maps)", async (state) => {
    stubFetchJSON(200, listPageBody([{ ...VALID_LIST_ITEM, block_state: state }]));
    const page = await httpInvoiceApi.getAccountLedger({});
    expect(page.items[0].blockState).toBe(state);
  });

  it("maps a null last_checkpoint_at to undefined (an account never evaluated)", async () => {
    stubFetchJSON(
      200,
      listPageBody([{ ...VALID_LIST_ITEM, last_checkpoint_at: null }]),
    );
    const page = await httpInvoiceApi.getAccountLedger({});
    expect(page.items[0].lastCheckpointAt).toBeUndefined();
  });

  it("renders an empty page (no accounts match) without throwing", async () => {
    stubFetchJSON(200, listPageBody([]));
    const page = await httpInvoiceApi.getAccountLedger({});
    expect(page.items).toEqual([]);
    expect(page.nextCursor).toBeUndefined();
  });

  it("rejects an unrecognized block_state instead of silently displaying it", async () => {
    stubFetchJSON(
      200,
      listPageBody([{ ...VALID_LIST_ITEM, block_state: "some_new_state" }]),
    );
    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toThrow();
  });

  it("rejects a negative amount field", async () => {
    stubFetchJSON(
      200,
      listPageBody([{ ...VALID_LIST_ITEM, invoiceable_now_minor: -1 }]),
    );
    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toThrow();
  });

  it("rejects a response item carrying an unexpected extra field", async () => {
    stubFetchJSON(
      200,
      listPageBody([{ ...VALID_LIST_ITEM, unexpected_field: "x" }]),
    );
    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toThrow();
  });

  it("forwards external_user_id, sort and source_instance_id onto the request URL", async () => {
    const calls = stubFetchJSON(200, listPageBody([]));
    await httpInvoiceApi.getAccountLedger({
      externalUserId: "1147",
      sort: "block_state",
      sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    });
    expect(calls[0]).toContain("external_user_id=1147");
    expect(calls[0]).toContain("sort=block_state");
    expect(calls[0]).toContain(
      "source_instance_id=10000000-0000-4000-8000-000000000001",
    );
  });

  it("omits every filter when none is provided", async () => {
    const calls = stubFetchJSON(200, listPageBody([]));
    await httpInvoiceApi.getAccountLedger({});
    expect(calls[0]).not.toContain("external_user_id");
    expect(calls[0]).not.toContain("sort=");
    expect(calls[0]).not.toContain("source_instance_id");
  });

  it("rejects a malformed filter without ever calling fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      httpInvoiceApi.getAccountLedger({ externalUserId: "bad\r\nvalue" }),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("round-trips a has_more cursor onto the next request's query params", async () => {
    const calls = stubFetchJSON(
      200,
      listPageBody([VALID_LIST_ITEM], {
        has_more: true,
        next_before_invoiceable_minor: 31_800,
        next_before_id: "41000000-0000-4000-8000-000000000002",
      }),
    );
    const page = await httpInvoiceApi.getAccountLedger({});
    expect(page.nextCursor).toBeDefined();
    await httpInvoiceApi.getAccountLedger({}, page.nextCursor);
    expect(calls[1]).toContain("before_invoiceable_minor=31800");
    expect(calls[1]).toContain(
      "before_id=41000000-0000-4000-8000-000000000002",
    );
  });

  it("rejects a malformed cursor without ever calling fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      httpInvoiceApi.getAccountLedger({}, "not-json"),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("getAccountLedgerDetail: detail mapping", () => {
  it("maps every detail field the drawer renders", async () => {
    stubFetchJSON(200, VALID_DETAIL);
    const detail = await httpInvoiceApi.getAccountLedgerDetail(
      VALID_DETAIL.external_account_id,
    );
    expect(detail.externalAccountId).toBe(VALID_DETAIL.external_account_id);
    expect(detail.blockState).toBe("not_invoiceable_pending_reconciliation");
    expect(detail.blockReason).toBe(VALID_DETAIL.block_reason);
    expect(detail.openingBalance).toEqual({
      serviceUnits: "48200",
      unitCode: "SUB2_BALANCE_1E8",
    });
    expect(detail.recharges).toHaveLength(1);
    expect(detail.recharges[0]).toEqual({
      fundingLotId: "51000000-0000-4000-8000-000000000001",
      completedAt: "2026-09-02T03:11:00.000Z",
      amountMinor: 50_000,
      eligibilityKind: "WALLET_CASH",
      refundFrozen: false,
    });
    expect(detail.consumptionTimeline).toEqual([
      { date: "2026-09-02", consumedMinor: 96_000 },
    ]);
    expect(detail.lastReconciledAt).toBe("2026-09-02T18:04:02.000Z");
  });

  it("maps a null block_reason/last_reconciled_at to undefined for an invoiceable account", async () => {
    stubFetchJSON(200, {
      ...VALID_DETAIL,
      block_state: "invoiceable",
      block_reason: null,
      last_reconciled_at: null,
    });
    const detail = await httpInvoiceApi.getAccountLedgerDetail(
      VALID_DETAIL.external_account_id,
    );
    expect(detail.blockReason).toBeUndefined();
    expect(detail.lastReconciledAt).toBeUndefined();
  });

  it("rejects a blocked account with a null block_reason (state/reason inconsistency)", async () => {
    stubFetchJSON(200, { ...VALID_DETAIL, block_reason: null });
    await expect(
      httpInvoiceApi.getAccountLedgerDetail(VALID_DETAIL.external_account_id),
    ).rejects.toThrow();
  });

  it("rejects an invoiceable account with a non-null block_reason (state/reason inconsistency)", async () => {
    stubFetchJSON(200, {
      ...VALID_DETAIL,
      block_state: "invoiceable",
      block_reason: "不该出现的原因",
    });
    await expect(
      httpInvoiceApi.getAccountLedgerDetail(VALID_DETAIL.external_account_id),
    ).rejects.toThrow();
  });

  it("rejects an opening balance unit_code that does not match the account's own source type", async () => {
    stubFetchJSON(200, {
      ...VALID_DETAIL,
      opening_balance_units: { service_units: "0", unit_code: "NEWAPI_QUOTA" },
    });
    await expect(
      httpInvoiceApi.getAccountLedgerDetail(VALID_DETAIL.external_account_id),
    ).rejects.toThrow();
  });

  it("rejects a malformed account id without ever calling fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      httpInvoiceApi.getAccountLedgerDetail("not-a-uuid"),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("surfaces a 404 as the shared NOT_FOUND friendly message (empty state: account no longer exists)", async () => {
    stubFetchJSON(404, { error: { code: "NOT_FOUND", message: "not found" } });
    let caught: unknown;
    try {
      await httpInvoiceApi.getAccountLedgerDetail(
        "00000000-0000-4000-8000-000000000000",
      );
    } catch (error) {
      caught = error;
    }
    expect(caught).toBeInstanceOf(InvoiceApiError);
    expect((caught as InvoiceApiError).message).toBe(
      "未找到该记录，可能已被删除或地址有误。",
    );
    expect((caught as InvoiceApiError).code).toBe("NOT_FOUND");
  });
});

describe("account ledger error mapping (403/503)", () => {
  it("maps a 403 to the shared permission-denied sentence", async () => {
    stubFetchJSON(403, { error: { code: "FORBIDDEN", message: "raw" } });
    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toMatchObject({
      message: "当前账号没有执行此操作的权限。",
      code: "FORBIDDEN",
      status: 403,
    });
  });

  it("maps an unrecognized 503 to the shared service-unavailable sentence", async () => {
    stubFetchJSON(503, {
      error: { code: "OPERATIONS_UNAVAILABLE", message: "raw" },
    });
    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toMatchObject({
      message: "开票服务暂时不可用，请稍后重试。",
      code: "OPERATIONS_UNAVAILABLE",
      status: 503,
    });
  });
});
