package cards

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

var issueNow = time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)

// memStore 是内存台账，只服务本包的测试。
type memStore struct {
	ops        map[string]Operation
	cards      map[string]infini.Card
	txs        map[string][]infini.CardTransaction
	spentToday string
}

func newMemStore() *memStore {
	return &memStore{
		ops:        make(map[string]Operation),
		cards:      make(map[string]infini.Card),
		txs:        make(map[string][]infini.CardTransaction),
		spentToday: "0",
	}
}

func (m *memStore) BeginOperation(ctx context.Context, op Operation) (Operation, bool, error) {
	if existing, ok := m.ops[op.IdempotencyKey]; ok {
		return existing, true, nil
	}
	m.ops[op.IdempotencyKey] = op
	return op, false, nil
}

func (m *memStore) ResolveOperation(ctx context.Context, op Operation) error {
	m.ops[op.IdempotencyKey] = op
	return nil
}

func (m *memStore) SpentToday(ctx context.Context, kind string, day time.Time) (string, error) {
	return m.spentToday, nil
}

func (m *memStore) UpsertCard(ctx context.Context, card infini.Card, ownerRef string) error {
	m.cards[card.ID] = card
	return nil
}

func (m *memStore) UnresolvedOperations(ctx context.Context) ([]Operation, error) {
	var out []Operation
	for _, op := range m.ops {
		if op.State == StatePending || op.State == StateUnknown {
			out = append(out, op)
		}
	}
	return out, nil
}

func (m *memStore) TrackedCardIDs(ctx context.Context) ([]string, error) {
	var out []string
	for id := range m.cards {
		out = append(out, id)
	}
	return out, nil
}

func (m *memStore) UpsertTransactions(ctx context.Context, cardID string, txs []infini.CardTransaction) error {
	m.txs[cardID] = append(m.txs[cardID], txs...)
	return nil
}

func newService(client infini.CardClient, store *memStore) *Service {
	return &Service{
		client: client,
		store:  store,
		limits: Limits{PerOperation: "100", PerDay: "500"},
		now:    func() time.Time { return issueNow },
	}
}

func issueReq() IssueRequest {
	return IssueRequest{
		IdempotencyKey: "issue-1",
		ProductID:      1,
		TopUpAmount:    "10",
		TokenType:      "USDT",
		UserEmail:      "ops@example.com",
		HolderName:     "ZHANG WEI",
		OwnerRef:       "ops-team",
	}
}

func TestIssueCardHappyPathRecordsCardAndResolvesOperation(t *testing.T) {
	store := newMemStore()
	svc := newService(infini.NewFake(), store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	if res.State != StateSucceeded {
		t.Fatalf("State = %q, want succeeded", res.State)
	}
	if res.CardID == "" {
		t.Fatal("成功时应带回卡 id")
	}
	if _, ok := store.cards[res.CardID]; !ok {
		t.Fatal("成功后应把卡落进投影")
	}
	if store.ops["issue-1"].State != StateSucceeded {
		t.Fatalf("台账状态 = %q, want succeeded", store.ops["issue-1"].State)
	}
}

// 幂等：同一幂等键再来一次，绝不能再调一次上游。
func TestIssueCardIsIdempotentOnSameKey(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	first, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	if first.CardID != second.CardID {
		t.Fatalf("同一幂等键应返回同一张卡: %q vs %q", first.CardID, second.CardID)
	}

	page, _ := fake.ListCards(context.Background(), infini.ListCardsQuery{})
	if len(page.Cards) != 1 {
		t.Fatalf("上游只应被开卡一次, got %d 张卡", len(page.Cards))
	}
}

// 超限必须在**调上游之前**拦住：拦晚了钱就已经花出去了。
func TestIssueCardRejectsOverLimitBeforeCallingUpstream(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	req := issueReq()
	req.TopUpAmount = "101"

	_, err := svc.IssueCard(context.Background(), req)
	if !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("错误 = %v, want ErrPerOperationExceeded", err)
	}

	page, _ := fake.ListCards(context.Background(), infini.ListCardsQuery{})
	if len(page.Cards) != 0 {
		t.Fatal("超限时绝不能调上游")
	}
	if _, ok := store.ops["issue-1"]; ok {
		t.Fatal("超限被拒不该在台账里留 pending 记录")
	}
}

// 单日累计也算上：今天已花 495，再开 10 就超了。
func TestIssueCardCountsTodaysSpend(t *testing.T) {
	store := newMemStore()
	store.spentToday = "495"
	svc := newService(infini.NewFake(), store)

	if _, err := svc.IssueCard(context.Background(), issueReq()); !errors.Is(err, ErrDailyExceeded) {
		t.Fatalf("错误 = %v, want ErrDailyExceeded", err)
	}
}

// 发给上游的 alias 必须由幂等键派生——它是超时后对账的唯一抓手。
func TestIssueCardSendsDerivedAliasUpstream(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	if _, err := svc.IssueCard(context.Background(), issueReq()); err != nil {
		t.Fatal(err)
	}

	want := AliasFor("issue-1")
	page, _ := fake.ListCards(context.Background(), infini.ListCardsQuery{Alias: want})
	if len(page.Cards) != 1 {
		t.Fatalf("上游应收到派生出的 alias %q", want)
	}
	if store.ops["issue-1"].Alias != want {
		t.Fatalf("台账里的 alias = %q, want %q", store.ops["issue-1"].Alias, want)
	}
}

// 错误分类决定台账落成什么状态，而状态决定能不能重试。
// 这张表是整个功能里最不能错的一处：把「可能已经执行」错判成「确定没执行」，
// 就是允许了一次可能重复扣钱的重试。
func TestIssueCardMapsUpstreamErrorToOperationState(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want OperationState
	}{
		{"上游业务拒绝", connector.NewError(connector.KindRejected, "op", nil), StateFailed},
		{"认证失败", connector.NewError(connector.KindAuth, "op", nil), StateFailed},
		{"被限流", connector.NewError(connector.KindRateLimited, "op", nil), StateFailed},
		{"目标不在白名单", connector.NewError(connector.KindForbiddenTarget, "op", nil), StateFailed},
		{"上游不可达或超时", connector.NewError(connector.KindUnavailable, "op", nil), StateUnknown},
		{"响应无法解析", connector.NewError(connector.KindBadResponse, "op", nil), StateUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			fake := infini.NewFake()
			fake.FailNext(tc.err)
			svc := newService(fake, store)

			if _, err := svc.IssueCard(context.Background(), issueReq()); err == nil {
				t.Fatal("上游失败时必须返回错误")
			}

			got := store.ops["issue-1"]
			if got.State != tc.want {
				t.Fatalf("台账状态 = %q, want %q", got.State, tc.want)
			}
			if tc.want == StateUnknown && got.RetryAllowed() {
				t.Fatal("不确定态绝不允许重试")
			}
		})
	}
}

// 不确定态必须把 alias 留在台账里，否则对账无从下手。
func TestIssueCardKeepsAliasOnUnknownOutcome(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	fake.FailNext(connector.NewError(connector.KindUnavailable, "op", nil))
	svc := newService(fake, store)

	_, _ = svc.IssueCard(context.Background(), issueReq())

	op := store.ops["issue-1"]
	if op.Alias != AliasFor("issue-1") {
		t.Fatalf("不确定态下 alias 必须留存, got %q", op.Alias)
	}
	if op.StartedAt.IsZero() {
		t.Fatal("不确定态下 StartedAt 必须留存——宽限期从它算起")
	}
}
