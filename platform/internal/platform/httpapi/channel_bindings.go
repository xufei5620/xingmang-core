package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type PlatformChannelBindingLister interface {
	GetService(context.Context, uuid.UUID) (finance.BindingService, error)
	ListActive(context.Context, uuid.UUID) ([]finance.PlatformChannelBinding, error)
	History(context.Context, finance.ChannelRef) ([]finance.PlatformChannelBinding, error)
	TokenEvidence(context.Context, string) ([]finance.TokenMapEvidence, error)
}

type bindingInventory struct {
	state         string
	source        string
	observedAt    *time.Time
	complete      bool
	truncated     bool
	reportedCount *int64
	fetchedCount  int64
	coverage      bool
	evidence      string
	channels      []finance.InventoryChannel
}

type platformChannelBindingServiceResponse struct {
	ID          string `json:"id"`
	ServiceType string `json:"service_type"`
	InstanceID  string `json:"instance_id"`
	Environment string `json:"environment"`
}

type platformChannelBindingInventoryResponse struct {
	State           string  `json:"state"`
	Source          string  `json:"source"`
	ObservedAt      *string `json:"observed_at"`
	Complete        bool    `json:"complete"`
	Truncated       bool    `json:"truncated"`
	ReportedCount   *int64  `json:"reported_count"`
	FetchedCount    int64   `json:"fetched_count"`
	CoveragePartial bool    `json:"coverage_partial"`
	Evidence        string  `json:"evidence"`
}

type channelBindingResponse struct {
	ID                string `json:"id"`
	UpstreamAccountID string `json:"upstream_account_id"`
	ValidFrom         string `json:"valid_from"`
	Provenance        string `json:"provenance"`
	Reason            string `json:"reason"`
	CreatedBy         string `json:"created_by"`
}

type channelCandidateResponse struct {
	State                     string   `json:"state"`
	EvidenceStatus            string   `json:"evidence_status"`
	UpstreamAccountIDs        []string `json:"upstream_account_ids"`
	ReasonCodes               []string `json:"reason_codes"`
	PlatformAssignmentMissing bool     `json:"platform_assignment_missing"`
	InventoryUnknown          bool     `json:"inventory_unknown"`
}

type platformChannelBindingItemResponse struct {
	ChannelRef struct {
		ServiceID         string `json:"service_id"`
		ExternalChannelID string `json:"external_channel_id"`
	} `json:"channel_ref"`
	ChannelName string                   `json:"channel_name"`
	Binding     *channelBindingResponse  `json:"binding"`
	Candidate   channelCandidateResponse `json:"candidate"`
	History     []channelBindingResponse `json:"history,omitempty"`
}

type platformChannelBindingPageResponse struct {
	Service    platformChannelBindingServiceResponse   `json:"service"`
	Inventory  platformChannelBindingInventoryResponse `json:"inventory"`
	Items      []platformChannelBindingItemResponse    `json:"items"`
	NextCursor *string                                 `json:"next_cursor"`
}

func parseBindingLimit(raw string) (int, error) {
	if raw == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须在 1 到 200 之间", err)
	}
	return n, nil
}

func parseBindingBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, action.NewError(action.CodeInvalidParams, "include_history 必须是 true 或 false", err)
	}
	return value, nil
}

func decodeBindingCursor(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || strings.TrimSpace(string(decoded)) == "" {
		return "", action.NewError(action.CodeInvalidParams, "cursor 无效", err)
	}
	return string(decoded), nil
}

func encodeBindingCursor(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// findChannelsObservation locates the single "<service_type>.channels.status"
// observation for this service instance. Shared by inventoryForService (binding
// candidate evaluation) and platform_channels.go's catalogRowsForService
// (XM-CHAN-FIELDS0 display fields) — both read the exact same worker-written
// observation, they just extract different subsets of its "channels" array.
func findChannelsObservation(observations []ops.Observation, service finance.BindingService) *ops.Observation {
	key := service.ServiceType + ".channels.status"
	for i := range observations {
		if observations[i].MetricKey == key && observations[i].Source == service.InstanceID {
			return &observations[i]
		}
	}
	return nil
}

func inventoryForService(observations []ops.Observation, service finance.BindingService) bindingInventory {
	found := findChannelsObservation(observations, service)
	if found == nil {
		return bindingInventory{state: "not_initialized", source: service.InstanceID, evidence: "no_observation"}
	}
	inventory := bindingInventory{state: "ok", source: found.Source, observedAt: found.ObservedAt, evidence: "observation"}
	if found.Status != ops.SyncOK {
		inventory.state = "failed"
		return inventory
	}
	if raw, ok := found.Value["inventory_completeness"].(map[string]any); ok {
		inventory.complete, _ = raw["complete"].(bool)
		inventory.truncated, _ = raw["truncated"].(bool)
		if n, ok := raw["reported_count"].(int64); ok {
			inventory.reportedCount = &n
		}
		if n, ok := raw["reported_count"].(float64); ok {
			v := int64(n)
			inventory.reportedCount = &v
		}
		if n, ok := raw["fetched_count"].(int64); ok {
			inventory.fetchedCount = n
		}
		if n, ok := raw["fetched_count"].(float64); ok {
			inventory.fetchedCount = int64(n)
		}
		if evidence, ok := raw["evidence"].(string); ok && evidence != "" {
			inventory.evidence = evidence
		}
	}
	inventory.coverage, _ = found.Value["coverage_partial"].(bool)
	if rawItems, ok := found.Value["channels"].([]any); ok {
		for _, raw := range rawItems {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(toString(row["channel_id"]))
			if id == "" {
				continue
			}
			inventory.channels = append(inventory.channels, finance.InventoryChannel{ExternalChannelID: id, Name: toString(row["name"])})
		}
	}
	if inventory.fetchedCount == 0 {
		inventory.fetchedCount = int64(len(inventory.channels))
	}
	return inventory
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func bindingResponse(binding finance.PlatformChannelBinding) channelBindingResponse {
	return channelBindingResponse{
		ID: binding.ID.String(), UpstreamAccountID: binding.UpstreamAccountID.String(),
		ValidFrom: binding.ValidFrom.UTC().Format(time.RFC3339Nano), Provenance: binding.Provenance,
		Reason: binding.Reason, CreatedBy: binding.CreatedBy,
	}
}

func ListPlatformChannelBindingsHandler(
	bindings PlatformChannelBindingLister,
	metrics MetricLister,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		serviceID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("service_id")))
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "service_id 无效", err))
			return
		}
		limit, err := parseBindingLimit(r.URL.Query().Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		includeHistory, err := parseBindingBool(r.URL.Query().Get("include_history"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		cursor, err := decodeBindingCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		service, err := bindings.GetService(r.Context(), serviceID)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeNotRegistered, "service 不存在", err))
			return
		}
		if service.Environment != string(env) {
			WriteError(w, r, action.NewError(action.CodeEnvironmentMismatch, "service environment mismatch", nil))
			return
		}
		active, err := bindings.ListActive(r.Context(), serviceID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		observations, err := metrics.ListByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		inventory := inventoryForService(observations, service)
		evidence, err := bindings.TokenEvidence(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		confirmed := append([]finance.PlatformChannelBinding(nil), active...)
		candidates := finance.EvaluateBindingCandidates(finance.InventorySnapshot{ServiceID: serviceID, ServiceType: service.ServiceType, Known: inventory.state != "not_initialized" && inventory.state != "failed", Complete: inventory.complete && !inventory.truncated, Channels: inventory.channels}, confirmed, evidence)
		if cursor != "" {
			filtered := candidates[:0]
			for _, candidate := range candidates {
				if candidate.Channel.ExternalChannelID > cursor {
					filtered = append(filtered, candidate)
				}
			}
			candidates = filtered
		}
		var next *string
		if len(candidates) > limit {
			value := encodeBindingCursor(candidates[limit-1].Channel.ExternalChannelID)
			next = &value
			candidates = candidates[:limit]
		}
		items := make([]platformChannelBindingItemResponse, 0, len(candidates))
		for _, candidate := range candidates {
			item := platformChannelBindingItemResponse{ChannelName: candidate.Name, Candidate: channelCandidateResponse{State: string(candidate.State), EvidenceStatus: string(candidate.EvidenceStatus), ReasonCodes: append([]string(nil), candidate.ReasonCodes...), PlatformAssignmentMissing: candidate.PlatformAssignmentMissing, InventoryUnknown: candidate.InventoryUnknown}}
			item.ChannelRef.ServiceID, item.ChannelRef.ExternalChannelID = serviceID.String(), candidate.Channel.ExternalChannelID
			for _, id := range candidate.UpstreamAccountIDs {
				item.Candidate.UpstreamAccountIDs = append(item.Candidate.UpstreamAccountIDs, id.String())
			}
			for _, binding := range active {
				if binding.Channel.ExternalChannelID == candidate.Channel.ExternalChannelID {
					value := bindingResponse(binding)
					item.Binding = &value
					break
				}
			}
			if includeHistory {
				history, historyErr := bindings.History(r.Context(), candidate.Channel)
				if historyErr != nil {
					WriteError(w, r, historyErr)
					return
				}
				item.History = make([]channelBindingResponse, 0, len(history))
				for _, binding := range history {
					item.History = append(item.History, bindingResponse(binding))
				}
			}
			items = append(items, item)
		}
		var observed *string
		if inventory.observedAt != nil {
			value := inventory.observedAt.UTC().Format(time.RFC3339Nano)
			observed = &value
		}
		WriteJSON(w, http.StatusOK, platformChannelBindingPageResponse{Service: platformChannelBindingServiceResponse{ID: service.ID.String(), ServiceType: service.ServiceType, InstanceID: service.InstanceID, Environment: service.Environment}, Inventory: platformChannelBindingInventoryResponse{State: inventory.state, Source: inventory.source, ObservedAt: observed, Complete: inventory.complete, Truncated: inventory.truncated, ReportedCount: inventory.reportedCount, FetchedCount: inventory.fetchedCount, CoveragePartial: inventory.coverage, Evidence: inventory.evidence}, Items: items, NextCursor: next})
	}
}
