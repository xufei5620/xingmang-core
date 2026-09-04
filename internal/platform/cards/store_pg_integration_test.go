package cards

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// PgStore 的集成测试：SQL 只有对着真实 Postgres 跑过才算数。
//
// 多账号改造把三张表的唯一键、索引与每一条 SQL 都改了一遍，而这些改动
// 在编译期一个都看不出来——列名写错、参数位错、唯一键冲突全是运行时才炸。
//
// 用 scripts/dev/worktree-testdb.sh 起本 worktree 专属的库：
//
//	eval "$(scripts/dev/worktree-testdb.sh)"
//	go test ./internal/platform/cards/ -run TestPgStore -count=1
const testEnvironment = "development"

func pgStore(t *testing.T) (*PgStore, *pgxpool.Pool) {
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

	// 每个用例从干净状态开始：这三张表只有本包在用，整表清空是安全的。
	for _, table := range []string{
		"cards.card_operation", "cards.infini_card_transaction", "cards.infini_card",
		// 新增表忘了加进来的症状很隐蔽：单跑通过、连跑失败，
		// 因为上一轮的行把唯一键占住了。
		"cards.webhook_event",
	} {
		if _, err := pool.Exec(context.Background(), "TRUNCATE "+table); err != nil {
			t.Fatal(err)
		}
	}

	return NewPgStore(pool, testEnvironment, func() time.Time { return issueNow }), pool
}

func TestPgStoreBeginOperationIsIdempotent(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	op := Operation{
		IdempotencyKey: "k1", Account: "CHRIS", Kind: OpIssue, State: StatePending,
		Alias: "xm-abc", AmountText: "10", TokenType: "USDT", StartedAt: issueNow,
	}

	first, existed, err := store.BeginOperation(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if existed {
		t.Fatal("第一次插入不该报「已存在」")
	}
	if first.Account != "CHRIS" {
		t.Fatalf("Account = %q", first.Account)
	}

	// 同键第二次：必须报已存在并回既有记录，绝不能插出第二行
	second, existed, err := store.BeginOperation(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("同一幂等键第二次必须报「已存在」")
	}
	if second.IdempotencyKey != "k1" || second.Account != "CHRIS" {
		t.Fatalf("回读的记录不对: %+v", second)
	}
}

// 幂等键是环境内全局唯一，**不按账号分**：同键换个账号再来一次也要被去重挡住。
func TestPgStoreIdempotencyKeyIsGlobalAcrossAccounts(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	base := Operation{
		IdempotencyKey: "shared", Kind: OpIssue, State: StatePending,
		AmountText: "10", TokenType: "USDT", StartedAt: issueNow,
	}
	chris := base
	chris.Account = "CHRIS"
	if _, _, err := store.BeginOperation(ctx, chris); err != nil {
		t.Fatal(err)
	}

	linfeng := base
	linfeng.Account = "LINFENG"
	stored, existed, err := store.BeginOperation(ctx, linfeng)
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("同一幂等键换账号也必须被去重挡住")
	}
	if stored.Account != "CHRIS" {
		t.Fatalf("回读的应是先落的那条（CHRIS），got %q", stored.Account)
	}
}

func TestPgStoreResolveOperationRoundTrips(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	op := Operation{
		IdempotencyKey: "k2", Account: "CHRIS", Kind: OpIssue, State: StatePending,
		Alias: "xm-def", AmountText: "10", TokenType: "USDT", StartedAt: issueNow,
	}
	if _, _, err := store.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}

	op.State = StateUnknown
	op.NeedsHumanReview = true
	op.Reason = "超过宽限期仍未查到"
	if err := store.ResolveOperation(ctx, op); err != nil {
		t.Fatal(err)
	}

	attention, err := store.OperationsNeedingAttention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(attention) != 1 {
		t.Fatalf("待人工处置应有 1 条, got %d", len(attention))
	}
	got := attention[0]
	if got.State != StateUnknown || !got.NeedsHumanReview {
		t.Fatalf("回读的状态不对: %+v", got)
	}
	if got.Account != "CHRIS" || got.AmountText != "10" || got.TokenType != "USDT" {
		t.Fatalf("回读丢字段: %+v", got)
	}
	if got.RetryAllowed() {
		t.Fatal("不确定态不该允许重试")
	}
}

// 今日累计**按账号各算**，且覆盖 pending/succeeded/unknown 三态。
// 这是限额判定的输入，算错了直接导致上限失效。
func TestPgStoreSpentTodayIsPerAccountAndCoversUnknown(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	seed := []struct {
		key, account string
		state        OperationState
		amount       string
		started      time.Time
	}{
		{"a1", "CHRIS", StateSucceeded, "10", issueNow},
		{"a2", "CHRIS", StateUnknown, "5", issueNow},                         // 可能真花了，要算
		{"a3", "CHRIS", StatePending, "1", issueNow},                         // 在途，也要算
		{"a4", "CHRIS", StateFailed, "99", issueNow},                         // 确定没花，不算
		{"a5", "LINFENG", StateSucceeded, "7", issueNow},                     // 别的账号，不算
		{"a6", "CHRIS", StateSucceeded, "50", issueNow.Add(-48 * time.Hour)}, // 前天，不算
	}
	for _, s := range seed {
		op := Operation{
			IdempotencyKey: s.key, Account: s.account, Kind: OpIssue, State: StatePending,
			AmountText: s.amount, TokenType: "USDT", StartedAt: s.started,
		}
		if _, _, err := store.BeginOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		op.State = s.state
		if err := store.ResolveOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}

	chris, err := store.SpentToday(ctx, "CHRIS", OpIssue, issueNow)
	if err != nil {
		t.Fatal(err)
	}
	// 10 + 5 + 1 = 16
	if chris != "16.000000" {
		t.Fatalf("CHRIS 今日累计 = %q, want 16.000000", chris)
	}

	linfeng, err := store.SpentToday(ctx, "LINFENG", OpIssue, issueNow)
	if err != nil {
		t.Fatal(err)
	}
	if linfeng != "7.000000" {
		t.Fatalf("LINFENG 今日累计 = %q, want 7.000000", linfeng)
	}
}

func TestPgStoreUpsertCardAndList(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	card := infini.Card{
		ID: "card_1", Mask: "441357******7843", HolderName: "ops@example.com",
		Alias: "xm-abc", Status: "active", Currency: "USD", BalanceMinor: 49,
		UserID: "u1", CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: "ops-team"}); err != nil {
		t.Fatal(err)
	}

	// 同一张卡再来一次（同步作业的常态），owner_ref 传空必须保留原值
	card.BalanceMinor = 100
	card.Status = "frozen"
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: ""}); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListCards(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应只有 1 张卡（第二次是更新不是插入）, got %d", len(list))
	}
	got := list[0]
	if got.BalanceMinor != 100 || got.Status != "frozen" {
		t.Fatalf("更新没生效: %+v", got)
	}
	if got.OwnerRef != "ops-team" {
		t.Fatalf("owner_ref 传空时必须保留原值, got %q", got.OwnerRef)
	}
	if got.Account != "CHRIS" {
		t.Fatalf("Account = %q", got.Account)
	}
}

// 两个账号的同名卡 id 是两张不同的卡：唯一键带账号，不能互相覆盖。
func TestPgStoreSameCardIDInTwoAccountsAreDistinct(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	card := infini.Card{
		ID: "same_id", Mask: "400000******0000", Status: "active",
		Currency: "USD", BalanceMinor: 1, CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: "a"}); err != nil {
		t.Fatal(err)
	}
	card.BalanceMinor = 2
	if err := store.UpsertCard(ctx, "LINFENG", card, CardAttribution{OwnerRef: "b"}); err != nil {
		t.Fatal(err)
	}

	all, err := store.ListCards(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("两个账号的同名卡 id 应是两行, got %d", len(all))
	}

	onlyChris, err := store.ListCards(ctx, "CHRIS", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyChris) != 1 || onlyChris[0].Account != "CHRIS" {
		t.Fatalf("按账号过滤没生效: %+v", onlyChris)
	}
}

func TestPgStoreTrackedCardsCarriesAccount(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	for _, a := range []string{"CHRIS", "LINFENG"} {
		card := infini.Card{
			ID: "card_" + a, Status: "active", Currency: "USD",
			CreatedAt: issueNow, UpdatedAt: issueNow,
		}
		if err := store.UpsertCard(ctx, a, card, CardAttribution{}); err != nil {
			t.Fatal(err)
		}
	}

	refs, err := store.TrackedCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("应返回 2 张卡, got %d", len(refs))
	}
	for _, ref := range refs {
		if ref.Account == "" {
			t.Fatal("每条都必须带账号——同步作业要拿它决定用哪个客户端")
		}
		if ref.CardID != "card_"+ref.Account {
			t.Fatalf("账号与卡对不上: %+v", ref)
		}
	}
}

// 流水去重键是派生的（上游不给交易 id），同一笔重复同步不该造重复行。
func TestPgStoreUpsertTransactionsDeduplicates(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	tx := infini.CardTransaction{
		CardID: "card_1", Type: "purchase", AmountMinor: 350, FeeMinor: 10,
		Currency: "USD", Status: "settled", Merchant: "OPENAI",
		OccurredAt: "2026-09-02T08:00:00Z",
	}

	for i := 0; i < 3; i++ {
		if err := store.UpsertTransactions(ctx, "CHRIS", "card_1", []infini.CardTransaction{tx}); err != nil {
			t.Fatal(err)
		}
	}

	list, err := store.ListTransactions(ctx, "CHRIS", "card_1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("同一笔交易同步三次应只有 1 行, got %d——流水金额会翻倍", len(list))
	}
	if list[0].AmountMinor != 350 || list[0].Merchant != "OPENAI" {
		t.Fatalf("回读的流水不对: %+v", list[0])
	}
	if list[0].OccurredAt.IsZero() {
		t.Fatal("occurred_at 应被解析并落库")
	}
}

// 两个账号各自的流水互不干扰，且按账号查只拿自己的。
func TestPgStoreTransactionsAreScopedToAccount(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	tx := infini.CardTransaction{
		CardID: "card_1", Type: "purchase", AmountMinor: 100, Currency: "USD",
		Merchant: "M", OccurredAt: "2026-09-02T08:00:00Z",
	}
	if err := store.UpsertTransactions(ctx, "CHRIS", "card_1", []infini.CardTransaction{tx}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTransactions(ctx, "LINFENG", "card_1", []infini.CardTransaction{tx}); err != nil {
		t.Fatal(err)
	}

	chris, err := store.ListTransactions(ctx, "CHRIS", "card_1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(chris) != 1 {
		t.Fatalf("CHRIS 应只看到自己的 1 条, got %d", len(chris))
	}
}

func TestPgStoreUnresolvedOperationsOnlyReturnsOpenOnes(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	states := map[string]OperationState{
		"o1": StatePending,
		"o2": StateUnknown,
		"o3": StateSucceeded,
		"o4": StateFailed,
	}
	for key, state := range states {
		op := Operation{
			IdempotencyKey: key, Account: "CHRIS", Kind: OpIssue, State: StatePending,
			AmountText: "1", TokenType: "USDT", StartedAt: issueNow,
		}
		if _, _, err := store.BeginOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		op.State = state
		if err := store.ResolveOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}

	open, err := store.UnresolvedOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("只有 pending 与 unknown 该被返回, got %d 条", len(open))
	}
	for _, op := range open {
		if op.State != StatePending && op.State != StateUnknown {
			t.Fatalf("不该返回 %q", op.State)
		}
	}
}

// 卡面明文的一次性拉取，SQL 层的两个条件都要真库验证：
// pan_fetched_at IS NULL（只拉一次）与 status='active'（不白拉）。
func TestPgStoreCardsMissingSecretsRespectsBothConditions(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	seed := []struct {
		id, status string
	}{
		{"active_no_pan", "active"},  // 该拉
		{"init_no_pan", "init"},      // 还没激活，拉不到
		{"active_has_pan", "active"}, // 已经拉过，不该再拉
	}
	for _, c := range seed {
		card := infini.Card{
			ID: c.id, Status: c.status, Currency: "USD",
			CreatedAt: issueNow, UpdatedAt: issueNow,
		}
		if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: ""}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.StoreCardSecrets(ctx, "CHRIS", "active_has_pan", infini.RevealedCard{
		Number: "4000000000000000", CVV: "111", ExpiryMMYY: "1230",
	}); err != nil {
		t.Fatal(err)
	}

	refs, err := store.CardsMissingSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].CardID != "active_no_pan" {
		t.Fatalf("只有已激活且没拉过的那张该被返回, got %+v", refs)
	}
}

func TestPgStoreStoreCardSecretsRoundTrips(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	card := infini.Card{
		ID: "card_1", Mask: "441357******7843", Status: "active",
		Currency: "USD", CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: ""}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreCardSecrets(ctx, "CHRIS", "card_1", infini.RevealedCard{
		Number: "4413571234567843", CVV: "123", ExpiryMMYY: "1229",
	}); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListCards(ctx, "CHRIS", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 张卡, got %d", len(list))
	}
	got := list[0]
	if got.PAN != "4413571234567843" || got.CVV != "123" || got.ExpiryMMYY != "1229" {
		t.Fatalf("明文没回读出来: %+v", got)
	}
	// 掩码仍然保留：不是所有调用方都有权限看明文
	if got.Mask != "441357******7843" {
		t.Fatalf("掩码不该被覆盖, got %q", got.Mask)
	}
}

// 同步作业每轮都会 UpsertCard 刷新状态——那一步**不能把已拉到的明文冲掉**。
func TestPgStoreUpsertCardDoesNotClearStoredSecrets(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	card := infini.Card{
		ID: "card_1", Status: "active", Currency: "USD",
		CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: ""}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreCardSecrets(ctx, "CHRIS", "card_1", infini.RevealedCard{
		Number: "4413571234567843", CVV: "123", ExpiryMMYY: "1229",
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟下一轮同步：状态变了，再 upsert 一次
	card.Status = "frozen"
	card.BalanceMinor = 500
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{OwnerRef: ""}); err != nil {
		t.Fatal(err)
	}

	list, _ := store.ListCards(ctx, "CHRIS", "")
	if list[0].PAN != "4413571234567843" {
		t.Fatal("刷新卡状态把明文冲掉了——那会让同步作业每轮重新拉一次 reveal")
	}
	if list[0].Status != "frozen" {
		t.Fatal("状态该更新的还是要更新")
	}
}

// 用途登记要能完整往返，尤其是日期列：它在库里是 date 而不是文本，
// 空值必须回成空串而不是 0001-01-01——那种值会被前端当成「已登记且早已过期」。
func TestPgStoreSetCardUsageRoundTrips(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	seedCard(t, store, "CHRIS", "card_1", "ops@example.com")

	usage := CardUsage{
		Account: "CHRIS", CardID: "card_1",
		BoundAccount: "chris@example.com", BoundAccountKind: "email",
		ServiceName: "OpenAI Plus", NextRenewalOn: "2026-10-15", Note: "团队共用",
	}
	if err := store.SetCardUsage(ctx, usage); err != nil {
		t.Fatal(err)
	}

	got := onlyCard(t, store, "CHRIS")
	if got.BoundAccount != "chris@example.com" || got.BoundAccountKind != "email" {
		t.Fatalf("绑定账号没落库: %+v", got)
	}
	if got.ServiceName != "OpenAI Plus" || got.UsageNote != "团队共用" {
		t.Fatalf("服务/备注没落库: %+v", got)
	}
	if got.NextRenewalOn != "2026-10-15" {
		t.Fatalf("续费日期 = %q, 想要 2026-10-15", got.NextRenewalOn)
	}

	// 清空续费日期：date 列要回到 NULL，读出来是空串。
	usage.NextRenewalOn = ""
	if err := store.SetCardUsage(ctx, usage); err != nil {
		t.Fatal(err)
	}
	if got := onlyCard(t, store, "CHRIS"); got.NextRenewalOn != "" {
		t.Fatalf("清空续费日期后 = %q, 想要空串", got.NextRenewalOn)
	}
}

// 登记不存在的卡必须报错。
//
// UPDATE 匹配不到行在 SQL 里不是错误，会静默成功——那样管理端会显示
// 「已保存」，而运营以为设好的续费提醒根本不存在。
func TestPgStoreSetCardUsageOnMissingCardFails(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	err := store.SetCardUsage(ctx, CardUsage{
		Account: "CHRIS", CardID: "card_nope", ServiceName: "OpenAI Plus",
	})
	if err == nil {
		t.Fatal("登记不存在的卡必须报错，不能静默成功")
	}
}

// 登记按账号隔离：两个账号里同名的卡 id 是两张卡。
func TestPgStoreSetCardUsageIsScopedToAccount(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	seedCard(t, store, "CHRIS", "card_1", "a@example.com")
	seedCard(t, store, "LINFENG", "card_1", "b@example.com")

	if err := store.SetCardUsage(ctx, CardUsage{
		Account: "CHRIS", CardID: "card_1", ServiceName: "OpenAI Plus",
	}); err != nil {
		t.Fatal(err)
	}

	if got := onlyCard(t, store, "CHRIS"); got.ServiceName != "OpenAI Plus" {
		t.Fatalf("CHRIS 的登记没生效: %+v", got)
	}
	if got := onlyCard(t, store, "LINFENG"); got.ServiceName != "" {
		t.Fatalf("串账号了：LINFENG 的卡被写成 %q", got.ServiceName)
	}
}

// 同步作业刷新卡片状态时不能冲掉用途登记。
//
// 与「不能冲掉已存的卡面明文」同一类错误，但更容易发生：同步每 5 分钟跑一次，
// 一旦 UPSERT 把这几列一起覆盖，运营刚填的登记最多活五分钟。
func TestPgStoreUpsertCardDoesNotClearUsage(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	seedCard(t, store, "CHRIS", "card_1", "ops@example.com")
	if err := store.SetCardUsage(ctx, CardUsage{
		Account: "CHRIS", CardID: "card_1",
		BoundAccount: "chris@example.com", BoundAccountKind: "email",
		ServiceName: "OpenAI Plus", NextRenewalOn: "2026-10-15", Note: "团队共用",
	}); err != nil {
		t.Fatal(err)
	}

	// 同步作业的常态：只带上游知道的字段，登记列一个都不带。
	refreshed := infini.Card{
		ID: "card_1", Mask: "441357******7843", HolderName: "ops@example.com",
		Alias: "xm-abc", Status: "active", Currency: "USD", BalanceMinor: 900,
		UserID: "u1", CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(ctx, "CHRIS", refreshed, CardAttribution{}); err != nil {
		t.Fatal(err)
	}

	got := onlyCard(t, store, "CHRIS")
	if got.BalanceMinor != 900 {
		t.Fatalf("余额没刷新: %+v", got)
	}
	if got.ServiceName != "OpenAI Plus" || got.NextRenewalOn != "2026-10-15" {
		t.Fatalf("同步冲掉了用途登记: %+v", got)
	}
	if got.BoundAccount != "chris@example.com" || got.UsageNote != "团队共用" {
		t.Fatalf("同步冲掉了绑定账号/备注: %+v", got)
	}
}

// 成员邮箱下拉的候选：去重、排序、跳过空值、按环境隔离。
func TestPgStoreKnownMemberEmails(t *testing.T) {
	store, pool := pgStore(t)
	ctx := context.Background()

	seedCard(t, store, "CHRIS", "card_1", "zoe@example.com")
	seedCard(t, store, "CHRIS", "card_2", "adam@example.com")
	// 同一个人开了两张卡：下拉里只能出现一次。
	seedCard(t, store, "LINFENG", "card_3", "adam@example.com")
	// 没记邮箱的老卡（同步先于开卡登记时会有）：不能变成一个空选项。
	seedCard(t, store, "CHRIS", "card_4", "")

	got, err := store.KnownMemberEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"adam@example.com", "zoe@example.com"}
	if len(got) != len(want) {
		t.Fatalf("候选 = %v, 想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("候选 = %v, 想要 %v", got, want)
		}
	}

	// 另一个环境的卡不能出现在这里：生产的成员邮箱不该漏进开发环境的下拉。
	other := NewPgStore(pool, "staging", func() time.Time { return issueNow })
	seedCard(t, other, "CHRIS", "card_5", "leak@example.com")
	got, err = store.KnownMemberEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e == "leak@example.com" {
			t.Fatalf("串环境了: %v", got)
		}
	}
}

// seedCard 落一张最小可用的卡投影，供登记类用例做前置。
func seedCard(t *testing.T, store *PgStore, account, cardID, userEmail string) {
	t.Helper()
	card := infini.Card{
		ID: cardID, Mask: "441357******7843", HolderName: "ops@example.com",
		Alias: "xm-" + cardID, Status: "active", Currency: "USD", BalanceMinor: 49,
		UserID: "u1", CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(context.Background(), account, card,
		CardAttribution{UserEmail: userEmail}); err != nil {
		t.Fatal(err)
	}
}

// onlyCard 取某账号下唯一的一张卡；不唯一就是用例的前置错了。
func onlyCard(t *testing.T, store *PgStore, account string) CardView {
	t.Helper()
	list, err := store.ListCards(context.Background(), account, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("账号 %s 下应有 1 张卡, got %d", account, len(list))
	}
	return list[0]
}

// 回调去重：第一次是新事件，重复投递被识别为已收到。
func TestPgStoreWebhookEventDeduplicates(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	ev := WebhookEvent{
		ID: "evt-1", Type: WebhookEventCardStatusChange,
		CardID: "card_1", OccurredAt: issueNow,
	}

	first, err := store.RecordWebhookEvent(ctx, "CHRIS", ev)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Fresh {
		t.Fatal("第一次投递应是新事件")
	}
	if first.AlreadyProcessed {
		t.Fatal("第一次投递不该是已处理")
	}

	second, err := store.RecordWebhookEvent(ctx, "CHRIS", ev)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fresh {
		t.Fatal("重复投递不该被当成新事件")
	}
}

// **最要紧的一条**：处理失败后的重投必须被再处理一次。
//
// Infini 对非 200 最多重试 8 次。如果「收到过」就直接放行，那么第一次处理
// 失败之后，后面 7 次重试全部被当成重复事件丢掉——一次真实的状态变更就此
// 消失，而且我们还每次都回 200 让上游以为成功了。
func TestPgStoreWebhookEventRetryIsReprocessedUntilMarkedDone(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()
	ev := WebhookEvent{ID: "evt-retry", Type: WebhookEventCardTransaction, CardID: "card_1"}

	if _, err := store.RecordWebhookEvent(ctx, "CHRIS", ev); err != nil {
		t.Fatal(err)
	}
	// 处理失败：不标记完成。
	again, err := store.RecordWebhookEvent(ctx, "CHRIS", ev)
	if err != nil {
		t.Fatal(err)
	}
	if again.AlreadyProcessed {
		t.Fatal("没标记完成之前，重投必须仍要处理")
	}

	if err := store.MarkWebhookEventProcessed(ctx, "CHRIS", ev.ID); err != nil {
		t.Fatal(err)
	}
	done, err := store.RecordWebhookEvent(ctx, "CHRIS", ev)
	if err != nil {
		t.Fatal(err)
	}
	if !done.AlreadyProcessed {
		t.Fatal("标记完成之后，重投应识别为已处理")
	}
}

// 两个账号各自的事件互不影响：事件 id 是上游生成的，不假设跨账号唯一。
func TestPgStoreWebhookEventIsScopedToAccount(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()
	ev := WebhookEvent{ID: "evt-same", Type: WebhookEventCardStatusChange, CardID: "card_1"}

	if _, err := store.RecordWebhookEvent(ctx, "CHRIS", ev); err != nil {
		t.Fatal(err)
	}
	other, err := store.RecordWebhookEvent(ctx, "LINFENG", ev)
	if err != nil {
		t.Fatal(err)
	}
	if !other.Fresh {
		t.Fatal("另一个账号的同 id 事件应是新事件")
	}
}

// 开卡费只在开卡那一刻知道，同步作业每 5 分钟一轮传空，绝不能把它冲掉。
// 与 owner_ref / user_email 同一条纪律。
func TestPgStoreUpsertCardKeepsIssueFeeOnSync(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	card := infini.Card{
		ID: "card_fee", Mask: "441357******7843", HolderName: "ops@example.com",
		Alias: "xm-fee", Status: "active", Currency: "USD", BalanceMinor: 100,
		UserID: "u1", CreatedAt: issueNow, UpdatedAt: issueNow,
	}
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{
		OwnerRef: "ops", IssueFee: "1", IssuePayAmount: "2",
	}); err != nil {
		t.Fatal(err)
	}

	// 同步作业的常态：只带上游知道的字段。
	card.BalanceMinor = 50
	if err := store.UpsertCard(ctx, "CHRIS", card, CardAttribution{}); err != nil {
		t.Fatal(err)
	}

	got := onlyCard(t, store, "CHRIS")
	if got.BalanceMinor != 50 {
		t.Fatalf("余额没刷新: %+v", got)
	}
	if got.IssueFee != "1" || got.IssuePayAmount != "2" {
		t.Fatalf("同步冲掉了开卡费: %+v", got)
	}
}

// 流水的外币原始金额与结算时间要能往返。
//
// 一张 USD 卡在欧元商户消费，amount 是折成 USD 的，transaction_amount 才是
// EUR 原值——没有它看不出跨境消费与汇率加价。settled_at 分开「已授权」与
// 「已结算」：授权可以被撤销，只有结算了的才是真正扣掉的钱。
func TestPgStoreTransactionsCarryForeignAmountAndSettlement(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()
	seedCard(t, store, "CHRIS", "card_1", "ops@example.com")

	txs := []infini.CardTransaction{{
		CardID: "card_1", Type: "Consume", AmountMinor: 1050, FeeMinor: 0,
		Status: "Completed", Currency: "USD", Merchant: "Amazon DE",
		OccurredAt:          issueNow.UTC().Format(time.RFC3339),
		SettledAt:           issueNow.Add(time.Hour).UTC().Format(time.RFC3339),
		TransactionAmount:   "9.80",
		TransactionCurrency: "EUR",
	}}
	if err := store.UpsertTransactions(ctx, "CHRIS", "card_1", txs); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListTransactions(ctx, "CHRIS", "card_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条流水, got %d", len(list))
	}
	got := list[0]
	if got.TransactionAmount != "9.80" || got.TransactionCurrency != "EUR" {
		t.Fatalf("外币原值没往返: %+v", got)
	}
	if got.SettledAt.IsZero() {
		t.Fatalf("结算时间没往返: %+v", got)
	}
}

// 未结算的流水：settled_at 为空要存 NULL，读出来是零值而不是 1970 年。
func TestPgStoreTransactionsLeaveUnsettledNull(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()
	seedCard(t, store, "CHRIS", "card_1", "ops@example.com")

	if err := store.UpsertTransactions(ctx, "CHRIS", "card_1", []infini.CardTransaction{{
		CardID: "card_1", Type: "Consume", AmountMinor: 100, Currency: "USD",
		Status: "authorized", OccurredAt: issueNow.UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListTransactions(ctx, "CHRIS", "card_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !list[0].SettledAt.IsZero() {
		t.Fatalf("未结算的流水不该有结算时间: %+v", list[0])
	}
}
