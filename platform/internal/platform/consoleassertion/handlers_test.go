package consoleassertion

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// fakeSigner lets tests control Sign's outcome without a real Ed25519 key.
type fakeSigner struct {
	assertion string
	expiresAt time.Time
	nonce     string
	err       error
	calls     int
	lastInput ClaimsInput
}

func (f *fakeSigner) Sign(_ time.Time, in ClaimsInput) (string, time.Time, string, error) {
	f.calls++
	f.lastInput = in
	if f.err != nil {
		return "", time.Time{}, "", f.err
	}
	return f.assertion, f.expiresAt, f.nonce, nil
}

// fakeAccounts is a minimal staffAccountLookup.
type fakeAccounts struct {
	byUsername map[string]localauth.Account
	err        error
}

func (f *fakeAccounts) GetByUsername(_ context.Context, username string) (localauth.Account, error) {
	if f.err != nil {
		return localauth.Account{}, f.err
	}
	acc, ok := f.byUsername[username]
	if !ok {
		return localauth.Account{}, localauth.ErrNotFound
	}
	return acc, nil
}

// fakeAudit records every event appended, so tests can assert on it.
type fakeAudit struct {
	events []audit.Event
}

func (f *fakeAudit) Append(_ context.Context, e audit.Event) (audit.Event, error) {
	f.events = append(f.events, e)
	return e, nil
}

func testAccountID() uuid.UUID { return uuid.MustParse("11111111-1111-1111-1111-111111111111") }

func newTestHandlers(signer assertionSigner, accounts staffAccountLookup, allowlist localauth.AdminIPAllowlist, auditStore auditAppender) *Handlers {
	h := NewHandlers(signer, accounts, allowlist, Config{Issuer: "https://console.solov.cc"}, "production", auditStore, nil)
	h.now = func() time.Time { return time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC) }
	return h
}

func newRequestWithPrincipal(t *testing.T, body string, p *principal.Principal) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/console-assertion", bytes.NewBufferString(body))
	req.Header.Set("X-Requested-With", "xingmang")
	if p != nil {
		req = req.WithContext(principal.WithPrincipal(req.Context(), *p))
	}
	return req
}

func financeReadPrincipal(mfaAt *time.Time) principal.Principal {
	return principal.Principal{
		ID: "staff:alice", Type: principal.TypeHuman, Issuer: "xingmang://localauth",
		Subject: testAccountID().String(), Environment: "production",
		Scopes: []string{financeReadScope}, MFAAt: mfaAt,
	}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (raw=%s)", err, rec.Body.String())
	}
	return rec.Code, body.Error.Code
}

func TestIssueRequiresPrincipal(t *testing.T) {
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, localauth.AdminIPAllowlist{}, nil)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, nil)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusUnauthorized || code != "PERMISSION_DENIED" {
		t.Fatalf("got status=%d code=%s, want 401 PERMISSION_DENIED", status, code)
	}
}

func TestIssueRejectsMalformedBody(t *testing.T) {
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, localauth.AdminIPAllowlist{}, nil)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `not json`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusBadRequest || code != "INVALID_PARAMS" {
		t.Fatalf("got status=%d code=%s, want 400 INVALID_PARAMS", status, code)
	}
}

func TestIssueRejectsInvalidScope(t *testing.T) {
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, localauth.AdminIPAllowlist{}, nil)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"invoicing"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusBadRequest || code != "INVALID_PARAMS" {
		t.Fatalf("got status=%d code=%s, want 400 INVALID_PARAMS", status, code)
	}
}

func TestIssueRejectsMissingFinanceScope(t *testing.T) {
	auditStore := &fakeAudit{}
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, localauth.AdminIPAllowlist{}, auditStore)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := principal.Principal{
		ID: "staff:bob", Type: principal.TypeHuman, Issuer: "xingmang://localauth",
		Subject: testAccountID().String(), Environment: "production", Scopes: []string{"ops.read"}, MFAAt: &now,
	}
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusForbidden || code != "FINANCE_SCOPE_REQUIRED" {
		t.Fatalf("got status=%d code=%s, want 403 FINANCE_SCOPE_REQUIRED", status, code)
	}
	if len(auditStore.events) != 1 || auditStore.events[0].CompensationResult != "finance_scope_missing" {
		t.Fatalf("expected one failed audit event with finance_scope_missing, got %+v", auditStore.events)
	}
	if auditStore.events[0].ResourceID != "" {
		t.Fatalf("failed issuance must not carry a nonce (none was ever generated): %+v", auditStore.events[0])
	}
}

func TestIssueRejectsDeniedIP(t *testing.T) {
	allowlist, err := localauth.ParseAdminIPAllowlist("203.0.113.0/24")
	if err != nil {
		t.Fatalf("ParseAdminIPAllowlist: %v", err)
	}
	auditStore := &fakeAudit{}
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, allowlist, auditStore)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	req.RemoteAddr = "198.51.100.1:12345"
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusForbidden || code != "ADMIN_NETWORK_DENIED" {
		t.Fatalf("got status=%d code=%s, want 403 ADMIN_NETWORK_DENIED", status, code)
	}
	if len(auditStore.events) != 1 || auditStore.events[0].CompensationResult != "ip_denied" {
		t.Fatalf("expected one failed audit event with ip_denied, got %+v", auditStore.events)
	}
}

func TestIssueAllowsWhitelistedIP(t *testing.T) {
	allowlist, err := localauth.ParseAdminIPAllowlist("203.0.113.0/24")
	if err != nil {
		t.Fatalf("ParseAdminIPAllowlist: %v", err)
	}
	accounts := &fakeAccounts{byUsername: map[string]localauth.Account{
		"alice": {ID: testAccountID(), Username: "alice", Roles: []string{"admin"}},
	}}
	signer := &fakeSigner{assertion: "jws", expiresAt: time.Date(2026, 9, 15, 10, 5, 0, 0, time.UTC), nonce: "nonce-value"}
	h := newTestHandlers(signer, accounts, allowlist, nil)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	req.RemoteAddr = "203.0.113.5:12345"
	h.Issue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIssueRejectsStaleStepUp(t *testing.T) {
	auditStore := &fakeAudit{}
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, localauth.AdminIPAllowlist{}, auditStore)
	stale := time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC) // 1 hour before h.now(), beyond default 10m
	p := financeReadPrincipal(&stale)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusForbidden || code != "ADMIN_STEP_UP_REQUIRED" {
		t.Fatalf("got status=%d code=%s, want 403 ADMIN_STEP_UP_REQUIRED", status, code)
	}
	if len(auditStore.events) != 1 || auditStore.events[0].CompensationResult != "step_up_required" {
		t.Fatalf("expected one failed audit event with step_up_required, got %+v", auditStore.events)
	}
}

func TestIssueRejectsNeverAuthenticatedMFA(t *testing.T) {
	h := newTestHandlers(&fakeSigner{}, &fakeAccounts{}, localauth.AdminIPAllowlist{}, nil)
	p := financeReadPrincipal(nil) // MFAAt nil: never passed TOTP this session
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusForbidden || code != "ADMIN_STEP_UP_REQUIRED" {
		t.Fatalf("got status=%d code=%s, want 403 ADMIN_STEP_UP_REQUIRED", status, code)
	}
}

func TestIssueReturns500WhenSignerUnavailable(t *testing.T) {
	h := NewHandlers(nil, &fakeAccounts{}, localauth.AdminIPAllowlist{}, Config{Issuer: "https://console.solov.cc"}, "production", nil, nil)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusInternalServerError || code != "INTERNAL" {
		t.Fatalf("got status=%d code=%s, want 500 INTERNAL", status, code)
	}
}

func TestIssueReturns500OnAccountLookupFailure(t *testing.T) {
	accounts := &fakeAccounts{err: localauth.ErrStore}
	h := newTestHandlers(&fakeSigner{}, accounts, localauth.AdminIPAllowlist{}, nil)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusInternalServerError || code != "INTERNAL" {
		t.Fatalf("got status=%d code=%s, want 500 INTERNAL", status, code)
	}
}

func TestIssueReturns500OnSignFailure(t *testing.T) {
	accounts := &fakeAccounts{byUsername: map[string]localauth.Account{
		"alice": {ID: testAccountID(), Username: "alice", Roles: []string{"admin"}},
	}}
	signer := &fakeSigner{err: context.DeadlineExceeded}
	h := newTestHandlers(signer, accounts, localauth.AdminIPAllowlist{}, nil)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"sub2api"}`, &p)
	h.Issue(rec, req)
	status, code := decodeError(t, rec)
	if status != http.StatusInternalServerError || code != "INTERNAL" {
		t.Fatalf("got status=%d code=%s, want 500 INTERNAL", status, code)
	}
}

func TestIssueSuccessReturnsAssertionAndRecordsAudit(t *testing.T) {
	accounts := &fakeAccounts{byUsername: map[string]localauth.Account{
		"alice": {ID: testAccountID(), Username: "alice", Roles: []string{"admin", "credential-admin"}},
	}}
	signer := &fakeSigner{
		assertion: "header.payload.signature",
		expiresAt: time.Date(2026, 9, 15, 10, 5, 0, 0, time.UTC),
		nonce:     "0123456789abcdefghijklmnopqrstuvwxyzABCDEF",
	}
	auditStore := &fakeAudit{}
	h := newTestHandlers(signer, accounts, localauth.AdminIPAllowlist{}, auditStore)
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p := financeReadPrincipal(&now)
	rec := httptest.NewRecorder()
	req := newRequestWithPrincipal(t, `{"scope":"newapi"}`, &p)
	h.Issue(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Assertion string `json:"assertion"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode success body: %v", err)
	}
	if body.Assertion != signer.assertion {
		t.Fatalf("assertion = %q, want %q", body.Assertion, signer.assertion)
	}
	if body.ExpiresAt != "2026-09-15T10:05:00Z" {
		t.Fatalf("expires_at = %q, want RFC3339 UTC", body.ExpiresAt)
	}

	if signer.calls != 1 {
		t.Fatalf("signer called %d times, want 1", signer.calls)
	}
	if signer.lastInput.Subject != testAccountID().String() {
		t.Fatalf("sub = %q, want the account's UUID", signer.lastInput.Subject)
	}
	if signer.lastInput.Username != "alice" {
		t.Fatalf("username = %q, want alice", signer.lastInput.Username)
	}
	if len(signer.lastInput.Roles) != 2 || signer.lastInput.Roles[0] != "admin" {
		t.Fatalf("roles = %+v, want the account's real roles (not scopes)", signer.lastInput.Roles)
	}
	if signer.lastInput.Scope != "newapi" {
		t.Fatalf("scope = %q, want newapi", signer.lastInput.Scope)
	}
	if signer.lastInput.Issuer != "https://console.solov.cc" {
		t.Fatalf("issuer = %q, want the configured issuer", signer.lastInput.Issuer)
	}
	if signer.lastInput.Audience != DefaultAudience {
		t.Fatalf("audience = %q, want the default", signer.lastInput.Audience)
	}

	if len(auditStore.events) != 1 {
		t.Fatalf("expected exactly one audit event, got %d", len(auditStore.events))
	}
	ev := auditStore.events[0]
	if ev.Result != audit.ResultSucceeded {
		t.Fatalf("audit result = %v, want succeeded", ev.Result)
	}
	if ev.ActionID != auditActionIssue || ev.ResourceType != resourceConsoleAssertion {
		t.Fatalf("audit action/resource mismatch: %+v", ev)
	}
	// nonce is truncated, never logged in full (spec §7)
	if ev.ResourceID != signer.nonce[:truncatedNonceLen] {
		t.Fatalf("audit ResourceID = %q, want a %d-char truncation of the nonce", ev.ResourceID, truncatedNonceLen)
	}
	if len(ev.ResourceID) >= len(signer.nonce) {
		t.Fatal("audit must not carry the full nonce")
	}
}

func TestValidateIssuer(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		want    string
	}{
		{"valid", "https://console.solov.cc", false, "https://console.solov.cc"},
		{"valid with trailing slash", "https://console.solov.cc/", false, "https://console.solov.cc"},
		{"rejects http", "http://console.solov.cc", true, ""},
		{"rejects path", "https://console.solov.cc/admin", true, ""},
		{"rejects query", "https://console.solov.cc/?x=1", true, ""},
		{"rejects fragment", "https://console.solov.cc/#x", true, ""},
		{"rejects empty", "", true, ""},
		{"rejects userinfo", "https://user@console.solov.cc", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateIssuer(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
