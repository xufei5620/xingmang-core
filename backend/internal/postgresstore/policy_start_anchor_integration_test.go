package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"
)

// policyStartUUID generates deterministic UUID-shaped literals for this
// file's fixtures, matching this package's established convention
// ('1'=source instance, '2'=user, '3'=account, '8'/'9'=event/cycle ids).
// Every test in this file gets its own fresh schema (integrationStore's DROP
// SCHEMA CASCADE), so reusing the same n across different test functions in
// this file is safe.
func policyStartUUID(prefix rune, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", prefix, n)
}

// newPolicyStartMinimalAccount constructs, via direct SQL (matching this
// package's established repair-tool-test precedent, e.g.
// newQueueNarrowAccount), a bare account with a SIGNED_CUTOVER eligibility
// state row -- just enough to satisfy source_credit_events/
// source_usage_events' own trusted-state trigger so a scenario can insert
// facts directly. bootstrap_kind/cutover_at here are otherwise irrelevant to
// what this file's derivation tests exercise.
func newPolicyStartMinimalAccount(t *testing.T, store *Store, ctx context.Context, idSuffix int, policyStart time.Time) (accountID, sourceID, manifestHash, configHash string) {
	t.Helper()
	sourceID = policyStartUUID('1', idSuffix)
	userID := policyStartUUID('2', idSuffix)
	accountID = policyStartUUID('3', idSuffix)
	manifestHash = testHash(fmt.Sprintf("policy-start-minimal-manifest-%d", idSuffix))
	configHash = testHash(fmt.Sprintf("policy-start-minimal-config-%d", idSuffix))
	cutover := policyStart.UTC().Add(-1 * time.Hour).Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-start-minimal-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutover,
		configHash, testHash(fmt.Sprintf("policy-start-minimal-baseline-%d", idSuffix))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2)`, userID, fmt.Sprintf("policy-start-minimal-user-%d", idSuffix)); err != nil {
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
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900,'active')`,
		accountID, sourceID, cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	return accountID, sourceID, manifestHash, configHash
}

// TestDeriveCutoverBalanceUnitsSubtractsCreditsAddsUsageInWindow directly
// unit-tests deriveCutoverBalanceUnitsTx (design XM-INV-ELIG-SIMPLIFY section
// 3(D)): checkpoint balance 1000, a 200-unit credit and a 50-unit usage fact
// both dated inside (policyStart,checkpointAsOf], derived=1000-200+50=850. A
// third credit dated before the policy start must not affect the result.
func TestDeriveCutoverBalanceUnitsSubtractsCreditsAddsUsageInWindow(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-3 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	accountID, sourceID, manifestHash, configHash := newPolicyStartMinimalAccount(t, store, ctx, 200, policyStart)

	checkpointAsOf := policyStart.Add(2 * time.Hour)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "derive-credit",
		policyStart.Add(30*time.Minute), "200", "BONUS", 1, manifestHash, configHash)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "derive-usage",
		policyStart.Add(1*time.Hour), "50", 2, manifestHash, configHash)
	// Before the policy start -- must not affect the derivation.
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "derive-credit-outside",
		policyStart.Add(-30*time.Minute), "999", "BONUS", 3, manifestHash, configHash)
	// After the checkpoint -- must not affect the derivation either.
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "derive-usage-outside",
		checkpointAsOf.Add(30*time.Minute), "777", 4, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	derived, err := deriveCutoverBalanceUnitsTx(ctx, tx, accountID, policyStart, checkpointAsOf, big.NewInt(1000))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if derived.Cmp(big.NewInt(850)) != 0 {
		t.Fatalf("derived=%s, want 850 (1000-200+50)", derived)
	}
}

// TestDeriveCutoverBalanceUnitsFloorsAtZero covers deriveCutoverBalanceUnitsTx's
// own defined-outcome floor: a window credit larger than the checkpoint
// balance plus window usage would naively go negative -- cutover_balance_units
// carries a NOT NULL CHECK (>=0), so this must floor at zero, not error.
func TestDeriveCutoverBalanceUnitsFloorsAtZero(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-3 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	accountID, sourceID, manifestHash, configHash := newPolicyStartMinimalAccount(t, store, ctx, 201, policyStart)

	checkpointAsOf := policyStart.Add(2 * time.Hour)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "derive-floor-credit",
		policyStart.Add(30*time.Minute), "500", "BONUS", 1, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	derived, err := deriveCutoverBalanceUnitsTx(ctx, tx, accountID, policyStart, checkpointAsOf, big.NewInt(10))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if derived.Sign() != 0 {
		t.Fatalf("derived=%s, want 0 (floored, naive value would be 10-500=-490)", derived)
	}
}

// policyStartBootstrapFixture builds the account/manifest/stream scaffolding
// shared by this file's ObserveBalanceCheckpoint-driven scenarios (matching
// TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy's
// own construction in policy_anchor_integration_test.go).
func policyStartBootstrapFixture(t *testing.T, idSuffix int, policyStart time.Time) (store *Store, ctx context.Context, sourceID, accountID, manifestHash, configHash string, chain *v3TestChain) {
	t.Helper()
	store, ctx = integrationStoreWithPolicyStart(t, policyStart)
	sourceID = policyStartUUID('1', idSuffix)
	userID := policyStartUUID('2', idSuffix)
	accountID = policyStartUUID('3', idSuffix)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-start-bootstrap-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2)`, userID, fmt.Sprintf("policy-start-bootstrap-user-%d", idSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,$4,'test','verified')`, accountID, userID, sourceID, fmt.Sprint(idSuffix)); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}

	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain = newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: policyStartUUID('8', idSuffix*10+1),
		EntityType: "cutover_manifest", Operation: "upsert",
		PayloadHash:       testHash(fmt.Sprintf("policy-start-bootstrap-manifest-event-%d", idSuffix)),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances", policyStartUUID('9', idSuffix*10+1), cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash = testHash(fmt.Sprintf("policy-start-bootstrap-manifest-%d", idSuffix))
	configHash = testHash(fmt.Sprintf("policy-start-bootstrap-config-%d", idSuffix))
	snapshotHash := testHash(fmt.Sprintf("policy-start-bootstrap-snapshot-%d", idSuffix))
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: snapshotHash,
		BaselineSnapshotHash: snapshotHash, BaselineRowCount: 0, SigningKeyID: "ignored-payload-key",
		CutoverAt: cutoverAt, DatabaseClock: cutoverAt, StreamWatermarkAt: cutoverAt,
		ExternalEventID: manifestEvent.EventID, BatchID: manifestCycle.batchID,
		ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)
	return store, ctx, sourceID, accountID, manifestHash, configHash, chain
}

// TestPolicyStartBootstrapAnchorsAtPolicyStartAndSynthesizesReconciliationCheckpoint
// covers design XM-INV-ELIG-SIMPLIFY section 3(D) item 1 through the real
// ObserveBalanceCheckpoint entrypoint: cutover_at becomes the global policy
// start, not the triggering checkpoint's own as_of (this account has no
// window facts, so the derived balance equals the checkpoint's own raw
// balance -- window arithmetic itself is covered directly by
// TestDeriveCutoverBalanceUnitsSubtractsCreditsAddsUsageInWindow above), and
// the derived reconciliation checkpoint row exists exactly as migration
// 0016/0021's deferred COMMIT-time validation requires -- the bootstrap
// committing without error is itself proof that validation passed; this test
// also asserts the row directly. This is also the required "account first
// observed after policy start" scenario: this account has no prior state at
// all, and its first-ever checkpoint arrives three hours after the policy
// start.
func TestPolicyStartBootstrapAnchorsAtPolicyStartAndSynthesizesReconciliationCheckpoint(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour).Truncate(time.Second)
	store, ctx, sourceID, accountID, manifestHash, configHash, chain := policyStartBootstrapFixture(t, 210, policyStart)

	postAsOf := policyStart.Add(3 * time.Hour)
	postEvent := SourceBatchEvent{EventID: policyStartUUID('8', 2102),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-start-bootstrap-post-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: postAsOf}
	postCycle := chain.commit(t, store, ctx, sourceID, "balances", policyStartUUID('9', 2102), postAsOf, []SourceBatchEvent{postEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "210", ExternalEventID: postEvent.EventID,
		CheckpointID: "reconcile-post-210", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(postCycle.cycleID),
		SnapshotRowCount: "1", AsOf: postAsOf, ObservedAt: postAsOf, StreamWatermarkAt: postAsOf,
		SourceCursor: "balance:210:post", SourceRevision: postEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: postCycle.batchID, ScanCycleID: postCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("post-policy baseline checkpoint should bootstrap POLICY_ANCHOR directly: %v", err)
	}

	var bootstrapKind string
	var storedCutoverAt, storedFinalizedThrough time.Time
	var cutoverBalance string
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind,cutover_at,cutover_balance_units::text,finalized_through
		FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(
		&bootstrapKind, &storedCutoverAt, &cutoverBalance, &storedFinalizedThrough); err != nil {
		t.Fatal(err)
	}
	if bootstrapKind != "POLICY_ANCHOR" || !storedCutoverAt.Equal(policyStart.UTC()) || cutoverBalance != "500" {
		t.Fatalf("policy anchor bootstrap kind=%s cutover_at=%s (want %s) balance=%s (want 500)",
			bootstrapKind, storedCutoverAt, policyStart, cutoverBalance)
	}
	if !storedFinalizedThrough.Equal(policyStart.UTC()) {
		t.Fatalf("finalized_through=%s, want cutover_at=%s", storedFinalizedThrough, policyStart)
	}

	var derivedAsOf time.Time
	var derivedBalance, derivedKind string
	if err := store.pool.QueryRow(ctx, `SELECT as_of,balance_service_units::text,checkpoint_kind
		FROM balance_reconciliation_checkpoints WHERE external_account_id=$1 AND checkpoint_id=$2`,
		accountID, "policy-start:reconcile-post-210").Scan(&derivedAsOf, &derivedBalance, &derivedKind); err != nil {
		t.Fatalf("derived reconciliation checkpoint row missing: %v", err)
	}
	if !derivedAsOf.Equal(policyStart.UTC()) || derivedBalance != "500" || derivedKind != "reconciliation" {
		t.Fatalf("derived checkpoint as_of=%s balance=%s kind=%s, want %s/500/reconciliation",
			derivedAsOf, derivedBalance, derivedKind, policyStart)
	}

	var checkpointCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1`, accountID).Scan(&checkpointCount); err != nil || checkpointCount != 2 {
		t.Fatalf("checkpoint count=%d err=%v, want 2 (the real triggering checkpoint and the derived one)", checkpointCount, err)
	}
}

// TestPolicyStartBootstrapFirstCheckpointBeforePolicyStartStillSkipped
// confirms the unchanged edge case design 3(D) item 3 requires: a first
// checkpoint dated before the policy start is still ignored without
// bootstrapping anything -- this behavior is untouched by this slice (2.1's
// own pre-existing skip, still exercised end-to-end by
// TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy
// in policy_anchor_integration_test.go); this is a focused, independent
// confirmation in this file too.
func TestPolicyStartBootstrapFirstCheckpointBeforePolicyStartStillSkipped(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour).Truncate(time.Second)
	store, ctx, sourceID, accountID, manifestHash, configHash, chain := policyStartBootstrapFixture(t, 211, policyStart)

	preAsOf := policyStart.Add(-1 * time.Hour)
	preEvent := SourceBatchEvent{EventID: policyStartUUID('8', 2112),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-start-bootstrap-pre-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: preAsOf}
	preCycle := chain.commit(t, store, ctx, sourceID, "balances", policyStartUUID('9', 2112), preAsOf, []SourceBatchEvent{preEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "211", ExternalEventID: preEvent.EventID,
		CheckpointID: "reconcile-pre-211", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(preCycle.cycleID),
		SnapshotRowCount: "1", AsOf: preAsOf, ObservedAt: preAsOf, StreamWatermarkAt: preAsOf,
		SourceCursor: "balance:211:pre", SourceRevision: preEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: preCycle.batchID, ScanCycleID: preCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("pre-policy reconciliation checkpoint was not ignored: %v", err)
	}
	var stateCount, checkpointCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("pre-policy checkpoint must not bootstrap eligibility state: count=%d err=%v", stateCount, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1`, accountID).Scan(&checkpointCount); err != nil || checkpointCount != 0 {
		t.Fatalf("pre-policy checkpoint must not insert any checkpoint row: count=%d err=%v", checkpointCount, err)
	}
}

// TestPolicyStartBootstrapIncludesInWindowCashFundingLot covers this slice's
// own "confirm with a test" requirement: a WALLET_CASH funding lot with
// completed_at inside (policyStart,checkpointAsOf] -- previously permanently
// excluded by buildEligibilityProjectionTx's completed_at>cutover_at
// predicate when cutover_at was the triggering checkpoint's own (later)
// as_of -- is automatically included once cutover_at becomes the policy
// start, with no query change needed (buildEligibilityProjectionTx's own
// predicate is generic). The lot is inserted before the account has any
// eligibility state at all: funding_lots carries no trusted-state trigger
// (payments are accepted independent of eligibility bootstrap timing),
// unlike source_credit_events/source_usage_events.
func TestPolicyStartBootstrapIncludesInWindowCashFundingLot(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour).Truncate(time.Second)
	store, ctx, sourceID, accountID, manifestHash, configHash, chain := policyStartBootstrapFixture(t, 212, policyStart)

	lotID := randomUUID()
	paymentAt := policyStart.Add(1 * time.Hour)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,(SELECT invoice_user_id FROM external_accounts WHERE id=$2),$2,$3,'policy-start-window-order','CNY',
			50000,50000,0,0,'verified','COMPLETED',$4,$5,$5,'WALLET_CASH',$5,50000,0,FALSE,1)`,
		lotID, accountID, sourceID, testHash("policy-start-window-payment"), paymentAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,500,0,0,0,0)`, lotID); err != nil {
		t.Fatal(err)
	}

	postAsOf := policyStart.Add(2 * time.Hour)
	postEvent := SourceBatchEvent{EventID: policyStartUUID('8', 2122),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-start-window-post-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: postAsOf}
	postCycle := chain.commit(t, store, ctx, sourceID, "balances", policyStartUUID('9', 2122), postAsOf, []SourceBatchEvent{postEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "212", ExternalEventID: postEvent.EventID,
		CheckpointID: "reconcile-post-212", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(postCycle.cycleID),
		SnapshotRowCount: "1", AsOf: postAsOf, ObservedAt: postAsOf, StreamWatermarkAt: postAsOf,
		SourceCursor: "balance:212:post", SourceRevision: postEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: postCycle.batchID, ScanCycleID: postCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("post-policy baseline checkpoint should bootstrap POLICY_ANCHOR directly: %v", err)
	}

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !account.CutoverAt.Equal(policyStart.UTC()) {
		t.Fatalf("account.CutoverAt=%s, want %s", account.CutoverAt, policyStart)
	}
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, postAsOf.Add(time.Minute))
	if err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := projection.Lots[lotID]; !ok {
		t.Fatalf("in-window cash lot %s was not included in the projection: lots=%+v", lotID, projection.Lots)
	}
}

// TestPolicyStartBootstrapCheckpointExactlyAtPolicyStartReconcilesCleanly
// covers the required "checkpoint exactly at policy start" edge case: the
// real triggering checkpoint and the derived reconciliation checkpoint end
// up with an identical as_of (and, since both borrow the same source
// sequence, an identical evaluator tie-break key too) -- verified here to
// resolve to a consistent, correct final state (exactly one UNKNOWN_POSITIVE
// credit, both checkpoints reach a terminal matched/positive_classified_non_cash
// status, account active, no freeze) regardless of which of the two
// evaluates first, driven through the real evaluator via
// ProcessEligibilityProjectionJobs.
func TestPolicyStartBootstrapCheckpointExactlyAtPolicyStartReconcilesCleanly(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour).Truncate(time.Second)
	store, ctx, sourceID, accountID, manifestHash, configHash, chain := policyStartBootstrapFixture(t, 213, policyStart)

	asOf := policyStart
	event := SourceBatchEvent{EventID: policyStartUUID('8', 2132),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-start-exact-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: asOf}
	cycle := chain.commit(t, store, ctx, sourceID, "balances", policyStartUUID('9', 2132), asOf, []SourceBatchEvent{event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "213", ExternalEventID: event.EventID,
		CheckpointID: "reconcile-exact-213", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "300", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: asOf, ObservedAt: asOf, StreamWatermarkAt: asOf,
		SourceCursor: "balance:213:exact", SourceRevision: event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("checkpoint exactly at policy start should bootstrap POLICY_ANCHOR: %v", err)
	}

	worker := AuditActor{Type: "system", ID: "test-worker"}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, accountID, asOf.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}

	var creditCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM source_credit_events
		WHERE external_account_id=$1 AND credit_kind='UNKNOWN_POSITIVE'`, accountID).Scan(&creditCount); err != nil || creditCount != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d err=%v, want exactly 1", creditCount, err)
	}
	var creditUnits string
	if err := store.pool.QueryRow(ctx, `SELECT service_units::text FROM source_credit_events
		WHERE external_account_id=$1 AND credit_kind='UNKNOWN_POSITIVE'`, accountID).Scan(&creditUnits); err != nil || creditUnits != "300" {
		t.Fatalf("UNKNOWN_POSITIVE credit units=%q err=%v, want 300", creditUnits, err)
	}

	rows, err := store.pool.Query(ctx, `SELECT bce.evaluation_status FROM balance_checkpoint_evaluations bce
		JOIN balance_reconciliation_checkpoints c ON c.id=bce.checkpoint_id
		WHERE c.external_account_id=$1 ORDER BY bce.evaluation_status`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	statuses := make([]string, 0, 2)
	for rows.Next() {
		var status string
		if err = rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0] != "matched" || statuses[1] != "positive_classified_non_cash" {
		t.Fatalf("checkpoint evaluation statuses=%v, want exactly one matched and one positive_classified_non_cash", statuses)
	}

	if status := accountEligibilityStatus(t, store, ctx, accountID); status != "active" {
		t.Fatalf("account status=%q, want active", status)
	}
	var openFreezes int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&openFreezes); err != nil || openFreezes != 0 {
		t.Fatalf("open freezes=%d err=%v, want 0", openFreezes, err)
	}
}
