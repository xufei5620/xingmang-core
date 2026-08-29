package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// RunwayThresholdProvider / RunwayThresholdHistoryLister are kept as narrow
// HTTP contracts so tests and future non-Postgres readers cannot leak generated
// sqlc models into the API package.
type RunwayThresholdProvider interface {
	Current(context.Context, string) (finance.RunwayThresholdSnapshot, error)
}

type RunwayThresholdHistoryLister interface {
	ListHistory(context.Context, finance.RunwayThresholdHistoryQuery) ([]finance.RunwayThresholdHistoryEntry, error)
}

type runwayThresholdResponse struct {
	Environment  string `json:"environment"`
	CriticalDays int    `json:"critical_days"`
	WarningDays  int    `json:"warning_days"`
	SeriousDays  int    `json:"serious_days"`
	Revision     int64  `json:"revision"`
	Source       string `json:"source"`
	UpdatedAt    string `json:"updated_at"`
	UpdatedBy    string `json:"updated_by"`
	Reason       string `json:"reason"`
}

type runwayThresholdHistoryResponse struct {
	Environment  string `json:"environment"`
	Revision     int64  `json:"revision"`
	CriticalDays int    `json:"critical_days"`
	WarningDays  int    `json:"warning_days"`
	SeriousDays  int    `json:"serious_days"`
	ChangedAt    string `json:"changed_at"`
	ChangedBy    string `json:"changed_by"`
	Reason       string `json:"reason"`
	RequestID    string `json:"request_id"`
	ChangeSource string `json:"change_source"`
}

type runwayThresholdHistoryPage struct {
	Items   []runwayThresholdHistoryResponse `json:"items"`
	HasMore bool                             `json:"has_more"`
}

func thresholdResponse(snapshot finance.RunwayThresholdSnapshot) runwayThresholdResponse {
	return runwayThresholdResponse{
		Environment:  snapshot.Environment,
		CriticalDays: snapshot.Thresholds.CriticalDays,
		WarningDays:  snapshot.Thresholds.WarningDays,
		SeriousDays:  snapshot.Thresholds.SeriousDays,
		Revision:     snapshot.Revision,
		Source:       snapshot.Source,
		UpdatedAt:    snapshot.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedBy:    snapshot.UpdatedBy,
		Reason:       snapshot.Reason,
	}
}

func thresholdHistoryResponse(item finance.RunwayThresholdHistoryEntry) runwayThresholdHistoryResponse {
	return runwayThresholdHistoryResponse{
		Environment:  item.Environment,
		Revision:     item.Revision,
		CriticalDays: item.Thresholds.CriticalDays,
		WarningDays:  item.Thresholds.WarningDays,
		SeriousDays:  item.Thresholds.SeriousDays,
		ChangedAt:    item.ChangedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		ChangedBy:    item.ChangedBy,
		Reason:       item.Reason,
		RequestID:    item.RequestID,
		ChangeSource: item.ChangeSource,
	}
}

func runwayConfigUnavailable(err error) error {
	if err == nil {
		return nil
	}
	return action.NewError(action.CodeRunwayConfigUnavailable, "可用天数阈值配置暂不可用", err)
}

func runwayEnvironment(r *http.Request) (string, error) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		return "", action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	env, err := resolveEnvironment(r, p)
	if err != nil {
		return "", err
	}
	return string(env), nil
}

// GetRunwayThresholdHandler 返回当前 DB revision；缺行和数据库错误都明确为
// 503，不会在 HTTP 层合成旧的 5/10/20 默认值。
func GetRunwayThresholdHandler(provider RunwayThresholdProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		environment, err := runwayEnvironment(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		snapshot, err := provider.Current(r.Context(), environment)
		if err != nil {
			WriteError(w, r, runwayConfigUnavailable(err))
			return
		}
		WriteJSON(w, http.StatusOK, thresholdResponse(snapshot))
	}
}

func parseRunwayHistoryLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 100 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须在 1 到 100 之间", err)
	}
	return n, nil
}

func parseBeforeRevision(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0, action.NewError(action.CodeInvalidParams, "before_revision 必须是正整数", err)
	}
	return n, nil
}

// ListRunwayThresholdHistoryHandler 返回按 revision 降序的有限历史页。
func ListRunwayThresholdHistoryHandler(lister RunwayThresholdHistoryLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		environment, err := runwayEnvironment(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseRunwayHistoryLimit(r.URL.Query().Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		before, err := parseBeforeRevision(r.URL.Query().Get("before_revision"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := lister.ListHistory(r.Context(), finance.RunwayThresholdHistoryQuery{
			Environment: environment, BeforeRevision: before, Limit: limit + 1,
		})
		if err != nil {
			if errors.Is(err, finance.ErrRunwayConfigUnavailable) {
				WriteError(w, r, runwayConfigUnavailable(err))
			} else {
				WriteError(w, r, runwayConfigUnavailable(err))
			}
			return
		}
		hasMore := len(items) > limit
		if hasMore {
			items = items[:limit]
		}
		out := make([]runwayThresholdHistoryResponse, 0, len(items))
		for _, item := range items {
			out = append(out, thresholdHistoryResponse(item))
		}
		WriteJSON(w, http.StatusOK, runwayThresholdHistoryPage{Items: out, HasMore: hasMore})
	}
}
