package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

func rescanGraceTestHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// TestListFundingLotsStaysActionableDuringActiveRescanAndBlocksWhenStalled is
// the application-layer proof for XM-INV-AGENT-RESTART-GRACE part A: the
// five-stream freshness gate in ListFundingLots reads stream.Ready, so an
// active, bounded rescan on one stream (proven by a source_economic_scan_cycles
// row) must not remove an otherwise healthy source from service, while a
// rescan whose row stopped updating must.
func TestListFundingLotsStaysActionableDuringActiveRescanAndBlocksWhenStalled(t *testing.T) {
	service, store, settings, ctx := integrationApplication(t)
	// integrationApplication's own service intentionally leaves source
	// freshness disabled (see its comment: "avoids fabricating five healthy
	// signed streams in non-source workflow tests"). This test is exactly
	// about that gate, so it needs a second Service against the same store
	// with freshness enabled, matching production wiring (buildProductionRuntime).
	freshnessService, err := NewService(store, testKeys(), settings, Options{
		MinimumRequestMinor: domain.MinimumRequestMinor, DownloadBaseURL: "https://invoice.example/",
		SourceEconomicHeartbeatMaxAge: 5 * time.Minute, SourceEconomicWatermarkMaxAge: 15 * time.Minute,
		SourceIdentitiesMaxAge: 15 * time.Minute, SourceEconomicRescanActivityMaxAge: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Microsecond)
	freshnessService.now = func() time.Time { return clock }

	user, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://central-id.example", Subject: "rescan-grace-subject",
		Email: "rescan-grace-user@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	const sourceID = "10000000-0000-4000-8000-000000000031"
	if _, err = store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: sourceID, SourceType: domain.SourceSub2API, Name: "sub2-rescan-grace", RuntimeVersion: "test-runtime", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if err = store.ProvisionSourceStream(ctx, sourceID, stream, postgresstore.AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000001"}); err != nil {
			t.Fatal(err)
		}
	}

	// A funding lot needs an identity binding and a payment, unrelated to the
	// five-stream readiness plumbing under test here.
	commit := func(stream, batchID, bodyHash string, sequence int64, eventID, entity string, payload []byte, payloadHash string) {
		t.Helper()
		if _, commitErr := service.CommitVerifiedSourceBatch(ctx, sourceID, VerifiedSourceBatch{
			SourceInstanceID: sourceID, StreamID: stream, BatchID: batchID,
			Sequence: sequence, BodyHash: bodyHash, PreviousBatchHash: "",
			SigningKeyID: "source-key-1", SourceRuntimeVersion: "test-runtime",
			SourceAgentVersion: "test-agent", SourceCapturedAt: clock, ProjectionStatus: "healthy",
			Events: []VerifiedSourceBatchEvent{{EventID: eventID, EntityType: entity, Operation: "upsert", PayloadHash: payloadHash, Payload: payload, ObservedAt: clock}},
		}); commitErr != nil {
			t.Fatalf("commit %s batch: %v", stream, commitErr)
		}
	}
	identityPayload := []byte(fmt.Sprintf(`{"external_user_id":"41","provider_type":"oidc","provider_key":"central","provider_subject":"rescan-grace-subject","issuer":"https://central-id.example","verified_at":%q,"user_status":"unknown","updated_at":%q}`,
		clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)))
	commit("identities", "73000000-0000-4000-8000-000000000001", rescanGraceTestHash("rescan-grace-identity"), 1,
		"73100000-0000-4000-8000-000000000001", "identity_binding", identityPayload, rescanGraceTestHash("rescan-grace-identity-payload"))
	paymentPayload := []byte(fmt.Sprintf(`{"external_order_id":"901","external_user_id":"41","status":"COMPLETED","order_type":"balance","amount":"600.00","pay_amount":"500.00","currency":"CNY","refund_amount":"0","gateway_refund_amount":"0","completed_at":%q,"created_at":%q,"updated_at":%q,"payment_type":"stripe","provider_key":"stripe","external_trade_ref_hmac":""}`,
		clock.Format(time.RFC3339Nano), clock.Add(-time.Hour).Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)))
	commit("payments", "73000000-0000-4000-8000-000000000002", rescanGraceTestHash("rescan-grace-payment"), 1,
		"73100000-0000-4000-8000-000000000002", "payment_order", paymentPayload, rescanGraceTestHash("rescan-grace-payment-payload"))

	processor := SourceEventProcessor{Service: service, BatchSize: 100, Now: func() time.Time { return clock.Add(time.Minute) }}
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	processor.Now = func() time.Time { return clock.Add(7 * time.Minute) }
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// Seed the five-stream readiness plumbing directly: this is the read
	// model evaluateSourceStreamHealth consumes (source_ingest_state,
	// source_economic_stream_watermarks, source_economic_scan_cycles), not
	// the V3 receiver/projection pipeline that normally writes it (which
	// lives partly in consumption.go and is out of scope for this agent to
	// touch or re-drive end to end here).
	pool := store.Pool()
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if _, err = pool.Exec(ctx, `UPDATE source_ingest_state SET
			source_runtime_version='test-runtime', source_agent_version='test-agent',
			projection_status='healthy', last_accepted_at=$3, last_nonempty_batch_at=$3
			WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, stream, clock); err != nil {
			t.Fatalf("seed source_ingest_state[%s]: %v", stream, err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'test-runtime','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'payment_orders:0','usage_logs:0','credits:0','balance_snapshot:0',$2,$2,0,'test-key')`,
		sourceID, rescanGraceTestHash("rescan-grace-manifest"), clock.Add(-48*time.Hour), rescanGraceTestHash("rescan-grace-config")); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "credits", "balances"} {
		if _, err = pool.Exec(ctx, `INSERT INTO source_economic_stream_watermarks(
			source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,1,$4,$5)`, sourceID, stream, clock, stream+":1", rescanGraceTestHash("rescan-grace-config")); err != nil {
			t.Fatalf("seed watermark[%s]: %v", stream, err)
		}
	}
	// usage stream: the watermark is stale (frozen since before an active
	// rescan started, exactly the production symptom), but a
	// source_economic_scan_cycles row proves work is still in progress.
	staleWatermark := clock.Add(-20 * time.Minute)
	if _, err = pool.Exec(ctx, `INSERT INTO source_economic_stream_watermarks(
		source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,'usage',$2,1,'usage_logs:1',$3)`, sourceID, staleWatermark, rescanGraceTestHash("rescan-grace-config")); err != nil {
		t.Fatal(err)
	}
	const usageCycleID = "84000000-0000-4000-8000-000000000031"
	insertUsageCycle := func(updatedAt time.Time) {
		t.Helper()
		if _, cycleErr := pool.Exec(ctx, `INSERT INTO source_economic_scan_cycles(
			source_instance_id,stream_id,scan_cycle_id,stream_watermark_at,source_cursor,
			scan_ceiling_at,scan_ceiling_cursor,first_sequence,last_sequence,final_sequence,cycle_status,updated_at)
			VALUES($1,'usage',$2::uuid,$3,'usage_logs:1',$4,'usage_logs:9999',1,1,NULL,'receiving',$5)
			ON CONFLICT(source_instance_id,stream_id,scan_cycle_id) DO UPDATE SET updated_at=EXCLUDED.updated_at`,
			sourceID, usageCycleID, staleWatermark, clock, updatedAt); cycleErr != nil {
			t.Fatal(cycleErr)
		}
	}

	// Active rescan: the cycle's updated_at is fresh (well inside the
	// configured 10-minute activity window) -- the stream must still be
	// Ready with the non-fatal ECONOMIC_RESCAN_ACTIVE reason, and the lot
	// must stay actionable.
	insertUsageCycle(clock.Add(-2 * time.Minute))
	lots, err := freshnessService.ListFundingLots(ctx, user.ID, "")
	if err != nil || len(lots) != 1 {
		t.Fatalf("lots=%+v err=%v", lots, err)
	}
	if lots[0].EligibilityStatus == "source_unavailable" {
		t.Fatalf("an active, bounded rescan made an otherwise healthy lot source_unavailable: %+v", lots[0])
	}
	report, err := store.SourceHealth(ctx, freshnessService.sourceFreshnessPolicy())
	if err != nil {
		t.Fatal(err)
	}
	usageItem := mustFindStreamHealth(t, report, sourceID, "usage")
	if !usageItem.Ready || len(usageItem.Reasons) != 1 || usageItem.Reasons[0] != "ECONOMIC_RESCAN_ACTIVE" {
		t.Fatalf("usage stream did not report the active-rescan grace: %#v", usageItem)
	}

	// Stalled rescan: the same cycle row stopped updating well beyond the
	// activity window -- the grace must not apply, and the lot must become
	// source_unavailable again.
	insertUsageCycle(clock.Add(-90 * time.Minute))
	stalledLots, err := freshnessService.ListFundingLots(ctx, user.ID, "")
	if err != nil || len(stalledLots) != 1 {
		t.Fatalf("stalled lots=%+v err=%v", stalledLots, err)
	}
	if stalledLots[0].EligibilityStatus != "source_unavailable" {
		t.Fatalf("a stalled rescan (no recent scan-cycle activity) did not block the lot: %+v", stalledLots[0])
	}
	stalledReport, err := store.SourceHealth(ctx, freshnessService.sourceFreshnessPolicy())
	if err != nil {
		t.Fatal(err)
	}
	stalledUsageItem := mustFindStreamHealth(t, stalledReport, sourceID, "usage")
	if stalledUsageItem.Ready || len(stalledUsageItem.Reasons) != 1 || stalledUsageItem.Reasons[0] != "ECONOMIC_WATERMARK_STALE" {
		t.Fatalf("stalled rescan did not report the plain, fatal ECONOMIC_WATERMARK_STALE reason: %#v", stalledUsageItem)
	}
}

func mustFindStreamHealth(t *testing.T, report postgresstore.SourceHealthReport, sourceID, streamID string) postgresstore.SourceStreamHealth {
	t.Helper()
	for _, item := range report.Items {
		if item.SourceInstanceID == sourceID && item.StreamID == streamID {
			return item
		}
	}
	t.Fatalf("stream %s/%s not found in source health report", sourceID, streamID)
	return postgresstore.SourceStreamHealth{}
}
