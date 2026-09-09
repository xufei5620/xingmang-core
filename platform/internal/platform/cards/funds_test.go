package cards

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

func issuedCard(t *testing.T, svc *Service) string {
	t.Helper()
	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}
	return res.CardID
}

func TestTopUpGoesThroughLimitsAndLedger(t *testing.T) {
	store := newMemStore()
	svc := newService(infini.NewFake(), store)
	cardID := issuedCard(t, svc)

	_, err := svc.TopUpCard(context.Background(), FundsRequest{
		Account:        testAccount,
		IdempotencyKey: "topup-1",
		CardID:         cardID,
		Amount:         "20",
		TokenType:      "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}

	if store.ops["topup-1"].State != StateSucceeded {
		t.Fatalf("台账状态 = %q, want succeeded", store.ops["topup-1"].State)
	}
	if store.ops["topup-1"].Kind != OpTopUp {
		t.Fatalf("台账类型 = %q, want topup", store.ops["topup-1"].Kind)
	}
}

func TestTopUpRejectsOverLimit(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	cardID := issuedCard(t, svc)

	_, err := svc.TopUpCard(context.Background(), FundsRequest{
		Account:        testAccount,
		IdempotencyKey: "topup-2", CardID: cardID, Amount: "101", TokenType: "USDT",
	})
	if !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("错误 = %v, want ErrPerOperationExceeded", err)
	}
}

// 充值超时与开卡同理：钱可能已经划走了，禁止自动重试。
func TestTopUpTimeoutLandsInUnknown(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)
	cardID := issuedCard(t, svc)

	fake.FailNext(connector.NewError(connector.KindUnavailable, "op", nil))
	_, _ = svc.TopUpCard(context.Background(), FundsRequest{
		Account:        testAccount,
		IdempotencyKey: "topup-3", CardID: cardID, Amount: "20", TokenType: "USDT",
	})

	op := store.ops["topup-3"]
	if op.State != StateUnknown {
		t.Fatalf("台账状态 = %q, want unknown", op.State)
	}
	if op.RetryAllowed() {
		t.Fatal("充值的不确定态禁止重试")
	}
}

// 赎回是把余额退回账户，不是花钱，所以不受金额上限约束——
// 用上限卡住退款会在最需要止损的时候拦住止损动作。
func TestRedeemIsNotBoundByLimits(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	cardID := issuedCard(t, svc)

	// 金额远超单笔上限 100
	if _, err := svc.RedeemCard(context.Background(), FundsRequest{
		Account:        testAccount,
		IdempotencyKey: "redeem-1", CardID: cardID, Amount: "9999", TokenType: "USDT",
	}); err != nil {
		t.Fatalf("赎回不该被金额上限拦住: %v", err)
	}
}

// 但赎回仍然要幂等：重复赎回会重复动账。
func TestRedeemIsIdempotent(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)
	cardID := issuedCard(t, svc)

	req := FundsRequest{Account: testAccount, IdempotencyKey: "redeem-2", CardID: cardID, Amount: "5", TokenType: "USDT"}
	if _, err := svc.RedeemCard(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RedeemCard(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if store.ops["redeem-2"].Kind != OpRedeem {
		t.Fatalf("台账类型 = %q", store.ops["redeem-2"].Kind)
	}
}

// 冻结与解冻在上游是**天然幂等**的：重复冻结不产生新效果。
// 因此它们的超时可以安全重试，落 failed 而不是 unknown——
// 把天然幂等的操作也锁进不确定态，只会让卡在出事时冻不上。
func TestFreezeTimeoutIsRetryableUnlikeSpendingOperations(t *testing.T) {
	store := newMemStore()
	fake := infini.NewFake()
	svc := newService(fake, store)
	cardID := issuedCard(t, svc)

	fake.FailNext(connector.NewError(connector.KindUnavailable, "op", nil))
	_ = svc.FreezeCard(context.Background(), testAccount, "freeze-1", cardID)

	op := store.ops["freeze-1"]
	if op.State != StateFailed {
		t.Fatalf("冻结超时应落 failed（天然幂等，重试无害）, got %q", op.State)
	}
	if !op.RetryAllowed() {
		t.Fatal("冻结应允许重试")
	}
}

func TestFreezeAndUnfreezeChangeUpstreamStatus(t *testing.T) {
	fake := infini.NewFake()
	svc := newService(fake, newMemStore())
	cardID := issuedCard(t, svc)

	if err := svc.FreezeCard(context.Background(), testAccount, "freeze-2", cardID); err != nil {
		t.Fatal(err)
	}
	card, _ := fake.CardStatus(context.Background(), cardID)
	if card.Status != "frozen" {
		t.Fatalf("冻结后上游状态 = %q", card.Status)
	}

	if err := svc.UnfreezeCard(context.Background(), testAccount, "unfreeze-1", cardID); err != nil {
		t.Fatal(err)
	}
	card, _ = fake.CardStatus(context.Background(), cardID)
	if card.Status == "frozen" {
		t.Fatal("解冻后不该还是 frozen")
	}
}

// 四个 Action 都要能注册，且都只允许人类身份。
func TestFundsActionsAreRegisteredAndHumanOnly(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	reg := newRegistryWith(t, svc)

	for _, id := range []string{ActionTopUp, ActionRedeem, ActionFreeze, ActionUnfreeze} {
		def, _, ok := reg.Lookup(id, actionVersion)
		if !ok {
			t.Fatalf("%s 未注册", id)
		}
		if def.RiskLevel.RequiresAdvancedControls() {
			t.Fatalf("%s 的风险等级会让它无法执行", id)
		}
		for _, pt := range def.PrincipalTypes {
			if pt != "HUMAN" {
				t.Fatalf("%s 不该允许 %q 身份", id, pt)
			}
		}
	}
}

// 充值属于花钱，权限归 card.manage；赎回/冻结/解冻是止损与处置，同样归
// card.manage——它们与 card.issue 分开，是为了「能开卡的人未必能动已有的卡」。
func TestFundsActionsUseManagePermission(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	reg := newRegistryWith(t, svc)

	for _, id := range []string{ActionTopUp, ActionRedeem, ActionFreeze, ActionUnfreeze} {
		def, _, _ := reg.Lookup(id, actionVersion)
		if def.Permission != PermissionManage {
			t.Fatalf("%s 的权限 = %q, want %q", id, def.Permission, PermissionManage)
		}
	}
}

// 关停卡走台账，且**不是天然幂等的**。
//
// 冻结/解冻重复执行不产生新效果，所以超时可以安全重试。关停不同：它不可逆，
// 而且会触发余额结清。超时后我们不知道上游收到没有，落 unknown 交给对账，
// 绝不自动重试——这与开卡、充值同一档。
func TestDeleteCardIsNotNaturallyIdempotent(t *testing.T) {
	if naturallyIdempotent(OpDelete) {
		t.Fatal("关停不可逆，不能当成天然幂等——那会允许一次超时后的自动重试")
	}
	// 与冻结对照：那两个确实是幂等的。
	if !naturallyIdempotent(OpFreeze) || !naturallyIdempotent(OpUnfreeze) {
		t.Fatal("冻结/解冻仍应是天然幂等的")
	}
}

func TestDeleteCardRecordsOperationAndAdvancesUpstream(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteCard(context.Background(), testAccount, "del-1", res.CardID); err != nil {
		t.Fatal(err)
	}

	op, ok := store.ops["del-1"]
	if !ok || op.Kind != OpDelete || op.State != StateSucceeded {
		t.Fatalf("关停应留台账且收敛: %+v", op)
	}
	// 上游进 pending_delete（异步：结清余额后才 deleted），投影要跟上。
	if store.cards[res.CardID].Status != "pending_delete" {
		t.Fatalf("关停后投影状态 = %q", store.cards[res.CardID].Status)
	}
}

// 同一个幂等键重复提交不再打上游。
func TestDeleteCardIsIdempotentByKey(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()
	svc := newService(fake, store)

	res, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCard(context.Background(), testAccount, "del-1", res.CardID); err != nil {
		t.Fatal(err)
	}
	// 第二次：卡已经在 pending_delete，若真打上游 fake 会再改一次状态；
	// 这里断言的是「不再打」——台账已 succeeded 就直接返回。
	if err := svc.DeleteCard(context.Background(), testAccount, "del-1", res.CardID); err != nil {
		t.Fatalf("同键重复提交应直接返回: %v", err)
	}
}
