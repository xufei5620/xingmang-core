package extapp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/extapp"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 本文件跑在真库上：这几条路径都必须**先把资源读出来**才判定得了
// （跨环境闸门、下线、记录发布时的父行校验），用 nil 池的 Store 到不了。

func actionCtx(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "test", Environment: env, Scopes: []string{extapp.ScopeManage},
	})
}

func runAction(t *testing.T, s *extapp.Store, ctx context.Context,
	id string, params map[string]any) (any, error) {
	t.Helper()
	reg := action.NewRegistry()
	if err := extapp.RegisterActions(reg, s); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, "1")
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	return handler(ctx, params)
}

func actionErrorMessage(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望被拒，实际返回 nil")
	}
	return err.Error()
}

// extapp.app.set 的两条分支：留空 app_id 新登记，填了 app_id 整行替换。
func TestAppSetCreatesThenReplaces(t *testing.T) {
	s := testStore(t)
	ctx := actionCtx(testEnv)

	out, err := runAction(t, s, ctx, extapp.ActionAppSet, map[string]any{
		"app_key": "admin-web", "display_name": "运营后台", "owner": "平台组",
		"primary_domain": "admin.example.test", "auth_mode": "oidc",
	})
	if err != nil {
		t.Fatalf("新登记: %v", err)
	}
	created, ok := out.(extapp.App)
	if !ok {
		t.Fatalf("返回值应当是 extapp.App，实际 %T", out)
	}
	if created.Status != extapp.AppActive {
		t.Fatalf("没给 status 时应当默认 active，实际 %q", created.Status)
	}
	if created.Environment != testEnv {
		t.Fatalf("environment 应当取自调用者身份，实际 %q", created.Environment)
	}

	out, err = runAction(t, s, ctx, extapp.ActionAppSet, map[string]any{
		"app_id": created.ID.String(), "app_key": "admin-web",
		"display_name": "运营后台", "owner": "运维组", "status": "planned",
	})
	if err != nil {
		t.Fatalf("整行替换: %v", err)
	}
	updated := out.(extapp.App)
	if updated.ID != created.ID {
		t.Fatalf("整行替换不该换 id：%s → %s", created.ID, updated.ID)
	}
	if updated.Owner != "运维组" || updated.Status != extapp.AppPlanned {
		t.Fatalf("整行替换没生效: %+v", updated)
	}
	if updated.PrimaryDomain != "" || updated.AuthMode != "" {
		t.Fatalf("整行替换应当清掉这次没给的字段，实际 domain=%q auth=%q",
			updated.PrimaryDomain, updated.AuthMode)
	}
}

// 主机名一律小写：DNS 不区分大小写，而唯一索引区分。
//
// 不归一的话，登记 Admin.Example.Test 与 admin.example.test 会得到两行，
// 「这个域名归谁负责」于是有两个答案——而那正是那条唯一索引要防的事。
func TestAppSetNormalisesTheDomainToLowercase(t *testing.T) {
	s := testStore(t)
	ctx := actionCtx(testEnv)

	out, err := runAction(t, s, ctx, extapp.ActionAppSet, map[string]any{
		"app_key": "admin-web", "display_name": "运营后台", "owner": "平台组",
		"primary_domain": "Admin.Example.Test",
	})
	if err != nil {
		t.Fatalf("大小写混排的主机名应当被归一后接受: %v", err)
	}
	if got := out.(extapp.App).PrimaryDomain; got != "admin.example.test" {
		t.Fatalf("primary_domain = %q，应当归一成全小写", got)
	}

	// 归一之后，另一个应用再登记同一个域名（写法不同）必须撞唯一索引。
	// 这一步才是归一的意义所在——只断言字符串变小写，改成
	// 「在展示层小写」也能全绿，而那挡不住重复登记。
	_, err = runAction(t, s, ctx, extapp.ActionAppSet, map[string]any{
		"app_key": "console", "display_name": "控制台", "owner": "平台组",
		"primary_domain": "ADMIN.EXAMPLE.TEST",
	})
	if err == nil {
		t.Fatal("换个大小写写法登记同一个域名，应当仍被唯一索引挡下")
	}
}

// 跨环境闸门：内核只校验「这个 Action 允许在你的环境执行」，它不认识资源。
// 一个 staging 身份拿着生产应用的 UUID 打过来，必须在读到资源之后被拒。
func TestCrossEnvironmentWritesAreRejected(t *testing.T) {
	s := testStore(t)
	prodApp := mustCreateApp(t, s, extapp.App{AppKey: "admin-web", Environment: "production"})
	staging := actionCtx("staging")

	t.Run("改登记", func(t *testing.T) {
		msg := actionErrorMessage(t, mustFail(t, s, staging, extapp.ActionAppSet, map[string]any{
			"app_id": prodApp.ID.String(), "app_key": "admin-web",
			"display_name": "运营后台", "owner": "平台组",
		}))
		requireCrossEnvMessage(t, msg)
	})

	t.Run("下线", func(t *testing.T) {
		msg := actionErrorMessage(t, mustFail(t, s, staging, extapp.ActionAppRetire, map[string]any{
			"app_id": prodApp.ID.String(), "reason": "越界试探",
		}))
		requireCrossEnvMessage(t, msg)
	})

	t.Run("记录发布", func(t *testing.T) {
		msg := actionErrorMessage(t, mustFail(t, s, staging, extapp.ActionReleaseRecord, map[string]any{
			"app_id": prodApp.ID.String(), "version": "x", "released_by": "y",
		}))
		requireCrossEnvMessage(t, msg)
	})

	// 对照组：同环境的同一批调用必须成功。少了它，「跨环境被拒」用一个
	// 「所有调用都失败」的实现也能全绿。
	prod := actionCtx("production")
	if _, err := runAction(t, s, prod, extapp.ActionAppSet, map[string]any{
		"app_id": prodApp.ID.String(), "app_key": "admin-web",
		"display_name": "运营后台", "owner": "平台组",
	}); err != nil {
		t.Fatalf("同环境改登记应当成功: %v", err)
	}
	if _, err := runAction(t, s, prod, extapp.ActionReleaseRecord, map[string]any{
		"app_id": prodApp.ID.String(), "version": "2026.09.08-1", "released_by": "平台组",
	}); err != nil {
		t.Fatalf("同环境记录发布应当成功: %v", err)
	}
	if _, err := runAction(t, s, prod, extapp.ActionAppRetire, map[string]any{
		"app_id": prodApp.ID.String(), "reason": "站点合并进控制台",
	}); err != nil {
		t.Fatalf("同环境下线应当成功: %v", err)
	}
}

func mustFail(t *testing.T, s *extapp.Store, ctx context.Context,
	id string, params map[string]any) error {
	t.Helper()
	_, err := runAction(t, s, ctx, id, params)
	return err
}

// 只断错误码不够，文案也要逐字：这条错误要说清「你是哪个环境的身份」，
// 否则拿到 403 的人会去查权限配置，而真因是他打错了环境。
func requireCrossEnvMessage(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "不允许跨环境操作前端应用登记簿") {
		t.Fatalf("错误文案应当写明跨环境，实际 %q", msg)
	}
	if !strings.Contains(msg, "调用者身份属于 staging") {
		t.Fatalf("错误文案应当说清调用者属于哪个环境，实际 %q", msg)
	}
}

func TestAppRetireOnlyChangesStatus(t *testing.T) {
	s := testStore(t)
	ctx := actionCtx(testEnv)
	app := mustCreateApp(t, s, extapp.App{
		AppKey: "admin-web", PrimaryDomain: "admin.example.test",
		AuthMode: extapp.AuthOIDC, Notes: "保留这句",
	})

	out, err := runAction(t, s, ctx, extapp.ActionAppRetire, map[string]any{
		"app_id": app.ID.String(), "reason": "站点已合并进控制台",
	})
	if err != nil {
		t.Fatalf("下线: %v", err)
	}
	got := out.(extapp.App)
	if got.Status != extapp.AppRetired {
		t.Fatalf("status = %q, want retired", got.Status)
	}
	// 一键下线不该顺手清掉别的字段——那会让「下线」变成一次隐形的整行替换。
	if got.PrimaryDomain != "admin.example.test" || got.AuthMode != extapp.AuthOIDC ||
		got.Notes != "保留这句" {
		t.Fatalf("下线不该动其它字段: %+v", got)
	}
}

func TestAppRetireRejectsUnknownApp(t *testing.T) {
	s := testStore(t)
	err := mustFail(t, s, actionCtx(testEnv), extapp.ActionAppRetire, map[string]any{
		"app_id": uuid.New().String(), "reason": "试探",
	})
	if err == nil {
		t.Fatal("不存在的应用应当被拒")
	}
}

// 记录发布时父行不存在，要给出「这个应用不在」而不是一条外键违例。
func TestReleaseRecordRejectsUnknownAppWithAReadableError(t *testing.T) {
	s := testStore(t)
	err := mustFail(t, s, actionCtx(testEnv), extapp.ActionReleaseRecord, map[string]any{
		"app_id": uuid.New().String(), "version": "x", "released_by": "y",
	})
	msg := actionErrorMessage(t, err)
	if !strings.Contains(msg, "not found") {
		t.Fatalf("应当是「找不到这个应用」，实际 %q", msg)
	}
	// 外键违例长这样；漏了上面那次 GetApp 的话就会看到它。
	if strings.Contains(msg, "violates foreign key") {
		t.Fatalf("不该把库层外键违例直接抛给调用方: %q", msg)
	}
}

// released_at 留空 = 现在。补记历史发布时才显式给一个时刻。
func TestReleaseRecordDefaultsReleasedAtToNow(t *testing.T) {
	s := testStore(t)
	ctx := actionCtx(testEnv)
	app := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})

	out, err := runAction(t, s, ctx, extapp.ActionReleaseRecord, map[string]any{
		"app_id": app.ID.String(), "version": "2026.09.08-1", "released_by": "平台组",
	})
	if err != nil {
		t.Fatalf("记录发布: %v", err)
	}
	rel := out.(extapp.Release)
	if rel.ReleasedAt.IsZero() {
		t.Fatal("留空 released_at 应当兜底成现在，而不是零值")
	}
	if rel.Kind != extapp.ReleaseDeploy {
		t.Fatalf("留空 kind 应当默认 deploy，实际 %q", rel.Kind)
	}

	// 对照组：显式给一个过去的时刻时，必须原样存下来而不是被"现在"覆盖——
	// 补记历史发布这条路径全靠它。
	out, err = runAction(t, s, ctx, extapp.ActionReleaseRecord, map[string]any{
		"app_id": app.ID.String(), "version": "很久以前", "released_by": "平台组",
		"released_at": "2026-01-02T03:04:05Z", "kind": "rollback",
	})
	if err != nil {
		t.Fatalf("补记历史发布: %v", err)
	}
	back := out.(extapp.Release)
	if !back.ReleasedAt.Equal(at("2026-01-02T03:04:05Z")) {
		t.Fatalf("显式给的 released_at 被改成了 %s", back.ReleasedAt)
	}
	if back.Kind != extapp.ReleaseRollback {
		t.Fatalf("kind = %q, want rollback", back.Kind)
	}
}

// 提交号一律小写：短 SHA 常被从代码托管页面复制成大写，而校验只认小写。
func TestReleaseRecordNormalisesTheCommitSHA(t *testing.T) {
	s := testStore(t)
	ctx := actionCtx(testEnv)
	app := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})

	out, err := runAction(t, s, ctx, extapp.ActionReleaseRecord, map[string]any{
		"app_id": app.ID.String(), "version": "2026.09.08-1",
		"released_by": "平台组", "commit_sha": "8C446E5",
	})
	if err != nil {
		t.Fatalf("大写提交号应当被归一后接受: %v", err)
	}
	if got := out.(extapp.Release).CommitSHA; got != "8c446e5" {
		t.Fatalf("commit_sha = %q，应当归一成全小写", got)
	}

	// 对照组：真的不合法的提交号仍要被拒（归一不等于放行任何输入）。
	if _, err := runAction(t, s, ctx, extapp.ActionReleaseRecord, map[string]any{
		"app_id": app.ID.String(), "version": "2026.09.08-2",
		"released_by": "平台组", "commit_sha": "不是提交号",
	}); err == nil {
		t.Fatal("非十六进制的提交号应当被拒")
	}
}
