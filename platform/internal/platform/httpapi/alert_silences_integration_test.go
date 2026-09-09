package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
)

// 端到端验证：真实静默表 + 真实仓储 + 真实路由。
//
// 单元测试（alert_silences_test.go）用 fake 证明了 handler 会**怎么分类**；
// 这里证明的是另一件事——「只给生效中的」这条过滤真的落在 SQL 上。
// 那两段是分开的代码：判据在 alerts.Silence.Active（Go），过滤在
// ListActiveSilences（SQL）。只测其中一段的话，一个「SQL 忘了带时间条件」
// 的实现照样能让 fake 用例全绿，而页面会把三个月前的窗口显示成正在生效。

func silenceTestPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := pool.Exec(ctx, "TRUNCATE alerts.alert_silence"); err != nil {
		t.Fatalf("清空静默表失败: %v", err)
	}
	return pool
}

// silenceIntRouter 挂真实仓储，时钟仍然固定——三态的边界不该跟着墙上时钟走。
func silenceIntRouter(t *testing.T, store *alerts.Store) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         &fakeExecutor{},
		ActionRegistry: action.NewRegistry(),
		Silences:       store,
		SilenceNow:     func() time.Time { return silenceNow },
	})
}

func createSilenceRow(
	t *testing.T, s *alerts.Store, reason string, startOffset, endOffset time.Duration,
) alerts.Silence {
	t.Helper()
	got, err := s.CreateSilence(context.Background(), alerts.Silence{
		ID:          uuid.New(),
		RuleKey:     alerts.RuleMetricSyncFailed,
		Environment: "development",
		Reason:      reason,
		StartsAt:    silenceNow.Add(startOffset),
		EndsAt:      silenceNow.Add(endOffset),
		CreatedBy:   "staff_alice",
	})
	if err != nil {
		t.Fatalf("CreateSilence(%s): %v", reason, err)
	}
	return got
}

// TestSilencesEndpointAgainstRealStore：默认只给此刻生效的那一条。
//
// 这是本片最要紧的一条。库里三条窗口（生效中 / 未开始 / 已过期），
// 默认查询必须**只**返回中间那一条正在压着告警的。
//
// 变异验证（见交接文档）：把路由默认分支改成 ListSilences（即去掉「只看
// 生效中」这条过滤），本用例立刻变红——它断言的是 items 恰好一条且 id
// 对得上，不是「某某不存在」这种可能恒真的形状。
func TestSilencesEndpointAgainstRealStore(t *testing.T) {
	store := alerts.NewStore(silenceTestPool(t))
	active := createSilenceRow(t, store, "正在压着", -30*time.Minute, 30*time.Minute)
	scheduled := createSilenceRow(t, store, "还没开始", time.Hour, 2*time.Hour)
	expired := createSilenceRow(t, store, "早就过了", -3*time.Hour, -time.Hour)

	got := decodeSilences(t, getSilences(t, silenceIntRouter(t, store), "", "ops.read"))

	if len(got.Items) != 1 {
		t.Fatalf("默认应只返回此刻生效的 1 条，实际 %d 条: %+v", len(got.Items), got.Items)
	}
	item := got.Items[0]
	if item.ID != active.ID.String() {
		t.Fatalf("返回的不是生效中那条: got id=%s reason=%q，want id=%s",
			item.ID, item.Reason, active.ID)
	}
	if item.State != "active" {
		t.Fatalf("state = %q, want active", item.State)
	}
	if item.Reason != "正在压着" || item.CreatedBy != "staff_alice" {
		t.Fatalf("理由与按下的人要原样到达前端: %+v", item)
	}
	// 起止时间经过一轮 timestamptz 往返之后仍要对得上——这一段是
	// fake 测不到的（精度与时区漂移只在真实库上出现）。
	if item.StartsAt != "2026-09-08T11:30:00Z" || item.EndsAt != "2026-09-08T12:30:00Z" {
		t.Fatalf("起止时间在真实库往返后不一致: %+v", item)
	}
	_, _ = scheduled, expired
}

// TestSilencesEndpointStateAllAgainstRealStore：state=all 把三条都给出来，
// 且每条带对的态。
//
// 这条是上一条的对照组：同一批数据、同一个时钟，只换 state 参数。
// 「默认那条只返回 1 条」于是不可能是因为库里本来就只有一条。
func TestSilencesEndpointStateAllAgainstRealStore(t *testing.T) {
	store := alerts.NewStore(silenceTestPool(t))
	active := createSilenceRow(t, store, "正在压着", -30*time.Minute, 30*time.Minute)
	scheduled := createSilenceRow(t, store, "还没开始", time.Hour, 2*time.Hour)
	expired := createSilenceRow(t, store, "早就过了", -3*time.Hour, -time.Hour)

	got := decodeSilences(t, getSilences(t, silenceIntRouter(t, store), "?state=all", "ops.read"))

	if len(got.Items) != 3 {
		t.Fatalf("state=all 应返回 3 条，实际 %d 条", len(got.Items))
	}
	states := map[string]string{}
	for _, item := range got.Items {
		states[item.ID] = item.State
	}
	for _, c := range []struct {
		id   uuid.UUID
		want string
	}{
		{active.ID, "active"},
		{scheduled.ID, "scheduled"},
		{expired.ID, "expired"},
	} {
		if states[c.id.String()] != c.want {
			t.Fatalf("id=%s state = %q, want %q", c.id, states[c.id.String()], c.want)
		}
	}
}

// TestSilencesEndpointFiltersByEnvironmentAgainstRealStore：跨环境读不到。
//
// 静默窗口按环境隔离与告警同一条理由（规格 §20.5）。这条在真实 SQL 上
// 验证 WHERE environment = $1 真的挂上了——handler 传对了环境但 SQL 忘了
// 用它的话，fake 用例发现不了。
func TestSilencesEndpointFiltersByEnvironmentAgainstRealStore(t *testing.T) {
	store := alerts.NewStore(silenceTestPool(t))
	mine := createSilenceRow(t, store, "本环境的", -30*time.Minute, 30*time.Minute)
	if _, err := store.CreateSilence(context.Background(), alerts.Silence{
		ID:          uuid.New(),
		RuleKey:     alerts.RuleMetricSyncFailed,
		Environment: "production",
		Reason:      "生产的，不该看见",
		StartsAt:    silenceNow.Add(-30 * time.Minute),
		EndsAt:      silenceNow.Add(30 * time.Minute),
		CreatedBy:   "staff_bob",
	}); err != nil {
		t.Fatalf("CreateSilence(production): %v", err)
	}

	// 调用者身份是 development（见 silenceIntRouter）
	got := decodeSilences(t, getSilences(t, silenceIntRouter(t, store), "?state=all", "ops.read"))
	if len(got.Items) != 1 {
		t.Fatalf("只该看见本环境的 1 条，实际 %d 条: %+v", len(got.Items), got.Items)
	}
	if got.Items[0].ID != mine.ID.String() {
		t.Fatalf("看见的不是本环境那条: %+v", got.Items[0])
	}
	if got.Items[0].Environment != "development" {
		t.Fatalf("environment = %q", got.Items[0].Environment)
	}
}
