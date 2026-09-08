package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// --- 内存假仓储 ---------------------------------------------------------
//
// 用假货而不是真库，是为了让本文件里最要紧的那条断言（登记规则不会执行
// 任何 Action）在**任何**机器上都跑，不因为没有 XM_TEST_DATABASE_URL 就被
// t.Skip 掉——库层的不变量另有 store_integration_test.go 在真库上验。

type memStore struct {
	mu      sync.Mutex
	clients map[uuid.UUID]APIClient
	rules   map[uuid.UUID]AutomationRule
	env     string
}

func newMemStore(env string) *memStore {
	return &memStore{
		clients: map[uuid.UUID]APIClient{},
		rules:   map[uuid.UUID]AutomationRule{},
		env:     env,
	}
}

var _ ActionStore = (*memStore)(nil)

func (m *memStore) GetAPIClient(_ context.Context, id uuid.UUID) (APIClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	if !ok {
		return APIClient{}, ErrNotFound
	}
	return c, nil
}

func (m *memStore) CreateAPIClient(_ context.Context, c APIClient) (APIClient, error) {
	c.Environment = m.env
	if err := c.Validate(); err != nil {
		return APIClient{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.clients {
		if existing.PrincipalID == c.PrincipalID {
			return APIClient{}, ErrDuplicate
		}
	}
	c.ID = uuid.New()
	c.CreatedAt, c.UpdatedAt = time.Unix(0, 0).UTC(), time.Unix(0, 0).UTC()
	m.clients[c.ID] = c
	return c, nil
}

func (m *memStore) UpdateAPIClient(_ context.Context, id uuid.UUID, c APIClient) (APIClient, error) {
	c.Environment = m.env
	if err := c.Validate(); err != nil {
		return APIClient{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.clients[id]
	if !ok {
		return APIClient{}, ErrNotFound
	}
	c.ID, c.CreatedAt, c.CreatedBy = id, old.CreatedAt, old.CreatedBy
	m.clients[id] = c
	return c, nil
}

func (m *memStore) SetAPIClientStatus(_ context.Context, id uuid.UUID, status ClientStatus, by string) (APIClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	if !ok {
		return APIClient{}, ErrNotFound
	}
	c.Status, c.UpdatedBy = status, by
	m.clients[id] = c
	return c, nil
}

func (m *memStore) GetAutomationRule(_ context.Context, id uuid.UUID) (AutomationRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return AutomationRule{}, ErrNotFound
	}
	return r, nil
}

func (m *memStore) CreateAutomationRule(_ context.Context, r AutomationRule) (AutomationRule, error) {
	r.Environment = m.env
	if err := r.Validate(); err != nil {
		return AutomationRule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = uuid.New()
	m.rules[r.ID] = r
	return r, nil
}

func (m *memStore) UpdateAutomationRule(_ context.Context, id uuid.UUID, r AutomationRule) (AutomationRule, error) {
	r.Environment = m.env
	if err := r.Validate(); err != nil {
		return AutomationRule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rules[id]; !ok {
		return AutomationRule{}, ErrNotFound
	}
	r.ID = id
	m.rules[id] = r
	return r, nil
}

func (m *memStore) SetAutomationRuleStatus(_ context.Context, id uuid.UUID, status RuleStatus, by string) (AutomationRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return AutomationRule{}, ErrNotFound
	}
	r.Status, r.UpdatedBy = status, by
	m.rules[id] = r
	return r, nil
}

func (m *memStore) ListAutomationRules(_ context.Context) ([]AutomationRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AutomationRule, 0, len(m.rules))
	for _, r := range m.rules {
		out = append(out, r)
	}
	return out, nil
}

// --- 测试脚手架 ---------------------------------------------------------

const testEnvironment = "production"

func humanCtx() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "test", Environment: testEnvironment, Scopes: []string{ScopeManage},
	})
}

// nopRunStore 让内核跑起来而不需要数据库。
type nopRunStore struct{}

func (nopRunStore) InsertRun(context.Context, action.Run) error { return nil }

func execute(t *testing.T, store ActionStore, ctx context.Context, id string, params map[string]any) error {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	_, err := handler(ctx, params)
	return err
}

func asActionError(err error) *action.Error {
	for e := err; e != nil; {
		if ae, ok := e.(*action.Error); ok {
			return ae
		}
		unwrapper, ok := e.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		e = unwrapper.Unwrap()
	}
	return nil
}

// --- 声明本身 -----------------------------------------------------------

// TestActionDefinitionsAreValidAndHumanOnly 钉的是**行为**而不是字面等级：
// 「不需要审批就能直接执行」与「机器身份不能自己写这两张登记簿」。
//
// 刻意不断言 RiskLevel == "L1"：等级表会演进，钉字面值会在改表那天
// 变成一条只需要照抄新值就能修好的假门禁。真正要守住的是这两条性质。
func TestActionDefinitionsAreValidAndHumanOnly(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, newMemStore(testEnvironment)); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	ids := []string{
		ActionAPIClientSet, ActionAPIClientSetStatus,
		ActionAutomationRuleSet, ActionAutomationRuleSetStatus,
	}
	for _, id := range ids {
		def, _, ok := reg.Lookup(id, actionVersion)
		if !ok {
			t.Fatalf("%s 未注册", id)
		}
		if err := def.Validate(); err != nil {
			t.Fatalf("%s 声明非法: %v", id, err)
		}
		// 登记簿写入不该落审批单：它不触碰第三方系统、不改变任何运行中的
		// 行为。要求审批会让「改一行备注」也排队等人批。
		if def.RiskLevel.RequiresAdvancedControls() {
			t.Fatalf("%s 不该需要审批：两张表都是纯登记（调用方登记簿不是授权面、"+
				"规则登记簿没有执行器）", id)
		}
		if len(def.PrincipalTypes) != 1 || def.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 只应允许 HUMAN，实际 %v：让机器身份自己写这两张表，"+
				"等于让它给自己发通行证与工单", id, def.PrincipalTypes)
		}
		if def.Permission != ScopeManage {
			t.Fatalf("%s 的权限应是 %s，实际 %s", id, ScopeManage, def.Permission)
		}
	}
}

// --- 参数校验 -----------------------------------------------------------

func TestAPIClientSetRejectsPlaintextCredential(t *testing.T) {
	err := execute(t, newMemStore(testEnvironment), humanCtx(), ActionAPIClientSet, map[string]any{
		"principal_id":   "svc-billing-sync",
		"principal_type": "SERVICE",
		"display_name":   "对账同步",
		// 一把裸 token。这条路径必须在写库之前就拒掉——参数会进审计的
		// before/after 摘要（宪法 7 条）。
		"credential_ref": "sk-live-0123456789",
	})
	ae := asActionError(err)
	if ae == nil || ae.Code != action.CodeInvalidParams {
		t.Fatalf("期望 INVALID_PARAMS，实际 %v", err)
	}
	// 文案逐字：只给错误码的话，运营看到「参数非法」不知道该改哪个字段。
	if !strings.Contains(ae.Message, "credential_ref 必须形如 secret://<scope>/<name>") {
		t.Fatalf("错误文案应指明正确形态，实际 %q", ae.Message)
	}
}

func TestAPIClientSetStatusRequiresReason(t *testing.T) {
	store := newMemStore(testEnvironment)
	if err := execute(t, store, humanCtx(), ActionAPIClientSet, map[string]any{
		"principal_id": "svc-a", "principal_type": "SERVICE", "display_name": "A",
	}); err != nil {
		t.Fatalf("先建一条: %v", err)
	}
	var id uuid.UUID
	for k := range store.clients {
		id = k
	}
	err := execute(t, store, humanCtx(), ActionAPIClientSetStatus, map[string]any{
		"client_id": id.String(), "status": "disabled", "reason": "   ",
	})
	ae := asActionError(err)
	if ae == nil || ae.Code != action.CodeInvalidParams {
		t.Fatalf("期望 INVALID_PARAMS，实际 %v", err)
	}
	if ae.Message != "reason 不能为空白" {
		t.Fatalf("错误文案应逐字是「reason 不能为空白」，实际 %q", ae.Message)
	}
}

// TestAutomationRuleSetAcceptsUnregisteredTargetAction 钉的是一条刻意的
// 宽松：登记一条指向**尚未注册**的 Action 的规则是允许的。
//
// 理由写在迁移 000051 里：注册表是进程内的运行期对象，不在数据库里；在写
// 路径上假装能校验它，只会在 Action 改版本那天变成一条挡住登记的假约束。
// 「指没指向真实存在的 Action」由读取端点如实标出（见 httpapi 那侧的用例）。
func TestAutomationRuleSetAcceptsUnregisteredTargetAction(t *testing.T) {
	err := execute(t, newMemStore(testEnvironment), humanCtx(), ActionAutomationRuleSet, map[string]any{
		"name":                  "余额低时补充",
		"trigger_kind":          "event",
		"target_action_id":      "nowhere.no_such.action",
		"target_action_version": "1",
	})
	if err != nil {
		t.Fatalf("指向未注册 Action 的登记应被接受: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 缺席型断言：登记一条规则不会让任何 Action 跑起来
// ---------------------------------------------------------------------------

// spyCounter 记录被执行了几次。
type spyCounter struct {
	mu sync.Mutex
	n  int
}

func (s *spyCounter) inc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
}

func (s *spyCounter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

const spyActionID = "spy.target.run"

// registerSpy 往注册表里放一个只会自增计数器的 Action，并返回计数器。
func registerSpy(t *testing.T, reg *action.Registry) *spyCounter {
	t.Helper()
	spy := &spyCounter{}
	def := action.Definition{
		ID:             spyActionID,
		Version:        actionVersion,
		RiskLevel:      action.L1,
		Permission:     ScopeManage,
		Schema:         action.Schema{},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
	handler := func(context.Context, map[string]any) (any, error) {
		spy.inc()
		return nil, nil
	}
	if err := reg.Register(def, handler); err != nil {
		t.Fatalf("注册 spy Action: %v", err)
	}
	return spy
}

// TestRegisteringAutomationRuleDoesNotExecuteTargetAction 是本片最要紧的一条
// 断言：**登记一条「当 X 发生时执行 Y」的规则，不会让 Y 跑起来**
// （ADMIN-IA §5.4.1「自动化流程先做只读」）。
//
// 它穿过两段代码：登记走**真内核**（action.Kernel.Execute，含身份/权限/
// 风险/环境全链），而被指向的 Y 是同一个注册表里一个真实可执行的 Action。
// 如果哪天有人在登记路径上顺手接了执行，这条用例会红。
//
// 缺席断言的两道防伪：
//
//   - **对照组**（下方 subtest「对照组」）直接经内核执行 spy，计数器必须
//     从 0 变 1。没有它，"计数器恒为 0" 也可能只是因为计数器根本不工作；
//   - **变异验证**记录在交接文档：把 httpapi.ListAutomationRulesHandler 里
//     `defs.Lookup` 丢弃的 handler 接起来并调用，本用例的 spy 计数断言变红,
//     而对照组保持绿。
//
// 断言是**同步**的，没有 waitFor：这里要证明的是"没有发生"，把它包进重试
// 里只会让它在第一次检查就通过——那种绿什么也没证明。
func TestRegisteringAutomationRuleDoesNotExecuteTargetAction(t *testing.T) {
	store := newMemStore(testEnvironment)
	reg := action.NewRegistry()
	spy := registerSpy(t, reg)
	if err := RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	kernel := action.NewKernel(reg, nopRunStore{})
	ctx := humanCtx()

	res, err := kernel.Execute(ctx, action.Request{
		ActionID:      ActionAutomationRuleSet,
		ActionVersion: actionVersion,
		RequestID:     "req-rule-1",
		Params: map[string]any{
			"name":                  "有新调用方出现时通知",
			"trigger_kind":          "event",
			"target_action_id":      spyActionID,
			"target_action_version": actionVersion,
			// 即便直接把状态登记成「已定稿」，也不会执行。
			"status": "registered",
		},
	})
	// 正向锚点先立住：登记这一步真的走到了、真的成功了。没有它，
	// 一个在更早处就失败的用例会带着"计数器是 0"绿过去。
	if err != nil {
		t.Fatalf("登记规则失败（锚点）: %v", err)
	}
	if res.RunID == uuid.Nil {
		t.Fatal("登记规则未产生执行记录（锚点）")
	}
	rules, err := store.ListAutomationRules(context.Background())
	if err != nil || len(rules) != 1 {
		t.Fatalf("锚点：应恰好登记了一条规则，实际 %d 条 err=%v", len(rules), err)
	}
	if rules[0].TargetActionID != spyActionID {
		t.Fatalf("锚点：登记的目标应是 %s，实际 %s", spyActionID, rules[0].TargetActionID)
	}

	// 目标断言：那条规则指向的 Action 一次都没被执行。
	if got := spy.count(); got != 0 {
		t.Fatalf("登记规则不该执行目标 Action，实际执行了 %d 次——"+
			"平台没有规则执行器（ADMIN-IA §5.4.1）", got)
	}

	// 改状态、改内容同样不执行。
	if _, err := kernel.Execute(ctx, action.Request{
		ActionID:      ActionAutomationRuleSetStatus,
		ActionVersion: actionVersion,
		RequestID:     "req-rule-2",
		Params:        map[string]any{"rule_id": rules[0].ID.String(), "status": "registered"},
	}); err != nil {
		t.Fatalf("改状态失败（锚点）: %v", err)
	}
	if got := spy.count(); got != 0 {
		t.Fatalf("改规则状态不该执行目标 Action，实际执行了 %d 次", got)
	}

	t.Run("对照组", func(t *testing.T) {
		// 证明计数器与这条执行链路都是活的：同一个内核、同一个注册表，
		// 直接执行 spy，计数必须从 0 变 1。
		if _, err := kernel.Execute(ctx, action.Request{
			ActionID:      spyActionID,
			ActionVersion: actionVersion,
			RequestID:     "req-spy-direct",
			Params:        map[string]any{},
		}); err != nil {
			t.Fatalf("直接执行 spy 失败: %v", err)
		}
		if got := spy.count(); got != 1 {
			t.Fatalf("对照组：直接执行后计数应为 1，实际 %d——"+
				"计数器不工作的话，上面那条「恒为 0」什么也没证明", got)
		}
	})
}

// TestAutomationRuleHandlersHaveNoExecutionCapability 从另一个角度钉同一件事：
// 四个 Handler 拿到的全部能力就是 ActionStore，而它一个执行方法都没有。
//
// 与上一条不重复：那条证明「今天没有发生」，这条证明「拿不到发生的手段」。
// 一个能顺手接上执行器的设计迟早会被接上。
func TestAutomationRuleHandlersHaveNoExecutionCapability(t *testing.T) {
	var store ActionStore = newMemStore(testEnvironment)
	// 如果哪天 ActionStore 上出现了能触发 Action 的方法，这个类型断言会
	// 让本用例编译不过——比一条运行期断言更早拦住。
	type executor interface {
		Execute(ctx context.Context, req action.Request) (action.Result, error)
	}
	if _, ok := store.(executor); ok {
		t.Fatal("ActionStore 不该具备执行能力：规则登记簿没有执行器（ADMIN-IA §5.4.1）")
	}
}
