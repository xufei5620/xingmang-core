package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// preAnchorUsageFixture bootstraps a POLICY_ANCHOR account through the real
// observe pipeline (ObserveBalanceCheckpoint on a no-state account, per
// design XM-INV-POLICY-ANCHOR 2.1), then provisions the usage and credits
// streams so a caller can drive ObserveUsageEvent/ObserveCreditEvent for
// facts dated before or after the account's own cutover_at.
type preAnchorUsageFixture struct {
	store        *Store
	ctx          context.Context
	sourceID     string
	accountID    string
	chain        *v3TestChain
	manifestHash string
	configHash   string
	policyStart  time.Time
	cutoverAt    time.Time // the account's own POLICY_ANCHOR anchor point
}

func newPreAnchorUsageFixture(t *testing.T, suffix string) preAnchorUsageFixture {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-4 * time.Hour)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-0000000002" + suffix
	userID := "20000000-0000-4000-8000-0000000002" + suffix
	accountID := "30000000-0000-4000-8000-0000000002" + suffix
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','pre-anchor-usage-test-`+suffix+`','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','pre-anchor-usage-user-`+suffix+`')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'`+suffix+`','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}

	// The manifest's own cutover must predate the policy start, matching the
	// production invariant RegisterCutoverManifest and its DB trigger both
	// enforce.
	manifestCutover := policyStart.Add(-10 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-0000000002" + suffix,
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-manifest-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: manifestCutover}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-0000000010"+suffix, manifestCutover, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("pre-anchor-usage-manifest-" + suffix)
	configHash := testHash("pre-anchor-usage-config-" + suffix)
	snapshotHash := testHash("pre-anchor-usage-snapshot-" + suffix)
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: snapshotHash,
		BaselineSnapshotHash: snapshotHash, BaselineRowCount: 0, SigningKeyID: "ignored-payload-key",
		CutoverAt: manifestCutover, DatabaseClock: manifestCutover, StreamWatermarkAt: manifestCutover,
		ExternalEventID: manifestEvent.EventID, BatchID: manifestCycle.batchID,
		ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)

	// A reconciliation checkpoint at/after the policy start bootstraps the
	// account directly via POLICY_ANCHOR: this is the account's cutover_at,
	// matching the incident's 106 accounts bootstrapped by the 2026-09-02
	// balances cycle.
	anchorAsOf := policyStart.Add(2 * time.Hour)
	anchorEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-0000000003" + suffix,
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-anchor-event-" + suffix),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: anchorAsOf}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-0000000011"+suffix, anchorAsOf, []SourceBatchEvent{anchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: suffix, ExternalEventID: anchorEvent.EventID,
		CheckpointID: "reconcile-anchor-" + suffix, CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "2000", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAsOf, ObservedAt: anchorAsOf, StreamWatermarkAt: anchorAsOf,
		SourceCursor: "balance:" + suffix + ":anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("policy anchor bootstrap: %v", err)
	}
	var bootstrapKind string
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&bootstrapKind); err != nil || bootstrapKind != "POLICY_ANCHOR" {
		t.Fatalf("fixture account did not bootstrap POLICY_ANCHOR: kind=%q err=%v", bootstrapKind, err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", anchorCycle)

	return preAnchorUsageFixture{store: store, ctx: ctx, sourceID: sourceID, accountID: accountID,
		chain: chain, manifestHash: manifestHash, configHash: configHash,
		policyStart: policyStart, cutoverAt: anchorAsOf}
}

func (f preAnchorUsageFixture) openFreezeCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, f.accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f preAnchorUsageFixture) auditCount(t *testing.T, action string) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action=$1 AND object_id=$2`, action, f.accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze covers the
// XM-INV-PREANCHOR-USAGE incident's own mechanism (design 2.6): a usage fact
// dated at/before a POLICY_ANCHOR account's own cutover_at must be skipped --
// not frozen SOURCE_GAP -- since it cannot be attributed to any ledger
// baseline by construction. ObserveUsageEvent must return nil so the caller
// marks the source event processed, not PROJECTION_FAILED/dead.
//
// preUsageAt is dated before the policy start itself, not merely before the
// fixture's own (pre-XM-INV-ELIG-POLICY-START-ANCHOR-shaped) anchor
// checkpoint: XM-INV-ELIG-POLICY-START-ANCHOR (design XM-INV-ELIG-SIMPLIFY
// section 3(D)) made cutover_at unconditionally equal to the global policy
// start for every account this fixture's own ObserveBalanceCheckpoint call
// now bootstraps, so the original incident's exact shape -- a fact after the
// policy start but at/before the account's own (later) cutover_at -- is the
// very dead zone that slice eliminates; such a fact is now correctly
// persisted and projected normally instead (see
// policy_start_anchor_integration_test.go's own
// TestPolicyStartBootstrapIncludesInWindowCashFundingLot for that positive
// case). This test's own mechanism (2.6's skip-without-freeze branch) is
// still live and still needed for a fact genuinely at/before the policy
// start itself, exercised here.
func TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze(t *testing.T) {
	f := newPreAnchorUsageFixture(t, "40")
	if err := f.store.ProvisionSourceStream(f.ctx, f.sourceID, "usage", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	preUsageAt := f.policyStart.Add(-30 * time.Minute)
	if !preUsageAt.Before(f.policyStart) {
		t.Fatalf("fixture bug: pre-usage time %s must be before policy start %s", preUsageAt, f.policyStart)
	}
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000440",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-pre-fact-40"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: preUsageAt}
	usageCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000004440", preUsageAt, []SourceBatchEvent{usageEvent})

	err := f.store.ObserveUsageEvent(f.ctx, UsageObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "40", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "pre-anchor-usage-fact-40", EventTime: preUsageAt, ObservedAt: preUsageAt,
		StreamWatermarkAt: preUsageAt, ServiceUnits: "50", UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
		SourceCursor: "usage:40:pre", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID})
	if err != nil {
		t.Fatalf("pre-anchor usage fact was not skipped cleanly: %v", err)
	}
	if got := f.openFreezeCount(t); got != 0 {
		t.Fatalf("pre-anchor usage fact froze the account: open freezes=%d", got)
	}
	var factCount int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM source_usage_events
		WHERE external_account_id=$1 AND external_usage_id='pre-anchor-usage-fact-40'`,
		f.accountID).Scan(&factCount); err != nil {
		t.Fatal(err)
	}
	if factCount != 0 {
		t.Fatalf("pre-anchor usage fact was persisted as a ledger-visible fact: count=%d", factCount)
	}
	if got := f.auditCount(t, "eligibility.usage.pre_anchor_skipped"); got != 1 {
		t.Fatalf("pre-anchor usage skip audit missing: count=%d", got)
	}
}

// TestPolicyAnchorAccountPreCutoverCreditFactSkippedWithoutFreeze mirrors the
// usage case for a non-cash credit fact (observeEligibilityFact is shared by
// both ObserveUsageEvent and ObserveCreditEvent). See
// TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze's own doc
// comment for why preCreditAt is dated before the policy start itself, not
// merely before the fixture's own anchor checkpoint.
func TestPolicyAnchorAccountPreCutoverCreditFactSkippedWithoutFreeze(t *testing.T) {
	f := newPreAnchorUsageFixture(t, "41")
	if err := f.store.ProvisionSourceStream(f.ctx, f.sourceID, "credits", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	preCreditAt := f.policyStart.Add(-30 * time.Minute)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000441",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-pre-fact-41"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: preCreditAt}
	creditCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "credits",
		"84000000-0000-4000-8000-000000004441", preCreditAt, []SourceBatchEvent{creditEvent})

	err := f.store.ObserveCreditEvent(f.ctx, CreditObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "41", ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "pre-anchor-credit-fact-41", EventTime: preCreditAt, ObservedAt: preCreditAt,
		StreamWatermarkAt: preCreditAt, ServiceUnits: "20", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		SourceCursor: "credit:41:pre", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID})
	if err != nil {
		t.Fatalf("pre-anchor credit fact was not skipped cleanly: %v", err)
	}
	if got := f.openFreezeCount(t); got != 0 {
		t.Fatalf("pre-anchor credit fact froze the account: open freezes=%d", got)
	}
	var factCount int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM source_credit_events
		WHERE external_account_id=$1 AND external_credit_id='pre-anchor-credit-fact-41'`,
		f.accountID).Scan(&factCount); err != nil {
		t.Fatal(err)
	}
	if factCount != 0 {
		t.Fatalf("pre-anchor credit fact was persisted as a ledger-visible fact: count=%d", factCount)
	}
	if got := f.auditCount(t, "eligibility.credit.pre_anchor_skipped"); got != 1 {
		t.Fatalf("pre-anchor credit skip audit missing: count=%d", got)
	}
}

// TestPolicyAnchorAccountPostCutoverUsageFactAllocatesNormally confirms the
// fix is scoped exactly to facts at/before cutover_at: a fact after it must
// still freeze-free flow through to a normal, allocated ledger fact.
func TestPolicyAnchorAccountPostCutoverUsageFactAllocatesNormally(t *testing.T) {
	f := newPreAnchorUsageFixture(t, "42")
	if err := f.store.ProvisionSourceStream(f.ctx, f.sourceID, "usage", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ProvisionSourceStream(f.ctx, f.sourceID, "credits", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	postAt := f.cutoverAt.Add(1 * time.Hour)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000442",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-post-credit-42"),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: postAt}
	creditCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "credits",
		"84000000-0000-4000-8000-000000004442", postAt, []SourceBatchEvent{creditEvent})
	if err := f.store.ObserveCreditEvent(f.ctx, CreditObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "42", ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "post-anchor-credit-42", EventTime: postAt, ObservedAt: postAt,
		StreamWatermarkAt: postAt, ServiceUnits: "80", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		SourceCursor: "credit:42:post", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}

	usageAt := postAt.Add(1 * time.Hour)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000443",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-post-fact-42"),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: usageAt}
	usageCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000004443", usageAt, []SourceBatchEvent{usageEvent})
	usageID := "post-anchor-usage-42"
	if err := f.store.ObserveUsageEvent(f.ctx, UsageObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "42", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: usageID, EventTime: usageAt, ObservedAt: usageAt,
		StreamWatermarkAt: usageAt, ServiceUnits: "30", UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
		SourceCursor: "usage:42:post", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	if got := f.openFreezeCount(t); got != 0 {
		t.Fatalf("post-anchor facts unexpectedly froze the account: open freezes=%d", got)
	}
	// buildEligibilityProjectionTx keys allocations by source_usage_events'
	// own internal id (a generated UUID), not the external_usage_id supplied
	// to ObserveUsageEvent -- look up the row ObserveUsageEvent persisted to
	// get the id the projection will report.
	var persistedUsageRowID string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT id FROM source_usage_events
		WHERE external_account_id=$1 AND external_usage_id=$2`, f.accountID, usageID).Scan(&persistedUsageRowID); err != nil {
		t.Fatalf("post-anchor usage fact was not persisted: %v", err)
	}

	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	account, err := getEligibilityAccountTx(f.ctx, tx, f.accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildEligibilityProjectionTx(f.ctx, tx, account, usageAt.Add(1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, allocation := range projection.Allocations {
		if allocation.UsageID == persistedUsageRowID {
			found = true
		}
	}
	if !found {
		t.Fatal("post-anchor usage fact was not allocated")
	}
}

// TestLegacyAccountPreCutoverUsageFactStillFreezesSourceGap is the
// regression guard: a legacy (SIGNED_CUTOVER) account's pre-cutover usage
// fact is real evidence of a source gap (the cutover replays real history,
// unlike a POLICY_ANCHOR checkpoint anchor) and must keep freezing exactly
// as before this slice.
func TestLegacyAccountPreCutoverUsageFactStillFreezesSourceGap(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-4 * time.Hour)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000243"
	userID := "20000000-0000-4000-8000-000000000243"
	accountID := "30000000-0000-4000-8000-000000000243"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','pre-anchor-usage-legacy-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','pre-anchor-usage-legacy-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'243','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "usage", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}

	manifestCutover := policyStart.Add(-10 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000243",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-legacy-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: manifestCutover}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"84000000-0000-4000-8000-000000005243", manifestCutover, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("pre-anchor-usage-legacy-manifest")
	configHash := testHash("pre-anchor-usage-legacy-config")
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: testHash("pre-anchor-usage-legacy-snapshot"),
		BaselineSnapshotHash: testHash("pre-anchor-usage-legacy-snapshot"), BaselineRowCount: 0,
		SigningKeyID: "ignored-payload-key", CutoverAt: manifestCutover, DatabaseClock: manifestCutover,
		StreamWatermarkAt: manifestCutover, ExternalEventID: manifestEvent.EventID, BatchID: manifestCycle.batchID,
		ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)

	// bootstrap_kind is intentionally omitted -- its DEFAULT is
	// 'SIGNED_CUTOVER' (migration 0009), and no production code path creates
	// a new SIGNED_CUTOVER row after design XM-INV-POLICY-ANCHOR shipped, so
	// direct SQL is how every legacy-kind fixture in this package is built
	// now (see store_integration_test.go's processEligibilityWithoutReanchor
	// doc comment for the same pattern).
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`, accountID, sourceID,
		manifestCutover, manifestHash); err != nil {
		t.Fatal(err)
	}

	preUsageAt := manifestCutover.Add(-1 * time.Hour)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000244",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("pre-anchor-usage-legacy-fact"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: preUsageAt}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage",
		"84000000-0000-4000-8000-000000005244", preUsageAt, []SourceBatchEvent{usageEvent})

	err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: "243", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "legacy-pre-cutover-usage", EventTime: preUsageAt, ObservedAt: preUsageAt,
		StreamWatermarkAt: preUsageAt, ServiceUnits: "50", UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
		SourceCursor: "usage:243:pre", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err == nil {
		t.Fatal("legacy pre-cutover usage fact was not rejected")
	}
	var freezeReason, triggerObjectType string
	if scanErr := store.pool.QueryRow(ctx, `SELECT freeze_reason,trigger_object_type FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&freezeReason, &triggerObjectType); scanErr != nil {
		t.Fatalf("legacy pre-cutover usage fact did not freeze SOURCE_GAP as before: %v (observe err=%v)", scanErr, err)
	}
	if freezeReason != "SOURCE_GAP" || triggerObjectType != "usage" {
		t.Fatalf("unexpected freeze reason=%q object_type=%q", freezeReason, triggerObjectType)
	}
}
