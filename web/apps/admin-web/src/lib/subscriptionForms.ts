import type {
  ProxyAssetCreateParams,
  ProxyAssetEditParams,
  RegisterSubscriptionBatchParams,
} from "../api/finance";
import { formatScaledMinorUnits } from "./money";
import { validateCredentialRef } from "./upstreamForm";

export interface SubscriptionBatchFormValues {
  paidMajor: string;
  surchargeMajor: string;
  currency: string;
  startsOn: string;
  expiresOn: string;
  accountCount: string;
  proxyAssetId: string;
}

export interface ProxyAssetFormValues {
  paidMajor: string;
  surchargeMajor: string;
  currency: string;
  openedOn: string;
  expiresOn: string;
  sharedAccountCount: string;
  buyPlatform: string;
  buyAddress: string;
  credentialRef: string;
  mounted: boolean;
}

export type AmountParseResult =
  | { ok: true; minor: string }
  | { ok: false; error: string };

export type SubscriptionBatchFormErrors = Partial<
  Record<keyof SubscriptionBatchFormValues, string>
>;
export type ProxyAssetFormErrors = Partial<Record<keyof ProxyAssetFormValues, string>>;

const SCALE_FACTOR = 1_000_000n;
const MAX_SIGNED_INT64 = 9_223_372_036_854_775_807n;
// account_count / shared_account_count 最终落 PostgreSQL integer（int4）。
const MAX_POSTGRES_INTEGER = 2_147_483_647n;
/** 对齐后端 money.maxAmountTextLen；必须先于正则与 BigInt 执行。 */
export const MAX_AMOUNT_INPUT_LENGTH = 40;
/** int4 最大值是 10 位十进制；先挡超长文本，再进入正则与 BigInt。 */
export const MAX_COUNT_INPUT_LENGTH = 10;

/** 与 money.CurrencyScale 已登记代码一致；未知币种不猜最小单位位数。 */
export const SUPPORTED_FINANCE_CURRENCIES = [
  "USD",
  "CNY",
  "EUR",
  "GBP",
  "HKD",
  "AUD",
  "CAD",
  "SGD",
  "JPY",
  "KRW",
  "VND",
] as const;

const SUPPORTED_CURRENCY_SET = new Set<string>(SUPPORTED_FINANCE_CURRENCIES);
const USERINFO_URL = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[^/]*@/;

export function parseScale6MajorUnits(raw: string, required: boolean): AmountParseResult {
  if (raw.length > MAX_AMOUNT_INPUT_LENGTH) {
    return { ok: false, error: `金额文本最多 ${MAX_AMOUNT_INPUT_LENGTH} 个字符` };
  }
  const value = raw.trim();
  if (!value) {
    return required ? { ok: false, error: "金额必填" } : { ok: true, minor: "0" };
  }
  if (!/^[0-9]+(?:\.[0-9]{1,6})?$/.test(value)) {
    return {
      ok: false,
      error: "金额只能是不带符号的十进制，整数部分必填且最多 6 位小数",
    };
  }

  const [whole = "0", fraction = ""] = value.split(".");
  const minor = BigInt(whole) * SCALE_FACTOR + BigInt(fraction.padEnd(6, "0") || "0");
  if (minor > MAX_SIGNED_INT64) {
    return { ok: false, error: "金额超过 signed int64 可表示范围" };
  }
  return { ok: true, minor: minor.toString() };
}

export function effectiveDaysInclusive(startsOn: string, expiresOn: string): number | null {
  const start = dayIndex(startsOn);
  const end = dayIndex(expiresOn);
  if (start === null || end === null || end < start) return null;
  return end - start + 1;
}

export function validateSubscriptionBatchForm(
  values: SubscriptionBatchFormValues,
): SubscriptionBatchFormErrors {
  const errors: SubscriptionBatchFormErrors = {};
  validateAmounts(values.paidMajor, values.surchargeMajor, errors);
  validateCurrency(values.currency, errors);
  const dates = validateDateRange(values.startsOn, values.expiresOn);
  if (dates.start) errors.startsOn = dates.start;
  if (dates.end) errors.expiresOn = dates.end;
  if (values.accountCount.length > MAX_COUNT_INPUT_LENGTH) {
    errors.accountCount = `账号数量最多 ${MAX_COUNT_INPUT_LENGTH} 个字符`;
  } else if (safePositiveInteger(values.accountCount) === null) {
    errors.accountCount = "账号数量必须是 1 到 2147483647 的整数";
  }
  return errors;
}

export function buildSubscriptionBatchParams(
  accountId: string,
  values: SubscriptionBatchFormValues,
): RegisterSubscriptionBatchParams {
  if (Object.keys(validateSubscriptionBatchForm(values)).length > 0) {
    throw new Error("订阅批次表单尚有错误");
  }
  const paid = requireParsedAmount(values.paidMajor, true);
  const surcharge = requireParsedAmount(values.surchargeMajor, false);
  const count = safePositiveInteger(values.accountCount);
  if (count === null) throw new Error("订阅批次账号数量无效");
  const proxyAssetId = values.proxyAssetId.trim();
  return {
    upstream_account_id: accountId,
    paid_minor: paid,
    surcharge_minor: surcharge,
    currency: values.currency.trim(),
    starts_on: values.startsOn.trim(),
    expires_on: values.expiresOn.trim(),
    account_count: count,
    ...(proxyAssetId ? { proxy_asset_id: proxyAssetId } : {}),
  };
}

export function validateProxyAssetForm(
  values: ProxyAssetFormValues,
  mode: "create" | "edit",
): ProxyAssetFormErrors {
  const errors: ProxyAssetFormErrors = {};
  if (mode === "create") {
    validateAmounts(values.paidMajor, values.surchargeMajor, errors);
    validateCurrency(values.currency, errors);
    const dates = validateDateRange(values.openedOn, values.expiresOn);
    if (dates.start) errors.openedOn = dates.start;
    if (dates.end) errors.expiresOn = dates.end;
    if (values.sharedAccountCount.length > MAX_COUNT_INPUT_LENGTH) {
      errors.sharedAccountCount = `共享账号数量最多 ${MAX_COUNT_INPUT_LENGTH} 个字符`;
    } else if (safePositiveInteger(values.sharedAccountCount) === null) {
      errors.sharedAccountCount = "共享账号数量必须是 1 到 2147483647 的整数";
    }
  }

  const credentialRef = values.credentialRef.trim();
  if (credentialRef) {
    const problem = validateCredentialRef(credentialRef);
    if (problem) errors.credentialRef = problem;
  }
  if (USERINFO_URL.test(values.buyAddress.trim())) {
    errors.buyAddress = "购买地址不得在 URL 中包含 user:pass@；凭据请使用 CredentialRef";
  }
  return errors;
}

export function buildProxyAssetParams(
  _values: ProxyAssetFormValues,
  mode: "create",
): ProxyAssetCreateParams;
export function buildProxyAssetParams(
  _values: ProxyAssetFormValues,
  mode: "edit",
  proxyAssetId: string,
): ProxyAssetEditParams;
export function buildProxyAssetParams(
  values: ProxyAssetFormValues,
  mode: "create" | "edit",
  proxyAssetId?: string,
): ProxyAssetCreateParams | ProxyAssetEditParams {
  if (Object.keys(validateProxyAssetForm(values, mode)).length > 0) {
    throw new Error("代理资产表单尚有错误");
  }
  const editable = {
    buy_platform: values.buyPlatform.trim(),
    buy_address: values.buyAddress.trim(),
    credential_ref: values.credentialRef.trim(),
    mounted: values.mounted,
  };
  if (mode === "edit") {
    const id = proxyAssetId?.trim();
    if (!id) throw new Error("编辑代理资产时缺少 proxy_asset_id");
    return { proxy_asset_id: id, ...editable };
  }

  const count = safePositiveInteger(values.sharedAccountCount);
  if (count === null) throw new Error("代理资产共享账号数量无效");
  return {
    paid_minor: requireParsedAmount(values.paidMajor, true),
    surcharge_minor: requireParsedAmount(values.surchargeMajor, false),
    currency: values.currency.trim(),
    opened_on: values.openedOn.trim(),
    expires_on: values.expiresOn.trim(),
    shared_account_count: count,
    ...editable,
  };
}

// --- 生命周期表单（退款 / 终止）---
//
// 退款与终止共用一份表单状态：两者都要一个生效日、一句理由和一次二次确认，
// 只有「累计退款额」是退款独有的。合成一个而不是各写一份，是因为这两张表单
// 上真正要紧的东西是**同一条**——那句「这一步不可逆」的说明与它后面那个勾。
// 拆开就会有一处先漂走。

/** 表单侧金额的定点标度。与后端 `money.MicroScale` 及 read DTO 的
 *  `financeAmountScale`（`httpapi/profit_daily.go`）一致。**只在标度相同时**
 *  才拿 DTO 的金额与表单值比较——跨标度比较是「差一万倍且不报错」那一类错误。 */
export const FINANCE_FORM_AMOUNT_SCALE = 6;

export type LifecycleAction = "refund" | "terminate";

export interface LifecycleFormValues {
  /** 人类主单位的**累计**退款总额；终止表单不用这一格。 */
  refundedMajor: string;
  /** 退款生效日或终止日（同一格，按 action 决定送 `refunded_on` 还是
   *  `terminated_on`）。 */
  effectiveOn: string;
  /** 进审计事件的理由（这四个 Action 的 Schema 必填字段）。 */
  reason: string;
  /** 二次确认的那个勾。它不是「同意条款」，是这次提交的前置条件。 */
  confirmed: boolean;
}

export type LifecycleFormErrors = Partial<Record<keyof LifecycleFormValues, string>>;

/** 校验要用到的、来自只读 DTO 的当前状态。 */
export interface LifecycleGuards {
  /** 当前已登记的累计退款额（DTO 的 `refunded.amount_minor`）。 */
  refundedMinor: string;
  /** 上一格的标度（DTO 的 `refunded.scale`）。 */
  refundedScale: number;
  currency: string;
}

/** 校验一次退款 / 终止提交。
 *
 *  这里拦的是两条**用户最容易填错**的规则，其余一律让服务端说话：
 *
 *  - 「累计退款额不得低于已登记值」在仓储里是 `ErrRefundNotDecreasing`；
 *  - 「已经终止过」是 `ErrAlreadyTerminated`，由入口自己收起来（见
 *    SubscriptionLifecycleDialog 的 `alreadyTerminated`），不在这一层。
 *
 *  ⚠️ **下面这段归因已经过期，留着是为了说清它变了**（XM-READONLY-QUERIES）：
 *  这两条以前之所以**必须**在前端拦，是因为 `finance.domainError` 没有把它们
 *  列进映射表，于是它们落到 default 分支被内核归一成 `EXECUTION_FAILED` + 一句
 *  「action … 执行失败」，服务端的原话到不了界面。那两条映射现在补上了
 *  （退款 → INVALID_PARAMS/400，重复终止 → CONFLICT/409，文案都是完整句子，
 *  见 `internal/platform/httpapi/finance_domain_error_integration_test.go`）。
 *  所以这一层现在是**优化**而不是必需：它省掉一次往返，而不再是「服务端说不清」。
 *  本片只补后端映射、不动这里的行为；要不要简化留给后续切片。
 *
 *  反过来，**生效日必须落在有效期内**这一条这里不判：它在仓储里是
 *  `ErrInvalidFormat`，`domainError` 会把它映成 INVALID_PARAMS，连同
 *  「终止日 X 必须落在有效期 A..B 内」这句完整的话一起回到界面。前端再写一遍
 *  只会有两份会漂开的判据，而服务端那一份是权威。 */
export function validateLifecycleForm(
  values: LifecycleFormValues,
  action: LifecycleAction,
  guards: LifecycleGuards,
): LifecycleFormErrors {
  const errors: LifecycleFormErrors = {};

  if (action === "refund") {
    const parsed = parseScale6MajorUnits(values.refundedMajor, true);
    if (!parsed.ok) {
      errors.refundedMajor = parsed.error;
    } else {
      const floor = refundFloor(guards);
      if (floor !== null && BigInt(parsed.minor) < floor.minor) {
        errors.refundedMajor =
          `累计退款额不得低于已登记的 ${floor.text}：这一格填的是累计总额，不是本次新增。` +
          `服务端也会拒绝，但那条拒绝在界面上只会显示成一句「执行失败」，看不出是这个原因。`;
      }
    }
  }

  if (dayIndex(values.effectiveOn) === null) {
    errors.effectiveOn =
      action === "refund"
        ? "退款生效日必须是有效的 YYYY-MM-DD"
        : "终止日必须是有效的 YYYY-MM-DD";
  }

  if (!values.reason.trim()) {
    errors.reason =
      "理由必填：它原样进审计事件，是事后唯一说得清「这笔钱为什么动」的记录。";
  }

  if (!values.confirmed) {
    errors.confirmed =
      action === "refund"
        ? "请先勾选确认：累计退款额登记之后只能增不能减，服务端不接受调低。"
        : "请先勾选确认：终止只能做一次，终止日登记之后不可再改，平台没有撤销终止的 Action。";
  }

  return errors;
}

/** 退款额的下限：当前已登记的累计退款额。
 *
 *  两种情况返回 null（**不拦**，让服务端去判）：DTO 的标度与表单不一致——
 *  跨标度比大小得出的结论是错的；以及当前累计为 0 或读不出来——0 不构成下限，
 *  任何合法金额都不小于它，写一条恒真的校验只会让人以为这里有把关。 */
function refundFloor(guards: LifecycleGuards): { minor: bigint; text: string } | null {
  if (guards.refundedScale !== FINANCE_FORM_AMOUNT_SCALE) return null;
  if (!/^[0-9]+$/.test(guards.refundedMinor.trim())) return null;
  const minor = BigInt(guards.refundedMinor.trim());
  if (minor <= 0n) return null;
  return {
    minor,
    text: formatScaledMinorUnits(guards.refundedMinor, guards.currency, guards.refundedScale),
  };
}

/** 退款 Action 里除资源 id 之外的三个字段。
 *
 *  字段名逐字对应 `subscriptionBatchRefundDef` / `proxyAssetRefundDef` 的
 *  Schema；资源 id 那一格刻意留给调用点填，因为两个 Action 的 id 字段**不同名**
 *  （`subscription_batch_id` / `proxy_asset_id`），在这里合并会把差异藏起来。 */
export function lifecycleRefundFields(values: LifecycleFormValues): {
  refunded_minor: string;
  refunded_on: string;
  reason: string;
} {
  const parsed = parseScale6MajorUnits(values.refundedMajor, true);
  if (!parsed.ok) throw new Error("退款金额尚有错误");
  return {
    refunded_minor: parsed.minor,
    refunded_on: values.effectiveOn.trim(),
    reason: values.reason.trim(),
  };
}

/** 终止 Action 里除资源 id 之外的两个字段。 */
export function lifecycleTerminateFields(values: LifecycleFormValues): {
  terminated_on: string;
  reason: string;
} {
  return {
    terminated_on: values.effectiveOn.trim(),
    reason: values.reason.trim(),
  };
}

function validateAmounts<T extends { paidMajor?: string; surchargeMajor?: string }>(
  paidRaw: string,
  surchargeRaw: string,
  errors: T,
): void {
  const paid = parseScale6MajorUnits(paidRaw, true);
  const surcharge = parseScale6MajorUnits(surchargeRaw, false);
  if (!paid.ok) errors.paidMajor = paid.error;
  if (!surcharge.ok) errors.surchargeMajor = surcharge.error;
  if (paid.ok && surcharge.ok && BigInt(paid.minor) + BigInt(surcharge.minor) > MAX_SIGNED_INT64) {
    errors.surchargeMajor = "实付与附加费合计超过 signed int64 可表示范围";
  }
}

function validateCurrency<T extends { currency?: string }>(raw: string, errors: T): void {
  const currency = raw.trim();
  if (!SUPPORTED_CURRENCY_SET.has(currency)) {
    errors.currency = "币种必须是平台已登记的三位大写代码";
  }
}

function validateDateRange(
  startRaw: string,
  endRaw: string,
): { start?: string; end?: string } {
  const errors: { start?: string; end?: string } = {};
  const start = dayIndex(startRaw);
  const end = dayIndex(endRaw);
  if (start === null) errors.start = "开始日期必须是有效的 YYYY-MM-DD";
  if (end === null) errors.end = "结束日期必须是有效的 YYYY-MM-DD";
  if (start !== null && end !== null && end < start) {
    errors.end = "结束日期不得早于开始日期（起止日均计入）";
  }
  return errors;
}

function dayIndex(raw: string): number | null {
  const value = raw.trim();
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const milliseconds = Date.parse(`${value}T00:00:00.000Z`);
  if (!Number.isFinite(milliseconds)) return null;
  const date = new Date(milliseconds);
  if (date.toISOString().slice(0, 10) !== value) return null;
  return milliseconds / 86_400_000;
}

function safePositiveInteger(raw: string): number | null {
  if (raw.length > MAX_COUNT_INPUT_LENGTH) return null;
  const value = raw.trim();
  if (!/^[1-9]\d*$/.test(value)) return null;
  const integer = BigInt(value);
  if (integer > MAX_POSTGRES_INTEGER) return null;
  // 这里是 Action 的 int4 计数字段，不是金额；先以 BigInt 锁到 DB 上界后才转换。
  return Number(integer);
}

function requireParsedAmount(raw: string, required: boolean): string {
  const parsed = parseScale6MajorUnits(raw, required);
  if (!parsed.ok) throw new Error("金额字段尚有错误");
  return parsed.minor;
}
