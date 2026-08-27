# NewAPI 只读接入契约 v1

| 项 | 值 |
|---|---|
| 状态 | 冻结（XM-0035 起生效） |
| Connector Key | `newapi` |
| Contract Version | `1` |
| 实现 | 契约与 Fake：`connectors/newapi`；真实只读客户端：**尚未实现（XM-0038）** |
| 合规判据 | **必须通过 `connectors/newapi/contracttest` 套件**（Fake 已通过，12 项） |
| 上游依据 | 规格 §8.4 NewAPI Connector · Foundation-A 只读范围 |

⚠️ **本契约冻结，但真实数据链路尚未接通。** Foundation-A 阶段 `XM_NEWAPI_MODE`
只有 `fake` 走得通；`real` 会每周期写一条 `not_supported` 的失败观测。
这是刻意的：「这条链路还没接通」是一个事实，看板该看得见它，而不是让 NewAPI
那几格数据静静地不更新（规格 §9.1）。

## 能力清单（规格 §8.4 Foundation-A 只读范围）

```
newapi.service.version_read    服务状态
newapi.users.read              用户数量、余额和消费
newapi.orders.read             充值 / 订阅订单摘要
newapi.channels.read           渠道状态
newapi.models.usage_read       模型状态与使用量
newapi.errors.read             错误率
newapi.health.read             运营健康
```

实现可以返回**子集**（旧版本上游少支持几项），但不得返回清单之外的能力。
每一项都必须 `registry.ParseCapability` 可解析且 `IsWrite() == false`。

规格 §8.2 的能力示例里还有 `newapi.tokens.read` 与 `newapi.models.probe`，
本清单**刻意不收**：两者都不在 §8.4 的 Foundation-A 只读范围内，现在就要来
只会多一份被滥用的面。能力清单不是许愿单。

### 与规格 §8.4 的逐条映射

| §8.4 只读范围 | 契约落点 | 指标键 |
|---|---|---|
| 服务状态 | `ReadClient.Version` + `Health` | —（进 Registry 与连接状态，不进指标表） |
| 用户数量 | `UserStats.TotalUsers` / `ActiveUsers` | `newapi.users.total` |
| 余额和消费 | `UserStats.BalanceMinorUnits`；`ModelUsage.ConsumedMinorUnits` | `newapi.users.total`、`newapi.models.usage` |
| 充值/订阅订单摘要 | `OrderSummary.RechargeMinorUnits` / `SubscriptionMinorUnits` / `OrderCount` | `newapi.recharge.daily`、`newapi.subscription.daily` |
| 模型和渠道状态 | `ChannelStatus.Enabled` / `Type` / `ModelCount`；`ModelUsage` | `newapi.channels.status`、`newapi.models.usage` |
| 错误率 | `ChannelStatus.ErrorRatePPM` | `newapi.channels.status`（逐渠道数组内） |
| 性能指标 | `ChannelStatus.LatencyMS` | `newapi.channels.status`（逐渠道数组内） |
| 数据水位 | `Snapshot.Watermark`（每个结果都带） | 全部指标的 `watermark` 字段 |

充值与订阅**分成两条指标**而不是合并成「收入」：两笔钱的业务含义不同
（一次性买额度 vs 周期订阅），合成一个数之后就再也拆不开了，而运营看板上
「这个月订阅涨了没有」恰恰是要分开看的问题。

## 数据形状

所有读取结果内嵌 `Snapshot{ObservedAt, Watermark, IsPartial}`——
**没有「只返回值不返回新鲜度」的结构**（规格 §9.1 禁止裸数字）。

`Snapshot` 是 `newapi` 包自己声明的，**不复用 `sub2api.Snapshot`**：
三个契约独立演进、各自钉住各自上游的版本与语义，共享结构体会让任意一边的
破坏性变更无声地传染到另一边。字段重复几行是刻意付出的代价。

| 类型 | 内容 |
|---|---|
| `UserStats` | 用户数、活跃数、余额、币种 |
| `OrderSummary` | 业务日、充值额、订阅额、订单数、币种 |
| `ChannelStatus` | 渠道 ID/名/类型、启停、余额（**可空**）、模型数、错误率（ppm）、延迟 |
| `ModelUsage` | 模型名、请求数、消耗额、币种 |

### 金额：整数最小货币单位 + 币种

一律 `int64` 最小货币单位 + `Currency`，禁止 float（规格 §5.9）。

### 渠道余额是 `*int64`，`nil` ≠ `0`

`ChannelStatus.BalanceMinorUnits` 是**指针**，这是本契约最容易被改坏的一处：

- `nil` = 「NewAPI 上这个渠道没有配置余额」——正常状态，这个渠道本来就不按
  余额计费；
- `0` = 「配了，而且已经花光了」——要立刻处理的事故。

用 `int64` 零值同时表达两者，看板上就会出现一排理直气壮的「¥0.00」，
运营分不出哪个该救。这条是 XM-0017（sub2api 真实客户端）上踩过的教训：
那边曾把「上游没给字段」和「上游给了 0」在解码时合流，前端只好靠猜。

落到指标 `value` 里的表现是：**余额未配置的渠道不写 `balance_minor_units`
这个键**（而不是写 0 或 null）。前端因此能用「键在不在」区分两者；写成 null
则要求 JSON 往返全程保住 null 语义，多一个环节就多一处会把它变回 0 的地方。

契约套件用反射断言该字段必须是 `*int64`，并验证 `nil` 渠道在聚合指标里确实
没有那个键——有人把它改回 `int64`「省掉一层指针」时会当场变红。

### 错误率是 ppm 整数，不是浮点

`ChannelStatus.ErrorRatePPM` 的单位是 **ppm（百万分之一）**，`int64`。

- **为什么不是 `float64`**：比率和金额是同一类东西——一旦落进浮点，0.1% 就
  不再等于 0.1%，两次采集算出来的同一个比率可能不相等，阈值比较会在边界上
  抖动，JSON 往返还会把 `0.001` 变成 `0.0009999999999999998`。
  规格 §5.9 对金额的纪律在这里同样适用。
- **为什么是 ppm 而不是万分之/百分之**：一个健康网关的错误率常在 0.01% 量级
  （= 100 ppm），用百分之整数表达时它和 0 无法区分。

换算 `1% = 10000 ppm`。显示成百分比时用**整数运算**（`ppm/10000` 取整数位，
`ppm%10000/100` 取两位小数），不要先转成 float 再除。

契约套件用反射遍历全部契约结构体，命中任何 `float32/float64` 就失败——
真正的失败模式不是「现在的字段是不是 int64」，而是**将来**有人图省事加一个
`ErrorRate float64` 或 `CostRate float64`，而那个改动本身编译得过。

### 渠道「异常」的判据

`ErrorRateUnhealthyPPM = 50000`（5%）。**停用的渠道一律不算异常**——它没在
服务，谈不上出错；把停用渠道计进异常数会让一个被人为关掉的渠道在看板上永久
亮红，然后所有人学会无视那个数字。

这个常量只用于聚合指标里的 `unhealthy_channel_count`，**不是告警阈值**：
告警阈值是运营策略，该由 `alerts` 包按环境配置，而不是由一个契约常量替所有
部署拍板。判据本身也一并写进指标 `value`（`unhealthy_threshold_ppm`），
看板显示「2 个异常」时，人要能当场问出「异常是按什么算的」并得到答案。

## 契约里没有什么：平台经营登记簿

产品负责人的 UI 原型里，NewAPI 平台页除渠道状态外还要显示四类东西：

> 联系人、充值成本率、接入平台标签、上游账号凭据到期日

**这四类都不在本契约里，也不会加进来。** 它们不是 NewAPI 的 API 能回答的
问题，而是平台自己对供应商的经营登记——属于**平台经营登记簿**
（supplier ledger），由 **XM-0037** 单独设计（等 UI 定稿）。

| UI 原型要的 | 为什么不属于 NewAPI 只读契约 |
|---|---|
| 联系人 | NewAPI 里没有「这个上游供应商的对接人是谁」这个概念，它只存在于我们自己的经营活动里 |
| 充值成本率 | 「我们花多少钱买到这些额度」是采购合同派生的经营数字，NewAPI 完全不知道我们付了多少钱 |
| 接入平台标签 | 我们给上游打的分类标签，上游不持有它 |
| 上游账号凭据到期日 | 凭据元数据。凭据只经 CredentialRef（ADR-014），本契约连凭据本身都读不到 |

⚠️ **「充值成本率」容易与规格 §9.2 的「充值倍率」混淆，两者是不同的对象。**
充值倍率是 NewAPI 渠道配置里的一个**字段**，读得到，将来可以进本契约；
充值成本率是我们的采购成本，读不到，也不该假装读得到。

底层依据是 **ADR-011 第 4 条：控制平台不拥有第三方业务数据的最终真相**。
把登记簿数据混进只读契约，等于让 `newapi.read.v1` 同时承担两种角色——一半是
上游的投影（上游改了我们跟着改），一半是我们自己的账本（我们说了算）。
两半的变更节奏、可信度与冻结条件完全不同，混在一起，第一次上游改字段时就会
分不清哪些该跟、哪些不该跟。

还有一个更实际的理由：登记簿是**可写**的（有人要维护联系人），而本包是只读
契约。让它们共处一个包，ADR-018 闸 4「Connector 包内无写路径」就只能靠评审时
有人记得，而不是靠接口形状本身。

## 四道只读闸（ADR-018）

| 闸 | 落地位置 | 状态 |
|---|---|---|
| 1 拒绝可写连接配置 | `connector.Config.Validate()` | ⏳ 待 XM-0038（尚无连接配置） |
| 2 强制 read-only 事务 | 只读 DSN 通道 | ⏳ 待 XM-0038 |
| 3 每个新连接复核服务端只读设置与权限 | 只读 DSN 通道 | ⏳ 待 XM-0038 |
| 4 Connector 包内无写路径 | `ReadClient` 接口形状 | ✅ XM-0035，有测试 |

**闸 4 现在就已经是机械强制的**：`ReadClient` 接口只有读方法，能力清单里
每一项都被契约测试断言 `IsWrite() == false`。规格 §8.4 的「后续写范围」
（Token 管理、渠道启停、模型配置、有限用户管理）在 Foundation-B 后**另立
接口**，绝不往 `ReadClient` 上加方法。

**闸 1/2/3 与 Sub2API 的情况不同，必须逐字生效。** `sub2api.read.v1.md` 把
闸 2 标为「不适用」、闸 3 标为「部分满足」，理由是它走的是官方 HTTP 只读 API，
没有数据库连接可谈。NewAPI 不能照抄这个结论——规格 §8.4 明确要求：

```text
Connector → CredentialRef → SecretProvider → 只读 DSN
```

既然是 **DSN（数据库连接）**，ADR-018 闸 2「强制 read-only 事务」与闸 3
「每个新连接复核服务端只读设置和权限」的原文就原样适用，没有 HTTP 通道那种
折衷余地。XM-0038 落地时必须实打实地：

1. 用专门发的**只读数据库账号**（不是复用管理账号）；
2. 每个连接开 `SET TRANSACTION READ ONLY`；
3. 每个新连接复核 `SHOW transaction_read_only` 与账号权限，不符就 fail closed；
4. 凭据只经 `CredentialRef`，**不得由 Connector 直接读取固定环境变量名**
   （§8.4 末句、ADR-014）。

因此本任务**刻意不放出** `XM_NEWAPI_ENDPOINT` / `_ALLOWLIST` /
`_CREDENTIAL_REF` 这几个变量：没有任何代码会读它们，先放出来只会让人以为
配齐了就能切真实数据。

## 错误映射（ADR-004：不透传供应商原始错误）

| ErrorKind | 触发场景 |
|---|---|
| `unavailable` | 网络不可达、超时、上游 5xx |
| `auth` | 凭据无效或权限不足 |
| `rate_limited` | 429 / Retry-After |
| `not_supported` | 上游版本不支持该能力；**以及 XM-0038 之前的 real 模式** |
| `bad_response` | 响应格式非法、字段缺失、业务日格式非法 |
| `forbidden_target` | 目标不在 allowlist |
| `write_attempt` | 只读通道上出现写请求 |

上游原始错误文本只进服务端日志（Unwrap 链），不进对外错误信息。

`not_supported` 承担双重含义值得说明：它表达的是「本部署还不具备这项读取
能力」，无论原因是上游版本太老还是我们自己的客户端还没写。与 `internal`
（我们的配置/装配写错了）区分开——运维一看 `error_code` 就知道该去补配置，
还是去等一个未交付的任务。

## 版本兼容

`Version()` 对不支持的上游版本返回 `Supported=false` 而非报错——
是否 Fail Closed 由调用方按场景决定：**读取可降级，写入必须停**（ADR-004）。

⚠️ **兼容矩阵尚未建立。** Fake 报告的版本是 `v1.0.0-rc.25`，取自
`docs/inventory/managed-systems.yaml` 里 `newapi-prod` 的 `detected_version`，
只为让 Fake 在版本形状上贴着真实实例（带 `v` 前缀、带 `-rc.N` 后缀）。
XM-0038 接真实客户端时第一件事就是跑一次 `Version()`，把实际探测值与解析规则
回填这里与 `managed-systems.yaml`。

## 指标输出

`ToObservations()` 把契约数据转成 `ops.Observation`，接进数据新鲜度模型：

```
newapi.users.total          用户数、活跃数、余额
newapi.recharge.daily       当日充值额、订单数
newapi.subscription.daily   当日订阅额
newapi.channels.status      渠道聚合：总数 / 启用数 / 异常数 + 逐渠道数组
newapi.models.usage         模型聚合：模型数 / 请求数合计 + 逐模型数组
```

这五个键在 `internal/platform/ops` 的白名单里有一份**字面量副本**（`ops` 不能
反向 import `connectors`，会成环）。两边由 `ops_test` 的
`TestRegisteredMetricsMatchConnectorContracts` 逐条对齐，任一边加减指标都会
当场失败（XM-0031）。

**渠道与模型都聚合成一条指标**，而不是每个渠道/模型一条：指标表的一行是
「一个可以设阈值、可以画趋势的量」，渠道数量会随运营增删而变，逐渠道建指标会
让白名单变成一张永远追不上的表，历史趋势也会在渠道下线那天断掉。逐条明细走
详情页（前端从指标 `value` 的数组里读）。

聚合指标的新鲜度由**最不新鲜的那个成员**决定。取最新会让一个刚更新的渠道替
十个陈旧的渠道背书，看板显示「数据新鲜」，而那一格里九成的数字已经过期了。

模型指标**不给消耗合计**：币种可能不一致，把不同币种的最小单位加在一起是纯粹
的错数。请求数没有这个问题，所以只合计请求数。

上游未提供观测时刻时，`ObservedAt` 保持为空（显示为「未初始化」），
**不用当前时间冒充**。

默认新鲜度阈值 **1800 秒**，与 Sub2API 取齐（不像开票放宽到 3600）：
NewAPI 是**在线网关**，请求量与错误率是分钟级变化的东西，半小时没有新数据
就已经说明采集链路有问题，而不是「今天没人提交」。

## 破坏性变更

本契约的能力清单、数据形状或错误映射变化必须发布 v2，并保留 v1 兼容期。

## 后续任务

| 任务 | 内容 |
|---|---|
| XM-0037 | 平台经营登记簿设计（联系人 / 充值成本率 / 接入平台标签 / 凭据到期日），等 UI 定稿 |
| XM-0038 | NewAPI 真实只读客户端：只读 DSN 通道、四道只读闸逐字落地、兼容矩阵回填 |
