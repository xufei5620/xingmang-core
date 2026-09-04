package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/httpapi"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/mailer"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
	"invoice-system/backend/internal/sourceingest"
)

type onceWorker interface {
	RunOnce(context.Context) (int, error)
}

type workerFunc func(context.Context) (int, error)

func (f workerFunc) RunOnce(ctx context.Context) (int, error) { return f(ctx) }

type workerSpec struct {
	Name     string
	Interval time.Duration
	Worker   onceWorker
}

type appRuntime struct {
	API        *httpapi.Server
	AuthMode   string
	SourceMode string
	Workers    []workerSpec
	close      func()
}

func (r appRuntime) Close() {
	if r.close != nil {
		r.close()
	}
}

func runWorker(ctx context.Context, spec workerSpec) {
	interval := spec.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		processed, err := spec.Worker.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("background worker failed", "worker", spec.Name, "processed", processed, "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil && processed > 0 {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func buildRuntime(ctx context.Context) (appRuntime, error) {
	appEnv := strings.ToLower(strings.TrimSpace(env("APP_ENV", "development")))
	authMode := strings.ToLower(strings.TrimSpace(env("AUTH_MODE", "mock")))
	if err := auth.EnforceProductionAuthMode(appEnv, authMode, os.Getenv); err != nil {
		return appRuntime{}, err
	}
	if appEnv == "production" {
		return buildProductionRuntime(ctx, authMode)
	}
	return buildMockRuntime(authMode)
}

func buildMockRuntime(authMode string) (appRuntime, error) {
	if authMode != "mock" {
		return appRuntime{}, errors.New("non-production runtime currently supports AUTH_MODE=mock only")
	}
	service := ledger.NewService()
	seedMock(service)
	adminCIDRs := csvEnv("ADMIN_IP_ALLOWLIST", "127.0.0.1/32,::1/128")
	breakGlassCIDRs, err := loadBreakGlassCIDRs(authMode)
	if err != nil {
		return appRuntime{}, err
	}
	settingsRepo := adminsettings.NewMemoryRepository(adminsettings.Settings{IssuerName: adminsettings.UnconfiguredIssuerName, ServiceItem: adminsettings.FixedServiceItem, MinimumRequestMinor: adminsettings.DefaultMinimumRequestMinor, EligibilityStartAt: adminsettings.RequiredEligibilityStartAt, SMTPHost: "smtp.qq.com", SMTPPort: 587, SMTPFrom: "not-configured@qq.com", SMTPFromName: "发票中心", SMTPStartTLS: true, AdminCIDRs: adminCIDRs, Revision: 1, UpdatedBy: "bootstrap", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	keyring := securefields.Keyring{CurrentKeyID: "dev-only", EncryptionKeys: map[string][]byte{"dev-only": bytes.Repeat([]byte{0x42}, 32)}, IndexKey: bytes.Repeat([]byte{0x24}, 32)}
	settingsService := adminsettings.NewService(settingsRepo, adminsettings.SecureFieldsBox{Keyring: keyring, AAD: "invoice/admin-settings/smtp-authorization-code"})
	var store document.Store
	if root := strings.TrimSpace(os.Getenv("DOCUMENT_ROOT")); root != "" {
		store = document.LocalStore{Root: root, Scanner: document.ScannerFunc(func(context.Context, string) error { return nil })}
	}
	api, err := httpapi.NewWithConfig(service, httpapi.Config{AuthMode: authMode, SourceMode: "mock", AdminIPAllowlist: adminCIDRs, BreakGlassCIDRs: breakGlassCIDRs, TrustedProxies: csvEnv("TRUSTED_PROXY_CIDRS", ""), DocumentStore: store, AdminSettings: settingsService}, slog.Default())
	if err != nil {
		return appRuntime{}, err
	}
	return appRuntime{API: api, AuthMode: authMode, SourceMode: "mock"}, nil
}

// economicRescanActivityPollWindows is the XM-INV-AGENT-RESTART-GRACE part A
// readiness-grace activity-window multiplier: how many SOURCE_POLL_INTERVAL
// windows of continued source_economic_scan_cycles updated_at activity are
// tolerated (see buildProductionRuntime's economicRescanActivityMaxAge and
// postgresstore.economicRescanActivityWithinWindow) before an actively
// rescanning stream is treated as stalled instead of in-progress. Two windows
// gives one full cycle of margin beyond the single window a healthy,
// continuously-updating cycle would already satisfy -- this covers the gap
// between successive agent-side batches while a cycle is still 'receiving'.
//
// economicRescanProcessingTailAllowance covers a second, separate gap that
// the poll-interval term above does not: once the agent has sent every page
// (the row flips from 'receiving' to 'processing'), nothing touches
// updated_at again until the backend's own projection workers finish
// processing every event in the cycle and it publishes -- CommitSourceBatch's
// INSERT only fires from the agent side (source_sync.go), and the row is not
// touched again until postgresstore's projection/publish path (consumption.go)
// marks it published or blocked. A production reconcile observed end to end
// (2026-09-03, Sub2API usage, restart-triggered, cutover 2026-08-25): started
// 06:31Z, published 06:56Z, 3,327 batches -- about 25 minutes total, covering
// only the data since cutover (not the full source table; a fixed, one-time
// cutover manifest position never advances, so this window grows slowly with
// time since cutover -- exactly what part B now bounds to "since the last
// reconcile" instead). The receiving/processing split within that 25 minutes
// is not separately known, so this allowance is sized to exceed the entire
// observed cycle even in the worst case (all 25 minutes spent motionless in
// 'processing'), with real margin, while staying well short of
// SOURCE_RECONCILE_INTERVAL (6h) so a genuinely stalled cycle -- the agent
// crashed, or the backend workers stopped -- is still caught same-day.
const (
	economicRescanActivityPollWindows     = 2
	economicRescanProcessingTailAllowance = 30 * time.Minute
)

func buildProductionRuntime(ctx context.Context, authMode string) (appRuntime, error) {
	if authMode != "oidc" || strings.ToLower(strings.TrimSpace(os.Getenv("SOURCE_MODE"))) != "agent" {
		return appRuntime{}, errors.New("production requires AUTH_MODE=oidc and SOURCE_MODE=agent")
	}
	publicOrigin, err := exactHTTPSOrigin(os.Getenv("PUBLIC_ORIGIN"))
	if err != nil {
		return appRuntime{}, err
	}
	databaseURL, err := readSecretLine(os.Getenv("DATABASE_URL_FILE"), 64<<10)
	if err != nil {
		return appRuntime{}, fmt.Errorf("database URL: %w", err)
	}
	store, err := postgresstore.Open(ctx, databaseURL)
	if err != nil {
		return appRuntime{}, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			store.Close()
		}
	}()
	migrationsDir := env("MIGRATIONS_DIR", "/app/migrations")
	if err = migrate.Verify(ctx, store.Pool(), migrationsDir); err != nil {
		return appRuntime{}, fmt.Errorf("production migration verification: %w", err)
	}
	if err = verifyRuntimeDatabasePrivileges(ctx, store); err != nil {
		return appRuntime{}, err
	}
	keyring, err := securefields.LoadKeyringFile(os.Getenv("FIELD_KEYRING_FILE"))
	if err != nil {
		return appRuntime{}, err
	}
	settingsRepository := adminsettings.NewPostgresRepository(store.Pool())
	settingsService := adminsettings.NewService(settingsRepository, adminsettings.SecureFieldsBox{Keyring: keyring, AAD: "invoice/admin-settings/smtp-authorization-code"})
	settings, err := settingsService.Get(ctx)
	if err != nil {
		return appRuntime{}, fmt.Errorf("production admin settings must be bootstrapped: %w", err)
	}
	smtpTestRecipient := strings.TrimSpace(os.Getenv("SMTP_TEST_RECIPIENT"))
	if smtpTestRecipient != "" && strings.EqualFold(smtpTestRecipient, strings.TrimSpace(settings.SMTPFrom)) {
		return appRuntime{}, errors.New("SMTP_TEST_RECIPIENT must differ from the configured SMTP sender")
	}
	// An unconfigured issuer must not prevent the API from starting: the
	// protected admin UI is the only supported path for replacing the bootstrap
	// placeholder. Saving settings and confirming an issue remain fail closed.
	if err = validateEligibilityPolicyStart(os.Getenv("ELIGIBILITY_START_AT"), settings.EligibilityStartAt); err != nil {
		return appRuntime{}, err
	}
	if err = verifyEligibilitySourceManifests(ctx, store); err != nil {
		return appRuntime{}, err
	}
	economicHeartbeatMaxAge, err := boundedDurationEnv("SOURCE_ECONOMIC_HEARTBEAT_MAX_STALENESS", "5m", 30*time.Second, 24*time.Hour)
	if err != nil {
		return appRuntime{}, err
	}
	economicWatermarkMaxAge, err := boundedDurationEnv("SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS", "15m", 30*time.Second, 24*time.Hour)
	if err != nil {
		return appRuntime{}, err
	}
	economicSafetyDelay, err := boundedDurationEnv("SOURCE_ECONOMIC_SAFETY_DELAY", "5m", time.Minute, 24*time.Hour)
	if err != nil {
		return appRuntime{}, err
	}
	sourcePollInterval, err := boundedDurationEnv("SOURCE_POLL_INTERVAL", "1m", 5*time.Second, time.Hour)
	if err != nil {
		return appRuntime{}, err
	}
	// XM-INV-AGENT-RESTART-GRACE part A: the readiness-grace activity window
	// for a source_economic_scan_cycles row proving an active rescan. Derived,
	// not independently configured, from the same poll-interval and
	// safety-delay budget the freshness check above already validates
	// (economicRescanActivityPollWindows poll intervals of slack while the
	// agent is actively posting pages, plus the safety delay), plus
	// economicRescanProcessingTailAllowance for the separate gap after the
	// agent finishes sending and before the backend finishes processing and
	// publishes -- see that constant's doc comment for the production
	// measurement this is calibrated against.
	economicRescanActivityMaxAge := economicRescanActivityPollWindows*sourcePollInterval + economicSafetyDelay + economicRescanProcessingTailAllowance
	if err = validateSourceFreshnessBudget(economicWatermarkMaxAge, economicSafetyDelay, sourcePollInterval); err != nil {
		return appRuntime{}, err
	}
	identitiesMaxAge, err := boundedDurationEnv("SOURCE_IDENTITIES_MAX_STALENESS", "15m", 30*time.Second, 24*time.Hour)
	if err != nil {
		return appRuntime{}, err
	}
	logoutTokenMaxAge, err := boundedDurationEnv("OIDC_LOGOUT_TOKEN_MAX_AGE", "10m", time.Minute, 30*time.Minute)
	if err != nil {
		return appRuntime{}, err
	}
	oidcMaximumResponseBytes, err := boundedInt64Env("OIDC_MAX_HTTP_RESPONSE_BYTES", 1<<20, 64<<10, 4<<20)
	if err != nil {
		return appRuntime{}, err
	}
	appService, err := application.NewService(store, keyring, settingsService, application.Options{
		MinimumRequestMinor: settings.MinimumRequestMinor, DownloadBaseURL: publicOrigin,
		EmailTemplateVersion:               "invoice-ready-v1",
		SourceEconomicHeartbeatMaxAge:      economicHeartbeatMaxAge,
		SourceEconomicWatermarkMaxAge:      economicWatermarkMaxAge,
		SourceIdentitiesMaxAge:             identitiesMaxAge,
		SourceEconomicRescanActivityMaxAge: economicRescanActivityMaxAge,
	})
	if err != nil {
		return appRuntime{}, err
	}
	oidcConfig := auth.OIDCConfig{
		IssuerURL: os.Getenv("OIDC_ISSUER_URL"), ClientID: os.Getenv("OIDC_CLIENT_ID"),
		ClientSecretFile: os.Getenv("OIDC_CLIENT_SECRET_FILE"), RedirectURL: publicOrigin + "/api/v1/auth/callback",
		PostLogoutRedirectURL: publicOrigin + "/",
		ProviderLabel:         env("OIDC_PROVIDER_LABEL", "SoloV 统一登录"), AdminRole: os.Getenv("OIDC_ADMIN_ROLE"),
		RoleClaim: env("OIDC_ROLE_CLAIM", "roles"), RequiredAdminACR: os.Getenv("OIDC_REQUIRED_ADMIN_ACR"),
		RequiredAdminAMR:          csvEnv("OIDC_REQUIRED_ADMIN_AMR", "otp"),
		AllowedSigningAlgs:        csvEnv("OIDC_ALLOWED_SIGNING_ALGS", "RS256"),
		AllowedEndpointHosts:      csvEnv("OIDC_ALLOWED_ENDPOINT_HOSTS", ""),
		AllowedPrivateEndpointIPs: csvEnv("OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS", ""),
		Scopes:                    csvEnv("OIDC_SCOPES", "openid,profile,email"),
		TokenEndpointAuthMethod:   env("OIDC_TOKEN_AUTH_METHOD", "client_secret_basic"),
		LogoutTokenMaxAge:         logoutTokenMaxAge, RequireBackchannelLogout: true,
		MaximumHTTPResponseBytes: oidcMaximumResponseBytes,
	}
	// CR-0006 (XM-INV-CONSOLE-ASSERT): OIDC_ADMIN_LOGIN_ENABLED defaults true
	// (unchanged production behavior in this phase; Keycloak stays the
	// default admin login path). When explicitly set to false, OIDC
	// discovery below is skipped entirely (a real network dependency on the
	// IdP at every process start) and ProductionAuth.Register does not wire
	// the OIDC-touching routes at all -- see production_auth.go.
	oidcAdminLoginEnabled, err := boolEnv("OIDC_ADMIN_LOGIN_ENABLED", true)
	if err != nil {
		return appRuntime{}, err
	}
	consoleAssertionEnabled, err := boolEnv("CONSOLE_ASSERTION_ENABLED", false)
	if err != nil {
		return appRuntime{}, err
	}
	flowStore := auth.NewPostgresFlowStore(store.Pool())
	var oidcClient *auth.OIDCClient
	if oidcAdminLoginEnabled {
		oidcClient, err = auth.NewOIDCClient(ctx, oidcConfig, flowStore, auth.SecureFieldsFlowProtector{Keyring: keyring})
		if err != nil {
			return appRuntime{}, err
		}
	}
	sessionStore := auth.NewPostgresSessionStore(store.Pool(), keyring)
	auditSink := auth.NewPostgresSecurityAuditSink(store.Pool())
	sessions, err := auth.NewSessionManager(sessionStore, auth.SessionConfig{}, auditSink)
	if err != nil {
		return appRuntime{}, err
	}
	bindingHasher, err := auth.NewClientBindingHasherFromFile(os.Getenv("SESSION_BINDING_KEY_FILE"))
	if err != nil {
		return appRuntime{}, err
	}
	csrf, err := auth.NewCSRFPolicy([]string{publicOrigin})
	if err != nil {
		return appRuntime{}, err
	}
	adminPolicy := auth.AdminPolicy{Role: oidcConfig.AdminRole, RequiredACR: oidcConfig.RequiredAdminACR, RequiredAMR: oidcConfig.RequiredAdminAMR, StepUpMaxAge: 10 * time.Minute}
	identityStore := auth.NewPostgresIdentityStore(store.Pool())
	backchannelLogout, err := auth.NewBackchannelLogoutService(auth.NewPostgresBackchannelLogoutRepository(store.Pool()))
	if err != nil {
		return appRuntime{}, err
	}
	platformSourceInstanceIDs, err := loadPlatformSourceInstanceIDs(ctx, store)
	if err != nil {
		return appRuntime{}, err
	}
	// CR-0006: CONSOLE_ASSERTION_ENABLED defaults false (unchanged
	// production behavior in this phase). The nonce store, rate limiter and
	// audit sink have no external config of their own and are always built;
	// only the issuer/audience/keys-file below need "fail closed on
	// malformed config" gated to when the feature is actually meant to be
	// used -- an operator who never turns this on should not need a valid
	// CONSOLE_ASSERTION_ISSUER or keys file at all.
	consoleAssertionNonces := auth.NewPostgresConsoleAssertionNonceStore(store.Pool())
	// 20/minute matches the design spec's own cited rate exactly (section
	// 5.2) and the edge nginx zone (invoice_auth, deploy/nginx/invoice-
	// http-context.conf) that already covers this route by prefix -- kept
	// as a fixed value, not a new env var, so there is exactly one place
	// this number is decided rather than two that could drift apart.
	consoleAssertionRateLimiter := auth.NewLoginRateLimiter(20, time.Minute)
	consoleAssertionKeyring, consoleAssertionCfg, err := loadConsoleAssertionRuntimeConfig(consoleAssertionEnabled, oidcConfig.AdminRole)
	if err != nil {
		return appRuntime{}, err
	}
	productionAuth := &httpapi.ProductionAuth{
		Sessions: sessions, BindingHasher: bindingHasher,
		CSRF: csrf, Admin: adminPolicy, BackchannelLogout: backchannelLogout,
		DisableOIDCAdminLogin: !oidcAdminLoginEnabled,
		ProvisionUser: func(callbackCtx context.Context, principal auth.Principal, requestID string) (httpapi.SessionUser, error) {
			return provisionPlatformOrOIDCUser(callbackCtx, appService, identityStore, principal, requestID, platformSourceInstanceIDs)
		},
		LoadUser: func(loadCtx context.Context, userID string) (httpapi.SessionUser, error) {
			// No principal is available on a plain session reload (only the
			// stored userID), so Claimed can't be recovered here -- only a fresh
			// login (see provisionPlatformOrOIDCUser) sets it. The platform
			// display name lives on the session row now (encrypted,
			// migration 0017), not here -- sessionStatus reads it straight off
			// current.Session.DisplayName instead of going through LoadUser.
			return loadSessionUser(loadCtx, appService, userID, false)
		},
		ConsoleAssertionEnabled:     consoleAssertionEnabled,
		ConsoleAssertionKeyring:     consoleAssertionKeyring,
		ConsoleAssertionConfig:      consoleAssertionCfg,
		ConsoleAssertionNonces:      consoleAssertionNonces,
		ConsoleAssertionRateLimiter: consoleAssertionRateLimiter,
		SecurityAudit:               auditSink,
	}
	// oidcClient is only non-nil when oidcAdminLoginEnabled (see its
	// construction above); assigning it unconditionally here would wrap a
	// nil *auth.OIDCClient inside the OIDC/Logout interface fields, which is
	// NOT a nil interface (the classic Go footgun) -- Register's route-
	// registration gate is the real defense, but leaving these fields as a
	// literal nil interface value when disabled is a second, independent
	// guard against ever calling through a nil pointer.
	if oidcAdminLoginEnabled {
		productionAuth.OIDC = oidcClient
		productionAuth.Logout = oidcClient
	}
	platformLogin, err := buildPlatformLogin(productionAuth)
	if err != nil {
		return appRuntime{}, err
	}
	documentRoot := strings.TrimSpace(os.Getenv("DOCUMENT_ROOT"))
	quarantineRoot := strings.TrimSpace(os.Getenv("DOCUMENT_QUARANTINE_ROOT"))
	clamAVAddress := strings.TrimSpace(os.Getenv("CLAMAV_ADDRESS"))
	clamAVDatabaseRoot := strings.TrimSpace(os.Getenv("CLAMAV_DATABASE_ROOT"))
	pdfScannerSocket := strings.TrimSpace(os.Getenv("PDF_SCANNER_SOCKET"))
	pdfScannerCapabilityFile := strings.TrimSpace(os.Getenv("PDF_SCANNER_CAPABILITY_FILE"))
	if documentRoot == "" || quarantineRoot == "" || clamAVAddress == "" || clamAVDatabaseRoot == "" || pdfScannerSocket == "" || pdfScannerCapabilityFile == "" {
		return appRuntime{}, errors.New("production document root, tmpfs quarantine root, ClamAV controls and isolated PDF scanner paths are required")
	}
	clamAVMaxAge := document.DefaultClamAVMaxUpdateAge
	if value := strings.TrimSpace(os.Getenv("CLAMAV_MAX_SIGNATURE_AGE")); value != "" {
		clamAVMaxAge, err = time.ParseDuration(value)
		if err != nil || clamAVMaxAge <= 0 {
			return appRuntime{}, errors.New("CLAMAV_MAX_SIGNATURE_AGE must be a positive duration")
		}
	}
	clamAVScanner := document.ClamAVScanner{Address: clamAVAddress}
	clamAVDatabaseScanner := document.ClamAVDatabaseScanner{Root: clamAVDatabaseRoot, MaxUpdateAge: clamAVMaxAge}
	pdfScanner := document.SidecarScanner{SocketPath: pdfScannerSocket, TokenFile: pdfScannerCapabilityFile, MaxBytes: document.DefaultMaxPDFBytes, Timeout: 20 * time.Second}
	documentStore := document.EncryptedLocalStore{Root: documentRoot, QuarantineRoot: quarantineRoot, Scanner: document.ScannerChain{clamAVDatabaseScanner, clamAVScanner, pdfScanner}, Keyring: keyring}
	trust, err := sourceingest.LoadTrustFile(os.Getenv("SOURCE_TRUST_FILE"))
	if err != nil {
		return appRuntime{}, err
	}
	ingestProxyNetworks, err := sourceingest.ParseProxyCIDRs(csvEnv("INGEST_PROXY_CIDRS", ""))
	if err != nil {
		return appRuntime{}, err
	}
	receiver := &sourceingest.Receiver{Trust: trust, Acceptor: appService, ProxyCIDRs: ingestProxyNetworks, Logger: slog.Default()}
	breakGlass, err := loadBreakGlassCIDRs(authMode)
	if err != nil {
		return appRuntime{}, err
	}
	trustedProxies := csvEnv("TRUSTED_PROXY_CIDRS", "")
	if len(trustedProxies) == 0 {
		return appRuntime{}, errors.New("production trusted proxy CIDRs are required")
	}
	// One instance for the process lifetime: the Readiness closure below
	// calls warnIfStale on every /readyz evaluation, and the rate limit
	// (eligibilityProofPendingWarnInterval) only works if state persists
	// across calls instead of being reset per-request.
	eligibilityProofPendingWarnings := &eligibilityProofPendingWarner{}
	api, err := httpapi.NewWithConfig(appService, httpapi.Config{
		AuthMode: "oidc", SourceMode: "agent", AdminIPAllowlist: settings.AdminCIDRs,
		BreakGlassCIDRs: breakGlass, TrustedProxies: trustedProxies,
		DocumentStore: documentStore, AdminSettings: settingsService, ProductionAuth: productionAuth, PlatformLogin: platformLogin,
		SMTPTestSender: mailer.SettingsSender{Source: settingsService}, SMTPTestRecipient: smtpTestRecipient, PublicOrigin: publicOrigin,
		SourceIngest: receiver,
		Readiness: func(readyCtx context.Context) error {
			if pingErr := store.Pool().Ping(readyCtx); pingErr != nil {
				return pingErr
			}
			currentSettings, settingsErr := settingsService.Get(readyCtx)
			if settingsErr != nil {
				return settingsErr
			}
			if issuerErr := validateIssuerReadiness(currentSettings); issuerErr != nil {
				return issuerErr
			}
			if clamErr := clamAVScanner.Ping(readyCtx); clamErr != nil {
				return clamErr
			}
			if signatureErr := document.CheckClamAVDatabaseFreshness(clamAVDatabaseRoot, clamAVMaxAge, time.Now().UTC()); signatureErr != nil {
				return signatureErr
			}
			if pdfScannerErr := pdfScanner.Ping(readyCtx); pdfScannerErr != nil {
				return pdfScannerErr
			}
			sourceHealth, healthErr := appService.SourceReadinessHealth(readyCtx)
			if healthErr != nil {
				return healthErr
			}
			if ingestErr := validateSourceIngestRuntimeReadiness(sourceHealth.Ingest, time.Now().UTC()); ingestErr != nil {
				return ingestErr
			}
			eligibilityHealth, healthErr := store.EligibilityProjectionHealth(readyCtx)
			if healthErr != nil {
				return healthErr
			}
			readinessNow := time.Now().UTC()
			eligibilityProofPendingWarnings.warnIfStale(eligibilityHealth, readinessNow)
			if readinessErr := eligibilityProjectionReady(eligibilityHealth, readinessNow); readinessErr != nil {
				return readinessErr
			}
			if readinessErr := validateSourceRuntimeReadiness(sourceHealth.Report); readinessErr != nil {
				return readinessErr
			}
			return nil
		},
	}, slog.Default())
	if err != nil {
		return appRuntime{}, err
	}
	workers := []workerSpec{
		{Name: "email-outbox", Interval: 5 * time.Second, Worker: mailer.Worker{Repository: appService, Sender: mailer.SettingsSender{Source: settingsService}, BatchSize: 20}},
		{Name: "source-projection", Interval: 2 * time.Second, Worker: application.SourceEventProcessor{Service: appService, BatchSize: 100}},
		{Name: "eligibility-projection", Interval: 2 * time.Second, Worker: workerFunc(func(workerCtx context.Context) (int, error) {
			return store.ProcessEligibilityProjectionJobs(workerCtx, 25, time.Now().UTC(), postgresstore.AuditActor{
				Type: "system", ID: "eligibility-projection-worker", Reason: "finalized economic facts projected",
			})
		})},
		{Name: "auth-cleanup", Interval: time.Hour, Worker: workerFunc(func(cleanCtx context.Context) (int, error) {
			flows, flowErr := flowStore.DeleteExpired(cleanCtx, time.Now().UTC())
			if flowErr != nil {
				return int(flows), flowErr
			}
			sessionCount, sessionErr := sessionStore.DeleteExpired(cleanCtx, time.Now().UTC())
			if sessionErr != nil {
				return int(flows + sessionCount), sessionErr
			}
			// console_assertion_nonces (migration 0018): harmless to sweep
			// even when CONSOLE_ASSERTION_ENABLED=false -- the table simply
			// stays empty in that case.
			nonceCount, nonceErr := consoleAssertionNonces.DeleteExpired(cleanCtx, time.Now().UTC())
			return int(flows + sessionCount + nonceCount), nonceErr
		})},
	}
	closeOnError = false
	return appRuntime{API: api, AuthMode: "oidc", SourceMode: "agent", Workers: workers, close: store.Close}, nil
}

// loadConsoleAssertionRuntimeConfig is the pure config-loading decision
// extracted from buildProductionRuntime so the "off means untouched" contract
// is table-testable without a database (same extraction pattern as
// eligibilityProjectionReady below, XM-INV-READY-PENDING).
//
// When enabled is false it must read neither CONSOLE_ASSERTION_ISSUER/
// CONSOLE_ASSERTION_AUDIENCE nor the CONSOLE_ASSERTION_KEYS_FILE path, and
// must not touch the filesystem at all -- an operator who has never turned
// this feature on needs no valid keys file to exist. This matters concretely
// for docker-compose.prod.yml's CONSOLE_ASSERTION_KEYRING_FILE bind mount
// (XM-INV-CONSOLE-ASSERT-DEPLOY): the mounted path may be an empty
// placeholder file, or briefly absent before deploy/roll-forward.sh's own
// preflight creates one, for every release where this flag stays false.
//
// When enabled is true, a missing/empty/malformed keys file must still fail
// the process closed (auth.LoadConsoleAssertionKeyringJSON already refuses an
// empty manifest -- "at least one key" -- by design, see its own doc
// comment): silently starting with zero trusted keys would make the
// exchange endpoint reject every assertion forever instead of the operator
// noticing at startup that the real reviewed manifest was never installed.
// oidcAdminRole is this deployment's OIDC_ADMIN_ROLE and serves as the
// default for CONSOLE_ASSERTION_ADMIN_ROLE, so a deployment that has not set
// the newer variable keeps the exact behavior it had before that variable
// existed. Production sets them to different values on purpose: the console
// signs its own staff role vocabulary while the transitional Keycloak login
// carries the realm role -- see ConsoleAssertionConfig.AdminRole's doc
// comment (XM-INV-CONSOLE-ASSERT-ADMIN-ROLE).
func loadConsoleAssertionRuntimeConfig(enabled bool, oidcAdminRole string) (*auth.ConsoleAssertionKeyring, auth.ConsoleAssertionConfig, error) {
	if !enabled {
		return nil, auth.ConsoleAssertionConfig{}, nil
	}
	cfg := auth.ConsoleAssertionConfig{
		Issuer:    os.Getenv("CONSOLE_ASSERTION_ISSUER"),
		Audience:  env("CONSOLE_ASSERTION_AUDIENCE", "xingmang-console-assertion-v1"),
		AdminRole: env("CONSOLE_ASSERTION_ADMIN_ROLE", oidcAdminRole),
	}
	if err := cfg.Validate(); err != nil {
		return nil, auth.ConsoleAssertionConfig{}, fmt.Errorf("console assertion config: %w", err)
	}
	keysFilePath := strings.TrimSpace(os.Getenv("CONSOLE_ASSERTION_KEYS_FILE"))
	if keysFilePath == "" {
		return nil, auth.ConsoleAssertionConfig{}, errors.New("CONSOLE_ASSERTION_KEYS_FILE is required when CONSOLE_ASSERTION_ENABLED=true")
	}
	keysFileBytes, readErr := readBoundedConfigFile(keysFilePath, 256<<10)
	if readErr != nil {
		return nil, auth.ConsoleAssertionConfig{}, fmt.Errorf("read CONSOLE_ASSERTION_KEYS_FILE: %w", readErr)
	}
	keyring, err := auth.LoadConsoleAssertionKeyringJSON(keysFileBytes)
	if err != nil {
		return nil, auth.ConsoleAssertionConfig{}, fmt.Errorf("load console assertion keyring: %w", err)
	}
	return keyring, cfg, nil
}

func validateIssuerReadiness(settings adminsettings.Settings) error {
	if !adminsettings.IsIssuerConfigured(settings.IssuerName) {
		return application.ErrIssuerNotConfigured
	}
	return nil
}

// eligibilityProjectionStuckAfter bounds EligibilityProjectionHealth's
// OldestPending (a job making no real progress: claimed, failed, or queued
// for a reason other than an actively-scheduled BALANCE_PROOF_PENDING
// backoff -- see that struct's doc comment in postgresstore/consumption.go).
// A job legitimately waiting on a balance proof never counts toward
// OldestPending and so never trips this on its own, however long it waits;
// eligibilityProofPendingWarnAge below is its own, non-blocking signal for
// that case.
const eligibilityProjectionStuckAfter = 15 * time.Minute

// eligibilityProjectionDeadReason/eligibilityProjectionStuckReason
// (XM-INV-PROJECTION-FAILURE-GRADING) are eligibilityProjectionReady's two
// distinct not-ready reasons, named so an operator reading /readyz's error
// text (or application logs) can immediately tell "a job needs
// invoice-eligibility-repair --kind=projection-requeue-dead" apart from "the
// queue is not making progress" -- previously both conditions returned the
// identical generic string.
const (
	eligibilityProjectionDeadReason  = "invoice eligibility projection has dead jobs requiring operator repair"
	eligibilityProjectionStuckReason = "invoice eligibility projection is not making progress"
)

// eligibilityProjectionReady is the pure decision extracted from the
// Readiness closure in buildProductionRuntime so it is table-testable
// without a database (XM-INV-READY-PENDING). XM-INV-PROJECTION-FAILURE-GRADING:
// Dead (renamed from Failed) counts only the new terminal status='dead'
// grade -- a job merely retrying with backoff (Retrying>0, EligibilityProjectionHealth's
// Queued/attempts>0 bucket) is never terminal and must never trip readiness
// on its own; OldestPending already excludes such a job while its backoff
// has not elapsed (see that struct's own doc comment), so a purely
// backoff-driven retry loop never ages into the stuck bucket either.
func eligibilityProjectionReady(health postgresstore.EligibilityProjectionHealth, now time.Time) error {
	if health.Dead > 0 {
		return errors.New(eligibilityProjectionDeadReason)
	}
	if !health.OldestPending.IsZero() && now.Sub(health.OldestPending) > eligibilityProjectionStuckAfter {
		return errors.New(eligibilityProjectionStuckReason)
	}
	return nil
}

// eligibilityProofPendingWarnAge/-Interval bound the operations-visibility
// Warn log for balance-proof-pending jobs that have been waiting a long
// time. This never affects readiness (see eligibilityProjectionReady) --
// it exists only so a source stream that stays behind for hours is visible
// in application logs, not discoverable only by querying the database. The
// interval rate-limits it: a /readyz probe firing every few seconds must
// not spam this line on every single evaluation once the age threshold is
// crossed.
const (
	eligibilityProofPendingWarnAge      = 60 * time.Minute
	eligibilityProofPendingWarnInterval = 5 * time.Minute
)

// shouldWarnProofPending is eligibilityProofPendingWarner's decision, pulled
// out as a pure function (health/now/lastWarnedAt in, bool out) so the rate
// limit's boundary is table-testable without touching the mutex or slog.
func shouldWarnProofPending(health postgresstore.EligibilityProjectionHealth, now, lastWarnedAt time.Time) bool {
	if health.ProofPending <= 0 || health.OldestProofPending.IsZero() {
		return false
	}
	if now.Sub(health.OldestProofPending) <= eligibilityProofPendingWarnAge {
		return false
	}
	return lastWarnedAt.IsZero() || now.Sub(lastWarnedAt) >= eligibilityProofPendingWarnInterval
}

// eligibilityProofPendingWarner holds the one piece of state
// shouldWarnProofPending's rate limit needs across readiness evaluations.
// One instance is shared for the life of the process (see its construction
// in buildProductionRuntime); the mutex guards concurrent /readyz requests.
type eligibilityProofPendingWarner struct {
	mu     sync.Mutex
	lastAt time.Time
}

func (w *eligibilityProofPendingWarner) warnIfStale(health postgresstore.EligibilityProjectionHealth, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !shouldWarnProofPending(health, now, w.lastAt) {
		return
	}
	w.lastAt = now
	slog.Warn("invoice eligibility projection has balance-proof-pending jobs waiting a long time",
		"proof_pending", health.ProofPending,
		"oldest_proof_pending_age", now.Sub(health.OldestProofPending).Round(time.Second).String())
}

// readySourceStreamAllowedReasons and notReadySourceStreamAllowedReasons are
// the exact non-fatal reason combinations validateSourceRuntimeReadiness
// tolerates for, respectively, a Ready and a not-Ready required source
// stream -- everything else is treated as a genuine problem and fails
// /readyz. XM-INV-AGENT-RESTART-GRACE part A added ECONOMIC_RESCAN_ACTIVE
// alongside the pre-existing EVENTS_PENDING tolerance.
var (
	readySourceStreamAllowedReasons    = map[string]bool{"ECONOMIC_RESCAN_ACTIVE": true}
	notReadySourceStreamAllowedReasons = map[string]bool{"EVENTS_PENDING": true, "ECONOMIC_RESCAN_ACTIVE": true}
)

func reasonsWithinSet(reasons []string, allowed map[string]bool) bool {
	for _, reason := range reasons {
		if !allowed[reason] {
			return false
		}
	}
	return true
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func validateSourceRuntimeReadiness(report postgresstore.SourceHealthReport) error {
	requiredStreams := map[string]struct{}{"payments": {}, "identities": {}, "usage": {}, "credits": {}, "balances": {}}
	type sourceState struct {
		sourceType domain.SourceType
		streams    map[string]struct{}
	}
	sources := make(map[string]sourceState)
	enabledTypes := make(map[domain.SourceType]bool)
	strictReady := true
	for _, item := range report.Items {
		if !item.SourceEnabled {
			continue
		}
		if item.SourceType != domain.SourceSub2API && item.SourceType != domain.SourceNewAPI {
			return errors.New("required source streams contain an unsupported source type")
		}
		if strings.TrimSpace(item.SourceInstanceID) == "" {
			return errors.New("required source stream identity is missing")
		}
		if _, ok := requiredStreams[item.StreamID]; !ok {
			return errors.New("required source streams contain an unsupported stream")
		}
		state, ok := sources[item.SourceInstanceID]
		if !ok {
			state = sourceState{sourceType: item.SourceType, streams: make(map[string]struct{}, len(requiredStreams))}
		} else if state.sourceType != item.SourceType {
			return errors.New("required source identity changed type")
		}
		if _, duplicate := state.streams[item.StreamID]; duplicate {
			return errors.New("required source stream is duplicated")
		}
		state.streams[item.StreamID] = struct{}{}
		sources[item.SourceInstanceID] = state
		enabledTypes[item.SourceType] = true
		if !item.Ready {
			strictReady = false
		}
		switch {
		case item.Ready:
			// XM-INV-AGENT-RESTART-GRACE part A: Ready may now carry the
			// non-fatal ECONOMIC_RESCAN_ACTIVE reason (an active, bounded
			// rescan downgraded from ECONOMIC_WATERMARK_STALE -- see
			// evaluateSourceStreamHealth), and nothing else.
			if item.PendingEvents != 0 || item.DeadEvents != 0 || !reasonsWithinSet(item.Reasons, readySourceStreamAllowedReasons) {
				return errors.New("ready source stream has inconsistent health evidence")
			}
		case item.DeadEvents != 0:
			return errors.New("required source stream contains dead events")
		case item.PendingEvents <= 0 || !containsReason(item.Reasons, "EVENTS_PENDING") ||
			!reasonsWithinSet(item.Reasons, notReadySourceStreamAllowedReasons):
			return errors.New("required source streams are stale, blocked, dead, or version-mismatched")
		}
		// The preceding SourceIngestHealth check already bounds the oldest
		// pending event to 15 minutes. This exact pending-only state (whether
		// or not it is joined by an active, bounded rescan) must not remove
		// an otherwise healthy API from service; irreversible invoice
		// operations still use the stricter per-stream freshness policy.
	}
	if !enabledTypes[domain.SourceSub2API] || !enabledTypes[domain.SourceNewAPI] {
		strictReady = false
		if report.Ready != strictReady {
			return errors.New("source health report readiness is inconsistent")
		}
		return errors.New("both Sub2API and New API sources must be enabled")
	}
	for _, state := range sources {
		if len(state.streams) != len(requiredStreams) {
			return errors.New("required source stream set is incomplete")
		}
	}
	if report.Ready != strictReady {
		return errors.New("source health report readiness is inconsistent")
	}
	return nil
}

func validateSourceIngestRuntimeReadiness(health postgresstore.SourceIngestHealth, now time.Time) error {
	if now.IsZero() {
		return errors.New("source ingestion readiness clock is unavailable")
	}
	if health.Dead > 0 {
		return errors.New("source ingestion contains dead events")
	}
	if health.Pending == 0 {
		if !health.OldestPending.IsZero() {
			return errors.New("source ingestion pending evidence is inconsistent")
		}
		return nil
	}
	if health.OldestPending.IsZero() || health.OldestPending.After(now.Add(5*time.Minute)) || now.Sub(health.OldestPending) > 15*time.Minute {
		return errors.New("source ingestion processing is unhealthy")
	}
	return nil
}

func validateEligibilityPolicyStart(configured string, databaseValue time.Time) error {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return errors.New("ELIGIBILITY_START_AT is required in production")
	}
	parsed, err := time.Parse(time.RFC3339, configured)
	if err != nil {
		return fmt.Errorf("ELIGIBILITY_START_AT must be RFC3339: %w", err)
	}
	if !parsed.UTC().Equal(adminsettings.RequiredEligibilityStartAt) {
		return errors.New("ELIGIBILITY_START_AT does not match the immutable release policy")
	}
	if databaseValue.IsZero() || !databaseValue.UTC().Equal(parsed.UTC()) {
		return errors.New("deployment eligibility start does not match immutable database policy")
	}
	return nil
}

func verifyEligibilitySourceManifests(ctx context.Context, store *postgresstore.Store) error {
	var invalid int
	err := store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM source_cutover_manifests scm
		JOIN source_instances si ON si.id=scm.source_instance_id
		CROSS JOIN invoice_eligibility_policy policy
		WHERE policy.singleton_id=1 AND (
			scm.cutover_at>=policy.eligibility_start_at
			OR scm.database_clock>=policy.eligibility_start_at
			OR (si.source_type='sub2api' AND scm.projection_contract<>'sub2api-economic-v4')
			OR (si.source_type='newapi' AND scm.projection_contract<>'newapi-economic-rc25-v4')
			OR scm.projection_contract='fixture-v3'
			OR scm.signing_key_id='fixture'
			OR scm.source_runtime_version='fixture-runtime'
		)`).Scan(&invalid)
	if err != nil {
		return fmt.Errorf("verify source manifests against invoice eligibility policy: %w", err)
	}
	if invalid != 0 {
		return errors.New("source manifest cutover/contract violates immutable invoice eligibility policy")
	}
	return nil
}

func verifyRuntimeDatabasePrivileges(ctx context.Context, store *postgresstore.Store) error {
	var canInsertAudit, canUpdateAudit, canDeleteAudit, canUpdateLogoutEvent, canDeleteLogoutEvent, canCreateSchema, canReadMigrations, canTemporary bool
	var canUpdateSourceEvent, canUpdateSourceBatch, canUpdatePaymentReview, canUpdateUsageFact, canUpdateCreditFact, canUpdateBalanceFact bool
	var canUpdateConsumptionAllocation bool
	// console_assertion_nonces is the single-use judge for console assertions.
	// The store only ever INSERTs (ON CONFLICT DO NOTHING, which needs no
	// UPDATE privilege) and DELETEs expired rows, so UPDATE here would only
	// ever be an attacker's or a bug's tool for rewriting a claim that has
	// already been redeemed. deploy/postgres/harden-runtime-role.sql revokes
	// it; this assertion is what stops the process from running against a
	// database where that job was never replayed (XM-INV-CONSOLE-ASSERT-
	// ADMIN-ROLE's release notes and PRODUCTION-RUNBOOK section 4.1 spell out
	// the replay, which roll-forward deliberately does not do for you).
	var canUpdateConsoleNonce bool
	var canReadEligibilityPolicy, canWriteEligibilityPolicy bool
	var superuser, createDB, createRole, replication, bypassRLS, inherit, memberOfRole bool
	var connectionLimit int
	err := store.Pool().QueryRow(ctx, `
		SELECT has_table_privilege(current_user,'audit_events','INSERT'),
		       has_table_privilege(current_user,'audit_events','UPDATE'),
		       has_table_privilege(current_user,'audit_events','DELETE'),
		       has_table_privilege(current_user,'oidc_backchannel_logout_events','UPDATE'),
		       has_table_privilege(current_user,'oidc_backchannel_logout_events','DELETE'),
		       has_table_privilege(current_user,'source_events','UPDATE'),
		       has_table_privilege(current_user,'source_ingest_batches','UPDATE'),
		       has_table_privilege(current_user,'payment_candidate_reviews','UPDATE'),
		       has_table_privilege(current_user,'source_usage_events','UPDATE'),
		       has_table_privilege(current_user,'source_credit_events','UPDATE'),
		       has_table_privilege(current_user,'balance_reconciliation_checkpoints','UPDATE'),
		       has_table_privilege(current_user,'consumption_allocations','UPDATE'),
		       has_table_privilege(current_user,'console_assertion_nonces','UPDATE'),
		       has_table_privilege(current_user,'invoice_eligibility_policy','SELECT'),
		       has_table_privilege(current_user,'invoice_eligibility_policy','UPDATE'),
		       has_schema_privilege(current_user,'public','CREATE'),
		       has_table_privilege(current_user,'schema_migrations','SELECT'),
		       has_database_privilege(current_user,current_database(),'TEMP'),
		       r.rolsuper,r.rolcreatedb,r.rolcreaterole,r.rolreplication,
		       r.rolbypassrls,r.rolinherit,r.rolconnlimit,
		       EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid)
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(
		&canInsertAudit, &canUpdateAudit, &canDeleteAudit, &canUpdateLogoutEvent, &canDeleteLogoutEvent,
		&canUpdateSourceEvent, &canUpdateSourceBatch, &canUpdatePaymentReview, &canUpdateUsageFact,
		&canUpdateCreditFact, &canUpdateBalanceFact, &canUpdateConsumptionAllocation,
		&canUpdateConsoleNonce,
		&canReadEligibilityPolicy, &canWriteEligibilityPolicy, &canCreateSchema,
		&canReadMigrations, &canTemporary, &superuser, &createDB, &createRole,
		&replication, &bypassRLS, &inherit, &connectionLimit, &memberOfRole)
	if err != nil {
		return fmt.Errorf("inspect runtime database privileges: %w", err)
	}
	if !canInsertAudit || canUpdateAudit || canDeleteAudit || canUpdateLogoutEvent || canDeleteLogoutEvent ||
		canUpdateSourceEvent || canUpdateSourceBatch || canUpdatePaymentReview || canUpdateUsageFact ||
		canUpdateCreditFact || canUpdateBalanceFact || canUpdateConsumptionAllocation ||
		canUpdateConsoleNonce ||
		!canReadEligibilityPolicy || canWriteEligibilityPolicy ||
		canCreateSchema || !canReadMigrations || canTemporary ||
		superuser || createDB || createRole || replication || bypassRLS || inherit || connectionLimit != 20 || memberOfRole {
		return errors.New("runtime database role is over-privileged or missing required append/read grants")
	}
	return nil
}

// currentUserLoader is the minimal dependency loadSessionUser needs.
// provisionUserDeps embeds it since provisioning always ends by loading the
// resulting session user.
type currentUserLoader interface {
	GetCurrentUser(ctx context.Context, userID string) (application.CurrentUser, error)
}

// provisionUserDeps groups the application-service dependencies
// provisionPlatformOrOIDCUser needs, narrowed to an interface so its
// claim-vs-create branching (XM-INV-AUTOLOGIN: see
// docs/handoffs/XM-INV-AUTOLOGIN.md) is unit-tested against fakes in
// runtime_test.go instead of requiring a live PostgreSQL connection.
// *application.Service satisfies this in production.
type provisionUserDeps interface {
	currentUserLoader
	GetExternalAccountBySourceUser(ctx context.Context, sourceInstanceID, externalUserID string) (postgresstore.ExternalAccountRecord, error)
	ClaimPlatformIdentity(ctx context.Context, userID, platform, platformUserID string) (storedPlatform, storedPlatformUserID string, err error)
	EnsureUser(ctx context.Context, identity application.OIDCIdentity) (postgresstore.UserRecord, error)
	BindExternalAccount(ctx context.Context, record postgresstore.ExternalAccountRecord) (postgresstore.ExternalAccountRecord, error)
	WakeSourceAccountFacts(ctx context.Context, sourceInstanceID, externalUserID string) error
}

// provisionPlatformOrOIDCUser resolves the local invoice_user for a verified
// login -- OIDC administrator or platform-password -- and returns the
// session-ready projection of it.
//
// For a platform-password principal, it first checks whether a
// source-projection pipeline (BindExternalAccountFromSource) already owns
// this external account, bound to a pre-existing invoice_user with real
// funding lots already attached, before its owner ever tried a
// platform-password login. If so, it claims that existing identity instead
// of creating a second, orphaned one. Without this check,
// identity.ResolveOrCreate below always resolves/creates a *different*
// invoice_user (keyed by the platform's own issuer/subject pair), and
// BindExternalAccount's UPSERT then fails closed with domain.ErrForbidden
// because the external account already belongs to someone else's
// invoice_user_id -- this was the root cause of a production 403
// ("当前账号没有执行此操作的权限") on every platform-password login for an
// account a source-projection pipeline had already touched. See
// docs/handoffs/XM-INV-AUTOLOGIN.md for the full writeup.
func provisionPlatformOrOIDCUser(ctx context.Context, deps provisionUserDeps, identity auth.IdentityStore, principal auth.Principal, requestID string, sourceInstanceIDs map[auth.Platform]string) (httpapi.SessionUser, error) {
	actorType := "oidc"
	reason := "OIDC login synchronized"
	var sourceInstanceID string
	if principal.Platform != "" {
		actorType = "platform"
		reason = "platform password login synchronized"
		sourceInstanceID = sourceInstanceIDs[principal.Platform]
		if sourceInstanceID == "" {
			return httpapi.SessionUser{}, fmt.Errorf("no enabled source instance configured for platform %q", principal.Platform)
		}
	}
	auditCtx := application.WithAuditActor(ctx, postgresstore.AuditActor{Type: actorType, ID: principal.IdentityHash(), RequestID: requestID, Reason: reason})

	if principal.Platform != "" {
		existing, lookupErr := deps.GetExternalAccountBySourceUser(ctx, sourceInstanceID, principal.PlatformUserID)
		if lookupErr != nil && !errors.Is(lookupErr, domain.ErrNotFound) {
			return httpapi.SessionUser{}, fmt.Errorf("look up existing external account binding: %w", lookupErr)
		}
		if lookupErr == nil {
			// Found: this external_accounts row IS the ownership proof -- it
			// was written either by the signed source projection or by an
			// earlier verified password bind, and the platform just verified
			// this login's password. Land the session on its invoice_user.
			//
			// ClaimPlatformIdentity backfills invoice_users.platform/
			// platform_user_id only while both are still NULL and never
			// overwrites. A user whose accounts on BOTH platforms are bound
			// to one SSO invoice_user (the identity projection does exactly
			// that) claims first with one platform; the other platform's
			// stored pair then legitimately differs from this login's --
			// that is a multi-platform identity, not a conflict, so it is
			// NOT a rejection (the RC57 production canary rejected exactly
			// this). The columns record the first password-login platform
			// only; per-login platform identity lives on the session row.
			if _, _, claimErr := deps.ClaimPlatformIdentity(auditCtx, existing.PrincipalID, string(principal.Platform), principal.PlatformUserID); claimErr != nil {
				return httpapi.SessionUser{}, fmt.Errorf("claim existing platform identity: %w", claimErr)
			}
			// The claim path never re-binds, so it must fire the binding wake
			// itself: an account bound before the wake existed (or whose bind
			// predates RC56) still has its signed cutover row and usage facts
			// parked on this dependency, and parked events are never swept.
			if wakeErr := deps.WakeSourceAccountFacts(auditCtx, sourceInstanceID, principal.PlatformUserID); wakeErr != nil {
				return httpapi.SessionUser{}, fmt.Errorf("wake parked source facts for claimed binding: %w", wakeErr)
			}
			return loadSessionUser(auditCtx, deps, existing.PrincipalID, true)
		}
		// Not found: fall through to the create-or-find path below exactly
		// like a first-ever login (OIDC or platform) always has.
	}

	resolved, resolveErr := identity.ResolveOrCreate(ctx, principal, requestID)
	if resolveErr != nil {
		return httpapi.SessionUser{}, resolveErr
	}
	record, ensureErr := deps.EnsureUser(auditCtx, application.OIDCIdentity{Issuer: principal.Issuer, Subject: principal.Subject, Email: principal.Email, EmailVerified: principal.EmailVerified, Status: "active"})
	if ensureErr != nil || record.ID != resolved.UserID {
		if ensureErr == nil {
			ensureErr = errors.New("identity persistence mismatch")
		}
		return httpapi.SessionUser{}, ensureErr
	}
	if principal.Platform != "" {
		// Platform password login IS the account-ownership proof (the
		// platform's own login endpoint just verified it): bind the matching
		// external account immediately instead of requiring the separate
		// out-of-band challenge flow in binding.go, so the user's funding
		// lots resolve on first login. The claim path above already handled
		// an external account that was bound to a different, pre-existing
		// identity, so this UPSERT only ever runs for a genuinely new
		// binding.
		if _, bindErr := deps.BindExternalAccount(auditCtx, postgresstore.ExternalAccountRecord{
			PrincipalID: record.ID, SourceInstanceID: sourceInstanceID,
			ExternalUserID: principal.PlatformUserID, BindingMethod: "platform_password_login",
			BindingStatus: "verified",
		}); bindErr != nil {
			return httpapi.SessionUser{}, fmt.Errorf("bind platform external account: %w", bindErr)
		}
	}
	return loadSessionUser(auditCtx, deps, record.ID, false)
}

// loadSessionUser loads the invoice_user row and layers the request-scoped
// claimed marker onto it (see SessionUser.Claimed's doc comment -- log-line
// observability only, never serialized to a response). claimed is only ever
// true on the claim-path return above; the captured platform display name
// lives on the session row instead (migration 0017), not here.
func loadSessionUser(ctx context.Context, service currentUserLoader, userID string, claimed bool) (httpapi.SessionUser, error) {
	user, err := service.GetCurrentUser(ctx, userID)
	if err != nil {
		return httpapi.SessionUser{}, err
	}
	return httpapi.SessionUser{
		ID: user.ID, Claimed: claimed, Email: user.Email, EmailVerified: user.EmailVerified,
		CanonicalIssuer: user.OIDCIssuer, CanonicalSubject: user.OIDCSubject,
	}, nil
}

// loadPlatformSourceInstanceIDs resolves the one enabled source_instances row
// per platform. Platform-password login auto-binds a fresh identity to this
// exact ID (see the ProvisionUser closure above) instead of introducing a
// second, independently-configured ID that could drift out of sync with it.
func loadPlatformSourceInstanceIDs(ctx context.Context, store *postgresstore.Store) (map[auth.Platform]string, error) {
	sub2apiID, err := store.GetEnabledSourceInstanceID(ctx, "sub2api")
	if err != nil {
		return nil, fmt.Errorf("resolve Sub2API source instance: %w", err)
	}
	newapiID, err := store.GetEnabledSourceInstanceID(ctx, "newapi")
	if err != nil {
		return nil, fmt.Errorf("resolve New API source instance: %w", err)
	}
	return map[auth.Platform]string{auth.PlatformSub2API: sub2apiID, auth.PlatformNewAPI: newapiID}, nil
}

// buildPlatformLogin wires the Sub2API/New API password-login verifiers used
// by the user-facing login page (CR-0004). It shares session issuance with
// OIDC through productionAuth; it does not replace the administrator OIDC
// login, which remains unchanged.
func buildPlatformLogin(productionAuth *httpapi.ProductionAuth) (*httpapi.PlatformLogin, error) {
	sub2apiBaseURL := strings.TrimRight(env("SUB2API_LOGIN_BASE_URL", "https://api.solov.cc"), "/")
	newapiBaseURL := strings.TrimRight(env("NEWAPI_LOGIN_BASE_URL", "https://xm.solov.cc"), "/")
	timeout, err := boundedDurationEnv("PLATFORM_LOGIN_TIMEOUT", "10s", time.Second, time.Minute)
	if err != nil {
		return nil, err
	}
	maxAttempts, err := boundedInt64Env("PLATFORM_LOGIN_MAX_ATTEMPTS", 8, 3, 50)
	if err != nil {
		return nil, err
	}
	lockoutWindow, err := boundedDurationEnv("PLATFORM_LOGIN_LOCKOUT_WINDOW", "15m", time.Minute, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	sub2apiAuth, err := auth.NewSub2APIAuthenticator(auth.PlatformEndpointConfig{BaseURL: sub2apiBaseURL, Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("Sub2API login authenticator: %w", err)
	}
	newapiAuth, err := auth.NewNewAPIAuthenticator(auth.PlatformEndpointConfig{BaseURL: newapiBaseURL, Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("New API login authenticator: %w", err)
	}
	return &httpapi.PlatformLogin{
		Authenticators: map[auth.Platform]auth.PlatformAuthenticator{
			auth.PlatformSub2API: sub2apiAuth,
			auth.PlatformNewAPI:  newapiAuth,
		},
		Origins: map[auth.Platform]string{
			auth.PlatformSub2API: sub2apiBaseURL,
			auth.PlatformNewAPI:  newapiBaseURL,
		},
		RateLimiter: auth.NewLoginRateLimiter(int(maxAttempts), lockoutWindow),
		// Fixed, not env-configured: this is internal bookkeeping (which
		// platform issued a temp_token during auto-detect), not an operator
		// tuning knob, and its lifetime only needs to comfortably outlast a
		// real 2FA round-trip.
		PendingTwoFA: auth.NewPendingTwoFAPlatforms(10 * time.Minute),
		Auth:         productionAuth,
	}, nil
}

func exactHTTPSOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("PUBLIC_ORIGIN must be one exact HTTPS origin")
	}
	return parsed.String(), nil
}

func boundedDurationEnv(name, fallback string, minimum, maximum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		raw = fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", name, minimum, maximum)
	}
	return value, nil
}

func validateSourceFreshnessBudget(economicWatermarkMaxAge, economicSafetyDelay, pollInterval time.Duration) error {
	if economicWatermarkMaxAge < economicSafetyDelay+sourceingest.DefaultMaximumSkew+2*pollInterval {
		return errors.New("SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS must cover SOURCE_ECONOMIC_SAFETY_DELAY, receiver clock skew and two SOURCE_POLL_INTERVAL windows")
	}
	return nil
}

func boundedInt64Env(name string, fallback, minimum, maximum int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func readSecretLine(path string, maxBytes int64) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("secret file path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 || int64(len(body)) > maxBytes {
		return "", errors.New("secret file has invalid size")
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("secret file must contain exactly one non-empty line")
	}
	return string(body), nil
}

func readBoundedConfigFile(path string, maxBytes int64) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("configuration file path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || int64(len(body)) > maxBytes || bytes.IndexByte(body, 0) >= 0 {
		return nil, errors.New("configuration file has invalid size or content")
	}
	return body, nil
}
