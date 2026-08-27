/** 「登记服务」表单的取值与校验。
 *
 *  这里的校验**不是安全控制**，只是让人不必把表单提交出去才知道打错了字
 *  （宪法：服务端为最终裁决）。规则逐条对齐后端，改后端时这里也要跟着改：
 *  - 标识符：registry.ValidateIdentifier 的 `^[a-z0-9][a-z0-9-]{0,63}$`
 *  - endpoint 必须 https、internal_endpoint 必须 http(s)：registry.Service.Validate
 *  - environment 由身份决定，不做成输入项（见下方 buildServiceCreateParams）。 */

export interface ServiceFormValues {
  service_type: string;
  instance_id: string;
  endpoint: string;
  owner: string;
  internal_endpoint: string;
  health_check_path: string;
  native_console_url: string;
  runbook_path: string;
}

export type ServiceFormField = keyof ServiceFormValues;

export type ServiceFormErrors = Partial<Record<ServiceFormField, string>>;

export const EMPTY_SERVICE_FORM: ServiceFormValues = {
  service_type: "",
  instance_id: "",
  endpoint: "",
  owner: "",
  internal_endpoint: "",
  health_check_path: "",
  native_console_url: "",
  runbook_path: "",
};

/** 与 registry.identifierPattern 逐字一致。 */
const IDENTIFIER = /^[a-z0-9][a-z0-9-]{0,63}$/;
const IDENTIFIER_HINT = "只能用小写字母、数字与短横线，且以字母或数字开头，最长 64 位";

/** 必填的四个字段（registry.serviceCreateDef 里 Required 的那些，
 *  environment 除外——它不由人填）。 */
const REQUIRED: ServiceFormField[] = ["service_type", "instance_id", "endpoint", "owner"];

const LABELS: Record<ServiceFormField, string> = {
  service_type: "服务类型",
  instance_id: "实例标识",
  endpoint: "对外地址",
  owner: "负责人",
  internal_endpoint: "内网地址",
  health_check_path: "健康检查路径",
  native_console_url: "原生后台入口",
  runbook_path: "运行手册路径",
};

export function serviceFieldLabel(field: ServiceFormField): string {
  return LABELS[field];
}

/** 查询参数名里出现这些词，就当作有人把凭据填进了地址栏。
 *
 *  与后端 XM-0031 的同一条规则并行：endpoint 会被写进审计摘要，审计投影又原样
 *  返回并展示，于是一个 `?token=...` 就成了一条可复现的凭据回显链（Codex #7）。
 *  前端不是安全边界——服务端才是——但让人在提交前就看见「这里不能放密钥」，
 *  比事后去审计库里追一条已经泄露的记录便宜得多。 */
const CREDENTIAL_QUERY_WORDS = ["token", "key", "secret", "password"];

/** URL 里的凭据形态：userinfo（`user:pass@host`）或疑似凭据的查询参数。
 *  返回提示语，没问题时返回空串。 */
function credentialShapeIn(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    // 解析不出来的地址交给前面的 https:// 前缀规则去说，这里不重复报错
    return "";
  }
  if (url.username || url.password) {
    return "不能把用户名/密码写进地址（形如 https://user:pass@host）：它会随审计摘要一起被记录并展示";
  }
  for (const name of url.searchParams.keys()) {
    const lower = name.toLowerCase();
    if (CREDENTIAL_QUERY_WORDS.some((word) => lower.includes(word))) {
      return `查询参数 ${name} 看起来是凭据：地址会随审计摘要一起被记录并展示，不能放 token/key/secret/password`;
    }
  }
  return "";
}

/** 校验表单。返回空对象表示前端这一关过了（后端仍可能拒）。
 *
 *  值一律先 trim 再判：末尾一个空格换来一个 400，人还得自己去数空格。 */
export function validateServiceForm(values: ServiceFormValues): ServiceFormErrors {
  const errors: ServiceFormErrors = {};
  const v = trimValues(values);

  for (const field of REQUIRED) {
    if (!v[field]) errors[field] = `${LABELS[field]}必填`;
  }

  if (v.service_type && !IDENTIFIER.test(v.service_type)) {
    errors.service_type = `服务类型${IDENTIFIER_HINT}`;
  }
  if (v.instance_id && !IDENTIFIER.test(v.instance_id)) {
    errors.instance_id = `实例标识${IDENTIFIER_HINT}`;
  }
  // 明文 http 会把凭据与业务数据裸奔在内网之外，后端直接拒
  if (v.endpoint && !v.endpoint.startsWith("https://")) {
    errors.endpoint = "对外地址必须以 https:// 开头";
  }
  if (
    v.internal_endpoint &&
    !v.internal_endpoint.startsWith("http://") &&
    !v.internal_endpoint.startsWith("https://")
  ) {
    errors.internal_endpoint = "内网地址必须以 http:// 或 https:// 开头";
  }

  // 三个 URL 字段都过一遍凭据形态。放在最后：前缀规则先判完，
  // 一个字段上只显示一条最该先修的错
  for (const field of ["endpoint", "internal_endpoint", "native_console_url"] as const) {
    if (!v[field] || errors[field]) continue;
    const problem = credentialShapeIn(v[field]);
    if (problem) errors[field] = `${LABELS[field]}${problem}`;
  }
  return errors;
}

export function hasErrors(errors: ServiceFormErrors): boolean {
  return Object.keys(errors).length > 0;
}

function trimValues(values: ServiceFormValues): ServiceFormValues {
  const out = { ...values };
  for (const key of Object.keys(out) as ServiceFormField[]) out[key] = out[key].trim();
  return out;
}

/** 表单值 → registry.service.create 的 params。
 *
 *  两条纪律：
 *  1. environment 来自当前身份，不是输入项——后端 resolveEnvironment 只认
 *     调用者自己的环境，做成下拉框只会让人选出一个必然被拒的值（规格 §20.5）。
 *  2. 空的可选字段直接不传，而不是传空串：空串会被后端原样写进记录，
 *     于是「没填」和「填了个空」在库里长得一模一样。 */
export function buildServiceCreateParams(
  values: ServiceFormValues,
  environment: string,
): Record<string, string> {
  const v = trimValues(values);
  const params: Record<string, string> = {
    service_type: v.service_type,
    instance_id: v.instance_id,
    environment,
    endpoint: v.endpoint,
    owner: v.owner,
  };
  for (const field of [
    "internal_endpoint",
    "health_check_path",
    "native_console_url",
    "runbook_path",
  ] as const) {
    if (v[field]) params[field] = v[field];
  }
  return params;
}

// --- 观测上报表单 ---

/** 观测上报的默认水位：`wm-<UTC 紧凑时间戳>`。
 *
 *  用可读的 UTC 时间而不是 epoch 秒：水位会原样显示在注册表页上，
 *  人一眼要能看出「这是什么时候的数据」，而不是去心算一串数字。
 *  可编辑——真正的水位应当来自上游，手填只是 Foundation-A 的过渡手段。 */
export function defaultWatermark(now: Date = new Date()): string {
  const iso = now.toISOString(); // 2026-08-27T10:15:30.123Z
  return `wm-${iso.slice(0, 19).replace(/[-:]/g, "")}Z`;
}

/** registry.serviceObserveDef 的 status 枚举（registry.ServiceStatus）。 */
export const SERVICE_STATUS_OPTIONS = [
  { value: "active", label: "运行中（active）" },
  { value: "degraded", label: "降级（degraded）" },
  { value: "retired", label: "已下线（retired）" },
];

export interface ObserveFormValues {
  watermark: string;
  status: string;
}

export function validateObserveForm(values: ObserveFormValues): Partial<
  Record<keyof ObserveFormValues, string>
> {
  const errors: Partial<Record<keyof ObserveFormValues, string>> = {};
  if (!values.watermark.trim()) errors.watermark = "水位必填";
  if (!SERVICE_STATUS_OPTIONS.some((o) => o.value === values.status)) {
    errors.status = "状态必须是 active / degraded / retired 之一";
  }
  return errors;
}
