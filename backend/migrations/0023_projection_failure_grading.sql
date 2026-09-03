-- XM-INV-PROJECTION-FAILURE-GRADING: production incident mechanism identified
-- 2026-09-03 during a read-only audit of RC79 -- ProcessEligibilityProjectionJobs
-- (backend/internal/postgresstore/consumption.go) marked ANY per-account error
-- from processEligibilityProjectionJob other than the balance-proof-pending
-- case status='failed' immediately, with no attempt counter, no backoff and
-- no terminal grade, and EligibilityProjectionHealth/eligibilityProjectionReady
-- (backend/cmd/api/runtime.go) turned /readyz unconditionally 503 the instant
-- any row carried status='failed' -- so one account hitting a transient error
-- (a serialization failure, a momentary database error, a one-off evaluator
-- bug) took the whole API not-ready until a human intervened. This is the
-- exact RC75 incident mechanism (see docs/handoffs/XM-INV-BLIP-SOFTFAIL.md),
-- now fixed at its structural root rather than only for that one bug class.
--
-- This migration gives eligibility_projection_jobs the same retry/dead-letter
-- shape source_ingest_events (migration 0009) already has via
-- MarkSourceEventFailed: attempts (a new, dedicated consecutive-failure
-- counter, distinct from the pre-existing attempt_count which increments on
-- every claim regardless of outcome -- see ProcessEligibilityProjectionJobs'
-- claim UPDATE) and a new terminal status, 'dead', reached only after
-- projectionFailureDeadThreshold (8, mirroring source_ingest_events' own
-- attempt_count>=8 threshold) consecutive non-proof-pending failures.
-- 'failed' stays in the vocabulary for backward compatibility with any row
-- already in that status at deploy time (the worker's claim query still
-- picks those up and grades them going forward), but the code no longer
-- produces it.
--
-- last_error carries the actual Go error text (bounded, distinct from the
-- existing last_error_code's short discriminator values) so an operator
-- inspecting a dead job -- or invoice-eligibility-repair
-- --kind=projection-requeue-dead's listing -- sees what actually happened,
-- not just a fixed code.
ALTER TABLE eligibility_projection_jobs
    DROP CONSTRAINT IF EXISTS eligibility_projection_jobs_status_check,
    ADD CONSTRAINT eligibility_projection_jobs_status_check CHECK (
        status IN ('queued','processing','failed','dead')
    ),
    ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    ADD COLUMN last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) <= 2000);
