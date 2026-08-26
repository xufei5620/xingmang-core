package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// MetricLister 是指标观测的只读能力（*ops.Store 满足）。
type MetricLister interface {
	ListByEnvironment(ctx context.Context, environment string) ([]ops.Observation, error)
}

type freshnessBody struct {
	State            string  `json:"state"`
	StalenessSeconds *int64  `json:"staleness_seconds"`
	ThresholdSeconds int32   `json:"threshold_seconds"`
	IsPartial        bool    `json:"is_partial"`
	ObservedAt       *string `json:"observed_at"`
	LastSuccess      *string `json:"last_success"`
	LastErrorCode    string  `json:"last_error_code"`
}

// metricItem 把值与新鲜度绑在一起。
//
// **没有只返回值的字段组合**——规格 §9.1 禁止裸数字冒充实时完整数据，
// 这条铁律在这里由结构本身保证：freshness 不是可选字段。
type metricItem struct {
	MetricKey   string         `json:"metric_key"`
	Source      string         `json:"source"`
	Environment string         `json:"environment"`
	Watermark   string         `json:"watermark"`
	Value       map[string]any `json:"value"`
	Freshness   freshnessBody  `json:"freshness"`
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// ListMetricsHandler 列出某环境下的指标及其新鲜度。
//
// 未显式指定 environment 时用调用者 Principal 的环境——不默认生产（规格 §20.5）。
func ListMetricsHandler(store MetricLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		envParam := r.URL.Query().Get("environment")
		if envParam == "" {
			envParam = p.Environment
		}
		env, err := registry.ParseEnvironment(envParam)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"environment 必须是 development / staging / production 之一", err))
			return
		}

		items, err := store.ListByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		// 新鲜度按「此刻」计算——同一条记录随时间推移会自动从 fresh 变 stale，
		// 不需要任何后台任务翻转标记（规格 §9.1）
		now := time.Now().UTC()
		out := make([]metricItem, 0, len(items))
		for _, o := range items {
			f := o.Freshness(now)
			out = append(out, metricItem{
				MetricKey:   o.MetricKey,
				Source:      o.Source,
				Environment: o.Environment,
				Watermark:   o.Watermark,
				Value:       o.Value,
				Freshness: freshnessBody{
					State:            string(f.State),
					StalenessSeconds: f.StalenessSeconds,
					ThresholdSeconds: f.ThresholdSeconds,
					IsPartial:        f.IsPartial,
					ObservedAt:       rfc3339Ptr(f.ObservedAt),
					LastSuccess:      rfc3339Ptr(f.LastSuccess),
					LastErrorCode:    f.LastErrorCode,
				},
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
