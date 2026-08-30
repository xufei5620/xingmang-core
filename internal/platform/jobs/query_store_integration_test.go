package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/rivertype"
)

// 本文件测试「后台任务」页（XM-JOBS0）的只读仓储 QueryStore。它复用
// river_integration_test.go 里的 selectIntegrationDSN/validateIntegrationDSN
// （同包，未导出函数可以直接用），跳过条件与其余 *_integration_test.go
// 逐字一致：没设 XM_TEST_DATABASE_URL 也没有 XM_RUN_INTEGRATION=1 就跳过。
//
// 夹具直接对 river_job 发参数化 INSERT——这不是本文件唯一的例外，是测试
// 数据准备的标准做法（宪法 27 条禁止的是绕过 Action 的生产写路径，不是
// 测试自己在自己的 scratch 库里灌数据）。为避免与同包其它集成测试
// （sub2api_sync_integration_test.go 等）在共享的 XM_TEST_DATABASE_URL 上
// 互相干扰，凡是能用任意 kind 字符串验证的用例（ListRuns）一律用一个
// 本次运行唯一的假 kind；只有 Overview 必须用六个真实注册的 kind 时，
// 断言只认自己刚插入那一行的 id，不对整表计数下结论。

type testRunRow struct {
	Kind        string
	Queue       string
	State       RunState
	Attempt     int
	MaxAttempts int
	Args        map[string]any
	Errors      []rivertype.AttemptError
	CreatedAt   time.Time
	ScheduledAt time.Time
	AttemptedAt *time.Time
	FinalizedAt *time.Time
}

// fmtInt64Ptr 把 *int64 打印成可读的值，而不是 %v 默认打印的指针地址——
// 断言失败时后者会让人误以为值坏了，其实只是格式化动词选错了。
func fmtInt64Ptr(p *int64) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *p)
}

func insertTestRunRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, row testRunRow) int64 {
	t.Helper()
	if row.Queue == "" {
		row.Queue = QueueMaintenance
	}
	if row.MaxAttempts == 0 {
		row.MaxAttempts = 3
	}
	if row.ScheduledAt.IsZero() {
		row.ScheduledAt = row.CreatedAt
	}
	// river_job 的 finalized_or_finalized_at_null 检查约束要求：终结状态
	// （cancelled/completed/discarded）必须有 finalized_at，其余状态必须没有。
	// 测试夹具按状态自动补齐，调用方不必每次都记得这条 River 自己的规则。
	switch row.State {
	case RunStateCancelled, RunStateCompleted, RunStateDiscarded:
		if row.FinalizedAt == nil {
			finalizedAt := row.CreatedAt
			row.FinalizedAt = &finalizedAt
		}
	default:
		row.FinalizedAt = nil
	}
	argsBytes, err := json.Marshal(row.Args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	errorTexts := make([]string, 0, len(row.Errors))
	for _, e := range row.Errors {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal attempt error: %v", err)
		}
		errorTexts = append(errorTexts, string(b))
	}

	var id int64
	err = pool.QueryRow(ctx, `
		INSERT INTO river_job
			(kind, queue, state, attempt, max_attempts, args, errors,
			 created_at, scheduled_at, attempted_at, finalized_at)
		VALUES (
			$1, $2, $3::river_job_state, $4, $5, $6::jsonb,
			(SELECT array_agg(e::jsonb) FROM unnest($7::text[]) AS e),
			$8, $9, $10, $11
		)
		RETURNING id
	`, row.Kind, row.Queue, string(row.State), row.Attempt, row.MaxAttempts,
		string(argsBytes), errorTexts, row.CreatedAt.UTC(), row.ScheduledAt.UTC(),
		row.AttemptedAt, row.FinalizedAt,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert test river_job row (kind=%s): %v", row.Kind, err)
	}
	return id
}

func mustQueryStorePool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set XM_TEST_DATABASE_URL (CI) or XM_RUN_INTEGRATION=1 with a local DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, nil); err != nil {
		pool.Close()
		t.Fatalf("river migration: %v", err)
	}
	return pool, pool.Close
}

func TestQueryStoreOverviewPostgresIntegration(t *testing.T) {
	pool, closePool := mustQueryStorePool(t)
	defer closePool()
	ctx := context.Background()
	store := NewQueryStore(pool)

	// Overview 按「每个 kind 最近两条」推 ObservedIntervalSeconds，这个断言
	// 天然是全表口径，没法像 ListRuns 那样靠一个本次唯一的假 kind 避开同一个
	// scratch 库里其它测试运行留下的旧数据（包括本测试自己上一次运行的残留）。
	// 清空是安全的：这是测试专用的 scratch 库，不是任何真实环境。
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE river_job RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate river_job: %v", err)
	}

	runID := fmt.Sprintf("xmjobs0-overview-%d", time.Now().UnixNano())
	now := time.Now().UTC()

	// platform_heartbeat：两次出现，间隔 90 秒，用来验证观测周期推算与
	// worker 心跳投影。
	olderHeartbeat := now.Add(-90 * time.Second)
	insertTestRunRow(t, ctx, pool, testRunRow{
		Kind: HeartbeatJobKind, State: RunStateCompleted,
		Args:        map[string]any{"run_id": runID},
		CreatedAt:   olderHeartbeat,
		AttemptedAt: &olderHeartbeat, FinalizedAt: &olderHeartbeat,
	})
	latestHeartbeatID := insertTestRunRow(t, ctx, pool, testRunRow{
		Kind: HeartbeatJobKind, State: RunStateCompleted,
		Args:        map[string]any{"run_id": runID},
		CreatedAt:   now,
		AttemptedAt: &now, FinalizedAt: &now,
	})

	// sub2api_sync：单次出现、retryable，带一条错误——验证 LastRun/LastError
	// 与「只观测到一次时 ObservedIntervalSeconds 为 nil」。
	sub2apiID := insertTestRunRow(t, ctx, pool, testRunRow{
		Kind: Sub2APISyncJobKind, State: RunStateRetryable, Attempt: 1,
		Args: map[string]any{"run_id": runID},
		Errors: []rivertype.AttemptError{
			{At: now, Attempt: 1, Error: "jobs: sub2api sync worker has no observation store"},
		},
		CreatedAt: now,
	})

	overview, err := store.Overview(ctx, "staging")
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}

	if len(overview.Schedules) != len(RegisteredPeriodicJobSpecs()) {
		t.Fatalf("schedules count = %d, want %d", len(overview.Schedules), len(RegisteredPeriodicJobSpecs()))
	}
	// 目录顺序必须逐字跟随 RegisteredPeriodicJobSpecs——前端按顺序渲染。
	for i, spec := range RegisteredPeriodicJobSpecs() {
		if overview.Schedules[i].Kind != spec.Kind {
			t.Fatalf("schedules[%d].Kind = %q, want %q", i, overview.Schedules[i].Kind, spec.Kind)
		}
	}

	var heartbeatSchedule, sub2apiSchedule, financeSchedule *ScheduleStatus
	for i := range overview.Schedules {
		switch overview.Schedules[i].Kind {
		case HeartbeatJobKind:
			heartbeatSchedule = &overview.Schedules[i]
		case Sub2APISyncJobKind:
			sub2apiSchedule = &overview.Schedules[i]
		case FinanceCollectJobKind:
			financeSchedule = &overview.Schedules[i]
		}
	}
	if heartbeatSchedule == nil || heartbeatSchedule.LastRun == nil || heartbeatSchedule.LastRun.ID != latestHeartbeatID {
		t.Fatalf("heartbeat last run = %+v, want id %d", heartbeatSchedule, latestHeartbeatID)
	}
	if heartbeatSchedule.ObservedIntervalSeconds == nil || *heartbeatSchedule.ObservedIntervalSeconds != 90 {
		t.Fatalf("heartbeat observed interval = %s, want 90s", fmtInt64Ptr(heartbeatSchedule.ObservedIntervalSeconds))
	}
	if heartbeatSchedule.Activity != ScheduleActivityObserved {
		t.Fatalf("heartbeat activity = %q, want %q", heartbeatSchedule.Activity, ScheduleActivityObserved)
	}
	if !overview.WorkerHeartbeat.Known {
		t.Fatal("worker heartbeat should be known once a platform_heartbeat row exists")
	}
	if overview.WorkerHeartbeat.SecondsAgo == nil || *overview.WorkerHeartbeat.SecondsAgo < 0 {
		t.Fatalf("worker heartbeat seconds_ago = %s, want a non-negative value", fmtInt64Ptr(overview.WorkerHeartbeat.SecondsAgo))
	}
	if overview.WorkerHeartbeat.Environment != "platform" {
		t.Fatalf("worker heartbeat environment = %q, want %q (heartbeat args carry no environment)",
			overview.WorkerHeartbeat.Environment, "platform")
	}

	if sub2apiSchedule == nil || sub2apiSchedule.LastRun == nil || sub2apiSchedule.LastRun.ID != sub2apiID {
		t.Fatalf("sub2api_sync last run = %+v, want id %d", sub2apiSchedule, sub2apiID)
	}
	if sub2apiSchedule.ObservedIntervalSeconds != nil {
		t.Fatalf("sub2api_sync observed interval = %v, want nil (only one observation)",
			sub2apiSchedule.ObservedIntervalSeconds)
	}
	if sub2apiSchedule.LastRun.LastError == nil {
		t.Fatal("sub2api_sync last run should carry the inserted error")
	} else if !strings.Contains(sub2apiSchedule.LastRun.LastError.Message, "no observation store") {
		t.Fatalf("last error message = %q, want it to contain the inserted text", sub2apiSchedule.LastRun.LastError.Message)
	}
	if sub2apiSchedule.LastRun.ErrorCount != 1 {
		t.Fatalf("sub2api_sync error count = %d, want 1", sub2apiSchedule.LastRun.ErrorCount)
	}

	// finance_cost_sync 这次一行都没插：只要它在目录里出现且诚实报告
	// 「从未观测到」，就不该因为没有数据被漏掉（规格 §9.1：没有数据不代表
	// 队列不存在）。它可能因为别的并发集成测试而非空，因此只断言字段存在，
	// 不断言具体状态。
	if financeSchedule == nil {
		t.Fatal("finance_cost_sync must always appear in the schedule catalog")
	}

	// 队列积压：default / maintenance 两个已知队列必须总是出现。
	queues := map[string]QueueBacklogRow{}
	for _, row := range overview.QueueBacklog {
		queues[row.Queue] = row
	}
	if _, ok := queues["default"]; !ok {
		t.Fatal("queue_backlog missing 'default' even though no data ≠ queue doesn't exist")
	}
	maint, ok := queues[QueueMaintenance]
	if !ok {
		t.Fatalf("queue_backlog missing %q", QueueMaintenance)
	}
	if maint.Retryable < 1 {
		t.Fatalf("maintenance queue retryable = %d, want >= 1 (just inserted one)", maint.Retryable)
	}
}

func TestQueryStoreListRunsPostgresIntegration(t *testing.T) {
	pool, closePool := mustQueryStorePool(t)
	defer closePool()
	ctx := context.Background()
	store := NewQueryStore(pool)

	// 用一个本次运行唯一的假 kind：ListRuns 对 kind 不做枚举校验（river_job.kind
	// 是自由文本），这样可以在共享的 scratch 库里完全不受其它集成测试影响。
	kind := fmt.Sprintf("xmjobs0_test_kind_%d", time.Now().UnixNano())
	base := time.Now().UTC().Add(-time.Hour)

	longMessage := strings.Repeat("错", 400) // 每个字符 3 字节，共 1200 字节，超过 500 字节上限
	var ids []int64
	for i := 0; i < 5; i++ {
		createdAt := base.Add(time.Duration(i) * time.Minute)
		row := testRunRow{
			Kind: kind, State: RunStateCompleted, Attempt: 1,
			Args:      map[string]any{"run_id": "r", "secret_field": "must-not-leak", "business_day": "2026-08-31"},
			CreatedAt: createdAt,
		}
		if i == 2 {
			row.State = RunStateDiscarded
			row.Errors = []rivertype.AttemptError{{At: createdAt, Attempt: 1, Error: longMessage}}
		}
		ids = append(ids, insertTestRunRow(t, ctx, pool, row))
	}
	// ids[4] 是最新的一条（created_at 最大），ListRuns 按 id 降序排列——
	// 因为 id 是 bigserial 且本测试按时间顺序递增插入，降序 id 与降序
	// created_at 在这批夹具里是同一个顺序。

	t.Run("分页与游标", func(t *testing.T) {
		page, err := store.ListRuns(ctx, ListRunsInput{Environment: "staging", Kinds: []string{kind}, Limit: 2})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("items = %d, want 2", len(page.Items))
		}
		if page.Items[0].ID != ids[4] || page.Items[1].ID != ids[3] {
			t.Fatalf("first page ids = [%d %d], want [%d %d]", page.Items[0].ID, page.Items[1].ID, ids[4], ids[3])
		}
		if page.NextBefore != ids[3] {
			t.Fatalf("next_before = %d, want %d", page.NextBefore, ids[3])
		}

		page2, err := store.ListRuns(ctx, ListRunsInput{Environment: "staging", Kinds: []string{kind}, Limit: 2, Before: page.NextBefore})
		if err != nil {
			t.Fatalf("ListRuns page 2: %v", err)
		}
		if len(page2.Items) != 2 || page2.Items[0].ID != ids[2] || page2.Items[1].ID != ids[1] {
			t.Fatalf("second page ids unexpected: %+v", page2.Items)
		}

		page3, err := store.ListRuns(ctx, ListRunsInput{Environment: "staging", Kinds: []string{kind}, Limit: 2, Before: page2.NextBefore})
		if err != nil {
			t.Fatalf("ListRuns page 3: %v", err)
		}
		if len(page3.Items) != 1 || page3.Items[0].ID != ids[0] {
			t.Fatalf("third page unexpected: %+v", page3.Items)
		}
		if page3.NextBefore != 0 {
			t.Fatalf("next_before on the last page = %d, want 0 (translated as no more pages)", page3.NextBefore)
		}
	})

	t.Run("状态筛选与错误截断", func(t *testing.T) {
		page, err := store.ListRuns(ctx, ListRunsInput{
			Environment: "staging", Kinds: []string{kind}, State: RunStateDiscarded, Limit: 10,
		})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != ids[2] {
			t.Fatalf("discarded filter = %+v, want exactly id %d", page.Items, ids[2])
		}
		item := page.Items[0]
		if item.LastError == nil {
			t.Fatal("discarded row should carry the inserted error")
		}
		if !item.LastError.Truncated {
			t.Fatal("1200-byte message should have been truncated")
		}
		if len(item.LastError.Message) > maxRunErrorMessageBytes {
			t.Fatalf("truncated message is %d bytes, want <= %d", len(item.LastError.Message), maxRunErrorMessageBytes)
		}
		if !utf8.ValidString(item.LastError.Message) {
			t.Fatalf("truncation split a multi-byte rune: %q", item.LastError.Message)
		}
		if item.LastError.OriginalLength != len(longMessage) {
			t.Fatalf("original_length = %d, want %d", item.LastError.OriginalLength, len(longMessage))
		}
	})

	t.Run("Args 只透出白名单字段", func(t *testing.T) {
		page, err := store.ListRuns(ctx, ListRunsInput{Environment: "staging", Kinds: []string{kind}, Limit: 1})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items = %d, want 1", len(page.Items))
		}
		args := page.Items[0].Args
		if _, leaked := args["secret_field"]; leaked {
			t.Fatalf("args leaked a field outside the allowlist: %+v", args)
		}
		if args["business_day"] != "2026-08-31" {
			t.Fatalf("args dropped an allowlisted field: %+v", args)
		}
	})

	t.Run("非法 state 报错", func(t *testing.T) {
		if _, err := store.ListRuns(ctx, ListRunsInput{Environment: "staging", State: "not_a_state"}); err == nil {
			t.Fatal("expected an error for an unknown state")
		}
	})
}

// 「同步批次」页签要同时看多个 kind；服务端一次查询按「其中任意一个」匹配。
func TestQueryStoreListRunsMultipleKindsPostgresIntegration(t *testing.T) {
	pool, closePool := mustQueryStorePool(t)
	defer closePool()
	ctx := context.Background()
	store := NewQueryStore(pool)

	suffix := time.Now().UnixNano()
	kindA := fmt.Sprintf("xmjobs0_multi_a_%d", suffix)
	kindB := fmt.Sprintf("xmjobs0_multi_b_%d", suffix)
	kindC := fmt.Sprintf("xmjobs0_multi_c_%d", suffix) // 故意不查询这个
	now := time.Now().UTC()

	idA := insertTestRunRow(t, ctx, pool, testRunRow{Kind: kindA, State: RunStateCompleted, CreatedAt: now})
	idB := insertTestRunRow(t, ctx, pool, testRunRow{Kind: kindB, State: RunStateCompleted, CreatedAt: now.Add(time.Second)})
	insertTestRunRow(t, ctx, pool, testRunRow{Kind: kindC, State: RunStateCompleted, CreatedAt: now.Add(2 * time.Second)})

	page, err := store.ListRuns(ctx, ListRunsInput{
		Environment: "staging", Kinds: []string{kindA, kindB}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	got := map[int64]bool{}
	for _, item := range page.Items {
		got[item.ID] = true
	}
	if !got[idA] || !got[idB] {
		t.Fatalf("items = %+v, want ids %d and %d present", page.Items, idA, idB)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %+v, want exactly 2 (kindC must be excluded)", page.Items)
	}
}

// environment 软过滤：字段存在时按调用者环境过滤，不存在时（今天全部六个
// 周期任务）照常返回——见 query_store.go 文件头注释。
func TestQueryStoreListRunsEnvironmentSoftFilterPostgresIntegration(t *testing.T) {
	pool, closePool := mustQueryStorePool(t)
	defer closePool()
	ctx := context.Background()
	store := NewQueryStore(pool)

	kind := fmt.Sprintf("xmjobs0_test_env_%d", time.Now().UnixNano())
	now := time.Now().UTC()

	prodOnly := insertTestRunRow(t, ctx, pool, testRunRow{
		Kind: kind, State: RunStateCompleted,
		Args: map[string]any{"environment": "production", "run_id": "prod-row"}, CreatedAt: now,
	})
	noEnv := insertTestRunRow(t, ctx, pool, testRunRow{
		Kind: kind, State: RunStateCompleted,
		Args: map[string]any{"run_id": "no-env-row"}, CreatedAt: now.Add(time.Second),
	})

	staging, err := store.ListRuns(ctx, ListRunsInput{Environment: "staging", Kinds: []string{kind}, Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns(staging): %v", err)
	}
	stagingIDs := map[int64]bool{}
	for _, item := range staging.Items {
		stagingIDs[item.ID] = true
	}
	if stagingIDs[prodOnly] {
		t.Fatalf("a row tagged environment=production leaked into a staging query: %+v", staging.Items)
	}
	if !stagingIDs[noEnv] {
		t.Fatal("a row with no environment field must show up regardless of caller environment")
	}

	production, err := store.ListRuns(ctx, ListRunsInput{Environment: "production", Kinds: []string{kind}, Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns(production): %v", err)
	}
	productionIDs := map[int64]bool{}
	for _, item := range production.Items {
		productionIDs[item.ID] = true
	}
	if !productionIDs[prodOnly] {
		t.Fatal("a row tagged environment=production must show up for a production caller")
	}
	if !productionIDs[noEnv] {
		t.Fatal("a row with no environment field must show up regardless of caller environment")
	}
}
