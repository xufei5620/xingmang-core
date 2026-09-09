package metering_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 本文件跑在**真库**上。收入侧只读直查有三件事只有真 PostgreSQL 才验得出来：
//
//  1. **服务端真的拒绝写。** 启动包参数只是「我请求了只读」，
//     `cannot execute … in a read-only transaction` 才是证据。用假货复刻
//     只会测到假货——而这条通道连的是别人家的生产计费库，
//     「绝不写」不能只靠代码自律。
//  2. **SUM 与半开区间的边界。** 落在窗口首尾各一秒的两行，一行该算一行不该算；
//     这类差一秒的错误在内存假货上永远暴露不出来。
//  3. **quota → 微美元的整数换算在真数据上逐位对得上**（§2.4/§3.2）。
//
// 库结构按上游 new-api 的 quota_data / options 建（字段名逐字抄自
// K:/newapi-src 的 model/usedata.go 与 model/option.go）——测的是我们的 SQL
// 对不对得上**那个**表，所以表必须长成那样。

// newapiFixtureSchema 是上游两张表的最小复刻。
//
// 只建本通道真的会读的列。多建几列不会让测试更强，只会让「我们以为上游长这样」
// 与「上游实际长这样」的差别更难看出来。
const newapiFixtureSchema = `
DROP SCHEMA IF EXISTS xm_newapi_fixture CASCADE;
CREATE SCHEMA xm_newapi_fixture;
CREATE TABLE xm_newapi_fixture.quota_data (
    id         bigserial PRIMARY KEY,
    channel_id bigint  NOT NULL,
    created_at bigint  NOT NULL,
    quota      bigint  NOT NULL,
    model_name text    NOT NULL DEFAULT ''
);
CREATE TABLE xm_newapi_fixture.options (
    key   text PRIMARY KEY,
    value text NOT NULL
);`

// revenueFixtureDay 是被查询的业务日。固定值：跟着真实时钟走的话，
// 这套测试会在某个未来的日子突然变红，而且红得毫无道理。
const revenueFixtureDay = "2026-08-28"

// writablePool 给夹具建表用。**与被测的只读池是两条连接**——
// 只读池按定义建不了表，而夹具必须建得出来。
func writablePool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("XM_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, url
}

// seedRevenueFixture 建表并塞进边界数据，返回业务日窗口的首尾 unix 秒。
func seedRevenueFixture(t *testing.T, pool *pgxpool.Pool) (start, end int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, newapiFixtureSchema); err != nil {
		t.Fatalf("建夹具库结构失败: %v", err)
	}
	// 跑完就收拾干净：夹具 schema 与平台自己的表同库不同 schema，
	// 留着不会撞车，但一个测试不该在别人的库里留下痕迹。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS xm_newapi_fixture CASCADE")
	})

	cst := time.FixedZone("CST", 8*3600)
	day, err := time.ParseInLocation("2006-01-02", revenueFixtureDay, cst)
	if err != nil {
		t.Fatal(err)
	}
	start = day.Unix()
	end = day.AddDate(0, 0, 1).Unix()

	// 六行，逐行都在钉一条边界：
	rows := []struct {
		channel   int64
		createdAt int64
		quota     int64
		why       string
	}{
		{1, start, 2_500, "窗口第一秒——必须算进去（>= start）"},
		{1, start + 3600, 2_500, "窗口正中"},
		{1, end - 1, 5_000, "窗口最后一秒——必须算进去（< end）"},
		{1, end, 999_999, "次日零点整——**不能**算进去（半开区间的上界）"},
		{1, start - 1, 888_888, "前一天最后一秒——不能算进去"},
		{2, start + 60, 777_777, "别的渠道——不能算进去"},
	}
	for _, r := range rows {
		if _, err := pool.Exec(ctx,
			"INSERT INTO xm_newapi_fixture.quota_data (channel_id, created_at, quota) VALUES ($1,$2,$3)",
			r.channel, r.createdAt, r.quota); err != nil {
			t.Fatalf("塞夹具数据失败（%s）: %v", r.why, err)
		}
	}
	return start, end
}

// readOnlyRevenueDB 用夹具 schema 建一个只读通道。
//
// search_path 指到夹具 schema：本通道的 SQL 写的是裸表名 quota_data / options
// （与 SoloAI 一致），靠 search_path 落到真实的库里。生产上那个 DSN 连的就是
// new-api 自己的库，裸表名本来就对。
func readOnlyRevenueDB(t *testing.T, rawURL string) (*metering.NewAPIRevenueDB, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		t.Fatalf("解析测试 DSN 失败: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "xm_newapi_fixture"
	// 闸 2：与生产路径同一个启动包参数。
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	// 闸 3：与生产路径同一把复核锁——happy path 也要过它，
	// 否则这套测试只证明了「拒绝可写连接」，没证明「放行只读连接」。
	cfg.AfterConnect = metering.VerifyReadOnly

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("建只读池失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("只读池 Ping 失败: %v", err)
	}

	db := metering.NewAPIRevenueDBFromPool(pool, metering.RevenueDBConfig{
		Now: func() time.Time { return time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) },
	})
	return db, pool
}

// TestRevenueDBServerRejectsWrites 是只读纪律的**真实证据**（ADR-018 闸 2/3）。
//
// 代码里没有写路径（那由 TestRevenueDBSourceHasNoWritePath 证明），
// 但那只证明「我们没打算写」。这条证明「就算写了也写不进去」——
// 服务端在只读事务里会拒绝，这是连的是别人家生产计费库时唯一算数的保证。
func TestRevenueDBServerRejectsWrites(t *testing.T) {
	writable, url := writablePool(t)
	seedRevenueFixture(t, writable)
	_, readOnly := readOnlyRevenueDB(t, url)

	ctx := context.Background()
	for _, sql := range []string{
		"CREATE TABLE xm_newapi_fixture.should_not_exist (id int)",
		"INSERT INTO xm_newapi_fixture.quota_data (channel_id, created_at, quota) VALUES (9,9,9)",
		"UPDATE xm_newapi_fixture.options SET value = '1'",
		"DELETE FROM xm_newapi_fixture.quota_data",
	} {
		_, err := readOnly.Exec(ctx, sql)
		if err == nil {
			t.Fatalf("只读连接上竟然执行成功了: %s", sql)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "read-only") {
			t.Fatalf("拒绝原因应是只读事务, got %v（语句: %s）", err, sql)
		}
	}
}

// TestRevenueDBRefusesConnectionWhenServerIsWritable 证明闸 3 真的在拦。
//
// 不带只读启动参数的池会被 AfterConnect 拒掉——这条防的是「启动参数被
// 连接池中间件（pgbouncer 之类）吞掉」：那种情况下连接会**看起来正常**
// 而实际可写，只有向服务端复核才发现得了。
func TestRevenueDBRefusesConnectionWhenServerIsWritable(t *testing.T) {
	_, url := writablePool(t)

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	// 故意**不**设 default_transaction_read_only，模拟启动参数没生效，
	// 但把生产路径上的那把复核锁原样挂上。
	cfg.AfterConnect = metering.VerifyReadOnly
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

// TestRevenueDBSumsBusinessDayWindow 验窗口边界与整数换算。
func TestRevenueDBSumsBusinessDayWindow(t *testing.T) {
	writable, url := writablePool(t)
	seedRevenueFixture(t, writable)
	db, _ := readOnlyRevenueDB(t, url)

	ctx := context.Background()
	got, err := db.AccountRevenue(ctx, "1", revenueFixtureDay)
	if err != nil {
		t.Fatalf("取收入失败: %v", err)
	}

	// 窗口内的三行：2500 + 2500 + 5000 = 10000 credits。
	// options 里没有 QuotaPerUnit → 回落到 ★ 口径常量 500000。
	// 10000 / 500000 = $0.02 = 20000 微美元。
	if got.RevenueMinorUnits != 20_000 {
		t.Fatalf("收入 = %d 微美元, want 20000（窗口内 10000 credits ÷ 500000）；"+
			"数字偏大说明边界行被算进来了", got.RevenueMinorUnits)
	}
	if got.Currency != "USD" {
		t.Fatalf("币种 = %q, want USD", got.Currency)
	}
	if got.OwnAccountID != "1" || got.Day != revenueFixtureDay {
		t.Fatalf("回填的键不对: %+v", got)
	}
	if got.ObservedAt.IsZero() || got.Watermark == "" {
		t.Fatalf("缺少新鲜度: %+v", got)
	}
	// 刻度来源必须写进水位：运维追问「收入怎么差了一个倍数」时，
	// 第一个要看的就是刻度是不是回落了。
	if !strings.Contains(got.Watermark, "qpu:500000:fallback") {
		t.Fatalf("水位应写明刻度与来源: %q", got.Watermark)
	}

	// 没有任何数据的渠道 = **已知的 0**，不是错误。
	// 「未知」与「已知 0」在台账里落成不同的东西（NULL vs 0，§5.1）。
	zero, err := db.AccountRevenue(ctx, "999", revenueFixtureDay)
	if err != nil {
		t.Fatalf("没有流量的渠道应是已知 0 而不是报错: %v", err)
	}
	if zero.RevenueMinorUnits != 0 {
		t.Fatalf("收入 = %d, want 0", zero.RevenueMinorUnits)
	}

	// 前一天：只有那条 start-1 的行（888888 credits）落在窗口里。
	// 888888 / 500000 = 1.777776 美元 = 1777776 微美元。
	prev, err := db.AccountRevenue(ctx, "1", "2026-08-27")
	if err != nil {
		t.Fatalf("取前一天失败: %v", err)
	}
	if prev.RevenueMinorUnits != 1_777_776 {
		t.Fatalf("前一天收入 = %d 微美元, want 1777776——"+
			"两天的窗口必须首尾相接、不重不漏", prev.RevenueMinorUnits)
	}
}

// TestRevenueDBReadsRuntimeQuotaPerUnit：上游改了刻度，收入必须跟着改。
//
// quota_per_unit 在 new-api 里是**运行期可变**的站点配置。写死 500000 的话，
// 上游一改刻度平台的收入就整体偏一个倍数，而且不报错——而收入与成本
// 必须用同一把尺子，否则 `毛利 = 收入 − 成本` 是两把尺子相减
// （成本侧 connectors/metering/newapi.go 已经是「读上游此刻在用的刻度」）。
func TestRevenueDBReadsRuntimeQuotaPerUnit(t *testing.T) {
	writable, url := writablePool(t)
	seedRevenueFixture(t, writable)

	ctx := context.Background()
	if _, err := writable.Exec(ctx,
		"INSERT INTO xm_newapi_fixture.options (key, value) VALUES ('QuotaPerUnit', '1000000')"); err != nil {
		t.Fatalf("写 options 失败: %v", err)
	}

	db, _ := readOnlyRevenueDB(t, url)
	got, err := db.AccountRevenue(ctx, "1", revenueFixtureDay)
	if err != nil {
		t.Fatalf("取收入失败: %v", err)
	}
	// 刻度翻倍 → 同样 10000 credits 只值一半：10000/1000000 = $0.01 = 10000 微美元。
	if got.RevenueMinorUnits != 10_000 {
		t.Fatalf("收入 = %d 微美元, want 10000（刻度改成 1000000 之后）", got.RevenueMinorUnits)
	}
	if !strings.Contains(got.Watermark, "qpu:1000000:options") {
		t.Fatalf("水位应写明刻度取自 options: %q", got.Watermark)
	}
}

// TestRevenueDBRejectsNonPositiveQuotaPerUnit：刻度非正时**拒绝取数**。
//
// 与成本侧同一条纪律：按默认 500000 折算会得到一个看起来完全正常的错数字
// （宪法 12 条）。上游写选项时把解析错误丢掉了，这个值真的可能是 0。
func TestRevenueDBRejectsNonPositiveQuotaPerUnit(t *testing.T) {
	writable, url := writablePool(t)
	seedRevenueFixture(t, writable)

	ctx := context.Background()
	if _, err := writable.Exec(ctx,
		"INSERT INTO xm_newapi_fixture.options (key, value) VALUES ('QuotaPerUnit', '0')"); err != nil {
		t.Fatalf("写 options 失败: %v", err)
	}

	db, _ := readOnlyRevenueDB(t, url)
	_, err := db.AccountRevenue(ctx, "1", revenueFixtureDay)
	if err == nil {
		t.Fatal("刻度为 0 必须拒绝取数，而不是回落到 500000")
	}
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("分类 = %q, want bad_response", connector.KindOf(err))
	}
}
