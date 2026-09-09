package postgresstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedReadySourceStreams(t *testing.T, store *Store, ctx context.Context, now time.Time) SourceFreshnessPolicy {
	t.Helper()
	const (
		sub2ID = "10000000-0000-4000-8000-000000000001"
		newID  = "10000000-0000-4000-8000-000000000002"
	)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'newapi','readiness-newapi','fixture-runtime')`, newID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_cutover_manifests(
			source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
			projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
			credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
			baseline_row_count,signing_key_id)
		SELECT $1,repeat('d',64),cutover_at,database_clock,'fixture-runtime',
			'fixture-v3',repeat('e',64),'NEWAPI_CREDIT_1E6','p','u','c','b',
			repeat('d',64),repeat('d',64),0,'fixture'
		FROM source_cutover_manifests WHERE source_instance_id=$2`, newID, sub2ID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO source_economic_stream_watermarks(
				source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,1,$2||':1',repeat('e',64))`, newID, stream, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET runtime_version='fixture-runtime' WHERE id=$1`, sub2ID); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []string{sub2ID, newID} {
		for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
			if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "readiness-test"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.pool.Exec(ctx, `
				UPDATE source_ingest_state SET sequence=1,last_batch_hash=repeat('a',64),
					source_runtime_version='fixture-runtime',source_agent_version='readiness-agent',
					projection_status='healthy',last_accepted_at=$3,last_nonempty_batch_at=$3,updated_at=$3
				WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, stream, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	return SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: time.Hour, EconomicWatermarkMaxAge: time.Hour,
		IdentitiesMaxAge: time.Hour, Now: now,
	}
}

func TestSourceReadinessHealthIgnoresParkedBacklogAndUsesPartialIndex(t *testing.T) {
	store, ctx := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := seedReadySourceStreams(t, store, ctx, now)
	const sourceID = "10000000-0000-4000-8000-000000000001"

	baseline, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || !baseline.Report.Ready || len(baseline.Report.Items) != 10 || baseline.Ingest.Pending != 0 || baseline.Ingest.Dead != 0 {
		t.Fatalf("baseline readiness=%+v err=%v", baseline, err)
	}
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'identities','80000000-0000-4000-8000-000000000001',1,repeat('a',64),
			'readiness-key',0,'fixture-runtime','readiness-agent',$2,'healthy')`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	const parkedCount = 20000
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,dependency_kind,dependency_key_hmac,catchup_key_hmac,created_at,updated_at)
		SELECT $1,'identities',('81000000-0000-4000-8000-'||lpad(to_hex(n),12,'0'))::uuid,
			'80000000-0000-4000-8000-000000000001','identity_binding','upsert',repeat('b',64),
			decode(repeat('11',16),'hex'),$2,'parked_identity','source_external_account',
			'h1:'||repeat('c',64),'h1:'||repeat('c',64),$2,$2
		FROM generate_series(1,$3::integer) n`, sourceID, now, parkedCount); err != nil {
		t.Fatal(err)
	}

	parked, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || !parked.Report.Ready || parked.Ingest.Pending != 0 || parked.Ingest.Dead != 0 || !parked.Ingest.OldestPending.IsZero() {
		t.Fatalf("parked backlog affected readiness=%+v err=%v", parked, err)
	}
	for _, item := range parked.Report.Items {
		if item.PendingEvents != 0 || item.DeadEvents != 0 || item.WaitingDependencies != 0 {
			t.Fatalf("readiness exposed non-active dependency backlog: %+v", item)
		}
	}
	full, err := store.SourceHealth(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	var waiting int64
	for _, item := range full.Items {
		waiting += item.WaitingDependencies
	}
	if waiting != parkedCount {
		t.Fatalf("full management health waiting=%d want=%d", waiting, parkedCount)
	}

	pendingAt := now.Add(-time.Minute)
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,created_at,updated_at)
		VALUES($1,'identities','82000000-0000-4000-8000-000000000001',
			'80000000-0000-4000-8000-000000000001','identity_binding','upsert',repeat('f',64),
			decode(repeat('22',16),'hex'),$2,'queued',$2,$2)`, sourceID, pendingAt); err != nil {
		t.Fatal(err)
	}
	pending, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || pending.Report.Ready || pending.Ingest.Pending != 1 || pending.Ingest.Dead != 0 || !pending.Ingest.OldestPending.Equal(pendingAt) {
		t.Fatalf("pending readiness=%+v err=%v", pending, err)
	}
	pendingItem := readinessItem(t, pending.Report, sourceID, "identities")
	if pendingItem.PendingEvents != 1 || pendingItem.DeadEvents != 0 || len(pendingItem.Reasons) != 1 || pendingItem.Reasons[0] != "EVENTS_PENDING" {
		t.Fatalf("pending-only evidence=%+v", pendingItem)
	}

	if _, err = store.pool.Exec(ctx, `ANALYZE source_ingest_events`); err != nil {
		t.Fatal(err)
	}
	planRows, err := store.pool.Query(ctx, "EXPLAIN (COSTS OFF) "+sourceReadinessHealthQuery)
	if err != nil {
		t.Fatal(err)
	}
	var planLines []string
	for planRows.Next() {
		var line string
		if err = planRows.Scan(&line); err != nil {
			planRows.Close()
			t.Fatal(err)
		}
		planLines = append(planLines, line)
	}
	if err = planRows.Err(); err != nil {
		planRows.Close()
		t.Fatal(err)
	}
	planRows.Close()
	plan := strings.Join(planLines, "\n")
	if !strings.Contains(plan, "source_ingest_events_readiness_active_idx") ||
		strings.Contains(strings.ToLower(plan), "seq scan on source_ingest_events") {
		t.Fatalf("readiness EXPLAIN lost active-only index path:\n%s", plan)
	}

	if _, err = store.pool.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='dead',attempt_count=8,updated_at=$2
		WHERE source_instance_id=$1 AND stream_id='identities' AND event_id='82000000-0000-4000-8000-000000000001'`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	dead, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || dead.Ingest.Pending != 0 || dead.Ingest.Dead != 1 || !dead.Ingest.OldestPending.IsZero() {
		t.Fatalf("dead readiness=%+v err=%v", dead, err)
	}
	deadItem := readinessItem(t, dead.Report, sourceID, "identities")
	if deadItem.DeadEvents != 1 || !containsReason(deadItem.Reasons, "EVENTS_DEAD") {
		t.Fatalf("dead event did not block readiness: %+v", deadItem)
	}

	staleAt := now.Add(-2 * time.Hour)
	if _, err = store.pool.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processed_at=$2,updated_at=$2
		WHERE source_instance_id=$1 AND stream_id='identities' AND event_id='82000000-0000-4000-8000-000000000001'`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `
		UPDATE source_ingest_state SET last_accepted_at=$2,last_nonempty_batch_at=$2,updated_at=$3
		WHERE source_instance_id=$1 AND stream_id='identities'`, sourceID, staleAt, now); err != nil {
		t.Fatal(err)
	}
	stale, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	staleItem := readinessItem(t, stale.Report, sourceID, "identities")
	if staleItem.Ready || !containsReason(staleItem.Reasons, "STREAM_STALE") {
		t.Fatalf("stale stream did not block readiness: %+v", staleItem)
	}
}

func readinessItem(t *testing.T, report SourceHealthReport, sourceID, streamID string) SourceStreamHealth {
	t.Helper()
	for _, item := range report.Items {
		if item.SourceInstanceID == sourceID && item.StreamID == streamID {
			return item
		}
	}
	t.Fatalf("missing readiness item %s/%s", sourceID, streamID)
	return SourceStreamHealth{}
}

// XM-INV-CATCHUP-BURST-BACKPRESSURE fix 2. An event requeued with
// ACCOUNT_LOCK_BUSY is waiting fifteen seconds for an account's projection
// to release its lock; counting it as pending turned readiness off within a
// minute of the first busy retry on 2026-09-04. It is tolerated for a bounded
// grace, then counted again so a genuinely stuck stream still fails.
func TestSourceReadinessHealthGivesAnAccountLockBusyEventABoundedGrace(t *testing.T) {
	store, ctx := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := seedReadySourceStreams(t, store, ctx, now)
	const sourceID = "10000000-0000-4000-8000-000000000001"
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'balances','80000000-0000-4000-8000-0000000000b1',1,repeat('b',64),
			'readiness-key',0,'fixture-runtime','readiness-agent',$2,'healthy')`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,processing_error,next_attempt_at,created_at,updated_at)
		VALUES($1,'balances','82000000-0000-4000-8000-0000000000b1',
			'80000000-0000-4000-8000-0000000000b1','balance_checkpoint','upsert',repeat('c',64),
			decode(repeat('33',16),'hex'),$2,'queued','ACCOUNT_LOCK_BUSY',$3,$2,$2)`, sourceID, now, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Busy for a moment: not pending, still ready.
	fresh, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || !fresh.Report.Ready || fresh.Ingest.Pending != 0 {
		t.Fatalf("a freshly busy event must not count as pending: ready=%v pending=%d err=%v", fresh.Report.Ready, fresh.Ingest.Pending, err)
	}
	// Busy for longer than the grace: pending again, readiness off.
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_events SET updated_at=$2 WHERE event_id=$1`,
		"82000000-0000-4000-8000-0000000000b1", now.Add(-11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stale, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || stale.Report.Ready || stale.Ingest.Pending != 1 {
		t.Fatalf("an event busy past the grace must count as pending: ready=%v pending=%d err=%v", stale.Report.Ready, stale.Ingest.Pending, err)
	}
	// And an ordinary queued event (no busy marker) is pending at once, as before.
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_events SET processing_error=NULL,updated_at=$2 WHERE event_id=$1`,
		"82000000-0000-4000-8000-0000000000b1", now); err != nil {
		t.Fatal(err)
	}
	plain, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || plain.Report.Ready || plain.Ingest.Pending != 1 {
		t.Fatalf("a plain queued event must still count as pending: ready=%v pending=%d err=%v", plain.Report.Ready, plain.Ingest.Pending, err)
	}
}
