package registry_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

type memRunStore struct{ runs []action.Run }

func (m *memRunStore) InsertRun(_ context.Context, r action.Run) error {
	m.runs = append(m.runs, r)
	return nil
}

func actionKernel(t *testing.T, store *registry.Store) (*action.Kernel, *memRunStore) {
	t.Helper()
	reg := action.NewRegistry()
	if err := registry.RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	runs := &memRunStore{}
	return action.NewKernel(reg, runs), runs
}

func staffCtx(scopes ...string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID:                  "staff_alice",
		Type:                principal.TypeHuman,
		IdentityZone:        "staff",
		Issuer:              "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa",
		Environment:         "production",
		Scopes:              scopes,
	})
}

func TestRegisterActionsRegistersAllFive(t *testing.T) {
	reg := action.NewRegistry()
	if err := registry.RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions（仅注册，不执行）: %v", err)
	}
	want := []string{
		"registry.service.create",
		"registry.service.observe",
		"registry.connector.create",
		"registry.connection.create",
		"registry.connection.set_status",
	}
	for _, id := range want {
		if _, _, ok := reg.Lookup(id, "1"); !ok {
			t.Fatalf("未注册 %s", id)
		}
	}
	if n := len(reg.List()); n != len(want) {
		t.Fatalf("注册数应为 %d, got %d", len(want), n)
	}
}

func TestServiceCreateThroughAction(t *testing.T) {
	store := registry.NewStore(testPool(t))
	k, runs := actionKernel(t, store)

	res, err := k.Execute(staffCtx("registry.service.manage"), action.Request{
		ActionID:      "registry.service.create",
		ActionVersion: "1",
		RequestID:     "req-" + uuid.NewString(),
		Params: map[string]any{
			"service_type": "sub2api",
			"instance_id":  "sub2api-prod",
			"environment":  "production",
			"endpoint":     "https://api.solov.cc",
			"owner":        "platform",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	svc, ok := res.Value.(registry.Service)
	if !ok || svc.InstanceID != "sub2api-prod" {
		t.Fatalf("返回值不是 Service: %+v", res.Value)
	}
	if len(runs.runs) != 1 || runs.runs[0].Status != action.RunSucceeded {
		t.Fatalf("ActionRun 不符: %+v", runs.runs)
	}

	got, err := store.GetServiceByInstance(context.Background(), "sub2api", "sub2api-prod")
	if err != nil || got.ID != svc.ID {
		t.Fatalf("库中未找到刚创建的服务: %+v, %v", got, err)
	}
}

func TestServiceCreateRejectedWithoutPermission(t *testing.T) {
	store := registry.NewStore(testPool(t))
	k, _ := actionKernel(t, store)

	_, err := k.Execute(staffCtx("registry.read"), action.Request{
		ActionID:      "registry.service.create",
		ActionVersion: "1",
		RequestID:     "req-1",
		Params: map[string]any{
			"service_type": "sub2api", "instance_id": "sub2api-prod",
			"environment": "production", "endpoint": "https://api.solov.cc", "owner": "platform",
		},
	})
	if action.ErrorCode(err) != action.CodePermissionDenied {
		t.Fatalf("应 PERMISSION_DENIED, got %v", err)
	}
	// 权限被拒时不得落库
	if _, err := store.GetServiceByInstance(context.Background(), "sub2api", "sub2api-prod"); err == nil {
		t.Fatal("权限被拒后不应有数据落库")
	}
}

func TestConnectorCreateBlockedUntilFoundationB(t *testing.T) {
	store := registry.NewStore(testPool(t))
	k, runs := actionKernel(t, store)

	_, err := k.Execute(staffCtx("registry.connector.manage"), action.Request{
		ActionID:      "registry.connector.create",
		ActionVersion: "1",
		RequestID:     "req-1",
		Params: map[string]any{
			"key": "sub2api", "version": "1.0.0", "contract_version": "1",
			"connection_schema_path": "contracts/connectors/sub2api.connection.v1.json",
			"target_allowlist":       []any{"api.solov.cc"},
		},
	})
	if action.ErrorCode(err) != action.CodeAdvancedControlsRequired {
		t.Fatalf("L2 在 Foundation-A 必须被拒绝, got %v", err)
	}
	if len(runs.runs) != 1 || runs.runs[0].ErrorCode != action.CodeAdvancedControlsRequired {
		t.Fatalf("拒绝必须留痕: %+v", runs.runs)
	}
	if _, err := store.GetConnector(context.Background(), "sub2api", "1.0.0"); err == nil {
		t.Fatal("被拒的 Action 不应落库")
	}
}

func TestServiceObserveThroughAction(t *testing.T) {
	store := registry.NewStore(testPool(t))
	k, _ := actionKernel(t, store)
	ctx := staffCtx("registry.service.manage")

	if _, err := k.Execute(ctx, action.Request{
		ActionID: "registry.service.create", ActionVersion: "1", RequestID: "req-1",
		Params: map[string]any{
			"service_type": "sub2api", "instance_id": "sub2api-prod",
			"environment": "production", "endpoint": "https://api.solov.cc", "owner": "platform",
		},
	}); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	res, err := k.Execute(ctx, action.Request{
		ActionID: "registry.service.observe", ActionVersion: "1", RequestID: "req-2",
		Params: map[string]any{
			"service_type": "sub2api", "instance_id": "sub2api-prod",
			"watermark": "wm-100", "status": "degraded",
		},
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	svc, ok := res.Value.(registry.Service)
	if !ok || svc.SourceWatermark != "wm-100" || svc.Status != registry.ServiceDegraded {
		t.Fatalf("观测未回写: %+v", res.Value)
	}
	if svc.ObservedAt == nil {
		t.Fatal("ObservedAt 应被设置")
	}
}
