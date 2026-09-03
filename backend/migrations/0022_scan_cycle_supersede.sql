-- XM-INV-SCAN-CYCLE-SUPERSEDE: production incident 2026-09-03 -- an
-- 0.3.1 source-agent restart abandons a legacy in-flight scan cycle
-- client-side (XM-INV-AGENT-RESTART-GRACE part B,
-- shouldAbandonLegacyReconcileCycle in agents/sourceagent/economics_db.go)
-- and starts a brand-new scan_cycle_id from its rolling-window baseline, but
-- nothing server-side ever closed the orphaned row that new cycle replaces:
-- CommitSourceBatch's one-active-cycle partial unique index
-- (source_economic_one_active_scan_cycle, migration 0009) rejected every
-- batch of the new cycle as domain.ErrScanCycleBusy forever, and the agent
-- treats that 503 as transient and retries without end -- the stream stays
-- wedged until an operator marks the old row 'blocked' by hand.
--
-- superseded_by_scan_cycle_id/supersede_reason record, on the abandoned row
-- itself, that CommitSourceBatch (not an operator) closed it out and why --
-- see backend/internal/postgresstore/source_sync.go's
-- supersedeStaleActiveScanCycleTx, which sets both together with
-- cycle_status='blocked' in the same transaction that then inserts the new
-- cycle's row. The FK is DEFERRABLE INITIALLY DEFERRED: within that
-- transaction the old row is UPDATEd (pointing at the new cycle id) before
-- the new row is INSERTed, so the reference would not resolve yet if checked
-- immediately; deferring to COMMIT lets both statements land in their
-- natural order. Named this migration 0022 rather than the next free slot
-- 0021 because another slice may independently claim 0021 -- higher numbers
-- only order migrations relative to each other (backend/internal/migrate
-- applies *.sql in lexical order, tracked individually by filename+checksum
-- in schema_migrations), so this file does not depend on whatever 0021 turns
-- out to contain.
ALTER TABLE source_economic_scan_cycles
    ADD COLUMN superseded_by_scan_cycle_id UUID,
    ADD COLUMN supersede_reason TEXT
        CHECK (supersede_reason IS NULL OR (btrim(supersede_reason) <> '' AND char_length(supersede_reason) <= 256)),
    ADD CONSTRAINT source_economic_scan_cycles_supersede_pair CHECK (
        (superseded_by_scan_cycle_id IS NULL) = (supersede_reason IS NULL)
    ),
    ADD CONSTRAINT source_economic_scan_cycles_supersede_requires_blocked CHECK (
        superseded_by_scan_cycle_id IS NULL OR cycle_status = 'blocked'
    ),
    ADD CONSTRAINT source_economic_scan_cycles_superseded_by_fk
        FOREIGN KEY (source_instance_id, stream_id, superseded_by_scan_cycle_id)
        REFERENCES source_economic_scan_cycles (source_instance_id, stream_id, scan_cycle_id)
        DEFERRABLE INITIALLY DEFERRED;
