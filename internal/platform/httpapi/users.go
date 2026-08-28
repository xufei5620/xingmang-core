package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

// PlatformUsersQuerier 是终端用户清单的只读能力（*platformusers.Service 满足）。
type PlatformUsersQuerier interface {
	List(ctx context.Context, in platformusers.ListInput) (connusers.UserPage, error)
}

// amountBody 是一个**可能缺席**的金额的对外表示。
//
// `minor_units` 为 null 表示上游没给这个数——与 0 是相反的两件事。
// 前端据此显示「—」而不是「¥0.00」（宪法 12 条：禁止裸数字冒充完整数据；
// NewAPI 渠道表的「未配置余额」是同一条）。
//
// **金额是字符串**：JSON number 在 JavaScript 里是 float64，超过 2^53 的
// 最小单位金额会静默丢精度（宪法 13 条：金额禁止 float）。前端拿到字符串后
// 用 BigInt 解析，全程整数。
type amountBody struct {
	MinorUnits *string `json:"minor_units"`
	Currency   string  `json:"currency"`
}

func toAmountBody(a connusers.Amount) amountBody {
	if !a.Known {
		return amountBody{}
	}
	// 与 profit_daily / subscription 同一条约定：金额走十进制字符串
	s := strconv.FormatInt(a.MinorUnits, 10)
	return amountBody{MinorUnits: &s, Currency: a.Currency}
}

// platformUserItem 是一个终端用户的对外表示。
//
// **逐字段列出，不是连接器结构体的直接序列化**：响应体是契约，不是结构体的
// 倒影（规格 §18.4）。这一条在本端点上尤其要紧——契约层将来若为了排查方便
// 往 User 上加一个手机号字段，直接序列化会让它一路漏到前端，而这里加字段
// 必须是一次显式动作。
type platformUserItem struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	// EmailMasked 已在**连接器**侧打码（platformusers.MaskEmail），这里不做
	// 二次处理——二次处理会让「脱敏在哪一层做」变成两个答案。
	// 空串表示上游没记；`invalid-contact` 表示记了但形状我们不认识。
	EmailMasked    string     `json:"email_masked"`
	Status         string     `json:"status"`
	Balance        amountBody `json:"balance"`
	PeriodRecharge amountBody `json:"period_recharge"`
	PeriodConsumed amountBody `json:"period_consumed"`
	// Last30dConsumed 是近 30 天消费(滚动窗口,**不随所选区间变**)。
	Last30dConsumed amountBody `json:"last_30d_consumed"`
	// LastActiveAt 为 null 表示从未活跃——与「很久以前活跃过」不是一回事。
	LastActiveAt *string `json:"last_active_at"`
	TokenPrefix  string  `json:"token_prefix"`
}

// countBody 是一个可能缺席的计数。理由同 amountBody。
type countBody struct {
	Value *int64 `json:"value"`
}

// platformUserPage 是一页终端用户。
type platformUserPage struct {
	Items []platformUserItem `json:"items"`
	// NextCursor 为空串表示已经翻到底。
	NextCursor string `json:"next_cursor"`
	// TotalCount 的 value 为 null 表示上游只给了这一页，没给总数。
	// 编一个总数出来会让分页控件显示一个错的总页数。
	TotalCount countBody `json:"total_count"`
	// TotalBalance 是**全体**用户的余额合计（不只本页）。
	TotalBalance amountBody `json:"total_balance"`
	// ActiveToday 的 value 为 null 表示上游没给「今日活跃」。
	ActiveToday countBody `json:"active_today"`
	// PeriodTotals 是区间合计，**带覆盖率**。
	PeriodTotals totalsBody `json:"period_totals"`
	// Period 回显服务端实际用的区间，前端据此显示「2026-08-27 · 按日查看」。
	Period     periodBody `json:"period"`
	DataSource string     `json:"data_source"`
	// Freshness 与 /api/v1/metrics 同一个形状，前端复用同一个徽章组件。
	Freshness freshnessBody `json:"freshness"`
}

// totalsBody 是区间合计的对外表示。
//
// **带 covered_users / total_users 而不是两个裸金额**：合计只能把上游给得出
// 流水的那些用户加起来，而 v1 契约对一部分用户给不出。一个盖住这件事的合计
// 会被读成全量——那正是宪法 12 条要防的「裸数字冒充完整数据」。
// 两个数不等时，前端必须说出这个合计是**下界**。
type totalsBody struct {
	Recharge amountBody `json:"recharge"`
	Consumed amountBody `json:"consumed"`
	// CoveredUsers 是流水已知的用户数；TotalUsers 是符合筛选条件的用户数。
	CoveredUsers int64 `json:"covered_users"`
	TotalUsers   int64 `json:"total_users"`
	// Complete 是 covered == total 的便捷判定，避免前端各自比一遍比错。
	Complete bool `json:"complete"`
}

func toTotalsBody(t connusers.Totals) totalsBody {
	return totalsBody{
		Recharge:     toAmountBody(t.Recharge),
		Consumed:     toAmountBody(t.Consumed),
		CoveredUsers: t.CoveredUsers,
		TotalUsers:   t.TotalUsers,
		Complete:     t.Complete(),
	}
}

// periodBody 回显服务端实际用的统计区间。
//
// From / To 是**闭区间**业务日：粒度是「周」时，人要能看见到底是哪七天。
// 只回一个 granularity 的话，「本周」在跨月那几天最容易被理解错。
type periodBody struct {
	Day         string `json:"day"`
	Granularity string `json:"granularity"`
	From        string `json:"from"`
	To          string `json:"to"`
}

func toPeriodBody(p connusers.Period) periodBody {
	return periodBody{
		Day:         p.Day,
		Granularity: string(p.Granularity),
		From:        p.From,
		To:          p.To,
	}
}

// platformUsersStalenessThresholdSeconds：与请求详情同一条理由——
// 这是一次**实时读取**，阈值守的不是上游的采集周期，而是我们自己这条链路。
const platformUsersStalenessThresholdSeconds int32 = 60

func usersFreshness(snap connusers.Snapshot, now time.Time) freshnessBody {
	observedAt := snap.ObservedAt.UTC()
	staleness := int64(now.Sub(observedAt).Seconds())
	if staleness < 0 {
		// 上游时钟比我们快时会出现负数。夹到 0 而不是原样返回：
		// 一个「负 3 秒前的数据」会让人以为界面坏了
		staleness = 0
	}
	body := freshnessBody{
		StalenessSeconds: &staleness,
		ThresholdSeconds: platformUsersStalenessThresholdSeconds,
		ObservedAt:       rfc3339Ptr(&observedAt),
		LastSuccess:      rfc3339Ptr(&observedAt),
	}
	if staleness >= int64(platformUsersStalenessThresholdSeconds) {
		body.State = "stale"
	} else {
		body.State = "fresh"
	}
	return body
}

// ListPlatformUsersHandler 列出某平台的终端用户清单。
//
// 环境范围：**不接受 environment 查询参数**，一律用 Principal 的环境——
// 与请求详情、审计事件同一档处理。这批数据逐用户可还原一家客户的经营规模，
// 多一个可写的入参就多一个将来被放宽成「跨环境看板」的口子（规格 §20.5）。
//
// 权限：`platform.users.read`，由路由上的 RequireScope 判定。
func ListPlatformUsersHandler(q PlatformUsersQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, err := parseLimitParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		page, err := q.List(r.Context(), platformusers.ListInput{
			Platform: chi.URLParam(r, "platform"),
			Query:    strings.TrimSpace(r.URL.Query().Get("q")),
			Status:   strings.TrimSpace(r.URL.Query().Get("status")),
			Sort:     strings.TrimSpace(r.URL.Query().Get("sort")),
			// 统计区间：day 是锚点业务日，granularity 是日/周/月。
			// 都不传 = 今天 · 按日（与原型默认态一致）。
			Day:         strings.TrimSpace(r.URL.Query().Get("day")),
			Granularity: strings.TrimSpace(r.URL.Query().Get("granularity")),
			Limit:       limit,
			Cursor:      strings.TrimSpace(r.URL.Query().Get("cursor")),
		})

		if err != nil {
			WriteError(w, r, err)
			return
		}

		items := make([]platformUserItem, 0, len(page.Users))
		for _, u := range page.Users {
			items = append(items, toPlatformUserItem(u))
		}
		out := platformUserPage{
			Items:        items,
			NextCursor:   page.NextCursor,
			TotalBalance: toAmountBody(page.TotalBalance),
			PeriodTotals: toTotalsBody(page.PeriodTotals),
			Period:       toPeriodBody(page.Period),
			DataSource:   page.Snapshot.Source,
			Freshness:    usersFreshness(page.Snapshot, time.Now().UTC()),
		}
		if page.TotalCount.Known {
			v := page.TotalCount.Value
			out.TotalCount = countBody{Value: &v}
		}
		if page.ActiveToday.Known {
			v := page.ActiveToday.Value
			out.ActiveToday = countBody{Value: &v}
		}
		WriteJSON(w, http.StatusOK, out)
	}
}

func toPlatformUserItem(u connusers.User) platformUserItem {
	item := platformUserItem{
		ID:              u.ID,
		Username:        u.Username,
		EmailMasked:     u.EmailMasked,
		Status:          string(u.Status),
		Balance:         toAmountBody(u.Balance),
		PeriodRecharge:  toAmountBody(u.PeriodRecharge),
		PeriodConsumed:  toAmountBody(u.PeriodConsumed),
		Last30dConsumed: toAmountBody(u.Last30dConsumed),
		TokenPrefix:     u.TokenPrefix,
	}
	if !u.LastActiveAt.IsZero() {
		at := u.LastActiveAt.UTC()
		item.LastActiveAt = rfc3339Ptr(&at)
	}
	return item
}
