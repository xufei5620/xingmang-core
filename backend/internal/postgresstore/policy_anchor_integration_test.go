package postgresstore

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// TestPrePolicyReconciliationCheckpointIsIgnoredForAccountWithoutState covers
// design XM-INV-POLICY-ANCHOR 2.1's implementable half: a reconciliation
// checkpoint dated before the invoice policy start, for an account with no
// eligibility state yet, is ignored (acknowledged, audited) instead of
// parking on the signed cutover row. The design's other half -- bootstrapping
// such an account directly from a post-policy-start checkpoint with
// bootstrap_kind='POLICY_ANCHOR' -- cannot be implemented without a schema
// migration (source_account_eligibility_state_bootstrap_kind_check only
// allows SIGNED_CUTOVER/POST_CUTOVER_REPLAY; see the handoff doc), so this
// test also locks in that the post-policy-start boundary is unchanged.
func TestPrePolicyReconciliationCheckpointIsIgnoredForAccountWithoutState(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow
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

	// A reconciliation checkpoint at/after the policy start still waits for
	// the signed cutover row today: the design's policy-anchor bootstrap
	// (bootstrap_kind='POLICY_ANCHOR') cannot be implemented without a schema
	// migration (see handoff), so this boundary is intentionally unchanged.
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
	if !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("post-policy baseline checkpoint should still wait for the signed cutover row: %v", err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("post-policy wait must not create eligibility state: count=%d err=%v", stateCount, err)
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
// this; the account here stands in for a policy-anchored account (its
// cutover_at is later than the historical usage fact under test) using the
// existing POST_CUTOVER_REPLAY bootstrap, since bootstrap_kind='POLICY_ANCHOR'
// cannot be constructed without a schema migration (see handoff) -- the
// windowing behavior under test depends only on account.CutoverAt, not on
// bootstrap_kind.
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

	// Must be >= policyStart (not just > anchor): design 2.1's new
	// pre-policy-checkpoint-ignored rule applies to non-baseline accounts too
	// (bullet 3), and this test wants the existing, unaffected
	// POST_CUTOVER_REPLAY bootstrap so it can test windowing, not 2.1.
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
