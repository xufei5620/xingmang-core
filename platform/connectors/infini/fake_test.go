package infini

import (
	"context"
	"testing"
)

// 替身必须和真客户端实现同一个接口，否则调用方在测试里用替身、
// 在生产里用真客户端，两条路径可以静静地分叉。
var _ CardClient = (*Fake)(nil)
var _ CardClient = (*Client)(nil)

func TestFakeApplyThenFindByAlias(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	app, err := f.ApplyCard(ctx, ApplyCardRequest{
		ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT",
		UserEmail: "ops@example.com", HolderName: "ZHANG WEI", Alias: "xm-ops-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "init" {
		t.Fatalf("新申请单状态 = %q, want init（开卡是异步的）", app.Status)
	}

	// 这正是超时对账要走的路径：用 alias 精确匹配判定卡开没开成
	page, err := f.ListCards(ctx, ListCardsQuery{Alias: "xm-ops-001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Cards) != 1 {
		t.Fatalf("按 alias 应查到 1 张卡, got %d", len(page.Cards))
	}
	if page.Cards[0].Alias != "xm-ops-001" {
		t.Fatalf("Alias = %q", page.Cards[0].Alias)
	}
}

// 替身要能模拟异步开卡：申请出来是 init，推进之后才是 active。
// 不能一开卡就 active，否则轮询逻辑在测试里根本走不到。
func TestFakeAdvanceMovesCardToActive(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	app, err := f.ApplyCard(ctx, ApplyCardRequest{ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT", UserEmail: "o@e.com", HolderName: "A B", Alias: "a1"})
	if err != nil {
		t.Fatal(err)
	}

	before, err := f.CardStatus(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status == "active" {
		t.Fatal("刚申请的卡不该是 active")
	}

	f.Advance(app.ID, "active")

	after, err := f.CardStatus(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "active" {
		t.Fatalf("推进后状态 = %q, want active", after.Status)
	}
}

func TestFakeFreezeAndUnfreeze(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	app, _ := f.ApplyCard(ctx, ApplyCardRequest{ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT", UserEmail: "o@e.com", HolderName: "A B"})

	if err := f.FreezeCard(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	card, _ := f.CardStatus(ctx, app.ID)
	if card.Status != "frozen" {
		t.Fatalf("冻结后状态 = %q, want frozen", card.Status)
	}

	if err := f.UnfreezeCard(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	card, _ = f.CardStatus(ctx, app.ID)
	if card.Status == "frozen" {
		t.Fatal("解冻后不该还是 frozen")
	}
}

// 替身要能被要求失败：不确定态、限流、被拒这些路径在真实环境里
// 难以复现，而它们恰恰是最需要测试的分支。
func TestFakeCanBeToldToFail(t *testing.T) {
	f := NewFake()
	f.FailNext(context.DeadlineExceeded)

	_, err := f.ApplyCard(context.Background(), ApplyCardRequest{ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT", UserEmail: "o@e.com", HolderName: "A B"})
	if err == nil {
		t.Fatal("被要求失败时必须返回错误")
	}
}

func TestFakeRevealReturnsPlaceholderNotRealPAN(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	app, _ := f.ApplyCard(ctx, ApplyCardRequest{ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT", UserEmail: "o@e.com", HolderName: "A B"})

	revealed, err := f.RevealCard(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revealed.Number == "" {
		t.Fatal("替身也要返回一个可用的卡号占位值")
	}
	// 替身里绝不能出现看起来像真卡号的东西——测试固定值会被复制粘贴
	if revealed.Number[0] != '4' && revealed.Number[0] != '5' {
		return // 不是常见卡 BIN 开头，符合预期
	}
	if len(revealed.Number) == 16 {
		t.Fatalf("替身的卡号不该长得像真卡号: %q", revealed.Number)
	}
}
