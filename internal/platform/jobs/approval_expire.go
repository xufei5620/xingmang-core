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

	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// XM-0030c 审批单过期清理与队列观测。
//
// ── 修的是什么 ────────────────────────────────────────────────────────────
// `approval.Policy.CanExecute` 在**执行那一刻**按 expires_at 判过期，但没有
// 任何东西把库里的 status 推到 EXPIRED。后果不是「多留几行」：
//
//   - 审批队列页看到的 status 一直是 PENDING，一屏「待审批」里混着一批其实
//     谁也批不动的单（XM-0030b-ui 因此不得不在前端按 expires_at 自行判过期，
//     并在详情里解释「库中仍记为待审批」）；
//   - 「有没有人在等」这个问题没有答案，于是「有人等了太久」也无从告警。
//
// 这个任务负责前半件事；后半件由它顺带写下的观测 + alerts 的
// `approval.pending.too_long` 规则完成。
//
// ── 为什么把统计也放在这里 ────────────────────────────────────────────────
// 清理与统计必须**同一轮、清理在前**：先把过期的推走，剩下的才是真正在等人
// 的那些。反过来的话，一张昨晚就该过期的单会被算成「等了 14 小时」，告警
// 报的是清理任务没跑，却说成没人审批。
const (
	// ApprovalExpireJobKind 是审批过期清理任务的稳定 River kind。
	ApprovalExpireJobKind = "approval_expire"

	// DefaultApprovalExpireInterval 是默认执行周期。
	//
	// 5 分钟：过期本身不急（服务端在执行那一刻仍会拒绝一张过期的单，安全性
	// 不依赖这个任务），但**队列观测的新鲜度**依赖它——告警规则要回答「最久
	// 那张等了多久」，观测滞后多少，告警就迟报多少。5 分钟相对于 4 小时的
	// 告警门槛是可忽略的误差，而每轮只有两条带索引的查询。
	DefaultApprovalExpireInterval = 5 * time.Minute

	approvalExpirePrincipalID = "worker:platform"
	approvalExpireMaxAttempts = 3

	// MetricApprovalQueue 是队列观测的指标键。
	//
	// alerts 的 `approval.pending.too_long` 规则按**这个字面量**匹配，两处
	// 各写一份会在改名时静默失联；因此规则那边引用的是本常量，且有一条
	// 一致性测试钉住（同 cfg.ChannelBalanceMetricKey 的既有做法）。
	MetricApprovalQueue = "platform.approval.queue"

	// ApprovalQueueStalenessThresholdSeconds 约为默认周期的 3 倍。
	//
	// 比 retention 的 2 倍宽松一档，因为这条观测的价值在于**告警的输入**：
	// 一次抖动就把它判成 stale，会让「没人审批」的告警被「观测陈旧」的告警
	// 盖过去，而后者对处置没有帮助。
	ApprovalQueueStalenessThresholdSeconds int32 = 15 * 60
)

// ApprovalQueueMaintainer 是本任务所需的最小审批能力（*approval.Service 满足）。
//
// 只声明这两个方法：单元测试能用内存实现跑完整轮，不必起容器。
type ApprovalQueueMaintainer interface {
	ExpirePending(ctx context.Context) (int64, error)
	PendingStats(ctx context.Context) (approval.QueueStats, error)
}

// ApprovalExpireArgs 是任务的可序列化参数。
type ApprovalExpireArgs struct {
	// RunID 只服务于集成测试隔离，生产必须留空。
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (ApprovalExpireArgs) Kind() string { return ApprovalExpireJobKind }

// InsertOpts 给每一轮同一套重试与唯一性策略。
func (ApprovalExpireArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: approvalExpireMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultApprovalExpireInterval,
			ByQueue:  true,
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// ApprovalExpireOptions 是 Worker 的构造参数。
type ApprovalExpireOptions struct {
	Logger      *slog.Logger
	Environment string
	// Approvals 为 nil 时任务什么也不做（不报错）——与 RetentionOptions 的
	// Samples/Alerts 同一条纪律，让单元测试能只测装配。
	Approvals ApprovalQueueMaintainer
	// Observations 为 nil 时只记日志，不写观测。生产必须提供：
	// 没有观测就没有告警的输入。
	Observations ObservationStore
	// ExpectedInterval 进观测的 rollup 元数据；零值省略该元数据。
	ExpectedInterval time.Duration
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
}

// ApprovalExpireWorker 周期性清理过期审批单并记录队列积压。
type ApprovalExpireWorker struct {
	river.WorkerDefaults[ApprovalExpireArgs]

	logger           *slog.Logger
	environment      string
	approvals        ApprovalQueueMaintainer
	observations     ObservationStore
	expectedInterval time.Duration
	now              func() time.Time
}

// NewApprovalExpireWorker 构造 Worker 并补齐安全默认值。
func NewApprovalExpireWorker(opts ApprovalExpireOptions) *ApprovalExpireWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &ApprovalExpireWorker{
		logger:           opts.Logger,
		environment:      opts.Environment,
		approvals:        opts.Approvals,
		observations:     opts.Observations,
		expectedInterval: opts.ExpectedInterval,
		now:              opts.Now,
	}
}

// Work 跑一轮：先清理，再统计，最后写观测。
//
// **顺序不能换**（见文件头）：先把过期的推走，剩下的才是真正在等人的那些。
//
// 清理失败时**不写成功观测**也不接着统计：那一轮的数字会把已经过期的单算成
// 在等人，告警报出来的是清理坏了、说的却是没人审批——一条指向错误方向的告警
// 比没有告警更糟。这时写一条 failed 观测，让「同步失败」那条既有规则去响。
func (w *ApprovalExpireWorker) Work(ctx context.Context, job *river.Job[ApprovalExpireArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.approvals == nil {
		w.logJob(ctx, job, slog.LevelInfo, "job_completed", true, "",
			slog.String("skipped", "no_approval_service"))
		return nil
	}
	now := w.now().UTC()

	expired, err := w.approvals.ExpirePending(ctx)
	var stats approval.QueueStats
	errorCode := ""
	if err != nil {
		errorCode = "approval_expire_failed"
		err = fmt.Errorf("expire pending approvals: %w", err)
	} else {
		stats, err = w.approvals.PendingStats(ctx)
		if err != nil {
			errorCode = "approval_queue_stats_failed"
			err = fmt.Errorf("read approval queue stats: %w", err)
		}
	}

	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelError
	}
	w.logJob(ctx, job, level, "job_completed", err == nil, errorCode,
		slog.Int64("expired", expired),
		slog.Int64("pending", stats.PendingCount),
		slog.Int64("oldest_pending_age_seconds", int64(stats.OldestPendingAge.Seconds())),
	)

	if w.observations != nil {
		obs := ops.Observation{
			MetricKey:                 MetricApprovalQueue,
			Source:                    approvalExpirePrincipalID,
			Environment:               w.environment,
			SyncedAt:                  now,
			StalenessThresholdSeconds: ApprovalQueueStalenessThresholdSeconds,
			Value: map[string]any{
				"pending_count":              stats.PendingCount,
				"oldest_pending_age_seconds": int64(stats.OldestPendingAge.Seconds()),
				"expired_this_round":         expired,
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
			err = errors.Join(err, fmt.Errorf("write approval queue observation: %w", obsErr))
		}
	}
	return err
}

func (w *ApprovalExpireWorker) logJob(
	ctx context.Context, job *river.Job[ApprovalExpireArgs],
	level slog.Level, event string, succeeded bool, errorCode string, attrs ...slog.Attr,
) {
	base := []slog.Attr{
		slog.String("module", "platform.jobs"),
		slog.String("event", event),
		slog.String("job_kind", ApprovalExpireJobKind),
		slog.String("environment", w.environment),
		slog.String("principal_id", approvalExpirePrincipalID),
		slog.Bool("succeeded", succeeded),
	}
	if job != nil {
		base = append(base, slog.Int64("job_id", job.ID), slog.Int("attempt", job.Attempt))
	}
	if errorCode != "" {
		base = append(base, slog.String("error_code", errorCode))
	}
	w.logger.LogAttrs(ctx, level, "approval expire", append(base, attrs...)...)
}
