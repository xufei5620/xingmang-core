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
