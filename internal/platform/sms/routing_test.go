package sms

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 路由规则（ADR-022 决策 3，XM-SMS2 #5）。
//
// 「要号」默认由系统按规则选供应商，人只在想指定时才选。规则按「服务 × 国家」
// 配，值是供应商的优先级列表（第一家失败回落下一家）与单价上限。

// 命中顺序：精确 > 服务通配国家 > 国家通配服务 > 全通配 > 没有规则时的默认顺序。
//
// 服务比国家优先：同一个服务在各国的供应商偏好通常一致（哪家对这个平台成功率
// 高），而「某国一律走某家」是更粗的兜底。
func TestResolveRoutePrecedence(t *testing.T) {
	rules := []RoutingRule{
		{ID: "any", Service: "*", Country: "*", Providers: []string{ProviderHero}, Enabled: true},
		{ID: "svc", Service: "go", Country: "*", Providers: []string{ProviderSMS62}, Enabled: true},
		{ID: "exact", Service: "go", Country: "12", Providers: []string{ProviderHero, ProviderSMS62}, MaxUnitPriceText: "0.50", Enabled: true},
		{ID: "country", Service: "*", Country: "7", Providers: []string{ProviderSMS62, ProviderHero}, Enabled: true},
		{ID: "off", Service: "tg", Country: "*", Providers: []string{ProviderSMS62}, Enabled: false},
	}
	defaults := []string{ProviderSMS62, ProviderHero}
	cases := []struct {
		name, service, country, preferred string
		want                              []string
		wantRule, wantCap                 string
	}{
		{"精确命中", "go", "12", "", []string{ProviderHero, ProviderSMS62}, "exact", "0.50"},
		{"服务通配国家", "go", "99", "", []string{ProviderSMS62}, "svc", ""},
		{"国家通配服务", "wa", "7", "", []string{ProviderSMS62, ProviderHero}, "country", ""},
		{"服务规则压过国家规则", "go", "7", "", []string{ProviderSMS62}, "svc", ""},
		{"全通配兜底", "wa", "99", "", []string{ProviderHero}, "any", ""},
		{"停用的规则不算", "tg", "1", "", []string{ProviderHero}, "any", ""},
		{"人指定就只用指定的那家，但上限仍按命中的规则", "go", "12", ProviderSMS62, []string{ProviderSMS62}, "exact", "0.50"},
	}
	for _, c := range cases {
		got := ResolveRoute(rules, defaults, c.service, c.country, c.preferred)
		if !reflect.DeepEqual(got.Providers, c.want) {
			t.Errorf("%s: providers = %v, want %v", c.name, got.Providers, c.want)
		}
		if got.RuleID != c.wantRule {
			t.Errorf("%s: rule = %q, want %q", c.name, got.RuleID, c.wantRule)
		}
		if got.MaxUnitPriceText != c.wantCap {
			t.Errorf("%s: cap = %q, want %q", c.name, got.MaxUnitPriceText, c.wantCap)
		}
	}

	// 一条规则都没有：默认顺序，没有规则 ID，也没有上限。
	got := ResolveRoute(nil, defaults, "go", "12", "")
	if !reflect.DeepEqual(got.Providers, defaults) || got.RuleID != "" || got.MaxUnitPriceText != "" {
		t.Errorf("无规则应回默认顺序, got %+v", got)
	}
	// 人指定 + 无规则：只用指定的那家。
	got = ResolveRoute(nil, defaults, "go", "12", ProviderHero)
	if !reflect.DeepEqual(got.Providers, []string{ProviderHero}) {
		t.Errorf("人指定应只用那家, got %+v", got)
	}
}

func TestValidateRoutingRule(t *testing.T) {
	known := []string{ProviderSMS62, ProviderHero}
	ok := RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, MaxUnitPriceText: "0.35"}
	if err := ValidateRoutingRule(ok, known); err != nil {
		t.Fatalf("合法规则被拒: %v", err)
	}
	noCap := RoutingRule{Service: "go", Country: "12", Providers: []string{ProviderHero, ProviderSMS62}}
	if err := ValidateRoutingRule(noCap, known); err != nil {
		t.Fatalf("不设上限也合法: %v", err)
	}

	bad := []struct {
		name string
		r    RoutingRule
	}{
		{"服务为空", RoutingRule{Country: "*", Providers: []string{ProviderHero}}},
		{"国家为空", RoutingRule{Service: "go", Providers: []string{ProviderHero}}},
		{"没有供应商", RoutingRule{Service: "go", Country: "*"}},
		{"未知供应商", RoutingRule{Service: "go", Country: "*", Providers: []string{"nobody"}}},
		{"重复供应商", RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero, ProviderHero}}},
		{"单价不是十进制", RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, MaxUnitPriceText: "abc"}},
		{"单价带符号", RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, MaxUnitPriceText: "-1"}},
		{"单价带币种", RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, MaxUnitPriceText: "$1"}},
		{"单价为零", RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, MaxUnitPriceText: "0"}},
	}
	for _, c := range bad {
		if err := ValidateRoutingRule(c.r, known); !errors.Is(err, ErrRoutingRuleInvalid) {
			t.Errorf("%s: 应为 ErrRoutingRuleInvalid, got %v", c.name, err)
		}
	}
	// 没装配的供应商也不行：规则指向一家这个进程根本没有的供应商，要号时
	// 只会一路回落到失败。
	if err := ValidateRoutingRule(ok, []string{ProviderSMS62}); !errors.Is(err, ErrRoutingRuleInvalid) {
		t.Errorf("没装配的供应商应被拒, got %v", err)
	}
}

// 同一个「服务 × 国家」只有一条规则：再设就是覆盖，不是第二条。
func TestSetRoutingRuleUpsertsByServiceCountry(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	ctx := context.Background()

	first, err := svc.SetRoutingRule(ctx, RoutingRule{Service: " GO ", Country: " * ", Providers: []string{ProviderHero}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Service != "go" || first.Country != "*" {
		t.Fatalf("服务与国家应去空白、服务小写, got %q / %q", first.Service, first.Country)
	}
	second, err := svc.SetRoutingRule(ctx, RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderSMS62, ProviderHero}, MaxUnitPriceText: "1.5", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("同键应覆盖同一条, got %q vs %q", second.ID, first.ID)
	}
	rules, err := svc.ListRoutingRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || !reflect.DeepEqual(rules[0].Providers, []string{ProviderSMS62, ProviderHero}) || rules[0].MaxUnitPriceText != "1.5" {
		t.Fatalf("覆盖后应只有一条且是新值, got %+v", rules)
	}
}

func TestSetRoutingRuleRejectsInvalid(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	_, err := svc.SetRoutingRule(context.Background(), RoutingRule{Service: "go", Country: "*", Providers: []string{"nobody"}})
	if !errors.Is(err, ErrRoutingRuleInvalid) {
		t.Fatalf("应拒绝, got %v", err)
	}
	if rules, _ := svc.ListRoutingRules(context.Background()); len(rules) != 0 {
		t.Fatalf("被拒的规则不该落库, got %+v", rules)
	}
}

func TestRemoveRoutingRule(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	ctx := context.Background()
	r, err := svc.SetRoutingRule(ctx, RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveRoutingRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveRoutingRule(ctx, r.ID); !errors.Is(err, ErrRoutingRuleNotFound) {
		t.Fatalf("再删应为 ErrRoutingRuleNotFound, got %v", err)
	}
	if rules, _ := svc.ListRoutingRules(ctx); len(rules) != 0 {
		t.Fatalf("删后应为空, got %+v", rules)
	}
}

// Service.Route 用库里的规则；没有规则时按装配顺序。
func TestServiceRouteFallsBackToConfiguredOrder(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	ctx := context.Background()

	route, err := svc.Route(ctx, "go", "12", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(route.Providers, svc.Providers()) || route.RuleID != "" {
		t.Fatalf("无规则应按装配顺序, got %+v", route)
	}
	rule, _ := svc.SetRoutingRule(ctx, RoutingRule{Service: "go", Country: "*", Providers: []string{ProviderHero}, Enabled: true})
	route, _ = svc.Route(ctx, "go", "12", "")
	if !reflect.DeepEqual(route.Providers, []string{ProviderHero}) || route.RuleID != rule.ID {
		t.Fatalf("应命中规则, got %+v", route)
	}
}

// 两个 Action：sms.routing.set / sms.routing.remove，L1、sms.manage、只给人。
func TestRoutingActionsRegistered(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	setDef, setHandler, ok := reg.Lookup(ActionRoutingSet, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", ActionRoutingSet)
	}
	if setDef.RiskLevel != action.L1 || setDef.Permission != PermissionManage {
		t.Errorf("set: risk=%v perm=%q", setDef.RiskLevel, setDef.Permission)
	}
	params := map[string]any{
		"service": "go", "country": "*", "providers": []any{ProviderHero, ProviderSMS62},
		"max_unit_price": "0.5",
	}
	if err := setDef.Schema.Validate(params); err != nil {
		t.Fatalf("参数应通过 Schema: %v", err)
	}
	out, err := setHandler(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	ruleID, _ := result["rule_id"].(string)
	if ruleID == "" {
		t.Fatalf("结果应带 rule_id, got %+v", result)
	}
	rules, _ := svc.ListRoutingRules(ctx)
	if len(rules) != 1 || !rules[0].Enabled {
		t.Fatalf("不传 enabled 默认启用, got %+v", rules)
	}
	if !reflect.DeepEqual(rules[0].Providers, []string{ProviderHero, ProviderSMS62}) {
		t.Fatalf("顺序必须原样保留, got %v", rules[0].Providers)
	}

	// 显式停用。
	params["enabled"] = false
	if _, err := setHandler(ctx, params); err != nil {
		t.Fatal(err)
	}
	rules, _ = svc.ListRoutingRules(ctx)
	if len(rules) != 1 || rules[0].Enabled {
		t.Fatalf("enabled=false 应停用同一条, got %+v", rules)
	}

	rmDef, rmHandler, ok := reg.Lookup(ActionRoutingRemove, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", ActionRoutingRemove)
	}
	if rmDef.RiskLevel != action.L1 || rmDef.Permission != PermissionManage {
		t.Errorf("remove: risk=%v perm=%q", rmDef.RiskLevel, rmDef.Permission)
	}
	if _, err := rmHandler(ctx, map[string]any{"rule_id": ruleID}); err != nil {
		t.Fatal(err)
	}
	if rules, _ := svc.ListRoutingRules(ctx); len(rules) != 0 {
		t.Fatalf("删后应为空, got %+v", rules)
	}
	if _, err := rmHandler(ctx, map[string]any{"rule_id": ruleID}); !errors.Is(err, ErrRoutingRuleNotFound) {
		t.Fatalf("再删应为 ErrRoutingRuleNotFound, got %v", err)
	}
}
