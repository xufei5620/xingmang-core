# XM-OPS-TRUTH 子片 A：可观测性说真话

- **status**: ready-for-review（分支内交付，未推 GitHub、未部署）
- **branch**: `ai/claude/XM-OPS-TRUTH`（起点 e4f7dcf）
- **commit**: `f802ab7`（实现，32 files changed, +2638 / -287）→ `1f7ca20`（回填 SHA）
  → `REVIEW_SHA`（审稿四条阻塞级问题的处置，见「审稿处置」一节；追加提交，未 amend）
- **时间**: 第一轮 2026-09-08T16:15:41Z – 17:05Z（约 50 分钟）；
  审稿处置轮 2026-09-08T17:33:58Z – 18:0XZ
- **需求来源**: `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md` 三·1、三·2、二表第一行
- **旧账**: `docs/handoffs/ACCEPTANCE-LOG.md:88`（作业日志打 env 兜底值）、`:82`/`:86`
  （verify-real-mode.sh 应改读 `connector_config_applied`）——本片一并还掉

---

## summary

三件事，都是「让系统对同一个问题只给一个答案，而且是真答案」。

**1. 生效模式只剩一个判定点。** 新增 `jobs.ResolveEffectiveMode`（纯函数）与
`jobs.EffectiveConnectorConfig`。worker 的动态工厂每轮经它判定，并把结果**随客户端
一起返回**；`/ops/overview` 的 `modeFor` 也调它。作业日志的 `newapi_mode` /
`sub2api_mode` 从此是**本轮生效**模式而不是进程启动时拷来的 env 缺省——2026-09-08
生产两行都是 `real`，日志却一直打 `fake`，排查因此得出「生产在跑假数据」的错误结论。

配套物理删除：`NewAPISyncOptions.Mode` / `Sub2APISyncOptions.Mode` 与两个 worker 的
`mode` 字段整个删掉。留着注释拦不住下一个人再打它（memory「被信任的过期闸最危险：
要让它物理上只剩一处」）。`worker_started` 里那两个字段改名 `*_mode_default` 并新增
`effective_mode_log_events` 指路。

**2. 读取预算按链长推导 + 每组独立 deadline + 逐次耗时日志。** 旧实现是一个写死的
20s 整轮预算，五组共用：前两组慢一点，排在后面的三组还没开始就被判 `unavailable`——
handoff 二表第一行描述的正是这个形态。现在每组各有 `单次超时 × 2` 的 deadline，整轮
预算 = 组数 × 每组预算（NewAPI 100s / Sub2API 80s），并同步补了 River 的 `Timeout()`
覆写（120s / 100s）——**不补的话整个预算改动等于没做**，River 默认 1 分钟会掐掉整个
`Work`，那一轮连「同步失败」都写不进库。每组打一条 `upstream_read`（Info 级），
带 `read_step` / `metric_keys` / `elapsed_ms` / `status` / `error_code` /
`group_budget_ms` / `round_remaining_ms`。

**3. 六处过期文档 + 验收脚本。** brief 说三处，实际六处；另有 `verify-real-mode.sh`
与三个 fixture 会被改名直接打红，必须同一个提交落地。

**4.（审稿处置轮）`maintenance` 队列不再是单槽，`worker_started` 整条可测，
「唯一判定点」的措辞收窄到它真正覆盖的范围。** 详见下面「审稿处置」。

---

## 审稿处置（四条阻塞级问题，逐条）

### A. `maintenance` 单槽会被抬高的执行期限占满（major）

**问题**：`QueueMaintenance` 的槽位一直是 `cfg.MaxWorkers`，而 `DefaultMaxWorkers = 1`
且全仓没有任何 env / compose 覆盖点——生产恒为 1。心跳、告警评估、留存清理、两条
同步采集、成本采集全挤在这一个槽上。本片把 `newapi_sync` 的执行期限从 River 默认的
60s 抬到 120s、`sub2api_sync` 抬到 100s：最坏情况下一个 300s 周期里
`newapi(120) + sub2api(100) + finance(120) = 340s`，**超过一个周期**，排在后面的
60 秒节拍任务（心跳、告警评估）被推迟数分钟。一个为「让观测说真话」而做的改动，
不该让故障期的观测变差。

**处置**（新文件 `internal/platform/jobs/queue_slots.go`）：

- 槽位 = 慢任务个数 + 1（慢任务全在跑时仍留一个槽给快节拍的任务）。
- 慢任务是**数**出来的，不是手列的：
  (1) 这个部署启用了哪些 maintenance 任务、周期多少——走 `effectiveJobConfig`，
  与 `BuildEffectiveManifest` / `/ops` 那张部署态时刻表同一段每任务解析；
  (2) 每个 Worker 声明的 `Timeout()`——在**真正的注册点**收集（`addWorker` 取代
  `river.AddWorker`），不是另开一份名单。
- 判据 = 队列最快节拍与 River 默认 1 分钟里**更小的那一个**（`slowJobThreshold`）：
  节拍那一头挡「能占过一个节拍的任务」，默认期限那一头挡「把常住人口也算成慢任务、
  于是每个任务一个槽」。取小者，永不少开槽。
- `cfg.MaxWorkers` 仍是下限：调大它的部署不会被这里调小。
- 新增启动日志 `queue_slots`，`max_workers` **直接从交给 River 的那张 map 里取**，
  测试断言这一行等于断言了那张 map。

默认部署下：节拍 60s、慢任务 3 个（`newapi_sync` / `sub2api_sync` /
`finance_cost_sync`）、槽位 4。关掉一条链路槽位自己少一个。

**副作用（已知并已守）**：不同种类的任务从此可能同时跑。同一种任务的两轮要重叠，
得先有「执行期限 ≥ 自己的周期」——三个慢任务都不满足（120/100/120 对 300s），
`TestSlowMaintenanceJobsCannotOverlapThemselves` 钉住这条；满足的只有心跳与告警评估
（60s 对 60s），它们与顺序相关的写在库层各有闸（`LockAuditChain` 的
`pg_advisory_xact_lock`、告警投递的 `FOR UPDATE SKIP LOCKED`）。连接池见 risks 8。

### B. `connector_config_source` 是写死的 `"database"`（major）

`connectorModeStartupAttrs` 原先把它写成字面量，不看 `config.ConnectorConfigs`。
`ConnectorConfigs == nil` 的部署走静态工厂、每轮按 env 解析、`job_completed` 打
`*_mode_source=env`，而 `worker_started` 仍宣称 `database`。今天为真只因为生产装配
无条件设了 `ConnectorConfigs`——靠别处事实成立的判断就是静默债，而本片存在的全部理由
正是消灭这种副本。**处置**：改成 `connectorConfigSourceLabel(config)`（`nil` → `env`），
词表与 `jobs.ModeSource*` 同一套，好让启动日志与每轮 `job_completed` 直接对读。
测试两个装配各一个子例，外加一条「两者必须不相等」——写死成任何常量都过不去。

### C. 缺席断言钉在半成品上（major）

`worker_started` 的属性此前只有接入模式那一小段抽进了 `connectorModeStartupAttrs`，
其余仍内联在 `main()`。于是「不许再出现裸键 `sub2api_mode`」这条缺席断言钉的是辅助
函数的返回值，而不是打出去的那一行：在 `main()` 的切片里加回裸键，全套门禁一条都不红。

**处置**：整条属性搬进 `workerStartupAttrs(config)`，`main()` 只剩一句
`logger.InfoContext(ctx, "worker_started", workerStartupAttrs(config)...)`；缺席与在场
断言都跑在完整属性集上。**闸的范围也改成发现来的**：不再手写
`{"sub2api","newapi"}`，而是遍历 `credentials.Platforms`（与迁移 000020 的 CHECK 同源）
——加第三个平台时这条闸会自己红。`finance_collect_mode` / `audit_archive_mode` /
`reqlog_metrics_mode` 这些裸 `_mode` 键**不在**范围内，它们本来就是生效值。
变异 M17（在 `workerStartupAttrs` 的尾段加回裸键）双向变红。

### D.「唯一判定点」的措辞比事实大（major）

`EffectiveConnectorConfig` 的注释写着「不许任何一方再写第二个 if」，但同一个
platform-api 进程里还有三处各写各的：`platformpayments.go` 的 `resolveSub2API` /
`resolveNewAPI`、`platformusers.go` 的 `dynamicUsersClient.resolve`。

**处置：收窄措辞，逐处点名，不合并**（reviewer 给的方案 b）。理由：那三处回答的是
**另一个问题**——「platform-api 这次请求用哪个客户端」，无行时回落的是它们自己的
进程缺省（`XM_PLATFORM_PAYMENTS_MODE` / `XM_PLATFORM_USERS_MODE`），不是 worker 的
`XM_SUB2API_MODE`。把它们并进来会造出一个假的统一。唯一真会分叉的地方是「行在、
但 mode 是空串」（`ParseSub2APIMode("")` → fake vs 本解析器落到缺省侧），而
`core.connector_config.mode` 有 `CHECK (mode IN ('fake','real'))`（迁移 000020），
空串进不了表——**这是靠迁移那条约束成立的，不是靠这几段代码自己成立的**，注释里
写明了这一点。合并那三处会改动另外两个切片的端点语义，属于越权，列进 follow_ups 7。

---

## files_changed

### 生产代码

| 文件 | 改了什么 |
|---|---|
| `internal/platform/jobs/connector_config.go` | 新增 `EffectiveConnectorConfig`、`ModeSource{Database,Env,Unknown}`、`ResolveEffectiveMode`；两个动态工厂改为经它判定并回传生效配置；`logApplied` 改吃 `EffectiveConnectorConfig`（不再自己重算 source/version） |
| `internal/platform/jobs/newapi_sync.go` | 工厂签名加第二返回值；删 `Options.Mode` / `worker.mode`；加 `Options.RequestTimeout`；`newapiGroupBudget` / `newapiReadBudget` / `newapiSyncJobTimeout` / `Timeout()`；`newapiReadStepNames` + `newapiReadStepMetricKeys`；`read()` 改为按具名步骤链循环、每组独立 deadline；新增 `logUpstreamRead`；`job_completed` 打生效模式 + 来源 + 版本 |
| `internal/platform/jobs/sub2api_sync.go` | 与 NewAPI 同型（四组：stats/orders/balances/payments） |
| `internal/platform/jobs/cpa_sync.go` | 只加 `cpa_mode_source="env"` + 一段说明。**不接解析器**——见下面 owner_decisions |
| `internal/platform/jobs/client.go` | 两个 worker 去掉 `Mode:`、加 `RequestTimeout:`；探针工厂适配新签名（`_` 占位） |
| `internal/platform/httpapi/ops_overview.go` | `modeFor` 改走 `jobs.ResolveEffectiveMode`，无行时不再硬答 `"fake"`；响应新增 `effective_mode_source` |
| `cmd/platform-worker/main.go` | `worker_started` **整条**属性搬进 `workerStartupAttrs`，`main()` 只剩一句打印（审稿 C） |
| `cmd/platform-worker/startup_log.go` | **新增**：`workerStartupAttrs`（全量）+ `connectorModeStartupAttrs`（`*_mode_default` 改名、`effective_mode_log_events`）+ `connectorConfigSourceLabel`（审稿 B） |
| `internal/platform/jobs/queue_slots.go` | **新增**（审稿 A）：`jobTimeouts` / `addWorker` / `slowJobThreshold` / `maintenanceQueueSlots` / `riverQueueConfigs` / `logQueueSlots` |
| `internal/platform/jobs/client.go`（审稿轮） | 13 处 `river.AddWorker` → `addWorker(timeouts, …)`；`Queues` 改由 `riverQueueConfigs` 生成；新增 `queue_slots` 启动日志 |
| `internal/platform/jobs/connector_config.go`（审稿轮） | `EffectiveConnectorConfig` 的文档收窄到真实覆盖范围，逐处点名未收编的三处（审稿 D） |
| `cmd/platform-worker/README.md`（审稿轮） | 新增「maintenance 队列的槽位是算出来的」小节；`connector_config_source` 的推导说明 |
| `cmd/platform-api/platformpayments.go` | 两处 `_` 占位（签名变更的机械改动，不在本片所有权内，已说明） |

### 门禁与 fixture

- `scripts/verify-real-mode.sh`：`worker_started` 不再参与模式判定；新增
  `connector_config_applied` 判定（`--mode real` 时还要求 `config_source=database`）；
  `db_mode_by_platform.get(platform, "fake")` 的猜法改成 `none` + 不比对
- `tests/deploy/verify-real-mode.test.sh`：新增一条 `expect_failure`（生效模式与请求
  模式不一致）；未配置平台的期望输出 `sub2api:fake` → `sub2api:none`
- `tests/fixtures/real-mode/worker-{real,staging,failed}.log`：改名 + 补
  `connector_config_applied` / `job_completed` 的新字段
- `tests/fixtures/real-mode/worker-applied-fake.log`：**新增**（env 缺省与后台表都说
  real、只有生效模式是 fake——旧脚本一条都抓不到的形态）

### 文档（六处，brief 只列了三处）

- `cmd/platform-worker/README.md`：`:93` 字段名；`:107-121` production fake 闸改成
  两层叙述（原文与同文件 `:196-200` 自相矛盾，后者才是现行行为）；新增
  「别拿启动日志验证生效模式」小节
- `docs/runbooks/SWITCH-NEWAPI-REAL.md` 验证第 1 条（含 `unavailable` 的新判据）
- `docs/runbooks/SWITCH-SUB2API-REAL.md` 同款
- `docs/modules/connector/RUNBOOK.md:115-122`：**brief 没列的第四处**，而且是两份
  SWITCH runbook 明确指过去的「完整切换步骤与故障对照表」

### 测试

- `internal/platform/jobs/effective_mode_test.go`（新增）
- `internal/platform/jobs/read_budget_test.go`（新增）
- `internal/platform/httpapi/ops_overview_mode_parity_test.go`（新增，同源探针）
- `cmd/platform-worker/startup_log_test.go`（新增）
- 既有测试适配：`connector_config_test.go`（顺带补生效配置断言）、
  `newapi_sync_test.go`、`sub2api_sync_test.go`、`rollup_metadata_test.go`、
  两个 `*_integration_test.go`、`sub2api_sync_atomicity_integration_test.go`、
  `ops_overview_test.go`

**无迁移**：没有新表、没有新列，`db/migrations/000054` 不用建，dbroles 漂移门禁的
契约清单也不用动。

---

## 新增 API 字段清单（供 XM-WORKBENCH-TRUTH 消费）

`GET /api/v1/ops/overview` → `sync_pipelines[]`：

| 字段 | 类型 | 取值 | 说明 |
|---|---|---|---|
| `effective_mode` | string | `"fake"` / `"real"` / `""` | **语义变更**：空串表示本进程不知道 |
| `effective_mode_source` | string | `"database"` / `"unknown"` / `""` | **新增** |
| `config_available` | bool | 不变 | 仍是「credentials 模块挂没挂」，与「有没有这一行」是两件事 |
| `config_updated_at` | string\|null | 不变 | |

**渲染规则（请照抄）**

- `config_available === false` → 两个字段都是空串：显示「未接入配置模块」（既有行为）
- `effective_mode_source === "database"` → 按 `effective_mode` 显示「模拟数据」/「真实对接」
- `effective_mode_source === "unknown"` → `effective_mode` 是**空串**，显示「模式未知」，
  悬浮说明建议逐字用：
  > 该平台未在后台配置接入模式，实际按 worker 进程的环境变量缺省运行；
  > 到 设置 → 凭据 → 接入模式 显式配置后此处才有确定答案。

**危险处**：生产两行都在库里，所以生产上 `effective_mode` 不会为空——恰恰因此
本地/staging 才测得到。**请加一条空串用例**，别让 `effective_mode === 'fake'` 之类的
判断把空串落进未定义分支。

`effective_mode_source` 永远不会是 `"env"`：platform-api 容器没有
`XM_SUB2API_*` / `XM_NEWAPI_*`（它不跑同步），看不到 worker 的缺省。

### 日志字段（运维 / 巡检脚本消费）

| 事件 | 字段 | 说明 |
|---|---|---|
| `job_completed`（newapi_sync / sub2api_sync） | `{platform}_mode` | 语义变更：**本轮生效**，不再是 env 缺省。字段名刻意不变 |
| 同上 | `{platform}_mode_source` | 新增：`database` / `env` / `unknown` |
| 同上 | `{platform}_config_version` | 新增：行版本，非 database 来源时 0 |
| `job_completed`（cpa_sync） | `cpa_mode_source` | 新增，恒 `env`（见 owner_decisions 第 2 条） |
| `upstream_read` | 全部 | **新增事件**，每组读取一条 |
| `worker_started` | `sub2api_mode` / `newapi_mode` | **改名** → `*_mode_default` |
| 同上 | `effective_mode_log_events` | 新增：`"connector_config_applied,job_completed"` |
| 同上 | `connector_config_source` | 语义变更：从写死的 `"database"` 改为按装配推导（`database` / `env`） |
| `queue_slots` | 全部 | **新增事件**（审稿 A）：`queue` / `max_workers` / `fastest_cadence_seconds` / `slow_job_threshold_seconds` / `slow_job_count` / `slow_job_kinds`，每次 `NewClient` 打一条 |

---

## tests_run

```
export XM_TEST_DATABASE_URL="$(bash scripts/dev/worktree-testdb.sh --print-url)"
env -u HTTP_PROXY … go test -p 1 -count=1 \
  ./internal/platform/jobs/ ./internal/platform/httpapi/ \
  ./cmd/platform-worker/ ./cmd/platform-api/
```
→ 四个包全 ok。第一轮：jobs 7.971s / httpapi 1.148s / platform-worker 0.105s /
platform-api 0.082s。审稿处置轮复跑：jobs 7.237s / platform-worker 0.086s /
httpapi 1.157s / platform-api 0.083s（含 jobs 的全部集成测试，测试库
`xm_test_wt_xm_ops_truth`）。

```
env -u … go vet ./internal/platform/jobs/ ./internal/platform/httpapi/ \
  ./cmd/platform-worker/ ./cmd/platform-api/     → 无输出
gofmt -l <同四目录>                                → 无输出
bash tests/deploy/verify-real-mode.test.sh        → VERIFY-REAL-MODE-TEST-OK（29 条全 ok）
bash scripts/check-governance.sh                  → exit 0（审稿处置轮补跑）
```

### 新增测试清单

| 测试 | 钉住什么 |
|---|---|
| `TestNewAPISyncLogsEffectiveModeNotEnvDefault`（4 子例） | 行 real/env fake、无行/env real、行 fake/env real、库读不到/env real |
| `TestSub2APISyncLogsEffectiveModeNotEnvDefault`（4 子例） | 同型 |
| `TestCPASyncLogsModeSourceEnv` | CPA 补的来源标签 |
| `TestCPAHasNoConnectorConfigRow` | 「今天为什么不需要解析器」的前提 |
| `TestResolveEffectiveModeIsTheOnlyJudgment` | 唯一判定的四种入参组合 |
| `TestSyncWorkersHaveNoModeField` | Mode 字段不许长回来；CPA 的不许被「统一」掉 |
| `TestUpstreamReadDoesNotLeakCredentials` | 新日志的泄漏闸 |
| `TestNewAPI/Sub2APIReadBudgetDerivesFromChainLength` | 预算是纯比例函数、fail closed |
| `TestSyncJobTimeoutsCoverTheReadBudget` | JobTimeout > 读预算 + 写库余量，且 < 同步周期 |
| `TestNewAPIReadStepsCoverEveryReadErrorField` | 名单 ↔ `*ReadErrors` 字段数 ↔ 指标键并集 |
| `TestNewAPIReadChainMatchesDeclaredSteps` | 从跑出来的日志反钉名单 |
| `TestNewAPIEachReadGroupGetsItsOwnDeadline` | 一组一个 deadline，不是共用 |
| `TestNewAPI/Sub2APISyncLogsPerReadElapsed` | 逐组日志字段齐全、耗时是分段的 |
| `TestNewAPIUpstreamReadStatusIsPerGroup` | 状态逐组 |
| `TestUpstreamReadTellsSlowUpstreamFromFailingUpstream` | handoff 三·2 那道题的可执行判据 |
| `TestOpsOverviewAgreesWithWorkerWhenRowExists` | 同源探针：worker 日志 ↔ HTTP 响应逐字对拍 |
| `TestOpsOverviewAbstainsWhereWorkerFallsBackToEnv` | 无行时 API 弃权而不是猜 |
| `TestOpsOverviewBothPipelinesUseTheResolver` | 两条流水线各自判定 |
| `TestOpsOverviewMissingRowIsUnknownNotFake` | 替换旧的 `...MissingRowDefaultsToFake` |
| `TestWorkerStartupLogNamesDefaultsAsDefaults` / `TestStartupDefaultsRenameIsTwoWay` | 改名双向；审稿轮改为跑在**完整**属性集上，范围取自 `credentials.Platforms` |
| `TestStartupConnectorConfigSourceFollowsAssembly` | 审稿 B：接与不接 `core.connector_config` 必须给出**不同**答案 |
| `TestMaintenanceQueueSlotsLeavesRoomForTheFastestCadence` | 审稿 A：默认部署槽位 4 > `MaxWorkers` |
| `TestMaintenanceQueueSlotsFollowsDeployment`（4 子例） | 关链路→槽位少、`MaxWorkers` 只是下限 |
| `TestMaintenanceQueueSlotsThresholdFollowsCadence` | 判据跟着节拍走，不是写死的 1 分钟 |
| `TestMaintenanceQueueSlotsDiscoverNewSlowJobs` | 范围是数出来的：多一个慢任务，槽位自己 +1 |
| `TestSlowMaintenanceJobsCannotOverlapThemselves` | 多槽的副作用闸：慢任务的期限必须 < 自己的周期 |
| `TestNewClientWiresDerivedMaintenanceSlots` | 从最外层打进来，读交给 River 的那张队列 map |
| `TestWorkersRegisterThroughAddWorker` | `client.go` 里不许再出现 `river.AddWorker(` |

---

## 变异表（23 条，逐条改 → 跑 → 记录 → 还原 → 复跑）

M1–M16 是第一轮，M17–M23 是审稿处置轮。

| # | 变异 | 结果 | 变红的测试 |
|---|---|---|---|
| M1 | `newapi_sync.go` job_completed 打 env 缺省而不是 `effective.Mode` | 🔴 | `…LogsEffectiveModeNotEnvDefault` 3 子例；`OpsOverviewAgreesWithWorker`；`OpsOverviewAbstainsWhere` |
| M2 | 删掉 `newapi_mode_source` 字段 | 🔴 | `…LogsEffectiveModeNotEnvDefault` 全 4 子例；parity 的 source 对拍 |
| M3 | `ResolveEffectiveMode` 的 unknown 分支返回 `Mode:"fake"` | 🔴 | `ResolveEffectiveModeIsTheOnlyJudgment`；`AbstainsWhere`；`BothPipelines`；`MissingRowIsUnknownNotFake` |
| M4 | `ops_overview.go` 恢复 handler 自己的 `"fake"` 分支 | 🔴 | 同 M3 的 httpapi 三条（**这条证明「抽出解析器」不等于「handler 真的走了它」**） |
| M5 | 每组 deadline 改回共用整轮 `readCtx` | 🔴 | `EachReadGroupGetsItsOwnDeadline`（1m40s vs 20s）；`TellsSlowUpstream…` |
| M6 | `newapiReadBudget` 返回写死的 20s | 🔴 | `ReadBudgetDerivesFromChainLength`；`SyncJobTimeoutsCover…`；`TellsSlowUpstream…` |
| M7 | 删掉 `NewAPISyncWorker.Timeout()` | 🔴 | `SyncJobTimeoutsCoverTheReadBudget`（Timeout=0） |
| M8 | payments 那一组不打 `upstream_read` | 🔴 | `ReadChainMatchesDeclaredSteps`；`LogsPerReadElapsed`；`TellsSlowUpstream…` |
| M9 | `elapsed_ms` 改成整轮累计（`time.Since(roundStart)`） | 🔴 | `LogsPerReadElapsed` 的上界断言（channels 81ms > 75ms） |
| M10 | `status` 恒打 `"ok"` | 🔴 | `UpstreamReadStatusIsPerGroup`；`TellsSlowUpstream…` |
| M11 | orders 组的 `metric_keys` 只写一半 | 🔴 | `ReadStepsCoverEveryReadErrorField`；`LogsPerReadElapsed` |
| M12 | `newapiReadStepNames` 少一组 | 🔴 | `ReadStepsCover…`；`ReadChainMatches…`；`LogsPerReadElapsed` |
| M13 | `startup_log.go` 改回裸键 `sub2api_mode` | 🔴 | `WorkerStartupLogNamesDefaultsAsDefaults`（在场 + 缺席**同时**红）；`StartupDefaultsRenameIsTwoWay` |
| M14 | `sub2api_sync.go` job_completed 打 env 缺省 | 🔴 | `Sub2APISyncLogsEffectiveModeNotEnvDefault` 3 子例 |
| M15 | 删掉 `cpa_mode_source` | 🔴 | `CPASyncLogsModeSourceEnv` |
| M16 | `verify-real-mode.sh` 不再比对 `connector_config_applied` 的 mode | 🔴 | 部署门禁 `VERIFY-REAL-MODE-TEST-FAILED`（被 `config_source` 子闸抓住，两个子闸互相独立） |
| M17 | 在 `workerStartupAttrs` 的**尾段**（原先内联在 `main()` 的那半截）加回裸键 `sub2api_mode` / `newapi_mode` | 🔴 | `WorkerStartupLogNamesDefaultsAsDefaults`（缺席）+ `StartupDefaultsRenameIsTwoWay`（双向）——这正是审稿 C 说旧闸抓不到的那条 |
| M18 | `connector_config_source` 改回写死的 `"database"` | 🔴 | `StartupConnectorConfigSourceFollowsAssembly` 的 `env` 子例 + 「两者必须不相等」那条 |
| M19 | `maintenanceQueueSlots` 不再按慢任务加槽（槽位恒为 `cfg.MaxWorkers`） | 🔴 | 4 条槽位测试 + `NewClientWiresDerivedMaintenanceSlots` |
| M20 | `riverQueueConfigs` 里 maintenance 用回 `cfg.MaxWorkers`（改动前的状态） | 🔴 | `NewClientWiresDerivedMaintenanceSlots`（槽位 1，want 4） |
| M21 | `newapi_sync` 的 Worker 改回 `river.AddWorker` 直接注册 | 🔴 | `NewClientWiresDerivedMaintenanceSlots`（`slow_job_kinds` 少一个）+ `WorkersRegisterThroughAddWorker` |
| M22 | `slowJobThreshold` 写死成 `riverDefaultJobTimeout`（忽略节拍） | 🔴 | `MaintenanceQueueSlotsThresholdFollowsCadence` |
| M23 | `newapiSyncJobTimeout` 抬到 320s（超过 300s 周期） | 🔴 | `SlowMaintenanceJobsCannotOverlapThemselves` + `SyncJobTimeoutsCoverTheReadBudget` |

每条变异后都已还原并复跑到全绿；仓库里已 grep 确认无 `MUTATION` 残留。
M19 在实现改用 `effectiveJobConfig`（而不是 `EffectiveJobSchedules`）之后**重跑过一次**，
仍然全红。

**缺席型断言的变异证据**（memory「缺席型断言要做变异验证」）：
- 「日志里不该出现相反的模式值」→ M1 让它红
- 「`worker_started` 里没有裸键 `*_mode`」→ M13 让它与在场断言同时红（双向）
- 「无行时 `effective_mode` 是空串」→ 配一条「响应体必须含 `effective_mode_source`
  子串」，防止字段被整个删掉时空串断言恒真

---

## owner_decisions_taken（替负责人做的决定，请复核）

1. **`/ops/overview` 无行时回答 `""` + `unknown`，不再回答 `"fake"`。**
   platform-api 容器结构上看不到 worker 的 env 缺省（handoff 主控者补正第 6 条）。
   旧答案只在「生产 env 缺省恰好也是 fake」时对，靠的是别处的事实。
   **对当前生产零可见影响**（两行都在库里），只在「后台没配过」时把一句假话换成真话。
   代价：前端字段语义变更，已写进上面的字段清单。

2. **CPA 不接解析器，只补 `cpa_mode_source="env"`。**
   brief 把三处并列，但 CPA 是异型：`cpa` 不在 `credentials.Platforms`
   （`credentials/connector_config.go:24`，与迁移 000020 的 CHECK 一致），
   没有 `core.connector_config` 行，`cpa_mode` 打的**已经是**生效值。照抄解析器会
   凭空造出一条 cpa 的 connector_config 语义。前提由
   `TestCPAHasNoConnectorConfigRow` 钉住。

3. **预算按「读取组」而不是「上游请求数」推导。** 这是对 brief 字面公式的偏离，
   见 deviations 第 1 条。

4. **同步补 `Timeout()` 覆写（brief 未提）。** 不补的话预算改动被 River 的 1 分钟
   默认吞掉，那一轮连失败都写不进库——比现在还糟（看板从「正在失败」退化成
   「数据静静变旧」，规格 §9.1 明令禁止）。

5. **改工厂签名而不是让 worker 再查一次库。** 再查一次会跨过 30s 的读取缓存 TTL
   （而读预算已经 100s），把「日志说的模式」和「实际读的上游」重新劈成两个事实。
   代价：`cmd/platform-api/platformpayments.go` 两处加 `_` 占位——**那个文件不在本片
   文件所有权清单里**，改动是纯机械的两行，请确认。

6. **`verify-real-mode.sh` 的第四份猜法只拆一半。** brief 的 sites 说把
   `.get(platform, "fake")` 改成显式失败，但 risks 又说「本片只拆得掉前两份」——
   brief 内部矛盾。落地方案：无行时如实报 `none` 且**不参与比对**（生效模式已由
   `connector_config_applied` 判定，那是 worker 真的拿去建客户端的那一份）。
   这样既不再猜，也不会把「未配置的 staging 部署」变成红门禁。相应地
   `tests/deploy/verify-real-mode.test.sh` 的期望输出从 `sub2api:fake` 改为 `sub2api:none`。

7. **动态工厂新增一条 fail-closed 分支。** 库里没有行、进程也没有缺省模式时返回
   `internal` 而不是悄悄按 fake 跑（旧实现在这条路上返回的也是 internal，行为保持）。

8. **「行存在但 mode 为空串」落到 env 缺省一侧**（旧实现是 `ParseSub2APIMode("")`
   → fake）。与 `overrideConnectorConfig` 的逐字段口径一致：行里留空的那一格用 env。
   库有 CHECK，实践中不可达。

### 审稿处置轮追加的决定

9. **`maintenance` 队列改用「慢任务 + 1」的推导槽位，而不是新开一个队列。**
   reviewer 给了两个方案。新开队列要动 `contracts/jobs/cluster-jobs.v1.json`
   （冻结契约里三行的 `queue` 值）、`query_store.go` 的队列深度视图清单，还会让一个
   新队列名出现在后台「后台任务」页上——那需要中文文案，而 `web/` 由
   XM-WORKBENCH-TRUTH 并行做、不在本片所有权内。抬槽位只动
   `internal/platform/jobs/*`（本片所有权内），且顺带覆盖 `finance_cost_sync`
   这个本片没碰、但同样会独占单槽 120s 的任务。代价是引入跨任务并发，见 risks 8。

10. **判据取「节拍」与「River 默认 1 分钟」中更小的那一个。**
    只按节拍算的话，`>=` 才是严格正确的（期限正好等于节拍也会漏一拍），但那会把每个
    默认期限的任务都算成慢任务，等于每个任务一个槽。只按默认期限算的话，把节拍调到
    30s 的部署就守不住。取小者永不少开槽，两头都挡住。

11. **不改 `pgxpool` 的池大小。** 并发从 1 升到 4 会增加连接压力（默认
    `MaxConns = max(4, CPU 核数)`），但同步任务的大部分时间花在上游 HTTP 上、并不
    握着连接，而显式设池大小要动 `cmd/platform-worker/main.go` 的连接串解析与
    compose 变量——属于另一件事。列进 follow_ups 6，请负责人决定是否单开。

---

## deviations（与 brief / 测试计划的偏离，边做边记）

1. **预算公式**：brief 写「单次超时 × 请求数 + 余量」。照字面算不出可用值——NewAPI
   一轮最坏 262 次上游请求，×10s = 2620s，是 300s 周期的 8.7 倍。改成
   **单次超时 × 卡顿余量(2) × 读取组数**，且余量是**比例**不是绝对常数（这样单元测试
   能把单次超时缩到毫秒级跑出同构形态，不必真等 100 秒）。这比单一整轮 deadline 更对：
   它让 handoff 二表第一行那句「前两组慢一点就把 20 秒用光，排在后面的三组还没开始就
   被判超时」在结构上不可能再发生。

2. **`metric_key` → `metric_keys`（复数）**：brief 写单数，但 orders 一组同时喂
   `newapi.recharge.daily` 与 `newapi.subscription.daily`，单数字段在这一组必然说谎。

3. **API 字段名用 `effective_mode_source`（brief）而不是 `mode_source`（测试计划）**，
   值用 `unknown`（brief）而不是 `env_unknown`（测试计划）。两份文档冲突，取 brief。

4. **每组独立 deadline（brief）而不是单一整轮 deadline（测试计划假设）**。测试计划里
   「所有读取拿到的 deadline 完全相同」那条断言随之作废，换成
   `TestNewAPIEachReadGroupGetsItsOwnDeadline`（每组的 deadline ≈ 组预算，且首尾拉开）。

5. **「预算耗尽 vs 上游 5xx」的判据形状变了**：有了每组独立 deadline 之后，「一组慢
   把后面几组饿死」在结构上不可能，所以判据改成「每组耗时是否顶到 `group_budget_ms`
   + `round_remaining_ms` 是否被抽干」。runbook 里写的就是这个新判据。

6. **文档六处而不是三处**（多了 `docs/modules/connector/RUNBOOK.md` 与
   `cmd/platform-worker/README.md:93`），外加脚本 + 4 个 fixture。

7. **`cmd/platform-worker/main.go` 抽了一个 `connectorModeStartupAttrs`**：不抽的话
   `worker_started` 内联在 `main()` 里，测试一条都伸不进去。审稿轮进一步把**整条**
   属性搬进 `workerStartupAttrs`——只抽一半等于把闸架在半成品上。

8. **（审稿轮）本片改了 `internal/platform/jobs/client.go` 的队列装配。**
   派工的文件所有权包含 `internal/platform/jobs/*`，但队列槽位不在 brief 的三条
   任务里；这是审稿指出的、由本片的期限改动直接引发的后果，所以在本片处置而不是
   另开一片。没有动 `contracts/jobs/cluster-jobs.v1.json`、没有动任何任务的 `queue`
   取值、没有新增队列。

9. **（审稿轮）`maintenanceQueueSlots` 走 `effectiveJobConfig` 而不是
   `EffectiveJobSchedules`。** 后者会对**每一个**已注册任务校验周期为正，包括这个
   部署没启用、因此手写 `Config` 字面量里根本没填周期的那些——集成测试正是这样构造
   `Config` 的（`TestSub2APISyncPostgresIntegration` 第一次跑就红在
   `approval_expire interval must be positive`）。一个「算槽位」的函数不许改变谁能
   启动，所以只借用每任务解析那一段。

---

## not_run

- 全量 `go test ./...`（按派工由主控者跑）
- 前端 `pnpm` 系列（本片不改 `web/`）
- **真实并发下的 `maintenance` 队列行为**：槽位 4 的效果只在单元测试与推导层面验证过
  （交给 River 的那张 map + 慢任务不许自重叠的闸），没有起一个真 worker 去观察四个
  任务同时跑。风险与已有的库层闸见 risks 8。
- 任何生产/服务器动作：未 ssh、未连生产库、未查看任何密钥文件、未推 GitHub、未部署

---

## risks

1. **`effective_mode` 可能是空串，前端要接住。** 生产两行都在库里所以生产不会为空——
   **这恰恰是危险处**：只有本地/staging 没配行时才会空，容易上线前测不到。
   已在字段清单里要求前端加一条空串用例。

2. **组内扇出仍不受墙钟约束。** `ChannelDirectory` 一组最坏 171 次上游请求
   （10 页渠道 + 2×40 错误率 COUNT + 1 次 /api/status + 2×40 今日统计），给它 20s 也
   照样整组超时。本片修掉了「一组拖垮别组」，**没修**「一组自己太胖」。真要修得动
   `connectors/newapi/upstream.go` 的 `maxErrorRateChannels` / `maxTodayStatsChannels`
   （都是 40），不在本片文件所有权内，且会降低渠道错误率的覆盖度
   （`CoveragePartial`）——建议单开并让前端知道覆盖率会降。

3. **`upstream_read` 让 worker 日志量变大。** 5 组 × 2 连接器 × 288 轮 = 2880 行/天，
   量级很小，但如果生产有按行数计费的日志收集请先确认。**不建议**改成只在失败时打或
   采样：区分预算耗尽与上游故障靠的正是「成功但很慢」那几行。

4. **`cpa_mode` 保持不变是有前提的**（cpa 不在 `credentials.Platforms`）。前提已由
   `TestCPAHasNoConnectorConfigRow` 钉住，但那条测试红的时候需要有人真的去改
   `cpa_sync.go`，而不是把断言删掉。

5. **`cmd/platform-api/platformpayments.go` 不在本片所有权内**，改了两行 `_` 占位。

6. **`ops_overview.go` 现在 import 了 `internal/platform/jobs`。** 该文件顶部原有一段
   注释论证「不该为四个字符串常量引入 jobs」——已改写为「为复用一个**不许重复**的
   判定而引入是值得的，为复制常量不值得」。`httpapi/jobs.go` 早有此依赖先例。

7. **`scripts/verify-real-mode.sh` 现在依赖 worker 日志里有
   `connector_config_applied`。** 该日志只在**生效配置变化时**打（指纹去重），而
   `logApplied` 的指纹初值是空串，所以第一轮必定打一条；`--since` 窗口起点早于 worker
   启动时才拿得到。脚本已经是从 `worker_started_at` 起取日志，成立。但如果将来有人把
   `*_SYNC_RUN_ON_START` 关掉，这条会变成假红——已记在 follow_ups。

8. **（审稿轮）`maintenance` 队列从 1 槽变 4 槽，是一次并发语义变更。**
   不同种类的任务可能同时跑。已守住的部分：慢任务不可能与自己重叠
   （期限 < 周期，有测试）；心跳与告警评估这两个期限等于周期的任务，其与顺序相关的
   写在库层各有闸（审计链 `pg_advisory_xact_lock`、告警投递 `FOR UPDATE SKIP LOCKED`）。
   **没验的部分**：没有起真 worker 观察四个任务同时跑；`pgxpool` 默认
   `MaxConns = max(4, CPU 核数)`，并发变高时可能出现短暂的连接等待（同步任务大部分
   时间在等上游 HTTP、并不握着连接，但这是推理不是实测）。若要保守，可临时把
   `cfg.MaxWorkers` 之外的推导关掉——但那等于退回单槽，把审稿 A 的问题放回去。

9. **`queue_slots` 是每次 `NewClient` 打一条的新事件。** 巡检脚本按事件名 grep 时
   会多出一行；`verify-real-mode.sh` 不消费它，已跑过部署门禁确认无影响。

---

## follow_ups

1. **组内扇出**：调低 `connectors/newapi/upstream.go` 的 `maxErrorRateChannels` /
   `maxTodayStatsChannels`，或给扇出本身一个 deadline。会改变渠道错误率覆盖度，
   需要前端同步。（风险 2）
2. **新增 `connector.ErrorKind` 区分「预算耗尽」与「上游 5xx」**：本片刻意没做——
   会连带库 CHECK、`alerts/rules.go` 判据、前端 13 组枚举，而
   `internal/platform/connector` 与 `web/` 都不在本片所有权内。handoff 三·2 要的
   「能区分」，日志已经够。建议单开小片。
3. **`credentials/connector_config.go:302-303` 的口径注释**是「没有行就是 fake」那套
   猜法的第三份拷贝（只是注释，不影响行为）。本片没动，建议下一片顺手改。
4. **`verify-real-mode.sh` 对 `RUN_ON_START` 的隐含依赖**（风险 7）：可以改成
   「窗口内没有 `connector_config_applied` 时降级为 warn」，但那会削弱这道闸，
   需要负责人拍板。
5. **告警语义（防抖、连续失败计数被成功清零、`fire_count` 当成「次数」显示）**属于
   子片 B，本片按派工**未动** `internal/platform/alerts/rules.go`。
6. **（审稿轮）显式设 worker 的 `pgxpool` 池大小。** `maintenance` 并发从 1 升到 4
   之后，默认 `MaxConns = max(4, CPU 核数)` 可能偏紧（River 自己还要连接跑领导者选举
   与通知）。要改得动 `cmd/platform-worker/main.go` 的连接串解析与 compose 变量，
   属于另一件事。（风险 8）
7. **（审稿轮）platform-api 里另外三处模式判定**：`platformpayments.go` 的
   `resolveSub2API` / `resolveNewAPI`、`platformusers.go` 的
   `dynamicUsersClient.resolve`。它们回答的是另一个问题（这次请求用哪个客户端），
   无行时回落各自的 `XM_PLATFORM_*_MODE`，本片刻意**没有**合并——合并会改动另外两个
   切片的端点语义。唯一真会分叉的点是「行在、mode 为空串」，今天靠迁移 000020 的
   `CHECK (mode IN ('fake','real'))` 不可达；**放宽那条 CHECK 的人必须回来看这三处**。
   已写进 `EffectiveConnectorConfig` 的文档注释。（审稿 D）
8. **（审稿轮）`RegisteredPeriodicJobSpecs` 的 `Queue` 全是 `maintenance`。**
   把重上游读取的任务真正分到独立队列仍然是更干净的形态，但要同步改
   `contracts/jobs/cluster-jobs.v1.json`（冻结契约）、`query_store.go` 的队列深度视图
   清单，并给新队列名配中文文案（`web/`，不在本片所有权内）。建议单开。
9. **`rules.go` 的规则注册点**（给 XM-CARD-VISIBILITY）：本片**一行都没改**
   `internal/platform/alerts/rules.go`。注册点是 `func Rules(cfg RuleConfig) []Rule`
   （`internal/platform/alerts/rules.go:261`）返回的那个切片字面量——在末尾追加一条
   `Rule{...}` 即可（顺序稳定，文档与测试逐条比对）。本片与它无冲突。
