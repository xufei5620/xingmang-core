# REQLOG_USERREF_APPROVAL

> status：待审批（PENDING）。本文件是可执行的审批单——审阅人只需要按顺序核对
> 每一项、把方括号里的占位符替换成证据目录里的真实值、勾掉 checklist、签字。
> **在填写前请先读"头条发现"一节**——它会影响能批准的范围。

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

## 前置条件（Task 8，缺一不可）

- [ ] 产品签字批准（见文末，须注明选择"窄批准"还是"宽批准"）
- [ ] 安全签字批准（见文末，须对"宽批准"分支额外评估
      `cmd/reqlog-recorder/tokenmap.go` 直连数据库这条既有链路的风险是否
      可以承载更高信任等级的数据，或要求先立 ADR/Change Request）
- [ ] 下方"证据"一节已填写完整并通过复核

## 证据

由 `cmd/evidence-capture reqlog --data-dir ... --tokenmap ...` 生成到
`docs/evidence/users-real/reqlog/<timestamp>/`；本文件只引用路径与哈希，
**不复制其内容**。数据来源是人工从服务器复制下来的本地文件副本
（`/root/reqlog/data`、`/root/reqlog/tokenmap.json`，见
`docs/runbooks/USERS-REAL-APPROVAL.md`），本工具本身不连接服务器或任何数据库。

| 字段 | 值 |
|---|---|
| 证据目录 | `docs/evidence/users-real/reqlog/[填入时间戳目录名]/` |
| 采集时间（UTC） | `[填入]` |
| 观测到的日目录数量、最旧/最新日期 | `[填入，见 retention_and_association.json]` |
| 观测跨度是否在配置保留期内 | `[是/否，填入]` |
| tokenmap 总条目数 | `[填入]` |
| tokenmap 是否携带 source_user_id | 否（schema 级别事实，见上方"头条发现"） |
| 记录关联结果：已关联 / 无前缀 / 前缀未命中 | `[填入三个计数]` |
| `index_sample.redacted.jsonl` 的 sha256 | `[填入，见 SHA256SUMS]` |
| `tokenmap_shape.redacted.json` 的 sha256 | `[填入，见 SHA256SUMS]` |
| `retention_and_association.json` 的 sha256 | `[填入，见 SHA256SUMS]` |

设计文档 §10 对 reqlog 列出的证据要求，逐项核对：

- [ ] **真实 API/源码**——`connectors/reqlog/file_client.go`、
      `cmd/reqlog-recorder/tokenmap.go`、`internal/platform/reqlogformat/record.go`
      是已合入、正在生产使用的源码（不是 DRAFT 猜测），`cmd/evidence-capture`
      的解析直接复用 `reqlogformat.Record` 这个类型，不是重新猜测格式。
- [ ] **token 映射能否给出 platform + source user ID**——不能，见上方"头条
      发现"，`tokenmap_shape.redacted.json` 的 `carries_source_user_id`
      字段恒为 `false`。
- [ ] **retention、cursor、watermark、partial 与统计语义**——
      `retention_and_association.json` 的 `retention` 一节给出观测到的日
      目录跨度与配置保留期（默认 30 天，`connectors/reqlog.RetentionDays`，
      注意该常量本身标注为"未对真实部署核对"的 DRAFT 值）；`cursor_semantics`
      字段是从源码读出的事实（游标是 `"reqlog:<offset>"`，每次查询都全量
      重扫再排序后套用 offset，对并发写入不稳定，与 `XM-USERS-REAL.md`
      记录的 Sub2API/NewAPI offset 分页同一类风险），不是从这次采样数据
      推导出来的，因此不会随不同的 tokenmap/index 样本而改变。
- [ ] **未关联记录的计数和处置**——`retention_and_association.json` 的
      `association` 一节给出三个计数（已关联/无前缀/前缀未命中）；"处置"
      指的是 `connectors/reqlog/file_client.go` 的 `resolveUsername` 对
      未关联记录一律返回空字符串（不用前缀顶替），这一行为本审批不改变。

## 审阅人 checklist

- [ ] 已读 `docs/evidence/users-real/reqlog/[timestamp]/README.md` 全文，
      尤其是"头条发现"一节
- [ ] 已核对 `SHA256SUMS`：目录内每个文件重新计算的 sha256 与 `SHA256SUMS`
      里记录的一致
- [ ] 已确认 `index_sample.redacted.jsonl` 里不含任何真实 IP 全量、真实
      token 前缀、真实用户名/邮箱、`preview`/`end_note` 或任何请求/响应
      正文片段
- [ ] 已在"头条发现"一节的两种范围里明确选择（或都不批），并已理解"宽
      批准"分支需要额外的 ADR/Change Request 才能动
      `cmd/reqlog-recorder/tokenmap.go` 的导出查询
- [ ] 已知悉本审批不授权 invoice、payment、本地 link 表，也不授权
      Sub2API/NewAPI/DailyUsage/Key metadata 的任何 real 实现

## 决定

批准或驳回都必须回写本仓库才算正式决定（宪法第 19 条），且文本必须明确写出
事件名、批准范围（窄/宽）、证据路径与哈希、允许的任务号——下面这一段填写后
连同本文件一起提交，即是回写记录。格式参考
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2 的 `DAILY_USAGE_APPROVAL`/
`KEY_SCOPE_APPROVAL` 条目。

> REQLOG_USERREF_APPROVAL：`[批准（窄）/批准（宽，另需 ADR/CR）/驳回]`——
> `[理由，须含证据目录路径与 SHA256SUMS 摘要，若选择"宽"须注明后续 ADR/CR
> 的编号或待办]`。授权 Task 8 的对应范围，不构成对 invoice/payment/本地
> link 表/Sub2API/NewAPI/DailyUsage/Key metadata 的批准。

签字：

- 产品：________________________　日期：__________
- 安全：________________________　日期：__________

## 本审批如何生效

本审批不是代码里的一个开关——`connectors/reqlog` 的 `RequestLogSummary`
今天没有 `User *UserRef` 字段（契约仍是设计文档 §6.1 描述的未来状态）。本审批
批准的是"允许开始实现"（按上方选择的范围）：实现该改动的 PR 必须在提交信息
或 Handoff 里引用本文件与上表的证据路径及哈希，由人工合入
`release/v0.1-launch` 时核对一致后才能合入，详见
`docs/runbooks/USERS-REAL-APPROVAL.md`"审批如何生效"一节。
