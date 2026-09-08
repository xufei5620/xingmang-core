package integration

import (
	"errors"
	"strings"
	"testing"
)

func validClient() APIClient {
	return APIClient{
		PrincipalID:   "svc-billing-sync",
		PrincipalType: "SERVICE",
		DisplayName:   "对账同步",
		Status:        ClientActive,
		Environment:   "production",
	}
}

func TestAPIClientValidateAcceptsMinimalRow(t *testing.T) {
	if err := validClient().Validate(); err != nil {
		t.Fatalf("最小合法行被拒: %v", err)
	}
}

func TestAPIClientValidateRejectsBadFields(t *testing.T) {
	for name, mutate := range map[string]func(*APIClient){
		"principal_id 空":       func(c *APIClient) { c.PrincipalID = "  " },
		"principal_type 不在闭集": func(c *APIClient) { c.PrincipalType = "ROBOT" },
		"display_name 空":       func(c *APIClient) { c.DisplayName = "" },
		"status 不在闭集":          func(c *APIClient) { c.Status = "paused" },
		"environment 空":        func(c *APIClient) { c.Environment = "" },
		"expected_scopes 含空项":  func(c *APIClient) { c.ExpectedScopes = []string{"ops.read", " "} },
	} {
		t.Run(name, func(t *testing.T) {
			c := validClient()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("期望被拒，实际通过")
			}
		})
	}
}

// TestAPIClientValidateRejectsNonRefCredential 钉住宪法 7 条在本包的落点：
// 这个字段收的是**引用**，不是值。
//
// 三个反例都是真会被人粘进来的东西：一把裸 token、一个 https 地址（企微群
// 机器人的 Webhook 地址本身就是凭据）、一个少了一段的引用。
func TestAPIClientValidateRejectsNonRefCredential(t *testing.T) {
	for _, bad := range []string{
		"sk-live-0123456789",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc",
		"secret://only-one-segment",
		"secret://UPPER/name",
	} {
		c := validClient()
		c.CredentialRef = bad
		err := c.Validate()
		if err == nil {
			t.Fatalf("credential_ref=%q 应被拒", bad)
		}
		if !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("credential_ref=%q 期望 ErrInvalidFormat，实际 %v", bad, err)
		}
		// 错误文案要指出正确形态，否则运营只知道"不对"、不知道该填什么。
		if !strings.Contains(err.Error(), "secret://<scope>/<name>") {
			t.Fatalf("错误文案应给出正确形态，实际 %q", err.Error())
		}
	}
}

func TestAPIClientValidateAcceptsEmptyAndWellFormedCredentialRef(t *testing.T) {
	// 空串是合法的：员工账号这类调用方本来就没有 API Key。把它判成错误会
	// 逼着运营编一个假引用填进去。
	c := validClient()
	c.CredentialRef = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("空 credential_ref 应被接受: %v", err)
	}
	c.CredentialRef = "secret://integration/billing-sync"
	if err := c.Validate(); err != nil {
		t.Fatalf("合法 credential_ref 被拒: %v", err)
	}
}

func validRule() AutomationRule {
	return AutomationRule{
		Name:                "余额低于阈值时补充",
		TriggerKind:         TriggerEvent,
		TargetActionID:      "finance.upstream_account.set",
		TargetActionVersion: "1",
		Status:              RuleDraft,
		Environment:         "production",
	}
}

func TestAutomationRuleValidateRejectsBadFields(t *testing.T) {
	for name, mutate := range map[string]func(*AutomationRule){
		"name 空":                  func(r *AutomationRule) { r.Name = " " },
		"trigger_kind 不在闭集":       func(r *AutomationRule) { r.TriggerKind = "cron" },
		"target_action_id 空":      func(r *AutomationRule) { r.TargetActionID = "" },
		"target_action_version 空": func(r *AutomationRule) { r.TargetActionVersion = "" },
		"status 不在闭集":             func(r *AutomationRule) { r.Status = "enabled" },
		"environment 空":           func(r *AutomationRule) { r.Environment = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := validRule()
			mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("期望被拒，实际通过")
			}
		})
	}
}

// TestRuleStatusHasNoEnabledSpelling 钉住一个命名决定：三个状态里不能出现
// enabled / active / on 这类读起来像运行期开关的词。
//
// 这不是洁癖。「登记状态」与「运行状态」在这一页上是最容易被混淆的两件事，
// 而登记簿**没有执行器**——一个叫 enabled 的状态会让人以为配了就生效。
func TestRuleStatusHasNoEnabledSpelling(t *testing.T) {
	for _, s := range []RuleStatus{RuleDraft, RuleRegistered, RuleDisabled} {
		for _, forbidden := range []string{"enabled", "active", "running", "on"} {
			if string(s) == forbidden {
				t.Fatalf("规则登记状态不能叫 %q：它会被读成运行期开关，"+
					"而这张表没有执行器", forbidden)
			}
		}
	}
}

// TestAutomaticExecutionIsFalse 钉住那句会被序列化下发的事实本身。
//
// 它看起来像在测一个常量，但这正是要点：它是端点响应里
// `automatic_execution` 的唯一来源，改它就等于让整套后台对外宣称
// 「规则会自动执行」——那必须是一次显式的、会撞红这条用例的改动。
func TestAutomaticExecutionIsFalse(t *testing.T) {
	if AutomaticExecution() {
		t.Fatal("AutomaticExecution 必须为 false：平台没有规则执行器（ADMIN-IA §5.4.1）")
	}
}
