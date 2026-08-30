package localauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type fakeSessionLookup struct {
	acc Account
	err error
}

func (f *fakeSessionLookup) LookupSession(context.Context, string) (Account, error) {
	return f.acc, f.err
}

func testAccount(roles ...string) Account {
	return Account{ID: uuid.New(), Username: "alice", DisplayName: "Alice", Roles: roles}
}

func TestResolveMissingCookie(t *testing.T) {
	r := NewResolver(&fakeSessionLookup{}, "staging", RoleScopesFrom(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	if _, err := r.Resolve(req); err == nil {
		t.Fatal("缺少 cookie 应报错")
	}
}

func TestResolveEmptyCookieValue(t *testing.T) {
	r := NewResolver(&fakeSessionLookup{}, "staging", RoleScopesFrom(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: ""})
	if _, err := r.Resolve(req); err == nil {
		t.Fatal("空 cookie 值应报错")
	}
}

func TestResolveInvalidSession(t *testing.T) {
	r := NewResolver(&fakeSessionLookup{err: ErrSessionInvalid}, "staging", RoleScopesFrom(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "whatever"})
	if _, err := r.Resolve(req); err == nil {
		t.Fatal("失效会话应报错")
	}
}

func TestResolveSuccessTranslatesRolesToScopesAndEnvironment(t *testing.T) {
	roleMap := map[string][]string{"staff": {"registry.read", "ops.read"}}
	r := NewResolver(&fakeSessionLookup{acc: testAccount("staff")}, "staging", RoleScopesFrom(roleMap))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "whatever"})
	p, err := r.Resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "staff:alice" || p.Environment != "staging" || p.IdentityZone != "staff" {
		t.Fatalf("p = %+v", p)
	}
	// 绝不从请求读环境
	if p.Environment != "staging" {
		t.Fatalf("environment 必须来自 Resolver 配置, got %q", p.Environment)
	}
	want := []string{"ops.read", "registry.read"}
	if len(p.Scopes) != len(want) || p.Scopes[0] != want[0] || p.Scopes[1] != want[1] {
		t.Fatalf("scopes = %v, want %v", p.Scopes, want)
	}
}

func TestResolveEnforcesCSRFHeaderOnMutatingMethods(t *testing.T) {
	r := NewResolver(&fakeSessionLookup{acc: testAccount("staff")}, "staging", RoleScopesFrom(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "whatever"})
	if _, err := r.Resolve(req); err == nil {
		t.Fatal("缺少 CSRF 头的 POST 应被拒绝")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", nil)
	req2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "whatever"})
	req2.Header.Set("X-Requested-With", "not-xingmang")
	if _, err := r.Resolve(req2); err == nil {
		t.Fatal("CSRF 头值不对也应被拒绝")
	}

	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", nil)
	req3.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "whatever"})
	req3.Header.Set("X-Requested-With", "xingmang")
	if _, err := r.Resolve(req3); err != nil {
		t.Fatalf("带正确 CSRF 头应通过: %v", err)
	}
}

func TestResolveAllowsReadOnlyMethodsWithoutCSRFHeader(t *testing.T) {
	r := NewResolver(&fakeSessionLookup{acc: testAccount("staff")}, "staging", RoleScopesFrom(nil))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/api/v1/services", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "whatever"})
		if _, err := r.Resolve(req); err != nil {
			t.Fatalf("%s 不该要求 CSRF 头: %v", method, err)
		}
	}
}

func TestRoleScopesFromDedupsAndSorts(t *testing.T) {
	roleMap := map[string][]string{
		"staff": {"ops.read", "registry.read"},
		"admin": {"registry.read", "audit.read"},
	}
	scopes := RoleScopesFrom(roleMap)([]string{"staff", "admin"})
	want := []string{"audit.read", "ops.read", "registry.read"}
	if len(scopes) != len(want) {
		t.Fatalf("scopes = %v, want %v", scopes, want)
	}
	for i := range want {
		if scopes[i] != want[i] {
			t.Fatalf("scopes = %v, want %v", scopes, want)
		}
	}
}

func TestRoleScopesFromIgnoresUnknownRoles(t *testing.T) {
	roleMap := map[string][]string{"staff": {"registry.read"}}
	scopes := RoleScopesFrom(roleMap)([]string{"staff", "not-a-real-role"})
	if len(scopes) != 1 || scopes[0] != "registry.read" {
		t.Fatalf("scopes = %v", scopes)
	}
}
