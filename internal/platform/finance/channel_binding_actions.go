package finance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	ActionPlatformChannelBindingSet    = "finance.platform_channel_binding.set"
	ActionPlatformChannelBindingRemove = "finance.platform_channel_binding.remove"
)

type BindingObservationStore interface {
	Get(context.Context, string, string) (ops.Observation, error)
}

type ChannelInventoryGate struct {
	Observations BindingObservationStore
	Now          func() time.Time
}

func (g ChannelInventoryGate) Verify(ctx context.Context, service BindingService, externalID string) error {
	if g.Observations == nil {
		return ErrBindingPrecondition
	}
	metricKey := ""
	switch service.ServiceType {
	case "sub2api":
		metricKey = "sub2api.channels.status"
	case "newapi":
		metricKey = "newapi.channels.status"
	default:
		return ErrBindingPrecondition
	}
	observation, err := g.Observations.Get(ctx, metricKey, service.Environment)
	if err != nil || observation.Source != service.InstanceID || observation.Status != ops.SyncOK {
		return ErrBindingPrecondition
	}
	now := time.Now().UTC()
	if g.Now != nil {
		now = g.Now().UTC()
	}
	freshness := observation.Freshness(now)
	if freshness.State == ops.StateStale || freshness.State == ops.StateFailed || freshness.State == ops.StateUninitialized {
		return ErrBindingPrecondition
	}
	completeness, ok := observation.Value["inventory_completeness"].(map[string]any)
	if !ok || completeness["complete"] != true || completeness["truncated"] == true {
		return ErrBindingPrecondition
	}
	items, ok := observation.Value["channels"].([]any)
	if !ok {
		return ErrBindingPrecondition
	}
	for _, raw := range items {
		row, ok := raw.(map[string]any)
		if ok && strings.TrimSpace(fmt.Sprint(row["channel_id"])) == externalID {
			return nil
		}
	}
	return ErrNotFound
}

func RegisterChannelBindingActions(
	registry *action.Registry,
	store *ChannelBindingStore,
	gate ChannelInventoryGate,
) error {
	definitions := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{channelBindingSetDefinition(), channelBindingSetHandler(store, gate)},
		{channelBindingRemoveDefinition(), channelBindingRemoveHandler(store, gate)},
	}
	for _, item := range definitions {
		if err := registry.Register(item.def, item.handler); err != nil {
			return fmt.Errorf("register %s: %w", item.def.ID, err)
		}
	}
	return nil
}

func channelBindingSetDefinition() action.Definition {
	return action.Definition{
		ID: ActionPlatformChannelBindingSet, Version: "1", RiskLevel: action.L1,
		Permission: ScopePlatformChannelBindingManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_id", Type: action.FieldString, Required: true},
			{Name: "external_channel_id", Type: action.FieldString, Required: true},
			{Name: "upstream_account_id", Type: action.FieldString, Required: true},
			{Name: "expected_binding_id", Type: action.FieldString},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   []string{"development", "staging", "production"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}
}

func channelBindingRemoveDefinition() action.Definition {
	return action.Definition{
		ID: ActionPlatformChannelBindingRemove, Version: "1", RiskLevel: action.L1,
		Permission: ScopePlatformChannelBindingManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_id", Type: action.FieldString, Required: true},
			{Name: "external_channel_id", Type: action.FieldString, Required: true},
			{Name: "expected_binding_id", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   []string{"development", "staging", "production"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}
}

func bindingPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "missing principal", nil)
	}
	return p, nil
}

func bindingActionError(err error) error {
	switch {
	case errors.Is(err, ErrBindingConflict):
		return action.NewError(action.CodeConflict, "channel binding changed; refresh and retry", err)
	case errors.Is(err, ErrBindingPrecondition):
		return action.NewError(action.CodePreconditionFailed, "channel inventory is not complete and fresh", err)
	case errors.Is(err, ErrNotFound):
		return action.NewError(action.CodeNotRegistered, "binding resource not found", err)
	case errors.Is(err, ErrMissingField), errors.Is(err, ErrInvalidFormat):
		return action.NewError(action.CodeInvalidParams, "binding parameters are invalid", err)
	default:
		return err
	}
}

func parseBindingCommon(params map[string]any) (ChannelRef, string, error) {
	serviceID, err := uuid.Parse(action.StringParam(params, "service_id"))
	if err != nil {
		return ChannelRef{}, "", action.NewError(action.CodeInvalidParams, "service_id is invalid", nil)
	}
	ref, err := NewChannelRef(serviceID, action.StringParam(params, "external_channel_id"))
	if err != nil {
		return ChannelRef{}, "", action.NewError(action.CodeInvalidParams, "external_channel_id is invalid", nil)
	}
	reason := strings.TrimSpace(action.StringParam(params, "reason"))
	if reason == "" {
		return ChannelRef{}, "", action.NewError(action.CodeInvalidParams, "reason is required", nil)
	}
	return ref, reason, nil
}

func channelBindingSetHandler(store *ChannelBindingStore, gate ChannelInventoryGate) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, errors.New("channel binding store missing")
		}
		p, err := bindingPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		ref, reason, err := parseBindingCommon(params)
		if err != nil {
			return nil, err
		}
		accountID, err := uuid.Parse(action.StringParam(params, "upstream_account_id"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "upstream_account_id is invalid", nil)
		}
		var expected *uuid.UUID
		if raw := action.StringParam(params, "expected_binding_id"); raw != "" {
			parsed, parseErr := uuid.Parse(raw)
			if parseErr != nil {
				return nil, action.NewError(action.CodeInvalidParams, "expected_binding_id is invalid", nil)
			}
			expected = &parsed
		}
		service, err := store.GetService(ctx, ref.ServiceID)
		if err != nil {
			return nil, bindingActionError(err)
		}
		if service.Environment != p.Environment {
			return nil, action.NewError(action.CodeEnvironmentMismatch, "service environment mismatch", nil)
		}
		if err := gate.Verify(ctx, service, ref.ExternalChannelID); err != nil {
			return nil, bindingActionError(err)
		}
		result, err := store.Set(ctx, SetBindingInput{
			Environment: p.Environment, Channel: ref, UpstreamAccountID: accountID,
			ExpectedBindingID: expected, Provenance: "manual", Reason: reason,
			CreatedBy: p.Issuer + ":" + p.ID,
		})
		if err != nil {
			return nil, bindingActionError(err)
		}
		action.RecordResource(ctx, "finance.platform_channel_binding", result.Binding.ID.String())
		action.RecordReason(ctx, reason)
		if result.Previous != nil {
			action.RecordBefore(ctx, bindingAuditSummary(*result.Previous))
		}
		action.RecordAfter(ctx, bindingAuditSummary(result.Binding))
		var previous any
		if result.Previous != nil {
			previous = result.Previous.ID.String()
		}
		return map[string]any{"binding_id": result.Binding.ID.String(), "previous_binding_id": previous, "changed": result.Changed}, nil
	}
}

func channelBindingRemoveHandler(store *ChannelBindingStore, gate ChannelInventoryGate) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, errors.New("channel binding store missing")
		}
		p, err := bindingPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		ref, reason, err := parseBindingCommon(params)
		if err != nil {
			return nil, err
		}
		expected, err := uuid.Parse(action.StringParam(params, "expected_binding_id"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "expected_binding_id is invalid", nil)
		}
		service, err := store.GetService(ctx, ref.ServiceID)
		if err != nil {
			return nil, bindingActionError(err)
		}
		if service.Environment != p.Environment {
			return nil, action.NewError(action.CodeEnvironmentMismatch, "service environment mismatch", nil)
		}
		if err := gate.Verify(ctx, service, ref.ExternalChannelID); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, bindingActionError(err)
		}
		removed, err := store.Remove(ctx, p.Environment, ref, expected, reason, p.Issuer+":"+p.ID)
		if err != nil {
			return nil, bindingActionError(err)
		}
		action.RecordResource(ctx, "finance.platform_channel_binding", removed.ID.String())
		action.RecordReason(ctx, reason)
		action.RecordBefore(ctx, bindingAuditSummary(removed))
		return map[string]any{"removed_binding_id": removed.ID.String(), "changed": true}, nil
	}
}

func bindingAuditSummary(binding PlatformChannelBinding) map[string]any {
	return map[string]any{
		"service_id":          binding.Channel.ServiceID.String(),
		"external_channel_id": binding.Channel.ExternalChannelID,
		"upstream_account_id": binding.UpstreamAccountID.String(),
		"valid_from":          binding.ValidFrom.UTC().Format(time.RFC3339Nano),
	}
}
