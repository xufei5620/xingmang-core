package extapp_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/extapp"
)

// 本文件跑在真库上：库层的部分不变量只在 SQL 里（两处唯一索引、主机名与
// 提交号的 CHECK、外键 RESTRICT，以及「当前版本」那条 LATERAL 子查询的
// 排序），用内存假货复刻只会测到假货。

const testEnv = "production"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// ext_app_release 对 ext_app 是 ON DELETE RESTRICT——TRUNCATE 必须一次
	// 列全引用方，漏掉子表这条语句会直接报错。
	if _, err := pool.Exec(ctx,
		"TRUNCATE core.ext_app_release, core.ext_app"); err != nil {
		t.Fatalf("清空前端应用登记簿失败: %v", err)
	}
	return pool
}

func testStore(t *testing.T) *extapp.Store {
	t.Helper()
	return extapp.NewStore(testPool(t))
}

func mustCreateApp(t *testing.T, s *extapp.Store, in extapp.App) extapp.App {
	t.Helper()
	if in.Environment == "" {
		in.Environment = testEnv
	}
	if in.Status == "" {
		in.Status = extapp.AppActive
	}
	if in.DisplayName == "" {
		in.DisplayName = in.AppKey
	}
	if in.Owner == "" {
		in.Owner = "平台组"
	}
	out, err := s.CreateApp(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateApp(%s): %v", in.AppKey, err)
	}
	return out
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestAppCreateGetUpdateRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	created := mustCreateApp(t, s, extapp.App{
		AppKey:        "admin-web",
		DisplayName:   "运营后台",
		PrimaryDomain: "admin.example.test",
		AuthMode:      extapp.AuthOIDC,
		Owner:         "平台组",
		Status:        extapp.AppActive,
		Notes:         "同源部署，前端与 /api 同一个 nginx",
	})
	if created.ID == uuid.Nil {
		t.Fatal("CreateApp 应当分配一个 id")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("时间戳应当由库层填上")
	}

	got, err := s.GetApp(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.AppKey != "admin-web" || got.DisplayName != "运营后台" ||
		got.PrimaryDomain != "admin.example.test" || got.AuthMode != extapp.AuthOIDC ||
		got.Owner != "平台组" || got.Status != extapp.AppActive ||
		got.Notes != "同源部署，前端与 /api 同一个 nginx" || got.Environment != testEnv {
		t.Fatalf("往返后字段不一致: %+v", got)
	}

	// 整行替换：这次把域名与登录方式都改掉，并清空备注。
	updated, err := s.UpdateApp(ctx, extapp.App{
		ID:            created.ID,
		AppKey:        "admin-web",
		DisplayName:   "运营后台（新）",
		PrimaryDomain: "admin2.example.test",
		AuthMode:      extapp.AuthLocal,
		Owner:         "平台组",
		Status:        extapp.AppActive,
		Environment:   testEnv,
	})
	if err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	if updated.DisplayName != "运营后台（新）" || updated.PrimaryDomain != "admin2.example.test" ||
		updated.AuthMode != extapp.AuthLocal {
		t.Fatalf("整行替换没生效: %+v", updated)
	}
	// 整行替换必须**真的清空**没给的字段——半更新的行会让人以为备注还在。
	if updated.Notes != "" {
		t.Fatalf("整行替换应当清掉未给出的 notes，实际 %q", updated.Notes)
	}
}

// 未登记的可选字段读回来是空串，不是别的什么值。
func TestAppOptionalFieldsRoundTripAsEmpty(t *testing.T) {
	s := testStore(t)
	created := mustCreateApp(t, s, extapp.App{AppKey: "console", Status: extapp.AppPlanned})
	got, err := s.GetApp(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.PrimaryDomain != "" || got.AuthMode != "" || got.Notes != "" {
		t.Fatalf("未登记字段应当读回空串: %+v", got)
	}
	if got.Status != extapp.AppPlanned {
		t.Fatalf("status = %q, want planned", got.Status)
	}
}

func TestAppKeyIsUniquePerEnvironment(t *testing.T) {
	s := testStore(t)
	mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})
	_, err := s.CreateApp(context.Background(), extapp.App{
		AppKey: "admin-web", DisplayName: "另一条", Owner: "别人",
		Status: extapp.AppActive, Environment: testEnv,
	})
	if err == nil {
		t.Fatal("同环境下重复的 app_key 应当被唯一索引挡下")
	}
	// 对照组：换一个 key 就能建——上面那条不是靠「CreateApp 永远失败」绿的。
	mustCreateApp(t, s, extapp.App{AppKey: "console"})
}

func TestDomainIsUniquePerEnvironmentButEmptyIsNot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustCreateApp(t, s, extapp.App{AppKey: "admin-web", PrimaryDomain: "admin.example.test"})

	_, err := s.CreateApp(ctx, extapp.App{
		AppKey: "console", DisplayName: "控制台", Owner: "平台组",
		PrimaryDomain: "admin.example.test",
		Status:        extapp.AppActive, Environment: testEnv,
	})
	if err == nil {
		t.Fatal("同环境下两个应用登记同一个域名应当被挡下：" +
			"「这个域名归谁负责」不该有两个答案")
	}

	// 但「没有域名」不是一个值：多个规划中的站点都可以留空（部分唯一索引
	// 的 WHERE primary_domain IS NOT NULL 就是为了这个）。
	mustCreateApp(t, s, extapp.App{AppKey: "console", Status: extapp.AppPlanned})
	mustCreateApp(t, s, extapp.App{AppKey: "portal", Status: extapp.AppPlanned})
}

// 库层 CHECK 是第二道闸。这里绕过领域校验直接写库，确认库自己也拦得住——
// 领域校验哪天被改松了，数据仍然进不去坏形状。
func TestDatabaseChecksRejectBadShapesEvenWhenDomainValidationIsBypassed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{
			"域名是整条 URL",
			`INSERT INTO core.ext_app (id, app_key, display_name, primary_domain, owner, status, environment)
			 VALUES ($1, 'admin-web', 'x', 'https://admin.example.test', 'y', 'active', $2)`,
			[]any{uuid.New(), testEnv},
		},
		{
			"域名带用户信息段",
			`INSERT INTO core.ext_app (id, app_key, display_name, primary_domain, owner, status, environment)
			 VALUES ($1, 'admin-web', 'x', 'u:p@admin.example.test', 'y', 'active', $2)`,
			[]any{uuid.New(), testEnv},
		},
		{
			"app_key 带大写",
			`INSERT INTO core.ext_app (id, app_key, display_name, owner, status, environment)
			 VALUES ($1, 'Admin-Web', 'x', 'y', 'active', $2)`,
			[]any{uuid.New(), testEnv},
		},
		{
			"未知 status",
			`INSERT INTO core.ext_app (id, app_key, display_name, owner, status, environment)
			 VALUES ($1, 'admin-web', 'x', 'y', 'deleted', $2)`,
			[]any{uuid.New(), testEnv},
		},
		{
			"未知 auth_mode",
			`INSERT INTO core.ext_app (id, app_key, display_name, owner, status, auth_mode, environment)
			 VALUES ($1, 'admin-web', 'x', 'y', 'active', 'saml', $2)`,
			[]any{uuid.New(), testEnv},
		},
		{
			"environment 不在 core.environment 里",
			`INSERT INTO core.ext_app (id, app_key, display_name, owner, status, environment)
			 VALUES ($1, 'admin-web', 'x', 'y', 'active', 'preprod')`,
			[]any{uuid.New()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.sql, tc.args...); err == nil {
				t.Fatal("库层应当拒绝这一行")
			}
		})
	}

	// 对照组：一行完全合法的记录必须插得进去。少了它，上面六条用一张
	// 「谁都写不进」的表也能全绿。
	if _, err := pool.Exec(ctx,
		`INSERT INTO core.ext_app (id, app_key, display_name, primary_domain, owner, status, auth_mode, environment)
		 VALUES ($1, 'admin-web', 'x', 'admin.example.test', 'y', 'active', 'oidc', $2)`,
		uuid.New(), testEnv); err != nil {
		t.Fatalf("合法的一行应当插得进去: %v", err)
	}
}

func TestReleaseRoundTripAndForeignKey(t *testing.T) {
	pool := testPool(t)
	s := extapp.NewStore(pool)
	ctx := context.Background()
	app := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})

	rel, err := s.CreateRelease(ctx, extapp.Release{
		AppID:      app.ID,
		Version:    "2026.09.08-1",
		CommitSHA:  "8c446e5",
		Kind:       extapp.ReleaseDeploy,
		ReleasedAt: at("2026-09-08T10:00:00Z"),
		ReleasedBy: "平台组",
		Notes:      "同源部署",
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if !rel.ReleasedAt.Equal(at("2026-09-08T10:00:00Z")) {
		t.Fatalf("released_at 往返后 = %s", rel.ReleasedAt)
	}
	if rel.CreatedAt.IsZero() {
		t.Fatal("created_at 应当由库层填上——它与 released_at 是两个时刻")
	}

	// 未知 app_id 撞外键。
	if _, err := s.CreateRelease(ctx, extapp.Release{
		AppID: uuid.New(), Version: "x", ReleasedAt: at("2026-09-08T10:00:00Z"),
		ReleasedBy: "y", Kind: extapp.ReleaseDeploy,
	}); err == nil {
		t.Fatal("指向不存在应用的发布记录应当被外键挡下")
	}

	// 父行有子行时不能删（ON DELETE RESTRICT）：删掉应用会让发布历史
	// 变成孤儿，而那是这张表唯一的价值。本包没有删除应用的方法，所以这里
	// 用原生 SQL 试删——测的是库层的约束，不是某个 Go 方法。
	if _, err := pool.Exec(ctx, "DELETE FROM core.ext_app WHERE id = $1", app.ID); err == nil {
		t.Fatal("有发布记录的应用不该删得掉（ON DELETE RESTRICT）")
	}

	// 对照组：把子行删掉之后父行就能删了——上面那条不是靠「DELETE 永远
	// 失败」绿的（比如权限不足也会报错，那与外键毫无关系）。
	if _, err := pool.Exec(ctx, "DELETE FROM core.ext_app_release WHERE id = $1", rel.ID); err != nil {
		t.Fatalf("删除发布记录: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM core.ext_app WHERE id = $1", app.ID); err != nil {
		t.Fatalf("没有子行之后应当删得掉: %v", err)
	}
}

func TestSameReleaseCannotBeRecordedTwiceAtTheSameInstant(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	app := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})

	in := extapp.Release{
		AppID: app.ID, Version: "2026.09.08-1", Kind: extapp.ReleaseDeploy,
		ReleasedAt: at("2026-09-08T10:00:00Z"), ReleasedBy: "平台组",
	}
	if _, err := s.CreateRelease(ctx, in); err != nil {
		t.Fatalf("第一次登记: %v", err)
	}
	if _, err := s.CreateRelease(ctx, in); err == nil {
		t.Fatal("同一应用、同一版本、同一时刻重复登记应当被唯一索引挡下（表单双击）")
	}

	// 对照组一：同版本、换时刻——重新发一次同一个版本是合法的。
	in2 := in
	in2.ReleasedAt = at("2026-09-08T11:00:00Z")
	if _, err := s.CreateRelease(ctx, in2); err != nil {
		t.Fatalf("同版本换时刻应当允许（重新发一次）: %v", err)
	}
	// 对照组二：同时刻、换版本。
	in3 := in
	in3.Version = "2026.09.08-2"
	if _, err := s.CreateRelease(ctx, in3); err != nil {
		t.Fatalf("同时刻换版本应当允许: %v", err)
	}
}

// 「当前版本」取的是 released_at 最新的那一条，不是最后登记的那一条。
//
// 这两者会分叉：补记一次很久以前的发布时，它是最后登记的，但绝不是当前版本。
func TestListAppsReportsTheLatestReleaseNotTheLatestRecord(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	app := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})

	newest := extapp.Release{
		AppID: app.ID, Version: "新的", Kind: extapp.ReleaseDeploy,
		ReleasedAt: at("2026-09-08T10:00:00Z"), ReleasedBy: "平台组",
	}
	older := extapp.Release{
		AppID: app.ID, Version: "旧的", Kind: extapp.ReleaseDeploy,
		ReleasedAt: at("2026-09-01T10:00:00Z"), ReleasedBy: "平台组",
	}
	// **先登记新的，后补记旧的**——顺序刻意与时间相反。
	if _, err := s.CreateRelease(ctx, newest); err != nil {
		t.Fatalf("登记新发布: %v", err)
	}
	if _, err := s.CreateRelease(ctx, older); err != nil {
		t.Fatalf("补记旧发布: %v", err)
	}

	items, err := s.ListAppsByEnvironment(ctx, testEnv)
	if err != nil {
		t.Fatalf("ListAppsByEnvironment: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应当有 1 个应用，实际 %d 个", len(items))
	}
	if items[0].CurrentRelease == nil {
		t.Fatal("登记过发布的应用应当带上当前版本")
	}
	if items[0].CurrentRelease.Version != "新的" {
		t.Fatalf("当前版本 = %q，want 新的——"+
			"取的应当是 released_at 最新的那条，不是最后登记的那条",
			items[0].CurrentRelease.Version)
	}
}

// 没登记过发布的应用，CurrentRelease 是 nil。
//
// 缺席型断言，配了对照组：同一次查询里另一个应用必须**有**当前版本。
// 只写"是 nil"的一半，实现里把这个字段永远置 nil 也能全绿——而那会让
// 「发布版本」这一列对所有应用都显示未登记。
func TestAppsWithoutReleasesReportNilAndOthersDoNot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	withRelease := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})
	mustCreateApp(t, s, extapp.App{AppKey: "console", Status: extapp.AppPlanned})

	if _, err := s.CreateRelease(ctx, extapp.Release{
		AppID: withRelease.ID, Version: "2026.09.08-1", Kind: extapp.ReleaseDeploy,
		ReleasedAt: at("2026-09-08T10:00:00Z"), ReleasedBy: "平台组",
	}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	items, err := s.ListAppsByEnvironment(ctx, testEnv)
	if err != nil {
		t.Fatalf("ListAppsByEnvironment: %v", err)
	}
	byKey := map[string]extapp.AppWithRelease{}
	for _, it := range items {
		byKey[it.AppKey] = it
	}
	// 正向锚点先来：有发布的那个必须有版本。
	got, ok := byKey["admin-web"]
	if !ok {
		t.Fatal("列表里少了 admin-web")
	}
	if got.CurrentRelease == nil || got.CurrentRelease.Version != "2026.09.08-1" {
		t.Fatalf("admin-web 的当前版本应当是 2026.09.08-1，实际 %+v", got.CurrentRelease)
	}
	// 锚点过了，再同步断言另一个是 nil。
	none, ok := byKey["console"]
	if !ok {
		t.Fatal("列表里少了 console")
	}
	if none.CurrentRelease != nil {
		t.Fatalf("没登记过发布的应用应当是 nil（= 未登记），实际 %+v", none.CurrentRelease)
	}
}

func TestListsAreScopedToTheEnvironment(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	prod := mustCreateApp(t, s, extapp.App{AppKey: "admin-web", Environment: "production"})
	mustCreateApp(t, s, extapp.App{AppKey: "admin-web", Environment: "staging"})

	if _, err := s.CreateRelease(ctx, extapp.Release{
		AppID: prod.ID, Version: "prod-1", Kind: extapp.ReleaseDeploy,
		ReleasedAt: at("2026-09-08T10:00:00Z"), ReleasedBy: "平台组",
	}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	prodApps, err := s.ListAppsByEnvironment(ctx, "production")
	if err != nil {
		t.Fatalf("ListAppsByEnvironment(production): %v", err)
	}
	if len(prodApps) != 1 || prodApps[0].Environment != "production" {
		t.Fatalf("production 应当只看到自己那一条，实际 %+v", prodApps)
	}
	stagingApps, err := s.ListAppsByEnvironment(ctx, "staging")
	if err != nil {
		t.Fatalf("ListAppsByEnvironment(staging): %v", err)
	}
	if len(stagingApps) != 1 || stagingApps[0].Environment != "staging" {
		t.Fatalf("staging 应当只看到自己那一条，实际 %+v", stagingApps)
	}

	// 发布记录自己没有 environment 列，环境经父行判定——这条用例钉住那个 JOIN。
	prodReleases, err := s.ListReleasesByEnvironment(ctx, "production")
	if err != nil {
		t.Fatalf("ListReleasesByEnvironment(production): %v", err)
	}
	if len(prodReleases) != 1 || prodReleases[0].Version != "prod-1" {
		t.Fatalf("production 的发布记录 = %+v", prodReleases)
	}
	stagingReleases, err := s.ListReleasesByEnvironment(ctx, "staging")
	if err != nil {
		t.Fatalf("ListReleasesByEnvironment(staging): %v", err)
	}
	if len(stagingReleases) != 0 {
		t.Fatalf("staging 不该看到 production 的发布记录，实际 %+v", stagingReleases)
	}
}

func TestReleasesAreListedNewestFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	app := mustCreateApp(t, s, extapp.App{AppKey: "admin-web"})
	for _, spec := range []struct {
		version string
		at      string
	}{
		{"最早", "2026-09-01T10:00:00Z"},
		{"最新", "2026-09-08T10:00:00Z"},
		{"中间", "2026-09-05T10:00:00Z"},
	} {
		if _, err := s.CreateRelease(ctx, extapp.Release{
			AppID: app.ID, Version: spec.version, Kind: extapp.ReleaseDeploy,
			ReleasedAt: at(spec.at), ReleasedBy: "平台组",
		}); err != nil {
			t.Fatalf("CreateRelease(%s): %v", spec.version, err)
		}
	}
	got, err := s.ListReleasesByEnvironment(ctx, testEnv)
	if err != nil {
		t.Fatalf("ListReleasesByEnvironment: %v", err)
	}
	var versions []string
	for _, r := range got {
		versions = append(versions, r.Version)
	}
	if strings.Join(versions, ",") != "最新,中间,最早" {
		t.Fatalf("发布记录应当按 released_at 倒序，实际 %v", versions)
	}
}

func TestGetAppReportsNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetApp(context.Background(), uuid.New())
	if !errors.Is(err, extapp.ErrNotFound) {
		t.Fatalf("未知 id 应当返回 ErrNotFound，实际 %v", err)
	}
}
