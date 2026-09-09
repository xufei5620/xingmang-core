# 计量型渠道成本 / 收入取数 只读接入契约 v1

| 项 | 值 |
|---|---|
| 状态 | 冻结（XM-0037a 起生效） |
| Connector Key | `metering` |
| Contract Version | `1` |
| 实现 | 契约与 Fake：`connectors/metering`；真实驱动：`sub2api.go` / `newapi.go`（**未对真实实例验证**） |
| 合规判据 | **必须通过 `connectors/metering/contracttest` 套件**（Fake 与两个真实驱动均已通过，11 项） |
| 上游依据 | `docs/superpowers/plans/2026-08-28-xm-0037-cost-accounting-design.md` §3.1（成本）/ §3.2（收入）/ §2.4（金额）/ §4（口径常量） |

⚠️ **真实凭据尚未到位。** 三个实现都只由本地假上游的契约测试保证正确性；
账号到位后必须按下面的「账号到位后的验证清单」逐项核对——尤其是金额单位与
业务日边界，那两样错了之后数字看起来完全正常。

---

## 为什么不并进 `connectors/sub2api` 与 `connectors/newapi`

这是本契约存在的**首要理由**，不是包组织的口味问题。

那两个包读的是**自营实例的运营面板**（用户数、充值收入、渠道余额），用管理员
凭据打面板端点；本契约读的是**上游账号的实扣**与**自营账号的使用计费收入**，
用每令牌凭据打另一组端点。设计稿 §3.1 有一段显著警告：

> 平台现有 `connectors/sub2api` 的 `sub2api.cost.daily` 读的是 admin 面板
> `trend[].cost`（刻意避开 `actual_cost`），是自营实例口径，**不是本核算成本**。
> XM-0037 成本侧必须新走「每令牌 `/v1/usage` actual_cost」，**不得复用**。

两个口径同名不同义，差着一个倍率和一整套语义，是最容易被静静用错的一类东西。
放进**不同的包**、用**不同的指标键**（`finance.cost.daily` vs
`sub2api.cost.daily`），那条警告才从一句注释变成结构上做不到的事——
`connectors/metering/contract_test.go` 的
`TestMetricKeysDoNotCollideWithPanelCost` 与 `ops` 白名单的一致性用例
各钉一头。

---

## 能力清单

```
metering.service.version_read   上游版本探测
metering.health.read            运营健康
metering.token.usage_read       每令牌当日上游实扣（§3.1）
metering.account.revenue_read   每自营账号当日使用计费收入（§3.2）
```

实现可以返回**子集**，但不得返回清单之外的能力。每一项都必须
`registry.ParseCapability` 可解析且 `IsWrite() == false`。

`newapi` 驱动**不声明** `metering.account.revenue_read`：它的收入在自营
new-api 库的 `quota_data` 表里（§3.2），走只读**数据库**通道而不是 HTTP。
声明一项本客户端兑现不了的能力等于说谎；调用它会返回 `not_supported`，
而不是一个理直气壮的 0。

---

## 数据形状

### 取数与折算是**两步**

`TokenUsage` 是**未经 `recharge_ratio` 折算**的上游实扣；折算由 `CostOf`
单独完成，产出带 `RatioSnapshot` 的 `TokenCost`。分开有三个实打实的理由：

- 倍率改了不必重新打上游；
- 取数错了不会被折算掩盖（两步各有各的测试）；
- 台账要冻结 `ratio_snapshot`（§6.3），而「用哪个倍率折的」只有折算那一步知道。

### 金额：整数最小单位 @ scale-6 + 币种

所有金额是 `int64` 微单位（`UsageScale = 6`），**全程零 float**（宪法 13 条）。
标度取 6 的理由（设计稿 §2.4 / §12 拍板）：

- newapi 的 `quota / quota_per_unit`（口径常量 500000）在 scale-6 下**精确无舍入**：
  `micro = quota × 2`。scale-2 做不到，会在每条令牌成本上留一个舍入误差；
- 比展示所需的「分」低 4 位，分级展示零舍入；
- `int64 @ scale-6` 上限 ±9.2×10¹² 单位货币，成本台账不可能触顶。

折算走 `money.Divide`：`round_halfup(usage × 10^ratioScale / ratio_num)`，
中间乘积用 `math/big`（精确整数，不是浮点）——用 int64 直接算会在 usage 超过
约 $9200 时回绕成负数，而回绕不报错，只会让当天的成本变成一个荒谬的负数。

**worked example**（设计稿 §2.4，测试里逐条复核）：`actual_cost = "5.813729"`、
`ratio = 1.5` → `5813729×10/15 = 3875819.33…` 半进 `3875819` 微单位 = `$3.875819`
→ 分 `$3.88`；SoloAI 的 `float64(5.813729)/1.5 = 3.8758193…` → 分 `$3.88`。**一致**。

### 「先 SUM 再除」

newapi 的读数带 `RawUnits`（整数 credits）与 `UnitsPerWhole`（刻度）。做账号级
合计时必须把**整数 quota 相加、只在最后折一次**（`SumRawUnits`）：逐条折算后
相加会让每条各带半个微单位的舍入，几十条累加就够在分粒度上抖一次。

一批读数里出现两种 `UnitsPerWhole` 会**报错**——那意味着上游在采集中途改了
`quota_per_unit`（它是运行期可变的），两种刻度的计数不可相加。

### 「已知 0」与「未知」必须可区分

设计稿 §5.1：取数失败存 NULL / 跳过，**绝不写 0**；而**已知 0**（上游明确说
今天没有流量）要如实写 0。契约层的表达是：

- 成功返回 + 值为 0 = **已知 0**（sub2api 的 `today == null`，newapi 的 quota 缺失）；
- 返回 **error** = 未知，调用方据此让台账落 NULL。

两者是两种返回形态，而不是同一个 0。

### `IsPartial` 在这一层永远是 false

两个真实驱动的每次读取都是**单值原子读**（一个 `actual_cost` 或一个 `quota`），
要么拿到要么报错，没有「读到一半」这种状态。该字段真正的用武之地在**聚合**
那一层：`ToObservations` 把几十条令牌读数汇成一条观测时，一部分令牌失败就是
货真价实的部分数据。因此 `AssertPartialPropagates` 不在 `RunSuite` 里——
硬塞进去只会逼出一个永远为 false 的假标志位。

---

## 口径常量（错了不报错，只会让数字静静地偏）

| 常量 | 值 | 一致性要求 |
|---|---|---|
| 业务日时区 | **CST 固定 +08:00，无夏令时** | ★与 SoloAI `cstNow`（`relay_profit.go:60`）一致。用 `time.FixedZone` 而不是 `LoadLocation("Asia/Shanghai")`——后者依赖 tzdata 且会随其更新而变 |
| 收入 / 成本切日 | **共用同一时间权威** | ★一致。各切各的会「收入记 D、成本记 D+1」且**不报错** |
| 币种 | 计量台账全程 USD，每个金额带 Currency | ★一致；未登记的币种硬报错，不猜 |
| newapi 成本 type | `type=2`（仅 consume，不含充值） | ★一致。抄成别的值会把充值混进成本 |
| newapi 刻度 | `quota_per_unit`，**从 `/api/status` 读**，缺省回落 500000 | ★与 `NewAPIQuotaPerUSD`（`newapi.go:21`）一致。它是运行期可变的站点配置，写死的后果是上游一改刻度成本整体偏一个倍数且不报错 |
| sub2api 成本源 | `/v1/usage` 的 `usage.today.actual_cost` | ★一致（**非** `trend[].cost`，见上文警告） |
| sub2api 收入源 | `/admin/accounts/{id}/stats` 的 `summary.today.user_cost` | ★一致。使用计费，**不含充值 / 余额 / 赠送**（§10.5 铁律） |

上游报了非正 `quota_per_unit` 时**拒绝取数**而不是回落到 500000：那说明上游
状态异常，按默认刻度算出来的成本会是一个看起来正常的错数字（宪法 12 条）。

---

## 业务日：两个上游都**只回答今天**

- **sub2api**：`/v1/usage` 的响应段就叫 `today`，端点不接受任何日期参数。
- **newapi**：端点本身接受时间戳区间，但「过去某一天」的右边界该取当日
  23:59:59 还是次日 00:00:00（闭区间还是半开区间）**未对真实实例验证过**。

两者遇到答不出的业务日一律返回 `not_supported`，**绝不**把今天的数当成那一天
返回回去——那是一个看起来完全正常的错数字，比读不到糟得多。这与设计稿
§4/§5.3 的「今日覆盖、过去日冻结」一致：平台从不回填历史。

非法业务日（`2026-8-1`、`2026/08/28`）归 `bad_response`——那是**调用方**的错，
与「上游答不出这一天」是两回事。

---

## 凭据（ADR-014、宪法 7 条）

| 上游 | 用途 | 凭据来源 | 拼进哪里 |
|---|---|---|---|
| sub2api | 成本（§3.1） | `TokenRef.CredentialRef`，**每令牌一把** | `Authorization: Bearer <明文>` |
| sub2api | 收入（§3.2） | 连接级 `Config.CredentialRef`（admin key） | `X-Api-Key: <明文>` |
| newapi | 成本（§3.1） | 连接级 `Config.CredentialRef`，内容形如 `<user_id>:<session>` | `New-Api-User` + `Cookie: session=` |

- 明文只在拼头那**一瞬**存在，不进日志、不进错误、不进任何返回值。
- 成本侧的凭据**逐次解析、不缓存**：一轮采集会遍历几十把 key，缓存的收益是
  省几次 Provider 调用，代价是让明文在进程里活得更久；审计上也更清楚
  （「这把 key 什么时候被用过」逐次可查，规格 §4.5）。
- `<user_id>:<session>` 的编码属于**凭据的内部格式**，不进契约、不进日志；
  形状不对时报错**不含任何片段**。
- 登记簿侧的对应约束：`finance.token_map.credential_ref`（sub2api 计量型必填）。
  ⚠️ 该列是设计稿 §2.1 正文（「sk- 走 SecretProvider」+「每令牌打 /v1/usage」）
  的**强制推论**，列清单里没有写——理由记在 `db/migrations/000008` 的注释里。

---

## 四道只读闸（ADR-018）

| 闸 | 落点 |
|---|---|
| 1 拒绝可写连接配置 | `connector.Config.Validate()`，构造第一步就跑（https、CredentialRef 形态、非空 allowlist、endpoint 主机在自己的 allowlist 内） |
| 2 强制只读 | `connector.NewReadOnlyClientWithBase`：只放行 GET/HEAD，其余方法在 RoundTrip 里就被拒，请求根本不发出 |
| 3 复核服务端只读 | HTTP 侧能做的是目标主机**精确** allowlist（不做后缀匹配）+ 拒绝重定向（一个 302 就能把请求引到 allowlist 之外）；「上游发的是只读账号」平台强制不了，必须在发号那一步保证 |
| 4 包内无写路径 | `ReadClient` 接口只有读方法，两个驱动也只有 GET |

`client_contract_test.go` 逐条实测：`TestOnlyGetIsEverSent`、
`TestTargetOutsideAllowlistIsRejected`、`TestRedirectIsRefused`、
`TestCredentialsNeverLeakIntoErrors`。

⚠️ allowlist 写**不带端口的主机名**：`ReadOnlyTransport` 的集合按原样存，
查表时却用 `hostOnly()` 把 `host:port` 削成 `host`。写成 `api.example.test:443`
会永远匹配不上，症状是所有请求都被护栏拒绝——看起来像护栏坏了。

---

## 错误映射（ADR-004：不透传供应商原始错误）

| 上游情形 | 分类 | 理由 |
|---|---|---|
| 401 / 403 | `auth` | ⚠️ sub2api 对**未分组**的 apikey 返 403：那不是凭据错了，是那把 key 在上游还没被放进分组。两者的处置都是「去上游看这把 key 的配置」 |
| 423 | `auth` | sub2api 的合规确认门（0.1.183 起）。能力上游有，只是这个账号现在没被授权用；原样重试永远不会自己变好 |
| 429 | `rate_limited` | 成本采集是**每令牌一次请求**，令牌多了很容易撞限流（SoloAI 正因此把 sk- 加密缓存以规避 newapi 取 key 限流）。归此类让调用方能退避重试 |
| 404 / 405 / 501 | `not_supported` | 路由不存在 = 这个上游版本没有这项能力，不是「上游挂了」。重试没用 |
| ≥500 | `unavailable` | 过一会儿可能就好了 |
| 其余 4xx | `bad_response` | 我们发的请求上游看不懂；重试同样的请求不会有不同结果 |
| 传输层失败 | `unavailable` | 但**不覆盖**护栏已给出的 `forbidden_target` / `write_attempt` |

上游的响应体**一个字都不进错误链**：它可能带用户数据，极端情况下还可能把
请求头原样回显出来。排查需要的是「哪个操作、什么状态码」，那两样都在。

---

## 版本兼容

| 上游 | 矩阵 | 探测路由 |
|---|---|---|
| sub2api | `0.1` | `/api/v1/admin/system/version`（需 admin 认证） |
| newapi | `0.8` | `/api/status`（同时给 `quota_per_unit`） |

只写到 major.minor：补丁升级不该让采集链路判为不支持。不支持的版本返回
`Supported=false` 而**不报错**——是否 Fail Closed 由调用方按场景决定
（读取可降级，写入必须停）。`WithSupportedVersions` 给运维一条不改代码就能
应急的路。

---

## 指标输出

| 指标键 | 内容 |
|---|---|
| `finance.cost.daily` | 逐令牌 `usage_minor_units` / `cost_minor_units` / `ratio_snapshot` + 合计 |
| `finance.revenue.daily` | 逐自营账号 `revenue_minor_units` + 合计 |

- 聚合成一条而不是每个令牌一条：令牌数量随运营增删而变，逐令牌建指标会让
  指标白名单变成一张永远追不上的表，历史趋势也会在令牌下线那天断掉。
- 聚合观测时刻取**最旧**的成员：取最新会让一个刚更新的令牌替十个陈旧的令牌
  背书，看板显示「数据新鲜」而那一格里九成的数字其实已经过期了。
- 跨币种时**不给合计**（那是纯粹的错数），但写一个
  `total_omitted_reason: "mixed_currency"` 说清为什么不给——留一个没有解释的
  空位会让人以为是 bug（宪法 12 条）。
- `ratio_snapshot` 进逐令牌明细是必要的：看板上「这条渠道成本怎么涨了」的第一个
  追问就是「倍率是不是被改了」，答案必须能就地看到（§6.3）。
- 默认新鲜度阈值 1800 秒（采集按 §12 拍板默认 5 分钟一轮）。

⚠️ 这两个常量在 `internal/platform/ops` 的白名单里有一份**字面量副本**
（ops 不能反向 import connectors，会成环）。改动必须同步，
`ops_test` 的 `TestRegisteredMetricsMatchConnectorContracts` 会当场拦住遗漏。

---

## 账号到位后的验证清单

1. **先做合规确认**（sub2api）：0.1.183 起 admin 组挂了 `AdminComplianceGuard`，
   没确认过的账号所有 admin GET 都返 423。人工 `POST /api/v1/admin/compliance/accept`
   一次；平台是只读通道，发不出这个 POST，也不该发。
2. **确认成本令牌已分组**：未分组的 apikey 打 `/v1/usage` 返 403。
3. **核对金额单位**：拿一天的 `actual_cost` 与上游账单逐位对，确认是美元。
4. **核对业务日边界**：跨 CST 零点前后各取一次，确认落在同一个业务日里。
5. **核对 `quota_per_unit`**：读一次 `/api/status`，与上游设置页面比对。
6. **核对 `type=2` 的语义**：造一笔充值，确认它**不**出现在成本里。
7. **跑一次 `Version()`**，把探测值补进 `docs/inventory/managed-systems.yaml`。
8. **确认历史区间边界语义**后，再考虑放开 newapi 的「只回答今天」限制。

---

## 破坏性变更

上游改版时，要改的应该只有 `sub2api.go` / `newapi.go` 两个文件。
本契约的形状变更必须发新版本（`ContractVersion`），不得原地改。

## 后续任务

| 任务 | 范围 |
|---|---|
| XM-0037b | 利润台账 `profit_daily` + River 周期任务 `cost_sync`（消费本契约） |
| XM-0037b/未定 | newapi 收入的只读**数据库**通道（§3.2 的 `quota_data`），不在本 HTTP 契约内 |
| XM-0037e | 影子对比工具（仅计量型渠道） |
