package action

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type recordingGateway struct {
	submissions []ApprovalSubmission
	id          string
	err         error
}

func (g *recordingGateway) Submit(_ context.Context, in ApprovalSubmission) (string, error) {
	g.submissions = append(g.submissions, in)
	if g.err != nil {
		return "", g.err
	}
	if g.id == "" {
		g.id = "approval-1"
	}
	return g.id, nil
}

func l2Definition() Definition {
	def := demoDefinition()
	def.RiskLevel = L2
	return def
}

// TestApprovalGatewayTurnsRejectionIntoRequest 钉住 XM-0030 的主路径：
// 接了审批中心之后，L2+ 不再直接拒，而是落一张待批单。
func TestApprovalGatewayTurnsRejectionIntoRequest(t *testing.T) {
	gateway := &recordingGateway{id: "req-42"}
	def := l2Definition()
	k, store := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用——审批通过之前什么都不执行")
		return nil, nil
	})
	k.approvals = gateway
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	req := okRequest()
	req.Reason = "上游换了域名，需要重新登记连接器"
	_, err := k.Execute(ctx, req)

	if ErrorCode(err) != CodeApprovalRequired {
		t.Fatalf("应当受理为审批单，got %v", err)
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.ApprovalRequestID != "req-42" {
		t.Fatalf("错误里必须带单号供调用方跟进，got %+v", ae)
	}
	if len(gateway.submissions) != 1 {
		t.Fatalf("应当只落一张单，got %d", len(gateway.submissions))
	}
	got := gateway.submissions[0]
	if got.RiskLevel != L2 || got.Reason != req.Reason || got.Requester.ID == "" {
		t.Fatalf("提交内容不完整：%+v", got)
	}
	// 参数要原样冻结——审批中心据此算 params_hash。
	if len(got.Params) != len(req.Params) {
		t.Fatalf("参数没有原样带过去：%+v vs %+v", got.Params, req.Params)
	}
	// 落单也要留痕，否则「谁请求过什么」在审计里是空白。
	if len(store.runs) != 1 || store.runs[0].ErrorCode != CodeApprovalRequired {
		t.Fatalf("落单必须留痕：%+v", store.runs)
	}
}

// TestWithoutGatewayL2StillFailsClosed：没接审批中心时行为一个字节不变。
// 这是 Foundation-A 的既有保证，不能因为加了 XM-0030 就悄悄放行。
func TestWithoutGatewayL2StillFailsClosed(t *testing.T) {
	for _, lvl := range []RiskLevel{L2, L3, L4} {
		def := demoDefinition()
		def.RiskLevel = lvl
		k, _ := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
			t.Fatal("Handler 不该被调用")
			return nil, nil
		})
		ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
		req := okRequest()
		req.Reason = "给了理由也不行——没有审批中心就没有通道"
		if _, err := k.Execute(ctx, req); ErrorCode(err) != CodeAdvancedControlsRequired {
			t.Fatalf("%s 没接审批中心时必须 fail closed，got %v", lvl, err)
		}
	}
}

// TestApprovalRequiresReason：理由是审计要求，缺了要在内核就拒，
// 而不是落一张没人看得懂的空理由待批单。
func TestApprovalRequiresReason(t *testing.T) {
	gateway := &recordingGateway{}
	k, _ := newTestKernel(t, l2Definition(), func(context.Context, map[string]any) (any, error) {
		return nil, nil
	})
	k.approvals = gateway
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	for _, reason := range []string{"", "   ", "\t\n"} {
		req := okRequest()
		req.Reason = reason
		if _, err := k.Execute(ctx, req); ErrorCode(err) != CodeInvalidParams {
			t.Fatalf("理由为 %q 时应当 INVALID_PARAMS，got %v", reason, err)
		}
	}
	if len(gateway.submissions) != 0 {
		t.Fatalf("没有理由不该落单，却落了 %d 张", len(gateway.submissions))
	}
}

// TestUnauthorizedCallerCannotOpenApproval 是这一片**改了校验顺序的理由**。
//
// 风险闸原本排在环境/权限/Schema 之前，于是一个没有权限的人对 L2 Action
// 发起调用，会先撞上风险闸。接了审批中心之后那就变成「单已建立」——等于
// 谁都能往审批队列里灌单。挪到之后，他拿到的是 PERMISSION_DENIED。
//
// 这条断言的形状是「本该被拒的确实被拒」，容易恒真，所以每个子用例都配了
// 一个只差那一项的对照组：把缺的那一项补上，同一次调用就应当变成落单。
func TestUnauthorizedCallerCannotOpenApproval(t *testing.T) {
	newKernel := func(t *testing.T) (*Kernel, *recordingGateway) {
		t.Helper()
		gateway := &recordingGateway{}
		k, _ := newTestKernel(t, l2Definition(), func(context.Context, map[string]any) (any, error) {
			t.Fatal("Handler 不该被调用")
			return nil, nil
		})
		k.approvals = gateway
		return k, gateway
	}

	t.Run("缺权限拿 PERMISSION_DENIED 而不是落单", func(t *testing.T) {
		k, gateway := newKernel(t)
		ctx := principal.WithPrincipal(context.Background(), testPrincipal("some.other.scope"))
		req := okRequest()
		req.Reason = "理由齐全，但这个人没有权限"
		if _, err := k.Execute(ctx, req); ErrorCode(err) != CodePermissionDenied {
			t.Fatalf("got %v", err)
		}
		if len(gateway.submissions) != 0 {
			t.Fatalf("没权限的人往审批队列里灌进了 %d 张单", len(gateway.submissions))
		}
		// 对照：补上权限，同一次调用应当落单——确认上面拒的是权限。
		k2, gw2 := newKernel(t)
		ctx2 := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
		if _, err := k2.Execute(ctx2, req); ErrorCode(err) != CodeApprovalRequired {
			t.Fatalf("补上权限后应当落单，got %v", err)
		}
		if len(gw2.submissions) != 1 {
			t.Fatalf("补上权限后应当落一张单，got %d", len(gw2.submissions))
		}
	})

	t.Run("参数不合 Schema 拿 INVALID_PARAMS 而不是落单", func(t *testing.T) {
		k, gateway := newKernel(t)
		ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
		req := okRequest()
		req.Reason = "理由齐全，但参数不合 Schema"
		req.Params = map[string]any{"不存在的字段": 1}
		if _, err := k.Execute(ctx, req); ErrorCode(err) != CodeInvalidParams {
			t.Fatalf("got %v", err)
		}
		if len(gateway.submissions) != 0 {
			t.Fatalf("参数非法却落了 %d 张单——审批人会被迫替内核做校验", len(gateway.submissions))
		}
	})
}

// TestApprovalSubmitFailureIsInternalNotSilentSuccess：审批中心落单失败时，
// 内核必须报错，绝不能当作「已受理」放过去——那会让调用方以为在等审批，
// 实际上没有任何人会看到这张单。
func TestApprovalSubmitFailureIsInternalNotSilentSuccess(t *testing.T) {
	gateway := &recordingGateway{err: errors.New("数据库挂了")}
	k, store := newTestKernel(t, l2Definition(), func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用")
		return nil, nil
	})
	k.approvals = gateway
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
	req := okRequest()
	req.Reason = "落单会失败"

	if _, err := k.Execute(ctx, req); ErrorCode(err) != CodeInternal {
		t.Fatalf("落单失败必须报错，got %v", err)
	}
	if len(store.runs) != 1 || store.runs[0].ErrorCode != CodeInternal {
		t.Fatalf("落单失败也要留痕：%+v", store.runs)
	}
}

// TestLowRiskUnaffectedByGateway：接了审批中心不影响 L0/L1——它们照常直接执行，
// 也不要求 reason。
func TestLowRiskUnaffectedByGateway(t *testing.T) {
	for _, lvl := range []RiskLevel{L0, L1} {
		gateway := &recordingGateway{}
		def := demoDefinition()
		def.RiskLevel = lvl
		called := false
		k, _ := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
			called = true
			return "ok", nil
		})
		k.approvals = gateway
		ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

		if _, err := k.Execute(ctx, okRequest()); err != nil {
			t.Fatalf("%s 不该受审批影响，got %v", lvl, err)
		}
		if !called {
			t.Fatalf("%s 的 Handler 应当被调用", lvl)
		}
		if len(gateway.submissions) != 0 {
			t.Fatalf("%s 不该落审批单", lvl)
		}
	}
}
