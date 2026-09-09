package alerts

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
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

// TestActionDefinitionsAreValid：四个 Action 的声明本身必须过内核的校验。
func TestActionDefinitionsAreValid(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	defs := reg.List()
	if len(defs) != 4 {
		t.Fatalf("应注册 4 个 Action，实际 %d 个", len(defs))
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

	// **核对与撤销必须成对存在。** 一个能建立永久抑制器的 Action，如果没有
	// 与它同时上线的解除路径，那个抑制器就只能靠外部事件（上游再升一次版本）
	// 解除——而 acknowledge 要求 version 与当轮观测逐字相同，连「用另一个值
	// 覆盖掉」这条路都走不通。这正是任务书致命清单里的「无恢复路径的闩」。
	revoke, ok := byID[ActionRevokeUpstreamVersion]
	if !ok {
		t.Fatalf("缺少 %s：已核对版本不能是一个撤不掉的抑制器", ActionRevokeUpstreamVersion)
	}
	if revoke.RiskLevel != action.L1 {
		t.Fatalf("%s 风险等级 = %s, want L1", ActionRevokeUpstreamVersion, revoke.RiskLevel)
	}
	if revoke.Permission != ScopeAcknowledge {
		t.Fatalf("%s 权限 = %s, want %s", ActionRevokeUpstreamVersion, revoke.Permission, ScopeAcknowledge)
	}
	// 撤销**不带 version 参数**：让调用方再报一次版本号只会多出一种失败形态
	// （「上游已经又升级了，所以你撤不掉上一次的核对」），而那恰恰是最需要
	// 撤销的时刻。
	for _, f := range revoke.Schema.Fields {
		if f.Name == "version" {
			t.Fatal("撤销不该要求 version：撤的是「此刻记着的那条核对」")
		}
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

// --- alerts.upstream_version.acknowledge ---

// fakeObservationReader 是 Handler 用到的只读观测来源。
type fakeObservationReader struct {
	obs map[string]ops.Observation
	err error
}

func (f *fakeObservationReader) Get(_ context.Context, metricKey, _ string) (ops.Observation, error) {
	if f.err != nil {
		return ops.Observation{}, f.err
	}
	o, ok := f.obs[metricKey]
	if !ok {
		return ops.Observation{}, pgx.ErrNoRows
	}
	return o, nil
}

func probeReader(metricKey, version string) *fakeObservationReader {
	return &fakeObservationReader{obs: map[string]ops.Observation{
		metricKey: {
			MetricKey: metricKey, Source: "sub2api-prod", Environment: "production",
			Status: ops.SyncOK, Value: map[string]any{"version": version},
		},
	}}
}

// TestAcknowledgeUpstreamVersionDefinition：等级、权限、身份、Schema 白名单。
//
// **L1 不许抬。** L2 及以上会把整包 params 冻进 core.approval_request.params_json
// 并由 GET /api/v1/approvals 原样回给每一个能看审批队列的人——只要参数里能塞
// 自由文本，抬级就等于给凭据开一条展示通道。
func TestAcknowledgeUpstreamVersionDefinition(t *testing.T) {
	def := acknowledgeUpstreamVersionDef()
	if def.RiskLevel != action.L1 {
		t.Fatalf("风险等级 = %s, want L1", def.RiskLevel)
	}
	// 复用既有 scope：新增 scope 要同时改 oidcauth/rolemap.go 与前端权限清单
	// （本片不得改 web/），而它换不来任何实际的信息隔离。
	if def.Permission != ScopeAcknowledge {
		t.Fatalf("权限 = %s, want %s", def.Permission, ScopeAcknowledge)
	}
	if len(def.PrincipalTypes) != 1 || def.PrincipalTypes[0] != principal.TypeHuman {
		t.Fatalf("应只允许人类身份，实际 %v", def.PrincipalTypes)
	}
	// ID 必须是三段式，否则 action.Definition.Validate 会拒绝注册、进程起不来。
	if err := def.Validate(); err != nil {
		t.Fatalf("声明本身不合法（ID 三段式？）: %v", err)
	}
	if strings.Count(def.ID, ".") < 2 {
		t.Fatalf("Action ID 必须是 <域>.<资源>.<动作> 三段式: %s", def.ID)
	}

	// metric_key **不写 Enum**：R6 的判据是「这条观测里有没有 version」而不是
	// 指标键叫什么。手列一份键清单会让「将来多一个连接器探测自动被覆盖」
	// 这条性质失效；存在性校验在 Handler 里走 ops.KnownMetricKey（注册出来的，
	// 不是手抄的）。
	for _, f := range def.Schema.Fields {
		if f.Name == "metric_key" && len(f.Enum) != 0 {
			t.Fatalf("metric_key 不该写死枚举：%v", f.Enum)
		}
	}
	// 白名单语义：环境只能来自 Principal。
	if err := def.Schema.Validate(map[string]any{
		"metric_key": "sub2api.connector.health", "version": "0.2.3", "environment": "production",
	}); err == nil {
		t.Fatal("未声明的 environment 字段应被拒绝——环境只能来自 Principal")
	}
}

// TestAcknowledgeUpstreamVersionRejectsCredentialShapedParams：参数装不下凭据。
//
// 这里断言的是第一道闸（形态）。真正的收口在下一条测试：version 必须与平台
// 自己观测到的值逐字相同才会落库。两道都要有——只有形态校验的话，一个形态
// 合法但与观测不符的值会被原样存进库。
func TestAcknowledgeUpstreamVersionRejectsCredentialShapedParams(t *testing.T) {
	handler := acknowledgeUpstreamVersionHandler(nilPoolStore(),
		probeReader("sub2api.connector.health", "0.2.3"))

	cases := []struct {
		name    string
		version string
	}{
		{"凭据引用", "secret://alerts/telegram-bot"},
		{"telegram token 形态", "8987654321:AAE2ETESTtokenNOTreal00000000000000000"},
		{"带空白", "0.2.3 extra"},
		{"带换行", "0.2.3\nX-Injected: 1"},
		{"全角字符", "０.２.３"},
		{"超长", strings.Repeat("1", maxAckVersionBytes+1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := handler(humanCtx("production"), map[string]any{
				"metric_key": "sub2api.connector.health", "version": c.version,
			})
			if err == nil {
				t.Fatalf("version=%q 应被拒绝", c.version)
			}
			if action.ErrorCode(err) != action.CodeInvalidParams {
				t.Fatalf("错误码 = %s, want INVALID_PARAMS", action.ErrorCode(err))
			}
		})
	}

	// 超长要写出上限数字——只说「太长了」帮不上忙。
	_, err := handler(humanCtx("production"), map[string]any{
		"metric_key": "sub2api.connector.health",
		"version":    strings.Repeat("1", maxAckVersionBytes+1),
	})
	if !strings.Contains(err.Error(), fmt.Sprint(maxAckVersionBytes)) {
		t.Fatalf("超长的错误文案应写出上限：%v", err)
	}

	// 形态合法的版本不该被形态闸拦下——否则上面那几条「被拒绝」可能只是因为
	// 这个 Handler 把**所有**输入都拒了，断言就成了恒真。
	//
	// 让观测报一个永远对不上的版本，这样合法输入会停在 CONFLICT（形态闸之后、
	// 写库之前），既证明形态通过了，又不需要一个真库。
	neverMatches := acknowledgeUpstreamVersionHandler(nilPoolStore(),
		probeReader("sub2api.connector.health", "999.999.999"))
	for _, ok := range []string{"0.2.3", "v0.2.3-rc.1", "1", "0.1.179"} {
		_, err := neverMatches(humanCtx("production"), map[string]any{
			"metric_key": "sub2api.connector.health", "version": ok,
		})
		if action.ErrorCode(err) != action.CodeConflict {
			t.Fatalf("version=%q 形态合法，应走到版本比对（CONFLICT），实际 %s：%v",
				ok, action.ErrorCode(err), err)
		}
	}
}

// TestAcknowledgeUpstreamVersionScopeComesFromObservationsNotAWhitelist：
// 结束这条告警的范围必须与产生它的范围**同源**。
//
// R6 的命中范围是**发现出来的**：判据是这条观测里有没有 version，不是它的
// 键叫什么——将来多一个连接器探测，它自动就被覆盖。而这个 Action 此前用
// ops.KnownMetricKey 做准入闸，那是一份**手列**的白名单
// （ops/freshness.go 的 registeredMetrics），落库侧只校验 ValidMetricKey，
// 所以一条未注册的指标观测完全可以存在。两个范围一旦漂开就会出现
// 「告警响得起来、但按钮点不动」——那条告警又回到本片要消灭的状态：
// 只能等旧样本被挤出窗口后自己消失。
//
// 旧实现下这条测试是红的：未注册但确实有观测的那条会拿到 INVALID_PARAMS。
func TestAcknowledgeUpstreamVersionScopeComesFromObservationsNotAWhitelist(t *testing.T) {
	const unregistered = "futureconnector.connector.health"
	if ops.KnownMetricKey(unregistered) {
		t.Fatalf("用例自身失效：%s 已经被注册进白名单了，换一个键", unregistered)
	}

	// 未注册，但这个环境下**确实观测到了**一个上游版本 → 必须走得到版本比对。
	//
	// 让观测报一个永远对不上的版本，这样合法输入会停在 CONFLICT（准入闸
	// 之后、写库之前）——既证明这条未注册的指标通过了准入，又不需要真库
	// （nilPoolStore 一旦写库就会因为没有连接池而 panic）。
	handler := acknowledgeUpstreamVersionHandler(nilPoolStore(), probeReader(unregistered, "999.999.999"))
	_, err := handler(humanCtx("production"), map[string]any{
		"metric_key": unregistered, "version": "0.2.3",
	})
	if code := action.ErrorCode(err); code != action.CodeConflict {
		t.Fatalf("未注册但有观测的指标应走到版本比对（CONFLICT），实际 %s：%v", code, err)
	}

	// 拼错的键：没有观测 → PRECONDITION_FAILED（「没有可核对的东西」），
	// 而且文案要把已注册清单当**提示**给出来——拼错照样要拿到有用的报错，
	// 只是那份清单不再是准入闸。
	_, err = handler(humanCtx("production"), map[string]any{
		"metric_key": "sub2api.connector.healt", "version": "0.2.3",
	})
	if action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("错误码 = %s, want PRECONDITION_FAILED", action.ErrorCode(err))
	}
	if !strings.Contains(err.Error(), "sub2api.connector.health") {
		t.Fatalf("错误应把已注册指标当提示列出来: %v", err)
	}

	// 形态非法仍然是 INVALID_PARAMS：那种键连落库都通不过，不必去查观测。
	_, err = handler(humanCtx("production"), map[string]any{
		"metric_key": "Sub2API Connector!", "version": "0.2.3",
	})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("形态非法的键应是 INVALID_PARAMS，实际 %s: %v", action.ErrorCode(err), err)
	}
}

// TestUpstreamVersionActionsArePinnedToL1BecauseFreeTextParamsExist：
// 这两个 Action **永久锁定 L1**，理由要说对。
//
// 声明注释的第一版写的是「本 Action 的两个参数在形状上装不下凭据」——它把
// note 数漏了：note 是 ≤200 字节的自由文本，除长度外没有任何形态校验，
// 形状上**装得下**凭据。L1 这个结论是对的，但理由反了：正因为装得下，
// 它才**必须**留在 L1（L2+ 会把整包 params 冻进 core.approval_request.
// params_json 并回给每个审批人）。一条把自己的理由说错了的注释，比没有注释
// 更容易被拿去做相反的决定。
func TestUpstreamVersionActionsArePinnedToL1BecauseFreeTextParamsExist(t *testing.T) {
	cases := map[string]struct {
		def          action.Definition
		freeTextName string
	}{
		"acknowledge": {acknowledgeUpstreamVersionDef(), "note"},
		"revoke":      {revokeUpstreamVersionDef(), "reason"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.def.RiskLevel != action.L1 {
				t.Fatalf("风险等级 = %s, want L1（params 里有自由文本，抬级会把它冻进审批单展示给人看）",
					c.def.RiskLevel)
			}
			// 自由文本字段确实存在——这条断言是「为什么锁 L1」的那个前提。
			// 哪天它被去掉了，这条会红，届时该重新读一遍那段理由再决定，
			// 而不是让一段过期的理由继续挂着。
			found := false
			for _, f := range c.def.Schema.Fields {
				if f.Name == c.freeTextName {
					found = true
					if len(f.Enum) != 0 {
						t.Fatalf("%s 有枚举约束了？那段「锁 L1」的理由要重写", c.freeTextName)
					}
				}
			}
			if !found {
				t.Fatalf("Schema 里没有 %s：锁在 L1 的理由要重新写", c.freeTextName)
			}
		})
	}
}

// TestAcknowledgeUpstreamVersionRefusesToStoreWhatTheUpstreamDidNotSay 是
// 「参数装不下凭据」的**硬理由**：version 不是「调用方给什么就存什么」。
func TestAcknowledgeUpstreamVersionRefusesToStoreWhatTheUpstreamDidNotSay(t *testing.T) {
	const key = "sub2api.connector.health"

	// 上游自报 0.2.3，调用方要核对 0.2.2 → CONFLICT，文案逐字给出两个版本。
	handler := acknowledgeUpstreamVersionHandler(nilPoolStore(), probeReader(key, "0.2.3"))
	_, err := handler(humanCtx("production"), map[string]any{
		"metric_key": key, "version": "0.2.2",
	})
	if action.ErrorCode(err) != action.CodeConflict {
		t.Fatalf("错误码 = %s, want CONFLICT", action.ErrorCode(err))
	}
	for _, want := range []string{"0.2.3", "0.2.2", "请刷新后再确认"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("CONFLICT 文案缺 %q：%v", want, err)
		}
	}

	// 这条指标没有上游自报版本 → PRECONDITION_FAILED，不是 CONFLICT：
	// 「没有可核对的东西」与「你核对的版本不对」是两件事。
	noVersion := &fakeObservationReader{obs: map[string]ops.Observation{
		key: {MetricKey: key, Environment: "production", Status: ops.SyncOK, Value: map[string]any{}},
	}}
	_, err = acknowledgeUpstreamVersionHandler(nilPoolStore(), noVersion)(
		humanCtx("production"), map[string]any{"metric_key": key, "version": "0.2.3"})
	if action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("错误码 = %s, want PRECONDITION_FAILED", action.ErrorCode(err))
	}
	if !strings.Contains(err.Error(), "没有上游自报版本") {
		t.Fatalf("文案要说清没有可核对的东西：%v", err)
	}

	// 这个环境下压根没有这条观测 → 同样 PRECONDITION_FAILED，而不是 500。
	_, err = acknowledgeUpstreamVersionHandler(nilPoolStore(), &fakeObservationReader{})(
		humanCtx("production"), map[string]any{"metric_key": key, "version": "0.2.3"})
	if action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("错误码 = %s, want PRECONDITION_FAILED", action.ErrorCode(err))
	}
}
