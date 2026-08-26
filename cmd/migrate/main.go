// Command migrate 执行平台数据库迁移。
//
// 这是 Platform Lifecycle Operation（ADR-003 / 规格 §2.4）：不走 Action API，
// 但必须经版本化脚本、变更单、人工批准与独立执行审计。任何模块不得把本命令
// 当作绕过 Action 的写通道。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
)

func main() {
	var (
		databaseURL = flag.String("database", os.Getenv("XM_DATABASE_URL"),
			"PostgreSQL 连接串（默认取环境变量 XM_DATABASE_URL）")
		migrationsPath = flag.String("path", "db/migrations", "迁移目录")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil)).With(
		slog.String("service", "migrate"),
		slog.String("module", "platform-lifecycle"),
		slog.String("build", buildinfo.String()),
	)

	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "up"
	}
	if *databaseURL == "" {
		logger.Error("缺少数据库连接串：用 -database 或环境变量 XM_DATABASE_URL")
		os.Exit(2)
	}

	m, err := migrate.New("file://"+*migrationsPath, migrateURL(*databaseURL))
	if err != nil {
		logger.Error("初始化迁移失败", slog.String("error_code", "migrate_init"), slog.Any("err", err))
		os.Exit(1)
	}
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			logger.Warn("关闭迁移器时出错", slog.Any("src_err", srcErr), slog.Any("db_err", dbErr))
		}
	}()

	switch cmd {
	case "up":
		err = m.Up()
		if errors.Is(err, migrate.ErrNoChange) {
			logger.Info("无待应用迁移")
			err = nil
		}
	case "down":
		// 仅本地开发；生产回滚走备份恢复与前进式修复迁移（规格 §5.7）。
		err = m.Down()
	case "version":
		v, dirty, verErr := m.Version()
		if verErr != nil {
			logger.Error("读取版本失败", slog.Any("err", verErr))
			os.Exit(1)
		}
		fmt.Printf("version=%d dirty=%v\n", v, dirty)
		return
	default:
		logger.Error("未知命令（支持 up|down|version）", slog.String("cmd", cmd))
		os.Exit(2)
	}

	if err != nil {
		logger.Error("迁移失败", slog.String("error_code", "migrate_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("迁移完成", slog.String("cmd", cmd))
}

// migrateURL 把标准 postgres:// 连接串改写为 golang-migrate 的 pgx/v5 驱动
// 注册名 pgx5://。这样全平台（pgxpool、compose、Runbook）只需维护一种
// 连接串写法，驱动差异被限制在本文件内。
func migrateURL(raw string) string {
	for _, scheme := range []string{"postgres://", "postgresql://"} {
		if rest, ok := strings.CutPrefix(raw, scheme); ok {
			return "pgx5://" + rest
		}
	}
	return raw
}
