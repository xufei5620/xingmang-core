package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// XM-R012 保留期清理的真库验证。
//
// 单元测试证的是分批循环的**控制流**（用内存 pruner）；这里证的是 SQL 本身：
// cutoff 边界筛得对不对、LIMIT 是不是真的分了批、以及最要紧的那条——
// **活跃告警一条都不能被删**。后者错了不会有任何报错，只会在某天有人去查
// 「昨天那条告警呢」时才发现。

// retentionTestMetric 是告警夹具引用的指标键。
//
// 抽成常量不是为了复用：gitleaks 的 generic-api-key 规则会把
// `SourceMetricKey: "<字面量>"` 判成泄漏的密钥——已知误报，本仓库反复中招。
// 仓库纪律禁止用 allowlist 消音（消音会连真的一起放过），抽常量是既定解法。
const retentionTestMetric = "sub2api.revenue.daily"

func retentionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatalf("集成测试 DSN 无效: %v", err)
	}
	if !enabled {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, stmt := range []string{
		"TRUNCATE ops.metric_observation_sample",
		"TRUNCATE ops.metric_observation CASCADE",
		"TRUNCATE alerts.alert CASCADE",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return pool
}

// insertSampleAged 直接写一条指定 synced_at 的样本。
//
// 不走 ops.Store.InsertSample：那条路径把 synced_at 当成「刚采到」的时刻，
// 而这里要造的正是**很久以前**采到的行。
func insertSampleAged(t *testing.T, pool *pgxpool.Pool, key string, syncedAt time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO ops.metric_observation_sample
			(metric_key, source, environment, observed_at, synced_at,
			 status, watermark, value_json)
		VALUES ($1, 'test', 'development', $2, $2, 'ok', 'wm', '{}'::jsonb)`,
		key, syncedAt)
	if err != nil {
		t.Fatalf("插入样本失败: %v", err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestPruneSamplesRespectsCutoffAndBatches：只删过期的，并且真的分批。
func TestPruneSamplesRespectsCutoffAndBatches(t *testing.T) {
	pool := retentionTestPool(t)
	ctx := context.Background()
	store := ops.NewStore(pool)
	now := time.Now().UTC()

	// 5 条很旧（100 天前）+ 3 条还在保留期内（10 天前）
	for i := 0; i < 5; i++ {
		insertSampleAged(t, pool, fmt.Sprintf("old.%d", i), now.AddDate(0, 0, -100))
	}
	for i := 0; i < 3; i++ {
		insertSampleAged(t, pool, fmt.Sprintf("fresh.%d", i), now.AddDate(0, 0, -10))
	}

	cutoff := now.AddDate(0, 0, -90)

	// batchSize=2：分批必须真的生效，一次只删两行。
	// 用 LIMIT 却漏了它（比如 DELETE 直接带 WHERE）的话，第一次调用就会把
	// 5 条全删了——那正是长事务风险的来源。
	deleted, err := store.PruneSamples(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("PruneSamples: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("第一批删了 %d 行, want 2", deleted)
	}
	if got := countRows(t, pool, "ops.metric_observation_sample"); got != 6 {
		t.Fatalf("剩余 %d 行, want 6", got)
	}

	// 删到干净
	total := deleted
	for range 10 {
		n, err := store.PruneSamples(ctx, cutoff, 2)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n < 2 {
			break
		}
	}
	if total != 5 {
		t.Fatalf("共删 %d 行, want 5", total)
	}
	// 保留期内的三条必须完好——cutoff 写反（> 而不是 <）会删掉的正是它们
	if got := countRows(t, pool, "ops.metric_observation_sample"); got != 3 {
		t.Fatalf("保留期内剩余 %d 行, want 3", got)
	}
}

// TestPruneResolvedNeverTouchesActiveAlerts 是本任务里风险最高的一条断言。
//
// 一条 2019 年就开着、至今没人处理的告警，恰恰是**最不该被删**的那种——
// 它是一个还没解决的问题。而按 resolved_at 之外的任何条件筛（比如 created_at）
// 都会把它删掉，且没有任何报错：表变小了，看起来像清理生效了。
func TestPruneResolvedNeverTouchesActiveAlerts(t *testing.T) {
	pool := retentionTestPool(t)
	ctx := context.Background()
	store := alerts.NewStore(pool)
	now := time.Now().UTC()
	ancient := now.AddDate(0, 0, -400)

	newAlert := func(key string, at time.Time) uuid.UUID {
		t.Helper()
		a, _, err := store.Upsert(ctx, alerts.UpsertInput{
			RuleKey:         "metric.stale",
			DedupKey:        key,
			Severity:        alerts.SeverityWarning,
			Title:           "指标过期",
			Detail:          "测试用",
			Environment:     "development",
			SourceMetricKey: retentionTestMetric,
			Now:             at,
		})
		if err != nil {
			t.Fatalf("Upsert %s: %v", key, err)
		}
		return a.ID
	}

	// 三条都很老，区别只在**有没有解决**
	staleOpen := newAlert("open-since-forever", ancient)
	resolvedLongAgo := newAlert("resolved-long-ago", ancient)
	acked := newAlert("acknowledged", ancient)

	if _, err := store.Resolve(ctx, resolvedLongAgo, ancient.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := store.Acknowledge(ctx, acked, ancient.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	deleted, err := store.PruneResolved(ctx, now.AddDate(0, 0, -180), 100)
	if err != nil {
		t.Fatalf("PruneResolved: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("删了 %d 条, want 1（只有已解决的那条该走）", deleted)
	}
	// 未解决与已认领的必须还在
	for _, id := range []uuid.UUID{staleOpen, acked} {
		if _, err := store.Get(ctx, id); err != nil {
			t.Fatalf("未解决/已认领的告警被删了（%s）: %v", id, err)
		}
	}
	if _, err := store.Get(ctx, resolvedLongAgo); err == nil {
		t.Fatal("已解决且过期的告警应当被删除")
	}
}

// TestPruneResolvedKeepsRecentlyResolved：刚解决的不删。
//
// 一条上周解决的告警正是复盘要看的材料。cutoff 用错时区或用错字段
// （resolved_at vs updated_at）都会误伤它。
func TestPruneResolvedKeepsRecentlyResolved(t *testing.T) {
	pool := retentionTestPool(t)
	ctx := context.Background()
	store := alerts.NewStore(pool)
	now := time.Now().UTC()

	a, _, err := store.Upsert(ctx, alerts.UpsertInput{
		RuleKey: "metric.stale", DedupKey: "resolved-last-week",
		Severity: alerts.SeverityWarning, Title: "指标过期", Detail: "测试用",
		Environment: "development", SourceMetricKey: retentionTestMetric,
		Now: now.AddDate(0, 0, -30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(ctx, a.ID, now.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.PruneResolved(ctx, now.AddDate(0, 0, -180), 100)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("删了 %d 条, want 0", deleted)
	}
}

// TestRetentionWorkerEndToEnd：整个任务在真库上跑一轮。
//
// 单元测试用的是内存 pruner，接线错了（比如把样本的 cutoff 传给了告警）
// 那里看不出来。这条把 Worker、两个仓储、两条 SQL 串起来验一遍。
func TestRetentionWorkerEndToEnd(t *testing.T) {
	pool := retentionTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 样本：超过 90 天的 3 条 + 保留期内 2 条
	for i := 0; i < 3; i++ {
		insertSampleAged(t, pool, fmt.Sprintf("old.%d", i), now.AddDate(0, 0, -120))
	}
	for i := 0; i < 2; i++ {
		insertSampleAged(t, pool, fmt.Sprintf("fresh.%d", i), now.AddDate(0, 0, -1))
	}

	// 告警：一条 200 天前解决（超 180 天）、一条 100 天前解决（在期内）
	alertStore := alerts.NewStore(pool)
	for _, c := range []struct {
		key        string
		resolvedAt time.Time
	}{
		{"stale-resolved", now.AddDate(0, 0, -200)},
		{"recent-resolved", now.AddDate(0, 0, -100)},
	} {
		a, _, err := alertStore.Upsert(ctx, alerts.UpsertInput{
			RuleKey: "metric.stale", DedupKey: c.key,
			Severity: alerts.SeverityWarning, Title: "指标过期", Detail: "测试用",
			Environment: "development", SourceMetricKey: retentionTestMetric,
			Now: c.resolvedAt.AddDate(0, 0, -1),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := alertStore.Resolve(ctx, a.ID, c.resolvedAt); err != nil {
			t.Fatal(err)
		}
	}

	w := NewRetentionWorker(RetentionOptions{
		Logger:      slog.New(slog.NewJSONHandler(discardWriter{}, nil)),
		Environment: "development",
		Samples:     ops.NewStore(pool),
		Alerts:      alertStore,
	})
	if err := w.Work(ctx, nil); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if got := countRows(t, pool, "ops.metric_observation_sample"); got != 2 {
		t.Fatalf("样本剩余 %d 行, want 2", got)
	}
	if got := countRows(t, pool, "alerts.alert"); got != 1 {
		t.Fatalf("告警剩余 %d 行, want 1", got)
	}
}

// TestRetentionWorkerLeavesAuditUntouched：跑完清理，审计表一条不少。
//
// 这不是在测「我们没写审计清理代码」——那是显然的。它测的是这条约束在
// **端到端**上成立：审计链 append-only（宪法 11 条），而库层的
// audit_event_no_delete 规则让 DELETE 静默空转，于是「不小心加了清理」
// 这个失误在日志里看起来会完全正常。这条测试是那个失误的唯一栅栏。
func TestRetentionWorkerLeavesAuditUntouched(t *testing.T) {
	pool := retentionTestPool(t)
	ctx := context.Background()

	before := countRows(t, pool, "audit.audit_event")

	w := NewRetentionWorker(RetentionOptions{
		Logger:      slog.New(slog.NewJSONHandler(discardWriter{}, nil)),
		Environment: "development",
		Samples:     ops.NewStore(pool),
		Alerts:      alerts.NewStore(pool),
		// 天数压到 1：如果存在任何审计清理路径，这里必然会踩到它
		SampleRetentionDays: 1,
		AlertRetentionDays:  1,
	})
	if err := w.Work(ctx, nil); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if after := countRows(t, pool, "audit.audit_event"); after != before {
		t.Fatalf("审计事件数从 %d 变成 %d——审计链一条都不能删（见 retention.go 文件头）",
			before, after)
	}
}

// discardWriter 丢弃日志，避免真库用例刷屏。
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
