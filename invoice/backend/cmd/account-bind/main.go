// Command account-bind implements design XM-INV-SHADOW-BINDING section B
// option 1, as decided by the product owner on 2026-09-09: a versioned,
// human-approved lifecycle operation that binds a real upstream customer
// account to a shadow invoice_user BEFORE that customer has ever logged in,
// so the operator can run reconciliation and see whether the account could
// invoice at all.
//
// What it does, in one all-or-nothing transaction (see
// postgresstore.OperatorBindExternalAccount):
//
//	resolve the platform's one enabled source_instance
//	-> EnsureUser at (issuer = the platform's login origin,
//	   subject = the upstream user id), status active, platform columns filled
//	-> BindExternalAccount with binding_method operator_attested and
//	   binding_status verified, carrying a self-derived external_subject_hmac
//	-> wake the source facts parked on that binding
//
// Three things about it that are not obvious and are not negotiable:
//
//   - It cannot be cleanly undone. There is no unbind; the wake rewrites
//     every pre-policy usage/balance fact for the account to
//     PRE_POLICY_SKIPPED, which is irreversible; and hard deletion is blocked
//     by ON DELETE RESTRICT. The approved withdrawal option is (a): leave the
//     binding in place, and the customer's eventual real login claims it.
//   - It must run one account at a time, in a low-traffic window, with
//     somebody watching /readyz. Waking one account releases hundreds to
//     thousands of parked facts, and while the worker drains them every other
//     customer on the same source reads source_unavailable.
//   - The resulting shadow user must never be used to submit a request or
//     upload a document. Those are the system's only two outbound paths to a
//     real person.
//
// Defaults to a dry run; --apply requires --operator-id and is refused
// outright unless the ingest timing gate is satisfied. Mirrors
// cmd/identity-migrate's and cmd/eligibility-repair's CLI shape exactly
// (absolute-path flags, one-line secret file reader, migrate.Verify before
// touching data, a printed summary). See docs/PRODUCTION-RUNBOOK.md section
// "代为绑定（operator_attested）" for the docker run form and the timing gate,
// and docs/handoffs/XM-INV-SHADOW-BINDING.md for the handoff.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
)

// uuidPattern is the same shape auth.validUUIDString accepts; --operator-id
// must name a real admin, not a free-text label, because that id is the only
// record of who decided to bind on a customer's behalf.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// externalUserIDPattern is the shape both upstream platforms actually
// produce: a decimal integer (auth/sub2api_login.go and auth/newapi_login.go
// both format theirs with strconv.FormatInt). Twenty digits covers int64.
var externalUserIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// bindOptions is the CLI's parsed input, grouped so run's signature does not
// grow a row of same-typed positional arguments a caller could transpose.
type bindOptions struct {
	platform       string
	externalUserID string
	email          string
	apply          bool
	operatorID     string
}

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to the database URL secret")
	keyringFile := flag.String("field-keyring-file", "", "absolute path to the field encryption keyring")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	platform := flag.String("platform", "", "the upstream platform: sub2api or newapi")
	externalUserID := flag.String("external-user-id", "", "the upstream platform's own user id, copied verbatim from its admin console")
	email := flag.String("email", "", "optional: the customer's platform email, stored encrypted and UNVERIFIED (it is not proof of anything; the customer's own login is)")
	apply := flag.Bool("apply", false, "actually create the shadow binding and wake its parked facts (default is a dry run that changes nothing)")
	operatorID := flag.String("operator-id", "", "the approving operator's admin UUID (required with --apply)")
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("account-bind does not accept positional arguments")
		os.Exit(2)
	}
	for name, value := range map[string]string{"database URL file": *databaseURLFile,
		"field keyring file": *keyringFile, "migrations directory": *migrationsDir} {
		if !filepath.IsAbs(value) {
			slog.Error("account-bind path must be absolute", "field", name)
			os.Exit(2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	options := bindOptions{
		platform: *platform, externalUserID: *externalUserID, email: *email,
		apply: *apply, operatorID: *operatorID,
	}
	if err := run(ctx, *databaseURLFile, *keyringFile, *migrationsDir, options, os.Stdout); err != nil {
		slog.Error("account-bind failed", "error", err)
		os.Exit(exitCodeFor(err))
	}
}

// Exit codes. They are distinct because the operational responses are
// distinct, and because the runbook documents them: 3 means "come back later,
// nothing was wrong with what you typed", which is the single most likely
// outcome of a first attempt and must not be confused with a real failure.
//
//	0  success (an apply that committed, or a dry run -- including a dry run
//	   whose timing gate is NO-GO, which still prints a complete plan)
//	1  the operation failed: database unreachable, migration set mismatch,
//	   a refused bind (ErrForbidden), a serialization conflict, an issuer that
//	   disagrees with the identities already in the database
//	2  the command line could not be parsed at all: a positional argument was
//	   supplied, or one of the three path flags is not absolute. This is
//	   NARROWER than "the command line is wrong": an invalid flag VALUE
//	   (--platform=sub3api, --external-user-id=alice, --operator-id=bob) is
//	   rejected inside run(), before the database is opened, and exits 1. The
//	   runbook's exit-code table says the same. Do not branch on 2 as "my
//	   command was malformed".
//	3  --apply was refused by the ingest timing gate: nothing was wrong with
//	   the request, the deployment is simply not in a state to accept it
const (
	exitTimingGateRefused = 3
	exitOperationFailed   = 1
)

func exitCodeFor(err error) int {
	if errors.Is(err, postgresstore.ErrOperatorBindTimingGate) {
		return exitTimingGateRefused
	}
	return exitOperationFailed
}

func run(ctx context.Context, databaseURLFile, keyringFile, migrationsDir string, options bindOptions, out io.Writer) error {
	platform := strings.TrimSpace(options.platform)
	externalUserID := strings.TrimSpace(options.externalUserID)
	operatorID := strings.TrimSpace(options.operatorID)
	if platform != "sub2api" && platform != "newapi" {
		return fmt.Errorf("--platform must be sub2api or newapi, got %q", options.platform)
	}
	if externalUserID == "" {
		return errors.New("--external-user-id is required")
	}
	// Both platforms' user ids are decimal integers -- Sub2API and New API
	// each render theirs with strconv.FormatInt (auth/sub2api_login.go,
	// auth/newapi_login.go). Anything else is a paste of the wrong field, and
	// an email address pasted here is the worst case: it mints a shadow
	// identity whose (issuer, subject) no login will ever produce, so nothing
	// ever claims it, while the irreversible PRE_POLICY_SKIPPED write-off has
	// already happened.
	if !externalUserIDPattern.MatchString(externalUserID) {
		return fmt.Errorf("--external-user-id must be the upstream platform's numeric user id (1-20 digits), got %q", options.externalUserID)
	}
	if options.apply && !uuidPattern.MatchString(operatorID) {
		return errors.New("a valid operator UUID is required to apply")
	}
	issuer, err := platformLoginOrigin(platform)
	if err != nil {
		return err
	}

	databaseURL, err := readOneLineSecret(databaseURLFile)
	if err != nil {
		return fmt.Errorf("read database credential: %w", err)
	}
	keyring, err := securefields.LoadKeyringFile(keyringFile)
	if err != nil {
		return fmt.Errorf("load field keyring: %w", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err = migrate.Verify(ctx, pool, migrationsDir); err != nil {
		return fmt.Errorf("database migration set mismatch: %w", err)
	}
	store := postgresstore.New(pool)

	// The source instance has to be resolved before the HMACs can be derived
	// (both namespaces are scoped to it), and again inside the transaction
	// that does the work. Resolving it twice is deliberate: the in-transaction
	// resolution is the one that decides, and this one exists only to key the
	// blind indexes. A source instance that changed between them would produce
	// indexes that match nothing, and the summary's "released: 0" would say so.
	sourceInstanceID, err := store.GetEnabledSourceInstanceID(ctx, platform)
	if err != nil {
		return err
	}
	subjectHMAC, err := application.PlatformBindingSubjectIndex(keyring, sourceInstanceID, externalUserID)
	if err != nil {
		return fmt.Errorf("derive external subject blind index: %w", err)
	}
	dependencyKey, err := application.SourceDependencyKeyHMAC(keyring, "source_external_account", sourceInstanceID, externalUserID)
	if err != nil {
		return fmt.Errorf("derive source dependency blind index: %w", err)
	}
	// The empty source instance and the issuer+"\n"+subject value are not a
	// slip: they mirror Service.EnsureUser's own invoice_oidc_user wake byte
	// for byte. A key derived any other way matches no rows and reports a
	// cheerful zero.
	oidcUserKey, err := application.SourceDependencyKeyHMAC(keyring, "invoice_oidc_user", "", issuer+"\n"+externalUserID)
	if err != nil {
		return fmt.Errorf("derive invoice_oidc_user dependency blind index: %w", err)
	}

	in := postgresstore.OperatorBindInput{
		Platform: platform, Issuer: issuer, ExternalUserID: externalUserID,
		ExternalSubjectHMAC: subjectHMAC, DependencyKeyHMAC: dependencyKey,
		OIDCUserDependencyKeyHMAC: oidcUserKey,
		Apply:                     options.apply, OperatorID: operatorID,
	}
	if email := strings.TrimSpace(options.email); email != "" {
		// Stored encrypted under the same AAD a real login would use, but
		// email_verified stays FALSE: an operator typing an address into a
		// terminal has not verified it, and a verified delivery address is
		// what the invoice is actually sent to.
		ciphertext, encErr := keyring.Encrypt([]byte(email), application.UserEmailAAD(issuer, externalUserID))
		if encErr != nil {
			return fmt.Errorf("encrypt platform email: %w", encErr)
		}
		in.EmailCiphertext, in.EmailVerified = ciphertext, false
	}

	result, err := store.OperatorBindExternalAccount(ctx, in, postgresstore.AuditActor{
		Type: "operator", ID: operatorID, RequestID: "account-bind-cli",
		Reason: "XM-INV-SHADOW-BINDING operator-attested binding " + modeLabel(options.apply),
	})
	if err != nil {
		printRefusalDetail(out, platform, externalUserID, err)
		return fmt.Errorf("operator-attested bind: %w", err)
	}
	printSummary(out, platform, issuer, externalUserID, result)
	return nil
}

// printRefusalDetail explains the two refusals whose whole value is in the
// explanation. Both are returned in dry-run too -- a dry run that printed a
// tidy plan for either would be printing a plan to do damage -- so without
// this the operator would see only a one-line error and no context.
func printRefusalDetail(out io.Writer, platform, externalUserID string, err error) {
	switch {
	case errors.Is(err, postgresstore.ErrOperatorBindWouldOverwriteBinding):
		fmt.Fprintf(out, "REFUSED: %s user %s already has a binding this tool did not create.\n\n", platform, externalUserID)
		fmt.Fprintf(out, "  The usual reason is that the customer has already logged in with their own\n"+
			"  platform password. That login IS the ownership proof, and it is recorded as\n"+
			"  binding_method=platform_password_login. Rebinding would overwrite that record\n"+
			"  with operator_attested and reset verified_at, leaving no way to tell afterwards\n"+
			"  that the customer proved it themselves.\n\n"+
			"  There is nothing to do here: the account is already bound, and its parked facts\n"+
			"  were released by that login. Check the admin ledger instead.\n\n")
	case errors.Is(err, postgresstore.ErrOperatorBindIdentityProjectionOpen):
		fmt.Fprintf(out, "REFUSED: unprocessed identity_binding ingest events exist.\n\n")
		fmt.Fprintf(out, "  These are resolved against the CENTRAL OIDC issuer and subject, not the\n"+
			"  platform login origin this binding uses, so a shadow identity cannot satisfy\n"+
			"  them. Binding now risks driving them to dead, which fails /readyz for everyone.\n"+
			"  Clear them first (runbook section 9, dead-event handling), then retry.\n\n")
	}
}

// platformLoginOrigin reads the platform's login origin from the same
// environment variables cmd/api's buildPlatformLogin reads, with the same
// trailing-slash trim -- but deliberately WITHOUT their compiled-in defaults.
//
// cmd/api can afford a default: a wrong origin there produces a login that
// fails loudly and immediately. Here the same value is written permanently
// into invoice_users.oidc_issuer, and the customer's later real login claims
// the row through the external account rather than through (issuer, subject),
// so it never repairs the column -- cmd/api's
// TestShadowBindWithAWrongIssuerStillClaimsButLeavesTheIdentityWrong proves
// that.
//
// The first review of this tool found exactly how a default goes wrong here.
// The runbook passed `docker run -e SUB2API_LOGIN_BASE_URL` with no value; the
// variable lives only in .env.production and is not exported in an operator's
// shell, so `-e VAR` passed nothing and the tool silently used its default.
// That default happens to equal today's configured value, so the mistake would
// have stayed invisible until somebody changed the variable. A default that is
// right only by coincidence is worse than no default, so this refuses instead.
// checkPlatformIssuerConsistency then checks the supplied value against what
// the running api demonstrably minted for earlier logins.
func platformLoginOrigin(platform string) (string, error) {
	variable := "SUB2API_LOGIN_BASE_URL"
	if platform == "newapi" {
		variable = "NEWAPI_LOGIN_BASE_URL"
	}
	value := strings.TrimSpace(os.Getenv(variable))
	if value == "" {
		return "", fmt.Errorf("%s is not set. Copy the value the api actually uses: "+
			`S="$(docker exec invoice-system-prod-api-1 printenv %s)"`+
			` then pass -e "%s=$S". Do NOT rely on --env-file: this key is optional in`+
			" .env.production (compose supplies it as ${%s:-...}), so the env file usually"+
			" does not contain it at all. This value is written permanently into"+
			" invoice_users.oidc_issuer and no later login repairs it",
			variable, variable, variable, variable)
	}
	value = strings.TrimRight(value, "/")
	if !strings.HasPrefix(value, "https://") || strings.ContainsAny(value, " \r\n\t") {
		return "", fmt.Errorf("%s must be one exact HTTPS origin", variable)
	}
	return value, nil
}

func modeLabel(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry run"
}

// issuerProvenance annotates the issuer line with whether anything in the
// database corroborates it. The comparison population is identities that own a
// platform-login or operator-attested binding on this source -- NOT every
// identity carrying this platform, which also includes centrally minted OIDC
// users whose issuer is legitimately different (see
// checkPlatformIssuerConsistency). On a source's first such identity there is
// nothing to compare against and the operator is the only check, so the line
// says so rather than looking as verified as a corroborated one.
func issuerProvenance(result postgresstore.OperatorBindResult) string {
	if result.PlatformIssuerInUse == "" {
		return "   <- FIRST platform-login identity on this source: nothing corroborates this. Verify it by hand."
	}
	return "   (matches every platform-login identity on this source)"
}

func printSummary(out io.Writer, platform, issuer, externalUserID string, result postgresstore.OperatorBindResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "account-bind XM-INV-SHADOW-BINDING: %s\n\n", mode)
	fmt.Fprintf(out, "platform:            %s\n", platform)
	fmt.Fprintf(out, "issuer:              %s%s\n", issuer, issuerProvenance(result))
	fmt.Fprintf(out, "external_user_id:    %s\n", externalUserID)
	fmt.Fprintf(out, "source_instance_id:  %s\n", result.SourceInstanceID)
	// Echoed so the runbook's observation-window queries have a key to paste:
	// the parked rows are found by this blind index and it cannot be derived
	// by hand.
	fmt.Fprintf(out, "dependency_key_hmac: %s\n\n", result.DependencyKeyHMAC)

	gate := "GO"
	if !result.GateSatisfied {
		gate = "NO-GO"
	}
	fmt.Fprintf(out, "timing gate:         %s (%s)\n", gate, result.GateReason)
	fmt.Fprintf(out, "ingest pending:      %d\n", result.Health.Pending)
	fmt.Fprintf(out, "ingest dead:         %d (contained %d)\n", result.Health.Dead, result.Health.DeadContained)
	fmt.Fprintf(out, "ingest waiting:      %d\n", result.Health.Waiting)
	// Deployment-wide, not per-id: see countOpenIdentityBindingEvents for why
	// it cannot be scoped. Labelled so nobody reads it as "this customer's".
	fmt.Fprintf(out, "identity_binding open (whole deployment): %d\n\n", result.IdentityBindingEventsOpen)

	userLabel := "reused"
	if result.UserCreated {
		userLabel = "created"
	}
	bindingLabel := "updated in place"
	if result.BindingCreated {
		bindingLabel = "created"
	}
	// A dry run really did INSERT these rows before rolling back, so the ids
	// are real ids that no longer exist. The runbook tells operators to save
	// external_account_id and invoice_user_id as observation-window variables,
	// and ids copied from a dry run make every one of those queries return
	// nothing -- which reads exactly like a failed bind. Say so on the line.
	rolledBack := ""
	if !result.Applied {
		rolledBack = " (rolled back; --apply will mint different ids)"
	}
	fmt.Fprintf(out, "invoice_user_id:     %s (%s)%s\n", result.InvoiceUserID, userLabel, rolledBack)
	fmt.Fprintf(out, "external_account_id: %s (%s)%s\n", result.ExternalAccountID, bindingLabel, rolledBack)
	fmt.Fprintf(out, "binding:             %s / %s\n\n", result.BindingMethod, result.BindingStatus)

	fmt.Fprintf(out, "PRE_POLICY_SKIPPED:  %d (irreversible)\n", result.PrePolicySkipped)
	fmt.Fprintf(out, "released to queued:  %d\n", result.Released)
	fmt.Fprintf(out, "facts ever seen:     %d\n\n", result.FactsEverSeen)

	// "released: 0" reads identically for "this customer had nothing parked"
	// and "this id belongs to nobody because a digit was mistyped". The
	// ownership guard does not separate them either: it only refuses an id
	// already bound to SOMEBODY, and a mistyped id that lands on a real but
	// never-bound customer is exactly the case it lets through. Say so.
	if result.BindingCreated && result.FactsEverSeen == 0 {
		fmt.Fprintf(out, "WARNING: no parked facts for this external id, and none were ever released for it.\n"+
			"  This database has never heard of %q on this source. That is what a mistyped\n"+
			"  upstream id normally looks like. Re-copy the id from the upstream console before\n"+
			"  applying. A non-zero count would NOT have proved the id is right either -- it can\n"+
			"  belong to a different, never-bound customer -- so verify the id either way.\n\n", externalUserID)
	}

	if result.Applied {
		fmt.Fprintf(out, "applied. Watch /readyz and the dead-event count until the released events drain;\n"+
			"other customers on this source read source_unavailable meanwhile, which is expected.\n"+
			"Do not submit a request or upload a document as this user.\n")
		return
	}
	if !result.GateSatisfied {
		fmt.Fprintf(out, "planned, but --apply would be REFUSED: %s\n", result.GateReason)
		return
	}
	fmt.Fprintf(out, "planned. Re-run with --apply --operator-id <uuid> to commit exactly the above.\n")
}

func readOneLineSecret(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 || len(body) > 64<<10 {
		return "", errors.New("secret file has invalid size")
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("secret file must contain exactly one non-empty line")
	}
	return string(body), nil
}
