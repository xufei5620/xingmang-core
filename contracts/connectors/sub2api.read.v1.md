# Sub2API 只读接入契约 v1

| 项 | 值 |
|---|---|
| 状态 | 冻结（XM-0016 起生效） |
| Connector Key | `sub2api` |
| Contract Version | `1` |
| 实现 | 契约与 Fake：`connectors/sub2api`；真实只读客户端：`connectors/sub2api/client.go`（XM-0017） |
| 合规判据 | **必须通过 `connectors/sub2api/contracttest` 套件**（Fake 与真实实现均已通过） |

## 能力清单（规格 §8.3 Foundation-A 只读范围）

```
sub2api.service.version_read    服务版本
sub2api.users.read              注册用户
sub2api.users.balance_read      用户余额与透支
sub2api.orders.read             收入与订单摘要
sub2api.groups.read             分组
sub2api.accounts.read           上游账号状态
sub2api.models.usage_read       模型使用
sub2api.channels.balance_read   渠道余额
sub2api.health.read             运营健康
```

实现可以返回**子集**（旧版本上游少支持几项），但不得返回清单之外的能力。
每一项都必须 `registry.ParseCapability` 可解析且 `IsWrite() == false`。

## 数据形状

所有读取结果内嵌 `Snapshot{ObservedAt, Watermark, IsPartial}`——
**没有「只返回值不返回新鲜度」的结构**（规格 §9.1 禁止裸数字）。

金额一律 `int64` 最小货币单位 + `Currency`，禁止 float（规格 §5.9）。

| 类型 | 内容 |
|---|---|
| `UserStats` | 用户数、活跃数、余额、透支 |
| `OrderSummary` | 业务日、收入、成本、订单数 |
| `ChannelBalance` | 渠道 ID/名、余额、Token 是否有效 |

### `OrderSummary.RevenueMinorUnits` 的口径（XM-R013 明确）

**毛收入，单一币种。** 具体地：

- **毛**：上游 `payment/dashboard` 的 `daily_series` 按 `paid_at` 累加订单的
  `pay_amount`，**不扣退款**。上游有退款（`RefundResult`），但退款不回冲这条
  日序列。所以这个数字是"当天收进来多少"，不是"当天净赚多少"；
- **单一币种**：值只属于 `Currency` 这一个币种。上游 0.1.183 起把金额按
  **币种分桶**（`CurrencyAmounts = map[ISO4217]float64`），本客户端按
  `Currency` 取对应那一桶，**绝不跨币种相加**——上游源码里写着同一条纪律
  （"Amounts in different currencies must never be added together"）；
- 那一天**存在其他币种的收入**时（本契约装不下），或者 `Currency` 那一桶
  **压根不存在**时，`IsPartial = true`，收入按取到的部分报（取不到就是 0）。
  取不到时**不拿别的币种顶替**：顶替得到的是一个看起来完全正常的错数字。

⚠️ **`OrderCount` 是跨币种的**：上游按天累加订单条数时不分币种。
多币种的日子里它覆盖的范围比 `RevenueMinorUnits` 大，两者**不构成均价**。

⚠️ **收入币种与成本币种在上游是两个独立概念**：`pay_amount` 的币种是
**逐订单**的（上游默认 `CNY`），而用户余额、用量成本、账号额度全线按
**美元**记账。本契约只有一个 `Currency` 字段，由 `WithCurrency` 显式声明。
声明值与上游支付币种不一致时，收入会变成 `0 + IsPartial`——
这是**有意的失败形态**：宁可报不出来，也不给一个错的数。

## 四道只读闸（ADR-018）

| 闸 | 落地位置 | 状态 |
|---|---|---|
| 1 拒绝可写连接配置 | `connector.Config.Validate()` | ✅ XM-0016，有测试 |
| 2 强制 read-only 事务 | 数据库通道 | ➖ 不适用（见下） |
| 3 每个新连接复核服务端只读设置与权限 | 数据库通道 | ⚠️ **部分满足**（见下） |
| 4 Connector 包内无写路径 | `connector.ReadOnlyTransport` + `ReadClient` 接口形状 | ✅ XM-0016，有测试 |

闸 4 是**机械强制**而非评审约定：`ReadOnlyTransport` 拒绝任何非 GET/HEAD 请求
与任何不在 allowlist 内的主机，请求根本不发出；`ReadClient` 接口只有读方法。

**闸 2/3 原文针对数据库通道**（只读事务、`SHOW transaction_read_only` 复核）。
XM-0017 交付的是**官方 HTTP 只读 API** 通道——按 ADR-004 的读通道优先级，
「稳定官方只读 API」本来就排在「只读副本」之前，所以没有数据库连接可谈：

- 闸 2 在 HTTP 上由 `ReadOnlyTransport`（只放行 GET/HEAD）承担，与闸 4 同一份代码；
- 闸 3 在 HTTP 上只能做到主机精确 allowlist + 拒绝重定向。
  **上游没有只读角色**（只有 admin/user 两种，而所有需要的数据都在
  `/api/v1/admin/*` 下），所以拿到的凭据必然是全权限的。
  平台能保证「平台自己不发写请求」，保证不了「凭据在别处被误用」——
  缓解措施与验证清单见 `docs/modules/connector/RUNBOOK.md`。

将来若改走数据库只读副本，闸 2/3 的原文要求原样生效。

## 错误映射（ADR-004：不透传供应商原始错误）

| ErrorKind | 触发场景 |
|---|---|
| `unavailable` | 网络不可达、超时、上游 5xx |
| `auth` | 凭据无效或权限不足 |
| `rate_limited` | 429 / Retry-After |
| `not_supported` | 上游版本不支持该能力 |
| `bad_response` | 响应格式非法、字段缺失、超大 |
| `auth`（423 Locked） | 上游 0.1.183 起的**合规确认门**：该 admin 账号没做过合规确认，admin 组全部 GET 返 423。见下方「接入前置」 |
| `forbidden_target` | 目标不在 allowlist |
| `write_attempt` | 只读通道上出现写请求 |

上游原始错误文本只进服务端日志（Unwrap 链），不进对外错误信息。

## 版本兼容

`Version()` 对不支持的上游版本返回 `Supported=false` 而非报错——
是否 Fail Closed 由调用方按场景决定：**读取可降级，写入必须停**（ADR-004）。

兼容矩阵：`sub2api.SupportedUpstreamVersions`，当前登记 `0.1` 一条线
（只写到 major.minor，补丁升级不判为不支持）。上游的版本串来自编译期嵌入的
`VERSION` 文件（`backend/cmd/server/VERSION`），形如 `0.1.183`，
~~**没有 `v` 前缀**（待验证）~~ → **已确认无 `v` 前缀**：`GET
/api/v1/admin/system/version` 的 handler 只回 `{"version": <VERSION 原文>}`，
VERSION 文件里就是裸的 `0.1.183`。

### 逐补丁版核对记录

| 上游版本 | 核对方式 | 结论 |
|---|---|---|
| `0.1.133` | XM-0017 实现时的基线 | 7 条在用路由的字段名与形状全部对上 |
| `0.1.183` | XM-R013 对源码 `K:/sub2api-src @ efb46db` 逐字段核对（EV-2026-08-27） | 5 条路由不变；**2 处破坏性差异已修**（见下），客户端同时兼容两版形状 |

0.1.133 → 0.1.183 的两处破坏性差异：

1. **`/admin/payment/dashboard` 金额改成币种 map。** `DashboardStats` /
   `DailyStats` 的 `amount` 从标量 `float64` 变成
   `CurrencyAmounts = map[string]float64`。按标量解会拿到 `{"CNY":…}` 的
   **字面量文本**，金额解析直接失败 → `bad_response`。
   **这条在空实例上测不出来**：没有已支付订单的日子上游给的是空对象 `{}`，
   按标量解会被当成 0，一路正常——只有真的有收入的那天才炸；
2. **admin 组新增 `AdminComplianceGuard`**，未确认合规时全部 GET 返 423
   （见「接入前置」与错误映射表）。

⚠️ **仍未对真实实例跑过 `Version()`。** 上面是对上游**源码**的核对，
不是一次观测——`api.solov.cc` 实际跑的是哪个补丁版目前仍未知。
只读凭据到位后第一件事仍是跑一次 `Version()`，把探测值回填
`docs/inventory/managed-systems.yaml`（矩阵本身多半不用动，`0.1` 已覆盖）。

## 接入前置（拿到凭据后、开采之前必须做的两件事）

依据：`docs/evidence/EV-2026-08-27-sub2api-read-survey.md`。

### 1. 先做一次合规确认，否则 admin 组一条都读不出来

上游 0.1.183 起在 `/api/v1/admin`（以及 payment 的 admin 子组）挂了
`AdminComplianceGuard`。采集凭据对应的 admin 账号没确认过合规声明时，
**所有** admin GET 返 `423 Locked`，正文 `code=ADMIN_COMPLIANCE_ACK_REQUIRED`。

- 处置：由**人**对该账号执行一次 `POST /api/v1/admin/compliance/accept`。
  平台是只读通道，发不出这个 POST，也不该发（ADR-018 闸 4）；
- 现状可查：`GET /api/v1/admin/compliance`（这条路由在 guard 的白名单里）；
- 漏做的表现：看板上这批指标全部 `failed` + `last_error_code=auth`。

### 2. 这 5 个端点返回写死的假数据，列入永久黑名单

它们 HTTP 200、形状也正常，但 handler 里就是常量（上游源码注释：
"Return mock data for now"）。采了会得到一批**永远不动且看起来正常**的指标
——比读不到更糟：读不到会告警，假数据不会。

```
/api/v1/admin/users/:id/usage
/api/v1/admin/dashboard/realtime
/api/v1/admin/redeem-codes/stats
/api/v1/admin/groups/:id/stats
/api/v1/admin/proxies/:id/stats
```

本契约在用的 7 条路由都不在这份名单里。将来加路由前先回上游源码确认
handler 真的查了库。

## 指标输出

`ToObservations()` 把契约数据转成 `ops.Observation`，接进数据新鲜度模型：

```
sub2api.users.total       sub2api.users.balance
sub2api.revenue.daily     sub2api.cost.daily
sub2api.channels.balance
```

渠道余额聚合为一条指标，其新鲜度由**最不新鲜的那个渠道**决定。
上游未提供观测时刻时，`ObservedAt` 保持为空（显示为「未初始化」），
**不用当前时间冒充**。

## 破坏性变更

本契约的能力清单、数据形状或错误映射变化必须发布 v2，并保留 v1 兼容期。
