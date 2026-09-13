package sourceagent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type scriptedPageSyncer struct {
	pages []ScanPage
	modes []ScanMode
	err   error
}

func (s *scriptedPageSyncer) SyncPage(_ context.Context, mode ScanMode) (ScanPage, IngestAck, error) {
	s.modes = append(s.modes, mode)
	if s.err != nil {
		return ScanPage{}, IngestAck{}, s.err
	}
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, IngestAck{Accepted: true, BatchID: "batch", Sequence: uint64(len(s.modes))}, nil
}

// memoryScheduleStore is a minimal in-memory ScheduleStore fake used only to
// test SyncRunner's own load/save wiring (XM-INV-AGENT-RESTART-GRACE part
// C); FileScheduleStore's durable behavior is covered separately in
// state_store_file_test.go.
type memoryScheduleStore struct {
	state   ScheduleState
	loadErr error
	saveErr error
	saves   []ScheduleState
}

func (m *memoryScheduleStore) Load(context.Context) (ScheduleState, error) {
	return m.state, m.loadErr
}

func (m *memoryScheduleStore) Save(_ context.Context, state ScheduleState) error {
	m.saves = append(m.saves, state)
	if m.saveErr != nil {
		return m.saveErr
	}
	m.state = state
	return nil
}

func TestRunnerForcesNewAPIFullAndSub2APIReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	newAPI := &SyncRunner{SourceType: SourceNewAPI, FullScanInterval: time.Hour}
	if mode := newAPI.modeAt(now, time.Time{}, time.Time{}); mode != ScanFull {
		t.Fatalf("initial New API mode = %q", mode)
	}
	if mode := newAPI.modeAt(now, time.Time{}, now.Add(-30*time.Minute)); mode != ScanIncremental {
		t.Fatalf("between New API full scans mode = %q", mode)
	}
	if mode := newAPI.modeAt(now, time.Time{}, now.Add(-time.Hour)); mode != ScanFull {
		t.Fatalf("due New API full scan mode = %q", mode)
	}
	sub2 := &SyncRunner{SourceType: SourceSub2API, ReconcileInterval: 6 * time.Hour}
	if mode := sub2.modeAt(now, time.Time{}, time.Time{}); mode != ScanReconcile {
		t.Fatalf("initial Sub2API mode = %q", mode)
	}
	if mode := sub2.modeAt(now, now.Add(-time.Hour), time.Time{}); mode != ScanIncremental {
		t.Fatalf("Sub2API incremental mode = %q", mode)
	}
}

func TestRunnerDrainsBoundedPages(t *testing.T) {
	syncer := &scriptedPageSyncer{pages: []ScanPage{
		{Projections: []Projection{{}}, HasMore: true},
		{Projections: []Projection{{}, {}}, HasMore: false},
	}}
	runner := &SyncRunner{Coordinator: syncer, MaxPagesPerCycle: 3}
	result, err := runner.runCycle(context.Background(), ScanFull, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pages != 2 || result.Records != 3 || !result.Complete || len(syncer.modes) != 2 || syncer.modes[0] != ScanFull || syncer.modes[1] != ScanFull {
		t.Fatalf("unexpected cycle: %#v modes=%#v", result, syncer.modes)
	}
}

func TestRunnerYieldsAfterMaxPagesWithoutMarkingFullScanComplete(t *testing.T) {
	syncer := &scriptedPageSyncer{pages: []ScanPage{
		{Projections: []Projection{{}}, HasMore: true},
		{Projections: []Projection{{}}, HasMore: true},
	}}
	runner := &SyncRunner{Coordinator: syncer, MaxPagesPerCycle: 2}
	result, err := runner.runCycle(context.Background(), ScanFull, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Pages != 2 {
		t.Fatalf("bounded partial scan was misclassified: %#v", result)
	}
}

func TestRunnerStopsOnPermanentIngestionFailure(t *testing.T) {
	syncer := &scriptedPageSyncer{err: &IngestHTTPError{StatusCode: http.StatusConflict}}
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceNewAPI,
		PollInterval: 5 * time.Second, FullScanInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10,
		MaxConsecutiveFailures: 10,
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("permanent conflict must not sleep/retry")
			return nil
		},
	}
	err := runner.Run(context.Background())
	var ingestErr *IngestHTTPError
	if !errors.As(err, &ingestErr) || ingestErr.StatusCode != http.StatusConflict {
		t.Fatalf("unexpected permanent error: %v", err)
	}
}

func TestRunnerDoesNotShortenRetryAfter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	syncer := &scriptedPageSyncer{err: &IngestHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 3 * time.Minute}}
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceNewAPI,
		PollInterval: 5 * time.Second, FullScanInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10,
		MaxConsecutiveFailures: 10,
		Jitter:                 func(value time.Duration) time.Duration { return value },
		Sleep: func(_ context.Context, duration time.Duration) error {
			if duration != 3*time.Minute {
				t.Fatalf("Retry-After was shortened to %s", duration)
			}
			cancel()
			return context.Canceled
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerOpensCircuitAfterBoundedTransientFailures(t *testing.T) {
	syncer := &scriptedPageSyncer{err: errors.New("database contract unavailable")}
	sleeps := 0
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceSub2API,
		PollInterval: 5 * time.Second, ReconcileInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 2,
		Jitter: func(value time.Duration) time.Duration { return value },
		Sleep:  func(context.Context, time.Duration) error { sleeps++; return nil },
	}
	err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "circuit opened") {
		t.Fatalf("expected bounded circuit-open error, got %v", err)
	}
	if len(syncer.modes) != 2 || sleeps != 1 {
		t.Fatalf("unexpected retry count: calls=%d sleeps=%d", len(syncer.modes), sleeps)
	}
}

func TestRunnerScanCycleBusyBacksOffWithoutOpeningCircuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	syncer := &scriptedPageSyncer{err: &IngestHTTPError{StatusCode: http.StatusServiceUnavailable, RetryAfter: 30 * time.Second, Code: "SOURCE_SCAN_CYCLE_BUSY"}}
	attempts := 0
	const observeAttempts = 5 // far more than MaxConsecutiveFailures below
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceNewAPI,
		PollInterval: 5 * time.Second, FullScanInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10,
		MaxConsecutiveFailures: 2, // deliberately tiny: busy responses must never count against it
		Jitter:                 func(value time.Duration) time.Duration { return value },
		Sleep: func(_ context.Context, duration time.Duration) error {
			attempts++
			if duration != 30*time.Second {
				t.Fatalf("attempt %d: busy retry did not honor Retry-After: waited %s", attempts, duration)
			}
			if attempts >= observeAttempts {
				cancel()
				return context.Canceled
			}
			return nil
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("scan-cycle-busy responses must never open the circuit or return a permanent error: %v", err)
	}
	if attempts < observeAttempts {
		t.Fatalf("expected at least %d retried attempts, got %d", observeAttempts, attempts)
	}
	// One SyncPage call precedes each Sleep call within the same failed iteration,
	// so the coordinator is called exactly once per observed busy attempt.
	if len(syncer.modes) < observeAttempts {
		t.Fatalf("coordinator (same batch/sequence source) was not retried on every busy attempt: calls=%d", len(syncer.modes))
	}
}

func TestRunnerResumesPersistedReconcileScheduleAcrossRestart(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	schedule := &memoryScheduleStore{state: ScheduleState{LastReconcileAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano)}}
	syncer := &scriptedPageSyncer{pages: []ScanPage{{HasMore: false}}}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceSub2API,
		PollInterval: 5 * time.Second, ReconcileInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 10,
		Now:      func() time.Time { return now },
		Schedule: schedule,
		Sleep:    func(context.Context, time.Duration) error { cancel(); return context.Canceled },
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(syncer.modes) != 1 || syncer.modes[0] != ScanIncremental {
		t.Fatalf("restart forced an unnecessary cycle despite a fresh persisted schedule: modes=%#v", syncer.modes)
	}
}

func TestRunnerPersistsCompletedReconcileScheduleForNextRestart(t *testing.T) {
	schedule := &memoryScheduleStore{}
	syncer := &scriptedPageSyncer{pages: []ScanPage{{HasMore: false}}}
	ctx, cancel := context.WithCancel(context.Background())
	finishedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceSub2API,
		PollInterval: 5 * time.Second, ReconcileInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 10,
		Now:      func() time.Time { return finishedAt },
		Schedule: schedule,
		Sleep:    func(context.Context, time.Duration) error { cancel(); return context.Canceled },
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(schedule.saves) != 1 || schedule.saves[0].LastReconcileAt != finishedAt.Format(time.RFC3339Nano) || schedule.saves[0].LastFullAt != "" {
		t.Fatalf("completed reconcile cycle was not persisted: %#v", schedule.saves)
	}

	// A second runner instance loading from the same store must resume
	// Incremental instead of forcing another reconcile -- proving the
	// persisted value, not just the Save call shape, drives modeAt.
	resumedSyncer := &scriptedPageSyncer{pages: []ScanPage{{HasMore: false}}}
	ctx2, cancel2 := context.WithCancel(context.Background())
	resumed := &SyncRunner{
		Coordinator: resumedSyncer, SourceType: SourceSub2API,
		PollInterval: 5 * time.Second, ReconcileInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 10,
		Now:      func() time.Time { return finishedAt.Add(10 * time.Minute) },
		Schedule: schedule,
		Sleep:    func(context.Context, time.Duration) error { cancel2(); return context.Canceled },
	}
	if err := resumed.Run(ctx2); err != nil {
		t.Fatal(err)
	}
	if len(resumedSyncer.modes) != 1 || resumedSyncer.modes[0] != ScanIncremental {
		t.Fatalf("restart did not resume the persisted schedule: modes=%#v", resumedSyncer.modes)
	}
}

func TestRunnerScheduleLoadFailureFallsBackToForcedInitialCycle(t *testing.T) {
	schedule := &memoryScheduleStore{loadErr: errors.New("boom")}
	syncer := &scriptedPageSyncer{pages: []ScanPage{{HasMore: false}}}
	ctx, cancel := context.WithCancel(context.Background())
	var reported error
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceSub2API,
		PollInterval: 5 * time.Second, ReconcileInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 10,
		Schedule:        schedule,
		OnScheduleError: func(err error) { reported = err },
		Sleep:           func(context.Context, time.Duration) error { cancel(); return context.Canceled },
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(syncer.modes) != 1 || syncer.modes[0] != ScanReconcile {
		t.Fatalf("schedule load failure must fall back to the safe forced initial cycle: modes=%#v", syncer.modes)
	}
	if reported == nil {
		t.Fatal("schedule load failure was not reported via OnScheduleError")
	}
}

func TestRunnerNilScheduleKeepsPreExistingBehavior(t *testing.T) {
	syncer := &scriptedPageSyncer{pages: []ScanPage{{HasMore: false}}}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &SyncRunner{
		Coordinator: syncer, SourceType: SourceSub2API,
		PollInterval: 5 * time.Second, ReconcileInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 10,
		Sleep: func(context.Context, time.Duration) error { cancel(); return context.Canceled },
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(syncer.modes) != 1 || syncer.modes[0] != ScanReconcile {
		t.Fatalf("a nil Schedule must keep forcing the initial cycle exactly as before: modes=%#v", syncer.modes)
	}
}

func TestPositiveJitterIsBounded(t *testing.T) {
	base := 10 * time.Second
	for range 100 {
		value := positiveJitter(base)
		if value < base || value > 12*time.Second {
			t.Fatalf("jitter %s is outside expected range", value)
		}
	}
}
