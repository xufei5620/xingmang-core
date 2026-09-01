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
	Now                     time.Time
}

func (p SourceFreshnessPolicy) enabled() bool {
	return p.EconomicHeartbeatMaxAge > 0 && p.EconomicWatermarkMaxAge > 0 && p.IdentitiesMaxAge > 0 && !p.Now.IsZero()
}

type SourceStreamHealth struct {
	SourceInstanceID                   string            `json:"source_instance_id"`
	SourceType                         domain.SourceType `json:"source_type"`
	SourceName                         string            `json:"source_name"`
	SourceEnabled                      bool              `json:"source_enabled"`
	StreamID                           string            `json:"stream_id"`
	Sequence                           int64             `json:"sequence"`
	ApprovedRuntimeVersion             string            `json:"approved_runtime_version"`
	ObservedRuntimeVersion             string            `json:"observed_runtime_version"`
	ObservedAgentVersion               string            `json:"observed_agent_version"`
	ProjectionStatus                   string            `json:"projection_status"`
	LastAcceptedAt                     time.Time         `json:"last_accepted_at"`
	LastNonemptyBatchAt                time.Time         `json:"last_nonempty_batch_at"`
	EconomicWatermarkAt                time.Time         `json:"economic_watermark_at,omitempty"`
	MaximumAgeSeconds                  int64             `json:"maximum_age_seconds"`
	EconomicWatermarkMaximumAgeSeconds int64             `json:"economic_watermark_maximum_age_seconds,omitempty"`
	PendingEvents                      int64             `json:"pending_events"`
	DeadEvents                         int64             `json:"dead_events"`
	WaitingDependencies                int64             `json:"waiting_dependencies"`
	Ready                              bool              `json:"ready"`
	Reasons                            []string          `json:"reasons"`
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

const sourceEventLease = 10 * time.Minute

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
		FROM source_ingest_events sie JOIN source_instances si ON si.id=sie.source_instance_id
		JOIN source_ingest_batches sib ON sib.source_instance_id=sie.source_instance_id
			AND sib.stream_id=sie.stream_id AND sib.batch_id=sie.first_batch_id
		WHERE (
			(sie.processing_status IN ('queued','failed') AND sie.attempt_count < 8 AND sie.next_attempt_at <= $1)
			OR (sie.processing_status='waiting_dependency' AND sie.next_attempt_at <= $1)
			OR (sie.processing_status='processing' AND sie.lease_expires_at <= $1)
		)
		ORDER BY sie.next_attempt_at,sie.created_at,sie.event_id
		FOR UPDATE OF sie SKIP LOCKED LIMIT $2`, now, limit)
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

func (s *Store) MarkSourceEventFailed(ctx context.Context, claim SourceEventClaim, errorCode string, next time.Time) error {
	errorCode = strings.TrimSpace(errorCode)
	if errorCode == "" || len(errorCode) > 512 || strings.ContainsAny(errorCode, "\r\n\x00") {
		return errors.New("invalid source event processing error code")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var newStatus string
	err = tx.QueryRow(ctx, `
		UPDATE source_ingest_events SET
			processing_status=CASE WHEN attempt_count>=8 THEN 'dead' ELSE 'failed' END,
			processing_error=$1,next_attempt_at=$2,lease_token=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE source_instance_id=$3 AND stream_id=$4 AND event_id=$5
			AND processing_status='processing' AND lease_token=$6
		RETURNING processing_status`, errorCode, next,
		claim.SourceInstanceID, claim.StreamID, claim.EventID, claim.LeaseToken).Scan(&newStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	if newStatus == "dead" {
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
			return lookupErr
		}
		if lookupErr == nil {
			if err = freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_DEAD", objectType, objectID,
				claim.PayloadHash, AuditActor{Type: "source_connector", ID: claim.SourceInstanceID,
					Reason: "dead event correlated to a persisted fact"}); err != nil {
				return err
			}
		}
	}
	if claim.SchemaVersion == "3.0" {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, claim.SourceInstanceID, claim.StreamID,
			AuditActor{Type: "source_connector", ID: claim.SourceInstanceID, Reason: "source event marked failed/dead"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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
		item.Reasons = append(item.Reasons, "ECONOMIC_WATERMARK_STALE")
	}
	if item.PendingEvents > 0 {
		item.Reasons = append(item.Reasons, "EVENTS_PENDING")
	}
	if item.DeadEvents > 0 {
		item.Reasons = append(item.Reasons, "EVENTS_DEAD")
	}
	item.Ready = len(item.Reasons) == 0
}

const sourceReadinessHealthQuery = `
	WITH active_event_health AS MATERIALIZED (
		SELECT source_instance_id,stream_id,
			count(*) FILTER (WHERE processing_status IN ('queued','failed','processing')) AS pending_events,
			count(*) FILTER (WHERE processing_status='dead') AS dead_events,
			COALESCE(min(created_at) FILTER (WHERE processing_status IN ('queued','failed','processing')),'epoch'::timestamptz) AS oldest_pending
		FROM source_ingest_events
		WHERE processing_status IN ('queued','failed','processing','dead')
		GROUP BY source_instance_id,stream_id
	), ingest_health AS (
		SELECT COALESCE(sum(pending_events),0)::bigint AS pending_events,
			COALESCE(sum(dead_events),0)::bigint AS dead_events,
			COALESCE(min(oldest_pending) FILTER (WHERE pending_events>0),'epoch'::timestamptz) AS oldest_pending
		FROM active_event_health
	)
	SELECT si.id,si.source_type,si.name,si.enabled,required.stream_id,
		COALESCE(sis.sequence,0),si.runtime_version,
		COALESCE(sis.source_runtime_version,''),COALESCE(sis.source_agent_version,''),COALESCE(sis.projection_status,'unknown'),
		COALESCE(sis.last_accepted_at,'epoch'::timestamptz),
		COALESCE(sis.last_nonempty_batch_at,'epoch'::timestamptz),
		COALESCE(sew.watermark_at,'epoch'::timestamptz),
		COALESCE(aeh.pending_events,0),COALESCE(aeh.dead_events,0),
		ingest.pending_events,ingest.dead_events,ingest.oldest_pending
	FROM source_instances si
	CROSS JOIN (VALUES ('payments'::text),('identities'::text),('usage'::text),('credits'::text),('balances'::text)) required(stream_id)
	LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id AND sis.stream_id=required.stream_id
	LEFT JOIN source_economic_stream_watermarks sew ON sew.source_instance_id=si.id AND sew.stream_kind=required.stream_id
	LEFT JOIN active_event_health aeh ON aeh.source_instance_id=si.id AND aeh.stream_id=required.stream_id
	CROSS JOIN ingest_health ingest
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
			&item.ApprovedRuntimeVersion, &item.ObservedRuntimeVersion,
			&item.ObservedAgentVersion, &item.ProjectionStatus, &item.LastAcceptedAt,
			&item.LastNonemptyBatchAt, &item.EconomicWatermarkAt, &item.PendingEvents, &item.DeadEvents,
			&ingest.Pending, &ingest.Dead, &ingest.OldestPending); err != nil {
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
			COALESCE(sis.sequence,0),si.runtime_version,
			COALESCE(sis.source_runtime_version,''),COALESCE(sis.source_agent_version,''),COALESCE(sis.projection_status,'unknown'),
			COALESCE(sis.last_accepted_at,'epoch'::timestamptz),
			COALESCE(sis.last_nonempty_batch_at,'epoch'::timestamptz),
			COALESCE(sew.watermark_at,'epoch'::timestamptz),
			count(sie.event_id) FILTER (WHERE sie.processing_status IN ('queued','failed','processing')),
			count(sie.event_id) FILTER (WHERE sie.processing_status='dead'),
			count(sie.event_id) FILTER (WHERE sie.processing_status IN ('waiting_dependency','parked_identity'))
		FROM source_instances si
		CROSS JOIN (VALUES ('payments'::text),('identities'::text),('usage'::text),('credits'::text),('balances'::text)) required(stream_id)
		LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id AND sis.stream_id=required.stream_id
		LEFT JOIN source_economic_stream_watermarks sew ON sew.source_instance_id=si.id AND sew.stream_kind=required.stream_id
		LEFT JOIN source_ingest_events sie ON sie.source_instance_id=si.id AND sie.stream_id=required.stream_id
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
			&item.ApprovedRuntimeVersion, &item.ObservedRuntimeVersion,
			&item.ObservedAgentVersion, &item.ProjectionStatus, &item.LastAcceptedAt,
			&item.LastNonemptyBatchAt, &item.EconomicWatermarkAt, &item.PendingEvents, &item.DeadEvents, &item.WaitingDependencies); err != nil {
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
				si.runtime_version,COALESCE(sis.source_runtime_version,''),
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
			&item.ApprovedRuntimeVersion, &item.ObservedRuntimeVersion,
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
