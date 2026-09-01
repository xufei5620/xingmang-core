package jobs

// CPA 周期同步任务（XM-CPA0）。照四件套：job kind 常量、Args 实现
// river.JobArgs、Worker 嵌 river.WorkerDefaults、间隔常量；装配在 client.go
// 的 NewClient（AddWorker + append periodic），env 在
// cmd/platform-worker/config.go。遵 ADR-013，同 sub2api_sync/cost_sync。
//
// 与 sub2api_sync/cost_sync 的关键不同：那两个任务的取数是**一次**上游调用，
// 失败就是全部指标一起失败。CPA 的 ReadClient 有三个互相独立的方法
// （UsageSummary/KeyUsage/AccountHealth），各自是对同一份 usage.sqlite 的独立
// 查询——一个失败（比如 codex_inspection_* 表在旧版 usage.sqlite 里还不存在）
// 不代表另外两个也会失败。所以本文件按**每条指标**单独判定成功/失败，而不是
// 整轮成败一起判，failureObservation 因此按 metricKey 取值而不是按已有的
// base Observation 改写。
//
// CPA 没有 fake 模式（只有 off/file）：它的数据源是本机只读挂载的 SQLite
// 文件，不是一个可以伪造响应的 HTTP 契约，所以这里没有 Sub2API/NewAPI/
// Finance 那种「生产环境不许 fake」的启动期硬拒绝——那道闸本来就是为了防止
// Fake 编出来的数字被当成生产读数落库，而 CPA file 模式读的从来都是真实数据
// 或者干脆读不到，不存在这个风险类别。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/cpa"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	// CPASyncJobKind is the stable River kind for CPA's periodic sync.
	CPASyncJobKind = "cpa_sync"

	// DefaultCPASyncInterval matches every other sync job's 5-minute default
	// (finance/cost_sync precedent, §12 拍板).
	DefaultCPASyncInterval = 300 * time.Second

	// DefaultCPAInstanceID becomes ops.Observation.Source for every CPA
	// metric. Unlike Sub2API/NewAPI/Finance's "-staging" fake-distinguishing
	// suffix, there is no fake mode to distinguish from here (see file doc
	// comment), so the name just names the real system it reads.
	DefaultCPAInstanceID = "cpa-cli-proxy-api"

	cpaSyncMaxAttempts = 3

	// cpaReadTimeout bounds the three ReadClient calls together. A local
	// read-only SQLite read should be fast; this is generous headroom for a
	// busy_timeout retry (see connectors/cpa.busyTimeoutMillis), not an
	// expected steady-state duration.
	cpaReadTimeout = 30 * time.Second

	cpaSyncPrincipalID = "worker:platform"
	cpaSyncModule      = "platform.jobs"
)

// CPAMode selects CPA's read backend. There is no "fake": unlike an HTTP
// upstream, a SQLite file has no meaningful synthetic response to fabricate
// — "off" (don't read at all) is the only alternative to reading the real
// file.
type CPAMode string

const (
	CPAModeOff  CPAMode = "off"
	CPAModeFile CPAMode = "file"
)

// ParseCPAMode parses mode, treating an empty string as off — the safe
// resting state (task brief: "off 不注册任务"). Unlike Sub2API/NewAPI, off
// is not just "default" but the only state that requires zero configuration,
// so it is what an unset environment variable should mean.
func ParseCPAMode(s string) (CPAMode, error) {
	switch mode := CPAMode(strings.TrimSpace(s)); mode {
	case "":
		return CPAModeOff, nil
	case CPAModeOff, CPAModeFile:
		return mode, nil
	default:
		return "", fmt.Errorf("cpa mode %q: 只接受 off 或 file", s)
	}
}

// CPAClientFactory constructs a read-only CPA client on demand, mirroring
// Sub2APIClientFactory's reasoning: a factory instead of a held client keeps
// the door open for a future mode that has real per-cycle setup, and matches
// the shape every other sync job in this package already uses.
type CPAClientFactory func(ctx context.Context) (cpa.ReadClient, error)

// NewCPAClientFactory builds the mode-appropriate factory. In "off" mode
// every call fails not_supported — the worker converts that into a failed
// observation on all four metrics rather than skipping the job entirely, so
// "off" is still visible on the freshness dashboard as "not connected," not
// as silence (constitution §12).
func NewCPAClientFactory(mode CPAMode, dataDir, fileName string) CPAClientFactory {
	return func(context.Context) (cpa.ReadClient, error) {
		if mode != CPAModeFile {
			return nil, connector.NewError(connector.KindNotSupported, "cpa.client.factory",
				fmt.Errorf("cpa mode %q 不提供只读客户端", mode))
		}
		return cpa.NewFileClient(cpa.FileConfig{DataDir: dataDir, FileName: fileName})
	}
}

// CPASyncArgs is the sync job's serializable parameters.
type CPASyncArgs struct {
	RunID string `json:"run_id,omitempty"`
}

func (CPASyncArgs) Kind() string { return CPASyncJobKind }

func (CPASyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: cpaSyncMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true, ByPeriod: DefaultCPASyncInterval, ByQueue: true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// CPASyncOptions constructs CPASyncWorker.
type CPASyncOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID becomes every CPA observation's Source.
	InstanceID string
	// Mode is logging-only — NewClient decides what to register based on
	// mode; the worker just needs to say which mode produced these numbers.
	Mode CPAMode

	Store     ObservationStore
	NewClient CPAClientFactory

	Now              func() time.Time
	ExpectedInterval time.Duration
}

// CPASyncWorker periodically reads CPA's usage.sqlite and writes four
// operational metrics.
type CPASyncWorker struct {
	river.WorkerDefaults[CPASyncArgs]

	logger      *slog.Logger
	environment string
	instanceID  string
	mode        CPAMode

	store            ObservationStore
	newClient        CPAClientFactory
	now              func() time.Time
	expectedInterval time.Duration
}

// NewCPASyncWorker fills safe defaults, mirroring every other *_sync worker
// constructor in this package.
func NewCPASyncWorker(opts CPASyncOptions) *CPASyncWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if strings.TrimSpace(opts.InstanceID) == "" {
		opts.InstanceID = DefaultCPAInstanceID
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &CPASyncWorker{
		logger: opts.Logger, environment: opts.Environment, instanceID: opts.InstanceID,
		mode: opts.Mode, store: opts.Store, newClient: opts.NewClient,
		now: opts.Now, expectedInterval: opts.ExpectedInterval,
	}
}

func (w *CPASyncWorker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}

// cpaMetricKeys is every metric this worker can write, in a fixed order so
// log output and failure fan-out are deterministic.
var cpaMetricKeys = []string{
	cpa.MetricRequestsDaily, cpa.MetricCostDaily, cpa.MetricKeysUsage, cpa.MetricAccountsHealth,
}

func cpaStalenessThreshold(metricKey string) int32 {
	switch metricKey {
	case cpa.MetricRequestsDaily:
		return cpa.RequestsStalenessThresholdSeconds
	case cpa.MetricCostDaily:
		return cpa.CostStalenessThresholdSeconds
	case cpa.MetricKeysUsage:
		return cpa.KeysStalenessThresholdSeconds
	case cpa.MetricAccountsHealth:
		return cpa.AccountsStalenessThresholdSeconds
	default:
		return 1800
	}
}

// Work executes one sync round.
//
// Only a **write failure** returns error (River retry); a read failure is
// recorded as a SyncFailed observation for that one metric and Work still
// returns nil — same reasoning as sub2api_sync/cost_sync's Work, applied
// per-metric here because CPA's three reads can fail independently.
func (w *CPASyncWorker) Work(ctx context.Context, job *river.Job[CPASyncArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.store == nil {
		return errors.New("jobs: cpa sync worker has no observation store")
	}
	if w.newClient == nil {
		return errors.New("jobs: cpa sync worker has no client factory")
	}

	now := w.clock()
	day := now.Format("2006-01-02")

	readCtx, cancel := context.WithTimeout(ctx, cpaReadTimeout)
	defer cancel()

	observations, readErrorCode := w.readAll(readCtx, now, day)

	// Context cancellation means the process is shutting down, not that the
	// sync failed — recording it as a failure would put a false alarm on the
	// dashboard for a routine restart (same reasoning as every other worker
	// in this package).
	if err := ctx.Err(); err != nil {
		return err
	}

	writeFailed := ""
	for i := range observations {
		annotateRollupMetadata(&observations[i], w.expectedInterval)
		if _, err := w.store.UpsertWithSample(ctx, observations[i]); err != nil {
			writeFailed = observations[i].MetricKey
			w.logJob(ctx, job, slog.LevelError, "job_failed", false, "observation_write_failed",
				slog.String("metric_key", observations[i].MetricKey))
			// Returning here (not continuing the loop) matches sub2api_sync/
			// cost_sync: a write failure means retry the whole round, and a
			// partially-written round is still useful (the metrics before the
			// failing one already have fresh values).
			return fmt.Errorf("write %s: %w", observations[i].MetricKey, err)
		}
	}

	level := slog.LevelInfo
	if readErrorCode != "" {
		level = slog.LevelWarn
	}
	w.logJob(ctx, job, level, "job_completed", writeFailed == "" && readErrorCode == "", readErrorCode,
		slog.String("cpa_mode", string(w.mode)),
		slog.String("source", w.instanceID),
		slog.String("business_day", day),
	)
	return nil
}

// readAll runs the three independent reads and converts each to its
// observation(s), substituting a per-metric failure observation for any read
// that errored. errorCode is the last read error's classification, empty if
// every read succeeded — used only for the completion log line's severity.
func (w *CPASyncWorker) readAll(ctx context.Context, now time.Time, day string) ([]ops.Observation, string) {
	client, err := w.newClient(ctx)
	if err != nil {
		kind := connector.KindOf(err)
		out := make([]ops.Observation, 0, len(cpaMetricKeys))
		for _, key := range cpaMetricKeys {
			out = append(out, w.failureObservation(ctx, key, now, kind))
		}
		return out, string(kind)
	}

	usage, usageErr := client.UsageSummary(ctx, day)
	keys, keysErr := client.KeyUsage(ctx, day)
	health, healthErr := client.AccountHealth(ctx)
	if !sameCPASnapshotGeneration(usage, usageErr, keys, keysErr, health, healthErr) {
		kind := connector.KindBadResponse
		out := make([]ops.Observation, 0, len(cpaMetricKeys))
		for _, key := range cpaMetricKeys {
			out = append(out, w.failureObservation(ctx, key, now, kind))
		}
		return out, string(kind)
	}

	var out []ops.Observation
	errorCode := ""

	if usageErr != nil {
		kind := connector.KindOf(usageErr)
		errorCode = string(kind)
		out = append(out,
			w.failureObservation(ctx, cpa.MetricRequestsDaily, now, kind),
			w.failureObservation(ctx, cpa.MetricCostDaily, now, kind))
	} else {
		out = append(out,
			cpa.RequestsObservation(now, w.instanceID, w.environment, usage),
			cpa.CostObservation(now, w.instanceID, w.environment, usage))
	}

	if keysErr != nil {
		kind := connector.KindOf(keysErr)
		errorCode = string(kind)
		out = append(out, w.failureObservation(ctx, cpa.MetricKeysUsage, now, kind))
	} else {
		out = append(out, cpa.KeysObservation(now, w.instanceID, w.environment, keys))
	}

	if healthErr != nil {
		kind := connector.KindOf(healthErr)
		errorCode = string(kind)
		out = append(out, w.failureObservation(ctx, cpa.MetricAccountsHealth, now, kind))
	} else {
		out = append(out, cpa.AccountsObservation(now, w.instanceID, w.environment, health))
	}

	return out, errorCode
}

func sameCPASnapshotGeneration(
	usage cpa.UsageSummary, usageErr error,
	keys cpa.KeyUsagePage, keysErr error,
	health cpa.AccountHealthSummary, healthErr error,
) bool {
	generation := ""
	successes := 0
	for _, candidate := range []struct {
		watermark string
		err       error
	}{
		{watermark: usage.Watermark, err: usageErr},
		{watermark: keys.Watermark, err: keysErr},
		{watermark: health.Watermark, err: healthErr},
	} {
		if candidate.err != nil {
			continue
		}
		successes++
		if candidate.watermark == "" {
			return false
		}
		if generation == "" {
			generation = candidate.watermark
			continue
		}
		if candidate.watermark != generation {
			return false
		}
	}
	return successes == 0 || generation != ""
}

// failureObservation builds a SyncFailed observation for one metric,
// preserving the metric's last known good value/observed time so the
// dashboard shows "stale, last known value N" instead of erasing history on
// a transient failure (same discipline as sub2api_sync/cost_sync's
// same-named helper, keyed by metricKey here instead of a base Observation
// because CPA's reads are independent — see file doc comment).
func (w *CPASyncWorker) failureObservation(ctx context.Context, metricKey string, now time.Time, kind connector.ErrorKind) ops.Observation {
	if kind == "" {
		kind = connector.KindInternal
	}
	failure := ops.Observation{
		MetricKey: metricKey, Source: w.instanceID, Environment: w.environment,
		SyncedAt: now, Status: ops.SyncFailed, LastErrorCode: string(kind),
		StalenessThresholdSeconds: cpaStalenessThreshold(metricKey),
		Value:                     map[string]any{},
	}
	previous, err := w.store.Get(ctx, metricKey, w.environment)
	if err != nil {
		// No prior row (first sync, or a transient store issue): leave
		// ObservedAt nil so the dashboard shows "uninitialized," not a
		// fabricated success time.
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

// logJob mirrors every other periodic job's structured log shape in this
// package. Never logs a metric value, only counts/classifications
// (constitution §7 — see finance/cost_sync.go's identical rule).
func (w *CPASyncWorker) logJob(
	ctx context.Context, job *river.Job[CPASyncArgs],
	level slog.Level, event string, success bool, errorCode string, extra ...slog.Attr,
) {
	logger := w.logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment := w.environment
	if strings.TrimSpace(environment) == "" {
		environment = "unknown"
	}
	jobID, attempt, maxAttempts := int64(0), 1, cpaSyncMaxAttempts
	if job != nil && job.JobRow != nil {
		jobID = job.ID
		if job.Attempt > 0 {
			attempt = job.Attempt
		}
		if job.MaxAttempts > 0 {
			maxAttempts = job.MaxAttempts
		}
	}
	attrs := []slog.Attr{
		slog.String("event", event),
		slog.String("module", cpaSyncModule),
		slog.String("environment", environment),
		slog.String("principal_id", cpaSyncPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", CPASyncJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}
