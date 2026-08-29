import type {
  ProxyAssetCreateParams,
  ProxyAssetEditParams,
  RegisterSubscriptionBatchParams,
} from "../api/finance";
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
