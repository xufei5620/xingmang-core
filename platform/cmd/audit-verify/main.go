// Command audit-verify 校验审计哈希链的完整性（规格 §4.4「恢复演练验证审计链」）。
//
// 这是 Platform Lifecycle Operation（ADR-003）：只读、不修改任何数据，
// 但属于运维工具而非业务 Action。
//
// 退出码：0 = 链完好；1 = 发现断链（输出断点）；2 = 参数或连接错误。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
)

func main() {
	var (
		databaseURL = flag.String("database", os.Getenv("XM_DATABASE_URL"),
			"PostgreSQL 连接串（默认取环境变量 XM_DATABASE_URL）")
		from = flag.Int64("from", 1, "起始 sequence")
		to   = flag.Int64("to", 0, "结束 sequence（0 表示校验到链尖）")
	)
	flag.Parse()

	if *databaseURL == "" {
		fmt.Fprintln(os.Stderr, "缺少数据库连接串：用 -database 或环境变量 XM_DATABASE_URL")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接数据库失败: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	store := audit.NewStore(pool)
	tipSeq, tipHash, err := store.Tip(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取链尖失败: %v\n", err)
		os.Exit(2)
	}
	if tipSeq == 0 {
		fmt.Printf("%s\n审计链为空，无需校验\n", buildinfo.String())
		return
	}
	end := *to
	if end <= 0 || end > tipSeq {
		end = tipSeq
	}

	problem, err := store.VerifyChain(ctx, *from, end)
	if err != nil {
		fmt.Fprintf(os.Stderr, "校验过程出错: %v\n", err)
		os.Exit(2)
	}
	if problem != nil {
		fmt.Fprintf(os.Stderr, "审计链校验失败：%s\n", problem)
		fmt.Fprintln(os.Stderr, "这是事故：请先保全现场（勿改动数据库），再按 RUNBOOK 排查。")
		os.Exit(1)
	}

	fmt.Printf("%s\n审计链完好：区间 [%d, %d]，链尖哈希 %s\n",
		buildinfo.String(), *from, end, tipHash)
}
