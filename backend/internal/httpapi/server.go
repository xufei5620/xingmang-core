package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/mailer"
)

type identity struct {
	UserID       string
	Role         string
	Session      *auth.Session
	SessionToken string
	CSRFToken    string
}
type contextKey string

const identityKey contextKey = "identity"

type Server struct {
	ledger             InvoiceService
	authMode           string
	logger             *slog.Logger
	mux                *http.ServeMux
	adminNetworks      []*net.IPNet
	breakGlassNetworks []*net.IPNet
	trustedProxies     []*net.IPNet
	documentStore      document.Store
	adminNetworkMu     sync.RWMutex
	adminSettings      *adminsettings.Service
	productionAuth     *ProductionAuth
	operations         OperationsService
	sourceMode         string
	readiness          func(context.Context) error
	smtpTestSender     mailer.Sender
	publicOrigin       string
	smtpTestMu         sync.Mutex
	lastSMTPTest       map[string]time.Time
	sourceIngest       http.Handler
}

type Config struct {
	AuthMode         string
	AdminIPAllowlist []string
	// BreakGlassCIDRs are deployment-only recovery networks. They are never
	// returned by the settings API and cannot be changed by an API request.
	BreakGlassCIDRs []string
	TrustedProxies  []string
	DocumentStore   document.Store
	AdminSettings   *adminsettings.Service
	ProductionAuth  *ProductionAuth
	SourceMode      string
	Readiness       func(context.Context) error
	SMTPTestSender  mailer.Sender
	PublicOrigin    string
	SourceIngest    http.Handler
}

func New(service InvoiceService, authMode string, logger *slog.Logger) *Server {
	server, err := NewWithConfig(service, Config{AuthMode: authMode, AdminIPAllowlist: []string{"127.0.0.1/32", "::1/128"}, BreakGlassCIDRs: []string{"127.0.0.1/32", "::1/128"}}, logger)
	if err != nil {
		panic(err)
	}
	return server
}

func NewWithConfig(service InvoiceService, cfg Config, logger *slog.Logger) (*Server, error) {
	if service == nil {
		return nil, errors.New("invoice service is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	adminNetworks, err := parseAdminNetworks(cfg.AdminIPAllowlist)
	if err != nil {
		return nil, fmt.Errorf("admin IP allowlist: %w", err)
	}
	if len(adminNetworks) == 0 {
		return nil, errors.New("admin IP allowlist cannot be empty")
	}
	breakGlassNetworks, err := parseAdminNetworks(cfg.BreakGlassCIDRs)
	if err != nil {
		return nil, fmt.Errorf("break-glass admin IP allowlist: %w", err)
	}
	trustedProxies, err := parseTrustedProxyNetworks(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("trusted proxies: %w", err)
	}
	switch cfg.AuthMode {
	case "mock":
		if cfg.ProductionAuth != nil {
			return nil, errors.New("production authentication cannot be enabled in mock mode")
		}
	case "oidc":
		if cfg.ProductionAuth == nil {
			return nil, errors.New("OIDC authentication runtime is required")
		}
		if err := cfg.ProductionAuth.Validate(); err != nil {
			return nil, fmt.Errorf("OIDC authentication runtime: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", cfg.AuthMode)
	}
	operations, _ := service.(OperationsService)
	sourceMode := strings.TrimSpace(cfg.SourceMode)
	if sourceMode == "" {
		sourceMode = "mock"
	}
	if sourceMode == "agent" && cfg.SourceIngest == nil {
		return nil, errors.New("source ingestion handler is required in agent mode")
	}
	s := &Server{ledger: service, authMode: cfg.AuthMode, logger: logger, mux: http.NewServeMux(), adminNetworks: adminNetworks, breakGlassNetworks: breakGlassNetworks, trustedProxies: trustedProxies, documentStore: cfg.DocumentStore, adminSettings: cfg.AdminSettings, productionAuth: cfg.ProductionAuth, operations: operations, sourceMode: sourceMode, readiness: cfg.Readiness, smtpTestSender: cfg.SMTPTestSender, publicOrigin: strings.TrimRight(cfg.PublicOrigin, "/"), lastSMTPTest: make(map[string]time.Time), sourceIngest: cfg.SourceIngest}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.securityHeaders(s.requestMetadata(s.mux)) }

func (s *Server) routes() {
	if s.sourceIngest != nil {
		s.mux.Handle("POST /internal/v1/source-batches", s.sourceIngest)
	}
	if s.productionAuth != nil {
		s.productionAuth.Register(s)
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "source_mode": s.sourceMode, "auth_mode": s.authMode})
	})
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.readiness != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if err := s.readiness(ctx); err != nil {
				writeError(w, http.StatusServiceUnavailable, "NOT_READY", "required dependencies are unavailable")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	})
	s.mux.Handle("GET /api/v1/user/funding-lots", s.require("user", http.HandlerFunc(s.listLots)))
	s.mux.Handle("GET /api/v1/user/invoice-policy", s.require("user", http.HandlerFunc(s.getInvoicePolicy)))
	s.mux.Handle("GET /api/v1/user/source-accounts", s.require("user", http.HandlerFunc(s.listSourceAccounts)))
	s.mux.Handle("GET /api/v1/user/eligibility-summary", s.require("user", http.HandlerFunc(s.listUserEligibilitySummary)))
	s.mux.Handle("GET /api/v1/user/profiles", s.require("user", http.HandlerFunc(s.listProfiles)))
	s.mux.Handle("POST /api/v1/user/profiles", s.require("user", http.HandlerFunc(s.saveProfile)))
	s.mux.Handle("GET /api/v1/user/invoice-requests", s.require("user", http.HandlerFunc(s.listUserRequests)))
	s.mux.Handle("GET /api/v1/user/invoice-requests/{id}", s.require("user", http.HandlerFunc(s.getUserRequest)))
	s.mux.Handle("GET /api/v1/user/invoice-requests/{id}/delivery", s.require("user", http.HandlerFunc(s.getUserDeliveryState)))
	s.mux.Handle("POST /api/v1/user/invoice-requests", s.require("user", http.HandlerFunc(s.submitRequest)))
	s.mux.Handle("POST /api/v1/user/invoice-requests/{id}/cancel", s.require("user", http.HandlerFunc(s.cancelRequest)))
	s.mux.Handle("GET /api/v1/admin/invoice-requests", s.require("admin", http.HandlerFunc(s.listAdminRequests)))
	s.mux.Handle("GET /api/v1/admin/invoice-requests/{id}", s.require("admin", http.HandlerFunc(s.getAdminRequest)))
	s.mux.Handle("GET /api/v1/admin/source-health", s.require("admin", http.HandlerFunc(s.getSourceHealth)))
	s.mux.Handle("GET /api/v1/admin/invoice-requests/{id}/delivery", s.require("admin", http.HandlerFunc(s.getAdminDeliveryState)))
	s.mux.Handle("GET /api/v1/admin/payment-candidates", s.require("admin", http.HandlerFunc(s.listPaymentCandidates)))
	s.mux.Handle("POST /api/v1/admin/funding-lots/{id}/verify-payment", s.require("admin", http.HandlerFunc(s.verifyNewAPIPayment)))
	s.mux.Handle("POST /api/v1/admin/funding-lots/{id}/reject-payment", s.require("admin", http.HandlerFunc(s.rejectNewAPIPayment)))
	s.mux.Handle("POST /api/v1/admin/funding-lots/{id}/freeze-payment", s.require("admin", http.HandlerFunc(s.freezeNewAPIPayment)))
	s.mux.Handle("POST /api/v1/admin/funding-lots/{id}/manual-cap-adjustment", s.require("admin", http.HandlerFunc(s.applyNewAPIManualCap)))
	s.mux.Handle("GET /api/v1/admin/refund-cases", s.require("admin", http.HandlerFunc(s.listRefundCases)))
	s.mux.Handle("GET /api/v1/admin/eligibility-freezes", s.require("admin", http.HandlerFunc(s.listEligibilityFreezes)))
	s.mux.Handle("POST /api/v1/admin/eligibility-freezes/{id}/resolve", s.require("admin", http.HandlerFunc(s.resolveEligibilityFreeze)))
	s.mux.Handle("POST /api/v1/admin/refund-cases/{id}/resolve", s.require("admin", http.HandlerFunc(s.resolveRefundCase)))
	s.mux.Handle("POST /api/v1/admin/invoice-requests/{id}/email/requeue", s.require("admin", http.HandlerFunc(s.requeueInvoiceEmail)))
	s.mux.Handle("POST /api/v1/admin/invoice-requests/{id}/review", s.require("admin", http.HandlerFunc(s.reviewRequest)))
	s.mux.Handle("POST /api/v1/admin/invoice-requests/{id}/begin-manual-issue", s.require("admin", http.HandlerFunc(s.beginManualIssue)))
	s.mux.Handle("POST /api/v1/admin/invoice-requests/{id}/confirm-manual-issue", s.require("admin", http.HandlerFunc(s.confirmManualIssue)))
	s.mux.Handle("POST /api/v1/admin/invoice-requests/{id}/documents/upload", s.require("admin", http.HandlerFunc(s.uploadDocument)))
	s.mux.Handle("GET /api/v1/user/invoice-requests/{id}/document", s.require("user", http.HandlerFunc(s.downloadDocument)))
	s.mux.Handle("GET /api/v1/admin/invoice-requests/{id}/document", s.require("admin", http.HandlerFunc(s.downloadAdminDocument)))
	s.mux.Handle("GET /api/v1/admin/settings", s.require("admin", http.HandlerFunc(s.getAdminSettings)))
	s.mux.Handle("PUT /api/v1/admin/settings/invoice", s.require("admin", http.HandlerFunc(s.updateInvoiceSettings)))
	s.mux.Handle("PUT /api/v1/admin/settings/smtp", s.require("admin", http.HandlerFunc(s.updateSMTPSettings)))
	s.mux.Handle("PUT /api/v1/admin/settings/admin-access", s.require("admin", http.HandlerFunc(s.updateAdminAccessSettings)))
	s.mux.Handle("POST /api/v1/admin/settings/smtp/test", s.require("admin", http.HandlerFunc(s.testEmail)))
}

func (s *Server) require(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.productionAuth != nil {
			s.productionAuth.Require(s, role, next).ServeHTTP(w, r)
			return
		}
		if s.authMode != "mock" {
			writeError(w, http.StatusServiceUnavailable, "AUTH_NOT_CONFIGURED", "OIDC authentication is not configured")
			return
		}
		userID := strings.TrimSpace(r.Header.Get("X-Mock-User-ID"))
		gotRole := strings.TrimSpace(r.Header.Get("X-Mock-Role"))
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing mock identity")
			return
		}
		if role == "admin" && gotRole != "admin" {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "administrator role required")
			return
		}
		if role == "admin" && !s.adminIPAllowed(r) {
			writeError(w, http.StatusForbidden, "ADMIN_NETWORK_DENIED", "administrator network is not allowed")
			return
		}
		if gotRole == "" {
			gotRole = "user"
		}
		ctx := context.WithValue(r.Context(), identityKey, identity{UserID: userID, Role: gotRole})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func parseNetworks(values []string) ([]*net.IPNet, error) {
	result := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "/") {
			if ip := net.ParseIP(value); ip != nil {
				if ip.To4() != nil {
					value += "/32"
				} else {
					value += "/128"
				}
			}
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", value)
		}
		result = append(result, network)
	}
	return result, nil
}

func parseAdminNetworks(values []string) ([]*net.IPNet, error) {
	networks, err := parseNetworks(values)
	if err != nil {
		return nil, err
	}
	if len(networks) > 16 {
		return nil, errors.New("at most 16 admin CIDRs are allowed")
	}
	for _, network := range networks {
		ones, bits := network.Mask.Size()
		if network.IP.IsUnspecified() || network.IP.IsMulticast() || network.IP.IsLinkLocalUnicast() || network.IP.IsLinkLocalMulticast() || (bits == 32 && ones < 24) || (bits == 128 && ones < 64) {
			return nil, fmt.Errorf("unsafe or overly broad admin CIDR %q", network.String())
		}
	}
	return networks, nil
}

func parseTrustedProxyNetworks(values []string) ([]*net.IPNet, error) {
	networks, err := parseNetworks(values)
	if err != nil {
		return nil, err
	}
	if len(networks) > 1 {
		return nil, errors.New("exactly one trusted proxy host CIDR is allowed")
	}
	for _, network := range networks {
		ones, bits := network.Mask.Size()
		if ones != bits || network.IP.IsUnspecified() || network.IP.IsMulticast() || network.IP.IsLinkLocalUnicast() || network.IP.IsLinkLocalMulticast() {
			return nil, fmt.Errorf("trusted proxy must be one exact /32 or /128 host: %q", network.String())
		}
	}
	return networks, nil
}

func containsIP(networks []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	return net.ParseIP(host)
}

func (s *Server) adminIPAllowed(r *http.Request) bool {
	remote := remoteIP(r)
	client := remote
	if containsIP(s.trustedProxies, remote) {
		if candidate := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); candidate != nil {
			client = candidate
		}
	}
	s.adminNetworkMu.RLock()
	defer s.adminNetworkMu.RUnlock()
	return containsIP(s.adminNetworks, client) || containsIP(s.breakGlassNetworks, client)
}

func (s *Server) ReplaceAdminIPAllowlist(values []string) error {
	networks, err := parseAdminNetworks(values)
	if err != nil {
		return err
	}
	if len(networks) == 0 {
		return errors.New("admin IP allowlist cannot be empty")
	}
	s.adminNetworkMu.Lock()
	s.adminNetworks = networks
	s.adminNetworkMu.Unlock()
	return nil
}

func (s *Server) requestClientIP(r *http.Request) net.IP {
	remote := remoteIP(r)
	if containsIP(s.trustedProxies, remote) {
		if candidate := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); candidate != nil {
			return candidate
		}
	}
	return remote
}

func (s *Server) allowlistContainsClient(values []string, r *http.Request) bool {
	networks, err := parseAdminNetworks(values)
	return err == nil && containsIP(networks, s.requestClientIP(r))
}

func (s *Server) requestUsesBootstrap(r *http.Request) bool {
	s.adminNetworkMu.RLock()
	defer s.adminNetworkMu.RUnlock()
	return containsIP(s.breakGlassNetworks, s.requestClientIP(r))
}

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); validExternalRequestID(value) {
		return value
	}
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func validExternalRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func (s *Server) settingsActor(r *http.Request) adminsettings.Actor {
	current := principal(r)
	sourceIPHash := ""
	if current.Session != nil {
		sourceIPHash = current.Session.ClientIPHash
	}
	return adminsettings.Actor{ID: current.UserID, RequestID: requestID(r), SourceIPHash: sourceIPHash, Reason: "admin settings update"}
}

func (s *Server) getAdminSettings(w http.ResponseWriter, r *http.Request) {
	if s.adminSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_DISABLED", "admin settings are not configured")
		return
	}
	settings, err := s.adminSettings.Get(r.Context())
	if err != nil {
		handleSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(settings, r))
}

func (s *Server) settingsResponse(settings adminsettings.Settings, r *http.Request) map[string]any {
	issuerName := strings.TrimSpace(settings.IssuerName)
	issuerConfigured := adminsettings.IsIssuerConfigured(issuerName)
	return map[string]any{
		"revision":                   settings.Revision,
		"issuer_name":                issuerName,
		"issuer_configured":          issuerConfigured,
		"service_item":               adminsettings.FixedServiceItem,
		"minimum_request_minor":      settings.MinimumRequestMinor,
		"eligibility_start_at":       settings.EligibilityStartAt.UTC().Format(time.RFC3339),
		"eligibility_policy_version": settings.EligibilityPolicyVersion,
		"eligibility_timezone":       adminsettings.EligibilityDisplayTimeZone,
		"eligibility_rule":           "payment_and_usage_at_or_after",
		"smtp":                       map[string]any{"host": settings.SMTPHost, "port": settings.SMTPPort, "from_address": settings.SMTPFrom, "from_name": settings.SMTPFromName, "starttls": settings.SMTPStartTLS, "credential_configured": settings.SMTPSecretConfigured},
		"admin_access":               map[string]any{"cidrs": settings.AdminCIDRs, "current_ip": s.requestClientIP(r).String(), "bootstrap_access": s.requestUsesBootstrap(r)},
	}
}

func (s *Server) currentSettings(w http.ResponseWriter, r *http.Request, revision int64) (adminsettings.Settings, bool) {
	if revision <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REVISION", "revision must be positive")
		return adminsettings.Settings{}, false
	}
	settings, err := s.adminSettings.Get(r.Context())
	if err != nil {
		handleSettingsError(w, err)
		return adminsettings.Settings{}, false
	}
	if settings.Revision != revision {
		handleSettingsError(w, adminsettings.ErrRevisionConflict)
		return adminsettings.Settings{}, false
	}
	return settings, true
}

func updateInputFromSettings(settings adminsettings.Settings) adminsettings.UpdateInput {
	return adminsettings.UpdateInput{IssuerName: settings.IssuerName, MinimumRequestMinor: settings.MinimumRequestMinor, EligibilityStartAt: settings.EligibilityStartAt, SMTPHost: settings.SMTPHost, SMTPPort: settings.SMTPPort, SMTPFrom: settings.SMTPFrom, SMTPFromName: settings.SMTPFromName, SMTPStartTLS: settings.SMTPStartTLS, AdminCIDRs: append([]string(nil), settings.AdminCIDRs...)}
}

func (s *Server) updateInvoiceSettings(w http.ResponseWriter, r *http.Request) {
	if s.adminSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_DISABLED", "admin settings are not configured")
		return
	}
	var body struct {
		Revision            int64  `json:"revision"`
		IssuerName          string `json:"issuer_name"`
		MinimumRequestMinor int64  `json:"minimum_request_minor"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	current, ok := s.currentSettings(w, r, body.Revision)
	if !ok {
		return
	}
	input := updateInputFromSettings(current)
	input.IssuerName = body.IssuerName
	input.MinimumRequestMinor = body.MinimumRequestMinor
	updated, err := s.adminSettings.UpdateInvoice(r.Context(), input, body.Revision, s.settingsActor(r))
	if err != nil {
		handleSettingsError(w, err)
		return
	}
	if err = s.ledger.SetMinimumRequestMinor(updated.MinimumRequestMinor); err != nil {
		writeError(w, http.StatusInternalServerError, "LEDGER_POLICY_REFRESH_FAILED", "settings saved but ledger policy refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(updated, r))
}

func (s *Server) updateSMTPSettings(w http.ResponseWriter, r *http.Request) {
	if s.adminSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_DISABLED", "admin settings are not configured")
		return
	}
	var body struct {
		Revision               int64   `json:"revision"`
		Host                   string  `json:"host"`
		Port                   int     `json:"port"`
		FromAddress            string  `json:"from_address"`
		FromName               string  `json:"from_name"`
		StartTLS               bool    `json:"starttls"`
		AuthorizationCode      *string `json:"authorization_code,omitempty"`
		ClearAuthorizationCode bool    `json:"clear_authorization_code,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.AuthorizationCode != nil && body.ClearAuthorizationCode {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SMTP_SECRET_ACTION", "authorization_code and clear_authorization_code cannot be used together")
		return
	}
	if body.AuthorizationCode != nil && strings.TrimSpace(*body.AuthorizationCode) == "" {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SMTP_SECRET_ACTION", "authorization_code cannot be empty")
		return
	}
	current, ok := s.currentSettings(w, r, body.Revision)
	if !ok {
		return
	}
	input := updateInputFromSettings(current)
	input.SMTPHost = body.Host
	input.SMTPPort = body.Port
	input.SMTPFrom = body.FromAddress
	input.SMTPFromName = body.FromName
	input.SMTPStartTLS = body.StartTLS
	actor := s.settingsActor(r)
	change := adminsettings.SMTPSecretUnchanged
	authorizationCode := ""
	if body.AuthorizationCode != nil {
		change = adminsettings.SMTPSecretSet
		authorizationCode = *body.AuthorizationCode
	} else if body.ClearAuthorizationCode {
		change = adminsettings.SMTPSecretClear
	}
	updated, err := s.adminSettings.UpdateSMTP(r.Context(), input, change, authorizationCode, body.Revision, actor)
	if err != nil {
		handleSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(updated, r))
}

func (s *Server) updateAdminAccessSettings(w http.ResponseWriter, r *http.Request) {
	if s.adminSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_DISABLED", "admin settings are not configured")
		return
	}
	var body struct {
		Revision int64    `json:"revision"`
		CIDRs    []string `json:"cidrs"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !s.requestUsesBootstrap(r) && !s.allowlistContainsClient(body.CIDRs, r) {
		writeError(w, http.StatusUnprocessableEntity, "CURRENT_IP_REQUIRED", "new allowlist must keep the current administrator IP")
		return
	}
	current, ok := s.currentSettings(w, r, body.Revision)
	if !ok {
		return
	}
	input := updateInputFromSettings(current)
	input.AdminCIDRs = body.CIDRs
	updated, err := s.adminSettings.Update(r.Context(), input, body.Revision, s.settingsActor(r))
	if err != nil {
		handleSettingsError(w, err)
		return
	}
	if err = s.ReplaceAdminIPAllowlist(updated.AdminCIDRs); err != nil {
		writeError(w, http.StatusInternalServerError, "IP_POLICY_REFRESH_FAILED", "settings saved but IP policy refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(updated, r))
}

func (s *Server) testEmail(w http.ResponseWriter, r *http.Request) {
	if s.smtpTestSender == nil || s.productionAuth == nil || s.publicOrigin == "" {
		writeError(w, http.StatusServiceUnavailable, "TEST_EMAIL_NOT_CONNECTED", "test email delivery requires production authentication and SMTP")
		return
	}
	adminID := principal(r).UserID
	user, err := s.productionAuth.LoadUser(r.Context(), adminID)
	if err != nil || !user.EmailVerified || strings.TrimSpace(user.Email) == "" {
		writeError(w, http.StatusUnprocessableEntity, "ADMIN_EMAIL_NOT_VERIFIED", "current administrator email must be verified")
		return
	}
	now := time.Now().UTC()
	s.smtpTestMu.Lock()
	last := s.lastSMTPTest[adminID]
	if now.Sub(last) < time.Minute {
		s.smtpTestMu.Unlock()
		if s.operations != nil {
			_ = s.operations.RecordAdminAudit(r.Context(), adminID, "smtp_test", "smtp", requestID(r), "rate_limited")
		}
		writeError(w, http.StatusTooManyRequests, "SMTP_TEST_RATE_LIMITED", "wait before sending another test email")
		return
	}
	s.lastSMTPTest[adminID] = now
	s.smtpTestMu.Unlock()
	messageID := requestID(r)
	_, err = s.smtpTestSender.SendInvoiceReady(r.Context(), mailer.Message{
		ID: messageID, Kind: mailer.MessageSMTPTest, Recipient: user.Email,
		RequestNo:   "SMTP-TEST-" + strings.ToUpper(messageID[:min(len(messageID), 12)]),
		DownloadURL: s.publicOrigin + "/", CreatedAt: now,
	})
	if err != nil {
		s.logger.Warn("SMTP test failed", "request_id", messageID, "failure_stage", mailer.SMTPFailureStage(err))
		if s.operations != nil {
			_ = s.operations.RecordAdminAudit(r.Context(), adminID, "smtp_test", "smtp", messageID, "failure")
		}
		writeError(w, http.StatusBadGateway, "SMTP_TEST_FAILED", "test email delivery failed")
		return
	}
	if s.operations == nil || s.operations.RecordAdminAudit(r.Context(), adminID, "smtp_test", "smtp", messageID, "success") != nil {
		writeError(w, http.StatusInternalServerError, "SMTP_TEST_AUDIT_FAILED", "test email was sent but audit persistence failed")
		return
	}
	s.logger.Info("SMTP test email delivered", "admin_id", adminID, "request_id", messageID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminsettings.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "SETTINGS_REVISION_CONFLICT", err.Error())
	case errors.Is(err, adminsettings.ErrInvalidSettings):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SETTINGS", err.Error())
	case errors.Is(err, adminsettings.ErrNotConfigured):
		writeError(w, http.StatusNotFound, "SETTINGS_NOT_CONFIGURED", err.Error())
	case errors.Is(err, adminsettings.ErrSecretMissing):
		writeError(w, http.StatusNotFound, "SMTP_SECRET_MISSING", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "SETTINGS_ERROR", "settings operation failed")
	}
}

func principal(r *http.Request) identity {
	value, _ := r.Context().Value(identityKey).(identity)
	return value
}

func (s *Server) listLots(w http.ResponseWriter, r *http.Request) {
	items, err := s.ledger.ListFundingLots(r.Context(), principal(r).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DATA_UNAVAILABLE", "funding data is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": userFundingLotDTOs(items)})
}

func (s *Server) getInvoicePolicy(w http.ResponseWriter, r *http.Request) {
	if s.adminSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "POLICY_UNAVAILABLE", "invoice eligibility policy is unavailable")
		return
	}
	settings, err := s.adminSettings.Get(r.Context())
	if err != nil {
		handleSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"minimum_request_minor":      s.ledger.MinimumRequestMinor(),
		"service_item":               domain.FixedServiceItem,
		"eligibility_start_at":       settings.EligibilityStartAt.UTC().Format(time.RFC3339),
		"eligibility_policy_version": settings.EligibilityPolicyVersion,
		"eligibility_timezone":       adminsettings.EligibilityDisplayTimeZone,
		"eligibility_rule":           "payment_and_usage_at_or_after",
	})
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := s.ledger.ListProfiles(r.Context(), principal(r).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DATA_UNAVAILABLE", "profile data is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) listUserRequests(w http.ResponseWriter, r *http.Request) {
	s.listRequestsPage(w, r, false)
}
func (s *Server) listAdminRequests(w http.ResponseWriter, r *http.Request) {
	s.listRequestsPage(w, r, true)
}

func (s *Server) listRequestsPage(w http.ResponseWriter, r *http.Request, admin bool) {
	principalID := principal(r).UserID
	if admin {
		principalID = ""
	}
	pager, supportsPaging := s.ledger.(requestPageService)
	if !supportsPaging {
		items, err := s.ledger.ListRequests(r.Context(), principalID, admin)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "DATA_UNAVAILABLE", "invoice requests are temporarily unavailable")
			return
		}
		if admin {
			writeJSON(w, http.StatusOK, map[string]any{"items": adminRequestDTOs(items), "has_more": false})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"items": userRequestDTOs(items), "has_more": false})
		}
		return
	}
	query := application.RequestPageQuery{PrincipalID: principalID, Admin: admin, Limit: boundedQueryLimit(r, 100)}
	beforeAt := strings.TrimSpace(r.URL.Query().Get("before_submitted_at"))
	beforeID := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if (beforeAt == "") != (beforeID == "") {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "both request cursor fields are required")
		return
	}
	if beforeAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, beforeAt)
		if err != nil || !validUUIDText(beforeID) {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "invalid request cursor")
			return
		}
		query.BeforeSubmittedAt = parsed
		query.BeforeID = beforeID
	}
	for _, raw := range r.URL.Query()["status"] {
		for _, value := range strings.Split(raw, ",") {
			if value = strings.TrimSpace(value); value != "" {
				status := domain.RequestStatus(value)
				switch status {
				case domain.StatusPendingReview, domain.StatusNeedsChanges, domain.StatusApproved,
					domain.StatusRejected, domain.StatusUserCancelled, domain.StatusManualIssuing,
					domain.StatusIssuedAwaitingDocument, domain.StatusIssued, domain.StatusRefundAttention:
					query.Statuses = append(query.Statuses, status)
				default:
					writeError(w, http.StatusBadRequest, "INVALID_FILTER", "invalid request status filter")
					return
				}
			}
		}
	}
	if admin {
		query.SourceInstanceID = strings.TrimSpace(r.URL.Query().Get("source_instance_id"))
		if query.SourceInstanceID != "" && !validUUIDText(query.SourceInstanceID) {
			writeError(w, http.StatusBadRequest, "INVALID_FILTER", "invalid source instance filter")
			return
		}
	} else if r.URL.Query().Has("source_instance_id") {
		writeError(w, http.StatusBadRequest, "INVALID_FILTER", "source filter is administrator-only")
		return
	}
	page, err := pager.ListRequestsPage(r.Context(), query)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	response := map[string]any{"has_more": page.HasMore}
	if admin {
		response["items"] = adminRequestDTOs(page.Items)
	} else {
		response["items"] = userRequestDTOs(page.Items)
	}
	if page.HasMore {
		response["next_before_submitted_at"] = page.NextBeforeSubmittedAt
		response["next_before_id"] = page.NextBeforeID
	}
	writeJSON(w, http.StatusOK, response)
}

func validUUIDText(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

func (s *Server) getUserRequest(w http.ResponseWriter, r *http.Request) {
	s.getRequest(w, r, false)
}

func (s *Server) getAdminRequest(w http.ResponseWriter, r *http.Request) {
	s.getRequest(w, r, true)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request, admin bool) {
	requestID := strings.TrimSpace(r.PathValue("id"))
	if s.authMode != "mock" && !validUUIDText(requestID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_ID", "invalid invoice request ID")
		return
	}
	principalID := principal(r).UserID
	if admin {
		principalID = ""
	}
	request, err := s.ledger.GetRequest(r.Context(), principalID, requestID, admin)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	if admin {
		writeJSON(w, http.StatusOK, adminRequestDTOs([]domain.InvoiceRequest{request})[0])
	} else {
		writeJSON(w, http.StatusOK, userRequestDTOs([]domain.InvoiceRequest{request})[0])
	}
}

func (s *Server) verifyNewAPIPayment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PaidMinor int64  `json:"paid_minor"`
		Currency  string `json:"currency"`
		Evidence  string `json:"evidence_reference"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Evidence) == "" {
		writeError(w, http.StatusUnprocessableEntity, "EVIDENCE_REQUIRED", "payment evidence reference is required")
		return
	}
	lot, err := s.ledger.VerifyNewAPIPaymentWithEvidence(r.Context(), principal(r).UserID, r.PathValue("id"), input.PaidMinor, input.Currency, input.Evidence)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lot)
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	var profile domain.InvoiceProfile
	if !decodeJSON(w, r, &profile) {
		return
	}
	profile.PrincipalID = principal(r).UserID
	if s.authMode == "mock" {
		profile.EmailVerified = true
	} else {
		// Production persistence resolves verification server-side from the
		// authenticated user's verified-email registry. The browser flag is
		// deliberately ignored.
		profile.EmailVerified = false
	}
	saved, err := s.ledger.SaveProfile(r.Context(), profile)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) submitRequest(w http.ResponseWriter, r *http.Request) {
	var input ledger.SubmitInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.PrincipalID = principal(r).UserID
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	request, err := s.ledger.Submit(r.Context(), input)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, userRequestDTO(request))
}

func (s *Server) cancelRequest(w http.ResponseWriter, r *http.Request) {
	version, ok := expectedVersion(w, r)
	if !ok {
		return
	}
	request, err := s.ledger.Cancel(r.Context(), principal(r).UserID, r.PathValue("id"), version)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, userRequestDTO(request))
}

func (s *Server) reviewRequest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action, Note string
		Version      int64
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	request, err := s.ledger.Review(r.Context(), principal(r).UserID, r.PathValue("id"), input.Action, input.Note, input.Version)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

func (s *Server) beginManualIssue(w http.ResponseWriter, r *http.Request) {
	version, ok := expectedVersion(w, r)
	if !ok {
		return
	}
	request, err := s.ledger.BeginManualIssue(r.Context(), principal(r).UserID, r.PathValue("id"), version)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

func (s *Server) confirmManualIssue(w http.ResponseWriter, r *http.Request) {
	version, ok := expectedVersion(w, r)
	if !ok {
		return
	}
	request, err := s.ledger.ConfirmManualIssue(r.Context(), principal(r).UserID, r.PathValue("id"), version)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

func (s *Server) uploadDocument(w http.ResponseWriter, r *http.Request) {
	if s.documentStore == nil {
		writeError(w, http.StatusServiceUnavailable, "DOCUMENT_STORE_DISABLED", "document storage is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, document.DefaultMaxPDFBytes+(1<<20))
	if err := r.ParseMultipartForm(document.DefaultMaxPDFBytes); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_UPLOAD", "invalid or oversized multipart upload")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	version, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("version")), 10, 64)
	if err != nil || version <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_VERSION", "invalid request version")
		return
	}
	invoiceNumber := strings.TrimSpace(r.FormValue("invoice_number"))
	if invoiceNumber == "" {
		writeError(w, http.StatusUnprocessableEntity, "INVOICE_NUMBER_REQUIRED", "invoice number is required")
		return
	}
	issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(r.FormValue("issued_at")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ISSUED_AT", "issued_at must be RFC3339")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "PDF_REQUIRED", "PDF file is required")
		return
	}
	defer file.Close()
	stored, err := s.documentStore.SavePDF(r.Context(), file)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "PDF_REJECTED", "PDF failed size, format, or malware validation")
		return
	}
	doc := domain.InvoiceDocument{RequestID: r.PathValue("id"), InvoiceNumber: invoiceNumber, ObjectKey: stored.ObjectKey, ObjectVersion: stored.ObjectVersion, SHA256: stored.SHA256, SizeBytes: stored.SizeBytes, MIME: stored.MIME, ScanStatus: "clean", IssuedAt: issuedAt}
	request, saved, outbox, err := s.ledger.AttachDocument(r.Context(), principal(r).UserID, doc, version)
	if err != nil {
		_ = s.documentStore.Delete(stored.ObjectKey)
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"request": request, "document": saved, "email_outbox": outbox})
}

func (s *Server) downloadDocument(w http.ResponseWriter, r *http.Request) {
	s.downloadDocumentForRole(w, r, false)
}

func (s *Server) downloadAdminDocument(w http.ResponseWriter, r *http.Request) {
	s.downloadDocumentForRole(w, r, true)
}

func (s *Server) downloadDocumentForRole(w http.ResponseWriter, r *http.Request, admin bool) {
	if s.documentStore == nil {
		writeError(w, http.StatusServiceUnavailable, "DOCUMENT_STORE_DISABLED", "document storage is not configured")
		return
	}
	var doc domain.InvoiceDocument
	var err error
	if admin {
		doc, err = s.ledger.GetDocumentForRequestAsAdmin(r.Context(), r.PathValue("id"))
	} else {
		doc, err = s.ledger.GetDocumentForRequest(r.Context(), principal(r).UserID, r.PathValue("id"))
	}
	if err != nil {
		handleDomainError(w, err)
		return
	}
	file, err := s.documentStore.OpenAuthorized(doc.ObjectKey)
	if err != nil {
		writeError(w, http.StatusNotFound, "DOCUMENT_NOT_FOUND", "document file is unavailable")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="invoice.pdf"`)
	w.Header().Set("Cache-Control", "private, no-store")
	// The administrator route passes through the generic API proxy location.
	// Explicitly disable upstream response buffering so decrypted PDF bytes can
	// never spill into the host proxy cache/temp path.
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Length", strconv.FormatInt(doc.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func expectedVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match-Version"))
	if raw == "" {
		var body struct {
			Version int64 `json:"version"`
		}
		if !decodeJSON(w, r, &body) {
			return 0, false
		}
		return body.Version, body.Version > 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_VERSION", "invalid request version")
		return 0, false
	}
	return value, true
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-site")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("request body must contain exactly one JSON value")
		}
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func handleDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, 404, "NOT_FOUND", err.Error())
	case errors.Is(err, domain.ErrForbidden):
		writeError(w, 403, "FORBIDDEN", err.Error())
	case errors.Is(err, domain.ErrMinimumAmount), errors.Is(err, domain.ErrInsufficientAmount), errors.Is(err, domain.ErrUnverifiedPayment), errors.Is(err, domain.ErrSourceMixing):
		writeError(w, 422, "UNPROCESSABLE", err.Error())
	case errors.Is(err, application.ErrOIDCEmailUnverified):
		writeError(w, http.StatusUnprocessableEntity, "EMAIL_NOT_VERIFIED", "delivery email must be verified")
	case errors.Is(err, application.ErrEvidenceRequired):
		writeError(w, http.StatusUnprocessableEntity, "EVIDENCE_REQUIRED", "independent payment evidence is required")
	case errors.Is(err, application.ErrIssuerNotConfigured):
		writeError(w, http.StatusConflict, "ISSUER_NOT_CONFIGURED", "invoice issuer is not configured")
	case errors.Is(err, domain.ErrSourceUnavailable):
		writeError(w, http.StatusServiceUnavailable, "SOURCE_SYNC_UNAVAILABLE", "source synchronization is stale or still processing")
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrVersionConflict), errors.Is(err, domain.ErrInvalidState):
		writeError(w, 409, "CONFLICT", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be processed")
	}
}
