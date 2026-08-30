package connector

// This file contains the immutable, in-process half of the connector budget
// contract.  It deliberately has no network, database, clock or credential
// dependencies.  R215-2 adds the source authority; R215-1 only validates a
// literal policy and exposes a safe route inventory.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	PolicyVersionV1 = 1

	CostClassProbe             = "probe"
	CostClassLight             = "light"
	CostClassPage              = "page"
	CostClassCountHeavy        = "count-heavy"
	CostClassDestructive       = "destructive-consume"
	CostClassWrite             = "write"
	CursorModeNone             = "none"
	CursorModePage             = "page"
	CursorModeCursor           = "cursor"
	CoveragePolicyComplete     = "complete"
	CoveragePolicyPartial      = "partial"
	defaultSchedulerTickSecs   = int64(300)
	defaultBaseIntervalSecs    = int64(300)
	defaultMaxIntervalSecs     = int64(3600)
	defaultMaxRunMillis        = int64(20_000)
	defaultMaxConcurrentSource = int64(1)
)

var (
	allowedCostClasses = map[string]struct{}{
		CostClassProbe: {}, CostClassLight: {}, CostClassPage: {},
		CostClassCountHeavy: {}, CostClassDestructive: {}, CostClassWrite: {},
	}
	allowedCursorModes = map[string]struct{}{
		CursorModeNone: {}, CursorModePage: {}, CursorModeCursor: {},
	}
	allowedCoveragePolicies = map[string]struct{}{
		CoveragePolicyComplete: {}, CoveragePolicyPartial: {},
	}
)

// RouteSpec is the non-secret identity of one outbound route.  PathTemplate
// is intentionally a path template, never a host, query string or credential.
type RouteSpec struct {
	ConnectorType string `json:"connector_type"`
	Capability    string `json:"capability"`
	RouteID       string `json:"route_id"`
	Method        string `json:"method"`
	PathTemplate  string `json:"path_template"`
}

// RouteCost describes the cost and response ceilings for one route.
type RouteCost struct {
	RouteID          string `json:"route_id"`
	Method           string `json:"method"`
	PathTemplate     string `json:"path_template"`
	CostUnits        int64  `json:"cost_units"`
	MaxResponseBytes int64  `json:"max_response_bytes"`
	MaxRows          int64  `json:"max_rows"`
}

func (r RouteSpec) Validate() error {
	if strings.TrimSpace(r.ConnectorType) == "" || strings.TrimSpace(r.Capability) == "" || strings.TrimSpace(r.RouteID) == "" {
		return errors.New("route requires connector_type, capability and route_id")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		return errors.New("route method must be GET or HEAD")
	}
	if !strings.HasPrefix(r.PathTemplate, "/") || strings.ContainsAny(r.PathTemplate, "?\r\n\t") {
		return errors.New("route path template must be a path without query/control characters")
	}
	return nil
}

type RetryPolicy struct {
	MaxAttempts      int      `json:"max_attempts"`
	InitialBackoffMS int64    `json:"initial_backoff_ms"`
	MaxBackoffMS     int64    `json:"max_backoff_ms"`
	MaxRetryAfterMS  int64    `json:"max_retry_after_ms"`
	JitterPPM        int32    `json:"jitter_ppm"`
	RetryKinds       []string `json:"retry_kinds"`
}

type PollPolicy struct {
	SchedulerTickSeconds         int64 `json:"scheduler_tick_seconds"`
	BaseIntervalSeconds          int64 `json:"base_interval_seconds"`
	MinIntervalSeconds           int64 `json:"min_interval_seconds"`
	MaxIntervalSeconds           int64 `json:"max_interval_seconds"`
	UnchangedStepsBeforeSlowdown int   `json:"unchanged_steps_before_slowdown"`
	SuccessStepsBeforeRecovery   int   `json:"success_steps_before_recovery"`
	JitterPPM                    int32 `json:"jitter_ppm"`
}

// CapabilityBudget is a complete finite budget for one capability run.
type CapabilityBudget struct {
	ConnectorType       string      `json:"connector_type"`
	Capability          string      `json:"capability"`
	ProviderScopeRule   string      `json:"provider_scope_rule"`
	CostClass           string      `json:"cost_class"`
	MinConnectorVersion string      `json:"min_connector_version"`
	MaxConnectorVersion string      `json:"max_connector_version"`
	Routes              []RouteCost `json:"routes"`
	MaxRequests         int64       `json:"max_requests"`
	MaxPages            int64       `json:"max_pages"`
	MaxRows             int64       `json:"max_rows"`
	MaxBytes            int64       `json:"max_bytes"`
	MaxCostUnits        int64       `json:"max_cost_units"`
	MaxRunMillis        int64       `json:"max_run_millis"`
	MaxConcurrentSource int64       `json:"max_concurrent_source"`
	CursorMode          string      `json:"cursor_mode"`
	SingleFlightKey     string      `json:"single_flight_key"`
	CoveragePolicy      string      `json:"coverage_policy"`
	// Disabled marks a known contract capability with no approved outbound
	// route yet.  It remains visible for inventory/audit but cannot be used to
	// construct a guard, so it is fail-closed rather than silently omitted.
	Disabled bool        `json:"disabled,omitempty"`
	Retry    RetryPolicy `json:"retry"`
	Poll     PollPolicy  `json:"poll"`
}

// BudgetPolicyV1 is the top-level immutable policy document.
type BudgetPolicyV1 struct {
	PolicyVersion int                `json:"policy_version"`
	Capabilities  []CapabilityBudget `json:"capabilities"`
}

// ConnectorBudgetPolicyV1 is the longer name used by the design document.
type ConnectorBudgetPolicyV1 = BudgetPolicyV1

// registeredRouteSpecs is deliberately sorted by RouteID before exposure.
// The paths mirror the current read-only connectors and contain no endpoint
// host/query values.  A route used for two capabilities receives two distinct
// route IDs (for example the Sub2API accounts endpoint) so each budget remains
// independently auditable.
var registeredRouteSpecs = []RouteSpec{
	{ConnectorType: "sub2api", Capability: "sub2api.health.read", RouteID: "sub2api.health", Method: "GET", PathTemplate: "/health"},
	{ConnectorType: "sub2api", Capability: "sub2api.service.version_read", RouteID: "sub2api.version", Method: "GET", PathTemplate: "/api/v1/admin/system/version"},
	{ConnectorType: "sub2api", Capability: "sub2api.users.read", RouteID: "sub2api.dashboard.stats", Method: "GET", PathTemplate: "/api/v1/admin/dashboard/stats"},
	{ConnectorType: "sub2api", Capability: "sub2api.users.balance_read", RouteID: "sub2api.users", Method: "GET", PathTemplate: "/api/v1/admin/users"},
	{ConnectorType: "sub2api", Capability: "sub2api.orders.read", RouteID: "sub2api.payment.dashboard", Method: "GET", PathTemplate: "/api/v1/admin/payment/dashboard"},
	{ConnectorType: "sub2api", Capability: "sub2api.orders.read", RouteID: "sub2api.dashboard.trend", Method: "GET", PathTemplate: "/api/v1/admin/dashboard/trend"},
	{ConnectorType: "sub2api", Capability: "sub2api.accounts.read", RouteID: "sub2api.accounts", Method: "GET", PathTemplate: "/api/v1/admin/accounts"},
	{ConnectorType: "sub2api", Capability: "sub2api.channels.balance_read", RouteID: "sub2api.channels.balance", Method: "GET", PathTemplate: "/api/v1/admin/accounts"},
	{ConnectorType: "newapi", Capability: "newapi.service.version_read", RouteID: "newapi.status.version", Method: "GET", PathTemplate: "/api/status"},
	{ConnectorType: "newapi", Capability: "newapi.health.read", RouteID: "newapi.status.health", Method: "GET", PathTemplate: "/api/status"},
	{ConnectorType: "newapi", Capability: "newapi.users.read", RouteID: "newapi.users", Method: "GET", PathTemplate: "/api/user/"},
	{ConnectorType: "newapi", Capability: "newapi.orders.read", RouteID: "newapi.topups", Method: "GET", PathTemplate: "/api/user/topup"},
	{ConnectorType: "newapi", Capability: "newapi.channels.read", RouteID: "newapi.channels", Method: "GET", PathTemplate: "/api/channel/"},
	{ConnectorType: "newapi", Capability: "newapi.errors.read", RouteID: "newapi.logs", Method: "GET", PathTemplate: "/api/log/"},
	{ConnectorType: "newapi", Capability: "newapi.models.usage_read", RouteID: "newapi.data", Method: "GET", PathTemplate: "/api/data/"},
	{ConnectorType: "metering", Capability: "metering.health.read", RouteID: "metering.sub2api.health", Method: "GET", PathTemplate: "/health"},
	{ConnectorType: "metering", Capability: "metering.service.version_read", RouteID: "metering.sub2api.version", Method: "GET", PathTemplate: "/api/v1/admin/system/version"},
	{ConnectorType: "metering", Capability: "metering.token.usage_read", RouteID: "metering.sub2api.token.usage", Method: "GET", PathTemplate: "/v1/usage"},
	{ConnectorType: "metering", Capability: "metering.account.revenue_read", RouteID: "metering.sub2api.account.revenue", Method: "GET", PathTemplate: "/api/v1/admin/accounts/{account_id}/stats"},
	{ConnectorType: "metering", Capability: "metering.health.read", RouteID: "metering.newapi.status.health", Method: "GET", PathTemplate: "/api/status"},
	{ConnectorType: "metering", Capability: "metering.service.version_read", RouteID: "metering.newapi.status.version", Method: "GET", PathTemplate: "/api/status"},
	{ConnectorType: "metering", Capability: "metering.token.usage_read", RouteID: "metering.newapi.token.usage", Method: "GET", PathTemplate: "/api/log/self/stat"},
}

// RegisteredRouteSpecs returns a defensive, deterministic copy.
func RegisteredRouteSpecs() []RouteSpec {
	out := append([]RouteSpec(nil), registeredRouteSpecs...)
	sort.Slice(out, func(i, j int) bool { return out[i].RouteID < out[j].RouteID })
	return out
}

func routeSpecMap() map[string]RouteSpec {
	out := make(map[string]RouteSpec, len(registeredRouteSpecs))
	for _, route := range registeredRouteSpecs {
		out[route.RouteID] = route
	}
	return out
}

// Validate checks all finite bounds and rejects unknown policy vocabulary.
func (p BudgetPolicyV1) Validate() error {
	if p.PolicyVersion != PolicyVersionV1 {
		return fmt.Errorf("unsupported connector budget policy version %d", p.PolicyVersion)
	}
	if len(p.Capabilities) == 0 {
		return errors.New("connector budget policy has no capabilities")
	}
	routes := routeSpecMap()
	seenCaps := make(map[string]struct{}, len(p.Capabilities))
	seenRoutes := make(map[string]string)
	for i, cap := range p.Capabilities {
		if err := validateCapability(cap, i, routes, seenCaps, seenRoutes); err != nil {
			return err
		}
	}
	for _, spec := range registeredRouteSpecs {
		if owner, ok := seenRoutes[spec.RouteID]; !ok {
			return fmt.Errorf("registered route %q has no policy", spec.RouteID)
		} else if owner == "" {
			return fmt.Errorf("registered route %q has empty capability", spec.RouteID)
		}
	}
	return nil
}

func validateCapability(cap CapabilityBudget, index int, routes map[string]RouteSpec, seenCaps map[string]struct{}, seenRoutes map[string]string) error {
	if strings.TrimSpace(cap.ConnectorType) == "" || strings.TrimSpace(cap.Capability) == "" {
		return fmt.Errorf("capability[%d] requires connector_type and capability", index)
	}
	if cap.ConnectorType != "sub2api" && cap.ConnectorType != "newapi" && cap.ConnectorType != "metering" {
		return fmt.Errorf("capability %q has unknown connector type", cap.Capability)
	}
	if _, err := registry.ParseCapability(cap.Capability); err != nil {
		return fmt.Errorf("capability %q is not registered: %w", cap.Capability, err)
	}
	if _, ok := seenCaps[cap.Capability]; ok {
		return fmt.Errorf("duplicate capability %q", cap.Capability)
	}
	seenCaps[cap.Capability] = struct{}{}
	if strings.TrimSpace(cap.ProviderScopeRule) == "" || strings.ContainsAny(cap.ProviderScopeRule, "\r\n\t ") {
		return fmt.Errorf("capability %q has invalid provider scope rule", cap.Capability)
	}
	if _, ok := allowedCostClasses[cap.CostClass]; !ok {
		return fmt.Errorf("capability %q has unknown cost class %q", cap.Capability, cap.CostClass)
	}
	if cap.CostClass == CostClassDestructive || cap.CostClass == CostClassWrite {
		return fmt.Errorf("capability %q cost class %q is denied in policy v1", cap.Capability, cap.CostClass)
	}
	if strings.TrimSpace(cap.MinConnectorVersion) == "" || strings.TrimSpace(cap.MaxConnectorVersion) == "" {
		return fmt.Errorf("capability %q has unknown connector version range", cap.Capability)
	}
	parsedCapability, _ := registry.ParseCapability(cap.Capability)
	if parsedCapability.IsWrite() {
		return fmt.Errorf("capability %q is a write capability", cap.Capability)
	}
	if !strings.HasPrefix(cap.Capability, cap.ConnectorType+".") && cap.ConnectorType != "metering" {
		return fmt.Errorf("capability %q does not belong to connector %q", cap.Capability, cap.ConnectorType)
	}
	if cap.MaxRequests <= 0 || cap.MaxPages <= 0 || cap.MaxRows <= 0 || cap.MaxBytes <= 0 || cap.MaxCostUnits <= 0 || cap.MaxRunMillis <= 0 || cap.MaxConcurrentSource <= 0 {
		return fmt.Errorf("capability %q has a zero or negative hard cap", cap.Capability)
	}
	if _, ok := allowedCursorModes[cap.CursorMode]; !ok {
		return fmt.Errorf("capability %q has unknown cursor mode %q", cap.Capability, cap.CursorMode)
	}
	if _, ok := allowedCoveragePolicies[cap.CoveragePolicy]; !ok {
		return fmt.Errorf("capability %q has unknown coverage policy %q", cap.Capability, cap.CoveragePolicy)
	}
	if strings.TrimSpace(cap.SingleFlightKey) == "" {
		return fmt.Errorf("capability %q has empty single-flight key", cap.Capability)
	}
	if err := validateRetry(cap.Retry, cap.MaxRequests, cap.MaxCostUnits); err != nil {
		return fmt.Errorf("capability %q retry policy: %w", cap.Capability, err)
	}
	if err := validatePoll(cap.Poll); err != nil {
		return fmt.Errorf("capability %q poll policy: %w", cap.Capability, err)
	}
	if len(cap.Routes) == 0 && !cap.Disabled {
		return fmt.Errorf("capability %q has no routes", cap.Capability)
	}
	if cap.Disabled {
		if len(cap.Routes) != 0 {
			return fmt.Errorf("disabled capability %q must not contain routes", cap.Capability)
		}
		return nil
	}
	for j, route := range cap.Routes {
		spec, ok := routes[route.RouteID]
		if !ok {
			return fmt.Errorf("capability %q route[%d] %q is unknown", cap.Capability, j, route.RouteID)
		}
		if spec.Capability != cap.Capability || spec.ConnectorType != cap.ConnectorType {
			return fmt.Errorf("route %q does not belong to capability %q", route.RouteID, cap.Capability)
		}
		if previous, ok := seenRoutes[route.RouteID]; ok {
			return fmt.Errorf("route %q appears in %q and %q", route.RouteID, previous, cap.Capability)
		}
		seenRoutes[route.RouteID] = cap.Capability
		if route.Method != spec.Method || route.PathTemplate != spec.PathTemplate {
			return fmt.Errorf("route %q method/path differs from registered inventory", route.RouteID)
		}
		if route.Method != "GET" && route.Method != "HEAD" {
			return fmt.Errorf("route %q uses non-read method", route.RouteID)
		}
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("route %q inventory is invalid: %w", route.RouteID, err)
		}
		if route.CostUnits <= 0 || route.MaxResponseBytes <= 0 || route.MaxRows <= 0 {
			return fmt.Errorf("route %q has a zero or negative cap", route.RouteID)
		}
		if route.CostUnits > cap.MaxCostUnits || route.MaxRows > cap.MaxRows || route.MaxResponseBytes > cap.MaxBytes {
			return fmt.Errorf("route %q exceeds capability cap", route.RouteID)
		}
	}
	return nil
}

func validateRetry(r RetryPolicy, maxRequests, maxCost int64) error {
	if r.MaxAttempts <= 0 || int64(r.MaxAttempts) > maxRequests {
		return errors.New("max_attempts must be positive and no greater than max_requests")
	}
	if r.InitialBackoffMS < 0 || r.MaxBackoffMS < r.InitialBackoffMS || r.MaxRetryAfterMS <= 0 || r.JitterPPM < 0 || r.JitterPPM > 1_000_000 {
		return errors.New("invalid retry bounds")
	}
	if int64(r.MaxAttempts) > maxCost {
		return errors.New("retry attempts exceed hard cost total")
	}
	knownKinds := map[string]struct{}{"unavailable": {}, "rate_limited": {}, "bad_response": {}, "auth": {}}
	for _, kind := range r.RetryKinds {
		if _, ok := knownKinds[kind]; !ok {
			return fmt.Errorf("unknown retry kind %q", kind)
		}
	}
	return nil
}

func validatePoll(p PollPolicy) error {
	if p.SchedulerTickSeconds <= 0 || p.BaseIntervalSeconds <= 0 || p.MinIntervalSeconds <= 0 || p.MaxIntervalSeconds < p.BaseIntervalSeconds || p.MinIntervalSeconds > p.BaseIntervalSeconds {
		return errors.New("invalid interval bounds")
	}
	if p.MinIntervalSeconds != p.BaseIntervalSeconds {
		return errors.New("v1 minimum interval must equal base interval")
	}
	if p.UnchangedStepsBeforeSlowdown <= 0 || p.SuccessStepsBeforeRecovery <= 0 || p.JitterPPM < 0 || p.JitterPPM > 1_000_000 {
		return errors.New("invalid poll transition bounds")
	}
	return nil
}

// LoadPolicy decodes a policy with unknown-field rejection and validates it.
func LoadPolicy(r io.Reader) (BudgetPolicyV1, error) {
	if r == nil {
		return BudgetPolicyV1{}, errors.New("nil policy reader")
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var policy BudgetPolicyV1
	if err := dec.Decode(&policy); err != nil {
		return BudgetPolicyV1{}, fmt.Errorf("decode connector budget policy: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return BudgetPolicyV1{}, errors.New("connector budget policy has trailing JSON")
		}
		return BudgetPolicyV1{}, fmt.Errorf("decode trailing policy data: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return BudgetPolicyV1{}, err
	}
	return policy, nil
}

func LoadPolicyBytes(data []byte) (BudgetPolicyV1, error) {
	return LoadPolicy(bytes.NewReader(data))
}

// DefaultPolicyV1 returns the checked-in v1 literal policy represented as Go
// values.  The JSON artifact is generated from the same values during review;
// callers receive a deep-enough copy so slices can be safely adjusted in tests.
func DefaultPolicyV1() BudgetPolicyV1 {
	capability := func(connectorType, name, scope, class, cursor, coverage string, maxReq, maxPages, maxRows, maxBytes, maxCost int64, routes ...RouteCost) CapabilityBudget {
		return CapabilityBudget{
			ConnectorType: connectorType, Capability: name, ProviderScopeRule: scope, CostClass: class,
			MinConnectorVersion: "1", MaxConnectorVersion: "1", Routes: append([]RouteCost(nil), routes...),
			MaxRequests: maxReq, MaxPages: maxPages, MaxRows: maxRows, MaxBytes: maxBytes, MaxCostUnits: maxCost,
			MaxRunMillis: defaultMaxRunMillis, MaxConcurrentSource: defaultMaxConcurrentSource,
			CursorMode: cursor, SingleFlightKey: "environment/service_instance/capability/policy", CoveragePolicy: coverage,
			Retry: RetryPolicy{MaxAttempts: int(minInt64(maxReq, 3)), InitialBackoffMS: 250, MaxBackoffMS: 30_000, MaxRetryAfterMS: 120_000, JitterPPM: 10_000, RetryKinds: []string{"unavailable", "rate_limited"}},
			Poll:  PollPolicy{SchedulerTickSeconds: defaultSchedulerTickSecs, BaseIntervalSeconds: defaultBaseIntervalSecs, MinIntervalSeconds: defaultBaseIntervalSecs, MaxIntervalSeconds: defaultMaxIntervalSecs, UnchangedStepsBeforeSlowdown: 3, SuccessStepsBeforeRecovery: 2, JitterPPM: 10_000},
		}
	}
	route := func(id, method, path string, cost, bytes, rows int64) RouteCost {
		return RouteCost{RouteID: id, Method: method, PathTemplate: path, CostUnits: cost, MaxResponseBytes: bytes, MaxRows: rows}
	}
	const (
		probeBytes = int64(256 * 1024)
		pageBytes  = int64(8 * 1024 * 1024)
		wideBytes  = int64(64 * 1024 * 1024)
	)
	p := BudgetPolicyV1{PolicyVersion: PolicyVersionV1, Capabilities: []CapabilityBudget{
		capability("sub2api", "sub2api.health.read", "sub2api", CostClassProbe, CursorModeNone, CoveragePolicyComplete, 1, 1, 1, probeBytes, 1, route("sub2api.health", "GET", "/health", 1, probeBytes, 1)),
		capability("sub2api", "sub2api.service.version_read", "sub2api", CostClassProbe, CursorModeNone, CoveragePolicyComplete, 1, 1, 1, probeBytes, 1, route("sub2api.version", "GET", "/api/v1/admin/system/version", 1, probeBytes, 1)),
		capability("sub2api", "sub2api.users.read", "sub2api", CostClassLight, CursorModeNone, CoveragePolicyComplete, 1, 1, 1, pageBytes, 1, route("sub2api.dashboard.stats", "GET", "/api/v1/admin/dashboard/stats", 1, pageBytes, 1)),
		capability("sub2api", "sub2api.users.balance_read", "sub2api", CostClassPage, CursorModePage, CoveragePolicyPartial, 20, 20, 20_000, wideBytes, 20, route("sub2api.users", "GET", "/api/v1/admin/users", 1, pageBytes, 1_000)),
		capability("sub2api", "sub2api.orders.read", "sub2api", CostClassPage, CursorModePage, CoveragePolicyPartial, 180, 90, 9_000, wideBytes, 180, route("sub2api.payment.dashboard", "GET", "/api/v1/admin/payment/dashboard", 1, pageBytes, 1_000), route("sub2api.dashboard.trend", "GET", "/api/v1/admin/dashboard/trend", 1, pageBytes, 1_000)),
		capability("sub2api", "sub2api.accounts.read", "sub2api", CostClassPage, CursorModePage, CoveragePolicyPartial, 10, 10, 2_000, wideBytes, 10, route("sub2api.accounts", "GET", "/api/v1/admin/accounts", 1, pageBytes, 200)),
		capability("sub2api", "sub2api.channels.balance_read", "sub2api", CostClassPage, CursorModePage, CoveragePolicyPartial, 10, 10, 2_000, wideBytes, 10, route("sub2api.channels.balance", "GET", "/api/v1/admin/accounts", 1, pageBytes, 200)),
		capability("newapi", "newapi.service.version_read", "newapi", CostClassProbe, CursorModeNone, CoveragePolicyComplete, 1, 1, 1, probeBytes, 1, route("newapi.status.version", "GET", "/api/status", 1, probeBytes, 1)),
		capability("newapi", "newapi.health.read", "newapi", CostClassProbe, CursorModeNone, CoveragePolicyComplete, 1, 1, 1, probeBytes, 1, route("newapi.status.health", "GET", "/api/status", 1, probeBytes, 1)),
		capability("newapi", "newapi.users.read", "newapi", CostClassPage, CursorModePage, CoveragePolicyPartial, 50, 50, 5_000, wideBytes, 50, route("newapi.users", "GET", "/api/user/", 1, pageBytes, 100)),
		capability("newapi", "newapi.orders.read", "newapi", CostClassPage, CursorModePage, CoveragePolicyPartial, 20, 20, 2_000, wideBytes, 40, route("newapi.topups", "GET", "/api/user/topup", 2, pageBytes, 100)),
		capability("newapi", "newapi.channels.read", "newapi", CostClassPage, CursorModePage, CoveragePolicyPartial, 10, 10, 1_000, wideBytes, 10, route("newapi.channels", "GET", "/api/channel/", 1, pageBytes, 100)),
		capability("newapi", "newapi.errors.read", "newapi", CostClassCountHeavy, CursorModePage, CoveragePolicyPartial, 80, 40, 80, wideBytes, 160, route("newapi.logs", "GET", "/api/log/", 2, pageBytes, 1)),
		capability("newapi", "newapi.models.usage_read", "newapi", CostClassPage, CursorModePage, CoveragePolicyPartial, 10, 10, 1_000, wideBytes, 10, route("newapi.data", "GET", "/api/data/", 1, pageBytes, 100)),
		capability("metering", "metering.health.read", "metering", CostClassProbe, CursorModeNone, CoveragePolicyComplete, 2, 2, 2, probeBytes, 2, route("metering.sub2api.health", "GET", "/health", 1, probeBytes, 1), route("metering.newapi.status.health", "GET", "/api/status", 1, probeBytes, 1)),
		capability("metering", "metering.service.version_read", "metering", CostClassProbe, CursorModeNone, CoveragePolicyComplete, 2, 2, 2, probeBytes, 2, route("metering.sub2api.version", "GET", "/api/v1/admin/system/version", 1, probeBytes, 1), route("metering.newapi.status.version", "GET", "/api/status", 1, probeBytes, 1)),
		capability("metering", "metering.token.usage_read", "metering", CostClassLight, CursorModeNone, CoveragePolicyComplete, 2, 2, 2, pageBytes, 2, route("metering.sub2api.token.usage", "GET", "/v1/usage", 1, pageBytes, 1), route("metering.newapi.token.usage", "GET", "/api/log/self/stat", 1, pageBytes, 1)),
		capability("metering", "metering.account.revenue_read", "metering", CostClassLight, CursorModeNone, CoveragePolicyComplete, 1, 1, 1, pageBytes, 1, route("metering.sub2api.account.revenue", "GET", "/api/v1/admin/accounts/{account_id}/stats", 1, pageBytes, 1)),
	}}
	// Contract-declared capabilities whose upstream route is not implemented
	// remain explicit and disabled.  This prevents an accidental future caller
	// from treating an absent route as an unbounded request path.
	disabled := func(connectorType, name string) CapabilityBudget {
		return CapabilityBudget{
			ConnectorType: connectorType, Capability: name, ProviderScopeRule: connectorType,
			CostClass: CostClassLight, MinConnectorVersion: "1", MaxConnectorVersion: "1",
			MaxRequests: 1, MaxPages: 1, MaxRows: 1, MaxBytes: probeBytes, MaxCostUnits: 1,
			MaxRunMillis: defaultMaxRunMillis, MaxConcurrentSource: defaultMaxConcurrentSource,
			CursorMode: CursorModeNone, SingleFlightKey: "environment/service_instance/capability/policy",
			CoveragePolicy: CoveragePolicyPartial, Disabled: true,
			Retry: RetryPolicy{MaxAttempts: 1, InitialBackoffMS: 250, MaxBackoffMS: 30_000, MaxRetryAfterMS: 120_000, JitterPPM: 10_000, RetryKinds: []string{"unavailable"}},
			Poll:  PollPolicy{SchedulerTickSeconds: defaultSchedulerTickSecs, BaseIntervalSeconds: defaultBaseIntervalSecs, MinIntervalSeconds: defaultBaseIntervalSecs, MaxIntervalSeconds: defaultMaxIntervalSecs, UnchangedStepsBeforeSlowdown: 3, SuccessStepsBeforeRecovery: 2, JitterPPM: 10_000},
		}
	}
	p.Capabilities = append(p.Capabilities,
		disabled("sub2api", "sub2api.groups.read"),
		disabled("sub2api", "sub2api.models.usage_read"),
		disabled("metering", "metering.upstream.balance_read"),
	)
	return p
}

// Capability returns a defensive copy of a named capability budget.
func (p BudgetPolicyV1) Capability(name string) (CapabilityBudget, bool) {
	for _, cap := range p.Capabilities {
		if cap.Capability == name {
			cap.Routes = append([]RouteCost(nil), cap.Routes...)
			cap.Retry.RetryKinds = append([]string(nil), cap.Retry.RetryKinds...)
			return cap, true
		}
	}
	return CapabilityBudget{}, false
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
