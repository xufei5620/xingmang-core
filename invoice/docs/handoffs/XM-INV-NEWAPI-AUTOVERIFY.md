# XM-INV-NEWAPI-AUTOVERIFY：New API 充值按证据自动核验

- **status:** implemented，未发布。**含迁移 0029，且这条迁移移除一道财务控制。**
- **branch:** `ai/claude/XM-INV-NEWAPI-AUTOVERIFY`，基于
  `ai/claude/XM-INV-NOTICE-WEBHOOK-SETTING`（合入链：SUBMIT-NOTICE →
  NOTICE-VIEW → NOTICE-UI → NOTICE-WEBHOOK-SETTING → 本片）。
- **产品负责人 2026-09-06 明确决定「完全自动」**，是在被完整告知双人复核触发器
  的存在与移除后果之后做的决定。三条路（保留双人 / 降为单人 / 完全自动）都摆过。

## 为什么

New API 用户**连提交开票申请都做不到**——可开票金额恒为 0，因为每一笔充值都
卡在双人复核队列里，而那个队列没有人在处理。这是确定的、每天都在发生的损失。

## 移除的到底是什么

迁移 0006 建的 `newapi_verified_dual_control_guard`：一道数据库触发器，要求每
一笔 `verified` 的 New API 资金批次都有一条 `proposed_by <> approved_by` 的已
批准决策。**移除之后，没有任何人需要对这笔钱签字**，唯一的关卡是开票那一刻的
人工判断。

**决定所依据的三个事实**（对着上游源码 `K:/newapi-src` 逐条查的，不是推测）：

1. **管理员送的免费额度进不到本系统。** 手动加额度走 `IncreaseUserQuota`
   （`controller/user.go:1174`），它只改 `users.quota`，**从不写 `top_ups`**；
   而本系统的充值投影只读 `top_ups`（五个 `TopUp{}` 插入点全在支付控制器里：
   epay / creem / stripe / waffo / pancake）。这是结构隔离，不是操作纪律。
2. **"补单"是真钱。** `AdminCompleteTopUp` 必须带 `tradeNo`，那是支付流程创建
   的订单——钱到了、只是回调没回来。
3. **最终由人开票。** 用户提交的只是申请；真发票由管理员在税务平台手动开具再
   上传。自动核验 ≠ 自动开票。

## 判据

`newAPICandidateSettled(sourceStatus, completedAt, payMinor)`：
状态 `success` + 有完成时刻 + 金额为正。与 Sub2API 那条**同形**。

**抽成函数而不是写在调用点里**，是为了让测试测到真东西——判据埋在 if 里时，
测试只能复制一份条件再断言那份副本，改了实现测试照样绿。

**故意不额外要求支付渠道名**：补单不改 `payment_provider`，拿它当判据既挡不住
补单，又会把渠道名为空的正常订单误挡。**也不放宽大小写**：New API 的常量是小写
`success`，放宽等于替上游猜它没说过的话。

## 一处必须一起改的地方

自动核验带来一个原设计没有的情形：**后来的扫描会覆盖人工复核过的金额**。
原来那条守卫只在"观测为 pending"时保护已复核的批次，而现在观测可能直接是
verified。

`postgresstore/funding.go` 因此增加一条分支：**该批次只要有过任何人工复核记录
（`payment_candidate_reviews` 有行），观测就只能刷新元数据，永远改不动金额与
核验状态。** 人工判断不该被自动流程无声覆盖——这正是人工通道存在的意义。

## 历史数据

迁移把已满足判据的待核验批次提上来（`source_status='candidate:success'` +
有完成时刻 + 金额为正 + 未冻结）。`current_cap_minor` 与 `verified_cash_minor`
都设成 `original_minor`，分别与人工核验路径（`funding.go`）和资格路径
（`consumption.go`：verified 时 `verifiedCash = currentCap`）同义，不是这里发明
的数。

**不动 `consumed_cash_minor`**：那是消耗分配的结果。因此回填之后，历史批次的
**可开票额仍取决于已消耗现金**——想让历史消耗补上去，需要另外跑一次资格重投影
（`--reproject-all`）。**上线前请在恢复副本上确认这一步的效果。**

## 仍然存在的缺口

**退款。** New API 的状态里没有退款态，一笔已退的充值在这里仍是 `success`。
已写进 `docs/PRODUCTION-RUNBOOK.md` 新增的 **§9b**：每次手动退 New API 的款，
必须在管理端冻结该批次（全退）或调低上限（部分退），并建议每月对账一次。
**这件事没有自动化，也没有任何告警会提醒。**

## 测试

- `newapi_autoverify_test.go`（新）：判据的九种输入，**调的是处理器真正用的那个
  函数**。每条用例都写明它在防什么（待支付/失败/过期/无完成时刻/零元/负数/
  大小写/空状态）。
- `service_integration_test.go`：第一笔候选改成非 `success` 状态——**它不满足自动
  核验判据，于是整套人工复核（冻结/驳回/提议/批准）仍然被覆盖到**，那条通道
  没有因为放开而失去测试。
- `store_integration_test.go`：原来断言"数据库拒绝改动已核验批次的上限"的那条
  改写为**断言那道触发器确实已被删除**——移除要是彻底的、看得见的，不能让后来
  的人把半途而废的迁移误当成预期状态。同时保留决策与复核记录不可改写的断言：
  取消"需要两个人"这个要求，不等于让"人到底决定了什么"的记录变得可改。
- `migrate_test.go`：0029 登记进那个刻意精简的私有 schema 夹具（它排除了 0009，
  而 0029 用的两列是 0009 加的）——与 0016/0020/0022/0023 同一个理由。

门禁：`go build ./...`、`go test -p 1 -count=1 ./...` 全绿（带
`INVOICE_TEST_DATABASE_URL`）、`gofmt`（LF 归一后）干净、
`gitleaks protect --staged` 无泄漏。

## 上线

**含迁移 0029，且不可逆**（触发器与函数被 DROP）。回滚需要重建 0006 的触发器
与函数，且在重建之前必须先把所有 `verified` 的 New API 批次冻结——0006 自带的
那道 `DO $$` 检查就是为此而写的。

## follow_ups

- 历史消耗的回填（`--reproject-all`）没有在这一片里做，也没有验证过效果。
- 没有任何告警会发现"已退款却仍可开票"。真要自动化，得让上游的退款事实进得来
  ——那要改 New API 的投影契约，属另一片。
