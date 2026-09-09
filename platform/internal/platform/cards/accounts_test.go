package cards

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// 两个账号并列管理：各自持卡、各自有资金、各自算限额。
// 账号维度必须贯穿到底，任何一处漏掉都会让两个账号的钱混在一起算。

func twoAccountService(store *memStore) (*Service, *infini.Fake, *infini.Fake) {
	a := infini.NewFake()
	b := infini.NewFake()
	svc := NewService([]Account{
		{ID: "main", Client: a, Limits: Limits{PerOperation: "100", PerDay: "500"}},
		{ID: "backup", Client: b, Limits: Limits{PerOperation: "50", PerDay: "200"}},
	}, store, func() time.Time { return issueNow })
	return svc, a, b
}

func accountIssueReq(account, key string) IssueRequest {
	r := issueReq()
	r.Account = account
	r.IdempotencyKey = key
	return r
}

// 开卡只能落到指定账号的上游，绝不能串。
func TestIssueRoutesToNamedAccountOnly(t *testing.T) {
	store := newMemStore()
	svc, mainFake, backupFake := twoAccountService(store)

	if _, err := svc.IssueCard(context.Background(), accountIssueReq("main", "issue-main")); err != nil {
		t.Fatal(err)
	}

	mainPage, _ := mainFake.ListCards(context.Background(), infini.ListCardsQuery{})
	backupPage, _ := backupFake.ListCards(context.Background(), infini.ListCardsQuery{})
	if len(mainPage.Cards) != 1 {
		t.Fatalf("main 账号应有 1 张卡, got %d", len(mainPage.Cards))
	}
	if len(backupPage.Cards) != 0 {
		t.Fatalf("backup 账号不该被碰, got %d 张卡", len(backupPage.Cards))
	}
}

// 未配置的账号必须 fail closed：绝不能回落到「第一个账号」或「默认账号」，
// 那会把钱花到一个调用方没打算用的账号上。
func TestIssueRejectsUnknownAccount(t *testing.T) {
	store := newMemStore()
	svc, mainFake, _ := twoAccountService(store)

	_, err := svc.IssueCard(context.Background(), accountIssueReq("typo", "issue-x"))
	if !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("错误 = %v, want ErrUnknownAccount", err)
	}

	page, _ := mainFake.ListCards(context.Background(), infini.ListCardsQuery{})
	if len(page.Cards) != 0 {
		t.Fatal("未知账号绝不能回落到任何一个真实账号")
	}
	if _, ok := store.ops["issue-x"]; ok {
		t.Fatal("未知账号不该在台账里留记录")
	}
}

// 账号为空同样拒绝：省略账号在双账号下没有安全的默认值。
func TestIssueRejectsEmptyAccount(t *testing.T) {
	store := newMemStore()
	svc, _, _ := twoAccountService(store)

	if _, err := svc.IssueCard(context.Background(), accountIssueReq("", "issue-y")); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("错误 = %v, want ErrUnknownAccount", err)
	}
}

// 限额按账号各算：backup 的单笔上限 50，同样的 60 在 main 放行、在 backup 拒绝。
func TestLimitsAreEvaluatedPerAccount(t *testing.T) {
	store := newMemStore()
	svc, _, _ := twoAccountService(store)

	okReq := accountIssueReq("main", "issue-ok")
	okReq.TopUpAmount = "60"
	if _, err := svc.IssueCard(context.Background(), okReq); err != nil {
		t.Fatalf("main 单笔上限 100，60 应放行: %v", err)
	}

	blocked := accountIssueReq("backup", "issue-blocked")
	blocked.TopUpAmount = "60"
	if _, err := svc.IssueCard(context.Background(), blocked); !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("backup 单笔上限 50，60 应被拒, err = %v", err)
	}
}

// 今日累计也按账号各算：一个账号花掉的额度不该占用另一个账号的。
func TestDailySpendIsCountedPerAccount(t *testing.T) {
	store := newMemStore()
	store.spentByAccount = map[string]string{"main": "495", "backup": "0"}
	svc, _, _ := twoAccountService(store)

	if _, err := svc.IssueCard(context.Background(), accountIssueReq("main", "k1")); !errors.Is(err, ErrDailyExceeded) {
		t.Fatalf("main 今日已用 495，再来 10 应超单日 500, err = %v", err)
	}
	if _, err := svc.IssueCard(context.Background(), accountIssueReq("backup", "k2")); err != nil {
		t.Fatalf("backup 今日未用，10 应放行: %v", err)
	}
}

// 台账必须记账号：不记的话对账时不知道该去哪个账号查，
// 而拿 A 账号的卡去认 B 账号那笔操作，等于把另一张卡的 id 记错地方。
func TestOperationLedgerRecordsAccount(t *testing.T) {
	store := newMemStore()
	svc, _, _ := twoAccountService(store)

	if _, err := svc.IssueCard(context.Background(), accountIssueReq("backup", "issue-acct")); err != nil {
		t.Fatal(err)
	}

	if got := store.ops["issue-acct"].Account; got != "backup" {
		t.Fatalf("台账里的账号 = %q, want backup", got)
	}
}

// 同一个幂等键在两个账号上不能各来一次：幂等键标识的是「哪一笔业务操作」，
// 复用它等于让去重失效。
func TestIdempotencyKeyIsGlobalNotPerAccount(t *testing.T) {
	store := newMemStore()
	svc, mainFake, backupFake := twoAccountService(store)

	if _, err := svc.IssueCard(context.Background(), accountIssueReq("main", "shared-key")); err != nil {
		t.Fatal(err)
	}
	// 同一个键换个账号再来一次：必须被去重挡住，不能真的去 backup 开卡
	if _, err := svc.IssueCard(context.Background(), accountIssueReq("backup", "shared-key")); err != nil {
		t.Fatal(err)
	}

	mainPage, _ := mainFake.ListCards(context.Background(), infini.ListCardsQuery{})
	backupPage, _ := backupFake.ListCards(context.Background(), infini.ListCardsQuery{})
	if len(mainPage.Cards)+len(backupPage.Cards) != 1 {
		t.Fatalf("同一幂等键只该开出一张卡, main=%d backup=%d",
			len(mainPage.Cards), len(backupPage.Cards))
	}
}

// 同步作业要遍历所有账号，且每个账号只用自己的客户端对账。
func TestSyncCoversEveryAccount(t *testing.T) {
	store := newMemStore()
	a := infini.NewFake()
	b := infini.NewFake()

	// 两个账号各有一笔不确定态的开卡，上游其实都开成了
	for _, tc := range []struct {
		account string
		key     string
		fake    *infini.Fake
	}{{"main", "u-main", a}, {"backup", "u-backup", b}} {
		alias := AliasFor(tc.key, "")
		if _, err := tc.fake.ApplyCard(context.Background(), infini.ApplyCardRequest{
			ProductID: 1, TopUpAmount: "10", TokenType: "USDT",
			UserEmail: "o@e.com", HolderName: "A B", Alias: alias,
		}); err != nil {
			t.Fatal(err)
		}
		store.ops[tc.key] = Operation{
			IdempotencyKey: tc.key, Account: tc.account, Kind: OpIssue,
			State: StateUnknown, Alias: alias, StartedAt: issueNow.Add(-time.Minute),
		}
	}

	syncer := NewSyncer([]Account{{ID: "main", Client: a}, {ID: "backup", Client: b}},
		store, SyncOptions{UnknownGrace: 30 * time.Minute, Now: func() time.Time { return issueNow }})

	if _, err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"u-main", "u-backup"} {
		if store.ops[key].State != StateSucceeded {
			t.Fatalf("%s 应收敛为 succeeded, got %q", key, store.ops[key].State)
		}
	}
}

// 对账绝不能跨账号找卡：A 账号那张 alias 相同的卡不能认成 B 账号的结果。
func TestSyncNeverMatchesCardFromAnotherAccount(t *testing.T) {
	store := newMemStore()
	a := infini.NewFake()
	b := infini.NewFake()

	alias := AliasFor("u-cross", "")
	// 卡开在 main 上，但台账记的是 backup 账号的操作
	if _, err := a.ApplyCard(context.Background(), infini.ApplyCardRequest{
		ProductID: 1, TopUpAmount: "10", TokenType: "USDT",
		UserEmail: "o@e.com", HolderName: "A B", Alias: alias,
	}); err != nil {
		t.Fatal(err)
	}
	store.ops["u-cross"] = Operation{
		IdempotencyKey: "u-cross", Account: "backup", Kind: OpIssue,
		State: StateUnknown, Alias: alias, StartedAt: issueNow.Add(-31 * time.Minute),
	}

	syncer := NewSyncer([]Account{{ID: "main", Client: a}, {ID: "backup", Client: b}},
		store, SyncOptions{UnknownGrace: 30 * time.Minute, Now: func() time.Time { return issueNow }})
	if _, err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := store.ops["u-cross"]
	if got.State == StateSucceeded {
		t.Fatal("绝不能拿 main 账号的卡认成 backup 账号那笔操作的结果")
	}
	if !got.NeedsHumanReview {
		t.Fatal("在自己账号里查不到且超宽限期，应报人工")
	}
}
