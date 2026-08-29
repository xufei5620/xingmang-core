package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// notifyBatchLimit 是单轮投递的上限。
//
// 有上限是必须的：一次上游全挂能一口气产出几十上百条告警，无界地逐条发
// HTTP 会把一轮评估拖过 River 的 JobTimeout，结果是**整轮回滚重来**，
// 下一轮再从头挤——投递永远追不上。有上限则每轮稳定投出一批，
// 剩下的留在 pending 里等下一轮（60 秒后），队列会排空。
const notifyBatchLimit int32 = 50

// AlertStore 是 Reconciler 用到的仓储子集。
//
// 声明接口而不是直接依赖 *Store：规则判定、静默语义与自动恢复这三件事
// 是本模块最容易写错的部分，它们必须能在没有数据库的机器上被完整测试。
// *Store 天然满足本接口。
type AlertStore interface {
	Upsert(ctx context.Context, in UpsertInput) (Alert, bool, error)
	ListActive(ctx context.Context, environment string) ([]Alert, error)
	Resolve(ctx context.Context, id uuid.UUID, at time.Time) (Alert, error)
	ListActiveSilences(ctx context.Context, environment string, at time.Time) ([]Silence, error)
	ListPendingNotify(ctx context.Context, environment string, limit int32) ([]Alert, error)
	MarkDelivered(ctx context.Context, id uuid.UUID, at time.Time) error
	MarkNotifyFailed(ctx context.Context, id uuid.UUID, reason string) error
}

// Result 是一轮 Reconcile 的产出统计，进结构化日志，也供集成测试断言。
type Result struct {
	// Findings 是本轮命中的规则数（去重前）。
	Findings int
	// Opened 是新开的告警数，Merged 是合并进已有告警的次数。
	Opened int
	Merged int
	// Resolved 是本轮自动恢复的告警数（§9.3「恢复条件」）。
	Resolved int
	// Delivered / NotifyFailed 是投递结果。
	Delivered    int
	NotifyFailed int
	// NotifySkipped 是「有待投递的告警，但一个渠道都没配」的条数。
	// 它不是失败，但必须可见——见 Reconcile 里那条 warn。
	NotifySkipped int
	// ThresholdRevision/Source prove which DB-backed runway snapshot this
	// evaluation consumed. They are zero/empty for legacy evaluators without a
	// threshold provider and are safe to emit in worker logs.
	ThresholdRevision int64
	ThresholdSource   string
}

// Reconciler 把一轮评估结果落成库里的告警状态，然后投递。
//
// 顺序固定：评估 → 读静默窗口 → 读当前活跃 → 逐条 Upsert → 自动恢复 → 投递。
//
// 「读当前活跃」必须在 Upsert **之前**：那份快照是算「哪些告警本轮没再命中」
// 的基准。放到后面读就会把本轮刚新开的告警也算进候选集，然后立刻把它们解决掉。
type Reconciler struct {
	store     AlertStore
	evaluator *Evaluator
	notifier  Notifier
	logger    *slog.Logger
	now       func() time.Time
}

// ReconcilerOptions 是构造参数。
type ReconcilerOptions struct {
	Store     AlertStore
	Evaluator *Evaluator
	// Notifier 可为 nil：那表示一个渠道都没配，告警照常落库但不投递。
	Notifier Notifier
	Logger   *slog.Logger
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
}

// NewReconciler 构造并补齐安全默认值。
func NewReconciler(opts ReconcilerOptions) *Reconciler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Reconciler{
		store:     opts.Store,
		evaluator: opts.Evaluator,
		notifier:  opts.Notifier,
		logger:    opts.Logger,
		now:       opts.Now,
	}
}

// Reconcile 跑一轮完整的评估—落库—投递。
func (r *Reconciler) Reconcile(ctx context.Context, environment string) (Result, error) {
	var res Result
	if r.store == nil {
		return res, errors.New("alerts: reconciler 没有仓储")
	}
	if r.evaluator == nil {
		return res, errors.New("alerts: reconciler 没有评估器")
	}
	if environment == "" {
		return res, fmt.Errorf("environment: %w", ErrMissingField)
	}
	now := r.now().UTC()

	findings, err := r.evaluator.Evaluate(ctx, environment, now)
	if err != nil {
		return res, err
	}
	res.Findings = len(findings)
	res.ThresholdRevision, res.ThresholdSource, _ = r.evaluator.LastThresholdSnapshot()

	silences, err := r.store.ListActiveSilences(ctx, environment, now)
	if err != nil {
		return res, err
	}

	// 基准快照：本轮开始时还活着的告警。
	before, err := r.store.ListActive(ctx, environment)
	if err != nil {
		return res, err
	}

	seen := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		silenced := matchesAnySilence(silences, f.RuleKey, now)
		alert, created, err := r.store.Upsert(ctx, UpsertInput{
			RuleKey:         f.RuleKey,
			DedupKey:        f.DedupKey,
			Severity:        f.Severity,
			Title:           f.Title,
			Detail:          f.Detail,
			Environment:     environment,
			SourceMetricKey: f.SourceMetricKey,
			Silenced:        silenced,
			Now:             now,
		})
		if err != nil {
			return res, fmt.Errorf("落库告警 %s: %w", f.DedupKey, err)
		}
		seen[f.DedupKey] = struct{}{}
		if created {
			res.Opened++
			// 新告警是需要有人知道的事，级别给 warn 而不是 info：
			// info 在正常运行的日志洪流里等于看不见。
			r.logger.WarnContext(ctx, "alert_opened",
				slog.String("module", "platform.alerts"),
				slog.String("environment", environment),
				slog.String("alert_id", alert.ID.String()),
				slog.String("rule_key", alert.RuleKey),
				slog.String("dedup_key", alert.DedupKey),
				slog.String("severity", string(alert.Severity)),
				slog.String("status", string(alert.Status)),
			)
			continue
		}
		res.Merged++
	}

	// 本轮没有再命中的活跃告警 = 恢复条件已满足（§9.3）。
	//
	// 这条「没再命中即恢复」的规则是**全部规则共用**的恢复实现：每条 Rule
	// 的 Recovery 字段描述的都是「触发条件不再成立」的具体形态，而在实现上
	// 它们统一表现为「这一轮的 findings 里没有这个 dedup_key」。规则各写各的
	// 恢复判定会立刻长出第二套语义，而且必然与触发判定漂移。
	for _, a := range before {
		if _, still := seen[a.DedupKey]; still {
			continue
		}
		resolved, err := r.store.Resolve(ctx, a.ID, now)
		if errors.Is(err, ErrNotFound) {
			// 与另一条路径竞态（比如有人正好在确认它）。不是错误。
			continue
		}
		if err != nil {
			return res, fmt.Errorf("恢复告警 %s: %w", a.ID, err)
		}
		res.Resolved++
		r.logger.InfoContext(ctx, "alert_resolved",
			slog.String("module", "platform.alerts"),
			slog.String("environment", environment),
			slog.String("alert_id", resolved.ID.String()),
			slog.String("rule_key", resolved.RuleKey),
			slog.String("dedup_key", resolved.DedupKey),
			slog.Int("fire_count", int(resolved.FireCount)),
		)
	}

	if err := r.deliver(ctx, environment, now, &res); err != nil {
		return res, err
	}
	return res, nil
}

// deliver 投递本轮待投递与上轮投递失败的告警（§9.3「通知投递状态」「失败重试」）。
//
// 投递失败**不让整轮 Reconcile 失败**：告警已经诚实落库了，看板看得见，
// 也看得见它没投出去。让 River 重试整轮只会把评估重跑一遍（无意义），
// 而下一个 60 秒的周期本来就会重试投递。真正需要往上冒的只有写库失败。
func (r *Reconciler) deliver(ctx context.Context, environment string, now time.Time, res *Result) error {
	pending, err := r.store.ListPendingNotify(ctx, environment, notifyBatchLimit)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	if r.notifier == nil {
		res.NotifySkipped = len(pending)
		r.logNotifySkipped(ctx, environment, len(pending))
		return nil
	}

	for _, a := range pending {
		err := r.notifier.Notify(ctx, a)
		switch {
		case err == nil:
			if markErr := r.store.MarkDelivered(ctx, a.ID, now); markErr != nil {
				// 投递成功但状态写不进去：下一轮会重投，收件人收到重复消息。
				// 这比反过来（写成 delivered 但其实没发出去）好得多，
				// 但仍然是必须冒泡的写库故障。
				return fmt.Errorf("记录投递成功 %s: %w", a.ID, markErr)
			}
			res.Delivered++
		case errors.Is(err, ErrNoNotifier):
			// MultiNotifier 里一个渠道都没有。不标 failed——见 ErrNoNotifier 注释。
			res.NotifySkipped++
			r.logNotifySkipped(ctx, environment, 1)
		default:
			reason := SanitizeNotifyError(err)
			if markErr := r.store.MarkNotifyFailed(ctx, a.ID, reason); markErr != nil {
				return fmt.Errorf("记录投递失败 %s: %w", a.ID, markErr)
			}
			res.NotifyFailed++
		}
	}
	return nil
}

func (r *Reconciler) logNotifySkipped(ctx context.Context, environment string, count int) {
	// warn 而不是 info：「有告警但没人会被通知到」是一个需要被处理的配置状态，
	// 只是它不是投递故障。落库不投递照样是规格 §9.4 意义上的**没有闭环**。
	r.logger.WarnContext(ctx, "alert_notify_skipped",
		slog.String("module", "platform.alerts"),
		slog.String("environment", environment),
		slog.Int("pending", count),
		slog.String("error_code", "no_notifier_configured"),
		slog.String("hint", "仅落库未投递：未配置 XM_ALERT_TELEGRAM_BOT_REF / XM_ALERT_WEBHOOK_URL"),
	)
}

// matchesAnySilence 报告此刻是否有窗口覆盖这条规则。
func matchesAnySilence(silences []Silence, ruleKey string, at time.Time) bool {
	for _, s := range silences {
		if s.Matches(ruleKey, at) {
			return true
		}
	}
	return false
}
