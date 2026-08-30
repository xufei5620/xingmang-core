# XM-OPS0 · 平台治理「运行保障」页真实数据接通

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-OPS0`
- implementation commit: `6cba5d6`（本分支 HEAD，单提交，含代码+测试+部署配置）
- 本文件在后续一个小提交里加入（handoff-only commit）
- base: `ef0a85f`（`release/v0.1-launch` 或等价发布线的当时 HEAD）
- worktree: `K:/星芒统一控制平台/acceptance/wt-ops0`

## summary

把「平台治理 → 运行保障」页从占位做成真实页面，范围限定在 ADMIN-IA 定义的
6 个子页签中的第 1 个——「控制平面健康」；其余 5 个（稳定性与外部监控 / 备份
与恢复 / 故障处理手册 / 迁移与数据对比 / 模型质量保障）按 ADMIN-IA §九 本就
标注为 M1/M1.5 之后的范围，本片如实显示「未接入」并注明缺什么数据源，不
提前造后端。

### 后端

**新周期任务 `connector_probe`**（`internal/platform/jobs/connector_probe.go`）：

- 每轮对**当前生效**的 sub2api / newapi 连接器调用 `Version()` 与
  `Health()`。「当前生效」与 sub2api_sync / newapi_sync 用**同一个**解析
  路径（`cfg.sub2apiClientFactory()` / `cfg.newapiClientFactory()`）——不
  会有第二套「这个环境该连哪里」的判断逻辑漂在探测任务里。
- 写观测 `sub2api.connector.health` / `newapi.connector.health`，
  `value = {"version","supported","healthy","kind","latency_ms","checked_at"}`
  （`kind` 是 `HealthResult.ErrorKind`，健康时为空串），新鲜度阈值固定
  **900 秒**（与可配置的采集周期解耦，按需求原文写死）。
- Version/Health 合并成一条观测：任一次读取失败（或客户端本身建不出来）
  整条记 `SyncFailed`，失败时保留上一次成功的 `Watermark`/`Value`/
  `ObservedAt`（镜像 `Sub2APISyncWorker.failureObservation` 的写法），让
  仪表盘显示的是「正在变旧」而不是「数据消失」。
- **production + fake 不探测，写 not_supported**：这条约束**不是**探测
  任务自己判断的——`NewDynamicSub2APIClientFactory`/
  `NewDynamicNewAPIClientFactory`（XM-CRED0 既有代码）每轮已经会在
  `mode==fake && environment==production` 时拒绝交出客户端
  （`connector.KindNotSupported` 包 `ErrConnectorProductionFake`），探测
  任务只是像 sync 任务一样把这个错误如实记成 `SyncFailed`。另外在
  `Config.validate()` 里补了一条**独立**闸（不与 Sub2API/NewAPI 的闸合并
  ——探测能在两条同步都关掉时单独打开，因此也要能单独触发/单独关闭）：
  `ConnectorProbeEnabled && production && ConnectorConfigs==nil &&
  (Sub2APIMode==fake || NewAPIMode==fake)` 时启动即拒。这条闸只在
  `ConnectorConfigs==nil`（没有动态配置来源）时才可能触发——`cmd/
  platform-worker/main.go` 一直无条件装配 `ConnectorConfigs`，所以生产
  实际部署走的始终是前一条（每轮 fail closed 的）保护，这条是给缺省装配
  的嵌入者/测试兜底的纵深防御。
- 新增 Config 字段 `ConnectorProbeEnabled`（默认 true）/
  `ConnectorProbeInterval`（默认 5 分钟）/ `ConnectorProbeRunOnStart`
  （默认 true，无对应环境变量，与其它任务的 RunOnStart 同款——只有默认值，
  没有运行时旋钮）/ `ConnectorProbeRunID`（生产禁止）。已接入
  `job_manifest.go` 的六→七任务清单、`effective_manifest.go`、
  `contracts/jobs/cluster-jobs.v1.json`，三处测试同步更新。

**`heartbeat.go` / `retention.go` 补写观测**（都是可选、向后兼容的新增）：

- `HeartbeatWorker` 新增 `WithObservations(store, interval) *HeartbeatWorker`
  fluent setter（没有改动现有两参数构造函数签名，两个现有调用点不受影响）
  ——设了它之后每次心跳成功额外写一条 `platform.heartbeat` 观测
  （`value={"job_id","attempt"}`，阈值 180s = 3× 默认心跳周期）。这是运行
  保障页回答「worker 还活着吗」的唯一依据：不是新起一个存活探测接口，而是
  让心跳本身的新鲜度自然承担这个信号——心跳一旦停摆，staleness 自己会涨。
- `RetentionOptions` 新增可选字段 `Observations`/`ExpectedInterval`：设了
  之后每轮清理额外写一条 `platform.retention.last_run` 观测
  （`value={"metric_samples_deleted","resolved_alerts_deleted",
  "sample_retention_days","alert_retention_days"}`，阈值 172800s = 2 天，
  对应默认 24h 周期）。
- 两处都是**新增可选字段/方法**，不是签名修改；现有全部测试原样通过。

**新 Query `GET /api/v1/ops/overview`**（`internal/platform/httpapi/
ops_overview.go`，scope `ops.read`，同 `/metrics` 的环境裁剪规则）：

响应聚合 7 段，逐段列出数据来源（team lead 任务描述里的每一项都能对应到
一段）：

| 响应字段 | 内容 | 来源 |
|---|---|---|
| `build` | 版本/commit/环境 | `buildinfo.Version/Commit` + 已解析的请求环境 |
| `worker_heartbeat` | 最近心跳观测 + 距今 | `platform.heartbeat` 观测 |
| `sync_pipelines[2]` | 每条同步任务的 kind/生效模式/source/新鲜度 | `credentials.Store.ListConnectorConfigs`（生效模式）+ 该平台的 `{sub2api,newapi}.channels.status` 观测（新鲜度代表值） |
| `connector_health[2]` | 连接器版本/支持性/健康/延迟 | `{sub2api,newapi}.connector.health` 观测（上面新任务写的） |
| `alert_delivery` | Telegram/Webhook 是否配置（仅布尔值） | `cmd/platform-api` 进程启动时读取的三个 `XM_ALERT_*` 环境变量（**只判断非空，从不解析明文**） |
| `retention` | 上一次清理结果 | `platform.retention.last_run` 观测 |
| `database` | 数据库连通性 | 与 `/readyz` 同一个 `Pinger.Ping()`，同 2s 超时，嵌入响应而不是要求前端再打一次 |

设计取舍（供审阅时核对是否符合预期）：

- `sync_pipelines` 每条同步任务只挑**一个代表性指标**
  （`{platform}.channels.status`）汇总新鲜度，不是把该平台全部指标做
  「最差状态」聚合——保持简单、可解释（响应里的 `sample_metric_key` 字段
  明确写出是哪个指标撑起这一行，便于排查）。sub2api_sync/newapi_sync 各
  写 5~6 个指标，逐个铺开不是这次需要的粒度。
- 生效模式来自 `credentials.Store.ListConnectorConfigs`（与 XM-CRED0 的
  `GET /connectors/config` 同一个仓储，没有开第二条访问 `core.
  connector_config` 的路径）；`ConnectorConfigs` 为 nil（凭据模块未挂载）
  时响应里 `config_available=false` 且 `effective_mode=""`，不猜是
  fake——诚实展示「这个部署没有这个模块」而不是伪装成已知状态。
  没有配置行（模块挂载了但这个平台没配过）时报 `"fake"`，与 worker 自己
  的约定一致（`credentials.Store.ListConnectorConfigs` 文档注释里写明的
  「没有行的平台按 fake/未接入处理」）。
- `alert_delivery` **不是**从数据库读的：这是本片唯一一处「不完全只读
  DB/观测」的地方，细节与理由见下面的 risks。

**4 个新 metric_key 的登记**（按 team lead 要求「按 ops 的登记方式登记」，
逐项落实）：

1. `internal/platform/ops/freshness.go` 白名单加 4 条（`platform.
   heartbeat` / `platform.retention.last_run` / `sub2api.connector.health`
   / `newapi.connector.health`），沿用现有约定——字面量而非
   `RegisterMetricKey` 动态注册。**这是刻意的取舍，不是抄近路**：
   `RegisterMetricKey` 的注册是**进程级**的，platform-api 与
   platform-worker 是两个独立进程；如果只在 `internal/platform/jobs` 的
   `init()` 里注册，platform-api 进程（不 import jobs 包）永远不会执行到
   那次注册，`GET /metrics/history?metric_key=sub2api.connector.health`
   在 platform-api 里会被 `KnownMetricKey` 拦成未注册 400——即使 DB 里
   已经有数据。字面量写进 `freshness.go` 能保证两个二进制看到同一份白名单，
   这也是现有全部 16 条指标采用的方式（`RegisterMetricKey` 目前唯一的
   调用方是它自己的单元测试，没有生产调用点）。
2. `internal/platform/ops/metrickeys_test.go` 的 `TestRegisteredMetrics
   MatchConnectorContracts` 加 4 条（import `internal/platform/jobs` 取
   常量，与已有的 `finance.MetricProfitDaily` 同一先例——`ops_test` 是
   外部测试包，可以同时看见两边而不构成生产环境的导入环）。
3. `contracts/ops/metric-rollup-policy.v1.json` 加 4 条 policy（都是
   `value_kind=gauge / primary_kind=count / sum_mode=forbidden`，
   `primary_json_pointer` 分别指向 `/latency_ms`（两条连接器健康）/
   `/attempt`（心跳）/ `/metric_samples_deleted`（保留期清理））——这是
   **判断调用**：这四个指标都没有天然的「金额」或「多子项汇总」语义，
   选哪个字段当 rollup 主值是本片实施方的取舍，不是需求文档指定的，值得
   复核（细节见下面 risks）。`internal/platform/ops/rollup_policy_test.go`
   的硬编码计数 14→18 同步更新。
4. `internal/platform/jobs/job_manifest.go` / `effective_manifest.go` /
   `contracts/jobs/cluster-jobs.v1.json` 三处的「六个周期任务」清单扩到
   七个（`connector_probe`），`job_manifest_test.go` 五处硬编码断言
   （数量、`want` 映射表、`checks` 切片、`intervalForJob`、
   `TestProductionArgsContainNoReplicaRunID` 的参数清单）同步更新。

**为什么 Sub2API/NewAPI 的 production+fake 启动闸没有被扩展成包含
`ConnectorProbeEnabled`**：最初的实现把探测开关直接 OR 进两条既有闸
（`(Sub2APISyncEnabled || ConnectorProbeEnabled) && Sub2APIMode==fake && ...`），
结果撞坏了三个既有测试——它们靠「关掉 SyncEnabled 就能在 production+fake
默认值下启动」来隔离测试其它闸（`TestSub2APIFakeModeRejectedInProduction`
的 `off`/`realMode` 分支、`TestNewAPIFakeModeRejectedInProduction` 的两个
「显式关闭后应放行」分支、`TestFinanceCollectFakeIsRefusedInProduction`）。
改法是让探测走**自己独立的**闸（见上面 summary），并给这三个测试文件各加
一行 `X.ConnectorProbeEnabled = false`——这不是削弱测试，是它们本来就在
用的「隔离出被测那一条闸」手法多加一环，理由已写进各处的行内注释。

### 前端

`/ops` 路由从占位页换成真实页面（`web/apps/admin-web/src/pages/
OpsPage.tsx`），结构照抄 `AuditPage.tsx` 的模式：`?sub=` 同步到 URL、
未知子页显示「「X」子页尚未接入」而不是静默回落。6 个子页签 id/中文标签
逐字取自 `navigation.ts`（本就是对的，只把 `built` 从 `false` 翻成
`true`）：

- **控制平面健康**（真实数据）：接 `getOpsOverview`（`api/ops.ts`），
  `ApiStateView` 处理 loading/error/denied 三态，`DataTableV2` 渲染 6 行
  （组件/环境/状态/数据新鲜度/最近心跳/依赖/最近错误——这 7 列取自
  `OPS_BLUEPRINT` 里本就画好的表头），上方一条摘要栏显示 build 版本/
  commit、告警投递配置、数据库连通性。
- 其余 5 个子页签：`PageState kind="unavailable"`，文案取自
  `OPS_BLUEPRINT.tabs[].source`（已经写好的、经过审阅的说明文字，本片
  原样复用，未新写措辞）。`OPS_BLUEPRINT` 与 `PlaceholderPage.tsx` 本身
  **未删除**——它们的测试走的是合成路由（不经 `router.tsx`），/ops 换成
  真实页面后这两处变成无害的死代码，删除不是本片必要工作，留作后续
  清理（见 follow_ups）。

新鲜度状态的展示复用了 `@xingmang/ui-admin` 既有的 `describeFreshness`/
`formatFreshnessNote`/`formatFreshnessDetail`/`formatUtcTimestamp`
（`OpsFreshness` 与该包的 `FreshnessContract` 结构逐字一致），没有为本片
重新发明一套新鲜度文案组件。

### 部署（新增 .env 变量）

`deploy/compose/launch.yaml` + `deploy/compose/.env.example` 已加好，
逐条列在这里方便直接核对/复制到其它部署方式：

| 变量 | 默认值 | 读取方 | 说明 |
|---|---|---|---|
| `XM_CONNECTOR_PROBE_ENABLED` | `true` | platform-worker | 探测总开关，宪法 26 条 Kill Switch |
| `XM_CONNECTOR_PROBE_INTERVAL` | `5m` | platform-worker | 探测周期；新鲜度阈值固定 900s，不随本变量联动 |
| `XM_ALERT_TELEGRAM_BOT_REF` | 空 | **platform-api（新增）** + platform-worker（既有） | platform-api 只判断非空，不解析 |
| `XM_ALERT_TELEGRAM_CHAT_ID` | 空 | **platform-api（新增）** + platform-worker（既有） | 同上 |
| `XM_ALERT_WEBHOOK_URL` | 空 | **platform-api（新增）** + platform-worker（既有） | 同上 |

后三个变量本来就存在（告警投递用），本片新增的是「platform-api 进程也读
这三个变量」——`XM_ALERT_TELEGRAM_TOKEN`（Bot Token 明文）**没有**透传给
platform-api，它没有任何合法理由持有这把凭据。

## files_changed

后端（Go）：

- `internal/platform/jobs/connector_probe.go`（新增）+
  `connector_probe_test.go`（新增，13 个测试）
- `internal/platform/jobs/heartbeat.go`（`WithObservations` + 观测写入）
- `internal/platform/jobs/retention.go`（`Observations`/`ExpectedInterval`
  字段 + 观测写入）
- `internal/platform/jobs/client.go`（Config 新字段、DefaultConfig、
  normalized、validate 新增闸、NewClient 里三处装配改动）
- `internal/platform/jobs/job_manifest.go` / `effective_manifest.go`
  （七个任务）
- `internal/platform/jobs/job_manifest_test.go` / `sub2api_sync_test.go`
  / `newapi_sync_test.go` / `cost_sync_test.go`（既有测试的隔离修正）
- `internal/platform/ops/freshness.go`（4 条新指标白名单）
- `internal/platform/ops/metrickeys_test.go` /
  `internal/platform/ops/rollup_policy_test.go`（一致性断言同步）
- `internal/platform/httpapi/ops_overview.go`（新增）+
  `ops_overview_test.go`（新增，10 个测试）
- `internal/platform/httpapi/router.go`（`Deps` 新字段 +
  `GET /ops/overview` 路由）
- `cmd/platform-api/main.go`（Deps 装配）
- `cmd/platform-api/alert_delivery.go`（新增）+ `alert_delivery_test.go`
  （新增）
- `cmd/platform-worker/config.go` / `config_test.go`（两个新环境变量）
- `contracts/jobs/cluster-jobs.v1.json`（第七个任务）
- `contracts/ops/metric-rollup-policy.v1.json`（4 条新 policy）

部署：

- `deploy/compose/launch.yaml` / `deploy/compose/.env.example`

前端（TypeScript/React）：

- `web/apps/admin-web/src/api/ops.ts`（新增）+ `ops.test.ts`（新增）
- `web/apps/admin-web/src/lib/ops.ts`（新增）+ `ops.test.ts`（新增）
- `web/apps/admin-web/src/pages/OpsPage.tsx`（新增）+ `OpsPage.test.tsx`
  （新增）
- `web/apps/admin-web/src/router.tsx` / `router.test.tsx`（挂载真实路由；
  两个原本以 `/ops` 为占位页示例的测试改用 `/finance`，行为断言不变）
- `web/packages/ui-admin/src/navigation.ts`（`built: false → true`）/
  `navigation.test.ts`（对应断言更新）

## tests_run

- `go build ./...`：全仓 PASS。
- `go vet ./...`：全仓 PASS（无输出）。
- `gofmt -l`：本片改动/新增的全部 `.go` 文件干净。全仓扫描另列出
  `internal/platform/httpapi/finance_test.go`——**改动前就存在、本片未碰**
  （`git status` 确认无改动；XM-USERS-REAL 的 handoff 里已记录过同一条
  历史债）。
- `go test ./...`：全仓 43 个包 **PASS，0 FAIL**（含新增的
  `TestConnectorProbe*` 13 个用例、`TestOpsOverview*` 10 个用例、
  `TestAlertDeliveryStatusFromEnv*`、`TestConfigFromEnvReadsConnectorProbe
  Settings` 等新测试；也含所有需要真实 DB 的集成测试，均在本环境下通过）。
- `bash scripts/check-governance.sh`：PASS（exit 0，无输出）。
- `docker compose -f deploy/compose/launch.yaml config`：PASS（语法校验，
  未启动任何容器）；抽查渲染结果确认 `XM_CONNECTOR_PROBE_*` 与三个
  `XM_ALERT_*` 变量在 platform-worker/platform-api 两个服务块里都正确
  解析出默认值。
- `gitleaks detect --no-git`（全仓扫描）：4 条既有 `generic-api-key`
  误报（指标键字面量 `"sub2api.revenue.daily"` 等），全部在**本片未改动**
  的文件里（`atomicity_store_test.go`、`platform.test.ts`、
  `metrics.test.ts`），与仓库已知的这一类误报一致；本片改动的 34 个文件
  与这 4 条无交集。
- 前端 `pnpm --filter admin-web run typecheck`：PASS（无输出）。
- 前端 `pnpm --filter admin-web run test`：**82 个测试文件、1158 个
  测试全部 PASS**。
- 前端 `pnpm --filter admin-web run build`：PASS（`tsc --noEmit && vite
  build` 成功；产物体积告警是 pre-existing，与本片无关）。
- 前端 `pnpm --filter ui-admin run test`：16 个测试文件、232 个测试
  PASS（`navigation.ts` 所在包）。

## not_run / risks

- **未对真实上游验证过 `connector_probe`**：`Version()`/`Health()` 的
  行为完全依赖 `connectors/sub2api`、`connectors/newapi` 既有连接器的
  契约实现（本片没有改动那两个包），而那两个连接器本身也带着「未对真实
  实例验证过」的免责声明（见 `docs/modules/connector/README.md`「尚未
  做的事」）。真实凭据到位后，第一次真实探测要核对：`Health()` 返回的
  `ErrorKind` 在真实 5xx/超时场景下是否真的落进预期分类、`latency_ms`
  的量级是否合理（如果上游本身响应就慢，900s 阈值配合 5 分钟默认周期是
  否够用需要用真实数据回看一次）。
- **`alert_delivery` 依赖「两个进程读同一份 .env」这个部署假设，不是
  DB 保证的**：`cmd/platform-api/alert_delivery.go` 的文档注释里写清楚
  了这个取舍——platform-api 今天不读任何其它 `XM_SUB2API_*`/`XM_NEWAPI_*`
  变量（那些改走 `core.connector_config` 是因为有运营在后台切换的实际
  需求），告警投递配置目前没有对应的后台可改入口，纯粹是部署期 env，所以
  让 platform-api 多读三个已存在的变量名，而不是新起一条「worker 写一条
  观测」的机制。**如果两个进程未来真的被独立配置**（例如换成不同的
  Kubernetes Deployment、各自的 ConfigMap），这里会读到不一致的结果
  （platform-api 说「未配置」而 worker 其实配了，或反过来）——这不是本片
  遗漏，是一个记录在案的、依赖当前部署拓扑的简化，follow_ups 里给了替代
  方案。
- **rollup policy 的字段选择是实施方判断，非需求指定**：4 条新指标都不是
  金额或直观的「总数」，`primary_json_pointer` 选了 `/latency_ms`（两条
  连接器健康）/`/attempt`（心跳）/`/metric_samples_deleted`（保留期清理）
  ——这些字段确实在各自的 value 里、确实是可累计/可画趋势线的数字，但
  选它们而不是别的字段（比如 `/job_id` 这种不该被聚合的量）是本片实施方
  的判断，值得产品/架构侧过一眼是否符合运行保障页未来想看的趋势维度。
- **`sync_pipelines` 用单一代表指标而非全指标聚合**：见 summary「设计
  取舍」一节——如果某个平台的 `channels.status` 新鲜但同平台的
  `users.balance` 恰好卡住了，`sync_pipelines` 这一行会显示「新鲜」，
  看不出 `users.balance` 的问题（那条数据仍然能在 `/metrics` 端点或未来
  细化的表格里单独看到，只是这次的汇总行没有覆盖它）。
- **未做浏览器端到端验证**：没有起真实的 `docker compose` 栈、没有在浏览器
  里打开 `/ops` 页面截图核对视觉效果；只跑了 `docker compose config`
  （语法校验）与前端单测/组件测试（jsdom 环境）。DataTableV2 的横向滚动
  行为、深色模式下的展示等，按 ADMIN-IA §七横切规则应当没问题（组件本身
  已在其它已上线页面验证过），但本片没有专门截图确认。
- `OPS_BLUEPRINT`（`web/apps/admin-web/src/blueprints/governance.ts`）与
  `PlaceholderPage.tsx` 的 `/ops` 相关代码路径现在是死代码（见 summary），
  未删除。

## follow_ups

- 真实凭据到位、`XM_SUB2API_MODE`/`XM_NEWAPI_MODE` 切到 real 后，按上面
  「not_run/risks」第一条核对 `connector_probe` 的真实行为，把结果记进
  `docs/inventory/managed-systems.yaml`（若无对应条目就新建）。
- 若「两个进程独立配置」的部署假设未来不再成立，把 `alert_delivery` 改成
  worker 写一条 `platform.alerts.notifier_config` 式的观测（bool-only
  value），platform-api 端只需把 `alertDeliveryStatusFromEnv` 换成一次
  `Observations.Get` 查询——`OpsOverviewDeps`/`AlertDeliveryStatus` 的
  对外形状不用变，只是数据来源从 env 换成观测，前端契约不受影响。
- 待产品/架构侧确认 rollup policy 的 4 个字段选择后，如需调整
  `primary_json_pointer`，同步改 `contracts/ops/metric-rollup-policy.v1.json`
  与 `rollup_policy_test.go` 的对应断言。
- 待安排一次真实浏览器走查（或接入 `webapp-testing`/`claude-in-chrome`
  技能跑一遍），核对 `/ops` 页面在实际数据（含至少一条 `SyncFailed`/
  `stale` 状态）下的视觉呈现，尤其是 `DataTableV2` 六行数据在窄视口下的
  横向滚动行为。
- 待评估：`OPS_BLUEPRINT` 与 `blueprints/index.ts` 里 `/ops` 的注册、
  `PlaceholderPage.tsx` 里因此变成不可达的分支，是否值得单独清理（本片
  评估为非必要风险，未动）。
- 其余 5 个子页签（稳定性与外部监控 / 备份与恢复 / 故障处理手册 / 迁移与
  数据对比 / 模型质量保障）按 ADMIN-IA §九是 M1/M1.5 之后的范围，各自的
  数据源依赖列在 `OPS_BLUEPRINT` 里，不在本片重复。
