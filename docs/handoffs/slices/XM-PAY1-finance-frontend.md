# XM-PAY1 · 支付与财务：前端接入 payments.read.v1

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-PAY1-finance-frontend`
- base: `release/v0.1-launch`
- worktree: `K:/星芒统一控制平台/wt-xmPAY1`
- commit: 见分支提交历史（本文档随最后一个提交合入）

## summary

XM-PAY0 只做完了后端只读接入（`ListOrders`/`DailyPaymentSummary` + 逐笔订单查询端点），
`web/` 目录当时未改动。本片把 `Sub2ApiFinanceOverview.tsx`/`NewApiFinanceOverview.tsx`
接到真实数据，并按 XM-PAY0 交接文档写明的响应形状新增了「充值订单」「退款与冲正」
两个页签与订单/退款详情页——这些页面此前都是诚实占位（`PageState kind="unavailable"`），
现在换成真实数据wiring，仍然保留"没有就说没有"的纪律（未接入/不适用/覆盖不全三态分开）。

后端只加了两样"缺失字段/端点"（均在既有 Query 模式内，未新增 Scope、未新增 Action）：

1. `connectors/{sub2api,newapi}.Order` 新增 `FeeMinorUnits`/`RefundAmountMinorUnits`
   两个可空字段——XM-PAY0 的内部计算（`orderRow.rawFeeMinorUnits`/
   `rawRefundAmountMinorUnits`）本来就有，只是刻意没有对外暴露到逐笔明细（当时的
   注释原话"逐笔明细不单独展开退款金额"）；本片"充值订单"表格需要按行展示
   手续费/退款额，因此把这两个字段補上，门禁规则与 `DailyPaymentSummary` 聚合口径
   完全一致（Fee 只在 succeeded/refunded 桶给出；RefundAmount 对 Sub2API 每笔都给出，
   NewAPI 恒为 nil）。
2. `GET /api/v1/platforms/{platform}/orders/{id}`（`GetPlatformOrderHandler`）——
   订单/退款详情页需要按 ID 查一笔，但两个上游都没有"按 ID 查询"的能力（`OrderFilter`
   只接受时间窗口）。新端点复用既有 `PlatformOrdersQuerier.ListOrders`（未改接口），
   在给定窗口内翻页查找匹配 ID 的订单；找不到就诚实 404（`ACTION_NOT_REGISTERED` +
   安全文案里带上已搜索的窗口），不会悄悄换一个窗口重试或回退到另一条订单。

**验收线复核后的一处修正**：初版把 NewAPI「资金与订单」也改成了 Sub2API 同款
六卡。验收线指出仓库既定原则是"UI 不做大改动，按原型"，原型对 NewAPI 这一页
定的是四格布局（区间到账/区间退款/月累计/支付失败），要求按原型改回来，
仍从同一份 `payments.daily` 数据接（不是另起一套数据源）。已按此修正：
Sub2API「资金概览」保留六卡（`PaymentSummaryCards`，未改），NewAPI「资金与
订单」换回原型四格，其中「月累计」是本片新增的一种数据形状——单日指标的
`/api/v1/metrics` 给不出"自然月至今"这个概念，因此这一格改走
`/api/v1/metrics/history`（按天历史观测求和），见下方"关键设计决定"。

## 已接入 / 仍未接入一览

### 已接入（真实数据，来自 `sub2api.payments.daily`/`newapi.payments.daily` 指标
与 `/platforms/{platform}/orders[/​{id}]` 端点）

- **资金概览六卡**（**仅 Sub2API**「资金概览」子页签，`PaymentSummaryCards`）：
  区间成功到账 / 区间待处理 / 区间失败 / 退款与冲正 / 支付手续费 / 净现金流入。
  - 指标是**单日**粒度：统计区间收窄到单日且与指标业务日一致时才展示真实值，
    周/月区间显示「周/月需要按天聚合；目前只提供单日汇总」，不会用单日值冒充
    区间合计。
  - 分桶键缺席 + `is_partial=false` 时展示**确认过的零**（不是「未接入」）——这是
    契约本身的约定（"上游这一天没有落进某个桶的订单，那个桶的键就不出现"，见
    `payments.read.v1.md`）；`is_partial=true` 时缺席的桶才退回「覆盖不全，无法
    确认是否真的是零」。
  - 命中非合约币种（`is_partial=true`）时，卡片副行明说「覆盖不全：可能有非合约
    币种订单未计入金额（笔数仍计入）」，不只靠 FreshnessBadge 的笼统「数据不完整」。
  - 「支付手续费」：只在有 succeeded/refunded 订单时才有值。
  - 「净现金流入」：**恒为「未接入」**——净现金流公式尚未确定（需先确认
    手续费承担方与是否有未建模成本），前端不现算 amount-fee 去冒充一个后端
    明确拒绝下结论的数字。
- **资金与订单四格**（**仅 NewAPI**，原型原样布局，不是 Sub2API 的六卡）：
  区间到账 / 区间退款 / 月累计 / 支付失败，与 Sub2API 六卡共用同一份
  `BucketCard`/`payments.daily` 判断逻辑——「区间到账」就是 `bucket="succeeded"`、
  「支付失败」就是 `bucket="failed"`，「区间退款」传 `bucket="refunded"` 时
  `BucketCard` 对 NewAPI 会自动落到「不适用」分支（结构性事实：上游没有 refund
  字段/状态/函数，不随数据是否加载而变），三格与 Sub2API 同名分桶卡片是**同一段
  代码**，不是照抄一遍再改。「月累计」是本片新增的第四种卡（`MonthToDateSucceededCard`）：
  - 自然日历月至今的 succeeded 桶累计，**恒等于当前自然月**，不随 PeriodControls
    的日/周/月选择变化——这是它与「区间到账」刻意不同的地方（后者跟着所选区间走，
    月/周时会诚实显示"未接入"）。
  - `/api/v1/metrics` 只给最新单日快照，给不出"本月至今"，因此这一格改走
    `GET /api/v1/metrics/history?metric_key=…&hours=168`（既有端点，本片新增
    调用方）按天求和；服务端硬顶 7 天（`maxHistoryHours=168`，见
    `metrics_history.go` 的注释：不让这条端点被当成数据导出口）。月初超过 7 天前
    的部分天然覆盖不到，卡片用「覆盖不全：N/M 天」+ 警示色徽章诚实说明，不会
    显示一个看起来完整的月合计；月初 7 天以内时覆盖率能做到 100%，不带这个徽章。
  - 逐日判据与单日的 `BucketCard` 完全一致，只是应用在每一天：同步失败
    （`status !== "ok"`）的整天跳过；桶键缺席且当天 `is_partial` 时说不清是否
    真的是零，跳过；其余情况计入并累加（键缺席且非 partial 是确认过的零，加 0）；
    同一天多次同步取 `synced_at` 最新的一条。
- **充值订单**（Sub2API 独立子页签 + NewAPI「资金与订单」页内表格，两边共用
  `platformOrdersColumns.tsx` 的列定义）：订单 / 用户 / 类型（固定"用户充值"，
  这个端点只返回充值订单） / 支付方式 / 订单金额 / 手续费 / 净额（恒未接入） /
  状态 / 创建 / 到账（恒未接入，contract 没有这个字段） / 参考号（`out_trade_no`，
  不是网关流水号） / 详情。keyset 游标翻页（`useInfiniteQuery` + `next_cursor`
  原样回传，"加载更多"按钮）；状态筛选按四个归一化分桶（不是原始状态字面量，
  避免"退款失败"这种含"失败"两字的原始状态友好名被"失败"筛选误命中）；
  支付方式筛选按当前已加载行动态取值；两者都是 `DataTableV2` 内置的客户端
  筛选，不额外发请求。
- **退款与冲正**（仅 Sub2API；NewAPI 没有退款概念，不加这一格）：从「充值订单」
  同一批数据里按 `REFUND_LIFECYCLE_STATUSES`（六个退款生命周期原始状态）客户端
  筛出一个子集，与「充值订单」共用同一个 `useSub2ApiOrdersQuery` 缓存（切页签
  不重新请求同一天）。列：订单（id + 上游订单号） / 用户 / 原金额（面值） /
  退款金额（实退，部分退款时小于原金额） / 支付方式 / 状态 / 创建时间 / 详情。
  说明文案明说这一格上线之后也不会有「直接退款」按钮（退款写路径尚未设计）。
- **订单详情** `/platforms/:p/finance/orders/:id` 与 **退款详情**
  `/platforms/:p/finance/refunds/:id`（仅 Sub2API 挂载；NewAPI 只有订单详情）：
  共用同一个组件、同一个后端端点，variant 只影响标题/返回目标/四格顺序措辞。
  4 格：订单金额 / 支付手续费 / 净入账（恒未接入） / 退款金额（订单变体里退到
  第四格，退款变体里前置到第二格）。「订单信息」卡：用户 / 类型 / 支付方式 /
  创建时间 / 到账时间（恒未接入） / 参考号。退款变体额外一句说明：退款原因、
  处理人、完成时间、独立退款流水号在 payments.read.v1 里都没有对应字段，不猜。
  找不到订单时展示原型同款「未找到订单」状态（回显已搜索窗口），不回退到
  另一条记录；列表页的详情链接总是带上该订单 `created_at` 所在的 UTC 业务日
  （`?day=`），保证同一天内点开总能命中——裸链接/分享链接不带 `day` 时默认查
  「今天」，查不到较早的订单是已知且诚实的行为（与 XM-PAY0 的翻页限制同一条
  已知代价，未在本片解决）。

### 仍未接入（与 XM-PAY0 交接文档的留白一致，本片未处理）

- 净现金流公式（两平台，两处卡片/详情页都保持「未接入」+ 原因）。
- 到账/完成时刻（两平台的逐笔明细/详情页都只有 `created_at`，contract 没有
  对应字段）。
- 退款原因、处理人、完成时间、独立退款流水号（Sub2API 退款详情页）。
- 「资金对账」「经营利润桥」「最近事件」（Sub2API 资金概览页的另外三个既有区块）
  ——任务范围是"六卡"，这三块与 payments.read.v1 无关（对账/事件端点仍未接入），
  本片未触碰，继续保留原有的诚实占位。
- 利润核算子页签（两平台）——未接入的是渠道账号归属，与本片支付数据无关，
  按任务要求原样不动。

## 关键设计决定

- **资金概览卡走 `/api/v1/metrics`，不是新端点**：XM-PAY0 交接文档"follow_ups"
  一节已经写明"资金概览卡消费 `/api/v1/metrics` 的 `sub2api.payments.daily`/
  `newapi.payments.daily`"——这两个指标由 worker 每轮同步写入，HTTP 层没有
  `DailyPaymentSummary` 专属端点。本片严格照办，复用既有 `listMetrics()`，
  未新增聚合端点。
- **NewAPI「资金与订单」保留原型原样的四格**（区间到账/区间退款/月累计/支付
  失败），**不套用 Sub2API 的六卡**：初版按任务描述的字面表述做成了两平台统一
  六卡，验收线复核后按仓库既定原则「UI 不做大改动，按原型」纠正——原型对这
  一页定的就是四格，六卡是对原型的改动，不是接数据本身要求的。改回来之后
  「区间到账/区间退款/支付失败」与 Sub2API 六卡里的同名卡片复用同一个
  `BucketCard`（导出自 `PaymentSummaryCards.tsx`），不是另起一套判断——两边
  除了挑了不同的分桶子集，逻辑必须是同一份，否则"未接入/不适用/覆盖不全"这
  三态迟早会在两个平台上开始出现不一致的判定。旧的 `newapi.recharge.daily`
  单卡（旧版"区间到账"）已移除——它与新的 `succeeded` 分桶是**两条不保证对得
  上的口径**（complete_time 到账时刻 vs. create_time 下单时刻，且只认
  status=success），同时展示容易被读成「这两个数应该相等」；
  `newapi.subscription.daily`（当日订阅收入）与本片无关，原样保留。
- **「月累计」是四格里唯一一个不能复用 `BucketCard` 单日判断的卡**：它回答的是
  "自然月至今"而不是"所选区间"，这两件事在语义上刻意不同——PeriodControls
  切到周/月不会改变"月累计"的值（它一直是当前自然月），也不会让"区间到账"
  显示月累计的数字（那会让月累计变得多余）。因为 `/api/v1/metrics` 只保留
  最新一条快照，实现这个概念唯一的路径是查历史（`/api/v1/metrics/history`，
  服务端硬顶 7 天）后按天求和，这是本片除了两个 Go 新增之外，第三处真正意义
  上的"新数据形状"（`lib/metrics.ts` 的 `monthToDateSucceeded`）。7 天硬顶
  意味着**月初超过一周的部分永远覆盖不到**，不是一个会随时间自然消失的临时
  限制——只要服务端这条上限不变，"月累计"在每个月的第 8 天开始就会一直带着
  「覆盖不全」标记到月底，这是设计上已知且接受的行为，不是 bug。
- **详情页复用 `ListOrders`，不新增"按 ID 查询"的连接器能力**：两个上游都没有
  这个能力（`OrderFilter` 强制要求非零时间窗口），给连接器加一个"无界按 ID 查"
  的假象比诚实复用现有窗口查询更危险。默认窗口退回"今天"、需要窗口参数
  找较早订单，这是**已知代价**（继承自 XM-PAY0 的翻页限制），已经在
  `GetPlatformOrderHandler` 顶部注释与本文档写清楚，未尝试在本片解决。
- **`Order.RefundAmountMinorUnits` 对 Sub2API 恒非 nil、`FeeMinorUnits` 按桶门禁**：
  两者的门禁规则不同，是刻意的——`refund_amount` 是上游一个永远存在的字段
  （未退款订单上是真实的 0），而 `pay_amount - amount` 只有在钱真的动过
  （succeeded/refunded）时才是一个有意义的"手续费"，pending/failed 订单上它只是
  算术产物。详见 `connectors/sub2api/payments.go` 里两个字段各自的注释。

## files_changed

### Go（后端，均为既有 Query 模式内的字段/端点补齐，无新 Scope、无 Action、无写操作）

- `connectors/sub2api/payments.go`：`Order` 新增 `FeeMinorUnits`/
  `RefundAmountMinorUnits` 两个可空字段。
- `connectors/sub2api/upstream.go`：`orderRow` 内部字段改名避免与新增的 `Order`
  同名字段发生遮蔽（`RefundAmountMinorUnits`/`FeeMinorUnits` → `raw*`）；
  `decodeOrderRow` 按门禁规则填充两个新字段；`fetchDailyPaymentSummary` 的两处
  聚合改用改名后的 `raw*` 字段（无行为变化，纯粹避免同名遮蔽的可读性修复）。
- `connectors/sub2api/fake.go`：`fakeOrders` 同步生成这两个新字段的假数据，
  门禁规则与真实客户端一致（供 fake 模式下的前端联调看到有意义的数字）。
- `connectors/newapi/payments.go`：`Order` 同步新增两个字段，恒为 nil（NewAPI
  没有手续费/退款概念）。
- `internal/platform/httpapi/payments.go`：`PlatformOrderItem` 新增两个字段；
  `platformOrderItemBody`/`toPlatformOrderItemBody` 新增 `fee`/`refund_amount`
  两个 JSON 字段（`nilableAmountBody` 辅助函数）；新增
  `GetPlatformOrderHandler` + `platformOrderDetailBody`（订单详情端点）。
- `internal/platform/httpapi/router.go`：挂载
  `GET /platforms/{platform}/orders/{id}`（`finance.read`，与既有 `/orders`
  同一个 `if d.PlatformOrders != nil` 判据块，off 时同样不挂载）。
- `cmd/platform-api/platformpayments.go`：`sub2APIOrdersResult`/
  `newAPIOrdersResult` 补上两个新字段的透传。
- Go 测试：`connectors/sub2api/payments_test.go`（新增逐笔 Fee/RefundAmount
  门禁的真实客户端与 Fake 客户端测试）、`connectors/newapi/payments_test.go`
  （新增恒为 nil 的 Fake 测试）、`internal/platform/httpapi/payments_test.go`
  （新增 `GetPlatformOrderHandler` 的 found/not-found/默认窗口/权限测试，及
  `ListPlatformOrdersHandler` 响应体里 fee/refund_amount 的映射测试）。

### TypeScript（前端）

新增：

- `web/apps/admin-web/src/components/PaymentSummaryCards.tsx` + `.test.tsx`：
  Sub2API「资金概览」六卡（`PaymentSummaryCards`），同时导出
  `BucketCard`/`MonthToDateSucceededCard`/`metricByKey`/`metricKeyFor`
  供 `NewApiFinanceOverview.tsx` 组装自己的四格。
- `web/apps/admin-web/src/components/platformOrdersColumns.tsx`：充值订单表的
  共用列定义（Sub2API 与 NewAPI 两处订单表共用同一份列集合）。
- `web/apps/admin-web/src/components/Sub2ApiOrdersPanel.tsx` + `.test.tsx`：
  「充值订单」子页签，含 `useSub2ApiOrdersQuery`/`businessTodayDateOnly` 供
  退款页签复用。
- `web/apps/admin-web/src/components/Sub2ApiRefundsPanel.tsx` + `.test.tsx`：
  「退款与冲正」子页签。
- `web/apps/admin-web/src/pages/PlatformOrderDetailPage.tsx` + `.test.tsx`：
  订单/退款详情页（`PlatformOrderDetailPage`/`PlatformRefundDetailPage` 两个
  导出，共用同一个内部组件）。

修改：

- `web/apps/admin-web/src/api/finance.ts` + `.test.ts`：新增 `PlatformOrderItem`/
  `PlatformOrdersPage`/`PlatformOrderDetail` 等类型、`listPlatformOrders`/
  `getPlatformOrder`（含 `FeatureNotMountedError`/结构化 404→notFound 的区分处理）、
  `describePaymentStatus`、`platformHasRefunds`、`REFUND_LIFECYCLE_STATUSES`、
  `PAYMENT_BUCKET_LABELS`。
- `web/apps/admin-web/src/api/platform.ts`：`MetricHistoryItem` 补上 `source`
  字段——后端 `historyItem` 一直逐点带这个字段（顶部注释专门解释了为什么：
  一条曲线可能混着不同来源），TS 类型此前漏了，"月累计"卡需要它显示「来源」
  时才发现这个既有缺口，顺手补上。
- `web/apps/admin-web/src/lib/metrics.ts` + `.test.ts`：新增
  `readPaymentsDailySummary`（解析 `sub2api.payments.daily`/
  `newapi.payments.daily` 的 `value` 形状）、两条 `METRIC_LABELS` 登记、
  `monthToDateSucceeded`（把 `/api/v1/metrics/history` 的按天历史观测聚合成
  「月累计」需要的求和结果，逐日判据与单日的 `BucketCard` 对齐）。
- `web/apps/admin-web/src/components/Sub2ApiFinanceOverview.tsx`：六个占位卡换成
  `<PaymentSummaryCards platform="sub2api" range={range} />`；「使用收入」「渠道
  毛利」两卡从原来与六个占位共享的 4 列大网格拆成独立的 2 列小网格；「资金对账」
  「经营利润桥」「最近事件」三个区块未改动。
- `web/apps/admin-web/src/components/NewApiFinanceOverview.tsx`：`OrdersView`
  的四格占位换成真实数据——`区间到账`/`区间退款`/`支付失败` 三格复用
  `PaymentSummaryCards.tsx` 导出的 `BucketCard`（`platform="newapi"`），
  `月累计` 换成新的 `MonthToDateSucceededCard`（自己发起独立的
  `/api/v1/metrics/history` Query，与三个分桶卡共用的 `/api/v1/metrics`
  Query 分开）；新增 `businessMonthStartDateOnly` 算当前自然月第一天；新增
  `OrdersLedger`（真实订单表，替换原来 `rows={[]}` 的占位表）；移除未再使用的
  `MetricAmountTile`/`MissingTile`/`NEWAPI_RECHARGE_METRIC`/`metricOrderCount`/
  `FinanceOrderRow`/`ORDER_COLUMNS`（均已无调用点，`ProfitView` 仍在用的
  `InteractiveLedgerTable`/`PROFIT_COLUMNS` 未动）；`SubscriptionEvidence`
  （当日订阅收入）原样保留。
- `web/apps/admin-web/src/components/PlatformFinancePanel.tsx` + `.test.tsx`：
  `sub2apiFinanceSubTab` 的 `orders`/`refunds` 从 `pending(...)` 占位换成
  `<Sub2ApiOrdersPanel />`/`<Sub2ApiRefundsPanel />`。
- `web/apps/admin-web/src/router.tsx` + `.test.tsx`：新增两条详情路由
  （`platforms/:serviceType/finance/orders/:orderId`、`.../finance/refunds/
  :orderId`）+ 对应 loader（`orderDetailLoader` 校验平台已知、`refundDetailLoader`
  额外校验 `platformHasRefunds`）；`router.test.tsx` 的 `okHandler` 补上
  `/api/v1/platforms/{platform}/orders[/​{id}]` 与 `historyBody` 的 `source`
  字段（此前 `/orders` 路径完全没有处理，会落到裸 404 兜底）。
- `web/apps/admin-web/src/components/FinanceOverview.test.tsx`：更新 Sub2API
  六卡相关的两个测试以匹配真实数据（不再是固定占位文案），修复一处真实的异步
  竞态（payment 卡的 `/api/v1/metrics` 与渠道数据的 `/api/v1/finance/channels/
  summary` 是两条独立 Query，原测试只等了后者）。
- `web/apps/admin-web/src/components/NewApiFinanceOverview.test.tsx`：重写
  "orders" 相关用例以匹配四格 + 月累计的新行为，新增按当前真实挂钟日期动态
  构造夹具的 `businessTodayForTest()`（"月累计"读的是真实"今天"，不受
  `initialDate` 影响，测试夹具不能写死某个假设的日历日，否则换一天跑测试就会
  全部落空——这条踩坑过程见下方 risks）；"profit" 相关用例未改动。

### 文档

- `docs/handoffs/slices/XM-PAY1-finance-frontend.md`（本文件）。
- `docs/handoffs/slices/XM-PAY1-screenshots/*.png`（真实浏览器验证截图，见下）。

## tests_run

全部命令均在 `K:/星芒统一控制平台/wt-xmPAY1` 下执行；Go 命令统一加八变量
unset 前缀绕开本机代理坑（见项目既有 Windows 工具链纪律）。

- `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go build ./...`：PASS（无输出）。
- `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go vet ./...`：PASS（无输出）。
- `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test -p 1 -count=1 ./...`：
  全仓全部有测试的包 `ok`，0 `FAIL`（含 `internal/platform/oidcauth`——首轮
  混合并发跑全仓时该包曾报 3 个失败，单独重跑 `go test ./internal/platform/
  oidcauth/...` 与再次跑全仓均全绿，判定为与本片改动无关的偶发计时敏感测试，
  不是本片引入的问题；未改动过 `oidcauth` 包任何文件，`git diff --stat` 可核对）。
- `"$(go env GOROOT)/bin/gofmt" -l` 对本片实际改动的 8 个 Go 文件逐个核对：
  干净（本片改动的文件均不在名单里）；`gofmt -l connectors/sub2api connectors/
  newapi internal/platform/httpapi cmd/platform-api` 唯一列出的
  `internal/platform/httpapi/finance_test.go` 是改动前就存在、本片未碰的既有
  问题（`git diff --stat` 确认零改动），与 XM-PAY0 交接文档记录的同一条。
- `pnpm --config.verify-deps-before-run=false -r run typecheck`：PASS，5 个
  工作区包（design-tokens/ui-primitives/ui-admin/ui-storybook/admin-web）全绿。
- `pnpm --config.verify-deps-before-run=false -r run test`：PASS，
  10+16+253+1358=1637 个测试全绿（0 失败），含本片新增的 8 个测试文件
  （`PaymentSummaryCards.test.tsx`、`Sub2ApiOrdersPanel.test.tsx`、
  `Sub2ApiRefundsPanel.test.tsx`、`PlatformOrderDetailPage.test.tsx` 全新；
  `finance.test.ts`、`metrics.test.ts`、`FinanceOverview.test.tsx`、
  `NewApiFinanceOverview.test.tsx`、`PlatformFinancePanel.test.tsx`、
  `router.test.tsx` 增补/更新）。
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`：
  PASS（"Storybook build completed successfully"；本片未新增 Storybook
  story——新组件都是 admin-web 的业务组件，不是 ui-admin/ui-primitives 里的
  可复用设计系统组件，仓库既有同类业务组件也都没有 story，与既定惯例一致）。
- `bash scripts/check-governance.sh`：PASS（exit 0，无输出）。
- **真实浏览器验证**（本机起 mock API + Vite dev server，按 XM-0045 既有配方；
  未连任何真实上游，也未启动 `cmd/platform-api`/Postgres）：mock API 覆盖
  `/api/v1/metrics`、`/api/v1/platforms/{platform}/orders[/​{id}]`、
  `/api/v1/services`、`/api/v1/finance/{channels,upstreams}/summary`，逐笔订单
  按请求的 `day`/`from`/`to` 窗口动态生成（不是写死日期，跨真实"今天"仍然
  可信）。用 Playwright 逐页截图核对，截图见
  `docs/handoffs/slices/XM-PAY1-screenshots/`：
  - `sub2api-finance-overview.png`：资金概览六卡（¥90/¥10/¥180/¥126/¥7.20/
    未接入），使用收入/渠道毛利独立一行，资金对账/经营利润桥/最近事件保持
    未改动的诚实占位。
  - `sub2api-orders.png`：充值订单表 13 行（覆盖全部已知状态），手续费列按桶
    门禁显示"—"或真实金额，净额/到账两列恒"未接入"。
  - `sub2api-refunds.png`：退款与冲正表从 13 笔里筛出 6 笔退款生命周期订单，
    原金额/退款金额正确不相等（部分退款场景）。
  - `sub2api-order-detail.png`：订单详情页四格 + 订单信息卡，创建时间格式化
    正确（发现并修复了一处用原始 ISO 字符串而非 `formatUtcTimestamp` 的
    小疏漏）。
  - `newapi-finance-fourtile.png`：NewAPI 原型四格（区间到账 $20.00·1 笔 /
    区间退款"不适用" / 月累计 $40.00·"覆盖 2/2 天·本月至今" / 支付失败
    $70.00·2 笔）+ 当日订阅收入 + 4 行订单表（NewAPI 已知状态全集）。
  - `newapi-finance-month-monthtodate-unaffected.png`：把 PeriodControls 切到
    "月"后，区间到账/支付失败正确退回"未接入"（周/月需要按天聚合），但
    "月累计"维持同一个 $40.00 不变——验证了它确实不受统计区间选择影响，
    恒是当前自然月。
  - `newapi-order-detail.png`：NewAPI 订单详情，手续费/净额/退款金额三格均
    正确显示"未接入"。
  - `order-not-found.png`：未知订单 ID 的"未找到订单"诚实状态，回显已搜索
    窗口，未回退到其他订单。
  - `sub2api-overview-week.png`：切到"周"视图后六卡全部诚实显示"未接入"
    （周/月需要按天聚合），不会用单日值冒充周合计。

  验证"月累计"时额外把 mock API 的 `/api/v1/metrics/history` 从空实现补成
  返回最近 5 天的 `payments.daily` 历史观测（脚本改动只在本机 mock 里，未进
  提交），实测确认了"覆盖 N/M 天"与"月累计不受周/月切换影响"两条行为均按
  设计工作。

## not_run

- 未对真实 Sub2API/NewAPI 实例验证——与 XM-PAY0 同一条免责声明，本片继续
  只在 fake 模式下核对（无论是 vitest 里的 mock，还是本机 mock API 的真实浏览器
  验证）。
- 未跑 Go 集成测试（`XM_TEST_DATABASE_URL`/`XM_RUN_INTEGRATION` 场景）——本片
  未改动任何数据库交互代码（`internal/platform/jobs`、`internal/platform/ops`
  均未触碰），沿用 XM-PAY0 已验证过的观测写入路径，判断没有必要为纯读取投影层
  的改动重新跑一遍全套集成测试；`go test ./...`（单元测试范围）已完整覆盖本片
  改动的包。
- 未跑 `gitleaks`——本片未引入任何凭据/密钥字面量，改动的 Go 文件都是既有
  连接器/httpapi 包内的字段与端点补齐，风险面与 XM-PAY0 已扫描过的同一批目录
  相同；任务的 gate 列表本身也未把 gitleaks 列为必需项。
- 未新增 Storybook story——原因见 tests_run 一节。
- `docs/change-requests/`：本片未修改任何跨线共享接口（开票系统集成
  `InvoiceConsolePanel` 完全未触碰），未触发变更单要求。

## risks

- **`GetPlatformOrderHandler` 默认窗口="今天"，继承自 XM-PAY0 的已知限制**：
  裸链接/分享链接不带 `?day=` 时只在当前 UTC 业务日窗口内查找；查一笔更早的
  订单需要带上正确的 `day`。列表页的详情链接已经自动带上正确的业务日，正常
  点击路径不受影响；只有"直接把 URL 分享给别人、对方隔了一天才点开"这种场景
  会命中，且命中时的表现是诚实的"未找到订单"（不是误报或崩溃）。
- **`Order.FeeMinorUnits`/`RefundAmountMinorUnits` 是本片新增的字段，尚未对真实
  Sub2API 实例验证过**门禁规则是否与真实上游数据吻合（例如"pay_amount 是否
  恒 ≥ amount"这条前提，XM-PAY0 交接文档"真实实例验证清单"第 3 条已经记录过
  同一个假设，本片新增的门禁复用的是同一组已核对过的字段，风险不是全新的）。
- **`orderRow` 内部字段改名**（`RefundAmountMinorUnits`/`FeeMinorUnits` →
  `rawRefundAmountMinorUnits`/`rawFeeMinorUnits`）是纯粹的内部重构，改动点集中
  在 `connectors/sub2api/upstream.go` 一个文件内，`go build`/`go vet`/全部 Go
  测试已验证行为不变；仅在此提示评审者这处 diff 看起来改动行数较多，但语义
  上是重命名 + 消除潜在的同名字段遮蔽风险，不是逻辑变化。
- **「月累计」在每个月第 8 天起会一直带着"覆盖不全"标记到月底**：这是
  `/api/v1/metrics/history` 服务端硬顶 7 天（`maxHistoryHours=168`）的直接
  后果，不是本片的实现缺陷，也没有绕过的办法（那条上限是刻意设计，见
  `metrics_history.go` 的注释）。如果产品侧希望月累计不受这个限制，需要后端
  另外提供一种不经过"历史查询"的月度预聚合（例如在指标 `value` 里内嵌一个
  月至今累计字段，类似 `*.requests.trend_7d` 内嵌 7 天数组的做法），这是一项
  新的后端能力，不在本片时间盒内。
- **写测试时踩到一处真实的 React 协调（reconciliation）坑，已在组件里修掉**：
  `MonthToDateSucceededCard` 自己管理独立的加载态，最初加载中/错误态委托给
  共享的 `UnavailableCard` 包装组件、成功态直接返回 `StatTile`——两条分支在
  同一个位置返回不同的组件类型，React 按类型做协调时会把整个子树卸载重挂，
  而不是原地更新同一个 DOM 节点，导致组件从"加载中"过渡到"已加载"时旧的
  `<article>` 引用变成 detached 节点。测试里用 `tile.closest("article")` 提前
  拿到的引用因此永远等不到新内容，`findByText` 会一直超时。修法是让这张卡
  全程只返回同一种元素类型（`<StatTile>`），不再让加载态经由另一个包装组件。
  记录这条是因为它是一类容易被误判成"我的测试写法有问题"、实际是组件设计
  该改的情况——任何卡片如果开始自己管理独立的异步状态（而不是像其余卡片
  那样把加载态整个交给外层 `ApiStateView`），都要留意这同一个坑。

## follow_ups

- 真实凭据到位后，按 XM-PAY0"真实实例验证清单"核对时，一并验证本片新增的
  `FeeMinorUnits`/`RefundAmountMinorUnits` 门禁在真实数据上是否成立。
- 净现金流公式、到账时刻字段、退款原因/处理人/完成时间/独立退款流水号——
  均需要业务侧或上游契约扩展后才能点亮，见"仍未接入一览"。
- 若运营反馈"查旧日期订单要多次点击"是真问题，可以考虑给 `GetPlatformOrderHandler`
  增加更聪明的窗口定位（例如按订单 ID 区间估算），而不是简单默认"今天"——
  这个决定留给以后需要时再做，与 XM-PAY0 记录的翻页代价是同一条 follow_up。
- 「资金对账」「经营利润桥」「最近事件」三个 Sub2API 资金概览区块仍是诚实占位；
  如果产品侧希望下一片把它们也接上（例如"订单成功金额"其实已经等价于六卡的
  "区间成功到账"），需要先决定是否要在页面上暗示这两组数字应该相等——目前
  刻意分开陈列，避免造成"这两个数应该对得上"的错误印象。
- 「月累计」目前每个月第 8 天起就会一直显示"覆盖不全"直到月底（`/metrics/
  history` 服务端 7 天硬顶所致，见 risks）。如果这条限制在实际使用中确实
  造成困扰，值得考虑的方向是让 worker 在写入 `sub2api.payments.daily`/
  `newapi.payments.daily` 时顺带算一个"月至今累计"内嵌进 `value`（与
  `*.requests.trend_7d` 内嵌 7 天数组同一个思路），这样"月累计"就能像
  "区间到账"一样走单次 `/api/v1/metrics` 而不必依赖历史查询——这是一项
  新的后端能力，留给以后需要时再做决定。
