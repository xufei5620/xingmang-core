package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap covers
// the XM-INV-ANCHOR-BALANCE fix: a POLICY_ANCHOR account's own anchor
// (design XM-INV-POLICY-ANCHOR 2.1's bootstrap checkpoint) must be a trusted
// starting point for evaluatePendingBalanceEvidenceTx's positive-difference
// branch, exactly as a legacy account's checkpoint_kind='cutover' row
// already is -- not merely as buildEligibilityProjectionTx's lower window
// bound. Before this fix, a fresh POLICY_ANCHOR account's own anchor
// checkpoint had no seeded opening credit and no checkpoint_kind='cutover'
// row to ground the trust chain, so its very first balance evaluation (and
// every real checkpoint after it) opened a spurious SOURCE_GAP freeze -- see
// docs/handoffs/XM-INV-ANCHOR-BALANCE.md and production accounts
// 98cce4c8-a03c-4b55-9049-61b650db2d0e and 6706ea6a....
//
// This test drives the real bootstrap (ObserveBalanceCheckpoint), a real
// post-anchor credit, two more real reconciliation checkpoints, a real
// funding lot payment and a real usage fact -- all through
// ProcessEligibilityProjectionJobs, the same entrypoint production uses --
// and checks arithmetic precise enough to also prove the opening balance
// stays non-invoiceable: usage must drain the anchor's synthesized non-cash
// credit before ever touching the paid cash lot.
func TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000270"
	userID := "20000000-0000-4000-8000-000000000270"
	accountID := "30000000-0000-4000-8000-000000000270"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','anchor-balance-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','anchor-balance-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'270','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "payments", "usage", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000270",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("anchor-balance-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000101", cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("anchor-balance-manifest")
	configHash := testHash("anchor-balance-config")
	snapshotHash := testHash("anchor-balance-snapshot")
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

	// Bootstrap POLICY_ANCHOR: opening balance 500, entirely non-invoiceable
	// per design XM-INV-POLICY-ANCHOR 2.1.
	anchorAt := policyStart.Add(1 * time.Hour)
	anchorEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000271",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("anchor-balance-anchor-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: anchorAt}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000102", anchorAt, []SourceBatchEvent{anchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "270", ExternalEventID: anchorEvent.EventID,
		CheckpointID: "anchor-balance-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:270:anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", anchorCycle)
	var anchorCheckpointID string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id='anchor-balance-anchor'`, accountID).Scan(&anchorCheckpointID); err != nil {
		t.Fatal(err)
	}

	worker := AuditActor{Type: "system", ID: "test-worker"}
	queueJobThrough := func(through time.Time) {
		t.Helper()
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
			VALUES($1,$2,'queued',now())
			ON CONFLICT(external_account_id) DO UPDATE SET
				requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
				status='queued',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`,
			accountID, through); err != nil {
			t.Fatal(err)
		}
	}
	assertNoFreezes := func(step string) {
		t.Helper()
		var status string
		var freezeCount int
		if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
			WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "active" {
			t.Fatalf("%s: status=%q err=%v, want active", step, status, err)
		}
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
			WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&freezeCount); err != nil || freezeCount != 0 {
			t.Fatalf("%s: open freeze count=%d err=%v, want 0", step, freezeCount, err)
		}
	}

	// Step 1: evaluate the pending balance evidence. Before the
	// XM-INV-ANCHOR-BALANCE fix this opened a SOURCE_GAP freeze immediately
	// -- there was no checkpoint_kind='cutover' row and no prior matched
	// evaluation to ground the trust chain, only account.CutoverAt itself.
	//
	// XM-INV-ELIG-POLICY-START-ANCHOR (design XM-INV-ELIG-SIMPLIFY section
	// 3(D)): cutover_at is now the global policy start, not this checkpoint's
	// own as_of, and a second, derived reconciliation checkpoint exists at
	// as_of=policyStart (this fixture has no window facts between policyStart
	// and anchorAt, so its derived balance equals the same 500 unchanged).
	// That derived checkpoint -- not the real one at anchorAt -- is now the
	// account's first-ever balance evidence (earliest as_of): it self-heals
	// into the synthesized UNKNOWN_POSITIVE credit, dated at policyStart. The
	// real checkpoint at anchorAt is evaluated second (in the same batch) and
	// reconciles directly against that credit: matched, difference 0. Both
	// outcomes together are exactly what the pre-fix code could never reach
	// (no freeze, real forward progress) -- only which of the two rows carries
	// which terminal status changed from this slice.
	queueJobThrough(anchorAt.Add(time.Minute))
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("step1 processed=%d err=%v", processed, err)
	}
	assertNoFreezes("step1")
	var step1Status string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, anchorCheckpointID).Scan(&step1Status); err != nil {
		t.Fatal(err)
	}
	if step1Status != "matched" {
		t.Fatalf("real anchor checkpoint evaluation status=%q, want matched (reconciles against the derived checkpoint's own synthesized credit)", step1Status)
	}
	var derivedCheckpointID string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id=$2`, accountID, "policy-start:anchor-balance-anchor").Scan(&derivedCheckpointID); err != nil {
		t.Fatalf("derived reconciliation checkpoint row missing: %v", err)
	}
	var derivedStatus string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, derivedCheckpointID).Scan(&derivedStatus); err != nil {
		t.Fatal(err)
	}
	if derivedStatus != "positive_classified_non_cash" {
		t.Fatalf("derived checkpoint evaluation status=%q, want positive_classified_non_cash", derivedStatus)
	}
	var syntheticUnits string
	var syntheticAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT event_time,service_units::text FROM source_credit_events
		WHERE external_account_id=$1 AND credit_kind='UNKNOWN_POSITIVE'`, accountID).Scan(&syntheticAt, &syntheticUnits); err != nil {
		t.Fatalf("synthesized opening non-cash credit missing: %v", err)
	}
	if syntheticUnits != "500" || !syntheticAt.Equal(policyStart.UTC()) {
		t.Fatalf("synthesized opening credit units=%s at=%s, want 500 at policyStart=%s", syntheticUnits, syntheticAt, policyStart)
	}

	// Step 2: a real post-anchor credit, then a real checkpoint reflecting
	// it (500+80=580). Must be exactly `matched` -- the synthesized credit
	// from step 1 now sits inside buildEligibilityProjectionTx's window.
	creditAt := anchorAt.Add(10 * time.Minute)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000272",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("anchor-balance-credit-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: creditAt}
	creditCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"84000000-0000-4000-8000-000000000103", creditAt, []SourceBatchEvent{creditEvent})
	if err := store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: "270", ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "anchor-balance-credit", EventTime: creditAt, ObservedAt: creditAt,
		StreamWatermarkAt: creditAt, ServiceUnits: "80", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		SourceCursor: "credit:270:1", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", creditCycle)

	checkpoint2At := anchorAt.Add(20 * time.Minute)
	checkpoint2Event := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000273",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("anchor-balance-checkpoint2-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: checkpoint2At}
	checkpoint2Cycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000104", checkpoint2At, []SourceBatchEvent{checkpoint2Event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "270", ExternalEventID: checkpoint2Event.EventID,
		CheckpointID: "anchor-balance-checkpoint2", CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "580", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(checkpoint2Cycle.cycleID),
		SnapshotRowCount: "1", AsOf: checkpoint2At, ObservedAt: checkpoint2At, StreamWatermarkAt: checkpoint2At,
		SourceCursor: "balance:270:2", SourceRevision: checkpoint2Event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 2,
		BatchID: checkpoint2Cycle.batchID, ScanCycleID: checkpoint2Cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", checkpoint2Cycle)
	var checkpoint2ID string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id='anchor-balance-checkpoint2'`, accountID).Scan(&checkpoint2ID); err != nil {
		t.Fatal(err)
	}

	queueJobThrough(checkpoint2At.Add(time.Minute))
	processed, err = store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("step2 processed=%d err=%v", processed, err)
	}
	assertNoFreezes("step2")
	var step2Status, step2Difference string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, checkpoint2ID).Scan(&step2Status, &step2Difference); err != nil {
		t.Fatal(err)
	}
	if step2Status != "matched" || step2Difference != "0" {
		t.Fatalf("checkpoint2 evaluation status=%q difference=%s, want matched/0", step2Status, step2Difference)
	}

	// Step 3: a real WALLET_CASH payment (1000 units / 100000 minor) and
	// usage of 700 units. Usage must drain the 580 non-cash units (the
	// anchor's synthesized 500 plus the 80 credit) before spending any cash,
	// leaving only 120 units (12000 minor) consumed against the paid lot --
	// proving the opening balance stayed non-invoiceable. A third real
	// checkpoint (500+80-700+1000=880) must again evaluate as `matched`.
	paymentAt := anchorAt.Add(25 * time.Minute)
	paymentEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000274",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: testHash("anchor-balance-payment-event"),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: paymentAt}
	paymentCycle := chain.commit(t, store, ctx, sourceID, "payments",
		"84000000-0000-4000-8000-000000000105", paymentAt, []SourceBatchEvent{paymentEvent})
	payment, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "anchor-balance-payment", Currency: domain.CurrencyCNY, OriginalMinor: 100_000,
			CurrentCapMinor: 100_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: paymentEvent.PayloadHash, CompletedAt: paymentAt, ObservedAt: paymentAt},
		ExternalUserID: "270", EventKind: "payment", ExternalEventID: paymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: paymentAt, SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "1000", WalletUnitCode: "SUB2_BALANCE_1E8",
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, CausalDomain: "anchor-balance-ledger",
		CausalOrder: "1", SourceCursor: "payment:270", BatchID: paymentCycle.batchID,
		ScanCycleID: paymentCycle.cycleID, StreamWatermarkAt: paymentAt,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "payments", paymentCycle)

	usageAt := anchorAt.Add(30 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000275",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("anchor-balance-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: usageAt}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage",
		"84000000-0000-4000-8000-000000000106", usageAt, []SourceBatchEvent{usageEvent})
	if err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: "270", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "anchor-balance-usage", EventTime: usageAt, ObservedAt: usageAt,
		StreamWatermarkAt: usageAt, ServiceUnits: "700", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "anchor-balance-ledger", CausalOrder: "2",
		SourceCursor: "usage:270", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", usageCycle)

	checkpoint3At := anchorAt.Add(35 * time.Minute)
	checkpoint3Event := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000276",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("anchor-balance-checkpoint3-event"),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: checkpoint3At}
	checkpoint3Cycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000107", checkpoint3At, []SourceBatchEvent{checkpoint3Event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "270", ExternalEventID: checkpoint3Event.EventID,
		CheckpointID: "anchor-balance-checkpoint3", CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "880", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(checkpoint3Cycle.cycleID),
		SnapshotRowCount: "1", AsOf: checkpoint3At, ObservedAt: checkpoint3At, StreamWatermarkAt: checkpoint3At,
		SourceCursor: "balance:270:3", SourceRevision: checkpoint3Event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 3,
		BatchID: checkpoint3Cycle.batchID, ScanCycleID: checkpoint3Cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", checkpoint3Cycle)
	var checkpoint3ID string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id='anchor-balance-checkpoint3'`, accountID).Scan(&checkpoint3ID); err != nil {
		t.Fatal(err)
	}

	queueJobThrough(checkpoint3At.Add(time.Minute))
	processed, err = store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("step3 processed=%d err=%v", processed, err)
	}
	assertNoFreezes("step3")
	var step3Status, step3Difference string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, checkpoint3ID).Scan(&step3Status, &step3Difference); err != nil {
		t.Fatal(err)
	}
	if step3Status != "matched" || step3Difference != "0" {
		t.Fatalf("checkpoint3 evaluation status=%q difference=%s, want matched/0", step3Status, step3Difference)
	}
	lot, err := store.GetFundingLot(ctx, payment.Lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lot.ConsumedCashMinor != 12_000 {
		t.Fatalf("consumed_cash_minor=%d, want 12000 (only the 120 units of usage the 580 non-cash units could not absorb)",
			lot.ConsumedCashMinor)
	}
}

// TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap covers
// design XM-INV-POLICY-ANCHOR 2.1's own claim ("carry-forward proofs anchor
// at account.CutoverAt, which is now the policy checkpoint -- no change
// needed") for real: a POLICY_ANCHOR account's carry-forward proof (derived
// by ensureBalanceCarryForwardProofTx when a published balances cycle
// carries no real checkpoint of its own) must evaluate via the same trusted
// anchor as a real checkpoint does, not freeze SOURCE_GAP.
func TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000280"
	userID := "20000000-0000-4000-8000-000000000280"
	accountID := "30000000-0000-4000-8000-000000000280"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','anchor-carry-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','anchor-carry-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'280','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "usage", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000280",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("anchor-carry-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000201", cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("anchor-carry-manifest")
	configHash := testHash("anchor-carry-config")
	snapshotHash := testHash("anchor-carry-snapshot")
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

	anchorAt := policyStart.Add(1 * time.Hour)
	anchorEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000281",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("anchor-carry-anchor-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: anchorAt}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000202", anchorAt, []SourceBatchEvent{anchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "280", ExternalEventID: anchorEvent.EventID,
		CheckpointID: "anchor-carry-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:280:anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", anchorCycle)
	var anchorCheckpointID string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id='anchor-carry-anchor'`, accountID).Scan(&anchorCheckpointID); err != nil {
		t.Fatal(err)
	}

	// A net-zero credit/usage pair (+20/-20, mirroring
	// TestBalanceDeltaCarryForwardMatchedNetFactsFinalize's legacy fixture)
	// so the derived carry-forward balance (carried forward unchanged from
	// the anchor's own 500) matches ExpectedBalance exactly once the
	// synthesized opening credit is in place.
	creditAt := anchorAt.Add(10 * time.Minute)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000282",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("anchor-carry-credit-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: creditAt}
	creditCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"84000000-0000-4000-8000-000000000203", creditAt, []SourceBatchEvent{creditEvent})
	if err := store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: "280", ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "anchor-carry-credit", EventTime: creditAt, ObservedAt: creditAt,
		StreamWatermarkAt: creditAt, ServiceUnits: "20", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		SourceCursor: "credit:280:1", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", creditCycle)

	usageAt := anchorAt.Add(15 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000283",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("anchor-carry-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: usageAt}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage",
		"84000000-0000-4000-8000-000000000204", usageAt, []SourceBatchEvent{usageEvent})
	if err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: "280", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "anchor-carry-usage", EventTime: usageAt, ObservedAt: usageAt,
		StreamWatermarkAt: usageAt, ServiceUnits: "20", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "anchor-carry-ledger", CausalOrder: "2",
		SourceCursor: "usage:280", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", usageCycle)

	// An empty published "balances" cycle covering both facts' visibility,
	// with no real checkpoint of its own -- ensureBalanceCarryForwardProofTx
	// derives a balance_carry_forward_proofs row from it, carrying forward
	// the anchor's own 500 unchanged.
	emptyBalancesAt := anchorAt.Add(20 * time.Minute)
	emptyBalancesCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000205", emptyBalancesAt, nil)
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", emptyBalancesCycle)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, accountID, emptyBalancesAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}

	var status string
	var freezeCount int
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%q err=%v, want active", status, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&freezeCount); err != nil || freezeCount != 0 {
		t.Fatalf("open freeze count=%d err=%v, want 0", freezeCount, err)
	}
	// XM-INV-ELIG-POLICY-START-ANCHOR: as in
	// TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap above,
	// the *derived* reconciliation checkpoint (as_of=policyStart, not
	// anchorAt) is now the account's first-ever balance evidence and carries
	// the positive_classified_non_cash self-heal; the real anchor checkpoint
	// reconciles against that credit and is matched instead.
	var anchorStatus string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, anchorCheckpointID).Scan(&anchorStatus); err != nil || anchorStatus != "matched" {
		t.Fatalf("real anchor checkpoint evaluation status=%q err=%v, want matched", anchorStatus, err)
	}
	var derivedCheckpointID string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_id=$2`, accountID, "policy-start:anchor-carry-anchor").Scan(&derivedCheckpointID); err != nil {
		t.Fatalf("derived reconciliation checkpoint row missing: %v", err)
	}
	var derivedStatus string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, derivedCheckpointID).Scan(&derivedStatus); err != nil || derivedStatus != "positive_classified_non_cash" {
		t.Fatalf("derived checkpoint evaluation status=%q err=%v, want positive_classified_non_cash", derivedStatus, err)
	}
	var proofStatus, proofDifference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation.evaluation_status,evaluation.difference_service_units::text
		FROM balance_carry_forward_evaluations evaluation
		JOIN balance_carry_forward_proofs proof ON proof.id=evaluation.proof_id
		WHERE proof.external_account_id=$1`, accountID).Scan(&proofStatus, &proofDifference); err != nil {
		t.Fatalf("carry-forward proof evaluation missing: %v", err)
	}
	if proofStatus != "matched" || proofDifference != "0" {
		t.Fatalf("carry-forward proof evaluation status=%q difference=%s, want matched/0", proofStatus, proofDifference)
	}
}

// TestLegacyAccountBalanceCheckpointWithNoTrustAnchorStillFreezesSourceGap
// is a regression guard for XM-INV-ANCHOR-BALANCE's fix: the new
// POLICY_ANCHOR trust branch in evaluatePendingBalanceEvidenceTx's
// intervalStart query is scoped strictly to bootstrap_kind='POLICY_ANCHOR'.
// A legacy account is constructed here (direct SQL, since no production
// code path since XM-INV-POLICY-ANCHOR creates a fresh SIGNED_CUTOVER row
// without also seeding a checkpoint_kind='cutover' row -- see
// ObserveBalanceCheckpoint's cutover-kind bootstrap branch) that
// deliberately has no checkpoint_kind='cutover' row and no seeded opening
// credit -- i.e. genuinely no trust anchor at all. A positive balance
// difference on this account must still freeze SOURCE_GAP exactly as
// before this fix; the new branch must not leak into it.
func TestLegacyAccountBalanceCheckpointWithNoTrustAnchorStillFreezesSourceGap(t *testing.T) {
	store, ctx := integrationStore(t)
	const (
		sourceID  = "15000000-0000-4000-8000-000000000001"
		userID    = "25000000-0000-4000-8000-000000000001"
		accountID = "35000000-0000-4000-8000-000000000001"
	)
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	manifestHash := testHash("no-trust-anchor-manifest")
	configHash := testHash("no-trust-anchor-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','no-trust-anchor-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','no-trust-anchor-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'no-trust-anchor','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutover,
		configHash, testHash("no-trust-anchor-baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900)`,
		accountID, sourceID, cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	checkpointAt := cutover.Add(10 * time.Minute)
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
		as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,
		configuration_hash,reconciliation_status,source_sequence,source_cursor,
		stream_watermark_at,source_revision_hash,observed_at)
		VALUES('45000000-0000-4000-8000-000000000001',$1,$2,'no-trust-anchor-checkpoint-event',
		'no-trust-anchor-checkpoint','reconciliation',FALSE,$3,1,$4,50,FALSE,'SUB2_BALANCE_1E8',
		$5,$6,'cutover_baseline',1,'no-trust-anchor:1',$4,$7,$4)`, sourceID, accountID,
		testHash("no-trust-anchor-snapshot"), checkpointAt, manifestHash, configHash,
		testHash("no-trust-anchor-revision")); err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, checkpointAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id='45000000-0000-4000-8000-000000000001'`).Scan(&status); err != nil || status != "source_gap_frozen" {
		t.Fatalf("legacy account with no trust anchor status=%q err=%v, want source_gap_frozen", status, err)
	}
	var freezeCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND freeze_reason='SOURCE_GAP' AND status='open'`,
		accountID).Scan(&freezeCount); err != nil || freezeCount != 1 {
		t.Fatalf("legacy no-trust-anchor freeze count=%d err=%v", freezeCount, err)
	}
}
