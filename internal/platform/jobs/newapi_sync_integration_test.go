package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// TestNewAPISyncPostgresIntegration 在真库上跑完整条链路：River 周期任务
// 起来 → Fake 读取 → 五条观测落进 ops.metric_observation。
//
// 单元测试用的是内存仓储，证明不了三件只有真库才暴露的事：
//
//  1. ops.metric_observation.environment 是指向 core.environment 的外键；
//  2. Upsert 的 ON CONFLICT 是**整行覆盖**——失败路径若不先读旧行，
//     last_success 会被静静抹掉；
//  3. **渠道余额「键缺席 = 未配置」这个语义要活过 JSONB 往返。**
//     第 3 条是 NewAPI 独有的：内存仓储里 map 原样存着，缺的键当然还缺；
//     真库要把它编码成 JSONB 再解回来，中间任何一层给缺失键补一个零值，
//     看板上「未配置」就会变成「¥0.00」——而这两者的处置完全相反。
//     这条链路上没有第二个地方会发现这种退化。
func TestNewAPISyncPostgresIntegration(t *testing.T) {
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
	source := fmt.Sprintf("newapi-integration-%d", time.Now().UnixNano())
	// 用 defer 而不是 t.Cleanup：t.Cleanup 在测试函数的 defer 全部跑完之后
	// 才执行，那时上面的 pool.Close() 已经把连接池关了，清理语句发不出去。
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM ops.metric_observation WHERE source = $1`, source); err != nil {
			t.Errorf("清理观测记录: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM ops.metric_observation_sample WHERE source = $1`, source); err != nil {
			t.Errorf("清理历史样本: %v", err)
		}
	}()

	client, err := NewClient(pool, Config{
		Environment: environment,
		// 心跳与 Sub2API 同步在本用例里都只是噪音。
		HeartbeatInterval:    time.Minute,
		HeartbeatRunOnStart:  false,
		MaxWorkers:           1,
		NewAPISyncEnabled:    true,
		NewAPISyncInterval:   time.Second,
		NewAPISyncRunOnStart: true,
		NewAPISyncRunID:      source,
		NewAPIMode:           NewAPIModeFake,
		NewAPIInstanceID:     source,
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
	for _, key := range newapiMetricKeys {
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

	assertChannelBalanceSemanticsSurviveJSONB(t, before[newapi.MetricChannelsStatus])

	// 第二段：配置未就绪的 real 模式必然失败。停掉 River 直接驱动 Worker——
	// 这里要验的是 Upsert 的覆盖语义，不是 River 的调度。
	stop()
	failing := NewNewAPISyncWorker(NewAPISyncOptions{
		Environment: environment,
		InstanceID:  source,
		Mode:        NewAPIModeReal,
		Store:       store,
		NewClient:   NewNewAPIClientFactory(NewAPIModeReal, NewAPIRealConfig{}),
	})
	if err := failing.Work(ctx, newapiSyncJob()); err != nil {
		t.Fatalf("real 模式的 Work = %v, want nil（配置未就绪是事实，不是任务失败）", err)
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

	// 失败观测沿用上次成功的值，所以「余额未配置」这个语义在失败态下也要还在：
	// 看板此时显示的是「上次已知值 + 失败徽章」，那份旧值同样不能把「未配置」
	// 显示成「¥0.00」。
	failedRow, err := store.Get(ctx, newapi.MetricChannelsStatus, environment)
	if err != nil {
		t.Fatal(err)
	}
	assertChannelBalanceSemanticsSurviveJSONB(t, failedRow)

	// 第三段：历史样本（XM-0024）。最新态被整行覆盖成失败了，但趋势图必须
	// 还能看见前面那段成功——这正是分表的意义。
	since := time.Now().UTC().Add(-time.Hour)
	for key := range before {
		samples, _, err := store.ListSamples(ctx, environment, key, since, ops.MaxSampleLimit)
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
}

// assertChannelBalanceSemanticsSurviveJSONB 验证「余额键缺席 = 未配置」
// 这个语义在 JSONB 往返之后还在。
//
// Fake 的 6 条渠道里，ch-5 是唯一没配余额的那条。断言分两半，缺一不可：
// 有余额的渠道**必须**有那个键（否则退化成「全都未配置」也能蒙混过关），
// 没余额的渠道**必须**没有那个键（这是真正要防的那种退化）。
//
// 错误率同时验一次「还是不是整数」：契约层写进去的是 int64，而 JSONB 往返
// 天然是把整数还原成浮点的地方。ops.Store 用 UseNumber 挡住了这一步，
// 本断言就是那道防线的回归测试——它被去掉时这里会红。
func assertChannelBalanceSemanticsSurviveJSONB(t *testing.T, row ops.Observation) {
	t.Helper()

	rawChannels, ok := row.Value["channels"].([]any)
	if !ok {
		t.Fatalf("JSONB 往返后 channels 不是数组: %T", row.Value["channels"])
	}
	if len(rawChannels) != 6 {
		t.Fatalf("JSONB 往返后渠道条数 = %d, want 6", len(rawChannels))
	}

	unconfigured, configured := 0, 0
	for i, raw := range rawChannels {
		channel, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("渠道 #%d 不是对象: %T", i, raw)
		}
		id, _ := channel["channel_id"].(string)
		if _, present := channel["balance_minor_units"]; present {
			configured++
		} else {
			unconfigured++
			// Fake 里只有 ch-5 是未配置的；换了别条说明数据错位了
			if id != "ch-5" {
				t.Fatalf("渠道 %s 不该缺 balance_minor_units", id)
			}
		}
		rate, present := channel["error_rate_ppm"]
		if !present {
			t.Fatalf("渠道 %s 缺 error_rate_ppm", id)
		}
		// **必须是 json.Number 而不是 float64。** ops.Store 的解码器刻意开了
		// UseNumber（XM-0031，回归 Codex 冷审 PR #48 第 4 条）：json.Number 是
		// 原始字面量的字符串包装，编码回去时原样写出，超过 2^53 的整数不丢精度。
		// 解成 float64 的那一刻，「ppm 用整数表达」这条纪律就已经破了——
		// 契约层守住的东西会在仓储层被悄悄还原成浮点。
		number, ok := rate.(json.Number)
		if !ok {
			t.Fatalf("渠道 %s 的 error_rate_ppm 类型 = %T, want json.Number"+
				"（解成 float64 说明 ops.Store 的 UseNumber 被去掉了）", id, rate)
		}
		if _, err := number.Int64(); err != nil {
			t.Fatalf("渠道 %s 的 error_rate_ppm = %s 不是整数：%v——"+
				"带小数说明中间有一层把 ppm 转成了浮点", id, number, err)
		}
	}

	if unconfigured != 1 {
		t.Fatalf("未配置余额的渠道数 = %d, want 1——"+
			"「键缺席 = 未配置」的语义没活过 JSONB 往返，看板会把它显示成 ¥0.00", unconfigured)
	}
	if configured != 5 {
		t.Fatalf("已配置余额的渠道数 = %d, want 5", configured)
	}
}
