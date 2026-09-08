package postgresstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"invoice-system/backend/internal/domain"
)

// containDeadEventBehindFreeze makes the named freeze the only thing
// containing a dead source event, the way production does: one dead
// source_ingest_events row whose payload_hash is the freeze's
// source_revision_hash. Fixtures that leave source_revision_hash NULL get one
// assigned first -- freezeEligibilityTx always writes it, and a NULL is
// exactly what makes the guard unreachable, so a test built on a NULL freeze
// would be asserting nothing.
//
// Returns the event id so the caller can turn the key (write the event off)
// and prove the refusal has an exit.
func containDeadEventBehindFreeze(t *testing.T, store *Store, ctx context.Context,
	freezeID, streamID, entityType string) (eventID string) {
	t.Helper()
	var sourceID, payloadHash string
	if err := store.pool.QueryRow(ctx, `
		SELECT eas.source_instance_id::text,COALESCE(ef.source_revision_hash,'')
		FROM eligibility_freezes ef
		JOIN source_account_eligibility_state eas ON eas.external_account_id=ef.external_account_id
		WHERE ef.id=$1`, freezeID).Scan(&sourceID, &payloadHash); err != nil {
		t.Fatal(err)
	}
	if payloadHash == "" {
		payloadHash = testHash("contained-dead-" + freezeID)
		if _, err := store.pool.Exec(ctx, `UPDATE eligibility_freezes SET source_revision_hash=$2
			WHERE id=$1`, freezeID, payloadHash); err != nil {
			t.Fatal(err)
		}
	}
	// source_ingest_batches keys into source_ingest_state, and the repair
	// fixtures differ over which streams they provision.
	if err := store.ProvisionSourceStream(ctx, sourceID, streamID,
		AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	batchID, eventID := randomUUID(), randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		SELECT $1,$2,$3,COALESCE(max(b.sequence),0)+1,repeat('a',64),'guard-key',0,
			(SELECT runtime_version FROM source_instances WHERE id=$1),'guard-agent',now(),'healthy'
		FROM source_ingest_batches b WHERE b.source_instance_id=$1 AND b.stream_id=$2`,
		sourceID, streamID, batchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at,processing_status,attempt_count,processing_error,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'upsert',$6,decode(repeat('11',16),'hex'),now(),'dead',8,'PROJECTION_FAILED',now(),now())`,
		sourceID, streamID, eventID, batchID, entityType, payloadHash); err != nil {
		t.Fatal(err)
	}
	return eventID
}

// writeOffDeadEvent turns the key: acknowledging an event as unreplayable and
// replaying it successfully both land in processing_status, so both end the
// refusal. This is the half of every test below that proves the guard is a
// door and not a wall -- a fail-closed latch with no recovery path is exactly
// what this repository forbids.
func writeOffDeadEvent(t *testing.T, store *Store, ctx context.Context, eventID string) {
	t.Helper()
	command, err := store.pool.Exec(ctx, `UPDATE source_ingest_events
		SET processing_status='processed',processing_error='UNREPLAYABLE_BINDING',processed_at=now(),updated_at=now()
		WHERE event_id=$1`, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 1 {
		t.Fatalf("write-off touched %d rows, want 1", command.RowsAffected())
	}
}

// TestEveryRepairToolRefusesToUncontainADeadEvent is the behavioural half of
// TestEveryFreezeResolutionPassesTheDeadEventGuard: the discovery guard proves
// every door calls the check, these prove the check actually refuses through
// each door and that each refusal has an exit.
//
// The first cut of XM-INV-DEAD-CONTAINMENT guarded only the admin resolution
// endpoint and documented one exemption. There were four other doors. Three of
// them resolved freezes without looking at source_ingest_events at all, so a
// routine repair run silently un-contained a dead event -- taking the source
// instance away from every account again, and, for a balance_checkpoint, also
// releasing the carry-forward proof wait so the projection worker could write
// an immutable "balance unchanged" proof over a still-replayable fact.
//
// Every subtest is shaped refuse-then-repair-then-succeed on one fixture, so
// the success arm is the control for the refusal arm: without it, "the tool
// returned an error" could be any of the several other errors these tools
// raise, and a fixture that could never be repaired at all would prove
// nothing.
func TestEveryRepairToolRefusesToUncontainADeadEvent(t *testing.T) {
	t.Run("balance anchor repair", func(t *testing.T) {
		f := newBalanceAnchorRepairFixture(t)
		eventID := containDeadEventBehindFreeze(t, f.store, f.ctx, f.ckptA1.freezeID, "balances", "balance_checkpoint")

		_, err := f.store.RepairBalanceAnchorEligibility(f.ctx, fixedBalanceAnchorRepairEvidence(),
			AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000098", Reason: "test repair"})
		if !errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) {
			t.Fatalf("err=%v, want ErrEligibilityDeadEventUnrepaired -- the repair resolved the "+
				"freeze that was the only thing containing a dead balance checkpoint", err)
		}
		// The whole run is one transaction, so nothing at all may have been
		// written: not the offending freeze, not the other accounts' freezes.
		for _, id := range []string{f.ckptA1.freezeID, f.ckptA2.freezeID, f.proofA.freezeID, f.ckptB1.freezeID} {
			if status := f.freezeStatus(t, id); status != "open" {
				t.Fatalf("freeze %s status=%s after a refused run, want open", id, status)
			}
		}
		if status := f.accountEligibilityStatus(t, f.acctA); status != "frozen" {
			t.Fatalf("account A status=%s after a refused run, want frozen", status)
		}

		writeOffDeadEvent(t, f.store, f.ctx, eventID)
		if _, err = f.store.RepairBalanceAnchorEligibility(f.ctx, fixedBalanceAnchorRepairEvidence(),
			AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000098", Reason: "test repair"}); err != nil {
			t.Fatalf("the refusal has no exit: %v", err)
		}
		if status := f.freezeStatus(t, f.ckptA1.freezeID); status != "resolved" {
			t.Fatalf("freeze status=%s after the event was written off, want resolved", status)
		}
	})

	t.Run("balance blip repair", func(t *testing.T) {
		store, ctx := integrationStore(t)
		accountID, poisoned := newBlipRepairLegacyAccount(t, store, ctx, 970, "3667080", 1)
		var freezeID string
		if err := store.pool.QueryRow(ctx, `SELECT id FROM eligibility_freezes
			WHERE external_account_id=$1 AND status='open' AND freeze_reason='UNKNOWN_NEGATIVE_BALANCE'
				AND trigger_object_type='balance_checkpoint' AND trigger_object_id=$2`,
			accountID, poisoned[0]).Scan(&freezeID); err != nil {
			t.Fatal(err)
		}
		eventID := containDeadEventBehindFreeze(t, store, ctx, freezeID, "balances", "balance_checkpoint")

		in := blipRepairFixedResolution(t)
		in.Apply = true
		_, err := store.RepairBalanceBlipEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
		if !errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) {
			t.Fatalf("err=%v, want ErrEligibilityDeadEventUnrepaired", err)
		}
		if count := openFreezeCount(t, store, ctx, accountID); count != 1 {
			t.Fatalf("open freezes after a refused run=%d, want 1", count)
		}
		if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
			t.Fatalf("the refused run still removed the blip credit: remaining=%d", count)
		}

		writeOffDeadEvent(t, store, ctx, eventID)
		if _, err = store.RepairBalanceBlipEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID}); err != nil {
			t.Fatalf("the refusal has no exit: %v", err)
		}
		if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
			t.Fatalf("open freezes after the event was written off=%d, want 0", count)
		}
	})

	t.Run("queue narrow repair", func(t *testing.T) {
		store, ctx := integrationStore(t)
		accountID, _, _, _ := newQueueNarrowAccount(t, store, ctx, 970)
		negID := insertQueueNarrowFreeze(t, store, ctx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE", "balance_checkpoint", "qn-guard-neg")
		eventID := containDeadEventBehindFreeze(t, store, ctx, negID, "balances", "balance_checkpoint")

		in := queueNarrowFixedResolution(t)
		in.Apply = true
		// This tool's contract is per-account: one account's refusal must not
		// stop the rest of the run, so it surfaces as an error row rather than
		// a returned error.
		result, err := store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) != 1 || result.Errors[0].ExternalAccountID != accountID {
			t.Fatalf("errors=%+v, want exactly one for the account whose freeze contains a dead event", result.Errors)
		}
		if !strings.Contains(result.Errors[0].Message, "a dead source event still correlates to this freeze") {
			t.Fatalf("error message=%q does not name the reason an operator has to act on",
				result.Errors[0].Message)
		}
		if status, version := freezeStatus(t, store, ctx, negID); status != "open" || version != 1 {
			t.Fatalf("freeze status=%s version=%d after a refused run, want open/1", status, version)
		}
		if status := accountEligibilityStatus(t, store, ctx, accountID); status != "frozen" {
			t.Fatalf("account status=%s after a refused run, want frozen", status)
		}

		writeOffDeadEvent(t, store, ctx, eventID)
		result, err = store.RepairQueueNarrowEligibility(ctx, in, AuditActor{Type: "admin", ID: in.OperatorID})
		if err != nil || len(result.Errors) != 0 {
			t.Fatalf("the refusal has no exit: err=%v errors=%+v", err, result.Errors)
		}
		if status, _ := freezeStatus(t, store, ctx, negID); status != "resolved" {
			t.Fatalf("freeze status=%s after the event was written off, want resolved", status)
		}
	})

	// The pre-anchor repair is the door that always did establish the right
	// ordering by itself, by requeuing the correlated events in the same
	// transaction -- which is why the first cut exempted it. It still has to
	// ask, and this subtest is why: the tool only requeues dead/failed
	// usage_event/credit_event rows, so a correlated dead event of any other
	// entity type is one it cannot repair and must not resolve around.
	t.Run("pre-anchor usage repair", func(t *testing.T) {
		f := newRepairFixture(t)
		eventID := containDeadEventBehindFreeze(t, f.store, f.ctx, f.gapFreezeUsageID, "balances", "balance_checkpoint")

		_, err := f.store.RepairPreAnchorUsageEligibility(f.ctx, fixedRepairEvidence(),
			AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000099", Reason: "test repair"})
		if !errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) {
			t.Fatalf("err=%v, want ErrEligibilityDeadEventUnrepaired", err)
		}
		for _, id := range []string{f.gapFreezeUsageID, f.gapFreezeCreditID, f.deadFreezeID} {
			if status := f.freezeStatus(t, id); status != "open" {
				t.Fatalf("freeze %s status=%s after a refused run, want open", id, status)
			}
		}
		// The requeue now runs before the resolutions, so this also pins that
		// the refusal rolls the requeue back with it rather than leaving the
		// events half-repaired.
		status, attempts, _ := f.ingestStatus(t, "usage", f.usageEventID)
		if status != "dead" || attempts != 8 {
			t.Fatalf("usage event status=%s attempts=%d after a refused run, want dead/8", status, attempts)
		}

		writeOffDeadEvent(t, f.store, f.ctx, eventID)
		if _, err = f.store.RepairPreAnchorUsageEligibility(f.ctx, fixedRepairEvidence(),
			AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000099", Reason: "test repair"}); err != nil {
			t.Fatalf("the refusal has no exit: %v", err)
		}
		if status := f.freezeStatus(t, f.gapFreezeUsageID); status != "resolved" {
			t.Fatalf("freeze status=%s after the event was written off, want resolved", status)
		}
	})
}
