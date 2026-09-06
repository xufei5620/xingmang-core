package sms

import (
	"context"
	"errors"
	"testing"
	"time"
)

// extrasFake 是带扩展能力的可编程替身：主链路的 fakeAdapter 不实现
// HeroExtras，这里嵌入它再补上要测的几个方法。
type extrasFake struct {
	fakeAdapter
	rentRes      Resource
	rentErr      error
	emailRes     Email
	emailErr     error
	batchItems   []HeroEmailBatchItem
	listedItems  []Email
	cancelErr    error
	rentCalls    int
	balanceText  string
	balanceErr   error
	balanceCalls int
}

func (f *extrasFake) ListOTPs(ctx context.Context, r Resource) ([]Code, error) { return nil, nil }
func (f *extrasFake) ExtendOptions(ctx context.Context, r Resource, kind string) ([]HeroExtendOption, error) {
	return nil, nil
}
func (f *extrasFake) ProlongHistory(ctx context.Context, r Resource) ([]HeroProlongRecord, error) {
	return nil, nil
}
func (f *extrasFake) History(ctx context.Context, q HeroHistoryQuery) (HeroHistoryPage, error) {
	return HeroHistoryPage{}, nil
}
func (f *extrasFake) Stats(ctx context.Context, date string) ([]HeroStatsEntry, error) {
	return nil, nil
}
func (f *extrasFake) CustomDurations(ctx context.Context) (map[string]map[string]int64, error) {
	return nil, nil
}
func (f *extrasFake) Balance(ctx context.Context) (string, error) {
	f.balanceCalls++
	if f.balanceErr != nil {
		return "", f.balanceErr
	}
	if f.balanceText == "" {
		return "1.00", nil
	}
	return f.balanceText, nil
}
func (f *extrasFake) Countries(ctx context.Context) ([]HeroCountry, error) { return nil, nil }
func (f *extrasFake) Services(ctx context.Context, country int64, lang string) ([]HeroServiceEntry, error) {
	return nil, nil
}
func (f *extrasFake) Operators(ctx context.Context, country int64) (map[string][]string, error) {
	return nil, nil
}
func (f *extrasFake) Prices(ctx context.Context, service string, country int64) (map[string]map[string]HeroPriceCell, error) {
	return nil, nil
}
func (f *extrasFake) TopCountries(ctx context.Context, service string, freePrice, byRank bool) ([]HeroTopCountry, error) {
	return nil, nil
}
func (f *extrasFake) RentOffers(ctx context.Context, country, hours int64) (HeroRentOffers, error) {
	return HeroRentOffers{}, nil
}
func (f *extrasFake) RentCount(ctx context.Context, service string, country int64, operator string) (map[string]map[string]HeroPriceCell, error) {
	return nil, nil
}
func (f *extrasFake) EmailDomains(ctx context.Context, site string) ([]HeroEmailDomain, error) {
	return nil, nil
}
func (f *extrasFake) ListEmailsUpstream(ctx context.Context, q HeroEmailListQuery) ([]Email, error) {
	return f.listedItems, nil
}
func (f *extrasFake) GetEmailUpstream(ctx context.Context, externalID string) (Email, error) {
	return f.emailRes, f.emailErr
}
func (f *extrasFake) SetFavorite(ctx context.Context, service string, country int64, operator string) (HeroFavorite, error) {
	return HeroFavorite{IsFavorite: true}, nil
}
func (f *extrasFake) RemoveFavorite(ctx context.Context, service string, country int64) error {
	return nil
}
func (f *extrasFake) CancelEmail(ctx context.Context, externalID string) error { return f.cancelErr }
func (f *extrasFake) Rent(ctx context.Context, in HeroRentInput) (Resource, error) {
	f.rentCalls++
	return f.rentRes, f.rentErr
}
func (f *extrasFake) PurchaseEmail(ctx context.Context, site, domain string) (Email, error) {
	return f.emailRes, f.emailErr
}
func (f *extrasFake) PurchaseEmailBatch(ctx context.Context, site, domain string, count int, service string) ([]HeroEmailBatchItem, error) {
	return f.batchItems, f.emailErr
}
func (f *extrasFake) ReorderEmail(ctx context.Context, externalID string) (Email, error) {
	return f.emailRes, f.emailErr
}

func newExtrasService(t *testing.T, ef *extrasFake, store *memStore) *Service {
	t.Helper()
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: true, VerifiedAt: testNow}
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService(
		[]Provider{{ID: ProviderSMS62, Adapter: &fakeAdapter{}}, {ProviderHero, ef}},
		store, nil, func() time.Time { return testNow },
	)
}

// 关着的供应商连只读的扩展能力都拿不到：这些调用都会真打上游。
func TestHeroExtrasRequireEnabledAndVerified(t *testing.T) {
	store := newMemStore()
	svc := newExtrasService(t, &extrasFake{}, store)
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: false, VerifiedAt: testNow}

	if _, err := svc.Hero(context.Background(), ProviderHero); !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("关着的供应商必须拒绝, got %v", err)
	}
}

// 62 没有 Hero 的能力；装错供应商要报「没有这项能力」，不是空指针。
func TestSMS62HasNoHeroExtras(t *testing.T) {
	svc := newExtrasService(t, &extrasFake{}, newMemStore())
	if _, err := svc.Hero(context.Background(), ProviderSMS62); !errors.Is(err, ErrExtrasNotSupported) {
		t.Fatalf("62 不该有 Hero 能力, got %v", err)
	}
	if _, err := svc.SMS62(context.Background(), ProviderHero); !errors.Is(err, ErrExtrasNotSupported) {
		t.Fatalf("Hero 不该有 62 能力, got %v", err)
	}
}

// 租用走七态台账：成功落 subtype=2 的资源，台账 succeeded。
func TestRentRecordsSubtypeAndSucceeds(t *testing.T) {
	store := newMemStore()
	ef := &extrasFake{rentRes: Resource{ExternalID: "rent-1", Phone: "+79990000001", Subtype: SubtypeRent}}
	svc := newExtrasService(t, ef, store)

	op, err := svc.Rent(context.Background(), "op-rent", ProviderHero, HeroRentInput{Service: "go", Country: 12, DurationHours: 4})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateSucceeded || op.Kind != KindRent || op.ProviderRef != "rent-1" {
		t.Fatalf("op = %+v", op)
	}
	res, err := store.GetResource(context.Background(), op.ResourceID)
	if err != nil || res.Subtype != SubtypeRent || res.Provider != ProviderHero {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

// 同一份租用请求（同参数）未决时再发一次要被挡住——换 operation ID 也不行。
func TestRentRejectsDuplicatePendingRequest(t *testing.T) {
	store := newMemStore()
	// 让第一笔停在 submitted：Rent 返回不确定错误 → unknown（仍算未决）。
	ef := &extrasFake{rentErr: &ProtocolError{Kind: "超时"}}
	svc := newExtrasService(t, ef, store)
	in := HeroRentInput{Service: "go", Country: 12, DurationHours: 4}

	op, _ := svc.Rent(context.Background(), "op-a", ProviderHero, in)
	if op.State != StateUnknown {
		t.Fatalf("协议错误应落 unknown, got %s", op.State)
	}
	if _, err := svc.Rent(context.Background(), "op-b", ProviderHero, in); !errors.Is(err, ErrPendingDuplicate) {
		t.Fatalf("同一请求未决时必须拒绝, got %v", err)
	}
	if ef.rentCalls != 1 {
		t.Fatalf("第二次不该打上游, calls = %d", ef.rentCalls)
	}
}

// 批量买邮箱：官方响应没有 id，要补读列表；补读缺一条就是 unknown——钱已经花了。
func TestPurchaseEmailsBatchFallsToUnknownWhenReadbackIncomplete(t *testing.T) {
	store := newMemStore()
	ef := &extrasFake{
		batchItems:  []HeroEmailBatchItem{{Email: "a@x"}, {Email: "b@x"}},
		listedItems: []Email{{ExternalID: "1", Email: "a@x"}}, // 少了 b@x
	}
	svc := newExtrasService(t, ef, store)

	op, err := svc.PurchaseEmails(context.Background(), "op-em", ProviderHero, "site", "x", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateUnknown || !op.NeedsHumanReview {
		t.Fatalf("补读不全必须落 unknown 交人工, got %+v", op)
	}
	if op.RetryAllowed() {
		t.Fatal("unknown 不许重试——重试就是再买一批")
	}
}

// 单买邮箱成功后落库，台账指向邮箱行。
func TestPurchaseEmailSingleLinksOperationToEmail(t *testing.T) {
	store := newMemStore()
	ef := &extrasFake{emailRes: Email{ExternalID: "9", Email: "demo@x", Status: "WAIT"}}
	svc := newExtrasService(t, ef, store)

	op, err := svc.PurchaseEmails(context.Background(), "op-em1", ProviderHero, "site", "x", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateSucceeded || op.EmailID == "" {
		t.Fatalf("op = %+v", op)
	}
	e, err := store.GetEmail(context.Background(), op.EmailID)
	if err != nil || e.ExternalID != "9" || e.Provider != ProviderHero {
		t.Fatalf("email = %+v err = %v", e, err)
	}
}

// 取消邮箱成功后**不自行改本地状态**：上游只回了 204。
func TestEmailCancelDoesNotRewriteLocalStatus(t *testing.T) {
	store := newMemStore()
	ef := &extrasFake{}
	svc := newExtrasService(t, ef, store)
	id, _ := store.UpsertEmail(context.Background(), Email{Provider: ProviderHero, ExternalID: "9", Status: "WAIT"})

	op, err := svc.EmailAction(context.Background(), "op-cancel", KindEmailCancel, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateSucceeded || op.EmailID != id {
		t.Fatalf("op = %+v", op)
	}
	e, _ := store.GetEmail(context.Background(), id)
	if e.Status != "WAIT" {
		t.Fatalf("本地状态不该被改写, got %q", e.Status)
	}
}

// 导入上游订单：只读、不购买；62 先落订单行，号码指回它。
func TestImportUpstreamRecordsOrderAndResourcesForSMS62(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{importResources: []Resource{
		{ExternalID: TokenFingerprint("tok-a"), Phone: "+13860000001", ProviderToken: "tok-a"},
		{ExternalID: TokenFingerprint("tok-b"), Phone: "+13860000002", ProviderToken: "tok-b"},
	}}
	svc := newService(t, adapter, store)

	imported, err := svc.ImportUpstream(context.Background(), ProviderSMS62, "21968")
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 2 || adapter.purchaseCalls != 0 {
		t.Fatalf("导入 %d 个，购买调用 %d 次——导入绝不能购买", len(imported), adapter.purchaseCalls)
	}
	if adapter.importCalls != 1 {
		t.Fatalf("应只读一次上游, got %d", adapter.importCalls)
	}
	if len(store.orders) != 1 {
		t.Fatalf("62 导入应落一条订单行, got %d", len(store.orders))
	}
	for _, r := range imported {
		if r.OrderID == "" || r.Provider != ProviderSMS62 {
			t.Fatalf("号码要指回订单: %+v", r)
		}
	}
	// 同一单再导一次：按 external_id 幂等，不会多出一份号。
	if _, err := svc.ImportUpstream(context.Background(), ProviderSMS62, "21968"); err != nil {
		t.Fatal(err)
	}
	if len(store.resources) != 2 {
		t.Fatalf("重复导入不该复制号码, got %d", len(store.resources))
	}
}

// 没验证过的供应商不许导入：导入虽不花钱，但会真打上游。
func TestImportUpstreamRequiresVerifiedProvider(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true}
	if _, err := svc.ImportUpstream(context.Background(), ProviderHero, "42"); !errors.Is(err, ErrProviderNotVerified) {
		t.Fatalf("未验证必须拒绝, got %v", err)
	}
}

// 取码 Action：「还没有码」是正常状态——Action 成功、received=false，
// 页面据此继续等而不是报错。
func TestCodeFetchActionTreatsNotYetAsSuccess(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{codeErr: ErrCodeNotAvailable}
	svc := newService(t, adapter, store)
	id, _ := store.UpsertResource(context.Background(), Resource{Provider: ProviderHero, ExternalID: "act-1", Phone: "+79990000001"})

	out, err := codeFetchHandler(svc)(context.Background(), map[string]any{"resource_id": id})
	if err != nil {
		t.Fatalf("还没有码不该是错误: %v", err)
	}
	if got := out.(map[string]any)["received"]; got != false {
		t.Fatalf("received = %v, want false", got)
	}

	adapter.codeErr = nil
	adapter.code = Code{Code: "123456", Sender: "Google"}
	out, err = codeFetchHandler(svc)(context.Background(), map[string]any{"resource_id": id})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["received"] != true || result["code_id"] == "" {
		t.Fatalf("result = %v", result)
	}
	// 返回体里**不带码**：能不能看由 sms.reveal 决定，走本地验证码端点。
	if _, leaked := result["code"]; leaked {
		t.Fatal("取码结果不该带码本身")
	}
}
