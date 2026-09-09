package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	platformusers "github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type PlatformUserDailyUsageQuerier interface {
	DailyUsage(context.Context, platformusers.DailyUsageInput) (connusers.DailyUsageSeries, error)
}

type dailyUsagePointResponse struct {
	Day      string     `json:"day"`
	Consumed amountBody `json:"consumed"`
	Requests countBody  `json:"requests"`
}

type dailyUsageResponse struct {
	From     string                    `json:"from"`
	To       string                    `json:"to"`
	Points   []dailyUsagePointResponse `json:"points"`
	Coverage struct {
		ExpectedDays int  `json:"expected_days"`
		CoveredDays  int  `json:"covered_days"`
		Complete     bool `json:"complete"`
	} `json:"coverage"`
	Snapshot userDetailSnapshotResponse `json:"snapshot"`
}

func GetPlatformUserDailyUsageHandler(q PlatformUserDailyUsageQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, err := connusers.DecodeUserIDSegment(chi.URLParam(r, "userID"))
		if err != nil {
			WriteError(w, r, actionInvalidUserSegment())
			return
		}
		if q == nil {
			WriteError(w, r, actionUnavailableUserDetail())
			return
		}
		days := 0
		if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
			days, err = strconv.Atoi(raw)
			if err != nil {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "days 必须是整数", err))
				return
			}
		}
		series, err := q.DailyUsage(r.Context(), platformusers.DailyUsageInput{Platform: chi.URLParam(r, "platform"), UserID: userID, Day: strings.TrimSpace(r.URL.Query().Get("day")), Days: days})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := dailyUsageResponse{From: series.From, To: series.To, Points: make([]dailyUsagePointResponse, 0, len(series.Points)), Snapshot: userDetailSnapshotResponse{ObservedAt: series.Snapshot.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Source: series.Snapshot.Source, Watermark: series.Snapshot.Watermark, IsPartial: series.Snapshot.IsPartial}}
		for _, point := range series.Points {
			var requests *int64
			if point.Requests.Known {
				value := point.Requests.Value
				requests = &value
			}
			out.Points = append(out.Points, dailyUsagePointResponse{Day: point.Day, Consumed: toAmountBody(point.Consumed), Requests: countBody{Value: requests}})
		}
		out.Coverage.ExpectedDays, out.Coverage.CoveredDays, out.Coverage.Complete = series.Coverage.ExpectedDays, series.Coverage.CoveredDays, series.Coverage.Complete
		WriteJSON(w, http.StatusOK, out)
	}
}
