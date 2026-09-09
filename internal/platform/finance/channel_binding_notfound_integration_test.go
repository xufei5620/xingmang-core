package finance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// bindingRunStore 吞掉执行记录：这条用例验的是错误码，不是审计落库。
type bindingRunStore struct{ runs []action.Run }

func (s *bindingRunStore) InsertRun(_ context.Context, r action.Run) error {
	s.runs = append(s.runs, r)
	return nil
}

func bindingStaffCtx() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman,
		IdentityZone: "staff", Issuer: "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: "production",
		Scopes: []string{finance.ScopePlatformChannelBindingManage},
	})
}

// TestRemoveUnknownServiceIsNotActionNotRegistered 从 **Action 入口**打进来，
// 钉住「参数指名的对象不存在」不会被说成「这个 Action 没注册」。
//
// **修的是什么**：`CodeNotRegistered` 的对外字符串是 ACTION_NOT_REGISTERED。
// 它在 Query 路径上被当作通用 404 用（全仓 13 处，GET 一个不存在的资源，
// 那是对的），但这里是 Action handler——端点在、Action 注册着，不存在的只是
// 参数里那个对象。回 ACTION_NOT_REGISTERED 会让人去查部署。
//
// **走的是 `store.GetService` 那一条**，不是 `gate.Verify`。这个选择是被变异
// 验证逼出来的：第一版用「真 service + 不存在的 channel」，看着合理，但
// `ChannelInventoryGate{}` 的零值会在读到 store 之前就返回
// ErrBindingPrecondition（也是 412）——于是测试**因为另一个原因而绿**，
// 把修复回退掉它照样过。用一个库里没有的 service_id 才真正打到那条分支。
//
// 教训与本 session 前几次同源：正向断言要问一句「旧实现下它会不会照样绿」。
func TestRemoveUnknownServiceIsNotActionNotRegistered(t *testing.T) {
	pool := bindingPool(t)

	reg := action.NewRegistry()
	store := finance.NewChannelBindingStore(pool, time.Now)
	if err := finance.RegisterChannelBindingActions(reg, store, finance.ChannelInventoryGate{}); err != nil {
		t.Fatalf("RegisterChannelBindingActions: %v", err)
	}
	kernel := action.NewKernel(reg, &bindingRunStore{})

	// 语法合法但 core.service 里没有的 id：GetService 返回 ErrNotFound，
	// 而它在 gate 之前被调用。
	_, err := kernel.Execute(bindingStaffCtx(), action.Request{
		ActionID:      finance.ActionPlatformChannelBindingRemove,
		ActionVersion: "1",
		RequestID:     "req-unknown-service-1",
		Params: map[string]any{
			"service_id":          uuid.NewString(),
			"external_channel_id": "chn-1",
			"expected_binding_id": uuid.NewString(),
			"reason":              "回归用例：不存在的 service",
		},
	})
	if err == nil {
		t.Fatal("指名一个不存在的 service 应当报错")
	}
	if code := action.ErrorCode(err); code == action.CodeNotRegistered {
		t.Fatalf("错误码是 %q——那句话说的是「这个 Action 没注册」，"+
			"会让调用方去查部署而不是查自己给的 id", code)
	}
	if code := action.ErrorCode(err); code != action.CodePreconditionFailed {
		t.Fatalf("错误码 = %q，期望 PRECONDITION_FAILED", code)
	}
	// 文案要是设计过的那一句，不是内核的通用兜底——只断言码的话，
	// 一个「码对了但文案被内核换掉」的实现照样绿。
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Message != "binding resource not found" {
		t.Fatalf("文案 = %+v", ae)
	}
}

// TestBindingActionRejectsMissingPermission 补一条最外层的权限断言。
//
// 本包此前只在 definition 层面断言过 `Permission` 字段等于某个字符串
// （TestChannelBindingActionsAreL1HumanOnly…），那证明的是「声明写对了」，
// 不是「内核真的按它拒绝」。声明与执行是两段代码。
func TestBindingActionRejectsMissingPermission(t *testing.T) {
	pool := bindingPool(t)
	reg := action.NewRegistry()
	store := finance.NewChannelBindingStore(pool, time.Now)
	if err := finance.RegisterChannelBindingActions(reg, store, finance.ChannelInventoryGate{}); err != nil {
		t.Fatalf("RegisterChannelBindingActions: %v", err)
	}
	kernel := action.NewKernel(reg, &bindingRunStore{})

	noScope := principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_bob", Type: principal.TypeHuman,
		IdentityZone: "staff", Issuer: "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: "production",
		Scopes: []string{"some.other.scope"},
	})
	_, err := kernel.Execute(noScope, action.Request{
		ActionID:      finance.ActionPlatformChannelBindingRemove,
		ActionVersion: "1",
		RequestID:     "req-noscope-1",
		Params: map[string]any{
			"service_id": uuid.NewString(), "external_channel_id": "c",
			"expected_binding_id": uuid.NewString(), "reason": "无权限",
		},
	})
	if code := action.ErrorCode(err); code != action.CodePermissionDenied {
		t.Fatalf("错误码 = %q，期望 PERMISSION_DENIED", code)
	}
	// 对照：带上权限就不会因为权限被拒（会因为绑定不存在被拒，那是另一条）。
	if code := action.ErrorCode(finance.ErrNotFound); code == action.CodePermissionDenied {
		t.Fatal("对照断言写错了")
	}
}
