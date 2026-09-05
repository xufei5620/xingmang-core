package cards

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

var issueNow = time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)

// memStore 是内存台账，只服务本包的测试。
type memStore struct {
	ops   map[string]Operation
	cards map[string]infini.Card
	// cardAccount 记住每张卡属于哪个账号，供 TrackedCards 还原。
	cardAccount map[string]string
	txs         map[string][]infini.CardTransaction
	secrets     map[string]infini.RevealedCard
	ownerRef    map[string]string
	userEmail   map[string]string
	usage       map[string]CardUsage
	// attributions 记住最后一次非空归属，供断言开卡费落库。
	attributions map[string]CardAttribution
	// 提现：登记的地址与台账。
	addresses   map[string]WithdrawAddress
	withdrawals map[string]WithdrawRecord
	// withdrawLimits 按账号存提现额度；没有条目 = 该账号不能提现。
	withdrawLimits map[string]Limits
	spentToday     string
	// spentByAccount 非空时按账号取值，否则回落到 spentToday。
	spentByAccount map[string]string
}

func newMemStore() *memStore {
	return &memStore{
		ops:            make(map[string]Operation),
		cards:          make(map[string]infini.Card),
		cardAccount:    make(map[string]string),
		txs:            make(map[string][]infini.CardTransaction),
		secrets:        make(map[string]infini.RevealedCard),
		ownerRef:       make(map[string]string),
		userEmail:      make(map[string]string),
		usage:          make(map[string]CardUsage),
		attributions:   make(map[string]CardAttribution),
		addresses:      make(map[string]WithdrawAddress),
		withdrawals:    make(map[string]WithdrawRecord),
		withdrawLimits: make(map[string]Limits),
		spentToday:     "0",
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

func (m *memStore) SpentToday(ctx context.Context, account, kind string, day time.Time) (string, error) {
	if m.spentByAccount != nil {
		if v, ok := m.spentByAccount[account]; ok {
			return v, nil
		}
		return "0", nil
	}
	return m.spentToday, nil
}

func (m *memStore) UpsertCard(ctx context.Context, account string, card infini.Card, attribution CardAttribution) error {
	m.cards[card.ID] = card
	m.cardAccount[card.ID] = account
	// 与 PgStore 同口径：非空才覆盖，否则同步作业每轮都会把开卡时
	// 记下的归属冲掉。
	if attribution.OwnerRef != "" {
		m.ownerRef[card.ID] = attribution.OwnerRef
	}
	if attribution.UserEmail != "" {
		m.userEmail[card.ID] = attribution.UserEmail
	}
	if attribution.IssueFee != "" || attribution.IssuePayAmount != "" {
		m.attributions[card.ID] = attribution
	}
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

func (m *memStore) CardStatusOf(ctx context.Context, account, cardID string) (string, error) {
	c, ok := m.cards[cardID]
	if !ok {
		return "", fmt.Errorf("memStore: 卡 %s 不存在", cardID)
	}
	return c.Status, nil
}

func (m *memStore) TrackedCards(ctx context.Context) ([]CardRef, error) {
	var out []CardRef
	for id := range m.cards {
		out = append(out, CardRef{Account: m.cardAccount[id], CardID: id})
	}
	return out, nil
}

func (m *memStore) UpsertTransactions(ctx context.Context, account, cardID string, txs []infini.CardTransaction) error {
	m.txs[cardID] = append(m.txs[cardID], txs...)
	return nil
}

func (m *memStore) SetCardUsage(ctx context.Context, usage CardUsage) error {
	m.usage[usage.CardID] = usage
	return nil
}

func (m *memStore) CardsMissingSecrets(ctx context.Context) ([]CardRef, error) {
	var out []CardRef
	for id, card := range m.cards {
		// 与 PgStore 同一条判据：只有 active 的卡才拉得到明文
		if card.Status != "active" {
			continue
		}
		if _, ok := m.secrets[id]; ok {
			continue
		}
		out = append(out, CardRef{Account: m.cardAccount[id], CardID: id})
	}
	return out, nil
}

func (m *memStore) StoreCardSecrets(ctx context.Context, account, cardID string, r infini.RevealedCard) error {
	m.secrets[cardID] = r
	return nil
}

// testAccount 是既有单账号用例统一使用的账号 id。
const testAccount = "main"

func newService(client infini.CardClient, store *memStore) *Service {
	return NewService([]Account{{
		ID:     testAccount,
		Client: client,
		Limits: Limits{PerOperation: "100", PerDay: "500"},
	}}, store, func() time.Time { return issueNow })
}

func issueReq() IssueRequest {
	return IssueRequest{
		Account:        testAccount,
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

	// alias 的可读前缀来自请求里的用途标签（产品负责人 2026-09-05 拍板：
	// 上游把 card_alias 当卡片名称显示，纯哈希在那边不可读）。
	want := AliasFor("issue-1", issueReq().OwnerRef)
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
	if op.Alias != AliasFor("issue-1", issueReq().OwnerRef) {
		t.Fatalf("不确定态下 alias 必须留存, got %q", op.Alias)
	}
	if op.StartedAt.IsZero() {
		t.Fatal("不确定态下 StartedAt 必须留存——宽限期从它算起")
	}
}

// 卡开出来就是 active 时，当场把卡面明文拉下来。
//
// 上游的开卡是异步的，但实测（2026-09-05）返回时状态已经是 active。
// 此前明文要等同步作业下一轮才拉，最长 300 秒——运营开完卡看到的是一行
// 没有卡号的记录，只能反复刷新。reveal 需要 active，所以只对 active 动作。
func TestIssueCardFetchesSecretsWhenCardIsAlreadyActive(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply() // 2026-09-05 生产实测：apply 返回时已是 active
	store := newMemStore()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	got, ok := store.secrets[res.CardID]
	if !ok {
		t.Fatal("开卡后应当场拉到卡面明文，而不是等同步作业下一轮")
	}
	if got.Number == "" || got.CVV == "" {
		t.Fatalf("明文不完整: %+v", got)
	}
}

// 卡还没 active 时不去拉明文：reveal 对 init/pending 的卡本来就取不到，
// 白打一次受 IP 白名单限制的敏感接口。
func TestIssueCardSkipsRevealWhileCardIsNotActive(t *testing.T) {
	fake := infini.NewFake() // 默认文档口径：卡从 init 起步
	store := newMemStore()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.secrets[res.CardID]; ok {
		t.Fatal("卡还没 active 就不该拉明文")
	}
}

// 拉明文失败不能让开卡失败。
//
// 卡已经开出来了、钱已经花了，此时因为「明文没拉到」而返回错误，会诱使
// 调用方重试——而重试开卡是这套设计里最危险的动作。明文有周期同步兜底。
func TestIssueCardSucceedsEvenWhenRevealFails(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()
	svc := newService(fake, store)
	fake.FailReveal(errors.New("上游拒绝"))

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatalf("拉明文失败不该让开卡失败: %v", err)
	}
	if res.State != StateSucceeded {
		t.Fatalf("State = %q, want succeeded", res.State)
	}
	if _, ok := store.secrets[res.CardID]; ok {
		t.Fatal("这次本就该没存下明文")
	}
}

// 开卡费与实付金额要落进投影。
//
// apply 的响应里带 total_fee / total_pay_amount，此前只用来判断成功、没落库。
// 实测开卡费是 1 USD 固定（不是文档示例暗示的按比例），$1 的卡成本 100%——
// 这个数字不落库，成本核算就永远少一块。
func TestIssueCardRecordsFeeAndPaidAmount(t *testing.T) {
	fake := infini.NewFake()
	store := newMemStore()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	// 断言的是 apply 响应里的那两个值原样落库，不是「非空」——
	// 非空断言会被一个把两列都填成同一个数的实现骗过去。
	got := store.attributions[res.CardID]
	if got.IssueFee != "0" {
		t.Fatalf("开卡费应取自 apply 响应的 total_fee, got %+v", got)
	}
	if got.IssuePayAmount != issueReq().TopUpAmount {
		t.Fatalf("实付金额应取自 total_pay_amount, got %+v", got)
	}
}

func (m *memStore) AllowedAddress(ctx context.Context, id string) (WithdrawAddress, error) {
	a, ok := m.addresses[id]
	if !ok {
		return WithdrawAddress{}, fmt.Errorf("%w: %q", ErrAddressNotAllowed, id)
	}
	return a, nil
}

func (m *memStore) BeginWithdraw(ctx context.Context, w WithdrawRecord) (WithdrawRecord, bool, error) {
	if existing, ok := m.withdrawals[w.RequestID]; ok {
		return existing, true, nil
	}
	m.withdrawals[w.RequestID] = w
	return w, false, nil
}

// UpdateWithdraw 只覆盖非空字段，与 PgStore 的 COALESCE 行为一致。
//
// 整条替换会让替身比真实存储**更宽松**：终态回填只带回状态与链上信息，
// 整条替换会把金额、链与地址快照清空，而真实存储不会。替身宽松一档，
// 等于让一类真实存在的 bug 在单元测试里永远看不见。
func (m *memStore) UpdateWithdraw(ctx context.Context, w WithdrawRecord) error {
	cur := m.withdrawals[w.RequestID]
	if w.Status != "" {
		cur.Status = w.Status
	}
	if w.TxHash != "" {
		cur.TxHash = w.TxHash
	}
	if w.ActualAmount != "" {
		cur.ActualAmount = w.ActualAmount
	}
	if w.GasFee != "" {
		cur.GasFee = w.GasFee
		cur.GasFeeCurrency = w.GasFeeCurrency
	}
	if w.FXFee != "" {
		cur.FXFee = w.FXFee
		cur.FXFeeCurrency = w.FXFeeCurrency
	}
	if !w.UpdatedAt.IsZero() {
		cur.UpdatedAt = w.UpdatedAt
	}
	m.withdrawals[w.RequestID] = cur
	return nil
}

// WithdrawLimitsFor 返回该账号的提现额度。没有条目返回零值 Limits——
// 两个空串会被 Check 判成 ErrLimitsUnconfigured，也就是 fail closed。
func (m *memStore) WithdrawLimitsFor(ctx context.Context, account string) (Limits, error) {
	return m.withdrawLimits[account], nil
}

func (m *memStore) OpenWithdrawals(ctx context.Context) ([]WithdrawRecord, error) {
	var out []WithdrawRecord
	for _, w := range m.withdrawals {
		if w.Status == "pending" || w.Status == "processing" {
			out = append(out, w)
		}
	}
	// 按 requestID 排序：map 迭代顺序不定，会让依赖顺序的断言随机失败。
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out, nil
}

func (m *memStore) WithdrawnToday(ctx context.Context, account string, day time.Time) (string, error) {
	return "0", nil
}
