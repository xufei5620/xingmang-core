package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

// seedAnchoredAccountWithCheckpoints builds a POLICY_ANCHOR-bootstrapped
// account with `count` reconciliation checkpoints an hour apart, through the
// real ingest APIs, so a projection job completes against it (the recipe is
// TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap's, with
// the checkpoint loop generalised). It returns the account id, the as_of of
// every checkpoint in order, and a function that enqueues a job through a
// given instant.
func seedAnchoredAccountWithCheckpoints(t *testing.T, store *Store, ctx context.Context, tag string, count int) (string, []time.Time, func(time.Time)) {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-24 * time.Hour).Truncate(time.Second)
	sourceID := "10000000-0000-4000-8000-0000000003" + tag
	userID := "20000000-0000-4000-8000-0000000003" + tag
	accountID := "30000000-0000-4000-8000-0000000003" + tag
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','batch-'||$2,'v3-test')`, sourceID, tag); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','batch-user-'||$2)`, userID, tag); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'3'||$4,'test','verified')`, accountID, userID, sourceID, tag); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "payments", "usage", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-0000000003" + tag,
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("batch-manifest-event-" + tag),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-0000000003"+tag, cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("batch-manifest-" + tag)
	configHash := testHash("batch-config-" + tag)
	snapshotHash := testHash("batch-snapshot-" + tag)
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
	asOfs := make([]time.Time, 0, count)
	for i := 0; i < count; i++ {
		asOf := policyStart.Add(time.Duration(i+1) * time.Hour)
		eventID := fmt.Sprintf("82000000-0000-4000-8000-00000000%02x%s", 0x10+i, tag)
		cycleID := fmt.Sprintf("84000000-0000-4000-8000-00000000%02x%s", 0x10+i, tag)
		event := SourceBatchEvent{EventID: eventID, EntityType: "balance_checkpoint", Operation: "upsert",
			PayloadHash:       testHash(fmt.Sprintf("batch-checkpoint-%d-%s", i, tag)),
			PayloadCiphertext: bytes.Repeat([]byte{byte(2 + i)}, 32), ObservedAt: asOf}
		cycle := chain.commit(t, store, ctx, sourceID, "balances", cycleID, asOf, []SourceBatchEvent{event})
		if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
			SourceInstanceID: sourceID, ExternalUserID: "3" + tag, ExternalEventID: eventID,
			CheckpointID: fmt.Sprintf("batch-checkpoint-%d", i), CheckpointKind: "reconciliation", BaselineMember: i == 0,
			BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
			SnapshotRowCount: "1", AsOf: asOf, ObservedAt: asOf, StreamWatermarkAt: asOf,
			SourceCursor: fmt.Sprintf("balance:3%s:%d", tag, i), SourceRevision: event.PayloadHash,
			CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: int64(i + 1),
			BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
		}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
			t.Fatal(err)
		}
		markV3CycleProcessed(t, store, ctx, sourceID, "balances", cycle)
		asOfs = append(asOfs, asOf)
	}
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
	return accountID, asOfs, queueJobThrough
}
