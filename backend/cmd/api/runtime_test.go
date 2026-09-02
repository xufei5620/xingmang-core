package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

// fakeIdentityStore is a scriptable auth.IdentityStore double for
// provisionPlatformOrOIDCUser's tests: it never talks to a real PostgreSQL
// connection.
type fakeIdentityStore struct {
	calls   int
	resolve func(auth.Principal) (auth.InvoiceIdentity, error)
}

func (f *fakeIdentityStore) ResolveOrCreate(_ context.Context, principal auth.Principal, _ string) (auth.InvoiceIdentity, error) {
	f.calls++
	return f.resolve(principal)
}

// fakeProvisionDeps is a scriptable provisionUserDeps double. Call counts on
// each method let tests assert not just the HTTP-visible outcome but which
// code path actually ran -- in particular, that the claim path never touches
// ResolveOrCreate/EnsureUser/BindExternalAccount (XM-INV-AUTOLOGIN's fix for
// the production 403: see docs/handoffs/XM-INV-AUTOLOGIN.md).
type fakeProvisionDeps struct {
	getExternalAccountCalls int
	getExternalAccount      func(sourceInstanceID, externalUserID string) (postgresstore.ExternalAccountRecord, error)
	claimCalls              int
	claim                   func(userID, platform, platformUserID string) (string, string, error)
	ensureUserCalls         int
	ensureUser              func(application.OIDCIdentity) (postgresstore.UserRecord, error)
	bindCalls               int
	bind                    func(postgresstore.ExternalAccountRecord) (postgresstore.ExternalAccountRecord, error)
	getCurrentUserCalls     int
	getCurrentUser          func(userID string) (application.CurrentUser, error)
	wakeCalls               int
	wakeSourceInstanceID    string
	wakeExternalUserID      string
}

func (f *fakeProvisionDeps) WakeSourceAccountFacts(_ context.Context, sourceInstanceID, externalUserID string) error {
	f.wakeCalls++
	f.wakeSourceInstanceID = sourceInstanceID
	f.wakeExternalUserID = externalUserID
	return nil
}

func (f *fakeProvisionDeps) GetExternalAccountBySourceUser(_ context.Context, sourceInstanceID, externalUserID string) (postgresstore.ExternalAccountRecord, error) {
	f.getExternalAccountCalls++
	return f.getExternalAccount(sourceInstanceID, externalUserID)
}

func (f *fakeProvisionDeps) ClaimPlatformIdentity(_ context.Context, userID, platform, platformUserID string) (string, string, error) {
	f.claimCalls++
	return f.claim(userID, platform, platformUserID)
}

func (f *fakeProvisionDeps) EnsureUser(_ context.Context, identity application.OIDCIdentity) (postgresstore.UserRecord, error) {
	f.ensureUserCalls++
	return f.ensureUser(identity)
}

func (f *fakeProvisionDeps) BindExternalAccount(_ context.Context, record postgresstore.ExternalAccountRecord) (postgresstore.ExternalAccountRecord, error) {
	f.bindCalls++
	return f.bind(record)
}

func (f *fakeProvisionDeps) GetCurrentUser(_ context.Context, userID string) (application.CurrentUser, error) {
	f.getCurrentUserCalls++
	return f.getCurrentUser(userID)
}

func TestProvisionPlatformOrOIDCUserClaimsExistingProjectedIdentity(t *testing.T) {
	// Mirrors the production incident: a source-projection pipeline already
	// bound (sub2api-main, "1113") to a pre-existing invoice_user before its
	// owner ever tried a platform-password login.
	const projectedUserID = "projected-user-0001"
	deps := &fakeProvisionDeps{
		getExternalAccount: func(sourceInstanceID, externalUserID string) (postgresstore.ExternalAccountRecord, error) {
			if sourceInstanceID != "sub2api-main" || externalUserID != "1113" {
				t.Fatalf("unexpected lookup: %s/%s", sourceInstanceID, externalUserID)
			}
			return postgresstore.ExternalAccountRecord{PrincipalID: projectedUserID, BindingMethod: "source_signed_oidc_projection"}, nil
		},
		claim: func(userID, platform, platformUserID string) (string, string, error) {
			if userID != projectedUserID || platform != "sub2api" || platformUserID != "1113" {
				t.Fatalf("unexpected claim args: %s/%s/%s", userID, platform, platformUserID)
			}
			return "sub2api", "1113", nil
		},
		getCurrentUser: func(userID string) (application.CurrentUser, error) {
			return application.CurrentUser{ID: userID, Email: "user@example.com", EmailVerified: true}, nil
		},
	}
	identity := &fakeIdentityStore{}
	principal := auth.Principal{Issuer: "https://api.solov.cc", Subject: "1113", Platform: auth.PlatformSub2API, PlatformUserID: "1113"}
	sourceInstanceIDs := map[auth.Platform]string{auth.PlatformSub2API: "sub2api-main"}

	user, err := provisionPlatformOrOIDCUser(context.Background(), deps, identity, principal, "req-1", sourceInstanceIDs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != projectedUserID {
		t.Fatalf("session landed on %q, want the pre-existing projected user %q", user.ID, projectedUserID)
	}
	if identity.calls != 0 {
		t.Fatalf("ResolveOrCreate must not run on the claim path: calls=%d", identity.calls)
	}
	if deps.ensureUserCalls != 0 || deps.bindCalls != 0 {
		t.Fatalf("EnsureUser/BindExternalAccount must not run on the claim path: ensure=%d bind=%d", deps.ensureUserCalls, deps.bindCalls)
	}
	if deps.claimCalls != 1 {
		t.Fatalf("expected exactly one ClaimPlatformIdentity call, got %d", deps.claimCalls)
	}
	if deps.getCurrentUserCalls != 1 {
		t.Fatalf("expected exactly one GetCurrentUser call, got %d", deps.getCurrentUserCalls)
	}
	// XM-INV-OBS-BUNDLE: the claim-path marker rides the returned SessionUser
	// purely for completeLogin's observability log line (the captured
	// platform username itself now lives on the session row -- see
	// auth/postgres_integration_test.go -- not on SessionUser).
	if !user.Claimed {
		t.Fatal("Claimed must be true on the claim path")
	}
}

func TestProvisionPlatformOrOIDCUserCreatesNewIdentityWhenNoExistingBinding(t *testing.T) {
	// Regression: a genuinely first-ever platform-password login (no
	// source-projection binding exists yet) must still go through the
	// original resolve-then-bind flow unchanged.
	const newUserID = "new-user-0002"
	deps := &fakeProvisionDeps{
		getExternalAccount: func(string, string) (postgresstore.ExternalAccountRecord, error) {
			return postgresstore.ExternalAccountRecord{}, domain.ErrNotFound
		},
		ensureUser: func(identity application.OIDCIdentity) (postgresstore.UserRecord, error) {
			if identity.Issuer != "https://xm.solov.cc" || identity.Subject != "48" {
				t.Fatalf("unexpected EnsureUser identity: %+v", identity)
			}
			return postgresstore.UserRecord{ID: newUserID}, nil
		},
		bind: func(record postgresstore.ExternalAccountRecord) (postgresstore.ExternalAccountRecord, error) {
			if record.PrincipalID != newUserID || record.SourceInstanceID != "newapi-main" || record.ExternalUserID != "48" {
				t.Fatalf("unexpected BindExternalAccount record: %+v", record)
			}
			return record, nil
		},
		getCurrentUser: func(userID string) (application.CurrentUser, error) {
			return application.CurrentUser{ID: userID}, nil
		},
	}
	identity := &fakeIdentityStore{resolve: func(auth.Principal) (auth.InvoiceIdentity, error) {
		return auth.InvoiceIdentity{UserID: newUserID}, nil
	}}
	principal := auth.Principal{Issuer: "https://xm.solov.cc", Subject: "48", Platform: auth.PlatformNewAPI, PlatformUserID: "48"}
	sourceInstanceIDs := map[auth.Platform]string{auth.PlatformNewAPI: "newapi-main"}

	user, err := provisionPlatformOrOIDCUser(context.Background(), deps, identity, principal, "req-2", sourceInstanceIDs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != newUserID {
		t.Fatalf("user.ID=%q want %q", user.ID, newUserID)
	}
	if identity.calls != 1 {
		t.Fatalf("expected exactly one ResolveOrCreate call, got %d", identity.calls)
	}
	if deps.ensureUserCalls != 1 || deps.bindCalls != 1 {
		t.Fatalf("expected exactly one EnsureUser and one BindExternalAccount call, got ensure=%d bind=%d", deps.ensureUserCalls, deps.bindCalls)
	}
	if deps.claimCalls != 0 {
		t.Fatalf("ClaimPlatformIdentity must not run when there is no existing binding: calls=%d", deps.claimCalls)
	}
	if user.Claimed {
		t.Fatal("Claimed must be false on the create path")
	}
}

func TestProvisionPlatformOrOIDCUserAcceptsMultiPlatformIdentity(t *testing.T) {
	// The external_accounts binding row is the ownership proof. A user whose
	// accounts on BOTH platforms are bound to one SSO invoice_user claims
	// first with one platform; the stored platform columns then differ from
	// the other platform's login. That is a multi-platform identity, not a
	// takeover -- the login must land on the bound invoice_user (the RC57
	// production canary was rejected here; RC58 accepts it).
	const sharedUserID = "sso-user-0003"
	deps := &fakeProvisionDeps{
		getExternalAccount: func(string, string) (postgresstore.ExternalAccountRecord, error) {
			return postgresstore.ExternalAccountRecord{PrincipalID: sharedUserID}, nil
		},
		claim: func(string, string, string) (string, string, error) {
			// Stored pair belongs to the OTHER platform: never overwritten,
			// and no longer a rejection.
			return "sub2api", "1113", nil
		},
		getCurrentUser: func(userID string) (application.CurrentUser, error) {
			if userID != sharedUserID {
				return application.CurrentUser{}, errors.New("unexpected user id")
			}
			return application.CurrentUser{ID: sharedUserID, Status: "active",
				OIDCIssuer: "https://auth.solov.example/realms/solov", OIDCSubject: "sso-subject"}, nil
		},
	}
	identity := &fakeIdentityStore{}
	principal := auth.Principal{Issuer: "https://xm.solov.cc", Subject: "48", Platform: auth.PlatformNewAPI, PlatformUserID: "48"}
	sourceInstanceIDs := map[auth.Platform]string{auth.PlatformNewAPI: "newapi-main"}

	user, err := provisionPlatformOrOIDCUser(context.Background(), deps, identity, principal, "req-3", sourceInstanceIDs)
	if err != nil {
		t.Fatalf("multi-platform identity login must succeed: %v", err)
	}
	if user.ID != sharedUserID {
		t.Fatalf("session must land on the bound invoice_user: got %q want %q", user.ID, sharedUserID)
	}
	if user.CanonicalIssuer != "https://auth.solov.example/realms/solov" || user.CanonicalSubject != "sso-subject" {
		t.Fatalf("canonical pair must be exposed for session issuance, got %q/%q", user.CanonicalIssuer, user.CanonicalSubject)
	}
	if identity.calls != 0 {
		t.Fatalf("ResolveOrCreate must not run on the claim path: calls=%d", identity.calls)
	}
	if deps.ensureUserCalls != 0 || deps.bindCalls != 0 {
		t.Fatalf("EnsureUser/BindExternalAccount must not run on the claim path: ensure=%d bind=%d", deps.ensureUserCalls, deps.bindCalls)
	}
	if deps.wakeCalls != 1 || deps.wakeSourceInstanceID != "newapi-main" || deps.wakeExternalUserID != "48" {
		t.Fatalf("claim login must fire the binding wake for its own account: calls=%d source=%q user=%q",
			deps.wakeCalls, deps.wakeSourceInstanceID, deps.wakeExternalUserID)
	}
}

func TestProvisionPlatformOrOIDCUserOIDCPrincipalSkipsClaimPathEntirely(t *testing.T) {
	const oidcUserID = "oidc-admin-0004"
	deps := &fakeProvisionDeps{
		getExternalAccount: func(string, string) (postgresstore.ExternalAccountRecord, error) {
			t.Fatal("an OIDC principal must never look up a platform external account binding")
			return postgresstore.ExternalAccountRecord{}, nil
		},
		claim: func(string, string, string) (string, string, error) {
			t.Fatal("an OIDC principal must never call ClaimPlatformIdentity")
			return "", "", nil
		},
		ensureUser: func(application.OIDCIdentity) (postgresstore.UserRecord, error) {
			return postgresstore.UserRecord{ID: oidcUserID}, nil
		},
		bind: func(postgresstore.ExternalAccountRecord) (postgresstore.ExternalAccountRecord, error) {
			t.Fatal("an OIDC principal must never call BindExternalAccount")
			return postgresstore.ExternalAccountRecord{}, nil
		},
		getCurrentUser: func(userID string) (application.CurrentUser, error) {
			return application.CurrentUser{ID: userID}, nil
		},
	}
	identity := &fakeIdentityStore{resolve: func(auth.Principal) (auth.InvoiceIdentity, error) {
		return auth.InvoiceIdentity{UserID: oidcUserID}, nil
	}}
	principal := auth.Principal{Issuer: "https://keycloak.example/realms/invoice", Subject: "admin-sub"}

	user, err := provisionPlatformOrOIDCUser(context.Background(), deps, identity, principal, "req-4", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != oidcUserID {
		t.Fatalf("user.ID=%q want %q", user.ID, oidcUserID)
	}
	if identity.calls != 1 {
		t.Fatalf("expected exactly one ResolveOrCreate call, got %d", identity.calls)
	}
}

func TestProvisionPlatformOrOIDCUserRejectsMissingSourceInstanceConfig(t *testing.T) {
	deps := &fakeProvisionDeps{}
	identity := &fakeIdentityStore{}
	principal := auth.Principal{Issuer: "https://api.solov.cc", Subject: "1113", Platform: auth.PlatformSub2API, PlatformUserID: "1113"}

	if _, err := provisionPlatformOrOIDCUser(context.Background(), deps, identity, principal, "req-5", map[auth.Platform]string{}); err == nil {
		t.Fatal("expected an error when no source instance is configured for the platform")
	}
	if deps.getExternalAccountCalls != 0 {
		t.Fatalf("must fail before ever looking up an external account: calls=%d", deps.getExternalAccountCalls)
	}
}

func TestProvisionPlatformOrOIDCUserPropagatesExternalAccountLookupFailure(t *testing.T) {
	// A lookup error other than "not found" (e.g. a database error) must fail
	// closed rather than silently falling through to the create path -- that
	// would risk creating a second, orphaned identity underneath a lookup
	// that might actually have found a binding.
	deps := &fakeProvisionDeps{
		getExternalAccount: func(string, string) (postgresstore.ExternalAccountRecord, error) {
			return postgresstore.ExternalAccountRecord{}, context.DeadlineExceeded
		},
	}
	identity := &fakeIdentityStore{}
	principal := auth.Principal{Issuer: "https://api.solov.cc", Subject: "1113", Platform: auth.PlatformSub2API, PlatformUserID: "1113"}
	sourceInstanceIDs := map[auth.Platform]string{auth.PlatformSub2API: "sub2api-main"}

	if _, err := provisionPlatformOrOIDCUser(context.Background(), deps, identity, principal, "req-6", sourceInstanceIDs); err == nil {
		t.Fatal("expected the lookup failure to propagate")
	}
	if identity.calls != 0 {
		t.Fatalf("must not fall through to ResolveOrCreate after an unexplained lookup failure: calls=%d", identity.calls)
	}
}

type onceWorkerFunc func(context.Context) (int, error)

func (f onceWorkerFunc) RunOnce(ctx context.Context) (int, error) { return f(ctx) }

func TestRunWorkerLogsProcessedCountAlongsideError(t *testing.T) {
	// XM-INV-OBS-BUNDLE: runWorker's failure log previously named the worker
	// but not how much work it completed before failing. That count matters
	// most for eligibility-projection: ProcessEligibilityProjectionJobs
	// (postgresstore/consumption.go) keeps processing the rest of its batch
	// after the first failure and only ever returns that first error, so
	// "processed" is the only place the log can show whether the batch was
	// mostly fine (a handful of failures) or largely stuck.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	worker := onceWorkerFunc(func(context.Context) (int, error) {
		calls++
		cancel() // let runWorker return after exactly one RunOnce call
		return 3, errors.New("eligibility projection job failed: boom")
	})

	runWorker(ctx, workerSpec{Name: "eligibility-projection", Worker: worker})

	if calls != 1 {
		t.Fatalf("expected exactly one RunOnce call, got %d", calls)
	}
	logText := logs.String()
	for _, want := range []string{"background worker failed", "worker=eligibility-projection", "processed=3", "boom"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log missing %q: %s", want, logText)
		}
	}
}

// testConsoleAssertionKeyJSON mirrors auth's unexported wire shape for
// contracts/auth/console-assertion-keyring.v1.json (see console_assertion.go)
// -- redefined here since the real type is unexported and this package
// cannot import it, only build an equivalent JSON document by hand.
type testConsoleAssertionKeyJSON struct {
	KeyID       string `json:"key_id"`
	Algorithm   string `json:"algorithm"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	Purpose     string `json:"purpose"`
	Protocol    string `json:"protocol"`
	ValidFrom   string `json:"valid_from"`
	ValidUntil  string `json:"valid_until"`
}

// writeTestConsoleAssertionKeyringFile writes a single valid, freshly
// generated Ed25519 trust record to a temp file and returns its path. Every
// keypair used here is generated at test-run time, never a literal committed
// key (matching this codebase's existing console-assertion test discipline).
func writeTestConsoleAssertionKeyringFile(t *testing.T, dir string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test ed25519 key: %v", err)
	}
	sum := sha256.Sum256(pub)
	raw, err := json.Marshal([]testConsoleAssertionKeyJSON{{
		KeyID:       "2026-09-test",
		Algorithm:   "Ed25519",
		PublicKey:   base64.StdEncoding.EncodeToString(pub),
		Fingerprint: hex.EncodeToString(sum[:]),
		Purpose:     "console_admin_assertion_signing",
		Protocol:    "xm-console-assertion-v1",
		ValidFrom:   "2020-01-01T00:00:00Z",
		ValidUntil:  "2099-01-01T00:00:00Z",
	}})
	if err != nil {
		t.Fatalf("marshal test keyring: %v", err)
	}
	path := filepath.Join(dir, "console-assertion-keyring.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write test keyring file: %v", err)
	}
	return path
}

func TestLoadConsoleAssertionRuntimeConfigDisabledNeverTouchesEnvOrFilesystem(t *testing.T) {
	// docker-compose.prod.yml's CONSOLE_ASSERTION_KEYRING_FILE bind mount
	// (XM-INV-CONSOLE-ASSERT-DEPLOY) may point at an absent or empty host
	// path for every release where this flag stays false. Deliberately set
	// garbage/absent values for all three variables this function would need
	// if it actually read them, so this test fails loudly if the "disabled
	// means untouched" guard is ever removed or weakened.
	t.Setenv("CONSOLE_ASSERTION_ISSUER", "")
	t.Setenv("CONSOLE_ASSERTION_AUDIENCE", "")
	t.Setenv("CONSOLE_ASSERTION_KEYS_FILE", filepath.Join(t.TempDir(), "does-not-exist.json"))

	keyring, cfg, err := loadConsoleAssertionRuntimeConfig(false)
	if err != nil {
		t.Fatalf("disabled config load must never fail, got: %v", err)
	}
	if keyring != nil {
		t.Fatalf("disabled config load must return a nil keyring, got %v", keyring)
	}
	if cfg != (auth.ConsoleAssertionConfig{}) {
		t.Fatalf("disabled config load must return a zero-value config, got %+v", cfg)
	}
}

func TestLoadConsoleAssertionRuntimeConfigEnabledLoadsValidKeyring(t *testing.T) {
	keysPath := writeTestConsoleAssertionKeyringFile(t, t.TempDir())
	t.Setenv("CONSOLE_ASSERTION_ISSUER", "https://console.example.test")
	t.Setenv("CONSOLE_ASSERTION_AUDIENCE", "xingmang-console-assertion-v1")
	t.Setenv("CONSOLE_ASSERTION_KEYS_FILE", keysPath)

	keyring, cfg, err := loadConsoleAssertionRuntimeConfig(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keyring.Len() != 1 {
		t.Fatalf("expected exactly one trusted key, got %d", keyring.Len())
	}
	if cfg.Issuer != "https://console.example.test" || cfg.Audience != "xingmang-console-assertion-v1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConsoleAssertionRuntimeConfigEnabledRequiresKeysFilePath(t *testing.T) {
	t.Setenv("CONSOLE_ASSERTION_ISSUER", "https://console.example.test")
	t.Setenv("CONSOLE_ASSERTION_AUDIENCE", "xingmang-console-assertion-v1")
	t.Setenv("CONSOLE_ASSERTION_KEYS_FILE", "")

	if _, _, err := loadConsoleAssertionRuntimeConfig(true); err == nil ||
		!strings.Contains(err.Error(), "CONSOLE_ASSERTION_KEYS_FILE is required") {
		t.Fatalf("expected a required-keys-file error, got: %v", err)
	}
}

func TestLoadConsoleAssertionRuntimeConfigEnabledFailsClosedOnEmptyKeysFile(t *testing.T) {
	// Simulates the exact Docker bind-mount footgun the compose change
	// guards against: an empty placeholder file at the mounted path
	// (deploy/roll-forward.sh creates one only when nothing exists yet)
	// must never be silently accepted as "zero trusted keys" once the flag
	// is actually turned on -- an operator who flips
	// CONSOLE_ASSERTION_ENABLED before the real reviewed manifest is
	// installed must see a loud startup failure, not a verifier that
	// silently rejects every assertion forever.
	emptyPath := filepath.Join(t.TempDir(), "console-assertion-keyring.json")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty test keys file: %v", err)
	}
	t.Setenv("CONSOLE_ASSERTION_ISSUER", "https://console.example.test")
	t.Setenv("CONSOLE_ASSERTION_AUDIENCE", "xingmang-console-assertion-v1")
	t.Setenv("CONSOLE_ASSERTION_KEYS_FILE", emptyPath)

	if _, _, err := loadConsoleAssertionRuntimeConfig(true); err == nil {
		t.Fatal("expected an empty keys file to fail closed when the flag is enabled")
	}
}
