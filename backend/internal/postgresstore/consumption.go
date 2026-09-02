package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

const defaultEligibilityFinalizationDelay = 15 * time.Minute

// XM-INV-PROOF-CONTENTION 1: BALANCE_PROOF_PENDING retry schedule.
//
// ProcessEligibilityProjectionJobs's BALANCE_PROOF_PENDING requeue backs off
// exponentially per attempt (the job's own attempt_count, already
// incremented by the claim step above it): 30s, 60s, 120s, 240s, 480s, then
// capped at balanceProofPendingBackoffCapSeconds (10 minutes) from the 6th
// attempt on. Before this, every attempt retried at a flat 30s regardless of
// how many times it had already found the proof unprovable -- on the
// production account that motivated this slice, that meant 500+ retries
// each re-running the (now also fixed, see ensureBalanceCarryForwardProofTx)
// per-visibility proof loop under the per-account advisory lock every 30s
// for hours.
//
// A newly observed fact for the account (the two ON CONFLICT(external_account_id)
// DO UPDATE requeue upserts, in finalizeSourceAccountsTx and
// ObserveBalanceCheckpoint) still may mean the proof has become provable
// sooner than the backoff schedule would otherwise retry it -- a new
// balances cycle publishing is exactly the kind of fact that can unblock a
// pending proof. But unconditionally resetting next_attempt_at=now() on
// every such upsert (the pre-existing behavior) is what let the retry
// cadence collapse back to sub-second in practice: every fact for a
// contended account -- including ones unrelated to the proof itself, since
// facts keep streaming in while the proof stays pending -- re-armed the job
// immediately, defeating the backoff entirely and re-creating the same lock
// contention the backoff exists to bound. The requeue upserts below instead
// pull a deep backoff forward to "now" only when the job is currently
// BALANCE_PROOF_PENDING *and* still more than balanceProofPendingRequeueResetWindow
// away from its next attempt; once pulled forward, the job's own next
// failure re-derives next_attempt_at from its (unchanged, honestly
// incrementing) attempt_count, so a burst of facts within that window cannot
// keep re-arming it faster than once per window. A job that is not
// currently in a BALANCE_PROOF_PENDING backoff (queued for an unrelated
// reason, mid-lease, or freshly failed) is unaffected -- it keeps the
// pre-existing immediate-requeue behavior.
const (
	balanceProofPendingBackoffCapSeconds = 600
	balanceProofPendingRequeueResetWindow = 5 * time.Minute
)

var (
	serviceUnitsPattern                = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)
	unitCodePattern                    = regexp.MustCompile(`^[A-Z0-9_:-]{1,32}$`)
	errBalanceCarryForwardProofPending = errors.New("signed balance carry-forward proof is not published yet")
	errBalanceCarryForwardProofInvalid = errors.New("signed balance carry-forward proof contract is invalid")
)

type eligibilityAccount struct {
	ExternalAccountID string
	SourceInstanceID  string
	PrincipalID       string
	CutoverAt         time.Time
	GlobalCutoverAt   time.Time
	PolicyStartAt     time.Time
	PolicyVersion     int64
	UnitCode          string
	ManifestHash      string
	ConfigurationHash string
	BootstrapKind     string
	FinalizedThrough  time.Time
	Delay             time.Duration
	Status            string
	Version           int64
}

type eligibilityFact struct {
	Kind            string
	ID              string
	At              time.Time
	Units           *big.Int
	LotID           string
	CreditID        string
	UsageID         string
	PaidMinor       int64
	InvoiceEligible bool
	CausalDomain    string
	CausalOrder     *big.Int
	Revision        string
}

type projectedLot struct {
	ID            string
	PaidMinor     int64
	TotalUnits    *big.Int
	ConsumedUnits *big.Int
	RoundedMinor  int64
	Numerator     *big.Int
	Remainder     *big.Int
	OldMinor      int64
	OldUnits      *big.Int
	IssuedMinor   int64
	ReservedMinor int64
}

type projectedAllocation struct {
	UsageID, LotID, CreditID string
	Order                    int
	Units                    *big.Int
	CashMinorDelta           int64
}

type eligibilityProjection struct {
	Lots            map[string]*projectedLot
	Allocations     []projectedAllocation
	ExpectedBalance *big.Int
	AmbiguousAt     time.Time
	ShortfallUsage  string
}

type EligibilityProjectionHealth struct {
	Queued, Failed, Processing int64
	OldestPending              time.Time
}

func (s *Store) EligibilityProjectionHealth(ctx context.Context) (EligibilityProjectionHealth, error) {
	var health EligibilityProjectionHealth
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='queued'),count(*) FILTER (WHERE status='failed'),
			count(*) FILTER (WHERE status='processing'),
			COALESCE(min(created_at),'epoch'::timestamptz)
		FROM eligibility_projection_jobs`).Scan(&health.Queued, &health.Failed, &health.Processing,
		&health.OldestPending)
	if health.OldestPending.Equal(time.Unix(0, 0).UTC()) {
		health.OldestPending = time.Time{}
	}
	return health, err
}

func parseUnsignedUnits(value, field string, allowZero bool) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if !serviceUnitsPattern.MatchString(value) {
		return nil, fmt.Errorf("%s must be a canonical non-negative integer", field)
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || (!allowZero && result.Sign() == 0) {
		return nil, fmt.Errorf("%s must be positive", field)
	}
	return result, nil
}

func parseOptionalCausal(domainValue, orderValue string) (string, *big.Int, error) {
	domainValue = strings.TrimSpace(domainValue)
	orderValue = strings.TrimSpace(orderValue)
	if (domainValue == "") != (orderValue == "") {
		return "", nil, errors.New("causal domain and causal order must be supplied together")
	}
	if domainValue == "" {
		return "", nil, nil
	}
	if len(domainValue) > 128 || strings.ContainsAny(domainValue, "\x00\r\n") {
		return "", nil, errors.New("causal domain is invalid")
	}
	order, err := parseUnsignedUnits(orderValue, "causal order", true)
	if err != nil {
		return "", nil, err
	}
	return domainValue, order, nil
}

func validHash(value string) bool { return hexHashPattern.MatchString(strings.TrimSpace(value)) }

func validateFactMetadata(sourceID, externalUserID, eventID, revision, cursor, manifestHash, configurationHash, unitCode string, eventAt, observedAt, watermarkAt time.Time, sequence int64) error {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(externalUserID) == "" ||
		strings.TrimSpace(eventID) == "" || len(eventID) > 256 || sequence <= 0 ||
		strings.TrimSpace(cursor) == "" || len(cursor) > 256 || !validHash(revision) ||
		!validHash(manifestHash) || !validHash(configurationHash) || !unitCodePattern.MatchString(unitCode) {
		return errors.New("complete v3 source fact metadata is required")
	}
	if eventAt.IsZero() || observedAt.IsZero() || watermarkAt.IsZero() || eventAt.After(watermarkAt) ||
		watermarkAt.After(observedAt.Add(5*time.Minute)) {
		return errors.New("source fact event time/watermark is invalid")
	}
	return nil
}

func (s *Store) RegisterCutoverManifest(ctx context.Context, manifest CutoverManifest, actor AuditActor) error {
	if strings.TrimSpace(manifest.SourceInstanceID) == "" || !validHash(manifest.ManifestHash) ||
		!validHash(manifest.ConfigurationHash) || !unitCodePattern.MatchString(manifest.UnitCode) ||
		manifest.CutoverAt.IsZero() || manifest.DatabaseClock.IsZero() ||
		manifest.CutoverAt.After(manifest.DatabaseClock.Add(5*time.Minute)) ||
		strings.TrimSpace(manifest.SourceRuntimeVersion) == "" || len(manifest.SourceRuntimeVersion) > 128 ||
		strings.TrimSpace(manifest.ProjectionContract) == "" || len(manifest.ProjectionContract) > 128 ||
		strings.TrimSpace(manifest.SigningKeyID) == "" || len(manifest.SigningKeyID) > 128 ||
		!validHash(manifest.BaselineSnapshotID) || !validHash(manifest.BaselineSnapshotHash) ||
		manifest.BaselineSnapshotID != manifest.BaselineSnapshotHash ||
		manifest.BaselineRowCount < 0 || manifest.BaselineRowCount > 2_000_000 {
		return errors.New("complete signed cutover manifest is required")
	}
	actor = actor.normalized()
	if actor.Type != "source_connector" || actor.ID != manifest.SourceInstanceID {
		return domain.ErrForbidden
	}
	ceilings := []string{manifest.PaymentsCeiling, manifest.UsageCeiling, manifest.CreditsCeiling, manifest.BalancesCeiling}
	for _, ceiling := range ceilings {
		if strings.TrimSpace(ceiling) == "" || len(ceiling) > 256 {
			return errors.New("all cutover stream ceilings are required")
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,41))`, manifest.SourceInstanceID); err != nil {
		return err
	}
	// The source-scoped advisory lock serializes create/idempotence checks.
	// source_cutover_manifests is append-only, so a row lock would add no
	// protection and would incorrectly require UPDATE on the hardened runtime.
	var policyStart time.Time
	if err = tx.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		return err
	}
	if !manifest.CutoverAt.UTC().Before(policyStart.UTC()) ||
		!manifest.DatabaseClock.UTC().Before(policyStart.UTC()) {
		return domain.ErrConflict
	}
	trustedSigningKey, err := verifyFactBatchContextTx(ctx, tx, manifest.SourceInstanceID, "balances",
		manifest.ExternalEventID, manifest.BatchID, manifest.ScanCycleID, manifest.SourceRevision,
		manifest.StreamWatermarkAt)
	if err != nil {
		return err
	}
	// The payload field is informational only. The persisted key comes from the
	// already authenticated mTLS/Ed25519 batch row.
	manifest.SigningKeyID = trustedSigningKey
	var existing CutoverManifest
	err = tx.QueryRow(ctx, `
		SELECT source_instance_id,manifest_hash,source_runtime_version,projection_contract,configuration_hash,unit_code,
			payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,
			baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,
			signing_key_id,cutover_at,database_clock
		FROM source_cutover_manifests WHERE source_instance_id=$1`, manifest.SourceInstanceID).Scan(
		&existing.SourceInstanceID, &existing.ManifestHash, &existing.SourceRuntimeVersion, &existing.ProjectionContract, &existing.ConfigurationHash,
		&existing.UnitCode, &existing.PaymentsCeiling, &existing.UsageCeiling, &existing.CreditsCeiling,
		&existing.BalancesCeiling, &existing.BaselineSnapshotID, &existing.BaselineSnapshotHash,
		&existing.BaselineRowCount, &existing.SigningKeyID, &existing.CutoverAt, &existing.DatabaseClock)
	if err == nil {
		if !sameCutoverManifest(existing, manifest) {
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var configuredRuntime string
	var sourceType domain.SourceType
	if err = tx.QueryRow(ctx, `SELECT runtime_version,source_type FROM source_instances WHERE id=$1 AND enabled FOR SHARE`, manifest.SourceInstanceID).Scan(&configuredRuntime, &sourceType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrForbidden
		}
		return err
	}
	expectedContract := "sub2api-economic-v4"
	if sourceType == domain.SourceNewAPI {
		expectedContract = "newapi-economic-rc25-v4"
	}
	if configuredRuntime != manifest.SourceRuntimeVersion || manifest.UnitCode != expectedUnitForSource(sourceType) ||
		manifest.ProjectionContract != expectedContract {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_cutover_manifests(
			source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,projection_contract,
			configuration_hash,unit_code,payments_ceiling,usage_ceiling,credits_ceiling,
			balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, manifest.SourceInstanceID,
		manifest.ManifestHash, manifest.CutoverAt.UTC(), manifest.DatabaseClock.UTC(), manifest.SourceRuntimeVersion,
		manifest.ProjectionContract, manifest.ConfigurationHash, manifest.UnitCode, manifest.PaymentsCeiling, manifest.UsageCeiling,
		manifest.CreditsCeiling, manifest.BalancesCeiling, manifest.BaselineSnapshotID,
		manifest.BaselineSnapshotHash, manifest.BaselineRowCount, manifest.SigningKeyID)
	if err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.cutover_manifest.accepted", "source_instance", manifest.SourceInstanceID,
		nil, map[string]any{"manifest_hash": manifest.ManifestHash, "cutover_at": manifest.CutoverAt,
			"configuration_hash": manifest.ConfigurationHash}); err != nil {
		return err
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, manifest.SourceInstanceID, stream, actor); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) CutoverManifestExists(ctx context.Context, sourceID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_cutover_manifests WHERE source_instance_id=$1)`, sourceID).Scan(&exists)
	return exists, err
}

func sameCutoverManifest(a, b CutoverManifest) bool {
	return a.SourceInstanceID == b.SourceInstanceID && a.ManifestHash == b.ManifestHash &&
		a.SourceRuntimeVersion == b.SourceRuntimeVersion && a.ProjectionContract == b.ProjectionContract &&
		a.ConfigurationHash == b.ConfigurationHash && a.UnitCode == b.UnitCode &&
		a.PaymentsCeiling == b.PaymentsCeiling && a.UsageCeiling == b.UsageCeiling &&
		a.CreditsCeiling == b.CreditsCeiling && a.BalancesCeiling == b.BalancesCeiling &&
		a.BaselineSnapshotID == b.BaselineSnapshotID && a.BaselineSnapshotHash == b.BaselineSnapshotHash &&
		a.BaselineRowCount == b.BaselineRowCount && a.SigningKeyID == b.SigningKeyID &&
		a.CutoverAt.Equal(b.CutoverAt) && a.DatabaseClock.Equal(b.DatabaseClock)
}

func validEconomicStream(value string) bool {
	return value == "payments" || value == "usage" || value == "credits" || value == "balances"
}

// AdvanceSourceEconomicWatermark consumes signed batch completeness metadata.
// It is intentionally source-scoped: an empty batch advances inactive users as
// well as users represented by rows in that batch.
func (s *Store) AdvanceSourceEconomicWatermark(ctx context.Context, sourceID, manifestHash, configurationHash string, watermark EligibilityWatermark, actor AuditActor) error {
	if strings.TrimSpace(sourceID) == "" || !validHash(manifestHash) || !validHash(configurationHash) ||
		!validEconomicStream(watermark.StreamKind) || watermark.WatermarkAt.IsZero() ||
		watermark.SourceSequence < 0 || strings.TrimSpace(watermark.SourceCursor) == "" || len(watermark.SourceCursor) > 256 {
		return errors.New("invalid source economic watermark")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, sourceID); err != nil {
		return err
	}
	var trustedManifest, trustedConfig string
	if err = tx.QueryRow(ctx, `SELECT manifest_hash,configuration_hash FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&trustedManifest, &trustedConfig); err != nil {
		return err
	}
	if trustedManifest != manifestHash || trustedConfig != configurationHash {
		return domain.ErrConflict
	}
	var oldAt time.Time
	var oldSequence int64
	var oldCursor string
	err = tx.QueryRow(ctx, `
		SELECT watermark_at,source_sequence,source_cursor
		FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind=$2 FOR UPDATE`, sourceID, watermark.StreamKind).Scan(&oldAt, &oldSequence, &oldCursor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && (watermark.WatermarkAt.Before(oldAt) || watermark.SourceSequence < oldSequence) {
		if freezeErr := freezeSourceAccountsTx(ctx, tx, sourceID, "STREAM_WATERMARK_REGRESSION", "source_watermark", watermark.StreamKind,
			"", actor); freezeErr != nil {
			return freezeErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return domain.ErrConflict
	}
	if err == nil && watermark.WatermarkAt.Equal(oldAt) && watermark.SourceSequence == oldSequence {
		if oldCursor != watermark.SourceCursor {
			if freezeErr := freezeSourceAccountsTx(ctx, tx, sourceID, "EVENT_PAYLOAD_DRIFT", "source_watermark", watermark.StreamKind, "", actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_economic_stream_watermarks(
			source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET
			watermark_at=EXCLUDED.watermark_at,source_sequence=EXCLUDED.source_sequence,
			source_cursor=EXCLUDED.source_cursor,configuration_hash=EXCLUDED.configuration_hash,updated_at=now()`,
		sourceID, watermark.StreamKind, watermark.WatermarkAt.UTC(), watermark.SourceSequence,
		watermark.SourceCursor, configurationHash)
	if err != nil {
		return err
	}
	if err = finalizeSourceAccountsTx(ctx, tx, sourceID, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func finalizeSourceAccountsTx(ctx context.Context, tx pgx.Tx, sourceID string, _ AuditActor) error {
	var minWatermark time.Time
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT min(watermark_at),count(*) FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1`, sourceID).Scan(&minWatermark, &count); err != nil {
		return err
	}
	if count != 4 {
		return nil
	}
	// Enqueue only accounts with facts in the newly finalized interval. This is
	// a single set-based operation; ingestion never loops/replays thousands of
	// accounts while holding the signed batch transaction open.
	_, err := tx.Exec(ctx, `
		WITH targets AS (
			SELECT eas.external_account_id,eas.finalized_through,
				GREATEST(eas.cutover_at,$2::timestamptz-
					make_interval(secs=>eas.finalization_delay_seconds)) AS requested_through
			FROM source_account_eligibility_state eas
			WHERE eas.source_instance_id=$1 AND eas.catchup_key_hmac IS NULL
				AND eas.eligibility_status<>'syncing'
		), changed AS (
			SELECT t.* FROM targets t WHERE t.requested_through>t.finalized_through AND (
				EXISTS (SELECT 1 FROM source_usage_events e WHERE e.external_account_id=t.external_account_id
					AND e.event_time>t.finalized_through AND e.event_time<=t.requested_through)
				OR EXISTS (SELECT 1 FROM source_credit_events e WHERE e.external_account_id=t.external_account_id
					AND e.event_time>t.finalized_through AND e.event_time<=t.requested_through)
				OR EXISTS (SELECT 1 FROM funding_lots fl WHERE fl.external_account_id=t.external_account_id
					AND fl.completed_at>t.finalized_through AND fl.completed_at<=t.requested_through
					AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH'))
				OR EXISTS (SELECT 1 FROM balance_reconciliation_checkpoints b
					WHERE b.external_account_id=t.external_account_id
						AND b.as_of>t.finalized_through AND b.as_of<=t.requested_through)
			)
		)
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		SELECT external_account_id,requested_through,'queued',now() FROM changed
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			status='queued',lease_token=NULL,lease_expires_at=NULL,
			next_attempt_at=CASE
				WHEN eligibility_projection_jobs.status='queued'
					AND eligibility_projection_jobs.last_error_code='BALANCE_PROOF_PENDING'
					AND eligibility_projection_jobs.next_attempt_at<=now()+make_interval(secs=>$3)
				THEN eligibility_projection_jobs.next_attempt_at
				ELSE now()
			END,
			updated_at=now()`,
		sourceID, minWatermark, balanceProofPendingRequeueResetWindow.Seconds())
	if err != nil {
		return err
	}
	// Accounts with no facts in the interval are safely advanced in one UPDATE;
	// this covers users with zero events in one or all streams after an empty
	// signed scan cycle publishes its source-level watermark.
	_, err = tx.Exec(ctx, `
		WITH targets AS (
			SELECT eas.external_account_id,
				GREATEST(eas.cutover_at,$2::timestamptz-
					make_interval(secs=>eas.finalization_delay_seconds)) AS requested_through
			FROM source_account_eligibility_state eas
			WHERE eas.source_instance_id=$1 AND eas.catchup_key_hmac IS NULL
				AND eas.eligibility_status<>'syncing'
		)
		UPDATE source_account_eligibility_state eas
		SET finalized_through=t.requested_through,projection_version=eas.projection_version+1,updated_at=now()
		FROM targets t
		WHERE eas.external_account_id=t.external_account_id
			AND t.requested_through>eas.finalized_through
			AND NOT EXISTS (SELECT 1 FROM eligibility_projection_jobs j
				WHERE j.external_account_id=eas.external_account_id)`, sourceID, minWatermark)
	return err
}

func freezeSourceAccountsTx(ctx context.Context, tx pgx.Tx, sourceID, reason, objectType, objectID, revision string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `SELECT external_account_id FROM source_account_eligibility_state WHERE source_instance_id=$1 ORDER BY external_account_id FOR UPDATE`, sourceID)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = freezeEligibilityTx(ctx, tx, id, "", reason, objectType, objectID, revision, actor); err != nil {
			return err
		}
	}
	return nil
}

func tryPublishEconomicScanCyclesTx(ctx context.Context, tx pgx.Tx, sourceID, streamID string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT scan_cycle_id::text,stream_watermark_at,source_cursor,final_sequence,
			COALESCE(scan_snapshot_id,''),COALESCE(scan_snapshot_row_count,-1)
		FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id=$2 AND cycle_status='processing'
			AND final_sequence IS NOT NULL
		ORDER BY first_sequence FOR UPDATE`, sourceID, streamID)
	if err != nil {
		return err
	}
	type cycle struct {
		id, cursor, snapshotID string
		watermark              time.Time
		sequence, snapshotRows int64
	}
	cycles := make([]cycle, 0)
	for rows.Next() {
		var item cycle
		if err = rows.Scan(&item.id, &item.watermark, &item.cursor, &item.sequence,
			&item.snapshotID, &item.snapshotRows); err != nil {
			rows.Close()
			return err
		}
		cycles = append(cycles, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range cycles {
		var incomplete, manifestRecords, checkpointRecords int64
		// XM-INV-POLICY-ANCHOR 2.5: a failed/dead event no longer holds the
		// cycle once its account has already been frozen over it -- the
		// processor freezes before returning a plain (non-retryable) error on
		// payload drift/conflict, so by the time the event reaches
		// failed/dead an open freeze already exists. There is no ingest-layer
		// column identifying which account/fact a frozen, encrypted event
		// belongs to, so the correlation goes through source_revision_hash,
		// which every freeze call site sets to the triggering fact's revision
		// hash -- the same value stored as the ingest event's payload_hash.
		// A failed/dead event with no matching freeze (pure transient
		// exhaustion, retry budget not proven unrecoverable) still holds the
		// cycle.
		err = tx.QueryRow(ctx, `
			SELECT count(*) FILTER (
					WHERE sie.processing_status NOT IN ('processed','parked_identity')
						AND NOT (sie.processing_status IN ('failed','dead') AND ef.id IS NOT NULL)
				),
				count(*) FILTER (WHERE sie.entity_type='cutover_manifest'),
				count(*) FILTER (WHERE sie.entity_type='balance_checkpoint')
			FROM source_economic_scan_cycle_events m
			JOIN source_ingest_events sie ON sie.source_instance_id=m.source_instance_id
				AND sie.stream_id=m.stream_id AND sie.event_id=m.event_id
			LEFT JOIN eligibility_freezes ef ON ef.source_revision_hash=sie.payload_hash AND ef.status='open'
			WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.scan_cycle_id=$3::uuid`,
			sourceID, streamID, item.id).Scan(&incomplete, &manifestRecords, &checkpointRecords)
		if err != nil {
			return err
		}
		if incomplete != 0 {
			continue
		}
		var configHash string
		var baselineRows int64
		err = tx.QueryRow(ctx, `
			SELECT configuration_hash,baseline_row_count FROM source_cutover_manifests
			WHERE source_instance_id=$1`, sourceID).Scan(&configHash, &baselineRows)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		invalidBalanceSnapshot := streamID == "balances" &&
			(item.snapshotRows < 0 || checkpointRecords != item.snapshotRows ||
				(manifestRecords > 0 && (manifestRecords != 1 || checkpointRecords != baselineRows)))
		if invalidBalanceSnapshot {
			if _, err = tx.Exec(ctx, `UPDATE source_economic_scan_cycles SET cycle_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`, sourceID, streamID, item.id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE source_ingest_state SET projection_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, streamID); err != nil {
				return err
			}
			if err = freezeSourceAccountsTx(ctx, tx, sourceID, "SOURCE_GAP", "balance_snapshot_cycle",
				item.id, "", actor); err != nil {
				return err
			}
			continue
		}
		var oldAt time.Time
		var oldSequence int64
		var oldCursor string
		err = tx.QueryRow(ctx, `
			SELECT watermark_at,source_sequence,source_cursor
			FROM source_economic_stream_watermarks
			WHERE source_instance_id=$1 AND stream_kind=$2 FOR UPDATE`, sourceID, streamID).Scan(
			&oldAt, &oldSequence, &oldCursor)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		regressed := err == nil && (item.watermark.Before(oldAt) || item.sequence < oldSequence ||
			(item.watermark.Equal(oldAt) && item.sequence == oldSequence && item.cursor != oldCursor))
		if regressed {
			if _, err = tx.Exec(ctx, `UPDATE source_economic_scan_cycles SET cycle_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`, sourceID, streamID, item.id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE source_ingest_state SET projection_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, streamID); err != nil {
				return err
			}
			if err = freezeSourceAccountsTx(ctx, tx, sourceID, "STREAM_WATERMARK_REGRESSION",
				"source_scan_cycle", item.id, "", actor); err != nil {
				return err
			}
			continue
		}
		command, err := tx.Exec(ctx, `
			INSERT INTO source_economic_stream_watermarks(
				source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET
				watermark_at=EXCLUDED.watermark_at,source_sequence=EXCLUDED.source_sequence,
				source_cursor=EXCLUDED.source_cursor,configuration_hash=EXCLUDED.configuration_hash,updated_at=now()`,
			sourceID, streamID, item.watermark, item.sequence, item.cursor, configHash)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE source_economic_scan_cycles SET cycle_status='published',published_at=now(),updated_at=now()
			WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid
				AND cycle_status='processing'`, sourceID, streamID, item.id)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		if err = finalizeSourceAccountsTx(ctx, tx, sourceID, actor); err != nil {
			return err
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.source_watermark.published", "source_scan_cycle",
			sourceID+"/"+streamID+"/"+item.id, nil, map[string]any{"watermark_at": item.watermark,
				"source_sequence": item.sequence}); err != nil {
			return err
		}
	}
	return nil
}

func completeEligibilityCatchupTx(ctx context.Context, tx pgx.Tx, sourceID, catchupKey string, actor AuditActor) error {
	if !dependencyHMACPattern.MatchString(catchupKey) {
		return domain.ErrConflict
	}
	var remaining int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM source_ingest_events
		WHERE source_instance_id=$1 AND catchup_key_hmac=$2 AND processing_status<>'processed'`,
		sourceID, catchupKey).Scan(&remaining); err != nil {
		return err
	}
	if remaining != 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT external_account_id FROM source_account_eligibility_state
		WHERE source_instance_id=$1 AND catchup_key_hmac=$2 AND eligibility_status='syncing'
		ORDER BY external_account_id FOR UPDATE`, sourceID, catchupKey)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		var openFreezes int64
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes WHERE external_account_id=$1 AND status='open'`, id).Scan(&openFreezes); err != nil {
			return err
		}
		status := "active"
		if openFreezes > 0 {
			status = "frozen"
		}
		if _, err = tx.Exec(ctx, `
			UPDATE source_account_eligibility_state SET eligibility_status=$1,catchup_key_hmac=NULL,
				projection_version=projection_version+1,updated_at=now()
			WHERE external_account_id=$2`, status, id); err != nil {
			return err
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.identity_catchup.completed", "external_account", id,
			nil, map[string]any{"status": status}); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		return finalizeSourceAccountsTx(ctx, tx, sourceID, actor)
	}
	return nil
}

func expectedUnitForSource(sourceType domain.SourceType) string {
	if sourceType == domain.SourceSub2API {
		return "SUB2_BALANCE_1E8"
	}
	if sourceType == domain.SourceNewAPI {
		return "NEWAPI_QUOTA"
	}
	return ""
}

func resolveExternalAccountTx(ctx context.Context, tx pgx.Tx, sourceID, externalUserID string) (string, string, domain.SourceType, error) {
	var accountID, principalID string
	var sourceType domain.SourceType
	err := tx.QueryRow(ctx, `
		SELECT ea.id,ea.invoice_user_id,si.source_type
		FROM external_accounts ea JOIN source_instances si ON si.id=ea.source_instance_id
		WHERE ea.source_instance_id=$1 AND ea.external_user_id=$2
			AND ea.binding_status='verified' AND si.enabled
		FOR SHARE OF ea,si`, sourceID, externalUserID).Scan(&accountID, &principalID, &sourceType)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", domain.ErrNotFound
	}
	return accountID, principalID, sourceType, err
}

func getEligibilityAccountTx(ctx context.Context, tx pgx.Tx, accountID string, lock bool) (eligibilityAccount, error) {
	clause := ""
	if lock {
		clause = " FOR UPDATE OF eas"
	}
	var account eligibilityAccount
	var delaySeconds int
	err := tx.QueryRow(ctx, `
		SELECT eas.external_account_id,eas.source_instance_id,ea.invoice_user_id,eas.cutover_at,scm.cutover_at,
			policy.eligibility_start_at,policy.policy_version,
			eas.unit_code,eas.cutover_manifest_hash,scm.configuration_hash,eas.bootstrap_kind,eas.finalized_through,
			eas.finalization_delay_seconds,eas.eligibility_status,eas.projection_version
		FROM source_account_eligibility_state eas
		JOIN external_accounts ea ON ea.id=eas.external_account_id
		JOIN source_cutover_manifests scm ON scm.source_instance_id=eas.source_instance_id
		CROSS JOIN invoice_eligibility_policy policy
		WHERE eas.external_account_id=$1`+clause, accountID).Scan(
		&account.ExternalAccountID, &account.SourceInstanceID, &account.PrincipalID, &account.CutoverAt, &account.GlobalCutoverAt,
		&account.PolicyStartAt, &account.PolicyVersion,
		&account.UnitCode, &account.ManifestHash, &account.ConfigurationHash, &account.BootstrapKind, &account.FinalizedThrough,
		&delaySeconds, &account.Status, &account.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return eligibilityAccount{}, domain.ErrSourceUnavailable
	}
	account.Delay = time.Duration(delaySeconds) * time.Second
	return account, err
}

func verifyFactTrustTx(_ context.Context, _ pgx.Tx, account eligibilityAccount, manifestHash, configurationHash, unitCode, stream string, factWatermark time.Time) error {
	if account.ManifestHash != manifestHash || account.ConfigurationHash != configurationHash || account.UnitCode != unitCode {
		return domain.ErrConflict
	}
	// factWatermark is injected from the verified batch context. It may be
	// ahead of the published completeness watermark while records in that scan
	// cycle are still processing. Publishing happens only after the final page
	// and every record in the cycle has reached processed/idempotent state.
	if !validEconomicStream(stream) || factWatermark.IsZero() {
		return domain.ErrSourceUnavailable
	}
	return nil
}

func verifyFactBatchContextTx(ctx context.Context, tx pgx.Tx, sourceID, streamID, eventID,
	batchID, scanCycleID, revision string, watermarkAt time.Time) (string, error) {
	if !validEconomicStream(streamID) || strings.TrimSpace(eventID) == "" ||
		strings.TrimSpace(batchID) == "" || strings.TrimSpace(scanCycleID) == "" ||
		!validHash(revision) || watermarkAt.IsZero() {
		return "", domain.ErrForbidden
	}
	var storedHash, signingKeyID, cycleStatus string
	var storedWatermark time.Time
	// Lock only the mutable scan-cycle row. source_ingest_batches and the
	// cycle-event binding are append-only; locking b would require UPDATE on an
	// intentionally immutable batch table and break the least-privilege runtime
	// role without adding any consistency guarantee.
	err := tx.QueryRow(ctx, `
		SELECT m.payload_hash,b.signing_key_id,b.scan_ceiling_at,c.cycle_status
		FROM source_economic_scan_cycle_events m
		JOIN source_ingest_batches b ON b.source_instance_id=m.source_instance_id
			AND b.stream_id=m.stream_id AND b.batch_id=m.batch_id
		JOIN source_economic_scan_cycles c ON c.source_instance_id=m.source_instance_id
			AND c.stream_id=m.stream_id AND c.scan_cycle_id=m.scan_cycle_id
		WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.event_id=$3::uuid
			AND m.batch_id=$4::uuid AND m.scan_cycle_id=$5::uuid
			AND b.schema_version='3.0' FOR SHARE OF c`, sourceID, streamID, eventID,
		batchID, scanCycleID).Scan(&storedHash, &signingKeyID, &storedWatermark, &cycleStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if storedHash != revision || !storedWatermark.Equal(watermarkAt) ||
		(cycleStatus != "receiving" && cycleStatus != "processing" && cycleStatus != "published") {
		return "", domain.ErrConflict
	}
	return signingKeyID, nil
}

func (s *Store) ValidateEconomicFactContext(ctx context.Context, sourceID, streamID, eventID,
	batchID, scanCycleID, revision string, watermarkAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = verifyFactBatchContextTx(ctx, tx, sourceID, streamID, eventID, batchID,
		scanCycleID, revision, watermarkAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ObserveBalanceCheckpoint(ctx context.Context, in BalanceCheckpointObservation, actor AuditActor) error {
	if err := validateFactMetadata(in.SourceInstanceID, in.ExternalUserID, in.ExternalEventID,
		in.SourceRevision, in.SourceCursor, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, in.AsOf, in.ObservedAt, in.StreamWatermarkAt, in.SourceSequence); err != nil {
		return err
	}
	if strings.TrimSpace(in.CheckpointID) == "" || len(in.CheckpointID) > 256 ||
		(in.CheckpointKind != "cutover" && in.CheckpointKind != "reconciliation") {
		return errors.New("invalid balance checkpoint")
	}
	balance, err := parseUnsignedUnits(in.BalanceServiceUnits, "balance service units", true)
	if err != nil {
		return err
	}
	if in.BalanceNegative && balance.Sign() != 0 {
		return errors.New("negative balance checkpoint must carry zero service units")
	}
	snapshotRows, err := parseUnsignedUnits(in.SnapshotRowCount, "snapshot row count", true)
	if err != nil || !snapshotRows.IsInt64() || snapshotRows.Int64() > 2_000_000 || !validHash(in.SourceSnapshotID) {
		return errors.New("invalid balance snapshot row count")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	accountID, principalID, sourceType, err := resolveExternalAccountTx(ctx, tx, in.SourceInstanceID, in.ExternalUserID)
	if err != nil {
		return err
	}
	if in.UnitCode != expectedUnitForSource(sourceType) {
		return domain.ErrConflict
	}
	// XM-INV-PROOF-CONTENTION 2: try, don't block. processEligibilityProjectionJob
	// holds this same per-account lock for its write phase; a source-projection
	// worker call that instead waited here risked a lock-timeout error deep
	// inside its own transaction, and one such failure used to abort the
	// worker's whole batch (see RunOnce, application/source_processor.go).
	// A busy account is routine, expected contention -- the caller reschedules
	// this claim shortly (MarkSourceEventBusy) without spending its retry budget.
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,43))`, accountID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return domain.ErrAccountLockBusy
	}
	if _, err = verifyFactBatchContextTx(ctx, tx, in.SourceInstanceID, "balances", in.ExternalEventID,
		in.BatchID, in.ScanCycleID, in.SourceRevision, in.StreamWatermarkAt); err != nil {
		return err
	}
	var cycleSnapshotID string
	var cycleSnapshotRows int64
	if err = tx.QueryRow(ctx, `SELECT scan_snapshot_id,scan_snapshot_row_count
		FROM source_economic_scan_cycles WHERE source_instance_id=$1 AND stream_id='balances'
			AND scan_cycle_id=$2::uuid FOR SHARE`, in.SourceInstanceID, in.ScanCycleID).Scan(
		&cycleSnapshotID, &cycleSnapshotRows); err != nil {
		return err
	}
	if cycleSnapshotID != in.SourceSnapshotID || cycleSnapshotRows != snapshotRows.Int64() {
		return domain.ErrConflict
	}
	var carryProofKey string
	err = tx.QueryRow(ctx, `SELECT proof_key FROM balance_carry_forward_proofs
		WHERE external_account_id=$1 AND scan_cycle_id=$2::uuid`, accountID, in.ScanCycleID).Scan(&carryProofKey)
	if err == nil {
		if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_PAYLOAD_DRIFT",
			"balance_carry_forward_proof", carryProofKey, in.SourceRevision, actor); freezeErr != nil {
			return freezeErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return domain.ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var manifest CutoverManifest
	err = tx.QueryRow(ctx, `
		SELECT source_instance_id,manifest_hash,source_runtime_version,projection_contract,configuration_hash,unit_code,
			payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,
			baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,
			signing_key_id,cutover_at,database_clock
		FROM source_cutover_manifests WHERE source_instance_id=$1`, in.SourceInstanceID).Scan(
		&manifest.SourceInstanceID, &manifest.ManifestHash, &manifest.SourceRuntimeVersion, &manifest.ProjectionContract, &manifest.ConfigurationHash,
		&manifest.UnitCode, &manifest.PaymentsCeiling, &manifest.UsageCeiling, &manifest.CreditsCeiling,
		&manifest.BalancesCeiling, &manifest.BaselineSnapshotID, &manifest.BaselineSnapshotHash,
		&manifest.BaselineRowCount, &manifest.SigningKeyID, &manifest.CutoverAt, &manifest.DatabaseClock)
	if err != nil {
		return domain.ErrSourceUnavailable
	}
	if manifest.ManifestHash != in.CutoverManifestHash || manifest.ConfigurationHash != in.ConfigurationHash ||
		manifest.UnitCode != in.UnitCode {
		return domain.ErrConflict
	}
	var existingHash string
	err = tx.QueryRow(ctx, `
		SELECT source_revision_hash FROM balance_reconciliation_checkpoints
		WHERE source_instance_id=$1 AND (external_event_id=$2 OR checkpoint_id=$3)`,
		in.SourceInstanceID, in.ExternalEventID, in.CheckpointID).Scan(&existingHash)
	if err == nil {
		if existingHash != in.SourceRevision {
			if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_PAYLOAD_DRIFT", "balance_checkpoint", in.CheckpointID, in.SourceRevision, actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if in.CheckpointKind == "cutover" {
		if !in.BaselineMember || !in.AsOf.Equal(manifest.CutoverAt) || !validHash(in.BaselineSnapshotID) ||
			in.BaselineSnapshotID != manifest.BaselineSnapshotHash || in.SourceSnapshotID != manifest.BaselineSnapshotHash ||
			snapshotRows.Int64() != manifest.BaselineRowCount {
			return domain.ErrConflict
		}
		if _, stateErr := getEligibilityAccountTx(ctx, tx, accountID, true); !errors.Is(stateErr, domain.ErrSourceUnavailable) {
			if stateErr != nil {
				return stateErr
			}
			return domain.ErrConflict
		}
		eligibilityStatus := "active"
		if in.CatchupKeyHMAC != "" {
			if !dependencyHMACPattern.MatchString(in.CatchupKeyHMAC) {
				return errors.New("invalid catch-up dependency key")
			}
			eligibilityStatus = "syncing"
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO source_account_eligibility_state(
				external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
				cutover_manifest_hash,finalized_through,finalization_delay_seconds,catchup_key_hmac,eligibility_status)
			VALUES($1,$2,$3,$4,$5::numeric,$6,$3,$7,NULLIF($8,''),$9)`, accountID, in.SourceInstanceID,
			manifest.CutoverAt.UTC(), in.UnitCode, balance.String(), in.CutoverManifestHash,
			int(defaultEligibilityFinalizationDelay/time.Second), in.CatchupKeyHMAC, eligibilityStatus)
		if err != nil {
			return err
		}
		checkpointID := randomUUID()
		_, err = tx.Exec(ctx, `
			INSERT INTO balance_reconciliation_checkpoints(
				id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
				checkpoint_kind,baseline_snapshot_id,baseline_member,source_snapshot_id,snapshot_row_count,
				as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,
				configuration_hash,reconciliation_status,source_sequence,source_cursor,
				stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,'cutover',$6,TRUE,$7,$8,$9,$10::numeric,$11,$12,$13,$14,
				'cutover_baseline',$15,$16,$17,$18,$19)`, checkpointID, in.SourceInstanceID,
			accountID, in.ExternalEventID, in.CheckpointID, in.BaselineSnapshotID,
			in.SourceSnapshotID, snapshotRows.Int64(), in.AsOf.UTC(), balance.String(),
			in.BalanceNegative, in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash, in.SourceSequence,
			in.SourceCursor, in.StreamWatermarkAt.UTC(), in.SourceRevision, in.ObservedAt.UTC())
		if err != nil {
			return err
		}
		if in.BalanceNegative {
			if err = freezeEligibilityTx(ctx, tx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE",
				"balance_checkpoint", in.CheckpointID, in.SourceRevision, actor); err != nil {
				return err
			}
		}
		legacyID := randomUUID()
		_, err = tx.Exec(ctx, `
			INSERT INTO source_credit_events(
				id,source_instance_id,external_account_id,external_event_id,external_credit_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				credit_kind,source_sequence,source_cursor,stream_watermark_at,
				source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,
				'LEGACY_NON_INVOICEABLE',0,$11,$6,$12,$13)`, legacyID, in.SourceInstanceID,
			accountID, "legacy:"+in.ExternalEventID, "legacy:"+in.CheckpointID, in.AsOf.UTC(),
			balance.String(), in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash,
			in.SourceCursor, in.SourceRevision, in.ObservedAt.UTC())
		if err != nil {
			return err
		}
		// Per-account watermarks are diagnostic only. Source-scoped rows are the
		// completeness authority used by finalizeSourceAccountsTx.
		for _, watermark := range in.Watermarks {
			if !validEconomicStream(watermark.StreamKind) || watermark.WatermarkAt.Before(manifest.CutoverAt) ||
				strings.TrimSpace(watermark.SourceCursor) == "" {
				return errors.New("invalid cutover account watermark")
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO source_account_stream_watermarks(
					external_account_id,stream_kind,watermark_at,source_sequence,source_cursor)
				VALUES($1,$2,$3,$4,$5)
				ON CONFLICT(external_account_id,stream_kind) DO UPDATE SET
					watermark_at=EXCLUDED.watermark_at,source_sequence=EXCLUDED.source_sequence,
					source_cursor=EXCLUDED.source_cursor,updated_at=now()`, accountID,
				watermark.StreamKind, watermark.WatermarkAt.UTC(), watermark.SourceSequence,
				watermark.SourceCursor)
			if err != nil {
				return err
			}
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.account_cutover.created", "external_account", accountID,
			nil, map[string]any{"principal_id": principalID, "manifest_hash": manifest.ManifestHash,
				"cutover_at": manifest.CutoverAt}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if errors.Is(err, domain.ErrSourceUnavailable) && in.CheckpointKind == "reconciliation" {
		var policyStartAt time.Time
		if err = tx.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
			WHERE singleton_id=1`).Scan(&policyStartAt); err != nil {
			return err
		}
		if manifest.CutoverAt.Before(policyStartAt) && in.AsOf.Before(policyStartAt) {
			// XM-INV-POLICY-ANCHOR 2.1: a reconciliation checkpoint dated
			// before the invoice policy start carries no eligibility-relevant
			// information for an account that has not bootstrapped yet -- the
			// account cannot become invoice-eligible before the policy start
			// regardless of which row eventually establishes its cutover
			// boundary. Acknowledge it instead of parking on the (possibly
			// long-delayed) signed cutover row.
			if err = writeAudit(ctx, tx, actor, "eligibility.pre_policy_checkpoint.skipped",
				"external_account", accountID, nil, map[string]any{"checkpoint_id": in.CheckpointID,
					"as_of": in.AsOf, "baseline_member": in.BaselineMember,
					"policy_start_at": policyStartAt}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		// XM-INV-POLICY-ANCHOR 2.1 (superseding the original POST_CUTOVER_REPLAY
		// non-baseline path -- per the team lead's follow-up decision, after
		// this slice no code path creates a new SIGNED_CUTOVER or
		// POST_CUTOVER_REPLAY row, baseline member or not; a signed cutover
		// row for a no-state account is just a pre-policy checkpoint like any
		// other and is skipped above). This reconciliation checkpoint is
		// at/after the policy start, so bootstrap directly from it instead of
		// waiting for the (possibly indefinitely parked) signed cutover row.
		// The checkpoint's entire balance becomes the account's opening,
		// non-invoiceable balance -- the same treatment a SIGNED_CUTOVER
		// baseline gets, just anchored to a later point the invoice policy
		// actually cares about. The state row must be inserted *before* the
		// checkpoint row: the checkpoint table's own (unchanged, immediate)
		// balance_reconciliation_checkpoints_contract_guard trigger requires
		// a trusted state row to already exist, while this state row's own
		// new POLICY_ANCHOR validation (migration 0016) is deferred to
		// COMMIT specifically so it can, in turn, require this same
		// checkpoint row to already exist by then.
		eligibilityStatus := "active"
		if in.CatchupKeyHMAC != "" {
			if !dependencyHMACPattern.MatchString(in.CatchupKeyHMAC) {
				return errors.New("invalid catch-up dependency key")
			}
			eligibilityStatus = "syncing"
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO source_account_eligibility_state(
				external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
				cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,
				catchup_key_hmac,eligibility_status)
			VALUES($1,$2,$3,$4,$5::numeric,$6,'POLICY_ANCHOR',$3,$7,NULLIF($8,''),$9)`,
			accountID, in.SourceInstanceID, in.AsOf.UTC(), in.UnitCode, balance.String(),
			in.CutoverManifestHash, int(defaultEligibilityFinalizationDelay/time.Second),
			in.CatchupKeyHMAC, eligibilityStatus)
		if err != nil {
			return err
		}
		checkpointID := randomUUID()
		_, err = tx.Exec(ctx, `
			INSERT INTO balance_reconciliation_checkpoints(
				id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
				checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
				as_of,balance_service_units,balance_negative,unit_code,
				cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
				source_cursor,stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,'reconciliation',$6,$7,$8,$9,$10::numeric,$11,$12,$13,
				$14,'cutover_baseline',$15,$16,$17,$18,$19)`, checkpointID, in.SourceInstanceID,
			accountID, in.ExternalEventID, in.CheckpointID, in.BaselineMember, in.SourceSnapshotID,
			snapshotRows.Int64(), in.AsOf.UTC(), balance.String(),
			in.BalanceNegative, in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash,
			in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(), in.SourceRevision,
			in.ObservedAt.UTC())
		if err != nil {
			return err
		}
		if in.BalanceNegative {
			if err = freezeEligibilityTx(ctx, tx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE",
				"balance_checkpoint", in.CheckpointID, in.SourceRevision, actor); err != nil {
				return err
			}
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.policy_anchor.bootstrapped", "external_account", accountID,
			nil, map[string]any{"cutover_at": in.AsOf, "opening_units": balance.String(),
				"baseline_member": in.BaselineMember, "status": eligibilityStatus}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err = verifyFactTrustTx(ctx, tx, account, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, "balances", in.StreamWatermarkAt); err != nil {
		return err
	}
	// A checkpoint never finalizes itself. Store the immutable fact, then let
	// the bounded worker evaluate it only after the source-wide four-stream
	// completeness boundary has crossed as_of.
	status := "pending_finalization"
	checkpointID := randomUUID()
	_, err = tx.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
			as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,configuration_hash,
			reconciliation_status,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,'reconciliation',$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,
			$15,$16,$17,$18,$19,$20)`, checkpointID, in.SourceInstanceID,
		accountID, in.ExternalEventID, in.CheckpointID, in.BaselineMember,
		in.SourceSnapshotID, snapshotRows.Int64(), in.AsOf.UTC(), balance.String(),
		in.BalanceNegative, in.UnitCode, in.CutoverManifestHash,
		in.ConfigurationHash, status, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(),
		in.SourceRevision, in.ObservedAt.UTC())
	if err != nil {
		return err
	}
	if !in.AsOf.After(account.FinalizedThrough) {
		_, err = tx.Exec(ctx, `
			INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
			VALUES($1,$2,'queued',now())
			ON CONFLICT(external_account_id) DO UPDATE SET
				requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
				status='queued',lease_token=NULL,lease_expires_at=NULL,
				next_attempt_at=CASE
					WHEN eligibility_projection_jobs.status='queued'
						AND eligibility_projection_jobs.last_error_code='BALANCE_PROOF_PENDING'
						AND eligibility_projection_jobs.next_attempt_at<=now()+make_interval(secs=>$3)
					THEN eligibility_projection_jobs.next_attempt_at
					ELSE now()
				END,
				updated_at=now()`,
			accountID, account.FinalizedThrough, balanceProofPendingRequeueResetWindow.Seconds())
		if err != nil {
			return err
		}
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.balance_checkpoint.observed", "external_account", accountID,
		nil, map[string]any{"checkpoint_id": in.CheckpointID, "status": status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableBigInt(value *big.Int) any {
	if value == nil {
		return nil
	}
	return value.String()
}

func (s *Store) ObserveUsageEvent(ctx context.Context, in UsageObservation, actor AuditActor) error {
	if err := validateFactMetadata(in.SourceInstanceID, in.ExternalUserID, in.ExternalEventID,
		in.SourceRevision, in.SourceCursor, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, in.EventTime, in.ObservedAt, in.StreamWatermarkAt, in.SourceSequence); err != nil {
		return err
	}
	units, err := parseUnsignedUnits(in.ServiceUnits, "usage service units", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.ExternalUsageID) == "" || len(in.ExternalUsageID) > 256 || in.BillingScope != "wallet" {
		return errors.New("invalid wallet usage fact")
	}
	domainValue, order, err := parseOptionalCausal(in.CausalDomain, in.CausalOrder)
	if err != nil {
		return err
	}
	return s.observeEligibilityFact(ctx, eligibilityFactObservation{
		SourceInstanceID: in.SourceInstanceID, ExternalUserID: in.ExternalUserID,
		ExternalEventID: in.ExternalEventID, ExternalObjectID: in.ExternalUsageID,
		EventTime: in.EventTime, ObservedAt: in.ObservedAt, StreamWatermarkAt: in.StreamWatermarkAt,
		Units: units, UnitCode: in.UnitCode, Kind: "usage", DetailKind: in.BillingScope,
		CausalDomain: domainValue, CausalOrder: order, SourceCursor: in.SourceCursor,
		SourceRevision: in.SourceRevision, CutoverManifestHash: in.CutoverManifestHash,
		ConfigurationHash: in.ConfigurationHash, SourceSequence: in.SourceSequence,
		BatchID: in.BatchID, ScanCycleID: in.ScanCycleID,
	}, actor)
}

func (s *Store) ObserveCreditEvent(ctx context.Context, in CreditObservation, actor AuditActor) error {
	if err := validateFactMetadata(in.SourceInstanceID, in.ExternalUserID, in.ExternalEventID,
		in.SourceRevision, in.SourceCursor, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, in.EventTime, in.ObservedAt, in.StreamWatermarkAt, in.SourceSequence); err != nil {
		return err
	}
	units, err := parseUnsignedUnits(in.ServiceUnits, "credit service units", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.ExternalCreditID) == "" || len(in.ExternalCreditID) > 256 ||
		(in.CreditKind != "BONUS" && in.CreditKind != "REBATE" && in.CreditKind != "ADMIN" && in.CreditKind != "UNKNOWN_POSITIVE") {
		return errors.New("invalid non-cash credit fact")
	}
	domainValue, order, err := parseOptionalCausal(in.CausalDomain, in.CausalOrder)
	if err != nil {
		return err
	}
	return s.observeEligibilityFact(ctx, eligibilityFactObservation{
		SourceInstanceID: in.SourceInstanceID, ExternalUserID: in.ExternalUserID,
		ExternalEventID: in.ExternalEventID, ExternalObjectID: in.ExternalCreditID,
		EventTime: in.EventTime, ObservedAt: in.ObservedAt, StreamWatermarkAt: in.StreamWatermarkAt,
		Units: units, UnitCode: in.UnitCode, Kind: "credit", DetailKind: in.CreditKind,
		CausalDomain: domainValue, CausalOrder: order, SourceCursor: in.SourceCursor,
		SourceRevision: in.SourceRevision, CutoverManifestHash: in.CutoverManifestHash,
		ConfigurationHash: in.ConfigurationHash, SourceSequence: in.SourceSequence,
		BatchID: in.BatchID, ScanCycleID: in.ScanCycleID,
	}, actor)
}

type eligibilityFactObservation struct {
	SourceInstanceID, ExternalUserID, ExternalEventID, ExternalObjectID  string
	EventTime, ObservedAt, StreamWatermarkAt                             time.Time
	Units                                                                *big.Int
	UnitCode, Kind, DetailKind, CausalDomain                             string
	CausalOrder                                                          *big.Int
	SourceCursor, SourceRevision, CutoverManifestHash, ConfigurationHash string
	SourceSequence                                                       int64
	BatchID, ScanCycleID                                                 string
}

func (s *Store) observeEligibilityFact(ctx context.Context, in eligibilityFactObservation, actor AuditActor) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	accountID, _, sourceType, err := resolveExternalAccountTx(ctx, tx, in.SourceInstanceID, in.ExternalUserID)
	if err != nil {
		return err
	}
	if in.UnitCode != expectedUnitForSource(sourceType) {
		return domain.ErrConflict
	}
	// XM-INV-PROOF-CONTENTION 2: try, don't block -- see the identical
	// comment in ObserveBalanceCheckpoint above.
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,43))`, accountID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return domain.ErrAccountLockBusy
	}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	stream := "credits"
	if in.Kind == "usage" {
		stream = "usage"
	}
	if _, err = verifyFactBatchContextTx(ctx, tx, in.SourceInstanceID, stream, in.ExternalEventID,
		in.BatchID, in.ScanCycleID, in.SourceRevision, in.StreamWatermarkAt); err != nil {
		return err
	}
	if err = verifyFactTrustTx(ctx, tx, account, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, stream, in.StreamWatermarkAt); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "UNIT_MISMATCH", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
		}
		return err
	}
	if !in.EventTime.After(account.CutoverAt) {
		if account.BootstrapKind == "POLICY_ANCHOR" {
			// XM-INV-PREANCHOR-USAGE: a POLICY_ANCHOR account's cutover_at is
			// itself a reconciliation checkpoint (design XM-INV-POLICY-ANCHOR
			// 2.1), not a replayed history boundary -- a fact at or before it
			// cannot be attributed to any ledger baseline by construction,
			// whether or not it is also before the account's own policy
			// start. That is not evidence of a missing source event the way
			// it is for a legacy SIGNED_CUTOVER/POST_CUTOVER_REPLAY account
			// (whose cutover replays real history up to a known point), so
			// freezing SOURCE_GAP here is wrong: see the 2026-09-02
			// production incident in docs/handoffs/XM-INV-PREANCHOR-USAGE.md
			// (106 accounts freshly POLICY_ANCHOR-bootstrapped by a balances
			// checkpoint had their own backlog of pre-anchor usage facts
			// rejected this way in the same reconcile pass, wedging the
			// ingestion pipeline dead after 8 retries each). Skip: no
			// freeze, one audit row, commit so the caller marks the source
			// event processed. Deliberately does not persist a
			// source_usage_events/source_credit_events row for this fact --
			// consistent with the existing pre-policy-checkpoint precedent
			// (ObserveBalanceCheckpoint's eligibility.pre_policy_checkpoint.skipped
			// path above), a skipped fact is represented purely by its audit
			// row, never as a stored ledger-visible fact.
			if err = writeAudit(ctx, tx, actor, "eligibility."+in.Kind+".pre_anchor_skipped",
				"external_account", accountID, nil, map[string]any{
					"external_object_id": in.ExternalObjectID, "external_event_id": in.ExternalEventID,
					"event_time": in.EventTime, "cutover_at": account.CutoverAt,
					"source_revision": in.SourceRevision}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		if err = freezeEligibilityTx(ctx, tx, accountID, "", "SOURCE_GAP", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); err != nil {
			return err
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return domain.ErrConflict
	}
	table := "source_credit_events"
	objectColumn := "external_credit_id"
	if in.Kind == "usage" {
		table = "source_usage_events"
		objectColumn = "external_usage_id"
	}
	var existingRevision, existingEventID, existingObjectID string
	query := fmt.Sprintf(`
		SELECT source_revision_hash,external_event_id,%s FROM %s
		WHERE source_instance_id=$1 AND (external_event_id=$2 OR %s=$3)`, objectColumn, table, objectColumn)
	err = tx.QueryRow(ctx, query, in.SourceInstanceID, in.ExternalEventID, in.ExternalObjectID).Scan(
		&existingRevision, &existingEventID, &existingObjectID)
	if err == nil {
		if existingRevision != in.SourceRevision || existingEventID != in.ExternalEventID || existingObjectID != in.ExternalObjectID {
			if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_PAYLOAD_DRIFT", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	id := randomUUID()
	causalOrder := nullableBigInt(in.CausalOrder)
	if in.Kind == "usage" {
		invoiceEligible := !in.EventTime.Before(account.PolicyStartAt)
		_, err = tx.Exec(ctx, `
			INSERT INTO source_usage_events(
				id,source_instance_id,external_account_id,external_event_id,external_usage_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				billing_scope,invoice_eligible,causal_domain,causal_order,source_sequence,source_cursor,
				stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,$12,NULLIF($13,''),$14::numeric,
				$15,$16,$17,$18,$19)`, id, in.SourceInstanceID, accountID, in.ExternalEventID,
			in.ExternalObjectID, in.EventTime.UTC(), in.Units.String(), in.UnitCode,
			in.CutoverManifestHash, in.ConfigurationHash, in.DetailKind, invoiceEligible, in.CausalDomain,
			causalOrder, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(),
			in.SourceRevision, in.ObservedAt.UTC())
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO source_credit_events(
				id,source_instance_id,external_account_id,external_event_id,external_credit_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				credit_kind,causal_domain,causal_order,source_sequence,source_cursor,
				stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,NULLIF($12,''),$13::numeric,
				$14,$15,$16,$17,$18)`, id, in.SourceInstanceID, accountID, in.ExternalEventID,
			in.ExternalObjectID, in.EventTime.UTC(), in.Units.String(), in.UnitCode,
			in.CutoverManifestHash, in.ConfigurationHash, in.DetailKind, in.CausalDomain,
			causalOrder, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(),
			in.SourceRevision, in.ObservedAt.UTC())
	}
	if err != nil {
		return err
	}
	late := !in.EventTime.After(account.FinalizedThrough)
	if late {
		if err = reprojectEligibilityTx(ctx, tx, accountID, account.FinalizedThrough, actor); err != nil {
			return err
		}
		if err = freezeEligibilityTx(ctx, tx, accountID, "", "LATE_FINALIZED_EVENT", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); err != nil {
			return err
		}
	}
	if err = writeAudit(ctx, tx, actor, "eligibility."+in.Kind+".observed", in.Kind, id,
		nil, map[string]any{"event_time": in.EventTime, "late": late,
			"source_revision": in.SourceRevision}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanCausal(domainValue, orderValue string) (string, *big.Int, error) {
	if domainValue == "" && orderValue == "" {
		return "", nil, nil
	}
	return parseOptionalCausal(domainValue, orderValue)
}

func buildEligibilityProjectionTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, through time.Time) (eligibilityProjection, error) {
	projection := eligibilityProjection{
		Lots: map[string]*projectedLot{}, ExpectedBalance: new(big.Int),
	}
	facts := make([]eligibilityFact, 0)
	rows, err := tx.Query(ctx, `
		SELECT id,event_time,service_units::text,credit_kind,
			COALESCE(causal_domain,''),COALESCE(causal_order::text,''),source_revision_hash
		FROM source_credit_events
		WHERE external_account_id=$1 AND event_time<=$2
			AND (event_time>$3 OR (event_time=$3 AND credit_kind IN ('LEGACY_NON_INVOICEABLE','UNKNOWN_POSITIVE')))
		ORDER BY event_time,id`, account.ExternalAccountID, through, account.CutoverAt)
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var id, unitsText, creditKind, causalDomain, causalOrder, revision string
		var at time.Time
		if err = rows.Scan(&id, &at, &unitsText, &creditKind, &causalDomain, &causalOrder, &revision); err != nil {
			rows.Close()
			return projection, err
		}
		units, parseErr := parseUnsignedUnits(unitsText, "stored credit units", true)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		domainValue, order, parseErr := scanCausal(causalDomain, causalOrder)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		facts = append(facts, eligibilityFact{Kind: "credit:" + creditKind, ID: id, CreditID: id,
			At: at, Units: units, CausalDomain: domainValue, CausalOrder: order, Revision: revision})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return projection, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT fl.id,fl.completed_at,fl.verified_cash_minor,fl.reserved_minor,fl.issued_minor,
			flcs.cash_service_units::text,flcs.consumed_service_units::text,
			flcs.rounded_consumed_cash_minor,COALESCE(flcs.causal_domain,''),
			COALESCE(flcs.causal_order::text,''),fl.source_revision_hash
		FROM funding_lots fl
		JOIN funding_lot_consumption_state flcs ON flcs.funding_lot_id=fl.id
		WHERE fl.external_account_id=$1 AND fl.eligibility_kind='WALLET_CASH'
			AND fl.completed_at>$3 AND fl.completed_at<=$2
			AND fl.completed_at>=$4
			AND fl.verification_state='verified' AND fl.refund_frozen=FALSE
		ORDER BY fl.completed_at,fl.id FOR UPDATE OF fl,flcs`, account.ExternalAccountID, through, account.CutoverAt, account.PolicyStartAt)
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var id, totalText, oldUnitsText, causalDomain, causalOrder, revision string
		var at time.Time
		var paid, reserved, issued, oldMinor int64
		if err = rows.Scan(&id, &at, &paid, &reserved, &issued, &totalText, &oldUnitsText,
			&oldMinor, &causalDomain, &causalOrder, &revision); err != nil {
			rows.Close()
			return projection, err
		}
		total, parseErr := parseUnsignedUnits(totalText, "stored cash service units", false)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		oldUnits, parseErr := parseUnsignedUnits(oldUnitsText, "stored consumed service units", true)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		domainValue, order, parseErr := scanCausal(causalDomain, causalOrder)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		lot := &projectedLot{ID: id, PaidMinor: paid, TotalUnits: total,
			ConsumedUnits: new(big.Int), Numerator: new(big.Int), Remainder: new(big.Int),
			OldMinor: oldMinor, OldUnits: oldUnits, IssuedMinor: issued, ReservedMinor: reserved}
		projection.Lots[id] = lot
		facts = append(facts, eligibilityFact{Kind: "payment", ID: id, LotID: id, At: at,
			Units: new(big.Int).Set(total), PaidMinor: paid, CausalDomain: domainValue,
			CausalOrder: order, Revision: revision})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return projection, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT id,event_time,service_units::text,invoice_eligible,COALESCE(causal_domain,''),
			COALESCE(causal_order::text,''),source_revision_hash
		FROM source_usage_events
		WHERE external_account_id=$1 AND event_time>$3 AND event_time<=$2
		ORDER BY event_time,id`, account.ExternalAccountID, through, account.CutoverAt)
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var id, unitsText, causalDomain, causalOrder, revision string
		var at time.Time
		var invoiceEligible bool
		if err = rows.Scan(&id, &at, &unitsText, &invoiceEligible, &causalDomain, &causalOrder, &revision); err != nil {
			rows.Close()
			return projection, err
		}
		units, parseErr := parseUnsignedUnits(unitsText, "stored usage units", false)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		domainValue, order, parseErr := scanCausal(causalDomain, causalOrder)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		facts = append(facts, eligibilityFact{Kind: "usage", ID: id, UsageID: id,
			At: at, Units: units, InvoiceEligible: invoiceEligible,
			CausalDomain: domainValue, CausalOrder: order, Revision: revision})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return projection, err
	}
	rows.Close()

	sort.Slice(facts, func(i, j int) bool {
		if !facts[i].At.Equal(facts[j].At) {
			return facts[i].At.Before(facts[j].At)
		}
		// UNKNOWN_POSITIVE is intentionally anchored at a trusted interval
		// start and conservatively precedes every fact in that interval.
		if facts[i].Kind == "credit:UNKNOWN_POSITIVE" && facts[j].Kind != facts[i].Kind {
			return true
		}
		if facts[j].Kind == "credit:UNKNOWN_POSITIVE" && facts[i].Kind != facts[j].Kind {
			return false
		}
		if facts[i].CausalOrder != nil && facts[j].CausalOrder != nil && facts[i].CausalDomain == facts[j].CausalDomain {
			if comparison := facts[i].CausalOrder.Cmp(facts[j].CausalOrder); comparison != 0 {
				return comparison < 0
			}
		}
		return facts[i].ID < facts[j].ID
	})
	for start := 0; start < len(facts); {
		end := start + 1
		for end < len(facts) && facts[end].At.Equal(facts[start].At) {
			end++
		}
		if end-start > 1 && ambiguousFactGroup(facts[start:end]) {
			projection.AmbiguousAt = facts[start].At
			return projection, nil
		}
		start = end
	}

	type pool struct {
		id        string
		remaining *big.Int
	}
	nonCash := make([]pool, 0)
	cash := make([]pool, 0)
	for _, fact := range facts {
		switch {
		case strings.HasPrefix(fact.Kind, "credit:"):
			nonCash = append(nonCash, pool{id: fact.CreditID, remaining: new(big.Int).Set(fact.Units)})
		case fact.Kind == "payment":
			cash = append(cash, pool{id: fact.LotID, remaining: new(big.Int).Set(fact.Units)})
		case fact.Kind == "usage":
			remaining := new(big.Int).Set(fact.Units)
			allocationOrder := 0
			for i := range nonCash {
				if remaining.Sign() == 0 {
					break
				}
				amount := minBig(remaining, nonCash[i].remaining)
				if amount.Sign() == 0 {
					continue
				}
				allocationOrder++
				projection.Allocations = append(projection.Allocations, projectedAllocation{
					UsageID: fact.UsageID, CreditID: nonCash[i].id, Order: allocationOrder,
					Units: new(big.Int).Set(amount),
				})
				nonCash[i].remaining.Sub(nonCash[i].remaining, amount)
				remaining.Sub(remaining, amount)
			}
			if !fact.InvoiceEligible {
				if remaining.Sign() > 0 && projection.ShortfallUsage == "" {
					projection.ShortfallUsage = fact.UsageID
				}
				continue
			}
			for i := range cash {
				if remaining.Sign() == 0 {
					break
				}
				amount := minBig(remaining, cash[i].remaining)
				if amount.Sign() == 0 {
					continue
				}
				lot := projection.Lots[cash[i].id]
				oldRounded := lot.RoundedMinor
				lot.ConsumedUnits.Add(lot.ConsumedUnits, amount)
				rounded, numerator, remainder, roundErr := cumulativeCashRound(lot.PaidMinor, lot.ConsumedUnits, lot.TotalUnits)
				if roundErr != nil {
					return projection, roundErr
				}
				lot.RoundedMinor, lot.Numerator, lot.Remainder = rounded, numerator, remainder
				allocationOrder++
				projection.Allocations = append(projection.Allocations, projectedAllocation{
					UsageID: fact.UsageID, LotID: cash[i].id, Order: allocationOrder,
					Units: new(big.Int).Set(amount), CashMinorDelta: rounded - oldRounded,
				})
				cash[i].remaining.Sub(cash[i].remaining, amount)
				remaining.Sub(remaining, amount)
			}
			if remaining.Sign() > 0 && projection.ShortfallUsage == "" {
				projection.ShortfallUsage = fact.UsageID
			}
		}
	}
	for _, item := range nonCash {
		projection.ExpectedBalance.Add(projection.ExpectedBalance, item.remaining)
	}
	for _, item := range cash {
		projection.ExpectedBalance.Add(projection.ExpectedBalance, item.remaining)
	}
	return projection, nil
}

func ambiguousFactGroup(group []eligibilityFact) bool {
	if len(group) < 2 {
		return false
	}
	filtered := make([]eligibilityFact, 0, len(group))
	for _, fact := range group {
		if fact.Kind == "credit:UNKNOWN_POSITIVE" {
			continue
		}
		filtered = append(filtered, fact)
	}
	if len(filtered) < 2 {
		return false
	}
	allCredit, allUsage := true, true
	for _, fact := range filtered {
		allCredit = allCredit && strings.HasPrefix(fact.Kind, "credit:")
		allUsage = allUsage && fact.Kind == "usage"
	}
	if allCredit || allUsage {
		return false
	}
	domainValue := filtered[0].CausalDomain
	if domainValue == "" {
		return true
	}
	seen := map[string]struct{}{}
	for _, fact := range filtered {
		if fact.CausalDomain != domainValue || fact.CausalOrder == nil {
			return true
		}
		key := fact.CausalOrder.String()
		if _, duplicate := seen[key]; duplicate {
			return true
		}
		seen[key] = struct{}{}
	}
	return false
}

func minBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func cumulativeCashRound(paidMinor int64, consumed, total *big.Int) (int64, *big.Int, *big.Int, error) {
	if paidMinor < 0 || total.Sign() <= 0 || consumed.Sign() < 0 || consumed.Cmp(total) > 0 {
		return 0, nil, nil, errors.New("invalid cumulative cash ratio")
	}
	numerator := new(big.Int).Mul(big.NewInt(paidMinor), consumed)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, total, remainder)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(total) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Int64() < 0 || quotient.Int64() > paidMinor {
		return 0, nil, nil, errors.New("rounded cumulative cash is outside verified payment")
	}
	rounded := quotient.Int64()
	roundingRemainder := new(big.Int).Sub(new(big.Int).Set(numerator), new(big.Int).Mul(big.NewInt(rounded), total))
	return rounded, numerator, roundingRemainder, nil
}

func reprojectEligibilityTx(ctx context.Context, tx pgx.Tx, accountID string, through time.Time, actor AuditActor) error {
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, through)
	if err != nil {
		return err
	}
	if !projection.AmbiguousAt.IsZero() {
		return freezeEligibilityTx(ctx, tx, accountID, "", "AMBIGUOUS_EVENT_ORDER", "event_time",
			projection.AmbiguousAt.UTC().Format(time.RFC3339Nano), "", actor)
	}
	if projection.ShortfallUsage != "" {
		if err = freezeEligibilityTx(ctx, tx, accountID, "", "USAGE_EXCEEDS_LEDGER", "usage_event",
			projection.ShortfallUsage, "", actor); err != nil {
			return err
		}
	}
	// Derived allocations may be rebuilt because source facts themselves are
	// immutable. The account advisory lock and SERIALIZABLE transaction make a
	// replay atomic with invoice reservation/refund transitions.
	if _, err = tx.Exec(ctx, `
		DELETE FROM consumption_allocations ca USING source_usage_events sue
		WHERE ca.usage_event_id=sue.id AND sue.external_account_id=$1`, accountID); err != nil {
		return err
	}
	projectionVersion := account.Version + 1
	for _, allocation := range projection.Allocations {
		_, err = tx.Exec(ctx, `
			INSERT INTO consumption_allocations(
				id,usage_event_id,funding_lot_id,credit_event_id,allocation_order,
				service_units,cash_minor_delta,projection_version)
			VALUES($1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,$5,$6::numeric,$7,$8)`,
			randomUUID(), allocation.UsageID, allocation.LotID, allocation.CreditID,
			allocation.Order, allocation.Units.String(), allocation.CashMinorDelta, projectionVersion)
		if err != nil {
			return err
		}
	}
	lotIDs := make([]string, 0, len(projection.Lots))
	for id := range projection.Lots {
		lotIDs = append(lotIDs, id)
	}
	sort.Strings(lotIDs)
	for _, id := range lotIDs {
		lot := projection.Lots[id]
		if lot.RoundedMinor < lot.OldMinor {
			if err = invalidateLotReservationsTx(ctx, tx, lot.ID, actor); err != nil {
				return err
			}
			if err = tx.QueryRow(ctx, `SELECT reserved_minor,issued_minor FROM funding_lots WHERE id=$1 FOR UPDATE`, lot.ID).
				Scan(&lot.ReservedMinor, &lot.IssuedMinor); err != nil {
				return err
			}
			if lot.RoundedMinor < lot.IssuedMinor {
				if err = markLotIssuedAttentionTx(ctx, tx, lot, actor); err != nil {
					return err
				}
				if err = freezeEligibilityTx(ctx, tx, accountID, lot.ID, "LATE_FINALIZED_EVENT",
					"funding_lot", lot.ID, "", actor); err != nil {
					return err
				}
				// Preserve the last recognized amount as an accounting exposure
				// floor. The open refund case records the lower recomputation.
				continue
			}
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE funding_lots
			SET consumed_cash_minor=$1,eligibility_revision=eligibility_revision+1,updated_at=now()
			WHERE id=$2 AND eligibility_kind='WALLET_CASH'
				AND reserved_minor+issued_minor<=$1`, lot.RoundedMinor, lot.ID)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		_, err = tx.Exec(ctx, `
			UPDATE funding_lot_consumption_state SET
				consumed_service_units=$1::numeric,cumulative_cash_numerator=$2::numeric,
				rounded_consumed_cash_minor=$3,rounding_remainder_numerator=$4::numeric,
				state_version=state_version+1,updated_at=now()
			WHERE funding_lot_id=$5`, lot.ConsumedUnits.String(), lot.Numerator.String(),
			lot.RoundedMinor, lot.Remainder.String(), lot.ID)
		if err != nil {
			return err
		}
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.projection.rebuilt", "external_account", accountID,
		map[string]any{"finalized_through": account.FinalizedThrough, "projection_version": account.Version},
		map[string]any{"requested_through": through, "projection_version": projectionVersion,
			"allocation_count": len(projection.Allocations)}); err != nil {
		return err
	}
	return nil
}

func freezeEligibilityTx(ctx context.Context, tx pgx.Tx, accountID, lotID, reason, objectType, objectID, revision string, actor AuditActor) error {
	if strings.TrimSpace(objectID) == "" {
		objectID = accountID
	}
	if !validHash(revision) {
		revision = ""
	}
	freezeID := randomUUID()
	err := tx.QueryRow(ctx, `
		INSERT INTO eligibility_freezes(
			id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,
			trigger_object_id,source_revision_hash)
		VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,NULLIF($7,''))
		ON CONFLICT(external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
			WHERE status='open'
		DO UPDATE SET updated_at=eligibility_freezes.updated_at
		RETURNING id`, freezeID, accountID, lotID, reason, objectType, objectID, revision).Scan(&freezeID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET eligibility_status='frozen',projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1`, accountID); err != nil {
		return err
	}
	if err = invalidateAccountReservationsTx(ctx, tx, accountID, actor); err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.frozen."+strings.ToLower(reason),
		"eligibility_freeze", freezeID, nil, map[string]any{"external_account_id": accountID,
			"funding_lot_id": lotID, "reason": reason, "trigger_type": objectType,
			"trigger_id": objectID}); err != nil {
		return err
	}
	return nil
}

func freezeRefundedLotTx(ctx context.Context, tx pgx.Tx, accountID, lotID string, oldConsumed int64, revision string, actor AuditActor) error {
	if err := invalidateLotReservationsTx(ctx, tx, lotID, actor); err != nil {
		return err
	}
	var currentCap, verifiedCash, reserved, issued int64
	if err := tx.QueryRow(ctx, `
		SELECT current_cap_minor,verified_cash_minor,reserved_minor,issued_minor
		FROM funding_lots WHERE id=$1 FOR UPDATE`, lotID).Scan(&currentCap, &verifiedCash, &reserved, &issued); err != nil {
		return err
	}
	if verifiedCash > oldConsumed {
		oldConsumed = verifiedCash
	}
	if issued > 0 {
		attention := &projectedLot{ID: lotID, OldMinor: oldConsumed, RoundedMinor: currentCap,
			IssuedMinor: issued, ReservedMinor: reserved}
		if err := markLotIssuedAttentionTx(ctx, tx, attention, actor); err != nil {
			return err
		}
	}
	freezeID := randomUUID()
	if !validHash(revision) {
		revision = ""
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO eligibility_freezes(
			id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,
			trigger_object_id,source_revision_hash)
		VALUES($1,$2,$3::uuid,'SOURCE_REFUND','funding_lot',$3::text,NULLIF($4,''))
		ON CONFLICT(external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
			WHERE status='open'
		DO UPDATE SET updated_at=now()
		RETURNING id`, freezeID, accountID, lotID, revision).Scan(&freezeID)
	if err != nil {
		return err
	}
	return writeAudit(ctx, tx, actor, "eligibility.funding_lot.refund_frozen", "funding_lot", lotID,
		map[string]any{"verified_cash_minor": verifiedCash, "consumed_cash_minor": oldConsumed},
		map[string]any{"refund_frozen": true, "current_cap_minor": currentCap, "freeze_id": freezeID})
}

func invalidateAccountReservationsTx(ctx context.Context, tx pgx.Tx, accountID string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version
		FROM invoice_requests ir
		WHERE EXISTS (
			SELECT 1 FROM invoice_allocations ia
			JOIN funding_lots fl ON fl.id=ia.funding_lot_id
			WHERE ia.invoice_request_id=ir.id AND ia.allocation_state='reserved'
				AND fl.external_account_id=$1)
			AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
		ORDER BY ir.id FOR UPDATE OF ir`, accountID)
	if err != nil {
		return err
	}
	type pending struct {
		id      string
		status  domain.RequestStatus
		version int64
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.status, &item.version); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if err = releaseReservations(ctx, tx, item.id); err != nil {
			return err
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE invoice_requests SET status='rejected',
				review_note='eligibility freeze invalidated this unissued request',
				version=version+1,updated_at=now()
			WHERE id=$1 AND version=$2`, item.id, item.version)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.invalidated_by_eligibility_freeze",
			"invoice_request", item.id, map[string]any{"status": item.status},
			map[string]any{"status": domain.StatusRejected}); err != nil {
			return err
		}
	}
	return nil
}

func invalidateLotReservationsTx(ctx context.Context, tx pgx.Tx, lotID string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version
		FROM invoice_requests ir
		WHERE EXISTS (
			SELECT 1 FROM invoice_allocations ia
			WHERE ia.invoice_request_id=ir.id AND ia.funding_lot_id=$1
				AND ia.allocation_state='reserved')
			AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
		ORDER BY ir.id FOR UPDATE OF ir`, lotID)
	if err != nil {
		return err
	}
	type item struct {
		id      string
		status  domain.RequestStatus
		version int64
	}
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.id, &value.status, &value.version); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, value := range items {
		if err = releaseReservations(ctx, tx, value.id); err != nil {
			return err
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE invoice_requests SET status='rejected',
				review_note='consumption ledger reduction invalidated this unissued request',
				version=version+1,updated_at=now()
			WHERE id=$1 AND version=$2`, value.id, value.version)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.invalidated_by_consumption_replay",
			"invoice_request", value.id, map[string]any{"status": value.status},
			map[string]any{"status": domain.StatusRejected}); err != nil {
			return err
		}
	}
	return nil
}

func markLotIssuedAttentionTx(ctx context.Context, tx pgx.Tx, lot *projectedLot, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version,ia.amount_minor,fl.source_revision_hash
		FROM invoice_allocations ia
		JOIN invoice_requests ir ON ir.id=ia.invoice_request_id
		JOIN funding_lots fl ON fl.id=ia.funding_lot_id
		WHERE ia.funding_lot_id=$1 AND ia.allocation_state IN ('issued','refund_attention')
			AND ir.status IN ('issued_awaiting_document','issued','refund_attention')
		ORDER BY ir.id FOR UPDATE OF ir`, lot.ID)
	if err != nil {
		return err
	}
	type attention struct {
		id, revision      string
		status            domain.RequestStatus
		version, exposure int64
	}
	items := make([]attention, 0)
	for rows.Next() {
		var item attention
		if err = rows.Scan(&item.id, &item.status, &item.version, &item.exposure, &item.revision); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if _, err = tx.Exec(ctx, `
			UPDATE invoice_allocations SET allocation_state='refund_attention',updated_at=now()
			WHERE invoice_request_id=$1 AND funding_lot_id=$2
				AND allocation_state IN ('issued','refund_attention')`, item.id, lot.ID); err != nil {
			return err
		}
		caseID := randomUUID()
		observedReduction := lot.OldMinor - lot.RoundedMinor
		if observedReduction <= 0 {
			observedReduction = 1
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO refund_cases(
				id,invoice_request_id,funding_lot_id,source_revision_hash,
				observed_refund_minor,issued_exposure_minor,observed_cap_minor)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(invoice_request_id,funding_lot_id) WHERE status='open'
			DO UPDATE SET observed_refund_minor=GREATEST(refund_cases.observed_refund_minor,EXCLUDED.observed_refund_minor),
				issued_exposure_minor=GREATEST(refund_cases.issued_exposure_minor,EXCLUDED.issued_exposure_minor),
				observed_cap_minor=LEAST(refund_cases.observed_cap_minor,EXCLUDED.observed_cap_minor),updated_at=now()
			RETURNING id`, caseID, item.id, lot.ID, item.revision, observedReduction,
			item.exposure, lot.RoundedMinor).Scan(&caseID)
		if err != nil {
			return err
		}
		if item.status != domain.StatusRefundAttention {
			command, updateErr := tx.Exec(ctx, `
				UPDATE invoice_requests SET status='refund_attention',version=version+1,updated_at=now()
				WHERE id=$1 AND version=$2`, item.id, item.version)
			if updateErr != nil {
				return updateErr
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.consumption_replay_attention",
			"invoice_request", item.id, map[string]any{"status": item.status},
			map[string]any{"status": domain.StatusRefundAttention, "refund_case_id": caseID}); err != nil {
			return err
		}
	}
	return nil
}

// ProcessEligibilityProjectionJobs is a bounded, retryable worker entry point.
// It must run outside the signed ingest transaction. Multiple replicas safely
// share work through SKIP LOCKED leases.
func (s *Store) ProcessEligibilityProjectionJobs(ctx context.Context, limit int, now time.Time, actor AuditActor) (int, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lease := randomUUID()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
		WITH candidates AS (
			SELECT external_account_id FROM eligibility_projection_jobs
			WHERE (status IN ('queued','failed') AND next_attempt_at<=$1)
				OR (status='processing' AND lease_expires_at<=$1)
			ORDER BY next_attempt_at,external_account_id
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE eligibility_projection_jobs j
		SET status='processing',lease_token=$3,lease_expires_at=$1+interval '2 minutes',
			attempt_count=attempt_count+1,updated_at=$1
		FROM candidates c WHERE j.external_account_id=c.external_account_id
		RETURNING j.external_account_id`, now.UTC(), limit, lease)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	processed := 0
	var firstProcessingError error
	for _, accountID := range ids {
		if processErr := s.processEligibilityProjectionJob(ctx, accountID, lease, actor); processErr != nil {
			if errors.Is(processErr, errBalanceCarryForwardProofPending) {
				// Exponential backoff keyed off the job's own attempt_count
				// (already incremented by the claim step's UPDATE above):
				// 30s,60s,120s,240s,480s, capped at balanceProofPendingBackoffCapSeconds
				// from the 6th attempt on. See the doc comment on that
				// constant for the full rationale.
				_, markErr := s.pool.Exec(ctx, fmt.Sprintf(`
					UPDATE eligibility_projection_jobs SET status='queued',lease_token=NULL,lease_expires_at=NULL,
						last_error_code='BALANCE_PROOF_PENDING',
						next_attempt_at=$3::timestamptz+(LEAST(%d,30*power(2,LEAST(attempt_count,11)-1)))::int*interval '1 second',
						updated_at=$3::timestamptz
					WHERE external_account_id=$1 AND lease_token=$2`, balanceProofPendingBackoffCapSeconds),
					accountID, lease, now.UTC())
				if markErr != nil {
					return processed, markErr
				}
				continue
			}
			if firstProcessingError == nil {
				firstProcessingError = fmt.Errorf("eligibility projection job failed: %w", processErr)
			}
			_, markErr := s.pool.Exec(ctx, `
				UPDATE eligibility_projection_jobs SET status='failed',lease_token=NULL,lease_expires_at=NULL,
					last_error_code='PROJECTION_FAILED',next_attempt_at=now()+interval '5 minutes',updated_at=now()
				WHERE external_account_id=$1 AND lease_token=$2`, accountID, lease)
			if markErr != nil {
				return processed, markErr
			}
			continue
		}
		processed++
	}
	return processed, firstProcessingError
}

func applyFundingObservationEligibilityTx(ctx context.Context, tx pgx.Tx, lotID, accountID string, in SourceObservation, actor AuditActor) error {
	// V1/V2 payment facts are retained for audit but can never infer post-cutover
	// entitlement. Only the complete signed v3 contract enters this branch.
	if in.EligibilityKind == "" || strings.TrimSpace(in.CashServiceUnits) == "" ||
		strings.TrimSpace(in.WalletUnitCode) == "" || !validHash(in.CutoverManifestHash) ||
		!validHash(in.ConfigurationHash) {
		return nil
	}
	if in.EligibilityKind != domain.EligibilityWalletCash && in.EligibilityKind != domain.EligibilitySubscriptionCash {
		return errors.New("source payment eligibility kind must be wallet or subscription cash")
	}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	if _, err = verifyFactBatchContextTx(ctx, tx, in.Lot.SourceInstanceID, "payments", in.ExternalEventID,
		in.BatchID, in.ScanCycleID, in.Lot.SourceRevision, in.StreamWatermarkAt); err != nil {
		return err
	}
	if err = verifyFactTrustTx(ctx, tx, account, in.CutoverManifestHash, in.ConfigurationHash,
		in.WalletUnitCode, "payments", in.StreamWatermarkAt); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "UNIT_MISMATCH", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
		return err
	}
	sourceCutover := account.CutoverAt
	if in.EligibilityKind == domain.EligibilitySubscriptionCash {
		sourceCutover = account.GlobalCutoverAt
	}
	if in.Lot.CompletedAt.IsZero() || !in.Lot.CompletedAt.After(sourceCutover) {
		return nil
	}
	cashUnits, err := parseUnsignedUnits(in.CashServiceUnits, "wallet cash service units", in.EligibilityKind == domain.EligibilitySubscriptionCash)
	if err != nil {
		return err
	}
	parsedDomain, parsedOrder, err := parseOptionalCausal(in.CausalDomain, in.CausalOrder)
	if err != nil {
		return err
	}
	in.CausalDomain = parsedDomain
	if in.Lot.CompletedAt.Before(account.PolicyStartAt) {
		if in.EligibilityKind == domain.EligibilityWalletCash {
			return applyPrePolicyWalletFundingTx(ctx, tx, lotID, accountID, account, in, cashUnits, parsedOrder, actor)
		}
		return nil
	}
	eligibilityCutover := sourceCutover
	if eligibilityCutover.Before(account.PolicyStartAt) {
		eligibilityCutover = account.PolicyStartAt
	}
	var causalOrder any
	if parsedOrder != nil {
		causalOrder = parsedOrder.String()
	}
	var verification domain.VerificationState
	var originalMinor, currentCap, oldVerified, oldConsumed int64
	var existingKind domain.EligibilityKind
	var sourceRevision string
	var oldRefundFrozen bool
	err = tx.QueryRow(ctx, `
		SELECT verification_state,original_minor,current_cap_minor,verified_cash_minor,
			consumed_cash_minor,eligibility_kind,source_revision_hash,refund_frozen
		FROM funding_lots WHERE id=$1 FOR UPDATE`, lotID).Scan(&verification, &originalMinor,
		&currentCap, &oldVerified, &oldConsumed, &existingKind, &sourceRevision, &oldRefundFrozen)
	if err != nil {
		return err
	}
	verifiedCash := int64(0)
	if verification == domain.VerificationVerified {
		verifiedCash = currentCap
	}
	if verifiedCash > originalMinor {
		return domain.ErrConflict
	}
	refundFrozen := oldRefundFrozen || in.EventKind == "refund" || in.EventKind == "tombstone" ||
		(oldVerified > 0 && currentCap < oldVerified)
	if refundFrozen {
		verifiedCash = oldVerified
		if verifiedCash == 0 && verification == domain.VerificationFrozen {
			verifiedCash = currentCap
		}
	}
	if existingKind != domain.EligibilityLegacyNonInvoiceable && existingKind != in.EligibilityKind {
		return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
			sourceRevision, actor)
	}
	if in.EligibilityKind == domain.EligibilityWalletCash && existingKind == domain.EligibilityWalletCash {
		var oldUnits, oldDomain, oldOrder string
		err = tx.QueryRow(ctx, `
			SELECT cash_service_units::text,COALESCE(causal_domain,''),COALESCE(causal_order::text,'')
			FROM funding_lot_consumption_state WHERE funding_lot_id=$1 FOR UPDATE`, lotID).Scan(
			&oldUnits, &oldDomain, &oldOrder)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		newOrder := ""
		if parsedOrder != nil {
			newOrder = parsedOrder.String()
		}
		if err == nil && (oldUnits != cashUnits.String() || oldDomain != in.CausalDomain || oldOrder != newOrder) {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
	}
	consumed := oldConsumed
	if refundFrozen {
		consumed = oldConsumed
	}
	if consumed > verifiedCash && !refundFrozen {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `
		UPDATE funding_lots SET eligibility_kind=$1,eligibility_cutover_at=$2,
			verified_cash_minor=$3,consumed_cash_minor=$4,refund_frozen=$5,
			eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE id=$6`, in.EligibilityKind, eligibilityCutover, verifiedCash, consumed, refundFrozen, lotID)
	if err != nil {
		return err
	}
	if in.EligibilityKind == domain.EligibilityWalletCash {
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_lot_consumption_state(
				funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
				rounded_consumed_cash_minor,rounding_remainder_numerator,causal_domain,causal_order)
			VALUES($1,$2::numeric,0,0,$3,0,NULLIF($4,''),$5::numeric)
			ON CONFLICT(funding_lot_id) DO UPDATE SET
				cash_service_units=EXCLUDED.cash_service_units,causal_domain=EXCLUDED.causal_domain,
				causal_order=EXCLUDED.causal_order,state_version=funding_lot_consumption_state.state_version+1,
				updated_at=now()`, lotID, cashUnits.String(), consumed, in.CausalDomain, causalOrder)
		if err != nil {
			return err
		}
	}
	if refundFrozen {
		return nil
	}
	if !in.Lot.CompletedAt.After(account.FinalizedThrough) && oldVerified == 0 {
		// The payment fact itself was present before finalization only when this
		// is a later independent evidence approval. A first-seen payment after a
		// published boundary violates completeness and freezes instead.
		if verification != domain.VerificationVerified || in.Lot.SourceType != domain.SourceNewAPI {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "LATE_FINALIZED_EVENT", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
	}
	return nil
}

// reanchorLegacyEligibilityAccountTx implements design XM-INV-POLICY-ANCHOR
// 2.4: an account still carrying a legacy bootstrap (SIGNED_CUTOVER or
// POST_CUTOVER_REPLAY -- after this slice, no code path creates a new row of
// either kind, so this set only ever shrinks and a re-anchored account never
// matches it again, making the whole flow naturally idempotent) is
// re-anchored to POLICY_ANCHOR in place the next time its projection job
// runs, before projecting. bootstrap_kind is the trigger condition, not
// cutover_at < policy start: that was tried first and reverted after it
// started firing on every legacy account regardless of genuine staleness
// (every real manifest's cutover necessarily precedes the policy start by
// construction) -- see the handoff doc for that history.
//
// The trust-boundary trigger stays immutable by default; migration 0016
// carves out exactly one exception (transaction-local
// invoice.policy_anchor_reanchor='on', legacy OLD kind, POLICY_ANCHOR NEW
// kind, and the same checkpoint/policy-start/manifest/finalized_through
// validation an INSERT gets), which is what makes an UPDATE-based re-anchor
// possible at all -- the original design called for DELETE-and-reinsert,
// blocked by non-deferrable ON DELETE RESTRICT foreign keys on
// source_account_eligibility_state(external_account_id)
// (source_account_stream_watermarks, and eligibility_projection_jobs' own
// row for the account being processed); see the handoff doc for that
// history too.
//
// Returns true when it performed a re-anchor UPDATE, so the caller knows to
// re-read the account before continuing to project with it.
func reanchorLegacyEligibilityAccountTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, actor AuditActor) (bool, error) {
	if account.BootstrapKind != "SIGNED_CUTOVER" && account.BootstrapKind != "POST_CUTOVER_REPLAY" {
		return false, nil
	}
	// An active invoice reservation or issuance on any of the account's
	// funding lots means real invoice accounting already depends on the
	// current (pre-anchor) consumption state -- resetting it would corrupt
	// that, and for an issued lot the funding_lots_consumed_cash_allocation_bound
	// CHECK constraint would refuse the reset outright regardless. Includes
	// refund_attention (an issued lot already under refund review) alongside
	// reserved/issued -- everything except released, which no longer
	// represents a live claim.
	var hasExposure bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM invoice_allocations ia
			JOIN funding_lots fl ON fl.id=ia.funding_lot_id
			WHERE fl.external_account_id=$1 AND ia.allocation_state IN ('reserved','issued','refund_attention')
		)`, account.ExternalAccountID).Scan(&hasExposure); err != nil {
		return false, err
	}
	if hasExposure {
		var alreadyBlocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM eligibility_freezes
			WHERE external_account_id=$1 AND freeze_reason='POLICY_ANCHOR_BLOCKED' AND status='open')`,
			account.ExternalAccountID).Scan(&alreadyBlocked); err != nil {
			return false, err
		}
		if alreadyBlocked {
			return false, nil
		}
		if err := freezeEligibilityTx(ctx, tx, account.ExternalAccountID, "", "POLICY_ANCHOR_BLOCKED",
			"source_account_eligibility_state", account.ExternalAccountID, "", actor); err != nil {
			return false, err
		}
		return false, writeAudit(ctx, tx, actor, "eligibility.policy_anchor.blocked", "external_account",
			account.ExternalAccountID, map[string]any{"bootstrap_kind": account.BootstrapKind,
				"cutover_at": account.CutoverAt}, nil)
	}

	var candidateAsOf time.Time
	var candidateBalance string
	err := tx.QueryRow(ctx, `
		SELECT as_of,balance_service_units::text FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_kind='reconciliation' AND as_of>=$2
		ORDER BY as_of,id LIMIT 1`, account.ExternalAccountID, account.PolicyStartAt).Scan(
		&candidateAsOf, &candidateBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		// No post-policy checkpoint has arrived yet to anchor to. Continue
		// projecting under the existing anchor -- do not block settlement;
		// the next job retries this check.
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var oldBalance string
	if err = tx.QueryRow(ctx, `SELECT cutover_balance_units::text FROM source_account_eligibility_state
		WHERE external_account_id=$1`, account.ExternalAccountID).Scan(&oldBalance); err != nil {
		return false, err
	}
	deleted, err := tx.Exec(ctx, `
		DELETE FROM consumption_allocations
		WHERE usage_event_id IN (SELECT id FROM source_usage_events WHERE external_account_id=$1)`,
		account.ExternalAccountID)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lot_consumption_state flcs SET
			consumed_service_units=0,cumulative_cash_numerator=0,
			rounded_consumed_cash_minor=0,rounding_remainder_numerator=0,
			state_version=state_version+1,updated_at=now()
		FROM funding_lots fl
		WHERE fl.id=flcs.funding_lot_id AND fl.external_account_id=$1
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')`,
		account.ExternalAccountID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lots SET consumed_cash_minor=0,eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE external_account_id=$1 AND eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')`,
		account.ExternalAccountID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('invoice.policy_anchor_reanchor','on',true)`); err != nil {
		return false, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_account_eligibility_state SET
			cutover_at=$2,cutover_balance_units=$3::numeric,bootstrap_kind='POLICY_ANCHOR',
			finalized_through=$2,projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1`, account.ExternalAccountID, candidateAsOf, candidateBalance)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, domain.ErrConflict
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.policy_anchor.migrated", "external_account",
		account.ExternalAccountID,
		map[string]any{"bootstrap_kind": account.BootstrapKind, "cutover_at": account.CutoverAt,
			"cutover_balance_units": oldBalance},
		map[string]any{"bootstrap_kind": "POLICY_ANCHOR", "cutover_at": candidateAsOf,
			"cutover_balance_units": candidateBalance, "consumption_allocations_deleted": deleted.RowsAffected()},
	); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) processEligibilityProjectionJob(ctx context.Context, accountID, lease string, actor AuditActor) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// The runtime role's defensive statement_timeout (15s) is sized for
	// request-path queries. A first-login reprojection of a heavy account
	// replays its full post-cutover history (thousands of usage facts) in
	// this one SERIALIZABLE transaction and legitimately exceeds it -- the
	// first such whale account in production wedged at PROJECTION_FAILED /
	// SQLSTATE 57014 across 36 retries. Grant this job a bounded,
	// transaction-local budget instead (same pattern the OIDC retention
	// worker already uses); the advisory lock below serializes per account,
	// so a long replay never blocks another account's job.
	if _, err = tx.Exec(ctx, `
		SELECT set_config('statement_timeout','300s',true),
		       set_config('idle_in_transaction_session_timeout','300s',true)`); err != nil {
		return err
	}
	var requested time.Time
	err = tx.QueryRow(ctx, `
		SELECT requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1 AND status='processing' AND lease_token=$2
		FOR UPDATE`, accountID, lease).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	// XM-INV-PROOF-CONTENTION 2: the per-account advisory lock
	// (hashtextextended(...,43)) used to be acquired here, before reanchor
	// and the proof evaluation, and held for the rest of the transaction --
	// including ensureBalanceCarryForwardProofTx's evaluation, which on a
	// contended production account took tens of seconds per attempt (before
	// requirement 4's set-based rewrite) while retrying every 30s. A
	// concurrent source-projection worker event for the same account
	// (ObserveUsageEvent/ObserveCreditEvent/ObserveBalanceCheckpoint, which
	// take the identical lock) blocked for that whole duration and could
	// hit a lock timeout (see MarkSourceEventBusy's doc comment).
	//
	// Reanchor and the proof evaluation below now run against an unlocked
	// read of the account (lock=false): reanchor is a one-time, per-account
	// transition gated by bootstrap_kind, and the job-claim SKIP LOCKED
	// handoff in ProcessEligibilityProjectionJobs already guarantees only
	// one processEligibilityProjectionJob execution per account runs at a
	// time, so neither step needs the advisory lock to exclude another
	// instance of itself; SERIALIZABLE isolation still catches (and this
	// job's own backoff still retries) any genuine conflict with a
	// concurrent writer, such as a worker-side freeze, at commit. The lock
	// is acquired below, immediately before the write phase that actually
	// needs a stable, exclusive view of the account: reprojection, pending
	// evidence, and publishing finalized_through.
	account, err := getEligibilityAccountTx(ctx, tx, accountID, false)
	if err != nil {
		return err
	}
	if reanchored, err := reanchorLegacyEligibilityAccountTx(ctx, tx, account, actor); err != nil {
		return err
	} else if reanchored {
		if account, err = getEligibilityAccountTx(ctx, tx, accountID, false); err != nil {
			return err
		}
	}
	if requested.Before(account.FinalizedThrough) {
		requested = account.FinalizedThrough
	}
	if err = ensureBalanceCarryForwardProofTx(ctx, tx, account, requested, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, accountID); err != nil {
		return err
	}
	if err = reprojectEligibilityTx(ctx, tx, accountID, requested, actor); err != nil {
		return err
	}
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, requested, actor); err != nil {
		return err
	}
	// A positive checkpoint may have inserted a conservative non-cash fact at
	// the previous trusted boundary. Rebuild once more before publishing.
	if err = reprojectEligibilityTx(ctx, tx, accountID, requested, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET finalized_through=GREATEST(finalized_through,$1),projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$2`, requested, accountID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM eligibility_projection_jobs WHERE external_account_id=$1 AND lease_token=$2`, accountID, lease)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return tx.Commit(ctx)
}

func ensureBalanceCarryForwardProofTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, requested time.Time, actor AuditActor) error {
	// XM-INV-PROOF-CONTENTION 2: the caller (processEligibilityProjectionJob)
	// deliberately does NOT hold the per-account advisory lock or a row lock
	// on the account here -- only its own SERIALIZABLE snapshot, taken
	// before this call, and the job-claim SKIP LOCKED handoff that already
	// guarantees no other processEligibilityProjectionJob execution for this
	// account is concurrently running. This function only reads (the
	// carry-forward proof rows it inserts are additive and idempotent, keyed
	// by (account,cycle) -- see the ON CONFLICT DO NOTHING below -- so a
	// concurrent writer elsewhere in the account's row set cannot corrupt
	// them; the caller re-locks for the write phase that follows). A
	// published delta cycle may still contain parked checkpoints for
	// unrelated identities; those must not block this account's unchanged
	// proof. A later checkpoint for this account is rejected by the mutually
	// exclusive proof/checkpoint contract.
	if account.Status == "syncing" {
		return errBalanceCarryForwardProofPending
	}
	var unmappedFunding bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM funding_lots lot
			WHERE lot.external_account_id=$1
			  AND lot.completed_at>$2 AND lot.completed_at<=$3
			  AND lot.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
			  AND NOT EXISTS (
				SELECT 1 FROM source_events event
				-- Cast the TEXT side to uuid so this join hits the
			-- (source_instance_id, stream_id, event_id) index: the reverse
			-- cast event_id::text defeated every index and filtered ~63k
			-- rows per probe (75ms each), and a heavy account's replay makes
			-- thousands of such probes -- the first production whale wedged
			-- for hours on exactly this. The CASE guard keeps synthetic
			-- non-uuid external ids (e.g. carry-forward proof rows named
			-- "carry-cutover-event") on the original no-match semantics
			-- instead of a 22P02 cast error; CASE evaluation order is
			-- guaranteed, a bare regex AND is not.
			JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=lot.source_instance_id
				 AND mapped.stream_id='payments'
				 AND mapped.event_id=CASE WHEN event.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN event.external_event_id::uuid END
				 AND mapped.payload_hash=lot.source_revision_hash
				JOIN source_economic_scan_cycles cycle
				  ON cycle.source_instance_id=mapped.source_instance_id
				 AND cycle.stream_id=mapped.stream_id
				 AND cycle.scan_cycle_id=mapped.scan_cycle_id
				WHERE event.id=lot.source_event_id AND cycle.cycle_status='published'
			  )
		)`, account.ExternalAccountID, account.FinalizedThrough, requested).Scan(&unmappedFunding); err != nil {
		return err
	}
	if unmappedFunding {
		return errBalanceCarryForwardProofInvalid
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT visibility_at FROM (
			SELECT stream_watermark_at AS visibility_at
			FROM source_usage_events
			WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3
			UNION ALL
			SELECT stream_watermark_at
			FROM source_credit_events
			WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3
			UNION ALL
			SELECT cycle.scan_ceiling_at
			FROM funding_lots lot
			JOIN source_events event ON event.id=lot.source_event_id
			JOIN source_economic_scan_cycle_events mapped
			  ON mapped.source_instance_id=lot.source_instance_id
			 AND mapped.stream_id='payments'
			 AND mapped.event_id=CASE WHEN event.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN event.external_event_id::uuid END
			 AND mapped.payload_hash=lot.source_revision_hash
			JOIN source_economic_scan_cycles cycle
			  ON cycle.source_instance_id=mapped.source_instance_id
			 AND cycle.stream_id=mapped.stream_id
			 AND cycle.scan_cycle_id=mapped.scan_cycle_id
			WHERE lot.external_account_id=$1
			  AND lot.completed_at>$2 AND lot.completed_at<=$3
			  AND lot.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
			  AND cycle.cycle_status='published'
		) facts ORDER BY visibility_at`, account.ExternalAccountID, account.FinalizedThrough, requested)
	if err != nil {
		return err
	}
	visibilities := make([]time.Time, 0)
	for rows.Next() {
		var visibility time.Time
		if err = rows.Scan(&visibility); err != nil {
			rows.Close()
			return err
		}
		visibilities = append(visibilities, visibility.UTC())
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	type carryCandidate struct {
		cycleID, batchID, snapshotID, cursor, revision string
		priorID, balance                               string
		asOf, watermark, observed                      time.Time
		snapshotRows, sequence                         int64
		negative, baselineMember, hasRealCheckpoint    bool
	}
	// XM-INV-PROOF-CONTENTION 4: set-based evaluation, replacing what used to
	// be up to two queries issued per entry of `visibilities` (each ~42ms on
	// production, up to ~1,150 checkpoints in the contention incident's
	// window -- tens of seconds per attempt, spent holding up whatever else
	// needed this account; see the design note on
	// processEligibilityProjectionJob). Both per-visibility lookups below are
	// "smallest candidate ceiling >= some threshold" queries; since
	// `visibilities` is fixed, sorted, and deduplicated up front, prefetch
	// every candidate once (bounded by the account's own real-checkpoint and
	// the source's published-cycle counts in the window -- not by how many
	// distinct visibilities they end up covering) and walk both lists with
	// `visibilities` using an ordinary ascending two-pointer merge, computing
	// identical results to the original per-visibility queries: a
	// same-shape, forward-only pointer is valid here for the same reason a
	// merge join is valid on two sorted inputs, and this loop already
	// establishes that visibilities themselves are processed in ascending,
	// deduplicated order.
	//
	// Real-checkpoint candidates first (query A below, unbounded above like
	// the original -- only the checkpoint's own as_of is bounded by
	// `requested`, not the cycle ceiling it's tied to): a checkpoint's own
	// arrival cycle is preferred over any delta-carry cycle regardless of
	// which has the smaller ceiling, exactly as the original code's
	// try-real-then-fall-back-to-carry order did.
	realCeilings := make([]time.Time, 0, len(visibilities))
	realRows, err := tx.Query(ctx, `
		SELECT cycle.scan_ceiling_at
		FROM balance_reconciliation_checkpoints checkpoint
		JOIN source_economic_scan_cycle_events mapped
		  ON mapped.source_instance_id=checkpoint.source_instance_id
		 AND mapped.stream_id='balances'
		 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
		 AND mapped.payload_hash=checkpoint.source_revision_hash
		JOIN source_economic_scan_cycles cycle
		  ON cycle.source_instance_id=mapped.source_instance_id
		 AND cycle.stream_id=mapped.stream_id
		 AND cycle.scan_cycle_id=mapped.scan_cycle_id
		WHERE checkpoint.external_account_id=$1
		  AND checkpoint.checkpoint_kind='reconciliation'
		  AND checkpoint.as_of>$2 AND checkpoint.as_of<=$3
		  AND cycle.cycle_status='published' AND cycle.scan_ceiling_at>=$2
		ORDER BY cycle.scan_ceiling_at,cycle.first_sequence`,
		account.ExternalAccountID, account.FinalizedThrough, requested.UTC())
	if err != nil {
		return err
	}
	for realRows.Next() {
		var ceiling time.Time
		if err = realRows.Scan(&ceiling); err != nil {
			realRows.Close()
			return err
		}
		realCeilings = append(realCeilings, ceiling.UTC())
	}
	if err = realRows.Err(); err != nil {
		realRows.Close()
		return err
	}
	realRows.Close()

	// Delta-carry candidates (query B below): every published balances cycle
	// in the window, each with its own prior-checkpoint delta and
	// has_real_checkpoint flag precomputed -- identical per-row logic to the
	// original, just evaluated for every candidate cycle once instead of
	// only for the ones an earlier per-visibility query happened to land on.
	carryCandidates := make([]carryCandidate, 0, len(visibilities))
	carryRows, err := tx.Query(ctx, `
		SELECT cycle.scan_cycle_id::text,batch.batch_id::text,cycle.scan_snapshot_id,
			cycle.scan_snapshot_row_count,cycle.scan_ceiling_at,cycle.stream_watermark_at,
			cycle.source_cursor,cycle.final_sequence,batch.body_hash,batch.source_captured_at,
			prior.id::text,prior.balance_service_units::text,prior.balance_negative,prior.baseline_member,
			EXISTS (
				SELECT 1 FROM balance_reconciliation_checkpoints checkpoint
				JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=checkpoint.source_instance_id
				 AND mapped.stream_id='balances'
				 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
				 AND mapped.payload_hash=checkpoint.source_revision_hash
				WHERE checkpoint.external_account_id=$1
				  AND mapped.scan_cycle_id=cycle.scan_cycle_id
			) AS has_real_checkpoint
		FROM source_economic_scan_cycles cycle
		JOIN source_ingest_batches batch
		  ON batch.source_instance_id=cycle.source_instance_id
		 AND batch.stream_id=cycle.stream_id
		 AND batch.scan_cycle_id=cycle.scan_cycle_id
		 AND batch.sequence=cycle.final_sequence
		JOIN LATERAL (
			SELECT checkpoint.id,checkpoint.balance_service_units,
				checkpoint.balance_negative,checkpoint.baseline_member
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<cycle.scan_ceiling_at
			       OR (checkpoint.as_of=cycle.scan_ceiling_at
			           AND checkpoint.source_sequence<cycle.final_sequence))
			ORDER BY checkpoint.as_of DESC,checkpoint.source_sequence DESC,checkpoint.id DESC LIMIT 1
		) prior ON true
		WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
		  AND cycle.cycle_status='published'
		  AND cycle.scan_ceiling_at>=$3 AND cycle.scan_ceiling_at<=$4
		ORDER BY cycle.scan_ceiling_at,cycle.first_sequence`,
		account.ExternalAccountID, account.SourceInstanceID, account.FinalizedThrough, requested.UTC())
	if err != nil {
		return err
	}
	for carryRows.Next() {
		var item carryCandidate
		if err = carryRows.Scan(&item.cycleID, &item.batchID, &item.snapshotID, &item.snapshotRows,
			&item.asOf, &item.watermark, &item.cursor, &item.sequence, &item.revision,
			&item.observed, &item.priorID, &item.balance, &item.negative, &item.baselineMember,
			&item.hasRealCheckpoint); err != nil {
			carryRows.Close()
			return err
		}
		item.asOf, item.watermark, item.observed = item.asOf.UTC(), item.watermark.UTC(), item.observed.UTC()
		carryCandidates = append(carryCandidates, item)
	}
	if err = carryRows.Err(); err != nil {
		carryRows.Close()
		return err
	}
	carryRows.Close()

	coveredVisibility := account.FinalizedThrough.UTC()
	realIndex, carryIndex := 0, 0
	for _, visibility := range visibilities {
		if !visibility.After(coveredVisibility) {
			continue
		}
		// A real signed account checkpoint keeps its established semantics: its
		// own as_of must be finalizable, while the enclosing published cycle must
		// be at or after the fact's receiver visibility. Delta carry proofs are
		// stricter below because they derive as_of from the cycle ceiling itself.
		for realIndex < len(realCeilings) && realCeilings[realIndex].Before(visibility) {
			realIndex++
		}
		if realIndex < len(realCeilings) {
			coveredVisibility = realCeilings[realIndex]
			continue
		}
		for carryIndex < len(carryCandidates) && carryCandidates[carryIndex].asOf.Before(visibility) {
			carryIndex++
		}
		if carryIndex >= len(carryCandidates) {
			return errBalanceCarryForwardProofPending
		}
		item := carryCandidates[carryIndex]
		coveredVisibility = item.asOf
		if item.hasRealCheckpoint {
			continue
		}
		proofKey := "carry-forward:" + item.cycleID + ":" + strings.ToLower(account.ExternalAccountID)
		proofID := randomUUID()
		command, insertErr := tx.Exec(ctx, `
			INSERT INTO balance_carry_forward_proofs(
				id,source_instance_id,external_account_id,proof_key,prior_checkpoint_id,
				scan_cycle_id,final_batch_id,as_of,balance_service_units,balance_negative,
				baseline_member,source_snapshot_id,snapshot_row_count,source_sequence,
				source_cursor,stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT(external_account_id,scan_cycle_id) DO NOTHING`, proofID,
			account.SourceInstanceID, account.ExternalAccountID, proofKey, item.priorID,
			item.cycleID, item.batchID, item.asOf.UTC(), item.balance, item.negative,
			item.baselineMember, item.snapshotID, item.snapshotRows, item.sequence,
			item.cursor, item.watermark.UTC(), item.revision, item.observed.UTC())
		if insertErr != nil {
			return insertErr
		}
		if command.RowsAffected() == 0 {
			var existingKey, existingPrior, existingRevision string
			if err = tx.QueryRow(ctx, `SELECT proof_key,prior_checkpoint_id::text,source_revision_hash
				FROM balance_carry_forward_proofs
				WHERE external_account_id=$1 AND scan_cycle_id=$2::uuid`,
				account.ExternalAccountID, item.cycleID).Scan(&existingKey, &existingPrior, &existingRevision); err != nil {
				return err
			}
			if existingKey != proofKey || existingPrior != item.priorID || existingRevision != item.revision {
				return domain.ErrConflict
			}
			continue
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.balance_carry_forward.derived",
			"balance_carry_forward_proof", proofID, nil, map[string]any{
				"proof_key": proofKey, "scan_cycle_id": item.cycleID,
				"prior_checkpoint_id": item.priorID, "source_revision": item.revision,
			}); err != nil {
			return err
		}
	}
	return nil
}

func applyPrePolicyWalletFundingTx(
	ctx context.Context,
	tx pgx.Tx,
	lotID, accountID string,
	account eligibilityAccount,
	in SourceObservation,
	cashUnits *big.Int,
	causalOrder *big.Int,
	actor AuditActor,
) error {
	var existingKind domain.EligibilityKind
	var verification domain.VerificationState
	var currentCap, reserved, issued int64
	if err := tx.QueryRow(ctx, `
		SELECT eligibility_kind,verification_state,current_cap_minor,reserved_minor,issued_minor
		FROM funding_lots WHERE id=$1 FOR UPDATE`, lotID).Scan(
		&existingKind, &verification, &currentCap, &reserved, &issued); err != nil {
		return err
	}
	if (existingKind != domain.EligibilityLegacyNonInvoiceable && existingKind != domain.EligibilityNonCash) ||
		reserved != 0 || issued != 0 {
		return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
			in.Lot.SourceRevision, actor)
	}
	verifiedHistoricalCash := int64(0)
	if verification == domain.VerificationVerified {
		verifiedHistoricalCash = currentCap
	}
	command, err := tx.Exec(ctx, `
		UPDATE funding_lots SET eligibility_kind='NON_CASH',eligibility_cutover_at=$1,
			verified_cash_minor=$2,consumed_cash_minor=0,
			eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE id=$3 AND eligibility_kind IN ('LEGACY_NON_INVOICEABLE','NON_CASH')
			AND reserved_minor=0 AND issued_minor=0`, account.PolicyStartAt, verifiedHistoricalCash, lotID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	causalOrderText := ""
	if causalOrder != nil {
		causalOrderText = causalOrder.String()
	}
	var existingUnits, existingUnitCode, existingManifest, existingConfiguration string
	var existingAt time.Time
	var existingDomain, existingOrder string
	err = tx.QueryRow(ctx, `
		SELECT service_units::text,event_time,unit_code,cutover_manifest_hash,
			configuration_hash,COALESCE(causal_domain,''),COALESCE(causal_order::text,'')
		FROM source_credit_events
		WHERE funding_lot_id=$1 AND credit_kind='PRE_POLICY_NON_INVOICEABLE'`, lotID).Scan(&existingUnits, &existingAt, &existingUnitCode,
		&existingManifest, &existingConfiguration, &existingDomain, &existingOrder)
	if err == nil {
		if existingUnits != cashUnits.String() || !existingAt.Equal(in.Lot.CompletedAt.UTC()) ||
			existingUnitCode != in.WalletUnitCode || existingManifest != in.CutoverManifestHash ||
			existingConfiguration != in.ConfigurationHash || existingDomain != in.CausalDomain ||
			existingOrder != causalOrderText {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	derivedID := randomUUID()
	derivedExternalID := "pre-policy:" + lotID
	_, err = tx.Exec(ctx, `
		INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			funding_lot_id,event_time,service_units,unit_code,cutover_manifest_hash,
			configuration_hash,credit_kind,causal_domain,causal_order,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,$5,$6,$7::numeric,$8,$9,$10,
			'PRE_POLICY_NON_INVOICEABLE',NULLIF($11,''),$12::numeric,$13,$14,$15,$16,$17)`,
		derivedID, in.Lot.SourceInstanceID, accountID, derivedExternalID, lotID,
		in.Lot.CompletedAt.UTC(), cashUnits.String(), in.WalletUnitCode,
		in.CutoverManifestHash, in.ConfigurationHash, in.CausalDomain,
		nullableBigInt(causalOrder), in.SourceSequence, in.SourceCursor,
		in.StreamWatermarkAt.UTC(), in.Lot.SourceRevision, in.Lot.ObservedAt.UTC())
	if err != nil {
		return err
	}
	if !in.Lot.CompletedAt.After(account.FinalizedThrough) {
		return freezeEligibilityTx(ctx, tx, accountID, lotID, "LATE_FINALIZED_EVENT", "funding_lot", lotID,
			in.Lot.SourceRevision, actor)
	}
	return writeAudit(ctx, tx, actor, "eligibility.pre_policy_funding_classified", "funding_lot", lotID,
		nil, map[string]any{"eligibility_start_at": account.PolicyStartAt, "credit_event_id": derivedID})
}

func evaluatePendingBalanceEvidenceTx(ctx context.Context, tx pgx.Tx, accountID string, through time.Time, actor AuditActor) error {
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT evidence_kind,id,evidence_key,external_event_id,as_of,balance_service_units,
			balance_negative,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at
		FROM (
			SELECT 'real'::text AS evidence_kind,checkpoint.id,
				checkpoint.checkpoint_id AS evidence_key,checkpoint.external_event_id,
				checkpoint.as_of,checkpoint.balance_service_units::text AS balance_service_units,
				checkpoint.balance_negative,checkpoint.source_sequence,checkpoint.source_cursor,
				checkpoint.stream_watermark_at,checkpoint.source_revision_hash,checkpoint.observed_at
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
			  AND checkpoint.as_of<=$2
			  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations evaluation
				WHERE evaluation.checkpoint_id=checkpoint.id)
			UNION ALL
			SELECT 'carry'::text,proof.id,proof.proof_key,proof.proof_key,
				proof.as_of,proof.balance_service_units::text,proof.balance_negative,
				proof.source_sequence,proof.source_cursor,proof.stream_watermark_at,
				proof.source_revision_hash,proof.observed_at
			FROM balance_carry_forward_proofs proof
			WHERE proof.external_account_id=$1 AND proof.as_of<=$2
			  AND NOT EXISTS (SELECT 1 FROM balance_carry_forward_evaluations evaluation
				WHERE evaluation.proof_id=proof.id)
		) pending
		ORDER BY as_of,source_sequence,
			CASE evidence_kind WHEN 'real' THEN 0 ELSE 1 END,id`, accountID, through)
	if err != nil {
		return err
	}
	type proof struct {
		kind, id, key, externalEventID string
		balanceText, cursor, revision  string
		asOf, watermark, observed      time.Time
		sequence                       int64
		balanceNegative                bool
	}
	items := make([]proof, 0)
	for rows.Next() {
		var item proof
		if err = rows.Scan(&item.kind, &item.id, &item.key, &item.externalEventID,
			&item.asOf, &item.balanceText,
			&item.balanceNegative, &item.sequence, &item.cursor, &item.watermark,
			&item.revision, &item.observed); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		balance, parseErr := parseUnsignedUnits(item.balanceText, "carry-forward balance", true)
		if parseErr != nil {
			return parseErr
		}
		projection, projectionErr := buildEligibilityProjectionTx(ctx, tx, account, item.asOf)
		if projectionErr != nil {
			return projectionErr
		}
		difference := new(big.Int).Sub(new(big.Int).Set(balance), projection.ExpectedBalance)
		status := "matched"
		objectType := "balance_checkpoint"
		if item.kind == "carry" {
			objectType = "balance_carry_forward_proof"
		}
		if item.balanceNegative {
			status = "negative_frozen"
			if err = freezeEligibilityTx(ctx, tx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE",
				objectType, item.key, item.revision, actor); err != nil {
				return err
			}
		} else {
			switch difference.Sign() {
			case -1:
				status = "negative_frozen"
				if err = freezeEligibilityTx(ctx, tx, accountID, "", "UNKNOWN_NEGATIVE_BALANCE",
					objectType, item.key, item.revision, actor); err != nil {
					return err
				}
			case 1:
				var intervalStart time.Time
				err = tx.QueryRow(ctx, `
					SELECT max(q.as_of) FROM (
						SELECT checkpoint.as_of FROM balance_reconciliation_checkpoints checkpoint
						WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='cutover'
						  AND (checkpoint.as_of<$2 OR (checkpoint.as_of=$2 AND checkpoint.source_sequence<$3))
						UNION ALL
						SELECT checkpoint.as_of FROM balance_reconciliation_checkpoints checkpoint
						JOIN balance_checkpoint_evaluations evaluation ON evaluation.checkpoint_id=checkpoint.id
						WHERE checkpoint.external_account_id=$1
						  AND (checkpoint.as_of<$2 OR (checkpoint.as_of=$2 AND checkpoint.source_sequence<$3))
						  AND evaluation.evaluation_status IN ('matched','positive_classified_non_cash')
						UNION ALL
						SELECT proof.as_of FROM balance_carry_forward_proofs proof
						JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
						WHERE proof.external_account_id=$1
						  AND (proof.as_of<$2 OR (proof.as_of=$2 AND proof.source_sequence<$3))
						  AND evaluation.evaluation_status IN ('matched','positive_classified_non_cash')
					) q`, accountID, item.asOf, item.sequence).Scan(&intervalStart)
				if err != nil || intervalStart.IsZero() {
					status = "source_gap_frozen"
					if err = freezeEligibilityTx(ctx, tx, accountID, "", "SOURCE_GAP",
						objectType, item.key, item.revision, actor); err != nil {
						return err
					}
				} else {
					status = "positive_classified_non_cash"
					_, err = tx.Exec(ctx, `
						INSERT INTO source_credit_events(
							id,source_instance_id,external_account_id,external_event_id,external_credit_id,
							event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
							credit_kind,source_sequence,source_cursor,stream_watermark_at,
							source_revision_hash,observed_at)
						VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,
							'UNKNOWN_POSITIVE',$11,$12,$13,$14,$15)
						ON CONFLICT(source_instance_id,external_event_id) DO NOTHING`, randomUUID(),
						account.SourceInstanceID, accountID, "unknown-positive:"+item.externalEventID,
						"unknown-positive:"+item.id, intervalStart.UTC(), difference.String(),
						account.UnitCode, account.ManifestHash, account.ConfigurationHash,
						item.sequence, item.cursor, item.watermark.UTC(), item.revision, item.observed.UTC())
					if err != nil {
						return err
					}
				}
			}
		}
		if item.kind == "carry" {
			_, err = tx.Exec(ctx, `
				INSERT INTO balance_carry_forward_evaluations(
					id,proof_id,projection_version,expected_service_units,
					difference_service_units,evaluation_status)
				VALUES($1,$2,$3,$4::numeric,$5::numeric,$6)`, randomUUID(), item.id,
				account.Version+1, projection.ExpectedBalance.String(), difference.String(), status)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO balance_checkpoint_evaluations(
					id,checkpoint_id,projection_version,expected_service_units,
					difference_service_units,evaluation_status)
				VALUES($1,$2,$3,$4::numeric,$5::numeric,$6)`, randomUUID(), item.id,
				account.Version+1, projection.ExpectedBalance.String(), difference.String(), status)
		}
		if err != nil {
			return err
		}
		action := "eligibility.balance_checkpoint.evaluated"
		if item.kind == "carry" {
			action = "eligibility.balance_carry_forward.evaluated"
		}
		if err = writeAudit(ctx, tx, actor, action,
			objectType, item.id, nil, map[string]any{
				"status": status, "evidence_key": item.key,
			}); err != nil {
			return err
		}
	}
	return nil
}
