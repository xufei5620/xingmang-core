package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestHeartbeatArgsDeclareRetryQueueAndUniqueness(t *testing.T) {
	args := HeartbeatArgs{}
	if got := args.Kind(); got != HeartbeatJobKind {
		t.Fatalf("Kind() = %q, want %q", got, HeartbeatJobKind)
	}

	opts := args.InsertOpts()
	if opts.Queue != QueueMaintenance {
		t.Fatalf("heartbeat queue = %q, want %q", opts.Queue, QueueMaintenance)
	}
	if opts.MaxAttempts < 2 {
		t.Fatalf("heartbeat MaxAttempts = %d, want retryable value", opts.MaxAttempts)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("heartbeat must be unique by args")
	}
	if opts.UniqueOpts.ByPeriod <= 0 {
		t.Fatalf("heartbeat uniqueness period = %s, want positive period", opts.UniqueOpts.ByPeriod)
	}
	if !opts.UniqueOpts.ByQueue {
		t.Fatal("heartbeat uniqueness must be scoped to its queue")
	}
}

func TestHeartbeatWorkerRetriesThenSucceedsWithStructuredLogs(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	worker := NewHeartbeatWorker(logger, "development")
	job := &river.Job[HeartbeatArgs]{
		JobRow: &rivertype.JobRow{
			ID:          42,
			Kind:        HeartbeatJobKind,
			Queue:       QueueMaintenance,
			Attempt:     1,
			MaxAttempts: 3,
		},
		Args: HeartbeatArgs{FailuresBeforeSuccess: 1},
	}

	err := worker.Work(context.Background(), job)
	if !errors.Is(err, ErrHeartbeatRetry) {
		t.Fatalf("first attempt error = %v, want ErrHeartbeatRetry", err)
	}
	first := output.String()
	for _, want := range []string{
		`"event":"job_failed"`,
		`"module":"platform.jobs"`,
		`"environment":"development"`,
		`"principal_id":"worker:platform"`,
		`"job_id":42`,
		`"attempt":1`,
		`"success":false`,
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("first log missing %q: %s", want, first)
		}
	}

	output.Reset()
	job.Attempt = 2
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatalf("second attempt error = %v, want nil", err)
	}
	second := output.String()
	for _, want := range []string{
		`"event":"job_completed"`,
		`"module":"platform.jobs"`,
		`"success":true`,
		`"attempt":2`,
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("second log missing %q: %s", want, second)
		}
	}
}

func TestHeartbeatWorkerHonorsCancellation(t *testing.T) {
	worker := NewHeartbeatWorker(slog.Default(), "test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	job := &river.Job[HeartbeatArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Attempt: 1, MaxAttempts: 3},
	}
	if err := worker.Work(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled work error = %v, want context.Canceled", err)
	}
}

func TestHeartbeatWorkerZeroValueIsSafe(t *testing.T) {
	worker := HeartbeatWorker{}
	job := &river.Job[HeartbeatArgs]{
		JobRow: &rivertype.JobRow{ID: 8, Attempt: 1, MaxAttempts: 3},
	}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatalf("zero-value worker error = %v", err)
	}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.HeartbeatInterval <= 0 || cfg.MaxWorkers <= 0 {
		t.Fatalf("invalid defaults: %+v", cfg)
	}
	cfg.Environment = "test"
	pool, err := pgxpool.New(context.Background(), "postgres://localhost/xingmang")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := NewClient(pool, cfg); err != nil {
		t.Fatalf("client should be constructible before Start connects: %v", err)
	}

	bad := cfg
	bad.HeartbeatInterval = 500 * time.Millisecond
	if _, err := NewClient(pool, bad); err == nil {
		t.Fatal("sub-second heartbeat interval should be rejected")
	}

	bad = cfg
	bad.HeartbeatFailures = -1
	if _, err := NewClient(pool, bad); err == nil {
		t.Fatal("negative heartbeat failures should be rejected")
	}

	bad = cfg
	bad.Environment = "production"
	bad.HeartbeatRunID = "production-override"
	if _, err := NewClient(pool, bad); err == nil {
		t.Fatal("heartbeat run ID must not be configurable outside test environment")
	}

	bad = cfg
	bad.Environment = "production"
	bad.HeartbeatFailures = 1
	if _, err := NewClient(pool, bad); err == nil {
		t.Fatal("heartbeat failure injection must be disabled outside development/test")
	}

	bad = cfg
	bad.HeartbeatFailures = 3
	if _, err := NewClient(pool, bad); err == nil {
		t.Fatal("heartbeat failures must leave at least one successful attempt")
	}
}

func TestMigrateRejectsNilPool(t *testing.T) {
	if err := Migrate(context.Background(), nil, nil); err == nil {
		t.Fatal("nil pool must be rejected before migration work")
	}
}

func TestPinnedRiverMigrationMirror(t *testing.T) {
	migrator, err := NewMigrator(nil, nil)
	if err != nil {
		t.Fatalf("construct pinned migrator: %v", err)
	}
	if got := len(migrator.AllVersions()); got != 7 {
		t.Fatalf("pinned River main line has %d versions, want 7", got)
	}
}
