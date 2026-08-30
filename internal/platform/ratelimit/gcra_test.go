package ratelimit

import (
	"errors"
	"math"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{
		Revision:        1,
		Algorithm:       AlgorithmGCRAv1,
		KeyVersion:      1,
		PerMinute:       60,
		Burst:           2,
		IdleTTL:         15 * time.Minute,
		CleanupInterval: 10 * time.Minute,
	}
}

func TestPolicyValidate(t *testing.T) {
	valid := testPolicy()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}

	cases := []struct {
		name string
		edit func(*Policy)
	}{
		{"revision", func(p *Policy) { p.Revision = 0 }},
		{"algorithm", func(p *Policy) { p.Algorithm = "token-bucket" }},
		{"key version", func(p *Policy) { p.KeyVersion = 0 }},
		{"key version exceeds sql integer", func(p *Policy) { p.KeyVersion = uint32(math.MaxInt32) + 1 }},
		{"per minute", func(p *Policy) { p.PerMinute = 0 }},
		{"burst", func(p *Policy) { p.Burst = 0 }},
		{"non-whole idle ttl", func(p *Policy) { p.IdleTTL = 15*time.Minute + time.Millisecond }},
		{"cleanup not below ttl", func(p *Policy) { p.CleanupInterval = 15 * time.Minute }},
		{"cleanup zero", func(p *Policy) { p.CleanupInterval = 0 }},
		{"ttl below refill floor", func(p *Policy) {
			p.PerMinute = 1
			p.Burst = 20
			p.IdleTTL = time.Minute
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.edit(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("expected validation error")
			} else {
				var typed *Error
				if !errors.As(err, &typed) || typed.Kind != ErrorKindInvalidPolicy {
					t.Fatalf("error = %T %v, want typed invalid-policy error", err, err)
				}
			}
		})
	}
}

func TestGCRABurstIsExact(t *testing.T) {
	p := testPolicy()
	p.PerMinute = 1
	p.Burst = 20
	p.IdleTTL = time.Hour
	p.CleanupInterval = 10 * time.Minute
	now := time.Unix(1_000, 123_000).UTC()
	var state GCRAState
	for i := 0; i < 20; i++ {
		var d Decision
		var err error
		state, d, err = Evaluate(p, state, now)
		if err != nil {
			t.Fatalf("evaluation %d: %v", i+1, err)
		}
		if !d.Allowed {
			t.Fatalf("evaluation %d denied before burst was spent: retry=%v", i+1, d.RetryAfter)
		}
	}
	_, d, err := Evaluate(p, state, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("21st simultaneous request must be denied")
	}
	if d.RetryAfter != time.Minute {
		t.Fatalf("retry after = %v, want 1m", d.RetryAfter)
	}
}

func TestGCRARefillAndRetryAfter(t *testing.T) {
	p := testPolicy()
	p.PerMinute = 120
	p.Burst = 1
	now := time.Unix(2_000, 0).UTC()
	state, d, err := Evaluate(p, GCRAState{}, now)
	if err != nil || !d.Allowed {
		t.Fatalf("first request: state=%+v decision=%+v err=%v", state, d, err)
	}
	_, d, err = Evaluate(p, state, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("burst=1 must deny immediately after the first request")
	}
	// interval is 500,000us. A little under half a second is not enough;
	// the calculated sub-second retry is rounded up to one second.
	_, d, err = Evaluate(p, state, now.Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("request before the interval must be denied")
	}
	if d.RetryAfter != time.Second {
		t.Fatalf("sub-second retry = %v, want 1s minimum", d.RetryAfter)
	}
	_, d, err = Evaluate(p, state, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatalf("request after refill denied: %+v", d)
	}
}

func TestGCRAClockRollbackDoesNotGrantCapacity(t *testing.T) {
	p := testPolicy()
	p.PerMinute = 60
	p.Burst = 1
	now := time.Unix(3_000, 0).UTC()
	state, d, err := Evaluate(p, GCRAState{}, now)
	if err != nil || !d.Allowed {
		t.Fatalf("first request: %+v %v", d, err)
	}
	rolledBack := now.Add(-time.Minute)
	next, d, err := Evaluate(p, state, rolledBack)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("clock rollback must not grant a token")
	}
	if next.LastSeenMicros < state.LastSeenMicros {
		t.Fatalf("last seen moved backwards: before=%d after=%d", state.LastSeenMicros, next.LastSeenMicros)
	}
	if d.ObservedAt != rolledBack {
		t.Fatalf("observed time should retain the injected clock: %v", d.ObservedAt)
	}
}

func TestGCRARejectsIntegerOverflow(t *testing.T) {
	cases := []struct {
		name  string
		state GCRAState
		now   time.Time
	}{
		{
			name:  "tat addition",
			state: GCRAState{TATMicros: math.MaxInt64 - 1, LastSeenMicros: math.MaxInt64 - 2, Initialized: true},
			now:   time.Unix(math.MaxInt64/microsPerSecond, (math.MaxInt64%microsPerSecond)*1000).UTC(),
		},
		{
			name:  "time conversion",
			state: GCRAState{},
			now:   time.Unix(math.MaxInt64, 0).UTC(),
		},
	}
	p := testPolicy()
	p.PerMinute = 1
	p.Burst = 1
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Evaluate(p, tc.state, tc.now)
			if err == nil {
				t.Fatal("expected arithmetic error")
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != ErrorKindOverflow {
				t.Fatalf("error = %T %v, want typed overflow", err, err)
			}
		})
	}
}
