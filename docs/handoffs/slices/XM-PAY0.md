# XM-PAY0 · 支付与财务：Sub2API / NewAPI 逐笔订单只读接入 + 按日按状态资金汇总

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-PAY0`
- implementation commit: `56ac9b3`（本分支 HEAD，单提交）
- base: `release/v0.1-launch`
- worktree: `K:/星芒统一控制平台/acceptance/wt-pay0`

## summary

M3"支付 Connector"第一片：不接第三方支付网关，直接只读两个平台自己的
订单/充值记录。后端数据接通 + 查询端点这一片完成；前端接入（把
`Sub2ApiFinanceOverview.tsx`/`NewApiFinanceOverview.tsx` 接到真实数据）
是下一片，`web/` 目录本片未改动。

### 契约（`contracts/connectors/payments.read.v1.md`，DRAFT）

逐字段核对 `K:/sub2api-src`（`internal/handler/admin/payment_handler.go`、
`internal/service/payment_*.go`、`internal/payment/types.go`）与
`K:/newapi-src`（`controller/topup.go`、`model/topup.go`、
`common/constants.go`）源码得出，只读参考、未做任何改动。核心决定：

- **新增 `ListOrders`/`DailyPaymentSummary`，不改既有 `DailyOrders`**：
  既有的按日预聚合口径（sub2api 走 `/admin/payment/dashboard`，newapi
  按 `complete_time` 归日且只认 `status=success`）继续原样服务；本片是
  独立的逐笔明细 + 按状态汇总口径，两者归日字段刻意不同（本片统一按
  `created_at`/`create_time`，即"下单时刻"），数字不保证互相对得上，
  契约文档里有专门一节说明差异原因。
- **状态归一化分桶**（`succeeded`/`pending`/`failed`/`refunded`，供资金
  概览卡消费）是本片唯一需要判断力的地方：
  - Sub2API `succeeded` 含 `PAID`/`RECHARGING`/`COMPLETED`——三者在
    `internal/service/payment_fulfillment.go` 的状态机里是同一条链，资金
    在进入 `PAID` 那一刻就已被网关 captured；这与上游自己的
    `publicOrderStatusPaid()`（终端用户"支付完成了吗"布尔值，不认
    `RECHARGING`）不同，本片回答的是财务问题，不能照抄那个函数（判据与
    引用见 `connectors/sub2api/payments.go` 的 `paymentStatusBucket`）。
  - `refunded` 含整条退款生命周期（`REFUND_REQUESTED`/`REFUNDING`/
    `REFUND_PENDING`/`REFUND_FAILED`/`PARTIALLY_REFUNDED`/`REFUNDED`），
    退款桶金额取 `refund_amount`（实际退还额），不是原订单 `amount`。
  - NewAPI 没有退款概念（`model/topup.go`、`controller/topup_*.go` 全文
    核对：无 refund 字段/状态/函数），`refunded` 键因此**永不出现**，
    不是"今天没有退款"。
- **手续费/净现金流**：Sub2API 的 `FeeMinorUnits` 可从 `pay_amount-amount`
  两个真实字段相减得出（只对已 captured 到资金的订单求和），有值；
  `NetMinorUnits` 两平台恒为 `nil`——需要额外假设手续费的承担方与是否
  还有未建模成本，本片核对不到，宁可留白也不猜（任务硬约束）。NewAPI
  两者都无上游字段支撑，恒为 `nil`。
- **币种**：Sub2API 逐笔订单的 `currency` 是**逐行真实字段**（Stripe/
  Airwallex 走各自配置币种，其余默认 CNY——`service.PaymentOrderCurrency`
  的计算结果），核对时发现这与既有 `sub2api.read` 契约"上游全线用美元
  记账"的措辞不是同一件事（那句话说的是用户余额/用量成本，不是支付订单
  币种），已在新契约里做更正说明，未改动旧契约文本。`ListOrders.Items`
  永远给每行真实币种；`StatsByStatus`/`DailyPaymentSummary` 只对连接器
  配置币种（`WithCurrency`，默认 USD）求和，命中其他币种时笔数仍计入、
  金额不计入，整体标记 `IsPartial`——不做跨币种相加（宪法 13 条）。
- **命名**：逐笔字段用 `Method` 而不是 `Channel`：两个连接器包里的
  "渠道"（`ChannelBalance`/`ManagedChannel`/`ChannelStatus`）已经指
  完全不同的概念（上游供应商账号/计费路由），字段名不重复这个坑。
  `UpstreamOrderRef` 取上游自己生成的业务订单号（`out_trade_no`/
  `trade_no`），不是网关自己的 trade no。

### 连接器（`connectors/{sub2api,newapi}/payments.go` + `upstream.go` 追加段）

- 新接口 `PaymentsReadClient`（叠加在既有 `ReadClientV2` 之上的独立
  版本化切片，不直接扩 `ReadClientV2`/`ReadClient`），`NewClient`/
  `NewFake` 返回类型升级为它（结构上是 `ReadClientV2` 的超集，旧调用方
  不受影响，`go build ./...` 已验证）。
- 两个上游的逐笔订单列表端点都**没有服务端日期过滤**（sub2api 的
  `/admin/payment/orders` 只有 status/order_type/payment_type/keyword/
  user_id；newapi 的 `/api/user/topup` 只有 keyword）。`ListOrders`/
  `DailyPaymentSummary` 因此自己翻页判断窗口：按上游排序键（sub2api 是
  `created_at DESC`，newapi 是 `id DESC`）从新到旧扫，一旦订单早于窗口
  起点即可停手；页数上限 `maxPaymentOrderPages=20`（sub2api 200/页、
  newapi 100/页上游硬顶），到顶未扫完窗口则标记 `IsPartial=true`（水位
  追加 `/truncated`，本片新增，见下）。**已知代价**：查询一个较早的
  历史日期，需要先翻过该日期之后所有更新的订单才能进入目标窗口，繁忙
  实例上查旧数据可能提前撞到页数上限——这与既有 `newapi.fetchRechargeDay`
  同一类限制，不是本片引入的新问题，但值得在 RUNBOOK 或前端交互上提醒
  （例如查询范围越靠近今天越可靠）。
- NewAPI 复用既有 `topupUSDQuota`/`topupItem`/`topupKind`（provider 分桶
  的 Amount/Money 语义），扩展到不再限定 `status=success`——任何状态都
  按同一套规则折算；`provider=balance`（内部划转）固定报 0（不是"值 0"，
  是"不重新解释语义"）；`provider` 未识别同样报 0 并标记部分数据。
- Sub2API 新增独立打码实现 `connectors/sub2api/redact.go`
  （`maskEmail`），口径与 `connectors/platformusers.MaskEmail` 一致但不
  跨包 import（两个连接器包之间的既定纪律，见 `Snapshot` 不共享的同一
  条理由）。
- **本片新增的改进**：把水位（Watermark）从只含时间戳，改成同时编码
  "为什么可能不完整"（`/truncated`、`/currency_gap`、`/unknown_status`
  等后缀），与既有 `rechargeWatermark`/`paymentDay` 的水位纪律对齐——
  `IsPartial=true` 时运维不必翻代码/日志就知道是翻页到顶、遇到非合约
  币种、还是遇到未登记的新状态。Sub2API 侧顺带把 `fetchDailyPaymentSummary`
  里原先合用一个 `currencyGap` 标志表达"币种缺口"与"未知状态"两件事的
  写法拆成两个独立布尔，避免水位诊断信息失真。

### 观测（worker 每轮同步）

- `internal/platform/jobs/{sub2api,newapi}_sync.go` 的 `Work()`/`read()`
  各追加一次 `DailyPaymentSummary(today)` 读取，独立错误分组
  （`sub2apiReadErrors.payments`/`newapiReadErrors.payments`），一次支付
  读取超时不会污染已经读到的用户数/渠道状态。写入指标键
  `sub2api.payments.daily`/`newapi.payments.daily`（已登记进
  `internal/platform/ops/freshness.go` 白名单）。
- `contracts/ops/metric-rollup-policy.v1.json` + `internal/platform/ops/
  rollup_policy.go` 把这两个新指标登记为已知缺口（`gate:
  "ROLLUP-MULTI-BUCKET"`）：`by_status` 是多桶结构（每个归一化状态各一组
  count+amount），v1 rollup policy 每个指标只支持一个
  `primary_json_pointer`，装不下——原始观测仍正常经 `/metrics`、
  `/metrics/history` 可查，只是暂不参与历史降采样汇总。这是本片核对
  代码时主动发现并处理的下游兼容性问题，不在原任务描述里。

### 查询端点

`GET /api/v1/platforms/{platform}/orders?day=&from=&to=&status=&cursor=&limit=`

- scope `finance.read`；环境取自 Principal（`resolveEnvironment`，不接受
  跨环境读取）；`day` 与 `from`/`to` 互斥，都不传默认当前 UTC 业务日。
- 装配 `cmd/platform-api/platformpayments.go` 的 `dynamicPaymentsQuerier`：
  fake/real 生效模式来自 `core.connector_config`（经
  `jobs.NewPgConnectorConfigSource` 30s 缓存），复用
  `jobs.NewSub2APIClientFactory`/`NewNewAPIClientFactory` 的配置校验与
  错误分类——与 worker 同步走同一段代码，不另立一套判定。production
  环境下生效模式仍是 fake 时返回 `not_supported`
  （`jobs.ErrConnectorProductionFake`），与 XM-USERS-REAL 的
  `dynamicUsersClient` 同一条闸；独立开关
  `XM_PLATFORM_PAYMENTS_MODE`（默认 fake），不复用
  `XM_PLATFORM_USERS_MODE`；未配置（`=off`）时端点不挂载（404），不是
  挂载后一调 500。
- 响应形状（供前端下一片对齐，契约文档里有完整示例 JSON）：
  `{items:[{order_id, created_at, status, amount:{minor_units, currency},
  method, user_ref, upstream_order_ref}], next_cursor,
  stats_by_status:{<原始状态>:{count, amount:{minor_units, currency}}},
  from, to, data_source, freshness}`。要点：
  1. 金额是**十进制字符串**（`amount.minor_units` 是 JSON string 不是
     number），复用既有 `amountBody`/`amountString` 惯例——JS 的
     `number` 是 float64，会在超过 2^53 时静默丢精度；
  2. `stats_by_status` 键是**上游原始状态字面量**，不是归一化分桶——
     归一化分桶走 `/api/v1/metrics` 的 `sub2api.payments.daily`/
     `newapi.payments.daily`，两个通道口径刻意不同；
  3. `next_cursor` 空串表示翻到底，不是 null；
  4. 游标是**不透明的 base64 偏移量**（不是订单 ID——ID 数字字符串化后
     按字典序比较不等价于按数值比较，位数跨界会漏页/重复），前端不应
     假设内部结构，原样回传即可；
  5. `data_source` 恒为 `"<platform>-fake"`（fake 模式）或实例 ID（real
     模式），与用户清单页同一条"演示数据"判读规则。

## files_changed

- `contracts/connectors/payments.read.v1.md`（新增，DRAFT）
- `connectors/sub2api/payments.go`（新增：`Order`/`OrderPage`/
  `DailyPaymentSummary`/`PaymentsReadClient`/状态分桶/观测转换）
- `connectors/sub2api/redact.go`（新增：独立 `maskEmail`）
- `connectors/sub2api/upstream.go`（追加：`fetchOrders`/`fetchOrderPage`/
  `fetchDailyPaymentSummary`/水位辅助函数；新增路由常量
  `routePaymentOrders`）
- `connectors/sub2api/client.go`（`NewClient` 返回类型升级；新增
  `ListOrders`/`DailyPaymentSummary` 公开方法 + 入参校验）
- `connectors/sub2api/fake.go`（`NewFake` 返回类型升级；新增确定性假
  订单生成 + 两个方法的假实现）
- `connectors/sub2api/budget_routes.go`（登记 `sub2api.payment.orders`
  路由，能力仍是既有 `sub2api.orders.read`）
- `connectors/sub2api/client_contract_test.go`（新增 httptest 假上游对
  `/admin/payment/orders` 的固定数据与分页/状态过滤模拟）
- `connectors/sub2api/payments_test.go` / `payments_internal_test.go`
  （新增：Fake 与真实客户端的字段映射、窗口、状态过滤、退款分桶、手续费、
  币种缺口、状态归一化覆盖率测试）
- `connectors/newapi/payments.go`（同上，NewAPI 版本；`refunded` 桶恒
  不出现的专门测试覆盖）
- `connectors/newapi/upstream.go`（追加：复用 `topupUSDQuota` 的
  `fetchOrders`/`fetchOrderPage`/`fetchDailyPaymentSummary`）
- `connectors/newapi/client.go`（同 sub2api）
- `connectors/newapi/fake.go`（同 sub2api）
- `connectors/newapi/client_contract_test.go`（`newClient` 返回类型升级）
- `connectors/newapi/payments_test.go` / `payments_internal_test.go`
  （新增：provider 分桶语义、状态归一化、client-side 状态过滤测试）
- `internal/platform/httpapi/payments.go`（新增：`PlatformOrdersQuerier`
  接口、`ListPlatformOrdersHandler`、offset 游标分页、金额转字符串体）
- `internal/platform/httpapi/payments_test.go`（新增：分页往返、金额是
  字符串、窗口解析、权限、freshness/partial 透传等 11 项测试）
- `internal/platform/httpapi/router.go`（挂载 `GET
  /platforms/{platform}/orders`，`finance.read`，`PlatformOrders` 为 nil
  时不挂载）
- `cmd/platform-api/platformpayments.go`（新增：`dynamicPaymentsQuerier`、
  `paymentsMode`、production+fake 闸、`core.connector_config` 解析、
  `loadSub2APIPaymentsDefaults`/`loadNewAPIPaymentsDefaults`）
- `cmd/platform-api/platformpayments_test.go`（新增：off/fake 模式、
  production 闸、配置行解析、未知平台拒绝等测试）
- `cmd/platform-api/main.go`（装配 `platformPaymentsDeps`，接入路由 Deps）
- `internal/platform/jobs/{sub2api,newapi}_sync.go` / 对应 `_test.go`
  （每轮追加 `DailyPaymentSummary` 读取与观测写入，独立错误分组）
- `internal/platform/jobs/rollup_metadata_test.go`（随新指标登记同步调整）
- `internal/platform/ops/freshness.go`（登记两个新指标键到白名单）
- `internal/platform/ops/metrickeys_test.go`（同步）
- `internal/platform/ops/rollup_policy.go` / `rollup_policy_test.go`
  （登记 `ROLLUP-MULTI-BUCKET` 已知缺口）
- `contracts/ops/metric-rollup-policy.v1.json`（同上，契约侧登记）

## tests_run

- `go build ./...`：PASS（无输出）。
- `go vet ./...`：PASS（无输出）。
- `go test ./...`：全仓 43 个有测试的包全部 `ok`，0 `FAIL`；对本片直接
  涉及的包（`connectors/sub2api`、`connectors/newapi`、
  `internal/platform/httpapi`、`internal/platform/jobs`、
  `cmd/platform-api`、`internal/platform/ops`）额外跑过
  `-count=1`（禁用缓存）确认非陈旧结果，逐条 PASS；集成测试按既有约定
  在未设置 `XM_TEST_DATABASE_URL`/`XM_RUN_INTEGRATION` 时正确 SKIP。
- `gofmt -l .`：本片改动的文件全部干净；仅
  `internal/platform/httpapi/finance_test.go` 被列出——这是**改动前就
  存在、本片未碰**的既有问题（对照基线提交 `fc709e8` 直接验证过，
  XM-USERS-REAL 的交接文档里也记录过同一条）。
- `bash scripts/check-governance.sh`：PASS（exit 0，无输出）。
- `gitleaks detect --no-git`：对本片实际改动的每个目录（
  `connectors/sub2api`、`connectors/newapi`、`cmd/platform-api`、
  `internal/platform/httpapi`、`internal/platform/jobs`、
  `internal/platform/ops`、`contracts`）分别扫描，均 `no leaks found`；
  `internal/platform/ops` 整目录扫描出的唯一一条 `generic-api-key`
  命中位于 `atomicity_store_test.go`（一个既有指标键字面量
  `"sub2api.revenue.daily"` 的常量误报），该文件**不在本片改动范围内**，
  是历史已知的同类误报（与既往 handoff 记录的模式一致），未做处理。
- 上游参考仓库确认未被改动：`git status` 在 `K:/sub2api-src`、
  `K:/newapi-src` 均为 `nothing to commit, working tree clean`。

## not_run / risks

- **未对真实实例验证过**：与两个既有已上线连接器（`connectors/sub2api`、
  `connectors/newapi`）同一条免责声明。契约文档 `payments.read.v1.md`
  末尾"真实实例验证清单"列了 6 项，账号到位后需逐项核对，重点是：
  1. Sub2API 采集账号是否已过 `AdminComplianceGuard` 合规确认（423 门，
     与既有 `DailyOrders` 同一道坑）；
  2. Sub2API 真实订单的 `currency` 字段核对"默认 CNY，Stripe/Airwallex
     走各自配置币种"这条假设；
  3. `RECHARGING` 状态在真实数据里是否真的短暂出现（验证归入
     `succeeded` 不会让"钱没到账"的订单被误判）；
  4. `pay_amount - amount` 在真实订单上是否恒为非负（手续费计算前提）；
  5. NewAPI 是否存在 `payment_provider` 不在已知六者之列的真实订单；
  6. 两平台各查一天真实订单量较大的业务日，核对笔数与上游后台自己
     显示的一致（翻页早停逻辑是否正确覆盖整个窗口）。
- **查询较早历史日期的翻页代价**：见上面"连接器"小节——两个上游列表
  端点都没有服务端日期过滤，查询远早于"现在"的日期需要先翻过该日期
  之后的全部订单，繁忙实例可能提前撞到 `maxPaymentOrderPages=20` 的
  上限而被标记部分数据。`IsPartial`/水位会正确反映这一点，但没有做
  更聪明的窗口定位（比如按 ID 区间二分）——不在本片时间盒内，真实
  订单量级摸清楚之后再决定要不要优化。
- **`Capabilities()` 不探测新路由**：`sub2api.orders.read` 的能力探测
  仍只验证既有两条路由（`/payment/dashboard`+`/dashboard/trend`），不
  要求 `/payment/orders` 也可达——避免让一个仍能正常跑 `DailyOrders`
  的旧版本上游被误判为"不支持 orders.read"。真实账号到位后如果发现
  `/payment/orders` 在某个受支持版本上缺失，需要回来决定处理方式。
- **Rollup 降采样不支持新指标**（见上文"观测"小节），已登记
  `ROLLUP-MULTI-BUCKET` 已知缺口，原始观测不受影响。
- **前端未接入**：`web/` 目录本片未改动，`Sub2ApiFinanceOverview.tsx`/
  `NewApiFinanceOverview.tsx` 仍是下一片的工作；本文档"查询端点"小节
  已把响应形状、金额编码方式、游标语义写清楚供下一片对齐。
- 未做端到端生产验证（把某个 `core.connector_config` 行切成 real、
  实际打到真实上游观察 `/orders` 返回）——本片只有单元测试 + httptest
  集成测试覆盖，模式与 XM-USERS-REAL 相同。
- 未给 `contracts/connectors/sub2api.read.v2.md`/`newapi.read.v1.md`
  （两份已冻结的契约）加反向指向新文档的引用——新文档已经单向说明了
  与旧文档的关系，这两份是冻结文档，改动它们本身需要走文档治理决定，
  留给后续决定要不要做。

## follow_ups

- 前端接入（下一片）：按本文档"查询端点"小节的响应形状对齐
  `Sub2ApiFinanceOverview.tsx`/`NewApiFinanceOverview.tsx`——资金概览卡
  消费 `/api/v1/metrics` 的 `sub2api.payments.daily`/`newapi.payments.daily`
  （归一化分桶），"充值订单"页签消费本片新增的
  `/api/v1/platforms/{platform}/orders`（原始状态逐笔列表）。
- 真实凭据到位后，按"not_run/risks"第一条逐项核对，把探测结果写进
  `docs/inventory/managed-systems.yaml`。
- 净现金流公式：需要业务侧先确认 `amount`/`pay_amount` 的净额关系（是否
  已经净掉手续费、有没有未建模的成本如汇兑价差/chargeback），再决定
  `NetMinorUnits` 的计算公式——契约文档里已经写明这是刻意留白，不是
  遗漏。
- 如果运营发现"查旧日期的订单要翻很多页"在真实数据量下确实是个问题，
  可以考虑给连接器加一个基于 ID 区间估算的窗口定位优化，或者在前端
  交互上引导"越新的日期查询越快"。
- Rollup policy v1 schema 如果将来要支持多桶结构（每个 metric 允许多个
  `primary_json_pointer`），`sub2api.payments.daily`/
  `newapi.payments.daily` 应该是第一批解除 `ROLLUP-MULTI-BUCKET` 门禁
  的候选。
