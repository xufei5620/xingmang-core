package sourceagent

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"time"
)

type PageSyncer interface {
	SyncPage(context.Context, ScanMode) (ScanPage, IngestAck, error)
}

type SyncCycleResult struct {
	Mode       ScanMode
	Pages      int
	Records    int
	LastBatch  string
	LastSeq    uint64
	Complete   bool
	Warnings   []string
	FinishedAt time.Time
}

type SyncFailure struct {
	Mode      ScanMode
	Err       error
	RetryIn   time.Duration
	Permanent bool
}

// SyncRunner executes one source stream. A production process must own exactly
// one runner, one FileStateStore and one source DB projection connection.
type SyncRunner struct {
	Coordinator            PageSyncer
	SourceType             string
	PollInterval           time.Duration
	ReconcileInterval      time.Duration
	FullScanInterval       time.Duration
	MaxBackoff             time.Duration
	MaxPagesPerCycle       int
	MaxConsecutiveFailures int
	Now                    func() time.Time
	Sleep                  func(context.Context, time.Duration) error
	Jitter                 func(time.Duration) time.Duration
	OnCycle                func(SyncCycleResult)
	OnFailure              func(SyncFailure)
	// Schedule persists the reconcile/full-scan schedule (XM-INV-AGENT-RESTART-GRACE
	// part C) so a process restart resumes it instead of forcing an immediate
	// ScanReconcile/ScanFull cycle. A nil Schedule keeps the pre-existing
	// behavior: every process start forces one initial complete cycle, purely
	// in memory, exactly as before this field existed.
	Schedule ScheduleStore
	// OnScheduleError reports a failure to load or persist Schedule. It is
	// purely observational: Schedule failures never stop or fail the runner --
	// the worst case is falling back to the always-safe, pre-existing
	// behavior of forcing another initial cycle on the next restart.
	OnScheduleError func(error)
}

func (r *SyncRunner) Validate() error {
	if r == nil || r.Coordinator == nil || (r.SourceType != SourceSub2API && r.SourceType != SourceNewAPI) {
		return errors.New("production sync runner is not configured")
	}
	if r.PollInterval < 5*time.Second || r.PollInterval > time.Hour {
		return errors.New("poll interval must be between 5 seconds and 1 hour")
	}
	if r.SourceType == SourceSub2API && (r.ReconcileInterval < r.PollInterval || r.ReconcileInterval > 7*24*time.Hour) {
		return errors.New("Sub2API reconcile interval must be between poll interval and 7 days")
	}
	if r.SourceType == SourceNewAPI && (r.FullScanInterval < r.PollInterval || r.FullScanInterval > 24*time.Hour) {
		return errors.New("New API full scan interval must be between poll interval and 24 hours")
	}
	if r.MaxBackoff < time.Second || r.MaxBackoff > time.Hour {
		return errors.New("maximum backoff must be between 1 second and 1 hour")
	}
	if r.MaxPagesPerCycle < 1 || r.MaxPagesPerCycle > 10000 {
		return errors.New("max pages per cycle must be between 1 and 10000")
	}
	if r.MaxConsecutiveFailures < 1 || r.MaxConsecutiveFailures > 1000 {
		return errors.New("max consecutive failures must be between 1 and 1000")
	}
	return nil
}

// Run resumes the persisted reconcile/full-scan schedule (Schedule) across a
// process restart when one is configured and recorded; otherwise, exactly as
// before this schedule existed, it starts with a complete reconciliation/full
// scan. New API then performs mandatory full scans on the configured bounded
// cadence; Sub2API performs keyset incrementals between reconciliations.
func (r *SyncRunner) Run(ctx context.Context) error {
	if err := r.Validate(); err != nil {
		return err
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	sleep := sleepContext
	if r.Sleep != nil {
		sleep = r.Sleep
	}
	jitter := positiveJitter
	if r.Jitter != nil {
		jitter = r.Jitter
	}

	var lastReconcile, lastFull time.Time
	if r.Schedule != nil {
		lastReconcile, lastFull = r.loadSchedule(ctx)
	}
	backoff := time.Second
	consecutiveFailures := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		started := now().UTC()
		mode := r.modeAt(started, lastReconcile, lastFull)
		result, err := r.runCycle(ctx, mode, now)
		if err != nil {
			var ingestErr *IngestHTTPError
			if errors.As(err, &ingestErr) && ingestErr.Permanent() {
				if r.OnFailure != nil {
					r.OnFailure(SyncFailure{Mode: mode, Err: err, Permanent: true})
				}
				return err
			}
			// Only the explicit scan-cycle-busy rejection (503 with a positive
			// Retry-After and SOURCE_SCAN_CYCLE_BUSY) means the stream already has a
			// legitimate cycle processing, which alone can take up to ~30 minutes.
			// That is not evidence of a broken receiver, so it must not spend the
			// consecutive-failure budget the way a real transient failure does --
			// otherwise a long-running cycle alone trips the circuit breaker and
			// restarts the process into an unnecessary reconcile sweep. Genuine
			// transient failures (network errors, other 5xx) still count normally.
			busy := errors.As(err, &ingestErr) && ingestErr.StatusCode == http.StatusServiceUnavailable &&
				ingestErr.RetryAfter > 0 && ingestErr.Code == "SOURCE_SCAN_CYCLE_BUSY"
			if !busy {
				consecutiveFailures++
				if consecutiveFailures >= r.MaxConsecutiveFailures {
					if r.OnFailure != nil {
						r.OnFailure(SyncFailure{Mode: mode, Err: err, Permanent: true})
					}
					return errors.Join(errors.New("source stream circuit opened after consecutive failures"), err)
				}
			}
			wait := backoff
			if wait > r.MaxBackoff {
				wait = r.MaxBackoff
			}
			// A valid receiver Retry-After (already bounded to one hour by the
			// transport) is a minimum and is not shortened by MaxBackoff.
			if errors.As(err, &ingestErr) && ingestErr.RetryAfter > wait {
				wait = ingestErr.RetryAfter
			}
			wait = jitter(wait)
			if r.OnFailure != nil {
				r.OnFailure(SyncFailure{Mode: mode, Err: err, RetryIn: wait})
			}
			if err := sleep(ctx, wait); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return err
			}
			backoff *= 2
			if backoff > r.MaxBackoff {
				backoff = r.MaxBackoff
			}
			continue
		}
		backoff = time.Second
		consecutiveFailures = 0
		if result.Complete && mode == ScanReconcile {
			lastReconcile = result.FinishedAt
			r.saveSchedule(ctx, lastReconcile, lastFull)
		}
		if result.Complete && mode == ScanFull {
			lastFull = result.FinishedAt
			r.saveSchedule(ctx, lastReconcile, lastFull)
		}
		if r.OnCycle != nil {
			r.OnCycle(result)
		}
		if err := sleep(ctx, jitter(r.PollInterval)); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}

// loadSchedule best-effort restores the persisted reconcile/full-scan
// schedule. Any failure -- store unavailable, corrupt/unparseable timestamp,
// or a state file written before Schedule existed -- resolves to the zero
// value for that timestamp, which is exactly today's pre-existing behavior
// (forces one initial cycle of that kind). It never fails Run.
func (r *SyncRunner) loadSchedule(ctx context.Context) (lastReconcile, lastFull time.Time) {
	state, err := r.Schedule.Load(ctx)
	if err != nil {
		if r.OnScheduleError != nil {
			r.OnScheduleError(fmt.Errorf("load persisted reconcile/full schedule: %w", err))
		}
		return time.Time{}, time.Time{}
	}
	if state.LastReconcileAt != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, state.LastReconcileAt); parseErr == nil {
			lastReconcile = parsed
		}
	}
	if state.LastFullAt != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, state.LastFullAt); parseErr == nil {
			lastFull = parsed
		}
	}
	return lastReconcile, lastFull
}

// saveSchedule best-effort persists the reconcile/full-scan schedule after a
// cycle of that kind completes. A save failure is reported but never stops
// the runner: the worst case on the next restart is a forced initial cycle,
// which is the safe, pre-existing behavior.
func (r *SyncRunner) saveSchedule(ctx context.Context, lastReconcile, lastFull time.Time) {
	if r.Schedule == nil {
		return
	}
	state := ScheduleState{LastReconcileAt: formatScheduleTime(lastReconcile), LastFullAt: formatScheduleTime(lastFull)}
	if err := r.Schedule.Save(ctx, state); err != nil && r.OnScheduleError != nil {
		r.OnScheduleError(fmt.Errorf("persist reconcile/full schedule: %w", err))
	}
}

func formatScheduleTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (r *SyncRunner) modeAt(now, lastReconcile, lastFull time.Time) ScanMode {
	if r.SourceType == SourceNewAPI {
		if lastFull.IsZero() || now.Sub(lastFull) >= r.FullScanInterval {
			return ScanFull
		}
		return ScanIncremental
	}
	if lastReconcile.IsZero() || now.Sub(lastReconcile) >= r.ReconcileInterval {
		return ScanReconcile
	}
	return ScanIncremental
}

func (r *SyncRunner) runCycle(ctx context.Context, mode ScanMode, now func() time.Time) (SyncCycleResult, error) {
	result := SyncCycleResult{Mode: mode}
	warnings := map[string]struct{}{}
	for result.Pages < r.MaxPagesPerCycle {
		page, ack, err := r.Coordinator.SyncPage(ctx, mode)
		if err != nil {
			return SyncCycleResult{}, err
		}
		result.Pages++
		result.Records += len(page.Projections)
		result.LastBatch = ack.BatchID
		result.LastSeq = ack.Sequence
		for _, warning := range page.Warnings {
			if warning != "" {
				warnings[warning] = struct{}{}
			}
		}
		result.Warnings = result.Warnings[:0]
		for warning := range warnings {
			result.Warnings = append(result.Warnings, warning)
		}
		sort.Strings(result.Warnings)
		if !page.HasMore {
			result.Complete = true
			result.FinishedAt = now().UTC()
			return result, nil
		}
	}
	result.FinishedAt = now().UTC()
	return result, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// positiveJitter adds 0-20% using crypto/rand so replicas do not synchronize.
func positiveJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	ceiling := base / 5
	if ceiling <= 0 {
		return base
	}
	random, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(ceiling)+1))
	if err != nil {
		return base
	}
	return base + time.Duration(random.Int64())
}
