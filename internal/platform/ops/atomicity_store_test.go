package ops_test

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

const (
	atomicityEnvironment = "production"
	atomicityMetricKey   = "sub2api.revenue.daily"
)

// faultSourcePattern 限死能拼进 DDL 的 source 形态。
var faultSourcePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// faultSource 生成一个只属于本次运行的 source。
//
// 带纳秒后缀是为了让并行跑的其他用例（ops 与 jobs 是两个包，go test 会并行
// 调度）不会撞上本用例注入的故障。
func faultSource(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// failSampleInsertsFor 让样本表的 INSERT 对指定 source 必然失败，用来在**真库**上
// 验证 UpsertWithSample 的事务回滚（XM-R010）。返回值可提前拆掉故障；
// 不调用也会在用例结束时自动拆。
//
// 为什么用触发器而不是「构造一个非法/超长的值」：样本表与最新态表的列约束是
// 逐条对齐的——同一条 metric_key 正则、同一份 status 白名单、同一条「失败必须
// 带错误码」CHECK（见 db/migrations/000004 与 000005）。任何能让样本 INSERT 撞
// 库错的值，都会让同一事务里**前一条** upsert 先撞上同一条错；那样测到的是
// 「第一条语句失败」，恰恰证明不了本次要证的东西：第一条成功、第二条失败时，
// 第一条也必须被撤销。触发器是唯一能精确命中第二条语句的手段。
//
// 也不在产品代码里留测试钩子：一个「让写入失败」的开关，编译进的是生产二进制。
// 触发器把故障留在测试自己安装、自己拆掉的库对象里，产品代码一个字节都不知情。
func failSampleInsertsFor(t *testing.T, pool *pgxpool.Pool, source string) func() {
	t.Helper()
	if !faultSourcePattern.MatchString(source) {
		t.Fatalf("source %q 需匹配 %s，否则拼进 DDL 不安全", source, faultSourcePattern)
	}
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	fn := "ops.xm_test_fail_sample_" + suffix
	trigger := "xm_test_fail_sample_" + suffix

	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $fault$
BEGIN
    IF NEW.source = '%s' THEN
        RAISE EXCEPTION 'XM-R010 故障注入：样本写入失败';
    END IF;
    RETURN NEW;
END;
$fault$`, fn, source)); err != nil {
		t.Fatalf("创建故障注入函数失败（测试账号需要 ops schema 的建对象权限）: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE TRIGGER %s BEFORE INSERT ON ops.metric_observation_sample
    FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, fn)); err != nil {
		t.Fatalf("创建故障注入触发器失败（测试账号需要表属主权限）: %v", err)
	}

	dropped := false
	drop := func() {
		if dropped {
			return
		}
		dropped = true
		if _, err := pool.Exec(ctx, fmt.Sprintf(
			"DROP TRIGGER IF EXISTS %s ON ops.metric_observation_sample", trigger)); err != nil {
			t.Errorf("卸载故障注入触发器失败: %v", err)
		}
		if _, err := pool.Exec(ctx, "DROP FUNCTION IF EXISTS "+fn+"()"); err != nil {
			t.Errorf("卸载故障注入函数失败: %v", err)
		}
	}
	t.Cleanup(drop)
	return drop
}

// atomicityObservation 造一条完整的成功观测（最新态与样本共用同一个值）。
func atomicityObservation(source string, at time.Time) ops.Observation {
	observedAt := at
	return ops.Observation{
		MetricKey: atomicityMetricKey, Source: source, Environment: atomicityEnvironment,
		ObservedAt: &observedAt, SyncedAt: at, LastSuccess: &observedAt,
		Watermark: "wm-" + at.Format(time.RFC3339), Status: ops.SyncOK,
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{"amount_minor": 123456, "currency": "CNY"},
	}
}

func countBySource(t *testing.T, pool *pgxpool.Pool, table, source string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE source = $1", source).Scan(&n); err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return n
}

// TestUpsertWithSampleRollsBackLatestStateWhenSampleFails 是 XM-R010 的核心回归
// （Codex 冷审 PR #48 第 1 条）。
//
// 旧写法先 Upsert 提交、再单独 InsertSample，样本挂了就留下一条**历史里查无
// 对证**的最新态：库自称「T1 采到了 V」，趋势图上却没有 T1。重试取的是更晚的
// now 与一份新的上游快照，补不回 T1。本用例证明这个中间状态现在不可达。
func TestUpsertWithSampleRollsBackLatestStateWhenSampleFails(t *testing.T) {
	pool := samplePool(t)
	s := ops.NewStore(pool)
	ctx := context.Background()

	source := faultSource("xmr010")
	t1 := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
	drop := failSampleInsertsFor(t, pool, source)

	if _, err := s.UpsertWithSample(ctx, atomicityObservation(source, t1)); err == nil {
		t.Fatal("样本写入被库拒绝时，UpsertWithSample 必须返回 error")
	}
	if n := countBySource(t, pool, "ops.metric_observation", source); n != 0 {
		t.Fatalf("事务回滚后最新态仍有 %d 行——这正是 Codex 指出的孤儿最新态", n)
	}
	if n := countBySource(t, pool, "ops.metric_observation_sample", source); n != 0 {
		t.Fatalf("事务回滚后样本仍有 %d 条", n)
	}

	// 故障排除，River 重试：取新的 now、重读上游。因为上一轮**什么都没写**，
	// 这是一次完整重放，不是给 T1 打补丁——没有缺口需要补。
	drop()
	t2 := t1.Add(5 * time.Minute)
	if _, err := s.UpsertWithSample(ctx, atomicityObservation(source, t2)); err != nil {
		t.Fatalf("重试轮必须成功: %v", err)
	}
	if n := countBySource(t, pool, "ops.metric_observation", source); n != 1 {
		t.Fatalf("重试后最新态 = %d 行, want 1", n)
	}
	if n := countBySource(t, pool, "ops.metric_observation_sample", source); n != 1 {
		t.Fatalf("重试后样本 = %d 条, want 1", n)
	}

	latest, err := s.Get(ctx, atomicityMetricKey, atomicityEnvironment)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !latest.SyncedAt.Equal(t2) {
		t.Fatalf("最新态 synced_at = %v, want %v", latest.SyncedAt, t2)
	}
	series, truncated, err := s.ListSamples(
		ctx, atomicityEnvironment, atomicityMetricKey, t1.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ListSamples: %v", err)
	}
	if truncated || len(series) != 1 {
		t.Fatalf("样本序列 = %d 条 (truncated=%v), want 恰好 1 条", len(series), truncated)
	}
	if !series[0].SyncedAt.Equal(t2) {
		t.Fatalf("样本停在 %v, want %v——失败轮的 synced_at 不该留下痕迹",
			series[0].SyncedAt, t2)
	}
}

// TestUpsertWithSampleWritesBothTables：成功路径两张表各留一条，且值一致。
func TestUpsertWithSampleWritesBothTables(t *testing.T) {
	pool := samplePool(t)
	s := ops.NewStore(pool)
	ctx := context.Background()

	source := faultSource("xmr010-ok")
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	got, err := s.UpsertWithSample(ctx, atomicityObservation(source, at))
	if err != nil {
		t.Fatalf("UpsertWithSample: %v", err)
	}
	if got.MetricKey != atomicityMetricKey || got.Value["currency"] != "CNY" {
		t.Fatalf("返回值未如实读回: %+v", got)
	}
	if n := countBySource(t, pool, "ops.metric_observation", source); n != 1 {
		t.Fatalf("最新态 = %d 行, want 1", n)
	}
	if n := countBySource(t, pool, "ops.metric_observation_sample", source); n != 1 {
		t.Fatalf("样本 = %d 条, want 1", n)
	}
}

// TestUpsertWithSampleRejectsInvalidObservationBeforeWriting：领域校验不过时
// 连事务都不开，两张表都不动。失败观测没有 last_error_code 是典型的静默失败。
func TestUpsertWithSampleRejectsInvalidObservationBeforeWriting(t *testing.T) {
	pool := samplePool(t)
	s := ops.NewStore(pool)

	source := faultSource("xmr010-bad")
	o := atomicityObservation(source, time.Now().UTC().Add(-time.Minute))
	o.Status = ops.SyncFailed // 没有 last_error_code：静默失败

	if _, err := s.UpsertWithSample(context.Background(), o); err == nil {
		t.Fatal("没有错误码的失败观测必须在领域层就被拒")
	}
	if n := countBySource(t, pool, "ops.metric_observation", source); n != 0 {
		t.Fatalf("校验失败却写了 %d 行最新态", n)
	}
	if n := countBySource(t, pool, "ops.metric_observation_sample", source); n != 0 {
		t.Fatalf("校验失败却写了 %d 条样本", n)
	}
}
