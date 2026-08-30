package connector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Stable budget outcomes.  These strings are safe to expose in metrics and
// evidence; they never include an upstream URL, query, body or credential.
const (
	OutcomeNotDue                 = "not_due"
	OutcomeSourceBudgetWait       = "source_budget_wait"
	OutcomeSourceConcurrencyFull  = "source_concurrency_full"
	OutcomeRunRequestLimit        = "run_request_limit"
	OutcomeRunPageLimit           = "run_page_limit"
	OutcomeRunRowLimit            = "run_row_limit"
	OutcomeRunByteLimit           = "run_byte_limit"
	OutcomeRunCostLimit           = "run_cost_limit"
	OutcomeRunDeadline            = "run_deadline"
	OutcomeCursorStalled          = "cursor_stalled"
	OutcomeCursorCycle            = "cursor_cycle"
	OutcomeResponseTooLarge       = "response_too_large"
	OutcomeRateLimited            = "rate_limited"
	OutcomeAuthSuspended          = "auth_suspended"
	OutcomeNotSupported           = "not_supported"
	OutcomeVersionUnsupported     = "version_unsupported"
	OutcomeBudgetBackend          = "budget_backend_unavailable"
	OutcomePartialBudgetExhausted = "partial_budget_exhausted"
	OutcomeComplete               = "complete"
	OutcomeUnavailable            = "unavailable"
	OutcomeBadResponse            = "bad_response"
	OutcomeSuccessChanged         = "success_changed"
	OutcomeSuccessUnchanged       = "success_unchanged"
	OutcomePartial                = "partial"
)

// BudgetErrorCode is a stable, low-card error identifier.
type BudgetErrorCode string

// Error lets callers use a code as an errors.Is target without exposing
// implementation details or upstream text.
func (c BudgetErrorCode) Error() string { return "connector budget: " + string(c) }

const (
	ErrUnknownRoute       BudgetErrorCode = "unknown_route"
	ErrInvalidCostUnits   BudgetErrorCode = "invalid_cost_units"
	ErrRunRequestLimit    BudgetErrorCode = OutcomeRunRequestLimit
	ErrRunPageLimit       BudgetErrorCode = OutcomeRunPageLimit
	ErrRunRowLimit        BudgetErrorCode = OutcomeRunRowLimit
	ErrRunByteLimit       BudgetErrorCode = OutcomeRunByteLimit
	ErrRunCostLimit       BudgetErrorCode = OutcomeRunCostLimit
	ErrRunDeadline        BudgetErrorCode = OutcomeRunDeadline
	ErrCursorStalled      BudgetErrorCode = OutcomeCursorStalled
	ErrCursorCycle        BudgetErrorCode = OutcomeCursorCycle
	ErrAuthorityDenied    BudgetErrorCode = "authority_denied"
	ErrGuardFinished      BudgetErrorCode = "guard_finished"
	ErrInvalidPlan        BudgetErrorCode = "invalid_plan"
	ErrInvalidObservation BudgetErrorCode = "invalid_observation"
)

var (
	ErrUnknownRouteValue     = &BudgetError{Code: ErrUnknownRoute}
	ErrInvalidCostUnitsValue = &BudgetError{Code: ErrInvalidCostUnits}
	ErrRunRequestLimitValue  = &BudgetError{Code: ErrRunRequestLimit}
	ErrRunPageLimitValue     = &BudgetError{Code: ErrRunPageLimit}
	ErrRunRowLimitValue      = &BudgetError{Code: ErrRunRowLimit}
	ErrRunByteLimitValue     = &BudgetError{Code: ErrRunByteLimit}
	ErrRunCostLimitValue     = &BudgetError{Code: ErrRunCostLimit}
	ErrRunDeadlineValue      = &BudgetError{Code: ErrRunDeadline}
	ErrCursorStalledValue    = &BudgetError{Code: ErrCursorStalled}
	ErrCursorCycleValue      = &BudgetError{Code: ErrCursorCycle}
	ErrAuthorityDeniedValue  = &BudgetError{Code: ErrAuthorityDenied}
	ErrGuardFinishedValue    = &BudgetError{Code: ErrGuardFinished}
)

// BudgetError is intentionally terse.  The optional cause is only available
// through Unwrap for internal logs; Error() never prints it.
type BudgetError struct {
	Code  BudgetErrorCode
	cause error
}

func (e *BudgetError) Error() string {
	if e == nil {
		return ""
	}
	return "connector budget: " + string(e.Code)
}

func (e *BudgetError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *BudgetError) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}
	if code, ok := target.(BudgetErrorCode); ok {
		return e.Code == code
	}
	return false
}

func budgetError(code BudgetErrorCode, cause error) *BudgetError {
	return &BudgetError{Code: code, cause: cause}
}

// BudgetCodeOf extracts only the stable code; it never exposes wrapped source
// errors or request details.
func BudgetCodeOf(err error) BudgetErrorCode {
	if err == nil {
		return ""
	}
	var be *BudgetError
	if errors.As(err, &be) && be != nil {
		return be.Code
	}
	return ""
}

// RunPlan identifies one scheduled capability run.  Service and provider
// values are registry IDs, not hostnames or secrets.
type RunPlan struct {
	Environment          string `json:"environment"`
	ServiceID            string `json:"service_id"`
	ServiceInstanceID    string `json:"service_instance_id"`
	ProviderScope        string `json:"provider_scope"`
	Capability           string `json:"capability"`
	PolicyVersion        int    `json:"policy_version"`
	CredentialGeneration int64  `json:"credential_generation"`
	RunID                string `json:"run_id"`
	ScheduledSlot        string `json:"scheduled_slot"`
}

func (p RunPlan) validate() error {
	for name, value := range map[string]string{
		"environment": p.Environment, "service_id": p.ServiceID,
		"service_instance_id": p.ServiceInstanceID, "provider_scope": p.ProviderScope,
		"capability": p.Capability, "run_id": p.RunID,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\t") {
			return fmt.Errorf("%s is empty or contains control whitespace", name)
		}
	}
	if p.PolicyVersion <= 0 {
		return errors.New("policy_version must be positive")
	}
	return nil
}

type RunCounters struct {
	Requests  int64 `json:"requests"`
	Pages     int64 `json:"pages"`
	Rows      int64 `json:"rows"`
	Bytes     int64 `json:"bytes"`
	CostUnits int64 `json:"cost_units"`
	Attempts  int64 `json:"attempts"`
}

// RunOutcome is an alias for the stable outcome vocabulary in the design
// document; keeping it an alias preserves compatibility with string callers.
type RunOutcome = string

type CoverageEvidence struct {
	Complete bool     `json:"complete"`
	Covered  int64    `json:"covered"`
	Expected int64    `json:"expected"`
	Reasons  []string `json:"reasons,omitempty"`
}

type RunEvidence struct {
	Plan        RunPlan          `json:"plan"`
	Counters    RunCounters      `json:"counters"`
	OutcomeCode string           `json:"outcome_code"`
	Coverage    CoverageEvidence `json:"coverage"`
}

// BudgetAuthority is the pure seam used by R215-1 tests.  R215-2 adapts the
// PostgreSQL source-budget reservation to this interface.
type BudgetAuthority interface {
	Reserve(context.Context, string, int64) error
}

type BudgetAuthorityFunc func(context.Context, string, int64) error

func (f BudgetAuthorityFunc) Reserve(ctx context.Context, routeID string, units int64) error {
	if f == nil {
		return nil
	}
	return f(ctx, routeID, units)
}

// SourceBudgetAuthority is an alias with the terminology used by the design.
type SourceBudgetAuthority = BudgetAuthority

type BudgetOption func(*BudgetGuard)

func WithBudgetAuthority(a BudgetAuthority) BudgetOption {
	return func(g *BudgetGuard) { g.authority = a }
}
func WithBudgetClock(now func() time.Time) BudgetOption {
	return func(g *BudgetGuard) {
		if now != nil {
			g.now = now
		}
	}
}
func WithBudgetDeadline(deadline time.Time) BudgetOption {
	return func(g *BudgetGuard) { g.deadline = deadline }
}

// Guard is the contract connectors use before/after each request and page.
type Guard interface {
	BeforeRequest(context.Context, string, int64) error
	ObserveResponse(string, int, int64, int64, *time.Duration) error
	ObservePage(string, string, int64) error
	Finish(string) RunEvidence
}

// BudgetRun is a compatibility alias used by the design document.
type BudgetRun = Guard

type BudgetGuard struct {
	mu          sync.Mutex
	plan        RunPlan
	budget      CapabilityBudget
	routes      map[string]RouteCost
	authority   BudgetAuthority
	now         func() time.Time
	deadline    time.Time
	counters    RunCounters
	seenCursors map[string]struct{}
	reasons     map[string]struct{}
	outcome     string
	finished    bool
	evidence    RunEvidence
}

// NewBudgetGuard builds a pure per-run guard.  It does not resolve secrets or
// perform I/O.  The caller must supply a capability policy already selected by
// the registry; unknown route IDs fail closed here and again in policy.Validate.
func NewBudgetGuard(plan RunPlan, budget CapabilityBudget, options ...BudgetOption) (*BudgetGuard, error) {
	if err := plan.validate(); err != nil {
		return nil, budgetError(ErrInvalidPlan, err)
	}
	if budget.Disabled {
		return nil, budgetError(ErrInvalidPlan, errors.New("capability is disabled"))
	}
	if strings.TrimSpace(budget.Capability) == "" || strings.TrimSpace(budget.ConnectorType) == "" || strings.TrimSpace(budget.ProviderScopeRule) == "" || budget.MaxRequests <= 0 || budget.MaxPages <= 0 || budget.MaxRows <= 0 || budget.MaxBytes <= 0 || budget.MaxCostUnits <= 0 || budget.MaxRunMillis <= 0 || budget.Retry.MaxAttempts <= 0 {
		return nil, budgetError(ErrInvalidPlan, errors.New("capability budget has missing hard cap"))
	}
	routeInventory := routeSpecMap()
	routes := make(map[string]RouteCost, len(budget.Routes))
	for _, route := range budget.Routes {
		spec, ok := routeInventory[route.RouteID]
		if !ok {
			return nil, budgetError(ErrUnknownRoute, nil)
		}
		if spec.Capability != budget.Capability || spec.ConnectorType != budget.ConnectorType || route.Method != spec.Method || route.PathTemplate != spec.PathTemplate {
			return nil, budgetError(ErrInvalidPlan, errors.New("route does not match capability inventory"))
		}
		if route.CostUnits <= 0 || route.MaxResponseBytes <= 0 || route.MaxRows <= 0 {
			return nil, budgetError(ErrInvalidPlan, errors.New("route budget has missing hard cap"))
		}
		if _, exists := routes[route.RouteID]; exists {
			return nil, budgetError(ErrInvalidPlan, errors.New("duplicate route"))
		}
		routes[route.RouteID] = route
	}
	if len(routes) == 0 {
		return nil, budgetError(ErrInvalidPlan, errors.New("capability budget has no routes"))
	}
	g := &BudgetGuard{plan: plan, budget: budget, routes: routes, now: time.Now, seenCursors: make(map[string]struct{}), reasons: make(map[string]struct{})}
	for _, option := range options {
		if option != nil {
			option(g)
		}
	}
	return g, nil
}

// NewBudgetRun is the explicit name used by callers that model a run rather
// than a guard.  It is intentionally identical and side-effect free.
func NewBudgetRun(plan RunPlan, budget CapabilityBudget, options ...BudgetOption) (*BudgetGuard, error) {
	return NewBudgetGuard(plan, budget, options...)
}

func (g *BudgetGuard) BeforeRequest(ctx context.Context, routeID string, costUnits int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finished {
		return ErrGuardFinishedValue
	}
	if _, ok := g.routes[routeID]; !ok {
		return ErrUnknownRouteValue
	}
	if costUnits <= 0 {
		return ErrInvalidCostUnitsValue
	}
	if ctx == nil {
		return budgetError(ErrInvalidPlan, errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return ErrRunDeadlineValue
	}
	now := g.now()
	if !g.deadline.IsZero() && !now.Before(g.deadline) {
		return ErrRunDeadlineValue
	}
	if deadline, ok := ctx.Deadline(); ok && !now.Before(deadline) {
		return ErrRunDeadlineValue
	}
	if g.counters.Requests >= g.budget.MaxRequests || g.counters.Attempts >= int64(g.budget.Retry.MaxAttempts) {
		g.recordReason(OutcomeRunRequestLimit)
		return ErrRunRequestLimitValue
	}
	if costUnits > g.budget.MaxCostUnits-g.counters.CostUnits {
		g.recordReason(OutcomeRunCostLimit)
		return ErrRunCostLimitValue
	}
	if g.authority != nil {
		if err := g.authority.Reserve(ctx, routeID, costUnits); err != nil {
			// Preserve a known budget error code, otherwise expose only a generic
			// authority denial.  The underlying cause remains internal.
			var be *BudgetError
			if errors.As(err, &be) {
				g.recordReason(string(be.Code))
				return be
			}
			g.recordReason(string(ErrAuthorityDenied))
			return budgetError(ErrAuthorityDenied, err)
		}
	}
	// Checked increments (the caps are positive, but keep arithmetic robust if
	// a caller constructs a malformed budget in a test).
	if g.counters.Requests == int64(^uint64(0)>>1) || g.counters.Attempts == int64(^uint64(0)>>1) {
		return ErrRunRequestLimitValue
	}
	g.counters.Requests++
	g.counters.Attempts++
	g.counters.CostUnits += costUnits
	return nil
}

func (g *BudgetGuard) ObserveResponse(routeID string, status int, bytesRead, rows int64, _ *time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finished {
		return ErrGuardFinishedValue
	}
	if _, ok := g.routes[routeID]; !ok {
		return ErrUnknownRouteValue
	}
	if status < 0 || status > 999 || bytesRead < 0 || rows < 0 {
		return budgetError(ErrInvalidObservation, nil)
	}
	route := g.routes[routeID]
	if bytesRead > route.MaxResponseBytes {
		g.recordReason(OutcomeResponseTooLarge)
		g.outcome = OutcomeResponseTooLarge
		return budgetError(ErrRunByteLimit, nil)
	}
	if rows > route.MaxRows {
		g.recordReason(OutcomeRunRowLimit)
		g.outcome = OutcomeRunRowLimit
		return ErrRunRowLimitValue
	}
	if bytesRead > g.budget.MaxBytes-g.counters.Bytes {
		g.recordReason(OutcomeRunByteLimit)
		g.outcome = OutcomeRunByteLimit
		return ErrRunByteLimitValue
	}
	if rows > g.budget.MaxRows-g.counters.Rows {
		g.recordReason(OutcomeRunRowLimit)
		g.outcome = OutcomeRunRowLimit
		return ErrRunRowLimitValue
	}
	g.counters.Bytes += bytesRead
	g.counters.Rows += rows
	return nil
}

func (g *BudgetGuard) ObservePage(cursorIn, cursorOut string, rows int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finished {
		return ErrGuardFinishedValue
	}
	if rows < 0 {
		return budgetError(ErrInvalidObservation, nil)
	}
	if g.counters.Pages >= g.budget.MaxPages {
		g.recordReason(OutcomeRunPageLimit)
		g.outcome = OutcomeRunPageLimit
		return ErrRunPageLimitValue
	}
	if rows > g.budget.MaxRows-g.counters.Rows {
		g.recordReason(OutcomeRunRowLimit)
		g.outcome = OutcomeRunRowLimit
		return ErrRunRowLimitValue
	}
	if g.budget.CursorMode == CursorModeCursor || g.budget.CursorMode == CursorModePage {
		in, out := strings.TrimSpace(cursorIn), strings.TrimSpace(cursorOut)
		if out == "" || out == in {
			g.recordReason(OutcomeCursorStalled)
			g.outcome = OutcomeCursorStalled
			return ErrCursorStalledValue
		}
		if _, seen := g.seenCursors[out]; seen {
			g.recordReason(OutcomeCursorCycle)
			g.outcome = OutcomeCursorCycle
			return ErrCursorCycleValue
		}
		g.seenCursors[out] = struct{}{}
	}
	g.counters.Pages++
	g.counters.Rows += rows
	return nil
}

func (g *BudgetGuard) recordReason(reason string) {
	if strings.TrimSpace(reason) != "" {
		g.reasons[reason] = struct{}{}
	}
}

func (g *BudgetGuard) Finish(outcome string) RunEvidence {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finished {
		return cloneEvidence(g.evidence)
	}
	if strings.TrimSpace(outcome) == "" {
		outcome = g.outcome
	}
	if strings.TrimSpace(outcome) == "" {
		outcome = OutcomeComplete
	}
	if g.outcome != "" && outcome == OutcomeComplete {
		outcome = g.outcome
	}
	g.outcome = outcome
	complete := outcome == OutcomeComplete && len(g.reasons) == 0
	reasons := make([]string, 0, len(g.reasons))
	for reason := range g.reasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	g.evidence = RunEvidence{Plan: g.plan, Counters: g.counters, OutcomeCode: outcome, Coverage: CoverageEvidence{Complete: complete, Covered: g.counters.Pages, Reasons: reasons}}
	g.finished = true
	return cloneEvidence(g.evidence)
}

func cloneEvidence(in RunEvidence) RunEvidence {
	in.Coverage.Reasons = append([]string(nil), in.Coverage.Reasons...)
	return in
}

func (g *BudgetGuard) Counters() RunCounters { g.mu.Lock(); defer g.mu.Unlock(); return g.counters }
