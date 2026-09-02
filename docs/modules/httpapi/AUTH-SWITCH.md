# 从 dev-header 切到 Keycloak 登录（XM-0008）

本文回答一个问题：**CR-0001 执行完的那天，怎么让平台走真实登录。**

前置：`docs/change-requests/CR-0001-keycloak-solov-staff-realm.md` 已由 auth-admin
执行并回写执行记录。CR-0001 之前，本文的所有配置都没有对应的 Realm 可连——
平台侧代码已经就位，但**不要**提前打开开关。

> 代码侧从未连接过任何真实 Keycloak。XM-0008 的全部测试用 `httptest` 起假
> 发现端点 + 假 JWKS + 自签 RSA 键，模仿的是 Keycloak 的形状，不是 Keycloak 本身。

## 一、开关

`XM_AUTH_MODE` 决定用哪个 `PrincipalResolver`：

| 值 | 身份来源 | 允许的环境 |
|---|---|---|
| `dev-header` | `X-Dev-Principal-ID` / `X-Dev-Principal-Type` / `X-Dev-Scopes` 请求头 | development / staging |
| `oidc` | Keycloak `solov-staff` Realm 的 Access Token | 全部（生产**只能**用它） |

默认值跟着 `ENVIRONMENT` 走：生产默认 `oidc`，其余默认 `dev-header`。
也就是说 **development / staging 的现状不受 XM-0008 影响**，栈照常跑。

三条 Fail Closed 规则（都有测试）：

1. `ENVIRONMENT=production` + `XM_AUTH_MODE=dev-header` → **拒绝启动**。
   请求头自称身份在生产等于没有鉴权。这里拒一次，`NewDevHeaderResolver`
   自己再拒一次，两道闸都留着；
2. `XM_AUTH_MODE=oidc` 缺 `XM_OIDC_ISSUER` 或 `XM_OIDC_AUDIENCE` → **拒绝启动**。
   少了 issuer 就没有信任根，少了 audience 就等于接受任何 Client 拿到的令牌；
3. `XM_AUTH_MODE` 是其他任何值 → **拒绝启动**，不静默回落。

## 二、切换步骤

1. 确认 CR-0001 已执行，且验证清单第 1 条通过：

   ```bash
   curl -s https://auth.solov.cc/realms/solov-staff/.well-known/openid-configuration \
     | python3 -m json.tool | head
   # issuer 必须逐字等于 https://auth.solov.cc/realms/solov-staff
   ```

2. **人工审定 RoleScopeMap**（见第四节）。这一步不能跳过——它决定登录进来的
   员工默认能看到什么；

3. 配三个环境变量（`deploy/compose/.env` 或宿主的 systemd unit）：

   ```bash
   XM_AUTH_MODE=oidc
   XM_OIDC_ISSUER=https://auth.solov.cc/realms/solov-staff
   XM_OIDC_AUDIENCE=xingmang-admin-web
   ```

4. 重启 `platform-api`。启动日志里应出现：

   ```json
   {"level":"INFO","msg":"oidc_resolver_ready","issuer":"https://auth.solov.cc/realms/solov-staff",
    "audience":"xingmang-admin-web","environment":"...","role_scope_map":{...}}
   ```

   看到 `auth_mode_dev_header` 这条 WARN 说明开关没生效，回到第 3 步。

5. 回滚：把 `XM_AUTH_MODE` 改回 `dev-header`（仅非生产）并重启。平台侧无状态，
   不涉及数据迁移。

> 前端的 OIDC 流（PKCE、跳转、令牌刷新）由 XM-AUTH1 补上，见第十节。
> 后端可以先单独用 `Authorization: Bearer <token>` 手工验证；但**对用户开放**
> 之前，web 容器的 `XM_WEB_AUTH_MODE` 必须与这里的 `XM_AUTH_MODE` 同时切换。

## 三、可选配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `XM_OIDC_JWKS_URL` | 从 issuer 的 `/.well-known/openid-configuration` 发现 | 发现端点不可达但 JWKS 可达的隔离网络才需要显式配。必须与 issuer 同源 |
| `XM_OIDC_ROLE_SCOPES` | 代码里的默认表 | JSON：`{"staff":["registry.read","ops.read","ui.saved_view.manage"]}`；显式配置部署时必须同步新增 scope |
| `XM_OIDC_CLOCK_SKEW` | `60s` | `exp`/`nbf`/`iat` 的时钟偏移容忍。上限 5 分钟——再大就把 CR-0001 定的 5 分钟令牌寿命架空了 |

## 四、RoleScopeMap 必须人工审定

ADR-016 与 CR-0001 §5：**平台细粒度权限不进 Keycloak**。Realm 只发 `staff`
这类粗粒度角色，`registry.read` / `ops.read` / `audit.read` /
`registry.service.manage` / `ui.saved_view.manage` 由平台自己解析。翻译表就是 RoleScopeMap。

代码里的默认表（`oidcauth.DefaultRoleScopeMap`）：

| Realm 角色 | 翻译成的平台 scope | 状态 |
|---|---|---|
| `staff` | `registry.read`、`ops.read`、`ui.saved_view.manage` | CR-0001 §5 会创建这个角色；个人视图 scope 只读写自己的偏好 |
| `admin` | 上面三个 + `audit.read` + `registry.service.manage` + `registry.connector.manage` + `registry.connection.manage` + `request.read` | **Realm 里今天没有这个角色**，预留位；不含 `platform.user_keys.read` |
| `key-metadata-reader` | `platform.user_keys.read` | KEY_SCOPE_APPROVAL 专门角色；只读元数据，不含完整凭据 |

三处刻意的保守，前两处审定时可以推翻，第三处**已经裁定过**，请先读完理由：

- **`staff` 默认不含 `audit.read`。** `PERMISSIONS.md` 已经论证过它比 `ops.read`
  高一档：审计事件带 `before_summary` / `after_summary`，是被改动对象的前后镜像；
  「看板角色拿到 ops.read 不应顺带看见全平台的操作明细」。而 CR-0001 里 `staff`
  是**唯一**的 Realm 角色——把 audit.read 塞进去，等于每个员工默认看见全部操作
  明细，和那段论证直接冲突。要给，应当是一次显式的人工决定；
- **`staff` 默认含 `ui.saved_view.manage`。** 这是一次明确授权：SavedView Query 与
  L0 set/remove Action 都只能作用于 Principal 派生的 owner + Environment，不能读写
  其他人，也不增加任何业务数据权限。读写共用一个 scope 是因为再拆 read/write
  不增加隔离；不要用 `registry.read` / `ops.read` 代替它；
- **`admin` 今天不会命中。** CR-0001 §5 只创建 `staff`。要加角色需要另开一张
  变更单（ADR-016 的变更单机制）。预留这一行只是为了「加角色时不用改代码」；
- **`admin` 含 `request.read`，但没有 `request.content.read`**（XM-0039 验收
  裁定，2026-08-28）。两者差着一个量级：前者是逐条的调用清单（谁、几点、什么
  模型、多少 token），后者是**用户与模型之间的完整对话**——用户自己粘进去的
  合同、简历、身份信息、源码都在里面（交接文档 §9.4 列为高敏数据）。
  按最小权限原则，看正文的应该是显式授权的客诉/风控岗，而不是每个管理员顺带
  获得的能力。这一条与上面两条不同：**它不是留待审定的默认值，是已经做出的
  决定**，`oidcauth/resolver_test.go` 有断言钉住，防止以后被顺手加回去。
  要授予就配一个专门的角色：
  `XM_OIDC_ROLE_SCOPES='{"request-auditor":["request.read","request.content.read"]}'`。

于是 CR-0001 执行完当天的效果是：员工能登录、能看服务清单与运营指标；
审计页与业务写操作会 403，直到有人显式授权；仅限自己的 SavedView L0 写操作可用。
**Fail Closed 比「先放开再收」便宜。**

要改，用环境变量而不是改代码：

```bash
XM_OIDC_ROLE_SCOPES='{"staff":["registry.read","ops.read","ui.saved_view.manage"],"auditor":["audit.read"]}'
```

左边是 Keycloak 的粗粒度角色，右边是平台 scope。**方向写反会被拒绝启动**：
把 `registry.service.manage` 当角色名，意味着有人打算在 Realm 里建它——正是
ADR-016 禁止的东西。

## 五、配置漂移会被记 warn

令牌里出现平台形态的权限串（`registry.*` / `ops.*` / `audit.*` / `platform.*` /
`action.*` / `connector.*` / `request.*` / `ui.*`），无论在 `realm_access.roles`、`scope` 还是
`resource_access` 里，平台都**忽略**并记一条 WARN：

```json
{"level":"WARN","msg":"oidc_fine_grained_role_ignored","error_code":"keycloak_scope_drift",
 "ignored":["registry.service.manage","scope:ops.read"],"adr":"ADR-016"}
```

平台侧忽略只是止血。真正的修复在 Realm 那边——按变更单流程移除这些角色。
巡检时按 `error_code=keycloak_scope_drift` 搜这条日志。

## 六、令牌校验做了什么

按顺序（**先签名后声明**：验签之前相信的任何字段都是攻击者可以随手改的）：

1. `Authorization: Bearer <token>`，长度上限 16 KiB；
2. 三段式 JWS；头部 `alg` 必须是 `RS256`（白名单，不是黑名单——`none` 与 HMAC
   混淆全靠这一条）；`crit` 非空即拒；`kid` 必填；
3. 按 `kid` 从 JWKS 取 RSA 公钥（< 2048 位的键直接拒），验 RS256 签名；
4. `iss` 与 `XM_OIDC_ISSUER` **精确相等**——这是「只接受 solov-staff Realm」的
   唯一执行点，前缀匹配会让 `...-test` 之类的 Realm 混进来；
5. 受众绑定：`aud` 含 `XM_OIDC_AUDIENCE` **或** `azp` 等于它；
6. 载荷 `typ` 是 `ID` / `Refresh` 的拒（ID Token 不是访问令牌）；
7. `exp` 必填且未过期；`nbf` / `iat` 不在未来（均含时钟偏移容忍）；
8. `sub` 必填。

任何一步失败，对外都是同一句 `PERMISSION_DENIED / 身份令牌无效`（HTTP 403）。
**不做区分披露**：把「过期」「签名错」「aud 不符」分开告诉调用方，等于白送一个
探测 issuer/audience 配置的接口。根因只进服务端日志，且**不含令牌任何片段**
（宪法 7 条，有测试断言整条错误链）。

唯一的例外是「压根没带 `Authorization` 头」——那是调用方自己知道的事实，
对外说 `缺少身份`，前端据此跳登录而不是显示一个「令牌无效」把人吓住。

## 七、Principal 字段映射

| Principal | 来源 |
|---|---|
| `ID` | `preferred_username`，缺失或含控制字符时回落 `sub` |
| `Subject` | `sub`（不可变标识） |
| `Type` | 恒为 `HUMAN` |
| `IdentityZone` | 恒为 `staff` |
| `Issuer` | `iss` |
| `ClientID` | `azp` |
| `AuthenticationLevel` | `amr` 含 `otp`/`mfa`/`hwk`… 或 `acr` 是 `>=2` 的数字 / 含 `mfa` → `mfa`，否则 `password` |
| `Environment` | **服务自身配置**，绝不从令牌读 |
| `Scopes` | `realm_access.roles` 经 RoleScopeMap 翻译 |

## 八、已知取舍

- **`aud` 之外还接受 `azp`。** CR-0001 的 `xingmang-admin-web` 是 public client，
  Keycloak 默认不会把 Client ID 写进 Access Token 的 `aud`（那里通常是 `account`），
  而是写进 `azp`——CR-0001「验证」第 4 条列出的正是 `azp = xingmang-admin-web`。
  只认 `aud` 的话，CR-0001 执行完当天所有令牌都会被拒，还得再开一张变更单去
  Keycloak 加 audience mapper。要收紧成「只认 aud」，请先加 mapper 再改代码；
- **`ID` 用用户名而不是 `sub`。** CR-0001 把 "Email as username" 关成 OFF，理由
  写的就是「用显式用户名，便于审计对齐」；审计事件里的 `principal_id` 是给人读的。
  **代价**：Keycloak 里改用户名会让审计链上同一个人前后叫两个名字。审计事件目前
  只记 `principal_id` 不记 `subject`——真要允许改名，得先把 `subject` 也落进审计
  （见 follow-up）；
- **失败一律 403 而不是 401。** 仓库的错误模型（`action.Code` → `StatusForCode`）
  里没有 401，现有的 `RequirePrincipal` 与前端都按 403 处理。为 OIDC 单独引入
  401 + `WWW-Authenticate` 是一次跨模块的契约变更，不该顺手做在这个任务里；
- **JWKS 用软 TTL。** 拉取失败时会继续用未超硬上限（默认 1 小时）的旧键，并记
  `oidc_jwks_refresh_failed_serving_stale` WARN。令牌寿命只有 5 分钟，风险窗口
  有界；反过来让管理后台在身份服务抖一下时整体掉线，代价更大；
- **构造 Resolver 不发起网络请求。** 发现与 JWKS 拉取都是惰性的。Keycloak 没起来
  时平台仍能启动并回答 `/healthz`，只是鉴权失败——比「Keycloak 没起来所以平台
  起不来」好得多。

## 九、相关文件

- `internal/platform/oidcauth/` —— 实现与测试（只用标准库，无新增依赖）
- `cmd/platform-api/auth.go` —— 装配开关
- `cmd/platform-api/config.go` —— `authConfigFromEnv`
- `docs/modules/httpapi/PERMISSIONS.md` —— 权限表（XM-0008 不改它）
- `web/apps/admin-web/src/auth/` —— 前端 OIDC（XM-AUTH1，见下）
- `deploy/docker/web-app-config.sh` —— web 容器启动时生成 `/app-config.js`

## 十、前端（admin-web，XM-AUTH1）

授权码 + PKCE（S256），public client，不引入依赖（Web Crypto + fetch）。
令牌只放 `sessionStorage`（关标签页即失效，不跨标签共享），任何日志与错误
信息都不含令牌片段。

### 10.1 运行时配置 `/app-config.js`

鉴权方式**不烧进构建产物**：同一个 `xingmang/web` 镜像同时服务 staging
（dev-header）与生产（oidc）。web 容器启动时 `deploy/docker/web-app-config.sh`
（挂在官方 nginx 镜像的 `/docker-entrypoint.d/`）按环境变量生成
`/usr/share/nginx/html/app-config.js`，`index.html` 在业务包之前用普通
`<script>` 同步加载它，文件里只有一句：

```js
window.__XM_CONFIG__ = {
  "authMode": "oidc",                                         // dev-header | oidc
  "oidcIssuer": "https://auth.solov.cc/realms/solov-staff",   // = 后端 XM_OIDC_ISSUER，逐字
  "oidcClientId": "xingmang-admin-web",                       // = 后端 XM_OIDC_AUDIENCE
  "oidcScopes": "openid profile email"                        // 可选，默认就是这个
};
```

| web 容器环境变量 | 字段 | 说明 |
|---|---|---|
| `XM_WEB_AUTH_MODE` | `authMode` | 默认 `dev-header`；其它值容器**拒绝启动** |
| `XM_WEB_OIDC_ISSUER` | `oidcIssuer` | oidc 时必填，缺了拒绝启动；必须与 `XM_OIDC_ISSUER` 逐字相同 |
| `XM_WEB_OIDC_CLIENT_ID` | `oidcClientId` | oidc 时必填；必须与 `XM_OIDC_AUDIENCE` 相同 |
| `XM_WEB_OIDC_SCOPES` | `oidcScopes` | 可选 |

浏览器端读取顺序（`src/auth/runtimeConfig.ts`，逐字段回落）：
`window.__XM_CONFIG__` → 构建期 `VITE_XM_AUTH_MODE` / `VITE_XM_OIDC_ISSUER` /
`VITE_XM_OIDC_CLIENT_ID` → `dev-header`。文件缺失或写坏**不会弄垮应用**，
只回落并把问题写在登录页上；Nginx 对 `/app-config.js` 发 `Cache-Control: no-store`，
切换后重启容器即生效，浏览器不会拿着旧模式发请求。

compose：`launch.yaml` 的 `web` 透传三项（默认 dev-header；issuer / client id
留空时回落到后端的 `XM_OIDC_ISSUER` / `XM_OIDC_AUDIENCE`）；`server-prod.yaml`
固定 `oidc` 且必填。**前后端必须同时切**：只切前端，后端不认 Bearer（403
缺少身份）；只切后端，前端还在发开发头。

本地 `vite dev` 联调真 Keycloak：`web/apps/admin-web/.env.local` 写
`VITE_XM_AUTH_MODE=oidc` + issuer + client id，后端同时 `XM_AUTH_MODE=oidc`；
回调地址是 `http://localhost:5173/auth/callback`。

### 10.2 登录 / 请求 / 登出

1. `/login`「使用 solov 账号登录」→ 拉 `<issuer>/.well-known/openid-configuration`
   （内存缓存；文档里的 `issuer` 必须与配置逐字相等，否则登录前就报错）→
   生成 `code_verifier` + `state` 存 `sessionStorage` → 跳 `authorization_endpoint`
   （`response_type=code`、`code_challenge_method=S256`、`scope=openid profile email`）；
2. `/auth/callback`：`state` 不匹配 / 带 `error` / 事务缺失 **一律拒绝**并把原因
   写在屏幕上，不回落到工作台；匹配则用 `code_verifier` 到 `token_endpoint` 换令牌
   （无 client secret），存 `{access_token, refresh_token?, id_token?, expires_at}`，
   回到发起登录时的 `next`（只接受站内路径，防开放重定向）；
3. 每个 API 请求 `Authorization: Bearer <access_token>`，三个 `X-Dev-*` 头不再发送。
   距过期不足 60s 先用 `refresh_token` 续期（并发请求单飞，只发一次）；续期失败
   清会话 → `/login?next=…&reason=session_expired`；
4. 服务端拒绝：HTTP 401，或 403 且文案**逐字**是第六节的「缺少身份」/「身份令牌无效」
   → 清会话 → `/login?reason=token_rejected`。「缺少权限 xxx」**不**跳登录——那是
   登录了但没权限，照常显示无权访问；
5. 退出：清会话 → `end_session_endpoint?post_logout_redirect_uri=https://<host>/login&client_id=…&id_token_hint=…`；
   发现文档拉不到时只回 `/login`。顶栏显示 `id_token` 的 `preferred_username`
   （与审计里的 `principal_id` 同源），其次 `name`。

### 10.3 Keycloak Client 必须满足的设置

CR-0001 §3 的表大部分已经对上，下面几项是前端流**硬依赖**的：

| 设置 | 值 | 为什么 |
|---|---|---|
| Client ID | `xingmang-admin-web` | = 后端 `XM_OIDC_AUDIENCE` = 前端 `oidcClientId` |
| Client authentication | **OFF**（public client） | 浏览器里没有地方放 secret |
| Standard flow | ON | 授权码流 |
| Direct access grants / Implicit / Service accounts | OFF | 不走密码直传，不走 implicit |
| PKCE Code Challenge Method | **S256** | 前端只发 S256；Keycloak 设了这一项就会拒绝没带 challenge 的请求 |
| Valid redirect URIs | `https://<host>/auth/callback`（每个部署一条；本地 `http://localhost:5173/auth/callback`） | CR-0001 的 `https://admin.solov.cc/*` 通配也覆盖它，但建议收紧到精确路径 |
| Valid post logout redirect URIs | `https://<host>/login` | 登出后的落点，精确匹配 |
| Web origins | `https://<host>` | 令牌端点是浏览器发起的**跨域 POST**，靠这一项放 CORS；不填就是换令牌时 CORS 失败 |
| Client scopes | `openid`、`profile`（`email` 可选） | `profile` 才有 `preferred_username` / `name` 进 `id_token` |
| Use refresh tokens | ON（默认） | 访问令牌只有 5 分钟，没有 refresh 就每 5 分钟跳一次登录 |

`<host>` 按部署替换：staging `admin-staging.solov.cc`、生产 `admin.solov.cc`。

### 10.4 相关文件

- `web/apps/admin-web/src/auth/runtimeConfig.ts` —— 运行时配置解析
- `web/apps/admin-web/src/auth/oidc.ts` —— 发现 / PKCE / 回调 / 续期 / 登出
- `web/apps/admin-web/src/auth/session.ts` —— 模式门面、Bearer 提供者、跳登录
- `web/apps/admin-web/src/auth/RequireAuth.tsx` —— 路由门禁
- `web/apps/admin-web/src/pages/LoginPage.tsx`、`AuthCallbackPage.tsx`
- `web/apps/admin-web/src/api/client.ts` —— `auth` 注入点（不传＝dev-header，行为不变）
- `deploy/docker/web-app-config.sh`、`deploy/docker/web.Dockerfile`、`deploy/nginx/launch.conf`

## 十一、local 模式（XM-LOGIN）

第三种 `PrincipalResolver`：`XM_AUTH_MODE=local` 时身份来自平台自带的账号库
（`core.staff_account`），不依赖任何外部身份提供方，也不像 `dev-header` 那样
是「请求头自称身份」——账号需要显式创建、口令走 argon2id 校验，因此**允许
在生产使用**（`server-prod.yaml` 已把生产默认改成它；仍可显式覆盖回
`oidc`）。实现在 `internal/platform/localauth/`。

### 11.1 端点

| 方法 | 路径 | 要求 Principal？ | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/login` | 否 | `{username,password}` → 未启用 TOTP 时成功 200 + `Set-Cookie: xm_session`；已启用 TOTP 时成功 200 + `{requires_totp:true,temp_token}`（不设 Cookie）；失败见 11.3 |
| POST | `/api/v1/auth/login/totp` | 否（见 11.7） | 登录第二步与步进刷新，见 11.7 |
| POST | `/api/v1/auth/logout` | 否（有 Cookie 就吊销，没有也 200） | 清 Cookie、吊销会话 |
| GET | `/api/v1/auth/me` | 是 | 与登录成功同形状，另含 TOTP 状态字段（见 11.7） |
| POST | `/api/v1/auth/password` | 是 | `{current_password,new_password}`，自助改密，成功后重签会话 |
| GET | `/api/v1/staff/accounts` | 是 + `staff.manage` | 账号清单（不含密码哈希，含 TOTP 状态） |
| POST | `/api/v1/auth/totp/enroll` | 是 | 委托 `staff.account.enroll_totp@1`，见 11.7 |
| POST | `/api/v1/auth/totp/confirm` | 是 | 委托 `staff.account.confirm_totp@1`，见 11.7 |

管理员重置他人 TOTP（`staff.account.reset_totp@1`）没有专用端点，走既有的
通用 `POST /api/v1/actions/staff.account.reset_totp/versions/1/execute`
入口，与 `staff.account.reset_password@1` 同一形状（CR-0006 技术规格 §5.1
只为 enroll/confirm 两个自助操作开了专用端点）。

登录/登出（以及 `/auth/login/totp` 的 temp_token 分支）**不**挂
`RequirePrincipal`（在建立/终止身份，不可能先要求一个还不存在的身份），
其余端点与 `/api/v1` 下所有其它端点一样必须先过 `RequirePrincipal`
（这里即 `localauth.Resolver.Resolve`）。

### 11.2 Cookie 与 CSRF

`xm_session`：`HttpOnly`、`SameSite=Lax`、`Path=/`、`Max-Age=43200`（12h）；
请求经 TLS（直连或 `X-Forwarded-Proto: https`）时加 `Secure`。库里只存原始
token 的 sha256 摘要，token 本身只在登录成功那一次的 `Set-Cookie` 里出现。

Cookie 鉴权天然带 CSRF 风险（浏览器会在跨站请求上自动附带 Cookie，OIDC 的
Bearer 头不会）：`Resolver.Resolve` 对非 `GET/HEAD/OPTIONS` 的请求强制要求
`X-Requested-With: xingmang`，缺失或值不对一律 `403 PERMISSION_DENIED`。
这条闸覆盖 `/api/v1` 下所有走 Cookie 鉴权的写请求，包括
`POST /api/v1/actions/{id}/versions/{v}/execute`——前端的 fetch/XHR 封装必须
在 local 模式下给非只读请求加这个头。

### 11.3 登录失败

| 情形 | 状态码 | code |
|---|---|---|
| 用户名不存在 / 密码错误 | 401 | `INVALID_CREDENTIALS` |
| 账号已锁定（连续 5 次失败） | 423 | `ACCOUNT_LOCKED` |
| 账号已停用 | 403 | `ACCOUNT_DISABLED` |

「用户名不存在」与「密码错误」返回**逐字相同**的状态码、`code`、`message`，
且服务端总会跑一次 argon2id 校验（用户名不存在时对着一个固定的哑哈希跑），
避免响应内容或响应时延变成一个用户名枚举接口。登录端点另有一个按客户端
IP 分桶的限流（默认 20/分钟、瞬时 10），与全局 XM-R011 限流分开配置。

### 11.4 账号管理（Action）

四个 L1、仅 HUMAN、要求 `staff.manage` 的 Action（`internal/platform/localauth/actions.go`）：

| Action ID | 参数 | 说明 |
|---|---|---|
| `staff.account.create@1` | `username`、`display_name`、`roles`（逗号分隔）、`initial_password`（可选） | 留空密码时生成一个随机初始密码，**只在本次结果里返回一次** |
| `staff.account.set_roles@1` | `username`、`roles` | 覆盖角色集合 |
| `staff.account.set_disabled@1` | `username`、`disabled` | 停用会连带吊销该账号全部会话 |
| `staff.account.reset_password@1` | `username`、`new_password`（可选） | 强制 `must_change_password=true` 并吊销全部会话；留空密码同样只回显一次 |
| `staff.account.enroll_totp@1` | 无 | 只作用于调用者自己；生成新密钥并挂到账号（未激活），见 11.7 |
| `staff.account.confirm_totp@1` | `code` | 只作用于调用者自己；校验后激活并一次性发恢复码，见 11.7 |
| `staff.account.reset_totp@1` | `username` | 管理员动作，撤销目标账号的 TOTP 启用状态，见 11.7 |

`roles` 必须是 `XM_OIDC_ROLE_SCOPES`（留空则 `oidcauth.DefaultRoleScopeMap`）
里已有的角色名——两种登录模式共用同一张「角色 → scope」翻译表，不需要为
本地账号单独维护一份，也不会出现「能分配一个谁也翻译不出 scope 的角色」。

`admin` 角色额外含 `staff.manage`、`credential.manage`、`connector.manage`
（见 `oidcauth.DefaultRoleScopeMap` 第 6 条）：这是对 XM-CRED0 原「不进
staff/admin」原则的一次显式收窄，理由是 local 模式的 bootstrap 管理员必须
一上线就能管账号/凭据/连接器，冷启动阶段没有「已登录的人」可以审批第二次
授权申请。

### 11.5 Bootstrap（第一个账号）

库里没有任何本地账号时，`staff.account.create` 会陷入「先有蛋还是先有鸡」
——它要求调用者已经持有 `staff.manage`。第一个账号只能用一条 Platform
Lifecycle Operation 建出来：`cmd/staff-bootstrap`（幂等；已存在则打印
`exists` 并 0 退出）：

```bash
docker compose -p xingmang-launch -f deploy/compose/launch.yaml \
  --profile tools run --rm staff-bootstrap
```

环境变量 `XM_STAFF_BOOTSTRAP_USERNAME`（必填）、`XM_STAFF_BOOTSTRAP_PASSWORD`
（留空则生成随机密码并打印到 stdout，仅此一次），角色固定
`admin,credential-admin,staff`。数据库连接复用 `DATABASE_URL` /
`DATABASE_PASSWORD_REF` 的 CredentialRef 纪律，与 `runway-threshold-bootstrap`
同构。

### 11.7 TOTP 二因素（XM-AUTH-TOTP0，CR-0006 第一阶段）

登录改为两步：密码校验通过后，若账号已激活 TOTP（`totp_enrolled_at` 非空），
不直接签发会话，转而在 `core.staff_login_challenge` 建一条 5 分钟有效的
"登录挑战"，返回 `{requires_totp:true,temp_token}`；前端拿 `temp_token`
配合动态码或恢复码调用 `POST /api/v1/auth/login/totp` 完成登录，成功后
`core.staff_session` 记录 `amr=["pwd","otp"]`、`mfa_at`。未激活 TOTP 的账号
（含"待启用但还没 confirm"）登录路径不变，一步签发会话。

**同一个端点服务两种场景**（请求体是否带 `temp_token` 区分）：

| 请求体 | 场景 | 鉴权 |
|---|---|---|
| `{temp_token,code}` 或 `{temp_token,recovery_code}` | 完成登录第二步 | 无（temp_token 本身就是凭据） |
| `{code}` 或 `{recovery_code}`（无 temp_token） | 步进刷新：已登录会话重新证明"刚过了一次 TOTP" | 需要有效 `xm_session` Cookie + `X-Requested-With` 头 |

步进刷新调用 `Store.TouchSessionMFA`，只更新**当前**会话的 `mfa_at`/`amr`，
不换 `session id`、不重新设 Cookie。这是为团队内后续切片（XM-INVCON1 的
断言签发端点，签发前要求 `mfa_at` 在 `StepUpMaxAge` 内）准备的能力；本片
本身没有任何端点消费它。判定用 `localauth.RequireFreshOTP(p, maxAge, now)`
——读 `principal.Principal.MFAAt`（`localauth.Resolver.Resolve` 从会话的
`mfa_at` 列填入），不在解析阶段折叠成布尔值,由调用方按自己的新鲜度阈值判断。

**启用 / 确认 / 重置**（RFC 6238：30 秒步长、6 位数字、SHA1、±1 步容忍窗口）：

1. `POST /api/v1/auth/totp/enroll` → 生成新密钥，经**既有**
   `credential.secret.upsert` 写路径写入 `secret://staff-totp/<account_id>`
   （不新增写入口，CR-0006 正文原话），返回 `otpauth://` URI 与 base32
   手动录入串（密钥明文只出现这一次）；
2. `POST /api/v1/auth/totp/confirm {code}` → 校验通过则激活（写
   `totp_enrolled_at`、清 `must_enroll_totp`）并一次性生成 10 个恢复码（仅
   哈希落库，明文只在这次响应里）；
3. `staff.account.reset_totp@1`（管理员，`staff.manage`，走通用执行入口，
   见上）→ 撤销目标账号的启用状态、删除其全部恢复码、吊销其全部会话、经
   既有 `credential.secret.revoke` 吊销密钥文件，`must_enroll_totp` 重新
   置真。

**强制启用**：`core.staff_account.must_enroll_totp` 语义与既有
`must_change_password` 完全对称——账号被授予需要 TOTP 的角色（翻译出的
scope 含 `staff.manage` 或 `finance.read`，见 `localauth.rolesRequireTOTP`）
时置真，`confirm_totp` 成功后置假。**这是前端 `RequireAuth` 的软重定向**
（与 `must_change_password` 同一模式，不是后端 API 级别的强制拦截）：
`GET /api/v1/auth/me`/登录响应把这个字段带给前端，前端据此把人无论原本要
去哪都先带到启用页；后端不因为这个字段拒绝其它 API 调用。

**管理员来源 IP 名单**（`XM_CONSOLE_ADMIN_IP_ALLOWLIST`，逗号分隔 CIDR，空＝
不启用）：只对落入 TOTP 管辖范围的账号生效（`must_enroll_totp` 或已激活
TOTP 任一为真），在密码校验通过之后、签发会话或完成步进之前校验来源 IP
（`clientIP` 解析，与本文件其余 XFF 处理同一条纪律），不过返回 403
`ADMIN_NETWORK_DENIED` 并写审计（`CompensationResult=ip_denied`）。这是纵深
防御，与 CR-0006 后续切片（XM-INVCON1）的断言签发端点各自独立校验——两侧
应配置**同一份** CIDR 列表，但目前没有自动同步机制，运维需手工保持一致
（与 `XM_INVOICE_CONSOLE_ORIGIN` 一类"两个仓库各存一份、必须一致"的既有
操作代价同类）。

**错误码**（沿用本文件既有的 `httpapi.ErrorResponse` 信封）：

| HTTP | code | 触发条件 |
|---|---|---|
| 401 | `CHALLENGE_INVALID` | temp_token 不存在/已过期/已消费 |
| 401 | `INVALID_CREDENTIALS` | temp_token（或步进的会话）有效，但 code/recovery_code 不对 |
| 423 | `ACCOUNT_LOCKED` | 二步验证连续失败达到 `lockThreshold`（复用密码登录同一套账号锁定） |
| 403 | `ADMIN_NETWORK_DENIED` | 来源 IP 不在 `XM_CONSOLE_ADMIN_IP_ALLOWLIST` |
| 429 | `RATE_LIMITED` | 按目标账号用户名分桶的验证码尝试限流（10/分钟、突发 5，独立于登录的按 IP 限流） |
| 409 | `CONFLICT` | `enroll_totp`/`confirm_totp` 时账号已激活 TOTP |
| 412 | `PRECONDITION_FAILED` | `confirm_totp` 时账号尚未开始 enroll |

**恢复码**：确认启用时一次性生成 10 个，`core.staff_totp_recovery_code`
仅存 sha256 摘要，单次可用（`used_at` 非空即失效）。登录第二步与步进均可用
恢复码代替动态码（`recovery_code` 字段，与 `code` 二选一，不能同传）。
`GET /api/v1/auth/me` 的 `recovery_codes_remaining` 字段供账号安全页提醒
"还剩几张"，不返回任何码本身；用尽后没有自动补发路径——管理员走
`reset_totp` 让账号重新走一遍启用即可拿到一套新恢复码。

相关文件：`internal/platform/localauth/totp.go`（RFC 6238 核心，含对
RFC 6238 附录 B 官方测试向量的单测）、`adminip.go`（CIDR 名单）、
`actions.go`（三个 Action）、`handlers.go`（`Login`/`LoginTOTP`/
`EnrollTOTP`/`ConfirmTOTP`/`ResetTOTP`）、`resolver.go`（`RequireFreshOTP`）、
`db/migrations/000024_staff_totp.up.sql`、
`contracts/actions/staff.account.{enroll,confirm,reset}_totp.v1.json`。

### 11.8 已知缺口

- **前端还没有本地登录页**：`server-prod.yaml` 已把 `platform-api` 的默认
  `XM_AUTH_MODE` 改成 `local`，但 `web` 容器的 `XM_WEB_AUTH_MODE` 仍固定
  `oidc`（XM-AUTH1 的产物），没有用户名/密码表单，也不处理 `xm_session`
  Cookie 或 `X-Requested-With` 头。默认组合会立即 403 缺少身份，直到前端
  补上本地登录 UI——这是本次（XM-LOGIN 后端切片）刻意留给下一个切片的
  工作，见交接文档的 follow-up。
- `GET /api/v1/staff/accounts` 之外没有单账号详情端点；清单已足够管理页
  渲染一张表，需要时可以再加。
