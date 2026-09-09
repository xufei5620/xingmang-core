package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

type RunwayPreviewSource interface {
	UpstreamRunways(context.Context, string, finance.RunwayThresholds) ([]finance.UpstreamRunway, error)
}

// RunwayPreviewAlertCoverageLister lets the preview distinguish a complete
// active-R5 set from a capped page. The legacy AlertLister remains accepted
// for tests/older stores and is treated as complete by default.
type RunwayPreviewAlertCoverageLister interface {
	ListByStatusWithTruncation(context.Context, string, []alerts.Status, int32) ([]alerts.Alert, bool, error)
}

type runwayPreviewThresholdResponse struct {
	CriticalDays int `json:"critical_days"`
	WarningDays  int `json:"warning_days"`
	SeriousDays  int `json:"serious_days"`
}

type runwayPreviewCoverageResponse struct {
	Total          int            `json:"total"`
	Known          int            `json:"known"`
	UnknownReasons map[string]int `json:"unknown_reasons"`
}

type runwayPreviewCountsResponse struct {
	WouldOpen           int `json:"would_open"`
	WouldEscalate       int `json:"would_escalate"`
	WouldDeescalate     int `json:"would_deescalate"`
	WouldResolve        int `json:"would_resolve"`
	Unchanged           int `json:"unchanged"`
	CurrentInconsistent int `json:"current_inconsistent"`
}

type runwayPreviewItemResponse struct {
	AccountID            string  `json:"account_id"`
	Name                 string  `json:"name"`
	Days                 *int    `json:"days"`
	OldLevel             string  `json:"old_level"`
	NewLevel             string  `json:"new_level"`
	CurrentAlertSeverity string  `json:"current_alert_severity"`
	CurrentAlertStatus   string  `json:"current_alert_status"`
	AlertCount           int     `json:"alert_count"`
	Transition           string  `json:"alert_transition"`
	ConsistencyReason    *string `json:"consistency_reason"`
	ObservedAt           *string `json:"observed_at"`
}

type runwayPreviewResponse struct {
	Current               runwayPreviewThresholdResponse `json:"current"`
	Proposed              runwayPreviewThresholdResponse `json:"proposed"`
	CurrentRevision       int64                          `json:"current_revision"`
	EvaluationAt          string                         `json:"evaluation_at"`
	Coverage              runwayPreviewCoverageResponse  `json:"coverage"`
	Counts                runwayPreviewCountsResponse    `json:"counts"`
	Items                 []runwayPreviewItemResponse    `json:"items"`
	HasMore               bool                           `json:"has_more"`
	AlertCoverageComplete bool                           `json:"alert_coverage_complete"`
}

func parseRunwayPreviewDays(raw, name string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, action.NewError(action.CodeInvalidParams, name+" 必须是正整数", err)
	}
	return n, nil
}

func parseRunwayPreviewLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 200 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须在 1 到 200 之间", err)
	}
	return n, nil
}

func runwayThresholdResponseFrom(thresholds finance.RunwayThresholds) runwayPreviewThresholdResponse {
	return runwayPreviewThresholdResponse{
		CriticalDays: thresholds.CriticalDays,
		WarningDays:  thresholds.WarningDays,
		SeriousDays:  thresholds.SeriousDays,
	}
}

func runwayPreviewItemResponseFrom(item alerts.RunwayImpactItem) runwayPreviewItemResponse {
	var reason *string
	if item.ConsistencyReason != "" {
		reason = &item.ConsistencyReason
	}
	var observed *string
	if item.ObservedAt != nil {
		value := item.ObservedAt.UTC().Format(time.RFC3339)
		observed = &value
	}
	return runwayPreviewItemResponse{
		AccountID: item.AccountID.String(), Name: item.Name, Days: item.Days,
		OldLevel: string(item.OldLevel), NewLevel: string(item.NewLevel),
		CurrentAlertSeverity: string(item.CurrentAlertSeverity),
		CurrentAlertStatus:   string(item.CurrentAlertStatus), AlertCount: item.AlertCount,
		Transition: string(item.Transition), ConsistencyReason: reason, ObservedAt: observed,
	}
}

// PreviewRunwayThresholdHandler 只读当前配置、可用天数与 R5 活跃告警，然后
// 使用 alerts 的纯比较器返回影响预览。它不调用 Action、审计或任何写方法。
func PreviewRunwayThresholdHandler(
	provider RunwayThresholdProvider,
	runways RunwayPreviewSource,
	alertStore AlertLister,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		environment, err := runwayEnvironment(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		critical, err := parseRunwayPreviewDays(r.URL.Query().Get("critical_days"), "critical_days")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		warning, err := parseRunwayPreviewDays(r.URL.Query().Get("warning_days"), "warning_days")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		serious, err := parseRunwayPreviewDays(r.URL.Query().Get("serious_days"), "serious_days")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		proposed := finance.RunwayThresholds{CriticalDays: critical, WarningDays: warning, SeriousDays: serious}
		if err := proposed.Validate(); err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "必须满足 0 < critical < warning < serious", err))
			return
		}
		limit, err := parseRunwayPreviewLimit(r.URL.Query().Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		// evaluationAt 是这次预览的服务端时间；不能把它冒充每条余额证据。
		evaluationAt := time.Now().UTC()
		currentSnapshot, err := provider.Current(r.Context(), environment)
		if err != nil {
			WriteError(w, r, runwayConfigUnavailable(err))
			return
		}
		currentRunways, err := runways.UpstreamRunways(r.Context(), environment, currentSnapshot.Thresholds)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeExecutionFailed, "可用天数预览暂不可用", err))
			return
		}
		alertStatuses := []alerts.Status{alerts.StatusOpen, alerts.StatusAcknowledged, alerts.StatusSilenced, alerts.StatusReopened}
		var active []alerts.Alert
		// A legacy reader has no way to prove that its capped result is complete;
		// fail closed in the evidence envelope rather than silently claiming full
		// consistency. The production alerts.Store implements the bounded method.
		alertCoverageComplete := false
		if coverageLister, ok := alertStore.(RunwayPreviewAlertCoverageLister); ok {
			var truncated bool
			active, truncated, err = coverageLister.ListByStatusWithTruncation(r.Context(), environment, alertStatuses, alerts.MaxListLimit)
			alertCoverageComplete = !truncated
		} else {
			active, err = alertStore.ListByStatus(r.Context(), environment, alertStatuses, alerts.MaxListLimit)
		}
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeExecutionFailed, "告警一致性预览暂不可用", err))
			return
		}
		preview, err := alerts.PreviewRunwayThresholds(currentSnapshot.Thresholds, proposed, currentRunways, active)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "阈值预览失败", err))
			return
		}
		hasMore := len(preview.Items) > limit
		if hasMore {
			preview.Items = preview.Items[:limit]
		}
		items := make([]runwayPreviewItemResponse, 0, len(preview.Items))
		for _, item := range preview.Items {
			items = append(items, runwayPreviewItemResponseFrom(item))
		}
		// comparator 已按 transition/account 排序；再次稳定排序作为边界护栏，
		// 防止未来分页或转换层改变顺序。
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Transition != items[j].Transition {
				return items[i].Transition < items[j].Transition
			}
			return items[i].AccountID < items[j].AccountID
		})
		unknown := make(map[string]int, len(preview.Coverage.UnknownReasons))
		for reason, count := range preview.Coverage.UnknownReasons {
			unknown[reason] = count
		}
		WriteJSON(w, http.StatusOK, runwayPreviewResponse{
			Current:         runwayThresholdResponseFrom(preview.Current),
			Proposed:        runwayThresholdResponseFrom(preview.Proposed),
			CurrentRevision: currentSnapshot.Revision,
			EvaluationAt:    evaluationAt.Format(time.RFC3339),
			Coverage:        runwayPreviewCoverageResponse{Total: preview.Coverage.Total, Known: preview.Coverage.Known, UnknownReasons: unknown},
			Counts: runwayPreviewCountsResponse{
				WouldOpen: preview.Counts.WouldOpen, WouldEscalate: preview.Counts.WouldEscalate,
				WouldDeescalate: preview.Counts.WouldDeescalate, WouldResolve: preview.Counts.WouldResolve,
				Unchanged: preview.Counts.Unchanged, CurrentInconsistent: preview.Counts.CurrentInconsistent,
			},
			Items: items, HasMore: hasMore, AlertCoverageComplete: alertCoverageComplete,
		})
	}
}
