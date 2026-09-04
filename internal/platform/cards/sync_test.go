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
	if fake.listCalls > 0 {
		t.Fatalf("已收敛的操作不该触发对账查询, listCalls=%d", fake.listCalls)
	}
}

type countingClient struct {
	infini.CardClient
	listCalls int
}

func (c *countingClient) ListCards(ctx context.Context, q infini.ListCardsQuery) (infini.CardPage, error) {
	c.listCalls++
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
