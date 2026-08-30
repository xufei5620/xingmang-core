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

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// XM-R012 保留期清理（Codex 冷审 #48 第 8 条，Issue #75）。
//
// ── 修的是什么 ────────────────────────────────────────────────────────────
// ops.metric_observation_sample 以 5 分钟粒度追加，约 288 条/日/指标；十几条
// 指标一年就是百万量级，而在此之前**没有任何清理路径**（迁移 000005 的注释
// 里已经预告了这个任务）。告警表同理，只是增速慢得多。
//
// ── 审计事件为什么不在这里 ────────────────────────────────────────────────
// **审计链不清理，一条都不删。** 三条理由，任何一条单独成立：
//
//  1. 宪法 11 条要求审计 append-only 并在库外锚定签名摘要——「永远不删」是
//     需求本身，不是还没做的功能；
//  2. 删掉中间任意一条都会**断链**：prev_hash 链接的是相邻两条，VerifyChain
//     会立刻报 sequence_gap，于是清理任务的产物是一条永远验不过的链；
//  3. 就算写了也删不掉：audit.audit_event 上有
//     `audit_event_no_delete ... DO INSTEAD NOTHING`（迁移 000003），
//     DELETE 是**静默空操作**——任务会报告「删了 0 行」并显示成功，
//     表照涨不误。一个必然静默失败的清理器比没有清理器更糟。
//
// 审计的容量问题应当走**归档**（导出 + 链根锚定后转冷存储），那是另一件事：
// 它需要先有导出格式、锚定验证与恢复演练，不该被塞进一个 DELETE 任务里。
// 现状 audit.chain_root 已经在做锚定（见 audit/anchor.go），归档本身待立任务。
//
// ── 为什么分批 ────────────────────────────────────────────────────────────
// 一条 `DELETE ... WHERE synced_at < cutoff` 在积压一年后会一次删掉上百万行：
// 单个长事务持有大量行锁、撑大 WAL、把 autovacuum 挤在后面，而采集任务正在往
// 同一张表写。分批之后每个事务只碰一批，批之间还能检查 ctx 与记录进度。

const (
	// RetentionJobKind 是保留期清理任务的稳定 River kind。
	RetentionJobKind = "retention_prune"

	// DefaultRetentionInterval 是默认清理周期。
	//
	// 一天一次：保留期以天计，清理频率再高也只是把同样的行提前几小时删掉，
	// 而每一次都要扫索引。放在低峰期由部署决定（River 的周期任务从进程启动
	// 时刻起算，这里不假装能控制具体时刻）。
	DefaultRetentionInterval = 24 * time.Hour

	// DefaultMetricSampleRetentionDays 是指标样本的默认保留天数。
	//
	// 90 天：设计上「原始样本 90 天」够覆盖季度回顾与影子对比的 14 天窗口，
	// 再久的趋势应当走降采样（日粒度汇总，另立任务）而不是留着原始行。
	DefaultMetricSampleRetentionDays = 90

	// DefaultAlertRetentionDays 是**已解决**告警的默认保留天数。
	//
	// 180 天，比样本长一倍：一条告警是一次事件的记录，事后复盘的时间跨度
	// 通常以季度计；而它的增速比样本低两三个数量级，留久一点不构成压力。
	DefaultAlertRetentionDays = 180

	// retentionBatchSize 是单批删除的行数上限。
	//
	// 2000：足够让百万行在几百批内清完，又小到每个事务都很短。
	// 不做成可配置——它是一个实现细节，调它只会让人以为自己在调保留期。
	retentionBatchSize = 2000

	// retentionMaxBatches 是单轮的批次上限。
	//
	// 有上限是为了让一轮清理有确定的结束：首次启用时可能积压了上百万行，
	// 一口气删完会让这个任务跑几十分钟、期间一直在跟采集抢锁。删不完没关系,
	// 明天那一轮接着删——保留期不是一个必须在今天精确达成的目标。
	retentionMaxBatches = 500

	retentionPrincipalID = "worker:platform"
	retentionModule      = "platform.jobs"
	retentionMaxAttempts = 3

	// MetricRetentionLastRun is the ops metric key recording the outcome
	// of each retention run (XM-OPS0), so the ops overview query can show
	// "when did cleanup last run and what did it delete" without tailing
	// structured logs.
	MetricRetentionLastRun = "platform.retention.last_run"

	// RetentionObservationStalenessThresholdSeconds is roughly 2x the
	// default 24h cleanup interval: a run more than two days overdue is
	// worth flagging as stale rather than assuming "still within today's
	// window".
	RetentionObservationStalenessThresholdSeconds int32 = 2 * 24 * 60 * 60
)

// SamplePruner 是指标样本清理所需的最小仓储能力（*ops.Store 天然满足）。
//
// 只声明用得到的这一个方法：单元测试能用内存实现跑完整条分批循环，
// 不必为了验证「删不完会继续删」先起一个库。
type SamplePruner interface {
	PruneSamples(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error)
}

// AlertPruner 是已解决告警清理所需的最小仓储能力（*alerts.Store 天然满足）。
type AlertPruner interface {
	PruneResolved(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error)
}

// RetentionArgs 是清理任务的可序列化参数。
type RetentionArgs struct {
	// RunID 只服务于集成测试隔离，生产必须留空。
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (RetentionArgs) Kind() string { return RetentionJobKind }

// InsertOpts 给每次清理同一套重试与唯一性策略。
func (RetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: retentionMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultRetentionInterval,
			ByQueue:  true,
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// RetentionOptions 是 Worker 的构造参数。
type RetentionOptions struct {
	Logger      *slog.Logger
	Environment string
	// Samples / Alerts 为 nil 时对应的那一类跳过（不报错）。
	//
	// 允许为 nil 是为了让单元测试能只测一类；生产装配两个都传。
	Samples SamplePruner
	Alerts  AlertPruner
	// SampleRetentionDays / AlertRetentionDays <=0 时回落到默认值。
	SampleRetentionDays int
	AlertRetentionDays  int
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
	// Observations, when set, records a summary of each run as an ops
	// observation (MetricRetentionLastRun), so the ops overview query can
	// show "last cleanup result" without tailing worker logs (XM-OPS0).
	// Optional: nil preserves pre-XM-OPS0 behavior exactly (log-only).
	Observations ObservationStore
	// ExpectedInterval documents the configured cleanup cadence for the
	// observation's rollup metadata (see annotateRollupMetadata); zero
	// omits that metadata.
	ExpectedInterval time.Duration
}

// RetentionWorker 周期性清理过期的运营遥测。
type RetentionWorker struct {
	river.WorkerDefaults[RetentionArgs]

	logger           *slog.Logger
	environment      string
	samples          SamplePruner
	alerts           AlertPruner
	sampleDays       int
	alertDays        int
	now              func() time.Time
	observations     ObservationStore
	expectedInterval time.Duration
}

// NewRetentionWorker 构造 Worker 并补齐安全默认值。
func NewRetentionWorker(opts RetentionOptions) *RetentionWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if opts.SampleRetentionDays <= 0 {
		opts.SampleRetentionDays = DefaultMetricSampleRetentionDays
	}
	if opts.AlertRetentionDays <= 0 {
		opts.AlertRetentionDays = DefaultAlertRetentionDays
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &RetentionWorker{
		logger:           opts.Logger,
		environment:      opts.Environment,
		samples:          opts.Samples,
		alerts:           opts.Alerts,
		sampleDays:       opts.SampleRetentionDays,
		alertDays:        opts.AlertRetentionDays,
		now:              opts.Now,
		observations:     opts.Observations,
		expectedInterval: opts.ExpectedInterval,
	}
}

// Work 执行一轮清理。
//
// 两类各自独立：样本清理失败不该让告警清理也不跑。任一类出错整轮算失败
// （River 会重试），但**已经删掉的行不会回来**——删除是幂等的，重试只会
// 从剩下的继续删。
func (w *RetentionWorker) Work(ctx context.Context, job *river.Job[RetentionArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := w.now().UTC()

	sampleDeleted, sampleErr := w.prune(ctx, "metric_samples", w.sampleTarget(),
		now.AddDate(0, 0, -w.sampleDays))
	alertDeleted, alertErr := w.prune(ctx, "resolved_alerts", w.alertTarget(),
		now.AddDate(0, 0, -w.alertDays))

	err := errors.Join(sampleErr, alertErr)
	level, errorCode := slog.LevelInfo, ""
	if err != nil {
		level, errorCode = slog.LevelError, "retention_prune_failed"
	}
	w.logJob(ctx, job, level, "job_completed", err == nil, errorCode,
		slog.Int64("metric_samples_deleted", sampleDeleted),
		slog.Int("metric_sample_retention_days", w.sampleDays),
		slog.Int64("resolved_alerts_deleted", alertDeleted),
		slog.Int("alert_retention_days", w.alertDays),
		// 审计**不清理**，把这个事实打进每一轮的日志：一个只在注释里的
		// 承诺，半年后没人记得它是有意为之还是漏了（理由见文件头）。
		slog.String("audit_events", "never_pruned"),
	)

	// XM-OPS0: record this run's outcome as an observation, if a sink was
	// attached. A write failure here folds into the returned error (via
	// errors.Join) rather than being swallowed -- River will retry the
	// whole job, which is safe: deletes are idempotent, so a retry after a
	// successful prune-but-failed-observation just re-runs prune() against
	// an already-clean window (each batch call returns 0 rows and the loop
	// exits on the first short batch).
	if w.observations != nil {
		obs := ops.Observation{
			MetricKey:                 MetricRetentionLastRun,
			Source:                    retentionPrincipalID,
			Environment:               w.environment,
			SyncedAt:                  now,
			StalenessThresholdSeconds: RetentionObservationStalenessThresholdSeconds,
			Value: map[string]any{
				"metric_samples_deleted":       sampleDeleted,
				"resolved_alerts_deleted":      alertDeleted,
				"metric_sample_retention_days": w.sampleDays,
				"alert_retention_days":         w.alertDays,
			},
		}
		if err == nil {
			obs.Status = ops.SyncOK
			obs.ObservedAt = &now
			obs.LastSuccess = &now
		} else {
			obs.Status = ops.SyncFailed
			obs.LastErrorCode = errorCode
		}
		annotateRollupMetadata(&obs, w.expectedInterval)
		if _, obsErr := w.observations.UpsertWithSample(ctx, obs); obsErr != nil {
			err = errors.Join(err, fmt.Errorf("write retention observation: %w", obsErr))
		}
	}
	return err
}

// pruneFunc 是「删一批」的统一形状，让两类清理共用同一条分批循环。
type pruneFunc func(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error)

func (w *RetentionWorker) sampleTarget() pruneFunc {
	if w.samples == nil {
		return nil
	}
	return w.samples.PruneSamples
}

func (w *RetentionWorker) alertTarget() pruneFunc {
	if w.alerts == nil {
		return nil
	}
	return w.alerts.PruneResolved
}

// prune 分批删到干净为止（或撞上批次上限）。
//
// 每批之间检查 ctx：一轮清理可能跑几分钟，进程关机时不该等它删完。
// 已删的批不回滚——删除是幂等的，下一轮从剩下的继续。
func (w *RetentionWorker) prune(
	ctx context.Context, what string, del pruneFunc, cutoff time.Time,
) (int64, error) {
	if del == nil {
		return 0, nil
	}
	var total int64
	for batch := 0; batch < retentionMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			// 关机或超时：把**已经删掉的**数量如实带出去，不当成失败。
			// 报成失败会让 River 重试一轮本来正常结束的清理。
			return total, nil
		}
		deleted, err := del(ctx, cutoff, retentionBatchSize)
		if err != nil {
			return total, fmt.Errorf("清理 %s 失败（已删 %d 行）: %w", what, total, err)
		}
		total += deleted
		if deleted < retentionBatchSize {
			// 这一批没删满 = 没有更旧的了
			return total, nil
		}
	}
	// 撞上批次上限：没删完，但这不是错误——保留期不是必须今天精确达成的目标，
	// 明天那一轮接着删。打一条 Warn 让积压可见。
	w.logger.WarnContext(ctx, "retention_batch_limit_reached",
		slog.String("module", retentionModule),
		slog.String("environment", w.environment),
		slog.String("target", what),
		slog.Int64("deleted", total),
		slog.Int("max_batches", retentionMaxBatches),
		slog.String("hint", "本轮未清完，下一轮继续；持续出现说明保留期或清理频率需要调整"),
	)
	return total, nil
}

func (w *RetentionWorker) logJob(
	ctx context.Context, job *river.Job[RetentionArgs],
	level slog.Level, event string, success bool, errorCode string, extra ...slog.Attr,
) {
	logger := w.logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	jobID, attempt, maxAttempts := int64(0), 1, retentionMaxAttempts
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
		slog.String("module", retentionModule),
		slog.String("environment", w.environment),
		slog.String("principal_id", retentionPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", RetentionJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}
