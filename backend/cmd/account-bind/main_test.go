package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
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
		"unknown platform":    {bindOptions{platform: "sub3api", externalUserID: "1"}, "--platform"},
		"empty platform":      {bindOptions{externalUserID: "1"}, "--platform"},
		"missing external id": {bindOptions{platform: "sub2api"}, "--external-user-id"},
		// Both platforms format their user ids with strconv.FormatInt, so a
		// non-numeric value is a paste of the wrong field. An email is the
		// worst of them: it mints a shadow identity at an (issuer, subject)
		// pair no login will ever produce, so nothing ever claims it -- after
		// the irreversible PRE_POLICY_SKIPPED write-off has already happened.
		"email pasted as id":    {bindOptions{platform: "sub2api", externalUserID: "a@b.test"}, "numeric user id"},
		"username pasted as id": {bindOptions{platform: "sub2api", externalUserID: "alice"}, "numeric user id"},
		"id with stray sign":    {bindOptions{platform: "sub2api", externalUserID: "-42"}, "numeric user id"},
		"id far too long":       {bindOptions{platform: "sub2api", externalUserID: strings.Repeat("9", 21)}, "numeric user id"},
		"apply without id":      {bindOptions{platform: "sub2api", externalUserID: "1", apply: true}, "operator UUID"},
		"apply with non-uuid":   {bindOptions{platform: "sub2api", externalUserID: "1", apply: true, operatorID: "alice"}, "operator UUID"},
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

// TestPlatformLoginOriginRefusesAnUnsetVariable is the first review's finding
// turned into a test.
//
// The runbook passed `docker run -e SUB2API_LOGIN_BASE_URL` with no value.
// That variable lives only in .env.production and is not exported in an
// operator's shell, so nothing reached the container and the tool fell back to
// a compiled-in default -- which happens to equal today's configured value, so
// the mistake was invisible. It becomes permanent damage the day the variable
// changes, because no later login repairs oidc_issuer. There is now no default
// to fall back to. The mutation that must turn this red is restoring the
// fallback: the loop below then gets a value instead of an error.
func TestPlatformLoginOriginRefusesAnUnsetVariable(t *testing.T) {
	t.Setenv("SUB2API_LOGIN_BASE_URL", "")
	t.Setenv("NEWAPI_LOGIN_BASE_URL", "")
	for _, platform := range []string{"sub2api", "newapi"} {
		got, err := platformLoginOrigin(platform)
		if err == nil {
			t.Fatalf("%s: silently used a default origin %q instead of refusing", platform, got)
		}
		if !strings.Contains(err.Error(), "LOGIN_BASE_URL") || !strings.Contains(err.Error(), "--env-file") {
			t.Fatalf("%s: the error does not tell the operator how to fix it: %v", platform, err)
		}
	}
}

// TestPlatformLoginOriginNormalizesLikeTheApiRuntime keeps the surviving half
// of the contract with cmd/api's buildPlatformLogin: same variables, same
// trailing-slash trim, same exact-HTTPS-origin rule. Only the default is
// deliberately not shared.
func TestPlatformLoginOriginNormalizesLikeTheApiRuntime(t *testing.T) {
	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.example.test/")
	got, err := platformLoginOrigin("sub2api")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.test" {
		t.Fatalf("trailing slash was not trimmed: %q", got)
	}
	t.Setenv("NEWAPI_LOGIN_BASE_URL", "https://xm.example.test")
	if got, err = platformLoginOrigin("newapi"); err != nil || got != "https://xm.example.test" {
		t.Fatalf("newapi origin is %q (%v)", got, err)
	}
	t.Setenv("SUB2API_LOGIN_BASE_URL", "http://api.example.test")
	if _, err = platformLoginOrigin("sub2api"); err == nil {
		t.Fatal("accepted a non-HTTPS login origin")
	}
}

// TestUserEmailAADIsTheApplicationPackagesOwn proves the CLI encrypts under
// the definition the api decrypts with, not under a copy of it.
//
// The first review pointed out that the previous shape was circular: a local
// copy plus a test asserting that same local copy stayed green no matter what
// application/crypto.go did. The definition now lives once, in
// application.UserEmailAAD, and this calls it.
func TestUserEmailAADIsTheApplicationPackagesOwn(t *testing.T) {
	const want = "invoice-user-email\nhttps://api.solov.cc\n7788"
	if got := application.UserEmailAAD("https://api.solov.cc", "7788"); got != want {
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
		"dependency_key_hmac: h1:",
		"timing gate:         GO",
		"binding:             operator_attested / verified",
		"PRE_POLICY_SKIPPED:  0 (irreversible)",
		"facts ever seen:     0",
		// A dry run's ids are real ids that no longer exist. The runbook has
		// operators paste external_account_id and invoice_user_id into the
		// observation-window queries, and dry-run ids make every one of those
		// return nothing -- indistinguishable from a failed bind.
		"(rolled back; --apply will mint different ids)",
		// The two lines the first review found missing from the runbook's
		// checklist, plus the warning that makes the dangerous case visible
		// without an operator having to compare numbers by eye.
		"ingest waiting:      0",
		"FIRST platform-login identity on this source",
		"WARNING: no parked facts for this external id",
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
	// The rolled-back marker is dry-run only. On the run whose ids the operator
	// is actually told to save it must be absent, or it would teach them to
	// distrust the ids that do work.
	if strings.Contains(out.String(), "rolled back") {
		t.Fatalf("apply output carries the dry-run rollback marker:\n%s", out.String())
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

// TestRunRefusesAnIssuerThatDisagreesWithTheDatabase is the durable half of
// the first review's issuer finding. Refusing an unset variable stops the
// silent-default case; this stops the case where a variable IS set and is
// simply wrong, by checking it against the issuer every existing identity for
// the platform already carries -- i.e. against what the running api actually
// minted, rather than against configuration.
func TestRunRefusesAnIssuerThatDisagreesWithTheDatabase(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir, pool := setupBindCLIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.solov.cc")
	var first bytes.Buffer
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir,
		bindOptions{platform: "sub2api", externalUserID: "7788", apply: true, operatorID: cliOperatorID}, &first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), "FIRST platform-login identity on this source") {
		t.Fatalf("the first bind did not flag that nothing corroborates its issuer:\n%s", first.String())
	}

	// Now the deployment's variable is changed (or mistyped) and a second
	// account is bound. The database already knows better.
	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.moved.example")
	var second bytes.Buffer
	err := run(ctx, databaseURLFile, keyringFile, migrationsDir,
		bindOptions{platform: "sub2api", externalUserID: "9911", apply: true, operatorID: cliOperatorID}, &second)
	if err == nil {
		t.Fatal("accepted an issuer that disagrees with every existing identity for the platform")
	}
	if !strings.Contains(err.Error(), "does not match the issuer every platform-login") {
		t.Fatalf("unexpected error: %v", err)
	}
	var users int64
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM invoice_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("%d invoice_users rows after the refusal, want 1", users)
	}

	// The corroborated case must still pass, and say so.
	t.Setenv("SUB2API_LOGIN_BASE_URL", "https://api.solov.cc")
	var third bytes.Buffer
	if err = run(ctx, databaseURLFile, keyringFile, migrationsDir,
		bindOptions{platform: "sub2api", externalUserID: "9911"}, &third); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third.String(), "matches every platform-login identity on this source") {
		t.Fatalf("a corroborated issuer was not reported as such:\n%s", third.String())
	}
}

// TestExitCodeSeparatesTheTimingGateFromRealFailures pins the exit contract the
// runbook documents. "Come back in the next quiet window" and "this failed"
// need different answers from a script, and previously both were 1.
func TestExitCodeSeparatesTheTimingGateFromRealFailures(t *testing.T) {
	// Literals, not the constants. Comparing exitCodeFor's output against the
	// same constants it returns is self-certifying: renaming 3 to 4 would keep
	// it green while silently breaking the runbook's exit-code table, which is
	// written in literals and is what an operator's script actually keys on.
	gateErr := fmt.Errorf("wrapped: %w", postgresstore.ErrOperatorBindTimingGate)
	if got := exitCodeFor(gateErr); got != 3 {
		t.Fatalf("timing gate refusal exits %d, want 3 (PRODUCTION-RUNBOOK section 9c exit-code table)", got)
	}
	if got := exitCodeFor(errors.New("database is unreachable")); got != 1 {
		t.Fatalf("ordinary failure exits %d, want 1 (PRODUCTION-RUNBOOK section 9c exit-code table)", got)
	}
}
