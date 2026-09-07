package cards

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 审计贡献只有在**经过内核**时才会发生（Handler 用 action.RecordResource
// 等函数往 ctx 里塞，内核取走）。直接调 Handler 测不到这一层，
// 所以这些用例走真的 Kernel + 捕获审计的 Sink。

type capturingSink struct{ events []action.AuditEvent }

func (s *capturingSink) Append(ctx context.Context, e action.AuditEvent) error {
	s.events = append(s.events, e)
	return nil
}

type nopRunStore struct{}

func (nopRunStore) InsertRun(ctx context.Context, r action.Run) error { return nil }

func kernelWith(t *testing.T, svc *Service) (*action.Kernel, *capturingSink) {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	sink := &capturingSink{}
	return action.NewKernel(reg, nopRunStore{}, action.WithAuditSink(sink)), sink
}

// kernelWithApprovals 是接了审批中心替身的内核。
//
// 开卡与关停恢复成 L2、提现恢复成 L3 之后（XM-RISK-RESTORE），它们不再能一
// 步执行完；要测 Handler 的审计贡献就得走完「落单 → 批准 → 执行」这条路。
func kernelWithApprovals(t *testing.T, svc *Service) (*action.Kernel, *capturingSink, *fakeApprovals) {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	sink := &capturingSink{}
	gw := &fakeApprovals{claims: map[string]action.ApprovalClaim{}}
	k := action.NewKernel(reg, nopRunStore{},
		action.WithAuditSink(sink), action.WithApprovalGateway(gw))
	return k, sink, gw
}

// fakeApprovals 是审批中心在内核这一侧那三个方法的替身：**批准是无条件的**。
//
// 它替代不了 approval 包的策略（票数、自批限制、有效期都在那边，由
// approval 自己的用例覆盖）；这里只需要「单能落下、能取回、能占用」，
// 好让 Handler 侧的用例走到执行那一步。
type fakeApprovals struct {
	submitted []action.ApprovalSubmission
	claims    map[string]action.ApprovalClaim
	claimed   []string
}

func (g *fakeApprovals) Submit(_ context.Context, in action.ApprovalSubmission) (string, error) {
	id := fmt.Sprintf("appr-%d", len(g.submitted)+1)
	g.submitted = append(g.submitted, in)
	g.claims[id] = action.ApprovalClaim{
		ActionID: in.ActionID, ActionVersion: in.ActionVersion,
		RiskLevel: in.RiskLevel, Params: in.Params,
		Reason: in.Reason, RequesterID: in.Requester.ID,
	}
	return id, nil
}

func (g *fakeApprovals) Peek(_ context.Context, id string) (action.ApprovalClaim, error) {
	c, ok := g.claims[id]
	if !ok {
		return action.ApprovalClaim{}, fmt.Errorf("审批单 %s 不存在", id)
	}
	return c, nil
}

func (g *fakeApprovals) Claim(_ context.Context, id string, _ uuid.UUID, _ map[string]any) error {
	g.claimed = append(g.claimed, id)
	return nil
}

// runThroughApproval 走完「落单 → 执行」并把落单那一段的审计事件清掉，
// 让调用方的断言对着**执行**那一条，与恢复等级之前的写法保持一致。
func runThroughApproval(
	t *testing.T, k *action.Kernel, sink *capturingSink, ctx context.Context, req action.Request,
) (action.Result, error) {
	t.Helper()
	_, err := k.Execute(ctx, req)
	if code := action.ErrorCode(err); code != action.CodeApprovalRequired {
		t.Fatalf("L2+ 的调用应当被受理成审批单，got %q（%v）", code, err)
	}
	var ae *action.Error
	if !errors.As(err, &ae) || ae.ApprovalRequestID == "" {
		t.Fatalf("错误里必须带单号：%v", err)
	}
	sink.events = nil
	return k.ExecuteApproved(ctx, ae.ApprovalRequestID, req.RequestID, nil)
}

// ctxAs 把身份放进 ctx——内核从 ctx 取 Principal，Request 里没有这个字段。
func ctxAs(p principal.Principal) context.Context {
	return principal.WithPrincipal(context.Background(), p)
}

func operator() principal.Principal {
	return principal.Principal{
		ID:          "ops-1",
		Type:        principal.TypeHuman,
		Issuer:      "test",
		Environment: "development",
		Scopes:      []string{PermissionIssue, PermissionReveal, PermissionManage},
	}
}

// 开卡必须在审计里指明作用于哪张卡——审计事件只记「谁执行了 cards.card.issue」
// 而不记资源 id 的话，出事时无法从审计定位到具体的卡。
func TestIssueThroughKernelRecordsResource(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	k, sink, _ := kernelWithApprovals(t, svc)

	res, err := runThroughApproval(t, k, sink, ctxAs(operator()), action.Request{
		ActionID:      ActionIssue,
		ActionVersion: actionVersion,
		RequestID:     "req-test",
		Reason:        "给新同事开一张订阅卡",
		Params:        validIssueParams(),
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	_ = res

	if len(sink.events) != 1 {
		t.Fatalf("应写 1 条审计, got %d", len(sink.events))
	}
	e := sink.events[0]
	if e.ResourceType == "" {
		t.Fatal("审计缺少 resource_type")
	}
	if e.ResourceID == "" {
		t.Fatal("审计缺少 resource_id——出事时无法定位到具体的卡")
	}
	if !e.Succeeded {
		t.Fatal("成功的开卡应记成功")
	}
}

// 审计是 append-only，写进去删不掉。持卡人姓名与邮箱不该进去：
// 「谁开的卡」由 PrincipalID 回答，不需要重复个人信息。
func TestIssueAuditOmitsPersonalData(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	k, sink, _ := kernelWithApprovals(t, svc)

	if _, err := runThroughApproval(t, k, sink, ctxAs(operator()), action.Request{
		ActionID:      ActionIssue,
		ActionVersion: actionVersion,
		RequestID:     "req-test",
		Reason:        "给新同事开一张订阅卡",
		Params:        validIssueParams(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, v := range sink.events[0].AfterSummary {
		if s, ok := v.(string); ok {
			if strings.Contains(s, "example.com") || strings.Contains(s, "ZHANG WEI") {
				t.Fatalf("审计摘要泄漏个人信息: %q", s)
			}
		}
	}
}

// reveal 是整个功能里最该留痕的动作：卡被盗刷时要能回答
// 「上一次是谁、什么时候看了这张卡的明文」。
func TestRevealThroughKernelIsAudited(t *testing.T) {
	fake := infini.NewFake()
	store := newMemStore()
	svc := newService(fake, store)

	issued, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	k, sink := kernelWith(t, svc)
	if _, err := k.Execute(ctxAs(operator()), action.Request{
		ActionID:      ActionReveal,
		ActionVersion: actionVersion,
		RequestID:     "req-test",
		Params:        map[string]any{"account": testAccount, "card_id": issued.CardID},
	}); err != nil {
		t.Fatalf("reveal 执行失败: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("reveal 必须留下审计, got %d 条", len(sink.events))
	}
	if sink.events[0].ResourceID != issued.CardID {
		t.Fatalf("审计的 resource_id = %q, want %q", sink.events[0].ResourceID, issued.CardID)
	}
	if sink.events[0].PrincipalID != "ops-1" {
		t.Fatalf("审计应记下是谁看的, got %q", sink.events[0].PrincipalID)
	}
}

// 最要紧的一条：明文卡面数据一个字都不能进审计。
// 审计会被归档、会被导出、会被搜索。
func TestRevealAuditNeverContainsPlaintext(t *testing.T) {
	fake := infini.NewFake()
	svc := newService(fake, newMemStore())
	issued, _ := svc.IssueCard(context.Background(), issueReq())

	revealed, err := svc.RevealCard(context.Background(), testAccount, issued.CardID)
	if err != nil {
		t.Fatal(err)
	}

	k, sink := kernelWith(t, svc)
	if _, err := k.Execute(ctxAs(operator()), action.Request{
		ActionID:      ActionReveal,
		ActionVersion: actionVersion,
		RequestID:     "req-test",
		Params:        map[string]any{"account": testAccount, "card_id": issued.CardID},
	}); err != nil {
		t.Fatal(err)
	}

	e := sink.events[0]
	for name, summary := range map[string]map[string]any{
		"before": e.BeforeSummary,
		"after":  e.AfterSummary,
	} {
		for key, v := range summary {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, revealed.Number) || strings.Contains(s, revealed.CVV) ||
				strings.Contains(s, revealed.ExpiryMMYY) {
				t.Fatalf("%s 摘要的 %q 字段泄漏了卡面明文: %q", name, key, s)
			}
		}
	}
}

// 没有 card.reveal 权限的人拿不到明文，哪怕他能开卡。
// 这是「对内全量、对外受限」在权限层的落点。
func TestRevealRequiresDedicatedPermission(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	issued, _ := svc.IssueCard(context.Background(), issueReq())

	k, _ := kernelWith(t, svc)
	issuerOnly := operator()
	issuerOnly.Scopes = []string{PermissionIssue}

	_, err := k.Execute(ctxAs(issuerOnly), action.Request{
		ActionID:      ActionReveal,
		ActionVersion: actionVersion,
		RequestID:     "req-test",
		Params:        map[string]any{"account": testAccount, "card_id": issued.CardID},
	})
	if err == nil {
		t.Fatal("没有 card.reveal 权限必须被拒")
	}
}

// newRegistryWith 组一个注册表，供多处测试查声明。
func newRegistryWith(t *testing.T, svc *Service) *action.Registry {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	return reg
}
