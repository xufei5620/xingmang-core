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

// TestAssertSelectOnly 钉住只读语句判据。
//
// 白名单（只放行 SELECT）而不是黑名单：黑名单要枚举 INSERT/UPDATE/DELETE/
// TRUNCATE/COPY/CREATE/DROP/ALTER/GRANT/DO/CALL/MERGE…，漏一个就是一个写口子。
// 下面这串正是「黑名单会漏掉哪些」的清单——它们**全部**必须被拒。
func TestAssertSelectOnly(t *testing.T) {
	for _, ok := range []string{
		"SELECT 1",
		"select value from options",
		"  \n SELECT COALESCE(SUM(quota),0)::bigint\n FROM quota_data",
		"-- 注释在前\nSELECT 1",
	} {
		if err := assertSelectOnly(ok); err != nil {
			t.Fatalf("assertSelectOnly(%q) 应放行, got %v", ok, err)
		}
	}

	for _, bad := range []string{
		"",
		"   ",
		"INSERT INTO quota_data VALUES (1)",
		"UPDATE options SET value = '1'",
		"DELETE FROM quota_data",
		"TRUNCATE quota_data",
		"DROP TABLE quota_data",
		"CREATE TABLE t (id int)",
		"ALTER TABLE quota_data ADD COLUMN x int",
		"GRANT ALL ON quota_data TO public",
		"COPY quota_data FROM '/tmp/x'",
		"DO $$ BEGIN PERFORM 1; END $$",
		"CALL some_proc()",
		"MERGE INTO t USING s ON true",
		"WITH x AS (DELETE FROM quota_data RETURNING *) SELECT * FROM x",
		// 多语句拼接是最经典的绕过：首词是 SELECT，第二条才是写。
		"SELECT 1; DROP TABLE quota_data",
		"SELECT 1;",
		// 注释掩护：真正的首词是 UPDATE
		"-- SELECT 1\nUPDATE options SET value='1'",
	} {
		err := assertSelectOnly(bad)
		if err == nil {
			t.Fatalf("assertSelectOnly(%q) 必须被拒", bad)
		}
		if !errors.Is(err, ErrRevenueDBWriteAttempt) {
			t.Fatalf("assertSelectOnly(%q) 的根因应可被 errors.Is 认出: %v", bad, err)
		}
	}
}

// TestRevenueDBQueriesAreSelectOnly：本通道会发的每一条 SQL 都必须是只读的。
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

// ---------------------------------------------------------------------------
// 闸 1 / 闸 2：连接配置
// ---------------------------------------------------------------------------

// TestNewAPIRevenueConfigForcesReadOnly：闸 2 的启动包参数必须被写进去。
func TestNewAPIRevenueConfigForcesReadOnly(t *testing.T) {
	cfg, err := newAPIRevenueConfig("postgres://reader:pw@db.example.test:5432/newapi")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.RuntimeParams[readOnlyParam]; got != "on" {
		t.Fatalf("%s = %q, want on（闸 2：启动包强制只读）", readOnlyParam, got)
	}
	// 闸 3 必须挂上：池是懒建连接的，只在开池时查一次会漏掉后续新建的连接。
	if cfg.AfterConnect == nil {
		t.Fatal("AfterConnect 为空——每条新连接的只读复核（闸 3）没挂上")
	}
	// 别人家的生产库，连接数保守到底。
	if cfg.MaxConns != revenuePoolMaxConns {
		t.Fatalf("MaxConns = %d, want %d", cfg.MaxConns, revenuePoolMaxConns)
	}
}

// TestNewAPIRevenueConfigRejectsExplicitReadWrite：显式要求可写的连接串必须被拒，
// **而不是被静默改成只读**。
//
// 配置者是带着意图写下 off 的。静默改写会让那个意图（以及它背后的误解——
// 比如「我以为这个通道也要写」）永远没有机会被发现。
func TestNewAPIRevenueConfigRejectsExplicitReadWrite(t *testing.T) {
	for _, dsn := range []string{
		"postgres://reader:pw@db.example.test/newapi?" + readOnlyParam + "=off",
		"postgres://reader:pw@db.example.test/newapi?" + readOnlyParam + "=false",
		// 塞在 options 里的那种也要认：pgx 把 options 当不透明字符串原样发给
		// 服务端，我们在 RuntimeParams 上写的 on 并不保证能覆盖它。
		"postgres://reader:pw@db.example.test/newapi?options=-c%20" + readOnlyParam + "%3Doff",
	} {
		_, err := newAPIRevenueConfig(dsn)
		if !errors.Is(err, ErrRevenueDBReadWriteRequested) {
			t.Fatalf("显式可写的连接串必须被拒（dsn=%s）, got %v", dsn, err)
		}
	}

	// 显式写 on 是可以的：它与我们要做的事一致，没有需要暴露的误解。
	if _, err := newAPIRevenueConfig(
		"postgres://reader:pw@db.example.test/newapi?" + readOnlyParam + "=on"); err != nil {
		t.Fatalf("显式 on 不该被拒: %v", err)
	}
}

func TestRequestsReadWrite(t *testing.T) {
	cases := []struct {
		params map[string]string
		want   bool
	}{
		{map[string]string{}, false},
		{map[string]string{readOnlyParam: "on"}, false},
		{map[string]string{readOnlyParam: "off"}, true},
		{map[string]string{readOnlyParam: "0"}, true},
		{map[string]string{readOnlyParam: "false"}, true},
		{map[string]string{"options": "-c " + readOnlyParam + "=off"}, true},
		{map[string]string{"options": "-c " + readOnlyParam + "=on"}, false},
		{map[string]string{"options": "-c statement_timeout=5s"}, false},
	}
	for _, tc := range cases {
		if got := requestsReadWrite(tc.params); got != tc.want {
			t.Fatalf("requestsReadWrite(%v) = %v, want %v", tc.params, got, tc.want)
		}
	}
}

// TestOpenNewAPIRevenueDBNotConfigured：没配 DSN = 功能未启用，**不是错误**。
//
// 这条锁住 XM-0044 最重要的兼容性承诺：没配的部署行为与本任务之前逐字相同
// （收入侧 not_supported、台账写 NULL）。
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
// 凭据遮罩
// ---------------------------------------------------------------------------

// TestScrubError：pgx 的连接错误会带整条连接串，而这些错误会进结构化日志。
func TestScrubError(t *testing.T) {
	if got := scrubError(nil); got != "" {
		t.Fatalf("nil 应回空串, got %q", got)
	}
	cases := []struct {
		in       string
		mustHide string
		mustKeep string
	}{
		{
			in:       `failed to connect to postgres://reader:hunter2@db.example.test:5432/newapi`,
			mustHide: "hunter2",
			// 主机与库名要留着——整条抹掉等于把一个可修的配置错误变成黑箱。
			mustKeep: "db.example.test",
		},
		{
			in:       `cannot connect: host=db.example.test password=hunter2 dbname=newapi`,
			mustHide: "hunter2",
			mustKeep: "dbname=newapi",
		},
	}
	for _, tc := range cases {
		got := scrubError(errors.New(tc.in))
		if strings.Contains(got, tc.mustHide) {
			t.Fatalf("口令泄漏: %s", got)
		}
		if !strings.Contains(got, tc.mustKeep) {
			t.Fatalf("排障信息被抹掉了（应保留 %q）: %s", tc.mustKeep, got)
		}
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
