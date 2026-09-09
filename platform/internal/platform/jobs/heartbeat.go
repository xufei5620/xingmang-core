package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	// HeartbeatJobKind is the stable River kind for the demonstration job.
	HeartbeatJobKind = "platform_heartbeat"

	// QueueMaintenance isolates platform maintenance work from the default
	// queue. It is intentionally a plain queue name, not a workflow concept.
	QueueMaintenance = "maintenance"

	heartbeatPrincipalID  = "worker:platform"
	heartbeatErrorCode    = "heartbeat_retry"
	heartbeatUniquePeriod = time.Minute
	heartbeatMaxAttempts  = 3

	// MetricPlatformHeartbeat is the ops metric key recording each
	// successful heartbeat (XM-OPS0). It lets the ops overview query answer
	// "is the worker process alive" using the same freshness model as every
	// other observation, instead of a bespoke liveness probe: once the
	// heartbeat stops writing, staleness grows on its own.
	MetricPlatformHeartbeat = "platform.heartbeat"

	// HeartbeatStalenessThresholdSeconds is 3x the default heartbeat
	// interval (60s), giving a couple of missed cycles of slack before the
	// ops page flags the worker as stale rather than flapping on one slow
	// beat.
	HeartbeatStalenessThresholdSeconds int32 = 180
)

// ErrHeartbeatRetry is returned only by the demonstration failure switch. It
// lets integration tests prove River's retry behavior without introducing a
// business job or an external dependency.
var ErrHeartbeatRetry = errors.New("heartbeat demonstration retry")

// HeartbeatArgs are the serializable arguments for the demonstration job.
// FailuresBeforeSuccess is deliberately bounded to test data and defaults to
// zero in production, so the periodic heartbeat succeeds normally.
type HeartbeatArgs struct {
	RunID                 string `json:"run_id,omitempty"`
	FailuresBeforeSuccess int    `json:"failures_before_success,omitempty"`
}

func (HeartbeatArgs) Kind() string { return HeartbeatJobKind }

// InsertOpts gives every heartbeat the same retry and uniqueness policy. The
// period prevents duplicate heartbeat records in the same time window while
// retaining a durable job row for observability.
func (HeartbeatArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: heartbeatMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: heartbeatUniquePeriod,
			ByQueue:  true,
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// HeartbeatWorker emits one structured record for each completed or failed
// attempt. It never logs job arguments, which keeps the pattern safe if the
// demo arguments are expanded later.
type HeartbeatWorker struct {
	river.WorkerDefaults[HeartbeatArgs]

	logger      *slog.Logger
	environment string

	// observations and expectedInterval are set together via
	// WithObservations; see its comment for why this is a fluent setter
	// instead of a constructor parameter.
	observations     ObservationStore
	expectedInterval time.Duration
}

// NewHeartbeatWorker constructs a worker with safe defaults for logger and
// environment labels.
func NewHeartbeatWorker(logger *slog.Logger, environment string) *HeartbeatWorker {
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(environment) == "" {
		environment = "unknown"
	}
	return &HeartbeatWorker{logger: logger, environment: environment}
}

// WithObservations attaches an optional observation sink (XM-OPS0). When
// set, every successful heartbeat also writes an ops.Observation (metric key
// MetricPlatformHeartbeat) so the ops overview query can show worker
// liveness without tailing structured logs or reaching into River's own job
// table.
//
// This is a fluent setter rather than a third constructor parameter: the two
// existing call sites (client.go's production wiring, this file's own tests)
// already call NewHeartbeatWorker positionally, and changing that signature
// would be a breaking change for both. The zero value (never calling this
// method) preserves the exact pre-XM-OPS0 behavior: log-only, no store
// dependency.
func (w *HeartbeatWorker) WithObservations(store ObservationStore, expectedInterval time.Duration) *HeartbeatWorker {
	w.observations = store
	w.expectedInterval = expectedInterval
	return w
}

func (w *HeartbeatWorker) Work(ctx context.Context, job *river.Job[HeartbeatArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	logger := w.logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment := w.environment
	if strings.TrimSpace(environment) == "" {
		environment = "unknown"
	}

	jobID, kind, queue, attempt, maxAttempts := heartbeatJobFields(job)
	attrs := []slog.Attr{
		slog.String("event", "job_failed"),
		slog.String("module", "platform.jobs"),
		slog.String("environment", environment),
		slog.String("principal_id", heartbeatPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", kind),
		slog.String("queue", queue),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
	}

	if job != nil && job.Args.FailuresBeforeSuccess >= attempt {
		failedAttrs := append(attrs,
			slog.Bool("success", false),
			slog.String("error_code", heartbeatErrorCode),
		)
		logger.LogAttrs(ctx, slog.LevelWarn, "job_failed", failedAttrs...)
		return ErrHeartbeatRetry
	}

	completedAttrs := []slog.Attr{
		slog.String("event", "job_completed"),
		slog.String("module", "platform.jobs"),
		slog.String("environment", environment),
		slog.String("principal_id", heartbeatPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", kind),
		slog.String("queue", queue),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", true),
		slog.String("error_code", ""),
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "job_completed", completedAttrs...)

	// XM-OPS0: record this heartbeat as an observation, if a sink was
	// attached. A write failure here does not fail the job — the heartbeat's
	// own purpose (proving the worker loop runs) is already satisfied by
	// reaching this line, and losing one sample is recoverable next round.
	if w.observations != nil {
		now := time.Now().UTC()
		obs := ops.Observation{
			MetricKey:                 MetricPlatformHeartbeat,
			Source:                    heartbeatPrincipalID,
			Environment:               environment,
			SyncedAt:                  now,
			Status:                    ops.SyncOK,
			ObservedAt:                &now,
			LastSuccess:               &now,
			StalenessThresholdSeconds: HeartbeatStalenessThresholdSeconds,
			Value: map[string]any{
				"job_id":  jobID,
				"attempt": attempt,
			},
		}
		annotateRollupMetadata(&obs, w.expectedInterval)
		if _, err := w.observations.UpsertWithSample(ctx, obs); err != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "heartbeat_observation_write_failed",
				slog.String("event", "heartbeat_observation_write_failed"),
				slog.String("module", "platform.jobs"),
				slog.String("environment", environment),
				slog.String("principal_id", heartbeatPrincipalID),
				slog.String("error_code", "observation_write_failed"),
				slog.Any("err", err))
		}
	}
	return nil
}

func heartbeatJobFields(job *river.Job[HeartbeatArgs]) (int64, string, string, int, int) {
	if job == nil || job.JobRow == nil {
		return 0, HeartbeatJobKind, QueueMaintenance, 1, heartbeatMaxAttempts
	}
	kind := job.Kind
	if kind == "" {
		kind = HeartbeatJobKind
	}
	queue := job.Queue
	if queue == "" {
		queue = QueueMaintenance
	}
	attempt := job.Attempt
	if attempt < 1 {
		attempt = 1
	}
	maxAttempts := job.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = heartbeatMaxAttempts
	}
	return job.ID, kind, queue, attempt, maxAttempts
}
