# XM-SERVER0：服务器登记簿（纯记录，不装 Agent）

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-SERVER0`（base 见分支历史，工作树 `acceptance/wt-server0`）

## commit

`a7a7bbb` — feat(server): 服务器登记簿（XM-SERVER0，纯记录，不装 Agent）

## summary

用户拍板原话：「服务器只做记录好了」。侧栏「平台 → 服务器」原来的 7
页签是 UI 蓝图态（`ServerDetailPage.tsx` 与 `blueprints/server.ts` 的字段
清单已经定稿，但一直没有后端）。本片把其中 4 格（服务器资产、供应商与
采购、域名与证书、服务与容器）接成真实可写的登记簿；监控与告警、连接
与凭据两格**保持「未接入」**并在蓝图文案里写明这是 2026-08-31 的拍板
结论，不是遗漏。

### 后端：迁移 000023 + 新包 `internal/platform/server`

四张纯登记表，字段清单逐字对照 `ServerDetailPage.tsx` 的蓝图态与
`docs/architecture/ADMIN-IA.md` §2.1：

- `core.server_asset`：主机名、IP（jsonb 数组，可多个）、机房、供应商
  （FK → server_supplier，`ON DELETE RESTRICT`）、规格（vCPU/内存GB/
  磁盘GB）、用途、状态枚举（`active`/`retired`/`planned`——登记簿口径，
  不是心跳判定）、月付成本（整数最小单位，按币种自然标度，与币种成对
  出现的 CHECK）、计费周期、到期日、备注、environment、审计字段。
- `core.server_supplier`：名称、官网、控制台地址、联系人、联系方式、
  备注。只存联系方式，不存密码——见下方 follow_ups。
- `core.server_domain`：域名、注册商、DNS 托管、到期日、证书来源枚举
  （`acme`/`managed`/`manual`）、证书到期日、绑定服务备注（自由文本，
  非外键——服务与容器目前也是手工表，两边没有能互相引用的稳定主键）。
- `core.server_service_note`：服务器 id（FK → server_asset，
  `ON DELETE RESTRICT`）、服务名、类型枚举（`container`/`systemd`/
  `process`）、端口、备注。**不重复存 environment 列**——同
  `finance.token_map` 挂在 `upstream_account` 下的形状，环境判定经父行
  （`server.resolveOwningAsset`）。

六个 L1 Action（`internal/platform/server/actions.go`）：
`server.asset.set@1`（整行替换，`asset_id` 留空=新登记）、
`server.asset.retire@1`（独立的状态迁移，只改 `status`，`reason` 必填，
供列表页一键退役不必打开整表单）、`server.supplier.set@1`、
`server.domain.set@1`、`server.service_note.set@1`（新登记分支必须先
经 `resolveOwningAsset` 读一次父资产、校验存在且同环境）、
`server.service_note.remove@1`。全部 `Permission: server.manage`、
`PrincipalTypes: [HUMAN]`、`Environments` 三环境全开，不接受
`environment` 参数（取自调用者身份，宪法 15 条）。

权限：

- 新 scope `server.manage`（`internal/platform/server/permissions.go`）
  只授写。`oidcauth.DefaultRoleScopeMap` 的 `admin` 角色新增这一项；
  `resolver_test.go` 的 `TestDefaultRoleScopeMapIsConservative` 新增
  断言并写明理由——**与 `finance.platform_channel_binding.manage`
  刻意排除在默认角色外不是同一类**：渠道绑定写错会让成本记到错误渠道
  且不报错，属于设计稿裁定必须人工显式授予的高风险面；服务器登记簿写
  的是主机名/规格/供应商/到期日这类纯记录字段，不触碰第三方系统、不
  影响任何成本或收入归属，与已经在默认表里的
  `finance.upstream_account.manage` 同一档（登记簿，不是执行）。
- 读侧**没有新建 scope**：`GET /api/v1/servers/assets|suppliers|
  domains|service-notes` 复用既有 `registry.read`——能看服务清单的人
  本就该能看服务器登记簿，两者是同一类知识面。

金额口径：月付成本是**整数最小单位、按币种自身的自然标度**（USD/CNY 是
分，JPY/KRW/VND 无小数位），不是财务登记簿（`internal/platform/
finance`）用的 scale-6 微单位口径——两者服务不同目的（一个是简单月度
账单记录，一个是要支撑逐笔核算与影子对比的成本引擎），刻意不复用同一
套标度换算，Action 层（`optionalMinorParam`）只接受**纯整数字符串**，
拒绝带小数点或科学计数法的输入（同 `finance.subscription_actions.go`
的 `minorAmountPattern` 纪律）。金额与币种必须成对出现（都填或都空），
0 是合法值（免费/试用）区别于「未登记」（`*int64` 指针语义）。

日期字段（`expires_at`/`cert_expires_at`）用 `2006-01-02` 格式的自然日
字符串，库层是 `date` 类型不是 `timestamptz`——到期日是业务日期不是
时间点。

### httpapi 层

`internal/platform/httpapi/server_registry.go`：四个只读 Query
handler，`resolveEnvironment(r, p)` 同 `/services`/`/finance/
upstream-accounts` 的模式（可选 `?environment=`，缺省取调用者身份）。
`router.go` 新增 `Deps.ServerAssets/Suppliers/Domains/ServiceNotes`
四个最小接口字段，`cmd/platform-api/main.go` 用同一个
`server.NewStore(pool)` 同时喂读写两侧（同财务登记簿的装配方式）。

### 前端

`web/apps/admin-web/src/pages/PlatformDetailPage.tsx` 的 `tabContent()`
switch 新增/改动：

- `case "overview"`：`spec.serviceType === "server"` 时走
  `ServerOverviewPanel`，**必须排在** `platformHasPrototypeOverview`
  判断之前——顺序反了会先落到 sub2api/newapi 那条路。
- `case "suppliers"`：先判 `platformHasUpstreamRegistry`（sub2api/
  newapi 的成本登记簿），不匹配再判 `serviceType === "server"` 走
  `ServerSuppliersPanel`，两者都不匹配才落蓝图——`suppliers` 这个
  tab.value 现在有三种语义，按既有教训（memory：「一个 tab.value 两种
  语义」）逐条判完才能兜底。
- 新增 `case "assets"`/`"domains"`/`"services"`：服务器专属 tab.value，
  仍显式判 `serviceType === "server"` 而不是无条件接管（防将来复用）。
- `monitoring`/`creds` 未加 case，继续落 `default` → 蓝图占位
  （`blueprints/server.ts` 两格的 `source` 文案已改写，明确写「拍板
  结论（2026-08-31）」而不是「随 M2 上线」）。

五个新组件（`web/apps/admin-web/src/components/Server*.tsx`）：
`ServerOverviewPanel`（台数/按币种分别合计的月成本/30 天内到期的
服务器+域名+证书数，已退役资产不计入成本合计）、`ServerAssetsPanel`
（含退役 Dialog）、`ServerSuppliersPanel`（关联服务器台数在内存里按
`supplier_id` 分组）、`ServerDomainsPanel`、`ServerServiceNotesPanel`
（含删除 Dialog，没有任何资产时新登记按钮禁用并给出理由）。到期日
30 天内（**含已过期**，`isExpiringSoon` 判定不只看未来）标 `warning`
色徽章，统计口径与表格标色共用同一个 `EXPIRY_WARNING_DAYS=30` 常量
（`lib/serverRegistryForm.ts`），避免两处漂开。

金额输入：表单收十进制文本（如 `99.90`），`lib/serverRegistryForm.ts`
的 `parseDecimalToMinorUnits` 按 `lib/money.ts` 既有的
`currencyExponent()` 换算成整数最小单位字符串再发给 Action——前端不
把「已经换算好的整数」这个负担甩给操作员去心算。

`api/server.ts`：四个 Query 客户端 + 六个 Action 包装函数，条目形状
保持后端 snake_case 原样（同 `UpstreamAccountItem` 的选择，理由见
文件顶部注释：只有一个消费者，不必建映射层）。

## files_changed

后端：

- `db/migrations/000023_server_registry.{up,down}.sql`（新增；验收整合时因
  ACTIONS0 已占用 000022 而顺延）
- `db/queries/server.sql`（新增）
- `sqlc.yaml`（新增一个 gen 输出块）
- `internal/platform/server/`（新包：`doc.go`、`types.go`、
  `permissions.go`、`store.go`、`actions.go`、`gen/`，及四个测试文件）
- `internal/platform/httpapi/server_registry.go`（新增）+
  `server_registry_test.go`（新增）
- `internal/platform/httpapi/router.go`（新增 4 个 Deps 字段与 4 条路由）
- `cmd/platform-api/main.go`（注册 Action、装配 Deps）
- `internal/platform/oidcauth/rolemap.go`（admin 加 `server.manage`）
- `internal/platform/oidcauth/resolver_test.go`（新增断言）
- `contracts/actions/server.{asset.set,asset.retire,supplier.set,
  domain.set,service_note.set,service_note.remove}.v1.json`（新增，
  6 个 Action 契约文档）

前端：

- `web/apps/admin-web/src/api/server.ts`（新增）
- `web/apps/admin-web/src/lib/serverRegistryForm.ts` +
  `serverRegistryForm.test.ts`（新增）
- `web/apps/admin-web/src/components/Server{Overview,Assets,
  Suppliers,Domains,ServiceNotes}Panel.tsx` 及各自 `.test.tsx`
  （新增，10 个文件）
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`
  （`tabContent()` 新增/改动 5 个 case）
- `web/apps/admin-web/src/blueprints/server.ts`（`monitoring`/`creds`
  两格 `source` 文案改写）
- `web/apps/admin-web/src/router.test.tsx`（新增 `/api/v1/servers/*`
  的默认空响应兜底；更新两条被本片语义改变的既有断言——见下方说明）

### 关于 `router.test.tsx` 的两条既有断言

`XM-0052` 曾为「`suppliers` 在服务器上仍是采购蓝图」写过回归测试，
断言蓝图占位文案还在、没被 sub2api 的成本登记簿悄悄接管。本片把这一格
**真的接成了登记簿**，那条断言的前提被推翻，已改写成断言真实面板内容
（「登记供应商」按钮）而不是旧蓝图文案；`overview` 同理，`"折算月成本"`
（蓝图态的静态文案）换成 `"月成本合计"`（`ServerOverviewPanel` 的真实
Tile 标签）。两条测试保留的是原本要防的事——不被别的平台的面板悄悄
接管——只是判据从「蓝图文案还在」换成了「真实面板内容在」。

## tests_run

后端（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy
-u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 前缀绕开本机代理
TUN 对 httptest 的干扰）：

- `go build ./...` —— PASS
- `go vet ./...` —— PASS
- `go test -p 1 -count=1 ./...` —— PASS（全仓库，含本片新增的
  `internal/platform/server`、`internal/platform/httpapi` 新用例；
  未见既有包因本片改动回归）
- `"$(go env GOROOT)/bin/gofmt" -l <本片改动的每个 .go 文件>` ——
  干净（0 输出），未对整仓库跑 `gofmt -l .`（本机已知的 CRLF 假警报，
  见 windows-toolchain-quirks 记忆）
- **真库集成测试**（`internal/platform/server/store_integration_test.go`
  + `actions_integration_test.go`）在 scratch 数据库上实际跑通：
  `docker exec xingmang-launch-postgres-1` 建库 `xm_scratch_server0`，
  依次灌 000001~000022 全部正向迁移（作者原始分支中本片编号为 000022），
  验证新表在真实 schema 上能干净应用；验收整合已将本片顺延为 000023，
  最终候选会重新验证 000001~000023。随后建一次性 `xm_test_server0` 角色并
  `GRANT xingmang`，`docker run --network container:xingmang-launch-
  postgres-1 golang:1.27` 跑 `go test -p 1`，PASS。覆盖：四张表的
  CRUD 往返、jsonb IP 数组精度、date 类型到期日往返、供应商/资产/
  服务备注三处外键 `RESTRICT` 生效、四处唯一索引生效、跨环境闸门拒绝、
  `service_note.set` 新登记分支解析未知 `server_id` 的错误路径。
  跑完已 `DROP DATABASE`/`DROP ROLE` 清理，未留痕迹。
  **验收整合复验（替代上述违规共享容器做法）**：使用仓库钉 digest 的
  PostgreSQL 18 compose fixture，随机 project `xm-server0-646d74c4d62a`、
  回环随机端口与独立 volume；完整 migration `up`（含 ACTIONS0 000022 与
  SERVER0 000023）PASS，`go test -p 1 -count=1 -v ./internal/platform/server`
  PASS，四张 `core.server_*` 表计数为 4；finally 已删除本次容器、网络、volume
  与临时口令文件，未连接共享/staging/production 数据库。
  **这一步过程中发现并修好了 3 处测试自身的 bug**（`store_integration_
  test.go` 里直接调 Store 方法时漏传 `Environment`/`ServerID`
  这两个「不进 SQL SET 子句但 Validate 仍要求」的字段——真实 Action
  Handler 路径本就会正确带上，问题只在测试直接绕过 Handler 调 Store），
  不是实现缺陷。
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks protect --staged -v`（本机已装 gitleaks 二进制，未走
  docker）—— PASS（`no leaks found`）

前端（`web/` 目录，`--config.verify-deps-before-run=false` 跳过
pnpm 依赖校验，node_modules 已由团队预先镜像好）：

- `pnpm --filter admin-web run typecheck` —— PASS（`tsc --noEmit`
  无输出）
- `pnpm --filter admin-web run test` —— PASS，85 个测试文件、1177 个
  用例全绿（含本片新增 5 个组件测试文件 + 1 个纯函数测试文件，共 43
  条新用例；`router.test.tsx` 的两条既有断言按上方说明更新）
- `pnpm --filter admin-web run build` —— PASS（`tsc --noEmit &&
  vite build`；946 KB 的 chunk-size 警告是既有情况，非本片引入的
  回归——本片 5 个新组件加起来只有几十 KB）

**过程中发现并修好一处真实的 a11y 回归**：`ServerAssetRetireDialog`
（「退役原因」）与 `ServerServiceNoteRemoveDialog`（「删除原因」）
两个确认弹窗最初漏了给 `FormField`/`Input` 配对 `htmlFor`/`id`——
标签与输入框没有程序化关联，屏幕阅读器读不出这个字段是什么。
写组件测试时 `getByLabelText` 直接查不到元素，当场发现当场改；
已在 `ServerAssetsPanel.test.tsx`/`ServerServiceNotesPanel.test.tsx`
留了会失败的用例钉住这个修复。

## not_run

- **Storybook 构建**（`pnpm --filter ui-storybook run build`）：未跑。
  本片没有新增/修改任何 `ui-admin`/`ui-primitives` 组件，五个新面板
  全部复用既有的 `DataTableV2`/`Dialog`/`FormField`/`Badge`/`StatTile`
  等既有能力，无需补 Storybook 素材。
- **真实浏览器 / Playwright 实测**：未跑。vitest + jsdom 已经覆盖了
  列表渲染、Dialog 表单校验、Action 提交与回执展示这几类行为；本片
  没有涉及横向滚动/粘性列/复杂布局这类只有真浏览器布局引擎才测得出的
  东西（DataTableV2 本身已经在别处测过），按 windows-toolchain-quirks
  记忆的判断标准不属于「必须真机」的那类。
- **`dbroles` DB 角色治理**：**尝试过，主动撤销了**——见下方 risks。
- **生产/staging 实际部署验证**：未跑，交给验收线合入后核实（含
  `pnpm install` 之外，还要确认 `go run ./cmd/migrate up` 能在真实
  部署环境把 000023 迁移应用上）。
- **contracts/actions 下 6 个 JSON 契约文件的机器校验**：本仓库目前
  没有代码读取/校验 `contracts/actions/*.json`（已确认，纯文档性质，
  仅供人工核对），只手工过了一遍 JSON 语法（`python3 -c "json.load(...)"`
  逐个 PASS），未做进一步校验。

## risks

- **`internal/platform/dbroles` 的角色-表授权治理未同步更新，是刻意
  撤销的**：起初往 `policy.go` 的 `defaultObjects()`/`tableColumns()`
  加了四张新表的 `xm_api_runtime` 授权项（比照 `core.connector`/
  `core.connection` 的模式），但这触发 `TestCheckedInPolicyContractLoads`
  失败——`contracts/database/role-policy.v1.json` 是一份**独立维护、
  经人工批准的**快照契约（带 `$schema`/版本号，`policy_test.go` 会把
  它与 Go 代码算出的「approved v1 inventory」逐项比对，缺项直接判定
  `OBJECT_MISSING`）。这条治理机制本身就是在拦「改了库权限范围但没走
  审批」这类变更——继续往前推等于绕过它。已经 `git checkout --` 撤销
  了对 `policy.go` 的改动，`dbroles` 目录**在本片里完全未改动**。
  **影响**：如果生产/staging 数据库确实按细粒度能力角色
  （`xm_api_runtime` 等）而不是宽泛超级用户连接（本仓库过往
  DBR0/DBR1 两个独立切片专门做这件事，说明这条基础设施是真实存在的），
  那么在那种部署形态下，`server.*` 四张新表**目前没有被这份角色策略
  覆盖**，`xm_api_runtime` 角色可能因为不在批准的对象清单里而拿不到
  预期的 SELECT/INSERT/UPDATE/DELETE 授权，实际读写会报权限错误。
  本机快速核对：`docker exec xingmang-launch-postgres-1 psql ... \du`
  没去查该库当前实际用的连接身份是不是这类细粒度角色，留给验收线
  确认——如果生产走的是这套角色体系，需要另开一个专门的 dbroles 切片
  把这四张表补进 `contracts/database/role-policy.v1.json`（连同它的
  批准流程，不该在这片里顺带做掉）。验收整合复核：当前 release 的
  `deploy/compose/launch.yaml` 仍以 `${POSTGRES_USER:-xingmang}` 给 API/worker
  组装 DSN，DBR3 runtime cutover 未合入且未获 `APPROVED DBR3 LOCAL-CUTOVER`；
  因而本次部署不会把运行身份切到 `xm_api_runtime`，此缺口不阻塞当前
  owner-role 栈。但在任何 DBR3/受限角色切换前，它是必须先关闭的硬门禁。
- **供应商/服务器登录凭据仍是自由文本，不是 CredentialRef**：
  `server_supplier` 目前只有 `contact_info`（联系方式）这类自由文本
  字段，购买账号密码、SSH 私钥、sudo 凭据都还没有专门的登记入口——
  `creds`（连接与凭据）页签因此保持「未接入」。真要登记这些，字段设计
  上必须走 `secret://<scope>/<name>` 的 CredentialRef（宪法 7 条），
  不能直接加一个文本框，这是刻意留白不是漏做。
- **`bound_service_note`（绑定服务备注）是自由文本，不是真正的外键
  关联**：域名与「服务与容器」两张表目前都没有能互相引用的稳定主键
  （服务名可能跨服务器重复），勉强建外键换不来真实的引用完整性；
  如果后续要做「点这个域名跳到对应的服务登记」这类导航，需要先给
  服务与容器表补一个跨服务器唯一的自然键或者显式加一个可选 FK 列。
- **月付成本没有历史/审计层面的"这个月多少钱"聚合视图**：概览页的
  "月成本合计"是**当前登记状态的即时快照**（把所有非退役资产的
  `monthly_cost_minor_units` 按币种加总），不是任何时间点的历史值；
  如果未来需要"上个月我们花了多少钱在服务器上"这类回溯问题，现在的
  设计答不出来（这与拍板"只做记录"的范围一致，只是标注出来避免
  被误用成财务口径）。
- **`sqlc generate` 曾顺带发现更早迁移（000019~21）的既有生成漂移**
  （`OpsMetricObservationSample` 新增列、`CoreStaffAccount`/
  `CoreCredentialRef`/`CoreConnectorConfig` 等类型）。作者原始分支为了不
  扩片曾撤销这些生成结果；验收整合的最终全门禁要求生成零漂移，因此已用
  钉定的 sqlc v1.31.1 全量重生成 9 个既有 `gen/*.go` 文件，同时纳入
  000023 的 `CoreServer*` 模型。连续第二次 `go tool sqlc generate` 退出码 0
  且 9 文件 SHA256 均未变化；这项 08:15 待办已关闭，不再作为 follow-up。

## follow_ups

1. **`dbroles` 角色策略补丁**（见上方 risks 第一条）：如果生产走细粒度
   DB 角色，需要一个专门切片把 `core.server_asset/supplier/domain/
   service_note` 四张表加进 `contracts/database/role-policy.v1.json`
   并走它自己的批准流程。
2. **服务器登录/购买凭据的 CredentialRef 化**：`creds`（连接与凭据）
   页签的真正实现——供应商门户账号、SSH、sudo 凭据登记，全部经
   `secret://` 引用，复用 `internal/platform/credentials` 或
   `internal/platform/secrets` 的既有基础设施，不新造一套。
3. **域名 ↔ 服务与容器的真实关联**（见上方 risks 第三条），如果产品侧
   确认需要这个导航能力。
4. **M2 Server Agent 上线后**：`ServerDetailPage.tsx`（资产详情蓝图页）
   与 `monitoring` 页签才该真正接入实时数据；本片完全没有触碰这两处，
   `ServerDetailPage.tsx` 仍是纯 UI 蓝图壳，`isServerDetailPreviewId`
   的 fixture 列表也没有跟真实登记的资产 id 打通——这是刻意的，
   Server Agent 接入是一个独立的、大得多的后续里程碑。
5. **前端本片新登记的资产/供应商/域名 id 目前互不打通到
   `ServerDetailPage.tsx` 的详情页**：列表页没有「查看详情」跳转，
   因为详情页目前只认蓝图 fixture id；等 follow_up 4 落地后，两者
   应该合并成同一份真实数据源。
