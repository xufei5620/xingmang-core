package cards

import (
	"context"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

func newSyncer(client infini.CardClient, store *memStore, now time.Time) *Syncer {
	return NewSyncer([]Account{{ID: testAccount, Client: client}}, store, SyncOptions{
		UnknownGrace: 30 * time.Minute,
		Now:          func() time.Time { return now },
	})
}

// 不确定态的开卡：对账查到 alias 对应的卡，台账收敛成成功，卡落投影。
// 这是整套幂等方案真正闭环的那一步。
func TestSyncConvergesUnknownIssueWhenCardFound(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()

	// 先让上游真的把卡开出来，但让平台侧以为自己不知道结果
	app, err := fake.ApplyCard(context.Background(), infini.ApplyCardRequest{
		ProductID: 1, TopUpAmount: "10", TokenType: "USDT",
		UserEmail: "o@e.com", HolderName: "A B", Alias: AliasFor("issue-x", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.ops["issue-x"] = Operation{
		IdempotencyKey: "issue-x",
		Account:        testAccount,
		Kind:           OpIssue,
		State:          StateUnknown,
		Alias:          AliasFor("issue-x", ""),
		StartedAt:      issueNow.Add(-time.Minute),
	}

	if err := newSyncer(fake, store, issueNow).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := store.ops["issue-x"]
	if got.State != StateSucceeded {
		t.Fatalf("台账状态 = %q, want succeeded", got.State)
	}
	if got.CardID != app.ID {
		t.Fatalf("收敛后的卡 id = %q, want %q", got.CardID, app.ID)
	}
	if _, ok := store.cards[app.ID]; !ok {
		t.Fatal("收敛后应把卡落进投影")
	}
}

// 超出宽限期仍查不到：亮红条要人工，且**不自己改判成失败**——
// 改判会解锁重试，而重试可能开出第二张卡。
func TestSyncEscalatesStaleUnknownWithoutMarkingFailed(t *testing.T) {
	store := newMemStore()
	store.ops["issue-y"] = Operation{
		IdempotencyKey: "issue-y",
		Account:        testAccount,
		Kind:           OpIssue,
		State:          StateUnknown,
		Alias:          AliasFor("issue-y", ""),
		StartedAt:      issueNow.Add(-31 * time.Minute),
	}

	if err := newSyncer(infini.NewFake(), store, issueNow).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := store.ops["issue-y"]
	if !got.NeedsHumanReview {
		t.Fatal("超宽限期必须亮红条")
	}
	if got.State == StateFailed {
		t.Fatal("绝不能自己改判成失败——那会解锁重试")
	}
	if got.RetryAllowed() {
		t.Fatal("人工确认前不许重试")
	}
}

// 开卡是异步的：申请单出来时卡还是 init，作业要把它轮询到 active。
func TestSyncPollsPendingApplicationToActive(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}
	if store.cards[res.CardID].Status == "active" {
		t.Fatal("前提不成立：刚开的卡不该已经是 active")
	}

	fake.Advance(res.CardID, "active")
	if err := newSyncer(fake, store, issueNow).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.cards[res.CardID].Status != "active" {
		t.Fatalf("同步后卡状态 = %q, want active", store.cards[res.CardID].Status)
	}
}

// 上游不可用时，同步作业**不能把不确定态改判**，也不该整轮报废——
// 它下一轮还要再来一次。
func TestSyncKeepsUnknownIntactWhenUpstreamUnavailable(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	store.ops["issue-z"] = Operation{
		IdempotencyKey: "issue-z",
		Account:        testAccount,
		Kind:           OpIssue,
		State:          StateUnknown,
		Alias:          AliasFor("issue-z", ""),
		StartedAt:      issueNow.Add(-time.Minute),
	}
	fake.FailNext(connector.NewError(connector.KindUnavailable, "op", nil))

	err := newSyncer(fake, store, issueNow).RunOnce(context.Background())
	if err == nil {
		t.Fatal("上游不可用应作为错误上报，让作业重试")
	}

	if store.ops["issue-z"].State != StateUnknown {
		t.Fatal("上游读不到时不该动台账状态")
	}
	if store.ops["issue-z"].NeedsHumanReview {
		t.Fatal("上游读不到不等于要人工——下一轮还能再试")
	}
}

// 已经收敛过的操作不该被反复对账，否则每轮都要打一次上游。
func TestSyncSkipsResolvedOperations(t *testing.T) {
	store := newMemStore()
	store.ops["done"] = Operation{
		IdempotencyKey: "done",
		Account:        testAccount,
		Kind:           OpIssue,
		State:          StateSucceeded,
		CardID:         "card_done",
		StartedAt:      issueNow.Add(-time.Hour),
	}
	fake := &countingClient{CardClient: infini.NewFake()}

	if err := newSyncer(fake, store, issueNow).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.aliasListCalls > 0 {
		t.Fatalf("已收敛的操作不该触发对账查询, aliasListCalls=%d", fake.aliasListCalls)
	}
}

type countingClient struct {
	infini.CardClient
	// aliasListCalls 只数**对账**用的那种列卡：带 alias 过滤的。
	//
	// 与发现遍历（不带 alias、翻页）分开数是必要的：两者打的是同一个端点，
	// 混在一个计数器里会让「已收敛的操作不该再对账」这条断言被发现遍历
	// 带绿或带红，而它想守的根本不是那件事。
	aliasListCalls int
	listCalls      int
}

func (c *countingClient) ListCards(ctx context.Context, q infini.ListCardsQuery) (infini.CardPage, error) {
	c.listCalls++
	if q.Alias != "" {
		c.aliasListCalls++
	}
	return c.CardClient.ListCards(ctx, q)
}

// 状态刷新走批量接口，而不是每张卡一次调用。
//
// 周期同步每 300 秒跑一轮；卡一多，逐张查就是几十次调用/轮，而上游的限流
// 阈值是 600 次/分钟/Key（安全说明）。批量一次最多 100 张。
func TestRefreshUsesBatchStatusAndOnlyRefetchesChangedCards(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()

	// 开 250 张卡：跨 3 个批次。
	var ids []string
	for i := 0; i < 250; i++ {
		app, err := fake.ApplyCard(context.Background(), infini.ApplyCardRequest{
			ProductID: 1, TopUpAmount: "1", TokenType: "USDT",
			UserEmail: "a@b.c", HolderName: "A", Alias: "x",
		})
		if err != nil {
			t.Fatal(err)
		}
		card, err := fake.CardStatus(context.Background(), app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertCard(context.Background(), testAccount, card, CardAttribution{}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, app.ID)
	}

	counter := &batchCountingClient{CardClient: fake}
	syncer := NewSyncer([]Account{{ID: testAccount, Client: counter}}, store, SyncOptions{})

	if err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatalf("同步不该报错: %v", err)
	}

	if counter.batchCalls != 3 {
		t.Fatalf("250 张卡应分 3 批查询, got %d", counter.batchCalls)
	}
	// 没有一张卡的状态变过，所以不该有任何单张全量查询。
	if counter.statusCalls != 0 {
		t.Fatalf("状态没变的卡不该再逐张查, got %d", counter.statusCalls)
	}

	// 让一张卡的状态变掉：只有它需要再取一次全量字段
	// （批量接口只回 card_id + status，余额之类要单查）。
	fake.Advance(ids[0], "suspend")
	counter.batchCalls, counter.statusCalls = 0, 0
	if err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counter.statusCalls != 1 {
		t.Fatalf("只有状态变了的那张要单查, got %d", counter.statusCalls)
	}
	if store.cards[ids[0]].Status != "suspend" {
		t.Fatalf("变化的卡没落库: %q", store.cards[ids[0]].Status)
	}
}

type batchCountingClient struct {
	infini.CardClient
	batchCalls  int
	statusCalls int
}

func (c *batchCountingClient) BatchCardStatus(ctx context.Context, ids []string) (map[string]string, error) {
	c.batchCalls++
	return c.CardClient.BatchCardStatus(ctx, ids)
}

func (c *batchCountingClient) CardStatus(ctx context.Context, id string) (infini.Card, error) {
	c.statusCalls++
	return c.CardClient.CardStatus(ctx, id)
}

// 同步要**发现**上游有而投影没有的卡。
//
// 2026-09-05 生产上暴露的缺口:产品负责人在 Infini 后台有 11 张卡,平台只
// 显示 2 张。根因是 refreshTrackedCards 只遍历投影里已有的卡,而卡只有两条
// 路径能进投影——我们自己开的、或回调带来的。在上游后台直接建的卡,平台
// 压根不知道它存在。一个叫「卡片管理」的页面只管着 18% 的卡。
func TestSyncDiscoversCardsCreatedUpstream(t *testing.T) {
	fake := infini.NewFake()
	// 模拟"在 Infini 后台直接建的卡":上游有,我们的投影里没有。
	fake.SeedCard(infini.Card{
		ID: "upstream-1", Alias: "Two.V", Status: "active",
		Mask: "441357******2650",
	})
	store := newMemStore()
	syncer := newSyncer(fake, store, issueNow)

	if err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if _, ok := store.cards["upstream-1"]; !ok {
		t.Fatalf("上游直接建的卡必须被发现并落进投影, 投影里有 %d 张", len(store.cards))
	}
	// 账号要对：拿 A 账号的卡记到 B 名下，等于把另一张卡的钱算错地方。
	if got := store.cardAccount["upstream-1"]; got != testAccount {
		t.Fatalf("发现的卡应归到它所在的账号, got %q", got)
	}
}

// 发现是幂等的：已经在投影里的卡不会被当成新卡重复处理。
func TestSyncDiscoveryIsIdempotent(t *testing.T) {
	fake := infini.NewFake()
	fake.SeedCard(infini.Card{ID: "upstream-1", Alias: "Two.V", Status: "active"})
	store := newMemStore()
	syncer := newSyncer(fake, store, issueNow)

	for i := 0; i < 3; i++ {
		if err := syncer.RunOnce(context.Background()); err != nil {
			t.Fatalf("第 %d 轮同步失败: %v", i+1, err)
		}
	}
	if len(store.cards) != 1 {
		t.Fatalf("重复同步不该产生多条, got %d", len(store.cards))
	}
}
