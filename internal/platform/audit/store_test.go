package audit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
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
	// audit_event 有 append-only 规则，但 TRUNCATE 不受规则限制
	if _, err := pool.Exec(ctx, "TRUNCATE audit.chain_root, audit.audit_event"); err != nil {
		t.Fatalf("清空审计表失败: %v", err)
	}
	return pool
}

func evt(actionID string) audit.Event {
	now := time.Now().UTC()
	return audit.Event{
		ID: uuid.New(), OccurredAt: now,
		PrincipalID: "staff_alice", PrincipalType: principal.TypeHuman,
		ActionID: actionID, ActionVersion: "1", ActionRunID: uuid.New(),
		ResourceType: "core.service", ResourceID: "sub2api-prod",
		Environment: "production", RequestID: "req-" + uuid.NewString(),
		AfterSummary: map[string]any{"status": "active"},
		Result:       audit.ResultSucceeded,
	}
}

func TestAppendBuildsChain(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	seq, hash, err := s.Tip(ctx)
	if err != nil || seq != 0 || hash != audit.GenesisHash {
		t.Fatalf("空链的链尖应是 (0, Genesis): %d %s %v", seq, hash, err)
	}

	prev := audit.GenesisHash
	for i := 1; i <= 3; i++ {
		e, err := s.Append(ctx, evt("registry.service.create"))
		if err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
		if e.Sequence != int64(i) {
			t.Fatalf("sequence = %d, want %d", e.Sequence, i)
		}
		if e.PrevHash != prev {
			t.Fatalf("#%d prev_hash = %s, want %s", i, e.PrevHash, prev)
		}
		if len(e.EventHash) != 64 {
			t.Fatalf("#%d event_hash 非法: %s", i, e.EventHash)
		}
		if e.RecordedAt.IsZero() {
			t.Fatalf("#%d recorded_at 未填充", i)
		}
		prev = e.EventHash
	}

	problem, err := s.VerifyChain(ctx, 1, 3)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem != nil {
		t.Fatalf("完好的链不应报问题: %+v", problem)
	}
}

func TestVerifyDetectsTamperedRow(t *testing.T) {
	// 这是整个审计链存在的理由：改一条历史记录必须被发现
	pool := testPool(t)
	s := audit.NewStore(pool)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, evt("registry.service.create")); err != nil {
			t.Fatal(err)
		}
	}

	// append-only 规则挡住普通 UPDATE；这里模拟「攻击者有 DBA 权限、
	// 先禁用规则」的场景，验证哈希链仍能发现篡改。
	if _, err := pool.Exec(ctx, "ALTER TABLE audit.audit_event DISABLE RULE audit_event_no_update"); err != nil {
		t.Fatalf("禁用规则失败: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), "ALTER TABLE audit.audit_event ENABLE RULE audit_event_no_update")
	}()

	if _, err := pool.Exec(ctx,
		"UPDATE audit.audit_event SET principal_id = 'staff_mallory' WHERE sequence = 2"); err != nil {
		t.Fatalf("篡改失败: %v", err)
	}

	problem, err := s.VerifyChain(ctx, 1, 3)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem == nil {
		t.Fatal("篡改未被发现——审计链失去意义")
	}
	if problem.Sequence != 2 || problem.Kind != "hash_mismatch" {
		t.Fatalf("应定位到 sequence=2 的 hash_mismatch, got %+v", problem)
	}
}

func TestVerifyDetectsSequenceGap(t *testing.T) {
	pool := testPool(t)
	s := audit.NewStore(pool)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, evt("registry.service.create")); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := pool.Exec(ctx, "ALTER TABLE audit.audit_event DISABLE RULE audit_event_no_delete"); err != nil {
		t.Fatalf("禁用规则失败: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), "ALTER TABLE audit.audit_event ENABLE RULE audit_event_no_delete")
	}()
	if _, err := pool.Exec(ctx, "DELETE FROM audit.audit_event WHERE sequence = 2"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	problem, err := s.VerifyChain(ctx, 1, 3)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem == nil || problem.Kind != "sequence_gap" {
		t.Fatalf("删除中间行应被发现为 sequence_gap, got %+v", problem)
	}
}

func TestAppendOnlyRulesBlockNormalWrites(t *testing.T) {
	pool := testPool(t)
	s := audit.NewStore(pool)
	ctx := context.Background()
	e, err := s.Append(ctx, evt("registry.service.create"))
	if err != nil {
		t.Fatal(err)
	}

	// 规则让 DELETE/UPDATE 成为静默空操作——断言「值没变」而非「语句报错」
	if _, err := pool.Exec(ctx, "DELETE FROM audit.audit_event WHERE id = $1", e.ID); err != nil {
		t.Fatalf("DELETE 不应报错: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit.audit_event WHERE id = $1", e.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("DELETE 必须无效——审计表 append-only")
	}
}

func TestAppendRejectsReusedEventID(t *testing.T) {
	// 审计事件 ID 不可复用：同 ID 再次追加必须被主键拒绝，
	// 否则「补一条同 ID 的记录」会成为掩盖历史的手段
	s := audit.NewStore(testPool(t))
	ctx := context.Background()
	e := evt("registry.service.create")
	if _, err := s.Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, e); err == nil {
		t.Fatal("复用事件 ID 必须被拒绝")
	}
	// 被拒后链应仍然完好（事务回滚，没留下半条记录）
	tipSeq, _, err := s.Tip(ctx)
	if err != nil || tipSeq != 1 {
		t.Fatalf("失败的追加不应推进链尖: seq=%d %v", tipSeq, err)
	}
	problem, err := s.VerifyChain(ctx, 1, 1)
	if err != nil || problem != nil {
		t.Fatalf("链应完好: %+v %v", problem, err)
	}
}

func TestAppendSameContentDifferentIDsBothSucceed(t *testing.T) {
	// 同内容、不同 ID 的两条事件都能追加，且哈希不同
	// （sequence 与 prev_hash 参与计算），链保持完好
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	a := evt("registry.service.create")
	b := a
	b.ID = uuid.New()

	ea, err := s.Append(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	eb, err := s.Append(ctx, b)
	if err != nil {
		t.Fatalf("不同 ID 的同内容事件应可追加: %v", err)
	}
	if ea.EventHash == eb.EventHash {
		t.Fatal("不同链位置的事件哈希不应相同")
	}
	problem, err := s.VerifyChain(ctx, 1, 2)
	if err != nil || problem != nil {
		t.Fatalf("链应完好: %+v %v", problem, err)
	}
}

func TestAppendSerializesConcurrentWriters(t *testing.T) {
	// 并发追加必须串行化，否则会产生两条指向同一 prev_hash 的事件（链分叉）
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := s.Append(ctx, evt("registry.service.create"))
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发 Append 失败: %v", err)
		}
	}

	problem, err := s.VerifyChain(ctx, 1, int64(n))
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem != nil {
		t.Fatalf("并发追加后链应完好（advisory lock 串行化）: %+v", problem)
	}
	tipSeq, _, err := s.Tip(ctx)
	if err != nil || tipSeq != int64(n) {
		t.Fatalf("链尖 sequence = %d, want %d (%v)", tipSeq, n, err)
	}
}

func TestGetByActionRunID(t *testing.T) {
	// XM-ACTIONS0：操作与审批页「执行记录」详情用它关联 before/after 摘要
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	e := evt("finance.upstream_account.set")
	e.BeforeSummary = map[string]any{"status": "disabled"}
	written, err := s.Append(ctx, e)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, ok, err := s.GetByActionRunID(ctx, written.ActionRunID)
	if err != nil {
		t.Fatalf("GetByActionRunID: %v", err)
	}
	if !ok {
		t.Fatal("应能读到刚写入的关联事件")
	}
	if got.ID != written.ID || got.ActionID != written.ActionID {
		t.Fatalf("读回的事件与写入不符: %+v vs %+v", got, written)
	}
	if got.BeforeSummary["status"] != "disabled" || got.AfterSummary["status"] != "active" {
		t.Fatalf("前后摘要不符: before=%v after=%v", got.BeforeSummary, got.AfterSummary)
	}

	_, ok, err = s.GetByActionRunID(ctx, uuid.New())
	if err != nil {
		t.Fatalf("GetByActionRunID(不存在): %v", err)
	}
	if ok {
		t.Fatal("不存在的 action_run_id 应返回 ok=false")
	}
}

func countEvents(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit.audit_event").Scan(&n); err != nil {
		t.Fatalf("统计审计事件失败: %v", err)
	}
	return n
}
