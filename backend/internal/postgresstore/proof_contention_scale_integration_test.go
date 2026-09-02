package postgresstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestBalanceCarryForwardProofScalesToOneThousandCheckpoints builds a
// fixture shaped like the XM-INV-PROOF-CONTENTION production incident: one
// account with 1,000 distinct usage-fact visibilities, each covered by its
// own tiny published balances cycle (no cycle covers more than one
// visibility, so the original per-visibility loop's jump-ahead skip never
// applied and it issued a real query for every one of them -- the
// worst case that made a single processEligibilityProjectionJob attempt
// take "tens of seconds", per the incident, while holding the account's
// advisory lock the whole time).
//
// It proves two things: (1) the set-based rewrite (requirement 4) still
// evaluates all 1,000 correctly (every visibility gets its own
// carry-forward proof row, none left pending) in one call; (2) it captures
// EXPLAIN ANALYZE for the new prefetch query against the same data the old
// per-visibility query would have run against 1,000 times, so the two can
// be compared directly. Both plans are written to the test log
// (go test -v) and reproduced in docs/handoffs/XM-INV-PROOF-CONTENTION.md.
func TestBalanceCarryForwardProofScalesToOneThousandCheckpoints(t *testing.T) {
	const checkpointCount = 1000
	store, ctx := integrationStore(t)
	const (
		sourceID  = "16000000-0000-4000-8000-000000000001"
		userID    = "26000000-0000-4000-8000-000000000001"
		accountID = "36000000-0000-4000-8000-000000000001"
	)
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	requested := cutover.Add(time.Duration(checkpointCount+5) * time.Minute)
	manifestHash := testHash("scale-manifest")
	configHash := testHash("scale-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','scale-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','scale-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'scale-user','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, sourceID, manifestHash,
		cutover, configHash, testHash("scale-baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`, accountID, sourceID,
		cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances",
		AuditActor{Type: "system", ID: "scale-test"}); err != nil {
		t.Fatal(err)
	}
	// One real checkpoint, dated before every cycle below, gives the
	// LATERAL "prior" join in the delta-carry query something to find for
	// every one of the 1,000 cycles (an INNER LATERAL JOIN, so a cycle with
	// no prior checkpoint at all would be dropped from the candidate list
	// entirely -- unrepresentative of a real, running account).
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
		as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,
		configuration_hash,reconciliation_status,source_sequence,source_cursor,
		stream_watermark_at,source_revision_hash,observed_at)
		VALUES('46000000-0000-4000-8000-000000000000',$1,$2,'scale-opening-event','scale-opening',
		'reconciliation',TRUE,$3,1,$4,0,FALSE,'SUB2_BALANCE_1E8',$5,$6,'cutover_baseline',
		1,'scale-opening:0',$4,$7,$4)`, sourceID, accountID, testHash("scale-baseline"),
		cutover, manifestHash, configHash, testHash("scale-opening-revision")); err != nil {
		t.Fatal(err)
	}

	t.Logf("seeding %d usage facts + %d tiny published balances cycles (one visibility each, no jump-ahead)...", checkpointCount, checkpointCount)
	seedStart := time.Now()
	chain := newV3TestChain()
	for i := 0; i < checkpointCount; i++ {
		visibility := cutover.Add(time.Duration(i+1) * time.Minute)
		invoiceEligible := !visibility.Before(policyStart)
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$4,$5,1,'SUB2_BALANCE_1E8',$6,$7,'wallet',$8,$9,$4,$5,$10,$5)`,
			fmt.Sprintf("66000000-0000-4000-8000-%012d", i+1), sourceID, accountID,
			fmt.Sprintf("scale-usage-%d", i+1), visibility, manifestHash, configHash, invoiceEligible, i+1,
			testHash(fmt.Sprintf("scale-usage-revision-%d", i+1))); err != nil {
			t.Fatalf("seed usage event %d: %v", i, err)
		}
		cycleID := fmt.Sprintf("76%06d-0000-4000-8000-%012d", i+1, i+1)
		event := SourceBatchEvent{
			EventID:           fmt.Sprintf("86%06d-0000-4000-8000-%012d", i+1, i+1),
			EntityType:        "balance_checkpoint",
			Operation:         "upsert",
			PayloadHash:       testHash(fmt.Sprintf("scale-cycle-payload-%d", i+1)),
			PayloadCiphertext: []byte("scale-cycle-ciphertext"),
			ObservedAt:        visibility,
		}
		cycle := chain.commit(t, store, ctx, sourceID, "balances", cycleID, visibility, []SourceBatchEvent{event})
		markV3CycleProcessed(t, store, ctx, sourceID, "balances", cycle)
	}
	t.Logf("seed complete in %s", time.Since(seedStart))

	var cycleCount, checkpointRowCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='balances' AND cycle_status='published'`,
		sourceID).Scan(&cycleCount); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_reconciliation_checkpoints
		WHERE source_instance_id=$1`, sourceID).Scan(&checkpointRowCount); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture built: %d published balances cycles, %d balance_reconciliation_checkpoints rows", cycleCount, checkpointRowCount)
	if cycleCount != checkpointCount {
		t.Fatalf("published cycle count=%d, want %d", cycleCount, checkpointCount)
	}

	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_projection_jobs(
		external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now()-interval '1 second')`, accountID, requested); err != nil {
		t.Fatal(err)
	}

	// --- Correctness at scale: the set-based rewrite under test ---
	runStart := time.Now()
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC(),
		AuditActor{Type: "system", ID: "scale-test-worker"})
	runElapsed := time.Since(runStart)
	if err != nil {
		t.Fatalf("ProcessEligibilityProjectionJobs err=%v", err)
	}
	if processed != 1 {
		t.Fatalf("processed=%d, want 1 (all %d visibilities must resolve, none left pending)", processed, checkpointCount)
	}
	t.Logf("ProcessEligibilityProjectionJobs (set-based evaluation, %d visibilities) completed in %s", checkpointCount, runElapsed)
	var proofCount int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_carry_forward_proofs
		WHERE external_account_id=$1`, accountID).Scan(&proofCount); err != nil {
		t.Fatal(err)
	}
	if proofCount != checkpointCount {
		t.Fatalf("balance_carry_forward_proofs count=%d, want %d (one per visibility, none covered by a jump-ahead)", proofCount, checkpointCount)
	}
	var finalizedThrough time.Time
	if err = store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&finalizedThrough); err != nil {
		t.Fatal(err)
	}
	if !finalizedThrough.Equal(requested.UTC()) {
		t.Fatalf("finalized_through=%s, want %s (job must fully complete, not just avoid erroring)", finalizedThrough, requested)
	}

	// --- EXPLAIN ANALYZE: new prefetch query (one call covers all 1,000) ---
	explainPrefetch(t, store, ctx, "NEW delta-carry prefetch (requirement 4, one call for the whole window)", `
		EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
		SELECT cycle.scan_cycle_id::text,batch.batch_id::text,cycle.scan_snapshot_id,
			cycle.scan_snapshot_row_count,cycle.scan_ceiling_at,cycle.stream_watermark_at,
			cycle.source_cursor,cycle.final_sequence,batch.body_hash,batch.source_captured_at,
			prior.id::text,prior.balance_service_units::text,prior.balance_negative,prior.baseline_member,
			EXISTS (
				SELECT 1 FROM balance_reconciliation_checkpoints checkpoint
				JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=checkpoint.source_instance_id
				 AND mapped.stream_id='balances'
				 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
				 AND mapped.payload_hash=checkpoint.source_revision_hash
				WHERE checkpoint.external_account_id=$1
				  AND mapped.scan_cycle_id=cycle.scan_cycle_id
			) AS has_real_checkpoint
		FROM source_economic_scan_cycles cycle
		JOIN source_ingest_batches batch
		  ON batch.source_instance_id=cycle.source_instance_id
		 AND batch.stream_id=cycle.stream_id
		 AND batch.scan_cycle_id=cycle.scan_cycle_id
		 AND batch.sequence=cycle.final_sequence
		JOIN LATERAL (
			SELECT checkpoint.id,checkpoint.balance_service_units,
				checkpoint.balance_negative,checkpoint.baseline_member
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<cycle.scan_ceiling_at
			       OR (checkpoint.as_of=cycle.scan_ceiling_at
			           AND checkpoint.source_sequence<cycle.final_sequence))
			ORDER BY checkpoint.as_of DESC,checkpoint.source_sequence DESC,checkpoint.id DESC LIMIT 1
		) prior ON true
		WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
		  AND cycle.cycle_status='published'
		  AND cycle.scan_ceiling_at>=$3 AND cycle.scan_ceiling_at<=$4
		ORDER BY cycle.scan_ceiling_at,cycle.first_sequence`,
		accountID, sourceID, cutover, requested.UTC())

	// --- EXPLAIN ANALYZE: old per-visibility query, one representative call ---
	// This is the exact query text ensureBalanceCarryForwardProofTx issued
	// once per not-yet-covered visibility before requirement 4 -- bound to
	// one specific visibility (the 500th, arbitrarily) the way the original
	// loop would have called it on one iteration. The incident ran this
	// (or its real-checkpoint counterpart) up to ~1,150 times per attempt;
	// multiply this plan's own cost by however many of the 1,000
	// visibilities aren't skipped by the jump-ahead to see why.
	midVisibility := cutover.Add(time.Duration(checkpointCount/2) * time.Minute)
	explainPrefetch(t, store, ctx, "OLD per-visibility query (pre-requirement-4, one representative call of up to 1,000 per attempt)", `
		EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
		SELECT cycle.scan_cycle_id::text,batch.batch_id::text,cycle.scan_snapshot_id,
			cycle.scan_snapshot_row_count,cycle.scan_ceiling_at,cycle.stream_watermark_at,
			cycle.source_cursor,cycle.final_sequence,batch.body_hash,batch.source_captured_at,
			prior.id::text,prior.balance_service_units::text,prior.balance_negative,prior.baseline_member,
			EXISTS (
				SELECT 1 FROM balance_reconciliation_checkpoints checkpoint
				JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=checkpoint.source_instance_id
				 AND mapped.stream_id='balances'
				 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
				 AND mapped.payload_hash=checkpoint.source_revision_hash
				WHERE checkpoint.external_account_id=$1
				  AND mapped.scan_cycle_id=cycle.scan_cycle_id
			) AS has_real_checkpoint
		FROM source_economic_scan_cycles cycle
		JOIN source_ingest_batches batch
		  ON batch.source_instance_id=cycle.source_instance_id
		 AND batch.stream_id=cycle.stream_id
		 AND batch.scan_cycle_id=cycle.scan_cycle_id
		 AND batch.sequence=cycle.final_sequence
		JOIN LATERAL (
			SELECT checkpoint.id,checkpoint.balance_service_units,
				checkpoint.balance_negative,checkpoint.baseline_member
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<cycle.scan_ceiling_at
			       OR (checkpoint.as_of=cycle.scan_ceiling_at
			           AND checkpoint.source_sequence<cycle.final_sequence))
			ORDER BY checkpoint.as_of DESC,checkpoint.source_sequence DESC,checkpoint.id DESC LIMIT 1
		) prior ON true
		WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
		  AND cycle.cycle_status='published'
		  AND cycle.scan_ceiling_at>=$3 AND cycle.scan_ceiling_at<=$4
		ORDER BY cycle.scan_ceiling_at,cycle.first_sequence LIMIT 1`,
		accountID, sourceID, midVisibility, requested.UTC())
}

func explainPrefetch(t *testing.T, store *Store, ctx context.Context, label, sql string, args ...any) {
	t.Helper()
	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("%s: EXPLAIN ANALYZE failed: %v", label, err)
	}
	defer rows.Close()
	t.Logf("--- %s ---", label)
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		t.Log(line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
