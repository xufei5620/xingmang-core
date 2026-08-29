package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews"
)

type SavedViewLister interface {
	List(context.Context, savedviews.Owner, string) ([]savedviews.SavedView, error)
}

type savedViewSortResponse struct {
	ColumnID  string `json:"column_id"`
	Direction string `json:"direction"`
}

type savedViewColumnsResponse struct {
	Known   []string `json:"known"`
	Visible []string `json:"visible"`
}

type savedViewStateResponse struct {
	SchemaVersion int                      `json:"schema_version"`
	Query         string                   `json:"query"`
	Filters       map[string]string        `json:"filters"`
	Sort          *savedViewSortResponse   `json:"sort"`
	Columns       savedViewColumnsResponse `json:"columns"`
	Density       string                   `json:"density"`
}

type savedViewItemResponse struct {
	ID           string                 `json:"id"`
	TableKey     string                 `json:"table_key"`
	Name         string                 `json:"name"`
	StateVersion int                    `json:"state_version"`
	State        savedViewStateResponse `json:"state"`
	CreatedAt    string                 `json:"created_at"`
	UpdatedAt    string                 `json:"updated_at"`
}

func savedViewToResponse(view savedviews.SavedView) savedViewItemResponse {
	var sort *savedViewSortResponse
	if view.State.Sort != nil {
		sort = &savedViewSortResponse{
			ColumnID: view.State.Sort.ColumnID, Direction: string(view.State.Sort.Direction),
		}
	}
	filters := make(map[string]string, len(view.State.Filters))
	for key, value := range view.State.Filters {
		filters[key] = value
	}
	return savedViewItemResponse{
		ID: view.ID.String(), TableKey: view.TableKey, Name: view.Name,
		StateVersion: view.State.SchemaVersion,
		State: savedViewStateResponse{
			SchemaVersion: view.State.SchemaVersion, Query: view.State.Query, Filters: filters,
			Sort: sort,
			Columns: savedViewColumnsResponse{
				Known:   append([]string(nil), view.State.KnownColumns...),
				Visible: append([]string(nil), view.State.VisibleColumns...),
			},
			Density: string(view.State.Density),
		},
		CreatedAt: view.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: view.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func ListSavedViewsHandler(store SavedViewLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		owner, err := savedviews.OwnerFromPrincipal(p)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "个人视图只允许 HUMAN Principal", nil))
			return
		}

		query := r.URL.Query()
		for key, values := range query {
			if key != "table_key" || len(values) != 1 {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "只接受一个 table_key 参数", nil))
				return
			}
		}
		tableKey := query.Get("table_key")
		if err := savedviews.ValidateTableKey(tableKey); err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "table_key 格式无效", nil))
			return
		}
		if store == nil {
			WriteError(w, r, savedviews.ErrStore)
			return
		}
		items, err := store.List(r.Context(), owner, tableKey)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]savedViewItemResponse, 0, len(items))
		for _, item := range items {
			out = append(out, savedViewToResponse(item))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
