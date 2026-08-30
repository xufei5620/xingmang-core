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

> 前端的 OIDC 流（PKCE、跳转、令牌刷新）是**后续任务**，XM-0008 不含。
> 在那之前，用 `Authorization: Bearer <token>` 手工验证后端。

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
