# CR-0008：reqlog tokenmap 导出补选上游用户 ID（稳定 UserRef 的数据前提）

## 历史状态摘要（本地记录核对截至 2026-09-10T20:11:23Z）

历史实现状态：[实现交接](../handoffs/slices/XM-REQLOG-TOKENMAP-V2.md)及[验收日志](../handoffs/ACCEPTANCE-LOG.md)记录实现 `6eba823` 已合入，并先后记有 2026-09-03 tokenmap 的 `3593/3593` 条非空 ID、后补索引匹配 `6116/6116` 的统计。原文 `implemented-pending-verification` 与 `implemented-verified` 是不同阶段的旧摘要；这些文件形状/索引匹配统计不能替代真实读侧 `RequestLogSummary.User` 的 ≥99% 验收。按[现有运行手册的证据边界](../runbooks/REQLOG-RECORDER.md)，本次未找到足以证明该运行时指标的本地原始验收产物，该项尚未核实；不据旧标签推断当前生产已通过。

本摘要仅核对本地记录；当前生产状态未重新核实，不构成本次执行或上线授权。

## 原始历史记录（原文保留）
以下全部原文（包括状态、确认、执行顺序、回滚命令与旧工作树路径）均为当时的历史快照；各条记录按原日期理解，不作为当前状态或本次执行指令。

> 状态：**implemented-verified（2026-09-03，tokenmap.v2.json 3593/3593 解析出 user_id）**；（2026-09-03，分支
> `ai/claude/XM-REQLOG-TOKENMAP-V2`）。优先级 P2（解除 platform-user-read-v2 设计文档 Task 8 的数据
> 前提；不阻塞发布）。
> 依据：`docs/approvals/REQLOG_USERREF_APPROVAL.md`（APPROVED，2026-09-03，选择"宽批准，另需 ADR/CR"，
> 决定原文要求"在该 CR 批准前不得改动 `cmd/reqlog-recorder/tokenmap.go` 的导出查询"）；产品负责人同日
> 附加约束：**不得改动 Sub2API 与 NewAPI 源码**，只允许经既有只读 API 或只读数据库角色读取。
> ADR-020「请求日志记录器的上游库只读接入」已于 2026-09-03 被接受，本 CR 的实现前置条件已满足。
> 实现细节、门禁结果与未验证事项见 `docs/handoffs/slices/XM-REQLOG-TOKENMAP-V2.md`——**验收标准第 1
> 条（服务器上 99% 解出非空 User）需要连服务器的会话验证，本次实现未验证**。

## 发起方
验收线（准备 `REQLOG_USERREF_APPROVAL` 审批证据与回写时的直接发现；本 CR 正是该审批要求的前置变更单）。

## 接收方
平台线（xingmang-platform）。开票线（invoice-system）、Sub2API、NewAPI 均不涉及——本 CR 全部改动落在
`cmd/reqlog-recorder`、`connectors/reqlog`、`cmd/evidence-capture` 三处平台自有代码。

## 目标资源
- `cmd/reqlog-recorder/tokenmap.go` 的 `refreshTokenMap` 导出查询（两条只读 SQL）；
- tokenmap 磁盘格式（`connectors/reqlog/file_client.go` 的 `FileConfig`/`loadTokenMap`/`resolveUsername`）；
- `connectors/reqlog/contract.go` 的 `RequestLogSummary`/`ListFilter`；
- `cmd/evidence-capture/reqlog.go` 的 `carries_source_user_id` 字段计算；
- 部署：`docs/runbooks/REQLOG-RECORDER.md`（记录代理与容器化 `platform-api` 各自独立部署、独立升级）。

## 背景/问题
`docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md` §6.1 要求 reqlog 的
`RequestLogSummary` 携带稳定 `UserRef{Platform, ID}`，不能靠 `Username`/`TokenPrefix` 推导身份
（"当前 Username + TokenPrefix 不合格"）。`REQLOG_USERREF_APPROVAL` 审批核实：这在今天的数据形状下
做不到——是 schema 级别事实，不是某次采样运气不好。

`tokenmap.json` 是扁平的 `map[string]string`，键是 token 前缀，值是 `"<用户名或邮箱>@<来源>"`
（`cmd/reqlog-recorder/tokenmap.go:43` 起的 `refreshTokenMap`）。两条导出 SQL 都已经 `JOIN users`，
但只 `SELECT` 了用户名/邮箱，从未选出已经 JOIN 到的 `u.id`：

- NewAPI（第 48 行）：`select concat('sk-', left(t.key,17)), u.username from tokens t join users u on u.id=t.user_id;`
- Sub2API（第 57 行）：`select left(k.key,20), coalesce(nullif(u.username,''), u.email) from api_keys k join users u on u.id=k.user_id;`

生产采集的证据（数字见下"证据"一节）证实这一空白在真实 tokenmap 上同样成立，与
`docs/handoffs/slices/XM-USERS-V2-real-detail.md` 的 follow_ups 纯读源码独立得出的结论一致。

## 证据
- `docs/approvals/REQLOG_USERREF_APPROVAL.md`：产品与安全 2026-09-03 签字批准"宽（另需 ADR/CR）"，
  明确"在该 CR 批准前不得改动 `cmd/reqlog-recorder/tokenmap.go` 的导出查询"；
- `docs/evidence/users-real/reqlog/20260902T190305Z/`：`tokenmap_shape.redacted.json` 给出
  `total_entries=3572`（`suffix_sub2api=3333`、`suffix_newapi=239`、`malformed_suffix=0`、
  `carries_source_user_id=false`）；`retention_and_association.json` 给出总记录 116880，已关联
  116112 / 无前缀 443 / 前缀未命中 325；
- `cmd/reqlog-recorder/tokenmap.go:43-63`（`refreshTokenMap`/`parsePsqlTSV`）：两条导出 SQL 原文与
  写入格式；
- `connectors/reqlog/file_client.go:527`（`resolveUsername`）与 `cmd/evidence-capture/reqlog.go:166-179`
  （`carries_source_user_id`/`finding` 硬编码为 `false`）：今天读侧如何解出 `Username`，以及证据工具
  目前不按文件内容计算该字段。

## 变更范围

**1. 导出 SQL 各加一列（`cmd/reqlog-recorder/tokenmap.go`）**——两条查询已经 `JOIN` 到 `u.id`，只需
多 `SELECT` 一列，不改 `JOIN`/`WHERE`，不新增语句、不新增连接目标：

```sql
-- NewAPI（原第 48 行）
select concat('sk-', left(t.key,17)), u.username, u.id
from tokens t join users u on u.id=t.user_id;

-- Sub2API（原第 57 行）
select left(k.key,20), coalesce(nullif(u.username,''), u.email), u.id
from api_keys k join users u on u.id=k.user_id;
```

`parsePsqlTSV` 相应改成解析三列（前缀、标识、上游 ID）。两条 `docker exec ... psql` 命令本身不变。

**2. tokenmap 格式：新增并行文件 `tokenmap.v2.json`，不改 `tokenmap.json`。** 与"原地升版本"（同一
路径、值从字符串换成对象、加顶层 `schema_version`）相比，选并行文件的理由：记录代理（宿主机 systemd）
与连接器（容器化 `platform-api`）独立部署、升级顺序不确定（`docs/runbooks/REQLOG-RECORDER.md`"前提"
一节："两步互相独立，可以分开验证"）。原地升版本一旦记录代理先升级，旧连接器的
`json.Unmarshal(b, &m)`（`m` 是 `map[string]string`）会对**整份文件**类型不匹配而解析失败，升级窗口内
全部请求的 `Username` 显示消失——比"看不到新 ID"严重得多的回归。并行文件让 `tokenmap.json` 字节级
不变，新旧连接器都能一直读到它；新连接器可选读取 `tokenmap.v2.json`，文件缺失或解析失败按今天
"映射不到"同一容错路径处理（`User` 为 `nil`），不是新错误类别。

v2 文件形状：

```json
{
  "schema_version": 2,
  "entries": {
    "<token_prefix>": {"username": "<用户名或邮箱>", "source": "sub2api", "user_id": "<上游数字/不透明ID>"}
  }
}
```

**3. `connectors/reqlog/file_client.go`**：`FileConfig` 新增可选字段 `TokenMapV2Path`（同
`TokenMapPath` 一样"留空即不生效"）；新增 v2 解析与 `resolveUserRef` 函数（形状剥不出合法结构就判
"映射不到"，同 `resolveUsername` 纪律）；`summaryFromRecord` 填充 `RequestLogSummary.User`。是否需要
新的可选能力常量（同 `CapabilityRoutingRead`/`CapabilityBillingRead` 先例）留给实现切片决定，本 CR
只要求"能不能给出 `UserRef`"的诚实声明，不规定具体常量命名。

**4. `cmd/evidence-capture/reqlog.go`**：`carries_source_user_id`（第 172 行）与 `schema`/`finding`
文案改成按实际读到的 tokenmap 文件计算——读到 v2 文件且 `user_id` 非空时报告 `true`，否则保持今天的
`false` 与既有 finding 文案。

**5. 部署**：记录代理二进制按 `docs/runbooks/REQLOG-RECORDER.md` 第 1～3 步重新构建、替换、核对属组
权限（`tokenmap.v2.json` 沿用同一套可配置权限位/属组）；不涉及平台数据库迁移——`user_id` 全程只经
文件系统传递，不落任何平台数据库表。

## 契约变化
`RequestLogSummary`（`connectors/reqlog/contract.go`）新增：

```go
// User 是本条记录关联到的稳定上游身份；nil 表示未关联，
// 不能由 Username/TokenPrefix 反推（设计文档 §6.1）。
User *platformusers.UserRef
```

`ListFilter` 新增：

```go
// User 精确过滤；User.Platform 必须与 Source 相等（同 §6.1 判据）。
User *platformusers.UserRef
```

`Username`/`TokenPrefix` 两个既有字段不删除、行为不变——`User` 是新增的第三条身份线索，不是替换。

## 兼容性/安全
- **只读边界**：本 CR 全程只给两条既有 `SELECT` 语句各加一列，不新增语句、不新增连接目标、不引入
  任何写路径；"导出路径不含写语句"应作为一条可 grep 断言的测试写进实现切片（验收标准第 4 条）。
- **产品负责人硬约束（2026-09-03）**：不得改动 Sub2API 与 NewAPI 源码。本 CR 改的两条 SQL 是
  `cmd/reqlog-recorder`（平台自有代码）里拼的查询字符串，经既有只读连接发出；若两个上游系统存在只读
  数据库角色，导出连接应改用该角色，但角色切换是运维配置，不在本 CR 强制的代码改动范围内。
- **信任等级升级需要 ADR**：`docs/adr/` 现有 ADR-001 至 ADR-019 中，ADR-014（CredentialRef）与
  ADR-018（开票与平台数据通道隔离的四道只读闸）都只覆盖经 Connector 建立的只读账号连接；记录代理的
  `docker exec ... psql` 不是 Connector，两条 ADR 都不覆盖这条路径（`XM-REQLOG-MERGE.md` 风险清单
  第 1 条与 `XM-USERS-V2-real-detail.md` follow_ups 均已指出这一空白）。`REQLOG_USERREF_APPROVAL` 的
  "宽批准"分支要求"先立 ADR/Change Request"才能把这条链路升级为承载稳定身份关联的数据源。**结论：
  需要新写 ADR-020「请求日志记录器的上游库只读接入」**（`docs/adr/` 现存最大编号为 ADR-019，下一个
  空号是 020），记录该链路的只读边界、与 ADR-014/ADR-018 的关系（延伸而非违反）；该 ADR 须先被接受，
  本 CR 的实现切片才能合入。
- **PII**：`user_id` 是控制台用户详情页已经明文展示的同一数字/不透明 ID
  （`web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx:154` 的 `found.user.id`），不构成新的
  PII 类别；`username`/邮箱继续经 `resolveUsername`/`platformusers.MaskEmail` 同一打码路径。

## 验收标准
1. 服务器上产出一份 `tokenmap.v2.json` 后，抽样核对：前缀命中的记录中至少 99% 能解出非空 `User`。
2. 只提供旧 `tokenmap.json`（无 v2 文件）时，读取行为与本 CR 之前逐字节一致，`User` 恒为 `nil`。
3. `cmd/evidence-capture reqlog` 对同一份 v2 文件报告 `carries_source_user_id=true`；对纯 v1 文件
   仍报告 `false`。
4. 导出路径的实现测试里有一条对生成的 SQL 字符串做"不含 `insert`/`update`/`delete`/`drop` 等写
   关键字"的断言。
5. `contracttest` 覆盖 `User` 为 `nil`（未关联/无 v2 数据）与非 `nil`（已关联）两态，不回归既有
   21 项契约测试。

## 优先级
P2——解除 Task 8（reqlog 稳定 UserRef 面板，已获 `REQLOG_USERREF_APPROVAL` 宽批准）的数据前提，不阻塞
任何已发布或待发布的生产能力，也不阻塞 v0.1 发布线。

## 状态
implemented-pending-verification。ADR-020 已被接受（2026-09-03），实现切片见分支
`ai/claude/XM-REQLOG-TOKENMAP-V2`（`docs/handoffs/slices/XM-REQLOG-TOKENMAP-V2.md`）；验收标准第 1
条需服务器验证，尚未完成。

## 明确不变
- 不新增 invoice、payment 或任何平台本地 link 表；`UserRef` 只在 reqlog 一域内生效，不做跨域自动
  关联（设计文档 §3 判据 2、3）。
- 不改动 Sub2API、NewAPI 任何源码文件（产品负责人硬约束）。
- 不改变 tokenmap 30 天保留期、清理任务、`RetentionDays` 语义与新鲜度口径。
- `tokenmap.json` 的既有格式、路径、生成方式不变；`Username`/`TokenPrefix` 两个既有字段不删除。
- 不新增任何写操作；两条导出 SQL 维持只读 `SELECT`。

## 回滚
本 CR 全部改动是新增列/新增并行文件/新增可空字段，没有数据迁移、没有平台数据库写入。回滚 = 还原
`tokenmap.go` 两条 SQL、停止生成 `tokenmap.v2.json`（连接器因文件缺失自动退回"仅 `Username`"路径）、
把其余改动还原到上一个已发布版本。ADR-020 记录的是事实判断，不必随实现回滚而撤销。

## 确认
- 平台线：验收线，2026-09-03（按 `REQLOG_USERREF_APPROVAL` 的批准条件起草）。
- 开票线：不涉及（n/a）。
- 产品负责人：待确认（含 ADR-020 是否照此结论立项）。

## 执行记录
- 2026-09-03 立单（设计阶段，未派发实现切片；实现前置条件为 ADR-020 被接受）。
- 2026-09-03 ADR-020 被接受（产品负责人"按建议默认接受"四项待拍板问题），实现切片派发。
- 2026-09-03 实现切片完成（分支 `ai/claude/XM-REQLOG-TOKENMAP-V2`）：变更范围 1～5 全部落地，
  验收标准第 2～5 条已通过本地测试验证；第 1 条（服务器上 99% 解出非空 `User`）需连服务器的会话
  验证，本次未验证。细节、门禁结果、偏离与 follow-up 见
  `docs/handoffs/slices/XM-REQLOG-TOKENMAP-V2.md`。
