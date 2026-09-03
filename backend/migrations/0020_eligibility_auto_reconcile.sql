-- XM-INV-ELIG-AUTO-RECONCILE (design XM-INV-ELIG-SIMPLIFY, section 3(A)/(B)):
-- a negative or otherwise unreconciled balance difference no longer opens a
-- manual eligibility_freezes row -- it downgrades the account to a new,
-- self-clearing eligibility_status, not_invoiceable_pending_reconciliation,
-- carried entirely on source_account_eligibility_state itself (no parallel
-- state table: eligibility_status is already the single column every
-- submit/issue gate keys off -- requests.go/store.go's
-- eligibility_status='active' checks are unchanged by this migration). The
-- account automatically returns to active once two consecutive real balance
-- evidence items evaluate matched (see evaluatePendingBalanceEvidenceTx's
-- writeEvaluation closure, postgresstore/consumption.go) -- no
-- eligibility_freezes row is ever opened for this reason, so
-- ResolveEligibilityFreeze/MFA is never involved. A genuinely open
-- eligibility_freezes row (a real, distinct freeze reason) always takes
-- priority: enterPendingReconciliationTx never downgrades an account that is
-- already 'frozen', and freezeEligibilityTx now clears these five columns
-- whenever it does freeze an account, so the two states can never overlap on
-- one row (enforced by the pairing CHECK below).
--
-- Separately (design section 3(B)): USAGE_EXCEEDS_LEDGER no longer freezes
-- the account either -- the unallocated overage (usage with no non-cash or
-- cash pool left to draw from) is recorded on the account row instead.
-- Invoiceable amounts were always capped at what actually got allocated into
-- a cash pool (buildEligibilityProjectionTx never lets a lot's own
-- consumed_cash_minor exceed its verified_cash_minor), so recording the
-- overage without freezing removes a defensive-but-unnecessary account-wide
-- block with no change to what can ever be invoiced. freeze_reason itself
-- keeps 'USAGE_EXCEEDS_LEDGER' (historical rows, and the queue-narrowing
-- repair tool planned for the next slice still need to reference it) -- only
-- the code path that used to open a *new* freeze for it is removed.
ALTER TABLE source_account_eligibility_state
    DROP CONSTRAINT IF EXISTS source_account_eligibility_state_eligibility_status_check,
    ADD CONSTRAINT source_account_eligibility_state_eligibility_status_check CHECK (
        eligibility_status IN ('active','syncing','frozen','not_invoiceable_pending_reconciliation')
    ),
    ADD COLUMN pending_reconciliation_reason TEXT CHECK (
        pending_reconciliation_reason IS NULL OR pending_reconciliation_reason IN (
            'UNKNOWN_NEGATIVE_BALANCE','LATE_FINALIZED_EVENT','AMBIGUOUS_EVENT_ORDER',
            'EVENT_PAYLOAD_DRIFT','UNIT_MISMATCH','USAGE_EXCEEDS_LEDGER',
            'STREAM_WATERMARK_REGRESSION','SOURCE_GAP','SOURCE_REFUND','EVENT_DEAD',
            'POLICY_ANCHOR_BLOCKED'
        )
    ),
    ADD COLUMN pending_reconciliation_trigger_type TEXT CHECK (
        pending_reconciliation_trigger_type IS NULL
        OR (btrim(pending_reconciliation_trigger_type) <> '' AND char_length(pending_reconciliation_trigger_type) <= 64)
    ),
    ADD COLUMN pending_reconciliation_trigger_id TEXT CHECK (
        pending_reconciliation_trigger_id IS NULL
        OR (btrim(pending_reconciliation_trigger_id) <> '' AND char_length(pending_reconciliation_trigger_id) <= 256)
    ),
    ADD COLUMN pending_reconciliation_detail TEXT CHECK (
        pending_reconciliation_detail IS NULL
        OR (btrim(pending_reconciliation_detail) <> '' AND char_length(pending_reconciliation_detail) <= 2000)
    ),
    ADD COLUMN pending_reconciliation_since TIMESTAMPTZ,
    ADD COLUMN pending_reconciliation_consecutive_matches INTEGER NOT NULL DEFAULT 0
        CHECK (pending_reconciliation_consecutive_matches >= 0),
    ADD COLUMN non_invoiceable_overage_units NUMERIC(78,0)
        CHECK (non_invoiceable_overage_units IS NULL OR non_invoiceable_overage_units > 0),
    ADD COLUMN non_invoiceable_overage_usage_event_id UUID
        REFERENCES source_usage_events(id) ON DELETE RESTRICT,
    ADD CONSTRAINT source_account_eligibility_state_pending_reconciliation_pair CHECK (
        (eligibility_status = 'not_invoiceable_pending_reconciliation'
         AND pending_reconciliation_reason IS NOT NULL
         AND pending_reconciliation_trigger_type IS NOT NULL
         AND pending_reconciliation_trigger_id IS NOT NULL
         AND pending_reconciliation_detail IS NOT NULL
         AND pending_reconciliation_since IS NOT NULL)
        OR
        (eligibility_status <> 'not_invoiceable_pending_reconciliation'
         AND pending_reconciliation_reason IS NULL
         AND pending_reconciliation_trigger_type IS NULL
         AND pending_reconciliation_trigger_id IS NULL
         AND pending_reconciliation_detail IS NULL
         AND pending_reconciliation_since IS NULL
         AND pending_reconciliation_consecutive_matches = 0)
    ),
    ADD CONSTRAINT source_account_eligibility_state_overage_pair CHECK (
        (non_invoiceable_overage_units IS NULL) = (non_invoiceable_overage_usage_event_id IS NULL)
    );
