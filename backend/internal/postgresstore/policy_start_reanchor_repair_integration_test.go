package postgresstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// newPreSlicePolicyAnchorAccount constructs, via one explicit transaction
// (bypassing ObserveBalanceCheckpoint's now-fixed bootstrap -- the shape this
// creates can no longer be produced by real code once this slice ships,
// matching every sibling repair-tool fixture's own precedent for a
// pre-fix-only shape, e.g. balance_anchor_repair_integration_test.go's
// "stuck" checkpoints), a POLICY_ANCHOR account exactly as
// ObserveBalanceCheckpoint's bootstrap branch used to leave one *before*
// XM-INV-ELIG-POLICY-START-ANCHOR: cutover_at/cutover_balance_units/
// finalized_through are the triggering checkpoint's own (later) as_of/
// balance, not the global policy start. This matches the three real
// production accounts (40bd883d..., 98cce4c8..., 6706ea6a...) this repair
// targets. The state row is inserted before the checkpoint row in the same
// transaction: the checkpoint table's own immediate trusted-state trigger
// needs the state row to already exist, while the state row's own
// POLICY_ANCHOR validation (migration 0016/0021) is deferred to COMMIT so it
// can, in turn, require this checkpoint row to exist by then -- the same
// ordering ObserveBalanceCheckpoint's real bootstrap code uses.
func newPreSlicePolicyAnchorAccount(t *testing.T, store *Store, ctx context.Context, idSuffix int, policyStart time.Time, oldCutoverAt time.Time, oldBalance string) (accountID, sourceID, manifestHash, configHash, checkpointBusinessKey string) {
	t.Helper()
	sourceID = policyStartUUID('1', idSuffix)
	userID := policyStartUUID('2', idSuffix)
	accountID = policyStartUUID('3', idSuffix)
	manifestHash = testHash(fmt.Sprintf("policy-reanchor-manifest-%d", idSuffix))
	configHash = testHash(fmt.Sprintf("policy-reanchor-config-%d", idSuffix))
	checkpointBusinessKey = fmt.Sprintf("policy-reanchor-anchor-%d", idSuffix)
	cutover := policyStart.UTC().Add(-1 * time.Hour).Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-reanchor-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutover,
		configHash, testHash(fmt.Sprintf("policy-reanchor-baseline-%d", idSuffix))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2)`, userID, fmt.Sprintf("policy-reanchor-user-%d", idSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,$4,'test','verified')`, accountID, userID, sourceID, fmt.Sprint(idSuffix)); err != nil {
		t.Fatal(err)
	}

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,eligibility_status)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',$4::numeric,$5,'POLICY_ANCHOR',$3,900,'active')`,
		accountID, sourceID, oldCutoverAt.UTC(), oldBalance, manifestHash); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_member,as_of,balance_service_units,balance_negative,unit_code,
		cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
		source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,'reconciliation',TRUE,$5,$6::numeric,FALSE,'SUB2_BALANCE_1E8',
		$7,$8,'cutover_baseline',1,$9,$5,$10,$5)`,
		randomUUID(), sourceID, accountID, checkpointBusinessKey, oldCutoverAt.UTC(), oldBalance,
		manifestHash, configHash, "cursor:"+checkpointBusinessKey, testHash(checkpointBusinessKey)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return accountID, sourceID, manifestHash, configHash, checkpointBusinessKey
}

func policyReanchorFixedOperator() string {
	return "9c2a9e0d-1f4b-4a6e-9c3d-7e5f1a2b3c4e"
}

// TestPolicyStartReanchorDryRunZeroLotsReportsNoOpAndChangesNothing covers
// design section 3(D)'s own "expected no-op": an account with no WALLET_CASH
// funding lot in the window must be reported as such and, crucially,
// completely unmodified -- not merely by the dry-run's own ROLLBACK, but as
// a documented, deliberate behavior asserted here by comparing every
// relevant row before and after.
func TestPolicyStartReanchorDryRunZeroLotsReportsNoOpAndChangesNothing(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-3 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	oldCutoverAt := policyStart.Add(2 * time.Hour)
	accountID, _, _, _, _ := newPreSlicePolicyAnchorAccount(t, store, ctx, 500, policyStart, oldCutoverAt, "500")

	before := snapshotEligibilityState(t, store, ctx, accountID)
	beforeCheckpointCount := countCheckpoints(t, store, ctx, accountID)

	result, err := store.RepairPolicyStartReanchorEligibility(ctx, PolicyStartReanchorRepairInput{Apply: false}, AuditActor{Type: "admin", ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ExternalAccountID != accountID {
		t.Fatalf("accounts=%+v, want exactly one row for %s", result.Accounts, accountID)
	}
	row := result.Accounts[0]
	if !row.NoOp || row.WindowFundingLots != 0 || row.Reprojected {
		t.Fatalf("row=%+v, want NoOp=true WindowFundingLots=0 Reprojected=false", row)
	}
	if !row.NewCutoverAt.Equal(row.OldCutoverAt) || row.NewCutoverBalanceUnits != row.OldCutoverBalanceUnits {
		t.Fatalf("row=%+v, want NewCutoverAt==OldCutoverAt and NewCutoverBalanceUnits==OldCutoverBalanceUnits for a NoOp", row)
	}

	after := snapshotEligibilityState(t, store, ctx, accountID)
	if !before.equal(after) {
		t.Fatalf("dry run mutated the account row: before=%+v after=%+v", before, after)
	}
	if afterCount := countCheckpoints(t, store, ctx, accountID); afterCount != beforeCheckpointCount {
		t.Fatalf("checkpoint count changed: before=%d after=%d", beforeCheckpointCount, afterCount)
	}

	// Apply must be equally inert for a NoOp account -- see the store
	// method's own doc comment: only a real, in-window funding lot is ever
	// re-anchored, in either mode.
	applyResult, err := store.RepairPolicyStartReanchorEligibility(ctx, PolicyStartReanchorRepairInput{
		Apply: true, OperatorID: policyReanchorFixedOperator()}, AuditActor{Type: "admin", ID: policyReanchorFixedOperator()})
	if err != nil {
		t.Fatal(err)
	}
	if len(applyResult.Accounts) != 1 || !applyResult.Accounts[0].NoOp {
		t.Fatalf("apply on a NoOp account=%+v, want NoOp=true", applyResult.Accounts)
	}
	afterApply := snapshotEligibilityState(t, store, ctx, accountID)
	if !before.equal(afterApply) {
		t.Fatalf("apply mutated a NoOp account: before=%+v after=%+v", before, afterApply)
	}
}

// TestPolicyStartReanchorApplyOneInWindowLotReanchorsAndReprojects covers the
// real re-anchor path: a WALLET_CASH funding lot completed inside the window
// (policyStart,oldCutoverAt] is invisible to buildEligibilityProjectionTx
// before the repair (excluded by completed_at>oldCutoverAt) and consumed
// (invoiceable) after it, once cutover_at moves back to the policy start.
func TestPolicyStartReanchorApplyOneInWindowLotReanchorsAndReprojects(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-3 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	oldCutoverAt := policyStart.Add(2 * time.Hour)
	accountID, sourceID, manifestHash, configHash, _ := newPreSlicePolicyAnchorAccount(t, store, ctx, 501, policyStart, oldCutoverAt, "500")

	lotID := randomUUID()
	paymentAt := policyStart.Add(1 * time.Hour)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,(SELECT invoice_user_id FROM external_accounts WHERE id=$2),$2,$3,'policy-reanchor-order','CNY',
			30000,30000,0,0,'verified','COMPLETED',$4,$5,$5,'WALLET_CASH',$5,30000,0,FALSE,1)`,
		lotID, accountID, sourceID, testHash("policy-reanchor-payment"), paymentAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,300,0,0,0,0)`, lotID); err != nil {
		t.Fatal(err)
	}
	// A real usage fact inside the window, after the payment: with no
	// non-cash credit anywhere on this account, it drains straight into
	// this lot's cash pool once cutover_at moves back to include both --
	// this is what proves the payment actually entered the ledger (real
	// consumption), not merely that cutover_at moved and the lot became
	// visible.
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "policy-reanchor-usage",
		paymentAt.Add(10*time.Minute), "50", 1, manifestHash, configHash)

	dryRun, err := store.RepairPolicyStartReanchorEligibility(ctx, PolicyStartReanchorRepairInput{Apply: false}, AuditActor{Type: "admin", ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dryRun.Accounts) != 1 || dryRun.Accounts[0].NoOp || dryRun.Accounts[0].WindowFundingLots != 1 || dryRun.Accounts[0].Reprojected {
		t.Fatalf("dry run=%+v, want NoOp=false WindowFundingLots=1 Reprojected=false", dryRun.Accounts)
	}
	if !dryRun.Accounts[0].NewCutoverAt.Equal(policyStart.UTC()) {
		t.Fatalf("dry run NewCutoverAt=%s, want policyStart=%s", dryRun.Accounts[0].NewCutoverAt, policyStart)
	}
	// Dry run must not have written anything.
	var stillOldCutover time.Time
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&stillOldCutover); err != nil || !stillOldCutover.Equal(oldCutoverAt.UTC()) {
		t.Fatalf("dry run mutated cutover_at: got=%s want=%s err=%v", stillOldCutover, oldCutoverAt, err)
	}

	apply, err := store.RepairPolicyStartReanchorEligibility(ctx, PolicyStartReanchorRepairInput{
		Apply: true, OperatorID: policyReanchorFixedOperator()}, AuditActor{Type: "admin", ID: policyReanchorFixedOperator()})
	if err != nil {
		t.Fatal(err)
	}
	if len(apply.Accounts) != 1 {
		t.Fatalf("apply accounts=%+v, want exactly one row", apply.Accounts)
	}
	applied := apply.Accounts[0]
	if applied.NoOp || applied.WindowFundingLots != 1 || !applied.Reprojected {
		t.Fatalf("applied=%+v, want NoOp=false WindowFundingLots=1 Reprojected=true", applied)
	}
	if !applied.NewCutoverAt.Equal(policyStart.UTC()) {
		t.Fatalf("applied NewCutoverAt=%s, want policyStart=%s", applied.NewCutoverAt, policyStart)
	}

	var newCutoverAt, newFinalizedThrough time.Time
	var newBootstrapKind string
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at,finalized_through,bootstrap_kind
		FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(
		&newCutoverAt, &newFinalizedThrough, &newBootstrapKind); err != nil {
		t.Fatal(err)
	}
	if !newCutoverAt.Equal(policyStart.UTC()) || newBootstrapKind != "POLICY_ANCHOR" {
		t.Fatalf("post-apply cutover_at=%s bootstrap_kind=%s, want policyStart=%s/POLICY_ANCHOR", newCutoverAt, newBootstrapKind, policyStart)
	}
	if newFinalizedThrough.Before(oldCutoverAt.UTC()) {
		t.Fatalf("post-apply finalized_through=%s regressed below the account's prior finalized_through=%s", newFinalizedThrough, oldCutoverAt)
	}

	var derivedCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND as_of=$2`, accountID, policyStart.UTC()).Scan(&derivedCount); err != nil || derivedCount != 1 {
		t.Fatalf("derived checkpoint count=%d err=%v, want 1", derivedCount, err)
	}

	// The lot's own consumption state was reset then rebuilt by the
	// repair's own reprojectEligibilityTx call -- it is now consumed
	// (invoiceable), proving the in-window payment actually entered the
	// ledger, not merely that cutover_at moved.
	lot, err := store.GetFundingLot(ctx, lotID)
	if err != nil {
		t.Fatal(err)
	}
	if lot.ConsumedCashMinor <= 0 {
		t.Fatalf("lot.ConsumedCashMinor=%d, want >0 (the derived opening balance's usage should have drawn on this lot once it entered the window)", lot.ConsumedCashMinor)
	}

	var queuedJobs int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, accountID).Scan(&queuedJobs); err != nil || queuedJobs != 1 {
		t.Fatalf("queued projection jobs=%d err=%v, want 1 (the repair's own defensive queue)", queuedJobs, err)
	}
	var auditCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.start_reanchored' AND object_id=$1`, accountID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v, want 1", auditCount, err)
	}

	// A second apply run is idempotent: the account is no longer a
	// candidate (cutover_at==policyStart), so it is silently skipped.
	second, err := store.RepairPolicyStartReanchorEligibility(ctx, PolicyStartReanchorRepairInput{
		Apply: true, OperatorID: policyReanchorFixedOperator()}, AuditActor{Type: "admin", ID: policyReanchorFixedOperator()})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Accounts) != 0 {
		t.Fatalf("second apply accounts=%+v, want none left (already re-anchored)", second.Accounts)
	}
}

// TestPolicyStartReanchorAccountIsolation covers the hard rule this repair
// (like XM-INV-ELIG-QUEUE-NARROW's own) must satisfy: one account's own
// transaction failing must never abort the run for any other account. The
// "broken" account is given a funding lot whose reserved+issued exposure no
// longer fits under what a fresh reprojection recomputes once the window
// widens (the same real, reachable conflict shape
// TestQueueNarrowAccountIsolation uses for its own broken account) --
// reprojectEligibilityTx's own guarded UPDATE refuses this with
// domain.ErrConflict, which must be collected as this account's own error,
// not returned from RepairPolicyStartReanchorEligibility itself.
func TestPolicyStartReanchorAccountIsolation(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-3 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)

	// Good account: a clean in-window lot, no exposure conflict.
	goodOldCutoverAt := policyStart.Add(2 * time.Hour)
	goodAccountID, goodSourceID, _, _, _ := newPreSlicePolicyAnchorAccount(t, store, ctx, 510, policyStart, goodOldCutoverAt, "500")
	goodLotID := randomUUID()
	goodPaymentAt := policyStart.Add(1 * time.Hour)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,(SELECT invoice_user_id FROM external_accounts WHERE id=$2),$2,$3,'policy-reanchor-good-order','CNY',
			10000,10000,0,0,'verified','COMPLETED',$4,$5,$5,'WALLET_CASH',$5,10000,0,FALSE,1)`,
		goodLotID, goodAccountID, goodSourceID, testHash("policy-reanchor-good-payment"), goodPaymentAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,100,0,0,0,0)`, goodLotID); err != nil {
		t.Fatal(err)
	}

	// Broken account: a lot whose reserved+issued (9000) currently exactly
	// equals its own recognized consumed_cash_minor (9000, backed by a real,
	// matching 90-unit usage allocation -- self-consistent at fixture-setup
	// time, satisfying every immediate and deferred consumption-mirror
	// constraint) but exceeds what a *fresh* reprojection recomputes for it
	// once its consumption state is reset under a window with no usage fact
	// at all in scope for this lot any more (the repair's own reset wipes
	// consumption_allocations, and nothing replaces this one) --
	// reprojectEligibilityTx's own guarded UPDATE
	// (`reserved_minor+issued_minor<=$1`) refuses to drop consumed_cash_minor
	// below reserved+issued, a real, reachable conflict. original_minor=100000
	// keeps reserved+issued=9000 within funding_lots' own table-level CHECK
	// (reserved_minor+issued_minor<=original_minor), matching
	// TestQueueNarrowAccountIsolation's own broken-account shape.
	//
	// Unlike a hand-crafted inconsistent state, this fixture must be fully
	// self-consistent from its own INSERT onward: funding_lots' own
	// immediate CHECK (funding_lots_consumed_cash_allocation_bound,
	// reserved_minor+issued_minor<=consumed_cash_minor) and the deferred
	// funding_lot_consumption_mirror_guard (which, for an 'active' account,
	// requires consumption_allocations to sum to exactly
	// consumed_service_units/rounded_consumed_cash_minor) both apply
	// immediately to this fixture's own construction, not only to what the
	// repair later does -- TestQueueNarrowAccountIsolation's own broken
	// account sidesteps this by starting 'frozen' (the deferred check is
	// gated on account_status='active'); this account must be 'active' to
	// be a valid POLICY_ANCHOR re-anchor candidate at all, so the
	// consistency is built in for real instead.
	brokenOldCutoverAt := policyStart.Add(2 * time.Hour)
	brokenAccountID, brokenSourceID, brokenManifestHash, brokenConfigHash, _ := newPreSlicePolicyAnchorAccount(t, store, ctx, 511, policyStart, brokenOldCutoverAt, "0")
	brokenLotID := randomUUID()
	brokenPaymentAt := policyStart.Add(1 * time.Hour)
	brokenUsageID := insertUsageEventDirect(t, store, ctx, brokenSourceID, brokenAccountID, "policy-reanchor-broken-usage",
		brokenPaymentAt.Add(10*time.Minute), "90", 1, brokenManifestHash, brokenConfigHash)
	// funding_lot_consumption_mirror_guard (migration 0009, DEFERRABLE
	// INITIALLY DEFERRED) checks consumption_allocations against
	// funding_lot_consumption_state at COMMIT, not per-statement -- the lot,
	// its consumption state, and the matching allocation row must all land
	// in one explicit transaction so the deferred check sees all three by
	// the time it fires (matching newPreSlicePolicyAnchorAccount's own
	// state-then-checkpoint ordering, for the identical reason).
	brokenTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = brokenTx.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,(SELECT invoice_user_id FROM external_accounts WHERE id=$2),$2,$3,'policy-reanchor-broken-order','CNY',
			100000,100000,4000,5000,'verified','COMPLETED',$4,$5,$5,'WALLET_CASH',$5,100000,9000,FALSE,1)`,
		brokenLotID, brokenAccountID, brokenSourceID, testHash("policy-reanchor-broken-payment"), brokenPaymentAt); err != nil {
		t.Fatal(err)
	}
	if _, err = brokenTx.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,1000,90,9000000,9000,0)`, brokenLotID); err != nil {
		t.Fatal(err)
	}
	if _, err = brokenTx.Exec(ctx, `
		INSERT INTO consumption_allocations(
			id,usage_event_id,funding_lot_id,allocation_order,service_units,cash_minor_delta,projection_version)
		VALUES($1,$2,$3,1,90,9000,1)`, randomUUID(), brokenUsageID, brokenLotID); err != nil {
		t.Fatal(err)
	}
	if err = brokenTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	result, err := store.RepairPolicyStartReanchorEligibility(ctx, PolicyStartReanchorRepairInput{
		Apply: true, OperatorID: policyReanchorFixedOperator()}, AuditActor{Type: "admin", ID: policyReanchorFixedOperator()})
	if err != nil {
		t.Fatalf("RepairPolicyStartReanchorEligibility itself must not fail for one account's error: %v", err)
	}
	if len(result.Errors) != 1 || result.Errors[0].ExternalAccountID != brokenAccountID {
		t.Fatalf("errors=%+v, want exactly one error for the broken account %s", result.Errors, brokenAccountID)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ExternalAccountID != goodAccountID || !result.Accounts[0].Reprojected {
		t.Fatalf("accounts=%+v, want the good account processed to completion", result.Accounts)
	}

	// The good account committed fully.
	var goodCutoverAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state
		WHERE external_account_id=$1`, goodAccountID).Scan(&goodCutoverAt); err != nil || !goodCutoverAt.Equal(policyStart.UTC()) {
		t.Fatalf("good account cutover_at=%s err=%v, want policyStart=%s", goodCutoverAt, err, policyStart)
	}

	// The broken account's own failed transaction left no partial trace:
	// cutover_at is still its original (pre-repair) value, and its lot is
	// unchanged.
	var brokenCutoverAt time.Time
	var brokenConsumedCashMinor int64
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state
		WHERE external_account_id=$1`, brokenAccountID).Scan(&brokenCutoverAt); err != nil || !brokenCutoverAt.Equal(brokenOldCutoverAt.UTC()) {
		t.Fatalf("broken account cutover_at=%s err=%v, want unchanged=%s", brokenCutoverAt, err, brokenOldCutoverAt)
	}
	if err := store.pool.QueryRow(ctx, `SELECT consumed_cash_minor FROM funding_lots WHERE id=$1`,
		brokenLotID).Scan(&brokenConsumedCashMinor); err != nil || brokenConsumedCashMinor != 9000 {
		t.Fatalf("broken lot consumed_cash_minor=%d err=%v, want unchanged=9000", brokenConsumedCashMinor, err)
	}
}

type eligibilityStateSnapshot struct {
	CutoverAt           time.Time
	CutoverBalanceUnits string
	FinalizedThrough    time.Time
	BootstrapKind       string
	ProjectionVersion   int64
}

// equal compares two snapshots field-by-field, using time.Time.Equal for the
// timestamp fields rather than a struct-level == (which compares time.Time's
// internal representation directly and can spuriously differ between two
// otherwise-identical values read back from the database).
func (s eligibilityStateSnapshot) equal(other eligibilityStateSnapshot) bool {
	return s.CutoverAt.Equal(other.CutoverAt) && s.CutoverBalanceUnits == other.CutoverBalanceUnits &&
		s.FinalizedThrough.Equal(other.FinalizedThrough) && s.BootstrapKind == other.BootstrapKind &&
		s.ProjectionVersion == other.ProjectionVersion
}

func snapshotEligibilityState(t *testing.T, store *Store, ctx context.Context, accountID string) eligibilityStateSnapshot {
	t.Helper()
	var s eligibilityStateSnapshot
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at,cutover_balance_units::text,finalized_through,
		bootstrap_kind,projection_version FROM source_account_eligibility_state WHERE external_account_id=$1`,
		accountID).Scan(&s.CutoverAt, &s.CutoverBalanceUnits, &s.FinalizedThrough, &s.BootstrapKind, &s.ProjectionVersion); err != nil {
		t.Fatal(err)
	}
	return s
}

func countCheckpoints(t *testing.T, store *Store, ctx context.Context, accountID string) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
