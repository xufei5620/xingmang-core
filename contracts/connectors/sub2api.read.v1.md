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
| `forbidden_target` | 目标不在 allowlist |
| `write_attempt` | 只读通道上出现写请求 |

上游原始错误文本只进服务端日志（Unwrap 链），不进对外错误信息。

## 版本兼容

`Version()` 对不支持的上游版本返回 `Supported=false` 而非报错——
是否 Fail Closed 由调用方按场景决定：**读取可降级，写入必须停**（ADR-004）。

兼容矩阵：`sub2api.SupportedUpstreamVersions`，当前登记 `0.1` 一条线
（只写到 major.minor，补丁升级不判为不支持）。上游的版本串来自编译期嵌入的
`VERSION` 文件，形如 `0.1.133`，**没有 `v` 前缀**。

⚠️ 这条矩阵尚未对真实实例验证过——真实只读凭据到位后第一件事就是跑一次
`Version()`，把实际探测值回填这里、`SupportedUpstreamVersions` 与
`docs/inventory/managed-systems.yaml`。

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
