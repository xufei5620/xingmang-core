package postgresstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// queueNarrowUUID generates deterministic UUID-shaped literals for this
// file's fixtures, matching blipUUID/autoReconcileUUID's own convention.
func queueNarrowUUID(prefix rune, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", prefix, n)
}

// newQueueNarrowAccount constructs, via direct SQL (not the real
// observe/evaluate pipeline -- matching newBlipRepairLegacyAccount's own
// precedent for repair-tool tests, which only need a plausible account shape
// to exercise the repair's own selection/resolution logic, not a full
// production replay), a minimal account whose eligibility_status starts
// 'frozen' (the production invariant: an account with any open
// eligibility_freezes row is always 'frozen'). idSuffix must be unique
// within one test's schema. Returns the account id, source instance id, and
// the cutover manifest/configuration hashes (needed by fixtures that also
// insert real facts/lots against this account).
func newQueueNarrowAccount(t *testing.T, store *Store, ctx context.Context, idSuffix int) (accountID, sourceID, manifestHash, configHash string) {
	t.Helper()
	sourceID = queueNarrowUUID('1', idSuffix)
	userID := queueNarrowUUID('2', idSuffix)
	accountID = queueNarrowUUID('3', idSuffix)
	manifestHash = testHash(fmt.Sprintf("queue-narrow-manifest-%d", idSuffix))
	configHash = testHash(fmt.Sprintf("queue-narrow-config-%d", idSuffix))

	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','queue-narrow-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutover,
		configHash, testHash(fmt.Sprintf("queue-narrow-baseline-%d", idSuffix))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2)`, userID, fmt.Sprintf("queue-narrow-user-%d", idSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,$4,'test','verified')`, accountID, userID, sourceID, fmt.Sprint(idSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,eligibility_status)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900,'frozen')`,
		accountID, sourceID, cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	return accountID, sourceID, manifestHash, configHash
}

// insertQueueNarrowFreeze inserts one open eligibility_freezes row directly,
// matching this package's own established precedent for constructing a
// repair tool's candidate rows (newBlipRepairLegacyAccount and the sibling
// balance-anchor/balance-blip tests all insert freezes this way).
func insertQueueNarrowFreeze(t *testing.T, store *Store, ctx context.Context, accountID, lotID, reason, triggerObjectType, triggerObjectID string) string {
	t.Helper()
	id := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7)`,
		id, accountID, lotID, reason, triggerObjectType, triggerObjectID, testHash(id)); err != nil {
		t.Fatal(err)
	}
	return id
}

func queueNarrowFixedResolution(t *testing.T) QueueNarrowRepairInput {
	t.Helper()
	note := "test queue-narrow resolution evidence"
	return QueueNarrowRepairInput{
		OperatorID:         "8c2a9e0d-1f4b-4a6e-9c3d-7e5f1a2b3c4d",
		NoteHash:           testHash(note),
		NoteCiphertext:     []byte("0123456789abcdef" + note),
		EvidenceHash:       testHash(note),
		EvidenceCiphertext: []byte("0123456789abcdef" + note),
	}
}

func freezeStatus(t *testing.T, store *Store, ctx context.Context, freezeID string) (status string, resolutionVersion int64) {
	t.Helper()
	if err := store.pool.QueryRow(ctx, `SELECT status,resolution_version FROM eligibility_freezes WHERE id=$1`,
		freezeID).Scan(&status, &resolutionVersion); err != nil {
		t.Fatal(err)
	}
	return status, resolutionVersion
}

func accountEligibilityStatus(t *testing.T, store *Store, ctx context.Context, accountID string) string {
	t.Helper()
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestQueueNarrowDryRunChangesNothing covers design section 3(C) item 3's
// dry-run/apply contract: a dry run against an account carrying all three
// target categories must report the same per-category counts apply would,
// without writing anything (every freeze stays open, the account stays
// frozen, no audit rows are written).
func TestQueueNarrowDryRunChangesNothing(t *testing.T) {
	store, ctx := integrationStore(t)
	accountID, _, _, _ := newQueueNarrowAccount(t, store, ctx, 900)
	negID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-dry-neg")
	usageID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "USAGE_EXCEEDS_LEDGER", "usage_event", "qn-dry-usage")
	lateID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "LATE_FINALIZED_EVENT", "usage", "qn-dry-late")

	result, err := store.RepairQueueNarrowEligibility(ctx, QueueNarrowRepairInput{Apply: false}, AuditActor{Type: "system", ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("dry run reported Applied=true")
	}
	if len(result.Errors) != 0 {
		t.Fatalf("dry run reported errors: %+v", result.Errors)
	}
	if len(result.Accounts) != 1 {
		t.Fatalf("dry run accounts=%+v, want exactly 1", result.Accounts)
	}
	row := result.Accounts[0]
	if row.ExternalAccountID != accountID || row.NegativeBalanceFreezesResolved != 1 ||
		row.UsageExceedsLedgerFreezesResolved != 1 || row.LateFactFreezesResolved != 1 {
		t.Fatalf("dry run row=%+v, want 1/1/1 for account %s", row, accountID)
	}

	for _, freezeID := range []string{negID, usageID, lateID} {
		status, version := freezeStatus(t, store, ctx, freezeID)
		if status != "open" || version != 1 {
			t.Fatalf("dry run mutated freeze %s: status=%s version=%d", freezeID, status, version)
		}
	}
	if status := accountEligibilityStatus(t, store, ctx, accountID); status != "frozen" {
		t.Fatalf("dry run mutated account status=%q, want frozen (unchanged)", status)
	}
	var auditCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action IN ('eligibility.freeze.resolved','eligibility.queue_narrow.repaired',
			'eligibility.pending_reconciliation.entered','eligibility.usage.overage_recorded',
			'eligibility.usage.overage_cleared') AND object_id IN ($1,$2,$3,$4)`,
		negID, usageID, lateID, accountID).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("dry run audit count=%d err=%v, want 0", auditCount, err)
	}
}

// TestQueueNarrowApplyResolvesAllThreeCategoriesAndReactivates covers apply
// against a clean-ledger account (no real balance evidence, no funding
// lots): all three target categories resolve, the usage-exceeds-ledger
// reproject finds nothing to record, the negative-balance rebuild finds no
// evidence of a still-real gap, and with no freeze left open the account
// returns to active.
func TestQueueNarrowApplyResolvesAllThreeCategoriesAndReactivates(t *testing.T) {
	store, ctx := integrationStore(t)
	accountID, _, _, _ := newQueueNarrowAccount(t, store, ctx, 901)
	negID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-apply-neg")
	usageID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "USAGE_EXCEEDS_LEDGER", "usage_event", "qn-apply-usage")
	lateID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "LATE_FINALIZED_EVENT", "usage", "qn-apply-late")

	in := queueNarrowFixedResolution(t)
	in.Apply = true
	result, err := store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || len(result.Errors) != 0 || len(result.Accounts) != 1 {
		t.Fatalf("apply result=%+v, want Applied with 1 account and no errors", result)
	}
	row := result.Accounts[0]
	if row.NegativeBalanceFreezesResolved != 1 || row.UsageExceedsLedgerFreezesResolved != 1 ||
		row.LateFactFreezesResolved != 1 || row.RebuiltPendingReconciliation || !row.UsageOverageReprojected || !row.Reactivated {
		t.Fatalf("apply row=%+v, want 1/1/1, RebuiltPendingReconciliation=false, UsageOverageReprojected=true, Reactivated=true", row)
	}

	for _, freezeID := range []string{negID, usageID, lateID} {
		status, version := freezeStatus(t, store, ctx, freezeID)
		if status != "resolved" || version != 2 {
			t.Fatalf("freeze %s status=%s version=%d, want resolved/2", freezeID, status, version)
		}
	}
	if status := accountEligibilityStatus(t, store, ctx, accountID); status != "active" {
		t.Fatalf("account status=%q, want active", status)
	}
	overageStatus, overageUnits, overageUsage := readUsageOverage(t, store, ctx, accountID)
	if overageStatus != "active" || overageUnits != "" || overageUsage != "" {
		t.Fatalf("overage columns status=%q units=%q usageEventID=%q, want active/''/''", overageStatus, overageUnits, overageUsage)
	}
	var repairedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.queue_narrow.repaired' AND object_id=$1`, accountID).Scan(&repairedAudits); err != nil || repairedAudits != 1 {
		t.Fatalf("queue_narrow.repaired audit count=%d err=%v, want 1", repairedAudits, err)
	}
	var resolvedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.freeze.resolved' AND object_id IN ($1,$2,$3)`,
		negID, usageID, lateID).Scan(&resolvedAudits); err != nil || resolvedAudits != 3 {
		t.Fatalf("freeze.resolved audit count=%d err=%v, want 3", resolvedAudits, err)
	}
}

// TestQueueNarrowApplyRebuildsPendingReconciliationForStillNegativeAccount
// covers design section 3(C) item 3's own core safety property: an account
// whose latest real balance evaluation still shows negative_frozen must not
// be reported simply "resolved" while genuinely still negative -- apply puts
// it into not_invoiceable_pending_reconciliation (slice 1's self-clearing
// state) instead, using the latest evidence to populate the five columns.
func TestQueueNarrowApplyRebuildsPendingReconciliationForStillNegativeAccount(t *testing.T) {
	store, ctx := integrationStore(t)
	accountID, sourceID, manifestHash, configHash := newQueueNarrowAccount(t, store, ctx, 902)

	var cutover time.Time
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&cutover); err != nil {
		t.Fatal(err)
	}
	checkpointAt := cutover.Add(20 * time.Minute)
	checkpointRowID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID,
		"qn-still-negative", checkpointAt, "60", 2, manifestHash, configHash)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_checkpoint_evaluations(id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,1,'100','-40','negative_frozen')`, randomUUID(), checkpointRowID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$1 WHERE external_account_id=$2`, checkpointAt.Add(time.Minute), accountID); err != nil {
		t.Fatal(err)
	}
	negID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-still-negative-old-trigger")

	in := queueNarrowFixedResolution(t)
	in.Apply = true
	result, err := store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || !result.Accounts[0].RebuiltPendingReconciliation || result.Accounts[0].Reactivated {
		t.Fatalf("apply accounts=%+v, want RebuiltPendingReconciliation=true, Reactivated=false", result.Accounts)
	}
	if status, _ := freezeStatus(t, store, ctx, negID); status != "resolved" {
		t.Fatalf("freeze status=%s, want resolved", status)
	}
	pending := readPendingReconciliation(t, store, ctx, accountID)
	if pending.status != "not_invoiceable_pending_reconciliation" || pending.reason != "UNKNOWN_NEGATIVE_BALANCE" ||
		pending.triggerType != "balance_checkpoint" || pending.triggerID != "qn-still-negative" ||
		pending.detail == "" || !pending.sinceSet || pending.consecutiveMatches != 0 {
		t.Fatalf("account not rebuilt into pending reconciliation as expected: %+v", pending)
	}
}

// TestQueueNarrowApplyDoesNotRebuildPendingReconciliationForReconciledAccount
// covers the other half: an account whose latest real evaluation already
// shows matched (the ledger reconciled since the stale freeze was opened)
// must simply resolve and reactivate -- not be pushed into pending
// reconciliation based on an already-superseded gap.
func TestQueueNarrowApplyDoesNotRebuildPendingReconciliationForReconciledAccount(t *testing.T) {
	store, ctx := integrationStore(t)
	accountID, sourceID, manifestHash, configHash := newQueueNarrowAccount(t, store, ctx, 903)

	var cutover time.Time
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&cutover); err != nil {
		t.Fatal(err)
	}
	checkpointAt := cutover.Add(20 * time.Minute)
	checkpointRowID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID,
		"qn-reconciled", checkpointAt, "0", 2, manifestHash, configHash)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_checkpoint_evaluations(id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,1,'0','0','matched')`, randomUUID(), checkpointRowID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$1 WHERE external_account_id=$2`, checkpointAt.Add(time.Minute), accountID); err != nil {
		t.Fatal(err)
	}
	negID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-reconciled-old-trigger")

	in := queueNarrowFixedResolution(t)
	in.Apply = true
	result, err := store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].RebuiltPendingReconciliation || !result.Accounts[0].Reactivated {
		t.Fatalf("apply accounts=%+v, want RebuiltPendingReconciliation=false, Reactivated=true", result.Accounts)
	}
	if status, _ := freezeStatus(t, store, ctx, negID); status != "resolved" {
		t.Fatalf("freeze status=%s, want resolved", status)
	}
	if status := accountEligibilityStatus(t, store, ctx, accountID); status != "active" {
		t.Fatalf("account status=%q, want active", status)
	}
}

// TestQueueNarrowApplyLeavesNonTargetReasonsUntouched covers design section
// 3(C)'s explicit "the six data-integrity/manual reasons are unaffected"
// guarantee: SOURCE_GAP and SOURCE_REFUND freezes on the same account as a
// target UNKNOWN_NEGATIVE_BALANCE freeze must stay open, untouched, and
// correctly keep the account frozen (real exposure remains) even though the
// target freeze resolves.
func TestQueueNarrowApplyLeavesNonTargetReasonsUntouched(t *testing.T) {
	store, ctx := integrationStore(t)
	accountID, _, _, _ := newQueueNarrowAccount(t, store, ctx, 904)
	negID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-untouched-neg")
	gapID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "SOURCE_GAP", "balance_checkpoint", "qn-untouched-gap")
	refundID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "SOURCE_REFUND", "funding_lot", "qn-untouched-refund")

	in := queueNarrowFixedResolution(t)
	in.Apply = true
	result, err := store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].NegativeBalanceFreezesResolved != 1 ||
		result.Accounts[0].UsageExceedsLedgerFreezesResolved != 0 || result.Accounts[0].LateFactFreezesResolved != 0 ||
		result.Accounts[0].Reactivated {
		t.Fatalf("apply accounts=%+v, want exactly 1 negative-balance resolution and Reactivated=false", result.Accounts)
	}
	if status, _ := freezeStatus(t, store, ctx, negID); status != "resolved" {
		t.Fatalf("target freeze status=%s, want resolved", status)
	}
	for _, untouchedID := range []string{gapID, refundID} {
		status, version := freezeStatus(t, store, ctx, untouchedID)
		if status != "open" || version != 1 {
			t.Fatalf("non-target freeze %s status=%s version=%d, want open/1 (untouched)", untouchedID, status, version)
		}
	}
	if status := accountEligibilityStatus(t, store, ctx, accountID); status != "frozen" {
		t.Fatalf("account status=%q, want frozen (real exposure remains open)", status)
	}
}

// TestQueueNarrowAccountIsolation covers the hard account-isolation
// requirement: a second account whose data is deliberately inconsistent (a
// funding lot whose reserved+issued exposure no longer fits under its own
// reprojected recognized amount -- reprojectEligibilityTx's own pre-existing
// guarded UPDATE conflict, domain.ErrConflict, a real and reachable
// production data shape, not a synthetic error injection) must not abort the
// run for another, healthy account processed in the same call.
func TestQueueNarrowAccountIsolation(t *testing.T) {
	store, ctx := integrationStore(t)

	goodAccountID, _, _, _ := newQueueNarrowAccount(t, store, ctx, 905)
	insertQueueNarrowFreeze(t, store, ctx, goodAccountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-isolation-good")

	brokenAccountID, brokenSourceID, brokenManifestHash, brokenConfigHash := newQueueNarrowAccount(t, store, ctx, 906)
	var brokenCutover time.Time
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state
		WHERE external_account_id=$1`, brokenAccountID).Scan(&brokenCutover); err != nil {
		t.Fatal(err)
	}
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	lotID := randomUUID()
	// A WALLET_CASH lot already fully consumed (100 units, 100_000 minor),
	// carrying both a reservation and an issued exposure that together
	// (55_000) exceed what a fresh reprojection is about to recompute
	// (50_000, once the 50-unit usage fact below is the only fact left
	// after the lot's own prior 100-unit recognition is invalidated).
	// completed_at/eligibility_cutover_at must be at or after the immutable
	// policy start (enforced by a trigger on WALLET_CASH/SUBSCRIPTION_CASH
	// lots), which is after this account's own cutover_at by construction.
	paymentAt := policyStart.Add(5 * time.Minute)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,(SELECT invoice_user_id FROM external_accounts WHERE id=$2),$2,$3,'qn-isolation-order','CNY',
			100000,100000,10000,45000,'verified','COMPLETED',$4,$5,$5,'WALLET_CASH',$5,100000,100000,FALSE,1)`,
		lotID, brokenAccountID, brokenSourceID, testHash("qn-isolation-payment"), paymentAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,100,100,10000000,100000,0)`, lotID); err != nil {
		t.Fatal(err)
	}
	usageAt := brokenCutover.Add(20 * time.Minute)
	insertUsageEventDirect(t, store, ctx, brokenSourceID, brokenAccountID, "qn-isolation-usage",
		usageAt, "50", 1, brokenManifestHash, brokenConfigHash)
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$1 WHERE external_account_id=$2`, usageAt.Add(time.Minute), brokenAccountID); err != nil {
		t.Fatal(err)
	}
	brokenFreezeID := insertQueueNarrowFreeze(t, store, ctx, brokenAccountID, "", "USAGE_EXCEEDS_LEDGER", "usage_event", "qn-isolation-broken")

	in := queueNarrowFixedResolution(t)
	in.Apply = true
	result, err := store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
	if err != nil {
		t.Fatalf("RepairQueueNarrowEligibility itself must not fail for one account's error: %v", err)
	}
	if len(result.Errors) != 1 || result.Errors[0].ExternalAccountID != brokenAccountID || !strings.Contains(result.Errors[0].Message, "conflict") {
		t.Fatalf("errors=%+v, want exactly one conflict error for the broken account", result.Errors)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ExternalAccountID != goodAccountID || !result.Accounts[0].Reactivated {
		t.Fatalf("accounts=%+v, want the good account processed to completion and reactivated", result.Accounts)
	}

	// The good account committed fully.
	if status := accountEligibilityStatus(t, store, ctx, goodAccountID); status != "active" {
		t.Fatalf("good account status=%q, want active", status)
	}

	// The broken account's own transaction rolled back entirely: its freeze
	// is still open, its account row is still frozen, and its lot is
	// untouched -- a failure for this account leaves no partial trace.
	if status, version := freezeStatus(t, store, ctx, brokenFreezeID); status != "open" || version != 1 {
		t.Fatalf("broken account's freeze status=%s version=%d, want open/1 (untouched by its own failed transaction)", status, version)
	}
	if status := accountEligibilityStatus(t, store, ctx, brokenAccountID); status != "frozen" {
		t.Fatalf("broken account status=%q, want frozen (its failed transaction rolled back)", status)
	}
	var consumedCashMinor int64
	if err := store.pool.QueryRow(ctx, `SELECT consumed_cash_minor FROM funding_lots WHERE id=$1`, lotID).Scan(&consumedCashMinor); err != nil {
		t.Fatal(err)
	}
	if consumedCashMinor != 100000 {
		t.Fatalf("broken account's lot consumed_cash_minor=%d, want unchanged at 100000 (rolled back)", consumedCashMinor)
	}
}
