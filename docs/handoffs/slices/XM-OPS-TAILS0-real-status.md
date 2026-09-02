# XM-OPS-TAILS0 · 周期任务真实状态 / reqlog 指标入汇总 / verify-real-mode 读 connector_config

## status

READY（待验收线审读、复跑并人工合入）

## branch / commit / base

- branch: `ai/claude/XM-OPS-TAILS0-real-status`
- base: `release/v0.1-launch@b893183`
- worktree: `K:/星芒统一控制平台/wt-xmOPSTAILS0`
- commits（7，从旧到新）：`a1137b3` `0d96267` `221c527` `a278ac3` `279f0ef` `2b10080` `9e4e88c`

## 开工前状态确认（团队交接要求「先确认现状」）

| 尾巴 | 开工前实际状态（不是交接文档的推测） |
|---|---|
| 周期任务真实状态 | XM-JOBS0 已把「定时任务」表接到真实 `river_job`（最近一次/观测周期/预计下次/活跃度全部真实），但明确不做「已启用」判断（httpapi 读不到 worker 的 `jobs.Config`）。`core.connector_config`（XM-CRED0）存在且已有只读 HTTP 端点 `GET /connectors/config`，但没有被 jobs 概览消费。**`/jobs` 路由本身在某次并行合并后从 `router.tsx` 丢失，页面在真实浏览器里 404**——这一条是本片开工后才发现的，不在交接文档的预期缺口清单里。 |
| reqlog 指标入汇总 | 六个 `*.requests.*` 指标（XM-REQLOG-METRICS）已注册进 `/api/v1/metrics` 白名单且**已经**被 Sub2API 概览页消费成真实卡片+新鲜度（XM-OVERVIEW-UI，已合入）。真正缺的只是 rollup policy 契约层：`sub2api.requests.daily`/`newapi.requests.daily` 与另外四个滚动窗口/多日数组指标一起被显式排除在外，即便前两个形状与已激活的 `daily_snapshot` policy 完全一致。 |
| verify-real-mode 读 connector_config | 脚本存在（XM-REAL0-d）且有完整测试套件，但「mode=」这句最终结论只是把 `--mode` 参数原样回显，唯一的模式核验来自 Worker **启动日志**的快照字段，对 `core.connector_config` 的热切换完全不可见。 |

## summary

### 1. 周期任务真实状态

**先修的阻塞性 bug（不在原计划里）**：真实浏览器验证时发现 `/jobs` 完全 404——`navigation.ts` 早把 `jobs` 标 `built:true`（`navigation.test.ts` 也断言"后台任务已实装"），`JobsPage.tsx`/`JobsPage.test.tsx` 也一直都在，但 `router.tsx` 从未 import 或注册这条路由。两边测试各自绿是因为 `JobsPage.test.tsx` 直接渲染组件不经真实路由，`router.test.tsx` 也没有一条用例导航到 `/jobs`。`router.test.tsx` 里一句既有注释（"XM-JOBS0 把 /jobs 接上真实数据后…"）证明这条路由在某个历史时点确实存在过，大概率是后续某次并行合并冲突时静默丢失。已在 `router.tsx` 补回 `{ path: "jobs", Component: JobsPage }` 并在 `router.test.tsx` 加两条回归用例锁定"导航到 /jobs 渲染真实内容而不是兜底 404"。

**新增的真实状态**（`internal/platform/jobs/deployed_schedule.go`）：
- `DeployedSchedulesFromEnv`：httpapi 进程按与 `cmd/platform-worker/config.go` 完全相同的变量名与解析规则（只解析 `_ENABLED`/`_INTERVAL`/mode 三类字段，不碰凭据/端点/白名单），复用包内私有的 `effectiveJobConfig` 开关表，独立算出每个已注册任务「部署配置声明」的启用状态与周期。**这不是 worker 进程的实时确认**——两个进程互不可见对方内存里的 `jobs.Config`，字段命名与文档反复强调这一点，正常部署下（两个容器读同一份 `.env`）两者应当一致，但这句话本身没有变。
- `QueryStore` 新增两个可选依赖（`WithDeployedSchedules`/`WithConnectorConfigSource`，向后兼容，`NewQueryStore(pool)` 零值行为不变）：`ScheduleStatus` 上补 `Configured`（启用/周期/来源变量名）与 `ConfiguredMode`（`real`/`fake`，只在 `sub2api_sync`/`newapi_sync` 非空——`core.connector_config` 的 `platform` 列 CHECK 约束只认这两个平台）。库里没有对应平台的行按 `credentials.Store.ListConnectorConfigs` 的既定口径视为 `fake`/`"default"`；读库失败标 `"unavailable"`，不让整个 Overview 报错。
- `cmd/platform-api/main.go` 装配这两个依赖；解析失败按启动期配置错误处理（`os.Exit(1)`，与本文件其它校验同一条纪律）。
- `deploy/compose/launch.yaml` 给 `platform-api` 服务补上与 `platform-worker` 完全同名同默认值的 `HEARTBEAT_INTERVAL`/`XM_{SUB2API,NEWAPI}_SYNC_{ENABLED,INTERVAL}`/`XM_FINANCE_COLLECT_{ENABLED,INTERVAL}`/`XM_RETENTION_{ENABLED,INTERVAL}`/`XM_ALERT_EVALUATE_{ENABLED,INTERVAL}`/`XM_CONNECTOR_PROBE_{ENABLED,INTERVAL}`/`XM_REQLOG_METRICS_INTERVAL`/`XM_CPA_SYNC_{ENABLED,INTERVAL}` 透传（`XM_REQLOG_MODE`/`XM_CPA_MODE` 该服务已有，不重复加）。刻意不透传任何凭据/端点/白名单——最小权限。`server-prod.yaml` 不需要改：compose 按 key 合并 `environment:` map，验证过。
- `internal/platform/httpapi/jobs.go` 的 `jobScheduleBody` 新增五个字段（`configured_enabled`/`configured_interval_seconds`/`configured_source`/`configured_mode`/`configured_mode_source`），JSON 恒不省略（无 `omitempty`，null 与既有字段同一惯例）。
- 前端 `JobsPage.tsx` 「定时任务」表新增「部署状态」列：两行徽章——「已配置启用/停用」+来源变量名，以及只在 sub2api_sync/newapi_sync 上出现的「真实接入/演示数据」模式徽章（或读取失败提示）。页面说明文案同步更新，讲清"部署状态"（配置声明）与既有的"活跃度"/"观测周期"（真实观测）是两件不同的事。顺带补齐三个此前只能原样显示英文 kind 的任务中文名（`reqlog_metrics`/`connector_probe`/`cpa_sync`，都是 JOBS0 之后新注册的任务，标签表没跟上）。

### 2. reqlog 指标入汇总

- `internal/platform/ops/rollup_policy.go` + `contracts/ops/metric-rollup-policy.v1.json`：把 `sub2api.requests.daily`/`newapi.requests.daily` 从 exclusion 列表移到 active policies（`daily_snapshot`/`count`/`primary_json_pointer=/request_count`，与已激活的 `newapi.models.usage`/`cpa.requests.daily` 同一形状）。`success_rate_24h`/`trend_7d`（滚动窗口/多日数组）继续排除——激活需要给 `RollupPolicy` 引入新 `ValueKind`，是独立设计任务，不在本片范围。`business_day_json_pointer` 留空、`expected_interval_seconds` 留 `null`：仓库里**现存全部** active policy 都是这样，不率先给新增的两条用一个没有其它先例验证过的字段组合。
- **诚实的范围说明**（写进了 `docs/modules/ops/DATA-MODEL.md`）：全仓库 grep 确认**没有任何生产代码路径调用 `LoadRollupPolicies`**——这份契约今天只被测试加载校验，还没有降采样引擎消费它产出 `ops.metric_observation_daily` 的日粒度行。这次激活的价值是把契约补齐（团队交接原话「必须是 metrics rollup policy 的一部分」，本片按字面完成），并把未来降采样引擎要用的 policy 设计提前定好，**不是**让概览页面立刻多出什么新数据——运营工作台/概览卡片显示的今日调用量/成功率/趋势早就是真实数据了（下一段）。
- **确认（不是新做的）**：Sub2API 平台详情「概览」页的「今日调用量」「成功率（24h）」「近 7 日调用量」三张卡片已经在消费真实 `/api/v1/metrics`（XM-OVERVIEW-UI，已合入 release），真实浏览器验证截图见 `docs/evidence/screens/XM-OPS-TAILS0/02-sub2api-overview-reqlog-cards.png`。运营工作台的「平台状态矩阵」按 `metric_key` 前缀通用聚合每个平台全部指标里最差的新鲜度，reqlog 三条指标因此**自动**纳入矩阵的新鲜度计算，不需要新代码（`lib/platforms.ts` 的 `platformOfMetricKey` 是按 `.` 前缀通用拆分，不是要逐键维护的清单）；截图 `03-overview-workbench-matrix.png`。
- **本片刻意没做**：NewAPI 平台详情概览页目前没有「今日调用量」「成功率（24h）」两格占位——`PlatformOverviewPanel.tsx` 明确把 Sub2API/NewAPI 的概览结构判定为「原型给这两个平台各画了一版结构不同的概览」，NewAPI 的版本是「用户/渠道/利润样例总览」，不是 Sub2API 的「今天有没有事」；`newapi.requests.*` 三个键的友好名/渲染函数已经登记（`lib/metrics.ts`），接卡片只是布局决定，XM-OVERVIEW-UI 原作者已明确把这件事标成"布局变更，不在本片范围内"的 follow_up。团队交接给的 `UI 不大改` 约束加上这条已有的、更了解原型语境的决定，本片选择尊重现状、不新增卡片。**如果这条判断是错的（团队实际想要 NewAPI 也有这两格），把它加回来是纯增量：两个键的 label/renderer 已经就绪，只需要在 `NewApiOverview` 里仿照 `Sub2ApiOverview` 加两张 `MetricTile`。**

### 3. verify-real-mode 读 connector_config

- `scripts/verify-real-mode.sh`：新增对 `GET /api/v1/connectors/config` 的探针（dev-header scope 加 `connector.manage`，与写 Action `connector.config.set@1` 同一个 scope）。解析出的逐平台模式与 `--mode` 推出的期望模式核对，不一致直接 fail（`core.connector_config mode mismatch`，点名具体哪个平台是什么模式）；没有对应平台的行按既定口径视为 `fake`。最终 `VERIFY REAL MODE PASS` 行新增 `connectors_config:<code>` 探针状态；Python 校验段的汇总行新增 `connector_config_mode=sub2api:real,newapi:fake` 这样的逐平台真值（**不是**回显 `--mode` 参数）。旧的 Worker 日志核验保留（仍然证明 worker 进程活着、上一轮同步成功），两者互补而不是互相替代。
- `tests/deploy/verify-real-mode.test.sh`：新增 connector-config fixture 生成（staging 全 fake / real 全 real / 未配置任何平台 / sub2api 落后于 newapi 的"热切换未察觉"场景），新增 4 条用例（含一条真正验证新逻辑的负向用例：connector_config 与请求模式不一致时必须 fail）。全部 24 条（既有 18 + 新增 6，其中 2 条是既有用例补充断言）通过。
- `deploy/scripts/deploy-local.sh`：补一段头部注释指向 `verify-real-mode.sh` 作为部署后手动核验模式的独立步骤——**没有**把两者接起来自动调用：`deploy-local.sh` 自己的冒烟测试用的是更窄的 dev-header scope（`registry.read,ops.read`），且它今天根本不调 `verify-real-mode.sh`；接起来需要统一两边的鉴权面，是一次比"补一条文档指针"更大的改动，团队交接原话允许"calls it or documents it"二选一，选了风险更小的后者。

## files_changed

后端（8）：

- `internal/platform/jobs/deployed_schedule.go`（新）、`deployed_schedule_test.go`（新）
- `internal/platform/jobs/query_store.go`、`query_store_test.go`、`query_store_integration_test.go`
- `internal/platform/httpapi/jobs.go`、`jobs_test.go`
- `cmd/platform-api/main.go`
- `internal/platform/ops/rollup_policy.go`、`rollup_policy_test.go`

契约/部署（4）：

- `contracts/ops/metric-rollup-policy.v1.json`
- `deploy/compose/launch.yaml`
- `deploy/scripts/deploy-local.sh`（仅头部注释）
- `docs/modules/ops/DATA-MODEL.md`

脚本（2）：

- `scripts/verify-real-mode.sh`
- `tests/deploy/verify-real-mode.test.sh`

前端（6）：

- `web/apps/admin-web/src/router.tsx`、`router.test.tsx`
- `web/apps/admin-web/src/api/jobs.ts`、`jobs.test.ts`
- `web/apps/admin-web/src/pages/JobsPage.tsx`、`JobsPage.test.tsx`

证据（新）：

- `docs/evidence/screens/XM-OPS-TAILS0/01-jobs-scheduled-tab.png`
- `docs/evidence/screens/XM-OPS-TAILS0/02-sub2api-overview-reqlog-cards.png`
- `docs/evidence/screens/XM-OPS-TAILS0/03-overview-workbench-matrix.png`

## tests_run

全部命令均在本 worktree 内以串行方式执行（未并行跑多个 pnpm 门禁）。

后端（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 前缀）：

```
go build ./...                                — PASS
go vet ./...                                  — PASS
go test -p 1 -count=1 ./...                   — PASS，47 个包 ok，0 FAIL
"$(go env GOROOT)/bin/gofmt" -d <本片改动的全部 .go 文件>  — 全部干净（diff 模式逐文件核对，未对整仓库跑 -l）
bash scripts/check-governance.sh              — PASS（exit 0，无输出）
bash tests/deploy/verify-real-mode.test.sh    — PASS，VERIFY-REAL-MODE-TEST-OK（24 用例）
bash tests/deploy/deploy-local.test.sh        — PASS，DEPLOY-LOCAL-TEST-OK（未改动逻辑，仅确认头部注释没弄坏脚本）
docker compose -f deploy/compose/launch.yaml config --quiet                              — PASS
docker compose -f deploy/compose/launch.yaml -f deploy/compose/server-prod.yaml config --quiet
  （补足 XM_SUB2API_MODE/XM_SUB2API_INSTANCE_ID/XM_NEWAPI_MODE/XM_NEWAPI_INSTANCE_ID/
   XM_FINANCE_COLLECT_MODE/XM_FINANCE_COLLECT_INSTANCE_ID 等必填变量后）        — PASS，并逐一核对
   platform-api 服务解析出的新增变量值与 platform-worker 一致
```

**真实 Postgres 集成测试**（自建隔离容器 `postgres:18`，不是 `xingmang-launch-postgres-1`——该容器本次未在跑，且这是一个多代理共享的环境，改用独立容器避免互相干扰；灌入全部 23 个顶层 `db/migrations/*.up.sql`，river 子目录留给测试自己跑，跑完已 `docker rm -f` 清理，未留任何残留容器/卷）：

```
docker run -d --name <临时> -e POSTGRES_USER=xm_test -e POSTGRES_PASSWORD=<一次性口令> \
  -e POSTGRES_DB=xm_scratch postgres:18
docker cp db/migrations <容器>:/tmp/migrations
docker exec <容器> sh -c 'for f in /tmp/migrations/*.up.sql; do psql -U xm_test -d xm_scratch -f "$f"; done'
docker run --rm --network container:<容器> -v <worktree>:/src -v $HOME/go/pkg/mod:/gomodcache \
  -e XM_TEST_DATABASE_URL=... golang:1.27 go test -p 1 -count=1 ./internal/platform/jobs/...
```
—— 196 个用例 PASS（含本片新增的 `TestQueryStoreOverviewConfiguredScheduleAndModePostgresIntegration`、
`TestQueryStoreConfiguredModeFor` 三个子用例、`TestScheduleConfiguredModePlatform`、
`TestDeployedSchedulesFromEnv*` 五个用例），**1 个 FAIL**：`TestPgConnectorConfigSourceIntegration`
（`internal/platform/jobs/connector_config_integration_test.go`，本片未改动此文件）。

根因已定位且与本片改动无关：该用例的第二条 INSERT 故意省略 `updated_at`/`updated_by` 来验证
COALESCE 读 NULL 的行为，靠 `CREATE TABLE IF NOT EXISTS` 在"表不存在"时现建一份列可空的宽松
schema；本片按仓库既定配方（灌全部顶层迁移）预先建表后，该用例撞见的是 XM-CRED0 迁移里真实
`updated_at timestamptz NOT NULL`（无默认值）的严格约束，INSERT 失败。**已用第二个全新、完全
未迁移的容器单独复跑这一条用例验证：在未预先建表时 PASS**——证明这是测试自身对"表是否已存在"
这件事的假设与本机既定的"先灌全部迁移再跑测试"配方冲突，是一个预先已存在、与本片代码无关的
测试脆弱性，不是回归。两个临时容器均已清理。

前端（`web/` workspace，`--config.verify-deps-before-run=false`）：

```
pnpm -r run typecheck                         — PASS（design-tokens/ui-primitives/ui-storybook/ui-admin/admin-web 全部 5 个包）
pnpm -r run test                               — PASS：design-tokens 10、ui-primitives 16、ui-admin 253、
                                                  admin-web 1390（96 个测试文件），ui-storybook 无单测
  （其中 admin-web 完整跑了三遍：加完 Configured 列后一遍、修完路由丢失 bug 后一遍、
   最终收尾前一遍，确保两处改动叠加后仍然全绿）
pnpm --filter ui-storybook run build（在 web/apps/ui-storybook 包目录内直接跑，
  不走 -r --filter——既定坑记录里那条网络问题的规避方式）             — PASS，storybook-static 产物正常生成
```

**真实浏览器验证**（mock API + `node node_modules/vite/bin/vite.js`，worktree 内临时
`vite.dev.local.config.ts` 放开 `fs.strict`，验证完成后已删除；chrome-devtools 工具）：

- `/jobs?sub=scheduled` —— 九个已注册周期任务全部出现，「部署状态」列正确显示
  已配置启用/停用徽章+来源变量名，Sub2API 同步显示「真实接入」、NewAPI 同步显示「演示数据」，
  与 mock 数据逐条核对一致。截图 `01-jobs-scheduled-tab.png`。**这一步顺带证实了路由丢失 bug 的
  修复是真实生效的**——修复前这个地址渲染的是路由表兜底 404 页。
- `/platforms/sub2api?tab=overview` —— 「今日调用量」「成功率（24h）」「近 7 日调用量」
  三张卡片显示真实数字与新鲜度徽章（「数据新鲜」），确认 XM-OVERVIEW-UI 早先交付的这部分功能
  在当前分支上仍然工作正常。截图 `02-sub2api-overview-reqlog-cards.png`。
- `/dashboard`（运营工作台）—— 平台状态矩阵 Sub2API/NewAPI 两行显示「数据新鲜」，确认
  reqlog 指标的新鲜度已计入矩阵的最差新鲜度聚合。截图 `03-overview-workbench-matrix.png`。
- 三个页面均检查了浏览器控制台，无应用层 error/warn（仅一条与本片无关的资源级
  `ERR_CONNECTION_TIMED_OUT`，出现在页面切换过程中，不影响页面功能）。

## not_run

- **服务器/staging 栈上的真实验证**：未连接服务器，未对着真实 `platform-worker`/真实
  `core.connector_config` 数据核实。全部真实浏览器验证用的是本地 mock API。
- **`verify-real-mode.sh` 对着真实 staging 栈跑一次**：脚本改动只在离线测试模式
  （`--test-mode` + fake curl/docker）下验证过；REAL0-d 交接文档记录过一次真实
  staging 栈运行证据，本片未重新对着真实栈跑一遍（没有服务器访问权限）。
- **`docker build` 真正构建 `platform-api`/`platform-worker` 镜像**：只做了
  `docker compose config` 的静态解析验证，没有执行 `deploy/docker/go.Dockerfile`
  的多阶段构建。
- **`TestPgConnectorConfigSourceIntegration` 的修复**：诊断清楚了（见 tests_run），
  但修复这条既有测试的 schema 假设不在本片范围内（本片没有改动
  `connector_config_integration_test.go` 或 `connector_config.go`），留给验收线判断
  是否值得单独派一个小片修。
- **NewAPI 概览页补「今日调用量」「成功率（24h）」两格**：判断详见 summary
  第 2 节——尊重 XM-OVERVIEW-UI 原作者已有的"布局变更不在本片范围"的决定与
  `UI 不大改` 约束，未做。两个指标键的前端登记已就绪，随时可以加。
- **`success_rate_24h`/`trend_7d` 的 rollup policy 设计**：需要给 `RollupPolicy`
  引入新 `ValueKind`（滚动窗口 / 多日数组），是独立设计任务，本片未做（`XM-REQLOG-METRICS`
  原交接文档同样把这条列为独立 follow_up，本片认同这个判断）。

## risks

1. **「部署状态」列是"两个进程各自解析同一份环境变量"，不是跨进程读取**：如果某次部署
   给 `platform-worker` 传了这些变量却漏传给 `platform-api`（或反过来、或两边的值不小心
   写岔了），这一列会显示一个与 worker 实际行为不一致的"部署声明"，而不会报错——这是
   设计上接受的代价（诚实标注"这是部署配置读数，不是 worker 确认"好过完全没有这个信息），
   已通过 `deploy/compose/launch.yaml` 让两个服务用**同名同默认值**的变量降低这个风险，
   但没有运行时交叉校验。
2. **`connector_config` 读取用的是无缓存的直接查询**（`QueryStore.configuredModeFor`
   每次 `Overview()` 调用都会各打一次 `sub2api`/`newapi` 的库查询，没有复用
   `NewPgConnectorConfigSource` 自带的 30s 缓存的"跳过重复查询"效果之外的进一步优化）——
   `Overview()` 现在比修改前多两次数据库往返；量级判断：这是一个人工查看的运营页面，
   不是高频轮询端点，60 秒自动刷新一次，可接受，但没有专门做性能测试。
3. **verify-real-mode.sh 的新检查目前只在 staging 环境的 dev-header 鉴权下可用**：
   `internal/platform/httpapi/principal.go` 的 `NewDevHeaderResolver` 对
   `environment=="production"` 硬拒绝，这是脚本一直以来的既有限制（healthz/readyz 之外的
   全部探针，包括本片新增的 `/connectors/config`），不是本片引入的新限制，但意味着
   "在生产环境跑这个脚本验证连接器模式"这件事今天做不到，需要走 OIDC/local 会话或
   另一条认证路径——这是一个更大的、跨越多个既有脚本（`deploy-local.sh` 自己的冒烟测试
   也有同样的限制）的问题，不在本片范围内解决。
4. **`TestPgConnectorConfigSourceIntegration` 的脆弱性**：任何后续在"先灌全部迁移再跑
   集成测试"这个既定配方下工作的人都会撞到同一个失败，如果不知道 tests_run 里记录的根因，
   容易误判成自己的改动引入的回归。

## follow_ups

- 修复 `TestPgConnectorConfigSourceIntegration`：改成显式 `DROP TABLE IF EXISTS` 后按
  自己的宽松 schema 重建，而不是依赖 `CREATE TABLE IF NOT EXISTS` 在"表已存在"时静默假设
  对方是自己期望的那个宽松版本；或者反过来，改成对真实迁移 schema 的严格版本断言（连带
  去掉"newapi 行故意留 NULL"这个已经不成立的场景）。两种修法都不在本片范围，留给下一个
  碰这个文件的人。
- 给 `sub2api.requests.success_rate_24h`/`trend_7d` 设计新的 `RollupPolicy.ValueKind`
  （滚动窗口 / 多日数组），激活后六个 reqlog 指标才能全部纳入 rollup policy——目前还有四个
  显式排除。
- 决定 NewAPI 概览页要不要补「今日调用量」「成功率（24h）」两格；如果要，
  `newapi.requests.daily`/`newapi.requests.success_rate_24h` 的 label/renderer 已经在
  `lib/metrics.ts` 里注册好，接卡片是纯增量工作。
- 真正让 rollup policy 产生日粒度降采样结果需要先有一个消费 `LoadRollupPolicies` 的
  降采样引擎——目前完全不存在（全仓库 grep 确认），这是比本片大得多的一块独立工作。
- 在服务器 staging 栈上把 `scripts/verify-real-mode.sh --mode real` 真正跑一遍，
  核对 `connector_config_mode=` 输出与后台「设置→凭据」页面显示的模式一致。
