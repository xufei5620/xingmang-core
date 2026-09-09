package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// repairFixture builds the exact mixed shape the 2026-09-02 incident left
// behind, entirely through DB state (not through observeEligibilityFact,
// which no longer produces these freezes after XM-INV-PREANCHOR-USAGE's
// projection fix -- see docs/handoffs/XM-INV-PREANCHOR-USAGE.md): a
// POLICY_ANCHOR account "A" with two open SOURCE_GAP freezes (usage and
// credit), one open EVENT_DEAD freeze, and three dead/failed
// usage_event/credit_event source_ingest_events correlated to those freezes
// by source_revision_hash/payload_hash; and a legacy (SIGNED_CUTOVER)
// account "B" with one open SOURCE_GAP freeze that is a real gap and must
// never be touched by the repair tool.
type repairFixture struct {
	store             *Store
	ctx               context.Context
	sourceID          string
	accountA          string
	accountB          string
	gapFreezeUsageID  string
	gapFreezeCreditID string
	deadFreezeID      string
	legacyFreezeID    string
	usageEventID      string
	creditEventID     string
	deadEventID       string
}

func newRepairFixture(t *testing.T) repairFixture {
	t.Helper()
	f := newPreAnchorUsageFixture(t, "50")
	if err := f.store.ProvisionSourceStream(f.ctx, f.sourceID, "usage", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ProvisionSourceStream(f.ctx, f.sourceID, "credits", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}

	// Event 1: usage, ends up 'dead' (8 attempts), correlated to a
	// SOURCE_GAP freeze on the fact's own external_usage_id (mirroring
	// freezeEligibilityTx's real trigger_object_id for a usage/credit fact).
	usageAt := f.cutoverAt.Add(-30 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000550",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("repair-fixture-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: usageAt}
	usageCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000005550", usageAt, []SourceBatchEvent{usageEvent})
	// Close out the scan cycle (a stream allows only one active cycle at a
	// time) before this event's own status is overwritten below to
	// dead/failed -- markV3CycleProcessed itself sets processing_status=
	// 'processed', which the next statement immediately supersedes.
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "usage", usageCycle)
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE source_ingest_events
		SET processing_status='dead',attempt_count=8,processing_error='PROJECTION_FAILED',
			lease_token=NULL,lease_expires_at=NULL,processed_at=NULL,next_attempt_at=now()+interval '1 hour'
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		f.sourceID, usageEvent.EventID); err != nil {
		t.Fatal(err)
	}
	gapFreezeUsageID := randomUUID()
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'SOURCE_GAP','usage','repair-fixture-usage-fact',$3)`,
		gapFreezeUsageID, f.accountID, usageEvent.PayloadHash); err != nil {
		t.Fatal(err)
	}

	// Event 2: credit, ends up 'failed' (3 attempts, not yet dead) --
	// exercises requeuing a merely-failed (not dead) event.
	creditAt := f.cutoverAt.Add(-20 * time.Minute)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000551",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("repair-fixture-credit-event"),
		PayloadCiphertext: bytes.Repeat([]byte{8}, 32), ObservedAt: creditAt}
	creditCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "credits",
		"84000000-0000-4000-8000-000000005551", creditAt, []SourceBatchEvent{creditEvent})
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "credits", creditCycle)
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE source_ingest_events
		SET processing_status='failed',attempt_count=3,processing_error='PROJECTION_FAILED',
			lease_token=NULL,lease_expires_at=NULL,processed_at=NULL,next_attempt_at=now()+interval '5 minutes'
		WHERE source_instance_id=$1 AND stream_id='credits' AND event_id=$2`,
		f.sourceID, creditEvent.EventID); err != nil {
		t.Fatal(err)
	}
	gapFreezeCreditID := randomUUID()
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'SOURCE_GAP','credit','repair-fixture-credit-fact',$3)`,
		gapFreezeCreditID, f.accountID, creditEvent.PayloadHash); err != nil {
		t.Fatal(err)
	}

	// Event 3: another usage event, dead, but correlated via the
	// application-layer account hint (design XM-INV-PROOF-CONTENTION 5):
	// trigger_object_type/trigger_object_id point at the ingest event itself
	// (entity type + event id), not at any fact's own external id.
	deadAt := f.cutoverAt.Add(-10 * time.Minute)
	deadEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000552",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("repair-fixture-dead-event"),
		PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: deadAt}
	deadCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "usage",
		"84000000-0000-4000-8000-000000005552", deadAt, []SourceBatchEvent{deadEvent})
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "usage", deadCycle)
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE source_ingest_events
		SET processing_status='dead',attempt_count=8,processing_error='PROJECTION_FAILED',
			lease_token=NULL,lease_expires_at=NULL,processed_at=NULL,next_attempt_at=now()+interval '1 hour'
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		f.sourceID, deadEvent.EventID); err != nil {
		t.Fatal(err)
	}
	deadFreezeID := randomUUID()
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'EVENT_DEAD','usage_event',$3,$4)`,
		deadFreezeID, f.accountID, deadEvent.EventID, deadEvent.PayloadHash); err != nil {
		t.Fatal(err)
	}

	if _, err := f.store.pool.Exec(f.ctx, `UPDATE source_account_eligibility_state
		SET eligibility_status='frozen' WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}

	// Account B: a legacy account on its own, separate source instance, with
	// a real, unrelated SOURCE_GAP freeze. Constructed directly
	// (bootstrap_kind defaults to SIGNED_CUTOVER, migration 0009) since no
	// production code path creates a new SIGNED_CUTOVER row after design
	// XM-INV-POLICY-ANCHOR shipped. A separate source instance (rather than
	// reusing f.sourceID) avoids external_accounts' UNIQUE NULLS NOT
	// DISTINCT (source_instance_id, external_subject_hmac) constraint --
	// both accounts leave external_subject_hmac NULL.
	sourceB := "10000000-0000-4000-8000-000000000551"
	accountB := "30000000-0000-4000-8000-000000000551"
	userB := "20000000-0000-4000-8000-000000000551"
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','repair-fixture-legacy','v3-test')`, sourceB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','repair-fixture-legacy-user')`, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'551','test','verified')`, accountB, userB, sourceB); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ProvisionSourceStream(f.ctx, sourceB, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	legacyManifestCutover := f.policyStart.Add(-10 * time.Hour)
	legacyChain := newV3TestChain()
	legacyManifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000551",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("repair-fixture-legacy-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: legacyManifestCutover}
	legacyManifestCycle := legacyChain.commit(t, f.store, f.ctx, sourceB, "balances",
		"84000000-0000-4000-8000-000000005553", legacyManifestCutover, []SourceBatchEvent{legacyManifestEvent})
	legacyManifestHash := testHash("repair-fixture-legacy-manifest")
	if err := f.store.RegisterCutoverManifest(f.ctx, CutoverManifest{
		SourceInstanceID: sourceB, ManifestHash: legacyManifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: testHash("repair-fixture-legacy-config"),
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0", CreditsCeiling: "c0",
		BalancesCeiling: "b0", BaselineSnapshotID: testHash("repair-fixture-legacy-snapshot"),
		BaselineSnapshotHash: testHash("repair-fixture-legacy-snapshot"), BaselineRowCount: 0,
		SigningKeyID: "ignored-payload-key", CutoverAt: legacyManifestCutover, DatabaseClock: legacyManifestCutover,
		StreamWatermarkAt: legacyManifestCutover, ExternalEventID: legacyManifestEvent.EventID,
		BatchID: legacyManifestCycle.batchID, ScanCycleID: legacyManifestCycle.cycleID,
		SourceRevision: legacyManifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceB}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds,eligibility_status)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900,'frozen')`,
		accountB, sourceB, legacyManifestCutover, legacyManifestHash); err != nil {
		t.Fatal(err)
	}
	legacyFreezeID := randomUUID()
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,'SOURCE_GAP','usage','legacy-real-gap-fact',$3)`,
		legacyFreezeID, accountB, testHash("legacy-real-gap-revision")); err != nil {
		t.Fatal(err)
	}

	return repairFixture{store: f.store, ctx: f.ctx, sourceID: f.sourceID, accountA: f.accountID, accountB: accountB,
		gapFreezeUsageID: gapFreezeUsageID, gapFreezeCreditID: gapFreezeCreditID, deadFreezeID: deadFreezeID,
		legacyFreezeID: legacyFreezeID, usageEventID: usageEvent.EventID, creditEventID: creditEvent.EventID,
		deadEventID: deadEvent.EventID}
}

func (f repairFixture) freezeStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT status FROM eligibility_freezes WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (f repairFixture) ingestStatus(t *testing.T, streamID, eventID string) (status string, attempts int, procErr string) {
	t.Helper()
	var errText *string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT processing_status,attempt_count,processing_error
		FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3`,
		f.sourceID, streamID, eventID).Scan(&status, &attempts, &errText); err != nil {
		t.Fatal(err)
	}
	if errText != nil {
		procErr = *errText
	}
	return status, attempts, procErr
}

func (f repairFixture) accountEligibilityStatus(t *testing.T, accountID string) string {
	t.Helper()
	var status string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func fixedRepairEvidence() PreAnchorUsageRepairInput {
	return PreAnchorUsageRepairInput{
		Apply: true, OperatorID: "70000000-0000-4000-8000-000000000099",
		NoteCiphertext: bytes.Repeat([]byte{1}, 32), NoteHash: testHash("repair-note"),
		EvidenceCiphertext: bytes.Repeat([]byte{2}, 32), EvidenceHash: testHash("repair-evidence"),
	}
}

// TestRepairPreAnchorUsageEligibilityDryRunReportsWithoutMutating verifies
// the dry-run path (design XM-INV-PREANCHOR-USAGE part 3, default mode):
// it must report exactly what apply would do, while leaving every row --
// freezes, ingest events, account status -- untouched.
func TestRepairPreAnchorUsageEligibilityDryRunReportsWithoutMutating(t *testing.T) {
	f := newRepairFixture(t)
	result, err := f.store.RepairPreAnchorUsageEligibility(f.ctx, PreAnchorUsageRepairInput{Apply: false},
		AuditActor{Type: "system", ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("dry run reported Applied=true")
	}
	if result.TotalSourceGapFreezesResolved != 2 || result.TotalEventDeadFreezesResolved != 1 || result.TotalEventsRequeued != 3 {
		t.Fatalf("dry run totals=%+v, want 2/1/3", result)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ExternalAccountID != f.accountA {
		t.Fatalf("dry run accounts=%+v, want exactly account A", result.Accounts)
	}
	acct := result.Accounts[0]
	if acct.SourceGapFreezesResolved != 2 || acct.EventDeadFreezesResolved != 1 || acct.EventsRequeued != 3 || acct.Reactivated {
		t.Fatalf("dry run account summary=%+v", acct)
	}
	// Nothing was actually mutated.
	for _, id := range []string{f.gapFreezeUsageID, f.gapFreezeCreditID, f.deadFreezeID, f.legacyFreezeID} {
		if status := f.freezeStatus(t, id); status != "open" {
			t.Fatalf("dry run mutated freeze %s to status=%s", id, status)
		}
	}
	if status, attempts, _ := f.ingestStatus(t, "usage", f.usageEventID); status != "dead" || attempts != 8 {
		t.Fatalf("dry run mutated usage ingest event: status=%s attempts=%d", status, attempts)
	}
	if status := f.accountEligibilityStatus(t, f.accountA); status != "frozen" {
		t.Fatalf("dry run reactivated account A: status=%s", status)
	}
}

// TestRepairPreAnchorUsageEligibilityApplyResolvesRequeuesAndReactivates is
// the apply-mode happy path: every SOURCE_GAP/EVENT_DEAD freeze this
// incident caused for the POLICY_ANCHOR account is resolved, its dead/failed
// usage/credit ingest events are requeued clean, the account reactivates
// once its last freeze clears, and the legacy account's real gap freeze is
// left completely untouched -- the core safety property of this tool.
func TestRepairPreAnchorUsageEligibilityApplyResolvesRequeuesAndReactivates(t *testing.T) {
	f := newRepairFixture(t)
	result, err := f.store.RepairPreAnchorUsageEligibility(f.ctx, fixedRepairEvidence(),
		AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000099", Reason: "test repair"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.TotalSourceGapFreezesResolved != 2 || result.TotalEventDeadFreezesResolved != 1 || result.TotalEventsRequeued != 3 {
		t.Fatalf("apply totals=%+v", result)
	}
	if len(result.Accounts) != 1 || !result.Accounts[0].Reactivated {
		t.Fatalf("apply accounts=%+v, want account A reactivated", result.Accounts)
	}

	for _, id := range []string{f.gapFreezeUsageID, f.gapFreezeCreditID, f.deadFreezeID} {
		if status := f.freezeStatus(t, id); status != "resolved" {
			t.Fatalf("freeze %s status=%s, want resolved", id, status)
		}
	}
	if status := f.freezeStatus(t, f.legacyFreezeID); status != "open" {
		t.Fatalf("legacy account's real SOURCE_GAP freeze was touched: status=%s", status)
	}
	if status := f.accountEligibilityStatus(t, f.accountB); status != "frozen" {
		t.Fatalf("legacy account was reactivated: status=%s", status)
	}

	for _, tc := range []struct{ stream, eventID string }{
		{"usage", f.usageEventID}, {"credits", f.creditEventID}, {"usage", f.deadEventID},
	} {
		status, attempts, procErr := f.ingestStatus(t, tc.stream, tc.eventID)
		if status != "queued" || attempts != 0 || procErr != "" {
			t.Fatalf("ingest event %s/%s status=%s attempts=%d error=%q, want queued/0/empty",
				tc.stream, tc.eventID, status, attempts, procErr)
		}
	}
	if status := f.accountEligibilityStatus(t, f.accountA); status != "active" {
		t.Fatalf("account A eligibility_status=%s, want active", status)
	}

	var freezeAudits int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.freeze.resolved' AND object_id IN ($1,$2,$3)`,
		f.gapFreezeUsageID, f.gapFreezeCreditID, f.deadFreezeID).Scan(&freezeAudits); err != nil || freezeAudits != 3 {
		t.Fatalf("freeze resolution audit rows=%d err=%v", freezeAudits, err)
	}
	var requeueAudits int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='source_ingest_event.repair_requeued'`).Scan(&requeueAudits); err != nil || requeueAudits != 3 {
		t.Fatalf("requeue audit rows=%d err=%v", requeueAudits, err)
	}
	var summaryAudits int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.policy_anchor.pre_anchor_usage_repaired' AND object_id=$1`,
		f.accountA).Scan(&summaryAudits); err != nil || summaryAudits != 1 {
		t.Fatalf("per-account summary audit rows=%d err=%v", summaryAudits, err)
	}

	// A second apply run must find nothing left to do -- idempotent.
	second, err := f.store.RepairPreAnchorUsageEligibility(f.ctx, fixedRepairEvidence(),
		AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000099", Reason: "test repair rerun"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Accounts) != 0 || second.TotalSourceGapFreezesResolved != 0 || second.TotalEventDeadFreezesResolved != 0 || second.TotalEventsRequeued != 0 {
		t.Fatalf("second apply run found leftover work=%+v", second)
	}
}

// TestRepairPreAnchorUsageEligibilityApplyRequiresOperatorAndEvidence checks
// the guard against an incomplete apply invocation (no operator id, or no
// encrypted evidence/note) -- the CLI must supply both.
func TestRepairPreAnchorUsageEligibilityApplyRequiresOperatorAndEvidence(t *testing.T) {
	f := newRepairFixture(t)
	if _, err := f.store.RepairPreAnchorUsageEligibility(f.ctx,
		PreAnchorUsageRepairInput{Apply: true}, AuditActor{Type: "admin", ID: "test"}); err == nil {
		t.Fatal("apply without operator id or evidence was accepted")
	}
	if status := f.freezeStatus(t, f.gapFreezeUsageID); status != "open" {
		t.Fatalf("rejected apply still mutated a freeze: status=%s", status)
	}
}
