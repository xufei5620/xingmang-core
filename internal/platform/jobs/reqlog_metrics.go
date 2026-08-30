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

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	// ReqlogMetricsJobKind 是周期聚合任务的稳定 River kind（XM-REQLOG-METRICS）。
	ReqlogMetricsJobKind = "reqlog_metrics"

	// DefaultReqlogMetricsInterval 是默认采集周期（交付契约：5 分钟）。
	//
	// 远小于这批指标 900s 的新鲜度阈值：偶尔一两轮读盘失败不会立刻把看板打成
	// 「延迟」，连续失败才会——与 sub2api_sync/newapi_sync 同一条纪律
	// （规格 §9.1：新鲜度是派生的，不是周期本身该承担的职责）。
	DefaultReqlogMetricsInterval = 5 * time.Minute

	reqlogMetricsPrincipalID = "worker:platform"
	reqlogMetricsModule      = "platform.jobs"
	reqlogMetricsMaxAttempts = 3

	// reqlogMetricsReadTimeout 只约束「扫盘」这一段，不约束整个 Work——
	// 与 sub2apiReadTimeout/newapiReadTimeout 同一条理由：本地磁盘扫描远比
	// HTTP 上游快，仍然给一个独立预算，留出时间把失败写进库。
	reqlogMetricsReadTimeout = 20 * time.Second

	// defaultReqlogMetricsDataDir 是 file 模式下记录代理数据目录在**worker
	// 容器内**的挂载点，与 cmd/platform-api 的 defaultReqlogDataDir 是同一个
	// 值——两个进程只读挂载同一份宿主机数据（deploy/compose/server-prod.yaml），
	// 容器内路径没有理由让两边漂开。
	defaultReqlogMetricsDataDir = "/var/lib/xm/reqlog"
)

// ReqlogMetricsMode 决定「请求量/成功率」聚合任务是否启用。
//
// 与其余采集任务的 Enabled bool 不同，这里没有单独的布尔开关——模式本身
// 就是开关：off 完全不注册这个周期任务（不写任何观测，也不写
// not_supported，这条链路「未接入」由前端按缺观测处理，见 XM-UX-OFFSTATE
// 的同类先例）；file 注册。
type ReqlogMetricsMode string

const (
	ReqlogMetricsModeOff  ReqlogMetricsMode = "off"
	ReqlogMetricsModeFile ReqlogMetricsMode = "file"
)

// ParseReqlogMetricsMode 解析 worker 侧认识的两档模式，空串按 off 处理。
//
// 与 platform-api 共享同一个环境变量名 XM_REQLOG_MODE（cmd/platform-api/
// reqlog.go 的 parseReqlogMode），但那边还认识 fake/real 两档——服务的是
// 「请求详情」这条完全不同的链路（HTTP 查询单条记录的正文）。本任务只从
// 磁盘聚合 index.jsonl 的计数字段，没有「演示数据」或「HTTP 只读客户端」
// 的概念，因此**不对 fake/real 报错**：遇到就退化成 off，recognized 返回
// false 让调用方记一条 warn。一个只服务于另一条链路的合法值不该把整个
// worker 启动打断——那会连累心跳与全部其余任务，而这里唯一的代价只是
// 「这一条聚合链路这一轮不跑」。
func ParseReqlogMetricsMode(s string) (mode ReqlogMetricsMode, recognized bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off":
		return ReqlogMetricsModeOff, true
	case "file":
		return ReqlogMetricsModeFile, true
	default:
		return ReqlogMetricsModeOff, false
	}
}

// ReqlogMetricsArgs 是同步任务的可序列化参数。
//
// RunID 只服务于集成测试的隔离，生产必须留空，让所有实例共享同一条唯一性
// 记录——否则每个副本都会各跑一遍同一个周期。
type ReqlogMetricsArgs struct {
	RunID string `json:"run_id,omitempty"`
}

// Kind 返回稳定的 River kind。
func (ReqlogMetricsArgs) Kind() string { return ReqlogMetricsJobKind }

// InsertOpts 给每次同步同一套重试与唯一性策略。
func (ReqlogMetricsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: reqlogMetricsMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: DefaultReqlogMetricsInterval,
			ByQueue:  true,
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// ReqlogMetricsOptions 是 Worker 的构造参数。
type ReqlogMetricsOptions struct {
	Logger      *slog.Logger
	Environment string
	// DataDir 是记录代理数据目录在 worker 容器内的挂载路径，用于构造默认
	// 的 NewReader 工厂；显式提供 NewReader 时本字段只用于日志标注。
	DataDir string
	// Sub2APISource / NewAPISource 是两个平台各自的观测 Source。
	//
	// 与 sub2api_sync / newapi_sync 使用**同一个**实例标识（交付契约：
	// 「source 与现有同步一致」）——调用方直接传 cfg.Sub2APIInstanceID /
	// cfg.NewAPIInstanceID，本任务不另开一套 XM_REQLOG_METRICS_*_INSTANCE_ID。
	Sub2APISource string
	NewAPISource  string
	Store         ObservationStore
	// NewReader 构造读取器；为空时按 DataDir 构造一个真实的
	// reqlog.NewMetricsReader。单元测试注入指向临时目录的读取器。
	NewReader func() (*reqlog.MetricsReader, error)
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
	// ExpectedInterval is the effective cadence of this writer.  It is copied
	// into every raw sample; zero keeps legacy/manual coverage unknown.
	ExpectedInterval time.Duration
}

// ReqlogMetricsWorker 周期性从记录代理落盘的 index.jsonl 聚合请求量/成功率
// 并写进运营指标表。
//
// 它是 connectors/reqlog（聚合读取器）与看板之间那条接缝的消费端：
// 聚合器不直接写库，由本任务负责落库——与 sub2api.ToObservations /
// newapi.ToObservations 交给各自 Worker 落库是同一条纪律。
type ReqlogMetricsWorker struct {
	river.WorkerDefaults[ReqlogMetricsArgs]

	logger           *slog.Logger
	environment      string
	dataDir          string
	sub2apiSource    string
	newapiSource     string
	store            ObservationStore
	newReader        func() (*reqlog.MetricsReader, error)
	now              func() time.Time
	expectedInterval time.Duration
}

// NewReqlogMetricsWorker 构造 Worker 并补齐安全默认值。
func NewReqlogMetricsWorker(opts ReqlogMetricsOptions) *ReqlogMetricsWorker {
	if opts.Logger == nil {
		opts.Logger = structuredDefaultLogger()
	}
	if strings.TrimSpace(opts.Environment) == "" {
		opts.Environment = "unknown"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewReader == nil {
		dataDir := opts.DataDir
		logger := opts.Logger
		opts.NewReader = func() (*reqlog.MetricsReader, error) {
			return reqlog.NewMetricsReader(reqlog.MetricsReaderConfig{
				DataDir: dataDir,
				Logger:  logger,
			})
		}
	}
	return &ReqlogMetricsWorker{
		logger:           opts.Logger,
		environment:      opts.Environment,
		dataDir:          opts.DataDir,
		sub2apiSource:    opts.Sub2APISource,
		newapiSource:     opts.NewAPISource,
		store:            opts.Store,
		newReader:        opts.NewReader,
		now:              opts.Now,
		expectedInterval: opts.ExpectedInterval,
	}
}

// reqlogMetricsPlatformSpec 把「某个平台」要用到的 reqlog source 字面量、
// 观测 Source、三个指标键捆在一起，让 Work() 对 sub2api/newapi 跑同一段
// 循环体，不必复制两遍——契约里两个平台的三个键呈完全对称的形状
// （仅前缀不同）。
type reqlogMetricsPlatformSpec struct {
	// reqlogSource 是 reqlog.SourceSub2API / SourceNewAPI，筛选 index 行用。
	reqlogSource                string
	obsSource                   string
	dailyKey, rateKey, trendKey string
}

func (w *ReqlogMetricsWorker) platforms() []reqlogMetricsPlatformSpec {
	return []reqlogMetricsPlatformSpec{
		{
			reqlogSource: reqlog.SourceSub2API, obsSource: w.sub2apiSource,
			dailyKey: reqlog.MetricSub2APIRequestsDaily,
			rateKey:  reqlog.MetricSub2APIRequestsSuccessRate24h,
			trendKey: reqlog.MetricSub2APIRequestsTrend7d,
		},
		{
			reqlogSource: reqlog.SourceNewAPI, obsSource: w.newapiSource,
			dailyKey: reqlog.MetricNewAPIRequestsDaily,
			rateKey:  reqlog.MetricNewAPIRequestsSuccessRate24h,
			trendKey: reqlog.MetricNewAPIRequestsTrend7d,
		},
	}
}

// platformResult 是一个平台一轮聚合的结果：成功时 err 为 nil、observations
// 是三条「成功形态」的观测；失败时 observations 是三条只有骨架字段
// （MetricKey/Source/Environment/StalenessThresholdSeconds）的占位观测，
// 由调用方统一改写成失败观测（failureObservation）。
type platformResult struct {
	observations []ops.Observation
	err          error
}

// Work 执行一轮聚合。
//
// 返回 error 的条件很窄，只有**写库失败**才算任务失败：读盘失败已经作为
// SyncFailed 观测落库了，看板看得见，再让 River 重试只会和 5 分钟的周期
// 重复排队——与 sub2api_sync/newapi_sync 同一条纪律。
func (w *ReqlogMetricsWorker) Work(ctx context.Context, job *river.Job[ReqlogMetricsArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.store == nil {
		return errors.New("jobs: reqlog metrics worker has no observation store")
	}
	if w.newReader == nil {
		return errors.New("jobs: reqlog metrics worker has no reader factory")
	}

	now := w.clock()
	readCtx, cancel := context.WithTimeout(ctx, reqlogMetricsReadTimeout)
	defer cancel()

	reader, rootErr := w.newReader()
	if rootErr == nil {
		rootErr = reader.CheckRoot(readCtx)
	}

	platforms := w.platforms()
	results := make([]platformResult, len(platforms))
	for i, platform := range platforms {
		if rootErr != nil {
			results[i] = platformResult{observations: w.placeholderObservations(platform, now), err: rootErr}
			continue
		}
		obs, err := w.readPlatform(readCtx, reader, platform, now)
		results[i] = platformResult{observations: obs, err: err}
	}

	// 上下文被取消说明是本进程在关机，不是磁盘出问题。把它记成 failed 会让
	// 看板把一次正常重启显示成同步故障——那是**假的**失败信号，比没有信号
	// 更糟（与 sub2api_sync/newapi_sync 的 Work() 同一条纪律）。
	if err := ctx.Err(); err != nil {
		return err
	}

	failed := 0
	total := 0
	var firstErr error
	for _, result := range results {
		for i := range result.observations {
			total++
			observation := result.observations[i]
			if result.err != nil {
				if firstErr == nil {
					firstErr = result.err
				}
				observation = w.failureObservation(ctx, observation, now, connector.KindOf(result.err))
				failed++
			}
			annotateRollupMetadata(&observation, w.expectedInterval)
			// 最新态与历史样本一次写完，同一个事务（XM-R010）：成功与失败的
			// 观测都留样，趋势图上那段红正是从失败样本里画出来的。
			if _, err := w.store.UpsertWithSample(ctx, observation); err != nil {
				w.logJob(ctx, job, slog.LevelError, "job_failed", false, "observation_write_failed",
					slog.String("metric_key", observation.MetricKey))
				return fmt.Errorf("write %s: %w", observation.MetricKey, err)
			}
		}
	}

	level := slog.LevelInfo
	errorCode := ""
	if failed > 0 {
		level = slog.LevelWarn
		errorCode = string(connector.KindOf(firstErr))
	}
	w.logJob(ctx, job, level, "job_completed", failed == 0, errorCode,
		slog.String("source_data_dir", w.dataDir),
		slog.Int("metrics_total", total),
		slog.Int("metrics_failed", failed),
	)
	return nil
}

// readPlatform 为一个平台跑三次扫描（今日 / 24h 窗口 / 7 天趋势），再翻译成
// 三条「成功形态」的观测。
func (w *ReqlogMetricsWorker) readPlatform(
	ctx context.Context, reader *reqlog.MetricsReader,
	platform reqlogMetricsPlatformSpec, now time.Time,
) ([]ops.Observation, error) {
	daily, err := reader.DailyStats(ctx, platform.reqlogSource, now)
	if err != nil {
		return w.placeholderObservations(platform, now), err
	}
	window, err := reader.WindowStats(ctx, platform.reqlogSource, now.Add(-24*time.Hour), now)
	if err != nil {
		return w.placeholderObservations(platform, now), err
	}
	trend, err := reader.TrendDays(ctx, platform.reqlogSource, now, reqlog.RequestsTrendDays)
	if err != nil {
		return w.placeholderObservations(platform, now), err
	}
	observations, err := reqlog.ToRequestMetricsObservations(now, platform.obsSource, w.environment,
		platform.dailyKey, platform.rateKey, platform.trendKey,
		reqlog.RequestMetricsInputs{Daily: daily, Window: window, Trend: trend})
	if err != nil {
		return w.placeholderObservations(platform, now), err
	}
	return observations, nil
}

// placeholderObservations 造三条「形状正确但没有值」的观测骨架，专给失败
// 路径用——failureObservation 只需要 MetricKey/Source/Environment/
// StalenessThresholdSeconds 四个字段就能工作，其余字段它自己会补
// （沿用上次成功值或留空，见该方法注释）。
func (w *ReqlogMetricsWorker) placeholderObservations(platform reqlogMetricsPlatformSpec, now time.Time) []ops.Observation {
	skeleton := func(metricKey string) ops.Observation {
		return ops.Observation{
			MetricKey: metricKey, Source: platform.obsSource, Environment: w.environment,
			SyncedAt: now, Status: ops.SyncOK,
			StalenessThresholdSeconds: reqlog.MetricsStalenessThresholdSeconds,
		}
	}
	return []ops.Observation{
		skeleton(platform.dailyKey), skeleton(platform.rateKey), skeleton(platform.trendKey),
	}
}

// failureObservation 把一条成功形态的观测改写为失败观测。
//
// 关键在于**保住上一次成功的痕迹**：ops.Store.Upsert 是整行覆盖，不先读旧
// 行就写会把 observed_at / last_success 一起清空，看板会把「同步失败，但
// 半小时前成功过」错报成「从未采集」——与 sub2api_sync/newapi_sync 的同名
// 方法逐字同一条纪律。
func (w *ReqlogMetricsWorker) failureObservation(
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

func (w *ReqlogMetricsWorker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}

// logJob 输出与其余周期任务同一套结构化字段。永远不记录指标值本身。
func (w *ReqlogMetricsWorker) logJob(
	ctx context.Context, job *river.Job[ReqlogMetricsArgs],
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

	jobID, attempt, maxAttempts := int64(0), 1, reqlogMetricsMaxAttempts
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
		slog.String("module", reqlogMetricsModule),
		slog.String("environment", environment),
		slog.String("principal_id", reqlogMetricsPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", ReqlogMetricsJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
	}
	logger.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}
