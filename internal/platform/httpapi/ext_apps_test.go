package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/extapp"
)

type fakeExtAppRegistry struct {
	gotAppEnv     string
	gotReleaseEnv string

	apps     []extapp.AppWithRelease
	releases []extapp.Release

	err error
}

func (f *fakeExtAppRegistry) ListAppsByEnvironment(_ context.Context, env string) ([]extapp.AppWithRelease, error) {
	f.gotAppEnv = env
	return f.apps, f.err
}

func (f *fakeExtAppRegistry) ListReleasesByEnvironment(_ context.Context, env string) ([]extapp.Release, error) {
	f.gotReleaseEnv = env
	return f.releases, f.err
}

func extAppRouter(t *testing.T, reg *fakeExtAppRegistry) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         nil,
		ActionRegistry: action.NewRegistry(),
		ExtApps:        reg,
		ExtAppReleases: reg,
	})
}

func sampleExtRelease(appID uuid.UUID) extapp.Release {
	return extapp.Release{
		ID:         uuid.New(),
		AppID:      appID,
		Version:    "2026.09.08-1",
		CommitSHA:  "8c446e5",
		Kind:       extapp.ReleaseDeploy,
		ReleasedAt: time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC),
		ReleasedBy: "平台组",
		CreatedAt:  time.Date(2026, 9, 8, 10, 5, 0, 0, time.UTC),
	}
}

func sampleExtApp() extapp.AppWithRelease {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	id := uuid.New()
	rel := sampleExtRelease(id)
	return extapp.AppWithRelease{
		App: extapp.App{
			ID:            id,
			AppKey:        "admin-web",
			DisplayName:   "运营后台",
			PrimaryDomain: "admin.example.test",
			AuthMode:      extapp.AuthOIDC,
			Owner:         "平台组",
			Status:        extapp.AppActive,
			Environment:   "development",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		CurrentRelease: &rel,
	}
}

func TestListExtAppsHandlerReturnsItemsAndRespectsScope(t *testing.T) {
	reg := &fakeExtAppRegistry{apps: []extapp.AppWithRelease{sampleExtApp()}}
	h := extAppRouter(t, reg)

	t.Run("registry.read 授权时返回列表", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/ext/apps", "registry.read"))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if reg.gotAppEnv != "development" {
			t.Fatalf("gotAppEnv = %q, want development", reg.gotAppEnv)
		}
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if len(body.Items) != 1 {
			t.Fatalf("items 长度 = %d, want 1", len(body.Items))
		}
		item := body.Items[0]
		if item["app_key"] != "admin-web" || item["display_name"] != "运营后台" ||
			item["primary_domain"] != "admin.example.test" || item["auth_mode"] != "oidc" ||
			item["owner"] != "平台组" || item["status"] != "active" {
			t.Fatalf("条目字段不对: %#v", item)
		}
		rel, ok := item["current_release"].(map[string]any)
		if !ok {
			t.Fatalf("current_release 应当是对象, got %#v", item["current_release"])
		}
		if rel["version"] != "2026.09.08-1" || rel["commit_sha"] != "8c446e5" {
			t.Fatalf("current_release 内容不对: %#v", rel)
		}
		// released_at（发布真正发生的时刻）与 created_at（登记时刻）是两个
		// 字段，前端要能分辨"什么时候上的"和"什么时候记的"。
		if rel["released_at"] == rel["created_at"] {
			t.Fatalf("released_at 与 created_at 不该是同一个值: %#v", rel)
		}
	})

	t.Run("缺 registry.read 时 403", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/ext/apps", ""))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
		}
	})
}

// 没登记过发布的应用，current_release 序列化成 null。
//
// 不能悄悄变成一个空对象——那会在页面上显示成一个空版本号，而"没登记"和
// "版本是空的"是两件事（宪法 12 条同一条精神）。
//
// 配了对照组（上面那条用例断言有发布时它是对象），所以这条不是恒真断言。
func TestExtAppItemReportsNullReleaseWhenNoneRecorded(t *testing.T) {
	app := sampleExtApp()
	app.CurrentRelease = nil
	app.PrimaryDomain = ""
	app.AuthMode = ""

	reg := &fakeExtAppRegistry{apps: []extapp.AppWithRelease{app}}
	h := extAppRouter(t, reg)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/ext/apps", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	item := body.Items[0]
	// 正向锚点：这一行确实读出来了（不是空列表让下面的断言恒真）。
	if item["app_key"] != "admin-web" {
		t.Fatalf("app_key = %#v", item["app_key"])
	}
	raw, ok := item["current_release"]
	if !ok {
		t.Fatal("current_release 这个键必须在：缺键与 null 在前端是两种写法")
	}
	if raw != nil {
		t.Fatalf("没登记过发布时 current_release 应当是 null, got %#v", raw)
	}
	if item["primary_domain"] != "" || item["auth_mode"] != "" {
		t.Fatalf("未登记的可选字段应当是空串: %#v", item)
	}
}

func TestListExtAppReleasesHandlerReturnsItemsAndRespectsScope(t *testing.T) {
	appID := uuid.New()
	rel := sampleExtRelease(appID)
	rel.Kind = extapp.ReleaseRollback
	rel.CommitSHA = ""
	reg := &fakeExtAppRegistry{releases: []extapp.Release{rel}}
	h := extAppRouter(t, reg)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/ext/apps/releases", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if reg.gotReleaseEnv != "development" {
		t.Fatalf("gotReleaseEnv = %q, want development", reg.gotReleaseEnv)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items 长度 = %d, want 1", len(body.Items))
	}
	item := body.Items[0]
	if item["app_id"] != appID.String() {
		t.Fatalf("app_id = %#v", item["app_id"])
	}
	if item["kind"] != "rollback" {
		t.Fatalf("kind = %#v, want rollback", item["kind"])
	}
	if item["commit_sha"] != "" {
		t.Fatalf("没取到提交号时应当是空串, got %#v", item["commit_sha"])
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/ext/apps/releases", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
}

// 跨环境读同样被拒（resolveEnvironment 的既有规则）：一个 development 身份
// 带着 ?environment=production 来，必须 403 而不是拿到生产的登记。
func TestExtAppsRejectCrossEnvironmentReads(t *testing.T) {
	reg := &fakeExtAppRegistry{apps: []extapp.AppWithRelease{sampleExtApp()}}
	h := extAppRouter(t, reg)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet,
		"/api/v1/ext/apps?environment=production", "registry.read"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	// 被拒时不该已经查过库——403 之后还查一次，等于跨环境的数据已经被读出来了。
	if reg.gotAppEnv != "" {
		t.Fatalf("跨环境请求不该走到仓储，实际查了 %q", reg.gotAppEnv)
	}

	// 对照组：同环境显式带参数时必须放行，并且真的查了库。
	w = httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet,
		"/api/v1/ext/apps?environment=development", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("同环境显式带参数应当放行，status = %d", w.Code)
	}
	if reg.gotAppEnv != "development" {
		t.Fatalf("同环境请求应当查库，实际 %q", reg.gotAppEnv)
	}
}
