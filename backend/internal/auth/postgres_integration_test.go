package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
)

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
	manager, err := NewSessionManager(NewPostgresSessionStore(pool), SessionConfig{IdleTTL: time.Hour, AbsoluteTTL: 4 * time.Hour}, audit)
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
	if _, err = manager.Authenticate(ctx, rotated.Token, ClientBinding{}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("back-channel sid logout did not revoke the active session")
	}
	replay, err := backchannel.Process(ctx, logoutEvent, BackchannelLogoutActor{RequestID: "req-backchannel-replay"})
	if err != nil || !replay.Replay || replay.EventID != firstCall.result.EventID || replay.RevokedSessions != firstCall.result.RevokedSessions {
		t.Fatalf("logout replay=%+v err=%v", replay, err)
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
	if audits != 7 {
		t.Fatalf("security audit rows=%d want 7", audits)
	}
	var plaintextTokens int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE token_hash=$1 OR csrf_hash=$2`, issued.Token, issued.CSRFToken).Scan(&plaintextTokens); err != nil || plaintextTokens != 0 {
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
