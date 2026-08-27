// Command platform-api 提供管理后台与内部集成 API（规格 §5.5）。
//
// 本进程不参与用户实时请求路径（ADR-011）：它只服务管理后台与后置的
// 私有集成 API，平台故障不得影响用户 API 中转、支付回调或开票前端。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	// 连接串单独解析：密码走 CredentialRef，明文不进 config、不进日志
	databaseURL, err := databaseURLFromEnv(ctx, os.Getenv, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "database_url_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "database_pool_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()

	// 身份解析器由 XM_AUTH_MODE 决定（dev-header / oidc）。
	// 生产只允许 oidc，且缺 issuer/audience 直接拒绝启动——见 authConfigFromEnv
	resolver, err := newPrincipalResolver(cfg, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "no_principal_resolver"), slog.Any("err", err))
		os.Exit(2)
	}

	actionRegistry := action.NewRegistry()
	registryStore := registry.NewStore(pool)
	if err := registry.RegisterActions(actionRegistry, registryStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 告警的确认与静默是写操作，必须经 Action 内核（宪法 2 条 / ADR-003）。
	// 注册失败即拒绝启动：一个「告警页有按钮但后端没注册动作」的进程，
	// 会让运维在真出事的时候才发现确认键点不动。
	alertStore := alerts.NewStore(pool)
	if err := alerts.RegisterActions(actionRegistry, alertStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 成本登记簿的写操作（登记上游账号、改充值倍率、维护令牌映射）同样
	// 必须经 Action 内核（宪法 2 条 / ADR-003）。注册失败即拒绝启动：
	// 一个「登记簿页面有按钮但后端没注册动作」的进程，会让运维在真要
	// 改倍率的时候才发现保存键点不动。
	financeStore := finance.NewStore(pool)
	if err := finance.RegisterActions(actionRegistry, financeStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 每次 Action 执行（成功或被拒）都进哈希链审计（规格 §4.4）
	auditStore := audit.NewStore(pool)
	kernel := action.NewKernel(
		actionRegistry,
		action.NewPgRunStore(pool, logger),
		action.WithAuditSink(audit.NewActionSink(auditStore)),
		action.WithLogger(logger),
	)
	opsStore := ops.NewStore(pool)

	handler := httpapi.NewRouter(httpapi.Deps{
		Logger:         logger,
		Service:        "platform-api",
		Environment:    cfg.Environment,
		DB:             pool,
		Resolver:       resolver,
		Kernel:         kernel,
		ActionRegistry: actionRegistry,
		Services:       registryStore,
		Metrics:        opsStore,
		// 历史样本复用同一个 Store：最新态与样本是同一个仓储的两张表
		MetricHistory: opsStore,
		// 只读审计视图复用同一个 Store：写入（ActionSink）与读取共用一份
		// 实现，不另开一条访问审计表的路径
		AuditEvents: auditStore,
		Alerts:      alertStore,
		// 登记簿的读与写共用同一个仓储：Query 端点与 Action Handler
		// 不各开一条访问路径
		FinanceAccounts: financeStore,
		RequestTimeout:  cfg.RequestTimeout,
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.RequestTimeout,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("api_listening",
			slog.String("module", "platform.api"),
			slog.String("environment", cfg.Environment),
			slog.String("addr", cfg.ListenAddr),
			slog.String("build", buildinfo.String()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api_serve_failed", slog.String("module", "platform.api"),
				slog.String("error_code", "listen_failed"), slog.Any("err", err))
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("api_shutdown_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "shutdown_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("api_stopped", slog.String("module", "platform.api"))
}
