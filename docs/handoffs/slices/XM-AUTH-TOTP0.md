# XM-AUTH-TOTP0：控制台 TOTP 二因素 + 管理员 IP 名单

## status

READY（待验收线审读合入 `release/v0.1-launch`）。CR-0006 路线第一片，无前置依赖，本片自身即可通过"控制台登录要求 TOTP"这条验收线，不依赖 XM-INVCON1。

## branch

`ai/claude/XM-AUTH-TOTP0`（base `release/v0.1-launch` @ `14b840b`），worktree `K:/星芒统一控制平台/wt-xmAUTHTOTP0`。

## commits

一个提交（前一位代理因宿主重启中断、未留下任何提交；本次在同一提交里完成剩余实现、修复、门禁与本文档之外的全部工作）：

- `a116c6d` — feat(localauth): XM-AUTH-TOTP0 console TOTP two-factor + admin IP allowlist

本文档单独作为收尾提交。

## summary

管理员（`admin` 角色，翻译出 `staff.manage`/`finance.read` scope 的账号）现在必须启用 TOTP 才能使用控制台：

1. **首次登录体验**：密码校验通过后，若账号 `must_enroll_totp=true` 且尚未启用 TOTP，会话正常签发（不因为没启用 TOTP 而拒绝登录），前端 `RequireAuth` 检测到 `must_enroll_totp && !totp_enrolled` 后，无论原本要去哪个页面都会先重定向到 `/account/totp`（与既有 `must_change_password` 强制改密同一套软重定向模式，改密优先于启用 TOTP）。该页展示 `otpauth://` URI + Base32 手动录入密钥，用户在认证器 App 里添加后回填 6 位动态码确认；确认成功后一次性展示 10 个恢复码（仅哈希落库），确认后才放行到目标页。
2. **已启用 TOTP 后的登录体验**：密码校验通过后不再直接签发会话，而是返回 `{requires_totp:true, temp_token}`（5 分钟有效的登录挑战），前端展示"两步验证"页要求动态码或恢复码（二选一）；提交 `POST /api/v1/auth/login/totp {temp_token, code|recovery_code}` 校验通过后才签发正式会话，`core.staff_session` 记录 `amr=["pwd","otp"]`、`mfa_at=签发时刻`。
3. **管理员重置**：`人员与权限 → 账号与身份` 表格新增"两步验证"列与"重置 TOTP"操作，走既有通用执行入口调用 `staff.account.reset_totp@1`（管理员动作，撤销目标账号的启用状态、恢复码、会话，`must_enroll_totp` 重新置真）。
4. **管理员来源 IP 名单**（`XM_CONSOLE_ADMIN_IP_ALLOWLIST`，CIDR 逗号分隔，空=不启用）：只对落入 TOTP 强制范围的账号生效，在密码校验通过之后、签发会话或完成二步/步进之前校验来源 IP（复用既有 XFF 解析），拒绝返回 `403 ADMIN_NETWORK_DENIED` 并写审计（`CompensationResult=ip_denied`）。

接手时前一位代理已完成绝大部分实现（`git status` 显示 37 个文件的未提交改动，构建与既有单测均已绿），本次工作内容：

- **通读并核对**已有实现是否与 CR-0006 正文、技术规格（`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`）、路线图一致——未发现"店内已有内容与规格冲突"之处，均保留。
- **修复一处真实的路由缺口**：`internal/platform/httpapi/router.go` 的注释详细说明了 `POST /api/v1/auth/totp/enroll`/`confirm` 两个端点"为什么不需要重复声明 RequireScope"，但**实际的 `api.Post(...)` 路由注册语句从未写出**——`Handlers.EnrollTOTP`/`ConfirmTOTP` 两个方法存在且有完整单测（`handlers_test.go` 的 `TestEnrollTOTPDelegatesToKernel`/`TestConfirmTOTPDelegatesToKernel`/`TestTOTPHandlersTranslateKernelErrors`/`TestEnrollTOTPWithoutKernelReturnsInternalError` 都是直接调用 `h.EnrollTOTP(rec, req)`，绕开了路由层，因此没能捕捉到这个缺口），前端 `web/apps/admin-web/src/api/totp.ts` 也已经在调用这两个路径——但没有实际路由，请求会落到 404。已补上这两行注册（挂在 `RequirePrincipal` 组内，与 `/auth/me`、`/auth/password` 同级，天然带 CSRF 头校验），真实浏览器走一遍（见下）验证通过。
- **补上 `XM_CONSOLE_ADMIN_IP_ALLOWLIST` 的部署侧透传**：`cmd/platform-api/config.go` 已经在读这个环境变量，但 `deploy/compose/launch.yaml`（`platform-api` 服务 environment 块）与 `deploy/compose/.env.example` 都没有它——运维照着 `.env.example` 配置时根本看不到这一项存在。已补上（默认空=不启用，说明见 §11.7）。
- **前一位代理未跑过的门禁**：Storybook 构建、真实浏览器走查、gitleaks，均由本次补齐（见下）。
- **本文档**。

## Action 与契约

| Action ID | 版本 | 风险等级 | Permission | 作用对象 | 参数 |
|---|---|---|---|---|---|
| `staff.account.enroll_totp` | 1 | L1 | `staff.manage` | 调用者自己 | 无 |
| `staff.account.confirm_totp` | 1 | L1 | `staff.manage` | 调用者自己 | `code`（必填） |
| `staff.account.reset_totp` | 1 | L1 | `staff.manage` | 目标账号（`username`） | `username`（必填） |

契约文件：`contracts/actions/staff.account.{enroll,confirm,reset}_totp.v1.json`。

## HTTP 端点

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/login/totp` | 否（temp_token 分支）/ 需 `xm_session`+CSRF（步进分支） | 登录第二步与步进刷新，见 `docs/modules/httpapi/AUTH-SWITCH.md` §11.7 |
| POST | `/api/v1/auth/totp/enroll` | 需 `xm_session`+CSRF | 委托 `staff.account.enroll_totp@1`（**本次新补的路由**） |
| POST | `/api/v1/auth/totp/confirm` | 需 `xm_session`+CSRF | 委托 `staff.account.confirm_totp@1`（**本次新补的路由**） |

`staff.account.reset_totp@1` 没有专用端点，走既有 `POST /api/v1/actions/{id}/versions/{version}/execute` 通用入口。

## XM-INVCON1 将调用的接口（本片交付、下一片消费）

1. **`localauth.RequireFreshOTP(p principal.Principal, maxAge time.Duration, now time.Time) bool`**（`internal/platform/localauth/resolver.go`）——判断 `p.MFAAt` 是否在 `maxAge` 内。断言签发端点应在签发前调用它（建议 `maxAge=10*time.Minute`，与 CR-0006 正文/技术规格里开票侧 `AdminPolicy.StepUpMaxAge` 默认值一致），不通过时返回 `403 ADMIN_STEP_UP_REQUIRED`，前端就地弹出 TOTP 重新验证（复用 `POST /api/v1/auth/login/totp` 的步进分支，不带 `temp_token`，走已登录会话）。
2. **`principal.Principal.MFAAt *time.Time`**（`internal/platform/principal/principal.go`）——`localauth.Resolver.Resolve` 从会话的 `mfa_at` 列填入；`nil` 表示这次会话从未过二因素或当前身份来源不适用这个概念（oidc/dev-header 两种解析器留空）。
3. **`localauth.AdminIPAllowlist`**（`internal/platform/localauth/adminip.go`，`ParseAdminIPAllowlist(raw string) (AdminIPAllowlist, error)` + `(a AdminIPAllowlist) Allowed(ip string) bool`）——断言签发端点应读取**同一个** `XM_CONSOLE_ADMIN_IP_ALLOWLIST` 环境变量（已在 `cmd/platform-api/config.go` 解析进 `config.ConsoleAdminIPAllowlist`），对签发请求的来源 IP 做纵深防御校验，与本片登录/步进用的是同一份配置来源，不要重新读一份。
4. **`localauth.Store.TouchSessionMFA(ctx, rawToken) error`** 与 `LookupSession` 返回的 `Account.SessionMFAAt`/`SessionAMR`——若断言签发端点需要直接查会话状态而不经 `principal.Principal`，这两个方法/字段已经就绪。
5. **账号是否已启用 TOTP、是否强制**：`GET /api/v1/auth/me` 响应含 `totp_enrolled`/`must_enroll_totp`/`totp_enrolled_at`/`recovery_codes_remaining` 四个字段（形状见 `internal/platform/localauth/handlers.go` 的 `sessionResponse`），前端 `EmbeddedConsoleFrame`（XM-INVCON1 范围）若需要判断"这个操作员能不能签发断言"可以直接读这几个字段，不需要新查询。

## files_changed

新增（12）：
- `internal/platform/localauth/totp.go`、`totp_test.go`——RFC 6238 核心（HOTP/TOTP、otpauth URI、恢复码生成/哈希/规整），含对 RFC 6238 附录 B 官方测试向量的单测。
- `internal/platform/localauth/adminip.go`、`adminip_test.go`——CIDR 名单解析与校验。
- `contracts/actions/staff.account.{enroll,confirm,reset}_totp.v1.json`。
- `db/migrations/000024_staff_totp.{up,down}.sql`。
- `web/apps/admin-web/src/api/totp.ts`、`src/lib/totpForm.ts`、`src/pages/TotpEnrollPage.tsx`。
- `docs/evidence/screens/XM-AUTH-TOTP0/`（8 张真实浏览器截图，见下）。

修改（既有实现，逐一读过，未发现需要推翻的问题；本次仅修 2 处见上文"summary"）：
- `internal/platform/localauth/{store,actions,handlers,resolver}.go` 及三者的 `_test.go`——TOTP 三列/两步登录/恢复码/步进的仓储与 Action/Handler 层。
- `internal/platform/httpapi/{localauth,router}.go`——`LocalAuthHandlers` 接口追加三个方法；**路由层补上两行注册**（本次修复）。
- `internal/platform/principal/principal.go`——`Principal.MFAAt *time.Time`。
- `cmd/platform-api/{config,localauth,main}.go`——`XM_CONSOLE_ADMIN_IP_ALLOWLIST` 解析与装配、TOTP 密钥读写路径接线。
- `cmd/staff-bootstrap/main.go`——bootstrap 账号同样落入 `must_enroll_totp=true`（不因为是 bootstrap 账号豁免，见该文件注释）。
- `web/apps/admin-web/src/{api/staff.ts,auth/{RequireAuth,localSession}.{ts,tsx},components/StaffAccountsPanel.tsx,pages/LoginPage.tsx,router.tsx}` 及各自 `.test.ts(x)`——两步登录 UI、强制引导、管理员重置入口。
- `docs/modules/httpapi/AUTH-SWITCH.md`——新增 §11.7（本片）小节；**本次追加**部署侧 `deploy/compose/launch.yaml`、`.env.example` 的 `XM_CONSOLE_ADMIN_IP_ALLOWLIST` 透传（原缺口，见上文"summary"）。

## tests_run

Go（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 八变量全 unset 前缀，见项目既有坑位笔记）：

```bash
go build ./...
go vet ./...
go test -p 1 -count=1 ./...
```

结果：全绿，一次性通过，**未观察到 loopback/httptest 相关的抖动**，未需要隔离重跑任何包。`internal/platform/localauth` 包本身 94 个 `func Test...`（含新增的 15 个 TOTP/IP 名单相关、`store_test.go` 里数据库集成用例见下"not_run"）。

前端（`pnpm --config.verify-deps-before-run=false` 前缀，worktree 的根 `node_modules` 是指回主检出的 junction，见既有坑位笔记）：

```bash
pnpm --config.verify-deps-before-run=false -r run typecheck   # 5/6 workspace 项目，全部 Done
pnpm --config.verify-deps-before-run=false -r run test        # design-tokens 10、ui-primitives 16、ui-admin 253、admin-web 1415，全绿
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build   # Storybook build completed successfully
```

Storybook 构建**首次尝试卡在 pnpm 的隐式依赖校验触发的 `install` 死循环**（EEXIST/rename 竞争，本机 Windows Defender + junction 化 node_modules 的既知坑），杀掉后加 `--config.verify-deps-before-run=false` 重试即通过，本片未新增任何 Storybook story（复用 `ui-primitives`/`ui-admin` 既有已入库的组件与 stories，`TotpEnrollPage`/`LoginPage` 两个页面级组件与既有 `ChangePasswordPage` 同一先例不单独建 story）。

治理与密钥泄露：

```bash
bash scripts/check-governance.sh   # exit 0，无输出
gitleaks protect --staged --verbose   # no leaks found
```

格式化核对（本次改动/新建的文件逐个跑，不对整仓库跑 `gofmt -w`，见既有坑位笔记）：

```bash
"$(go env GOROOT)/bin/gofmt" -d <本次涉及的 17 个 .go 文件>   # 全部空 diff
```

**真实浏览器走查**（Docker Desktop 在本环境不可用，见下"not_run"；改用一个约 230 行的 mock API + 真实 Vite dev server + Chrome DevTools 的组合，复现"一个新管理员账号从强制启用 TOTP 到完成两步登录"的完整体验）：

1. `/login` 密码登录（`docs/evidence/screens/XM-AUTH-TOTP0/02-login-password-step.png`）——密码通过、账号 `must_enroll_totp=true`、TOTP 尚未启用。
2. 自动重定向到 `/account/totp` 展示 otpauth 密钥（`03-totp-enroll-forced.png`），用本地计算的当前有效动态码确认。
3. 一次性恢复码展示（`04-recovery-codes-reveal.png`），确认后进入工作台（`05-dashboard-after-enroll.png`，顶栏正确显示操作员姓名"陈静"）。
4. 退出登录、重新用同一账号密码登录：这次密码通过后**不再直接签发会话**，落到"两步验证"页要求动态码（`06-login-totp-second-step.png`）——这是"下次登录"体验的核心证据；输入当前有效动态码后成功进入工作台。
5. "人员与权限 → 账号与身份"表格正确显示该账号"两步验证：已启用"（`07-staff-accounts-totp-status.png`），"重置 TOTP"确认弹窗正确点名 `staff.account.reset_totp@1` 与后果说明（`08-admin-reset-totp-dialog.png`）。

mock API 精确复刻了 `internal/platform/localauth/handlers.go` 里各响应体的字段形状（`sessionResponse`/`pendingTOTPResponse`/`enrollTOTPResponse` 等）与 RFC 6238 算法本身（30 秒步长、6 位、SHA1），用于验证**前端**的状态机、路由重定向、表单校验与错误文案，**不是**对后端 Go 实现的替代验证——后端由上面的 `go test` 覆盖。`01-forced-change-password.png` 是前一位代理中断前留下的既有截图（强制改密页，非本次新增，一并保留在同一目录）。

## not_run

- **`internal/platform/localauth/store_test.go` 的 15 个真库集成用例被跳过**（`XM_TEST_DATABASE_URL` 未设置时 `t.Skip`），包括本片新增的 `TestStoreTOTPEnrollConfirmResetLifecycle`、`TestStoreLoginChallengeLifecycle`、`TestStoreCreateSessionWithAMRAndMFA`、`TestStoreTouchSessionMFA` 等——**本环境没有可用的 Postgres**：Docker Desktop 未安装完整（只有 `com.docker.service`/CLI 插件，没有实际的 `Docker Desktop.exe`/WSL2 后端，`dockerDesktopLinuxEngine` 命名管道不存在，尝试启动服务后仍连不上），本机也没有原生 PostgreSQL 安装。这些用例的 SQL 逻辑与既有四个账号 Action 的迁移往返在设计评审阶段核对过，但**未在本环境实际针对真实 PostgreSQL 跑过**，是本片最大的验证缺口，请验收线在有 Docker/Postgres 的环境复跑一次（`XM_TEST_DATABASE_URL=postgres://... go test -p 1 -run TestStore ./internal/platform/localauth/...`）作为合入前置。
- **未跑集成测试矩阵里的限流/IP 名单端到端场景**（技术规格测试矩阵 #16~19 一类）——`adminip_test.go`/`handlers_test.go` 里的单测覆盖了 `AdminIPAllowlist.Allowed` 本身与 Handler 层的 IP 拒绝分支（用假 store），但没有对着真实 HTTP 服务器+真实数据库跑一次端到端的允许/拒绝往返。
- **未执行 XM-INVCON1 的任何实现**——本片明确"不涉及断言签发/兑换"，上面"XM-INVCON1 将调用的接口"一节只是把已就绪的钩子指给下一片，不代表下一片已经开工。
- **未修 `docs/modules/httpapi/AUTH-SWITCH.md` §11.8"已知缺口"里"前端还没有本地登录页"这一条陈旧表述**——核实过这条文字在本片开工前（`git show HEAD:...` 基线）就已经过时（`LoginPage.tsx` 的本地登录表单其实在更早的 XM-LOGIN 提交 `18f05ad` 就已经交付），与本片无关，不在本片范围内顺手改，留给下次touch这份文档的人一并清理。
- **未验证 `staff-bootstrap` 生成的第一个账号在真实数据库上完整走一遍"必须启用 TOTP"流程**——代码逻辑与既有 `must_change_password` 完全对称，且本次浏览器走查已经用等价场景（`must_enroll_totp=true` 的新账号）验证了前端行为，但走查用的是 mock API，不是真的 `cmd/staff-bootstrap` 二进制 + 真实数据库。

## risks

- **本片最大风险是"未在真实 Postgres 上跑过 15 个新增/改动的集成测试"**（见上）。仓储层的 SQL（尤其 `ConfirmTOTP`/`ResetTOTP`/`SetTOTPSecretRef` 几个用 `WHERE ... IS NULL` 做原子状态转换、防 TOCTOU 的写法）逻辑上经过设计评审，但没有真库背书，建议验收线合入前第一件事就是把这些用例跑一遍。
- **路由缺口是一类容易被单测掩盖的错误**：`handlers_test.go` 直接构造 `httptest.NewRequest` 后手工调用 `h.EnrollTOTP(rec, req)`，完全绕开了 `router.go` 的路由匹配，因此"处理器存在但没挂路由"这类错误不会被单元测试捕捉到——只有集成测试（真实启动 HTTP 服务器）或本次做的真实浏览器/mock API 走查才能发现。建议后续切片如果也走"先写 Handler 单测、路由留到最后"的顺序，在门禁清单里加一条"用真实路由表核对每个新 Handler 都被挂载"的检查，不必依赖人工记得去读。
- **两侧 IP 名单（`XM_CONSOLE_ADMIN_IP_ALLOWLIST` 与开票侧 `AdminCIDRs`）没有自动同步机制**，这是 CR-0006 正文已经承认、本片继承的既有操作代价，不是新引入的脆弱性，但本片交付时两侧还没有任何一侧真正配置这个名单（默认空=不启用），首次上线配置时格外需要人工核对一致。
- **恢复码用尽没有自动补发路径**（设计如此，CR-0006 与技术规格测试矩阵 #28 都提到）：管理员必须走 `reset_totp` 让账号整个重新走一遍启用才能拿到新一批恢复码，期间该账号完全无法登录（TOTP 已清空但 `must_enroll_totp` 又要求先启用）——这不是本片引入的缺陷而是设计取舍，但值得在运维手册里提醒一句"看到账号联系不上又用尽恢复码，唯一出路是另一个管理员 reset_totp"。

## follow_ups

- **合入前**：在有 Docker/Postgres 的环境跑一次 `XM_TEST_DATABASE_URL=... go test -p 1 -run TestStore ./internal/platform/localauth/...`（见 not_run），确认 15 个真库集成用例通过。
- 开工 **XM-INVCON1**：断言签发端点、`internal/platform/consoleassertion` 新包、Ed25519 签名密钥托管、`EmbeddedConsoleFrame` 的 `kind:"admin-assertion"` postMessage 扩展——需要用到上面"XM-INVCON1 将调用的接口"一节列出的四个钩子。
- 运维需要在实际启用 `XM_CONSOLE_ADMIN_IP_ALLOWLIST` 时同步核对开票侧 `AdminCIDRs` 是否一致（CR-0006 正文已建议加一条轻量校验脚本，本片未实现，留给 XM-INVCON1 或独立的小改动）。
- 清理 `docs/modules/httpapi/AUTH-SWITCH.md` §11.8 里"前端还没有本地登录页"这条已经过时的表述（见 not_run，不在本片范围）。

## screenshot 路径

`docs/evidence/screens/XM-AUTH-TOTP0/`：
- `01-forced-change-password.png`（既有，前一位代理留下）
- `02-login-password-step.png`
- `03-totp-enroll-forced.png`
- `04-recovery-codes-reveal.png`
- `05-dashboard-after-enroll.png`
- `06-login-totp-second-step.png`
- `07-staff-accounts-totp-status.png`
- `08-admin-reset-totp-dialog.png`
