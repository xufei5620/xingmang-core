# XM-CHAN-WIRE0：渠道目录扩展字段接线（消费 XM-CHAN-FIELDS0 的真实契约）

## status

READY（待验收线审读、复跑并人工合入）

## branch / commit

同一条分支 `ai/claude/XM-CHAN-MERGE0-channel-upstream-merge`（team-lead 明确
指示不再另开分支：不重开 worktree、不改名、不强推）。这份 handoff 只覆盖
XM-CHAN-WIRE0 自己的范围——分支上 `5131b8f` 之后的七个提交（`5131b8f` 是
XM-CHAN-MERGE0 已合入 `release/v0.1-launch` 的边界，见
`XM-CHAN-MERGE0-channel-upstream-merge.md` 的"分支状态"一节）：

1. `6c150e3` feat(admin-web): code the channel table against chanfields' exact JSON contract
2. `03acb95` feat(admin-web): match the channel detail page to the new field contract
3. `23cb559` docs(admin-ia): record team-lead's precise 07:20/07:25 implementation spec
4. `bf2e30c` docs(handoff): update for the third refinement pass and its browser proof
5. `0896a33` fix(admin-web): correct rate_multiplier/success_rate numeric encoding
6. `fb8f892` docs(handoff): mark branch status blocked pending merge-integration decision
7. `54a4c2d` feat(admin-web): replace stale "pending chanfields" copy with real per-platform reasons

最新提交（这份 handoff 落地时）为 `54a4c2d`；这份文件本身作为下一个提交加入。
提交 3/4/6 是文档提交，属于 XM-CHAN-MERGE0 那份 handoff/ADMIN-IA §8.8 的收尾
记录（记录"分支被中途合并""team-lead 精确规格"这些过程性事实），不是
XM-CHAN-WIRE0 自己的功能改动——放在这里一并列出是为了提交历史完整，不代表
这份 handoff 要为它们的内容负责，详情见各自提交信息。

## summary

XM-CHAN-FIELDS0（chanfields）已经把渠道目录契约扩展交付到
`release/v0.1-launch`（`0502e60`）：`GET /api/v1/platforms/{platform}/channels`
现在下发 `id`/`kind`/`vendor`/`capacity`/`status`/`scheduling`/`today`/
`usage_window`/`proxy`/`rate_multiplier`/`upstream_multiplier`/`last_used_at`/
`created_at`/`expires_at` 十四个新键，全部可空。XM-CHAN-MERGE0 那一轮（提交
1-15）在字段还不存在的时候，已经把这些列的**位置、顺序、渲染逻辑框架**都
搭好了，只是字段读不到、显式标"未接入·等 XM-CHAN-FIELDS0"。这一片
（XM-CHAN-WIRE0）做的事：

1. **把 `PlatformChannelRow` 按 chanfields 实际交付的字段名/类型改到位**——
   开工前没有直接相信 team-lead 转述的字段名，逐字核对了三处独立来源:
   chanfields 的两份契约文档、chanfields 的 handoff、以及最终**直接读
   `internal/platform/httpapi/platform_channels.go` 的 struct json tag**。
   核对中发现两处之前按 team-lead 转述"猜"出来的类型是错的（见下面"两个
   真实 bug"），照 Go struct 改正。
2. **把"未接入"的原因从笼统的"等 XM-CHAN-FIELDS0"换成逐字段、逐平台的真实
   原因**——chanfields 交付之后，"这个字段是 null"不再有统一解释：Sub2API
   一侧多数是"这次没采集到"（today-stats 预算 40 账号/次、账号没开某个
   功能），NewAPI 一侧不少字段是"这个平台压根没有这个概念"（恒为 null，
   `kind`/`capacity`/`usage_window`/`rate_multiplier`/`upstream_multiplier`/
   `last_used_at`/`expires_at` 七个字段）。继续显示"等 XM-CHAN-FIELDS0"会让
   人以为这是暂时性的，NewAPI 那七个字段其实永远不会变。
3. **视图/筛选改用 `kind` 字段**（`row.kind` 优先，取不到才退回既有的
   `accountRowType` 从 access_method 派生）——按 team-lead 规格。
4. **`用量窗口` 补一条领域知识**：Sub2API 的真实值是"费用上限占用率",
   不是 Anthropic 原生的 5 小时用量百分比，两者容易混淆，在有值和无值两种
   状态下的 tooltip 里都点明是哪一个（抄自 chanfields 契约文档的 Follow-ups
   一节）。
5. **真实浏览器验证"字段一旦非 null，前端不用改代码就自动显示真值"这件事
   本身**——不只是单测断言，用编造但符合真实契约形状的 mock 数据分别给
   Sub2API 与 NewAPI 各填一行，逐字段核对渲染结果，过程中在 mock 数据本身
   （不是应用代码）里抓到一个真实的疏漏（见 tests_run）。

### 两个真实 bug（team-lead 已在回复里确认"good catch"）

开工时没有假设 team-lead 转述的 JSON 字段名/类型完全准确，核对了 chanfields
交付的实际契约（两份 `.md` 文档 + `platform_channels.go` 的 struct tag），
发现两处需要修正：

1. **`rate_multiplier`/`upstream_multiplier` 的真实类型是 `*float64`（数字）,
   不是十进制字符串**。之前按登记簿 `group_rate`/`recharge_ratio`（都是
   字符串）的既有惯例类推，猜成了 `string | null`。改成 `number | null`,
   同时把"这一格有没有值"的判断从假值检查（`!rate`）改成显式 null 检查——
   数字倍率理论上可以是 0，`!0` 会把真的 0 误判成"没有这个字段"。
2. **`today.success_rate` 契约文档明确写 Sub2API 端恒为 `null`**——这个
   连接器没有账号级成功/失败计数的数据源，即使 `today.requests`/
   `today.cost_minor` 是另一个真实端点给的、确实有值。之前的实现对
   `success_rate` 缺失时默认成 `0`，会在**每一条 Sub2API 渠道行上永久
   显示一个编造的"0.0%"**——这正是宪法 12 条要挡的"裸 0 冒充真实数据"。
   改成 `successRate: number | null`（在 `today` 对象内部单独可空，不再
   靠"整个 `today` 是否非空"这一个开关判断），null 时显示"成功率未接入"。
   顺带发现并修了数值编码本身：契约的 `success_rate`/`used_ratio` 都是
   0-1 的小数（如 0.42），不是 0-100 的百分数——`used_ratio` 之前歪打正着
   写对了，`success_rate` 没有，显示前要乘 100。

## files_changed

- `web/apps/admin-web/src/api/platformChannels.ts`——`PlatformChannelFieldsExtension`
  新增 14 个字段的类型 + `RawPage` 对应的 snake_case 原始形状 +
  `parseFieldsExtension` 解析函数（关键子键任一缺失就整体按 null 处理,
  不拼半真半假的对象）；`rate_multiplier`/`upstream_multiplier` 从
  `string | null` 改成 `number | null`；`today.successRate` 从"整个 today
  对象内必有"改成"该字段自己也可空"。
- `web/apps/admin-web/src/lib/channelFieldReasons.ts`（新增）——逐字段、
  逐平台的未接入原因（`FIELD_NULL_REASONS`/`channelFieldNullReason`）,
  调度写操作固定 tooltip（`SCHEDULING_WRITE_HINT`），用量窗口的 Sub2API
  专属说明（`USAGE_WINDOW_SUB2API_HINT`）。行列与详情页共用同一份定义,
  不会一边改一边忘了改另一边。
- `web/apps/admin-web/src/components/ManagedChannelTable.tsx` + `.test.tsx`——
  「平台 / 类型」列显示真实供应商名（`row.vendor` 优先）而不是平台徽章;
  8 个原占位列改成实际读 `PlatformChannelFieldsExtension` 的字段,
  非 null 显示真值、null 显示 `channelFieldNullReason` 给出的具体原因;
  「调度」渲染禁用态开关控件（`role="switch"`，ui-primitives 没有现成
  Toggle 组件，这里画一个纯展示用的，够不上单独开一个可复用组件）；
  「倍率 / 上游倍率」「类型」两处保留"新契约字段优先、查不到退回登记簿
  join"的既有模式（这两个字段在 XM-CHAN-MERGE0 那一轮就已经有真实数据,
  不属于本片新增的"8 个占位字段"）；筛选/视图改用 `row.kind` 优先。
- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx` + `SupplyDetailPages.test.tsx`——
  「容量与调度」区块的 8 个字段同步改成读真实字段 + 逐平台原因；「类型」
  「来源上游」「倍率 / 上游倍率」三处的"新字段优先"逻辑同步文档化。
- `docs/architecture/ADMIN-IA.md` / `docs/handoffs/slices/
  XM-CHAN-MERGE0-channel-upstream-merge.md`——过程性记录（分支中途被合并、
  team-lead 的精确规格、分支状态更新为 BLOCKED 再到本片落地解除阻塞),
  不是 XM-CHAN-WIRE0 自己的功能改动，随手一并提交，见上方"commit"节的
  说明。

## tests_run

在 `web/` 目录串行执行（Windows worktree 用镜像脚本补齐 `node_modules`,
命令带 `--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；未并发跑
多个 pnpm 门禁）——以下是提交 `54a4c2d`（本片最终状态）之后的复跑结果：

- `pnpm --config.verify-deps-before-run=false -r run typecheck`—— PASS
  （5 个前端 workspace 包全部 `tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false -r run test`—— PASS：
  design-tokens 10、ui-primitives 16、ui-admin 232、admin-web 1304，
  共 1562 个用例全绿
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`
  —— PASS（"Storybook build completed successfully"；本片没有新增/修改
  ui-admin 包组件，Storybook 不需要新故事）
- `bash scripts/check-governance.sh`—— PASS（exit 0，无输出）
- `gitleaks git --log-opts="release/v0.1-launch..HEAD"`—— PASS（22 commits
  scanned，含 XM-CHAN-MERGE0 那 15 个已合入的提交，"no leaks found"）

新增测试覆盖（都在上面两个 `.test.tsx` 文件里）：真实字段非 null 时行列都
显示真值、不用改代码（`ManagedChannelTable.test.tsx` 的容量/今日统计/用量
窗口/最近使用组合测试，`SupplyDetailPages.test.tsx` 的"字段一旦非 null,
不用改代码就显示真值"）；同一个字段在 Sub2API 与 NewAPI 上给出不同的、
正确的未接入原因；`rate_multiplier` 为数字 `0` 时正确显示 `0×`（不是误判成
未接入）；`success_rate` 单独为 null、但 `requests`/`cost` 有真数据时显示
"成功率未接入"而不是假的"0.0%"。

真实浏览器实测（`node node_modules/vite/bin/vite.js --port 5180 --strictPort`
+ 手写 mock 后端，同一套脚本延续自 XM-CHAN-MERGE0；这一片给 Sub2API 的
`ch-openai-main` 与 NewAPI 的 `n-claude-main` 各填了一套符合真实契约形状的
样例扩展字段值，其余行留空验证未接入路径）：

- Sub2API `ch-openai-main`：供应商名"Relay 甲直连"（覆盖登记簿 join 的
  "Relay 甲"）、容量"12 / 50"、调度开关呈勾选态 + "优先级 1"、今日统计
  "842 次 · 99.2% · ¥45.60"、用量窗口因类型是"上游渠道"显示"不适用"、
  倍率"0.80× / 1.10×"（覆盖登记簿 join 的"0.85× / 1.15×"）、最近使用与
  创建时间显示真实时间戳、过期时间仍未接入——逐格核对与预期一致
  （截图 02）
- NewAPI `n-claude-main`：供应商名"Anthropic"、容量显式"未接入"且 title
  核对确实是"NewAPI 没有渠道级并发上限这个概念"（不是"还没采集到"）、
  调度"已开启 · 优先级 5"、今日统计"312 次 · 97.4% · ¥18.80"、用量窗口
  "不适用"、最近使用与过期时间显式未接入（NewAPI 恒为 null）、创建时间与
  代理显示真值——验证了 NewAPI 的"永久没有"与 Sub2API 的"这次没采集到"
  两条文案路径都对（截图 01、渠道详情页截图 03）
- 过程中在 mock 数据本身（不是应用代码）里发现一处疏漏：`ch-openai-main`
  的 `success_rate` 样例值是修完数值编码 bug **之前**写的（`99.2`，当时
  按 0-100 假设），修完 bug 之后忘了回头改这条 mock 数据，页面因此一度
  显示"9920.0%"——这恰恰证明了乘 100 的渲染逻辑在正确执行（拿错误刻度的
  输入喂给正确的逻辑，输出自然离谱），不是应用代码的问题。已把 mock 数据
  改成 `0.992`，重新验证显示"99.2%"，确认修复。mock 脚本本身未纳入提交
  （纯本地实测工具）

截图（`docs/evidence/screens/XM-CHAN-WIRE0/`）：

- `01-newapi-row-real-fields-and-permanent-nulls.png`——NewAPI 渠道管理表,
  `n-claude-main` 一行的真实字段（供应商/调度/今日统计/创建时间/代理）与
  永久未接入字段（容量/用量窗口显示不适用/最近使用/过期时间）并排可见
- `02-sub2api-row-real-fields.png`——Sub2API 渠道管理表，`ch-openai-main`
  一行的完整真实字段，含"新字段优先覆盖登记簿 join"的供应商名与倍率
- `03-newapi-detail-real-fields-and-permanent-nulls.png`——NewAPI 渠道
  详情页，「容量与调度」区块与行内数据逐字段一致

## not_run

- 后端 `go test ./...` / `go vet ./...`：本片未改动任何 `.go` 文件（只是
  消费 XM-CHAN-FIELDS0 已经交付、已经测试过的契约），未跑；chanfields 的
  handoff 已记录后端侧的完整测试结果（66 个包全绿）。建议验收线按常规仍
  复跑一次作为基线确认。
- 生产环境验证：未跑，也不应该跑——本任务明确要求不部署、不碰服务器。
- 未对真实 Sub2API/NewAPI 实例验证过字段的实际取值分布——与 chanfields 的
  免责声明同一条：只有本地假上游/mock 数据覆盖过；真实凭据到位后需要
  按两份契约文档的 Follow-ups 一节逐项核对（尤其是 `upstream_multiplier`
  "多数账号是 null"、today-stats 预算在真实大规模账号数下的实际覆盖率）。
- 移动端/窄视口截图：只测了 1440×1000 桌面视口，延续 XM-CHAN-MERGE0 未测
  这一项的既有状态。

## risks

- **`capacity` 字段的"关键子键任一缺失就整体按 null"处理**（`used`/`limit`
  必须同时是 number 才认为有值）是本片沿用 XM-CHAN-MERGE0 已经定下的保守
  策略，没有重新评估。chanfields 的 handoff 显示这两个子字段来自
  `AccountWithConcurrency`/`dto.Account` 的同一次响应，实践中大概率同时
  出现或同时缺失，但严格来说 Go 的 `*int64` 允许两者独立为 nil——如果真实
  环境出现"只有 used 没有 limit"这种情况，这一格会显示未接入而不是
  "3 / —"这种部分展示，是有意的取舍（避免拼半真半假的数字），不是漏做。
- **`upstream_multiplier`（Sub2API）绝大多数账号会是 `null`**——只有开启过
  "上游计费探测"功能的账号才有值，这是 chanfields 明确记录的能力覆盖范围
  限制，不是本片或后端的缺陷；真实生产环境大概率看到这一格在多数行上
  未接入，是预期行为。
- **NewAPI `vendor` 依赖 chanfields 抄自上游的静态映射表**——上游新增渠道
  类型后需要人工同步这张表，否则新类型渠道的 `vendor` 会悄悄变成 `null`
  （不是错误，但容易被误读成"这个字段没接上"），chanfields 的 handoff 已
  记录这条，本片沿用未做额外处理。
- **`状态` 列仍然没有接 `row.status`**——延续 XM-CHAN-MERGE0 的刻意取舍
  （见其 handoff）：Sub2API 的 `status` 是 active/disabled/error,
  NewAPI 是 enabled/manually_disabled/auto_disabled/unknown，两个平台
  取值都不同，且都不是"健康/需关注"这种观测新鲜度语义，贸然映射等于猜。
  `PlatformChannelRow` 类型上已有这个字段备用，渲染逻辑仍未接。

## follow_ups

- **`用量窗口`（Sub2API）如果产品侧确认需要 Anthropic 原生 5 小时用量
  百分比而不是费用上限占用率**，需要另立预算评估——chanfields 的契约
  文档已经记录这需要对每个订阅账号再打一次实时代理 Anthropic 官方接口的
  调用，是一个新的、未设预算的扇出，不是本片或简单调整能覆盖的。
- **`today-stats` 预算（40 账号或渠道/次）在真实规模下的覆盖率**未知——
  两个平台的契约文档都记录了这条，账号/渠道数一旦明显超过预算，大多数行
  的"今日统计"会持续显示未接入，是否需要放宽预算或改成缓存策略要等真实
  规模摸清楚再评估。
- **调度的写操作（开关/优先级）是另一个切片 XM-SCHED0**——本片的字段接线
  只覆盖只读展示，写操作仍然需要一个新的 L1/L2 Action 设计。
- **质量指标卡片（首字异常/缓存命中/在线率/TPS/主动探测）明确划给渠道保障
  XM-ASSURE0**——不在本片范围，也不应该有人在没有确认 XM-ASSURE0 范围的
  情况下把它们加回渠道管理页。
- 渠道绑定 Action 的错误处理已经接了 `ActionErrorNote`，但没有专门测试
  乐观并发冲突（`CONFLICT`）在 UI 上的呈现是否清楚——延续自 XM-CHAN-MERGE0
  的 follow-up，本片未涉及。
