package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// XM-OPS0 connector health probe.
//
// This periodic job answers one question the sync jobs (sub2api_sync /
// newapi_sync) do not: "is the connector itself reachable and running a
// compatible version", independent of whether any business data (users,
// revenue, channels, ...) happened to sync successfully this round. It never
// touches the request path -- the ops overview HTTP query only ever reads
// what this job already wrote to ops.metric_observation, never calls
// Version()/Health() itself.

const (
	// ConnectorProbeJobKind is the stable River kind for this job.
	ConnectorProbeJobKind = "connector_probe"

	// DefaultConnectorProbeInterval matches the Sub2API/NewAPI/FinanceCollect
	// precedent of "5 minutes": frequent enough that the ops page never
	// looks stale, infrequent enough not to hammer either upstream's admin
	// API just to ask "are you there".
	DefaultConnectorProbeInterval = 5 * time.Minute

	// ConnectorProbeStalenessThresholdSeconds is a fixed 900s (15m): three
	// missed 5-minute cycles of slack before the ops page flags a
	// connector's health as stale. Fixed rather than derived from the
	// configured interval, per the ops overview design.
	ConnectorProbeStalenessThresholdSeconds int32 = 900

	// MetricSub2APIConnectorHealth / MetricNewAPIConnectorHealth are the ops
	// metric keys this job writes. Each observation's Value is
	// {"version","supported","healthy","kind","latency_ms","checked_at"}:
	// version/supported come from connector.VersionInfo, healthy/latency_ms/
	// checked_at from connector.HealthResult, and "kind" is
	// HealthResult.ErrorKind (empty string when healthy).
	MetricSub2APIConnectorHealth = "sub2api.connector.health"
	MetricNewAPIConnectorHealth  = "newapi.connector.health"

	connectorProbePrincipalID  = "worker:platform"
	connectorProbeModule       = "platform.jobs"
	connectorProbeMaxAttempts  = 3
	connectorProbeUniquePeriod = DefaultConnectorProbeInterval
)

// ConnectorProbeArgs are the serializable arguments for the probe job.
type ConnectorProbeArgs struct {
	// RunID is reserved for isolated integration tests. Production clients
	// must leave it empty so all replicas share one unique probe record.
	RunID string `json:"run_id,omitempty"`
}

// Kind returns the stable River kind.
func (ConnectorProbeArgs) Kind() string { return ConnectorProbeJobKind }

// InsertOpts gives every probe run the same retry and uniqueness policy.
func (ConnectorProbeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: connectorProbeMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: connectorProbeUniquePeriod,
			ByQueue:  true,
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// probeReadClient is the minimal read surface the probe needs from either
// connector's ReadClientV2. Declared locally instead of importing
// connectors/sub2api or connectors/newapi: both packages' ReadClient
// interfaces already expose exactly these two methods returning
// internal/platform/connector's shared VersionInfo/HealthResult types, so
// any sub2api.ReadClientV2 or newapi.ReadClientV2 value satisfies this
// interface structurally with no explicit conversion or extra dependency.
type probeReadClient interface {
	Version(ctx context.Context) (connector.VersionInfo, error)
	Health(ctx context.Context) (connector.HealthResult, error)
}

// probeClientFactory builds a probeReadClient for one connector round.
//
// This is deliberately narrower than Sub2APIClientFactory/NewAPIClientFactory
// (which return the full sub2api.ReadClientV2/newapi.ReadClientV2 -- many
// methods this worker never calls): the probe only ever calls Version and
// Health, so its dependency surface should say so. Production wiring adapts
// cfg.sub2apiClientFactory()/cfg.newapiClientFactory() into this shape with a
// one-line closure in client.go (any *ReadClientV2 already satisfies
// probeReadClient structurally); tests can inject a minimal stub directly
// without implementing either connector's full interface.
type probeClientFactory func(ctx context.Context) (probeReadClient, error)

// ConnectorProbeOptions are the Worker's construction parameters.
type ConnectorProbeOptions struct {
	Logger      *slog.Logger
	Environment string
	// Store is where probe results land; *ops.Store satisfies it. Reuses
	// the same narrow ObservationStore interface (Get + UpsertWithSample)
	// the sync workers already depend on.
	Store ObservationStore

	// Sub2APISource / NewAPISource become each observation's Source field.
	// Callers should pass the same InstanceID used for the corresponding
	// sync job, so "which connector instance" reads consistently across
	// the sync-pipeline metrics and this health-probe metric.
	Sub2APISource    string
	Sub2APINewClient probeClientFactory
	NewAPISource     string
	NewAPINewClient  probeClientFactory

	// ExpectedInterval documents the configured probe cadence for the
	// observation's rollup metadata (see annotateRollupMetadata). Zero
	// omits that metadata.
	ExpectedInterval time.Duration
	// Now can inject a fixed clock for tests; defaults to time.Now.
	Now func() time.Time
}

// ConnectorProbeWorker periodically calls Version() and Health() on the
// currently effective sub2api and newapi connectors (mode resolved the same
// way the sync jobs resolve it -- see Sub2APINewClient/NewAPINewClient) and
// records the results as ops observations.
type ConnectorProbeWorker struct {
	river.WorkerDefaults[ConnectorProbeArgs]

	logger      *slog.Logger
	environment string
	store       ObservationStore

	sub2apiSource    string
	sub2apiNewClient probeClientFactory
	newapiSource     string
	newapiNewClient  probeClientFactory

	expectedInterval time.Duration
	now              func() time.Time
}

// NewConnectorProbeWorker constructs a worker with safe defaults.
func NewConnectorProbeWorker(opts ConnectorProbeOptions) *ConnectorProbeWorker {
	logger := opts.Logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment := opts.Environment
	if strings.TrimSpace(environment) == "" {
		environment = "unknown"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ConnectorProbeWorker{
		logger:           logger,
		environment:      environment,
		store:            opts.Store,
		sub2apiSource:    opts.Sub2APISource,
		sub2apiNewClient: opts.Sub2APINewClient,
		newapiSource:     opts.NewAPISource,
		newapiNewClient:  opts.NewAPINewClient,
		expectedInterval: opts.ExpectedInterval,
		now:              now,
	}
}

// Work probes both connectors and writes one observation each. The two
// probes are independent: a failure probing sub2api never prevents the
// newapi observation (or vice versa) from being attempted or written,
// matching the sync workers' "each metric writes independently" discipline.
//
// The returned error reflects only *store write* failures, never a probed
// connector reporting unhealthy/not_supported/etc -- that outcome is
// recorded honestly as a SyncFailed observation (see failureObservation),
// which is a successful run of this job, not a reason for River to retry.
// Retrying would not fix "production+fake" or "real mode unconfigured", so
// treating those as job failures would just add retry noise.
func (w *ConnectorProbeWorker) Work(ctx context.Context, job *river.Job[ConnectorProbeArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := w.now().UTC()

	observations := [2]ops.Observation{
		w.probe(ctx, now, MetricSub2APIConnectorHealth, w.sub2apiSource, w.sub2apiNewClient),
		w.probe(ctx, now, MetricNewAPIConnectorHealth, w.newapiSource, w.newapiNewClient),
	}

	var writeErr error
	probeOK, probeFailed := 0, 0
	for i := range observations {
		obs := observations[i]
		if obs.Status == ops.SyncOK {
			probeOK++
		} else {
			probeFailed++
		}
		annotateRollupMetadata(&obs, w.expectedInterval)
		if _, err := w.store.UpsertWithSample(ctx, obs); err != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("write %s: %w", obs.MetricKey, err))
		}
	}

	w.logJob(ctx, job, writeErr == nil, probeOK, probeFailed)
	return writeErr
}

// probe builds a client, calls Version then Health, and returns exactly one
// observation. Version and Health are combined into a single observation
// (rather than two independent metrics) because the required shape is one
// JSON value carrying both, so either read failing -- or the client failing
// to build at all, which covers both "real mode misconfigured" and the
// production+fake guard baked into the dynamic factories (both surface as
// connector.KindNotSupported) -- is treated as the whole probe failing for
// that connector. This means production+fake connectors are simply never
// probed, with no special-case branch needed here: the factory itself
// refuses to hand back a client for that combination.
func (w *ConnectorProbeWorker) probe(
	ctx context.Context, now time.Time, metricKey, source string,
	newClient probeClientFactory,
) ops.Observation {
	base := ops.Observation{
		MetricKey:                 metricKey,
		Source:                    source,
		Environment:               w.environment,
		SyncedAt:                  now,
		StalenessThresholdSeconds: ConnectorProbeStalenessThresholdSeconds,
	}

	client, err := newClient(ctx)
	if err != nil {
		return w.failureObservation(ctx, base, now, connector.KindOf(err))
	}

	versionInfo, verErr := client.Version(ctx)
	if verErr != nil {
		return w.failureObservation(ctx, base, now, connector.KindOf(verErr))
	}
	healthResult, healthErr := client.Health(ctx)
	if healthErr != nil {
		return w.failureObservation(ctx, base, now, connector.KindOf(healthErr))
	}

	base.Status = ops.SyncOK
	base.ObservedAt = &now
	base.LastSuccess = &now
	base.Watermark = versionInfo.Fingerprint
	base.Value = map[string]any{
		"version":    versionInfo.Detected,
		"supported":  versionInfo.Supported,
		"healthy":    healthResult.Healthy,
		"kind":       string(healthResult.ErrorKind),
		"latency_ms": healthResult.LatencyMS,
		"checked_at": healthResult.CheckedAt.UTC().Format(time.RFC3339),
	}
	return base
}

// failureObservation mirrors Sub2APISyncWorker.failureObservation /
// NewAPISyncWorker.failureObservation: it preserves the last-known-good
// Watermark/Value/ObservedAt/LastSuccess when a previous observation exists,
// so a transient probe failure degrades the dashboard's freshness badge
// (via growing staleness) instead of blanking out the last good reading.
func (w *ConnectorProbeWorker) failureObservation(
	ctx context.Context, base ops.Observation, now time.Time, kind connector.ErrorKind,
) ops.Observation {
	if kind == "" {
		kind = connector.KindInternal
	}
	failure := ops.Observation{
		MetricKey:                 base.MetricKey,
		Source:                    base.Source,
		Environment:               base.Environment,
		SyncedAt:                  now,
		Status:                    ops.SyncFailed,
		LastErrorCode:             string(kind),
		StalenessThresholdSeconds: base.StalenessThresholdSeconds,
		Value:                     map[string]any{},
	}
	previous, err := w.store.Get(ctx, base.MetricKey, base.Environment)
	if err != nil {
		return failure
	}
	failure.ObservedAt = previous.ObservedAt
	failure.LastSuccess = previous.LastSuccess
	failure.Watermark = previous.Watermark
	failure.IsPartial = previous.IsPartial
	if len(previous.Value) > 0 {
		failure.Value = previous.Value
	}
	return failure
}

func (w *ConnectorProbeWorker) logJob(
	ctx context.Context, job *river.Job[ConnectorProbeArgs], success bool, probeOK, probeFailed int,
) {
	logger := w.logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	jobID, attempt, maxAttempts := int64(0), 1, connectorProbeMaxAttempts
	if job != nil && job.JobRow != nil {
		jobID = job.ID
		if job.Attempt > 0 {
			attempt = job.Attempt
		}
		if job.MaxAttempts > 0 {
			maxAttempts = job.MaxAttempts
		}
	}
	level, errorCode, event := slog.LevelInfo, "", "job_completed"
	if !success {
		level, errorCode, event = slog.LevelError, "connector_probe_write_failed", "job_failed"
	}
	logger.LogAttrs(ctx, level, event,
		slog.String("event", event),
		slog.String("module", connectorProbeModule),
		slog.String("environment", w.environment),
		slog.String("principal_id", connectorProbePrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", ConnectorProbeJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
		// These count connectors that were successfully *probed* (a client
		// built and both reads succeeded), not connectors reporting
		// healthy=true -- an unhealthy-but-successfully-probed connector is
		// still probeOK here; its Value.healthy=false is the honest signal,
		// visible in the observation itself, not summarized into this log.
		slog.Int("connectors_probe_ok", probeOK),
		slog.Int("connectors_probe_failed", probeFailed),
	)
}
