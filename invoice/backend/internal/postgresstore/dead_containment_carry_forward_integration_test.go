package postgresstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// strandedEventID is the balance_checkpoint event seedCarryForwardFixture puts
// into the published proof cycle. The fixture parks it; these tests kill it.
const carryForwardStrandedEventID = "75000000-0000-4000-8000-000000000001"

func runCarryForwardProof(t *testing.T, fixture carryForwardFixture) error {
	t.Helper()
	tx, err := fixture.store.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(fixture.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	account, err := getEligibilityAccountTx(fixture.ctx, tx, fixture.accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ensureBalanceCarryForwardProofTx(fixture.ctx, tx, account, fixture.requested,
		AuditActor{Type: "system", ID: "carry-forward-worker"}); err != nil {
		return err
	}
	if err = tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	return nil
}

func carryForwardProofCount(t *testing.T, fixture carryForwardFixture) int64 {
	t.Helper()
	var proofs int64
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM balance_carry_forward_proofs
		WHERE external_account_id=$1`, fixture.accountID).Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	return proofs
}

// strandCarryForwardCheckpoint kills the published cycle's balance_checkpoint
// event and opens a containing freeze for accountID, returning the payload
// hash the two are correlated by.
func strandCarryForwardCheckpoint(t *testing.T, fixture carryForwardFixture, accountID string) string {
	t.Helper()
	return strandCarryForwardCheckpointAs(t, fixture, accountID, "EVENT_DEAD")
}

// strandCarryForwardCheckpointAs is the same thing with the freeze reason
// chosen by the caller. Containment is deliberately reason-blind, so the
// reason changes nothing about the wait itself -- but it decides which repair
// tool selects the freeze, which is how the repair-tool arm below reproduces
// the real route to the data loss rather than a hand-made one.
func strandCarryForwardCheckpointAs(t *testing.T, fixture carryForwardFixture, accountID, reason string) string {
	t.Helper()
	var payloadHash string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
		UPDATE source_ingest_events SET processing_status='dead',processing_error='PROJECTION_FAILED',
			attempt_count=8,dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2::uuid
		RETURNING payload_hash`, fixture.sourceID, carryForwardStrandedEventID).Scan(&payloadHash); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.pool.Exec(fixture.ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,
			trigger_object_id,source_revision_hash,status)
		VALUES($1,$2,$4,'balance_checkpoint','stranded-checkpoint',$3,'open')`,
		randomUUID(), accountID, payloadHash, reason); err != nil {
		t.Fatal(err)
	}
	// The wait must be explained by the stranded checkpoint, not by an
	// unpublished cycle.
	var cycleStatus string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`,
		fixture.sourceID, fixture.proofCycle.cycleID).Scan(&cycleStatus); err != nil {
		t.Fatal(err)
	}
	if cycleStatus != "published" {
		t.Fatalf("fixture cycle status=%s want published", cycleStatus)
	}
	return payloadHash
}

// TestStrandedCheckpointHoldsTheCarryForwardProofInsteadOfFakingIt covers
// XM-INV-DEAD-CONTAINMENT decision A2, which this slice took rather than
// deferred (see docs/handoffs/XM-INV-DEAD-CONTAINMENT.md).
//
// Containment lets a scan cycle publish while one of its balance_checkpoint
// events is dead. The eligibility projection worker runs every two seconds, so
// it reaches that published cycle long before an operator could requeue the
// event -- and, finding no real checkpoint for the account, writes a
// carry-forward proof asserting "the balance did not change in this cycle".
// That proof is immutable, and migration 0014's
// reject_real_checkpoint_after_carry_forward trigger then refuses the real
// checkpoint forever: a replayable balance fact lost permanently as a side
// effect of a containment decision taken about something else.
//
// So the proof waits. The wait is not a new fail-closed latch without a key --
// both repairs end it, and the second subtest is the key being turned.
//
// Each subtest gets its own fixture because balance_carry_forward_proofs is
// immutable by trigger: a proof written in one phase cannot be deleted to set
// up the next.
func TestStrandedCheckpointHoldsTheCarryForwardProofInsteadOfFakingIt(t *testing.T) {
	// Without this control, "no proof was written" below would be satisfied
	// by a fixture that could never write one.
	t.Run("control: nothing stranded, the proof is written", func(t *testing.T) {
		fixture := seedCarryForwardFixture(t, "", "20")
		if err := runCarryForwardProof(t, fixture); err != nil {
			t.Fatalf("the fixture cannot write a proof at all, so the rest is vacuous: %v", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 1 {
			t.Fatalf("proofs=%d want 1", proofs)
		}
	})

	t.Run("a stranded checkpoint holds the proof until the event is written off", func(t *testing.T) {
		fixture := seedCarryForwardFixture(t, "", "20")
		strandCarryForwardCheckpoint(t, fixture, fixture.accountID)

		if err := runCarryForwardProof(t, fixture); !errors.Is(err, errBalanceCarryForwardProofPending) {
			t.Fatalf("err=%v want errBalanceCarryForwardProofPending -- an immutable "+
				"\"no checkpoint here\" proof was written over a replayable balance fact", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 0 {
			t.Fatalf("proofs=%d want 0 while a checkpoint for this cycle is stranded", proofs)
		}

		// The key. Acknowledging the event as unreplayable and replaying it
		// successfully both land in processing_status, so both end the wait.
		if _, err := fixture.store.pool.Exec(fixture.ctx, `
			UPDATE source_ingest_events SET processing_status='processed',processing_error='UNREPLAYABLE_BINDING',
				processed_at=now(),updated_at=now()
			WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2::uuid`,
			fixture.sourceID, carryForwardStrandedEventID); err != nil {
			t.Fatal(err)
		}
		if err := runCarryForwardProof(t, fixture); err != nil {
			t.Fatalf("the wait has no exit: %v", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 1 {
			t.Fatalf("proofs=%d want 1 once the stranded checkpoint was resolved", proofs)
		}
	})

	// The arm that was missing, and the one a code review used to walk the
	// whole data loss end to end.
	//
	// The wait's account correlation runs through the open freeze, because
	// source_ingest_events carries no account column. That made the freeze
	// load-bearing in a way nothing tested: resolve it while the event is
	// still dead and the wait evaporates, the projection worker writes the
	// immutable "balance unchanged" proof within two seconds, and migration
	// 0014's trigger refuses the real checkpoint forever.
	//
	// Two independent things now stop that, and this arm is the second one.
	// The first is that every path which resolves a freeze refuses while its
	// event is dead (see TestEveryRepairToolRefusesToUncontainADeadEvent).
	// The second is here: a stranded checkpoint that no open freeze answers
	// for is treated as possibly this account's, because nothing is left to
	// say whose it is, and guessing "not mine" is the guess that loses the
	// fact. The raw UPDATE below deliberately goes around every guarded door
	// to reach that state at all.
	t.Run("a freeze resolved out from under a still-dead checkpoint still holds the proof", func(t *testing.T) {
		fixture := seedCarryForwardFixture(t, "", "20")
		payloadHash := strandCarryForwardCheckpoint(t, fixture, fixture.accountID)
		if _, err := fixture.store.pool.Exec(fixture.ctx, `
			UPDATE eligibility_freezes SET status='resolved',resolved_at=now(),resolved_by=$2::uuid,
				resolution_evidence_hash=repeat('d',64),resolution_note_hash=repeat('e',64),
				resolution_evidence_ciphertext=decode(repeat('11',16),'hex'),
				resolution_note_ciphertext=decode(repeat('22',16),'hex'),
				resolution_version=resolution_version+1,updated_at=now()
			WHERE source_revision_hash=$1 AND status='open'`, payloadHash, randomUUID()); err != nil {
			t.Fatal(err)
		}
		if err := runCarryForwardProof(t, fixture); !errors.Is(err, errBalanceCarryForwardProofPending) {
			t.Fatalf("err=%v want errBalanceCarryForwardProofPending -- resolving the freeze "+
				"released the wait and an immutable \"no checkpoint here\" proof was written "+
				"over a still-dead, still-replayable balance fact", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 0 {
			t.Fatalf("proofs=%d want 0 while the checkpoint is still dead", proofs)
		}
		// Still not a latch without a key: writing the event off ends the
		// wait whether or not any freeze remains.
		if _, err := fixture.store.pool.Exec(fixture.ctx, `
			UPDATE source_ingest_events SET processing_status='processed',processing_error='UNREPLAYABLE_BINDING',
				processed_at=now(),updated_at=now()
			WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2::uuid`,
			fixture.sourceID, carryForwardStrandedEventID); err != nil {
			t.Fatal(err)
		}
		if err := runCarryForwardProof(t, fixture); err != nil {
			t.Fatalf("the wait has no exit once the freeze is gone: %v", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 1 {
			t.Fatalf("proofs=%d want 1", proofs)
		}
	})

	// The same walk again, through a door an operator actually uses. A
	// queue-narrow migration run resolves every open UNKNOWN_NEGATIVE_BALANCE
	// freeze on every candidate account in one pass, with no idea a dead event
	// might be hanging from one of them -- and, before the guard, no reason to
	// look.
	t.Run("a repair run refuses instead of releasing the wait", func(t *testing.T) {
		fixture := seedCarryForwardFixture(t, "", "20")
		strandCarryForwardCheckpointAs(t, fixture, fixture.accountID, "UNKNOWN_NEGATIVE_BALANCE")

		in := queueNarrowFixedResolution(t)
		in.Apply = true
		result, err := fixture.store.RepairQueueNarrowEligibility(fixture.ctx, in,
			AuditActor{Type: "admin", ID: in.OperatorID})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) != 1 ||
			!strings.Contains(result.Errors[0].Message, "a dead source event still correlates to this freeze") {
			t.Fatalf("errors=%+v, want one refusal naming the dead event", result.Errors)
		}
		var status string
		if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT status FROM eligibility_freezes
			WHERE external_account_id=$1`, fixture.accountID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "open" {
			t.Fatalf("freeze status=%s after a refused repair run, want open", status)
		}
		if err = runCarryForwardProof(t, fixture); !errors.Is(err, errBalanceCarryForwardProofPending) {
			t.Fatalf("err=%v want errBalanceCarryForwardProofPending", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 0 {
			t.Fatalf("proofs=%d want 0", proofs)
		}
	})

	// The carry-forward wait is per (account, cycle) -- unlike the
	// stream-health containment predicate, which is deliberately
	// account-blind. Both semantics are correct for their own question, and
	// the difference is easy to erase by accident, so it is asserted.
	t.Run("another account's freeze does not hold this account's proof", func(t *testing.T) {
		fixture := seedCarryForwardFixture(t, "", "20")
		otherUser, otherAccount := randomUUID(), randomUUID()
		if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
			VALUES($1,'test','carry-forward-other-user')`, otherUser); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
			VALUES($1,$2,$3,'carry-other','h1:'||repeat('5',64),'test','verified')`,
			otherAccount, otherUser, fixture.sourceID); err != nil {
			t.Fatal(err)
		}
		strandCarryForwardCheckpoint(t, fixture, otherAccount)
		if err := runCarryForwardProof(t, fixture); err != nil {
			t.Fatalf("another account's freeze held this account's carry-forward proof: %v", err)
		}
		if proofs := carryForwardProofCount(t, fixture); proofs != 1 {
			t.Fatalf("proofs=%d want 1", proofs)
		}
	})
}
