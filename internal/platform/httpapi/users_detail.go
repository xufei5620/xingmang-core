package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	platformusers "github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type PlatformUserDetailsQuerier interface {
	Get(context.Context, platformusers.DetailInput) (connusers.UserDetail, error)
}

func actionInvalidUserSegment() error {
	return action.NewError(action.CodeNotRegistered, "用户详情地址不是 canonical 形式", nil)
}

func actionUnavailableUserDetail() error {
	return action.NewError(action.CodeAdvancedControlsRequired, "用户详情真实读取尚未接入", nil)
}

type userDetailSnapshotResponse struct {
	ObservedAt string `json:"observed_at"`
	Source     string `json:"source"`
	Watermark  string `json:"watermark"`
	IsPartial  bool   `json:"is_partial"`
}

type platformUserDetailResponse struct {
	Ref          connusers.UserRef          `json:"ref"`
	User         platformUserItem           `json:"user"`
	RegisteredAt *string                    `json:"registered_at"`
	Period       periodBody                 `json:"period"`
	Snapshot     userDetailSnapshotResponse `json:"snapshot"`
	Capabilities []string                   `json:"capabilities"`
}

func GetPlatformUserHandler(q PlatformUserDetailsQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platform := strings.TrimSpace(chi.URLParam(r, "platform"))
		segment := chi.URLParam(r, "userID")
		userID, err := connusers.DecodeUserIDSegment(segment)
		if err != nil {
			// Canonical errors are intentionally not sent to the connector; the caller must
			// fix the URL rather than receive a fuzzy result.
			WriteError(w, r, actionInvalidUserSegment())
			return
		}
		if q == nil {
			WriteError(w, r, actionUnavailableUserDetail())
			return
		}
		detail, err := q.Get(r.Context(), platformusers.DetailInput{
			Platform: platform, UserID: userID,
			Day:         strings.TrimSpace(r.URL.Query().Get("day")),
			Granularity: strings.TrimSpace(r.URL.Query().Get("granularity")),
		})
		if err != nil {
			if errors.Is(err, connusers.ErrNotFound) {
				WriteError(w, r, action.NewError(action.CodeNotRegistered, "没有这条用户记录", err))
				return
			}
			if errors.Is(err, connusers.ErrLookupIncomplete) {
				WriteError(w, r, action.NewError(action.CodeExecutionFailed, "用户精确查找未完成，请重试", err))
				return
			}
			WriteError(w, r, err)
			return
		}
		var registered *string
		if !detail.RegisteredAt.IsZero() {
			value := detail.RegisteredAt.UTC().Format(time.RFC3339Nano)
			registered = &value
		}
		caps := make([]string, 0, len(detail.Capabilities))
		for _, capability := range detail.Capabilities {
			caps = append(caps, string(capability))
		}
		WriteJSON(w, http.StatusOK, platformUserDetailResponse{
			Ref: detail.Ref, User: toPlatformUserItem(detail.User), RegisteredAt: registered,
			Period: toPeriodBody(detail.Period), Snapshot: userDetailSnapshotResponse{ObservedAt: detail.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano), Source: detail.Snapshot.Source, Watermark: detail.Snapshot.Watermark, IsPartial: detail.Snapshot.IsPartial},
			Capabilities: caps,
		})
	}
}
