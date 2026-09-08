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
	// Absence assertion, mutation-verified: a retry must not write the
	// terminal audit event either, or the row would stop meaning "this event
	// died" and start meaning "this event failed once".
	var retryAudits int
	if err = store.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='source_ingest_event.dead' AND object_id=$1`, eventID).Scan(&retryAudits); err != nil {
		t.Fatal(err)
	}
	if retryAudits != 0 {
		t.Fatalf("a retrying event wrote %d source_ingest_event.dead audit rows, want 0", retryAudits)
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
	// claim.Attempt is the pre-claim attempt_count plus one, incremented in
	// lockstep with the row's own attempt_count during the claim. Asserting
	// it against the attempt_count read back from the database above is what
	// proves the number in the log line is the real attempt number rather
	// than something that merely looks plausible.
	if !strings.Contains(died, "attempt=8") {
		t.Fatalf("the dead line's attempt does not match the row's attempt_count=%d: %s", attempts, died)
	}
	// The retry line is still written on this attempt too (it is logged
	// before the mark, when the grade is not yet known). That is intended:
	// the two lines are a pair, and the Error one is the new signal.
	if !strings.Contains(died, `msg="source event projection failed"`) {
		t.Fatalf("the pre-existing failure line was lost: %s", died)
	}

	// The durable half. Shaped after eligibility.projection.dead's own
	// assertion in postgresstore/projection_failure_grading_integration_test.go
	// -- exactly one row, on the object the grade was applied to. The log
	// answers "tell me now"; this answers "what happened three weeks ago",
	// after the container's logs have rotated.
	var deadAudits int
	var actorType, actorID, objectType, reason, afterHash string
	if err = store.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='source_ingest_event.dead' AND object_id=$1`, eventID).Scan(&deadAudits); err != nil {
		t.Fatal(err)
	}
	if deadAudits != 1 {
		t.Fatalf("source_ingest_event.dead audit rows=%d, want exactly 1", deadAudits)
	}
	if err = store.Pool().QueryRow(ctx, `SELECT actor_type,actor_id,object_type,reason,COALESCE(after_hash,'')
		FROM audit_events WHERE action='source_ingest_event.dead' AND object_id=$1`,
		eventID).Scan(&actorType, &actorID, &objectType, &reason, &afterHash); err != nil {
		t.Fatal(err)
	}
	if actorType != "source_connector" || actorID != sourceID {
		t.Fatalf("audit actor=%s/%s, want source_connector/%s", actorType, actorID, sourceID)
	}
	if objectType != "source_ingest_event" {
		t.Fatalf("audit object_type=%q, want source_ingest_event (matching source_ingest_event.repair_requeued)", objectType)
	}
	if reason != "source event reached the terminal dead grade" {
		t.Fatalf("audit reason=%q, want the exact terminal-grade wording", reason)
	}
	// writeAudit hashes the payload map into after_hash, so the fields
	// themselves are not readable back; a non-empty hash is what proves a
	// payload was supplied rather than nil.
	if afterHash == "" {
		t.Fatal("audit event carries no after payload, so attempts/threshold/error were never recorded")
	}
}

// TestSourceEventDeadThresholdGovernsBothTheGradeAndTheClaimPredicate proves
// the two queries that used to spell the literal 8 independently now move
// together. If the claim predicate were below the grade threshold the event
// would stop being re-claimed before it could ever be graded dead -- readiness
// would stay green over a permanently stuck event, the exact opposite of the
// bug this slice exists for -- so this walks an event all the way to the
// boundary rather than asserting on the constant.
func TestSourceEventDeadThresholdGovernsBothTheGradeAndTheClaimPredicate(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)
	sourceID := "10000000-0000-4000-8000-000000000082"
	userID := "20000000-0000-4000-8000-000000000082"
	accountID := "30000000-0000-4000-8000-000000000082"
	if _, err := store.Pool().Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','dead-threshold','thr-v3')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'https://id.example','dead-threshold')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'82',$4,'test','verified')`, accountID, userID, sourceID,
		"h1:"+strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "usage",
		postgresstore.AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	causalOrder := "1"
	payload, err := json.Marshal(usageEventPayload{
		ExternalUserID: "82", ExternalUsageID: "usage-dead-82", OccurredAt: now.Format(time.RFC3339Nano),
		ServiceUnits: "10", UnitCode: "WRONG_UNIT_CODE", BillingScope: "wallet",
		SourceCursor: "usage:1", CausalDomain: "usage_event", CausalOrder: &causalOrder,
		CutoverManifestHash: strings.Repeat("a", 64), ConfigurationHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	payloadSum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(payloadSum[:])
	eventID := "82000000-0000-4000-8000-000000000001"
	ciphertext, err := service.keys.Encrypt(payload, ingestEventAAD(sourceID, "usage", eventID, payloadHash))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "usage",
		BatchID: "82000000-0000-4000-8000-000000000002", Sequence: 1,
		BodyHash: strings.Repeat("d", 64), SigningKeyID: "thr-key",
		SourceRuntimeVersion: "thr-v3", SourceAgentVersion: "thr-agent", SourceCapturedAt: now,
		ProjectionStatus: "healthy", StreamWatermarkAt: now, SourceCursor: "usage:1",
		ScanCeilingAt: now, ScanCeilingCursor: "usage-ceiling:82",
		ScanCycleID: "82000000-0000-4000-8000-000000000003", ScanComplete: true,
		Events: []postgresstore.SourceBatchEvent{{EventID: eventID, EntityType: "usage_event",
			Operation: "upsert", PayloadHash: payloadHash, PayloadCiphertext: ciphertext, ObservedAt: now}},
		Actor: postgresstore.AuditActor{Type: "source_connector", ID: sourceID},
	}); err != nil {
		t.Fatal(err)
	}

	processor := SourceEventProcessor{Service: service, BatchSize: 10,
		Now: func() time.Time { return now.Add(time.Minute) }}
	// Run the real loop for every attempt, clearing only the backoff between
	// them. No fast-forwarding here: the point is that the claim predicate
	// keeps handing the event back for exactly as many attempts as the grade
	// needs, and skipping attempts would skip the thing under test.
	for attempt := 1; attempt <= 8; attempt++ {
		processed, runErr := processor.RunOnce(ctx)
		if runErr != nil || processed != 1 {
			t.Fatalf("attempt %d: processed=%d err=%v (the claim predicate stopped re-claiming before the grade could apply)",
				attempt, processed, runErr)
		}
		var status string
		var attempts int
		if err = store.Pool().QueryRow(ctx, `SELECT processing_status,attempt_count FROM source_ingest_events
			WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
			sourceID, eventID).Scan(&status, &attempts); err != nil {
			t.Fatal(err)
		}
		if attempts != attempt {
			t.Fatalf("attempt %d: attempt_count=%d", attempt, attempts)
		}
		wantStatus := postgresstore.SourceEventFailed
		if attempt == 8 {
			wantStatus = postgresstore.SourceEventDead
		}
		if status != wantStatus {
			t.Fatalf("attempt %d of 8: status=%s want %s", attempt, status, wantStatus)
		}
		if _, err = store.Pool().Exec(ctx, `UPDATE source_ingest_events SET next_attempt_at=now()-interval '1 second'
			WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`, sourceID, eventID); err != nil {
			t.Fatal(err)
		}
	}
	// Dead rows are excluded by the claim predicate, so the ninth pass must
	// find nothing -- the other half of the same coupling.
	processed, runErr := processor.RunOnce(ctx)
	if runErr != nil || processed != 0 {
		t.Fatalf("a dead event was re-claimed: processed=%d err=%v", processed, runErr)
	}
	var deadAudits int
	if err = store.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='source_ingest_event.dead' AND object_id=$1`, eventID).Scan(&deadAudits); err != nil {
		t.Fatal(err)
	}
	if deadAudits != 1 {
		t.Fatalf("eight real attempts wrote %d dead audit rows, want exactly 1", deadAudits)
	}
}
