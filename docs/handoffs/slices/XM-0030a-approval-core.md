# XM-0030a-approval-core —— 审批中心的落库面与状态机

- **status:** ready-for-review（未合入验收线；**不改变任何现有行为**，见下）
- **branch:** `ai/claude/XM-0030a-approval-core`
- **base:** `66cd420`（release/v0.1-launch）
- **commit:** 见分支尖端

## summary

盘点显示 **Foundation-B / XM-0030（Action 高级风险控制）是整个平台最集中的
阻塞源**，一条未实装的能力卡住了：

- 「操作与审批」的待审批页、「版本与发布」的待评审变更（ADMIN-IA 明文规定
  「F-B 未完成前，操作与审批页必须显示门禁，不可伪造执行」）；
- 工作台四类待办里的两类（`lib/workbench.ts` 的 `blockedBy`）；
- `cards/funds_actions.go:262`、`cards/withdraw_actions.go:71`、`sms/actions.go:62`
  等处业务代码**被迫把本该 L2/L3 的 Action 声明成 L1** 绕开内核的 fail-closed；
- `registry.connection.set_status` 与 `registry.connector.create` 两份契约声明为
  L2，而内核对 L2+ 一律拒绝——**这两个 Action 目前声明了却执行不了**。

本片按设计稿 `docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md`
交付 XM-0030a 的**前半段**：落库面 + 领域逻辑 + 测试。**内核接线与 Postgres
Store 留给 XM-0030a-wire**，理由见「not_run」。

### 关于设计稿 §7 的四个待拍板问题

设计稿把四项列为「待产品负责人拍板」，并各给了建议默认值。**我没有停下来
等，也没有把建议值写死**——它们做成了 `Policy` 这个配置结构，`DefaultPolicy()`
逐字采用设计稿的建议值。负责人日后给出裁定，改 `Policy` 即可，不动代码、
不返工。四项已登记进晨间清单：

| # | 问题 | 本片采用的默认 |
|---|---|---|
| 1 | L3/L4 票数与 `approval.l4` 持有人 | L3=2 票；L4=2 票且至少 1 张特权票 |
| 2 | 审批单有效期 | L2/L3 = 24h；**L4 = 4h**（设计稿留的问号，取更严一侧） |
| 3 | 提交人可否自批 | L2 可；L3+ 不可 |
| 4 | APPROVED 后的执行窗口 | 24h |

## 落库面（迁移 000049）

`core.approval_request` 与 `core.approval_decision`。几处刻意的约束：

- **`approver_type` 只收 `HUMAN`**，是宪法 24 条（AI 不作为 L3/L4 第二审批人）
  的**库层**红线。比条文更严——一律只收 HUMAN，让「AI 能不能投票」变成一个
  不需要读代码就能回答的问题。代码侧另有一道同样的校验（双重）。
- **`execution_run_id` UNIQUE**：「一单最多一跑」在库层就撞上，不靠应用层自觉。
- **`privileged` 冻结在票上**而不是执行时再查权限：权限会变动，而「这一票当时
  算不算特权票」是审计事实。一个人今天被收回 `approval.l4`，不该让他昨天投出
  的那张特权票追溯失效。
- **状态与时间戳的自洽 CHECK**：不允许出现「已执行但没有执行时刻」这种行——
  审计无从解释的数据比没有数据更糟。
- `params_hash` 带 `^[0-9a-f]{64}$` 形状约束。

## 领域逻辑（internal/platform/approval）

`Policy` 承载全部可配置裁决；`CanVote` / `Settle` / `CanExecute` 是三个纯函数，
不碰数据库，所以能在没有容器的情况下把状态机跑穿。几处判断值得点名：

- **`CanVote` 的检查顺序有讲究**：先身份类别，再权限，再自批限制，最后重复
  投票。这样 AI 拿到的永远是「AI 不能投票」，而不是「你缺 approval.decide」
  ——后者会让人以为补个 scope 就行。
- **一票 REJECT 即驳回**：审批是「所有人都同意」而非「多数同意」。任何一个
  审批人看出问题，这件事就不该做。
- **未配票数的等级不放行**（`Settle` 返回 PENDING 而不是 APPROVED）。等级表
  演进时新增等级若忘了配票数，宁可卡住也不能默认零票通过。
- **`HashParams` 用 `encoding/json`**：Go 的 json 包对 map 键保证字典序输出，
  同一份参数总是同一个哈希，不需要另写一套规范化。`nil` 与空 map 同哈希，
  否则「不传参数」和「传了个空对象」会成为两张不同的单。

## files_changed

- `db/migrations/000049_action_approval.{up,down}.sql`（新增）
- `internal/platform/approval/approval.go`（新增）
- `internal/platform/approval/approval_test.go`（新增）
- `docs/handoffs/slices/XM-0030a-approval-core.md`（本文）

## tests_run

- `go test ./internal/platform/approval/` 全绿，10 个测试覆盖：AI 与其它机器
  身份不能投票、自批仅 L2 允许、票数与特权票、一票否决、未配等级不放行、
  过期同时挡住投票与执行、参数漂移拒绝、一单最多一跑、执行窗口、哈希稳定性、
  重复投票、缺 scope。
- **做过变异验证**：去掉 `CanVote` 里「审批人必须是 HUMAN」那道判断，
  `TestAIPrincipalCanNeverVote` 立刻变红（AI 拿到 `nil` 即放行）。这条断言的
  形状是「本该被拒的操作确实被拒」，极易恒真——所以测试里给 AI **配齐了
  decide + l4 全部权限**，让它除身份类别外每一项都合格，唯一能拒它的理由只剩
  「它是 AI」；并且带一个同等条件的 HUMAN 对照组。
- 平台后端全量 `go test -p 1 -count=1 ./...`。
- `gofmt` 干净。

## not_run

- **内核接线未做**，留给 `XM-0030a-wire`。理由：接线要改
  `kernel.go` 的校验顺序（L2+ 的分支目前在**环境/权限/Schema 之前**，应当挪到
  之后——没有权限的人不该能刷审批单，他该拿到 PERMISSION_DENIED 而不是「单已
  建立」），这是一处会影响既有错误语义的改动，值得单独一片单独审读。
- **Postgres Store 未做**，同上片。本片的领域逻辑是纯函数，不依赖 Store。
- HTTP 端点与前端审批页是 XM-0030b，告警规则与 Runbook 是 XM-0030c。
- 迁移未在真实库上跑过（本机没起平台测试库；下一片接 Store 时会带容器测试）。

## risks

- **本片不改变任何现有行为**：新包没有任何调用方，迁移只新增两张空表。内核
  对 L2+ 仍然 fail closed。这是有意的——把「能不能表达审批」和「内核什么时候
  改走审批」拆成两次审读。
- 迁移 000049 可逆（两条 `DROP TABLE`，先删 decision 再删 request）。
- `DefaultPolicy()` 的四项是**建议值不是裁定**。在负责人拍板之前，任何依赖这
  四项的对外承诺都不成立。

## follow_ups

- **XM-0030a-wire**：Postgres Store + 内核接线（`RequiresAdvancedControls()`
  分支从「直接拒」改为「落审批单」，`ExecuteApproved(requestID)` 走原有 Execute
  全链）+ 容器测试。**接线时必须把 L2+ 的判断挪到环境/权限/Schema 之后。**
- **XM-0030b**：`POST /api/v1/approvals`、`GET /approvals?status=`、
  `POST /approvals/{id}/decide`、`POST /approvals/{id}/execute` + 审批页。
- **XM-0030c**：PENDING 超 4h 未决的告警规则 + Runbook（含「审批人失联怎么办」）。
- **接线后要回头收的债**：`cards`/`sms`/`registry` 里那些「仍是 L1：内核对 L2
  及以上返回 ADVANCED_CONTROLS_REQUIRED」的注释与降级声明，应当逐个恢复成
  设计上正确的等级。**这批改动会让一批 Action 从「谁都能直接跑」变成「要人批」**
  ——是行为变更，必须单独一片并让负责人知情。
- `approval.l4` 特权票持有人要映射进 RoleScopeMap（设计稿明确「不进 Keycloak」）。
