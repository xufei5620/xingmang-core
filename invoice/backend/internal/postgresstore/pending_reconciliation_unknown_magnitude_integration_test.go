package postgresstore

import (
	"testing"
	"time"
)

// XM-INV-PENDING-RECON C4 (design section 3, acceptance ruling 2026-09-09 on
// section 7 D3(a)): balance evidence whose magnitude the source never
// reported still parks the account, but it no longer resets a real
// consecutive-match streak and no longer logs a second `entered` audit row
// for an account already in the state.
//
// The reset's own stated reason is that "a fresh negative difference means
// the previous streak of matches did not actually resolve the gap". Evidence
// without a magnitude produces no difference, so that reason does not apply
// to it. And because every carry-forward proof restates its prior verbatim,
// one such item in a replay becomes one per published cycle: a production
// account collected 419 `entered` rows this way.

// TestUnknownMagnitudeEvidenceKeepsTheStreakAndLogsNoSecondEntry is design
// section 5.1 A11 and matrix row (j), the re-entry leg.
func TestUnknownMagnitudeEvidenceKeepsTheStreakAndLogsNoSecondEntry(t *testing.T) {
	f := newIdlePendingFixture(t)
	before := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if before.consecutiveMatches != pendingReconciliationExitMatches-1 {
		t.Fatalf("fixture: want a streak of %d, got %+v", pendingReconciliationExitMatches-1, before)
	}
	enteredBefore := f.pendingEnteredCount(t)
	if enteredBefore == 0 {
		t.Fatal("fixture: the account must already have an entry audit to compare against")
	}

	// A checkpoint reporting a negative balance with no magnitude -- the
	// shape every balance fact had before the bridge started reporting
	// deficits, and the shape every carry-forward proof restating one still
	// has.
	unknownAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "unknown-magnitude", unknownAt, nil, 6)
	f.project(t, unknownAt.Add(time.Minute))

	status, _, stored := f.checkpointEvaluation(t, "unknown-magnitude")
	if status != "negative_frozen" || stored != "<null>" {
		t.Fatalf("unknown magnitude: status=%q deficit=%s, want negative_frozen/<null> (the classification is unchanged)",
			status, stored)
	}
	after := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if after.status != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("the account must stay parked: %+v", after)
	}
	if after.consecutiveMatches != pendingReconciliationExitMatches-1 {
		t.Fatalf("consecutive_matches=%d, want %d: evidence with no magnitude computed no difference, so it is not "+
			"proof the streak failed", after.consecutiveMatches, pendingReconciliationExitMatches-1)
	}
	// The trigger detail is still refreshed to the newest evidence, so an
	// operator is not looking at a stale reason.
	if after.triggerID != "unknown-magnitude" || after.detail == "" {
		t.Fatalf("the trigger must still be refreshed to the latest item: %+v", after)
	}
	// pending_reconciliation_since is preserved across a re-entry, so the
	// operator still sees how long the account has been unreconciled.
	if !after.since.Equal(before.since) {
		t.Fatalf("pending_reconciliation_since moved from %s to %s on a re-entry", before.since, after.since)
	}
	if got := f.pendingEnteredCount(t); got != enteredBefore {
		t.Fatalf("entered audits=%d, want %d: an already-pending account did not enter anything", got, enteredBefore)
	}
}

// TestUnknownMagnitudeEvidenceStillParksAnActiveAccount is design section 5.1
// A11's control leg and the reason C4 is a narrowing, not a removal: for an
// account that is not already pending, evidence with no magnitude parks it
// exactly as before, with exactly one entry audit and a zero streak.
func TestUnknownMagnitudeEvidenceStillParksAnActiveAccount(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "active" {
		t.Fatalf("fixture: want an active account, got %+v", row)
	}
	if got := f.pendingEnteredCount(t); got != 0 {
		t.Fatalf("fixture: entered audits=%d, want 0", got)
	}

	unknownAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "unknown-first-entry", unknownAt, nil, 4)
	f.project(t, unknownAt.Add(time.Minute))

	status, _, stored := f.checkpointEvaluation(t, "unknown-first-entry")
	if status != "negative_frozen" || stored != "<null>" {
		t.Fatalf("unknown magnitude: status=%q deficit=%s, want negative_frozen/<null>", status, stored)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.reason != "UNKNOWN_NEGATIVE_BALANCE" ||
		row.triggerType != "balance_checkpoint" || row.triggerID != "unknown-first-entry" ||
		row.detail == "" || !row.sinceSet || row.consecutiveMatches != 0 {
		t.Fatalf("an active account must still be parked with every column set: %+v", row)
	}
	if got := f.pendingEnteredCount(t); got != 1 {
		t.Fatalf("entered audits=%d, want exactly 1", got)
	}
	if count := openFreezeCount(t, f.store, f.ctx, f.accountID); count != 0 {
		t.Fatalf("open freezes=%d, want 0 (never a manual freeze)", count)
	}
}

// TestStatedMagnitudeThatDisagreesStillResetsTheStreak is the other control:
// C4 must not weaken the case it was carved out of. Evidence that does state
// a magnitude and disagrees with the ledger is a real fresh negative
// difference, so it resets the streak and logs its own entry, exactly as
// before.
func TestStatedMagnitudeThatDisagreesStillResetsTheStreak(t *testing.T) {
	f := newIdlePendingFixture(t)
	enteredBefore := f.pendingEnteredCount(t)

	disagreeAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "stated-disagreement", disagreeAt, stringPtr("70"), 6)
	f.project(t, disagreeAt.Add(time.Minute))

	status, difference, stored := f.checkpointEvaluation(t, "stated-disagreement")
	if status != "negative_frozen" || difference != "-20" || stored != "70" {
		t.Fatalf("stated magnitude: status=%q difference=%s deficit=%s, want negative_frozen/-20/70",
			status, difference, stored)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.consecutiveMatches != 0 {
		t.Fatalf("consecutive_matches=%d, want 0: a stated magnitude that disagrees does prove the streak failed", row.consecutiveMatches)
	}
	if got := f.pendingEnteredCount(t); got != enteredBefore+1 {
		t.Fatalf("entered audits=%d, want %d (a real re-entry is still logged)", got, enteredBefore+1)
	}
}
