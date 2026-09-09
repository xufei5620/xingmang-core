package sms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 「要号」流程（ADR-022 决策 3，XM-SMS2 #6）。
//
// 服务 + 国家 + 数量（+ 可选指定供应商）→ 按规则选供应商 → 买 → 落资源。
// 失败按规则回落下一家，**每家最多试一次**；结果未知就停——钱可能已经花了，
// 再试下一家就是双倍花钱。

func newRoutedService(t *testing.T, a62, ahero Adapter, store *memStore) *Service {
	t.Helper()
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: true, VerifiedAt: testNow}
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService(
		[]Provider{{ID: ProviderSMS62, Adapter: a62}, {ID: ProviderHero, Adapter: ahero}},
		store, nil, func() time.Time { return testNow },
	)
}

func rejectedUpstream() error {
	return connector.NewError(connector.KindRejected, "余额不足", nil)
}

// 62 的商品：按名字匹配服务，同国家两档价格。
func goods62() []CatalogItem {
	return []CatalogItem{
		{ID: "1-12-1", Name: "Google", Country: "12", DefaultPrice: "0.80", Available: 5},
		{ID: "1-12-2", Name: "Google 7天", Country: "12", DefaultPrice: "0.40", Available: 10},
		{ID: "2-12-1", Name: "Telegram", Country: "12", DefaultPrice: "0.10", Available: 10},
	}
}

func heroOK() *fakeAdapter {
	return &fakeAdapter{purchaseOutcome: PurchaseOutcome{Resources: []Resource{
		{ExternalID: "a1", Phone: "79990000001", PhoneMask: "****0001", Service: "google", Country: "12"},
	}}}
}

func TestRequestNumberFallsBackToNextProviderOnce(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{catalog: goods62(), purchaseErr: rejectedUpstream()}
	hero := heroOK()
	svc := newRoutedService(t, a62, hero, store)

	out, err := svc.RequestNumber(context.Background(), "req-1", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateSucceeded || out.Provider != ProviderHero {
		t.Fatalf("62 明确拒绝后应回落到 Hero 成功, got %+v", out)
	}
	if len(out.Attempts) != 2 || out.Attempts[0].Provider != ProviderSMS62 || out.Attempts[0].State != StateFailed {
		t.Fatalf("应记录两次尝试且第一次是 failed, got %+v", out.Attempts)
	}
	if a62.purchaseCalls != 1 || hero.purchaseCalls != 1 {
		t.Fatalf("每家最多打一次上游, got 62=%d hero=%d", a62.purchaseCalls, hero.purchaseCalls)
	}
	if out.OperationID == "" || len(out.ResourceIDs) != 1 {
		t.Fatalf("成功应带操作 ID 与号码 ID, got %+v", out)
	}
	r, _ := store.GetResource(context.Background(), out.ResourceIDs[0])
	if r.State != StateWaitingCode {
		t.Fatalf("新买的号应是待收码, got %q", r.State)
	}
}

// 结果未知 = 钱可能已经花了。**停下，不回落。**
func TestRequestNumberStopsAfterUnknownOutcome(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{catalog: goods62(), purchaseErr: errors.New("read tcp: i/o timeout")}
	hero := heroOK()
	svc := newRoutedService(t, a62, hero, store)

	out, err := svc.RequestNumber(context.Background(), "req-2", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateUnknown || out.Provider != ProviderSMS62 || out.OperationID == "" {
		t.Fatalf("未知结果应停在 62 并带操作 ID 供人核对, got %+v", out)
	}
	if hero.purchaseCalls != 0 || len(out.Attempts) != 1 {
		t.Fatalf("未知之后不许再试下一家, hero=%d attempts=%+v", hero.purchaseCalls, out.Attempts)
	}
}

func TestRequestNumberAllRejectedIsFailed(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{catalog: goods62(), purchaseErr: rejectedUpstream()}
	hero := &fakeAdapter{purchaseErr: rejectedUpstream()}
	svc := newRoutedService(t, a62, hero, store)

	out, err := svc.RequestNumber(context.Background(), "req-3", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateFailed || out.Provider != "" || len(out.Attempts) != 2 {
		t.Fatalf("全部明确失败应为 failed 且没有赢家, got %+v", out)
	}
}

// 人指定了供应商就只试那一家。
func TestRequestNumberPreferredProviderOnly(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{catalog: goods62()}
	hero := heroOK()
	svc := newRoutedService(t, a62, hero, store)

	out, err := svc.RequestNumber(context.Background(), "req-4", RequestInput{Service: "google", Country: "12", Quantity: 1, Provider: ProviderHero})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != ProviderHero || a62.purchaseCalls != 0 || len(out.Attempts) != 1 {
		t.Fatalf("指定 Hero 就不该碰 62, got %+v (62 calls=%d)", out, a62.purchaseCalls)
	}
}

// 62 没有「服务」概念，只有商品：按名字匹配服务、按国家匹配、取上限内最便宜且
// 有货的那个；数量原样传。
func TestRequestNumberPicksCheapest62GoodsUnderCap(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{
		catalog:         goods62(),
		purchaseOutcome: PurchaseOutcome{OrderRef: "o1"},
		importResources: []Resource{{ExternalID: "t1", Phone: "15550000001", ProviderToken: "tok1"}, {ExternalID: "t2", Phone: "15550000002", ProviderToken: "tok2"}},
	}
	hero := heroOK()
	svc := newRoutedService(t, a62, hero, store)
	ctx := context.Background()
	if _, err := svc.SetRoutingRule(ctx, RoutingRule{Service: "google", Country: "12", Providers: []string{ProviderSMS62}, MaxUnitPriceText: "0.50", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	out, err := svc.RequestNumber(ctx, "req-5", RequestInput{Service: "google", Country: "12", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateSucceeded || out.Provider != ProviderSMS62 || out.RuleID == "" {
		t.Fatalf("应按规则走 62 成功, got %+v", out)
	}
	if a62.lastPurchase.GoodsID != "1-12-2" || a62.lastPurchase.Quantity != 2 {
		t.Fatalf("应选上限内最便宜的 Google 商品且数量原样, got %+v", a62.lastPurchase)
	}
	if len(out.ResourceIDs) != 2 {
		t.Fatalf("两个号都该落库, got %+v", out.ResourceIDs)
	}

	// 上限低于所有商品：不买，尝试记录说明原因。
	if _, err := svc.SetRoutingRule(ctx, RoutingRule{Service: "google", Country: "12", Providers: []string{ProviderSMS62}, MaxUnitPriceText: "0.30", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	out, err = svc.RequestNumber(ctx, "req-6", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateFailed || a62.purchaseCalls != 1 || !strings.Contains(out.Attempts[0].Reason, "上限") {
		t.Fatalf("超上限不该买, got %+v (calls=%d)", out, a62.purchaseCalls)
	}
}

// Hero 有服务与国家的概念：服务原样、国家转数字、上限作 maxPrice 交给上游。
func TestRequestNumberHeroGetsServiceCountryAndCap(t *testing.T) {
	store := newMemStore()
	hero := heroOK()
	svc := newRoutedService(t, &fakeAdapter{}, hero, store)
	ctx := context.Background()
	if _, err := svc.SetRoutingRule(ctx, RoutingRule{Service: "google", Country: "*", Providers: []string{ProviderHero}, MaxUnitPriceText: "0.5", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RequestNumber(ctx, "req-7", RequestInput{Service: "Google", Country: "12", Quantity: 3}); err != nil {
		t.Fatal(err)
	}
	p := hero.lastPurchase
	if p.Service != "google" || p.Country != 12 || p.Quantity != 3 || p.MaxPrice != "0.5" {
		t.Fatalf("Hero 入参不对: %+v", p)
	}

	// Hero 的国家必须是数字 ID：不是数字就跳过这家，说明原因，不打上游。
	out, err := svc.RequestNumber(ctx, "req-8", RequestInput{Service: "google", Country: "us", Quantity: 1, Provider: ProviderHero})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateFailed || hero.purchaseCalls != 1 || out.Attempts[0].Reason == "" {
		t.Fatalf("非数字国家不该打 Hero, got %+v (calls=%d)", out, hero.purchaseCalls)
	}
}

// 关着或没验证的供应商算「试过了」：跳过并说明，回落下一家。
func TestRequestNumberSkipsDisabledProvider(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{catalog: goods62()}
	hero := heroOK()
	svc := newRoutedService(t, a62, hero, store)
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: false, VerifiedAt: testNow}

	out, err := svc.RequestNumber(context.Background(), "req-9", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != ProviderHero || a62.purchaseCalls != 0 {
		t.Fatalf("关着的 62 不该被打, got %+v", out)
	}
	if len(out.Attempts) != 2 || !strings.Contains(out.Attempts[0].Reason, "未启用") {
		t.Fatalf("跳过也要记一笔并说明原因, got %+v", out.Attempts)
	}
}

func TestRequestNumberRejectsBadInput(t *testing.T) {
	store := newMemStore()
	svc := newRoutedService(t, &fakeAdapter{}, heroOK(), store)
	cases := []struct {
		name string
		id   string
		in   RequestInput
	}{
		{"服务为空", "r", RequestInput{Country: "12", Quantity: 1}},
		{"国家为空", "r", RequestInput{Service: "google", Quantity: 1}},
		{"国家通配", "r", RequestInput{Service: "google", Country: "*", Quantity: 1}},
		{"数量为零", "r", RequestInput{Service: "google", Country: "12"}},
		{"数量超上限", "r", RequestInput{Service: "google", Country: "12", Quantity: 201}},
		{"请求 ID 为空", "", RequestInput{Service: "google", Country: "12", Quantity: 1}},
		{"未知供应商", "r", RequestInput{Service: "google", Country: "12", Quantity: 1, Provider: "nobody"}},
	}
	for _, c := range cases {
		if _, err := svc.RequestNumber(context.Background(), c.id, c.in); !errors.Is(err, ErrRequestInvalid) {
			t.Errorf("%s: 应为 ErrRequestInvalid, got %v", c.name, err)
		}
	}
}

// 同一个 request_id 再来一次 = 回放：不再打上游，回同一个结果。
//
// 这是 XM-SMS4 接入规范的基础：调用方重试带同一个 request_id，永远不会多买。
func TestRequestNumberIsIdempotentPerRequestID(t *testing.T) {
	store := newMemStore()
	a62 := &fakeAdapter{catalog: goods62(), purchaseErr: rejectedUpstream()}
	hero := heroOK()
	svc := newRoutedService(t, a62, hero, store)
	ctx := context.Background()

	first, err := svc.RequestNumber(ctx, "req-10", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.RequestNumber(ctx, "req-10", RequestInput{Service: "google", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != StateSucceeded || second.Provider != first.Provider || second.OperationID != first.OperationID {
		t.Fatalf("回放应回同一个结果, first=%+v second=%+v", first, second)
	}
	if a62.purchaseCalls != 1 || hero.purchaseCalls != 1 {
		t.Fatalf("回放不许再打上游, 62=%d hero=%d", a62.purchaseCalls, hero.purchaseCalls)
	}
	if len(second.ResourceIDs) != 1 || second.ResourceIDs[0] != first.ResourceIDs[0] {
		t.Fatalf("回放应回同一批号码, got %+v", second.ResourceIDs)
	}
}

func TestRequestActionRegisteredAsPurchase(t *testing.T) {
	store := newMemStore()
	svc := newRoutedService(t, &fakeAdapter{catalog: goods62(), purchaseErr: rejectedUpstream()}, heroOK(), store)
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	def, handler, ok := reg.Lookup(ActionNumberRequest, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", ActionNumberRequest)
	}
	if def.RiskLevel != action.L1 || def.Permission != PermissionPurchase {
		t.Errorf("risk=%v perm=%q", def.RiskLevel, def.Permission)
	}
	params := map[string]any{"request_id": "req-a", "service": "google", "country": "12", "quantity": 1}
	if err := def.Schema.Validate(params); err != nil {
		t.Fatalf("参数应通过 Schema: %v", err)
	}
	out, err := handler(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["state"] != string(StateSucceeded) || result["provider"] != ProviderHero {
		t.Fatalf("结果应说明状态与赢家, got %+v", result)
	}
	attempts, _ := result["attempts"].([]map[string]any)
	if len(attempts) != 2 {
		t.Fatalf("结果应带每一次尝试, got %+v", result["attempts"])
	}
	// 非法输入要翻成 INVALID_PARAMS，不能是「执行失败」。
	_, err = handler(context.Background(), map[string]any{"request_id": "req-b", "service": "google", "country": "*", "quantity": 1})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("应为 INVALID_PARAMS, got %v", err)
	}
}
