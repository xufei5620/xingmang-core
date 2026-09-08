import type {
  FundingOrder,
  InvoiceProfile,
  InvoiceRequestPage,
  SourceAccount,
  UserEligibilitySummary,
} from "../types";

// XM-INV-LOT-REASON-CONTRACT.
//
// The user invoice centre loads five independent things. It used to load them
// with Promise.all, which meant any one rejection discarded all five results:
// the setters after the await never ran, every panel kept whatever it had
// before -- for a first load, its empty initial value -- and the page rendered
// as if the account had nothing in it.
//
// That is how one unrecognised enum value in ONE lot became a whole-page
// outage. The worst of it was not the missing data: with sourceAccounts stuck
// at [], the "已关联的平台账号" panel renders its empty state, which is the
// three-step binding wizard. Users whose bindings were verified and working
// were shown instructions to go and bind again. Data that is merely missing is
// recoverable; data that is missing while the UI confidently tells you to take
// a destructive action is not.
//
// The decision of what survives a partial failure is extracted here, away from
// React, so it can be tested directly. A panel is only ever updated by its own
// request. A request that fails contributes its name to the error line and
// changes nothing else.

export type UserDataRequestKey =
  | "orders"
  | "profiles"
  | "sourceAccounts"
  | "eligibilitySummaries"
  | "requests";

// The Chinese name of each panel, used in the partial-failure banner so the
// user can tell which part of the page is stale rather than being told, with no
// detail, that everything failed.
export const userDataRequestLabels: Record<UserDataRequestKey, string> = {
  orders: "充值记录",
  profiles: "开票抬头",
  sourceAccounts: "已关联账号",
  eligibilitySummaries: "开票资格摘要",
  requests: "开票申请记录",
};

export interface UserDataResults {
  orders: PromiseSettledResult<FundingOrder[]>;
  profiles: PromiseSettledResult<InvoiceProfile[]>;
  sourceAccounts: PromiseSettledResult<SourceAccount[]>;
  eligibilitySummaries: PromiseSettledResult<UserEligibilitySummary[]>;
  requests: PromiseSettledResult<InvoiceRequestPage>;
}

export interface UserDataLoadPlan {
  orders?: FundingOrder[];
  profiles?: InvoiceProfile[];
  sourceAccounts?: SourceAccount[];
  eligibilitySummaries?: UserEligibilitySummary[];
  requests?: InvoiceRequestPage;
  // Only set when BOTH the requests and the summaries loaded. A partial total
  // would render as a confident "¥0.00", which a user cannot distinguish from
  // a genuinely empty balance -- so the card keeps its previous value instead.
  summaryAvailableMinor?: number;
  failed: UserDataRequestKey[];
  loadError: string | null;
}

/**
 * The state updates one refresh performs. Kept as an interface, and applied by
 * applyUserDataPlan below, so that "a failed request must not clear a panel it
 * does not own" is enforced in tested code rather than in six `if` statements
 * inside a React component that a test would have to re-implement to check.
 */
export interface UserDataSetters {
  setOrders(value: FundingOrder[]): void;
  setProfiles(value: InvoiceProfile[]): void;
  setSourceAccounts(value: SourceAccount[]): void;
  setEligibilitySummaries(value: UserEligibilitySummary[]): void;
  setRequests(page: InvoiceRequestPage): void;
  setSummary(page: InvoiceRequestPage, availableMinor: number): void;
  setLoadError(value: string | null): void;
}

function value<T>(result: PromiseSettledResult<T>): T | undefined {
  return result.status === "fulfilled" ? result.value : undefined;
}

/**
 * Decides what to apply from one refresh. A key is present in the plan only if
 * its own request succeeded; `undefined` means "leave this panel alone", never
 * "clear this panel".
 */
export function planUserDataLoad(results: UserDataResults): UserDataLoadPlan {
  const failed: UserDataRequestKey[] = [];
  for (const key of [
    "orders",
    "profiles",
    "sourceAccounts",
    "eligibilitySummaries",
    "requests",
  ] as const) {
    if (results[key].status === "rejected") failed.push(key);
  }

  const requests = value(results.requests);
  const summaries = value(results.eligibilitySummaries);

  return {
    orders: value(results.orders),
    profiles: value(results.profiles),
    sourceAccounts: value(results.sourceAccounts),
    eligibilitySummaries: summaries,
    requests,
    summaryAvailableMinor:
      requests !== undefined && summaries !== undefined
        ? summaries.reduce((total, item) => total + item.availableMinor, 0)
        : undefined,
    failed,
    loadError: failed.length
      ? `${failed.map((key) => userDataRequestLabels[key]).join("、")}暂时无法读取，其余数据仍是最新的。`
      : null,
  };
}

/**
 * Applies a plan. Each setter runs only when its own request succeeded, so no
 * panel is ever cleared on another panel's behalf.
 */
export function applyUserDataPlan(
  plan: UserDataLoadPlan,
  setters: UserDataSetters,
) {
  if (plan.orders) setters.setOrders(plan.orders);
  if (plan.profiles) setters.setProfiles(plan.profiles);
  if (plan.sourceAccounts) setters.setSourceAccounts(plan.sourceAccounts);
  if (plan.eligibilitySummaries)
    setters.setEligibilitySummaries(plan.eligibilitySummaries);
  if (plan.requests) setters.setRequests(plan.requests);
  if (plan.requests && plan.summaryAvailableMinor !== undefined) {
    setters.setSummary(plan.requests, plan.summaryAvailableMinor);
  }
  setters.setLoadError(plan.loadError);
}

/** Convenience wrapper: plan and apply in one call. */
export function applyUserDataResults(
  results: UserDataResults,
  setters: UserDataSetters,
) {
  const plan = planUserDataLoad(results);
  applyUserDataPlan(plan, setters);
  return plan;
}
