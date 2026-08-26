package action

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// fakeRunStore 记录写入的 ActionRun，供断言使用。
type fakeRunStore struct{ runs []Run }

func (f *fakeRunStore) InsertRun(_ context.Context, r Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func testPrincipal(scopes ...string) principal.Principal {
	return principal.Principal{
		ID:                  "staff_alice",
		Type:                principal.TypeHuman,
		IdentityZone:        "staff",
		Issuer:              "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa",
		Environment:         "production",
		Scopes:              scopes,
	}
}

func newTestKernel(t *testing.T, def Definition, h Handler) (*Kernel, *fakeRunStore) {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(def, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	store := &fakeRunStore{}
	return NewKernel(reg, store), store
}

func okRequest() Request {
	return Request{
		ActionID:      "registry.service.create",
		ActionVersion: "1",
		RequestID:     "req-1",
		Params:        map[string]any{"service_type": "sub2api", "environment": "production"},
	}
}

func TestKernelHappyPath(t *testing.T) {
	called := false
	k, store := newTestKernel(t, demoDefinition(), func(context.Context, map[string]any) (any, error) {
		called = true
		return "created", nil
	})
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	res, err := k.Execute(ctx, okRequest())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !called || res.Value != "created" || res.RunID == uuid.Nil {
		t.Fatalf("结果不符: %+v called=%v", res, called)
	}
	if len(store.runs) != 1 {
		t.Fatalf("应记录 1 条 ActionRun, got %d", len(store.runs))
	}
	run := store.runs[0]
	if run.Status != RunSucceeded || run.ErrorCode != "" ||
		run.ActionID != "registry.service.create" || run.PrincipalID != "staff_alice" ||
		run.RequestID != "req-1" || run.Environment != "production" || run.RiskLevel != L1 {
		t.Fatalf("ActionRun 字段不全: %+v", run)
	}
	if run.FinishedAt.Before(run.StartedAt) || run.DurationMS < 0 {
		t.Fatalf("时间字段不合理: %+v", run)
	}
}

func TestKernelRejectsUnregistered(t *testing.T) {
	k, store := newTestKernel(t, demoDefinition(), noopHandler)
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
	req := okRequest()
	req.ActionID = "registry.service.nope"

	_, err := k.Execute(ctx, req)
	if ErrorCode(err) != CodeNotRegistered {
		t.Fatalf("未注册 Action 应 ACTION_NOT_REGISTERED, got %v", err)
	}
	// 未注册时无法确定 risk_level 等字段，因此不写 ActionRun（避免污染审计口径）
	if len(store.runs) != 0 {
		t.Fatalf("未注册不应写 ActionRun, got %d", len(store.runs))
	}
}

func TestKernelRejectsMissingPrincipal(t *testing.T) {
	k, _ := newTestKernel(t, demoDefinition(), noopHandler)
	_, err := k.Execute(context.Background(), okRequest())
	if ErrorCode(err) != CodePermissionDenied {
		t.Fatalf("无 Principal 应 PERMISSION_DENIED, got %v", err)
	}
}

func TestKernelRejectsWrongPrincipalType(t *testing.T) {
	k, store := newTestKernel(t, demoDefinition(), noopHandler)
	p := testPrincipal("registry.service.manage")
	p.Type = principal.TypeAI
	ctx := principal.WithPrincipal(context.Background(), p)

	_, err := k.Execute(ctx, okRequest())
	if ErrorCode(err) != CodePrincipalTypeNotAllowed {
		t.Fatalf("类型不允许应 PRINCIPAL_TYPE_NOT_ALLOWED, got %v", err)
	}
	if len(store.runs) != 1 || store.runs[0].Status != RunFailed {
		t.Fatalf("失败也必须留下 ActionRun: %+v", store.runs)
	}
}

func TestKernelRejectsAdvancedControlLevels(t *testing.T) {
	// ADR-003：Foundation-A 只做 L0/L1；L2 及以上必须显式拒绝。
	for _, lvl := range []RiskLevel{L2, L3, L4} {
		def := demoDefinition()
		def.RiskLevel = lvl
		k, store := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
			t.Fatal("Handler 不应被调用")
			return nil, nil
		})
		ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

		_, err := k.Execute(ctx, okRequest())
		if ErrorCode(err) != CodeAdvancedControlsRequired {
			t.Fatalf("%s 应 ADVANCED_CONTROLS_REQUIRED, got %v", lvl, err)
		}
		if len(store.runs) != 1 || store.runs[0].ErrorCode != CodeAdvancedControlsRequired {
			t.Fatalf("%s 的拒绝必须留痕: %+v", lvl, store.runs)
		}
	}
}

func TestKernelRejectsEnvironmentMismatch(t *testing.T) {
	def := demoDefinition()
	def.Environments = []string{"development", "staging"} // 不含 production
	k, store := newTestKernel(t, def, noopHandler)
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	_, err := k.Execute(ctx, okRequest())
	if ErrorCode(err) != CodeEnvironmentMismatch {
		t.Fatalf("环境不允许应 ENVIRONMENT_MISMATCH, got %v", err)
	}
	if len(store.runs) != 1 {
		t.Fatal("环境拒绝也要留痕")
	}
}

func TestKernelRejectsMissingPermission(t *testing.T) {
	k, store := newTestKernel(t, demoDefinition(), func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不应被调用")
		return nil, nil
	})
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.read")) // 缺 manage

	_, err := k.Execute(ctx, okRequest())
	if ErrorCode(err) != CodePermissionDenied {
		t.Fatalf("缺权限应 PERMISSION_DENIED, got %v", err)
	}
	if len(store.runs) != 1 || store.runs[0].ErrorCode != CodePermissionDenied {
		t.Fatalf("权限拒绝必须留痕: %+v", store.runs)
	}
}

func TestKernelRejectsBadParams(t *testing.T) {
	k, store := newTestKernel(t, demoDefinition(), func(context.Context, map[string]any) (any, error) {
		t.Fatal("Handler 不应被调用")
		return nil, nil
	})
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
	req := okRequest()
	req.Params = map[string]any{"service_type": "sub2api", "environment": "production", "sneaky": true}

	_, err := k.Execute(ctx, req)
	if ErrorCode(err) != CodeInvalidParams {
		t.Fatalf("未声明字段应 INVALID_PARAMS, got %v", err)
	}
	if len(store.runs) != 1 {
		t.Fatal("参数拒绝也要留痕")
	}
}

func TestKernelRecordsHandlerFailureWithoutLeaking(t *testing.T) {
	cause := errors.New("pq: duplicate key value violates unique constraint")
	k, store := newTestKernel(t, demoDefinition(), func(context.Context, map[string]any) (any, error) {
		return nil, cause
	})
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	_, err := k.Execute(ctx, okRequest())
	if ErrorCode(err) != CodeExecutionFailed {
		t.Fatalf("Handler 失败应 EXECUTION_FAILED, got %v", err)
	}
	if got := err.Error(); strings.Contains(got, "pq:") || strings.Contains(got, "constraint") {
		t.Fatalf("对外错误泄漏底层细节: %s", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("内部仍应能 unwrap 到根因")
	}
	if len(store.runs) != 1 || store.runs[0].Status != RunFailed {
		t.Fatalf("失败必须留痕: %+v", store.runs)
	}
}

func TestKernelRequiresRequestID(t *testing.T) {
	k, _ := newTestKernel(t, demoDefinition(), noopHandler)
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
	req := okRequest()
	req.RequestID = ""

	if _, err := k.Execute(ctx, req); ErrorCode(err) != CodeInvalidParams {
		t.Fatalf("缺 RequestID 应 INVALID_PARAMS, got %v", err)
	}
}
