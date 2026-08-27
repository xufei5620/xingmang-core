package finance

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 订阅批次与代理资产六个 Action 的**声明与参数校验**（XM-0037c）。
// 需要真库的（跨环境闸门、损失结转、审计前后镜像）在
// subscription_store_integration_test.go 与 actions_integration_test.go 里。

// executeSubscription 直接调 Handler 而不经 Kernel——理由同 actions_test.go 的
// execute：本组用例验的是**参数校验**，内核的身份 / 权限 / 风险判定有它自己的测试。
func executeSubscription(
	t *testing.T, subs *SubscriptionStore, accounts *Store,
	ctx context.Context, id string, params map[string]any,
) error {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterSubscriptionActions(reg, subs, accounts); err != nil {
		t.Fatalf("RegisterSubscriptionActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	_, err := handler(ctx, params)
	return err
}

func subscriptionCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Environment: env,
		Scopes:      []string{ScopeSubscriptionManage, ScopeRead},
	})
}

// TestSubscriptionActionDefinitionsAreValid：六个 Action 的声明必须过内核校验，
// 且全部是 L1 + 仅人类。
//
// L1 是设计稿 §8.3 的硬性要求（内核对 L2+ fail-closed，声明成 L2 等于让这个
// 动作在当前阶段根本执行不了）；仅人类是 ADR-009 红线——订阅付款决定平台
// 承认自己花了多少钱，不该有一条机器身份能改它的路径。
func TestSubscriptionActionDefinitionsAreValid(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterSubscriptionActions(reg, nil, nil); err != nil {
		t.Fatalf("RegisterSubscriptionActions: %v", err)
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
		ActionSubscriptionBatchRegister, ActionSubscriptionBatchRefund,
		ActionSubscriptionBatchTerminate, ActionProxyAssetSet,
		ActionProxyAssetRefund, ActionProxyAssetTerminate,
	} {
		d, ok := byID[id]
		if !ok {
			t.Fatalf("缺少 %s", id)
		}
		if d.RiskLevel != action.L1 {
			t.Fatalf("%s 的 RiskLevel = %s, want L1", id, d.RiskLevel)
		}
		if d.Permission != ScopeSubscriptionManage {
			t.Fatalf("%s 的权限 = %q, want %s", id, d.Permission, ScopeSubscriptionManage)
		}
		if len(d.PrincipalTypes) != 1 || d.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 必须仅限人类身份, got %v", id, d.PrincipalTypes)
		}
	}
}

// TestSubscriptionAmountsAreStringsNotNumbers 钉住宪法 13 条在参数层的落点。
//
// 金额若声明成 int，JSON 数字一路解成 float64——scale-6 微单位下超过
// $9,007,199 就开始丢精度，而丢掉的那一位不会报错。
func TestSubscriptionAmountsAreStringsNotNumbers(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterSubscriptionActions(reg, nil, nil); err != nil {
		t.Fatalf("RegisterSubscriptionActions: %v", err)
	}
	for _, d := range reg.List() {
		for _, f := range d.Schema.Fields {
			if !strings.HasSuffix(f.Name, "_minor") {
				continue
			}
			if f.Type != action.FieldString {
				t.Fatalf("%s.%s 的类型是 %s，金额必须以字符串传（宪法 13 条）",
					d.ID, f.Name, f.Type)
			}
		}
	}
}

// TestMinorParamRejectsFloatAndNegative：金额参数只收整数最小单位的十进制串。
//
// 带小数点的写法要另定义一遍「几位小数」，而链路上已经有一个
// money.MicroScale 了；负数则是「实付」被写反了符号。
func TestMinorParamRejectsFloatAndNegative(t *testing.T) {
	for _, raw := range []string{"29.99", "-1", "abc", "1e6"} {
		if _, err := parseMinorParam(raw, "paid_minor"); err == nil {
			t.Fatalf("paid_minor=%q 必须被拒", raw)
		}
	}
	got, err := parseMinorParam("29990000", "paid_minor")
	if err != nil {
		t.Fatalf("合法金额应通过: %v", err)
	}
	if got != 29_990_000 {
		t.Fatalf("解析结果 = %d, want 29990000", got)
	}
}

// TestDayParamIsStrict：日期必须严格 YYYY-MM-DD。
//
// time.Parse 对 "2026-8-1" 是宽容的，而这些日期直接决定有效天数与摊销日程
// ——少一位就是少一天的成本。
func TestDayParamIsStrict(t *testing.T) {
	params := map[string]any{"starts_on": "2026-8-1"}
	if _, err := requiredDayParam(params, "starts_on"); err == nil {
		t.Fatal("少位写法必须被拒")
	}
	params["starts_on"] = "2026-08-01"
	if _, err := requiredDayParam(params, "starts_on"); err != nil {
		t.Fatalf("严格写法应通过: %v", err)
	}
}

// TestSubscriptionActionsRejectMalformedUUID：id 打错时给 INVALID_PARAMS（400），
// 而不是让它一路走到库里变成一个内部错误。
func TestSubscriptionActionsRejectMalformedUUID(t *testing.T) {
	subs := NewSubscriptionStore(nil)
	ctx := subscriptionCtx("production")

	cases := map[string]map[string]any{
		ActionSubscriptionBatchRegister: {
			"upstream_account_id": "not-a-uuid", "paid_minor": "1",
			"starts_on": "2026-08-01", "expires_on": "2026-08-31", "account_count": 1,
		},
		ActionSubscriptionBatchRefund: {
			"subscription_batch_id": "not-a-uuid", "refunded_minor": "1",
			"refunded_on": "2026-08-10", "reason": "r",
		},
		ActionProxyAssetTerminate: {
			"proxy_asset_id": "not-a-uuid", "terminated_on": "2026-08-10", "reason": "r",
		},
	}
	for id, params := range cases {
		err := executeSubscription(t, subs, nilPoolStore(), ctx, id, params)
		if err == nil {
			t.Fatalf("%s 应拒绝非法 UUID", id)
		}
		var actionErr *action.Error
		if !asActionError(err, &actionErr) || actionErr.Code != action.CodeInvalidParams {
			t.Fatalf("%s 的错误码 = %v, want INVALID_PARAMS", id, err)
		}
	}
}

// TestUnboundSubscriptionStoreFailsLoudly：只登记声明、不绑执行体的注册表
// 被调用时必须报错，而不是静静地什么都不做（同 RegisterActions）。
func TestUnboundSubscriptionStoreFailsLoudly(t *testing.T) {
	err := executeSubscription(t, nil, nil, subscriptionCtx("production"),
		ActionSubscriptionBatchRegister, map[string]any{
			"upstream_account_id": uuid.New().String(), "paid_minor": "1",
			"starts_on": "2026-08-01", "expires_on": "2026-08-31", "account_count": 1,
		})
	if err == nil {
		t.Fatal("未绑定仓储时必须报错")
	}
}
