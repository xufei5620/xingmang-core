# XM-USERS-REAL-EVIDENCE0 · 用户详情 real 接入的证据采集工具与审批文书

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-USERS-REAL-EVIDENCE0`
- base: `release/v0.1-launch`（`22257de`）
- worktree: `K:/星芒统一控制平台/wt-xmUSEREV0`

## summary

`docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md` §0/§10 冻结
了三个独立审批事件（`SUB2_REAL_APPROVAL`/`NEWAPI_REAL_APPROVAL`/
`REQLOG_USERREF_APPROVAL`），对应 Task 4/5/8（Sub2API real GetUser、NewAPI
real GetUser、reqlog 稳定 UserRef）；`docs/handoffs/slices/XM-USERS-V2-real-detail.md`
已核实这三个 Task 今天都硬阻塞在"缺证据、缺人工审批"上，且不可由 AI 自行补齐
（宪法第 9、10、18、20 条）。本片交付的是让这三份审批"除了接触真实凭据/真实
数据、以及审阅人签字之外，不需要任何人再做额外工程"的工具与文书：

1. **`cmd/evidence-capture`**：一个新的 Go 命令，两个子命令。
   - `capture --platform sub2api|newapi`：给定一个**已经登记在平台里的**
     CredentialRef（永不接受命令行明文 token），对该平台执行设计文档 §10
     点名的只读探测——版本端点 + 用户列表页——套用脱敏规则后写出
     `docs/evidence/users-real/<platform>/<timestamp>/`：`users_page.redacted.json`
     （与将来 `connectors/platformusers/testdata/<platform>/` 需要的 fixture
     同构，可直接复制过去）、`user_detail.redacted.json`（从列表页第一条
     记录提取的单用户样本——两平台的用户列表都没有独立的按 ID 查询端点，
     这一点已核对 `EV-2026-08-27-{sub2api,newapi}-read-survey.md` 与
     `docs/superpowers/plans/2026-08-28-platform-user-read-v2.md` Task 4/5
     的 `parseSub2UsersPage`/`parseNewAPIUsersPage` 均以"页"为解析单位，
     确认这不是本片遗漏，而是两个上游本来就是这个形状）、`version.json`、
     `request-log.json`（每次请求的方法/路径/状态码/耗时，不含响应正文）、
     `SHA256SUMS`、`README.md`。`--dry-run` 零配置打印固定请求计划。
   - `reqlog --data-dir <本地副本> --tokenmap <本地副本>`：读一份从服务器
     复制下来的 `index.jsonl`（按天分目录）与 `tokenmap.json` 本地文件
     （不连接任何服务器、不发起任何网络请求），产出 retention/cursor 证据、
     未关联记录计数、以及一份脱敏后的 index 抽样（`index_sample.redacted.jsonl`、
     `tokenmap_shape.redacted.json`、`retention_and_association.json`）。
     **头条发现**（写进了证据 README 与审批单）：`tokenmap.json` 当前的
     `map[string]string`（前缀→"用户名/邮箱@source"）形状**没有位置容纳
     上游 user id**——`cmd/reqlog-recorder/tokenmap.go` 的导出 SQL 只
     `SELECT` 了 `username`/`email`，虽已 `JOIN` 了 `u.id` 却从未选出来。
     这是本工具对 schema 结构的程序化核实，与 `XM-USERS-V2-real-detail.md`
     纯读源码得出的结论一致，但换了一种独立的验证方式重新确认了它——
     REQLOG_USERREF_APPROVAL 因此被写成必须先在"窄/宽"两种范围里明确选择
     （见该审批单）。
2. **三份审批文书** `docs/approvals/{SUB2_REAL,NEWAPI_REAL,REQLOG_USERREF}_APPROVAL.md`：
   预填了设计文档要求的每一项证据字段（作为占位符，工具产出证据后填入
   真实值）、逐条对照设计文档 §10 证据门的 checklist、审阅人 checklist、
   带签名行的决定区块（格式仿照 `docs/handoffs/CODEX-SPRINT-2026-08-29.md`
   §7.2 已经走过的 `DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL` 记录格式）。
3. **`docs/runbooks/USERS-REAL-APPROVAL.md`**：验收线在服务器上怎么跑采集
   （凭据在哪、reqlog 本地文件从服务器哪里复制）、审阅人怎么核验哈希、以及
   ——这是本片核实过程中发现的一处需要澄清给团队的地方——**审批实际上怎么
   让门禁生效**（见下方"重要发现"）。

本片**没有**实现 Task 4/5/8 本身，**没有**改动任何现有 gate、连接器、契约或
前端代码，也从未接触任何真实凭据、真实上游实例或真实 reqlog 数据——所有测试
都用录制的合成 fixture（`example.invalid` 邮箱、`u_10241` 一类的示例 ID，取自
设计计划文档自己的测试片段），不联网。

## 重要发现：Task 4/5/8 今天没有对应的运行期开关

团队指派说明要求本 runbook 说明"审批怎么让门禁生效（写出 Task 4/5/8 检查的
确切环境变量/配置项）"。核实后发现**这个前提本身需要修正**：Task 4/5/8 描述
的 real reader 代码今天完全不存在——`connectors/platformusers` 只有
`fake_v2.go` 实现 v2 GetUser/DailyUsage/KeyMetadata，没有
`sub2api_v2.go`/`newapi_v2.go`；`connectors/reqlog` 的 `RequestLogSummary`
也没有设计文档 §6.1 描述的 `User *UserRef` 字段（均已由
`docs/handoffs/slices/XM-USERS-V2-real-detail.md` 核实，本片重新核对过一次，
结论一致）。**因此今天不存在任何环境变量或数据库配置项能把这三项能力"打开"
——不是被一个开关关掉了，是实现它的代码还没被写出来。**

容易被误认成"就是这个开关"的是 `XM_PLATFORM_USERS_MODE`（+
`core.connector_config` 表，经 `connector.config.set@1` Action 覆盖）——但
`cmd/platform-api/platformusers.go` 的 `dynamicUsersClient` 明确把
`GetUser`/`DailyUsage`/`ListKeyMetadata` 等 v2 方法**转发到 fake 端**，不论
这个开关是 real 还是 fake（原注释："本任务明确不实装 real 端 v2……那道闸只
针对本片新加的 `ListUsers` 纪律"）。也就是说，即使某平台已经切到 real，用户
详情页的 v2 面板此刻依然是 Fake 数据。

据此，`docs/runbooks/USERS-REAL-APPROVAL.md` 第 4 节把"门禁怎么生效"改写成
了实际情况：门禁是**代码审查时点**，不是运行期布尔值——实现 Task 4/5/8 的
PR 必须在提交信息/Handoff 里引用对应审批文件与证据路径/哈希，验收线合入前
核对一致，不一致就 `REJECT`（`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.1
的验收信号约定）。这与 `DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL` 已经走过
的路径一致——那两项同样没有运行期开关，是验收线核对 §7.2 的记录。若团队认为
将来确实需要一个运行期 kill switch（比如上线后发现某字段解析有问题，想不经
重新部署就临时关掉），这是 Task 4/5/8 实现时需要一并设计的内容，本片没有替
它决定。

## 设计取舍（供审阅时参考）

- **"one user detail" 不是第二次网络请求**：两个上游的用户管理 API 都只有
  列表端点，没有独立的按 ID 查询端点（Sub2API 唯一的 per-id admin 路由
  `/api/v1/admin/users/:id/usage` 已被 `EV-2026-08-27-sub2api-read-survey.md`
  证实是写死的 mock）。`user_detail.redacted.json` 因此是从 `users_page`
  响应里提取的第一条记录，而不是对一个猜测出来的端点发起的第二次请求——
  这与设计计划文档 Task 4/5 里 `parseSub2UsersPage`/`parseNewAPIUsersPage`
  都以"页"为输入单位是同一个判断，不是本片自行发明的简化。
- **固定请求计划而非可配置路径**：`cmd/evidence-capture` 没有任何
  `--path` 一类的 flag；每个平台能发出的请求写死在 `plan.go` 的 `planFor`
  里（每平台恰好两条）。这比"传一个路径 allowlist 校验"更强的地方在于：
  一个从未被写进源码的路径，物理上无法被这个工具构造出来，不需要依赖校验
  逻辑本身没有漏洞。
- **脱敏优先用允许清单，不用禁止清单**：`sub2api.go`/`newapi.go` 的
  `ItemPolicy` 只列出"允许保留（并按规则改写）的字段名"；任何不在清单里的
  字段一律丢弃，只在证据 README 里记录字段名（不记录值）。上游未来加一个
  新字段（哪怕叫 `full_key`），默认结果是"消失，字段名进丢弃清单"，而不是
  "原样进入脱敏样本，指望某条正则去拦"。
- **ID/用户名用一次性哈希而不是平台已有的 canonical 编码**：
  `connectors/platformusers/userref.go` 的 `EncodeUserIDSegment` 是十六进制
  编码，可逆——它是给路由用的，不是脱敏。本片自己实现了
  `hashUserID`/`hashName`（sha256 + 每次运行随机生成的 salt），并在代码注释
  里说明了为什么：真实 ID 的取值空间很小（`u_10241`、`20031` 这类），不加盐
  的哈希是可以被暴力枚举撞破的编码，不是真正的单向函数；加了 salt 之后，
  它的目的也只是"让证据目录里的样本不会被随手认出是谁"，不是抵御一个蓄意
  攻击者——README 与 runbook 都如实这样描述，没有夸大成"密码学级不可逆"。
- **金额脱敏保留"形状"而不是简单地全部清零**：`redactAmountField` 把每一位
  数字换成 `0`（JSON 禁止多位数字前导零时，最高位换成 `1`），但保留正负号、
  小数点位置、以及上游到底是用 JSON 字符串还是裸数字表达金额——后者正是
  Task 4/5 的证据门要求说清楚的"金额单位/scale"，删掉数值本身不等于删掉
  这个形状信息。这一步踩过一个真实 bug：最初直接把所有数字位清零会产出
  `"0000000"` 这种非法的 JSON 数字字面量（多位前导零），被
  `TestRedactNewAPIUsersPageFromFixture` 当场抓到（测试先失败后修复，细节
  见 `redact.go` 的 `scrubBareJSONNumber` 注释）。
- **写盘前的独立最终扫描**：逐字段脱敏规则之外，`scanForbidden`
  （`redact.go`）在任何脱敏 JSON 落盘前，独立扫描最终文本里是否残留邮箱
  形状字符串或 `secret`/`token`/`password` 等禁词；命中就拒绝写出整份证据。
  这是设计文档 §8 明确要求的"只做 Go struct 反射不算通过"那一层——
  `redactSub2APIUsersPage`/`redactNewAPIUsersPage`/`redactIndexRow` 的每个
  测试都额外断言了这一层不会误伤正常的脱敏输出（`TestScanForbiddenAllowsMaskedEmail`）。
- **reqlog 子命令绝不读正文**：只解析 `index.jsonl`（`reqlogformat.Record`，
  元数据），从不打开对应的 `<id>.json.gz`（完整请求/响应正文，contract.go
  自己的注释"摘要不含正文"）；索引行里可能携带正文片段的 `preview`/
  `end_note` 两个字段被无条件丢弃（连字段名都不出现在 `redactedIndexRow`
  这个 Go struct 定义里——不是运行时判断丢弃，是类型上就没有这个字段）。

## files_changed

新增，未修改任何既有文件：

- `cmd/evidence-capture/main.go`、`main_test.go` —— 子命令分发（`capture`/
  `reqlog`）与 `-h`/`help`。
- `cmd/evidence-capture/plan.go`、`plan_test.go` —— 每平台固定请求计划、
  已证实 mock/写端点的机械拒绝清单（`mustNotBeMockRoute`）。
- `cmd/evidence-capture/redact.go`、`redact_test.go` —— 一次性哈希、金额
  形状脱敏、`ItemPolicy` 允许清单引擎、写盘前最终安全扫描
  （`scanForbidden`）。
- `cmd/evidence-capture/sub2api.go`、`sub2api_test.go` —— Sub2API 响应
  envelope 解析与脱敏（版本端点 + 用户列表页）。
- `cmd/evidence-capture/newapi.go`、`newapi_test.go` —— NewAPI 响应
  envelope 解析与脱敏（含 `quota_per_unit`、软删除 `DeletedAt` 形态）。
- `cmd/evidence-capture/httpfetch.go`、`httpfetch_test.go` —— 只读 HTTP
  探测层，复用 `internal/platform/connector.NewReadOnlyClientWithBase`；
  423（AdminComplianceGuard）单独分类，从不发送合规确认 POST。
- `cmd/evidence-capture/capture.go`、`capture_test.go` —— `capture` 子命令
  flag 解析、凭据解析（`internal/platform/secrets`，文件型 Provider，永不
  接受命令行明文 token）、编排与证据落盘。
- `cmd/evidence-capture/evidence.go` —— 证据目录写入器（`SHA256SUMS` 生成、
  防止覆盖已有目录）。
- `cmd/evidence-capture/readme.go` —— 两个子命令共用的证据 `README.md`
  渲染。
- `cmd/evidence-capture/reqlog.go`、`reqlog_test.go` —— `reqlog` 子命令：
  本地文件读取、retention/cursor/关联证据计算、脱敏抽样。
- `cmd/evidence-capture/testdata/{sub2api,newapi}/*.sample.json` —— 单元
  测试用的合成上游响应 fixture（`example.invalid` 邮箱，非真实数据）。
- `cmd/evidence-capture/testdata/reqlog/{20260828,20260829}/index.jsonl`、
  `tokenmap.sample.json` —— 单元测试用的合成 reqlog 本地文件 fixture。
- `docs/approvals/SUB2_REAL_APPROVAL.md`、`NEWAPI_REAL_APPROVAL.md`、
  `REQLOG_USERREF_APPROVAL.md` —— 三份可执行审批单。
- `docs/runbooks/USERS-REAL-APPROVAL.md` —— 操作与审批生效说明。
- 本文件：`docs/handoffs/slices/XM-USERS-REAL-EVIDENCE0.md`。

## tests_run

以下命令均在 `K:/星芒统一控制平台/wt-xmUSEREV0` 内执行；`go test` 前先
`unset` 八个代理环境变量（`HTTP_PROXY`/`HTTPS_PROXY`/`http_proxy`/
`https_proxy`/`ALL_PROXY`/`all_proxy`/`NO_PROXY`/`no_proxy`）：

- `go build ./...` —— PASS（无输出）。
- `go vet ./...` —— PASS（无输出）。
- `gofmt -l .`（排除 `node_modules`）—— 只列出
  `internal/platform/httpapi/finance_test.go`，这是**改动前就存在、本片
  未碰**的既有问题（`docs/handoffs/slices/XM-USERS-REAL.md` 已经记录过
  同一条）；本片新增的全部文件（`cmd/evidence-capture/*.go`）均已 gofmt
  干净。
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）。
- `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test -p 1 -count=1 ./...`
  —— **PASS**：68 个 Go 包全绿，见下方"go test 全量结果"。
- `env ... go test -count=1 -v ./cmd/evidence-capture/...` —— PASS：8 个
  测试文件（`main_test.go`/`plan_test.go`/`redact_test.go`/`sub2api_test.go`/
  `newapi_test.go`/`httpfetch_test.go`/`capture_test.go`/`reqlog_test.go`）
  共 64 个顶层测试函数全部通过，全程无网络调用（`httptest` 回环 + 录制
  fixture）。
- `gitleaks git --redact --no-banner --log-opts="release/v0.1-launch..HEAD"`
  —— PASS（"6 commits scanned"，"no leaks found"）。**过程中确实命中过**：
  第一轮扫描在 `cmd/evidence-capture/testdata/reqlog/` 的合成 token 前缀
  样本（形如 `sk-abc1234567890123`）上命中 5 条 `generic-api-key` 误报——
  这些字符串本身就是刻意仿造"看起来像真实 token"的合成占位符，triggered
  是预期内的（宪法未禁止合成样本触发扫描器，只是要求处理方式正确）。按既
  有解法处理：不加 gitleaks allowlist（`scripts/check-governance.sh` 本身
  就禁止任何 `gitleaks*.toml`/`.gitleaksignore` 文件入库），改用更明显是
  合成占位符的字符串（`test-prefix-{newapi,sub2api}-{one,two}`，逐个候选
  先用 `gitleaks detect` 单独验证过不再触发），再用 `git reset --soft` 回
  到 base 提交、重新按相同的六个逻辑分组提交（未使用 `rebase -i`）——重写
  后的历史里，这些字符串在任何一次提交、任何一个 diff hunk 里都不出现过。
- `git diff --check release/v0.1-launch..HEAD` —— PASS（无输出，无空白
  错误）。

### go test 全量结果

`internal/platform/audit/archive` 的 `TestS3PutIfAbsentUsesConditionalSSES3KnownSizeAndReadback`
在两次全量跑（重写历史前后各一次）中都短暂出现过一次 `dial tcp
127.0.0.1:<port>: connectex` 回环连接失败——该包不在本片改动范围内（本片
从未触碰 `internal/platform/audit`），单独重跑该包（`go test -v
./internal/platform/audit/archive/...`）两次都全绿，判定为本机 Windows
回环 TLS/S3 mock 端口绑定的环境性抖动，与 `capture_test.go` 里用
`httptest.NewTLSServer` 的两个测试在同一轮全量跑里也短暂抖动过一次、单独
重跑同样全绿是同一类现象（团队指派说明已预先点名"loopback httptest
flakes are environmental"）。**最终确认跑**（重写历史、修正 gitleaks 命中
之后的最后一次 `go test -p 1 -count=1 ./...`）68 个包**全部一次性通过，
零失败**（`EXIT_CODE=0`，日志里没有任何 `FAIL` 行）。

## not_run

- 未连接任何真实 Sub2API/NewAPI 实例、未读取任何真实 reqlog 数据——
  `cmd/evidence-capture` 的全部测试只对 `httptest` 回环服务器与录制的合成
  fixture 运行；没有、也不能在本次任务的权限与网络边界内做"对真实实例跑一次
  证据采集"的验证。
- 未在真实 Sub2API 实例上验证 423（AdminComplianceGuard）分支的端到端行为
  ——单元测试用 `httptest` 服务器模拟 423 响应验证了分类与文案，但没有对
  一个真实的、未完成合规确认的 Sub2API 账号验证过。
- 未验证 `--secret-root` 指向真实 `XM_SECRET_ROOT` 共享卷（容器环境）时的
  行为——`TestDefaultSecretsProviderResolvesFromFile` 验证的是文件型
  Provider 本身的读取逻辑，用的是本机临时目录，不是容器挂载卷。
- 未做 pnpm/前端相关的任何门禁——本片不改动任何前端代码、Go 依赖版本或
  `contracts/`，`pnpm install`/`typecheck`/`test`/Storybook 构建均未运行，
  预期结果不受影响。
- 未实现 Task 4/5/8 本身，未改动任何现有 gate、连接器、契约或前端代码
  ——这是派工说明明确划定的范围外（"不实现 Task 4/5/8 本身，不改任何
  gate"）。

## risks

- **"one user detail 来自列表页第一条记录"是本片的判断，不是设计文档逐字
  写出的规则**——设计文档与实施计划都没有明确说"若无独立 detail 端点就从
  列表页取一条"，这是本片核对两个上游的真实端点清单（
  `EV-2026-08-27-{sub2api,newapi}-read-survey.md`）与实施计划 Task 4/5 的
  parser 签名（都吃"page body"）后做出的推断。审阅人若认为这个判断有误，
  需要在 Task 4/5 实现阶段重新核实，而不是假设本片已经把这件事钉死。
- **`cmd/evidence-capture reqlog` 的 retention/cursor 结论质量取决于喂给它
  的本地文件是否是当前生产数据的忠实副本**——本工具本身无法验证一份
  `index.jsonl`/`tokenmap.json` 副本是不是刚从服务器复制下来的最新状态，
  这一点需要验收线在跑采集命令时自行保证（runbook 第 1.2 节已经写明"只要求
  是只读副本"，但没有、也无法机械校验"新鲜度"）。
- **金额/quota 脱敏的"数字位归零"规则会让 `0` 与"数字全零"两种真实情况在
  脱敏后长得一样**——这是脱敏本身必然的信息损失，不是 bug；README 与本
  文件都如实说明了这一点，审阅证据时不应该从脱敏样本反推真实数值分布。
- **本片没有对"`--sample-size` 上限 20"这个数字做任何形式的产品/安全评审
  ——它是本片自行选定的、认为"足够看清形状又不像批量导出"的数值，不是
  设计文档或团队指派明确给出的数字。如果审阅人认为这个上限应该更严格
  （比如 3）或可以更宽松，这是一个可以在代码评审时直接调整的常量
  （`plan.go` 的 `maxSampleSize`），不需要重新设计。
- **NewAPI 的 `GET /api/status` 不需要鉴权**（`connectors/newapi/upstream.go`
  的既有注释）——本工具仍然对它发送凭据头，这是为了让请求路径在两个平台间
  保持一致、简化实现，不是因为这个端点需要鉴权；这个决定对安全性没有
  负面影响（多发一个头不会让一个公开端点变得更敏感），只是提出来避免审阅
  时误以为这是一个疏漏。

## follow_ups

- **Task 4/5/8 的实际实现**（`connectors/platformusers/sub2api_v2.go`/
  `newapi_v2.go`、`connectors/reqlog` 的 `UserRef` 字段）——本片明确不做；
  需要先有对应审批单签字，且 REQLOG_USERREF_APPROVAL 还需要产品与安全先
  在"窄/宽"两种范围里做出选择（见该审批单"头条发现"一节）。
- **若选择 REQLOG_USERREF_APPROVAL 的"宽"范围**（改
  `cmd/reqlog-recorder/tokenmap.go` 的导出 SQL 以携带上游 user id）——
  该链路本身是已知架构债务（`docker exec ... psql` 直连数据库，绕开
  Connector/CredentialRef，见 `docs/handoffs/slices/XM-REQLOG-MERGE.md`
  风险清单），审批单里已经建议先立独立 ADR/Change Request，不在本片或未来
  Task 8 里顺带处理。
- **首次真实运行前，建议先在测试/预发实例上跑一次 `capture`**（若有这样的
  环境），确认真实响应形状与 `connectors/platformusers/upstream.go` 已核对
  的字段完全一致——本片的 parser 是照着那份已核对的源码写的，但从未对着
  一次真实观测验证过（与 `docs/handoffs/slices/XM-USERS-REAL.md` 的同一条
  免责声明）。
- **`docs/evidence/EV-2026-08-28-platformusers-{sub2api,newapi}-v2-shape.md`
  两份叙事性证据文档**（实施计划 Task 4/5 点名要求的文件）仍需要人工在拿到
  真实证据后撰写——`cmd/evidence-capture` 产出的是原始证据材料，不是这两份
  叙事文档本身；`README.md` 已经在"这份证据喂给谁"一节写明了目标路径。
- 若团队认为 Task 4/5/8 需要一个运行期 kill switch（不经重新部署即可临时
  关闭 real v2 GetUser），需要在实现那三个 Task 时一并设计——本片在
  `docs/runbooks/USERS-REAL-APPROVAL.md` 第 4 节末尾提了这一点，但没有替
  它做设计决定。
