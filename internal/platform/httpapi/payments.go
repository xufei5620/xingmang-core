package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// PlatformOrdersQuerier 是"支付与财务"逐笔订单查询的只读能力（XM-PAY0）。
//
// 只有一个方法而不是像 PlatformUsersQuerier 那样再拆出详情/日用量/Key
// 三个可选能力：payments.read.v1 目前只有一片（逐笔订单 + 按日按状态汇总，
// 后者走 /api/v1/metrics 现有通道，不需要专门的 Querier 方法）。
type PlatformOrdersQuerier interface {
	ListOrders(ctx context.Context, in PlatformOrdersInput) (PlatformOrdersResult, error)
}

// PlatformOrdersInput 是查询入参。
//
// Environment 显式携带而不是像 dynamicUsersClient 那样在构造期固定死：
// 与 ListPlatformChannelsHandler 同一条纪律——环境取自 Principal
// （resolveEnvironment），不取进程启动时的固定配置，为将来一个进程服务
// 多环境的形态留出空间，也让"跨环境读取"这条红线在这里也生效。
type PlatformOrdersInput struct {
	Platform    string
	Environment string
	From, To    time.Time
	// Status 非空时按上游原始状态过滤；空则不过滤。
	Status string
}

// PlatformOrdersResult 是查询结果：窗口内匹配的全部订单（未做 cursor/limit
// 截断，那是本文件 ListPlatformOrdersHandler 自己做的展示分页，与
// ListPlatformChannelsHandler 同一套路——Querier 给全量候选，处理器切页）。
type PlatformOrdersResult struct {
	Items         []PlatformOrderItem
	StatsByStatus map[string]PlatformOrderStat
	// Currency 是 StatsByStatus 里金额的合约币种（连接器配置）。
	Currency string
	// Source 是产出这批数据的实例标识（sub2api-staging 之类），供前端
	// 判断"这是不是演示数据"——与 platformUserPage.DataSource 同一条纪律。
	Source     string
	ObservedAt time.Time
	Watermark  string
	IsPartial  bool
}

// PlatformOrderItem 是一笔订单的查询投影，字段名与两个连接器的 Order 类型
// 逐一对应（见 connectors/sub2api.Order、connectors/newapi.Order）。
type PlatformOrderItem struct {
	OrderID          string
	CreatedAt        time.Time
	Status           string
	AmountMinorUnits int64
	Currency         string
	Method           string
	UserRef          string
	UpstreamOrderRef string
}

// PlatformOrderStat 是某个原始状态的笔数与金额合计（只在 Currency 对应的
// 币种内求和，见连接器 OrderStats 的注释）。
type PlatformOrderStat struct {
	Count            int64
	AmountMinorUnits int64
}

// platformOrderItemBody 是逐笔订单的对外表示。
//
// 金额走 amountBody（十进制字符串，见 users.go 顶部注释）：JSON number 在
// JavaScript 里是 float64，订单金额一旦超过 2^53 会静默丢精度（宪法 13 条）。
type platformOrderItemBody struct {
	OrderID          string     `json:"order_id"`
	CreatedAt        string     `json:"created_at"`
	Status           string     `json:"status"`
	Amount           amountBody `json:"amount"`
	Method           string     `json:"method"`
	UserRef          string     `json:"user_ref"`
	UpstreamOrderRef string     `json:"upstream_order_ref"`
}

func toPlatformOrderItemBody(item PlatformOrderItem) platformOrderItemBody {
	return platformOrderItemBody{
		OrderID:   item.OrderID,
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339),
		Status:    item.Status,
		Amount: amountBody{
			MinorUnits: amountString(item.AmountMinorUnits),
			Currency:   item.Currency,
		},
		Method:           item.Method,
		UserRef:          item.UserRef,
		UpstreamOrderRef: item.UpstreamOrderRef,
	}
}

// platformOrderStatBody 是某个原始状态的笔数与金额合计的对外表示。
type platformOrderStatBody struct {
	Count  int64      `json:"count"`
	Amount amountBody `json:"amount"`
}

// platformOrdersPage 是逐笔订单查询的响应体。
type platformOrdersPage struct {
	Items []platformOrderItemBody `json:"items"`
	// NextCursor 为空串表示已经翻到底（与 platformUserPage 同一条约定）。
	NextCursor string `json:"next_cursor"`
	// StatsByStatus 的键是上游原始状态字面量（PAID/PENDING/success/…），
	// **不做归一化**——归一化分桶（succeeded/pending/failed/refunded）走
	// /api/v1/metrics 的 sub2api.payments.daily / newapi.payments.daily，
	// 这条端点给的是可核对的原始状态，两个通道的口径刻意不同，见
	// contracts/connectors/payments.read.v1.md。
	StatsByStatus map[string]platformOrderStatBody `json:"stats_by_status"`
	From          string                           `json:"from"`
	To            string                           `json:"to"`
	DataSource    string                           `json:"data_source"`
	Freshness     freshnessBody                    `json:"freshness"`
}

// platformOrdersStalenessThresholdSeconds：与用户清单同一条理由——这是一次
// **实时读取**，阈值守的不是上游的采集周期，而是我们自己这条链路。
const platformOrdersStalenessThresholdSeconds int32 = 60

func platformOrdersFreshness(observedAt time.Time, watermark string, isPartial bool, now time.Time) freshnessBody {
	staleness := int64(now.Sub(observedAt).Seconds())
	if staleness < 0 {
		staleness = 0
	}
	body := freshnessBody{
		StalenessSeconds: &staleness,
		ThresholdSeconds: platformOrdersStalenessThresholdSeconds,
		ObservedAt:       rfc3339Ptr(&observedAt),
		LastSuccess:      rfc3339Ptr(&observedAt),
		IsPartial:        isPartial,
	}
	switch {
	case staleness >= int64(platformOrdersStalenessThresholdSeconds):
		body.State = "stale"
	case isPartial:
		body.State = "partial"
	default:
		body.State = "fresh"
	}
	return body
}

const platformOrdersDateLayout = "2006-01-02"

// parseOrdersWindow 把 day（单日简写）或 from/to（区间）解析成 [from,to] 闭区间
// UTC 时刻。day 与 from/to 互斥；都不给时默认当前 UTC 业务日——与
// parseBusinessDayRange（platform_channels.go）的"都不传=今天"同一条约定，
// 但这里多一个 day 简写，因为逐笔订单最常见的用法就是看"今天"或"某一天"。
func parseOrdersWindow(day, rawFrom, rawTo string) (from, to time.Time, err error) {
	day = strings.TrimSpace(day)
	rawFrom = strings.TrimSpace(rawFrom)
	rawTo = strings.TrimSpace(rawTo)

	if day != "" {
		if rawFrom != "" || rawTo != "" {
			return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
				"day 不能与 from/to 同时提供", nil)
		}
		parsed, perr := time.Parse(platformOrdersDateLayout, day)
		if perr != nil {
			return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
				"day 必须是 YYYY-MM-DD", perr)
		}
		from = parsed.UTC()
		to = from.AddDate(0, 0, 1).Add(-time.Nanosecond)
		return from, to, nil
	}

	fromDay, toDay, perr := parseBusinessDayRange(rawFrom, rawTo)
	if perr != nil {
		return time.Time{}, time.Time{}, perr
	}
	from, _ = time.Parse(platformOrdersDateLayout, fromDay)
	toParsed, _ := time.Parse(platformOrdersDateLayout, toDay)
	from = from.UTC()
	to = toParsed.UTC().AddDate(0, 0, 1).Add(-time.Nanosecond)
	return from, to, nil
}

// ListPlatformOrdersHandler 列出某平台在给定窗口内的逐笔订单（XM-PAY0）。
//
// 环境范围：环境取自 Principal（resolveEnvironment），不接受跨环境读取
// ——与用户清单、渠道目录同一档处理，理由同样是"这批数据逐笔可还原一家
// 客户的经营规模"（见 platformusers.ScopeRead 的注释）。
//
// 权限：finance.read，由路由上的 RequireScope 判定。
func ListPlatformOrdersHandler(q PlatformOrdersQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		platform := chi.URLParam(r, "platform")
		if platform != "sub2api" && platform != "newapi" {
			WriteError(w, r, action.NewError(action.CodeNotRegistered, "平台不支持订单查询", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		from, to, err := parseOrdersWindow(
			r.URL.Query().Get("day"), r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseBindingLimit(r.URL.Query().Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		cursor, err := decodeBindingCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		result, err := q.ListOrders(r.Context(), PlatformOrdersInput{
			Platform:    platform,
			Environment: string(env),
			From:        from,
			To:          to,
			Status:      strings.TrimSpace(r.URL.Query().Get("status")),
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}

		// 展示分页：Querier 给出窗口内的全量候选（已按 CreatedAt 降序），
		// 这里按**偏移量**切页，而不是像 ListPlatformChannelsHandler 那样按
		// 业务字段值切：订单 ID 是数字，字符串化后按字典序比较不等价于
		// 按数值比较（"9" > "10" 的字典序判断是错的），拿它当游标比较值会在
		// 位数跨界时悄悄漏页或重复——这个坑已经在契约测试里现形过一次。
		// 偏移量游标的已知代价是"翻页途中若上游插入了更新的订单，后续页可能
		// 错位"，对一个有限窗口内的管理端浏览场景可以接受。
		offset := 0
		if cursor != "" {
			n, perr := strconv.Atoi(cursor)
			if perr != nil || n < 0 {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "cursor 无效", perr))
				return
			}
			offset = n
		}
		items := result.Items
		if offset > len(items) {
			offset = len(items)
		}
		items = items[offset:]
		var nextCursor string
		if len(items) > limit {
			nextCursor = encodeBindingCursor(strconv.Itoa(offset + limit))
			items = items[:limit]
		}

		body := make([]platformOrderItemBody, 0, len(items))
		for _, item := range items {
			body = append(body, toPlatformOrderItemBody(item))
		}
		stats := make(map[string]platformOrderStatBody, len(result.StatsByStatus))
		for status, stat := range result.StatsByStatus {
			stats[status] = platformOrderStatBody{
				Count: stat.Count,
				Amount: amountBody{
					MinorUnits: amountString(stat.AmountMinorUnits),
					Currency:   result.Currency,
				},
			}
		}

		WriteJSON(w, http.StatusOK, platformOrdersPage{
			Items:         body,
			NextCursor:    nextCursor,
			StatsByStatus: stats,
			From:          from.UTC().Format(platformOrdersDateLayout),
			To:            to.UTC().Format(platformOrdersDateLayout),
			DataSource:    result.Source,
			Freshness:     platformOrdersFreshness(result.ObservedAt, result.Watermark, result.IsPartial, time.Now().UTC()),
		})
	}
}

// amountString 把整数最小货币单位格式化成 amountBody 要的十进制字符串指针。
func amountString(minorUnits int64) *string {
	s := strconv.FormatInt(minorUnits, 10)
	return &s
}
