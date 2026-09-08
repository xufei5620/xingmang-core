package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"invoice-system/backend/internal/domain"
)

var (
	streamIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	hexHashPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	dependencyHMACPattern = regexp.MustCompile(`^h1:[0-9a-f]{64}$`)
)

type SourceBatchEvent struct {
	EventID           string
	EntityType        string
	Operation         string
	PayloadHash       string
	PayloadCiphertext []byte
	ObservedAt        time.Time
}

type SourceEventClaim struct {
	SourceInstanceID  string
	SourceType        domain.SourceType
	StreamID          string
	EventID           string
	EntityType        string
	Operation         string
	PayloadHash       string
	PayloadCiphertext []byte
	ObservedAt        time.Time
	BatchSequence     int64
	BatchID           string
	SchemaVersion     string
	SigningKeyID      string
	StreamWatermarkAt time.Time
	BatchSourceCursor string
	ScanCeilingAt     time.Time
	ScanCeilingCursor string
	ScanCycleID       string
	ScanComplete      bool
	CatchupKeyHMAC    string
	LeaseToken        string
	Attempt           int
}

// SourceEventFailed and SourceEventDead are the two grades
// MarkSourceEventFailed applies, and the two values it returns. They are
// spelled out as constants (XM-INV-READYZ-DETAIL) because the difference is
// no longer internal to that method: 'failed' is a retry that will come round
// again on its own, while 'dead' is terminal -- it takes SourceIngestHealth's
// Dead above one, which holds /readyz at 503 until an operator repairs it, so
// its caller reports it at a different log level.
const (
	SourceEventFailed = "failed"
	SourceEventDead   = "dead"
)

// sourceEventDeadThreshold is the consecutive-attempt count at which an event
// becomes terminally dead. It was the bare literal 8 in two separate queries:
// MarkSourceEventFailed's `attempt_count>=8` grade and
// ClaimUnprocessedSourceEvents' `attempt_count < 8` claim predicate. Those two
// have to agree exactly -- a claim predicate below the grade would stop
// re-claiming an event before it could ever be graded dead, and one above it
// would keep re-claiming rows already marked dead -- so they now read the same
// constant, bound as a query parameter (XM-INV-READYZ-DETAIL). It is also the
// `threshold` field of the source_ingest_event.dead audit event, mirroring
// projectionFailureDeadThreshold's role in eligibility.projection.dead, whose
// value this deliberately matches.
const sourceEventDeadThreshold = 8

type SourceIngestHealth struct {
	Pending       int64
	Dead          int64
	Waiting       int64
	OldestPending time.Time
}

// SourceFreshnessPolicy protects irreversible operations from using an old
// projection. Accepted-batch heartbeats and proven economic watermarks have
// independent budgets; identities carry the authority to use economic state.
type SourceFreshnessPolicy struct {
	EconomicHeartbeatMaxAge time.Duration
	EconomicWatermarkMaxAge time.Duration
	IdentitiesMaxAge        time.Duration
	// EconomicRescanActivityMaxAge (XM-INV-AGENT-RESTART-GRACE part A) bounds
	// the readiness-grace window for a stream whose economic watermark looks
	// stale only because a source_economic_scan_cycles row proves an active,
	// still-updating rescan (agents/sourceagent's ScanReconcile rolling
	// window, or a mandatory New API full scan) is in progress -- see
	// evaluateSourceStreamHealth. Zero or negative disables the grace
	// entirely: an ECONOMIC_WATERMARK_STALE finding is never downgraded, the
	// same as before this field existed.
	EconomicRescanActivityMaxAge time.Duration
	Now                          time.Time
}

func (p SourceFreshnessPolicy) enabled() bool {
	return p.EconomicHeartbeatMaxAge > 0 && p.EconomicWatermarkMaxAge > 0 && p.IdentitiesMaxAge > 0 && !p.Now.IsZero()
}

type SourceStreamHealth struct {
	SourceInstanceID       string            `json:"source_instance_id"`
	SourceType             domain.SourceType `json:"source_type"`
	SourceName             string            `json:"source_name"`
	SourceEnabled          bool              `json:"source_enabled"`
	StreamID               string            `json:"stream_id"`
	Sequence               int64             `json:"sequence"`
	ApprovedRuntimeVersion string            `json:"approved_runtime_version"`
	// CutoverRuntimeVersion is the source runtime the sealed cutover manifest was
	// captured under; immutable for the state generation, while
	// ApprovedRuntimeVersion is the pin batches must declare (XM-INV-SOURCE-RUNTIME-PIN).
	CutoverRuntimeVersion  string    `json:"cutover_runtime_version"`
	ObservedRuntimeVersion string    `json:"observed_runtime_version"`
	ObservedAgentVersion   string    `json:"observed_agent_version"`
	ProjectionStatus       string    `json:"projection_status"`
	LastAcceptedAt         time.Time `json:"last_accepted_at"`
	LastNonemptyBatchAt    time.Time `json:"last_nonempty_batch_at"`
	EconomicWatermarkAt    time.Time `json:"economic_watermark_at,omitempty"`
	// ActiveRescanUpdatedAt (XM-INV-AGENT-RESTART-GRACE part A) is the
	// updated_at of this (source, stream)'s source_economic_scan_cycles row
	// still in 'receiving' or 'processing' status, if any -- the partial
	// unique index source_economic_one_active_scan_cycle guarantees at most
	// one. Zero means no scan cycle is currently in progress.
	ActiveRescanUpdatedAt              time.Time `json:"active_rescan_updated_at,omitempty"`
	MaximumAgeSeconds                  int64     `json:"maximum_age_seconds"`
	EconomicWatermarkMaximumAgeSeconds int64     `json:"economic_watermark_maximum_age_seconds,omitempty"`
	PendingEvents                      int64     `json:"pending_events"`
	DeadEvents                         int64     `json:"dead_events"`
	WaitingDependencies                int64     `json:"waiting_dependencies"`
	Ready                              bool      `json:"ready"`
	Reasons                            []string  `json:"reasons"`
}

type SourceHealthReport struct {
	Ready bool                 `json:"ready"`
	Items []SourceStreamHealth `json:"items"`
}

// SourceReadinessHealth is the bounded subset of source health needed by the
// HTTP readiness probe. Waiting dependency counts are deliberately absent:
// management surfaces use SourceHealth when they need those exact counts.
type SourceReadinessHealth struct {
	Ingest SourceIngestHealth
	Report SourceHealthReport
}

type SourceBatchInput struct {
	SchemaVersion        string
	SourceInstanceID     string
	StreamID             string
	BatchID              string
	Sequence             int64
	BodyHash             string
	PreviousBatchHash    string
	SigningKeyID         string
	SourceRuntimeVersion string
	SourceAgentVersion   string
	SourceCapturedAt     time.Time
	ProjectionStatus     string
	StreamWatermarkAt    time.Time
	SourceCursor         string
	ScanCeilingAt        time.Time
	ScanCeilingCursor    string
	ScanCycleID          string
	ScanComplete         bool
	ScanSnapshotID       string
	ScanSnapshotRowCount int64
	Events               []SourceBatchEvent
	Actor                AuditActor
	// Now and StaleActiveScanCycleMaxAge (XM-INV-SCAN-CYCLE-SUPERSEDE) let
	// CommitSourceBatch decide whether an existing active scan cycle for this
	// (SourceInstanceID,StreamID) under a DIFFERENT ScanCycleID has gone stale
	// enough to supersede instead of rejecting this batch with
	// domain.ErrScanCycleBusy forever -- see supersedeStaleActiveScanCycleTx.
	// Both are zero-value safe: an unset Now or a non-positive
	// StaleActiveScanCycleMaxAge disables the supersede path entirely and
	// preserves the pre-existing behavior (a different active cycle id is
	// always busy), the same "zero disables" convention
	// SourceFreshnessPolicy.EconomicRescanActivityMaxAge already uses.
	// CommitVerifiedSourceBatch (application/service.go) wires
	// StaleActiveScanCycleMaxAge from that exact same field
	// (Service.sourceEconomicRescanActivityMaxAge) so both call sites agree on
	// what "still looks like a live rescan" means.
	Now                        time.Time
	StaleActiveScanCycleMaxAge time.Duration
}

type SourceBatchResult struct {
	Duplicate       bool
	AcceptedRecords int
	Sequence        int64
}

func validSourceStream(sourceInstanceID, streamID string) bool {
	return strings.TrimSpace(sourceInstanceID) != "" && streamIDPattern.MatchString(streamID)
}

// ProvisionSourceStream is an explicit receiver-side deployment/admin
// operation. Source agents never access this database.
func (s *Store) ProvisionSourceStream(ctx context.Context, sourceInstanceID, streamID string, actor AuditActor) error {
	if !validSourceStream(sourceInstanceID, streamID) {
		return errors.New("valid source instance and stream are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `
		INSERT INTO source_ingest_state(source_instance_id,stream_id)
		VALUES($1,$2) ON CONFLICT DO NOTHING`, sourceInstanceID, streamID); err != nil {
		return fmt.Errorf("provision source stream: %w", err)
	}
	if err = writeAudit(ctx, tx, actor, "source_stream.provisioned", "source_stream",
		sourceInstanceID+"/"+streamID, nil, map[string]any{"provisioned": true}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateSourceBatch(in SourceBatchInput) error {
	if in.SchemaVersion == "" {
		in.SchemaVersion = "2.0"
	}
	if !validSourceStream(in.SourceInstanceID, in.StreamID) || strings.TrimSpace(in.BatchID) == "" ||
		in.Sequence <= 0 || !hexHashPattern.MatchString(in.BodyHash) ||
		strings.TrimSpace(in.SigningKeyID) == "" || len(in.SigningKeyID) > 128 ||
		strings.TrimSpace(in.SourceRuntimeVersion) == "" || len(in.SourceRuntimeVersion) > 128 ||
		strings.TrimSpace(in.SourceAgentVersion) == "" || len(in.SourceAgentVersion) > 128 ||
		(in.ProjectionStatus != "healthy" && in.ProjectionStatus != "blocked" && in.ProjectionStatus != "unknown") ||
		in.SourceCapturedAt.IsZero() || len(in.Events) > 500 {
		return errors.New("invalid verified source batch")
	}
	if in.SchemaVersion == "3.0" {
		if !validEconomicStream(in.StreamID) || in.StreamWatermarkAt.IsZero() || in.ScanCeilingAt.IsZero() ||
			in.StreamWatermarkAt.After(in.ScanCeilingAt) || strings.TrimSpace(in.SourceCursor) == "" ||
			len(in.SourceCursor) > 256 || strings.TrimSpace(in.ScanCeilingCursor) == "" ||
			len(in.ScanCeilingCursor) > 256 || strings.TrimSpace(in.ScanCycleID) == "" ||
			(in.StreamID == "balances" && (!hexHashPattern.MatchString(in.ScanSnapshotID) || in.ScanSnapshotRowCount < 0 || in.ScanSnapshotRowCount > 2_000_000)) ||
			(in.StreamID != "balances" && (in.ScanSnapshotID != "" || in.ScanSnapshotRowCount != 0)) {
			return errors.New("invalid v3 economic scan metadata")
		}
	} else if in.SchemaVersion != "2.0" || !in.StreamWatermarkAt.IsZero() || !in.ScanCeilingAt.IsZero() ||
		in.SourceCursor != "" || in.ScanCeilingCursor != "" || in.ScanCycleID != "" || in.ScanComplete ||
		in.ScanSnapshotID != "" || in.ScanSnapshotRowCount != 0 {
		return errors.New("economic scan metadata is restricted to schema v3")
	}
	if (in.Sequence == 1 && in.PreviousBatchHash != "") ||
		(in.Sequence > 1 && !hexHashPattern.MatchString(in.PreviousBatchHash)) {
		return domain.ErrConflict
	}
	seen := make(map[string]string, len(in.Events))
	for i := range in.Events {
		event := &in.Events[i]
		if event.Operation == "" {
			event.Operation = "upsert"
		}
		if event.EventID == "" || strings.TrimSpace(event.EntityType) == "" || len(event.EntityType) > 128 ||
			(event.Operation != "upsert" && event.Operation != "tombstone") ||
			!hexHashPattern.MatchString(event.PayloadHash) || len(event.PayloadCiphertext) < 16 ||
			len(event.PayloadCiphertext) > 1<<20 || event.ObservedAt.IsZero() {
			return errors.New("invalid verified source event")
		}
		if prior, exists := seen[event.EventID]; exists {
			if prior != event.PayloadHash {
				return domain.ErrConflict
			}
			return errors.New("duplicate event in source batch")
		}
		seen[event.EventID] = event.PayloadHash
	}
	return nil
}

// CommitSourceBatch is called only after mTLS source identity, detached
// signature, body hash and record schemas have been verified.  It makes the
// receiver's sequence/hash chain and durable encrypted event inserts atomic.
func (s *Store) CommitSourceBatch(ctx context.Context, in SourceBatchInput) (SourceBatchResult, error) {
	if in.SchemaVersion == "" {
		in.SchemaVersion = "2.0"
	}
	if err := validateSourceBatch(in); err != nil {
		return SourceBatchResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SourceBatchResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,5))`, in.SourceInstanceID+"\n"+in.StreamID); err != nil {
		return SourceBatchResult{}, err
	}
	var currentSequence int64
	var currentHash, approvedRuntime string
	var sourceEnabled bool
	err = tx.QueryRow(ctx, `
		SELECT sis.sequence,COALESCE(sis.last_batch_hash,''),si.runtime_version,si.enabled
		FROM source_ingest_state sis
		JOIN source_instances si ON si.id=sis.source_instance_id
		WHERE sis.source_instance_id=$1 AND sis.stream_id=$2
		FOR UPDATE OF sis,si`, in.SourceInstanceID, in.StreamID).Scan(
		&currentSequence, &currentHash, &approvedRuntime, &sourceEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceBatchResult{}, domain.ErrNotFound
	}
	if err != nil {
		return SourceBatchResult{}, err
	}
	if !sourceEnabled || strings.TrimSpace(approvedRuntime) == "" || in.SourceRuntimeVersion != approvedRuntime {
		return SourceBatchResult{}, domain.ErrVersionConflict
	}
	var existingHash string
	var existingSequence int64
	err = tx.QueryRow(ctx, `
		SELECT body_hash,sequence FROM source_ingest_batches
		WHERE source_instance_id=$1 AND stream_id=$2 AND batch_id=$3`,
		in.SourceInstanceID, in.StreamID, in.BatchID).Scan(&existingHash, &existingSequence)
	if err == nil {
		if existingHash != in.BodyHash || existingSequence != in.Sequence {
			return SourceBatchResult{}, domain.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return SourceBatchResult{}, err
		}
		return SourceBatchResult{Duplicate: true, AcceptedRecords: len(in.Events), Sequence: in.Sequence}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SourceBatchResult{}, err
	}
	if in.Sequence != currentSequence+1 || in.PreviousBatchHash != currentHash {
		return SourceBatchResult{}, domain.ErrVersionConflict
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_ingest_batches(
			source_instance_id,stream_id,batch_id,sequence,body_hash,
			previous_batch_hash,signing_key_id,record_count,source_runtime_version,
			source_agent_version,source_captured_at,projection_status,schema_version,
			stream_watermark_at,source_cursor,scan_ceiling_at,scan_ceiling_cursor,
			scan_cycle_id,scan_complete,scan_snapshot_id,scan_snapshot_row_count)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::uuid,$19,$20,$21)`, in.SourceInstanceID, in.StreamID,
		in.BatchID, in.Sequence, in.BodyHash, optionalString(in.PreviousBatchHash),
		in.SigningKeyID, len(in.Events), in.SourceRuntimeVersion,
		in.SourceAgentVersion, in.SourceCapturedAt.UTC(), in.ProjectionStatus, in.SchemaVersion,
		optionalTime(in.StreamWatermarkAt), optionalString(in.SourceCursor), optionalTime(in.ScanCeilingAt),
		optionalString(in.ScanCeilingCursor), optionalString(in.ScanCycleID), in.ScanComplete,
		optionalString(in.ScanSnapshotID), nullableSnapshotRowCount(in.ScanSnapshotID, in.ScanSnapshotRowCount))
	if err != nil {
		return SourceBatchResult{}, fmt.Errorf("insert source batch: %w", err)
	}
	if in.SchemaVersion == "3.0" {
		if err = supersedeStaleActiveScanCycleTx(ctx, tx, in); err != nil {
			return SourceBatchResult{}, err
		}
		command, cycleErr := tx.Exec(ctx, `
			INSERT INTO source_economic_scan_cycles(
				source_instance_id,stream_id,scan_cycle_id,stream_watermark_at,source_cursor,
				scan_ceiling_at,scan_ceiling_cursor,scan_snapshot_id,scan_snapshot_row_count,
				first_sequence,last_sequence,final_sequence,cycle_status)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::bigint,$10::bigint,
				CASE WHEN $11 AND $12<>'blocked' THEN $10::bigint ELSE NULL::bigint END,
				CASE WHEN $12='blocked' THEN 'blocked' WHEN $11 THEN 'processing' ELSE 'receiving' END)
			ON CONFLICT(source_instance_id,stream_id,scan_cycle_id) DO UPDATE SET
				stream_watermark_at=EXCLUDED.stream_watermark_at,source_cursor=EXCLUDED.source_cursor,
				last_sequence=EXCLUDED.last_sequence,
				final_sequence=COALESCE(source_economic_scan_cycles.final_sequence,EXCLUDED.final_sequence),
				cycle_status=CASE WHEN $12='blocked' THEN 'blocked'
					WHEN EXCLUDED.final_sequence IS NOT NULL THEN 'processing'
					ELSE source_economic_scan_cycles.cycle_status END,updated_at=now()
			WHERE source_economic_scan_cycles.scan_ceiling_at=EXCLUDED.scan_ceiling_at
				AND source_economic_scan_cycles.scan_ceiling_cursor=EXCLUDED.scan_ceiling_cursor
				AND source_economic_scan_cycles.scan_snapshot_id IS NOT DISTINCT FROM EXCLUDED.scan_snapshot_id
				AND source_economic_scan_cycles.scan_snapshot_row_count IS NOT DISTINCT FROM EXCLUDED.scan_snapshot_row_count
				AND source_economic_scan_cycles.final_sequence IS NULL
				AND source_economic_scan_cycles.last_sequence+1=EXCLUDED.last_sequence`, in.SourceInstanceID,
			in.StreamID, in.ScanCycleID, in.StreamWatermarkAt.UTC(), in.SourceCursor,
			in.ScanCeilingAt.UTC(), in.ScanCeilingCursor, optionalString(in.ScanSnapshotID),
			nullableSnapshotRowCount(in.ScanSnapshotID, in.ScanSnapshotRowCount), in.Sequence,
			in.ScanComplete, in.ProjectionStatus)
		if cycleErr != nil {
			// The one-active-cycle partial unique index (source_economic_one_active_scan_cycle)
			// is not the ON CONFLICT arbiter above, so a second, different scan_cycle_id
			// racing an already-processing cycle surfaces here as a real unique_violation,
			// not as a RowsAffected()==0 update miss. That is an expected, temporary
			// rejection, not a data conflict -- callers must back off and retry, not fail.
			var pgErr *pgconn.PgError
			if errors.As(cycleErr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "source_economic_one_active_scan_cycle" {
				return SourceBatchResult{}, domain.ErrScanCycleBusy
			}
			return SourceBatchResult{}, fmt.Errorf("record economic scan cycle: %w", cycleErr)
		}
		if command.RowsAffected() != 1 {
			return SourceBatchResult{}, domain.ErrConflict
		}
		if in.ProjectionStatus == "blocked" {
			if freezeErr := freezeSourceAccountsTx(ctx, tx, in.SourceInstanceID, "SOURCE_GAP",
				"source_stream", in.StreamID, "", in.Actor); freezeErr != nil {
				return SourceBatchResult{}, freezeErr
			}
		}
	}
	for _, event := range in.Events {
		var priorHash string
		lookupErr := tx.QueryRow(ctx, `
			SELECT payload_hash FROM source_ingest_events
			WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3`,
			in.SourceInstanceID, in.StreamID, event.EventID).Scan(&priorHash)
		if lookupErr == nil {
			if priorHash != event.PayloadHash {
				return SourceBatchResult{}, domain.ErrConflict
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return SourceBatchResult{}, lookupErr
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO source_ingest_events(
					source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
					payload_hash,payload_ciphertext,observed_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.SourceInstanceID, in.StreamID,
				event.EventID, in.BatchID, event.EntityType, event.Operation, event.PayloadHash,
				event.PayloadCiphertext, event.ObservedAt)
			if err != nil {
				return SourceBatchResult{}, fmt.Errorf("insert source event: %w", err)
			}
		}
		if in.SchemaVersion == "3.0" {
			_, err = tx.Exec(ctx, `
				INSERT INTO source_economic_scan_cycle_events(
					source_instance_id,stream_id,scan_cycle_id,event_id,batch_id,payload_hash)
				VALUES($1,$2,$3,$4,$5,$6)
				ON CONFLICT(source_instance_id,stream_id,scan_cycle_id,event_id) DO NOTHING`,
				in.SourceInstanceID, in.StreamID, in.ScanCycleID, event.EventID, in.BatchID, event.PayloadHash)
			if err != nil {
				return SourceBatchResult{}, fmt.Errorf("map economic scan cycle event: %w", err)
			}
		}
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_state SET sequence=$1,last_batch_hash=$2,
			source_runtime_version=$3,source_agent_version=$4,projection_status=$5,last_accepted_at=now(),
			last_nonempty_batch_at=CASE WHEN $6::integer>0 THEN now() ELSE last_nonempty_batch_at END,
			updated_at=now()
		WHERE source_instance_id=$7 AND stream_id=$8 AND sequence=$9`,
		in.Sequence, in.BodyHash, in.SourceRuntimeVersion, in.SourceAgentVersion,
		in.ProjectionStatus, len(in.Events), in.SourceInstanceID, in.StreamID, currentSequence)
	if err != nil {
		return SourceBatchResult{}, err
	}
	if command.RowsAffected() != 1 {
		return SourceBatchResult{}, domain.ErrVersionConflict
	}
	if err = writeAudit(ctx, tx, in.Actor, "source_batch.committed", "source_batch",
		in.SourceInstanceID+"/"+in.StreamID+"/"+in.BatchID,
		nil, map[string]any{"sequence": in.Sequence, "body_hash": in.BodyHash, "records": len(in.Events),
			"source_runtime_version": in.SourceRuntimeVersion, "source_agent_version": in.SourceAgentVersion,
			"projection_status": in.ProjectionStatus}); err != nil {
		return SourceBatchResult{}, err
	}
	if in.SchemaVersion == "3.0" {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, in.SourceInstanceID, in.StreamID, in.Actor); err != nil {
			return SourceBatchResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return SourceBatchResult{}, err
	}
	return SourceBatchResult{AcceptedRecords: len(in.Events), Sequence: in.Sequence}, nil
}

// scanCycleSupersededAction is the audit action recorded when
// supersedeStaleActiveScanCycleTx (XM-INV-SCAN-CYCLE-SUPERSEDE) blocks a
// stale active scan cycle to let a new one take its place.
const scanCycleSupersededAction = "source.scan_cycle.superseded"

// scanCycleSupersedeReason is the free-text reason persisted on the
// abandoned row itself (source_economic_scan_cycles.supersede_reason,
// migration 0022) so an operator reading that table directly -- not just the
// audit log -- can see why a row went 'blocked' without an explicit freeze.
const scanCycleSupersedeReason = "stale active scan cycle abandoned by an agent restart; superseded by a new scan cycle after exceeding the rescan activity grace window"

// supersedeStaleActiveScanCycleTx is XM-INV-SCAN-CYCLE-SUPERSEDE's fix for the
// production incident where an agent restart (XM-INV-AGENT-RESTART-GRACE part
// B's shouldAbandonLegacyReconcileCycle) abandons a legacy in-flight scan
// cycle client-side and starts a brand-new scan_cycle_id from its rolling
// baseline, but nothing server-side ever closed the orphaned row the new
// cycle replaces: CommitSourceBatch's one-active-cycle partial unique index
// (source_economic_one_active_scan_cycle, migration 0009) rejected every
// batch of the new cycle as domain.ErrScanCycleBusy forever, and the agent
// treats that 503 as transient and retries without end.
//
// Called (from CommitSourceBatch, only for schema v3 batches) inside the same
// transaction that already holds the per-(source,stream) advisory xact lock
// taken at the top of CommitSourceBatch, so no concurrent CommitSourceBatch
// call for this stream can race this decision -- the SELECT below is a
// consistent, exclusive read of "the" active cycle, not a check that could go
// stale before the INSERT that follows it.
//
//   - No active cycle at all, or the active cycle's id matches in.ScanCycleID
//     (a continuation batch of the same cycle): nothing to do, the caller's
//     INSERT proceeds exactly as before this feature existed.
//   - A different active cycle id that is still fresh -- its updated_at is
//     within in.StaleActiveScanCycleMaxAge of in.Now, the exact same
//     "still looks like a live rescan" test evaluateSourceStreamHealth uses
//     for readiness grace (economicRescanActivityWithinWindow) -- returns
//     domain.ErrScanCycleBusy, matching the pre-existing comment on the
//     INSERT's unique_violation mapping below: this is the expected,
//     temporary rejection while a genuinely active cycle owns the stream.
//   - A different active cycle id that has gone stale (no update within that
//     same window) is marked 'blocked' with superseded_by_scan_cycle_id/
//     supersede_reason set and an audit event recorded, then this returns nil
//     so the caller's INSERT proceeds: the partial unique index no longer
//     sees a conflicting row by the time that INSERT runs.
//
// in.Now.IsZero() or in.StaleActiveScanCycleMaxAge<=0 (the caller did not opt
// in) disables this entirely and always returns domain.ErrScanCycleBusy for a
// different active cycle id, preserving the pre-existing behavior byte for
// byte.
func supersedeStaleActiveScanCycleTx(ctx context.Context, tx pgx.Tx, in SourceBatchInput) error {
	var activeCycleID, activeCycleStatus string
	var activeUpdatedAt time.Time
	var activeFirstSequence, activeLastSequence int64
	err := tx.QueryRow(ctx, `
		SELECT scan_cycle_id::text,cycle_status,updated_at,first_sequence,last_sequence
		FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id=$2 AND cycle_status IN ('receiving','processing')
		FOR UPDATE`, in.SourceInstanceID, in.StreamID).Scan(
		&activeCycleID, &activeCycleStatus, &activeUpdatedAt, &activeFirstSequence, &activeLastSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if activeCycleID == in.ScanCycleID {
		return nil
	}
	if in.Now.IsZero() || in.StaleActiveScanCycleMaxAge <= 0 {
		return domain.ErrScanCycleBusy
	}
	stillFresh := economicRescanActivityWithinWindow(activeUpdatedAt, SourceFreshnessPolicy{
		Now: in.Now, EconomicRescanActivityMaxAge: in.StaleActiveScanCycleMaxAge,
	})
	if stillFresh {
		return domain.ErrScanCycleBusy
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_economic_scan_cycles
		SET cycle_status='blocked',superseded_by_scan_cycle_id=$1::uuid,supersede_reason=$2,updated_at=now()
		WHERE source_instance_id=$3 AND stream_id=$4 AND scan_cycle_id=$5::uuid`,
		in.ScanCycleID, scanCycleSupersedeReason, in.SourceInstanceID, in.StreamID, activeCycleID); err != nil {
		return fmt.Errorf("supersede stale active scan cycle: %w", err)
	}
	if err = writeAudit(ctx, tx, in.Actor, scanCycleSupersededAction, "source_scan_cycle",
		in.SourceInstanceID+"/"+in.StreamID+"/"+activeCycleID,
		map[string]any{
			"scan_cycle_id": activeCycleID, "cycle_status": activeCycleStatus,
			"first_sequence": activeFirstSequence, "last_sequence": activeLastSequence,
			"updated_at": activeUpdatedAt,
		},
		map[string]any{
			"scan_cycle_id": in.ScanCycleID, "superseded_scan_cycle_id": activeCycleID,
			"sequence": in.Sequence, "cycle_status": "blocked",
		}); err != nil {
		return err
	}
	return nil
}

const sourceEventLease = 10 * time.Minute

// claimBindingSelect is XM-INV-CLAIM-BINDING: the lateral that decides which
// source_ingest_batches row a claim carries -- and therefore which
// (event_id, batch_id, scan_cycle_id) triple verifyFactBatchContextTx is
// later handed.
//
// It used to be sie.first_batch_id, unconditionally: the batch an event
// arrived in, frozen forever. That is wrong once the agent restarts.
// supersedeStaleActiveScanCycleTx marks the abandoned cycle 'blocked', and
// any event that had not finished is pinned to a binding the verifier now
// refuses -- permanently, because first_batch_id never moves. Meanwhile the
// agent's own rescan re-delivers the identical event (ids are deterministic:
// agents/sourceagent/batch.go's deterministicUUID over
// source/entity/external id/operation/payload hash), and CommitSourceBatch
// writes a fresh mapping row under the new, healthy cycle while deliberately
// leaving the existing ingest row alone. The valid binding is therefore
// already on file; nothing was looking at it. Production 2026-09-08: two
// dead usage events each carried a 'blocked' binding on their first batch and
// a 'published' one on a later batch.
//
// So: prefer the event's newest *currently valid* economic binding -- the
// agent's latest statement about this event -- and fall back to
// first_batch_id when there is none.
//
// This is not a relaxation of verifyFactBatchContextTx, which is untouched.
// It hands that function a triple that is *true*: the mapping row is real,
// written by CommitSourceBatch inside the transaction that verified the
// batch's hash chain and signing key. Three of the conditions below are the
// verifier's own (schema_version='3.0', mapping payload_hash equal to the
// event's, cycle status in its accepted set), so the preferred branch can
// only ever pick a binding the verifier would accept.
//
// The fourth condition -- factClockSkewTolerance on b.scan_ceiling_at against
// sie.observed_at -- is deliberately NOT the verifier's. It is
// validateFactMetadata's (consumption.go), which runs *earlier* than the
// verifier and refuses the fact outright, so a binding that fails it never
// reaches verifyFactBatchContextTx at all. Without it the preference above is
// a trap: an event parked for hours and then woken picks a re-delivery whose
// ceiling its own frozen observed_at can never carry, and burns all eight
// attempts on a rule the verifier is never reached to apply. Production
// 2026-09-07: a customer binding woke usage events first observed at
// 07:08:33; the newest binding's ceiling was 09:28:14, two hours fifteen
// later, and every attempt returned "source fact event time/watermark is
// invalid" until the events were dead and the whole source latched.
//
// The fallback is deliberate rather than a strict refusal to claim. An event
// whose only bindings are unusable keeps today's behavior exactly: it is
// claimed, the verifier refuses it, and after eight attempts it becomes
// 'dead' -- which readiness reports as "contains dead events" and
// invoice-eligibility-repair --kind=ingest-requeue-dead explains per row.
// Declining to claim it instead would leave it 'queued' forever, counted in
// SourceIngestHealth.Pending against a created_at hours old, so /readyz would
// fail with the generic "processing is unhealthy" and no pointer to the
// event -- a named failure traded for an unnamed one. See the handoff.
//
// Side effects of preferring a later batch all point at "more correct": the
// fact records the source_sequence and scan_ceiling_at of the scan that
// actually observed it, not of a scan that was abandoned.
// Not a const: the time predicate below is rendered from
// factClockSkewToleranceSQL so the five minutes stays stated in exactly one
// place. Both callers already concatenate this fragment at runtime
// (ClaimUnprocessedSourceEvents below and ingestRequeueDeadReplayBindingTx in
// ingest_requeue_dead_repair.go), so nothing needs it to be constant-foldable.
var claimBindingSelect = `
	JOIN LATERAL (
		SELECT chosen.sequence,chosen.batch_id,chosen.schema_version,chosen.signing_key_id,
			chosen.stream_watermark_at,chosen.source_cursor,chosen.scan_ceiling_at,
			chosen.scan_ceiling_cursor,chosen.scan_cycle_id,chosen.scan_complete
		FROM (
			-- Both arms are parenthesized: PostgreSQL rejects a bare
			-- ORDER BY/LIMIT inside one arm of a UNION.
			(
				SELECT b.sequence,b.batch_id,b.schema_version,b.signing_key_id,
					b.stream_watermark_at,b.source_cursor,b.scan_ceiling_at,
					b.scan_ceiling_cursor,b.scan_cycle_id,b.scan_complete,1 AS preference
				FROM source_economic_scan_cycle_events m
				JOIN source_ingest_batches b ON b.source_instance_id=m.source_instance_id
					AND b.stream_id=m.stream_id AND b.batch_id=m.batch_id
				JOIN source_economic_scan_cycles c ON c.source_instance_id=m.source_instance_id
					AND c.stream_id=m.stream_id AND c.scan_cycle_id=m.scan_cycle_id
				WHERE m.source_instance_id=sie.source_instance_id AND m.stream_id=sie.stream_id
					AND m.event_id=sie.event_id AND m.payload_hash=sie.payload_hash
					AND b.schema_version='3.0'
					AND c.cycle_status IN ('receiving','processing','published')
					-- XM-INV-BINDING-SKEW: and a batch whose ceiling this
					-- event's own observation can actually carry. The claim
					-- hands scan_ceiling_at over as the fact's
					-- stream_watermark_at (application/source_processor.go:
					-- every Observe* call site passes
					-- StreamWatermarkAt: claim.ScanCeilingAt) against the
					-- event's observed_at, which source_ingest_events freezes
					-- at first delivery and never rewrites on a re-delivery.
					-- validateFactMetadata refuses that pair past
					-- factClockSkewTolerance, before verifyFactBatchContextTx
					-- is ever reached, so a binding this predicate excludes is
					-- one the runtime would spend all eight attempts refusing.
					-- Production 2026-09-07: observed 07:08:33, newest
					-- binding's ceiling 09:28:14, eight refusals of "source
					-- fact event time/watermark is invalid".
					--
					-- If XM-INV-OBSERVED-AT-PER-BINDING (L2) lands, this
					-- predicate and the outer query must move to
					-- COALESCE(m.observed_at,sie.observed_at) together: left
					-- as is, it would exclude the very re-delivery L2 exists
					-- to make usable.
					AND b.scan_ceiling_at<=sie.observed_at+` + factClockSkewToleranceSQL + `
				ORDER BY b.sequence DESC
				LIMIT 1
			)
			UNION ALL
			(
				SELECT fb.sequence,fb.batch_id,fb.schema_version,fb.signing_key_id,
					fb.stream_watermark_at,fb.source_cursor,fb.scan_ceiling_at,
					fb.scan_ceiling_cursor,fb.scan_cycle_id,fb.scan_complete,2 AS preference
				FROM source_ingest_batches fb
				WHERE fb.source_instance_id=sie.source_instance_id AND fb.stream_id=sie.stream_id
					AND fb.batch_id=sie.first_batch_id
			)
		) chosen
		ORDER BY chosen.preference
		LIMIT 1
	) sib ON TRUE`

func (s *Store) ClaimUnprocessedSourceEvents(ctx context.Context, limit int, now time.Time) ([]SourceEventClaim, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
		SELECT sie.source_instance_id,si.source_type,sie.stream_id,sie.event_id,
			sie.entity_type,sie.operation,sie.payload_hash,sie.payload_ciphertext,
			sie.observed_at,sie.attempt_count,sib.sequence,sib.batch_id,sib.schema_version,sib.signing_key_id,
			COALESCE(sib.stream_watermark_at,'epoch'::timestamptz),COALESCE(sib.source_cursor,''),
			COALESCE(sib.scan_ceiling_at,'epoch'::timestamptz),COALESCE(sib.scan_ceiling_cursor,''),
			COALESCE(sib.scan_cycle_id::text,''),sib.scan_complete,COALESCE(sie.catchup_key_hmac,'')
		FROM source_ingest_events sie JOIN source_instances si ON si.id=sie.source_instance_id` +
		claimBindingSelect + `
		WHERE (
			(sie.processing_status IN ('queued','failed') AND sie.attempt_count < $3 AND sie.next_attempt_at <= $1)
			OR (sie.processing_status='waiting_dependency' AND sie.next_attempt_at <= $1)
			OR (sie.processing_status='processing' AND sie.lease_expires_at <= $1)
		)
		ORDER BY sie.next_attempt_at,sie.created_at,sie.event_id
		FOR UPDATE OF sie SKIP LOCKED LIMIT $2`, now, limit, sourceEventDeadThreshold)
	if err != nil {
		return nil, err
	}
	claims := make([]SourceEventClaim, 0, limit)
	for rows.Next() {
		var claim SourceEventClaim
		if err = rows.Scan(&claim.SourceInstanceID, &claim.SourceType, &claim.StreamID,
			&claim.EventID, &claim.EntityType, &claim.Operation, &claim.PayloadHash,
			&claim.PayloadCiphertext, &claim.ObservedAt, &claim.Attempt, &claim.BatchSequence, &claim.BatchID,
			&claim.SchemaVersion, &claim.SigningKeyID, &claim.StreamWatermarkAt, &claim.BatchSourceCursor,
			&claim.ScanCeilingAt, &claim.ScanCeilingCursor, &claim.ScanCycleID, &claim.ScanComplete,
			&claim.CatchupKeyHMAC); err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, claim)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range claims {
		claims[i].LeaseToken = randomUUID()
		claims[i].Attempt++
		command, updateErr := tx.Exec(ctx, `
			UPDATE source_ingest_events SET processing_status='processing',
				attempt_count=attempt_count+1,lease_token=$1,lease_expires_at=$2,
				dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
			WHERE source_instance_id=$3 AND stream_id=$4 AND event_id=$5`,
			claims[i].LeaseToken, now.Add(sourceEventLease), claims[i].SourceInstanceID,
			claims[i].StreamID, claims[i].EventID)
		if updateErr != nil {
			return nil, updateErr
		}
		if command.RowsAffected() != 1 {
			return nil, domain.ErrConflict
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Store) MarkSourceEventProcessed(ctx context.Context, claim SourceEventClaim, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processed_at=$1,
			lease_token=NULL,lease_expires_at=NULL,processing_error=NULL,updated_at=$1
		WHERE source_instance_id=$2 AND stream_id=$3 AND event_id=$4
			AND processing_status='processing' AND lease_token=$5`, now,
		claim.SourceInstanceID, claim.StreamID, claim.EventID, claim.LeaseToken)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	if claim.SchemaVersion == "3.0" {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, claim.SourceInstanceID, claim.StreamID,
			AuditActor{Type: "source_connector", ID: claim.SourceInstanceID, Reason: "v3 scan cycle facts completed"}); err != nil {
			return err
		}
	}
	if claim.CatchupKeyHMAC != "" {
		if err = completeEligibilityCatchupTx(ctx, tx, claim.SourceInstanceID, claim.CatchupKeyHMAC,
			AuditActor{Type: "source_connector", ID: claim.SourceInstanceID, Reason: "parked identity facts caught up"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// hintAccountID is the external_account_id the application layer already
// resolved from the decrypted payload before this event's processing
// failed, or "" if it never got that far (payload undecryptable, or the
// account itself could not be resolved). See XM-INV-PROOF-CONTENTION 5 in
// the dead-status branch below for why this is needed in addition to the
// pre-existing source_revision_hash correlation.
//
// The returned status is the grade this call actually applied, either
// SourceEventDead or SourceEventFailed. XM-INV-READYZ-DETAIL: the CASE
// expression below has always computed it, but the method used to return only
// an error, so the one caller could not tell the irreversible outcome from the
// routine one and logged both identically -- the eighth failure, which pins
// /readyz at 503 until an operator intervenes, was indistinguishable from the
// first seven. Returning it is what lets that caller raise the dead
// transition to Error level.
func (s *Store) MarkSourceEventFailed(ctx context.Context, claim SourceEventClaim, errorCode string, next time.Time, hintAccountID string) (string, error) {
	errorCode = strings.TrimSpace(errorCode)
	if errorCode == "" || len(errorCode) > 512 || strings.ContainsAny(errorCode, "\r\n\x00") {
		return "", errors.New("invalid source event processing error code")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var newStatus string
	var attemptCount int
	err = tx.QueryRow(ctx, `
		UPDATE source_ingest_events SET
			processing_status=CASE WHEN attempt_count>=$7 THEN 'dead' ELSE 'failed' END,
			processing_error=$1,next_attempt_at=$2,lease_token=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE source_instance_id=$3 AND stream_id=$4 AND event_id=$5
			AND processing_status='processing' AND lease_token=$6
		RETURNING processing_status,attempt_count`, errorCode, next,
		claim.SourceInstanceID, claim.StreamID, claim.EventID, claim.LeaseToken,
		sourceEventDeadThreshold).Scan(&newStatus, &attemptCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrConflict
	}
	if err != nil {
		return "", err
	}
	if newStatus == SourceEventDead {
		// XM-INV-READYZ-DETAIL: the same durable record eligibility's own
		// terminal grade already writes (eligibility.projection.dead, in
		// markEligibilityProjectionJobFailedOrDead), for the one dead path
		// that never had it. Shaped after this table's existing audit event
		// source_ingest_event.repair_requeued (eligibility_repair.go) --
		// same action prefix, same object_type/object_id, same habit of
		// carrying source_instance_id and stream_id in the payload -- and
		// after eligibility.projection.dead for the grading fields
		// (attempts, the error, the threshold). Written inside this
		// transaction, so it commits with the row it describes or not at
		// all; the freeze below is conditional on correlation succeeding,
		// but this is not, which is the gap it closes.
		if err = writeAudit(ctx, tx, AuditActor{Type: "source_connector", ID: claim.SourceInstanceID,
			Reason: "source event reached the terminal dead grade"},
			"source_ingest_event.dead", "source_ingest_event", claim.EventID, nil,
			map[string]any{"source_instance_id": claim.SourceInstanceID, "stream_id": claim.StreamID,
				"entity_type": claim.EntityType, "attempts": attemptCount,
				"processing_error": errorCode, "threshold": sourceEventDeadThreshold}); err != nil {
			return "", err
		}
		// XM-INV-POLICY-ANCHOR 2.5: a dead event with no freeze yet would
		// otherwise hold its scan cycle open indefinitely (see the freeze
		// correlation in tryPublishEconomicScanCyclesTx). There is no
		// ingest-layer column identifying which account an encrypted,
		// unprocessed event belongs to; correlate via source_revision_hash,
		// the same content-hash link every domain fact table already carries
		// (and the exact one the cycle-completeness query matches an
		// *existing* freeze against). This only finds a match when the fact
		// was actually persisted on some earlier attempt -- a pure repeated
		// transient failure that never wrote anything leaves nothing to
		// correlate, and stays un-frozen (still holds the cycle, unchanged;
		// see the handoff doc for why this is a structural gap, not an
		// oversight).
		var accountID, objectType, objectID string
		lookupErr := tx.QueryRow(ctx, `
			SELECT external_account_id,'balance_checkpoint',checkpoint_id
				FROM balance_reconciliation_checkpoints WHERE source_revision_hash=$1
			UNION ALL
			SELECT external_account_id,'usage',external_usage_id
				FROM source_usage_events WHERE source_revision_hash=$1
			UNION ALL
			SELECT external_account_id,'credit',external_credit_id
				FROM source_credit_events WHERE source_revision_hash=$1
			LIMIT 1`, claim.PayloadHash).Scan(&accountID, &objectType, &objectID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return "", lookupErr
		}
		reason := "dead event correlated to a persisted fact"
		if errors.Is(lookupErr, pgx.ErrNoRows) && hintAccountID != "" {
			// XM-INV-PROOF-CONTENTION 5: the correlation above only finds a
			// match when some earlier attempt actually persisted the domain
			// fact -- a dead event whose every attempt was rejected before
			// any INSERT (e.g. a deterministic validation failure) leaves
			// nothing to correlate here, even though the application layer
			// decrypted the payload and already resolved its account on the
			// same attempt that ultimately failed. Use that hint instead so
			// the freeze -- and the cycle publish it unblocks, see
			// tryPublishEconomicScanCyclesTx -- still happens. There is no
			// persisted fact to point trigger_object_id at, so point it at
			// the ingest event itself.
			accountID, objectType, objectID = hintAccountID, claim.EntityType, claim.EventID
			lookupErr = nil
			reason = "dead event attributed via the application layer's resolved account"
		}
		if lookupErr == nil && accountID != "" {
			if err = freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_DEAD", objectType, objectID,
				claim.PayloadHash, AuditActor{Type: "source_connector", ID: claim.SourceInstanceID,
					Reason: reason}); err != nil {
				return "", err
			}
		}
	}
	if claim.SchemaVersion == "3.0" {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, claim.SourceInstanceID, claim.StreamID,
			AuditActor{Type: "source_connector", ID: claim.SourceInstanceID, Reason: "source event marked failed/dead"}); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return newStatus, nil
}

func (s *Store) MarkSourceEventWaitingDependency(ctx context.Context, claim SourceEventClaim, kind, keyHMAC string, next time.Time) error {
	if (kind != "invoice_oidc_user" && kind != "source_external_account" && kind != "source_funding_lot" && kind != "source_eligibility_cutover" && kind != "source_cutover_manifest") ||
		!dependencyHMACPattern.MatchString(keyHMAC) || next.IsZero() {
		return errors.New("invalid source dependency wait")
	}
	status := "waiting_dependency"
	if kind == "invoice_oidc_user" || kind == "source_external_account" || kind == "source_eligibility_cutover" {
		// Identity-shaped waits park instead of blocking: parked_identity is
		// excluded from scan-cycle completeness, released only by its exact
		// dependency wake, and never swept. source_eligibility_cutover joined
		// this set in RC62: it fires when an account is BOUND but its
		// eligibility state has not bootstrapped yet -- exactly the state a
		// real customer left behind by logging in once during a window where
		// binding succeeded and nothing woke their parked signed-cutover row.
		// As waiting_dependency this held the stream's active scan cycle in
		// 'processing' (waiting events count as incomplete), which blocked
		// every new agent cycle behind the one-active-cycle constraint --
		// one stranger's half-provisioned login froze the whole stream. Their
		// facts still materialize the moment their eligibility bootstraps
		// (the checkpoint/POST_CUTOVER paths fire this wake), matching the
		// long-standing parked_identity semantics for unbound users.
		status = "parked_identity"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status=$8,
			attempt_count=greatest(attempt_count-1,0),processing_error='WAITING_DEPENDENCY',
			dependency_kind=$1,dependency_key_hmac=$2,next_attempt_at=$3,
			lease_token=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE source_instance_id=$4 AND stream_id=$5 AND event_id=$6
			AND processing_status='processing' AND lease_token=$7`,
		kind, keyHMAC, next, claim.SourceInstanceID, claim.StreamID, claim.EventID, claim.LeaseToken, status)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	if status == "parked_identity" && claim.SchemaVersion == "3.0" {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, claim.SourceInstanceID, claim.StreamID,
			AuditActor{Type: "source_connector", ID: claim.SourceInstanceID, Reason: "identity-unbound fact parked durably"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// MarkSourceEventBusy reschedules a claim shortly without spending any of
// its attempt/retry budget. XM-INV-PROOF-CONTENTION 2: ObserveUsageEvent/
// ObserveCreditEvent/ObserveBalanceCheckpoint's per-account
// pg_try_advisory_xact_lock found the account's advisory lock
// (hashtextextended(...,43)) held by a concurrent eligibility projection
// job. That is routine, expected contention -- not a processing failure --
// so unlike MarkSourceEventFailed this credits the attempt back (mirrors
// MarkSourceEventWaitingDependency's attempt_count-1) instead of spending
// it, and reuses the existing 'queued' status rather than adding a new one.
func (s *Store) MarkSourceEventBusy(ctx context.Context, claim SourceEventClaim, marker string, next time.Time) error {
	if next.IsZero() {
		return errors.New("invalid source event busy reschedule")
	}
	if !isTransientRequeueMarker(marker) {
		return fmt.Errorf("unknown transient requeue marker %q", marker)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='queued',
			attempt_count=greatest(attempt_count-1,0),processing_error=$6,
			dependency_kind=NULL,dependency_key_hmac=NULL,next_attempt_at=$1,
			lease_token=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE source_instance_id=$2 AND stream_id=$3 AND event_id=$4
			AND processing_status='processing' AND lease_token=$5`,
		next, claim.SourceInstanceID, claim.StreamID, claim.EventID, claim.LeaseToken, marker)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) RequeueSourceDependency(ctx context.Context, kind, keyHMAC string) (int64, error) {
	if (kind != "invoice_oidc_user" && kind != "source_external_account" && kind != "source_funding_lot" && kind != "source_eligibility_cutover" && kind != "source_cutover_manifest") ||
		!dependencyHMACPattern.MatchString(keyHMAC) {
		return 0, errors.New("invalid source dependency wakeup")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if kind == "invoice_oidc_user" || kind == "source_external_account" {
		// XM-INV-POLICY-ANCHOR 2.2: an identity wake can release a large
		// backlog of parked usage/balance facts that predate the invoice
		// policy start -- useless for invoicing regardless of which cutover
		// boundary the account eventually bootstraps at. Acknowledge them
		// here instead of releasing them into another park/replay cycle.
		// Payments, credits, identities and manifests are never skipped: few
		// in number, and legacy funding lots keep their pre-eligibility
		// display state.
		var policyStartAt time.Time
		if err = tx.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
			WHERE singleton_id=1`).Scan(&policyStartAt); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `
			UPDATE source_ingest_events SET processing_status='processed',processing_error='PRE_POLICY_SKIPPED',
				processed_at=now(),lease_token=NULL,lease_expires_at=NULL,
				dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
			WHERE processing_status IN ('waiting_dependency','parked_identity') AND dependency_kind=$1 AND dependency_key_hmac=$2
				AND entity_type IN ('usage_event','balance_checkpoint') AND observed_at<$3`,
			kind, keyHMAC, policyStartAt); err != nil {
			return 0, err
		}
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='queued',processing_error=NULL,
			catchup_key_hmac=CASE WHEN processing_status='parked_identity' THEN $2 ELSE catchup_key_hmac END,
			dependency_kind=NULL,dependency_key_hmac=NULL,
			next_attempt_at=now()-interval '1 second',updated_at=now()
		WHERE processing_status IN ('waiting_dependency','parked_identity') AND dependency_kind=$1 AND dependency_key_hmac=$2`,
		kind, keyHMAC)
	if err != nil {
		return 0, err
	}
	released := command.RowsAffected()
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return released, nil
}

func (s *Store) SourceIngestHealth(ctx context.Context) (SourceIngestHealth, error) {
	var out SourceIngestHealth
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE processing_status IN ('queued','failed','processing')),
			count(*) FILTER (WHERE processing_status='dead'),
			count(*) FILTER (WHERE processing_status IN ('waiting_dependency','parked_identity')),
			COALESCE(min(created_at) FILTER (WHERE processing_status IN ('queued','failed','processing')),'epoch'::timestamptz)
		FROM source_ingest_events`).Scan(&out.Pending, &out.Dead, &out.Waiting, &out.OldestPending)
	if out.OldestPending.Equal(time.Unix(0, 0).UTC()) {
		out.OldestPending = time.Time{}
	}
	return out, err
}

func maximumHeartbeatAgeForStream(policy SourceFreshnessPolicy, streamID string) time.Duration {
	if streamID == "identities" {
		return policy.IdentitiesMaxAge
	}
	return policy.EconomicHeartbeatMaxAge
}

// economicRescanActiveReason is the XM-INV-AGENT-RESTART-GRACE part A
// honesty-guard downgrade of ECONOMIC_WATERMARK_STALE: it still appears in
// SourceStreamHealth.Reasons for the admin source-health report, but --
// unlike every other reason -- it does not make the stream not-ready. See
// evaluateSourceStreamHealth and the sole other non-fatal reason,
// EVENTS_PENDING, which is instead tolerated one layer up in
// validateSourceRuntimeReadiness (backend/cmd/api/runtime.go); this one must
// live here because ListFundingLots/Submit's five-stream gate reads
// stream.Ready directly and never goes through that readyz-only layer.
const economicRescanActiveReason = "ECONOMIC_RESCAN_ACTIVE"

// nonFatalStreamHealthReasons lists reasons that are surfaced for visibility
// but never make item.Ready false on their own.
var nonFatalStreamHealthReasons = map[string]bool{economicRescanActiveReason: true}

// economicRescanActivityWithinWindow reports whether ActiveRescanUpdatedAt
// proves a genuinely still-progressing scan cycle, per the bounded activity
// window in policy.EconomicRescanActivityMaxAge (zero disables the grace
// entirely). A cycle whose updated_at stopped advancing beyond the window --
// stalled, crashed, or simply old -- does not count: evaluateSourceStreamHealth
// then reports the plain, fatal ECONOMIC_WATERMARK_STALE instead.
func economicRescanActivityWithinWindow(activeAt time.Time, policy SourceFreshnessPolicy) bool {
	if activeAt.IsZero() || policy.EconomicRescanActivityMaxAge <= 0 {
		return false
	}
	age := policy.Now.Sub(activeAt)
	return age >= 0 && age <= policy.EconomicRescanActivityMaxAge && !activeAt.After(policy.Now.Add(5*time.Minute))
}

func evaluateSourceStreamHealth(item *SourceStreamHealth, policy SourceFreshnessPolicy) {
	item.MaximumAgeSeconds = int64(maximumHeartbeatAgeForStream(policy, item.StreamID) / time.Second)
	item.EconomicWatermarkMaximumAgeSeconds = 0
	if item.StreamID != "identities" {
		item.EconomicWatermarkMaximumAgeSeconds = int64(policy.EconomicWatermarkMaxAge / time.Second)
	}
	item.Reasons = make([]string, 0, 5)
	if !item.SourceEnabled {
		item.Reasons = append(item.Reasons, "SOURCE_DISABLED")
	}
	if item.LastAcceptedAt.IsZero() {
		item.Reasons = append(item.Reasons, "STREAM_NEVER_ACCEPTED")
	} else if policy.Now.Sub(item.LastAcceptedAt) > maximumHeartbeatAgeForStream(policy, item.StreamID) || item.LastAcceptedAt.After(policy.Now.Add(5*time.Minute)) {
		item.Reasons = append(item.Reasons, "STREAM_STALE")
	}
	if strings.TrimSpace(item.ApprovedRuntimeVersion) == "" || item.ObservedRuntimeVersion != item.ApprovedRuntimeVersion {
		item.Reasons = append(item.Reasons, "RUNTIME_VERSION_UNAPPROVED")
	}
	if item.ProjectionStatus != "healthy" {
		item.Reasons = append(item.Reasons, "PROJECTION_BLOCKED")
	}
	if item.StreamID != "identities" && item.EconomicWatermarkAt.IsZero() {
		item.Reasons = append(item.Reasons, "ECONOMIC_WATERMARK_NEVER_PUBLISHED")
	} else if item.StreamID != "identities" &&
		(policy.Now.Sub(item.EconomicWatermarkAt) > policy.EconomicWatermarkMaxAge ||
			item.EconomicWatermarkAt.After(policy.Now.Add(5*time.Minute))) {
		if economicRescanActivityWithinWindow(item.ActiveRescanUpdatedAt, policy) {
			item.Reasons = append(item.Reasons, economicRescanActiveReason)
		} else {
			item.Reasons = append(item.Reasons, "ECONOMIC_WATERMARK_STALE")
		}
	}
	if item.PendingEvents > 0 {
		item.Reasons = append(item.Reasons, "EVENTS_PENDING")
	}
	if item.DeadEvents > 0 {
		item.Reasons = append(item.Reasons, "EVENTS_DEAD")
	}
	item.Ready = true
	for _, reason := range item.Reasons {
		if !nonFatalStreamHealthReasons[reason] {
			item.Ready = false
			break
		}
	}
}

// accountLockBusyReadinessGrace bounds how long an event requeued with
// ACCOUNT_LOCK_BUSY stays invisible to readiness. XM-INV-CATCHUP-BURST-BACKPRESSURE
// fix 2: a checkpoint event for an account whose projection is in its write
// phase is requeued 15 seconds at a time and counted as pending, and one such
// event flips the whole deployment to 503 within a minute -- that, not a stale
// watermark, is what turned readiness off at 03:45 on 2026-09-04. The busy
// state is benign and self-limiting (the write phase holds the lock for
// seconds to a couple of minutes now that the proof runs first), so it gets
// the same treatment as ECONOMIC_RESCAN_ACTIVE: tolerated for a bounded time,
// then counted again. The bound is deliberately shorter than
// SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS (15m) so a stream that is genuinely
// stuck behind a lock still fails readiness through the freshness gate; this
// only stops a short, expected wait from masquerading as an outage.
const accountLockBusyReadinessGrace = "10 minutes"

// Transient requeue markers. A claim that hits one of these is rescheduled
// shortly *without* spending its attempt budget, so contention alone can never
// drive an event to the dead threshold. They are listed once, here, because two
// places need the same set and must not drift: MarkSourceEventBusy writes one of
// them, and sourceReadinessHealthQuery's busy_within_grace predicate reads them
// back. The previous shape pinned the literal 'ACCOUNT_LOCK_BUSY' separately in
// each place; adding a second marker that way is exactly how a grace silently
// stops covering half of what it was written to cover.
const (
	TransientRequeueAccountLockBusy = "ACCOUNT_LOCK_BUSY"
	// XM-INV-SER-BUSY: PostgreSQL serialization_failure (40001) and
	// deadlock_detected (40P01). Both mean "this transaction lost a race and is
	// expected to be retried", which is the contract the busy path already
	// implements -- not the processing failure MarkSourceEventFailed records.
	// Before this marker existed a 40001 fell into the generic PROJECTION_FAILED
	// branch, spent one of eight attempts every five minutes, and killed the
	// event for good: that is what took the balances and usage streams down at
	// 08:32 on 2026-09-07 and held every funding lot unavailable for 20 hours.
	TransientRequeueSerializationBusy = "SERIALIZATION_BUSY"
)

var transientRequeueMarkers = []string{
	TransientRequeueAccountLockBusy,
	TransientRequeueSerializationBusy,
}

// transientRequeueMarkerSQL renders transientRequeueMarkers as a SQL array
// literal for the readiness grace. Rendering it from the same slice the writer
// validates against is what makes the two physically one definition rather than
// two copies that merely agree today.
var transientRequeueMarkerSQL = renderTransientRequeueMarkerSQL(transientRequeueMarkers)

var transientRequeueMarkerPattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,62}$`)

func renderTransientRequeueMarkerSQL(markers []string) string {
	if len(markers) == 0 {
		panic("transient requeue marker list must not be empty")
	}
	quoted := make([]string, len(markers))
	for i, marker := range markers {
		// The markers are compile-time constants, so a value that could change
		// the shape of the query is a programming error: stop the process
		// rather than quietly escape it into place.
		if !transientRequeueMarkerPattern.MatchString(marker) {
			panic("transient requeue marker must match ^[A-Z][A-Z_]*$: " + marker)
		}
		quoted[i] = "'" + marker + "'"
	}
	return "ARRAY[" + strings.Join(quoted, ",") + "]"
}

// IsTransientSerializationFailure reports whether err is a PostgreSQL
// serialization_failure (40001) or deadlock_detected (40P01). Both are the
// database telling the caller "you lost a race, run it again" -- a retry
// contract, not a defect in the event being processed. The projection loop
// had no branch for them, so every such conflict was recorded as a generic
// PROJECTION_FAILED and burned one of the event's eight attempts; on
// 2026-09-07 seventeen conflicts inside two hours exhausted three events and
// dead-lettered them permanently.
func IsTransientSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

func isTransientRequeueMarker(marker string) bool {
	for _, candidate := range transientRequeueMarkers {
		if candidate == marker {
			return true
		}
	}
	return false
}

var sourceReadinessHealthQuery = `
	WITH active_event_health AS MATERIALIZED (
		SELECT source_instance_id,stream_id,
			count(*) FILTER (WHERE processing_status IN ('queued','failed','processing') AND NOT busy_within_grace) AS pending_events,
			count(*) FILTER (WHERE processing_status='dead') AS dead_events,
			COALESCE(min(created_at) FILTER (WHERE processing_status IN ('queued','failed','processing') AND NOT busy_within_grace),'epoch'::timestamptz) AS oldest_pending
		FROM (
			SELECT source_instance_id,stream_id,processing_status,created_at,
				-- COALESCE on both sides: processing_error is NULL for an ordinary
				-- queued event, and NULL = 'ACCOUNT_LOCK_BUSY' is NULL, not false --
				-- a bare NOT NULL in the FILTER above would silently drop every
				-- plain pending event from the count.
				COALESCE(processing_status='queued' AND COALESCE(processing_error,'')=ANY(` + transientRequeueMarkerSQL + `)
				 AND updated_at>=now()-interval '` + accountLockBusyReadinessGrace + `',false) AS busy_within_grace
			FROM source_ingest_events
			WHERE processing_status IN ('queued','failed','processing','dead')
		) classified
		GROUP BY source_instance_id,stream_id
	), ingest_health AS (
		SELECT COALESCE(sum(pending_events),0)::bigint AS pending_events,
			COALESCE(sum(dead_events),0)::bigint AS dead_events,
			COALESCE(min(oldest_pending) FILTER (WHERE pending_events>0),'epoch'::timestamptz) AS oldest_pending
		FROM active_event_health
	)
	SELECT si.id,si.source_type,si.name,si.enabled,required.stream_id,
		COALESCE(sis.sequence,0),si.runtime_version,COALESCE((SELECT scm.source_runtime_version FROM source_cutover_manifests scm WHERE scm.source_instance_id=si.id ORDER BY scm.cutover_at DESC LIMIT 1),''),
		COALESCE(sis.source_runtime_version,''),COALESCE(sis.source_agent_version,''),COALESCE(sis.projection_status,'unknown'),
		COALESCE(sis.last_accepted_at,'epoch'::timestamptz),
		COALESCE(sis.last_nonempty_batch_at,'epoch'::timestamptz),
		COALESCE(sew.watermark_at,'epoch'::timestamptz),
		COALESCE(aeh.pending_events,0),COALESCE(aeh.dead_events,0),
		ingest.pending_events,ingest.dead_events,ingest.oldest_pending,
		COALESCE(sesc.updated_at,'epoch'::timestamptz)
	FROM source_instances si
	CROSS JOIN (VALUES ('payments'::text),('identities'::text),('usage'::text),('credits'::text),('balances'::text)) required(stream_id)
	LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id AND sis.stream_id=required.stream_id
	LEFT JOIN source_economic_stream_watermarks sew ON sew.source_instance_id=si.id AND sew.stream_kind=required.stream_id
	LEFT JOIN active_event_health aeh ON aeh.source_instance_id=si.id AND aeh.stream_id=required.stream_id
	CROSS JOIN ingest_health ingest
	-- XM-INV-AGENT-RESTART-GRACE part A: at most one row can match, via the
	-- partial unique index source_economic_one_active_scan_cycle, so this join
	-- stays index-bound regardless of how large the cycle history table grows.
	LEFT JOIN source_economic_scan_cycles sesc ON sesc.source_instance_id=si.id AND sesc.stream_id=required.stream_id
		AND sesc.cycle_status IN ('receiving','processing')
	ORDER BY si.source_type,si.id,required.stream_id`

// SourceReadinessHealth returns the same freshness/version/projection evidence
// used by SourceHealth plus only active pending/dead event state. Its query can
// be satisfied entirely from source_ingest_events_readiness_active_idx, so a
// large waiting/parked backlog cannot make /readyz scan the event table.
func (s *Store) SourceReadinessHealth(ctx context.Context, policy SourceFreshnessPolicy) (SourceReadinessHealth, error) {
	if !policy.enabled() {
		return SourceReadinessHealth{}, errors.New("source freshness policy is not configured")
	}
	rows, err := s.pool.Query(ctx, sourceReadinessHealthQuery)
	if err != nil {
		return SourceReadinessHealth{}, fmt.Errorf("query source readiness health: %w", err)
	}
	defer rows.Close()
	health := SourceReadinessHealth{Report: SourceHealthReport{Ready: true, Items: make([]SourceStreamHealth, 0, 10)}}
	enabledTypes := map[domain.SourceType]bool{}
	for rows.Next() {
		var item SourceStreamHealth
		var ingest SourceIngestHealth
		if err = rows.Scan(&item.SourceInstanceID, &item.SourceType, &item.SourceName,
			&item.SourceEnabled, &item.StreamID, &item.Sequence,
			&item.ApprovedRuntimeVersion, &item.CutoverRuntimeVersion, &item.ObservedRuntimeVersion,
			&item.ObservedAgentVersion, &item.ProjectionStatus, &item.LastAcceptedAt,
			&item.LastNonemptyBatchAt, &item.EconomicWatermarkAt, &item.PendingEvents, &item.DeadEvents,
			&ingest.Pending, &ingest.Dead, &ingest.OldestPending, &item.ActiveRescanUpdatedAt); err != nil {
			return SourceReadinessHealth{}, err
		}
		if item.LastAcceptedAt.Equal(time.Unix(0, 0).UTC()) {
			item.LastAcceptedAt = time.Time{}
		}
		if item.LastNonemptyBatchAt.Equal(time.Unix(0, 0).UTC()) {
			item.LastNonemptyBatchAt = time.Time{}
		}
		if item.EconomicWatermarkAt.Equal(time.Unix(0, 0).UTC()) {
			item.EconomicWatermarkAt = time.Time{}
		}
		if ingest.OldestPending.Equal(time.Unix(0, 0).UTC()) {
			ingest.OldestPending = time.Time{}
		}
		if item.ActiveRescanUpdatedAt.Equal(time.Unix(0, 0).UTC()) {
			item.ActiveRescanUpdatedAt = time.Time{}
		}
		health.Ingest = ingest
		evaluateSourceStreamHealth(&item, policy)
		if item.SourceEnabled {
			enabledTypes[item.SourceType] = true
			if !item.Ready {
				health.Report.Ready = false
			}
		}
		health.Report.Items = append(health.Report.Items, item)
	}
	if err = rows.Err(); err != nil {
		return SourceReadinessHealth{}, err
	}
	if !enabledTypes[domain.SourceSub2API] || !enabledTypes[domain.SourceNewAPI] {
		health.Report.Ready = false
	}
	return health, nil
}

// SourceHealth returns one row for both mandatory streams of every configured
// source, including a never-provisioned stream. This makes cold-start and
// partial bootstrap failures visible instead of treating absence as healthy.
func (s *Store) SourceHealth(ctx context.Context, policy SourceFreshnessPolicy) (SourceHealthReport, error) {
	if !policy.enabled() {
		return SourceHealthReport{}, errors.New("source freshness policy is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT si.id,si.source_type,si.name,si.enabled,required.stream_id,
			COALESCE(sis.sequence,0),si.runtime_version,COALESCE((SELECT scm.source_runtime_version FROM source_cutover_manifests scm WHERE scm.source_instance_id=si.id ORDER BY scm.cutover_at DESC LIMIT 1),''),
			COALESCE(sis.source_runtime_version,''),COALESCE(sis.source_agent_version,''),COALESCE(sis.projection_status,'unknown'),
			COALESCE(sis.last_accepted_at,'epoch'::timestamptz),
			COALESCE(sis.last_nonempty_batch_at,'epoch'::timestamptz),
			COALESCE(sew.watermark_at,'epoch'::timestamptz),
			count(sie.event_id) FILTER (WHERE sie.processing_status IN ('queued','failed','processing')),
			count(sie.event_id) FILTER (WHERE sie.processing_status='dead'),
			count(sie.event_id) FILTER (WHERE sie.processing_status IN ('waiting_dependency','parked_identity')),
			COALESCE(max(sesc.updated_at),'epoch'::timestamptz)
		FROM source_instances si
		CROSS JOIN (VALUES ('payments'::text),('identities'::text),('usage'::text),('credits'::text),('balances'::text)) required(stream_id)
		LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id AND sis.stream_id=required.stream_id
		LEFT JOIN source_economic_stream_watermarks sew ON sew.source_instance_id=si.id AND sew.stream_kind=required.stream_id
		LEFT JOIN source_ingest_events sie ON sie.source_instance_id=si.id AND sie.stream_id=required.stream_id
		-- XM-INV-AGENT-RESTART-GRACE part A: at most one row can match (see
		-- source_economic_one_active_scan_cycle), so max() here is a no-op
		-- aggregate over that single value, not a real fan-out reduction.
		LEFT JOIN source_economic_scan_cycles sesc ON sesc.source_instance_id=si.id AND sesc.stream_id=required.stream_id
			AND sesc.cycle_status IN ('receiving','processing')
		GROUP BY si.id,si.source_type,si.name,si.enabled,required.stream_id,sis.sequence,
			si.runtime_version,sis.source_runtime_version,sis.source_agent_version,sis.projection_status,
			sis.last_accepted_at,sis.last_nonempty_batch_at,sew.watermark_at
		ORDER BY si.source_type,si.id,required.stream_id`)
	if err != nil {
		return SourceHealthReport{}, fmt.Errorf("query source stream health: %w", err)
	}
	defer rows.Close()
	report := SourceHealthReport{Ready: true, Items: make([]SourceStreamHealth, 0, 10)}
	enabledTypes := map[domain.SourceType]bool{}
	for rows.Next() {
		var item SourceStreamHealth
		if err = rows.Scan(&item.SourceInstanceID, &item.SourceType, &item.SourceName,
			&item.SourceEnabled, &item.StreamID, &item.Sequence,
			&item.ApprovedRuntimeVersion, &item.CutoverRuntimeVersion, &item.ObservedRuntimeVersion,
			&item.ObservedAgentVersion, &item.ProjectionStatus, &item.LastAcceptedAt,
			&item.LastNonemptyBatchAt, &item.EconomicWatermarkAt, &item.PendingEvents, &item.DeadEvents, &item.WaitingDependencies,
			&item.ActiveRescanUpdatedAt); err != nil {
			return SourceHealthReport{}, err
		}
		if item.LastAcceptedAt.Equal(time.Unix(0, 0).UTC()) {
			item.LastAcceptedAt = time.Time{}
		}
		if item.LastNonemptyBatchAt.Equal(time.Unix(0, 0).UTC()) {
			item.LastNonemptyBatchAt = time.Time{}
		}
		if item.EconomicWatermarkAt.Equal(time.Unix(0, 0).UTC()) {
			item.EconomicWatermarkAt = time.Time{}
		}
		if item.ActiveRescanUpdatedAt.Equal(time.Unix(0, 0).UTC()) {
			item.ActiveRescanUpdatedAt = time.Time{}
		}
		evaluateSourceStreamHealth(&item, policy)
		if item.SourceEnabled {
			enabledTypes[item.SourceType] = true
			if !item.Ready {
				report.Ready = false
			}
		}
		report.Items = append(report.Items, item)
	}
	if err = rows.Err(); err != nil {
		return SourceHealthReport{}, err
	}
	if !enabledTypes[domain.SourceSub2API] || !enabledTypes[domain.SourceNewAPI] {
		report.Ready = false
	}
	return report, nil
}

func assertSourceFreshTx(ctx context.Context, tx pgx.Tx, sourceInstanceID string, policy SourceFreshnessPolicy) error {
	if !policy.enabled() {
		return nil
	}
	for _, streamID := range []string{"identities", "payments", "usage", "credits", "balances"} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,5))`, sourceInstanceID+"\n"+streamID); err != nil {
			return err
		}
		var item SourceStreamHealth
		item.SourceInstanceID = sourceInstanceID
		item.StreamID = streamID
		err := tx.QueryRow(ctx, `
			SELECT si.source_type,si.name,si.enabled,COALESCE(sis.sequence,0),
				si.runtime_version,COALESCE((SELECT scm.source_runtime_version FROM source_cutover_manifests scm WHERE scm.source_instance_id=si.id ORDER BY scm.cutover_at DESC LIMIT 1),''),COALESCE(sis.source_runtime_version,''),
				COALESCE(sis.source_agent_version,''),COALESCE(sis.projection_status,'unknown'),
				COALESCE(sis.last_accepted_at,'epoch'::timestamptz),
				COALESCE(sis.last_nonempty_batch_at,'epoch'::timestamptz),
				COALESCE(sew.watermark_at,'epoch'::timestamptz),
				count(sie.event_id) FILTER (WHERE sie.processing_status IN ('queued','failed','processing')),
				count(sie.event_id) FILTER (WHERE sie.processing_status='dead'),
				count(sie.event_id) FILTER (WHERE sie.processing_status IN ('waiting_dependency','parked_identity'))
			FROM source_instances si
			LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id AND sis.stream_id=$2
			LEFT JOIN source_economic_stream_watermarks sew ON sew.source_instance_id=si.id AND sew.stream_kind=$2
			LEFT JOIN source_ingest_events sie ON sie.source_instance_id=si.id AND sie.stream_id=$2
			WHERE si.id=$1
			GROUP BY si.id,si.source_type,si.name,si.enabled,sis.sequence,si.runtime_version,
				sis.source_runtime_version,sis.source_agent_version,sis.projection_status,sis.last_accepted_at,
				sis.last_nonempty_batch_at,sew.watermark_at`, sourceInstanceID, streamID).Scan(
			&item.SourceType, &item.SourceName, &item.SourceEnabled, &item.Sequence,
			&item.ApprovedRuntimeVersion, &item.CutoverRuntimeVersion, &item.ObservedRuntimeVersion,
			&item.ObservedAgentVersion, &item.ProjectionStatus, &item.LastAcceptedAt,
			&item.LastNonemptyBatchAt, &item.EconomicWatermarkAt, &item.PendingEvents, &item.DeadEvents, &item.WaitingDependencies)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if item.LastAcceptedAt.Equal(time.Unix(0, 0).UTC()) {
			item.LastAcceptedAt = time.Time{}
		}
		if item.EconomicWatermarkAt.Equal(time.Unix(0, 0).UTC()) {
			item.EconomicWatermarkAt = time.Time{}
		}
		evaluateSourceStreamHealth(&item, policy)
		if !item.Ready {
			return domain.ErrSourceUnavailable
		}
	}
	return nil
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableSnapshotRowCount(snapshotID string, value int64) any {
	if snapshotID == "" {
		return nil
	}
	return value
}
