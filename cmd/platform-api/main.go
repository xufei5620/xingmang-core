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
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
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

	resolver, err := httpapi.NewDevHeaderResolver(cfg.Environment)
	if err != nil {
		// 生产环境走到这里说明还没接 OIDC（XM-0008）——不允许无鉴权启动
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
		RequestTimeout: cfg.RequestTimeout,
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
