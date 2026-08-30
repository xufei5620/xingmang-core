# XM-ACTIONS0 · 全局「操作与审批」页接真数据（操作目录 + 执行记录）

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-ACTIONS0`
- implementation commit: `b4bc054`（本分支 HEAD，单提交）
- base: `release/v0.1-launch` @ `ef0a85f`
- worktree: `K:/星芒统一控制平台/acceptance/wt-actions0`

## summary

把 `/actions`（ADMIN-IA §2.2 `g/actions`：操作目录 / 待审批 / 执行记录 / 风险
与启用条件）从占位页做成真实页面。四个子页签里两个接了真数据（操作目录、
执行记录），一个如实显示「未接入」（待审批——不是没做完，是这个模块在仓库
里真的还不存在），一个是静态治理事实 + 目录实时统计的参考页（风险与启用
条件）。**没有新增任何绕过 Action 内核的写路径**，本片只加只读 Query。

### 后端

**`internal/platform/action`**

- 新文件 `permissions.go`：`action.ScopeRead = "action.read"`，跟 `audit.ScopeRead`
  同一个理由链——执行记录逐条暴露 principal/action/风险等级/错误码，比运营
  指标更接近"操作明细"，但不含 before/after（那部分仍单独要 `audit.read`）。
- `store.go` 新增：
  - `RunFilter` / `RunPage`：环境必填（httpapi 层填 Principal 的环境，不接受
    调用方传任意环境）、`action_id`/`status`/`principal_id` 可选过滤。
  - `(started_at, id)` 复合 keyset 游标（`encodeRunCursor`/`decodeRunCursor`，
    base64 编码，前端当不透明字符串对待）——**没有**用单独的 `started_at`
    当游标：那会像 `audit.ListRecentAuditEvents` 修复前一样在同一微秒内的
    多条记录上翻页重复/漏读，`action_run` 没有 `audit_event` 那种全局递增
    `sequence`，但 `id` 是 UUID 主键，`(started_at DESC, id DESC)` 仍是严格
    全序。
  - `ListRuns` / `GetRun`；`ListRunsByAction` 的行映射抽成 `runFromRow` 三处
    共用。
- 新查询 `ListActionRuns` / `GetActionRunByID`（`db/queries/action.sql` + 手写
  `gen/action.sql.go`——**本机没有 `sqlc` 二进制，生成代码是照现有文件的
  `sqlc v1.31.1` 输出风格手写的**，`.sql` 源文件与 `.sql.go` 保持逐字对应，
  下次有人跑 `sqlc generate` 应该是空 diff，但没有实测验证过，见下方
  risks）。
- 迁移 `000022_action_run_environment_index`：`(environment, started_at DESC,
  id DESC)` 复合索引，理由与 000006 给 `audit_event` 打的同一个补丁一致
  （没有它时稀疏环境的一页要沿全局索引倒扫）。

**`internal/platform/audit`**

- `store.go` 新增 `GetByActionRunID`：按 `action_run_id` 取回关联的审计事件
  （走既有索引 `audit_event_action_run_idx`），不取两个 connector 摘要（同
  `ListRecent` 的纪律）。新查询 `GetAuditEventByActionRunID`（同样手写
  `gen/audit.sql.go`）。

**`internal/platform/httpapi`**

- 新文件 `actionruns.go`：
  - `GET /api/v1/actions/runs`（`action.ScopeRead`）——列表，**不含**
    before/after。
  - `GET /api/v1/actions/runs/{run_id}`（`action.ScopeRead` **+**
    `audit.ScopeRead` 叠加，同 `platform channels` 端点叠 `ops.read` +
    `finance.read` 的先例）——详情，`audit` 字段为 null 表示没找到关联的
    审计事件（理论上不该发生，kernel.go 的 record/recordAudit 总是成对
    调用，但审计写入失败不回滚业务结果，界面要能诚实呈现这种缺口）。
    跨环境的记录一律当 404（不是 403——403 会向调用者确认"这个 run_id
    确实存在"）。
  - `Deps.ActionRuns` / `Deps.ActionRunAudit` 都是**可选**字段（nil 时对应
    端点不挂载），与 `RequestLogs`/`PlatformUsers` 同一条"端点不存在比存在
    却 500 诚实"的纪律——但这两个在生产装配里**应当恒非 nil**（`cmd/
    platform-api/main.go` 已经无条件传了同一个 `actionRunStore`/
    `auditStore`），做成可选纯粹是为了不动 `internal/platform/httpapi` 里
    其它测试文件构造 `Deps{}` 的最小 harness（`testhelpers_test.go` 的
    `testRouter`），不是一个真的可以关掉的开关。
- `GET /api/v1/actions`（目录，`actions.go`）**没有改动**：`actionSummary`
  已经含 `risk_level`/`permission`/`environments`/`principal_types`/
  `executable`/`blocked_reason` 全部字段，任务描述里"补上若缺"的字段其实
  都已经在。
- `cmd/platform-api/main.go`：`action.NewPgRunStore(...)` 原来是内联在
  `action.NewKernel(...)` 调用里的匿名值，改成具名变量 `actionRunStore`
  （Kernel 写、httpapi 读，同一个实例，不另开访问 `action_run` 表的路径），
  同样把 `Deps.ActionRunAudit` 指向已有的 `auditStore`。

### 前端

- `web/packages/ui-admin/src/navigation.ts`：`actions` 项 `built: false →
  true`（子页签集合 `catalog`/`pending`/`runs`/`risk` 不变，已经是 ADMIN-IA
  §2.2 逐字定义）。
- 新文件 `web/apps/admin-web/src/api/actions.ts`：`listActionDefinitions`
  （目录）、`listActionRuns`（执行记录，游标分页）、`getActionRun`（详情）；
  三者都不接受 `environment` 参数——一律用 Principal 自己的环境，同
  `listAuditEvents`/`listPlatformRequests` 的既有纪律。
- 新文件 `web/apps/admin-web/src/pages/ActionsPage.tsx`：
  - 外层判断 `?sub=` 是否认识，认识就交给 `Tabs`（结构照 `AuditPage.tsx`/
    `IdentityPage.tsx`），默认落在 `catalog`（`subTabs[0]`）。
  - **页面级 F-B 门禁横幅**（`AdvancedControlsGate`，原文照抄旧
    `PlaceholderPage.tsx` 里 `PlaceholderGate` 的原文）挂在 `PageHeader` 与
    `Tabs` 之间，四个子页签下都可见——这是与 `AuditPage`/`IdentityPage` 唯一
    的结构性差异：ADMIN-IA §七要求"F-B 未完成前，操作与审批页必须显示门
    禁"，这是整页的事实，不能只塞进"待审批"一格，否则默认落在"操作目录"
    的人根本看不到。
  - **操作目录**：`DataTableV2`，可按风险等级筛选、表内搜索；"可执行=否"
    的行展示 `blocked_reason`。
  - **待审批**：`PageState kind="unavailable"`，如实说明 `internal/
    platform/approval/` 只有 `.gitkeep`、内核对 L2 及以上一律
    `ADVANCED_CONTROLS_REQUIRED`，不伪造队列；提案见下方"审批模型提案"。
  - **执行记录**：结构照 `components/RequestsPanel.tsx`（筛选进 URL Search
    Params，游标是页内状态不进 URL），`DataTableV2` 的行内展开
    （`renderExpanded`）懒加载 `GET .../runs/{id}`——DataTableV2 只为真正
    展开的行调用 `renderExpanded`（见 `ui-admin/DataTableV2.tsx` 的
    `expanded.has(...)` 过滤），不会一页 N 行就发 N 次请求。
  - **风险与启用条件**：ADR-003 的 L0～L4 静态表（逐字对齐）+ 复用
    `["action-definitions"]` 这个 queryKey 算出的实时统计（已注册/可执行
    数），与"操作目录"共用 react-query 缓存，切换子页签不会重新拉一次。
- `router.tsx`：加路由 `{ path: "actions", Component: ActionsPage }`（原来
  落进 `placeholderRoutes`，现在 `navigation.ts` 标了 `built: true` 之后会
  被过滤掉，两处不会重复注册）。

## files_changed

后端：

- `cmd/platform-api/main.go`
- `db/migrations/000022_action_run_environment_index.{up,down}.sql`（新增）
- `db/queries/action.sql`、`db/queries/audit.sql`
- `internal/platform/action/permissions.go`（新增）
- `internal/platform/action/store.go`、`store_test.go`
- `internal/platform/action/gen/action.sql.go`
- `internal/platform/audit/store.go`、`store_test.go`
- `internal/platform/audit/gen/audit.sql.go`
- `internal/platform/httpapi/actionruns.go`、`actionruns_test.go`（新增）
- `internal/platform/httpapi/router.go`

前端：

- `web/packages/ui-admin/src/navigation.ts`、`navigation.test.ts`
- `web/apps/admin-web/src/api/actions.ts`、`actions.test.ts`（新增）
- `web/apps/admin-web/src/pages/ActionsPage.tsx`（新增）
- `web/apps/admin-web/src/router.tsx`、`router.test.tsx`

## tests_run

- `go build ./...`：PASS。
- `go vet ./...`：PASS（无输出）。
- `gofmt -l .`：本片改动的文件全部干净；仅
  `internal/platform/httpapi/finance_test.go` 被列出——**改动前就存在、本片
  未碰**（同 XM-USERS-REAL handoff 记录的同一条既有问题，未处理，不在本片
  范围）。
- `go test ./...`：全仓 PASS，含新增的
  `internal/platform/action`（`TestPgRunStoreListRunsFiltersAndPaginates`/
  `RejectsInvalidCursor`/`GetRun` 三条集成测试，需要 `XM_TEST_DATABASE_URL`
  才会真的跑，本机未设置该变量，均 SKIP——与仓库里全部数据库集成测试同一个
  既有约定）、`internal/platform/audit`（`TestGetByActionRunID`，同样
  SKIP）、`internal/platform/httpapi`（`actionruns_test.go` 14 条，全部走
  fake store，不需要数据库，全部真正跑过并 PASS：scope 叠加、环境隔离
  （含跨环境 404 而非 403）、limit 夹取、cursor 透传、字段契约、空审计摘要
  返回 null、内部错误隐藏细节等）。
- `bash scripts/check-governance.sh`：PASS（exit 0）。
- 前端（`web/` 下）：
  - `pnpm --filter admin-web run typecheck`：PASS。
  - `pnpm --filter ui-admin run typecheck`：PASS（改了 `navigation.ts`，一并
    验证）。
  - `pnpm --filter admin-web run test -- --run`：PASS，80 files / 1148
    tests（含 `router.test.tsx` 新增的"操作与审批"describe 块 6 条、
    `api/actions.test.ts` 新增 10 条）。
  - `pnpm --filter ui-admin run test -- --run`：PASS，16 files / 232 tests
    （`navigation.test.ts` 三条既有断言按 `actions` 现已 `built: true` 更新，
    见下方 risks）。
  - `pnpm --filter admin-web run build`：PASS（`vite build` 产物警告单个
    chunk 超 500KB，是改动前就有的既有状态，不是本片引入）。

## not_run / risks

- **手写的 sqlc 生成代码没有跑真实 sqlc 校验**：本机没有 `sqlc` 二进制
  （`which sqlc` 落空），`internal/platform/action/gen/action.sql.go` 与
  `internal/platform/audit/gen/audit.sql.go` 里新增的
  `ListActionRuns`/`GetActionRunByID`/`GetAuditEventByActionRunID` 是照着
  文件里已有的 `sqlc v1.31.1` 输出（尤其是 `ListActionRunsByAction`/
  `ListRecentAuditEvents` 两条已有查询的确切风格：具名参数如何编号成
  `$1`/`$2`、timestamptz 参数映射成 `pgtype.Timestamptz`、`uuid` 映射成
  `github.com/google/uuid.UUID`）**手写**的，不是工具生成的。`go build`/
  `go vet`/`go test` 都过，运行期行为（`go test` 里的集成测试路径）没有
  真实数据库可验证——下一个能连数据库的人（或有 `sqlc` 二进制的环境）应该
  跑一次 `sqlc generate` 核对是否零 diff，并用 `XM_TEST_DATABASE_URL`
  真跑一次 `TestPgRunStoreListRunsFiltersAndPaginates`/`TestGetByActionRunID`
  这几条集成测试。
- **执行记录目前没有任何真实数据**：Foundation-A 阶段内核对 L2 及以上一律
  `ADVANCED_CONTROLS_REQUIRED`，已注册的 Action 里能真正跑到 `succeeded`/
  `failed` 而写进 `action_run` 的只有 L0/L1（现在主要是各模块的 L1
  Action，比如 XM-0048 的成本登记簿四个 Action）。页面本身没问题，只是
  在还没人执行过任何 L0/L1 Action 的新环境上会看到"这个环境还没有执行
  记录"的空态，这是如实反映现状，不是 bug。
- **`GET /api/v1/actions/runs/{run_id}` 要求两个 scope 同时持有**（`action.
  read` + `audit.read`）是我做的设计决定，不是任务描述里的既定要求——理由
  见上面 summary，但**没有找人确认过这个权限分级是不是产品想要的粒度**。
  如果实际运营角色模型里"能看执行记录列表"和"能看执行详情里的 before/
  after"从来不会分开授予，这条设计增加的只是维护成本，值得跟人员与权限
  （`gov/identity`）那条线对一次。
- 没有验证过大量执行记录下的翻页体验（keyset 游标的正确性有集成测试覆盖，
  但没有在数千行规模的表上测过查询计划是否真的命中新建的复合索引——本地
  没有可用的测试数据库，无法跑 `EXPLAIN`）。

## 审批模型提案（供产品/架构拍板，不是本片的既定实现）

`internal/platform/approval/` 目前只有 `.gitkeep`；Foundation-B 的
Action Advanced Controls（幂等键、写后读取确认、人工审批、Step-up MFA、
冷却期、Kill Switch）整体未实装，`kernel.go` 对 `RiskLevel.
RequiresAdvancedControls()`（L2/L3/L4）一律在 `Execute` 早期拒绝
（`CodeAdvancedControlsRequired`）。以下是"操作与审批 → 待审批"这一格
真正需要什么数据模型的一份提案，**没有写任何代码**，供人拍板后再排期：

1. **审批申请（Approval Request）**：谁在什么时候，针对哪个
   `action_id`/`version`/`params`（校验通过但被内核拦在审批前的那次尝试）
   发起了申请；应该在**内核层**生成，而不是一个独立于 Action 执行链路之外
   的表单——申请的参数必须是"如果批准了会拿去执行的那份参数"，否则会出现
   "批准的是 A，实际执行的是 B"的窗口期。落点建议：`kernel.Execute` 在
   `RequiresAdvancedControls()` 分支里，不再是直接拒绝，而是调用一个
   `ApprovalSink`（类似现有 `AuditSink` 的接口形状，`action` 包不 import
   `approval` 包）写一条 Pending 记录，返回给调用方一个"已提交审批"的结果
   （而不是 `ACTION_RUN_ID`——这次调用本身还没有执行）。
2. **审批人（Approver）**：L3 要求人工批准，L4 目标双人审批（宪法 9/10
   条：AI 不作为第二审批人）。谁有资格批某个 `action_id` 需要一个独立于
   `principal.Scopes` 的授权面（不能复用执行该 Action 所需的
   `permission`——申请人自己批自己违反基本的职责分离，而"谁能执行"和"谁能
   批准别人执行"必须是两个能分开配置的集合）。这条应该跟`gov/identity`
   （人员与权限）那条线一起设计，不该在 `action`/`approval` 包内部另起
   一套身份模型。
3. **期限（Expiry）**：一条 Pending 申请如果长期没人处理，不应该无限期
   悬挂——过期后申请人需要重新发起（而不是"过期后自动执行"或"过期后自动
   拒绝并允许无限重试绕过"，两者都会架空审批本身的意义）。期限长度大概率
   要按风险等级分（L3 与 L4 可能不同），且应该在 `RiskLevel` 的声明或
   `Definition` 里补一个显式字段，不是写死的全局常量。
4. **与 Action 执行的衔接**：批准后**谁来真正触发执行**是设计里最容易留
   后门的一环。建议：批准动作本身不直接执行业务逻辑，而是把 Pending 记录
   标记为 Approved，由申请人（或任何持有原始执行权限的人）在有效期内
   "确认执行"——这一步重新走一次完整的 `kernel.Execute`（重新校验
   Schema/权限/环境，因为批准和确认执行之间系统状态可能已经变化），只是
   这次因为存在一条匹配的 Approved 记录而放行 `RequiresAdvancedControls()`
   这一关。这样"审批"和"执行"两个动作都各自有完整的 Principal/审计轨迹，
   而不是审批人的一次点击直接改变了生产状态。
5. **审计衔接**：Pending/Approved/Rejected/Expired 四个状态迁移都应该各自
   产生审计事件（`AuditEvent.ApprovalID` 字段已经存在于 `audit.Event`
   结构体里，目前没有任何写入路径会填它——这是个信号，说明审批模型从一
   开始就被规划过，只是没有实现）。

这份提案刻意没有回答的问题（需要产品/架构决定）：批准是否需要理由字段
必填；L3/L4 是否允许同一个人在不同申请上既当申请人又当另一次的审批人
（跨申请的职责分离粒度）；期限的具体数值；Kill Switch 与审批队列的交互
（一个正被审批的 Action 如果期间被 Kill Switch 停用，Pending 记录应该
怎么处置）。

## follow_ups

- 找一个有 `sqlc` 二进制或能连测试数据库的环境，核对本片手写的生成代码
  与迁移索引（见上方 risks 第一条）。
- 上面的审批模型提案需要产品/架构确认后才能排期实现；实现前"待审批"子
  页签应该继续保持现在这个诚实的"未接入"状态，不要为了让页面看起来更
  完整而提前垒一个空表。
- 如果人员与权限（`gov/identity`）那条线后续要建审批人角色模型，
  `action.ScopeRead` 与 `audit.ScopeRead` 是否要合并成一个 scope 值得
  一并复议（见上方 risks 第三条）。
