# XM-CHAN-MERGE0：渠道管理 / 上游管理合并为一个页签

## status

READY（这份 handoff 本身覆盖的提交 1-15 已经合入，见下方「⚠️ 分支状态」；
分支上更晚的提交属于另一个切片 XM-CHAN-WIRE0，见其独立 handoff）

## branch

`ai/claude/XM-CHAN-MERGE0-channel-upstream-merge`（base `release/v0.1-launch` @ `b47af3d`）

**⚠️ 分支状态（2026-09-02 解决）**：这条分支的提交 1-15（本文档覆盖的范围）
已经被验收线合入 `release/v0.1-launch`（`5cc7b25`/`06536df`，ACCEPTANCE-LOG
"MERGED XM-CHAN-MERGE0 渠道管理单表"）——本片一度在不知情的情况下继续在本地
按 team-lead 更精确的规格做了更多提交。team-lead 回复确认：**沿用同一条
分支，不重开、不改名、不强推**；分支上 `5131b8f` 之后的提交由验收线记账为
独立切片 **XM-CHAN-WIRE0**（消费并行切片 XM-CHAN-FIELDS0 交付的真实字段
契约），完整内容见 `docs/handoffs/slices/XM-CHAN-WIRE0-catalog-wire.md`,
不在本文档范围内。

## commit

二十个提交，最新为 `0896a33`：

1. `1f8c834` refactor(admin-web): extract supplier-grain grouping for upstream registry
2. `075ed4c` feat(nav): drop the standalone suppliers tab on Sub2API/NewAPI
3. `ef7fd1c` feat(api): wire the two channel binding L1 Actions
4. `d5638b5` fix(admin-web): rebuild channel management list to match the prototype
5. `443f990` feat(admin-web): fold upstream management into the channel page as a supplier-grain section
6. `c2e3dde` feat(admin-web): give the channel detail page real data and an upstream-mapping card
7. `cf69293` docs(admin-ia): record the channel/upstream tab merge ruling (§8.8)
8. `b2b9aa9` fix(admin-web): real-browser fixes from XM-CHAN-MERGE0 verification
9. `c64f152` docs(handoff): XM-CHAN-MERGE0 slice handoff + browser evidence
10. `3c97057` refactor(admin-web): collapse channel management to a single table
11. `ccb588a` feat(admin-web): add type badge and pending-field section to channel detail
12. `edbe8d3` refactor(admin-web): drop the upstream-management anchor from the legacy redirect
13. `9d5c107` docs(admin-ia): record the 07:20 supplementary ruling on §8.8
14. `98343b9` docs(handoff): rewrite XM-CHAN-MERGE0 handoff for the 07:20 final state
15. `5131b8f` docs(handoff): fix stale commit count after the handoff-rewrite commit itself

**——提交 15 是 `release/v0.1-launch` 上 `5cc7b25` 合并进去的边界，往上都已经
在生产分支历史里了；往下是尚未合入的部分——**

16. `6c150e3` feat(admin-web): code the channel table against chanfields' exact JSON contract
17. `03acb95` feat(admin-web): match the channel detail page to the new field contract
18. `23cb559` docs(admin-ia): record team-lead's precise 07:20/07:25 implementation spec
19. `bf2e30c` docs(handoff): update for the third refinement pass and its browser proof
20. `0896a33` fix(admin-web): correct rate_multiplier/success_rate numeric encoding

**⚠️ 提交 1-9 实现的是第一版已经被推翻的中间设计，提交 10-15 是第二版（按
ACCEPTANCE-LOG 里 07:20 裁定摘要重做）**，读这份 handoff 之前请看下面「两条
裁定、一次交付」与「第三轮：team-lead 的精确规格」两节——本文档描述的是
提交 16-18 落地之后的**最终状态**，不是提交 9 或提交 15 时的状态（那两个
时间点都各自写过一份 handoff，内容现在都不对，被本文件取代）。

## summary

### 两条裁定、一次交付

产品负责人 2026-09-02 对同一件事发了两条裁定，第二条在同一天内推翻了第一条的
一部分：

- **04:40 裁定**（`docs/handoffs/ACCEPTANCE-LOG.md`）：Sub2API/NewAPI 的「渠道
  管理」与「上游管理」两个页签合并为一个页签「渠道管理」；渠道管理列表照原型
  `V["s2/upstream"]` 不变（四格 KPI、平台/来源+状态筛选、十一列、三个视图）,
  上游管理降级为渠道表下方的页内区块，供应商粒度展示。提交 1-9 按这条裁定
  实现并验证，提交 9 是当时写的 handoff。
- **07:20 补充裁定**（同一份 ACCEPTANCE-LOG，晚 3 小时）：产品负责人在验收线
  复核落地方案后发现"降级为页内区块"仍然是两张表拼在一页，没有真正做到
  "合并"，于是推翻这一点——**渠道管理只保留一张表**，一行 = 一个 Sub2API
  账号 / 一条 NewAPI 渠道，行内区分「订阅账号」与「上游渠道」；新增 ID 列；
  列集 = 13 个必需列（含 ID、名称、平台/类型、状态、倍率/上游倍率、余额/
  有效期、毛利、详情，以及 5 个来自并行切片 XM-CHAN-FIELDS0 的占位列：容量/
  并发、调度、今日统计、用量窗口、最近使用）+ 9 个列管理里默认收起的可选列
  （代理、上游分组、可用模型、供给成本、我方计费消耗、创建时间、过期时间、
  上游名称/联系人、充值成本率，其中 3 个同样是 XM-CHAN-FIELDS0 占位列）;
  登记簿数据并入行与详情页，不再有独立表或独立区块；「添加上游」保留为工具栏
  按钮；质量指标卡片（首字异常/缓存命中/在线率/TPS/主动探测）明确不做,
  归渠道保障（XM-ASSURE0）；调度写操作另立 XM-SCHED0，本轮只读。提交 10-13
  按这条补充裁定重做——`suppliers` 页内区块（`UpstreamAccountsPanel.tsx` +
  `lib/upstreamGrouping.ts`）整体删除，`ManagedChannelTable.tsx` 与
  `ChannelDetailPage.tsx` 再次重写。两条裁定的完整原文与本片的应对，见本仓库
  `docs/architecture/ADMIN-IA.md` §8.8（含 07:20 补充裁定小节）。

提交 1-9 的分支历史原样保留（没有 rebase/squash），因为它们本身没有错——04:40
裁定当时是真实、已批准的决定，提交 1-9 正确落地了它；07:20 裁定是产品负责人
在看到落地方案后**主动修订**的决定，不是纠正 XM-CHAN-MERGE0 的错误。

### 第三轮：team-lead 的精确规格（消息延迟到达）

ACCEPTANCE-LOG 里的 07:20 裁定条目只是产品负责人裁定的**摘要**。team-lead
在裁定发出后不久就给这一片发了完整规格（含逐字段 JSON 名、精确的筛选/视图
清单、调度列的具体 UI 要求），但那几条 SendMessage 都没有以对话轮次的形式
送达——本片是自己在验收线的 ACCEPTANCE-LOG 里挖到摘要、按摘要重做（提交
10-15），写完 handoff 报告完成之后，那几条延迟的消息才真正送达。两次描述
指向同一条裁定，没有冲突，只是精度不同：team-lead 的规格更精确、更可执行,
提交 16-18 按它把提交 10-15 的实现修订到位（不是另一次推翻，是把摘要级的
实现修成规格级的实现）。改动的东西：

- 「平台 / 类型」列显示真实供应商名（`row.vendor` 优先，退回登记簿 join 的
  `upstream_name`）+ 类型徽章，不是之前实现的"平台徽章 + 类型徽章"——页面
  本身已经是单平台域，供应商名才是真正在区分行的信息。
- 8 个占位字段有了逐字段 JSON 名（`kind`/`vendor`/`status`/`capacity`/
  `scheduling`/`today`/`usage_window`/`proxy`/`rate_multiplier`/
  `upstream_multiplier`/`last_used_at`/`created_at`/`expires_at`，全部可空）,
  已经加进 `PlatformChannelRow` 类型并按这些名字解析——之前的实现是用一个
  跟行数据完全无关的 `pendingFieldColumn` 硬编码显示未接入，字段到位后需要
  另一次代码改动才能接上；现在渲染逻辑直接读这些字段，字段到位那天**不需要
  再改前端代码**（已用真实数据端到端验证过，见 tests_run）。
- 「倍率 / 上游倍率」与"类型"这两处是"新契约字段优先、查不到就退回已有的
  登记簿 join"，不是从头到尾未接入——这两处今天就有真实数据，不该因为新契约
  字段还没到位就从"有真数据"退化成"未接入"。
- 「调度」列即使字段到位也保持只读：渲染一个禁用态的开关控件（`role="switch"
  aria-disabled="true"`）+ 优先级，固定 tooltip「调度开关待 XM-SCHED0
  Action」——之前的实现只有一段说明文字，没有开关外观。
- 「用量窗口」按类型分叉：上游渠道类型显示"不适用"（不是"未接入"——这个
  概念对上游渠道本来就不存在），订阅账号/未映射类型字段未接时才是"未接入"。
- 「状态」列**刻意没有**接 chanfields 未来会给的 `status` 字段：那是还没
  定形的枚举，盲目映射进既有的健康/需关注徽章体系等于猜取值，宪法 12 条
  不允许——类型已经留在类型定义里备用，渲染逻辑维持现状，记录在案的取舍。
- 筛选补回「平台 / 来源」（按真实供应商名动态生成选项，回到最早 04:40 那一版
  的做法）；视图从"全部/未映射/需关注"改成规格要求的"全部/订阅账号/上游
  渠道/需关注"（未映射仍是「类型」筛选下拉里的一个选项，只是不再单独占
  一个预置视图）。

精确规格的完整原文（`docs/architecture/ADMIN-IA.md` §8.8 新增小节）与本片的
应对逐条对应，不在这里重复摘抄。

### 现状是什么、为什么要改（04:40 裁定的背景，仍然成立）

`ChannelTable.tsx` 内部按"这个平台是否恰好有一个已登记且 active 的 service"分两条
分支：多实例/未登记时走 `lib/channelTable.ts` 驱动的账号粒度表（这条路已经和原型
逐格对齐，本片基本没动）；单实例 active 时走 `ManagedChannelTable.tsx`，这正是
生产环境 sub2api-prod / newapi-prod 实际在走的分支。XM-C-MAP0 把这条分支做成了
候选/绑定映射工作台——顶部四格是"目录渠道/已确认映射/待处理冲突/目录完整性",
筛选是"映射状态"，列是"上游映射/健康观测/模型能力/经营核算/共享余额"。验收线
当时只审了它背后的数据契约（`GET /api/v1/platforms/{p}/channels` 的候选四态与
绑定 Query），没有对照原型 UI，这个偏差因此一直留到生产环境。提交 1-9 纠正了
这一点；提交 10-13 在此基础上进一步把"渠道表 + 登记簿区块"收成"一张表"。

### 这一片最终做了什么（提交 10-13 之后的状态）

1. **导航**：与 04:40 裁定时相同——Sub2API/NewAPI 页签从 9 格降到 8 格，去掉
   `suppliers`；旧 `?tab=suppliers` 改跳 `?tab=upstream`。**07:20 之后不再带
   锚点**：`LEGACY_TAB_ALIAS_ANCHORS` 与滚动逻辑已删除，因为已经没有独立区块
   可滚——落地在渠道管理页顶部就是登记簿字段所在的地方。服务器平台自己的
   `suppliers`（供应商与采购）完全不受影响。
2. **渠道管理：单表，22 列**：`ManagedChannelTable.tsx` 一行 = 一个 Sub2API
   账号 / 一条 NewAPI 渠道（ChannelRef 粒度）。新增 `id` 独立列（原来是名称
   列里的子文字）；新增「平台 / 类型」列，按绑定账号的 `access_method` 派生
   "订阅账号"/"上游渠道"（`api/finance.ts` 的 `accountRowType`，比既有三态的
   `describeAccessMethod` 粗一档），未绑定显示"未映射"。成功率列整体去掉——
   07:20 裁定的必需列清单里没有它，它此前恒为"未接入 · M1.5"，属于渠道保障
   （XM-ASSURE0）范围。两平台列集完全相同，不再有 11/10 列的平台差异。
   13 必需列默认显示，9 个登记簿/占位列在列管理里默认收起。「＋ 添加上游」
   移到表格工具栏（`DataTableV2` 的 `toolbarExtra`），空态里也放了一份。
   候选/冲突状态与确认/解绑操作仍在渠道详情页，这一点 07:20 没有改。
3. **8 个占位列，等 XM-CHAN-FIELDS0**：容量/并发、调度、今日统计、用量窗口、
   最近使用（必需列）与代理、创建时间、过期时间（可选列）——今天的渠道目录
   契约（`GET /api/v1/platforms/{p}/channels`）完全没有这些字段，不是"查出来
   是空"。这一片只落地列的位置、顺序与说明，全部显式标未接入并注明原因
   （"要 XM-CHAN-FIELDS0 扩展契约之后才有"）；等并行切片 XM-CHAN-FIELDS0
   （代理 chanfields）交付约定 JSON 字段名后，由后续切片接上真值。「调度」
   额外注明：即使字段到位，写操作也在另一个切片 XM-SCHED0，本轮任何时候都
   只读展示。**本片开工时确认过 `wt-xmCHANFIELDS0` 分支仍在 base 提交，没有
   任何交付**，因此这一轮完全没有真实字段可接，8 列在这一片里始终是未接入。
4. **登记簿并入行与详情页，不再有独立区块**：`UpstreamAccountsPanel.tsx`
   （供应商粒度归并表）与 `lib/upstreamGrouping.ts`（归并纯函数，连同各自的
   测试）整体删除。原本必需的六个登记簿列（上游分组、可用模型、供给成本、
   我方计费消耗、上游名称/联系人、充值成本率）降级为列管理里默认收起的可选
   列，数据没变，只是不再默认铺满屏幕。回落到账号粒度分支（多实例/未登记场景）
   同样补了一份工具栏「＋ 添加上游」入口——那条分支之前完全依赖被删除的区块
   提供这个入口。
5. **渠道详情页同步**：`ChannelDetailPage.tsx` 的「渠道与映射」区块新增「类型」
   字段（与行内同一个派生逻辑）；新增「容量与调度」区块，放 8 个占位字段里
   除「详情」外的全部内容（详情本身不是字段）。既有的「上游映射」卡片（确认
   绑定/解绑两个 L1 Action：`finance.platform_channel_binding.set@1` /
   `.remove@1`）与经营核算、余额、渠道保障等区块不变。
6. **ADMIN-IA.md**：§8.8 保留 04:40 裁定原文与内容作为历史记录，新增「07:20
   补充裁定」小节引用补充裁定原文并逐条说明改动；§一/§三/§4.1/§5.2/§九同步
   更新到最终状态。未改动 `ACCEPTANCE-LOG.md`。

### 与原型的差异（如实记录，均有意为之；04:40 裁定时已记录的几条依然成立）

- **「平台 / 类型」列不是原型的 OpenAI/Anthropic/Google/xAI 供应商徽章**。
  登记簿没有"供应商类别"这个维度，编一套假分类会违反宪法 12 条。这一列现在
  显示真实的平台徽章 + 订阅账号/上游渠道/未映射类型徽章——这是 07:20 裁定
  明确要求的维度，不是本片替换掉的。
- **「平台 / 类型」筛选与「状态」筛选是本片按新列结构选的两个维度**，07:20
  裁定原文没有列出筛选清单，只列了列集本身。原型对应格子的三个筛选（接入
  平台/分组状态/周期）今天要么没有对应真实维度，要么在当前粒度下意义有限,
  沿用 04:40 裁定时的取舍：不为了凑数编几个不起作用的下拉。
- **8 个占位列显示"未接入"而不是原型画出来的数字**——这是宪法 12 条的直接
  要求，也是 07:20 裁定原文自己写的"未接字段显示未接入"。
- **详情列有显式文字"详情"，原型是纯箭头 chevron**——沿用既有写法，未特意
  改成箭头（04:40 裁定时已如此，07:20 未涉及这一点）。
- **可用模型仍是"N 个模型（仅数量）"**，没有原型的"验证数/总数 + 模型名
  chips"——既有的 M1.5（渠道保障）缺口，不是本片引入或本片能解决的。

## files_changed

对比 base `b47af3d` 的完整差异（39 个文件，含二进制截图）：

**新增**

- `web/apps/admin-web/src/components/ChannelBindingCard.tsx` —— 上游映射卡片 +
  确认/解绑两个对话框（提交 6，后续两轮均未改动）
- `docs/handoffs/slices/XM-CHAN-MERGE0-channel-upstream-merge.md` —— 本文件
  （提交 9、提交 14 各写过一版，本次是第三次重写）
- `docs/evidence/screens/XM-CHAN-MERGE0/09-13*.png` —— 后两轮的浏览器截图
  （见 tests_run；09-11 是第二轮"摘要级"设计，12-13 是第三轮"精确规格"设计,
  含用真实数据点亮 8 个占位字段的端到端验证）

**新增又删除（净值为零，但分支历史里存在过）**

- `web/apps/admin-web/src/lib/upstreamGrouping.ts` + `.test.ts` —— 04:40 裁定的
  供应商归并纯函数，07:20 裁定推翻后删除
- `web/apps/admin-web/src/components/UpstreamAccountsPanel.tsx` + `.test.tsx` ——
  04:40 裁定的供应商粒度页内区块，07:20 裁定推翻后删除

**改动**

导航与路由：

- `web/packages/ui-admin/src/navigation.ts` + `.test.ts` —— `apiPlatformTabs()`
  去掉 `suppliers`（提交 2，07:20 未改动）
- `web/apps/admin-web/src/lib/platforms.ts` + `.test.ts` —— `LEGACY_TAB_ALIASES`
  新增 `suppliers`（提交 2）；`LEGACY_TAB_ALIAS_ANCHORS` 提交 2 新增、提交 12
  删除（07:20 裁定后不再需要锚点）
- `web/apps/admin-web/src/router.tsx` + `.test.tsx` —— `platformTabLoader` 提交 2
  加锚点逻辑、提交 12 删除锚点逻辑
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx` —— 删除已不可达的
  `platformHasUpstreamRegistry` 分支（提交 1）；修正一处过期的文档注释指向
  已删除的 `UpstreamAccountsPanel`（提交 10）

API：

- `web/apps/admin-web/src/api/platformChannels.ts` + `.test.ts` —— 提交 3 新增
  `confirmPlatformChannelBinding` / `removePlatformChannelBinding` /
  `PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION`；提交 16 新增
  `PlatformChannelFieldsExtension`（XM-CHAN-FIELDS0 逐字段 JSON 名，全部
  可空）与解析函数 `parseFieldsExtension`，`PlatformChannelRow` 继承这个
  接口——字段一旦从服务端拿到非 null 值，行列渲染不需要再改代码
- `web/apps/admin-web/src/api/finance.ts` —— 新增导出 `accountRowType`（提交
  10）：按绑定账号的 `access_method` 派生"订阅账号"/"上游渠道"二分，行列与
  详情页共用同一个函数

渠道管理列表：

- `web/apps/admin-web/src/components/ManagedChannelTable.tsx` + `.test.tsx` ——
  提交 4 从映射工作台改回原型 11/10 列；提交 10 再次整体重写为单表 22 列
  （13 必需 + 9 可选），去掉平台条件分支，新增 ID/类型列与 8 个占位列，工具栏
  加「＋ 添加上游」；提交 16 按 team-lead 的精确规格再次修订——「平台 / 类型」
  改显示真实供应商名（`row.vendor` 优先、退回登记簿 join）而不是平台徽章,
  「倍率 / 上游倍率」同样新契约字段优先、退回登记簿 join，8 个占位列改成
  实际读取 `PlatformChannelFieldsExtension` 的字段（之前是硬编码未接入),
  「调度」列加禁用态开关控件，「用量窗口」按类型分叉不适用/未接入，筛选
  补回「平台 / 来源」（动态供应商名选项），视图改成 全部/订阅账号/上游渠道/
  需关注
- `web/apps/admin-web/src/components/ChannelTable.tsx` —— 提交 4 两条分支共享
  口径声明/顶部四格；提交 5 挂载页内登记簿区块 + 滚动锚点 effect；提交 10
  删除该区块的挂载与滚动 effect，给账号粒度回落分支补一份工具栏入口
- `web/apps/admin-web/src/components/ChannelScopeNote.tsx` —— 提交 4 入口从
  页签链接改成锚点链接；提交 10 改为 `grain` 参数化（按渠道/账号两条分支
  分别措辞），不再指向任何独立区块
- `web/apps/admin-web/src/components/ChannelsPanel.test.tsx` —— 断言跟着
  ChannelScopeNote 的文案/链接改动更新（提交 4、提交 10 两次）
- `web/apps/admin-web/src/lib/channelTable.ts` —— 修正一处过期的文档注释
  （提交 10）

渠道详情页：

- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx` + `SupplyDetailPages.test.tsx`
  —— 提交 6 从纯 UI 壳接真实数据 + 上游映射卡片；提交 11 新增「类型」字段与
  「容量与调度」区块（8 个占位字段）；提交 17 把「类型」「倍率 / 上游倍率」
  「来源上游」改成新契约字段优先、退回既有 join，「容量与调度」区块改成
  实际读取扩展字段（不适用/未接入按类型分叉，同行为的行列版本）
- `web/apps/admin-web/src/pages/UpstreamDetailPage.tsx` / `SupplierCreatePage.tsx`
  —— 返回链接：提交 5 改成 `?tab=upstream#upstream-management`，提交 12 简化
  为 `?tab=upstream`（不带锚点）
- `web/apps/admin-web/src/components/UpstreamAccountDialog.tsx` + `.test.tsx` ——
  新建按钮文案改成原型字面「＋ 添加上游」（提交 1，07:20 未改动；提交 10 把
  这个按钮的挂载位置从区块顶部移到了渠道表工具栏，组件本身没变）
- `web/apps/admin-web/src/lib/upstreamTotals.ts` —— 新增导出
  `oldestActualTimestamp`（提交 1，随 `UpstreamAccountsPanel` 一起在提交 10
  失去了唯一调用方，但函数本身不影响正确性，未删除——如果后续任何汇总类
  组件需要"取最旧的有效观测时间"，可以直接复用，删除后要用还得重写）

文档：

- `docs/architecture/ADMIN-IA.md` —— 提交 7 新增 §8.8（04:40 裁定）；提交 13
  新增「07:20 补充裁定」小节，并修正 §一/§三/§4.1/§5.2/§九 里所有描述"上游
  管理是页内区块"的地方

## tests_run

在 `web/` 目录串行执行（Windows worktree 用镜像脚本补齐 `node_modules`，
命令带 `--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；未并发跑
多个 pnpm 门禁）——以下是提交 18（最终状态）之后的复跑结果：

- `pnpm --config.verify-deps-before-run=false -r run typecheck` —— PASS
  （5 个前端 workspace 包全部 `tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false -r run test` —— PASS：
  design-tokens 10、ui-primitives 16、ui-admin 232、admin-web 1300，
  共 1558 个用例全绿（ui-storybook 无单测，构建即验证）
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`
  —— PASS（"Storybook build completed successfully"；本片没有新增/修改
  ui-admin 包组件，只改了 `api/platformChannels.ts`/`api/finance.ts` 这两份
  API 辅助函数与 admin-web 应用层组件，`ui-admin` 包本身未改动，Storybook
  不需要新故事）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks git --log-opts="release/v0.1-launch..HEAD"` —— PASS
  （18 commits scanned，"no leaks found"）

真实浏览器实测（`node node_modules/vite/bin/vite.js --port 5180 --strictPort`
+ 手写 mock 后端；同一套 mock 数据契约，仅为验证"字段点亮"这一件事在
`ch-openai-main` 这一行额外填了 8 个扩展字段的样例值）：

- Sub2API/NewAPI 的渠道管理页：单表 22 列，两平台列集完全一致；`列管理`
  面板逐项核对 13 必需 + 9 可选清单（截图 09，第二轮验证时截的，列集本身
  这一轮没变）
- 「平台 / 来源」「类型」「状态」三个筛选与 全部/订阅账号/上游渠道/需关注
  四个视图逐项核对；「平台 / 来源」的选项确认是真实供应商名（`Relay 甲`/
  `Relay 甲直连`）动态生成，不是编的静态列表
- **8 个占位列点亮验证**（本轮验证的核心）：给 `ch-openai-main` 这一行的
  mock 数据填上 `kind`/`vendor`/`capacity`/`scheduling`/`today`/`proxy`/
  `rate_multiplier`/`upstream_multiplier`/`last_used_at`/`created_at` 的
  样例值（`usage_window`/`expires_at` 故意留 null，验证"部分字段点亮、部分
  仍未接入"的混合状态也正确），刷新页面后逐格核对：供应商名显示
  "Relay 甲直连"（覆盖登记簿 join 的"Relay 甲"）、容量"12 / 50"、调度开关
  呈勾选态 + "优先级 1"、今日统计"842 次 · 99.2% · ¥45.60"、用量窗口因为
  `kind=upstream` 显示"不适用"（不是未接入，尽管这一格本身也是 null）、
  倍率"0.80× / 1.10×"（覆盖登记簿 join 的"0.85× / 1.15×"）、最近使用与
  创建时间显示真实时间戳、过期时间仍未接入（截图 12）。**全程没有再改一行
  渲染代码**——这正是 team-lead 规格里"字段到位后不需要再改前端"这个设计
  目标的端到端证明，不只是单测断言
- 渠道详情页同一行核对同样的点亮结果：「类型」「来源上游」「倍率 / 上游
  倍率」「容量与调度」区块的每个字段都与行内一致（截图 13）
- 过程中两次在浏览器 Network 面板看到 `/api/v1/finance/upstream-accounts`
  的重复并发请求里有一个 timeout/aborted，与之前两轮验证记录过的 Windows
  本地开发网络抖动是同一类环境问题（非应用代码缺陷）；页面在后续成功请求
  到达后自我纠正，重新核对确认渲染正确，未发现任何需要修的应用层 bug

截图（`docs/evidence/screens/XM-CHAN-MERGE0/`）：

- `01-08`：第一轮（04:40 裁定摘要）实现的截图，画面上能看到已经被删除的
  「上游管理」页内区块——保留作为历史记录，不代表当前状态
- `09-11`：第二轮（07:20 裁定摘要）实现的截图，单表 22 列但字段渲染逻辑
  是硬编码未接入——列集布局仍然正确，但截图里看不出"字段点亮"这件事
  （那时候还没有真实字段可读）
- `12-row-fields-lit-up.png`/`13-detail-fields-lit-up.png`：第三轮（精确
  规格）实现的截图，用填了样例值的 mock 数据验证 8 个占位字段的渲染逻辑
  真的在读 `PlatformChannelFieldsExtension`，不是摆设

当前状态以 12-13 号截图与本文档正文为准。

## not_run

- 后端 `go test ./...` / `go vet ./...`：本片全部十八个提交都未改动任何
  `.go` 文件（只是接上了 XM-C-MAP0 早已注册好的两个 Action 与已批的 Query
  契约），未跑；建议验收线按常规仍复跑一次作为基线确认。
- 生产环境验证：未跑，也不应该跑——本任务明确要求不部署、不碰服务器。
- 移动端/窄视口截图：只测了 1440×1000 桌面视口。两张表都复用了
  `DataTableV2`/`stickyFirstColumn` 既有实现，理论上继承既有的横向滚动约束,
  但没有专门在窄视口下截图验证。
- 未对照 `K:/sub2api-src`、`K:/newapi-src` 逐字段核对具体字段名/取值范围是否
  与 chanfields 实际交付的契约完全一致——那本来就是 XM-CHAN-FIELDS0 自己的
  工作，不是这一片能做的；本片只按 team-lead 给定的字段名把类型和渲染逻辑
  先接好、用**编造的样例数据**证明了"字段非 null 时能正确渲染"这条链路,
  不代表 chanfields 交付的真实契约形状与这里假设的完全一致——如果不一致
  （比如某个字段实际是别的类型，或者嵌套结构不同），仍然需要一次小的对齐
  修改，只是比"完全没有类型定义、从零接入"要小得多。
- 未测试「订阅账号」类型在真实浏览器里的渲染（mock 数据里没有一条 sub2api/
  newapi 的渠道行绑定到订阅型账号）——这条分支在单元测试里有专门覆盖,
  真实浏览器这一轮没有专门补一条 mock 数据去复现，风险较低（渲染逻辑与
  已验证过的"上游渠道"分支共用同一个组件、只是文案不同）。

## risks

- ~~提交 1-15 已经被合入 release/v0.1-launch，提交 16-20 还没有~~——**已解决**：
  team-lead 确认沿用同一条分支继续合，`5131b8f` 之后的提交由验收线记账为
  独立切片 XM-CHAN-WIRE0（见 `docs/handoffs/slices/XM-CHAN-WIRE0-catalog-wire.md`）。
  这条风险原样保留一份历史记录：本片开工时没有意识到自己的分支已经被合并,
  继续在本地按 team-lead 更精确的规格做了提交——这是当时的真实过程，不是
  凭空发生的，供以后类似情况参考（长任务应定期 `git fetch` 检查自己的分支
  是否已被合入）。
- **提交 16-18 最初是照 team-lead 消息里给的 JSON 字段名猜的形状，提交 20
  用 chanfields 实际交付的契约（`contracts/connectors/{sub2api,newapi}.
  channel-catalog.v3.md` 与 `internal/platform/httpapi/platform_channels.go`
  的 struct tag）核对过一遍，发现并修了两处真实的类型/数值编码错误**（详见
  提交 20 的说明，以及 XM-CHAN-WIRE0 handoff 的完整记录）：`rate_multiplier`/
  `upstream_multiplier` 契约是 `*float64`（数字），不是十进制字符串；
  `today.success_rate` 是 0-1 小数且 Sub2API 端恒为 null，即使
  `requests`/`cost_minor` 有真数据，之前的实现会默认成 0 显示假的"0.0%"。
  修完之后逐字段核对过 Go struct 的 json tag，确认类型定义与实际契约完全
  一致，不只是跟裁定摘要或 team-lead 的转述一致。
- **「平台 / 类型」列与详情页「类型」字段的默认二分逻辑（`accountRowType`)
  把 `official_api` 与 `upstream_key` 都归到"上游渠道"**——这个归类没有经过
  产品侧对"官方直连算不算上游渠道"这个具体问题的确认，是从裁定"行内区分
  订阅账号与上游渠道"这句话反推出的最直接读法（非订阅即上游渠道）。今天
  `row.kind` 非 null 时会覆盖这个默认逻辑，所以一旦 chanfields 交付了真实
  的 `kind` 分类，这条风险自动消解；只在 chanfields 交付之前、且产品负责人
  对这个子类型有不同期望时才需要手动调整默认逻辑。
- **渠道详情页解析 `serviceId` 的方式与列表页的判据字面相同，但是各自独立
  实现的**——这条风险延续自最早的版本，三轮改动都没有涉及这部分代码，风险
  原样保留：`ChannelTable.tsx` 用 props 传入，`ChannelDetailPage.tsx` 自己
  再查一次 `listServices()`。两处判据目前逻辑等价，但如果未来这条规则改变,
  需要记得两处一起改。
- **确认/解绑映射 Action 参数里的 `service_id` 来自渠道详情页解析出的
  `serviceId`，不是渠道行自带的**——同样延续自最早版本，未受任何一轮改动
  影响。
- **分支历史里有两段已经被后续裁定推翻的中间状态**（提交 1-9 是 04:40 裁定
  的实现，提交 10-15 是"按 ACCEPTANCE-LOG 摘要重做"的实现）：任何人直接
  `git checkout` 到这两段之间的某个点，看到的都不是最终交付状态。这在正常
  合并流程（合并 `HEAD`，不合并中间提交）下不是问题，只在有人手动
  cherry-pick 中间提交时才会造成困惑，记录在案。
- **team-lead 的 SendMessage 有过不止一次没有以对话轮次形式送达这一片**
  （原始的 07:20/07:25 规格消息、以及后续的重发消息 df28dc74，都是在本片
  已经完成"按 ACCEPTANCE-LOG 摘要重做"并报告完成之后才真正送达的）——这不是
  本片能修的问题，但导致了本文档记录的三轮实现而不是一轮；如果这类延迟是
  系统性的，可能值得在协调层面单独排查，不只是"写进 handoff 记录一下"。

## follow_ups

- **等 chanfields 真正交付渠道目录契约扩展后，建议做一次小规模的核对**（不是
  重新实现）：确认 `PlatformChannelFieldsExtension` 里假设的字段名/嵌套结构
  与 chanfields 实际交付的完全一致；如果一致，字段会在不改代码的前提下自动
  显示真值（本片已经用编造的样例数据端到端验证过这条链路，见 tests_run）;
  如果有出入，需要调整 `api/platformChannels.ts` 的解析函数
  `parseFieldsExtension`。
- **调度的写操作（开关/优先级）是另一个切片 XM-SCHED0**——本片的「调度」列
  与详情页字段都是只读展示（禁用态开关控件），即使 XM-CHAN-FIELDS0 交付了
  只读的 `scheduling` 字段，写操作仍然需要一个新的 L1/L2 Action 设计，不在
  本片或"核对字段契约"这个后续 follow-up 的范围内。
- **质量指标卡片（首字异常/缓存命中/在线率/TPS/主动探测）明确划给渠道保障
  XM-ASSURE0**——裁定原文明确排除，本片没有做，也不应该有人在没有确认
  XM-ASSURE0 范围的情况下把它们加回渠道管理页。
- ADMIN-IA §8.6 #1 记录的"上游管理仍缺一张真正的供应商实体表"这件事——04:40
  裁定时曾在展示层做过缓解（供应商归并），07:20/07:25 裁定连这个缓解方案都
  推翻了，现在完全没有任何供应商聚合视图。如果后续要做真正的供应商实体,
  需要另立切片从头设计，不能复用本片删除的 `upstreamGrouping.ts`。
- 渠道绑定 Action 的错误处理已经接了 `ActionErrorNote`，但没有专门测试乐观
  并发冲突（`CONFLICT`）在 UI 上的呈现是否清楚——延续自最早版本的
  follow-up，三轮改动都未涉及，建议后续针对并发冲突单独补一条集成测试。
