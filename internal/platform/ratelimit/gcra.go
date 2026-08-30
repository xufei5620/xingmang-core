package ratelimit

import (
	"math"
	"time"
)

const (
	microsPerSecond = int64(time.Second / time.Microsecond)
	microsPerMinute = int64(60) * microsPerSecond
)

// Validate checks every policy value that is represented by a PostgreSQL
// integer or participates in checked GCRA arithmetic.
func (p Policy) Validate() error {
	if p.Revision <= 0 {
		return domainError(ErrorKindInvalidPolicy, "revision", ErrInvalidPolicy)
	}
	if p.Algorithm != AlgorithmGCRAv1 {
		return domainError(ErrorKindInvalidPolicy, "algorithm", ErrInvalidPolicy)
	}
	if p.KeyVersion == 0 || p.KeyVersion > MaxSQLInt {
		return domainError(ErrorKindInvalidPolicy, "key_version", ErrInvalidPolicy)
	}
	if p.PerMinute == 0 || p.PerMinute > MaxSQLInt {
		return domainError(ErrorKindInvalidPolicy, "per_minute", ErrInvalidPolicy)
	}
	if p.Burst == 0 || p.Burst > MaxSQLInt {
		return domainError(ErrorKindInvalidPolicy, "burst", ErrInvalidPolicy)
	}

	idleSeconds, err := wholeSeconds(p.IdleTTL)
	if err != nil {
		return domainError(ErrorKindInvalidPolicy, "idle_ttl", ErrInvalidPolicy)
	}
	cleanupSeconds, err := wholeSeconds(p.CleanupInterval)
	if err != nil {
		return domainError(ErrorKindInvalidPolicy, "cleanup_interval", ErrInvalidPolicy)
	}
	// The policy relation mirrors the SQL CHECK: at least one minute and at
	// least twice the time needed to refill a full burst.
	refillSeconds, err := checkedCeilMulDiv(int64(p.Burst), 60, int64(p.PerMinute))
	if err != nil {
		return domainError(ErrorKindInvalidPolicy, "burst", ErrInvalidPolicy)
	}
	minIdle := refillSeconds
	if minIdle > math.MaxInt64/2 {
		return domainError(ErrorKindOverflow, "idle_ttl", ErrOverflow)
	}
	minIdle *= 2
	if minIdle < 60 {
		minIdle = 60
	}
	if idleSeconds < minIdle {
		return domainError(ErrorKindInvalidPolicy, "idle_ttl", ErrInvalidPolicy)
	}
	if cleanupSeconds <= 0 || cleanupSeconds >= idleSeconds {
		return domainError(ErrorKindInvalidPolicy, "cleanup_interval", ErrInvalidPolicy)
	}
	return nil
}

// IntervalMicroseconds returns the exact integer inter-arrival interval.
func (p Policy) IntervalMicroseconds() (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	return checkedCeilDiv(microsPerMinute, int64(p.PerMinute))
}

// ToleranceMicroseconds returns the burst tolerance used by GCRA.
func (p Policy) ToleranceMicroseconds() (int64, error) {
	interval, err := p.IntervalMicroseconds()
	if err != nil {
		return 0, err
	}
	return checkedMul(int64(p.Burst-1), interval)
}

// Evaluate applies one integer GCRA decision and returns the state that must
// be persisted for the next call. observedAt is the authoritative observed
// clock value (the PostgreSQL implementation supplies its clock value).
// Every invalid or overflowing input returns an error and a zero decision;
// callers must never interpret an error as an allow.
func Evaluate(p Policy, state GCRAState, observedAt time.Time) (GCRAState, Decision, error) {
	if err := p.Validate(); err != nil {
		return GCRAState{}, Decision{}, err
	}
	if state.TATMicros < 0 || state.LastSeenMicros < 0 {
		return GCRAState{}, Decision{}, domainError(ErrorKindInvalidState, "state", ErrInvalidState)
	}
	nowMicros, err := timeToMicros(observedAt)
	if err != nil {
		return GCRAState{}, Decision{}, err
	}
	interval, err := p.IntervalMicroseconds()
	if err != nil {
		return GCRAState{}, Decision{}, err
	}
	tolerance, err := checkedMul(int64(p.Burst-1), interval)
	if err != nil {
		return GCRAState{}, Decision{}, domainError(ErrorKindOverflow, "tolerance", ErrOverflow)
	}

	initialized := state.Initialized || state.TATMicros != 0 || state.LastSeenMicros != 0
	lastSeen := state.LastSeenMicros
	tat := state.TATMicros
	if !initialized {
		// A missing row starts at the current time, yielding exactly Burst
		// simultaneous allows rather than an artificial warm-up delay.
		tat = nowMicros
		lastSeen = nowMicros
	}
	effectiveNow := nowMicros
	if lastSeen > effectiveNow {
		effectiveNow = lastSeen // clock rollback cannot grant capacity
	}

	// tat and tolerance are bounded so this subtraction cannot underflow,
	// nevertheless keep it checked to make the invariant explicit.
	eligibleAt, err := checkedSub(tat, tolerance)
	if err != nil {
		return GCRAState{}, Decision{}, domainError(ErrorKindOverflow, "eligible_at", ErrOverflow)
	}
	allowed := effectiveNow >= eligibleAt
	nextTAT := tat
	if allowed {
		base := tat
		if effectiveNow > base {
			base = effectiveNow
		}
		nextTAT, err = checkedAdd(base, interval)
		if err != nil {
			return GCRAState{}, Decision{}, domainError(ErrorKindOverflow, "tat", ErrOverflow)
		}
	}
	next := GCRAState{TATMicros: nextTAT, LastSeenMicros: effectiveNow, Initialized: true}
	decision := Decision{
		Allowed:        allowed,
		PolicyRevision: p.Revision,
		ObservedAt:     observedAt.UTC(),
	}
	if !allowed {
		delta, err := checkedSub(eligibleAt, effectiveNow)
		if err != nil {
			return GCRAState{}, Decision{}, domainError(ErrorKindOverflow, "retry_after", ErrOverflow)
		}
		if delta <= 0 {
			// This should be unreachable when allowed is false; fail closed if
			// a malformed state ever violates that relationship.
			return GCRAState{}, Decision{}, domainError(ErrorKindInvalidState, "retry_after", ErrInvalidState)
		}
		seconds, err := checkedCeilDiv(delta, microsPerSecond)
		if err != nil {
			return GCRAState{}, Decision{}, domainError(ErrorKindOverflow, "retry_after", ErrOverflow)
		}
		if seconds < 1 {
			seconds = 1
		}
		if seconds > int64(math.MaxInt64/int64(time.Second)) {
			return GCRAState{}, Decision{}, domainError(ErrorKindOverflow, "retry_after", ErrOverflow)
		}
		decision.RetryAfter = time.Duration(seconds) * time.Second
	}
	return next, decision, nil
}

func wholeSeconds(d time.Duration) (int64, error) {
	if d <= 0 || d%time.Second != 0 {
		return 0, ErrInvalidPolicy
	}
	seconds := int64(d / time.Second)
	if seconds <= 0 || seconds > int64(MaxSQLInt) {
		return 0, ErrInvalidPolicy
	}
	return seconds, nil
}

func timeToMicros(t time.Time) (int64, error) {
	seconds := t.Unix()
	if seconds < 0 {
		return 0, domainError(ErrorKindInvalidState, "observed_at", ErrInvalidState)
	}
	if seconds > math.MaxInt64/microsPerSecond {
		return 0, domainError(ErrorKindOverflow, "observed_at", ErrOverflow)
	}
	micros := seconds * microsPerSecond
	nanos := int64(t.Nanosecond())
	part := nanos / int64(time.Microsecond)
	if micros > math.MaxInt64-part {
		return 0, domainError(ErrorKindOverflow, "observed_at", ErrOverflow)
	}
	return micros + part, nil
}

func checkedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, ErrOverflow
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func checkedSub(a, b int64) (int64, error) {
	if b > 0 && a < math.MinInt64+b {
		return 0, ErrOverflow
	}
	if b < 0 && a > math.MaxInt64+b {
		return 0, ErrOverflow
	}
	return a - b, nil
}

func checkedMul(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a == math.MinInt64 && b == -1 || b == math.MinInt64 && a == -1 {
		return 0, ErrOverflow
	}
	result := a * b
	if result/b != a {
		return 0, ErrOverflow
	}
	return result, nil
}

func checkedCeilDiv(numerator, denominator int64) (int64, error) {
	if numerator < 0 || denominator <= 0 {
		return 0, ErrOverflow
	}
	if numerator == 0 {
		return 0, nil
	}
	if numerator > math.MaxInt64-(denominator-1) {
		// Avoid numerator+denominator-1 overflow.
		quotient := numerator / denominator
		if numerator%denominator != 0 {
			quotient, err := checkedAdd(quotient, 1)
			return quotient, err
		}
		return quotient, nil
	}
	return (numerator + denominator - 1) / denominator, nil
}

func checkedCeilMulDiv(value, multiplier, divisor int64) (int64, error) {
	product, err := checkedMul(value, multiplier)
	if err != nil {
		return 0, err
	}
	return checkedCeilDiv(product, divisor)
}
