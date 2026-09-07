package httpapi

import (
	"errors"
	"regexp"
	"sync"
	"time"
)

// ReadinessCheckError names which one of /readyz's sequential checks failed.
//
// XM-INV-READYZ-DETAIL: the readiness closure (cmd/api/runtime.go) runs
// eleven checks in dependency order and short-circuits on the first failure,
// but every one of them used to surface as the identical
// `NOT_READY / required dependencies are unavailable` body, with the
// underlying error discarded entirely -- not even logged. A production
// incident spent six hours at 503 while an operator read the source and
// queried the database check by check to work out which one it was.
//
// The split between the three fields is the security boundary, and it is
// enforced at the response boundary rather than trusted here (see
// readinessFailureFields):
//
//   - Check is a stable machine identifier from a closed vocabulary of
//     constants (cmd/api/readiness.go). It is safe for the public body.
//   - Summary is one fixed operator-facing sentence, also a constant. It is
//     safe for the public body.
//   - Err is the real underlying error -- a database error carrying a DSN, a
//     dial error carrying an internal host and port, a store error carrying
//     an account id. It goes to the application log and MUST NEVER reach the
//     response body. /readyz is unauthenticated and internet-facing (no
//     s.require wrapper here, no allow/deny in
//     deploy/nginx/invoice.solov.cc.conf.template's `location = /readyz`).
type ReadinessCheckError struct {
	Check   string
	Summary string
	Err     error
}

func (e *ReadinessCheckError) Error() string {
	if e.Err == nil {
		return e.Check
	}
	return e.Check + ": " + e.Err.Error()
}

func (e *ReadinessCheckError) Unwrap() error { return e.Err }

// NotReady tags err with the readiness check that produced it. check and
// summary must be compile-time constants; anything else is rejected at the
// response boundary and degraded to the generic body.
func NotReady(check, summary string, err error) error {
	return &ReadinessCheckError{Check: check, Summary: summary, Err: err}
}

// readinessCheckNamePattern and readinessSummaryPattern are the fail-closed
// guard on everything /readyz publishes. They are deliberately far narrower
// than "not obviously secret": no digits, no dot, colon, slash, at-sign or
// percent in either. That is a structural guarantee rather than a matter of
// care at each call site -- a hostname, an IP address, a port, a DSN, a
// filesystem path, a UUID or an account id cannot be spelled within these
// character sets at all. A future edit that carelessly passes err.Error()
// through as the summary therefore cannot leak: it fails the pattern and the
// endpoint falls back to the original generic message (see
// readinessFailureFields).
var (
	readinessCheckNamePattern = regexp.MustCompile(`^[a-z][a-z_]{2,63}$`)
	readinessSummaryPattern   = regexp.MustCompile(`^[a-z][a-z -]{9,159}$`)
)

// readinessUnclassifiedCheck is the log-only check name for a readiness
// failure that carries no ReadinessCheckError -- nothing is published for it.
const readinessUnclassifiedCheck = "unclassified"

// readinessRejectedCheck is the log-only check name for a ReadinessCheckError
// whose Check or Summary failed the patterns above. It is distinct from
// readinessUnclassifiedCheck on purpose: an unclassified failure means a
// readiness closure this package did not tag, while a rejected one means the
// guard actually caught something and is a bug worth finding in the logs.
const readinessRejectedCheck = "rejected_check_name"

// readinessFailureFields splits a readiness error into what may be logged and
// what may be published. publishCheck/publishSummary are empty whenever the
// error is not a well-formed, pattern-clean ReadinessCheckError, and the
// caller then emits the pre-XM-INV-READYZ-DETAIL generic body unchanged.
func readinessFailureFields(err error) (logCheck, publishCheck, publishSummary string) {
	var named *ReadinessCheckError
	if !errors.As(err, &named) {
		return readinessUnclassifiedCheck, "", ""
	}
	if !readinessCheckNamePattern.MatchString(named.Check) || !readinessSummaryPattern.MatchString(named.Summary) {
		return readinessRejectedCheck, "", ""
	}
	return named.Check, named.Check, named.Summary
}

// readinessFailureLogInterval rate-limits the readiness failure log while the
// same check keeps failing. The production healthcheck probes /readyz every
// 10s (deploy/docker-compose.prod.yml), so an unrate-limited line would have
// written ~2000 entries over the six-hour incident and buried everything
// else. Five minutes matches eligibilityProofPendingWarnInterval, the
// existing precedent for exactly this problem in cmd/api/runtime.go.
const readinessFailureLogInterval = 5 * time.Minute

// readinessOutcomeLog decides which readiness evaluations are worth a log
// line. It is a small state machine over the last-reported check name:
// a newly failing check, or a different check than last time, always reports;
// the same check repeating stays quiet until the interval elapses; and the
// first success after any reported failure reports the recovery, so an
// operator reading the log sees a bounded episode with both ends rather than
// a wall of identical lines that simply stops.
type readinessOutcomeLog struct {
	mu        sync.Mutex
	lastCheck string
	lastAt    time.Time
}

// record reports whether this outcome should be logged, and advances the
// state machine. check is "" for a successful evaluation. A steady-state
// ready service is silent: "" following "" never reports.
func (l *readinessOutcomeLog) record(check string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if check == "" {
		if l.lastCheck == "" {
			return false
		}
		l.lastCheck, l.lastAt = "", time.Time{}
		return true
	}
	if l.lastCheck == check && !l.lastAt.IsZero() && now.Sub(l.lastAt) < readinessFailureLogInterval {
		return false
	}
	l.lastCheck, l.lastAt = check, now
	return true
}
