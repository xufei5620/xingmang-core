package cards

import (
	"context"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// 卡面明文落库（产品负责人 2026-09-04 决定）。上游的列表接口只给 mask，
// 明文要调 reveal 才拿得到，所以同步作业负责拉一次存下来。
//
// **只拉一次**是这里最要紧的性质：卡号在卡的生命周期内不变，每轮同步都拉
// 等于每个周期 N 次 reveal 调用——上游限流阈值未知，而 reveal 还是个
// 要 IP 白名单的受限权限。

func TestSyncFetchesPANOnceForActiveCards(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}
	fake.Advance(res.CardID, "active")

	counting := &countingReveal{CardClient: fake}
	syncer := NewSyncer([]Account{{ID: testAccount, Client: counting}}, store,
		SyncOptions{UnknownGrace: 30 * time.Minute, Now: func() time.Time { return issueNow }})

	// 第一轮：卡刚 active，明文还没拉过
	if _, err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counting.calls != 1 {
		t.Fatalf("第一轮应拉一次明文, got %d", counting.calls)
	}
	if store.secrets[res.CardID].Number == "" {
		t.Fatal("明文应被落库")
	}

	// 第二、三轮：已经有了，绝不能再拉
	for i := 0; i < 2; i++ {
		if _, err := syncer.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if counting.calls != 1 {
		t.Fatalf("明文只该拉一次，got %d 次——每轮都拉等于每周期 N 次 reveal", counting.calls)
	}
}

// 卡还没 active 时拉不到明文（上游还没把卡建出来），不该白调。
func TestSyncSkipsPANFetchForInactiveCards(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	if _, err := svc.IssueCard(context.Background(), issueReq()); err != nil {
		t.Fatal(err)
	}
	// 刻意不 Advance：卡停在 init

	counting := &countingReveal{CardClient: fake}
	syncer := NewSyncer([]Account{{ID: testAccount, Client: counting}}, store,
		SyncOptions{UnknownGrace: 30 * time.Minute, Now: func() time.Time { return issueNow }})

	if _, err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counting.calls != 0 {
		t.Fatalf("未激活的卡不该拉明文, got %d 次", counting.calls)
	}
}

// 拉明文失败不该让整轮同步报废——卡状态刷新是独立的一件事。
func TestSyncPANFetchFailureDoesNotAbortTheRound(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)

	res, _ := svc.IssueCard(context.Background(), issueReq())
	fake.Advance(res.CardID, "active")

	failing := &failingReveal{CardClient: fake}
	syncer := NewSyncer([]Account{{ID: testAccount, Client: failing}}, store,
		SyncOptions{UnknownGrace: 30 * time.Minute, Now: func() time.Time { return issueNow }})

	round, err := syncer.RunOnce(context.Background())
	// XM-CARD-VISIBILITY：这一条此前断言「整轮报错」。现在的契约是
	// **部分成功不算作业失败**——同一轮里发现与刷新都成功了，把整轮判失败
	// 会让 River 重试三次再落一条 discarded，而重试解决不了 reveal 失败。
	// 失败本身没有被吞掉：它在 RoundResult 里，由 cards.sync.failed 按
	// 「同一账号同一步骤连续 N 轮」挑出来。
	if err != nil {
		t.Fatalf("拉明文失败不该让整轮报错（其余步骤成功了）: %v", err)
	}
	failures := round.Failures()
	if len(failures) == 0 {
		t.Fatal("拉明文失败必须留在结果里，不能被静默")
	}
	if failures[0].Step != StepFetchSecrets {
		t.Fatalf("失败的步骤 = %q, want %q", failures[0].Step, StepFetchSecrets)
	}
	if !round.AnySucceeded() {
		t.Fatal("同一轮里刷新卡状态是成功的，AnySucceeded 应为真")
	}
	if round.AllFailed() {
		t.Fatal("只有一步失败不该算整轮失败")
	}
	// 但卡状态该刷新的还是刷新了
	if store.cards[res.CardID].Status != "active" {
		t.Fatal("拉明文失败不该影响卡状态刷新")
	}
}

type countingReveal struct {
	infini.CardClient
	calls int
}

func (c *countingReveal) RevealCard(ctx context.Context, cardID string) (infini.RevealedCard, error) {
	c.calls++
	return c.CardClient.RevealCard(ctx, cardID)
}

type failingReveal struct {
	infini.CardClient
}

func (f *failingReveal) RevealCard(ctx context.Context, cardID string) (infini.RevealedCard, error) {
	return infini.RevealedCard{}, context.DeadlineExceeded
}
