# XM-USERS-V2-REAL · Sub2API/NewAPI real GetUser v2（Task 4/5）

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-USERS-V2-REAL`
- commit: `1b54e09`
- base: `release/v0.1-launch`（`c93c89e`，含 SUB2_REAL/NEWAPI_REAL/REQLOG_USERREF
  三份审批的合入）
- worktree: `K:/星芒统一控制平台/acceptance/wt-usersreal`

## 审批与证据引用（宪法第 19 条要求的回写）

本片实现的授权来自两份独立、已签字的审批单——**均不互相推定，也不推定自
CORE_APPROVAL**：

| 事件 | 状态 | 授权范围 | 证据目录 | SHA256SUMS |
|---|---|---|---|---|
| `SUB2_REAL_APPROVAL` | APPROVED 2026-09-03 | Task 4：`connectors/platformusers/sub2api_v2.go`，仅 `platformusers.user.detail_read` | `docs/evidence/users-real/sub2api/20260902T054723Z/` | `5b333bbe462821b04122ee891c5ae9f66bcbc285450bfbb8c665e9824f81c191`（users_page `02013b4f7761cbd6…`、user_detail `d250d91f403b4dba…`、version `8f3991ad282def8b…`） |
| `NEWAPI_REAL_APPROVAL` | APPROVED 2026-09-03 | Task 5：`connectors/platformusers/newapi_v2.go`，仅 `platformusers.user.detail_read` | `docs/evidence/users-real/newapi/20260902T054724Z/` | `a7fb8e640a3377287a348329d9551204a43e4ff3bce31e1c2cc18eca11f64e8b`（users_page `58b5701b7230bf6a…`、user_detail `2e344cfed070d83e…`、version `19eb35ae14b75c70…`） |

两份审批都附带产品负责人的同一条附加约束（2026-09-03 批准时提出）：**不得
改动 Sub2API 与 NewAPI 的源码**，只能用它们既有的只读 HTTP API。本片没有
修改 `K:/sub2api-src`、`K:/newapi-src` 或任何上游仓库，也没有创建任何数据库
角色或迁移；两个 real reader 都只调用既有只读 HTTP 端点（`GET
/api/v1/admin/users`、`GET /api/user/`、`GET /api/status`），凭据只经
`CredentialRef`/`secrets.SecretProvider` 解析。

字段形状核对的叙述性证据文档：
`docs/evidence/EV-2026-08-28-platformusers-sub2api-v2-shape.md`、
`docs/evidence/EV-2026-08-28-platformusers-newapi-v2-shape.md`。

不在本片范围（未获批、未实现）：DailyUsage/Key metadata 的 real reader
（需要独立的 `DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL` 真实数据面审批）、
reqlog 稳定 UserRef（Task 8，需要 `REQLOG_USERREF_APPROVAL` 与 CR-0008）、
invoice/payment（未过门）。

## summary

- **Task 4（Sub2API）**：`connectors/platformusers/sub2api_v2.go` 新增
  `RealClient.getSub2APIUser`——两个上游都没有原生的按 ID 精确查询端点
  （批准证据只覆盖列表端点），因此对 `GET /api/v1/admin/users` 的分页做
  完整扫描：命中即返回；翻完仍未命中返回 `ErrNotFound`；预算
  （`sub2apiMaxPages`，与 v1 `ListUsers` 一致）耗尽仍有更多页返回
  `ErrLookupIncomplete`。只映射批准的六个字段（`id`/`email`/`username`/
  `status`/`balance`/`last_active_at`），金额走定点解析（`decimalToMinorUnits`
  /`rescaleMinorUnits`），全程不经 `float64`。`sub2V2AllowedPath` 是一个允许
  清单守卫，只放行裸列表端点，永久拒绝已被
  `EV-2026-08-27-sub2api-read-survey.md` 证实是 mock 的
  `/api/v1/admin/users/:id/usage` 等 5 个端点。AdminComplianceGuard 的 423
  被翻译成有类型标记的 `ErrSub2APIComplianceConfirmationRequired`；该方法
  结构上只发 `GET`（复用 `RealClient.get`），不可能发出
  `POST /admin/compliance/accept`。
- **Task 5（NewAPI）**：`connectors/platformusers/newapi_v2.go` 新增
  `RealClient.getNewAPIUser`，同样对 `GET /api/user/` 做完整分页扫描
  （预算 `newapiMaxPages`）。每次 `GetUser` 调用都重新调用
  `newapiQuotaPerUnit` 现读一次 `/api/status`，绝不跨调用缓存或硬编码——
  批准记录的观测值 500000 只是采集那一刻的快照。软删除用户
  （`DeletedAt` 非 `null`）视为不存在。只映射批准的八个字段（`id`/
  `username`/`display_name`/`email`/`status`/`quota`/`last_login_at`/
  `DeletedAt`）；`password`/`original_password`/`verification_code`（批准
  记录里的 dropped 字段名之一，值从未离开采集进程）一个字节都不解析——
  `newapiUserItem` 结构体压根没有为它们声明字段。`newAPIV2AllowedPath` 用
  允许清单（只放行 `/api/user/`、`/api/status`）机械覆盖既有的 GET
  写端点黑名单（`/api/user/token`、`/api/user/aff`、
  `/api/user/epay/notify` 等）。
- **共享 dispatch**：`connectors/platformusers/client.go` 新增
  `RealClient.GetUser`（校验 Ref、归一化 Period、按 `cfg.Source` 分发）与
  `RealClient.V2Capabilities`（只声明 `platformusers.user.detail_read`，
  两个来源相同——real 端目前只对两者都实现了这一项）。
- **contracttest**：`connectors/platformusers/contracttest/suite.go` 的
  `assertV2Capabilities` 新增对 `detail_read` capability 的通用断言——用一个
  任何实现都不可能真的拥有的哨兵 ID 探测"能力布线正确 + 未知 ID 走
  `ErrNotFound`/`ErrLookupIncomplete`"，不假设某个具体 ID 在所有固件里都
  存在（与 `assertUnknownSourceRejected` 用"查无此平台"同一个思路）。
- **wiring**：`cmd/platform-api/platformusers.go` 的
  `dynamicUsersClient.GetUser` 从"生效模式非 fake 时统一 not_supported"
  改成按生效模式转发（real → `connusers.RealClient.GetUser`，fake →
  `FakeClient.GetUser`），与 `ListUsers` 同一套 `resolve()` 决策表；并补上
  `ListUsers` 已有、GetUser 此前没有的 production+fake 闸（生产环境生效
  模式仍是 fake 时拒绝，宪法 12 条——real 首次真正下场之后，这道闸如果不
  补，一个在生产被误配成 fake 的连接器会把演示样本悄悄当成真实客户资料
  展示在用户详情页）。`DailyUsage`/`ListKeyMetadata` 的转发逻辑未改动
  （生效模式非 fake 时继续 not_supported）。

## 与 plan 示意代码的一处有意偏离

`docs/superpowers/plans/2026-08-28-platform-user-read-v2.md` Task 5 给出的
`parseNewAPIUsersPage` 示意签名是
`func parseNewAPIUsersPage(body []byte, ref UserRef, observedAt time.Time) (UserDetail, error)`，
不含换算基数参数。但 NEWAPI_REAL_APPROVAL 的证据表明确写"quota_per_unit……
运行期可变，这里记录的只是采集那一刻的观测值，不是永久基数，real reader
必须每次现读，不得硬编码"——一个不接收这个值的"纯"解析函数只剩两条路：
要么在函数体内悄悄写死一个默认基数（直接违反批准的硬约束），要么把
`Balance` 留成 `UnknownAmount` 指望调用方事后改写（那样"做了金额换算"这件
事会从类型签名里消失，评审时容易被忽略）。本片把签名改成
`parseNewAPIUsersPage(body []byte, ref UserRef, quotaPerUnit int64, observedAt time.Time) (UserDetail, error)`，
在函数定义处的注释里说明了这个理由；生产路径（`getNewAPIUser`）每次调用都
现读 `/api/status` 后把结果传入，测试路径（`newapi_v2_test.go`）显式喂值。

`cmd/platform-api/main.go` 与 `contracttest/v2_suite.go`：两份 Task 4/5
计划里列出的"Modify"文件里，`main.go` 最终没有改动——核实后发现
`XM-USERS-V2-real-detail` 那一片（2026-09-02 合入）已经把
`PlatformUserDetails`/`PlatformUserDailyUsage`/`PlatformUserKeys` 三个
Handler 依赖全部接到了 `httpapi.NewRouter` 的调用点（`main.go:425-428`），
本片不需要再改；实际改动的是 `contracttest/suite.go`（计划文本写的文件名
是 `v2_suite.go`，仓库里这部分逻辑实际就在 `suite.go` 一个文件里，不存在
`v2_suite.go`）。

## files_changed

- `connectors/platformusers/sub2api_v2.go`（新增）
- `connectors/platformusers/sub2api_v2_test.go`（新增）
- `connectors/platformusers/newapi_v2.go`（新增）
- `connectors/platformusers/newapi_v2_test.go`（新增）
- `connectors/platformusers/testdata/sub2api/users_page.redacted.json`（新增，
  证据目录 `docs/evidence/users-real/sub2api/20260902T054723Z/` 的逐字节
  拷贝）
- `connectors/platformusers/testdata/sub2api/user_detail.redacted.json`（新增，同上）
- `connectors/platformusers/testdata/newapi/users_page.redacted.json`（新增，
  证据目录 `docs/evidence/users-real/newapi/20260902T054724Z/` 的逐字节
  拷贝）
- `connectors/platformusers/testdata/newapi/user_detail.redacted.json`（新增，同上）
- `connectors/platformusers/client.go`（新增 `RealClient.GetUser`/`V2Capabilities`）
- `connectors/platformusers/contracttest/suite.go`（新增 detail_read 通用断言）
- `cmd/platform-api/platformusers.go`（`dynamicUsersClient.GetUser` 改为按
  生效模式转发，补 production+fake 闸；更新相关注释）
- `cmd/platform-api/platformusers_test.go`（拆分/更新 real 模式下 GetUser
  与 DailyUsage/KeyMetadata 的期望；新增生产环境 fake 闸测试）
- `docs/evidence/EV-2026-08-28-platformusers-sub2api-v2-shape.md`（新增）
- `docs/evidence/EV-2026-08-28-platformusers-newapi-v2-shape.md`（新增）
- `docs/handoffs/slices/XM-USERS-V2-REAL.md`（本文件）

## tests_run

以下命令均在本 worktree（`K:/星芒统一控制平台/acceptance/wt-usersreal`）
内串行执行：

- `go build ./...` — PASS（无输出）
- `go vet ./...` — PASS（无输出）
- `docker exec invoice-test-pg psql -U postgres -c "CREATE DATABASE xm_test_usersreal"` — PASS；
  `go run ./cmd/migrate -database "postgres://postgres:test@127.0.0.1:55432/xm_test_usersreal?sslmode=disable" -path db/migrations up` — PASS（"迁移完成"）
- `XM_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/xm_test_usersreal?sslmode=disable env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test -p 1 -count=1 ./...` — PASS（71 个包，51 个含测试的包全部 `ok`，其余 20 个 `[no test files]`，0 个 FAIL/panic）
- 新增/改动测试专项复核（均含在上面的全量结果里）：
  - `go test ./connectors/platformusers/... -run "TestSub2APIV2|TestNewAPIV2|TestRealClientSub2APIGetUser|TestRealClientNewAPIGetUser" -v` — PASS（20 个用例：精确 ID/定点金额、六/八字段边界、mock 路由永久拒绝、GET 写端点黑名单、跨页扫描、翻完未命中→`ErrNotFound`、预算耗尽→`ErrLookupIncomplete`、423 合规门有类型标记且从不发 POST、软删除视为不存在、`quota_per_unit` 逐次现读、ctx 取消传播、跨平台 Ref 拒绝、capability 不继承 daily/key）
  - `go test ./connectors/platformusers/... -run "TestRealClientSatisfiesContract|TestFakeV2" -v` — PASS（既有 contracttest 套件在 real 客户端新增 `V2Capabilities` 后仍全绿，新增的 detail_read 通用断言在两个真实固件上都通过）
  - `go test ./cmd/platform-api/... -run "TestDynamicUsersClient|TestBuildPlatformUsers" -v` — PASS（含新增/改写的三条：real 模式下 GetUser 真的尝试构造 RealClient 而非直接 not_supported、real 模式下 DailyUsage/KeyMetadata 仍 not_supported、生产环境 fake 闸对 GetUser 同样生效）
- `bash scripts/check-governance.sh` — PASS（exit 0，无输出）
- `gitleaks git --no-banner --log-opts="c93c89e..HEAD" .` — PASS（1 commit scanned，~67.10 KB，no leaks found；脱敏测试固件未触发 generic-api-key 或其他规则）
- `gofmt -l .` — 仅报告 `internal/platform/httpapi/finance_test.go`
  一个**与本片无关的既有问题**：`git status --porcelain` 显示本片未改动
  该文件，且在基线提交 `c93c89e` 上单独跑 `gofmt -l` 同样命中——不在本片
  范围内修复，留给该文件所属的切片处理。
- 前端：本片未改动 `web/` 下任何文件，未运行 `pnpm run typecheck`/
  `pnpm run test`（按派工说明"仅当改动 web 时才跑"）。

## not_run

- 未接触任何真实 Sub2API/NewAPI 实例、真实凭据或生产系统；所有网络层
  测试都对 `httptest` 假上游发起，凭据均为
  `secret://.../...`/`test-only.invalid-token` 形状的占位符。
- 未运行浏览器/前端验证（本片未改动任何前端文件；`GET
  /platforms/{platform}/users/{canonicalUserId}` 的 HTTP 层已由 Task 3
  （`XM-C-USER0-impl`）的既有测试覆盖，本片只是让它在 `real` 模式下真的
  返回真实数据而不是 `not_supported`）。
- 未运行 `scripts/verify-real-mode.sh`：核对过该脚本全文，它检查的是
  `platform-worker` 指标同步（`.users.total`/`.users.balance` 等聚合数字），
  没有针对本片新增的用户详情 `GetUser` 端点的检查项，因此未修改也未运行它
  （派工说明的措辞是"if it has a users section expecting the real reader"，
  核实后确认没有）。

## risks

- **未对真实实例验证过**：与仓库里其它 real 客户端上线时的免责声明一致，
  两个 real reader 的正确性目前只由"对着源码逐字段核对出的脱敏证据固件"
  的契约测试保证，尚未对着真实上游跑过一次端到端读取。SUB2_REAL_APPROVAL
  的核对项已经接受了实例版本 `0.1.184` 相对普查矩阵上限 `0.1.183` 高一个
  patch 版；若后续观测到更新的版本，需要重新核对是否有新增/删除字段。
- **翻页预算耗尽会拖慢用户详情页**：若某个平台的用户量增长到
  `sub2apiMaxPages`/`newapiMaxPages` 页翻不完还找不到目标 ID，`GetUser`
  会在返回 `ErrLookupIncomplete`（HTTP 502，前端按可重试错误处理）之前
  发出满预算次数的上游请求（例如 Sub2API 20 次、NewAPI 50 次），对上游与
  平台自身都是一次不小的读取开销。这是"上游没有原生精确查询端点"这个
  已知限制的直接后果，不是本片引入的额外缺口；若未来两个上游任一方新增
  原生按 ID 查询端点并经普查证明非 mock，应该优先切过去。
- **423 合规门错误目前只在 Sub2API 侧有类型标记**：`ErrSub2APIComplianceConfirmationRequired`
  是本片新增的哨兵错误，NewAPI 没有对应机制（上游本身没有
  AdminComplianceGuard 这类合规门）。
- **contracttest 的 detail_read 探测只覆盖"未知 ID"这一条通用路径**：
  不同调用方（Fake 固定样本 vs. 各真实固件）拥有的用户集合互不相同，
  共享套件没有对"已知存在的 ID 命中"做通用断言；这条路径由各自的专属
  测试（`sub2api_v2_test.go`/`newapi_v2_test.go`）覆盖，不是共享契约测试
  的缺口，而是有意的设计取舍。

## follow_ups

- Task 6/7 的 real DailyUsage/Key metadata reader：需要各自独立的
  `DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL` 真实数据面审批与脱敏证据，
  不在本片范围（设计文档 §0/§12 明确"不影响 1~4/6~8"/"不影响 1~7"的独立
  阻断关系）。
- Task 8（reqlog 稳定 UserRef）：需要 `REQLOG_USERREF_APPROVAL` 与 reqlog
  真实证据，另需先解决 `cmd/reqlog-recorder/tokenmap.go` 现有的 `docker
  exec ... psql` 直连上游数据库机制是否适合继续承载"稳定身份关联"这一
  更高信任等级的数据（见 `docs/handoffs/slices/XM-USERS-V2-real-detail.md`
  的同一条 follow_up，本片未改动这一判断）。
- 若两个上游未来任一方新增原生按 ID 精确查询端点，且经普查证明不是
  mock，应该发起新的评审/审批取代目前的"翻页扫描到命中或耗尽"实现，
  以降低用户详情页的读取延迟与上游负载。
- `internal/platform/httpapi/finance_test.go` 的 gofmt 问题（本片未触碰，
  基线提交上同样存在）留给该文件所属的切片处理。
- 验收线合入前，请按 `docs/runbooks/USERS-REAL-APPROVAL.md`"审批如何生效"
  一节核对本文件引用的两份审批单文本与证据路径/哈希，与
  `docs/approvals/SUB2_REAL_APPROVAL.md`/`NEWAPI_REAL_APPROVAL.md` 已签字
  的内容逐字一致。
