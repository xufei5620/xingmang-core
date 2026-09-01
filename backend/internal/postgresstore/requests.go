package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"invoice-system/backend/internal/domain"
)

func scanRequestRecord(row pgxRow) (RequestRecord, error) {
	var rec RequestRecord
	err := row.Scan(
		&rec.Request.ID, &rec.Request.RequestNo, &rec.Request.PrincipalID,
		&rec.Request.SourceInstanceID, &rec.ProfileID, &rec.Request.SourceType,
		&rec.ProfileSnapshotCiphertext, &rec.IssuerSettingRevision, &rec.IssueSnapshotCiphertext,
		&rec.Request.Currency, &rec.Request.IssuerCode, &rec.Request.ServiceItem,
		&rec.Request.AmountMinor, &rec.Request.Status, &rec.IdempotencyKey,
		&rec.Request.ReviewNote, &rec.Request.ReviewedBy, &rec.Request.IssuedBy,
		&rec.Request.Version, &rec.Request.SubmittedAt, &rec.Request.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return RequestRecord{}, err
	}
	return rec, nil
}

const requestSelect = `
	SELECT ir.id,ir.request_no,ir.invoice_user_id,ir.source_instance_id,ir.profile_id,
		si.source_type,ir.profile_snapshot_ciphertext,
		COALESCE(ir.issuer_setting_revision,0),COALESCE(ir.issue_snapshot_ciphertext,''::bytea),
		ir.currency,ir.issuer_code,
		ir.service_item,ir.amount_minor,ir.status,ir.idempotency_key,ir.review_note,
		COALESCE(ir.reviewed_by::text,''),COALESCE(ir.issued_by::text,''),
		ir.version,ir.submitted_at,ir.updated_at
	FROM invoice_requests ir JOIN source_instances si ON si.id=ir.source_instance_id`

func getRequestRecordTx(ctx context.Context, tx pgx.Tx, requestID string, lock bool) (RequestRecord, error) {
	query := requestSelect + ` WHERE ir.id=$1`
	if lock {
		query += ` FOR UPDATE OF ir`
	}
	rec, err := scanRequestRecord(tx.QueryRow(ctx, query, requestID))
	if err != nil {
		return RequestRecord{}, err
	}
	allocations, err := loadAllocations(ctx, tx, requestID)
	if err != nil {
		return RequestRecord{}, err
	}
	rec.Request.Allocations = allocations
	return rec, nil
}

func loadAllocations(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, requestID string) ([]domain.Allocation, error) {
	rows, err := q.Query(ctx, `
		SELECT ia.funding_lot_id,fl.external_order_id,ia.amount_minor
		FROM invoice_allocations ia JOIN funding_lots fl ON fl.id=ia.funding_lot_id
		WHERE ia.invoice_request_id=$1 ORDER BY ia.funding_lot_id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Allocation, 0)
	for rows.Next() {
		var allocation domain.Allocation
		if err = rows.Scan(&allocation.FundingLotID, &allocation.ExternalOrder, &allocation.AmountMinor); err != nil {
			return nil, err
		}
		out = append(out, allocation)
	}
	return out, rows.Err()
}

// GetRequestRecord loads one request. platform (XM-INV-PLATFORM-SCOPE) is
// always empty for admin or platform-less sessions. When set and the request
// belongs to a different platform, it reports ErrNotFound rather than
// ErrForbidden -- the request is genuinely this principal's own (the
// ownership check above already passed or is bypassed by admin), but a
// platform-scoped session must never be able to distinguish "belongs to the
// other platform" from "does not exist" (CR-0003: no existence leak).
func (s *Store) GetRequestRecord(ctx context.Context, principalID, requestID string, admin bool, platform domain.SourceType) (RequestRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rec, err := getRequestRecordTx(ctx, tx, requestID, false)
	if err != nil {
		return RequestRecord{}, err
	}
	if !admin && rec.Request.PrincipalID != principalID {
		return RequestRecord{}, domain.ErrForbidden
	}
	if !admin && platform != "" && rec.Request.SourceType != platform {
		return RequestRecord{}, domain.ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestRecord{}, err
	}
	return rec, nil
}

func (s *Store) ListRequestRecords(ctx context.Context, principalID string, admin bool, limit int, platform domain.SourceType) ([]RequestRecord, error) {
	page, err := s.ListRequestRecordsPage(ctx, RequestPageQuery{
		PrincipalID: principalID, Admin: admin, Limit: limit, Platform: platform,
	})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func validRequestStatus(status domain.RequestStatus) bool {
	switch status {
	case domain.StatusPendingReview, domain.StatusNeedsChanges, domain.StatusApproved,
		domain.StatusRejected, domain.StatusUserCancelled, domain.StatusManualIssuing,
		domain.StatusIssuedAwaitingDocument, domain.StatusIssued, domain.StatusRefundAttention:
		return true
	default:
		return false
	}
}

// ListRequestRecordsPage is a stable (submitted_at,id) keyset query.  User
// pages are capped at 200; administrator pages at 100.  The extra row is used
// only to compute HasMore and is never returned to callers.
func (s *Store) ListRequestRecordsPage(ctx context.Context, in RequestPageQuery) (RequestPage, error) {
	if !in.Admin && strings.TrimSpace(in.PrincipalID) == "" {
		return RequestPage{}, errors.New("principal ID is required")
	}
	maxLimit := 200
	defaultLimit := 100
	if in.Admin {
		maxLimit = 100
	}
	if in.Limit <= 0 {
		in.Limit = defaultLimit
	}
	if in.Limit > maxLimit {
		in.Limit = maxLimit
	}
	if in.BeforeSubmittedAt.IsZero() != (strings.TrimSpace(in.BeforeID) == "") {
		return RequestPage{}, errors.New("both request cursor fields are required")
	}
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 8)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if !in.Admin {
		conditions = append(conditions, "ir.invoice_user_id="+addArg(in.PrincipalID))
	}
	if strings.TrimSpace(in.SourceInstanceID) != "" {
		conditions = append(conditions, "ir.source_instance_id="+addArg(in.SourceInstanceID))
	}
	if in.Platform != "" {
		conditions = append(conditions, "si.source_type="+addArg(string(in.Platform)))
	}
	if len(in.Statuses) > 0 {
		statuses := make([]string, len(in.Statuses))
		for i, status := range in.Statuses {
			if !validRequestStatus(status) {
				return RequestPage{}, errors.New("invalid invoice request status filter")
			}
			statuses[i] = string(status)
		}
		conditions = append(conditions, "ir.status=ANY("+addArg(statuses)+"::text[])")
	}
	if !in.BeforeSubmittedAt.IsZero() {
		timeArg := addArg(in.BeforeSubmittedAt)
		idArg := addArg(in.BeforeID)
		conditions = append(conditions, "(ir.submitted_at,ir.id)<("+timeArg+","+idArg+"::uuid)")
	}
	query := requestSelect
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY ir.submitted_at DESC,ir.id DESC LIMIT " + addArg(in.Limit+1)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return RequestPage{}, err
	}
	defer rows.Close()
	out := make([]RequestRecord, 0)
	for rows.Next() {
		rec, scanErr := scanRequestRecord(rows)
		if scanErr != nil {
			return RequestPage{}, scanErr
		}
		out = append(out, rec)
	}
	if err = rows.Err(); err != nil {
		return RequestPage{}, err
	}
	hasMore := len(out) > in.Limit
	if hasMore {
		out = out[:in.Limit]
	}
	if err = s.attachAllocations(ctx, out); err != nil {
		return RequestPage{}, err
	}
	page := RequestPage{Items: out, HasMore: hasMore}
	if hasMore && len(out) > 0 {
		last := out[len(out)-1].Request
		page.NextBeforeSubmittedAt = last.SubmittedAt
		page.NextBeforeID = last.ID
	}
	return page, nil
}

func (s *Store) attachAllocations(ctx context.Context, records []RequestRecord) error {
	if len(records) == 0 {
		return nil
	}
	ids := make([]string, len(records))
	positions := make(map[string]int, len(records))
	for i := range records {
		ids[i] = records[i].Request.ID
		positions[ids[i]] = i
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ia.invoice_request_id,ia.funding_lot_id,fl.external_order_id,ia.amount_minor
		FROM invoice_allocations ia JOIN funding_lots fl ON fl.id=ia.funding_lot_id
		WHERE ia.invoice_request_id=ANY($1::uuid[])
		ORDER BY ia.invoice_request_id,ia.funding_lot_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var requestID string
		var allocation domain.Allocation
		if err = rows.Scan(&requestID, &allocation.FundingLotID, &allocation.ExternalOrder, &allocation.AmountMinor); err != nil {
			return err
		}
		position, ok := positions[requestID]
		if !ok {
			return domain.ErrConflict
		}
		records[position].Request.Allocations = append(records[position].Request.Allocations, allocation)
	}
	return rows.Err()
}

func releaseReservations(ctx context.Context, tx pgx.Tx, requestID string) error {
	rows, err := tx.Query(ctx, `
		SELECT funding_lot_id,amount_minor FROM invoice_allocations
		WHERE invoice_request_id=$1 AND allocation_state='reserved'
		ORDER BY funding_lot_id FOR UPDATE`, requestID)
	if err != nil {
		return err
	}
	type item struct {
		lotID  string
		amount int64
	}
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.lotID, &value.amount); err != nil {
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
		command, updateErr := tx.Exec(ctx, `
			UPDATE funding_lots SET reserved_minor=reserved_minor-$1,updated_at=now()
			WHERE id=$2 AND reserved_minor >= $1`, value.amount, value.lotID)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE invoice_allocations SET allocation_state='released',updated_at=now()
		WHERE invoice_request_id=$1 AND allocation_state='reserved'`, requestID)
	return err
}

func issueReservations(ctx context.Context, tx pgx.Tx, requestID string) error {
	var requestPolicyStart, currentPolicyStart time.Time
	var requestPolicyVersion, currentPolicyVersion int64
	if err := tx.QueryRow(ctx, `
		SELECT ir.eligibility_policy_start_at,ir.eligibility_policy_version,
			policy.eligibility_start_at,policy.policy_version
		FROM invoice_requests ir CROSS JOIN invoice_eligibility_policy policy
		WHERE ir.id=$1 FOR SHARE OF ir`, requestID).Scan(
		&requestPolicyStart, &requestPolicyVersion, &currentPolicyStart, &currentPolicyVersion); err != nil {
		return err
	}
	if !requestPolicyStart.Equal(currentPolicyStart) || requestPolicyVersion != currentPolicyVersion {
		return domain.ErrConflict
	}
	rows, err := tx.Query(ctx, `
		SELECT funding_lot_id,amount_minor FROM invoice_allocations
		WHERE invoice_request_id=$1 AND allocation_state='reserved'
		ORDER BY funding_lot_id FOR UPDATE`, requestID)
	if err != nil {
		return err
	}
	type item struct {
		lotID  string
		amount int64
	}
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.lotID, &value.amount); err != nil {
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
	if len(items) == 0 {
		return domain.ErrConflict
	}
	for _, value := range items {
		command, updateErr := tx.Exec(ctx, `
			UPDATE funding_lots SET reserved_minor=reserved_minor-$1,
				issued_minor=issued_minor+$1,updated_at=now()
			WHERE id=$2 AND reserved_minor >= $1
				AND eligibility_kind='WALLET_CASH'
				AND verification_state='verified' AND refund_frozen=FALSE
				AND completed_at >= (SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1)
				AND eligibility_cutover_at >= (SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1)
				AND reserved_minor+issued_minor <= consumed_cash_minor
				AND EXISTS (
					SELECT 1 FROM source_account_eligibility_state eas
					WHERE eas.external_account_id=funding_lots.external_account_id
						AND eas.eligibility_status='active')
				AND NOT EXISTS (SELECT 1 FROM eligibility_projection_jobs epj
					WHERE epj.external_account_id=funding_lots.external_account_id)`, value.amount, value.lotID)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE invoice_allocations SET allocation_state='issued',updated_at=now()
		WHERE invoice_request_id=$1 AND allocation_state='reserved'`, requestID)
	return err
}

// CancelRequest: see GetRequestRecord's doc comment for why a platform
// mismatch (XM-INV-PLATFORM-SCOPE) reports ErrNotFound rather than
// ErrForbidden -- the same no-existence-leak reasoning applies here.
func (s *Store) CancelRequest(ctx context.Context, principalID, requestID string, expectedVersion int64, platform domain.SourceType, actor AuditActor) (RequestRecord, error) {
	if expectedVersion <= 0 {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rec, err := getRequestRecordTx(ctx, tx, requestID, true)
	if err != nil {
		return RequestRecord{}, err
	}
	if rec.Request.PrincipalID != principalID {
		return RequestRecord{}, domain.ErrForbidden
	}
	if platform != "" && rec.Request.SourceType != platform {
		return RequestRecord{}, domain.ErrNotFound
	}
	if rec.Request.Version != expectedVersion {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if rec.Request.Status != domain.StatusPendingReview && rec.Request.Status != domain.StatusNeedsChanges {
		return RequestRecord{}, domain.ErrInvalidState
	}
	before := rec.Request
	if err = releaseReservations(ctx, tx, requestID); err != nil {
		return RequestRecord{}, err
	}
	err = tx.QueryRow(ctx, `
		UPDATE invoice_requests SET status='user_cancelled',version=version+1,updated_at=now()
		WHERE id=$1 AND version=$2
		RETURNING version,updated_at`, requestID, expectedVersion).Scan(&rec.Request.Version, &rec.Request.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if err != nil {
		return RequestRecord{}, fmt.Errorf("cancel invoice request: %w", err)
	}
	rec.Request.Status = domain.StatusUserCancelled
	if err = writeAudit(ctx, tx, actor, "invoice_request.cancelled", "invoice_request", requestID, before, rec.Request); err != nil {
		return RequestRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestRecord{}, err
	}
	return rec, nil
}

func (s *Store) ReviewRequest(ctx context.Context, adminID, requestID, action, note string, expectedVersion int64, actor AuditActor) (RequestRecord, error) {
	if expectedVersion <= 0 || strings.TrimSpace(adminID) == "" {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	note = strings.TrimSpace(note)
	if len(note) > 2000 || strings.ContainsRune(note, '\x00') {
		return RequestRecord{}, errors.New("review note is invalid")
	}
	if (action == "return" || action == "reject") && note == "" {
		return RequestRecord{}, errors.New("review note is required for return or rejection")
	}
	var next domain.RequestStatus
	switch action {
	case "approve":
		next = domain.StatusApproved
	case "return":
		next = domain.StatusNeedsChanges
	case "reject":
		next = domain.StatusRejected
	default:
		return RequestRecord{}, domain.ErrInvalidState
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rec, err := getRequestRecordTx(ctx, tx, requestID, true)
	if err != nil {
		return RequestRecord{}, err
	}
	if rec.Request.PrincipalID == adminID {
		return RequestRecord{}, domain.ErrForbidden
	}
	if rec.Request.Version != expectedVersion {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if rec.Request.Status != domain.StatusPendingReview && rec.Request.Status != domain.StatusNeedsChanges {
		return RequestRecord{}, domain.ErrInvalidState
	}
	before := rec.Request
	if next == domain.StatusRejected {
		if err = releaseReservations(ctx, tx, requestID); err != nil {
			return RequestRecord{}, err
		}
	}
	err = tx.QueryRow(ctx, `
		UPDATE invoice_requests SET status=$1,review_note=$2,reviewed_by=$3,
			version=version+1,updated_at=now()
		WHERE id=$4 AND version=$5 RETURNING version,updated_at`,
		next, note, adminID, requestID, expectedVersion).Scan(&rec.Request.Version, &rec.Request.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if err != nil {
		return RequestRecord{}, fmt.Errorf("persist invoice review: %w", err)
	}
	rec.Request.Status = next
	rec.Request.ReviewNote = note
	rec.Request.ReviewedBy = adminID
	if err = writeAudit(ctx, tx, actor, "invoice_request.reviewed."+action, "invoice_request", requestID, before, rec.Request); err != nil {
		return RequestRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestRecord{}, err
	}
	return rec, nil
}

func (s *Store) BeginManualIssue(ctx context.Context, adminID, requestID string, expectedVersion int64, actor AuditActor) (RequestRecord, error) {
	return s.setSimpleRequestState(ctx, adminID, requestID, expectedVersion,
		domain.StatusApproved, domain.StatusManualIssuing, "invoice_request.manual_issue_started", actor)
}

func ensureCandidateReviewerCannotIssue(ctx context.Context, tx pgx.Tx, requestID, adminID string) error {
	var reviewed bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM invoice_allocations ia
			JOIN payment_candidate_reviews pcr ON pcr.funding_lot_id=ia.funding_lot_id
			WHERE ia.invoice_request_id=$1 AND pcr.admin_id::text=$2
			  AND pcr.review_action IN ('verify_propose','verify_approve')
		)`, requestID, adminID).Scan(&reviewed)
	if err != nil {
		return err
	}
	if reviewed {
		return domain.ErrForbidden
	}
	return nil
}

func (s *Store) setSimpleRequestState(ctx context.Context, adminID, requestID string, expectedVersion int64, from, to domain.RequestStatus, action string, actor AuditActor) (RequestRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rec, err := getRequestRecordTx(ctx, tx, requestID, true)
	if err != nil {
		return RequestRecord{}, err
	}
	if rec.Request.PrincipalID == adminID {
		return RequestRecord{}, domain.ErrForbidden
	}
	if rec.Request.Version != expectedVersion {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if rec.Request.Status != from {
		return RequestRecord{}, domain.ErrInvalidState
	}
	if action == "invoice_request.manual_issue_started" {
		if err = ensureCandidateReviewerCannotIssue(ctx, tx, requestID, adminID); err != nil {
			return RequestRecord{}, err
		}
	}
	before := rec.Request
	err = tx.QueryRow(ctx, `UPDATE invoice_requests SET status=$1,issued_by=$2,version=version+1,updated_at=now() WHERE id=$3 AND version=$4 RETURNING version,updated_at`,
		to, adminID, requestID, expectedVersion).Scan(&rec.Request.Version, &rec.Request.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if err != nil {
		return RequestRecord{}, fmt.Errorf("persist request state %s: %w", to, err)
	}
	rec.Request.Status = to
	rec.Request.IssuedBy = adminID
	if err = writeAudit(ctx, tx, actor, action, "invoice_request", requestID, before, rec.Request); err != nil {
		return RequestRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestRecord{}, err
	}
	return rec, nil
}

func (s *Store) ConfirmManualIssue(ctx context.Context, in ConfirmIssueInput) (RequestRecord, error) {
	if strings.TrimSpace(in.AdminID) == "" || in.ExpectedVersion <= 0 ||
		in.IssuerSettingRevision <= 0 || len(in.IssueSnapshotCiphertext) == 0 {
		return RequestRecord{}, errors.New("admin, version and immutable issue snapshot are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var sourceInstanceID string
	if err = tx.QueryRow(ctx, `SELECT source_instance_id FROM invoice_requests WHERE id=$1`, in.RequestID).Scan(&sourceInstanceID); errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrNotFound
	} else if err != nil {
		return RequestRecord{}, err
	}
	// Acquire every source stream serialization lock before any request/lot
	// row. A blocked economic batch takes its stream lock first and may then
	// freeze requests without forming a lock-order cycle with issuance.
	if err = assertSourceFreshTx(ctx, tx, sourceInstanceID, in.Freshness); err != nil {
		return RequestRecord{}, err
	}
	// Linearize the application-layer issuer snapshot against settings updates.
	// FOR SHARE blocks an UPDATE of the singleton until this issue transaction
	// commits; a settings change that won the race is detected by revision.
	var issuerSettingRevision int64
	if err = tx.QueryRow(ctx, `
		SELECT revision FROM admin_settings WHERE singleton_id=1 FOR SHARE`).Scan(&issuerSettingRevision); errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrVersionConflict
	} else if err != nil {
		return RequestRecord{}, err
	}
	if issuerSettingRevision != in.IssuerSettingRevision {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	rec, err := getRequestRecordTx(ctx, tx, in.RequestID, true)
	if err != nil {
		return RequestRecord{}, err
	}
	if rec.Request.PrincipalID == in.AdminID {
		return RequestRecord{}, domain.ErrForbidden
	}
	if rec.Request.Version != in.ExpectedVersion {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if rec.Request.Status != domain.StatusApproved && rec.Request.Status != domain.StatusManualIssuing {
		return RequestRecord{}, domain.ErrInvalidState
	}
	if err = ensureCandidateReviewerCannotIssue(ctx, tx, in.RequestID, in.AdminID); err != nil {
		return RequestRecord{}, err
	}
	if rec.Request.SourceInstanceID != sourceInstanceID {
		return RequestRecord{}, domain.ErrConflict
	}
	before := rec.Request
	if err = issueReservations(ctx, tx, in.RequestID); err != nil {
		return RequestRecord{}, err
	}
	err = tx.QueryRow(ctx, `
		UPDATE invoice_requests SET status='issued_awaiting_document',issued_by=$1,
			issuer_setting_revision=$2,issue_snapshot_ciphertext=$3,
			version=version+1,updated_at=now()
		WHERE id=$4 AND version=$5 RETURNING version,updated_at`,
		in.AdminID, in.IssuerSettingRevision, in.IssueSnapshotCiphertext,
		in.RequestID, in.ExpectedVersion).Scan(&rec.Request.Version, &rec.Request.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.ErrVersionConflict
	}
	if err != nil {
		return RequestRecord{}, fmt.Errorf("confirm manual invoice issue: %w", err)
	}
	rec.Request.Status = domain.StatusIssuedAwaitingDocument
	rec.Request.IssuedBy = in.AdminID
	rec.IssuerSettingRevision = in.IssuerSettingRevision
	rec.IssueSnapshotCiphertext = append([]byte(nil), in.IssueSnapshotCiphertext...)
	if err = writeAudit(ctx, tx, in.Actor, "invoice_request.manual_issue_confirmed", "invoice_request", in.RequestID, before, map[string]any{
		"request": rec.Request, "issuer_setting_revision": in.IssuerSettingRevision,
	}); err != nil {
		return RequestRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestRecord{}, err
	}
	return rec, nil
}

func (s *Store) AttachDocument(ctx context.Context, in AttachDocumentInput) (RequestRecord, domain.InvoiceDocument, domain.EmailOutbox, error) {
	doc := in.Document
	if err := doc.Validate(); err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	if in.ExpectedVersion <= 0 || strings.TrimSpace(in.RecipientHash) == "" || strings.TrimSpace(in.TemplateVersion) == "" {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, errors.New("document version, recipient hash and template are required")
	}
	if doc.ID == "" {
		doc.ID = randomUUID()
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	doc.IssuedAt = doc.IssuedAt.UTC().Truncate(time.Microsecond)
	doc.CreatedAt = doc.CreatedAt.UTC().Truncate(time.Microsecond)
	if strings.TrimSpace(in.Actor.ID) == "" {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, errors.New("authenticated uploader is required")
	}
	doc.UploadedBy = in.Actor.ID
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rec, err := getRequestRecordTx(ctx, tx, doc.RequestID, true)
	if err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	if rec.Request.PrincipalID == in.Actor.ID {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrForbidden
	}
	if doc.IssuedAt.Before(rec.Request.SubmittedAt) || doc.IssuedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, errors.New("invoice issue time is outside the accepted request window")
	}
	// Exact retries return the original document/outbox even when the caller's
	// version is now stale.  A different document for an issued request fails.
	existingDoc, existingOutbox, found, err := getAttachedDocumentTx(ctx, tx, doc.RequestID)
	if err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	if found {
		if sameDocument(existingDoc, doc) {
			if err = tx.Commit(ctx); err != nil {
				return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
			}
			return rec, existingDoc, existingOutbox, nil
		}
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrConflict
	}
	if rec.Request.Version != in.ExpectedVersion {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrVersionConflict
	}
	if rec.Request.Status != domain.StatusIssuedAwaitingDocument {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrInvalidState
	}
	before := rec.Request
	_, err = tx.Exec(ctx, `
		INSERT INTO invoice_documents(
			id,invoice_request_id,invoice_number,object_key,object_version,sha256,
			size_bytes,mime_type,scan_status,uploaded_by,issued_at,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		doc.ID, doc.RequestID, doc.InvoiceNumber, doc.ObjectKey, doc.ObjectVersion,
		doc.SHA256, doc.SizeBytes, doc.MIME, doc.ScanStatus, doc.UploadedBy,
		doc.IssuedAt, doc.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514") {
			return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, fmt.Errorf("%w: invoice document conflicts with an existing record", domain.ErrConflict)
		}
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, fmt.Errorf("insert invoice document: %w", err)
	}
	outbox := domain.EmailOutbox{
		ID: randomUUID(), RequestID: doc.RequestID, DocumentID: doc.ID,
		RecipientHash: in.RecipientHash, TemplateVersion: in.TemplateVersion,
		Status: "queued", NextAttemptAt: time.Now().UTC(), CreatedAt: time.Now().UTC(),
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO email_outbox(
			id,invoice_request_id,document_id,recipient_hash,template_version,status,
			attempt_count,next_attempt_at,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'queued',0,$6,$7,$7)`,
		outbox.ID, outbox.RequestID, outbox.DocumentID, outbox.RecipientHash,
		outbox.TemplateVersion, outbox.NextAttemptAt, outbox.CreatedAt)
	if err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, fmt.Errorf("queue invoice email: %w", err)
	}
	err = tx.QueryRow(ctx, `
		UPDATE invoice_requests SET status='issued',version=version+1,updated_at=now()
		WHERE id=$1 AND version=$2 RETURNING version,updated_at`,
		doc.RequestID, in.ExpectedVersion).Scan(&rec.Request.Version, &rec.Request.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrVersionConflict
	}
	if err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, fmt.Errorf("mark invoice issued: %w", err)
	}
	rec.Request.Status = domain.StatusIssued
	if err = writeAudit(ctx, tx, in.Actor, "invoice_request.document_attached", "invoice_request", doc.RequestID, before, rec.Request); err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestRecord{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	return rec, doc, outbox, nil
}

func getAttachedDocumentTx(ctx context.Context, tx pgx.Tx, requestID string) (domain.InvoiceDocument, domain.EmailOutbox, bool, error) {
	var doc domain.InvoiceDocument
	var out domain.EmailOutbox
	err := tx.QueryRow(ctx, `
		SELECT d.id,d.invoice_request_id,d.invoice_number,d.object_key,d.object_version,
			d.sha256,d.size_bytes,d.mime_type,d.scan_status,d.uploaded_by,d.issued_at,d.created_at,
			o.id,o.recipient_hash,o.template_version,o.status,o.attempt_count,o.next_attempt_at,o.created_at
		FROM invoice_documents d JOIN email_outbox o ON o.document_id=d.id
		WHERE d.invoice_request_id=$1`, requestID).Scan(
		&doc.ID, &doc.RequestID, &doc.InvoiceNumber, &doc.ObjectKey, &doc.ObjectVersion,
		&doc.SHA256, &doc.SizeBytes, &doc.MIME, &doc.ScanStatus, &doc.UploadedBy,
		&doc.IssuedAt, &doc.CreatedAt, &out.ID, &out.RecipientHash, &out.TemplateVersion,
		&out.Status, &out.Attempts, &out.NextAttemptAt, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceDocument{}, domain.EmailOutbox{}, false, nil
	}
	if err != nil {
		return domain.InvoiceDocument{}, domain.EmailOutbox{}, false, err
	}
	out.RequestID = doc.RequestID
	out.DocumentID = doc.ID
	return doc, out, true, nil
}

func sameDocument(a, b domain.InvoiceDocument) bool {
	return a.RequestID == b.RequestID && a.InvoiceNumber == b.InvoiceNumber &&
		a.ObjectKey == b.ObjectKey && a.ObjectVersion == b.ObjectVersion &&
		a.SHA256 == b.SHA256 && a.SizeBytes == b.SizeBytes && a.MIME == b.MIME &&
		a.ScanStatus == b.ScanStatus && a.IssuedAt.UnixMicro() == b.IssuedAt.UnixMicro()
}

// GetDocumentForRequest: a request belonging to a different platform than
// the scoped session (XM-INV-PLATFORM-SCOPE) is excluded by the same
// WHERE clause as "does not exist" or "not this principal's" -- the query
// itself cannot distinguish those cases, which is exactly the no-existence
// -leak behavior CR-0003 requires for a platform-scoped session.
func (s *Store) GetDocumentForRequest(ctx context.Context, principalID, requestID string, platform domain.SourceType) (domain.InvoiceDocument, error) {
	var doc domain.InvoiceDocument
	err := s.pool.QueryRow(ctx, `
		SELECT d.id,d.invoice_request_id,d.invoice_number,d.object_key,d.object_version,
			d.sha256,d.size_bytes,d.mime_type,d.scan_status,d.uploaded_by,d.issued_at,d.created_at
		FROM invoice_documents d
			JOIN invoice_requests ir ON ir.id=d.invoice_request_id
			JOIN source_instances si ON si.id=ir.source_instance_id
		WHERE d.invoice_request_id=$1 AND ir.invoice_user_id=$2 AND ir.status='issued'
			AND ($3='' OR si.source_type=$3)`,
		requestID, principalID, string(platform)).Scan(&doc.ID, &doc.RequestID, &doc.InvoiceNumber,
		&doc.ObjectKey, &doc.ObjectVersion, &doc.SHA256, &doc.SizeBytes, &doc.MIME,
		&doc.ScanStatus, &doc.UploadedBy, &doc.IssuedAt, &doc.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceDocument{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.InvoiceDocument{}, err
	}
	return doc, nil
}

func (s *Store) GetDocumentForRequestAsAdmin(ctx context.Context, requestID string) (domain.InvoiceDocument, error) {
	var doc domain.InvoiceDocument
	err := s.pool.QueryRow(ctx, `
		SELECT d.id,d.invoice_request_id,d.invoice_number,d.object_key,d.object_version,
			d.sha256,d.size_bytes,d.mime_type,d.scan_status,d.uploaded_by,d.issued_at,d.created_at
		FROM invoice_documents d JOIN invoice_requests ir ON ir.id=d.invoice_request_id
		WHERE d.invoice_request_id=$1 AND ir.status IN ('issued','refund_attention')`, requestID).Scan(
		&doc.ID, &doc.RequestID, &doc.InvoiceNumber, &doc.ObjectKey, &doc.ObjectVersion,
		&doc.SHA256, &doc.SizeBytes, &doc.MIME, &doc.ScanStatus, &doc.UploadedBy,
		&doc.IssuedAt, &doc.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceDocument{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.InvoiceDocument{}, err
	}
	return doc, nil
}
