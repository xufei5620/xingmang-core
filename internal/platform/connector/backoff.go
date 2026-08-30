package connector

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	CircuitClosed        = "closed"
	CircuitManualSuspend = "manual_suspend"
	CircuitAuthSuspend   = "auth_suspend"
	CircuitNotSupported  = "not_supported"
)

type RetryAfterDecision struct {
	Duration      time.Duration
	Valid         bool
	ManualSuspend bool
	OverCap       bool
	Invalid       bool
}

// ParseRetryAfter parses either delta-seconds or an HTTP-date.  A valid value
// above maxDuration is never silently capped: callers must suspend instead.
// responseDate is optional evidence for HTTP-date; when zero, now is used.
func ParseRetryAfter(value string, now, responseDate time.Time, maxDuration time.Duration) RetryAfterDecision {
	value = strings.TrimSpace(value)
	if value == "" || maxDuration <= 0 {
		return RetryAfterDecision{Invalid: true}
	}
	var duration time.Duration
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
			return RetryAfterDecision{Invalid: true}
		}
		duration = time.Duration(seconds) * time.Second
	} else {
		when, dateErr := http.ParseTime(value)
		if dateErr != nil {
			return RetryAfterDecision{Invalid: true}
		}
		base := now
		if !responseDate.IsZero() {
			base = responseDate
		}
		duration = when.Sub(base)
		if duration < 0 {
			return RetryAfterDecision{Invalid: true}
		}
	}
	decision := RetryAfterDecision{Duration: duration, Valid: true}
	if duration > maxDuration {
		decision.OverCap, decision.ManualSuspend = true, true
	}
	return decision
}

// ParseRetryAfterHeader is a convenience wrapper for callers that only have
// a local reference clock.
func ParseRetryAfterHeader(value string, now time.Time, maxDuration time.Duration) RetryAfterDecision {
	return ParseRetryAfter(value, now, time.Time{}, maxDuration)
}

// DeterministicJitter returns a non-negative deterministic delay bounded by
// interval*ppm/1e6.  Hash inputs are identifiers only; no secret is required.
func DeterministicJitter(identity, slot string, policyVersion int, interval time.Duration, ppm int32) time.Duration {
	if interval <= 0 || ppm <= 0 {
		return 0
	}
	if ppm > 1_000_000 {
		ppm = 1_000_000
	}
	h := sha256.New()
	h.Write([]byte(identity))
	h.Write([]byte{0})
	h.Write([]byte(slot))
	var version [8]byte
	binary.BigEndian.PutUint64(version[:], uint64(policyVersion))
	h.Write(version[:])
	sum := h.Sum(nil)
	raw := binary.BigEndian.Uint64(sum[:8])
	// Keep multiplication checked; duration is int64 nanoseconds.
	if interval > time.Duration(math.MaxInt64/int64(ppm)) {
		interval = time.Duration(math.MaxInt64 / int64(ppm))
	}
	bound := interval * time.Duration(ppm) / 1_000_000
	if bound <= 0 {
		return 0
	}
	return time.Duration(raw % uint64(bound+1))
}

type PollState struct {
	CurrentIntervalSeconds int64     `json:"current_interval_seconds"`
	ConsecutiveSuccess     int       `json:"consecutive_success"`
	ConsecutiveUnchanged   int       `json:"consecutive_unchanged"`
	ConsecutiveFailure     int       `json:"consecutive_failure"`
	RetryAfterUntil        time.Time `json:"retry_after_until,omitempty"`
	CircuitState           string    `json:"circuit_state"`
	NextDueAt              time.Time `json:"next_due_at"`
	LastOutcomeCode        string    `json:"last_outcome_code"`
	LastCoverageComplete   bool      `json:"last_coverage_complete"`
}

type PollTransition struct {
	Now           time.Time
	Outcome       string
	Changed       bool
	RetryAfter    time.Duration
	Policy        PollPolicy
	Identity      string
	Slot          string
	PolicyVersion int
}

func (p PollPolicy) validateForTransition() error {
	return validatePoll(p)
}

// NextPollState is a pure reference transition.  It never sleeps, starts a
// ticker, accesses a database, or contacts a connector.
func NextPollState(previous PollState, transition PollTransition) (PollState, error) {
	if err := transition.Policy.validateForTransition(); err != nil {
		return PollState{}, err
	}
	if transition.Now.IsZero() {
		return PollState{}, errors.New("poll transition requires reference time")
	}
	now := transition.Now.UTC()
	p := transition.Policy
	current := previous.CurrentIntervalSeconds
	if current <= 0 {
		current = p.BaseIntervalSeconds
	}
	if current < p.BaseIntervalSeconds {
		current = p.BaseIntervalSeconds
	}
	if current > p.MaxIntervalSeconds {
		current = p.MaxIntervalSeconds
	}
	out := previous
	out.CircuitState = CircuitClosed
	out.LastOutcomeCode = transition.Outcome
	out.RetryAfterUntil = time.Time{}

	inc := func(v *int) {
		if *v < math.MaxInt {
			*v = *v + 1
		}
	}
	resetSuccess := func() { out.ConsecutiveSuccess = 0 }
	resetUnchanged := func() { out.ConsecutiveUnchanged = 0 }
	stepUp := func() {
		if current >= p.MaxIntervalSeconds {
			current = p.MaxIntervalSeconds
			return
		}
		if current > p.MaxIntervalSeconds/2 {
			current = p.MaxIntervalSeconds
		} else {
			current *= 2
		}
		if current > p.MaxIntervalSeconds {
			current = p.MaxIntervalSeconds
		}
	}
	stepDown := func() {
		if current <= p.BaseIntervalSeconds {
			current = p.BaseIntervalSeconds
			return
		}
		current /= 2
		if current < p.BaseIntervalSeconds {
			current = p.BaseIntervalSeconds
		}
	}

	switch transition.Outcome {
	case OutcomeAuthSuspended:
		out.CircuitState = CircuitAuthSuspend
		resetSuccess()
		resetUnchanged()
		inc(&out.ConsecutiveFailure)
		current = p.MaxIntervalSeconds
	case OutcomeVersionUnsupported, OutcomeNotSupported:
		out.CircuitState = CircuitNotSupported
		resetSuccess()
		resetUnchanged()
		inc(&out.ConsecutiveFailure)
		current = p.MaxIntervalSeconds
	case OutcomeRateLimited:
		resetSuccess()
		resetUnchanged()
		inc(&out.ConsecutiveFailure)
		stepUp()
		if transition.RetryAfter > 0 {
			if transition.RetryAfter > time.Duration(p.MaxIntervalSeconds)*time.Second {
				out.CircuitState = CircuitManualSuspend
				out.RetryAfterUntil = now.Add(transition.RetryAfter)
				out.NextDueAt = out.RetryAfterUntil
				out.CurrentIntervalSeconds = current
				return out, nil
			}
			if transition.RetryAfter > time.Duration(current)*time.Second {
				current = int64(transition.RetryAfter / time.Second)
				if transition.RetryAfter%time.Second != 0 {
					current++
				}
			}
		}
	case OutcomeUnavailable:
		resetSuccess()
		resetUnchanged()
		inc(&out.ConsecutiveFailure)
		stepUp()
	case OutcomeBadResponse:
		resetSuccess()
		resetUnchanged()
		inc(&out.ConsecutiveFailure)
		stepUp()
	case OutcomePartial, OutcomePartialBudgetExhausted:
		resetSuccess()
		resetUnchanged()
		inc(&out.ConsecutiveFailure)
		// Partial data is conservative: never poll faster, and back off one step.
		stepUp()
		out.LastCoverageComplete = false
	case OutcomeSuccessChanged:
		resetUnchanged()
		out.ConsecutiveFailure = 0
		inc(&out.ConsecutiveSuccess)
		if out.ConsecutiveSuccess >= p.SuccessStepsBeforeRecovery {
			stepDown()
			out.ConsecutiveSuccess = 0
		}
		out.LastCoverageComplete = true
	case OutcomeSuccessUnchanged:
		out.ConsecutiveFailure = 0
		inc(&out.ConsecutiveSuccess)
		inc(&out.ConsecutiveUnchanged)
		if out.ConsecutiveUnchanged >= p.UnchangedStepsBeforeSlowdown {
			stepUp()
			out.ConsecutiveUnchanged = 0
		}
		out.LastCoverageComplete = true
	case OutcomeComplete:
		out.ConsecutiveFailure = 0
		resetUnchanged()
		inc(&out.ConsecutiveSuccess)
		if transition.Changed {
			out.ConsecutiveUnchanged = 0
		} else {
			inc(&out.ConsecutiveUnchanged)
		}
		if out.ConsecutiveSuccess >= p.SuccessStepsBeforeRecovery {
			stepDown()
			out.ConsecutiveSuccess = 0
		}
		out.LastCoverageComplete = true
	case OutcomeNotDue, OutcomeSourceBudgetWait, OutcomeSourceConcurrencyFull, OutcomeRunRequestLimit, OutcomeRunPageLimit, OutcomeRunRowLimit, OutcomeRunByteLimit, OutcomeRunCostLimit, OutcomeRunDeadline, OutcomeCursorStalled, OutcomeCursorCycle, OutcomeResponseTooLarge, OutcomeBudgetBackend:
		// Scheduling/guard outcomes do not count as upstream failure.  Keep the
		// current conservative interval and coverage state.
	default:
		return PollState{}, errors.New("unknown poll outcome")
	}

	if current < p.BaseIntervalSeconds {
		current = p.BaseIntervalSeconds
	}
	if current > p.MaxIntervalSeconds {
		current = p.MaxIntervalSeconds
	}
	out.CurrentIntervalSeconds = current
	delay := time.Duration(current) * time.Second
	if out.CircuitState == CircuitClosed {
		delay += DeterministicJitter(transition.Identity, transition.Slot, transition.PolicyVersion, delay, p.JitterPPM)
	}
	if !out.RetryAfterUntil.IsZero() && delay < out.RetryAfterUntil.Sub(now) {
		delay = out.RetryAfterUntil.Sub(now)
	}
	out.NextDueAt = now.Add(delay)
	return out, nil
}
