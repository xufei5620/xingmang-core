# XM-JOBS0：「全局 → 后台任务」页接真实 River 数据

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-JOBS0`（base `ef0a85f`）

## commit

`9a22ce0` — feat(jobs): 后台任务页接真实 River 数据（XM-JOBS0）

## summary

「后台任务」页此前是纯只读蓝图（`JobsPage.tsx` 明确写着「没有 query、fetch
或 River client」）。本片按 ADMIN-IA §2.2 逐字子页签把它接成真实页面，后端
两个只读端点、前端整页重建。

### 后端：两个 Query 端点（scope `ops.read`）

- `internal/platform/jobs/query_store.go`（新）—— `QueryStore`，只依赖
  `*pgxpool.Pool`，不依赖 River 客户端本身（从不插入/修改任务，只读
  `river_job`/`river_queue`）：
  - `Overview(ctx, environment)`：遍历 `jobs.RegisteredPeriodicJobSpecs()`
    （目前 6 个：心跳/Sub2API 同步/NewAPI 同步/成本采集/告警评估/保留期
    清理），对每个 kind 查最近 2 条记录，推出「最近一次」「观测周期」
    「活跃度」；队列积压按 `queue,state` 分组计数（`completed` 只统计最近
    24 小时——River 自带的 job cleaner 会清掉更早的记录）；worker 心跳直接
    复用 `platform_heartbeat` 这一条目录项的最近一次记录，不另发查询。
  - `ListRuns(ctx, ListRunsInput{Environment, Kinds, State, Before, Limit})`：
    按 id 降序游标分页（「多取一行判断是否还有下一页」，与
    `ops.Store.ListSamples` 同一条纪律）；`Kinds` 支持多值（`kind = ANY(...)`），
    「同步批次」页签靠它一次查询拿到 sub2api_sync/newapi_sync/
    finance_cost_sync 三种，不必自己拼三条游标各翻各的页。
  - 错误文案截到 500 字节且不切碎多字节字符（`safeTruncateUTF8`），`errors`
    jsonb[] 用 `rivertype.AttemptError` 解码（不重新定义一份字段）。
  - `args` 只透出白名单字段（`environment`/`source`/`business_day`/
    `run_id`/`approval_envelope_sha256`），绝不把整段 River args 吐出去
    （团队交接的明确红线）。
- `internal/platform/httpapi/jobs.go`（新）—— `JobsOverviewHandler`、
  `ListJobRunsHandler`，响应 DTO 与仓储类型完全解耦（「响应体是契约，不是
  结构体的倒影」）。`?state=` 在拼 SQL 之前用 `jobs.ValidRunStates()`
  校验，非法值报 400 而不是让数据库枚举转换失败泄漏成 500；`?kind=` 接受
  逗号分隔的多个 kind。
- `internal/platform/httpapi/router.go`、`cmd/platform-api/main.go`：
  `Deps.Jobs JobsQuerier`，路由 `GET /jobs/overview` / `GET /jobs/runs`
  挂在 `RequireScope(ops.ScopeRead)` 下，**不做 nil 门禁**（与
  Metrics/Alerts 同档：river_job 是本平台自己数据库里的表，没有「这个环境
  没接」的情形，不像 RequestLogs/PlatformUsers 那样依赖外挂系统）。

### 一处主动偏离团队交接原始要求，如实记录

交接要求 `/jobs/overview` 报告每个周期任务「是否启用」。**没有做**：
platform-api 与 platform-worker 是两个独立进程，httpapi 进程读不到
worker 手上那份 `jobs.Config`（哪个任务被 `XM_*_ENABLED` 开关关掉了），
任何「已启用/已停用」的断言都会是装出来的事实，与宪法 12 条「禁止裸数字
冒充实时完整数据」同一条精神。改用 `river_job` 里能诚实观测到的
`Activity`（`activity_observed`/`no_recent_activity`/`never_observed`）
与从最近两次出现的时间差推算出的 `observed_interval_seconds`——明确标注
这是观测值不是配置值。前端不出现任何「已启用」字样（`JobsPage.test.tsx`
有一条回归用例专门锁定这一点）。R210 的 `EffectiveJobManifest`/
`BuildEffectiveManifest`（`internal/platform/jobs/effective_manifest.go`）
理论上能算出真实的启用状态，但今天没有被任何进程写进数据库或暴露成端点
（`grep` 全仓库确认只有 `jobs` 包自己的测试在用），接上它是一项独立工作，
见 follow_ups。

### 环境归属：如实反映「这批数据是平台级共享的」

验证下来，六个已注册周期任务的 `Args`（`HeartbeatArgs`/`Sub2APISyncArgs`/
.../`RetentionArgs`）今天**全部**只有 `run_id` 一个字段，没有一个带
`environment`。staging 与 production 共享同一个物理 Postgres（表级用
`environment` 列隔离，见 2026-08-30 验收记录），但 `river_job` 是 River
自带的 schema，本片不能也不应该去改它加一列（宪法「不改第三方」）。于是
这批维护型任务的运行记录**天然是平台级、跨环境共享的**。后端按
`args->>'environment' IS NULL OR args->>'environment' = $env` 做软过滤：
字段不存在时不筛（今天的全部情形），字段存在时按调用者环境过滤——为
未来某个任务的 Args 真的带上 `environment`（比如 reqlog_metrics 若按环境
拉取指标）自动生效，不需要再改这层代码。前端页头描述与 `worker_heartbeat.
environment` 恒为 `「platform」` 都如实说明了这一点，不假装存在一条今天
不成立的环境隔离。

### 前端

`web/apps/admin-web/src/api/jobs.ts`（新）—— 类型对齐后端 DTO，
`getJobsOverview`/`listJobRuns` 两个函数；`jobKindLabel`/`jobStateLabel`
把 kind/state 翻成中文，未知值原样暴露（不隐藏、不猜测，同 `ruleLabel`
的纪律）；`SYNC_JOB_KINDS` 供「同步批次」页签使用。

`web/apps/admin-web/src/pages/JobsPage.tsx`（重写）—— 五个子页签：

- **运行中**：`/jobs/runs` 不带 kind/state 过滤（全部最近记录）。
- **定时任务**：读 `/jobs/overview` 的 `schedules`，与顶部四格共用同一个
  react-query 缓存键（Radix 只挂载当前页签内容，实际请求只发一次）。
- **同步批次**：固定 `kinds=[sub2api_sync, newapi_sync, finance_cost_sync]`。
- **失败与重试**：固定 `state=retryable`。
- **多次失败任务**：固定 `state=discarded`。

后三者与「运行中」共用同一套 `DataTableV2` + 游标「加载更多」（`useInfiniteQuery`，
`footerExtra`，不设 `pageSize`——与 `AuditPage.tsx` 同一条纪律，服务端游标
分页与客户端分页叠一层会出现两种「下一页」）。顶部四格（运行中任务/Worker
心跳/失败待重试/多次失败任务）与队列积压小表替换了原来的 `BoundaryCard`
占位。每个空态都保留了原页面「没有数据不代表队列/任务系统正常」的措辞，
只是按新的数据模型重新措辞（例如「请确认 Platform Worker 进程在跑」）。

## files_changed

后端（5，其中 3 新建）：

- `internal/platform/jobs/query_store.go`（新）—— 仓储层
- `internal/platform/httpapi/jobs.go`（新）—— Handler + DTO
- `internal/platform/httpapi/router.go` —— 挂载两条路由、`Deps.Jobs`
- `cmd/platform-api/main.go` —— 装配 `jobs.NewQueryStore(pool)`
- `internal/platform/httpapi/testhelpers_test.go` —— 新增
  `testRouterWithJobs` 测试辅助函数

后端测试（3 新建）：

- `internal/platform/jobs/query_store_test.go` —— 纯函数单测（截断/白名单
  过滤/错误解码/活跃度窗口/DurationMS），不需要数据库
- `internal/platform/jobs/query_store_integration_test.go` —— 真实
  Postgres 集成测试（`XM_TEST_DATABASE_URL`/`XM_RUN_INTEGRATION=1` 门控，
  与仓库既有 `*_integration_test.go` 同一套约定）
- `internal/platform/httpapi/jobs_test.go` —— Handler 单测（fake
  `JobsQuerier`，覆盖鉴权/环境校验/参数解析/多 kind 解析/错误脱敏/响应
  形状/回归用例「响应体绝不出现 enabled 字段」）

前端（4，其中 2 新建）：

- `web/apps/admin-web/src/api/jobs.ts`（新）—— API 客户端 + 类型
- `web/apps/admin-web/src/api/jobs.test.ts`（新）—— 客户端单测
- `web/apps/admin-web/src/pages/JobsPage.tsx`（重写）—— 真实页面
- `web/apps/admin-web/src/pages/JobsPage.test.tsx`（重写）—— 页面测试

## tests_run

后端（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy
-u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 前缀，
见 `windows-toolchain-quirks` 记忆里的既定坑）：

- `go build ./...` —— PASS
- `go vet ./...` —— PASS
- `go test -p 1 -count=1 ./...` —— PASS，全仓库无 FAIL（含本片新增的三个
  测试文件）
- `"$(go env GOROOT)/bin/gofmt" -l <本片改动的 8 个 .go 文件>` —— 干净
  （用工具链自带 gofmt，不信 PATH 上那个，见既有坑记录）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- **真实 Postgres 集成测试**（本片新增的 3 个 `TestQueryStore*
  PostgresIntegration` 测试）：用 `xingmang-launch-postgres-1`（本机已在跑，
  无发布端口）+ 共享网络命名空间的配方，建了一次性 scratch 库
  `xm_jobs0_test`、跑 `jobs.Migrate` 建好 River schema、灌了顶层
  `db/migrations/*.up.sql`（`ops`/`finance`/`audit` 等应用 schema，供同包
  其它既有集成测试使用）、`GRANT xingmang TO <scratch role>` 补齐属主权限
  （`DROP RULE`/触发器需要）后跑 `go test -p 1 -count=1
  ./internal/platform/jobs/...`：**全绿，包括本片新增的 3 个测试与仓库
  既有的全部 jobs 包集成测试**（sub2api/newapi sync、retention prune 等）。
  复跑了两遍（含 XM-JOBS0 开发中途新增的多 kind 过滤能力）确认无残留状态
  污染。跑完 `rm -rf .gocache`、`DROP DATABASE`/`DROP ROLE` 清理，未在
  worktree 或宿主库留下痕迹。

前端（`web/` 目录，`--config.verify-deps-before-run=false` 跳过 pnpm 依赖
校验，见既定坑记录）：

- `pnpm --filter admin-web run typecheck` —— PASS
- `pnpm --filter admin-web run test` —— PASS，80 个测试文件、1160 个用例
  全绿（含本片新增/重写的两个测试文件）
- `pnpm --filter admin-web run build` —— PASS（`tsc --noEmit && vite
  build`；907 KB 单 chunk 的体积警告是既有的，与本片无关，产物体积较
  改动前略增属预期——新增了完整的表格与查询逻辑）
- `pnpm -r run typecheck`（整个 `web/` workspace：design-tokens/
  ui-primitives/ui-storybook/ui-admin/admin-web）—— PASS

## not_run

- **真实浏览器 / Playwright 实测**：未跑。本片没有涉及横向滚动、粘性列等
  只有真浏览器布局引擎才能验证的问题（`windows-toolchain-quirks` 记忆的
  判断标准），DataTableV2/PageState 都是仓库里已经在多张真实表格上验证过
  的既有组件，vitest + jsdom 的组件测试对新增的数据映射/查询参数拼接
  逻辑置信度足够。
- **Storybook**：未跑 `pnpm --filter ui-storybook run build`。本片没有
  新增或修改任何 `ui-admin`/`ui-primitives` 组件，只是复用既有的
  `DataTableV2`/`PageState`/`StatTile`/`PageHeader`，无需补 Storybook
  素材。
- **对真实 staging/production 环境的验证**（真实 River 数据、真实
  Platform Worker 跑起来之后点开这五个页签）：未跑，需验收线合入部署后
  核实。集成测试用的是手工灌入的合成 `river_job` 行，不是真实 worker 写入
  的记录——字段形状经过 `rivertype.AttemptError`/schema 约束校验，应该
  一致，但没有对着一个真正跑着的 worker 实测过。
- **`reqlog_metrics` job kind**（团队交接提到「另一代理并行开发，可能不在
  你分支上」）：本分支上不存在这个 kind（已用 `grep` 确认），未做任何
  假设性适配。`RegisteredPeriodicJobSpecs()` 是 Overview 目录的唯一数据源，
  它合入后会自动出现在「定时任务」页签，`/jobs/runs?kind=` 也能立刻按它
  过滤——不需要为它单独改这一片的代码，但也没有专门针对它写测试（因为它
  现在不存在，写了也测不了真实形状）。

## risks

- **「活跃度」是推断，不是真相**：`Activity`/`ObservedIntervalSeconds` 由
  最近两次出现的时间差算出，窗口取观测周期的 3 倍（下限 1 小时）。如果
  某个任务的实际周期发生剧烈变化（比如运维把 `XM_RETENTION_INTERVAL` 从
  24h 改成 5min），窗口需要经过「至少两次新周期的观测」才能追上新节奏，
  期间可能短暂显示「长时间无活动」（其实只是刚改完配置）。这是设计上刻意
  接受的代价——诚实的「不确定」好过一个装出来的「已启用/已停用」。
- **`args` 白名单可能需要随新任务扩充**：目前白名单是
  `environment`/`source`/`business_day`/`run_id`/
  `approval_envelope_sha256` 五个字段，全部来自现有六个任务 + 手动归档
  任务的 Args 定义。未来新任务如果 Args 里有值得展示的新字段（比如
  `reqlog_metrics` 若带 `environment`），需要显式加进
  `internal/platform/jobs/query_store.go` 的 `runArgsAllowlist`——不加就是
  静默不显示，不会报错，需要人工记得去补。
- **队列积压的 `completed_24h` 依赖 River 默认的 job cleaner 保留策略**：
  `NewClient` 没有显式配置 `river.Config` 的清理保留期，用的是 River OSS
  默认值。如果未来有人改了保留期配置，`completed_24h` 这个名字本身没变但
  语义窗口和实际保留窗口可能不完全对齐（比如保留期短于 24h 时，这个数字
  会比「真实 24 小时内完成数」更小）。当前默认值下两者一致，未验证过非默认
  配置下的行为。
- **`/jobs/runs` 的 `state` 过滤依赖 `river_job_state` 恰好七个值**：新增
  枚举值需要同时改 `jobs.ValidRunStates()`；忘改的后果是前端传新状态会被
  拒成 400，而不是查出一个空集合（fail closed，比较安全，但会阻塞使用
  新状态筛选，需要留意）。

## follow_ups

- **真正的「是否启用」**：把 `jobs.BuildEffectiveManifest` 接进一个由
  platform-worker 启动时写入数据库（或类似 R210 的签名 fleet manifest）
  的机制，`/jobs/overview` 再读那张表，才能诚实回答「这个任务今天是不是
  真的开着」。当前实现只给了「观测到的活跃度」这个替代品，作为一个明确的
  产品缺口记录，不在本片范围内解决。
- **环境软过滤目前是空转的**：等某个周期任务的 Args 真的加上
  `environment` 字段（例如 `reqlog_metrics` 若按环境采集），这条软过滤
  会自动生效；如果产品侧确认「所有维护型任务确实应该保持平台级、不拆分
  环境」，也可以反过来把这条过滤逻辑连同 UI 上的 `environment` 参数一并
  简化掉——本片选择先按交接文档的字面要求实现，把决定权留给产品侧。
- **可以考虑把 kind/state 筛选状态放进 URL search params**（类似 `?sub=`
  的深链模式），目前每个页签的 kind/state 是硬编码的（页签定义本身），
  只有 DataTableV2 内置的「类型」/「状态」客户端筛选是可交互的、不进 URL——
  与 `AuditPage.tsx` 的「结果」筛选同一条现状，不是本片独有的缺口。
