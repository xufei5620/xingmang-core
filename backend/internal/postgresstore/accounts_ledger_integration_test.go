package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// ledgerUUID mirrors autoReconcileUUID's own deterministic-UUID convention
// ('1'=source instance, '2'=user, '3'=account, '5'=funding lot, '6'=freeze,
// '7'=balance checkpoint, '8'=balance checkpoint evaluation, '9'=usage
// event, 'a'=consumption allocation), scoped to this file's own fixtures
// only.
func ledgerUUID(prefix rune, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", prefix, n)
}

// seedLedgerAccount creates a fresh invoice_user + external_account bound to
// sourceID with the given external user id, then bootstraps
// source_account_eligibility_state/source_cutover_manifests via
// UpsertFundingLot's own fixture-bootstrap side effects (see store.go's
// UpsertFundingLot comment) with one WALLET_CASH lot of amountMinor, fully
// consumed (UpsertFundingLot's own default -- see its VerifiedCashMinor/
// ConsumedCashMinor assignment). Returns the lot id for further
// per-scenario mutation (setOpsConsumedCash, deletion, ...).
func seedLedgerAccount(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, externalUserID string, amountMinor int64) (lotID string) {
	t.Helper()
	userID := ledgerUUID('2', accountSuffix(accountID))
	// oidc_subject is keyed by accountID, not externalUserID: two accounts
	// under different sources can deliberately share the same
	// external_user_id (the source_instance_id disambiguation scenario),
	// and invoice_users has a UNIQUE(oidc_issuer,oidc_subject) constraint.
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES($1,'test',$2)`, userID, "ledger-user-"+accountID); err != nil {
		t.Fatal(err)
	}
	// external_subject_hmac must be distinct per account sharing one source
	// instance: external_accounts has a UNIQUE NULLS NOT DISTINCT
	// (source_instance_id, external_subject_hmac) constraint, so leaving it
	// NULL on a second account for the same source collides with the first.
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status) VALUES($1,$2,$3,$4,$5,'test','verified')`, accountID, userID, sourceID, externalUserID, testHash("ledger-hmac-"+accountID)); err != nil {
		t.Fatal(err)
	}
	lotID = ledgerUUID('5', accountSuffix(accountID))
	lot := domain.FundingLot{ID: lotID, PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
		ExternalOrderID: "order-" + accountID, Currency: domain.CurrencyCNY, OriginalMinor: amountMinor, CurrentCapMinor: amountMinor,
		Verification: domain.VerificationVerified, SourceStatus: "COMPLETED", SourceRevision: testHash("ledger-" + accountID),
		CompletedAt: time.Now(), ObservedAt: time.Now()}
	if err := store.UpsertFundingLot(ctx, lot); err != nil {
		t.Fatal(err)
	}
	return lotID
}

// accountSuffix recovers the numeric suffix ledgerUUID('3', n) encoded, so
// the paired user/lot ids can be derived deterministically from the account
// id alone without threading an extra parameter through every call site.
func accountSuffix(accountID string) int {
	var n int
	if _, err := fmt.Sscanf(accountID[len(accountID)-12:], "%012d", &n); err != nil {
		panic(err)
	}
	return n
}

// insertMatchedEvaluation gives an account a fresh, 'matched'
// balance_checkpoint_evaluations row -- the "books balance, nothing
// pending" signal accountBlockStateExpr's not_invoiceable_pending_
// reconciliation branch requires before it will call an otherwise-healthy
// account invoiceable/below_threshold rather than pending (see that
// function's own "no evaluation recorded" branch). checkpointSuffix must be
// unique per call.
func insertMatchedEvaluation(t *testing.T, store *Store, ctx context.Context, sourceID, accountID string, checkpointSuffix int) {
	t.Helper()
	insertEvaluatedCheckpoint(t, store, ctx, sourceID, accountID, checkpointSuffix, "matched", 0, 0)
}

// insertEvaluatedCheckpoint inserts one balance_reconciliation_checkpoints
// row (checkpoint_kind='reconciliation', its own reconciliation_status
// fixed to 'matched' with a zero difference -- the checkpoint's own initial
// classification is not what accountBlockStateExpr reads) plus one
// balance_checkpoint_evaluations row carrying the evaluationStatus this
// test actually wants to pin (the latest-by-projection_version row per
// checkpoint, exactly what accountLedgerLatestEvaluationLateral reads).
// as_of is set safely inside the account's own finalized_through window
// (read back after UpsertFundingLot's own bootstrap).
func insertEvaluatedCheckpoint(t *testing.T, store *Store, ctx context.Context, sourceID, accountID string, checkpointSuffix int, evaluationStatus string, expectedUnits, differenceUnits int64) string {
	t.Helper()
	var manifestHash, configHash, unitCode string
	if err := store.pool.QueryRow(ctx, `SELECT manifest_hash,configuration_hash,unit_code FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&manifestHash, &configHash, &unitCode); err != nil {
		t.Fatal(err)
	}
	var finalizedThrough time.Time
	if err := store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(&finalizedThrough); err != nil {
		t.Fatal(err)
	}
	asOf := finalizedThrough.Add(-time.Minute).Truncate(time.Microsecond)
	checkpointID := ledgerUUID('7', checkpointSuffix)
	checkpointKey := fmt.Sprintf("ckpt-%d", checkpointSuffix)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,checkpoint_kind,
			as_of,balance_service_units,expected_service_units,difference_service_units,unit_code,
			cutover_manifest_hash,configuration_hash,reconciliation_status,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,'reconciliation',$5,0,0,0,$6,$7,$8,'matched',1,$9,$5,$10,$5)`,
		checkpointID, sourceID, accountID, checkpointKey, asOf, unitCode, manifestHash, configHash,
		"cursor:"+checkpointKey, testHash("ledger-checkpoint-"+checkpointKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_checkpoint_evaluations(id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,1,$3,$4,$5)`,
		ledgerUUID('8', checkpointSuffix), checkpointID, expectedUnits, differenceUnits, evaluationStatus); err != nil {
		t.Fatal(err)
	}
	return checkpointID
}

// insertProjectionJob gives an account a queued eligibility_projection_jobs
// row -- the table only ever holds queued/processing/failed rows (a
// successful run unconditionally deletes its own row, see
// processEligibilityProjectionJob), so mere existence is the
// "not finalized yet" signal accountBlockStateExpr reads.
func insertProjectionJob(t *testing.T, store *Store, ctx context.Context, accountID string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,now(),'queued',now())`, accountID); err != nil {
		t.Fatal(err)
	}
}

// insertConsumptionAllocation gives an account one real usage-event-backed
// cash allocation against lotID, dated eventTime -- exercises
// accountLedgerConsumptionTimeline's real join path (consumption_allocations
// joined to source_usage_events and funding_lots) rather than only the
// zero-allocations default. Satisfies enforce_cash_consumption_invoice_policy
// (usage/lot/account contract match, invoice_eligible computed the same way
// enforce_usage_invoice_policy requires) and the deferred
// consumption_allocations_mirror_guard constraint trigger (a no-op for this
// package's fixture-contract accounts, same as setOpsConsumedCash already
// relies on).
func insertConsumptionAllocation(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, lotID string, allocationSuffix int, eventTime time.Time, serviceUnits, cashMinorDelta int64) {
	t.Helper()
	var manifestHash, configHash, unitCode string
	if err := store.pool.QueryRow(ctx, `SELECT manifest_hash,configuration_hash,unit_code FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&manifestHash, &configHash, &unitCode); err != nil {
		t.Fatal(err)
	}
	usageEventID := ledgerUUID('9', allocationSuffix)
	usageKey := fmt.Sprintf("usage-%d", allocationSuffix)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,billing_scope,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at,invoice_eligible)
		VALUES($1,$2,$3,$4,$4,$5,$6,$7,$8,$9,'wallet',$10,$11,$5,$12,$5,TRUE)`,
		usageEventID, sourceID, accountID, usageKey, eventTime, serviceUnits, unitCode, manifestHash, configHash,
		allocationSuffix, "cursor:"+usageKey, testHash("ledger-usage-"+usageKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO consumption_allocations(id,usage_event_id,funding_lot_id,allocation_order,service_units,cash_minor_delta,projection_version)
		VALUES($1,$2,$3,1,$4,$5,1)`,
		ledgerUUID('a', allocationSuffix), usageEventID, lotID, serviceUnits, cashMinorDelta); err != nil {
		t.Fatal(err)
	}
}

func TestAccountLedgerPageCoversEveryBlockStateAndFilters(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := ledgerUUID('1', 900)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name) VALUES($1,'sub2api','ledger-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	// A second source instance, to prove source_instance_id disambiguates
	// two accounts that otherwise share the same external_user_id -- the
	// same CR-0007 precedent eligibility_freezes/eligibility-ledger already
	// established.
	// sub2api, not newapi: seedLedgerAccount's fixture-v3 bootstrap bypasses
	// the newapi dual-control verified-payment trigger only for the source
	// type it was actually written against -- this fixture only needs a
	// second, distinct source_instance_id, not real newapi semantics.
	sourceID2 := ledgerUUID('1', 800)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name) VALUES($1,'sub2api','ledger-test-2')`, sourceID2); err != nil {
		t.Fatal(err)
	}

	// n=901: invoiceable -- healthy account, consumed/invoiceable diverge
	// (reserved/issued nonzero), a fresh 'matched' evaluation on record.
	invoiceableID := ledgerUUID('3', 901)
	invoiceableLot := seedLedgerAccount(t, store, ctx, sourceID, invoiceableID, "ledger-1", 500_000)
	setOpsConsumedCash(t, store, ctx, invoiceableLot, 500_000, 320_000, 1000, 640, 50_000, 90_000)
	insertMatchedEvaluation(t, store, ctx, sourceID, invoiceableID, 9010)

	// n=902: not_invoiceable_pending_reconciliation via the eligibility_status
	// column and its five plaintext columns directly (the common,
	// already-classified case: enterPendingReconciliationTx already ran).
	pendingID := ledgerUUID('3', 902)
	pendingLot := seedLedgerAccount(t, store, ctx, sourceID, pendingID, "ledger-2", 500_000)
	setOpsConsumedCash(t, store, ctx, pendingLot, 500_000, 320_000, 1000, 640, 50_000, 90_000)
	pendingSince := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `
		UPDATE source_account_eligibility_state SET
			eligibility_status='not_invoiceable_pending_reconciliation',
			pending_reconciliation_reason='UNKNOWN_NEGATIVE_BALANCE',
			pending_reconciliation_trigger_type='balance_checkpoint',
			pending_reconciliation_trigger_id='ckpt-test',
			pending_reconciliation_detail='balance_checkpoint ckpt-test reported a negative balance',
			pending_reconciliation_since=$2
		WHERE external_account_id=$1`, pendingID, pendingSince); err != nil {
		t.Fatal(err)
	}

	// n=903: frozen_manual_review, two open eligibility_freezes rows -- the
	// query must surface the latest (by opened_at), not merely any open row.
	frozenID := ledgerUUID('3', 903)
	seedLedgerAccount(t, store, ctx, sourceID, frozenID, "ledger-3", 60_000)
	olderFreezeAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	latestFreezeAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,opened_at) VALUES($1,$2,'EVENT_PAYLOAD_DRIFT','funding_lot','stale-older',$3)`, ledgerUUID('6', 931), frozenID, olderFreezeAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,opened_at) VALUES($1,$2,'SOURCE_GAP','source_stream','usage',$3)`, ledgerUUID('6', 932), frozenID, latestFreezeAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET eligibility_status='frozen' WHERE external_account_id=$1`, frozenID); err != nil {
		t.Fatal(err)
	}

	// n=904: frozen_manual_review with NO open eligibility_freezes row at
	// all -- the task brief's own explicit defensive edge case: must still
	// return a row, never error or silently fall through to a different
	// state.
	frozenNoRowID := ledgerUUID('3', 904)
	seedLedgerAccount(t, store, ctx, sourceID, frozenNoRowID, "ledger-4", 60_000)
	insertMatchedEvaluation(t, store, ctx, sourceID, frozenNoRowID, 9040)
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET eligibility_status='frozen' WHERE external_account_id=$1`, frozenNoRowID); err != nil {
		t.Fatal(err)
	}

	// n=905: active with non_invoiceable_overage_* set -- must not leak
	// into block_state and must not perturb consumed/invoiceable (the
	// overage was never allocated into any cash pool to begin with). A
	// 'matched' evaluation keeps it out of pending_reconciliation.
	overageID := ledgerUUID('3', 905)
	seedLedgerAccount(t, store, ctx, sourceID, overageID, "ledger-5", 250_000)
	insertMatchedEvaluation(t, store, ctx, sourceID, overageID, 9050)
	var manifestHash, configHash, unitCode string
	if err := store.pool.QueryRow(ctx, `SELECT manifest_hash,configuration_hash,unit_code FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&manifestHash, &configHash, &unitCode); err != nil {
		t.Fatal(err)
	}
	overageUsageEventID := ledgerUUID('9', 9051)
	overageUsageAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,billing_scope,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at,invoice_eligible)
		VALUES($1,$2,$3,'overage-event','overage-usage',$4,500,$5,$6,$7,'wallet',1,'cursor:overage:1',$4,$8,$4,TRUE)`,
		overageUsageEventID, sourceID, overageID, overageUsageAt, unitCode, manifestHash, configHash, testHash("overage-revision")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET non_invoiceable_overage_units=500,non_invoiceable_overage_usage_event_id=$2 WHERE external_account_id=$1`, overageID, overageUsageEventID); err != nil {
		t.Fatal(err)
	}

	// n=906: bootstrapped account, zero funding lots (delete the bootstrap
	// lot after the account/state row exist), no evaluation ever recorded,
	// no projection job, no freeze -- the task brief's own hard-rule shape
	// ("an account with no lots, no evaluations, no projection job... must
	// still produce a row with a sensible block_state and reason"). This
	// endpoint's own answer: not_invoiceable_pending_reconciliation ("we
	// have never actually reconciled this account"), not a false
	// "invoiceable" -- see docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md for
	// why this reading was chosen over treating "no evidence" as "fine".
	zeroLotsID := ledgerUUID('3', 906)
	zeroLot := seedLedgerAccount(t, store, ctx, sourceID, zeroLotsID, "ledger-6", 10_000)
	if _, err := store.pool.Exec(ctx, `DELETE FROM funding_lot_consumption_state WHERE funding_lot_id=$1`, zeroLot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM funding_lots WHERE id=$1`, zeroLot); err != nil {
		t.Fatal(err)
	}

	// n=907: active, no open freeze, no pending_reconciliation columns, no
	// projection job -- but the latest recorded balance evaluation is
	// 'negative_frozen'. Not reachable via the normal evaluator today
	// (enterPendingReconciliationTx sets eligibility_status atomically with
	// any negative_frozen evaluation write), constructed directly via SQL
	// to prove accountBlockStateExpr's own defensive "latest evaluation not
	// good" branch fires independently of the eligibility_status column --
	// the same "test the guard in isolation" precedent
	// TestPendingReconciliationOpenFreezeBlocksAutoExit already established
	// for a different guard.
	evalOnlyID := ledgerUUID('3', 907)
	evalOnlyLot := seedLedgerAccount(t, store, ctx, sourceID, evalOnlyID, "ledger-7", 500_000)
	setOpsConsumedCash(t, store, ctx, evalOnlyLot, 500_000, 320_000, 1000, 640, 50_000, 90_000)
	insertEvaluatedCheckpoint(t, store, ctx, sourceID, evalOnlyID, 9070, "negative_frozen", 380, -500)

	// n=908: active, no open freeze, no pending_reconciliation columns, a
	// 'matched' evaluation on record -- but a queued eligibility_projection_
	// jobs row exists. Proves has_projection_job alone drives the state,
	// independent of eligibility_status/evaluation status.
	jobID := ledgerUUID('3', 908)
	jobLot := seedLedgerAccount(t, store, ctx, sourceID, jobID, "ledger-8", 500_000)
	setOpsConsumedCash(t, store, ctx, jobLot, 500_000, 320_000, 1000, 640, 50_000, 90_000)
	insertMatchedEvaluation(t, store, ctx, sourceID, jobID, 9080)
	insertProjectionJob(t, store, ctx, jobID)

	// n=909: below_threshold -- healthy in every other respect (matched
	// evaluation, no freeze, no pending columns, no job), but
	// invoiceable_now_minor is under the threshold this test passes in.
	belowID := ledgerUUID('3', 909)
	belowLot := seedLedgerAccount(t, store, ctx, sourceID, belowID, "ledger-9", 15_000)
	insertMatchedEvaluation(t, store, ctx, sourceID, belowID, 9090)
	_ = belowLot

	// n=810 (source 2): shares external_user_id "ledger-1" with n=901
	// (source 1) -- proves source_instance_id disambiguates.
	otherSourceID := ledgerUUID('3', 810)
	otherSourceLot := seedLedgerAccount(t, store, ctx, sourceID2, otherSourceID, "ledger-1", 40_000)
	insertMatchedEvaluation(t, store, ctx, sourceID2, otherSourceID, 8100)
	_ = otherSourceLot

	const threshold = 20_000

	byID := func(page AccountLedgerPage, accountSuffixWant int) *AccountLedgerListEntry {
		for i := range page.Items {
			if page.Items[i].ExternalAccountID == ledgerUUID('3', accountSuffixWant) {
				return &page.Items[i]
			}
		}
		return nil
	}

	t.Run("invoiceable", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-1", SourceInstanceID: sourceID, ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("expected exactly one item for exact external_user_id+source_instance_id lookup, got %+v", page.Items)
		}
		item := page.Items[0]
		if item.BlockState != AccountBlockStateInvoiceable {
			t.Fatalf("block_state=%q", item.BlockState)
		}
		if item.RechargesSinceStartMinor != 500_000 || item.ConsumedSinceStartMinor != 320_000 || item.InvoiceableNowMinor != 180_000 {
			t.Fatalf("amounts: recharges=%d consumed=%d invoiceable=%d", item.RechargesSinceStartMinor, item.ConsumedSinceStartMinor, item.InvoiceableNowMinor)
		}
		if item.RechargesSinceStartCount != 1 {
			t.Fatalf("recharges_count=%d", item.RechargesSinceStartCount)
		}
		if item.IssuedMinor != 90_000 {
			t.Fatalf("issued_minor=%d", item.IssuedMinor)
		}
		if item.SourceType != domain.SourceSub2API {
			t.Fatalf("source_type=%+v", item)
		}
		if item.LastCheckpointAt.IsZero() {
			t.Fatal("expected last_checkpoint_at to be populated from the matched evaluation fixture")
		}
	})

	t.Run("source_instance_id disambiguates accounts sharing one external_user_id", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-1", SourceInstanceID: sourceID2, ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ExternalAccountID != otherSourceID {
			t.Fatalf("expected only the source-2 account, got %+v", page.Items)
		}
	})

	t.Run("not_invoiceable_pending_reconciliation via eligibility_status column", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-2", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		item := page.Items[0]
		if item.BlockState != AccountBlockStateNotInvoiceablePendingReconciliation {
			t.Fatalf("block_state=%q", item.BlockState)
		}
		if item.InvoiceableNowMinor != 180_000 {
			t.Fatalf("invoiceable_now_minor=%d, threshold_reached must still be computable even while blocked", item.InvoiceableNowMinor)
		}
	})

	t.Run("frozen_manual_review reads the latest open eligibility_freezes row", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-3", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		if page.Items[0].BlockState != AccountBlockStateFrozenManualReview {
			t.Fatalf("block_state=%q", page.Items[0].BlockState)
		}
		detail, err := store.GetAccountLedgerDetail(ctx, frozenID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		if detail.FreezeReason != "SOURCE_GAP" {
			t.Fatalf("expected the latest freeze (SOURCE_GAP), got %q", detail.FreezeReason)
		}
		if !detail.FreezeOpenedAt.Equal(latestFreezeAt) {
			t.Fatalf("freeze_opened_at=%s want=%s (the latest freeze, not the older one)", detail.FreezeOpenedAt, latestFreezeAt)
		}
	})

	t.Run("frozen_manual_review with no open freeze row still returns a defined row", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-4", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("a frozen account with no open freeze row must still appear: %+v", page.Items)
		}
		if page.Items[0].BlockState != AccountBlockStateFrozenManualReview {
			t.Fatalf("block_state=%q, want frozen_manual_review even without an open freeze row (eligibility_status='frozen' alone)", page.Items[0].BlockState)
		}
		detail, err := store.GetAccountLedgerDetail(ctx, frozenNoRowID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		if detail.HasOpenFreeze {
			t.Fatal("expected HasOpenFreeze=false for this defensive fixture")
		}
		if detail.FreezeReason != "" {
			t.Fatalf("expected empty FreezeReason with no open freeze row, got %q", detail.FreezeReason)
		}
	})

	t.Run("overage does not leak into block_state or perturb amounts", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-5", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		item := page.Items[0]
		if item.BlockState != AccountBlockStateInvoiceable {
			t.Fatalf("block_state=%q, recorded usage overage must not surface as a block", item.BlockState)
		}
		if item.RechargesSinceStartMinor != 250_000 || item.ConsumedSinceStartMinor != 250_000 || item.InvoiceableNowMinor != 250_000 {
			t.Fatalf("amounts perturbed by recorded overage: %+v", item)
		}
	})

	t.Run("zero lots, no evaluation, no job, no freeze still returns a defined pending row", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-6", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("bootstrapped account with zero lots must still appear: %+v", page.Items)
		}
		item := page.Items[0]
		if item.RechargesSinceStartMinor != 0 || item.ConsumedSinceStartMinor != 0 || item.InvoiceableNowMinor != 0 {
			t.Fatalf("expected all-zero amounts, got %+v", item)
		}
		if item.BlockState != AccountBlockStateNotInvoiceablePendingReconciliation {
			t.Fatalf("block_state=%q, want not_invoiceable_pending_reconciliation (never reconciled -- see the hard-rule comment above)", item.BlockState)
		}
		if !item.LastCheckpointAt.IsZero() {
			t.Fatalf("expected zero LastCheckpointAt (no evaluation was ever recorded), got %s", item.LastCheckpointAt)
		}
	})

	t.Run("latest evaluation alone (negative_frozen) drives pending state", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-7", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].BlockState != AccountBlockStateNotInvoiceablePendingReconciliation {
			t.Fatalf("items=%+v", page.Items)
		}
	})

	// XM-INV-LEDGER-SETTLING-STATE renamed this outcome. A queued projection
	// job used to report not_invoiceable_pending_reconciliation, which claims
	// the books and the source disagree; it means only that the figures are
	// not final yet. Jobs are enqueued every finalization cycle, so healthy
	// production accounts flickered into that state all day -- the operator
	// screenshot that started this had two of eight accounts showing it while
	// the database had none in that eligibility_status at all.
	t.Run("projection job alone reports settling, not a reconciliation problem", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-8", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].BlockState != AccountBlockStateSettling {
			t.Fatalf("items=%+v", page.Items)
		}
	})

	t.Run("a real reconciliation problem still outranks a queued job", func(t *testing.T) {
		// ledger-7 has the negative_frozen evaluation AND gets a job here:
		// the dispute is what the operator has to act on, so it wins.
		insertProjectionJob(t, store, ctx, evalOnlyID)
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-7", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].BlockState != AccountBlockStateNotInvoiceablePendingReconciliation {
			t.Fatalf("items=%+v", page.Items)
		}
	})

	t.Run("below_threshold", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-9", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		item := page.Items[0]
		if item.BlockState != AccountBlockStateBelowThreshold {
			t.Fatalf("block_state=%q invoiceable=%d threshold=%d", item.BlockState, item.InvoiceableNowMinor, threshold)
		}
	})

	t.Run("invalid before_id is rejected", func(t *testing.T) {
		if _, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{BeforeID: "not-a-uuid", ThresholdMinor: threshold}); err == nil {
			t.Fatal("expected an error for a malformed before_id cursor")
		}
	})

	t.Run("invalid external_user_id filter is rejected", func(t *testing.T) {
		if _, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: strings.Repeat("x", 513), ThresholdMinor: threshold}); err == nil {
			t.Fatal("expected an error for an over-long external_user_id filter")
		}
		if _, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "bad\r\nvalue", ThresholdMinor: threshold}); err == nil {
			t.Fatal("expected an error for a CR/LF-bearing external_user_id filter")
		}
	})

	t.Run("invalid sort is rejected", func(t *testing.T) {
		if _, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{Sort: "not_a_real_sort", ThresholdMinor: threshold}); err == nil {
			t.Fatal("expected an error for an unrecognized sort value")
		}
	})

	t.Run("mismatched default-sort cursor pair is rejected", func(t *testing.T) {
		minor := int64(1000)
		if _, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{BeforeInvoiceableMinor: &minor, ThresholdMinor: threshold}); err == nil {
			t.Fatal("expected an error for before_invoiceable_minor without before_id")
		}
	})

	t.Run("keyset pagination (default sort, invoiceable_now_minor desc) walks every account exactly once", func(t *testing.T) {
		var seen []string
		query := AccountLedgerPageQuery{Limit: 3, SourceInstanceID: sourceID, ThresholdMinor: threshold}
		for i := 0; i < 20; i++ {
			page, err := store.ListAccountLedgerPage(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 3 {
				t.Fatalf("page exceeded requested limit: %d items", len(page.Items))
			}
			for _, item := range page.Items {
				seen = append(seen, item.ExternalAccountID)
			}
			if !page.HasMore {
				break
			}
			if page.NextBeforeID == "" || page.NextBeforeInvoiceableMinor == nil {
				t.Fatal("has_more=true but the cursor envelope is incomplete")
			}
			query = AccountLedgerPageQuery{Limit: 3, SourceInstanceID: sourceID, ThresholdMinor: threshold,
				BeforeInvoiceableMinor: page.NextBeforeInvoiceableMinor, BeforeID: page.NextBeforeID}
		}
		wantCount := 9 // n=901..909 -- SourceInstanceID:sourceID excludes both n=810 (different source) and this store's own base fixture account (yet another source)
		if len(seen) != wantCount {
			t.Fatalf("walked %d accounts, want %d: %v", len(seen), wantCount, seen)
		}
		seenSet := map[string]bool{}
		for _, id := range seen {
			if seenSet[id] {
				t.Fatalf("account %s walked twice: %v", id, seen)
			}
			seenSet[id] = true
		}
	})

	t.Run("keyset pagination (sort=block_state) walks every account exactly once, most-needs-attention first", func(t *testing.T) {
		var seen []string
		var seenStates []string
		query := AccountLedgerPageQuery{Limit: 3, SourceInstanceID: sourceID, Sort: "block_state", ThresholdMinor: threshold}
		for i := 0; i < 20; i++ {
			page, err := store.ListAccountLedgerPage(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 3 {
				t.Fatalf("page exceeded requested limit: %d items", len(page.Items))
			}
			for _, item := range page.Items {
				seen = append(seen, item.ExternalAccountID)
				seenStates = append(seenStates, item.BlockState)
			}
			if !page.HasMore {
				break
			}
			if page.NextBeforeID == "" {
				t.Fatal("has_more=true but next_before_id is empty")
			}
			query = AccountLedgerPageQuery{Limit: 3, SourceInstanceID: sourceID, Sort: "block_state", ThresholdMinor: threshold, BeforeID: page.NextBeforeID}
		}
		if len(seen) != 9 {
			t.Fatalf("walked %d accounts, want 9: %v", len(seen), seen)
		}
		// settling shares rank 1 with pending reconciliation, mirroring
		// accountBlockStateRank (XM-INV-LEDGER-SETTLING-STATE).
		rank := map[string]int{AccountBlockStateFrozenManualReview: 0, AccountBlockStateNotInvoiceablePendingReconciliation: 1, AccountBlockStateSettling: 1, AccountBlockStateBelowThreshold: 2, AccountBlockStateInvoiceable: 3}
		for i := 1; i < len(seenStates); i++ {
			if rank[seenStates[i]] < rank[seenStates[i-1]] {
				t.Fatalf("block_state sort went backwards at position %d: %v", i, seenStates)
			}
		}
		if rank[seenStates[0]] != 0 {
			t.Fatalf("expected a frozen_manual_review account first, got sequence %v", seenStates)
		}
	})

	t.Run("a stale block_state cursor yields a defined empty page, not an error", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{Sort: "block_state", BeforeID: ledgerUUID('3', 999999), ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 || page.HasMore {
			t.Fatalf("expected an empty, defined page for an unknown cursor, got %+v", page)
		}
	})

	t.Run("limit is bounded to the documented default and max of 100", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{Limit: 5000, ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if byID(page, 901) == nil || byID(page, 909) == nil {
			t.Fatalf("expected every fixture account present in an unfiltered page: %+v", page.Items)
		}
	})
}

func TestAccountLedgerDetail(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := ledgerUUID('1', 700)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name) VALUES($1,'sub2api','ledger-detail-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	const threshold = 20_000

	t.Run("unknown account returns not found", func(t *testing.T) {
		if _, err := store.GetAccountLedgerDetail(ctx, ledgerUUID('3', 799999), threshold); err == nil {
			t.Fatal("expected an error for an unknown account id")
		}
	})

	t.Run("malformed account id returns not found, not a 500-shaped error", func(t *testing.T) {
		if _, err := store.GetAccountLedgerDetail(ctx, "not-a-uuid", threshold); err == nil {
			t.Fatal("expected an error for a malformed account id")
		}
	})

	t.Run("full detail round trip: recharges, opening balance, consumption timeline", func(t *testing.T) {
		accountID := ledgerUUID('3', 701)
		lotID := seedLedgerAccount(t, store, ctx, sourceID, accountID, "detail-1", 300_000)
		setOpsConsumedCash(t, store, ctx, lotID, 300_000, 90_000, 1000, 300, 0, 0)
		insertMatchedEvaluation(t, store, ctx, sourceID, accountID, 7010)

		var cutoverAt time.Time
		if err := store.pool.QueryRow(ctx, `SELECT cutover_at FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(&cutoverAt); err != nil {
			t.Fatal(err)
		}
		insertConsumptionAllocation(t, store, ctx, sourceID, accountID, lotID, 7011, cutoverAt.Add(2*time.Hour), 100, 30_000)
		insertConsumptionAllocation(t, store, ctx, sourceID, accountID, lotID, 7012, cutoverAt.Add(26*time.Hour), 50, 15_000)

		detail, err := store.GetAccountLedgerDetail(ctx, accountID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		if detail.BlockState != AccountBlockStateInvoiceable {
			t.Fatalf("block_state=%q", detail.BlockState)
		}
		if detail.OpeningBalanceUnitCode == "" {
			t.Fatal("expected a non-empty opening balance unit_code")
		}
		if len(detail.Recharges) != 1 || detail.Recharges[0].FundingLotID != lotID || detail.Recharges[0].AmountMinor != 300_000 {
			t.Fatalf("recharges=%+v", detail.Recharges)
		}
		if detail.Recharges[0].EligibilityKind != "WALLET_CASH" || detail.Recharges[0].RefundFrozen {
			t.Fatalf("recharge fields: %+v", detail.Recharges[0])
		}
		if len(detail.ConsumptionTimeline) != 2 {
			t.Fatalf("expected two daily buckets, got %+v", detail.ConsumptionTimeline)
		}
		var total int64
		for _, day := range detail.ConsumptionTimeline {
			total += day.ConsumedMinor
			if len(day.Date) != 10 {
				t.Fatalf("expected a YYYY-MM-DD date, got %q", day.Date)
			}
		}
		if total != 45_000 {
			t.Fatalf("consumption timeline total=%d want=45000", total)
		}
	})

	t.Run("pending reconciliation detail exposes the raw ingredients block_reason is built from", func(t *testing.T) {
		accountID := ledgerUUID('3', 702)
		lotID := seedLedgerAccount(t, store, ctx, sourceID, accountID, "detail-2", 300_000)
		setOpsConsumedCash(t, store, ctx, lotID, 300_000, 90_000, 1000, 300, 0, 0)
		since := time.Now().UTC().Add(-45 * time.Minute).Truncate(time.Microsecond)
		if _, err := store.pool.Exec(ctx, `
			UPDATE source_account_eligibility_state SET
				eligibility_status='not_invoiceable_pending_reconciliation',
				pending_reconciliation_reason='UNKNOWN_NEGATIVE_BALANCE',
				pending_reconciliation_trigger_type='balance_checkpoint',
				pending_reconciliation_trigger_id='ckpt-detail-2',
				pending_reconciliation_detail='balance_checkpoint ckpt-detail-2 reported a negative balance',
				pending_reconciliation_since=$2
			WHERE external_account_id=$1`, accountID, since); err != nil {
			t.Fatal(err)
		}
		insertEvaluatedCheckpoint(t, store, ctx, sourceID, accountID, 7020, "negative_frozen", 38000, -5000)

		detail, err := store.GetAccountLedgerDetail(ctx, accountID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		if detail.BlockState != AccountBlockStateNotInvoiceablePendingReconciliation {
			t.Fatalf("block_state=%q", detail.BlockState)
		}
		if detail.PendingReconciliationReason != "UNKNOWN_NEGATIVE_BALANCE" {
			t.Fatalf("pending_reconciliation_reason=%q", detail.PendingReconciliationReason)
		}
		if !detail.PendingReconciliationSince.Equal(since) {
			t.Fatalf("pending_reconciliation_since=%s want=%s", detail.PendingReconciliationSince, since)
		}
		if detail.LatestEvaluationStatus != "negative_frozen" {
			t.Fatalf("latest_evaluation_status=%q", detail.LatestEvaluationStatus)
		}
		if detail.LatestEvaluationExpectedUnits != "38000" || detail.LatestEvaluationDifferenceUnits != "-5000" {
			t.Fatalf("latest evaluation units: expected=%q difference=%q", detail.LatestEvaluationExpectedUnits, detail.LatestEvaluationDifferenceUnits)
		}
	})

	t.Run("last_reconciled_at is the latest matched evaluation, distinct from last_checkpoint_at", func(t *testing.T) {
		accountID := ledgerUUID('3', 703)
		lotID := seedLedgerAccount(t, store, ctx, sourceID, accountID, "detail-3", 100_000)
		setOpsConsumedCash(t, store, ctx, lotID, 100_000, 20_000, 1000, 200, 0, 0)
		var finalizedThrough time.Time
		if err := store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(&finalizedThrough); err != nil {
			t.Fatal(err)
		}
		insertEvaluatedCheckpoint(t, store, ctx, sourceID, accountID, 7030, "matched", 20000, 0)
		// A later evaluation with a mismatch -- last_checkpoint_at must move
		// to this one, but last_reconciled_at must stay pinned to the
		// earlier matched evaluation.
		checkpointID2 := ledgerUUID('7', 7031)
		var manifestHash, configHash, unitCode string
		if err := store.pool.QueryRow(ctx, `SELECT manifest_hash,configuration_hash,unit_code FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&manifestHash, &configHash, &unitCode); err != nil {
			t.Fatal(err)
		}
		laterAsOf := finalizedThrough.Add(-30 * time.Second).Truncate(time.Microsecond)
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO balance_reconciliation_checkpoints(
				id,source_instance_id,external_account_id,external_event_id,checkpoint_id,checkpoint_kind,
				as_of,balance_service_units,expected_service_units,difference_service_units,unit_code,
				cutover_manifest_hash,configuration_hash,reconciliation_status,
				source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,'ckpt-7031','ckpt-7031','reconciliation',$4,0,0,0,$5,$6,$7,'matched',2,'cursor:7031',$4,$8,$4)`,
			checkpointID2, sourceID, accountID, laterAsOf, unitCode, manifestHash, configHash, testHash("ledger-checkpoint-7031")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO balance_checkpoint_evaluations(id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
			VALUES($1,$2,1,19500,-500,'negative_frozen')`, ledgerUUID('8', 7031), checkpointID2); err != nil {
			t.Fatal(err)
		}

		detail, err := store.GetAccountLedgerDetail(ctx, accountID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		if detail.LatestEvaluationStatus != "negative_frozen" {
			t.Fatalf("expected the later, unmatched evaluation to be latest: %q", detail.LatestEvaluationStatus)
		}
		if detail.LastReconciledAt.After(detail.LastCheckpointAt) {
			t.Fatalf("last_reconciled_at=%s should not be after last_checkpoint_at=%s", detail.LastReconciledAt, detail.LastCheckpointAt)
		}
		if detail.LastReconciledAt.Equal(detail.LastCheckpointAt) {
			t.Fatalf("last_reconciled_at should be the earlier matched evaluation, not the later mismatched one")
		}
	})
}

// TestResolvedFreezeStopsBlockingOnTheEvidenceItAnswered is
// XM-INV-RESOLVED-FREEZE-UNBLOCKS's guard.
//
// Production account 2820: two carry-forward proofs on 2026-09-02 showed the
// same unexplained +¥0.50, the account was frozen, an operator resolved the
// freeze on 2026-09-03, and it then stayed 对账中暂不可开票 indefinitely. The
// account consumes nothing, so no newer evidence is ever produced, so the
// 09-02 evaluation stays "latest" forever and keeps blocking a question a
// human already answered. Resolving a freeze is a judgement about exactly
// that evidence, and it is newer than the evaluation it supersedes.
func TestResolvedFreezeStopsBlockingOnTheEvidenceItAnswered(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := ledgerUUID('1', 940)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name) VALUES($1,'sub2api','ledger-resolved-freeze')`, sourceID); err != nil {
		t.Fatal(err)
	}

	accountID := ledgerUUID('3', 941)
	lot := seedLedgerAccount(t, store, ctx, sourceID, accountID, "ledger-941", 500_000)
	setOpsConsumedCash(t, store, ctx, lot, 500_000, 320_000, 1000, 640, 50_000, 90_000)
	// The evidence an operator later ruled on: a gap, not a match.
	insertEvaluatedCheckpoint(t, store, ctx, sourceID, accountID, 9410, "source_gap_frozen", 1_000_000_000, 50_000_000)

	const threshold = 20_000
	blockState := func() string {
		t.Helper()
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{ExternalUserID: "ledger-941", ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		return page.Items[0].BlockState
	}

	// While the freeze is open the account is frozen_manual_review, and that
	// arm is untouched by this change.
	var evidenceAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT max(as_of) FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1`, accountID).Scan(&evidenceAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,opened_at)
		VALUES($1,$2,'SOURCE_GAP','balance_checkpoint','ckpt-9410',$3)`,
		ledgerUUID('6', 941), accountID, evidenceAt); err != nil {
		t.Fatal(err)
	}
	if got := blockState(); got != AccountBlockStateFrozenManualReview {
		t.Fatalf("block_state=%q while the freeze is open, want frozen_manual_review", got)
	}

	// The operator resolves it, after the evidence. Nothing else changes --
	// no new checkpoint, no new evaluation, because this account is idle.
	// The schema requires a resolution to carry who did it and the evidence
	// they filed -- that is what makes it a judgement worth honouring here.
	if _, err := store.pool.Exec(ctx, `UPDATE eligibility_freezes
		SET status='resolved',resolved_at=$2,resolved_by=$3,resolution_evidence_hash=$4,
			resolution_evidence_ciphertext=$5,resolution_note_ciphertext=$5,
			resolution_note_hash=$6,resolution_version=2
		WHERE id=$1`,
		ledgerUUID('6', 941), evidenceAt.Add(time.Hour),
		ledgerUUID('2', 941), testHash("resolution-evidence-941"),
		bytes.Repeat([]byte{9}, 32), testHash("resolution-note-941")); err != nil {
		t.Fatal(err)
	}
	if got := blockState(); got == AccountBlockStateNotInvoiceablePendingReconciliation {
		t.Fatal("a resolved freeze left the account blocked on the very evidence it answered: " +
			"an idle account can never produce newer evidence, so this never clears")
	}

	// The boundary runs the other way too: a resolution that predates the
	// evidence cannot vouch for it, so the account blocks again. (The
	// evidence itself is immutable by design -- source eligibility facts
	// cannot be moved -- so this moves the resolution instead, which is the
	// same ordering question from the other side.)
	if _, err := store.pool.Exec(ctx, `UPDATE eligibility_freezes
		SET resolved_at=$2 WHERE id=$1`,
		ledgerUUID('6', 941), evidenceAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := blockState(); got != AccountBlockStateNotInvoiceablePendingReconciliation {
		t.Fatalf("block_state=%q, want pending: a resolution older than the evidence adjudicates nothing", got)
	}
}
