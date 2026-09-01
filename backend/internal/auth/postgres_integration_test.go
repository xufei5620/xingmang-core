package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/securefields"
)

// testSessionKeyring provides PostgresSessionStore's field-encryption
// keyring for these integration tests -- same key-material shape as
// client_test.go's inline keyrings elsewhere in this package.
func testSessionKeyring() securefields.Keyring {
	return securefields.Keyring{
		CurrentKeyID:   "k1",
		EncryptionKeys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")},
		IndexKey:       []byte("abcdef0123456789abcdef0123456789"),
	}
}

func TestPostgresOIDCFlowSessionIdentityBindingAndAudit(t *testing.T) {
	databaseURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	identityStore := NewPostgresIdentityStore(pool)
	principal := Principal{Issuer: "https://identity.example", Subject: "subject-1", ProviderSID: "provider-session-1", Roles: []string{"invoice-admin"}, ACR: "urn:invoice:mfa", AMR: []string{"pwd", "otp"}}
	now := time.Now().UTC().Truncate(time.Millisecond)
	principal.AuthTime = now
	identity, err := identityStore.ResolveOrCreate(ctx, principal, "req-identity")
	if err != nil {
		t.Fatal(err)
	}
	again, err := identityStore.ResolveOrCreate(ctx, Principal{Issuer: principal.Issuer, Subject: principal.Subject, Email: "same@example.com", EmailVerified: true}, "req-identity-2")
	if err != nil || again.UserID != identity.UserID {
		t.Fatalf("stable issuer/subject identity failed: again=%+v err=%v", again, err)
	}
	var users int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM invoice_users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("identity rows=%d err=%v", users, err)
	}

	flowStore := NewPostgresFlowStore(pool)
	flow := AuthorizationFlow{
		StateHash: sha256Hex("state"), NonceHash: sha256Hex("nonce"), BrowserBindingHash: sha256Hex("browser"),
		CodeVerifierCiphertext: []byte("encrypted-code-verifier-value-1234567890"), CodeVerifierKeyVersion: "key-1",
		Purpose: FlowLogin, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err = flowStore.Create(ctx, flow); err != nil {
		t.Fatal(err)
	}
	consumed, err := flowStore.Consume(ctx, flow.StateHash, flow.BrowserBindingHash, now.Add(time.Second))
	if err != nil || consumed.ConsumedAt == nil {
		t.Fatalf("consume=%+v err=%v", consumed, err)
	}
	if _, err = flowStore.Consume(ctx, flow.StateHash, flow.BrowserBindingHash, now.Add(2*time.Second)); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("flow replay error=%v", err)
	}

	audit := NewPostgresSecurityAuditSink(pool)
	sessionStore := NewPostgresSessionStore(pool, testSessionKeyring())
	manager, err := NewSessionManager(sessionStore, SessionConfig{IdleTTL: time.Hour, AbsoluteTTL: 4 * time.Hour}, audit)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	mfaAt := now
	issued, err := manager.Issue(ctx, IssueSessionInput{UserID: identity.UserID, Principal: principal, MFAAt: &mfaAt, RequestID: "req-session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Authenticate(ctx, issued.Token, ClientBinding{}); err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now.Add(time.Minute) }
	var successes atomic.Int64
	var rotated SessionCredentials
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			fresh := now.Add(time.Minute)
			candidate, rotateErr := manager.Rotate(ctx, RotateSessionInput{Token: issued.Token, ExpectedSessionID: issued.Session.ID, Principal: principal, MFAAt: &fresh, RequestID: "req-rotate"})
			if rotateErr == nil {
				successes.Add(1)
				mutex.Lock()
				rotated = candidate
				mutex.Unlock()
			} else if !errors.Is(rotateErr, ErrSessionInvalid) {
				t.Errorf("unexpected rotate error: %v", rotateErr)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent rotation successes=%d", successes.Load())
	}
	if _, err = manager.Authenticate(ctx, issued.Token, ClientBinding{}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("old PostgreSQL session remained active")
	}
	if _, err = manager.Authenticate(ctx, rotated.Token, ClientBinding{}); err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now.Add(2 * time.Minute) }
	secondRotationMFA := now.Add(2 * time.Minute)
	activeLeaf, err := manager.Rotate(ctx, RotateSessionInput{
		Token: rotated.Token, ExpectedSessionID: rotated.Session.ID,
		Principal: principal, MFAAt: &secondRotationMFA, RequestID: "req-rotate-again",
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := sessionStore.DeleteExpired(ctx, now.Add(3*time.Minute))
	if err != nil || deleted != 0 {
		t.Fatalf("cleanup with active PostgreSQL descendant deleted=%d err=%v", deleted, err)
	}
	if _, err = manager.Authenticate(ctx, activeLeaf.Token, ClientBinding{}); err != nil {
		t.Fatal(err)
	}
	backchannel, err := NewBackchannelLogoutService(NewPostgresBackchannelLogoutRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	logoutEvent := VerifiedBackchannelLogout{Issuer: principal.Issuer, Subject: principal.Subject, SessionID: principal.ProviderSID, TokenID: "logout-jti-1", IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	type logoutCall struct {
		result BackchannelLogoutResult
		err    error
	}
	logoutCalls := make(chan logoutCall, 2)
	logoutStart := make(chan struct{})
	for index := range 2 {
		go func() {
			<-logoutStart
			result, callErr := backchannel.Process(ctx, logoutEvent, BackchannelLogoutActor{RequestID: fmt.Sprintf("req-backchannel-%d", index)})
			logoutCalls <- logoutCall{result: result, err: callErr}
		}()
	}
	close(logoutStart)
	firstCall, secondCall := <-logoutCalls, <-logoutCalls
	close(logoutCalls)
	if firstCall.err != nil || secondCall.err != nil || firstCall.result.EventID == "" || firstCall.result.EventID != secondCall.result.EventID ||
		firstCall.result.RevokedSessions != 1 || secondCall.result.RevokedSessions != 1 || firstCall.result.Replay == secondCall.result.Replay {
		t.Fatalf("concurrent logout calls=%+v %+v", firstCall, secondCall)
	}
	if _, err = manager.Authenticate(ctx, activeLeaf.Token, ClientBinding{}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("back-channel sid logout did not revoke the active session")
	}
	replay, err := backchannel.Process(ctx, logoutEvent, BackchannelLogoutActor{RequestID: "req-backchannel-replay"})
	if err != nil || !replay.Replay || replay.EventID != firstCall.result.EventID || replay.RevokedSessions != firstCall.result.RevokedSessions {
		t.Fatalf("logout replay=%+v err=%v", replay, err)
	}
	deleted, err = sessionStore.DeleteExpired(ctx, now.Add(4*time.Minute))
	if err != nil || deleted != 3 {
		t.Fatalf("stale PostgreSQL rotation family deleted=%d err=%v", deleted, err)
	}
	var remainingFamilySessions int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE family_id=$1`, activeLeaf.Session.FamilyID).Scan(&remainingFamilySessions); err != nil || remainingFamilySessions != 0 {
		t.Fatalf("remaining PostgreSQL rotation family sessions=%d err=%v", remainingFamilySessions, err)
	}
	principal.ProviderSID = "provider-session-2"
	second, err := manager.Issue(ctx, IssueSessionInput{UserID: identity.UserID, Principal: principal, MFAAt: &mfaAt, RequestID: "req-session-2"})
	if err != nil {
		t.Fatal(err)
	}
	subjectLogout := VerifiedBackchannelLogout{Issuer: principal.Issuer, Subject: principal.Subject, TokenID: "logout-jti-2", IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	subjectResult, err := backchannel.Process(ctx, subjectLogout, BackchannelLogoutActor{RequestID: "req-backchannel-2"})
	if err != nil || subjectResult.RevokedSessions != 1 {
		t.Fatalf("subject logout result=%+v err=%v", subjectResult, err)
	}
	if _, err = manager.Authenticate(ctx, second.Token, ClientBinding{}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("back-channel subject logout did not revoke the active session")
	}

	if _, err = pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name) VALUES('10000000-0000-4000-8000-000000000001','sub2api','fixture')`); err != nil {
		t.Fatal(err)
	}
	bindingStore := NewPostgresBindingProofStore(pool)
	bindingService, err := NewBindingService(bindingStore, fixedBindingVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	bindingService.now = func() time.Time { return now }
	challenge, err := bindingService.Begin(ctx, BeginBindingInput{
		InvoiceUserID: identity.UserID, SourceInstanceID: "10000000-0000-4000-8000-000000000001",
		ExternalUserID: "upstream-user-7", Method: BindingSourceSignedChallenge, RequestID: "req-bind-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	proof := BindingProof{
		ChallengeID: challenge.Challenge.ID, InvoiceUserID: identity.UserID,
		SourceInstanceID: challenge.Challenge.SourceInstanceID, ExternalUserID: challenge.Challenge.ExternalUserID,
		Method: challenge.Challenge.Method, Challenge: challenge.Secret, Evidence: []byte("source-signed-evidence"),
		SourceRevisionHash: "revision-1", RequestID: "req-bind-verify",
	}
	verified, err := bindingService.Verify(ctx, proof)
	if err != nil || verified.EvidenceHash == "" {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	if _, err = bindingService.Verify(ctx, proof); !errors.Is(err, ErrInvalidBindingProof) {
		t.Fatalf("binding proof replay error=%v", err)
	}

	var audits int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action IN ('auth.identity.create','auth.session.issue','auth.session.rotate','auth.backchannel_logout.process','external_account.binding_proof.verify')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 8 {
		t.Fatalf("security audit rows=%d want 8", audits)
	}
	var plaintextTokens int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE token_hash=$1 OR csrf_hash=$2`, second.Token, second.CSRFToken).Scan(&plaintextTokens); err != nil || plaintextTokens != 0 {
		t.Fatalf("raw secrets reached session table: count=%d err=%v", plaintextTokens, err)
	}
}

type fixedBindingVerifier struct{}

func (fixedBindingVerifier) VerifyBindingProof(_ context.Context, challenge BindingChallenge, proof BindingProof) (VerifiedBindingProof, error) {
	if string(proof.Evidence) != "source-signed-evidence" {
		return VerifiedBindingProof{}, errors.New("bad signature")
	}
	return VerifiedBindingProof{
		ChallengeID: challenge.ID, InvoiceUserID: challenge.InvoiceUserID,
		SourceInstanceID: challenge.SourceInstanceID, ExternalUserID: challenge.ExternalUserID,
		Method: challenge.Method, EvidenceHash: sha256Hex(string(proof.Evidence)), SourceRevisionHash: proof.SourceRevisionHash,
	}, nil
}

// TestPostgresPlatformSessionWithoutRolesOrAMR reproduces the first real
// platform-password login in production after the identity-claim fix: a
// platform principal carries no OIDC roles/AMR, and auth_sessions.roles/amr
// are TEXT[] NOT NULL with an explicit column list, so a nil []string
// (encoded by pgx as SQL NULL) broke session issuance with SQLSTATE 23502.
// Issue and Rotate must both store empty arrays instead.
func TestPostgresPlatformSessionWithoutRolesOrAMR(t *testing.T) {
	databaseURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	identityStore := NewPostgresIdentityStore(pool)
	now := time.Now().UTC().Truncate(time.Millisecond)
	// Exactly what httpapi's platform-login handler builds: no Roles, no ACR,
	// no AMR, no ProviderSID, no MFA -- only the platform identity.
	principal := Principal{
		Issuer: "https://api.solov.example", Subject: "1113",
		Platform: PlatformSub2API, PlatformUserID: "1113", AuthTime: now,
	}
	identity, err := identityStore.ResolveOrCreate(ctx, principal, "req-platform-identity")
	if err != nil {
		t.Fatal(err)
	}

	audit := NewPostgresSecurityAuditSink(pool)
	sessionStore := NewPostgresSessionStore(pool, testSessionKeyring())
	manager, err := NewSessionManager(sessionStore, SessionConfig{IdleTTL: time.Hour, AbsoluteTTL: 4 * time.Hour}, audit)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	issued, err := manager.Issue(ctx, IssueSessionInput{UserID: identity.UserID, Principal: principal, RequestID: "req-platform-session"})
	if err != nil {
		t.Fatalf("platform session issuance must not require OIDC roles/AMR: %v", err)
	}
	var storedRoles, storedAMR []string
	if err = pool.QueryRow(ctx, `SELECT roles,amr FROM auth_sessions WHERE id=$1`, issued.Session.ID).
		Scan(&storedRoles, &storedAMR); err != nil {
		t.Fatal(err)
	}
	if storedRoles == nil || len(storedRoles) != 0 || storedAMR == nil || len(storedAMR) != 0 {
		t.Fatalf("roles/amr must be stored as empty arrays, got roles=%#v amr=%#v", storedRoles, storedAMR)
	}
	if _, err = manager.Authenticate(ctx, issued.Token, ClientBinding{}); err != nil {
		t.Fatal(err)
	}

	// Rotation constructs a second Session literal; it must hold the same
	// invariant for a roles-free platform principal.
	manager.now = func() time.Time { return now.Add(time.Minute) }
	rotated, err := manager.Rotate(ctx, RotateSessionInput{Token: issued.Token, ExpectedSessionID: issued.Session.ID, Principal: principal, RequestID: "req-platform-rotate"})
	if err != nil {
		t.Fatalf("platform session rotation must not require OIDC roles/AMR: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT roles,amr FROM auth_sessions WHERE id=$1`, rotated.Session.ID).
		Scan(&storedRoles, &storedAMR); err != nil {
		t.Fatal(err)
	}
	if storedRoles == nil || len(storedRoles) != 0 || storedAMR == nil || len(storedAMR) != 0 {
		t.Fatalf("rotated roles/amr must be stored as empty arrays, got roles=%#v amr=%#v", storedRoles, storedAMR)
	}
}

// TestPostgresSessionDisplayNameEncryptedRotatedAndBackwardCompatible covers
// migration 0017 (XM-INV-OBS-BUNDLE follow-up: persist the platform's
// captured username on the session row, encrypted, rather than on
// invoice_users): Issue() encrypts and stores it, Authenticate() decrypts it
// back, Rotate() carries it forward onto the new row, a principal with no
// captured name stores NULL (mirrors the roles/amr empty-array pattern
// above, extended to this column), and a session row that predates the
// migration -- display_name_ciphertext genuinely absent from the INSERT,
// not just empty -- loads with an empty DisplayName instead of erroring.
func TestPostgresSessionDisplayNameEncryptedRotatedAndBackwardCompatible(t *testing.T) {
	databaseURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	identityStore := NewPostgresIdentityStore(pool)
	now := time.Now().UTC().Truncate(time.Millisecond)
	principal := Principal{
		Issuer: "https://api.solov.example", Subject: "2224",
		Platform: PlatformSub2API, PlatformUserID: "2224", AuthTime: now,
		DisplayName: "xufei",
	}
	identity, err := identityStore.ResolveOrCreate(ctx, principal, "req-displayname-identity")
	if err != nil {
		t.Fatal(err)
	}

	audit := NewPostgresSecurityAuditSink(pool)
	sessionStore := NewPostgresSessionStore(pool, testSessionKeyring())
	manager, err := NewSessionManager(sessionStore, SessionConfig{IdleTTL: time.Hour, AbsoluteTTL: 4 * time.Hour}, audit)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	issued, err := manager.Issue(ctx, IssueSessionInput{UserID: identity.UserID, Principal: principal, RequestID: "req-displayname-session"})
	if err != nil {
		t.Fatal(err)
	}

	// The stored column must be actual ciphertext -- never the plaintext
	// name, and long enough that it plainly isn't just base64 of "xufei".
	var storedCiphertext []byte
	if err = pool.QueryRow(ctx, `SELECT display_name_ciphertext FROM auth_sessions WHERE id=$1`, issued.Session.ID).
		Scan(&storedCiphertext); err != nil {
		t.Fatal(err)
	}
	if len(storedCiphertext) < 16 || strings.Contains(string(storedCiphertext), "xufei") {
		t.Fatalf("display_name_ciphertext does not look encrypted: %q", storedCiphertext)
	}

	// Authenticate (used by GET /api/v1/auth/session) must decrypt it back.
	authenticated, err := manager.Authenticate(ctx, issued.Token, ClientBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.DisplayName != "xufei" {
		t.Fatalf("Authenticate must decrypt the captured display name, got %q", authenticated.DisplayName)
	}

	// Rotate must carry it forward onto the new row (mirrors sessionStatus's
	// CSRF-rotation branch, which rebuilds Principal.DisplayName from the
	// just-authenticated session).
	manager.now = func() time.Time { return now.Add(time.Minute) }
	rotatePrincipal := principal
	rotatePrincipal.DisplayName = authenticated.DisplayName
	rotated, err := manager.Rotate(ctx, RotateSessionInput{Token: issued.Token, ExpectedSessionID: issued.Session.ID, Principal: rotatePrincipal, RequestID: "req-displayname-rotate"})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Session.DisplayName != "xufei" {
		t.Fatalf("Rotate must carry the display name forward, got %q", rotated.Session.DisplayName)
	}
	reauthenticated, err := manager.Authenticate(ctx, rotated.Token, ClientBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if reauthenticated.DisplayName != "xufei" {
		t.Fatalf("post-rotation Authenticate must still decrypt the display name, got %q", reauthenticated.DisplayName)
	}

	// A principal with no captured name (OIDC, or a platform response that
	// genuinely returned none) must store NULL, not an encrypted empty
	// string -- same "don't encrypt nothing" convention as
	// application/crypto.go's encryptProfile.
	noNamePrincipal := Principal{Issuer: "https://api.solov.example", Subject: "2225", Platform: PlatformSub2API, PlatformUserID: "2225", AuthTime: now}
	noNameIdentity, err := identityStore.ResolveOrCreate(ctx, noNamePrincipal, "req-noname-identity")
	if err != nil {
		t.Fatal(err)
	}
	noNameIssued, err := manager.Issue(ctx, IssueSessionInput{UserID: noNameIdentity.UserID, Principal: noNamePrincipal, RequestID: "req-noname-session"})
	if err != nil {
		t.Fatal(err)
	}
	var noNameCiphertext []byte
	if err = pool.QueryRow(ctx, `SELECT display_name_ciphertext FROM auth_sessions WHERE id=$1`, noNameIssued.Session.ID).
		Scan(&noNameCiphertext); err != nil {
		t.Fatal(err)
	}
	if noNameCiphertext != nil {
		t.Fatalf("a principal with no captured name must store NULL, got %q", noNameCiphertext)
	}
	noNameAuthenticated, err := manager.Authenticate(ctx, noNameIssued.Token, ClientBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if noNameAuthenticated.DisplayName != "" {
		t.Fatalf("a NULL ciphertext must decrypt to empty, got %q", noNameAuthenticated.DisplayName)
	}

	// Backward compatibility: a session row inserted the way pre-migration
	// code would have -- display_name_ciphertext entirely absent from the
	// column list, not merely NULL-valued -- must still load cleanly via
	// AuthenticateAndTouch instead of erroring.
	// validOpaqueToken requires a 43-128 char token; pad well past the
	// minimum so this reads unambiguously as a test fixture, not a real one.
	preMigrationToken := "pre-migration-token-" + strings.Repeat("a", 30)
	preMigrationTokenHash := sha256Hex(preMigrationToken)
	if _, err = pool.Exec(ctx, `
		INSERT INTO auth_sessions(
			id,family_id,invoice_user_id,token_hash,csrf_hash,roles,acr,amr,
			created_at,last_seen_at,idle_expires_at,absolute_expires_at
		) VALUES($1,$2,$3,$4,$5,'{}','',
			'{}',$6::timestamptz,$6::timestamptz,$6::timestamptz+interval '1 hour',$6::timestamptz+interval '4 hours')`,
		randomUUIDv4(), randomUUIDv4(), identity.UserID, preMigrationTokenHash, sha256Hex("pre-migration-csrf"), now); err != nil {
		t.Fatal(err)
	}
	preMigration, err := manager.Authenticate(ctx, preMigrationToken, ClientBinding{})
	if err != nil {
		t.Fatalf("a pre-migration session (NULL display_name_ciphertext) must still authenticate: %v", err)
	}
	if preMigration.DisplayName != "" {
		t.Fatalf("a pre-migration session must decrypt to an empty display name, got %q", preMigration.DisplayName)
	}
}
