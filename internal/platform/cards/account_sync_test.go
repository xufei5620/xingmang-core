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
	listCalls   int
	batchCalls  int
	statusCalls int
	revealCalls int
	txCalls     int
}

func (c *spyClient) ListCards(ctx context.Context, q infini.ListCardsQuery) (infini.CardPage, error) {
	c.listCalls++
	return c.CardClient.ListCards(ctx, q)
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
	return c.listCalls + c.batchCalls + c.statusCalls + c.revealCalls + c.txCalls
}

const pauseReason = "上游拒绝待查 XM-CARD-VISIBILITY"

// 暂停的账号一次上游都不打，而且结果里说清为什么跳过。
func TestSyncSkipsPausedAccountAndSaysSo(t *testing.T) {
	store := newMemStore()

	goodFake := infini.NewFake()
	goodFake.ActivateOnApply()
	goodFake.SeedCard(infini.Card{ID: "main-1", Alias: "M", Status: "active"})
	badFake := infini.NewFake()
	badFake.SeedCard(infini.Card{ID: "backup-1", Alias: "B", Status: "active"})

	// 让两个账号名下都有已知的卡，否则「没打上游」可能只是因为没活要干。
	if err := store.UpsertCard(context.Background(), "main",
		infini.Card{ID: "main-1", Status: "active"}, CardAttribution{}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCard(context.Background(), "backup",
		infini.Card{ID: "backup-1", Status: "active"}, CardAttribution{}); err != nil {
		t.Fatal(err)
	}
	store.pauses["backup"] = AccountSyncPause{
		Account: "backup", Paused: true, Reason: pauseReason,
		PausedBy: "human:ops", PausedAt: issueNow,
	}

	good := &spyClient{CardClient: goodFake}
	bad := &spyClient{CardClient: badFake}
	syncer := NewSyncer([]Account{
		{ID: "backup", Client: bad},
		{ID: "main", Client: good},
	}, store, SyncOptions{
		UnknownGrace:     30 * time.Minute,
		SyncTransactions: true,
		Now:              func() time.Time { return issueNow },
	})

	round, err := syncer.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("暂停不是失败，不该让作业变红: %v", err)
	}

	// 五个计数器分别断言：一次只删一处跳过时才分得清漏了哪一处。
	if bad.listCalls != 0 {
		t.Fatalf("暂停账号不该列卡, got %d", bad.listCalls)
	}
	if bad.batchCalls != 0 {
		t.Fatalf("暂停账号不该批量查状态, got %d", bad.batchCalls)
	}
	if bad.statusCalls != 0 {
		t.Fatalf("暂停账号不该逐张查状态, got %d", bad.statusCalls)
	}
	if bad.revealCalls != 0 {
		t.Fatalf("暂停账号不该拉明文, got %d", bad.revealCalls)
	}
	if bad.txCalls != 0 {
		t.Fatalf("暂停账号不该查流水, got %d", bad.txCalls)
	}
	// 正向对照：不是整轮什么都没干（否则上面五条是恒真的）。
	if good.total() == 0 {
		t.Fatal("未暂停的账号应照常同步")
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
