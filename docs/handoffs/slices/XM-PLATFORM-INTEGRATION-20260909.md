# XM-PLATFORM-INTEGRATION-20260909 — 五条分支集成

- **status**: 待验收线合入
- **branch**: `ai/claude/XM-PLATFORM-INTEGRATION-20260909`
- **commit**: `9f1fe02`（集成分支 HEAD）
- **base**: `release/v0.1-launch` @ `66cd420`
- **worktree**: `K:/星芒统一控制平台/wt-XM-INTEGRATION`

## summary

把五条已完成、各自门禁全绿的分支合成一条，供验收线一次合入。五次
`git merge --no-ff`，三处文本冲突逐处解决，另有五处「文本合得上、语义合不上」
的问题在集成分支上修复（详见「冲突与取舍」第 4–8 条）。全量门禁在合并后重跑，
全绿；gitleaks 11 条 generic-api-key 与基线逐条相同，无新增。

**先读这一条**：这条分支带给验收线的不是五个切片，是 **34 个**。
详见「负责人必须知道的三件事」第 1 条。

## 五条分支与合并提交

按合并顺序（顺序不是随意的，见下）：

| # | 分支 | 分支头 | 合并提交 | 冲突 |
|---|---|---|---|---|
| 1 | `ai/claude/XM-I18N-LABELS` | `e4f7dcf` | `785b537` | 无 |
| 2 | `ai/claude/XM-CPA-TIMER-CALENDAR` | `fa1f661` | `eea42b2` | 无 |
| 3 | `ai/claude/XM-WORKBENCH-TRUTH` | `5d8b8a8` | `0503ef9` | 无 |
| 4 | `ai/claude/XM-CARD-VISIBILITY` | `c04d846` | `b1da59b` | 无 |
| 5 | `ai/claude/XM-OPS-TRUTH` | `eccb76a` | `9f1fe02` | 3 处 |

合并顺序里 I18N 必须第一，不是偏好问题：**`XM-I18N-LABELS` 是其余四条的共同
祖先**（`git merge-base --is-ancestor` 对四条都成立），四条分支实际是从 I18N
上分出去的，不是各自从 `release/v0.1-launch` 分出去的。先合它，后面四次
三方合并的 merge-base 都落在 `e4f7dcf`，冲突面最小。

相对 `release/v0.1-launch`：62 个提交，431 个文件，+72241 / -2241。

## 冲突与取舍

### 1. `internal/platform/alerts/rules.go` —— `Rules()` 开头（文本冲突）

两边改了同一段的不同事情：

- `XM-CARD-VISIBILITY` 把返回的切片字面量包成 `append(..., cardSyncRules(cfg)...)`；
- `XM-OPS-TRUTH` 在上面新增了 `hysteresisFor` 局部变量。

**取舍：两个都保留。** 先算 `hysteresisFor`，再 `return append([]Rule{`。

卡片规则必须留在这个切片里而不是另起一个导出函数：`RuleKeys()` 与
`KnownRuleKey()` 都从它派生，而 `KnownRuleKey` 正是静默窗口的存在性校验。
不在这个切片里的规则**静默不了**。

### 2. `internal/platform/jobs/client.go` —— `CardSyncEnabled` 分支（文本冲突）

这一处是同一行被改成了两个不同的样子，不是各自追加：

- `XM-CARD-VISIBILITY`：`river.AddWorker(workers, newCardSyncWorkerFor(cfg, pool))`
  —— 换构造函数，把观测（`WithObservations`）一并装上；
- `XM-OPS-TRUTH`：`addWorker(timeouts, workers, NewCardSyncWorker(...))`
  —— 换注册入口，注册时收集该 kind 的执行期限，用于算 maintenance 队列槽位。

**取舍：合成 `addWorker(timeouts, workers, newCardSyncWorkerFor(cfg, pool))`。**

两个改动正交，而且**任一半丢掉都不会报错**：

- 丢掉 `newCardSyncWorkerFor`，`cards.sync.failed` 告警在结构上不可能响——
  卡片同步现在对部分成功返回 nil，那条告警唯一的输入就是这个观测；
- 丢掉 `addWorker`，队列槽位按旧的慢任务个数算（`addWorker` 的注释写着它是
  本包注册 Worker 的**唯一**入口，直接调 `river.AddWorker` 会绕过收集）。

### 3. `docs/modules/notify/CATALOG.md` —— 规则条数那一节（文本冲突）

`XM-CARD-VISIBILITY` 写「九条规则」，`XM-OPS-TRUTH` 写「八条规则」并加了一句
「条数由 `TestCatalogListsEveryAlertRule` 从 `alerts.Rules()` 发现着对账，
不是手数出来的」。

**取舍：条数取真实数 9，那句话保留但改写成真的。**

- 真实数是 9：基线 8 条 + `XM-CARD-VISIBILITY` 的 `cards.sync.failed`。
  与 `TestRulesDeclareAllNineSpecFields`（独立地钉 `len(rules) != 9`）对得上。
- 那句话当时**并不成立**：`TestCatalogListsEveryAlertRule` 只逐条检查规则键
  在不在表里，标题里的条数是手写的。证据就在基线上——`e4f7dcf` 的标题写着
  「七条规则」，而 `alerts.Rules()` 当时已经有八条，测试全绿。

一句「这个数是机器对过的」而机器其实没对，比没有这句话更危险：它让下一个人
不再去核。所以没有照抄，而是补成真的，见下面第 4 条。

### 4. `internal/platform/notify/catalog_test.go`（集成分支新增断言）

给 `TestCatalogListsEveryAlertRule` 补上条数断言：标题「N 条规则」与正文
「取值只有这 N 个」两处都必须等于 `len(alerts.Rules())`，N 由代码渲染成中文
数字，测试里不写字面量。

逐条对账拦不住条数漂：条数写错时每一条规则都还在表里，逐条对账照样全绿。
这正是基线上「七条 vs 八条」能存活的原因。

### 5. `internal/platform/alerts/rules_cards.go`（语义冲突，编译期发现）

`cfg.ConsecutiveFailureThreshold` 已被 `XM-OPS-TRUTH` 删除。那是**替换不是
改名**（该分支自己的注释写明了）：R4 从「连续 K 轮」改成「最近 W 轮里失败
≥ K 轮」的滑动窗口，新字段叫 `ChronicFailureThreshold`，默认 9。

**取舍：卡片规则改接 `SyncFailedHysteresisRounds`（默认 3）。**

理由是语义，不是就近：`cards.sync.failed` 数的确实是**连续串**
（见 `cardSyncStreakOf`／`cardSyncStreak.open`），而替换之后仍然表示
「连续 N 轮失败」的字段是 `SyncFailedHysteresisRounds`，默认值 3 也与被删掉的
`DefaultConsecutiveFailureThreshold` 相同，判据与数字都没变。

**接到 `ChronicFailureThreshold` 上会编译通过**，然后把判据从「连续 3 轮」
悄悄变成「连续 9 轮」——量纲还对不上（那个数是窗口内次数，不是连续轮数），
这条告警会晚三倍才响，而且没有任何东西会报错。

同步改：`cards_rule_test.go` 六处 `DefaultConsecutiveFailureThreshold` →
`DefaultSyncFailedHysteresisRounds`。

### 6. `internal/platform/alerts/rules.go` —— `Evaluate` 的卡片分支（语义冲突）

`XM-OPS-TRUTH` 把 `Evaluate` 的返回改成了 `EvaluateResult` 值，而
`XM-CARD-VISIBILITY` 插入的错误分支还写着 `return nil, cardErr`。
改成 `return res, cardErr`，与相邻 `versionChangeFinding` 的错误分支同形——
出错也要把已经数出来的计数带回去。

### 7. `internal/platform/jobs/card_sync_visibility_test.go`（语义冲突）

两条分支各加了一个同名 `logLines` 测试辅助函数，一个收 `*bytes.Buffer`，
一个收 `string`，同包内重名编译不过。保留 `XM-OPS-TRUTH` 在
`effective_mode_test.go` 里的那个（入参更通用，`logEvents` 建在它上面），
本片两个调用点改传 `buf.String()`。

### 8. `docs/modules/alerts/README.md`（语义冲突，测试期发现）

`XM-OPS-TRUTH` 新增的 `TestEveryRuleHasARowInBothDocs` 要求每条规则在
`alerts/README.md` 与 `notify/CATALOG.md` **都**有行，而 `cards.sync.failed`
只写进了 CATALOG。补上 README 的行（数据来源、条件、持续时间、恢复条件、
去重键，全部照 `rules_cards.go` 的声明填）。

这是本次最典型的一处「两条分支各自门禁全绿、合起来才红」：那条测试当时只在
`XM-OPS-TRUTH` 上，那条规则当时只在 `XM-CARD-VISIBILITY` 上，两边都没机会红。

顺带修正 CATALOG 里 `cards.sync.failed` 那行的一句话：原文写「阈值与
『同步连续失败』同一个」，而「同步连续失败」已被 `XM-OPS-TRUTH` 改名为
「同步长期失败」且换成了滑动窗口判据。改成「与『指标同步失败』的连续轮数
同一个，默认 3 轮」，与第 5 条的取舍一致。

## tests_run（全量门禁，实测时间）

测试库：`bash scripts/dev/worktree-testdb.sh` 建 `xm_test_wt_xm_integration`，
迁移全量应用成功（7 条新迁移一次过，无缺号）。

| 门禁 | 开始 UTC | 结束 UTC | 耗时 | 结果 |
|---|---|---|---|---|
| `go build ./...` | 2026-09-09T06:03:37Z | 06:03:40Z | 2s | 通过 |
| `go vet ./...` | 06:03:45Z | 06:03:46Z | 1s | 通过 |
| `go test -p 1 -count=1 ./...`（去代理变量） | 06:03:53Z | 06:05:13Z | 80s | 通过 |
| `pnpm --filter admin-web run typecheck` | 06:05:19Z | 06:05:25Z | 6s | 通过 |
| `pnpm --filter admin-web run test` | 06:05:30Z | 06:05:49Z | 19s | 通过，143 文件 / 2208 用例 |
| `pnpm --filter ui-admin run test` | 06:05:54Z | 06:05:57Z | 3s | 通过，17 文件 / 262 用例 |
| `bash scripts/check-governance.sh` | 06:06:02Z | 06:06:06Z | 4s | 通过 |
| `gitleaks detect --source . --no-git --redact` | 06:06:10Z | 06:06:12Z | 2s | 11 条，与基线相同 |

前端三段都带 `--config.verify-deps-before-run=false`；`node_modules` 用 junction
镜像自 `wt-XM-I18N`（六处：根、`web/apps/admin-web`、`web/apps/ui-storybook`、
`web/packages/design-tokens`、`web/packages/ui-admin`、`web/packages/ui-primitives`），
未跑 `pnpm install`。

**gitleaks 基线核对**（不是只看总数）：把集成分支的报告与
`wt-XM-OPS-TRUTH`、`wt-XM-CARD-VISIBILITY` 两个工作树的报告按
（规则 ID，文件）比对，集成分支的集合与两者并集**完全相等**，
既无新增也无遗漏。11 条全是 `generic-api-key` 对指标键／测试夹具的误报，
分布在 9 个文件：`connectors/infini/redact_test.go`、
`connectors/infini/signing_test.go`、`connectors/sms62/client.go`、
`internal/platform/cards/webhook_test.go`、
`internal/platform/integration/actions_test.go`、
`internal/platform/jobs/query_store_integration_test.go`（2 处）、
`internal/platform/ops/atomicity_store_test.go`、
`web/apps/admin-web/src/api/platform.test.ts`、
`web/apps/admin-web/src/lib/metrics.test.ts`（2 处）。

## not_run

- 未跑 `pnpm --filter ui-storybook run build`（不在派工的门禁清单里）。
- 未做浏览器端手工验证；管理端界面表现只有单测覆盖。
- 未在生产或预发环境部署验证。
- 未 `git push`（派工硬约束）。

## WORKBENCH ↔ OPS-TRUTH 字段接线现状

`XM-WORKBENCH-TRUTH` 消费 `XM-OPS-TRUTH` 新增的后端字段，且对字段缺席有回退。
合并后逐处核对结果如下（三项里两项接上了，一项没有）：

| 字段／端点 | 后端 | 前端类型 | 前端读取 | 结论 |
|---|---|---|---|---|
| `AlertItem.trigger_count` | 有，`httpapi/alerts.go:58,94`（`*int32`，老数据为 null） | 有，`api/alerts.ts:58`（可选） | 有，`describeFireCount`（`alerts.ts:107-122`）被 `AlertsPage.tsx:304`、`workbench.ts:205,250`、`overview.ts:54` 使用 | **已接上** |
| `AlertItem.first_opened_at` | 有，`httpapi/alerts.go:65,96`（恒非空） | 有，`api/alerts.ts:64`（可选） | 工作台读了（`alertAgeAnchor`，`alerts.ts:129-137` → `workbench.ts:186-207,237-252`）；**告警页表格没读**（`AlertsPage.tsx:260-274` 仍直接渲染 `opened_at`） | **部分接上** |
| 失败作业按类型聚合 | 有，`failed_jobs_by_kind` 等三个字段挂在既有的 `GET /api/v1/ops/overview` 上（`httpapi/ops_overview.go:189-210`，handler `:224`，路由 `router.go:376`，数据源 `jobs/query_store.go:618`，窗口 24h） | **无**，`api/ops.ts:79-87` 的 `OpsOverview` 接口没有这三个字段 | **无**，全仓 `web/` 搜不到 `failed_jobs_by_kind` | **未接上** |

三条后续项（**不在本次范围**，按派工记录在此）：

1. **失败作业聚合前端未接**。前端仍走旧路径：`OverviewPage.tsx:105-109` 拉
   `GET /jobs/runs?state=discarded&limit=20`，再由 `workbench.ts:267-347`
   在客户端按 kind 分组。这正是新后端聚合要替掉的那条路——它只看最近 20 条，
   而 09-08 那次 288 条 `card_sync` 废作业会把第一页挤满，别的类型看不见。
   后端已经算好了全 24 小时窗口的 `count`/`first_at`/`last_at`，前端没用。
2. **`first_opened_at_estimated` 被丢掉**。后端为这个字段专门加了「这是估算值」
   的兄弟标志（`alerts.go:68`），全仓 `web/` 零引用。前端现在无法区分
   「真的首次打开时间」与「拿 `opened_at` 兜的估算值」。
3. **告警页表格的「首次」列仍是 `opened_at`**，不是 `first_opened_at`。
   工作台与告警页现在对同一条告警的「首次」会给出不同的时刻。

另有两处**注释过期**（不影响运行，但会误导下一个人）：`api/alerts.ts:54-57`
与 `workbench.ts:193-195` 仍写着这些字段「今天还不存在」，而后端已经在发。

## 迁移清单

本分支相对 `release/v0.1-launch` 新增 **7 条**迁移（不是 2 条）：

| 版本 | 名称 | 来源分支 |
|---|---|---|
| `000049` | `action_approval` | 随 `XM-I18N-LABELS` 带入 |
| `000050` | `core_schema_migration_state` | 随 `XM-I18N-LABELS` 带入 |
| `000051` | `integration_registry` | 随 `XM-I18N-LABELS` 带入 |
| `000052` | `content_publishing` | 随 `XM-I18N-LABELS` 带入 |
| `000053` | `ext_app_registry` | 随 `XM-I18N-LABELS` 带入 |
| `000054` | `alerts_trigger_and_version_ack` | `XM-OPS-TRUTH` |
| `000055` | `card_account_sync_pause` | `XM-CARD-VISIBILITY` |

**无版本号冲突**：两条并行分支恰好各占一个号（`000054` / `000055`），
不存在同号两文件。已在干净的新库上从头全量 `up` 验证过一次。

## 部署注意事项

1. **生产管理端仍是 09-06 的构建**（`686e5b25`，2026-09-06 13:56Z）。本分支的
   前端改动一条都还没上生产。前端相关的验证只有单测，界面表现要等构建部署后
   才能看。
2. **CPA 快照定时器改了 unit 文件**（`deploy/cpa-snapshot/xingmang-cpa-snapshot.timer`
   加了 `OnCalendar=*:0/5` 墙钟兜底）。改的是 systemd unit，**必须
   `systemctl daemon-reload && systemctl restart xingmang-cpa-snapshot.timer`
   才生效**（`docs/runbooks/CPA-SNAPSHOT.md:71`）。只 `git pull` 不 reload，
   定时器还是老的，而这次修的恰恰是「定时器过期后再也不自己醒来」——
   漏了这一步就等于没修。
3. **迁移按 7 条准备**，不是 2 条。见上表。
4. 告警规则从 8 条变 9 条（新增 `cards.sync.failed`）。静默窗口按 `rule_key`
   校验存在性，新规则上线后运营才能在静默下拉里选到它。

## risks

1. **本分支带的内容远多于五个切片**（见下节第 1 条）。这是最大的风险，
   不是技术风险而是审读范围的风险。
2. **卡片告警阈值的取舍是我做的判断**（第 5 条）。判据是「哪个字段在替换后
   仍表示连续轮数」，数字没变（3），但这是一次跨切片的语义选择，
   建议 `XM-CARD-VISIBILITY` 与 `XM-OPS-TRUTH` 的作者各看一眼。
3. **失败作业聚合端点造好了没人用**。后端多算一份 24 小时聚合、前端继续用
   会被挤满的 20 条列表，两边都在跑。这不是错误，是白花的成本加一个仍然存在
   的盲区。
4. 集成分支上新增了一条测试断言（第 4 条）。它现在是绿的，但它约束的是
   CATALOG.md 的措辞——将来有人重写那一节的句式会红，红的时候要改的是
   文档或断言，不是规则。

## 负责人必须知道的三件事

### 1. 这条分支给验收线带来的是 34 个切片，不是 5 个

`ai/claude/XM-I18N-LABELS` 不是一条薄切片：它自己就是一条累积分支，
里面已经合了 `XM-0030a-approval-core` 和 `XM-EXT-MERGE-TRIAL`
（`XM-EXT-APP` / `XM-EXT-INTEGRATION` / `XM-EXT-PUBLISHING`）。
这四个合并提交（`8655a63`、`4bf81f7`、`ee953f5`、`79be3fc`）都**不在**
`release/v0.1-launch` 里，会跟着这次合入一起进去。

相对 `release/v0.1-launch`，`docs/handoffs/slices/` 下**新增 34 个**切片
handoff（全部是新增，没有一个是修改），其中只有 5 个是本次派工点名的那几条：
`XM-I18N-LABELS`、`XM-CARD-VISIBILITY`、`XM-WORKBENCH-TRUTH`、
`XM-OPS-TRUTH-A`、`XM-OPS-TRUTH-B`。另外 29 个是随 I18N 带进来的，包括
`XM-0030a/b/c` 系列、`XM-EXT-*` 三条、`XM-ERRCODE-*` 四条、
`XM-FINANCE-GLOBAL0`、`XM-IDENTITY-BINDING`、`XM-DESIGN0` 等。
（`XM-CPA-TIMER-CALENDAR` 没有独立的切片 handoff，它的记录在
`docs/handoffs/CPA-REPLAN-FACTS-2026-09-09.md`。）

派工说的「五条分支都从 `release/v0.1-launch` 分出」在 git 拓扑上成立
（共同祖先确实是它），但在**内容**上不成立。如果验收线的预期是
「这次只审五个切片」，那预期与实际差 29 个，需要先对齐再合。

### 2. 有五处「两边都绿、合起来才红」，其中三处是静默的

第 5、6、7 条是编译期／vet 期发现的，合不上就红，跑不掉。
真正值得注意的是第 3、8 两条和第 5 条的**反事实**：

- 第 8 条（README 缺行）只有把两条分支放在一起才会红，各自分支上永远绿；
- 第 3 条（规则条数）在基线上已经错了一年半载没人发现，因为没有任何东西在看；
- 第 5 条如果我接错字段，**全量门禁会全绿**，只是那条告警晚三倍才响。

这三处的共性是：判据在别处，而没有东西把它钉住。第 4 条和第 8 条的修法都是
让判据从 `alerts.Rules()` **发现**而不是手列。

### 3. 失败作业聚合端点是造好了没接的

`XM-OPS-TRUTH` 加的 `failed_jobs_by_kind`（24 小时窗口、按 kind 聚合、
带 `count`/`first_at`/`last_at`/`last_error`）在后端每次
`GET /api/v1/ops/overview` 都会算，但前端的 `OpsOverview` 接口里根本没有这三个
字段，全仓零引用。工作台仍用旧的「拉 20 条 discarded 再在客户端分组」。
09-08 那次 288 条废作业挤满第一页的问题，后端已经修好了，前端还没接。

这一项按派工要求**没有在本次修**，记为后续项。

## follow_ups

1. 前端接上 `failed_jobs_by_kind`（`api/ops.ts` 的 `OpsOverview` 补三个字段，
   工作台改用它替掉客户端分组）。
2. 前端读 `first_opened_at_estimated`，把「估算值」在界面上说出来。
3. 告警页表格的「首次」列改用 `first_opened_at`，与工作台对齐口径。
4. 清掉 `api/alerts.ts:54-57` 与 `workbench.ts:193-195` 里「这个字段今天还不
   存在」的过期注释。
5. 与验收线对齐第 1 条：这次合入的实际范围是 34 个切片。
