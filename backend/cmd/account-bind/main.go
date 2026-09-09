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
		os.Exit(1)
	}
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

	in := postgresstore.OperatorBindInput{
		Platform: platform, Issuer: issuer, ExternalUserID: externalUserID,
		ExternalSubjectHMAC: subjectHMAC, DependencyKeyHMAC: dependencyKey,
		Apply: options.apply, OperatorID: operatorID,
	}
	if email := strings.TrimSpace(options.email); email != "" {
		// Stored encrypted under the same AAD a real login would use, but
		// email_verified stays FALSE: an operator typing an address into a
		// terminal has not verified it, and a verified delivery address is
		// what the invoice is actually sent to.
		ciphertext, encErr := keyring.Encrypt([]byte(email), userEmailAAD(issuer, externalUserID))
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
		return fmt.Errorf("operator-attested bind: %w", err)
	}
	printSummary(out, platform, issuer, externalUserID, result)
	return nil
}

// platformLoginOrigin reads the platform's login origin from the same
// environment variables cmd/api's buildPlatformLogin reads, including their
// defaults and the same trailing-slash trim. It must agree byte for byte:
// this value becomes invoice_users.oidc_issuer, and the customer's eventual
// real login looks up their identity by exactly (issuer, subject). A shadow
// row minted under a different issuer is not claimed -- it is orphaned, and a
// second invoice_user appears beside it.
func platformLoginOrigin(platform string) (string, error) {
	variable := "SUB2API_LOGIN_BASE_URL"
	fallback := "https://api.solov.cc"
	if platform == "newapi" {
		variable, fallback = "NEWAPI_LOGIN_BASE_URL", "https://xm.solov.cc"
	}
	value := strings.TrimSpace(os.Getenv(variable))
	if value == "" {
		value = fallback
	}
	value = strings.TrimRight(value, "/")
	if !strings.HasPrefix(value, "https://") || strings.ContainsAny(value, " \r\n\t") {
		return "", fmt.Errorf("%s must be one exact HTTPS origin", variable)
	}
	return value, nil
}

// userEmailAAD must byte-for-byte match application/crypto.go's unexported
// userEmailAAD -- the same "mirror by hand rather than widen an interface"
// precedent cmd/identity-migrate's userEmailAADForMigration already follows.
// A mismatch here does not fail now: it produces a ciphertext the api can
// never decrypt, which surfaces only when the customer first logs in.
func userEmailAAD(issuer, subject string) string {
	return "invoice-user-email\n" + issuer + "\n" + subject
}

func modeLabel(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry run"
}

func printSummary(out io.Writer, platform, issuer, externalUserID string, result postgresstore.OperatorBindResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "account-bind XM-INV-SHADOW-BINDING: %s\n\n", mode)
	fmt.Fprintf(out, "platform:            %s\n", platform)
	fmt.Fprintf(out, "issuer:              %s\n", issuer)
	fmt.Fprintf(out, "external_user_id:    %s\n", externalUserID)
	fmt.Fprintf(out, "source_instance_id:  %s\n\n", result.SourceInstanceID)

	gate := "GO"
	if !result.GateSatisfied {
		gate = "NO-GO"
	}
	fmt.Fprintf(out, "timing gate:         %s (%s)\n", gate, result.GateReason)
	fmt.Fprintf(out, "ingest pending:      %d\n", result.Health.Pending)
	fmt.Fprintf(out, "ingest dead:         %d (contained %d)\n", result.Health.Dead, result.Health.DeadContained)
	fmt.Fprintf(out, "ingest waiting:      %d\n\n", result.Health.Waiting)

	userLabel := "reused"
	if result.UserCreated {
		userLabel = "created"
	}
	bindingLabel := "updated in place"
	if result.BindingCreated {
		bindingLabel = "created"
	}
	fmt.Fprintf(out, "invoice_user_id:     %s (%s)\n", result.InvoiceUserID, userLabel)
	fmt.Fprintf(out, "external_account_id: %s (%s)\n", result.ExternalAccountID, bindingLabel)
	fmt.Fprintf(out, "binding:             %s / %s\n\n", result.BindingMethod, result.BindingStatus)

	fmt.Fprintf(out, "PRE_POLICY_SKIPPED:  %d (irreversible)\n", result.PrePolicySkipped)
	fmt.Fprintf(out, "released to queued:  %d\n\n", result.Released)

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
