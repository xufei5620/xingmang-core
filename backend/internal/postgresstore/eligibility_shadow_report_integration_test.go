package postgresstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedEligibilityShadowCheckpoint inserts the minimal
// balance_reconciliation_checkpoints row that balance_checkpoint_evaluations'
// foreign key requires, reusing the source/account fixture
// seedEligibilityProjectionHealthSource/seedEligibilityProjectionHealthAccount
// already build for the sibling XM-INV-READY-PENDING/READY-LEASE health
// tests in this package.
func seedEligibilityShadowCheckpoint(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, manifestHash, configHash string, asOf time.Time) string {
	t.Helper()
	checkpointID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,checkpoint_kind,
			as_of,balance_service_units,unit_code,cutover_manifest_hash,configuration_hash,
			reconciliation_status,expected_service_units,difference_service_units,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,'reconciliation',$5,0,'SUB2_BALANCE_1E8',$6,$7,'matched',0,0,1,'cursor-1',$5,$8,$5)`,
		checkpointID, sourceID, accountID, "event-"+checkpointID, asOf, manifestHash, configHash,
		testHash("shadow-checkpoint-"+checkpointID)); err != nil {
		t.Fatal(err)
	}
	return checkpointID
}

func seedEligibilityShadowCheckpointEvaluation(t *testing.T, store *Store, ctx context.Context, checkpointID string, projectionVersion int64, status string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_checkpoint_evaluations(
			id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,$3,0,0,$4)`, randomUUID(), checkpointID, projectionVersion, status); err != nil {
		t.Fatal(err)
	}
}

func TestEligibilityShadowSnapshotAccountsAndFreezes(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	activeAccount := "60000000-0000-4000-8000-000000000001"
	frozenAccount := "60000000-0000-4000-8000-000000000002"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, activeAccount, "shadow-active", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, frozenAccount, "shadow-frozen", cutover, manifestHash, configHash)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
		VALUES($1,$2,'SOURCE_GAP','external_account',$3)`, randomUUID(), frozenAccount, frozenAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE source_account_eligibility_state SET eligibility_status='frozen' WHERE external_account_id=$1`,
		frozenAccount); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.EligibilityShadowSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]EligibilityShadowAccountStatus{}
	for _, account := range snapshot.Accounts {
		statuses[account.ExternalAccountID] = account
	}
	active, ok := statuses[activeAccount]
	if !ok || active.EligibilityStatus != "active" || active.OpenFreezes != 0 {
		t.Fatalf("active account snapshot wrong: %+v (present=%v)", active, ok)
	}
	frozen, ok := statuses[frozenAccount]
	if !ok || frozen.EligibilityStatus != "frozen" || frozen.OpenFreezes != 1 {
		t.Fatalf("frozen account snapshot wrong: %+v (present=%v)", frozen, ok)
	}
	if len(snapshot.OpenFreezes) != 1 || snapshot.OpenFreezes[0].FreezeReason != "SOURCE_GAP" || snapshot.OpenFreezes[0].Open != 1 {
		t.Fatalf("open freezes by reason wrong: %+v", snapshot.OpenFreezes)
	}
}

// TestEligibilityShadowSnapshotEvaluationsKeepsLatestPerCheckpoint proves the
// evaluation-status count only counts each checkpoint's current (highest
// projection_version) evaluation, not one row per historical re-evaluation
// attempt -- a checkpoint re-evaluated from source_gap_frozen to matched
// (the exact XM-INV-ANCHOR-BALANCE repair transition) must count once, as
// matched.
func TestEligibilityShadowSnapshotEvaluationsKeepsLatestPerCheckpoint(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	accountID := "60000000-0000-4000-8000-000000000003"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "shadow-eval", cutover, manifestHash, configHash)
	checkpointID := seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, accountID, manifestHash, configHash, cutover.Add(time.Hour))
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, checkpointID, 1, "source_gap_frozen")
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, checkpointID, 2, "matched")

	snapshot, err := store.EligibilityShadowSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, count := range snapshot.Evaluations {
		counts[count.EvaluationStatus] = count.Count
	}
	if counts["matched"] != 1 {
		t.Fatalf("expected exactly one matched evaluation, got counts=%+v", counts)
	}
	if counts["source_gap_frozen"] != 0 {
		t.Fatalf("superseded evaluation must not still be counted, got counts=%+v", counts)
	}
}

func TestEligibilityProjectionClaimableCount(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	now := time.Now().UTC().Truncate(time.Second)

	dueQueued := "60000000-0000-4000-8000-0000000000a1"
	futureQueued := "60000000-0000-4000-8000-0000000000a2"
	dueFailed := "60000000-0000-4000-8000-0000000000a3"
	lapsedProcessing := "60000000-0000-4000-8000-0000000000a4"
	liveProcessing := "60000000-0000-4000-8000-0000000000a5"
	for _, accountID := range []string{dueQueued, futureQueued, dueFailed, lapsedProcessing, liveProcessing} {
		seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "claim-"+accountID, cutover, manifestHash, configHash)
	}
	seedEligibilityProjectionJobRow(t, store, ctx, dueQueued, "queued", nil, now, now, now.Add(-time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, futureQueued, "queued", strPtr("BALANCE_PROOF_PENDING"), now, now, now.Add(10*time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, dueFailed, "failed", strPtr("PROJECTION_FAILED"), now, now, now.Add(-time.Minute))
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, lapsedProcessing, now, now, now.Add(-time.Minute))
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, liveProcessing, now, now, now.Add(time.Minute))

	claimable, err := store.EligibilityProjectionClaimableCount(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// Claimable: dueQueued, dueFailed, lapsedProcessing. Not claimable:
	// futureQueued (backoff not elapsed), liveProcessing (lease still live).
	if claimable != 3 {
		t.Fatalf("expected 3 claimable rows, got %d", claimable)
	}
}

// TestEligibilityShadowFailedJobs seeds a status='dead' row, not 'failed':
// XM-INV-PROJECTION-FAILURE-GRADING renamed EligibilityShadowFailedJobs' own
// predicate from the legacy 'failed' status (no longer produced by
// ProcessEligibilityProjectionJobs) to the new terminal 'dead' grade -- see
// that method's own doc comment. A merely-retrying queued job (attempts>0,
// still short of the dead threshold) must not show up here either.
func TestEligibilityShadowFailedJobs(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	deadAccount := "60000000-0000-4000-8000-0000000000b1"
	queuedAccount := "60000000-0000-4000-8000-0000000000b2"
	retryingAccount := "60000000-0000-4000-8000-0000000000b3"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, deadAccount, "dead-job", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, queuedAccount, "queued-job", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, retryingAccount, "retrying-job", cutover, manifestHash, configHash)
	now := time.Now().UTC().Truncate(time.Second)
	seedEligibilityProjectionJobRow(t, store, ctx, deadAccount, "dead", strPtr("PROJECTION_DEAD"), now, now, now.Add(5*time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, queuedAccount, "queued", nil, now, now, now)
	seedEligibilityProjectionJobRowWithAttempts(t, store, ctx, retryingAccount, "queued", 3, strPtr("PROJECTION_FAILED"), now, now, now.Add(2*time.Minute))

	failed, err := store.EligibilityShadowFailedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ExternalAccountID != deadAccount || failed[0].LastErrorCode != "PROJECTION_DEAD" {
		t.Fatalf("expected exactly the one dead job, got %+v", failed)
	}
}

// TestEnqueueEligibilityShadowReprojectionQueuesEveryAccountAtItsOwnBoundary
// is the geology under XM-INV-SHADOW-EVAL-VACUOUS: the rehearsal only stops
// being vacuous if this INSERT really does put one job per account into the
// queue, at that account's own finalized_through. Asserting it against a
// real Postgres rather than reasoning about the SQL is the point -- the
// defect this fixes existed because nobody checked what the rehearsal
// actually did to the database.
func TestEnqueueEligibilityShadowReprojectionQueuesEveryAccountAtItsOwnBoundary(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	first := "60000000-0000-4000-8000-0000000000c1"
	second := "60000000-0000-4000-8000-0000000000c2"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, first, "reproject-first", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, second, "reproject-second", cutover, manifestHash, configHash)
	firstBoundary := cutover.Add(2 * time.Hour)
	secondBoundary := cutover.Add(9 * time.Hour)
	for accountID, boundary := range map[string]time.Time{first: firstBoundary, second: secondBoundary} {
		if _, err := store.pool.Exec(ctx, `
			UPDATE source_account_eligibility_state SET finalized_through=$2
			WHERE external_account_id=$1`, accountID, boundary); err != nil {
			t.Fatal(err)
		}
	}

	enqueued, err := store.EnqueueEligibilityShadowReprojection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The contract is "one job per account", so the expectation is derived
	// from the table rather than hard-coded: the shared fixture seeds an
	// account of its own, and a literal 2 here would assert this test's
	// bookkeeping instead of the behaviour.
	var accounts int64
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if enqueued != accounts {
		t.Fatalf("expected one job per account (%d), got %d", accounts, enqueued)
	}
	// Each account must be requested at its OWN boundary, not at a shared
	// one: requesting a low-water account at a high-water time would ask the
	// projection for a window it has no facts for.
	for accountID, want := range map[string]time.Time{first: firstBoundary, second: secondBoundary} {
		var requested time.Time
		var status string
		if err = store.pool.QueryRow(ctx, `
			SELECT requested_through,status FROM eligibility_projection_jobs
			WHERE external_account_id=$1`, accountID).Scan(&requested, &status); err != nil {
			t.Fatal(err)
		}
		if !requested.Equal(want) {
			t.Fatalf("account %s: requested_through %s, want its own finalized_through %s", accountID, requested, want)
		}
		if status != "queued" {
			t.Fatalf("account %s: status %q, want queued", accountID, status)
		}
	}
	// Claimable immediately -- next_attempt_at is backdated so the drain
	// loop's very first round sees the work rather than waiting a tick.
	claimable, err := store.EligibilityProjectionClaimableCount(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claimable != accounts {
		t.Fatalf("expected every queued job immediately claimable (%d), got %d", accounts, claimable)
	}
}

// A rehearsal must add work, never silently reset state the restored backup
// captured. A dead job is the case that matters: resetting it to 'queued'
// would erase the very evidence an operator restored the backup to inspect,
// and would also hide a genuinely stuck account behind a clean-looking run.
func TestEnqueueEligibilityShadowReprojectionLeavesExistingJobsUntouched(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	deadAccount := "60000000-0000-4000-8000-0000000000d1"
	freshAccount := "60000000-0000-4000-8000-0000000000d2"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, deadAccount, "reproject-dead", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, freshAccount, "reproject-fresh", cutover, manifestHash, configHash)
	now := time.Now().UTC().Truncate(time.Second)
	seedEligibilityProjectionJobRow(t, store, ctx, deadAccount, "dead", strPtr("PROJECTION_DEAD"), now, now, now.Add(5*time.Minute))
	// Read the row back rather than predicting it: the shared seed helper
	// derives requested_through itself, and "untouched" is a statement about
	// what was there before, not about what this test believes was written.
	var deadRequested time.Time
	if err := store.pool.QueryRow(ctx, `
		SELECT requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, deadAccount).Scan(&deadRequested); err != nil {
		t.Fatal(err)
	}

	enqueued, err := store.EnqueueEligibilityShadowReprojection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var accounts int64
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if enqueued != accounts-1 {
		t.Fatalf("expected every account except the one already holding a job (%d), got %d", accounts-1, enqueued)
	}
	var status string
	var requested time.Time
	if err = store.pool.QueryRow(ctx, `
		SELECT status,requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, deadAccount).Scan(&status, &requested); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || !requested.Equal(deadRequested) {
		t.Fatalf("dead job was modified: status=%q requested_through=%s", status, requested)
	}
}

// RC92 finding: the whale had a queued job in the backup, requested through a
// window a frozen copy could never cover, so ON CONFLICT DO NOTHING left the
// one account that matters most unexercised. A non-dead job is now retargeted
// to the account's own boundary: claimable at once, backoff and lease cleared.
// A queued job captured in the backup is a real window: raised to at least
// the account's own boundary, never lowered (a window above the boundary is
// the catch-up backlog a bounded-evidence differential exists to exercise;
// RC95 found the old overwrite erased it), and made claimable now.
func TestEnqueueEligibilityShadowReprojectionKeepsALargerQueuedWindowAndRaisesASmallerOne(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	larger := "60000000-0000-4000-8000-0000000000e1"
	smaller := "60000000-0000-4000-8000-0000000000e2"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, larger, "reproject-keep", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, smaller, "reproject-raise", cutover, manifestHash, configHash)
	boundary := cutover.Add(3 * time.Hour)
	for _, account := range []string{larger, smaller} {
		if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET finalized_through=$2 WHERE external_account_id=$1`, account, boundary); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	// The seed helper requests through next_attempt_at plus one hour. Queued,
	// backed off with a proof-pending error and three attempts, its window
	// two hours past the boundary: the shape a continuously-consuming
	// account's job has on a backup taken during a catch-up.
	seedEligibilityProjectionJobRowWithAttempts(t, store, ctx, larger, "queued", 3, strPtr("BALANCE_PROOF_PENDING"), now, now, boundary.Add(time.Hour))
	largerWindow := boundary.Add(2 * time.Hour)
	// And one whose window fell an hour behind the boundary a later
	// publication moved (RC92's shape).
	seedEligibilityProjectionJobRowWithAttempts(t, store, ctx, smaller, "queued", 1, strPtr("BALANCE_PROOF_PENDING"), now, now, boundary.Add(-2*time.Hour))

	enqueued, err := store.EnqueueEligibilityShadowReprojection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var accounts int64
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if enqueued != accounts {
		t.Fatalf("a retargeted row must count as enqueued: expected %d, got %d", accounts, enqueued)
	}
	check := func(account string, want time.Time) {
		t.Helper()
		var status string
		var lastError *string
		var attempts int64
		var requested, next time.Time
		if err := store.pool.QueryRow(ctx, `SELECT status,last_error_code,attempt_count,requested_through,next_attempt_at
			FROM eligibility_projection_jobs WHERE external_account_id=$1`, account).Scan(&status, &lastError, &attempts, &requested, &next); err != nil {
			t.Fatal(err)
		}
		if status != "queued" || lastError != nil || attempts != 0 || !requested.Equal(want) || next.After(time.Now().UTC()) {
			t.Fatalf("%s: status=%q err=%v attempts=%d requested=%s (want %s) next=%s", account, status, lastError, attempts, requested, want, next)
		}
	}
	check(larger, largerWindow)
	check(smaller, boundary)
	claimable, err := store.EligibilityProjectionClaimableCount(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claimable != accounts {
		t.Fatalf("expected every job claimable at once (%d), got %d", accounts, claimable)
	}
}

func TestEligibilityShadowPendingJobsListsNonDeadRowsWithTheirReason(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	pendingAccount := "60000000-0000-4000-8000-0000000000f1"
	deadAccount := "60000000-0000-4000-8000-0000000000f2"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, pendingAccount, "pending-listing", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, deadAccount, "dead-listing", cutover, manifestHash, configHash)
	now := time.Now().UTC().Truncate(time.Second)
	seedEligibilityProjectionJobRowWithAttempts(t, store, ctx, pendingAccount, "queued", 2, strPtr("BALANCE_PROOF_PENDING"), now, now, now.Add(5*time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, deadAccount, "dead", strPtr("PROJECTION_DEAD"), now, now, now)

	pending, err := store.EligibilityShadowPendingJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Account, status and reason are what a reader needs; the attempt count is
	// whatever the row holds (the shared seed helper sets it itself) and is
	// only asserted to be carried through, not to be a particular number.
	if len(pending) != 1 || pending[0].ExternalAccountID != pendingAccount || pending[0].LastErrorCode != "BALANCE_PROOF_PENDING" || pending[0].Status != "queued" || pending[0].AttemptCount <= 0 {
		t.Fatalf("expected exactly the queued proof-pending job, got %+v", pending)
	}
}

// XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3: the evidence-pass boundary.
func TestEvidenceBatchBoundaryCutsAtTheLimitThItemAndKeepsASharedInstantWhole(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	account := "60000000-0000-4000-8000-0000000000a9"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, account, "batch-boundary", cutover, manifestHash, configHash)
	first := cutover.Add(1 * time.Hour)
	second := cutover.Add(2 * time.Hour)
	third := cutover.Add(3 * time.Hour)
	seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, account, manifestHash, configHash, first)
	seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, account, manifestHash, configHash, second)
	seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, account, manifestHash, configHash, third)
	through := cutover.Add(4 * time.Hour)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Unbounded: the window is untouched.
	got, err := evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 0)
	if err != nil || !got.Equal(through) {
		t.Fatalf("limit 0 must leave the window alone: got %s err %v", got, err)
	}
	// Limit larger than the pile: untouched.
	got, err = evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 3)
	if err != nil || !got.Equal(through) {
		t.Fatalf("limit >= pending must leave the window alone: got %s err %v", got, err)
	}
	// Limit 2 of 3: cut at the second item's as_of.
	got, err = evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 2)
	if err != nil || !got.Equal(second) {
		t.Fatalf("limit 2 must cut at the second as_of %s: got %s err %v", second, got, err)
	}
	// Limit 1 of 3: cut at the first.
	got, err = evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 1)
	if err != nil || !got.Equal(first) {
		t.Fatalf("limit 1 must cut at the first as_of %s: got %s err %v", first, got, err)
	}
	// A second item sharing the first as_of: limit 1 still cuts at that
	// instant, and the cut includes both (the evaluation pass selects by
	// as_of <= boundary, so a shared instant is never split).
	seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, account, manifestHash, configHash, first)
	got, err = evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 1)
	if err != nil || !got.Equal(first) {
		t.Fatalf("a shared instant must be kept whole at %s: got %s err %v", first, got, err)
	}
	// An already-evaluated item does not count: with one of the four
	// evaluated, three remain, and limit 3 fits them all -- untouched --
	// while limit 2 cuts at the second remaining item's as_of.
	var firstID string
	if err = store.pool.QueryRow(ctx, `SELECT id FROM balance_reconciliation_checkpoints WHERE external_account_id=$1 ORDER BY as_of,id LIMIT 1`, account).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, firstID, 1, "matched")
	got, err = evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 3)
	if err != nil || !got.Equal(through) {
		t.Fatalf("with one item evaluated, 3 pending remain and limit 3 fits: got %s err %v", got, err)
	}
	got, err = evidenceBatchBoundaryTx(ctx, tx, account, cutover, through, 2)
	if err != nil || !got.Equal(second) {
		t.Fatalf("with one item evaluated, limit 2 cuts at %s: got %s err %v", second, got, err)
	}
}

// The differential rehearsal needs a pile to evaluate; on a copy every
// evaluation already exists. Clearing must respect the anchor floor (an
// evaluation before a POLICY_ANCHOR account's cutover is not this account's
// evidence and is left alone) and count what it removed.
func TestEligibilityShadowReevaluateEvidenceClearsEvaluationsAtOrAfterTheAnchorFloor(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	account := "60000000-0000-4000-8000-0000000000c9"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, account, "reevaluate", cutover, manifestHash, configHash)
	// bootstrap_kind is guarded by the trust-boundary trigger; the fixture
	// lifts it for this one statement the same way the feature under test
	// lifts the evaluation triggers -- superuser session, replica role,
	// transaction-local.
	anchorTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = anchorTx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err = anchorTx.Exec(ctx, `UPDATE source_account_eligibility_state SET bootstrap_kind='POLICY_ANCHOR' WHERE external_account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	if err = anchorTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, account, manifestHash, configHash, cutover.Add(-time.Hour))
	after := seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, account, manifestHash, configHash, cutover.Add(time.Hour))
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, before, 1, "matched")
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, after, 1, "matched")
	// The account has published past its evidence, as production would have,
	// and --reproject-all has already queued its job at that boundary.
	published := cutover.Add(2 * time.Hour)
	if _, err = store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET finalized_through=$2 WHERE external_account_id=$1`, account, published); err != nil {
		t.Fatal(err)
	}
	if _, err = store.EnqueueEligibilityShadowReprojection(ctx); err != nil {
		t.Fatal(err)
	}

	cleared, err := store.EligibilityShadowReevaluateEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Fatalf("expected exactly the post-anchor evaluation cleared, got %d", cleared)
	}
	// The published boundary is left where production put it (RC95: rewinding
	// it made the bounded replay fail at commit on real cash accounts), and
	// the queued job keeps the window --reproject-all gave it.
	var finalized, requested time.Time
	if err = store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state WHERE external_account_id=$1`, account).Scan(&finalized); err != nil {
		t.Fatal(err)
	}
	if !finalized.Equal(published) {
		t.Fatalf("finalized_through must stay at the published boundary %s, got %s", published, finalized)
	}
	if err = store.pool.QueryRow(ctx, `SELECT requested_through FROM eligibility_projection_jobs WHERE external_account_id=$1`, account).Scan(&requested); err != nil {
		t.Fatal(err)
	}
	if !requested.Equal(published) {
		t.Fatalf("the queued job must keep the old boundary %s as its window, got %s", published, requested)
	}
	var remaining int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, before).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("the pre-anchor evaluation must be left alone, got %d rows", remaining)
	}
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, after).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("the post-anchor evaluation must be gone, got %d rows", remaining)
	}
}

// The RC87 post-deploy repair, replayed on the copy: only the named accounts,
// only the key, counted.
func TestEligibilityShadowReleaseCatchupClearsOnlyTheNamedAccountsKeys(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	stuck := "60000000-0000-4000-8000-0000000000d1"
	other := "60000000-0000-4000-8000-0000000000d2"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, stuck, "release-stuck", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, other, "release-other", cutover, manifestHash, configHash)
	for _, account := range []string{stuck, other} {
		if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET catchup_key_hmac=$2 WHERE external_account_id=$1`, account, "h1:"+testHash("catchup-"+account)); err != nil {
			t.Fatal(err)
		}
	}
	released, err := store.EligibilityShadowReleaseCatchup(ctx, []string{stuck})
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("expected exactly the stuck account released, got %d", released)
	}
	var stuckKey, otherKey *string
	if err = store.pool.QueryRow(ctx, `SELECT catchup_key_hmac FROM source_account_eligibility_state WHERE external_account_id=$1`, stuck).Scan(&stuckKey); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT catchup_key_hmac FROM source_account_eligibility_state WHERE external_account_id=$1`, other).Scan(&otherKey); err != nil {
		t.Fatal(err)
	}
	if stuckKey != nil || otherKey == nil {
		t.Fatalf("release must clear only the named account: stuck=%v other=%v", stuckKey, otherKey)
	}
	if again, err := store.EligibilityShadowReleaseCatchup(ctx, []string{stuck}); err != nil || again != 0 {
		t.Fatalf("a second release must be a no-op: released=%d err=%v", again, err)
	}
}

// The window a finalization pass would request at the copy's last watermarks:
// GREATEST(cutover, min watermark - delay), never below finalized_through,
// never lowering a captured window, skipping accounts finalization skips.
func TestEnqueueEligibilityShadowFinalizationWindowRequestsWhatFinalizationWould(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	behind := "60000000-0000-4000-8000-0000000000d3"
	captured := "60000000-0000-4000-8000-0000000000d4"
	excluded := "60000000-0000-4000-8000-0000000000d5"
	for _, account := range []string{behind, captured, excluded} {
		seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, account, "window-"+account[len(account)-2:], cutover, manifestHash, configHash)
	}
	// Two streams; the earlier watermark bounds the window. The seeded
	// accounts carry a 900 s finalization delay.
	for stream, at := range map[string]time.Time{"usage": cutover.Add(3 * time.Hour), "balances": cutover.Add(2 * time.Hour)} {
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_economic_stream_watermarks(
			source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,1,'cursor',$4)`, sourceID, stream, at, configHash); err != nil {
			t.Fatal(err)
		}
	}
	expected := cutover.Add(2*time.Hour - 900*time.Second)
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET catchup_key_hmac=$2 WHERE external_account_id=$1`, excluded, "h1:"+testHash("catchup-"+excluded)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	// A captured row asking for more than finalization would: kept.
	seedEligibilityProjectionJobRowWithAttempts(t, store, ctx, captured, "queued", 2, strPtr("BALANCE_PROOF_PENDING"), now, now, cutover.Add(4*time.Hour))
	capturedWindow := cutover.Add(5 * time.Hour)

	windowed, err := store.EnqueueEligibilityShadowFinalizationWindow(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The shared test database may hold other tests' sources; the two targets
	// here are the floor, the excluded account is checked by name below.
	if windowed < 2 {
		t.Fatalf("expected at least the two finalization targets queued, got %d", windowed)
	}
	requestedOf := func(account string) (time.Time, bool) {
		var requested time.Time
		err := store.pool.QueryRow(ctx, `SELECT requested_through FROM eligibility_projection_jobs WHERE external_account_id=$1 AND status='queued'`, account).Scan(&requested)
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return requested, true
	}
	if got, ok := requestedOf(behind); !ok || !got.Equal(expected) {
		t.Fatalf("the account behind must be asked through min watermark minus delay %s: got %s present=%v", expected, got, ok)
	}
	if got, ok := requestedOf(captured); !ok || !got.Equal(capturedWindow) {
		t.Fatalf("a larger captured window must be kept %s: got %s present=%v", capturedWindow, got, ok)
	}
	if _, ok := requestedOf(excluded); ok {
		t.Fatal("an account still in catch-up must not be queued, exactly as finalization skips it")
	}
	// With lag, the window is what finalization would have requested that
	// much earlier; a captured larger window is still kept.
	lagged := cutover.Add(2*time.Hour - 900*time.Second - time.Hour)
	if _, err = store.pool.Exec(ctx, `DELETE FROM eligibility_projection_jobs WHERE external_account_id=$1`, behind); err != nil {
		t.Fatal(err)
	}
	if _, err = store.EnqueueEligibilityShadowFinalizationWindow(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, ok := requestedOf(behind); !ok || !got.Equal(lagged) {
		t.Fatalf("with an hour of lag the account behind must be asked through %s: got %s present=%v", lagged, got, ok)
	}
	if got, ok := requestedOf(captured); !ok || !got.Equal(capturedWindow) {
		t.Fatalf("lag must not lower a captured window %s: got %s present=%v", capturedWindow, got, ok)
	}
}
