package postgresstore

import (
	"context"
	"testing"
	"time"
)

type evidenceOutcome struct {
	statuses        []string
	finalized       time.Time
	consumedCash    int64
	evaluationCount int
}

func readEvidenceOutcome(t *testing.T, store *Store, ctx context.Context, accountID string) evidenceOutcome {
	t.Helper()
	var out evidenceOutcome
	rows, err := store.pool.Query(ctx, `
		SELECT evaluation.evaluation_status FROM balance_checkpoint_evaluations evaluation
		JOIN balance_reconciliation_checkpoints checkpoint ON checkpoint.id=evaluation.checkpoint_id
		WHERE checkpoint.external_account_id=$1 ORDER BY checkpoint.as_of,evaluation.projection_version`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var status string
		if err = rows.Scan(&status); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out.statuses = append(out.statuses, status)
	}
	rows.Close()
	out.evaluationCount = len(out.statuses)
	if err = store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(&out.finalized); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT COALESCE(sum(consumed_cash_minor),0) FROM funding_lots WHERE external_account_id=$1`, accountID).Scan(&out.consumedCash); err != nil {
		t.Fatal(err)
	}
	return out
}

// XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3. With the bound set to one, a job
// evaluates exactly one pending checkpoint, publishes finalized_through at
// its as_of, and requeues itself; the queue drains in as many jobs as there
// are checkpoints. And the result must be byte-identical to the single pass:
// same evaluation statuses in the same order, same finalized_through, same
// consumed cash. That equality is the in-repo half of the differential
// rehearsal.
func TestEvidenceBatchLimitChunksTheEvidencePassWithoutChangingItsResult(t *testing.T) {
	const checkpoints = 3
	worker := AuditActor{Type: "system", ID: "batch-limit-test"}

	// Bounded: one checkpoint per job.
	store, ctx := integrationStore(t)
	store.SetEvidenceBatchLimit(1)
	account, asOfs, queue := seedAnchoredAccountWithCheckpoints(t, store, ctx, "b1", checkpoints)
	through := asOfs[len(asOfs)-1].Add(time.Minute)
	queue(through)
	// Drive the queue with the bound on. Each job evaluates at most one
	// pending item (a carry-forward proof the lock-free half inserted at the
	// cutover cycle counts too, and rides along with the first chunk), so
	// the queue takes at least one job per checkpoint to drain.
	jobs := 0
	var progressed []time.Time
	for jobs < 12 {
		processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
		if err != nil {
			t.Fatalf("bounded job %d: %v", jobs+1, err)
		}
		if processed == 0 {
			break
		}
		jobs++
		var finalized time.Time
		if err = store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state WHERE external_account_id=$1`, account).Scan(&finalized); err != nil {
			t.Fatal(err)
		}
		progressed = append(progressed, finalized)
	}
	if jobs < checkpoints {
		t.Fatalf("with a bound of one, draining %d checkpoints must take at least %d jobs, took %d (finalized_through after each: %v)", checkpoints, checkpoints, jobs, progressed)
	}
	// Every chunk that advanced landed exactly on a checkpoint's as_of or on
	// the requested window -- never in between.
	// (Compared with Equal, not as map keys: the value read back carries the
	// database session's location, and time.Time map keys compare locations.)
	isAllowed := func(at time.Time) bool {
		if at.Equal(through) {
			return true
		}
		for _, asOf := range asOfs {
			if at.Equal(asOf) {
				return true
			}
		}
		return false
	}
	for i, finalized := range progressed {
		if i > 0 && !finalized.Equal(progressed[i-1]) && !isAllowed(finalized) {
			t.Fatalf("job %d published finalized_through %s, which is neither a checkpoint as_of nor the window", i+1, finalized)
		}
	}
	bounded := readEvidenceOutcome(t, store, ctx, account)
	if !bounded.finalized.Equal(through) {
		t.Fatalf("bounded run must end at the requested window %s, got %s", through, bounded.finalized)
	}

	// Single pass, fresh schema: the reference.
	store2, ctx2 := integrationStore(t)
	account2, asOfs2, queue2 := seedAnchoredAccountWithCheckpoints(t, store2, ctx2, "b0", checkpoints)
	through2 := asOfs2[len(asOfs2)-1].Add(time.Minute)
	queue2(through2)
	processed, err := store2.ProcessEligibilityProjectionJobs(ctx2, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("single pass: processed=%d err=%v", processed, err)
	}
	single := readEvidenceOutcome(t, store2, ctx2, account2)

	// The anchor checkpoint is evaluated at observation as well as by the
	// pass, so the row count exceeds the checkpoint count; what matters is
	// that both modes produce the same rows in the same order.
	if single.evaluationCount < checkpoints || single.evaluationCount != bounded.evaluationCount {
		t.Fatalf("both runs must produce the same evaluation rows: single=%d bounded=%d (single=%v bounded=%v)", single.evaluationCount, bounded.evaluationCount, single.statuses, bounded.statuses)
	}
	for i := range single.statuses {
		if single.statuses[i] != bounded.statuses[i] {
			t.Fatalf("evaluation %d differs: single=%q bounded=%q (all: single=%v bounded=%v)", i, single.statuses[i], bounded.statuses[i], single.statuses, bounded.statuses)
		}
	}
	if single.consumedCash != bounded.consumedCash {
		t.Fatalf("consumed cash differs: single=%d bounded=%d", single.consumedCash, bounded.consumedCash)
	}
	if single.finalized.Sub(asOfs2[0]) != bounded.finalized.Sub(asOfs[0]) {
		t.Fatalf("finalized_through differs relative to the fixture: single=%s bounded=%s", single.finalized, bounded.finalized)
	}
}

// The case the constant-balance test cannot reach: a checkpoint that reports
// more than the ledger expects is deferred until the next checkpoint
// reproduces it. With a bound of one the boundary lands exactly on the
// deferred checkpoint. The next chunk must carry it along and let the
// following checkpoint confirm it -- the single pass's outcome -- and the
// queue must not spin on it (the boundary counts only items after
// finalized_through, so each chunk advances).
func TestEvidenceBatchLimitCarriesADeferredPositiveAcrossTheChunkBoundary(t *testing.T) {
	worker := AuditActor{Type: "system", ID: "batch-defer-test"}
	balances := []string{"500", "600", "600", "600"}

	store, ctx := integrationStore(t)
	store.SetEvidenceBatchLimit(1)
	account, asOfs, queue := seedAnchoredAccountWithCheckpointBalances(t, store, ctx, "d1", balances)
	through := asOfs[len(asOfs)-1].Add(time.Minute)
	queue(through)
	jobs := 0
	for jobs < 20 {
		processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
		if err != nil {
			t.Fatalf("bounded job %d: %v", jobs+1, err)
		}
		if processed == 0 {
			break
		}
		jobs++
	}
	if jobs >= 20 {
		t.Fatal("the bounded queue did not drain: a deferred item is pinning the boundary")
	}
	bounded := readEvidenceOutcome(t, store, ctx, account)

	store2, ctx2 := integrationStore(t)
	account2, asOfs2, queue2 := seedAnchoredAccountWithCheckpointBalances(t, store2, ctx2, "d0", balances)
	queue2(asOfs2[len(asOfs2)-1].Add(time.Minute))
	if processed, err := store2.ProcessEligibilityProjectionJobs(ctx2, 10, time.Now().UTC().Add(time.Minute), worker); err != nil || processed != 1 {
		t.Fatalf("single pass: processed=%d err=%v", processed, err)
	}
	single := readEvidenceOutcome(t, store2, ctx2, account2)

	if len(single.statuses) == 0 || len(single.statuses) != len(bounded.statuses) {
		t.Fatalf("both modes must produce the same evaluation rows: single=%v bounded=%v", single.statuses, bounded.statuses)
	}
	for i := range single.statuses {
		if single.statuses[i] != bounded.statuses[i] {
			t.Fatalf("evaluation %d differs: single=%q bounded=%q (single=%v bounded=%v)", i, single.statuses[i], bounded.statuses[i], single.statuses, bounded.statuses)
		}
	}
	if single.consumedCash != bounded.consumedCash {
		t.Fatalf("consumed cash differs: single=%d bounded=%d", single.consumedCash, bounded.consumedCash)
	}
	// The deferral path really ran: at least one status that only the
	// confirm-or-disconfirm logic produces.
	exercised := false
	for _, status := range single.statuses {
		if status == "positive_classified_non_cash" || status == "positive_blip_ignored" {
			exercised = true
		}
	}
	if !exercised {
		t.Fatalf("the fixture did not exercise the deferral rule; statuses=%v", single.statuses)
	}
	t.Logf("statuses (both modes): %v; bounded jobs: %d", single.statuses, jobs)
}
