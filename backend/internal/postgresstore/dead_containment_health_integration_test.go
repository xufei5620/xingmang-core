package postgresstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// resolveFreezeRaw marks a freeze resolved with plain SQL, deliberately
// bypassing ResolveEligibilityFreeze. The tests below need the state change in
// order to prove the containment judgment is live rather than a value written
// once onto the ingest row -- and ResolveEligibilityFreeze now refuses exactly
// this transition while the event is still dead (that guard has its own test,
// TestResolveEligibilityFreezeRefusesWhileACorrelatedEventIsStillDead). All
// the columns are set because migration 0010's CHECK requires a resolved
// freeze to carry complete evidence.
func resolveFreezeRaw(t *testing.T, store *Store, ctx context.Context, revisionHash string) {
	t.Helper()
	command, err := store.pool.Exec(ctx, `
		UPDATE eligibility_freezes
		SET status='resolved',resolved_at=now(),resolved_by='70000000-0000-4000-8000-000000000001',
			resolution_evidence_hash=repeat('e',64),resolution_evidence_ciphertext=decode(repeat('11',16),'hex'),
			resolution_note_ciphertext=decode(repeat('22',16),'hex'),resolution_note_hash=repeat('f',64),
			resolution_version=2,updated_at=now()
		WHERE source_revision_hash=$1 AND status='open'`, revisionHash)
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 1 {
		t.Fatalf("expected exactly one open freeze on revision %s, resolved %d", revisionHash, command.RowsAffected())
	}
}

func assertStreamEvidence(t *testing.T, surface string, item SourceStreamHealth,
	wantDead, wantContained int64, wantReady bool, wantReasons []string) {
	t.Helper()
	if item.DeadEvents != wantDead || item.ContainedDeadEvents != wantContained {
		t.Fatalf("%s: dead=%d contained=%d want %d/%d", surface, item.DeadEvents, item.ContainedDeadEvents, wantDead, wantContained)
	}
	if item.Ready != wantReady {
		t.Fatalf("%s: ready=%t want %t (reasons=%v)", surface, item.Ready, wantReady, item.Reasons)
	}
	if len(item.Reasons) != len(wantReasons) {
		t.Fatalf("%s: reasons=%v want %v", surface, item.Reasons, wantReasons)
	}
	for i, want := range wantReasons {
		if item.Reasons[i] != want {
			t.Fatalf("%s: reasons=%v want %v", surface, item.Reasons, wantReasons)
		}
	}
}

// TestDeadEventContainmentIsConsultedByEveryHealthSurface is the database half
// of XM-INV-DEAD-CONTAINMENT. Four separate queries decide whether a dead
// event takes a stream away from everybody -- SourceIngestHealth,
// SourceReadinessHealth, SourceHealth and assertSourceFreshTx -- and nothing
// in the compiler makes them agree. Each is therefore asserted on its own; a
// shared helper reading one of them would prove only that the helper works.
//
// The four phases divide the work and none can stand in for another:
//
//   - Mixed (one contained, one not) catches a miscount: drop the containment
//     predicate from a surface and its contained count collapses into its dead
//     count, which is visible here and nowhere else.
//   - Contained-only catches a surface that never learned about containment.
//   - Resolved catches containment being computed once and cached: a surface
//     reading a stored flag passes both phases above and fails only here.
//   - Another account's freeze pins a semantic that is easy to "fix" by
//     accident; see its own comment.
func TestDeadEventContainmentIsConsultedByEveryHealthSurface(t *testing.T) {
	store, ctx := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := seedReadySourceStreams(t, store, ctx, now)
	const (
		sourceID   = "10000000-0000-4000-8000-000000000001"
		accountID  = "30000000-0000-4000-8000-000000000001"
		otherUser  = "20000000-0000-4000-8000-000000000002"
		otherAcct  = "30000000-0000-4000-8000-000000000002"
		batchID    = "80000000-0000-4000-8000-0000000000c0"
		containedE = "82000000-0000-4000-8000-0000000000c1"
		orphanE    = "82000000-0000-4000-8000-0000000000c2"
	)
	containedHash := strings.Repeat("b1", 32)
	orphanHash := strings.Repeat("b2", 32)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'usage',$2,1,repeat('a',64),'containment-key',0,'fixture-runtime','readiness-agent',$3,'healthy')`,
		sourceID, batchID, now); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct{ id, hash string }{{containedE, containedHash}, {orphanE, orphanHash}} {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
				payload_hash,payload_ciphertext,observed_at,processing_status,attempt_count,created_at,updated_at)
			VALUES($1,'usage',$2,$3,'usage_event','upsert',$4,decode(repeat('11',16),'hex'),$5,'dead',8,$5,$5)`,
			sourceID, event.id, batchID, event.hash, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES('61000000-0000-4000-8000-0000000000c1',$1,'EVENT_DEAD','usage_event',$2,$3)`,
		accountID, containedE, containedHash); err != nil {
		t.Fatal(err)
	}

	assertSurfaces := func(t *testing.T, phase string, wantDead, wantContained int64, wantReady bool, wantReasons []string) {
		t.Helper()
		// Surface 1: the plain ingest aggregate.
		ingest, err := store.SourceIngestHealth(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ingest.Dead != wantDead || ingest.DeadContained != wantContained {
			t.Fatalf("%s: SourceIngestHealth dead=%d contained=%d want %d/%d",
				phase, ingest.Dead, ingest.DeadContained, wantDead, wantContained)
		}
		// Surface 2: the readiness query, the one /readyz runs.
		readiness, err := store.SourceReadinessHealth(ctx, policy)
		if err != nil {
			t.Fatal(err)
		}
		if readiness.Ingest.Dead != wantDead || readiness.Ingest.DeadContained != wantContained {
			t.Fatalf("%s: readiness ingest dead=%d contained=%d want %d/%d",
				phase, readiness.Ingest.Dead, readiness.Ingest.DeadContained, wantDead, wantContained)
		}
		assertStreamEvidence(t, phase+"/SourceReadinessHealth",
			readinessItem(t, readiness.Report, sourceID, "usage"), wantDead, wantContained, wantReady, wantReasons)
		if readiness.Report.Ready != wantReady {
			t.Fatalf("%s: readiness report ready=%t want %t", phase, readiness.Report.Ready, wantReady)
		}
		// Surface 3: the management report.
		full, err := store.SourceHealth(ctx, policy)
		if err != nil {
			t.Fatal(err)
		}
		assertStreamEvidence(t, phase+"/SourceHealth", readinessItem(t, full, sourceID, "usage"),
			wantDead, wantContained, wantReady, wantReasons)
		// Surface 4: the in-transaction gate every invoice submission runs.
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		freshErr := assertSourceFreshTx(ctx, tx, sourceID, policy)
		_ = tx.Rollback(ctx)
		if wantReady && freshErr != nil {
			t.Fatalf("%s: assertSourceFreshTx rejected a contained-only source: %v", phase, freshErr)
		}
		if !wantReady && !errors.Is(freshErr, domain.ErrSourceUnavailable) {
			t.Fatalf("%s: assertSourceFreshTx err=%v want domain.ErrSourceUnavailable", phase, freshErr)
		}
	}

	assertSurfaces(t, "mixed", 2, 1, false, []string{"EVENTS_DEAD", "EVENTS_DEAD_CONTAINED"})

	if _, err := store.pool.Exec(ctx, `DELETE FROM source_ingest_events WHERE event_id=$1`, orphanE); err != nil {
		t.Fatal(err)
	}
	assertSurfaces(t, "contained only", 1, 1, true, []string{"EVENTS_DEAD_CONTAINED"})

	resolveFreezeRaw(t, store, ctx, containedHash)
	assertSurfaces(t, "freeze resolved", 1, 0, false, []string{"EVENTS_DEAD"})

	// XM-INV-DEAD-CONTAINMENT A6, pinned rather than left implicit. Under the
	// current definition an open freeze on a different account with the same
	// payload_hash also counts as containing the event, because the
	// correlation runs through the payload's own content hash and nothing
	// else. That is safe only because the hash covers a payload carrying its
	// own external_user_id, so two accounts sharing one would need
	// byte-identical payloads.
	//
	// The assertion exists because the semantic looks like a bug to a reader
	// who has not followed that argument, and "fixing" it to match on account
	// would silently disagree with tryPublishEconomicScanCyclesTx, which uses
	// the same predicate to decide whether a scan cycle may publish. Change
	// both or neither.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES($1,'test','user-2')`, otherUser); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'u2','h1:'||repeat('2',64),'test','verified')`, otherAcct, otherUser, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES('61000000-0000-4000-8000-0000000000c2',$1,'EVENT_DEAD','usage_event',$2,$3)`,
		otherAcct, containedE, containedHash); err != nil {
		t.Fatal(err)
	}
	assertSurfaces(t, "another account's freeze on the same payload hash", 1, 1, true, []string{"EVENTS_DEAD_CONTAINED"})
}

// TestContainedDeadFilterKeepsReadinessOnTheActivePartialIndex is a guardrail,
// not a regression proof: it was green before this slice too, and says so here
// so nobody mistakes it for evidence of behaviour.
//
// What it protects is the shape of the change. The containment predicate is a
// correlated subquery, computed inside the readiness query's `classified`
// subquery, whose WHERE bounds the rows to exactly the status set
// source_ingest_events_readiness_active_idx covers. Move it outward, or widen
// that status set, and /readyz starts scanning a table whose parked backlog is
// unbounded -- the failure migration 0013 and the sibling test in
// source_readiness_integration_test.go exist to prevent.
//
// It only means anything after the 20,000 parked rows and the ANALYZE: on a
// small table the planner picks a sequential scan whatever the query says.
func TestContainedDeadFilterKeepsReadinessOnTheActivePartialIndex(t *testing.T) {
	store, ctx := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := seedReadySourceStreams(t, store, ctx, now)
	const (
		sourceID = "10000000-0000-4000-8000-000000000001"
		batchID  = "80000000-0000-4000-8000-0000000000d0"
		deadE    = "82000000-0000-4000-8000-0000000000d1"
	)
	deadHash := strings.Repeat("b1", 32)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'identities',$2,1,repeat('a',64),'containment-key',0,'fixture-runtime','readiness-agent',$3,'healthy')`,
		sourceID, batchID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,dependency_kind,dependency_key_hmac,catchup_key_hmac,created_at,updated_at)
		SELECT $1,'identities',('81000000-0000-4000-8000-'||lpad(to_hex(n),12,'0'))::uuid,
			$2,'identity_binding','upsert',repeat('b',64),
			decode(repeat('11',16),'hex'),$3,'parked_identity','source_external_account',
			'h1:'||repeat('c',64),'h1:'||repeat('c',64),$3,$3
		FROM generate_series(1,20000) n`, sourceID, batchID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,attempt_count,created_at,updated_at)
		VALUES($1,'identities',$2,$3,'identity_binding','upsert',$4,decode(repeat('22',16),'hex'),$5,'dead',8,$5,$5)`,
		sourceID, deadE, batchID, deadHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES('61000000-0000-4000-8000-0000000000d1','30000000-0000-4000-8000-000000000001','EVENT_DEAD','identity_binding',$1,$2)`,
		deadE, deadHash); err != nil {
		t.Fatal(err)
	}
	// The fixture has to be doing the thing whose plan is being checked.
	health, err := store.SourceReadinessHealth(ctx, policy)
	if err != nil || health.Ingest.Dead != 1 || health.Ingest.DeadContained != 1 {
		t.Fatalf("fixture is not exercising containment: %+v err=%v", health.Ingest, err)
	}
	if _, err = store.pool.Exec(ctx, `ANALYZE source_ingest_events`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `ANALYZE eligibility_freezes`); err != nil {
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
		t.Fatalf("the contained-dead predicate pushed /readyz off the active-only index. "+
			"It must stay inside the classified subquery, whose WHERE bounds the rows to the "+
			"statuses that index covers:\n%s", plan)
	}
}
