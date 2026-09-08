import { describe, expect, it } from "vitest";

import { InvoiceApiError } from "./api-contract";
import {
  applyUserDataResults,
  planUserDataLoad,
  type UserDataResults,
  type UserDataSetters,
} from "./user-data-load";
import type {
  FundingOrder,
  InvoiceProfile,
  InvoiceRequestPage,
  SourceAccount,
  UserEligibilitySummary,
} from "../types";

// XM-INV-LOT-REASON-CONTRACT: the amplifier half of the incident.
//
// One lot carrying an eligibility_status this bundle did not recognise made
// getOrders() reject. Under Promise.all that discarded the other four results
// too, so sourceAccounts kept its initial [] and the "已关联的平台账号" panel
// rendered its empty state -- the three-step binding wizard -- at users whose
// bindings were verified and fine.
//
// Note carefully what the test below does NOT do. Asserting "after the failure,
// sourceAccounts is empty" would have passed on the OLD implementation too,
// because the initial value is also empty; it is an assertion that cannot
// distinguish "cleared" from "never set". Every case here therefore runs a
// successful load FIRST and asserts the second, failing load preserves what the
// first one established.

const ACCOUNTS: SourceAccount[] = [
  {
    id: "a1",
    source: "sub2api",
    sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    sourceLabel: "SoloV API",
    externalUserIdMasked: "11**7",
    status: "verified",
  },
  {
    id: "a2",
    source: "newapi",
    sourceInstanceId: "10000000-0000-4000-8000-000000000002",
    sourceLabel: "SoloV 模型平台",
    externalUserIdMasked: "34**1",
    status: "verified",
  },
];

const PROFILES = [{ id: "p1" }] as unknown as InvoiceProfile[];
const ORDERS = [{ id: "o1", availableMinor: 0 }] as unknown as FundingOrder[];
const SUMMARIES = [
  { sourceInstanceId: "s1", availableMinor: 12_300 },
] as unknown as UserEligibilitySummary[];
const REQUEST_PAGE = {
  items: [],
  nextCursor: undefined,
} as unknown as InvoiceRequestPage;

function ok<T>(value: T): PromiseSettledResult<T> {
  return { status: "fulfilled", value };
}

function fail<T>(message: string): PromiseSettledResult<T> {
  return {
    status: "rejected",
    reason: new InvoiceApiError(message, { code: "INVALID_ELIGIBILITY_STATUS" }),
  };
}

function allOk(): UserDataResults {
  return {
    orders: ok(ORDERS),
    profiles: ok(PROFILES),
    sourceAccounts: ok(ACCOUNTS),
    eligibilitySummaries: ok(SUMMARIES),
    requests: ok(REQUEST_PAGE),
  };
}

// A stand-in for the provider's React state, driven through the very same
// applyUserDataResults the component calls -- so a future change that made the
// component clear a panel on someone else's failure would fail here rather than
// slipping past a test that re-implemented the setters.
function makeState() {
  const state = {
    orders: [] as FundingOrder[],
    profiles: [] as InvoiceProfile[],
    sourceAccounts: [] as SourceAccount[],
    eligibilitySummaries: [] as UserEligibilitySummary[],
    requests: undefined as InvoiceRequestPage | undefined,
    summaryAvailableMinor: undefined as number | undefined,
    loadError: null as string | null,
  };
  const setters: UserDataSetters = {
    setOrders: (value) => (state.orders = value),
    setProfiles: (value) => (state.profiles = value),
    setSourceAccounts: (value) => (state.sourceAccounts = value),
    setEligibilitySummaries: (value) => (state.eligibilitySummaries = value),
    setRequests: (page) => (state.requests = page),
    setSummary: (_page, availableMinor) =>
      (state.summaryAvailableMinor = availableMinor),
    setLoadError: (value) => (state.loadError = value),
  };
  return { state, setters };
}

describe("a failing request does not clear the panels it does not own", () => {
  // The exact production scenario, and the one that actually separates this
  // implementation from the old one: the FIRST load, with no good previous
  // state to fall back on. Under Promise.all the orders rejection discarded the
  // other four results, sourceAccounts stayed at its initial [], and the panel
  // rendered the binding wizard.
  //
  // The two-phase cases below cannot catch that on their own: once phase one
  // has populated the panel, "withheld the new value" and "kept the old value"
  // look identical, because the old value is already correct. Only a first load
  // tells them apart.
  it("populates the other panels on a first load even when one request fails", () => {
    const { state, setters } = makeState();
    expect(state.sourceAccounts).toHaveLength(0);

    applyUserDataResults(
      { ...allOk(), orders: fail("充值记录包含无效的资金账本状态。") },
      setters,
    );

    expect(state.sourceAccounts).toEqual(ACCOUNTS);
    expect(state.profiles).toEqual(PROFILES);
    expect(state.eligibilitySummaries).toEqual(SUMMARIES);
    expect(state.orders).toHaveLength(0);
    expect(state.loadError).toBe("充值记录暂时无法读取，其余数据仍是最新的。");
  });

  it("populates the other panels on a first load when the summary request fails", () => {
    const { state, setters } = makeState();
    applyUserDataResults(
      {
        ...allOk(),
        eligibilitySummaries: fail("开票资格摘要包含未识别状态，已停止显示。"),
      },
      setters,
    );
    expect(state.sourceAccounts).toEqual(ACCOUNTS);
    expect(state.orders).toEqual(ORDERS);
  });

  it("keeps the connected accounts when the orders request fails", () => {
    const { state, setters } = makeState();
    applyUserDataResults(allOk(), setters);
    expect(state.sourceAccounts).toHaveLength(2);

    applyUserDataResults(
      { ...allOk(), orders: fail("充值记录包含无效的资金账本状态。") },
      setters,
    );

    // The whole point: verified bindings survive an unrelated failure, so the
    // panel keeps rendering "已关联的平台账号" rather than the binding wizard.
    expect(state.sourceAccounts).toHaveLength(2);
    expect(state.sourceAccounts).toEqual(ACCOUNTS);
    expect(state.profiles).toEqual(PROFILES);
    expect(state.loadError).toBe("充值记录暂时无法读取，其余数据仍是最新的。");
  });

  it("keeps the connected accounts when the eligibility summary request fails", () => {
    // The second, independent crash point: even with mapLot fixed, the summary
    // endpoint rejected on the same accounts.
    const { state, setters } = makeState();
    applyUserDataResults(allOk(), setters);
    applyUserDataResults(
      {
        ...allOk(),
        eligibilitySummaries: fail("开票资格摘要包含未识别状态，已停止显示。"),
      },
      setters,
    );
    expect(state.sourceAccounts).toEqual(ACCOUNTS);
    expect(state.orders).toEqual(ORDERS);
    expect(state.loadError).toBe(
      "开票资格摘要暂时无法读取，其余数据仍是最新的。",
    );
  });

  it("keeps the previously loaded accounts when the accounts request itself fails", () => {
    // The case that most directly reproduces the reported symptom. Clearing a
    // panel because its OWN request failed is just as wrong as clearing it
    // because someone else's did: an empty sourceAccounts renders the binding
    // wizard, so a transient failure here tells a correctly-bound user to go
    // and re-bind. Stale-but-true beats confidently-empty.
    const { state, setters } = makeState();
    applyUserDataResults(allOk(), setters);
    expect(state.sourceAccounts).toHaveLength(2);

    applyUserDataResults({ ...allOk(), sourceAccounts: fail("boom") }, setters);

    expect(state.sourceAccounts).toEqual(ACCOUNTS);
    expect(state.loadError).toBe("已关联账号暂时无法读取，其余数据仍是最新的。");
  });

  it("keeps the previously loaded summaries when only the summary request fails", () => {
    const { state, setters } = makeState();
    applyUserDataResults(allOk(), setters);
    expect(state.eligibilitySummaries).toEqual(SUMMARIES);
    applyUserDataResults(
      { ...allOk(), eligibilitySummaries: fail("boom") },
      setters,
    );
    expect(state.eligibilitySummaries).toEqual(SUMMARIES);
  });

  it("reports every failed panel by its Chinese name", () => {
    const { state, setters } = makeState();
    applyUserDataResults(
      {
        ...allOk(),
        orders: fail("boom"),
        sourceAccounts: fail("boom"),
      },
      setters,
    );
    expect(state.loadError).toBe(
      "充值记录、已关联账号暂时无法读取，其余数据仍是最新的。",
    );
  });

  it("clears the error banner once everything loads again", () => {
    const { state, setters } = makeState();
    applyUserDataResults({ ...allOk(), orders: fail("boom") }, setters);
    expect(state.loadError).not.toBeNull();
    applyUserDataResults(allOk(), setters);
    expect(state.loadError).toBeNull();
  });

  it("does not report a total it could not compute", () => {
    // Passing 0 here would render a confident "¥0.00" that a user cannot tell
    // apart from a real zero balance.
    const { state, setters } = makeState();
    applyUserDataResults(allOk(), setters);
    expect(state.summaryAvailableMinor).toBe(12_300);
    applyUserDataResults(
      { ...allOk(), eligibilitySummaries: fail("boom") },
      setters,
    );
    expect(state.summaryAvailableMinor).toBe(12_300);
  });

  it("still reports the total when an unrelated request fails", () => {
    const { state, setters } = makeState();
    applyUserDataResults({ ...allOk(), profiles: fail("boom") }, setters);
    expect(state.summaryAvailableMinor).toBe(12_300);
  });
});

describe("planUserDataLoad", () => {
  it("omits only the keys whose own request failed", () => {
    const plan = planUserDataLoad({
      ...allOk(),
      orders: fail("boom"),
      requests: fail("boom"),
    });
    expect(plan.orders).toBeUndefined();
    expect(plan.requests).toBeUndefined();
    expect(plan.sourceAccounts).toEqual(ACCOUNTS);
    expect(plan.profiles).toEqual(PROFILES);
    expect(plan.eligibilitySummaries).toEqual(SUMMARIES);
    expect(plan.failed).toEqual(["orders", "requests"]);
  });

  it("reports no failures and a null error when everything succeeds", () => {
    const plan = planUserDataLoad(allOk());
    expect(plan.failed).toEqual([]);
    expect(plan.loadError).toBeNull();
    expect(plan.summaryAvailableMinor).toBe(12_300);
  });
});
