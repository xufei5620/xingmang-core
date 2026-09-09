package newapi

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// payments.read.v1（XM-PAY0）：逐笔订单读取 + 按日按状态汇总。
//
// 与既有 DailyOrders/OrderSummary（XM-0038，走 fetchRechargeDay 的按业务日
// 聚合）是**两条独立口径**：那条只认 status=success 且按 complete_time 归日
// （"业务日看的是到账时刻"，见 upstream.go fetchRechargeDay 顶部注释）；
// 本文件覆盖全部状态，且统一按 **create_time（下单时刻）** 归日——刻意
// 不跟 fetchRechargeDay 一致，理由：
//
//  1. pending/failed/expired 订单根本没有有意义的 complete_time（未结算），
//     只能按 create_time 归类；
//  2. 逐笔明细列表让用户按"这天下的单"筛选比按"这天到账的单"更符合直觉
//     （对照 sub2api 的 admin 后台本身也是按 created_at 排序展示）；
//  3. 用同一个字段（create_time）既当排序键又当窗口键，翻页早停判据
//     可以不留 fetchRechargeDay 那样的回溯余量（topupScanMarginDays）——
//     上游列表按 id desc 排，而 id 是插入时刻分配的，与 create_time 天然
//     同步，不会像 complete_time 那样在结算延迟后回填导致乱序。
//
// 两条口径的数字因此不保证互相对得上（例如今天下单、明天才 complete 的订单：
// DailyOrders 会算进明天，本文件的 succeeded 桶算进今天），这是已知且刻意的
// 差异，非一致性缺陷。

// Order 是一笔充值订单的逐笔记录。
//
// 不嵌 Snapshot，理由同 sub2api：一整页订单来自同一次读取，新鲜度由
// OrderPage.Snapshot 统一表达。
type Order struct {
	// OrderID 是上游 TopUp 记录的数据库主键（model.TopUp.Id），字符串化。
	OrderID string
	// CreatedAt 是下单时刻（上游 create_time，Unix 秒），UTC。
	CreatedAt time.Time
	// Status 是上游原始状态字面量（pending/success/failed/expired），
	// **不做归一化**——逐笔明细必须能核对回上游后台看到的原始状态。
	Status string
	// AmountMinorUnits 是这笔充值折算成连接器配置币种（默认 USD）后的整数
	// 最小货币单位。换算复用既有的 topupUSDQuota（按 payment_provider 分桶
	// 的 Amount/Money 语义）+ quotaPerUnit，与 fetchRechargeDay 同一套算法，
	// 只是不再筛 status=success、不再筛 complete_time——任何状态、任何时刻
	// 的订单都按同一套规则折算，见 upstream.go fetchOrders 的注释。
	//
	// provider=balance（内部余额划转）固定报 0，不代表这笔记录"值 0"，
	// 而是"这不是外部资金流入，不重新解释 Amount/Money 语义"——沿用
	// topupUSDQuota 对 topupInternal 的既有处理（该函数本身就不计算这类
	// 订单的金额）。provider 未识别的订单同样报 0，并把整页/整日汇总标记
	// 为部分数据（IsPartial），不猜金额。
	AmountMinorUnits int64
	// Currency 恒为连接器配置币种：NewAPI 内部只有 quota 一种单位，
	// 折算美元的唯一口径是 quota/quota_per_unit（controller/billing.go），
	// 不像 sub2api 那样存在逐行不同的真实币种。
	Currency string
	// Method 是支付提供方（stripe/creem/waffo/waffo_pancake/balance/epay），
	// 取自上游 payment_provider——与折算金额用的字段是同一个，两者不会对
	// 不上（详见本文件顶部关于不用 payment_method 的说明）。
	//
	// 命名避开"channel"的理由与 sub2api 相同：本连接器包里的"渠道"
	// （ChannelStatus）指上游计费路由渠道，是完全不同的概念。
	Method string
	// UserRef 是用户引用。NewAPI 的充值列表**不返回邮箱/用户名**
	// （model.TopUp 只有 UserId），因此没有可打码的邮箱——直接给
	// "u<user_id>"，数字 ID 本身不是可识别 PII。
	UserRef string
	// UpstreamOrderRef 是上游自己生成的业务订单号（trade_no），
	// 供人工核对时在上游后台按号查找。
	UpstreamOrderRef string
	// FeeMinorUnits 与 RefundAmountMinorUnits 恒为 nil（XM-PAY1 新增字段，
	// 与 connectors/sub2api.Order 对称，供 httpapi 层统一投影两个平台）：
	// model.TopUp 既没有第二个金额字段可供相减出手续费，也没有任何退款
	// 字段/状态/函数（controller/topup.go、model/topup.go 全文核对，见本文件
	// 顶部 DailyPaymentSummary.ByStatus 的同款说明）。nil 不是"这两个字段
	// 恰好是 0"，是"这个上游压根没有这两个概念"——与 sub2api 侧
	// RefundAmountMinorUnits 对每笔订单都给出真实数字（哪怕是 0）不是同一件
	// 事，调用方不能把两边的 nil 与非 nil 混着比较着看。
	FeeMinorUnits          *int64
	RefundAmountMinorUnits *int64
}

// OrderStats 是若干订单的笔数与金额合计。
type OrderStats struct {
	Count            int64
	AmountMinorUnits int64
}

// OrderFilter 圈定 ListOrders 的查询窗口。
type OrderFilter struct {
	// From、To 是**下单时刻**的闭区间（UTC），两者必须非零——理由同 sub2api：
	// 上游列表没有服务端日期过滤，无界查询没有一个调用方能预知的截断点。
	From, To time.Time
	// Status 非空时按上游原始状态过滤；空则返回窗口内全部状态。
	// 上游列表端点本身不支持服务端 status 过滤（只有 keyword），
	// 所以这里恒为客户端过滤，见 upstream.go fetchOrders。
	Status string
}

// OrderPage 是 ListOrders 的返回值，形状与 sub2api 对齐（同一份契约文档）。
type OrderPage struct {
	Snapshot
	Items         []Order
	StatsByStatus map[string]OrderStats
}

// DailyPaymentSummary 是某业务日按归一化状态分桶的资金汇总。
type DailyPaymentSummary struct {
	Snapshot
	Day      string
	Currency string
	// ByStatus 上游没有的桶不出现——NewAPI 没有退款概念（model/topup.go
	// 全文核对：不存在任何 refund 字段、状态值或函数），refunded 桶因此
	// **永远不出现**，不是"今天没有退款"，而是"这个上游不支持退款查询"。
	ByStatus map[string]StatusAmount
	// FeeMinorUnits、NetMinorUnits 恒为 nil：NewAPI 的 TopUp 模型里没有任何
	// 手续费或净现金流字段（controller/topup.go、model/topup.go 全文核对），
	// 不像 sub2api 那样能从 pay_amount-amount 推出手续费——这里没有第二个
	// 金额字段可供相减，因此老实报未知，不猜（见任务约束"上游没有就标注
	// 未知，不冒充 0"）。
	FeeMinorUnits *int64
	NetMinorUnits *int64
}

// StatusAmount 是某个归一化分桶的笔数与金额。
type StatusAmount struct {
	Count            int64
	AmountMinorUnits int64
}

// 归一化资金状态分桶，与 connectors/sub2api 使用相同的四个键名（同一份
// 前端资金概览卡消费两边的数据，键名必须一致）。
const (
	PaymentStatusSucceeded = "succeeded"
	PaymentStatusPending   = "pending"
	PaymentStatusFailed    = "failed"
	PaymentStatusRefunded  = "refunded"
)

// KnownOrderStatuses 是上游 TopUp.status 的全部合法取值，逐字抄自
// K:/newapi-src common/constants.go 的 TopUpStatus* 常量（只读核对，
// 未修改上游源码）。
var KnownOrderStatuses = []string{"pending", "success", "failed", "expired"}

// KnownOrderStatus 报告 status 是否是 KnownOrderStatuses 之一（大小写不敏感）。
func KnownOrderStatus(status string) bool {
	want := strings.ToLower(strings.TrimSpace(status))
	for _, s := range KnownOrderStatuses {
		if s == want {
			return true
		}
	}
	return false
}

// paymentStatusBucket 把上游原始状态归一化成资金概览卡的分桶。
//
// NewAPI 没有退款概念（见 DailyPaymentSummary.ByStatus 的注释），所以这里
// **不可能**返回 PaymentStatusRefunded——四态里只用得上三态，第四态由
// ByStatus 的"键不出现"规则表达"这个上游没有这类数据"，不是本函数漏判。
func paymentStatusBucket(rawStatus string) (bucket string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(rawStatus)) {
	case "success":
		return PaymentStatusSucceeded, true
	case "pending":
		return PaymentStatusPending, true
	case "failed", "expired":
		return PaymentStatusFailed, true
	default:
		return "", false
	}
}

// PaymentsReadClient 是 ReadClientV2 之上叠加的 payments.read.v1 切片
// （XM-PAY0），与 connectors/sub2api.PaymentsReadClient 同一份独立版本化契约
// （contracts/connectors/payments.read.v1.md），理由见该接口在 sub2api 包里
// 的注释。
type PaymentsReadClient interface {
	ReadClientV2

	// ListOrders 读取 [filter.From, filter.To] 闭区间内（按下单时刻）、
	// 可选按 filter.Status 过滤的全部订单，及其按原始状态的统计。
	ListOrders(ctx context.Context, filter OrderFilter) (OrderPage, error)

	// DailyPaymentSummary 读取某业务日（2006-01-02，按连接器声明的业务日
	// 时区）按归一化状态分桶的资金汇总。
	DailyPaymentSummary(ctx context.Context, day string) (DailyPaymentSummary, error)
}

// MetricPaymentsDaily 是写进 ops.metric_observation 的指标键。
const MetricPaymentsDaily = "newapi.payments.daily"

// ToPaymentsDailyObservation 把 DailyPaymentSummary 转成新鲜度模型，接进看板。
// 独立于 ToObservations，理由与调用方式见 connectors/sub2api 的同名函数。
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
// 不解引用成 0——手续费/净现金流"未知"与"确认过是零"必须能被区分（规格 §12）。
func nilableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// userRef 把 TopUp 的数字用户 ID 格式化成展示引用。
//
// 上游充值列表不返回邮箱/用户名，没有可打码的字段——不像 sub2api 需要
// maskEmail，这里直接给 "u<user_id>"：裸数字 ID 不是可识别 PII。
func userRef(userID int64) string {
	return "u" + strconv.FormatInt(userID, 10)
}
