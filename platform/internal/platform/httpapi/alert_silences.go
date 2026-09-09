package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// SilenceLister 是静默窗口的只读查询能力（*alerts.Store 满足）。
//
// 与 AlertLister 分开而不是往它上面加两个方法：那个接口今天还被
// finance_runway_preview 用着，把静默塞进去会让一个只关心告警的调用方
// 平白背上两个它永远不调的方法。
type SilenceLister interface {
	ListActiveSilences(ctx context.Context, environment string, at time.Time) ([]alerts.Silence, error)
	ListSilences(ctx context.Context, environment string, limit int32) ([]alerts.Silence, error)
}

// state 查询参数的两个取值。
//
// 只有这两个，不提供 state=expired：过滤要么在 SQL 里（active 走的那条），
// 要么就没有。「先按 limit 截断再在 Go 里挑出已过期的」会让一页全是生效中
// 的窗口时返回空列表，而调用方读到的是「没有已过期的静默」——那是假话。
const (
	silenceStateActive = "active"
	silenceStateAll    = "all"
)

// 响应里 state 字段的另外两个取值（active 与查询参数共用一个词，同义）。
//
// 没有「已取消」：平台今天**没有**撤销静默的能力（alerts/actions.go 只声明了
// alerts.silence.create，库里也没有 delete/cancel 查询）。窗口只会自己到期。
// 编一个永远不出现的状态，会让运营以为「取消」这件事已经做得到。
const (
	silenceStateScheduled = "scheduled"
	silenceStateExpired   = "expired"
)

// defaultSilenceLimit 是不传 limit 时返回的条数。
//
// 与 defaultAlertLimit 同值但**各写一个**：两者是两份列表的页大小，
// 共用一个常量的话，将来调整告警页大小会连带改掉静默页，而改的人
// 不会知道自己动了第二个页面。
const defaultSilenceLimit int32 = 200

// silenceItem 是 GET /api/v1/alerts/silences 的一条记录。
type silenceItem struct {
	ID string `json:"id"`
	// RuleKey 为空串表示全局窗口（该环境下所有规则）。
	// 保持空串而不是换个哨兵值：领域模型里它就是空串，翻译留给前端。
	RuleKey     string `json:"rule_key"`
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
	StartsAt    string `json:"starts_at"`
	EndsAt      string `json:"ends_at"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`

	// State 由服务端按 as_of 算，不让前端自己比时间。
	//
	// 判据（左闭右开）在 alerts.Silence.Active 一处，投递侧压制告警用的是
	// 同一个方法。前端拿自己的时钟再比一遍的话，一台快五分钟的机器会把
	// 刚过期的窗口显示成「生效中」，而运营据此以为告警还压着。
	State string `json:"state"`
}

func silenceToItem(s alerts.Silence, at time.Time) silenceItem {
	return silenceItem{
		ID:          s.ID.String(),
		RuleKey:     s.RuleKey,
		Environment: s.Environment,
		Reason:      s.Reason,
		StartsAt:    s.StartsAt.UTC().Format(time.RFC3339),
		EndsAt:      s.EndsAt.UTC().Format(time.RFC3339),
		CreatedBy:   s.CreatedBy,
		CreatedAt:   s.CreatedAt.UTC().Format(time.RFC3339),
		State:       silenceStateAt(s, at),
	}
}

// silenceStateAt 把一个窗口归到三态之一。
func silenceStateAt(s alerts.Silence, at time.Time) string {
	if s.Active(at) {
		return silenceStateActive
	}
	// 「还没开始」与「已经结束」都不是生效中，但对运营是两件事：前者要等，
	// 后者要重建。压成一个「不生效」会让人对着一个五分钟后才生效的窗口
	// 反复重建。
	if at.UTC().Before(s.StartsAt.UTC()) {
		return silenceStateScheduled
	}
	return silenceStateExpired
}

// ListSilencesHandler 列出某环境下的静默窗口。
//
// 存在的理由：静默此前**只能建不能看**。运营按得下去，却回答不了
// 「现在有哪些静默生效中、是谁按的、什么时候到期」——而那正是静默唯一
// 危险的地方（一个足够宽的窗口等于临时关掉整套告警，见 alerts.ScopeSilenceManage
// 的注释）。看不见的静默窗口比看得见的更危险。
//
// 权限用 alerts.ScopeRead（= ops.read），与 GET /alerts 同一个：窗口正文是
// rule_key + 理由 + 按的人 + 起止时间，泄漏面比告警正文（含余额、收入）
// 还小，多开一个 scope 只是多一个会漏授的授权面。**写路径不在这里**——
// 建窗口仍是 L1 Action，权限是 alerts.silence.manage，两者刻意分开。
//
// 环境范围由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致
// （规格 §20.5）。
func ListSilencesHandler(store SilenceLister, now func() time.Time) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
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
		limit, err := parseListLimit(r.URL.Query().Get("limit"), defaultSilenceLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		state, err := parseSilenceState(r.URL.Query().Get("state"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		// 时刻取一次并随响应回去（as_of）：分类结果只在这一刻成立，
		// 而列表在浏览器里会挂很久。不说清算的是哪一刻，「生效中」就成了
		// 一句没有时效的断言。
		at := now().UTC()

		var items []alerts.Silence
		if state == silenceStateAll {
			items, err = store.ListSilences(r.Context(), string(env), limit)
		} else {
			// 默认只给生效中的：这一页要回答的是「现在什么在压着告警」。
			// 走 ListActiveSilences 而不是「全取回来再在 Go 里筛」，是因为
			// 后者会先被 limit 截断——一屏历史窗口就能把生效中的挤掉，
			// 而页面会显示「当前没有静默」。
			items, err = store.ListActiveSilences(r.Context(), string(env), at)
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}

		effectiveLimit := alerts.ClampListLimit(limit)
		// ListActiveSilences 的 SQL 没有 LIMIT（窗口数量本该是个位数），
		// 所以 limit 在这条路径上由这里兑现。all 那条已经在仓储里钳过，
		// 这一刀切不到东西。
		if int32(len(items)) > effectiveLimit {
			items = items[:effectiveLimit]
		}

		out := make([]silenceItem, 0, len(items))
		for _, s := range items {
			out = append(out, silenceToItem(s, at))
		}
		// truncated 与 /alerts 同一套语义与同一条理由：只有服务端知道生效
		// 的 limit，也只能说「可能」（正好取满时也可能就是恰好这么多）。
		WriteJSON(w, http.StatusOK, map[string]any{
			"items":     out,
			"limit":     effectiveLimit,
			"truncated": int32(len(out)) >= effectiveLimit,
			"as_of":     at.Format(time.RFC3339),
		})
	}
}

// parseSilenceState 解析 state 查询参数。
//
// 拼错的值当场 400，不回落到默认：一个 state=expired（本端点不支持）如果被
// 当成默认的 active，调用方会拿到一份「生效中」的列表却以为是已过期的，
// 而两者的条目看起来一模一样——没有任何线索能让人发现这件事。
func parseSilenceState(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return silenceStateActive, nil
	}
	if strings.EqualFold(raw, silenceStateActive) {
		return silenceStateActive, nil
	}
	if strings.EqualFold(raw, silenceStateAll) {
		return silenceStateAll, nil
	}
	return "", action.NewError(action.CodeInvalidParams,
		"state 必须是 active（默认，只看此刻生效的）或 all（含未开始与已过期）", nil)
}
