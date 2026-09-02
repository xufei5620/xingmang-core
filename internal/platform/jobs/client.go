package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

const (
	DefaultHeartbeatInterval = time.Minute
	DefaultMaxWorkers        = 1
	DefaultHeartbeatAttempts = 0
)

// Config controls the worker process without exposing River's whole config
// surface to callers. It keeps the baseline intentionally small and explicit.
type Config struct {
	Logger      *slog.Logger
	Environment string
	// AuditArchive is deliberately not consumed by NewClient's periodic
	// registration path.  Until R2-10 and the DB role split are proven, archive
	// execution is an explicit manual-only seam (see audit_archive_manual.go).
	AuditArchive AuditArchiveConfig
	// WorkerClusterID and RiverSchema are non-secret identity inputs used by
	// the R210 effective job manifest. They are intentionally not inferred or
	// defaulted here: a deployment must name its ownership domain explicitly.
	WorkerClusterID     string
	RiverSchema         string
	HeartbeatInterval   time.Duration
	HeartbeatRunOnStart bool
	// HeartbeatRunID is reserved for isolated integration tests. Production
	// clients must leave it empty so all instances share one unique heartbeat.
	HeartbeatRunID string
	// HeartbeatFailures is a bounded failure-injection switch for development
	// and integration tests; production clients must leave it at zero.
	HeartbeatFailures int
	MaxWorkers        int

	// Sub2APISyncEnabled 决定是否注册 Sub2API 周期同步任务（XM-0022）。
	//
	// 零值 false 是有意的：用 Config 字面量构造的调用方（集成测试等）必须
	// 显式打开，和 Environment 一样不给「能跑生产」的隐式默认。
	// DefaultConfig 把它打开——正常进程走 DefaultConfig。
	// 它同时是这条采集链路的停用开关（宪法 26 条）。
	Sub2APISyncEnabled bool
	// Sub2APISyncInterval 是同步周期，默认 DefaultSub2APISyncInterval。
	Sub2APISyncInterval time.Duration
	// Sub2APISyncRunOnStart 让进程起来就先采一次，而不是干等一个周期。
	Sub2APISyncRunOnStart bool
	// Sub2APISyncRunID 仅供集成测试隔离，生产必须留空——留空才让所有副本
	// 共享同一条唯一性记录，同一个周期只采一次。
	Sub2APISyncRunID string
	// Sub2APIMode 选择 fake / real 客户端；空值按 fake 处理。
	Sub2APIMode Sub2APIMode
	// Sub2APIInstanceID 是观测的 Source，默认 DefaultSub2APIInstanceID。
	Sub2APIInstanceID string
	// Sub2APICredentialRef 是只读凭据的引用（secret://<scope>/<name>）。
	// 本层只校验引用的**形状**，不解析出任何明文；明文由 Sub2APISecrets
	// 在客户端构造请求头的那一瞬才出现（ADR-014、宪法 7 条）。
	Sub2APICredentialRef string
	// Sub2APIEndpoint 是上游只读端点（必须 https）。real 模式必填。
	Sub2APIEndpoint string
	// Sub2APITargetAllowlist 是允许连接的主机精确清单（ADR-004）。real 模式必填。
	// 留空不是「放行一切」而是「一个请求都发不出去」——护栏 fail closed。
	Sub2APITargetAllowlist []string
	// Sub2APIRequestTimeout 是单次上游 HTTP 请求的超时，
	// 零值回落到 DefaultSub2APIRequestTimeout。
	Sub2APIRequestTimeout time.Duration
	// Sub2APISecrets 解析 Sub2APICredentialRef。装配在进程入口（cmd/），
	// 而不是在这里现造：Provider 的选择（env/SOPS/Vault）是部署决定，
	// 不是任务决定（ADR-014）。fake 模式用不到它。
	Sub2APISecrets secrets.SecretProvider

	// NewAPISyncEnabled 决定是否注册 NewAPI 周期同步任务（XM-0035）。
	//
	// 零值 false 与 Sub2APISyncEnabled 同一条纪律：用 Config 字面量构造的
	// 调用方（集成测试等）必须显式打开。DefaultConfig 把它打开。
	// 它同时是这条采集链路的停用开关（宪法 26 条）。
	NewAPISyncEnabled bool
	// NewAPISyncInterval 是同步周期，默认 DefaultNewAPISyncInterval。
	NewAPISyncInterval time.Duration
	// NewAPISyncRunOnStart 让进程起来就先采一次，而不是干等一个周期。
	NewAPISyncRunOnStart bool
	// NewAPISyncRunID 仅供集成测试隔离，生产必须留空——留空才让所有副本
	// 共享同一条唯一性记录，同一个周期只采一次。
	NewAPISyncRunID string
	// NewAPIMode 选择 fake / real 客户端；空值按 fake 处理。
	//
	// real 在配置不全时失败，失败会作为 not_supported 的观测落库而不是让
	// 进程起不来——见 NewNewAPIClientFactory。
	NewAPIMode NewAPIMode
	// NewAPIInstanceID 是观测的 Source，默认 DefaultNewAPIInstanceID。
	NewAPIInstanceID string
	// NewAPICredentialRef 是只读凭据的引用（secret://<scope>/<name>）。
	// 本层只校验引用的**形状**，不解析出任何明文；明文由 NewAPISecrets
	// 在客户端构造 Authorization 头的那一瞬才出现（ADR-014、宪法 7 条）。
	NewAPICredentialRef string
	// NewAPIEndpoint 是上游只读端点（必须 https）。real 模式必填。
	NewAPIEndpoint string
	// NewAPITargetAllowlist 是允许连接的主机精确清单（ADR-004）。real 模式必填。
	// 留空不是「放行一切」而是「一个请求都发不出去」——护栏 fail closed。
	NewAPITargetAllowlist []string
	// NewAPIUserID 是旧版本 NewAPI 需要的 New-Api-User 头（管理员用户 id）。
	// **可选**，不是凭据；新版本上游会忽略它。见 NewAPIRealConfig.UserID。
	NewAPIUserID string
	// NewAPIRequestTimeout 是单次上游 HTTP 请求的超时，
	// 零值回落到 DefaultNewAPIRequestTimeout。
	NewAPIRequestTimeout time.Duration
	// NewAPISecrets 解析 NewAPICredentialRef。装配在进程入口（cmd/），
	// 而不是在这里现造：Provider 的选择（env/SOPS/Vault）是部署决定，
	// 不是任务决定（ADR-014）。fake 模式用不到它。
	NewAPISecrets secrets.SecretProvider

	// FinanceCollectEnabled 决定是否注册成本采集任务（XM-0037b，设计稿 §8.1）。
	//
	// 零值 false 与前两条同一条纪律：用 Config 字面量构造的调用方必须显式
	// 打开。DefaultConfig 把它打开。它同时是这条采集链路的停用开关
	// （宪法 26 条）——关掉之后台账不会假装有数：今日行停止刷新，
	// finance.profit.daily 的 observed_at 不再前进，新鲜度自然降级。
	FinanceCollectEnabled bool
	// FinanceCollectInterval 是采集周期，默认 DefaultFinanceCollectInterval
	// （§12 拍板：可配，默认 5min）。
	FinanceCollectInterval time.Duration
	// FinanceCollectRunOnStart 让进程起来就先采一次，而不是干等一个周期。
	FinanceCollectRunOnStart bool
	// FinanceCollectRunID 仅供集成测试隔离，生产必须留空。
	FinanceCollectRunID string
	// FinanceCollectMode 选择 fake / real 客户端；空值按 fake 处理。
	FinanceCollectMode FinanceCollectMode
	// FinanceCollectInstanceID 是观测的 Source 与台账行的 source，
	// 默认 DefaultFinanceCollectInstanceID。
	FinanceCollectInstanceID string
	// FinanceCollectTargetAllowlist 是允许连接的上游主机精确清单（ADR-004）。
	// real 模式必填；留空不是「放行一切」而是「一个请求都发不出去」。
	//
	// endpoint 与凭据引用**不在这里**：那两样逐账号不同，来自成本登记簿
	// （§2.1）。进程级配置里再放一份只会让两处漂开。
	FinanceCollectTargetAllowlist []string
	// FinanceCollectRequestTimeout 是单次上游 HTTP 请求的超时，
	// 零值回落到 DefaultFinanceCollectRequestTimeout。
	FinanceCollectRequestTimeout time.Duration
	// FinanceCollectSecrets 解析登记簿里的账号级与每令牌 CredentialRef。
	// 装配在进程入口（cmd/），而不是在这里现造：Provider 的选择
	// （env/SOPS/Vault）是部署决定，不是任务决定（ADR-014）。fake 模式用不到它。
	FinanceCollectSecrets secrets.SecretProvider
	// FinanceCollectSecretProvider 选择动态凭据来源（env 或 file）。空值表示
	// 未装配，real 模式会按既有契约把成本观测记为 not_supported，而不拖垮 worker。
	FinanceCollectSecretProvider string
	// FinanceCollectSecretRoot 是 file provider 的固定绝对根目录；不保存明文。
	FinanceCollectSecretRoot string
	// FinanceCollectSecretScopes 是动态 provider 的精确 scope 白名单，防止登记簿
	// 中的未知 scope 变成任意环境变量/文件读取。
	FinanceCollectSecretScopes []string
	// FinanceNewAPIRevenue 是 newapi 收入侧的只读数据库通道（XM-0044，§3.2）。
	//
	// **nil 是合法且是默认**：没配 XM_NEWAPI_REVENUE_DSN 时收入继续
	// not_supported、台账写 NULL——那是「这条链路还没接通」的如实表达。
	//
	// 与 FinanceCollectSecrets 同样装配在进程入口：连接池连的是别人家的生产库，
	// 必须按进程持有（含 Close），不能让每轮采集各开一个。
	FinanceNewAPIRevenue metering.RevenueSource

	// RetentionEnabled 决定是否注册保留期清理任务（XM-R012）。
	//
	// 与其他采集开关同样的零值纪律：用 Config 字面量构造的调用方必须显式
	// 打开，DefaultConfig 把它打开。它同时是这条链路的停用开关（宪法 26 条）
	// ——上线初期想先观察表增长时能关掉，而不必改代码。
	RetentionEnabled bool
	// RetentionInterval 是清理周期，默认 DefaultRetentionInterval（24h）。
	RetentionInterval time.Duration
	// RetentionRunOnStart 让进程起来就先清一次。
	//
	// 默认 **false**（与采集任务相反）：清理是删数据，进程一起来就删让人没有
	// 机会先看一眼配置对不对。等第一个周期到，运维有一天时间发现配错了。
	RetentionRunOnStart bool
	// RetentionRunID 仅供集成测试隔离，生产必须留空。
	RetentionRunID string
	// MetricSampleRetentionDays / AlertRetentionDays 是两类保留天数，
	// 零值回落到各自的默认值（90 / 180）。
	MetricSampleRetentionDays int
	AlertRetentionDays        int

	// AlertEvaluateEnabled 决定是否注册告警评估任务（XM-0033）。
	//
	// 与 Sub2APISyncEnabled 同样的零值纪律：用 Config 字面量构造的调用方
	// 必须显式打开，DefaultConfig 把它打开。它同时是这条告警链路的停用
	// 开关（宪法 26 条）——关掉之后告警不会假装正常，页面上的告警会停在
	// 最后一次评估的状态，last_seen_at 不再前进。
	AlertEvaluateEnabled bool
	// AlertEvaluateInterval 是评估周期，默认 DefaultAlertEvaluateInterval。
	AlertEvaluateInterval time.Duration
	// AlertEvaluateRunOnStart 让进程起来就先评估一次，而不是干等一个周期。
	AlertEvaluateRunOnStart bool
	// AlertEvaluateRunID 仅供集成测试隔离，生产必须留空——留空才让所有副本
	// 共享同一条唯一性记录，同一个周期只评估一次、只投递一次。
	AlertEvaluateRunID string
	// AlertTelegramBotRef 是 Telegram Bot Token 的引用（secret://<scope>/<name>）。
	// 本层只校验引用的**形状**，不解析出任何明文；明文由 AlertSecrets 在
	// 构造 Bot API URL 的那一瞬才出现（ADR-014、宪法 7 条）。
	AlertTelegramBotRef string
	// AlertTelegramChatID 是投递目标会话。它不是秘密。
	AlertTelegramChatID string
	// AlertWebhookURL 是自建投递端点，必须 https。
	AlertWebhookURL string
	// AlertSecrets 解析 AlertTelegramBotRef。装配在进程入口（cmd/）。
	AlertSecrets secrets.SecretProvider
	// AlertWeComWebhookRef 是企业微信群机器人 Webhook 地址（含 key）的引用
	// （secret://<scope>/<name>，XM-ALERT-WECOM）。与 AlertWebhookURL 不同：
	// 这里**整个地址就是凭据**（key 作为查询参数嵌在地址里），不接受静态
	// 值，只能经 CredentialRef（PROJECT-CONSTITUTION 第 7 条）。
	AlertWeComWebhookRef string
	// AlertWeComSecrets 解析 AlertWeComWebhookRef。装配走文件优先链
	// （XM-CRED0，见 cmd/platform-worker 的 connectorSecretsChain），
	// 与 AlertSecrets（Telegram 专用、纯 env）是两个独立 Provider。
	AlertWeComSecrets secrets.SecretProvider
	// AlertBalanceThresholdMinorUnits 是渠道余额告警阈值（最小货币单位，
	// 宪法 13 条：金额禁止 float）。零值回落到 alerts 包的默认值。
	AlertBalanceThresholdMinorUnits int64
	// AlertRunwayThresholds 是无 provider 时的静态兼容档（XM-0049）。
	// 生产装配必须同时提供 RunwayThresholdProvider；评估器每轮从
	// finance.runway_threshold_config 读取与 platform-api 相同的 DB revision。
	// 该字段只供单测/迁移过渡，不能被理解为 DB 缺行时的运行时 fallback。
	AlertRunwayThresholds finance.RunwayThresholds
	// RunwayThresholdProvider 非空时，告警评估每轮从数据库读取一次完整
	// revision 快照；仅测试/迁移过渡可使用上面的静态字段。
	RunwayThresholdProvider finance.RunwayThresholdProvider

	// CPASyncEnabled 决定是否注册 CPA 周期同步任务（XM-CPA0）。
	//
	// 与其他采集开关不同，它的零值 false 就是 DefaultConfig 的默认值——
	// CPA 没有 fake 模式，file 模式需要一个宿主机只读挂载才有意义，所以
	// 「默认就采」在这里不成立：一个没配 XM_CPA_MODE 的环境应当保持关闭，
	// 而不是每轮同步都失败。cmd/platform-worker/config.go 按 XM_CPA_MODE
	// 是否为 file 来决定要不要打开它。它同时是这条链路的停用开关（宪法 26 条）。
	CPASyncEnabled bool
	// CPASyncInterval 是同步周期，默认 DefaultCPASyncInterval。
	CPASyncInterval time.Duration
	// CPASyncRunOnStart 让进程起来就先采一次，而不是干等一个周期。
	CPASyncRunOnStart bool
	// CPASyncRunID 仅供集成测试隔离，生产必须留空。
	CPASyncRunID string
	// CPAMode 选择 off / file；空值按 off 处理（见 ParseCPAMode）。
	CPAMode CPAMode
	// CPAInstanceID 是观测的 Source，默认 DefaultCPAInstanceID。
	CPAInstanceID string
	// CPADataDir 是只读挂载目录（含 usage.sqlite 及其 -wal/-shm），
	// file 模式必填。不经 CredentialRef：这是一条挂载路径，不是向第三方
	// 系统认证的凭据（与 connectors/reqlog.FileConfig.DataDir 同一条纪律，
	// 见该类型的注释）。
	CPADataDir string
	// CPAFileName 覆盖 DataDir 内的数据库文件名；空值回落到连接器自己的默认值
	// （"usage.sqlite"）。
	CPAFileName string

	// ConnectorConfigs 是接入配置（模式 / 端点 / allowlist / 凭据引用）的
	// 运行时来源：core.connector_config（XM-CRED0）。
	//
	// nil = 只按环境变量（与本片之前逐字相同）。非 nil 时 Sub2API / NewAPI
	// 的客户端工厂**每轮同步**都重新读取：行存在即以行里非空的字段为准，
	// 上面那些 Sub2API* / NewAPI* 字段只作缺省；库读不到则本轮回落到缺省。
	// 装配在进程入口（cmd/），因为它需要连接池。
	ConnectorConfigs ConnectorConfigSource
	// SecretRoot 是文件 SecretProvider 的根目录（XM_SECRET_ROOT），
	// 管理后台写入的凭据文件按 <root>/<scope>/<name> 落在这里。
	// 本层只把路径带进启动日志，不读取任何文件内容。
	SecretRoot string

	// ReqlogMetricsMode 决定「请求量/成功率」聚合任务是否启用
	// （XM-REQLOG-METRICS）。off 不注册周期任务（不写观测，也不写
	// not_supported）；file 时从 ReqlogMetricsDataDir 聚合 index.jsonl。
	// 与 platform-api 共享同一个环境变量名 XM_REQLOG_MODE，但 worker 只认
	// 得出这两档，其余值（fake/real，服务另一条链路）在 configFromEnv
	// 阶段就已经被 ParseReqlogMetricsMode 折成 off。
	ReqlogMetricsMode ReqlogMetricsMode
	// ReqlogMetricsModeRecognized 记录原始配置值是否被 ParseReqlogMetricsMode
	// 认识；false 时 NewClient 会 warn 一次，而不是让一个只服务于请求详情
	// 链路的合法值（fake/real）悄悄退化成 off 却没有任何痕迹。
	ReqlogMetricsModeRecognized bool
	// ReqlogMetricsDataDir 是记录代理数据目录在 worker 容器内的挂载路径，
	// 与 platform-api 的 XM_REQLOG_DATA_DIR 同一份路径、同一个默认值。
	ReqlogMetricsDataDir string
	// ReqlogMetricsInterval 是采集周期，默认 DefaultReqlogMetricsInterval。
	ReqlogMetricsInterval time.Duration
	// ReqlogMetricsRunOnStart 让进程起来就先聚合一次，而不是干等一个周期。
	ReqlogMetricsRunOnStart bool
	// ReqlogMetricsRunID 仅供集成测试隔离，生产必须留空——留空才让所有副本
	// 共享同一条唯一性记录，同一个周期只聚合一次。
	ReqlogMetricsRunID string
	// ConnectorProbeEnabled controls whether the connector health probe
	// (XM-OPS0) is registered. Same zero-value discipline as the other
	// periodic jobs: literal-constructed callers must opt in explicitly;
	// DefaultConfig turns it on. It doubles as this job's off switch
	// (constitution clause 26).
	ConnectorProbeEnabled bool
	// ConnectorProbeInterval is the probe cadence, default
	// DefaultConnectorProbeInterval (5m).
	ConnectorProbeInterval time.Duration
	// ConnectorProbeRunOnStart makes the process probe once on boot instead
	// of waiting a full cycle, so the ops overview page has data
	// immediately.
	ConnectorProbeRunOnStart bool
	// ConnectorProbeRunID is reserved for integration-test isolation;
	// production must leave it empty (same rule as every other *RunID
	// field on this Config).
	ConnectorProbeRunID string
	// AssuranceProbeGlobalEnabled is the ADR-019 global Kill Switch
	// (XM_ASSURE_PROBE_ENABLED). Off by default: real-mode probing needs
	// this **and** the per-platform core.connector_config.probe_enabled
	// both on. This process (platform-worker) parses the same env var
	// independently of cmd/platform-api — the two processes cannot see
	// each other's memory, same precedent as XM_CONNECTOR_PROBE_ENABLED.
	AssuranceProbeGlobalEnabled bool
	// AssuranceProbeDailyBudget overrides
	// assurance.DefaultDailyBudgetPerPlatform when > 0.
	AssuranceProbeDailyBudget int
	// AssuranceProbeSecrets resolves a platform's probe_credential_ref
	// (real mode only). Unlike Sub2APISecrets/NewAPISecrets, this cannot be
	// scoped to one fixed ref at process start — the ref lives per-platform
	// in core.connector_config and is only known at Job execution time —
	// so this is a generic "resolve whatever secret:// ref you're handed,
	// rooted at XM_SECRET_ROOT" provider.
	AssuranceProbeSecrets secrets.SecretProvider
}

// DefaultConfig returns the safe local-development baseline.
func DefaultConfig() Config {
	return Config{
		// Environment is intentionally empty: callers must declare it rather
		// than inheriting a production-capable default.
		Environment:         "",
		AuditArchive:        DefaultAuditArchiveConfig(),
		HeartbeatInterval:   DefaultHeartbeatInterval,
		HeartbeatRunOnStart: true,
		HeartbeatFailures:   DefaultHeartbeatAttempts,
		MaxWorkers:          DefaultMaxWorkers,
		// 看板要的是「持续更新的新鲜度数据」，所以正常进程默认就采；
		// 默认走 fake，因为真实只读账号还没就绪（XM-0017）。
		Sub2APISyncEnabled:    true,
		Sub2APISyncInterval:   DefaultSub2APISyncInterval,
		Sub2APISyncRunOnStart: true,
		Sub2APIMode:           Sub2APIModeFake,
		Sub2APIInstanceID:     DefaultSub2APIInstanceID,
		Sub2APIRequestTimeout: DefaultSub2APIRequestTimeout,
		// NewAPI 同理（XM-0035）：默认就采、默认走 fake，因为真实只读凭据
		// 还没就绪（XM-0038 交付了客户端，凭据由用户自配）。看板要的是
		// 「持续更新的新鲜度数据」——一条默认关闭的采集链路会让 NewAPI
		// 平台页一直空着，而空着与「采集失败」在页面上长得一模一样。
		NewAPISyncEnabled:    true,
		NewAPISyncInterval:   DefaultNewAPISyncInterval,
		NewAPISyncRunOnStart: true,
		NewAPIMode:           NewAPIModeFake,
		NewAPIInstanceID:     DefaultNewAPIInstanceID,
		NewAPIRequestTimeout: DefaultNewAPIRequestTimeout,
		// 成本采集同理（XM-0037b）：默认就采、默认走 fake。登记簿里没有
		// 计量型账号时它每轮什么都不写（AccountsTotal=0），代价只有一条
		// info 日志；而一条默认关闭的采集链路会让毛利看板一直空着，
		// 空着与「采集失败」在页面上长得一模一样。
		FinanceCollectEnabled:        true,
		FinanceCollectInterval:       DefaultFinanceCollectInterval,
		FinanceCollectRunOnStart:     true,
		FinanceCollectMode:           FinanceCollectModeFake,
		FinanceCollectInstanceID:     DefaultFinanceCollectInstanceID,
		FinanceCollectRequestTimeout: DefaultFinanceCollectRequestTimeout,
		// 告警默认就跑：Foundation-A 的退出条件之一是「能触发一条真实告警」
		// （规格 §22.2），而一个默认关闭的告警系统在需要它的那天多半还是关的。
		// 没配投递渠道时它照常评估落库，只是每轮打一条 warn 说没投出去。
		// 保留期清理默认开启（XM-R012）：一张没有清理路径的追加表迟早会成为
		// 运维事故，而默认关掉等于把这件事留给「以后有人想起来」。
		// RunOnStart 保持 false——清理是删数据，见那个字段的注释。
		RetentionEnabled:          true,
		RetentionInterval:         DefaultRetentionInterval,
		MetricSampleRetentionDays: DefaultMetricSampleRetentionDays,
		AlertRetentionDays:        DefaultAlertRetentionDays,

		AlertEvaluateEnabled:            true,
		AlertEvaluateInterval:           DefaultAlertEvaluateInterval,
		AlertEvaluateRunOnStart:         true,
		AlertBalanceThresholdMinorUnits: alerts.DefaultBalanceThresholdMinorUnits,
		AlertRunwayThresholds:           finance.DefaultRunwayThresholds(),

		// 请求量/成功率聚合（XM-REQLOG-METRICS）：默认 off——与请求详情链路
		// 的默认值同一条纪律（cmd/platform-api 的 parseReqlogMode 默认
		// off，不是 fake）：这批指标的原料是记录代理落盘的真实数据，没有
		// "safe fake" 可以顶替；没有部署记录代理的环境本来就不该有这三条
		// 指标，off 让它们如实缺席，而不是编三个假数字出来。
		ReqlogMetricsMode:           ReqlogMetricsModeOff,
		ReqlogMetricsModeRecognized: true,
		ReqlogMetricsDataDir:        defaultReqlogMetricsDataDir,
		ReqlogMetricsInterval:       DefaultReqlogMetricsInterval,
		ReqlogMetricsRunOnStart:     true,
		// Connector health probing defaults on for the same reason the
		// sync jobs do: an ops page that never gets connector health data
		// looks identical to "the probe is broken", so a default-off probe
		// would just move the confusion from "no data" to "no data, but
		// now silently by design".
		ConnectorProbeEnabled:    true,
		ConnectorProbeInterval:   DefaultConnectorProbeInterval,
		ConnectorProbeRunOnStart: true,
		// CPA 默认关闭（XM-CPA0）：off 是唯一不需要任何配置就安全的状态——
		// 没有 fake 模式可以垫底，打开却没配 CPADataDir 只会让每轮同步都写一条
		// SyncFailed。cmd/platform-worker/config.go 按 XM_CPA_MODE=file 显式打开。
		CPASyncEnabled:    false,
		CPASyncInterval:   DefaultCPASyncInterval,
		CPASyncRunOnStart: true,
		CPAMode:           CPAModeOff,
		CPAInstanceID:     DefaultCPAInstanceID,
	}
}

func (c Config) normalized() Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(c.Environment) == "" {
		c.Environment = defaults.Environment
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = defaults.HeartbeatInterval
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = defaults.MaxWorkers
	}
	if c.Sub2APISyncInterval == 0 {
		c.Sub2APISyncInterval = defaults.Sub2APISyncInterval
	}
	if c.Sub2APIRequestTimeout <= 0 {
		// 漏填超时回落到默认值，绝不能变成「没有超时」（规格 §18.1-4）
		c.Sub2APIRequestTimeout = defaults.Sub2APIRequestTimeout
	}
	if c.NewAPISyncInterval == 0 {
		c.NewAPISyncInterval = defaults.NewAPISyncInterval
	}
	if strings.TrimSpace(string(c.NewAPIMode)) == "" {
		c.NewAPIMode = defaults.NewAPIMode
	}
	if strings.TrimSpace(c.NewAPIInstanceID) == "" {
		c.NewAPIInstanceID = defaults.NewAPIInstanceID
	}
	if c.NewAPIRequestTimeout <= 0 {
		// 漏填超时回落到默认值，绝不能变成「没有超时」（规格 §18.1-4）
		c.NewAPIRequestTimeout = defaults.NewAPIRequestTimeout
	}
	if c.FinanceCollectInterval == 0 {
		c.FinanceCollectInterval = defaults.FinanceCollectInterval
	}
	if c.FinanceCollectRequestTimeout <= 0 {
		// 漏填超时回落到默认值，绝不能变成「没有超时」（规格 §18.1-4）
		c.FinanceCollectRequestTimeout = defaults.FinanceCollectRequestTimeout
	}
	if strings.TrimSpace(string(c.FinanceCollectMode)) == "" {
		c.FinanceCollectMode = defaults.FinanceCollectMode
	}
	if strings.TrimSpace(c.FinanceCollectInstanceID) == "" {
		c.FinanceCollectInstanceID = defaults.FinanceCollectInstanceID
	}
	if c.RetentionInterval == 0 {
		c.RetentionInterval = defaults.RetentionInterval
	}
	if c.MetricSampleRetentionDays <= 0 {
		c.MetricSampleRetentionDays = defaults.MetricSampleRetentionDays
	}
	if c.AlertRetentionDays <= 0 {
		c.AlertRetentionDays = defaults.AlertRetentionDays
	}
	if c.AlertEvaluateInterval == 0 {
		c.AlertEvaluateInterval = defaults.AlertEvaluateInterval
	}
	if c.ConnectorProbeInterval == 0 {
		c.ConnectorProbeInterval = defaults.ConnectorProbeInterval
	}
	if c.AlertBalanceThresholdMinorUnits <= 0 {
		// 零或负阈值等于「永不触发」，但看起来像是配了一个阈值。
		// 回落到默认值而不是照单全收——静默失效的护栏比没有护栏更危险。
		c.AlertBalanceThresholdMinorUnits = defaults.AlertBalanceThresholdMinorUnits
	}
	if strings.TrimSpace(string(c.Sub2APIMode)) == "" {
		c.Sub2APIMode = defaults.Sub2APIMode
	}
	// 来源标识借道一个局部变量补默认值，而不是「字段直接赋成默认字段」一行写完：
	// 后一种写法会被 gitleaks 的 generic-api-key 规则误判成泄漏——它看见
	// 名字里带 API 的东西后面跟着赋值和一长串字符就报警，认不出两边都只是
	// Go 标识符。本仓禁止加 gitleaks allowlist（会顺手掩盖真报，见
	// scripts/check-governance.sh），所以换个写法比放宽扫描器划算。
	source := strings.TrimSpace(c.Sub2APIInstanceID)
	if source == "" {
		source = defaults.Sub2APIInstanceID
	}
	c.Sub2APIInstanceID = source
	if strings.TrimSpace(string(c.ReqlogMetricsMode)) == "" {
		c.ReqlogMetricsMode = defaults.ReqlogMetricsMode
		c.ReqlogMetricsModeRecognized = defaults.ReqlogMetricsModeRecognized
	}
	if strings.TrimSpace(c.ReqlogMetricsDataDir) == "" {
		c.ReqlogMetricsDataDir = defaults.ReqlogMetricsDataDir
	}
	if c.ReqlogMetricsInterval == 0 {
		c.ReqlogMetricsInterval = defaults.ReqlogMetricsInterval
	}
	if c.Logger == nil {
		c.Logger = structuredDefaultLogger()
	}
	if c.CPASyncInterval == 0 {
		c.CPASyncInterval = defaults.CPASyncInterval
	}
	if strings.TrimSpace(string(c.CPAMode)) == "" {
		c.CPAMode = defaults.CPAMode
	}
	if strings.TrimSpace(c.CPAInstanceID) == "" {
		c.CPAInstanceID = defaults.CPAInstanceID
	}
	// Do not infer an enabled archive mode from a partially populated literal.
	// The archive config has its own fail-closed defaults and validation.
	c.AuditArchive = c.AuditArchive.normalized()
	return c
}

func (c Config) validate() error {
	if c.HeartbeatInterval < time.Second {
		return fmt.Errorf("heartbeat interval %s is below River's one-second minimum", c.HeartbeatInterval)
	}
	if c.MaxWorkers < 1 {
		return fmt.Errorf("max workers must be positive, got %d", c.MaxWorkers)
	}
	if c.HeartbeatFailures < 0 {
		return fmt.Errorf("heartbeat failures must not be negative, got %d", c.HeartbeatFailures)
	}
	if c.HeartbeatFailures > heartbeatMaxAttempts-1 {
		return fmt.Errorf("heartbeat failures must be less than max attempts (%d), got %d", heartbeatMaxAttempts, c.HeartbeatFailures)
	}
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("environment must not be empty")
	}
	c.AuditArchive.Environment = c.Environment
	if err := c.AuditArchive.Validate(); err != nil {
		return err
	}
	if c.HeartbeatFailures > 0 && c.Environment != "development" && c.Environment != "test" {
		return fmt.Errorf("heartbeat failure injection is only allowed in development/test")
	}
	if c.HeartbeatRunID != "" && c.Environment != "test" {
		return fmt.Errorf("heartbeat run ID is reserved for test environment")
	}
	if _, err := ParseSub2APIMode(string(c.Sub2APIMode)); err != nil {
		return err
	}
	if c.Sub2APISyncRunID != "" && c.Environment == "production" {
		// RunID 只服务于集成测试隔离。生产留空才让所有副本共享同一条唯一性
		// 记录，同一个周期只采一次；配上 RunID 等于给每个副本发一张免签，
		// 上游会挨到 N 倍读取。
		return fmt.Errorf("sub2api sync run ID must not be set in production")
	}
	if c.Sub2APISyncEnabled && c.Sub2APISyncInterval < time.Second {
		return fmt.Errorf("sub2api sync interval %s is below River's one-second minimum", c.Sub2APISyncInterval)
	}
	if c.Sub2APISyncEnabled && c.Sub2APIMode == Sub2APIModeFake && c.Environment == "production" && c.ConnectorConfigs == nil {
		// 生产环境启动即拒（fail closed）。
		//
		// 有 ConnectorConfigs 时放行：模式由 core.connector_config 每轮决定，
		// 环境变量里的 fake 只是缺省，而生效模式仍为 fake 的每一轮会由动态
		// 工厂记成 not_supported（见 ErrConnectorProductionFake），演示数据
		// 同样一条都写不进生产。
		//
		// Fake 客户端返回的是**构造出来的**用户数、收入、余额，而同步任务会把
		// 它们原样写进 ops.metric_observation / _sample。一旦落库，看板就以正常
		// 主数字 + 「数据新鲜」徽章呈现它们，只在底部小字里写一个 source——
		// 那已经不是「库里有演示数据」的风险，而是生产运营读数直接是假的。
		//
		// 为什么在启动时拒绝而不是运行时降级：默认值（DefaultConfig）是
		// Sub2APIMode=fake，所以「忘了配」的结果恰好是最危险的那一种。只有让
		// 进程起不来，这个疏忽才必然被发现；写一条告警日志会淹没在启动噪声里。
		//
		// staging / development 保持允许：XM-0017 的真实只读账号就绪前，
		// 这两个环境本来就要靠 Fake 把整条采集链路跑通。
		return fmt.Errorf(
			"环境 production 不允许 sub2api fake 模式：Fake 会把演示数据写成生产运营读数，" +
				"请配置 XM_SUB2API_MODE=real（或显式关闭同步 XM_SUB2API_SYNC_ENABLED=false）")
	}
	if ref := strings.TrimSpace(c.Sub2APICredentialRef); ref != "" {
		// 只校验引用的**形状**，不解析出任何明文（ADR-014）。拼错的引用在
		// 进程启动时就该炸，而不是等 XM-0017 接上真实客户端那天才发现。
		if _, err := secrets.ParseCredentialRef(ref); err != nil {
			return fmt.Errorf("sub2api credential ref: %w", err)
		}
	}
	if _, err := ParseNewAPIMode(string(c.NewAPIMode)); err != nil {
		return err
	}
	if c.NewAPISyncRunID != "" && c.Environment == "production" {
		// 与 Sub2APISyncRunID 同一条理由：RunID 只服务于集成测试隔离。
		// 生产留空才让所有副本共享同一条唯一性记录，同一个周期只采一次。
		return fmt.Errorf("newapi sync run ID must not be set in production")
	}
	if c.NewAPISyncEnabled && c.NewAPISyncInterval < time.Second {
		return fmt.Errorf("newapi sync interval %s is below River's one-second minimum", c.NewAPISyncInterval)
	}
	if c.NewAPISyncEnabled && c.NewAPIMode == NewAPIModeFake && c.Environment == "production" && c.ConnectorConfigs == nil {
		// 生产环境启动即拒（fail closed）——与 Sub2API 完全同一条纪律，
		// 包括「有 ConnectorConfigs 时放行、每轮再 fail closed」那一条。
		//
		// Fake 客户端返回的是**构造出来的**用户数、充值额、渠道错误率，
		// 而同步任务会把它们原样写进 ops.metric_observation / _sample。
		// 一旦落库，看板就以正常主数字 + 「数据新鲜」徽章呈现它们，
		// 只在底部小字里写一个 source——那已经不是「库里有演示数据」的风险，
		// 而是生产运营读数直接是假的（宪法 12 条）。
		//
		// 为什么在启动时拒绝而不是运行时降级：默认值（DefaultConfig）是
		// NewAPIMode=fake，所以「忘了配」的结果恰好是最危险的那一种。
		//
		// XM-0038 之后 real 模式真的走得通了，所以这里的出路与 Sub2API 一致：
		// 配 real（见 docs/runbooks/SWITCH-NEWAPI-REAL.md），或者显式关掉
		// 这条采集。之前那条「real 根本不存在、生产只能关同步」的说法已经
		// 过时，留着会让运维照旧去关同步，白白丢掉一条能用的采集链路。
		return fmt.Errorf(
			"环境 production 不允许 newapi fake 模式：Fake 会把演示数据写成生产运营读数，" +
				"请配置 XM_NEWAPI_MODE=real（见 docs/runbooks/SWITCH-NEWAPI-REAL.md），" +
				"或者显式关闭同步 XM_NEWAPI_SYNC_ENABLED=false")
	}
	if ref := strings.TrimSpace(c.NewAPICredentialRef); ref != "" {
		// 只校验引用的**形状**，不解析出任何明文（ADR-014）。拼错的引用在
		// 进程启动时就该炸，而不是等到第一轮同步才发现。
		if _, err := secrets.ParseCredentialRef(ref); err != nil {
			return fmt.Errorf("newapi credential ref: %w", err)
		}
	}
	if _, err := ParseFinanceCollectMode(string(c.FinanceCollectMode)); err != nil {
		return err
	}
	if provider := strings.TrimSpace(c.FinanceCollectSecretProvider); provider != "" {
		if provider != "env" && provider != "file" {
			return fmt.Errorf("finance secret provider %q 只接受 env 或 file", provider)
		}
		if len(c.FinanceCollectSecretScopes) == 0 {
			return fmt.Errorf("finance secret provider 已启用但 scope allowlist 为空")
		}
	}
	if c.FinanceCollectRunID != "" && c.Environment == "production" {
		// 与 Sub2APISyncRunID 同一条理由，外加一条本任务独有的：多个副本
		// 各跑各的会同时 upsert 同一批今日行，最后留下的是哪一轮的读数
		// 取决于谁最后写完——而它们读的是不同时刻的上游。
		return fmt.Errorf("finance collect run ID must not be set in production")
	}
	if c.FinanceCollectEnabled && c.FinanceCollectInterval < time.Second {
		return fmt.Errorf("finance collect interval %s is below River's one-second minimum",
			c.FinanceCollectInterval)
	}
	if c.FinanceCollectEnabled && c.FinanceCollectMode == FinanceCollectModeFake &&
		c.Environment == "production" {
		// 生产环境启动即拒（fail closed）——与 Sub2API / NewAPI 完全同一条纪律，
		// 但后果更重：Fake 的读数不只进 ops 指标，还会被**写进利润台账**
		// （finance.profit_daily）。台账是过去冻结的（§5.3），一旦让 Fake 的
		// 数字落进某一天的行，那一天的毛利就永久是假的——没有任何后续采集
		// 会去覆盖一个历史业务日，只能靠人工数据修复（宪法 2 条的
		// Platform Lifecycle Operation）把它挖出来。
		//
		// 为什么在启动时拒绝而不是运行时降级：默认值就是 fake，
		// 所以「忘了配」的结果恰好是最危险的那一种。
		return fmt.Errorf(
			"环境 production 不允许 finance collect fake 模式：Fake 的读数会被写进利润台账，" +
				"而历史业务日过去冻结、不会被后续采集覆盖。" +
				"请配置 XM_FINANCE_COLLECT_MODE=real（或显式关闭采集 XM_FINANCE_COLLECT_ENABLED=false）")
	}
	if c.AlertEvaluateEnabled && c.AlertEvaluateInterval < time.Second {
		return fmt.Errorf("alert evaluate interval %s is below River's one-second minimum", c.AlertEvaluateInterval)
	}
	if c.AlertEvaluateRunID != "" && c.Environment == "production" {
		// 与 Sub2APISyncRunID 同一条理由：RunID 只服务于集成测试隔离。
		// 生产配上它等于给每个副本发一张免签，同一条告警会被投递 N 次。
		return fmt.Errorf("alert evaluate run ID must not be set in production")
	}
	switch c.ReqlogMetricsMode {
	case ReqlogMetricsModeOff, ReqlogMetricsModeFile:
	default:
		return fmt.Errorf("reqlog metrics mode %q: 只接受 off 或 file", c.ReqlogMetricsMode)
	}
	if c.ReqlogMetricsMode == ReqlogMetricsModeFile && c.ReqlogMetricsInterval < time.Second {
		return fmt.Errorf("reqlog metrics interval %s is below River's one-second minimum", c.ReqlogMetricsInterval)
	}
	if c.ReqlogMetricsRunID != "" && c.Environment == "production" {
		// 与 Sub2APISyncRunID 同一条理由：RunID 只服务于集成测试隔离。
		// 生产配上它等于给每个副本发一张免签，同一个周期会被聚合 N 次。
		return fmt.Errorf("reqlog metrics run ID must not be set in production")
	}
	if c.ConnectorProbeEnabled && c.ConnectorProbeInterval < time.Second {
		return fmt.Errorf("connector probe interval %s is below River's one-second minimum", c.ConnectorProbeInterval)
	}
	if c.ConnectorProbeEnabled && c.Environment == "production" && c.ConnectorConfigs == nil &&
		(c.Sub2APIMode == Sub2APIModeFake || c.NewAPIMode == NewAPIModeFake) {
		// Fail closed at startup, same discipline as the Sub2API/NewAPI
		// sync gates above -- kept as its own independent block (not OR'd
		// into either of those) because the probe can legitimately run with
		// both sync jobs disabled (e.g. "just watch connectivity, skip
		// collecting business data"), so it needs to be both triggerable
		// and disable-able on its own.
		//
		// With ConnectorConfigs set (every real deployment: cmd/platform-
		// worker/main.go wires it unconditionally), this static fallback
		// path is never reached in the first place -- the dynamic factory's
		// own per-round check already turns a fake-in-production round into
		// a KindNotSupported observation. This guard only closes the
		// narrower gap of an embedder/test that runs the probe against the
		// static factory (ConnectorConfigs == nil) without having set a
		// real mode explicitly.
		return fmt.Errorf(
			"environment production does not allow connector probe to probe a fake-mode connector: " +
				"configure real mode for sub2api/newapi (XM_SUB2API_MODE=real / XM_NEWAPI_MODE=real), " +
				"or explicitly disable probing (XM_CONNECTOR_PROBE_ENABLED=false)")
	}
	if c.ConnectorProbeRunID != "" && c.Environment == "production" {
		// Same reason as every other *RunID field: it exists only for
		// integration-test isolation. Production must leave it empty so
		// all replicas share one unique probe record and each round only
		// probes once.
		return fmt.Errorf("connector probe run ID must not be set in production")
	}
	if _, err := ParseCPAMode(string(c.CPAMode)); err != nil {
		return err
	}
	if c.CPASyncRunID != "" && c.Environment == "production" {
		// 与 Sub2APISyncRunID 同一条理由：RunID 只服务于集成测试隔离。
		return fmt.Errorf("cpa sync run ID must not be set in production")
	}
	if c.CPASyncEnabled && c.CPAMode != CPAModeFile {
		// off 是硬性的停用状态，不是「暂时没配」：即便有人误把 Enabled 设成
		// true（比如手写 Config 字面量做集成测试），也不能在 mode=off 时
		// 注册一个每轮都必然失败的任务——那会把 cpa.* 四条指标的新鲜度看板
		// 从「未接入」变成「持续报错」，对运维是噪音而不是信号。
		return fmt.Errorf("cpa sync 已启用但模式不是 file（当前 %q）：off 模式下必须保持同步关闭", c.CPAMode)
	}
	if c.CPASyncEnabled && c.CPASyncInterval < time.Second {
		return fmt.Errorf("cpa sync interval %s is below River's one-second minimum", c.CPASyncInterval)
	}
	if c.CPASyncEnabled && strings.TrimSpace(c.CPADataDir) == "" {
		return fmt.Errorf("cpa sync 已启用（file 模式）但 CPADataDir 为空")
	}
	return nil
}

// NewClient builds a River client with the two baseline queues and one
// periodic heartbeat. The pool is only used when the client is started or a
// job is inserted; construction itself does not connect to Postgres.
func NewClient(pool *pgxpool.Pool, cfg Config) (*river.Client[pgx.Tx], error) {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.ConnectorConfigs != nil {
		// 运维必须能一眼看出：这个进程的接入模式**不是**环境变量说了算，
		// 而是 core.connector_config 每轮决定，环境变量只是缺省。
		cfg.Logger.Info("connector_config_source",
			slog.String("event", "connector_config_source"),
			slog.String("module", "platform.jobs"),
			slog.String("environment", cfg.Environment),
			slog.String("principal_id", "worker:platform"),
			slog.String("connector_config_source", "database"),
			slog.String("sub2api_default_mode", string(cfg.Sub2APIMode)),
			slog.String("newapi_default_mode", string(cfg.NewAPIMode)),
			slog.String("secret_root", cfg.SecretRoot))
	}

	workers := river.NewWorkers()
	// XM-OPS0: WithObservations lets the ops overview query answer "is the
	// worker alive" from the same freshness model as everything else,
	// instead of a bespoke liveness probe.
	river.AddWorker(workers, NewHeartbeatWorker(cfg.Logger, cfg.Environment).
		WithObservations(ops.NewStore(pool), cfg.HeartbeatInterval))

	heartbeat, err := newManifestPeriodicJob(
		HeartbeatJobKind, cfg.HeartbeatInterval, cfg.HeartbeatRunOnStart,
		func() (river.JobArgs, *river.InsertOpts) {
			args := HeartbeatArgs{
				RunID:                 cfg.HeartbeatRunID,
				FailuresBeforeSuccess: cfg.HeartbeatFailures,
			}
			opts := args.InsertOpts()
			return args, &opts
		},
	)
	if err != nil {
		return nil, err
	}
	periodic := []*river.PeriodicJob{heartbeat}

	if cfg.Sub2APISyncEnabled {
		// 仓储在这里从既有的连接池构造：任务只依赖 ObservationStore 接口，
		// 换成内存实现就能在没有库的机器上跑完整条失败路径的单元测试。
		river.AddWorker(workers, NewSub2APISyncWorker(Sub2APISyncOptions{
			Logger:           cfg.Logger,
			Environment:      cfg.Environment,
			InstanceID:       cfg.Sub2APIInstanceID,
			Mode:             cfg.Sub2APIMode,
			ExpectedInterval: cfg.Sub2APISyncInterval,
			Store:            ops.NewStore(pool),
			NewClient:        cfg.sub2apiClientFactory(),
		}))
		sub2apiPeriodic, err := newManifestPeriodicJob(
			Sub2APISyncJobKind, cfg.Sub2APISyncInterval, cfg.Sub2APISyncRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := Sub2APISyncArgs{RunID: cfg.Sub2APISyncRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, sub2apiPeriodic)
	}

	if cfg.NewAPISyncEnabled {
		// 与 Sub2API 同样的装配方式：仓储在这里从既有连接池构造，任务只依赖
		// ObservationStore 接口，换成内存实现就能在没有库的机器上跑完整条
		// 失败路径的单元测试。
		river.AddWorker(workers, NewNewAPISyncWorker(NewAPISyncOptions{
			Logger:           cfg.Logger,
			Environment:      cfg.Environment,
			InstanceID:       cfg.NewAPIInstanceID,
			Mode:             cfg.NewAPIMode,
			ExpectedInterval: cfg.NewAPISyncInterval,
			Store:            ops.NewStore(pool),
			NewClient:        cfg.newapiClientFactory(),
		}))
		newapiPeriodic, err := newManifestPeriodicJob(
			NewAPISyncJobKind, cfg.NewAPISyncInterval, cfg.NewAPISyncRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := NewAPISyncArgs{RunID: cfg.NewAPISyncRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, newapiPeriodic)
	}

	if cfg.FinanceCollectEnabled {
		// 两个仓储在这里从既有连接池构造：任务只依赖 finance.AccountRegistry
		// 与 finance.LedgerWriter 两个接口，换成内存实现就能在没有库的机器上
		// 跑完三条不静默纪律的分支（同 ObservationStore 的理由）。
		//
		// 台账仓储拿的是**同一个时钟**：「今日可覆盖、过去冻结」（§5.3）的
		// 判据与业务日切分必须来自同一个 now，否则跨零点那一瞬会出现
		// 「按 A 时钟算是今天、按 B 时钟算是昨天」的写入，然后被冻结纪律拒掉。
		river.AddWorker(workers, NewFinanceCollectWorker(FinanceCollectOptions{
			Logger:           cfg.Logger,
			Environment:      cfg.Environment,
			InstanceID:       cfg.FinanceCollectInstanceID,
			Mode:             cfg.FinanceCollectMode,
			ExpectedInterval: cfg.FinanceCollectInterval,
			Store:            ops.NewStore(pool),
			Registry:         finance.NewStore(pool),
			// 订阅摊销的取数端（XM-0037c）。与登记簿分成两个仓储：
			// 登记簿是「怎么算」，批次与代理是「付了多少钱」。
			Subscriptions: finance.NewSubscriptionStore(pool),
			// 余额历史（XM-0037d）。与台账分开是因为它**不参与成本**（§2.3）：
			// 台账里的每一个数都会进毛利，余额一个都不会。
			Balances: finance.NewSummaryStore(pool, nil),
			Ledger:   finance.NewProfitStore(pool, nil),
			NewClient: NewFinanceCollectClientFactory(
				cfg.FinanceCollectMode,
				finance.RealMeteringConfig{
					TargetAllowlist: cfg.FinanceCollectTargetAllowlist,
					Secrets:         cfg.FinanceCollectSecrets,
					Timeout:         cfg.FinanceCollectRequestTimeout,
					Environment:     cfg.Environment,
					InstanceID:      cfg.FinanceCollectInstanceID,
					// XM-0044：newapi 收入侧的只读 DSN 通道。nil = 没配，
					// 收入继续 not_supported、台账写 NULL（§5.1）。
					NewAPIRevenue: cfg.FinanceNewAPIRevenue,
				},
				nil,
			),
			// ResolvePlatform 留空 = 全部落「未归属」桶（§5.2）。
			// 登记簿里还没有「自营账号属于哪个自营平台」这一列，
			// 编一个归属出来会让四桶里的有效平台桶凭空多出金额（宪法 12 条）。
			// 037c/d 接上归属配置时只注入一个函数，写入路径不动。
		}))
		financePeriodic, err := newManifestPeriodicJob(
			FinanceCollectJobKind, cfg.FinanceCollectInterval, cfg.FinanceCollectRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := FinanceCollectArgs{RunID: cfg.FinanceCollectRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, financePeriodic)
	}

	if cfg.RetentionEnabled {
		// XM-R012 保留期清理。两个仓储从既有连接池构造；任务只依赖
		// SamplePruner / AlertPruner 两个单方法接口，换成内存实现就能在没有
		// 库的机器上跑完整条分批循环。
		//
		// **审计事件不在这里**：审计链一条都不删（宪法 11 条 append-only，
		// 删中间任意一条都会断链，而且库层规则会让 DELETE 静默空转）。
		// 理由完整写在 retention.go 的文件头。
		river.AddWorker(workers, NewRetentionWorker(RetentionOptions{
			Logger:              cfg.Logger,
			Environment:         cfg.Environment,
			Samples:             ops.NewStore(pool),
			Alerts:              alerts.NewStore(pool),
			SampleRetentionDays: cfg.MetricSampleRetentionDays,
			AlertRetentionDays:  cfg.AlertRetentionDays,
			// XM-OPS0: records "last cleanup result" as an observation so
			// the ops overview query can show it without tailing logs.
			Observations:     ops.NewStore(pool),
			ExpectedInterval: cfg.RetentionInterval,
		}))
		retentionPeriodic, err := newManifestPeriodicJob(
			RetentionJobKind, cfg.RetentionInterval, cfg.RetentionRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := RetentionArgs{RunID: cfg.RetentionRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, retentionPeriodic)
	}

	if cfg.AlertEvaluateEnabled {
		// 渠道在这里装配。配错（ref 拼错、URL 不是 https、只配了一半）
		// 会让进程起不来——理由见 newAlertNotifier 的注释：一个没建起来的
		// 告警渠道不会有任何后续痕迹，只会在真出事那天才被发现。
		notifier, err := newAlertNotifier(AlertNotifierConfig{
			TelegramBotRef:  cfg.AlertTelegramBotRef,
			TelegramChatID:  cfg.AlertTelegramChatID,
			WebhookURL:      cfg.AlertWebhookURL,
			Secrets:         cfg.AlertSecrets,
			WeComWebhookRef: cfg.AlertWeComWebhookRef,
			WeComSecrets:    cfg.AlertWeComSecrets,
			Logger:          cfg.Logger,
		})
		if err != nil {
			return nil, fmt.Errorf("告警投递渠道: %w", err)
		}
		if notifier.Len() == 0 {
			// 「一个渠道都没配」是允许的（本地开发、刚起的环境），但必须在
			// 启动时就说出来，而不是等第一条告警来的时候才在某一行 warn 里
			// 一闪而过。规格 §9.4 的闭环在这个状态下是断的。
			cfg.Logger.Warn("alert_notifier_not_configured",
				slog.String("module", "platform.jobs"),
				slog.String("environment", cfg.Environment),
				slog.String("error_code", "no_notifier_configured"),
				slog.String("hint", "告警只会落库，不会通知任何人；配置 XM_ALERT_TELEGRAM_BOT_REF + XM_ALERT_TELEGRAM_CHAT_ID、XM_ALERT_WEBHOOK_URL 或 XM_ALERT_WECOM_WEBHOOK_REF"))
		}

		// 「采集周期」直接取 Sub2API 的同步周期，而不是再开一个可配项：
		// R1b 的「陈旧持续 ≥2 采集周期」里的采集周期，指的就是这条链路的
		// 采集节奏。多一个变量只会让两者漂开，然后规则按一个不存在的节奏
		// 去判断持续时间。
		// 可用天数来自 finance 而不是 ops 观测（XM-0049）——它是平台自己算
		// 出来的数，两侧原料都在自己的库里。时钟传 nil：告警评估要判
		// 「余额过期没有」，用的就是此刻。
		var evaluator *alerts.Evaluator
		ruleConfig := alerts.RuleConfig{
			CollectionInterval:         cfg.Sub2APISyncInterval,
			BalanceThresholdMinorUnits: cfg.AlertBalanceThresholdMinorUnits,
			RunwayThresholds:           cfg.AlertRunwayThresholds,
		}
		if cfg.RunwayThresholdProvider != nil {
			evaluator = alerts.NewEvaluatorWithThresholdProvider(
				ops.NewStore(pool), finance.NewSummaryStore(pool, nil), cfg.RunwayThresholdProvider, ruleConfig)
		} else {
			evaluator = alerts.NewEvaluator(
				ops.NewStore(pool), finance.NewSummaryStore(pool, nil), ruleConfig)
		}
		river.AddWorker(workers, NewAlertEvaluateWorker(AlertEvaluateOptions{
			Logger:      cfg.Logger,
			Environment: cfg.Environment,
			Reconciler: alerts.NewReconciler(alerts.ReconcilerOptions{
				Store:     alerts.NewStore(pool),
				Evaluator: evaluator,
				Notifier:  notifier,
				Logger:    cfg.Logger,
			}),
		}))
		alertPeriodic, err := newManifestPeriodicJob(
			AlertEvaluateJobKind, cfg.AlertEvaluateInterval, cfg.AlertEvaluateRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := AlertEvaluateArgs{RunID: cfg.AlertEvaluateRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, alertPeriodic)
	}

	if cfg.ReqlogMetricsMode == ReqlogMetricsModeFile {
		// 仓储在这里从既有连接池构造：任务只依赖 ObservationStore 接口，
		// 与 sub2api_sync/newapi_sync 同一条装配纪律。
		river.AddWorker(workers, NewReqlogMetricsWorker(ReqlogMetricsOptions{
			Logger:           cfg.Logger,
			Environment:      cfg.Environment,
			DataDir:          cfg.ReqlogMetricsDataDir,
			Sub2APISource:    cfg.Sub2APIInstanceID,
			NewAPISource:     cfg.NewAPIInstanceID,
			Store:            ops.NewStore(pool),
			ExpectedInterval: cfg.ReqlogMetricsInterval,
		}))
		reqlogMetricsPeriodic, err := newManifestPeriodicJob(
			ReqlogMetricsJobKind, cfg.ReqlogMetricsInterval, cfg.ReqlogMetricsRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := ReqlogMetricsArgs{RunID: cfg.ReqlogMetricsRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, reqlogMetricsPeriodic)
	} else if !cfg.ReqlogMetricsModeRecognized {
		// 原始配置值不是 off/file/空串（多半是 fake/real，服务的是请求详情
		// 那条完全不同的链路）：已经退化成 off 处理，但这里必须留一条 warn，
		// 不然「聚合链路为什么没有指标」会变成一次排查两个进程配置的事故。
		cfg.Logger.Warn("reqlog_metrics_mode_unrecognized",
			slog.String("event", "reqlog_metrics_mode_unrecognized"),
			slog.String("module", reqlogMetricsModule),
			slog.String("environment", cfg.Environment),
			slog.String("detail", "XM_REQLOG_MODE 的值 worker 侧无法识别（只认 off/file），"+
				"已按 off 处理；如果这是 fake/real，那是请求详情链路（platform-api）的合法值，"+
				"与本任务无关"))
	}
	if cfg.ConnectorProbeEnabled {
		// XM-OPS0: the probe reuses the exact same effective-mode
		// resolution the sync workers use (cfg.sub2apiClientFactory() /
		// cfg.newapiClientFactory()) -- same XM-CRED0 dynamic lookup, same
		// production+fake guard, same env-var defaults when
		// ConnectorConfigs is nil. This is deliberate: "which connector am
		// I talking to" must never be a second decision that can drift
		// from what the sync jobs are already doing.
		sub2apiFactory := cfg.sub2apiClientFactory()
		newapiFactory := cfg.newapiClientFactory()
		river.AddWorker(workers, NewConnectorProbeWorker(ConnectorProbeOptions{
			Logger:        cfg.Logger,
			Environment:   cfg.Environment,
			Store:         ops.NewStore(pool),
			Sub2APISource: cfg.Sub2APIInstanceID,
			// Adapts the full Sub2APIClientFactory down to probeClientFactory
			// (see its doc comment): sub2api.ReadClientV2 already satisfies
			// probeReadClient structurally, so this is a pure narrowing, not
			// a behavior change.
			Sub2APINewClient: func(ctx context.Context) (probeReadClient, error) {
				client, err := sub2apiFactory(ctx)
				if err != nil {
					return nil, err
				}
				return client, nil
			},
			NewAPISource: cfg.NewAPIInstanceID,
			NewAPINewClient: func(ctx context.Context) (probeReadClient, error) {
				client, err := newapiFactory(ctx)
				if err != nil {
					return nil, err
				}
				return client, nil
			},
			ExpectedInterval: cfg.ConnectorProbeInterval,
		}))
		connectorProbePeriodic, err := newManifestPeriodicJob(
			ConnectorProbeJobKind, cfg.ConnectorProbeInterval, cfg.ConnectorProbeRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := ConnectorProbeArgs{RunID: cfg.ConnectorProbeRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, connectorProbePeriodic)
	}

	if cfg.CPASyncEnabled {
		// 仓储在这里从既有连接池构造，同 Sub2API/NewAPI 的理由：任务只依赖
		// ObservationStore 接口。客户端工厂只在 mode=file 时才真的打开
		// usage.sqlite——validate() 已经保证 Enabled 时 mode 只能是 file。
		river.AddWorker(workers, NewCPASyncWorker(CPASyncOptions{
			Logger:           cfg.Logger,
			Environment:      cfg.Environment,
			InstanceID:       cfg.CPAInstanceID,
			Mode:             cfg.CPAMode,
			ExpectedInterval: cfg.CPASyncInterval,
			Store:            ops.NewStore(pool),
			NewClient:        NewCPAClientFactory(cfg.CPAMode, cfg.CPADataDir, cfg.CPAFileName),
		}))
		cpaPeriodic, err := newManifestPeriodicJob(
			CPASyncJobKind, cfg.CPASyncInterval, cfg.CPASyncRunOnStart,
			func() (river.JobArgs, *river.InsertOpts) {
				args := CPASyncArgs{RunID: cfg.CPASyncRunID}
				opts := args.InsertOpts()
				return args, &opts
			},
		)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, cpaPeriodic)
	}

	// XM-ASSURE1-core：检测任务批次处理器。**始终注册**，不受任何
	// _ENABLED 开关影响，也不进 periodic 列表——它是按需触发的（由
	// assurance.probe.run@1 Action 入队），不是周期任务。fake 模式的探测
	// 完全绕过两道 Kill Switch（ADR-019 决策·四），如果这里也加一个"未启用
	// 就不注册 Worker"的开关，会让 fake 模式的批次卡死在 pending（River
	// 找不到能处理这个 kind 的 Worker，报 unhandled job kind）。真正"允许
	// 探测花钱"的闸在 assurance.Store.checkGates 里，不是"这个 Worker
	// 存不存在"。
	assuranceStore, err := assurance.NewStore(pool, nil, ops.NewStore(pool),
		assurance.WithLimits(assurance.Limits{DailyBudgetPerPlatform: cfg.AssuranceProbeDailyBudget}),
		assurance.WithGlobalKillSwitch(cfg.AssuranceProbeGlobalEnabled),
	)
	if err != nil {
		return nil, err
	}
	river.AddWorker(workers, NewAssuranceProbeWorker(AssuranceProbeOptions{
		Logger:                  cfg.Logger,
		Environment:             cfg.Environment,
		Store:                   assuranceStore,
		GlobalKillSwitchEnabled: cfg.AssuranceProbeGlobalEnabled,
		ProbeSecrets:            cfg.AssuranceProbeSecrets,
	}))

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger: cfg.Logger,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault:   {MaxWorkers: cfg.MaxWorkers},
			QueueMaintenance:     {MaxWorkers: cfg.MaxWorkers},
			assurance.QueueProbe: {MaxWorkers: cfg.MaxWorkers},
		},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}

// newManifestPeriodicJob is the single construction path for periodic River
// jobs. The static JobManifest supplies the stable ID/queue contract while the
// caller supplies only deployment-resolved cadence and enablement. Keeping the
// wrapper here prevents a future job from silently omitting the manifest's
// uniqueness state set when it is wired into NewClient.
func newManifestPeriodicJob(
	id string,
	interval time.Duration,
	runOnStart bool,
	factory func() (river.JobArgs, *river.InsertOpts),
) (*river.PeriodicJob, error) {
	spec, ok := registeredPeriodicJobSpec(id)
	if !ok {
		return nil, fmt.Errorf("periodic job %q is not present in the JobManifest registry", id)
	}
	if interval < time.Second || interval%time.Second != 0 {
		return nil, fmt.Errorf("periodic job %q interval %s must be whole seconds and at least one second", id, interval)
	}
	if factory == nil {
		return nil, fmt.Errorf("periodic job %q has no args factory", id)
	}
	return river.NewPeriodicJob(
		river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) {
			args, opts := factory()
			if opts == nil {
				opts = &river.InsertOpts{}
			}
			// The schedule value is deployment-specific, while the uniqueness
			// dimensions are frozen by the Args contract and must follow the
			// effective cadence here as well.
			opts.Queue = spec.Queue
			opts.UniqueOpts.ByPeriod = interval
			if len(opts.UniqueOpts.ByState) == 0 {
				opts.UniqueOpts.ByState = rivertype.UniqueOptsByStateDefault()
			}
			return args, opts
		},
		&river.PeriodicJobOpts{ID: spec.ID, RunOnStart: runOnStart},
	), nil
}

// sub2apiClientFactory 按是否有动态配置来源选择工厂。
//
// 两条路的缺省完全相同：没有 ConnectorConfigs 的部署行为与 XM-CRED0
// 之前逐字一致；有的话，同一份缺省成为行缺字段时的兜底。
func (c Config) sub2apiClientFactory() Sub2APIClientFactory {
	defaults := Sub2APIRealConfig{
		Endpoint:        c.Sub2APIEndpoint,
		TargetAllowlist: c.Sub2APITargetAllowlist,
		CredentialRef:   c.Sub2APICredentialRef,
		Environment:     c.Environment,
		InstanceID:      c.Sub2APIInstanceID,
		Timeout:         c.Sub2APIRequestTimeout,
		Secrets:         c.Sub2APISecrets,
	}
	if c.ConnectorConfigs == nil {
		return NewSub2APIClientFactory(c.Sub2APIMode, defaults)
	}
	return NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source:      c.ConnectorConfigs,
		Logger:      c.Logger,
		DefaultMode: c.Sub2APIMode,
		Defaults:    defaults,
	})
}

// newapiClientFactory 是 NewAPI 侧的同款选择，见 sub2apiClientFactory。
func (c Config) newapiClientFactory() NewAPIClientFactory {
	defaults := NewAPIRealConfig{
		Endpoint:        c.NewAPIEndpoint,
		TargetAllowlist: c.NewAPITargetAllowlist,
		CredentialRef:   c.NewAPICredentialRef,
		UserID:          c.NewAPIUserID,
		Environment:     c.Environment,
		InstanceID:      c.NewAPIInstanceID,
		Timeout:         c.NewAPIRequestTimeout,
		Secrets:         c.NewAPISecrets,
	}
	if c.ConnectorConfigs == nil {
		return NewNewAPIClientFactory(c.NewAPIMode, defaults)
	}
	return NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
		Source:      c.ConnectorConfigs,
		Logger:      c.Logger,
		DefaultMode: c.NewAPIMode,
		Defaults:    defaults,
	})
}
