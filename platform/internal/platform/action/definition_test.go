package action

import (
	"context"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func demoDefinition() Definition {
	return Definition{
		ID:             "registry.service.create",
		Version:        "1",
		RiskLevel:      L1,
		Permission:     "registry.service.manage",
		Schema:         demoSchema(),
		Environments:   []string{"development", "staging", "production"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}
}

func noopHandler(context.Context, map[string]any) (any, error) { return nil, nil }

func TestDefinitionValidate(t *testing.T) {
	if err := demoDefinition().Validate(); err != nil {
		t.Fatalf("合法 Definition 被拒绝: %v", err)
	}
	for name, mutate := range map[string]func(*Definition){
		"空 ID":             func(d *Definition) { d.ID = "" },
		"非点分 ID":           func(d *Definition) { d.ID = "createService" },
		"空 Version":        func(d *Definition) { d.Version = "" },
		"非法 RiskLevel":     func(d *Definition) { d.RiskLevel = RiskLevel("HIGH") },
		"空 Permission":     func(d *Definition) { d.Permission = "" },
		"空 Environments":   func(d *Definition) { d.Environments = nil },
		"非法 Environment":   func(d *Definition) { d.Environments = []string{"prod"} },
		"空 PrincipalTypes": func(d *Definition) { d.PrincipalTypes = nil },
	} {
		d := demoDefinition()
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Fatalf("%s：应被拒绝但通过了", name)
		}
	}
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(demoDefinition(), noopHandler); err != nil {
		t.Fatalf("Register: %v", err)
	}
	def, h, ok := r.Lookup("registry.service.create", "1")
	if !ok || h == nil || def.Permission != "registry.service.manage" {
		t.Fatalf("Lookup 失败: %+v %v", def, ok)
	}
	if _, _, ok := r.Lookup("registry.service.create", "2"); ok {
		t.Fatal("不同版本不应命中")
	}
	if _, _, ok := r.Lookup("nope", "1"); ok {
		t.Fatal("未注册的 ID 不应命中")
	}
	if n := len(r.List()); n != 1 {
		t.Fatalf("List 应为 1 条, got %d", n)
	}
}

func TestRegistryRejectsDuplicateAndInvalid(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(demoDefinition(), noopHandler); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(demoDefinition(), noopHandler); err == nil {
		t.Fatal("同 ID+版本重复注册必须拒绝")
	}
	bad := demoDefinition()
	bad.Permission = ""
	if err := r.Register(bad, noopHandler); err == nil {
		t.Fatal("非法 Definition 必须拒绝")
	}
	if err := r.Register(demoDefinition(), nil); err == nil {
		t.Fatal("nil Handler 必须拒绝")
	}
}
