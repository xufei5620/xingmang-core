package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/testdb"
)

const (
	cliSourceID   = "10000000-0000-4000-8000-0000000000f0"
	cliOperatorID = "70000000-0000-4000-8000-0000000000f0"
)

// setupBindCLIEnv mirrors cmd/eligibility-repair's setupRepairCLIEnv: it
// resets the schema, applies the migrations, writes the two secret files run()
// reads, and seeds the one enabled source instance the tool resolves.
func setupBindCLIEnv(t *testing.T) (databaseURLFile, keyringFile, migrationsDir string, pool *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	migrationsDir = filepath.Join("..", "..", "migrations")
	if err = migrate.Up(ctx, pool, migrationsDir); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
		VALUES($1,'sub2api','account-bind CLI fixture','fixture-runtime',TRUE)`, cliSourceID); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	databaseURLFile = filepath.Join(dir, "database-url")
	if err = os.WriteFile(databaseURLFile, []byte(databaseURL), 0o600); err != nil {
		t.Fatal(err)
	}
	keyringFile = filepath.Join(dir, "keyring.json")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	index := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("i", 32)))
	keyringBody := `{"current_key_id":"2026-09","encryption_keys":{"2026-09":"` + key + `"},"index_key":"` + index + `"}`
	if err = os.WriteFile(keyringFile, []byte(keyringBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return databaseURLFile, keyringFile, migrationsDir, pool
}

// TestRunRefusesBadInputBeforeTouchingTheDatabase: every one of these must
// fail on the flags alone. They are checked with deliberately unusable file
// paths, so a check that ran too late would fail with a file error instead
// and the assertion on the message would catch it.
func TestRunRefusesBadInputBeforeTouchingTheDatabase(t *testing.T) {
	ctx := context.Background()
	for name, testCase := range map[string]struct {
		options bindOptions
		want    string
	}{
		"unknown platform":     {bindOptions{platform: "sub3api", externalUserID: "1"}, "--platform"},
		"empty platform":       {bindOptions{externalUserID: "1"}, "--platform"},
		"missing external id":  {bindOptions{platform: "sub2api"}, "--external-user-id"},
		"apply without id":     {bindOptions{platform: "sub2api", externalUserID: "1", apply: true}, "operator UUID"},
		"apply with non-uuid":  {bindOptions{platform: "sub2api", externalUserID: "1", apply: true, operatorID: "alice"}, "operator UUID"},
		"apply with short uuid": {bindOptions{platform: "sub2api", externalUserID: "1", apply: true,
			operatorID: "70000000-0000-4000-8000-00000000"}, "operator UUID"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(ctx, "/nonexistent/database-url", "/nonexistent/keyring.json", "/nonexistent/migrations", testCase.options, &out)
			if err == nil {
				t.Fatal("accepted invalid input")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not mention %q -- the check may be running after the database is opened", err, testCase.want)
			}
			if out.Len() != 0 {
				t.Fatalf("printed a summary for rejected input: %s", out.String())
			}
		})
	}
}

// TestPlatformLoginOriginMatchesTheApiRuntime is the byte-for-byte contract
// with cmd/api's buildPlatformLogin: same variables, same defaults, same
// trailing-slash trim. The stored oidc_issuer comes from here, and a mismatch
// does not fail now -- it surfaces months later as a customer whose identity
// hash and email AAD were derived from the wrong origin.
func TestPlatformLoginOriginMatchesTheApiRuntime(t *testing.T) {
	t.Setenv("SUB2API_LOGIN_BASE_URL", "")
	t.Setenv("NEWAPI_LOGIN_BASE_URL", "")
	for platform, want := range map[string]string{"sub2api": "https://api.solov.cc", "newapi": "https://xm.solov.cc"} {
		got, err := platformLoginOrigin(platform)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s default origin is %q, want %q (cmd/api/runtime.go buildPlatformLogin)", platform, got, want)
		}
	}
	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.example.test/")
	got, err := platformLoginOrigin("sub2api")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.test" {
		t.Fatalf("trailing slash was not trimmed: %q", got)
	}
	t.Setenv("SUB2API_LOGIN_BASE_URL", "http://api.example.test")
	if _, err = platformLoginOrigin("sub2api"); err == nil {
		t.Fatal("accepted a non-HTTPS login origin")
	}
}

// TestUserEmailAADMatchesTheApplicationPackage guards the hand-mirrored AAD.
// application/crypto.go's userEmailAAD is unexported and this package
// deliberately does not import that package, so the only thing keeping the
// two in step is this literal.
func TestUserEmailAADMatchesTheApplicationPackage(t *testing.T) {
	const want = "invoice-user-email\nhttps://api.solov.cc\n7788"
	if got := userEmailAAD("https://api.solov.cc", "7788"); got != want {
		t.Fatalf("AAD is %q, want %q", got, want)
	}
}

// TestRunDryRunAgainstAnEmptyDatabaseReportsAPlan is the wiring smoke test:
// flag parsing, both secret files, migrate.Verify, the blind-index derivation
// and the store call all have to line up for this to print anything at all.
func TestRunDryRunAgainstAnEmptyDatabaseReportsAPlan(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir, pool := setupBindCLIEnv(t)
	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.solov.cc")
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir,
		bindOptions{platform: "sub2api", externalUserID: "7788"}, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	for _, want := range []string{
		"account-bind XM-INV-SHADOW-BINDING: DRY RUN (nothing was changed)",
		"platform:            sub2api",
		"issuer:              https://api.solov.cc",
		"external_user_id:    7788",
		"source_instance_id:  " + cliSourceID,
		"timing gate:         GO",
		"binding:             operator_attested / verified",
		"PRE_POLICY_SKIPPED:  0 (irreversible)",
		"Re-run with --apply",
	} {
		if !strings.Contains(printed, want) {
			t.Fatalf("dry run output is missing %q:\n%s", want, printed)
		}
	}
	var users int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoice_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Fatalf("the dry run wrote %d invoice_users row(s)", users)
	}
}

// TestRunApplyWritesTheShadowBinding proves the CLI's own apply path end to
// end -- in particular that the two blind indexes it derives from the keyring
// file are the ones the store stores, which no dry run can show.
func TestRunApplyWritesTheShadowBinding(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir, pool := setupBindCLIEnv(t)
	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.solov.cc")
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options := bindOptions{platform: "sub2api", externalUserID: "7788", email: "customer@example.test",
		apply: true, operatorID: cliOperatorID}
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, options, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "account-bind XM-INV-SHADOW-BINDING: APPLIED") {
		t.Fatalf("apply did not report applying:\n%s", out.String())
	}
	var issuer, subject, platform, platformUserID, status string
	var emailVerified bool
	if err := pool.QueryRow(ctx, `SELECT oidc_issuer,oidc_subject,COALESCE(platform,''),
		COALESCE(platform_user_id,''),status,email_verified FROM invoice_users`).Scan(
		&issuer, &subject, &platform, &platformUserID, &status, &emailVerified); err != nil {
		t.Fatal(err)
	}
	if issuer != "https://api.solov.cc" || subject != "7788" || platform != "sub2api" ||
		platformUserID != "7788" || status != "active" {
		t.Fatalf("shadow identity is %s/%s %s/%s %s", issuer, subject, platform, platformUserID, status)
	}
	// An operator typing an address into a terminal has not verified it.
	if emailVerified {
		t.Fatal("--email marked the address verified")
	}
	var method, bindingStatus, subjectHMAC string
	if err := pool.QueryRow(ctx, `SELECT binding_method,binding_status,COALESCE(external_subject_hmac,'')
		FROM external_accounts`).Scan(&method, &bindingStatus, &subjectHMAC); err != nil {
		t.Fatal(err)
	}
	if method != "operator_attested" || bindingStatus != "verified" {
		t.Fatalf("binding is %s/%s", method, bindingStatus)
	}
	if !strings.HasPrefix(subjectHMAC, "h1:") || len(subjectHMAC) != 67 {
		t.Fatalf("external_subject_hmac is %q; the CLI did not derive one", subjectHMAC)
	}

	// The realistic operator mistake is running it twice.
	var repeat bytes.Buffer
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, options, &repeat); err != nil {
		t.Fatalf("re-run failed: %v", err)
	}
	var users int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoice_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("%d invoice_users rows after two applies, want 1", users)
	}
}
