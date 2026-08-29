package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type platformChannelRow struct {
	ChannelRef struct {
		ServiceID         string `json:"service_id"`
		ExternalChannelID string `json:"external_channel_id"`
	} `json:"channel_ref"`
	Name           string                   `json:"name"`
	Binding        *channelBindingResponse  `json:"binding"`
	Candidate      channelCandidateResponse `json:"candidate"`
	Economics      any                      `json:"economics"`
	EconomicsState string                   `json:"economics_state"`
	Conflicts      []string                 `json:"conflicts"`
	Health         any                      `json:"health"`
	Models         any                      `json:"models"`
	Assurance      any                      `json:"assurance"`
	Runway         any                      `json:"runway"`
	Observed       map[string]any           `json:"observed"`
}

type platformChannelInventoryResponse struct {
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

type platformChannelsResponse struct {
	Service        platformChannelBindingServiceResponse `json:"service"`
	Inventory      platformChannelInventoryResponse      `json:"inventory"`
	From           string                                `json:"from"`
	To             string                                `json:"to"`
	Items          []platformChannelRow                  `json:"items"`
	RunwayCoverage map[string]any                        `json:"runway_coverage"`
	NextCursor     *string                               `json:"next_cursor"`
}

const platformChannelDateLayout = "2006-01-02"

// parseBusinessDayRange applies the frozen channel-query window contract. Both
// dates omitted means the current UTC business day; providing only one date is
// ambiguous and must be rejected. The inclusive window is capped at 92 days so
// a single read cannot turn the channel page into an unbounded ledger export.
func parseBusinessDayRange(rawFrom, rawTo string) (string, string, error) {
	if (rawFrom == "") != (rawTo == "") {
		return "", "", action.NewError(action.CodeInvalidParams, "from/to 必须同时提供", nil)
	}
	if rawFrom == "" {
		today := time.Now().UTC().Format(platformChannelDateLayout)
		return today, today, nil
	}
	from, err := time.Parse(platformChannelDateLayout, rawFrom)
	if err != nil {
		return "", "", action.NewError(action.CodeInvalidParams, "from 日期必须是 YYYY-MM-DD", err)
	}
	to, err := time.Parse(platformChannelDateLayout, rawTo)
	if err != nil {
		return "", "", action.NewError(action.CodeInvalidParams, "to 日期必须是 YYYY-MM-DD", err)
	}
	if to.Before(from) {
		return "", "", action.NewError(action.CodeInvalidParams, "to 不能早于 from", nil)
	}
	if to.Sub(from)/(24*time.Hour)+1 > 92 {
		return "", "", action.NewError(action.CodeInvalidParams, "日期窗口不能超过 92 天", nil)
	}
	return rawFrom, rawTo, nil
}

func parseBusinessDayParam(raw string) (string, error) {
	if raw == "" {
		return time.Now().UTC().Format(platformChannelDateLayout), nil
	}
	if _, err := time.Parse(platformChannelDateLayout, raw); err != nil {
		return "", action.NewError(action.CodeInvalidParams, "日期必须是 YYYY-MM-DD", err)
	}
	return raw, nil
}

func platformChannelHealth(inventory bindingInventory, id string, platform string) map[string]any {
	// The inventory parser intentionally keeps only identity fields. Re-read the raw row here
	// to expose platform-specific health without inventing a shared schema.
	for _, channel := range inventory.channels {
		if channel.ExternalChannelID == id {
			return map[string]any{"state": "observed", "name": channel.Name, "platform": platform}
		}
	}
	return nil
}

func ListPlatformChannelsHandler(
	bindings PlatformChannelBindingLister,
	metrics MetricLister,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		platform := chi.URLParam(r, "platform")
		if platform != "sub2api" && platform != "newapi" {
			WriteError(w, r, action.NewError(action.CodeNotRegistered, "平台不支持渠道目录", nil))
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
		from, to, err := parseBusinessDayRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseBindingLimit(r.URL.Query().Get("limit"))
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
		if service.Environment != string(env) || service.ServiceType != platform {
			WriteError(w, r, action.NewError(action.CodeEnvironmentMismatch, "service 与平台或环境不匹配", nil))
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
		candidates := finance.EvaluateBindingCandidates(finance.InventorySnapshot{ServiceID: serviceID, ServiceType: service.ServiceType, Known: inventory.state != "not_initialized" && inventory.state != "failed", Complete: inventory.complete && !inventory.truncated, Channels: inventory.channels}, active, evidence)
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
		rows := make([]platformChannelRow, 0, len(candidates))
		for _, candidate := range candidates {
			row := platformChannelRow{Name: candidate.Name, Candidate: channelCandidateResponse{State: string(candidate.State), EvidenceStatus: string(candidate.EvidenceStatus), ReasonCodes: append([]string(nil), candidate.ReasonCodes...), PlatformAssignmentMissing: candidate.PlatformAssignmentMissing, InventoryUnknown: candidate.InventoryUnknown}, EconomicsState: string(candidate.State), Conflicts: append([]string(nil), candidate.ReasonCodes...), Health: platformChannelHealth(inventory, candidate.Channel.ExternalChannelID, platform)}
			row.ChannelRef.ServiceID, row.ChannelRef.ExternalChannelID = serviceID.String(), candidate.Channel.ExternalChannelID
			for _, id := range candidate.UpstreamAccountIDs {
				row.Candidate.UpstreamAccountIDs = append(row.Candidate.UpstreamAccountIDs, id.String())
			}
			for _, binding := range active {
				if binding.Channel.ExternalChannelID == candidate.Channel.ExternalChannelID {
					value := bindingResponse(binding)
					row.Binding = &value
					row.EconomicsState = "binding_pending_economics"
					break
				}
			}
			if platform == "sub2api" {
				row.Models = nil
				row.Assurance = nil
			} else {
				row.Models = map[string]any{"state": "model_names_not_integrated", "count": nil}
				row.Assurance = nil
			}
			observed := map[string]any{"source": inventory.source, "observed_at": nil, "is_stale": inventory.state != "ok"}
			if inventory.observedAt != nil {
				observed["observed_at"] = inventory.observedAt.UTC().Format(time.RFC3339Nano)
			}
			row.Observed = observed
			rows = append(rows, row)
		}
		var observed *string
		if inventory.observedAt != nil {
			value := inventory.observedAt.UTC().Format(time.RFC3339Nano)
			observed = &value
		}
		WriteJSON(w, http.StatusOK, platformChannelsResponse{Service: platformChannelBindingServiceResponse{ID: service.ID.String(), ServiceType: service.ServiceType, InstanceID: service.InstanceID, Environment: service.Environment}, Inventory: platformChannelInventoryResponse{State: inventory.state, Source: inventory.source, ObservedAt: observed, Complete: inventory.complete, Truncated: inventory.truncated, ReportedCount: inventory.reportedCount, FetchedCount: inventory.fetchedCount, CoveragePartial: inventory.coverage, Evidence: inventory.evidence}, From: from, To: to, Items: rows, RunwayCoverage: map[string]any{"total": 0, "known": 0, "reasons": map[string]int{"not_loaded": 1}}, NextCursor: next})
	}
}

// Keep strconv referenced in generated API docs and make future numeric health projections use
// the same parser without changing the public handler signature.
var _ = strconv.Itoa
