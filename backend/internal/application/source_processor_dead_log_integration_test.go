package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

// TestSourceProjectionWorkerLogsAnErrorOnlyWhenTheEventActuallyDies is the
// wiring proof for XM-INV-READYZ-DETAIL's second half. The unit tests next
// door show the two log helpers differ in level; only this one shows that
// RunOnce calls the right one at the right moment, because the grade is
// decided inside MarkSourceEventFailed's SQL (processing_status=CASE WHEN
// attempt_count>=8 ...) and nothing above the database can predict it.
//
// The fixture is the deterministic unit_code mismatch used by
// TestDeadUsageEventWithoutPersistedFactStillFreezesViaApplicationLayerAccountHint:
// it fails identically on every attempt, so the only thing that changes
// between the two RunOnce calls below is the attempt count -- which is
// exactly the variable under test.
//
// The first half is an absence assertion (no error-level line while the event
// is merely retrying) and doubles as the control for the second: it proves
// the fixture really does reach the failure path, so the second half's
// success cannot be explained by the event never failing at all.
func TestSourceProjectionWorkerLogsAnErrorOnlyWhenTheEventActuallyDies(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)
	sourceID := "10000000-0000-4000-8000-000000000081"
	userID := "20000000-0000-4000-8000-000000000081"
	accountID := "30000000-0000-4000-8000-000000000081"
	if _, err := store.Pool().Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','dead-event-log','log-v3')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'https://id.example','dead-event-log')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'81',$4,'test','verified')`, accountID, userID, sourceID,
		"h1:"+strings.Repeat("8", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "usage",
		postgresstore.AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	causalOrder := "1"
	payload, err := json.Marshal(usageEventPayload{
		ExternalUserID: "81", ExternalUsageID: "usage-dead-81", OccurredAt: now.Format(time.RFC3339Nano),
		ServiceUnits: "10", UnitCode: "WRONG_UNIT_CODE", BillingScope: "wallet",
		SourceCursor: "usage:1", CausalDomain: "usage_event", CausalOrder: &causalOrder,
		CutoverManifestHash: strings.Repeat("a", 64), ConfigurationHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	payloadSum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(payloadSum[:])
	eventID := "81000000-0000-4000-8000-000000000001"
	batchID := "81000000-0000-4000-8000-000000000002"
	cycleID := "81000000-0000-4000-8000-000000000003"
	ciphertext, err := service.keys.Encrypt(payload, ingestEventAAD(sourceID, "usage", eventID, payloadHash))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "usage",
		BatchID: batchID, Sequence: 1, BodyHash: strings.Repeat("d", 64), SigningKeyID: "log-key",
		SourceRuntimeVersion: "log-v3", SourceAgentVersion: "log-agent", SourceCapturedAt: now,
		ProjectionStatus: "healthy", StreamWatermarkAt: now, SourceCursor: "usage:1",
		ScanCeilingAt: now, ScanCeilingCursor: "usage-ceiling:81", ScanCycleID: cycleID,
		ScanComplete: true,
		Events: []postgresstore.SourceBatchEvent{{EventID: eventID, EntityType: "usage_event",
			Operation: "upsert", PayloadHash: payloadHash, PayloadCiphertext: ciphertext, ObservedAt: now}},
		Actor: postgresstore.AuditActor{Type: "source_connector", ID: sourceID},
	}); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	processor := SourceEventProcessor{Service: service, BatchSize: 10,
		Now:    func() time.Time { return now.Add(time.Minute) },
		Logger: slog.New(slog.NewTextHandler(&logs, nil))}

	processed, err := processor.RunOnce(ctx)
	if err != nil || processed != 1 {
		t.Fatalf("first attempt processed=%d err=%v", processed, err)
	}
	var status string
	var attempts int
	if err = store.Pool().QueryRow(ctx, `SELECT processing_status,attempt_count FROM source_ingest_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		sourceID, eventID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != postgresstore.SourceEventFailed || attempts != 1 {
		t.Fatalf("first attempt status=%s attempts=%d, want failed/1", status, attempts)
	}
	retrying := logs.String()
	if !strings.Contains(retrying, `msg="source event projection failed"`) {
		t.Fatalf("the retry itself was not logged, so the absence check below would be vacuous: %s", retrying)
	}
	if strings.Contains(retrying, "level=ERROR") {
		t.Fatalf("a retrying event produced an error-level line; that would make the terminal one meaningless: %s", retrying)
	}
	if strings.Contains(retrying, "source event marked dead") {
		t.Fatalf("an event still on attempt 1 of 8 was announced as dead: %s", retrying)
	}

	// Fast-forward to the eighth attempt, as the sibling dead-event test
	// does: the failure is deterministic, so only the transition is
	// interesting.
	if _, err = store.Pool().Exec(ctx, `UPDATE source_ingest_events SET attempt_count=7,next_attempt_at=now()-interval '1 second'
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`, sourceID, eventID); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if processed, err = processor.RunOnce(ctx); err != nil || processed != 1 {
		t.Fatalf("eighth attempt processed=%d err=%v", processed, err)
	}
	if err = store.Pool().QueryRow(ctx, `SELECT processing_status,attempt_count FROM source_ingest_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		sourceID, eventID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != postgresstore.SourceEventDead || attempts != 8 {
		t.Fatalf("eighth attempt status=%s attempts=%d, want dead/8", status, attempts)
	}
	died := logs.String()
	if count := strings.Count(died, `msg="source event marked dead"`); count != 1 {
		t.Fatalf("the dead transition produced %d announcements, want exactly 1: %s", count, died)
	}
	if !strings.Contains(died, "level=ERROR") {
		t.Fatalf("the dead transition produced no error-level line, so an error-level watch still misses it: %s", died)
	}
	if !strings.Contains(died, "event_id="+eventID) {
		t.Fatalf("the dead line does not identify the event: %s", died)
	}
	// The retry line is still written on this attempt too (it is logged
	// before the mark, when the grade is not yet known). That is intended:
	// the two lines are a pair, and the Error one is the new signal.
	if !strings.Contains(died, `msg="source event projection failed"`) {
		t.Fatalf("the pre-existing failure line was lost: %s", died)
	}
}
