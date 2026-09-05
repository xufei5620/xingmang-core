package sms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

var testNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// ---- 替身 ----

type memStore struct {
	ops         map[string]Operation
	resources   map[string]Resource
	orders      map[string]Order
	codes       map[string]Code
	status      map[string]ProviderStatus
	emails      map[string]Email
	routing     map[string]RoutingRule
	snapshots   []BalanceSnapshot
	snapshotErr error
	thresholds  map[string]BalanceThreshold
	costs       map[string]CostEvent
	costOrder   []string
	alerts      map[string]AlertEvent
	// pendingHash 模拟未决唯一索引。
	pendingHash map[string]string
	seq         int
	touched     []string
}

func newMemStore() *memStore {
	return &memStore{
		ops: map[string]Operation{}, resources: map[string]Resource{},
		orders: map[string]Order{}, codes: map[string]Code{},
		status: map[string]ProviderStatus{}, pendingHash: map[string]string{},
		emails:     map[string]Email{},
		routing:    map[string]RoutingRule{},
		thresholds: map[string]BalanceThreshold{},
		costs:      map[string]CostEvent{},
		alerts:     map[string]AlertEvent{},
	}
}

func (m *memStore) PrepareOperation(ctx context.Context, op Operation) error {
	if _, dup := m.pendingHash[op.Provider+"/"+op.RequestHash]; dup {
		return ErrPendingDuplicate
	}
	m.pendingHash[op.Provider+"/"+op.RequestHash] = op.ID
	op.State = StatePrepared
	m.ops[op.ID] = op
	return nil
}

func (m *memStore) MarkSubmitted(ctx context.Context, id string, at time.Time) error {
	op, ok := m.ops[id]
	if !ok || op.State != StatePrepared {
		return errors.New("不在 prepared 状态")
	}
	op.State = StateSubmitted
	m.ops[id] = op
	return nil
}

func (m *memStore) ResolveOperation(ctx context.Context, op Operation) error {
	if !op.Pending() {
		delete(m.pendingHash, op.Provider+"/"+op.RequestHash)
	}
	m.ops[op.ID] = op
	return nil
}

func (m *memStore) GetOperation(ctx context.Context, id string) (Operation, error) {
	op, ok := m.ops[id]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	return op, nil
}

func (m *memStore) ListOperations(ctx context.Context, provider string, limit int) ([]Operation, error) {
	var out []Operation
	for _, op := range m.ops {
		if provider == "" || op.Provider == provider {
			out = append(out, op)
		}
	}
	return out, nil
}

func (m *memStore) RecoverStaleOperations(ctx context.Context, at time.Time) (int, error) {
	n := 0
	for id, op := range m.ops {
		switch op.State {
		case StatePrepared:
			op.State = StateFailed
			m.ops[id] = op
			n++
		case StateSubmitted:
			op.State = StateUnknown
			op.NeedsHumanReview = true
			m.ops[id] = op
			n++
		}
	}
	return n, nil
}

func (m *memStore) UpsertResource(ctx context.Context, r Resource) (string, error) {
	for id, existing := range m.resources {
		if existing.Provider == r.Provider && existing.ExternalID == r.ExternalID {
			r.ID = id
			// 与 PgStore 一致：传空 state 保留原值。
			if r.State == "" {
				r.State = existing.State
			}
			if r.OperationID == "" {
				r.OperationID = existing.OperationID
			}
			m.resources[id] = r
			return id, nil
		}
	}
	m.seq++
	id := "res-" + itoa(m.seq)
	r.ID = id
	m.resources[id] = r
	return id, nil
}

func (m *memStore) GetResource(ctx context.Context, id string) (Resource, error) {
	r, ok := m.resources[id]
	if !ok {
		return Resource{}, errors.New("号码不存在")
	}
	return r, nil
}

func (m *memStore) ListResources(ctx context.Context, provider string, limit int) ([]Resource, error) {
	var out []Resource
	for _, r := range m.resources {
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) TouchResourceCodeAt(ctx context.Context, id string, at time.Time) error {
	m.touched = append(m.touched, id)
	return nil
}

func (m *memStore) SetResourceState(ctx context.Context, id string, state NumberState, at time.Time) error {
	r, ok := m.resources[id]
	if !ok {
		return errors.New("号码不存在")
	}
	r.State = state
	m.resources[id] = r
	return nil
}

func (m *memStore) UpsertOrder(ctx context.Context, o Order) (string, error) {
	m.seq++
	id := "order-" + itoa(m.seq)
	o.ID = id
	m.orders[id] = o
	return id, nil
}

func (m *memStore) RecordCode(ctx context.Context, c Code) (Code, bool, error) {
	key := c.ResourceID + "/" + c.Code
	if existing, ok := m.codes[key]; ok {
		return existing, false, nil
	}
	m.seq++
	c.ID = "code-" + itoa(m.seq)
	m.codes[key] = c
	return c, true, nil
}

func (m *memStore) ListCodes(ctx context.Context, resourceID string, limit int) ([]Code, error) {
	var out []Code
	for _, c := range m.codes {
		out = append(out, c)
	}
	return out, nil
}

func (m *memStore) ProviderStatus(ctx context.Context, provider string) (ProviderStatus, error) {
	return m.status[provider], nil
}

func (m *memStore) ListProviderStatus(ctx context.Context) ([]ProviderStatus, error) {
	var out []ProviderStatus
	for _, s := range m.status {
		out = append(out, s)
	}
	return out, nil
}

func (m *memStore) SaveProviderStatus(ctx context.Context, s ProviderStatus) error {
	// 与 PgStore 一致：**不改 enabled**。替身比真实存储宽松一档，
	// 等于让一类真实存在的 bug 在单元测试里永远看不见。
	s.Enabled = m.status[s.Provider].Enabled
	m.status[s.Provider] = s
	return nil
}

func (m *memStore) SetProviderEnabled(ctx context.Context, provider string, enabled bool, at time.Time) error {
	st := m.status[provider]
	st.Provider = provider
	st.Enabled = enabled
	m.status[provider] = st
	return nil
}

func (m *memStore) UpsertEmail(ctx context.Context, e Email) (string, error) {
	for id, existing := range m.emails {
		if existing.Provider == e.Provider && existing.ExternalID == e.ExternalID {
			e.ID = id
			if e.Value == "" {
				e.Value = existing.Value
			}
			m.emails[id] = e
			return id, nil
		}
	}
	m.seq++
	id := "email-" + itoa(m.seq)
	e.ID = id
	m.emails[id] = e
	return id, nil
}

func (m *memStore) GetEmail(ctx context.Context, id string) (Email, error) {
	e, ok := m.emails[id]
	if !ok {
		return Email{}, errors.New("邮箱不存在")
	}
	return e, nil
}

func (m *memStore) ListEmails(ctx context.Context, limit int) ([]Email, error) {
	var out []Email
	for _, e := range m.emails {
		out = append(out, e)
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

// fakeAdapter 是可编程的适配器替身。
type fakeAdapter struct {
	purchaseOutcome PurchaseOutcome
	purchaseErr     error
	// catalog 是 ListCatalog 的回答；lastPurchase 记最近一次 Purchase 的入参。
	catalog         []CatalogItem
	lastPurchase    PurchaseInput
	importResources []Resource
	importErr       error
	code            Code
	codeErr         error
	testIP          string
	testErr         error
	purchaseCalls   int
	importCalls     int
	testCalls       int
}

func (f *fakeAdapter) TestConnection(ctx context.Context) (string, error) {
	f.testCalls++
	return f.testIP, f.testErr
}

func (f *fakeAdapter) ListCatalog(ctx context.Context, filter CatalogFilter) ([]CatalogItem, error) {
	return f.catalog, nil
}

func (f *fakeAdapter) Purchase(ctx context.Context, in PurchaseInput) (PurchaseOutcome, error) {
	f.purchaseCalls++
	f.lastPurchase = in
	return f.purchaseOutcome, f.purchaseErr
}

func (f *fakeAdapter) ImportByUpstreamID(ctx context.Context, id string) ([]Resource, error) {
	f.importCalls++
	return f.importResources, f.importErr
}

func (f *fakeAdapter) FetchCode(ctx context.Context, r Resource) (Code, error) {
	return f.code, f.codeErr
}

func (f *fakeAdapter) ExecuteAction(ctx context.Context, kind string, r Resource, opt ActionOptions) (Resource, error) {
	return Resource{}, ErrActionNotSupported
}

func newService(t *testing.T, adapter Adapter, store *memStore) *Service {
	t.Helper()
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: true, VerifiedAt: testNow}
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService(
		[]Provider{{ID: ProviderSMS62, Adapter: adapter}, {ID: ProviderHero, Adapter: adapter}},
		store, nil, func() time.Time { return testNow },
	)
}

// ---- 测试 ----

// 没做过连接测试的供应商**不许花钱**，而且要在打上游之前就拦住。
//
// 拿一份没验证过的凭据去买号，失败时分不清是密钥没配还是上游故障——
// 而那两件事的处置完全不同。
func TestPurchaseRequiresVerifiedProvider(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{}
	svc := newService(t, adapter, store)
	// 把验证事实抹掉。
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: true}

	_, err := svc.Purchase(context.Background(), "op-1", ProviderSMS62, PurchaseInput{Quantity: 1})
	if !errors.Is(err, ErrProviderNotVerified) {
		t.Fatalf("未验证的供应商必须拒绝, got %v", err)
	}
	if adapter.purchaseCalls != 0 {
		t.Fatal("必须在打上游之前就拒绝")
	}
}

// 62 的形态：买完只回订单 ID，要再读一次才拿到号码。
func TestPurchaseFetchesNumbersWhenProviderReturnsOnlyOrder(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{
		purchaseOutcome: PurchaseOutcome{OrderRef: "order-9"},
		importResources: []Resource{{ExternalID: "fp-1", Phone: "+15550001111", ProviderToken: "tok"}},
	}
	svc := newService(t, adapter, store)

	op, err := svc.Purchase(context.Background(), "op-1", ProviderSMS62, PurchaseInput{Quantity: 1, GoodsID: "1-2-3"})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.importCalls != 1 {
		t.Fatalf("应补读一次订单号码, importCalls=%d", adapter.importCalls)
	}
	if op.State != StateSucceeded {
		t.Fatalf("应成功, got %s", op.State)
	}
	if len(store.resources) != 1 {
		t.Fatalf("号码应落库, got %d", len(store.resources))
	}
}

// **买成功了但补读失败 → unknown，不是 failed。**
//
// 这是 62 最要紧的一条：钱已经花了。判成 failed 会让页面告诉人可以重来，
// 而重来就是再买一次。
func TestPurchaseFallsToUnknownWhenTokenFetchFails(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{
		purchaseOutcome: PurchaseOutcome{OrderRef: "order-9"},
		// 哪怕补读回的是 4xx（明确拒绝），也不能判 failed——
		// 那个 4xx 说的是「这次读失败了」，不是「那次买没成功」。
		importErr: connector.NewError(connector.KindRejected, "订单不存在", nil),
	}
	svc := newService(t, adapter, store)

	op, err := svc.Purchase(context.Background(), "op-1", ProviderSMS62, PurchaseInput{Quantity: 1, GoodsID: "1-2-3"})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateUnknown {
		t.Fatalf("补读失败必须落 unknown, got %s", op.State)
	}
	if !op.NeedsHumanReview {
		t.Fatal("应亮红条要人工核对")
	}
	// 上游订单 ID 必须留住：那是人去供应商侧对账的唯一抓手。
	if op.ProviderRef != "order-9" {
		t.Fatalf("订单 ID 应留在台账里, got %q", op.ProviderRef)
	}
	if op.RetryAllowed() {
		t.Fatal("unknown 绝不允许重试")
	}
}

// 上游明确拒绝 → failed（钱没花出去，可以改参数重来）。
func TestPurchaseRejectionIsFailedNotUnknown(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseErr: &herosms.RejectedError{Status: 422, Message: "余额不足"}}
	svc := newService(t, adapter, store)

	op, err := svc.Purchase(context.Background(), "op-1", ProviderHero, PurchaseInput{Quantity: 1, Service: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateFailed {
		t.Fatalf("明确拒绝应是 failed, got %s", op.State)
	}
	if !op.RetryAllowed() {
		t.Fatal("确定失败应允许重试")
	}
}

// 传输失败 → unknown（可能已扣费）。
func TestPurchaseTransportFailureIsUnknown(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseErr: connector.NewError(connector.KindUnavailable, "超时", nil)}
	svc := newService(t, adapter, store)

	op, err := svc.Purchase(context.Background(), "op-1", ProviderHero, PurchaseInput{Quantity: 1, Service: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateUnknown {
		t.Fatalf("传输失败应是 unknown, got %s", op.State)
	}
}

// 同一份请求换个 operation UUID 也发不出去。
func TestPurchaseRejectsDuplicatePendingRequest(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseErr: connector.NewError(connector.KindUnavailable, "超时", nil)}
	svc := newService(t, adapter, store)
	in := PurchaseInput{Quantity: 1, Service: "op", Country: 1}

	// 第一笔落 unknown，仍占着未决索引。
	if _, err := svc.Purchase(context.Background(), "op-1", ProviderHero, in); err != nil {
		t.Fatal(err)
	}
	// 换 UUID 重发同一份请求——必须被挡住。
	_, err := svc.Purchase(context.Background(), "op-2", ProviderHero, in)
	if !errors.Is(err, ErrPendingDuplicate) {
		t.Fatalf("同请求换 UUID 必须被未决索引挡住, got %v", err)
	}
	if adapter.purchaseCalls != 1 {
		t.Fatalf("上游只该被打一次, got %d", adapter.purchaseCalls)
	}
}

// 62 不支持生命周期动作，在领域层就挡掉。
func TestActionRejectedForSMS62(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	id, _ := store.UpsertResource(context.Background(), Resource{Provider: ProviderSMS62, ExternalID: "fp-1"})

	_, err := svc.ExecuteAction(context.Background(), "op-1", KindCancel, id, ActionOptions{})
	if !errors.Is(err, ErrActionNotSupported) {
		t.Fatalf("62 不支持取消, got %v", err)
	}
}

// 取码取到就落库；**重复取到同一条不重复推送**。
func TestFetchCodeDeduplicates(t *testing.T) {
	store := newMemStore()
	notifier := &countingNotifier{}
	adapter := &fakeAdapter{code: Code{Code: "123456", Sender: "OPENAI"}}
	svc := NewService([]Provider{{ID: ProviderHero, Adapter: adapter}}, store, notifier,
		func() time.Time { return testNow })
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	id, _ := store.UpsertResource(context.Background(), Resource{Provider: ProviderHero, ExternalID: "a1"})

	for i := 0; i < 3; i++ {
		if _, err := svc.FetchCode(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	// 轮询会反复读到同一条短信；每次都推会让人在群里看到一串一样的码，
	// 然后开始忽略它们——而下一次真的新码来时也就被忽略了。
	if notifier.calls != 1 {
		t.Fatalf("同一个码只该推一次, got %d", notifier.calls)
	}
}

// 还没有码是**正常状态**，不是故障。
func TestFetchCodeReportsNotAvailable(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{codeErr: ErrCodeNotAvailable}
	svc := newService(t, adapter, store)
	id, _ := store.UpsertResource(context.Background(), Resource{Provider: ProviderHero, ExternalID: "a1"})

	_, err := svc.FetchCode(context.Background(), id)
	if !errors.Is(err, ErrCodeNotAvailable) {
		t.Fatalf("应回「还没有码」, got %v", err)
	}
}

// 人工核对**必须写依据**，判定购买成功还必须关联号码。
//
// 三个月后回看这条记录时，「谁凭什么判定它成功了」只有那句话回答得了。
func TestResolveRequiresEvidence(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	store.ops["op-1"] = Operation{
		ID: "op-1", Provider: ProviderHero, Kind: KindPurchase, State: StateUnknown,
	}

	if _, err := svc.Resolve(context.Background(), "op-1", true, "res-1", ""); err == nil {
		t.Fatal("空备注必须拒绝")
	}
	if _, err := svc.Resolve(context.Background(), "op-1", true, "", "确认买到了"); err == nil {
		t.Fatal("判定购买成功必须关联号码")
	}
	op, err := svc.Resolve(context.Background(), "op-1", true, "res-1", "在供应商后台看到这笔订单")
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateReconciledSucceeded || op.NeedsHumanReview {
		t.Fatalf("核对后状态不对: %+v", op)
	}
}

// 只有 unknown 能被人工核对。
func TestResolveOnlyAcceptsUnknown(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	store.ops["op-1"] = Operation{ID: "op-1", Provider: ProviderHero, State: StateSucceeded}

	if _, err := svc.Resolve(context.Background(), "op-1", false, "", "随便写点"); err == nil {
		t.Fatal("非 unknown 的操作不该能被核对")
	}
}

// 连接测试失败**不清掉上一次的验证事实**。
//
// 一次网络抖动不该让一个配好的供应商变回「从未验证」，那会连带把购买挡死。
func TestFailedConnectionTestKeepsPreviousVerification(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{testErr: errors.New("网络抖动")}
	svc := newService(t, adapter, store)
	before := store.status[ProviderHero].VerifiedAt

	status, err := svc.TestConnection(context.Background(), ProviderHero)
	if err == nil {
		t.Fatal("测试失败应返回错误")
	}
	if !status.VerifiedAt.Equal(before) {
		t.Fatal("失败不该清掉上一次的验证事实")
	}
	if status.LastError == "" {
		t.Fatal("应记下这次的错误")
	}
}

// 进程崩溃遗留的两种未决，处置不同。
func TestRecoverSplitsPreparedFromSubmitted(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	store.ops["a"] = Operation{ID: "a", State: StatePrepared}
	store.ops["b"] = Operation{ID: "b", State: StateSubmitted}

	if _, err := svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	// prepared：还没发出去，钱没花，判失败是安全的。
	if store.ops["a"].State != StateFailed {
		t.Fatalf("prepared 应恢复成 failed, got %s", store.ops["a"].State)
	}
	// submitted：发出去了但不知道结果，只能人工。
	if store.ops["b"].State != StateUnknown || !store.ops["b"].NeedsHumanReview {
		t.Fatalf("submitted 应恢复成 unknown 并要人工, got %+v", store.ops["b"])
	}
}

type countingNotifier struct{ calls int }

func (n *countingNotifier) NotifyCode(ctx context.Context, provider, mask, code string) error {
	n.calls++
	return nil
}

// ---- 路由规则（XM-SMS2 #5）----

func (m *memStore) UpsertRoutingRule(ctx context.Context, r RoutingRule) (string, error) {
	for id, existing := range m.routing {
		if existing.Service == r.Service && existing.Country == r.Country {
			r.ID = id
			m.routing[id] = r
			return id, nil
		}
	}
	m.seq++
	r.ID = "rule-" + itoa(m.seq)
	m.routing[r.ID] = r
	return r.ID, nil
}

func (m *memStore) RemoveRoutingRule(ctx context.Context, id string) error {
	if _, ok := m.routing[id]; !ok {
		return ErrRoutingRuleNotFound
	}
	delete(m.routing, id)
	return nil
}

func (m *memStore) ListRoutingRules(ctx context.Context) ([]RoutingRule, error) {
	out := make([]RoutingRule, 0, len(m.routing))
	for _, r := range m.routing {
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) ListResourcesByOperation(ctx context.Context, operationID string) ([]Resource, error) {
	var out []Resource
	for _, r := range m.resources {
		if r.OperationID == operationID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) SaveBalanceSnapshot(ctx context.Context, snap BalanceSnapshot) (string, error) {
	if m.snapshotErr != nil {
		return "", m.snapshotErr
	}
	m.seq++
	snap.ID = "snap-" + itoa(m.seq)
	m.snapshots = append(m.snapshots, snap)
	return snap.ID, nil
}

// LatestBalanceSnapshots 每家最新一条（与 PgStore 的 DISTINCT ON 同义）。
func (m *memStore) LatestBalanceSnapshots(ctx context.Context) ([]BalanceSnapshot, error) {
	latest := map[string]BalanceSnapshot{}
	for _, s := range m.snapshots {
		prev, ok := latest[s.Provider]
		if !ok || !s.TakenAt.Before(prev.TakenAt) {
			latest[s.Provider] = s
		}
	}
	out := make([]BalanceSnapshot, 0, len(latest))
	for _, id := range AllProviders {
		if s, ok := latest[id]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) snapshotCount() int { return len(m.snapshots) }

func (m *memStore) SaveBalanceThreshold(ctx context.Context, t BalanceThreshold) error {
	m.thresholds[t.Provider] = t
	return nil
}

func (m *memStore) RemoveBalanceThreshold(ctx context.Context, provider string) error {
	delete(m.thresholds, provider)
	return nil
}

func (m *memStore) ListBalanceThresholds(ctx context.Context) ([]BalanceThreshold, error) {
	out := make([]BalanceThreshold, 0, len(m.thresholds))
	for _, id := range AllProviders {
		if t, ok := m.thresholds[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *memStore) UpsertAlertEvent(ctx context.Context, ev AlertEvent) (string, error) {
	if prev, ok := m.alerts[ev.Fingerprint]; ok {
		ev.ID = prev.ID
		if prev.ResolvedAt.IsZero() {
			// 与 PgStore 一致：还开着的保留首次看见的时间。
			ev.FirstSeenAt = prev.FirstSeenAt
		}
		ev.ResolvedAt = time.Time{}
		m.alerts[ev.Fingerprint] = ev
		return ev.ID, nil
	}
	m.seq++
	ev.ID = "alert-" + itoa(m.seq)
	m.alerts[ev.Fingerprint] = ev
	return ev.ID, nil
}

func (m *memStore) ResolveAlertEventsNotIn(ctx context.Context, fingerprints []string, at time.Time) (int, error) {
	firing := map[string]bool{}
	for _, f := range fingerprints {
		firing[f] = true
	}
	n := 0
	for key, ev := range m.alerts {
		if firing[key] || !ev.ResolvedAt.IsZero() {
			continue
		}
		ev.ResolvedAt = at
		m.alerts[key] = ev
		n++
	}
	return n, nil
}

func (m *memStore) ListOpenAlertEvents(ctx context.Context) ([]AlertEvent, error) {
	var out []AlertEvent
	for _, ev := range m.alerts {
		if ev.Open() {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *memStore) ListStaleUnknownOperations(ctx context.Context, before time.Time) ([]Operation, error) {
	var out []Operation
	for _, op := range m.ops {
		if op.State == StateUnknown && op.NeedsHumanReview && op.UpdatedAt.Before(before) {
			out = append(out, op)
		}
	}
	return out, nil
}

func (m *memStore) ListExpiringRentals(ctx context.Context, from, until time.Time) ([]Resource, error) {
	var out []Resource
	for _, r := range m.resources {
		if r.Subtype != SubtypeRent || r.State != StateWaitingCode || r.ExpiresAt.IsZero() {
			continue
		}
		if r.ExpiresAt.After(from) && !r.ExpiresAt.After(until) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) AppendCostEvents(ctx context.Context, events []CostEvent) (int, error) {
	n := 0
	for _, ev := range events {
		key := ev.OperationID + "|" + ev.Subject
		if _, ok := m.costs[key]; ok {
			// 与 PgStore 的 ON CONFLICT DO NOTHING 一致：记过的不改写。
			continue
		}
		m.seq++
		ev.ID = "cost-" + itoa(m.seq)
		m.costs[key] = ev
		m.costOrder = append(m.costOrder, key)
		n++
	}
	return n, nil
}

func (m *memStore) ListCostEvents(ctx context.Context, provider string, limit int) ([]CostEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []CostEvent
	for _, key := range m.costOrder {
		ev := m.costs[key]
		if provider != "" && ev.Provider != provider {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
