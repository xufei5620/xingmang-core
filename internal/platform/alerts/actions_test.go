package alerts

import (
	"context"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// nilPoolStore 造一个不会连库的 Store。
//
// 参数校验发生在任何查询之前，所以「拒绝非法输入」这类用例不需要真库。
// 需要真库的（跨环境闸门、审计前后镜像）在 store_integration_test.go 里。
func nilPoolStore() *Store { return NewStore(nil) }

func humanCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Environment: env, Scopes: []string{ScopeAcknowledge, ScopeSilenceManage},
	})
}

// TestActionDefinitionsAreValid：两个 Action 的声明本身必须过内核的校验。
func TestActionDefinitionsAreValid(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	defs := reg.List()
	if len(defs) != 2 {
		t.Fatalf("应注册 2 个 Action，实际 %d 个", len(defs))
	}

	byID := map[string]action.Definition{}
	for _, d := range defs {
		byID[d.ID] = d
	}

	ack, ok := byID[ActionAcknowledge]
	if !ok {
		t.Fatalf("缺少 %s", ActionAcknowledge)
	}
	// L0：确认不改配置、不影响运行中的系统，只是记下「有人看见了」。
	if ack.RiskLevel != action.L0 {
		t.Fatalf("%s 风险等级 = %s, want L0", ActionAcknowledge, ack.RiskLevel)
	}
	if ack.Permission != ScopeAcknowledge {
		t.Fatalf("%s 权限 = %s", ActionAcknowledge, ack.Permission)
	}

	silence, ok := byID[ActionSilenceCreate]
	if !ok {
		t.Fatalf("缺少 %s", ActionSilenceCreate)
	}
	// L1：静默会改变系统行为（告警不再投递），与确认不是一回事。
	if silence.RiskLevel != action.L1 {
		t.Fatalf("%s 风险等级 = %s, want L1", ActionSilenceCreate, silence.RiskLevel)
	}
	if silence.Permission != ScopeSilenceManage {
		t.Fatalf("%s 权限 = %s", ActionSilenceCreate, silence.Permission)
	}
	// 两个权限必须不同：静默的爆炸半径比确认大一个量级。
	if ack.Permission == silence.Permission {
		t.Fatal("确认与静默不该共用一个权限")
	}

	for _, d := range defs {
		// L0/L1 才能在 Foundation-A 执行；L2 以上会被内核直接拒绝。
		if d.RiskLevel.RequiresAdvancedControls() {
			t.Fatalf("%s 在 Foundation-A 无法执行（风险等级 %s）", d.ID, d.RiskLevel)
		}
		if len(d.PrincipalTypes) != 1 || d.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 应只允许人类身份，实际 %v", d.ID, d.PrincipalTypes)
		}
	}
}

// TestSilenceCreateRequiredFields：三个字段都必填（Schema 层）。
func TestSilenceCreateRequiredFields(t *testing.T) {
	schema := silenceCreateDef().Schema
	required := map[string]bool{}
	for _, f := range schema.Fields {
		if f.Required {
			required[f.Name] = true
		}
	}
	for _, name := range []string{"rule_key", "duration_minutes", "reason"} {
		if !required[name] {
			t.Fatalf("字段 %s 应为必填", name)
		}
	}
	// 白名单语义：未声明的字段一律拒绝（参数偷渡是权限绕过的常见入口）。
	if err := schema.Validate(map[string]any{
		"rule_key": "", "duration_minutes": 30, "reason": "x", "environment": "production",
	}); err == nil {
		t.Fatal("未声明的 environment 字段应被拒绝——环境只能来自 Principal")
	}
}

// TestSilenceCreateRejectsUnknownRuleKey：拼错的规则键会静默零条告警，
// 而创建者以为已经静默了（宪法 12 条）。必须当场拒绝并给出可选值。
func TestSilenceCreateRejectsUnknownRuleKey(t *testing.T) {
	handler := silenceCreateHandler(nilPoolStore())
	_, err := handler(humanCtx("production"), map[string]any{
		"rule_key": "channel.balance.lo", "duration_minutes": 30, "reason": "上游维护",
	})
	if err == nil {
		t.Fatal("拼错的 rule_key 应被拒绝")
	}
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("错误码 = %s, want INVALID_PARAMS", action.ErrorCode(err))
	}
	// 错误文案要告诉他有哪些键——只说「你写错了」帮不上忙。
	if !strings.Contains(err.Error(), RuleChannelBalanceLow) {
		t.Fatalf("错误应列出可选规则键: %v", err)
	}
}

// TestSilenceCreateAcceptsEmptyRuleKeyAsGlobal：空串是「全局静默」这个
// 明确的选择，不是漏填。
func TestSilenceCreateAcceptsEmptyRuleKeyAsGlobal(t *testing.T) {
	// 走到建库那一步会因为 nil pool 而 panic，所以只验证它**通过了**校验：
	// 用一个必然更早失败的 duration 把它拦在建库之前是不行的（那会掩盖本意），
	// 改为直接验证 KnownRuleKey 的分支语义。
	if KnownRuleKey("") {
		t.Fatal("空串不该被当成一个已注册的规则键")
	}
	handler := silenceCreateHandler(nilPoolStore())
	// 空 rule_key + 非法 duration：如果空 rule_key 被误判成「未注册」，
	// 报的会是 rule_key 的错；正确实现应该报 duration 的错。
	_, err := handler(humanCtx("production"), map[string]any{
		"rule_key": "", "duration_minutes": 0, "reason": "全站发布窗口",
	})
	if err == nil {
		t.Fatal("duration_minutes=0 应被拒绝")
	}
	if strings.Contains(err.Error(), "rule_key") {
		t.Fatalf("空 rule_key 应被当成全局静默而非拼错，实际报了 rule_key 的错: %v", err)
	}
	if !strings.Contains(err.Error(), "duration_minutes") {
		t.Fatalf("应报 duration_minutes 的错: %v", err)
	}
}

// TestSilenceCreateRejectsOutOfRangeDuration：一个「静默 99999 分钟」的窗口
// 等于永久关掉这条规则，而且没有任何东西会提醒有人去解除它。
func TestSilenceCreateRejectsOutOfRangeDuration(t *testing.T) {
	handler := silenceCreateHandler(nilPoolStore())
	for _, minutes := range []any{0, -30, maxSilenceMinutes + 1, 525600} {
		_, err := handler(humanCtx("production"), map[string]any{
			"rule_key": RuleMetricSyncFailed, "duration_minutes": minutes, "reason": "上游维护",
		})
		if err == nil {
			t.Fatalf("duration_minutes=%v 应被拒绝", minutes)
		}
		if action.ErrorCode(err) != action.CodeInvalidParams {
			t.Fatalf("duration_minutes=%v 错误码 = %s", minutes, action.ErrorCode(err))
		}
	}
}

// TestSilenceCreateRejectsBlankReason：没有理由的静默在事后复盘时
// 与「有人手滑」不可区分。库层 CHECK 也拦，这里是第一道。
func TestSilenceCreateRejectsBlankReason(t *testing.T) {
	handler := silenceCreateHandler(nilPoolStore())
	for _, reason := range []string{"", "   ", "\t\n"} {
		_, err := handler(humanCtx("production"), map[string]any{
			"rule_key": RuleMetricSyncFailed, "duration_minutes": 30, "reason": reason,
		})
		if err == nil {
			t.Fatalf("reason=%q 应被拒绝", reason)
		}
	}
}

// TestAcknowledgeRejectsMalformedID：不是 UUID 的 alert_id 当场 400。
func TestAcknowledgeRejectsMalformedID(t *testing.T) {
	handler := acknowledgeHandler(nilPoolStore())
	_, err := handler(humanCtx("production"), map[string]any{"alert_id": "not-a-uuid"})
	if err == nil {
		t.Fatal("非法 UUID 应被拒绝")
	}
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("错误码 = %s, want INVALID_PARAMS", action.ErrorCode(err))
	}
}

// TestHandlersRejectMissingPrincipal：没有身份时不该走到任何写路径。
// 环境与 created_by 都来自 Principal，不能由参数自称。
func TestHandlersRejectMissingPrincipal(t *testing.T) {
	ctx := context.Background()
	if _, err := acknowledgeHandler(nilPoolStore())(ctx, map[string]any{"alert_id": "x"}); err == nil {
		t.Fatal("缺少身份时确认应失败")
	}
	if _, err := silenceCreateHandler(nilPoolStore())(ctx, map[string]any{
		"rule_key": "", "duration_minutes": 30, "reason": "x",
	}); err == nil {
		t.Fatal("缺少身份时静默应失败")
	}
}

// TestHandlersRejectUnboundStore：只登记声明的注册表实例被调用时必须
// 明确报错，而不是静默成功。
func TestHandlersRejectUnboundStore(t *testing.T) {
	ctx := humanCtx("production")
	if _, err := acknowledgeHandler(nil)(ctx, map[string]any{"alert_id": "x"}); err == nil {
		t.Fatal("未绑定 store 的 handler 应报错")
	}
	if _, err := silenceCreateHandler(nil)(ctx, map[string]any{
		"rule_key": "", "duration_minutes": 30, "reason": "x",
	}); err == nil {
		t.Fatal("未绑定 store 的 handler 应报错")
	}
}

// TestSilenceSummaryCarriesReason：静默的理由必须进审计链——事后复盘的
// 第一个问题就是「当时为什么静默」。
func TestSilenceSummaryCarriesReason(t *testing.T) {
	s := Silence{RuleKey: RuleMetricSyncFailed, Environment: "production", Reason: "上游维护窗口"}
	got := silenceSummary(s)
	if got["reason"] != "上游维护窗口" {
		t.Fatalf("审计摘要缺少理由: %v", got)
	}
	if got["rule_key"] != RuleMetricSyncFailed || got["environment"] != "production" {
		t.Fatalf("审计摘要不完整: %v", got)
	}
}

// TestAlertSummaryOmitsDetail：detail 每轮评估都会刷新（余额数字会变），
// 进审计链只会给每条确认事件带一段与本次动作无关的噪声。
func TestAlertSummaryOmitsDetail(t *testing.T) {
	a := testAlert()
	a.Detail = "余额 499999 低于阈值 500000"
	got := alertSummary(a)
	if _, has := got["detail"]; has {
		t.Fatalf("审计摘要不该带 detail: %v", got)
	}
	for _, key := range []string{"rule_key", "dedup_key", "severity", "status", "fire_count"} {
		if _, has := got[key]; !has {
			t.Fatalf("审计摘要缺少 %s: %v", key, got)
		}
	}
}
