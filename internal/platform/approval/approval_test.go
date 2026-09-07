package approval

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func humanApprover(id string, scopes ...string) principal.Principal {
	return principal.Principal{
		ID: id, Type: principal.TypeHuman, IdentityZone: "staff",
		Environment: "production", Scopes: scopes,
	}
}

func pendingRequest(risk, requester string, now time.Time, policy Policy) Request {
	return Request{
		ID: uuid.New(), ActionID: "registry.connector.create", ActionVersion: "1",
		Params: map[string]any{"name": "sub2api"}, ParamsHash: mustHash(map[string]any{"name": "sub2api"}),
		RiskLevel: risk, Environment: "production",
		RequesterID: requester, RequesterType: principal.TypeHuman,
		Reason: "登记新的上游连接器", Status: StatusPending,
		CreatedAt: now, ExpiresAt: now.Add(policy.TTLFor(risk)),
	}
}

func mustHash(params map[string]any) string {
	h, err := HashParams(params)
	if err != nil {
		panic(err)
	}
	return h
}

// TestAIPrincipalCanNeverVote 是**宪法 24 条的代码侧证据**：AI 不作为审批人。
//
// 这条断言的形状是「本该被拒绝的操作确实被拒绝」，容易写成恒真——比如身份
// 恰好也缺 approval.decide，那么就算红线没实现它也会「通过」。所以这里给 AI
// **配齐了全部权限**（decide + l4），让它在除身份类别之外的每一项上都合格：
// 唯一能让它被拒的理由只剩「它是 AI」。
func TestAIPrincipalCanNeverVote(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	req := pendingRequest("L3", "human-requester", now, policy)

	ai := principal.Principal{
		ID: "ai-claude", Type: principal.TypeAI, IdentityZone: "machine",
		Environment: "production", Scopes: []string{ScopeDecide, ScopeL4},
	}
	if err := policy.CanVote(req, ai, now); !errors.Is(err, ErrApproverNotHuman) {
		t.Fatalf("AI 拿到的拒绝理由必须是身份类别，got %v", err)
	}

	// 对照组：同样权限、同样单，只把身份类别换成 HUMAN 就应当放行。
	// 没有这一组，上面那条断言可能只是因为别的原因恒真。
	human := humanApprover("human-approver", ScopeDecide, ScopeL4)
	if err := policy.CanVote(req, human, now); err != nil {
		t.Fatalf("同等条件下的 HUMAN 应当可以投票，got %v", err)
	}

	// 其它机器身份同样不行——红线是「必须 HUMAN」，不是「不是 AI 就行」。
	for _, machine := range []principal.Type{principal.TypeService, principal.TypeServerAgent} {
		p := principal.Principal{
			ID: "machine-" + string(machine), Type: machine, IdentityZone: "machine",
			Environment: "production", Scopes: []string{ScopeDecide, ScopeL4},
		}
		if err := policy.CanVote(req, p, now); !errors.Is(err, ErrApproverNotHuman) {
			t.Fatalf("%s 也不该能投票，got %v", machine, err)
		}
	}
}

func TestSelfApprovalOnlyAllowedAtL2(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	requester := humanApprover("alice", ScopeDecide, ScopeL4)

	l2 := pendingRequest("L2", "alice", now, policy)
	if err := policy.CanVote(l2, requester, now); err != nil {
		t.Fatalf("L2 允许自批（单票即自批，等级本意如此），got %v", err)
	}

	for _, risk := range []string{"L3", "L4"} {
		req := pendingRequest(risk, "alice", now, policy)
		if err := policy.CanVote(req, requester, now); !errors.Is(err, ErrApproverIsRequester) {
			t.Fatalf("%s 不允许自批，got %v", risk, err)
		}
		// 别人投同一张单则可以——确认上面拒的是「自批」而不是别的什么。
		if err := policy.CanVote(req, humanApprover("bob", ScopeDecide, ScopeL4), now); err != nil {
			t.Fatalf("%s 的他人投票应当放行，got %v", risk, err)
		}
	}
}

func TestSettleCountsVotesAndPrivilege(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()

	t.Run("L2 一票即通过", func(t *testing.T) {
		req := pendingRequest("L2", "alice", now, policy)
		got := policy.Settle(req, []Decision{{Verdict: VerdictApprove, ApproverID: "alice"}})
		if got != StatusApproved {
			t.Fatalf("status=%s", got)
		}
	})

	t.Run("L3 一票不够、两票通过", func(t *testing.T) {
		req := pendingRequest("L3", "alice", now, policy)
		if got := policy.Settle(req, []Decision{{Verdict: VerdictApprove, ApproverID: "bob"}}); got != StatusPending {
			t.Fatalf("一票就通过了 L3：status=%s", got)
		}
		two := []Decision{{Verdict: VerdictApprove, ApproverID: "bob"}, {Verdict: VerdictApprove, ApproverID: "carol"}}
		if got := policy.Settle(req, two); got != StatusApproved {
			t.Fatalf("两票仍未通过 L3：status=%s", got)
		}
	})

	t.Run("L4 两票但都不是特权票则仍然挂起", func(t *testing.T) {
		req := pendingRequest("L4", "alice", now, policy)
		plain := []Decision{
			{Verdict: VerdictApprove, ApproverID: "bob"},
			{Verdict: VerdictApprove, ApproverID: "carol"},
		}
		if got := policy.Settle(req, plain); got != StatusPending {
			t.Fatalf("L4 缺特权票却通过了：status=%s", got)
		}
		withPrivileged := []Decision{
			{Verdict: VerdictApprove, ApproverID: "bob"},
			{Verdict: VerdictApprove, ApproverID: "carol", Privileged: true},
		}
		if got := policy.Settle(req, withPrivileged); got != StatusApproved {
			t.Fatalf("L4 齐了两票含特权票仍未通过：status=%s", got)
		}
	})

	t.Run("一票反对即驳回", func(t *testing.T) {
		req := pendingRequest("L4", "alice", now, policy)
		mixed := []Decision{
			{Verdict: VerdictApprove, ApproverID: "bob", Privileged: true},
			{Verdict: VerdictReject, ApproverID: "carol"},
			{Verdict: VerdictApprove, ApproverID: "dave", Privileged: true},
		}
		if got := policy.Settle(req, mixed); got != StatusRejected {
			t.Fatalf("审批是全体同意而非多数同意：status=%s", got)
		}
	})

	t.Run("未配票数的等级不放行", func(t *testing.T) {
		req := pendingRequest("L9", "alice", now, policy)
		many := []Decision{
			{Verdict: VerdictApprove, ApproverID: "b", Privileged: true},
			{Verdict: VerdictApprove, ApproverID: "c", Privileged: true},
			{Verdict: VerdictApprove, ApproverID: "d", Privileged: true},
		}
		if got := policy.Settle(req, many); got != StatusPending {
			t.Fatalf("等级表演进时漏配票数必须卡住而不是零票通过：status=%s", got)
		}
	})
}

func TestExpiryBlocksVotingAndExecution(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	req := pendingRequest("L3", "alice", now, policy)
	after := req.ExpiresAt.Add(time.Second)

	if err := policy.CanVote(req, humanApprover("bob", ScopeDecide), after); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期后不该还能投票，got %v", err)
	}

	approved := req
	approved.Status = StatusApproved
	decided := now
	approved.DecidedAt = &decided
	if err := policy.CanExecute(approved, req.Params, after); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期后不该还能执行，got %v", err)
	}
	// 对照：没过期时同一张单可以执行，确认上面拒的确实是过期。
	if err := policy.CanExecute(approved, req.Params, now.Add(time.Minute)); err != nil {
		t.Fatalf("未过期的已批准单应当可执行，got %v", err)
	}
}

// TestExecutionRefusesDriftedParams 钉住「审批过的是这份参数，不是这个意图的
// 任意版本」——没有它，「批准一件小事、执行一件大事」就成立了。
func TestExecutionRefusesDriftedParams(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	req := pendingRequest("L3", "alice", now, policy)
	req.Status = StatusApproved
	decided := now
	req.DecidedAt = &decided

	drifted := map[string]any{"name": "sub2api", "extra": "偷偷加的参数"}
	if err := policy.CanExecute(req, drifted, now.Add(time.Minute)); !errors.Is(err, ErrParamsDrifted) {
		t.Fatalf("参数漂移必须拒绝，got %v", err)
	}
	if err := policy.CanExecute(req, req.Params, now.Add(time.Minute)); err != nil {
		t.Fatalf("原样参数应当放行，got %v", err)
	}
}

func TestExecutionIsAtMostOnce(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	req := pendingRequest("L2", "alice", now, policy)
	req.Status = StatusExecuted
	runID := uuid.New()
	req.ExecutionRunID = &runID

	if err := policy.CanExecute(req, req.Params, now.Add(time.Minute)); !errors.Is(err, ErrAlreadyExecuted) {
		t.Fatalf("一单最多一跑，got %v", err)
	}
}

func TestExecutionWindowClosesAfterApproval(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	policy.TTL = map[string]time.Duration{"L3": 30 * 24 * time.Hour} // 让有效期不成为干扰项
	req := pendingRequest("L3", "alice", now, policy)
	req.Status = StatusApproved
	decided := now
	req.DecidedAt = &decided

	past := now.Add(policy.ExecutionWindow + time.Minute)
	if err := policy.CanExecute(req, req.Params, past); !errors.Is(err, ErrExpired) {
		t.Fatalf("批准后超出执行窗口应当失效，got %v", err)
	}
	if err := policy.CanExecute(req, req.Params, now.Add(policy.ExecutionWindow-time.Minute)); err != nil {
		t.Fatalf("窗口内应当可执行，got %v", err)
	}
}

func TestHashParamsIsStableAcrossKeyOrder(t *testing.T) {
	a, err := HashParams(map[string]any{"b": 2, "a": 1, "c": map[string]any{"z": 1, "y": 2}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashParams(map[string]any{"c": map[string]any{"y": 2, "z": 1}, "a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("同一份参数的哈希必须稳定：%s vs %s", a, b)
	}
	// nil 与空 map 必须同哈希，否则「不传参数」和「传了个空对象」会成为两张不同的单。
	empty, _ := HashParams(map[string]any{})
	nilHash, _ := HashParams(nil)
	if empty != nilHash {
		t.Fatalf("nil 与空参数的哈希不一致：%s vs %s", nilHash, empty)
	}
	if a == empty {
		t.Fatal("不同参数不该同哈希")
	}
}

func TestDuplicateVoteRejected(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	req := pendingRequest("L3", "alice", now, policy)
	req.Decisions = []Decision{{ApproverID: "bob", Verdict: VerdictApprove, ApproverType: principal.TypeHuman}}

	if err := policy.CanVote(req, humanApprover("bob", ScopeDecide), now); !errors.Is(err, ErrDuplicateVote) {
		t.Fatalf("一人一单一票，got %v", err)
	}
	if err := policy.CanVote(req, humanApprover("carol", ScopeDecide), now); err != nil {
		t.Fatalf("另一个人应当还能投，got %v", err)
	}
}

func TestMissingDecideScopeRejected(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultPolicy()
	req := pendingRequest("L2", "alice", now, policy)

	if err := policy.CanVote(req, humanApprover("bob"), now); !errors.Is(err, ErrMissingDecideScope) {
		t.Fatalf("没有 approval.decide 不该能投票，got %v", err)
	}
}
