package sms

import (
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 注册表是唯一的供应商清单：ID 唯一、有标签、凭据引用解析得过、有构造函数。
//
// 这四条里任何一条漏了，症状都在很远的地方出现：标签空是页面上一个空白按钮，
// 引用非法只在 real 模式启动时炸，构造函数缺失是 real 模式下这家永远不存在。
func TestRegistryEntriesAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Providers() {
		if p.ID == "" || p.Label == "" {
			t.Errorf("供应商缺 ID 或标签: %+v", p)
		}
		if seen[p.ID] {
			t.Errorf("供应商 ID 重复: %s", p.ID)
		}
		seen[p.ID] = true
		if _, err := secrets.ParseCredentialRef(p.CredentialRef()); err != nil {
			t.Errorf("%s 的凭据引用 %q 解析失败: %v", p.ID, p.CredentialRef(), err)
		}
		if p.Build == nil {
			t.Errorf("%s 没有构造函数", p.ID)
		}
		if len(p.Capabilities) == 0 {
			t.Errorf("%s 没有任何能力", p.ID)
		}
	}
	if len(AllProviders) != len(Providers()) {
		t.Fatalf("AllProviders 应由注册表生成: %v", AllProviders)
	}
}

// SupportsAction 必须由能力集推导，而不是再写一份按名字的 switch——
// 两份清单迟早差一家。这里逐个动作对照能力集验证。
func TestSupportsActionIsDerivedFromCapabilities(t *testing.T) {
	kinds := []string{KindPurchase, KindCancel, KindFinish, KindReplace, KindReactivate, KindProlong,
		KindRent, KindEmailPurchase, KindEmailCancel, KindEmailReorder, KindFavoriteSet, KindFavoriteRemove}
	for _, p := range Providers() {
		for _, kind := range kinds {
			capability, ok := capabilityForKind(kind)
			if !ok {
				t.Fatalf("动作 %s 没有映射到能力", kind)
			}
			if got, want := SupportsAction(p.ID, kind), p.Has(capability); got != want {
				t.Errorf("%s / %s: SupportsAction=%v, 能力集=%v", p.ID, kind, got, want)
			}
		}
	}
	if SupportsAction("nobody", KindPurchase) {
		t.Fatal("不认识的供应商不该支持任何动作")
	}
}

// 62 只有购买、目录、订单与 token；Hero 有生命周期、租用、邮箱、收藏、余额、历史。
// 这两条是官方文档钉死的事实，注册表改错了页面上会多出或少掉按钮。
func TestRegistryMatchesOfficialCapabilities(t *testing.T) {
	sms62, _ := Spec(ProviderSMS62)
	hero, _ := Spec(ProviderHero)
	for _, c := range []Capability{CapLifecycle, CapRent, CapEmail, CapFavorites, CapBalance, CapHistory} {
		if sms62.Has(c) {
			t.Errorf("62 不该有 %s", c)
		}
		if !hero.Has(c) {
			t.Errorf("Hero 应有 %s", c)
		}
	}
	if !sms62.Has(CapToken) || hero.Has(CapToken) {
		t.Error("只有 62 的取码要带 token")
	}
	if !sms62.Has(CapOrders) || hero.Has(CapOrders) {
		t.Error("只有 62 有上游订单列表")
	}
	if got := ProvidersWith(CapRent); len(got) != 1 || got[0] != ProviderHero {
		t.Errorf("ProvidersWith(rent) = %v", got)
	}
}

func TestHostOfRequiresHTTPS(t *testing.T) {
	if h, err := hostOf("https://api.62-us.com"); err != nil || h != "api.62-us.com" {
		t.Fatalf("hostOf = %q, %v", h, err)
	}
	if h, err := hostOf("https://hero-sms.com/api/v1"); err != nil || h != "hero-sms.com" {
		t.Fatalf("hostOf = %q, %v", h, err)
	}
	if _, err := hostOf("http://api.62-us.com"); err == nil {
		t.Fatal("带密钥的请求必须走 TLS")
	}
}
