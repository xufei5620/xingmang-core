package sms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 消费者配额（ADR-022，XM-SMS4 #3）。
//
// 机器身份要号是花真钱：一个循环里的 bug 能在十分钟内买光余额。所以机器**必须
// 先被登记**——没有配额行就不许调用，与「新装环境两家默认都是关的」同一条纪律
// （宪法 26 条：天生不允许花钱，必须显式打开）。人不受影响：人有自己的权限闸
// 与页面上的两步确认。
//
// 花费上限**按币种各自计**：两家的币种不同且不折算，一个跨币种的总额上限
// 是个算不出来的数。

func machineCtx(id string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: id, Type: principal.TypeService, IdentityZone: "machine",
		Issuer: "test", Subject: id, Environment: "development",
		Scopes: []string{PermissionPurchase, PermissionRead, PermissionManage},
	})
}

func humanCtx() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff-1", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "test", Subject: "staff-1", Environment: "development",
		Scopes: []string{PermissionPurchase},
	})
}

// quotaFake 每次买号都回**新的**号：固定 external_id 会让第二次 upsert 覆盖
// 第一次，用量永远停在 1——那是替身的假象，不是配额的行为。
type quotaFake struct {
	fakeAdapter
	round int
}

func (f *quotaFake) Purchase(ctx context.Context, in PurchaseInput) (PurchaseOutcome, error) {
	f.purchaseCalls++
	f.round++
	out := make([]Resource, 0, in.Quantity)
	for i := 0; i < in.Quantity; i++ {
		id := "a" + itoa(f.round) + "-" + itoa(i)
		out = append(out, Resource{
			ExternalID: id, Phone: "7999000" + id, Service: "go", Country: "12", PriceText: "0.35",
		})
	}
	return PurchaseOutcome{Resources: out}, nil
}

func quotaService(t *testing.T, store *memStore) *Service {
	t.Helper()
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService([]Provider{{ID: ProviderHero, Adapter: &quotaFake{}}}, store, nil,
		func() time.Time { return testNow })
}

// 没有配额行的机器**一次都不能要号**：登记是显式动作。
func TestRequestNumberRejectsUnregisteredMachine(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)

	_, err := svc.RequestNumber(machineCtx("svc:unknown"), "req-1",
		RequestInput{Service: "go", Country: "12", Quantity: 1})
	if !errors.Is(err, ErrQuotaNotRegistered) {
		t.Fatalf("未登记的机器必须被拒, got %v", err)
	}
	if ops, _ := store.ListOperations(context.Background(), "", 10); len(ops) != 0 {
		t.Fatalf("拒绝要发生在打上游之前, got %+v", ops)
	}
}

// 人不受配额约束：人有自己的权限闸与页面上的两步确认。
func TestRequestNumberIgnoresQuotaForHumans(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)

	out, err := svc.RequestNumber(humanCtx(), "req-human", RequestInput{Service: "go", Country: "12", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateSucceeded {
		t.Fatalf("人要号不该被配额挡, got %+v", out)
	}
}

// 停用的配额等于没有：停一个跑飞了的消费者要立刻生效。
func TestRequestNumberRejectsDisabledConsumer(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	ctx := machineCtx("svc:worker")
	if _, err := svc.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc:worker", DailyRequests: 10, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RequestNumber(ctx, "req-2", RequestInput{Service: "go", Country: "12", Quantity: 1}); !errors.Is(err, ErrQuotaNotRegistered) {
		t.Fatalf("停用的消费者应被拒, got %v", err)
	}
}

// 日请求数用完就拒。**按数量算而不是按次数**：一次要 50 个号与 50 次要一个号
// 花的钱一样多。
func TestRequestNumberEnforcesDailyRequestQuota(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	ctx := machineCtx("svc:worker")
	if _, err := svc.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc:worker", DailyRequests: 2, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, err := svc.RequestNumber(ctx, "req-a"+itoa(i), RequestInput{Service: "go", Country: "12", Quantity: 1}); err != nil {
			t.Fatalf("第 %d 次应该成功: %v", i+1, err)
		}
	}
	_, err := svc.RequestNumber(ctx, "req-over", RequestInput{Service: "go", Country: "12", Quantity: 1})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("第三次应超配额, got %v", err)
	}
	// 一次要多个也按个数算。
	store2 := newMemStore()
	svc2 := quotaService(t, store2)
	if _, err := svc2.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc:worker", DailyRequests: 2, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.RequestNumber(machineCtx("svc:worker"), "req-bulk",
		RequestInput{Service: "go", Country: "12", Quantity: 5}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("一次要 5 个超过日配额 2，应被拒, got %v", err)
	}
}

// 回放（同一个 request_id 重试）**不重复计数**：那不是新的一次要号。
func TestQuotaCountsRequestOncePerRequestID(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	ctx := machineCtx("svc:worker")
	if _, err := svc.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc:worker", DailyRequests: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RequestNumber(ctx, "req-same", RequestInput{Service: "go", Country: "12", Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	// 同一个 request_id 再来一次是回放，不该被算成第二次。
	if _, err := svc.RequestNumber(ctx, "req-same", RequestInput{Service: "go", Country: "12", Quantity: 1}); err != nil {
		t.Fatalf("回放不该被配额挡: %v", err)
	}
}

// 花费上限是**止损线**，不是预授权：买之前没人知道这次要花多少（62 连买完
// 都不说），所以语义是「今天已经花到上限就不再放行」——最多超出一次请求。
// 按币种各自计：两家的币种不同且不折算。
func TestQuotaSpendCapStopsAfterCapReached(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	ctx := machineCtx("svc:worker")
	if _, err := svc.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc:worker", DailyRequests: 100, DailySpendCapText: "0.30", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// 第一次放行：今天还没花过钱。买完是 0.35，已经越过 0.30 这条线。
	if _, err := svc.RequestNumber(ctx, "req-s1", RequestInput{Service: "go", Country: "12", Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	// 第二次就拦住了。
	if _, err := svc.RequestNumber(ctx, "req-s2", RequestInput{Service: "go", Country: "12", Quantity: 1}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("到了止损线应被拒, got %v", err)
	}

	// 上限宽到花不到时不拦。
	store2 := newMemStore()
	svc2 := quotaService(t, store2)
	if _, err := svc2.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc:worker", DailyRequests: 100, DailySpendCapText: "100.00", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	ctx2 := machineCtx("svc:worker")
	if _, err := svc2.RequestNumber(ctx2, "req-w1", RequestInput{Service: "go", Country: "12", Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.RequestNumber(ctx2, "req-w2", RequestInput{Service: "go", Country: "12", Quantity: 1}); err != nil {
		t.Fatalf("远没到上限不该拦: %v", err)
	}
}

func TestSetConsumerQuotaValidates(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	ctx := context.Background()
	bad := []struct {
		name string
		in   ConsumerQuota
	}{
		{"消费者为空", ConsumerQuota{DailyRequests: 1}},
		{"日配额为负", ConsumerQuota{Consumer: "svc:a", DailyRequests: -1}},
		{"上限不是十进制", ConsumerQuota{Consumer: "svc:a", DailyRequests: 1, DailySpendCapText: "abc"}},
		{"上限为负", ConsumerQuota{Consumer: "svc:a", DailyRequests: 1, DailySpendCapText: "-1"}},
	}
	for _, c := range bad {
		if _, err := svc.SetConsumerQuota(ctx, c.in); !errors.Is(err, ErrQuotaInvalid) {
			t.Errorf("%s 应被拒, got %v", c.name, err)
		}
	}
	// 日配额 0 = 一次都不许（显式的「停掉但保留登记」），合法。
	if _, err := svc.SetConsumerQuota(ctx, ConsumerQuota{Consumer: "svc:a", DailyRequests: 0, Enabled: true}); err != nil {
		t.Fatalf("0 应当合法: %v", err)
	}
}

// 配额只给人配：机器不该能给自己提额。
func TestQuotaActionRegisteredForHumansOnly(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	def, handler, ok := reg.Lookup(ActionQuotaSet, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", ActionQuotaSet)
	}
	if def.RiskLevel != action.L1 || def.Permission != PermissionManage {
		t.Errorf("risk=%v perm=%q", def.RiskLevel, def.Permission)
	}
	for _, pt := range def.PrincipalTypes {
		if pt != principal.TypeHuman {
			t.Errorf("配额只给人配, got %v", def.PrincipalTypes)
		}
	}
	params := map[string]any{"consumer": "svc:worker", "daily_requests": 10, "daily_spend_cap": "5.00", "enabled": true}
	if err := def.Schema.Validate(params); err != nil {
		t.Fatalf("参数应通过 Schema: %v", err)
	}
	if _, err := handler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	quotas, _ := svc.ListConsumerQuotas(context.Background())
	if len(quotas) != 1 || quotas[0].DailyRequests != 10 || quotas[0].DailySpendCapText != "5.00" {
		t.Fatalf("配额没写进去: %+v", quotas)
	}
	if _, err := handler(context.Background(), map[string]any{"consumer": "", "daily_requests": 1}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("非法参数应为 INVALID_PARAMS, got %v", err)
	}
}

// 三个 Action 对机器开放（XM-SMS4 #2），其余仍然只给人。
func TestMachinePrincipalsAllowedOnConsumerActions(t *testing.T) {
	store := newMemStore()
	svc := quotaService(t, store)
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	forMachines := map[string]bool{
		ActionNumberRequest: true, ActionCodeFetch: true, ActionResourceAction: true,
	}
	for id := range forMachines {
		def, _, ok := reg.Lookup(id, actionVersion)
		if !ok {
			t.Fatalf("%s 未注册", id)
		}
		if !containsType(def.PrincipalTypes, principal.TypeService) {
			t.Errorf("%s 应允许 SERVICE, got %v", id, def.PrincipalTypes)
		}
		// **AI 不在其中**：让 AI 身份自己买号是另一回事，要产品负责人拍板。
		if containsType(def.PrincipalTypes, principal.TypeAI) {
			t.Errorf("%s 不该允许 AI, got %v", id, def.PrincipalTypes)
		}
	}
	// 花钱的另外几个仍然只给人。
	for _, id := range []string{ActionNumberPurchase, ActionProviderSetEnabled, ActionRoutingSet, ActionQuotaSet} {
		def, _, ok := reg.Lookup(id, actionVersion)
		if !ok {
			t.Fatalf("%s 未注册", id)
		}
		if containsType(def.PrincipalTypes, principal.TypeService) {
			t.Errorf("%s 不该对机器开放, got %v", id, def.PrincipalTypes)
		}
	}
}

func containsType(types []principal.Type, want principal.Type) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}
