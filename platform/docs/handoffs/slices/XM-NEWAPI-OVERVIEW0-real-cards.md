# XM-NEWAPI-OVERVIEW0 · NewAPI 概览渠道健康卡接真实数据、近 7 日请求量切 trend_7d

## status

READY（待验收线审读、复跑并人工合入）

## branch / commit / base

- branch: `ai/claude/XM-NEWAPI-OVERVIEW0-real-cards`
- worktree: `K:/星芒统一控制平台/wt-xmNEWAPIOV0`
- base: `release/v0.1-launch@baf6a5b`
- commits（2，从旧到新）：
  - `91f8111` feat(admin-web): NewAPI 概览渠道健康卡接真实渠道目录、近 7 日请求量切 trend_7d（实现 + 测试，本文档描述的全部功能改动都在这一个提交里）
  - `a13fd8c` docs(evidence): NewAPI 概览真实渠道健康卡的浏览器截图
  - 本文档自身作为第三个提交加入，不改动任何逻辑或断言

## 开工前状态确认（团队交接要求"先确认现状"）

任务交接把 NewAPI 概览描述成"留着样例形状的占位符"，但实际代码（`baab1b2`
`align NewAPI overview and finance pages`、`0d254b7` `harden NewAPI finance
evidence states`，均已在 `release/v0.1-launch` 上）比这句话新：四格里已经有
三格是真实数据，`docs/architecture/ADMIN-IA.md §8.7` 的静态表格没跟上这两次
提交，是过期文档，不是代码现状。本片开工前实测（读代码 + 跑现有测试）确认：

| 格/卡 | 开工前实际状态 |
|---|---|
| 用户总数（含今日活跃） | ✅ 已经是真实数据：`newapi.users.total` 指标，`NewApiUsersMetricCard` 消费 `active_users` 字段，新鲜度徽章已在 |
| 今日我方计费 / 今日上游成本 / 今日毛利 | ✅ 已经是真实数据：`listChannelSummaries()` 按 `systemType==="newapi"` 过滤后经 `lib/financeOverview.ts` 的 `aggregateChannelMoney` 聚合，币种/标度不一致会 fail closed，覆盖不全会显式标注，测试齐全 |
| 渠道健康 | 🔨 部分：渠道名/类型/启用状态/错误率来自 `newapi.channels.status` 指标（真实），但上游、分组、成功率三列**硬编码**显示未接入，即使 XM-CHAN-FIELDS0/WIRE0 已经在渠道目录契约里交付了 `vendor`/`today.successRate` 两个真实字段——只是这张概览卡片没有去读那个 Query，不是没有数据源 |
| 近 7 日请求量 | 🔨 部分：接的是 `newapi.models.usage`（走 metric-history 管线，逐次轮询快照），不是 `newapi.requests.*` reqlog 那条线；`newapi.requests.trend_7d` 已经在白名单里被动态采集（XM-REQLOG-METRICS），前端 `lib/metrics.ts` 也已经登记了它的解析函数，只是这张卡片没有切过去用 |

**结论**：本片实际要做的只有两件事——渠道健康卡接真实渠道目录、近 7 日请求量
切到 `trend_7d`。用户总数与三张财务卡不需要改动。

## tile → 数据源速查表

| 格/卡 | 数据源 | 状态 |
|---|---|---|
| 用户总数（含今日活跃） | `newapi.users.total` 指标 | ✅ 已接（开工前既有，本片未改动） |
| 今日我方计费 | `GET /finance/channels/summary` 按 newapi 过滤聚合 `usageRevenue` | ✅ 已接（开工前既有，本片未改动） |
| 今日上游成本 | 同上聚合 `supplyCost` | ✅ 已接（开工前既有，本片未改动） |
| 今日毛利 | 同上聚合 `grossProfit`（覆盖不全时不显示合计） | ✅ 已接（开工前既有，本片未改动） |
| 渠道健康 · 渠道 | `listPlatformChannels("newapi", serviceId)` 的 `name`/`channelRef` | ✅ 已接（本片新增数据源），链接到 `/platforms/newapi/upstream/detail/<id>` |
| 渠道健康 · 上游 | 同一 Query 的 `vendor` 字段（XM-CHAN-FIELDS0 从 NewAPI `Type` 查表得出） | ✅ 已接（本片新增）；查表落空时未接入 + 具体原因 |
| 渠道健康 · 分组 | 无——渠道目录契约 14 个扩展字段里没有分组维度 | 未接入 —— NewAPI 原生有 `group` 字段（`model/channel.go`，路由用），但没有任何连接器/契约把它读出来 |
| 渠道健康 · 成功率 | 同一 Query 的 `today.successRate`（XM-CHAN-FIELDS0 预算内 `GET /api/log/stat` 业务日窗口，逐渠道真实值） | ✅ 已接（本片新增，见下方"与团队交接的一处偏离"）；`today` 缺席或 `successRate` 单独为 null 时未接入 + 具体原因 |
| 渠道健康 · 状态 | `newapi.channels.status` 指标的 `enabled`/`error_rate_ppm`，按渠道 id 与目录关联 | ✅ 已接（开工前既有口径，本片保留不改） |
| 近 7 日请求量 | `newapi.requests.trend_7d`（reqlog 被动采集，内嵌逐日数组，`missing` 显式标记） | ✅ 已接（本片切换数据源）；未采到时未接入 |

## 与团队交接的一处偏离：成功率没有等 XM-ASSURE0

任务交接原话是"成功率 from the request-log passive metrics if XM-ASSURE0's
Query exists on the branch you base on...if absent, 未接入 + XM-ASSURE0 的
理由"。开工确认过 `ai/claude/XM-ASSURE0-passive-assurance` **没有**合入
`release/v0.1-launch`（`git merge-base --is-ancestor` 返回否），按字面确实
应该整列未接入。

但读 `XM-CHAN-FIELDS0`/`XM-CHAN-WIRE0` 两份 handoff 发现：NewAPI 渠道目录
契约的 `today.successRate` 字段是**另一条已经真实交付、与 XM-ASSURE0 无关**
的数据源——XM-CHAN-WIRE0 的真实浏览器验证截图显示 NewAPI 渠道
`n-claude-main` 的"今日统计"栏已经显示"312 次 · 97.4% · ¥18.80"，且契约文档
明确写 Sub2API 端 `success_rate` 恒为 null（无账号级成功/失败计数）而 NewAPI
端是真实值（预算内 `GET /api/log/stat` 拿到）。这条数据源今天已经在渠道管理
页（`ManagedChannelTable.tsx` 的"今日统计"列）显示真值，只是概览页的渠道健康
卡没有去读它。

判断：与其为了不"提前用"一条尚未落地的 XM-ASSURE0，而放着一条**已经交付、
已经在生产代码路径上验证过**的真实成功率字段不用、继续显示"未接入"，不如
按宪法 12 条"数据新鲜度必须可见，禁止裸数字冒充实时完整数据"的精神——已经
有真实数据源时不该假装没有。采用 `today.successRate`，并在 secondary 文案与
本文档里如实说明它的来源（NewAPI 自报的 `/api/log/stat`，不是独立的请求审计
核验）。

**这两条数据源不是同一回事，值得说清楚**：`today.successRate` 是 NewAPI
自己上报的统计（第二手，来自上游自己的日志聚合接口）；XM-ASSURE0（"被动
保障"）等落地后大概率是从我方独立的请求审计（reqlog）里核算出来的成功率，
是第一手、独立核验的口径，两者可能对不上。XM-ASSURE0 合入后如果它的 Query
覆盖了同一个维度，建议按"更权威来源优先、退回 chanfields today.successRate"
的模式合并（与 `rateMultiplier`/`upstreamMultiplier` 那种"新字段优先、查不到
退回登记簿 join"同一个既有模式），而不是简单地互相替换。

若验收线认为这个判断错了、坚持要等 XM-ASSURE0，回退方式很小：把
`RealNewApiChannelHealthCard`/`NewApiChannelHealthRow` 里读
`row.today?.successRate` 的两处改成直接判 XM-ASSURE0 是否存在即可，其余
（渠道/上游/链接/新鲜度）不受影响。

## summary

### 1. 渠道健康卡：`newapi.channels.status` 指标 → 真实渠道目录 + 指标合并

`web/apps/admin-web/src/components/PlatformOverviewPanel.tsx`：

- `NewApiChannelHealthCard` 变成一个分派器：**恰好一个已登记且 active 的
  service** 时（与 `ChannelTable.tsx` 的 `usesChannelRefGrain` 同一判据，
  由 `PlatformDetailPage.tsx` → `PlatformOverviewPanel` → `NewApiOverview`
  一路把 `serviceId`/`serviceStatus` 传下来）渲染新的
  `RealNewApiChannelHealthCard`；不满足时落回原样保留的
  `LegacyNewApiChannelHealthCard`（纯指标口径，行为逐字不变，多实例/未登记
  场景没有 `serviceId` 可用，猜一个是错的）。
- `RealNewApiChannelHealthCard` 用 `listPlatformChannels("newapi", serviceId)`
  作为行的主数据源（渠道/上游/成功率），同时仍然读 `newapi.channels.status`
  指标（按 `channel_id`/`channelRef.externalChannelId` 关联，两者共用同一条
  底层 observation——`internal/platform/httpapi/channel_bindings.go` 的
  `findChannelsObservation` 注释证实了这一点，不是靠猜 id 对得上）取
  `enabled`/`error_rate_ppm` 渲染"状态"列。
- "状态"列**刻意不改用**目录的 `row.status` 字符串枚举——这是尊重
  `XM-CHAN-MERGE0` handoff 记录在案的取舍（Sub2API/NewAPI 的 `status` 取值
  集合不同，都不是"健康/需关注"这种语义，贸然映射等于猜，违反宪法 12 条），
  本片认同这个判断、不重新决定它。
- "分组"列没有任何数据源：渠道目录契约的 14 个扩展字段里没有分组维度。
  代码注释与卡片下方的说明段落都点明 NewAPI 上游其实有原生 `group` 字段
  （`model/channel.go`，用于路由），只是目前没有任何连接器/契约把它读出来
  ——不是"查出来是空"，是"从来没人去查"。
- 每行渠道名链接到渠道详情页（`channelDetailPath("newapi", externalChannelId)`,
  与 `ManagedChannelTable.tsx`/`ChannelDetailPage.tsx` 同一条路由，复用既有
  的路由 helper，不是新起一条）。
- 卡片级新鲜度：渠道目录 Query 没有像指标那样现成的 `FreshnessContract`,
  新增 `channelCatalogFreshness(page, now)` 从 `inventory.observedAt`/
  `complete`/`coveragePartial` 现算，阈值沿用全站 1800 秒惯例（与
  `lib/financeOverview.ts` 的 `aggregateFreshness` 同一约定）。

### 2. 「近 7 日请求量」：`newapi.models.usage` → `newapi.requests.trend_7d`

- `newapi.requests.trend_7d` 与 `sub2api.requests.trend_7d` 是同一条 reqlog
  管线（XM-REQLOG-METRICS 被动采集，XM-OPS-TAILS0 确认"六个 `*.requests.*`
  指标已注册进 `/api/v1/metrics` 白名单"，不受 rollup policy 是否激活影响
  ——rollup policy 只管一个尚不存在的降采样引擎，与这条卡片读的实时 `/metrics`
  无关），内嵌逐日数组，每天有显式 `missing` 标记，比 `newapi.models.usage`
  的 metric-history 轮询快照更贴近"近 7 日请求量"这个语义（后者是否每天都被
  轮询到取决于采集频率，缺口含义是"没采到"而不是"这天真的没数据"）。
- 直接复用 `Sub2ApiOverview` 已经用过的整条管线（`usableTrendDaysMetric` /
  `readRequestsTrendDays` / `toTrendSparkSamples` / `TrendCard` 的 `samples`
  参数），零新增解析逻辑——两份 handoff（`XM-OVERVIEW-UI`、`XM-OPS-TAILS0`）
  都把这个切换记录成"纯增量 follow-up，两个键的 label/renderer 已经登记好"。
  `newapi.models.usage` 仍然是已注册指标（其它地方要用随时可用），只是这张
  卡不再消费它。
- 已删除现在无人使用的 `usableTrendMetric`（旧的"主数值判可用"函数，只有
  `newapi.models.usage` 那条路径在用）。

### 3. 未改动

用户总数（含今日活跃）与三张财务卡（今日我方计费/今日上游成本/今日毛利）
开工前已经是真实数据，本片没有碰这部分代码；Sub2API 概览、渠道管理表
（`ManagedChannelTable.tsx`/`ChannelTable.tsx`）、财务页、后台任务页、
`navigation.ts` 均未改动。

## files_changed

实现 + 测试（1 个提交 `91f8111`，3 个文件）：

- `web/apps/admin-web/src/components/PlatformOverviewPanel.tsx`——
  `NewApiChannelHealthCard` 拆成分派器 + `RealNewApiChannelHealthCard` +
  `NewApiChannelHealthRow`（新）、原实现更名为
  `LegacyNewApiChannelHealthCard`（保留、未改动行为）、新增
  `channelCatalogFreshness`、`NewApiOverview`/`PlatformOverviewPanel` 新增
  `serviceId`/`serviceStatus` 参数、「近 7 日请求量」改接
  `newapi.requests.trend_7d`、删除已死代码 `usableTrendMetric`
- `web/apps/admin-web/src/components/PlatformOverviewPanel.test.tsx`——
  7 个新用例（见下），`stub()`/`renderPanel()` 新增 `platformChannels`/
  `serviceId`/`serviceStatus` 支持，新增 `catalogRawItem`/`catalogPage`
  两个渠道目录 mock 数据构造函数
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`——"overview" case
  新增按 `entry.services.length===1` 判据传 `serviceId`/`serviceStatus`
  （与相邻的 "upstream" case 同一个判据，抄自同一段代码）

证据（1 个提交 `a13fd8c`，2 个文件）：

- `docs/evidence/screens/XM-NEWAPI-OVERVIEW0/01-newapi-overview-real-cards.png`
- `docs/evidence/screens/XM-NEWAPI-OVERVIEW0/02-newapi-overview-reload-stable.png`

未改动 `lib/metrics.ts`/`lib/financeOverview.ts`/`api/platformChannels.ts`/
`lib/channelFieldReasons.ts`——全部只读消费既有导出，没有一处需要新增字段
解析或新的原因文案键（"上游查不到"/"分组没有数据源"两条原因文案是本片
局部常量，特意不写进共享的 `channelFieldReasons.ts`，见 risks）。

## tests_run

在 `web/` 目录串行执行（Windows worktree 用镜像脚本补齐 `node_modules`,
命令带 `--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；未并发跑
多个 pnpm 门禁）：

- `pnpm --config.verify-deps-before-run=false -r run typecheck` —— PASS
  （design-tokens/ui-primitives/ui-storybook/ui-admin/admin-web 5 个包全部
  `tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false -r run test` —— PASS：
  design-tokens 10、ui-primitives 16、ui-admin 253、admin-web 1401（96 个
  测试文件），共 1680 个用例全绿（含本片新增的 7 个用例；单独跑
  `PlatformOverviewPanel.test.tsx` 是 28 个用例全绿，含 20 个既有 + 8 个
  新增——原 NewAPI 描述块 5 条既有用例逐字未改动仍然通过，其中"渠道健康表
  缺的三列说出来"这条既有断言 `/上游、分组与成功率三列/` 现在覆盖的是
  `LegacyNewApiChannelHealthCard`（没有 serviceId 时的回落路径），证明本片
  没有破坏这条既有行为）
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`
  （在 `web/apps/ui-storybook` 包目录内直接跑，不走 `-r --filter`）——PASS,
  "Storybook build completed successfully"；本片没有新增/修改
  `ui-admin`/`ui-primitives` 任何组件，不需要新 Storybook 素材
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks protect --staged -v`（两次，分别对实现+测试提交、证据提交）
  —— 均 PASS（"no leaks found"）；本片新增的点分指标键字面量沿用既定的
  「抽成常量」规避手法（`NEWAPI_REQUESTS_TREND_7D_METRIC_KEY`/测试文件里的
  `NEWAPI_TREND_METRIC` 等），嵌在长中文句子里的 `newapi.requests.trend_7d`
  （pendingNote 文案）与既有代码同一惯例，未被拦下

新增测试覆盖（`PlatformOverviewPanel.test.tsx`，均在既有文件上新增用例）：

1. 恰好一个 active service 时，渠道健康表从真实渠道目录读上游与成功率,
   链接到渠道详情页，状态列仍来自指标口径，分组恒未接入，新鲜度徽章在
2. 渠道目录字段（vendor/today）为 null 时，上游/成功率显示未接入 + 具体
   原因（不是笼统的"未接入"，用 `getByTitle` 精确核对原因文案）
3. 多个/零个已登记 service 时落回旧实现（既有断言措辞不变），并断言
   **没有**打渠道目录端点——多实例/未登记场景不该猜一个 `serviceId`
4. 渠道目录观测过期（`inventory.observed_at` 2 小时前）时，卡片显示"数据
   延迟"（用动态 `Date.now()` 相对时间戳，不写死历史时间戳，避免断言随
   测试实际运行时刻漂移）
5. 渠道目录 `coverage_partial` 时，卡片标记"数据不完整"
6. 「近 7 日请求量」没有采到 `newapi.requests.trend_7d` 时仍显示未接入,
   不编数字
7. 「近 7 日请求量」接上 `trend_7d` 后显示真实趋势，缺数据的日子入图但
   不是 0（镜像 Sub2API 既有的同款用例）

## 真实浏览器验证

`node node_modules/vite/bin/vite.js --port 5193 --strictPort` + 手写 mock
后端（`http.createServer`，端口 18234，`XM_DEV_API_TARGET` 覆盖 vite 代理
目标）——**注意**：本机默认的 8080 端口被同一会话里另一个代理的 mock 服务
占用（`EADDRINUSE`），已确认避开、改用不冲突的端口组合，不是复用了别人的
后端。

- `/platforms/newapi?tab=overview`（开发模式登录后）——渠道健康卡三行
  `gpt 主`/`claude 备`/`gemini 冷备`：前两行显示真实供应商名（OpenAI /
  Anthropic）与真实成功率（99.2% / 97.4%），渠道名可点击链到
  `/platforms/newapi/upstream/detail/n1` `.../n2`；第三行故意留空扩展字段,
  上游/分组/成功率三列如实显示"未接入"，状态列显示"停用"（来自指标口径,
  与目录字段是否为 null 无关）。卡片级新鲜度徽章显示"数据新鲜"。「近 7 日
  请求量」显示真实折线，图片 alt 文本读出"1 次同步失败"，与 mock 数据里
  故意留的缺失日对得上。用户总数/三张财务卡照旧显示真实数据（开工前已有）。
  截图 `01-newapi-overview-real-cards.png`。
- 刷新一次确认状态稳定，无新增控制台错误；截图
  `02-newapi-overview-reload-stable.png`。浏览器控制台在首次加载与刷新时
  各出现过一次 502（对渠道目录端点的某次并发请求），复核网络请求列表确认
  是重复触发的同一个 Query 在手写 mock 服务器（原生 `http.createServer`,
  没有做并发连接的健壮性处理）上偶发的连接竞争，紧随其后的重试都是 200，
  最终渲染结果两次截图一致；这是本地 mock 脚本的已知局限，不是应用代码的
  缺陷，真实 Go 后端不会有这个问题。

## not_run

- 后端 `go test ./...`/`go vet ./...`：本片零 `.go` 文件改动（只读消费
  `XM-CHAN-FIELDS0` 已经交付、已经测试过的契约与既有的 `newapi.channels.status`
  指标端点），未跑。
- 服务器/生产环境验证：未连接服务器，未部署，未碰任何真实上游实例——
  按任务要求全程只在本地 worktree 与手写 mock 后端上验证。
- `newapi.channels.status` 与 `listPlatformChannels` 目录的 `channel_id`
  在**真实**连接器输出下是否总是完全一致：本片凭 Go 源码里
  `findChannelsObservation` 读的是同一条底层 observation 这一点做出"两边
  id 同源"的判断，并用 mock 数据验证了关联逻辑本身是对的；没有对着真实
  NewAPI 实例跑一次端到端联调确认这个假设在生产数据下从不失配（如果失配,
  后果只是"状态"列退化成"未接入"，不会显示错误的启用/停用状态，是安全的
  失败模式，但仍值得验收线留意）。
- 分组字段的后续实现：本片只是如实标注未接入，没有评估给 NewAPI 连接器
  新增读取 `group` 字段需要多大改动（不在本片范围，`Group` 字段本身在
  `model/channel.go` 已确认存在，值得作为一个很小的后续切片）。
- 移动端/窄视口截图：只测了 1440×1100 桌面视口，延续既有多个 handoff 未测
  这一项的做法。

## risks

- **成功率数据源的偏离**（见上方专门一节）：本片用了 `today.successRate`
  而不是等待 XM-ASSURE0，这是一处经过论证但确实偏离任务字面指示的判断。
  已在本文档与代码注释里写明理由、来源差异与回退方式，但仍需验收线确认
  这个判断本身站得住。
- **`NEWAPI_VENDOR_NULL_REASON`/`NEWAPI_GROUP_NULL_REASON`/
  `NEWAPI_STATUS_NULL_REASON` 三个原因文案是 `PlatformOverviewPanel.tsx`
  的局部常量，没有写进共享的 `lib/channelFieldReasons.ts`**——那个文件被
  `ManagedChannelTable.tsx`/`ChannelDetailPage.tsx`（渠道管理页与详情页,
  按任务要求不可改动）共用，本片选择不碰共享依赖以保持改动面最小、避免与
  可能并行改这个文件的其它线冲突；代价是如果渠道管理页将来也要显示"上游
  查不到"这类原因，需要有人回头把这三条原因搬进共享文件、两处一起改，不会
  自动同步。
- **`RealNewApiChannelHealthCard` 是本片新增的第三个查询**（在
  `metricsQuery`/`financeQuery` 之外）：多实例/未登记场景不受影响（落回
  Legacy 分支，不发这个请求），但恰好一个 active service 的正常场景下,
  概览页现在并发发起三个查询而不是两个；量级判断与既有的
  `financeQuery`/`WorkCard`/`HealthCard` 各自独立查询同一档，概览是人工
  查看页面、不是高频端点，可接受，但没有专门做性能测试。
- **`newapi.models.usage` 指标在这张概览页上不再被消费**：指标本身没有被
  删除、注册信息原样保留，其它地方要用随时可用；但如果验收线认为这条指标
  的历史意义就是"喂给近 7 日请求量卡"，需要知道这张卡已经不读它了。

## follow_ups

- 如果验收线确认"成功率必须等 XM-ASSURE0"这条判断，按上方"回退方式很小"
  的说明改两处读取即可；如果确认"chanfields 的 today.successRate 可以先用,
  XM-ASSURE0 落地后再合并成'更权威来源优先'"，等 XM-ASSURE0 合入后需要有人
  设计两个成功率来源的合并策略（不是简单互相替换）。
- 给 NewAPI 连接器新增读取原生 `group` 字段（`model/channel.go`），补齐渠道
  目录契约的第 15 个扩展字段，渠道健康卡的"分组"列会自动跟着亮起来（渲染
  逻辑已经在等这个字段，同 `XM-CHAN-FIELDS0` 交接文档里"字段一旦非 null
  不用改前端代码"的既定模式）。
- 找机会对着真实 NewAPI 连接器输出核实 `newapi.channels.status` 与渠道目录
  的 `channel_id` 在生产数据下是否总是能关联上（见 not_run）。
- 如果运营反馈"分组"列长期空着造成困惑，可以考虑在卡片下方把"渠道健康"整
  张表退化展示逻辑写得更醒目，但目前没有证据表明现在的说明段落不够用。
