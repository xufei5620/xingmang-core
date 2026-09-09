import type {
  FundingOrder,
  InvoiceProfile,
  InvoiceRequestPage,
  SourceAccount,
  UserEligibilitySummary,
} from "../types";
import type { InvoiceApiClient } from "./api-contract";

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

export const userDataRequestKeys = [
  "orders",
  "profiles",
  "sourceAccounts",
  "eligibilitySummaries",
  "requests",
] as const;

export type UserDataRequestKey = (typeof userDataRequestKeys)[number];

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
  // Which requests failed, so a panel can tell "I could not read this" apart
  // from "you have none". Without it the only signal reaching the components
  // is a banner string, and SourceAccountStatus was reading an empty list as
  // "this user has never bound an account".
  setFailed(keys: UserDataRequestKey[]): void;
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
  for (const key of userDataRequestKeys) {
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
    loadError: loadErrorFor(results, failed),
  };
}

// A rejection message is surfaced only when it is copy a person can act on.
// The backend's own 5xx line ("开票服务暂时不可用，请稍后重试。") is exactly that,
// and it WAS the banner body before the five requests were split apart --
// dropping it made the banner strictly less informative than the bug it
// replaced. But `fetch` rejects with "Failed to fetch", and the product owner
// does not read English, so the test is the shape of the sentence rather than
// where it came from: some Chinese, one line, bounded length.
const readableReasonPattern = /[一-鿿]/;

export function readableReason(reason: unknown): string | null {
  const message =
    reason instanceof Error
      ? reason.message
      : typeof reason === "string"
        ? reason
        : "";
  const trimmed = message.trim();
  if (!trimmed || trimmed.length > 200 || /[\r\n]/.test(trimmed)) return null;
  return readableReasonPattern.test(trimmed) ? trimmed : null;
}

function loadErrorFor(
  results: UserDataResults,
  failed: UserDataRequestKey[],
): string | null {
  if (failed.length === 0) return null;
  const reasons: string[] = [];
  for (const key of failed) {
    const result = results[key];
    if (result.status !== "rejected") continue;
    const reason = readableReason(result.reason);
    if (reason && !reasons.includes(reason)) reasons.push(reason);
  }
  const detail = reasons.length ? `（${reasons.join("；")}）` : "";
  // "其余数据仍是最新的" is only true when there IS a 其余. When every request
  // failed, that sentence tells the one person who reads this page that the
  // rest of it is fresh, at the moment when none of it is -- and the banner is
  // the only thing on screen saying anything at all.
  if (failed.length === userDataRequestKeys.length) {
    return `开票数据全部读取失败，请稍后重试。${detail}`;
  }
  return `${failed.map((key) => userDataRequestLabels[key]).join("、")}暂时无法读取，其余数据仍是最新的。${detail}`;
}

export type SourceAccountPanelMode = "accounts" | "unavailable" | "onboarding";

/**
 * What the "已关联的平台账号" panel must render.
 *
 * `onboarding` is the destructive one: it walks a user through binding an
 * account. Picking it purely on "the account list is empty" is what made the
 * incident worse than missing data, and splitting the five loads apart only
 * fixed half of that. It fixed a SIBLING request taking the panel down; the
 * accounts request failing on its own produces the identical empty list on a
 * first load, and would still have shown the wizard to someone whose bindings
 * are verified and working.
 *
 * So "I could not read this" and "you have none" are different answers. Only
 * the second one may invite the user to go and bind again.
 */
// Takes the whole failed-request list rather than a ready-made boolean on
// purpose. The component that calls this has no test around it (no RTL in this
// repo), so every line of judgement written there is a line nothing checks --
// `failed.includes("sourceAccounts")` belongs on this side of the boundary,
// where the mutation tests can reach it.
export function sourceAccountPanelMode(input: {
  accountCount: number;
  failed: readonly UserDataRequestKey[];
}): SourceAccountPanelMode {
  // Last-good accounts still count as accounts: the banner already says the
  // page is stale, and showing the list the user had a moment ago is never the
  // dangerous direction.
  if (input.accountCount > 0) return "accounts";
  return input.failed.includes("sourceAccounts") ? "unavailable" : "onboarding";
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
  setters.setFailed(plan.failed);
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

export const userDataLoadFailureMessage = "读取开票数据失败，请稍后重试。";

/**
 * The plan for a refresh that failed AS A WHOLE -- not one of the five
 * requests rejecting (planUserDataLoad handles that per request) but the
 * refresh itself throwing after they settled: applyUserDataResults blowing up
 * on a malformed result, a setter throwing, anything the outer `catch` sees.
 *
 * It records every request as failed. Before this, the outer catch only set
 * the banner text, and on a first load that left `sourceAccounts` at [] with
 * an empty failed list -- which sourceAccountPanelMode reads as "you have no
 * accounts", i.e. the binding wizard. The same shape as the incident, one
 * `return` away from being reachable. No data key is present, so applying it
 * clears nothing: a whole-load failure keeps every last-good panel exactly
 * like a per-request one does.
 */
export function userDataLoadFailurePlan(error: unknown): UserDataLoadPlan {
  return {
    failed: [...userDataRequestKeys],
    loadError: readableReason(error) ?? userDataLoadFailureMessage,
  };
}

export type UserDataSource = Pick<
  InvoiceApiClient,
  | "getOrders"
  | "getProfiles"
  | "getSourceAccounts"
  | "getUserEligibilitySummary"
  | "getUserRequestPage"
>;

/**
 * One full refresh of the user invoice centre: five independent reads,
 * settled independently, then applied through planUserDataLoad. Lives here
 * rather than in the React component so that the whole-load `catch` -- the
 * one that decides whether a thrown error becomes "本次读取失败" or the
 * binding wizard -- is code a test can actually reach (the repo has no DOM
 * renderer to drive the component's effect).
 *
 * `isCurrent` is the component's stale-refresh guard: if a newer refresh has
 * started while these were in flight, nothing is applied.
 */
export async function loadUserInvoiceData(
  api: UserDataSource,
  setters: UserDataSetters,
  isCurrent: () => boolean = () => true,
): Promise<UserDataLoadPlan | undefined> {
  const [orders, profiles, sourceAccounts, eligibilitySummaries, requests] =
    await Promise.allSettled([
      api.getOrders(),
      api.getProfiles(),
      api.getSourceAccounts(),
      api.getUserEligibilitySummary(),
      api.getUserRequestPage(),
    ]);
  if (!isCurrent()) return undefined;
  try {
    return applyUserDataResults(
      { orders, profiles, sourceAccounts, eligibilitySummaries, requests },
      setters,
    );
  } catch (error) {
    // Whole-load failure: every panel is marked unreadable (see
    // userDataLoadFailurePlan), never "empty".
    const plan = userDataLoadFailurePlan(error);
    applyUserDataPlan(plan, setters);
    return plan;
  }
}
