package ops_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// samplePool 复用 testPool 的连接与跳过逻辑，额外清空样本表。
//
// 不改 testPool 本身：那个 helper 由最新态的用例共用，样本表对它们是无关的。
func samplePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE ops.metric_observation_sample"); err != nil {
		t.Fatalf("清空样本表失败: %v", err)
	}
	return pool
}

// sampleAt 造一条落在指定时刻的成功样本。
func sampleAt(key string, syncedAt time.Time) ops.Observation {
	observedAt := syncedAt
	return ops.Observation{
		MetricKey: key, Source: "sub2api-prod", Environment: "production",
		ObservedAt: &observedAt, SyncedAt: syncedAt,
		Watermark: "wm-" + syncedAt.Format(time.RFC3339), Status: ops.SyncOK,
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{"amount_minor": 123456, "currency": "CNY"},
	}
}

// TestInsertSampleReturnsSeriesInAscendingOrder：趋势图靠顺序吃饭，
// 乱序会画成一团麻线。写入顺序刻意打乱，验证排序来自库而不是写入顺序。
func TestInsertSampleReturnsSeriesInAscendingOrder(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-30 * time.Minute)

	// 乱序写入：t+10、t+0、t+20
	for _, offset := range []time.Duration{10 * time.Minute, 0, 20 * time.Minute} {
		if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", base.Add(offset))); err != nil {
			t.Fatalf("InsertSample: %v", err)
		}
	}

	got, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", base.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("ListSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("样本数 = %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !got[i].SyncedAt.After(got[i-1].SyncedAt) {
			t.Fatalf("样本未按 synced_at 升序: %v", []time.Time{
				got[0].SyncedAt, got[1].SyncedAt, got[2].SyncedAt})
		}
	}
	// 追加型：同一个 (metric_key, environment) 保留全部三条，不像最新态那样覆盖。
	if got[0].Value["currency"] != "CNY" || got[0].Watermark == "" {
		t.Fatalf("样本字段未如实读回: %+v", got[0])
	}
	if got[0].Environment != "production" || got[0].Source != "sub2api-prod" {
		t.Fatalf("样本来源字段未如实读回: %+v", got[0])
	}
}

// TestListSamplesFiltersByWindowAndKeyAndEnvironment：窗口外、别的指标、
// 别的环境的点一个都不许混进来。跨环境混入是最危险的一种——
// 生产数字出现在 staging 的图上（宪法 15 条）。
func TestListSamplesFiltersByWindowAndKeyAndEnvironment(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	inWindow := sampleAt("sub2api.revenue.daily", now.Add(-10*time.Minute))
	outOfWindow := sampleAt("sub2api.revenue.daily", now.Add(-3*time.Hour))
	otherMetric := sampleAt("sub2api.cost.daily", now.Add(-10*time.Minute))
	otherEnv := sampleAt("sub2api.revenue.daily", now.Add(-10*time.Minute))
	otherEnv.Environment = "development"

	for _, o := range []ops.Observation{inWindow, outOfWindow, otherMetric, otherEnv} {
		if err := s.InsertSample(ctx, o); err != nil {
			t.Fatalf("InsertSample: %v", err)
		}
	}

	// 1 小时窗口：只该剩 inWindow 一条
	got, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("窗口过滤后样本数 = %d, want 1: %+v", len(got), got)
	}
	if !got[0].SyncedAt.Equal(inWindow.SyncedAt) {
		t.Fatalf("留下的不是窗口内那条: %v want %v", got[0].SyncedAt, inWindow.SyncedAt)
	}

	// 放宽到 4 小时：窗口外那条回来了，仍然不含别的指标/环境
	wide, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-4*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide) != 2 {
		t.Fatalf("放宽窗口后样本数 = %d, want 2", len(wide))
	}
}

// TestListSamplesLimitKeepsNewest：窗口内超量时丢掉的必须是**最旧**的。
//
// 若留下最旧的那批，图会在窗口中途断掉，看起来像同步早就死了——
// 那是假的故障信号，比数据少几个点糟得多。
func TestListSamplesLimitKeepsNewest(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", at)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", base.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=2 应只返回 2 条, got %d", len(got))
	}
	// 保留的是 t+3、t+4，且仍是升序
	if !got[0].SyncedAt.Equal(base.Add(3*time.Minute)) || !got[1].SyncedAt.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("limit 应丢掉最旧的样本: got %v, %v", got[0].SyncedAt, got[1].SyncedAt)
	}

	// limit <= 0 与超上限都钳到 MaxSampleLimit，而不是变成全表扫描
	for _, limit := range []int32{0, -1, ops.MaxSampleLimit + 1} {
		all, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", base.Add(-time.Hour), limit)
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if len(all) != 5 {
			t.Fatalf("limit=%d 应钳到上限并返回全部 5 条, got %d", limit, len(all))
		}
	}
}

// TestInsertSampleKeepsFailedObservations：失败观测正是趋势图上「那段红」
// 的数据来源。它必须能落库，而且错误码必须留住。
func TestInsertSampleKeepsFailedObservations(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	failed := sampleAt("sub2api.revenue.daily", now.Add(-5*time.Minute))
	failed.Status = ops.SyncFailed
	failed.LastErrorCode = "unavailable"
	// 失败时上游没给新数据：observed_at 停在上一次成功的时刻
	previous := now.Add(-20 * time.Minute)
	failed.ObservedAt = &previous

	if err := s.InsertSample(ctx, sampleAt("sub2api.revenue.daily", now.Add(-20*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSample(ctx, failed); err != nil {
		t.Fatalf("失败样本必须写得进去: %v", err)
	}

	got, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("样本数 = %d, want 2", len(got))
	}
	if got[0].Status != ops.SyncOK {
		t.Fatalf("第一个点应为 ok: %+v", got[0])
	}
	if got[1].Status != ops.SyncFailed || got[1].LastErrorCode != "unavailable" {
		t.Fatalf("失败样本的状态与错误码必须留住: %+v", got[1])
	}
	// 失败点的 observed_at 停在旧时刻，但它在横轴上的位置由 synced_at 决定
	if got[1].ObservedAt == nil || !got[1].ObservedAt.Equal(previous) {
		t.Fatalf("失败样本的 observed_at = %v, want %v", got[1].ObservedAt, previous)
	}
}

// TestInsertSampleAllowsUninitializedObservation：从未成功采集时 observed_at
// 为空。历史上那一段「灰」同样要有点，不能整段消失。
func TestInsertSampleAllowsUninitializedObservation(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	o := sampleAt("sub2api.revenue.daily", now.Add(-5*time.Minute))
	o.ObservedAt = nil
	o.Status = ops.SyncFailed
	o.LastErrorCode = "never_synced"
	o.Value = nil

	if err := s.InsertSample(ctx, o); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	got, err := s.ListSamples(ctx, "production", "sub2api.revenue.daily", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ObservedAt != nil {
		t.Fatalf("observed_at 应保持为空: %+v", got)
	}
	// value 为 nil 时落成空对象，读回也是空 map——前端不必先判 null
	if got[0].Value == nil || len(got[0].Value) != 0 {
		t.Fatalf("空值应落成空对象, got %+v", got[0].Value)
	}
}

// TestInsertSampleRejectsSilentFailure：失败却没有错误码，在领域层就该被拒。
// 放行一条没有原因的失败样本，图上就会出现一段没人解释得了的红。
func TestInsertSampleRejectsSilentFailure(t *testing.T) {
	s := ops.NewStore(samplePool(t))
	o := sampleAt("sub2api.revenue.daily", time.Now().UTC())
	o.Status = ops.SyncFailed // 没有 LastErrorCode
	if err := s.InsertSample(context.Background(), o); err == nil {
		t.Fatal("失败但无错误码必须被拒")
	}
}

// TestDatabaseRejectsInconsistentSampleStatus：绕过领域校验直接插库，
// 验证数据库 CHECK 也拦得住（纵深防御，与最新态表同一条规则）。
func TestDatabaseRejectsInconsistentSampleStatus(t *testing.T) {
	pool := samplePool(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO ops.metric_observation_sample (
			metric_key, source, environment, synced_at, status, last_error_code
		) VALUES ('x.y.z', 'src', 'production', now(), 'failed', '')`)
	if err == nil {
		t.Fatal("failed 但无错误码必须被数据库拒绝")
	}
}
