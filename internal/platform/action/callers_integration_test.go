package action_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 本文件跑在真库上：这条汇总是一句 GROUP BY，用假货复刻只会测到假货。
//
// action_run 是 append-only 的共享表，别的用例也会往里写行——所以这里
// **不 TRUNCATE**（那会把并发跑的其它用例的行删掉），改用一个每次运行都
// 唯一的 principal_id 前缀把自己的行圈出来，断言只看自己那几行。

func callerRun(principalID string, ptype principal.Type, actionID string,
	status action.RunStatus, code action.Code, startedAt time.Time) action.Run {
	return action.Run{
		ID:            uuid.New(),
		ActionID:      actionID,
		ActionVersion: "1",
		PrincipalID:   principalID,
		PrincipalType: ptype,
		Environment:   "production",
		RequestID:     "req-" + uuid.NewString(),
		RiskLevel:     action.L1,
		Status:        status,
		ErrorCode:     code,
		DurationMS:    7,
		StartedAt:     startedAt,
		FinishedAt:    startedAt.Add(7 * time.Millisecond),
	}
}

func TestAggregateCallersSummarisesPerPrincipal(t *testing.T) {
	pool := testPool(t)
	runs := action.NewPgRunStore(pool, quietLogger())
	callers := action.NewPgCallerStore(pool)
	ctx := context.Background()

	unique := uuid.NewString()[:8]
	busy := "svc-busy-" + unique
	quiet := "svc-quiet-" + unique
	stale := "svc-stale-" + unique

	now := time.Now().UTC()
	for _, r := range []action.Run{
		callerRun(busy, principal.TypeService, "registry.service.create",
			action.RunSucceeded, "", now.Add(-3*time.Hour)),
		callerRun(busy, principal.TypeService, "registry.connection.create",
			action.RunFailed, action.CodePermissionDenied, now.Add(-2*time.Hour)),
		callerRun(busy, principal.TypeService, "server.asset.set",
			action.RunSucceeded, "", now.Add(-1*time.Hour)),
		callerRun(quiet, principal.TypeAI, "server.domain.set",
			action.RunSucceeded, "", now.Add(-30*time.Minute)),
		// 窗口之外的一条：不该出现在汇总里。
		callerRun(stale, principal.TypeService, "server.asset.set",
			action.RunSucceeded, "", now.Add(-40*24*time.Hour)),
	} {
		if err := runs.InsertRun(ctx, r); err != nil {
			t.Fatalf("InsertRun: %v", err)
		}
	}

	page, err := callers.AggregateCallers(ctx, "production", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("AggregateCallers: %v", err)
	}
	got := map[string]action.CallerActivity{}
	for _, a := range page.Items {
		got[a.PrincipalID] = a
	}

	b, ok := got[busy]
	if !ok {
		t.Fatalf("窗口内活跃的身份应出现在汇总里，实际 %v", page.Items)
	}
	if b.RunCount != 3 || b.FailedCount != 1 {
		t.Fatalf("计数应是 3 次 / 1 次失败，实际 %d / %d", b.RunCount, b.FailedCount)
	}
	if b.PrincipalType != string(principal.TypeService) {
		t.Fatalf("身份类型应带出，实际 %q", b.PrincipalType)
	}
	// 「最近一次」按 started_at 取，不是按插入顺序。
	if b.LastActionID != "server.asset.set" || b.LastStatus != "succeeded" {
		t.Fatalf("最近一次应是 server.asset.set/succeeded，实际 %s/%s", b.LastActionID, b.LastStatus)
	}
	if !b.FirstSeenAt.Before(b.LastSeenAt) {
		t.Fatalf("首次应早于最近一次，实际 %s / %s", b.FirstSeenAt, b.LastSeenAt)
	}

	if q, ok := got[quiet]; !ok || q.RunCount != 1 || q.FailedCount != 0 {
		t.Fatalf("只跑过一次的身份应计 1 次 0 失败，实际 %+v ok=%v", q, ok)
	}

	// 窗口之外的那一条不该进来。这是缺席型断言，所以上面每一条正向断言
	// 都先立住了——它们证明这次汇总确实读到了数据，"stale 不在里面"
	// 才不是因为整个查询返回了空。
	if _, present := got[stale]; present {
		t.Fatalf("窗口之外的调用不该出现在汇总里，实际 %+v", got[stale])
	}
}

// TestAggregateCallersIsEnvironmentScoped 钉住环境隔离：一个身份在
// production 的调用不该出现在 staging 的汇总里（宪法 15 条）。
func TestAggregateCallersIsEnvironmentScoped(t *testing.T) {
	pool := testPool(t)
	runs := action.NewPgRunStore(pool, quietLogger())
	callers := action.NewPgCallerStore(pool)
	ctx := context.Background()

	unique := uuid.NewString()[:8]
	who := "svc-envscope-" + unique
	now := time.Now().UTC()
	if err := runs.InsertRun(ctx, callerRun(who, principal.TypeService,
		"server.asset.set", action.RunSucceeded, "", now.Add(-time.Hour))); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	// 正向锚点：本环境查得到。
	prod, err := callers.AggregateCallers(ctx, "production", now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("AggregateCallers(production): %v", err)
	}
	if !containsCaller(prod.Items, who) {
		t.Fatalf("锚点：production 应查得到 %s", who)
	}

	staging, err := callers.AggregateCallers(ctx, "staging", now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("AggregateCallers(staging): %v", err)
	}
	if containsCaller(staging.Items, who) {
		t.Fatalf("staging 不该看到 production 的调用方 %s", who)
	}
}

func TestAggregateCallersRejectsEmptyEnvironment(t *testing.T) {
	callers := action.NewPgCallerStore(testPool(t))
	if _, err := callers.AggregateCallers(context.Background(), "", time.Now()); err == nil {
		t.Fatal("空环境应被拒：环境绝不该由调用方留空后回落成「全部」")
	}
}

func containsCaller(items []action.CallerActivity, principalID string) bool {
	for _, a := range items {
		if a.PrincipalID == principalID {
			return true
		}
	}
	return false
}
