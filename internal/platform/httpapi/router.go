package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
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
	RequestTimeout time.Duration
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
		api.Use(RequirePrincipal(d.Resolver))
		api.Get("/actions", ListActionsHandler(d.ActionRegistry))
		api.Post("/actions/{actionID}/versions/{version}/execute", ExecuteActionHandler(d.Kernel))
		// 读也要权限（规格 §2.4）。声明在路由上，让路由表成为
		// 「哪个端点要什么权限」的单一清单
		api.With(RequireScope(registry.ScopeRead)).
			Get("/services", ListServicesHandler(d.Services))
		api.With(RequireScope(ops.ScopeRead)).
			Get("/metrics", ListMetricsHandler(d.Metrics))
	})
	return r
}
