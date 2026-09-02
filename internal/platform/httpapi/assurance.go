package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ChannelAssuranceQuerier 是「渠道保障」保障概览 / 历史记录两个子页签的只读
// 能力（*channelassurance.Service 满足）。
//
// httpapi 包不直接 import internal/platform/channelassurance——与
// RequestLogQuerier 对 requestlog.Service 的做法一致，路由层只认窄接口,
// 不认识具体实现，方便测试用替身注入。
type ChannelAssuranceQuerier interface {
	Overview(ctx context.Context, platform string, preset reqlog.AssuranceWindowPreset) (reqlog.AssuranceResult, error)
	History(ctx context.Context, platform string) ([]reqlog.AssuranceResult, error)
}

type assuranceStatusClasses struct {
	Success      int64 `json:"success"`
	ClientError  int64 `json:"client_error"`
	ServerError  int64 `json:"server_error"`
	Disconnected int64 `json:"disconnected"`
	Other        int64 `json:"other"`
}

func toAssuranceStatusClasses(c reqlog.StatusClassCounts) assuranceStatusClasses {
	return assuranceStatusClasses{
		Success: c.Success, ClientError: c.ClientError, ServerError: c.ServerError,
		Disconnected: c.Disconnected, Other: c.Other,
	}
}

// assuranceLatencyPercentiles 是延迟百分位的对外表示。三个百分位字段与
// SampleCount==0 时一并为 null——没有样本时不存在百分位这个概念，前端不能
// 把 null 显示成 0ms（宪法 12 条同一条纪律，用在延迟而不是金额上）。
type assuranceLatencyPercentiles struct {
	SampleCount int64  `json:"sample_count"`
	P50MS       *int64 `json:"p50_ms"`
	P95MS       *int64 `json:"p95_ms"`
	P99MS       *int64 `json:"p99_ms"`
}

func toAssuranceLatencyPercentiles(p reqlog.LatencyPercentiles) assuranceLatencyPercentiles {
	return assuranceLatencyPercentiles{SampleCount: p.SampleCount, P50MS: p.P50MS, P95MS: p.P95MS, P99MS: p.P99MS}
}

type assuranceModelRow struct {
	// Model 空串表示 reqlog 没记到模型名；前端必须显示成「未知模型」而不是
	// 空白（与 requestSummaryItem.Username 空串的处理同一条纪律）。
	Model         string                      `json:"model"`
	RequestCount  int64                       `json:"request_count"`
	StatusClasses assuranceStatusClasses      `json:"status_classes"`
	DurationMS    assuranceLatencyPercentiles `json:"duration_ms"`
	TTFBMS        assuranceLatencyPercentiles `json:"ttfb_ms"`
}

func toAssuranceModelRows(rows []reqlog.ModelBreakdownRow) []assuranceModelRow {
	out := make([]assuranceModelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, assuranceModelRow{
			Model: r.Model, RequestCount: r.RequestCount,
			StatusClasses: toAssuranceStatusClasses(r.StatusClasses),
			DurationMS:    toAssuranceLatencyPercentiles(r.Duration),
			TTFBMS:        toAssuranceLatencyPercentiles(r.TTFB),
		})
	}
	return out
}

// assuranceCoverage 说明这次聚合覆盖了多少天的目录、其中几天缺失、跳过了
// 多少坏行——「保障概览」与「历史记录」都要能回答「这批数字覆盖到哪天」
// （宪法 12 条：数据新鲜度/完整性必须可见）。
type assuranceCoverage struct {
	SpannedDays int   `json:"spanned_days"`
	MissingDays int   `json:"missing_days"`
	BadLines    int64 `json:"bad_lines"`
}

// assuranceResultBody 是 reqlog.AssuranceResult 的对外表示。保障概览（单个
// 窗口）与历史记录（逐日，多条）共用同一个形状——Day 在概览响应里恒为空串
// （`omitempty` 因此不出现），历史记录的每一天里则带上当天的业务日与闭区间,
// 让前端能用同一套渲染函数处理两个页面的每一行/每一天。
type assuranceResultBody struct {
	Day             string                      `json:"day,omitempty"`
	Since           string                      `json:"since"`
	Until           string                      `json:"until"`
	RequestCount    int64                       `json:"request_count"`
	StatusClasses   assuranceStatusClasses      `json:"status_classes"`
	DurationMS      assuranceLatencyPercentiles `json:"duration_ms"`
	TTFBMS          assuranceLatencyPercentiles `json:"ttfb_ms"`
	Models          []assuranceModelRow         `json:"models"`
	ModelsTruncated bool                        `json:"models_truncated"`
	Coverage        assuranceCoverage           `json:"coverage"`
}

func toAssuranceResultBody(r reqlog.AssuranceResult) assuranceResultBody {
	return assuranceResultBody{
		Day:             r.Day,
		Since:           r.Since.UTC().Format(time.RFC3339),
		Until:           r.Until.UTC().Format(time.RFC3339),
		RequestCount:    r.RequestCount,
		StatusClasses:   toAssuranceStatusClasses(r.StatusClasses),
		DurationMS:      toAssuranceLatencyPercentiles(r.Duration),
		TTFBMS:          toAssuranceLatencyPercentiles(r.TTFB),
		Models:          toAssuranceModelRows(r.Models),
		ModelsTruncated: r.ModelsTruncated,
		Coverage:        assuranceCoverage{SpannedDays: r.SpannedDays, MissingDays: r.MissingDays, BadLines: r.BadLines},
	}
}

// assuranceOverviewBody 是「保障概览」端点的响应体。
type assuranceOverviewBody struct {
	Source string `json:"source"`
	// Window 原样回显请求参数（15m/1h/24h），让前端不用自己记住选了哪一档。
	Window string `json:"window"`
	assuranceResultBody
	// ChannelBreakdownSupported 恒为 false（见 reqlog.ChannelBreakdownUnsupportedReason）
	// ——渠道保障页的「按渠道」维度必须显式标注做不到，而不是安静地不出现
	// 一列，让人以为是这一片忘了做。
	ChannelBreakdownSupported bool          `json:"channel_breakdown_supported"`
	ChannelBreakdownReason    string        `json:"channel_breakdown_reason"`
	RetentionDays             int           `json:"retention_days"`
	Freshness                 freshnessBody `json:"freshness"`
}

// assuranceHistoryDayBody 是历史记录里的一天。
type assuranceHistoryDayBody struct {
	assuranceResultBody
	// Missing 是 Coverage.MissingDays>0 的布尔简写（历史记录里每一天恒
	// SpannedDays==1，MissingDays 只能是 0 或 1）——前端渲染逐日表格时不用
	// 反复判 `coverage.missing_days > 0`，直接读这个字段。
	Missing bool `json:"missing"`
}

// assuranceHistoryBody 是「历史记录」端点的响应体。
type assuranceHistoryBody struct {
	Source                    string                    `json:"source"`
	Days                      []assuranceHistoryDayBody `json:"days"`
	ChannelBreakdownSupported bool                      `json:"channel_breakdown_supported"`
	ChannelBreakdownReason    string                    `json:"channel_breakdown_reason"`
	RetentionDays             int                       `json:"retention_days"`
	Freshness                 freshnessBody             `json:"freshness"`
}

// freshnessFromAssurance 把一次聚合的「是否覆盖不全」折算成统一的新鲜度形状。
//
// 与 freshnessFromSnapshot（requests.go）同一条纪律：这是一次**实时读取**,
// 不是周期采集的观测，所以 ObservedAt 恒等于「我们刚刚问的这一刻」、
// staleness 恒为 0——这里没有「上游多久没更新」的概念，只有「这次聚合覆盖
// 全不全」，因此 State 只在 fresh/partial 两档之间取值（不会有
// stale/failed/uninitialized：读失败根本不会走到这里，是一次实时聚合而不是
// 依赖某条周期任务的产出）。
func freshnessFromAssurance(isPartial bool, now time.Time) freshnessBody {
	observedAt := now.UTC()
	staleness := int64(0)
	body := freshnessBody{
		StalenessSeconds: &staleness,
		ThresholdSeconds: requestLogStalenessThresholdSeconds,
		IsPartial:        isPartial,
		ObservedAt:       rfc3339Ptr(&observedAt),
		LastSuccess:      rfc3339Ptr(&observedAt),
	}
	if isPartial {
		body.State = "partial"
	} else {
		body.State = "fresh"
	}
	return body
}

func parseAssuranceWindowParam(r *http.Request) (reqlog.AssuranceWindowPreset, error) {
	preset, err := reqlog.ParseAssuranceWindowPreset(r.URL.Query().Get("window"))
	if err != nil {
		return "", action.NewError(action.CodeInvalidParams,
			"window 只接受 15m / 1h / 24h", err)
	}
	return preset, nil
}

// GetPlatformAssuranceOverviewHandler 读取某平台在指定窗口内的被动保障聚合
// （渠道保障 · 保障概览子页签）。
//
// 权限（requestlog.ScopeRead，即 request.read）由路由上的 RequireScope
// 判定——复用既有 scope，不新开一个（团队交接的明确要求）。
func GetPlatformAssuranceOverviewHandler(q ChannelAssuranceQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		preset, err := parseAssuranceWindowParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		result, err := q.Overview(r.Context(), chi.URLParam(r, "platform"), preset)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, assuranceOverviewBody{
			Source:                    result.Source,
			Window:                    string(preset),
			assuranceResultBody:       toAssuranceResultBody(result),
			ChannelBreakdownSupported: result.ChannelBreakdownSupported,
			ChannelBreakdownReason:    result.ChannelBreakdownReason,
			RetentionDays:             reqlog.RetentionDays,
			Freshness:                 freshnessFromAssurance(result.IsPartial(), time.Now()),
		})
	}
}

// GetPlatformAssuranceHistoryHandler 读取某平台最近 7 天的逐日被动保障聚合
// （渠道保障 · 历史记录子页签）。没有可用的 rollup 时这是唯一的历史来源,
// 因此固定 7 天，不接受任意区间参数（见 channelassurance.HistoryWindowDays
// 的说明）。
func GetPlatformAssuranceHistoryHandler(q ChannelAssuranceQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		days, err := q.History(r.Context(), chi.URLParam(r, "platform"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		bodyDays := make([]assuranceHistoryDayBody, 0, len(days))
		anyPartial := false
		var source string
		for _, d := range days {
			if d.IsPartial() {
				anyPartial = true
			}
			if source == "" {
				source = d.Source
			}
			bodyDays = append(bodyDays, assuranceHistoryDayBody{
				assuranceResultBody: toAssuranceResultBody(d),
				Missing:             d.MissingDays > 0,
			})
		}
		WriteJSON(w, http.StatusOK, assuranceHistoryBody{
			Source:                    source,
			Days:                      bodyDays,
			ChannelBreakdownSupported: false,
			ChannelBreakdownReason:    reqlog.ChannelBreakdownUnsupportedReason,
			RetentionDays:             reqlog.RetentionDays,
			Freshness:                 freshnessFromAssurance(anyPartial, time.Now()),
		})
	}
}
