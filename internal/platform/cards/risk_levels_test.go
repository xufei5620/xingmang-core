package cards

import (
	"context"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 本文件钉住 XM-RISK-RESTORE 改变了什么：开卡、关停与提现从「有权限就能直接
// 跑」变成「先落一张审批单」。
//
// 断言的是**行为**而不是字面等级。写死 `RiskLevel == "L2"` 的测试证明不了
// 任何事——它只是把常量抄了一遍，等级表下次演进时还会挡路；这里问的是同一次
// 调用到底被执行了，还是被受理成一张单。

// fundOperator 持提现权限（fund-operator 角色），不持 card.* 那几把。
func fundOperator() principal.Principal {
	return principal.Principal{
		ID:          "ops-1",
		Type:        principal.TypeHuman,
		Issuer:      "test",
		Environment: "development",
		Scopes:      []string{PermissionWithdraw},
	}
}

// registryWithWithdraw 把卡片与提现两组 Action 都登记进来。
//
// 提现的 service/store 传 nil：本文件的用例全部止步于风险闸，够不到 Handler。
func registryWithWithdraw(t *testing.T, svc *Service) *action.Registry {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	if err := RegisterWithdrawActions(reg, nil, nil, nil, svc.AccountIDs()); err != nil {
		t.Fatal(err)
	}
	return reg
}

func withdrawParams() map[string]any {
	return map[string]any{
		"account":    testAccount,
		"request_id": "wd-1",
		"chain":      "TRON",
		"token_type": "USDT",
		"amount":     "25",
		"address_id": "addr-1",
	}
}

// 动钱且不可逆的三个动作，经过接了审批中心的内核时被受理成审批单而不是执行。
//
// 三个一起测是刻意的：本仓库栽过「只修撞见的那一处，把两行之外的另一处漏到
// 下一片」的跟头，所以这条用例把本片声称恢复的每一个都点名。
func TestMoneyMovingCardActionsLandAsApprovalRequests(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	reg := registryWithWithdraw(t, svc)
	gw := &fakeApprovals{claims: map[string]action.ApprovalClaim{}}
	k := action.NewKernel(reg, nopRunStore{}, action.WithApprovalGateway(gw))

	cases := []struct {
		name   string
		who    principal.Principal
		req    action.Request
		params map[string]any
	}{
		{
			name: "开卡",
			who:  operator(),
			req: action.Request{
				ActionID: ActionIssue, ActionVersion: actionVersion,
				RequestID: "req-issue", Reason: "给新同事开一张订阅卡",
				Params: validIssueParams(),
			},
		},
		{
			name: "关停",
			who:  operator(),
			req: action.Request{
				ActionID: ActionDelete, ActionVersion: actionVersion,
				RequestID: "req-delete", Reason: "这张卡不再使用，结清余额后关停",
				Params: map[string]any{
					"account": testAccount, "idempotency_key": "del-1", "card_id": "card-1",
				},
			},
		},
		{
			name: "提现",
			who:  fundOperator(),
			req: action.Request{
				ActionID: ActionWithdraw, ActionVersion: actionVersion,
				RequestID: "req-withdraw", Reason: "把结余提回公司钱包",
				Params: withdrawParams(),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(gw.submitted)

			_, err := k.Execute(ctxAs(c.who), c.req)

			if code := action.ErrorCode(err); code != action.CodeApprovalRequired {
				t.Fatalf("%s 应当被受理成审批单，got %q（%v）", c.name, code, err)
			}
			if len(gw.submitted) != before+1 {
				t.Fatalf("%s 没有真的落单：submitted %d -> %d", c.name, before, len(gw.submitted))
			}
			// 单上冻结的必须是这一次调用，而不是随便一张单——否则
			// 「落了单」这个断言换成任何一个 Action 都会绿。
			sub := gw.submitted[len(gw.submitted)-1]
			if sub.ActionID != c.req.ActionID || sub.Reason != c.req.Reason {
				t.Fatalf("%s 单上冻结的内容不对：%+v", c.name, sub)
			}
			if sub.Requester.ID != c.who.ID {
				t.Fatalf("%s 单上的提交人应是发起人，got %q", c.name, sub.Requester.ID)
			}
		})
	}
}

// 反面：没被本片碰过的低风险动作仍然一步执行完，不经过审批。
//
// 这条用例存在的理由是「别改过头」——恢复等级很容易顺手把整个模块抬上去，
// 那样每看一次卡面都要走一遍审批。用 reveal 是因为它在 fake 装配下能真的跑
// 通，所以这里断言的是「执行成功了」这件正事，而不只是「没落单」。
func TestRevealStillExecutesWithoutApproval(t *testing.T) {
	fake := infini.NewFake()
	store := newMemStore()
	svc := newService(fake, store)

	issued, err := svc.IssueCard(context.Background(), issueReq())
	if err != nil {
		t.Fatal(err)
	}

	reg := registryWithWithdraw(t, svc)
	gw := &fakeApprovals{claims: map[string]action.ApprovalClaim{}}
	k := action.NewKernel(reg, nopRunStore{}, action.WithApprovalGateway(gw))

	res, err := k.Execute(ctxAs(operator()), action.Request{
		ActionID: ActionReveal, ActionVersion: actionVersion,
		RequestID: "req-reveal",
		Params:    map[string]any{"account": testAccount, "card_id": issued.CardID},
	})
	if err != nil {
		t.Fatalf("reveal 应当直接执行: %v", err)
	}
	if res.Value == nil {
		t.Fatal("reveal 应当返回卡面，got nil")
	}
	if len(gw.submitted) != 0 {
		t.Fatalf("reveal 不该落审批单，got %+v", gw.submitted)
	}
}

// 提现定 L3 而不是 L2，要的就是这条性质：**提交人自己批不了自己的提现**。
//
// 这是 L3 与 L2 的实际差别（approval.DefaultPolicy 里 L2 一票且允许自批），
// 也是 withdrawDef 那段注释当初说平台缺的那道闸。测的是策略对这张单的判断，
// 不是等级的字面值。
func TestWithdrawRequiresASecondPerson(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())
	reg := registryWithWithdraw(t, svc)
	gw := &fakeApprovals{claims: map[string]action.ApprovalClaim{}}
	k := action.NewKernel(reg, nopRunStore{}, action.WithApprovalGateway(gw))

	if _, err := k.Execute(ctxAs(fundOperator()), action.Request{
		ActionID: ActionWithdraw, ActionVersion: actionVersion,
		RequestID: "req-withdraw", Reason: "把结余提回公司钱包",
		Params: withdrawParams(),
	}); action.ErrorCode(err) != action.CodeApprovalRequired {
		t.Fatalf("提现应当落审批单，got %v", err)
	}

	sub := gw.submitted[0]
	now := time.Now()
	policy := approval.DefaultPolicy()
	req := approval.Request{
		ActionID:      sub.ActionID,
		ActionVersion: sub.ActionVersion,
		RiskLevel:     string(sub.RiskLevel),
		RequesterID:   sub.Requester.ID,
		RequesterType: sub.Requester.Type,
		Status:        approval.StatusPending,
		ExpiresAt:     now.Add(policy.TTLFor(string(sub.RiskLevel))),
	}

	// 一、提交人投不了自己这张单。
	self := voter(sub.Requester.ID)
	if err := policy.CanVote(req, self, now); err != approval.ErrApproverIsRequester {
		t.Fatalf("提交人不该能批自己的提现，got %v", err)
	}

	// 二、别人投得了——否则上一条可能只是因为这张单谁都投不了（比如过期），
	// 那样它会以错误的理由绿。
	other := voter("staff_bob")
	if err := policy.CanVote(req, other, now); err != nil {
		t.Fatalf("另一个人应当能投这张单，got %v", err)
	}

	// 三、一票还不够。L2 是一票即批，提现要的是两个人都看过。
	oneVote := []approval.Decision{{ApproverID: "staff_bob", Verdict: approval.VerdictApprove}}
	if got := policy.Settle(req, oneVote); got != approval.StatusPending {
		t.Fatalf("提现一票不该就批了，got %q", got)
	}
	twoVotes := append(oneVote, approval.Decision{ApproverID: "staff_carol", Verdict: approval.VerdictApprove})
	if got := policy.Settle(req, twoVotes); got != approval.StatusApproved {
		t.Fatalf("两票之后应当批准，got %q", got)
	}
}

func voter(id string) principal.Principal {
	return principal.Principal{
		ID: id, Type: principal.TypeHuman, Issuer: "test",
		Environment: "development", Scopes: []string{approval.ScopeDecide},
	}
}
