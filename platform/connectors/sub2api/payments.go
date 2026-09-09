package sub2api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// payments.read.v1（XM-PAY0）：逐笔订单读取 + 按日按状态汇总。
//
// 与既有 DailyOrders/OrderSummary（XM-R013，走 /api/v1/admin/payment/dashboard
// 的按日聚合序列）是**两条独立口径**，故意不合并：
//   - DailyOrders 只回答"这天收入多少、成本多少"，金额来自上游自己按天预聚合的
//     daily_series，无法拆到逐笔、也拆不出待处理/失败/退款；
//   - 本文件回答"这天有哪些订单、每张分桶卡片各多少"，逐笔来自
//     /api/v1/admin/payment/orders（该端点**没有任何日期过滤参数**，见
//     upstream.go 的 fetchOrders），窗口边界由本包自己翻页判定。
//
// 两条口径的"这一天"计算方式也不同：DailyOrders 按上游自己的服务器时区分日
// （上游按 days-back 计算，见 fetchPaymentDay 的注释）；本文件统一按
// **created_at（下单时刻）+ 平台声明的业务日时区**分日，对所有状态一视同仁。
// 这是本文件的显式选择，不是两边本该一致却没做到——原因见 fetchOrders 顶部
// 的注释。两条口径的数字因此不保证互相对得上，这是已知且刻意的差异。

// Order 是一笔支付订单的逐笔记录。
//
// 不嵌 Snapshot：一整页订单来自同一次读取，新鲜度由 OrderPage.Snapshot 统一
// 表达——逐笔重复同一个时间戳没有信息量（对照 ManagedChannel 逐项 Snapshot：
// 那边的每一项来自独立的探测调用，时刻真的不同；这里的每一项来自同一次
// 分页读取，时刻本就相同）。
type Order struct {
	// OrderID 是上游订单的数据库主键（payment_orders.id），字符串化。
	OrderID string
	// CreatedAt 是下单时刻（上游 created_at），UTC。
	CreatedAt time.Time
	// Status 是上游原始状态字面量（PENDING/PAID/COMPLETED/REFUNDED/…），
	// **不做归一化**——逐笔明细必须能核对回上游后台看到的原始状态。
	// 归一化分桶只用于 DailyPaymentSummary，见 paymentStatusBucket。
	Status string
	// AmountMinorUnits 是订单面值（上游 amount 字段，不含 pay_amount 里
	// 附加的手续费部分），整数最小货币单位。选它而不是 pay_amount 的理由见
	// upstream.go fetchOrders 的注释。
	AmountMinorUnits int64
	// Currency 是**这一笔订单自己的真实币种**，逐行独立，不假设与
	// DailyOrders/OrderSummary 的合约币种一致——上游按 Stripe/Airwallex
	// 的实际收款币种记账，默认 CNY（K:/sub2api-src
	// internal/service/payment_currency.go），既不是全局常量也不能猜。
	Currency string
	// Method 是支付方式（alipay/wxpay/stripe/card/link/easypay/airwallex）。
	//
	// 命名刻意避开"channel"：本连接器包里的"渠道"（ChannelBalance /
	// ManagedChannel）指的是完全不同的另一个概念——上游供应商账号
	// （见 budget_routes.go 与 channel_directory.go 顶部的命名陷阱说明）。
	// 两个不同的概念共用一个中文名"渠道"已经容易混淆，字段名不能再重复这个坑。
	Method string
	// UserRef 是打码后的用户引用（宪法 7 条：明文邮箱不落库、不进任何响应）。
	// 优先给打码邮箱；邮箱解析不出时退到 "u<user_id>"，见 maskUserRef。
	//
	// 这不是 connectors/platformusers.UserRef——那是跨系统查询用的稳定
	// 不透明 ID，规格明确禁止用邮箱/打码邮箱充当（见
	// docs/superpowers/plans/2026-08-28-platform-user-read-v2.md 的
	// "禁止 username、email、masked email、token prefix join"）。这里的
	// UserRef 只是给人看的展示字段，不作为任何跨系统 join 键。
	UserRef string
	// UpstreamOrderRef 是上游自己生成的业务订单号（out_trade_no），
	// 供人工核对时在上游后台按号查找——不是网关自己的 trade no
	// （payment_trade_no），后者只在网关自己的对账台上有意义。
	UpstreamOrderRef string
	// FeeMinorUnits 是这一笔订单的支付手续费（pay_amount-amount），
	// **只在这笔订单的钱真的动过时才公开**：与 DailyPaymentSummary 聚合
	// 手续费同一条闸（只对 succeeded/refunded 两个归一化桶求和，见
	// paymentStatusBucket 的注释）——pending/failed 订单的 pay_amount-amount
	// 只是算术产物，不是已发生的手续费，公开出去会被误读成"这笔待处理订单
	// 已经扣了手续费"。nil 表示"这笔订单当前状态下不适用"，不是 0
	// （规格 §12）。XM-PAY1 新增：逐笔明细此前不展开手续费，见本文件旧注释
	// （已随本次改动一并更新）。
	FeeMinorUnits *int64
	// RefundAmountMinorUnits 是上游 refund_amount 字段，**对每一笔订单都
	// 公开**：这个字段本身永远存在且有意义——未退款订单上就是真实的 0，
	// 不是"不知道"，与 FeeMinorUnits 的按桶门禁不是同一条规则（退款金额
	// 不需要"这笔钱是否已结算"这个前提，它只是上游记录的一个数字字段）。
	// XM-PAY1 新增，供"退款与冲正"页签按笔展示退款额。
	RefundAmountMinorUnits *int64
}

// OrderStats 是若干订单的笔数与金额合计。
type OrderStats struct {
	Count int64
	// AmountMinorUnits 只对连接器配置币种（WithCurrency，默认 USD）求和；
	// 其余币种的订单计入 Count 但不计入这里，也不悄悄换算——见
	// OrderPage.IsPartial 与 fetchOrders 对 currencyGap 的处理。
	AmountMinorUnits int64
}

// OrderFilter 圈定 ListOrders 的查询窗口。
type OrderFilter struct {
	// From、To 是**下单时刻**的闭区间（UTC），两者必须非零。
	// 没有"不限日期"这个选项：上游订单列表没有服务端日期过滤（见
	// fetchOrders），无界查询只能翻到页数上限为止，那是一个会随数据量
	// 变化、调用方猜不到的截断点，不如从接口形状上直接禁止。
	From, To time.Time
	// Status 非空时按上游原始状态过滤（服务端过滤，见 KnownOrderStatuses）；
	// 空则不过滤状态，返回窗口内全部状态。
	Status string
}

// OrderPage 是 ListOrders 的返回值：窗口内匹配的全部订单 + 按状态的统计。
//
// Items 不做游标/条数截断——那是 HTTP 层基于已知总量做的展示分页
// （对照 ListPlatformChannelsHandler 的同一套路：连接器给全量候选，
// 处理器自己切页）。IsPartial 表示**上游翻页到了页数上限还没扫完窗口**，
// 这时 Items 与 StatsByStatus 都可能少报，不是"没有更多数据"。
type OrderPage struct {
	Snapshot
	Items         []Order
	StatsByStatus map[string]OrderStats
}

// DailyPaymentSummary 是某业务日按归一化状态分桶的资金汇总（资金概览卡的口径）。
type DailyPaymentSummary struct {
	Snapshot
	// Day 是业务日，格式 2006-01-02。
	Day string
	// Currency 是本次汇总的合约币种（连接器配置，默认 USD）。
	Currency string
	// ByStatus 的键是归一化分桶（PaymentStatusSucceeded 等 4 个常量之一）。
	// **上游这一天没有任何订单落进某个桶，那个桶的键就不出现**——不写
	// {Count:0, AmountMinorUnits:0} 冒充"确认过是零"，那与"没读到"是两回事
	// （规格 §12：禁止裸数字冒充完整数据）。
	ByStatus map[string]StatusAmount
	// FeeMinorUnits 是当日手续费合计（sum(pay_amount-amount)，只对已经
	// captured 到资金的订单——即 succeeded 与 refunded 两个桶——求和，
	// pending/failed 的订单钱没有真的动过，谈不上手续费）。
	// nil 表示未知，绝不用 0 冒充"确认过没有手续费"。
	FeeMinorUnits *int64
	// NetMinorUnits 留空：净现金流需要额外假设手续费的承担方与是否还有
	// 未建模的成本（chargeback、汇兑价差），本片核对不到，不猜（规格
	// §18.9 证据优先级 + 本任务"不猜字段"的硬约束）。留给以后核实清楚
	// 再补，见 contracts/connectors/payments.read.v1.md 的 open_questions。
	NetMinorUnits *int64
}

// StatusAmount 是某个归一化分桶的笔数与金额。
type StatusAmount struct {
	Count            int64
	AmountMinorUnits int64
}

// 归一化资金状态分桶——资金概览卡"成功到账/待处理/失败/退款与冲正"的键。
const (
	PaymentStatusSucceeded = "succeeded"
	PaymentStatusPending   = "pending"
	PaymentStatusFailed    = "failed"
	PaymentStatusRefunded  = "refunded"
)

// KnownOrderStatuses 是上游 payment_orders.status 的全部合法取值，逐字抄自
// K:/sub2api-src backend/internal/payment/types.go 的 OrderStatus* 常量
// （只读核对，未修改上游源码）。HTTP 层的 status 过滤参数据此校验，
// 拒绝而不是静默返回空结果，一个拼错的状态值才不会被误读成"这天没这类订单"。
var KnownOrderStatuses = []string{
	"PENDING", "PAID", "RECHARGING", "COMPLETED", "EXPIRED", "CANCELLED", "FAILED",
	"REFUND_REQUESTED", "REFUNDING", "REFUND_PENDING", "PARTIALLY_REFUNDED", "REFUNDED", "REFUND_FAILED",
}

// KnownOrderStatus 报告 status 是否是 KnownOrderStatuses 之一（大小写不敏感）。
func KnownOrderStatus(status string) bool {
	want := strings.ToUpper(strings.TrimSpace(status))
	for _, s := range KnownOrderStatuses {
		if s == want {
			return true
		}
	}
	return false
}

// paymentStatusBucket 把上游原始状态归一化成资金概览卡的四个分桶。
//
// 归类依据（K:/sub2api-src 逐条核对源码，不是猜的）：
//
//   - succeeded：PAID / RECHARGING / COMPLETED。三者在
//     internal/service/payment_fulfillment.go 的状态机里是同一条链
//     （PAID → RECHARGING → COMPLETED），资金在进入 PAID 那一刻就已经被
//     网关 captured；RECHARGING 只是"正在把已收到的钱记入用户余额"这一步
//     内部过渡态，不是"钱还没到"。
//     ⚠️ 这与上游自己的 publicOrderStatusPaid()
//     （internal/handler/payment_handler.go）不同——那个函数是给**终端用户**
//     看的"支付完成了吗"布尔值，只认 PAID/COMPLETED（+ 退款链的状态），
//     不认 RECHARGING，大概率是因为这个过渡态通常以秒计、用户端来不及看到。
//     这里回答的是财务问题"钱到账了没有"，与终端用户 UX 问题的答案不必相同，
//     不能照抄那个函数。
//   - pending：PENDING。订单已创建，尚未收到网关确认。
//   - failed：EXPIRED / CANCELLED / FAILED。三者都是"这笔钱最终没有进来"
//     的终态——CANCELLED 归进这里而不是单列，是因为资金概览卡只有四格，
//     从"钱有没有到账"这个问题看，主动取消和支付失败的答案相同。
//   - refunded：REFUND_REQUESTED / REFUNDING / REFUND_PENDING /
//     REFUND_FAILED / PARTIALLY_REFUNDED / REFUNDED。整条退款生命周期
//     （含尝试失败）都算进"退款与冲正"：订单一旦进入这条链，这笔钱就不再是
//     干净的已确认收入；REFUND_FAILED 虽然钱还留在账上，但代表一次未完成的
//     退款操作，需要人工介入，混进 succeeded 会把一个待处理问题藏起来。
//
// 逐笔明细（ListOrders）永远给原始状态，不受这个分桶影响——分桶归类错了
// 只影响一张聚合卡片，不会污染可核对的原始记录。
func paymentStatusBucket(rawStatus string) (bucket string, ok bool) {
	switch strings.ToUpper(strings.TrimSpace(rawStatus)) {
	case "PAID", "RECHARGING", "COMPLETED":
		return PaymentStatusSucceeded, true
	case "PENDING":
		return PaymentStatusPending, true
	case "EXPIRED", "CANCELLED", "FAILED":
		return PaymentStatusFailed, true
	case "REFUND_REQUESTED", "REFUNDING", "REFUND_PENDING", "REFUND_FAILED", "PARTIALLY_REFUNDED", "REFUNDED":
		return PaymentStatusRefunded, true
	default:
		return "", false
	}
}

// PaymentsReadClient 是 ReadClientV2 之上叠加的 payments.read.v1 切片
// （XM-PAY0）：逐笔订单查询 + 按日按状态资金汇总。
//
// 独立成接口而不是直接扩进 ReadClientV2：payments.read.v1 是一份独立版本化
// 的契约（contracts/connectors/payments.read.v1.md 从 DRAFT 起步），
// 不与 sub2api.read 的 v1/v2 共用版本号——两者各自的破坏性变更不该互相牵连。
// 真实客户端与 Fake 都实现本接口，NewClient/NewFake 因此返回本接口
// （结构上是 ReadClientV2 的超集，旧调用方按 ReadClientV2 使用不受影响）。
type PaymentsReadClient interface {
	ReadClientV2

	// ListOrders 读取 [filter.From, filter.To] 闭区间内（按下单时刻）、
	// 可选按 filter.Status 过滤的全部订单，及其按原始状态的统计。
	// 上游没有日期过滤参数，本方法自己翻页并在页数上限截断——命中上限时
	// OrderPage.IsPartial 为真，见 OrderPage 的注释。
	ListOrders(ctx context.Context, filter OrderFilter) (OrderPage, error)

	// DailyPaymentSummary 读取某业务日（2006-01-02，按连接器声明的业务日
	// 时区）按归一化状态分桶的资金汇总。
	DailyPaymentSummary(ctx context.Context, day string) (DailyPaymentSummary, error)
}

// 指标键（写进 ops.metric_observation 的 metric_key）。
const MetricPaymentsDaily = "sub2api.payments.daily"

// defaultPaymentsStalenessThresholdSeconds 与本包其余指标取齐（30 分钟）。
const defaultPaymentsStalenessThresholdSeconds int32 = 1800

// ToPaymentsDailyObservation 把 DailyPaymentSummary 转成新鲜度模型，接进看板。
//
// 独立于 ToObservations：DailyPaymentSummary 由 worker 按需为若干业务日各调
// 一次 DailyPaymentSummary（不像 UserStats/OrderSummary 那样每轮固定只有一份），
// 调用方（jobs 的 sub2api_sync）为每个业务日各写一条本指标的观测，
// Source 沿用该实例既有的 instanceID，不额外拼日期后缀——业务日已经是
// Value 里的字段，也已经在 Watermark 里，不需要靠 Source 再区分一次。
func ToPaymentsDailyObservation(
	now time.Time, instanceID, environment string, summary DailyPaymentSummary,
) ops.Observation {
	byStatus := make(map[string]any, len(summary.ByStatus))
	for bucket, amount := range summary.ByStatus {
		byStatus[bucket] = map[string]any{
			"count":              amount.Count,
			"amount_minor_units": amount.AmountMinorUnits,
		}
	}
	value := map[string]any{
		"day":             summary.Day,
		"currency":        summary.Currency,
		"by_status":       byStatus,
		"fee_minor_units": nilableInt64(summary.FeeMinorUnits),
		"net_minor_units": nilableInt64(summary.NetMinorUnits),
	}
	return snapshotToObservation(MetricPaymentsDaily, instanceID, environment, summary.Snapshot, now, value)
}

// nilableInt64 把 *int64 转成 map[string]any 的值：nil 保持 nil（JSON null），
// 而不是解引用成 0——手续费/净现金流"未知"与"确认过是零"必须能被区分
// （规格 §12）。
func nilableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// maskUserRef 把订单的用户标识打码成展示用引用。
//
// 优先用打码邮箱（与 connectors/platformusers.MaskEmail 同一套口径，
// 本函数是独立实现——两个连接器包之间不互相 import，见 Snapshot 不共享的
// 同一条理由：contract.go 顶部关于 Snapshot 的说明）；邮箱为空或解析不出时
// 退到 "u<user_id>"，绝不透传明文邮箱（宪法 7 条）。
func maskUserRef(email string, userID int64) string {
	if masked := maskEmail(email); masked != "" && masked != unparsedEmailPlaceholder {
		return masked
	}
	return "u" + strconv.FormatInt(userID, 10)
}
