package sms

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore 的集成测试：SQL 只有对着真实 Postgres 跑过才算数。
//
// 迁移 000040 给 sms_resource 加了五列、新建了 sms_email、重建了
// sms_operation 的 kind CHECK——这三样在编译期一个都看不出来：列名写错、
// 参数位错、CHECK 漏了新 kind 全是运行时才炸。
//
// 用 scripts/dev/worktree-testdb.sh 起本 worktree 专属的库：
//
//	eval "$(scripts/dev/worktree-testdb.sh)"
//	go test ./internal/platform/sms/ -run TestPgStore -count=1
const testEnvironment = "development"

func pgStore(t *testing.T) *PgStore {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过 PgStore 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{
		"sms.sms_code", "sms.sms_operation", "sms.sms_resource", "sms.sms_order",
		"sms.sms_email", "sms.provider_status",
	} {
		if _, err := pool.Exec(context.Background(), "TRUNCATE "+table+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	return NewPgStore(pool, testEnvironment, func() time.Time { return testNow })
}

// 资源的五个官方新字段要能写进去、读回来；同步传空时**保留原值**。
func TestPgStoreResourceKeepsOfficialFieldsAcrossSync(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()

	id, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-1", Phone: "79990000001", PhoneMask: "****0001",
		Service: "go", Country: "12", Status: "1",
		Operator: "mts", PriceText: "0.35", VerificationType: "sms", Subtype: SubtypeRent, CountryPhoneCode: "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 一次不带新字段的同步（比如列表接口不回它们）。
	if _, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-1", Status: "6",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetResource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "6" {
		t.Errorf("status 应更新为 6, got %q", got.Status)
	}
	if got.Operator != "mts" || got.PriceText != "0.35" || got.VerificationType != "sms" ||
		got.Subtype != SubtypeRent || got.CountryPhoneCode != "7" {
		t.Errorf("官方字段被同步冲掉了: %+v", got)
	}
}

// 邮箱：按 (provider, external_id) 幂等；value 一旦收到不会被后续同步冲成空。
func TestPgStoreEmailUpsertKeepsValue(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()

	id, err := store.UpsertEmail(ctx, Email{
		Provider: ProviderHero, ExternalID: "9", Site: "example.com", Email: "a@x",
		Status: "WAIT", CostText: "0.20", Currency: 840, UpstreamDate: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 收到验证内容。
	if _, err := store.UpsertEmail(ctx, Email{Provider: ProviderHero, ExternalID: "9", Status: "SUCCESS", Value: "123456"}); err != nil {
		t.Fatal(err)
	}
	// 再来一次不带 value 的列表同步。
	id2, err := store.UpsertEmail(ctx, Email{Provider: ProviderHero, ExternalID: "9", Status: "SUCCESS"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("同一邮箱应保持本地 UUID: %s vs %s", id, id2)
	}
	got, err := store.GetEmail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "123456" || got.Status != "SUCCESS" || got.Site != "example.com" || got.Currency != 840 {
		t.Errorf("email = %+v", got)
	}
	list, err := store.ListEmails(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err = %v", list, err)
	}
}

// 62 没有邮箱接码：CHECK 要挡住误写。
func TestPgStoreEmailRejectsSMS62(t *testing.T) {
	store := pgStore(t)
	if _, err := store.UpsertEmail(context.Background(), Email{Provider: ProviderSMS62, ExternalID: "x"}); err == nil {
		t.Fatal("62 的邮箱行必须被 CHECK 挡住")
	}
}

// 新 kind 要能进台账（CHECK 重建过），并能带 email_id。
func TestPgStoreOperationAcceptsNewKinds(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	emailID, err := store.UpsertEmail(ctx, Email{Provider: ProviderHero, ExternalID: "9", Status: "WAIT"})
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{KindRent, KindEmailPurchase, KindEmailCancel, KindEmailReorder, KindFavoriteSet, KindFavoriteRemove} {
		op := Operation{
			ID: "00000000-0000-0000-0000-00000000000" + string(rune('1'+i)), Provider: ProviderHero, Kind: kind,
			RequestHash: "h" + kind, ParamsSummary: kind, StartedAt: testNow, UpdatedAt: testNow, EmailID: emailID,
		}
		if err := store.PrepareOperation(ctx, op); err != nil {
			t.Fatalf("kind %s 进不了台账: %v", kind, err)
		}
	}
}

// 迁移 000041 之后，数据库不再按名字挡供应商：一个注册表里没有的名字要被
// **代码**拒绝，而不是撞 CHECK。这条测试同时钉住两件事：CHECK 真的没了
// （否则错误文案会是 constraint 而不是「不在注册表里」），以及代码的兜底在。
func TestPgStoreRejectsUnknownProviderInCodeNotByCheck(t *testing.T) {
	store := pgStore(t)
	_, err := store.UpsertResource(context.Background(), Resource{Provider: "nobody", ExternalID: "x", Phone: "1"})
	if err == nil {
		t.Fatal("不在注册表里的供应商必须被拒绝")
	}
	if !errors.Is(err, ErrProviderUnknown) {
		t.Fatalf("应由注册表拒绝，而不是数据库 CHECK: %v", err)
	}
}

// token 形状约束搬到代码后仍然有效：62 必须带、Hero 必须不带。
func TestPgStoreTokenShapeEnforcedInCode(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderSMS62, ExternalID: "a", Phone: "1"}); err == nil {
		t.Fatal("62 的号码没有 token 必须被拒绝")
	}
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderHero, ExternalID: "b", Phone: "2", ProviderToken: "t"}); err == nil {
		t.Fatal("Hero 的号码带 token 必须被拒绝")
	}
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderSMS62, ExternalID: "c", Phone: "3", ProviderToken: "tok"}); err != nil {
		t.Fatalf("合法的 62 号码应能落库: %v", err)
	}
}

// 这条才真正证明 CHECK 没了：provider_status 没有代码层校验，直接写一个注册表
// 里不存在的名字——迁移 000041 之前会撞 provider_status_provider_known。
// 「接第三家不改迁移」就靠这一点。
func TestPgStoreProviderStatusAcceptsThirdProviderAfter000041(t *testing.T) {
	store := pgStore(t)
	if err := store.SetProviderEnabled(context.Background(), "third_provider", true, testNow); err != nil {
		t.Fatalf("迁移 000041 后不该再按名字挡供应商: %v", err)
	}
}

// 迁移 000042：state 列。同步不带 state 时保留原值；SetResourceState 单独改它。
func TestPgStoreResourceStateRoundTrip(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()

	id, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-s1", Phone: "79990000009", Status: "1",
		State: StateWaitingCode,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 一次不带 state 的同步（比如 62 的导入只回号码）。
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderHero, ExternalID: "act-s1", Status: "2"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetResource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateWaitingCode {
		t.Fatalf("空 state 的同步应保留原值, got %q", got.State)
	}

	if err := store.SetResourceState(ctx, id, StateCodeReceived, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetResource(ctx, id)
	if got.State != StateCodeReceived {
		t.Fatalf("SetResourceState 没写进去, got %q", got.State)
	}
	// 列表也要带出来：页面的号码栏读的是列表。
	list, err := store.ListResources(ctx, ProviderHero, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != StateCodeReceived {
		t.Fatalf("ListResources 应带 state, got %+v", list)
	}
	// 带 state 的同步（Hero 回读到状态 6）会覆盖。
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderHero, ExternalID: "act-s1", Status: "6", State: StateFinished}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetResource(ctx, id)
	if got.State != StateFinished || got.Status != "6" {
		t.Fatalf("带 state 的同步应覆盖, got state=%q status=%q", got.State, got.Status)
	}
}

// 迁移 000043：路由规则。同「服务 × 国家」覆盖而不是第二条；numeric 进出都是文本；
// text[] 顺序原样保留；删不存在的（含不是 uuid 的字符串）是 ErrRoutingRuleNotFound。
func TestPgStoreRoutingRuleRoundTrip(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, "TRUNCATE sms.routing_rule"); err != nil {
		t.Fatal(err)
	}

	id, err := store.UpsertRoutingRule(ctx, RoutingRule{
		Service: "go", Country: "*", Providers: []string{ProviderHero, ProviderSMS62},
		MaxUnitPriceText: "0.35", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.UpsertRoutingRule(ctx, RoutingRule{
		Service: "go", Country: "*", Providers: []string{ProviderSMS62}, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("同键应覆盖同一条, got %q vs %q", again, id)
	}
	if _, err := store.UpsertRoutingRule(ctx, RoutingRule{
		Service: "*", Country: "7", Providers: []string{ProviderSMS62}, MaxUnitPriceText: "1.5", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	rules, err := store.ListRoutingRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("应有两条, got %+v", rules)
	}
	// ORDER BY service, country："*" 排在 "go" 前。
	if rules[0].Service != "*" || rules[0].MaxUnitPriceText != "1.5" {
		t.Errorf("numeric 应以文本原样回来, got %+v", rules[0])
	}
	if rules[1].ID != id || rules[1].Enabled || rules[1].MaxUnitPriceText != "" ||
		len(rules[1].Providers) != 1 || rules[1].Providers[0] != ProviderSMS62 {
		t.Errorf("覆盖后应是新值（上限被清空、停用、只剩 62）, got %+v", rules[1])
	}
	if rules[1].UpdatedAt.IsZero() {
		t.Errorf("updated_at 应被填上")
	}

	if err := store.RemoveRoutingRule(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveRoutingRule(ctx, id); !errors.Is(err, ErrRoutingRuleNotFound) {
		t.Errorf("再删应为 ErrRoutingRuleNotFound, got %v", err)
	}
	if err := store.RemoveRoutingRule(ctx, "not-a-uuid"); !errors.Is(err, ErrRoutingRuleNotFound) {
		t.Errorf("不是 uuid 的 ID 也是不存在, got %v", err)
	}
	// 空供应商列表被 CHECK 挡住（代码层 ValidateRoutingRule 之外的最后一道）。
	if _, err := store.UpsertRoutingRule(ctx, RoutingRule{Service: "x", Country: "*", Providers: []string{}, Enabled: true}); err == nil {
		t.Errorf("空供应商列表应被 CHECK 挡住")
	}
}

// 迁移 000044：号码记下买它的操作。FK 指向 sms_operation；不带 operation_id 的
// 同步保留原值；ListResourcesByOperation 只回那一笔的号。
func TestPgStoreResourceOperationLink(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	opID := "0f1e2d3c-4b5a-4968-8776-655443322110"
	if err := store.PrepareOperation(ctx, Operation{
		ID: opID, Provider: ProviderHero, Kind: KindPurchase, RequestHash: "h-link", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	id, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-op1", Phone: "79990000021", OperationID: opID, State: StateWaitingCode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderHero, ExternalID: "act-op2", Phone: "79990000022"}); err != nil {
		t.Fatal(err)
	}
	// 一次不带 operation_id 的同步（列表接口回读）。
	if _, err := store.UpsertResource(ctx, Resource{Provider: ProviderHero, ExternalID: "act-op1", Status: "4"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetResource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.OperationID != opID {
		t.Fatalf("同步不该冲掉 operation_id, got %q", got.OperationID)
	}
	linked, err := store.ListResourcesByOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0].ID != id {
		t.Fatalf("应只回这一笔买的号, got %+v", linked)
	}
	// 指向不存在的操作被 FK 挡住：号码不能声称自己被一笔不存在的操作买下。
	if _, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-op3", Phone: "1", OperationID: "0f1e2d3c-4b5a-4968-8776-000000000000",
	}); err == nil {
		t.Fatalf("不存在的操作应被 FK 挡住")
	}
}

// 迁移 000045：余额快照。追加而不是覆盖；numeric 进出都是文本；
// LatestBalanceSnapshots 每家只回最新一条。
func TestPgStoreBalanceSnapshotAppendsAndReadsLatest(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, "TRUNCATE sms.balance_snapshot"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)

	for _, row := range []BalanceSnapshot{
		{Provider: ProviderHero, AmountText: "5.0000", TakenAt: base},
		{Provider: ProviderHero, AmountText: "4.2000", Currency: "840", TakenAt: base.Add(time.Hour)},
		{Provider: ProviderSMS62, AmountText: "100.5000", TakenAt: base},
	} {
		if _, err := store.SaveBalanceSnapshot(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	latest, err := store.LatestBalanceSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("每家一条, got %+v", latest)
	}
	byProvider := map[string]BalanceSnapshot{}
	for _, row := range latest {
		byProvider[row.Provider] = row
	}
	hero := byProvider[ProviderHero]
	// 币种与时间必须来自**同一次抓取**：DISTINCT ON 回的是那一行，不是最大值。
	if hero.AmountText != "4.2000" || hero.Currency != "840" || !hero.TakenAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("Hero 最新一条不对: %+v", hero)
	}
	if byProvider[ProviderSMS62].AmountText != "100.5000" {
		t.Fatalf("62 的快照不对: %+v", byProvider[ProviderSMS62])
	}
	// 追加而不是覆盖：三条都还在。
	var count int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM sms.balance_snapshot WHERE environment = $1", testEnvironment).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("快照应追加, got %d 行", count)
	}
}
