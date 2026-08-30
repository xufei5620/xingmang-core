package action_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
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
	return pool
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sampleRun(actionID string, status action.RunStatus, code action.Code) action.Run {
	now := time.Now().UTC()
	return action.Run{
		ID:            uuid.New(),
		ActionID:      actionID,
		ActionVersion: "1",
		PrincipalID:   "staff_alice",
		PrincipalType: principal.TypeHuman,
		Environment:   "production",
		RequestID:     "req-" + uuid.NewString(),
		RiskLevel:     action.L1,
		Status:        status,
		ErrorCode:     code,
		DurationMS:    12,
		StartedAt:     now,
		FinishedAt:    now.Add(12 * time.Millisecond),
	}
}

func TestPgRunStoreInsertAndList(t *testing.T) {
	pool := testPool(t)
	store := action.NewPgRunStore(pool, quietLogger())
	ctx := context.Background()
	actionID := "registry.service.create"

	ok := sampleRun(actionID, action.RunSucceeded, "")
	if err := store.InsertRun(ctx, ok); err != nil {
		t.Fatalf("InsertRun(成功): %v", err)
	}
	bad := sampleRun(actionID, action.RunFailed, action.CodePermissionDenied)
	if err := store.InsertRun(ctx, bad); err != nil {
		t.Fatalf("InsertRun(失败): %v", err)
	}

	runs, err := store.ListRunsByAction(ctx, actionID, 10)
	if err != nil {
		t.Fatalf("ListRunsByAction: %v", err)
	}
	if len(runs) < 2 {
		t.Fatalf("应至少读到 2 条, got %d", len(runs))
	}
}

func TestPgRunStoreRejectsInconsistentErrorCode(t *testing.T) {
	pool := testPool(t)
	store := action.NewPgRunStore(pool, quietLogger())
	ctx := context.Background()

	// 成功却带错误码：库层 CHECK 必须拒绝（让「静默失败」不可表示）
	r := sampleRun("registry.service.create", action.RunSucceeded, action.CodeExecutionFailed)
	if err := store.InsertRun(ctx, r); err == nil {
		t.Fatal("成功状态带错误码必须被数据库拒绝")
	}
	// 失败却无错误码：同样拒绝
	r2 := sampleRun("registry.service.create", action.RunFailed, "")
	if err := store.InsertRun(ctx, r2); err == nil {
		t.Fatal("失败状态无错误码必须被数据库拒绝")
	}
}

func TestPgRunStoreListRunsFiltersAndPaginates(t *testing.T) {
	pool := testPool(t)
	store := action.NewPgRunStore(pool, quietLogger())
	ctx := context.Background()

	// 独立的 action_id 前缀，避免和同一测试库里其它测试写的行互相污染断言
	actionID := "registry.service.observe"
	env := "staging"
	principalID := "staff_" + uuid.NewString()

	var seeded []action.Run
	for i := 0; i < 3; i++ {
		r := sampleRun(actionID, action.RunSucceeded, "")
		r.Environment = env
		r.PrincipalID = principalID
		r.StartedAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		r.FinishedAt = r.StartedAt.Add(time.Millisecond)
		if err := store.InsertRun(ctx, r); err != nil {
			t.Fatalf("InsertRun[%d]: %v", i, err)
		}
		seeded = append(seeded, r)
	}
	// 另一个环境的一条：环境过滤必须把它挡在外面
	other := sampleRun(actionID, action.RunSucceeded, "")
	other.Environment = "production"
	other.PrincipalID = principalID
	if err := store.InsertRun(ctx, other); err != nil {
		t.Fatalf("InsertRun(其它环境): %v", err)
	}

	page, err := store.ListRuns(ctx, action.RunFilter{
		Environment: env, ActionID: actionID, PrincipalID: principalID, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("首页应恰好 2 条（limit=2 且共 3 条候选），got %d", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatal("满页应带 next_cursor")
	}
	for _, it := range page.Items {
		if it.Environment != env {
			t.Fatalf("环境过滤失效，读到 %q", it.Environment)
		}
	}

	next, err := store.ListRuns(ctx, action.RunFilter{
		Environment: env, ActionID: actionID, PrincipalID: principalID,
		Limit: 2, Cursor: page.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListRuns(翻页): %v", err)
	}
	if len(next.Items) != 1 {
		t.Fatalf("第二页应剩 1 条，got %d", len(next.Items))
	}
	if next.NextCursor != "" {
		t.Fatal("不满页应表示已翻到底（next_cursor 为空）")
	}
	// 两页合起来不重不漏
	seenIDs := map[string]bool{}
	for _, it := range append(append([]action.Run{}, page.Items...), next.Items...) {
		seenIDs[it.ID.String()] = true
	}
	if len(seenIDs) != 3 {
		t.Fatalf("两页合计应覆盖 3 条不重复记录，got %d", len(seenIDs))
	}

	// status 过滤：只种了 succeeded，查 failed 应为空
	failedOnly, err := store.ListRuns(ctx, action.RunFilter{
		Environment: env, ActionID: actionID, PrincipalID: principalID, Status: "failed",
	})
	if err != nil {
		t.Fatalf("ListRuns(status=failed): %v", err)
	}
	if len(failedOnly.Items) != 0 {
		t.Fatalf("status=failed 不应匹配任何 succeeded 记录，got %d", len(failedOnly.Items))
	}
}

func TestPgRunStoreListRunsRejectsInvalidCursor(t *testing.T) {
	pool := testPool(t)
	store := action.NewPgRunStore(pool, quietLogger())
	ctx := context.Background()

	_, err := store.ListRuns(ctx, action.RunFilter{Environment: "development", Cursor: "not-valid-base64!!"})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("非法 cursor 应报 INVALID_PARAMS, got %v", err)
	}
}

func TestPgRunStoreGetRun(t *testing.T) {
	pool := testPool(t)
	store := action.NewPgRunStore(pool, quietLogger())
	ctx := context.Background()

	r := sampleRun("registry.service.create", action.RunSucceeded, "")
	if err := store.InsertRun(ctx, r); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	got, ok, err := store.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !ok {
		t.Fatal("应能读到刚插入的记录")
	}
	if got.ID != r.ID || got.ActionID != r.ActionID || got.Status != r.Status {
		t.Fatalf("读回的记录与写入不符: %+v vs %+v", got, r)
	}

	_, ok, err = store.GetRun(ctx, uuid.New())
	if err != nil {
		t.Fatalf("GetRun(不存在): %v", err)
	}
	if ok {
		t.Fatal("不存在的 ID 应返回 ok=false")
	}
}

func TestActionRunIsAppendOnly(t *testing.T) {
	pool := testPool(t)
	store := action.NewPgRunStore(pool, quietLogger())
	ctx := context.Background()
	r := sampleRun("registry.service.create", action.RunSucceeded, "")
	if err := store.InsertRun(ctx, r); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	// 规格 §4.4：审计表 append-only。PostgreSQL 规则把 DELETE/UPDATE 改写为
	// 空操作——因此断言方式是「值没变」，而不是「语句报错」。
	if _, err := pool.Exec(ctx, "DELETE FROM action.action_run WHERE id = $1", r.ID); err != nil {
		t.Fatalf("DELETE 不应报错（规则改写为空操作）: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM action.action_run WHERE id = $1", r.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatal("DELETE 必须无效——审计表是 append-only")
	}

	if _, err := pool.Exec(ctx,
		"UPDATE action.action_run SET status = 'failed' WHERE id = $1", r.ID); err != nil {
		t.Fatalf("UPDATE 不应报错（规则改写为空操作）: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx,
		"SELECT status FROM action.action_run WHERE id = $1", r.ID).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("UPDATE 必须无效，status = %q", status)
	}
}
