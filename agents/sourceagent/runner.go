package sourceagent

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"math/big"
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

// Run starts with a complete reconciliation/full scan after every process
// restart. New API then performs mandatory full scans on the configured bounded
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
			consecutiveFailures++
			var ingestErr *IngestHTTPError
			if errors.As(err, &ingestErr) && ingestErr.Permanent() {
				if r.OnFailure != nil {
					r.OnFailure(SyncFailure{Mode: mode, Err: err, Permanent: true})
				}
				return err
			}
			if consecutiveFailures >= r.MaxConsecutiveFailures {
				if r.OnFailure != nil {
					r.OnFailure(SyncFailure{Mode: mode, Err: err, Permanent: true})
				}
				return errors.Join(errors.New("source stream circuit opened after consecutive failures"), err)
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
		}
		if result.Complete && mode == ScanFull {
			lastFull = result.FinishedAt
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
