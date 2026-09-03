package postgresstore

import (
	"context"
	"testing"
	"time"
)

// seedEligibilityProjectionHealthAccount inserts the minimal
// source_instances/invoice_users/external_accounts/source_cutover_manifests/
// source_account_eligibility_state chain that eligibility_projection_jobs'
// foreign key (and the row-level cutover-contract trigger) require, reusing
// the same shape as seedAlwaysPendingBalanceProofFixture in
// proof_contention_backoff_integration_test.go. sourceID is shared across
// every account this test seeds (source_cutover_manifests is keyed one row
// per source instance), so only accountID/externalUserID vary per call.
func seedEligibilityProjectionHealthAccount(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, externalUserID string, cutover time.Time, manifestHash, configHash string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2) ON CONFLICT DO NOTHING`, accountID, "user-"+externalUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$1,$2,$3,$4,'test','verified')`, accountID, sourceID, externalUserID, testHash(externalUserID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`, accountID, sourceID,
		cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	_ = configHash
}

// seedEligibilityProjectionJobRow writes an eligibility_projection_jobs row
// with fully explicit bookkeeping columns, bypassing the application code
// paths that normally produce them (the claim UPDATE, the backoff requeue,
// and the two ON CONFLICT upserts) so each health scenario below can pin
// created_at/updated_at/next_attempt_at exactly, independent of real
// wall-clock timing.
func seedEligibilityProjectionJobRow(t *testing.T, store *Store, ctx context.Context, accountID string, status string, lastErrorCode *string, createdAt, updatedAt, nextAttemptAt time.Time) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(
			external_account_id,requested_through,status,attempt_count,next_attempt_at,
			last_error_code,created_at,updated_at)
		VALUES($1,$2,$3,3,$4,$5,$6,$7)`,
		accountID, nextAttemptAt.Add(time.Hour), status, nextAttemptAt, lastErrorCode, createdAt, updatedAt); err != nil {
		t.Fatal(err)
	}
}

func strPtr(v string) *string { return &v }

// seedEligibilityProjectionJobRowProcessing writes a 'processing' row with an
// explicit lease, for XM-INV-READY-LEASE's live-lease exclusion tests below.
// Unlike seedEligibilityProjectionJobRow, this always sets
// lease_token/lease_expires_at: the table's own CHECK constraint requires
// both non-NULL exactly when status='processing'.
func seedEligibilityProjectionJobRowProcessing(t *testing.T, store *Store, ctx context.Context, accountID string, createdAt, updatedAt, leaseExpiresAt time.Time) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(
			external_account_id,requested_through,status,attempt_count,next_attempt_at,
			last_error_code,created_at,updated_at,lease_token,lease_expires_at)
		VALUES($1,$2,'processing',3,$2,'BALANCE_PROOF_PENDING',$3,$4,$5,$6)`,
		accountID, updatedAt, createdAt, updatedAt, "test-lease-"+accountID, leaseExpiresAt); err != nil {
		t.Fatal(err)
	}
}

// seedEligibilityProjectionHealthSource sets up the minimal
// source_instances/source_cutover_manifests fixture that
// seedEligibilityProjectionHealthAccount's foreign keys need, returning the
// source id, a cutover time safely before the fixture's eligibility policy
// start, and the manifest/config hashes to pass through. Factored out for
// XM-INV-READY-LEASE's four single-account scenarios below, each of which
// needs this exact boilerplate in its own fresh schema -- integrationStore(t)
// resets the schema per test, so reusing one literal source id across them is
// safe.
func seedEligibilityProjectionHealthSource(t *testing.T, store *Store, ctx context.Context) (sourceID string, cutover time.Time, manifestHash, configHash string) {
	t.Helper()
	sourceID = "55000000-0000-4000-8000-000000000001"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','ready-lease-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover = policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	manifestHash = testHash("ready-lease-manifest")
	configHash = testHash("ready-lease-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, sourceID, manifestHash,
		cutover, configHash, testHash("ready-lease-baseline")); err != nil {
		t.Fatal(err)
	}
	return sourceID, cutover, manifestHash, configHash
}

// TestEligibilityProjectionHealthLiveLeaseProcessingExcludedFromStuck is the
// XM-INV-READY-LEASE regression test reproducing the confirmed 2026-09-03
// 03:17:24Z production false positive: ProcessEligibilityProjectionJobs'
// batch claim stamps every claimed row's updated_at with one shared now, then
// works through the batch serially (each account up to 300s) -- so a row
// still waiting its turn sits status='processing' with an increasingly stale
// updated_at, even though its lease (claimed 30s ago, 2-minute TTL) is still
// live and the worker has not abandoned it. Must not count as stuck.
func TestEligibilityProjectionHealthLiveLeaseProcessingExcludedFromStuck(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	const accountID = "65000000-0000-4000-8000-000000000011"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "live-lease", cutover, manifestHash, configHash)

	now := time.Now().UTC().Truncate(time.Microsecond)
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, accountID,
		now.Add(-20*time.Minute), now.Add(-20*time.Minute), now.Add(30*time.Second))

	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Processing != 1 {
		t.Errorf("Processing=%d want 1", health.Processing)
	}
	if !health.OldestPending.IsZero() {
		t.Errorf("OldestPending=%s want zero (live-lease processing row must not count as stuck)", health.OldestPending)
	}
}

// TestEligibilityProjectionHealthLapsedLeaseProcessingCountsAsStuck is the
// control for the test above: same shape, except the lease itself (the
// worker crashed, or the process died mid-batch) expired 1 minute ago. This
// is a genuinely abandoned job and must still count toward OldestPending
// exactly as before this slice.
func TestEligibilityProjectionHealthLapsedLeaseProcessingCountsAsStuck(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	const accountID = "65000000-0000-4000-8000-000000000012"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "lapsed-lease", cutover, manifestHash, configHash)

	now := time.Now().UTC().Truncate(time.Microsecond)
	wantOldestPending := now.Add(-20 * time.Minute)
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, accountID,
		wantOldestPending, wantOldestPending, now.Add(-1*time.Minute))

	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Processing != 1 {
		t.Errorf("Processing=%d want 1", health.Processing)
	}
	if health.OldestPending.IsZero() {
		t.Fatal("OldestPending is zero, want the lapsed-lease row's updated_at")
	}
	if diff := health.OldestPending.Sub(wantOldestPending); diff < -time.Second || diff > time.Second {
		t.Errorf("OldestPending=%s want ~%s", health.OldestPending, wantOldestPending)
	}
}

// TestEligibilityProjectionHealthGraceWindowProofPendingExcludedFromStuck
// reproduces the incident's secondary contributor: next_attempt_at elapsed
// only 5 seconds ago, well inside the reclaim grace window
// (eligibilityProjectionReclaimGraceSeconds) before the 2-second
// eligibility-projection worker ticks around to actually reclaim and retry
// it. Must still count as ProofPending, carrying its previous attempt's
// (stale) updated_at, not as stuck.
func TestEligibilityProjectionHealthGraceWindowProofPendingExcludedFromStuck(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	const accountID = "65000000-0000-4000-8000-000000000013"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "grace-window", cutover, manifestHash, configHash)

	now := time.Now().UTC().Truncate(time.Microsecond)
	wantOldestProofPending := now.Add(-20 * time.Minute)
	seedEligibilityProjectionJobRow(t, store, ctx, accountID,
		"queued", strPtr("BALANCE_PROOF_PENDING"),
		wantOldestProofPending.Add(-time.Hour), wantOldestProofPending, now.Add(-5*time.Second))

	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Queued != 1 {
		t.Errorf("Queued=%d want 1", health.Queued)
	}
	if !health.OldestPending.IsZero() {
		t.Errorf("OldestPending=%s want zero (grace-window proof-pending row must not count as stuck)", health.OldestPending)
	}
	if health.ProofPending != 1 {
		t.Errorf("ProofPending=%d want 1", health.ProofPending)
	}
	if health.OldestProofPending.IsZero() {
		t.Fatal("OldestProofPending is zero, want this row's updated_at")
	}
	if diff := health.OldestProofPending.Sub(wantOldestProofPending); diff < -time.Second || diff > time.Second {
		t.Errorf("OldestProofPending=%s want ~%s", health.OldestProofPending, wantOldestProofPending)
	}
}

// TestEligibilityProjectionHealthGraceElapsedProofPendingCountsAsStuck is the
// control for the test above: next_attempt_at elapsed 10 minutes ago, far
// past any reasonable reclaim grace, so the worker has genuinely missed this
// job's turn. Must count toward OldestPending, not ProofPending, exactly as
// before this slice.
func TestEligibilityProjectionHealthGraceElapsedProofPendingCountsAsStuck(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	const accountID = "65000000-0000-4000-8000-000000000014"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "grace-elapsed", cutover, manifestHash, configHash)

	now := time.Now().UTC().Truncate(time.Microsecond)
	wantOldestPending := now.Add(-20 * time.Minute)
	seedEligibilityProjectionJobRow(t, store, ctx, accountID,
		"queued", strPtr("BALANCE_PROOF_PENDING"),
		wantOldestPending.Add(-time.Hour), wantOldestPending, now.Add(-10*time.Minute))

	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Queued != 1 {
		t.Errorf("Queued=%d want 1", health.Queued)
	}
	if health.OldestPending.IsZero() {
		t.Fatal("OldestPending is zero, want this row's updated_at")
	}
	if diff := health.OldestPending.Sub(wantOldestPending); diff < -time.Second || diff > time.Second {
		t.Errorf("OldestPending=%s want ~%s", health.OldestPending, wantOldestPending)
	}
	if health.ProofPending != 0 {
		t.Errorf("ProofPending=%d want 0 (backoff window already elapsed beyond the grace)", health.ProofPending)
	}
}

// TestEligibilityProjectionHealthSeparatesProofPendingFromStuck is the
// XM-INV-READY-PENDING regression test: a job legitimately waiting on a
// balance proof under XM-INV-PROOF-CONTENTION's exponential backoff (last
// attempt recent, next attempt still in the future) must not count toward
// OldestPending merely because its row is old, while a job that really has
// gone untouched -- whether it has never carried a BALANCE_PROOF_PENDING
// error code, or its own proof-pending backoff window has already elapsed
// without a retry -- must still count. Covers cases (a), (b) and (c) from
// the task brief in one aggregate snapshot, plus the "backoff window
// elapsed" edge the exact predicate (last_error_code=BALANCE_PROOF_PENDING
// AND next_attempt_at in the future) is specifically for.
func TestEligibilityProjectionHealthSeparatesProofPendingFromStuck(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "55000000-0000-4000-8000-000000000001"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','ready-pending-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	manifestHash := testHash("ready-pending-manifest")
	configHash := testHash("ready-pending-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, sourceID, manifestHash,
		cutover, configHash, testHash("ready-pending-baseline")); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	accounts := map[string]string{
		"a-recent-proof-pending":  "65000000-0000-4000-8000-000000000001", // (a)
		"b-stuck-queued":          "65000000-0000-4000-8000-000000000002", // (b)
		"c-failed":                "65000000-0000-4000-8000-000000000003", // (c)
		"d-overdue-proof-pending": "65000000-0000-4000-8000-000000000004", // edge: backoff window elapsed
	}
	for label, accountID := range accounts {
		seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, label, cutover, manifestHash, configHash)
	}

	// (a) created 20 minutes ago (old enough to trip the pre-XM-INV-READY-PENDING
	// created_at rule) but attempted 1 minute ago, with 9 minutes left on an
	// active BALANCE_PROOF_PENDING backoff: legitimately waiting, must not
	// count toward OldestPending.
	seedEligibilityProjectionJobRow(t, store, ctx, accounts["a-recent-proof-pending"],
		"queued", strPtr("BALANCE_PROOF_PENDING"),
		now.Add(-20*time.Minute), now.Add(-1*time.Minute), now.Add(9*time.Minute))

	// (b) a plain queued job (never carried a BALANCE_PROOF_PENDING error
	// code) that has not been touched in 20 minutes: genuinely stuck.
	seedEligibilityProjectionJobRow(t, store, ctx, accounts["b-stuck-queued"],
		"queued", nil,
		now.Add(-25*time.Minute), now.Add(-20*time.Minute), now.Add(-5*time.Minute))

	// (c) a failed job; Failed must reflect it regardless of age.
	seedEligibilityProjectionJobRow(t, store, ctx, accounts["c-failed"],
		"failed", strPtr("PROJECTION_FAILED"),
		now.Add(-2*time.Minute), now.Add(-1*time.Minute), now.Add(4*time.Minute))

	// (d) last attempted 16 minutes ago with last_error_code=BALANCE_PROOF_PENDING,
	// but its own backoff window already elapsed (next_attempt_at in the
	// past) without a retry: the worker missed it, so this must revert to
	// counting toward OldestPending, not ProofPending.
	seedEligibilityProjectionJobRow(t, store, ctx, accounts["d-overdue-proof-pending"],
		"queued", strPtr("BALANCE_PROOF_PENDING"),
		now.Add(-40*time.Minute), now.Add(-16*time.Minute), now.Add(-1*time.Minute))

	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Queued != 3 {
		t.Errorf("Queued=%d want 3 (a,b,d)", health.Queued)
	}
	if health.Failed != 1 {
		t.Errorf("Failed=%d want 1 (c)", health.Failed)
	}
	if health.Processing != 0 {
		t.Errorf("Processing=%d want 0", health.Processing)
	}
	// OldestPending must come from the stuck bucket {b, d} via updated_at,
	// picking b's (the older of the two updated_at values, 20 minutes ago) --
	// never from a's old created_at, and never from c since Failed already
	// carries that signal on its own.
	wantOldestPending := now.Add(-20 * time.Minute)
	if health.OldestPending.IsZero() {
		t.Fatal("OldestPending is zero, want the stuck bucket's oldest updated_at")
	}
	if diff := health.OldestPending.Sub(wantOldestPending); diff < -time.Second || diff > time.Second {
		t.Errorf("OldestPending=%s want ~%s (b's updated_at)", health.OldestPending, wantOldestPending)
	}
	if health.ProofPending != 1 {
		t.Errorf("ProofPending=%d want 1 (only a: d's backoff window already elapsed)", health.ProofPending)
	}
	wantOldestProofPending := now.Add(-1 * time.Minute)
	if health.OldestProofPending.IsZero() {
		t.Fatal("OldestProofPending is zero, want a's updated_at")
	}
	if diff := health.OldestProofPending.Sub(wantOldestProofPending); diff < -time.Second || diff > time.Second {
		t.Errorf("OldestProofPending=%s want ~%s (a's updated_at)", health.OldestProofPending, wantOldestProofPending)
	}
}

// TestEligibilityProjectionHealthEmptyIsZeroValue guards the pre-existing
// all-clear shape: with no rows at all, every count is zero and both oldest
// timestamps report as the zero time.Time, exactly as before this slice
// (COALESCE(...,'epoch'::timestamptz) normalized back to time.Time{}).
func TestEligibilityProjectionHealthEmptyIsZeroValue(t *testing.T) {
	store, ctx := integrationStore(t)
	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Queued != 0 || health.Failed != 0 || health.Processing != 0 || health.ProofPending != 0 {
		t.Fatalf("non-zero counts on an empty table: %+v", health)
	}
	if !health.OldestPending.IsZero() || !health.OldestProofPending.IsZero() {
		t.Fatalf("non-zero oldest timestamps on an empty table: %+v", health)
	}
}
