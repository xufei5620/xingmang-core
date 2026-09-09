package postgresstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"invoice-system/backend/internal/domain"
)

// seedGuardDeadEvent inserts a batch and one dead usage event whose
// payload_hash is the fixture freeze's source_revision_hash, so the freeze is
// the only thing containing it.
func seedGuardDeadEvent(t *testing.T, store *Store, ctx context.Context, f eligibilityOpsFixture) string {
	t.Helper()
	const (
		batchID = "80000000-0000-4000-8000-0000000000e0"
		eventID = "82000000-0000-4000-8000-0000000000e1"
	)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'usage',$2,1,repeat('a',64),'guard-key',0,
			(SELECT runtime_version FROM source_instances WHERE id=$1),'guard-agent',now(),'healthy')`,
		f.sourceID, batchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,attempt_count,created_at,updated_at)
		VALUES($1,'usage',$2,$3,'usage_event','upsert',$4,decode(repeat('11',16),'hex'),now(),'dead',8,now(),now())`,
		f.sourceID, eventID, batchID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	return eventID
}

// TestResolveEligibilityFreezeRefusesWhileACorrelatedEventIsStillDead is the
// other half of XM-INV-DEAD-CONTAINMENT: the slice makes a stream survive a
// contained dead event, so nothing but this guard stops one "tidy up the
// freeze queue" resolution from turning that contained event back into an
// uncontained one and taking the source instance away from every account
// again.
//
// Before the slice the ordering held by accident: a dead event made every
// stream not-ready, so assertSourceFreshTx rejected the resolution with
// ErrEligibilitySourceStale long before this point. That accident is exactly
// what the slice removes on purpose, which is why the rule now has to be
// stated rather than relied on.
func TestResolveEligibilityFreezeRefusesWhileACorrelatedEventIsStillDead(t *testing.T) {
	// The control arm has to run first and on its own store. Without it every
	// "returns the new sentinel" assertion below could be satisfied for a
	// reason that has nothing to do with the guard -- the same fixture can
	// legitimately produce ErrEligibilityRefundExposed,
	// ErrEligibilityProjectionPending or ErrEligibilityEvaluationUnmatched,
	// and a fixture that simply cannot be resolved proves nothing.
	t.Run("control: the fixture freeze resolves cleanly with no dead event", func(t *testing.T) {
		store, ctx := integrationStore(t)
		f := seedEligibilityOpsFixture(t, store, ctx)
		resolved, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f))
		if err != nil {
			t.Fatalf("control arm cannot resolve, so every guard assertion below would be vacuous: %v", err)
		}
		if resolved.Status != "resolved" || resolved.ResolutionVersion != 2 {
			t.Fatalf("control resolution=%+v", resolved)
		}
	})

	t.Run("a dead event on the freeze's revision blocks resolution", func(t *testing.T) {
		store, ctx := integrationStore(t)
		f := seedEligibilityOpsFixture(t, store, ctx)
		seedGuardDeadEvent(t, store, ctx, f)

		_, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f))
		if !errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) {
			t.Fatalf("err=%v want domain.ErrEligibilityDeadEventUnrepaired", err)
		}
		// It wraps the generic sentinel a caller may already check, and it is
		// not the stale-source family: those two report different HTTP codes
		// and mean different repairs.
		if !errors.Is(err, domain.ErrInvalidState) || errors.Is(err, domain.ErrSourceUnavailable) {
			t.Fatalf("sentinel family is wrong: %v", err)
		}
		// Spelled out rather than compared against the variable: this text is
		// what an operator reads in the 409 body, and a test that reuses the
		// variable would follow any edit to it silently.
		const want = "invalid request state transition: a dead source event still correlates to this freeze; requeue or acknowledge it first"
		if err.Error() != want {
			t.Fatalf("message=%q want %q", err.Error(), want)
		}

		// A refusal must leave nothing behind.
		var status string
		var version int64
		var evidence []byte
		if err = store.pool.QueryRow(ctx, `SELECT status,resolution_version,resolution_evidence_ciphertext
			FROM eligibility_freezes WHERE id=$1`, f.freezeID).Scan(&status, &version, &evidence); err != nil {
			t.Fatal(err)
		}
		if status != "open" || version != 1 || evidence != nil {
			t.Fatalf("refused resolution still wrote: status=%s version=%d evidence=%v", status, version, evidence)
		}
		var audits int64
		if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE action='eligibility.freeze.resolved' AND object_id=$1`, f.freezeID).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if audits != 0 {
			t.Fatalf("refused resolution wrote %d audit events", audits)
		}
	})

	// The guard correlates on the revision hash, never on the freeze's
	// reason. MarkSourceEventFailed can leave an EVENT_DEAD freeze beside an
	// EVENT_PAYLOAD_DRIFT one for the same payload (0009's unique index
	// separates open freezes by reason), and resolving *either* would remove
	// containment, because tryPublishEconomicScanCyclesTx does not
	// distinguish them either.
	for _, reason := range []string{"EVENT_PAYLOAD_DRIFT", "EVENT_DEAD"} {
		t.Run("the guard ignores freeze_reason: "+reason, func(t *testing.T) {
			store, ctx := integrationStore(t)
			f := seedEligibilityOpsFixture(t, store, ctx)
			seedGuardDeadEvent(t, store, ctx, f)
			if _, err := store.pool.Exec(ctx, `UPDATE eligibility_freezes SET freeze_reason=$2 WHERE id=$1`,
				f.freezeID, reason); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f)); !errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) {
				t.Fatalf("err=%v want domain.ErrEligibilityDeadEventUnrepaired", err)
			}
		})
	}

	// A fail-closed gate with no key is not allowed in this repository, so
	// the exits are asserted, not described. Acknowledging the event as
	// unreplayable is one of the two (the other is a successful requeue,
	// which writes the same column).
	t.Run("acknowledging the event opens the door again", func(t *testing.T) {
		store, ctx := integrationStore(t)
		f := seedEligibilityOpsFixture(t, store, ctx)
		eventID := seedGuardDeadEvent(t, store, ctx, f)
		if _, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f)); !errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) {
			t.Fatalf("precondition: err=%v want the guard to fire first", err)
		}
		if _, err := store.pool.Exec(ctx, `
			UPDATE source_ingest_events SET processing_status='processed',processing_error='UNREPLAYABLE_BINDING',
				processed_at=now(),updated_at=now() WHERE event_id=$1`, eventID); err != nil {
			t.Fatal(err)
		}
		resolved, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f))
		if err != nil {
			t.Fatalf("the guard has no key: %v", err)
		}
		if resolved.Status != "resolved" {
			t.Fatalf("resolution=%+v", resolved)
		}
	})

	// Only 'dead' blocks, and this arm is where that deviation from the
	// dispatch note ("until the event is processed") is pinned. A
	// waiting_dependency event is not processed either, but it has no
	// guaranteed path to becoming processed, so blocking on it would create a
	// freeze with no exit -- and this repository does not allow a fail-closed
	// gate without a key. L3 widens
	// eligibilityFreezeBlockingEventStatuses once a waiting state exists that
	// does have one; widening it today turns this arm red, which is the
	// intended alarm.
	//
	// waiting_dependency is also the only not-processed status that leaves
	// the source fresh, so it is the only one that can reach the guard at
	// all: queued/failed/processing count as pending and a *contained* dead
	// event is what the earlier arms already cover.
	t.Run("a waiting event does not block; only a dead one does", func(t *testing.T) {
		store, ctx := integrationStore(t)
		f := seedEligibilityOpsFixture(t, store, ctx)
		eventID := seedGuardDeadEvent(t, store, ctx, f)
		if _, err := store.pool.Exec(ctx, `
			UPDATE source_ingest_events SET processing_status='waiting_dependency',attempt_count=0,
				dependency_kind='source_external_account',dependency_key_hmac='h1:'||repeat('c',64),updated_at=now()
			WHERE event_id=$1`, eventID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f)); err != nil {
			t.Fatalf("a waiting (not yet processed) event blocked resolution: %v", err)
		}
	})

	// A dead event with a different payload hash is somebody else's problem
	// -- it needs its own freeze, and it has one here, otherwise it would
	// simply make the whole source stale and this arm would prove nothing
	// about the guard.
	t.Run("an unrelated dead event does not block", func(t *testing.T) {
		store, ctx := integrationStore(t)
		f := seedEligibilityOpsFixture(t, store, ctx)
		eventID := seedGuardDeadEvent(t, store, ctx, f)
		if _, err := store.pool.Exec(ctx, `
			UPDATE source_ingest_events SET payload_hash=repeat('7',64),updated_at=now() WHERE event_id=$1`,
			eventID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
			VALUES('61000000-0000-4000-8000-0000000000f1',$1,'EVENT_DEAD','usage_event',$2,repeat('7',64))`,
			f.accountID, eventID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f)); err != nil {
			t.Fatalf("a dead event on an unrelated payload blocked resolution: %v", err)
		}
	})

	// Idempotent replay must survive: an operator whose first resolution
	// succeeded and whose client retried gets the same answer back, not a 409
	// about an event that arrived afterwards. The second freeze is what keeps
	// the source fresh so the replay reaches the idempotency branch at all.
	t.Run("an idempotent replay is not caught by the guard", func(t *testing.T) {
		store, ctx := integrationStore(t)
		f := seedEligibilityOpsFixture(t, store, ctx)
		first, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f))
		if err != nil {
			t.Fatal(err)
		}
		eventID := seedGuardDeadEvent(t, store, ctx, f)
		if _, err = store.pool.Exec(ctx, `
			INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
			VALUES('61000000-0000-4000-8000-0000000000f2',$1,'EVENT_DEAD','usage_event',$2,repeat('c',64))`,
			f.accountID, eventID); err != nil {
			t.Fatal(err)
		}
		replay, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f))
		if err != nil {
			t.Fatalf("idempotent replay was rejected after the fact: %v", err)
		}
		if replay.ID != first.ID || replay.Status != "resolved" {
			t.Fatalf("replay=%+v first=%+v", replay, first)
		}
	})
}

// TestUnfreezeGuardStaysOnTheActiveIngestIndex is the A3 assertion. The guard
// runs inside every freeze resolution, in a SERIALIZABLE transaction, against
// a table the two repair tools also write under SERIALIZABLE: a sequential
// scan there would take a relation-level SIREAD predicate lock and turn a
// rare race into a 40001 on an admin path that reports it as an unclassified
// 500.
//
// The 20,000 parked rows are what make the assertion mean anything -- they sit
// outside the partial index, so the index holds a handful of rows while the
// table holds twenty thousand, and only then does the planner's choice reflect
// what it would do in production.
func TestUnfreezeGuardStaysOnTheActiveIngestIndex(t *testing.T) {
	store, ctx := integrationStore(t)
	f := seedEligibilityOpsFixture(t, store, ctx)
	const batchID = "80000000-0000-4000-8000-0000000000e0"
	seedGuardDeadEvent(t, store, ctx, f)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,dependency_kind,dependency_key_hmac,catchup_key_hmac,created_at,updated_at)
		SELECT $1,'usage',('81000000-0000-4000-8000-'||lpad(to_hex(n),12,'0'))::uuid,
			$2,'usage_event','upsert',repeat('b',64),
			decode(repeat('11',16),'hex'),now(),'parked_identity','source_external_account',
			'h1:'||repeat('c',64),'h1:'||repeat('c',64),now(),now()
		FROM generate_series(1,20000) n`, f.sourceID, batchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `ANALYZE source_ingest_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `ANALYZE eligibility_freezes`); err != nil {
		t.Fatal(err)
	}
	// The production query text, not a retyped copy of it. A copy would plan
	// beautifully forever while the real guard drifted underneath it.
	rows, err := store.pool.Query(ctx, "EXPLAIN (COSTS OFF) "+eligibilityFreezeDeadEventGuardQuery,
		f.freezeID, f.sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var planLines []string
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		planLines = append(planLines, line)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	plan := strings.Join(planLines, "\n")
	if !strings.Contains(plan, "source_ingest_events_readiness_active_idx") ||
		strings.Contains(strings.ToLower(plan), "seq scan on source_ingest_events") {
		t.Fatalf("the unfreeze guard left the active-only ingest index:\n%s", plan)
	}
	// "It uses the index" is not enough, and finding that out is what this
	// assertion is for: the partial index is small in any fixture, so the
	// planner will happily scan the whole of it with no bound at all. What
	// makes the lookup bounded in production -- where the index holds every
	// active event on every source -- is source_instance_id being an index
	// *condition*, so that is what is asserted. Dropping the narrowing leaves
	// the query on the same index and still passes the check above.
	var indexCond string
	for _, line := range planLines {
		if strings.Contains(line, "Index Cond:") {
			indexCond = line
		}
	}
	if !strings.Contains(indexCond, "source_instance_id") {
		t.Fatalf("the guard's index lookup is not bounded to one source, so it scans every "+
			"active event on the deployment:\n%s", plan)
	}
	// eligibility_freezes itself is scanned here, and that is expected: the
	// table is tiny in a fixture. Migration 0032's partial index is what keeps
	// the freeze side bounded once the freeze history grows, and an EXPLAIN on
	// a five-row table cannot show that either way.
}
