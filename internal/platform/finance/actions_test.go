package finance

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// nilPoolStore 造一个不会连库的 Store。
//
// 参数校验发生在任何查询之前，所以「拒绝非法输入」这类用例不需要真库。
// 需要真库的（跨环境闸门、审计前后镜像、库层 CHECK）在
// store_integration_test.go 与 actions_integration_test.go 里。
func nilPoolStore() *Store { return NewStore(nil) }

func humanCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Environment: env,
		Scopes: []string{
			ScopeAccountManage, ScopeRatioManage, ScopeTokenMapManage, ScopeRead,
		},
	})
}

// actionCredentialRef 是用例共用的凭据引用。常量而非字面量的理由见
// account_test.go 里同名说明（gitleaks generic-api-key 误报）。
const actionCredentialRef = "secret://finance-action/upstream-a"

// TestActionDefinitionsAreValid：四个 Action 的声明本身必须过内核的校验。
func TestActionDefinitionsAreValid(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
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
	for _, id := range []string{
		ActionAccountSet, ActionRechargeRatioSet, ActionTokenMapSet, ActionTokenMapRemove,
	} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("缺少 %s", id)
		}
	}

	for _, d := range defs {
		// L1 是设计稿 §8.3 的硬性要求：Foundation-A 内核对 L2+ 是 fail-closed，
		// 声明成 L2 等于让这个动作在当前阶段根本执行不了。
		if d.RiskLevel != action.L1 {
			t.Fatalf("%s 风险等级 = %s, want L1（§8.3）", d.ID, d.RiskLevel)
		}
		if d.RiskLevel.RequiresAdvancedControls() {
			t.Fatalf("%s 在 Foundation-A 无法执行（风险等级 %s）", d.ID, d.RiskLevel)
		}
		// AI 不拥有生产后门（ADR-009）：登记簿决定「用哪套凭据、按什么倍率
		// 算成本」，让机器身份能改这两样等于给了一条自我修饰报表的路径。
		if len(d.PrincipalTypes) != 1 || d.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 应只允许人类身份，实际 %v", d.ID, d.PrincipalTypes)
		}
		// 环境不能由参数自称（宪法 15 条）
		for _, f := range d.Schema.Fields {
			if f.Name == "environment" {
				t.Fatalf("%s 不该接受 environment 参数——环境取自调用者身份", d.ID)
			}
		}
	}

	// 倍率有独立权限（§6.3）：它是唯一会改变成本口径的字段
	if byID[ActionRechargeRatioSet].Permission == byID[ActionAccountSet].Permission {
		t.Fatal("改倍率与改登记簿不该共用一个权限（§6.3）")
	}
	if byID[ActionTokenMapSet].Permission == byID[ActionAccountSet].Permission {
		t.Fatal("维护映射与改登记簿不该共用一个权限")
	}
}

// TestRatioParamsAreStringsNotNumbers 锁住宪法 13 条在参数层的落点。
//
// Schema 只有 int / string / bool / string_slice 四种类型，没有 Decimal；
// 而 JSON 数字一路解成 float64，1.15 进来就已经不是 1.15 了。
// 所以倍率必须以**字符串**传，由 money.ParseRatio 在整数域里解析。
func TestRatioParamsAreStringsNotNumbers(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	for _, d := range reg.List() {
		for _, f := range d.Schema.Fields {
			if f.Name != "recharge_ratio" {
				continue
			}
			if f.Type != action.FieldString {
				t.Fatalf("%s 的 recharge_ratio 类型 = %s, want string"+
					"（JSON 数字会经 float64，宪法 13 条）", d.ID, f.Type)
			}
		}
	}
}

func execute(t *testing.T, store *Store, ctx context.Context, id string, params map[string]any) error {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	// 直接调 Handler 而不经 Kernel：本组用例验的是**参数校验**，
	// 而内核的身份/权限/风险判定有它自己的测试。经内核会把每条用例
	// 都变成一次完整装配（RunStore、AuditSink），噪声远大于收益。
	_, err := handler(ctx, params)
	return err
}

func TestAccountSetRejectsBadRatio(t *testing.T) {
	ctx := humanCtx("production")
	base := func() map[string]any {
		return map[string]any{
			"system_type":    string(SystemSub2API),
			"access_method":  string(AccessUpstreamKey),
			"credential_ref": actionCredentialRef,
		}
	}

	for name, raw := range map[string]string{
		"零倍率":   "0",
		"负倍率":   "-1.5",
		"非数字":   "abc",
		"科学计数法": "1.5e0",
		"小数位过多": "0.0000000001",
	} {
		params := base()
		params["recharge_ratio"] = raw
		err := execute(t, nilPoolStore(), ctx, ActionAccountSet, params)
		if err == nil {
			t.Fatalf("%s（%q）必须被拒", name, raw)
		}
		var ae *action.Error
		if !asActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
			t.Fatalf("%s 的错误码 = %v, want INVALID_PARAMS", name, err)
		}
	}
}

// TestAccountSetRejectsPlaintextCredential：凭据只能是 CredentialRef（ADR-014）。
func TestAccountSetRejectsPlaintextCredential(t *testing.T) {
	ctx := humanCtx("production")
	err := execute(t, nilPoolStore(), ctx, ActionAccountSet, map[string]any{
		"system_type":    string(SystemSub2API),
		"access_method":  string(AccessUpstreamKey),
		"credential_ref": "admin-0123456789abcdef",
		"recharge_ratio": "1.5",
	})
	if err == nil {
		t.Fatal("明文凭据必须被拒")
	}
	if strings.Contains(err.Error(), "0123456789abcdef") {
		t.Fatalf("错误里回显了疑似凭据的内容: %v", err)
	}
}

// TestAccountSetRejectsMeteredWithoutRatio 是 §2.0 在 Action 层的落点。
func TestAccountSetRejectsMeteredWithoutRatio(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionAccountSet, map[string]any{
		"system_type":    string(SystemSub2API),
		"access_method":  string(AccessUpstreamKey),
		"credential_ref": actionCredentialRef,
	})
	if err == nil {
		t.Fatal("计量型缺倍率必须被拒")
	}
	var ae *action.Error
	if !asActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("错误码 = %v, want INVALID_PARAMS", err)
	}
}

func TestRechargeRatioSetRequiresReason(t *testing.T) {
	err := execute(t, nilPoolStore(), humanCtx("production"), ActionRechargeRatioSet,
		map[string]any{
			"upstream_account_id": "5f9c1f38-0000-4000-8000-000000000000",
			"recharge_ratio":      "1.5",
			"reason":              "   ",
		})
	if err == nil {
		// 改倍率直接改变毛利报表，事后复盘的第一个问题就是「当时为什么改」
		t.Fatal("空白 reason 必须被拒")
	}
}

func TestActionsRejectMalformedUUID(t *testing.T) {
	ctx := humanCtx("production")
	for _, tc := range []struct {
		id     string
		params map[string]any
	}{
		{ActionRechargeRatioSet, map[string]any{
			"upstream_account_id": "not-a-uuid", "recharge_ratio": "1.5", "reason": "调价",
		}},
		{ActionTokenMapSet, map[string]any{
			"upstream_account_id": "not-a-uuid",
			"upstream_token_id":   "tok-1", "own_account_id": "258",
		}},
		{ActionTokenMapRemove, map[string]any{
			"upstream_account_id": "not-a-uuid",
			"upstream_token_id":   "tok-1", "reason": "映射写错了",
		}},
		{ActionAccountSet, map[string]any{
			"upstream_account_id": "not-a-uuid",
			"system_type":         string(SystemSub2API),
			"access_method":       string(AccessUpstreamKey),
			"credential_ref":      actionCredentialRef,
			"recharge_ratio":      "1.5",
		}},
	} {
		err := execute(t, nilPoolStore(), ctx, tc.id, tc.params)
		if err == nil {
			t.Fatalf("%s 的非法 UUID 必须被拒", tc.id)
		}
	}
}

// TestActionsRequirePrincipal：环境取自调用者身份，没有身份就没有环境。
func TestActionsRequirePrincipal(t *testing.T) {
	for _, id := range []string{
		ActionAccountSet, ActionRechargeRatioSet, ActionTokenMapSet, ActionTokenMapRemove,
	} {
		err := execute(t, nilPoolStore(), context.Background(), id, map[string]any{
			"system_type":         string(SystemSub2API),
			"access_method":       string(AccessUpstreamKey),
			"credential_ref":      actionCredentialRef,
			"recharge_ratio":      "1.5",
			"upstream_account_id": "5f9c1f38-0000-4000-8000-000000000000",
			"upstream_token_id":   "tok-1",
			"own_account_id":      "258",
			"reason":              "测试",
		})
		if err == nil {
			t.Fatalf("%s 缺少 Principal 必须被拒", id)
		}
	}
}

// TestUnboundStoreFailsLoudly：只登记声明不绑执行体时，
// Handler 被调用要给出明确错误而不是空跑成功。
func TestUnboundStoreFailsLoudly(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(ActionAccountSet, actionVersion)
	if !ok {
		t.Fatal("未注册")
	}
	if _, err := handler(humanCtx("production"), map[string]any{}); err == nil {
		t.Fatal("未绑定 store 时必须报错")
	}
}

// asActionError 是 errors.As 的一层薄封装，避免每个用例都写一遍导入。
func asActionError(err error, target **action.Error) bool {
	for e := err; e != nil; {
		if ae, ok := e.(*action.Error); ok {
			*target = ae
			return true
		}
		unwrapper, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = unwrapper.Unwrap()
	}
	return false
}

// testDatabaseURL 是包内集成测试的库地址来源。
//
// 与 store_integration_test.go（外部测试包）各有一份读取逻辑：两个包看不见
// 对方的辅助函数，而共用一个 export_test.go 只为省三行不划算。
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	return url
}
