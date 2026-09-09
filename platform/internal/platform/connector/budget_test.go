package connector

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testBudget(t *testing.T) CapabilityBudget {
	t.Helper()
	for _, b := range DefaultPolicyV1().Capabilities {
		if b.Capability == "sub2api.users.balance_read" {
			return b
		}
	}
	t.Fatal("default test capability missing")
	return CapabilityBudget{}
}

func testPlan() RunPlan {
	return RunPlan{Environment: "staging", ServiceID: "svc-1", ServiceInstanceID: "sub2api-1", ProviderScope: "sub2api-main", Capability: "sub2api.users.balance_read", PolicyVersion: 1, RunID: "run-1", ScheduledSlot: "slot-1"}
}

func TestBudgetGuardCountsFirstRequestAndEnforcesCaps(t *testing.T) {
	b := testBudget(t)
	b.MaxRequests, b.MaxPages, b.MaxRows, b.MaxBytes, b.MaxCostUnits = 1, 1, 1, 4, 2
	g, err := NewBudgetGuard(testPlan(), b)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	if err := g.BeforeRequest(context.Background(), b.Routes[0].RouteID, 2); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := g.BeforeRequest(context.Background(), b.Routes[0].RouteID, 1); !errors.Is(err, ErrRunRequestLimit) {
		t.Fatalf("second request error = %v, want request limit", err)
	}
	if got := g.Finish(OutcomeComplete); got.Counters.Requests != 1 || got.Counters.Attempts != 1 {
		t.Fatalf("counters = %+v", got.Counters)
	}
}

func TestBudgetGuardRejectsNegativeAndUnknownRouteWithoutRequest(t *testing.T) {
	b := testBudget(t)
	g, err := NewBudgetGuard(testPlan(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.BeforeRequest(context.Background(), "unknown-route", 1); !errors.Is(err, ErrUnknownRoute) {
		t.Fatalf("unknown route error = %v", err)
	}
	if err := g.BeforeRequest(context.Background(), b.Routes[0].RouteID, -1); !errors.Is(err, ErrInvalidCostUnits) {
		t.Fatalf("negative cost error = %v", err)
	}
	if got := g.Finish(OutcomeComplete); got.Counters.Requests != 0 {
		t.Fatalf("failed preflight must make zero requests: %+v", got.Counters)
	}
}

func TestBudgetGuardCursorCycleAndPartialEvidence(t *testing.T) {
	b := testBudget(t)
	b.CursorMode = CursorModeCursor
	b.MaxPages = 3
	g, err := NewBudgetGuard(testPlan(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.ObservePage("", "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := g.ObservePage("a", "b", 1); err != nil {
		t.Fatal(err)
	}
	if err := g.ObservePage("b", "a", 1); !errors.Is(err, ErrCursorCycle) {
		t.Fatalf("cycle error = %v", err)
	}
	e := g.Finish(OutcomePartialBudgetExhausted)
	if e.Coverage.Complete || len(e.Coverage.Reasons) == 0 {
		t.Fatalf("partial evidence = %+v", e.Coverage)
	}
}

func TestBudgetGuardDeadlineAndAuthorityFailClosed(t *testing.T) {
	b := testBudget(t)
	called := false
	g, err := NewBudgetGuard(testPlan(), b, WithBudgetClock(func() time.Time { return time.Unix(100, 0) }), WithBudgetDeadline(time.Unix(99, 0)), WithBudgetAuthority(BudgetAuthorityFunc(func(context.Context, string, int64) error {
		called = true
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.BeforeRequest(context.Background(), b.Routes[0].RouteID, 1); !errors.Is(err, ErrRunDeadline) {
		t.Fatalf("deadline error = %v", err)
	}
	if called {
		t.Fatal("deadline preflight must not invoke authority")
	}

	g, err = NewBudgetGuard(testPlan(), b, WithBudgetAuthority(BudgetAuthorityFunc(func(context.Context, string, int64) error {
		return errors.New("provider secret must not leak")
	})))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.BeforeRequest(context.Background(), b.Routes[0].RouteID, 1); !errors.Is(err, ErrAuthorityDenied) {
		t.Fatalf("authority error = %v", err)
	}
	if got := g.Finish(OutcomeComplete); got.Counters.Requests != 0 {
		t.Fatal("authority denial must produce zero requests")
	}
}

func TestBudgetGuardFinishIsIdempotentAndEvidenceSorted(t *testing.T) {
	b := testBudget(t)
	g, err := NewBudgetGuard(testPlan(), b)
	if err != nil {
		t.Fatal(err)
	}
	_ = g.ObservePage("", "a", 0)
	one := g.Finish(OutcomePartialBudgetExhausted)
	two := g.Finish(OutcomeComplete)
	if one.OutcomeCode != two.OutcomeCode || len(one.Coverage.Reasons) != len(two.Coverage.Reasons) {
		t.Fatalf("finish changed evidence: one=%+v two=%+v", one, two)
	}
}
