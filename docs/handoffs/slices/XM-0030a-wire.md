# XM-0030a-wire：审批中心落库 + 内核接线

- **status:** implemented，未上线。**接线但未启用**——没有任何调用方注入
  ApprovalGateway，所以生产行为一个字节不变（见「有没有改变现状」）。
- **branch:** `ai/claude/XM-0030a-approval-core`（与 XM-0030a-approval-core
  同一分支，接着提交），基线 `66cd420`。
- **来源：** 设计稿 `docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md`。
- **上一片：** XM-0030a-approval-core（`9a7fc68`：迁移 000049 + approval 领域包）。

## 为什么

Foundation-A 的内核对 L2 及以上一律返回 `ADVANCED_CONTROLS_REQUIRED`。这不是
一个理论限制：`cards/funds_actions.go`、`sms/actions.go` 里都留着"本该 L2、被迫
声明成 L1"的注释，而 `registry.connection.set_status` 和
`registry.connector.create` 声明了 L2、于是**根本执行不了**。

上一片把领域规则和库表做好了但没接上。这一片把它接进内核。

## 改了什么

**`internal/platform/approval/store_pg.go`（新增）** —— `Store` 接口 + `PgStore`。
接口而不是直接用具体类型：内核只需要落单/取单/记票/标执行四件事，拿接口能让
内核的接线测试不必起容器。

三处是**刻意的写法**，各自配了变异验证：

1. `Vote` 全程一个事务、单行 `FOR UPDATE`。两个人同时投 L3 的第二票时，若各自
   先读后写，两边都读到"已有一票、还差一票"，于是两票都被收下、单上留下三票，
   其中第三票是在决定作出**之后**才落的。它的近亲更危险：一张 REJECT 与一张
   APPROVE 并发时谁最后 UPDATE 谁说了算，已驳回的单会变成已批准。
2. `MarkExecuted` 把 `status='APPROVED' AND execution_run_id IS NULL` 写进
   WHERE，零行即 `ErrAlreadyExecuted`。一单最多一跑由库来保证。
3. `Cancel` 把 `requester_id` 写进 WHERE 而不是先读后判，少一个"读完到写之间被
   人改掉"的窗口。撤不动时不区分"不是你的单"和"已经不 PENDING 了"——区分开会
   泄漏别人单子的存在与状态。

**`internal/platform/action/kernel.go`** —— 加 `Request.Reason`、
`ApprovalSubmission`、`ApprovalGateway` 接口、`WithApprovalGateway`；
`RequiresAdvancedControls()` 那一支从"环境/权限/Schema **之前**"挪到了**之后**。

**`internal/platform/action/errors.go`** —— 加 `CodeApprovalRequired` 与
`Error.ApprovalRequestID`。单号放结构化字段，不塞进 Message 让人从文案里抠。

**`db/migrations/000049`** —— `action_version` 从 `int` 改成 `text`，对齐
`Definition.Version` 的实际类型（上一片写错了，这一片一并修）。

## 这一片唯一的行为设计决定：校验顺序

风险闸原本排在最前面。接了审批中心之后，"直接拒"变成"落一张待批单"——于是**一个
没有权限的人对 L2 Action 发起调用，会得到"单已建立"**。等于谁都能往审批队列里
灌单，而审批人被迫替内核做权限和参数校验。

挪到环境/权限/Schema 之后，他拿到的是 `PERMISSION_DENIED`。

这条断言的形状是"本该被拒的确实被拒"，天然容易恒真，所以两个子用例各配了一个
**只差那一项的对照组**：把缺的那一项补上，同一次调用就应当落单。

## 有没有改变现状

**没有。** 三重保证：

- `WithApprovalGateway` 目前没有任何调用方。`k.approvals == nil` 时内核走的还是
  Foundation-A 的原路径，返回 `ADVANCED_CONTROLS_REQUIRED`。
- `TestWithoutGatewayL2StillFailsClosed` 对 L2/L3/L4 逐个钉住这一点，且**明确给了
  合法的 Reason**——证明拦住它的是"没接审批中心"而不是"参数不全"。
- 既有的 `TestKernelRejectsAdvancedControlLevels` 原样通过。

迁移 000049 建的两张表在生产是空的，没有任何代码读写它们。

## 测试

**`internal/platform/action/kernel_approval_test.go`（新增 6 条）**
接了假网关之后：L2+ 落单并带回单号、参数原样冻结、Run 留痕；没接网关时 L2/L3/L4
仍 fail closed；缺 Reason 拿 `INVALID_PARAMS` 且不落单；**没权限/参数非法的人
拿 `PERMISSION_DENIED`/`INVALID_PARAMS` 而不是落单**（各带对照组）；落单失败报
`INTERNAL` 而不是静默当作已受理；L0/L1 不受影响、照常执行且不要求 Reason。

**`internal/platform/approval/store_integration_test.go`（新增 9 条）**
建单取回逐字段核对、跨环境读不到（带对照组）、按等级结算票数（L2 一票 / L3 两票 /
一张 REJECT 立即驳回 / L4 缺特权票不批，末条带"补上特权票就批"的对照组）、
一人一票、L3 不许自批而 L2 可以、并发第二票、执行幂等、撤回归属与时机、
过期不写 `decided_at`、List 按状态与环境过滤。

### 变异验证（四项，逐条跑过）

| 变异 | 期望 | 结果 |
|---|---|---|
| 风险闸挪回权限校验之前 | 越权可落单的两个子用例变红 | 红，`got APPROVAL_REQUIRED` |
| `Vote` 去掉 `FOR UPDATE` | 并发用例变红 | 三次三红，`got 2 张` |
| `MarkExecuted` 去掉状态守卫 | 幂等用例变红 | 红 |
| `Cancel` 去掉 `requester_id` 条件 | 撤回归属用例变红 | 红，`got <nil>` |

并发那条第一版**是恒真的**：去掉行锁跑五次全绿。原因是两个 goroutine 实际没撞
上那个窗口。改成把栅栏挂在 Store 注入的时钟上——`Vote` 在拿到行锁**之后**才调
`s.now()`，"谁进得来"恰好就是"谁拿到了锁"——并且判据从"总共来过几个"改成
**同时在临界区里的峰值**。有锁时后来者最终也会进来（在先来者提交之后），只是
两者从不重叠；第一版正是被这一点骗过去的。改完之后变异 0.04 秒就撞红，不靠调度
运气。

门禁：`go test -p 1 -count=1 ./...` 全绿（八个代理变量已 `env -u`）、
`go vet ./...` 退出 0、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 与 `66cd420..HEAD` 增量扫描均无泄漏。

## risks

- **`PgStore` 尚无生产调用方**，所以它只被集成测试验证过，没被真实流量验证过。
  下一片接 HTTP 时要留意的是分页与 `List` 的 N+1：`List` 目前对每个 id 单独
  `Get`（为了带上票），单页 50 条就是 51 次查询。审批队列量小，先不优化，但
  **别把这个 limit 放开到 200 以上**。
- **`Settle` 对未配置的等级返回 PENDING**，即单永远不落定。这是刻意的 fail
  closed，但缺一条告警——XM-0030c 的"PENDING > 4h"规则会捞到它。

## follow_ups

- **XM-0030b**：HTTP 端点（`POST /api/v1/approvals`、`GET /approvals?status=`、
  `POST /approvals/{id}/decide`、`POST /approvals/{id}/execute`）+ 审批页。
  执行端点要重算 `params_hash` 并与单上冻结的比对，然后走内核原有的 Execute
  全链——审批不是绕过 Action 的通道。
- **XM-0030c**：`PENDING > 4h` 告警规则 + Runbook；`ExpirePending` 需要一个定时
  任务来调用（River），目前没有任何调度器在调它。
- **单独一片，且要先问产品负责人**：cards/sms/registry 里被迫降级成 L1 的
  Action 恢复正确等级。那是**行为变更**——从"谁都能跑"变成"要人批"，不该混在
  接线片里悄悄生效。相关记忆见 `action-risk-level-l1-ceiling`。
- 设计稿 §7 的四个问题（票数 / 有效期 / 可否自批 / 执行窗口）仍待拍板。已登记
  进晨间清单；在此之前它们是 `DefaultPolicy()` 里的配置，改配置即可，不必改代码。
