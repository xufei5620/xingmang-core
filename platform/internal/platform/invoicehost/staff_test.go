package invoicehost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
)

type fakeStaffStore struct {
	account localauth.Account
	err     error
	calls   int
}

func (s *fakeStaffStore) LookupSession(_ context.Context, token string) (localauth.Account, error) {
	s.calls++
	if token != "synthetic-staff-session" {
		return localauth.Account{}, errors.New("unknown session")
	}
	return s.account, s.err
}
func staffFixture(t *testing.T) (*fakeStaffStore, StaffConfig) {
	t.Helper()
	now := time.Now().UTC()
	allowlist, err := localauth.ParseAdminIPAllowlist("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeStaffStore{account: localauth.Account{ID: uuid.MustParse("e3b053c0-257f-4878-a11c-77c08d4b7055"), Username: "operator", Roles: []string{"admin"}, SessionMFAAt: &now, SessionAMR: []string{"pwd", "otp"}}}, StaffConfig{Origin: "https://console.example.test", Environment: "production", RoleScopes: map[string][]string{"admin": {"finance.read"}}, AdminIPAllowlist: allowlist, TrustedProxyCIDRs: []string{"127.0.0.1/32"}}
}
func staffRequest(method string) *http.Request {
	r := httptest.NewRequest(method, "https://console.example.test/invoice-api/v1/auth/staff-session", nil)
	r.RemoteAddr = "192.0.2.17:443"
	r.AddCookie(&http.Cookie{Name: localauth.SessionCookieName, Value: "synthetic-staff-session"})
	r.Header.Set("X-Requested-With", "xingmang")
	return r
}
func TestStaffIdentityUsesCurrentAccountAndDerivedCSRF(t *testing.T) {
	store, cfg := staffFixture(t)
	resolve, err := NewStaffResolver(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := staffRequest(http.MethodGet)
	got, err := resolve(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != store.account.ID.String() || got.Issuer != cfg.Origin || got.DisplayName != "operator" || len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("wrong verified identity: %+v", got)
	}
	if len(got.CSRFToken) != 43 || got.CSRFToken == "synthetic-staff-session" {
		t.Fatal("invalid derived CSRF")
	}
	again, err := resolve(r)
	if err != nil || again.CSRFToken != got.CSRFToken {
		t.Fatal("CSRF must be stable within the current process/session")
	}
	if store.calls != 2 {
		t.Fatalf("session must be freshly read once per request, got %d", store.calls)
	}
	store.err = errors.New("revoked")
	if _, err := resolve(r); err == nil {
		t.Fatal("revoked session accepted")
	}
	store.err = nil
	store.account.Roles = []string{"ops"}
	if _, err := resolve(r); err == nil {
		t.Fatal("permission removal not effective immediately")
	}
}
func TestStaffResolverRejectsUntrustedRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request, *fakeStaffStore)
	}{
		{"missing-cookie", func(r *http.Request, _ *fakeStaffStore) { r.Header.Del("Cookie") }},
		{"duplicate-cookie", func(r *http.Request, _ *fakeStaffStore) {
			r.AddCookie(&http.Cookie{Name: localauth.SessionCookieName, Value: "another"})
		}},
		{"bearer", func(r *http.Request, _ *fakeStaffStore) { r.Header.Set("Authorization", "Bearer attacker") }},
		{"csrf-missing", func(r *http.Request, _ *fakeStaffStore) { r.Header.Del("X-Requested-With") }},
		{"csrf-duplicate", func(r *http.Request, _ *fakeStaffStore) { r.Header.Add("X-Requested-With", "attacker") }},
		{"disabled", func(_ *http.Request, s *fakeStaffStore) { s.account.Disabled = true }},
		{"password-reset", func(_ *http.Request, s *fakeStaffStore) { s.account.MustChangePassword = true }},
		{"invalid-subject", func(_ *http.Request, s *fakeStaffStore) { s.account.ID = uuid.Nil }},
		{"untrusted-forwarding", func(r *http.Request, _ *fakeStaffStore) {
			r.RemoteAddr = "198.51.100.7:443"
			r.Header.Set("CF-Connecting-IP", "192.0.2.17")
			r.Header.Set("X-Forwarded-For", "192.0.2.17")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := staffFixture(t)
			resolve, err := NewStaffResolver(s, c)
			if err != nil {
				t.Fatal(err)
			}
			r := staffRequest(http.MethodPost)
			tc.mutate(r, s)
			if _, err = resolve(r); err == nil {
				t.Fatal("untrusted request accepted")
			}
		})
	}
}
func TestStaffResolverDoesNotUpgradePasswordToMFA(t *testing.T) {
	s, c := staffFixture(t)
	s.account.SessionMFAAt = nil
	s.account.SessionAMR = []string{"pwd"}
	resolve, err := NewStaffResolver(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolve(staffRequest(http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	if got.MFAAt != nil {
		t.Fatal("password promoted to MFA")
	}
	now := time.Now().UTC()
	s.account.SessionMFAAt = &now
	got, err = resolve(staffRequest(http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	if got.MFAAt != nil {
		t.Fatal("MFA timestamp without OTP evidence promoted to MFA")
	}
}
func TestStaffConfigRejectsAmbiguousOrigin(t *testing.T) {
	for _, origin := range []string{"", "http://console.example.test", "https://console.example.test/path", "https://user@console.example.test", "https://console.example.test?x=1", "https://*.example.test", "https://console.example.test#"} {
		s, c := staffFixture(t)
		c.Origin = origin
		if _, err := NewStaffResolver(s, c); err == nil {
			t.Errorf("accepted origin %q", origin)
		}
	}
}
