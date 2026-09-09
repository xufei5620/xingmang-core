package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

// BindingMethodOperatorAttested is the external_accounts.binding_method value
// written by an operator-attested (shadow) binding: the operator copied the
// upstream user id out of the platform's own admin console and vouched for
// it, rather than the account owner proving possession by logging in.
//
// It is deliberately a NEW value rather than reusing platform_password_login.
// binding_method is free text with no CHECK and no reader outside this file
// (it reaches no wire, no admin ledger, no user-facing panel), so the ONLY
// thing that will ever distinguish a self-proved binding from one an operator
// asserted is this string in the row and in the audit trail. Reusing the
// existing value to save two lines would make that distinction unrecoverable.
const BindingMethodOperatorAttested = "operator_attested"

// operatorBoundAction is the audit action that carries the approving
// operator's id. external_account.bound is written too (by
// bindExternalAccountTx, exactly as a real login would), but its actor is the
// binding's own principal; this second row is what answers "who decided to
// create this binding on the customer's behalf".
const operatorBoundAction = "external_account.operator_bound"

// ErrOperatorBindTimingGate is returned when --apply is refused because the
// deployment is not in a state to accept a bind. It is a sentinel so the CLI
// can exit with its own code: "come back in the next quiet window" is a
// different instruction from "this failed", and an operator reading only the
// exit status must be able to tell them apart.
var ErrOperatorBindTimingGate = errors.New("source ingestion timing gate is not satisfied")

// ErrOperatorBindWouldOverwriteBinding is returned when the external account
// already carries a binding this tool did not create -- above all a
// platform_password_login one, which means the customer already proved
// ownership themselves. Overwriting it is silent and irreversible, so it is
// refused in BOTH modes rather than merely reported: a dry run that printed a
// plan here would be printing a plan to destroy evidence.
var ErrOperatorBindWouldOverwriteBinding = errors.New("external account already has a binding this tool did not create")

// ErrOperatorBindIdentityProjectionOpen is returned when unprocessed
// identity_binding ingest events exist. See countOpenIdentityBindingEvents.
var ErrOperatorBindIdentityProjectionOpen = errors.New("unprocessed identity-binding ingest events exist")

// blindIndexPattern matches securefields.Keyring.BlindIndex output. This
// package deliberately has no securefields dependency (see
// eligibility_repair.go), so the caller derives both HMACs and this only
// checks their shape -- enough to catch a caller that passed an empty string
// or a raw user id, not enough to catch a wrong namespace. The namespaces
// themselves are defined once, in application/binding_keys.go.
var blindIndexPattern = regexp.MustCompile(`^h1:[0-9a-f]{64}$`)

// OperatorBindInput drives OperatorBindExternalAccount. Issuer and
// ExternalUserID must be exactly the pair a later platform-password login
// would mint for this account (issuer = the platform's configured login
// origin, subject = the upstream user id), or the customer's first real login
// mints a second, orphaned invoice_user beside the shadow one.
type OperatorBindInput struct {
	Platform            string
	Issuer              string
	ExternalUserID      string
	ExternalSubjectHMAC string
	DependencyKeyHMAC   string

	// OIDCUserDependencyKeyHMAC is the invoice_oidc_user wake key for this
	// shadow identity. Service.EnsureUser fires that wake on every identity it
	// mints (service.go, right after EnsureUserAndSyncOIDCEmail); a bind that
	// mints an identity without it leaves any fact parked on
	// invoice_oidc_user for this pair frozen for the same reason a missing
	// source_external_account wake would. Derived by the caller with
	// application.SourceDependencyKeyHMAC("invoice_oidc_user", "",
	// issuer+"\n"+subject) -- note the empty source instance, matching
	// Service.EnsureUser exactly.
	OIDCUserDependencyKeyHMAC string
	EmailCiphertext           []byte
	EmailVerified             bool
	Apply                     bool
	OperatorID                string
}

// OperatorBindResult is the full outcome, printable as the CLI's summary. It
// is populated identically in both modes: a dry run executes every statement
// inside the transaction and then rolls back, so PrePolicySkipped and
// Released are measured, not estimated.
type OperatorBindResult struct {
	Applied           bool
	GateSatisfied     bool
	GateReason        string
	Health            SourceIngestHealth
	SourceInstanceID  string
	InvoiceUserID     string
	UserCreated       bool
	ExternalAccountID string
	BindingCreated    bool
	BindingMethod     string
	BindingStatus     string
	PrePolicySkipped  int64
	Released          int64

	// FactsEverSeen counts the source_ingest_events rows this external id has
	// ever been associated with -- parked on this binding right now
	// (dependency_key_hmac), or released by an earlier wake
	// (catchup_key_hmac). Zero on a freshly created binding means this
	// database has never heard of this id at all, which is what a mistyped
	// upstream id usually looks like.
	//
	// It is NOT a proof of correctness, and the summary must not present it
	// as one. An id mistyped into a DIFFERENT real but never-bound customer
	// has a perfectly healthy non-zero count, and no query here can tell that
	// apart from the intended customer -- only the operator copying the id
	// verbatim out of the upstream console can.
	FactsEverSeen int64

	// PlatformIssuerInUse is the oidc_issuer that every existing
	// invoice_users row for this platform already carries, or "" when this
	// bind is creating the first row for the platform. See
	// checkPlatformIssuerConsistency for why it is checked at all.
	PlatformIssuerInUse string

	// ExistingBindingMethod is the binding_method already on the external
	// account, or "" when there is no row yet. Reported so an operator sees
	// what they were about to overwrite, not only that they were refused.
	ExistingBindingMethod string

	// IdentityBindingEventsOpen counts identity_binding ingest events that are
	// not yet processed, across the whole deployment. See
	// countOpenIdentityBindingEvents for why it cannot be scoped to one
	// upstream id.
	IdentityBindingEventsOpen int64

	// DependencyKeyHMAC is echoed back so the runbook's observation-window
	// queries have something to paste: the parked rows are keyed by this
	// blind index and an operator cannot derive it by hand. It is an HMAC of
	// (source instance, external user id) under the index key, already stored
	// in plaintext in source_ingest_events, so echoing it discloses nothing
	// the database does not already hold.
	DependencyKeyHMAC string
}

// OperatorBindExternalAccount is the shadow binding of design
// XM-INV-SHADOW-BINDING section B option 1: it creates (or reuses) the
// invoice_user a platform-password login for this upstream account would land
// on, binds the external account to it as BindingMethodOperatorAttested /
// binding_status 'verified', and wakes the source facts parked on that
// binding -- all four in ONE transaction, so the database never holds a
// shadow user without its binding, or a binding whose parked facts were never
// released.
//
// Two rules it enforces that no caller may relax:
//
//   - The binding row must already belong to this invoice_user or not exist:
//     bindExternalAccountTx's UPSERT guard turns "belongs to somebody else"
//     into domain.ErrForbidden, and this is the whole reason the operator
//     cannot quietly move a real customer's funding history onto a new row.
//   - An invoice_user that already exists at (Issuer, ExternalUserID) must
//     carry either no platform identity or exactly this one. A row holding a
//     different (platform, platform_user_id) is refused rather than claimed,
//     mirroring the check auth.(*PostgresIdentityStore).ResolveOrCreate
//     applies to a real login.
//
// A dry run (Apply==false) runs every statement, including both wake UPDATEs,
// and then always rolls back -- the printed counts are what an apply would do
// to the exact rows present now. It does take brief row locks on the parked
// events while it runs; the runbook says to run it in the same low-traffic
// window as the apply for that reason.
func (s *Store) OperatorBindExternalAccount(ctx context.Context, in OperatorBindInput, actor AuditActor) (OperatorBindResult, error) {
	platform := strings.TrimSpace(in.Platform)
	issuer := strings.TrimSpace(in.Issuer)
	externalUserID := strings.TrimSpace(in.ExternalUserID)
	operatorID := strings.TrimSpace(in.OperatorID)
	if platform != "sub2api" && platform != "newapi" {
		return OperatorBindResult{}, fmt.Errorf("platform must be sub2api or newapi, got %q", in.Platform)
	}
	if issuer == "" || strings.HasSuffix(issuer, "/") || strings.ContainsAny(issuer, " \r\n\t") {
		return OperatorBindResult{}, errors.New("issuer must be the platform's exact login origin, with no trailing slash")
	}
	if externalUserID == "" || externalUserID != in.ExternalUserID || len(externalUserID) > 512 {
		return OperatorBindResult{}, errors.New("external user id must be a non-empty, untrimmed value of at most 512 bytes")
	}
	if !blindIndexPattern.MatchString(in.ExternalSubjectHMAC) {
		return OperatorBindResult{}, errors.New("external subject blind index is required (see application.PlatformBindingSubjectIndex)")
	}
	if !blindIndexPattern.MatchString(in.DependencyKeyHMAC) {
		return OperatorBindResult{}, errors.New("source dependency blind index is required (see application.SourceDependencyKeyHMAC)")
	}
	if !blindIndexPattern.MatchString(in.OIDCUserDependencyKeyHMAC) {
		return OperatorBindResult{}, errors.New("invoice_oidc_user dependency blind index is required (see OperatorBindInput.OIDCUserDependencyKeyHMAC)")
	}
	if in.Apply && operatorID == "" {
		return OperatorBindResult{}, errors.New("an approving operator id is required to apply")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return OperatorBindResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// This tool connects as invoice_owner, which -- unlike invoice_app
	// (deploy/postgres/010-invoice-roles.sh sets 15s statement / 5s lock / 15s
	// idle on that role) -- carries no timeouts at all. The transaction below
	// takes FOR UPDATE on invoice_users and external_accounts and then
	// batch-UPDATEs thousands of source_ingest_events rows, so without these
	// it could sit waiting on a lock for the CLI's whole five-minute context
	// while holding its own locks against the live api. lock_timeout matches
	// every batch precedent in deploy/postgres (balance-history-cleanup.sql
	// and the two apply-*.sh scripts all use 5s); statement_timeout is set to
	// the CLI's own context deadline so the server gives up at the same moment
	// the client does instead of grinding on behind an abandoned connection.
	for _, statement := range []string{
		`SET LOCAL lock_timeout='5s'`,
		`SET LOCAL statement_timeout='5min'`,
		`SET LOCAL idle_in_transaction_session_timeout='15s'`,
	} {
		if _, err = tx.Exec(ctx, statement); err != nil {
			return OperatorBindResult{}, err
		}
	}

	result := OperatorBindResult{}
	if result.Health, err = sourceIngestHealthTx(ctx, tx); err != nil {
		return OperatorBindResult{}, err
	}
	result.GateSatisfied, result.GateReason = operatorBindTimingGate(result.Health)
	if in.Apply && !result.GateSatisfied {
		return OperatorBindResult{}, fmt.Errorf("%w: %s", ErrOperatorBindTimingGate, result.GateReason)
	}

	if result.SourceInstanceID, err = getEnabledSourceInstanceID(ctx, tx, platform); err != nil {
		return OperatorBindResult{}, err
	}
	result.DependencyKeyHMAC = in.DependencyKeyHMAC

	// Serialize against the other two external_accounts writers
	// (BindExternalAccountFromSource and RevokeExternalAccountFromSource, both
	// in identity.go) on the same key and the same seed: seed 4 is the "one
	// upstream account on one source" lock namespace, and the key is exactly
	// what those two use. Taken here, immediately after the source instance is
	// known, and before anything is read that the decision depends on.
	//
	// Serializable isolation alone would probably do it today -- the UPSERT
	// would raise 40001 -- but the safety of this operation must not rest on
	// the isolation level: the day somebody lowers it to Read Committed to
	// stop seeing 40001, that protection disappears and nothing reports it.
	// The row-level FOR UPDATE further down cannot substitute either, because
	// it locks nothing when the row does not yet exist, which is the normal
	// case for a shadow bind.
	//
	// Lock order here is external_accounts (this advisory key) ->
	// invoice_users -> source_ingest_events; the projection writers never
	// touch invoice_users, so no cycle is possible.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,4))`,
		result.SourceInstanceID+"\n"+externalUserID); err != nil {
		return OperatorBindResult{}, err
	}

	if result.PlatformIssuerInUse, err = checkPlatformIssuerConsistency(ctx, tx, platform, result.SourceInstanceID, issuer); err != nil {
		return OperatorBindResult{}, err
	}
	if result.FactsEverSeen, err = countFactsEverSeen(ctx, tx, in.DependencyKeyHMAC); err != nil {
		return OperatorBindResult{}, err
	}
	if result.IdentityBindingEventsOpen, err = countOpenIdentityBindingEvents(ctx, tx); err != nil {
		return OperatorBindResult{}, err
	}
	if in.Apply && result.IdentityBindingEventsOpen > 0 {
		return OperatorBindResult{}, fmt.Errorf(
			"%d identity-binding ingest event(s) are still unprocessed: %w",
			result.IdentityBindingEventsOpen, ErrOperatorBindIdentityProjectionOpen)
	}

	user, created, err := ensureUserTx(ctx, tx, UserRecord{
		OIDCIssuer: issuer, OIDCSubject: externalUserID, Status: "active",
		EmailCiphertext: in.EmailCiphertext, EmailVerified: in.EmailVerified,
	}, actor)
	if err != nil {
		return OperatorBindResult{}, err
	}
	result.InvoiceUserID, result.UserCreated = user.ID, created

	storedPlatform, storedPlatformUserID, err := claimPlatformIdentityTx(ctx, tx, user.ID, platform, externalUserID, actor)
	if err != nil {
		return OperatorBindResult{}, fmt.Errorf("claim shadow platform identity: %w", err)
	}
	if storedPlatform != platform || storedPlatformUserID != externalUserID {
		// This invoice_user already belongs to a different platform account.
		// Binding here would collapse two upstream accounts onto one identity
		// and hand the wrong customer the other's funding history.
		return OperatorBindResult{}, fmt.Errorf(
			"invoice_user %s already carries platform identity %q/%q, refusing to bind %q/%q: %w",
			user.ID, storedPlatform, storedPlatformUserID, platform, externalUserID, domain.ErrForbidden)
	}

	var existingOwner string
	ownerErr := tx.QueryRow(ctx, `
		SELECT invoice_user_id,binding_method FROM external_accounts
		WHERE source_instance_id=$1 AND external_user_id=$2 FOR UPDATE`,
		result.SourceInstanceID, externalUserID).Scan(&existingOwner, &result.ExistingBindingMethod)
	if ownerErr != nil && !errors.Is(ownerErr, pgx.ErrNoRows) {
		return OperatorBindResult{}, ownerErr
	}
	result.BindingCreated = errors.Is(ownerErr, pgx.ErrNoRows)

	// Refuse to overwrite a binding the customer proved themselves.
	//
	// bindExternalAccountTx's UPSERT only refuses a row owned by SOMEBODY
	// ELSE. When the customer has already logged in with their platform
	// password, the row is owned by the very invoice_user this bind resolves
	// to -- (platform origin, upstream id) is the same pair both paths use --
	// so the UPSERT happily takes the DO UPDATE branch and rewrites
	// binding_method from platform_password_login to operator_attested,
	// resets verified_at to now(), and files an operator_bound audit row
	// claiming the operator established a binding the customer had in fact
	// established themselves. Nothing errors, and there is no way back:
	// binding_method is the only surviving record of which happened.
	//
	// An existing operator_attested row is allowed through, because that is
	// the idempotent re-run of this same tool.
	if !result.BindingCreated && result.ExistingBindingMethod != BindingMethodOperatorAttested {
		return OperatorBindResult{}, fmt.Errorf(
			"external account %s/%s already carries a %q binding (owner invoice_user %s); "+
				"refusing to overwrite a binding this tool did not create: %w",
			result.SourceInstanceID, externalUserID, result.ExistingBindingMethod, existingOwner,
			ErrOperatorBindWouldOverwriteBinding)
	}

	bound, err := bindExternalAccountTx(ctx, tx, ExternalAccountRecord{
		PrincipalID: user.ID, SourceInstanceID: result.SourceInstanceID,
		ExternalUserID: externalUserID, ExternalSubjectHMAC: in.ExternalSubjectHMAC,
		BindingMethod: BindingMethodOperatorAttested, BindingStatus: "verified",
	}, actor)
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			return OperatorBindResult{}, fmt.Errorf(
				"external account %s/%s is already bound to invoice_user %s: %w",
				result.SourceInstanceID, externalUserID, existingOwner, domain.ErrForbidden)
		}
		return OperatorBindResult{}, err
	}
	result.ExternalAccountID = bound.ID
	result.BindingMethod, result.BindingStatus = bound.BindingMethod, bound.BindingStatus

	if err = writeAudit(ctx, tx, actor, operatorBoundAction, "external_account", bound.ID, nil, map[string]any{
		"operator_id": operatorID, "invoice_user_id": user.ID,
		"source_instance_id": result.SourceInstanceID, "external_user_id": externalUserID,
		"binding_method": BindingMethodOperatorAttested, "user_created": created,
	}); err != nil {
		return OperatorBindResult{}, err
	}

	// Step four, first half. Service.BindExternalAccount fires this wake only
	// for platform_password_login, so an operator_attested bind that skipped
	// it would leave every parked fact for this customer frozen forever:
	// parked events are excluded from the periodic sweep by design.
	if result.PrePolicySkipped, result.Released, err = requeueSourceDependencyTx(ctx, tx,
		"source_external_account", in.DependencyKeyHMAC); err != nil {
		return OperatorBindResult{}, err
	}

	// Step four, second half. Minting an identity has its own wake, which
	// Service.EnsureUser fires and this originally missed: facts can park on
	// invoice_oidc_user for a (issuer, subject) pair that does not exist yet,
	// and this bind is what makes it exist. Same failure mode as the wake
	// above -- silent, permanent, invisible to every health surface. Counted
	// into the same Released/PrePolicySkipped totals because from the
	// operator's side it is one backlog being let go.
	oidcSkipped, oidcReleased, err := requeueSourceDependencyTx(ctx, tx,
		"invoice_oidc_user", in.OIDCUserDependencyKeyHMAC)
	if err != nil {
		return OperatorBindResult{}, err
	}
	result.PrePolicySkipped += oidcSkipped
	result.Released += oidcReleased

	if !in.Apply {
		return result, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return OperatorBindResult{}, err
	}
	result.Applied = true
	return result, nil
}

// checkPlatformIssuerConsistency refuses an issuer that disagrees with the one
// the running api demonstrably mints for platform logins on this source, and
// returns that in-use issuer (or "" when this source has no such identity yet).
//
// The issuer reaching this function came from the operator's environment, and
// a wrong one is written permanently: the customer's later real login claims
// the shadow row through the external account, never through (issuer,
// subject), so it does not repair the column -- proved by cmd/api's
// TestShadowBindWithAWrongIssuerStillClaimsButLeavesTheIdentityWrong. Every
// session identity hash and the email AAD derive from it. So rather than
// trusting the environment, the value is checked against production evidence.
//
// Choosing that evidence correctly is the whole difficulty, and the first
// version of this got it wrong. It compared against every invoice_users row
// with platform=<platform>, which on a fixture containing only platform-login
// identities looked right and on the real database was not: production also
// holds an identity minted by the CENTRAL OIDC provider (issuer
// auth.solov.cc/realms/solov) that later claimed a sub2api platform identity,
// so the query returned two issuers and the "more than one, refuse until
// explained" branch would have rejected every single sub2api bind on the very
// first production dry run.
//
// The population that actually answers the question is: identities owning a
// binding on THIS source that was created by a platform password login or by
// this tool. Those are exactly the identities whose oidc_issuer the api
// derived from the platform login origin. A source_signed_oidc_projection
// binding belongs to a centrally-minted identity whose issuer is legitimately
// something else, so it is excluded rather than treated as a contradiction.
//
// Scoping by external_accounts.source_instance_id rather than by
// invoice_users.platform is also deliberate: that column records only the
// FIRST platform a login claimed (see runtime.go's claim branch and the RC57
// canary), so a multi-platform identity carries just one of its platforms
// there and would be scoped wrongly.
//
// What this still cannot do: on a source whose first platform-login identity
// this bind is creating, there is nothing to compare against and the check
// passes vacuously. That first bind's issuer line has to be read by a human,
// which is why the summary flags it.
func checkPlatformIssuerConsistency(ctx context.Context, tx pgx.Tx, platform, sourceInstanceID, issuer string) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT u.oidc_issuer
		FROM invoice_users u
		JOIN external_accounts a ON a.invoice_user_id=u.id
		WHERE a.source_instance_id=$1 AND a.binding_method IN ('platform_password_login',$2)
		ORDER BY u.oidc_issuer LIMIT 5`, sourceInstanceID, BindingMethodOperatorAttested)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var inUse []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return "", err
		}
		inUse = append(inUse, value)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(inUse) == 0 {
		return "", nil
	}
	if len(inUse) > 1 {
		return "", fmt.Errorf(
			"%d different oidc_issuer values are in use by platform-login identities on source %s (%s); refusing to add another until that is explained",
			len(inUse), sourceInstanceID, strings.Join(inUse, ", "))
	}
	if inUse[0] != issuer {
		return "", fmt.Errorf(
			"issuer %q does not match the issuer every platform-login %s identity carries (%q); the login origin this tool read from the environment is not the one the running api uses",
			issuer, platform, inUse[0])
	}
	return inUse[0], nil
}

// countFactsEverSeen counts the ingest events this external id is or was
// associated with: still parked on the binding (dependency_key_hmac), or
// already released by an earlier wake (catchup_key_hmac). See
// OperatorBindResult.FactsEverSeen for what this can and cannot prove.
func countFactsEverSeen(ctx context.Context, tx pgx.Tx, dependencyKeyHMAC string) (int64, error) {
	var count int64
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM source_ingest_events
		WHERE dependency_key_hmac=$1 OR catchup_key_hmac=$1`, dependencyKeyHMAC).Scan(&count)
	return count, err
}

// countOpenIdentityBindingEvents counts identity_binding ingest events that
// have not reached 'processed'.
//
// Why it is deployment-wide rather than scoped to this upstream id, even
// though that is what an operator actually wants to know: a parked
// identity_binding event waits on dependency kind invoice_oidc_user, whose
// blind index is derived from the CENTRAL OIDC issuer and provider subject
// (source_processor.go's processIdentityBinding resolves the owner with
// FindUserByOIDC on those). This tool knows the platform login origin and the
// upstream user id -- a different pair entirely -- and the event payload is
// encrypted, so there is no way from here to tell which upstream customer a
// parked identity_binding event belongs to. Reporting a deployment-wide count
// and saying so is honest; reporting it as if it were per-id would not be.
//
// It is cheap to be strict about: production carried exactly two of these
// events on 2026-09-09, both processed since 2026-08-26, so a non-zero count
// is already an anomaly worth stopping for.
func countOpenIdentityBindingEvents(ctx context.Context, tx pgx.Tx) (int64, error) {
	var count int64
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM source_ingest_events
		WHERE entity_type='identity_binding' AND processing_status<>'processed'`).Scan(&count)
	return count, err
}

// operatorBindTimingGate is design XM-INV-SHADOW-BINDING section B guardrail
// 4's "readyz 200, Dead=0, no EVENTS_PENDING" timing door, expressed against
// the same evidence /readyz reads.
//
// It is deliberately STRICTER than /readyz on one axis: readyz forgives a
// pending backlog younger than fifteen minutes, because the api must stay up
// while the worker catches up. A shadow bind must not start into any backlog
// at all -- the whole point of the one-at-a-time rule is that the operator
// can attribute every subsequent pending event to the bind they just made.
// The dead-event half is not a second copy of readyz's rule: it calls
// SourceIngestHealth.UncontainedDead, the same method readyz calls.
func operatorBindTimingGate(health SourceIngestHealth) (bool, string) {
	uncontainedDead, consistent := health.UncontainedDead()
	if !consistent {
		return false, "dead-event evidence is inconsistent (contained exceeds dead)"
	}
	if uncontainedDead > 0 {
		return false, fmt.Sprintf("%d uncontained dead ingest event(s); /readyz would be 503", uncontainedDead)
	}
	if health.Pending > 0 {
		return false, fmt.Sprintf("%d pending ingest event(s) (EVENTS_PENDING); wait for the backlog to drain", health.Pending)
	}
	return true, "no uncontained dead events, no pending events"
}
