package postgresstore

import (
	"context"
	"testing"
	"time"
)

// seedAlwaysPendingBalanceProofFixture builds an account whose
// ensureBalanceCarryForwardProofTx deterministically returns
// errBalanceCarryForwardProofPending on every attempt: a usage fact sits in
// the (finalized_through,requested_through] window, and no balances scan
// cycle ever publishes to cover it. No reconciliation checkpoint dated at or
// after the policy start exists, so reanchorLegacyEligibilityAccountTx's own
// candidate lookup finds nothing and safely no-ops (case (b) of design
// XM-INV-POLICY-ANCHOR 2.4) regardless of this row's default (legacy)
// bootstrap_kind -- this fixture goes through the real
// ProcessEligibilityProjectionJobs entry point, not the reanchor-bypassing
// test helper, because these tests are about that entry point's own
// attempt_count/next_attempt_at bookkeeping.
type alwaysPendingBalanceProofFixture struct {
	store               *Store
	ctx                 context.Context
	sourceID, accountID string
}

func seedAlwaysPendingBalanceProofFixture(t *testing.T) alwaysPendingBalanceProofFixture {
	t.Helper()
	store, ctx := integrationStore(t)
	const (
		sourceID  = "15000000-0000-4000-8000-000000000001"
		userID    = "25000000-0000-4000-8000-000000000001"
		accountID = "35000000-0000-4000-8000-000000000001"
	)
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	// The manifest's own cutover must be strictly before the policy start
	// (enforced by trigger); the account's own cutover_at below is anchored
	// well after both, comfortably inside the fixture's usable window.
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	requested := cutover.Add(90 * time.Minute)
	manifestHash := testHash("backoff-manifest")
	configHash := testHash("backoff-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','backoff-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','backoff-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'backoff-user','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, sourceID, manifestHash,
		cutover, configHash, testHash("backoff-baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`, accountID, sourceID,
		cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	// One usage fact inside the requested window with no published balances
	// cycle anywhere near it -- ensureBalanceCarryForwardProofTx's delta-carry
	// query finds no covering cycle and returns errBalanceCarryForwardProofPending
	// deterministically, on every attempt, for as long as this fixture stands.
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_usage_events(
		id,source_instance_id,external_account_id,external_event_id,external_usage_id,
		event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
		billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
		source_revision_hash,observed_at)
		VALUES('45000000-0000-4000-8000-000000000001',$1,$2,'backoff-usage-event','backoff-usage',
		$3,10,'SUB2_BALANCE_1E8',$4,$5,'wallet',TRUE,1,'backoff-usage:1',$3,$6,$3)`,
		sourceID, accountID, cutover.Add(10*time.Minute), manifestHash, configHash,
		testHash("backoff-usage-revision")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_projection_jobs(
		external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now()-interval '1 second')`, accountID, requested); err != nil {
		t.Fatal(err)
	}
	return alwaysPendingBalanceProofFixture{store: store, ctx: ctx, sourceID: sourceID, accountID: accountID}
}

func (f alwaysPendingBalanceProofFixture) jobRow(t *testing.T) (status, lastErrorCode string, attemptCount int, nextAttemptAt time.Time) {
	t.Helper()
	if err := f.store.pool.QueryRow(f.ctx, `SELECT status,COALESCE(last_error_code,''),attempt_count,next_attempt_at
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, f.accountID).Scan(
		&status, &lastErrorCode, &attemptCount, &nextAttemptAt); err != nil {
		t.Fatal(err)
	}
	return
}

// makeImmediatelyClaimable fast-forwards the job's next_attempt_at directly
// (rather than sleeping, or feeding ProcessEligibilityProjectionJobs a
// synthetic future "now" -- which would desynchronize it from the SQL now()
// the two requeue upserts under test compare against in the other test
// below) so the next ProcessEligibilityProjectionJobs call picks it up
// immediately.
func (f alwaysPendingBalanceProofFixture) makeImmediatelyClaimable(t *testing.T) {
	t.Helper()
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE eligibility_projection_jobs
		SET next_attempt_at=now()-interval '1 second' WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
}

// TestBalanceProofPendingBackoffGrowsExponentiallyPerAttempt covers
// XM-INV-PROOF-CONTENTION requirement 1's backoff schedule: 30s, 60s, 120s,
// 240s, 480s, then capped at 10 minutes from the 6th attempt on, keyed off
// the job's own honestly-incrementing attempt_count. Before this slice every
// attempt retried at a flat 30s regardless of how many times it had already
// found the proof unprovable.
func TestBalanceProofPendingBackoffGrowsExponentiallyPerAttempt(t *testing.T) {
	fixture := seedAlwaysPendingBalanceProofFixture(t)
	wantDelays := []time.Duration{
		30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second,
		480 * time.Second, 600 * time.Second, 600 * time.Second,
	}
	for attempt, wantDelay := range wantDelays {
		before := time.Now().UTC()
		processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, before,
			AuditActor{Type: "system", ID: "backoff-test-worker"})
		if err != nil {
			t.Fatalf("attempt %d: ProcessEligibilityProjectionJobs err=%v", attempt+1, err)
		}
		if processed != 0 {
			t.Fatalf("attempt %d: processed=%d, want 0 (BALANCE_PROOF_PENDING is a requeue, not a completion)", attempt+1, processed)
		}
		status, lastErrorCode, attemptCount, nextAttemptAt := fixture.jobRow(t)
		if status != "queued" || lastErrorCode != "BALANCE_PROOF_PENDING" {
			t.Fatalf("attempt %d: status=%s last_error_code=%s, want queued/BALANCE_PROOF_PENDING", attempt+1, status, lastErrorCode)
		}
		if attemptCount != attempt+1 {
			t.Fatalf("attempt %d: attempt_count=%d, want %d (attempt counting must stay honest)", attempt+1, attemptCount, attempt+1)
		}
		gotDelay := nextAttemptAt.Sub(before)
		// ProcessEligibilityProjectionJobs runs real proof-evaluation work
		// between reading "before" and computing next_attempt_at from its
		// own now.UTC() parameter (which equals "before" exactly, passed
		// through unchanged) -- the delay should match the schedule exactly,
		// with a small tolerance only for float64-seconds round-tripping
		// through power(2,...).
		if gotDelay < wantDelay-time.Second || gotDelay > wantDelay+time.Second {
			t.Fatalf("attempt %d: next_attempt_at delay=%s, want %s (attempt_count=%d)", attempt+1, gotDelay, wantDelay, attemptCount)
		}
		fixture.makeImmediatelyClaimable(t)
	}
}

// TestBalanceProofPendingRequeuePreservesDeepBackoffButPullsInShallowOne
// covers XM-INV-PROOF-CONTENTION requirement 1's second half: a new fact for
// the account (the two ON CONFLICT(external_account_id) DO UPDATE requeue
// upserts, in finalizeSourceAccountsTx and ObserveBalanceCheckpoint) must
// not unconditionally reset a BALANCE_PROOF_PENDING job's next_attempt_at to
// "now" -- that collapsed the backoff back to sub-second retries in
// production, since facts kept streaming in for the contended account while
// its proof stayed pending. The exact rule: pull a deep backoff (more than
// balanceProofPendingRequeueResetWindow away) forward to now, but leave an
// already-shallow one alone.
func TestBalanceProofPendingRequeuePreservesDeepBackoffButPullsInShallowOne(t *testing.T) {
	fixture := seedAlwaysPendingBalanceProofFixture(t)
	before := time.Now().UTC()
	if _, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, before,
		AuditActor{Type: "system", ID: "backoff-test-worker"}); err != nil {
		t.Fatal(err)
	}
	_, _, attemptCount, shallowNextAttempt := fixture.jobRow(t)
	if attemptCount != 1 {
		t.Fatalf("attempt_count=%d, want 1", attemptCount)
	}
	if delay := shallowNextAttempt.Sub(before); delay < 25*time.Second || delay > 35*time.Second {
		t.Fatalf("first-attempt delay=%s, want ~30s (test assumes this is well inside the reset window)", delay)
	}

	// Case A: a new-fact upsert while the backoff is shallow (~30s away,
	// inside balanceProofPendingRequeueResetWindow's 5 minutes) must leave
	// next_attempt_at untouched.
	runFinalizeSourceAccountsTx(t, fixture)
	_, _, attemptCountAfterShallow, nextAttemptAfterShallow := fixture.jobRow(t)
	if attemptCountAfterShallow != 1 {
		t.Fatalf("attempt_count changed by a requeue upsert: %d, want 1 (attempt counting must stay honest)", attemptCountAfterShallow)
	}
	if !nextAttemptAfterShallow.Equal(shallowNextAttempt) {
		t.Fatalf("shallow backoff was reset by a new-fact upsert: next_attempt_at=%s, want unchanged %s",
			nextAttemptAfterShallow, shallowNextAttempt)
	}

	// Case B: push the job into a deep backoff (simulating several more
	// failed attempts without actually running them) and prove a new-fact
	// upsert now does pull next_attempt_at forward to (approximately) now.
	if _, err := fixture.store.pool.Exec(fixture.ctx, `UPDATE eligibility_projection_jobs
		SET next_attempt_at=now()+interval '10 minutes' WHERE external_account_id=$1`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	beforeDeepUpsert := time.Now().UTC()
	runFinalizeSourceAccountsTx(t, fixture)
	statusAfterDeep, lastErrorAfterDeep, attemptCountAfterDeep, nextAttemptAfterDeep := fixture.jobRow(t)
	if statusAfterDeep != "queued" {
		t.Fatalf("status after deep-backoff requeue=%s, want queued", statusAfterDeep)
	}
	// The requeue upsert's status/lease_token reset is unconditional (as
	// before this slice); only next_attempt_at's preserve-vs-reset decision
	// is new. last_error_code is likewise left alone by the upsert itself.
	if lastErrorAfterDeep != "BALANCE_PROOF_PENDING" {
		t.Fatalf("last_error_code after deep-backoff requeue=%s, want BALANCE_PROOF_PENDING (upsert must not touch it)", lastErrorAfterDeep)
	}
	if attemptCountAfterDeep != 1 {
		t.Fatalf("attempt_count changed by a requeue upsert: %d, want 1 (attempt counting must stay honest)", attemptCountAfterDeep)
	}
	if nextAttemptAfterDeep.Before(beforeDeepUpsert.Add(-2*time.Second)) || nextAttemptAfterDeep.After(beforeDeepUpsert.Add(2*time.Second)) {
		t.Fatalf("deep backoff was not pulled forward: next_attempt_at=%s, want ~now (%s)", nextAttemptAfterDeep, beforeDeepUpsert)
	}
}

// runFinalizeSourceAccountsTx exercises one of the two ON CONFLICT DO UPDATE
// requeue upserts (finalizeSourceAccountsTx, the source-wide one fired
// whenever all four of a source's stream watermarks are present) directly,
// in its own real transaction, standing in for "a new fact was observed for
// this account" without needing to drive the whole ingest pipeline. The
// second requeue upsert (inside ObserveBalanceCheckpoint) uses the
// byte-for-byte identical CASE expression, see consumption.go.
func runFinalizeSourceAccountsTx(t *testing.T, fixture alwaysPendingBalanceProofFixture) {
	t.Helper()
	tx, err := fixture.store.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	configHash := testHash("backoff-config")
	if _, err = tx.Exec(fixture.ctx, `INSERT INTO source_economic_stream_watermarks(
		source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,'payments',now(),1,'c',$2),($1,'usage',now(),1,'c',$2),
			($1,'credits',now(),1,'c',$2),($1,'balances',now(),1,'c',$2)
		ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET watermark_at=EXCLUDED.watermark_at`,
		fixture.sourceID, configHash); err != nil {
		t.Fatal(err)
	}
	if err = finalizeSourceAccountsTx(fixture.ctx, tx, fixture.sourceID,
		AuditActor{Type: "system", ID: "backoff-test-new-fact"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
}
