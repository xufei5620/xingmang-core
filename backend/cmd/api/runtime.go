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
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/document"
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
			slog.Error("background worker failed", "worker", spec.Name, "error", err)
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
	settingsRepo := adminsettings.NewMemoryRepository(adminsettings.Settings{IssuerName: "待配置开票主体", ServiceItem: adminsettings.FixedServiceItem, MinimumRequestMinor: adminsettings.MinimumMinor, EligibilityStartAt: adminsettings.RequiredEligibilityStartAt, SMTPHost: "smtp.qq.com", SMTPPort: 587, SMTPFrom: "not-configured@qq.com", SMTPFromName: "发票中心", SMTPStartTLS: true, AdminCIDRs: adminCIDRs, Revision: 1, UpdatedBy: "bootstrap", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
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
	if strings.TrimSpace(settings.IssuerName) == "" || settings.IssuerName == "待配置开票主体" {
		return appRuntime{}, application.ErrIssuerNotConfigured
	}
	if err = validateEligibilityPolicyStart(os.Getenv("ELIGIBILITY_START_AT"), settings.EligibilityStartAt); err != nil {
		return appRuntime{}, err
	}
	if err = verifyEligibilitySourceManifests(ctx, store); err != nil {
		return appRuntime{}, err
	}
	paymentsMaxAge, err := boundedDurationEnv("SOURCE_PAYMENTS_MAX_STALENESS", "5m", 30*time.Second, 24*time.Hour)
	if err != nil {
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
		EmailTemplateVersion: "invoice-ready-v1",
		SourcePaymentsMaxAge: paymentsMaxAge, SourceIdentitiesMaxAge: identitiesMaxAge,
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
	flowStore := auth.NewPostgresFlowStore(store.Pool())
	oidcClient, err := auth.NewOIDCClient(ctx, oidcConfig, flowStore, auth.SecureFieldsFlowProtector{Keyring: keyring})
	if err != nil {
		return appRuntime{}, err
	}
	sessionStore := auth.NewPostgresSessionStore(store.Pool())
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
	productionAuth := &httpapi.ProductionAuth{
		OIDC: oidcClient, Sessions: sessions, BindingHasher: bindingHasher,
		CSRF: csrf, Admin: adminPolicy, Logout: oidcClient, BackchannelLogout: backchannelLogout,
		ProvisionUser: func(callbackCtx context.Context, principal auth.Principal, requestID string) (httpapi.SessionUser, error) {
			identity, resolveErr := identityStore.ResolveOrCreate(callbackCtx, principal, requestID)
			if resolveErr != nil {
				return httpapi.SessionUser{}, resolveErr
			}
			auditCtx := application.WithAuditActor(callbackCtx, postgresstore.AuditActor{Type: "oidc", ID: principal.IdentityHash(), RequestID: requestID, Reason: "OIDC login synchronized"})
			record, ensureErr := appService.EnsureUser(auditCtx, application.OIDCIdentity{Issuer: principal.Issuer, Subject: principal.Subject, Email: principal.Email, EmailVerified: principal.EmailVerified, Status: "active"})
			if ensureErr != nil || record.ID != identity.UserID {
				if ensureErr == nil {
					ensureErr = errors.New("OIDC identity persistence mismatch")
				}
				return httpapi.SessionUser{}, ensureErr
			}
			return loadSessionUser(auditCtx, appService, record.ID)
		},
		LoadUser: func(loadCtx context.Context, userID string) (httpapi.SessionUser, error) {
			return loadSessionUser(loadCtx, appService, userID)
		},
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
	receiver := &sourceingest.Receiver{Trust: trust, Acceptor: appService, ProxyCIDRs: ingestProxyNetworks}
	breakGlass, err := loadBreakGlassCIDRs(authMode)
	if err != nil {
		return appRuntime{}, err
	}
	trustedProxies := csvEnv("TRUSTED_PROXY_CIDRS", "")
	if len(trustedProxies) == 0 {
		return appRuntime{}, errors.New("production trusted proxy CIDRs are required")
	}
	api, err := httpapi.NewWithConfig(appService, httpapi.Config{
		AuthMode: "oidc", SourceMode: "agent", AdminIPAllowlist: settings.AdminCIDRs,
		BreakGlassCIDRs: breakGlass, TrustedProxies: trustedProxies,
		DocumentStore: documentStore, AdminSettings: settingsService, ProductionAuth: productionAuth,
		SMTPTestSender: mailer.SettingsSender{Source: settingsService}, PublicOrigin: publicOrigin,
		SourceIngest: receiver,
		Readiness: func(readyCtx context.Context) error {
			if pingErr := store.Pool().Ping(readyCtx); pingErr != nil {
				return pingErr
			}
			if _, settingsErr := settingsService.Get(readyCtx); settingsErr != nil {
				return settingsErr
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
			health, healthErr := appService.SourceIngestHealth(readyCtx)
			if healthErr != nil {
				return healthErr
			}
			if health.Dead > 0 || !health.OldestPending.IsZero() && time.Since(health.OldestPending) > 15*time.Minute {
				return errors.New("source ingestion processing is unhealthy")
			}
			eligibilityHealth, healthErr := store.EligibilityProjectionHealth(readyCtx)
			if healthErr != nil {
				return healthErr
			}
			if eligibilityHealth.Failed > 0 ||
				!eligibilityHealth.OldestPending.IsZero() && time.Since(eligibilityHealth.OldestPending) > 15*time.Minute {
				return errors.New("invoice eligibility projection is unhealthy")
			}
			report, healthErr := appService.SourceHealth(readyCtx)
			if healthErr != nil || !report.Ready {
				if healthErr != nil {
					return healthErr
				}
				return errors.New("required source streams are stale, unprocessed, or version-mismatched")
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
			return int(flows + sessionCount), sessionErr
		})},
	}
	closeOnError = false
	return appRuntime{API: api, AuthMode: "oidc", SourceMode: "agent", Workers: workers, close: store.Close}, nil
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
			OR (si.source_type='sub2api' AND scm.projection_contract<>'sub2api-economic-v3')
			OR (si.source_type='newapi' AND scm.projection_contract<>'newapi-economic-rc25-v3')
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
		&canReadEligibilityPolicy, &canWriteEligibilityPolicy, &canCreateSchema,
		&canReadMigrations, &canTemporary, &superuser, &createDB, &createRole,
		&replication, &bypassRLS, &inherit, &connectionLimit, &memberOfRole)
	if err != nil {
		return fmt.Errorf("inspect runtime database privileges: %w", err)
	}
	if !canInsertAudit || canUpdateAudit || canDeleteAudit || canUpdateLogoutEvent || canDeleteLogoutEvent ||
		canUpdateSourceEvent || canUpdateSourceBatch || canUpdatePaymentReview || canUpdateUsageFact ||
		canUpdateCreditFact || canUpdateBalanceFact || canUpdateConsumptionAllocation ||
		!canReadEligibilityPolicy || canWriteEligibilityPolicy ||
		canCreateSchema || !canReadMigrations || canTemporary ||
		superuser || createDB || createRole || replication || bypassRLS || inherit || connectionLimit != 20 || memberOfRole {
		return errors.New("runtime database role is over-privileged or missing required append/read grants")
	}
	return nil
}

func loadSessionUser(ctx context.Context, service *application.Service, userID string) (httpapi.SessionUser, error) {
	user, err := service.GetCurrentUser(ctx, userID)
	if err != nil {
		return httpapi.SessionUser{}, err
	}
	return httpapi.SessionUser{ID: user.ID, Email: user.Email, EmailVerified: user.EmailVerified}, nil
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
