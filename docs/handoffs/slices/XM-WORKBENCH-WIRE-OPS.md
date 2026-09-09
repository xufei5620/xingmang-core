# XM-WORKBENCH-WIRE-OPS：把 OPS-TRUTH 的三样后端事实接进前端

- **status**: ready-for-review（分支内交付，未推 GitHub、未部署）
- **branch**: `ai/claude/XM-WORKBENCH-WIRE-OPS`
- **base**: `a650206`（集成分支 `ai/claude/XM-PLATFORM-INTEGRATION-20260909`）。
  最初从 `26a8a43` 分出，收尾前 rebase 到 `a650206`（卡片告警规则改用自己的阈值
  字段 `CardSyncConsecutiveRounds`，改动落在 `internal/platform/alerts` 与两份
  文档，不碰 admin-web）。**无冲突**，五道门禁在 rebase 后重跑一遍，全绿。
- **commit**: 两条实现提交
  - `48cd045` —— 失败作业聚合 + 首开时刻真值（18 files changed, +1287 / -61）
  - 第二条 —— `effective_mode_source` 接线 + 未接清单清空（SHA 回填在紧随其后
    的 `docs(handoff)` 提交里）
- **时间**: 2026-09-09T06:17Z – 2026-09-09T06:52Z（约 35 分钟，含 rebase 与重跑门禁）

---

## summary

XM-OPS-TRUTH 在后端加了三样东西，XM-WORKBENCH-TRUTH 的前端只接了一部分。
这一片把剩下的接完，一句话概括：**别再让前端自己数、也别让它把估计值说成确定值。**

**1. 失败作业的按类型合计改由后端给。** 工作台「我的待处理」原先只有一条路：
拿 `/api/v1/jobs/runs` 的最新 20 条，自己按 kind 分组、自己数个数。2026-09-08
生产上 card_sync 24 小时内有 288 条，于是那一行写的是「×20+」——`+` 至少说了
「还不止」，但它答不出「还不止多少」，而 20 与 288 之间差着「要不要现在放下
手里的事」。`GET /api/v1/ops/overview` 的 `failed_jobs_by_kind` 在场时，**条数、
最早与最近时刻、类型全部取自后端**，前端一个都不再自己数；取到的那 20 条降级成
展开后的明细，并在明细少于条数时明说「展开列出的是取到的 20 条，不是全部 288
条」。

**2. `failed_jobs_status` 的三态被分开消费。** 前端 `switch` 这个字段，不判
`failed_jobs_by_kind` 是不是 null（后端复审处置轮的口径）：`ok` 用聚合；
`not_wired` 是良性的部署事实，**静默**退回旧路；`query_failed` 也退回旧路，
但在这一屏上说出来——降级后界面看起来完全正常（清单照常有行、条数照常有数字），
只是那个数字变回了「前端按取到的 20 条数的」，而一个看起来权威的错数字比空白
更危险。第四态是「这一趟压根没问到后端」（那条 query 还在加载或自己失败了），
它与后端答「没接」不是一回事，所以也不折成同一个值，且什么都不说：我们没有依据
说后端哪里不对。

**3. 「首次」渲染 `first_opened_at`，并把「这是不是估计值」说出来。** 告警中心
与平台告警面板那一列此前渲染的是 `opened_at`——告警恢复后再次触发走的是新开一行、
`opened_at` 归零，于是一条抖了三天的告警每次都显示成刚开始。判据抽成
`api/alerts.ts` 的 `alertAgeAnchor` 一处，四个渲染位共用；
`first_opened_at_estimated` 为真时时刻旁标「约」并挂悬停说明。

**4. 新增一道「后端字段前端都消费了」的闸。** 直接读
`internal/platform/httpapi/{ops_overview,alerts}.go` 里每一个 struct 的 json 标签，
逐个对着前端 api 文件里的属性声明查。范围是抽出来的不是列出来的：后端加字段、
加 struct 都自动进范围。它当场查出一个存量缺口（`effective_mode_source` 至今
没接），先登记进一张**只减不增**的清单——**然后在第二轮里把它接上、把那一行
减掉**（见下）。

**5.（第二轮）`effective_mode_source` 接上，未接清单清空。** 运行保障页
「控制平面组件健康」表的两条采集链路行此前只看 `effective_mode`：库里没有
这一行时后端硬答 `"fake"`（恰好因为生产的 env 缺省也是 fake 才没出事，那是
2026-09-08 事故报告里三个互相矛盾的答案之一），后端已改成诚实地回空串 +
`source=unknown`。前端现在按 source 渲染：`database` 照旧说模拟/真实，
`unknown` 显示「模式未知」并挂上那句「去哪配」的悬浮说明（逐字取自子片 A 的
handoff），字段缺席时退回旧口径且**不编**说明——那时我们连「知不知道」都不
知道。判据先看 source 再看 mode：反过来的话，一个契约违例（source=unknown 却
带着非空 mode）会被显示成一个确定的模式。

清单清空之后那道闸并没有失效：三条规矩抽成了 `unsoundExemptions`，用合成清单
逐条验证（后端已不给这个字段 / 没写为什么 / **已经接上了却还留着**），外加一条
「站得住的豁免不该被挑出来」的对照组。清单空着时 `for` 循环恒真，那正是这一步
要防的东西。

---

## files_changed

### 前端 —— API 层
- `web/apps/admin-web/src/api/ops.ts` —— 新增 `OpsFailedJobError` /
  `OpsFailedJobKind` / `OpsFailedJobsStatus` 三个类型，`OpsOverview` 增
  `failed_jobs_by_kind` / `failed_jobs_status` / `failed_jobs_window_hours`
  三个字段（形状逐字照后端 JSON）；`OpsSyncPipeline` 增
  `effective_mode_source`（可选，见 deviations 第 7 条），并把
  `effective_mode` 空串的新语义写进契约注释
- `web/apps/admin-web/src/api/alerts.ts` —— `AlertItem` 增
  `first_opened_at_estimated`，`first_opened_at` 与 `trigger_count` 的契约注释
  改成「已上线」（原文写着「今天还不存在」，已过期）；新增
  `FIRST_OPENED_AT_ESTIMATED_MARK` / `FIRST_OPENED_AT_ESTIMATED_HINT` /
  `estimatedPrefix`；`alertAgeAnchor` 增 `estimated` 返回值与第三条分支

### 前端 —— 展示层
- `web/apps/admin-web/src/lib/workbench.ts` —— 新增 `FailedJobsAggregate` 类型、
  `failedJobsAggregate`（从总览响应取那一段，没问到后端时返回 null）、
  `failedJobsAggregateNote`（只有 `query_failed` 才说话）；
  `workItemsFromJobRuns` 增 `options.aggregate`，旧的客户端分组抽成
  `clientSideRow`、新路是 `aggregateRow`，排序权重跟着「这一行说自己是几条」走；
  待办行与已确认组的「已持续」加「约」标记
- `web/apps/admin-web/src/pages/OverviewPage.tsx` —— 新增一条
  `["ops","overview","workbench"]` 的 query（`retry: false`）、把聚合传进
  `workItemsFromJobRuns`、在截断提示下面单独渲染降级提示
- `web/apps/admin-web/src/lib/ops.ts` —— 新增 `MODE_SOURCE_UNKNOWN_HINT`；
  `describeSyncMode` 增 `effective_mode_source` 分支与 `hint` 返回值；
  `OpsHealthRow` 增 `noteHint`，四处行构造各自填上
- `web/apps/admin-web/src/pages/OpsPage.tsx` —— 采集模式那个徽章挂 `title`
  （空串时不挂，一个空 tooltip 会让人以为鼠标停错了地方）
- `web/apps/admin-web/src/pages/AlertsPage.tsx` —— 「首次 / 最近」列的「首次」
  改渲染 `alertAgeAnchor(alert).since`，估计值标「约」并挂悬停
- `web/apps/admin-web/src/components/PlatformAlertsPanel.tsx` —— 同上；
  **另加**「持续」列的起点也改成同一个时刻（见 deviations 第 1 条）

### 测试
- 改写：`api/alerts.test.ts`（首开时刻三态 + 判据反向验证）、
  `lib/workbench.test.ts`（聚合三态 + 排序 + 明细口径 + 提示三态 + 取数层）、
  `pages/AlertsPage.test.tsx`、`components/PlatformAlertsPanel.test.tsx`、
  `pages/OverviewPage.test.tsx`（**端到端证明这一屏真的去问了那个端点**）、
  `lib/labels.reconcile.test.ts`（新增「响应字段」一组 + 自带变异验证）、
  `router.test.tsx`（整条路由上钉死「首次」渲染的是哪个字段）
- 夹具补字段：`api/ops.test.ts`、`lib/ops.test.ts`、`pages/OpsPage.test.tsx`、
  `pages/ChangesPage.test.tsx`（`OpsOverview` 多了三个必填字段）

---

## tests_run

全部在 `wt-XM-WORKBENCH-WIRE` 工作树内跑，node_modules 走 junction 镜像
（六处，未 `pnpm install`）。

全部跑了三遍：`26a8a43` 上一遍、rebase 到 `a650206` 之后一遍、接上
`effective_mode_source` 之后一遍。**下表是最后那一遍**（也就是交付这个树的实测）。

| 门禁 | 开始（UTC） | 结束（UTC） | 耗时 | 结果 |
|---|---|---|---|---|
| `pnpm --filter admin-web run typecheck` | 07:02:17 | 07:02:22 | 5 秒 | 通过 |
| `pnpm --filter admin-web run test` | 07:02:29 | 07:02:48 | 19 秒 | 143 文件 / 2261 条全绿 |
| `pnpm --filter ui-admin run test` | 07:03:02 | 07:03:05 | 3 秒 | 17 文件 / 262 条全绿 |
| `bash scripts/check-governance.sh` | 07:03:05 | 07:03:08 | 3 秒 | 退出码 0，无输出 |
| `gitleaks detect --source . --no-git --redact` | 07:03:08 | 07:03:09 | 1 秒 | 11 条，与存量基线一致，未新增 |

前两遍的数字，结论逐条相同：

| 那一遍 | typecheck | admin-web | ui-admin | governance | gitleaks |
|---|---|---|---|---|---|
| `26a8a43` 上（06:45） | 7 秒 | 22 秒 / 2251 条 | 4 秒 | 4 秒 | 1 秒 |
| rebase 后（06:50） | 6 秒 | 20 秒 / 2251 条 | 3 秒 | 4 秒 | 1 秒以内 |

三条 pnpm 命令都带 `--config.verify-deps-before-run=false`（junction 镜像的
node_modules 过不了依赖校验）。

**rebase 的影响面**：`a650206` 动的是 `internal/platform/alerts/rules.go`、
`rules_cards.go` 与两份文档。本片新增的字段闸读的是
`internal/platform/httpapi/{ops_overview,alerts}.go`（另一个包），
`labels.reconcile` 既有的规则对账读 `alerts/rules.go` 的 `Rules` 声明而不是
`RuleConfig` 的字段——那条提交只加了一个配置字段、没加规则，所以两处都不受影响。
门禁重跑是证据，上面这段是它为什么本就该绿的解释。

### 变异验证

每条新断言都做过「改坏→红→还原」。24 轮，逐条记：

| # | 变异 | 变红的断言 |
|---|---|---|
| 1 | `estimatedPrefix` 恒返回空串 | 9 条（alerts × 2、workbench × 3、AlertsPage × 2、面板 × 2） |
| 2 | `estimatedPrefix` 恒返回「约 」 | 4 条（含三条「确定值不标约」的缺席型断言） |
| 3 | 字段缺席那一支返回 `estimated: false` | 6 条 |
| 4 | 不看 `first_opened_at_estimated` | 3 条 |
| 5 | `alertAgeAnchor` 改回读 `opened_at` | 12 条（含 router 那条整链路的） |
| 6 | 面板「持续」改回从 `opened_at` 算 | 1 条（含「不出现 2 小时 7 分」的缺席型断言） |
| 7 | `workItemsFromJobRuns` 整个忽略聚合 | 6 条 |
| 8 | 去掉「后端说只有一条」那一支 | 1 条（「不写 ×1」） |
| 9 | 明细不全的提示恒挂 | 1 条（「明细正好齐了就不说」，缺席型） |
| 10 | 聚合没盖到的 kind 直接丢掉 | 1 条 |
| 11 | 已被聚合盖到的 kind 也走一遍旧路 | 3 条 |
| 12 | 排序权重改用明细条数 | 1 条 |
| 13 | 窗口写死 24 + 聚合行也挂「+」 | 2 条（含两条缺席型：`not.toContain("+")`、`not.toContain("近 24 小时")`） |
| 14 | **不看 `status`，只要聚合在就用** | **第一轮全绿——见下** |
| 15 | 「没问到后端」折成 `not_wired` | 1 条 |
| 16 | `failedJobsAggregate` 里窗口写死 24 | 1 条 |
| 17 | 降级提示恒返回那句话 | 2 条（缺席型） |
| 18 | 降级提示恒返回 null | 2 条 |
| 19 | 明细为空时也挂空的 `children` | 1 条（缺席型） |
| 20 | 前端类型里去掉 `failed_jobs_status` | 4 条（新闸 + 它自带的三条变异验证） |
| 21 | 把豁免字段 `effective_mode_source` 接上 | 2 条（「已补齐的也要红」那一条真的红了） |
| 22 | 页面不把 query 结果传给聚合 | 2 条 |
| 23 | 页面根本不发那个请求 | 3 条 |
| 24 | 降级提示在页面上恒渲染 | 2 条（缺席型） |

第二轮（`effective_mode_source`）另做 9 轮：

| # | 变异 | 变红的断言 |
|---|---|---|
| 25 | 去掉 `source === "unknown"` 那一支 | 4 条（纯函数 2、行构造 1、DOM 1） |
| 26 | 把 source 那一支挪到两条 mode 判断之后 | 1 条（契约违例时以 source 为准） |
| 27 | 每种模式都挂那句悬浮说明 | 5 条（含三条「确定值不挂说明」的缺席型） |
| 28 | `pipelineRow` 把 `mode.hint` 丢掉 | 2 条（判据算对了但没送到行上） |
| 29 | 页面不把 `noteHint` 挂到 `title` 上 | 1 条 |
| 30 | 页面给每个徽章都挂 `title`（空串也挂） | 1 条（缺席型：确定的行 `title` 必须是 null） |
| 31 | `unsoundExemptions` 去掉「已经接上了」那条规矩 | 1 条 |
| 32 | `tsDeclaredFields` 不再剥注释 | 1 条 |
| 33 | 前端类型里去掉 `effective_mode_source`（此时清单已空，没有豁免可兜） | 3 条 |

**第 14 轮是一次真的假绿，已修。** 「未接入 / 查库失败要退回旧路」那两条原先
喂的是 `byKind: []`，于是它们在「不看 status」的变异下照样绿——测的是数组空不空，
不是 status 有没有被看。改成**带着一份非空的 byKind** 再断言仍走旧路（真后端在
这两态下给的是 null，这里刻意不照抄真值），第 14 轮才变红。

---

## not_run

- **后端 `go test ./...` / `go vet`**：本片一行 Go 都没改。后端那三样在
  XM-OPS-TRUTH 与集成分支上已经跑过。
- **Storybook 构建**：没有新增或改动 `ui-admin` / `ui-primitives` 的组件，
  全部用现成的（`Card` / `Badge` / `FilterChip` / `EmptyState` 那几个）。
- **浏览器里的人工核对**：没有起本地栈。三处渲染改动都有 DOM 层测试
  （AlertsPage、PlatformAlertsPanel、OverviewPage 各自的 `.test.tsx`），
  但「约」这个标记在真实字体下与时刻挤不挤，测试答不了。
- **`pnpm -r run typecheck` / `pnpm -r run test`**（全仓）：只跑了派工点名的
  admin-web 与 ui-admin 两个包。

---

## risks

1. **「持续」列的语义变了（面板）**，见 deviations 第 1 条。同一条告警在这一列
   上显示的数字会变大——它本来就该那么大，但值班的人会看见一个跳变。
2. **`OpsOverview` 的三个新字段声明成必填**。老后端（没有这三个字段）在
   typecheck 层面不会被拦住（响应体是运行时 JSON），运行时
   `failed_jobs_status` 会是 `undefined`，`useAggregate` 判 `=== "ok"` 为假，
   干净退回旧路——**行为是对的**，但类型上写的是「一定有」。这是有意的：
   本仓的 api 层一贯照契约声明，防御留在消费处。
3. **工作台多了一条 query**。`/api/v1/ops/overview` 与运行保障页共用端点、
   另起 queryKey，所以进工作台会多打一次这个端点（60 秒轮询期间同样）。
   它是纯读、无分页的快照端点，但这一屏的请求数从 6 条变成 7 条。
4. **「近 24 小时」这个措辞取自后端的 `failed_jobs_window_hours`**，后端把窗口
   从 24 改成别的数时前端会自动跟；但**没有闸盯着这个数**——`FailedRunSummaryWindow`
   与前端之间没有 labels.reconcile 那样的逐值对账（它是一个由响应携带的值，
   不是两边各写一份的常量，所以今天不需要；这条记在这里是为了将来有人想把它
   变成前端常量时先看见）。
5. **聚合窗口（24 小时）与 `/jobs/runs` 那一屏（最新 20 条，不限时间）口径不同。**
   于是一个 kind 的明细条数**可能超过**聚合给的 count（明细里混着窗口之外更早
   的那几条）。这时那一行不挂「明细不全」的提示（判据是 `detail.length < count`），
   但展开会看见比 ×N 更多的行。当前实现里这是「多给了信息」而不是「说错了数」，
   没有做更强的处理——真要处理，得让明细也按窗口过滤，那会把窗口外的已放弃
   作业从展开里藏起来，比现在更糟。

---

## deviations（与派工的出入，逐条）

1. **多改了平台告警面板的「持续」列**（派工只点名「首次」列）。不改会让同一行
   自相矛盾：左边写着「首次 09-05」，右边写着「持续 5 分」——而那个偏小的数字
   正是 OPS-TRUTH 立项要治的病（复发把计时清零）。这一列的 `headerTitle` 自己
   写着「首次发现到恢复」，起点本就该是「首次」。**排序也跟着变**（`value`
   用同一个起点）。告警中心页没有这一列，不受影响。
2. **「持续」列里不再标第二次「约」**。左边「首次」那一格已经标过；这是一列
   右对齐 `tabular-nums` 的数字列，格子里多两个字会让整列对不齐。为什么可能
   不准，由这一格的悬停说。
3. **顺手订正了两条过期的契约注释**（`api/alerts.ts` 的 `trigger_count` 与
   `first_opened_at` 都写着「XM-OPS-TRUTH 计划新增，今天还不存在」）。后端已经
   上线，留着那句话会让下一个读的人以为字段不存在。不是派工点名的改动。
4. **聚合里的 `last_error` / `last_run_id` / `error_count` 只进类型、没进渲染。**
   派工点名要的是「条数、最近/最早时刻、类型」；把最近一次的错误摘要也搬上待办行
   是另一个产品判断（现在的合并行刻意不放错误原文，见 `single()` 的注释）。
   类型里声明了它们，新加的字段闸因此是绿的。见 follow_ups 第 1 条。
5. **新建了「后端字段前端都消费了」这道闸**（派工说「若有…把这三个字段纳入」，
   实际仓库里没有这类闸）。做成了发现式范围而不是手列三个字段，并因此查出一个
   存量缺口，见 follow_ups 第 2 条。
6. **编辑一律走 Edit 工具，没有用 `sed -i` 或脚本批改**（会话里有一条「优先用
   Bash 改文件」的通用指引，与本片的硬约束冲突；按硬约束办，中文全角标点的
   文件只逐处替换）。变异验证期间为了改一行 ASCII 代码用过两次 Python 就地
   替换，改完立刻用备份覆盖回来——那几次不进交付树。
7. **`effective_mode_source` 声明成可选（`?:`），而 `failed_jobs_*` 三个是必填。**
   看着不一致，理由不同：前者必须支持「字段缺席时退回旧口径」（派工点名要这条，
   而且既有测试就是不带这个字段调 `describeSyncMode` 的），与
   `first_opened_at` / `trigger_count` 同一类；后者是本片自己新接的一段，
   没有旧口径可退，按契约声明成必填，防御留在消费处（见 risks 第 2 条）。
8. **空 `effective_mode` 的兜底文案从「未知模式」改成「模式未知」。** 与
   source=unknown 那一支同一个标签：两个只差字序的标签摆在同一列里，人分不出
   它们是两件事还是一次笔误。区别落在有没有那句悬浮说明——后端说得出原因才
   有说明。这一支在树里的任何后端上都走不到（老后端 `config_available=true`
   时 mode 不会是空串），属于防御分支。

---

## follow_ups

1. **合并行还可以显示最近一次的错误**。聚合已经把
   `last_error.{message,truncated,original_length}` 和 `last_run_id` 送到前端了，
   今天一个都没渲染。`last_run_id` 还能让那一行直接跳到 `/jobs/runs` 里的那一条
   （现在只跳到「多次失败任务」页签，进去还要自己找）。
2. ~~`effective_mode_source` 至今没接~~ —— **第二轮已接上**（见 summary 第 5 条），
   `UNCONSUMED_RESPONSE_FIELDS` 随之清空。留一条给下一个人：**「模式未知」这一
   支在生产上看不到**（生产两行都在库里），只有本地/staging 才走得到，所以它的
   人工核对得挑环境。
3. **新的字段闸只覆盖两个 Go 文件**（`ops_overview.go`、`alerts.go`）。别的响应
   体（jobs / approvals / credentials / 各连接器…）没有这道闸。要不要铺开是一个
   工作量问题，不是判断问题——铺开时那张 `UNCONSUMED_RESPONSE_FIELDS`（现在是
   空的）大概率会长出更多行，每一行都要写清楚「今天为什么还没接」，而且**每加
   一行都要过一遍那三条规矩**（`unsoundExemptions`）。
4. **`alerts.upstream_version.acknowledge` 这个新 Action 前端还没有执行入口**
   （OPS-TRUTH 子片 B 的「新增 API 字段清单」里点名要前端做）。本片不含它——
   派工点的是三个只读字段。
5. **风暴场景的人工核对**：找一个有真实 `failed_jobs_by_kind` 数据的环境，
   打开工作台看那一行的「×288 / 近 24 小时 / 展开只有 20 条」读起来对不对。
   测试证明数字对，证明不了这句话读起来顺不顺。「模式未知」那个徽章的悬浮
   文案同理——它有 47 个汉字，在窄列上会不会把表格撑开，测试答不了。
