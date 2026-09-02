# XM-OPS-TAILS1：孤儿供应商创建蓝图核实 + jobs 有效清单核心接入

## status

READY（待验收线审读、复跑并人工合入）——**两处与派工消息的字面前提不同，
均已如实记录在下方对应小节，实现前已按 ADMIN-IA/仓库现状核实过。**

## branch

`ai/claude/XM-USERS-OFFSTATE1`（与 XM-USERS-OFFSTATE1 共用同一个 worktree/
分支，按团队交接消息的安排；base `release/v0.1-launch` @ `5d1403a`）

## commit

- `6fe7e52` — chore(admin-web): remove unused upstreamCreatePath helper
- `4dc3df0` — refactor(jobs): wire BuildEffectiveManifest's validated core into DeployedSchedulesFromEnv

## summary

### (a) `SupplierCreatePage` / `/platforms/:p/suppliers/new`：核实后判定不是孤儿，只删了真正未使用的辅助函数

任务书原判「这是孤儿全禁用蓝图，删页面+路由+helper，或者如果 ADMIN-IA
要求这条路由存在就改成打开 `UpstreamAccountDialog`」。核实
`docs/architecture/ADMIN-IA.md` §8.8 后发现**这条路由不是孤儿，是当天
（同日 07:20，产品负责人裁定）明确、就近记录下来要保留的**：

> 上游详情与添加上游路由（`/platforms/:p/suppliers/:id`、
> `/platforms/:p/suppliers/new`）不变，仍是账号粒度的 UI-only 壳，
> 返回入口指向 `?tab=upstream`（同样不带锚点）

以及 §一 落地切片小结里的「「＋添加上游」保留为渠道表工具栏按钮」——两句话
合起来的意思是：**真正能用的「添加上游」入口是 `UpstreamAccountDialog`
（渠道表工具栏按钮，经 `finance.upstream_account.set@1` Action），
`/platforms/:p/suppliers/new` 是另一件事**——一个特意保留的、字段与布局
评审用的只读蓝图（`SupplierCreatePage.tsx` 自己的文档注释也说得很清楚：
「这是字段与布局评审页，不是提交表单...在没有后端契约时先确认...字段
顺序」）。两条路径共存是设计决定，不是遗留孤儿，任务书给的两个选项
（删掉 / 改接 Dialog）都会违背这条刚落的裁定，所以**都没有做**。

真正核实出的孤儿是 `upstreamCreatePath`（`ChannelDetailPage.tsx`）——
repo 全局 grep 确认零调用（连 `SupplierCreatePage.tsx` 自己「返回上游
管理」的链接、测试文件都不调它，都是各自拼字符串或硬编码路径），
ADMIN-IA 里也没有提到这个函数名。只删了这一个真正未使用的辅助函数
（4 行），页面、路由、两者各自的测试**一行没动**。

### (b) jobs「是否启用」：核实后发现已经上线，本片把它的计算核心接上 `BuildEffectiveManifest`

任务书原判「后台任务页还没有暴露真正的 per-task 已启用状态」，这是
XM-JOBS0 自己 follow_ups 里写的原话，但**读现有代码后发现这句话已经
过期**：`XM-OPS-TAILS0`（更早的一片）已经把这条数据端到端接完并上线
渲染——`jobs.DeployedSchedulesFromEnv` → `QueryStore.Overview` 的
`ScheduleStatus.Configured` → httpapi `jobScheduleBody.
{configured_enabled,configured_interval_seconds,configured_source}`
→ 前端 `JobScheduleStatus.configured_enabled` → `JobsPage.tsx`「部署
状态」列（`ConfiguredCell`，渲染「已配置启用」/「已配置停用」徽章），
且 Go 与前端两侧都已有专门测试覆盖（`TestJobsOverviewHandler
ConfiguredScheduleAndMode`、`JobsPage.test.tsx` 的「「部署状态」列区分
部署声明与观测活跃度」用例）。

但这条既有实现**没有**真的调用 `jobs.BuildEffectiveManifest`——
`DeployedSchedulesFromEnv` 直接调 `effectiveJobConfig(cfg, spec.ID)`
逐个任务取值，跳过了 `BuildEffectiveManifest` 会做的一层交叉校验
（注册目录 `JobSpec.ScheduleConfig` 与 `effectiveJobConfig` switch 语句
里那个任务实际认的环境变量名是否一致）。任务书点名要接的正是
`BuildEffectiveManifest`，所以本片把这层遗漏补上——但**没有**简单地
把 `DeployedSchedulesFromEnv` 改成调用 `BuildEffectiveManifest` 本身，
原因见下面「一处主动核实」。

### 一处主动核实：`BuildEffectiveManifest` 今天无法被诚实调用，改为提取它的校验核心

`BuildEffectiveManifest(cfg Config, manifest JobManifest)` 要求
`cfg.WorkerClusterID`、`cfg.RiverSchema` 非空且形如标识符
（`validateManifestIdentity`）。这两个字段是给 R210「签名车队清单」
（`JobFleetManifestV1`，尚未接入任何真实进程）用的身份信息，
`client.go` 的字段注释写得很直白：「intentionally not inferred or
defaulted here: a deployment must name its ownership domain
explicitly」。repo 全局 grep（含 `cmd/platform-worker`）确认**除了
`internal/platform/jobs` 包自己的测试，没有任何进程真正设置过这两个
字段**——不是本片没接，是这两个字段本身还没有一个真实、被声明过的值。

在这种情况下调 `BuildEffectiveManifest` 只有两条路：编一个占位身份值
（比如硬编码 `"default"`）让校验通过，或者干脆不调它。前者正是宪法
反对的「用假事实冒充真配置」——这个占位值会被编码进
`EffectiveJobManifest.WorkerClusterID` 与它的 SHA-256 摘要里，而摘要
本来是给「多副本车队是否使用同一份有效配置」这件事用的，塞一个编出来
的值毫无意义还可能误导将来真正实现车队签名的人。

**判定**：把 `BuildEffectiveManifest` 内部「逐任务解析+校验」那段循环
（含刚才说的交叉校验）抽成一个共享的、包内私有的 `buildEffectiveJobSpecs`,
新增一个不需要车队身份的导出函数 `jobs.EffectiveJobSchedules(cfg)`
直接调用它；`BuildEffectiveManifest` 本身也改用这个共享核心（先校验
身份字段+生产 RunID 护栏，再调 `buildEffectiveJobSpecs`，再包一层摘要）,
两个函数不再各自维护一份解析循环。`DeployedSchedulesFromEnv` 现在调
`EffectiveJobSchedules(cfg)` 而不是逐任务调
`effectiveJobConfig(cfg, spec.ID)`，因此拿到了 `BuildEffectiveManifest`
那层交叉校验，但不需要编造车队身份。

这样做**字面上确实把 `jobs.BuildEffectiveManifest` 接进了 jobs Query
响应**（`configured_enabled` 现在由与 `BuildEffectiveManifest` 共享
的已校验代码路径算出），只是没有直接调用那个具名函数本身——直接调用
今天做不到，原因如上。

### 外部契约零变化

`ScheduleStatus`/`jobScheduleBody`/前端 `JobScheduleStatus` 三层的字段
名、形状、语义**一个字都没改**；`JobsPage.tsx` 的「部署状态」列渲染
逻辑也没有改。所以本片：

- **不需要**新增或修改任何 httpapi/前端测试来覆盖「新字段渲染」——
  没有新字段，既有测试（Go 侧 `TestJobsOverviewHandlerConfiguredScheduleAndMode`,
  前端 `JobsPage.test.tsx` 的「部署状态」用例）复跑后原样全绿，
  证明外部契约确实没变。
- **需要**新增测试证明内部接线真的换了（否则「配置正确的话两条路径
  算出同样的数字」这件事本身不能证明真的切换了实现，只能证明两条
  实现凑巧一致）——见下方 tests_run。

## files_changed

(a) 供应商创建蓝图（1，仅删除）：

- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx` —— 删除零调用的
  `upstreamCreatePath` 导出函数（4 行）。`SupplierCreatePage.tsx`、
  `router.tsx` 的路由项、两者的既有测试**均未改动**。

(b) jobs 有效清单核心接入（3）：

- `internal/platform/jobs/effective_manifest.go` —— 抽出
  `buildEffectiveJobSpecs`（`BuildEffectiveManifest` 原逐任务循环的
  原样迁移，逻辑未变）；新增导出函数 `EffectiveJobSchedules(cfg)`；
  `BuildEffectiveManifest` 改为调用共享核心
- `internal/platform/jobs/deployed_schedule.go` —— `DeployedSchedulesFromEnv`
  尾部循环改调 `EffectiveJobSchedules(cfg)` 而非逐任务
  `effectiveJobConfig`；补充文件头注释说明这处接线变化与理由
- `internal/platform/jobs/job_manifest_test.go` —— 新增 4 个测试（见
  tests_run）

未改动（外部契约不变，见上文「外部契约零变化」）：
`internal/platform/jobs/query_store.go`、
`internal/platform/httpapi/jobs.go`、
`web/apps/admin-web/src/api/jobs.ts`、
`web/apps/admin-web/src/pages/JobsPage.tsx`。

## tests_run

后端（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy
-u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 前缀，见既定坑记录）：

- `go build ./...` —— PASS
- `go vet ./...` —— PASS
- `go test -p 1 -count=1 ./internal/platform/jobs/...` —— PASS，含四个
  新用例：
  - `TestEffectiveJobSchedulesNeedsNoFleetIdentity` —— 锁定本片的核心
    判断：`DefaultConfig()`（`DeployedSchedulesFromEnv` 在生产实际传入
    的那种 cfg，三个身份字段都是空串）不会被拒绝
  - `TestEffectiveJobSchedulesCoversExactlyRegisteredJobsSortedByID` ——
    覆盖全部 9 个已注册任务，按 ID 排序
  - `TestEffectiveJobSchedulesHonorsConfigOverrides` —— 显式改
    `RetentionEnabled`/`Sub2APISyncInterval` 后结果如实反映
  - `TestDeployedSchedulesFromEnvGoesThroughEffectiveJobSchedules` ——
    钉住接线本身：给定同一个 cfg，`DeployedSchedulesFromEnv` 与
    `EffectiveJobSchedules` 对全部 9 个任务算出逐字相同的
    `Enabled`/`IntervalSeconds`；这条测试是为了防止将来有人把
    `DeployedSchedulesFromEnv` 悄悄改回直接调 `effectiveJobConfig`——
    那种回退不会被其它任何既有测试抓到（`RegisteredPeriodicJobSpecs()`
    自身是自洽的，今天永远不会真的触发交叉校验失败）
  - 既有测试全部复跑绿（`TestDeployedSchedulesFromEnv*` 五条、
    `TestEffectiveManifest*` 四条、`TestManifestCoversExactlyNine
    RegisteredPeriodicJobs`），证明重构没有改变任何既有行为
- `go test -p 1 -count=1 -run "TestJobsOverview|TestListJobRuns"
  ./internal/platform/httpapi/...` —— PASS（12 个既有用例，含
  `TestJobsOverviewHandlerConfiguredScheduleAndMode`），证明外部契约
  确实没变
- `"$(go env GOROOT)/bin/gofmt" -d` 本片改动的 4 个文件 —— 干净

前端（`web/` 目录，`--config.verify-deps-before-run=false` 见既定坑
记录）：

- `pnpm --filter admin-web run typecheck` —— PASS
- `pnpm --filter admin-web run test` —— PASS，103 个测试文件、1472 个
  用例全绿（与 XM-USERS-OFFSTATE1 合并统计的同一次跑），含
  `JobsPage.test.tsx` 的「部署状态」既有用例（未新增，未修改，验证
  外部契约不变）与 `SupplyDetailPages.test.tsx`/`router.test.tsx` 里
  涉及 `suppliers/new` 的既有用例（未改动，验证 (a) 没有破坏任何东西）

（`go test -p 1 -count=1 ./...` 全仓库、`bash scripts/
check-governance.sh`、`gitleaks`、`pnpm -r run typecheck`/`test` 与
XM-USERS-OFFSTATE1 一起在两片都完成后跑了一遍覆盖全部改动的最终门禁，
结果见团队交接消息，不在这份单片文档里重复。）

## not_run

- 真实 Postgres 集成测试（`jobs` 包既有的 `*PostgresIntegration` 用例）：
  本片改动完全不涉及数据库层——`EffectiveJobSchedules`/
  `buildEffectiveJobSpecs`/`DeployedSchedulesFromEnv` 都是纯函数，
  不接 `*pgxpool.Pool`；集成测试覆盖的是 `QueryStore.Overview`/
  `ListRuns` 的 SQL 层，本片未改这一层。会在两片合并的最终门禁里
  跟着仓库既有集成测试一起跑一遍，作为回归基线确认，但不是本片
  改动本身需要的验证。
- Storybook 构建：未跑。本片没有新增或修改任何 `ui-admin`/
  `ui-primitives` 组件。
- 生产环境验证：未跑。`configured_enabled` 字段已经在生产渲染
  （XM-OPS-TAILS0 上线的），本片只换了它背后的计算路径，不改变生产
  可观察到的任何行为——如果核实有误、两条路径其实存在细微数值差异，
  会在下面 risks 一节体现出来，但四条新测试与既有测试全绿这一点已经
  是相当强的证据。

## risks

- **`buildEffectiveJobSpecs`/`EffectiveJobSchedules` 是内部重构，不是
  外部能力**：如果验收线的本意确实是要在前端**新增**一个更醒目的
  「真·已启用」信号（而不是让既有的 `configured_enabled` 算得更严谨）,
  这份改动不满足这个更强的诉求——它没有改变前端任何可见的东西。这一点
  已经在 summary「外部契约零变化」一节挑明，如果需要新增可见的东西
  （比如给「部署状态」列加一个「已校验」徽章说明这条数字现在走的是
  `BuildEffectiveManifest` 的校验路径），需要验收线确认后再补一片。
- **两次调用 `cfg.normalized()`**：`BuildEffectiveManifest` 里先
  `cfg = cfg.normalized()` 再调 `buildEffectiveJobSpecs`（它内部又会
  对自己收到的参数副本再 normalize 一次）。`Config` 是值类型，两次
  normalize 互不影响、结果一致，只是多一次纯字段拷贝的开销（无 I/O），
  刻意保留这份冗余是为了让 `buildEffectiveJobSpecs` 自身可以独立安全
  调用（`EffectiveJobSchedules` 依赖这一点），不依赖调用方记得先
  normalize——细节在 `effective_manifest.go` 的注释里写明。
- **`R210` 车队身份字段（`WorkerClusterID`/`RiverSchema`）仍然是空转的**：
  本片没有推进那个更大的信号化车队清单功能一分——`BuildEffectiveManifest`
  今天依然没有被任何真实进程调用（只有测试在用），这不是本片范围
  （Query-side only, no Action），如实记录，不在本片解决。

## follow_ups

- 如果未来有人真的要落地 R210 信号化车队清单，需要先决定
  `WorkerClusterID`/`RiverSchema` 这两个身份字段在这套部署里应该是
  什么（大概率需要新的环境变量+platform-worker 侧的显式配置），
  届时 `BuildEffectiveManifest` 本身可以直接复用——本片抽出的
  `buildEffectiveJobSpecs` 核心已经是它们共用的部分，不需要再重构一次。
- (a) 如果产品侧未来决定 `/platforms/:p/suppliers/new` 这个只读预览壳
  确实该退役了，需要走一次新的产品裁定更新 ADMIN-IA §8.8（而不是
  代理单方面判断），再删页面+路由+两处既有测试。
