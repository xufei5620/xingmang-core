package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	// defaultProfitWindowDays 是不传日期时的窗口（含今天在内的 7 个业务日）。
	//
	// 7 天对齐设计稿 §10.4 的「近 7 日」口径——看板上问「这条渠道最近怎么样」
	// 时，日均消耗与毛利趋势看的是同一个窗口，两处用不同的默认值会让
	// 两张卡片各自说一套。
	defaultProfitWindowDays = 6
	// maxProfitWindowDays 是窗口上限（92 天 ≈ 一个季度）。
	//
	// 上限的意义不是省 CPU，而是不让这个端点被当成数据导出口
	// （同 maxHistoryHours）。更长跨度的分析要的是按月聚合，那是另一个东西。
	maxProfitWindowDays = 92
)

// ProfitDailyLister 是利润台账的只读查询能力（*finance.ProfitStore 满足）。
//
// 第二个返回值是 truncated：区间内还有更多行没返回。它在签名里而不是藏进
// 结构体，是为了让每个实现与调用方都必须处理它——被悄悄截断的区间会被读成
// 「这几天真的没有数据」（宪法 12 条，同 MetricHistoryLister 的理由）。
type ProfitDailyLister interface {
	ListRows(ctx context.Context, q finance.ProfitQuery) ([]finance.ProfitRow, bool, error)
}

// moneyItem 是 UI 交接 §13 的 `Money` 形状。
//
// AmountMinor 是**字符串**而不是 JSON 数字：金额是 int64 @ scale-6 微单位，
// 超过 2^53 就会在前端的 IEEE-754 double 里丢精度，而丢精度的那一位不会报错
// （§2.4、§13、宪法 13 条）。字符串同时让「未知」有地方表达——见下面的指针。
type moneyItem struct {
	AmountMinor string `json:"amount_minor"`
	Currency    string `json:"currency"`
	// Scale 说明 AmountMinor 是几位小数的定点数。带上它，前端就不必把 6
	// 硬编码在某个格式化函数里——那个 6 一旦与后端漂开，所有金额差 100 倍
	// 且看起来完全正常。
	Scale int `json:"scale"`
}

// profitDailyItem 是台账一行的对外形状。
//
// 三个金额都是**可空**的（`null` = 未知），这是本响应最重要的性质：
// §5.1 要求「未知」与「已知的 0」在台账里是两件事，那个区分必须一路传到前端。
// 把未知渲染成 `{"amount_minor":"0"}` 会让页面显示一个笃定的 $0.00，
// 而真相是我们那天没读到数（宪法 12 条）。
type profitDailyItem struct {
	UpstreamAccountID string `json:"upstream_account_id"`
	// BusinessDay 是业务日（YYYY-MM-DD），按 BusinessDayTZ 切分。
	//
	// **不是 RFC3339 时刻**：它是一个日历日，渲染成带时区的时间戳会让前端
	// 按浏览器时区再解释一次，跨零点的用户看到的就是前一天（宪法 14 条）。
	BusinessDay string `json:"business_day"`
	// BusinessDayTZ 是这一行业务日的切日偏移，逐行冻结。
	// 前端解释 business_day 必须按它，而不是按浏览器时区。
	BusinessDayTZ string `json:"business_day_tz"`

	// TokenID 是成本侧键，AccountID 是收入侧键（§2.2）。
	TokenID   string `json:"token_id"`
	AccountID string `json:"account_id"`
	// PlatformID 为空串表示写入当时未配对（§5.2 的「未归属」桶）。
	PlatformID string `json:"platform_id"`

	UsageRevenue *moneyItem `json:"usage_revenue"`
	SupplyCost   *moneyItem `json:"supply_cost"`
	// GrossProfit = 收入 − 成本；任一侧未知则为 null，不是「等于另一侧」。
	GrossProfit *moneyItem `json:"gross_profit"`

	// RatioSnapshot 是本行成本折算**实际用的**倍率，逐行冻结（§6.3）。
	//
	// 定点十进制**字符串**而不是 JSON 数字：1.15 一路解成 double 会变成
	// 1.1499999999999999（宪法 13 条：比例用 Decimal）。它在这里的作用是
	// 让「这条渠道成本怎么涨了」的第一个追问——「倍率是不是被改了」——
	// 就地答得出。
	RatioSnapshot string `json:"ratio_snapshot"`

	// 数据来源与新鲜度（宪法 12 条：禁止裸数字）。
	//
	// 两侧**各有一个观测时刻**：它们读的是不同上游、在不同时刻、可以各自失败。
	// 合成一个的话，一次新鲜的收入读取就会替一份陈旧的成本读数背书。
	Source            string  `json:"source"`
	CostObservedAt    *string `json:"cost_observed_at"`
	RevenueObservedAt *string `json:"revenue_observed_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// profitDailyPage 是一次台账查询的完整响应。
type profitDailyPage struct {
	Items []profitDailyItem `json:"items"`
	// From / To 回显实际生效的业务日区间（含两端）。
	//
	// 回显而不是让调用方自己记：不传参数时区间是服务端算的，
	// 前端要在图表轴上标出「这是哪几天」，只能从这里拿。
	From string `json:"from"`
	To   string `json:"to"`
	// Truncated = true 表示区间内还有更多行，但没有返回。
	Truncated bool `json:"truncated"`
	// Limit 是本次实际生效的条数上限，让 truncated 可解释。
	Limit int32 `json:"limit"`
}

func moneyOf(minor *int64, currency string) *moneyItem {
	if minor == nil {
		// null 而不是 0：未知与已知的零必须在响应里也是两件事（§5.1）。
		return nil
	}
	return &moneyItem{
		AmountMinor: strconv.FormatInt(*minor, 10),
		Currency:    currency,
		Scale:       financeAmountScale,
	}
}

// financeAmountScale 是台账金额的定点标度。
//
// 取自 money 包而不是写一个 6：两处各写一个字面量迟早会漂，
// 而漂了之后所有金额差 100 倍且不报错。
const financeAmountScale = money.MicroScale

func profitRowToItem(r finance.ProfitRow) profitDailyItem {
	return profitDailyItem{
		UpstreamAccountID: r.UpstreamAccountID.String(),
		BusinessDay:       r.BusinessDayString(),
		BusinessDayTZ:     r.BusinessDayTZ,
		TokenID:           r.TokenID,
		AccountID:         r.AccountID,
		PlatformID:        r.PlatformID,
		UsageRevenue:      moneyOf(r.RevenueMinor, r.Currency),
		SupplyCost:        moneyOf(r.CostMinor, r.Currency),
		GrossProfit:       moneyOf(r.ProfitMinor(), r.Currency),
		RatioSnapshot:     r.RatioSnapshot.String(),
		Source:            r.Source,
		CostObservedAt:    rfc3339Ptr(r.CostObservedAt),
		RevenueObservedAt: rfc3339Ptr(r.RevenueObservedAt),
		UpdatedAt:         r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// parseProfitWindow 解析 from / to 业务日参数。
//
// 两个都不传 → 最近 defaultProfitWindowDays+1 个业务日（含今天）。
// 只传一个 → 报错而不是替调用方补另一个：「from=2026-08-01」到底是想要
// 一天还是想要从那天到今天，猜错了就是一张跨度完全不同的图。
//
// 「今天」按 CST 固定 +08:00 算（★口径常量 §4），不按服务器本地时区：
// 台账的业务日就是按它切的，用别的时区算默认窗口会在跨零点那几个小时
// 少给或多给一天。
func parseProfitWindow(rawFrom, rawTo string, now time.Time) (from, to time.Time, err error) {
	rawFrom, rawTo = strings.TrimSpace(rawFrom), strings.TrimSpace(rawTo)
	switch {
	case rawFrom == "" && rawTo == "":
		today := finance.BusinessDayAt(now, finance.DefaultBusinessDayLocation())
		return today.AddDate(0, 0, -defaultProfitWindowDays), today, nil
	case rawFrom == "" || rawTo == "":
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"from 与 to 必须同时给出（都不给则取最近 "+
				strconv.Itoa(defaultProfitWindowDays+1)+" 个业务日）", nil)
	}

	from, err = finance.ParseBusinessDay(rawFrom)
	if err != nil {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"from 必须形如 YYYY-MM-DD", err)
	}
	to, err = finance.ParseBusinessDay(rawTo)
	if err != nil {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"to 必须形如 YYYY-MM-DD", err)
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"业务日区间起止颠倒", nil)
	}
	// +1 因为区间含两端（§12 拍板的「起止含两端」同一口径）
	if days := int(to.Sub(from).Hours()/24) + 1; days > maxProfitWindowDays {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"业务日跨度 "+strconv.Itoa(days)+" 天超出上限 "+
				strconv.Itoa(maxProfitWindowDays)+" 天", nil)
	}
	return from, to, nil
}

// ListProfitDailyHandler 返回某环境、某业务日区间的利润台账（设计稿 §8.5 的 Query 侧）。
//
// 环境范围由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致——
// **不默认生产，也不允许跨环境读取**（宪法 15 条）。
// 权限（finance.ScopeRead）由路由上的 RequireScope 判定，不在此处重复。
//
// **写路径不在这里**：台账只由采集任务写（§8.1），HTTP 上没有任何入口能改它。
// 回填历史更是 Platform Lifecycle Operation（宪法 2 条），不是一个 API。
func ListProfitDailyHandler(store ProfitDailyLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		query := r.URL.Query()
		from, to, err := parseProfitWindow(
			query.Get("from"), query.Get("to"), time.Now().UTC())
		if err != nil {
			WriteError(w, r, err)
			return
		}

		limit, err := parseProfitLimit(query.Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		rows, truncated, err := store.ListRows(r.Context(), finance.ProfitQuery{
			Environment: string(env),
			From:        from,
			To:          to,
			PlatformID:  strings.TrimSpace(query.Get("platform_id")),
			Limit:       limit,
		})
		if err != nil {
			// 形态非法的 platform_id 由领域层判（与写入路径同一条正则），
			// 归 400——它是调用方写错了参数，不是服务端故障。
			if errors.Is(err, finance.ErrInvalidFormat) {
				WriteError(w, r, action.NewError(action.CodeInvalidParams,
					"platform_id 形态非法", err))
				return
			}
			WriteError(w, r, err)
			return
		}

		out := make([]profitDailyItem, 0, len(rows))
		for _, row := range rows {
			out = append(out, profitRowToItem(row))
		}
		WriteJSON(w, http.StatusOK, profitDailyPage{
			Items:     out,
			From:      from.Format(finance.ProfitBusinessDayLayout),
			To:        to.Format(finance.ProfitBusinessDayLayout),
			Truncated: truncated,
			Limit:     limit,
		})
	}
}

// parseProfitLimit 解析 limit 参数。
//
// 非法值一律 400 而不是悄悄取默认：`limit=abc` 静默变成 500 会让前端拿着
// 一份自以为完整的数据（同 parseHistoryHours 的理由）。
func parseProfitLimit(raw string) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return finance.DefaultProfitListLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是整数", err)
	}
	if value <= 0 || int32(value) > finance.MaxProfitListLimit {
		return 0, action.NewError(action.CodeInvalidParams,
			"limit 必须在 1 与 "+strconv.Itoa(int(finance.MaxProfitListLimit))+" 之间", nil)
	}
	return int32(value), nil
}
