package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

// TestContainedDeadEventLeavesOtherAccountsInvoiceable is the only test that
// shows what XM-INV-DEAD-CONTAINMENT is actually for: on 2026-09-07 three dead
// events belonging to one account made nineteen funding lots across six users
// unavailable for thirty hours, because ListFundingLots and
// ListUserEligibilitySummaries both AND together the five streams' Ready flag,
// and one dead event anywhere on the source cleared it.
//
// Two accounts, one source, one dead usage event that belongs to A. A must
// still be stopped -- by its own freeze, at the account level -- and B must
// not notice anything.
//
// The dead event is driven through SourceEventProcessor.RunOnce rather than
// inserted as a row: the freeze that contains it comes from
// MarkSourceEventFailed's account-hint path, and hand-inserting a dead row
// skips exactly the code that decides whether containment is possible at all.
func TestContainedDeadEventLeavesOtherAccountsInvoiceable(t *testing.T) {
	service, store, settings, ctx := integrationApplication(t)
	// integrationApplication's own service leaves source freshness disabled on
	// purpose (see its comment). The five-stream gate is the whole subject
	// here, so this test needs a second Service with freshness configured,
	// matching buildProductionRuntime.
	freshnessService, err := NewService(store, testKeys(), settings, Options{
		MinimumRequestMinor: domain.MinimumRequestMinor, DownloadBaseURL: "https://invoice.example/",
		SourceEconomicHeartbeatMaxAge: 5 * time.Minute, SourceEconomicWatermarkMaxAge: 15 * time.Minute,
		SourceIdentitiesMaxAge: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Microsecond)
	freshnessService.now = func() time.Time { return clock }

	const sourceID = "10000000-0000-4000-8000-0000000000c1"
	userA, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://central-id.example", Subject: "containment-a",
		Email: "containment-a@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	userB, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://central-id.example", Subject: "containment-b",
		Email: "containment-b@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: sourceID, SourceType: domain.SourceSub2API, Name: "sub2-containment",
		RuntimeVersion: "test-runtime", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if err = store.ProvisionSourceStream(ctx, sourceID, stream,
			postgresstore.AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000001"}); err != nil {
			t.Fatal(err)
		}
	}
	manifestHash := containmentTestHash("containment-manifest")
	configHash := containmentTestHash("containment-config")
	if _, err = store.Pool().Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'test-runtime','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'payment_orders:0','usage_logs:0','credits:0','balance_snapshot:0',$2,$2,0,'test-key')`,
		sourceID, manifestHash, clock.Add(-48*time.Hour), configHash); err != nil {
		t.Fatal(err)
	}

	commit := func(stream, batchID, bodyHash, previousHash string, sequence int64, eventID, entity string, payload []byte, payloadHash string) {
		t.Helper()
		if _, commitErr := service.CommitVerifiedSourceBatch(ctx, sourceID, VerifiedSourceBatch{
			SourceInstanceID: sourceID, StreamID: stream, BatchID: batchID,
			Sequence: sequence, BodyHash: bodyHash, PreviousBatchHash: previousHash,
			SigningKeyID: "source-key-1", SourceRuntimeVersion: "test-runtime",
			SourceAgentVersion: "test-agent", SourceCapturedAt: clock, ProjectionStatus: "healthy",
			Events: []VerifiedSourceBatchEvent{{EventID: eventID, EntityType: entity, Operation: "upsert",
				PayloadHash: payloadHash, Payload: payload, ObservedAt: clock}},
		}); commitErr != nil {
			t.Fatalf("commit %s batch %s: %v", stream, batchID, commitErr)
		}
	}
	bindAndPay := func(externalUserID, subject, orderID string, sequence int64, suffix, previousSuffix string) {
		t.Helper()
		var previousIdentity, previousPayment string
		if previousSuffix != "" {
			previousIdentity = containmentTestHash("identity-" + previousSuffix)
			previousPayment = containmentTestHash("payment-" + previousSuffix)
		}
		identityPayload := []byte(fmt.Sprintf(`{"external_user_id":%q,"provider_type":"oidc","provider_key":"central","provider_subject":%q,"issuer":"https://central-id.example","verified_at":%q,"user_status":"unknown","updated_at":%q}`,
			externalUserID, subject, clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)))
		commit("identities", "73000000-0000-4000-8000-000000000"+suffix+"1",
			containmentTestHash("identity-"+suffix), previousIdentity, sequence,
			"73100000-0000-4000-8000-000000000"+suffix+"1", "identity_binding", identityPayload,
			containmentTestHash("identity-payload-"+suffix))
		paymentPayload := []byte(fmt.Sprintf(`{"external_order_id":%q,"external_user_id":%q,"status":"COMPLETED","order_type":"balance","amount":"600.00","pay_amount":"500.00","currency":"CNY","refund_amount":"0","gateway_refund_amount":"0","completed_at":%q,"created_at":%q,"updated_at":%q,"payment_type":"stripe","provider_key":"stripe","external_trade_ref_hmac":""}`,
			orderID, externalUserID, clock.Format(time.RFC3339Nano),
			clock.Add(-time.Hour).Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)))
		commit("payments", "73000000-0000-4000-8000-000000000"+suffix+"2",
			containmentTestHash("payment-"+suffix), previousPayment, sequence,
			"73100000-0000-4000-8000-000000000"+suffix+"2", "payment_order", paymentPayload,
			containmentTestHash("payment-payload-"+suffix))
	}
	bindAndPay("41", "containment-a", "9041", 1, "aa", "")
	bindAndPay("42", "containment-b", "9042", 2, "bb", "aa")

	processor := SourceEventProcessor{Service: service, BatchSize: 100, Now: func() time.Time { return clock.Add(time.Minute) }}
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	processor.Now = func() time.Time { return clock.Add(7 * time.Minute) }
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// A's usage event: its unit_code can never match the source's, so it
	// fails deterministically on every attempt and never persists a fact
	// row -- which is precisely the shape whose freeze can only come from the
	// application-layer account hint.
	causalOrder := "1"
	usagePayload, err := json.Marshal(usageEventPayload{
		ExternalUserID: "41", ExternalUsageID: "usage-contained-41", OccurredAt: clock.Format(time.RFC3339Nano),
		ServiceUnits: "10", UnitCode: "WRONG_UNIT_CODE", BillingScope: "wallet",
		SourceCursor: "usage:1", CausalDomain: "usage_event", CausalOrder: &causalOrder,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	usageSum := sha256.Sum256(usagePayload)
	usageHash := hex.EncodeToString(usageSum[:])
	const (
		usageEventID = "79000000-0000-4000-8000-0000000000c1"
		usageBatchID = "79000000-0000-4000-8000-0000000000c2"
		usageCycleID = "79000000-0000-4000-8000-0000000000c3"
	)
	ciphertext, err := service.keys.Encrypt(usagePayload, ingestEventAAD(sourceID, "usage", usageEventID, usageHash))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "usage",
		BatchID: usageBatchID, Sequence: 1, BodyHash: containmentTestHash("usage-batch"),
		SigningKeyID: "source-key-1", SourceRuntimeVersion: "test-runtime", SourceAgentVersion: "test-agent",
		SourceCapturedAt: clock, ProjectionStatus: "healthy", StreamWatermarkAt: clock, SourceCursor: "usage:1",
		ScanCeilingAt: clock, ScanCeilingCursor: "usage-ceiling:c1", ScanCycleID: usageCycleID, ScanComplete: true,
		Events: []postgresstore.SourceBatchEvent{{EventID: usageEventID, EntityType: "usage_event",
			Operation: "upsert", PayloadHash: usageHash, PayloadCiphertext: ciphertext, ObservedAt: clock}},
		Actor: postgresstore.AuditActor{Type: "source_connector", ID: sourceID},
	}); err != nil {
		t.Fatal(err)
	}
	deadProcessor := SourceEventProcessor{Service: service, BatchSize: 10, Now: func() time.Time { return clock.Add(10 * time.Minute) }}
	if _, err = deadProcessor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool().Exec(ctx, `UPDATE source_ingest_events SET attempt_count=7,next_attempt_at=now()-interval '1 second'
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`, sourceID, usageEventID); err != nil {
		t.Fatal(err)
	}
	if _, err = deadProcessor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// The fixture has to actually be in the shape under test. Everything
	// below is meaningless if the event is not dead or the freeze is not the
	// EVENT_DEAD one produced by the hint path.
	var status string
	var attempts int
	if err = store.Pool().QueryRow(ctx, `SELECT processing_status,attempt_count FROM source_ingest_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`, sourceID, usageEventID).
		Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || attempts != 8 {
		t.Fatalf("fixture event status=%s attempts=%d, want dead/8", status, attempts)
	}
	var accountA, freezeReason, triggerType, revisionHash string
	if err = store.Pool().QueryRow(ctx, `SELECT external_account_id::text,freeze_reason,trigger_object_type,source_revision_hash
		FROM eligibility_freezes WHERE status='open'`).Scan(&accountA, &freezeReason, &triggerType, &revisionHash); err != nil {
		t.Fatalf("the dead event produced no containing freeze, so this test would prove nothing: %v", err)
	}
	if freezeReason != "EVENT_DEAD" || triggerType != "usage_event" || revisionHash != usageHash {
		t.Fatalf("freeze reason=%s type=%s revision=%s want EVENT_DEAD/usage_event/%s",
			freezeReason, triggerType, revisionHash, usageHash)
	}

	// The five-stream readiness read model, seeded last so the dead event is
	// the only difference from a completely healthy source.
	pool := store.Pool()
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if _, err = pool.Exec(ctx, `UPDATE source_ingest_state SET
			source_runtime_version='test-runtime', source_agent_version='test-agent',
			projection_status='healthy', last_accepted_at=$3, last_nonempty_batch_at=$3
			WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, stream, clock); err != nil {
			t.Fatal(err)
		}
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if _, err = pool.Exec(ctx, `INSERT INTO source_economic_stream_watermarks(
			source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,1,$4,$5)
			ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET watermark_at=EXCLUDED.watermark_at`,
			sourceID, stream, clock, stream+":1", configHash); err != nil {
			t.Fatal(err)
		}
	}

	assertUsageStream := func(phase string, wantReady bool, wantReasons []string) {
		t.Helper()
		report, healthErr := store.SourceHealth(ctx, freshnessService.sourceFreshnessPolicy())
		if healthErr != nil {
			t.Fatal(healthErr)
		}
		item := mustFindStreamHealth(t, report, sourceID, "usage")
		if item.Ready != wantReady || len(item.Reasons) != len(wantReasons) {
			t.Fatalf("%s: usage stream ready=%t reasons=%v want ready=%t reasons=%v",
				phase, item.Ready, item.Reasons, wantReady, wantReasons)
		}
		for i, want := range wantReasons {
			if item.Reasons[i] != want {
				t.Fatalf("%s: usage stream reasons=%v want %v", phase, item.Reasons, wantReasons)
			}
		}
		if item.DeadEvents != 1 {
			t.Fatalf("%s: usage stream dead=%d want 1", phase, item.DeadEvents)
		}
	}

	// Phase one: contained. B carries on; A is stopped by its own freeze.
	assertUsageStream("contained", true, []string{"EVENTS_DEAD_CONTAINED"})

	lotsB, err := freshnessService.ListFundingLots(ctx, userB.ID, "")
	if err != nil || len(lotsB) != 1 {
		t.Fatalf("B lots=%+v err=%v", lotsB, err)
	}
	if lotsB[0].EligibilityStatus == "source_unavailable" {
		t.Fatalf("another account's contained dead event took B's lot out of service: %+v", lotsB[0])
	}
	summariesB, err := freshnessService.ListUserEligibilitySummaries(ctx, userB.ID, "")
	if err != nil || len(summariesB) != 1 {
		t.Fatalf("B summaries=%+v err=%v", summariesB, err)
	}
	if containsString(summariesB[0].Reasons, "SOURCE_NOT_READY") {
		t.Fatalf("B's eligibility summary still reports the source as down: %+v", summariesB[0])
	}

	lotsA, err := freshnessService.ListFundingLots(ctx, userA.ID, "")
	if err != nil || len(lotsA) != 1 {
		t.Fatalf("A lots=%+v err=%v", lotsA, err)
	}
	// A is stopped too -- but by its own freeze, not by the stream gate. The
	// distinction is the entire point of the slice, so it is asserted from
	// both sides: A's lot must not be carrying the source-level verdict, and
	// A's eligibility summary must be carrying the account-level one. Without
	// the second half this test would pass with containment doing nothing at
	// all.
	if lotsA[0].EligibilityStatus == "source_unavailable" {
		t.Fatalf("A's own contained dead event is still being reported as a source outage: %+v", lotsA[0])
	}
	summariesA, err := freshnessService.ListUserEligibilitySummaries(ctx, userA.ID, "")
	if err != nil || len(summariesA) != 1 {
		t.Fatalf("A summaries=%+v err=%v", summariesA, err)
	}
	if !containsString(summariesA[0].Reasons, "ACCOUNT_FROZEN") || summariesA[0].AvailableMinor != 0 {
		t.Fatalf("A is not stopped at the account level: %+v", summariesA[0])
	}
	if containsString(summariesB[0].Reasons, "ACCOUNT_FROZEN") {
		t.Fatalf("B was frozen by another account's dead event: %+v", summariesB[0])
	}

	// Phase two: the judgment is live. Resolving the freeze with plain SQL
	// (ResolveEligibilityFreeze itself refuses this exact transition; that
	// guard has its own test in postgresstore) must put every other account
	// back out of service, because the event is uncontained again. A surface
	// that computed containment once and stored it would pass phase one and
	// fail here.
	if _, err = pool.Exec(ctx, `
		UPDATE eligibility_freezes
		SET status='resolved',resolved_at=now(),resolved_by='90000000-0000-4000-8000-000000000002',
			resolution_evidence_hash=repeat('e',64),resolution_evidence_ciphertext=decode(repeat('11',16),'hex'),
			resolution_note_ciphertext=decode(repeat('22',16),'hex'),resolution_note_hash=repeat('f',64),
			resolution_version=2,updated_at=now()
		WHERE source_revision_hash=$1 AND status='open'`, usageHash); err != nil {
		t.Fatal(err)
	}
	assertUsageStream("freeze resolved", false, []string{"EVENTS_DEAD"})
	reopenedB, err := freshnessService.ListFundingLots(ctx, userB.ID, "")
	if err != nil || len(reopenedB) != 1 {
		t.Fatalf("B lots after resolution=%+v err=%v", reopenedB, err)
	}
	if reopenedB[0].EligibilityStatus != "source_unavailable" {
		t.Fatalf("an uncontained dead event did not take B's lot out of service again: %+v", reopenedB[0])
	}
	reopenedSummariesB, err := freshnessService.ListUserEligibilitySummaries(ctx, userB.ID, "")
	if err != nil || len(reopenedSummariesB) != 1 {
		t.Fatalf("B summaries after resolution=%+v err=%v", reopenedSummariesB, err)
	}
	if !containsString(reopenedSummariesB[0].Reasons, "SOURCE_NOT_READY") {
		t.Fatalf("B's summary did not go back to reporting the source as down: %+v", reopenedSummariesB[0])
	}
}

func containmentTestHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
