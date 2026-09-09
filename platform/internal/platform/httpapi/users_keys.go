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

func toKeyMetadataPageBody(page connusers.KeyMetadataPage) (keyMetadataPageResponse, error) {
	if page.Snapshot.ObservedAt.IsZero() || strings.TrimSpace(page.Snapshot.Source) == "" {
		return keyMetadataPageResponse{}, connusers.ErrInvalidKeyMetadataQuery
	}
	if err := connusers.ValidateKeyMetadataCursor(page.NextCursor); err != nil {
		return keyMetadataPageResponse{}, err
	}
	out := keyMetadataPageResponse{Items: make([]keyMetadataItemResponse, 0, len(page.Items)), NextCursor: page.NextCursor, Snapshot: userDetailSnapshotResponse{ObservedAt: page.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano), Source: page.Snapshot.Source, Watermark: page.Snapshot.Watermark, IsPartial: page.Snapshot.IsPartial}}
	for _, item := range page.Items {
		if err := connusers.ValidateKeyMetadataID(item.ID); err != nil {
			// A malformed record ID could be a leaked source key. Reject the
			// whole page rather than silently returning a partial inventory.
			return keyMetadataPageResponse{}, err
		}
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
		prefix := item.Prefix
		if err := connusers.ValidateKeyPrefix(prefix); err != nil {
			// Fail closed for an upstream value that could contain too much of a
			// credential; never truncate in the HTTP layer and accidentally imply
			// the resulting fragment is safe.
			return keyMetadataPageResponse{}, err
		}
		out.Items = append(out.Items, keyMetadataItemResponse{ID: item.ID, Prefix: prefix, Status: normalizeKeyStatus(item.Status), CreatedAt: created, LastUsedAt: used, TodayPeakRPM: countBody{Value: rpm}})
	}
	return out, nil
}

// normalizeKeyStatus keeps the small, documented metadata state set and maps
// every unrecognized upstream value to unknown. It must not reuse user-status
// parsing: a revoked key is not the same thing as a disabled user.
func normalizeKeyStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active", "enabled", "normal":
		return "active"
	case "revoked", "revoke":
		return "revoked"
	case "disabled", "inactive":
		return "disabled"
	case "limited", "restricted":
		return "limited"
	default:
		return "unknown"
	}
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
		out, err := toKeyMetadataPageBody(page)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeExecutionFailed, "上游返回了不安全的 Key 元数据", err))
			return
		}
		WriteJSON(w, http.StatusOK, out)
	}
}
