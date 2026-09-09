package action

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// approvedClaim 造一张「已批准的 L2 单」，参数与 okRequest 一致。
func approvedClaim() ApprovalClaim {
	return ApprovalClaim{
		ActionID:      "registry.service.create",
		ActionVersion: "1",
		RiskLevel:     L2,
		Params:        map[string]any{"service_type": "sub2api", "environment": "production"},
		Reason:        "上游换了域名，需要重新登记连接器",
		RequesterID:   "staff_bob",
	}
}

// newApprovedKernel 装一个接了审批中心、注册了 L2 Action 的内核。
func newApprovedKernel(t *testing.T, h Handler) (*Kernel, *recordingGateway, *fakeRunStore) {
	t.Helper()
	def := demoDefinition()
	def.RiskLevel = L2
	k, store := newTestKernel(t, def, h)
	gw := &recordingGateway{claim: approvedClaim()}
	k.approvals = gw
	return k, gw, store
}

func executorCtx(scopes ...string) context.Context {
	return principal.WithPrincipal(context.Background(), testPrincipal(scopes...))
}

// TestExecuteApprovedRunsTheFrozenAction：主路径。
func TestExecuteApprovedRunsTheFrozenAction(t *testing.T) {
	var gotParams map[string]any
	k, gw, store := newApprovedKernel(t, func(_ context.Context, p map[string]any) (any, error) {
		gotParams = p
		return "done", nil
	})

	res, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil)
	if err != nil {
		t.Fatalf("ExecuteApproved: %v", err)
	}
	if res.Value != "done" {
		t.Fatalf("Handler 的返回值没带回来：%v", res.Value)
	}
	// 参数取自单上，不取自调用方——这是「批准一件小事、执行一件大事」不成立的
	// 结构性理由，不是一道可绕过的校验。
	if gotParams["service_type"] != "sub2api" {
		t.Fatalf("Handler 拿到的不是单上冻结的参数：%v", gotParams)
	}
	if len(gw.claimed) != 1 || gw.claimed[0].approvalID != "req-42" {
		t.Fatalf("必须占用这张单：%+v", gw.claimed)
	}
	// 占用写进去的 run id 必须与最终这条执行记录一致，否则审计里
	// 「哪张单跑出了哪次执行」就断了。
	if gw.claimed[0].runID != res.RunID {
		t.Fatalf("占用的 run id 与执行记录对不上：%s vs %s", gw.claimed[0].runID, res.RunID)
	}
	if len(store.runs) != 1 || store.runs[0].Status != RunSucceeded ||
		store.runs[0].RiskLevel != L2 || store.runs[0].ID != res.RunID {
		t.Fatalf("执行记录不对：%+v", store.runs)
	}
}

// TestExecuteApprovedIgnoresCallerParams：调用方给的参数只用于比对，
// **不会**成为实际执行的参数。
func TestExecuteApprovedIgnoresCallerParams(t *testing.T) {
	var gotParams map[string]any
	k, gw, _ := newApprovedKernel(t, func(_ context.Context, p map[string]any) (any, error) {
		gotParams = p
		return nil, nil
	})
	caller := map[string]any{"service_type": "newapi", "environment": "production"}

	if _, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", caller); err != nil {
		t.Fatalf("ExecuteApproved: %v", err)
	}
	if gotParams["service_type"] != "sub2api" {
		t.Fatalf("调用方的参数进到了 Handler：%v", gotParams)
	}
	// 调用方给的那份原样传给审批中心去比对——drift 的判定归它。
	if len(gw.claimed) != 1 || gw.claimed[0].params["service_type"] != "newapi" {
		t.Fatalf("调用方声明的参数没有交给审批中心比对：%+v", gw.claimed)
	}
}

// TestExecuteApprovedChecksExecutorNotRequester 是这一段**排序的理由**。
//
// 占用（写 execution_run_id）是一次性的：占用即作废。所以任何在校验之前占用的
// 实现，都能让一个没有权限的人一次调用就烧掉别人等了一天的单。
//
// 断言形状是「本该被拒的确实被拒」，容易恒真，所以每个子用例都配了一个只差那
// 一项的对照组：补上缺的那一项，同一次调用就应当执行成功并占用。
func TestExecuteApprovedChecksExecutorNotRequester(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx      context.Context
		wantCode Code
	}{
		"缺权限":  {executorCtx("some.other.scope"), CodePermissionDenied},
		"没有身份": {context.Background(), CodePermissionDenied},
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			k, gw, _ := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
				called = true
				return nil, nil
			})
			_, err := k.ExecuteApproved(tc.ctx, "req-42", "http-1", nil)
			if ErrorCode(err) != tc.wantCode {
				t.Fatalf("got %v", err)
			}
			if called {
				t.Fatal("Handler 不该被调用")
			}
			if len(gw.claimed) != 0 {
				t.Fatalf("单被白白烧掉了：%+v", gw.claimed)
			}
		})
	}

	// 对照：补上权限，同一次调用应当执行并占用——确认上面拒的是权限。
	k, gw, _ := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
		return nil, nil
	})
	if _, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil); err != nil {
		t.Fatalf("补上权限后应当执行成功：%v", err)
	}
	if len(gw.claimed) != 1 {
		t.Fatalf("补上权限后应当占用这张单，got %d 次", len(gw.claimed))
	}
}

// TestExecuteApprovedWithoutGatewayFailsClosed：没接审批中心时不能凭空执行。
func TestExecuteApprovedWithoutGatewayFailsClosed(t *testing.T) {
	def := demoDefinition()
	def.RiskLevel = L2
	k, _ := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用")
		return nil, nil
	})
	if _, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil); ErrorCode(err) != CodeAdvancedControlsRequired {
		t.Fatalf("got %v", err)
	}
}

// TestExecuteApprovedPropagatesPeekError：单不存在/还没批/已跑过，
// 由审批中心判并给码，内核原样透传且**不占用**。
func TestExecuteApprovedPropagatesPeekError(t *testing.T) {
	k, gw, store := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用")
		return nil, nil
	})
	gw.peekErr = NewError(CodeApprovalNotFound, "审批单不存在", nil)

	if _, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil); ErrorCode(err) != CodeApprovalNotFound {
		t.Fatalf("got %v", err)
	}
	if len(gw.claimed) != 0 {
		t.Fatalf("读不到单还去占用它：%+v", gw.claimed)
	}
	// 读不到单时连 Action 是哪个都不知道，写 ActionRun 会污染审计口径。
	if len(store.runs) != 0 {
		t.Fatalf("不该写执行记录：%+v", store.runs)
	}
}

// TestExecuteApprovedPropagatesClaimError：占用失败（被人抢先跑了/过期/参数漂移）
// 时不执行，且要留痕。
func TestExecuteApprovedPropagatesClaimError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want Code
	}{
		"已经跑过":  {NewError(CodeConflict, "审批单已经执行过", nil), CodeConflict},
		"还没批":   {NewError(CodePreconditionFailed, "审批单尚未通过", nil), CodePreconditionFailed},
		"占用时炸了": {errors.New("数据库挂了"), CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			k, gw, store := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
				t.Fatal("Handler 不该被调用")
				return nil, nil
			})
			gw.claimErr = tc.err
			_, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil)
			if ErrorCode(err) != tc.want {
				t.Fatalf("got %v", err)
			}
			var ae *Error
			if !errors.As(err, &ae) || ae.ApprovalRequestID != "req-42" {
				t.Fatalf("错误里要带单号：%+v", ae)
			}
			if len(store.runs) != 1 || store.runs[0].ErrorCode != tc.want {
				t.Fatalf("失败也要留痕：%+v", store.runs)
			}
		})
	}
}

// TestExecuteApprovedRefusesRiskLevelDrift：单批的时候是 L2，现在这个动作是 L3。
// 拿旧单跑新等级等于欠授权——L3 要两票，这张单只收了一票。
func TestExecuteApprovedRefusesRiskLevelDrift(t *testing.T) {
	def := demoDefinition()
	def.RiskLevel = L3 // 注册表里已经调高
	k, _ := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用")
		return nil, nil
	})
	gw := &recordingGateway{claim: approvedClaim()} // 单上冻结的还是 L2
	k.approvals = gw

	_, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil)
	if ErrorCode(err) != CodePreconditionFailed {
		t.Fatalf("等级漂移必须拒绝，got %v", err)
	}
	if len(gw.claimed) != 0 {
		t.Fatalf("等级漂移不该烧掉这张单：%+v", gw.claimed)
	}

	// 对照：等级一致就该执行——确认拒的是漂移本身。
	def2 := demoDefinition()
	def2.RiskLevel = L2
	k2, _ := newTestKernel(t, def2, func(context.Context, map[string]any) (any, error) { return nil, nil })
	k2.approvals = &recordingGateway{claim: approvedClaim()}
	if _, err := k2.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil); err != nil {
		t.Fatalf("等级一致时应当执行：%v", err)
	}
}

// TestExecuteApprovedRefusesUnregisteredAction：单上那一版被下线了。
func TestExecuteApprovedRefusesUnregisteredAction(t *testing.T) {
	k, gw, _ := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用")
		return nil, nil
	})
	gw.claim.ActionVersion = "99"

	if _, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil); ErrorCode(err) != CodeNotRegistered {
		t.Fatalf("got %v", err)
	}
	if len(gw.claimed) != 0 {
		t.Fatalf("动作都找不到还去占用单：%+v", gw.claimed)
	}
}

// TestExecuteApprovedKeepsHandlerErrorCode：Handler 自己给的域错误码要透出来，
// 与普通 Execute 一致；单**不**因为执行失败而回到可重跑状态。
func TestExecuteApprovedKeepsHandlerErrorCode(t *testing.T) {
	k, gw, store := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
		return nil, NewError(CodeConflict, "服务已存在", nil)
	})

	_, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil)
	if ErrorCode(err) != CodeConflict {
		t.Fatalf("Handler 的错误码被内核盖掉了：%v", err)
	}
	if len(gw.claimed) != 1 {
		t.Fatalf("执行失败也已经占用过：%+v", gw.claimed)
	}
	if len(store.runs) != 1 || store.runs[0].Status != RunFailed {
		t.Fatalf("失败的执行记录不对：%+v", store.runs)
	}
}

// TestExecuteApprovedValidatesFrozenParamsAgainstSchema：单上冻结的参数如果不
// 合当前 Schema（Action 换了版本以外的方式改了约束），执行前就该拒，而不是
// 让 Handler 收到一份它处理不了的参数。
func TestExecuteApprovedValidatesFrozenParamsAgainstSchema(t *testing.T) {
	k, gw, _ := newApprovedKernel(t, func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不该被调用")
		return nil, nil
	})
	gw.claim.Params = map[string]any{"不存在的字段": 1}

	if _, err := k.ExecuteApproved(executorCtx("registry.service.manage"), "req-42", "http-1", nil); ErrorCode(err) != CodeInvalidParams {
		t.Fatalf("got %v", err)
	}
	if len(gw.claimed) != 0 {
		t.Fatalf("参数不合 Schema 不该烧掉这张单：%+v", gw.claimed)
	}
}

// TestPrecheckIsSharedBetweenBothPaths 钉住「审批那条路径的校验一项不少」。
//
// 两条路径同一份实现（precheck），但**同一份实现**这件事本身要有测试守着——
// 否则日后有人为了「审批已经批过了」而在 ExecuteApproved 里跳过某一项，
// 现有测试一条都不会红。这里逐项造一个只坏那一项的场景，要求两条路径给出
// 同一个错误码。
func TestPrecheckIsSharedBetweenBothPaths(t *testing.T) {
	cases := map[string]struct {
		mutate   func(*Definition)
		ctx      context.Context
		want     Code
		requetID string
	}{
		"身份类型不允许": {
			mutate:   func(d *Definition) { d.PrincipalTypes = []principal.Type{principal.TypeAI} },
			ctx:      executorCtx("registry.service.manage"),
			want:     CodePrincipalTypeNotAllowed,
			requetID: "http-1",
		},
		"环境不允许": {
			mutate:   func(d *Definition) { d.Environments = []string{"staging"} },
			ctx:      executorCtx("registry.service.manage"),
			want:     CodeEnvironmentMismatch,
			requetID: "http-1",
		},
		"缺权限": {
			mutate:   func(*Definition) {},
			ctx:      executorCtx("wrong.scope"),
			want:     CodePermissionDenied,
			requetID: "http-1",
		},
		"缺 request_id": {
			mutate:   func(*Definition) {},
			ctx:      executorCtx("registry.service.manage"),
			want:     CodeInvalidParams,
			requetID: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// 审批路径
			defA := demoDefinition()
			defA.RiskLevel = L2
			tc.mutate(&defA)
			kA, _ := newTestKernel(t, defA, func(context.Context, map[string]any) (any, error) {
				t.Fatal("Handler 不该被调用")
				return nil, nil
			})
			gw := &recordingGateway{claim: approvedClaim()}
			kA.approvals = gw
			_, errA := kA.ExecuteApproved(tc.ctx, "req-42", tc.requetID, nil)

			// 普通路径（用 L1，好让它不落进风险闸）
			defB := demoDefinition()
			tc.mutate(&defB)
			kB, _ := newTestKernel(t, defB, func(context.Context, map[string]any) (any, error) {
				t.Fatal("Handler 不该被调用")
				return nil, nil
			})
			req := okRequest()
			req.RequestID = tc.requetID
			_, errB := kB.Execute(tc.ctx, req)

			if ErrorCode(errA) != tc.want || ErrorCode(errB) != tc.want {
				t.Fatalf("两条路径的判定不一致：审批=%v 普通=%v，期望 %s", errA, errB, tc.want)
			}
			if len(gw.claimed) != 0 {
				t.Fatalf("校验没过就占用了单：%+v", gw.claimed)
			}
		})
	}
}
