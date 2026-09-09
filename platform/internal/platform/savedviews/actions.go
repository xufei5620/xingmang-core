package savedviews

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	ActionSet         = "ui.saved_view.set"
	ActionRemove      = "ui.saved_view.remove"
	actionVersion     = "1"
	resourceSavedView = "ui.saved_view"
)

var (
	allEnvironments = []string{"development", "staging", "production"}
	humanOnly       = []principal.Type{principal.TypeHuman}

	errSavedViewJSONInvalid   = errors.New("saved_view_json_invalid")
	errSavedViewDomainInvalid = errors.New("saved_view_domain_invalid")
	errSavedViewNotFound      = errors.New("saved_view_not_found")
	errSavedViewStoreFailed   = errors.New("saved_view_store_failed")
)

type savedViewMutator interface {
	Set(context.Context, Owner, SavedView) (SetResult, error)
	Remove(context.Context, Owner, uuid.UUID) (RemoveResult, error)
}

func RegisterActions(registry *action.Registry, store *Store) error {
	var mutator savedViewMutator
	if store != nil {
		mutator = store
	}
	definitions := []struct {
		definition action.Definition
		handler    action.Handler
	}{
		{setDefinition(), setHandler(mutator)},
		{removeDefinition(), removeHandler(mutator)},
	}
	for _, item := range definitions {
		if err := registry.Register(item.definition, item.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", item.definition.ID, err)
		}
	}
	return nil
}

func setDefinition() action.Definition {
	return action.Definition{
		ID: ActionSet, Version: actionVersion, RiskLevel: action.L0, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "table_key", Type: action.FieldString, Required: true},
			{Name: "name", Type: action.FieldString, Required: true},
			{Name: "schema_version", Type: action.FieldInt, Required: true},
			{Name: "query", Type: action.FieldString},
			{Name: "filters_json", Type: action.FieldString, Required: true},
			{Name: "sort_column", Type: action.FieldString},
			{Name: "sort_direction", Type: action.FieldString, Enum: []string{"", "asc", "desc"}},
			{Name: "known_columns", Type: action.FieldStringSlice, Required: true},
			{Name: "visible_columns", Type: action.FieldStringSlice, Required: true},
			{Name: "density", Type: action.FieldString, Required: true, Enum: []string{"compact", "standard", "comfortable"}},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func removeDefinition() action.Definition {
	return action.Definition{
		ID: ActionRemove, Version: actionVersion, RiskLevel: action.L0, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "saved_view_id", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func ownerFromContext(ctx context.Context) (Owner, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return Owner{}, errSavedViewDomainInvalid
	}
	owner, err := OwnerFromPrincipal(p)
	if err != nil {
		return Owner{}, errSavedViewDomainInvalid
	}
	return owner, nil
}

func fixedMutationError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return errSavedViewNotFound
	case errors.Is(err, ErrInvalidState), errors.Is(err, ErrInvalidName),
		errors.Is(err, ErrInvalidTableKey), errors.Is(err, ErrInvalidOwner),
		errors.Is(err, ErrQuotaExceeded):
		return errSavedViewDomainInvalid
	default:
		return errSavedViewStoreFailed
	}
}

type actionStateResponse struct {
	SchemaVersion int               `json:"schema_version"`
	Query         string            `json:"query"`
	Filters       map[string]string `json:"filters"`
	Sort          *Sort             `json:"sort"`
	Columns       stateColumnsWire  `json:"columns"`
	Density       Density           `json:"density"`
}

type actionSavedViewResponse struct {
	ID           string              `json:"id"`
	TableKey     string              `json:"table_key"`
	Name         string              `json:"name"`
	StateVersion int                 `json:"state_version"`
	State        actionStateResponse `json:"state"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
}

func actionResponse(view SavedView) actionSavedViewResponse {
	return actionSavedViewResponse{
		ID: view.ID.String(), TableKey: view.TableKey, Name: view.Name,
		StateVersion: view.State.SchemaVersion,
		State: actionStateResponse{
			SchemaVersion: view.State.SchemaVersion, Query: view.State.Query,
			Filters: view.State.Filters, Sort: view.State.Sort,
			Columns: stateColumnsWire{Known: view.State.KnownColumns, Visible: view.State.VisibleColumns},
			Density: view.State.Density,
		},
		CreatedAt: view.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: view.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func setHandler(store savedViewMutator) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, errSavedViewStoreFailed
		}
		owner, err := ownerFromContext(ctx)
		if err != nil {
			return nil, err
		}
		filters, err := DecodeFiltersJSON([]byte(action.StringParam(params, "filters_json")))
		if err != nil {
			return nil, errSavedViewJSONInvalid
		}
		sortColumn := action.StringParam(params, "sort_column")
		sortDirection := action.StringParam(params, "sort_direction")
		if (sortColumn == "") != (sortDirection == "") {
			return nil, errSavedViewDomainInvalid
		}
		state := StateV1{
			SchemaVersion: action.IntParam(params, "schema_version"),
			Query:         action.StringParam(params, "query"), Filters: filters,
			KnownColumns:   append([]string(nil), action.StringSliceParam(params, "known_columns")...),
			VisibleColumns: append([]string(nil), action.StringSliceParam(params, "visible_columns")...),
			Density:        Density(action.StringParam(params, "density")),
		}
		if sortColumn != "" {
			state.Sort = &Sort{ColumnID: sortColumn, Direction: SortDirection(sortDirection)}
		}
		name, err := NormalizeName(action.StringParam(params, "name"))
		if err != nil || ValidateTableKey(action.StringParam(params, "table_key")) != nil || ValidateState(state) != nil {
			return nil, errSavedViewDomainInvalid
		}
		result, err := store.Set(ctx, owner, SavedView{
			TableKey: action.StringParam(params, "table_key"), Name: name, State: state,
		})
		if err != nil {
			return nil, fixedMutationError(err)
		}
		action.RecordResource(ctx, resourceSavedView, result.After.ID.String())
		if result.BeforeHash != nil {
			action.RecordBefore(ctx, map[string]any{"state_hash": *result.BeforeHash})
		}
		action.RecordAfter(ctx, map[string]any{"state_hash": result.After.StateHash})
		return actionResponse(result.After), nil
	}
}

func removeHandler(store savedViewMutator) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, errSavedViewStoreFailed
		}
		owner, err := ownerFromContext(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(action.StringParam(params, "saved_view_id"))
		if err != nil {
			return nil, errSavedViewDomainInvalid
		}
		result, err := store.Remove(ctx, owner, id)
		if err != nil {
			return nil, fixedMutationError(err)
		}
		action.RecordResource(ctx, resourceSavedView, result.ID.String())
		action.RecordBefore(ctx, map[string]any{"state_hash": result.BeforeHash})
		return map[string]string{"id": result.ID.String()}, nil
	}
}
