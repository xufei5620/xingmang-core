# REQLOG_USERREF_APPROVAL

> status：**已批准（APPROVED，2026-09-03）**。本文件是可执行的审批单——审阅人只需要按顺序核对
> 每一项、把方括号里的占位符替换成证据目录里的真实值、勾掉 checklist、签字。
> **在填写前请先读"头条发现"一节**——它会影响能批准的范围。

> 验收线预填（2026-09-03，Claude 验收线）：下方"证据"表的值来自已复制进仓库的证据目录，SHA256SUMS 已用 sha256sum -c 逐文件复核通过；勾选项仅限验收线机械核实过的项，产品/安全签字、需人工确认的项与最终"决定"仍留空待审阅人填写。

- 事件名：`REQLOG_USERREF_APPROVAL`
- 依据：`docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md` §0、
  §6.1、§10、§12；`docs/superpowers/plans/2026-08-28-platform-user-read-v2.md`
  Task 8
- 证据来源：`cmd/evidence-capture reqlog`（用法与安全说明见
  `docs/runbooks/USERS-REAL-APPROVAL.md`；只读本地文件，不连接任何服务器）

## 头条发现：今天的 tokenmap 给不出稳定 UserRef

`cmd/evidence-capture reqlog` 对任意一份 `tokenmap.json` 的**结构**（不是某一次
采样的内容）都会得出同一个结论：`tokenmap.json` 是扁平的
`map[string]string`（键=token 前缀，值=`"<用户名或邮箱>@<source>"`），这个形状
**没有位置容纳上游的数字/不透明 user id**。`cmd/reqlog-recorder/tokenmap.go`
自己的导出 SQL（`select ... u.username from ... join users u on u.id=t.user_id`
一类）只 `SELECT` 了 `username`/`email`，虽然已经 `JOIN` 了 `u.id`，却从未把它
选出来。

这意味着：**在不改 `cmd/reqlog-recorder/tokenmap.go` 的导出查询之前，Task 8
无法交付设计文档要求的 `platform.UserRef{Platform, ID}`**，因为源头数据里根本
没有这个字段。这不是本次采样运气不好，是 schema 级别的事实——见证据目录的
`tokenmap_shape.redacted.json` 与 `README.md`。

这与 `docs/handoffs/slices/XM-USERS-V2-real-detail.md` 的 follow_ups 通过纯读
源码独立得出的结论一致；本工具是用同一个结构性判断，但换成对一份实际
`tokenmap.json` 的程序化核实来重新确认它。

**因此，本审批面对两种范围不同的批准，请审阅人明确选择其一（或都不批）**：

1. **窄批准**：只批准"用现有 tokenmap 能给出的东西"——也就是继续只有
   `Username`（不是稳定 UserRef），Task 8 原定的"reqlog 稳定 UserRef 面板"
   目标事实上做不到，需要回設計文档重新定义 Task 8 的范围或改判为不可行；
2. **宽批准**：批准以本审批为前提，另立一个改动
   `cmd/reqlog-recorder/tokenmap.go` 导出查询（多 `SELECT` 一列 id）的变更，
   但那条链路本身是已知的架构债务（`docker exec ... psql` 直连上游数据库，
   绕开 Connector/CredentialRef 与四道只读闸，见
   `docs/handoffs/slices/XM-REQLOG-MERGE.md` 风险清单），把它升级成承载
   "稳定身份关联"这一更高信任等级的数据，本身值得单独的 ADR 或 Change
   Request，不应该被当作"批一下就能做"的小事顺带批准。

本审批**不**替审阅人做这个选择；下方"决定"一节要求明确写出选的是哪一种，
或两种都不批。

## 授权范围

| | 内容 |
|---|---|
| **本次批准的（若批准）** | Task 8：reqlog 稳定 UserRef，范围以上方"头条发现"一节审阅人选择的分支为准 |
| **明确不授权** | invoice、payment、本地 link 表；Sub2API/NewAPI/DailyUsage/Key metadata 的任何 real 实现；本片（XM-USERS-REAL-EVIDENCE0）本身不实现 Task 8 |

> 产品负责人附加约束（2026-09-03，批准时提出）：**实现不得改动 Sub2API 与 NewAPI 的源码**；只允许通过它们既有的只读 API 或只读数据库角色读取，任何需要改上游代码的做法一律不在本批准范围内。

## 前置条件（Task 8，缺一不可）

- [x] 产品签字批准（见文末，须注明选择"窄批准"还是"宽批准"）
- [x] 安全签字批准（见文末，须对"宽批准"分支额外评估
      `cmd/reqlog-recorder/tokenmap.go` 直连数据库这条既有链路的风险是否
      可以承载更高信任等级的数据，或要求先立 ADR/Change Request）
- [x] 下方"证据"一节已填写完整并通过复核

## 证据

由 `cmd/evidence-capture reqlog --data-dir ... --tokenmap ...` 生成到
`docs/evidence/users-real/reqlog/<timestamp>/`；本文件只引用路径与哈希，
**不复制其内容**。数据来源是人工从服务器复制下来的本地文件副本
（`/root/reqlog/data`、`/root/reqlog/tokenmap.json`，见
`docs/runbooks/USERS-REAL-APPROVAL.md`），本工具本身不连接服务器或任何数据库。

| 字段 | 值 |
|---|---|
| 证据目录 | `docs/evidence/users-real/reqlog/20260902T190305Z/` |
| 采集时间（UTC） | `2026-09-02T19:03:05Z`（重采集：首版样本的 `token_prefix_hash` 字段名触发 secret-scan 误报，工具字段改名为 `prefix_pseudonym` 后重跑） |
| 观测到的日目录数量、最旧/最新日期 | 10 个日目录，最旧 `20260825`，最新 `20260903`（跨度 10 天） |
| 观测跨度是否在配置保留期内 | `是`（10 天 ≤ 配置保留期 30 天；注意 30 天是未对真实部署核对的 DRAFT 常量） |
| tokenmap 总条目数 | `3572`（`@sub2api` 3333、`@newapi` 239、后缀异常 0） |
| tokenmap 是否携带 source_user_id | 否（schema 级别事实，见上方"头条发现"） |
| 记录关联结果：已关联 / 无前缀 / 前缀未命中 | 已关联 `116112` / 无前缀 `443` / 前缀未命中 `325`（共 116880 条） |
| `index_sample.redacted.jsonl` 的 sha256 | `c03aefc189c7efbb432717ddd0018fc05fec0c0e8aa5fdc4872e9bfd6d0a1011` |
| `tokenmap_shape.redacted.json` 的 sha256 | `06e9952e71ccc0553c3f235fa78be2cb2877bd08211c1e542e1bf5539cd1caba` |
| `retention_and_association.json` 的 sha256 | `35ed0aec77bf4f091c567926c0df5404472f24391ebf0af7a686ed980fecd6b1` |

设计文档 §10 对 reqlog 列出的证据要求，逐项核对：

- [x] **真实 API/源码**——`connectors/reqlog/file_client.go`、
      `cmd/reqlog-recorder/tokenmap.go`、`internal/platform/reqlogformat/record.go`
      是已合入、正在生产使用的源码（不是 DRAFT 猜测），`cmd/evidence-capture`
      的解析直接复用 `reqlogformat.Record` 这个类型，不是重新猜测格式。
- [x] **token 映射能否给出 platform + source user ID**——不能，见上方"头条
      发现"，`tokenmap_shape.redacted.json` 的 `carries_source_user_id`
      字段恒为 `false`。
- [x] **retention、cursor、watermark、partial 与统计语义**——
      `retention_and_association.json` 的 `retention` 一节给出观测到的日
      目录跨度与配置保留期（默认 30 天，`connectors/reqlog.RetentionDays`，
      注意该常量本身标注为"未对真实部署核对"的 DRAFT 值）；`cursor_semantics`
      字段是从源码读出的事实（游标是 `"reqlog:<offset>"`，每次查询都全量
      重扫再排序后套用 offset，对并发写入不稳定，与 `XM-USERS-REAL.md`
      记录的 Sub2API/NewAPI offset 分页同一类风险），不是从这次采样数据
      推导出来的，因此不会随不同的 tokenmap/index 样本而改变。
- [x] **未关联记录的计数和处置**——`retention_and_association.json` 的
      `association` 一节给出三个计数（已关联/无前缀/前缀未命中）；"处置"
      指的是 `connectors/reqlog/file_client.go` 的 `resolveUsername` 对
      未关联记录一律返回空字符串（不用前缀顶替），这一行为本审批不改变。

## 审阅人 checklist

- [x] 已读 `docs/evidence/users-real/reqlog/[timestamp]/README.md` 全文，（验收线代读全文并向产品负责人摘要"头条发现"）
      尤其是"头条发现"一节
- [x] 已核对 `SHA256SUMS`：目录内每个文件重新计算的 sha256 与 `SHA256SUMS`
      里记录的一致
- [x] 已确认 `index_sample.redacted.jsonl` 里不含任何真实 IP 全量、真实
      token 前缀、真实用户名/邮箱、`preview`/`end_note` 或任何请求/响应
      正文片段
- [x] 已在"头条发现"一节的两种范围里明确选择（或都不批），并已理解"宽（选择：宽批准）
      批准"分支需要额外的 ADR/Change Request 才能动
      `cmd/reqlog-recorder/tokenmap.go` 的导出查询
- [x] 已知悉本审批不授权 invoice、payment、本地 link 表，也不授权
      Sub2API/NewAPI/DailyUsage/Key metadata 的任何 real 实现

## 决定

批准或驳回都必须回写本仓库才算正式决定（宪法第 19 条），且文本必须明确写出
事件名、批准范围（窄/宽）、证据路径与哈希、允许的任务号——下面这一段填写后
连同本文件一起提交，即是回写记录。格式参考
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2 的 `DAILY_USAGE_APPROVAL`/
`KEY_SCOPE_APPROVAL` 条目。

> REQLOG_USERREF_APPROVAL：`批准（宽，另需 ADR/CR）`——
> `证据目录 docs/evidence/users-real/reqlog/20260902T190305Z/（采集 2026-09-02T19:03Z，SHA256SUMS 文件 sha256=2965c868027e99223cb9d1cc845b6bbbc1daf0e10d2bc3c999701876842708ff；tokenmap 3572 条、schema 无 source_user_id；记录 116880 条，已关联 116112 / 无前缀 443 / 前缀未命中 325）。后续待办：立 CR-0008「reqlog tokenmap 导出补选上游 user id」，在该 CR 批准前不得改动 cmd/reqlog-recorder/tokenmap.go 的导出查询；该导出只允许以只读方式读取上游数据库，且附加约束：不得改动 Sub2API 与 NewAPI 源码`。授权 Task 8 的对应范围，不构成对 invoice/payment/本地
> link 表/Sub2API/NewAPI/DailyUsage/Key metadata 的批准。

签字：

- 产品：xufei（产品负责人，2026-09-03 04:35 CST 在验收线会话中书面确认）　日期：2026-09-03
- 安全：xufei（兼任安全审阅，同上确认）　日期：2026-09-03

## 本审批如何生效

本审批不是代码里的一个开关——`connectors/reqlog` 的 `RequestLogSummary`
今天没有 `User *UserRef` 字段（契约仍是设计文档 §6.1 描述的未来状态）。本审批
批准的是"允许开始实现"（按上方选择的范围）：实现该改动的 PR 必须在提交信息
或 Handoff 里引用本文件与上表的证据路径及哈希，由人工合入
`release/v0.1-launch` 时核对一致后才能合入，详见
`docs/runbooks/USERS-REAL-APPROVAL.md`"审批如何生效"一节。
