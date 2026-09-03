package postgresstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// newBlipRepairLegacyAccount constructs, via direct SQL (not the real
// observe/evaluate pipeline -- the fixed evaluator can no longer produce
// this shape, exactly as design XM-INV-ANCHOR-BALANCE's own repair test
// found for its sibling bug), a legacy (SIGNED_CUTOVER) account carrying
// the XM-INV-BALANCE-BLIP production signature: one synthesized
// UNKNOWN_POSITIVE credit (creditUnits) tied to an origin checkpoint's own
// positive_classified_non_cash evaluation, and poisonedCount subsequent
// reconciliation checkpoints each evaluated negative_frozen by exactly
// -creditUnits with an open UNKNOWN_NEGATIVE_BALANCE freeze -- the exact
// state RepairBalanceBlipEligibility must detect and reverse. Returns the
// account id and the poisoned checkpoints' business (checkpoint_id) keys,
// in order.
func newBlipRepairLegacyAccount(t *testing.T, store *Store, ctx context.Context, idSuffix int,
	creditUnits string, poisonedCount int) (accountID string, poisonedCheckpointIDs []string) {
	t.Helper()
	sourceID := blipUUID('1', idSuffix)
	userID := blipUUID('2', idSuffix)
	accountID = blipUUID('3', idSuffix)
	manifestHash := testHash(fmt.Sprintf("blip-repair-manifest-%d", idSuffix))
	configHash := testHash(fmt.Sprintf("blip-repair-config-%d", idSuffix))

	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','blip-repair-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutover,
		configHash, testHash(fmt.Sprintf("blip-repair-baseline-%d", idSuffix))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2)`, userID, fmt.Sprintf("blip-repair-user-%d", idSuffix)); err != nil {
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
	// The account's own trust anchor (checkpoint_kind='cutover').
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_snapshot_id,baseline_member,as_of,balance_service_units,balance_negative,
		unit_code,cutover_manifest_hash,configuration_hash,reconciliation_status,
		source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,'cutover',$6,TRUE,$7,0,FALSE,'SUB2_BALANCE_1E8',$8,$9,
		'cutover_baseline',1,$10,$7,$11,$7)`,
		blipUUID('4', idSuffix*1000), sourceID, accountID, fmt.Sprintf("blip-repair-cutover-event-%d", idSuffix),
		fmt.Sprintf("blip-repair-cutover-%d", idSuffix), testHash(fmt.Sprintf("blip-repair-baseline-%d", idSuffix)),
		cutover, manifestHash, configHash, fmt.Sprintf("cursor-cutover-%d", idSuffix),
		testHash(fmt.Sprintf("blip-repair-cutover-revision-%d", idSuffix))); err != nil {
		t.Fatal(err)
	}

	// The origin: a reconciliation checkpoint whose positive difference the
	// (pre-fix) evaluator wrongly self-healed into an UNKNOWN_POSITIVE
	// credit.
	originAt := cutover.Add(10 * time.Minute)
	originCheckpointID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID,
		fmt.Sprintf("blip-repair-origin-%d", idSuffix), originAt, "0", 2, manifestHash, configHash)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_checkpoint_evaluations(
			id,checkpoint_id,projection_version,expected_service_units,
			difference_service_units,evaluation_status)
		VALUES($1,$2,1,'0',$3::numeric,'positive_classified_non_cash')`,
		randomUUID(), originCheckpointID, creditUnits); err != nil {
		t.Fatal(err)
	}
	creditID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			credit_kind,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7::numeric,'SUB2_BALANCE_1E8',$8,$9,
			'UNKNOWN_POSITIVE',0,$10,$6,$11,$6)`,
		creditID, sourceID, accountID, "unknown-positive:"+fmt.Sprintf("origin-event-%d", idSuffix),
		"unknown-positive:"+originCheckpointID, originAt, creditUnits, manifestHash, configHash,
		fmt.Sprintf("cursor-credit-%d", idSuffix), testHash(fmt.Sprintf("blip-repair-credit-revision-%d", idSuffix))); err != nil {
		t.Fatal(err)
	}

	negatedCreditUnits := "-" + creditUnits
	poisonedCheckpointIDs = make([]string, 0, poisonedCount)
	lastAt := originAt
	for i := 0; i < poisonedCount; i++ {
		poisonedAt := originAt.Add(time.Duration(i+1) * time.Minute)
		lastAt = poisonedAt
		poisonedBusinessID := fmt.Sprintf("blip-repair-poisoned-%d-%d", idSuffix, i)
		poisonedDBID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID,
			poisonedBusinessID, poisonedAt, "0", int64(3+i), manifestHash, configHash)
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO balance_checkpoint_evaluations(
				id,checkpoint_id,projection_version,expected_service_units,
				difference_service_units,evaluation_status)
			VALUES($1,$2,1,$3::numeric,$4::numeric,'negative_frozen')`,
			randomUUID(), poisonedDBID, creditUnits, negatedCreditUnits); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO eligibility_freezes(
				id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
			VALUES($1,$2,'UNKNOWN_NEGATIVE_BALANCE','balance_checkpoint',$3)`,
			randomUUID(), accountID, poisonedBusinessID); err != nil {
			t.Fatal(err)
		}
		poisonedCheckpointIDs = append(poisonedCheckpointIDs, poisonedBusinessID)
	}
	// Realistic production shape: processEligibilityProjectionJob always
	// advances finalized_through to whatever was requested, regardless of
	// whether the evaluation it ran froze the account -- by the time the
	// account actually froze, finalized_through already covered every
	// checkpoint evaluated so far, this fixture's poisoned ones included.
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$1 WHERE external_account_id=$2`, lastAt, accountID); err != nil {
		t.Fatal(err)
	}
	return accountID, poisonedCheckpointIDs
}

func blipRepairFixedResolution(t *testing.T) BalanceBlipRepairInput {
	t.Helper()
	note := "test resolution evidence"
	return BalanceBlipRepairInput{
		OperatorID:         "99ed401b-e78a-4883-b9bf-f4cb4ba1cf17",
		NoteHash:           testHash(note),
		NoteCiphertext:     []byte("0123456789abcdef" + note),
		EvidenceHash:       testHash(note),
		EvidenceCiphertext: []byte("0123456789abcdef" + note),
	}
}

func checkpointEvaluationStatus(t *testing.T, store *Store, ctx context.Context, accountID, checkpointBusinessID string) (status string, exists bool) {
	t.Helper()
	err := store.pool.QueryRow(ctx, `
		SELECT bce.evaluation_status FROM balance_checkpoint_evaluations bce
		JOIN balance_reconciliation_checkpoints c ON c.id=bce.checkpoint_id
		WHERE c.external_account_id=$1 AND c.checkpoint_id=$2`, accountID, checkpointBusinessID).Scan(&status)
	if err == nil {
		return status, true
	}
	return "", false
}

func freezeOpen(t *testing.T, store *Store, ctx context.Context, accountID, triggerObjectType, triggerObjectID string) bool {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open' AND trigger_object_type=$2 AND trigger_object_id=$3`,
		accountID, triggerObjectType, triggerObjectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

// TestRepairBalanceBlipEligibilityDryRunReportsWithoutMutating covers dry
// run: it must report the exact totals apply would produce without writing
// anything.
func TestRepairBalanceBlipEligibilityDryRunReportsWithoutMutating(t *testing.T) {
	store, ctx := integrationStore(t)
	accountID, poisoned := newBlipRepairLegacyAccount(t, store, ctx, 950, "3667080", 2)

	result, err := store.RepairBalanceBlipEligibility(ctx, BalanceBlipRepairInput{Apply: false},
		AuditActor{Type: "system", ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("dry run reported Applied=true")
	}
	if result.TotalBlipCreditsRemoved != 1 || result.TotalNegativeFreezesResolved != 2 || result.TotalCheckpointEvaluationsReset != 2 {
		t.Fatalf("dry run totals=%+v, want 1/2/2", result)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ExternalAccountID != accountID || result.Accounts[0].Reactivated {
		t.Fatalf("dry run accounts=%+v", result.Accounts)
	}

	// Nothing changed: credit still present, both checkpoints still
	// negative_frozen, both freezes still open, account still frozen.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("credit count after dry run=%d, want 1 (untouched)", count)
	}
	for _, checkpointID := range poisoned {
		status, exists := checkpointEvaluationStatus(t, store, ctx, accountID, checkpointID)
		if !exists || status != "negative_frozen" {
			t.Fatalf("%s status=%q exists=%t after dry run, want negative_frozen/true", checkpointID, status, exists)
		}
		if !freezeOpen(t, store, ctx, accountID, "balance_checkpoint", checkpointID) {
			t.Fatalf("%s freeze not open after dry run", checkpointID)
		}
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "frozen" {
		t.Fatalf("account status=%q err=%v after dry run, want frozen (untouched)", status, err)
	}
}

// TestRepairBalanceBlipEligibilityApplyResolvesResetsAndReactivates covers
// apply's full contract across three accounts in one run: account A (two
// poisoned checkpoints, no other freeze) fully recovers -- its blip credit
// is removed, both checkpoint evaluations are reset (deleted) so a real
// subsequent ProcessEligibilityProjectionJobs run re-evaluates them clean,
// both freezes resolve, and the account reactivates; account B (one
// poisoned checkpoint plus one unrelated, genuinely open USAGE_EXCEEDS_LEDGER
// freeze) has its poisoned checkpoint's freeze resolved and its credit
// removed but stays frozen because the unrelated freeze remains; account C
// (no UNKNOWN_POSITIVE credit at all, a real unrelated SOURCE_GAP freeze) is
// completely untouched and never appears in the repair's account list. A
// second apply run is idempotent (finds nothing left for A or B).
func TestRepairBalanceBlipEligibilityApplyResolvesResetsAndReactivates(t *testing.T) {
	store, ctx := integrationStore(t)
	accountA, poisonedA := newBlipRepairLegacyAccount(t, store, ctx, 960, "3667080", 2)
	accountB, poisonedB := newBlipRepairLegacyAccount(t, store, ctx, 961, "500", 1)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
		VALUES($1,$2,'USAGE_EXCEEDS_LEDGER','usage_event','blip-repair-unrelated-961')`,
		randomUUID(), accountB); err != nil {
		t.Fatal(err)
	}

	// Account C: a real unrelated freeze, no blip credit at all.
	accountC := blipUUID('3', 962)
	sourceC := blipUUID('1', 962)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','blip-repair-control','v3-test')`, sourceC); err != nil {
		t.Fatal(err)
	}
	userC := blipUUID('2', 962)
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','blip-repair-control-user')`, userC); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'962','test','verified')`, accountC, userC, sourceC); err != nil {
		t.Fatal(err)
	}
	var globalManifestHash string
	if err := store.pool.QueryRow(ctx, `SELECT manifest_hash FROM source_cutover_manifests LIMIT 1`).Scan(&globalManifestHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
		VALUES($1,$2,'SOURCE_GAP','balance_checkpoint','blip-repair-control-unrelated')`,
		randomUUID(), accountC); err != nil {
		t.Fatal(err)
	}

	in := blipRepairFixedResolution(t)
	in.Apply = true
	result, err := store.RepairBalanceBlipEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("apply reported Applied=false")
	}
	if result.TotalBlipCreditsRemoved != 2 || result.TotalNegativeFreezesResolved != 3 || result.TotalCheckpointEvaluationsReset != 3 {
		t.Fatalf("apply totals=%+v, want 2/3/3", result)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("apply touched %d accounts, want 2 (A and B only, never C)", len(result.Accounts))
	}
	byAccount := map[string]BalanceBlipRepairAccount{}
	for _, a := range result.Accounts {
		byAccount[a.ExternalAccountID] = a
	}
	if a, ok := byAccount[accountA]; !ok || !a.Reactivated || a.BlipCreditsRemoved != 1 ||
		a.NegativeFreezesResolved != 2 || a.CheckpointEvaluationsReset != 2 {
		t.Fatalf("account A summary=%+v ok=%t, want reactivated/1/2/2", a, ok)
	}
	if b, ok := byAccount[accountB]; !ok || b.Reactivated || b.BlipCreditsRemoved != 1 ||
		b.NegativeFreezesResolved != 1 || b.CheckpointEvaluationsReset != 1 {
		t.Fatalf("account B summary=%+v ok=%t, want not-reactivated/1/1/1", b, ok)
	}

	// Account A: credit gone, both checkpoints reset (no evaluation row),
	// both freezes resolved, account active.
	if count := countUnknownPositiveCredits(t, store, ctx, accountA); count != 0 {
		t.Fatalf("account A credit count=%d, want 0", count)
	}
	for _, checkpointID := range poisonedA {
		if _, exists := checkpointEvaluationStatus(t, store, ctx, accountA, checkpointID); exists {
			t.Fatalf("%s evaluation still present after apply, want reset (deleted)", checkpointID)
		}
		if freezeOpen(t, store, ctx, accountA, "balance_checkpoint", checkpointID) {
			t.Fatalf("%s freeze still open after apply", checkpointID)
		}
	}
	var statusA string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountA).Scan(&statusA); err != nil || statusA != "active" {
		t.Fatalf("account A status=%q err=%v, want active", statusA, err)
	}
	// A real subsequent projection job must complete cleanly, re-evaluating
	// the reset checkpoints matched with no new freeze -- proof this
	// repair leaves the account able to make real forward progress. The
	// repair's own reactivation step already queued the job with
	// requested_through covering every poisoned checkpoint (the fixture's
	// finalized_through); ProcessEligibilityProjectionJobs's own "now" is
	// only the claim-cutoff/lease clock, unrelated to fixture-simulated
	// ledger time, so it must be real wall-clock time here.
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("post-repair projection processed=%d err=%v", processed, err)
	}
	for _, checkpointID := range poisonedA {
		status, exists := checkpointEvaluationStatus(t, store, ctx, accountA, checkpointID)
		if !exists || status != "matched" {
			t.Fatalf("%s re-evaluated status=%q exists=%t, want matched/true", checkpointID, status, exists)
		}
	}
	if count := openFreezeCount(t, store, ctx, accountA); count != 0 {
		t.Fatalf("account A open freezes after re-evaluation=%d, want 0", count)
	}

	// Account B: credit gone, its own checkpoint reset and freeze resolved,
	// but the unrelated freeze remains and the account stays frozen.
	if count := countUnknownPositiveCredits(t, store, ctx, accountB); count != 0 {
		t.Fatalf("account B credit count=%d, want 0", count)
	}
	if _, exists := checkpointEvaluationStatus(t, store, ctx, accountB, poisonedB[0]); exists {
		t.Fatalf("account B checkpoint evaluation still present after apply")
	}
	if !freezeOpen(t, store, ctx, accountB, "usage_event", "blip-repair-unrelated-961") {
		t.Fatal("account B's unrelated freeze was disturbed")
	}
	var statusB string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountB).Scan(&statusB); err != nil || statusB != "frozen" {
		t.Fatalf("account B status=%q err=%v, want frozen (unrelated freeze remains)", statusB, err)
	}

	// Account C: completely untouched.
	if !freezeOpen(t, store, ctx, accountC, "balance_checkpoint", "blip-repair-control-unrelated") {
		t.Fatal("account C's unrelated freeze was disturbed")
	}

	// A second apply run is idempotent: nothing left to repair.
	in2 := blipRepairFixedResolution(t)
	in2.Apply = true
	second, err := store.RepairBalanceBlipEligibility(ctx, in2, AuditActor{Type: "admin", ID: in2.OperatorID})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Accounts) != 0 || second.TotalBlipCreditsRemoved != 0 {
		t.Fatalf("second apply run found leftovers=%+v, want none", second)
	}
}

// TestRepairBalanceBlipEligibilityApplyRequiresOperatorAndEvidence mirrors
// the sibling repairs' own apply-mode guard: apply refuses without a valid
// operator UUID or without encrypted evidence/note, before touching the
// database at all.
func TestRepairBalanceBlipEligibilityApplyRequiresOperatorAndEvidence(t *testing.T) {
	store, ctx := integrationStore(t)
	if _, err := store.RepairBalanceBlipEligibility(ctx, BalanceBlipRepairInput{Apply: true}, AuditActor{Type: "admin", ID: "test"}); err == nil {
		t.Fatal("apply without operator id was accepted")
	}
	if _, err := store.RepairBalanceBlipEligibility(ctx, BalanceBlipRepairInput{
		Apply: true, OperatorID: "99ed401b-e78a-4883-b9bf-f4cb4ba1cf17",
	}, AuditActor{Type: "admin", ID: "test"}); err == nil {
		t.Fatal("apply without encrypted evidence/note was accepted")
	}
}
