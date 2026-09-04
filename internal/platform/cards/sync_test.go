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
		UserEmail: "o@e.com", HolderName: "A B", Alias: AliasFor("issue-x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.ops["issue-x"] = Operation{
		IdempotencyKey: "issue-x",
		Account:        testAccount,
		Kind:           OpIssue,
		State:          StateUnknown,
		Alias:          AliasFor("issue-x"),
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
		Alias:          AliasFor("issue-y"),
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
		Alias:          AliasFor("issue-z"),
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
