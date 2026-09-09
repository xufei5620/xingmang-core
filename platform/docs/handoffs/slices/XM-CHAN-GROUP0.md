# XM-CHAN-GROUP0：NewAPI 渠道分组字段接线

## status

READY（待验收线审读、复跑并人工合入）

## branch / worktree / base

- branch: `ai/claude/XM-CHAN-GROUP0`
- worktree: `K:/星芒统一控制平台/wt-xmCHANGROUP0`
- base: `release/v0.1-launch@eadef4b`（`git merge-base` 确认，见下方"分支状态"）

## 分支状态（如实记录）

开工时 `release/v0.1-launch` 是 `eadef4b`。收尾阶段发现这个仓库里 `release/v0.1-launch`
这个本地分支引用已经前进到 `da5776e`（`XM-DEPLOY-SELFUPDATE0` 合入 + 两条生产部署
ACCEPTANCE-LOG）——worktree 共享同一份 `.git`，其它 worktree/操作对共享分支引用的更新
会立刻反映到这里，不是我做的。`git merge-base release/v0.1-launch HEAD` 确认仍是
`eadef4b`，本分支没有被重定基（rebase）或改写，只是没有跟着挪动。直接
`git diff release/v0.1-launch..HEAD` 会把 `eadef4b`→`da5776e` 之间的 20 个不相关文件
（`deploy/scripts/deploy-local.sh`、`docs/handoffs/slices/XM-DEPLOY-SELFUPDATE0.md` 等）
也算进来，误判成"这条分支删了它们"——正确的差异用三点 diff：
`git diff release/v0.1-launch...HEAD`，结果是本文档"files_changed"一节列出的 15 个文件,
与 `da5776e` 上的改动没有任何文件重叠，未发现冲突风险。是否需要在合入前 rebase 到
`da5776e`，留给验收线判断；本片没有自行 rebase。

## commits（5，从旧到新）

1. `7e9d575` feat(newapi): map channel group field from upstream into catalog
2. `d7ca17e` docs(contracts): document newapi channel group field
3. `157130c` feat(httpapi): decode group field in platform channels query
4. `8735f77` feat(admin-web): show real NewAPI channel group in overview and detail pages
5. `2b884a2` docs(admin-ia): correct stale NewAPI overview tile table for §8.7

## summary

任务交接（团队负责人）与 `XM-NEWAPI-OVERVIEW0` 的 handoff 都点名同一个缺口：NewAPI 概览
「渠道健康」卡的"分组"列长期显示"未接入"，原因是**没有任何连接器读取 NewAPI 原生的
`Channel.Group` 字段**（不是采集失败，是从没人去查）——`Group` 字段本身在
`K:/newapi-src` `model/channel.go:40` 确认存在（`json:"group"`，gorm 默认值
`'default'`，逗号分隔的分组名字符串，上游用它做按分组路由与计费，`GetGroups()` 是
上游自己的解析辅助函数）。本片把这条数据源接到底：连接器 → 契约 → httpapi →
前端三处展示位置，全部端到端。

### 1. 连接器（`connectors/newapi`）

- `contract.go`：`ChannelStatus` 新增 `Group *string`（可空，语义与来源见字段
  doc comment），`catalogFields()` 新增 `group` 键（沿用"没有数据就不写这个键"的既有
  纪律，不写 null）。
- `upstream.go`：`channelItem` 新增 `Group string \`json:"group"\``（非指针——上游这个
  字段永远不是 null，最坏情况是空字符串）；新增 `parseChannelGroup` 归一化辅助函数
  （逐行核对 `model/channel.go:296-305` `GetGroups()` 的归一化规则：去外层逗号、按逗号
  切分、每段去首尾空白；比上游那个函数多做一步——丢弃归一化后仍为空的段，上游自己的
  `GetGroups()` 不做这一步，`"a,,b"` 会被它解析出一个空字符串"分组"，这里选择清理掉）;
  `fetchChannelDirectory` 里接上 `Group: parseChannelGroup(item.Group)`。
- `fake.go`：新增 `fakeGroups` 查表（六条固定假渠道里五条给真实分组值，一条
  `ch-6` 故意不登记，覆盖"没有配置任何分组"的 nil 路径——不能让全部假数据都非空,
  那会让消费方看不到 null 分支要处理的真实情况）。
- 契约测试：`fakeChannelItems`（`client_contract_test.go`）三条渠道的 `group` 从统一的
  `"default"` 改成三种边界——渠道 1 多分组正常形态（`"default,vip"`）、渠道 2 空字符串
  （验证归一化后为空→`nil`，不是空字符串）、渠道 3 脏格式
  （`" default , vip, "`，首尾空白 + 尾随逗号 + 段内空白，验证归一化真的在生效、不是
  原样透传，且归一化结果与渠道 1 完全一致，互相印证）；三个渠道各自的子测试新增
  `Group` 断言；`TestFakeChannelCatalogFieldsSatisfyInvariants` 新增"必须同时覆盖 Group
  非 nil 与 nil 两条路径"的断言；`contracttest/suite.go` 的
  `AssertChannelStatusCatalogInvariants` 新增一条跨字段不变式——`Group` 非 nil 时不能是
  空白字符串（归一化后为空必须是 nil，不是空字符串）。

**Sub2API 完全没有改动**：`sub2api.ManagedChannel`/`catalogFields()` 不写 `group`
键，httpapi 侧按"这个观测行没有这个键"的既有路径自然解出 `nil`——与其它「这个平台
恒为 null」的维度（比如 NewAPI 侧的 `capacity`）走的是同一条既有机制，不需要专门为
Sub2API 写一行"总是 null"的代码。

### 2. 契约文档（`contracts/connectors/newapi.channel-catalog.v3.md`）

在原文件里新增一行字段表 + 一节 Follow-up，而不是另开 v4 文件——这份 v3 文档开篇
自己写明"是渠道目录字段的独立版本线，与 `ContractVersion` 接口版本无关，字段本身
可加"，`XM-CHAN-FIELDS0` 交付时已经确立"这是一个活的、持续累加的登记表"这个设计,
不是每加一个字段就分裂一个新文件。文档顶部加了一段说明这次是哪个切片在什么时候
按什么理由扩展的，保持可追溯。`sub2api.channel-catalog.v3.md` **没有改动**——那份
文档描述的是 Sub2API 连接器实际交付了什么，它没有交付 `group`，没有东西可记。

### 3. httpapi（`internal/platform/httpapi/platform_channels.go`）

`platformChannelRow` 新增 `Group *string \`json:"group"\``；`platformChannelCatalog`
新增 `group *string`；`catalogRowsForService` 新增 `c.group = toStringFromAny(row["group"])`；
`applyCatalog` 新增 `row.Group = c.group`。和其它 13 个既有 v3 字段完全同一套装配线,
没有引入任何新的解码路径或特殊分支。

### 4. 前端（`web/apps/admin-web`）

- `api/platformChannels.ts`：`PlatformChannelFieldsExtension` 新增
  `group: string | null`；`RawPage` 的原始行形状与 `parseFieldsExtension` 同步新增
  `group`/`item.group ?? null`——与其余 13 个字段同一个解析函数、同一条"关键子键缺失
  就是 null"纪律。
- `lib/channelFieldReasons.ts`：`FIELD_NULL_REASONS` 新增 `group` 键
  （`sub2api`："没有分组这个概念"；`newapi`："这条渠道没有配置任何分组"）——两个平台
  各自的真实原因，不是笼统的一句话。
- `components/PlatformOverviewPanel.tsx`（NewAPI 概览 → 渠道健康卡）：删除了本来
  局部、恒定返回"没有数据源"的 `NEWAPI_GROUP_NULL_REASON` 常量；"分组"列改成读
  `row.group`，非 null 显示真值，null 时用共享的
  `channelFieldNullReason("group", "newapi")`；没有 serviceId 的兜底旧实现
  （`LegacyNewApiChannelHealthCard`）那一格的原因文案从容易和"上游分组"（登记簿字段)
  混淆的"上游分组字段未接入"，改成与同一行"上游"/"成功率"两列一致的
  "没有 serviceId 可用……无法读取真实渠道目录"——三列缺的是同一个东西（真实渠道
  目录），不是三个不同的缺口。
- `pages/ChannelDetailPage.tsx`（渠道详情页）：「容量与调度」区块（这正是本仓库
  专门放 v3 目录字段的地方）里，紧跟在「容量 / 并发」之后新增一条「分组」
  Fact/UnavailableFact，读 `row.group`，null 时用
  `channelFieldNullReason("group", platform)`——Sub2API 的行会自然落到"没有分组这个
  概念"的诚实原因，不需要任何按平台分支的特殊代码。

**没有改动 `ManagedChannelTable.tsx`（合并后的渠道管理表）**：逐字核对过原型
`V["s2/upstream"]`（本仓库合并渠道管理表所依据的唯一原型来源）与已经落地的 07:20
精确规格（13 必需列 + 9 可选列），表里唯一带"分组"字样的列是「上游分组」
（`boundAccount(row)?.upstream_group`，来自绑定上游账号的登记簿，与本片接的 NewAPI
原生 `Channel.Group` 是完全不同的两个维度，前者两个平台通用、后者只有 NewAPI 有）。
原型没有为 NewAPI 单独画一个原生分组列，团队负责人的任务说明也明确"只有原型画了才
加，不画就不加"。因此本片只在概览卡与详情页接了真实分组，渠道管理表按任务指示
维持不变——这不是遗漏，是核对过原型之后的判断。

### 5. 顺带的文档修正（`docs/architecture/ADMIN-IA.md` §8.7）

「渠道健康」那一行描述仍停留在"上游/分组/成功率三列都没有数据源，都不画空列"——
这句话在 `XM-NEWAPI-OVERVIEW0` 把上游/成功率接上真实数据的时候就已经过期,
一直没人回头改；本片把分组也接上之后这句话彻底不对了。这一行原本就是本片直接
经手的同一行，顺手改成准确的当前状态，没有扩大到 §8.7 其余部分。

## files_changed

**连接器（commit 1）**：`connectors/newapi/contract.go`、`connectors/newapi/upstream.go`、
`connectors/newapi/fake.go`、`connectors/newapi/client_contract_test.go`、
`connectors/newapi/contracttest/suite.go`

**契约文档（commit 2）**：`contracts/connectors/newapi.channel-catalog.v3.md`

**httpapi（commit 3）**：`internal/platform/httpapi/platform_channels.go`、
`internal/platform/httpapi/platform_channels_test.go`

**前端（commit 4）**：`web/apps/admin-web/src/api/platformChannels.ts`、
`web/apps/admin-web/src/lib/channelFieldReasons.ts`、
`web/apps/admin-web/src/components/PlatformOverviewPanel.tsx` + `.test.tsx`、
`web/apps/admin-web/src/pages/ChannelDetailPage.tsx`、
`web/apps/admin-web/src/pages/SupplyDetailPages.test.tsx`

**文档修正（commit 5）**：`docs/architecture/ADMIN-IA.md`

## tests_run

后端（仓库根目录，代理变量已 unset：`HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy
https_proxy all_proxy NO_PROXY no_proxy`）：

- `go build ./...`：PASS（全仓，无输出）
- `go vet ./...`：PASS（全仓，无输出）
- `go test -p 1 -count=1 ./...`：67 个包，66 `ok`、1 `FAIL`
  （`connectors/metering` 的 `TestSub2APIClientSatisfiesContract`，报
  `unavailable: metering.capabilities`/`unavailable: metering.token.usage_read`）。
  单独重跑 `go test -p 1 -count=1 ./connectors/metering/...` 完全 PASS——环境性
  loopback httptest 抖动，与本片改动无关（本片零 `.go` 文件touch 过
  `connectors/metering`），按任务交接的既定判据记录并重跑确认，不是遗留失败。
- `gofmt -l ./connectors/newapi/ ./internal/platform/httpapi/`：本片改动的文件全部
  干净；`internal/platform/httpapi/finance_test.go` 有既有 drift 但本片未触碰该文件，
  不在本片改动范围内，未处理。
- 针对新增测试单独确认（非陈旧结果）：`connectors/newapi`、
  `internal/platform/httpapi` 两个包完整跑过，全绿。

前端（`web/` 目录，Windows worktree 用镜像脚本补齐 `node_modules`，命令带
`--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；未并发跑多个 pnpm 门禁）：

- `pnpm --config.verify-deps-before-run=false -r run typecheck`：PASS
  （5 个前端 workspace 包全部 `tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false -r run test`：PASS：
  design-tokens 10、ui-primitives 16、ui-admin 253、admin-web 1404，共 1683 个用例
  全绿（含本片新增/修改的用例，见下）
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`（在
  `web/apps/ui-storybook` 包目录内直接跑）：PASS，"Storybook build completed
  successfully"；本片未新增/修改任何 `ui-admin`/`ui-primitives` 组件，不需要新
  Storybook 素材
- `bash scripts/check-governance.sh`：PASS（exit 0，无输出；在最后一次文档改动
  之后又重跑一次确认）
- `gitleaks git --log-opts="-N"`（逐提交扫描）+
  `gitleaks git --log-opts="release/v0.1-launch..HEAD"`（全分支范围）：均
  "no leaks found"

新增/修改的测试覆盖：

- `connectors/newapi/client_contract_test.go`：`fakeChannelItems` 三条边界（正常
  多分组、空字符串→nil、脏格式→归一化）+ 对应的 `Group` 断言；
  `TestFakeChannelCatalogFieldsSatisfyInvariants` 新增"必须同时覆盖有/无分组"断言
- `connectors/newapi/contracttest/suite.go`：`AssertChannelStatusCatalogInvariants`
  新增"非 nil 时不能是空白字符串"不变式
- `internal/platform/httpapi/platform_channels_test.go`：
  `TestPlatformChannelsQueryExposesV3CatalogFields`/
  `TestPlatformChannelsQueryLeavesCatalogFieldsNilWhenAbsent` 各自新增 `group`
  的往返与"应为 null"断言
- `web/apps/admin-web/src/components/PlatformOverviewPanel.test.tsx`：真实分组值
  渲染 + null 时具体原因（不是笼统"没有采集这一维度"）两条既有用例的断言扩充
- `web/apps/admin-web/src/pages/SupplyDetailPages.test.tsx`：既有 3 条 Sub2API 用例
  按新增的第 9 个字段更新未接入计数（7→8、8→9）+ 标签清单；「字段一旦非 null」
  用例新增 `group` 断言；新增两条 NewAPI 专属用例——真实分组值出现在「容量与调度」
  区块且不与「上游分组」互相干扰、null 时显示 NewAPI 专属原因（不是 Sub2API 那条)

## 真实浏览器验证

手写 mock 后端（`http.createServer`，端口 18777，同一套"纯本地实测工具、不纳入
提交"惯例，延续 XM-CHAN-MERGE0/WIRE0/NEWAPI-OVERVIEW0）+
`node node_modules/vite/bin/vite.js --port 5199 --strictPort`
（`XM_DEV_API_TARGET` 指向 mock 后端）。三条 mock 渠道分别覆盖真实多分组
（`n-1: "default,vip"`）、真实单分组（`n-2: "default"`）、未配置分组
（`n-3: group=null`）。

- `/platforms/newapi?tab=overview`（开发模式登录后）——渠道健康卡「分组」列：
  `n-1`/`n-2` 两行分别显示真实值 `default,vip`/`default`；`n-3` 行显式「未接入」
  （无花括号占位、无空白）。截图
  `01-newapi-overview-group-column-real.png`。
- 渠道详情页 `/platforms/newapi/upstream/detail/n-1`——「容量与调度」区块新增的
  「分组」字段显示 `default,vip`；同一页顶部 StatTile「上游分组」与「渠道与映射」
  区块的「上游分组实际名」都显式「未接入」（这条渠道没有绑定上游账号，符合预期），
  用真实渲染证明了"分组"（NewAPI 原生）与"上游分组"（登记簿字段）是两个互不干扰
  的独立维度，不是同一个值被显示了两次。截图
  `02-newapi-detail-group-vs-upstream-group.png`。
- 渠道详情页 `/platforms/newapi/upstream/detail/n-3`——「分组」字段显式「未接入」,
  用 `evaluate_script` 读出 `<dt title>` 精确核对原因文案，与
  `channelFieldNullReason("group","newapi")` 的字面值完全一致：
  "这条渠道没有配置任何分组（上游 group 字段为空）"。截图
  `03-newapi-detail-group-null-reason.png`。
- 浏览器控制台在两次页面导航之间各出现过一次 `net::ERR_CONNECTION_TIMED_OUT`/
  `net::ERR_ABORTED`，复核 Network 面板确认是同一条 Query 在手写 mock 服务器
  （原生 `http.createServer`，没有做并发连接的健壮性处理）上偶发的重复并发请求,
  紧随其后的重试都是 200，最终渲染结果与预期一致——与前三片验证记录过的同一类
  本地 mock 局限（不是应用代码缺陷，真实 Go 后端不会有这个问题）。除此之外无 React
  层报错。
- 验证完毕后已确认性终止 mock 后端与 Vite 进程（分别监听 18777/5199 端口），未
  留下常驻进程；mock 脚本本身未纳入提交。

截图目录：`docs/evidence/screens/XM-CHAN-GROUP0/`

## not_run

- 未对真实 NewAPI 实例验证过——与既有连接器免责声明同一条：只有本地假上游的
  httptest 契约测试与手写 mock 浏览器验证覆盖过。真实凭据到位后，按契约文档
  Follow-ups 一节的提示核对：`group` 的 gorm 默认值是 `'default'`，真实实例上一条
  渠道的 `group` 是 null 大概率意味着这行数据早于该默认值生效（迁移遗留）或被人工
  清空过，而不是连接器的问题。
- 未跑数据库集成测试——本片未新增表/迁移/查询，`XM_TEST_DATABASE_URL` 相关测试
  路径未触碰。
- 移动端/窄视口截图：只测了 1440×1100 桌面视口，延续既有多个 handoff 未测这一项
  的做法。
- 未验证 `NormalizeChannelGroupFilter`/`ApplyChannelGroupFilter` 等上游自己用
  `group` 做请求过滤的机制——本片只读取上游已经返回的 `group` 字段值本身，不涉及
  用它去筛选上游的读取范围（那是完全不同的功能，不在任务范围内）。

## risks

- **分支基点与共享 `release/v0.1-launch` 引用不同步**（见上方"分支状态"一节）——
  已如实记录，未发现文件级冲突，是否需要合入前 rebase 留给验收线判断。
- **`group` 归一化的"丢弃空段"比上游自己的 `GetGroups()` 更严格**：上游遇到
  `"a,,b"` 会解析出三段（含一个空字符串），本连接器会清理成两段。这是刻意的选择
  （不把空字符串当"一个分组"展示出来），已在 `parseChannelGroup` 的 doc comment 与
  契约文档里写明，但如果未来有代码需要"跟上游 `GetGroups()` 完全一致"这个更强的
  保证（目前没有任何已知需求），需要知道这里有一处刻意的行为差异。
- **`group` 是路由/计费维度，不是健康或身份维度**——契约文档 Follow-ups 已经写明,
  本连接器不解释分组成员的业务含义（不会拿它去派生 `vendor`/`kind`），调用方也不
  应该这么做。
- 沿用既有的 NewAPI 连接器免责声明：未对真实实例验证过实际取值分布（见 not_run）。

## follow_ups

- 无已知的、需要另立切片处理的后续工作——本片是一个完整的端到端小闭环（连接器 →
  契约 → httpapi → 概览卡 → 详情页 → 测试 → 文档），不像 chanfields 那样为后续切片
  留下"占位、等真实字段"这类未完成状态。
- 如果运营侧后续需要按分组筛选/聚合渠道（比如"看某个分组下所有渠道的今日成本"),
  那是一个新的读契约/聚合需求，不是本片"暴露单条渠道的分组值"这个范围能覆盖的,
  需要另立评估。
