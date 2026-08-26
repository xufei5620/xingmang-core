package jobs

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const (
	DefaultHeartbeatInterval = time.Minute
	DefaultMaxWorkers        = 1
	DefaultHeartbeatAttempts = 0
)

// Config controls the worker process without exposing River's whole config
// surface to callers. It keeps the baseline intentionally small and explicit.
type Config struct {
	Logger              *slog.Logger
	Environment         string
	HeartbeatInterval   time.Duration
	HeartbeatRunOnStart bool
	// HeartbeatRunID is reserved for isolated integration tests. Production
	// clients must leave it empty so all instances share one unique heartbeat.
	HeartbeatRunID string
	// HeartbeatFailures is a bounded failure-injection switch for development
	// and integration tests; production clients must leave it at zero.
	HeartbeatFailures int
	MaxWorkers        int
}

// DefaultConfig returns the safe local-development baseline.
func DefaultConfig() Config {
	return Config{
		// Environment is intentionally empty: callers must declare it rather
		// than inheriting a production-capable default.
		Environment:         "",
		HeartbeatInterval:   DefaultHeartbeatInterval,
		HeartbeatRunOnStart: true,
		HeartbeatFailures:   DefaultHeartbeatAttempts,
		MaxWorkers:          DefaultMaxWorkers,
	}
}

func (c Config) normalized() Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(c.Environment) == "" {
		c.Environment = defaults.Environment
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = defaults.HeartbeatInterval
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = defaults.MaxWorkers
	}
	if c.Logger == nil {
		c.Logger = structuredDefaultLogger()
	}
	return c
}

func (c Config) validate() error {
	if c.HeartbeatInterval < time.Second {
		return fmt.Errorf("heartbeat interval %s is below River's one-second minimum", c.HeartbeatInterval)
	}
	if c.MaxWorkers < 1 {
		return fmt.Errorf("max workers must be positive, got %d", c.MaxWorkers)
	}
	if c.HeartbeatFailures < 0 {
		return fmt.Errorf("heartbeat failures must not be negative, got %d", c.HeartbeatFailures)
	}
	if c.HeartbeatFailures > heartbeatMaxAttempts-1 {
		return fmt.Errorf("heartbeat failures must be less than max attempts (%d), got %d", heartbeatMaxAttempts, c.HeartbeatFailures)
	}
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("environment must not be empty")
	}
	if c.HeartbeatFailures > 0 && c.Environment != "development" && c.Environment != "test" {
		return fmt.Errorf("heartbeat failure injection is only allowed in development/test")
	}
	if c.HeartbeatRunID != "" && c.Environment != "test" {
		return fmt.Errorf("heartbeat run ID is reserved for test environment")
	}
	return nil
}

// NewClient builds a River client with the two baseline queues and one
// periodic heartbeat. The pool is only used when the client is started or a
// job is inserted; construction itself does not connect to Postgres.
func NewClient(pool *pgxpool.Pool, cfg Config) (*river.Client[pgx.Tx], error) {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, NewHeartbeatWorker(cfg.Logger, cfg.Environment))

	heartbeat := river.NewPeriodicJob(
		river.PeriodicInterval(cfg.HeartbeatInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			args := HeartbeatArgs{
				RunID:                 cfg.HeartbeatRunID,
				FailuresBeforeSuccess: cfg.HeartbeatFailures,
			}
			opts := args.InsertOpts()
			// Keep periodic uniqueness aligned with a configured cadence. The
			// args-level default remains one minute for ad-hoc inserts.
			opts.UniqueOpts.ByPeriod = cfg.HeartbeatInterval
			return args, &opts
		},
		&river.PeriodicJobOpts{
			ID:         HeartbeatJobKind,
			RunOnStart: cfg.HeartbeatRunOnStart,
		},
	)

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger: cfg.Logger,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: cfg.MaxWorkers},
			QueueMaintenance:   {MaxWorkers: cfg.MaxWorkers},
		},
		Workers:      workers,
		PeriodicJobs: []*river.PeriodicJob{heartbeat},
	})
}
