package postgresstore

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSourceReadinessGraceCoversEveryTransientRequeueMarker walks
// transientRequeueMarkers rather than naming the markers, so a marker added
// later is covered by this test the day it is added instead of the day someone
// remembers to extend the test. That is the whole reason the marker set and
// the SQL array in the readiness query are rendered from one slice: before
// XM-INV-SER-BUSY the literal 'ACCOUNT_LOCK_BUSY' was pinned separately in the
// writer and in the grace predicate, and a second marker written that way
// would have been requeued forever while the grace quietly refused to cover
// it -- the stream would have failed readiness on an event the system had
// already decided was benign, with nothing anywhere reporting a mismatch.
//
// Each marker gets all three phases, because "inside the grace" only means
// something next to the two cases that must still count:
//   - freshly requeued under the marker: not pending, stream stays ready
//   - requeued longer ago than the grace: pending again, readiness off
//   - queued with no marker at all: pending immediately, as always
func TestSourceReadinessGraceCoversEveryTransientRequeueMarker(t *testing.T) {
	store, ctx := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := seedReadySourceStreams(t, store, ctx, now)
	const sourceID = "10000000-0000-4000-8000-000000000001"
	const batchID = "80000000-0000-4000-8000-0000000000c0"

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'balances',$2,1,repeat('b',64),
			'readiness-key',0,'fixture-runtime','readiness-agent',$3,'healthy')`,
		sourceID, batchID, now); err != nil {
		t.Fatal(err)
	}

	if len(transientRequeueMarkers) == 0 {
		t.Fatal("transientRequeueMarkers is empty, so this test would assert nothing at all")
	}
	for i, marker := range transientRequeueMarkers {
		t.Run(marker, func(t *testing.T) {
			eventID := fmt.Sprintf("82000000-0000-4000-8000-0000000000d%d", i)
			if _, err := store.pool.Exec(ctx, `
				INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
					payload_hash,payload_ciphertext,observed_at,processing_status,processing_error,next_attempt_at,created_at,updated_at)
				VALUES($1,'balances',$2,$3,'balance_checkpoint','upsert',$4,
					decode(repeat('33',16),'hex'),$5,'queued',$6,$7,$5,$5)`,
				sourceID, eventID, batchID, strings.Repeat("c", 64), now, marker, now.Add(15*time.Second)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = store.pool.Exec(ctx, `DELETE FROM source_ingest_events WHERE event_id=$1`, eventID)
			})

			fresh, err := store.SourceReadinessHealth(ctx, policy)
			if err != nil || !fresh.Report.Ready || fresh.Ingest.Pending != 0 {
				t.Fatalf("an event freshly requeued as %s must not count as pending: ready=%v pending=%d err=%v",
					marker, fresh.Report.Ready, fresh.Ingest.Pending, err)
			}

			if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_events SET updated_at=$2 WHERE event_id=$1`,
				eventID, now.Add(-11*time.Minute)); err != nil {
				t.Fatal(err)
			}
			stale, err := store.SourceReadinessHealth(ctx, policy)
			if err != nil || stale.Report.Ready || stale.Ingest.Pending != 1 {
				t.Fatalf("an event held as %s past the grace must count as pending again: ready=%v pending=%d err=%v",
					marker, stale.Report.Ready, stale.Ingest.Pending, err)
			}

			if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_events SET processing_error=NULL,updated_at=$2 WHERE event_id=$1`,
				eventID, now); err != nil {
				t.Fatal(err)
			}
			plain, err := store.SourceReadinessHealth(ctx, policy)
			if err != nil || plain.Report.Ready || plain.Ingest.Pending != 1 {
				t.Fatalf("a plain queued event must still count as pending: ready=%v pending=%d err=%v",
					plain.Report.Ready, plain.Ingest.Pending, err)
			}
		})
	}
}

// TestMarkSourceEventBusyRefusesAMarkerTheGraceWouldNotCover pins the other
// half of the single-definition arrangement. The readiness grace can only
// cover what transientRequeueMarkers lists, so a caller that invents its own
// marker string would write a row the grace treats as an ordinary pending
// event -- readiness off, with no error anywhere to say why. Rejecting the
// write is what keeps "the writer and the grace agree" true by construction
// instead of by convention.
func TestMarkSourceEventBusyRefusesAMarkerTheGraceWouldNotCover(t *testing.T) {
	store, ctx := integrationStore(t)
	err := store.MarkSourceEventBusy(ctx, SourceEventClaim{
		SourceInstanceID: "10000000-0000-4000-8000-000000000001",
		StreamID:         "balances",
		EventID:          "82000000-0000-4000-8000-0000000000e0",
		LeaseToken:       "irrelevant-the-marker-is-checked-first",
	}, "INVENTED_MARKER", time.Now().Add(time.Second))
	if err == nil || !strings.Contains(err.Error(), "unknown transient requeue marker") {
		t.Fatalf("MarkSourceEventBusy accepted a marker the readiness grace does not cover: %v", err)
	}
}

// TestTransientRequeueMarkerSQLRendersEveryMarker is the cheap structural
// companion to the behavioural test above: it fails on a rendering bug even
// when no database is available, and it states the invariant the readiness
// query depends on in one line.
func TestTransientRequeueMarkerSQLRendersEveryMarker(t *testing.T) {
	for _, marker := range transientRequeueMarkers {
		if !strings.Contains(transientRequeueMarkerSQL, "'"+marker+"'") {
			t.Fatalf("marker %s is in transientRequeueMarkers but missing from the rendered SQL array %s",
				marker, transientRequeueMarkerSQL)
		}
	}
	if !strings.Contains(sourceReadinessHealthQuery, transientRequeueMarkerSQL) {
		t.Fatalf("the readiness query does not embed the rendered marker array %s", transientRequeueMarkerSQL)
	}
}
