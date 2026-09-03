package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/mailer"
)

type httpSettingsBox struct{}

func (httpSettingsBox) Seal(_ context.Context, plaintext []byte) (adminsettings.SecretEnvelope, error) {
	return adminsettings.SecretEnvelope{Ciphertext: append([]byte("encrypted:"), plaintext...), KeyVersion: "test-v1"}, nil
}
func (httpSettingsBox) Open(_ context.Context, envelope adminsettings.SecretEnvelope) ([]byte, error) {
	return envelope.Ciphertext[len("encrypted:"):], nil
}

func settingsServer(t *testing.T, adminCIDRs, breakGlassCIDRs []string) (*Server, *ledger.Service) {
	return settingsServerWithIssuer(t, "待配置开票主体", adminCIDRs, breakGlassCIDRs)
}

func settingsServerWithIssuer(t *testing.T, issuerName string, adminCIDRs, breakGlassCIDRs []string) (*Server, *ledger.Service) {
	t.Helper()
	service := ledger.NewService()
	now := time.Now().UTC()
	repo := adminsettings.NewMemoryRepository(adminsettings.Settings{
		IssuerName: issuerName, ServiceItem: adminsettings.FixedServiceItem,
		MinimumRequestMinor: adminsettings.MinimumMinor, SMTPHost: "smtp.qq.com", SMTPPort: 587,
		SMTPFrom: "invoice@qq.com", SMTPFromName: "发票中心", SMTPStartTLS: true,
		AdminCIDRs: adminCIDRs, Revision: 1, UpdatedBy: "bootstrap", CreatedAt: now, UpdatedAt: now,
	})
	server, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: adminCIDRs, BreakGlassCIDRs: breakGlassCIDRs, AdminSettings: adminsettings.NewService(repo, httpSettingsBox{})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, service
}

func adminRequest(method, path, remote string, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remote + ":443"
	request.Header.Set("X-Mock-User-ID", "admin-1")
	request.Header.Set("X-Mock-Role", "admin")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func testServer(t *testing.T) (*Server, *ledger.Service) {
	t.Helper()
	service := ledger.NewService()
	for _, lot := range []domain.FundingLot{
		{ID: "u1-lot", PrincipalID: "u1", SourceInstanceID: "sub2-main", SourceType: domain.SourceSub2API, ExternalOrderID: "1", Currency: domain.CurrencyCNY, OriginalMinor: 30_000, CurrentCapMinor: 30_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED"},
		{ID: "u2-lot", PrincipalID: "u2", SourceInstanceID: "sub2-main", SourceType: domain.SourceSub2API, ExternalOrderID: "2", Currency: domain.CurrencyCNY, OriginalMinor: 30_000, CurrentCapMinor: 30_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED"},
	} {
		if err := service.AddFundingLot(context.Background(), lot); err != nil {
			t.Fatal(err)
		}
	}
	return New(service, "mock", nil), service
}

func TestUserEndpointsRequireIdentityAndEnforceOwnership(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/user/funding-lots", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/user/funding-lots", nil)
	request.Header.Set("X-Mock-User-ID", "u1")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Items []userFundingLot `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "u1-lot" || strings.Contains(recorder.Body.String(), "principal_id") {
		t.Fatalf("ownership leak: %+v", body.Items)
	}
}

func TestRequestDetailEnforcesOwnershipAndListAdvertisesPagination(t *testing.T) {
	server, service := testServer(t)
	profile, err := service.SaveProfile(context.Background(), domain.InvoiceProfile{
		PrincipalID: "u1", Type: domain.ProfilePersonal, Title: "测试用户",
		Email: "verified@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := service.Submit(context.Background(), ledger.SubmitInput{
		PrincipalID: "u1", ProfileID: profile.ID, SourceInstanceID: "sub2-main",
		IdempotencyKey: "request-detail-test",
		Allocations:    []ledger.AllocationInput{{FundingLotID: "u1-lot", AmountMinor: 20_000}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	own := httptest.NewRequest(http.MethodGet, "/api/v1/user/invoice-requests/"+request.ID, nil)
	own.Header.Set("X-Mock-User-ID", "u1")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, own)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), request.ID) {
		t.Fatalf("owned detail status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	other := httptest.NewRequest(http.MethodGet, "/api/v1/user/invoice-requests/"+request.ID, nil)
	other.Header.Set("X-Mock-User-ID", "u2")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, other)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-user detail status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/api/v1/user/invoice-requests?limit=1", nil)
	list.Header.Set("X-Mock-User-ID", "u1")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, list)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"has_more":false`) {
		t.Fatalf("list pagination contract status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequestIDIsSanitizedAndReturned(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "unsafe request id")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	got := recorder.Header().Get("X-Request-ID")
	if got == "" || got == "unsafe request id" || !validExternalRequestID(got) {
		t.Fatalf("request ID was not replaced safely: %q", got)
	}
}

func TestAdminEndpointsRequireAdminRole(t *testing.T) {
	server, _ := testServer(t)
	for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/api/v1/admin/invoice-requests"}, {http.MethodGet, "/api/v1/admin/eligibility-freezes"}, {http.MethodPost, "/api/v1/admin/eligibility-freezes/61000000-0000-4000-8000-000000000001/resolve"}, {http.MethodGet, "/api/v1/admin/accounts/ledger"}, {http.MethodGet, "/api/v1/admin/accounts/61000000-0000-4000-8000-000000000001/ledger"}} {
		request := httptest.NewRequest(endpoint.method, endpoint.path, nil)
		request.Header.Set("X-Mock-User-ID", "u1")
		request.Header.Set("X-Mock-Role", "user")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d", endpoint.method, endpoint.path, recorder.Code)
		}
	}
}

func TestAdminIPAllowlistDoesNotTrustSpoofedForwardingHeader(t *testing.T) {
	_, service := testServer(t)
	server, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}, TrustedProxies: []string{"173.245.48.10/32"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invoice-requests", nil)
	request.RemoteAddr = "198.51.100.22:443"
	request.Header.Set("CF-Connecting-IP", "203.0.113.9")
	request.Header.Set("X-Mock-User-ID", "admin-1")
	request.Header.Set("X-Mock-Role", "admin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed forwarding header status=%d", recorder.Code)
	}
}

func TestAdminIPAllowlistAcceptsHeaderOnlyFromTrustedProxy(t *testing.T) {
	_, service := testServer(t)
	server, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}, TrustedProxies: []string{"173.245.48.10/32"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invoice-requests", nil)
	request.RemoteAddr = "173.245.48.10:443"
	request.Header.Set("CF-Connecting-IP", "203.0.113.9")
	request.Header.Set("X-Mock-User-ID", "admin-1")
	request.Header.Set("X-Mock-Role", "admin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("trusted proxy status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTrustedProxyAllowlistRejectsWorldOpenNetwork(t *testing.T) {
	_, service := testServer(t)
	for _, cidr := range []string{"0.0.0.0/0", "::/0", "224.0.0.0/4", "169.254.0.0/16", "10.20.0.0/24", "2001:db8::/64"} {
		if _, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}, TrustedProxies: []string{cidr}}, nil); err == nil {
			t.Errorf("trusted proxy CIDR %s accepted", cidr)
		}
	}
	if _, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}, TrustedProxies: []string{"192.0.2.1/32", "192.0.2.2/32"}}, nil); err == nil {
		t.Error("multiple trusted proxy hosts accepted")
	}
}

func TestDynamicAdminAllowlistUpdatesWithoutRemovingBootstrapAccess(t *testing.T) {
	_, service := testServer(t)
	server, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}, BreakGlassCIDRs: []string{"127.0.0.1/32"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = server.ReplaceAdminIPAllowlist([]string{"198.51.100.8/32"}); err != nil {
		t.Fatal(err)
	}
	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invoice-requests", nil)
	allowed.RemoteAddr = "198.51.100.8:443"
	allowed.Header.Set("X-Mock-User-ID", "admin-1")
	allowed.Header.Set("X-Mock-Role", "admin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, allowed)
	if recorder.Code != http.StatusOK {
		t.Fatalf("dynamic IP status=%d", recorder.Code)
	}
	bootstrap := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invoice-requests", nil)
	bootstrap.RemoteAddr = "127.0.0.1:443"
	bootstrap.Header.Set("X-Mock-User-ID", "admin-1")
	bootstrap.Header.Set("X-Mock-Role", "admin")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, bootstrap)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bootstrap IP status=%d", recorder.Code)
	}
	if err = server.ReplaceAdminIPAllowlist(nil); err == nil {
		t.Fatal("empty dynamic allowlist accepted")
	}
}

func TestSecurityHeadersAreAlwaysPresent(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	for name := range map[string]struct{}{"Content-Security-Policy": {}, "Permissions-Policy": {}, "Referrer-Policy": {}, "X-Content-Type-Options": {}} {
		if recorder.Header().Get(name) == "" {
			t.Errorf("missing %s", name)
		}
	}
}

func TestNotReadyStillServesHealthAndProtectedAdminConfiguration(t *testing.T) {
	service := ledger.NewService()
	now := time.Now().UTC()
	repo := adminsettings.NewMemoryRepository(adminsettings.Settings{
		IssuerName: adminsettings.UnconfiguredIssuerName, ServiceItem: adminsettings.FixedServiceItem,
		MinimumRequestMinor: adminsettings.MinimumMinor, SMTPHost: "smtp.qq.com", SMTPPort: 587,
		SMTPFrom: "invoice@qq.com", SMTPFromName: "发票中心", SMTPStartTLS: true,
		AdminCIDRs: []string{"203.0.113.8/32"}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	server, err := NewWithConfig(service, Config{
		AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.8/32"},
		AdminSettings: adminsettings.NewService(repo, httpSettingsBox{}),
		Readiness:     func(context.Context) error { return errors.New("issuer not configured") },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != want {
			t.Errorf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodGet, "/api/v1/admin/settings", "203.0.113.8", ""))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"issuer_configured":false`) {
		t.Fatalf("admin setup route status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTypedAdminSettingsGetDoesNotExposeSecretOrBreakGlassCIDRs(t *testing.T) {
	server, _ := settingsServer(t, []string{"203.0.113.8/32"}, []string{"127.0.0.1/32"})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodGet, "/api/v1/admin/settings", "203.0.113.8", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Revision            int64  `json:"revision"`
		IssuerName          string `json:"issuer_name"`
		IssuerConfigured    bool   `json:"issuer_configured"`
		ServiceItem         string `json:"service_item"`
		EligibilityStartAt  string `json:"eligibility_start_at"`
		EligibilityVersion  int64  `json:"eligibility_policy_version"`
		EligibilityTimezone string `json:"eligibility_timezone"`
		EligibilityRule     string `json:"eligibility_rule"`
		SMTP                struct {
			FromAddress          string `json:"from_address"`
			CredentialConfigured bool   `json:"credential_configured"`
			AuthorizationCode    string `json:"authorization_code"`
		} `json:"smtp"`
		AdminAccess struct {
			CIDRs           []string `json:"cidrs"`
			CurrentIP       string   `json:"current_ip"`
			BootstrapAccess bool     `json:"bootstrap_access"`
		} `json:"admin_access"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Revision != 1 || response.IssuerConfigured || response.ServiceItem != adminsettings.FixedServiceItem ||
		response.EligibilityStartAt != "2026-08-31T16:00:00Z" || response.EligibilityTimezone != "Asia/Shanghai" ||
		response.EligibilityVersion != 1 || response.EligibilityRule != "payment_and_usage_at_or_after" ||
		response.SMTP.FromAddress != "invoice@qq.com" ||
		response.SMTP.AuthorizationCode != "" || response.AdminAccess.CurrentIP != "203.0.113.8" || response.AdminAccess.BootstrapAccess {
		t.Fatalf("unexpected response: %+v", response)
	}
	if strings.Contains(recorder.Body.String(), "127.0.0.1") || strings.Contains(recorder.Body.String(), "authorization_code") {
		t.Fatalf("sensitive field leaked: %s", recorder.Body.String())
	}
}

func TestTypedAdminSettingsIssuerConfiguredUsesSharedPlaceholderRule(t *testing.T) {
	for name, fixture := range map[string]struct {
		issuer     string
		configured bool
	}{
		"exact placeholder":      {issuer: "待配置开票主体", configured: false},
		"production placeholder": {issuer: "待配置实际开票主体（上线前必须修改）", configured: false},
		"legacy placeholder":     {issuer: "请替换为实际开票主体全称", configured: false},
		"real issuer":            {issuer: "示例科技有限公司", configured: true},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := settingsServerWithIssuer(t, fixture.issuer, []string{"203.0.113.8/32"}, nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, adminRequest(http.MethodGet, "/api/v1/admin/settings", "203.0.113.8", ""))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				IssuerConfigured bool `json:"issuer_configured"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.IssuerConfigured != fixture.configured {
				t.Fatalf("issuer_configured=%t want %t", response.IssuerConfigured, fixture.configured)
			}
		})
	}
}

func TestTypedInvoiceSettingsRejectsReservedIssuerPlaceholder(t *testing.T) {
	server, service := settingsServer(t, []string{"203.0.113.8/32"}, nil)
	for _, issuer := range []string{"", "待配置开票主体", "待配置实际开票主体（上线前必须修改）", "请替换为实际开票主体全称"} {
		body, err := json.Marshal(map[string]any{
			"revision": 1, "issuer_name": issuer, "minimum_request_minor": 20_000,
		})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/invoice", "203.0.113.8", string(body)))
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("issuer=%q status=%d body=%s", issuer, recorder.Code, recorder.Body.String())
		}
	}
	if service.MinimumRequestMinor() != adminsettings.MinimumMinor {
		t.Fatalf("failed updates changed ledger minimum to %d", service.MinimumRequestMinor())
	}
}

func TestTypedInvoiceSettingsCASUpdatesLedgerMinimum(t *testing.T) {
	server, service := settingsServer(t, []string{"203.0.113.8/32"}, nil)
	body := `{"revision":1,"issuer_name":"示例科技有限公司","minimum_request_minor":30000}`
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/invoice", "203.0.113.8", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.MinimumRequestMinor() != 30_000 {
		t.Fatalf("ledger minimum=%d", service.MinimumRequestMinor())
	}
	policyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/user/invoice-policy", nil)
	policyRequest.Header.Set("X-Mock-User-ID", "user-1")
	policyRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(policyRecorder, policyRequest)
	if policyRecorder.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", policyRecorder.Code, policyRecorder.Body.String())
	}
	var policy struct {
		MinimumRequestMinor int64  `json:"minimum_request_minor"`
		ServiceItem         string `json:"service_item"`
		EligibilityStartAt  string `json:"eligibility_start_at"`
		EligibilityVersion  int64  `json:"eligibility_policy_version"`
		EligibilityTimezone string `json:"eligibility_timezone"`
		EligibilityRule     string `json:"eligibility_rule"`
	}
	if err := json.Unmarshal(policyRecorder.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.MinimumRequestMinor != 30_000 || policy.ServiceItem != domain.FixedServiceItem ||
		policy.EligibilityStartAt != "2026-08-31T16:00:00Z" || policy.EligibilityTimezone != "Asia/Shanghai" ||
		policy.EligibilityVersion != 1 || policy.EligibilityRule != "payment_and_usage_at_or_after" ||
		strings.Contains(policyRecorder.Body.String(), "issuer") {
		t.Fatalf("unexpected public invoice policy: %s", policyRecorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/invoice", "203.0.113.8", body))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale revision status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTypedSMTPSettingsSecretIsWriteOnlyAndTestMailFailsClosed(t *testing.T) {
	server, _ := settingsServer(t, []string{"203.0.113.8/32"}, nil)
	server.smtpTestRecipient = "test-recipient@example.com"
	body := `{"revision":1,"host":"smtp.qq.com","port":587,"from_address":"billing@qq.com","from_name":"发票中心","starttls":true,"authorization_code":"qq-secret-code"}`
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/smtp", "203.0.113.8", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "qq-secret-code") || strings.Contains(recorder.Body.String(), "authorization_code") {
		t.Fatalf("secret echoed: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"credential_configured":true`) || !strings.Contains(recorder.Body.String(), `"from_address":"billing@qq.com"`) {
		t.Fatalf("secret flag missing: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"test_recipient_masked":"tes***@example.com"`) || strings.Contains(recorder.Body.String(), "test-recipient@example.com") {
		t.Fatalf("test recipient was not safely masked: %s", recorder.Body.String())
	}
	var response struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Revision != 2 {
		t.Fatalf("SMTP update consumed %d revisions, want 1", response.Revision-1)
	}
	testBody := `{"recipient":"attacker@example.com"}`
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPost, "/api/v1/admin/settings/smtp/test", "203.0.113.8", testBody))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("test email status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSMTPSettingsRejectSenderEqualToFixedTestRecipient(t *testing.T) {
	server, _ := settingsServer(t, []string{"203.0.113.8/32"}, nil)
	server.smtpTestRecipient = "test-recipient@example.com"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/smtp", "203.0.113.8", `{"revision":1,"host":"smtp.qq.com","port":587,"from_address":"test-recipient@example.com","from_name":"发票中心","starttls":true,"authorization_code":"qq-secret-code"}`))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "SMTP_TEST_RECIPIENT_CONFLICT") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSMTPTestFailureLogsOnlySafeStage(t *testing.T) {
	const sensitiveMarker = "provider-response-sensitive-marker"
	var logs bytes.Buffer
	server := &Server{
		logger:            slog.New(slog.NewTextHandler(&logs, nil)),
		smtpTestSender:    failingSMTPTestSender{err: errors.New(sensitiveMarker)},
		smtpTestRecipient: "test-recipient@example.com",
		adminSettings:     smtpTestSettings("sender@example.com"),
		productionAuth: &ProductionAuth{LoadUser: func(context.Context, string) (SessionUser, error) {
			return SessionUser{Email: "private-admin@example.com", EmailVerified: true}, nil
		}},
		publicOrigin: "https://invoice.example",
		lastSMTPTest: make(map[string]time.Time),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/smtp/test", strings.NewReader(`{}`))
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: "admin-user"}))
	recorder := httptest.NewRecorder()
	server.testEmail(recorder, request)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "SMTP_TEST_FAILED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	logText := logs.String()
	if !strings.Contains(logText, "failure_stage=other") || strings.Contains(logText, sensitiveMarker) || strings.Contains(logText, "private-admin@example.com") || strings.Contains(logText, "test-recipient@example.com") {
		t.Fatalf("unsafe SMTP failure log: %s", logText)
	}
}

func TestSMTPTestUsesFixedIndependentRecipientAndRejectsOverride(t *testing.T) {
	newServer := func(sender mailer.Sender) *Server {
		return &Server{
			logger:            slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			smtpTestSender:    sender,
			smtpTestRecipient: "test-recipient@example.com",
			adminSettings:     smtpTestSettings("sender@example.com"),
			productionAuth: &ProductionAuth{LoadUser: func(context.Context, string) (SessionUser, error) {
				return SessionUser{Email: "admin@example.com", EmailVerified: true}, nil
			}},
			publicOrigin: "https://invoice.example",
			lastSMTPTest: make(map[string]time.Time),
			operations:   smtpAuditOperations{},
		}
	}

	capture := &capturingSMTPTestSender{}
	server := newServer(capture)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/smtp/test", strings.NewReader(`{}`))
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: "admin-user"}))
	recorder := httptest.NewRecorder()
	server.testEmail(recorder, request)
	if recorder.Code != http.StatusOK || capture.calls != 1 || capture.message.Recipient != "test-recipient@example.com" || capture.message.Kind != mailer.MessageSMTPTest {
		t.Fatalf("status=%d calls=%d message=%+v", recorder.Code, capture.calls, capture.message)
	}

	for _, invalidBody := range []string{`{"recipient":"attacker@example.com"}`, `null`, `[]`, `"text"`} {
		capture = &capturingSMTPTestSender{}
		server = newServer(capture)
		request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/smtp/test", strings.NewReader(invalidBody))
		request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: "admin-user"}))
		recorder = httptest.NewRecorder()
		server.testEmail(recorder, request)
		if recorder.Code != http.StatusBadRequest || capture.calls != 0 {
			t.Fatalf("body=%s status=%d calls=%d response=%s", invalidBody, recorder.Code, capture.calls, recorder.Body.String())
		}
	}
}

func TestSMTPTestRecipientValidationMaskingAndSenderConflict(t *testing.T) {
	for _, value := range []string{"Display <test-recipient@example.com>", "test-recipient@example.com\r\nBcc:x@example.com", "not-an-email", "测试@example.com"} {
		if _, err := normalizeSMTPTestRecipient(value); err == nil {
			t.Fatalf("unsafe SMTP test recipient accepted: %q", value)
		}
	}
	if value, err := normalizeSMTPTestRecipient("test-recipient@example.com"); err != nil || value != "test-recipient@example.com" {
		t.Fatalf("recipient=%q err=%v", value, err)
	}
	if masked := maskSMTPTestRecipient("test-recipient@example.com"); masked != "tes***@example.com" {
		t.Fatalf("masked recipient=%q", masked)
	}

	server := &Server{
		smtpTestSender:    &capturingSMTPTestSender{},
		smtpTestRecipient: "test-recipient@example.com",
		adminSettings:     smtpTestSettings("test-recipient@example.com"),
		productionAuth: &ProductionAuth{LoadUser: func(context.Context, string) (SessionUser, error) {
			return SessionUser{Email: "admin@example.com", EmailVerified: true}, nil
		}},
		publicOrigin: "https://invoice.example", lastSMTPTest: make(map[string]time.Time), logger: slog.Default(),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/smtp/test", strings.NewReader(`{}`))
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: "admin-user"}))
	recorder := httptest.NewRecorder()
	server.testEmail(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "SMTP_TEST_RECIPIENT_CONFLICT") {
		t.Fatalf("conflict status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func smtpTestSettings(from string) *adminsettings.Service {
	now := time.Now().UTC()
	repo := adminsettings.NewMemoryRepository(adminsettings.Settings{
		IssuerName: "示例科技有限公司", ServiceItem: adminsettings.FixedServiceItem,
		MinimumRequestMinor: adminsettings.MinimumMinor, EligibilityStartAt: adminsettings.RequiredEligibilityStartAt,
		SMTPHost: "smtp.qq.com", SMTPPort: 587, SMTPFrom: from, SMTPFromName: "发票中心", SMTPStartTLS: true,
		AdminCIDRs: []string{"203.0.113.8/32"}, Revision: 1, UpdatedBy: "test", CreatedAt: now, UpdatedAt: now,
	})
	return adminsettings.NewService(repo, httpSettingsBox{})
}

type failingSMTPTestSender struct{ err error }

func (s failingSMTPTestSender) SendInvoiceReady(context.Context, mailer.Message) (string, error) {
	return "", s.err
}

type capturingSMTPTestSender struct {
	calls   int
	message mailer.Message
}

type smtpAuditOperations struct{ OperationsService }

func (smtpAuditOperations) RecordAdminAudit(context.Context, string, string, string, string, string) error {
	return nil
}

func (s *capturingSMTPTestSender) SendInvoiceReady(_ context.Context, message mailer.Message) (string, error) {
	s.calls++
	s.message = message
	return "smtp:test", nil
}

func TestLegacyIssuerPlaceholderDoesNotBlockSMTPOrAdminAccessSetup(t *testing.T) {
	server, _ := settingsServerWithIssuer(t, "请替换为实际开票主体全称", []string{"203.0.113.8/32"}, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/smtp", "203.0.113.8", `{"revision":1,"host":"smtp.qq.com","port":587,"from_address":"billing@qq.com","from_name":"发票中心","starttls":true,"authorization_code":"qq-secret-code"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy SMTP update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/admin-access", "203.0.113.8", `{"revision":2,"cidrs":["203.0.113.8","198.51.100.9"]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy admin access update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		IssuerName       string `json:"issuer_name"`
		IssuerConfigured bool   `json:"issuer_configured"`
		Revision         int64  `json:"revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.IssuerName != "请替换为实际开票主体全称" || response.IssuerConfigured || response.Revision != 3 {
		t.Fatalf("legacy placeholder lifecycle response=%+v", response)
	}
}

func TestAdminAccessRequiresCurrentIPUnlessUsingBreakGlassAndRefreshes(t *testing.T) {
	server, _ := settingsServer(t, []string{"203.0.113.8/32"}, []string{"127.0.0.1/32"})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/admin-access", "203.0.113.8", `{"revision":1,"cidrs":["198.51.100.9"]}`))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lockout status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/admin-access", "203.0.113.8", `{"revision":1,"cidrs":["203.0.113.8","198.51.100.9"]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "203.0.113.8/32") {
		t.Fatalf("bare IP not normalized: %s", recorder.Body.String())
	}
	check := httptest.NewRecorder()
	server.Handler().ServeHTTP(check, adminRequest(http.MethodGet, "/api/v1/admin/settings", "198.51.100.9", ""))
	if check.Code != http.StatusOK {
		t.Fatalf("dynamic refresh status=%d", check.Code)
	}
	// Deployment-only break-glass access survives dynamic replacement and may recover a broken list.
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/admin-access", "127.0.0.1", `{"revision":2,"cidrs":["192.0.2.44"]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("break-glass update status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "127.0.0.1/32") {
		t.Fatalf("break-glass CIDR leaked: %s", recorder.Body.String())
	}
}

func TestSettingsSurfaceHasNoLegacyUnifiedRouteAndRejectsTrailingJSON(t *testing.T) {
	server, _ := settingsServer(t, []string{"203.0.113.8/32"}, nil)
	for _, path := range []string{"/api/v1/admin/settings", "/api/v1/admin/settings/smtp-secret", "/api/v1/admin/settings/test-email"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, path, "203.0.113.8", `{}`))
		if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusNotFound {
			t.Errorf("legacy %s status=%d", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	body := `{"revision":1,"issuer_name":"A","minimum_request_minor":20000} {"revision":1}`
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/invoice", "203.0.113.8", body))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	body = `{"revision":1,"issuer_name":"A","minimum_request_minor":20000,"eligibility_start_at":"2026-09-01T00:00:00+08:00"}`
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodPut, "/api/v1/admin/settings/invoice", "203.0.113.8", body))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("mutable eligibility policy field status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSubmitRejectsAmountBelowTwoHundredYuan(t *testing.T) {
	server, service := testServer(t)
	profile, err := service.SaveProfile(context.Background(), domain.InvoiceProfile{PrincipalID: "u1", Type: domain.ProfilePersonal, Title: "张三", Email: "z@example.com", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"profile_id": profile.ID, "source_instance_id": "sub2-main", "idempotency_key": "small", "allocations": []map[string]any{{"funding_lot_id": "u1-lot", "amount_minor": 19_999}}}
	raw, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/user/invoice-requests", bytes.NewReader(raw))
	request.Header.Set("X-Mock-User-ID", "u1")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	payload["idempotency_key"] = "valid-hidden-issuer"
	payload["allocations"] = []map[string]any{{"funding_lot_id": "u1-lot", "amount_minor": 20_000}}
	raw, _ = json.Marshal(payload)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/user/invoice-requests", bytes.NewReader(raw))
	request.Header.Set("X-Mock-User-ID", "u1")
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("valid status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "issuer_code") || strings.Contains(recorder.Body.String(), `"issuer`) {
		t.Fatalf("user submission leaked issuer data: %s", recorder.Body.String())
	}
}

func TestPDFUploadAndDownloadRequireStateAndOwnership(t *testing.T) {
	_, service := testServer(t)
	profile, err := service.SaveProfile(context.Background(), domain.InvoiceProfile{PrincipalID: "u1", Type: domain.ProfilePersonal, Title: "张三", Email: "z@example.com", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	request, err := service.Submit(context.Background(), ledger.SubmitInput{PrincipalID: "u1", ProfileID: profile.ID, SourceInstanceID: "sub2-main", IdempotencyKey: "pdf", Allocations: []ledger.AllocationInput{{FundingLotID: "u1-lot", AmountMinor: 20_000}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	request, err = service.Review(context.Background(), "admin-1", request.ID, "approve", "ok", request.Version)
	if err != nil {
		t.Fatal(err)
	}
	request, err = service.ConfirmManualIssue(context.Background(), "admin-1", request.ID, request.Version)
	if err != nil {
		t.Fatal(err)
	}
	store := &document.LocalStore{Root: t.TempDir(), Scanner: document.ScannerFunc(func(context.Context, string) error { return nil })}
	server, err := NewWithConfig(service, Config{AuthMode: "mock", AdminIPAllowlist: []string{"127.0.0.1/32"}, DocumentStore: store}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("version", strconv.FormatInt(request.Version, 10))
	_ = writer.WriteField("invoice_number", "FP-2026-0001")
	_ = writer.WriteField("issued_at", time.Now().UTC().Format(time.RFC3339))
	part, err := writer.CreateFormFile("file", "invoice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("%PDF-1.7\nmock invoice\n%%EOF"))
	_ = writer.Close()
	upload := httptest.NewRequest(http.MethodPost, "/api/v1/admin/invoice-requests/"+request.ID+"/documents/upload", &body)
	upload.RemoteAddr = "127.0.0.1:4411"
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	upload.Header.Set("X-Mock-User-ID", "admin-1")
	upload.Header.Set("X-Mock-Role", "admin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, upload)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	download := httptest.NewRequest(http.MethodGet, "/api/v1/user/invoice-requests/"+request.ID+"/document", nil)
	download.Header.Set("X-Mock-User-ID", "u1")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, download)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("download status=%d headers=%v", recorder.Code, recorder.Header())
	}
	other := httptest.NewRequest(http.MethodGet, "/api/v1/user/invoice-requests/"+request.ID+"/document", nil)
	other.Header.Set("X-Mock-User-ID", "u2")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, other)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-user download status=%d", recorder.Code)
	}
	adminDownload := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invoice-requests/"+request.ID+"/document", nil)
	adminDownload.RemoteAddr = "127.0.0.1:4412"
	adminDownload.Header.Set("X-Mock-User-ID", "admin-2")
	adminDownload.Header.Set("X-Mock-Role", "admin")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminDownload)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/pdf" || recorder.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("admin download status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestUserCancelReleasesPendingAndReturnedReservations(t *testing.T) {
	server, service := testServer(t)
	profile, err := service.SaveProfile(context.Background(), domain.InvoiceProfile{
		PrincipalID: "u1", Type: domain.ProfilePersonal, Title: "张三",
		Email: "z@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, returned := range []bool{false, true} {
		name := "pending"
		if returned {
			name = "needs_changes"
		}
		t.Run(name, func(t *testing.T) {
			request, submitErr := service.Submit(context.Background(), ledger.SubmitInput{
				PrincipalID: "u1", ProfileID: profile.ID, SourceInstanceID: "sub2-main",
				IdempotencyKey: "cancel-" + name,
				Allocations:    []ledger.AllocationInput{{FundingLotID: "u1-lot", AmountMinor: 20_000}},
			}, "")
			if submitErr != nil {
				t.Fatal(submitErr)
			}
			if returned {
				request, submitErr = service.Review(context.Background(), "admin-1", request.ID, "return", "请修改抬头", request.Version)
				if submitErr != nil {
					t.Fatal(submitErr)
				}
			}
			body, _ := json.Marshal(map[string]any{"version": request.Version})
			cancel := httptest.NewRequest(http.MethodPost, "/api/v1/user/invoice-requests/"+request.ID+"/cancel", bytes.NewReader(body))
			cancel.Header.Set("Content-Type", "application/json")
			cancel.Header.Set("X-Mock-User-ID", "u1")
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, cancel)
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"user_cancelled"`) {
				t.Fatalf("cancel status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			lots, listErr := service.ListFundingLots(context.Background(), "u1", "")
			if listErr != nil {
				t.Fatal(listErr)
			}
			for _, lot := range lots {
				if lot.ID == "u1-lot" && lot.ReservedMinor != 0 {
					t.Fatalf("reservation remained after cancellation: %+v", lot)
				}
			}
		})
	}
}
