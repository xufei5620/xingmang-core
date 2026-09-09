package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

// TestSourceProjectionGradesSerializationFailuresAsTransientContention covers
// XM-INV-SER-BUSY, and reproduces the 2026-09-07 08:32:30 production outage.
//
// A routine periodic usage reconcile made the agent abandon its scan cycle
// while balance-checkpoint observation was running Serializable; the two sides
// collided on source_economic_scan_cycles (FOR UPDATE against FOR SHARE) and
// PostgreSQL raised serialization_failure, SQLSTATE 40001. RunOnce had no
// branch for it -- 40001 appeared nowhere in the codebase -- so it fell into
// the generic PROJECTION_FAILED arm, burned one of eight attempts every five
// minutes, and dead-lettered three events for good. EVENTS_DEAD is a fatal
// readiness reason, so both economic streams went not-ready and every funding
// lot in the deployment reported source_unavailable: six users could not open
// an invoice for twenty hours, until the rows were disposed of by hand.
//
// The failure is injected with a trigger that raises a real SQLSTATE from
// inside the projection's own transaction, so the error travels the whole
// production path -- store, then ProcessSourceEvent, then RunOnce's
// classification -- rather than being handed to the classifier directly. That
// matters here: the rule and the place that consults it are two separate
// pieces of code, and a test that exercised only the rule would still pass if
// RunOnce never called it.
//
// The third case is the control arm, and it is the reason the first two mean
// anything. Before this slice EVERY database error took the PROJECTION_FAILED
// branch, so an assertion that only checked 40001 would have been green
// against the old implementation too. Keeping a non-serialization SQLSTATE in
// the table proves the branch discriminates on the code, instead of grading
// every database error as transient -- which would be the opposite defect: an
// event that can never die no matter how broken it is.
func TestSourceProjectionGradesSerializationFailuresAsTransientContention(t *testing.T) {
	cases := []struct {
		name         string
		sqlstate     string
		wantStatus   string
		wantError    string
		wantAttempts int
	}{
		{"serialization_failure is requeued without spending budget", "40001", "queued", "SERIALIZATION_BUSY", 0},
		{"deadlock_detected is requeued without spending budget", "40P01", "queued", "SERIALIZATION_BUSY", 0},
		{"any other database error still spends the budget", "P0001", "failed", "PROJECTION_FAILED", 1},
	}
	for i, tc := range cases {
		i, tc := i, tc
		t.Run(tc.name, func(t *testing.T) {
			service, store, _, ctx := integrationApplication(t)
			suffix := fmt.Sprintf("%02d", 90+i)
			sourceID := "10000000-0000-4000-8000-0000000000" + suffix
			userID := "20000000-0000-4000-8000-0000000000" + suffix
			accountID := "30000000-0000-4000-8000-0000000000" + suffix
			eventID := "79000000-0000-4000-8000-0000000001" + suffix
			batchID := "79000000-0000-4000-8000-0000000002" + suffix
			cycleID := "79000000-0000-4000-8000-0000000003" + suffix

			if _, err := store.Pool().Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
				VALUES($1,'sub2api','ser-busy','ser-v3')`, sourceID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Pool().Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
				VALUES($1,'https://id.example','ser-busy-'||$2)`, userID, suffix); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Pool().Exec(ctx, `INSERT INTO external_accounts(
				id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
				VALUES($1,$2,$3,$4,$5,'test','verified')`, accountID, userID, sourceID, suffix,
				"h1:"+strings.Repeat(string(rune('a'+i)), 64)); err != nil {
				t.Fatal(err)
			}
			if err := store.ProvisionSourceStream(ctx, sourceID, "usage",
				postgresstore.AuditActor{Type: "system", ID: "test"}); err != nil {
				t.Fatal(err)
			}

			now := time.Now().UTC().Truncate(time.Microsecond)
			cutover := now.Add(-25 * time.Hour)
			manifestHash := strings.Repeat("a", 64)
			configHash := strings.Repeat("b", 64)
			snapshotHash := strings.Repeat(string(rune('c'+i)), 64)
			if _, err := store.Pool().Exec(ctx, `INSERT INTO source_cutover_manifests(
				source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
				projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
				credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
				baseline_row_count,signing_key_id)
				VALUES($1,$2,$3,$3,'ser-v3','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
				'p0','u0','c0','b0',$5,$5,1,'ser-key')`, sourceID, manifestHash,
				cutover, configHash, snapshotHash); err != nil {
				t.Fatal(err)
			}
			// The account is already bootstrapped and active, so the usage fact
			// below actually lands instead of parking on a dependency wait --
			// the projection has to reach a write for the injected fault to be
			// reachable at all.
			if _, err := store.Pool().Exec(ctx, `INSERT INTO source_account_eligibility_state(
				external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
				cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,eligibility_status)
				VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900,'active')`,
				accountID, sourceID, cutover, manifestHash); err != nil {
				t.Fatal(err)
			}

			causalOrder := "1"
			payload, err := json.Marshal(usageEventPayload{
				ExternalUserID: suffix, ExternalUsageID: "ser-busy-" + suffix,
				OccurredAt:          now.Add(-time.Hour).Format(time.RFC3339Nano),
				ServiceUnits:        "10", UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
				SourceCursor:        "usage:ser-busy-" + suffix,
				CausalDomain:        "usage_event", CausalOrder: &causalOrder,
				CutoverManifestHash: manifestHash, ConfigurationHash: configHash,
			})
			if err != nil {
				t.Fatal(err)
			}
			payloadSum := sha256.Sum256(payload)
			payloadHash := hex.EncodeToString(payloadSum[:])
			ciphertext, err := service.keys.Encrypt(payload, ingestEventAAD(sourceID, "usage", eventID, payloadHash))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
				SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "usage",
				BatchID: batchID, Sequence: 1, BodyHash: strings.Repeat("d", 64), SigningKeyID: "ser-key",
				SourceRuntimeVersion: "ser-v3", SourceAgentVersion: "ser-agent", SourceCapturedAt: now,
				ProjectionStatus: "healthy", StreamWatermarkAt: now, SourceCursor: "usage:ser-busy-" + suffix,
				ScanCeilingAt: now, ScanCeilingCursor: "usage-ceiling:" + suffix, ScanCycleID: cycleID,
				ScanComplete: true,
				Events: []postgresstore.SourceBatchEvent{{EventID: eventID, EntityType: "usage_event",
					Operation: "upsert", PayloadHash: payloadHash, PayloadCiphertext: ciphertext, ObservedAt: now}},
				Actor: postgresstore.AuditActor{Type: "source_connector", ID: sourceID},
			}); err != nil {
				t.Fatal(err)
			}

			// The injection point is source_usage_events, which
			// observeEligibilityFact writes inside ProcessSourceEvent's own
			// transaction. The placement is the whole point: the scan-cycle
			// table -- where the production conflict actually happened -- is
			// written by MarkSourceEventProcessed instead, which RunOnce
			// reaches on the success path, well past the classification under
			// test. An earlier draft injected there and proved only that
			// recordIsolated still works.
			fn := "xm_test_raise_" + suffix
			trg := "xm_test_trg_" + suffix
			if _, err = store.Pool().Exec(ctx, fmt.Sprintf(`
				CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $fn$
				BEGIN
					RAISE EXCEPTION 'injected fault' USING ERRCODE = '%s';
				END
				$fn$;
				CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON source_usage_events
					FOR EACH ROW EXECUTE FUNCTION %s();`, fn, tc.sqlstate, trg, fn)); err != nil {
				t.Fatalf("install %s fault injector: %v", tc.sqlstate, err)
			}
			t.Cleanup(func() {
				_, _ = store.Pool().Exec(ctx, fmt.Sprintf(
					`DROP TRIGGER IF EXISTS %s ON source_usage_events; DROP FUNCTION IF EXISTS %s();`, trg, fn))
			})

			processor := SourceEventProcessor{Service: service, BatchSize: 10, Now: func() time.Time { return now.Add(time.Minute) }}
			processed, err := processor.RunOnce(ctx)
			if err != nil {
				t.Fatalf("RunOnce must classify the claim and return cleanly rather than surface the raw database error: %v", err)
			}
			if processed != 1 {
				t.Fatalf("processed=%d, want 1 (the claim reached a terminal state of its own)", processed)
			}

			var status, errorCode string
			var attempts int
			var nextAttempt time.Time
			if err = store.Pool().QueryRow(ctx, `SELECT processing_status,attempt_count,COALESCE(processing_error,''),next_attempt_at
				FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
				sourceID, eventID).Scan(&status, &attempts, &errorCode, &nextAttempt); err != nil {
				t.Fatal(err)
			}
			if status != tc.wantStatus || errorCode != tc.wantError || attempts != tc.wantAttempts {
				t.Fatalf("SQLSTATE %s graded as status=%q error=%q attempts=%d; want status=%q error=%q attempts=%d",
					tc.sqlstate, status, errorCode, attempts, tc.wantStatus, tc.wantError, tc.wantAttempts)
			}
			// The retry delay separates the two gradings independently of the
			// marker string: contention comes back in seconds, a real failure
			// waits out the five-minute backoff.
			wantDelay := serializationBusyRetry
			if tc.wantError == "PROJECTION_FAILED" {
				wantDelay = 5 * time.Minute
			}
			if got := nextAttempt.Sub(now.Add(time.Minute)); got != wantDelay {
				t.Fatalf("SQLSTATE %s rescheduled in %s, want %s", tc.sqlstate, got, wantDelay)
			}
		})
	}
}
