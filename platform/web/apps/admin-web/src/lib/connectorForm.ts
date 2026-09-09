import type { ConnectorMode } from "../api/connectors";
import { parseCredentialRef } from "./credentialForm";

/** 接入模式表单的四个字段，与页面控件一一对应。 */
export interface ConnectorFormValues {
  mode: ConnectorMode;
  endpoint: string;
  /** 逗号分隔的主机清单，按用户输入原样保存在表单里；发送前再拆分。 */
  targetAllowlist: string;
  credentialRef: string;
}

export type ConnectorFormField = keyof ConnectorFormValues;
export type ConnectorFormErrors = Partial<Record<ConnectorFormField, string>>;

export const CONNECTOR_MODE_OPTIONS: ReadonlyArray<{ value: ConnectorMode; label: string }> = [
  { value: "fake", label: "fake · 演示数据，不连上游" },
  { value: "real", label: "real · 只读连接真实上游" },
];

/** 把逗号分隔的输入拆成主机清单：去空白、去空项、忽略大小写去重。 */
export function splitAllowlist(raw: string): string[] {
  const seen = new Set<string>();
  const hosts: string[] = [];
  for (const part of raw.split(",")) {
    const host = part.trim();
    if (!host) continue;
    const key = host.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    hosts.push(host);
  }
  return hosts;
}

/** 主机名（可带端口）。不接受 scheme、路径或空格——那些说明有人把整条 URL 粘进来了。 */
const HOST_PATTERN =
  /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:\d{1,5})?$/i;

/** 去掉端口、统一小写，用于「endpoint 主机是否在允许清单内」的比对
 *  （对齐后端 connector.Config.Validate 的 hostOnly）。 */
export function hostOnly(host: string): string {
  return host.trim().toLowerCase().replace(/:\d+$/, "");
}

/** 只接受完整的 http(s):// URL；其它 scheme 或裸主机名返回 null。 */
export function parseHttpUrl(value: string): URL | null {
  const raw = value.trim();
  if (!raw) return null;
  try {
    const url = new URL(raw);
    return url.protocol === "http:" || url.protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}

/** 照抄后端 connector.config.set@1 的规则，让人在提交前就看到会被拒绝的原因：
 *  - fake 模式三个字段都可留空——演示数据不需要上游地址与凭据；但**给了值就
 *    必须合法**（后端同样以 INVALID_PARAMS 拒绝 http、带 user:pw@ 的地址）；
 *  - real 模式再加 connector.Config.Validate 那几条：必填、允许清单含上游主机。
 *  服务端仍是最终裁决者，这里通过不代表一定能保存。 */
export function validateConnectorForm(values: ConnectorFormValues): ConnectorFormErrors {
  const errors: ConnectorFormErrors = {};
  const real = values.mode === "real";

  const endpoint = values.endpoint.trim();
  let endpointHost: string | null = null;
  if (!endpoint) {
    if (real) errors.endpoint = "real 模式必须填写上游地址";
  } else {
    const url = parseHttpUrl(endpoint);
    if (!url) {
      errors.endpoint = "上游地址必须是完整的 https:// URL（含主机）";
    } else if (url.protocol !== "https:") {
      errors.endpoint = "上游地址必须是 https://，后端会拒绝明文 http";
    } else if (url.username || url.password) {
      // 地址里内嵌 user:pw@ 就是内联凭据——这一页存在的意义就是不让它出现
      errors.endpoint = "上游地址里不能带 user:password@；凭据只能通过 CredentialRef 引用";
    } else {
      endpointHost = url.hostname.toLowerCase();
    }
  }

  const hosts = splitAllowlist(values.targetAllowlist);
  const invalidHost = hosts.find((host) => !HOST_PATTERN.test(host));
  if (invalidHost !== undefined) {
    errors.targetAllowlist = `「${invalidHost}」不是合法主机名；只填主机（可带端口），多个用英文逗号分隔`;
  } else if (real && hosts.length === 0) {
    errors.targetAllowlist = "real 模式必须至少允许一个主机（ADR-004）";
  } else if (real && endpointHost && !hosts.some((host) => hostOnly(host) === endpointHost)) {
    errors.targetAllowlist = `允许主机里没有上游地址的主机 ${endpointHost}；这样的配置一个请求都发不出去`;
  }

  const ref = values.credentialRef.trim();
  if (!ref) {
    if (real) errors.credentialRef = "real 模式必须指定 CredentialRef";
  } else if (!parseCredentialRef(ref)) {
    errors.credentialRef = "凭据引用必须是 secret://<scope>/<name>";
  }

  return errors;
}

export function connectorFieldLabel(field: ConnectorFormField): string {
  switch (field) {
    case "mode":
      return "接入模式";
    case "endpoint":
      return "上游地址";
    case "targetAllowlist":
      return "允许主机";
    case "credentialRef":
      return "CredentialRef";
  }
}

/** 数据库里当前生效的一行 → 表单初值。没有记录时给 fake 与该平台的预期引用。 */
export function connectorFormFromConfig(
  current:
    | { mode: string; endpoint: string; target_allowlist: readonly string[]; credential_ref: string }
    | undefined,
  defaultCredentialRef: string,
): ConnectorFormValues {
  if (!current) {
    return { mode: "fake", endpoint: "", targetAllowlist: "", credentialRef: defaultCredentialRef };
  }
  return {
    // 认不出来的模式按 fake 显示在表单里；「当前数据库值」区块仍原样显示原字符串
    mode: current.mode === "real" ? "real" : "fake",
    endpoint: current.endpoint,
    targetAllowlist: current.target_allowlist.join(", "),
    credentialRef: current.credential_ref || defaultCredentialRef,
  };
}
