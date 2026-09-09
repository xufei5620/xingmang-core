# XM-REQLOG-METRICS · 请求量/成功率聚合指标

## status

READY（详情见下方 not_run / risks——本次未连接真实服务器、未验证真实
`/root/reqlog/data` 上的实测表现，留给验收线在服务器上核对）

## branch / commit / base

- branch: `ai/claude/XM-REQLOG-METRICS`
- base: `fc709e8`（`release/v0.1-launch`，已含 XM-REQLOG-MERGE）
- implementation commit: `140829e`
- worktree: `K:/星芒统一控制平台/acceptance/wt-reqlog-metrics`

## scope

让 `platform-worker` 从请求审计落盘数据（记录代理的 `index.jsonl`）算出每个
被管平台（sub2api/newapi）的「今日调用量 / 24h 成功率 / 近 7 日调用量」三项
指标并写成 `ops.metric_observation` 观测，供概览页卡片使用。前端由另一个
代理并行实现，双方约定的指标契约（见下）不得改动。

### 指标契约（逐字实现，与前端代理共享）

每个平台三条 `metric_key`（把 `sub2api` 换成 `newapi` 即另一平台）：

| metric_key | value_json |
|---|---|
| `sub2api.requests.daily` | `{"day":"2026-08-31","request_count":N,"success_count":N,"failure_count":N,"avg_duration_ms":int\|null}`，只统计当天 CST 目录；success=HTTP 2xx；0 条时 avg 为 null |
| `sub2api.requests.success_rate_24h` | `{"window_hours":24,"request_count":N,"success_count":N,"success_rate_bp":int\|null}`；`[now-24h, now]` 闭区间（可能跨两个 CST 日历日目录）；rate 是**基点整数**（万分之几，四舍五入，绝不经过 float）；0 条时为 null |
| `sub2api.requests.trend_7d` | `{"days":[{"day":"…","request_count":N,"success_count":N,"missing"?:true},…]}`；固定 7 个元素、按日升序、以今天结尾；没有目录的日子 count 为 0 且标 `"missing":true` |

Source 与现有同步一致：`cfg.Sub2APIInstanceID`/`cfg.NewAPIInstanceID`（即
`XM_SUB2API_INSTANCE_ID`/`XM_NEWAPI_INSTANCE_ID`，不另开新变量）。环境
`production`；业务日按 Asia/Shanghai（与记录代理的 index 分日目录同一
换算）。新鲜度阈值 900s；任务周期 5 分钟（`XM_REQLOG_METRICS_INTERVAL`
可配）。

### 实现分层

1. **`connectors/reqlog/metrics.go`**（新增）：`MetricsReader` 只读
   `index.jsonl`（不解压 `.json.gz` 明细，只需要 `status/dur_ms/ts_ms/source`
   四个字段），按 CST 日历日聚合；`DailyStats`/`WindowStats`/`TrendDays` 三个
   方法分别服务今日/24h 窗口/7 天趋势；坏行（非法 JSON）跳过并计数，不让一行
   坏数据拖垮整天；`CheckRoot` 区分「数据目录整个不可读」（挂载配错，必须
   报失败）与「某一天没有目录」（合法状态，标 `missing`）。
   `ToRequestMetricsObservations` 把聚合结果翻成三条 `ops.Observation`，两个
   平台共用同一份翻译逻辑（`dailyKey/rateKey/trendKey` 由调用方传入）。
   六个指标键常量定义在本文件——字符串值用 `sub2api.`/`newapi.` 前缀而不是
   `reqlog.`：命名空间是**平台**不是**连接器**，与
   `connectors/metering.MetricCostDaily` 实际是 `"finance.cost.daily"`
   同一条先例（该常量注释里有完整论证）。
2. **`internal/platform/jobs/reqlog_metrics.go`**（新增）：新增 River 周期
   任务 `reqlog_metrics`。`ParseReqlogMetricsMode` 与 `platform-api` 共享
   环境变量名 `XM_REQLOG_MODE`，但 worker 只认 `off`/`file`——遇到
   `fake`/`real`（服务"请求详情"那条完全不同的链路）或拼写错误的值，
   **不报错**，退化成 `off` 并让 `jobs.NewClient` 记一条 `warn`，不拖垮
   worker 启动。`XM_REQLOG_MODE=file` 时注册任务，每轮为两个平台各写三条
   观测（先按成功路径算好三条，读失败时整体改写成失败观测，与
   `sub2api_sync`/`newapi_sync` 同一条写法：保住上一次成功的痕迹、成功与
   失败观测都留样）；非 `file` 时**完全不注册**这个任务——不写观测也不写
   `not_supported`，这条链路"未接入"由前端按缺观测处理（与 XM-UX-OFFSTATE
   的同类先例一致）。
3. **接线**：`internal/platform/jobs/client.go`（Config 新增四个
   `ReqlogMetrics*` 字段 + 默认值 + 校验 + `NewClient` 注册分支）、
   `job_manifest.go`（周期任务从 6 个增到 7 个，`contracts/jobs/
   cluster-jobs.v1.json` 同步更新）、`effective_manifest.go`
   （`effectiveJobConfig`/`productionRunID` 补上新 case）。
4. **`internal/platform/ops`**：六个指标键注册进 `freshness.go` 的
   `registeredMetrics` 白名单（与 `metrickeys_test.go` 的
   `TestRegisteredMetricsMatchConnectorContracts` 保持逐条对齐，新增了
   `connectors/reqlog` 的 import）；`rollup_policy.go` 给六个键加**显式
   exclusion**（gate=`XM-REQLOG-METRICS`），`contracts/ops/
   metric-rollup-policy.v1.json` 同步更新——理由见下方 risks #1，不是遗漏。
5. **`cmd/platform-worker`**：`config.go` 解析
   `XM_REQLOG_MODE`/`XM_REQLOG_DATA_DIR`/`XM_REQLOG_METRICS_INTERVAL`；
   `main.go` 启动日志补充这四个字段（含 `recognized` 标记），方便运维一眼
   看出这条聚合链路当前是不是被识别的配置。
6. **`deploy/compose`**：`launch.yaml` 给 `platform-worker` 透传三个环境
   变量（`XM_REQLOG_MODE` 默认 `off`，`XM_REQLOG_DATA_DIR` 默认空回落到
   Go 侧常量，`XM_REQLOG_METRICS_INTERVAL` 默认 `5m`）；`server-prod.yaml`
   给 `platform-worker` 加只读绑定挂载（`XM_REQLOG_HOST_DATA_DIR` 默认
   `/root/reqlog/data`，容器内 `/var/lib/xm/reqlog`，与 `platform-api`
   挂载同一份宿主机路径）——**只挂数据目录，不挂 tokenmap.json**：worker
   只聚合计数，不解析 `token_prefix` 到用户名，没有理由让这个只读凭据映射
   文件多一个访问面。
7. **`docs/modules/ops/DATA-MODEL.md`**：补充指标值形状表、registry 计数
   从 16 更新到 22（14 active + 8 excluded），新增小节说明原料/命名空间/
   新鲜度阈值。

## files_changed

新增：

- `connectors/reqlog/metrics.go`、`connectors/reqlog/metrics_test.go`
- `internal/platform/jobs/reqlog_metrics.go`、
  `internal/platform/jobs/reqlog_metrics_test.go`
- `docs/handoffs/slices/XM-REQLOG-METRICS.md`（本文件）

修改：

- `internal/platform/jobs/client.go`（Config 字段/默认值/校验/`NewClient` 注册）
- `internal/platform/jobs/job_manifest.go`（第 7 个周期任务）
- `internal/platform/jobs/job_manifest_test.go`（计数 6→7、`want` 表、
  `TestEveryArgsUsesArgsQueueEffectivePeriodAndExplicitDefaultStates`/
  `TestProductionArgsContainNoReplicaRunID`/`intervalForJob` 补新 case，
  测试函数改名 `TestManifestCoversExactlySevenRegisteredPeriodicJobs`）
- `internal/platform/jobs/effective_manifest.go`（`effectiveJobConfig`/
  `productionRunID` 补新 case）
- `internal/platform/ops/freshness.go`（`registeredMetrics` 白名单 +6）
- `internal/platform/ops/metrickeys_test.go`（`fromContracts` +6，新增
  `connectors/reqlog` import）
- `internal/platform/ops/rollup_policy.go`（`excludedRollupMetrics` +6）
- `internal/platform/ops/rollup_policy_test.go`（`TestPolicyCoversExactly
  RegisteredMetrics` 的 14/2 → 14/8）
- `contracts/jobs/cluster-jobs.v1.json`（第 7 个任务条目）
- `contracts/ops/metric-rollup-policy.v1.json`（`excluded_metric_keys` +6）
- `cmd/platform-worker/config.go`（三个新环境变量）
- `cmd/platform-worker/config_test.go`（对应用例）
- `cmd/platform-worker/main.go`（启动日志补字段）
- `deploy/compose/launch.yaml`（platform-worker 环境变量透传）
- `deploy/compose/server-prod.yaml`（platform-worker 只读绑定挂载 + 环境变量）
- `docs/modules/ops/DATA-MODEL.md`（指标值形状 + registry 计数 + 小节）

## tests_run

```
go build ./...                                  — PASS（全仓库，61 个包）
go vet ./...                                    — PASS（无输出）
gofmt -l <本片改动/新增的全部 .go 文件>          — PASS（净）；
  仓库里唯一命中的 internal/platform/httpapi/finance_test.go 是既有问题，
  git status 确认本片未touch过这个文件，与本片无关
go test ./...                                   — PASS：43 个包 ok + 18 个包
  no test files，0 个 FAIL（61/61）
bash scripts/check-governance.sh                — PASS（exit 0，无输出）
gitleaks protect --staged --verbose             — PASS（"no leaks found"，
  本片对 gitleaks 误报坑有准备：新增指标键都是常规英文短语拼接，未在测试里
  写任何长得像 token 的字符串）
docker compose -f launch.yaml config             — PASS（staging 单独，
  --quiet 无输出）
docker compose -f launch.yaml -f server-prod.yaml
  config                                        — PASS：platform-worker 的
  volumes 同时含 xm-secrets（launch.yaml）与新绑定挂载
  /root/reqlog/data:/var/lib/xm/reqlog:ro（server-prod.yaml），证实 compose
  对 list 字段是合并不是覆盖；XM_REQLOG_MODE/DATA_DIR/METRICS_INTERVAL 均
  按预期解析
GOOS=linux GOARCH=amd64 go build
  -o <scratch> ./cmd/platform-worker/...        — PASS，交叉编译验证（实际
  部署走 deploy/docker/go.Dockerfile 的多阶段构建，这里只做本地代码层面的
  linux/amd64 可编译性抽查）；验证后 go env GOOS/GOARCH 确认未残留
  （windows/amd64）
```

新增测试覆盖清单（对照团队交付文档「合成 index 行，含跨日 24h 窗口、非
2xx、缺目录、坏行跳过并计数」逐项核对）：

- `connectors/reqlog/metrics_test.go`：`TestMetricsReaderDailyStatsAggregates
  AndSkipsBadLines`（非 2xx + 坏行跳过并计数）、
  `TestMetricsReaderDailyStatsMissingDir`（缺目录）、
  `TestMetricsReaderWindowStatsSpansCSTDayBoundary`（跨日 24h 窗口，目录内
  同时含窗口内外的记录以验证 ts_ms 精确过滤而不是整目录纳入）、
  `TestMetricsReaderWindowStatsSameDaySourceIsolated`（source 隔离）、
  `TestMetricsReaderTrendDaysFixedLengthAscendingWithMissingFlag`（固定 7
  元素、升序、以今天结尾、缺目录标 missing）、`TestMetricsReaderCheckRoot`
  （目录不存在/是文件而非目录两种失败形状）、
  `TestToRequestMetricsObservationsShapesAndNulls`（0 请求时 avg/rate 为
  null，trend 的 missing 字段只在缺目录时出现）、
  `TestToRequestMetricsObservationsSuccessRateBpIsIntegerBasisPoints`
  （1/3 请求 → 3333 基点，验证整数半舍入、不经过 float）
- `internal/platform/jobs/reqlog_metrics_test.go`：
  `TestReqlogMetricsSuccessWritesAllSixMetrics`（一轮写满两平台六个键）、
  `TestReqlogMetricsRootUnreadableWritesFailureForAllSix`（挂载整体不可读
  时六条全部落失败观测带错误码，不是安静报 0）、
  `TestReqlogMetricsFailurePreservesLastSuccess`（先成功一轮再失败一轮，
  失败观测沿用上一次的 observed_at/value）、
  `TestReqlogMetricsAppendsSampleForEveryObservation`、
  `TestReqlogMetricsWorkerRejectsMissingCollaborators`、
  `TestReqlogMetricsHonorsCancellation`、`TestParseReqlogMetricsMode`（含
  fake/real/拼写错误退化 off 且 recognized=false 的三个用例）
- `cmd/platform-worker/config_test.go`：默认 off、file 模式读取三个变量、
  fake/real/typo 不报错退化 off、非法 interval 报错

## not_run

- **未连接真实生产环境**：没有 SSH 到 fiberstate 服务器，没有对着真实的
  `/root/reqlog/data` 验证聚合结果与 CST 目录切分的实际表现；
  `docker compose config` 用的是本地临时占位环境变量，不是服务器 `.env`。
- **未验证真实数据量级下的性能**：`WindowStats`/`TrendDays` 每次调用都会
  重新扫描对应的 `index.jsonl`（一轮同步对两个平台各扫 1（daily）+最多
  2（window）+7（trend）= 最多 10 次文件读取，两个平台共 20 次），在项目
  记忆记录的量级（约 13k 请求/日）下预期可接受，但没有拿真实文件量测过
  单轮耗时；`reqlogMetricsReadTimeout` 定的 20s 预算是按类比 sub2api/newapi
  的上游超时选的，不是实测出来的。
- **未跑前端相关门禁**：本任务明确前端由另一个代理并行实现、契约不可改，
  未运行 `pnpm` 系列命令，也没有看过前端消费这些指标的实际渲染效果。
- **未做 `docker build`（真正构建镜像）**：只做了 `docker compose config`
  的静态解析验证 + 本地 `GOOS=linux go build` 抽查，没有实际执行
  `deploy/docker/go.Dockerfile` 的多阶段构建来产出 `platform-worker` 镜像。
- **未验证 rollup/downsampling 消费端的实际行为**：本片只保证
  `LoadRollupPolicies` 在六个新键上给出**显式 exclusion**（让既有的
  policy-coverage 完整性校验通过），没有去看降采样任务（DS1/DS2 等）在
  遇到 excluded 指标时的实际调度行为——按 `invoice.*` 的既有先例推断是
  "跳过，不生成日桶"，但本片没有专门写集成测试验证这一点。

## risks

1. **`success_rate_24h`/`trend_7d` 两个指标被排除在 rollup policy 之外，
   `*.requests.daily` 陪同排除**：`ops.RollupPolicy` 的四种 `ValueKind`
   （gauge/daily_snapshot/additive_delta/document_status）都假设"一条样本
   对应一个可用单个 JSON pointer 取出的标量，可选一个业务日"。
   `success_rate_24h` 是**滚动 24 小时窗口**而不是按业务日切分的快照，
   `trend_7d` 把**七个日桶打包进一条观测的数组**里——两者都不符合这个
   形状假设，勉强套一个 `gauge`/`document_status` 进去会让 policy 的
   `primary_json_pointer` 语义变得含糊（"这条 gauge 的 primary 到底代表
   哪一刻"）。`*.requests.daily` 本身是标准的 `daily_snapshot`
   （`day`+`request_count`+`success_count`+`failure_count`+
   `avg_duration_ms`），技术上可以现在就给它设计一条 active policy，
   但为避免在 90 分钟时间盒内只做半套设计（三个键里两个不兼容、一个
   兼容，容易让降采样消费端的实现认知产生"这一族指标里有的能降采样有的
   不能"的不一致心智模型），本次**三个键一并排除**，用同一个 gate
   `XM-REQLOG-METRICS` 标注，留给专门设计这批 policy 的后续片一次性做完。
   影响面：这六条指标目前**只有原始 5 分钟粒度的样本**（`ops.
   metric_observation_sample`），没有日粒度的降采样结果
   （`ops.metric_observation_daily`）；如果前端或未来的报表需要长跨度
   （几个月）的请求量趋势图，现阶段只能查原始样本表（受
   `MaxSampleLimit`=1000 点上限约束），不能走降采样后的日桶。
2. **`WindowStats`/`TrendDays` 每轮重新全量扫描 `index.jsonl`，没有增量/
   缓存机制**：与 `connectors/reqlog/file_client.go` 的 `ListRequests`
   （同样全量扫描）是同一条已知取舍，理由也相同——按目录名做时间剪枝在
   CST/UTC 换算边界容易漏数据（宪法 1 条"准确优先于速度"）。当前量级下
   预计可接受，量级明显增长后可能需要与 `file_client.go` 的 risks #4
   一起解决（见 `docs/handoffs/slices/XM-REQLOG-MERGE.md`）。
3. **`avg_duration_ms`/`success_rate_bp` 的四舍五入规则是本片自行选定的
   （半舍入，`(a + b/2) / b`），团队交付文档没有明确规定舍入方式**：
   已在 `connectors/reqlog/metrics.go` 的 `roundDiv` 注释里写清楚选择的
   规则；如果前端或其他消费方对精确的边界值（例如 33.335% 到底是 3333
   还是 3334 基点）有不同预期，需要回来对齐，但契约本身只规定"基点整数，
   禁止 float"，没有规定具体舍入方向，本片按最常见的通用惯例实现。
4. **ReqlogMetricsMode 与 platform-api 的 reqlog 模式共享同一个环境变量名
   却有不同的取值集合**：`XM_REQLOG_MODE=fake`/`real` 对 platform-api
   合法（服务"请求详情"链路），对 worker 会被 `ParseReqlogMetricsMode`
   退化成 `off` 并打一条 warn 日志。这是有意设计（详见
   `ParseReqlogMetricsMode` 的注释），但运维如果只看 worker 侧的 warn
   日志、不知道这是"正常且预期"的退化，可能会误以为配置出错——已在
   `main.go` 的启动日志里把 `reqlog_metrics_mode_recognized` 打出来，
   `warn` 日志文案里也解释了"这是请求详情链路的合法值，与本任务无关"，
   降低误判概率。

## follow_ups

- 设计并激活 `sub2api.requests.daily`/`newapi.requests.daily` 两条的 active
  rollup policy（`value_kind=daily_snapshot`，`primary_json_pointer=
  /request_count`，`business_day_json_pointer=/day`），让它们享受日粒度
  降采样；`success_rate_24h`/`trend_7d` 则需要先决定是否要给 `RollupPolicy`
  引入新的 `ValueKind`（例如"滚动窗口"、"多日数组"）才能激活，这是一次
  独立的设计任务，不建议在补丁性质的小改动里顺手做。
- 验收线在服务器上对着真实 `/root/reqlog/data` 跑一轮 `platform-worker`，
  核对六条指标的实际数值与看板卡片渲染效果，并把结果补进一份
  `docs/evidence/EV-<日期>-reqlog-metrics-verify.md`（本片未创建，因为
  没有真实服务器可验证）。
- 观察真实数据量级下 `reqlog_metrics` 任务的实际耗时；如果接近或超过
  `reqlogMetricsReadTimeout`（20s）与任务周期（5 分钟）的余量，考虑给
  `WindowStats`/`TrendDays` 加缓存或按目录名安全剪枝（见 risks #2）。
- 前端如果需要在概览卡片上展示"这条聚合链路当前是 off 还是 file"这类
  运维状态（而不只是"有没有观测"），可以消费
  `internal/platform/jobs` 的启动日志字段或另开一个只读运维端点——本片
  只保证"缺观测=未接入"这一层，没有再往上做运维可视化。
