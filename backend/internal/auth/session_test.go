package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpaqueSessionCookieRotationRevocationBindingAndCSRF(t *testing.T) {
	store := NewMemorySessionStore()
	audit := &MemorySecurityAuditSink{}
	manager, err := NewSessionManager(store, SessionConfig{IdleTTL: time.Hour, AbsoluteTTL: 4 * time.Hour}, audit)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	manager.now = func() time.Time { return now }
	mfaAt := now
	principal := Principal{
		Issuer: "https://identity.example", Subject: "subject-1", ProviderSID: "provider-session",
		Roles: []string{"invoice-admin"}, ACR: "urn:invoice:mfa", AMR: []string{"pwd", "otp"}, AuthTime: now,
	}
	binding := ClientBinding{IPHash: sha256Hex("198.51.100.4"), UserAgentHash: sha256Hex("desktop-browser")}
	credentials, err := manager.Issue(context.Background(), IssueSessionInput{UserID: "20000000-0000-4000-8000-000000000001", Principal: principal, Binding: binding, MFAAt: &mfaAt, RequestID: "req-issue"})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Token == "" || credentials.CSRFToken == "" || credentials.Session.TokenHash == credentials.Token || credentials.Session.CSRFHash == credentials.CSRFToken {
		t.Fatal("raw session secrets were not separated from stored hashes")
	}
	cookie := manager.SessionCookie(credentials.Token, credentials.Session.AbsoluteExpiresAt)
	if cookie.Name != "__Host-invoice_session" || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe session cookie: %+v", cookie)
	}
	flowCookie, err := OIDCFlowCookie("abcdefghijklmnopqrstuvwxyzABCDEFGH0123456789-ab", now.Add(10*time.Minute))
	if err != nil || !flowCookie.Secure || !flowCookie.HttpOnly || flowCookie.Domain != "" || flowCookie.SameSite != http.SameSiteLaxMode || ClearOIDCFlowCookie().MaxAge != -1 {
		t.Fatalf("unsafe OIDC flow cookie: %+v err=%v", flowCookie, err)
	}
	if _, err = manager.Authenticate(context.Background(), credentials.Token, ClientBinding{IPHash: sha256Hex("203.0.113.9"), UserAgentHash: binding.UserAgentHash}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("wrong client binding error=%v", err)
	}
	session, err := manager.Authenticate(context.Background(), credentials.Token, binding)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := NewCSRFPolicy([]string{"https://invoice.example"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/invoices", nil)
	request.Header.Set("Origin", "https://invoice.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", credentials.CSRFToken)
	if err = csrf.ValidateMutation(request, session); err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://attacker.example")
	if err = csrf.ValidateMutation(request, session); err == nil {
		t.Fatal("cross-origin mutation accepted")
	}
	request.Header.Set("Origin", "https://invoice.example")
	request.Header.Set("X-CSRF-Token", "wrong-token-that-is-deliberately-long-enough-123456789")
	if err = csrf.ValidateMutation(request, session); err == nil {
		t.Fatal("wrong CSRF token accepted")
	}

	now = now.Add(time.Minute)
	newMFA := now
	rotated, err := manager.Rotate(context.Background(), RotateSessionInput{
		Token: credentials.Token, ExpectedSessionID: credentials.Session.ID,
		Principal: principal, Binding: binding, MFAAt: &newMFA, RequestID: "req-rotate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Session.FamilyID != credentials.Session.FamilyID || rotated.Session.RotatedFrom != credentials.Session.ID || rotated.Token == credentials.Token || rotated.CSRFToken == credentials.CSRFToken {
		t.Fatalf("session rotation did not preserve family/change secrets: %+v", rotated.Session)
	}
	if _, err = manager.Authenticate(context.Background(), credentials.Token, binding); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("rotated token still works: %v", err)
	}
	if _, err = manager.Authenticate(context.Background(), rotated.Token, binding); err != nil {
		t.Fatal(err)
	}
	if err = manager.RevokeToken(context.Background(), rotated.Token, "user logout", "req-revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Authenticate(context.Background(), rotated.Token, binding); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked token still works: %v", err)
	}
	if len(audit.Events) != 3 {
		t.Fatalf("audit events=%d want 3", len(audit.Events))
	}
}

func TestSessionRotationRejectsIdentitySwapAndWrongFlowSession(t *testing.T) {
	manager, err := NewSessionManager(NewMemorySessionStore(), SessionConfig{}, &MemorySecurityAuditSink{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	principal := Principal{Issuer: "https://identity.example", Subject: "subject-1"}
	issued, err := manager.Issue(context.Background(), IssueSessionInput{UserID: "user-1", Principal: principal, RequestID: "req-issue"})
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]RotateSessionInput{
		"wrong_session": {Token: issued.Token, ExpectedSessionID: "other-session", Principal: principal, RequestID: "req-wrong"},
		"identity_swap": {Token: issued.Token, ExpectedSessionID: issued.Session.ID, Principal: Principal{Issuer: principal.Issuer, Subject: "attacker"}, RequestID: "req-swap"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.Rotate(context.Background(), input); !errors.Is(err, ErrSessionInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err = manager.Authenticate(context.Background(), issued.Token, ClientBinding{}); err != nil {
		t.Fatal("failed rotation attempt revoked the valid session")
	}
}

func TestAdminPolicyRequiresExactRoleACRAMRAndFreshMFA(t *testing.T) {
	now := time.Now().UTC()
	policy := AdminPolicy{Role: "invoice-admin", RequiredACR: "urn:invoice:mfa", RequiredAMR: []string{"pwd", "otp"}, StepUpMaxAge: 10 * time.Minute}
	mfa := now.Add(-time.Minute)
	session := Session{Roles: []string{"invoice-admin"}, ACR: "urn:invoice:mfa", AMR: []string{"pwd", "otp"}, MFAAt: &mfa}
	if err := policy.AuthorizeSession(session, now); err != nil {
		t.Fatal(err)
	}
	session.Roles = []string{"invoice-user"}
	if err := policy.AuthorizeSession(session, now); !errors.Is(err, ErrAdminRoleRequired) {
		t.Fatalf("role error=%v", err)
	}
	session.Roles = []string{"invoice-admin"}
	session.AMR = []string{"pwd"}
	if err := policy.AuthorizeSession(session, now); !errors.Is(err, ErrMFAStepUpRequired) {
		t.Fatalf("AMR error=%v", err)
	}
}

func TestCSRFRejectsDuplicateHeadersAndUnsafeOriginConfig(t *testing.T) {
	if _, err := NewCSRFPolicy([]string{"https://*.example.com"}); err == nil {
		t.Fatal("wildcard origin accepted")
	}
	policy, _ := NewCSRFPolicy([]string{"https://invoice.example"})
	request := httptest.NewRequest(http.MethodPut, "https://invoice.example/api", nil)
	request.Header.Add("Origin", "https://invoice.example")
	request.Header.Add("Origin", "https://attacker.example")
	token := "abcdefghijklmnopqrstuvwxyzABCDEFGH0123456789-ab"
	request.Header.Set("X-CSRF-Token", token)
	if err := policy.ValidateMutation(request, Session{CSRFHash: sha256Hex(token)}); err == nil {
		t.Fatal("duplicate Origin headers accepted")
	}
}

func TestClientBindingUsesKeyedDomainSeparatedHMAC(t *testing.T) {
	hasher, err := newClientBindingHasher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := hasher.Hash("198.51.100.4", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if binding.IPHash == sha256Hex("198.51.100.4") || binding.UserAgentHash == sha256Hex("browser") || binding.IPHash == binding.UserAgentHash {
		t.Fatal("client binding was not keyed and domain separated")
	}
	if _, err = hasher.Hash("198.51.100.4\nspoof", "browser"); err == nil {
		t.Fatal("control characters accepted in client binding")
	}
}

func TestSessionIssueFailsClosedWhenAuditWriteFails(t *testing.T) {
	store := NewMemorySessionStore()
	manager, err := NewSessionManager(store, SessionConfig{}, failingAuditSink{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Issue(context.Background(), IssueSessionInput{UserID: "user-1", Principal: Principal{Issuer: "https://identity.example", Subject: "subject-1"}, RequestID: "req-audit-fail"}); err == nil {
		t.Fatal("session issued despite audit failure")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.byToken) != 1 {
		t.Fatalf("stored sessions=%d", len(store.byToken))
	}
	for _, session := range store.byToken {
		if session.RevokedAt == nil || session.RevokedReason != "audit write failed" {
			t.Fatal("audit-failed session remained active")
		}
	}
}

type failingAuditSink struct{}

func (failingAuditSink) RecordSecurityEvent(context.Context, SecurityAuditEvent) error {
	return errors.New("audit unavailable")
}
