package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
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
	// RequestLogs 为 nil 时「请求」两个端点不挂载（504 之外的 404）。
	//
	// 允许为 nil 而不是必填：这条链路依赖一个**外挂**系统（reqlog），
	// 一个没部署它的环境不该因此起不来。而挂了 nil 却照样注册路由更糟——
	// 那会让端点存在、一调就 500，前端分不清「没接」和「坏了」。
	RequestLogs     RequestLogQuerier
	FinanceAccounts UpstreamAccountLister
	FinanceProfit   ProfitDailyLister
	// FinanceSubscriptions 供订阅成本批次与代理资产的只读端点（XM-0037c）。
	FinanceSubscriptions SubscriptionLister
	RequestTimeout       time.Duration
}

// NewRouter 装配 Platform API 路由。
//
// 探针不需要身份（反代与看门狗要能访问）；/api/v1/* 一律要求 Principal。
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
		api.Use(RequirePrincipal(d.Resolver))
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
	})
	return r
}
