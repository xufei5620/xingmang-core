package jobs

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// jobsFaultSourcePattern 限死能拼进 DDL 的 source 形态。
var jobsFaultSourcePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// failSampleInsertsForSource 装上故障、返回拆除函数。**调用方必须 defer 它**，
// 不像 ops 包那份用 t.Cleanup：本用例自己 defer pool.Close()，而 t.Cleanup 在
// 函数的 defer 全跑完之后才执行——那时连接池已经关了，DROP 语句发不出去，
// 触发器会留在库里毒害后续用例。（ops 包那边的池由 t.Cleanup 关闭，cleanup 是
// LIFO，后注册的 drop 先跑，所以那份可以用 t.Cleanup。）
//
// 与 ops 包里的同名 helper 是同一套手法，
// 刻意各留一份而不是抽成共享包：它是**故障注入**，抽成 internal 包就会变成
// 一段编译进产品树的「让写入失败」的代码。二十行测试脚手架的重复，换产品代码
// 里一个测试钩子都没有，这笔账划算。
//
// 手法本身与它为什么不能用「构造非法值」代替，见
// internal/platform/ops/atomicity_store_test.go 的注释。
func failSampleInsertsForSource(t *testing.T, pool *pgxpool.Pool, source string) func() {
	t.Helper()
	if !jobsFaultSourcePattern.MatchString(source) {
		t.Fatalf("source %q 需匹配 %s，否则拼进 DDL 不安全", source, jobsFaultSourcePattern)
	}
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	fn := "ops.xm_test_fail_sample_job_" + suffix
	trigger := "xm_test_fail_sample_job_" + suffix

	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $fault$
BEGIN
    IF NEW.source = '%s' THEN
        RAISE EXCEPTION 'XM-R010 故障注入：样本写入失败';
    END IF;
    RETURN NEW;
END;
$fault$`, fn, source)); err != nil {
		t.Fatalf("创建故障注入函数失败: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE TRIGGER %s BEFORE INSERT ON ops.metric_observation_sample
    FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, fn)); err != nil {
		t.Fatalf("创建故障注入触发器失败: %v", err)
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
	return drop
}

// TestSub2APISyncRetryAfterSampleFailureIsCleanReplay 在**真库 + 真 ops.Store**
// 上跑两次 Work，闭合 Codex 冷审 PR #48 第 1 条点名的测试缺口
// （「从未真正执行第二次 Work 证明重试语义」）。
//
// 旧实现下这个用例会失败：第一次 Work 的 Upsert 已经提交，样本被触发器打回，
// 库里留下一条历史查无对证的最新态；第二次 Work 取更晚的 now 补不回那个点。
// 两写同事务之后，第一次 Work 什么都没写，第二次是一次干净的完整重放。
func TestSub2APISyncRetryAfterSampleFailureIsCleanReplay(t *testing.T) {
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set XM_TEST_DATABASE_URL (CI) or XM_RUN_INTEGRATION=1 with a local DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	var opsTable *string
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('ops.metric_observation_sample')::text`).Scan(&opsTable); err != nil {
		t.Fatal(err)
	}
	if opsTable == nil {
		t.Skip("ops.metric_observation_sample 不存在：先跑 db/migrations 的平台迁移")
	}

	// development 是 core.environment 里预置的环境；两张表的 environment
	// 都是它的外键，随便写个 "test" 会被库直接拒掉。
	const environment = "development"
	source := fmt.Sprintf("xmr010-replay-%d", time.Now().UnixNano())
	// defer 而不是 t.Cleanup：t.Cleanup 在函数的 defer 全跑完之后才执行，
	// 那时 pool.Close() 已经把连接池关了，清理语句发不出去。
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, table := range []string{
			"ops.metric_observation", "ops.metric_observation_sample",
		} {
			if _, err := pool.Exec(cleanupCtx,
				"DELETE FROM "+table+" WHERE source = $1", source); err != nil {
				t.Errorf("清理 %s: %v", table, err)
			}
		}
	}()

	store := ops.NewStore(pool)
	count := func(table string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM "+table+" WHERE source = $1", source).Scan(&n); err != nil {
			t.Fatalf("统计 %s: %v", table, err)
		}
		return n
	}
	workerAt := func(at time.Time) *Sub2APISyncWorker {
		return NewSub2APISyncWorker(Sub2APISyncOptions{
			Environment: environment,
			InstanceID:  source,
			Store:       store,
			NewClient:   fakeFactory(sub2api.FakeOptions{Now: func() time.Time { return at }}),
			Now:         func() time.Time { return at },
		})
	}

	t1 := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
	drop := failSampleInsertsForSource(t, pool, source)
	// 用例中途 t.Fatal 时也要拆掉触发器：它是库级对象，留下来会让后面每一个
	// 往这张表写 source=<本次> 的用例都失败。drop 幂等，成功路径重复调用无害。
	defer drop()

	// 第一次尝试：样本写入被触发器打回。
	if err := workerAt(t1).Work(ctx, syncJob()); err == nil {
		t.Fatal("样本写不进去时 Work 必须返回 error 让 River 重试")
	}
	if n := count("ops.metric_observation"); n != 0 {
		t.Fatalf("事务失败后最新态仍有 %d 行——孤儿最新态正是 XM-R010 要根除的", n)
	}
	if n := count("ops.metric_observation_sample"); n != 0 {
		t.Fatalf("事务失败后样本仍有 %d 条", n)
	}

	// River 重试：新的 now、重新读一遍上游。上一轮什么都没写，这是完整重放。
	drop()
	t2 := t1.Add(5 * time.Minute)
	if err := workerAt(t2).Work(ctx, syncJob()); err != nil {
		t.Fatalf("重试轮必须成功: %v", err)
	}

	if n := count("ops.metric_observation"); n != len(contractMetricKeys) {
		t.Fatalf("重试后最新态 = %d 行, want %d", n, len(contractMetricKeys))
	}
	if n := count("ops.metric_observation_sample"); n != len(contractMetricKeys) {
		t.Fatalf("重试后样本 = %d 条, want %d（第一轮不该留下任何点）",
			n, len(contractMetricKeys))
	}
	for _, key := range contractMetricKeys {
		latest, err := store.Get(ctx, key, environment)
		if err != nil {
			t.Fatalf("%s 未落库: %v", key, err)
		}
		if !latest.SyncedAt.Equal(t2) {
			t.Fatalf("%s 最新态 synced_at = %v, want %v", key, latest.SyncedAt, t2)
		}
		series, truncated, err := store.ListSamples(
			ctx, environment, key, t1.Add(-time.Hour), ops.MaxSampleLimit)
		if err != nil {
			t.Fatalf("%s ListSamples: %v", key, err)
		}
		if truncated {
			t.Fatalf("%s 不该截断", key)
		}
		// 本用例的 source 带纳秒后缀，但 ListSamples 只按 (environment,
		// metric_key) 过滤，同环境别的 source 可能也有点——只挑自己的看。
		mine := make([]ops.Observation, 0, 1)
		for _, point := range series {
			if point.Source == source {
				mine = append(mine, point)
			}
		}
		if len(mine) != 1 {
			t.Fatalf("%s 样本 = %d 条, want 恰好 1 条", key, len(mine))
		}
		if !mine[0].SyncedAt.Equal(t2) {
			t.Fatalf("%s 样本停在 %v, want %v——失败轮的 synced_at 不该留下痕迹",
				key, mine[0].SyncedAt, t2)
		}
		if mine[0].Status != ops.SyncOK {
			t.Fatalf("%s 样本应为成功: %+v", key, mine[0])
		}
	}
}
