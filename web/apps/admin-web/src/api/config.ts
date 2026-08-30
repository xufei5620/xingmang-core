import { getRuntimeConfig } from "../auth/runtimeConfig";

/** 平台 API 客户端配置。
 *
 *  身份三件套集中在这里，不散落到各个请求点：换鉴权方式时只改这一处。
 *  XM-AUTH1 起它们只在 authMode=dev-header 时使用；oidc 模式下
 *  principalId/principalType/scopes 由 Keycloak 下发的 Access Token 取代，
 *  客户端改发 Authorization: Bearer（见 client.ts 的 BearerTokenProvider）。 */
export interface PlatformApiConfig {
  /** 基地址。空串表示同源（开发时由 Vite 代理转发到 127.0.0.1:8080）。 */
  baseUrl: string;
  principalId: string;
  principalType: PrincipalType;
  scopes: string[];
  /** 显式查询环境；undefined 表示不传 environment 参数。 */
  environment?: string;
}

/** 后端 `principal.ParseType` 接受的两种主体类型（宪法 6 条：人机身份分域）。 */
export type PrincipalType = "HUMAN" | "MACHINE";

/** 开发身份默认持有的 scope。
 *
 *  前两个是只读看板要的（对应后端路由上的 RequireScope）；后两个是 XM-0026
 *  新增的：audit.read 用于审计事件页，registry.service.manage 是
 *  registry.service.create / observe 两个 Action 的 Permission
 *  （internal/platform/registry/actions.go）。
 *
 *  这里多给一个 scope **不等于**放权：服务端才是最终裁决者，
 *  拿着不该有的 scope 请求照样 403（宪法：前端隐藏不构成安全控制）。
 *  TODO(XM-0008): 这份清单由 Keycloak 下发的 Access Token 取代。 */
export const DEFAULT_SCOPES = [
  "registry.read",
  "ops.read",
  "audit.read",
  "registry.service.manage",
  // XM-0033 告警中心的两个写权限。**刻意分开两个 scope**：确认只是说
  // 「我看见了」，静默是让告警不再出现也不再投递，爆炸半径差一个量级
  // （internal/platform/alerts/permissions.go）。
  // 读路径复用 ops.read，不需要再加。
  "alerts.alert.manage",
  "alerts.silence.manage",
  // XM-0037d 成本看板。**不复用 ops.read**：登记簿与台账里的是倍率
  // （我们从上游拿到几折）与逐渠道毛利，比看板上的余额数字敏感一个量级
  // （internal/platform/finance/permissions.go 的 ScopeRead）。
  "finance.read",
  // XM-0039 请求详情。**刻意分开两级**：request.read 只看元数据列表；
  // request.content.read 才能看对话正文，且服务端每次读取都写审计事件
  // （internal/platform/requestlog/permissions.go）。
  "request.read",
  "request.content.read",
  // XM-0046 用户管理。**不复用 ops.read**：那看到的是聚合数字，
  // 这是逐用户的资金明细（internal/platform/platformusers/permissions.go）。
  "platform.users.read",
  // KEY_SCOPE_APPROVAL（2026-08-30）：开发态演示允许读取元数据-only Key
  // 列表；服务端仍按独立 scope 裁决，生产 admin/staff 默认映射不包含它。
  "platform.user_keys.read",
  // XM-0048 上游管理（成本登记簿）的写路径。读路径复用上面 XM-0037d 已加的
  // finance.read —— 登记簿与看板供数是同一个 ScopeRead。
  //
  // 三个写权限**刻意分开**，依据是爆炸半径而不是整齐：改倍率直接决定
  // 毛利报表长什么样，写错令牌映射会把成本记到别的渠道上（两条渠道一个虚高
  // 一个虚低、合计却完全正确，是最难从总数上看出来的一类错误）。
  "finance.upstream_account.manage",
  "finance.recharge_ratio.manage",
  "finance.token_map.manage",
  // XM-B003：只读写当前 HUMAN Principal 在当前 Environment 下自己的表格视图。
  // 业务数据权限没有随它扩大；生产仍由 OIDC RoleScopeMap 最终裁决。
  "ui.saved_view.manage",
  // XM-CRED0：开发态只为预览凭据 Action 边界携带该 scope；服务端仍需独立
  // 授权，staff/admin 默认映射刻意不包含它。
  "credential.manage",
  // XM-CRED0 接入模式：connector.config.set@1 与 GET /connectors/config。
  // 同为开发态默认；服务端独立裁决，生产 admin/staff 映射不含它。
  "connector.manage",
];

function parseScopes(raw: string | undefined): string[] {
  if (!raw) return DEFAULT_SCOPES;
  const parsed = raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  return parsed.length > 0 ? parsed : DEFAULT_SCOPES;
}

function parsePrincipalType(raw: string | undefined): PrincipalType {
  return raw === "MACHINE" ? "MACHINE" : "HUMAN";
}

/** 从构建期环境变量组装配置。
 *
 *  environment 默认**不传**：后端 resolveEnvironment 的规则是「不传用调用者
 *  自己的环境，传了必须一致」，而前端并不知道服务端把自己配成了哪个环境。
 *  猜一个值只会换来 403，不传反而总是对的（跨环境读取本来就不允许）。 */
export function configFromEnv(
  env: ImportMetaEnv,
  /** 运行时配置（/app-config.js）里的 environment 优先于构建期变量。 */
  runtime: { environment?: string | undefined } = {},
): PlatformApiConfig {
  return {
    baseUrl: env.VITE_XM_API_BASE_URL ?? "",
    principalId: env.VITE_XM_PRINCIPAL_ID ?? "dev-operator",
    principalType: parsePrincipalType(env.VITE_XM_PRINCIPAL_TYPE),
    scopes: parseScopes(env.VITE_XM_SCOPES),
    environment: runtime.environment ?? env.VITE_XM_ENVIRONMENT,
  };
}

/** 应用默认配置。测试里请自己造 PlatformApiConfig，不要依赖它。 */
export const appApiConfig: PlatformApiConfig = configFromEnv(import.meta.env, getRuntimeConfig());
