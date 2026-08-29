/** 上游账号登记表单的取值与校验。
 *
 *  这里的校验**不是安全控制**，只是让人不必把表单提交出去才知道打错了字
 *  （宪法：服务端为最终裁决）。规则逐条对齐后端，改后端时这里也要跟着改:
 *  - 凭据引用 `secret://<scope>/<name>`:secrets.ParseCredentialRef
 *  - base_url `^https://[^@\s]+$`:finance.validateBaseURL(拒 `@` 是因为
 *    明文凭证藏在 user:pass@ 段里是只读通道最常见的泄漏形态)
 *  - 倍率：money.ParseRatio(定点十进制，最多 9 位小数，且必须为正)
 *  - 倍率与接入方式的三套要求：finance.UpstreamAccount.validateRatio
 *  - currency / business_day_tz / platform_id 三条正则：finance/account.go
 *
 *  **不校验的东西也是有意的**:environment 不做成输入项（取自身份，宪法 15 条）,
 *  凭据明文本身根本不进这个表单（只填引用，宪法 7 条）。 */

export interface UpstreamFormValues {
  /** 空串 = 新登记；有值 = 改这一条。 */
  upstream_account_id: string;
  system_type: string;
  access_method: string;
  upstream_name: string;
  upstream_contact: string;
  upstream_group: string;
  base_url: string;
  credential_ref: string;
  recharge_ratio: string;
  group_rate: string;
  currency: string;
  business_day_tz: string;
  platform_id: string;
  status: string;
}

export type UpstreamFormField = keyof UpstreamFormValues;
export type UpstreamFormErrors = Partial<Record<UpstreamFormField, string>>;

const LABELS: Record<UpstreamFormField, string> = {
  upstream_account_id: "账号 ID",
  system_type: "系统类型",
  access_method: "接入方式",
  upstream_name: "上游名称",
  upstream_contact: "上游联系人",
  upstream_group: "上游分组",
  base_url: "上游网址",
  credential_ref: "凭据引用",
  recharge_ratio: "充值倍率",
  group_rate: "分组倍率",
  currency: "币种",
  business_day_tz: "业务日切时区",
  platform_id: "接入平台",
  status: "状态",
};

export function upstreamFieldLabel(field: UpstreamFormField): string {
  return LABELS[field];
}

/** finance.systemTypeEnum。 */
export const SYSTEM_TYPE_OPTIONS = [
  { value: "sub2api", label: "sub2api(成本走每令牌 /v1/usage)" },
  { value: "newapi", label: "newapi(成本走 /api/log/self/stat)" },
  { value: "official", label: "official(原厂官方 API 直连)" },
];

/** finance.accessMethodEnum。 */
export const ACCESS_METHOD_OPTIONS = [
  { value: "upstream_key", label: "上游中转（计量：实扣 ÷ 倍率）" },
  { value: "official_api", label: "官方 API(成本口径 v1 待定)" },
  { value: "subscription_account", label: "订阅账号（按批次摊销）" },
];

/** finance.statusEnum。 */
export const UPSTREAM_STATUS_OPTIONS = [
  { value: "active", label: "启用（active）" },
  { value: "disabled", label: "停用（disabled）" },
];

/** 后端 defaultIfBlank 的两个缺省值（finance/account.go）。
 *
 *  写在这里是为了**预填**，不是为了在前端替后端兜底：预填之后人看得见
 *  「不填会变成什么」，而一个空着的币种框会让人以为它可以留空。 */
export const DEFAULT_CURRENCY = "USD";
export const DEFAULT_BUSINESS_DAY_TZ = "+08:00";

export const EMPTY_UPSTREAM_FORM: UpstreamFormValues = {
  upstream_account_id: "",
  system_type: "",
  access_method: "",
  upstream_name: "",
  upstream_contact: "",
  upstream_group: "",
  base_url: "",
  credential_ref: "",
  recharge_ratio: "",
  group_rate: "",
  currency: DEFAULT_CURRENCY,
  business_day_tz: DEFAULT_BUSINESS_DAY_TZ,
  platform_id: "",
  status: "active",
};

/** 与 secrets.refPart 逐字一致。 */
const REF_PART = /^[a-z0-9][a-z0-9-]{0,63}$/;
const CURRENCY = /^[A-Z]{3}$/;
const BUSINESS_DAY_TZ = /^[+-][0-9]{2}:[0-9]{2}$/;
/** 与 finance.platformIDPattern 逐字一致（与 refPart 同形，但来源不同，不复用同一常量）。 */
const PLATFORM_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;

/** money.maxRatioScale。 */
const MAX_RATIO_SCALE = 9;

/** 校验凭据引用的形状。合法返回 undefined。
 *
 *  只判形状，**不判这个引用是否真的存在**：那要问 SecretProvider,
 *  而前端永远不该有一条能列举凭据的通道（宪法 7 条）。 */
export function validateCredentialRef(raw: string): string | undefined {
  const ref = raw.trim();
  if (!ref) return "凭据引用必填：登记簿只存引用，明文由 SecretProvider 解析";
  if (!ref.startsWith("secret://")) return "凭据引用必须以 secret:// 开头";
  const rest = ref.slice("secret://".length);
  const slash = rest.indexOf("/");
  if (slash < 0) return "凭据引用要有 <scope>/<name> 两段，形如 secret://sub2api/prod-key";
  const scope = rest.slice(0, slash);
  const name = rest.slice(slash + 1);
  if (!REF_PART.test(scope) || !REF_PART.test(name)) {
    return "scope 与 name 只能用小写字母、数字与短横线，且以字母或数字开头，各最长 64 位";
  }
  return undefined;
}

/** 校验倍率文本。合法返回 undefined。
 *
 *  自己逐字符判而不是 `Number(raw)`:`Number("1.15")` 得到的已经不是 1.15,
 *  而 `Number("1e3")`、`Number(" 1 ")`、`Number("0x10")` 全都「合法」——
 *  用它做校验会放进去一批后端必然拒绝的写法（宪法 13 条）。 */
export function validateRatioText(raw: string): string | undefined {
  const s = raw.trim();
  if (!s) return "倍率不能为空白";
  if (s.startsWith("-")) {
    return "倍率必须为正：非正倍率会被静默当成 1，让「上游涨价」与「倍率填错」在台账上无法区分";
  }
  const body = s.startsWith("+") ? s.slice(1) : s;
  const dot = body.indexOf(".");
  const intPart = dot < 0 ? body : body.slice(0, dot);
  const fracPart = dot < 0 ? "" : body.slice(dot + 1);
  if (fracPart.includes(".")) return "倍率里有多个小数点";
  if (!isDigits(intPart) || !isDigits(fracPart)) return "倍率只能由数字与一个小数点组成";
  if (!intPart && !fracPart) return "倍率里没有数字";
  if (fracPart.length > MAX_RATIO_SCALE) {
    return `倍率最多 ${MAX_RATIO_SCALE} 位小数，给了 ${fracPart.length} 位`;
  }
  // 「是不是零」要在整数域里判：0、0.0、.000 都是零
  if (!/[1-9]/.test(intPart + fracPart)) return "倍率必须为正，不能是 0";
  return undefined;
}

function isDigits(s: string): boolean {
  return /^[0-9]*$/.test(s);
}

/** 这个接入方式**要不要**倍率（finance.validateRatio 的三个分支）。 */
export function ratioRequirement(
  accessMethod: string,
): "required" | "forbidden" | "optional" {
  switch (accessMethod) {
    case "upstream_key":
      return "required";
    case "subscription_account":
      return "forbidden";
    default:
      // official_api 的成本口径 v1 待定，后端不约束倍率的有无，前端也不替它决定
      return "optional";
  }
}

export function validateUpstreamForm(values: UpstreamFormValues): UpstreamFormErrors {
  const errors: UpstreamFormErrors = {};
  const v = trimValues(values);

  if (!v.system_type) errors.system_type = "系统类型必填";
  if (!v.access_method) errors.access_method = "接入方式必填";

  const credProblem = validateCredentialRef(v.credential_ref);
  if (credProblem) errors.credential_ref = credProblem;

  if (v.base_url) {
    if (!v.base_url.startsWith("https://")) {
      errors.base_url = "上游网址必须以 https:// 开头";
    } else if (v.base_url.includes("@")) {
      errors.base_url =
        "网址里不能出现 @：那一段常被用来夹带 user:pass，而网址会随审计摘要一起被记录并展示";
    } else if (/\s/.test(v.base_url)) {
      errors.base_url = "网址里不能有空白字符";
    }
  }

  const need = ratioRequirement(v.access_method);
  if (need === "forbidden" && v.recharge_ratio) {
    errors.recharge_ratio = "订阅型渠道不配倍率：它的成本走批次摊销，留一个用不上的倍率早晚有人拿去乘一遍";
  } else if (need === "required" && !v.recharge_ratio) {
    errors.recharge_ratio = "计量型渠道必须配倍率，否则台账里会出现一列算不出来的成本";
  } else if (v.recharge_ratio) {
    const problem = validateRatioText(v.recharge_ratio);
    if (problem) errors.recharge_ratio = problem;
  }

  // 分组倍率对三种接入方式都可选，但填了就必须是正的定点十进制。
  // 它与 recharge_ratio 的「计量必填 / 订阅禁填」分叉完全无关。
  if (v.group_rate) {
    const problem = validateRatioText(v.group_rate);
    if (problem) errors.group_rate = problem;
  }

  if (v.currency && !CURRENCY.test(v.currency)) {
    errors.currency = "币种须为三位大写 ISO 4217 码，如 USD";
  }
  if (v.business_day_tz && !BUSINESS_DAY_TZ.test(v.business_day_tz)) {
    errors.business_day_tz = "业务日切时区须为固定偏移如 +08:00(不接受 IANA 时区名)";
  }
  if (v.platform_id && !PLATFORM_ID.test(v.platform_id)) {
    errors.platform_id = "接入平台只能用小写字母、数字与短横线，且以字母或数字开头";
  }
  if (v.status && !UPSTREAM_STATUS_OPTIONS.some((o) => o.value === v.status)) {
    errors.status = "状态必须是 active / disabled 之一";
  }
  return errors;
}

export function hasUpstreamErrors(errors: UpstreamFormErrors): boolean {
  return Object.keys(errors).length > 0;
}

function trimValues(values: UpstreamFormValues): UpstreamFormValues {
  const out = { ...values };
  for (const key of Object.keys(out) as UpstreamFormField[]) out[key] = out[key].trim();
  return out;
}

/** 表单值 → finance.upstream_account.set 的 params。
 *
 *  **可选字段留空也照传空串**，与 registry 的登记表单相反。理由是这个 Action
 *  是整行替换（store.UpdateAccount 用 desired 覆盖整条），漏传一个字段等于
 *  把它清空；既然清空无法避免，就让「表单里看到什么」与「写进去什么」一致,
 *  不要再多一层「传了才算」的隐藏规则。所以对话框在修改模式下必须预填现值。
 *
 *  唯一例外是 upstream_account_id：留空是**新建**的信号，不是清空某个字段。 */
export function buildUpstreamParams(values: UpstreamFormValues): Record<string, string> {
  const v = trimValues(values);
  const params: Record<string, string> = {
    system_type: v.system_type,
    access_method: v.access_method,
    upstream_name: v.upstream_name,
    upstream_contact: v.upstream_contact,
    upstream_group: v.upstream_group,
    credential_ref: v.credential_ref,
    base_url: v.base_url,
    recharge_ratio: v.recharge_ratio,
    group_rate: v.group_rate,
    currency: v.currency,
    business_day_tz: v.business_day_tz,
    platform_id: v.platform_id,
    status: v.status,
  };
  if (v.upstream_account_id) params.upstream_account_id = v.upstream_account_id;
  return params;
}

// --- 令牌映射表单 ---

export interface TokenMapFormValues {
  upstream_token_id: string;
  own_account_id: string;
  credential_ref: string;
}

export const EMPTY_TOKEN_MAP_FORM: TokenMapFormValues = {
  upstream_token_id: "",
  own_account_id: "",
  credential_ref: "",
};

export type TokenMapFormErrors = Partial<Record<keyof TokenMapFormValues, string>>;

/** 校验令牌映射表单。
 *
 *  `credentialRequired` 对应 finance.tokenMapSetHandler 里的那条：sub2api 的
 *  计量型渠道每条映射都必须带 credential_ref，否则成本侧的 /v1/usage 打不出去。
 *  在登记这一刻拒绝，比让采集任务每轮报一次「凭据缺失」有用得多。 */
export function validateTokenMapForm(
  values: TokenMapFormValues,
  credentialRequired: boolean,
): TokenMapFormErrors {
  const errors: TokenMapFormErrors = {};
  if (!values.upstream_token_id.trim()) errors.upstream_token_id = "上游令牌标识必填";
  if (!values.own_account_id.trim()) errors.own_account_id = "我方账号标识必填";

  const ref = values.credential_ref.trim();
  if (credentialRequired && !ref) {
    errors.credential_ref =
      "sub2api 计量型渠道的每条映射必须带凭据引用：成本侧要用该令牌的凭据打 /v1/usage";
  } else if (ref) {
    const problem = validateCredentialRef(ref);
    if (problem) errors.credential_ref = problem;
  }
  return errors;
}

export function buildTokenMapParams(
  accountId: string,
  values: TokenMapFormValues,
): Record<string, string> {
  const params: Record<string, string> = {
    upstream_account_id: accountId,
    upstream_token_id: values.upstream_token_id.trim(),
    own_account_id: values.own_account_id.trim(),
  };
  // 这个 Action 是逐字段写入而不是整行替换，空引用不传:
  // newapi 侧走账号级会话，没有每令牌凭据是**正常状态**，不是「配漏了」
  const ref = values.credential_ref.trim();
  if (ref) params.credential_ref = ref;
  return params;
}
