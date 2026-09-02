# XM-INVCON1：控制台断言签发端点 + 密钥托管 + EmbeddedConsoleFrame 转交（CR-0006 平台线第二片）

## status

READY（待验收线审读、复跑并人工合入）。依赖 XM-AUTH-TOTP0（已合入
`release/v0.1-launch` @ `68759b5`，本片 base 于其后的 `f61069f`）。

## branch

`ai/claude/XM-INVCON1`（base `release/v0.1-launch` @ `f61069f`），worktree
`K:/星芒统一控制平台/wt-xmINVCON1`。

## summary

依据 CR-0006（`docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`）
与冻结技术规格（`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`），
本片交付平台侧断言签发能力（"控制台代认证"第一阶段三个切片之一，与开票线
XM-INV-CONSOLE-ASSERT 各自独立实现、共享同一份冻结契约）：

1. **新包 `internal/platform/consoleassertion`**：Ed25519 签名（JWS compact
   serialization，手写实现，不引入新依赖，与开票侧、`oidcauth` 同一先例）、
   `POST /api/v1/auth/console-assertion` 签发端点（`finance.read` → IP 名单
   → TOTP 新鲜度三重校验，顺序固定；错误码
   `FINANCE_SCOPE_REQUIRED`/`ADMIN_NETWORK_DENIED`/`ADMIN_STEP_UP_REQUIRED`/
   `INVALID_PARAMS`/`INTERNAL` 逐字匹配技术规格 §5.1）、审计事件
   `staff.console_assertion.issue`（成功记截断 nonce，失败记原因，完整
   nonce 从不落审计）。**不是 Action**——理由见包文档注释，与 CR-0006 正文
   的裁定一致（签发不改平台状态，性质等价于登录/签会话）。
2. **签名密钥托管**：`Signer` 持有恰好一把当前生效的 Ed25519 私钥，经
   `secret://console-assertion/signing-key-<key_id>` 引用从与 TOTP 密钥
   同一份 `XM_SECRET_ROOT` 目录解析（`cmd/platform-api` 复用
   `platformUsersSecretProvider`）；`key_id` 直接从引用名派生，兼作 JWS
   `kid`，不另设配置项。`cmd/console-assertion-keygen`：一次性生成 Ed25519
   密钥对，打印公钥清单条目（`contracts/auth/console-assertion-keyring.v1.json`
   形状）与私钥一次性明文（同 `cmd/staff-bootstrap` 一次性密码的纪律：
   Platform Lifecycle Operation，不经 Action，因为此刻还没有可归因的会话）。
3. **`EmbeddedConsoleFrame`（`web/packages/ui-admin`）扩展**：新增
   `assertion`/`onAssertionNeeded` props，向 iframe `postMessage`
   `{type:"xm-embed",version:1,kind:"admin-assertion",assertion}`
   （targetOrigin 精确等于配置来源），并监听 `kind:"admin-assertion-needed"`
   请求重签发——沿用 XM-INVCON0 已建立的 `xm-embed` 信封，新增两个 `kind`，
   不升版本号。
4. **`InvoiceConsolePanel`（`web/apps/admin-web`）编排**：新 hook
   `useConsoleAssertion` 挂载即签发，按 `expires_at` 提前 60 秒自动重签发，
   五态状态机（loading/ready/step-up/denied/unavailable/error）；只在
   `authMode==="local"` 时启用——`oidc`/`dev-header` 模式原样回落到未改动的
   iframe 直渲染（旧行为，因为断言依赖仅 local 模式填充的 `mfa_at`）。
   `denied`/`unavailable`/`error` 用 `PageState` 呈现（"无权限"/"开票系统
   未启用断言登录"/带重试的加载失败）；`step-up` 渲染共享的 `TotpVerifyForm`。
5. **`TotpVerifyForm`（新组件）**：从 `LoginPage.tsx` 的 `TotpStepPage` 抽出
   的可复用动态码/恢复码表单，供登录第二步与本片的步进提示两处复用（任务书
   "reuse the TOTP login component" 的具体做法）；`LoginPage.tsx` 同步重构
   为委托它，行为不变。新增 `auth/localSession.ts` 的 `stepUpTotp()`——调用
   `POST /api/v1/auth/login/totp` 的步进分支（不带 `temp_token`），是
   `completeTotpLogin` 的姊妹函数。
6. **配置**：`XM_INVOICE_CONSOLE_ASSERTION_ENABLED`（默认 `false`）、
   `_ISSUER`（精确 https 来源）、`_AUDIENCE`（默认
   `xingmang-console-assertion-v1`）、`_KEY_REF`、`_STEP_UP_MAX_AGE`
   （默认 10 分钟）。`enabled=true` 时交叉校验 `XM_AUTH_MODE=local`，否则
   拒绝启动（断言依赖 local 模式的 `mfa_at`，另一模式启用了也永远
   `ADMIN_STEP_UP_REQUIRED`，是可预见的死锁，不如启动时直接报错）。

### 一处已记录并按冻结规格处理的分歧

任务书写 `RequireFreshOTP(principal, 15m)`；CR-0006 冻结技术规格与
XM-AUTH-TOTP0 交接文档「XM-INVCON1 将调用的接口」一节都只提到 **10 分钟**
一个数字（"建议 maxAge=10*time.Minute，与开票侧 AdminPolicy.StepUpMaxAge
现有默认值一致"）。按"冻结规格与前一切片显式建议优先于任务书转述"处理，
`defaultStepUpMaxAge` 采用 10 分钟（`internal/platform/consoleassertion/handlers.go`
的同名常量注释记录了这条分歧）；仍可经 `Config.StepUpMaxAge`
（`XM_INVOICE_CONSOLE_ASSERTION_STEP_UP_MAX_AGE`）覆盖。请验收线确认这个
取舍，如果任务书的 15 分钟另有权威来源，改一个常量即可。

## 端点契约（与开票侧 XM-INV-CONSOLE-ASSERT 核对一致）

`POST /api/v1/auth/console-assertion`（控制台侧，本片）：

- 鉴权：`xm_session` Cookie + `X-Requested-With: xingmang`（挂在
  `RequirePrincipal` 组内，由 `localauth.Resolver.Resolve` 统一强制，本包
  不重复判断）。
- 请求体：`{"scope":"sub2api"|"newapi"|"global"}`。
- 成功：`200 {"assertion":"<compact JWS>","expires_at":"<RFC3339 UTC>"}`。
- 失败：`{error:{code,message,request_id}}`，`code` ∈
  `INVALID_PARAMS`(400) / `FINANCE_SCOPE_REQUIRED`(403) /
  `ADMIN_NETWORK_DENIED`(403) / `ADMIN_STEP_UP_REQUIRED`(403) /
  `PERMISSION_DENIED`(401) / `INTERNAL`(500)。
- 断言 claims：`iss`（配置的 issuer）、`aud`（默认
  `xingmang-console-assertion-v1`）、`sub`（`core.staff_account.id`
  UUID）、`username`、`roles`（账号真实角色，不是 scope）、
  `acr="xingmang-console-totp-v1"`、`amr=["pwd","otp"]`、`scope`、
  `nonce`（32 字节 base64url）、`iat`/`nbf`/`exp`（`exp-iat` 恒等于 5
  分钟，`Sign` 内部计算，调用方无法拉长）。JWS header：
  `{"alg":"EdDSA","kid":"<key_id>","typ":"xm-console-assertion+jwt"}`。

已与开票侧 `K:/发票/wt-XM-INV-CONSOLEASSERT/docs/handoffs/XM-INV-CONSOLE-ASSERT.md`
逐字核对：路径（无 `/exchange` 后缀）、claim 名称、`typ`/`alg`、`aud` 默认
值、错误码分层、审计字段设计完全一致——开票侧的兑换端点是照着同一份冻结
规格独立实现的，两侧尚未做过真实密钥的端到端联调（见下"not_run"）。

## 公钥清单文件

`contracts/auth/console-assertion-keyring.v1.json` 本片**发布为空数组
`[]`**（占位，非空清单要等第一把生产密钥生成后才提交）——与开票侧当前副本
的占位状态一致（该侧交接文档记录"nothing here has ever verified against a
key XM-INVCON1 actually generated"）；`internal/platform/consoleassertion/keyring.go`
的 `LoadKeyringJSON`/`ValidateRecords` 接受空清单为合法状态。**这是本片对
"首版发布"的读法**：发布的是文件本身与其校验规则（`PublicKeyRecord`/
`NewPublicKeyRecord`/`MarshalManifestJSON`/`LoadKeyringJSON`，均有单测覆盖
fleet_keyring.go 同款校验规则的移植——purpose/protocol 域分离、指纹匹配、
`valid_from<valid_until`、UTC、撤销早于到期且有理由、排序去重、公钥材料不
可复用），不是一把已经生成好塞进仓库的"生产密钥"——生成真实密钥并同步进
两个仓库是生产上线步骤（见下），不属于本片"合入即完成"的范围。

**设计取舍（与"多个 kid，最新有效的签"的字面表述有出入，已记录）**：
`Signer` 只持有配置指定的**一把**当前生效私钥（由
`XM_INVOICE_CONSOLE_ASSERTION_KEY_REF` 单一环境变量指定，`key_id` 从引用名
派生），不在运行时同时加载多把私钥、按有效期挑"最新有效"的一把去签。理由
（`internal/platform/consoleassertion/signer.go` 的 `Signer` 类型注释有完整
记录）：断言本身有效期硬上限 5 分钟，不同于 `fleet_keyring.go` 的制品可能
被验证很久之后，签发侧完全不需要保留旧密钥——轮换 = 运维生成新密钥、把
`XM_INVOICE_CONSOLE_ASSERTION_KEY_REF` 指向新引用、重新部署，`valid_from`/
`valid_until`/`revoked_at` 三态是给**验证侧**（开票侧）用的防线，不是签发侧
的选择依据。如果验收线认为"运行时持有多把、按有效期自动切换"是必须的，
需要另一轮设计（不是这次的实现范围）。

## Action 与契约

本片**没有新增任何 Action**——`POST /api/v1/auth/console-assertion` 不经
`action.Registry`（理由见上文与包文档注释）。没有新增数据库迁移（TOTP 相关
表结构已在 XM-AUTH-TOTP0 交付）。

## HTTP 端点

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/console-assertion` | `xm_session`+CSRF；`finance.read`/IP 名单/TOTP 新鲜度三重校验 | 签发断言（本片新增） |

`httpapi.Deps.ConsoleAssertion` 为 `nil` 时（`XM_INVOICE_CONSOLE_ASSERTION_ENABLED=false`，
默认）路由不挂载这条端点——404，不是拿一个未装配的签名器硬跑出 500，与
`LocalAuth`/`RequestLogs` 等既有可选依赖同一条纪律。

## files_changed

新增：
- `internal/platform/consoleassertion/{consoleassertion,keyring,claims,signer,handlers}.go`
  + 对应 `_test.go`（keyring/signer/handlers 三个测试文件）。
- `internal/platform/httpapi/consoleassertion.go`——`ConsoleAssertionHandlers` 接口。
- `cmd/console-assertion-keygen/main.go`——一次性密钥生成 + 清单条目打印工具。
- `cmd/platform-api/consoleassertion.go`——签名器 + Handlers 装配。
- `contracts/auth/console-assertion-keyring.v1.json`——空数组占位。
- `web/apps/admin-web/src/api/consoleAssertion.{ts,test.ts}`。
- `web/apps/admin-web/src/components/TotpVerifyForm.{tsx,test.tsx}`。
- `docs/evidence/screens/XM-INVCON1/`（6 张真实浏览器截图，见下）。
- 本文档。

修改：
- `internal/platform/httpapi/router.go`——`Deps.ConsoleAssertion` 字段 +
  路由挂载（`RequirePrincipal` 组内）。
- `cmd/platform-api/{config,main}.go`——`consoleAssertionConfig`/
  `consoleAssertionConfigFromEnv`（交叉校验 `XM_AUTH_MODE=local`）、
  签名器/Handlers 装配、`httpapi.Deps.ConsoleAssertion` 接线。
- `deploy/compose/{launch.yaml,.env.example}`——五个新环境变量，默认关闭。
- `web/packages/ui-admin/src/EmbeddedConsoleFrame.{tsx,test.tsx,stories.tsx}`——
  `assertion`/`onAssertionNeeded` props、postMessage 投递、入站
  `admin-assertion-needed` 识别、两个新 Storybook 故事。
- `web/apps/admin-web/src/components/InvoiceConsolePanel.{tsx,test.tsx}`——
  `useConsoleAssertion` hook、五态渲染、local 模式判断。
- `web/apps/admin-web/src/auth/{localSession,localSession.test}.ts`——
  `stepUpTotp()`。
- `web/apps/admin-web/src/pages/LoginPage.tsx`——`TotpStepPage` 重构为委托
  `TotpVerifyForm`，行为不变（无既有自动化测试覆盖这条路径，真实浏览器走查
  已重新验证，见下）。

## tests_run

Go（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 八变量全 unset 前缀）：

```bash
go build ./...
go vet ./...
go test -p 1 -count=1 XM_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/xm_test?sslmode=disable" ./...
```

结果：`go build`/`go vet` 全仓库干净；`go test` 全仓库跑完，**consoleassertion
本包 100% 通过**（32 个测试函数：keyring 校验规则 15 个、signer 签名/
密钥装配/独立验签交叉核对 6 个、handlers 端到端 12 个含审计事件断言），
`httpapi`、`cmd/platform-api`、`cmd/console-assertion-keygen`（无测试文件）
同样全绿。

`"$(go env GOROOT)/bin/gofmt" -d <本次触达的 14 个 .go 文件>`——全部空 diff。

前端（`pnpm --config.verify-deps-before-run=false` 前缀，worktree 根
`node_modules` 是指回主检出的 junction）：

```bash
pnpm --config.verify-deps-before-run=false -r run typecheck   # 5/6 workspace 项目，全部 Done
pnpm --config.verify-deps-before-run=false -r run test        # design-tokens 10、ui-primitives 16、ui-admin 260、admin-web 1437，全绿
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
```

Storybook 构建：`storybook build` 自身打印"Storybook build completed
successfully"，产物目录 `storybook-static` 核对含新增的
`WithAssertion`/`AssertionNeededCallback` 两个故事（`grep` 命中构建产物
JS）。**wrapper 进程随后在退出阶段崩溃**（`Assertion failed:
!(handle->flags & UV_HANDLE_CLOSING)`，Node/Windows 已知的 libuv 退出期
断言，与本次改动无关，构建产物已核实完整存在），因此 `pnpm` 报了个
非零退出码——已通过检查产物目录而不是退出码确认构建本身成功。

治理：`bash scripts/check-governance.sh`——exit 0，无输出。

gitleaks：`gitleaks protect --staged --verbose`——`no leaks found`
（0 commits scanned，因为提交尚未创建；扫描的是暂存区的 ~127KB 改动）。

**真实浏览器走查**（Docker/真实 platform-api 在本环境不可用，同
XM-AUTH-TOTP0 的既有坑位；沿用同一套"mock API + 真实 Vite dev server +
Chrome DevTools"配方，本次额外加了一个跨源 mock 开票控制台页面，因为这次
要验证的正是跨源 postMessage 投递本身）：

1. 本地登录密码 + TOTP 两步验证（`docs/evidence/screens/XM-INVCON1/01-login-totp-step.png`）。
2. 导航到 Sub2API → 支付与财务 → 开票：断言签发成功，`EmbeddedConsoleFrame`
   把 `{kind:"admin-assertion",assertion}` postMessage 给跨源
   （`https://127.0.0.1:8081` vs 控制台的 `http://localhost:5183`）mock
   开票页面，页面正确收到并展示（`02-invoice-tab-assertion-delivered.png`）
   ——这是本片最核心的一条证据：跨源投递链路端到端打通。
3. 用调试端点把 mock 后端的 `mfa_at` 判为过期，切子页签再切回触发重新挂载：
   正确显示"需要二次验证"（复用的 `TotpVerifyForm`，
   `03-invoice-tab-step-up-required.png`）；输入动态码验证通过后自动重新
   签发并再次成功投递（第 2 份断言，`04-invoice-tab-after-stepup-reissued.png`）。
4. 在 mock 开票页面点击"请求重新签发"按钮（模拟真实开票前端发
   `admin-assertion-needed`）：控制台侧 `onAssertionNeeded` 正确触发
   `reissue()`，收到第 3 份断言
   （`05-invoice-tab-assertion-needed-reissue.png`）。
5. 用调试端点让后端对签发请求返回 `FINANCE_SCOPE_REQUIRED`：正确显示
   "无权访问"（`06-invoice-tab-finance-scope-denied.png`）。

mock API 精确复刻了 `internal/platform/consoleassertion/handlers.go` 的
各响应体字段形状与错误码；mock 开票页面是本片新写的一次性走查工具（不是
交付物，未纳入本次 `git add`），用于证明"跨源 postMessage 投递"这一步
真的发生了，而不是只在同源 jsdom 里通过。走查完成后已撤销
`web/apps/admin-web/public/app-config.js` 的临时覆盖（`git diff` 确认为
空）。

## not_run

- **未对着一把真实生成的 Ed25519 密钥做两侧端到端联调**：本片的
  `cmd/console-assertion-keygen` 与 `Signer` 均有独立单测覆盖（含"用独立
  重写的验签逻辑核对签出的 JWS"这条 cross-check 测试，模拟的正是开票侧
  验证规则），但没有真的生成一把密钥、把公钥条目同时放进两个仓库各自的
  `contracts/auth/console-assertion-keyring.v1.json`、再用开票侧的真实
  `VerifyConsoleAssertion` 去验证本片签出的 JWS。这是两侧都各自记录的同一
  个缺口（开票侧交接文档"Not run"第二条），需要两线都合入生产候选分支后
  再联调，或至少各自跑一次对方的验签/签发代码。
- **未在真实 PostgreSQL 上跑集成测试之外的场景**：本片没有新增数据库表
  或迁移，`go test` 已针对共享测试库跑通（含所有既有集成测试）。
- **未跑生产/预发布环境下的真实 `cmd/console-assertion-keygen`**：工具本身
  有 Go 层面的行为验证（生成的 record 经 `NewPublicKeyRecord`
  自校验），但没有真的跑过一次"生成 → 走 `credential.secret.upsert` →
  `platform-api` 用它签出一枚断言"的完整生产路径（这需要一个真实运行的
  `platform-api` 进程 + 数据库，本环境不可用）。
- **未验证 IP 名单/限流的真实网络层行为**：`AdminIPAllowlist.Allowed` 与
  `RequirePrincipal`→`RateLimit` 中间件链路各自有单测（`handlers_test.go`
  的 `TestIssueRejectsDeniedIP`/`TestIssueAllowsWhitelistedIP`），但没有对
  着真实反代/XFF 头链路跑一次端到端验证。
- **`LoginPage.tsx` 的 `TotpStepPage` 重构没有既有自动化测试可比对**：
  搜索仓库发现 `LoginPage.test.tsx` 从未覆盖过两步验证这条路径（不是本片
  引入的缺口），本次靠真实浏览器走查（上文步骤 1）重新验证了行为未变；
  建议后续独立小改动给这条路径补自动化覆盖（`TotpVerifyForm.test.tsx` 已
  覆盖表单本身，缺的是 `LoginPage.tsx` 里"密码步骤 → 两步验证 → 成功跳转"
  这条集成路径）。

## risks

- **本片新增的三个环境变量（`ISSUER`/`AUDIENCE`/`KEY_REF`）与开票侧对应
  三个环境变量必须逐字一致，否则断言会在验证阶段被拒**（`iss`/`aud` 精确
  匹配，`kid`——从 `KEY_REF` 派生——必须命中开票侧清单里的记录）：这是
  CR-0006 设计本身的既有代价（"静态清单，不采用实时 JWKS"），本片按契约
  实现，不是新引入的脆弱性，但上线时两侧配置核对是唯一防线，建议按 CR-0006
  正文建议补一条轻量校验脚本（本片未实现，留给后续）。
- **签发侧只持有一把当前生效密钥的设计取舍**（见上文"设计取舍"一节）：
  如果验收线认为"运行时同时持有多把、按 valid_from 自动切换"是硬性要求，
  这是一处需要返工的地方，请尽早确认。
- **`XM_INVOICE_CONSOLE_ASSERTION_STEP_UP_MAX_AGE` 默认值分歧**（见上文，
  10 分钟 vs 任务书的 15 分钟）：已按冻结规格处理，但请验收线一并确认。
- **`contracts/auth/console-assertion-keyring.v1.json` 仍是空数组**：
  `XM_INVOICE_CONSOLE_ASSERTION_ENABLED=true` 但清单为空时，
  `NewSigner` 会在找不到私钥时报错拒绝启动（fail closed），不会静默用
  一把不存在的密钥——这是预期行为，但意味着"启用开关"与"清单/密钥就绪"
  两件事必须按下文顺序推进，颠倒顺序会让 platform-api 直接起不来。
- **真实浏览器走查用到的自签证书未能从 Windows 当前用户 Root 证书库清除**：
  为了让 `invoiceConsoleOrigin` 的前端 https 校验在本地走查时通过，临时给
  `127.0.0.1` 生成了一张自签证书并信任进
  `Cert:\CurrentUser\Root`（2 天有效期，`CN=127.0.0.1`）。清理阶段
  `certutil -delstore`/PowerShell `Remove-Item`/.NET `X509Store.Remove`
  三种方式均触发 Windows 对 Root 证书库删除操作的强制交互确认对话框，在
  本沙箱环境里无法回应，全部挂起后被我手动终止——**这张证书目前仍留在
  本机当前用户的 Root 信任库里**（2 天后自然过期，作用域仅本机当前
  Windows 账号，不影响任何其它机器或系统），请验收线或本机操作者手动清除：
  `certmgr.msc` → 受信任的根证书颁发机构 → 证书 → 找到使用者
  "127.0.0.1" 的那条 → 删除（会弹一次系统确认框，人工点掉即可）。

## follow_ups

- 生成第一把生产 Ed25519 密钥、与开票侧联调一次真实签发/验证往返（见
  上文"not_run"）。
- 按 CR-0006 正文建议补一条轻量脚本，发布前核对两侧 `XM_CONSOLE_ADMIN_IP_ALLOWLIST`
  与开票侧 `AdminCIDRs`、以及本片新增的 `ISSUER`/`AUDIENCE` 三项是否一致
  （XM-AUTH-TOTP0 交接文档已经提过前一半，本片补上后一半）。
- 给 `LoginPage.tsx` 的两步登录集成路径补自动化测试（见上文"not_run"）。
- 确认 `defaultStepUpMaxAge` 采用 10 分钟是否符合预期（见"一处已记录并按
  冻结规格处理的分歧"）。
- 确认"签发侧只持有一把当前生效密钥"的设计取舍是否符合预期（见
  "设计取舍"）。
- 待"一个完整发布周期"生产验证窗口达成后，开工 XM-INV-KEYCLOAK-RETIRE
  （见 `docs/roadmap/CR-0006-console-auth-slices.md`，需产品负责人另行放行）。
- **XM-INVCON1-FALLBACK**（已交付）：生产在断言登录正式启用前的过渡期一直
  运行在 `XM_INVOICE_CONSOLE_ASSERTION_ENABLED=false`，`InvoiceConsolePanel`
  当时会把 404（未挂载）判成 `unavailable`，渲染一块整页替换的
  `PageState`，导致开票页签在断言未启用期间直接不可用——比 XM-INVCON0
  的旧行为（弹窗 OIDC 仍能用）倒退了。该片把这个 404 判定改为回落到
  XM-INVCON0 原版直接 iframe（旧登录方式不受影响），上方加一条诚实提示；
  详见 `docs/handoffs/slices/XM-INVCON1-FALLBACK.md`。

## 生产上线步骤（本片只装配能力，不激活；见 risks 的顺序依赖）

1. **确认前置条件**：目标环境 `XM_AUTH_MODE=local`（断言依赖本地登录的
   `mfa_at`），且 XM-AUTH-TOTP0 已在该环境生产运行（管理员账号已启用
   TOTP）。
2. **生成密钥**：在受控环境运行
   `go run ./cmd/console-assertion-keygen -key-id <YYYY-MM> -validity-days 365 -manifest contracts/auth/console-assertion-keyring.v1.json`。
   工具会：① 把公钥条目写进本地 `contracts/auth/console-assertion-keyring.v1.json`
   （提交前请审查）；② 把私钥一次性明文打印到终端。
3. **托管私钥**：持有 `credential.manage` 的操作员，用打印出的
   `credential_ref`/`secret_value`，调用既有的
   `credential.secret.upsert@1` Action（通用执行入口
   `POST /api/v1/actions/credential.secret.upsert/versions/1/execute`）把
   私钥存进 `XM_SECRET_ROOT` 目录。**本工具本身不接触密钥库**（见
   `cmd/console-assertion-keygen/main.go` 顶部注释：这是刻意的，一次性
   生成脚本没有可归因的会话，不该绕过 Action 直接写库）。
4. **同步清单文件**：把第 2 步生成的公钥条目提交进本仓库
   `contracts/auth/console-assertion-keyring.v1.json`（评审后合入），并把
   同一条目交给开票线，由他们提交进开票仓库自己的同名文件——两份必须
   字节一致（`purpose`/`protocol`/`key_id`/`public_key`/`fingerprint`/
   `valid_from`/`valid_until` 全部一致）。
5. **两侧配置核对**（逐字相同，任一处漂移都会让断言在验证阶段被拒）：
   - 本侧 `XM_INVOICE_CONSOLE_ASSERTION_ISSUER` = 开票侧
     `CONSOLE_ASSERTION_ISSUER`（控制台的精确 https 公开来源）。
   - 本侧 `XM_INVOICE_CONSOLE_ASSERTION_AUDIENCE` = 开票侧
     `CONSOLE_ASSERTION_AUDIENCE`（留空双方都用默认值
     `xingmang-console-assertion-v1` 即可）。
   - 本侧 `XM_INVOICE_CONSOLE_ASSERTION_KEY_REF` 的 `key_id` 段 = 清单里
     那条记录的 `key_id`。
6. **启用并部署**：设置
   `XM_INVOICE_CONSOLE_ASSERTION_ENABLED=true`、上面四个变量，重新部署
   `platform-api`；确认启动日志出现 `console_assertion_signer_loaded`
   且其 `key_id`/`fingerprint` 与清单记录一致。开票侧同一时刻或稍晚
   设置 `CONSOLE_ASSERTION_ENABLED=true`（该侧沿用 `OIDC_ADMIN_LOGIN_ENABLED=true`
   并存，不影响现有 Keycloak 路径）。
7. **金丝雀验证**：一名持有 `finance.read` 的管理员完成一次真实的控制台
   → Sub2API/NewAPI/治理三处任一"开票"页签的登录，确认：无弹窗、无跳转、
   立即呈现已登录的开票管理界面；控制台侧审计出现
   `staff.console_assertion.issue`（成功），开票侧审计出现对应的兑换成功
   事件，两侧 `nonce`（截断）/`kid` 可交叉核对。
8. 至此断言登录在生产可用，与 Keycloak OIDC 并存一整个发布周期（CR-0006
   正文要求）；下一步（XM-INV-KEYCLOAK-RETIRE）需要产品负责人另行放行，
   不在本片范围。
