package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := ParseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		logger.Error("reqlog_recorder_config_invalid", slog.String("error", err.Error()))
		os.Exit(2)
	}

	logger.Info("reqlog_recorder_starting",
		slog.String("data_dir", cfg.DataDir),
		slog.String("listen_newapi", cfg.ListenNewAPI),
		slog.String("upstream_newapi", cfg.UpstreamNewAPI),
		slog.String("listen_sub2api", cfg.ListenSub2API),
		slog.String("upstream_sub2api", cfg.UpstreamSub2API),
		slog.String("tokenmap_path", cfg.TokenMapPath),
		slog.Int("retention_days", cfg.RetentionDays),
		slog.String("dir_perm", cfg.DirPerm.String()),
		slog.String("file_perm", cfg.FilePerm.String()),
		slog.Int("group_id", cfg.GroupID),
	)

	if err := os.MkdirAll(cfg.DataDir, cfg.DirPerm); err != nil {
		logger.Error("reqlog_recorder_data_dir_unavailable",
			slog.String("dir", cfg.DataDir), slog.String("error", err.Error()))
		os.Exit(1)
	}
	chownGroup(logger, cfg.DataDir, cfg.GroupID)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rec := NewRecorder(cfg, logger)
	if err := rec.Run(ctx); err != nil && ctx.Err() == nil {
		// ctx 未被取消却仍然返回了错误：两个监听器中至少一个是真出错
		// （比如端口被占用），不是一次正常的 SIGTERM 收尾——这种情况要
		// 让 systemd 的 Restart=always 接管，所以非零退出。
		logger.Error("reqlog_recorder_fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("reqlog_recorder_stopped")
}
