package approval_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func newService(t *testing.T, pool *pgxpool.Pool, now func() time.Time) *approval.Service {
	t.Helper()
	return approval.NewService(approval.NewPgStore(pool, testEnv, now), approval.DefaultPolicy(), now)
}

func humanPrincipal(id string, scopes ...string) principal.Principal {
	return principal.Principal{
		ID: id, Type: principal.TypeHuman,
		IdentityZone: "staff", Issuer: "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: testEnv, Scopes: scopes,
	}
}

func submission(level action.RiskLevel, requester principal.Principal) action.ApprovalSubmission {
	return action.ApprovalSubmission{
		ActionID:      "registry.connection.set_status",
		ActionVersion: "1",
		RiskLevel:     level,
		Params:        map[string]any{"connection_id": "c-1", "status": "ACTIVE"},
		Requester:     requester,
		Reason:        "上游换域名，需要重新登记",
		RequestID:     "req-1",
	}
}

// submit 落一张单并登记清理。
func submit(t *testing.T, svc *approval.Service, pool *pgxpool.Pool, in action.ApprovalSubmission) string {
	t.Helper()
	id, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM core.approval_request WHERE id=$1`, id)
	})
	return id
}

func TestServiceSubmitFreezesParamsAndSetsTTL(t *testing.T) {
	pool := testPool(t)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	svc := newService(t, pool, func() time.Time { return now })
	alice := humanPrincipal("staff_alice")

	id := submit(t, svc, pool, submission(action.L2, alice))
	req, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Status != approval.StatusPending || req.RiskLevel != "L2" || req.RequesterID != "staff_alice" {
		t.Fatalf("单的内容不对：%+v", req)
	}
	// TTL 按等级取自 Policy——L2 是 24h。
	if want := now.Add(24 * time.Hour); !req.ExpiresAt.Equal(want) {
		t.Fatalf("有效期不对：%s，期望 %s", req.ExpiresAt, want)
	}
	hash, _ := approval.HashParams(map[string]any{"connection_id": "c-1", "status": "ACTIVE"})
	if req.ParamsHash != hash {
		t.Fatalf("参数哈希没冻结对：%s", req.ParamsHash)
	}
}

// TestUnconfiguredLevelFailsClosedEverywhere：一个等级在 Policy 里漏配了会怎样。
//
// 三处各自朝安全的一侧倒，合起来的结局是「落得下单、批不了、很快过期」：
// TTLFor 退化到**最短**的那一档而不是最长或无限；Settle 对未配票数的等级
// 一律返回 PENDING；于是这张单只能等到过期。
//
// 写这条是因为我原先在 Service 里加了一道 `ttl <= 0 就拒绝` 的守卫，而
// TTLFor 从不返回 0——那是一段读起来像保护、实际永不生效的死代码。真正的
// 保护在 Settle，就该在这里钉住。
func TestUnconfiguredLevelFailsClosedEverywhere(t *testing.T) {
	pool := testPool(t)
	policy := approval.DefaultPolicy()
	delete(policy.TTL, "L4")
	delete(policy.VotesRequired, "L4")
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	svc := approval.NewService(approval.NewPgStore(pool, testEnv, func() time.Time { return now }),
		policy, func() time.Time { return now })

	id, err := svc.Submit(context.Background(), submission(action.L4, humanPrincipal("staff_alice")))
	if err != nil {
		t.Fatalf("漏配的等级仍然落得下单（这是刻意的）：%v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM core.approval_request WHERE id=$1`, id)
	})
	req, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// 退化到最短的一档（DefaultPolicy 删掉 L4 之后剩 L2/L3 都是 24h）。
	if want := now.Add(24 * time.Hour); !req.ExpiresAt.Equal(want) {
		t.Fatalf("有效期该退化到最短的一档：%s，期望 %s", req.ExpiresAt, want)
	}
	// 关键：怎么投都批不了。
	for _, who := range []string{"staff_bob", "staff_carol", "staff_dave"} {
		got, err := svc.Vote(context.Background(), id, humanPrincipal(who, approval.ScopeDecide, approval.ScopeL4),
			approval.VerdictApprove, "")
		if err != nil {
			t.Fatalf("投票本身不该失败：%v", err)
		}
		if got.Status != approval.StatusPending {
			t.Fatalf("未配票数的等级不该被批准，got %s（%d 票）", got.Status, len(got.Decisions))
		}
	}
	// 对照：把票数配回去，同样三票就该批——确认卡住的是漏配而不是别的。
	policy2 := approval.DefaultPolicy()
	svc2 := approval.NewService(approval.NewPgStore(pool, testEnv, func() time.Time { return now }),
		policy2, func() time.Time { return now })
	id2 := submit(t, svc2, pool, submission(action.L4, humanPrincipal("staff_alice")))
	if _, err := svc2.Vote(context.Background(), id2, humanPrincipal("staff_bob", approval.ScopeDecide),
		approval.VerdictApprove, ""); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	got, err := svc2.Vote(context.Background(), id2,
		humanPrincipal("staff_carol", approval.ScopeDecide, approval.ScopeL4), approval.VerdictApprove, "")
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if got.Status != approval.StatusApproved {
		t.Fatalf("配齐票数后应当批准，got %s", got.Status)
	}
}

// TestServicePeekRefusesCorruptedParams：params_json 与 params_hash 对不上时拒绝。
//
// 正常写路径产生不了这种行（Create 同时写两列），所以这里直接改库造出来——
// 这正是这道校验存在的场景：有人只改了两列中的一列。
func TestServicePeekRefusesCorruptedParams(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, nil)
	id := submit(t, svc, pool, submission(action.L2, humanPrincipal("staff_alice")))

	// 先确认没篡改时读得出来——否则下面的「读不出来」可能与篡改无关。
	if _, err := svc.Peek(context.Background(), id); err != nil {
		t.Fatalf("未篡改时应当读得出来：%v", err)
	}

	if _, err := pool.Exec(context.Background(),
		`UPDATE core.approval_request SET params_json=$2 WHERE id=$1`,
		uuid.MustParse(id), []byte(`{"connection_id":"c-1","status":"DISABLED"}`)); err != nil {
		t.Fatalf("造篡改行失败: %v", err)
	}

	if _, err := svc.Peek(context.Background(), id); action.ErrorCode(err) != action.CodeConflict {
		t.Fatalf("参数与哈希不一致时必须拒绝，got %v", err)
	}
}

func TestServiceVoteMapsDomainErrorsToCodes(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, nil)
	alice := humanPrincipal("staff_alice", approval.ScopeDecide)
	bob := humanPrincipal("staff_bob", approval.ScopeDecide)

	t.Run("AI 不能投票", func(t *testing.T) {
		id := submit(t, svc, pool, submission(action.L2, alice))
		ai := humanPrincipal("agent_claude", approval.ScopeDecide, approval.ScopeL4)
		ai.Type = principal.TypeAI
		ai.IdentityZone = "ai"
		_, err := svc.Vote(context.Background(), id, ai, approval.VerdictApprove, "")
		if action.ErrorCode(err) != action.CodePrincipalTypeNotAllowed {
			t.Fatalf("宪法 24 条：AI 不作为审批人，got %v", err)
		}
		// 对照：同一张单换成自然人就投得下去——确认拒的是身份类型，
		// 而不是这张单不收票。
		if _, err := svc.Vote(context.Background(), id, bob, approval.VerdictApprove, ""); err != nil {
			t.Fatalf("自然人应当投得下去：%v", err)
		}
	})

	t.Run("缺 decide 权限", func(t *testing.T) {
		id := submit(t, svc, pool, submission(action.L2, alice))
		noScope := humanPrincipal("staff_carol")
		if _, err := svc.Vote(context.Background(), id, noScope, approval.VerdictApprove, ""); action.ErrorCode(err) != action.CodePermissionDenied {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("重复投票", func(t *testing.T) {
		id := submit(t, svc, pool, submission(action.L3, alice))
		if _, err := svc.Vote(context.Background(), id, bob, approval.VerdictApprove, ""); err != nil {
			t.Fatalf("第一票: %v", err)
		}
		if _, err := svc.Vote(context.Background(), id, bob, approval.VerdictApprove, ""); action.ErrorCode(err) != action.CodeConflict {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("单号形态不对与不存在同码", func(t *testing.T) {
		for _, id := range []string{"not-a-uuid", uuid.NewString()} {
			if _, err := svc.Vote(context.Background(), id, bob, approval.VerdictApprove, ""); action.ErrorCode(err) != action.CodeApprovalNotFound {
				t.Fatalf("id=%q got %v", id, err)
			}
		}
	})
}

// TestServiceVoteFreezesPrivilegeFromScope：特权与否取自投票那一刻持有的 scope。
func TestServiceVoteFreezesPrivilegeFromScope(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, nil)
	alice := humanPrincipal("staff_alice")
	id := submit(t, svc, pool, submission(action.L4, alice))

	bob := humanPrincipal("staff_bob", approval.ScopeDecide)
	carol := humanPrincipal("staff_carol", approval.ScopeDecide, approval.ScopeL4)

	if _, err := svc.Vote(context.Background(), id, bob, approval.VerdictApprove, "普通票"); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	req, err := svc.Vote(context.Background(), id, carol, approval.VerdictApprove, "特权票")
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if req.Status != approval.StatusApproved {
		t.Fatalf("L4 两票含一张特权票该批，got %s", req.Status)
	}
	privileged := 0
	for _, d := range req.Decisions {
		if d.Privileged {
			privileged++
		}
	}
	if privileged != 1 {
		t.Fatalf("特权票数不对：%d", privileged)
	}
}

// TestServiceClaimIsAtMostOnce：占用一次之后再占用要拿 CONFLICT。
func TestServiceClaimIsAtMostOnce(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, nil)
	alice := humanPrincipal("staff_alice")
	id := submit(t, svc, pool, submission(action.L2, alice))

	// 还没批就占用 → PRECONDITION_FAILED。
	if err := svc.Claim(context.Background(), id, uuid.New(), nil); action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("未批准就占用，got %v", err)
	}

	if _, err := svc.Vote(context.Background(), id,
		humanPrincipal("staff_bob", approval.ScopeDecide), approval.VerdictApprove, ""); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	runID := uuid.New()
	if err := svc.Claim(context.Background(), id, runID, nil); err != nil {
		t.Fatalf("首次占用应当成功：%v", err)
	}
	if err := svc.Claim(context.Background(), id, uuid.New(), nil); action.ErrorCode(err) != action.CodeConflict {
		t.Fatalf("重复占用，got %v", err)
	}
	req, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Status != approval.StatusExecuted || req.ExecutionRunID == nil || *req.ExecutionRunID != runID {
		t.Fatalf("执行痕迹不对：%+v", req)
	}
}

// TestServiceClaimComparesDeclaredParams：调用方声明的参数不一致即拒，
// 且**不占用**——否则一次拼错的执行就烧掉了单。
func TestServiceClaimComparesDeclaredParams(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, nil)
	id := submit(t, svc, pool, submission(action.L2, humanPrincipal("staff_alice")))
	if _, err := svc.Vote(context.Background(), id,
		humanPrincipal("staff_bob", approval.ScopeDecide), approval.VerdictApprove, ""); err != nil {
		t.Fatalf("Vote: %v", err)
	}

	drifted := map[string]any{"connection_id": "c-1", "status": "DISABLED"}
	if err := svc.Claim(context.Background(), id, uuid.New(), drifted); action.ErrorCode(err) != action.CodeConflict {
		t.Fatalf("参数漂移必须拒绝，got %v", err)
	}
	req, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Status != approval.StatusApproved || req.ExecutionRunID != nil {
		t.Fatalf("参数漂移不该烧掉这张单：%+v", req)
	}

	// 对照：声明一致就占得下去——确认拒的是漂移。
	same := map[string]any{"status": "ACTIVE", "connection_id": "c-1"} // 键序不同，哈希应当相同
	if err := svc.Claim(context.Background(), id, uuid.New(), same); err != nil {
		t.Fatalf("参数一致时应当占得下去：%v", err)
	}
}

// TestServiceClaimRefusesExpired：过期的单不能执行，即便它已经批准。
func TestServiceClaimRefusesExpired(t *testing.T) {
	pool := testPool(t)
	base := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	clock := base
	svc := newService(t, pool, func() time.Time { return clock })
	id := submit(t, svc, pool, submission(action.L2, humanPrincipal("staff_alice")))
	if _, err := svc.Vote(context.Background(), id,
		humanPrincipal("staff_bob", approval.ScopeDecide), approval.VerdictApprove, ""); err != nil {
		t.Fatalf("Vote: %v", err)
	}

	clock = base.Add(25 * time.Hour) // 越过 L2 的 24h
	if err := svc.Claim(context.Background(), id, uuid.New(), nil); action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("过期的单不该能执行，got %v", err)
	}

	// 对照：把钟拨回有效期内就执行得了——确认拒的是过期。
	clock = base.Add(time.Hour)
	if err := svc.Claim(context.Background(), id, uuid.New(), nil); err != nil {
		t.Fatalf("有效期内应当执行得了：%v", err)
	}
}

// TestServiceCancelOnlyByRequester。
func TestServiceCancelOnlyByRequester(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, nil)
	alice := humanPrincipal("staff_alice")
	id := submit(t, svc, pool, submission(action.L2, alice))

	if err := svc.Cancel(context.Background(), id, humanPrincipal("staff_bob")); action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("别人不该撤得掉，got %v", err)
	}
	if err := svc.Cancel(context.Background(), id, alice); err != nil {
		t.Fatalf("提交人应当撤得掉：%v", err)
	}
	req, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Status != approval.StatusCancelled {
		t.Fatalf("got %s", req.Status)
	}
}

// TestServiceSatisfiesKernelGateway 静态钉住 Service 就是内核要的那个网关。
// 这条不跑数据库，纯编译期断言，放在这里是为了让「谁实现了它」在测试里可见。
func TestServiceSatisfiesKernelGateway(t *testing.T) {
	var _ action.ApprovalGateway = (*approval.Service)(nil)
}
