/** 前端**运行时**配置（XM-AUTH1，XM-LOGIN 加入 local 模式）。
 *
 *  鉴权方式不能烧进构建产物：同一个 web 镜像要同时服务 staging（dev-header）
 *  与生产（如今是 local：管理台自带账号密码登录；oidc 分支保留但不再是生产默认），
 *  构建期的 `VITE_*` 变量做不到「一个镜像、两种环境」。
 *  于是容器启动时由 deploy/docker/web-app-config.sh 按环境变量生成
 *  `/app-config.js`，index.html 在业务包之前用普通 `<script>` 同步加载它，
 *  文件里只做一件事：`window.__XM_CONFIG__ = {…}`。
 *
 *  读取顺序（逐字段回落，不是整体二选一）：
 *    1. `window.__XM_CONFIG__`（容器生成；本地 public/app-config.js 是空壳）
 *    2. Vite 构建期变量 `VITE_XM_AUTH_MODE` / `VITE_XM_OIDC_ISSUER` / `VITE_XM_OIDC_CLIENT_ID`
 *    3. 默认 `dev-header`
 *
 *  文件缺失或不合法**不能**弄垮应用：这里只做纯解析，任何异常形状都回落到
 *  默认值并把问题记进 `problems`，由登录页如实展示。前端选错模式不构成安全
 *  问题——服务端才是最终裁决者（宪法：前端隐藏不构成安全控制；生产后端
 *  硬拒开发头）——但它会让人对着一屏 403 干瞪眼，所以要把原因说出来。
 *
 *  `local`：管理台自带账号密码登录（POST /api/v1/auth/login 等），会话是
 *  HttpOnly Cookie（xm_session），不需要 oidcIssuer / oidcClientId。 */

export type AuthMode = "dev-header" | "oidc" | "local";

/** `window.__XM_CONFIG__` 的契约。所有字段可省略；未知字段忽略。 */
export interface RuntimeConfigInput {
  authMode?: AuthMode;
  oidcIssuer?: string;
  oidcClientId?: string;
  /** 授权请求的 scope，空格分隔。默认 "openid profile email"。 */
  oidcScopes?: string;
  /** 显式查询环境；省略＝不传 environment 参数（理由见 api/config.ts）。 */
  environment?: string;
}

export interface RuntimeConfig {
  authMode: AuthMode;
  oidcIssuer: string;
  oidcClientId: string;
  oidcScopes: string;
  environment: string | undefined;
  /** authMode 这个决定性字段来自哪一层，登录页用来如实说明。 */
  source: "app-config" | "vite-env" | "default";
  /** 解析时发现的问题（原样给人看，不做安全判定）。 */
  problems: string[];
}

export const DEFAULT_OIDC_SCOPES = "openid profile email";

/** 只取本模块关心的几个 Vite 变量，测试里不必伪造整个 ImportMetaEnv。 */
export type RuntimeEnv = Pick<
  ImportMetaEnv,
  | "VITE_XM_AUTH_MODE"
  | "VITE_XM_OIDC_ISSUER"
  | "VITE_XM_OIDC_CLIENT_ID"
  | "VITE_XM_OIDC_SCOPES"
  | "VITE_XM_ENVIRONMENT"
>;

declare global {
  interface Window {
    __XM_CONFIG__?: unknown;
  }
}

function nonEmptyString(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : undefined;
}

function parseAuthMode(raw: unknown): AuthMode | undefined {
  const value = nonEmptyString(raw);
  if (value === "oidc" || value === "dev-header" || value === "local") return value;
  return undefined;
}

/** 纯函数：把 `window.__XM_CONFIG__` 的原始值与 Vite 变量合成一份配置。 */
export function resolveRuntimeConfig(raw: unknown, env: RuntimeEnv): RuntimeConfig {
  const problems: string[] = [];
  let input: Record<string, unknown> = {};
  if (raw !== undefined && raw !== null) {
    if (typeof raw === "object" && !Array.isArray(raw)) {
      input = raw as Record<string, unknown>;
    } else {
      problems.push("app-config.js 里的 window.__XM_CONFIG__ 不是对象，已忽略");
    }
  }

  let authMode: AuthMode = "dev-header";
  let source: RuntimeConfig["source"] = "default";
  const runtimeMode = parseAuthMode(input.authMode);
  const envMode = parseAuthMode(env.VITE_XM_AUTH_MODE);
  if (runtimeMode) {
    authMode = runtimeMode;
    source = "app-config";
  } else {
    // 空串/空白当作没配（生成脚本默认就写空值）；填了却不认识才算问题
    if (nonEmptyString(input.authMode) !== undefined || (input.authMode !== undefined && typeof input.authMode !== "string")) {
      problems.push(
        `app-config.js 的 authMode 只能是 dev-header、oidc 或 local（当前：${String(input.authMode)}），已回落`,
      );
    }
    if (envMode) {
      authMode = envMode;
      source = "vite-env";
    } else if (nonEmptyString(env.VITE_XM_AUTH_MODE)) {
      problems.push(
        `VITE_XM_AUTH_MODE 只能是 dev-header、oidc 或 local（当前：${env.VITE_XM_AUTH_MODE}），已回落`,
      );
    }
  }

  const oidcIssuer =
    nonEmptyString(input.oidcIssuer) ?? nonEmptyString(env.VITE_XM_OIDC_ISSUER) ?? "";
  const oidcClientId =
    nonEmptyString(input.oidcClientId) ?? nonEmptyString(env.VITE_XM_OIDC_CLIENT_ID) ?? "";
  const oidcScopes =
    nonEmptyString(input.oidcScopes) ??
    nonEmptyString(env.VITE_XM_OIDC_SCOPES) ??
    DEFAULT_OIDC_SCOPES;
  const environment =
    nonEmptyString(input.environment) ?? nonEmptyString(env.VITE_XM_ENVIRONMENT);

  if (authMode === "oidc") {
    if (!oidcIssuer) problems.push("authMode 是 oidc 但没有配置 oidcIssuer（XM_WEB_OIDC_ISSUER）");
    if (!oidcClientId)
      problems.push("authMode 是 oidc 但没有配置 oidcClientId（XM_WEB_OIDC_CLIENT_ID）");
  }

  return {
    authMode,
    oidcIssuer: oidcIssuer.replace(/\/+$/, ""),
    oidcClientId,
    oidcScopes,
    environment,
    source,
    problems,
  };
}

/** 应用当前的运行时配置。每次调用重新解析：代价可忽略，而测试可以随时改
 *  `window.__XM_CONFIG__` 而不必清缓存。 */
export function getRuntimeConfig(): RuntimeConfig {
  const raw = typeof window === "undefined" ? undefined : window.__XM_CONFIG__;
  return resolveRuntimeConfig(raw, import.meta.env);
}
