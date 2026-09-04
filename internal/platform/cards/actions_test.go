package cards

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func TestIssueActionDefinitionIsValid(t *testing.T) {
	if err := issueDef().Validate(); err != nil {
		t.Fatalf("声明非法: %v", err)
	}
}

// 风险等级必须落在**平台当前真能执行**的范围内。
//
// 内核对 L2 及以上返回 ADVANCED_CONTROLS_REQUIRED 并拒绝执行（Advanced
// Controls 属 Foundation-B / XM-0030，尚未实现），所以一个声明成 L2 的开卡
// Action 会是个永远跑不起来的摆设。这条测试钉住的不是「L1 这个字面值」，
// 而是「这个 Action 能被执行」——Foundation-B 落地后它会自然失效，
// 那正是重估风险等级的时机。
func TestIssueActionRiskLevelIsExecutableToday(t *testing.T) {
	def := issueDef()

	if def.RiskLevel.RequiresAdvancedControls() {
		t.Fatalf("RiskLevel %q 需要尚未实现的 Advanced Controls，该 Action 将无法执行", def.RiskLevel)
	}
}

func TestRevealActionRiskLevelIsExecutableToday(t *testing.T) {
	if revealDef().RiskLevel.RequiresAdvancedControls() {
		t.Fatal("reveal 的风险等级会让它无法执行")
	}
}

// 权限串单列，是「对内全量、对外受限」的落点：
// 以后开放给外部用户时只给读权限，开卡权限不下放。
func TestIssueActionPermissionIsDedicated(t *testing.T) {
	if got, want := issueDef().Permission, "card.issue"; got != want {
		t.Fatalf("Permission = %q, want %q", got, want)
	}
}

// 机器身份不得开卡：让采集任务或 AI 有能力花钱，等于开了一条后门
// （ADR-009：AI 不拥有生产后门）。
func TestIssueActionAllowsHumansOnly(t *testing.T) {
	for _, pt := range issueDef().PrincipalTypes {
		if pt != principal.TypeHuman {
			t.Fatalf("开卡不该允许 %q 身份", pt)
		}
	}
}

// Schema 是白名单语义：未声明的字段一律拒绝，防参数偷渡。
func TestIssueActionSchemaRejectsUnknownField(t *testing.T) {
	params := validIssueParams()
	params["amount_override"] = "999999"

	err := issueDef().Schema.Validate(params)
	if !errors.Is(err, action.ErrUnknownField) {
		t.Fatalf("错误 = %v, want ErrUnknownField", err)
	}
}

func TestIssueActionSchemaRequiresIdempotencyKey(t *testing.T) {
	params := validIssueParams()
	delete(params, "idempotency_key")

	err := issueDef().Schema.Validate(params)
	if !errors.Is(err, action.ErrRequiredField) {
		t.Fatalf("缺幂等键必须被拒, err = %v", err)
	}
}

func TestIssueActionSchemaRestrictsTokenType(t *testing.T) {
	params := validIssueParams()
	params["token_type"] = "DOGE"

	if err := issueDef().Schema.Validate(params); !errors.Is(err, action.ErrEnumViolation) {
		t.Fatalf("token_type 应受枚举限制, err = %v", err)
	}
}

func TestIssueHandlerIssuesCardThroughService(t *testing.T) {
	store := newMemStore()
	svc := newService(infini.NewFake(), store)

	out, err := issueHandler(svc)(context.Background(), validIssueParams())
	if err != nil {
		t.Fatal(err)
	}

	summary, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Handler 应返回可进审计的摘要, got %T", out)
	}
	if summary["state"] != string(StateSucceeded) {
		t.Fatalf("摘要 state = %v, want succeeded", summary["state"])
	}
	if store.ops["issue-42"].State != StateSucceeded {
		t.Fatal("台账未落成成功")
	}
}

// 审计摘要里不能出现持卡人邮箱这类个人信息，也不能出现任何卡面数据。
// 审计是 append-only 的，写进去就删不掉了。
func TestIssueHandlerSummaryOmitsPersonalData(t *testing.T) {
	svc := newService(infini.NewFake(), newMemStore())

	out, err := issueHandler(svc)(context.Background(), validIssueParams())
	if err != nil {
		t.Fatal(err)
	}

	summary := out.(map[string]any)
	for key, v := range summary {
		if s, ok := v.(string); ok {
			if s == "ops@example.com" || s == "ZHANG WEI" {
				t.Fatalf("审计摘要字段 %q 泄漏了个人信息: %q", key, s)
			}
		}
	}
}

// 服务未绑定时给出明确错误，而不是空指针崩溃——
// 注册表可以只登记声明供完整性测试与文档生成使用（照搬 finance/registry 的做法）。
func TestIssueHandlerWithoutServiceFailsClearly(t *testing.T) {
	_, err := issueHandler(nil)(context.Background(), validIssueParams())
	if err == nil {
		t.Fatal("服务未绑定时必须报错")
	}
}

func TestRegisterActionsRegistersIssue(t *testing.T) {
	reg := action.NewRegistry()
	svc := newService(infini.NewFake(), newMemStore())

	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Lookup(ActionIssue, actionVersion); !ok {
		t.Fatalf("注册后应能查到 %s", ActionIssue)
	}
}

func validIssueParams() map[string]any {
	return map[string]any{
		"idempotency_key": "issue-42",
		"product_id":      1,
		"top_up_amount":   "10",
		"token_type":      "USDT",
		"user_email":      "ops@example.com",
		"holder_name":     "ZHANG WEI",
		"owner_ref":       "ops-team",
	}
}
