package extapp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// nilPoolStore 是一个**非 nil 的 Store，但底下没有连接池**。
//
// 用它而不是 nil：三个 Handler 的第一句都是 requireStore，传 nil 会在参数
// 校验之前就返回，于是所有「填错参数应当被拒」的用例都会因为一个无关的
// 错误而"通过"——那是一整组恒真断言。传这个之后，够不到库的那些校验路径
// 会照常跑；真的碰到库的路径本文件不测（见 actions_integration_test.go）。
func nilPoolStore() *Store { return NewStore(nil) }

func humanCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		// Issuer 非空是 Principal.Validate 的硬要求；漏了它内核会在
		// **风险闸之前**就以 PERMISSION_DENIED 拒掉这次调用，于是下面
		// 那组「一张审批单都没落」会变成恒真——这一整组用例什么都测不到。
		// （写这组用例时就是这么撞上的，靠对照组抓出来的。）
		Issuer:      "test",
		Environment: env,
		Scopes:      []string{ScopeManage},
	})
}

// execute 直接调 Handler 而不经 Kernel——本组用例验的是参数校验与业务规则，
// 内核的身份/权限/风险判定另有它自己的用例（见下面的风险闸那组）。
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
	_, err := handler(ctx, params)
	return err
}

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

func requireCode(t *testing.T, err error, want action.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望被拒（%s），实际返回 nil", want)
	}
	var ae *action.Error
	if !asActionError(err, &ae) {
		t.Fatalf("错误不是 *action.Error: %v", err)
	}
	if ae.Code != want {
		t.Fatalf("错误码 = %s（%v），want %s", ae.Code, err, want)
	}
}

// --- 声明本身 -------------------------------------------------------------

func TestActionDefinitionsAreValid(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	defs := reg.List()
	if len(defs) != 3 {
		t.Fatalf("应注册 3 个 Action，实际 %d 个", len(defs))
	}
	byID := map[string]action.Definition{}
	for _, d := range defs {
		byID[d.ID] = d
	}
	for _, id := range []string{ActionAppSet, ActionAppRetire, ActionReleaseRecord} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("缺少 %s", id)
		}
	}

	for _, d := range defs {
		// 机器身份一个都不给（见 humanOnly 的注释：今天没有任何无人值守
		// 链路会调它们，开放 SERVICE 只是先把写入面敞开）。
		if len(d.PrincipalTypes) != 1 || d.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 的 PrincipalTypes = %v，应当只有 HUMAN", d.ID, d.PrincipalTypes)
		}
		// 环境取自调用者身份，不由参数自称（宪法 15 条）。
		for _, f := range d.Schema.Fields {
			if f.Name == "environment" {
				t.Fatalf("%s 不该有 environment 参数：环境取自调用者身份，"+
					"参数化等于让调用方自己说自己在哪个环境", d.ID)
			}
		}
		if len(d.Environments) != 3 {
			t.Fatalf("%s 的 Environments = %v，应当显式列举三个环境", d.ID, d.Environments)
		}
	}
}

// TestRegistryWritesRunDirectlyInsteadOfLandingAsApprovals 钉的是**行为**：
// 这三个动作经过一个接了审批中心的内核时，是**直接执行**的，不会被受理成
// 一张审批单。
//
// 刻意不写 `d.RiskLevel == action.L1`——那种断言只是把常量抄了一遍，
// 既证明不了任何事，等级表演进时还会挡路（同 cards/risk_levels_test.go 的
// 那句话）。这里问的是同一次调用到底被执行了，还是被挂起来等人批。
//
// 下面的 controlAction 是**对照组**：一个声明成 L2 的假动作，同一个内核、
// 同一个网关，它必须被受理成审批单。没有它的话，「submitted 是空的」用
// 一个坏掉的网关也能全绿。
func TestRegistryWritesRunDirectlyInsteadOfLandingAsApprovals(t *testing.T) {
	const controlAction = "extapp.test.control_l2"

	reg := action.NewRegistry()
	if err := RegisterActions(reg, nilPoolStore()); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	if err := reg.Register(action.Definition{
		ID:         controlAction,
		Version:    actionVersion,
		RiskLevel:  action.L2,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "app_key", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}, func(context.Context, map[string]any) (any, error) {
		return nil, fmt.Errorf("对照组的 Handler 不该被调用：L2 应当先落单")
	}); err != nil {
		t.Fatalf("注册对照组: %v", err)
	}

	gw := &fakeApprovals{claims: map[string]action.ApprovalClaim{}}
	k := action.NewKernel(reg, nopRunStore{}, action.WithApprovalGateway(gw))
	ctx := humanCtx("production")

	// 三个用例都**故意把 app_id 填成不是 UUID 的东西**。
	//
	// 这不是在测 UUID 校验，而是为了拿到一个只有 Handler 才给得出的回执：
	// 「app_id 不是合法 UUID」这句话在内核里没有第二个来源（Schema 只管
	// 类型，不管形态），所以看到它就等于看到「这次调用被放行到了执行路径」。
	// 用一份能走通的参数会让 Handler 一路撞到库上，而这一组用例不该需要库。
	cases := []struct {
		name   string
		id     string
		params map[string]any
	}{
		{"登记站点", ActionAppSet, map[string]any{
			"app_id": "不是 uuid", "app_key": "admin-web",
			"display_name": "运营后台", "owner": "平台组",
		}},
		{"下线站点", ActionAppRetire, map[string]any{
			"app_id": "不是 uuid", "reason": "站点已合并进控制台",
		}},
		{"记录一次发布", ActionReleaseRecord, map[string]any{
			"app_id": "不是 uuid", "version": "2026.09.08-1", "released_by": "平台组",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(gw.submitted)
			// **不给 Reason**：L2 及以上必填 Reason，L0/L1 可空。不给它，
			// 这条用例连「万一被判成 L2 却恰好带了理由」的可能都排除掉——
			// 判成 L2 时内核会先因为缺 reason 返回 INVALID_PARAMS，
			// 那条错误的文案与下面要求的那句不同，用例照样红。
			_, err := k.Execute(ctx, action.Request{
				ActionID: tc.id, ActionVersion: actionVersion,
				RequestID: "req-" + tc.id, Params: tc.params,
			})
			// 正向锚点：拿到 Handler 才给得出的那句话。
			var ae *action.Error
			if !asActionError(err, &ae) {
				t.Fatalf("%s 期望拿到 Handler 的 INVALID_PARAMS，实际 %v", tc.id, err)
			}
			if ae.Code == action.CodeAdvancedControlsRequired {
				t.Fatalf("%s 被风险闸挡下（ADVANCED_CONTROLS_REQUIRED）："+
					"这三个动作应当直接执行", tc.id)
			}
			if ae.ApprovalRequestID != "" {
				t.Fatalf("%s 被受理成审批单 %s：这三个动作应当直接执行",
					tc.id, ae.ApprovalRequestID)
			}
			if !strings.Contains(ae.Message, "app_id 不是合法 UUID") {
				t.Fatalf("%s 没走到 Handler：错误是 %s / %q，"+
					"而 Handler 应当回「app_id 不是合法 UUID」", tc.id, ae.Code, ae.Message)
			}
			// 正向锚点已经过了，这里再同步断言「没有落单」。
			if got := len(gw.submitted) - before; got != 0 {
				t.Fatalf("%s 落了 %d 张审批单，应当一张都不落", tc.id, got)
			}
		})
	}

	// 对照组：同一个内核、同一个网关，一个 L2 动作必须落单。
	// 这一条为上面那组「一张都不落」提供了它自己的变异验证——网关是活的。
	before := len(gw.submitted)
	_, err := k.Execute(ctx, action.Request{
		ActionID: controlAction, ActionVersion: actionVersion,
		RequestID: "req-control", Reason: "对照组：确认这个网关真的会收单",
		Params: map[string]any{"app_key": "admin-web"},
	})
	if err == nil {
		t.Fatal("对照组应当被受理成审批单而不是成功执行")
	}
	if got := len(gw.submitted) - before; got != 1 {
		t.Fatalf("对照组落了 %d 张审批单，期望 1 张——"+
			"网关没收到单的话，上面那组「一张都不落」什么都没证明", got)
	}
}

// --- 参数校验 -------------------------------------------------------------

func TestAppSetRejectsBadParams(t *testing.T) {
	s := nilPoolStore()
	ctx := humanCtx("production")

	t.Run("未知 status", func(t *testing.T) {
		requireCode(t, execute(t, s, ctx, ActionAppSet, map[string]any{
			"app_key": "admin-web", "display_name": "运营后台",
			"owner": "平台组", "status": "deleted",
		}), action.CodeInvalidParams)
	})

	t.Run("未知 auth_mode", func(t *testing.T) {
		requireCode(t, execute(t, s, ctx, ActionAppSet, map[string]any{
			"app_key": "admin-web", "display_name": "运营后台",
			"owner": "平台组", "auth_mode": "saml",
		}), action.CodeInvalidParams)
	})

	t.Run("app_id 不是 UUID", func(t *testing.T) {
		requireCode(t, execute(t, s, ctx, ActionAppSet, map[string]any{
			"app_id": "第二条", "app_key": "admin-web",
			"display_name": "运营后台", "owner": "平台组",
		}), action.CodeInvalidParams)
	})

	t.Run("缺少身份", func(t *testing.T) {
		requireCode(t, execute(t, s, context.Background(), ActionAppSet, map[string]any{
			"app_key": "admin-web", "display_name": "运营后台", "owner": "平台组",
		}), action.CodePermissionDenied)
	})
}

func TestAppRetireRequiresIDAndReason(t *testing.T) {
	s := nilPoolStore()
	ctx := humanCtx("production")

	requireCode(t, execute(t, s, ctx, ActionAppRetire, map[string]any{
		"app_id": "不是 uuid", "reason": "站点已下线",
	}), action.CodeInvalidParams)

	requireCode(t, execute(t, s, ctx, ActionAppRetire, map[string]any{
		"app_id": uuid.New().String(), "reason": "   ",
	}), action.CodeInvalidParams)
}

func TestReleaseRecordRejectsBadAppID(t *testing.T) {
	requireCode(t, execute(t, nilPoolStore(), humanCtx("production"), ActionReleaseRecord,
		map[string]any{
			"app_id": "admin-web", "version": "2026.09.08-1", "released_by": "平台组",
		}), action.CodeInvalidParams)
}

// released_at 只收带时区的 RFC3339。
//
// 不带时区的时刻会被解析成另一个时刻**而且不报错**——补记一次历史发布时，
// 「上一版是几点上的」正是回滚复盘要问的问题，差八小时的答案比没有答案更糟。
func TestOptionalTimeParamOnlyAcceptsRFC3339WithZone(t *testing.T) {
	for _, raw := range []string{
		"2026-09-08 10:00:00", // 没有 T、没有时区
		"2026-09-08T10:00:00", // 有 T、没有时区
		"2026-09-08",          // 只有日期
		"08/09/2026 10:00",    // 另一种本地写法
		"1757325600",          // Unix 秒
	} {
		_, err := optionalTimeParam(map[string]any{"released_at": raw}, "released_at")
		if err == nil {
			t.Fatalf("released_at=%q 没带时区，应被拒", raw)
		}
		var ae *action.Error
		if !asActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
			t.Fatalf("released_at=%q 的错误码应是 INVALID_PARAMS，实际 %v", raw, err)
		}
		// 只断错误码不够，文案也要逐字：这条错误要教会人下一步填什么。
		if !strings.Contains(ae.Message, "必须带时区") {
			t.Fatalf("错误文案里应当写明「必须带时区」，实际 %q", ae.Message)
		}
	}

	// 对照组：带时区的两种合法写法都必须收，且换算成 UTC。
	// 少了这一组，上面那组用「永远返回错误」也能全绿——而那会让补记历史
	// 发布这条路径彻底不可用。
	for _, raw := range []string{"2026-09-08T10:00:00Z", "2026-09-08T18:00:00+08:00"} {
		got, err := optionalTimeParam(map[string]any{"released_at": raw}, "released_at")
		if err != nil {
			t.Fatalf("released_at=%q 应当合法: %v", raw, err)
		}
		if got.UTC() != mustTime("2026-09-08T10:00:00Z") {
			t.Fatalf("released_at=%q 应当归一成 2026-09-08T10:00:00Z，实际 %s", raw, got.Format("2006-01-02T15:04:05Z07:00"))
		}
	}

	// 留空 = 现在，由 Handler 兜底；这里只确认解析器把它当作"没给"。
	got, err := optionalTimeParam(map[string]any{}, "released_at")
	if err != nil {
		t.Fatalf("留空应当合法: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("留空应当返回零值，实际 %s", got)
	}
}

// --- 审计摘要 -------------------------------------------------------------

// 审计摘要里「未登记」的字段不写键。
//
// 缺席型断言，所以配了对照组：同一个函数在字段有值时必须写出这些键。
// 只写"不该有"的一半，实现里把整个 map 换成空 map 也能全绿。
func TestAppSummaryOmitsUnregisteredFields(t *testing.T) {
	bare := App{
		AppKey: "console", DisplayName: "控制台", Owner: "平台组",
		Status: AppPlanned, Environment: "production",
	}
	m := appSummary(bare)
	for _, key := range []string{"primary_domain", "auth_mode", "notes"} {
		if _, ok := m[key]; ok {
			t.Fatalf("未登记的 %s 不该出现在审计摘要里：「没有」与「值是空串」"+
				"在审计上是两件事", key)
		}
	}
	// 正向锚点：必写的键一个都不能少。
	for _, key := range []string{"app_key", "display_name", "owner", "status", "environment"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("审计摘要缺少 %s", key)
		}
	}

	// 对照组：填了就必须写出来。
	full := bare
	full.PrimaryDomain = "console.example.test"
	full.AuthMode = AuthOIDC
	full.Notes = "并入控制台"
	m = appSummary(full)
	for _, key := range []string{"primary_domain", "auth_mode", "notes"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("已登记的 %s 应当出现在审计摘要里——"+
				"少了这一组，上面那半用一个空 map 也能全绿", key)
		}
	}
}

// 发布记录的审计摘要必须带 app_key。
//
// 审计事件是给人读的：一串 UUID 回答不了「是哪个站点发版了」，而这条记录的
// 全部价值就在那个问题上。
func TestReleaseSummaryCarriesTheAppKey(t *testing.T) {
	app := App{AppKey: "admin-web", Environment: "production"}
	rel := Release{
		AppID: uuid.New(), Version: "2026.09.08-1", Kind: ReleaseRollback,
		ReleasedAt: mustTime(t0), ReleasedBy: "平台组",
	}
	m := releaseSummary(app, rel)
	if m["app_key"] != "admin-web" {
		t.Fatalf("审计摘要里的 app_key = %v，want admin-web", m["app_key"])
	}
	if m["kind"] != string(ReleaseRollback) {
		t.Fatalf("审计摘要里的 kind = %v，want rollback", m["kind"])
	}
	if m["released_at"] != t0 {
		t.Fatalf("审计摘要里的 released_at = %v，want %s", m["released_at"], t0)
	}
	if _, ok := m["commit_sha"]; ok {
		t.Fatal("没取到提交号时不该写 commit_sha 键")
	}
	// 对照组，同上：取到了就必须写出来。
	rel.CommitSHA = "8c446e5"
	if got := releaseSummary(app, rel)["commit_sha"]; got != "8c446e5" {
		t.Fatalf("commit_sha = %v，want 8c446e5", got)
	}
}

// --- 测试替身 -------------------------------------------------------------

type nopRunStore struct{}

func (nopRunStore) InsertRun(context.Context, action.Run) error { return nil }

type fakeApprovals struct {
	submitted []action.ApprovalSubmission
	claims    map[string]action.ApprovalClaim
}

func (g *fakeApprovals) Submit(_ context.Context, in action.ApprovalSubmission) (string, error) {
	id := fmt.Sprintf("appr-%d", len(g.submitted)+1)
	g.submitted = append(g.submitted, in)
	g.claims[id] = action.ApprovalClaim{
		ActionID: in.ActionID, ActionVersion: in.ActionVersion,
		RiskLevel: in.RiskLevel, Params: in.Params,
		Reason: in.Reason, RequesterID: in.Requester.ID,
	}
	return id, nil
}

func (g *fakeApprovals) Peek(_ context.Context, id string) (action.ApprovalClaim, error) {
	c, ok := g.claims[id]
	if !ok {
		return action.ApprovalClaim{}, fmt.Errorf("审批单 %s 不存在", id)
	}
	return c, nil
}

func (g *fakeApprovals) Claim(context.Context, string, uuid.UUID, map[string]any) error {
	return nil
}
