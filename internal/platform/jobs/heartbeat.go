package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
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
