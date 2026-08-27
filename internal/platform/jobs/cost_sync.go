package jobs

// 成本采集周期任务（XM-0037b，设计稿 §8.1）。
//
// 照 sub2api_sync 的四件套：job kind 常量、Args 实现 river.JobArgs、
// Worker 嵌 river.WorkerDefaults、间隔常量；装配在 client.go 的 NewClient
// （AddWorker + append periodic），env 在 cmd/platform-worker/config.go。遵 ADR-013。
//
// 本文件**只管**「多久跑一次、失败要不要重试、日志长什么样」。
// 一轮采集究竟做什么——遍历登记簿、取数、折算、按 §5 三条纪律入账——
// 在 internal/platform/finance/collector.go，那样它不需要 River 也不需要库
// 就能被完整测试。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	// FinanceCollectJobKind 是成本采集任务的稳定 River kind。
	FinanceCollectJobKind = "finance_cost_sync"

	// DefaultFinanceCollectInterval 是默认采集周期。
	//
	// 300s = 5 分钟，§12 拍板「采集频率可配，默认 5min」（对齐 SoloAI
	// relay_scheduler.go:24）。远小于这批指标 1800s 的新鲜度阈值：偶尔一两个
	// 周期失败不会立刻把看板打成「延迟」，连续失败才会——这正是阈值该表达
	// 的意思。周期本身不承担告警职责，新鲜度是**派生**的（规格 §9.1）。
	DefaultFinanceCollectInterval = 300 * time.Second

	// DefaultFinanceCollectInstanceID 是默认来源标识，会原样成为
	// ops.Observation.Source 与每一行台账的 source——看板必须显示的字段。
	//
	// 默认值刻意带 -staging 后缀：Fake 模式产出的数字必须一眼能与真实来源
	// 区分开，绝不能伪装成生产数据（宪法 12 条）。
	DefaultFinanceCollectInstanceID = "finance-collect-staging"

	financeCollectPrincipalID = "worker:platform"
	financeCollectModule      = "platform.jobs"
	financeCollectMaxAttempts = 3

	// DefaultFinanceCollectRequestTimeout 是**单次**上游 HTTP 读取的超时。
	DefaultFinanceCollectRequestTimeout = 10 * time.Second

	// financeCollectReadTimeout 只约束「读上游 + 入账」这一段，不约束整个 Work。
	//
	// 分开是有意的（同 sub2apiReadTimeout）：它必须还留得下时间把
	// 「采集失败」写进库。超时那一轮如果什么都写不进去，看板只能靠
	// observed_at 变旧间接察觉——那是降级信号，不是失败信号。
	financeCollectReadTimeout = 90 * time.Second

	// financeCollectJobTimeout 覆盖 River 的默认任务超时（1 分钟）。
	//
	// 比 sub2api_sync 宽是因为工作量的形状不同：那个任务是**固定三次**上游读取，
	// 本任务是 O(账号数 × 令牌数) 次——成本侧每个令牌一次请求（§3.1 的
	// apikey 自鉴权决定了它无法批量），收入侧每个自营账号一次。几十把令牌
	// 就是几十次串行往返。1 分钟会让规模稍大的部署每轮都被掐断在半路，
	// 而半路被掐断的那一轮**已经写进去一部分行了**（逐行 upsert），
	// 看板上会是一份每轮都不完整、且每轮缺的不是同一批的台账。
	financeCollectJobTimeout = 2 * time.Minute
)

// FinanceCollectMode 决定采集用哪套取数客户端。
type FinanceCollectMode string

const (
	// FinanceCollectModeFake 用 metering.NewFake：真实只读凭据到位前
	// 唯一走得通的模式，让整条链路（登记簿 → 取数 → 折算 → 台账 → 看板）可跑通。
	FinanceCollectModeFake FinanceCollectMode = "fake"
	// FinanceCollectModeReal 走真实只读客户端（endpoint 与凭据引用来自登记簿，
	// allowlist 与 SecretProvider 来自部署）。
	FinanceCollectModeReal FinanceCollectMode = "real"
)

// ParseFinanceCollectMode 解析模式，空串按 fake 处理。
//
// 默认 fake 而不是 real：真实只读账号还没就绪，把默认设成 real 只会让每个
// 新环境一上来就满屏采集失败。
func ParseFinanceCollectMode(s string) (FinanceCollectMode, error) {
	switch mode := FinanceCollectMode(strings.TrimSpace(s)); mode {
	case "":
		return FinanceCollectModeFake, nil
	case FinanceCollectModeFake, FinanceCollectModeReal:
		return mode, nil
	default:
		return "", fmt.Errorf("finance collect mode %q: 只接受 fake 或 real", s)
	}
}

// FinanceCollectArgs 是采集任务的可序列化参数。
//
// RunID 只服务于集成测试的隔离，生产必须留空，让所有实例共享同一条唯一性
// 记录——否则每个副本都会各跑一遍同一个周期，上游挨到 N 倍读取，
// 而台账里同一行被 N 个副本轮流覆盖。
type FinanceCollectArgs struct {
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (FinanceCollectArgs) Kind() string { return FinanceCollectJobKind }

// InsertOpts 给每次采集同一套重试与唯一性策略。
func (FinanceCollectArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: financeCollectMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultFinanceCollectInterval,
			ByQueue:  true,
		},
	}
}

// FinanceCollectOptions 是 Worker 的构造参数。
type FinanceCollectOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID 会成为观测的 Source 与台账行的 source。
	InstanceID string
	// Mode 只用于日志标注，不参与任何判定——真正决定读谁的是 NewClient。
	// 运维必须能从日志里一眼看出这批数字是不是 Fake 产的。
	Mode FinanceCollectMode

	// Store 落运营指标；Registry 与 Ledger 是采集的两端。
	Store    ObservationStore
	Registry finance.AccountRegistry
	Ledger   finance.LedgerWriter

	NewClient       finance.MeteringClientFactory
	ResolvePlatform finance.PlatformResolver

	// Now 可注入固定时钟；默认 time.Now。业务日切分与「今日可覆盖、过去冻结」
	// 都靠它。
	Now func() time.Time
}

// FinanceCollectWorker 周期性采集计量型渠道的成本与收入并写进利润台账。
type FinanceCollectWorker struct {
	river.WorkerDefaults[FinanceCollectArgs]

	logger      *slog.Logger
	environment string
	instanceID  string
	mode        FinanceCollectMode

	store           ObservationStore
	registry        finance.AccountRegistry
	ledger          finance.LedgerWriter
	newClient       finance.MeteringClientFactory
	resolvePlatform finance.PlatformResolver
	now             func() time.Time
}

// NewFinanceCollectWorker 构造 Worker 并补齐安全默认值。
func NewFinanceCollectWorker(opts FinanceCollectOptions) *FinanceCollectWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if strings.TrimSpace(opts.InstanceID) == "" {
		opts.InstanceID = DefaultFinanceCollectInstanceID
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &FinanceCollectWorker{
		logger:          opts.Logger,
		environment:     opts.Environment,
		instanceID:      opts.InstanceID,
		mode:            opts.Mode,
		store:           opts.Store,
		registry:        opts.Registry,
		ledger:          opts.Ledger,
		newClient:       opts.NewClient,
		resolvePlatform: opts.ResolvePlatform,
		now:             opts.Now,
	}
}

// Timeout 放宽本任务的执行期限，理由见 financeCollectJobTimeout。
func (*FinanceCollectWorker) Timeout(*river.Job[FinanceCollectArgs]) time.Duration {
	return financeCollectJobTimeout
}

// Work 执行一轮采集。
//
// 返回 error 的条件很窄，只有**写指标失败**才算任务失败（同 sub2api_sync）：
// 取数失败已经按 §5.1 落成台账里的 NULL 并计进了观测，看板看得见；
// 逐账号的失败也已经被隔离并计数。真正需要重试的是「话都没说出口」——
// 这一轮的观测一个字都没写进库。
//
// **台账本身不因重试而重复**：每一行都是按主键 upsert 的今日行（§5.3），
// 重跑一遍只是拿新读数再覆盖一次，不会产生第二行。
func (w *FinanceCollectWorker) Work(
	ctx context.Context, job *river.Job[FinanceCollectArgs],
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.store == nil {
		return errors.New("jobs: finance collect worker has no observation store")
	}
	if w.registry == nil || w.ledger == nil {
		return errors.New("jobs: finance collect worker has no registry / ledger")
	}
	if w.newClient == nil {
		return errors.New("jobs: finance collect worker has no metering client factory")
	}

	now := w.clock()
	collector := finance.NewCollector(finance.CollectorOptions{
		Logger:          w.logger,
		Environment:     w.environment,
		InstanceID:      w.instanceID,
		Registry:        w.registry,
		Ledger:          w.ledger,
		NewClient:       w.newClient,
		ResolvePlatform: w.resolvePlatform,
		Now:             w.now,
	})

	// 采集单独限时，留出时间把结果（成功或失败）写进指标表。
	collectCtx, cancel := context.WithTimeout(ctx, financeCollectReadTimeout)
	defer cancel()
	result, collectErr := collector.CollectOnce(collectCtx)

	// 上下文被取消说明是本进程在关机，不是采集出问题。把它记成 failed 会让
	// 看板把一次正常重启显示成采集故障——那是**假的**失败信号，比没有信号更糟。
	if err := ctx.Err(); err != nil {
		return err
	}

	observations := result.ToObservations(now, w.instanceID, w.environment)
	if collectErr != nil {
		// 整轮取不到工作清单（读不了登记簿）：三条指标一起记失败。
		// 不是「没有数据」而是「不知道有没有数据」，两者在看板上必须不同。
		kind := connector.KindOf(collectErr)
		for i := range observations {
			observations[i] = w.failureObservation(ctx, observations[i], now, kind)
		}
	}

	for i := range observations {
		// 最新态与历史样本一次写完，同一个事务（XM-R010）。
		// **成功与失败的观测都留样**：失败样本正是趋势图上那段红的数据来源。
		if _, err := w.store.UpsertWithSample(ctx, observations[i]); err != nil {
			w.logJob(ctx, job, slog.LevelError, "job_failed", false, "observation_write_failed",
				slog.String("metric_key", observations[i].MetricKey))
			// 返回 error 让 River 重试，而不是只记日志放过去：只记日志的代价是
			// 历史**永久缺一个点**，而缺口恰好最可能出现在库压力大、
			// 也就是最值得回看的时候。
			return fmt.Errorf("write %s: %w", observations[i].MetricKey, err)
		}
	}

	level := slog.LevelInfo
	errorCode := ""
	switch {
	case collectErr != nil:
		level = slog.LevelError
		errorCode = string(connector.KindOf(collectErr))
	case result.Partial:
		// 部分没采全：任务本身成功（跳过与失败都已诚实落库），但运维需要在
		// 日志里看得见，不能只靠有人主动去翻看板。
		level = slog.LevelWarn
		errorCode = "partial"
		if result.FirstError != nil {
			errorCode = string(connector.KindOf(result.FirstError))
		}
	}
	w.logJob(ctx, job, level, "job_completed", collectErr == nil && !result.Partial, errorCode,
		slog.String("finance_collect_mode", string(w.mode)),
		slog.String("source", w.instanceID),
		slog.String("business_days", strings.Join(result.BusinessDays(), ",")),
		slog.Int("accounts_total", result.AccountsTotal),
		slog.Int("accounts_failed", result.AccountsFailed),
		slog.Int("rows_written", result.RowsWritten),
		slog.Int("rows_skipped_nothing_known", result.RowsSkippedNothingKnown),
		slog.Int("rows_skipped_one_sided", result.RowsSkippedOneSided),
		slog.Int("rows_failed", result.RowsFailed),
	)
	return nil
}

// failureObservation 把一条成功形态的观测改写为失败观测。
//
// 关键在于**保住上一次成功的痕迹**（同 sub2api_sync 的同名函数）：
// ops 的 upsert 是整行覆盖，不先读旧行就写会把 observed_at / last_success
// 一起清空，看板会把「采集失败，但半小时前成功过」错报成「从未采集」。
// 旧的 observed_at 留着，staleness 才会随时间自然增长。
func (w *FinanceCollectWorker) failureObservation(
	ctx context.Context, base ops.Observation, now time.Time, kind connector.ErrorKind,
) ops.Observation {
	if kind == "" {
		// 空错误码会被领域层与库层的一致性 CHECK 一起拒绝，届时连失败都
		// 写不进去。归 internal 兜底。
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
		// 不沿用 base 里那份值：整轮失败时它是拿零值算出来的，
		// 写进去看板会把 rows_written: 0 当成「真的一行都没写」。
		Value: map[string]any{},
	}

	previous, err := w.store.Get(ctx, base.MetricKey, base.Environment)
	if err != nil {
		// 读不到旧行（第一次采集，或库瞬时故障）：observed_at 留空，
		// 看板显示「未初始化」，而不是拿 now 冒充一次成功观测。
		return failure
	}
	failure.ObservedAt = previous.ObservedAt
	failure.LastSuccess = previous.LastSuccess
	failure.Watermark = previous.Watermark
	failure.IsPartial = previous.IsPartial
	if len(previous.Value) > 0 {
		// 沿用上次成功的值：看板显示「上次已知值 + 失败徽章 + 越来越大的
		// staleness」，比一失败就清空更有信息量，也不会骗人——状态字段
		// 已经说清楚了这是旧值（规格 §9.1 的状态优先级：失败 > 延迟）。
		failure.Value = previous.Value
	}
	return failure
}

func (w *FinanceCollectWorker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}

// logJob 输出与其他周期任务同一套结构化字段，外加采集专属字段。
//
// **永远不记录金额值本身与任何凭据材料**：日志里只有计数与错误分类。
// 台账里的金额是按环境与权限裁剪过的数据（finance.ScopeRead 单独授予，
// 见 permissions.go 的理由），把它顺手写进进程日志等于绕开那道授权。
func (w *FinanceCollectWorker) logJob(
	ctx context.Context, job *river.Job[FinanceCollectArgs],
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

	jobID, attempt, maxAttempts := int64(0), 1, financeCollectMaxAttempts
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
		slog.String("module", financeCollectModule),
		slog.String("environment", environment),
		slog.String("principal_id", financeCollectPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", FinanceCollectJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}

// NewFinanceCollectClientFactory 按模式构造取数客户端工厂。
//
// real 模式的 endpoint 与 CredentialRef 逐账号来自登记簿（§2.1），
// 只有 allowlist 与 SecretProvider 来自部署——所以这里传进去的配置很薄。
// 配置不全时**不返回 nil client**，而是让每个账号都拿到一个分类明确的错误：
// 那会被逐账号计为失败并写进观测，看板显示「采集失败」且说得出原因，
// 而不是数据静静停更（规格 §9.1）。
func NewFinanceCollectClientFactory(
	mode FinanceCollectMode, cfg finance.RealMeteringConfig, now func() time.Time,
) finance.MeteringClientFactory {
	switch mode {
	case FinanceCollectModeReal:
		return finance.NewRealMeteringClientFactory(cfg, now)
	default:
		// 未知模式走不到这里：Config.validate 已经用 ParseFinanceCollectMode
		// 拦过。默认落 fake 而不是报错，是因为这个函数的调用点在进程装配里，
		// 那时再报一个「模式非法」只会重复一遍已经报过的事。
		return finance.NewFakeMeteringClientFactory(now)
	}
}
