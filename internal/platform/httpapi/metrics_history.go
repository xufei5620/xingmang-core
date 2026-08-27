package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	// defaultHistoryHours 是不传 hours 时的窗口。
	// 24 小时对齐看板首屏「今天怎么样」，也正好是一天业务周期。
	defaultHistoryHours = 24
	// maxHistoryHours 是窗口上限（7 天）。
	//
	// 上限存在的意义不是省 CPU——索引扫多久都不慢——而是不让端点被当成
	// 数据导出口。更长跨度的分析需要降采样与聚合，那是另一个东西。
	maxHistoryHours = 168
)

// MetricHistoryLister 是指标历史样本的只读能力（*ops.Store 满足）。
type MetricHistoryLister interface {
	ListSamples(
		ctx context.Context, environment, metricKey string, since time.Time, limit int32,
	) ([]ops.Observation, error)
}

// historyItem 是趋势图上的一个点。
//
// 这里**没有** freshness 字段，也不该有：新鲜度是相对「现在」算的派生量，
// 对一个历史时刻算它没有意义。但每个点都带 status / is_partial /
// observed_at / last_error_code——这四个字段就是新鲜度的原料，前端据此把
// 失败区间画成红段、把 is_partial 画成虚线。所以它不是裸数字（规格 §9.1）：
// 每个点都自带「这一刻数据是什么成色」的说明。
type historyItem struct {
	// ObservedAt 为 null 表示这一刻没有成功采集到数据。
	ObservedAt *string `json:"observed_at"`
	// SyncedAt 是采样时刻，也是趋势图的横轴。失败样本没有新的 observed_at，
	// 只有它能把那段红放到正确的时间位置上。
	SyncedAt      string         `json:"synced_at"`
	Status        string         `json:"status"`
	IsPartial     bool           `json:"is_partial"`
	Watermark     string         `json:"watermark"`
	LastErrorCode string         `json:"last_error_code"`
	Value         map[string]any `json:"value"`
}

// parseHistoryHours 解析 hours 查询参数。
//
// 空串取默认值；非法值一律 400 而不是悄悄取默认——`hours=abc` 静默变成 24
// 小时会让前端拿着一张自以为是 7 天的图，那比报错难查得多。
func parseHistoryHours(raw string) (int, error) {
	if raw == "" {
		return defaultHistoryHours, nil
	}
	hours, err := strconv.Atoi(raw)
	if err != nil {
		return 0, action.NewError(action.CodeInvalidParams, "hours 必须是整数", err)
	}
	if hours <= 0 || hours > maxHistoryHours {
		return 0, action.NewError(action.CodeInvalidParams,
			"hours 必须在 1 与 "+strconv.Itoa(maxHistoryHours)+" 之间", nil)
	}
	return hours, nil
}

// ListMetricHistoryHandler 返回某个指标最近 N 小时的样本序列（按 synced_at 升序）。
//
// 环境范围由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致——
// 不默认生产，也不允许跨环境读取（规格 §20.5）。
// 权限（ops.ScopeRead）由路由上的 RequireScope 判定。
func ListMetricHistoryHandler(store MetricHistoryLister) http.HandlerFunc {
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

		metricKey := r.URL.Query().Get("metric_key")
		if metricKey == "" {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "缺少 metric_key", nil))
			return
		}
		// 拼错的 key 当场 400，而不是查出一个空列表——空列表会被读成
		// 「这个指标真的没数据」，而真相是这个指标根本不存在。
		// 判据与落库时同一条正则（ops.ValidMetricKey）。
		if !ops.ValidMetricKey(metricKey) {
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"metric_key 格式非法", nil))
			return
		}

		hours, err := parseHistoryHours(r.URL.Query().Get("hours"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		// 时间库内一律 UTC（宪法 14 条）。窗口相对「此刻」算，不缓存。
		since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
		samples, err := store.ListSamples(r.Context(), string(env), metricKey, since, ops.MaxSampleLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		out := make([]historyItem, 0, len(samples))
		for _, s := range samples {
			value := s.Value
			if value == nil {
				// 空对象而不是 null：前端遍历取值时不必先判空。
				value = map[string]any{}
			}
			out = append(out, historyItem{
				ObservedAt:    rfc3339Ptr(s.ObservedAt),
				SyncedAt:      s.SyncedAt.UTC().Format(time.RFC3339),
				Status:        string(s.Status),
				IsPartial:     s.IsPartial,
				Watermark:     s.Watermark,
				LastErrorCode: s.LastErrorCode,
				Value:         value,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
