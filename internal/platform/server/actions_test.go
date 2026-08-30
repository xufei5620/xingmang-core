package server

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func nilPoolStore() *Store { return NewStore(nil) }

func humanCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Environment: env,
		Scopes:      []string{ScopeManage},
	})
}

// execute 直接调 Handler 而不经 Kernel——本组用例验的是参数校验与业务规则，
// 内核的身份/权限/风险判定有它自己的测试（同 finance 包 execute 的理由）。
func execute(t *testing.T, store *Store, ctx context.Context, id string, params map[string]any) error {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	_, err := handler(ctx, params)
	return err
}

func asActionError(err error, target **action.Error) bool {
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

func requireInvalidParams(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望被拒，实际返回 nil")
	}
	var ae *action.Error
	if !asActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("错误码 = %v, want INVALID_PARAMS", err)
	}
}

// TestActionDefinitionsAreValid：六个 Action 的声明本身必须过内核的校验，
// 且落在 Foundation-A 能执行的边界内（L1、人类身份、环境不由参数自称）。
func TestActionDefinitionsAreValid(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	defs := reg.List()
	if len(defs) != 6 {
		t.Fatalf("应注册 6 个 Action，实际 %d 个", len(defs))
	}

	byID := map[string]action.Definition{}
	for _, d := range defs {
		byID[d.ID] = d
	}
	for _, id := range []string{
		ActionAssetSet, ActionAssetRetire, ActionSupplierSet,
		ActionDomainSet, ActionServiceNoteSet, ActionServiceNoteRemove,
	} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("缺少 %s", id)
		}
	}

	for _, d := range defs {
		if d.RiskLevel != action.L1 {
			t.Fatalf("%s 风险等级 = %s, want L1（Foundation-A 对 L2+ fail-closed）", d.ID, d.RiskLevel)
		}
		if d.Permission != ScopeManage {
			t.Fatalf("%s 权限 = %q, want %q", d.ID, d.Permission, ScopeManage)
		}
		if len(d.PrincipalTypes) != 1 || d.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 应只允许人类身份（ADR-009：AI 不拥有生产后门）, 实际 %v", d.ID, d.PrincipalTypes)
		}
		for _, f := range d.Schema.Fields {
			if f.Name == "environment" {
				t.Fatalf("%s 不该接受 environment 参数——环境取自调用者身份（宪法 15 条）", d.ID)
			}
		}
	}
}

func TestAssetSetRejectsBlankHostname(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname": "   ",
	})
	requireInvalidParams(t, err)
}

func TestAssetSetRejectsBadStatus(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname": "srv-1",
		"status":   "online",
	})
	requireInvalidParams(t, err)
}

func TestAssetSetRejectsDecimalMonthlyCost(t *testing.T) {
	// "99.90" 必须被拒——见 optionalMinorParam 的注释：静默接受会把
	// $99.90 记成 9990 个最小单位（差两个数量级）且完全不报错。
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname":                 "srv-1",
		"monthly_cost_minor_units": "99.90",
		"currency":                 "USD",
	})
	requireInvalidParams(t, err)
}

func TestAssetSetRejectsScientificNotationCost(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname":                 "srv-1",
		"monthly_cost_minor_units": "1e4",
		"currency":                 "USD",
	})
	requireInvalidParams(t, err)
}

// TestOptionalMinorParamAcceptsPureIntegers 直接测参数解析这一层（不经
// Handler/Store）：纯整数最小单位字符串必须被接受，且不做任何标度换算
// ——那是调用方（前端表单）按币种自然标度换算好之后才传进来的整数。
func TestOptionalMinorParamAcceptsPureIntegers(t *testing.T) {
	for raw, want := range map[string]int64{"0": 0, "9990": 9990, "100000000": 100000000} {
		v, err := optionalMinorParam(map[string]any{"monthly_cost_minor_units": raw}, "monthly_cost_minor_units")
		if err != nil {
			t.Fatalf("%q 应合法: %v", raw, err)
		}
		if v == nil || *v != want {
			t.Fatalf("%q 解析结果 = %v, want %d", raw, v, want)
		}
	}
	v, err := optionalMinorParam(map[string]any{}, "monthly_cost_minor_units")
	if err != nil || v != nil {
		t.Fatalf("缺省字段应返回 (nil, nil)，实际 (%v, %v)", v, err)
	}
}

func TestAssetSetRejectsUnknownCurrency(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname":                 "srv-1",
		"monthly_cost_minor_units": "100",
		"currency":                 "XYZ",
	})
	requireInvalidParams(t, err)
}

func TestAssetSetRejectsMalformedSupplierID(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname":    "srv-1",
		"supplier_id": "not-a-uuid",
	})
	requireInvalidParams(t, err)
}

func TestAssetSetRejectsMalformedExpiresAt(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"hostname":   "srv-1",
		"expires_at": "31/08/2026",
	})
	requireInvalidParams(t, err)
}

func TestAssetSetRejectsMalformedAssetID(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetSet, map[string]any{
		"asset_id": "not-a-uuid",
		"hostname": "srv-1",
	})
	requireInvalidParams(t, err)
}

func TestAssetRetireRequiresReason(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetRetire, map[string]any{
		"asset_id": uuid.New().String(),
		"reason":   "  ",
	})
	requireInvalidParams(t, err)
}

func TestAssetRetireRejectsMalformedID(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAssetRetire, map[string]any{
		"asset_id": "nope",
		"reason":   "机器下架",
	})
	requireInvalidParams(t, err)
}

func TestSupplierSetRequiresName(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionSupplierSet, map[string]any{
		"name": " ",
	})
	requireInvalidParams(t, err)
}

func TestSupplierSetRejectsNonHTTPSWebsite(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionSupplierSet, map[string]any{
		"name":    "Vultr",
		"website": "http://vultr.com",
	})
	requireInvalidParams(t, err)
}

func TestDomainSetRequiresDomainName(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionDomainSet, map[string]any{
		"domain_name": "",
	})
	requireInvalidParams(t, err)
}

func TestDomainSetRejectsBadCertSource(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionDomainSet, map[string]any{
		"domain_name": "example.com",
		"cert_source": "self-signed",
	})
	requireInvalidParams(t, err)
}

func TestServiceNoteSetRequiresServerIDOnCreate(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionServiceNoteSet, map[string]any{
		"service_name": "api",
		"service_kind": string(ServiceContainer),
	})
	requireInvalidParams(t, err)
}

func TestServiceNoteSetRejectsBadKind(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionServiceNoteSet, map[string]any{
		"server_id":    uuid.New().String(),
		"service_name": "api",
		"service_kind": "docker",
	})
	requireInvalidParams(t, err)
}

// 端口范围（1~65535）由 ServiceNote.Validate 校验，见
// types_test.go 的 TestServiceNoteValidateRejectsBadPort；不在这里经
// Handler 重复覆盖——service_note.set 的新登记分支要先解析 server_id 归属
// 的父资产（resolveOwningAsset）才轮到 Validate，那一步需要真实 DB，
// 属于 actions_integration_test.go 的范围（同 finance 包 token_map.set
// 的 resolveOwningAccount 先于字段校验是同一个形状）。

func TestServiceNoteRemoveRequiresReason(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionServiceNoteRemove, map[string]any{
		"service_note_id": uuid.New().String(),
		"reason":          "",
	})
	requireInvalidParams(t, err)
}

// TestHandlersRequirePrincipal：缺 Principal 时一律拒绝，不静默当成某个默认
// 环境处理（同 finance.callerPrincipal 的理由）。
func TestHandlersRequirePrincipal(t *testing.T) {
	for id, params := range map[string]map[string]any{
		ActionAssetSet:       {"hostname": "srv-1"},
		ActionSupplierSet:    {"name": "Vultr"},
		ActionDomainSet:      {"domain_name": "example.com"},
		ActionServiceNoteSet: {"server_id": uuid.New().String(), "service_name": "api", "service_kind": string(ServiceContainer)},
	} {
		t.Run(id, func(t *testing.T) {
			err := execute(t, nilPoolStore(), context.Background(), id, params)
			if err == nil {
				t.Fatal("缺 Principal 应被拒")
			}
			var ae *action.Error
			if !asActionError(err, &ae) || ae.Code != action.CodePermissionDenied {
				t.Fatalf("错误码 = %v, want PERMISSION_DENIED", err)
			}
		})
	}
}

func TestMachinePrincipalIsRejectedByKernelLevelDeclaration(t *testing.T) {
	// 这条不经 Handler——PrincipalTypes 的裁决在 Kernel 层，Handler 从不
	// 检查身份类型（Handler 文档：参数已通过 Schema 校验，不重复做权限判断）。
	// 这里只确认声明本身把机器身份挡在外面，真正的裁决行为属于 action 包
	// 自己的 Kernel 测试。
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	def, _, ok := reg.Lookup(ActionAssetSet, actionVersion)
	if !ok {
		t.Fatal("server.asset.set 未注册")
	}
	for _, pt := range def.PrincipalTypes {
		if pt != principal.TypeHuman {
			t.Fatalf("server.asset.set 不该允许非人类身份，声明里出现了 %s", pt)
		}
	}
}
