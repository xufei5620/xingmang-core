package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

func scanRefundCase(row pgxRow) (RefundCase, error) {
	var item RefundCase
	err := row.Scan(&item.ID, &item.RequestID, &item.FundingLotID, &item.SourceRevision,
		&item.ObservedRefundMinor, &item.IssuedExposureMinor, &item.ObservedCapMinor,
		&item.Status, &item.ResolutionEvidenceHash, &item.ResolutionNoteHash, &item.ResolvedBy,
		&item.OpenedAt, &item.UpdatedAt, &item.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefundCase{}, domain.ErrNotFound
	}
	if err != nil {
		return RefundCase{}, err
	}
	if item.ResolvedAt.Equal(time.Unix(0, 0).UTC()) {
		item.ResolvedAt = time.Time{}
	}
	return item, nil
}

const refundCaseSelect = `
	SELECT id,invoice_request_id,funding_lot_id,source_revision_hash,
		observed_refund_minor,issued_exposure_minor,observed_cap_minor,status,
		COALESCE(resolution_evidence_hash,''),COALESCE(resolution_note_hash,''),COALESCE(resolved_by::text,''),
		opened_at,updated_at,COALESCE(resolved_at,'epoch'::timestamptz)
	FROM refund_cases`

func (s *Store) ListRefundCasesPage(ctx context.Context, in RefundCasePageQuery) (RefundCasePage, error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Status == "" {
		in.Status = "open"
	}
	if in.Status != "open" && in.Status != "resolved_red_letter" && in.Status != "resolved_no_action" {
		return RefundCasePage{}, errors.New("invalid refund case status")
	}
	if in.BeforeOpenedAt.IsZero() != (strings.TrimSpace(in.BeforeID) == "") {
		return RefundCasePage{}, errors.New("both refund case cursor fields are required")
	}
	query := refundCaseSelect + ` WHERE status=$1`
	args := []any{in.Status}
	if !in.BeforeOpenedAt.IsZero() {
		query += ` AND (opened_at,id)<($2,$3::uuid)`
		args = append(args, in.BeforeOpenedAt, in.BeforeID)
	}
	args = append(args, in.Limit+1)
	query += fmt.Sprintf(` ORDER BY opened_at DESC,id DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return RefundCasePage{}, err
	}
	defer rows.Close()
	items := make([]RefundCase, 0, in.Limit+1)
	for rows.Next() {
		item, scanErr := scanRefundCase(rows)
		if scanErr != nil {
			return RefundCasePage{}, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return RefundCasePage{}, err
	}
	page := RefundCasePage{HasMore: len(items) > in.Limit}
	if page.HasMore {
		items = items[:in.Limit]
	}
	page.Items = items
	if page.HasMore && len(items) > 0 {
		last := items[len(items)-1]
		page.NextBeforeOpenedAt = last.OpenedAt
		page.NextBeforeID = last.ID
	}
	return page, nil
}

func (s *Store) ResolveRefundCase(ctx context.Context, in ResolveRefundCaseInput) (RefundCase, error) {
	if strings.TrimSpace(in.CaseID) == "" ||
		(in.ResolutionStatus != "resolved_red_letter" && in.ResolutionStatus != "resolved_no_action") ||
		!hexHashPattern.MatchString(in.EvidenceHash) || len(in.EvidenceCiphertext) < 16 ||
		len(in.EvidenceCiphertext) > 8192 || len(in.NoteCiphertext) < 16 || len(in.NoteCiphertext) > 8192 ||
		!hexHashPattern.MatchString(in.NoteHash) {
		return RefundCase{}, errors.New("complete encrypted refund resolution evidence is required")
	}
	actor := in.Actor.normalized()
	if actor.Type != "admin" || strings.TrimSpace(actor.ID) == "" {
		return RefundCase{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RefundCase{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	item, err := scanRefundCase(tx.QueryRow(ctx, refundCaseSelect+` WHERE id=$1 FOR UPDATE`, in.CaseID))
	if err != nil {
		return RefundCase{}, err
	}
	var requestOwner string
	if err = tx.QueryRow(ctx, `SELECT invoice_user_id::text FROM invoice_requests WHERE id=$1`, item.RequestID).Scan(&requestOwner); err != nil {
		return RefundCase{}, err
	}
	if requestOwner == actor.ID {
		return RefundCase{}, domain.ErrForbidden
	}
	if item.Status != "open" {
		if item.Status == in.ResolutionStatus && item.ResolutionEvidenceHash == in.EvidenceHash && item.ResolutionNoteHash == in.NoteHash {
			if err = tx.Commit(ctx); err != nil {
				return RefundCase{}, err
			}
			return item, nil
		}
		return RefundCase{}, domain.ErrInvalidState
	}
	before := item
	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		UPDATE refund_cases SET status=$1,resolution_evidence_ciphertext=$2,
			resolution_evidence_hash=$3,resolution_note_ciphertext=$4,resolution_note_hash=$5,resolved_by=$6,
			resolved_at=$7,updated_at=$7
		WHERE id=$8 AND status='open'
		RETURNING status,resolution_evidence_hash,resolution_note_hash,resolved_by::text,updated_at,resolved_at`,
		in.ResolutionStatus, in.EvidenceCiphertext, in.EvidenceHash, in.NoteCiphertext,
		in.NoteHash, actor.ID, now, in.CaseID).Scan(&item.Status, &item.ResolutionEvidenceHash,
		&item.ResolutionNoteHash, &item.ResolvedBy, &item.UpdatedAt, &item.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefundCase{}, domain.ErrVersionConflict
	}
	if err != nil {
		return RefundCase{}, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE invoice_allocations SET allocation_state='issued',updated_at=now()
		WHERE invoice_request_id=$1 AND funding_lot_id=$2 AND allocation_state='refund_attention'`,
		item.RequestID, item.FundingLotID); err != nil {
		return RefundCase{}, err
	}
	var remaining int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM refund_cases WHERE invoice_request_id=$1 AND status='open'`, item.RequestID).Scan(&remaining); err != nil {
		return RefundCase{}, err
	}
	if remaining == 0 {
		var hasDocument bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice_documents WHERE invoice_request_id=$1)`, item.RequestID).Scan(&hasDocument); err != nil {
			return RefundCase{}, err
		}
		next := domain.StatusIssuedAwaitingDocument
		if hasDocument {
			next = domain.StatusIssued
		}
		if _, err = tx.Exec(ctx, `
			UPDATE invoice_requests SET status=$1,version=version+1,updated_at=now()
			WHERE id=$2 AND status='refund_attention'`, next, item.RequestID); err != nil {
			return RefundCase{}, err
		}
	}
	actor.Reason = "manual refund/red-letter resolution evidence sha256:" + in.EvidenceHash
	if err = writeAudit(ctx, tx, actor, "refund_case."+in.ResolutionStatus, "refund_case", item.ID, before, item); err != nil {
		return RefundCase{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RefundCase{}, err
	}
	return item, nil
}

func (s *Store) GetRefundCaseResolution(ctx context.Context, caseID string) (RefundCaseResolutionRecord, error) {
	item, err := scanRefundCase(s.pool.QueryRow(ctx, refundCaseSelect+` WHERE id=$1`, caseID))
	if err != nil {
		return RefundCaseResolutionRecord{}, err
	}
	if item.Status == "open" {
		return RefundCaseResolutionRecord{}, domain.ErrInvalidState
	}
	var evidence, note []byte
	err = s.pool.QueryRow(ctx, `
		SELECT resolution_evidence_ciphertext,resolution_note_ciphertext
		FROM refund_cases WHERE id=$1`, caseID).Scan(&evidence, &note)
	if err != nil {
		return RefundCaseResolutionRecord{}, err
	}
	return RefundCaseResolutionRecord{Case: item, EvidenceCiphertext: evidence, NoteCiphertext: note}, nil
}
