package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews"
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
	Metrics        MetricLister
	MetricHistory  MetricHistoryLister
	AuditEvents    AuditEventLister
	Alerts         AlertLister
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
	r.Use(Recover(logger))
	r.Use(AccessLog(logger, d.Service, d.Environment))
	r.Use(Timeout(timeout))

	r.Get("/healthz", HealthHandler())
	r.Get("/readyz", ReadyHandler(d.DB))

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
			api.Post("/auth/logout", d.LocalAuth.Logout)
		}

		api.Group(func(api chi.Router) {
			api.Use(RequirePrincipal(d.Resolver))
			// 限流（XM-R011）**装在 RequirePrincipal 之后**：桶键要用已解析的
			// 身份，而不是调用方声称的那个。装反了等于让伪造者换个 Header 就
			// 换一个新桶。探针不在这一组，天然豁免——靠装配位置，不靠豁免名单。
			api.Use(RateLimit(NewRateLimiter(d.RateLimit), logger))
			api.Get("/actions", ListActionsHandler(d.ActionRegistry))
			api.Post("/actions/{actionID}/versions/{version}/execute", ExecuteActionHandler(d.Kernel))
			// 读也要权限（规格 §2.4）。声明在路由上，让路由表成为
			// 「哪个端点要什么权限」的单一清单
			api.With(RequireScope(registry.ScopeRead)).
				Get("/services", ListServicesHandler(d.Services))
			api.With(RequireScope(ops.ScopeRead)).
				Get("/metrics", ListMetricsHandler(d.Metrics))
			// 历史样本与最新态同属运营指标，共用 ops.read
			api.With(RequireScope(ops.ScopeRead)).
				Get("/metrics/history", ListMetricHistoryHandler(d.MetricHistory))
			// audit.read 单独授予：审计事件带前后摘要，敏感度高于 ops.read
			api.With(RequireScope(audit.ScopeRead)).
				Get("/audit/events", ListAuditEventsHandler(d.AuditEvents))
			// 告警与指标共用 ops.read：告警内容就是指标的判读结果，
			// 泄漏面完全相同（见 alerts.ScopeRead 的注释）。
			// 写路径（确认、静默）不在这里——它们是 Action，走
			// POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
			api.With(RequireScope(alerts.ScopeRead)).
				Get("/alerts", ListAlertsHandler(d.Alerts))
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
					Get("/credentials/expected", ListExpectedCredentialsHandler(d.Credentials))
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
			}
		})
	})
	return r
}
