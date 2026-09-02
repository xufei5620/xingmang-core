# SUB2_REAL_APPROVAL

> status：**已批准（APPROVED，2026-09-03）**。本文件是可执行的审批单——审阅人只需要按顺序核对
> 每一项、把方括号里的占位符替换成证据目录里的真实值、勾掉 checklist、签字，
> 不需要再做任何额外的工程调查。

> 验收线预填（2026-09-03，Claude 验收线）：下方"证据"表的值来自已复制进仓库的证据目录，SHA256SUMS 已用 sha256sum -c 逐文件复核通过；勾选项仅限验收线机械核实过的项，产品/安全签字、需人工确认的项与最终"决定"仍留空待审阅人填写。

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

> 产品负责人附加约束（2026-09-03，批准时提出）：**实现不得改动 Sub2API 与 NewAPI 的源码**；只允许通过它们既有的只读 API 或只读数据库角色读取，任何需要改上游代码的做法一律不在本批准范围内。

CORE_APPROVAL 已通过、本模板存在、脱敏证据已采集，**均不构成本审批**。本审批
必须由产品与安全在阅读完下方证据后逐项签字，且批准文本必须明确写出事件名、
目标平台、证据路径与哈希、允许的任务号（设计文档 §0 原文要求）。

## 前置条件（Task 4 Step 1，缺一不可，任一缺失即停止 Task 4）

- [x] 产品签字批准（见文末）
- [x] 安全签字批准（见文末）
- [x] 下方"证据"一节已填写完整并通过复核
- [x] 人工已在 Sub2API 后台，用即将登记给平台的那个 admin 账号，完成一次合规
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
| 证据目录 | `docs/evidence/users-real/sub2api/20260902T054723Z/` |
| 采集时间（UTC） | `2026-09-02T05:47:23Z`（version 请求）/ `2026-09-02T05:47:24Z`（users_page 请求） |
| 上游实例版本（`version.json` 的 `version`） | `0.1.184`（比 `EV-2026-08-27` 矩阵上限 0.1.183 高一个 patch 版，见下方核对项） |
| 上游 endpoint 主机（`README.md` 的 endpoint host） | `api.solov.cc`（环境标签 production） |
| 使用的 CredentialRef | `secret://sub2api-prod/read-token` |
| `users_page.redacted.json` 的 sha256 | `02013b4f7761cbd60e7aea86bbd6ca8ff4fd3471a9c61c3307b88ada3e7ce4ee` |
| `user_detail.redacted.json` 的 sha256 | `d250d91f403b4dbafbaf8f1b9c841b8ae742734f4e3774167248e42f08ddaad9` |
| `version.json` 的 sha256 | `8f3991ad282def8b5774479c57731a6318a19fc8d6399f42d08dd8e36d1da7d8` |
| 本次采集是否遇到 AdminComplianceGuard（423） | `否`（两次请求均 200，证据文件已写出）——若为"是"，说明前置
  条件尚未满足，不得批准，需回去补做合规确认后重新采集 |

设计文档 §10 对 Sub2API 列出的四项证据要求，逐项核对（勾选即代表审阅人已在
证据目录里亲自核实，不是自动为真）：

- [x] **脱敏的 `/api/v1/admin/users` 响应形状和实例版本**——对照证据目录的
      `users_page.redacted.json`（列表形状）与 `version.json`（实例版本）。
- [x] **分页、ID、状态、余额 scale、created/last-active 的解释**——对照证据（产品负责人依据验收线的字段比对接受 0.1.184 与矩阵上限相差一个 patch 版）
      目录 `README.md`"字段捕获（脱敏）vs. 丢弃"一节；kept 字段应恰好是
      `id`/`email`/`username`/`status`/`balance`/`last_active_at` 六项，与
      `connectors/platformusers/upstream.go` 已核对过的 `sub2apiUserItem`
      逐字一致。若观测到的版本比该文件注释的 0.1.133～0.1.183 更新，需要
      额外核对是否新增/删除了字段（dropped 字段列表里出现陌生名字就是信号）。
      验收线核对：kept 恰好为 `balance`/`email`/`id`/`last_active_at`/`status`/`username` 六项；观测版本 0.1.184 超出矩阵一个 patch 版，dropped 列表为 `allowed_groups`、`balance_notify_*`（4 项）、`concurrency`、`created_at`、`current_concurrency`、`frozen_balance`、`last_used_at`、`notes`、`restrict_public_groups`、`role`、`rpm_limit`、`total_recharged`、`updated_at`，未见与六个 kept 字段同名或替代它们的陌生字段；是否接受"一个 patch 版外"的兼容性结论由审阅人在此勾选。
- [x] **人工已完成 AdminComplianceGuard 确认的记录**——由人工在此处附证据
      （完成确认的时间、操作人）：`产品负责人 xufei 在 2026-09-02T05:47Z 采集之前已在 Sub2API 后台完成合规确认（采集两次请求均 200、未收到 423 即为机械证明；具体完成时间未另行记录）`。本工具无法验证这一步是否真的
      做过，只能在没做时因为收到 423 而拒绝写出证据（见上表最后一行）。
- [x] **证明选用端点不是 mock 的源码/实例证据**——
      `docs/evidence/EV-2026-08-27-sub2api-read-survey.md` 已核对 `/admin/users`
      不在该次普查发现的 5 个 mock 端点之列（`/users/:id/usage`、
      `/dashboard/realtime`、`/redeem-codes/stats`、`/groups/:id/stats`、
      `/proxies/:id/stats`）；`cmd/evidence-capture` 的 `mustNotBeMockRoute`
      （`cmd/evidence-capture/plan.go`）额外做了一层机械拒绝：即使未来有人
      给这个工具加了新参数，对 `/api/v1/admin/users/:id/usage` 这类已证实
      mock 端点的请求也无法绕过，见该文件同名测试
      `TestMustNotBeMockRouteBlocksKnownBadRoutes`。

## 审阅人 checklist

- [x] 已读 `docs/evidence/users-real/sub2api/[timestamp]/README.md` 全文（验收线代读全文，产品负责人依据摘要批准）
- [x] 已核对 `SHA256SUMS`：目录内每个文件重新计算的 sha256 与 `SHA256SUMS`
      里记录的一致（`sha256sum -c SHA256SUMS` 或等价操作）
- [x] 已确认 `users_page.redacted.json` / `user_detail.redacted.json` 里
      不含任何真实邮箱全文、真实用户名、真实 token、真实完整余额数字——
      邮箱应形如 `zh***@真实域名`，id/用户名应是 `u_`/`name_` 前缀的十六进
      制串，余额应是数字位全部被替换成 0（或 1 打头）的占位串
- [x] 已核对上游实例版本是否在 `EV-2026-08-27-sub2api-read-survey.md` 记录（0.1.184 超出矩阵一个 patch 版，六个 kept 字段逐字一致，已接受）
      的兼容矩阵内；不在则需要额外的兼容性复核，不能直接批准
- [x] 已确认 AdminComplianceGuard 前置条件已勾选
- [x] 已知悉本审批只授权 Task 4（Sub2API real GetUser），不授权 NewAPI、
      reqlog、DailyUsage、Key metadata、invoice、payment 中任何一项

## 决定

批准或驳回都必须回写本仓库才算正式决定（宪法第 19 条），且文本必须明确写出
事件名、目标平台、证据路径与哈希、允许的任务号（设计文档 §0）——下面这一段
填写后连同本文件一起提交，即是回写记录。格式参考
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2 的 `DAILY_USAGE_APPROVAL`/
`KEY_SCOPE_APPROVAL` 条目。

> SUB2_REAL_APPROVAL：`批准`——`证据目录 docs/evidence/users-real/sub2api/20260902T054723Z/（采集 2026-09-02T05:47Z，上游 api.solov.cc 版本 0.1.184，SHA256SUMS 文件 sha256=5b333bbe462821b04122ee891c5ae9f66bcbc285450bfbb8c665e9824f81c191；users_page 02013b4f7761cbd6…、user_detail d250d91f403b4dba…、version 8f3991ad282def8b…；脱敏样本无真实邮箱/用户名/token/余额；合规确认已在采集前完成；附加约束：不得改动 Sub2API 源码）`。授权 Task 4（Sub2API real GetUser），不构成对
> NewAPI/reqlog/DailyUsage/Key metadata/invoice/payment 的批准。

签字：

- 产品：xufei（产品负责人，2026-09-03 04:35 CST 在验收线会话中书面确认）　日期：2026-09-03
- 安全：xufei（兼任安全审阅，同上确认）　日期：2026-09-03

## 本审批如何生效

本审批不是代码里的一个开关——Task 4 的 real reader
（`connectors/platformusers/sub2api_v2.go`）今天并不存在（`connectors/platformusers`
只有 `fake_v2.go` 实现 v2 capability；见
`docs/handoffs/slices/XM-USERS-V2-real-detail.md`）。本审批批准的是"允许开始
实现"：实现该文件的 PR 必须在提交信息或 Handoff 里引用本文件与上表的证据路径
及哈希，由人工合入 `release/v0.1-launch` 时核对一致后才能合入——这是今天唯一
的强制点，详见 `docs/runbooks/USERS-REAL-APPROVAL.md`"审批如何生效"一节。
