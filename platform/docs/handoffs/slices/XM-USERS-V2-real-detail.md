# XM-USERS-V2-real-detail · 平台用户详情页数据补全核实与翻页/缓存补齐

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-USERS-V2-real-detail`
- commit: `7eed858`
- base: `release/v0.1-launch`（`ca114fa`，含 XM-PAY1-finance-frontend 合并）
- worktree: `K:/星芒统一控制平台/wt-xmUSERSV2`

## 计划任务状态表（修正原派工说明）

派工说明认为 Task 6/7 尚未实现、需要本片补齐。核实代码与合入历史后，
实际状态如下（务必先读这一节再看后面的 summary）：

| Task | 内容 | 状态 | 依据 |
|---|---|---|---|
| 1 | UserRef / canonical codec / v2 核心类型 | **DONE**（合入历史） | `docs/handoffs/slices/XM-C-USER0-impl.md`；`connectors/platformusers/userref.go` 等文件已存在于 `release/v0.1-launch` |
| 2 | v2 Fake 与 capability contracttest | **DONE**（合入历史） | 同上；`connectors/platformusers/fake_v2.go` |
| 3 | GetUser Service / HTTP Query / B001 前端切换 | **DONE**（合入历史） | 同上；`internal/platform/platformusers/service.go`、`internal/platform/httpapi/users_detail.go` |
| 4 | Sub2API real GetUser v2 | **BLOCKED** | 缺 `SUB2_REAL_APPROVAL` 与脱敏证据；见下文"未实现原因" |
| 5 | NewAPI real GetUser v2 | **BLOCKED** | 缺 `NEWAPI_REAL_APPROVAL` 与脱敏证据；同上 |
| 6 | DailyUsage capability（七日趋势，Fake/core） | **已经是 DONE，本片开工前就已合入** | `docs/handoffs/ACCEPTANCE-LOG.md:6`（`2026-08-30T05:16Z MERGED b94ea5b XM-C-USER0-v2`）；`docs/handoffs/slices/XM-C-USER0-v2.md`；`connectors/platformusers/daily_usage.go`、`web/apps/admin-web/src/components/DailyUsagePanel.tsx` 已在 `release/v0.1-launch` 基线中 |
| 7 | Key metadata（`platform.user_keys.read`，Fake/core） | **已经是 DONE，本片开工前就已合入** | 同上；`connectors/platformusers/key_metadata.go`、`web/apps/admin-web/src/components/KeyMetadataPanel.tsx` 同基线 |
| 8 | reqlog 稳定 UserRef 面板 | **BLOCKED** | 缺 `REQLOG_USERREF_APPROVAL` 与 reqlog 真实证据；技术可行性见下文 |

派工说明的时间点判断有误：`XM-C-USER0-v2` 于 2026-08-30 05:16Z 合入
`release/v0.1-launch`，早于本次派工（2026-09-02）。Task 6/7 的
`DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL` 已在
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2（2026-08-30）记录为批准，
且 Fake/core 实现、HTTP 路由、前端面板均已完整落地并通过验收线部署验证
（该 handoff 的 `runtime evidence` 一节记录了真实浏览器截图与
`curl` 响应证据）。本片开工后第一件事就是核实这一点（见下）。

## 未实现原因：Task 4 / 5 / 8 为什么本片没有做

派工要求"实现剩余任务的完整增量（Fake + real client + contracttest +
Service + HTTP Query + 前端面板），逐字段核对本地上游源码并像
XM-CHAN-FIELDS0 一样写进契约文档"。逐一核实后：

- **Task 6/7 没有"real client"这一步可做**：spec 与 plan 原文都明确
  "Task 6/7 只做 Fake/core，不依赖 Task 4/5 文件"（plan 第 431/432 行），
  real Sub2API/NewAPI 的 DailyUsage/Key reader 属于*另外独立授权*的任务，
  根本不在 Task 6/7 范围内。核实 `connectors/platformusers/*.go`，
  只有 `FakeClient` 实现 `DailyUsageReader`/`KeyMetadataReader`
  （`grep "DailyUsageReader\|KeyMetadataReader"` 只命中
  `daily_usage.go:33`、`key_metadata.go:37` 两处接口声明本身），没有
  `sub2api_v2.go`/`newapi_v2.go` 文件——这正是 Fake/core 完整、real 有意
  留空的预期状态，不是缺口。
- **Task 4/5（Sub2API/NewAPI real GetUser v2）**：需要
  `SUB2_REAL_APPROVAL`/`NEWAPI_REAL_APPROVAL`（产品+安全对脱敏样本、实例
  版本单独签署）与 `docs/evidence/EV-2026-08-28-platformusers-{sub2api,
  newapi}-v2-shape.md` 两份证据文档。搜索 `docs/handoffs/`、
  `docs/evidence/` 全文，只找到 `DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL`
  两条批准记录（`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2），没有这
  两个 real 批准事件，`docs/evidence/` 目录下也没有这两份证据文件。
  spec 明确写"CORE_APPROVAL 通过只代表 core/Fake 可以实施，绝不构成
  SUB2_REAL_APPROVAL 或 NEWAPI_REAL_APPROVAL"、"批准文本必须明确写出事件
  名……不得被推定或继承"。这两项证据本身要求"脱敏的真实响应形状"与"人工
  已完成 AdminComplianceGuard 的记录"，只读源码给不出，需要真实实例访问
  ——而这正是任务边界明确禁止我做的事（不接触服务器、不接触上游实例）。
  因此本片没有实现，也没有伪造批准文本去绕过这道闸。
- **Task 8（reqlog 稳定 UserRef）**：同理需要 `REQLOG_USERREF_APPROVAL`
  与 reqlog 真实证据，两者均不存在，故未实现。但派工说明把它标注为
  "if feasible"，所以额外做了一次只读技术可行性核实（不涉及任何批准，
  纯读源码）：

  `cmd/reqlog-recorder/tokenmap.go`（现有、已合入的 token map 刷新机制，
  第 46～63 行）对两个上游数据库执行的 SQL 是
  `select concat('sk-', left(t.key,17)), u.username from tokens t
  join users u on u.id=t.user_id`（NewAPI）与类似的 Sub2API 查询——
  都 JOIN 了 `u.id`，但只 SELECT 了 `u.username`/`email`，`u.id` 被
  丢弃。`connectors/reqlog/file_client.go` 的 `resolveUsername`
  （约第 527 行）消费的 `tokenMap` 类型是 `map[string]string`
  （前缀→用户名），只用于解出 `RequestLogSummary.Username`。

  技术判断：把这条链路从"前缀→用户名"改造成"前缀→(platform,
  source_user_id)"，从 SQL 层面看只是多 SELECT 一列、把 map 值从
  `string` 换成小结构体，改动量不大，**看起来可行**。但这条 token map
  刷新链路本身是通过 `docker exec ... psql` 直接连上游 Postgres 容器
  执行 SQL，完全绕开了平台的 Connector/CredentialRef 与四道只读闸
  （`tokenmap.go` 第 35～42 行注释自己承认这是"已知风险，本次收编原样
  保留、不重新设计"，`docs/handoffs/slices/XM-REQLOG-MERGE.md` 的风险
  清单同样记录了这一条）。Task 8 的证据门要求"受信映射"能给出
  platform + source user ID——把一条已经被标记为架构债务、绕过
  Connector 体系的直连 SQL 路径，进一步升级成*承载稳定身份关联*的数据源，
  这件事本身值得在准备 `REQLOG_USERREF_APPROVAL` 证据包时单独评估，而不
  应该被当成"批一下就能做"的小事。这条发现留在 follow_ups，供后续准备
  该审批证据的人参考。

## 本片实际做的事

既然 Task 6/7 在开工前就已完整交付、Task 4/5/8 都硬阻塞在缺失的人工批准
与真实证据上（不可由 AI 自行补齐或绕过——宪法第 9、10、18、20 条），
派工说明里唯一仍可继续推进的是第 3 点"前端"里的用户清单响应缓存与翻页：

- **缓存**：`web/apps/admin-web/src/main.tsx` 已经给全局 `QueryClient`
  设了 `staleTime: 15_000`，覆盖应用内所有 Query（含用户清单/详情/
  DailyUsage/Key 面板）；这部分在本片开工前就已生效，不是缺口。
- **翻页缺口是真的**：`PlatformUsersPanel` 原来只用 `useQuery` 取第一页
  （固定 50 条），"还有更多"只是一句静态文案（`还有更多（排序在服务端，
  翻页随第 6 片补齐）`），没有任何控件能真正翻到第二页，即使
  `listPlatformUsers` 的 API 客户端早就支持 `cursor` 参数。这是一处遗留
  TODO（"第 6 片"指的是 B001 时代的旧编号，与本计划的 Task 6 无关），
  与本仓库内其他清单页（`Sub2ApiOrdersPanel`、`RequestsPanel` 等）已经
  采用的 `useInfiniteQuery` + `DataTableV2` 的 `footerExtra` "加载更多"
  按钮模式不一致。

  补齐方式：把 `PlatformUsersPanel` 从 `useQuery` 改成
  `useInfiniteQuery`（`initialPageParam: ""`、
  `getNextPageParam: (lastPage) => lastPage.next_cursor || undefined`，
  与 `Sub2ApiOrdersPanel.useSub2ApiOrdersQuery` 逐字同构），显式声明
  `staleTime: 15_000`（与全局默认一致，但对多页缓存条目单独写一遍，
  不依赖隐式继承），把已加载的每一页 `flatMap` 成表格行，顶部四格与新鲜度
  取最后一次成功响应（这些字段本来就是"全体用户"口径的聚合，不随游标
  变化）。`DataTableV2` 的 `footerExtra` 插槽放一个"加载更多"按钮，
  与 `Sub2ApiOrdersPanel`/`RequestsPanel` 现有实现同一套 class 名与
  文案（`加载中…`/`加载更多`）。"需关注"格（NewAPI 原型第四格）原来的
  说明文案"本页 N 条"在翻页后会变得不准确（它只统计最后一次响应，不是
  已加载的全部行），改成"已加载 N 条"并统计所有已加载页，保持诚实。

  没有改动 `KeyMetadataPanel`——它在 Task 7 就已经用同一套
  `useInfiniteQuery` + 加载更多模式，不在本片范围内。

## files_changed

- `web/apps/admin-web/src/components/PlatformUsersPanel.tsx`
- `web/apps/admin-web/src/components/PlatformUsersPanel.test.tsx`

## tests_run

以下命令均在 `K:/星芒统一控制平台/wt-xmUSERSV2` 内串行执行（Windows
worktree 用 `link-node-modules.mjs` 镜像脚本补齐 `node_modules`；命令带
`--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；未并发跑多个
pnpm 门禁）：

- `go build ./...` — PASS（无输出）
- `go vet ./...` — PASS（无输出）
- `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test -p 1 -count=1 ./...` — PASS（47 个含测试的包全部 `ok`；`XM_TEST_DATABASE_URL`/`XM_RUN_INTEGRATION` 均未设置，DB 集成测试按设计自跳过，本片未连接任何数据库）
- `pnpm --config.verify-deps-before-run=false -r run typecheck` — PASS（5 个前端 workspace 包 `tsc --noEmit` 全部 Done）
- `pnpm --config.verify-deps-before-run=false -r run test` — PASS：design-tokens 10、ui-primitives 16、ui-admin 253、admin-web 96 files / 1386 tests（含本片新增 4 个用例）；ui-storybook 无单测（构建即验证）
- `pnpm --config.verify-deps-before-run=false run build`（`web/apps/ui-storybook` 目录内执行）— PASS（"Storybook build completed successfully"；既有大 chunk 警告，与本片无关）
- `bash scripts/check-governance.sh` — PASS（exit 0，无输出）
- `gitleaks git --redact --no-banner --log-opts="release/v0.1-launch..HEAD"` — PASS（1 commit scanned，no leaks found）
- `git diff --check` — PASS

## not_run

- 所有 DB 集成测试（`internal/platform/*/​*_integration_test.go` 等）：
  本片未改动任何 Go/DB 代码，未设置 `XM_TEST_DATABASE_URL`/
  `XM_RUN_INTEGRATION`，未在 `xingmang-launch-postgres-1` 或任何其他
  Postgres 上建 scratch 库。
- 未部署、未访问任何服务器或真实 Sub2API/NewAPI/CPA 实例；浏览器验证
  用的是本机 Vite dev server + 手写 mock 后端（见下），不是 Go
  `platform-api` 也不是共享 staging 栈。

## 真实浏览器验证

方法与 `XM-CHAN-MERGE0` 交接文档记录的方式相同：不起 Go 后端、不连
Postgres，直接 `node node_modules/vite/bin/vite.js --port 5180
--strictPort`（`web/apps/admin-web` 目录，`XM_DEV_API_TARGET` 指向本机
手写 mock 后端）+ 一份手写的最小 mock HTTP 服务器，只为验证"翻页/缓存/
DailyUsage/Key 面板渲染"这几件事，返回固定 JSON（形状对齐
`PlatformUserPage`/`PlatformUserDetailResponse`/`DailyUsageSeriesBody`/
`KeyMetadataPageBody`）。mock 脚本与截图均不含任何真实凭据、真实用户或
生产数据。

验证内容：

1. Sub2API 用户清单第一页（3 条）正确渲染：四格聚合、覆盖率下界提示、
   服务端排序/状态筛选控件、表格列与详情深链全部对齐既有原型。
2. 点击"加载更多"：URL 带上第一页返回的 `cursor=c2`；第二页 2 条
   （王芳/陈杰）**追加**到表格而不是替换第一页的 3 条，"已加载"与
   `DataTableV2` 自带的"共 N 条"页脚都从 3 更新到 5；按钮在游标耗尽
   （第二页 `next_cursor` 为空）后消失。
3. 点进用户详情页（张伟，`u-755f3130323431`）：可用余额/区间充值/
   区间消费/近 30 天消费、客户类型与注册时间诚实"未接入"、"近 7 天消费
   趋势"（覆盖 6/7 天，缺失的 2026-08-28 显示"—/—/未知"而不是 0）、
   API Key 页签（3 条前缀/状态/创建/最近使用/今日峰值，无完整 Key）全部
   正确渲染，`FreshnessBadge`/`FreshnessNote`/Fake 演示横幅均可见。
4. 从详情页点"返回用户管理"回到清单：**同一次点击的响应快照里已经是
   完整的 5 行数据**，没有加载态——缓存命中、导航回来是瞬时的。用 Network
   面板核实：这次导航之后确实又发出了一次后台重新验证请求，但那是因为
   本轮浏览器交互（截图、多次 snapshot、多次工具调用往返）实际耗时已经
   超过 15 秒 `staleTime` 窗口，是预期内的 stale-while-revalidate 单次
   后台刷新，不是"回退必然重新拉一遍"的刷屏。
5. NewAPI 用户清单：列集与顶部四格按 NewAPI 自己的原型渲染（没有"区间
   充值"列，第四格是"需关注"，文案已改成"已加载 2 条里状态不是「正常」
   的"），不是 Sub2API 页面的并集裁剪。
6. 390px 宽度下确认清单页本身没有引入新的横向溢出：`document.
   documentElement.scrollWidth` 在完全不相关的 `/dashboard` 路由上也是
   同样的 720px（对 485px 视口），证明这是既有的、已被计划文档记录过的
   `AdminShell` 布局级溢出（"既有 AdminShell overflow 单独记录"），不是
   本片引入的；`DataTableV2` 自己的表格容器已经有正确的
   `overflow-x-auto` 包裹。
7. 两个平台整个流程中浏览器 Console 无 error/warning。

截图（`docs/evidence/screens/XM-USERS-V2/`）：

- `01-sub2api-users-list-page1-1440.png` — 第一页，1440px
- `02-sub2api-users-list-after-load-more-1440.png` — 加载更多后 5 条，1440px
- `03-sub2api-user-detail-daily-usage-keys-1440.png` — 详情页七日趋势 + API Key 页签，1440px
- `04-newapi-users-list-1440.png` — NewAPI 清单（平台专属列/格），1440px
- `05-sub2api-users-list-390.png` — 390px 宽度

## risks

- 初次挂载时 Network 面板看到用户清单第一页请求被发了两次（均 200，无
  abort）：这是 React 19 `StrictMode` 在开发模式下对副作用的双调用，叠加
  mock 服务器近零延迟（没有人工延迟）导致两次都赶在 abort 生效前完成
  ——同一次页面加载里 `/api/v1/services`（本片未改动的既有 `useQuery`）
  也是同样的双请求模式，说明这是全局性、开发态限定的现象，不是本片改动
  引入的回归，生产构建不会有 `StrictMode` 的双调用。
- 本片改动范围比派工说明预期的窄：因为 Task 6/7 早已交付、Task 4/5/8
  硬阻塞在缺失的审批与证据上，净变更只有前端翻页/缓存这一处既有缺口的
  补齐，没有新增任何后端能力或真实字段映射——因此本片没有"逐字段核对
  上游源码并写进契约文档"的内容可交（没有新字段可核对）。
- 未改动 `KeyMetadataPanel`/`DailyUsagePanel`：两者在 Task 7/6 就已经是
  `useInfiniteQuery`/成熟实现，派工的翻页/缓存诉求点名的是"用户清单"，
  不包含这两个已完成的面板。

## follow_ups

- Task 4/5（Sub2API/NewAPI real GetUser v2）：需要产品+安全出具
  `SUB2_REAL_APPROVAL`/`NEWAPI_REAL_APPROVAL`，并在
  `docs/evidence/EV-2026-08-28-platformusers-{sub2api,newapi}-v2-shape.md`
  落地脱敏真实样本、实例版本、AdminComplianceGuard 确认记录；这些证据
  需要真实实例访问，不是本任务边界内可以完成的事。
- Task 8（reqlog 稳定 UserRef）：需要 `REQLOG_USERREF_APPROVAL` 与 reqlog
  真实证据；额外建议：准备该证据包时一并评估
  `cmd/reqlog-recorder/tokenmap.go` 现有的 `docker exec ... psql` 直连
  上游数据库机制（绕开 Connector/CredentialRef 与四道只读闸的已知风险，
  见 `docs/handoffs/slices/XM-REQLOG-MERGE.md` 风险清单）是否适合继续
  承载"稳定身份关联"这一更高信任等级的数据，还是需要先立 ADR/Change
  Request 把这条链路收编进 Connector 体系。
- 若未来某片改动 `KeyMetadataPanel`/`DailyUsagePanel`，可以考虑比照本片
  给它们的 `useInfiniteQuery` 也显式写一遍 `staleTime`，与用户清单的写法
  保持一致（目前两者依赖全局默认，行为一致但不够自文档化）。
