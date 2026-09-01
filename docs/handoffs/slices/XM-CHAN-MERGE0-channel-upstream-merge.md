# XM-CHAN-MERGE0：渠道管理 / 上游管理合并为一个页签

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-CHAN-MERGE0-channel-upstream-merge`（base `release/v0.1-launch` @ `b47af3d`）

## commit

十四个提交，最新为 `98343b9`：

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

**⚠️ 提交 1-9 实现的是一个已经被推翻的中间设计**，读这份 handoff 之前请看下面
「两条裁定、一次交付」这一节——本文档描述的是提交 10-14 落地之后的**最终状态**,
不是提交 9 时的状态（提交 9 当时也写过一份 handoff，内容现在已经不对，被本文件
取代）。

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

对比 base `b47af3d` 的完整差异（36 个文件，含二进制截图）：

**新增**

- `web/apps/admin-web/src/components/ChannelBindingCard.tsx` —— 上游映射卡片 +
  确认/解绑两个对话框（提交 6，07:20 未改动）
- `docs/handoffs/slices/XM-CHAN-MERGE0-channel-upstream-merge.md` —— 本文件
  （提交 9 曾写过一版，本次完全重写）
- `docs/evidence/screens/XM-CHAN-MERGE0/09-11*.png` —— 07:20 最终设计的浏览器
  截图（见 tests_run）

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

- `web/apps/admin-web/src/api/platformChannels.ts` + `.test.ts` —— 新增
  `confirmPlatformChannelBinding` / `removePlatformChannelBinding` /
  `PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION`（提交 3，07:20 未改动）
- `web/apps/admin-web/src/api/finance.ts` —— 新增导出 `accountRowType`（提交
  10）：按绑定账号的 `access_method` 派生"订阅账号"/"上游渠道"二分，行列与
  详情页共用同一个函数

渠道管理列表：

- `web/apps/admin-web/src/components/ManagedChannelTable.tsx` + `.test.tsx` ——
  提交 4 从映射工作台改回原型 11/10 列；提交 10 再次整体重写为单表 22 列
  （13 必需 + 9 可选），去掉平台条件分支，新增 ID/类型列与 8 个占位列，工具栏
  加「＋ 添加上游」
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
  「容量与调度」区块（8 个占位字段）
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
多个 pnpm 门禁）——以下是提交 13（最终状态）之后的复跑结果：

- `pnpm --config.verify-deps-before-run=false -r run typecheck` —— PASS
  （5 个前端 workspace 包全部 `tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false -r run test` —— PASS：
  design-tokens 10、ui-primitives 16、ui-admin 232、admin-web 1289，
  共 1547 个用例全绿（ui-storybook 无单测，构建即验证）
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`
  —— PASS（"Storybook build completed successfully"；本片没有新增/修改
  ui-admin 包组件，只改了 `finance.ts` 这份 API 辅助函数与 admin-web 应用层
  组件，`ui-admin` 包本身未改动，Storybook 不需要新故事）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks git --log-opts="release/v0.1-launch..HEAD"` —— PASS
  （13 commits scanned，"no leaks found"）

真实浏览器实测（`node node_modules/vite/bin/vite.js --port 5180 --strictPort`
+ 手写 mock 后端，同一套 mock 脚本复用自提交 9 那一轮，数据契约本片没有改动，
只是渲染逻辑变了）：

- Sub2API/NewAPI 的渠道管理页：单表 22 列（13 必需默认显示 + 9 可选默认收起）,
  两平台列集完全一致；`列管理` 面板逐项核对必需/可选清单与预期完全一致
  （截图 09）；打开一个可选列（上游分组）验证显示真实数据（`gpt-main`）
  而不是空白
- 「类型」筛选三态（订阅账号/上游渠道/未映射）与「状态」筛选两态渲染正确;
  绑定到 `access_method=upstream_key` 的账号显示"上游渠道"，未绑定显示
  "未映射"+候选数提示
- 8 个占位列（容量/并发、调度、今日统计、用量窗口、最近使用、代理、创建
  时间、过期时间）逐一核对显示"未接入"，`调度` 列的 title 属性核对确实
  包含对 XM-CHAN-FIELDS0 与 XM-SCHED0 的说明文字
- 工具栏「＋ 添加上游」按钮存在（截图 09）
- 渠道详情页：新增的「类型」字段（截图 10 核对显示"上游渠道"）与「容量与
  调度」区块（8 个字段全部"未接入"，区块说明文字核对）；既有的「上游映射」
  卡片、经营核算、余额与预计补充、渠道保障等区块未受影响，逐一确认仍正常
  渲染
- 旧路径 `?tab=suppliers`（NewAPI）核对改跳到 `?tab=upstream`，地址栏确认
  不带任何 hash（截图 11）
- 过程中在浏览器 Network 面板发现一次 `net::ERR_CONNECTION_TIMED_OUT`——
  追踪到是同一个 `/api/v1/finance/upstream-accounts` 请求的重复并发实例之一
  超时（4 次请求里 1 个 aborted、1 个 timeout、2 个 200），与上一轮验证记录
  过的 Windows 本地开发网络抖动是同一类环境问题（非应用代码缺陷）；页面在
  后续成功请求到达后自我纠正，重新读取该表格数据后确认渲染正确，未发现
  任何需要修的应用层 bug

截图新增（`docs/evidence/screens/XM-CHAN-MERGE0/`，编号接续提交 9 那一轮的
01-08）：

- `09-single-table-column-manager.png` —— Sub2API 渠道管理页，列管理面板
  展开，13 必需 + 9 可选清单可见
- `10-channel-detail-capacity-scheduling.png` —— 渠道详情页，「渠道与映射」
  区块的「类型」字段 + 新增「容量与调度」区块
- `11-newapi-suppliers-legacy-redirect.png` —— NewAPI，`?tab=suppliers` 落地
  后的渠道管理页（地址栏已改写为 `?tab=upstream`，无锚点）

**01-08 号截图对应的是提交 9（04:40 裁定的中间状态）**，画面上还能看到已经
被删除的「上游管理」页内区块——保留这些截图作为"04:40 裁定当时确实是这样
实现并验证过的"历史记录，不代表当前交付状态；当前状态以 09-11 号截图与
本文档正文为准。

## not_run

- 后端 `go test ./...` / `go vet ./...`：本片全部十三个提交都未改动任何
  `.go` 文件（只是接上了 XM-C-MAP0 早已注册好的两个 Action 与已批的 Query
  契约），未跑；建议验收线按常规仍复跑一次作为基线确认。
- 生产环境验证：未跑，也不应该跑——本任务明确要求不部署、不碰服务器。
- 移动端/窄视口截图：只测了 1440×1000 桌面视口。两张表都复用了
  `DataTableV2`/`stickyFirstColumn` 既有实现，理论上继承既有的横向滚动约束,
  但没有专门在窄视口下截图验证。
- 未对照 `K:/sub2api-src`、`K:/newapi-src` 逐字段核对——07:20 裁定原文要求
  这个核对由并行切片 **XM-CHAN-FIELDS0**（代理 chanfields）负责，不在本片
  范围；本片开工前确认过 chanfields 分支尚未交付任何东西（worktree 停在
  base 提交），因此本片的 8 个占位列这一轮完全没有真实字段可接，全部显式
  未接入。等 chanfields 交付约定的 JSON 字段名之后，需要另一个后续切片把
  真值接上（见 follow_ups）。
- 未测试「订阅账号」类型在真实浏览器里的渲染（mock 数据里没有一条 sub2api/
  newapi 的渠道行绑定到订阅型账号）——这条分支在单元测试里有专门覆盖
  （`ManagedChannelTable.test.tsx`「绑定到订阅账号...显示订阅账号」），
  真实浏览器这一轮没有专门补一条 mock 数据去复现，风险较低（渲染逻辑与
  已验证过的"上游渠道"分支共用同一个组件、只是文案不同）。

## risks

- **8 个占位列在这一轮完全没有真实数据可验证**——本片只能确认"字段不存在时
  正确显示未接入、说明文字正确"，无法确认"字段存在时正确显示真值"，因为
  `PlatformChannelRow` 类型本身没有为这 8 个字段预留 TypeScript 属性（有意
  如此：预留了假的属性形状，等 XM-CHAN-FIELDS0 交付的真实契约如果字段名/
  形状不一致，反而要返工两次）。后续接入这些字段的切片需要同时改
  `api/platformChannels.ts` 的 `PlatformChannelRow`/`RawPage` 类型与
  `ManagedChannelTable.tsx`/`ChannelDetailPage.tsx` 的渲染逻辑，不能只改
  后者。
- **「平台 / 类型」列的二分（订阅账号/上游渠道）与详情页「类型」字段是本片
  按 07:20 裁定原文的措辞新设计的**，`accountRowType` 把 `official_api` 与
  `upstream_key` 都归到"上游渠道"——这个归类没有经过产品侧对"官方直连算不算
  上游渠道"这个具体问题的确认，是从裁定原文"行内区分订阅账号与上游渠道"这
  句话反推出的最直接读法（非订阅即上游渠道）。如果产品负责人对官方直连这
  个子类型有更细的期望，可能需要调整。
- **渠道详情页解析 `serviceId` 的方式与列表页的判据字面相同，但是各自独立
  实现的**——这条风险延续自提交 9 那版 handoff，07:20 的改动没有涉及这部分
  代码，风险原样保留：`ChannelTable.tsx` 用 props 传入，`ChannelDetailPage.tsx`
  自己再查一次 `listServices()`。两处判据目前逻辑等价，但如果未来这条规则
  改变，需要记得两处一起改。
- **确认/解绑映射 Action 参数里的 `service_id` 来自渠道详情页解析出的
  `serviceId`，不是渠道行自带的**——同样延续自提交 9 版本，未受 07:20 影响。
- **提交 1-9 与提交 10-13 之间存在一段"半成品"分支历史**：任何人直接
  `git checkout` 到提交 1-9 之间的某个点，看到的是已经被推翻的 04:40 裁定
  中间状态（比如 `UpstreamAccountsPanel.tsx` 存在、`?tab=suppliers` 带锚点）。
  这在正常合并流程（合并 `HEAD`，不合并中间提交）下不是问题，只在有人手动
  cherry-pick 中间提交时才会造成困惑，记录在案。

## follow_ups

- **XM-CHAN-FIELDS0 交付后需要一个后续切片，把 8 个占位列接上真值**——这不是
  本片能做的（chanfields 这一轮完全没有交付任何字段契约），但结构已经就位:
  8 个字段的列位置、顺序、列头文案、headerTitle 说明都已经写好，后续切片
  只需要（a）在 `api/platformChannels.ts` 里给 `PlatformChannelRow` 加上
  真实字段，（b）把 `ManagedChannelTable.tsx`/`ChannelDetailPage.tsx` 里
  对应的 `pendingFieldColumn`/`UnavailableFact` 换成读真实字段的渲染逻辑。
- **调度的写操作（开关/优先级）是另一个切片 XM-SCHED0**——本片的「调度」列
  与详情页字段都是只读占位，即使 XM-CHAN-FIELDS0 交付了只读字段，写操作
  仍然需要一个新的 L1/L2 Action 设计，不在本片或推测中的"接字段"后续切片
  范围内。
- **质量指标卡片（首字异常/缓存命中/在线率/TPS/主动探测）明确划给渠道保障
  XM-ASSURE0**——07:20 裁定原文明确排除，本片没有做，也不应该有人在没有
  确认 XM-ASSURE0 范围的情况下把它们加回渠道管理页。
- ADMIN-IA §8.6 #1 记录的"上游管理仍缺一张真正的供应商实体表"这件事——04:40
  裁定时曾在展示层做过缓解（供应商归并），07:20 裁定连这个缓解方案都推翻了,
  现在完全没有任何供应商聚合视图。如果后续要做真正的供应商实体，需要另立
  切片从头设计，不能复用本片删除的 `upstreamGrouping.ts`。
- 渠道绑定 Action 的错误处理已经接了 `ActionErrorNote`，但没有专门测试乐观
  并发冲突（`CONFLICT`）在 UI 上的呈现是否清楚——延续自提交 9 版本的
  follow-up，07:20 未涉及，建议后续针对并发冲突单独补一条集成测试。
