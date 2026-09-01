# XM-CHAN-MERGE0：渠道管理 / 上游管理合并为一个页签

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-CHAN-MERGE0-channel-upstream-merge`（base `release/v0.1-launch` @ `b47af3d`）

## commit

八个提交，最新为 `b2b9aa9`：

1. `1f8c834` refactor(admin-web): extract supplier-grain grouping for upstream registry
2. `075ed4c` feat(nav): drop the standalone suppliers tab on Sub2API/NewAPI
3. `ef7fd1c` feat(api): wire the two channel binding L1 Actions
4. `d5638b5` fix(admin-web): rebuild channel management list to match the prototype
5. `443f990` feat(admin-web): fold upstream management into the channel page as a supplier-grain section
6. `c2e3dde` feat(admin-web): give the channel detail page real data and an upstream-mapping card
7. `cf69293` docs(admin-ia): record the channel/upstream tab merge ruling (§8.8)
8. `b2b9aa9` fix(admin-web): real-browser fixes from XM-CHAN-MERGE0 verification

## summary

产品负责人 2026-09-02 04:40 裁定（`docs/handoffs/ACCEPTANCE-LOG.md` 最后一行）：
Sub2API/NewAPI 的「渠道管理」与「上游管理」两个页签合并为一个页签「渠道管理」，
布局逐格照原型 `V["s2/upstream"]` 不变；上游管理降级为渠道表下方的页内区块，
供应商粒度展示，含「＋ 添加上游」入口。同一条裁定附带纠正了两处此前的偏差
（§8.6 #1 记录的上游管理行粒度问题、以及本片才发现的"渠道管理页被做成了映射
工作台"问题）。完整裁定原文与本片的应对，见本仓库 `docs/architecture/ADMIN-IA.md`
新增的 §8.8。

### 现状是什么、为什么要改

`ChannelTable.tsx` 内部按"这个平台是否恰好有一个已登记且 active 的 service"分两条
分支：多实例/未登记时走 `lib/channelTable.ts` 驱动的账号粒度表（这条路已经和原型
逐格对齐，本片基本没动）；单实例 active 时走 `ManagedChannelTable.tsx`，这正是
生产环境 sub2api-prod / newapi-prod 实际在走的分支。XM-C-MAP0 把这条分支做成了
候选/绑定映射工作台——顶部四格是"目录渠道/已确认映射/待处理冲突/目录完整性"，
筛选是"映射状态"，列是"上游映射/健康观测/模型能力/经营核算/共享余额"。验收线
当时只审了它背后的数据契约（`GET /api/v1/platforms/{p}/channels` 的候选四态与
绑定 Query），没有对照原型 UI，这个偏差因此一直留到生产环境。

另外，`ChannelTable.tsx` 的顶部口径声明、四格 KPI 在这条分支里被早期 `return`
跳过了，只有回落到账号粒度分支时才会渲染——换句话说，生产环境今天既看不到
正确的 KPI/列/筛选，也看不到这两样东西本身。

`suppliers` 页签（上游管理）此前是一行 = 一个 `finance.upstream_account` 的平铺表，
不是供应商实体（ADMIN-IA §8.6 #1 记录的缺口）。

### 这一片做了什么

1. **导航**：Sub2API/NewAPI 页签从 9 格降到 8 格，去掉 `suppliers`；旧
   `?tab=suppliers` 通过 `LEGACY_TAB_ALIASES` 改跳 `?tab=upstream`，并新增
   `LEGACY_TAB_ALIAS_ANCHORS` 让 `router.tsx` 的 `platformTabLoader` 在改跳时
   附带 `#upstream-management` 锚点——落地后自动滚到页内区块，不是停在页顶。
   服务器平台自己的 `suppliers`（供应商与采购）完全不受影响。
2. **渠道管理列表页重建**：`ManagedChannelTable.tsx` 改回原型的 11/10 列
   （Sub2API 多一列"成功率"）、吸附首列、紧凑密度、三个视图（"全部/未映射/
   需关注"——第二个视图把原型的"OpenAI"静态筛选换成"未映射"，原因见下面
   「与原型的差异」）。候选/冲突状态与确认/解绑操作从这张表搬到渠道详情页；
   这张表的「平台 / 来源」列现在只显示结果（已绑定上游的名字，或「未映射」
   徽章 + 候选数提示）。顶部四格与口径声明现在两条分支都会渲染。
3. **上游管理并入页内区块**：`UpstreamAccountsPanel.tsx` 从账号粒度改成供应商
   粒度——按 `upstream_name`（缺省退回 `base_url` host）在展示层归并
   （`lib/upstreamGrouping.ts`，纯函数，16 个单测）；展开一个供应商看到的是
   原样保留的账号级表格（列、写操作、令牌映射展开一个字节都没改）。顶部四格
   换成原型的「上游实例 / 接入账号-渠道 / 本期我方计费消耗 / 本期整体毛利」。
   挂载在 `ChannelTable.tsx` 里两条分支共用的 `id="upstream-management"` 区块下。
4. **渠道详情页接真实数据**：`ChannelDetailPage.tsx` 不再是纯 UI 壳——按"恰好
   一个已登记且 active 的 service"这条与列表页相同的判据解析出 `serviceId`，
   用既有的 `listPlatformChannels` 找到这一行，填充真实能拿到的字段（渠道名、
   上游分组、模型数量、共享余额/可用天数），经营核算等确实没有的字段继续
   诚实标"未接入"。新增 `ChannelBindingCard.tsx`："上游映射"卡片 + 确认绑定
   /解绑两个对话框，接上后端早已注册、之前没有前端 UI 的两个 L1 Action
   （`finance.platform_channel_binding.set@1` / `.remove@1`）。
5. **ADMIN-IA.md** 新增 §8.8，§2.1/§5.2/§三/§4.1/§九 同步更新；未改动
   `ACCEPTANCE-LOG.md`。

### 与原型的差异（如实记录，均有意为之）

- **「平台 / 来源」列/筛选不是原型的 OpenAI/Anthropic/Google/xAI 供应商徽章**。
  原型的样例数据给每个渠道贴了一个 AI 供应商分类；我们的登记簿没有这个维度
  （`UpstreamAccountItem` 没有"供应商类别"字段，只有 `system_type` 这种"哪个
  被管平台"的维度）。编一套假分类会违反宪法 12 条（禁止裸数字/假维度冒充真实
  完整数据）。改成显示绑定上游的**真实名字**（或"未映射"徽章），筛选选项也
  跟着从真实数据动态生成，不是硬编码的供应商列表。
- **上游管理表没有"接入平台/分组状态/周期"三个筛选下拉**（原型有）。这三个
  筛选对应的维度我们要么没有（供应商级分组目录，同 §8.6 #1），要么在当前
  每页只看归属本平台+未配对的过滤规则下意义有限（接入平台筛选，因为同一个
  供应商在两个平台页上会分别过滤出各自的子集，不会同时看到跨平台徽章）。
  留了搜索框，没有编几个不起作用的下拉。
- **供应商级"上游账号/凭据"列显示的是账号数+凭据配置率，不是原型的单一账号
  邮箱**。原型的"上游"字面就是一次登录，我们的供应商是多个独立账号的展示层
  归并，没有"这个供应商的账号"这个单一事实，只能诚实地给聚合计数。
- **"接入分组"列显示的是组内账号自带的分组名去重列表，不是原型"3/4 已接入/
  全部分组"这种有完整目录的计数**。理由同上，供应商级分组目录不存在。
- **"可用模型"仍是"N 个模型（仅数量）"**，没有原型的"验证数/总数 + 模型名
  chips"——这是既有的 M1.5（渠道保障）缺口，不是本片引入的。
- **详情列有显式文字"详情"，原型是纯箭头 chevron**——两者都提供同样的导航
  语义，显式文字对可访问性更友好，沿用了 `ChannelTableColumns.tsx` 既有的同名
  列写法，未特意改成箭头。

## files_changed

**新增（4）**

- `web/apps/admin-web/src/lib/upstreamGrouping.ts` —— 供应商归并纯函数
- `web/apps/admin-web/src/lib/upstreamGrouping.test.ts` —— 16 个单测
- `web/apps/admin-web/src/components/ChannelBindingCard.tsx` —— 上游映射卡片 +
  确认/解绑两个对话框
- `docs/handoffs/slices/XM-CHAN-MERGE0-channel-upstream-merge.md` —— 本文件

**改动（21）**

导航与路由：

- `web/packages/ui-admin/src/navigation.ts` —— `apiPlatformTabs()` 去掉
  `suppliers`；相关注释更新
- `web/packages/ui-admin/src/navigation.test.ts`
- `web/apps/admin-web/src/lib/platforms.ts` —— `LEGACY_TAB_ALIASES` 新增
  `suppliers`；新增导出 `LEGACY_TAB_ALIAS_ANCHORS`
- `web/apps/admin-web/src/lib/platforms.test.ts`
- `web/apps/admin-web/src/router.tsx` —— `platformTabLoader` 按
  `LEGACY_TAB_ALIAS_ANCHORS` 追加锚点
- `web/apps/admin-web/src/router.test.tsx` —— 补充断言 + 共用 `okHandler`
  新增 `/finance/upstream-accounts` 兜底路由（原本缺失，导致新增的上游管理
  区块在既有测试里读到 404）
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx` —— 删除
  `suppliers` case 里已不可达的 `platformHasUpstreamRegistry` 分支
  （sub2api/newapi 的 `suppliers` tab 值已不存在，`platformTabLoader` 会在
  组件渲染前拦截；服务器分支不变）

API：

- `web/apps/admin-web/src/api/platformChannels.ts` —— 新增
  `confirmPlatformChannelBinding` / `removePlatformChannelBinding` /
  `PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION`
- `web/apps/admin-web/src/api/platformChannels.test.ts`

渠道管理列表：

- `web/apps/admin-web/src/components/ManagedChannelTable.tsx` —— 整体重写
- `web/apps/admin-web/src/components/ManagedChannelTable.test.tsx` —— 整体重写
- `web/apps/admin-web/src/components/ChannelTable.tsx` —— 两条分支共享
  口径声明/顶部四格/上游管理区块；新增滚到锚点的 effect
- `web/apps/admin-web/src/components/ChannelScopeNote.tsx` —— 「上游管理」
  入口从页签链接改成页内锚点链接
- `web/apps/admin-web/src/components/ChannelsPanel.test.tsx` —— 两个断言
  跟着 ChannelScopeNote 的文案/链接改动更新

上游管理区块：

- `web/apps/admin-web/src/lib/upstreamTotals.ts` —— 新增导出
  `oldestActualTimestamp`（从 UpstreamAccountsPanel 提取）
- `web/apps/admin-web/src/components/UpstreamAccountsPanel.tsx` —— 整体重写
- `web/apps/admin-web/src/components/UpstreamAccountsPanel.test.tsx` —— 整体重写
- `web/apps/admin-web/src/components/UpstreamAccountDialog.tsx` —— 新建按钮
  文案改成原型字面「＋ 添加上游」
- `web/apps/admin-web/src/components/UpstreamAccountDialog.test.tsx`

渠道详情页：

- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx` —— 整体重写（原纯
  UI 壳 → 接真实数据 + 上游映射卡片）
- `web/apps/admin-web/src/pages/SupplyDetailPages.test.tsx` —— 整体重写
- `web/apps/admin-web/src/pages/UpstreamDetailPage.tsx` —— 返回链接目标改为
  `?tab=upstream#upstream-management`（页面本身仍是 UI-only 壳，未接数据）
- `web/apps/admin-web/src/pages/SupplierCreatePage.tsx` —— 同上

文档：

- `docs/architecture/ADMIN-IA.md` —— 新增 §8.8；§一/§2.1/§三/§4.1/§5.2/§九
  同步更新（未改 `ACCEPTANCE-LOG.md`）

## tests_run

在 `web/` 目录串行执行（Windows worktree 用镜像脚本补齐 `node_modules`，
命令带 `--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；未并发跑
多个 pnpm 门禁）：

- `pnpm --config.verify-deps-before-run=false -r run typecheck` —— PASS
  （5 个前端 workspace 包全部 `tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false -r run test` —— PASS：
  design-tokens 10、ui-primitives 16、ui-admin 232、admin-web 1313，
  共 1571 个用例全绿（ui-storybook 无单测，构建即验证）
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`
  —— PASS（"Storybook build completed successfully"；本片没有新增/修改
  ui-admin 包组件，只改了 `navigation.ts` 这份数据，`AdminShell.stories.tsx`
  等既有故事已经动态读取它，未新增 story 文件）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks git --log-opts="release/v0.1-launch..HEAD"` —— PASS
  （8 commits scanned，"no leaks found"）

真实浏览器实测（`node node_modules/vite/bin/vite.js --port 5180 --strictPort`
+ 手写 mock 后端，见下）：

- Sub2API/NewAPI 的渠道管理页：4 格 KPI、11/10 列、动态筛选选项、三个视图、
  吸附首列、紧凑密度，逐项核对与原型截图一致（差异见上面「与原型的差异」）
- 上游管理区块：供应商归并（同名两账号合并成一行、按 host 归并、未配对账号
  可见）、展开看到账号级明细
- 渠道详情页：绑定态、冲突态（候选两个账号 + 判定原因 + 冲突说明）分别核对
- 确认绑定对话框：填理由后提交，收到 `run_id` 回执，`invalidateQueries`
  触发列表重新读取（mock 后端不持久化状态，回执与请求体本身是核对重点）
- 过程中发现并修复一个真实 bug：未配对供应商组的"详情"链接原来拼成
  `/platforms/suppliers/<id>`（丢了平台段），现在正确退回当前页面的平台

截图（`docs/evidence/screens/XM-CHAN-MERGE0/`）：

- `01-sub2api-channel-management-top.png` —— Sub2API 渠道管理：口径声明 + 4
  格 KPI + 表格（含未映射候选提示、健康/需关注状态）
- `02-sub2api-upstream-management-section.png` —— 同页往下滚到「上游管理」
  区块：4 格 KPI + 供应商级表格（Relay 甲两账号已归并成一行）
- `03-prototype-reference-s2-upstream.png` —— 对照：原型 `#/s2/upstream`
- `04-prototype-reference-s2-suppliers.png` —— 对照：原型 `#/s2/suppliers`
- `05-newapi-channel-management-top.png` —— NewAPI 渠道管理（10 列，无成功率；
  一行候选冲突状态）
- `06-channel-detail-upstream-mapping-conflict.png` —— 渠道详情页，冲突态的
  「上游映射」卡片
- `07-confirm-binding-dialog.png` —— 确认绑定对话框（候选列表 + 表单）
- `08-binding-confirmed-success-receipt.png` —— 提交成功回执（run_id + 去
  审计记录页入口）

Mock 后端脚本未纳入提交（纯本地实测工具，不是交付物），如需复现：worktree
外的 `link-node-modules.mjs` 同目录下写一个只读 http.Server，覆盖
`/api/v1/services`（sub2api/newapi 各一个 active 单实例）、
`/api/v1/platforms/{sub2api,newapi}/channels`（覆盖已绑定/新鲜、已绑定/过期、
候选未确认、完全未映射、冲突五种候选态）、`/api/v1/finance/upstream-accounts`
+`/upstreams/summary`+`/channels/summary`（含两个同名账号验证供应商归并）、
`POST /api/v1/actions/*` 统一返回 `{action_run_id, result}`；再起一个静态文件
服务器托管 `rendered_v4.html` 做并排对照。

## not_run

- 后端 `go test ./...` / `go vet ./...`：本片未改动任何 `.go` 文件（只是
  接上了 XM-C-MAP0 早已注册好的两个 Action 与已批的 Query 契约），未跑；
  建议验收线按常规仍复跑一次作为基线确认。
- 生产环境验证（真实点开 console.solov.cc 的渠道管理/上游管理/渠道详情）：
  未跑，也不应该跑——本任务明确要求不部署、不碰服务器。需验收线合入部署后
  核实真实候选/冲突分布下的页面表现（生产的绑定候选算法、真实供应商归并
  结果本片完全没有触碰，只是换了个地方展示）。
- 移动端/窄视口截图：只测了 1440×1000 桌面视口，未测原型强调的"表格容器内
  横向滚动、页面不横向滚动"（§11.2）在更窄视口下的表现；两张表都复用了
  `DataTableV2`/`stickyFirstColumn` 既有实现，理论上继承既有的横向滚动约束，
  但没有专门在窄视口下截图验证。
- 供应商级"详情"链接指向第一个成员账号，尚未做真正聚合多账号的供应商详情页
  ——`UpstreamDetailPage.tsx` 本身仍是 ADMIN-IA §8.6 #1 记录的、按账号 id 键入
  的 UI-only 壳，不在本片范围内（brief 明确"Keep the upstream detail/create
  routes...reached from the merged page"，即复用现状路由，不新建）。

## risks

- **渠道详情页解析 `serviceId` 的方式（"当前环境恰好一个已登记且 active 的
  service"）与列表页的判据字面相同，但是各自独立实现的**（`ChannelTable.tsx`
  用 `serviceId`/`serviceStatus` props 由 `PlatformDetailPage.tsx` 传入；
  `ChannelDetailPage.tsx` 自己再查一次 `listServices()` 筛出同类型 service）。
  两处判据目前逻辑等价，但不是同一份代码，如果未来这条规则改变（比如允许
  多实例但按某种规则选一个），需要记得两处一起改，否则列表页显示的渠道
  详情链接会指向一个"点进去反而找不到这条渠道"的详情页。
- **供应商归并是展示层行为，纯前端计算**：`groupUpstreamAccounts` 每次渲染
  都重新按名称/host 分组，账号一旦改名（`upstream_name` 改了）会立刻换到
  另一个分组，且旧分组如果因此变空会消失。这是符合预期的（归并键本来就是
  展示层派生的，不是持久化的供应商 id），但如果运营在"改名"和"确认某个
  供应商还在不在"之间有强依赖，目前没有稳定的供应商标识可以跨改名追踪。
- **上游映射的确认/解绑 Action 参数里 `service_id` 来自渠道详情页解析出的
  `serviceId`，不是渠道行自带的**（`PlatformChannelRow.channelRef.serviceId`
  其实也有这个值，两者应该总是相同，因为渠道本来就是用这个 serviceId 查出来
  的）；本片选用了外层解析出的那份而不是行内的，是因为详情页在"找不到 service"
  的分支根本不会渲染出这一行，所以理论上不会有分叉，但没有显式断言两者一致，
  记录在案。
- **`SupplierRateCell`/`SupplierStatusCell` 等聚合展示的判据是本片新设计的**
  （比如"组内任一账号 disabled 或 attentionCount>0 就整组显示需关注"），没有
  产品侧对这套聚合语义做过专门确认——它们是从「不能编数字、不能瞒信息」这条
  红线反推出的合理默认，但如果产品负责人对"供应商整体状态该怎么合成"有具体
  期望，可能需要调整。

## follow_ups

- ADMIN-IA §8.6 #1 记录的"上游管理仍缺一张真正的供应商实体表"这件事，本片
  只在展示层缓解（按名称/host 归并），没有解决。如果后续要做真正的供应商
  实体（比如允许运营给供应商改名而不影响历史账号、或者需要跨改名的稳定
  id），需要另立切片设计存储层。
- `UpstreamDetailPage.tsx`（`/platforms/:p/suppliers/:id`）目前仍是账号粒度
  的 UI-only 壳；供应商表的"详情"现在指向这个壳、传入第一个成员账号的 id。
  如果要做名副其实的"供应商详情页"（汇总它名下全部账号，而不是只看第一个），
  需要重新设计这个页面，不在本片范围内。
- 供应商级列表目前没有筛选下拉（对比原型的"接入平台/分组状态/周期"三个）。
  如果产品负责人认为这几个筛选维度值得为了 UI 一致性而"编"（比如"接入平台"
  在跨平台供应商场景下确实有意义），需要先决定要不要放宽当前"没有真实维度
  就不做筛选"的取舍，再实现。
- 渠道绑定 Action 的错误处理已经接了 `ActionErrorNote`（403/409/…都会显示
  服务端原文），但没有专门测试乐观并发冲突（`CONFLICT`，绑定在提交期间被
  别人改过）在 UI 上的呈现是否清楚——现有测试只覆盖了成功与"理由必填"两条
  路径，建议后续针对并发冲突单独补一条集成测试。
