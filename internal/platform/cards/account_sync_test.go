package cards

// 按账号暂停同步（XM-CARD-VISIBILITY 第 3 项）：Action、跳过、结果注明三段。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// spyClient 分别数每一类上游调用。
//
// 既有的 countingClient 只数列卡、batchCountingClient 只数批量与单查，
// 两个都不够：变异验证一次只删掉一处跳过时，必须能分辨漏的是哪一处。
type spyClient struct {
	infini.CardClient
	listCalls int
	// aliasListCalls 只数带 alias 的列卡，也就是**对账**那一步打的。
	// 与 listCalls 分开是为了让变异验证分得清删掉的是哪一处跳过：
	// 发现卡片与不确定态对账走的是同一个上游方法。
	aliasListCalls int
	batchCalls     int
	statusCalls    int
	revealCalls    int
	txCalls        int
	withdrawCalls  int
}

func (c *spyClient) ListCards(ctx context.Context, q infini.ListCardsQuery) (infini.CardPage, error) {
	c.listCalls++
	if q.Alias != "" {
		c.aliasListCalls++
	}
	return c.CardClient.ListCards(ctx, q)
}

func (c *spyClient) WithdrawStatus(ctx context.Context, requestID string) (infini.WithdrawState, error) {
	c.withdrawCalls++
	return c.CardClient.WithdrawStatus(ctx, requestID)
}

func (c *spyClient) BatchCardStatus(ctx context.Context, ids []string) (map[string]string, error) {
	c.batchCalls++
	return c.CardClient.BatchCardStatus(ctx, ids)
}

func (c *spyClient) CardStatus(ctx context.Context, id string) (infini.Card, error) {
	c.statusCalls++
	return c.CardClient.CardStatus(ctx, id)
}

func (c *spyClient) RevealCard(ctx context.Context, id string) (infini.RevealedCard, error) {
	c.revealCalls++
	return c.CardClient.RevealCard(ctx, id)
}

func (c *spyClient) CardTransactions(ctx context.Context, id string, page, size int) (infini.TransactionPage, error) {
	c.txCalls++
	return c.CardClient.CardTransactions(ctx, id, page, size)
}

func (c *spyClient) total() int {
	return c.listCalls + c.batchCalls + c.statusCalls +
		c.revealCalls + c.txCalls + c.withdrawCalls
}

const pauseReason = "上游拒绝待查 XM-CARD-VISIBILITY"

// 暂停的账号一次上游都不打，而且结果里说清为什么跳过。
//
// 夹具必须让**六处**暂停检查点都真的有活要干。上一版漏了两处：
// SyncOptions.Withdrawals 是 nil（advanceWithdrawals 第一行就 return），
// store 里也没有未收敛的操作（reconcileOperations 的循环一次都不进），
// 于是那两处的 `if paused` 整段删掉，cards 与 jobs 全量测试照样全绿。
// 其中 advanceWithdrawals 是**动钱**的那一步——一个被人为停掉的账号会继续
// 被打上游推进提现，而门禁不会有任何反应。
func TestSyncSkipsPausedAccountAndSaysSo(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()

	goodFake := infini.NewFake()
	goodFake.ActivateOnApply()
	goodFake.SeedCard(infini.Card{ID: "main-1", Alias: "M", Status: "active"})
	badFake := infini.NewFake()
	badFake.SeedCard(infini.Card{ID: "backup-1", Alias: "B", Status: "active"})

	// 让两个账号名下都有已知的卡，否则「没打上游」可能只是因为没活要干。
	if err := store.UpsertCard(ctx, "main",
		infini.Card{ID: "main-1", Status: "active"}, CardAttribution{}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCard(ctx, "backup",
		infini.Card{ID: "backup-1", Status: "active"}, CardAttribution{}); err != nil {
		t.Fatal(err)
	}

	// 两个账号各有一笔**未收敛的开卡**：不确定态对账那一步才会真的进循环。
	for _, account := range []string{"main", "backup"} {
		store.ops[account+"-op"] = Operation{
			IdempotencyKey: account + "-op", Account: account,
			Kind: OpIssue, State: StateUnknown,
			Alias: account + "-alias", StartedAt: issueNow.Add(-time.Minute),
		}
	}

	// 两个账号各有一笔**未收敛的提现**：推进提现那一步才会真的进循环。
	// 这一步会打上游推进真金白银，暂停在这里最该被守住。
	for _, account := range []string{"main", "backup"} {
		fake := goodFake
		if account == "backup" {
			fake = badFake
		}
		if _, err := fake.Withdraw(ctx, infini.WithdrawRequest{
			RequestID: account + "-w", Chain: "TRON", TokenType: "USDT", Amount: "10",
		}); err != nil {
			t.Fatal(err)
		}
		store.withdrawals[account+"-w"] = WithdrawRecord{
			RequestID: account + "-w", Account: account, Status: "pending",
			Chain: "TRON", TokenType: "USDT", Amount: "10", UpdatedAt: issueNow,
		}
	}

	store.pauses["backup"] = AccountSyncPause{
		Account: "backup", Paused: true, Reason: pauseReason,
		PausedBy: "human:ops", PausedAt: issueNow,
	}

	good := &spyClient{CardClient: goodFake}
	bad := &spyClient{CardClient: badFake}
	accounts := []Account{
		{ID: "backup", Client: bad},
		{ID: "main", Client: good},
	}
	syncer := NewSyncer(accounts, store, SyncOptions{
		UnknownGrace:     30 * time.Minute,
		SyncTransactions: true,
		Withdrawals:      NewWithdrawService(accounts, store, func() time.Time { return issueNow }),
		Now:              func() time.Time { return issueNow },
	})

	round, err := syncer.RunOnce(ctx)
	if err != nil {
		t.Fatalf("暂停不是失败，不该让作业变红: %v", err)
	}

	// 逐个计数器分别断言：一次只删一处跳过时才分得清漏了哪一处。
	// 六处检查点 ↔ 六个计数器，一一对应。
	for _, tc := range []struct {
		what string
		got  int
	}{
		{"列卡（发现卡片）", bad.listCalls},
		{"按 alias 查卡（不确定态对账）", bad.aliasListCalls},
		{"批量查状态", bad.batchCalls},
		{"逐张查状态", bad.statusCalls},
		{"拉明文", bad.revealCalls},
		{"查流水", bad.txCalls},
		{"查提现状态（动钱的那一步）", bad.withdrawCalls},
	} {
		if tc.got != 0 {
			t.Errorf("暂停账号不该%s, got %d", tc.what, tc.got)
		}
	}

	// 正向对照：上面每一条都必须有一个「没暂停就会打」的对照，
	// 否则它们全是恒真的——夹具里根本没有那件活要干时，0 次调用不说明任何事。
	for _, tc := range []struct {
		what string
		got  int
	}{
		{"列卡（发现卡片）", good.listCalls},
		{"按 alias 查卡（不确定态对账）", good.aliasListCalls},
		{"查提现状态（动钱的那一步）", good.withdrawCalls},
		{"拉明文", good.revealCalls},
		{"查流水", good.txCalls},
	} {
		if tc.got == 0 {
			t.Errorf("未暂停的账号应照常%s，否则对应的缺席断言是恒真的", tc.what)
		}
	}
	if good.batchCalls+good.statusCalls == 0 {
		t.Error("未暂停的账号应照常查卡状态，否则对应的缺席断言是恒真的")
	}

	var sawSkip bool
	for _, s := range round.Steps {
		if s.Account != "backup" {
			continue
		}
		if !s.Skipped {
			t.Fatalf("暂停账号的步骤应标为 skipped: %+v", s)
		}
		sawSkip = true
		if !strings.Contains(s.SkipReason, "已暂停") {
			t.Fatalf("跳过原因要是中文的「已暂停」，实际 %q", s.SkipReason)
		}
		if !strings.Contains(s.SkipReason, pauseReason) {
			t.Fatalf("跳过原因要带上运营填的理由原文，实际 %q", s.SkipReason)
		}
	}
	if !sawSkip {
		t.Fatal("「跳过了」这件事必须在结果里看得见，不能静默")
	}
	if got := round.PausedAccountIDs(); len(got) != 1 || got[0] != "backup" {
		t.Fatalf("PausedAccountIDs = %v, want [backup]", got)
	}
}

// 开关读不出来时整轮 fail closed，不回落到「当作没暂停」。
//
// 回落会在一次数据库抖动里重新把请求打向一个被人为停掉的上游，
// 而且这件事在任何地方都看不见。
func TestSyncFailsClosedWhenPauseSwitchUnreadable(t *testing.T) {
	store := newMemStore()
	store.pauseReadErr = errors.New("boom")
	spy := &spyClient{CardClient: infini.NewFake()}

	round, err := newSyncer(spy, store, issueNow).RunOnce(context.Background())
	if err == nil {
		t.Fatal("读不到暂停开关必须报错，不能默认为未暂停")
	}
	if spy.total() != 0 {
		t.Fatalf("判断都还没做出来就不该打上游, got %d 次", spy.total())
	}
	if !round.AllFailed() {
		t.Fatal("这一轮什么都没做成")
	}
}

// ---------- Action ----------

func TestAccountSyncPauseDefinitionIsValid(t *testing.T) {
	for name, def := range map[string]action.Definition{
		"pause":  accountSyncPauseDef(testAccounts),
		"resume": accountSyncResumeDef(testAccounts),
	} {
		if err := def.Validate(); err != nil {
			t.Fatalf("%s 声明非法: %v", name, err)
		}
		if def.Permission != PermissionManage {
			t.Fatalf("%s 权限 = %q, want %q", name, def.Permission, PermissionManage)
		}
		for _, pt := range def.PrincipalTypes {
			if pt != principal.TypeHuman {
				t.Fatalf("%s 不该允许 %q 身份", name, pt)
			}
		}
		// **不抬级**：L2 及以上会把 params 冻进审批单给第二个人看，
		// 而 reason 是运营手打的自由文本——抬级正是「粘贴进来的凭据被冻进
		// 审批单」的那条路径。而且需要审批的暂停开关是自相矛盾的：
		// 人去够它的时刻正是「现在就出事了」。
		if def.RiskLevel != action.L1 {
			t.Fatalf("%s 风险等级 = %v, want L1", name, def.RiskLevel)
		}
		if def.RiskLevel.RequiresAdvancedControls() {
			t.Fatalf("%s 的风险等级会让它在事故当中跑不起来", name)
		}
	}
}

// Schema 是白名单语义：未声明的字段一律拒绝，防参数偷渡。
func TestAccountSyncPauseSchemaRejectsUnknownField(t *testing.T) {
	params := map[string]any{"account": testAccount, "reason": pauseReason, "api_token": "x"}
	err := accountSyncPauseDef(testAccounts).Schema.Validate(params)
	if !errors.Is(err, action.ErrUnknownField) {
		t.Fatalf("错误 = %v, want ErrUnknownField", err)
	}
}

// reason 不是装饰：暂停会让这个账号的数据按设计停止刷新，
// 「为什么停」是下一个接手的人唯一能依据的东西。
func TestAccountSyncPauseSchemaRequiresReason(t *testing.T) {
	params := map[string]any{"account": testAccount}
	err := accountSyncPauseDef(testAccounts).Schema.Validate(params)
	if !errors.Is(err, action.ErrRequiredField) {
		t.Fatalf("缺 reason 必须被拒, err = %v", err)
	}
}

func TestAccountSyncPauseHandlerWritesAndResumeClears(t *testing.T) {
	store := newMemStore()
	svc := newService(infini.NewFake(), store)
	ctx := principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "human:ops", Type: principal.TypeHuman,
	})

	summary, err := accountSyncPauseHandler(svc)(ctx, map[string]any{
		"account": testAccount, "reason": pauseReason,
	})
	if err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	m, ok := summary.(map[string]any)
	if !ok {
		t.Fatalf("摘要形状不对: %T", summary)
	}
	if m["paused"] != true {
		t.Fatalf("摘要应记 paused=true: %+v", m)
	}
	if m["reason"] != pauseReason {
		t.Fatalf("摘要应记下运营填的理由: %+v", m)
	}
	// 审计是 append-only：这两个动作作用于账号，一个卡片字段都不该进摘要。
	for _, forbidden := range []string{"card_id", "pan", "cvv", "mask"} {
		if _, present := m[forbidden]; present {
			t.Fatalf("审计摘要不该含 %q: %+v", forbidden, m)
		}
	}

	paused, err := svc.PausedAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paused[testAccount]; !ok {
		t.Fatalf("暂停后应能读到, got %v", paused)
	}

	if _, err := accountSyncResumeHandler(svc)(ctx, map[string]any{"account": testAccount}); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	paused, err = svc.PausedAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paused[testAccount]; ok {
		t.Fatalf("恢复后不该再出现在暂停清单里, got %v", paused)
	}
}

// 未配置的账号不能建暂停行：账号清单变化之后会留下一行永远不参与判断的
// 垃圾，而运营看着后台以为自己已经停掉了某个东西。
func TestAccountSyncPauseRejectsUnknownAccount(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	if _, err := svc.SetAccountSyncPause(context.Background(), "nope", true, pauseReason); err == nil {
		t.Fatal("未配置的账号应被拒")
	}
}

// 两个 Action 必须真的注册进去，否则后台点不到。
func TestAccountSyncActionsAreRegistered(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	for _, id := range []string{ActionAccountSyncPause, ActionAccountSyncResume} {
		if _, _, ok := reg.Lookup(id, actionVersion); !ok {
			t.Fatalf("%s 没有注册进 Action registry", id)
		}
	}
}
