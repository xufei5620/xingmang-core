# XM-AUTH1 · admin-web 真实登录（OIDC 授权码 + PKCE）

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-AUTH1`
- implementation commit: `4c8c23d`
- base: `758c223`（acceptance 线）
- worktree: `K:/星芒统一控制平台/acceptance/wt-auth1`

## scope

把 admin-web 从「三个 X-Dev-* 请求头自称身份」切到 Keycloak `solov-staff` Realm 的
授权码 + PKCE（S256）登录；鉴权方式由**运行时** `/app-config.js` 决定，同一个 web 镜像
服务 staging（dev-header）与生产（oidc）。dev-header 模式行为与之前逐字一致
（登录页仍是「开发模式进入」，请求头不变，既有 885+ 测试未改动）。

- `src/auth/runtimeConfig.ts`：`window.__XM_CONFIG__` → `VITE_XM_AUTH_MODE` /
  `VITE_XM_OIDC_ISSUER` / `VITE_XM_OIDC_CLIENT_ID` → `dev-header` 逐字段回落；文件缺失
  或写坏只回落并把问题写在登录页，不弄垮应用。
- `src/auth/oidc.ts`：零依赖（Web Crypto + fetch）。发现文档内存缓存并要求 `issuer`
  与配置逐字相等；`login()` 存 `state`/`code_verifier` 于 sessionStorage 后跳转；
  `handleCallback()` 校验 state（不匹配/带 error/事务缺失一律拒绝）、public client 换令牌、
  会话存 sessionStorage；`getAccessToken()` 距过期 < 60s 用 refresh_token 续期（单飞），
  续期失败清会话返回 null；`logout()` 清会话并跳 `end_session_endpoint`
  （`post_logout_redirect_uri` + `client_id` + `id_token_hint`），发现失败只回 `/login`。
  任何错误信息不含令牌片段。
- `src/api/client.ts`：新增 `auth?: BearerTokenProvider` 注入点。传入即发
  `Authorization: Bearer`，不再发 X-Dev-*；HTTP 401，或 403 且文案**逐字**为后端 OIDC 的
  固定拒绝句「缺少身份」/「身份令牌无效」→ 清会话回 `/login?next=…&reason=…`；
  「缺少权限 xxx」不跳登录。不传 `auth` ＝ dev-header，fetch 仍在同一 tick 同步发出。
- 路由：`/login`（oidc：「使用 solov 账号登录」；dev-header：「开发模式：无需登录」+
  「开发模式进入」）、`/auth/callback`、`RequireAuth` 门禁布局路由（带 `next`）。
  顶栏 oidc 模式显示 id_token 的 `preferred_username`（其次 `name`），退出走 Keycloak。
- 部署：`deploy/docker/web-app-config.sh` 挂 `/docker-entrypoint.d/40-xm-app-config.sh`，
  nginx 起来前按 `XM_WEB_AUTH_MODE` / `XM_WEB_OIDC_ISSUER` / `XM_WEB_OIDC_CLIENT_ID`
  生成 `/usr/share/nginx/html/app-config.js`（模式非法或 oidc 缺参 → 容器拒绝启动）；
  nginx 对 `/app-config.js` 发 `Cache-Control: no-store`；`launch.yaml` 透传三项
  （默认 dev-header，issuer/client id 回落到 `XM_OIDC_ISSUER` / `XM_OIDC_AUDIENCE`），
  `server-prod.yaml` 固定 oidc 且必填。
- 文档：`docs/modules/httpapi/AUTH-SWITCH.md` 新增第十节「前端」（运行时配置契约、
  登录/请求/登出流程、Keycloak Client 必须满足的设置）。

## files_changed

- `web/apps/admin-web/src/auth/runtimeConfig.ts`、`runtimeConfig.test.ts`
- `web/apps/admin-web/src/auth/oidc.ts`、`oidc.test.ts`
- `web/apps/admin-web/src/auth/session.ts`
- `web/apps/admin-web/src/auth/RequireAuth.tsx`、`RequireAuth.test.tsx`
- `web/apps/admin-web/src/auth/devSession.ts`（原 `src/auth.ts` 搬入；`src/auth.ts` 改为再导出）
- `web/apps/admin-web/src/pages/LoginPage.tsx`、`LoginPage.test.tsx`
- `web/apps/admin-web/src/pages/AuthCallbackPage.tsx`、`AuthCallbackPage.test.tsx`
- `web/apps/admin-web/src/api/client.ts`、`client.test.ts`、`src/api/config.ts`
- `web/apps/admin-web/src/router.tsx`（门禁改为 `RequireAuth` 布局路由、新增回调路由、顶栏用户/退出）
- `web/apps/admin-web/index.html`、`public/app-config.js`、`src/vite-env.d.ts`
- `deploy/docker/web-app-config.sh`（新，可执行）、`deploy/docker/web.Dockerfile`、`deploy/nginx/launch.conf`
- `deploy/compose/launch.yaml`、`deploy/compose/server-prod.yaml`、`deploy/compose/.env.example`
- `docs/modules/httpapi/AUTH-SWITCH.md`

## verification

- `pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck` — PASS。
- `pnpm --config.verify-deps-before-run=false --filter admin-web run test` — 68 文件 / 1020 用例全部 PASS
  （本片新增 76 个：PKCE 向量、发现+换令牌、state 不匹配、续期单飞与失败、登出、
  运行时配置回落、Bearer 注入与 401/403 处理、RequireAuth 重定向、回调页、登录页）。
- `pnpm --config.verify-deps-before-run=false --filter admin-web run build` — PASS；`dist/` 含
  `app-config.js` 且 `index.html` 引用它。仅保留既有大 chunk warning。
- `sh deploy/docker/web-app-config.sh`（改 `XM_WEB_APP_CONFIG_PATH` 落到临时目录）：
  默认 dev-header、oidc 三参齐全、含引号/反斜杠的值均生成可被 `new Function` 执行的合法 JS；
  `XM_WEB_AUTH_MODE=keycloak` 与 oidc 缺 issuer 均非零退出。
- `docker compose -f launch.yaml config` / `-f launch.yaml -f server-prod.yaml config`：
  三项透传与嵌套默认值按预期解析；生产缺 `XM_OIDC_ISSUER` 在 config 阶段即失败。
- `bash scripts/check-governance.sh` — PASS。

## not_run / risks

- **没有连过真实 Keycloak**：与 XM-0008 后端同款，全部测试用假发现文档 + 假令牌端点。
  真机联调前 Keycloak Client 必须加 `https://<host>/auth/callback`（redirect）、
  `https://<host>/login`（post logout）与 `https://<host>`（Web origins，否则换令牌时 CORS 失败）。
- 未做 web 镜像的 `docker build`（本机构建慢且与本片无关的层多）；入口脚本按官方 nginx
  镜像 `/docker-entrypoint.d/*.sh` 约定挂载，Dockerfile 内 `chmod +x`。
- `launch.yaml` 的 `platform-api` 仍未透传 `XM_AUTH_MODE` / `XM_OIDC_*`（XM-0008 遗留，
  本片未越界改后端段）；staging 想走 oidc 要先补那三行，前后端**必须同时**切。
- 后端对 OIDC 失败回 403 而不是 401（AUTH-SWITCH 第八节）；前端按固定文案识别，
  后端若改文案需同步 `client.ts` 的 `OIDC_REJECTED_MESSAGES`。
- 令牌在 sessionStorage：换标签页要重新走一次跳转（有 SSO 会话时无感）；这是有意取舍。

## handoff

验收线请重点审读 `src/auth/oidc.ts`（state/PKCE/续期语义）、`src/api/client.ts`
（401/403 判定边界）与 `deploy/docker/web-app-config.sh`（fail closed），并在
Keycloak Client 白名单补齐后做一次真机登录 → 刷新 → 5 分钟后续期 → 退出的手工验证。
