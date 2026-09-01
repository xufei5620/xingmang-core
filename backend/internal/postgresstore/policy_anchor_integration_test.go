package postgresstore

import (
	"bytes"
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy
// covers design XM-INV-POLICY-ANCHOR 2.1 in full for a baseline member with
// no eligibility state: a reconciliation checkpoint dated before the invoice
// policy start is ignored (acknowledged, audited) instead of parking on the
// signed cutover row -- it carries no eligibility-relevant information for
// an account that cannot become invoice-eligible before the policy start
// regardless of which row eventually establishes its cutover boundary. A
// later checkpoint at/after the policy start bootstraps the account directly
// (bootstrap_kind='POLICY_ANCHOR', migration 0016's trigger validates the
// cutover_at/cutover_balance_units/unit_code match against the anchoring
// checkpoint row), and the account functions normally afterward.
func TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	// Leave headroom after policyStart: migration 0016's POLICY_ANCHOR
	// validation rejects a cutover_at in the future, and postAsOf below is
	// policyStart+1h.
	policyStart := fixtureNow.Add(-2 * time.Hour)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000210"
	userID := "20000000-0000-4000-8000-000000000210"
	accountID := "30000000-0000-4000-8000-000000000210"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-anchor-ignore-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','policy-anchor-ignore-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'210','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}

	// The manifest's own cutover must predate the policy start -- an
	// application-level check (RegisterCutoverManifest) and a DB trigger both
	// enforce this as an invariant for every non-fixture manifest in
	// production, matching design 2.1's stated precondition.
	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000210",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("policy-anchor-ignore-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000001", cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("policy-anchor-ignore-manifest")
	configHash := testHash("policy-anchor-ignore-config")
	snapshotHash := testHash("policy-anchor-ignore-snapshot")
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

	// A reconciliation checkpoint dated before the policy start is ignored
	// for an account with no eligibility state: it carries no
	// eligibility-relevant information regardless of which row eventually
	// establishes the account's cutover boundary.
	preAsOf := policyStart.Add(-1 * time.Hour)
	preEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000211",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-anchor-ignore-pre-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: preAsOf}
	preCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000002", preAsOf, []SourceBatchEvent{preEvent})
	err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "210", ExternalEventID: preEvent.EventID,
		CheckpointID: "reconcile-pre-210", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(preCycle.cycleID),
		SnapshotRowCount: "1", AsOf: preAsOf, ObservedAt: preAsOf, StreamWatermarkAt: preAsOf,
		SourceCursor: "balance:210:pre", SourceRevision: preEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: preCycle.batchID, ScanCycleID: preCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatalf("pre-policy reconciliation checkpoint was not ignored: %v", err)
	}
	var stateCount int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("pre-policy checkpoint must not bootstrap eligibility state: count=%d err=%v", stateCount, err)
	}
	var skipAudits int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.pre_policy_checkpoint.skipped' AND object_id=$1`,
		accountID).Scan(&skipAudits); err != nil || skipAudits != 1 {
		t.Fatalf("pre-policy checkpoint skip audit missing: count=%d err=%v", skipAudits, err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", preCycle)

	// A reconciliation checkpoint at/after the policy start bootstraps the
	// account directly via POLICY_ANCHOR (migration 0016's deferred
	// constraint trigger validates it against this checkpoint row).
	postAsOf := policyStart.Add(1 * time.Hour)
	postEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000212",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-anchor-ignore-post-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: postAsOf}
	postCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000003", postAsOf, []SourceBatchEvent{postEvent})
	err = store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "210", ExternalEventID: postEvent.EventID,
		CheckpointID: "reconcile-post-210", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(postCycle.cycleID),
		SnapshotRowCount: "1", AsOf: postAsOf, ObservedAt: postAsOf, StreamWatermarkAt: postAsOf,
		SourceCursor: "balance:210:post", SourceRevision: postEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: postCycle.batchID, ScanCycleID: postCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatalf("post-policy baseline checkpoint should bootstrap POLICY_ANCHOR directly: %v", err)
	}
	var bootstrapKind string
	var storedCutoverAt time.Time
	var cutoverBalance string
	if err = store.pool.QueryRow(ctx, `SELECT bootstrap_kind,cutover_at,cutover_balance_units::text
		FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(
		&bootstrapKind, &storedCutoverAt, &cutoverBalance); err != nil {
		t.Fatal(err)
	}
	if bootstrapKind != "POLICY_ANCHOR" || !storedCutoverAt.Equal(postAsOf.UTC()) || cutoverBalance != "500" {
		t.Fatalf("policy anchor bootstrap kind=%s cutover_at=%s balance=%s", bootstrapKind, storedCutoverAt, cutoverBalance)
	}
	var checkpointKind, reconciliationStatus string
	var baselineMemberFlag bool
	if err = store.pool.QueryRow(ctx, `SELECT checkpoint_kind,baseline_member,reconciliation_status
		FROM balance_reconciliation_checkpoints WHERE external_account_id=$1 AND checkpoint_id='reconcile-post-210'`,
		accountID).Scan(&checkpointKind, &baselineMemberFlag, &reconciliationStatus); err != nil {
		t.Fatal(err)
	}
	if checkpointKind != "reconciliation" || !baselineMemberFlag || reconciliationStatus != "cutover_baseline" {
		t.Fatalf("policy anchor checkpoint kind=%s baseline_member=%t status=%s", checkpointKind, baselineMemberFlag, reconciliationStatus)
	}
	var bootstrapAudits int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.bootstrapped' AND object_id=$1`,
		accountID).Scan(&bootstrapAudits); err != nil || bootstrapAudits != 1 {
		t.Fatalf("policy anchor bootstrap audit missing: count=%d err=%v", bootstrapAudits, err)
	}

	// Design 2.1 bullet 4: the account must function normally afterward with
	// only post-anchor facts -- exercise a normal usage/credit projection
	// through this new POLICY_ANCHOR account.
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", postCycle)
	if err := store.ProvisionSourceStream(ctx, sourceID, "credits", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	creditAt := postAsOf.Add(1 * time.Hour)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000213",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("policy-anchor-ignore-credit-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: creditAt}
	creditCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"84000000-0000-4000-8000-000000000007", creditAt, []SourceBatchEvent{creditEvent})
	if err := store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: "210", ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "policy-anchor-post-credit", EventTime: creditAt, ObservedAt: creditAt,
		StreamWatermarkAt: creditAt, ServiceUnits: "80", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		SourceCursor: "credit:210:post", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("post-anchor credit fact was rejected: %v", err)
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
	if account.BootstrapKind != "POLICY_ANCHOR" {
		t.Fatalf("re-read account bootstrap_kind=%s", account.BootstrapKind)
	}
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, creditAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("post-anchor projection failed: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if projection.ExpectedBalance.Cmp(big.NewInt(80)) != 0 {
		t.Fatalf("post-anchor projection expected balance=%s want 80 (the anchor's own 500 is opening, non-invoiceable)",
			projection.ExpectedBalance)
	}
}

// TestRequeueSourceDependencySkipsPrePolicyUsageAndBalanceBacklog covers
// design 2.2: an identity wake (source_external_account /
// invoice_oidc_user) bulk-marks parked usage_event/balance_checkpoint
// entities observed before the policy start as processed with
// processing_error='PRE_POLICY_SKIPPED', while releasing everything else
// (including pre-policy events of other entity types, e.g. credits) for
// normal processing as today.
func TestRequeueSourceDependencySkipsPrePolicyUsageAndBalanceBacklog(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000220"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-anchor-wake-test','v2-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"usage", "balances", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	keyHMAC := "h1:" + strings.Repeat("a", 64)

	park := func(label, stream, entityType string, observedAt time.Time) string {
		t.Helper()
		eventID := randomUUID()
		var seq int64
		var lastHash string
		if err := store.pool.QueryRow(ctx, `SELECT sequence,COALESCE(last_batch_hash,'') FROM source_ingest_state
			WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, stream).Scan(&seq, &lastHash); err != nil {
			t.Fatal(err)
		}
		event := SourceBatchEvent{EventID: eventID, EntityType: entityType, Operation: "upsert",
			PayloadHash: testHash(label), PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: observedAt}
		if _, err := store.CommitSourceBatch(ctx, SourceBatchInput{
			SourceInstanceID: sourceID, StreamID: stream, BatchID: randomUUID(), Sequence: seq + 1,
			BodyHash: testHash(label + "-body"), PreviousBatchHash: lastHash, SigningKeyID: "test-key",
			SourceRuntimeVersion: "v2-test", SourceAgentVersion: "v2-agent", SourceCapturedAt: observedAt,
			ProjectionStatus: "healthy", Events: []SourceBatchEvent{event},
			Actor: AuditActor{Type: "source_connector", ID: sourceID},
		}); err != nil {
			t.Fatalf("commit %s: %v", label, err)
		}
		claims, err := store.ClaimUnprocessedSourceEvents(ctx, 10, time.Now().UTC().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		var claim *SourceEventClaim
		for i := range claims {
			if claims[i].EventID == eventID {
				claim = &claims[i]
			}
		}
		if claim == nil {
			t.Fatalf("event %s was not claimable", label)
		}
		if err = store.MarkSourceEventWaitingDependency(ctx, *claim, "source_external_account", keyHMAC,
			time.Now().UTC().Add(12*time.Hour)); err != nil {
			t.Fatal(err)
		}
		return eventID
	}

	preUsage := park("wake-pre-usage", "usage", "usage_event", policyStart.Add(-2*time.Hour))
	preBalance := park("wake-pre-balance", "balances", "balance_checkpoint", policyStart.Add(-2*time.Hour))
	postUsage := park("wake-post-usage", "usage", "usage_event", policyStart.Add(2*time.Hour))
	preCredit := park("wake-pre-credit", "credits", "credit_event", policyStart.Add(-2*time.Hour))

	if _, err := store.RequeueSourceDependency(ctx, "source_external_account", keyHMAC); err != nil {
		t.Fatal(err)
	}

	status := func(eventID string) (string, string) {
		t.Helper()
		var procStatus string
		var procError *string
		if err := store.pool.QueryRow(ctx, `SELECT processing_status,processing_error FROM source_ingest_events
			WHERE event_id=$1::uuid`, eventID).Scan(&procStatus, &procError); err != nil {
			t.Fatal(err)
		}
		errText := ""
		if procError != nil {
			errText = *procError
		}
		return procStatus, errText
	}

	if s, e := status(preUsage); s != "processed" || e != "PRE_POLICY_SKIPPED" {
		t.Fatalf("pre-policy usage event was not skipped: status=%s error=%s", s, e)
	}
	if s, e := status(preBalance); s != "processed" || e != "PRE_POLICY_SKIPPED" {
		t.Fatalf("pre-policy balance checkpoint was not skipped: status=%s error=%s", s, e)
	}
	if s, e := status(postUsage); s != "queued" || e != "" {
		t.Fatalf("post-policy usage event was not released normally: status=%s error=%s", s, e)
	}
	if s, e := status(preCredit); s != "queued" || e != "" {
		t.Fatalf("credit event must never be pre-policy skipped: status=%s error=%s", s, e)
	}
}

// TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact is design 2.3's
// assertion-style test: the projection's usage window is bounded below by
// account.CutoverAt, so a usage fact that somehow predates it (a defensive
// scenario -- normal ingestion paths freeze on SOURCE_GAP instead of
// persisting such a row) is never allocated. No query change was needed for
// this; the windowing behavior under test depends only on account.CutoverAt,
// not on bootstrap_kind, so it does not matter that the checkpoint below
// bootstraps this account via POLICY_ANCHOR (the same path any non-baseline,
// post-policy checkpoint takes since design 2.1).
func TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-24 * time.Hour)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000230"
	userID := "20000000-0000-4000-8000-000000000230"
	accountID := "30000000-0000-4000-8000-000000000230"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-anchor-window-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','policy-anchor-window-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'230','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}

	anchor := policyStart.Add(-1 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000230",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("policy-anchor-window-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: anchor}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000004", anchor, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("policy-anchor-window-manifest")
	configHash := testHash("policy-anchor-window-config")
	snapshotHash := testHash("policy-anchor-window-snapshot")
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: snapshotHash,
		BaselineSnapshotHash: snapshotHash, BaselineRowCount: 0, SigningKeyID: "ignored-payload-key",
		CutoverAt: anchor, DatabaseClock: anchor, StreamWatermarkAt: anchor,
		ExternalEventID: manifestEvent.EventID, BatchID: manifestCycle.batchID,
		ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)

	// Must be >= policyStart (not just > anchor): design 2.1's
	// pre-policy-checkpoint-ignored rule applies to non-baseline accounts too
	// (bullet 3) -- a checkpoint before policyStart would be skipped instead
	// of bootstrapping the account this test needs.
	checkpointAsOf := policyStart.Add(1 * time.Minute)
	checkpointEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000231",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("policy-anchor-window-checkpoint-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: checkpointAsOf}
	checkpointCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000005", checkpointAsOf, []SourceBatchEvent{checkpointEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "230", ExternalEventID: checkpointEvent.EventID,
		CheckpointID: "reconcile-anchor-230", CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "0", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(checkpointCycle.cycleID),
		SnapshotRowCount: "1", AsOf: checkpointAsOf, ObservedAt: checkpointAsOf, StreamWatermarkAt: checkpointAsOf,
		SourceCursor: "balance:230", SourceRevision: checkpointEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: checkpointCycle.batchID, ScanCycleID: checkpointCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}

	preUsageAt := anchor.Add(-1 * time.Hour)
	preEligible := !preUsageAt.Before(policyStart)
	preUsageID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,
			stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,$6,60,$7,$8,$9,'wallet',$10,1,'window:pre',$6,$11,$6)`,
		preUsageID, sourceID, accountID, "window-pre-event", "window-pre-usage",
		preUsageAt, "SUB2_BALANCE_1E8", manifestHash, configHash, preEligible,
		testHash("window-pre-usage-revision")); err != nil {
		t.Fatal(err)
	}
	creditAt := anchor.Add(2 * time.Hour)
	creditID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			credit_kind,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,$6,100,$7,$8,$9,'BONUS',1,'window:credit',$6,$10,$6)`,
		creditID, sourceID, accountID, "window-credit-event", "window-credit",
		creditAt, "SUB2_BALANCE_1E8", manifestHash, configHash,
		testHash("window-credit-revision")); err != nil {
		t.Fatal(err)
	}
	postUsageAt := anchor.Add(3 * time.Hour)
	postEligible := !postUsageAt.Before(policyStart)
	postUsageID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,
			stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,$6,30,$7,$8,$9,'wallet',$10,2,'window:post',$6,$11,$6)`,
		postUsageID, sourceID, accountID, "window-post-event", "window-post-usage",
		postUsageAt, "SUB2_BALANCE_1E8", manifestHash, configHash, postEligible,
		testHash("window-post-usage-revision")); err != nil {
		t.Fatal(err)
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
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, anchor.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	for _, allocation := range projection.Allocations {
		if allocation.UsageID == preUsageID {
			t.Fatalf("pre-anchor usage fact %s was allocated: %+v", preUsageID, allocation)
		}
	}
	found := false
	for _, allocation := range projection.Allocations {
		if allocation.UsageID == postUsageID && allocation.CreditID == creditID {
			found = true
			if allocation.Units.Cmp(big.NewInt(30)) != 0 {
				t.Fatalf("post-anchor usage allocation units=%s want 30", allocation.Units)
			}
		}
	}
	if !found {
		t.Fatal("post-anchor usage fact was not allocated against the in-window credit")
	}
	if projection.ExpectedBalance.Cmp(big.NewInt(70)) != 0 {
		t.Fatalf("expected balance=%s want 70 (100 credit - 30 consumed by the post-anchor usage only)",
			projection.ExpectedBalance)
	}
}

// TestScanCycleFrozenDeadEventPublishesWhilePureTransientDeadEventHoldsCycle
// covers design 2.5's implementable half: tryPublishEconomicScanCyclesTx
// treats a failed/dead event like parked_identity (not holding the cycle)
// only when its account was already frozen for it -- correlated via
// eligibility_freezes.source_revision_hash matching the ingest event's
// payload_hash, since freeze rows record a business object key
// (checkpoint/usage/credit id), never the ingest event's own UUID, and the
// event's plaintext payload is not otherwise queryable at this layer. A dead
// event with no matching freeze (pure transient exhaustion) still holds the
// cycle. This test does not cover the design's other half -- auto-freezing a
// dead event that has no freeze with reason EVENT_DEAD -- because
// eligibility_freezes.freeze_reason has no such value without a schema
// migration (see handoff).
func TestScanCycleFrozenDeadEventPublishesWhilePureTransientDeadEventHoldsCycle(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := "10000000-0000-4000-8000-000000000240"
	userID := "20000000-0000-4000-8000-000000000240"
	accountID := "30000000-0000-4000-8000-000000000240"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-anchor-freeze-cycle-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','policy-anchor-freeze-cycle-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'240','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "usage", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	ceiling := time.Now().UTC().Truncate(time.Microsecond)

	// tryPublishEconomicScanCyclesTx skips publication entirely for a source
	// with no registered manifest, so register one first. integrationStore
	// pins the policy start at (now-24h); the manifest cutover must be well
	// before that, per RegisterCutoverManifest's own invariant check.
	manifestAt := ceiling.Add(-48 * time.Hour)
	manifestChain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000240",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("policy-anchor-freeze-cycle-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: manifestAt}
	manifestCycle := manifestChain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000000006", manifestAt, []SourceBatchEvent{manifestEvent})
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: testHash("policy-anchor-freeze-cycle-manifest"),
		SourceRuntimeVersion: "v3-test", ProjectionContract: "sub2api-economic-v4",
		ConfigurationHash: testHash("policy-anchor-freeze-cycle-config"), UnitCode: "SUB2_BALANCE_1E8",
		PaymentsCeiling: "p0", UsageCeiling: "u0", CreditsCeiling: "c0", BalancesCeiling: "b0",
		BaselineSnapshotID:   testHash("policy-anchor-freeze-cycle-snapshot"),
		BaselineSnapshotHash: testHash("policy-anchor-freeze-cycle-snapshot"), BaselineRowCount: 0,
		SigningKeyID: "ignored-payload-key", CutoverAt: manifestAt, DatabaseClock: manifestAt,
		StreamWatermarkAt: manifestAt, ExternalEventID: manifestEvent.EventID,
		BatchID: manifestCycle.batchID, ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)

	killCycle := func(stream, label string, freeze bool) string {
		t.Helper()
		chain := newV3TestChain()
		event := SourceBatchEvent{EventID: randomUUID(), EntityType: "usage_event", Operation: "upsert",
			PayloadHash: testHash(label), PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: ceiling}
		cycle := chain.commit(t, store, ctx, sourceID, stream, randomUUID(), ceiling, []SourceBatchEvent{event})
		if _, err := store.pool.Exec(ctx, `
			UPDATE source_ingest_events SET processing_status='dead',processing_error='PROJECTION_FAILED',
				attempt_count=8,lease_token=NULL,lease_expires_at=NULL,updated_at=now()
			WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3::uuid`,
			sourceID, stream, event.EventID); err != nil {
			t.Fatal(err)
		}
		if freeze {
			if _, err := store.pool.Exec(ctx, `
				INSERT INTO eligibility_freezes(
					id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,
					source_revision_hash,status)
				VALUES($1,$2,'EVENT_PAYLOAD_DRIFT','usage',$3,$4,'open')`,
				randomUUID(), accountID, label, event.PayloadHash); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, sourceID, stream,
			AuditActor{Type: "source_connector", ID: sourceID, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		if err = store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
			WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`,
			sourceID, stream, cycle.cycleID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}

	if status := killCycle("usage", "freeze-cycle-transient", false); status != "processing" {
		t.Fatalf("pure transient dead event must still hold the cycle: status=%s", status)
	}
	if status := killCycle("credits", "freeze-cycle-frozen", true); status != "published" {
		t.Fatalf("dead event tied to an already-frozen account must not hold the cycle: status=%s", status)
	}
}

// policyAnchorReanchorFixture sets up one source, manifest and account
// bootstrapped SIGNED_CUTOVER at a pre-policy cutover -- the "legacy"
// population design 2.4 re-anchors. Shared by the three
// TestReanchorLegacyAccount* tests below.
type policyAnchorReanchorFixture struct {
	store                       *Store
	ctx                         context.Context
	sourceID, userID, accountID string
	manifestHash, configHash    string
	cutover, policyStart        time.Time
	chain                       *v3TestChain
}

func newPolicyAnchorReanchorFixture(t *testing.T, suffix string) *policyAnchorReanchorFixture {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	// integrationStoreWithPolicyStart truncates to the second internally;
	// match that here so later exact-equality checks against the stored
	// policy (e.g. invoice_requests' eligibility_policy_start_at trigger)
	// compare the same value, not a sub-second-different one.
	policyStart := fixtureNow.Add(-10 * time.Hour).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-0000000025" + suffix
	userID := "20000000-0000-4000-8000-0000000025" + suffix
	accountID := "30000000-0000-4000-8000-0000000025" + suffix
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-anchor-reanchor-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','policy-anchor-reanchor-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,$4,$5,'test','verified')`, accountID, userID, sourceID, suffix,
		"h1:"+testHash("reanchor-account-"+suffix)); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	cutover := policyStart.Add(-1 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-0000000025" + suffix,
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("reanchor-manifest-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutover}
	checkpointEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-0000000026" + suffix,
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("reanchor-cutover-checkpoint-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: cutover}
	cutoverCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-0000000025"+suffix, cutover, []SourceBatchEvent{manifestEvent, checkpointEvent})
	manifestHash := testHash("reanchor-manifest-" + suffix)
	configHash := testHash("reanchor-config-" + suffix)
	// Must match the cutover cycle's own scan_snapshot_id (testHash of the
	// cycle ID, computed by v3TestChain.commit for the "balances" stream) --
	// ObserveBalanceCheckpoint's cutover-kind branch requires SourceSnapshotID
	// to equal both the manifest's baseline snapshot hash and the enclosing
	// cycle's own snapshot id, so all three must be the same value.
	snapshotHash := testHash(cutoverCycle.cycleID)
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: snapshotHash,
		BaselineSnapshotHash: snapshotHash, BaselineRowCount: 1, SigningKeyID: "ignored-payload-key",
		CutoverAt: cutover, DatabaseClock: cutover, StreamWatermarkAt: cutover,
		ExternalEventID: manifestEvent.EventID, BatchID: cutoverCycle.batchID,
		ScanCycleID: cutoverCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: suffix, ExternalEventID: checkpointEvent.EventID,
		CheckpointID: snapshotHash + ":" + suffix, CheckpointKind: "cutover", BaselineMember: true,
		BalanceServiceUnits: "0", UnitCode: "SUB2_BALANCE_1E8", BaselineSnapshotID: snapshotHash,
		SourceSnapshotID: snapshotHash, SnapshotRowCount: "1",
		AsOf: cutover, ObservedAt: cutover, StreamWatermarkAt: cutover,
		SourceCursor: "balance:" + suffix, SourceRevision: checkpointEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: cutoverCycle.batchID, ScanCycleID: cutoverCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", cutoverCycle)

	// finalizeSourceAccountsTx (which queues eligibility_projection_jobs
	// rows) only acts once all four economic streams -- payments, usage,
	// credits, balances -- have published at least one watermark. Tests
	// below publish payments/usage/balances themselves; publish an empty
	// credits cycle here once, up front, so job queueing works from the
	// start without every test needing to know this.
	creditsCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"84000000-0000-4000-8000-0000000029"+suffix, cutover, nil)
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", creditsCycle)

	var bootstrapKind string
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&bootstrapKind); err != nil || bootstrapKind != "SIGNED_CUTOVER" {
		t.Fatalf("fixture bootstrap_kind=%q err=%v", bootstrapKind, err)
	}

	return &policyAnchorReanchorFixture{store: store, ctx: ctx, sourceID: sourceID, userID: userID,
		accountID: accountID, manifestHash: manifestHash, configHash: configHash,
		cutover: cutover, policyStart: policyStart, chain: chain}
}

// queueJob explicitly queues an eligibility_projection_jobs row for the
// fixture's account. The normal auto-queue path (finalizeSourceAccountsTx)
// only fires once all four economic streams' watermarks have all advanced
// past the requested point together; coordinating that across payments/
// usage/credits/balances in lockstep for every test scenario below adds
// nothing to what's under test, so tests queue explicitly instead -- the
// queuing mechanism itself is pre-existing, untouched behavior, not part of
// design 2.4.
func (f *policyAnchorReanchorFixture) queueJob(t *testing.T, through time.Time) {
	t.Helper()
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			status='queued',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`,
		f.accountID, through); err != nil {
		t.Fatal(err)
	}
}

// TestReanchorLegacyAccountMigratesAllocationsAndAudits covers design 2.4's
// happy path: a legacy account with real prior consumption (built through the
// normal observe+project pipeline, not hand-crafted) is re-anchored the next
// time its projection job runs once a post-policy candidate checkpoint
// exists -- consumption_allocations for its usage events are gone, its
// WALLET_CASH lot's consumption state is reset to zero, the state row is
// updated in place (bootstrap_kind='POLICY_ANCHOR', new cutover_at/balance),
// and an audit event records old/new values plus the allocations-deleted
// count. A second job run is a no-op (the account no longer matches the
// legacy-kind trigger condition).
func TestReanchorLegacyAccountMigratesAllocationsAndAudits(t *testing.T) {
	f := newPolicyAnchorReanchorFixture(t, "61")
	store, ctx := f.store, f.ctx
	suffix := "61"

	// Build real prior consumption exactly as it would exist the moment this
	// slice first deploys against an already-active legacy account: payment
	// and usage after the policy start, finalized through a real
	// reconciliation checkpoint, processed by the same production projection
	// functions processEligibilityProjectionJob itself calls -- but invoked
	// directly here rather than through ProcessEligibilityProjectionJobs, to
	// stand in for a job that ran before this slice's re-anchor check
	// existed. That is not a contrivance: reanchorLegacyEligibilityAccountTx's
	// candidate lookup only cares that a checkpoint_kind='reconciliation' row
	// with as_of>=policy start exists, never whether it was already used as
	// balance evidence -- so in production, the very first job to run after
	// this slice deploys re-anchors every legacy account that already had
	// post-policy activity like this, which is exactly the scenario below
	// reproduces for the second phase, further down.
	finalizeAt := f.policyStart.Add(25 * time.Minute)

	paymentAt := f.policyStart.Add(10 * time.Minute)
	paymentEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000262",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: testHash("reanchor-payment-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: finalizeAt}
	paymentCycle := f.chain.commit(t, store, ctx, f.sourceID, "payments",
		"84000000-0000-4000-8000-000000000026", finalizeAt, []SourceBatchEvent{paymentEvent})
	payment, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: f.userID, SourceInstanceID: f.sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "reanchor-payment", Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
			CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: paymentEvent.PayloadHash, CompletedAt: paymentAt, ObservedAt: finalizeAt},
		ExternalUserID: suffix, EventKind: "payment", ExternalEventID: paymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: paymentAt, SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "500", WalletUnitCode: "SUB2_BALANCE_1E8",
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, CausalDomain: "reanchor-ledger",
		CausalOrder: "1", SourceCursor: "payment:" + suffix, BatchID: paymentCycle.batchID,
		ScanCycleID: paymentCycle.cycleID, StreamWatermarkAt: finalizeAt,
	}, AuditActor{Type: "source_connector", ID: f.sourceID})
	if err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "payments", paymentCycle)

	usageAt := f.policyStart.Add(20 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000263",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("reanchor-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: finalizeAt}
	usageCycle := f.chain.commit(t, store, ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000000027", finalizeAt, []SourceBatchEvent{usageEvent})
	if err = store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: suffix, ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "reanchor-usage", EventTime: usageAt, ObservedAt: finalizeAt,
		StreamWatermarkAt: finalizeAt, ServiceUnits: "200", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "reanchor-ledger", CausalOrder: "2",
		SourceCursor: "usage:" + suffix, SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "usage", usageCycle)

	// A real reconciliation checkpoint whose balance correctly reflects the
	// payment minus the usage above (500-200=300 service units remaining) --
	// ensureBalanceCarryForwardProofTx (pre-existing, unrelated to design
	// 2.4) requires real balance evidence covering every new usage/credit/
	// funding visibility since finalized_through before it will let the
	// projection proceed; an empty covering cycle does not work here since
	// its derived carry-forward balance would be stale relative to the
	// payment above (reads as an incorrect negative-balance mismatch the
	// moment any real cash lands). This is also the checkpoint the second
	// phase below re-anchors to.
	finalizeEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000264",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("reanchor-finalize-event"),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: finalizeAt}
	finalizeCycle := f.chain.commit(t, store, ctx, f.sourceID, "balances",
		"84000000-0000-4000-8000-000000000028", finalizeAt, []SourceBatchEvent{finalizeEvent})
	if err = store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: suffix, ExternalEventID: finalizeEvent.EventID,
		CheckpointID: "reanchor-finalize", CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "300", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(finalizeCycle.cycleID),
		SnapshotRowCount: "1", AsOf: finalizeAt, ObservedAt: finalizeAt, StreamWatermarkAt: finalizeAt,
		SourceCursor: "balance:finalize", SourceRevision: finalizeEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 2,
		BatchID: finalizeCycle.batchID, ScanCycleID: finalizeCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "balances", finalizeCycle)

	// Process this exactly as processEligibilityProjectionJob would, minus
	// its reanchorLegacyEligibilityAccountTx call -- see the comment above
	// for why that faithfully represents a job that ran before this slice's
	// re-anchor check existed.
	preThrough := finalizeAt.Add(time.Minute)
	workerActor := AuditActor{Type: "system", ID: "test-worker"}
	preTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preAccount, err := getEligibilityAccountTx(ctx, preTx, f.accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBalanceCarryForwardProofTx(ctx, preTx, preAccount, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if err := reprojectEligibilityTx(ctx, preTx, f.accountID, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if err := evaluatePendingBalanceEvidenceTx(ctx, preTx, f.accountID, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if err := reprojectEligibilityTx(ctx, preTx, f.accountID, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if _, err := preTx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET finalized_through=GREATEST(finalized_through,$1),projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$2`, preThrough, f.accountID); err != nil {
		t.Fatal(err)
	}
	if err := preTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var preReanchorStatus, preReanchorKind string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status,bootstrap_kind FROM source_account_eligibility_state
		WHERE external_account_id=$1`, f.accountID).Scan(&preReanchorStatus, &preReanchorKind); err != nil {
		t.Fatal(err)
	}
	if preReanchorStatus != "active" || preReanchorKind != "SIGNED_CUTOVER" {
		t.Fatalf("pre-reanchor status=%q kind=%q, want active SIGNED_CUTOVER after ordinary consumption", preReanchorStatus, preReanchorKind)
	}
	lot, err := store.GetFundingLot(ctx, payment.Lot.ID)
	if err != nil || lot.ConsumedCashMinor != 20_000 {
		t.Fatalf("pre-reanchor lot=%+v err=%v (want 200/500 units of 50000 minor = 20000 consumed)", lot, err)
	}
	var allocationsBefore int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM consumption_allocations ca
		JOIN source_usage_events sue ON sue.id=ca.usage_event_id
		WHERE sue.external_account_id=$1`, f.accountID).Scan(&allocationsBefore); err != nil || allocationsBefore == 0 {
		t.Fatalf("pre-reanchor allocations=%d err=%v (fixture must have real consumption to reset)", allocationsBefore, err)
	}

	// Now that design 2.4 is live, the very next job run finds this same
	// finalize checkpoint via reanchorLegacyEligibilityAccountTx's candidate
	// lookup (it does not care whether the checkpoint was already used as
	// balance evidence above) and re-anchors to it instead of continuing to
	// project under the legacy boundary.
	f.queueJob(t, finalizeAt.Add(2*time.Minute))
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("reanchor projection processed=%d err=%v", processed, err)
	}

	var bootstrapKind string
	var storedCutoverAt time.Time
	var cutoverBalance string
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind,cutover_at,cutover_balance_units::text
		FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(
		&bootstrapKind, &storedCutoverAt, &cutoverBalance); err != nil {
		t.Fatal(err)
	}
	if bootstrapKind != "POLICY_ANCHOR" || !storedCutoverAt.Equal(finalizeAt.UTC()) || cutoverBalance != "300" {
		t.Fatalf("post-reanchor state kind=%s cutover_at=%s balance=%s", bootstrapKind, storedCutoverAt, cutoverBalance)
	}
	var allocationsAfter int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM consumption_allocations ca
		JOIN source_usage_events sue ON sue.id=ca.usage_event_id
		WHERE sue.external_account_id=$1`, f.accountID).Scan(&allocationsAfter); err != nil || allocationsAfter != 0 {
		t.Fatalf("post-reanchor allocations=%d err=%v, want 0", allocationsAfter, err)
	}
	lot, err = store.GetFundingLot(ctx, payment.Lot.ID)
	if err != nil || lot.ConsumedCashMinor != 0 {
		t.Fatalf("post-reanchor lot=%+v err=%v, want consumed_cash_minor=0", lot, err)
	}
	var migratedAudits int
	var beforeHash, afterHash string
	if err := store.pool.QueryRow(ctx, `SELECT count(*),COALESCE(MIN(before_hash),''),COALESCE(MIN(after_hash),'')
		FROM audit_events WHERE action='eligibility.policy_anchor.migrated' AND object_id=$1`,
		f.accountID).Scan(&migratedAudits, &beforeHash, &afterHash); err != nil || migratedAudits != 1 {
		t.Fatalf("migrated audit count=%d err=%v", migratedAudits, err)
	}
	if beforeHash == "" || afterHash == "" || beforeHash == afterHash {
		t.Fatalf("migrated audit before/after hashes not recorded distinctly: before=%q after=%q", beforeHash, afterHash)
	}

	// Idempotent: a second job run finds nothing to re-anchor (bootstrap_kind
	// is no longer legacy) and does not touch the ledger or audit trail again.
	f.queueJob(t, finalizeAt.Add(3*time.Minute))
	processed, err = store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("idempotent re-run processed=%d err=%v", processed, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.migrated' AND object_id=$1`,
		f.accountID).Scan(&migratedAudits); err != nil || migratedAudits != 1 {
		t.Fatalf("migrated audit count after idempotent re-run=%d err=%v", migratedAudits, err)
	}
}

// TestReanchorLegacyAccountBlockedByReservationFreezesWithoutTouchingLedger
// covers design 2.4's guard: a legacy account whose funding lot has a real
// invoice allocation (reserved) is frozen (POLICY_ANCHOR_BLOCKED) instead of
// having its consumption state reset -- resetting would corrupt real invoice
// accounting, and for an issued lot the funding_lots_consumed_cash_allocation_bound
// CHECK constraint would refuse it outright regardless. Idempotent: a second
// run does not re-freeze or duplicate the audit trail.
func TestReanchorLegacyAccountBlockedByReservationFreezesWithoutTouchingLedger(t *testing.T) {
	f := newPolicyAnchorReanchorFixture(t, "62")
	store, ctx := f.store, f.ctx
	suffix := "62"

	// Real prior consumption, built the same way and for the same reason as
	// the happy-path test: payment and usage after the policy start,
	// finalized through a real reconciliation checkpoint via the production
	// projection functions called directly (standing in for a job that ran
	// before this slice's re-anchor check existed). funding_lots_consumption_state_guard
	// requires reserved_minor+issued_minor<=consumed_cash_minor, so the
	// reservation below needs this real consumption to reserve against.
	finalizeAt := f.policyStart.Add(25 * time.Minute)

	paymentAt := f.policyStart.Add(10 * time.Minute)
	paymentEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000362",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: testHash("reanchor-blocked-payment-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: finalizeAt}
	paymentCycle := f.chain.commit(t, store, ctx, f.sourceID, "payments",
		"84000000-0000-4000-8000-000000000036", finalizeAt, []SourceBatchEvent{paymentEvent})
	payment, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: f.userID, SourceInstanceID: f.sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "reanchor-blocked-payment", Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
			CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: paymentEvent.PayloadHash, CompletedAt: paymentAt, ObservedAt: finalizeAt},
		ExternalUserID: suffix, EventKind: "payment", ExternalEventID: paymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: paymentAt, SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "500", WalletUnitCode: "SUB2_BALANCE_1E8",
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, CausalDomain: "reanchor-blocked-ledger",
		CausalOrder: "1", SourceCursor: "payment:" + suffix, BatchID: paymentCycle.batchID,
		ScanCycleID: paymentCycle.cycleID, StreamWatermarkAt: finalizeAt,
	}, AuditActor{Type: "source_connector", ID: f.sourceID})
	if err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "payments", paymentCycle)

	usageAt := f.policyStart.Add(20 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000363",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("reanchor-blocked-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: finalizeAt}
	usageCycle := f.chain.commit(t, store, ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000000037", finalizeAt, []SourceBatchEvent{usageEvent})
	if err = store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: suffix, ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "reanchor-blocked-usage", EventTime: usageAt, ObservedAt: finalizeAt,
		StreamWatermarkAt: finalizeAt, ServiceUnits: "200", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "reanchor-blocked-ledger", CausalOrder: "2",
		SourceCursor: "usage:" + suffix, SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "usage", usageCycle)

	finalizeEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000364",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("reanchor-blocked-finalize-event"),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: finalizeAt}
	finalizeCycle := f.chain.commit(t, store, ctx, f.sourceID, "balances",
		"84000000-0000-4000-8000-000000000038", finalizeAt, []SourceBatchEvent{finalizeEvent})
	if err = store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: suffix, ExternalEventID: finalizeEvent.EventID,
		CheckpointID: "reanchor-blocked-finalize", CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "300", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(finalizeCycle.cycleID),
		SnapshotRowCount: "1", AsOf: finalizeAt, ObservedAt: finalizeAt, StreamWatermarkAt: finalizeAt,
		SourceCursor: "balance:blocked-finalize", SourceRevision: finalizeEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 2,
		BatchID: finalizeCycle.batchID, ScanCycleID: finalizeCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "balances", finalizeCycle)

	// Process this exactly as processEligibilityProjectionJob would, minus
	// its reanchorLegacyEligibilityAccountTx call (see the happy-path test's
	// identical step for why that faithfully represents a job that ran
	// before this slice's re-anchor check existed).
	preThrough := finalizeAt.Add(time.Minute)
	workerActor := AuditActor{Type: "system", ID: "test-worker"}
	preTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preAccount, err := getEligibilityAccountTx(ctx, preTx, f.accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBalanceCarryForwardProofTx(ctx, preTx, preAccount, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if err := reprojectEligibilityTx(ctx, preTx, f.accountID, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if err := evaluatePendingBalanceEvidenceTx(ctx, preTx, f.accountID, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if err := reprojectEligibilityTx(ctx, preTx, f.accountID, preThrough, workerActor); err != nil {
		t.Fatal(err)
	}
	if _, err := preTx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET finalized_through=GREATEST(finalized_through,$1),projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$2`, preThrough, f.accountID); err != nil {
		t.Fatal(err)
	}
	if err := preTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A real, reserved invoice allocation against this lot -- built directly
	// (not through Submit, which needs a profile/settings ceremony
	// orthogonal to this test) but satisfying the same invariants Submit's
	// own writes would.
	profileID := "40000000-0000-4000-8000-000000000062"
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_profiles(
		id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified)
		VALUES($1,$2,'personal',decode(repeat('11',16),'hex'),decode(repeat('22',16),'hex'),TRUE)`,
		profileID, f.userID); err != nil {
		t.Fatal(err)
	}
	requestID := "60000000-0000-4000-8000-000000000062"
	// eligibility_policy_start_at/version must exactly match the singleton
	// row (enforce_invoice_request_policy_snapshot) -- integrationStoreWithPolicyStart
	// bumps policy_version when it moves the boundary, so read both live
	// rather than assuming version=1.
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_requests(
		id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,
		currency,amount_minor,status,idempotency_key,eligibility_policy_start_at,eligibility_policy_version)
		SELECT $1,'REANCHOR-BLOCKED',$2,$3,$4,decode(repeat('33',16),'hex'),'CNY',20000,
		'pending_review','reanchor-blocked-key',eligibility_start_at,policy_version
		FROM invoice_eligibility_policy WHERE singleton_id=1`,
		requestID, f.userID, f.sourceID, profileID); err != nil {
		t.Fatal(err)
	}
	// reanchorLegacyEligibilityAccountTx's reservation check only reads
	// invoice_allocations.allocation_state, but funding_lots.reserved_minor
	// must still be kept consistent with it: funding_lots_consumption_state_guard
	// (migration 0009) requires reserved_minor+issued_minor<=consumed_cash_minor,
	// and freezeEligibilityTx's own reservation-release cascade
	// (invalidateAccountReservationsTx -> releaseReservations) requires
	// reserved_minor to already reflect this reservation or it refuses to
	// release it.
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_allocations(
		id,invoice_request_id,funding_lot_id,amount_minor,source_revision_hash,allocation_state)
		VALUES($1,$2,$3,20000,$4,'reserved')`,
		"70000000-0000-4000-8000-000000000062", requestID, payment.Lot.ID, testHash("reanchor-blocked-allocation")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE funding_lots SET reserved_minor=20000,updated_at=now() WHERE id=$1`,
		payment.Lot.ID); err != nil {
		t.Fatal(err)
	}

	// The finalize checkpoint above is already a real
	// checkpoint_kind='reconciliation' row at/after the policy start --
	// reanchorLegacyEligibilityAccountTx's hasExposure check runs
	// unconditionally, before it ever looks for a candidate, so the
	// reservation above blocks regardless of whether a separate candidate
	// checkpoint exists.
	f.queueJob(t, finalizeAt.Add(2*time.Minute))
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("blocked projection processed=%d err=%v", processed, err)
	}

	var bootstrapKind, eligibilityStatus string
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind,eligibility_status
		FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(
		&bootstrapKind, &eligibilityStatus); err != nil {
		t.Fatal(err)
	}
	if bootstrapKind != "SIGNED_CUTOVER" || eligibilityStatus != "frozen" {
		t.Fatalf("blocked account kind=%s status=%s, want unchanged SIGNED_CUTOVER and frozen", bootstrapKind, eligibilityStatus)
	}
	var freezeCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND freeze_reason='POLICY_ANCHOR_BLOCKED' AND status='open'`,
		f.accountID).Scan(&freezeCount); err != nil || freezeCount != 1 {
		t.Fatalf("blocked freeze count=%d err=%v", freezeCount, err)
	}
	var blockedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.blocked' AND object_id=$1`,
		f.accountID).Scan(&blockedAudits); err != nil || blockedAudits != 1 {
		t.Fatalf("blocked audit count=%d err=%v", blockedAudits, err)
	}
	// The ledger itself -- the lot's consumption state the re-anchor would
	// otherwise reset -- is untouched: consumed_cash_minor stays at the real
	// prior consumption from above, it is not reset to 0.
	lot, err := store.GetFundingLot(ctx, payment.Lot.ID)
	if err != nil || lot.VerifiedCashMinor != 50_000 || lot.ConsumedCashMinor != 20_000 {
		t.Fatalf("blocked lot=%+v err=%v, consumption ledger must be untouched", lot, err)
	}
	// The reservation itself is released and its request rejected -- this is
	// freezeEligibilityTx's pre-existing cascade (invalidateAccountReservationsTx),
	// the same for every freeze reason and not something design 2.4 changes:
	// a frozen account's ledger is not trusted enough to still honor a
	// pending, unissued reservation.
	var allocationState string
	if err := store.pool.QueryRow(ctx, `SELECT allocation_state FROM invoice_allocations
		WHERE id='70000000-0000-4000-8000-000000000062'`).Scan(&allocationState); err != nil || allocationState != "released" {
		t.Fatalf("blocked allocation state=%q err=%v, want released by the freeze's own reservation cascade", allocationState, err)
	}
	var requestStatus string
	if err := store.pool.QueryRow(ctx, `SELECT status FROM invoice_requests WHERE id=$1`,
		requestID).Scan(&requestStatus); err != nil || requestStatus != "rejected" {
		t.Fatalf("blocked request status=%q err=%v, want rejected by the freeze's own reservation cascade", requestStatus, err)
	}

	// Idempotent: a second run must not re-freeze or duplicate the blocked
	// audit trail. The first reservation is already gone (released above),
	// so a fresh one is needed to recreate exposure -- otherwise the second
	// run would see no exposure at all and fall through to re-anchoring
	// instead of actually exercising the alreadyBlocked skip this guards.
	requestID2 := "60000000-0000-4000-8000-000000000162"
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_requests(
		id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,
		currency,amount_minor,status,idempotency_key,eligibility_policy_start_at,eligibility_policy_version)
		SELECT $1,'REANCHOR-BLOCKED-2',$2,$3,$4,decode(repeat('33',16),'hex'),'CNY',20000,
		'pending_review','reanchor-blocked-key-2',eligibility_start_at,policy_version
		FROM invoice_eligibility_policy WHERE singleton_id=1`,
		requestID2, f.userID, f.sourceID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_allocations(
		id,invoice_request_id,funding_lot_id,amount_minor,source_revision_hash,allocation_state)
		VALUES($1,$2,$3,20000,$4,'reserved')`,
		"70000000-0000-4000-8000-000000000162", requestID2, payment.Lot.ID, testHash("reanchor-blocked-allocation-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE funding_lots SET reserved_minor=20000,updated_at=now() WHERE id=$1`,
		payment.Lot.ID); err != nil {
		t.Fatal(err)
	}
	f.queueJob(t, finalizeAt.Add(3*time.Minute))
	processed, err = store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("idempotent blocked re-run processed=%d err=%v", processed, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND freeze_reason='POLICY_ANCHOR_BLOCKED'`,
		f.accountID).Scan(&freezeCount); err != nil || freezeCount != 1 {
		t.Fatalf("blocked freeze count after idempotent re-run=%d err=%v", freezeCount, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.blocked' AND object_id=$1`,
		f.accountID).Scan(&blockedAudits); err != nil || blockedAudits != 1 {
		t.Fatalf("blocked audit count after idempotent re-run=%d err=%v", blockedAudits, err)
	}
	// alreadyBlocked skips before ever reaching the freeze cascade again, so
	// the second reservation is left untouched -- unlike the first, it is
	// not released.
	if err := store.pool.QueryRow(ctx, `SELECT allocation_state FROM invoice_allocations
		WHERE id='70000000-0000-4000-8000-000000000162'`).Scan(&allocationState); err != nil || allocationState != "reserved" {
		t.Fatalf("second reservation state=%q err=%v, alreadyBlocked skip must leave it untouched", allocationState, err)
	}
}

// TestReanchorLegacyAccountWithNoPostPolicyCheckpointYetContinuesProjectionUnchanged
// covers design 2.4's third path: a legacy account with no active
// reservation and no post-policy reconciliation checkpoint yet is left
// exactly as it is -- no audit event is written and no error is raised
// (forcing an error here would incorrectly block settlement for every
// legacy account until one arrives). Concretely, with genuinely no balance
// evidence covering its new usage visibility, the job hits the pre-existing
// (unrelated to design 2.4) errBalanceCarryForwardProofPending path:
// ProcessEligibilityProjectionJobs re-queues it for retry rather than
// erroring or advancing anything, which is what "no error... it will be
// retried on the next job" actually looks like from the caller's side. A
// later job, once a real candidate checkpoint exists, re-anchors it
// (covered by the happy-path test above).
func TestReanchorLegacyAccountWithNoPostPolicyCheckpointYetContinuesProjectionUnchanged(t *testing.T) {
	f := newPolicyAnchorReanchorFixture(t, "63")
	store, ctx := f.store, f.ctx
	suffix := "63"

	var initialFinalizedThrough time.Time
	if err := store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state
		WHERE external_account_id=$1`, f.accountID).Scan(&initialFinalizedThrough); err != nil {
		t.Fatal(err)
	}

	// A funding lot for the usage below to draw against -- without one, the
	// projection legitimately freezes the account itself (USAGE_EXCEEDS_LEDGER,
	// pre-existing and unrelated to design 2.4: usage with no cash behind it
	// at all is never valid, checkpoint or no checkpoint), which is a
	// different failure mode than the one this test is targeting.
	paymentAt := f.policyStart.Add(10 * time.Minute)
	paymentEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000462",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: testHash("reanchor-nocandidate-payment-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: paymentAt}
	paymentCycle := f.chain.commit(t, store, ctx, f.sourceID, "payments",
		"84000000-0000-4000-8000-000000000045", paymentAt, []SourceBatchEvent{paymentEvent})
	if _, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: f.userID, SourceInstanceID: f.sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "reanchor-nocandidate-payment", Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
			CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: paymentEvent.PayloadHash, CompletedAt: paymentAt, ObservedAt: paymentAt},
		ExternalUserID: suffix, EventKind: "payment", ExternalEventID: paymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: paymentAt, SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "500", WalletUnitCode: "SUB2_BALANCE_1E8",
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, CausalDomain: "reanchor-nocandidate-ledger",
		CausalOrder: "1", SourceCursor: "payment:" + suffix, BatchID: paymentCycle.batchID,
		ScanCycleID: paymentCycle.cycleID, StreamWatermarkAt: paymentAt,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "payments", paymentCycle)

	usageAt := f.policyStart.Add(20 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000463",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("reanchor-nocandidate-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: usageAt}
	usageCycle := f.chain.commit(t, store, ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000000046", usageAt, []SourceBatchEvent{usageEvent})
	if err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: suffix, ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "reanchor-nocandidate-usage", EventTime: usageAt, ObservedAt: usageAt,
		StreamWatermarkAt: usageAt, ServiceUnits: "200", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "reanchor-nocandidate-ledger", CausalOrder: "2",
		SourceCursor: "usage:" + suffix, SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, f.sourceID, "usage", usageCycle)

	// Queue directly through the usage event -- deliberately no balances
	// activity at all, real or empty-cycle-derived: any checkpoint dated
	// at/after the policy start is itself indistinguishable from a re-anchor
	// candidate to reanchorLegacyEligibilityAccountTx's lookup, and an empty
	// covering cycle's derived carry-forward balance would be stale the
	// moment the payment above landed (see the happy-path test). With
	// genuinely no evidence, ensureBalanceCarryForwardProofTx legitimately
	// reports errBalanceCarryForwardProofPending, which
	// ProcessEligibilityProjectionJobs treats as "retry later", not an
	// error -- exactly the outcome this test wants.
	through := usageAt.Add(time.Minute)
	f.queueJob(t, through)

	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed != 0 {
		t.Fatalf("projection with no candidate processed=%d err=%v, want 0 processed (pending) and no error", processed, err)
	}

	var bootstrapKind, eligibilityStatus string
	var finalizedThrough time.Time
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind,eligibility_status,finalized_through
		FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(
		&bootstrapKind, &eligibilityStatus, &finalizedThrough); err != nil {
		t.Fatal(err)
	}
	if bootstrapKind != "SIGNED_CUTOVER" || eligibilityStatus != "active" {
		t.Fatalf("no-candidate account kind=%s status=%s, want unchanged SIGNED_CUTOVER and active", bootstrapKind, eligibilityStatus)
	}
	if !finalizedThrough.Equal(initialFinalizedThrough) {
		t.Fatalf("finalized_through changed with no evidence available: got %s want unchanged %s", finalizedThrough, initialFinalizedThrough)
	}
	var jobStatus string
	if err := store.pool.QueryRow(ctx, `SELECT status FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, f.accountID).Scan(&jobStatus); err != nil || jobStatus != "queued" {
		t.Fatalf("job status=%q err=%v, want queued for retry", jobStatus, err)
	}
	var freezeCount, auditCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1`, f.accountID).Scan(&freezeCount); err != nil || freezeCount != 0 {
		t.Fatalf("no-candidate freeze count=%d err=%v, want 0", freezeCount, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action IN ('eligibility.policy_anchor.blocked','eligibility.policy_anchor.migrated') AND object_id=$1`,
		f.accountID).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("no-candidate policy_anchor audit count=%d err=%v, want 0", auditCount, err)
	}
}
