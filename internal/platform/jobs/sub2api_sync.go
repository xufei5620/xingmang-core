package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	// Sub2APISyncJobKind 是周期同步任务的稳定 River kind。
	Sub2APISyncJobKind = "sub2api_sync"

	// DefaultSub2APISyncInterval 是默认同步周期。
	//
	// 300s 远小于这批指标 1800s 的新鲜度阈值（见 sub2api 包）：偶尔一两个
	// 周期失败不会立刻把看板打成「延迟」，连续失败才会——这正是阈值该表达
	// 的意思。周期本身不承担告警职责，新鲜度是**派生**的（规格 §9.1）。
	DefaultSub2APISyncInterval = 300 * time.Second

	// DefaultSub2APIInstanceID 是默认的来源标识，会原样成为
	// ops.Observation.Source——看板必须显示的字段。
	//
	// 默认值刻意带 -staging 后缀：Fake 模式产出的数字必须一眼能与真实来源
	// 区分开，绝不能伪装成生产数据（宪法 12 条：禁止裸数字冒充实时完整数据）。
	DefaultSub2APIInstanceID = "sub2api-staging"

	sub2apiSyncPrincipalID = "worker:platform"
	sub2apiSyncModule      = "platform.jobs"
	sub2apiSyncMaxAttempts = 3

	// sub2apiBusinessDayLayout 是业务日格式（规格 §5.9：业务日结时区显式声明）。
	sub2apiBusinessDayLayout = "2006-01-02"

	// sub2apiReadTimeout 只约束「读上游」这一段，不约束整个 Work。
	//
	// 分开是有意的：读超时必须还留得下时间把「同步失败」写进库。如果让
	// River 的 JobTimeout（默认 1 分钟）直接掐掉整个 Work，超时那一轮就
	// 什么都写不进去，看板只能靠 observed_at 变旧间接察觉——那是降级信号，
	// 不是失败信号。20s 之后仍有约 40s 用于 5 次 upsert，绰绰有余。
	sub2apiReadTimeout = 20 * time.Second
)

// Sub2APIMode 决定周期任务用哪个 ReadClient 实现。
type Sub2APIMode string

const (
	// Sub2APIModeFake 用 sub2api.NewFake：XM-0017 之前唯一走得通的模式。
	Sub2APIModeFake Sub2APIMode = "fake"
	// Sub2APIModeReal 走真实只读客户端；XM-0017 之前必然失败（见工厂注释）。
	Sub2APIModeReal Sub2APIMode = "real"
)

// ErrSub2APIRealClientUnavailable 是 real 模式当前的确定性失败。
//
// 「XM-0017 未实现，等待只读账号」是一个**事实**，不是异常：与其让配置成
// real 的进程无声无息什么都不采，不如让它每个周期都往库里写一条明确的
// SyncFailed，看板照样看得见这条指标存在、且正在失败（规格 §9.1）。
var ErrSub2APIRealClientUnavailable = errors.New(
	"sub2api 真实只读客户端未实现：XM-0017 未实现，等待只读账号")

// ObservationStore 是本任务用到的 ops 仓储子集。
//
// 只声明用得到的三个方法而不是直接依赖 *ops.Store：单元测试能用固定时钟 +
// 内存实现跑完整条失败路径，不必为了验证「失败也要写」而先起一个库。
// *ops.Store 天然满足本接口。
type ObservationStore interface {
	Get(ctx context.Context, metricKey, environment string) (ops.Observation, error)
	Upsert(ctx context.Context, o ops.Observation) (ops.Observation, error)
	// InsertSample 追加历史样本（XM-0024）。与 Upsert 并列调用而不是二选一：
	// Upsert 维护「现在是什么」，样本维护「一路是怎么过来的」。
	InsertSample(ctx context.Context, o ops.Observation) error
}

// Sub2APIClientFactory 按需构造一个只读客户端。
//
// 用工厂而不是直接持有一个 ReadClient：真实实现（XM-0017）需要在每轮同步
// 时解析 CredentialRef、按连接配置建传输层，那是有生命周期的东西，不该在
// 进程启动时构造一次然后一直握着。
type Sub2APIClientFactory func(ctx context.Context) (sub2api.ReadClient, error)

// NewSub2APIClientFactory 是 XM-0017 真实实现的落点。
//
// 现在只有 fake 一条路走得通。real 模式返回**分类明确**的错误而不是 nil
// client：调用方按 connector.KindOf 归类后写成 SyncFailed 观测，
// 看板显示「同步失败」，而不是数据静静停更（规格 §9.1）。
//
// credentialRef 在这里**只被接住、不被解析出明文**。凭据只经 CredentialRef
// （ADR-014、宪法 7 条），本任务不碰任何凭据材料；XM-0017 接上真实客户端时，
// 在这里用 secrets.Provider + connector.ReadOnlyTransport 把它换成实现，
// 其余调用点一行都不用改。
func NewSub2APIClientFactory(mode Sub2APIMode, credentialRef string) Sub2APIClientFactory {
	return func(context.Context) (sub2api.ReadClient, error) {
		switch mode {
		case Sub2APIModeFake:
			// 固定值即可：Fake 的意义是让上层不被真实凭据阻塞，不是模拟真实波动。
			// 随机化只会让「这条数据是假的」更难被看出来。
			return sub2api.NewFake(sub2api.FakeOptions{}), nil
		case Sub2APIModeReal:
			// _ = credentialRef：XM-0017 的入参已经就位，但现在解析它没有意义
			// ——没有实现可以用它。留着形参而不是留个 TODO，是为了让接缝的形状
			// 在编译期就固定下来。
			_ = credentialRef
			return nil, connector.NewError(
				connector.KindNotSupported, "sub2api.client.real", ErrSub2APIRealClientUnavailable)
		default:
			return nil, connector.NewError(
				connector.KindInternal, "sub2api.client.mode",
				fmt.Errorf("未知的 sub2api 模式 %q", string(mode)))
		}
	}
}

// ParseSub2APIMode 解析模式，空串按 fake 处理。
//
// 默认 fake 而不是 real：真实账号还没就绪，把默认设成 real 只会让每个新环境
// 一上来就满屏同步失败。
func ParseSub2APIMode(s string) (Sub2APIMode, error) {
	switch mode := Sub2APIMode(strings.TrimSpace(s)); mode {
	case "":
		return Sub2APIModeFake, nil
	case Sub2APIModeFake, Sub2APIModeReal:
		return mode, nil
	default:
		return "", fmt.Errorf("sub2api mode %q: 只接受 fake 或 real", s)
	}
}

// Sub2APISyncArgs 是同步任务的可序列化参数。
//
// RunID 只服务于集成测试的隔离，生产必须留空，让所有实例共享同一条唯一性
// 记录——否则每个副本都会各跑一遍同一个周期。
type Sub2APISyncArgs struct {
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (Sub2APISyncArgs) Kind() string { return Sub2APISyncJobKind }

// InsertOpts 给每次同步同一套重试与唯一性策略。
//
// 唯一性按周期 + 队列 + 参数：多副本部署时同一个周期只该有一次同步，
// 重复采集不会让数据更新鲜，只会让上游多挨几次读。
func (Sub2APISyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: sub2apiSyncMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultSub2APISyncInterval,
			ByQueue:  true,
		},
	}
}

// Sub2APISyncOptions 是 Worker 的构造参数。
type Sub2APISyncOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID 会成为观测的 Source。
	InstanceID string
	// Mode 只用于日志标注，不参与任何判定——真正决定读谁的是 NewClient。
	// 运维必须能从日志里一眼看出这批数字是不是 Fake 产的。
	Mode  Sub2APIMode
	Store ObservationStore
	// NewClient 是 XM-0017 的注入点，也是单元测试注入 FakeOptions 的地方。
	NewClient Sub2APIClientFactory
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
}

// Sub2APISyncWorker 周期性读取 Sub2API 只读契约并把结果写进运营指标表。
//
// 它是 Connector 与看板之间那条接缝的消费端：Connector 不直接写库
// （见 sub2api.ToObservations 的注释），由本任务负责落库。
type Sub2APISyncWorker struct {
	river.WorkerDefaults[Sub2APISyncArgs]

	logger      *slog.Logger
	environment string
	instanceID  string
	mode        Sub2APIMode
	store       ObservationStore
	newClient   Sub2APIClientFactory
	now         func() time.Time
}

// NewSub2APISyncWorker 构造 Worker 并补齐安全默认值。
func NewSub2APISyncWorker(opts Sub2APISyncOptions) *Sub2APISyncWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if strings.TrimSpace(opts.InstanceID) == "" {
		opts.InstanceID = DefaultSub2APIInstanceID
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Sub2APISyncWorker{
		logger:      opts.Logger,
		environment: opts.Environment,
		instanceID:  opts.InstanceID,
		mode:        opts.Mode,
		store:       opts.Store,
		newClient:   opts.NewClient,
		now:         opts.Now,
	}
}

// Work 执行一轮同步。
//
// 返回 error 的条件很窄，只有**写库失败**才算任务失败：上游读取失败已经
// 作为 SyncFailed 观测落库了，看板看得见，再让 River 重试只会和 300s 的
// 周期重复排队，而且下一次重试会把刚写好的失败态原样覆盖一遍。真正需要
// 重试的是「话都没说出口」——观测没写进库，或者历史样本没追加上
// （XM-0024，理由见下面 InsertSample 调用处）。
func (w *Sub2APISyncWorker) Work(ctx context.Context, job *river.Job[Sub2APISyncArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.store == nil {
		return errors.New("jobs: sub2api sync worker has no observation store")
	}
	if w.newClient == nil {
		return errors.New("jobs: sub2api sync worker has no client factory")
	}

	now := w.clock()
	// 业务日 = 当天 UTC。时间库内一律 UTC（宪法 14 条）；业务日结时区在这里
	// 显式声明为 UTC，而不是跟着进程所在机器的本地时区漂。
	day := now.Format(sub2apiBusinessDayLayout)

	stats, orders, balances, readErrs := w.read(ctx, day)

	// 上下文被取消说明是本进程在关机，不是上游出问题。把它记成 failed 会让
	// 看板把一次正常重启显示成同步故障——那是**假的**失败信号，比没有信号更糟。
	if err := ctx.Err(); err != nil {
		return err
	}

	// 先按成功路径把五条观测算出来，再把失败分组的那几条替换掉。
	// 这样指标键、来源、新鲜度阈值只有 sub2api.ToObservations 一个来源，
	// 失败路径不会长出第二套指标定义。
	observations := sub2api.ToObservations(now, w.instanceID, w.environment, stats, orders, balances)

	failed := 0
	for i := range observations {
		observation := observations[i]
		if err := readErrs.forMetric(observation.MetricKey); err != nil {
			observation = w.failureObservation(ctx, observation, now, connector.KindOf(err))
			failed++
		}
		if _, err := w.store.Upsert(ctx, observation); err != nil {
			w.logJob(ctx, job, slog.LevelError, "job_failed", false, "observation_upsert_failed",
				slog.String("metric_key", observation.MetricKey))
			return fmt.Errorf("upsert %s: %w", observation.MetricKey, err)
		}
		// 再追加一条历史样本（XM-0024）。先 Upsert 后 InsertSample 是有意的：
		// 看板首页读的是最新态，样本写失败时至少「现在是什么」已经是对的。
		//
		// **成功与失败的观测都留样。** 失败样本正是趋势图上那段红的数据来源；
		// 不留样，图上只会看到一段平直的旧值，看不出中间断过。
		if err := w.store.InsertSample(ctx, observation); err != nil {
			w.logJob(ctx, job, slog.LevelError, "job_failed", false, "observation_sample_failed",
				slog.String("metric_key", observation.MetricKey))
			// 返回 error 让 River 重试，而不是只记日志放过去。
			//
			// 代价是重试会让本轮已经写成功的那几条样本各多出一条重复点——
			// 同一个 synced_at 上两个点，图上是同一个位置，无害。
			// 反过来，只记日志的代价是历史**永久缺一个点**：那一刻的上游数据
			// 已经过去了，没有任何补数途径，而缺口恰好最可能出现在库压力大、
			// 也就是最值得回看的时候。可恢复的重复 vs 不可恢复的缺失，选前者。
			//
			// 重试是安全的：Upsert 幂等（整行覆盖），本方法只是再 INSERT 一次。
			return fmt.Errorf("insert sample %s: %w", observation.MetricKey, err)
		}
	}

	level := slog.LevelInfo
	errorCode := ""
	if failed > 0 {
		// 部分或全部指标同步失败：任务本身成功（失败已诚实落库），但运维需要
		// 在日志里看得见，不能只靠有人主动去翻看板。
		level = slog.LevelWarn
		errorCode = string(connector.KindOf(readErrs.first()))
	}
	w.logJob(ctx, job, level, "job_completed", failed == 0, errorCode,
		slog.String("sub2api_mode", string(w.mode)),
		slog.String("source", w.instanceID),
		slog.String("business_day", day),
		slog.Int("metrics_total", len(observations)),
		slog.Int("metrics_failed", failed),
	)
	return nil
}

// sub2apiReadErrors 记录三组读取各自的结果。
//
// 分组保留而不是「有一个错就整轮算失败」：渠道余额超时不该把已经读到的
// 当日收入一并抹成失败——那会让看板丢掉本来拿得到的真话。
type sub2apiReadErrors struct {
	stats    error
	orders   error
	balances error
}

// forMetric 把指标键映射回它依赖的那次读取。
func (e sub2apiReadErrors) forMetric(metricKey string) error {
	switch metricKey {
	case sub2api.MetricUsersTotal, sub2api.MetricUsersBalance:
		return e.stats
	case sub2api.MetricRevenueDaily, sub2api.MetricCostDaily:
		return e.orders
	case sub2api.MetricChannelBalance:
		return e.balances
	default:
		// 契约将来加了新指标却漏登记在上面：fail closed，任一读取失败就把它
		// 也判为失败，绝不让一条来源不明的指标以「成功」姿态进看板。
		// TestSub2APISyncMetricMappingIsExhaustive 会让这种遗漏在 CI 就暴露。
		return e.first()
	}
}

// first 返回第一个非空错误，用于给整轮同步一个代表性的错误分类。
func (e sub2apiReadErrors) first() error {
	for _, err := range []error{e.stats, e.orders, e.balances} {
		if err != nil {
			return err
		}
	}
	return nil
}

// read 读取三组数据。客户端构造失败时三组一起归到同一个失败分类。
func (w *Sub2APISyncWorker) read(ctx context.Context, day string) (
	sub2api.UserStats, sub2api.OrderSummary, []sub2api.ChannelBalance, sub2apiReadErrors,
) {
	// 读上游单独限时，留出时间把失败写进库（见 sub2apiReadTimeout 注释）。
	readCtx, cancel := context.WithTimeout(ctx, sub2apiReadTimeout)
	defer cancel()

	client, err := w.newClient(readCtx)
	if err != nil {
		return sub2api.UserStats{}, sub2api.OrderSummary{}, nil,
			sub2apiReadErrors{stats: err, orders: err, balances: err}
	}

	stats, statsErr := client.UserStats(readCtx)
	orders, ordersErr := client.DailyOrders(readCtx, day)
	balances, balancesErr := client.ChannelBalances(readCtx)
	return stats, orders, balances, sub2apiReadErrors{
		stats:    statsErr,
		orders:   ordersErr,
		balances: balancesErr,
	}
}

// failureObservation 把一条成功形态的观测改写为失败观测。
//
// 关键在于**保住上一次成功的痕迹**：ops.Store.Upsert 是整行覆盖
// （ON CONFLICT DO UPDATE SET observed_at = EXCLUDED.observed_at …），
// 不先读旧行就写，会把 observed_at / last_success 一起清空，看板会把
// 「同步失败，但半小时前成功过」错报成「从未采集」。旧的 observed_at 留着，
// staleness 才会随时间自然增长——新鲜度是派生的，这条路径不该破坏它。
func (w *Sub2APISyncWorker) failureObservation(
	ctx context.Context, base ops.Observation, now time.Time, kind connector.ErrorKind,
) ops.Observation {
	if kind == "" {
		// 不该发生（调用点只在 err != nil 时进来），但空错误码会被领域层与库层
		// 的一致性 CHECK 一起拒绝，届时连失败都写不进去。归为 internal 兜底。
		kind = connector.KindInternal
	}
	failure := ops.Observation{
		MetricKey:   base.MetricKey,
		Source:      base.Source,
		Environment: base.Environment,
		// SyncedAt 是最近一次同步**尝试**的时刻，无论成败——这是「任务还活着」
		// 的唯一证据，和 observed_at 各管各的。
		SyncedAt:                  now,
		Status:                    ops.SyncFailed,
		LastErrorCode:             string(kind),
		StalenessThresholdSeconds: base.StalenessThresholdSeconds,
		// 没有历史值时不沿用 base 里那份值：base 是拿零值结构体算出来的，
		// 写进去看板会把 total_users: 0 当成「真的是 0」。
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

func (w *Sub2APISyncWorker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}

// logJob 输出与 heartbeat 同一套结构化字段，外加同步专属字段。
// 永远不记录指标值本身与任何凭据材料。
func (w *Sub2APISyncWorker) logJob(
	ctx context.Context, job *river.Job[Sub2APISyncArgs],
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

	jobID, attempt, maxAttempts := int64(0), 1, sub2apiSyncMaxAttempts
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
		slog.String("module", sub2apiSyncModule),
		slog.String("environment", environment),
		slog.String("principal_id", sub2apiSyncPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", Sub2APISyncJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}
