package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

const ingestLockSource = "10000000-0000-4000-8000-000000000099"

func ingestLockFixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	store, ctx := integrationStore(t)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'newapi','concurrent-ingestion-test','locking-test')`, ingestLockSource); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "credits", "payments", "usage", "identities"} {
		if err := store.ProvisionSourceStream(ctx, ingestLockSource, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	return store, ctx
}

func ingestLockBatch(stream, batch string) SourceBatchInput {
	hash := testHash(batch)
	id := fmt.Sprintf("%s-%s-%s-%s-%s", hash[:8], hash[8:12], hash[12:16], hash[16:20], hash[20:32])
	return SourceBatchInput{
		SchemaVersion: "2.0", SourceInstanceID: ingestLockSource, StreamID: stream,
		BatchID: id, Sequence: 1, BodyHash: hash, SigningKeyID: "test-key",
		SourceRuntimeVersion: "locking-test", SourceAgentVersion: "test-agent",
		SourceCapturedAt: time.Now().UTC(), ProjectionStatus: "healthy",
		Actor: AuditActor{Type: "source_connector", ID: ingestLockSource},
	}
}

// The test-only trigger parks a real batch transaction AFTER its authority and
// stream locks have been taken. A second stream must finish without releasing
// that transaction; disabling or replacing the approved source must still wait.
func TestCommitSourceBatchSeparatesStreamLocksAndProtectsSourceConfiguration(t *testing.T) {
	for _, change := range []struct{ name, assignment string }{
		{"disable", "enabled=false"},
		{"runtime", "runtime_version='replacement-runtime'"},
	} {
		t.Run(change.name, func(t *testing.T) {
			store, root := ingestLockFixture(t)
			ctx, cancel := context.WithTimeout(root, 15*time.Second)
			defer cancel()
			if _, err := store.pool.Exec(ctx, `CREATE FUNCTION pause_balance_ingestion() RETURNS trigger
				LANGUAGE plpgsql AS $$ BEGIN
				IF NEW.stream_id='balances' THEN PERFORM pg_advisory_xact_lock(183763,1); END IF;
				RETURN NEW; END $$;
				CREATE TRIGGER pause_balance_ingestion BEFORE INSERT ON source_ingest_batches
				FOR EACH ROW EXECUTE FUNCTION pause_balance_ingestion()`); err != nil {
				t.Fatal(err)
			}
			holder, err := store.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback(context.Background()) }()
			if _, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(183763,1)`); err != nil {
				t.Fatal(err)
			}
			balanceDone := make(chan error, 1)
			go func() {
				_, err := store.CommitSourceBatch(ctx, ingestLockBatch("balances", "balance-1"))
				balanceDone <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				select {
				case err := <-balanceDone:
					t.Fatalf("balance fixture failed before test barrier: %v", err)
				default:
				}
				var parked bool
				if err = store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks
					WHERE locktype='advisory' AND classid=183763 AND objid=1 AND NOT granted)`).Scan(&parked); err != nil {
					t.Fatal(err)
				}
				if parked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("balance transaction did not reach the test barrier")
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, stream := range []string{"credits", "payments", "usage", "identities"} {
				otherCtx, stop := context.WithTimeout(ctx, time.Second)
				ack, err := store.CommitSourceBatch(otherCtx, ingestLockBatch(stream, stream+"-1"))
				stop()
				if err != nil || ack.Sequence != 1 || ack.Duplicate {
					t.Fatalf("independent %s batch blocked by balances: ack=%+v err=%v", stream, ack, err)
				}
			}
			config, err := store.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer config.Release()
			var configPID int32
			if err = config.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&configPID); err != nil {
				t.Fatal(err)
			}
			configDone := make(chan error, 1)
			go func() {
				_, err := config.Exec(ctx, "UPDATE source_instances SET "+change.assignment+" WHERE id=$1", ingestLockSource)
				configDone <- err
			}()
			deadline = time.Now().Add(3 * time.Second)
			for {
				select {
				case err := <-configDone:
					t.Fatalf("source authority changed before active batch completed: %v", err)
				default:
				}
				var blocked bool
				if err = store.pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, configPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("source configuration update did not wait on active ingestion")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-balanceDone; err != nil {
				t.Fatal(err)
			}
			if err = <-configDone; err != nil {
				t.Fatal(err)
			}
			stale := ingestLockBatch("credits", "credits-2")
			stale.Sequence, stale.PreviousBatchHash = 2, testHash("credits-1")
			if _, err = store.CommitSourceBatch(ctx, stale); !errors.Is(err, domain.ErrVersionConflict) {
				t.Fatalf("batch with revoked source authority must be rejected: %v", err)
			}
			var batches, audits int
			if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_ingest_batches WHERE source_instance_id=$1`, ingestLockSource).Scan(&batches); err != nil {
				t.Fatal(err)
			}
			if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='source_batch.committed' AND actor_id=$1`, ingestLockSource).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if batches != 5 || audits != 5 {
				t.Fatalf("unexpected partial/duplicate commits: batches=%d audits=%d", batches, audits)
			}
		})
	}
}

func TestCommitSourceBatchConcurrentSameStreamStillRejectsForks(t *testing.T) {
	t.Run("forks", func(t *testing.T) { checkConcurrentIngestChain(t, false) })
	t.Run("identical replay", func(t *testing.T) { checkConcurrentIngestChain(t, true) })
}

func checkConcurrentIngestChain(t *testing.T, replay bool) {
	t.Helper()
	store, root := ingestLockFixture(t)
	ctx, cancel := context.WithTimeout(root, 15*time.Second)
	defer cancel()
	type outcome struct {
		ack SourceBatchResult
		err error
	}
	results := make(chan outcome, 8)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			if replay {
				i = 0
			}
			batch := ingestLockBatch("credits", fmt.Sprintf("competing-batch-%d", i))
			batch.Events = []SourceBatchEvent{{EventID: fmt.Sprintf("70000000-0000-4000-8000-%012d", i), EntityType: "credit", Operation: "upsert",
				PayloadHash: strings.Repeat("a", 64), PayloadCiphertext: []byte("test-ciphertext-placeholder"), ObservedAt: time.Now().UTC()}}
			ack, err := store.CommitSourceBatch(ctx, batch)
			results <- outcome{ack, err}
		}(i)
	}
	close(start)
	group.Wait()
	close(results)
	accepted, conflicts, duplicates := 0, 0, 0
	for row := range results {
		if row.err == nil && !row.ack.Duplicate {
			accepted++
		} else if row.err == nil && row.ack.Duplicate {
			duplicates++
		} else if errors.Is(row.err, domain.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent result: ack=%+v err=%v", row.ack, row.err)
		}
	}
	var batches, events, seq int
	if err := store.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM source_ingest_batches WHERE source_instance_id=$1),
		(SELECT count(*) FROM source_ingest_events WHERE source_instance_id=$1),
		(SELECT sequence FROM source_ingest_state WHERE source_instance_id=$1 AND stream_id='credits')`, ingestLockSource).Scan(&batches, &events, &seq); err != nil {
		t.Fatal(err)
	}
	wantConflicts, wantDuplicates := 7, 0
	if replay {
		wantConflicts, wantDuplicates = 0, 7
	}
	if accepted != 1 || conflicts != wantConflicts || duplicates != wantDuplicates || batches != 1 || events != 1 || seq != 1 {
		t.Fatalf("same-stream fork or replay left partial writes: accepted=%d conflicts=%d duplicates=%d batches=%d events=%d seq=%d", accepted, conflicts, duplicates, batches, events, seq)
	}
}

// Exercise the actual v3 scan lock, in addition to the deterministic post-entry
// barrier above. A slow continuation must not stop another stream's heartbeat.
func TestCommitSourceBatchLockedScanDoesNotStallOtherStream(t *testing.T) {
	store, root := ingestLockFixture(t)
	ctx, cancel := context.WithTimeout(root, 10*time.Second)
	defer cancel()
	input := ingestLockBatch("usage", "usage-scan-first")
	input.SchemaVersion = "3.0"
	input.StreamWatermarkAt, input.ScanCeilingAt = input.SourceCapturedAt, input.SourceCapturedAt
	input.SourceCursor, input.ScanCeilingCursor = "usage:0", "usage:1"
	input.ScanCycleID = "90000000-0000-4000-8000-000000000099"
	if _, err := store.CommitSourceBatch(ctx, input); err != nil {
		t.Fatal(err)
	}
	holder, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var holderPID int32
	if err = holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err = holder.Exec(ctx, `SELECT scan_cycle_id FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='usage' AND scan_cycle_id=$2 FOR UPDATE`, ingestLockSource, input.ScanCycleID); err != nil {
		t.Fatal(err)
	}
	next := input
	next.BatchID, next.BodyHash = randomUUID(), testHash("usage-scan-second")
	next.Sequence, next.PreviousBatchHash, next.ScanComplete = 2, input.BodyHash, true
	done := make(chan error, 1)
	go func() { _, err := store.CommitSourceBatch(ctx, next); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("continuation escaped held scan lock: %v", err)
		default:
		}
		var blocked bool
		if err = store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("continuation never reached the scan lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	otherCtx, stop := context.WithTimeout(ctx, time.Second)
	ack, err := store.CommitSourceBatch(otherCtx, ingestLockBatch("payments", "other-stream-during-scan"))
	stop()
	if err != nil || ack.Sequence != 1 {
		t.Fatalf("payments blocked by another stream scan: ack=%+v err=%v", ack, err)
	}
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var sequence int64
	var hash string
	if err = store.pool.QueryRow(ctx, `SELECT sequence,last_batch_hash FROM source_ingest_state WHERE source_instance_id=$1 AND stream_id='usage'`, ingestLockSource).Scan(&sequence, &hash); err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || hash != next.BodyHash {
		t.Fatalf("scan chain drift: sequence=%d hash=%s", sequence, hash)
	}
}
