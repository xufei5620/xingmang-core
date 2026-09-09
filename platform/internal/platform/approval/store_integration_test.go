package approval_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const testEnv = "staging"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newStore(t *testing.T, pool *pgxpool.Pool, now func() time.Time) *approval.PgStore {
	t.Helper()
	return approval.NewPgStore(pool, testEnv, now)
}

// seed 落一张 PENDING 单，返回它。测试结束清掉自己的行——测试库是 worktree
// 独享的，但同一次跑里的用例之间仍然共享，不清会互相看到对方的单。
func seed(t *testing.T, s *approval.PgStore, pool *pgxpool.Pool, level string, requester string) approval.Request {
	t.Helper()
	params := map[string]any{"connector_id": "sub2api", "status": "ACTIVE"}
	hash, err := approval.HashParams(params)
	if err != nil {
		t.Fatalf("HashParams: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	req := approval.Request{
		ID:            uuid.New(),
		ActionID:      "registry.connection.set_status",
		ActionVersion: "1",
		Params:        params,
		ParamsHash:    hash,
		RiskLevel:     level,
		Environment:   testEnv,
		RequesterID:   requester,
		RequesterType: principal.TypeHuman,
		Reason:        "上游换域名，需要重新登记",
		ExpiresAt:     now.Add(24 * time.Hour),
		CreatedAt:     now,
	}
	if err := s.Create(context.Background(), req); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM core.approval_request WHERE id=$1`, req.ID)
	})
	return req
}

// voter 造一个有投票资格的自然人；privileged 决定他是否持 approval.l4。
// 资格取自 principal 而不是 Decision——Store 按前者覆写后者（见 Store.Vote）。
func voter(id string, privileged bool) principal.Principal {
	scopes := []string{approval.ScopeDecide}
	if privileged {
		scopes = append(scopes, approval.ScopeL4)
	}
	return principal.Principal{
		ID: id, Type: principal.TypeHuman,
		IdentityZone: "staff", Issuer: "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: testEnv, Scopes: scopes,
	}
}

func vote(verdict approval.Verdict) approval.Decision {
	return approval.Decision{ID: uuid.New(), Verdict: verdict, Comment: "看过了"}
}

func TestCreateAndGetRoundTrips(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	req := seed(t, s, pool, "L2", "staff_alice")

	got, err := s.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != approval.StatusPending || got.ActionID != req.ActionID ||
		got.ParamsHash != req.ParamsHash || got.Reason != req.Reason {
		t.Fatalf("单没有原样取回：%+v", got)
	}
	// 参数必须逐字段回来——它是审批的标的，掉一个字段就等于批了另一件事。
	if got.Params["connector_id"] != "sub2api" || got.Params["status"] != "ACTIVE" {
		t.Fatalf("参数没有原样取回：%+v", got.Params)
	}
	if len(got.Decisions) != 0 {
		t.Fatalf("新单不该带票：%+v", got.Decisions)
	}
	if got.DecidedAt != nil || got.ExecutedAt != nil || got.ExecutionRunID != nil {
		t.Fatalf("新单的三个终态字段都该是空：%+v", got)
	}

	if _, err := s.Get(context.Background(), uuid.New()); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("不存在的单应当 ErrNotFound，got %v", err)
	}
}

// TestGetIsEnvironmentScoped：跨环境读不到。环境边界由 Store 守，
// 不指望调用方每次都记得加条件。
func TestGetIsEnvironmentScoped(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	req := seed(t, s, pool, "L2", "staff_alice")

	other := approval.NewPgStore(pool, "production", nil)
	if _, err := other.Get(context.Background(), req.ID); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("另一个环境不该看得到这张单，got %v", err)
	}
	// 对照：同环境读得到——确认上面拒的是环境而不是单本身没建起来。
	if _, err := s.Get(context.Background(), req.ID); err != nil {
		t.Fatalf("同环境应当读得到：%v", err)
	}
}

func TestVoteSettlesAtRequiredCount(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	policy := approval.DefaultPolicy()

	t.Run("L2 一票即批", func(t *testing.T) {
		req := seed(t, s, pool, "L2", "staff_alice")
		got, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy)
		if err != nil {
			t.Fatalf("Vote: %v", err)
		}
		if got.Status != approval.StatusApproved {
			t.Fatalf("L2 一票该批，got %s", got.Status)
		}
		if got.DecidedAt == nil {
			t.Fatal("落定必须写 decided_at")
		}
		if len(got.Decisions) != 1 {
			t.Fatalf("票要留下：%+v", got.Decisions)
		}
	})

	t.Run("L3 一票还不够两票才批", func(t *testing.T) {
		req := seed(t, s, pool, "L3", "staff_alice")
		got, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy)
		if err != nil {
			t.Fatalf("第一票: %v", err)
		}
		if got.Status != approval.StatusPending {
			t.Fatalf("L3 一票不该批，got %s", got.Status)
		}
		if got.DecidedAt != nil {
			t.Fatal("还没落定就不该写 decided_at")
		}
		got, err = s.Vote(context.Background(), req.ID, voter("staff_carol", false), vote(approval.VerdictApprove), policy)
		if err != nil {
			t.Fatalf("第二票: %v", err)
		}
		if got.Status != approval.StatusApproved || len(got.Decisions) != 2 {
			t.Fatalf("L3 两票该批，got %s / %d 票", got.Status, len(got.Decisions))
		}
	})

	t.Run("一张 REJECT 立即驳回", func(t *testing.T) {
		req := seed(t, s, pool, "L3", "staff_alice")
		got, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictReject), policy)
		if err != nil {
			t.Fatalf("Vote: %v", err)
		}
		if got.Status != approval.StatusRejected {
			t.Fatalf("一票驳回就该驳回，got %s", got.Status)
		}
		// 驳回后不再收票。
		err = mustVoteErr(t, s, req.ID, voter("staff_carol", false), vote(approval.VerdictApprove), policy)
		if !errors.Is(err, approval.ErrNotPending) {
			t.Fatalf("驳回后不该再收票，got %v", err)
		}
	})

	t.Run("L4 缺特权票不批", func(t *testing.T) {
		req := seed(t, s, pool, "L4", "staff_alice")
		got, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy)
		if err != nil {
			t.Fatalf("第一票: %v", err)
		}
		got, err = s.Vote(context.Background(), req.ID, voter("staff_carol", false), vote(approval.VerdictApprove), policy)
		if err != nil {
			t.Fatalf("第二票: %v", err)
		}
		if got.Status != approval.StatusPending {
			t.Fatalf("L4 两张普通票不该批——缺特权票，got %s", got.Status)
		}
		// 对照：补一张特权票就该批，确认卡住的是特权票而不是票数。
		got, err = s.Vote(context.Background(), req.ID, voter("staff_dave", true), vote(approval.VerdictApprove), policy)
		if err != nil {
			t.Fatalf("特权票: %v", err)
		}
		if got.Status != approval.StatusApproved {
			t.Fatalf("补上特权票该批，got %s", got.Status)
		}
	})
}

func mustVoteErr(t *testing.T, s *approval.PgStore, id uuid.UUID, who principal.Principal, d approval.Decision, p approval.Policy) error {
	t.Helper()
	_, err := s.Vote(context.Background(), id, who, d, p)
	return err
}

// TestStoreRejectsDuplicateVote：一人一票，库层唯一键兜底。
func TestStoreRejectsDuplicateVote(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	req := seed(t, s, pool, "L3", "staff_alice")
	policy := approval.DefaultPolicy()

	if _, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy); err != nil {
		t.Fatalf("第一票: %v", err)
	}
	err := mustVoteErr(t, s, req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy)
	if !errors.Is(err, approval.ErrDuplicateVote) {
		t.Fatalf("同一人第二票应当 ErrDuplicateVote，got %v", err)
	}
	// 对照：换个人就能投——确认拒的是重复而不是单不收票了。
	if _, err := s.Vote(context.Background(), req.ID, voter("staff_carol", false), vote(approval.VerdictApprove), policy); err != nil {
		t.Fatalf("换人投票应当成功: %v", err)
	}
}

// TestSelfApprovalBlockedAboveL2：L3 提交人不能自批；L2 可以（DefaultPolicy 的裁定）。
func TestSelfApprovalBlockedAboveL2(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	policy := approval.DefaultPolicy()

	l3 := seed(t, s, pool, "L3", "staff_alice")
	err := mustVoteErr(t, s, l3.ID, voter("staff_alice", false), vote(approval.VerdictApprove), policy)
	if !errors.Is(err, approval.ErrApproverIsRequester) {
		t.Fatalf("L3 不许自批，got %v", err)
	}

	l2 := seed(t, s, pool, "L2", "staff_alice")
	got, err := s.Vote(context.Background(), l2.ID, voter("staff_alice", false), vote(approval.VerdictApprove), policy)
	if err != nil {
		t.Fatalf("L2 允许自批: %v", err)
	}
	if got.Status != approval.StatusApproved {
		t.Fatalf("L2 自批应当通过，got %s", got.Status)
	}
}

// meetingPoint 记录**同时**待在临界区里的人数，先到的等后到的、等不到就自己走。
//
// 它挂在 Store 注入的时钟上——Vote 在 s.get(...FOR UPDATE) 拿到行锁**之后**
// 才调 s.now()，所以「谁进得来」恰好就是「谁拿到了锁」。有锁时后来者卡在 get
// 上、进不来，先来者超时独自离开，峰值在飞数是 1；没锁时两个都进得来、会合即
// 放行，峰值是 2。
//
// 关键是「峰值在飞数」而不是「总共来过几个」：有锁时后来者最终也会进来（在先
// 来者提交之后），只是那时先来者早已离开——两者从不重叠。一旦有人是超时离开
// 的，栅栏就撤防，免得后来者再白等一轮。
type meetingPoint struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	want     int
	disarmed bool
	ready    chan struct{}
	timeout  time.Duration
}

func newMeetingPoint(want int, timeout time.Duration) *meetingPoint {
	return &meetingPoint{want: want, ready: make(chan struct{}), timeout: timeout}
}

func (m *meetingPoint) arrive() {
	m.mu.Lock()
	m.inFlight++
	if m.inFlight > m.peak {
		m.peak = m.inFlight
	}
	if m.inFlight == m.want && !m.disarmed {
		close(m.ready)
		m.disarmed = true
	}
	skip := m.disarmed
	m.mu.Unlock()

	if !skip {
		select {
		case <-m.ready:
		case <-time.After(m.timeout):
			m.mu.Lock()
			m.disarmed = true
			m.mu.Unlock()
		}
	}

	m.mu.Lock()
	m.inFlight--
	m.mu.Unlock()
}

func (m *meetingPoint) peakInFlight() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak
}

// TestConcurrentLastVoteSettlesExactlyOnce 是 Vote 用事务加 FOR UPDATE 的**理由**。
//
// 两个人同时投 L3 的第二票。没有行锁时两个事务都会读到「已有一票、还差一票」，
// 于是两票都被收下、两次都把单推进 APPROVED——单上留下三票，其中第三票是在
// 决定作出**之后**才落的。真正危险的是它的近亲：一张 REJECT 与一张 APPROVE
// 并发时，谁最后 UPDATE 谁说了算，已被驳回的单会变成已批准。
//
// 锁住单行之后，后来者读到的是第一票落库之后的状态，直接拿 ErrNotPending。
func TestConcurrentLastVoteSettlesExactlyOnce(t *testing.T) {
	pool := testPool(t)
	policy := approval.DefaultPolicy()
	gate := newMeetingPoint(2, 2*time.Second)
	s := newStore(t, pool, func() time.Time {
		gate.arrive()
		return time.Now().UTC()
	})
	// 铺第一票时不该被栅栏拦住，所以先用不带栅栏的 Store 落单和投第一票。
	plain := newStore(t, pool, nil)
	req := seed(t, plain, pool, "L3", "staff_alice")
	if _, err := plain.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy); err != nil {
		t.Fatalf("第一票: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, who := range []string{"staff_carol", "staff_dave"} {
		wg.Add(1)
		go func(i int, who string) {
			defer wg.Done()
			errs[i] = voteErr(s, req.ID, voter(who, false), vote(approval.VerdictApprove), policy)
		}(i, who)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, approval.ErrNotPending) {
			t.Fatalf("并发投票的失败原因只该是「已落定」，got %v", err)
		}
	}
	// 恰好一票被收下：另一个人到达时单已落定。两票都被收下意味着第二个事务
	// 读到的是过期状态——正是行锁要防的。
	if succeeded != 1 {
		t.Fatalf("并发第二票只该有一张被收下，got %d 张", succeeded)
	}
	if peak := gate.peakInFlight(); peak != 1 {
		t.Fatalf("同时待在临界区里的事务峰值应当是 1，got %d——行锁没拦住", peak)
	}

	final, err := plain.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.Status != approval.StatusApproved {
		t.Fatalf("并发投完必须落到 APPROVED，got %s", final.Status)
	}
	// 票数是露馅点：多出来的那一票是在决定作出之后才落的。
	if len(final.Decisions) != 2 {
		t.Fatalf("单上应当恰好两票，got %d", len(final.Decisions))
	}
}

func voteErr(s *approval.PgStore, id uuid.UUID, who principal.Principal, d approval.Decision, p approval.Policy) error {
	_, err := s.Vote(context.Background(), id, who, d, p)
	return err
}

// TestMarkExecutedIsIdempotentGuard：一单最多一跑。
func TestMarkExecutedIsIdempotentGuard(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	policy := approval.DefaultPolicy()
	req := seed(t, s, pool, "L2", "staff_alice")

	// 还没批准就不能标执行。
	err := s.MarkExecuted(context.Background(), req.ID, uuid.New(), time.Now().UTC())
	if !errors.Is(err, approval.ErrAlreadyExecuted) {
		t.Fatalf("未批准的单不该能标执行，got %v", err)
	}

	if _, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	runID := uuid.New()
	if err := s.MarkExecuted(context.Background(), req.ID, runID, time.Now().UTC()); err != nil {
		t.Fatalf("首次标执行应当成功: %v", err)
	}
	got, err := s.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != approval.StatusExecuted || got.ExecutionRunID == nil || *got.ExecutionRunID != runID {
		t.Fatalf("执行痕迹没落全：%+v", got)
	}

	// 第二次触发必须被挡——重复执行是这套机制最不能出的错。
	if err := s.MarkExecuted(context.Background(), req.ID, uuid.New(), time.Now().UTC()); !errors.Is(err, approval.ErrAlreadyExecuted) {
		t.Fatalf("重复执行应当 ErrAlreadyExecuted，got %v", err)
	}
	after, err := s.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if *after.ExecutionRunID != runID {
		t.Fatalf("被挡住的第二次不该改写 run id：%v", *after.ExecutionRunID)
	}
}

func TestCancelOnlyByRequesterAndOnlyWhilePending(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	policy := approval.DefaultPolicy()

	t.Run("别人撤不了", func(t *testing.T) {
		req := seed(t, s, pool, "L2", "staff_alice")
		if err := s.Cancel(context.Background(), req.ID, "staff_bob", time.Now().UTC()); !errors.Is(err, approval.ErrNotCancellableByOther) {
			t.Fatalf("got %v", err)
		}
		// 对照：本人撤得掉，确认拒的是身份。
		if err := s.Cancel(context.Background(), req.ID, "staff_alice", time.Now().UTC()); err != nil {
			t.Fatalf("提交人应当撤得掉: %v", err)
		}
		got, err := s.Get(context.Background(), req.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != approval.StatusCancelled {
			t.Fatalf("got %s", got.Status)
		}
	})

	t.Run("已批准的撤不了", func(t *testing.T) {
		req := seed(t, s, pool, "L2", "staff_alice")
		if _, err := s.Vote(context.Background(), req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy); err != nil {
			t.Fatalf("Vote: %v", err)
		}
		if err := s.Cancel(context.Background(), req.ID, "staff_alice", time.Now().UTC()); !errors.Is(err, approval.ErrNotCancellableByOther) {
			t.Fatalf("已批准的单不该能撤回，got %v", err)
		}
	})
}

// TestExpirePendingLeavesDecidedAtNull：过期不是一次决定，没有人做过这个决定。
// 库层的 approval_request_decided_when_terminal 也正是这么要求的——这条测试
// 同时证明那条约束没有把 EXPIRED 一起要求上 decided_at。
func TestExpirePendingLeavesDecidedAtNull(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	req := seed(t, s, pool, "L2", "staff_alice")

	// 未到期时不动它。
	n, err := s.ExpirePending(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	got, err := s.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != approval.StatusPending {
		t.Fatalf("未到期的单被扫掉了（本轮扫了 %d 张）：%s", n, got.Status)
	}

	// 越过它的到期时刻。
	if _, err := s.ExpirePending(context.Background(), req.ExpiresAt.Add(time.Second)); err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	got, err = s.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != approval.StatusExpired {
		t.Fatalf("到期了该 EXPIRED，got %s", got.Status)
	}
	if got.DecidedAt != nil {
		t.Fatalf("过期不该写 decided_at：%v", *got.DecidedAt)
	}

	// 过期的单一票也不能投。
	err = mustVoteErr(t, s, req.ID, voter("staff_bob", false), vote(approval.VerdictApprove), approval.DefaultPolicy())
	if !errors.Is(err, approval.ErrNotPending) && !errors.Is(err, approval.ErrExpired) {
		t.Fatalf("过期的单不该收票，got %v", err)
	}
}

func TestListFiltersByStatusAndEnvironment(t *testing.T) {
	pool := testPool(t)
	s := newStore(t, pool, nil)
	policy := approval.DefaultPolicy()
	pending := seed(t, s, pool, "L3", "staff_alice")
	approved := seed(t, s, pool, "L2", "staff_alice")
	if _, err := s.Vote(context.Background(), approved.ID, voter("staff_bob", false), vote(approval.VerdictApprove), policy); err != nil {
		t.Fatalf("Vote: %v", err)
	}

	ids := func(reqs []approval.Request) map[uuid.UUID]bool {
		out := map[uuid.UUID]bool{}
		for _, r := range reqs {
			out[r.ID] = true
		}
		return out
	}

	got, err := s.List(context.Background(), approval.StatusPending, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := ids(got)
	if !seen[pending.ID] || seen[approved.ID] {
		t.Fatalf("按 PENDING 过滤没生效：pending=%v approved=%v", seen[pending.ID], seen[approved.ID])
	}

	got, err = s.List(context.Background(), "", 100)
	if err != nil {
		t.Fatalf("List 全量: %v", err)
	}
	seen = ids(got)
	if !seen[pending.ID] || !seen[approved.ID] {
		t.Fatalf("空状态该返回全部：%+v", seen)
	}

	other := approval.NewPgStore(pool, "production", nil)
	got, err = other.List(context.Background(), "", 100)
	if err != nil {
		t.Fatalf("List 跨环境: %v", err)
	}
	if seen = ids(got); seen[pending.ID] || seen[approved.ID] {
		t.Fatal("另一个环境不该列出本环境的单")
	}
}
