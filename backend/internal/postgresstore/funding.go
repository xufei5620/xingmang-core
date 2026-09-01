package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

func validSourceEventKind(value string) bool {
	switch value {
	case "payment", "refund", "identity", "usage_summary", "tombstone":
		return true
	default:
		return false
	}
}

func (s *Store) FundingSourceVersion(ctx context.Context, sourceInstanceID, externalOrderID string) (time.Time, int64, error) {
	var observedAt time.Time
	var sequence int64
	err := s.pool.QueryRow(ctx, `
		SELECT source_last_observed_at,source_last_sequence FROM funding_lots
		WHERE source_instance_id=$1 AND external_order_id=$2`, sourceInstanceID, externalOrderID).Scan(&observedAt, &sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, 0, domain.ErrNotFound
	}
	return observedAt, sequence, err
}

func (s *Store) AuditStaleFundingProjection(ctx context.Context, sourceInstanceID, externalOrderID string, ignoredTime time.Time, ignoredSequence int64, actor AuditActor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var lotID string
	var currentTime time.Time
	var currentSequence int64
	if err = tx.QueryRow(ctx, `
		SELECT id,source_last_observed_at,source_last_sequence FROM funding_lots
		WHERE source_instance_id=$1 AND external_order_id=$2`, sourceInstanceID, externalOrderID).Scan(
		&lotID, &currentTime, &currentSequence); err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "funding_lot.stale_source_event_ignored", "funding_lot", lotID,
		map[string]any{"source_time": currentTime, "source_sequence": currentSequence},
		map[string]any{"ignored_source_time": ignoredTime, "ignored_source_sequence": ignoredSequence}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ObserveFundingLot records the immutable source event and updates the funding
// projection in one transaction.  If a refund makes issued+reserved money
// exceed the new cap, every affected request is conservatively frozen for
// manual refund/red-invoice attention.
func (s *Store) ObserveFundingLot(ctx context.Context, in SourceObservation, actor AuditActor) (ObservationResult, error) {
	lot := in.Lot
	observedVerification := lot.Verification
	manualNewAPICap := in.NewAPIManualCeilingMinor != nil
	manualApprovedGrossMinor := int64(0)
	normalizedActor := actor.normalized()
	if lot.ID == "" {
		lot.ID = randomUUID()
	}
	if lot.PrincipalID == "" || lot.SourceInstanceID == "" || lot.ExternalOrderID == "" ||
		strings.TrimSpace(in.ExternalUserID) == "" || !validSourceEventKind(in.EventKind) ||
		strings.TrimSpace(in.ExternalEventID) == "" || strings.TrimSpace(in.SchemaVersion) == "" ||
		strings.TrimSpace(lot.SourceRevision) == "" {
		return ObservationResult{}, errors.New("complete source observation identity is required")
	}
	if manualNewAPICap && (lot.SourceType != domain.SourceNewAPI || in.EventKind != "refund" ||
		normalizedActor.Type != "admin" || strings.TrimSpace(normalizedActor.ID) == "" ||
		*in.NewAPIManualCeilingMinor < 0 || lot.CurrentCapMinor != *in.NewAPIManualCeilingMinor) {
		return ObservationResult{}, errors.New("invalid New API manual funding ceiling observation")
	}
	if lot.Currency == "" {
		lot.Currency = domain.CurrencyCNY
	}
	if lot.ObservedAt.IsZero() {
		lot.ObservedAt = time.Now().UTC()
	}
	if lot.UpdatedAt.IsZero() {
		lot.UpdatedAt = lot.ObservedAt
	}
	if err := lot.Validate(); err != nil {
		return ObservationResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lockKey := lot.SourceInstanceID + "\n" + lot.ExternalOrderID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,3))`, lockKey); err != nil {
		return ObservationResult{}, fmt.Errorf("lock source order: %w", err)
	}
	sourceVersionTime := in.SourceUpdatedAt.UTC()
	if sourceVersionTime.IsZero() {
		sourceVersionTime = lot.ObservedAt.UTC()
	}
	var currentSourceTime time.Time
	var currentSourceSequence int64
	versionErr := tx.QueryRow(ctx, `
		SELECT source_last_observed_at,source_last_sequence FROM funding_lots
		WHERE source_instance_id=$1 AND external_order_id=$2 FOR UPDATE`,
		lot.SourceInstanceID, lot.ExternalOrderID).Scan(&currentSourceTime, &currentSourceSequence)
	if versionErr != nil && !errors.Is(versionErr, pgx.ErrNoRows) {
		return ObservationResult{}, versionErr
	}
	if !manualNewAPICap && normalizedActor.Type == "source_connector" && versionErr == nil &&
		(sourceVersionTime.Before(currentSourceTime) || sourceVersionTime.Equal(currentSourceTime) && in.SourceSequence <= currentSourceSequence) {
		existing, getErr := getFundingLotTx(ctx, tx, lot.SourceInstanceID, lot.ExternalOrderID, "", "")
		if getErr != nil {
			return ObservationResult{}, getErr
		}
		if err = writeAudit(ctx, tx, actor, "funding_lot.stale_source_event_ignored", "funding_lot", existing.ID,
			map[string]any{"source_time": currentSourceTime, "source_sequence": currentSourceSequence},
			map[string]any{"ignored_source_time": sourceVersionTime, "ignored_source_sequence": in.SourceSequence}); err != nil {
			return ObservationResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return ObservationResult{}, err
		}
		return ObservationResult{Lot: existing, Duplicate: true}, nil
	}
	var sourceType domain.SourceType
	var externalAccountID string
	err = tx.QueryRow(ctx, `
		SELECT si.source_type,ea.id
		FROM source_instances si
		JOIN external_accounts ea ON ea.source_instance_id=si.id
		WHERE si.id=$1 AND si.enabled AND ea.invoice_user_id=$2
			AND ea.external_user_id=$3
			AND (ea.binding_status='verified' OR $4='tombstone')
		FOR SHARE OF si,ea`,
		lot.SourceInstanceID, lot.PrincipalID, in.ExternalUserID, in.EventKind).Scan(&sourceType, &externalAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ObservationResult{}, domain.ErrForbidden
	}
	if err != nil {
		return ObservationResult{}, fmt.Errorf("resolve verified external account: %w", err)
	}
	if sourceType != lot.SourceType {
		return ObservationResult{}, domain.ErrSourceMixing
	}

	eventID := randomUUID()
	err = tx.QueryRow(ctx, `
		INSERT INTO source_events(
			id,source_instance_id,external_user_id,event_kind,external_event_id,
			source_status,schema_version,source_revision_hash,payload_ciphertext,
			source_created_at,source_updated_at,observed_at,source_sequence)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT(source_instance_id,event_kind,external_event_id,source_revision_hash) DO NOTHING
		RETURNING id`, eventID, lot.SourceInstanceID, in.ExternalUserID, in.EventKind,
		in.ExternalEventID, lot.SourceStatus, in.SchemaVersion, lot.SourceRevision,
		nullableBytes(in.PayloadCiphertext), optionalTime(in.SourceCreatedAt),
		optionalTime(sourceVersionTime), lot.ObservedAt, in.SourceSequence).Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := getFundingLotTx(ctx, tx, lot.SourceInstanceID, lot.ExternalOrderID, "", "")
		if getErr != nil {
			return ObservationResult{}, fmt.Errorf("duplicate source event has no funding projection: %w", getErr)
		}
		if err = tx.Commit(ctx); err != nil {
			return ObservationResult{}, err
		}
		return ObservationResult{Lot: existing, Duplicate: true}, nil
	}
	if err != nil {
		return ObservationResult{}, fmt.Errorf("insert source event: %w", err)
	}

	existing, getErr := getFundingLotTx(ctx, tx, lot.SourceInstanceID, lot.ExternalOrderID, "", "FOR UPDATE")
	isNew := errors.Is(getErr, domain.ErrNotFound)
	if getErr != nil && !isNew {
		return ObservationResult{}, getErr
	}
	before := existing
	nextSourceTime, nextSourceSequence := sourceVersionTime, in.SourceSequence
	trackedEligibilityKind := existing.EligibilityKind == domain.EligibilityWalletCash ||
		existing.EligibilityKind == domain.EligibilitySubscriptionCash ||
		existing.EligibilityKind == domain.EligibilityNonCash
	refundObservation := manualNewAPICap ||
		(in.EventKind == "refund" || in.EventKind == "tombstone") &&
			trackedEligibilityKind ||
		trackedEligibilityKind &&
			existing.VerifiedCashMinor > 0 && lot.CurrentCapMinor < existing.VerifiedCashMinor
	if refundObservation {
		nextSourceTime, nextSourceSequence = currentSourceTime, currentSourceSequence
	}
	if isNew {
		if manualNewAPICap {
			return ObservationResult{}, domain.ErrInvalidState
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_lots(
				id,invoice_user_id,external_account_id,source_instance_id,source_event_id,
				external_order_id,trade_no,currency,original_minor,current_cap_minor,
				reserved_minor,issued_minor,verification_state,source_status,
				source_revision_hash,completed_at,observed_at,updated_at,
				source_last_observed_at,source_last_sequence)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,0,$11,$12,$13,$14,$15,$16,$17,$18)`,
			lot.ID, lot.PrincipalID, externalAccountID, lot.SourceInstanceID, eventID,
			lot.ExternalOrderID, lot.TradeNo, lot.Currency, lot.OriginalMinor, lot.CurrentCapMinor,
			lot.Verification, lot.SourceStatus, lot.SourceRevision, optionalTime(lot.CompletedAt),
			lot.ObservedAt, lot.UpdatedAt, sourceVersionTime, in.SourceSequence)
		if err != nil {
			return ObservationResult{}, fmt.Errorf("insert funding lot: %w", err)
		}
	} else {
		if existing.PrincipalID != lot.PrincipalID || existing.SourceInstanceID != lot.SourceInstanceID || existing.SourceType != lot.SourceType {
			return ObservationResult{}, domain.ErrForbidden
		}
		// original_minor is historical paid value and must never shrink.  A
		// refund reduces current_cap_minor instead.
		if lot.OriginalMinor < existing.OriginalMinor {
			lot.OriginalMinor = existing.OriginalMinor
		}
		lot.ID = existing.ID
		lot.ReservedMinor = existing.ReservedMinor
		lot.IssuedMinor = existing.IssuedMinor
		if lot.CompletedAt.IsZero() {
			lot.CompletedAt = existing.CompletedAt
		} else if !existing.CompletedAt.IsZero() &&
			!lot.CompletedAt.UTC().Truncate(time.Microsecond).Equal(existing.CompletedAt.UTC()) {
			// completed_at is the financial policy boundary fact. Once observed it
			// cannot move, even to another timestamp on the same side of the
			// boundary. Preserve the original projection, retain the new source
			// event as evidence, and fail the entire account closed.
			lot.CompletedAt = existing.CompletedAt
			if existing.IssuedMinor > 0 {
				attention := &projectedLot{ID: existing.ID, OldMinor: existing.CurrentCapMinor,
					RoundedMinor: existing.CurrentCapMinor, IssuedMinor: existing.IssuedMinor,
					ReservedMinor: existing.ReservedMinor}
				if err = markLotIssuedAttentionTx(ctx, tx, attention, actor); err != nil {
					return ObservationResult{}, err
				}
			}
			if err = freezeEligibilityTx(ctx, tx, externalAccountID, existing.ID, "EVENT_PAYLOAD_DRIFT",
				"funding_lot.completed_at", existing.ID, lot.SourceRevision, actor); err != nil {
				return ObservationResult{}, err
			}
			if err = writeAudit(ctx, tx, actor, "funding_lot.completed_at_drift_rejected", "funding_lot", existing.ID,
				map[string]any{"completed_at": existing.CompletedAt},
				map[string]any{"rejected_completed_at": in.Lot.CompletedAt}); err != nil {
				return ObservationResult{}, err
			}
			frozen, getErr := getFundingLotTx(ctx, tx, "", "", existing.ID, "FOR UPDATE")
			if getErr != nil {
				return ObservationResult{}, getErr
			}
			if err = tx.Commit(ctx); err != nil {
				return ObservationResult{}, err
			}
			return ObservationResult{Lot: frozen}, domain.ErrConflict
		}
		if manualNewAPICap {
			if existing.SourceType != domain.SourceNewAPI || *in.NewAPIManualCeilingMinor >= existing.CurrentCapMinor {
				return ObservationResult{}, domain.ErrInvalidState
			}
			if err = tx.QueryRow(ctx, `
				SELECT COALESCE(MAX(paid_minor),-1)
				FROM payment_candidate_reviews
				WHERE funding_lot_id=$1 AND review_action='verify_approve'`, lot.ID).
				Scan(&manualApprovedGrossMinor); err != nil {
				return ObservationResult{}, err
			}
			if manualApprovedGrossMinor <= 0 || *in.NewAPIManualCeilingMinor > manualApprovedGrossMinor {
				return ObservationResult{}, domain.ErrInvalidState
			}
			lot.OriginalMinor = existing.OriginalMinor
			lot.CurrentCapMinor = *in.NewAPIManualCeilingMinor
			lot.Verification = domain.VerificationFrozen
			observedVerification = domain.VerificationFrozen
		} else if existing.SourceType == domain.SourceNewAPI &&
			(existing.Verification == domain.VerificationVerified || existing.Verification == domain.VerificationFrozen) &&
			observedVerification == domain.VerificationPending {
			// A repeated New API candidate remains non-authoritative after an
			// administrator has independently verified settlement.  It may
			// refresh source metadata but can never rewrite the reviewed amount.
			lot.OriginalMinor = existing.OriginalMinor
			lot.CurrentCapMinor = existing.CurrentCapMinor
		}
		if existing.Verification == domain.VerificationFrozen ||
			(existing.Verification == domain.VerificationVerified && lot.Verification == domain.VerificationPending) {
			lot.Verification = existing.Verification
		}
		if lot.CurrentCapMinor < lot.ReservedMinor+lot.IssuedMinor {
			lot.Verification = domain.VerificationFrozen
		}
		if err = lot.Validate(); err != nil {
			return ObservationResult{}, err
		}
		_, err = tx.Exec(ctx, `
			UPDATE funding_lots SET
				source_event_id=$1,trade_no=$2,original_minor=$3,current_cap_minor=$4,
				verification_state=$5,source_status=$6,source_revision_hash=$7,
				completed_at=$8,observed_at=$9,updated_at=$10,
				source_last_observed_at=$11,source_last_sequence=$12
			WHERE id=$13`, eventID, lot.TradeNo, lot.OriginalMinor, lot.CurrentCapMinor,
			lot.Verification, lot.SourceStatus, lot.SourceRevision, optionalTime(lot.CompletedAt),
			lot.ObservedAt, lot.UpdatedAt, nextSourceTime, nextSourceSequence, lot.ID)
		if err != nil {
			return ObservationResult{}, fmt.Errorf("update funding lot projection: %w", err)
		}
		if manualNewAPICap {
			if _, err = tx.Exec(ctx, `
				UPDATE funding_lots
				SET newapi_manual_ceiling_minor=LEAST(
					COALESCE(newapi_manual_ceiling_minor,original_minor),$1),
					updated_at=now()
				WHERE id=$2`, *in.NewAPIManualCeilingMinor, lot.ID); err != nil {
				return ObservationResult{}, fmt.Errorf("persist New API manual ceiling: %w", err)
			}
			if _, err = tx.Exec(ctx, `DELETE FROM payment_candidate_decisions WHERE funding_lot_id=$1 AND state='proposed'`, lot.ID); err != nil {
				return ObservationResult{}, fmt.Errorf("invalidate payment decision after manual adjustment: %w", err)
			}
		}
	}
	if err = applyFundingObservationEligibilityTx(ctx, tx, lot.ID, externalAccountID, in, actor); err != nil {
		return ObservationResult{}, fmt.Errorf("apply consumption eligibility: %w", err)
	}
	refundInvalidatedIDs := make([]string, 0)
	refundAttentionIDs := make([]string, 0)
	if refundObservation || in.EventKind == "refund" || in.EventKind == "tombstone" {
		rows, listErr := tx.Query(ctx, `
			SELECT ir.id,ia.allocation_state
			FROM invoice_allocations ia JOIN invoice_requests ir ON ir.id=ia.invoice_request_id
			WHERE ia.funding_lot_id=$1 AND ia.allocation_state IN ('reserved','issued','refund_attention')
			ORDER BY ir.id`, lot.ID)
		if listErr != nil {
			return ObservationResult{}, listErr
		}
		for rows.Next() {
			var requestID, allocationState string
			if err = rows.Scan(&requestID, &allocationState); err != nil {
				rows.Close()
				return ObservationResult{}, err
			}
			if allocationState == "reserved" {
				refundInvalidatedIDs = append(refundInvalidatedIDs, requestID)
			} else {
				refundAttentionIDs = append(refundAttentionIDs, requestID)
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return ObservationResult{}, err
		}
		rows.Close()
		if _, err = tx.Exec(ctx, `
			UPDATE funding_lots SET refund_frozen=TRUE,eligibility_revision=eligibility_revision+1,updated_at=now()
			WHERE id=$1`, lot.ID); err != nil {
			return ObservationResult{}, err
		}
		if existing.EligibilityKind == domain.EligibilityNonCash {
			err = freezeEligibilityTx(ctx, tx, externalAccountID, lot.ID, "SOURCE_REFUND",
				"funding_lot", lot.ID, lot.SourceRevision, actor)
		} else {
			err = freezeRefundedLotTx(ctx, tx, externalAccountID, lot.ID, existing.ConsumedCashMinor,
				lot.SourceRevision, actor)
		}
		if err != nil {
			return ObservationResult{}, err
		}
	}
	lot, err = getFundingLotTx(ctx, tx, "", "", lot.ID, "FOR UPDATE")
	if err != nil {
		return ObservationResult{}, err
	}

	attentionIDs := append([]string(nil), refundAttentionIDs...)
	invalidatedIDs := append([]string(nil), refundInvalidatedIDs...)
	if manualNewAPICap || lot.CurrentCapMinor < lot.ReservedMinor+lot.IssuedMinor {
		// First invalidate every not-yet-issued request touching the reduced
		// lot and release all of that request's reservations across all lots.
		// A New API manual adjustment always does this because it invalidates
		// the approval itself, even when the lower cap could still cover the
		// reservation numerically. This prevents a refund race or frozen lot
		// from being issued through an old approval.
		rows, queryErr := tx.Query(ctx, `
			SELECT ir.id,ir.status,ir.version
			FROM invoice_allocations ia JOIN invoice_requests ir ON ir.id=ia.invoice_request_id
			WHERE ia.funding_lot_id=$1 AND ia.allocation_state='reserved'
				AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
			ORDER BY ir.id FOR UPDATE OF ir`, lot.ID)
		if queryErr != nil {
			return ObservationResult{}, fmt.Errorf("lock refund-invalidated requests: %w", queryErr)
		}
		type invalidatedRequest struct {
			id      string
			status  domain.RequestStatus
			version int64
		}
		invalidated := make([]invalidatedRequest, 0)
		for rows.Next() {
			var item invalidatedRequest
			if err = rows.Scan(&item.id, &item.status, &item.version); err != nil {
				rows.Close()
				return ObservationResult{}, err
			}
			invalidated = append(invalidated, item)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return ObservationResult{}, err
		}
		rows.Close()
		for _, item := range invalidated {
			if err = releaseReservations(ctx, tx, item.id); err != nil {
				return ObservationResult{}, err
			}
			command, updateErr := tx.Exec(ctx, `
				UPDATE invoice_requests SET status='rejected',
					review_note='source refund invalidated this unissued request',
					version=version+1,updated_at=now()
				WHERE id=$1 AND version=$2`, item.id, item.version)
			if updateErr != nil {
				return ObservationResult{}, updateErr
			}
			if command.RowsAffected() != 1 {
				return ObservationResult{}, domain.ErrVersionConflict
			}
			invalidatedIDs = append(invalidatedIDs, item.id)
			if err = writeAudit(ctx, tx, actor, "invoice_request.invalidated_by_refund", "invoice_request", item.id,
				map[string]any{"status": item.status}, map[string]any{"status": domain.StatusRejected}); err != nil {
				return ObservationResult{}, err
			}
		}

		// Re-read the counters after releases.  Only if issued money still
		// exceeds the cap do issued invoices need refund/red-invoice attention.
		lot, err = getFundingLotTx(ctx, tx, "", "", lot.ID, "FOR UPDATE")
		if err != nil {
			return ObservationResult{}, err
		}
		if lot.CurrentCapMinor >= lot.ReservedMinor+lot.IssuedMinor &&
			before.Verification != domain.VerificationFrozen &&
			lot.SourceType == domain.SourceSub2API && observedVerification == domain.VerificationVerified {
			if _, err = tx.Exec(ctx, `UPDATE funding_lots SET verification_state='verified',updated_at=now() WHERE id=$1`, lot.ID); err != nil {
				return ObservationResult{}, err
			}
			lot.Verification = domain.VerificationVerified
		}
		if lot.CurrentCapMinor < lot.ReservedMinor+lot.IssuedMinor {
			rows, queryErr = tx.Query(ctx, `
				SELECT ir.id,ir.status,ir.version,ia.amount_minor
				FROM invoice_allocations ia JOIN invoice_requests ir ON ir.id=ia.invoice_request_id
				WHERE ia.funding_lot_id=$1 AND ia.allocation_state IN ('issued','refund_attention')
					AND ir.status IN ('issued_awaiting_document','issued','refund_attention')
				ORDER BY ir.id FOR UPDATE OF ir`, lot.ID)
			if queryErr != nil {
				return ObservationResult{}, fmt.Errorf("lock issued refund attention requests: %w", queryErr)
			}
			type attentionRequest struct {
				id       string
				status   domain.RequestStatus
				version  int64
				exposure int64
			}
			attention := make([]attentionRequest, 0)
			for rows.Next() {
				var item attentionRequest
				if err = rows.Scan(&item.id, &item.status, &item.version, &item.exposure); err != nil {
					rows.Close()
					return ObservationResult{}, err
				}
				attention = append(attention, item)
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				return ObservationResult{}, err
			}
			rows.Close()
			for _, item := range attention {
				if _, err = tx.Exec(ctx, `
					UPDATE invoice_allocations SET allocation_state='refund_attention',updated_at=now()
					WHERE invoice_request_id=$1 AND funding_lot_id=$2
						AND allocation_state IN ('issued','refund_attention')`, item.id, lot.ID); err != nil {
					return ObservationResult{}, err
				}
				attentionIDs = append(attentionIDs, item.id)
				refundCaseID := randomUUID()
				observedRefund := lot.OriginalMinor - lot.CurrentCapMinor
				if manualNewAPICap {
					observedRefund = manualApprovedGrossMinor - lot.CurrentCapMinor
				}
				err = tx.QueryRow(ctx, `
					INSERT INTO refund_cases(
						id,invoice_request_id,funding_lot_id,source_revision_hash,
						observed_refund_minor,issued_exposure_minor,observed_cap_minor)
					VALUES($1,$2,$3,$4,$5,$6,$7)
					ON CONFLICT(invoice_request_id,funding_lot_id) WHERE status='open'
					DO UPDATE SET source_revision_hash=EXCLUDED.source_revision_hash,
						observed_refund_minor=GREATEST(refund_cases.observed_refund_minor,EXCLUDED.observed_refund_minor),
						issued_exposure_minor=GREATEST(refund_cases.issued_exposure_minor,EXCLUDED.issued_exposure_minor),
						observed_cap_minor=LEAST(refund_cases.observed_cap_minor,EXCLUDED.observed_cap_minor),
						updated_at=now()
					RETURNING id`, refundCaseID, item.id, lot.ID, lot.SourceRevision,
					observedRefund, item.exposure, lot.CurrentCapMinor).Scan(&refundCaseID)
				if err != nil {
					return ObservationResult{}, fmt.Errorf("open refund case: %w", err)
				}
				if err = writeAudit(ctx, tx, actor, "refund_case.opened_or_updated", "refund_case", refundCaseID,
					nil, map[string]any{"request_id": item.id, "funding_lot_id": lot.ID,
						"observed_refund_minor": observedRefund, "issued_exposure_minor": item.exposure}); err != nil {
					return ObservationResult{}, err
				}
				if item.status == domain.StatusRefundAttention {
					continue
				}
				command, updateErr := tx.Exec(ctx, `
					UPDATE invoice_requests SET status='refund_attention',version=version+1,updated_at=now()
					WHERE id=$1 AND version=$2`, item.id, item.version)
				if updateErr != nil {
					return ObservationResult{}, updateErr
				}
				if command.RowsAffected() != 1 {
					return ObservationResult{}, domain.ErrVersionConflict
				}
				if err = writeAudit(ctx, tx, actor, "invoice_request.refund_attention", "invoice_request", item.id,
					map[string]any{"status": item.status}, map[string]any{"status": domain.StatusRefundAttention}); err != nil {
					return ObservationResult{}, err
				}
			}
		}
		sort.Strings(invalidatedIDs)
		sort.Strings(attentionIDs)
	}
	if err = writeAudit(ctx, tx, actor, "funding_lot.observed", "funding_lot", lot.ID, before, lot); err != nil {
		return ObservationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ObservationResult{}, fmt.Errorf("commit source observation: %w", err)
	}
	invalidatedIDs = uniqueSortedStrings(invalidatedIDs)
	attentionIDs = uniqueSortedStrings(attentionIDs)
	return ObservationResult{Lot: lot, AttentionRequestIDs: attentionIDs, InvalidatedRequestIDs: invalidatedIDs}, nil
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func optionalTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func getFundingLotTx(ctx context.Context, tx pgx.Tx, sourceInstanceID, externalOrderID, lotID, lockClause string) (domain.FundingLot, error) {
	query := `
		SELECT fl.id,fl.invoice_user_id,fl.source_instance_id,si.source_type,
			fl.external_order_id,fl.trade_no,fl.currency,fl.original_minor,
			fl.current_cap_minor,fl.verified_cash_minor,fl.consumed_cash_minor,
			fl.reserved_minor,fl.issued_minor,fl.eligibility_kind,
			COALESCE(fl.eligibility_cutover_at,'epoch'::timestamptz),fl.refund_frozen,fl.eligibility_revision,
			COALESCE(eas.eligibility_status,'missing'),
			fl.verification_state,fl.source_status,fl.source_revision_hash,
			COALESCE(fl.completed_at,'epoch'::timestamptz),fl.observed_at,fl.updated_at
		FROM funding_lots fl JOIN source_instances si ON si.id=fl.source_instance_id
		LEFT JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id WHERE `
	args := []any{sourceInstanceID, externalOrderID}
	if lotID != "" {
		query += `fl.id=$1`
		args = []any{lotID}
	} else {
		query += `fl.source_instance_id=$1 AND fl.external_order_id=$2`
	}
	if lockClause == "FOR UPDATE" {
		query += ` FOR UPDATE OF fl`
	}
	return scanFundingLot(tx.QueryRow(ctx, query, args...))
}

// pgxRow is satisfied by pgx.Row and keeps scanning shared by pool/tx queries.
type pgxRow interface{ Scan(...any) error }

func scanFundingLot(row pgxRow) (domain.FundingLot, error) {
	var lot domain.FundingLot
	err := row.Scan(&lot.ID, &lot.PrincipalID, &lot.SourceInstanceID, &lot.SourceType,
		&lot.ExternalOrderID, &lot.TradeNo, &lot.Currency, &lot.OriginalMinor,
		&lot.CurrentCapMinor, &lot.VerifiedCashMinor, &lot.ConsumedCashMinor,
		&lot.ReservedMinor, &lot.IssuedMinor, &lot.EligibilityKind, &lot.EligibilityCutoverAt,
		&lot.RefundFrozen, &lot.EligibilityRevision, &lot.EligibilityStatus, &lot.Verification,
		&lot.SourceStatus, &lot.SourceRevision, &lot.CompletedAt, &lot.ObservedAt, &lot.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FundingLot{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.FundingLot{}, err
	}
	if lot.CompletedAt.Equal(time.Unix(0, 0).UTC()) {
		lot.CompletedAt = time.Time{}
	}
	if lot.EligibilityCutoverAt.Equal(time.Unix(0, 0).UTC()) {
		lot.EligibilityCutoverAt = time.Time{}
	}
	return lot, nil
}

func (s *Store) GetFundingLot(ctx context.Context, lotID string) (domain.FundingLot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.FundingLot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lot, err := getFundingLotTx(ctx, tx, "", "", lotID, "")
	if err != nil {
		return domain.FundingLot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.FundingLot{}, err
	}
	return lot, nil
}

func (s *Store) GetFundingLotByExternalOrder(ctx context.Context, sourceInstanceID, externalOrderID string) (domain.FundingLot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.FundingLot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lot, err := getFundingLotTx(ctx, tx, sourceInstanceID, externalOrderID, "", "")
	if err != nil {
		return domain.FundingLot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.FundingLot{}, err
	}
	return lot, nil
}

func (s *Store) ExternalUserIDForFundingLot(ctx context.Context, lotID string) (string, error) {
	var externalUserID string
	err := s.pool.QueryRow(ctx, `
		SELECT ea.external_user_id
		FROM funding_lots fl JOIN external_accounts ea ON ea.id=fl.external_account_id
		WHERE fl.id=$1`, lotID).Scan(&externalUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return externalUserID, err
}

// ListFundingLots: platform (XM-INV-PLATFORM-SCOPE) narrows the result to
// one source instance type when set; empty means unscoped.
func (s *Store) ListFundingLots(ctx context.Context, principalID string, platform domain.SourceType) ([]domain.FundingLot, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fl.id,fl.invoice_user_id,fl.source_instance_id,si.source_type,
			fl.external_order_id,fl.trade_no,fl.currency,fl.original_minor,
			fl.current_cap_minor,fl.verified_cash_minor,fl.consumed_cash_minor,
			fl.reserved_minor,fl.issued_minor,fl.eligibility_kind,
			COALESCE(fl.eligibility_cutover_at,'epoch'::timestamptz),
			fl.refund_frozen,fl.eligibility_revision,COALESCE(eas.eligibility_status,'missing'),
			fl.verification_state,fl.source_status,fl.source_revision_hash,
			COALESCE(fl.completed_at,'epoch'::timestamptz),fl.observed_at,fl.updated_at
		FROM funding_lots fl JOIN source_instances si ON si.id=fl.source_instance_id
		LEFT JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id
		WHERE fl.invoice_user_id=$1 AND ($2='' OR si.source_type=$2)
		ORDER BY fl.completed_at DESC NULLS LAST,fl.id
		LIMIT 500`, principalID, string(platform))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.FundingLot, 0)
	for rows.Next() {
		lot, scanErr := scanFundingLot(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, lot)
	}
	return out, rows.Err()
}

func (s *Store) ListPaymentCandidatesPage(ctx context.Context, in PaymentCandidatePageQuery) (PaymentCandidatePage, error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.BeforeObservedAt.IsZero() != (strings.TrimSpace(in.BeforeID) == "") {
		return PaymentCandidatePage{}, errors.New("both payment candidate cursor fields are required")
	}
	if in.SourceInstanceID != "" && !eligibilityUUIDPattern.MatchString(in.SourceInstanceID) {
		return PaymentCandidatePage{}, errors.New("invalid source instance filter")
	}
	states := in.States
	if len(states) == 0 {
		states = []domain.VerificationState{domain.VerificationPending, domain.VerificationFrozen}
	}
	stateStrings := make([]string, len(states))
	for i, state := range states {
		if state != domain.VerificationPending && state != domain.VerificationFrozen && state != domain.VerificationVerified {
			return PaymentCandidatePage{}, errors.New("candidate state must be pending, frozen or verified")
		}
		stateStrings[i] = string(state)
	}
	query := `
		SELECT fl.id,fl.invoice_user_id,fl.source_instance_id,si.source_type,
			fl.external_order_id,fl.trade_no,fl.currency,
			LEAST(fl.original_minor,COALESCE(fl.newapi_manual_ceiling_minor,fl.original_minor)),
			fl.current_cap_minor,fl.verified_cash_minor,fl.consumed_cash_minor,
			fl.reserved_minor,fl.issued_minor,fl.eligibility_kind,
			COALESCE(fl.eligibility_cutover_at,'epoch'::timestamptz),fl.refund_frozen,fl.eligibility_revision,
			COALESCE(eas.eligibility_status,'missing'),
			fl.verification_state,fl.source_status,fl.source_revision_hash,
			COALESCE(fl.completed_at,'epoch'::timestamptz),fl.observed_at,fl.updated_at,
			COALESCE(pcd.state,CASE WHEN fl.eligibility_kind='LEGACY_NON_INVOICEABLE'
				THEN 'service_units_unprovable' ELSE 'unreviewed' END)
		FROM funding_lots fl JOIN source_instances si ON si.id=fl.source_instance_id
		LEFT JOIN payment_candidate_decisions pcd ON pcd.funding_lot_id=fl.id
		LEFT JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id
		WHERE si.source_type='newapi' AND fl.verification_state=ANY($1::text[])`
	args := []any{stateStrings}
	if in.SourceInstanceID != "" {
		args = append(args, in.SourceInstanceID)
		query += fmt.Sprintf(` AND fl.source_instance_id=$%d::uuid`, len(args))
	}
	if !in.BeforeObservedAt.IsZero() {
		args = append(args, in.BeforeObservedAt, in.BeforeID)
		query += fmt.Sprintf(` AND (fl.observed_at,fl.id)<($%d,$%d::uuid)`, len(args)-1, len(args))
	}
	args = append(args, in.Limit+1)
	query += fmt.Sprintf(` ORDER BY fl.observed_at DESC,fl.id DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return PaymentCandidatePage{}, err
	}
	defer rows.Close()
	items := make([]domain.FundingLot, 0, in.Limit+1)
	for rows.Next() {
		var lot domain.FundingLot
		scanErr := rows.Scan(&lot.ID, &lot.PrincipalID, &lot.SourceInstanceID, &lot.SourceType,
			&lot.ExternalOrderID, &lot.TradeNo, &lot.Currency, &lot.OriginalMinor,
			&lot.CurrentCapMinor, &lot.VerifiedCashMinor, &lot.ConsumedCashMinor,
			&lot.ReservedMinor, &lot.IssuedMinor, &lot.EligibilityKind, &lot.EligibilityCutoverAt,
			&lot.RefundFrozen, &lot.EligibilityRevision, &lot.EligibilityStatus, &lot.Verification,
			&lot.SourceStatus, &lot.SourceRevision, &lot.CompletedAt, &lot.ObservedAt,
			&lot.UpdatedAt, &lot.ManualReviewStage)
		if scanErr != nil {
			return PaymentCandidatePage{}, scanErr
		}
		items = append(items, lot)
	}
	if err = rows.Err(); err != nil {
		return PaymentCandidatePage{}, err
	}
	page := PaymentCandidatePage{HasMore: len(items) > in.Limit}
	if page.HasMore {
		items = items[:in.Limit]
	}
	page.Items = items
	if page.HasMore && len(items) > 0 {
		last := items[len(items)-1]
		page.NextBeforeObservedAt = last.ObservedAt
		page.NextBeforeID = last.ID
	}
	return page, nil
}

func (s *Store) ReviewNewAPIPaymentCandidate(ctx context.Context, in ReviewPaymentCandidateInput) (domain.FundingLot, error) {
	if strings.TrimSpace(in.LotID) == "" || (in.Action != "verify" && in.Action != "reject" && in.Action != "freeze") ||
		!hexHashPattern.MatchString(in.Evidence.Hash) || len(in.Evidence.Ciphertext) < 16 || len(in.Evidence.Ciphertext) > 8192 ||
		len(in.ReasonCiphertext) < 16 || len(in.ReasonCiphertext) > 4096 || !hexHashPattern.MatchString(in.ReasonHash) {
		return domain.FundingLot{}, errors.New("complete encrypted payment evidence is required")
	}
	if in.Action == "verify" {
		if in.PaidMinor <= 0 || in.Currency != domain.CurrencyCNY {
			return domain.FundingLot{}, domain.ErrConflict
		}
	} else if in.PaidMinor != 0 || in.Currency != "" {
		return domain.FundingLot{}, domain.ErrConflict
	}
	actor := in.Actor.normalized()
	if actor.Type != "admin" || strings.TrimSpace(actor.ID) == "" {
		return domain.FundingLot{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.FundingLot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lot, err := getFundingLotTx(ctx, tx, "", "", in.LotID, "FOR UPDATE")
	if err != nil {
		return domain.FundingLot{}, err
	}
	if lot.SourceType != domain.SourceNewAPI {
		return domain.FundingLot{}, domain.ErrInvalidState
	}
	if lot.RefundFrozen {
		return domain.FundingLot{}, domain.ErrInvalidState
	}
	if lot.PrincipalID == actor.ID {
		return domain.FundingLot{}, domain.ErrForbidden
	}
	var trustedCandidateUpper int64
	if err = tx.QueryRow(ctx, `
		SELECT LEAST(original_minor,COALESCE(newapi_manual_ceiling_minor,original_minor))
		FROM funding_lots WHERE id=$1`, lot.ID).Scan(&trustedCandidateUpper); err != nil {
		return domain.FundingLot{}, err
	}
	if in.Action == "verify" && in.PaidMinor > trustedCandidateUpper {
		// original_minor is the unverified but signed source candidate amount;
		// manual review may reduce it but can never mint additional entitlement.
		return domain.FundingLot{}, domain.ErrInsufficientAmount
	}
	if lot.Verification != domain.VerificationPending && lot.Verification != domain.VerificationFrozen &&
		!(in.Action == "verify" && lot.Verification == domain.VerificationVerified) {
		return domain.FundingLot{}, domain.ErrInvalidState
	}

	actualAction := in.Action
	decisionState := ""
	var proposedPaid int64
	var proposedCurrency, proposedEvidence, proposedBy string
	if in.Action == "verify" {
		err = tx.QueryRow(ctx, `
			SELECT state,paid_minor,currency,evidence_hash,proposed_by
			FROM payment_candidate_decisions WHERE funding_lot_id=$1 FOR UPDATE`, lot.ID).
			Scan(&decisionState, &proposedPaid, &proposedCurrency, &proposedEvidence, &proposedBy)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.FundingLot{}, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			decisionState = ""
			actualAction = "verify_propose"
		} else {
			if proposedPaid != in.PaidMinor || proposedCurrency != in.Currency || proposedEvidence != in.Evidence.Hash {
				return domain.FundingLot{}, domain.ErrConflict
			}
			if decisionState == "approved" {
				if err = tx.Commit(ctx); err != nil {
					return domain.FundingLot{}, err
				}
				return lot, nil
			}
			if decisionState != "proposed" {
				return domain.FundingLot{}, domain.ErrInvalidState
			}
			if proposedBy == actor.ID {
				// Exact retry by the proposer is idempotent, but never counts as
				// approval. A distinct authenticated administrator is mandatory.
				if err = tx.Commit(ctx); err != nil {
					return domain.FundingLot{}, err
				}
				return lot, nil
			}
			actualAction = "verify_approve"
		}
	} else {
		var existingPaid int64
		var existingCurrency, existingReasonHash string
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(paid_minor,0),COALESCE(currency,''),review_reason_hash
			FROM payment_candidate_reviews
			WHERE funding_lot_id=$1 AND review_action=$2 AND evidence_hash=$3`,
			lot.ID, actualAction, in.Evidence.Hash).Scan(&existingPaid, &existingCurrency, &existingReasonHash)
		if err == nil {
			if existingPaid != in.PaidMinor || existingCurrency != in.Currency || existingReasonHash != in.ReasonHash {
				return domain.FundingLot{}, domain.ErrConflict
			}
			if err = tx.Commit(ctx); err != nil {
				return domain.FundingLot{}, err
			}
			return lot, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.FundingLot{}, err
		}
	}

	before := lot
	if actualAction == "verify_approve" {
		lot.CurrentCapMinor = in.PaidMinor
		lot.VerifiedCashMinor = in.PaidMinor
		lot.Verification = domain.VerificationVerified
	} else {
		// Proposal, rejection and investigation freeze all remain unavailable
		// to users. Only a distinct second administrator can set verified.
		lot.CurrentCapMinor = 0
		lot.VerifiedCashMinor = 0
		lot.Verification = domain.VerificationFrozen
	}
	lot.UpdatedAt = time.Now().UTC()
	if err = lot.Validate(); err != nil {
		return domain.FundingLot{}, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE funding_lots SET current_cap_minor=$1,verified_cash_minor=$2,
			verification_state=$3,updated_at=$4
		WHERE id=$5 AND verification_state IN ('pending','frozen','verified')`,
		lot.CurrentCapMinor, lot.VerifiedCashMinor, lot.Verification, lot.UpdatedAt, lot.ID)
	if err != nil {
		return domain.FundingLot{}, err
	}
	if command.RowsAffected() != 1 {
		return domain.FundingLot{}, domain.ErrInvalidState
	}
	reviewID := randomUUID()
	_, err = tx.Exec(ctx, `
		INSERT INTO payment_candidate_reviews(
			id,funding_lot_id,review_action,admin_id,evidence_ref_ciphertext,
			review_reason_ciphertext,review_reason_hash,evidence_hash,paid_minor,currency,request_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, reviewID, lot.ID, actualAction,
		actor.ID, in.Evidence.Ciphertext, in.ReasonCiphertext, in.ReasonHash, in.Evidence.Hash,
		nullablePositive(in.PaidMinor), optionalString(in.Currency), actor.RequestID)
	if err != nil {
		return domain.FundingLot{}, fmt.Errorf("record payment candidate review: %w", err)
	}
	if actualAction == "verify_propose" {
		_, err = tx.Exec(ctx, `
			INSERT INTO payment_candidate_decisions(
				funding_lot_id,state,paid_minor,currency,evidence_hash,
				proposed_by,proposed_review_id,proposed_at,updated_at)
			VALUES($1,'proposed',$2,$3,$4,$5,$6,$7,$7)`,
			lot.ID, in.PaidMinor, in.Currency, in.Evidence.Hash, actor.ID, reviewID, lot.UpdatedAt)
		if err != nil {
			return domain.FundingLot{}, fmt.Errorf("record first payment verification: %w", err)
		}
	} else if actualAction == "verify_approve" {
		command, err = tx.Exec(ctx, `
			UPDATE payment_candidate_decisions
			SET state='approved',approved_by=$1,approved_review_id=$2,
				approved_at=$3,updated_at=$3
			WHERE funding_lot_id=$4 AND state='proposed' AND proposed_by<>$1`,
			actor.ID, reviewID, lot.UpdatedAt, lot.ID)
		if err != nil {
			return domain.FundingLot{}, fmt.Errorf("record second payment verification: %w", err)
		}
		if command.RowsAffected() != 1 {
			return domain.FundingLot{}, domain.ErrConflict
		}
	} else if actualAction == "reject" || actualAction == "freeze" {
		// A negative/investigation decision invalidates any earlier proposal.
		// Review rows remain immutable audit evidence, but an old tuple can no
		// longer be approved later by another administrator.
		if _, err = tx.Exec(ctx, `
			DELETE FROM payment_candidate_decisions
			WHERE funding_lot_id=$1 AND state='proposed'`, lot.ID); err != nil {
			return domain.FundingLot{}, fmt.Errorf("invalidate pending payment proposal: %w", err)
		}
	}
	if actualAction == "verify_approve" {
		var accountID, eligibilityKind string
		var finalized, completed time.Time
		err = tx.QueryRow(ctx, `
			SELECT fl.external_account_id,fl.eligibility_kind,eas.finalized_through,
				COALESCE(fl.completed_at,'epoch'::timestamptz)
			FROM funding_lots fl
			JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id
			WHERE fl.id=$1 FOR UPDATE OF eas`, lot.ID).Scan(&accountID, &eligibilityKind, &finalized, &completed)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.FundingLot{}, err
		}
		if err == nil && eligibilityKind == string(domain.EligibilityWalletCash) && !completed.After(finalized) {
			if err = reprojectEligibilityTx(ctx, tx, accountID, finalized, actor); err != nil {
				return domain.FundingLot{}, err
			}
			lot, err = getFundingLotTx(ctx, tx, "", "", lot.ID, "FOR UPDATE")
			if err != nil {
				return domain.FundingLot{}, err
			}
		}
	}
	actor.Reason = "newapi payment evidence sha256:" + in.Evidence.Hash
	if err = writeAudit(ctx, tx, actor, "funding_lot.manual_review."+actualAction, "funding_lot", lot.ID, before, lot); err != nil {
		return domain.FundingLot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.FundingLot{}, err
	}
	return lot, nil
}

func nullablePositive(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
