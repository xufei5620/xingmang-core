package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/integration"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// --- 假依赖 -------------------------------------------------------------

type fakeAPIClients struct {
	items []integration.APIClient
	err   error
}

func (f fakeAPIClients) ListAPIClients(context.Context) ([]integration.APIClient, error) {
	return f.items, f.err
}

type fakeAutomationRules struct {
	items []integration.AutomationRule
	err   error
}

func (f fakeAutomationRules) ListAutomationRules(context.Context) ([]integration.AutomationRule, error) {
	return f.items, f.err
}

type fakeCallers struct {
	page action.CallerActivityPage
	err  error
	// gotEnvironment / gotSince 记录被调用时的入参，供跨环境与窗口的断言。
	gotEnvironment string
	gotSince       time.Time
}

func (f *fakeCallers) AggregateCallers(_ context.Context, environment string, since time.Time) (action.CallerActivityPage, error) {
	f.gotEnvironment, f.gotSince = environment, since
	return f.page, f.err
}

var fixedNow = time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)

func integrationRouter(t *testing.T, clients APIClientLister, callers CallerAggregator,
	rules AutomationRuleLister, reg *action.Registry) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	if reg == nil {
		reg = action.NewRegistry()
	}
	return NewRouter(Deps{
		Logger:          discardLogger(),
		Service:         "platform-api",
		Environment:     "development",
		DB:              fakePinger{},
		Resolver:        res,
		Kernel:          &fakeExecutor{},
		ActionRegistry:  reg,
		APIClients:      clients,
		CallerActivity:  callers,
		AutomationRules: rules,
		IntegrationNow:  func() time.Time { return fixedNow },
	})
}

func getJSON(t *testing.T, h http.Handler, path, scopes string, out any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("解析响应失败: %v，body=%s", err, rec.Body.String())
		}
	}
	return rec
}

func sampleClientRow(principalID, name string) integration.APIClient {
	return integration.APIClient{
		ID:            uuid.New(),
		PrincipalID:   principalID,
		PrincipalType: principal.TypeService,
		DisplayName:   name,
		Status:        integration.ClientActive,
		Environment:   "development",
		CreatedAt:     fixedNow.Add(-48 * time.Hour),
		CreatedBy:     "staff_alice",
		UpdatedAt:     fixedNow.Add(-48 * time.Hour),
		UpdatedBy:     "staff_alice",
	}
}

// --- 对账 ---------------------------------------------------------------

// TestAPIClientsReconcilesRegistryAgainstObserved 是这一格的全部价值所在：
// 登记簿与 action_run 里实际观测到的调用方两侧都给出来，于是
// 「登记了却从没来过」与「来过却没登记」这两个真问题各有一个落点。
//
// 合成一张表就答不出这两问了——那也正是这条用例要挡住的重构。
func TestAPIClientsReconcilesRegistryAgainstObserved(t *testing.T) {
	registered := sampleClientRow("svc-billing-sync", "对账同步")
	dormant := sampleClientRow("svc-legacy-import", "老导入任务")
	callers := &fakeCallers{page: action.CallerActivityPage{Items: []action.CallerActivity{
		{
			PrincipalID: "svc-billing-sync", PrincipalType: "SERVICE",
			RunCount: 12, FailedCount: 1,
			FirstSeenAt:  fixedNow.Add(-30 * time.Hour),
			LastSeenAt:   fixedNow.Add(-2 * time.Hour),
			LastActionID: "finance.upstream_account.set", LastStatus: "succeeded",
		},
		{
			PrincipalID: "staff_bob", PrincipalType: "HUMAN",
			RunCount: 3, FailedCount: 0,
			FirstSeenAt:  fixedNow.Add(-5 * time.Hour),
			LastSeenAt:   fixedNow.Add(-1 * time.Hour),
			LastActionID: "server.asset.set", LastStatus: "succeeded",
		},
	}}}
	h := integrationRouter(t, fakeAPIClients{items: []integration.APIClient{registered, dormant}},
		callers, fakeAutomationRules{}, nil)

	var body struct {
		Items []struct {
			PrincipalID string `json:"principal_id"`
			Observed    *struct {
				RunCount     int64  `json:"run_count"`
				FailedCount  int64  `json:"failed_count"`
				LastActionID string `json:"last_action_id"`
			} `json:"observed"`
		} `json:"items"`
		Unregistered []struct {
			PrincipalID string `json:"principal_id"`
			RunCount    int64  `json:"run_count"`
		} `json:"unregistered"`
		WindowDays        int    `json:"window_days"`
		ObservedSince     string `json:"observed_since"`
		ObservedSource    string `json:"observed_source"`
		ObservedNote      string `json:"observed_note"`
		RegistryNote      string `json:"registry_note"`
		ObservedTruncated bool   `json:"observed_truncated"`
	}
	rec := getJSON(t, h, "/api/v1/integration/api-clients", integration.ScopeRead, &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}

	if len(body.Items) != 2 {
		t.Fatalf("登记簿应有 2 行，实际 %d", len(body.Items))
	}
	// 有观测的那一条要带上活动。
	if body.Items[0].PrincipalID != "svc-billing-sync" || body.Items[0].Observed == nil {
		t.Fatalf("登记且调用过的那条应带 observed: %+v", body.Items[0])
	}
	if body.Items[0].Observed.RunCount != 12 || body.Items[0].Observed.FailedCount != 1 {
		t.Fatalf("观测计数应原样带出: %+v", body.Items[0].Observed)
	}
	if body.Items[0].Observed.LastActionID != "finance.upstream_account.set" {
		t.Fatalf("最近一次 Action 应原样带出: %+v", body.Items[0].Observed)
	}
	// 「登记了却从没来过」：observed 必须是 null，不能编一个 0 行的活动——
	// 一个 run_count=0 的活动对象与「窗口内真的跑了 0 次」长得一模一样。
	if body.Items[1].PrincipalID != "svc-legacy-import" || body.Items[1].Observed != nil {
		t.Fatalf("登记但没观测到的那条 observed 应为 null: %+v", body.Items[1])
	}
	// 「来过却没登记」。
	if len(body.Unregistered) != 1 || body.Unregistered[0].PrincipalID != "staff_bob" {
		t.Fatalf("未登记调用方应恰好是 staff_bob，实际 %+v", body.Unregistered)
	}
	if body.Unregistered[0].RunCount != 3 {
		t.Fatalf("未登记调用方的计数应带出，实际 %+v", body.Unregistered[0])
	}

	// 窗口与来源随响应下发：读数与它的限定必须同源（宪法 12 条）。
	if body.WindowDays != 7 {
		t.Fatalf("默认窗口应是 7 天，实际 %d", body.WindowDays)
	}
	wantSince := fixedNow.Add(-7 * 24 * time.Hour)
	if !callers.gotSince.Equal(wantSince) {
		t.Fatalf("汇总起点应是 %s，实际 %s", wantSince, callers.gotSince)
	}
	if body.ObservedSince != wantSince.Format(time.RFC3339) {
		t.Fatalf("observed_since 应与实际起点一致，实际 %q", body.ObservedSince)
	}
	if body.ObservedSource != "action.action_run" {
		t.Fatalf("observed_source 应是 action.action_run，实际 %q", body.ObservedSource)
	}
	if body.ObservedTruncated {
		t.Fatal("未截断时 observed_truncated 应为 false")
	}
	// 文案逐字：这两句是这一页唯一能防止误读的东西，改了必须有人看见。
	if !strings.Contains(body.ObservedNote, "只统计经 Action 内核的**写操作**") ||
		!strings.Contains(body.ObservedNote, "不能说明它没来过") {
		t.Fatalf("observed_note 必须说清覆盖面边界，实际 %q", body.ObservedNote)
	}
	if !strings.Contains(body.RegistryNote, "登记簿不是授权面") ||
		!strings.Contains(body.RegistryNote, "停用也不会让任何请求被拒绝") {
		t.Fatalf("registry_note 必须说清登记簿不授权，实际 %q", body.RegistryNote)
	}
}

// TestAPIClientsUsesCallerEnvironment 钉住环境不由请求参数自称（宪法 15 条）。
func TestAPIClientsUsesCallerEnvironment(t *testing.T) {
	callers := &fakeCallers{}
	h := integrationRouter(t, fakeAPIClients{}, callers, fakeAutomationRules{}, nil)

	// 正向锚点：不带 environment 时，用的是调用者身份的环境。
	if rec := getJSON(t, h, "/api/v1/integration/api-clients", integration.ScopeRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("锚点：期望 200，实际 %d", rec.Code)
	}
	if callers.gotEnvironment != "development" {
		t.Fatalf("应使用调用者身份的环境，实际 %q", callers.gotEnvironment)
	}

	// 自称另一个环境要被拒，而不是静默读到别处的数据。
	callers.gotEnvironment = ""
	rec := getJSON(t, h, "/api/v1/integration/api-clients?environment=production", integration.ScopeRead, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨环境读取应 403，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if callers.gotEnvironment != "" {
		t.Fatalf("被拒之后不该已经查过库，实际查了 %q", callers.gotEnvironment)
	}
}

func TestAPIClientsRejectsBadWindow(t *testing.T) {
	h := integrationRouter(t, fakeAPIClients{}, &fakeCallers{}, fakeAutomationRules{}, nil)
	for _, bad := range []string{"0", "91", "-1", "abc"} {
		rec := getJSON(t, h, "/api/v1/integration/api-clients?window_days="+bad, integration.ScopeRead, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("window_days=%s 应 400，实际 %d body=%s", bad, rec.Code, rec.Body.String())
		}
	}
	// 对照组：边界内的值必须通过——否则上面那一串 400 可能只是这个端点
	// 整体坏了。
	if rec := getJSON(t, h, "/api/v1/integration/api-clients?window_days=90",
		integration.ScopeRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("对照组：window_days=90 应 200，实际 %d", rec.Code)
	}
}

// TestAPIClientsRequiresIntegrationScope 钉住读侧那道新 scope：
// 拿 registry.read（默认发给 staff 的那个）读不到这张表。
func TestAPIClientsRequiresIntegrationScope(t *testing.T) {
	h := integrationRouter(t, fakeAPIClients{}, &fakeCallers{}, fakeAutomationRules{}, nil)
	if rec := getJSON(t, h, "/api/v1/integration/api-clients", "registry.read", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("只有 registry.read 应 403（调用方登记簿是一张授权面的地图），实际 %d", rec.Code)
	}
	// 对照组：换成正确的 scope 必须 200。
	if rec := getJSON(t, h, "/api/v1/integration/api-clients",
		integration.ScopeRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("对照组：带 %s 应 200，实际 %d", integration.ScopeRead, rec.Code)
	}
}

// TestAPIClientsNotMountedWithoutBothHalves 钉住「缺一半就不挂」的纪律：
// 只有登记簿而没有观测侧时，这个端点根本不存在（404），而不是返回一张
// 什么也没校验的台账。
func TestAPIClientsNotMountedWithoutBothHalves(t *testing.T) {
	// 正向锚点：两半都在时确实挂着。
	full := integrationRouter(t, fakeAPIClients{}, &fakeCallers{}, fakeAutomationRules{}, nil)
	if rec := getJSON(t, full, "/api/v1/integration/api-clients",
		integration.ScopeRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("锚点：两半都在时应 200，实际 %d", rec.Code)
	}
	half := integrationRouter(t, fakeAPIClients{}, nil, fakeAutomationRules{}, nil)
	if rec := getJSON(t, half, "/api/v1/integration/api-clients",
		integration.ScopeRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("只有登记簿一半时端点不该存在，实际 %d", rec.Code)
	}
}

// --- 自动化规则 ---------------------------------------------------------

func sampleRuleRow(name, targetID, targetVersion string) integration.AutomationRule {
	return integration.AutomationRule{
		ID: uuid.New(), Name: name,
		TriggerKind: integration.TriggerEvent, TriggerDetail: "alerts.alert.opened",
		TargetActionID: targetID, TargetActionVersion: targetVersion,
		Status: integration.RuleRegistered, Environment: "development",
		CreatedAt: fixedNow, CreatedBy: "staff_alice",
		UpdatedAt: fixedNow, UpdatedBy: "staff_alice",
	}
}

type automationRulesBody struct {
	Items []struct {
		Name                   string `json:"name"`
		Status                 string `json:"status"`
		TargetActionID         string `json:"target_action_id"`
		TargetActionRegistered bool   `json:"target_action_registered"`
		TargetActionRiskLevel  string `json:"target_action_risk_level"`
	} `json:"items"`
	AutomaticExecution bool   `json:"automatic_execution"`
	ExecutionNote      string `json:"execution_note"`
}

// TestAutomationRulesReportNoAutomaticExecution 钉住这一格最要紧的一句话：
// 端点如实告诉前端「规则不会自动执行」，并给出为什么。
//
// 这是**缺席型主张的可见化**：光靠「没有执行器」这个事实，页面上没有任何
// 东西能阻止人以为配了就生效。变异验证见交接文档——把
// integration.AutomaticExecution 改成返回 true，本用例变红，而
// TestRegisteringAutomationRuleDoesNotExecuteTargetAction 保持绿
// （那条证明的是「没有发生」，与这条报告的是两件事）。
func TestAutomationRulesReportNoAutomaticExecution(t *testing.T) {
	rules := fakeAutomationRules{items: []integration.AutomationRule{
		sampleRuleRow("余额低时补充", "finance.upstream_account.set", "1"),
	}}
	h := integrationRouter(t, fakeAPIClients{}, &fakeCallers{}, rules, nil)

	var body automationRulesBody
	rec := getJSON(t, h, "/api/v1/integration/automation-rules", integration.ScopeRead, &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	// 正向锚点先立住：这一条规则确实被读出来了。
	if len(body.Items) != 1 || body.Items[0].Name != "余额低时补充" {
		t.Fatalf("锚点：应读出那一条规则，实际 %+v", body.Items)
	}
	// 即便这条规则的登记状态是 registered，也不会执行。
	if body.Items[0].Status != "registered" {
		t.Fatalf("锚点：状态应原样带出，实际 %q", body.Items[0].Status)
	}
	if body.AutomaticExecution {
		t.Fatal("automatic_execution 必须是 false：平台没有规则执行器（ADMIN-IA §5.4.1）")
	}
	// 文案逐字：只给一个 false 的话，前端渲染成什么样全凭它自己发挥。
	for _, want := range []string{
		"规则登记在此，但当前不会自动执行",
		"平台没有规则执行器",
		"L2 及以上必须有人批准，而机器凑不出审批人",
	} {
		if !strings.Contains(body.ExecutionNote, want) {
			t.Fatalf("execution_note 缺少「%s」，实际 %q", want, body.ExecutionNote)
		}
	}
}

// TestAutomationRulesMarkUnregisteredTargets 钉住那条刻意的宽松在**读侧**
// 的补偿：写路径允许登记一个尚未注册的 Action，读路径就必须把这件事说出来。
//
// 不说的话，一条指向不存在 Action 的规则看起来与一条正常规则一模一样。
func TestAutomationRulesMarkUnregisteredTargets(t *testing.T) {
	reg := action.NewRegistry()
	if err := reg.Register(action.Definition{
		ID: "demo.thing.set", Version: "1", RiskLevel: action.L1,
		Permission: "demo.manage", Environments: []string{"development"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}, func(context.Context, map[string]any) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("注册示例 Action: %v", err)
	}
	rules := fakeAutomationRules{items: []integration.AutomationRule{
		sampleRuleRow("指向已注册", "demo.thing.set", "1"),
		sampleRuleRow("指向不存在", "nowhere.no_such.action", "1"),
		sampleRuleRow("版本对不上", "demo.thing.set", "2"),
	}}
	h := integrationRouter(t, fakeAPIClients{}, &fakeCallers{}, rules, reg)

	var body automationRulesBody
	if rec := getJSON(t, h, "/api/v1/integration/automation-rules",
		integration.ScopeRead, &body); rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	if len(body.Items) != 3 {
		t.Fatalf("应读出 3 条，实际 %d", len(body.Items))
	}
	if !body.Items[0].TargetActionRegistered {
		t.Fatal("指向已注册 Action 的那条应标为已注册")
	}
	if body.Items[0].TargetActionRiskLevel != "L1" {
		t.Fatalf("已注册时应带出目标此刻声明的等级，实际 %q", body.Items[0].TargetActionRiskLevel)
	}
	if body.Items[1].TargetActionRegistered {
		t.Fatal("指向不存在 Action 的那条不该标为已注册")
	}
	if body.Items[1].TargetActionRiskLevel != "" {
		t.Fatalf("未注册时不该编一个等级，实际 %q", body.Items[1].TargetActionRiskLevel)
	}
	// 版本对不上也算没注册——注册表是按 ID+版本查的。
	if body.Items[2].TargetActionRegistered {
		t.Fatal("版本对不上的那条不该标为已注册")
	}
}

// TestListingAutomationRulesDoesNotExecuteTargetAction 是
// TestRegisteringAutomationRuleDoesNotExecuteTargetAction 在**读侧**的对应物。
//
// 两条各守一段：那条守写路径（登记不触发），这条守读路径（列出不触发）。
// 读路径手里恰恰握着注册表——Lookup 返回的第二个值就是可执行的 handler，
// 顺手把它接起来是这一格最可能出现的一次滑坡，所以必须有人钉着。
//
// 变异验证（记在交接文档）：把 ListAutomationRulesHandler 里被丢弃的
// handler 接起来并调用，本用例变红且红在计数断言上；对照组保持绿。
func TestListingAutomationRulesDoesNotExecuteTargetAction(t *testing.T) {
	executed := 0
	reg := action.NewRegistry()
	if err := reg.Register(action.Definition{
		ID: "demo.thing.set", Version: "1", RiskLevel: action.L1,
		Permission: "demo.manage", Environments: []string{"development"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}, func(context.Context, map[string]any) (any, error) {
		executed++
		return nil, nil
	}); err != nil {
		t.Fatalf("注册示例 Action: %v", err)
	}
	rules := fakeAutomationRules{items: []integration.AutomationRule{
		sampleRuleRow("指向已注册", "demo.thing.set", "1"),
	}}
	h := integrationRouter(t, fakeAPIClients{}, &fakeCallers{}, rules, reg)

	var body automationRulesBody
	if rec := getJSON(t, h, "/api/v1/integration/automation-rules",
		integration.ScopeRead, &body); rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	// 正向锚点：这一次请求真的走到了「查到目标 Action」那一步——
	// 没有它，"executed == 0" 也可能只是因为根本没读到任何规则。
	if len(body.Items) != 1 || !body.Items[0].TargetActionRegistered {
		t.Fatalf("锚点：应读出一条并查到它的目标 Action，实际 %+v", body.Items)
	}
	// 目标断言，同步、无 waitFor：要证明的是「没有发生」。
	if executed != 0 {
		t.Fatalf("列出规则不该执行目标 Action，实际执行了 %d 次", executed)
	}

	// 对照组：同一个注册表里的同一个 handler 直接调一次必须自增——
	// 否则上面那条「恒为 0」什么也没证明。
	_, handler, ok := reg.Lookup("demo.thing.set", "1")
	if !ok {
		t.Fatal("对照组：应查得到那个 Action")
	}
	if _, err := handler(context.Background(), nil); err != nil {
		t.Fatalf("对照组：直接调用失败 %v", err)
	}
	if executed != 1 {
		t.Fatalf("对照组：直接调用后计数应为 1，实际 %d", executed)
	}
}
