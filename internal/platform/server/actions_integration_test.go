package server_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/server"
)

func intCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Environment: env, Scopes: []string{server.ScopeManage},
	})
}

func intExecute(t *testing.T, store *server.Store, ctx context.Context, id string, params map[string]any) (any, error) {
	t.Helper()
	reg := action.NewRegistry()
	if err := server.RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, "1")
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	return handler(ctx, params)
}

func intAsActionError(err error, target **action.Error) bool {
	for e := err; e != nil; {
		if ae, ok := e.(*action.Error); ok {
			*target = ae
			return true
		}
		unwrapper, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = unwrapper.Unwrap()
	}
	return false
}

// TestAssetSetCreatesThenUpdatesAcrossFullPath 是端到端的成功路径：
// 经 Action Handler（不经 Kernel）新登记一台资产，再用返回的 asset_id 改它。
func TestAssetSetCreatesThenUpdatesAcrossFullPath(t *testing.T) {
	s := testStore(t)
	ctx := intCtx(testEnv)

	created, err := intExecute(t, s, ctx, server.ActionAssetSet, map[string]any{
		"hostname":                 "srv-e2e-01",
		"ip_addresses":             []string{"10.0.0.9"},
		"monthly_cost_minor_units": "9990",
		"currency":                 "USD",
		"billing_cycle":            "monthly",
		"expires_at":               "2026-12-31",
	})
	if err != nil {
		t.Fatalf("新登记不该失败: %v", err)
	}
	asset, ok := created.(server.Asset)
	if !ok {
		t.Fatalf("返回类型 = %T, want server.Asset", created)
	}
	if asset.ID == uuid.Nil {
		t.Fatal("应分配非零 ID")
	}

	updated, err := intExecute(t, s, ctx, server.ActionAssetSet, map[string]any{
		"asset_id": asset.ID.String(),
		"hostname": "srv-e2e-01",
		"status":   "planned",
	})
	if err != nil {
		t.Fatalf("修改不该失败: %v", err)
	}
	after := updated.(server.Asset)
	if after.Status != server.AssetPlanned {
		t.Fatalf("status = %s, want planned", after.Status)
	}
	// 整行替换：这次没带金额字段，应清空——同 finance.upstream_account.set
	// 的整行替换语义（前端总是先把当前值预填进表单再提交）。
	if after.MonthlyCostMinorUnits != nil {
		t.Fatalf("未带金额字段的更新应清空月付成本, got %v", *after.MonthlyCostMinorUnits)
	}
}

// TestAssetSetRejectsCrossEnvironment 是宪法 15 条在服务器登记簿上的落点：
// 一个 staging 身份不能拿着生产资产的 UUID 打过来。
func TestAssetSetRejectsCrossEnvironment(t *testing.T) {
	s := testStore(t)
	created := mustCreateAsset(t, s, server.Asset{Hostname: "srv-prod-only", Status: server.AssetActive})

	_, err := intExecute(t, s, intCtx("staging"), server.ActionAssetSet, map[string]any{
		"asset_id": created.ID.String(),
		"hostname": "srv-prod-only",
	})
	if err == nil {
		t.Fatal("跨环境操作应被拒")
	}
	var ae *action.Error
	if !intAsActionError(err, &ae) || ae.Code != action.CodePermissionDenied {
		t.Fatalf("错误码 = %v, want PERMISSION_DENIED", err)
	}
}

func TestAssetRetireEndToEnd(t *testing.T) {
	s := testStore(t)
	created := mustCreateAsset(t, s, server.Asset{Hostname: "srv-to-retire", Status: server.AssetActive})

	_, err := intExecute(t, s, intCtx(testEnv), server.ActionAssetRetire, map[string]any{
		"asset_id": created.ID.String(),
		"reason":   "机器到期下架",
	})
	if err != nil {
		t.Fatalf("退役不该失败: %v", err)
	}
	got, err := s.GetAsset(context.Background(), created.ID)
	if err != nil || got.Status != server.AssetRetired {
		t.Fatalf("退役后状态 = %v, err=%v", got.Status, err)
	}
}

// TestServiceNoteSetRejectsBadFieldsAfterResolvingParent 覆盖
// actions_test.go 明确留白的那一段：service_note.set 的新登记分支要先经
// resolveOwningAsset 读一次父资产（真库），domain.Validate 里的字段规则
// （端口范围、空服务名）才轮到检查——这条路径离了真库测不到。
func TestServiceNoteSetRejectsBadFieldsAfterResolvingParent(t *testing.T) {
	s := testStore(t)
	asset := mustCreateAsset(t, s, server.Asset{Hostname: "srv-for-notes", Status: server.AssetActive})
	ctx := intCtx(testEnv)

	_, err := intExecute(t, s, ctx, server.ActionServiceNoteSet, map[string]any{
		"server_id":    asset.ID.String(),
		"service_name": "api",
		"service_kind": string(server.ServiceContainer),
		"port":         70000,
	})
	if err == nil {
		t.Fatal("超出范围的端口应被拒")
	}
	var ae *action.Error
	if !intAsActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("错误码 = %v, want INVALID_PARAMS", err)
	}
}

// TestServiceNoteSetRejectsUnknownServer 是「server_id 指向不存在的资产」
// 这一支：resolveOwningAsset 读不到父行，必须报清楚而不是 500。
func TestServiceNoteSetRejectsUnknownServer(t *testing.T) {
	s := testStore(t)
	_, err := intExecute(t, s, intCtx(testEnv), server.ActionServiceNoteSet, map[string]any{
		"server_id":    uuid.New().String(),
		"service_name": "api",
		"service_kind": string(server.ServiceContainer),
	})
	if err == nil {
		t.Fatal("指向不存在的资产应被拒")
	}
	var ae *action.Error
	if !intAsActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("错误码 = %v, want INVALID_PARAMS", err)
	}
}

func TestServiceNoteSetAndRemoveEndToEnd(t *testing.T) {
	s := testStore(t)
	asset := mustCreateAsset(t, s, server.Asset{Hostname: "srv-notes-e2e", Status: server.AssetActive})
	ctx := intCtx(testEnv)

	created, err := intExecute(t, s, ctx, server.ActionServiceNoteSet, map[string]any{
		"server_id":    asset.ID.String(),
		"service_name": "worker",
		"service_kind": string(server.ServiceSystemd),
	})
	if err != nil {
		t.Fatalf("新登记不该失败: %v", err)
	}
	note := created.(server.ServiceNote)

	_, err = intExecute(t, s, ctx, server.ActionServiceNoteRemove, map[string]any{
		"service_note_id": note.ID.String(),
		"reason":          "服务已下线",
	})
	if err != nil {
		t.Fatalf("删除不该失败: %v", err)
	}
	if _, err := s.GetServiceNote(context.Background(), note.ID); err == nil {
		t.Fatal("删除后应查不到")
	}
}

func TestSupplierAndDomainSetEndToEnd(t *testing.T) {
	s := testStore(t)
	ctx := intCtx(testEnv)

	sup, err := intExecute(t, s, ctx, server.ActionSupplierSet, map[string]any{
		"name":    "Vultr",
		"website": "https://vultr.com",
	})
	if err != nil {
		t.Fatalf("登记供应商不该失败: %v", err)
	}
	supplier := sup.(server.Supplier)
	if supplier.Name != "Vultr" {
		t.Fatalf("name = %q", supplier.Name)
	}

	dom, err := intExecute(t, s, ctx, server.ActionDomainSet, map[string]any{
		"domain_name": "console.example.com",
		"cert_source": "acme",
	})
	if err != nil {
		t.Fatalf("登记域名不该失败: %v", err)
	}
	domain := dom.(server.ServerDomain)
	if domain.CertSource != server.CertACME {
		t.Fatalf("cert_source = %q", domain.CertSource)
	}
}
