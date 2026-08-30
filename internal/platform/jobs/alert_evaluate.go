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

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

const (
	// AlertEvaluateJobKind 是告警评估任务的稳定 River kind。
	AlertEvaluateJobKind = "alert_evaluate"

	// DefaultAlertEvaluateInterval 是默认评估周期。
	//
	// 60s 明显快于 300s 的采集周期，这是有意的：评估本身不产生新数据，
	// 它只是把最新一次采集的结论读出来。跑得比采集快，代价是几次空转的
	// 只读查询；跑得比采集慢，代价是「上游挂了」要多等好几分钟才有人知道。
	// 规格 §9.5 把「告警发现到投递延迟」列为 SLI，这个周期是它的地板。
	DefaultAlertEvaluateInterval = 60 * time.Second

	alertEvaluatePrincipalID = "worker:platform"
	alertEvaluateMaxAttempts = 3
)

// AlertEvaluateArgs 是评估任务的可序列化参数。
//
// RunID 只服务于集成测试的隔离，生产必须留空，让所有实例共享同一条唯一性
// 记录——否则每个副本都会各评估一遍同一个周期，把同一条告警重复投递出去。
type AlertEvaluateArgs struct {
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (AlertEvaluateArgs) Kind() string { return AlertEvaluateJobKind }

// InsertOpts 给每轮评估同一套重试与唯一性策略（照 XM-0022 的模式）。
func (AlertEvaluateArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: alertEvaluateMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultAlertEvaluateInterval,
			ByQueue:  true,
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// AlertEvaluateOptions 是 Worker 的构造参数。
type AlertEvaluateOptions struct {
	Logger      *slog.Logger
	Environment string
	// Reconciler 承担全部业务：评估、去重落库、自动恢复、投递。
	// 本 Worker 只负责调度语义与结构化日志。
	Reconciler *alerts.Reconciler
}

// AlertEvaluateWorker 周期性评估告警规则并投递（规格 §9.3 / §9.4）。
type AlertEvaluateWorker struct {
	river.WorkerDefaults[AlertEvaluateArgs]

	logger      *slog.Logger
	environment string
	reconciler  *alerts.Reconciler
}

// NewAlertEvaluateWorker 构造 Worker 并补齐安全默认值。
func NewAlertEvaluateWorker(opts AlertEvaluateOptions) *AlertEvaluateWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	return &AlertEvaluateWorker{
		logger:      opts.Logger,
		environment: opts.Environment,
		reconciler:  opts.Reconciler,
	}
}

// Work 执行一轮评估。
//
// 返回 error 的条件很窄，与 XM-0022 的同步任务同一条思路：只有**写库失败**
// 才算任务失败。投递失败已经作为 notify_status=failed 落库了，告警页看得见，
// 而下一个 60 秒周期本来就会重试它——再让 River 重试整轮只会把评估白跑一遍。
func (w *AlertEvaluateWorker) Work(ctx context.Context, job *river.Job[AlertEvaluateArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.reconciler == nil {
		return errors.New("jobs: alert evaluate worker has no reconciler")
	}

	res, err := w.reconciler.Reconcile(ctx, w.environment)
	if err != nil {
		// 上下文被取消说明是本进程在关机，不是评估出问题。
		// 记成 job_failed 会让一次正常重启在日志里长得像一次故障。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		w.logJob(ctx, job, slog.LevelError, "job_failed", false, "alert_reconcile_failed",
			slog.Int("findings", res.Findings))
		return fmt.Errorf("alert reconcile: %w", err)
	}

	level := slog.LevelInfo
	errorCode := ""
	switch {
	case res.NotifyFailed > 0:
		// 投递失败不让任务失败，但必须在日志里刺眼：一条落了库却没送到人
		// 手上的告警，是本模块最危险的状态。
		level = slog.LevelWarn
		errorCode = "alert_notify_failed"
	case res.NotifySkipped > 0:
		level = slog.LevelWarn
		errorCode = "no_notifier_configured"
	}

	w.logJob(ctx, job, level, "job_completed", res.NotifyFailed == 0, errorCode,
		slog.Int("findings", res.Findings),
		slog.Int64("threshold_revision", res.ThresholdRevision),
		slog.String("threshold_source", res.ThresholdSource),
		slog.Int("alerts_opened", res.Opened),
		slog.Int("alerts_merged", res.Merged),
		slog.Int("alerts_resolved", res.Resolved),
		slog.Int("notify_delivered", res.Delivered),
		slog.Int("notify_failed", res.NotifyFailed),
		slog.Int("notify_skipped", res.NotifySkipped),
	)
	return nil
}

// logJob 输出与 heartbeat / sub2api_sync 同一套结构化字段。
//
// **永远不记录告警正文与任何凭据材料**：告警标题里有指标键与渠道名，
// detail 里有余额数字。日志是给运维看「这一轮做了几件事」的，
// 具体内容去告警页看——那里有权限控制，日志没有。
func (w *AlertEvaluateWorker) logJob(
	ctx context.Context, job *river.Job[AlertEvaluateArgs],
	level slog.Level, event string, success bool, errorCode string, extra ...slog.Attr,
) {
	jobID, kind, queue, attempt, maxAttempts := alertEvaluateJobFields(job)
	attrs := []slog.Attr{
		slog.String("event", event),
		slog.String("module", "platform.jobs"),
		slog.String("environment", w.environment),
		slog.String("principal_id", alertEvaluatePrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", kind),
		slog.String("queue", queue),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	w.logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}

func alertEvaluateJobFields(job *river.Job[AlertEvaluateArgs]) (int64, string, string, int, int) {
	if job == nil || job.JobRow == nil {
		return 0, AlertEvaluateJobKind, QueueMaintenance, 1, alertEvaluateMaxAttempts
	}
	kind := job.Kind
	if kind == "" {
		kind = AlertEvaluateJobKind
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
		maxAttempts = alertEvaluateMaxAttempts
	}
	return job.ID, kind, queue, attempt, maxAttempts
}

// AlertNotifierConfig 是投递渠道的装配输入（规格 §9.4 平台内部告警）。
type AlertNotifierConfig struct {
	// TelegramBotRef 是 Bot Token 的 CredentialRef，形如 secret://<scope>/<name>。
	TelegramBotRef string
	TelegramChatID string
	// WebhookURL 必须是 https。
	WebhookURL string
	// Secrets 解析 TelegramBotRef。缺它则 Telegram 渠道立不起来。
	Secrets secrets.SecretProvider
	Logger  *slog.Logger
}

// newAlertNotifier 按配置组装投递渠道。
//
// **配错就拒绝启动，没配则只是警告**——这条区分是本函数的全部要点。
//
// 与 XM-0022 的 sub2api 配置刻意相反：那条链路配错时每轮都会往看板写一条
// 说得清原因的 SyncFailed 观测，故障是**可见**的，所以让 worker 照常启动
// 是对的。告警链路没有这个性质：一个因为 URL 拼错而没建起来的 Webhook
// 渠道，唯一的痕迹是启动时一行日志，之后每一条告警都会静静地不投递，
// 而运维会以为自己配好了。等到真出事那天才发现没人被通知——那正是这个
// 模块存在的意义被完全抵消的时刻。
//
// 所以：ref 拼错、URL 不是 https、配了 chat_id 却没配 ref —— 一律返回错误，
// 进程起不来。三个变量一个都没配 —— 返回一个空的 MultiNotifier，
// 由 Reconciler 每轮打 warn「仅落库未投递」。
func newAlertNotifier(cfg AlertNotifierConfig) (*alerts.MultiNotifier, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	var notifiers []alerts.Notifier

	ref := strings.TrimSpace(cfg.TelegramBotRef)
	chatID := strings.TrimSpace(cfg.TelegramChatID)
	switch {
	case ref != "" && chatID != "":
		parsed, err := secrets.ParseCredentialRef(ref)
		if err != nil {
			return nil, fmt.Errorf("XM_ALERT_TELEGRAM_BOT_REF: %w", err)
		}
		if cfg.Secrets == nil {
			return nil, errors.New(
				"配了 XM_ALERT_TELEGRAM_BOT_REF 却没有 SecretProvider：装配缺失，Telegram 渠道立不起来")
		}
		notifier, err := alerts.NewTelegramNotifier(alerts.TelegramOptions{
			TokenRef: parsed,
			ChatID:   chatID,
			Secrets:  cfg.Secrets,
		})
		if err != nil {
			return nil, err
		}
		notifiers = append(notifiers, notifier)
	case ref != "" || chatID != "":
		// 配了一半：这几乎必然是漏配而不是有意为之，而漏配的后果是
		// 「以为 Telegram 通了」。当场拒绝，并指名缺哪个。
		missing := "XM_ALERT_TELEGRAM_CHAT_ID"
		if ref == "" {
			missing = "XM_ALERT_TELEGRAM_BOT_REF"
		}
		return nil, fmt.Errorf("Telegram 告警渠道配置不完整：缺少 %s", missing)
	}

	if url := strings.TrimSpace(cfg.WebhookURL); url != "" {
		notifier, err := alerts.NewWebhookNotifier(url, nil)
		if err != nil {
			return nil, fmt.Errorf("XM_ALERT_WEBHOOK_URL: %w", err)
		}
		notifiers = append(notifiers, notifier)
	}

	return alerts.NewMultiNotifier(logger, notifiers...), nil
}
