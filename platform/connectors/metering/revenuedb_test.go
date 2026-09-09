package metering

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// ---------------------------------------------------------------------------
// 闸 4：包内无写路径
// ---------------------------------------------------------------------------
func TestRevenueDBQueriesAreSelectOnly(t *testing.T) {
	if len(revenueDBQueries) == 0 {
		t.Fatal("语句清单为空——这条测试就没有意义了")
	}
	for _, q := range revenueDBQueries {
		if err := assertSelectOnly(q); err != nil {
			t.Fatalf("语句未通过只读判据:\n%s\n%v", q, err)
		}
	}
}

// TestRevenueDBSourceHasNoWritePath 是闸 4 的**源码级**证明。
//
// 前两条测试证明「登记了的语句是只读的」，证明不了「没有别的路径绕过登记」。
// 这条直接扫本文件的源码：pgx 上任何能写的入口（Exec / CopyFrom / SendBatch）
// 一个都不许出现，而唯一允许直接碰池的地方是 querySelectOnly——
// 它会先跑 assertSelectOnly。
//
// 有人将来图省事写一句 `d.pool.Exec(ctx, "UPDATE …")`，编译得过、
// 前两条测试照绿，这一条会红。
func TestRevenueDBSourceHasNoWritePath(t *testing.T) {
	src, err := os.ReadFile("revenuedb.go")
	if err != nil {
		t.Fatalf("读源码失败: %v", err)
	}
	text := string(src)

	// pgx 上会写库的入口。注释里出现这些词是允许的，所以按「带左括号的调用形态」匹配。
	for _, forbidden := range []string{".Exec(", ".CopyFrom(", ".SendBatch(", ".Begin("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("revenuedb.go 出现了可写入口 %q——只读通道上不该有它（ADR-018 闸 4）", forbidden)
		}
	}

	// 直接碰池的地方只允许有一处，且必须在 querySelectOnly 里。
	const poolQuery = "d.pool.QueryRow("
	if got := strings.Count(text, poolQuery); got != 1 {
		t.Fatalf("%s 出现 %d 次，want 1——所有取数都必须经 querySelectOnly（它先跑 assertSelectOnly）", poolQuery, got)
	}
	fnStart := strings.Index(text, "func (d *NewAPIRevenueDB) querySelectOnly(")
	if fnStart < 0 {
		t.Fatal("找不到 querySelectOnly——它是本通道唯一的发语句入口")
	}
	if strings.Index(text, poolQuery) < fnStart {
		t.Fatalf("%s 出现在 querySelectOnly 之外", poolQuery)
	}
}

func TestOpenNewAPIRevenueDBNotConfigured(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		db, err := OpenNewAPIRevenueDB(context.Background(),
			RevenueDBConfig{DSN: dsn}, nil)
		if db != nil || err != nil {
			t.Fatalf("没配 DSN 应返回 (nil, nil), got (%v, %v)", db, err)
		}
	}
}

// TestOpenNewAPIRevenueDBRejectsInlinePassword：口令只能经 CredentialRef。
//
// 走 pgdsn.Validate 而不是自己解析 URL：pgx 会把 query 参数也当连接设置读，
// 且**在**填完 host/user 之后覆盖它们——`?password=` 与 `?host=` 都是真实的
// 绕过口子（见 internal/platform/pgdsn 的包注释）。
func TestOpenNewAPIRevenueDBRejectsInlinePassword(t *testing.T) {
	ctx := context.Background()
	for _, dsn := range []string{
		"postgres://reader:inline-secret@db.example.test/newapi",
		"postgres://reader@db.example.test/newapi?password=inline-secret",
	} {
		_, err := OpenNewAPIRevenueDB(ctx,
			RevenueDBConfig{DSN: dsn, PasswordRef: "secret://newapi/revenue-db"}, nil)
		if err == nil {
			t.Fatalf("带内联口令的 DSN 必须被拒: %s", dsn)
		}
		if connector.KindOf(err) != connector.KindInternal {
			t.Fatalf("配置错误应归 internal（是我们的部署配置问题）, got %q", connector.KindOf(err))
		}
	}

	// 配了 DSN 却没配引用：同样是配置错误，而且要说得出缺什么。
	_, err := OpenNewAPIRevenueDB(ctx,
		RevenueDBConfig{DSN: "postgres://reader@db.example.test/newapi"}, nil)
	if err == nil {
		t.Fatal("没配 PasswordRef 必须被拒")
	}
	if !strings.Contains(err.(*connector.Error).Unwrap().Error(), "CredentialRef") {
		t.Fatalf("错误应说清口令要走 CredentialRef: %v", errors.Unwrap(err))
	}
}

// ---------------------------------------------------------------------------
// 业务日切分
// ---------------------------------------------------------------------------

// TestDayWindowUnixIsHalfOpenCST：业务日按 CST(+08:00) 切，区间半开。
//
// 半开区间是有实际后果的：闭区间下次日零点整那一秒会同时落进两天，
// 一个只在极少数秒里发生、几乎不可能被复现的重复计数。
//
// CST 固定 +08:00 无夏令时，是设计稿 §4 的 ★ 口径常量——**收入与成本必须
// 共用同一个时间权威**，各切各的会让同一笔请求的收入记在 D 日、成本记在
// D+1 日，利润凭空多一天又少一天，而且不报错。
func TestDayWindowUnixIsHalfOpenCST(t *testing.T) {
	cst := time.FixedZone("CST", cstOffsetSeconds)

	start, end, err := dayWindowUnix("2026-08-28", nil) // nil → 默认 CST
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 8, 28, 0, 0, 0, 0, cst).Unix()
	wantEnd := time.Date(2026, 8, 29, 0, 0, 0, 0, cst).Unix()
	if start != wantStart || end != wantEnd {
		t.Fatalf("窗口 = [%d, %d), want [%d, %d)", start, end, wantStart, wantEnd)
	}
	if end-start != 86400 {
		t.Fatalf("窗口长度 = %d 秒, want 86400", end-start)
	}
	// CST 零点比 UTC 零点早 8 小时——这条防的是「默认时区被改成 UTC」那类改动。
	utcStart := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC).Unix()
	if utcStart-start != 8*3600 {
		t.Fatalf("CST 零点应比 UTC 零点早 8 小时, 实际差 %d 秒", utcStart-start)
	}

	// 相邻两天首尾相接、不重叠：前一天的 end 恰好是后一天的 start。
	_, prevEnd, err := dayWindowUnix("2026-08-27", nil)
	if err != nil {
		t.Fatal(err)
	}
	if prevEnd != start {
		t.Fatalf("前一天的 end=%d 应恰好等于后一天的 start=%d（不重不漏）", prevEnd, start)
	}

	// 显式传时区时必须真的生效（账号可以声明自己的业务日时区，§4）。
	utcStartGot, _, err := dayWindowUnix("2026-08-28", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if utcStartGot != utcStart {
		t.Fatalf("传 UTC 时窗口起点 = %d, want %d——时区参数没被用上", utcStartGot, utcStart)
	}
}

// ---------------------------------------------------------------------------
// 取数的入参纪律（不需要真库）
// ---------------------------------------------------------------------------

func TestAccountRevenueRejectsBadInput(t *testing.T) {
	// 没有池 = 通道没配起来：报 not_supported，与「没配 DSN」同一种表达。
	var unset *NewAPIRevenueDB
	if _, err := unset.AccountRevenue(context.Background(), "1", "2026-08-28"); connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("未配置通道应报 not_supported, got %q", connector.KindOf(err))
	}

	// 有池但入参非法：不该发出任何查询，直接归 bad_response。
	// 用一个未连接的池即可——真发了查询会超时而不是立刻返回。
	cfg, err := newAPIRevenueConfig("postgres://reader:pw@127.0.0.1:1/newapi")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := NewAPIRevenueDBFromPool(pool, RevenueDBConfig{})

	if _, err := db.AccountRevenue(context.Background(), "  ", "2026-08-28"); connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("空 own_account_id 应归 bad_response, got %q", connector.KindOf(err))
	}
	for _, day := range []string{"", "2026-13-01", "20260828", "2026/08/28", "2026-8-1", "yesterday"} {
		_, err := db.AccountRevenue(context.Background(), "1", day)
		if connector.KindOf(err) != connector.KindBadResponse {
			t.Fatalf("非法业务日 %q 应归 bad_response, got %q（err=%v）", day, connector.KindOf(err), err)
		}
		if !errors.Is(err, ErrInvalidDay) {
			t.Fatalf("非法业务日 %q 的根因应可被 errors.Is 认出: %v", day, err)
		}
	}
}

// TestNewAPIRevenueDBDefaults：币种与业务日时区的默认值不能是零值。
//
// 币种决定钱的含义，时区决定钱算在哪一天——两个都错得静悄悄。
func TestNewAPIRevenueDBDefaults(t *testing.T) {
	cfg, err := newAPIRevenueConfig("postgres://reader:pw@127.0.0.1:1/newapi")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	db := NewAPIRevenueDBFromPool(pool, RevenueDBConfig{})
	if db.currency != defaultRevenueCurrency {
		t.Fatalf("默认币种 = %q, want %q（quota 的换算口径是美元）", db.currency, defaultRevenueCurrency)
	}
	_, offset := time.Now().In(db.businessDay).Zone()
	if offset != cstOffsetSeconds {
		t.Fatalf("默认业务日时区偏移 = %d 秒, want %d（★ 口径常量 CST +08:00）",
			offset, cstOffsetSeconds)
	}

	// 显式声明要能覆盖默认值。
	custom := NewAPIRevenueDBFromPool(pool, RevenueDBConfig{
		Currency: "cny", BusinessDay: time.UTC,
	})
	if custom.currency != "CNY" {
		t.Fatalf("币种应被规范成大写, got %q", custom.currency)
	}
	if custom.businessDay != time.UTC {
		t.Fatalf("业务日时区没被覆盖: %v", custom.businessDay)
	}
}
