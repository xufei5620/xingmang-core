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

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
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

	// DefaultNewAPIRequestTimeout 是**单次**上游 HTTP 读取的超时。
	//
	// 比一组读取的预算小是有意的：一轮同步要串行发很多次请求（五个读方法，
	// 其中渠道错误率还要逐渠道两次 COUNT），一个卡死的连接不该把整组的读取
	// 预算独吞。两层超时各管一段——这层管「一次请求」，外面那层管「一组」。
	//
	// 比 Sub2API 的 10s 略紧一点没有意义，取同一个值：两条采集链路的
	// 网络特征相同，两个不同的数字只会让人猜哪个才是「对的」。
	DefaultNewAPIRequestTimeout = 10 * time.Second

	// newapiStallAllowancePerGroup 是**每一组读取**允许有多少次请求走到满额
	// 单次超时、仍不判这一组失败。取 2：一次偶发卡顿 + 一次余量。
	//
	// 这是本条链路上唯一一个「拍」出来的数，其余全部由它与单次超时推导
	// （newapiGroupBudget / newapiReadBudget / newapiSyncJobTimeout）。
	newapiStallAllowancePerGroup = 2

	// newapiWriteReserve 是读完之后把观测（含历史样本，同一事务）写进库的余量。
	// 它只进 JobTimeout，不进读取预算——两段各管各的。
	newapiWriteReserve = 20 * time.Second
)

// newapiReadStepNames 是一轮同步的串行读取链，顺序即执行顺序。
//
// 三处同一份名单：整轮预算按它的长度推导（newapiReadBudget）、每组的
// deadline 按它切分、日志里的 read_step 取自它。往读取链里加第六组时预算
// 自动跟着加，不会出现「加了读取、预算还按五组算」的静默漂移。
// TestNewAPIReadStepsCoverEveryReadErrorField 让「加了读取忘了加步骤名」
// 在 CI 就红。
var newapiReadStepNames = []string{"stats", "orders", "channels", "usages", "payments"}

// newapiReadStepMetricKeys 给每组读取列出它喂的指标键，只用于日志定位。
//
// 是**复数**而不是 brief 里的单数 metric_key：orders 一组同时喂
// newapi.recharge.daily 与 newapi.subscription.daily，单数字段在这一组
// 必然说谎。
var newapiReadStepMetricKeys = map[string][]string{
	"stats":    {newapi.MetricUsersTotal},
	"orders":   {newapi.MetricRechargeDaily, newapi.MetricSubscriptionDaily},
	"channels": {newapi.MetricChannelsStatus},
	"usages":   {newapi.MetricModelsUsage},
	"payments": {newapi.MetricPaymentsDaily},
}

// newapiGroupBudget 是**一组**读取的独立预算。
//
// 旧版只有一个 20s 的整轮预算，五组共用：前两组慢一点就把它用光，排在后面
// 的三组还没开始就被判 unavailable——2026-09-08 那三条 NewAPI 失败恰好就是
// 排在最后的三组（见 docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md 二表
// 第一行）。给每组自己的 deadline，一组慢再也吃不到别组的份。
func newapiGroupBudget(perRequestTimeout time.Duration) time.Duration {
	if perRequestTimeout <= 0 {
		perRequestTimeout = DefaultNewAPIRequestTimeout
	}
	return perRequestTimeout * newapiStallAllowancePerGroup
}

// newapiReadBudget = 组数 × 每组预算，是「读上游」这一整段的墙钟上限。
//
// 组数由调用方从 newapiReadStepNames 数出来而不是写死 5。余量是**比例**
// 而不是一个绝对常数：单元测试得以把单次超时缩到毫秒级，跑出与生产同构的
// 「预算耗尽」形态，而不是真等 100 秒。steps <= 0 时按一组算（fail closed：
// 给零预算会让每一轮秒失败）。
func newapiReadBudget(perRequestTimeout time.Duration, steps int) time.Duration {
	if steps <= 0 {
		steps = 1
	}
	return newapiGroupBudget(perRequestTimeout) * time.Duration(steps)
}

// newapiSyncJobTimeout 覆盖 River 的默认 JobTimeout（1 分钟）。
//
// 不覆盖的话读预算抬过 ~40s 就等于没抬：River 会在读完之前掐掉整个 Work，
// 那一轮连「同步失败」都写不进库——看板从「正在失败」退化成「数据静静变旧」，
// 正是规格 §9.1 明令禁止的失败模式。先例见 financeCollectJobTimeout。
func newapiSyncJobTimeout(perRequestTimeout time.Duration) time.Duration {
	return newapiReadBudget(perRequestTimeout, len(newapiReadStepNames)) + newapiWriteReserve
}

// NewAPIMode 决定周期任务用哪个 ReadClient 实现。
type NewAPIMode string

const (
	// NewAPIModeFake 用 newapi.NewFake：不连真实上游，产出固定的演示数据。
	NewAPIModeFake NewAPIMode = "fake"
	// NewAPIModeReal 走真实只读客户端（XM-0038）；三个连接变量没配齐时
	// 每轮写一条 not_supported 的失败观测（见工厂注释）。
	NewAPIModeReal NewAPIMode = "real"
)

// ErrNewAPIRealClientUnavailable 是 real 模式**配置未就绪**时的确定性失败。
//
// XM-0038 之后真实客户端已经存在，但它需要三样东西才立得起来：只读端点、
// 目标 allowlist、以及一个能解析出只读凭据的 CredentialRef。三者缺一，
// real 模式就还是走不通——而「走不通」是一个**事实**，不是异常：与其让配置成
// real 的进程无声无息什么都不采，不如让它每个周期都往库里写一条明确的
// SyncFailed，看板照样看得见这条指标存在、且正在失败（规格 §9.1）。
//
// 分类保持 not_supported（而不是 internal）：它表达的是「本部署还不具备
// 真实读取能力」，与「配了但配错了」区分开——后者归 internal，运维一看
// error_code 就知道该去补配置还是去改配置。
var ErrNewAPIRealClientUnavailable = errors.New(
	"newapi 真实只读客户端未配置：缺少只读端点/allowlist/凭据引用")

// NewAPIClientFactory 按需构造一个只读客户端，并说出**本轮生效**的接入配置。
//
// 用工厂而不是直接持有一个 ReadClient：真实实现（XM-0038）需要在每轮同步时
// 解析 CredentialRef、按连接配置建传输层，那是有生命周期的东西，不该在进程
// 启动时构造一次然后一直握着——凭据会轮换，握着的连接不会知道。
//
// 第二个返回值是这次调用**实际用来建客户端**的那一份配置（XM-OPS-TRUTH）。
// 它必须由工厂带出来，不能让调用方事后再查一次库：动态工厂的读取缓存 TTL
// 是 30s（DefaultConnectorConfigCacheTTL），而一轮读取的预算已经超过它，
// 两次查询会跨过 TTL 边界拿到不同的行——那就是把「日志说的模式」和「实际读
// 的上游」重新劈成两个事实，正是本片要消灭的东西。
//
// 客户端构造失败时第二个返回值仍尽量说得出模式（工厂已经解析过了）；
// 连模式都定不下来时它是零值，Source 为 ModeSourceUnknown。
type NewAPIClientFactory func(ctx context.Context) (newapi.ReadClientV2, EffectiveConnectorConfig, error)

// NewAPIRealConfig 是 real 模式构造真实只读客户端所需的全部输入。
//
// 它刻意只装「连哪儿、用谁的凭据、多久超时」，不装任何凭据材料本身：
// 凭据只经 CredentialRef，明文由 SecretProvider 在**构造 Authorization 头
// 的那一瞬**才出现（ADR-014、宪法 7 条）。
type NewAPIRealConfig struct {
	// Endpoint 是上游只读端点，必须 https。
	Endpoint string
	// TargetAllowlist 是允许连接的主机精确清单；为空时一个请求都发不出去。
	TargetAllowlist []string
	// CredentialRef 形如 secret://<scope>/<name>。
	CredentialRef string
	// UserID 是旧版本 NewAPI 需要的 New-Api-User 头（管理员的用户 id）。
	//
	// **可选**：普查依据的上游源码里这个头已经不参与鉴权，但真实实例跑的
	// 补丁版未知，而旧版本上没有它就是 401。它不是凭据（用户 id 不是秘密），
	// 所以走普通配置项而不是 CredentialRef——缺它也不会让 real 模式立不起来。
	UserID string
	// Environment / InstanceID 进连接配置，同时是观测的 Source。
	Environment string
	InstanceID  string
	// Timeout 是单次 HTTP 请求的超时；零值由客户端回落到保守默认。
	Timeout time.Duration
	// Secrets 解析 CredentialRef。缺它等于没有凭据，real 模式走不通。
	Secrets secrets.SecretProvider
}

// missing 列出缺了哪几项配置。
//
// 返回**清单**而不是第一个错：运维一次就能把配置补齐，而不是补一个重启一次
// 再看下一个缺什么。字段名用环境变量名而不是 Go 字段名——看日志的人手里
// 拿的是 .env，不是源码。
//
// UserID 不在清单里：它是可选的（见该字段的注释）。
func (c NewAPIRealConfig) missing() []string {
	var out []string
	if strings.TrimSpace(c.Endpoint) == "" {
		out = append(out, "XM_NEWAPI_ENDPOINT")
	}
	if len(c.TargetAllowlist) == 0 {
		out = append(out, "XM_NEWAPI_TARGET_ALLOWLIST")
	}
	if strings.TrimSpace(c.CredentialRef) == "" {
		out = append(out, "XM_NEWAPI_CREDENTIAL_REF")
	}
	if c.Secrets == nil {
		// 装配问题而不是环境变量问题，但同样让 real 模式立不起来，
		// 所以并进同一份清单，用能让人找到装配点的名字。
		out = append(out, "secret provider")
	}
	return out
}

// NewNewAPIClientFactory 按模式构造只读客户端。
//
// real 模式在这里真正接活（XM-0038）：每轮同步现解析 CredentialRef、
// 现建传输层，而不是在进程启动时构造一次然后一直握着。
//
// 配置不全时返回**分类明确**的错误而不是 nil client：调用方按
// connector.KindOf 归类后写成 SyncFailed 观测，看板显示「同步失败」
// 并说得出失败原因，而不是数据静静停更（规格 §9.1）。
func NewNewAPIClientFactory(mode NewAPIMode, cfg NewAPIRealConfig) NewAPIClientFactory {
	// 这条路上 env 缺省**就是**生效配置（没有动态来源可读），如实标注 env。
	eff := EffectiveConnectorConfig{
		Platform:      ConnectorPlatformNewAPI,
		Mode:          string(mode),
		Source:        ModeSourceEnv,
		EndpointHost:  endpointHost(cfg.Endpoint),
		CredentialRef: cfg.CredentialRef,
		AllowlistSize: len(cfg.TargetAllowlist),
	}
	// 形参是 context.Context 而不是具名 ctx：真实客户端的构造不做任何 I/O，
	// 凭据在首次读取时才解析——那时用的是**请求的** ctx，取消才管用。
	return func(context.Context) (newapi.ReadClientV2, EffectiveConnectorConfig, error) {
		switch mode {
		case NewAPIModeFake:
			// 固定值即可：Fake 的意义是让上层不被真实凭据阻塞，不是模拟真实波动。
			// 随机化只会让「这条数据是假的」更难被看出来。
			return newapi.NewFake(newapi.FakeOptions{}), eff, nil
		case NewAPIModeReal:
			if missing := cfg.missing(); len(missing) > 0 {
				return nil, eff, connector.NewError(
					connector.KindNotSupported, "newapi.client.real",
					fmt.Errorf("缺少 %s: %w", strings.Join(missing, ", "), ErrNewAPIRealClientUnavailable))
			}
			// 配置**写错了**（endpoint 不是 https、主机不在自己的 allowlist 里…）
			// 与配置**没写**分开归类：前者由客户端归 internal——是我们自己的部署
			// 配置有问题，不是上游不支持；后者归 not_supported（见上面的 missing）。
			// 凭据解析失败两者都不是，它在首次读取时归 auth。
			var opts []newapi.Option
			if id := strings.TrimSpace(cfg.UserID); id != "" {
				opts = append(opts, newapi.WithUserID(id))
			}
			client, err := newapi.NewClient(connector.Config{
				ServiceInstanceID: cfg.InstanceID,
				Environment:       cfg.Environment,
				Endpoint:          cfg.Endpoint,
				CredentialRef:     cfg.CredentialRef,
				TargetAllowlist:   cfg.TargetAllowlist,
				Timeout:           cfg.Timeout,
			}, cfg.Secrets, opts...)
			if err != nil {
				// 显式回 nil 接口：直接把类型化的 nil 指针塞进接口会让
				// 调用方的 `client == nil` 判断恒假。
				return nil, eff, err
			}
			return client, eff, nil
		default:
			// 模式定不下来：连「按什么在跑」都答不出，如实标 unknown。
			return nil, EffectiveConnectorConfig{Platform: ConnectorPlatformNewAPI, Source: ModeSourceUnknown},
				connector.NewError(
					connector.KindInternal, "newapi.client.mode",
					fmt.Errorf("未知的 newapi 模式 %q", string(mode)))
		}
	}
}

// ParseNewAPIMode 解析模式，空串按 fake 处理。
//
// 默认 fake 而不是 real：真实只读凭据还没就绪，把默认设成 real 只会让每个
// 新环境一上来就满屏同步失败。
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
			ByState:  rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// NewAPISyncOptions 是 Worker 的构造参数。
type NewAPISyncOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID 会成为观测的 Source。
	InstanceID string
	// 这里**没有** Mode 字段（XM-OPS-TRUTH 删掉了它）。
	//
	// 它曾经「只用于日志标注」，装的是进程启动时从环境变量拷来的缺省值。
	// XM-CRED0 之后生效模式每轮从 core.connector_config 读，这个字段就再也
	// 不是它自称的那个东西了：2026-09-08 生产上明明跑着 real，日志里的
	// newapi_mode 却一直打 fake，排查因此走偏。本轮生效模式改由 NewClient
	// 一并带回（见 NewAPIClientFactory）。物理上删掉字段而不是加注释，
	// 是为了让下一个人根本没得可打。
	Store ObservationStore
	// NewClient 是 XM-0038 的注入点，也是单元测试注入 FakeOptions 的地方。
	NewClient NewAPIClientFactory
	// RequestTimeout 是**单次**上游请求的超时，同时是读取预算的推导基数
	// （见 newapiReadBudget）。零值回落 DefaultNewAPIRequestTimeout。
	//
	// 它与 NewAPIRealConfig.Timeout 是同一个数，由 client.go 从同一个
	// Config.NewAPIRequestTimeout 分发到两处：客户端拿它约束一次请求，
	// 本任务拿它推导一组与一轮的墙钟上限。
	RequestTimeout time.Duration
	// Now 可注入固定时钟；默认 time.Now。
	Now func() time.Time
	// ExpectedInterval is the effective cadence of this writer.  It is copied
	// into every raw sample; zero keeps legacy/manual coverage unknown.
	ExpectedInterval time.Duration
}

// NewAPISyncWorker 周期性读取 NewAPI 只读契约并把结果写进运营指标表。
//
// 它是 Connector 与看板之间那条接缝的消费端：Connector 不直接写库
// （见 newapi.ToObservations 的注释），由本任务负责落库。
type NewAPISyncWorker struct {
	river.WorkerDefaults[NewAPISyncArgs]

	logger           *slog.Logger
	environment      string
	instanceID       string
	store            ObservationStore
	newClient        NewAPIClientFactory
	requestTimeout   time.Duration
	now              func() time.Time
	expectedInterval time.Duration
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
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultNewAPIRequestTimeout
	}
	return &NewAPISyncWorker{
		logger:           opts.Logger,
		environment:      opts.Environment,
		instanceID:       opts.InstanceID,
		store:            opts.Store,
		newClient:        opts.NewClient,
		requestTimeout:   opts.RequestTimeout,
		now:              opts.Now,
		expectedInterval: opts.ExpectedInterval,
	}
}

// Timeout 放宽本任务的执行期限，理由见 newapiSyncJobTimeout。
//
// **必须**跟着读取预算一起改：River 的默认 JobTimeout 是 1 分钟，读预算一旦
// 抬过它，没有这个方法的结果是那一轮连「同步失败」都写不进库。
func (w *NewAPISyncWorker) Timeout(*river.Job[NewAPISyncArgs]) time.Duration {
	return newapiSyncJobTimeout(w.requestTimeout)
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

	reads, readErrs, effective := w.read(ctx, job, day)

	// 上下文被取消说明是本进程在关机，不是上游出问题。把它记成 failed 会让
	// 看板把一次正常重启显示成同步故障——那是**假的**失败信号，比没有信号更糟。
	if err := ctx.Err(); err != nil {
		return err
	}

	// 先按成功路径把五条观测算出来，再把失败分组的那几条替换掉。
	// 这样指标键、来源、新鲜度阈值只有 newapi.ToObservations 一个来源，
	// 失败路径不会长出第二套指标定义。
	channels := newapi.LegacyChannelStatuses(reads.directory)
	observations := newapi.ToObservations(now, w.instanceID, w.environment,
		reads.stats, reads.orders, channels, reads.usages)
	for i := range observations {
		if observations[i].MetricKey == newapi.MetricChannelsStatus {
			observations[i] = newapi.ToChannelDirectoryObservation(
				now, w.instanceID, w.environment, reads.directory)
		}
	}
	observations = append(observations,
		newapi.ToPaymentsDailyObservation(now, w.instanceID, w.environment, reads.payments))

	failed := 0
	for i := range observations {
		observation := observations[i]
		if err := readErrs.forMetric(observation.MetricKey); err != nil {
			observation = w.failureObservation(ctx, observation, now, connector.KindOf(err))
			failed++
		}
		annotateRollupMetadata(&observation, w.expectedInterval)
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
		// newapi_mode 是**本轮生效**模式（工厂这一轮从 core.connector_config
		// 解析出来、并且真的拿去建客户端的那一个），不是 XM_NEWAPI_MODE。
		// 缺省值只出现在 worker_started 的 newapi_mode_default 里。
		// 字段名刻意不变：既有的 grep、runbook 与验收脚本指向的就是这个名字，
		// 让它开始说真话比再造一个名字好。
		slog.String("newapi_mode", effective.Mode),
		slog.String("newapi_mode_source", effective.Source),
		slog.Int("newapi_config_version", effective.Version),
		slog.String("source", w.instanceID),
		slog.String("business_day", day),
		slog.Int("metrics_total", len(observations)),
		slog.Int("metrics_failed", failed),
	)
	return nil
}

// newapiReads 是一轮同步读到的五组数据。
type newapiReads struct {
	stats     newapi.UserStats
	orders    newapi.OrderSummary
	directory newapi.ChannelDirectorySnapshot
	usages    []newapi.ModelUsage
	// payments 是 XM-PAY0 的 DailyPaymentSummary 读取结果，独立成组的理由
	// 见 newapiReadErrors.payments。
	payments newapi.DailyPaymentSummary
}

// newapiReadErrors 记录五组读取各自的结果。
//
// 分组保留而不是「有一个错就整轮算失败」：模型用量超时不该把已经读到的
// 渠道状态一并抹成失败——那会让看板丢掉本来拿得到的真话。
//
// 是五组而不是照抄 sub2api 的四组：分组数跟着**读取次数**走，不跟着别的
// Connector 走。NewAPI 现在有五个数据读取方法，合并任意两组都会让其中一组的
// 失败去污染另一组本来成功的指标。
type newapiReadErrors struct {
	stats    error
	orders   error
	channels error
	usages   error
	payments error
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
	case newapi.MetricPaymentsDaily:
		return e.payments
	default:
		// 契约将来加了新指标却漏登记在上面：fail closed，任一读取失败就把它
		// 也判为失败，绝不让一条来源不明的指标以「成功」姿态进看板。
		// TestNewAPISyncMetricMappingIsExhaustive 会让这种遗漏在 CI 就暴露。
		return e.first()
	}
}

// first 返回第一个非空错误，用于给整轮同步一个代表性的错误分类。
func (e newapiReadErrors) first() error {
	for _, err := range []error{e.stats, e.orders, e.channels, e.usages, e.payments} {
		if err != nil {
			return err
		}
	}
	return nil
}

// errPaymentsCapabilityUnavailable 是本轮的客户端不满足
// newapi.PaymentsReadClient 时的确定性失败（例如测试用的部分故障客户端只
// 嵌入了 ReadClientV2）。归 not_supported：这不是一次读取失败，是"这个
// 客户端实现在这一轮里就没有这项能力"。
var errNewAPIPaymentsCapabilityUnavailable = errors.New("newapi: client does not implement PaymentsReadClient")

// read 读取五组数据。客户端构造失败时五组一起归到同一个失败分类。
//
// 第三个返回值是本轮生效的接入配置，由工厂带出来（见 NewAPIClientFactory）。
func (w *NewAPISyncWorker) read(
	ctx context.Context, job *river.Job[NewAPISyncArgs], day string,
) (newapiReads, newapiReadErrors, EffectiveConnectorConfig) {
	// 读上游单独限时，留出时间把失败写进库（见 newapiReadBudget 注释）。
	readCtx, cancel := context.WithTimeout(ctx, newapiReadBudget(w.requestTimeout, len(newapiReadStepNames)))
	defer cancel()

	client, effective, err := w.newClient(readCtx)
	if err != nil {
		// 配置未就绪的 real 模式走这一支：全部指标写成 not_supported
		// 的失败观测，而不是静静地什么都不采。
		return newapiReads{}, newapiReadErrors{
			stats: err, orders: err, channels: err, usages: err, payments: err,
		}, effective
	}

	var reads newapiReads
	var errs newapiReadErrors
	// 读取串行：真实客户端在一轮里共用同一个 quota_per_unit 缓存与
	// 同一次凭据解析，并发跑只会让第一轮多打几次 /api/status 与 Provider，
	// 换不到什么——这条链路的瓶颈是上游的 COUNT，不是往返次数。
	//
	// 每一组各开自己的 deadline（newapiGroupBudget），而不是五组共用一个：
	// 共用那份预算正是 2026-09-08 的失败形态——前两组慢一点，排在后面的三组
	// 还没开始就被判 unavailable。整轮 readCtx 仍是上限，两层都在。
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"stats", func(c context.Context) error {
			reads.stats, errs.stats = client.UserStats(c)
			return errs.stats
		}},
		{"orders", func(c context.Context) error {
			reads.orders, errs.orders = client.DailyOrders(c, day)
			return errs.orders
		}},
		{"channels", func(c context.Context) error {
			reads.directory, errs.channels = client.ChannelDirectory(c)
			return errs.channels
		}},
		{"usages", func(c context.Context) error {
			reads.usages, errs.usages = client.ModelUsages(c, day)
			return errs.usages
		}},
		// PaymentsReadClient 是叠加在 ReadClientV2 之上的独立切片（XM-PAY0）；
		// 生产装配（NewNewAPIClientFactory）返回的客户端始终满足它，断言只在
		// 测试用的窄接口客户端上才会落空，见 errNewAPIPaymentsCapabilityUnavailable。
		{"payments", func(c context.Context) error {
			pc, ok := client.(newapi.PaymentsReadClient)
			if !ok {
				errs.payments = connector.NewError(connector.KindNotSupported,
					"newapi.payments.daily_read", errNewAPIPaymentsCapabilityUnavailable)
				return errs.payments
			}
			reads.payments, errs.payments = pc.DailyPaymentSummary(c, day)
			return errs.payments
		}},
	}
	// 步骤名单（newapiReadStepNames）与这条链是同一份事实的两半：预算按前者
	// 的长度算、执行按后者走。两边漂开时预算会按旧组数默默算，不报错——
	// TestNewAPIReadChainMatchesDeclaredSteps 从跑出来的 upstream_read 日志
	// 反过来钉住这份名单，TestNewAPIReadStepsCoverEveryReadErrorField 再把它
	// 钉到 newapiReadErrors 的字段数上。
	groupBudget := newapiGroupBudget(w.requestTimeout)
	for i, step := range steps {
		remaining := time.Duration(0)
		if deadline, ok := readCtx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		groupCtx, cancelGroup := context.WithTimeout(readCtx, groupBudget)
		started := time.Now()
		stepErr := step.run(groupCtx)
		elapsed := time.Since(started)
		cancelGroup()
		w.logUpstreamRead(ctx, job, i, step.name, elapsed, remaining, groupBudget, stepErr)
	}
	return reads, errs, effective
}

// logUpstreamRead 为每一组上游读取打一条耗时行。
//
// 级别是 Info 而不是 Debug，这不是偏好：worker 的 slog handler 是
// slog.NewJSONHandler(os.Stdout, nil)——HandlerOptions 为 nil，级别锁死在
// Info，没有任何环境变量能调。打成 Debug 等于一条都不落盘，下次出同样的问题
// 还得重新发版加日志，正是 2026-09-08「没有生产的逐次耗时数据」那个坑。
//
// 也**不**只在失败时打：区分「整轮预算耗尽」与「上游那几个接口坏了」靠的正是
// 「成功但很慢」那几行——只打失败会把关键证据丢掉。
//
// round_remaining_ms 是把两种解释一刀切开的那个字段（本组**开始前**整轮预算
// 还剩多少）：预算耗尽预测「靠后的组 round_remaining_ms 趋近 0、elapsed_ms
// 也趋近 0」，上游故障预测「每组都有实际往返、余量还宽裕」。只打 elapsed_ms
// 区分不掉「快速失败」与「还没轮到」。
func (w *NewAPISyncWorker) logUpstreamRead(
	ctx context.Context, job *river.Job[NewAPISyncArgs],
	index int, step string, elapsed, roundRemaining, groupBudget time.Duration, err error,
) {
	logger := w.logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment := w.environment
	if strings.TrimSpace(environment) == "" {
		environment = "unknown"
	}
	jobID := int64(0)
	if job != nil && job.JobRow != nil {
		jobID = job.ID
	}
	status, errorCode := "ok", ""
	if err != nil {
		status = "failed"
		errorCode = string(connector.KindOf(err))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "upstream_read",
		slog.String("event", "upstream_read"),
		slog.String("module", newapiSyncModule),
		slog.String("environment", environment),
		slog.String("principal_id", newapiSyncPrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", NewAPISyncJobKind),
		slog.String("read_step", step),
		slog.Int("sequence", index+1),
		slog.String("metric_keys", strings.Join(newapiReadStepMetricKeys[step], ",")),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
		slog.String("status", status),
		slog.String("error_code", errorCode),
		slog.Int64("group_budget_ms", groupBudget.Milliseconds()),
		slog.Int64("round_remaining_ms", roundRemaining.Milliseconds()),
	)
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
