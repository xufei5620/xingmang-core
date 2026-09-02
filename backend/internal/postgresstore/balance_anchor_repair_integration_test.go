package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// balanceAnchorRepairFixture builds the exact mixed shape the 2026-09-02
// XM-INV-ANCHOR-BALANCE incident left behind, entirely through DB state
// (not through evaluatePendingBalanceEvidenceTx, which no longer produces
// these freezes after this slice's fix -- see
// docs/handoffs/XM-INV-ANCHOR-BALANCE.md): a POLICY_ANCHOR account "A" with
// two open balance_checkpoint SOURCE_GAP freezes and one open
// balance_carry_forward_proof SOURCE_GAP freeze, all resolved and
// reactivated by the repair; a POLICY_ANCHOR account "B" with one open
// balance_checkpoint SOURCE_GAP freeze the repair resolves plus one
// unrelated USAGE_EXCEEDS_LEDGER freeze the repair must never touch, so B
// stays frozen even after its SOURCE_GAP freeze clears; and a legacy
// (SIGNED_CUTOVER) account "C" with a real balance_checkpoint SOURCE_GAP
// freeze that must never be touched by this repair at all.
type balanceAnchorRepairFixture struct {
	store         *Store
	ctx           context.Context
	acctA         string
	acctB         string
	acctC         string
	ckptA1        checkpointFreezePair // account A's own anchor checkpoint
	ckptA2        checkpointFreezePair
	proofA        proofFreezePair
	ckptB1        checkpointFreezePair
	ledgerFreezeB string
	ckptC1        checkpointFreezePair
}

type checkpointFreezePair struct {
	checkpointID string // internal balance_reconciliation_checkpoints.id
	businessKey  string // checkpoint_id, the freeze's trigger_object_id
	freezeID     string
}

type proofFreezePair struct {
	proofID  string // internal balance_carry_forward_proofs.id
	proofKey string // the freeze's trigger_object_id
	freezeID string
}

// bootstrapBalanceAnchorAccount creates a fresh POLICY_ANCHOR account
// (real ObserveBalanceCheckpoint bootstrap, so every migration-0016
// contract trigger is satisfied exactly as production requires) and
// returns its ids/hashes plus the anchor checkpoint's own row -- this
// anchor checkpoint is itself one of the "stuck" checkpoints the fixture
// marks source_gap_frozen below, matching production exactly (the
// account's own anchor is typically the first checkpoint the bug hit).
func bootstrapBalanceAnchorAccount(t *testing.T, store *Store, ctx context.Context, policyStart time.Time, suffix string) (sourceID, userID, accountID, manifestHash, configHash string, anchorAt time.Time, chain *v3TestChain, anchor checkpointFreezePair) {
	t.Helper()
	sourceID = "10000000-0000-4000-8000-000000000" + suffix
	userID = "20000000-0000-4000-8000-000000000" + suffix
	accountID = "30000000-0000-4000-8000-000000000" + suffix
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','anchor-repair-test-`+suffix+`','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','anchor-repair-user-`+suffix+`')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'`+suffix+`','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "usage", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain = newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000" + suffix,
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("anchor-repair-manifest-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000001"+suffix, cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash = testHash("anchor-repair-manifest-" + suffix)
	configHash = testHash("anchor-repair-config-" + suffix)
	snapshotHash := testHash("anchor-repair-snapshot-" + suffix)
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

	anchorAt = policyStart.Add(1 * time.Hour)
	anchorEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000001" + suffix,
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("anchor-repair-anchor-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: anchorAt}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000002"+suffix, anchorAt, []SourceBatchEvent{anchorEvent})
	anchorCheckpointID := "anchor-repair-anchor-" + suffix
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: suffix, ExternalEventID: anchorEvent.EventID,
		CheckpointID: anchorCheckpointID, CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:" + suffix + ":anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", anchorCycle)
	var anchorID, anchorRevision string
	if err := store.pool.QueryRow(ctx, `SELECT id,source_revision_hash FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id=$2`, accountID, anchorCheckpointID).Scan(&anchorID, &anchorRevision); err != nil {
		t.Fatal(err)
	}
	anchor = checkpointFreezePair{checkpointID: anchorID, businessKey: anchorCheckpointID}
	return sourceID, userID, accountID, manifestHash, configHash, anchorAt, chain, anchor
}

// stuckCheckpoint inserts an additional real reconciliation checkpoint
// (via ObserveBalanceCheckpoint, so it is a fully valid row) and marks it
// "stuck" exactly as the pre-fix bug left it: a balance_checkpoint_evaluations
// row with evaluation_status='source_gap_frozen' (this table carries no
// immutability trigger, unlike balance_carry_forward_evaluations, which is
// exactly why the repair can reset it) plus an open SOURCE_GAP freeze on
// the checkpoint's own business key -- the identical shape
// evaluatePendingBalanceEvidenceTx's own freezeEligibilityTx call produces.
func stuckCheckpoint(t *testing.T, store *Store, ctx context.Context, chain *v3TestChain, sourceID, userID, accountID, manifestHash, configHash, suffix string, at time.Time, balance string) checkpointFreezePair {
	t.Helper()
	// The event/business-key suffix (uniquifying IDs across accounts,
	// picked by the caller) is independent of the account's own
	// external_user_id -- resolveExternalAccountTx keys purely off that
	// stored value, so it must be read back rather than reused from suffix.
	externalUserID := mustExternalUserID(t, store, ctx, accountID)
	event := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000003" + suffix,
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("anchor-repair-stuck-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: at}
	cycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000003"+suffix, at, []SourceBatchEvent{event})
	businessKey := "anchor-repair-stuck-" + suffix
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: externalUserID, ExternalEventID: event.EventID,
		CheckpointID: businessKey, CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: balance, UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: at, ObservedAt: at, StreamWatermarkAt: at,
		SourceCursor: "balance:" + suffix + ":stuck", SourceRevision: event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 2,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", cycle)
	var checkpointID, revision string
	if err := store.pool.QueryRow(ctx, `SELECT id,source_revision_hash FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id=$2`, accountID, businessKey).Scan(&checkpointID, &revision); err != nil {
		t.Fatal(err)
	}
	return markCheckpointStuck(t, store, ctx, accountID, checkpointID, businessKey, revision, balance)
}

// markCheckpointStuck inserts the source_gap_frozen evaluation row and the
// open SOURCE_GAP freeze for an already-existing checkpoint row.
func markCheckpointStuck(t *testing.T, store *Store, ctx context.Context, accountID, checkpointID, businessKey, revision, balance string) checkpointFreezePair {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_checkpoint_evaluations(
		id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,1,0,$3::numeric,'source_gap_frozen')`, randomUUID(), checkpointID, balance); err != nil {
		t.Fatal(err)
	}
	freezeID := randomUUID()
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'SOURCE_GAP','balance_checkpoint',$3,$4)`,
		freezeID, accountID, businessKey, revision); err != nil {
		t.Fatal(err)
	}
	return checkpointFreezePair{checkpointID: checkpointID, businessKey: businessKey, freezeID: freezeID}
}

// stuckCarryForwardProof derives one real balance_carry_forward_proofs row
// (via ensureBalanceCarryForwardProofTx directly, so it is a fully valid
// row satisfying migration 0014's contract-guard trigger) from a net-zero
// credit/usage pair covered by an empty published balances cycle -- the
// same construction TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap
// uses, minus the (now-fixed) evaluation step -- and marks it stuck exactly
// as the pre-fix bug left it. balance_carry_forward_evaluations IS
// immutable (migration 0014), so unlike stuckCheckpoint this leaves no
// evaluation row for the repair to reset -- see
// BalanceAnchorRepairAccount's doc comment for why that is still safe.
func stuckCarryForwardProof(t *testing.T, store *Store, ctx context.Context, chain *v3TestChain, sourceID, userID, accountID, manifestHash, configHash, suffix string, after time.Time) proofFreezePair {
	t.Helper()
	// See stuckCheckpoint's comment: the account's own external_user_id
	// must be read back, not derived from the event-uniquifying suffix.
	externalUserID := mustExternalUserID(t, store, ctx, accountID)
	creditAt := after.Add(10 * time.Minute)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000004" + suffix,
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("anchor-repair-proof-credit-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: creditAt}
	creditCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"84000000-0000-4000-8000-000000004"+suffix, creditAt, []SourceBatchEvent{creditEvent})
	if err := store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: externalUserID, ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "anchor-repair-proof-credit-" + suffix, EventTime: creditAt, ObservedAt: creditAt,
		StreamWatermarkAt: creditAt, ServiceUnits: "20", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		SourceCursor: "credit:" + suffix + ":proof", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", creditCycle)

	usageAt := after.Add(15 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000005" + suffix,
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("anchor-repair-proof-usage-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: usageAt}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage",
		"84000000-0000-4000-8000-000000005"+suffix, usageAt, []SourceBatchEvent{usageEvent})
	if err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: externalUserID, ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "anchor-repair-proof-usage-" + suffix, EventTime: usageAt, ObservedAt: usageAt,
		StreamWatermarkAt: usageAt, ServiceUnits: "20", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "anchor-repair-proof-" + suffix, CausalOrder: "2",
		SourceCursor: "usage:" + suffix + ":proof", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", usageCycle)

	emptyBalancesAt := after.Add(20 * time.Minute)
	emptyBalancesCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000006"+suffix, emptyBalancesAt, nil)
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", emptyBalancesCycle)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBalanceCarryForwardProofTx(ctx, tx, account, emptyBalancesAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var proofID, proofKey, revision string
	if err := store.pool.QueryRow(ctx, `SELECT id,proof_key,source_revision_hash
		FROM balance_carry_forward_proofs WHERE external_account_id=$1`, accountID).Scan(&proofID, &proofKey, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_carry_forward_evaluations(
		id,proof_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,1,480,20,'source_gap_frozen')`, randomUUID(), proofID); err != nil {
		t.Fatal(err)
	}
	freezeID := randomUUID()
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'SOURCE_GAP','balance_carry_forward_proof',$3,$4)`,
		freezeID, accountID, proofKey, revision); err != nil {
		t.Fatal(err)
	}
	return proofFreezePair{proofID: proofID, proofKey: proofKey, freezeID: freezeID}
}

func mustExternalUserID(t *testing.T, store *Store, ctx context.Context, accountID string) string {
	t.Helper()
	var externalUserID string
	if err := store.pool.QueryRow(ctx, `SELECT external_user_id FROM external_accounts WHERE id=$1`,
		accountID).Scan(&externalUserID); err != nil {
		t.Fatal(err)
	}
	return externalUserID
}

func newBalanceAnchorRepairFixture(t *testing.T) balanceAnchorRepairFixture {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-4 * time.Hour)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)

	// Account A: POLICY_ANCHOR, "many" stuck checkpoint freezes (its own
	// anchor plus one more) and one stuck carry-forward proof freeze --
	// production shape (accounts 98cce4c8.../6706ea6a...) had dozens of
	// each; two of each here exercises the same loop-based mechanism at
	// test scale. Every one of these must resolve and the account must
	// reactivate (no other open freeze remains).
	sourceA, userA, acctA, manifestA, configA, anchorAtA, chainA, anchorPairA :=
		bootstrapBalanceAnchorAccount(t, store, ctx, policyStart, "090")
	ckptA1 := markCheckpointStuck(t, store, ctx, acctA, anchorPairA.checkpointID, anchorPairA.businessKey,
		mustCheckpointRevision(t, store, ctx, anchorPairA.checkpointID), "500")
	ckptA2 := stuckCheckpoint(t, store, ctx, chainA, sourceA, userA, acctA, manifestA, configA, "091",
		anchorAtA.Add(10*time.Minute), "620")
	proofA := stuckCarryForwardProof(t, store, ctx, chainA, sourceA, userA, acctA, manifestA, configA, "092",
		anchorAtA.Add(30*time.Minute))
	// In production, finalized_through keeps advancing to the job's own
	// requested_through even when evaluatePendingBalanceEvidenceTx freezes
	// something (processEligibilityProjectionJob's last step is
	// unconditional) -- every one of the repeated failing attempts that
	// produced this incident's freezes still advanced the boundary past
	// each stuck checkpoint/proof's own as_of. This fixture built the stuck
	// rows directly rather than through that failing loop, so advance
	// finalized_through by hand to match: past ckptA2 and proofA's own
	// as_of, so the repair's post-reactivation job actually re-evaluates
	// both reset checkpoints in one pass (checked below).
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$2,eligibility_status='frozen' WHERE external_account_id=$1`,
		acctA, anchorAtA.Add(51*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Account B: POLICY_ANCHOR, one stuck checkpoint freeze (repaired) plus
	// one unrelated, genuinely-open USAGE_EXCEEDS_LEDGER freeze the repair
	// must never resolve -- B must stay frozen after the repair even though
	// its SOURCE_GAP freeze clears, because another open freeze remains.
	_, _, acctB, _, _, _, _, anchorPairB :=
		bootstrapBalanceAnchorAccount(t, store, ctx, policyStart, "093")
	ckptB1 := markCheckpointStuck(t, store, ctx, acctB, anchorPairB.checkpointID, anchorPairB.businessKey,
		mustCheckpointRevision(t, store, ctx, anchorPairB.checkpointID), "500")
	ledgerFreezeB := randomUUID()
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'USAGE_EXCEEDS_LEDGER','usage','unrelated-usage-fact-093',$3)`,
		ledgerFreezeB, acctB, testHash("unrelated-093")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET eligibility_status='frozen' WHERE external_account_id=$1`, acctB); err != nil {
		t.Fatal(err)
	}

	// Account C: legacy (SIGNED_CUTOVER), constructed directly (no
	// production code path creates a fresh SIGNED_CUTOVER row after design
	// XM-INV-POLICY-ANCHOR shipped) with a real, unrelated balance_checkpoint
	// SOURCE_GAP freeze -- proves the repair's bootstrap_kind='POLICY_ANCHOR'
	// scoping, not merely its trigger_object_type filter, is what excludes
	// legacy accounts.
	sourceC := "10000000-0000-4000-8000-000000000094"
	userC := "20000000-0000-4000-8000-000000000094"
	acctC := "30000000-0000-4000-8000-000000000094"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','anchor-repair-legacy','v3-test')`, sourceC); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','anchor-repair-legacy-user')`, userC); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'094','test','verified')`, acctC, userC, sourceC); err != nil {
		t.Fatal(err)
	}
	legacyCutover := policyStart.Add(-10 * time.Hour)
	legacyManifestHash := testHash("anchor-repair-legacy-manifest")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceC, legacyManifestHash, legacyCutover,
		testHash("anchor-repair-legacy-config"), testHash("anchor-repair-legacy-baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,eligibility_status)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900,'frozen')`,
		acctC, sourceC, legacyCutover, legacyManifestHash); err != nil {
		t.Fatal(err)
	}
	legacyCheckpointID := randomUUID()
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,as_of,balance_service_units,balance_negative,unit_code,
		cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
		source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,'anchor-repair-legacy-checkpoint-event','anchor-repair-legacy-checkpoint',
		'reconciliation',$4,50,FALSE,'SUB2_BALANCE_1E8',$5,$6,'pending_finalization',1,
		'anchor-repair-legacy:1',$4,$7,$4)`, legacyCheckpointID, sourceC, acctC, legacyCutover.Add(time.Hour),
		legacyManifestHash, testHash("anchor-repair-legacy-config"),
		testHash("anchor-repair-legacy-checkpoint-revision")); err != nil {
		t.Fatal(err)
	}
	ckptC1 := markCheckpointStuck(t, store, ctx, acctC, legacyCheckpointID, "anchor-repair-legacy-checkpoint",
		testHash("anchor-repair-legacy-checkpoint-revision"), "50")

	return balanceAnchorRepairFixture{store: store, ctx: ctx, acctA: acctA, acctB: acctB, acctC: acctC,
		ckptA1: ckptA1, ckptA2: ckptA2, proofA: proofA, ckptB1: ckptB1, ledgerFreezeB: ledgerFreezeB, ckptC1: ckptC1}
}

func mustCheckpointRevision(t *testing.T, store *Store, ctx context.Context, checkpointID string) string {
	t.Helper()
	var revision string
	if err := store.pool.QueryRow(ctx, `SELECT source_revision_hash FROM balance_reconciliation_checkpoints
		WHERE id=$1`, checkpointID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func (f balanceAnchorRepairFixture) freezeStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT status FROM eligibility_freezes WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (f balanceAnchorRepairFixture) accountEligibilityStatus(t *testing.T, accountID string) string {
	t.Helper()
	var status string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (f balanceAnchorRepairFixture) checkpointEvaluationCount(t *testing.T, checkpointID string) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, checkpointID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func fixedBalanceAnchorRepairEvidence() BalanceAnchorRepairInput {
	return BalanceAnchorRepairInput{
		Apply: true, OperatorID: "70000000-0000-4000-8000-000000000098",
		NoteCiphertext: bytes.Repeat([]byte{1}, 32), NoteHash: testHash("balance-anchor-repair-note"),
		EvidenceCiphertext: bytes.Repeat([]byte{2}, 32), EvidenceHash: testHash("balance-anchor-repair-evidence"),
	}
}

// TestRepairBalanceAnchorEligibilityDryRunReportsWithoutMutating verifies
// the dry-run path (design XM-INV-ANCHOR-BALANCE, default mode): it must
// report exactly what apply would do -- including the checkpoint-evaluation
// reset count -- while leaving every row untouched.
func TestRepairBalanceAnchorEligibilityDryRunReportsWithoutMutating(t *testing.T) {
	f := newBalanceAnchorRepairFixture(t)
	result, err := f.store.RepairBalanceAnchorEligibility(f.ctx, BalanceAnchorRepairInput{Apply: false},
		AuditActor{Type: "system", ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("dry run reported Applied=true")
	}
	if result.TotalSourceGapFreezesResolved != 4 || result.TotalCheckpointEvaluationsReset != 3 {
		t.Fatalf("dry run totals=%+v, want 4 freezes (2 A-checkpoint+1 A-proof+1 B-checkpoint)/3 checkpoint resets", result)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("dry run accounts=%+v, want exactly A and B (never legacy C)", result.Accounts)
	}
	byAccount := map[string]BalanceAnchorRepairAccount{}
	for _, acct := range result.Accounts {
		byAccount[acct.ExternalAccountID] = acct
	}
	if acctA, ok := byAccount[f.acctA]; !ok || acctA.SourceGapFreezesResolved != 3 || acctA.CheckpointEvaluationsReset != 2 || acctA.Reactivated {
		t.Fatalf("dry run account A summary=%+v ok=%v", acctA, ok)
	}
	if acctB, ok := byAccount[f.acctB]; !ok || acctB.SourceGapFreezesResolved != 1 || acctB.CheckpointEvaluationsReset != 1 || acctB.Reactivated {
		t.Fatalf("dry run account B summary=%+v ok=%v", acctB, ok)
	}
	// Nothing was actually mutated.
	for _, id := range []string{f.ckptA1.freezeID, f.ckptA2.freezeID, f.proofA.freezeID, f.ckptB1.freezeID, f.ledgerFreezeB, f.ckptC1.freezeID} {
		if status := f.freezeStatus(t, id); status != "open" {
			t.Fatalf("dry run mutated freeze %s to status=%s", id, status)
		}
	}
	if count := f.checkpointEvaluationCount(t, f.ckptA1.checkpointID); count != 1 {
		t.Fatalf("dry run deleted checkpoint A1's evaluation row: count=%d", count)
	}
	if status := f.accountEligibilityStatus(t, f.acctA); status != "frozen" {
		t.Fatalf("dry run reactivated account A: status=%s", status)
	}
}

// TestRepairBalanceAnchorEligibilityApplyResolvesResetsAndReactivates is the
// apply-mode happy path plus the "stays frozen" and "never touches legacy"
// safety properties in one run.
func TestRepairBalanceAnchorEligibilityApplyResolvesResetsAndReactivates(t *testing.T) {
	f := newBalanceAnchorRepairFixture(t)
	result, err := f.store.RepairBalanceAnchorEligibility(f.ctx, fixedBalanceAnchorRepairEvidence(),
		AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000098", Reason: "test repair"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.TotalSourceGapFreezesResolved != 4 || result.TotalCheckpointEvaluationsReset != 3 {
		t.Fatalf("apply totals=%+v", result)
	}

	// Account A: every freeze resolved, every checkpoint evaluation reset
	// (the proof's evaluation is left in place -- immutable), reactivated.
	for _, id := range []string{f.ckptA1.freezeID, f.ckptA2.freezeID, f.proofA.freezeID} {
		if status := f.freezeStatus(t, id); status != "resolved" {
			t.Fatalf("account A freeze %s status=%s, want resolved", id, status)
		}
	}
	if count := f.checkpointEvaluationCount(t, f.ckptA1.checkpointID); count != 0 {
		t.Fatalf("account A checkpoint 1 evaluation not reset: count=%d", count)
	}
	if count := f.checkpointEvaluationCount(t, f.ckptA2.checkpointID); count != 0 {
		t.Fatalf("account A checkpoint 2 evaluation not reset: count=%d", count)
	}
	var proofEvalCount int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM balance_carry_forward_evaluations
		WHERE proof_id=$1`, f.proofA.proofID).Scan(&proofEvalCount); err != nil || proofEvalCount != 1 {
		t.Fatalf("account A proof evaluation unexpectedly removed: count=%d err=%v", proofEvalCount, err)
	}
	if status := f.accountEligibilityStatus(t, f.acctA); status != "active" {
		t.Fatalf("account A eligibility_status=%s, want active", status)
	}

	// Account B: its SOURCE_GAP freeze resolves and its evaluation resets,
	// but the unrelated USAGE_EXCEEDS_LEDGER freeze is never touched, so B
	// must stay frozen.
	if status := f.freezeStatus(t, f.ckptB1.freezeID); status != "resolved" {
		t.Fatalf("account B checkpoint freeze status=%s, want resolved", status)
	}
	if count := f.checkpointEvaluationCount(t, f.ckptB1.checkpointID); count != 0 {
		t.Fatalf("account B checkpoint evaluation not reset: count=%d", count)
	}
	if status := f.freezeStatus(t, f.ledgerFreezeB); status != "open" {
		t.Fatalf("unrelated USAGE_EXCEEDS_LEDGER freeze was touched: status=%s", status)
	}
	if status := f.accountEligibilityStatus(t, f.acctB); status != "frozen" {
		t.Fatalf("account B eligibility_status=%s, want frozen (unrelated freeze still open)", status)
	}

	// Account C (legacy): completely untouched.
	if status := f.freezeStatus(t, f.ckptC1.freezeID); status != "open" {
		t.Fatalf("legacy account's real SOURCE_GAP freeze was touched: status=%s", status)
	}
	if count := f.checkpointEvaluationCount(t, f.ckptC1.checkpointID); count != 1 {
		t.Fatalf("legacy account's checkpoint evaluation was touched: count=%d", count)
	}
	if status := f.accountEligibilityStatus(t, f.acctC); status != "frozen" {
		t.Fatalf("legacy account was reactivated: status=%s", status)
	}

	var freezeAudits int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.freeze.resolved' AND object_id IN ($1,$2,$3,$4)`,
		f.ckptA1.freezeID, f.ckptA2.freezeID, f.proofA.freezeID, f.ckptB1.freezeID).Scan(&freezeAudits); err != nil || freezeAudits != 4 {
		t.Fatalf("freeze resolution audit rows=%d err=%v", freezeAudits, err)
	}
	var summaryAudits int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.balance_anchor_repaired' AND object_id=$1`,
		f.acctA).Scan(&summaryAudits); err != nil || summaryAudits != 1 {
		t.Fatalf("account A summary audit rows=%d err=%v", summaryAudits, err)
	}
	// Account B gets no summary audit row: mirroring
	// RepairPreAnchorUsageEligibility's own precedent, the per-account
	// summary is written only when the account's last open freeze actually
	// clears and it reactivates, not merely because this repair resolved
	// some of its freezes.
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.balance_anchor_repaired' AND object_id=$1`,
		f.acctB).Scan(&summaryAudits); err != nil || summaryAudits != 0 {
		t.Fatalf("account B summary audit rows=%d err=%v, want 0 (only reactivated accounts get one)", summaryAudits, err)
	}

	// A second apply run must find nothing left to do -- idempotent.
	second, err := f.store.RepairBalanceAnchorEligibility(f.ctx, fixedBalanceAnchorRepairEvidence(),
		AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000098", Reason: "test repair rerun"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Accounts) != 0 || second.TotalSourceGapFreezesResolved != 0 || second.TotalCheckpointEvaluationsReset != 0 {
		t.Fatalf("second apply run found leftover work=%+v", second)
	}

	// The repair's own reactivation queued a fresh eligibility_projection_jobs
	// row for account A (mirroring RepairPreAnchorUsageEligibility's
	// reactivation logic). Confirm that job actually completes cleanly
	// under the fixed code -- both reset checkpoints (the account's own
	// anchor and the second one) must re-evaluate without opening a new
	// freeze, proving the repair does not merely flip a status flag but
	// leaves the account able to make real forward progress.
	processed, err := f.store.ProcessEligibilityProjectionJobs(f.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "post-repair-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("post-repair projection processed=%d err=%v", processed, err)
	}
	if status := f.accountEligibilityStatus(t, f.acctA); status != "active" {
		t.Fatalf("account A eligibility_status=%s after post-repair projection, want active", status)
	}
	var postRepairFreezes int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, f.acctA).Scan(&postRepairFreezes); err != nil || postRepairFreezes != 0 {
		t.Fatalf("account A open freezes after post-repair projection=%d err=%v, want 0", postRepairFreezes, err)
	}
	for _, checkpointID := range []string{f.ckptA1.checkpointID, f.ckptA2.checkpointID} {
		var status string
		if err := f.store.pool.QueryRow(f.ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
			WHERE checkpoint_id=$1`, checkpointID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "matched" && status != "positive_classified_non_cash" {
			t.Fatalf("checkpoint %s re-evaluated as %q, want matched or positive_classified_non_cash", checkpointID, status)
		}
	}
}

// TestRepairBalanceAnchorEligibilityApplyRequiresOperatorAndEvidence checks
// the guard against an incomplete apply invocation.
func TestRepairBalanceAnchorEligibilityApplyRequiresOperatorAndEvidence(t *testing.T) {
	f := newBalanceAnchorRepairFixture(t)
	if _, err := f.store.RepairBalanceAnchorEligibility(f.ctx,
		BalanceAnchorRepairInput{Apply: true}, AuditActor{Type: "admin", ID: "test"}); err == nil {
		t.Fatal("apply without operator id or evidence was accepted")
	}
	if status := f.freezeStatus(t, f.ckptA1.freezeID); status != "open" {
		t.Fatalf("rejected apply still mutated a freeze: status=%s", status)
	}
}
