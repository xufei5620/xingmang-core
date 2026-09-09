package cards

import (
	"context"
	"errors"
	"os"
	"strings"
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
		"cards.card_challenge",
		"cards.withdraw_request", "cards.withdraw_address", "cards.withdraw_limit",
		// XM-CARD-VISIBILITY：按账号暂停同步的开关（迁移 000055）。
		"cards.account_sync_pause",
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

// 同一 request_id 只落一次，第二次拿回既有记录。
//
// 这是提现幂等的库侧一半：上游对 request_id 也有真幂等，但我们不靠它——
// 同键重投时连上游都不打，少一次调用就少一次出错的机会。
func TestPgStoreBeginWithdrawIsIdempotent(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.RegisterWithdrawAddress(ctx, WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TColdWallet", Label: "冷钱包",
	}, "ops@example.com"); err != nil {
		t.Fatal(err)
	}

	rec := WithdrawRecord{
		RequestID: "w-1", Account: testAccount, Chain: "TRON", TokenType: "USDT",
		Amount: "100", AddressID: "addr-1", Address: "TColdWallet",
		Status: "pending", StartedAt: issueNow, UpdatedAt: issueNow,
	}
	if _, existed, err := store.BeginWithdraw(ctx, rec); err != nil || existed {
		t.Fatalf("首次落台账 existed=%v err=%v", existed, err)
	}

	// 第二次同键：拿回既有记录，且**不覆盖金额**。
	// 覆盖会让台账显示成第二次那笔的金额，而真正转出去的是第一次那笔。
	second := rec
	second.Amount = "9999"
	stored, existed, err := store.BeginWithdraw(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("同键重投必须被识别为已存在")
	}
	if stored.Amount != "100" {
		t.Fatalf("既有记录的金额被覆盖了: %q", stored.Amount)
	}
}

// 今日累计把**未收敛的也算进去**。
//
// pending 的那笔可能真的已经转出去了。当它没转过会让单日上限在最需要
// 生效的时刻失效——恰恰是上游正在慢、人在反复点的时候。
func TestPgStoreWithdrawnTodayCountsUnresolved(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		id     string
		amount string
		status string
	}{
		{"w-done", "100", "completed"},
		{"w-open", "50", "pending"},
		{"w-fail", "999", "failed"},
	} {
		if _, _, err := store.BeginWithdraw(ctx, WithdrawRecord{
			RequestID: tc.id, Account: testAccount, Chain: "TRON", TokenType: "USDT",
			Amount: tc.amount, AddressID: "addr-1", Address: "TColdWallet",
			Status: tc.status, StartedAt: issueNow, UpdatedAt: issueNow,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.WithdrawnToday(ctx, testAccount, issueNow)
	if err != nil {
		t.Fatal(err)
	}
	// 100（成功）+ 50（未收敛）= 150。失败那笔 999 不算：
	// 上游明确拒绝时钱确实没出去，把它算进去会平白吃掉当天的额度。
	if got != "150" {
		t.Fatalf("今日累计 = %q, want 150（成功 + 未收敛，不含确定失败）", got)
	}
}

// 账号之间的额度是分开的：A 账号的提现不该吃掉 B 账号的当日额度。
func TestPgStoreWithdrawnTodayIsPerAccount(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if _, _, err := store.BeginWithdraw(ctx, WithdrawRecord{
		RequestID: "w-other", Account: "OTHER", Chain: "TRON", TokenType: "USDT",
		Amount: "500", AddressID: "addr-x", Address: "TOther",
		Status: "completed", StartedAt: issueNow, UpdatedAt: issueNow,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.WithdrawnToday(ctx, testAccount, issueNow)
	if err != nil {
		t.Fatal(err)
	}
	if got != "0" {
		t.Fatalf("另一个账号的提现不该算进来, got %q", got)
	}
}

// 地址按 id 取回，且 UpdateWithdraw 只覆盖非空字段。
//
// 终态回填只带回 tx_hash 与手续费，不带金额与地址；用 COALESCE 之外的写法
// 会把那几列清空，而「这笔钱转到哪儿」必须永远查得到。
func TestPgStoreUpdateWithdrawKeepsSnapshot(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.RegisterWithdrawAddress(ctx, WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TColdWallet",
	}, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginWithdraw(ctx, WithdrawRecord{
		RequestID: "w-1", Account: testAccount, Chain: "TRON", TokenType: "USDT",
		Amount: "100", AddressID: "addr-1", Address: "TColdWallet",
		Status: "pending", StartedAt: issueNow, UpdatedAt: issueNow,
	}); err != nil {
		t.Fatal(err)
	}

	// 终态回填：只给状态与链上信息。
	if err := store.UpdateWithdraw(ctx, WithdrawRecord{
		RequestID: "w-1", Status: "completed", TxHash: "0xabc",
		ActualAmount: "99.5", GasFee: "0.5", GasFeeCurrency: "USDT",
		UpdatedAt: issueNow,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.RecentWithdrawals(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应有一条, got %d", len(rows))
	}
	got := rows[0]
	if got.Status != "completed" || got.TxHash != "0xabc" || got.GasFee != "0.5" {
		t.Fatalf("终态未回填: %+v", got)
	}
	if got.Address != "TColdWallet" || got.Amount != "100" || got.Chain != "TRON" {
		t.Fatalf("地址/金额/链的快照被回填清空了: %+v", got)
	}
}

// 地址清单能读回，且未登记的 id 取不到。
func TestPgStoreAllowedAddressRejectsUnknown(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.RegisterWithdrawAddress(ctx, WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TColdWallet", Label: "冷钱包",
	}, "ops@example.com"); err != nil {
		t.Fatal(err)
	}

	got, err := store.AllowedAddress(ctx, "addr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "TColdWallet" || got.Chain != "TRON" || got.Label != "冷钱包" {
		t.Fatalf("地址读回不完整: %+v", got)
	}

	if _, err := store.AllowedAddress(ctx, "从没登记过"); err == nil {
		t.Fatal("未登记的地址必须取不到——这是白名单的全部意义")
	}

	list, err := store.ListWithdrawAddresses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "addr-1" {
		t.Fatalf("清单不对: %+v", list)
	}
}

// 没设过额度的账号读回零值而**不是报错**。
//
// 报错会让「还没人给这个账号设过上限」和「库挂了」长得一样，
// 而这两件事的处置完全不同：前者去后台设一个，后者去看数据库。
func TestPgStoreWithdrawLimitsUnsetIsNotAnError(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	got, err := store.WithdrawLimitsFor(ctx, testAccount)
	if err != nil {
		t.Fatalf("没设过额度不该报错: %v", err)
	}
	if got.PerOperation != "" || got.PerDay != "" {
		t.Fatalf("没设过应读回零值, got %+v", got)
	}
	// 零值必须被判成「未配置」，也就是不能提现。
	if err := got.CheckWithdraw("1", "0"); !errors.Is(err, ErrLimitsUnconfigured) {
		t.Fatalf("零值额度必须 fail closed, got %v", err)
	}

	// 清单里也不该凭空冒出一行。
	list, err := store.ListWithdrawLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("没设过的账号不该出现在清单里, got %+v", list)
	}
}

// 额度写入即生效，重复写是整体替换。
func TestPgStoreSetWithdrawLimitsReplaces(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.SetWithdrawLimits(ctx, testAccount, "500", "2000", "xufei"); err != nil {
		t.Fatal(err)
	}
	got, err := store.WithdrawLimitsFor(ctx, testAccount)
	if err != nil {
		t.Fatal(err)
	}
	if got.PerOperation != "500" || got.PerDay != "2000" {
		t.Fatalf("额度读回不对: %+v", got)
	}

	// 再写一次：整体替换，不是叠加。
	if err := store.SetWithdrawLimits(ctx, testAccount, "800", "3000", "someone-else"); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListWithdrawLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("同一账号只该有一行, got %d", len(list))
	}
	if list[0].PerOperation != "800" || list[0].PerDay != "3000" {
		t.Fatalf("额度未被替换: %+v", list[0])
	}
	// 「谁改的」跟着一起更新——旧的那个人不该继续背这个上限。
	if list[0].UpdatedBy != "someone-else" {
		t.Fatalf("改动者应更新, got %q", list[0].UpdatedBy)
	}
}

// 额度按账号隔离：给 A 设的上限不该影响 B。
func TestPgStoreWithdrawLimitsArePerAccount(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.SetWithdrawLimits(ctx, testAccount, "500", "2000", "xufei"); err != nil {
		t.Fatal(err)
	}
	got, err := store.WithdrawLimitsFor(ctx, "OTHER")
	if err != nil {
		t.Fatal(err)
	}
	if got.PerOperation != "" {
		t.Fatalf("另一个账号不该拿到这份额度, got %+v", got)
	}
}

// 停用之后 AllowedAddress 仍读得到那条登记，但 Enabled 是假。
//
// 读得到是刻意的：领域层要能分辨「没登记」和「登记了但停用了」，
// 两者的下一步动作不同（去登记 / 去启用）。在 SQL 里过滤掉会把这两种
// 情形压成同一个错误。
func TestPgStoreDisabledAddressIsStillReadable(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.RegisterWithdrawAddress(ctx, WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TColdWallet", Label: "冷钱包",
	}, "xufei"); err != nil {
		t.Fatal(err)
	}
	// 新登记的必须是启用的：登记它的动作本身就是「我要用它」。
	got, err := store.AllowedAddress(ctx, "addr-1")
	if err != nil || !got.Enabled {
		t.Fatalf("新登记的地址应是启用的: %+v (%v)", got, err)
	}

	if err := store.SetWithdrawAddressEnabled(ctx, "addr-1", false, "someone"); err != nil {
		t.Fatal(err)
	}
	got, err = store.AllowedAddress(ctx, "addr-1")
	if err != nil {
		t.Fatalf("停用后仍应读得到（要能区分未登记与已停用）: %v", err)
	}
	if got.Enabled {
		t.Fatal("停用未生效")
	}
	// 清单里也留着，供查看与重新启用。
	list, err := store.ListWithdrawAddresses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("停用的地址应留在清单里且标记为停用: %+v", list)
	}
}

// 改一条不存在的登记要报错，不能静默成功。
//
// 静默会让页面显示「已停用」，而那条地址其实压根不在这个环境里。
func TestPgStoreSetEnabledOnUnknownAddressFails(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	err := store.SetWithdrawAddressEnabled(ctx, "从没登记过", false, "xufei")
	if !errors.Is(err, ErrAddressNotAllowed) {
		t.Fatalf("改一条不存在的登记必须报错, got %v", err)
	}
}

// **一个 id 不能被改指到另一条地址。**
//
// 这是白名单最要害的一条：若允许改指，以后所有选中「冷钱包」的提现都会
// 悄悄转去新地方，而页面上标签一个字都没变。库层面靠主键挡住，
// 这里断言它挡住了、而且报出来的是人话而不是裸的约束错。
func TestPgStoreAddressIDCannotBeRepointed(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	if err := store.RegisterWithdrawAddress(ctx, WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TOriginal", Label: "冷钱包",
	}, "xufei"); err != nil {
		t.Fatal(err)
	}

	err := store.RegisterWithdrawAddress(ctx, WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TAttackerAddress", Label: "冷钱包",
	}, "xufei")
	if err == nil {
		t.Fatal("同一个 id 改指到另一条地址必须被拒绝")
	}
	if !strings.Contains(err.Error(), "不能改指") {
		t.Fatalf("错误要说清是改指被拒，而不是一句裸的约束错: %v", err)
	}

	// 原地址纹丝不动。
	got, err := store.AllowedAddress(ctx, "addr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "TOriginal" {
		t.Fatalf("原地址被改掉了: %q", got.Address)
	}
}

// 重新登记一条已停用的地址会把它启用。
//
// 否则运营会在一条停用的地址上反复点「登记」，每次都显示成功，
// 却仍然提不了现。
func TestPgStoreReRegisterReEnables(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	a := WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TColdWallet", Label: "冷钱包",
	}
	if err := store.RegisterWithdrawAddress(ctx, a, "xufei"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWithdrawAddressEnabled(ctx, "addr-1", false, "xufei"); err != nil {
		t.Fatal(err)
	}

	// 同一条地址再登记一次（id 相同，走 ON CONFLICT 分支）。
	a.Label = "冷钱包（重新启用）"
	if err := store.RegisterWithdrawAddress(ctx, a, "xufei"); err != nil {
		t.Fatal(err)
	}
	got, err := store.AllowedAddress(ctx, "addr-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("重新登记应把停用的地址启用")
	}
	if got.Label != "冷钱包（重新启用）" {
		t.Fatalf("标签应更新, got %q", got.Label)
	}
}

// 跨卡流水：把全部卡的交易按时间倒序排在一起，并带上**卡片名称**。
//
// 这是 Infini「交易记录」那个页签的数据源。卡片名称必须在这里 join 出来：
// 一张流水表里最要紧的定位信息就是「这笔是哪张卡刷的」，让前端拿着
// card_id 再去卡片列表里配对，等于把一次 join 挪到浏览器里做——而那张列表
// 可能因为筛选根本没加载全。
func TestPgStoreRecentTransactionsAcrossCards(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	// 两个账号各一张卡，各一笔流水。
	for _, c := range []struct {
		account, cardID, alias string
	}{
		{testAccount, "card-a", "Two.V"},
		{"OTHER", "card-b", "flower"},
	} {
		if err := store.UpsertCard(ctx, c.account, infini.Card{
			ID: c.cardID, Alias: c.alias, Status: "active", Currency: "USD",
		}, CardAttribution{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertTransactions(ctx, testAccount, "card-a", []infini.CardTransaction{{
		Type: "Consume", AmountMinor: -2000, Currency: "USD", Status: "Completed",
		Merchant: "OPENAI", OccurredAt: issueNow.Add(-time.Hour).Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTransactions(ctx, "OTHER", "card-b", []infini.CardTransaction{{
		Type: "Consume", AmountMinor: -500, Currency: "USD", Status: "Completed",
		Merchant: "ANTHROPIC", OccurredAt: issueNow.Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.RecentTransactions(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("两个账号的流水都该在, got %d", len(rows))
	}
	// 新的在前：一张流水表默认要回答「刚刚发生了什么」。
	if rows[0].Merchant != "ANTHROPIC" {
		t.Fatalf("应按时间倒序, got %+v", rows[0])
	}
	// 卡片名称 join 出来了。
	if rows[0].CardAlias != "flower" || rows[1].CardAlias != "Two.V" {
		t.Fatalf("卡片名称没带上: %+v / %+v", rows[0], rows[1])
	}
	// 账号也要带：两个账号可以有同名卡 id，只给 card_id 定位不了。
	if rows[0].Account != "OTHER" {
		t.Fatalf("账号没带上: %+v", rows[0])
	}
}

// 统计必须在**服务端**按整表算，不能拿前端那份 limit 截断的列表求和。
//
// 截断求和的症状最坏：它不会报错，只会给出一个比真实值小的数，
// 而「这个月花了多少」看起来完全正常——没人会去怀疑一个像模像样的数字。
func TestPgStoreTransactionStatsAggregatesWholeTable(t *testing.T) {
	store, pool := pgStore(t)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	// 三笔已完成消费、一笔授权中、一笔充值、一笔另一账号的消费，
	// 外加一笔没有发生时间的——它不该被窗口统计悄悄吃掉。
	seed := []struct {
		account, card, dedupe, txType, currency, status, merchant string
		amount, fee                                               int64
		occurred                                                  *time.Time
	}{
		{"CHRIS", "c1", "d1", "consume", "USD", "completed", "OPENAI", -1795, 10, ptrTime(base)},
		{"CHRIS", "c1", "d2", "consume", "USD", "completed", "OPENAI", -9359, 0, ptrTime(base.Add(time.Hour))},
		{"CHRIS", "c2", "d3", "consume", "USD", "completed", "GOOGLE", -195, 5, ptrTime(base.Add(2 * time.Hour))},
		{"CHRIS", "c1", "d4", "consume", "USD", "authorized", "OPENAI", -100, 0, ptrTime(base.Add(3 * time.Hour))},
		{"CHRIS", "c1", "d5", "topup", "USD", "completed", "", 20000, 0, ptrTime(base.Add(4 * time.Hour))},
		{"LINFENG", "c9", "d6", "consume", "USD", "completed", "OPENAI", -500, 0, ptrTime(base.Add(5 * time.Hour))},
		{"CHRIS", "c1", "d7", "consume", "USD", "completed", "OPENAI", -777, 0, nil},
	}
	for _, r := range seed {
		if _, err := pool.Exec(ctx, `
INSERT INTO cards.infini_card_transaction
 (environment, account, upstream_card_id, dedupe_key, tx_type, amount_minor,
  fee_minor, currency, status, merchant, occurred_at, synced_at, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`,
			testEnvironment, r.account, r.card, r.dedupe, r.txType, r.amount,
			r.fee, r.currency, r.status, r.merchant, r.occurred, base); err != nil {
			t.Fatal(err)
		}
	}

	// 全账号、不限期间。
	all, err := store.TransactionStats(ctx, "", time.Time{}, time.Time{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	got := bucketOf(all, "USD", "consume", "completed")
	if got.Count != 5 || got.AmountMinor != -1795-9359-195-500-777 || got.FeeMinor != 15 {
		t.Fatalf("已完成消费格 = %+v", got)
	}
	if tp := bucketOf(all, "USD", "topup", "completed"); tp.AmountMinor != 20000 {
		t.Fatalf("充值格 = %+v", tp)
	}

	// 按账号筛：另一个账号的那笔必须不在里面。
	chris, err := store.TransactionStats(ctx, "CHRIS", time.Time{}, time.Time{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if c := bucketOf(chris, "USD", "consume", "completed"); c.Count != 4 {
		t.Fatalf("CHRIS 已完成消费笔数 = %d, want 4", c.Count)
	}

	// 期间统计：**没有发生时间的那笔单独报数**，不能被窗口悄悄吃掉。
	win, err := store.TransactionStats(ctx, "", base, base.Add(3*time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	if c := bucketOf(win, "USD", "consume", "completed"); c.Count != 3 {
		t.Fatalf("窗口内已完成消费笔数 = %d, want 3", c.Count)
	}
	if win.UndatedCount != 1 {
		t.Fatalf("无发生时间的笔数 = %d, want 1", win.UndatedCount)
	}
	// 不限期间时它是被算进去的，所以那里不该再报一遍「未计入」。
	if all.UndatedCount != 0 {
		t.Fatalf("不限期间时 UndatedCount 应为 0, got %d", all.UndatedCount)
	}

	// Top 商户按花掉的钱排，OPENAI 应在第一。
	if len(all.Merchants) == 0 || all.Merchants[0].Merchant != "OPENAI" {
		t.Fatalf("top 商户 = %+v", all.Merchants)
	}
	// Top 卡片同理：c1 花得最多。
	if len(all.Cards) == 0 || all.Cards[0].CardID != "c1" {
		t.Fatalf("top 卡片 = %+v", all.Cards)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func bucketOf(s TransactionStats, currency, txType, status string) StatsBucket {
	for _, b := range s.Buckets {
		if b.Currency == currency && b.Type == txType && b.Status == status {
			return b
		}
	}
	return StatsBucket{}
}

// 开卡费要能按**计价代币**分组汇总，而且全程不过 float。
//
// 产品负责人 2026-09-06 定的成本口径是「实际消费 + 手续费」，开卡费是其中
// 一块固定成本（实测 1 USDT/张）。它存在卡片表而不是流水表，所以必须单独
// 聚合——漏掉它，成本就永远少一块，而少掉的那块正比于开了多少卡。
//
// 没记单位的老卡（000039 之前）单独归一组，**不当成 USDT**：
// 1 USDT ≈ 1 USD 是汇率假设不是事实。
func TestPgStoreIssueFeeStatsGroupsByToken(t *testing.T) {
	store, pool := pgStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)

	seed := []struct {
		account, card, fee, token string
		created                   *time.Time
	}{
		{"CHRIS", "k1", "1", "USDT", ptrTime(base)},
		{"CHRIS", "k2", "1.5", "USDT", ptrTime(base.Add(time.Hour))},
		{"CHRIS", "k3", "2", "USDC", ptrTime(base.Add(2 * time.Hour))},
		{"LINFENG", "k4", "1", "USDT", ptrTime(base.Add(3 * time.Hour))},
		// 000039 之前开的卡：有费用没单位。
		{"CHRIS", "k5", "1", "", ptrTime(base.Add(4 * time.Hour))},
		// 费用字段是空的（同步进来的、不是我们开的卡）——不该算进任何一组。
		{"CHRIS", "k6", "", "", ptrTime(base.Add(5 * time.Hour))},
	}
	for _, r := range seed {
		if _, err := pool.Exec(ctx, `
INSERT INTO cards.infini_card
 (environment, account, upstream_card_id, mask, holder_name, card_alias, status,
  currency, balance_minor, last_synced_at, created_at, updated_at,
  upstream_created_at, issue_fee_text, issue_fee_token)
VALUES ($1,$2,$3,'****0000','h','a','active','USD',0,$4,$4,$4,$5,$6,$7)`,
			testEnvironment, r.account, r.card, base, r.created, r.fee, r.token); err != nil {
			t.Fatal(err)
		}
	}

	all, err := store.TransactionStats(ctx, "", time.Time{}, time.Time{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range all.IssueFees {
		got[f.Token] = f.AmountText
	}
	// 精确十进制：1 + 1.5 + 1 = 3.5，不是 3.4999999999999996。
	if got["USDT"] != "3.5" {
		t.Errorf("USDT 开卡费合计 = %q, want \"3.5\"", got["USDT"])
	}
	if got["USDC"] != "2" {
		t.Errorf("USDC 开卡费合计 = %q, want \"2\"", got["USDC"])
	}
	// 没记单位的单独一组，绝不并进 USDT。
	if got[""] != "1" {
		t.Errorf("单位未记录组 = %q, want \"1\"", got[""])
	}
	for _, f := range all.IssueFees {
		if f.Count == 0 {
			t.Errorf("每组都该有张数, got %+v", f)
		}
	}

	// 按账号筛。
	chris, err := store.TransactionStats(ctx, "CHRIS", time.Time{}, time.Time{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range chris.IssueFees {
		if f.Token == "USDT" && f.AmountText != "2.5" {
			t.Errorf("CHRIS 的 USDT 开卡费 = %q, want \"2.5\"", f.AmountText)
		}
	}
}

// 按账号暂停开关的落库往返（XM-CARD-VISIBILITY，迁移 000055）。
//
// SQL 只有对着真实 Postgres 跑过才算数：列名、参数位、ON CONFLICT 目标
// 全是运行时才炸的东西，而这个开关一旦写不进去，运营点了「暂停」之后
// 同步照旧打上游——按钮看起来生效了，实际什么都没发生。
func TestPgStoreAccountSyncPauseRoundTrip(t *testing.T) {
	store, _ := pgStore(t)
	ctx := context.Background()

	paused, err := store.PausedAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paused) != 0 {
		t.Fatalf("初始应为空（行缺席 = 未暂停），实际 %v", paused)
	}

	want := AccountSyncPause{
		Account:  "LINFENG",
		Paused:   true,
		Reason:   "上游拒绝待查 XM-CARD-VISIBILITY",
		PausedBy: "human:ops",
		PausedAt: issueNow,
	}
	if err := store.SetAccountSyncPause(ctx, want); err != nil {
		t.Fatal(err)
	}

	paused, err = store.PausedAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := paused["LINFENG"]
	if !ok {
		t.Fatalf("暂停后应读得到，实际 %v", paused)
	}
	if got.Reason != want.Reason || got.PausedBy != want.PausedBy {
		t.Fatalf("读回的记录 = %+v, want %+v", got, want)
	}
	if !got.PausedAt.Equal(issueNow) {
		t.Fatalf("paused_at = %v, want %v（时间一律 UTC 存取）", got.PausedAt, issueNow)
	}
	if got.PausedAt.Location() != time.UTC {
		t.Fatalf("读回的时间不是 UTC: %v", got.PausedAt.Location())
	}
	if !got.ExpiresAt.IsZero() {
		t.Fatalf("本片不实现自动恢复，expires_at 应为空，实际 %v", got.ExpiresAt)
	}

	// 恢复：同一行覆盖（last-write-wins），且不再出现在暂停清单里，
	// 但 reason 仍然留在库里供事后追查「上次为什么停过」。
	resumed := want
	resumed.Paused = false
	if err := store.SetAccountSyncPause(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	paused, err = store.PausedAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paused["LINFENG"]; ok {
		t.Fatalf("恢复后不该再出现在暂停清单里，实际 %v", paused)
	}
}
