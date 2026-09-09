package postgresstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"invoice-system/backend/internal/domain"
)

// The fixture below is deliberately hand-rolled rather than built on
// seedReadySourceStreams: an operator-attested bind is only ever run against
// an account with NO binding at all, so the interesting state is the parked
// facts, not a healthy stream.
const (
	shadowSourceID   = "10000000-0000-4000-8000-0000000000d0"
	shadowBatchID    = "80000000-0000-4000-8000-0000000000d0"
	shadowIssuer     = "https://api.solov.cc"
	shadowExternalID = "7788"
	shadowOperatorID = "70000000-0000-4000-8000-0000000000d0"
)

// testBlindIndex mirrors securefields.Keyring.BlindIndex's output shape with a
// fixed key. This package has no securefields dependency by design (the real
// callers derive the value and pass it in), so the tests do the same.
func testBlindIndex(namespace, value string) string {
	mac := hmac.New(sha256.New, []byte(strings.Repeat("i", 32)))
	normalized := strings.ToUpper(strings.Join(strings.Fields(value), ""))
	_, _ = mac.Write([]byte(namespace + "\n" + normalized))
	return fmt.Sprintf("h1:%x", mac.Sum(nil))
}

func shadowSubjectHMAC(externalUserID string) string {
	return testBlindIndex("external-platform/"+shadowSourceID, externalUserID)
}

func shadowDependencyKey(externalUserID string) string {
	return testBlindIndex("source-dependency/source_external_account", shadowSourceID+"\n"+externalUserID)
}

func shadowBindInput(externalUserID string, apply bool) OperatorBindInput {
	return OperatorBindInput{
		Platform: "sub2api", Issuer: shadowIssuer, ExternalUserID: externalUserID,
		ExternalSubjectHMAC: shadowSubjectHMAC(externalUserID),
		DependencyKeyHMAC:   shadowDependencyKey(externalUserID),
		Apply:               apply, OperatorID: shadowOperatorID,
	}
}

func shadowActor() AuditActor {
	return AuditActor{Type: "operator", ID: shadowOperatorID, RequestID: "account-bind-test", Reason: "shadow binding test"}
}

// seedShadowBindFixture installs the enabled source instance plus one parked
// batch, and returns the store/ctx. Each parked event is created through the
// same columns the ingest pipeline writes, so RequeueSourceDependency's
// predicates apply unchanged.
func seedShadowBindFixture(t *testing.T) (*Store, context.Context, time.Time) {
	t.Helper()
	policyStart := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	// integrationStore's own sub2api instance already owns an
	// external_accounts row with a NULL external_subject_hmac -- the single
	// NULL slot per source that this design's HMAC derivation exists to stop
	// competing for. Retire it and stand up a clean instance instead, so the
	// tests below start from the state a real never-bound customer is in.
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET enabled=FALSE WHERE source_type='sub2api'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
		VALUES($1,'sub2api','shadow-bind fixture','fixture-runtime',TRUE)`, shadowSourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_state(source_instance_id,stream_id) VALUES($1,'usage')`, shadowSourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'usage',$2,1,repeat('a',64),'shadow-key',0,'fixture-runtime','fixture-agent',now(),'healthy')`,
		shadowSourceID, shadowBatchID); err != nil {
		t.Fatal(err)
	}
	return store, ctx, policyStart
}

// parkEvent inserts one source_ingest_events row parked on externalUserID's
// binding. observedAt before the policy start is what
// requeueSourceDependencyTx writes off as PRE_POLICY_SKIPPED; at or after it,
// the row is released to 'queued'.
func parkEvent(t *testing.T, store *Store, ctx context.Context, eventID, entityType, externalUserID string, observedAt time.Time) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,dependency_kind,dependency_key_hmac,
			attempt_count,created_at,updated_at)
		VALUES($1,'usage',$2,$3,$4,'upsert',$5,decode(repeat('11',16),'hex'),$6,'parked_identity',
			'source_external_account',$7,0,now(),now())`,
		shadowSourceID, eventID, shadowBatchID, entityType,
		strings.Repeat("c", 64), observedAt, shadowDependencyKey(externalUserID)); err != nil {
		t.Fatal(err)
	}
}

func eventStatus(t *testing.T, store *Store, ctx context.Context, eventID string) (status string, processingError string) {
	t.Helper()
	if err := store.pool.QueryRow(ctx, `
		SELECT processing_status,COALESCE(processing_error,'') FROM source_ingest_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		shadowSourceID, eventID).Scan(&status, &processingError); err != nil {
		t.Fatal(err)
	}
	return status, processingError
}

// countUsers counts only the identities minted under the platform's login
// origin, so integrationStore's own unrelated fixture user never masks a
// shadow row that should or should not exist.
func countUsers(t *testing.T, store *Store, ctx context.Context) int64 {
	t.Helper()
	var count int64
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM invoice_users WHERE oidc_issuer=$1`, shadowIssuer).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// countShadowBindings counts external_accounts rows on the fixture's own
// source instance, excluding integrationStore's unrelated seed row.
func countShadowBindings(t *testing.T, store *Store, ctx context.Context) int64 {
	t.Helper()
	var count int64
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM external_accounts WHERE source_instance_id=$1`,
		shadowSourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestOperatorBindWakesParkedFactsInTheSameTransaction is the wake half of
// XM-INV-SHADOW-BINDING. Service.BindExternalAccount fires the parked-fact
// wake only for binding_method platform_password_login, so an
// operator_attested bind that did not fire it itself would leave every fact
// for this customer parked forever -- parked_identity rows are excluded from
// the periodic sweep by design, so nothing would ever report the omission.
//
// Mutation that must turn this red: delete the requeueSourceDependencyTx call
// from OperatorBindExternalAccount. The bind still succeeds, the summary still
// prints, and only the two assertions below notice.
//
// The pre-policy event is asserted separately because it takes the OTHER
// branch of the same wake (written off as PRE_POLICY_SKIPPED rather than
// released); a test that only checked the released count would pass with the
// irreversible half of the wake missing.
func TestOperatorBindWakesParkedFactsInTheSameTransaction(t *testing.T) {
	store, ctx, policyStart := seedShadowBindFixture(t)
	const (
		inWindowEvent  = "82000000-0000-4000-8000-0000000000d1"
		prePolicyEvent = "82000000-0000-4000-8000-0000000000d2"
		paymentEvent   = "82000000-0000-4000-8000-0000000000d3"
	)
	parkEvent(t, store, ctx, inWindowEvent, "usage_event", shadowExternalID, policyStart.Add(time.Hour))
	parkEvent(t, store, ctx, prePolicyEvent, "usage_event", shadowExternalID, policyStart.Add(-time.Hour))
	// A payment is never written off by the pre-policy branch, whatever its
	// observed_at: it is released like any other fact.
	parkEvent(t, store, ctx, paymentEvent, "payment", shadowExternalID, policyStart.Add(-2*time.Hour))

	result, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("apply reported not applied")
	}
	if result.Released != 2 {
		t.Fatalf("released=%d want 2 (the in-window usage event and the payment)", result.Released)
	}
	if result.PrePolicySkipped != 1 {
		t.Fatalf("pre-policy skipped=%d want 1", result.PrePolicySkipped)
	}
	for _, event := range []string{inWindowEvent, paymentEvent} {
		if status, _ := eventStatus(t, store, ctx, event); status != "queued" {
			t.Fatalf("event %s is %q, want queued -- the parked facts were never woken", event, status)
		}
	}
	if status, reason := eventStatus(t, store, ctx, prePolicyEvent); status != "processed" || reason != "PRE_POLICY_SKIPPED" {
		t.Fatalf("pre-policy event is %q/%q, want processed/PRE_POLICY_SKIPPED", status, reason)
	}
}

// TestOperatorBindRollsBackEverythingOnDryRun proves the four steps really do
// share one transaction, from the only direction that can prove it: a dry run
// executes all of them and then rolls back, so nothing at all is left behind.
// If the steps ran in their own transactions (as the Store methods they were
// extracted from still do), the user and binding would survive the rollback.
func TestOperatorBindRollsBackEverythingOnDryRun(t *testing.T) {
	store, ctx, policyStart := seedShadowBindFixture(t)
	const dryRunEvent = "82000000-0000-4000-8000-0000000000d4"
	parkEvent(t, store, ctx, dryRunEvent, "usage_event", shadowExternalID, policyStart.Add(time.Hour))

	result, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, false), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("dry run reported applied")
	}
	// The dry run's numbers are measured, not estimated: it ran the very same
	// UPDATEs before rolling back.
	if !result.UserCreated || !result.BindingCreated || result.Released != 1 {
		t.Fatalf("dry run summary is not a real plan: %+v", result)
	}
	if count := countUsers(t, store, ctx); count != 0 {
		t.Fatalf("dry run left %d invoice_users row(s) behind", count)
	}
	if bindings := countShadowBindings(t, store, ctx); bindings != 0 {
		t.Fatalf("dry run left %d external_accounts row(s) behind", bindings)
	}
	if status, _ := eventStatus(t, store, ctx, dryRunEvent); status != "parked_identity" {
		t.Fatalf("dry run left the event at %q, want parked_identity", status)
	}
}

// TestOperatorBindRequiresBlindIndexBecauseNullsCollide is the HMAC half.
//
// external_accounts carries UNIQUE NULLS NOT DISTINCT (source_instance_id,
// external_subject_hmac), so at most ONE row per source may leave that column
// NULL -- the RC55 production canary found this when the second real user's
// first login failed with SQLSTATE 23505. An operator_attested bind derives
// its own index (Service.BindExternalAccount derives one only for
// platform_password_login), so the store refuses an input without one.
//
// Both halves are needed. The first proves the guard exists; the second
// proves the guard is load-bearing rather than decorative, by showing what
// the database actually does to a second NULL index. Mutation that must turn
// this red: delete the blindIndexPattern check on ExternalSubjectHMAC from
// OperatorBindExternalAccount -- the first subtest's bind then succeeds.
func TestOperatorBindRequiresBlindIndexBecauseNullsCollide(t *testing.T) {
	store, ctx, _ := seedShadowBindFixture(t)

	t.Run("refuses an input with no derived index", func(t *testing.T) {
		in := shadowBindInput(shadowExternalID, true)
		in.ExternalSubjectHMAC = ""
		if _, err := store.OperatorBindExternalAccount(ctx, in, shadowActor()); err == nil {
			t.Fatal("accepted a binding with no external subject blind index")
		}
	})

	t.Run("two null indexes collide with 23505", func(t *testing.T) {
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		for i, externalUserID := range []string{"5001", "5002"} {
			user, _, userErr := ensureUserTx(ctx, tx, UserRecord{
				OIDCIssuer: shadowIssuer, OIDCSubject: externalUserID, Status: "active",
			}, shadowActor())
			if userErr != nil {
				t.Fatal(userErr)
			}
			_, bindErr := bindExternalAccountTx(ctx, tx, ExternalAccountRecord{
				PrincipalID: user.ID, SourceInstanceID: shadowSourceID, ExternalUserID: externalUserID,
				BindingMethod: BindingMethodOperatorAttested, BindingStatus: "verified",
			}, shadowActor())
			if i == 0 {
				if bindErr != nil {
					t.Fatalf("first null-index binding failed: %v", bindErr)
				}
				continue
			}
			var pgErr *pgconn.PgError
			if !errors.As(bindErr, &pgErr) || pgErr.Code != "23505" {
				t.Fatalf("second null-index binding returned %v, want SQLSTATE 23505", bindErr)
			}
		}
	})

	t.Run("two derived indexes coexist", func(t *testing.T) {
		for _, externalUserID := range []string{"6001", "6002"} {
			if _, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(externalUserID, true), shadowActor()); err != nil {
				t.Fatalf("binding %s failed: %v", externalUserID, err)
			}
		}
	})
}

// TestOperatorBindRefusesAnExternalAccountOwnedBySomebodyElse is the guardrail
// that stops an operator from moving a real customer's funding history onto a
// freshly minted row by mistyping an upstream id.
func TestOperatorBindRefusesAnExternalAccountOwnedBySomebodyElse(t *testing.T) {
	store, ctx, _ := seedShadowBindFixture(t)
	owner, err := store.EnsureUser(ctx, UserRecord{
		OIDCIssuer: shadowIssuer, OIDCSubject: "9999", Status: "active",
	}, shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindExternalAccount(ctx, ExternalAccountRecord{
		PrincipalID: owner.ID, SourceInstanceID: shadowSourceID, ExternalUserID: shadowExternalID,
		ExternalSubjectHMAC: shadowSubjectHMAC(shadowExternalID),
		BindingMethod:       "platform_password_login", BindingStatus: "verified",
	}, shadowActor()); err != nil {
		t.Fatal(err)
	}

	_, err = store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("bind onto somebody else's external account returned %v, want domain.ErrForbidden", err)
	}
	// The refusal must also have rolled back the shadow user the attempt
	// created on its way to the binding step.
	if count := countUsers(t, store, ctx); count != 1 {
		t.Fatalf("%d invoice_users rows after a refused bind, want 1 (the pre-existing owner)", count)
	}
	var method string
	if err = store.pool.QueryRow(ctx, `SELECT binding_method FROM external_accounts
		WHERE source_instance_id=$1 AND external_user_id=$2`, shadowSourceID, shadowExternalID).Scan(&method); err != nil {
		t.Fatal(err)
	}
	if method != "platform_password_login" {
		t.Fatalf("the existing binding's method became %q; the refusal did not roll back", method)
	}
}

// TestOperatorBindRefusesAMismatchedPlatformIdentity covers the second
// rejection: an invoice_users row already sitting at (issuer, subject) but
// carrying a DIFFERENT (platform, platform_user_id). Binding there would
// collapse two upstream accounts onto one identity. This mirrors the check
// auth.(*PostgresIdentityStore).ResolveOrCreate applies to a real login.
func TestOperatorBindRefusesAMismatchedPlatformIdentity(t *testing.T) {
	store, ctx, _ := seedShadowBindFixture(t)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,platform,platform_user_id,status,email_verified)
		VALUES('20000000-0000-4000-8000-0000000000d9',$1,$2,'newapi','4242','active',FALSE)`,
		shadowIssuer, shadowExternalID); err != nil {
		t.Fatal(err)
	}
	_, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("bind onto a foreign platform identity returned %v, want domain.ErrForbidden", err)
	}
	if bindings := countShadowBindings(t, store, ctx); bindings != 0 {
		t.Fatalf("%d external_accounts rows written despite the refusal", bindings)
	}
}

// TestOperatorBindIsIdempotent: a re-run of an already-applied bind must be a
// no-op, because the realistic operational mistake is running the same
// command twice, and a second run that re-released facts or minted a second
// user would be indistinguishable from the first in the summary.
func TestOperatorBindIsIdempotent(t *testing.T) {
	store, ctx, policyStart := seedShadowBindFixture(t)
	const repeatEvent = "82000000-0000-4000-8000-0000000000d5"
	parkEvent(t, store, ctx, repeatEvent, "usage_event", shadowExternalID, policyStart.Add(time.Hour))

	first, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	// The first apply left its released event 'queued', which the timing gate
	// counts as EVENTS_PENDING and refuses to bind into -- that refusal IS the
	// design's "one at a time, wait for the backlog" rule, made mechanical
	// rather than left to the operator's discipline. Drain it the way the
	// worker would before asking whether the re-run itself is a no-op.
	if _, err = store.pool.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processed_at=now(),
			catchup_key_hmac=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND processing_status='queued'`, shadowSourceID); err != nil {
		t.Fatal(err)
	}
	second, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatalf("re-running an applied bind failed: %v", err)
	}
	if second.InvoiceUserID != first.InvoiceUserID || second.ExternalAccountID != first.ExternalAccountID {
		t.Fatalf("re-run moved the identity: %+v then %+v", first, second)
	}
	if second.UserCreated || second.BindingCreated {
		t.Fatalf("re-run reported creating rows that already existed: %+v", second)
	}
	if second.Released != 0 || second.PrePolicySkipped != 0 {
		t.Fatalf("re-run released %d and skipped %d, want 0/0", second.Released, second.PrePolicySkipped)
	}
	if count := countUsers(t, store, ctx); count != 1 {
		t.Fatalf("%d invoice_users rows after two runs, want 1", count)
	}
}

// TestOperatorBindTimingGate pins design section B guardrail 4's timing door.
// The gate is evaluated in both modes; a dry run reports NO-GO and still
// prints its plan (that is what a rehearsal is for), while --apply is refused
// outright.
func TestOperatorBindTimingGate(t *testing.T) {
	store, ctx, policyStart := seedShadowBindFixture(t)
	parkEvent(t, store, ctx, "82000000-0000-4000-8000-0000000000d6", "usage_event", shadowExternalID, policyStart.Add(time.Hour))
	// One uncontained dead event: exactly what keeps /readyz at 503.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,attempt_count,created_at,updated_at)
		VALUES($1,'usage','82000000-0000-4000-8000-0000000000d7',$2,'usage_event','upsert',$3,
			decode(repeat('11',16),'hex'),now(),'dead',8,now(),now())`,
		shadowSourceID, shadowBatchID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}

	dry, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, false), shadowActor())
	if err != nil {
		t.Fatalf("dry run must still produce a plan when the gate is closed: %v", err)
	}
	if dry.GateSatisfied {
		t.Fatalf("gate reported satisfied with an uncontained dead event: %+v", dry.Health)
	}
	if _, err = store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor()); err == nil {
		t.Fatal("apply was allowed while an uncontained dead event was present")
	}

	// Containing the dead event (an open freeze answers for it) is what
	// /readyz forgives, so the gate must forgive it too -- proving the gate
	// reads UncontainedDead rather than a private Dead>0 copy.
	other, err := store.EnsureUser(ctx, UserRecord{OIDCIssuer: shadowIssuer, OIDCSubject: "3131", Status: "active"}, shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.BindExternalAccount(ctx, ExternalAccountRecord{
		PrincipalID: other.ID, SourceInstanceID: shadowSourceID, ExternalUserID: "3131",
		ExternalSubjectHMAC: shadowSubjectHMAC("3131"), BindingMethod: "platform_password_login", BindingStatus: "verified",
	}, shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES('61000000-0000-4000-8000-0000000000d7',$1,'EVENT_DEAD','usage_event','82000000-0000-4000-8000-0000000000d7',$2)`,
		bound.ID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	contained, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, false), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if !contained.GateSatisfied {
		t.Fatalf("gate stayed closed on a contained dead event: %s (%+v)", contained.GateReason, contained.Health)
	}
}

// TestOperatorBindAuditTrailNamesTheOperator: binding_method is free text
// that reaches no wire and no admin ledger, so the audit rows are the only
// place the "an operator decided this, the customer did not prove it" fact
// survives. external_account.bound alone is not enough -- a real login writes
// that too, with the principal as actor.
func TestOperatorBindAuditTrailNamesTheOperator(t *testing.T) {
	store, ctx, _ := seedShadowBindFixture(t)
	result, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if result.BindingMethod != BindingMethodOperatorAttested || result.BindingStatus != "verified" {
		t.Fatalf("binding is %s/%s, want operator_attested/verified", result.BindingMethod, result.BindingStatus)
	}
	for _, want := range []struct{ action, objectType, objectID string }{
		{"user.created", "invoice_user", result.InvoiceUserID},
		{"external_account.bound", "external_account", result.ExternalAccountID},
		{operatorBoundAction, "external_account", result.ExternalAccountID},
	} {
		var count int64
		if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE action=$1 AND object_type=$2 AND object_id=$3`, want.action, want.objectType, want.objectID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("audit rows for %s on %s: %d, want 1", want.action, want.objectID, count)
		}
	}
	var actorType, actorID string
	if err = store.pool.QueryRow(ctx, `SELECT actor_type,actor_id FROM audit_events WHERE action=$1`,
		operatorBoundAction).Scan(&actorType, &actorID); err != nil {
		t.Fatal(err)
	}
	if actorType != "operator" || actorID != shadowOperatorID {
		t.Fatalf("operator_bound audit actor is %s/%s, want operator/%s", actorType, actorID, shadowOperatorID)
	}
}

// TestOperatorBindRefusesApplyWithoutAnOperator keeps the approval on the
// same footing as cmd/identity-migrate's and cmd/eligibility-repair's.
func TestOperatorBindRefusesApplyWithoutAnOperator(t *testing.T) {
	store, ctx, _ := seedShadowBindFixture(t)
	in := shadowBindInput(shadowExternalID, true)
	in.OperatorID = ""
	if _, err := store.OperatorBindExternalAccount(ctx, in, AuditActor{Type: "operator"}); err == nil {
		t.Fatal("applied without an approving operator id")
	}
}

// TestOperatorBindTimingGateRefusesAPendingBacklog covers the gate's OTHER
// branch, which the first adversarial review found completely untested: the
// reviewer deleted the Pending>0 check and every test still passed.
//
// That branch is the only mechanical enforcement of the design's "one account
// at a time" rule. Everything else about the rule is prose in a runbook, and
// prose does not stop a second `--apply` typed sixty seconds after the first
// while several thousand released facts are still draining.
//
// It is deliberately stricter than /readyz, which forgives a backlog younger
// than fifteen minutes so the api can stay in rotation while the worker
// catches up. This gate forgives none, so the assertion uses a freshly created
// queued event -- one /readyz would tolerate -- to prove the extra strictness
// is real and not an accident of the fixture's clock.
//
// The mutation that must turn this red: delete the Pending>0 branch, or
// weaken it to `>= 1000`.
func TestOperatorBindTimingGateRefusesAPendingBacklog(t *testing.T) {
	store, ctx, policyStart := seedShadowBindFixture(t)
	parkEvent(t, store, ctx, "82000000-0000-4000-8000-0000000000e1", "usage_event", shadowExternalID, policyStart.Add(time.Hour))
	// A single queued event, created just now: /readyz would return 200 for
	// this (Pending=1 but OldestPending is well inside its fifteen-minute
	// budget), and this gate must still refuse.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,attempt_count,created_at,updated_at)
		VALUES($1,'usage','82000000-0000-4000-8000-0000000000e2',$2,'usage_event','upsert',$3,
			decode(repeat('11',16),'hex'),now(),'queued',0,now(),now())`,
		shadowSourceID, shadowBatchID, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}

	dry, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, false), shadowActor())
	if err != nil {
		t.Fatalf("dry run must still produce a plan while a backlog drains: %v", err)
	}
	if dry.GateSatisfied {
		t.Fatalf("gate reported satisfied with a pending backlog: %+v", dry.Health)
	}
	if !strings.Contains(dry.GateReason, "EVENTS_PENDING") {
		t.Fatalf("gate reason %q does not name the pending backlog", dry.GateReason)
	}
	if dry.Health.Dead != 0 {
		t.Fatalf("fixture has %d dead events; this test must fail on pending alone", dry.Health.Dead)
	}

	_, err = store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if !errors.Is(err, ErrOperatorBindTimingGate) {
		t.Fatalf("apply returned %v, want ErrOperatorBindTimingGate", err)
	}
	if bindings := countShadowBindings(t, store, ctx); bindings != 0 {
		t.Fatalf("%d bindings written despite the refusal", bindings)
	}

	// Draining the backlog reopens the gate -- proving the refusal is about
	// the backlog and not about something permanent in the fixture.
	if _, err = store.pool.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processed_at=now(),updated_at=now()
		WHERE source_instance_id=$1 AND processing_status='queued'`, shadowSourceID); err != nil {
		t.Fatal(err)
	}
	drained, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatalf("apply still refused after the backlog drained: %v", err)
	}
	if !drained.Applied {
		t.Fatal("apply reported not applied after the backlog drained")
	}
}

// TestOperatorBindRefusesAnIssuerTheDatabaseContradicts is the durable guard
// behind the first review's issuer finding. cmd/account-bind now refuses an
// unset login-origin variable, but a variable that is SET and wrong is still
// possible, and a wrong issuer is written permanently. So the value is also
// checked against what the running api demonstrably minted for this platform.
//
// The vacuous case is asserted on purpose: on a platform's first identity
// there is nothing to compare against and the bind is allowed. Pretending
// otherwise would make this look like a complete guard when it is not -- the
// summary flags that first bind for human review instead.
func TestOperatorBindRefusesAnIssuerTheDatabaseContradicts(t *testing.T) {
	store, ctx, _ := seedShadowBindFixture(t)

	first, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if first.PlatformIssuerInUse != "" {
		t.Fatalf("the first identity for a platform reported corroboration %q it cannot have", first.PlatformIssuerInUse)
	}

	moved := shadowBindInput("8899", true)
	moved.Issuer = "https://api.moved.example"
	if _, err = store.OperatorBindExternalAccount(ctx, moved, shadowActor()); err == nil {
		t.Fatal("accepted an issuer that contradicts every existing identity for the platform")
	}
	if countUsers(t, store, ctx) != 1 {
		t.Fatalf("the refused bind left rows behind: %d", countUsers(t, store, ctx))
	}

	agreeing, err := store.OperatorBindExternalAccount(ctx, shadowBindInput("8899", true), shadowActor())
	if err != nil {
		t.Fatalf("an agreeing issuer was refused: %v", err)
	}
	if agreeing.PlatformIssuerInUse != shadowIssuer {
		t.Fatalf("corroborating issuer is %q, want %q", agreeing.PlatformIssuerInUse, shadowIssuer)
	}
}

// TestOperatorBindCountsFactsEverSeen backs the summary's mistyped-id warning.
// The count must include facts an earlier wake already released, not only the
// ones parked right now -- otherwise a re-run of a correct bind would print
// the "this database has never heard of this id" warning and teach operators
// to ignore it.
func TestOperatorBindCountsFactsEverSeen(t *testing.T) {
	store, ctx, policyStart := seedShadowBindFixture(t)
	parkEvent(t, store, ctx, "82000000-0000-4000-8000-0000000000e3", "usage_event", shadowExternalID, policyStart.Add(time.Hour))

	unknown, err := store.OperatorBindExternalAccount(ctx, shadowBindInput("404404", false), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if unknown.FactsEverSeen != 0 {
		t.Fatalf("an id with no facts reported %d", unknown.FactsEverSeen)
	}

	known, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, true), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if known.FactsEverSeen != 1 {
		t.Fatalf("a parked fact was not counted: %d", known.FactsEverSeen)
	}

	// After the wake the row is no longer parked; it carries catchup_key_hmac
	// instead. A re-run must still see it.
	if _, err = store.pool.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processed_at=now(),updated_at=now()
		WHERE source_instance_id=$1 AND processing_status='queued'`, shadowSourceID); err != nil {
		t.Fatal(err)
	}
	repeat, err := store.OperatorBindExternalAccount(ctx, shadowBindInput(shadowExternalID, false), shadowActor())
	if err != nil {
		t.Fatal(err)
	}
	if repeat.FactsEverSeen != 1 {
		t.Fatalf("a released fact stopped being counted after the wake: %d", repeat.FactsEverSeen)
	}
}
