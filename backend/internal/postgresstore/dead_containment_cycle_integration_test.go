package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestTwoOpenFreezesOnOnePayloadDoNotInflateCycleRecordCounts covers the
// second half of XM-INV-DEAD-CONTAINMENT's change to
// tryPublishEconomicScanCyclesTx. Rendering the containment predicate from
// the shared definition also meant replacing a LEFT JOIN with an EXISTS, and
// that is not cosmetic: a LEFT JOIN multiplies the event row by the number of
// matching freezes, and two open freezes can legitimately share one
// payload_hash (migration 0009's unique index separates open freezes by
// reason, and MarkSourceEventFailed can add an EVENT_DEAD freeze beside an
// existing EVENT_PAYLOAD_DRIFT one for the same payload).
//
// The duplicate flowed straight into the manifest and checkpoint counts this
// query returns, which decide invalidBalanceSnapshot -- so a complete,
// correct cycle would be marked blocked, the stream's projection blocked, and
// every account on the source frozen with SOURCE_GAP. Both counts are covered
// because they are separate expressions with separate consequences; testing
// one is admitting nobody is watching the other.
func TestTwoOpenFreezesOnOnePayloadDoNotInflateCycleRecordCounts(t *testing.T) {
	for _, arm := range []struct {
		name       string
		entityType string
	}{
		{"checkpoint count", "balance_checkpoint"},
		{"manifest count", "cutover_manifest"},
	} {
		t.Run(arm.name, func(t *testing.T) {
			store, ctx := integrationStore(t)
			const (
				sourceID  = "10000000-0000-4000-8000-000000000241"
				userID    = "20000000-0000-4000-8000-000000000241"
				accountID = "30000000-0000-4000-8000-000000000241"
			)
			if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
				VALUES($1,'sub2api','dead-containment-cycle','v3-test')`, sourceID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
				VALUES($1,'test','dead-containment-cycle-user')`, userID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
				id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
				VALUES($1,$2,$3,'241','h1:'||repeat('4',64),'test','verified')`, accountID, userID, sourceID); err != nil {
				t.Fatal(err)
			}
			if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
				t.Fatal(err)
			}
			ceiling := time.Now().UTC().Truncate(time.Microsecond)
			manifestAt := ceiling.Add(-48 * time.Hour)
			chain := newV3TestChain()
			manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000241",
				EntityType: "cutover_manifest", Operation: "upsert",
				PayloadHash:       testHash("dead-containment-cycle-manifest-event"),
				PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: manifestAt}
			manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
				"84000000-0000-4000-8000-000000000241", manifestAt, []SourceBatchEvent{manifestEvent})
			if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
				SourceInstanceID: sourceID, ManifestHash: testHash("dead-containment-cycle-manifest"),
				SourceRuntimeVersion: "v3-test", ProjectionContract: "sub2api-economic-v4",
				ConfigurationHash: testHash("dead-containment-cycle-config"), UnitCode: "SUB2_BALANCE_1E8",
				PaymentsCeiling: "p0", UsageCeiling: "u0", CreditsCeiling: "c0", BalancesCeiling: "b0",
				BaselineSnapshotID:   testHash("dead-containment-cycle-snapshot"),
				BaselineSnapshotHash: testHash("dead-containment-cycle-snapshot"), BaselineRowCount: 0,
				SigningKeyID: "ignored-payload-key", CutoverAt: manifestAt, DatabaseClock: manifestAt,
				StreamWatermarkAt: manifestAt, ExternalEventID: manifestEvent.EventID,
				BatchID: manifestCycle.batchID, ScanCycleID: manifestCycle.cycleID,
				SourceRevision: manifestEvent.PayloadHash,
			}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
				t.Fatal(err)
			}
			markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)

			// One dead event, contained twice. The two reasons are what the
			// unique index allows to coexist, and both of them equally mean
			// "this account is stopped over this payload".
			payloadHash := testHash("dead-containment-cycle-" + arm.entityType)
			event := SourceBatchEvent{EventID: randomUUID(), EntityType: arm.entityType, Operation: "upsert",
				PayloadHash: payloadHash, PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: ceiling}
			cycle := chain.commit(t, store, ctx, sourceID, "balances", randomUUID(), ceiling, []SourceBatchEvent{event})
			if _, err := store.pool.Exec(ctx, `
				UPDATE source_ingest_events SET processing_status='dead',processing_error='PROJECTION_FAILED',
					attempt_count=8,lease_token=NULL,lease_expires_at=NULL,updated_at=now()
				WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2::uuid`,
				sourceID, event.EventID); err != nil {
				t.Fatal(err)
			}
			for _, reason := range []string{"EVENT_PAYLOAD_DRIFT", "EVENT_DEAD"} {
				if _, err := store.pool.Exec(ctx, `
					INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,
						trigger_object_id,source_revision_hash,status)
					VALUES($1,$2,$3,'balance_checkpoint',$4,$5,'open')`,
					randomUUID(), accountID, reason, "cycle-trigger-"+reason, payloadHash); err != nil {
					t.Fatal(err)
				}
			}
			// The fixture has to actually contain the duplicate, otherwise the
			// arm proves nothing about the join.
			var openFreezes int64
			if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
				WHERE status='open' AND source_revision_hash=$1`, payloadHash).Scan(&openFreezes); err != nil {
				t.Fatal(err)
			}
			if openFreezes != 2 {
				t.Fatalf("fixture has %d open freezes on the payload, want 2", openFreezes)
			}

			tx, err := store.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = tryPublishEconomicScanCyclesTx(ctx, tx, sourceID, "balances",
				AuditActor{Type: "source_connector", ID: sourceID, Reason: "test"}); err != nil {
				_ = tx.Rollback(context.Background())
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			var status string
			if err = store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
				WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`,
				sourceID, cycle.cycleID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "published" {
				t.Fatalf("a complete cycle was marked %s because two freezes on one payload were counted twice", status)
			}
			// The consequence, not just the count: a wrongly invalid balance
			// snapshot freezes every account on the source.
			var sourceGaps int64
			if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
				WHERE freeze_reason='SOURCE_GAP'`).Scan(&sourceGaps); err != nil {
				t.Fatal(err)
			}
			if sourceGaps != 0 {
				t.Fatalf("%d SOURCE_GAP freezes were opened over a cycle that was in fact complete", sourceGaps)
			}
			var projectionStatus string
			if err = store.pool.QueryRow(ctx, `SELECT projection_status FROM source_ingest_state
				WHERE source_instance_id=$1 AND stream_id='balances'`, sourceID).Scan(&projectionStatus); err != nil {
				t.Fatal(err)
			}
			if projectionStatus != "healthy" {
				t.Fatalf("the balances projection was blocked over a cycle that was in fact complete: %s", projectionStatus)
			}
		})
	}
}
