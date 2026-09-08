package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/integration"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/publishing"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// defaultRequestTimeout 是单请求的默认期限。
const defaultRequestTimeout = 30 * time.Second

// Deps 是路由装配所需的全部依赖。显式传入而非全局变量，便于测试替换。
type Deps struct {
	Logger         *slog.Logger
	Service        string
	Environment    string
	DB             Pinger
	Resolver       PrincipalResolver
	Kernel         ActionExecutor
	ActionRegistry *action.Registry
	Services       ServiceLister
	// Connectors / Connections 是资源目录另外两张表的只读查询
	// （XM-READONLY-QUERIES）。与 Services 同为 *registry.Store，分成三个字段
	// 是因为它们是三个窄接口（见 ConnectorLister / ConnectionLister）。
	//
	// 为 nil 时对应路由不挂载——与 RequestLogs 同一条纪律：端点不存在（404）
	// 比端点存在却一调就 500 诚实。生产装配始终提供（同一个 Store）。
	Connectors  ConnectorLister
	Connections ConnectionLister
	// Migrations 供「版本与发布 → 数据库变更」那一格读已应用的迁移版本
	// （XM-READONLY-QUERIES）。权限是 ops.read，见 migrations.go 的说明。
	//
	// 为 nil 时不挂载：这份读数依赖 public.schema_migrations 的读权限，而库
	// 角色拆分落地后该权限需要显式授予（见交接文档）。没授权的部署应当是
	// 端点不存在，而不是端点存在却每次 500。
	Migrations    MigrationReporter
	Metrics       MetricLister
	MetricHistory MetricHistoryLister
	// Jobs 供「后台任务」页的两个只读端点（XM-JOBS0）：周期任务目录 + 队列
	// 积压 + worker 心跳的快照，以及分页的运行记录。与 Metrics/Alerts 同样
	// 是核心能力，不做 nil 门禁——platform-api 进程总是持有数据库连接池,
	// river_job 是本平台自己数据库里的表，没有「这个环境没接」的情形。
	Jobs        JobsQuerier
	AuditEvents AuditEventLister
	// ActionRuns 供操作与审批页「执行记录」子页签的跨 Action 查询
	// （XM-ACTIONS0）。为 nil 时 /actions/runs* 两个端点不挂载——与
	// RequestLogs/PlatformUsers 同一条纪律：端点不存在（404）比端点存在却
	// 一调就 500 诚实。生产装配应始终提供（kernel 本就依赖同一个 RunStore）。
	ActionRuns ActionRunQuerier
	// ActionRunAudit 供执行记录详情端点关联的审计前后摘要（XM-ACTIONS0）。
	// 与 ActionRuns 分开传入是因为二者的读权限不同（见 ActionRunAuditLookup
	// 的注释）；为 nil 时详情端点不挂载，即便 ActionRuns 非 nil。
	ActionRunAudit ActionRunAuditLookup
	// Approvals / ApprovalExec 是审批中心（XM-0030b）。两个都为 nil 时
	// /approvals* 一概不挂载——与 Foundation-A 的行为一致：没有审批中心时，
	// L2+ 的调用仍然拿 ADVANCED_CONTROLS_REQUIRED，而不是拿到一组一调就 500
	// 的端点。ApprovalExec 单独一个字段是因为触发执行走的是内核而非审批服务。
	Approvals    ApprovalService
	ApprovalExec ApprovalExecutor
	Alerts       AlertLister
	// Silences 是静默窗口的只读列表（XM-SILENCE-LIST）。与 Alerts 同为
	// *alerts.Store，分成两个字段是因为它们是两个窄接口，见 SilenceLister。
	Silences SilenceLister
	// SilenceNow 只为测试而存在：nil 时用 time.Now，生产从不设置它。
	//
	// 静默列表的全部内容就是「拿此刻去比起止时间」，跟着真实时钟走的话，
	// 「生效中 / 未开始 / 已过期」三态的用例会在边界上间歇性变红——那种红
	// 比不红更难查。把时刻做成可注入的，是让这三态能被确定性地钉住。
	SilenceNow func() time.Time
	// Publishing / PublishingDeliver 是「内容发布」页的只读 Query
	// （XM-EXT-PUBLISHING）。为 nil 时整组 /publishing/* 端点不挂载——与
	// RequestLogs 同一条纪律：端点不存在（404）比端点存在却一调就 500 诚实。
	//
	// 写入仍只走 publishing.* Action（宪法 2 条），不在这里开第二条写路径。
	// PublishingDeliver 单独一个字段是因为它回答的不是「库里有什么」而是
	// 「此刻能不能真的发出去」——那个答案来自 Service 的投递器表，不是仓储。
	// 它为 nil 时渠道列表一律按**不能发**渲染（fail closed）。
	Publishing        PublishingQuerier
	PublishingDeliver PublishingDelivery
	// SavedViews 是 Principal/Environment 自隔离的个人表格视图 Query。
	// 写入仍只走 ui.saved_view.* Action，不在这里增加第二条写路径。
	SavedViews SavedViewLister
	// PlatformChannelBindings 是渠道绑定的候选四态与历史 Query；写入仍走 L1 Action。
	PlatformChannelBindings PlatformChannelBindingLister
	// RequestLogs 为 nil 时「请求」两个端点不挂载（504 之外的 404）。
	//
	// 允许为 nil 而不是必填：这条链路依赖一个**外挂**系统（reqlog），
	// 一个没部署它的环境不该因此起不来。而挂了 nil 却照样注册路由更糟——
	// 那会让端点存在、一调就 500，前端分不清「没接」和「坏了」。
	RequestLogs RequestLogQuerier
	// ChannelAssurance 供「渠道保障 · 保障概览 / 历史记录」两个只读端点
	// （XM-ASSURE0 第一片，被动指标）。与 RequestLogs 同一条纪律，甚至更窄
	// ——它只在 reqlog **file 模式**下才有值：这批聚合直接扫描记录代理落盘
	// 的 index.jsonl（见 connectors/reqlog 的 MetricsReader），fake/real 两种
	// 模式都没有可供扫描的真实磁盘数据，为 nil 时两个端点不挂载。
	ChannelAssurance ChannelAssuranceQuerier
	// AssuranceProbes 供"检测任务" / "主动检测历史"两个只读端点（XM-ASSURE1
	// -core，主动探测）。始终有值（本片不像 ChannelAssurance 依赖 reqlog
	// file 模式那样有可选依赖——assurance schema 是本片自带的表，只要
	// core.environment 存在就能挂载），但仍按同一条 nil 网关纪律处理：为 nil
	// 时两个端点不挂载而不是带着 nil 依赖硬跑成 500。
	AssuranceProbes AssuranceProbeQuerier
	// PlatformUsers 为 nil 时「用户管理」端点不挂载（XM-0046）。
	// 与 RequestLogs 同一条纪律：端点不存在（404）比端点存在却一调就 500 诚实。
	PlatformUsers          PlatformUsersQuerier
	PlatformUserDetails    PlatformUserDetailsQuerier
	PlatformUserDailyUsage PlatformUserDailyUsageQuerier
	PlatformUserKeys       PlatformUserKeysQuerier
	// Credentials 为 nil 时凭据登记与连接器配置的三个只读端点不挂载（XM-CRED0）。
	// 与 PlatformUsers 同一条纪律：端点不存在（404）比端点存在却一调就 500 诚实。
	// 写路径（登记 / 轮换 / 吊销 / 切模式）只走 credential.* / connector.* Action。
	Credentials     CredentialQuerier
	FinanceAccounts UpstreamAccountLister
	FinanceProfit   ProfitDailyLister
	// PlatformOrders 为 nil 时"支付与财务"逐笔订单端点不挂载（XM-PAY0）。
	// 与 PlatformUsers 同一条纪律：端点不存在（404）比端点存在却一调就 500
	// 诚实。写路径（无——本片只读）不在这里。
	PlatformOrders PlatformOrdersQuerier
	// ServerAssets/Suppliers/Domains/ServiceNotes 是服务器登记簿的四个只读
	// 查询（XM-SERVER0，拍板「服务器只做记录」——不装 Agent，全部手工登记）。
	// 权限复用 registry.ScopeRead，不新建读侧 scope（见
	// internal/platform/server.ScopeManage 的注释）。
	// Cards 为 nil 时卡片只读端点整组不挂载（XM-CARD3）。与 PlatformOrders
	// 同一条纪律：端点不存在（404）比端点存在却一调就 500 诚实。
	// 写路径只走 cards.card.* Action，这里不开第二条。
	Cards CardQuerier
	// CardAccounts 是已配置的账号清单，供管理端的开卡表单填下拉。
	CardAccounts []string
	// ExtraExpectedCredentials 是运行时才知道的预期凭据引用（卡片账号等），
	// 与 credentials.ExpectedRefs() 的固定清单合并后一起显示在密钥引用页。
	ExtraExpectedCredentials []credentials.ExpectedRef
	// CardSyncInterval 供新鲜度判定；为零时用 5 分钟兜底。
	CardSyncInterval time.Duration
	// SMS / SMSCatalog 是接码中心的读端点。为 nil 时不挂载
	// （XM_SMS_MODE=off），与卡片同一条纪律。
	SMS        SMSQuerier
	SMSCatalog SMSCatalogReader
	// CardStats 是卡片统计的读端点（「统计」页签）。为 nil 时不挂载。
	CardStats CardStatsQuerier
	// SMSExtras 是接码扩展能力（历史/统计/目录/租用报价/邮箱/62 详情与订单）。
	// 为 nil 时不挂载。
	SMSExtras SMSExtrasReader
	// SMSProviders 是**已装配的**供应商清单。以它为准而不是以库里有的行
	// 为准：没做过连接测试的那家库里根本没有行，而它恰恰最需要显示出来。
	SMSProviders []string
	// CardWithdraw 是提现页的读端点（地址白名单与提现历史）。
	//
	// 为 nil 时不挂载那两条路由——与卡片读端点分开判空，因为提现的
	// 权限是独立的 fund.withdraw，两者不该被同一个开关连坐。
	CardWithdraw WithdrawQuerier
	// CardBalances 读各账号的资金池可用余额（实时上游调用，非投影）。
	// 为 nil 时该端点不挂载。
	CardBalances CardBalanceReader
	// CardWebhook 处理 Infini 的卡片回调（XM-CARD4）。
	//
	// 为 nil 时整条回调路由不挂载——没有配回调密钥的部署，这个端点应当
	// 根本不存在，而不是存在但永远返回 401：不存在的端点连被试探的价值
	// 都没有。
	CardWebhook CardWebhookProcessor

	ServerAssets       ServerAssetLister
	ServerSuppliers    ServerSupplierLister
	ServerDomains      ServerDomainLister
	ServerServiceNotes ServerServiceNoteLister
	// ExtApps / ExtAppReleases 是前端应用登记簿与发布记录簿的两个只读查询
	// （XM-EXT-APP）。「应用」= 平台自己纳管的前端站点（ADMIN-IA §5.4.1），
	// 不是被管平台的前端、也不是页面搭建器。权限同样复用 registry.ScopeRead。
	//
	// **这一对里没有发布端点，将来也不该有**：发布与回滚是 Platform Lifecycle
	// Operation（宪法 2、3 条），走版本化脚本 + 人工批准。
	ExtApps        ExtAppLister
	ExtAppReleases ExtAppReleaseLister
	// APIClients / AutomationRules 是「接口与自动化」的两张登记簿
	// （XM-EXT-INTEGRATION）。各自为 nil 时对应端点不挂载——与
	// PlatformUsers 同一条纪律：端点不存在（404）比端点存在却一调就 500 诚实。
	//
	// APIClients 还额外要求 CallerActivity 非 nil：那一格的全部价值在于
	// **对账**（登记簿 × action_run 里实际观测到的调用方），只给登记簿一半
	// 就变成了一张什么也不校验的台账。缺哪一半都不挂，比挂一个只有半边
	// 事实的端点诚实。
	APIClients      APIClientLister
	CallerActivity  CallerAggregator
	AutomationRules AutomationRuleLister
	// IntegrationNow 只为测试注入固定时钟（观测窗口的起点由它算）；
	// nil 时用 time.Now，生产从不设置它。
	IntegrationNow func() time.Time
	// FinanceSubscriptions 供订阅成本批次与代理资产的只读端点（XM-0037c）。
	FinanceSubscriptions SubscriptionLister
	// FinanceSummaries 供看板的渠道 / 上游摘要（XM-0037d，§8.5 + §13）。
	FinanceSummaries FinanceSummaryLister
	// FinanceRunwayThresholds 是旧摘要处理器的静态兼容参数（XM-0049）。
	//
	// 生产装配必须提供 FinanceRunwayConfig，让 API 每个请求从
	// finance.runway_threshold_config 读取 DB 快照；本字段只为旧嵌入者和测试
	// 保留，不能再理解成从 env 注入的运行时真相，也不能作为 DB 缺行 fallback。
	FinanceRunwayThresholds finance.RunwayThresholds
	// FinanceRunwayConfig 是按环境版本化的运行时阈值快照（XM-C-RUNWAY0）。
	// nil 时不挂载专用 current/history Query；旧的 summary 端点仍使用上面的
	// 静态兼容字段，便于分阶段切换。正式进程若未迁移应在装配/健康检查层阻断，
	// 而不是把默认值呈现为可信 DB 结果。
	FinanceRunwayConfig        RunwayThresholdProvider
	FinanceRunwayConfigHistory RunwayThresholdHistoryLister
	FinanceRunwayPreviewSource RunwayPreviewSource
	RequestTimeout             time.Duration
	// RateLimit 是 /api/v1 的限流参数（XM-R011）。零值走默认配额。
	//
	// **没有「关掉」这个选项**：一个能被关掉的限流在出事那天多半是关着的。
	// 要放宽就把 PerMinute 调大，那是一个看得见的数字。
	RateLimit RateLimitConfig
	// LocalAuth 为 nil 时本地登录端点（XM-LOGIN，/api/v1/auth/* 与
	// /api/v1/staff/accounts）不挂载——只有 XM_AUTH_MODE=local 时才会有值。
	// 与 RequestLogs/PlatformUsers 同一条纪律：端点不存在（404）比端点存在
	// 却拿一个 nil 依赖硬跑更诚实。
	LocalAuth LocalAuthHandlers
	// ConsoleAssertion 为 nil 时断言签发端点（CR-0006/XM-INVCON1，
	// POST /api/v1/auth/console-assertion）不挂载——只有
	// XM_INVOICE_CONSOLE_ASSERTION_ENABLED=true 时才会有值。与 LocalAuth
	// 同一条纪律：端点不存在（404）比端点存在却拿一个未装配的签名器硬跑
	// 更诚实。挂载位置在 RequirePrincipal 组内（要求 xm_session + CSRF
	// 头），不额外声明 RequireScope——finance.read/IP 名单/TOTP 新鲜度三重
	// 校验需要各自返回不同的错误码（FINANCE_SCOPE_REQUIRED/
	// ADMIN_NETWORK_DENIED/ADMIN_STEP_UP_REQUIRED），比通用 RequireScope
	// 中间件统一回 PERMISSION_DENIED 更精确，因此校验放在 Handler 内部，
	// 与 localauth.Handlers 自身端点的既有做法一致。
	ConsoleAssertion ConsoleAssertionHandlers
	// OpsConnectorConfigs 供运行保障页的「控制平面健康」子页展示每条同步
	// 任务的生效模式（XM-OPS0）。为 nil 时该字段在响应里如实报告
	// config_available=false，而不是猜一个模式——与 Credentials 为 nil 时
	// 三个凭据端点整体不挂载是同一条纪律的另一种表达：这里端点仍然存在
	// （心跳/连接器健康/数据库探针不依赖凭据模块），只是缺一列数据。
	OpsConnectorConfigs ConnectorConfigLister
	// OpsAlertDelivery 是运行保障页展示的告警投递配置状态（只回布尔值，
	// 不回引用或地址；见 AlertDeliveryStatus 的注释）。
	OpsAlertDelivery AlertDeliveryStatus
	// CPAKeys 为 nil 时 /platforms/cpa/keys 不挂载（XM-CPA0，
	// XM_CPA_MODE!=file）。与 PlatformUsers 同一条纪律：端点不存在（404）
	// 比端点存在却一调就 500 诚实。CPA 不复用 PlatformUsers/PlatformUserKeys
	// 那组依赖——它的"用户"是 API key 而不是终端用户身份，走的是完全不同的
	// 只读连接器（connectors/cpa），不是 platformusers 域。
	CPAKeys CPAKeysQuerier
}

// NewRouter 装配 Platform API 路由。
//
// 探针不需要身份（反代与看门狗要能访问）；/api/v1/* 一律要求 Principal，
// 唯一的例外是本地登录（XM-LOGIN）的 /auth/login 与 /auth/logout——登录本身
// 就是在建立身份，不可能先要求一个还不存在的身份。
func NewRouter(d Deps) http.Handler {
	timeout := d.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(RequestID)
	// Logging 要排在 Recover 之前：panic 恢复时也该用注入的 logger，
	// 而不是掉回 slog.Default()。
	r.Use(Logging(logger))
	r.Use(Recover(logger))
	r.Use(AccessLog(logger, d.Service, d.Environment))
	r.Use(Timeout(timeout))

	r.Get("/healthz", HealthHandler())
	r.Get("/readyz", ReadyHandler(d.DB))

	// Infini 卡片回调（XM-CARD4）。**刻意挂在 /api/v1 之外**：它不带
	// Principal、不吃 RequirePrincipal，唯一的闸是 HMAC 签名。放进 /api/v1
	// 会让它继承那一组的鉴权中间件——而上游没有我们的会话，那样它永远
	// 进不来；更糟的是有人以后往那一组加中间件时，会以为这个端点也被保护着。
	if d.CardWebhook != nil {
		r.Post("/webhooks/infini/{account}", CardWebhookHandler(
			d.CardWebhook, d.CardAccounts, nil, logger))
	}

	r.Route("/api/v1", func(api chi.Router) {
		// /api/v1 下每一条响应都是按 Principal 与环境裁剪过的数据——审计前后
		// 镜像、收入余额、服务清单——没有一条可以被共享缓存或浏览器落盘
		// （XM-0031，回归 Codex 冷审 PR #47 第 8 条）。装在整组上而不是逐个
		// 端点：新加的端点自动继承，不靠作者记得。探针不在这一组，保持可缓存。
		api.Use(NoStore)

		// 本地登录（XM-LOGIN）的开放端点：登录/登出不要求 Principal——它们
		// 本身就是在建立/终止身份，不可能先经过 RequirePrincipal。挂在下面
		// 的 Group 之外，因此不吃 RequirePrincipal，也不吃共享的 RateLimit
		// （登录有自己按 IP 分桶的限流，见 localauth.Handlers.Login）。
		// LocalAuth 为 nil（未运行在 local 模式）时整组不挂载。
		if d.LocalAuth != nil {
			api.Post("/auth/login", d.LocalAuth.Login)
			// /auth/login/totp（XM-AUTH-TOTP0）同组挂在 RequirePrincipal 之外：
			// 它服务两种调用形态，其一（携带 temp_token 完成登录第二步）本身
			// 也是在建立身份；另一种（步进刷新，不带 temp_token）由 LoginTOTP
			// 内部直接读 Cookie 校验，不复用这里的中间件。
			api.Post("/auth/login/totp", d.LocalAuth.LoginTOTP)
			api.Post("/auth/logout", d.LocalAuth.Logout)
		}

		api.Group(func(api chi.Router) {
			api.Use(RequirePrincipal(d.Resolver))
			// 限流（XM-R011）**装在 RequirePrincipal 之后**：桶键要用已解析的
			// 身份，而不是调用方声称的那个。装反了等于让伪造者换个 Header 就
			// 换一个新桶。探针不在这一组，天然豁免——靠装配位置，不靠豁免名单。
			api.Use(RateLimit(NewRateLimiter(d.RateLimit), logger))
			// d.Approvals 兼作「审批中心接上了没有」的信号：它与内核的
			// WithApprovalGateway 在 cmd/platform-api 里由同一个 service 装配，
			// 两者要么都在要么都不在（见上面 Approvals 字段的注释）。
			api.Get("/actions", ListActionsHandler(d.ActionRegistry, d.Approvals != nil))
			api.Post("/actions/{actionID}/versions/{version}/execute", ExecuteActionHandler(d.Kernel))
			// 读也要权限（规格 §2.4）。声明在路由上，让路由表成为
			// 「哪个端点要什么权限」的单一清单
			api.With(RequireScope(registry.ScopeRead)).
				Get("/services", ListServicesHandler(d.Services))
			// 资源目录的另外两张表（XM-READONLY-QUERIES），**复用
			// registry.ScopeRead**：服务 / 连接器 / 连接是同一份「平台管着哪些
			// 系统、用哪种实现连、连成了几条」的知识面，能看第一张就该看得见
			// 另外两张（同 /servers/* 复用它的理由）。
			//
			// ⚠️ 与下面凭据模块的 /connectors/config 不是同一个东西：那一条读
			// 的是「连接器跑在 fake 还是 real 模式」（credentials 包，权限
			// connector.manage），这里读的是 core.connector 登记簿。路径相邻
			// 但分属两个模块，改动时别当成一族。
			//
			// 写路径不在这里：connector.create 是 L2、connection.create 是 L3、
			// connection.set_status 是 L2，一律走 Action 端点并经审批中心裁决。
			if d.Connectors != nil {
				api.With(RequireScope(registry.ScopeRead)).
					Get("/connectors", ListConnectorsHandler(d.Connectors))
			}
			if d.Connections != nil {
				api.With(RequireScope(registry.ScopeRead)).
					Get("/connections", ListConnectionsHandler(d.Connections))
			}
			api.With(RequireScope(ops.ScopeRead)).
				Get("/metrics", ListMetricsHandler(d.Metrics))
			// 历史样本与最新态同属运营指标，共用 ops.read
			api.With(RequireScope(ops.ScopeRead)).
				Get("/metrics/history", ListMetricHistoryHandler(d.MetricHistory))
			// 后台任务（XM-JOBS0）同样复用 ops.read：周期任务目录、队列积压、
			// worker 心跳与运行记录本质上是运营可观测性数据，泄漏面与
			// /metrics 相同——都是「系统跑得怎么样」，不是业务数据。
			api.With(RequireScope(ops.ScopeRead)).
				Get("/jobs/overview", JobsOverviewHandler(d.Jobs))
			api.With(RequireScope(ops.ScopeRead)).
				Get("/jobs/runs", ListJobRunsHandler(d.Jobs))
			// 运行保障页「控制平面健康」子页（XM-OPS0），同样复用 ops.read：
			// 心跳/连接器健康/保留期清理都是 ops 观测的另一种投影，泄漏面
			// 与 /metrics 相同。
			api.With(RequireScope(ops.ScopeRead)).
				Get("/ops/overview", OpsOverviewHandler(OpsOverviewDeps{
					Observations:     d.Metrics,
					ConnectorConfigs: d.OpsConnectorConfigs,
					DB:               d.DB,
					AlertDelivery:    d.OpsAlertDelivery,
				}))
			// 数据库变更（XM-READONLY-QUERIES）同样复用 ops.read：迁移版本
			// 回答「这套部署自己处在什么状态」，与心跳、队列积压、控制平面
			// 健康同一类运行保障知识面。它**不归 registry.read**——那一族说的
			// 是「平台管着哪些被管系统」，迁移是平台自己的运行事实。
			//
			// 挂在 /ops/ 下而不是顶层 /migrations：与上面 /ops/overview 同一
			// 个族，路径本身就说清了它属于运行保障而不是资源目录。
			//
			// 只读。执行迁移与回滚是 Platform Lifecycle Operation（宪法 2、3
			// 条），走版本化脚本 + 变更单 + 人工批准，不经 Action，更不经 HTTP。
			if d.Migrations != nil {
				api.With(RequireScope(ops.ScopeRead)).
					Get("/ops/migrations", ListMigrationsHandler(d.Migrations))
			}
			// audit.read 单独授予：审计事件带前后摘要，敏感度高于 ops.read
			api.With(RequireScope(audit.ScopeRead)).
				Get("/audit/events", ListAuditEventsHandler(d.AuditEvents))
			// 跨 Action 执行记录（操作与审批页「执行记录」子页签，XM-ACTIONS0）。
			// 列表只需 action.ScopeRead；详情再叠加 audit.ScopeRead，因为它
			// 附带审计事件里的 before/after 摘要（见 action.ScopeRead 的注释）。
			// 两个字段任一为 nil 时不挂载：详情端点还依赖 ActionRunAudit。
			if d.ActionRuns != nil {
				api.With(RequireScope(action.ScopeRead)).
					Get("/actions/runs", ListActionRunsHandler(d.ActionRuns))
				if d.ActionRunAudit != nil {
					api.With(RequireScope(action.ScopeRead)).
						With(RequireScope(audit.ScopeRead)).
						Get("/actions/runs/{runID}", GetActionRunHandler(d.ActionRuns, d.ActionRunAudit))
				}
			}
			// 审批中心（XM-0030b）。
			//
			// **没有 POST /approvals**：单由内核代落——一次 L2+ 的
			// POST /actions/{id}/versions/{v}/execute 被受理时返回 202 + 单号
			// （设计稿 §4 的「由内核代落，一般不直调」）。单独开一个建单端点
			// 就得在这一层把权限/环境/Schema 再判一遍，否则谁都能往队列里灌单
			// ——那正是 XM-0030a-wire 挪风险闸堵掉的洞，不该在这里重新开一个。
			//
			// 执行端点不挂 RequireScope：要什么权限取决于单上那个 Action，
			// 只有内核知道（见 ExecuteApprovalHandler 的注释）。
			if d.Approvals != nil {
				api.With(RequireScope(approval.ScopeRead)).
					Get("/approvals", ListApprovalsHandler(d.Approvals))
				api.With(RequireScope(approval.ScopeRead)).
					Get("/approvals/{approvalID}", GetApprovalHandler(d.Approvals))
				api.With(RequireScope(approval.ScopeDecide)).
					Post("/approvals/{approvalID}/decide", DecideApprovalHandler(d.Approvals))
				// 撤回只要 read：撤的是自己的单，归属由领域层按 requester_id 判。
				// 要求 decide 会把「提交人撤回自己的单」变成需要审批权，说不通。
				api.With(RequireScope(approval.ScopeRead)).
					Post("/approvals/{approvalID}/cancel", CancelApprovalHandler(d.Approvals))
				if d.ApprovalExec != nil {
					api.Post("/approvals/{approvalID}/execute", ExecuteApprovalHandler(d.ApprovalExec))
				}
			}
			// 告警与指标共用 ops.read：告警内容就是指标的判读结果，
			// 泄漏面完全相同（见 alerts.ScopeRead 的注释）。
			// 写路径（确认、静默）不在这里——它们是 Action，走
			// POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
			api.With(RequireScope(alerts.ScopeRead)).
				Get("/alerts", ListAlertsHandler(d.Alerts))
			// 静默窗口的只读列表（XM-SILENCE-LIST）。同一个 scope：窗口正文
			// （规则 + 理由 + 按的人 + 起止）比告警正文泄漏面更小。建窗口
			// 仍走 Action（alerts.silence.manage），不在这里开第二条写路径。
			api.With(RequireScope(alerts.ScopeRead)).
				Get("/alerts/silences", ListSilencesHandler(d.Silences, d.SilenceNow))
			// 内容发布（XM-EXT-PUBLISHING）。五个只读端点共用
			// publishing.read：草稿正文、素材地址、渠道登记（**只回显
			// CredentialRef，不是明文**）与发布记录属于同一份内容资产，
			// 拆成多个读 scope 只会多出几个会漏授的授权面。
			//
			// 写路径不在这里——草稿/素材/渠道/发布全部是 Action，走
			// POST /api/v1/actions/{id}/versions/{v}/execute，权限与风险
			// 等级由内核裁决（发布是 L3，会落审批单）。
			if d.Publishing != nil {
				api.With(RequireScope(publishing.ScopeRead)).
					Get("/publishing/drafts", ListPublishingDraftsHandler(d.Publishing))
				api.With(RequireScope(publishing.ScopeRead)).
					Get("/publishing/drafts/{draftID}", GetPublishingDraftHandler(d.Publishing))
				api.With(RequireScope(publishing.ScopeRead)).
					Get("/publishing/assets", ListPublishingAssetsHandler(d.Publishing))
				api.With(RequireScope(publishing.ScopeRead)).
					Get("/publishing/channels",
						ListPublishingChannelsHandler(d.Publishing, d.PublishingDeliver))
				api.With(RequireScope(publishing.ScopeRead)).
					Get("/publishing/records", ListPublishingRecordsHandler(d.Publishing))
			}
			if d.Cards != nil {
				interval := d.CardSyncInterval
				if interval <= 0 {
					interval = 5 * time.Minute
				}
				api.With(RequireScope(cards.PermissionRead)).
					Get("/cards", ListCardsHandler(d.Cards, d.CardAccounts, interval))
				api.With(RequireScope(cards.PermissionRead)).
					Get("/cards/{cardID}/transactions", ListCardTransactionsHandler(d.Cards))
				// 跨卡流水（「交易记录」页签）。路径不带 cardID，与上面那条
				// 靠形状区分而不是靠参数缺省——chi 的路由树里 /cards/transactions
				// 会先于 /cards/{cardID}/transactions 匹配，因为静态段优先。
				api.With(RequireScope(cards.PermissionRead)).
					Get("/cards/transactions", ListAllCardTransactionsHandler(d.Cards))
				// 待人工处置的操作单列一个端点：它是红条的数据源，
				// 前端要能在不拉全量卡片的情况下轮询它。
				api.With(RequireScope(cards.PermissionRead)).
					Get("/cards/operations/attention",
						ListCardOperationsNeedingAttentionHandler(d.Cards))
				// 资金池余额：实时上游调用，不是投影。为 nil 时不挂载
				// （fake 模式下没有真实余额可读）。
				api.With(RequireScope(cards.PermissionRead)).
					Get("/cards/challenges", ListCardChallengesHandler(d.Cards))
				// 统计：服务端整表聚合。与流水端点分开，因为它的代价不同，
				// 而且前端对截断列表求和会给出一个偏小却看着正常的数。
				if d.CardStats != nil {
					api.With(RequireScope(cards.PermissionRead)).
						Get("/cards/stats", CardStatsHandler(d.CardStats))
				}
				if d.CardBalances != nil {
					api.With(RequireScope(cards.PermissionRead)).
						Get("/cards/balances", CardBalancesHandler(d.CardBalances, d.CardAccounts))
				}
				// 提现的两条读端点由 fund.withdraw 把守，**不是 card.read**。
				//
				// 地址清单回的是完整转账地址，提现历史回的是逐笔资金流向；
				// 两者都不该因为「能看卡」就顺带能看。持有 card.read 的人
				// 看不见提现页，也就不会以为自己该有那个按钮。
				if d.CardWithdraw != nil {
					api.With(RequireScope(cards.PermissionWithdraw)).
						Get("/cards/withdraw/addresses", ListWithdrawAddressesHandler(d.CardWithdraw))
					api.With(RequireScope(cards.PermissionWithdraw)).
						Get("/cards/withdrawals", ListWithdrawalsHandler(d.CardWithdraw))
					// 额度用 card.read 而不是 fund.withdraw：它是一个上限
					// 数字，不泄漏地址也不泄漏资金流向，而「这个账号的提现
					// 上限是多少」是运营看板上该有的信息。改它才要
					// fund.limit.manage（Action 自己把守）。
					api.With(RequireScope(cards.PermissionRead)).
						Get("/cards/withdraw/limits", ListWithdrawLimitsHandler(d.CardWithdraw))
				}
			}
			// 接码中心（XM-SMS0）。
			//
			// 读端点由 sms.read 把守；**号码与验证码的可见性在 handler 内
			// 按 sms.reveal 再判一次**——它们与卡面明文同档，而清单本身
			// 是运营日常要看的。两道闸分开是为了让「能看清单」与「能看号码」
			// 可以分别授予。
			if d.SMS != nil {
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/providers", ListSMSProvidersHandler(d.SMS, d.SMSProviders))
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/resources", ListSMSResourcesHandler(d.SMS))
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/operations", ListSMSOperationsHandler(d.SMS))
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/resources/{resourceID}/codes", ListSMSCodesHandler(d.SMS))
				// 路由规则（XM-SMS2 #5）：读是 sms.read，改走 sms.routing.* Action。
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/routing", ListSMSRoutingRulesHandler(d.SMS, d.SMSProviders))
				// 余额快照（XM-SMS2 #7）：巡检任务写的，页面只读。
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/balances", ListSMSBalancesHandler(d.SMS))
				// 内部告警与阈值（XM-SMS2 #8）：**不外发**，只给页面红条。
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/alerts", ListSMSAlertsHandler(d.SMS))
				// 成本统计（XM-SMS3 #3）：库里聚合，页面只画。
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/costs", ListSMSCostsHandler(d.SMS, nil))
				// 消费者配额（XM-SMS4 #3）：读在这儿，写走 sms.quota.set。
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/quotas", ListSMSQuotasHandler(d.SMS, nil))
			}
			// 库存是实时上游调用，与投影读分开判空：fake 模式下没有真实库存。
			if d.SMSCatalog != nil {
				api.With(RequireScope(sms.PermissionRead)).
					Get("/sms/catalog", ListSMSCatalogHandler(d.SMSCatalog))
				if d.SMSExtras != nil {
					mountSMSExtras(api, d.SMS, d.SMSExtras)
				}
			}
			api.With(RequireScope(savedviews.ScopeManage)).
				Get("/ui/saved-views", ListSavedViewsHandler(d.SavedViews))
			api.With(RequireScope(finance.ScopeRead)).
				Get("/finance/platform-channel-bindings",
					ListPlatformChannelBindingsHandler(d.PlatformChannelBindings, d.Metrics))
			// 渠道级投影同时需要 ops.read（目录/健康）与 finance.read（绑定/经营归属）。
			// 两个 RequireScope 都保留，让缺哪一项能在响应里明确说出来。
			api.With(RequireScope(ops.ScopeRead)).
				With(RequireScope(finance.ScopeRead)).
				Get("/platforms/{platform}/channels",
					ListPlatformChannelsHandler(d.PlatformChannelBindings, d.Metrics))

			// 请求详情（XM-0039）。两条端点、两个权限，分级是这条能力的前提：
			// 元数据列表回答「这个人用得多不多」，正文回答「这个人问了什么」。
			// 交接文档 §9.4 把后者列为高敏数据，所以它不是 request.read 的
			// 一个子页面，而是另一次授权。
			//
			// 没有配 reqlog 的部署整组不挂载：端点不存在（404）比端点存在却
			// 一调就 500 诚实——前端据此分得清「没接」和「坏了」。
			if d.RequestLogs != nil {
				api.With(RequireScope(requestlog.ScopeRead)).
					Get("/platforms/{platform}/requests", ListPlatformRequestsHandler(d.RequestLogs))
				api.With(RequireScope(requestlog.ScopeContentRead)).
					Get("/platforms/{platform}/requests/{requestID}",
						GetPlatformRequestContentHandler(d.RequestLogs))
			}

			// 渠道保障 · 保障概览 / 历史记录（XM-ASSURE0 第一片，被动指标）。
			// 复用 requestlog.ScopeRead（request.read）而不是新开 scope——这批
			// 数据是同一条 reqlog 磁盘来源的聚合视图，泄漏面与「请求」列表端点
			// 相同（都是元数据聚合，不含正文），团队交接明确要求能复用就不新增。
			//
			// 没有配 reqlog file 模式的部署整组不挂载：与 RequestLogs 同一条
			// 纪律，端点不存在（404）比端点存在却一调就 500 诚实。
			if d.ChannelAssurance != nil {
				api.With(RequireScope(requestlog.ScopeRead)).
					Get("/platforms/{platform}/assurance/overview",
						GetPlatformAssuranceOverviewHandler(d.ChannelAssurance))
				api.With(RequireScope(requestlog.ScopeRead)).
					Get("/platforms/{platform}/assurance/history",
						GetPlatformAssuranceHistoryHandler(d.ChannelAssurance))
			}

			// 渠道保障 · 检测任务 / 主动检测历史（XM-ASSURE1-core，主动探测）。
			// 复用 requestlog.ScopeRead 同一个理由：这批数据的敏感度与被动
			// 指标同级（都是"这个平台今天调用多不多/健不健康"，不含 Prompt
			// 具体输出），团队交接明确要求能复用就不新增。**这两个端点与上面
			// 两个（保障概览/历史记录，被动）是完全独立的两组数据源**——
			// 主动探测的历史来自 assurance.probe_result，不与被动统计共用
			// 任何 metric_key 命名空间（ADR-019 决策·六）。
			if d.AssuranceProbes != nil {
				api.With(RequireScope(requestlog.ScopeRead)).
					Get("/platforms/{platform}/assurance/probes",
						GetPlatformAssuranceProbesHandler(d.AssuranceProbes))
				api.With(RequireScope(requestlog.ScopeRead)).
					Get("/platforms/{platform}/assurance/probe-history",
						GetPlatformAssuranceProbeHistoryHandler(d.AssuranceProbes))
			}

			// 被管平台的终端用户清单（XM-0046）。**不复用 ops.read**：
			// ops.read 看到的是聚合数字（平台有多少用户、总余额多少），这里是
			// **逐用户**的资金明细——即便邮箱已经在契约层打了码，一份逐用户清单
			// 也足以还原一家客户的经营规模，与 request.read 同一档
			// （见 platformusers.ScopeRead 的注释）。
			//
			// 没有配用户连接器的部署不挂载这条：前端据此分得清「没接」和「坏了」。
			if d.PlatformUsers != nil {
				// 精确详情是 v2 的可选 capability；没有 Reader 时保持端点不存在，
				// 不把"未接入"伪装成空用户或运行时 500。
				if d.PlatformUserDetails != nil {
					api.With(RequireScope(platformusers.ScopeRead)).
						Get("/platforms/{platform}/users/{userID}", GetPlatformUserHandler(d.PlatformUserDetails))
				}
				if d.PlatformUserDailyUsage != nil {
					api.With(RequireScope(platformusers.ScopeRead)).
						Get("/platforms/{platform}/users/{userID}/daily-usage", GetPlatformUserDailyUsageHandler(d.PlatformUserDailyUsage))
				}
				if d.PlatformUserKeys != nil {
					api.With(RequireScope(platformusers.ScopeKeyMetadataRead)).
						Get("/platforms/{platform}/users/{userID}/keys", ListPlatformUserKeysHandler(d.PlatformUserKeys))
				}
				api.With(RequireScope(platformusers.ScopeRead)).
					Get("/platforms/{platform}/users", ListPlatformUsersHandler(d.PlatformUsers))
			}

			// 支付与财务：逐笔订单查询（XM-PAY0）。复用 finance.read——这里的
			// 每一笔订单就是财务台账、渠道摘要那些聚合数字的**来源**，能看
			// 聚合数的人已经知道量级，逐笔明细的泄漏面与既有 finance.read
			// 端点相同（见 finance.ScopeRead 在本文件其余用法的同款理由）。
			//
			// 没有配支付连接器的部署不挂载这条：前端据此分得清「没接」和「坏了」。
			if d.PlatformOrders != nil {
				api.With(RequireScope(finance.ScopeRead)).
					Get("/platforms/{platform}/orders", ListPlatformOrdersHandler(d.PlatformOrders))
				// 订单详情（XM-PAY1）：复用同一个 Querier，见
				// GetPlatformOrderHandler 顶部注释。
				api.With(RequireScope(finance.ScopeRead)).
					Get("/platforms/{platform}/orders/{id}", GetPlatformOrderHandler(d.PlatformOrders))
			}

			// CPA 逐 key 用量（XM-CPA0）。路径写死 "cpa" 而不是 {platform}：
			// 这不是给全体被管平台复用的通用族——数据源是 connectors/cpa
			// 直读的 usage.sqlite，不经 platformusers 那套 Service/Client
			// 装配（CPA 没有终端用户身份，只有 API key，platformusers.
			// ParseSource 明确不认它，见 ScopeCPAKeysRead 的注释）。
			//
			// 没有配 CPA 文件后端的部署不挂载：端点不存在（404）比端点存在
			// 却一调就 500 诚实。
			if d.CPAKeys != nil {
				api.With(RequireScope(ScopeCPAKeysRead)).
					Get("/platforms/cpa/keys", ListCPAKeysHandler(d.CPAKeys))
			}

			// 凭据登记与连接器配置（XM-CRED0）。两个 scope **都不复用 finance.read**：
			// 清单里只有引用、指纹与可用性，但能看到「哪把 token 缺、哪条通道还是
			// fake」的人已经知道平台的接入盲区在哪；而对应的写 Action 会把明文写进
			// SecretProvider 目录、把 worker 切到真实上游——授权面必须独立。
			// 值本身永远不经过任何端点：只有 SecretProvider 碰得到它（宪法 7 条）。
			if d.Credentials != nil {
				api.With(RequireScope(credentials.ScopeManage)).
					Get("/credentials", ListCredentialsHandler(d.Credentials))
				api.With(RequireScope(credentials.ScopeManage)).
					Get("/credentials/expected",
						ListExpectedCredentialsHandler(d.Credentials, d.ExtraExpectedCredentials))
				api.With(RequireScope(credentials.ScopeConnectorManage)).
					Get("/connectors/config", ListConnectorConfigsHandler(d.Credentials))
			}

			// 成本登记簿**不复用 ops.read**：它列的是每个上游账号的凭据引用、
			// 充值倍率与令牌映射。倍率是商业条款（我们从上游拿到几折），
			// 映射是成本归属的对账键，两样都比看板上的余额数字敏感一个量级
			// （见 finance.ScopeRead 的注释）。
			// 写路径（登记、改倍率、维护映射）不在这里——它们是 L1 Action，
			// 走 POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
			api.With(RequireScope(finance.ScopeRead)).
				Get("/finance/upstream-accounts", ListUpstreamAccountsHandler(d.FinanceAccounts))

			// 服务器登记簿（XM-SERVER0）。四张纯登记表，复用 registry.ScopeRead：
			// 能看服务清单的人本就该能看服务器登记簿——两者都是「平台管着哪些
			// 基础设施」这同一类知识面，泄漏面相当。写路径（登记/修改/退役/
			// 删除）不在这里——它们是 L1 Action，走
			// POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
			api.With(RequireScope(registry.ScopeRead)).
				Get("/servers/assets", ListServerAssetsHandler(d.ServerAssets))
			api.With(RequireScope(registry.ScopeRead)).
				Get("/servers/suppliers", ListServerSuppliersHandler(d.ServerSuppliers))
			api.With(RequireScope(registry.ScopeRead)).
				Get("/servers/domains", ListServerDomainsHandler(d.ServerDomains))
			api.With(RequireScope(registry.ScopeRead)).
				Get("/servers/service-notes", ListServerServiceNotesHandler(d.ServerServiceNotes))

			// 前端应用登记簿与发布记录簿（XM-EXT-APP）。同样复用
			// registry.ScopeRead：能看服务清单与服务器登记簿的人本就该能看
			// 「我们自己有哪些前端站点」，三者是同一类知识面。
			//
			// 写路径（登记 / 修改 / 下线 / 记录发布）不在这里——三个 L1
			// Action，走执行通道，权限是独立的 extapp.manage。
			// **没有发布端点**：这两条只回答「登记了什么」，不做任何事。
			api.With(RequireScope(registry.ScopeRead)).
				Get("/ext/apps", ListExtAppsHandler(d.ExtApps))
			api.With(RequireScope(registry.ScopeRead)).
				Get("/ext/apps/releases", ListExtAppReleasesHandler(d.ExtAppReleases))
			// 「接口与自动化」的两张登记簿（XM-EXT-INTEGRATION，ADMIN-IA §5.4.1）。
			//
			// 读侧用**新 scope** integration.read 而不是复用 registry.read：
			// 后者默认发给 staff，而调用方登记簿是一张授权面的地图
			// （谁该来调我们、期望持有哪些 scope），看板角色不该顺带拿到
			// （见 internal/platform/integration.ScopeRead 的注释）。
			//
			// 写路径（四个 L1 Action）不在这里——走
			// POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
			// **这两条路由都不会触发任何 Action**：调用方那条只读两张表，
			// 规则那条只拿注册表查声明、不拿 handler。
			if d.APIClients != nil && d.CallerActivity != nil {
				api.With(RequireScope(integration.ScopeRead)).
					Get("/integration/api-clients",
						ListAPIClientsHandler(d.APIClients, d.CallerActivity, d.IntegrationNow))
			}
			if d.AutomationRules != nil {
				api.With(RequireScope(integration.ScopeRead)).
					Get("/integration/automation-rules",
						ListAutomationRulesHandler(d.AutomationRules, d.ActionRegistry))
			}

			// 利润台账（XM-0037b）**复用 finance.read**，不另立一个 scope：
			// 台账里的毛利就是「倍率 × 用量」的结果，能看登记簿里那个倍率的人
			// 已经能推出毛利的量级，泄漏面完全相同（对照 alerts.ScopeRead 复用
			// ops.read 的理由）。为它单独发一个 scope 只会多一处要维护的授权，
			// 换不来任何实际隔离。
			//
			// 台账**没有写路径**：它只由采集任务写（§8.1），回填历史是
			// Platform Lifecycle Operation（宪法 2 条），不是一个 API。
			api.With(RequireScope(finance.ScopeRead)).
				Get("/finance/profit-daily", ListProfitDailyHandler(d.FinanceProfit))

			// 订阅成本批次与代理资产（XM-0037c）同样复用 finance.read：
			// 它们里的金额就是订阅型渠道成本的**来源**，能看台账里那个成本的人
			// 已经知道它的量级，泄漏面完全相同。代理的 credential_ref 只出引用
			// （ADR-014、UI 交接 §14.2），明文一步都不进库。
			//
			// 写路径（登记、退款、终止）不在这里——它们是 L1 Action，
			// 走 POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
			api.With(RequireScope(finance.ScopeRead)).
				Get("/finance/subscription-batches", ListSubscriptionBatchesHandler(d.FinanceSubscriptions))
			api.With(RequireScope(finance.ScopeRead)).
				Get("/finance/proxy-assets", ListProxyAssetsHandler(d.FinanceSubscriptions))

			// 看板供数（XM-0037d，§8.5 + UI 交接 §13）。同样复用 finance.read：
			// 这里的每一个数都是台账的向上聚合，能看台账的人已经能自己加出来。
			//
			// 两个端点当前是同一个粒度（一个 upstream_account 一行），差别在投影
			// ——渠道看**钱**，上游看**供给**（余额 / 可用天数 / 充值成本率）。
			// 理由见 internal/platform/finance/summary.go 顶部。
			if d.FinanceRunwayConfig != nil {
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/channels/summary",
						ListChannelSummaryHandlerWithProvider(d.FinanceSummaries, d.FinanceRunwayConfig))
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/upstreams/summary",
						ListUpstreamSummaryHandlerWithProvider(d.FinanceSummaries, d.FinanceRunwayConfig))
			} else {
				// Legacy compatibility only. The platform-api main path always sets
				// FinanceRunwayConfig; retaining this branch avoids breaking older
				// embedders/tests while making the DB cutover explicit above.
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/channels/summary",
						ListChannelSummaryHandler(d.FinanceSummaries, d.FinanceRunwayThresholds))
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/upstreams/summary",
						ListUpstreamSummaryHandler(d.FinanceSummaries, d.FinanceRunwayThresholds))
			}
			if d.FinanceRunwayConfig != nil {
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/runway-thresholds", GetRunwayThresholdHandler(d.FinanceRunwayConfig))
			}
			if d.FinanceRunwayConfigHistory != nil {
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/runway-thresholds/history", ListRunwayThresholdHistoryHandler(d.FinanceRunwayConfigHistory))
			}
			if d.FinanceRunwayConfig != nil && d.FinanceRunwayPreviewSource != nil && d.Alerts != nil {
				api.With(RequireScope(finance.ScopeRead)).
					Get("/finance/runway-thresholds/preview", PreviewRunwayThresholdHandler(
						d.FinanceRunwayConfig, d.FinanceRunwayPreviewSource, d.Alerts))
			}

			// 本地登录（XM-LOGIN）的受保护端点：/me 与 /password 只要求已登录
			// （不需要额外 scope，任何本地账号都能查自己、改自己的密码）；
			// /staff/accounts 是账号管理清单，要求 staff.manage——与
			// staff.account.* 系列 Action 共用同一个 scope（见
			// internal/platform/localauth/permissions.go）。
			if d.LocalAuth != nil {
				api.Get("/auth/me", d.LocalAuth.Me)
				api.Post("/auth/password", d.LocalAuth.ChangePassword)
				api.With(RequireScope(localAuthScopeManage)).
					Get("/staff/accounts", d.LocalAuth.ListAccounts)
				api.Post("/auth/totp/enroll", d.LocalAuth.EnrollTOTP)
				api.Post("/auth/totp/confirm", d.LocalAuth.ConfirmTOTP)
				// TOTP 自助启用/确认（XM-AUTH-TOTP0，CR-0006 技术规格 §5.1 只为
				// 这两个开了专用端点）：作用对象只能是调用者自己，权限由各自的
				// Action Schema（staff.account.enroll_totp/confirm_totp，
				// Permission=staff.manage）在内核里裁决，路由层不重复声明
				// RequireScope——与既有的 /actions/.../execute 通用执行入口
				// 同一条纪律，避免同一份权限判定分两处维护。
				//
				// 管理员重置他人 TOTP（staff.account.reset_totp）**没有**专用
				// 端点：技术规格没有为它单开一条，且它与 staff.account.
				// reset_password 同一种"管理员改别人账号"的形状——两者统一走
				// 上面已经注册的通用 /actions/{id}/versions/{version}/execute
				// 入口即可，不必每加一个管理员动作就新开一条路由。
			}
			if d.ConsoleAssertion != nil {
				api.Post("/auth/console-assertion", d.ConsoleAssertion.Issue)
			}
		})
	})
	return r
}
