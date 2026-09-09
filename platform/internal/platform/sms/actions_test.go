package sms

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 「开哪几家」必须是一个 Action，不是环境变量。
//
// 走 Action 才有审计：谁在什么时候把某家打开或关掉，是出事之后第一个要问的
// 问题。改环境变量改完就没痕迹了，还得重启整个平台才生效。
func TestSetProviderEnabledIsRegisteredAsL1ManageAction(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store)
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatalf("注册: %v", err)
	}

	def, handler, ok := reg.Lookup(ActionProviderSetEnabled, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", ActionProviderSetEnabled)
	}
	// L1 是硬约束：内核对 L2 及以上返回 ADVANCED_CONTROLS_REQUIRED，
	// 声明成 L2 会让这个开关变成永远点不动的摆设。
	if def.RiskLevel != action.L1 {
		t.Errorf("risk = %v, want L1", def.RiskLevel)
	}
	if def.Permission != PermissionManage {
		t.Errorf("permission = %q, want %q", def.Permission, PermissionManage)
	}

	if _, err := handler(context.Background(), map[string]any{
		"provider": ProviderHero, "enabled": false,
	}); err != nil {
		t.Fatalf("关掉 hero: %v", err)
	}
	if store.status[ProviderHero].Enabled {
		t.Fatal("库里该记成关着")
	}
}

// 关掉的供应商**不许花钱**，而且要在打上游之前就拦住。
//
// 只靠前端不渲染是不够的：一个还留着旧页面的标签页仍然能提交，
// 而那一次提交花的是真钱。
func TestPurchaseRefusesDisabledProvider(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{}
	svc := newService(t, adapter, store)
	// 验证过，但运营把它关了——这两件事是独立的。
	store.status[ProviderSMS62] = ProviderStatus{
		Provider: ProviderSMS62, Enabled: false, VerifiedAt: testNow,
	}

	_, err := svc.Purchase(context.Background(), "op-off", ProviderSMS62, PurchaseInput{Quantity: 1})
	if !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("关着的供应商必须拒绝, got %v", err)
	}
	if adapter.purchaseCalls != 0 {
		t.Fatal("必须在打上游之前就拒绝")
	}
}

// 连接测试**不改开关**。
//
// 断言方向是「开着的仍然开着」而不是「关着的仍然关着」：后者在
// SaveProviderStatus 把 Enabled 整个覆盖掉时**照样通过**（覆盖成零值也是
// 关着），等于测了个寂寞。这条在覆盖发生时会红。
func TestTestConnectionDoesNotClobberEnabled(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{testIP: "1.2.3.4"}, store)
	if err := store.SetProviderEnabled(context.Background(), ProviderSMS62, true, testNow); err != nil {
		t.Fatalf("打开: %v", err)
	}

	if _, err := svc.TestConnection(context.Background(), ProviderSMS62); err != nil {
		t.Fatalf("连接测试: %v", err)
	}
	if !store.status[ProviderSMS62].Enabled {
		t.Fatal("连接测试把开着的供应商关掉了")
	}
}
