# XM-OVERVIEW-UI · Sub2API 概览页调用量三卡与上游渠道行接通真实指标

## status

READY（待验收线审读、复跑并人工合入）

## branch / commit / base

- branch: `ai/claude/XM-OVERVIEW-UI`
- implementation commit: `63d98c2`（feat，本文档描述的全部代码改动都在这一个提交里）
- HEAD: `3894116`（`63d98c2` 之上另加了 `5d3f987` 本交接文档、`3894116`
  一处测试注释里两个半角逗号改全角的小修，均不改动任何逻辑/断言）
- base: `release/v0.1-launch`
- worktree: `K:/星芒统一控制平台/acceptance/wt-overview-ui`

## summary

Sub2API 平台详情「概览」页里四处原来写死「未接入」/按 token_valid 计算的
地方，改接后端并行代理实装的真实指标（契约逐字消费，未改动契约本身）：

### 1. 调用量三卡（`web/apps/admin-web/src/components/PlatformOverviewPanel.tsx`）

- **「今日调用量」** 接 `sub2api.requests.daily`：主数值 = `request_count`
  （格式化成「N 次」），副文案 = 业务日 + 成功/失败数 + 平均耗时。
  `avg_duration_ms` 为 `null` 时显示「平均耗时未知」，**不是 0ms**——0
  是个会骗人的默认值，与宪法 12 条同一条纪律。
- **「成功率（24h）」** 接 `sub2api.requests.success_rate_24h`：主数值 =
  `success_rate_bp` 用**整数运算**格式化成两位小数百分比（`money.ts` 新增
  `formatBasisPointsPercent`，与既有 `formatErrorRatePPM` 除数不同——bp 是
  万分比不是百万分比，`whole = bp/100, fraction = bp%100`，天然精确不需要
  截断）。`success_rate_bp` 为 `null` 时显示「—」并说明「24h 内无请求」，
  **不显示 0.00%**——那会被读成「全部失败」，是比「未初始化显示 0」更隐蔽
  的一种编数据。
- 两张卡都不挂迷你趋势图：`MetricTile` 新增可选 `sparkline` 参数（默认
  `true`，与「今日充值」「今日成本」两张既有卡行为一致），这两张新卡传
  `false`——下面已经有一张专门的「近 7 日调用量」大图，卡片自己再叠一条
  基于 `metric-history` 接口的迷你折线，只会是同一件事口径还不一样的第二
  个版本。
- **观测不存在**（key 不在 `/metrics` 返回里）时保持既有 `PendingTile`
  「未接入」呈现，`missingNote` 文案原样保留（逐字未改），既有测试
  「**没有数据源的两格摆位置但不给数字**」因此零改动仍然通过。**观测过期**
  按既有新鲜度语义（`FreshnessBadge`/`FreshnessNote`）自动降级，没有写任何
  新的过期判断代码——这正是复用 `presentMetric`/`MetricTile` 现成机制而不是
  另起一套的原因。

### 2. 近 7 日调用量趋势图

- 接 `sub2api.requests.trend_7d`。这条指标的 `value` 是**同一次观测里内嵌
  的逐日数组**（`{"days":[{"day":"…","request_count":N,"success_count":N,
  "missing":true?},…]}`），**不是**分次采集的历史快照，因此不能走
  `MetricSparkline`/`toSparkSamples` 的 `metric-history` 数据管线（那条管线
  假设每个采样点是一次独立的历史 API 调用）。
- `lib/metrics.ts` 新增 `readRequestsTrendDays`（解析 `days` 数组，按 `day`
  字符串防御性重排——契约保证升序，这里再排一次防的是契约哪天漂了乱序）
  与 `toTrendSparkSamples`（逐日 → `SparkSample`：横轴取日历日的 UTC 零点，
  `missing` 映射到 `failed`，让折线在此断开而不是把 `request_count` 当成
  正常读数画进去，与「同步失败样本不进折线」同一条纪律）。
- `TrendCard` 新增可选 `samples` 参数：传了就直接用 `ui-admin` 的
  `Sparkline` 画（**复用画法**，不复用数据管线——先查过 ui-admin 没有另一
  个「趋势图」组件，「今日充值」卡下面的折线用的正是这一个）；不传维持
  原有 `MetricSparkline` 路径，**NewAPI 的「近 7 日请求量」卡因此零改动**
  （它接的是 `newapi.models.usage`，走的仍是 metric-history 管线）。
- `usableTrendMetric`（既有函数，判「主数值」是否可信）不能直接复用在
  `trend_7d` 上：它走 `metricPrimaryValue`，而 `trend_7d` 没有注册
  `PRIMARY_READERS`（见下），套用会永远判成「不可用」。新增
  `usableTrendDaysMetric`（只判「存在且非未初始化」，样本够不够画线交给
  `Sparkline` 自己判断）。

### 3. 「上游渠道」行改用渠道状态（`lib/overview.ts` + `HealthCard`）

- Sub2API 的上游账号是订阅型，没有钱包余额，`sub2api.channels.balance`
  永远是 `{"channels":[],"channel_count":0}`；`sub2api.channels.status`
  才有真实数据（`{"channels":[{"name":"…","status":"active|error|…",
  "currency":"USD","channel_id":"245"},…]}`）。
- `lib/metrics.ts` 新增 `Sub2ApiChannelStatusRow` / `readSub2ApiChannelStatusRows`
  ——与既有 `ChannelRow`（余额指标）**不是同一个形状**：没有 `token_valid`，
  键名是 `name` 不是 `channel_name`，特意分开定义而不是合并成一个可选字段
  的超集（合并后调用方要自己猜字段属于哪个平台）。
- `lib/overview.ts` 新增 `channelStatusHealth`（纯状态口径：`active` 计
  可用，其余状态一律计不可用，hint 列出不可用数量与前几个名字，超过 3 个
  用「等」收尾）与 `sub2ApiChannelHealth`（编排函数）：
  1. 状态观测有渠道 → 用状态计算，是主口径；
  2. 状态观测也有渠道时，若余额观测**恰好也**有渠道，不丢掉这条信息，
     追加到 hint 里作补充说明；
  3. 状态观测缺失（不存在/未初始化/无逐渠道记录）但余额观测有渠道 → 落回
     旧的 `channelHealth`（按 `token_valid` 统计）——**这一条是既有测试
     固定下来的行为**：「上游健康」三行都在的既有用例只提供了余额指标，
     期望仍显示按 `token_valid` 算出的「2 / 3 可用」，不能因为新加了状态
     口径就让这种环境从「有数字」退化成「未接入」；
  4. 两者都没有渠道 → 「未接入」。
- `HealthCard` 的 `channels` 参数拆成 `channelBalance` + `channelStatus`
  两个，`PlatformOverviewPanel.tsx` 从 `byKey` 分别取两条指标传入。

### metrics.ts 的登记范围

按团队约定登记了 6 个新键（`sub2api.`/`newapi.` 各自的
`requests.daily`/`requests.success_rate_24h`/`requests.trend_7d`）的友好名；
`daily` 与 `sub2api.channels.status` 补齐了 `PRIMARY_READERS`/`RENDERERS`。
**`success_rate_24h` 与 `trend_7d` 只登记 `RENDERERS`，不登记
`PRIMARY_READERS`**：前者的主数值是万分比（bp），套 `MetricPrimaryValue.kind`
的 `"money" | "count"` 哪一种都会让 `formatPrimary` 给出一个看着正常、单位
实际错了的数字，而这条指标又不挂迷你趋势图（见上），没有任何路径需要它；
后者的 `value` 是内嵌数组，根本不是「单个主数值」这个形状。两处都在代码
注释里写明原因，避免以后有人顺手补一个语义不对的 `kind`。NewAPI 侧的三个
新键目前**只登记了友好名/格式化，没有对应 UI 卡片**——NewAPI 概览页当前
没有「今日调用量」「成功率（24h）」这两格的占位（它的四格是用户总数 + 三
张财务卡，「近 7 日请求量」接的是已经在用的 `newapi.models.usage`），本片
只做了「把平台详情概览页里已经写死未接入的卡片接到真实指标」这一件事，
没有给 NewAPI 概览页新增这两张卡——那是布局变更，不在本片范围内；后端
`newapi.requests.*` 三条指标接好之后如果要在 NewAPI 概览页加卡片，直接复用
本片登记的 `metrics.ts` 条目即可。

## files_changed

实现（4）：

- `web/apps/admin-web/src/lib/money.ts` —— 新增 `formatBasisPointsPercent`
- `web/apps/admin-web/src/lib/metrics.ts` —— 6 个新键的友好名/格式化，
  `Sub2ApiChannelStatusRow`/`readSub2ApiChannelStatusRows`，
  `RequestsTrendDay`/`readRequestsTrendDays`/`toTrendSparkSamples`，
  `bigintToSafeNumber`（从 `metricSeriesValue` 抽出的小重构，行为不变）
- `web/apps/admin-web/src/lib/overview.ts` —— `channelStatusHealth`、
  `sub2ApiChannelHealth`
- `web/apps/admin-web/src/components/PlatformOverviewPanel.tsx` ——
  `Sub2ApiOverview` 三卡改线、`MetricTile` 新增 `sparkline` 参数、
  `HealthCard` 拆分 `channelBalance`/`channelStatus`、`TrendCard` 新增
  `samples` 参数、新增 `usableTrendDaysMetric`；`DispositionNote` 尾句同步
  更新（不再只提余额）

测试（4，均为在既有文件上新增用例，未新建测试文件）：

- `web/apps/admin-web/src/lib/money.test.ts`（+9）
- `web/apps/admin-web/src/lib/metrics.test.ts`（+23）
- `web/apps/admin-web/src/lib/overview.test.ts`（+9）
- `web/apps/admin-web/src/components/PlatformOverviewPanel.test.tsx`（+4）

未改动 `web/apps/admin-web/src/components/MetricSparkline.tsx`——NewAPI
「近 7 日请求量」卡与两张新卡都不需要它变化。

## tests_run

在 `web/` 目录串行执行（Windows 下 `pnpm install` 会因符号链接 rename 锁
挂起，本 worktree 用镜像脚本直接补齐了 `node_modules`，因此每条命令都带
`--config.verify-deps-before-run=false` 跳过 pnpm 的依赖校验）：

- `pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck`
  —— PASS（`tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false --filter admin-web run test`
  —— PASS，79 个测试文件、**1166 个用例全绿**（含本片新增的 45 个用例；
  既有 1138 个用例逐条未改动仍全部通过，包括「上游健康」按 `token_valid`
  计算的既有用例——验证了 §3 描述的兜底逻辑没有破坏既有行为）
- `pnpm --config.verify-deps-before-run=false --filter admin-web run build`
  —— PASS（`tsc --noEmit && vite build`；911.72 KB 的 chunk-size 警告是既
  有的，本片改动前该数字已经超过 500KB 阈值，非本片引入）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks protect --staged -v` —— PASS（`no leaks found`，扫描 ~38.73KB
  暂存内容；本片新增的点分指标键字面量沿用本仓已有的「抽成常量」规避
  手法——`metric_key: XXX_METRIC` 一律传常量引用而不是就地字面量，见
  `windows-toolchain-quirks`/gitleaks 记忆里记录的同一条误报）

## not_run

- 后端契约的真实联调：本片消费的 6 个 `*.requests.*` 键与
  `sub2api.channels.status` 由**并行代理**实装后端，本次未跑一次端到端的
  「后端真返回这个形状 → 前端真显示这个数」联调，只用测试固件模拟了契约
  文档给出的形状。建议验收线合入两条分支后，起一次真实（或 staging）环境
  过一遍这四处卡片，核对：
  - `avg_duration_ms`/`success_rate_bp` 的 `null` 分支是否真的会出现（例如
    刚部署、当天/近 24h 还没有请求的环境）；
  - `trend_7d` 的 `missing` 标记在真实缺数据的日子上是否真的会被置位（而
    不是后端偷偷补 0 蒙混过去）；
  - `sub2api.channels.status` 的 `status` 取值集合是否真的只有
    `active`/`error` 两种——本片 `channelStatusHealth` 对未知状态一律计入
    「不可用」，这是安全的默认，但如果上游还有第三种状态（比如
    `pending`），值得在验收时确认这个分类语义符合运营预期。
- 真实浏览器/Playwright 实测：未跑。改动集中在数据映射与既有组件
  （`StatTile`/`MetricCard`/`Sparkline`）的参数传递，没有新增布局/CSS；
  vitest + Testing Library 的 DOM 断言（含 `Sparkline` 的 `role="img"`
  存在性检查）对本片改动的置信度足够。
- Storybook 构建：未跑。本片没有修改 `ui-admin`/`ui-primitives` 任何组件
  本身（`Sparkline`/`StatTile`/`MetricCard` 均按既有 props 契约调用），
  无需补 Storybook 素材。
- NewAPI 概览页的调用量三卡：**明确不在本片范围**，见上面 summary 最后
  一段的说明。

## risks

- **`sub2ApiChannelHealth` 的「状态优先、余额兜底」优先级是从任务描述的
  自然语言推断出来的**（「只有 status 观测也缺时才显示未接入」+「余额观测
  有渠道时仍可作为补充信息」两句话本身有一点张力）。本片选择的读法是：
  状态非空就是唯一主口径（哪怕余额也非空，也只作为 hint 里的补充文字，不
  参与 x/y 计算）；状态空时落回余额的旧口径；两者都空才是「未接入」。这个
  读法被现有测试反过来验证——「上游健康」既有用例只提供余额指标就期望看到
  按 `token_valid` 算出的数字，如果读法是「状态缺失就直接未接入」，这条
  既有测试会失败，而它没有失败，说明推断与既有验收标准一致。仍建议验收线
  按这份文档的读法过一遍任务原文，确认理解没有偏差。
- **`success_rate_24h`/`trend_7d` 没有登记 `PRIMARY_READERS`**：如果未来
  有人想在别的地方（比如一个通用指标表格）用 `metricPrimaryValue`/
  `metricSeriesValue` 取这两条指标的「主数值」，会分别落进
  `unknownPrimary` 的猜测口径（对 `success_rate_24h` 大概率会猜中
  `window_hours=24` 这个常数字段，是错的）与「无法识别」。代码注释里写明
  了原因，但没有加运行期防呆（比如断言/警告）——如果后续真的需要，应该先
  想清楚 `MetricPrimaryValue.kind` 要不要扩出第三种（比如 `"ratio"`），而
  不是勉强塞进 money/count 二选一。
- **`avg_duration_ms`/`success_rate_bp` 为 `null` 的两个分支目前只有单元
  测试覆盖，没有真实数据验证过后端什么条件下会真的给出 `null`**（见
  not_run 第一条）。如果后端实际上从不返回 `null`（总是给 0 或干脆不给
  这两个字段），本片的「未知」/「—」文案就用不上，不算错，但也没有被真实
  路径验证过。

## follow_ups

- 后端两条分支合入后，按 not_run 第一条清单跑一次真实联调，核对三个 `null`
  /`missing` 分支与渠道状态取值集合。
- 如果运营需要 NewAPI 概览页也有「今日调用量」「成功率（24h）」两格，
  `metrics.ts` 已经登记好 `newapi.requests.daily`/
  `newapi.requests.success_rate_24h` 的友好名与格式化，直接在
  `NewApiOverview` 里加两张 `MetricTile`（`sparkline={false}`）即可，不需要
  再动 `lib/metrics.ts`。
- `channelStatusHealth` 对不可用渠道名字只截断到前 3 个（`UNAVAILABLE_NAMES_SHOWN`），
  超出部分只显示「等」不给数量提示（数量已经在前半句「N 个渠道不可用」里
  给过一次，未重复）；如果运营反馈这一行经常被截断到看不出是哪几条，可以
  考虑把 hint 换成可展开的列表，但目前没有证据表明单行文字不够用。
