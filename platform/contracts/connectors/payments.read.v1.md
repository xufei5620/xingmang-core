# payments.read.v1（草案）——支付与财务：逐笔订单 + 按日按状态资金汇总

> **状态：草案（DRAFT）。字段核对依据两个上游的源码普查（见下），未对真实
> 实例验证过——账号到位后按本文档「真实实例验证清单」逐项核对，尤其是
> 状态归一化分桶与币种假设，那两样错了之后数字看起来完全正常。**

| 项 | 值 |
|---|---|
| 状态 | 草案（DRAFT），未冻结 |
| 覆盖平台 | `sub2api`、`newapi` |
| Connector 能力 | 复用既有 `sub2api.orders.read` / `newapi.orders.read`（不新增能力字符串，见「与既有契约的关系」） |
| 实现 | `connectors/sub2api/payments.go` + `upstream.go` 追加段；`connectors/newapi/payments.go` + `upstream.go` 追加段 |
| 合规判据 | `connectors/{sub2api,newapi}` 的 `*_test.go`（`payments_test.go`、`payments_internal_test.go`、`client_contract_test.go` 追加用例）；**不在** `contracttest.RunSuite` 里——那套套件钉的是 `ReadClient` v1 的方法集合，本片是叠加在 `ReadClientV2` 之上的独立切片，见下 |
| 上游依据 | `K:/sub2api-src`（`internal/handler/admin/payment_handler.go`、`internal/service/payment_*.go`、`internal/payment/types.go`）；`K:/newapi-src`（`controller/topup.go`、`model/topup.go`、`common/constants.go`），均只读核对，未修改上游源码 |
| 任务 | XM-PAY0（M3"支付 Connector"第一片：后端只读接入 + 查询端点；前端接入是下一片） |

## 与既有契约的关系

`contracts/connectors/sub2api.read.v1.md`（v1）/`sub2api.read.v2.md`（v2）与
`newapi.read.v1.md` 已经各自定义了 `DailyOrders`/`OrderSummary`——那是**按日
预聚合**的收入/充值摘要，来自上游自己的看板端点（sub2api 的
`/admin/payment/dashboard`、newapi 复用 `/api/user/topup` 但只认
`status=success` 且按到账时刻归日）。

本文档新增的 `ListOrders` + `DailyPaymentSummary` 是**逐笔明细 + 按状态汇总**，
口径与既有 `DailyOrders` **故意不同**（不是没对齐，是回答不同的问题）：

| | 既有 `DailyOrders`（v1/v2） | 本文档 `ListOrders`/`DailyPaymentSummary` |
|---|---|---|
| 粒度 | 一天一个数 | 逐笔订单 + 按状态分桶 |
| 覆盖状态 | 只有"已支付/已到账"那一类 | 全部状态（待处理/失败/退款都看得见） |
| 归日字段 | sub2api 按上游服务器时区；newapi 按 `complete_time`（到账时刻） | 统一按 `created_at`（下单时刻）+ 平台声明的业务日时区，两个平台一致 |
| 数据来源 | 上游的预聚合看板端点 | 上游的逐笔列表端点，本包自己翻页判定窗口 |

两条口径的数字**不保证互相对得上**（例如今天下单、明天才到账的订单：
`DailyOrders` 算进明天，本文档的 `succeeded` 桶算进今天）。这是已知且刻意的
差异，前端展示两组数字时不应暗示它们应该相等。

不新增能力字符串：`ListOrders`/`DailyPaymentSummary` 挂在既有
`sub2api.orders.read`/`newapi.orders.read` 之下——两者本就是"收入/订单"这同一
项能力的更细粒度视图。因此本片没有修改 `ReadCapabilities`，也没有给
`Capabilities()` 的探测逻辑加新路由探针（探针加不加不影响 `ListOrders`/
`DailyPaymentSummary` 能否被调用，只影响 `Capabilities()` 自身报告的准确度；
这个决定权留给以后需要它的场景，见「已知缺口」）。

接口独立成 `PaymentsReadClient`（叠加在 `ReadClientV2` 之上）而不是直接往
`ReadClientV2`/`ReadClient` 加方法：本文档是独立版本化的契约（版本从 `1`
起步，不与 `sub2api.read`/`newapi.read` 的 v1/v2 共用版本号），两边的破坏性
变更不该互相牵连。`NewClient`/`NewFake` 因此返回 `PaymentsReadClient`
（`ReadClientV2` 的超集，旧调用方不受影响）。

## Go 接口

```go
type PaymentsReadClient interface {
    ReadClientV2
    ListOrders(ctx context.Context, filter OrderFilter) (OrderPage, error)
    DailyPaymentSummary(ctx context.Context, day string) (DailyPaymentSummary, error)
}
```

`OrderFilter{From, To time.Time; Status string}`：`From`/`To` 是下单时刻的
UTC 闭区间，**两者必须非零**——上游都没有服务端日期过滤（sub2api 的订单列表
只支持 status/order_type/payment_type/keyword/user_id；newapi 的充值列表只
支持 keyword），无界查询没有一个调用方能预知的截断点，所以接口形状上直接
禁止。`Status` 非空时按上游原始状态过滤（sub2api 走服务端 `status=` 参数；
newapi 没有服务端过滤，客户端翻页后自己筛）。

`OrderPage{Snapshot; Items []Order; StatsByStatus map[string]OrderStats}`：
`Items` 是窗口内匹配的全量候选，**不做 cursor/limit 截断**——那是 HTTP 层
（`ListPlatformOrdersHandler`）基于已知总量做的展示分页，与
`ListPlatformChannelsHandler` 同一套路。`StatsByStatus` 的键是**上游原始
状态字面量**（不归一化），只对连接器配置币种求和。

`DailyPaymentSummary{Snapshot; Day, Currency string; ByStatus
map[string]StatusAmount; FeeMinorUnits, NetMinorUnits *int64}`：`ByStatus`
的键是**归一化分桶**（`succeeded`/`pending`/`failed`/`refunded` 四者之一），
上游这一天没有落进某个桶的订单，那个桶的键就**不出现**——不写
`{count:0, amount_minor_units:0}` 冒充"确认过是零"（规格 §12）。

## 逐笔订单字段（`Order`，两平台通用形状）

| 字段 | 类型 | 说明 |
|---|---|---|
| `OrderID` | string | 上游行的数据库主键，字符串化 |
| `CreatedAt` | time.Time | 下单时刻，UTC |
| `Status` | string | 上游**原始**状态字面量，不归一化（见下方枚举） |
| `AmountMinorUnits` | int64 | 整数最小货币单位，含义见各平台小节 |
| `Currency` | string | 逐行真实币种（sub2api 逐行可能不同；newapi 恒为连接器配置币种） |
| `Method` | string | 支付方式/提供方（命名刻意避开"channel"，见下） |
| `UserRef` | string | 展示用打码引用，**不是**跨系统 join 键（见下） |
| `UpstreamOrderRef` | string | 上游自己生成的业务订单号，供人工在上游后台核对 |

**命名为什么不叫 `Channel`**：本连接器包里的"渠道"
（`ChannelBalance`/`ManagedChannel`）指的是完全不同的另一个概念——上游
供应商账号（sub2api）/计费路由渠道（newapi）。`Method` 字段避免与这个已经
容易混淆的既有概念再撞一次名。

**`UserRef` 不是 `connectors/platformusers.UserRef`**：后者是跨平台查询用的
稳定不透明 ID，规格明确禁止用邮箱/打码邮箱充当（见
`docs/superpowers/plans/2026-08-28-platform-user-read-v2.md`
"禁止 username、email、masked email、token prefix join"）。这里的 `UserRef`
只是给人看的展示字段。

### Sub2API 小节

- 来源端点：`GET /api/v1/admin/payment/orders`（新登记进
  `connectors/sub2api/budget_routes.go`，能力仍是 `sub2api.orders.read`），
  挂在 `AdminComplianceGuard` 之下——423 Locked 归 `auth`，与既有路由同一分类
  （见 `upstream.go` 顶部"接入真实实例前置清单"）。原生支持 `page`/
  `page_size`/`status` 查询参数，排序固定 `created_at DESC`，**没有日期过滤
  参数**。
- `AmountMinorUnits` 取上游 `amount`（订单面值），**不是** `pay_amount`
  （= `amount + fee`，含手续费附加）。`refund_amount` 只在 `DailyPaymentSummary`
  的 `refunded` 桶里使用（见下），逐笔明细不单独展开退款金额。
- `Currency` 逐行取上游 `currency` 字段（`service.PaymentOrderCurrency`
  的计算结果：Stripe/Airwallex 按各自配置的币种，其余默认 **CNY**）。
  ⚠️ 这与 `sub2api.read` 契约里"上游全线用美元记账"的既有措辞**不是同一件
  事**——那句话说的是用户余额/用量成本/账号额度（这些确实是美元），支付
  订单的币种是逐行独立、可能非美元的字段，本片核对时予以更正说明。
- `Method` 取 `payment_type`（`alipay`/`wxpay`/`stripe`/`card`/`link`/
  `easypay`/`airwallex`）。
- `UserRef` 打码邮箱（`connectors/sub2api` 包内独立实现的 `maskEmail`，
  算法与 `connectors/platformusers.MaskEmail` 一致但不共享代码——两个连接器
  包之间不互相 import，理由同 `Snapshot` 不跨包共享）；邮箱为空/解析不出时
  退到 `"u<user_id>"`。
- `UpstreamOrderRef` 取 `out_trade_no`（上游自己生成的业务订单号，不是网关
  的 `payment_trade_no`）。

**状态枚举**（逐字抄自 `internal/payment/types.go` 的 `OrderStatus*` 常量）：

```
PENDING  PAID  RECHARGING  COMPLETED  EXPIRED  CANCELLED  FAILED
REFUND_REQUESTED  REFUNDING  REFUND_PENDING  PARTIALLY_REFUNDED
REFUNDED  REFUND_FAILED
```

### NewAPI 小节

- 来源端点：`GET /api/user/topup`（管理员组，既有路由，无需新登记）。原生
  只支持 `keyword`（trade_no 模糊匹配）与分页（`p`/`page_size`），**没有
  status 或日期过滤**——状态过滤在客户端做。
- `AmountMinorUnits` 复用既有的 `topupUSDQuota`（按 `payment_provider` 分桶
  的 `Amount`/`Money` 语义：epay/waffo/waffo_pancake 取 `Amount`，stripe 取
  `Money`，creem 的 `Amount` 本身就是 quota）+ `quotaToMinorUnits`，与
  `fetchRechargeDay` 同一套算法，区别是**不再限定 `status=success`**——任何
  状态的订单都按同一套规则折算。
  - `provider=balance`（内部余额划转）固定报 `0`：不是"这笔钱值 0"，是
    "不重新解释 `Amount`/`Money` 语义"，沿用 `topupUSDQuota` 对这类订单的
    既有处理（该函数本身就不计算这类订单的金额）。
  - `provider` 未识别（不在 epay/stripe/creem/waffo/waffo_pancake/balance
    六者之列）：同样报 `0`，并把整页/整日汇总标记为 `IsPartial=true`——
    不猜金额，见「不猜字段」的硬约束。
  - **聚合口径**：按状态/分桶聚合金额时先 `SUM` quota 再换算一次
    （`amount.go` 的调用纪律），不是逐行换算再相加——避免舍入误差按笔数
    累积。
- `Currency` 恒为连接器配置币种（NewAPI 内部只有 quota 一种单位，没有逐行
  真实币种这个概念）。
- `Method` 取 `payment_provider`（**不是** `payment_method`——两个字段在
  上游可能语义重叠但取值来源不同；`payment_provider` 是折算金额时实际
  依据的字段，用同一个字段做展示可以保证"为什么这样算"与"你看到的方式
  标签"永远一致）。
- `UserRef` 恒为 `"u<user_id>"`：上游充值列表不返回邮箱/用户名（`model.TopUp`
  只有数字 `user_id`），没有可打码的字段。

**状态枚举**（逐字抄自 `common/constants.go` 的 `TopUpStatus*` 常量）：

```
pending  success  failed  expired
```

## 按日按状态资金汇总（`DailyPaymentSummary`）与状态归一化

`DailyPaymentSummary` 把上游原始状态收拢进四个归一化分桶，供资金概览卡
"成功到账/待处理/失败/退款与冲正"直接消费。**这是本片唯一需要判断力（而非
纯粹照抄字段）的地方**，判据与理由如下（`paymentStatusBucket`，两平台各自
实现，逐条注释里带引用）：

### Sub2API 分桶

| 分桶 | 原始状态 | 依据 |
|---|---|---|
| `succeeded` | `PAID` `RECHARGING` `COMPLETED` | 三者在 `internal/service/payment_fulfillment.go` 的状态机里是同一条链（`PAID`→`RECHARGING`→`COMPLETED`），资金在进入 `PAID` 那一刻就已经被网关 captured；`RECHARGING` 只是"正在把已收到的钱记入用户余额"这一步内部过渡态。⚠️ 这与上游自己的 `publicOrderStatusPaid()`（面向终端用户的"支付完成了吗"布尔值）不同——那个函数**不认** `RECHARGING`，大概率因为这个过渡态通常以秒计、用户端来不及看到；本分桶回答的是财务问题"钱到账了没有"，不能照抄那个函数。 |
| `pending` | `PENDING` | 订单已创建，尚未收到网关确认。 |
| `failed` | `EXPIRED` `CANCELLED` `FAILED` | 三者都是"这笔钱最终没有进来"的终态；`CANCELLED` 归进这里而不单列，是因为从"钱有没有到账"这个问题看，主动取消和支付失败的答案相同（资金概览卡只有四格）。 |
| `refunded` | `REFUND_REQUESTED` `REFUNDING` `REFUND_PENDING` `REFUND_FAILED` `PARTIALLY_REFUNDED` `REFUNDED` | 整条退款生命周期（含尝试失败）都算进"退款与冲正"：订单一旦进入这条链，这笔钱就不再是干净的已确认收入；`REFUND_FAILED` 虽然钱还留在账上，但代表一次未完成的操作，需要人工介入，混进 `succeeded` 会把一个待处理问题藏起来。 |

`refunded` 桶的金额取 `refund_amount`（**实际退还金额**），不是原订单
`amount`——两者对 `PARTIALLY_REFUNDED` 从定义上就不同，退款卡要回答的是
"钱退出去多少"。其余三个桶取订单 `amount`（面值）。

### NewAPI 分桶

| 分桶 | 原始状态 |
|---|---|
| `succeeded` | `success` |
| `pending` | `pending` |
| `failed` | `failed` `expired` |
| `refunded` | **永不出现**——NewAPI 没有退款概念（`model/topup.go`、`controller/topup_*.go` 全文核对：不存在任何 refund 字段、状态值或函数）。这不是"今天没有退款"，是"这个上游不支持退款查询"，键因此不出现，不是出现且恒为 0。 |

## 手续费与净现金流

- **Sub2API `FeeMinorUnits`**：`sum(pay_amount - amount)`，只对 `succeeded`
  与 `refunded` 两个桶的订单求和（`pending`/`failed` 的钱没有真的动过，
  谈不上手续费）。这是两个真实存在的上游字段相减，不是猜的。
- **NewAPI `FeeMinorUnits`**：恒为 `nil`——`model.TopUp` 没有第二个金额字段
  可供相减，上游没有手续费概念。
- **两平台 `NetMinorUnits` 恒为 `nil`**：净现金流需要额外假设"手续费由谁
  承担"（是否已经从 `amount` 里扣除）与是否还有未建模的成本（汇兑价差、
  chargeback），本片核对不到、不猜（规格 §18.9 证据优先级 + 任务的
  "不猜字段"硬约束）。**若后续要点亮它**，需要先由核对过真实实例的人确认
  `pay_amount`/`amount` 的净额关系，再决定公式，见「已知缺口」。

## 跨币种处理（仅影响 Sub2API）

`DailyPaymentSummary`/`OrderPage.StatsByStatus` 的金额只对**连接器配置
币种**（`WithCurrency`，默认 `USD`）求和；笔数（`Count`）不区分币种，
跨币种也照计。命中非配置币种的订单时，整体标记 `IsPartial=true`
（`currencyGap`），与既有 `fetchPaymentDay` 的
`revenue`/`orderCount`/`currencyGap` 三态处理同一条纪律——不悄悄把不同
币种的金额加在一起（宪法 13 条 + 上游源码同款纪律）。逐笔明细
（`ListOrders.Items`）不受此限制，永远给出每一行的真实币种。

## Query 端点：`GET /api/v1/platforms/{platform}/orders`

- Scope：`finance.read`；环境取自 Principal（`resolveEnvironment`，不接受
  跨环境读取，也不像用户清单端点那样在构造期固定死——为将来一个进程服务
  多环境的形态留出空间）。
- 装配：`cmd/platform-api/platformpayments.go` 的 `dynamicPaymentsQuerier`，
  fake/real 生效模式来自 `core.connector_config`（经
  `jobs.NewPgConnectorConfigSource` 的 30s 缓存），复用
  `jobs.NewSub2APIClientFactory`/`NewNewAPIClientFactory` 的配置校验与
  错误分类——与 worker 同步走同一段代码。production 环境下生效模式仍是
  fake 时返回 `not_supported`（`jobs.ErrConnectorProductionFake`），
  与 XM-USERS-REAL 的 `dynamicUsersClient` 同一条闸。
- 未配置（`XM_PLATFORM_PAYMENTS_MODE=off`）时端点不挂载（404），不是挂载后
  报 500——与 `PlatformUsers` 同一条纪律。

### 请求参数

| 参数 | 说明 |
|---|---|
| `day` | 单日简写（`YYYY-MM-DD`），与 `from`/`to` 互斥 |
| `from`/`to` | 区间（`YYYY-MM-DD`，闭区间），都不传时默认当前 UTC 业务日 |
| `status` | 可选，按上游**原始**状态过滤 |
| `cursor` | 可选，上一页 `next_cursor` 原样回传 |
| `limit` | 可选，默认 50，范围 1~200（复用既有 `parseBindingLimit`） |

### 响应形状（示例）

```json
{
  "items": [
    {
      "order_id": "10234",
      "created_at": "2026-08-29T10:15:00Z",
      "status": "PAID",
      "amount": { "minor_units": "10000", "currency": "USD" },
      "method": "alipay",
      "user_ref": "zh***@example.com",
      "upstream_order_ref": "OUT-20260829-10234"
    }
  ],
  "next_cursor": "MTAw",
  "stats_by_status": {
    "PAID": { "count": 42, "amount": { "minor_units": "418000", "currency": "USD" } },
    "PENDING": { "count": 3, "amount": { "minor_units": "9900", "currency": "USD" } }
  },
  "from": "2026-08-29",
  "to": "2026-08-29",
  "data_source": "sub2api-staging",
  "freshness": {
    "state": "fresh",
    "staleness_seconds": 4,
    "threshold_seconds": 60,
    "is_partial": false,
    "observed_at": "2026-08-29T10:20:00Z",
    "last_success": "2026-08-29T10:20:00Z",
    "last_error_code": ""
  }
}
```

**设计决定，供前端下一片对齐**：

1. **金额是十进制字符串，不是 JSON number**（`amount: {minor_units, currency}`，
   复用既有 `amountBody`/`toAmountBody` 惯例，见 `internal/platform/httpapi/users.go`
   顶部注释）：JS 的 `number` 是 float64，订单金额一旦超过 2^53 会静默丢
   精度（宪法 13 条）。前端解析用 BigInt，全程整数。
2. **`stats_by_status` 的键是上游原始状态**，不是归一化分桶——归一化分桶
   （`succeeded`/`pending`/`failed`/`refunded`）走 `/api/v1/metrics` 的
   `sub2api.payments.daily`/`newapi.payments.daily`（本片同时写入，见下）。
   两个通道的口径刻意不同：这个端点给的是可核对的原始台账，指标端点给的
   是资金概览卡直接消费的聚合视图。
3. **`next_cursor` 为空串表示已翻到底**（与用户清单端点同一约定），非
   null——前端判空用字符串比较即可。
4. **游标是不透明的偏移量**（base64 编码的整数），不是订单 ID——订单 ID
   数字字符串化后按字典序比较不等价于按数值比较（"9" 在字典序里大于
   "10"），拿它当游标比较值会在位数跨界时悄悄漏页或重复，这个坑已经在
   契约测试里现过形。前端不应假设 cursor 的内部结构，只需原样回传。
5. **`data_source` 恒为 `"<platform>-fake"`（fake 模式）或实例 ID（real
   模式）**，与用户清单页的 `data_source` 同一条判读规则：前端据此挂
   "演示数据"横幅。

## `ops.metric_observation` 观测（worker 每轮同步写入）

`metric_key`：`sub2api.payments.daily` / `newapi.payments.daily`
（已登记进 `internal/platform/ops/freshness.go` 的白名单）。

```json
{
  "day": "2026-08-29",
  "currency": "USD",
  "by_status": {
    "succeeded": { "count": 42, "amount_minor_units": 418000 },
    "pending":   { "count": 3,  "amount_minor_units": 9900 },
    "failed":    { "count": 1,  "amount_minor_units": 3000 },
    "refunded":  { "count": 1,  "amount_minor_units": 20200 }
  },
  "fee_minor_units": 20000,
  "net_minor_units": null
}
```

上游没有的桶不出现该键（例如 NewAPI 恒不出现 `refunded`）；`fee_minor_units`/
`net_minor_units` 未知时为 JSON `null`，不是 `0`（规格 §12）。写入点：
`internal/platform/jobs/sub2api_sync.go`/`newapi_sync.go` 的 `Work()`，
在既有五（sub2api）/五（newapi）条观测之外追加一条，读取失败独立归类
（`sub2apiReadErrors.payments`/`newapiReadErrors.payments`），不与既有的
`orders`/`channels` 等分组混淆——一次支付读取超时不该把已经读到的用户数
一并抹成失败。

## 不猜字段：核对不清的地方

- **净现金流公式未定**（见「手续费与净现金流」）——这不是缺失实现，是
  刻意留白，等待业务侧确认 `amount` 是否已经净掉手续费。
- **Sub2API `Capabilities()` 不探测新路由**：`sub2api.orders.read` 的能力
  探测仍只验证 `/payment/dashboard` + `/dashboard/trend` 两条既有路由，
  不要求 `/payment/orders` 也可达——这条端点在旧版本上游是否存在未经核实，
  强行要求会让一个仍能正常跑 `DailyOrders` 的环境被误判为"不支持
  orders.read"。真实账号到位后如果发现 `/payment/orders` 在某个受支持
  版本上确实缺失，需要回来决定：是弱化探测、还是把 `ListOrders`
  单独降级为 `not_supported`。
- **Rollup 降采样策略暂不支持这两个新指标**：`by_status` 是多桶结构
  （每个状态各一组 count+amount），v1 版本的
  `contracts/ops/metric-rollup-policy.v1.json` 每个指标只支持一个
  `primary_json_pointer`，装不下。已加入 `excludedRollupMetrics`
  （gate=`ROLLUP-MULTI-BUCKET`）——原始观测仍正常写入 `/metrics` 与
  `/metrics/history`，只是暂时不参与历史降采样汇总。

## 真实实例验证清单（账号到位后）

1. 确认采集凭据对应的 sub2api 管理员账号已过 `AdminComplianceGuard`
   合规确认（`POST /api/v1/admin/compliance/accept`，人工做一次）。
2. Sub2API：核对至少一笔真实订单的 `currency` 字段，确认"默认 CNY，
   Stripe/Airwallex 走各自配置币种"这条假设成立；核对 `RECHARGING` 状态
   在真实数据里确实短暂出现（验证归入 `succeeded` 不会让"钱没到账"的订单
   被误判为已到账）。
3. Sub2API：核对 `pay_amount - amount` 在真实订单上恒为非负（`fee` 计算
   的前提）。
4. NewAPI：核对是否存在 `payment_provider` 不在
   `epay/stripe/creem/waffo/waffo_pancake/balance` 六者之列的真实订单
   （若有，`IsPartial` 应正确触发，且需要评估要不要扩展 `topupUSDQuota`
   认得这个新 provider）。
5. 两平台：各查一天有充分订单量的真实业务日，核对
   `ListOrders`/`DailyPaymentSummary` 的笔数与上游后台自己显示的当日
   订单数一致（翻页早停逻辑是否正确覆盖了整个窗口）。
6. 核对 `docs/inventory/managed-systems.yaml` 里两个上游的
   `detected_version` 是否仍在 `SupportedUpstreamVersions`/
   `SupportedUpstreamVersions`（newapi 无此常量校验，仅 sub2api 有）覆盖
   范围内。
