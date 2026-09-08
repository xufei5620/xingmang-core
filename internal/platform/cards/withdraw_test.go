package cards

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// 提现额度**不接受 unlimited**。
//
// 卡片那边产品负责人选了不限，理由是「Infini 账户余额本身就是硬顶」。
// 提现不成立：提现的目的就是把余额搬空，余额不构成任何约束。
func TestWithdrawLimitsRejectUnlimited(t *testing.T) {
	l := Limits{PerOperation: LimitUnlimited, PerDay: "1000"}
	if err := l.CheckWithdraw("1", "0"); err == nil {
		t.Fatal("提现额度不许填 unlimited")
	}
	if err := (Limits{PerOperation: "100", PerDay: LimitUnlimited}).CheckWithdraw("1", "0"); err == nil {
		t.Fatal("单日额度同样不许 unlimited")
	}
	// 真数字照常工作，且复用卡片那边的精度闸。
	if err := (Limits{PerOperation: "100", PerDay: "1000"}).CheckWithdraw("50", "0"); err != nil {
		t.Fatalf("正常额度应放行: %v", err)
	}
	if err := (Limits{PerOperation: "100", PerDay: "1000"}).CheckWithdraw("100.0000001", "0"); err == nil {
		t.Fatal("超出可校验精度的金额必须拒绝——校验的值必须就是发出去的值")
	}
}

// 未登记的地址一律拒绝，且**在打上游之前**拒绝。
//
// 白名单挡不住有人拿密钥直接打 Infini（那条路平台管不了），但能挡住误操作
// 与通过后台的滥用——而这两样才是日常真正会发生的。
func TestWithdrawRefusesUnlistedAddress(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	svc := newWithdrawService(fake, store)

	_, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "没登记过",
	})
	if err == nil {
		t.Fatal("未登记的地址必须拒绝")
	}
	if fake.withdrawCalls != 0 {
		t.Fatal("必须在打上游之前就拒绝")
	}
}

// 登记过的地址才放行，且**发给上游的是登记时存的地址**，不是调用方传的。
//
// 这一点是白名单真正的价值所在：调用方只能选「哪一条登记」，
// 地址本身由服务端从库里取。传地址进来再比对，是另一回事——
// 那样一个比对逻辑的疏漏就能让任意地址过去。
func TestWithdrawUsesStoredAddressNotCallerSupplied(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TRealAddressFromDB", Label: "冷钱包", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	if _, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	}); err != nil {
		t.Fatal(err)
	}
	if fake.lastAddress != "TRealAddressFromDB" {
		t.Fatalf("必须用库里存的地址, got %q", fake.lastAddress)
	}
}

// 地址登记在哪个账号下，就只能在那个账号下用。
//
// 两个账号的资金是分开的；拿 A 账号登记的地址从 B 账号提现，
// 等于绕过了 A 的白名单审核。
func TestWithdrawAddressIsScopedToAccount(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: "OTHER", Chain: "TRON", Address: "TXYZ", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	if _, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	}); err == nil {
		t.Fatal("别的账号登记的地址不能用")
	}
}

// 链必须与地址登记时的链一致。
//
// 同一串地址在不同链上可能都「看起来合法」，但转错链的钱找不回来。
func TestWithdrawRefusesChainMismatch(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TXYZ", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	_, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "ETHEREUM", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	})
	if err == nil {
		t.Fatal("链与登记不符必须拒绝——转错链的钱找不回来")
	}
	if !strings.Contains(err.Error(), "链") {
		t.Fatalf("错误要说清是链的问题: %v", err)
	}
}

// 超额必须在打上游之前拒绝。
func TestWithdrawEnforcesLimitsBeforeCallingUpstream(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TXYZ", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	if _, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "999999", AddressID: "addr-1",
	}); !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("超额应被拒: %v", err)
	}
	if fake.withdrawCalls != 0 {
		t.Fatal("必须在打上游之前拒绝")
	}
}

// 同一个 request_id 重投不再打上游：台账已经有它了。
func TestWithdrawIsIdempotentByRequestID(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TXYZ", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	req := WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	}
	if _, err := svc.Withdraw(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Withdraw(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if fake.withdrawCalls != 1 {
		t.Fatalf("同键重投不该再打上游, got %d", fake.withdrawCalls)
	}
}

// ---- 测试替身 ----

type withdrawFake struct {
	*infini.Fake
	withdrawCalls int
	lastAddress   string
}

func newWithdrawFake() *withdrawFake { return &withdrawFake{Fake: infini.NewFake()} }

func (f *withdrawFake) Withdraw(ctx context.Context, req infini.WithdrawRequest) (infini.WithdrawResult, error) {
	f.withdrawCalls++
	f.lastAddress = req.WalletAddress
	return f.Fake.Withdraw(ctx, req)
}

// newWithdrawService 组一个带真实额度的服务。
//
// 额度用真数字而不是 unlimited：提现服务本来就拒绝 unlimited，
// 用它组测试会让每个用例都卡在额度校验上。
// newWithdrawService 组装一个「额度已配好」的服务。
//
// 额度现在存在库里（管理后台可调），而它是提现的必经闸——不配的话每个
// 用例都会先撞上 ErrLimitsUnconfigured，测不到它真正想测的那一段。
// 专门测「没配额度」的用例直接用 NewWithdrawService，不走这个助手。
func newWithdrawService(client infini.CardClient, store *memStore) *WithdrawService {
	store.withdrawLimits[testAccount] = Limits{PerOperation: "1000", PerDay: "5000"}
	return NewWithdrawService([]Account{{
		ID:     testAccount,
		Client: client,
		Limits: Limits{PerOperation: "1000", PerDay: "5000"},
	}}, store, func() time.Time { return issueNow })
}

// 额度从**库里**读，不是从进程配置读（XM-CARD6，产品负责人 2026-09-05
// 决定改成管理后台可调）。
//
// 这条断言的要害是「改完立刻生效」：额度存在进程里就意味着改一次要重启
// 一次，而重启 API 会把正在用后台的人踢下线——于是没人愿意改，额度就永远
// 停在第一次拍脑袋定的那个数上。
func TestWithdrawLimitsComeFromStore(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TAddr", Enabled: true,
	}
	svc := newWithdrawService(fake, store)
	// 助手会先塞一份宽松额度，这里覆盖成要测的那一档（顺序要紧）。
	store.withdrawLimits[testAccount] = Limits{PerOperation: "100", PerDay: "1000"}

	req := WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "150", AddressID: "addr-1",
	}
	if _, err := svc.Withdraw(context.Background(), req); !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("150 应超出库里的单次上限 100, got %v", err)
	}

	// 把上限调高——不重启、不改配置，下一笔立刻放行。
	store.withdrawLimits[testAccount] = Limits{PerOperation: "500", PerDay: "1000"}
	if _, err := svc.Withdraw(context.Background(), req); err != nil {
		t.Fatalf("调高上限后应放行: %v", err)
	}
}

// 没配额度的账号一律不能提现。
//
// 这是新装环境的默认状态，也是唯一正确的默认：一个「没人设过上限」的账号
// 若能提现，等于上限的默认值是无穷大。
func TestWithdrawRefusesAccountWithoutLimits(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TAddr", Enabled: true,
	}
	svc := NewWithdrawService([]Account{{ID: testAccount, Client: fake}},
		store, func() time.Time { return issueNow })

	_, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "1", AddressID: "addr-1",
	})
	if !errors.Is(err, ErrLimitsUnconfigured) {
		t.Fatalf("没设过额度必须拒绝, got %v", err)
	}
	if fake.withdrawCalls != 0 {
		t.Fatal("必须在打上游之前就拒绝")
	}
}

// 提现额度与卡片额度是**两套**，存在两个地方。
//
// 卡片额度在 Account 上（进程配置，生产按裁定配成 unlimited——「只要 infini
// 那边有余额就可以开」）；提现额度在库里（管理后台可调）。若提现读的是卡片
// 那个字段，CheckWithdraw 会永远抛 ErrWithdrawLimitsUnbounded——提现变成一个
// 装好了但永远跑不起来的功能，而这种「fail-closed 到不可用」在上线当天才会
// 被发现。
func TestWithdrawLimitsAreSeparateFromCardLimits(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TAddr", Enabled: true,
	}
	// 提现额度单独存库里，配真数字。
	store.withdrawLimits[testAccount] = Limits{PerOperation: "500", PerDay: "2000"}
	svc := NewWithdrawService([]Account{{
		ID:     testAccount,
		Client: fake,
		// 卡片不限额——这就是生产的配法。
		Limits: Limits{PerOperation: LimitUnlimited, PerDay: LimitUnlimited},
	}}, store, func() time.Time { return issueNow })

	if _, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "100", AddressID: "addr-1",
	}); err != nil {
		t.Fatalf("卡片不限额不应妨碍提现: %v", err)
	}

	// 而提现自己的上限照常生效。
	_, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-2", Chain: "TRON", TokenType: "USDT",
		Amount: "600", AddressID: "addr-1",
	})
	if !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("超出提现单次上限必须拒绝, got %v", err)
	}
}

// 未收敛的提现由周期同步推进到终态。
//
// 没有这一步，页面上的状态会永远停在「已提交」——上游受理后是异步上链的，
// 而提现响应只回一个受理确认。手动刷新按钮不算解法：人不会盯着看，
// 而「钱到底转出去没有」正是最需要自己变准的那个数。
func TestSyncerAdvancesOpenWithdrawals(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON", Address: "TAddr", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	if _, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	}); err != nil {
		t.Fatal(err)
	}

	// 上游随后上链完成。
	fake.AdvanceWithdraw("w-1", "completed", "0xhash")

	syncer := NewSyncer([]Account{{ID: testAccount, Client: fake}}, store, SyncOptions{
		Withdrawals: svc,
		Now:         func() time.Time { return issueNow },
	})
	if _, err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	got := store.withdrawals["w-1"]
	if got.Status != "completed" {
		t.Fatalf("提现状态应被推进到终态, got %q", got.Status)
	}
	if got.TxHash != "0xhash" {
		t.Fatalf("链上哈希应被回填（人要拿它去区块浏览器核对）, got %q", got.TxHash)
	}
}

// 没配提现服务时这一步整个跳过，不影响其余同步。
//
// 提现是可选功能（账号可以不配额度）；一个「没配提现就同步整体报错」的
// worker 会让卡片同步跟着一起停。
func TestSyncerSkipsWithdrawalsWhenUnconfigured(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	syncer := NewSyncer([]Account{{ID: testAccount, Client: fake}}, store, SyncOptions{
		Now: func() time.Time { return issueNow },
	})
	if _, err := syncer.RunOnce(context.Background()); err != nil {
		t.Fatalf("没配提现不该让同步失败: %v", err)
	}
}

// 停用的地址一律拒绝，且**在打上游之前**拒绝。
//
// 下线一条地址的全部意义就在这里：它必须真的挡住钱，而不只是从下拉框里
// 消失。只靠前端不渲染是不够的——一个还留着旧页面的标签页仍然能提交。
func TestWithdrawRefusesDisabledAddress(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TAddr", Label: "已下线的冷钱包", Enabled: false,
	}
	svc := newWithdrawService(fake, store)

	_, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	})
	if !errors.Is(err, ErrAddressNotAllowed) {
		t.Fatalf("停用的地址必须拒绝, got %v", err)
	}
	// 文案要能区分「没登记过」和「登记了但停用了」：前者去登记，
	// 后者去启用，两者的下一步动作不同。
	if !strings.Contains(err.Error(), "停用") {
		t.Fatalf("错误里应说明是被停用了, got %v", err)
	}
	if fake.withdrawCalls != 0 {
		t.Fatal("必须在打上游之前就拒绝")
	}
}

// 启用的地址照常放行——停用检查不能把正常路径也挡了。
func TestWithdrawAllowsEnabledAddress(t *testing.T) {
	fake := newWithdrawFake()
	store := newMemStore()
	store.addresses["addr-1"] = WithdrawAddress{
		ID: "addr-1", Account: testAccount, Chain: "TRON",
		Address: "TAddr", Enabled: true,
	}
	svc := newWithdrawService(fake, store)

	if _, err := svc.Withdraw(context.Background(), WithdrawRequest{
		Account: testAccount, RequestID: "w-1", Chain: "TRON", TokenType: "USDT",
		Amount: "10", AddressID: "addr-1",
	}); err != nil {
		t.Fatalf("启用的地址应放行: %v", err)
	}
}
