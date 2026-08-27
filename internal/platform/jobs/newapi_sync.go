package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	// NewAPISyncJobKind 是周期同步任务的稳定 River kind。
	NewAPISyncJobKind = "newapi_sync"

	// DefaultNewAPISyncInterval 是默认同步周期。
	//
	// 300s 远小于这批指标 1800s 的新鲜度阈值（见 newapi 包）：偶尔一两个
	// 周期失败不会立刻把看板打成「延迟」，连续失败才会——这正是阈值该表达
	// 的意思。周期本身不承担告警职责，新鲜度是**派生**的（规格 §9.1）。
	DefaultNewAPISyncInterval = 300 * time.Second

	// DefaultNewAPIInstanceID 是默认的来源标识，会原样成为
	// ops.Observation.Source——看板必须显示的字段。
	//
	// 默认值刻意带 -staging 后缀：Fake 模式产出的数字必须一眼能与真实来源
	// 区分开，绝不能伪装成生产数据（宪法 12 条：禁止裸数字冒充实时完整数据）。
	// 前端的演示横幅按这个字符串**整串相等**匹配（web 的 DEFAULT_DEMO_SOURCES），
	// 改这里必须同步改那边，否则演示数据会失去「这是假的」这层标注。
	DefaultNewAPIInstanceID = "newapi-staging"

	newapiSyncPrincipalID = "worker:platform"
	newapiSyncModule      = "platform.jobs"
	newapiSyncMaxAttempts = 3

	// newapiReadTimeout 只约束「读上游」这一段，不约束整个 Work。
	//
	// 分开是有意的：读超时必须还留得下时间把「同步失败」写进库。如果让
	// River 的 JobTimeout（默认 1 分钟）直接掐掉整个 Work，超时那一轮就
	// 什么都写不进去，看板只能靠 observed_at 变旧间接察觉——那是降级信号，
	// 不是失败信号。20s 之后仍有约 40s 用于 5 次 upsert，绰绰有余。
	newapiReadTimeout = 20 * time.Second
)

// NewAPIMode 决定周期任务用哪个 ReadClient 实现。
type NewAPIMode string

const (
	// NewAPIModeFake 用 newapi.NewFake：XM-0038 之前唯一走得通的模式。
	NewAPIModeFake NewAPIMode = "fake"
	// NewAPIModeReal 走真实只读客户端；XM-0038 之前必然失败（见工厂注释）。
	NewAPIModeReal NewAPIMode = "real"
)

// ErrNewAPIRealClientUnavailable 是 real 模式在 XM-0038 之前的确定性失败。
//
// 与 sub2api 那条同名错误的差别值得说清楚：sub2api 的真实客户端**已经存在**，
// 它那条错误表达的是「客户端在，配置没配齐」；NewAPI 这里连客户端本身都还
// 没写（XM-0038），所以 real 模式不是「配了就能用」，而是**现在无论怎么配
// 都走不通**。因此本包不提供 endpoint / allowlist / credential 这些配置项：
// 加一堆没有任何代码会读的环境变量，只会让人以为配齐了就能切真实数据。
//
// 「走不通」是一个**事实**，不是异常：与其让配置成 real 的进程无声无息什么
// 都不采，不如让它每个周期都往库里写一条明确的 SyncFailed，看板照样看得见
// 这条指标存在、且正在失败（规格 §9.1）。
//
// 分类是 not_supported（而不是 internal）：它表达的是「本部署还不具备真实
// 读取能力」，运维一看 error_code 就知道这是在等一个未交付的任务，
// 而不是自己哪里配错了。
var ErrNewAPIRealClientUnavailable = errors.New(
	"newapi 真实只读客户端尚未实现（XM-0038）：Foundation-A 阶段只能用 fake 模式")

// NewAPIClientFactory 按需构造一个只读客户端。
//
// 用工厂而不是直接持有一个 ReadClient：真实实现（XM-0038）需要在每轮同步时
// 解析 CredentialRef、按 §8.4 的 Connector → CredentialRef → SecretProvider →
// 只读 DSN 链路建连接，那是有生命周期的东西，不该在进程启动时构造一次然后
// 一直握着——凭据会轮换，握着的连接不会知道。
type NewAPIClientFactory func(ctx context.Context) (newapi.ReadClient, error)

// NewNewAPIClientFactory 按模式构造只读客户端。
//
// real 模式在 XM-0038 之前返回**分类明确**的错误而不是 nil client：调用方按
// connector.KindOf 归类后写成 SyncFailed 观测，看板显示「同步失败」并说得出
// 失败原因，而不是数据静静停更（规格 §9.1）。
func NewNewAPIClientFactory(mode NewAPIMode) NewAPIClientFactory {
	// 形参是 context.Context 而不是具名 ctx：这一层不做任何 I/O。
	return func(context.Context) (newapi.ReadClient, error) {
		switch mode {
		case NewAPIModeFake:
			// 固定值即可：Fake 的意义是让上层不被真实凭据阻塞，不是模拟真实波动。
			// 随机化只会让「这条数据是假的」更难被看出来。
			return newapi.NewFake(newapi.FakeOptions{}), nil
		case NewAPIModeReal:
			return nil, connector.NewError(
				connector.KindNotSupported, "newapi.client.real",
				ErrNewAPIRealClientUnavailable)
		default:
			return nil, connector.NewError(
				connector.KindInternal, "newapi.client.mode",
				fmt.Errorf("未知的 newapi 模式 %q", string(mode)))
		}
	}
}

// ParseNewAPIMode 解析模式，空串按 fake 处理。
//
// 默认 fake 而不是 real：真实客户端还没写（XM-0038），把默认设成 real
// 只会让每个新环境一上来就满屏同步失败。
func ParseNewAPIMode(s string) (NewAPIMode, error) {
	switch mode := NewAPIMode(strings.TrimSpace(s)); mode {
	case "":
		return NewAPIModeFake, nil
	case NewAPIModeFake, NewAPIModeReal:
		return mode, nil
	default:
		return "", fmt.Errorf("newapi mode %q: 只接受 fake 或 real", s)
	}
}

// NewAPISyncArgs 是同步任务的可序列化参数。
//
// RunID 只服务于集成测试的隔离，生产必须留空，让所有实例共享同一条唯一性
// 记录——否则每个副本都会各跑一遍同一个周期。
type NewAPISyncArgs struct {
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (NewAPISyncArgs) Kind() string { return NewAPISyncJobKind }

// InsertOpts 给每次同步同一套重试与唯一性策略。
//
// 唯一性按周期 + 队列 + 参数：多副本部署时同一个周期只该有一次同步，
// 重复采集不会让数据更新鲜，只会让上游多挨几次读。
func (NewAPISyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: newapiSyncMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultNewAPISyncInterval,
			ByQueue:  true,
		},
	}
}

// NewAPISyncOptions 是 Worker 的构造参数。
type NewAPISyncOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID 会成为观测的 Source。
	InstanceID string
	// Mode 只用于日志标注，不参与任何判定——真正决定读谁的是 NewClient。
	// 运维必须能从日志里一眼看出这批数字是不是 Fake 产的。
	Mode  NewAPIMode
	Store ObservationStore
	// NewClient 是 XM-0038 的注入点，也是单元测试注入 FakeOptions 的地方。
	NewClient NewAPIClientFactory
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
}

// NewAPISyncWorker 周期性读取 NewAPI 只读契约并把结果写进运营指标表。
//
// 它是 Connector 与看板之间那条接缝的消费端：Connector 不直接写库
// （见 newapi.ToObservations 的注释），由本任务负责落库。
type NewAPISyncWorker struct {
	river.WorkerDefaults[NewAPISyncArgs]

	logger      *slog.Logger
	environment string
	instanceID  string
	mode        NewAPIMode
	store       ObservationStore
	newClient   NewAPIClientFactory
	now         func() time.Time
}

// NewNewAPISyncWorker 构造 Worker 并补齐安全默认值。
func NewNewAPISyncWorker(opts NewAPISyncOptions) *NewAPISyncWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if strings.TrimSpace(opts.InstanceID) == "" {
		opts.InstanceID = DefaultNewAPIInstanceID
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &NewAPISyncWorker{
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
// 重试的是「话都没说出口」——观测没写进库，或者历史样本没追加上。
func (w *NewAPISyncWorker) Work(ctx context.Context, job *river.Job[NewAPISyncArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.store == nil {
		return errors.New("jobs: newapi sync worker has no observation store")
	}
	if w.newClient == nil {
		return errors.New("jobs: newapi sync worker has no client factory")
	}

	now := w.clock()
	// 业务日 = 当天 UTC。时间库内一律 UTC（宪法 14 条）；业务日结时区在这里
	// 显式声明为 UTC，而不是跟着进程所在机器的本地时区漂。
	day := now.Format(newapi.BusinessDayLayout)

	reads, readErrs := w.read(ctx, day)

	// 上下文被取消说明是本进程在关机，不是上游出问题。把它记成 failed 会让
	// 看板把一次正常重启显示成同步故障——那是**假的**失败信号，比没有信号更糟。
	if err := ctx.Err(); err != nil {
		return err
	}

	// 先按成功路径把五条观测算出来，再把失败分组的那几条替换掉。
	// 这样指标键、来源、新鲜度阈值只有 newapi.ToObservations 一个来源，
	// 失败路径不会长出第二套指标定义。
	observations := newapi.ToObservations(now, w.instanceID, w.environment,
		reads.stats, reads.orders, reads.channels, reads.usages)

	failed := 0
	for i := range observations {
		observation := observations[i]
		if err := readErrs.forMetric(observation.MetricKey); err != nil {
			observation = w.failureObservation(ctx, observation, now, connector.KindOf(err))
			failed++
		}
		// 最新态与历史样本同一事务写入（XM-R010）：要么都生效,要么两张表
		// 都没动——事务失败即 River 干净重放,不存在半截状态与历史缺口。
		// 本任务基于修复前基线开发,合并时由集成方改为事务写法,与
		// sub2api_sync 保持一致。
		//
		// **成功与失败的观测都留样。** 失败样本正是趋势图上那段红的数据来源；
		// 不留样，图上只会看到一段平直的旧值，看不出中间断过。
		if _, err := w.store.UpsertWithSample(ctx, observation); err != nil {
			w.logJob(ctx, job, slog.LevelError, "job_failed", false, "observation_write_failed",
				slog.String("metric_key", observation.MetricKey))
			return fmt.Errorf("write observation %s: %w", observation.MetricKey, err)
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
		slog.String("newapi_mode", string(w.mode)),
		slog.String("source", w.instanceID),
		slog.String("business_day", day),
		slog.Int("metrics_total", len(observations)),
		slog.Int("metrics_failed", failed),
	)
	return nil
}

// newapiReads 是一轮同步读到的四组数据。
type newapiReads struct {
	stats    newapi.UserStats
	orders   newapi.OrderSummary
	channels []newapi.ChannelStatus
	usages   []newapi.ModelUsage
}

// newapiReadErrors 记录四组读取各自的结果。
//
// 分组保留而不是「有一个错就整轮算失败」：模型用量超时不该把已经读到的
// 渠道状态一并抹成失败——那会让看板丢掉本来拿得到的真话。
//
// 是四组而不是照抄 sub2api 的三组：分组数跟着**读取次数**走，不跟着别的
// Connector 走。NewAPI 有四个数据读取方法，合并任意两组都会让其中一组的
// 失败去污染另一组本来成功的指标。
type newapiReadErrors struct {
	stats    error
	orders   error
	channels error
	usages   error
}

// forMetric 把指标键映射回它依赖的那次读取。
func (e newapiReadErrors) forMetric(metricKey string) error {
	switch metricKey {
	case newapi.MetricUsersTotal:
		return e.stats
	case newapi.MetricRechargeDaily, newapi.MetricSubscriptionDaily:
		return e.orders
	case newapi.MetricChannelsStatus:
		return e.channels
	case newapi.MetricModelsUsage:
		return e.usages
	default:
		// 契约将来加了新指标却漏登记在上面：fail closed，任一读取失败就把它
		// 也判为失败，绝不让一条来源不明的指标以「成功」姿态进看板。
		// TestNewAPISyncMetricMappingIsExhaustive 会让这种遗漏在 CI 就暴露。
		return e.first()
	}
}

// first 返回第一个非空错误，用于给整轮同步一个代表性的错误分类。
func (e newapiReadErrors) first() error {
	for _, err := range []error{e.stats, e.orders, e.channels, e.usages} {
		if err != nil {
			return err
		}
	}
	return nil
}

// read 读取四组数据。客户端构造失败时四组一起归到同一个失败分类。
func (w *NewAPISyncWorker) read(ctx context.Context, day string) (newapiReads, newapiReadErrors) {
	// 读上游单独限时，留出时间把失败写进库（见 newapiReadTimeout 注释）。
	readCtx, cancel := context.WithTimeout(ctx, newapiReadTimeout)
	defer cancel()

	client, err := w.newClient(readCtx)
	if err != nil {
		// real 模式在 XM-0038 之前必然走这一支：五条指标全部写成
		// not_supported 的失败观测，而不是静静地什么都不采。
		return newapiReads{}, newapiReadErrors{
			stats: err, orders: err, channels: err, usages: err,
		}
	}

	var reads newapiReads
	var errs newapiReadErrors
	reads.stats, errs.stats = client.UserStats(readCtx)
	reads.orders, errs.orders = client.DailyOrders(readCtx, day)
	reads.channels, errs.channels = client.Channels(readCtx)
	reads.usages, errs.usages = client.ModelUsages(readCtx, day)
	return reads, errs
}

// failureObservation 把一条成功形态的观测改写为失败观测。
//
// 关键在于**保住上一次成功的痕迹**：ops.Store.Upsert 是整行覆盖
// （ON CONFLICT DO UPDATE SET observed_at = EXCLUDED.observed_at …），
// 不先读旧行就写，会把 observed_at / last_success 一起清空，看板会把
// 「同步失败，但半小时前成功过」错报成「从未采集」。旧的 observed_at 留着，
// staleness 才会随时间自然增长——新鲜度是派生的，这条路径不该破坏它。
func (w *NewAPISyncWorker) failureObservation(
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

func (w *NewAPISyncWorker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}

// logJob 输出与 heartbeat 同一套结构化字段，外加同步专属字段。
// 永远不记录指标值本身与任何凭据材料。
func (w *NewAPISyncWorker) logJob(
	ctx context.Context, job *river.Job[NewAPISyncArgs],
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

	jobID, attempt, maxAttempts := int64(0), 1, newapiSyncMaxAttempts
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
		slog.String("module", newapiSyncModule),
		slog.String("environment", environment),
		slog.String("principal_id", newapiSyncPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", NewAPISyncJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}
