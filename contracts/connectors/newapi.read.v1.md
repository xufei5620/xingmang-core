# NewAPI 只读接入契约 v1

| 项 | 值 |
|---|---|
| 状态 | 冻结（XM-0035 起生效） |
| Connector Key | `newapi` |
| Contract Version | `1` |
| 实现 | 契约与 Fake：`connectors/newapi`；真实只读客户端：`connectors/newapi/{client,upstream,amount}.go`（XM-0038，**HTTP 通道**） |
| 合规判据 | **必须通过 `connectors/newapi/contracttest` 套件**（Fake 与真实客户端各 12 项，同一套） |
| 上游依据 | 规格 §8.4 NewAPI Connector · Foundation-A 只读范围；端点与字段依据 `docs/evidence/EV-2026-08-27-newapi-read-survey.md` |

✅ **真实数据链路已接通（XM-0038）。** `XM_NEWAPI_MODE=real` 配齐三个变量
（endpoint / allowlist / credential ref）即可读真实实例，切换步骤见
`docs/runbooks/SWITCH-NEWAPI-REAL.md`。配置不全时每周期写一条 `not_supported`
的失败观测——「这条链路还没配好」是一个事实，看板该看得见它，而不是让 NewAPI
那几格数据静静地不更新（规格 §9.1）。

⚠️ **真实客户端走的是 HTTP，不是只读 DSN。** 这与本文件早先的预期不同，
理由与影响见下面「四道只读闸」一节。三个指标口径因此**天生不完整**
（订阅金额、订单数、部分渠道的错误率），逐条列在「真实客户端的已知缺口」。

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
| 1 拒绝可写连接配置 | `connector.Config.Validate()`（构造第一步） | ✅ XM-0038，有测试 |
| 2 强制只读 | `connector.NewReadOnlyClient`：只放行 GET/HEAD | ✅ XM-0038，有测试 |
| 3 复核服务端只读设置与权限 | ⚠️ HTTP 通道上不存在可查的「服务端只读设置」；替代物是主机 allowlist + 拒绝重定向 | ⚠️ 部分满足 |
| 4 Connector 包内无写路径 | `ReadClient` 接口形状 | ✅ XM-0035，有测试 |
| **5 伪 GET 写端点黑名单** | `assertReadOnlyRoute`（NewAPI 特有） | ✅ XM-0038，有测试 |

**第五道闸是 NewAPI 特有的，也是这次交付最要紧的一处。** 上游有一批
**用 GET 方法但会写数据库**的端点，前四道闸对它们**全部放行**——闸 2 看的是
HTTP 方法（就是 GET），闸 3 看的是目标主机（就是同一个上游）。清单与各自的
写库点见 `connectors/newapi/upstream.go` 的 `writeDisguisedAsGetRoutes`，
其中最危险的一条是 `GET /api/user/token`：它会**静默轮换调用者自己的
access token**，调一次就把采集凭据打掉。

判据机械执行、有两层测试守着：`assertReadOnlyRoute` 逐条比对（按路径段匹配，
不是字符串前缀，否则 `/api/user/tokens-report` 会被误伤），
外加一条端到端断言——跑完一整轮同步后，假上游收到的每条请求都必须是 GET
且不在黑名单里。

**闸 3 为什么退回「部分满足」。** 本文件早先要求 XM-0038 走
`Connector → CredentialRef → SecretProvider → 只读 DSN`，那样闸 2/3 的原文
可以逐字生效。**实际交付的是 HTTP 通道**，原因有二：

1. 规格 §8.4 要读的指标（渠道状态、错误率、用量、充值）在 HTTP 上全部读得到，
   而建库连接需要用户在 NewAPI 的数据库上单独开只读账号——那是一次成本高得多、
   风险面也大得多的授权；
2. 真正**只有** DSN 才读得到的那部分（订阅在册与订阅收入）属于成本/收入线
   （XM-0037 设计稿 §3.2），不在本任务范围内。

代价必须说清楚：**上游没有只读角色**，本包读的端点全挂 `AdminAuth()`，
所以采集凭据在上游是全权限的管理员 token。平台侧强制的是「不发写请求」
（闸 2 + 闸 5），做不到「上游拒绝写请求」——后者只有发一个真正的只读账号
才做得到，而 NewAPI 目前发不出来。这一条写进了操作卡的风险说明。

**闸 4 是接口形状本身**：`ReadClient` 只有读方法，能力清单里每一项都被契约
测试断言 `IsWrite() == false`。规格 §8.4 的「后续写范围」（Token 管理、
渠道启停、模型配置、有限用户管理）在 Foundation-B 后**另立接口**，
绝不往 `ReadClient` 上加方法。

凭据只经 `CredentialRef`，**Connector 不读任何固定环境变量名**
（§8.4 末句、ADR-014）：`XM_NEWAPI_TOKEN` 只是 `cmd/platform-worker` 里
env Provider 的登记落点，Connector 拿到的自始至终只有 `secret://...` 引用。

## 真实客户端的已知缺口

这些不是故障，是上游 HTTP 面的能力边界。**每一条都在数据里被显式标注**，
没有一条被静默补成 0。

| 缺口 | 表现 | 依据 |
|---|---|---|
| 订阅金额读不到 | `SubscriptionMinorUnits` 恒为 0，且 `OrderSummary` **恒标记 `IsPartial`**，水位写 `subscription:unavailable_over_http` | 上游把订阅订单存在 `subscription_orders`，**没有任何 HTTP 端点列它**；能读到的只有「某人现在有哪些订阅」，换不出「这一天收了多少订阅费」 |
| 订单数偏小 | `OrderCount` 只含充值订单 | 同上——订阅订单列不出来，与其编一个凑数的加数不如少报并写进水位 |
| 渠道错误率有上限 | 最多为 40 个渠道计算（启用的优先）；其余渠道 `ErrorRatePPM` 留 0 且该渠道 `IsPartial=true` | 上游无错误率端点，只能逐渠道两次日志 COUNT 自己算（`type=5/(type=2+type=5)`，最近 24 小时），渠道多时会吃光一轮的读取预算 |
| 渠道余额可能大面积缺失 | `balance_updated_time == 0` → `BalanceMinorUnits = nil`（不写这个键） | `channel.balance` 是**手动刷新**的额度，刷新端点是写端点（在黑名单里）；订阅型上游账号永远静默为空 |
| 渠道余额可能很旧 | 最旧的刷新时刻写进渠道指标水位 `balance_oldest:<unix>` | 上游默认不自动刷新余额；契约 v1 的 `ChannelStatus` 没有「余额观测时刻」字段，v2 应当补 |
| 活跃用户是自定口径 | 最近 30 天内登录过；判据写进水位 `active:last_login_30d` | **NewAPI 上游没有「活跃用户」这个概念**，口径必须跟着数字一起被看见 |
| 逐模型用量最多滞后 5 分钟 | `ObservedAt` 取响应时刻，最新小时桶写进水位 `bucket:<unix>` | 上游的 `quota_data` 表由后台每 `DataExportInterval`（默认 5 分钟）落盘 |
| 用户数不含软删除 | 逐行看 `DeletedAt` 自己数，**不用** `data.total` | 上游的用户列表查询是 `Unscoped()` 的，软删除用户混在结果里、也算进 `total` |
| 认不出的支付渠道会丢金额 | 计进水位 `unclassified:N` | `amount`/`money` 哪个是美元**取决于 `payment_provider`**（三种语义）；认不出来就不猜——猜错的是「看起来完全正常」的错数字 |

### 金额口径：quota 与 quota_per_unit

NewAPI 内部的钱只有 **quota**（整数）一种单位，换算成美元的唯一口径是
`美元 = quota / quota_per_unit`。`quota_per_unit` **运行期可变**（root 改选项
即刻生效），所以客户端**每轮现读一次** `/api/status`，不编译期写死。

两条纪律：

- **先 SUM 再除，只除一次。** 每条明细各自换算再相加，误差会按条数累积
  （几千条各进位一次能差出几十块）。契约测试用「2500+2500+10000000 quota
  → 2001 分而不是 2002 分」把这条钉死在用户余额与模型用量两个调用点上。
- **`quota_per_unit <= 0` 报错，不拿默认值顶上。** 上游写选项时把
  `ParseFloat` 的 error 丢掉了，非法值会把它置 0；拿 0 做除数是崩溃，
  拿 500000 顶上是**编数**——那会产出一批看起来完全正常的错数字。

## 错误映射（ADR-004：不透传供应商原始错误）

| ErrorKind | 触发场景 |
|---|---|
| `unavailable` | 网络不可达、超时、上游 5xx |
| `auth` | 凭据无效或权限不足 |
| `rate_limited` | 429 / Retry-After |
| `not_supported` | 上游版本不支持该能力（404/405/501）；**以及 real 模式三个连接变量没配齐** |
| `bad_response` | 响应格式非法、字段缺失、业务日格式非法；**以及 HTTP 200 + `success:false`** |
| `forbidden_target` | 目标不在 allowlist；**以及上游发了重定向** |
| `write_attempt` | 只读通道上出现写请求；**以及命中伪 GET 写端点黑名单（闸 5）** |

上游原始错误文本只进服务端日志（Unwrap 链），不进对外错误信息。

`not_supported` 承担双重含义值得说明：它表达的是「本部署还不具备这项读取
能力」，无论原因是上游版本太老还是变量还没配齐。与 `internal`
（我们的连接配置写错了，比如 endpoint 不是 https）区分开——运维一看
`error_code` 就知道该去**补**配置还是去**改**配置。

两个 NewAPI 特有的坑值得单列：

- **业务失败是 HTTP 200 + `success:false`**（上游的 `common.ApiError` 走 200），
  只有鉴权失败才是真 401/403。只看状态码的话，一次「查询失败」会被当成一次
  成功读取，然后把空数据写进看板——数字变成 0，徽章还显示「数据新鲜」。
- **少写一个尾斜杠会表现为 `forbidden_target`。** `/api/channel/`、`/api/user/`、
  `/api/log/`、`/api/data/` 在 gin 里注册的是 `"/"`，少一个斜杠得到 301，
  而本客户端拒绝一切重定向。症状看起来像 allowlist 配错了，实际是路径少一个
  字符——`routeMustEndWithSlash` 与一条测试把这几条钉住了。

## 版本兼容

`Version()` 对不支持的上游版本返回 `Supported=false` 而非报错——
是否 Fail Closed 由调用方按场景决定：**读取可降级，写入必须停**（ADR-004）。

兼容矩阵是 `SupportedUpstreamVersions = ["1.0"]`，依据是
`docs/inventory/managed-systems.yaml` 里 `newapi-prod` 的 `detected_version`
`v1.0.0-rc.25`（2026-08-26 观测）——`normalizeVersion` 把它收敛成 `1.0.0`，
命中 `1.0` 这条 minor 线。

⚠️ **仍未对真实实例跑过 `Version()`。** 字段形状核对的是上游源码，不是那个
实例；而上游的版本串形态**在它自己那里就没有定论**：`VERSION` 文件是空的、
默认值是 `v0.0.0`，还能被 `VERSION` 环境变量、CI 的 `git describe`、分支镜像的
`<前缀>-日期-sha` 任意覆盖。凭据到位后第一件事是跑一次 `Version()`，把探测值
补进 `managed-systems.yaml`。

探测值不在矩阵内时用 `WithSupportedVersions` 显式声明（不改代码的应急路径），
**而不是把矩阵放宽成「什么都认」**——那等于取消版本探测。

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
| ~~XM-0038~~ | ✅ NewAPI 真实只读客户端（HTTP 通道）。DSN 通道未做，见下 |
| 契约 v2 | `ChannelStatus` 补「余额观测时刻」字段（现在挤在水位里）；`ErrorRatePPM` 改成可空或补一个「测没测过」标志（现在靠 `IsPartial` 间接表达） |
| 成本/收入线 | 只读 DSN 通道：订阅在册与订阅收入（普查报告的 P4），本契约的 `SubscriptionMinorUnits` 要靠它才填得上 |
| 凭据到位后 | 跑一次 `Version()` 回填 `managed-systems.yaml`；按操作卡验证金额口径与业务日边界 |
