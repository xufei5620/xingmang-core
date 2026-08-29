package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	platformusers "github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type PlatformUserKeysQuerier interface {
	KeyMetadata(context.Context, platformusers.KeyMetadataInput) (connusers.KeyMetadataPage, error)
}

type keyMetadataItemResponse struct {
	ID           string    `json:"id"`
	Prefix       string    `json:"prefix"`
	Status       string    `json:"status"`
	CreatedAt    *string   `json:"created_at"`
	LastUsedAt   *string   `json:"last_used_at"`
	TodayPeakRPM countBody `json:"today_peak_rpm"`
}

type keyMetadataPageResponse struct {
	Items      []keyMetadataItemResponse  `json:"items"`
	NextCursor string                     `json:"next_cursor"`
	Snapshot   userDetailSnapshotResponse `json:"snapshot"`
}

func toKeyMetadataPageBody(page connusers.KeyMetadataPage) keyMetadataPageResponse {
	out := keyMetadataPageResponse{Items: make([]keyMetadataItemResponse, 0, len(page.Items)), NextCursor: page.NextCursor, Snapshot: userDetailSnapshotResponse{ObservedAt: page.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano), Source: page.Snapshot.Source, Watermark: page.Snapshot.Watermark, IsPartial: page.Snapshot.IsPartial}}
	for _, item := range page.Items {
		var created, used *string
		if !item.CreatedAt.IsZero() {
			value := item.CreatedAt.UTC().Format(time.RFC3339Nano)
			created = &value
		}
		if !item.LastUsedAt.IsZero() {
			value := item.LastUsedAt.UTC().Format(time.RFC3339Nano)
			used = &value
		}
		var rpm *int64
		if item.TodayPeakRPM.Known {
			value := item.TodayPeakRPM.Value
			rpm = &value
		}
		out.Items = append(out.Items, keyMetadataItemResponse{ID: item.ID, Prefix: item.Prefix, Status: string(connusers.ParseUserStatus(item.Status)), CreatedAt: created, LastUsedAt: used, TodayPeakRPM: countBody{Value: rpm}})
	}
	return out
}

func ListPlatformUserKeysHandler(q PlatformUserKeysQuerier) http.HandlerFunc {
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
		limit := 0
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 200 {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "limit 必须在 1 到 200 之间", err))
				return
			}
		}
		page, err := q.KeyMetadata(r.Context(), platformusers.KeyMetadataInput{Platform: chi.URLParam(r, "platform"), UserID: userID, Limit: limit, Cursor: strings.TrimSpace(r.URL.Query().Get("cursor"))})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, toKeyMetadataPageBody(page))
	}
}
