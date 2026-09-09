package shadow_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgreadonly"
	"github.com/xufei5620/xingmang-platform/internal/platform/shadow"
)

// 本文件跑在**真库**上，补的是纯函数测试补不到的那一格：
// `soloaiProfitQuery` 从来没有被真正的 PostgreSQL 解析过。
//
// 这不是理论风险：那条 SQL 里有 `::text` 转型、`$1::date` 参数、GROUP BY 与
// 表名列名——任何一处抄错，单元测试全绿，而工具会在 14 天倒计时的**第一天**
// 报「读 SoloAI 台账失败」。倒计时是按自然日算的，第一天丢掉就是丢掉。
//
// 库结构按 SoloAI 迁移 0091 建（字段逐字抄自
// K:/soloai/soloai-v2-src/migrations/0091_relay_profit_daily.up.sql）——
// 测的是我们的 SQL 对不对得上**那张表**，所以表必须长成那样。

// soloaiFixtureSchema 是 relay_profit_daily 的最小复刻。
//
// 只建本读取器真的会读的列 + 主键。station_id 保留（并进主键）是有意的：
// 它证明「跨 station 聚合」那条注释不是空话——同一个 account 挂在两个 station
// 上时，两条行必须被合并成一格。
const soloaiFixtureSchema = `
DROP SCHEMA IF EXISTS xm_soloai_fixture CASCADE;
CREATE SCHEMA xm_soloai_fixture;
CREATE TABLE xm_soloai_fixture.relay_profit_daily (
    station_id TEXT    NOT NULL,
    day        DATE    NOT NULL,
    token_id   TEXT    NOT NULL,
    account_id TEXT    NOT NULL,
    revenue    NUMERIC NOT NULL DEFAULT 0,
    cost       NUMERIC NOT NULL DEFAULT 0,
    PRIMARY KEY (station_id, day, token_id)
);`

func soloaiFixture(t *testing.T) (writable *pgxpool.Pool, url string) {
	t.Helper()
	url = strings.TrimSpace(os.Getenv("XM_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, soloaiFixtureSchema); err != nil {
		t.Fatalf("建夹具库结构失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS xm_soloai_fixture CASCADE")
	})
	return pool, url
}

// readerOn 用夹具 schema 建一个只读读取器。
//
// search_path 指到夹具 schema：读取器的 SQL 写的是裸表名 relay_profit_daily
// （生产上那个 DSN 连的就是 SoloAI 自己的库，裸表名本来就对）。
// 闸 2/闸 3 与生产路径挂的是同一份代码。
func readerOn(t *testing.T, rawURL string) *shadow.SoloAIReader {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		t.Fatalf("解析测试 DSN 失败: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "xm_soloai_fixture"
	cfg.ConnConfig.RuntimeParams[pgreadonly.ReadOnlyParam] = "on"
	cfg.AfterConnect = pgreadonly.VerifyReadOnly

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("建只读池失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("只读池 Ping 失败: %v", err)
	}
	return shadow.NewSoloAIReaderFromPool(pool)
}

// TestSoloAIQueryRunsOnRealPostgres：那条 SQL 必须真的能被 PostgreSQL 执行。
//
// 顺带钉住三件只有真库才验得出来的事：跨 station 聚合、NUMERIC 经文本折分、
// 以及日期区间是**闭区间**。
func TestSoloAIQueryRunsOnRealPostgres(t *testing.T) {
	writable, url := soloaiFixture(t)
	ctx := context.Background()

	rows := []struct {
		station, day, token, account, revenue, cost string
	}{
		// 同一个 account 挂在两个 station 上：必须被合并成一格。
		{"st-a", "2026-08-27", "tok-1", "acc-1", "12.34", "5.00"},
		{"st-b", "2026-08-27", "tok-2", "acc-1", "0.11", "1.00"},
		// 半分边界：0.005 + 0.005 = 0.01 —— 先 SUM 再折分得 1 分；
		// 各自折分再相加会得到 2 分（两个 0.5 分各自进位）。
		{"st-a", "2026-08-27", "tok-3", "acc-2", "0.005", "0.005"},
		{"st-a", "2026-08-27", "tok-4", "acc-2", "0.005", "0.005"},
		// 窗口外的一天：不该被读进来。
		{"st-a", "2026-08-26", "tok-1", "acc-1", "999.99", "999.99"},
		{"st-a", "2026-08-29", "tok-1", "acc-1", "888.88", "888.88"},
	}
	for _, r := range rows {
		if _, err := writable.Exec(ctx,
			`INSERT INTO xm_soloai_fixture.relay_profit_daily
			 (station_id, day, token_id, account_id, revenue, cost)
			 VALUES ($1, $2::date, $3, $4, $5::numeric, $6::numeric)`,
			r.station, r.day, r.token, r.account, r.revenue, r.cost); err != nil {
			t.Fatalf("塞夹具数据失败: %v", err)
		}
	}

	reader := readerOn(t, url)
	day := func(s string) time.Time {
		parsed, err := time.Parse(shadow.DayLayout, s)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	got, err := reader.Rows(ctx, day("2026-08-27"), day("2026-08-27"))
	if err != nil {
		t.Fatalf("读 SoloAI 台账失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 格（acc-1、acc-2）: %+v", got)
	}

	byAccount := map[string]shadow.Row{}
	for _, r := range got {
		byAccount[r.AccountID] = r
	}

	// acc-1 跨两个 station：12.34 + 0.11 = 12.45 → 1245 分；成本 6.00 → 600 分。
	acc1 := byAccount["acc-1"]
	if !acc1.Revenue.Known || acc1.Revenue.Cents != 1245 {
		t.Fatalf("acc-1 收入 = %+v, want 1245 分（跨 station 要合并）", acc1.Revenue)
	}
	if !acc1.Cost.Known || acc1.Cost.Cents != 600 {
		t.Fatalf("acc-1 成本 = %+v, want 600 分", acc1.Cost)
	}
	if acc1.RowCount != 2 {
		t.Fatalf("acc-1 明细条数 = %d, want 2", acc1.RowCount)
	}

	// acc-2 的半分边界：先 SUM 再折分 = 1 分。各自折分再相加会得到 2 分——
	// 这条钉住「SQL 里不 round、折分只在 Go 里做一次」那个决定。
	acc2 := byAccount["acc-2"]
	if acc2.Revenue.Cents != 1 {
		t.Fatalf("acc-2 收入 = %d 分, want 1（0.005+0.005 先加再折）", acc2.Revenue.Cents)
	}

	// 口径元数据按 §4 的 ★ 常量填（SoloAI 表里没有这两列）。
	if acc1.Currency != shadow.DefaultCurrency || acc1.BusinessDayTZ != shadow.DefaultBusinessDayTZ {
		t.Fatalf("口径元数据不对: %+v", acc1)
	}

	// 区间是闭区间，且窗口外的两天一条都不该进来。
	wide, err := reader.Rows(ctx, day("2026-08-26"), day("2026-08-29"))
	if err != nil {
		t.Fatal(err)
	}
	days := map[string]bool{}
	for _, r := range wide {
		days[r.Day] = true
	}
	for _, want := range []string{"2026-08-26", "2026-08-27", "2026-08-29"} {
		if !days[want] {
			t.Fatalf("闭区间应含 %s: %+v", want, days)
		}
	}
}

// TestSoloAIReaderServerRejectsWrites：只读纪律的真实证据。
//
// 代码里没有写路径（那由 TestSoloAIReaderHasNoWritePath 证明），
// 但那只证明「我们没打算写」。这条证明「就算写了也写不进去」——
// 连的是 SoloAI 的生产库时，这才是唯一算数的保证。
func TestSoloAIReaderServerRejectsWrites(t *testing.T) {
	_, url := soloaiFixture(t)

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams[pgreadonly.ReadOnlyParam] = "on"
	cfg.AfterConnect = pgreadonly.VerifyReadOnly
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ctx := context.Background()
	for _, sql := range []string{
		"CREATE TABLE xm_soloai_fixture.should_not_exist (id int)",
		"DELETE FROM xm_soloai_fixture.relay_profit_daily",
		"UPDATE xm_soloai_fixture.relay_profit_daily SET revenue = 0",
	} {
		_, err := pool.Exec(ctx, sql)
		if err == nil {
			t.Fatalf("只读连接上竟然执行成功了: %s", sql)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "read-only") {
			t.Fatalf("拒绝原因应是只读事务, got %v（语句: %s）", err, sql)
		}
	}
}

// TestSoloAIReaderRefusesWritableConnection：闸 3 真的在拦。
//
// 不带只读启动参数的池会被 AfterConnect 拒掉——防的是「启动参数被连接池
// 中间件吞掉」：那种情况下连接会**看起来正常**而实际可写。
func TestSoloAIReaderRefusesWritableConnection(t *testing.T) {
	_, url := soloaiFixture(t)

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = pgreadonly.VerifyReadOnly // 故意不设只读启动参数
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err == nil {
		t.Fatal("服务端可写时必须拒绝使用该连接（闸 3）")
	} else if !strings.Contains(err.Error(), "只读复核不通过") {
		t.Fatalf("拒绝原因应是只读复核不通过, got %v", err)
	}
}
