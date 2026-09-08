# XM-WORKBENCH-TRUTH：运营工作台上五处「安静地给你一个旧答案」的地方

- **status:** implemented，已在分支上提交。
- **branch:** `ai/claude/XM-WORKBENCH-TRUTH`，基线 `e4f7dcf`。
- **来源：** `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md`。那份报告查完
  原因后留下五条**显示层**的债，本片把它们还掉。只改 `web/`，**没有动任何 Go
  文件**，没有连生产 / 数据库，没有查看任何密钥文件。
- **时间：**
  - 第一轮实现：开始 2026-09-08T16:14Z，结束 2026-09-08T16:52Z（约 38 分钟，
    含 15 次变异验证）。
  - 第二轮（处理评审的三条阻塞级问题）：开始 2026-09-08T17:02Z，结束
    2026-09-08T17:31Z（约 29 分钟。见文末「评审回合二」；含 23 次变异验证——
    15 条旧的全部重跑 + 8 条新的）。

## 一句话结论

这一片修的不是「界面不好看」，是**五个位置在用一个看起来正常的样子回答一个它
其实答不出来的问题**：状态矩阵把正在发生的故障显示成中性灰、开票那一行三个词
永远不变、288 条同类作业把待办格占满、「触发 N 次」把分钟数说成事故次数、黄灯
亮了两周一个字的说明都没有。

**注意验收时的一个反直觉之处：改了排序，今天的界面很可能一点变化都没有。**
Sub2API 眼下六条指标多为 fresh、只有渠道余额是 uninitialized、没有 failed，
所以那一格改完仍显示「未初始化」。这不是改动失败——本片做的是**解除失明**，
不是点亮红灯。真正的证据在测试里：构造 failed + uninitialized 并存的用例，旧
实现给 uninitialized，新实现给 failed。

## 改了什么

### 1. 状态矩阵取最差的顺序（`lib/workbench.ts`）

`FRESHNESS_RANK` 原来是 `{ uninitialized: 0, failed: 1, … }`，把「未初始化」排
在「同步失败」**之前**，与后端 `internal/platform/ops/freshness.go` 的
「失败 > 未初始化 > 延迟 > 部分 > 新鲜」正好相反。

- 新增导出常量 `FRESHNESS_PRIORITY`（worst-first 的字符串数组），`FRESHNESS_RANK`
  由它派生，**不再手写数字**。
- **顺序没有再抄一份成为无人看管的副本**：`labels.reconcile.test.ts` 新增一组
  「数据新鲜度」，从 `freshness.go` 的 `Freshness()` **函数体**按 `f.State =
  StateX` 首次出现顺序机械推导，与前端逐值对账；同时对账后端自己的文档注释
  （那句「优先级：失败 > 未初始化 > …」）与它自己的实现。
- 兜底 `?? 0` 改成显式的 `UNKNOWN_STATE_RANK = -1`。**这一条不是顺手加的**：
  `?? 0` 今天恰好等于「最差」只因为 0 是 uninitialized 的档位；把 0 让给 failed
  之后它会变成「与失败并列」，加上 `rank < worstRank` 的严格小于，后端新增的状态
  会被先到的 failed 静默吃掉（「条件恰好为真 ≠ 条件正确」）。

### 2. 「为什么是这个状态」写进格子（`workbench.ts` + `OverviewPage.tsx`）

`MatrixRow` 新增三个可选字段：`freshnessNote` / `freshnessEvidence` / `scopeNote`，
在矩阵表里渲染成徽章下 / 平台名下的一行小字（未使用任何新的颜色、圆角、阴影）。

- **partial**：点名该平台里哪几条指标 `is_partial`（用 `metricLabel` 的中文名），
  加一句通用解释「这一轮采集成功了，但其中一部分数据上游给不出来……这不是故障，
  也不会自己好转」；相关指标的 `watermark` 原样放进悬停当工程证据，**只展示不
  解析**（NewAPI 那条里确实带着 `subscription:unavailable_over_http`，但那是连接器
  私有自由文本，解析它等于在前端复刻一份后端事实）。
- **uninitialized**：区分「这个平台还没有任何指标在采」（服务器）与「有指标在采，
  但『X』从未采到值」（Sub2API 的渠道余额）。**这个区分不需要任何新后端字段**，
  从有没有指标行就能得出。
- **不说「不适用」。** 见下面「依赖的后端字段」第 5 条。

### 3. 开票系统那一行由数据算出来（`workbench.ts`）

原来是 `rows.push({ statusLabel: "未接入", freshness: UNINITIALIZED, events: 0,
observedAt: null, … })` 六个字面量，不查任何数据。现在走 `invoiceRow()`，与四个
平台行同构：按 `invoice.` 前缀过滤指标与告警、按 `service_type === "invoice"` 找
登记实例。

- 状态三级口径：有实例 → `describeServiceStatus`；无实例但有 `invoice.*` 指标 →
  「未登记」；两者皆无 → 「未接入」（**今天走的是这一支，算出来仍然是「未接入 /
  未初始化 / 0」——诚实不等于换结论**）。
- `invoice.requests.daily` / `invoice.amount.daily` 已经在 ops 的
  `registeredMetrics` 白名单里，所以「接上了会自动变」是一条真通路。
- 固定挂 `scopeNote`：「这一行说的是只读数据对接……点链接进去的是嵌到平台里的
  开票管理端，那条线已经能用——两者不是一回事。」
- **与 `platforms.ts` 的 `NON_PLATFORM_SERVICE_TYPES` 不矛盾**（那边显式把 invoice
  挡在侧栏平台段外）：侧栏问「哪些是平台」，矩阵问「我管的这些系统怎么样」。
  代码注释里写明了，别把其中一处当成 bug 顺手改掉。

### 4. 同 kind 的已放弃作业合并成一行（`workbench.ts` + `OverviewPage.tsx`）

`workItemsFromJobRuns(runs, now, options?: { truncated?: boolean })`：

- 按 `run.kind` 分组（**不是按 queue**——生产上 288 条 card_sync 与别的任务同在
  `default` 队列，按队列合并等于换一种方式丢信息）。
- `count === 1` 时**逐字保持原样**，不出现「×1」，也不给一个空的展开箭头。
- `count > 1` 时标题 `卡片数据同步 已放弃 ×288`，meta 给「最近 N 前 · 最早 M 前」，
  明细挂在 `children` 上（按放弃时刻倒序）。
- 组间按条数降序，同数保持取数顺序。
- 界面上展开与跳转是**两个可点区域**（`<button aria-expanded>` + 单独的 `<Link>`），
  明细用**条件渲染**而不是 `<details>` / CSS 隐藏——后者在 jsdom 里折叠时内容仍
  在 DOM，那条「折叠时看不到明细」会恒真。已做变异验证（见下表 M5）。
- **截断这一位必须传下去**：取数上限是 20，生产真实是 288。合并后若只显示「×20」
  且不说它可能不全，界面会从「288 行刷屏」退化成「一个看起来权威的错数字」。
  分工：粒度归 `×N` 后面那个 `+`，整句归 `truncationNote`（「已放弃的后台任务只
  取了 20 条」）；卡片说明段也补了一句「计数只统计这最多 20 条」。

### 5. 「触发 N 次」→「评估 N 轮」（`api/alerts.ts` 起，四个消费方）

`fire_count` 是每 60 秒重评一轮、条件仍成立就 +1 的**轮数**（`db/queries/alerts.sql`
的 `TouchAlert: fire_count = fire_count + 1` + `jobs/alert_evaluate.go` 的
`DefaultAlertEvaluateInterval = 60 * time.Second`；生产上 669 = 668 分钟 + 1）。

同一句错话原来有**五份措辞不同的副本**。现在全部从 `api/alerts.ts` 一处取：

| 位置 | 原来 | 现在 |
|---|---|---|
| `api/alerts.ts` 字段注释 | 「被去重合并掉的命中次数（含首次）」 | 如实说明 + 出处 |
| `lib/workbench.ts` 待办右列 | `触发 N 次` | `describeFireCount().combined` |
| `pages/AlertsPage.tsx` 列头 / 悬停 / caption | 「次数」 | `FIRE_COUNT_HEADER` / `FIRE_COUNT_MEANING` |
| `components/PlatformAlertsPanel.tsx` 列头 / 悬停 / caption | 「次数」 | 同上 |
| `lib/overview.ts` 平台概览副行 | `命中 N 次` | `describeFireCount().combined` |

新增 `ALERT_EVALUATE_INTERVAL_SECONDS = 60`，被 labels.reconcile 从 Go 源码抽出来
逐值对账；并断言 `TouchAlert` 里确有 `fire_count = fire_count + 1`——后端哪天把它
改成真正的发生次数，那一条会红，提醒把文案改回「触发 N 次」。

「已持续」改走 `alertAgeAnchor()`：`first_opened_at` 在就用它，不在就退回
`opened_at` 并挂上 `ALERT_AGE_RESET_HINT`（恢复后重新触发走 `InsertAlert` 新开一行，
`opened_at` 每次归零，所以抖动型告警的「已持续」系统性偏小）。

### 6. 纳入中文对照门禁（`labels.reconcile.test.ts`）

- `ENUM_INVENTORY["ops.State"]` 从 `labelled-elsewhere` 升成 **`reconciled`**，
  并补上真正的差集断言（五个状态都有中文 + 前端优先级清单不多不少）。
- 新增「告警计数」一组，把「评估 N 轮」这句新文案钉在两处后端事实上。
- 新增两条**跨文件扫描**（第二轮重写过，见文末「评审回合二」）：
  - **措辞**：`web/apps/admin-web/src` 与 `web/packages/ui-admin/src` 下**全部**
    非测试 `.ts/.tsx`（递归**走**出来，不是一张名单）剥掉注释后，不得出现
    「次数」单独成词 / 「命中次数」 / 「重复命中」 / 「命中 N 次」。
  - **取值**：`fire_count` 只许经 `describeFireCount` 一处出场，别处不得直接把它
    拼进文案（`` `触发 ${alert.fire_count} 次` `` 这种回退里既没有「次数」也没有
    「命中」，措辞那条挡不住它）。

**第 6 项只能部分兑现，这一点必须说实话：** `labels.reconcile` 是「后端枚举 ↔
前端中文表」的对账，它在结构上对不了自由文案（开票行说明、partial 解释、
「评估 N 轮」那句话本身）。本仓没有自由文案门禁。能真正进这道门禁的只有
`ops.State`（取值 + 顺序）、评估周期那个数、以及上面那条 grep。

## 变异验证表

每一条都是：改实现 → 跑测试 → 记录变红的用例 → 还原 → 复跑确认全绿。

| # | 变异 | 变红的用例 | 已还原 |
|---|---|---|---|
| M1 | `FRESHNESS_PRIORITY` 里 failed / uninitialized 对调 | workbench 5 条（取最差 4 条 + 兜底档锚点）；labels.reconcile 3 条（顺序对账、逐值钉死、合成源码变异） | ✅ |
| M2 | `worstFreshness` 兜底改回 `?? 0` | 「未知状态排在比 failed 还差的位置」 | ✅ |
| M3 | 开票行改回六个写死的字面量 | B/C/D 四条（登记状态、未登记、指标与告警归属、最近观测）；**A 组「今天仍是未接入」仍绿**——这正是「只靠 A 组会假绿」的证明 | ✅ |
| M4 | 合并改成按 `queue` 分组 | 合并那一组 5 条 | ✅ |
| M5 | 明细改成始终展开（去掉条件渲染） | DOM「明细默认收起」+「合并成一行不再占满整格」 | ✅ |
| M6 | 算出 `freshnessNote` 但 `MatrixTableRow` 不渲染 | **DOM 1 条红、纯函数 90 条全绿**——证明 DOM 测试在干活（规则存在 ≠ 调用得到） | ✅ |
| M7 | `due` 改回 `` `触发 ${fire_count} 次` `` | 评估轮数那一组 4 条 | ✅ |
| M8 | 读 `trigger_count` 不判 null | 「在场但为 null：不显示『触发 null 次』」 | ✅ |
| M9 | 「已持续」始终用 `opened_at` | 「在场：改用 first_opened_at」（A/C 两态仍绿——三份夹具的时刻是刻意错开的） | ✅ |
| M10 | `describeWhy` 恒返回 `{}` | 纯函数 4 条 + DOM 1 条 | ✅ |
| M11 | 截断时不加 `+` | DOM「既带 + 也说清只取了 20 条」+ 纯函数 1 条 | ✅ |
| M12 | `FRESHNESS_PRIORITY` 少一档 | labels.reconcile 3 条（含「后端多一个状态会被算成缺失」那条合成源码变异） | ✅ |
| M13 | `PlatformAlertsPanel` 列头改回「次数」 | 措辞扫描那条（报出文件与行号） | ✅ |
| M14 | 合并行丢掉 `children` | 纯函数 5 条 + DOM 展开那条（丢了 children 连排序权重也没了，比第一轮多红 3 条） | ✅ |
| M15 | 开票行去掉 `scopeNote` | 纯函数 2 条 + DOM 1 条 | ✅ |

**第二轮新增（针对评审的三条阻塞级问题）：**

| # | 变异 | 变红的用例 | 已还原 |
|---|---|---|---|
| M16 | `AlertsPage` 的 caption 从 `${FIRE_COUNT_HEADER}与投递结果` 改回「次数与投递结果」（评审复现过的那条：**在旧名单内**、模板串形式、真的渲染出去的表格说明） | 措辞扫描：`pages/AlertsPage.tsx:344 「次数」单独出现`。**旧门禁对此全绿**（正则只认双引号包的 `"次数"`） | ✅ |
| M17 | 往 `pages/OverviewPage.tsx` 种 `title="次数"` + 「命中 3 次」（评审复现过的 M-C，**旧名单外**） | 措辞扫描 2 处。旧门禁全绿 | ✅ |
| M18 | 往 `components/AlertNotifyDeliveries.tsx` 种 `{"次数"}` + 「重复命中」（评审复现过的第三条，**旧名单外**） | 措辞扫描 2 处。旧门禁全绿 | ✅ |
| M19 | `workbench.ts` 的 `due` 改回 `` `触发 ${alert.fire_count} 次` `` | **取值**扫描 1 条 + 评估轮数那组 4 条。措辞扫描对它无能为力（那句话里没有「次数」也没有「命中」）——这正是要两条扫描的原因 | ✅ |
| M20 | 把文件收集的后缀判据从 `.tsx?` 改成 `.jsx?`，让扫描范围**抽空** | 「扫描范围是走出来的」（`0 >= 180` 失败）+「剥注释真的生效」+「豁免清单每一条今天都还需要」共 3 条。**而「扫出来一条都没有」恒真地通过了**——这正是数量下界必须单独存在的证明 | ✅ |
| M21 | 把 `AlertsPage.tsx` 加进措辞豁免清单（同时保留 M16 的副本） | 「豁免清单只减不增」+ 判据反向验证里 `AlertsPage` 那条（豁免连自己的自检也一起关掉了，两道都响） | ✅ |
| M21b | 让 `api/alerts.ts` 不再需要「取值」豁免（把 `` `评估 ${alert.fire_count} 轮` `` 改成先取局部变量），但豁免仍留在清单里 | 「每一条今天都还需要」——这是「豁免清单只减不增」三条规矩里最容易写成恒真的第三条 | ✅ |
| M5b | 排序里去掉稳定并列判据 `\|\| a.index - b.index` | 「条数相同时保持取数顺序」 | ✅ |

测试内部另有三条**合成源码**变异（不改实现、只换输入，写在
`labels.reconcile.test.ts` 里长期运行）：对调 Go 函数体的两个分支、给 Go 常量块加
第六个状态、把 `f.State =` 改名让抽取器抓空。第二轮又加了两组同类的**合成文件**
变异（长期运行，不是一次性的手工验证）：五份「把旧话种回去」的副本各自必须被扫
出来，以及四条**合法用法**（最大重试次数 / 缓存命中 / 规则命中 / 调用次数）必须
**不**被误判——只有「该红的红」而没有「不该红的不红」，下一个人迟早会把门禁调松。

## 依赖的后端字段与回退

**今天一个都不存在。** 每一处读取都写成「字段不在就退回」，并且**缺席 / 在场 /
在场但为 null 三态都有测试**——只测缺席那一支，字段到位那天才会发现在场分支从来
没被走过。

| 字段 / 端点 | 来自 | 今天有 | 回退 |
|---|---|---|---|
| `AlertItem.trigger_count` | XM-OPS-TRUTH | ✗ | 只显示「评估 N 轮」。**不要 `?? 0`**——0 会被读成「一次都没触发」。字段到位后显示「触发 M 次 · 评估 N 轮」（两个数是不同的事实，不是替换关系）。已落在 `api/alerts.ts` 的类型上 |
| `AlertItem.first_opened_at` | XM-OPS-TRUTH | ✗ | 退回 `opened_at` 并挂 `ALERT_AGE_RESET_HINT`。字段到位后 `alertAgeAnchor` 自动改用它，不返回 hint |
| 按 `job_kind` 聚合的失败作业摘要端点 | XM-OPS-TRUTH | ✗ | 对现有 `GET /api/v1/jobs/runs?state=discarded&limit=20` 的 20 条在前端按 kind 合并，`next_before != null` 时计数写成 `×20+`，说明段明示只统计最新 20 条。**接端点时要改的只有 `OverviewPage` 那一条 useQuery + 说明文案那一句**，`workItemsFromJobRuns` 的签名已经预留 `options.truncated`。注意 `GET /api/v1/jobs/overview` 的 `queue_backlog[].discarded` **不能**当替代：它是按队列不是按 kind |
| `freshnessBody.partial_reason`（机器码）+ 中文 label | 无人认领 | ✗ | 显示通用中文解释 + 从数据里点名哪几条 `is_partial`，`watermark` 只作为悬停证据**展示不解析**。字段落地后应同时纳入 labels.reconcile |
| `freshnessBody.not_applicable` / `uninitialized_reason` | 无人认领 | ✗ | **不说「不适用」。** 改用今天就能做到的近似：有没有指标行。这**不等于**「结构上不适用」——一条刚上线还没采到值的新指标长得一模一样，措辞必须止步于事实 |
| 卡片同步的部分成功 / 暂停状态 | XM-CARD-VISIBILITY | ✗ | 本片**不预留字段名**。合并行只陈述作业事实（`card_sync 已放弃 ×N`），**不断言卡片数据是否已刷新**——报告说批量失败后会退回逐张单查且成功，但那是缓解事实，不是这一行能负责的东西。等对方定了字段名再补一句 |
| `JobRunItem.kind/queue/finalized_at/…`、`MetricItem.watermark`、`freshness.is_partial`、`ServiceItem.service_type === "invoice"`、`invoice.*` 指标键 | 已有 | ✓ | 无需回退 |

## 门禁

| 命令 | 结果 |
|---|---|
| `pnpm --filter admin-web run typecheck` | ✅（第二轮复跑） |
| `pnpm --filter admin-web run test` | ✅ 143 文件 / **2183** 用例（第二轮复跑） |
| `pnpm --filter ui-admin run test` | ✅ 17 文件 / 262 用例（第一轮；第二轮未动该包的任何源码，只是把它纳入了措辞扫描的**范围**） |
| `pnpm --filter ui-storybook run build` | ✅（第一轮；第二轮只改了两个 `.test.ts`，没有组件变更） |

（全部带 `--config.verify-deps-before-run=false`：本 worktree 的 `node_modules` 是
指向 `wt-XM-I18N` 的 junction。）

**被本片有意改掉的既有断言**（合并时不要当成回归退回）：

- `lib/workbench.test.ts` 「触发 7 次」→「评估 7 轮」
- `lib/overview.test.ts` 「命中 37 次」→「评估 37 轮」
- `router.test.tsx` 告警列表那条：`getByText("6")` → `getByText("评估 6 轮")`
  （裸数字既读不出口径，也会跟表里别的 6 撞上）
- `router.test.tsx` 「取满 20 条但没有下一页」：等的从逐条那句改成合并行标题

## risks

1. **改了排序，界面今天可能一点变化都没有。** 见开头。验收要看测试，不要看截图。
2. **顺序门禁抓的是 Go 源码文本，不是契约。** 判据依赖「`Freshness()` 写成最严重
   优先的早返回 / switch」这个**结构习惯**。后端改成表驱动、或换 helper 赋值，
   `freshnessPriorityFromBody` 会抓空——被信任的过期闸比没有闸更坏。防线是那条
   数量下界断言（`length >= 5`），已做抓空变异验证。真正的解法见 follow_up 1。
3. **「不适用」这个词今天说不出口。** 把「有没有指标行」当成「结构上不适用」的
   判据只是一个更好的近似。措辞已止步于事实，但读的人仍可能过度解读。
4. **开票行改成计算之后，可能比写死更会骗人。** 后端 registry 认得
   `service_type = "invoice"`，生产若真登记过一行，矩阵会从「未接入」跳成
   「运行中」——而那说的是**登记簿里有一行**，不是只读数据通道通了。`scopeNote`
   担着这个区分，并有一条专门的测试断言「有实例时这句话仍在」。
5. **合并行的 `×N` 与截断提示是同一个事实的两种粒度。** 措辞已分工，但它们在同一
   屏上，将来改文案时要一起看。
6. **措辞扫描的判据是一组正则启发式，不是语义。** 它认的是「『次数』前面不是
   汉字」这个指纹——今天仓库里合法的用法一律带前缀（最大重试次数 / 登录失败次数 /
   调用次数 / 往返次数），所以指纹成立。将来若真有一处合法地把「次数」放在句首或
   引号后，这条会误报；正确的处置是**改那句文案**或往豁免清单里加一行并说清理由，
   **不是**把正则调松。`FIRE_COUNT_MISNOMERS` 与豁免清单都在 `labels.reconcile.test.ts`
   里，旁边就有「合法用法不会被误判」那条对照断言，改动时两边一起看。
7. **「取值」豁免钉在一种写法上。** `RAW_FIRE_COUNT_EXEMPTIONS` 里的
   `api/alerts.ts` 有一条「今天还需要」的断言：它要求那一处**仍然**写成
   `` `评估 ${alert.fire_count} 轮` ``。若有人无害地重构成先取局部变量，那条会红。
   这是有意的（见 M21b）——处置是把豁免删掉，那正是这张清单唯一允许的方向。
8. 所有关于生产现状的数字（288、669、2922）都转引自
   `PLATFORM-ALERT-STORM-2026-09-08.md`，不是本次观测。

## follow_ups

1. **（给 XM-OPS-TRUTH / ops 包）导出有序的优先级清单**，例如
   `var StatesByPriority = []State{StateFailed, StateUninitialized, StateStale,
   StatePartial, StateFresh}`，并让 `Freshness()` 自己的判定与前端门禁都读它。
   届时 labels.reconcile 那一组改读契约，删掉源码抓取，risks 2 随之消失。
2. **（给 XM-OPS-TRUTH）`trigger_count` / `first_opened_at` / 按 job_kind 聚合的
   摘要端点**——三处的前端落点与回退都已就位，字段名以对方为准。建议的摘要形状：
   `items[{ kind, discarded_count, first_discarded_at, last_discarded_at,
   window_hours }]`。
3. **`partial_reason` 与 `uninitialized_reason` 今天无人认领。** 没有它们，前端
   永远说不出「订阅制上游结构上就没有钱包余额」与「这条刚上线还没采到」的区别。
   要真正解决 handoff 里 Sub2API 与 NewAPI 那两行，这个字段是必需的。
4. **本仓没有自由文案门禁。** 本片新增的三处解释性文案（开票行范围、partial 通用
   解释、「已持续」会重新计时的说明）只有单元测试在整句相等地钉着，没有像枚举
   那样的发现式对账。如果这类文案继续增加，值得考虑一层轻量的文案清点。
5. **`fire_count` 那句错话的源头在后端注释**（`internal/platform/alerts/alert.go`
   的「被去重合并掉的命中次数」）。本片只改了前端五处副本；后端那一句仍在，
   下一个读它的人还会长出第六份。
6. **发现式扫描这个写法值得抽出来共用。** 本片的 `uiSourceFiles()` 是仓库里第一个
   「递归走遍 web 源码 + 剥注释 + 一组模式 + 只减不增的豁免清单 + 判据反向验证」
   的骨架。下一个要盯全站文案的门禁（见 follow_up 4）不该再抄一份走目录的代码，
   否则「闸的范围要发现不要手列」这条教训会在门禁自己身上再犯一次。

## 评审回合二：三条阻塞级问题怎么处理的

评审给了三条 major（第 1、3 条是同一个洞的两份独立复现），都指向同一件事：
**那条跨文件 grep 在两个维度上都是空的。** 记在这里，因为它比修好本身更有价值。

### （1）+（3）措辞门禁的范围是手列的，正则是认引号的

- **原文**：`suspects = [AlertsPage, PlatformAlertsPanel, overview.ts, workbench.ts]`
  四个文件常量；正则 `/命中\s*\$\{|命中 \d+ 次|"次数"/` 里「次数」那一支**只匹配
  双引号字面量**。
- **评审复现**：把副本种进名单外的 `pages/OverviewPage.tsx` 与
  `components/AlertNotifyDeliveries.tsx` → 全绿；把 `AlertsPage` 的 caption
  （**名单内**、模板串、真的渲染出去）改回「次数与投递结果」→ **全量 2170 用例
  全绿**。测试名承诺的是「界面上不再有」，它实际保护的是「四个文件里、且写成双
  引号的那一种写法」。
- **怎么修的**（两件事必须一起做，只做一件仍然恒真）：
  - **范围改成走出来的**：`uiSourceFiles()` 递归列完
    `web/apps/admin-web/src` 与 `web/packages/ui-admin/src` 下全部非测试
    `.ts/.tsx`（实测 212 + 32 = 244 个文件），配一条数量下界
    `>= 180`、五个已知消费方的正向锚点、以及**评审种副本的那两个文件必须在范围里**
    的逐条断言。
  - **正则去掉引号依赖**：改成四条模式（「次数」单独成词 / 「命中次数」 /
    「重复命中」 / 「命中 N 次」），并配一条**合法用法不被误判**的对照断言。
  - **另立一条「取值」扫描**：措辞门禁挡不住 `` `触发 ${alert.fire_count} 次` ``
    这条最可能的回退（M19）。
  - **豁免清单三条规矩**：逐字钉死（措辞那张今天是**空的**）、路径必须还在、
    **并且去掉豁免后仍会被扫出来**——第三条最容易写成恒真，M21b 专门验它。
- **仍然是启发式**，见 risks 6/7。

### （2）合并行「按条数降序」没有任何测试真正钉住

- **原文**：夹具 `mixed = [...many, oldest, run({id:7, kind:"sub2api_sync"})]` 的
  插入顺序**本来就**把 `card_sync` 放在前面，所以看起来覆盖它的两条断言是靠分组
  的插入顺序恰好成立的。评审把 `workbench.ts` 的整段 `weight/sort` 删掉换成
  `return items;`，93 个用例全绿。
- **为什么要紧**：生产上 `/jobs/runs` 按放弃时刻倒序返回，只要有一条别的 kind 在
  那 288 条之后被放弃，取回的 20 条里它就排第一——排序一旦回归，那个「×N+」的大堆
  会掉到第二行，而这正是本片要修的「要紧的东西被挤出首屏」本身。
- **怎么修的**：夹具的插入顺序改成与期望**相反**（别的 kind 在最前），并另加两条
  专门用例——「条数多的排最前，与取数顺序无关」（两个方向各跑一遍）与「条数相同时
  保持取数顺序」（钉住 `|| a.index - b.index`）。删掉排序现在红 5 条（M5，见变异表），
  去掉稳定并列判据红 1 条（M5b）。
