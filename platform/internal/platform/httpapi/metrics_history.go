package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
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
//
// 第二个返回值是 truncated：窗口内还有更旧的样本没返回。它在签名里而不是
// 藏进结构体，是为了让每个实现与调用方都必须处理它（XM-0031，见
// ops.Store.ListSamples）。
type MetricHistoryLister interface {
	ListSamples(
		ctx context.Context, environment, metricKey string, since time.Time, limit int32,
	) ([]ops.Observation, bool, error)
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
	SyncedAt string `json:"synced_at"`
	// Source 是这一个点的数据来源（XM-0031，回归 Codex 冷审 PR #48 第 3 条）。
	//
	// **逐点返回，不是整条序列一个**：样本表本来就按点存 source，而查询只按
	// (environment, metric_key) 过滤，所以一条曲线上完全可能混着不同来源——
	// Fake 切 real、换实例、接第二个 Sub2API 部署，都会在同一条线上换源。
	// 以前的响应把 source 删掉，等于把「这段是演示数据、那段是真实数据」拼成
	// 一条无差别的曲线。前端必须能据此在换源处断线或加标记。
	Source        string         `json:"source"`
	Status        string         `json:"status"`
	IsPartial     bool           `json:"is_partial"`
	Watermark     string         `json:"watermark"`
	LastErrorCode string         `json:"last_error_code"`
	Value         map[string]any `json:"value"`
}

// historyPage 是一次历史查询的完整响应。
//
// Truncated / Limit 存在的理由（XM-0031，回归 Codex 冷审 PR #48 第 2 条）：
// `hours` 最大 168，5 分钟粒度下约 2016 个点，而单次最多返回 1000 个。
// 只给 `{items}` 时，前端无法区分「前 3.5 天真的没有数据」与「服务端把它裁掉
// 了」——把一个不完整的窗口画成完整趋势，正是宪法 12 条禁止的事。
//
// `items` 里第一个 `synced_at` 只说明返回从何处开始，**不说明为何从那里开始**。
// 所以截断必须是一个显式的布尔事实，而不是让调用方去比对时间戳猜。
type historyPage struct {
	Items []historyItem `json:"items"`
	// Truncated = true 表示窗口内还有更旧的样本，但没有返回。
	// 保留的永远是最新的那批（理由见 db/queries/ops.sql）。
	Truncated bool `json:"truncated"`
	// Limit 是本次实际生效的条数上限，让 truncated 可解释：
	// 前端能直接告诉用户「只显示了最近 N 个点」。
	Limit int32 `json:"limit"`
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
		// 形态非法 → 400。判据与落库时同一条正则（ops.ValidMetricKey）。
		if !ops.ValidMetricKey(metricKey) {
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"metric_key 格式非法", nil))
			return
		}
		// 形态合法但**不存在**的指标同样 400（XM-0031，回归 Codex 冷审
		// PR #48 第 6 条）。以前只验形态，于是 `sub2api.revenu.daily` 这类拼错
		// 会返回 200 + 空数组，被读成「这个指标真的没数据」——正是当时的注释
		// 声称要避免、实际却没做的那件事。
		//
		// 错误文案带上已注册的指标清单：告诉调用方「有哪些」比只说「你写错了」
		// 有用得多。这些键名不是机密，它们本来就出现在 /metrics 的响应里。
		if !ops.KnownMetricKey(metricKey) {
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"metric_key 未注册；已注册的指标："+strings.Join(ops.RegisteredMetricKeys(), ", "),
				nil))
			return
		}

		hours, err := parseHistoryHours(r.URL.Query().Get("hours"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		// 时间库内一律 UTC（宪法 14 条）。窗口相对「此刻」算，不缓存。
		since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
		samples, truncated, err := store.ListSamples(
			r.Context(), string(env), metricKey, since, ops.MaxSampleLimit)
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
				Source:        s.Source,
				Status:        string(s.Status),
				IsPartial:     s.IsPartial,
				Watermark:     s.Watermark,
				LastErrorCode: s.LastErrorCode,
				Value:         value,
			})
		}
		WriteJSON(w, http.StatusOK, historyPage{
			Items:     out,
			Truncated: truncated,
			Limit:     ops.MaxSampleLimit,
		})
	}
}
