-- XM-INV-CLAIM-BINDING: ClaimUnprocessedSourceEvents now resolves an event's
-- *currently valid* economic binding rather than always using the batch it
-- first arrived in, so it looks source_economic_scan_cycle_events up by event
-- id on the claim hot path.
--
-- Neither the primary key (source_instance_id, stream_id, scan_cycle_id,
-- event_id) nor source_economic_scan_cycle_events_status_idx
-- (source_instance_id, stream_id, scan_cycle_id, event_id) leads with
-- event_id, so that lookup has no usable index today and would degrade the
-- claim query to a sequential scan of a table that grows one row per event
-- per scan cycle. This index is what keeps the new lateral index-bound.
--
-- Read-only addition: no column, constraint or data changes, so it is safe to
-- apply ahead of the code that uses it and safe to leave in place if that
-- code is rolled back.
CREATE INDEX IF NOT EXISTS source_economic_scan_cycle_events_event_idx
    ON source_economic_scan_cycle_events(source_instance_id, stream_id, event_id);
