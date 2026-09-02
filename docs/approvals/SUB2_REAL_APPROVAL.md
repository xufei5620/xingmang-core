# SUB2_REAL_APPROVAL

> status：待审批（PENDING）。本文件是可执行的审批单——审阅人只需要按顺序核对
> 每一项、把方括号里的占位符替换成证据目录里的真实值、勾掉 checklist、签字，
> 不需要再做任何额外的工程调查。

- 事件名：`SUB2_REAL_APPROVAL`
- 依据：`docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md` §0、
  §10、§12；`docs/superpowers/plans/2026-08-28-platform-user-read-v2.md` Task 4
- 证据来源：`cmd/evidence-capture capture --platform sub2api`（用法与安全说明见
  `docs/runbooks/USERS-REAL-APPROVAL.md`）

## 授权范围

| | 内容 |
|---|---|
| **本次批准的** | Task 4：Sub2API real GetUser v2（`connectors/platformusers/sub2api_v2.go`，声明 `platformusers.user.detail_read` capability） |
| **明确不授权** | NewAPI、reqlog、DailyUsage、Key metadata 的任何 real 实现；invoice、payment；本片（XM-USERS-REAL-EVIDENCE0）本身不实现 Task 4；任何生产部署、迁移或 Action |

CORE_APPROVAL 已通过、本模板存在、脱敏证据已采集，**均不构成本审批**。本审批
必须由产品与安全在阅读完下方证据后逐项签字，且批准文本必须明确写出事件名、
目标平台、证据路径与哈希、允许的任务号（设计文档 §0 原文要求）。

## 前置条件（Task 4 Step 1，缺一不可，任一缺失即停止 Task 4）

- [ ] 产品签字批准（见文末）
- [ ] 安全签字批准（见文末）
- [ ] 下方"证据"一节已填写完整并通过复核
- [ ] 人工已在 Sub2API 后台，用即将登记给平台的那个 admin 账号，完成一次合规
      确认（等价于 `POST /api/v1/admin/compliance/accept`，见
      `docs/runbooks/SWITCH-SUB2API-REAL.md` 前置一节）。**这一步必须由人工
      在 Sub2API 自己的后台完成，`cmd/evidence-capture` 永不发送这个 POST**
      ——它只会在这一步没做时收到 HTTP 423 并拒绝写出证据文件。

## 证据

由 `cmd/evidence-capture capture --platform sub2api ...` 生成到
`docs/evidence/users-real/sub2api/<timestamp>/`；本文件只引用路径与哈希，
**不复制其内容**（脱敏样本本身仍然是需要控制访问面的材料，复制到多处只会
增加它意外流出的机会）。

| 字段 | 值 |
|---|---|
| 证据目录 | `docs/evidence/users-real/sub2api/[填入时间戳目录名]/` |
| 采集时间（UTC） | `[填入]` |
| 上游实例版本（`version.json` 的 `version`） | `[填入]` |
| 上游 endpoint 主机（`README.md` 的 endpoint host） | `[填入]` |
| 使用的 CredentialRef | `[填入，形如 secret://sub2api-prod/read-token]` |
| `users_page.redacted.json` 的 sha256 | `[填入，见 SHA256SUMS]` |
| `user_detail.redacted.json` 的 sha256 | `[填入，见 SHA256SUMS]` |
| `version.json` 的 sha256 | `[填入，见 SHA256SUMS]` |
| 本次采集是否遇到 AdminComplianceGuard（423） | `[是/否]`——若为"是"，说明前置
  条件尚未满足，不得批准，需回去补做合规确认后重新采集 |

设计文档 §10 对 Sub2API 列出的四项证据要求，逐项核对（勾选即代表审阅人已在
证据目录里亲自核实，不是自动为真）：

- [ ] **脱敏的 `/api/v1/admin/users` 响应形状和实例版本**——对照证据目录的
      `users_page.redacted.json`（列表形状）与 `version.json`（实例版本）。
- [ ] **分页、ID、状态、余额 scale、created/last-active 的解释**——对照证据
      目录 `README.md`"字段捕获（脱敏）vs. 丢弃"一节；kept 字段应恰好是
      `id`/`email`/`username`/`status`/`balance`/`last_active_at` 六项，与
      `connectors/platformusers/upstream.go` 已核对过的 `sub2apiUserItem`
      逐字一致。若观测到的版本比该文件注释的 0.1.133～0.1.183 更新，需要
      额外核对是否新增/删除了字段（dropped 字段列表里出现陌生名字就是信号）。
- [ ] **人工已完成 AdminComplianceGuard 确认的记录**——由人工在此处附证据
      （完成确认的时间、操作人）：`[填入]`。本工具无法验证这一步是否真的
      做过，只能在没做时因为收到 423 而拒绝写出证据（见上表最后一行）。
- [ ] **证明选用端点不是 mock 的源码/实例证据**——
      `docs/evidence/EV-2026-08-27-sub2api-read-survey.md` 已核对 `/admin/users`
      不在该次普查发现的 5 个 mock 端点之列（`/users/:id/usage`、
      `/dashboard/realtime`、`/redeem-codes/stats`、`/groups/:id/stats`、
      `/proxies/:id/stats`）；`cmd/evidence-capture` 的 `mustNotBeMockRoute`
      （`cmd/evidence-capture/plan.go`）额外做了一层机械拒绝：即使未来有人
      给这个工具加了新参数，对 `/api/v1/admin/users/:id/usage` 这类已证实
      mock 端点的请求也无法绕过，见该文件同名测试
      `TestMustNotBeMockRouteBlocksKnownBadRoutes`。

## 审阅人 checklist

- [ ] 已读 `docs/evidence/users-real/sub2api/[timestamp]/README.md` 全文
- [ ] 已核对 `SHA256SUMS`：目录内每个文件重新计算的 sha256 与 `SHA256SUMS`
      里记录的一致（`sha256sum -c SHA256SUMS` 或等价操作）
- [ ] 已确认 `users_page.redacted.json` / `user_detail.redacted.json` 里
      不含任何真实邮箱全文、真实用户名、真实 token、真实完整余额数字——
      邮箱应形如 `zh***@真实域名`，id/用户名应是 `u_`/`name_` 前缀的十六进
      制串，余额应是数字位全部被替换成 0（或 1 打头）的占位串
- [ ] 已核对上游实例版本是否在 `EV-2026-08-27-sub2api-read-survey.md` 记录
      的兼容矩阵内；不在则需要额外的兼容性复核，不能直接批准
- [ ] 已确认 AdminComplianceGuard 前置条件已勾选
- [ ] 已知悉本审批只授权 Task 4（Sub2API real GetUser），不授权 NewAPI、
      reqlog、DailyUsage、Key metadata、invoice、payment 中任何一项

## 决定

批准或驳回都必须回写本仓库才算正式决定（宪法第 19 条），且文本必须明确写出
事件名、目标平台、证据路径与哈希、允许的任务号（设计文档 §0）——下面这一段
填写后连同本文件一起提交，即是回写记录。格式参考
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2 的 `DAILY_USAGE_APPROVAL`/
`KEY_SCOPE_APPROVAL` 条目。

> SUB2_REAL_APPROVAL：`[批准/驳回]`——`[理由，须含证据目录路径与
> SHA256SUMS 摘要]`。授权 Task 4（Sub2API real GetUser），不构成对
> NewAPI/reqlog/DailyUsage/Key metadata/invoice/payment 的批准。

签字：

- 产品：________________________　日期：__________
- 安全：________________________　日期：__________

## 本审批如何生效

本审批不是代码里的一个开关——Task 4 的 real reader
（`connectors/platformusers/sub2api_v2.go`）今天并不存在（`connectors/platformusers`
只有 `fake_v2.go` 实现 v2 capability；见
`docs/handoffs/slices/XM-USERS-V2-real-detail.md`）。本审批批准的是"允许开始
实现"：实现该文件的 PR 必须在提交信息或 Handoff 里引用本文件与上表的证据路径
及哈希，由人工合入 `release/v0.1-launch` 时核对一致后才能合入——这是今天唯一
的强制点，详见 `docs/runbooks/USERS-REAL-APPROVAL.md`"审批如何生效"一节。
