# CR-0006：开票管理员改用控制台代认证，Keycloak 退役

> 状态：**已批准的产品需求，设计阶段（本文档）**；分两阶段实施——第一阶段（控制台断言登录与开票系统并行接受，Keycloak 保持在线）与第二阶段（Keycloak 实际停用）分属不同切片，第二阶段需产品负责人另行确认放行，不随本 CR 自动生效。
> 依据：产品负责人 2026-09-02 15:35 口头裁定（"开票系统去掉 Keycloak；方案 = 控制台代认证：控制台唯一身份 + TOTP + 签名断言 → 开票 API；Keycloak 退役，门禁 9→8 镜像"），已记入 `docs/handoffs/ACCEPTANCE-LOG.md` 当日条目；产品负责人原话理由是 Keycloak "太麻烦"（跳转、改密、绑 OTP、短令牌，对内部后台过重——与 CR-0004 废弃员工侧 Keycloak 的理由一致）。

## 发起方
产品负责人；由验收线整理成文（平台线与开票线当前均由验收线执行）。

## 接收方
平台线（xingmang-platform 控制台）与开票线（invoice-system）。

## 目标资源
- 星芒控制台的员工登录与会话（`internal/platform/localauth`）：新增 TOTP 二因素、新增断言签发端点；
- 开票系统的管理员认证边界（`backend/internal/auth`、`backend/internal/httpapi/production_auth.go`）：新增断言兑换端点，OIDC/Keycloak 登录路径进入可退役状态但本 CR 不删除代码；
- Keycloak 部署（`auth.solov.cc`，`keycloak` + `keycloak-postgres` 两个容器，`deploy/docker-compose.idp.yml`）及其在发布门禁（`invoice-keycloak` 镜像）、备份（`backup.sh` 的 `BACKUP_LOCAL_KEYCLOAK` 分支）、恢复演练（`restore-drill.sh` 的 `KEYCLOAK_BACKUP` 分支）中的位置；
- `invoice_users` 表中当前唯一一条 OIDC 关联的管理员身份行（`(oidc_issuer, oidc_subject)` 唯一约束）。

## 背景事实
1. CR-0003/CR-0004 已把开票**业务用户**登录改为各平台自己的密码（Sub2API/NewAPI），开票不再为普通用户存或转发密码；开票管理员登录是 Keycloak 在开票系统里**最后一个**用途。
2. CR-0005 第一阶段（XM-INVCON0 平台线、XM-INV-ADMIN-EMBED 开票线，均已合入生产）把开票管理端以 iframe 嵌入星芒控制台三个入口（Sub2API/NewAPI 的"支付与财务→开票"、治理的"开票集成"），但鉴权仍是**弹出顶层窗口走 Keycloak OIDC + MFA 步进**，且这一弹窗握手是 XM-INV-ADMIN-EMBED 新写的机制、并非复用已有的嵌入消息规范（该片交接文档"Risks"第 1 条已如实记录）。本 CR 正是要去掉这个弹窗与它背后的 Keycloak。
3. 开票管理员当前身份：`invoice_users` 表按 `(oidc_issuer, oidc_subject)` 唯一键，管理员那一行的 `oidc_issuer`/`oidc_subject` 是 Keycloak `solov` realm 的 issuer 与用户 subject（验收线此前一次修复工具操作记录的值：`invoice_users.id = 99ed401b-e78a-4883-b9bf-f4cb4ba1cf17`，对应 Keycloak 账号 `1187166666@qq.com`，subject 以 `cd680af8` 开头——**执行 f 条数据迁移前必须重新查询数据库现状核对该值**，本文档不作为其权威来源）。
4. 开票侧管理员鉴权链路（`backend/internal/auth`）现状：`AdminPolicy{Role, RequiredACR, RequiredAMR, StepUpMaxAge}` 从环境变量 `OIDC_ADMIN_ROLE`/`OIDC_REQUIRED_ADMIN_ACR`/`OIDC_REQUIRED_ADMIN_AMR`（默认 `otp`）读取，`AuthorizeSession` 只校验会话里已经落库的 `roles`/`acr`/`amr`/`mfa_at`——这四列在 `auth_sessions` 表里是与 OIDC 无关的普通列（`TEXT`/`TEXT[]`/`TIMESTAMPTZ`），签发它们的方式（OIDC 回调或本 CR 的断言兑换）与校验逻辑天然解耦，这是本 CR 能做到"零改 `AdminPolicy`/`ProductionAuth.Require` 判定逻辑，只换签发来源"的关键事实依据。
5. 开票侧管理员 IP 白名单（`AdminIPAllowlist`/`BreakGlassCIDRs`，`internal/adminsettings`、`server.go` 的 `adminIPAllowed`）独立于登录方式生效——它检查的是浏览器请求 `invoice.solov.cc` 时的来源 IP，不关心会话是怎么建立的，因此"复用"在本 CR 中首先意味着**这条检查原样保留、不重复实现**；第二层含义（控制台侧是否也做一次预检查）见下文"变更内容"。
6. 开票侧发布门禁（`scripts/release-image-gate-lib.ps1` 的 `Get-CommonReleaseImageTag`）今天固定 8 个基础镜像仓库 + `IdPMode -eq 'keycloak'` 时追加 `invoice-keycloak`，构成"九镜像"；`docs/IMAGE-SCAN-REVIEW.md`/`RELEASE-READINESS.md` 均以"九个镜像"为当前基线表述。`IMAGE-SCAN-REVIEW.md` 记录 Keycloak 26.7.2 有一条 vendor-rejected-CVE 例外，**时限到 2026-09-30T00:00:00Z 过期**——过期后若仍在用 Keycloak 镜像，发布门禁会直接拦截，这是本 CR 时间表上的一个真实外部约束，但不是发起本 CR 的理由（理由是产品负责人的"太麻烦"裁定）。
7. Foundation-B（Action 的 L2～L4 高级控制：人工审批、Step-up MFA、幂等、冷却、Kill Switch）尚未建成，`internal/platform/approval/` 为空（同 CR-0005 background 第 2 条事实）。本 CR 涉及的全部新写操作经审查**均为 L1 或 Platform Lifecycle Operation**（见"不需要 Foundation-B 的理由"一节），不依赖 Foundation-B。

## 结论
星芒控制台成为开票管理员的**唯一身份来源**：控制台在员工已有 12 小时会话之上加一层 TOTP 二因素与就近的步进有效期，操作员打开某个开票嵌入页时，控制台签发一枚 ≤5 分钟有效的签名断言，经 iframe 内 `postMessage` 转交给开票前端，由开票前端调用开票侧**新增**的兑换端点换成开票自己的会话 Cookie（`__Host-invoice_session`）；开票侧 IP 白名单、双人复核、审批、审计**不变**。Keycloak 分两阶段退役：第一阶段断言登录与 Keycloak OIDC **并存**一整个发布周期（本 CR 授权范围）；第二阶段（另行确认）才真正停用 Keycloak 容器、把发布门禁镜像数改为八个。

## 不需要 Foundation-B 的理由
逐项核对本 CR 引入的全部新写操作：

| 新写操作 | 归类 | 理由 |
|---|---|---|
| `staff.account.enroll_totp`/`confirm_totp`/`reset_totp`（控制台） | Action，L1 | 与既有 `staff.account.*` 四个 Action 同级：员工账号自身安全配置的显式变更，仅 HUMAN，风险面与"重置密码"（已是 L1）相当 |
| TOTP 密钥经 `credential.secret.upsert` 写入 `secret://staff-totp/<account_id>` | 复用既有 L1 Action | 不新增写入口，只是新增一类 `credential_ref` 前缀 |
| 控制台签发断言（`POST /api/v1/auth/console-assertion`） | 不是 Action | 不改变任何平台业务/配置状态（不落库新行、不改现有行），是"基于当前会话签一个短时证明"，性质等价于登录/签发会话——而登录、改密、会话签发在本仓库里从来不经 `action.Registry`（`localauth.Login`/`ChangePassword` 直接调用仓储），本 CR 沿用同一先例 |
| 开票侧兑换断言（`POST /api/v1/auth/console-assertion`） | 开票系统自己的会话签发路径 | 不属于 xingmang-platform 的 Action 框架管辖范围，与今天的 OIDC 回调（`callback` handler）同级 |
| `invoice_users` 一行的 `oidc_issuer`/`oidc_subject` 改写、Keycloak 容器停用、备份脚本分支停用 | Platform Lifecycle Operation（ADR-003） | 与迁移/Bootstrap/Keycloak Provisioning/备份恢复同类：版本化脚本 + 变更单 + 人工批准 + 独立审计，明确排除在 Action API 之外 |

**结论：CR-0006 不需要 Foundation-B 的任何能力（不需要人工审批工作流、不需要 Step-up MFA 内核、不需要幂等/冷却/Kill Switch）。** 它自己实现了一套与 Foundation-B 平行但范围窄得多的"新鲜度证明"（TOTP 时效 + 断言时效），专用于"证明一次浏览器会话最近做过二次验证"这一件事，不是 Foundation-B 的替代品，也不构成先例去豁免其他 L2+ 场景。

## 两侧改动清单

### 断言契约（两侧共同遵守，详见配套技术规格）
JWS compact 序列化，`alg=EdDSA`（Ed25519）。载荷字段：

| claim | 值 | 说明 |
|---|---|---|
| `iss` | `https://console.solov.cc` | 精确 HTTPS 来源；与开票 `IdentityStore.ResolveOrCreate` 现有的 `validateExactHTTPSURL` 校验直接兼容 |
| `aud` | `xingmang-console-assertion-v1` | 固定受众串，防止断言被挪作他用 |
| `sub` | 操作员的 `core.staff_account.id`（UUID） | 稳定身份键，不随改名变化；对应开票侧 `Principal.Subject` |
| `username` | 账号用户名 | 展示用，非身份键（同 `platform_login.go` 里 `Username` 与 `PlatformUserID` 的关系） |
| `roles` | 如 `["admin"]` | 直传 `core.staff_account.roles`，供开票侧 `AdminPolicy.Role` 判定 |
| `acr` | `xingmang-console-totp-v1` | 固定串，取代今天 Keycloak 签发的 ACR 值 |
| `amr` | `["pwd","otp"]` | `pwd`=已过控制台密码校验；`otp`=最近一次 TOTP/恢复码校验仍在有效期内；与开票侧 `RequiredAMR` 默认值 `["otp"]` 兼容，无需改该配置的默认值 |
| `scope` | `sub2api` \| `newapi` \| `global` | 记录本次断言对应哪个嵌入入口，**仅审计与展示用途，不是新的授权边界**——与 CR-0005 "员工视角可跨平台，视图约束而非安全边界"的既有裁定一致，明确排除"按 scope 限制开票后端可见数据"这一更大改动 |
| `nonce` | 32 字节随机值，base64url | 一次性重放保护标识，兼作 JWT `jti` |
| `iat`/`exp` | 签发时刻 / `iat` + ≤5 分钟 | 严格上限 5 分钟，服务端拒绝任何声称更长有效期的断言 |

签名密钥托管：私钥只存在于控制台服务端进程可达的位置，经与第三方连接器凭据同一条纪律的间接引用管理（`secret://console-assertion/signing-key-<key_id>`），**不进浏览器、不进日志**。公钥分发**不采用**开票实时拉取控制台 JWKS 端点的方案，而是复用本仓库 `internal/platform/jobs/fleet_keyring.go` 已经验证过的模式——评审过的公钥清单文件（`purpose`/`protocol` 域分离、`ValidFrom`/`ValidUntil`/`RevokedAt` 三态）随发布同步进开票仓库，理由与取舍见下"关于密钥分发方式，请产品负责人确认"。轮换 = 新增一条未来生效的公钥记录，旧记录到期后设 `RevokedAt`，两端各自一次评审发布即可完成，不需要运行时协调。

### 平台线（新切片 XM-AUTH-TOTP0 + XM-INVCON1）
a. **TOTP 二因素**（`internal/platform/localauth` 扩展）：`core.staff_account` 新增 `totp_secret_ref`（可空，`secret://staff-totp/<id>`）、`totp_enrolled_at`（可空）、`must_enroll_totp`（布尔，语义与既有 `must_change_password` 完全对称：账号被授予需要 TOTP 的角色时置真）；`core.staff_session` 新增 `mfa_at`（可空），与开票 `auth_sessions.mfa_at` 同名同义。登录分两步：密码校验通过后，若账号 `must_enroll_totp` 或已启用 TOTP，返回"需要二因素"的中间态（形态直接对照开票侧 `platform_login.go` 现成的 `PlatformLoginResult.RequiresTwoFA`/`TempToken`/`VerifyTwoFA` 三件套，是本仓库内已验证过的同一形状，非新发明）；新增 `POST /api/v1/auth/login/totp` 完成第二步。恢复码：确认启用时一次性生成 10 个，仅哈希落库，一次性展示给操作员，单次可用。**强制范围仅限持有 `staff.manage` 或后续开票断言所需角色的账号**——与产品负责人裁定"为管理员加 TOTP"逐字对应，不扩大到全体员工账号（若后续要求全员强制，需另立变更）。
b. **断言签发端点** `POST /api/v1/auth/console-assertion`（新包 `internal/platform/consoleassertion`，与 `localauth` 分开，职责单一）：要求已有 `xm_session`（复用现有 CSRF 头校验）；额外要求 `principal.Scopes` 含 `finance.read`（**这一步是对今天状态的实质加强**——今天任何能点开弹窗走完 Keycloak 登录的人都能拿到开票管理员会话，与是否持有 `finance.read` 无关；本 CR 之后，控制台根本不会为不持有该 scope 的账号签发断言）；额外要求 `mfa_at` 在 `StepUpMaxAge`（建议 10 分钟，与开票侧 `AdminPolicy.StepUpMaxAge` 现有默认值一致）内，否则返回 `ADMIN_STEP_UP_REQUIRED`，控制台前端就地（同源，无需跳转）提示重新输入 TOTP。请求体带目标 `scope`（`sub2api`/`newapi`/`global`），来自 `EmbeddedConsoleFrame` 已知的嵌入上下文。
c. **管理员 IP 名单同步**：新增只读配置 `XM_CONSOLE_ADMIN_IP_ALLOWLIST`，值应与开票侧 `AdminCIDRs`（`PUT /api/v1/admin/settings/admin-access` 维护）保持一致；断言签发端点额外按此名单预检查请求方 IP，未过则拒签（`ADMIN_NETWORK_DENIED`）。这是纵深防御，不是替代——开票侧 `adminIPAllowed()` 检查浏览器直接命中 `invoice.solov.cc` 时的来源 IP，两条检查各自独立生效，即便某一层配置漂移，另一层仍然把关。**两处配置需要运维保持一致，这与 `XM_INVOICE_CONSOLE_ORIGIN` / 开票侧 `frame-ancestors` 今天已经接受的"两个仓库各存一份、必须一致"操作代价同类**（XM-INV-ADMIN-EMBED 交接文档明确记录过这条风险），不是本 CR 新引入的脆弱性类别；建议新增一条类似 `verify-web-security-headers.ps1` 的轻量校验脚本，发布前断言两侧名单一致。
d. `EmbeddedConsoleFrame`（`ui-admin`）扩展：加载完成后调用 c 端点取得断言，通过 `postMessage({type:"xm-embed", version:1, kind:"admin-assertion", assertion:"<jwt>"}, invoiceConsoleOrigin)` 发给 iframe——**沿用 XM-INVCON0 已建立的 `xm-embed` 版本化信封，新增一个 `kind` 值即可，不需要升版本**（该信封设计时已预留这类演进空间，见 XM-INVCON0 交接文档）；方向与已有的高度同步消息（iframe→console）相反（console→iframe），是新的一条通道，两端都需要新代码，不是对现有代码的简单复用。

### 开票线（新切片 XM-INV-CONSOLE-ASSERT + XM-INV-KEYCLOAK-RETIRE）
e. **断言兑换端点** `POST /api/v1/auth/console-assertion`（`ProductionAuth.Register` 新增一条路由，与今天的 `/callback` 同级）：无需预先存在会话；校验 `Origin` 头精确等于 `https://console.solov.cc`（同 `CSRFPolicy.ValidateMutation` 现有的"恰好一个 Origin 头"纪律）；校验签名、`iss`/`aud`/`exp`/`nonce`；`nonce` 经新表 `console_assertion_nonces(nonce_hash CHAR(64) PRIMARY KEY, expires_at, consumed_at)` 的 `INSERT ... ON CONFLICT DO NOTHING` 原子判重（与 `oidc_backchannel_logout_events` 现有的 `UNIQUE(issuer_hash, jti_hash)` 判重同一手法）；映射为 `Principal{Issuer, Subject, Roles, ACR, AMR, AuthTime, Platform:"", PlatformUserID:""}` 后，走**现成、不改一行**的 `identity.ResolveOrCreate` → `provisionPlatformOrOIDCUser`（`principal.Platform == ""` 分支，与今天 OIDC 登录走的是同一分支）→ `Sessions.Issue`。对外失败只返回一种通用状态码（见配套技术规格的错误码表），不区分"签名错/过期/重放/claim 不符"——这是本仓库贯穿 OIDC 校验、会话查找、登录校验的既定纪律（"分开告诉调用方为什么不合格等于白送一个探测接口"），本 CR 延续，不新开先例。
f. **数据迁移（Platform Lifecycle Operation，非本 CR 自动执行，需切片内单独批准）**：在断言模式实际启用前，对当前唯一一条开票管理员身份（`invoice_users.id = 99ed401b-e78a-4883-b9bf-f4cb4ba1cf17`）执行一次评审过的一次性 `UPDATE`，把 `oidc_issuer`/`oidc_subject` 从 Keycloak 的值改写为断言的 `iss`/`sub`（控制台来源 + 该管理员对应的 `core.staff_account.id`），使第一次断言登录落在**同一条**已有历史记录上，而不是 `ResolveOrCreate` 因键不匹配另建一条新身份、把既有工单/操作历史挂在孤儿账号上。执行走既有"dry-run→apply 修复工具"生命周期（`--database-url-file`/`--field-keyring-file`，同 RC69/RC70 使用的机制），带独立审计与变更单，回滚 = 把两列改回原 Keycloak 值。
g. **Keycloak 退役分两阶段**：
   - 第一阶段（本 CR 授权，切片 XM-INV-CONSOLE-ASSERT）：新增断言兑换端点，与既有 OIDC 登录/步进路径**并存**；`OIDC_ADMIN_LOGIN_ENABLED`（新配置项，默认 `true`）控制 OIDC 路径是否注册；本阶段维持 `true`，Keycloak 容器继续运行，发布门禁维持 `IdPMode: 'keycloak'`、九镜像不变。
   - 第二阶段（另行确认，切片 XM-INV-KEYCLOAK-RETIRE，前提是断言模式已在生产完整跑过一个发布周期且金丝雀干净）：`OIDC_ADMIN_LOGIN_ENABLED` 置 `false`；`docker-compose.idp.yml` 从生产 compose 组合里摘除（容器**停止但不删除**，卷保留）；`backup.sh` 的 `BACKUP_LOCAL_KEYCLOAK`、`restore-drill.sh` 的 `KEYCLOAK_BACKUP` 两个分支置为不触发（脚本代码保留，不删除，为回滚保留能力）；`scripts/release-image-gate-lib.ps1` 的 `Get-CommonReleaseImageTag`/`IdPMode` 参数**新增**第四个取值 `console-assertion`（不复用/不重新定义现有 `none` 的语义——`none` 今天在代码里硬编码为"未包含 IdP，不可批准生产"，重新定义它会让其他潜在调用方读到不同含义；新增一个显式命名的取值更安全、审计路径更清楚），该模式下镜像清单为八个（去掉 `invoice-keycloak`），且要求一条新的"生产断言签发/兑换/防重放"金丝雀证据，替代今天"OIDC/MFA/RP-logout/back-channel-logout"金丝雀的位置；`docs/IMAGE-SCAN-REVIEW.md`/`RELEASE-READINESS.md` 的"九个镜像"表述改为"八个镜像"，移除即将于 2026-09-30 到期的 Keycloak vendor-rejected-CVE 例外条目（标记为历史，不删除，供追溯）。
   - `oidc_authorization_flows`/`oidc_backchannel_logout_events`（含其 `internal/oidcretention` 保留期任务）**本 CR 不删除表结构与代码**：第二阶段生效后这两张表自然不再产生新行，靠既有保留期任务自然清空；物理 `DROP TABLE`/移除相关代码是更晚一次独立变更（至少再晚一个发布周期），不在 XM-INV-KEYCLOAK-RETIRE 范围内。

## 明确不变
- 开票系统的审批、双人复核（支付候选）、退款结案、系统设置写操作规则**不变**；本 CR 完全不触碰 `internal/httpapi/operations.go` 的业务判定逻辑。
- 审计**不变且加强**：开票侧每次会话签发已有的 `SecurityAuditSink`/`insertSecurityAudit` 事件（`auth.identity.create` 等）对断言签发的会话同样记录；断言签发本身在控制台侧新增一条审计事件（操作员、目标 scope、断言 `nonce`、签发时刻），两侧审计可交叉核对签发数与兑换数，作为异常检测手段。
- ADR-018 的四道只读闸**不变**：本 CR 不新增平台到开票的数据库/Bridge 通道，断言兑换端点是开票系统自己新增的一个 HTTP 入口，不经 ADR-018 覆盖的连接器路径。
- CR-0003 的员工/用户分域纪律、"员工视角可跨平台"的既有裁定**不变**：断言的 `scope` claim 是审计字段，不是新的服务端授权边界（见上文断言契约表）。
- 开票侧 `AdminPolicy`/`ProductionAuth.Require`/`CSRFPolicy` 的判定代码**不需要改一行**——只需把 `OIDC_REQUIRED_ADMIN_ACR` 配置成新的 `xingmang-console-totp-v1` 常量；这是本设计刻意追求的最小改动面。
- 开票管理端 Web 独立入口（`https://invoice.solov.cc/admin`）继续可用，作为回退路径（同 CR-0005 f 条）；本 CR 之后它仍走 OIDC（第一阶段）或在第二阶段跟随 `OIDC_ADMIN_LOGIN_ENABLED` 一起变化——**待确认项**：第二阶段后独立入口是否也改走"控制台断言 + 跳转"或直接下线，本 CR 不预先裁定，留给 XM-INV-KEYCLOAK-RETIRE 切片单独说明。
- 开票管理员账号本身继续是"开票系统的管理员"这个角色概念（CR-0005 已确立的既定安排），本 CR 只改它的**登录方式**，不改它是谁、能做什么。

## 关于密钥分发方式，请产品负责人确认
本设计推荐"评审过的公钥清单文件随发布同步"而非"开票实时拉取控制台的类 JWKS 端点"，理由是：两个系统由同一团队维护、发布节奏已通过 `docs/change-requests/` 协调、且本仓库已有 `fleet_keyring.go` 这一先例证明该模式跑得通；代价是每次轮换密钥都需要两侧各一次评审 + 发布，而非运维改一个配置就生效。若认为轮换的运维成本比"新增一个实时依赖端点"更不可接受，请指出，技术规格文档会相应改写为 JWKS 方案。

## 影响面
- 系统：xingmang-platform 新增 `internal/platform/consoleassertion` 包与 `staff_account`/`staff_session` 两列扩展；`ui-admin` 的 `EmbeddedConsoleFrame`；invoice-system 新增兑换端点、`console_assertion_nonces` 表、`OIDC_ADMIN_LOGIN_ENABLED` 配置；两侧发布门禁脚本（仅第二阶段）。
- 用户：仅开票管理员一人（当前唯一持有该角色的账号）与未来可能新增的开票管理员。业务用户（Sub2API/NewAPI 登录）完全不受影响——他们从未经过 Keycloak（CR-0004 已实施）。
- 数据：`invoice_users` 一行的一次性身份改写（见 f 条）；两张新表（`console_assertion_nonces`、若采用独立恢复码表则再加一张）；`oidc_*` 相关表在第二阶段后自然停止增长，本 CR 不做物理删除。

## 验证
1. 控制台完成密码 + TOTP 登录 → 打开 Sub2API "支付与财务→开票" 页签 → 无弹窗、无跳转，iframe 内直接呈现已登录的开票管理界面；NewAPI、治理"开票集成"同理各验证一次。
2. 断言重放：截获一次成功兑换用过的断言，原样再发一次兑换请求，必须被拒绝且不建立新会话。
3. 断言超时：构造 `exp` 超过签发时刻 5 分钟的断言，必须被拒绝（无论签名是否合法）。
4. 越权断言：用不持有 `finance.read` 的控制台账号尝试触发签发端点，必须在控制台侧就被拒绝（不产生任何断言）。
5. TOTP 步进：TOTP 校验超过 `StepUpMaxAge` 后再次请求签发，必须收到 `ADMIN_STEP_UP_REQUIRED` 并在控制台内就地完成重新验证，不经过任何弹窗或跳转。
6. IP 名单：从不在 `AdminCIDRs`/`XM_CONSOLE_ADMIN_IP_ALLOWLIST` 内的地址分别尝试签发与兑换，两处各自独立拒绝。
7. 身份连续性：数据迁移执行后，用新断言登录产生的操作，其审计与历史工单仍归属同一个 `invoice_users.id`（迁移前后同一条）。
8. 并存期回归：Keycloak 未停用期间，独立入口 `https://invoice.solov.cc/admin` 的 OIDC 登录路径继续可用且不受断言路径影响。
9. 第二阶段：`scripts/test-release-image-gate.ps1` 新增 `console-assertion` 模式用例，断言镜像清单为八个且不含 `invoice-keycloak`；`restore-drill.sh` 在关闭 `KEYCLOAK_BACKUP` 后仍完整通过，证明非 Keycloak 备份集合本身是自足的。

## 回滚
- 第一阶段（断言与 OIDC 并存期）：断言路径本身有独立开关（若签发/兑换任一侧出现问题，直接停用 `internal/platform/consoleassertion` 的路由注册，或开票侧撤回兑换端点路由），操作员回退到弹窗 OIDC 登录，Keycloak 全程未受影响，风险最低。
- 数据迁移（f 条）：按变更单记录的原始值把 `invoice_users` 该行的 `oidc_issuer`/`oidc_subject` 改回 Keycloak 值。
- 第二阶段（`OIDC_ADMIN_LOGIN_ENABLED=false` 之后）：置回 `true`，重启已停止但未删除的 `keycloak`/`keycloak-postgres` 容器（卷保留，数据未丢失），发布门禁 `IdPMode` 改回 `keycloak`；这正是"容器停用一个发布周期不删除"设计的目的——回滚不需要重新走一遍 Provisioning。
- 第二阶段发布门禁改动（`console-assertion` `IdPMode` 值、八镜像清单）如需回滚，直接改回原有 `keycloak`/`none`/`external-managed` 三值枚举与九镜像清单，`console-assertion` 分支代码保留但不再被生产配置选中。

## 路线
1. **XM-AUTH-TOTP0**（平台线）——控制台 TOTP、恢复码、`core.staff_session.mfa_at`、IP 名单配置项。无前置依赖，可立即开工。
2. **XM-INVCON1**（平台线，依赖 1）——断言签发端点、签名密钥托管与轮换、`EmbeddedConsoleFrame` 的断言 `postMessage` 扩展。
3. **XM-INV-CONSOLE-ASSERT**（开票线，与 2 需共享断言契约草案、建议并行开发并在合入前对齐）——兑换端点、`console_assertion_nonces`、iframe 侧入站消息监听、`invoice_users` 数据迁移工具（迁移本身另需批准执行）。三者合入即完成"断言模式可用，Keycloak 仍是生产默认"的第一阶段。
4. **一个完整发布周期的生产验证窗口**（不是一个切片，是一个时间/证据门槛）：断言登录在生产实际使用，金丝雀干净，双侧审计计数吻合。
5. **XM-INV-KEYCLOAK-RETIRE**（开票线，依赖 4，且需产品负责人明确放行）——`OIDC_ADMIN_LOGIN_ENABLED=false`、Keycloak 容器停用、发布门禁镜像清单改为八个、`RELEASE-READINESS.md`/`IMAGE-SCAN-REVIEW.md` 更新、一次覆盖非 Keycloak 备份集合的恢复演练。

## 确认
- 平台线：验收线，2026-09-02（按产品负责人指令起草）。
- 开票线：验收线，2026-09-02（按产品负责人指令起草）。
- 产品负责人：2026-09-02 15:35 口头裁定；本 CR 为其书面化与技术展开，密钥分发方式（见专项确认段落）与第二阶段放行时机需另行确认。

## 执行记录
- 2026-09-02 立单（设计阶段，未派发实现切片）；配套技术规格见 `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`；切片路线见 `docs/roadmap/CR-0006-console-auth-slices.md`。
