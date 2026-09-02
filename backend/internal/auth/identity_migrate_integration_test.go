package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/testdb"
)

// setupIdentityMigrateTestDB resets the shared per-worktree test database to
// a freshly migrated, empty schema -- same pattern as this package's other
// *_integration_test.go files (see postgres_integration_test.go).
func setupIdentityMigrateTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	return pool
}

// insertTestInvoiceUser writes an invoice_users row directly (bypassing
// ResolveOrCreate) so tests can freely control status/email/created_at.
func insertTestInvoiceUser(t *testing.T, pool *pgxpool.Pool, issuer, subject, status string, emailCiphertext []byte, createdAt time.Time) string {
	t.Helper()
	id := randomUUIDv4()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,status,email_ciphertext,email_verified,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$7)`,
		id, issuer, subject, status, emailCiphertext, len(emailCiphertext) > 0, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const (
	testFromIssuer  = "https://auth.solov.cc/realms/solov"
	testToIssuer    = "https://console.solov.cc"
	testFromSubject = "10000000-0000-4000-8000-000000000001"
	testToSubject   = "20000000-0000-4000-8000-000000000002"
	testOperatorID  = "30000000-0000-4000-8000-000000000003"
)

func baseMigrationInput() IdentityMigrationInput {
	return IdentityMigrationInput{
		FromIssuer: testFromIssuer, FromSubject: testFromSubject,
		ToIssuer: testToIssuer, ToSubject: testToSubject,
	}
}

func TestMigrateOIDCBindingDryRunRefusesWhenNoRowsMatch(t *testing.T) {
	pool := setupIdentityMigrateTestDB(t)
	if _, err := MigrateOIDCBinding(context.Background(), pool, testSessionKeyring(), baseMigrationInput()); err == nil {
		t.Fatal("expected refusal when no invoice_users row matches --from-issuer/--from-subject")
	}
}

func TestMigrateOIDCBindingRefusesWhenRowNotActive(t *testing.T) {
	pool := setupIdentityMigrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	insertTestInvoiceUser(t, pool, testFromIssuer, testFromSubject, "suspended", nil, now)

	if _, err := MigrateOIDCBinding(context.Background(), pool, testSessionKeyring(), baseMigrationInput()); err == nil {
		t.Fatal("expected refusal for a non-active row (dry run must validate this too)")
	}

	in := baseMigrationInput()
	in.Apply = true
	in.OperatorID = testOperatorID
	if _, err := MigrateOIDCBinding(context.Background(), pool, testSessionKeyring(), in); err == nil {
		t.Fatal("expected apply to refuse a non-active row")
	}
}

func TestMigrateOIDCBindingRefusesWhenTargetExistsOnAnotherRow(t *testing.T) {
	pool := setupIdentityMigrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	insertTestInvoiceUser(t, pool, testFromIssuer, testFromSubject, "active", nil, now)
	insertTestInvoiceUser(t, pool, testToIssuer, testToSubject, "active", nil, now)

	if _, err := MigrateOIDCBinding(context.Background(), pool, testSessionKeyring(), baseMigrationInput()); err == nil {
		t.Fatal("expected refusal when --to-issuer/--to-subject already exists on another row")
	}
}

// TestMigrateOIDCBindingApplyHappyPath covers the full apply lifecycle: the
// matched row's issuer/subject and email ciphertext are rewritten, only the
// user's LIVE sessions are invalidated (an already-revoked one is left
// untouched), and the migration audit row is written with hashed
// before/after identities.
func TestMigrateOIDCBindingApplyHappyPath(t *testing.T) {
	pool := setupIdentityMigrateTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	keyring := testSessionKeyring()

	emailCiphertext, err := keyring.Encrypt([]byte("ab@example.com"), userEmailAADForMigration(testFromIssuer, testFromSubject))
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTestInvoiceUser(t, pool, testFromIssuer, testFromSubject, "active", emailCiphertext, now)

	sessionStore := NewPostgresSessionStore(pool, keyring)
	liveSession := Session{
		ID: randomUUIDv4(), FamilyID: randomUUIDv4(), UserID: userID,
		Issuer: testFromIssuer, Subject: testFromSubject,
		TokenHash: sha256Hex("live-token"), CSRFHash: sha256Hex("live-csrf"),
		Roles: []string{}, AMR: []string{},
		CreatedAt: now, LastSeenAt: now,
		IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(4 * time.Hour),
	}
	if err = sessionStore.Create(ctx, liveSession); err != nil {
		t.Fatal(err)
	}
	alreadyRevokedSession := Session{
		ID: randomUUIDv4(), FamilyID: randomUUIDv4(), UserID: userID,
		Issuer: testFromIssuer, Subject: testFromSubject,
		TokenHash: sha256Hex("revoked-token"), CSRFHash: sha256Hex("revoked-csrf"),
		Roles: []string{}, AMR: []string{},
		CreatedAt: now, LastSeenAt: now,
		IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(4 * time.Hour),
	}
	if err = sessionStore.Create(ctx, alreadyRevokedSession); err != nil {
		t.Fatal(err)
	}
	if _, err = sessionStore.RevokeToken(ctx, alreadyRevokedSession.TokenHash, now, "pre-existing logout"); err != nil {
		t.Fatal(err)
	}

	// Dry run first: must report the row without mutating anything.
	dryRun, err := MigrateOIDCBinding(ctx, pool, keyring, baseMigrationInput())
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dryRun.Applied || dryRun.AlreadyMigrated {
		t.Fatalf("dry run must not mutate: %+v", dryRun)
	}
	if dryRun.Row.UserID != userID || dryRun.Row.Status != "active" || dryRun.Row.MaskedEmail != "a***b" {
		t.Fatalf("unexpected dry run row: %+v", dryRun.Row)
	}
	if dryRun.Row.AuthSessionsTotal != 2 || dryRun.Row.AuthSessionsLive != 1 {
		t.Fatalf("unexpected dry run session counts: %+v", dryRun.Row)
	}
	var issuerAfterDryRun string
	if err = pool.QueryRow(ctx, `SELECT oidc_issuer FROM invoice_users WHERE id=$1`, userID).Scan(&issuerAfterDryRun); err != nil {
		t.Fatal(err)
	}
	if issuerAfterDryRun != testFromIssuer {
		t.Fatalf("dry run mutated oidc_issuer: %q", issuerAfterDryRun)
	}

	in := baseMigrationInput()
	in.Apply = true
	in.OperatorID = testOperatorID
	applied, err := MigrateOIDCBinding(ctx, pool, keyring, in)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !applied.Applied || applied.AlreadyMigrated {
		t.Fatalf("expected Applied=true AlreadyMigrated=false: %+v", applied)
	}
	if applied.SessionsInvalidated != 1 {
		t.Fatalf("expected exactly 1 live session invalidated, got %d", applied.SessionsInvalidated)
	}

	var issuer, subject string
	var newEmailCiphertext []byte
	if err = pool.QueryRow(ctx, `SELECT oidc_issuer,oidc_subject,email_ciphertext FROM invoice_users WHERE id=$1`, userID).
		Scan(&issuer, &subject, &newEmailCiphertext); err != nil {
		t.Fatal(err)
	}
	if issuer != testToIssuer || subject != testToSubject {
		t.Fatalf("row not migrated: issuer=%q subject=%q", issuer, subject)
	}
	plaintext, err := keyring.Decrypt(newEmailCiphertext, userEmailAADForMigration(testToIssuer, testToSubject))
	if err != nil || string(plaintext) != "ab@example.com" {
		t.Fatalf("email did not survive re-encryption: plaintext=%q err=%v", plaintext, err)
	}

	var liveRevokedAt, alreadyRevokedReason *string
	if err = pool.QueryRow(ctx, `SELECT revoked_at::text FROM auth_sessions WHERE id=$1`, liveSession.ID).Scan(&liveRevokedAt); err != nil {
		t.Fatal(err)
	}
	if liveRevokedAt == nil {
		t.Fatal("live session was not revoked by apply")
	}
	if err = pool.QueryRow(ctx, `SELECT revoked_reason FROM auth_sessions WHERE id=$1`, alreadyRevokedSession.ID).Scan(&alreadyRevokedReason); err != nil {
		t.Fatal(err)
	}
	if alreadyRevokedReason == nil || *alreadyRevokedReason != "pre-existing logout" {
		t.Fatalf("already-revoked session's reason must be untouched, got %v", alreadyRevokedReason)
	}

	var auditCount int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE object_type='invoice_user' AND object_id=$1 AND action=$2
		  AND actor_id=$3 AND before_hash=$4 AND after_hash=$5`,
		userID, identityMigratedAction, testOperatorID,
		identityMigrationHash(testFromIssuer, testFromSubject),
		identityMigrationHash(testToIssuer, testToSubject)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected exactly 1 migration audit row, got %d", auditCount)
	}
}

// TestMigrateOIDCBindingApplyIsIdempotent confirms a second --apply with the
// identical parameters reports AlreadyMigrated and changes nothing further
// (no additional session invalidation, no duplicate audit row).
func TestMigrateOIDCBindingApplyIsIdempotent(t *testing.T) {
	pool := setupIdentityMigrateTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	keyring := testSessionKeyring()
	userID := insertTestInvoiceUser(t, pool, testFromIssuer, testFromSubject, "active", nil, now)

	in := baseMigrationInput()
	in.Apply = true
	in.OperatorID = testOperatorID
	first, err := MigrateOIDCBinding(ctx, pool, keyring, in)
	if err != nil || !first.Applied {
		t.Fatalf("first apply: result=%+v err=%v", first, err)
	}

	second, err := MigrateOIDCBinding(ctx, pool, keyring, in)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if !second.AlreadyMigrated || second.Applied {
		t.Fatalf("expected AlreadyMigrated=true Applied=false on repeat, got %+v", second)
	}
	if second.Row.UserID != userID {
		t.Fatalf("already-migrated result should still report the row: %+v", second.Row)
	}

	var auditCount int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE object_type='invoice_user' AND object_id=$1 AND action=$2`,
		userID, identityMigratedAction).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("idempotent re-apply must not write a second audit row, got %d", auditCount)
	}

	// A dry run with the same parameters after the fact must also report
	// AlreadyMigrated, not a hard "0 rows matched" refusal.
	dryRunIn := baseMigrationInput()
	dryRunAgain, err := MigrateOIDCBinding(ctx, pool, keyring, dryRunIn)
	if err != nil || !dryRunAgain.AlreadyMigrated {
		t.Fatalf("expected a dry run after apply to report AlreadyMigrated: result=%+v err=%v", dryRunAgain, err)
	}
}
