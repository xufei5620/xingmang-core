package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

// TestParkedFactWokenByBindingLandsOnItsOwnScanCycleBinding is
// XM-INV-BINDING-SKEW T1, and reproduces the 2026-09-07 production failure
// end to end.
//
// The shape: a usage event arrives for a customer who has not bound their
// account yet, so the projection parks it (parked_identity) and its scan cycle
// publishes without it. The agent keeps scanning, and a later cycle
// re-delivers the identical deterministic event id -- but half an hour later,
// so that cycle's batch carries a scan ceiling half an hour past the event's
// observed_at. source_ingest_events.observed_at is written once, at first
// delivery, and CommitSourceBatch never rewrites it. Then the customer binds,
// the wake requeues the parked event, and the projection tries to land it.
//
// Before this slice, ClaimUnprocessedSourceEvents preferred that newest
// binding on cycle status alone. source_processor.go hands the chosen batch's
// scan_ceiling_at over as the fact's StreamWatermarkAt against the event's own
// frozen ObservedAt, and validateFactMetadata refuses a gap wider than
// factClockSkewTolerance -- so the event failed with "source fact event
// time/watermark is invalid", every attempt, until it was dead. On 2026-09-07
// one customer binding did that to six customers' invoicing for 25 hours.
//
// This test therefore has to run through SourceEventProcessor.RunOnce rather
// than calling ObserveUsageEvent with a hand-built pair: the coupling under
// test is precisely the claim's ScanCeilingAt/ObservedAt becoming the fact's
// StreamWatermarkAt/ObservedAt, and a test that constructs those itself would
// be forging the inputs whose selection is the whole question.
func TestParkedFactWokenByBindingLandsOnItsOwnScanCycleBinding(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)

	const sourceID = "10000000-0000-4000-8000-0000000000d7"
	const userID = "20000000-0000-4000-8000-0000000000d7"
	const accountID = "30000000-0000-4000-8000-0000000000d7"
	const eventID = "79000000-0000-4000-8000-0000000001d7"
	const firstBatchID = "79000000-0000-4000-8000-0000000002d7"
	const lateBatchID = "79000000-0000-4000-8000-0000000003d7"
	const firstCycleID = "79000000-0000-4000-8000-0000000004d7"
	const lateCycleID = "79000000-0000-4000-8000-0000000005d7"
	const externalUserID = "skew-user"

	now := time.Now().UTC().Truncate(time.Microsecond)
	// The first delivery, thirty minutes ago: comfortably outside
	// factClockSkewTolerance of "now" and comfortably inside the fixture's
	// policy start (now-24h), so the wake below genuinely requeues the event
	// instead of writing it off as PRE_POLICY_SKIPPED.
	firstObservedAt := now.Add(-30 * time.Minute)
	occurredAt := now.Add(-2 * time.Hour)
	cutover := now.Add(-25 * time.Hour)
	manifestHash := strings.Repeat("a", 64)
	configHash := strings.Repeat("b", 64)
	snapshotHash := strings.Repeat("e", 64)

	if _, err := store.Pool().Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','binding-skew','skew-v3')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "usage",
		postgresstore.AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	// Mandatory: without a manifest row tryPublishEconomicScanCyclesTx
	// short-circuits on pgx.ErrNoRows and no cycle ever publishes, which would
	// silently make both cycles unusable and the test meaningless.
	if _, err := store.Pool().Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'skew-v3','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'skew-key')`, sourceID, manifestHash, cutover,
		configHash, snapshotHash); err != nil {
		t.Fatal(err)
	}
	// No external_accounts row yet. That absence is the production state --
	// facts arriving for a customer who has not bound -- and is what makes the
	// event park instead of landing on its first pass.

	causalOrder := "1"
	payload, err := json.Marshal(usageEventPayload{
		ExternalUserID: externalUserID, ExternalUsageID: "binding-skew-1",
		OccurredAt: occurredAt.Format(time.RFC3339Nano), ServiceUnits: "10",
		UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
		SourceCursor: "usage:binding-skew", CausalDomain: "usage_event", CausalOrder: &causalOrder,
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
	commit := func(batchID, cycleID, previousHash string, sequence int64, ceiling, observedAt time.Time) string {
		t.Helper()
		bodyHash := hex.EncodeToString([]byte(strings.Repeat(string(rune('0'+sequence)), 32)))
		if _, err := store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
			SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "usage",
			BatchID: batchID, Sequence: sequence, BodyHash: bodyHash, PreviousBatchHash: previousHash,
			SigningKeyID: "skew-key", SourceRuntimeVersion: "skew-v3", SourceAgentVersion: "skew-agent",
			SourceCapturedAt: ceiling, ProjectionStatus: "healthy", StreamWatermarkAt: ceiling,
			SourceCursor: "usage:binding-skew", ScanCeilingAt: ceiling,
			ScanCeilingCursor: "usage-ceiling:" + cycleID, ScanCycleID: cycleID, ScanComplete: true,
			Events: []postgresstore.SourceBatchEvent{{EventID: eventID, EntityType: "usage_event",
				Operation: "upsert", PayloadHash: payloadHash, PayloadCiphertext: ciphertext,
				ObservedAt: observedAt}},
			Actor: postgresstore.AuditActor{Type: "source_connector", ID: sourceID},
		}); err != nil {
			t.Fatalf("commit batch %s: %v", batchID, err)
		}
		return bodyHash
	}
	cycleStatus := func(cycleID string) string {
		t.Helper()
		var status string
		if err := store.Pool().QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
			WHERE source_instance_id=$1 AND stream_id='usage' AND scan_cycle_id=$2::uuid`,
			sourceID, cycleID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	ingestRow := func() (status string, attempts int, observedAt time.Time) {
		t.Helper()
		if err := store.Pool().QueryRow(ctx, `SELECT processing_status,attempt_count,observed_at
			FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
			sourceID, eventID).Scan(&status, &attempts, &observedAt); err != nil {
			t.Fatal(err)
		}
		return status, attempts, observedAt
	}

	firstBodyHash := commit(firstBatchID, firstCycleID, "", 1, firstObservedAt, firstObservedAt)

	processor := SourceEventProcessor{Service: service, BatchSize: 10,
		Now: func() time.Time { return now.Add(time.Minute) }}
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatalf("the parking pass must classify cleanly: %v", err)
	}
	status, _, _ := ingestRow()
	if status != "parked_identity" {
		t.Fatalf("processing_status=%q after the unbound pass, want parked_identity; without a parked event "+
			"the first cycle cannot publish and this fixture is not the production shape", status)
	}
	if got := cycleStatus(firstCycleID); got != "published" {
		t.Fatalf("first cycle status=%q, want published", got)
	}

	// The agent's next scan, half an hour later, re-delivering the identical
	// deterministic event id. Its ObservedAt is deliberately different, to pin
	// that CommitSourceBatch ignores it on a re-delivery -- which is exactly
	// why a late binding can never carry this event's fact.
	commit(lateBatchID, lateCycleID, firstBodyHash, 2, now, now)
	if got := cycleStatus(lateCycleID); got != "published" {
		t.Fatalf("late cycle status=%q, want published; a late binding the claim could not have chosen anyway "+
			"would make the selection assertion below vacuous", got)
	}
	if _, _, observedAt := ingestRow(); !observedAt.Equal(firstObservedAt) {
		t.Fatalf("observed_at=%s after the re-delivery, want the first delivery's %s", observedAt, firstObservedAt)
	}

	// The binding moment. The customer's account appears, bootstrapped and
	// active, and the wake releases the parked event.
	if _, err = store.Pool().Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'https://id.example','binding-skew')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool().Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,$4,$5,'test','verified')`, accountID, userID, sourceID, externalUserID,
		"h1:"+strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool().Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,eligibility_status)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900,'active')`,
		accountID, sourceID, cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	if err = service.wakeDependency(ctx, "source_external_account", sourceID, externalUserID); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = ingestRow(); status != "queued" {
		t.Fatalf("processing_status=%q after the binding wake, want queued; RequeueSourceDependency writes off "+
			"pre-policy usage events, and a written-off event would make every assertion below vacuous", status)
	}

	// The wake-up pass. This is the pass that failed eight times in
	// production.
	processed, err := processor.RunOnce(ctx)
	if err != nil {
		t.Fatalf("the wake-up pass isolated a failure: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed=%d, want 1", processed)
	}
	status, attempts, observedAt := ingestRow()
	if status != "processed" {
		t.Fatalf("processing_status=%q attempts=%d, want processed: the claim handed the fact a binding "+
			"validateFactMetadata refuses", status, attempts)
	}
	if !observedAt.Equal(firstObservedAt) {
		t.Fatalf("observed_at=%s, want %s", observedAt, firstObservedAt)
	}

	// Which binding carried it. source_sequence is the load-bearing column:
	// 1 is the first cycle's batch, 2 is the late one, so this says which
	// binding the claim chose without the test having to re-derive the claim.
	var factSequence int64
	var factWatermark, factObserved time.Time
	var facts int
	if err = store.Pool().QueryRow(ctx, `SELECT count(*) FROM source_usage_events
		WHERE source_instance_id=$1`, sourceID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("source_usage_events rows=%d, want exactly 1", facts)
	}
	if err = store.Pool().QueryRow(ctx, `SELECT source_sequence,stream_watermark_at,observed_at
		FROM source_usage_events WHERE source_instance_id=$1 AND external_event_id=$2`,
		sourceID, eventID).Scan(&factSequence, &factWatermark, &factObserved); err != nil {
		t.Fatal(err)
	}
	if factSequence != 1 {
		t.Fatalf("the fact landed against batch sequence %d, want 1: the claim took the late re-delivery, "+
			"whose ceiling this event's own observation can never carry", factSequence)
	}
	if !factWatermark.Equal(firstObservedAt) {
		t.Fatalf("fact stream_watermark_at=%s, want the first cycle's ceiling %s", factWatermark, firstObservedAt)
	}
	if !factObserved.Equal(firstObservedAt) {
		t.Fatalf("fact observed_at=%s, want %s", factObserved, firstObservedAt)
	}

	// The fixture really does straddle the rule. Handing the same fact the
	// late cycle's ceiling -- which is exactly what the claim did before this
	// slice -- is refused, with the message production logged verbatim.
	// Without this the positive assertions above could be green for reasons
	// having nothing to do with clock skew.
	err = store.ObserveUsageEvent(ctx, postgresstore.UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: externalUserID, ExternalEventID: eventID,
		ExternalUsageID: "binding-skew-1", EventTime: occurredAt, ObservedAt: firstObservedAt,
		StreamWatermarkAt: now, ServiceUnits: "10", UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
		CausalDomain: "usage_event", CausalOrder: causalOrder, SourceCursor: "usage:binding-skew",
		SourceRevision: payloadHash, CutoverManifestHash: manifestHash, ConfigurationHash: configHash,
		SourceSequence: 2, BatchID: lateBatchID, ScanCycleID: lateCycleID,
	}, postgresstore.AuditActor{Type: "source_connector", ID: sourceID})
	if err == nil {
		t.Fatal("the late cycle's ceiling was accepted against the frozen observation; the fixture does not " +
			"reproduce the production failure and the test above proves nothing")
	}
	if err.Error() != "source fact event time/watermark is invalid" {
		t.Fatalf("late-ceiling observation returned %q, want the verbatim production message", err.Error())
	}

	// Neither cycle was disturbed by any of this.
	for cycleID, want := range map[string]string{firstCycleID: "published", lateCycleID: "published"} {
		if got := cycleStatus(cycleID); got != want {
			t.Fatalf("cycle %s status=%q, want %q", cycleID, got, want)
		}
	}
}
