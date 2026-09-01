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
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// platformChannelCapacityResponse/…UsageWindowResponse are the nested v3
// catalog objects (XM-CHAN-FIELDS0). Every field is individually nullable:
// the connectors already omit a whole dimension from the observation when
// nothing backs it (see sub2api/newapi catalogFields), and this layer stays
// honest about that rather than inventing a zero or an empty string.
type platformChannelCapacityResponse struct {
	Used  *int64 `json:"used"`
	Limit *int64 `json:"limit"`
}

type platformChannelSchedulingResponse struct {
	Enabled  *bool  `json:"enabled"`
	Priority *int64 `json:"priority"`
}

type platformChannelTodayResponse struct {
	Requests    *int64   `json:"requests"`
	SuccessRate *float64 `json:"success_rate"`
	// CostMinor follows the established money-as-decimal-string convention
	// (see amountString/amountBody in payments.go/users.go): JS numbers are
	// float64 and silently lose precision past 2^53, so money never rides a
	// bare JSON number in this API.
	CostMinor *string `json:"cost_minor"`
	Currency  *string `json:"currency"`
	Scale     *int    `json:"scale"`
}

type platformChannelUsageWindowResponse struct {
	UsedRatio *float64 `json:"used_ratio"`
	ResetsAt  *string  `json:"resets_at"`
}

type platformChannelRow struct {
	ChannelRef struct {
		ServiceID         string `json:"service_id"`
		ExternalChannelID string `json:"external_channel_id"`
	} `json:"channel_ref"`
	// ID is the upstream's own account/channel id — identical value to
	// ChannelRef.ExternalChannelID, exposed as a top-level field because
	// XM-CHAN-FIELDS0 chanmerge codes against exactly this JSON name.
	ID   string `json:"id"`
	Name string `json:"name"`

	// XM-CHAN-FIELDS0 catalog fields. See contracts/connectors/
	// sub2api.channel-catalog.v3.md and newapi.channel-catalog.v3.md for the
	// field-by-field upstream source of each; anything not traceable to a
	// real upstream field stays nil here, never approximated.
	Kind               *string                             `json:"kind"`
	Vendor             *string                             `json:"vendor"`
	Capacity           *platformChannelCapacityResponse    `json:"capacity"`
	Status             *string                             `json:"status"`
	Scheduling         *platformChannelSchedulingResponse  `json:"scheduling"`
	Today              *platformChannelTodayResponse       `json:"today"`
	UsageWindow        *platformChannelUsageWindowResponse `json:"usage_window"`
	Proxy              *string                             `json:"proxy"`
	RateMultiplier     *float64                            `json:"rate_multiplier"`
	UpstreamMultiplier *float64                            `json:"upstream_multiplier"`
	LastUsedAt         *string                             `json:"last_used_at"`
	CreatedAt          *string                             `json:"created_at"`
	ExpiresAt          *string                             `json:"expires_at"`

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

// platformChannelCatalog is the httpapi-internal decode of one connector's
// v3 catalog row (the map[string]any nested inside the "channels" array of
// the "<service_type>.channels.status" observation — see
// sub2api/newapi ToChannelDirectoryObservation). It exists only to carry
// values from catalogRowsForService to the row-building loop below; the
// JSON-facing shape is platformChannelRow and its nested Response types.
type platformChannelCatalog struct {
	kind                 *string
	vendor               *string
	capacityUsed         *int64
	capacityLimit        *int64
	status               *string
	schedulingEnabled    *bool
	schedulingPriority   *int64
	todayRequests        *int64
	todaySuccessRate     *float64
	todayCostMinorUnits  *int64
	todayCurrency        *string
	todayScale           *int
	usageWindowUsedRatio *float64
	usageWindowResetsAt  *string
	proxy                *string
	rateMultiplier       *float64
	upstreamMultiplier   *float64
	lastUsedAt           *string
	createdAt            *string
	expiresAt            *string
}

// toInt64FromAny coerces a decoded observation value into *int64. Values
// arrive as native Go int64 (Fake path, kept in memory) or as float64 (any
// path that round-trips the observation through real JSON, e.g. Postgres
// JSONB) — the same dual-type defensiveness already used by
// inventoryForService for reported_count/fetched_count.
func toInt64FromAny(v any) *int64 {
	switch n := v.(type) {
	case int64:
		return &n
	case float64:
		i := int64(n)
		return &i
	case int:
		i := int64(n)
		return &i
	default:
		return nil
	}
}

func toIntFromAny(v any) *int {
	switch n := v.(type) {
	case int:
		return &n
	case int64:
		i := int(n)
		return &i
	case float64:
		i := int(n)
		return &i
	default:
		return nil
	}
}

func toFloat64FromAny(v any) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case int64:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	default:
		return nil
	}
}

func toBoolFromAny(v any) *bool {
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

func toStringFromAny(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

// catalogRowsForService decodes the v3 catalog fields (XM-CHAN-FIELDS0) for
// every channel in the service's latest directory observation, keyed by
// external channel id. It reads the exact same observation as
// inventoryForService (via the shared findChannelsObservation) but keeps a
// richer per-row decode — bindingInventory intentionally stays narrow
// (identity fields only) because finance.EvaluateBindingCandidates doesn't
// need or want the display-only catalog fields, and channel_bindings.go's
// own handler has no reason to carry them either.
func catalogRowsForService(observations []ops.Observation, service finance.BindingService) map[string]platformChannelCatalog {
	out := map[string]platformChannelCatalog{}
	found := findChannelsObservation(observations, service)
	if found == nil {
		return out
	}
	rawItems, ok := found.Value["channels"].([]any)
	if !ok {
		return out
	}
	for _, raw := range rawItems {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(toString(row["channel_id"]))
		if id == "" {
			continue
		}
		var c platformChannelCatalog
		c.status = toStringFromAny(row["status"])
		c.kind = toStringFromAny(row["kind"])
		c.vendor = toStringFromAny(row["vendor"])
		if capacity, ok := row["capacity"].(map[string]any); ok {
			c.capacityUsed = toInt64FromAny(capacity["used"])
			c.capacityLimit = toInt64FromAny(capacity["limit"])
		}
		if scheduling, ok := row["scheduling"].(map[string]any); ok {
			c.schedulingEnabled = toBoolFromAny(scheduling["enabled"])
			c.schedulingPriority = toInt64FromAny(scheduling["priority"])
		}
		if today, ok := row["today"].(map[string]any); ok {
			c.todayRequests = toInt64FromAny(today["requests"])
			c.todaySuccessRate = toFloat64FromAny(today["success_rate"])
			c.todayCostMinorUnits = toInt64FromAny(today["cost_minor_units"])
			c.todayCurrency = toStringFromAny(today["currency"])
			c.todayScale = toIntFromAny(today["scale"])
		}
		if window, ok := row["usage_window"].(map[string]any); ok {
			c.usageWindowUsedRatio = toFloat64FromAny(window["used_ratio"])
			c.usageWindowResetsAt = toStringFromAny(window["resets_at"])
		}
		c.proxy = toStringFromAny(row["proxy"])
		c.rateMultiplier = toFloat64FromAny(row["rate_multiplier"])
		c.upstreamMultiplier = toFloat64FromAny(row["upstream_multiplier"])
		c.lastUsedAt = toStringFromAny(row["last_used_at"])
		c.createdAt = toStringFromAny(row["created_at"])
		c.expiresAt = toStringFromAny(row["expires_at"])
		out[id] = c
	}
	return out
}

// applyCatalog fills the v3 catalog fields (XM-CHAN-FIELDS0) on a
// platformChannelRow from the decoded observation row. Absent in the
// observation stays nil in the response — never a fabricated zero or "".
func applyCatalog(row *platformChannelRow, c platformChannelCatalog) {
	row.Kind = c.kind
	row.Vendor = c.vendor
	row.Status = c.status
	row.Proxy = c.proxy
	row.RateMultiplier = c.rateMultiplier
	row.UpstreamMultiplier = c.upstreamMultiplier
	row.LastUsedAt = c.lastUsedAt
	row.CreatedAt = c.createdAt
	row.ExpiresAt = c.expiresAt
	if c.capacityUsed != nil || c.capacityLimit != nil {
		row.Capacity = &platformChannelCapacityResponse{Used: c.capacityUsed, Limit: c.capacityLimit}
	}
	if c.schedulingEnabled != nil || c.schedulingPriority != nil {
		row.Scheduling = &platformChannelSchedulingResponse{Enabled: c.schedulingEnabled, Priority: c.schedulingPriority}
	}
	if c.todayRequests != nil || c.todaySuccessRate != nil || c.todayCostMinorUnits != nil {
		today := &platformChannelTodayResponse{
			Requests: c.todayRequests, SuccessRate: c.todaySuccessRate,
			Currency: c.todayCurrency, Scale: c.todayScale,
		}
		if c.todayCostMinorUnits != nil {
			today.CostMinor = amountString(*c.todayCostMinorUnits)
		}
		row.Today = today
	}
	if c.usageWindowUsedRatio != nil || c.usageWindowResetsAt != nil {
		row.UsageWindow = &platformChannelUsageWindowResponse{
			UsedRatio: c.usageWindowUsedRatio, ResetsAt: c.usageWindowResetsAt,
		}
	}
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
		catalog := catalogRowsForService(observations, service)
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
			row.ID = candidate.Channel.ExternalChannelID
			applyCatalog(&row, catalog[candidate.Channel.ExternalChannelID])
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
