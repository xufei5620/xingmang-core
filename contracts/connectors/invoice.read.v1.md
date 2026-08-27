# 开票系统只读接入契约 v1（草案）

> **状态：草案（DRAFT）——字段 / 状态集合 / 水位语义 / 金额编码待 CR-0002
> 双方确认，确认前不得被 XM-0029 依赖。**
>
> 依据宪法 20 条（跨线契约须双方确认后方可冻结）与开票线 issue #49。
> 本文档与 `connectors/invoice` 的合并**不等于跨线批准**：开票线随时可能
> 推翻其中任何一项。它现在唯一的用途是让平台侧上层开发不被尚未落地的
> `/readonly/v1/` 端点与 bearer 凭据阻塞。

| 项 | 值 |
|---|---|
| 状态 | **草案（DRAFT），未冻结** |
| Connector Key | `invoice` |
| Contract Version | `1` |
| 实现 | 契约与 Fake：`connectors/invoice`；真实实现：XM-0029 |
| 合规判据 | **必须通过 `connectors/invoice/contracttest` 套件** |
| 上游依据 | `docs/change-requests/CR-0002-invoice-readonly-interface.md`（含 2026-08-27 勘察补充） |
| 冻结前置 | CR-0002「Codex 侧待填」各节补齐 + 双方确认 |

## 待确认项（冻结前必须清空）

| # | 项 | 平台当前假设 | 谁来定 |
|---|---|---|---|
| 1 | `FailedCount` 口径 | Fake 演示用 `rejected + refund_attention` | **开票系统冻结**，平台不自行解释状态集合 |
| 2 | `PendingCount` 口径 | Fake 演示用 `pending_review + needs_changes + manual_issuing + issued_awaiting_document` | **开票系统冻结**，同上 |
| 3 | 金额 wire 层编码 | Go 侧固定 `int64`；HTTP 响应是 JSON number 还是十进制字符串未定 | CR-0002 待确认 |
| 4 | 水位语义 | `observed_at` + `watermark_at`，取值来源见下 | CR-0002 待确认 |
| 5 | 状态集合 | 9 项（勘察所得，DB/Go 一致） | 开票线确认后冻结 |
| 6 | `source_type` 取值集合 | 平台不解释语义，原样透传 | 无需确认（刻意不依赖） |

第 1、2 项尤其要紧：失败率与待办数是看板上最容易被当成事实的两个数字。
平台这边**不替开票业务下定义**——Fake 里那两组状态只是为了让卡片有个非零
的数可渲染，不构成任何口径主张。在开票线给出定义前，任何依赖这两个字段的
告警阈值都不应上线。

## 能力清单

```
invoice.service.version_read    服务版本
invoice.requests.read           开票申请只读投影（分页）
invoice.summary.daily_read      业务日汇总
invoice.health.read             运营健康
```

清单刻意保持最小：只覆盖运营看板真正要展示的东西。实现可以返回**子集**
（旧版本上游少支持几项），但不得返回清单之外的能力。
每一项都必须 `registry.ParseCapability` 可解析且 `IsWrite() == false`。

## 与 CR-0002 的字段映射

CR-0002「建议实现 §1」给的 DTO 白名单 → 平台契约结构体：

| CR-0002 字段 | 平台字段 | 类型 | 说明 |
|---|---|---|---|
| `id` | `InvoiceRequest.ID` | `string` | 上游主键，平台不解释格式 |
| `request_no` | `InvoiceRequest.RequestNo` | `string` | 给人看的申请单号 |
| `status` | `InvoiceRequest.Status` | `Status` | 9 项枚举，见下 |
| `amount_minor` | `InvoiceRequest.AmountMinor` | `int64` | 最小货币单位，禁止 float（待确认项 #3 只影响 wire 层解码） |
| `currency` | `InvoiceRequest.Currency` | `string` | 固定 `CNY`，字段保留 |
| `source_type` | `InvoiceRequest.SourceType` | `string` | 原样透传，平台不解释语义 |
| `submitted_at` | `InvoiceRequest.SubmittedAt` | `time.Time` | UTC |
| `updated_at` | `InvoiceRequest.UpdatedAt` | `time.Time` | UTC；亦为本条记录的水位来源 |
| `observed_at` | `Snapshot.ObservedAt` | `time.Time` | UTC，上游观测时刻 |
| `watermark_at` | `Snapshot.Watermark` | `string` | 见「新鲜度与水位」 |
| （无对应） | `Snapshot.IsPartial` | `bool` | 平台侧标记，上游降级/分页中断时置位 |

CR-0002「建议实现 §3」的日汇总 → `DailySummary`：

| CR-0002 | 平台字段 | 说明 |
|---|---|---|
| `date=YYYY-MM-DD` | `DailySummary.Day` | 按 `Asia/Shanghai` 业务日切分，平台**不做时区换算**，原样透传 |
| `count` | `DailySummary.Count` | |
| `sum(amount_minor)` | `DailySummary.TotalAmountMinor` | + `Currency` |
| 失败数 | `DailySummary.FailedCount` | **口径待确认（#1）** |
| 待处理数 | `DailySummary.PendingCount` | **口径待确认（#2）** |

CR-0002「建议实现 §2」的查询参数 → `ListQuery`：

| 查询参数 | 平台字段 | 说明 |
|---|---|---|
| `from` / `to` | `ListQuery.From` / `.To` | UTC 闭区间；零值=该端不设限；`From > To` 一律**拒绝** |
| `status` | `ListQuery.Status` | 空=不过滤；非空须在 9 项枚举内，否则**拒绝** |
| `limit` | `ListQuery.Limit` | `<=0` 用 50，`>200` 截断到 200 |
| `cursor` | `ListQuery.Cursor` | 不透明；见「分页」 |

## 契约里没有什么：PII

**`profile` / 抬头 / 税号 / 银行账号 / 地址 / 电话 / 邮箱在平台的类型里不存在。**

这是 CR-0002 勘察后的硬要求，起因具体：开票系统现有的
`admin_dto.go:10` 直接内嵌 `domain.InvoiceRequest`，会吐出
`TaxID` / `BankAccount` / `Address` / `Phone` / `Email` 全量 PII。
只读投影必须**新建**、DTO 手写字段白名单、禁止 embed 领域结构。

平台这一侧的防线不是「记得别读那些字段」，而是让那些字段在平台的类型里
根本没有位置——读不到就泄不了。`contracttest` 用反射遍历
`InvoiceRequest` / `RequestPage` / `DailySummary` / `ListQuery` / `Snapshot`
的字段名，命中 `profile` / `tax` / `email` / `phone` / `address` / `bank` /
`account` / `identity` / `title` / `contact` 等词根即失败——
「PII 不进平台」因此是一条会在有人加字段时立刻变红的测试，
而不是一段迟早没人看的注释。

## 状态枚举（9 项）

来自 CR-0002 勘察结论（开票系统 `migrations/0001_init.sql:124-127` 与
`internal/domain/types.go:59-67`，DB 与 Go 两侧一致）：

```
pending_review              待审核
needs_changes               需修改后重提
approved                    审核通过，待开具
rejected                    审核驳回
user_cancelled              用户主动撤销
manual_issuing              人工开具中
issued_awaiting_document    已开具，待回传票据
issued                      已开具并完成
refund_attention            涉退款，需人工关注
```

`ParseStatus` 对未知状态**一律拒绝**，不归入「其他」：上游加了新状态却没走
CR，平台会立刻在契约测试与真实调用里炸出来，而不是把新状态悄悄吞掉，
让日汇总的分子分母长期对不上还没人发现。

## 数据形状与新鲜度

所有读取结果内嵌 `Snapshot{ObservedAt, Watermark, IsPartial}`——
**没有「只返回值不返回新鲜度」的结构**（规格 §9.1 禁止裸数字）。

`RequestPage` 在逐条 `Item` 之外**另带一份页级 `Snapshot`**。理由是空页：
「这一天 0 张发票」与「上游降级所以读到 0 张」在看板上长得一模一样，而
`Items` 为空时逐项快照一个都没有，新鲜度无从判断——正是 §9.1 要堵的裸数字。
页级快照让空页也能回答「这个 0 是什么时候的 0」。

水位来源（CR-0002 勘察，**语义待确认项 #4**）：
`source_economic_stream_watermarks` 的 `min(watermark_at)`；
开票记录自身可用 `max(updated_at)`。

`Snapshot` 是 `invoice` 包自己声明的，**不复用 `sub2api.Snapshot`**：
两个契约独立演进、各自钉住各自上游的版本与语义，共享结构体会让任意一边的
破坏性变更无声地传染到另一边。字段重复几行是刻意付出的代价。

## 分页

游标对调用方**不透明**：不要解析它、不要自己拼一个。契约只保证把
`NextCursor` 原样回传能拿到紧接着的下一页；空 `NextCursor` = 没有更多。

实现应当用 **keyset**（按最后一条记录定位）而非 offset——offset 在上游并发
写入时会漏记录或重复记录，而「不重不漏」是 `contracttest` 会断言的硬要求
（套件用两种页大小各翻一遍全量，逐条比对且查重）。

失效游标（比如换了过滤条件却沿用旧游标）必须**报错**，不得静默从头开始：
静默重来会让调用方把第一页当成第二页处理。

## 四道只读闸（ADR-018）

| 闸 | 落地位置 | 状态 |
|---|---|---|
| 1 拒绝可写连接配置 | `connector.Config.Validate()` | ✅ 复用 XM-0016，有测试 |
| 2 只读凭证 + 只读路由 | Keycloak `invoice-readonly` role + `/readonly/v1/` | ⬜ **CR-0002 待落地（Codex 侧）** |
| 3 每个新连接复核上游只读设置与权限 | XM-0029 真实客户端 | ⬜ **XM-0029 必须落地** |
| 4 Connector 包内无写路径 | `connector.ReadOnlyTransport` + `ReadClient` 接口形状 | ✅ 契约已成型，有测试 |

闸 4 是**机械强制**而非评审约定：`ReadOnlyTransport` 拒绝任何非 GET/HEAD
请求与任何不在 allowlist 内的主机，请求根本不发出；`ReadClient` 接口只有
读方法。开票的审核/开具/退票永远由开票系统自己完成，平台不代劳——
所以本契约不存在也不会有配对的 `WriteClient`。

**XM-0029 的硬性要求**：
- 凭据只经 `secret://invoice-<env>/<name>`（ADR-014），明文不入代码/日志/前端；
- 目标域名精确 allowlist，拒绝重定向；
- 上游若在只读路由上接受了写请求，**立即报错**，不得降级继续。

## 错误映射（ADR-004：不透传供应商原始错误）

| ErrorKind | 触发场景 |
|---|---|
| `unavailable` | 网络不可达、超时、上游 5xx、上下文取消 |
| `auth` | bearer 无效、audience/azp 不匹配、缺 `invoice-readonly` role |
| `rate_limited` | 429 / Retry-After |
| `not_supported` | 上游版本不支持该能力 |
| `bad_response` | 响应格式非法、字段缺失、超大；**以及非法请求参数**（`From > To`、未知状态、坏游标） |
| `forbidden_target` | 目标不在 allowlist |
| `write_attempt` | 只读通道上出现写请求 |

非法请求参数归入 `bad_response` 是与 `sub2api` 对齐的选择（那边的非法业务日
同样归此类）：请求参数与响应都属于「这次交互的数据形状不对」，两个 Connector
用同一个分类，调用方的错误处理才不用按 Connector 分叉。

上游原始错误文本只进服务端日志（Unwrap 链），不进对外错误信息。

## 版本兼容

`Version()` 对不支持的上游版本返回 `Supported=false` 而非报错——
是否 Fail Closed 由调用方按场景决定（读取可降级）。

CR-0002 勘察：开票系统 **`/version` 尚不存在**（最小缺口，建议 Codex 首件做；
`/healthz` 已存在于 `httpapi/server.go:155-157`）。兼容矩阵在 `/version`
落地、XM-0029 接入真实上游后填入。

## 指标输出

`ToObservations()` 把日汇总转成 `ops.Observation`，接进数据新鲜度模型：

```
invoice.requests.daily    count / failed_count / pending_count
invoice.amount.daily      amount_minor_units / currency
```

默认新鲜度阈值 **3600 秒**，比 Sub2API 的 1800 秒宽一倍：开票是人工审核驱动的
低频业务，一小时没有新数据是常态而不是故障。阈值定得比业务节奏还紧，看板就会
长期挂着橙色，然后所有人学会无视它——那比没有阈值更糟。

只从 `DailySummary` 出指标、不从 `ListRequests` 出：逐条开票申请是明细，
明细进详情页而不是进指标表。

上游未提供观测时刻时，`ObservedAt` 保持为空（显示为「未初始化」），
**不用当前时间冒充**。

## 破坏性变更

本契约冻结后，能力清单、数据形状或错误映射的变化必须发布 v2 并保留 v1 兼容期。
**冻结之前**（当前状态）不适用该约束：草案期的字段调整只需在 CR-0002 里记录。
