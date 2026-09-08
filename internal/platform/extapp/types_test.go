package extapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validApp() App {
	return App{
		AppKey:        "admin-web",
		DisplayName:   "运营后台",
		PrimaryDomain: "admin.example.test",
		AuthMode:      AuthOIDC,
		Owner:         "平台组",
		Status:        AppActive,
		Environment:   "production",
	}
}

func TestAppValidateAcceptsAWellFormedRegistration(t *testing.T) {
	if err := validApp().Validate(); err != nil {
		t.Fatalf("合法登记被拒: %v", err)
	}
}

// 域名字段只收主机名。这条规则同时是一道**凭据泄漏闸**：登记 → Action →
// 审计摘要 → 审计页回显，链的终点是一条改不掉的记录，所以带凭据的地址必须在
// 入口就被拒（同 registry.validateEndpointCarriesNoCredential 要挡的那条链）。
func TestAppValidateRejectsAnythingThatIsNotABareHostname(t *testing.T) {
	cases := []struct {
		name   string
		domain string
	}{
		{"整条 URL", "https://admin.example.test"},
		{"带用户信息段（这正是凭据泄漏的形态）", "admin:s3cret@admin.example.test"},
		{"只有 userinfo 的 at 号", "user@admin.example.test"},
		{"带路径", "admin.example.test/admin"},
		{"带查询串", "admin.example.test?token=abc"},
		{"带端口", "admin.example.test:8443"},
		{"大写字母（唯一索引区分大小写，必须先归一）", "Admin.Example.Test"},
		{"没有点的裸标签", "localhost"},
		{"以连字符开头的段", "-admin.example.test"},
		{"以连字符结尾的段", "admin-.example.test"},
		{"下划线", "admin_web.example.test"},
		{"内嵌空格", "admin example.test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := validApp()
			app.PrimaryDomain = tc.domain
			err := app.Validate()
			if err == nil {
				t.Fatalf("primary_domain=%q 应被拒，实际通过", tc.domain)
			}
			if !errors.Is(err, ErrInvalidFormat) {
				t.Fatalf("错误应是 ErrInvalidFormat，实际 %v", err)
			}
		})
	}
}

// 对照组：合法主机名的几种形状都必须通过。
//
// 没有这一组的话，上面那组用「永远返回错误」也能全绿——而那会让这个字段
// 一个域名都登记不进去。
func TestAppValidateAcceptsOrdinaryHostnames(t *testing.T) {
	for _, domain := range []string{
		"admin.example.test",
		"a.b",
		"console-staging.example.test",
		"x1.y2.z3.example.test",
	} {
		app := validApp()
		app.PrimaryDomain = domain
		if err := app.Validate(); err != nil {
			t.Fatalf("primary_domain=%q 是合法主机名，却被拒: %v", domain, err)
		}
	}
}

// 域名可以留空：规划中的站点还没有域名，逼人编一个假域名比空着更糟。
func TestAppValidateAcceptsEmptyDomain(t *testing.T) {
	app := validApp()
	app.PrimaryDomain = ""
	app.Status = AppPlanned
	if err := app.Validate(); err != nil {
		t.Fatalf("未登记域名的规划中站点应当合法: %v", err)
	}
}

func TestAppValidateRequiresKeyNameAndOwner(t *testing.T) {
	cases := []struct {
		name  string
		mutfn func(*App)
	}{
		{"app_key 为空", func(a *App) { a.AppKey = "" }},
		{"app_key 只有空白", func(a *App) { a.AppKey = "   " }},
		{"display_name 为空", func(a *App) { a.DisplayName = "" }},
		{"owner 为空", func(a *App) { a.Owner = "" }},
		{"environment 为空", func(a *App) { a.Environment = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := validApp()
			tc.mutfn(&app)
			if err := app.Validate(); err == nil {
				t.Fatal("应被拒，实际通过")
			}
		})
	}
}

func TestAppValidateRejectsMalformedAppKey(t *testing.T) {
	for _, key := range []string{
		"Admin-Web",             // 大写
		"-admin",                // 连字符开头
		"admin_web",             // 下划线
		"admin web",             // 空格
		"admin.web",             // 点
		strings.Repeat("a", 65), // 超长
	} {
		app := validApp()
		app.AppKey = key
		if err := app.Validate(); err == nil {
			t.Fatalf("app_key=%q 应被拒，实际通过", key)
		}
	}
}

func TestAppValidateRejectsUnknownEnums(t *testing.T) {
	app := validApp()
	app.Status = AppStatus("deleted")
	if err := app.Validate(); err == nil {
		t.Fatal("未知 status 应被拒")
	}

	app = validApp()
	app.AuthMode = AuthMode("saml")
	if err := app.Validate(); err == nil {
		t.Fatal("未知 auth_mode 应被拒")
	}
}

// auth_mode 的三个取值不是这里现编的：它们逐字取自
// deploy/docker/web-app-config.sh 的 XM_WEB_AUTH_MODE，那是前端运行时真正
// 认得的三个值（容器启动时校验，不认的直接拒绝启动）。
//
// 这条用例把「登记簿认的登录方式」与「运行时认的登录方式」钉在一起：
// 哪天有人往枚举里加一个运行时不认的值，或者运行时改了取值而登记簿没跟，
// 这里会红——而不是等到有人按登记去配一个起不来的容器。
func TestAuthModeEnumMatchesTheRuntimeScript(t *testing.T) {
	for _, mode := range []AuthMode{AuthDevHeader, AuthOIDC, AuthLocal} {
		got, err := ParseAuthMode(string(mode))
		if err != nil {
			t.Fatalf("ParseAuthMode(%q): %v", mode, err)
		}
		if got != mode {
			t.Fatalf("ParseAuthMode(%q) = %q", mode, got)
		}
	}
	if string(AuthDevHeader) != "dev-header" ||
		string(AuthOIDC) != "oidc" || string(AuthLocal) != "local" {
		t.Fatalf("三个取值必须与 deploy/docker/web-app-config.sh 的 XM_WEB_AUTH_MODE 逐字相同，"+
			"实际 %q / %q / %q", AuthDevHeader, AuthOIDC, AuthLocal)
	}
	// 空串 = 未登记，是合法的；这一条与上面几个一起，说明「未登记」不是
	// 靠某个哨兵值伪装的。
	if _, err := ParseAuthMode(""); err != nil {
		t.Fatalf("空串应当合法（未登记）: %v", err)
	}
}

func validRelease(appID uuid.UUID) Release {
	return Release{
		AppID:      appID,
		Version:    "2026.09.08-1",
		CommitSHA:  "8c446e5",
		Kind:       ReleaseDeploy,
		ReleasedAt: mustTime(t0),
		ReleasedBy: "平台组",
	}
}

const t0 = "2026-09-08T10:00:00Z"

// mustTime 解析 RFC3339；空串返回零值（= 未登记 / 未给出）。
func mustTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestReleaseValidateAcceptsAWellFormedRecord(t *testing.T) {
	if err := validRelease(uuid.New()).Validate(); err != nil {
		t.Fatalf("合法发布记录被拒: %v", err)
	}
}

func TestReleaseValidateRequiresVersionReleaserAndTime(t *testing.T) {
	cases := []struct {
		name  string
		mutfn func(*Release)
	}{
		{"app_id 为空", func(r *Release) { r.AppID = uuid.Nil }},
		{"version 为空", func(r *Release) { r.Version = "" }},
		{"released_by 为空", func(r *Release) { r.ReleasedBy = "" }},
		{"released_at 为零值", func(r *Release) { r.ReleasedAt = mustTime("") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel := validRelease(uuid.New())
			tc.mutfn(&rel)
			if err := rel.Validate(); err == nil {
				t.Fatal("应被拒，实际通过")
			}
		})
	}
}

func TestReleaseValidateChecksCommitShape(t *testing.T) {
	for _, sha := range []string{
		"8C446E5",               // 大写
		"8c446",                 // 太短
		"zzzzzzz",               // 非十六进制
		strings.Repeat("a", 41), // 太长
		"8c446e5 ",              // 尾随空格
	} {
		rel := validRelease(uuid.New())
		rel.CommitSHA = sha
		if err := rel.Validate(); err == nil {
			t.Fatalf("commit_sha=%q 应被拒，实际通过", sha)
		}
	}
	// 对照组：短 SHA 与全长 SHA 都必须收。只写上面那组的话，
	// 「永远拒绝」也能全绿，而那会让提交号这一列永远填不进去。
	for _, sha := range []string{"8c446e5", strings.Repeat("a", 40), ""} {
		rel := validRelease(uuid.New())
		rel.CommitSHA = sha
		if err := rel.Validate(); err != nil {
			t.Fatalf("commit_sha=%q 应当合法，却被拒: %v", sha, err)
		}
	}
}

func TestParseReleaseKindDefaultsToDeploy(t *testing.T) {
	got, err := ParseReleaseKind("")
	if err != nil {
		t.Fatalf("空串应当合法: %v", err)
	}
	if got != ReleaseDeploy {
		t.Fatalf("空串应当默认成 deploy，实际 %q", got)
	}
	if _, err := ParseReleaseKind("hotfix"); err == nil {
		t.Fatal("未知 kind 应被拒")
	}
}
