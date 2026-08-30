package connector

import (
	"net/http"
	"testing"
	"time"
)

func testPollPolicy() PollPolicy {
	return PollPolicy{SchedulerTickSeconds: 300, BaseIntervalSeconds: 300, MinIntervalSeconds: 300, MaxIntervalSeconds: 3600, UnchangedStepsBeforeSlowdown: 2, SuccessStepsBeforeRecovery: 2, JitterPPM: 10000}
}

func TestParseRetryAfterDeltaDateInvalidAndOverCap(t *testing.T) {
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	got := ParseRetryAfter("30", now, time.Time{}, 120*time.Second)
	if !got.Valid || got.Duration != 30*time.Second || got.ManualSuspend {
		t.Fatalf("delta = %+v", got)
	}
	date := now.Add(45 * time.Second).Format(http.TimeFormat)
	got = ParseRetryAfter(date, now, now, 120*time.Second)
	if !got.Valid || got.Duration != 45*time.Second {
		t.Fatalf("date = %+v", got)
	}
	got = ParseRetryAfter("-1", now, time.Time{}, 120*time.Second)
	if got.Valid {
		t.Fatalf("negative retry-after should be invalid: %+v", got)
	}
	got = ParseRetryAfter("121", now, time.Time{}, 120*time.Second)
	if !got.Valid || !got.ManualSuspend {
		t.Fatalf("over-cap = %+v", got)
	}
}

func TestNextPollStateTransitionsAndDeterministicJitter(t *testing.T) {
	p := testPollPolicy()
	now := time.Unix(1_000, 0).UTC()
	initial := PollState{CurrentIntervalSeconds: 300}
	one, err := NextPollState(initial, PollTransition{Now: now, Outcome: OutcomeUnavailable, Policy: p, Identity: "svc/cap", Slot: "slot-1"})
	if err != nil || one.CurrentIntervalSeconds <= initial.CurrentIntervalSeconds {
		t.Fatalf("unavailable transition = %+v, %v", one, err)
	}
	a, err := NextPollState(initial, PollTransition{Now: now, Outcome: OutcomeSuccessChanged, Policy: p, Identity: "svc/cap", Slot: "slot-1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NextPollState(initial, PollTransition{Now: now, Outcome: OutcomeSuccessChanged, Policy: p, Identity: "svc/cap", Slot: "slot-1"})
	if err != nil || !a.NextDueAt.Equal(b.NextDueAt) {
		t.Fatalf("jitter must be deterministic: a=%+v b=%+v err=%v", a, b, err)
	}
	suspended, err := NextPollState(initial, PollTransition{Now: now, Outcome: OutcomeAuthSuspended, Policy: p, Identity: "svc/cap", Slot: "slot-1"})
	if err != nil || suspended.CircuitState != CircuitAuthSuspend {
		t.Fatalf("auth transition = %+v, %v", suspended, err)
	}
}

func TestRetryAfterNeverShortenedAndOverCapSuspends(t *testing.T) {
	p := testPollPolicy()
	now := time.Unix(1_000, 0).UTC()
	s, err := NextPollState(PollState{CurrentIntervalSeconds: 300}, PollTransition{Now: now, Outcome: OutcomeRateLimited, RetryAfter: 500 * time.Second, Policy: p, Identity: "svc/cap", Slot: "slot"})
	if err != nil || !s.NextDueAt.After(now.Add(499*time.Second)) {
		t.Fatalf("retry-after should win: %+v %v", s, err)
	}
	s, err = NextPollState(PollState{CurrentIntervalSeconds: 300}, PollTransition{Now: now, Outcome: OutcomeRateLimited, RetryAfter: 4000 * time.Second, Policy: p, Identity: "svc/cap", Slot: "slot"})
	if err != nil || s.CircuitState != CircuitManualSuspend {
		t.Fatalf("over-cap retry-after should suspend: %+v %v", s, err)
	}
}
