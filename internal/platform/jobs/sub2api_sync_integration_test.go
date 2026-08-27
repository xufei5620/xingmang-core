package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// TestSub2APISyncPostgresIntegration 在真库上跑完整条链路：River 周期任务
// 起来 → Fake 读取 → 五条观测落进 ops.metric_observation。
//
// 单元测试用的是内存仓储，证明不了两件只有真库才暴露的事：
// ops.metric_observation.environment 是指向 core.environment 的外键，
// 以及 Upsert 的 ON CONFLICT 是**整行覆盖**——失败路径若不先读旧行，
// last_success 会被静静抹掉。这两条都在下面被断言。
func TestSub2APISyncPostgresIntegration(t *testing.T) {
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

	// ops schema 来自平台迁移（db/migrations/000004），不是 River 的迁移。
	// 库里没有它就跳过：那是「库没迁移」，不是本任务失败。
	var opsTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('ops.metric_observation')::text`).Scan(&opsTable); err != nil {
		t.Fatal(err)
	}
	if opsTable == nil {
		t.Skip("ops.metric_observation 不存在：先跑 db/migrations 的平台迁移")
	}
	if err := Migrate(ctx, pool, nil); err != nil {
		t.Fatalf("River migration: %v", err)
	}

	// development 是 core.environment 里预置的三个环境之一；观测表的
	// environment 是它的外键，随便写个 "test" 会被库直接拒掉。
	const environment = "development"
	source := fmt.Sprintf("sub2api-integration-%d", time.Now().UnixNano())
	// 用 defer 而不是 t.Cleanup：t.Cleanup 在测试函数的 defer 全部跑完之后
	// 才执行，那时上面的 pool.Close() 已经把连接池关了，清理语句发不出去。
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM ops.metric_observation WHERE source = $1`, source); err != nil {
			t.Errorf("清理观测记录: %v", err)
		}
		// 样本表按 source 清理：本用例的 source 带纳秒后缀，不会误删别人的数据。
		// 这是**测试**的清理，不是产品路径——代码里没有删除样本的接口。
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM ops.metric_observation_sample WHERE source = $1`, source); err != nil {
			t.Errorf("清理历史样本: %v", err)
		}
	}()

	client, err := NewClient(pool, Config{
		Environment: environment,
		// 心跳在本用例里只是噪音：不 RunOnStart、周期远大于用例时长。
		HeartbeatInterval:     time.Minute,
		HeartbeatRunOnStart:   false,
		MaxWorkers:            1,
		Sub2APISyncEnabled:    true,
		Sub2APISyncInterval:   time.Second,
		Sub2APISyncRunOnStart: true,
		Sub2APISyncRunID:      source,
		Sub2APIMode:           Sub2APIModeFake,
		Sub2APIInstanceID:     source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := client.Stop(stopCtx); err != nil {
			t.Errorf("stop River client: %v", err)
		}
	}
	defer stop()

	deadline := time.Now().Add(30 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM ops.metric_observation WHERE environment = $1 AND source = $2`,
			environment, source).Scan(&count); err == nil && count >= 5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if count < 5 {
		t.Fatalf("周期同步只落库 %d 条观测，want 5", count)
	}

	store := ops.NewStore(pool)
	before := make(map[string]ops.Observation, 5)
	for _, key := range []string{
		sub2api.MetricUsersTotal, sub2api.MetricUsersBalance,
		sub2api.MetricRevenueDaily, sub2api.MetricCostDaily,
		sub2api.MetricChannelBalance,
	} {
		row, err := store.Get(ctx, key, environment)
		if err != nil {
			t.Fatalf("%s 未落库: %v", key, err)
		}
		if row.Source != source {
			t.Fatalf("%s source = %q, want %q", key, row.Source, source)
		}
		if row.Status != ops.SyncOK || row.ObservedAt == nil || row.Watermark == "" {
			t.Fatalf("%s 缺少新鲜度事实: %+v", key, row)
		}
		if state := row.Freshness(time.Now().UTC()).State; state != ops.StateFresh {
			t.Fatalf("%s freshness = %q, want fresh", key, state)
		}
		before[key] = row
	}

	// 第二段：真实模式当前必然失败。停掉 River 直接驱动 Worker——这里要验的
	// 是 Upsert 的覆盖语义，不是 River 的调度。
	stop()
	failing := NewSub2APISyncWorker(Sub2APISyncOptions{
		Environment: environment,
		InstanceID:  source,
		Mode:        Sub2APIModeReal,
		Store:       store,
		NewClient:   NewSub2APIClientFactory(Sub2APIModeReal, Sub2APIRealConfig{}),
	})
	if err := failing.Work(ctx, syncJob()); err != nil {
		t.Fatalf("real 模式的 Work = %v, want nil（未实现是事实，不是任务失败）", err)
	}
	for key, previous := range before {
		row, err := store.Get(ctx, key, environment)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != ops.SyncFailed || row.LastErrorCode != string(connector.KindNotSupported) {
			t.Fatalf("%s 应记为 not_supported 失败: %+v", key, row)
		}
		// 整行覆盖的陷阱：不先读旧行就写，这两个字段会被抹成 NULL，
		// 看板会把「刚才还成功过」错报成「从未采集」。
		if row.ObservedAt == nil || !row.ObservedAt.Equal(*previous.ObservedAt) {
			t.Fatalf("%s observed_at = %v, want 保留 %v", key, row.ObservedAt, previous.ObservedAt)
		}
		if row.LastSuccess == nil || !row.LastSuccess.Equal(*previous.LastSuccess) {
			t.Fatalf("%s last_success = %v, want 保留 %v", key, row.LastSuccess, previous.LastSuccess)
		}
		if state := row.Freshness(time.Now().UTC()).State; state != ops.StateFailed {
			t.Fatalf("%s freshness = %q, want failed（看板必须诚实显示失败）", key, state)
		}
	}

	// 第三段：历史样本（XM-0024）。最新态被整行覆盖成失败了，但趋势图必须
	// 还能看见前面那段成功——这正是分表的意义。
	since := time.Now().UTC().Add(-time.Hour)
	for key := range before {
		samples, err := store.ListSamples(ctx, environment, key, since, ops.MaxSampleLimit)
		if err != nil {
			t.Fatalf("%s ListSamples: %v", key, err)
		}
		if len(samples) < 2 {
			t.Fatalf("%s 样本 = %d 条, want >= 2（至少一条成功 + 一条失败）", key, len(samples))
		}
		for i := 1; i < len(samples); i++ {
			if samples[i].SyncedAt.Before(samples[i-1].SyncedAt) {
				t.Fatalf("%s 样本未按 synced_at 升序: %v -> %v",
					key, samples[i-1].SyncedAt, samples[i].SyncedAt)
			}
		}
		if samples[0].Status != ops.SyncOK {
			t.Fatalf("%s 第一个样本应为成功: %+v", key, samples[0])
		}
		// 失败观测也留样：这是趋势图上「那段红」的唯一数据来源。
		last := samples[len(samples)-1]
		if last.Status != ops.SyncFailed || last.LastErrorCode != string(connector.KindNotSupported) {
			t.Fatalf("%s 最后一个样本应是带错误码的失败样本: %+v", key, last)
		}
		if last.Source != source || last.Environment != environment {
			t.Fatalf("%s 样本的来源/环境字段不对: %+v", key, last)
		}
	}

	// 窗口过滤：把 since 推到未来，一条都不该返回。
	future, err := store.ListSamples(ctx, environment, sub2api.MetricUsersTotal,
		time.Now().UTC().Add(time.Hour), ops.MaxSampleLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(future) != 0 {
		t.Fatalf("窗口外不该返回样本, got %d 条", len(future))
	}

	// limit 生效，且丢掉的是最旧的那些——曲线右端必须始终贴着「现在」。
	limited, err := store.ListSamples(ctx, environment, sub2api.MetricUsersTotal, since, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit=1 应只返回 1 条, got %d", len(limited))
	}
	if limited[0].Status != ops.SyncFailed {
		t.Fatalf("limit 应保留最新的那条（失败样本）: %+v", limited[0])
	}
}
