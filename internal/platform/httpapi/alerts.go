package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// AlertLister 是告警的只读查询能力（*alerts.Store 满足）。
type AlertLister interface {
	ListByStatus(ctx context.Context, environment string, statuses []alerts.Status, limit int32) ([]alerts.Alert, error)
	ListRecent(ctx context.Context, environment string, limit int32) ([]alerts.Alert, error)
}

// statusAll 是 status 查询参数的特殊值：返回**含已解决**的最近告警。
//
// 有这个值是必要的：默认只返回活跃告警（那是「现在要处理什么」），
// 但「这周炸过几次、都恢复了吗」同样是运营要问的问题，而已解决的告警
// 恰恰不在活跃集合里。用一个显式的关键字而不是「不传 status 就返回全部」——
// 默认值应该是最常用也最安全的那个。
const statusAll = "all"

// defaultAlertLimit 是不传 limit 时返回的条数。
const defaultAlertLimit int32 = 200

type alertItem struct {
	ID       string `json:"id"`
	RuleKey  string `json:"rule_key"`
	DedupKey string `json:"dedup_key"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`

	Environment     string `json:"environment"`
	SourceMetricKey string `json:"source_metric_key"`

	OpenedAt       string  `json:"opened_at"`
	LastSeenAt     string  `json:"last_seen_at"`
	AcknowledgedAt *string `json:"acknowledged_at"`
	ResolvedAt     *string `json:"resolved_at"`
	FireCount      int32   `json:"fire_count"`

	// 投递状态与告警状态一起返回，从不省略：一条 OPEN 却没投递出去的告警
	// 是本模块最危险的状态，前端必须能显示它（规格 §9.3「通知投递状态」）。
	NotifyStatus string  `json:"notify_status"`
	NotifyError  string  `json:"notify_error"`
	NotifiedAt   *string `json:"notified_at"`
}

func alertToItem(a alerts.Alert) alertItem {
	return alertItem{
		ID:              a.ID.String(),
		RuleKey:         a.RuleKey,
		DedupKey:        a.DedupKey,
		Severity:        string(a.Severity),
		Status:          string(a.Status),
		Title:           a.Title,
		Detail:          a.Detail,
		Environment:     a.Environment,
		SourceMetricKey: a.SourceMetricKey,
		OpenedAt:        a.OpenedAt.UTC().Format(time.RFC3339),
		LastSeenAt:      a.LastSeenAt.UTC().Format(time.RFC3339),
		AcknowledgedAt:  rfc3339Ptr(a.AcknowledgedAt),
		ResolvedAt:      rfc3339Ptr(a.ResolvedAt),
		FireCount:       a.FireCount,
		NotifyStatus:    string(a.NotifyStatus),
		NotifyError:     a.NotifyError,
		NotifiedAt:      rfc3339Ptr(a.NotifiedAt),
	}
}

// ListAlertsHandler 列出某环境下的告警。
//
// 环境范围由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致——
// 不默认生产，也不允许跨环境读取（规格 §20.5）。
// 权限（alerts.ScopeRead）由路由上的 RequireScope 判定。
func ListAlertsHandler(store AlertLister) http.HandlerFunc {
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
		limit, err := parseAlertLimit(r.URL.Query().Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		raw := strings.TrimSpace(r.URL.Query().Get("status"))
		var items []alerts.Alert
		if strings.EqualFold(raw, statusAll) {
			items, err = store.ListRecent(r.Context(), string(env), limit)
		} else {
			statuses, parseErr := parseAlertStatuses(raw)
			if parseErr != nil {
				WriteError(w, r, parseErr)
				return
			}
			items, err = store.ListByStatus(r.Context(), string(env), statuses, limit)
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}

		out := make([]alertItem, 0, len(items))
		for _, a := range items {
			out = append(out, alertToItem(a))
		}
		// truncated：返回条数正好等于**生效的**上限，说明可能还有没返回的。
		//
		// 为什么由服务端说：调用方传的 limit 与真正生效的 limit 可能不是一个数
		// （0 或超过 MaxListLimit 都会被钳），前端拿 len(items) 去比自己传的那个
		// 数，在被钳的情况下会**永远判不出截断**。只有这里知道生效值。
		//
		// 只能说"可能"：恰好等于上限时也可能就是恰好这么多。含糊不好，但让一份
		// 被截断的列表看起来像全部更糟——人会据此收工。
		effectiveLimit := alerts.ClampListLimit(limit)
		WriteJSON(w, http.StatusOK, map[string]any{
			"items":     out,
			"limit":     effectiveLimit,
			"truncated": int32(len(out)) >= effectiveLimit,
		})
	}
}

// parseAlertStatuses 解析 status 查询参数（逗号分隔）。
//
// 空值返回 nil，仓储会把它当成「全部活跃状态」——那是页面默认要看的东西。
// **拼错的状态当场 400**，不静默忽略：一个 status=Open（大小写错）如果被
// 忽略成「全部活跃」，调用方会拿到一份看起来对、其实没按他要求过滤的列表。
func parseAlertStatuses(raw string) ([]alerts.Status, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]alerts.Status, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		status, err := alerts.ParseStatus(trimmed)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams,
				"status 必须是 OPEN / ACKNOWLEDGED / SILENCED / RESOLVED / REOPENED 之一（逗号分隔），或 all",
				err)
		}
		out = append(out, status)
	}
	return out, nil
}

func parseAlertLimit(raw string) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultAlertLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是正整数", err)
	}
	// 上界由仓储钳制（alerts.MaxListLimit）。这里不重复那个常量：
	// 两处各写一个数字，改一处忘另一处时会出现「API 说最多 500，
	// 实际返回 200」这种没人能解释的差异。
	return int32(n), nil
}
