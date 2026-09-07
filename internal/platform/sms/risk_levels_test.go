package sms

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 本文件钉住 XM-RISK-RESTORE 在接码这一侧改了什么：向供应商付费换资源的三个
// 动作从「有权限就能直接跑」变成「先落一张审批单」，而按设计给机器身份用的
// 要号仍然一步执行完。
//
// 断言的是**行为**不是字面等级：同一次调用，看它被执行了还是被受理成一张单。

// smsFakeApprovals 是审批中心在内核这一侧的替身，只记录「落了哪些单」。
type smsFakeApprovals struct{ submitted []action.ApprovalSubmission }

func (g *smsFakeApprovals) Submit(_ context.Context, in action.ApprovalSubmission) (string, error) {
	g.submitted = append(g.submitted, in)
	return fmt.Sprintf("appr-%d", len(g.submitted)), nil
}

func (g *smsFakeApprovals) Peek(_ context.Context, id string) (action.ApprovalClaim, error) {
	return action.ApprovalClaim{}, fmt.Errorf("审批单 %s 不存在", id)
}

func (g *smsFakeApprovals) Claim(_ context.Context, _ string, _ uuid.UUID, _ map[string]any) error {
	return nil
}

type smsNopRunStore struct{}

func (smsNopRunStore) InsertRun(context.Context, action.Run) error { return nil }

func smsBuyer() principal.Principal {
	return principal.Principal{
		ID: "ops-1", Type: principal.TypeHuman, Issuer: "test",
		Environment: "development", Scopes: []string{PermissionPurchase},
	}
}

// smsMachine 是 XM-SMS4 那条无人值守的消费者链路上的机器身份。
func smsMachine() principal.Principal {
	return principal.Principal{
		ID: "svc-bot", Type: principal.TypeService, Issuer: "test",
		Environment: "development", Scopes: []string{PermissionPurchase},
	}
}

func smsKernel(t *testing.T, svc *Service) (*action.Kernel, *smsFakeApprovals) {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	gw := &smsFakeApprovals{}
	return action.NewKernel(reg, smsNopRunStore{}, action.WithApprovalGateway(gw)), gw
}

// 花真钱且只给人的三个采购动作，经过内核时被受理成审批单而不是执行。
//
// 三个一起点名，是因为它们分散在 actions.go 与 actions_extras.go 两个文件里，
// 而本仓库栽过「只改撞见的那一处」的跟头。
func TestHumanPurchaseActionsLandAsApprovalRequests(t *testing.T) {
	store := newMemStore()
	svc := newRoutedService(t, &fakeAdapter{catalog: goods62()}, heroOK(), store)
	k, gw := smsKernel(t, svc)

	cases := []struct {
		name string
		req  action.Request
	}{
		{
			name: "买号",
			req: action.Request{
				ActionID: ActionNumberPurchase, ActionVersion: actionVersion,
				RequestID: "req-buy", Reason: "给新一批账号注册用",
				Params: map[string]any{
					"provider": ProviderHero, "operation_id": "op-buy-1",
					"quantity": 1, "service": "google", "country": 12,
				},
			},
		},
		{
			name: "租号",
			req: action.Request{
				ActionID: ActionRentPurchase, ActionVersion: actionVersion,
				RequestID: "req-rent", Reason: "长期接码要一个可续的号",
				Params: map[string]any{
					"provider": ProviderHero, "operation_id": "op-rent-1",
					"service": "google", "country": 12, "duration_hours": 4,
				},
			},
		},
		{
			name: "买邮箱",
			req: action.Request{
				ActionID: ActionEmailPurchase, ActionVersion: actionVersion,
				RequestID: "req-mail", Reason: "注册需要一个可收信的邮箱",
				Params: map[string]any{
					"provider": ProviderHero, "operation_id": "op-mail-1",
					"site": "example.com", "domain": "example.com",
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(gw.submitted)

			_, err := k.Execute(principal.WithPrincipal(context.Background(), smsBuyer()), c.req)

			if code := action.ErrorCode(err); code != action.CodeApprovalRequired {
				t.Fatalf("%s 应当被受理成审批单，got %q（%v）", c.name, code, err)
			}
			if len(gw.submitted) != before+1 {
				t.Fatalf("%s 没有真的落单：submitted %d -> %d", c.name, before, len(gw.submitted))
			}
			sub := gw.submitted[len(gw.submitted)-1]
			if sub.ActionID != c.req.ActionID || sub.Reason != c.req.Reason {
				t.Fatalf("%s 单上冻结的内容不对：%+v", c.name, sub)
			}
		})
	}
}

// **要号仍然一步执行完**，尽管它和买号花的是同一笔钱、用的是同一把权限。
//
// 这条是本片最要紧的一条反面用例：要号按设计对机器身份开放（XM-SMS4），
// 而机器凑不出审批人——把它一起抬到 L2 会让无人值守的要号链路整条停摆，
// 那是「顺手改过头」最容易造成的事故。这里用机器身份发起，断言它真的跑完了。
func TestMachineNumberRequestStillExecutesWithoutApproval(t *testing.T) {
	store := newMemStore()
	svc := newRoutedService(t, &fakeAdapter{catalog: goods62(), purchaseErr: rejectedUpstream()}, heroOK(), store)

	// 先给这台机器登记配额——那正是这条链路上花钱的闸：未登记的机器一次都
	// 要不到号（TestRequestNumberRejectsUnregisteredMachine 钉住了这一点）。
	// 要号不走审批，靠的就是这道闸，所以用例里必须把它摆上。
	if _, err := svc.SetConsumerQuota(context.Background(), ConsumerQuota{
		Consumer: "svc-bot", DailyRequests: 10, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	k, gw := smsKernel(t, svc)

	res, err := k.Execute(principal.WithPrincipal(context.Background(), smsMachine()), action.Request{
		ActionID: ActionNumberRequest, ActionVersion: actionVersion,
		RequestID: "req-a",
		Params: map[string]any{
			"request_id": "req-a", "service": "google", "country": "12", "quantity": 1,
		},
	})
	if err != nil {
		t.Fatalf("机器要号应当直接执行: %v", err)
	}
	if len(gw.submitted) != 0 {
		t.Fatalf("要号不该落审批单，got %+v", gw.submitted)
	}
	// 真的跑到了 Handler，而不是「没落单」这一件事本身成立就算过。
	result, ok := res.Value.(map[string]any)
	if !ok || result["state"] != string(StateSucceeded) {
		t.Fatalf("要号应当跑出结果，got %+v", res.Value)
	}
}

// 配置类的动作没被本片碰过，仍然一步执行完。
//
// 它回答的是「有没有改过头」：恢复等级时把整个模块抬上去，会让改一条路由
// 规则也要等人批。用真的执行成功来断言，而不只是「没落单」。
func TestRoutingRuleStillExecutesWithoutApproval(t *testing.T) {
	store := newMemStore()
	svc := newRoutedService(t, &fakeAdapter{catalog: goods62()}, heroOK(), store)
	k, gw := smsKernel(t, svc)

	manager := principal.Principal{
		ID: "ops-1", Type: principal.TypeHuman, Issuer: "test",
		Environment: "development", Scopes: []string{PermissionManage},
	}
	_, err := k.Execute(principal.WithPrincipal(context.Background(), manager), action.Request{
		ActionID: ActionRoutingSet, ActionVersion: actionVersion,
		RequestID: "req-routing",
		Params: map[string]any{
			"service": "google", "country": "12",
			"providers": []string{ProviderHero},
		},
	})
	if err != nil {
		t.Fatalf("改路由规则应当直接执行: %v", err)
	}
	if len(gw.submitted) != 0 {
		t.Fatalf("路由规则不该落审批单，got %+v", gw.submitted)
	}
}
