package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
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
	if _, err := pool.Exec(ctx, "TRUNCATE ops.metric_observation"); err != nil {
		t.Fatalf("清空表失败: %v", err)
	}
	return pool
}

func sample(key string) ops.Observation {
	at := time.Now().UTC().Add(-30 * time.Second)
	return ops.Observation{
		ID: uuid.New(), MetricKey: key, Source: "sub2api-prod",
		Environment: "production", ObservedAt: &at, SyncedAt: at,
		Watermark: "wm-1", Status: ops.SyncOK, LastSuccess: &at,
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{"amount_minor": 123456, "currency": "CNY"},
	}
}

func TestUpsertRoundTrip(t *testing.T) {
	s := ops.NewStore(testPool(t))
	ctx := context.Background()

	got, err := s.Upsert(ctx, sample("sub2api.revenue.daily"))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.MetricKey != "sub2api.revenue.daily" || got.Value["currency"] != "CNY" {
		t.Fatalf("读回不一致: %+v", got)
	}
	if got.ObservedAt == nil {
		t.Fatal("observed_at 应保留")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated_at 应由库侧填充")
	}
}

func TestUpsertOverwritesSameKeyAndEnvironment(t *testing.T) {
	s := ops.NewStore(testPool(t))
	ctx := context.Background()

	first := sample("sub2api.revenue.daily")
	if _, err := s.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}

	second := sample("sub2api.revenue.daily")
	second.Watermark = "wm-2"
	second.Value = map[string]any{"amount_minor": 999999, "currency": "CNY"}
	if _, err := s.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListByEnvironment(ctx, "production")
	if err != nil {
		t.Fatal(err)
	}
	// 每个 (metric_key, environment) 只留最新一条
	if len(list) != 1 {
		t.Fatalf("应只有 1 条: %+v", list)
	}
	// 数字读回是 json.Number 而不是 float64（XM-0031，见 decodeValueJSON）
	if list[0].Watermark != "wm-2" || list[0].Value["amount_minor"] != json.Number("999999") {
		t.Fatalf("未被覆盖: %+v", list[0])
	}
}

func TestUpsertIsolatesEnvironments(t *testing.T) {
	s := ops.NewStore(testPool(t))
	ctx := context.Background()

	prod := sample("sub2api.revenue.daily")
	dev := sample("sub2api.revenue.daily")
	dev.ID = uuid.New()
	dev.Environment = "development"
	dev.Watermark = "wm-dev"

	if _, err := s.Upsert(ctx, prod); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, dev); err != nil {
		t.Fatal(err)
	}

	prodList, _ := s.ListByEnvironment(ctx, "production")
	devList, _ := s.ListByEnvironment(ctx, "development")
	if len(prodList) != 1 || len(devList) != 1 {
		t.Fatalf("环境应互相隔离: prod=%d dev=%d", len(prodList), len(devList))
	}
	if prodList[0].Watermark == devList[0].Watermark {
		t.Fatal("两个环境的记录不应互相覆盖")
	}
}

func TestUpsertUninitializedObservation(t *testing.T) {
	// 从未成功采集：observed_at 为空，但记录本身要能落库——
	// 前端需要据此显示这个指标存在，而不是干脆看不到它。
	s := ops.NewStore(testPool(t))
	ctx := context.Background()

	// 一次都没采过（没有错误码）→ 中性的 uninitialized
	o := sample("sub2api.revenue.daily")
	o.ObservedAt = nil
	o.LastSuccess = nil

	got, err := s.Upsert(ctx, o)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.ObservedAt != nil {
		t.Fatal("observed_at 应保持为空")
	}
	if f := got.Freshness(time.Now().UTC()); f.State != ops.StateUninitialized {
		t.Fatalf("state = %q, want uninitialized", f.State)
	}

	// 第一次采集就失败：同样没有 observed_at，但带着错误码 → failed
	// （XM-0031，回归 Codex 冷审 PR #43 head `ba8e275` 第 3 条）。
	// 本用例此前把这条也断言成 uninitialized，固化的正是那个谎。
	failed := sample("sub2api.cost.daily")
	failed.ObservedAt = nil
	failed.LastSuccess = nil
	failed.Status = ops.SyncFailed
	failed.LastErrorCode = "never_synced"

	gotFailed, err := s.Upsert(ctx, failed)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	f := gotFailed.Freshness(time.Now().UTC())
	if f.State != ops.StateFailed {
		t.Fatalf("首次采集失败 state = %q, want failed", f.State)
	}
	if f.LastErrorCode != "never_synced" {
		t.Fatalf("错误码应随主状态一起返回: %+v", f)
	}
}

func TestUpsertRejectsInvalidBeforeHittingDB(t *testing.T) {
	s := ops.NewStore(testPool(t))
	o := sample("sub2api.revenue.daily")
	o.Status = ops.SyncFailed // 失败却没有错误码
	if _, err := s.Upsert(context.Background(), o); err == nil {
		t.Fatal("领域校验应在落库前拒绝")
	}
}

func TestDatabaseRejectsInconsistentStatus(t *testing.T) {
	// 绕过领域校验直接插库，验证数据库 CHECK 也拦得住（纵深防御）
	pool := testPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO ops.metric_observation (
			id, metric_key, source, environment, synced_at, status,
			last_error_code, staleness_threshold_seconds
		) VALUES ($1, 'x.y.z', 'src', 'production', now(), 'failed', '', 1800)`,
		uuid.New())
	if err == nil {
		t.Fatal("failed 但无错误码必须被数据库拒绝")
	}
}
