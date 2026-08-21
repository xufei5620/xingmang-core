package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

var eligibilityUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var eligibilityFreezeReasons = map[string]struct{}{
	"UNKNOWN_NEGATIVE_BALANCE": {}, "LATE_FINALIZED_EVENT": {},
	"AMBIGUOUS_EVENT_ORDER": {}, "EVENT_PAYLOAD_DRIFT": {},
	"UNIT_MISMATCH": {}, "USAGE_EXCEEDS_LEDGER": {},
	"STREAM_WATERMARK_REGRESSION": {}, "SOURCE_GAP": {}, "SOURCE_REFUND": {},
}

type EligibilityFreeze struct {
	ID                string            `json:"id"`
	PrincipalID       string            `json:"-"`
	ExternalAccountID string            `json:"-"`
	SourceInstanceID  string            `json:"source_instance_id"`
	SourceType        domain.SourceType `json:"source_type"`
	SourceName        string            `json:"source_name"`
	FundingLotID      string            `json:"funding_lot_id,omitempty"`
	FreezeReason      string            `json:"freeze_reason"`
	Status            string            `json:"status"`
	EligibilityStatus string            `json:"eligibility_status"`
	OpenedAt          time.Time         `json:"opened_at"`
	ResolvedAt        time.Time         `json:"resolved_at,omitempty"`
	ResolutionVersion int64             `json:"version"`
	EvidenceHash      string            `json:"-"`
	NoteHash          string            `json:"-"`
}

type EligibilityFreezePageQuery struct {
	Limit            int
	Status           string
	FreezeReason     string
	SourceInstanceID string
	BeforeOpenedAt   time.Time
	BeforeID         string
}

type EligibilityFreezePage struct {
	Items              []EligibilityFreeze
	HasMore            bool
	NextBeforeOpenedAt time.Time
	NextBeforeID       string
}

type ResolveEligibilityFreezeInput struct {
	FreezeID           string
	ExpectedVersion    int64
	EvidenceHash       string
	EvidenceCiphertext []byte
	NoteHash           string
	NoteCiphertext     []byte
	FreshnessPolicy    SourceFreshnessPolicy
	Actor              AuditActor
}

type EligibilitySummary struct {
	SourceInstanceID    string
	SourceType          domain.SourceType
	SourceName          string
	BindingStatus       string
	EligibilityStatus   string
	UnitCode            string
	AvailableMinor      int64
	ConsumedMinor       int64
	UnconsumedMinor     int64
	ReservedMinor       int64
	IssuedMinor         int64
	LegacyServiceUnits  string
	NonCashServiceUnits string
	HasOpenFreeze       bool
	ProjectionPending   bool
}

func scanEligibilityFreeze(row pgxRow) (EligibilityFreeze, error) {
	var item EligibilityFreeze
	err := row.Scan(&item.ID, &item.PrincipalID, &item.ExternalAccountID, &item.SourceInstanceID, &item.SourceType,
		&item.SourceName, &item.FundingLotID, &item.FreezeReason, &item.Status, &item.EligibilityStatus,
		&item.OpenedAt, &item.ResolvedAt, &item.ResolutionVersion, &item.EvidenceHash, &item.NoteHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, domain.ErrNotFound
	}
	if err != nil {
		return item, err
	}
	if item.ResolvedAt.Equal(time.Unix(0, 0).UTC()) {
		item.ResolvedAt = time.Time{}
	}
	return item, nil
}

const eligibilityFreezeSelect = `
	SELECT ef.id,ea.invoice_user_id::text,ef.external_account_id::text,eas.source_instance_id::text,
		si.source_type,si.name,COALESCE(ef.funding_lot_id::text,''),ef.freeze_reason,ef.status,
		eas.eligibility_status,ef.opened_at,COALESCE(ef.resolved_at,'epoch'::timestamptz),
		ef.resolution_version,COALESCE(ef.resolution_evidence_hash,''),COALESCE(ef.resolution_note_hash,'')
	FROM eligibility_freezes ef
	JOIN external_accounts ea ON ea.id=ef.external_account_id
	JOIN source_account_eligibility_state eas ON eas.external_account_id=ef.external_account_id
	JOIN source_instances si ON si.id=eas.source_instance_id`

func (s *Store) ListEligibilityFreezesPage(ctx context.Context, in EligibilityFreezePageQuery) (EligibilityFreezePage, error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Status == "" {
		in.Status = "open"
	}
	if in.Status != "open" && in.Status != "resolved" && in.Status != "all" {
		return EligibilityFreezePage{}, errors.New("invalid eligibility freeze status")
	}
	if in.FreezeReason != "" {
		if _, ok := eligibilityFreezeReasons[in.FreezeReason]; !ok {
			return EligibilityFreezePage{}, errors.New("invalid eligibility freeze reason")
		}
	}
	if in.SourceInstanceID != "" && !eligibilityUUIDPattern.MatchString(in.SourceInstanceID) {
		return EligibilityFreezePage{}, errors.New("invalid source instance filter")
	}
	if in.BeforeOpenedAt.IsZero() != (strings.TrimSpace(in.BeforeID) == "") || in.BeforeID != "" && !eligibilityUUIDPattern.MatchString(in.BeforeID) {
		return EligibilityFreezePage{}, errors.New("both valid eligibility freeze cursor fields are required")
	}
	query := eligibilityFreezeSelect + ` WHERE 1=1`
	args := []any{}
	add := func(clause string, value any) { args = append(args, value); query += fmt.Sprintf(clause, len(args)) }
	if in.Status != "all" {
		add(` AND ef.status=$%d`, in.Status)
	}
	if in.FreezeReason != "" {
		add(` AND ef.freeze_reason=$%d`, in.FreezeReason)
	}
	if in.SourceInstanceID != "" {
		add(` AND eas.source_instance_id=$%d::uuid`, in.SourceInstanceID)
	}
	if !in.BeforeOpenedAt.IsZero() {
		args = append(args, in.BeforeOpenedAt, in.BeforeID)
		query += fmt.Sprintf(` AND (ef.opened_at,ef.id)<($%d,$%d::uuid)`, len(args)-1, len(args))
	}
	args = append(args, in.Limit+1)
	query += fmt.Sprintf(` ORDER BY ef.opened_at DESC,ef.id DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return EligibilityFreezePage{}, err
	}
	defer rows.Close()
	items := make([]EligibilityFreeze, 0, in.Limit+1)
	for rows.Next() {
		item, scanErr := scanEligibilityFreeze(rows)
		if scanErr != nil {
			return EligibilityFreezePage{}, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return EligibilityFreezePage{}, err
	}
	page := EligibilityFreezePage{HasMore: len(items) > in.Limit}
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

func (s *Store) ResolveEligibilityFreeze(ctx context.Context, in ResolveEligibilityFreezeInput) (EligibilityFreeze, error) {
	if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.FreezeID)) || in.ExpectedVersion <= 0 ||
		!hexHashPattern.MatchString(in.EvidenceHash) || !hexHashPattern.MatchString(in.NoteHash) ||
		len(in.EvidenceCiphertext) < 16 || len(in.EvidenceCiphertext) > 8192 || len(in.NoteCiphertext) < 16 || len(in.NoteCiphertext) > 8192 || !in.FreshnessPolicy.enabled() {
		return EligibilityFreeze{}, errors.New("complete encrypted eligibility resolution evidence and freshness policy are required")
	}
	actor := in.Actor.normalized()
	if actor.Type != "admin" || strings.TrimSpace(actor.ID) == "" {
		return EligibilityFreeze{}, domain.ErrForbidden
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return EligibilityFreeze{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var accountID, sourceID string
	if err = tx.QueryRow(ctx, `SELECT ef.external_account_id::text,eas.source_instance_id::text FROM eligibility_freezes ef JOIN source_account_eligibility_state eas ON eas.external_account_id=ef.external_account_id WHERE ef.id=$1`, in.FreezeID).Scan(&accountID, &sourceID); errors.Is(err, pgx.ErrNoRows) {
		return EligibilityFreeze{}, domain.ErrNotFound
	} else if err != nil {
		return EligibilityFreeze{}, err
	}
	if err = assertSourceFreshTx(ctx, tx, sourceID, in.FreshnessPolicy); err != nil {
		return EligibilityFreeze{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,47))`, accountID); err != nil {
		return EligibilityFreeze{}, err
	}
	item, err := scanEligibilityFreeze(tx.QueryRow(ctx, eligibilityFreezeSelect+` WHERE ef.id=$1 FOR UPDATE OF ef,eas`, in.FreezeID))
	if err != nil {
		return EligibilityFreeze{}, err
	}
	if item.ExternalAccountID != accountID || item.SourceInstanceID != sourceID {
		return EligibilityFreeze{}, domain.ErrConflict
	}
	if item.PrincipalID == actor.ID {
		return EligibilityFreeze{}, domain.ErrForbidden
	}
	if item.Status == "resolved" {
		if item.EvidenceHash == in.EvidenceHash && item.NoteHash == in.NoteHash {
			if err = tx.Commit(ctx); err != nil {
				return EligibilityFreeze{}, err
			}
			return item, nil
		}
		return EligibilityFreeze{}, domain.ErrVersionConflict
	}
	if item.Status != "open" || item.ResolutionVersion != in.ExpectedVersion {
		return EligibilityFreeze{}, domain.ErrVersionConflict
	}
	if item.FreezeReason == "SOURCE_REFUND" {
		return EligibilityFreeze{}, domain.ErrInvalidState
	}
	var unsafeRefund, projectionJob bool
	if err = tx.QueryRow(ctx, `
		SELECT
			EXISTS(SELECT 1 FROM eligibility_freezes f WHERE f.external_account_id=$1 AND f.status='open' AND f.freeze_reason='SOURCE_REFUND')
			OR EXISTS(SELECT 1 FROM funding_lots fl WHERE fl.external_account_id=$1 AND fl.refund_frozen)
			OR EXISTS(SELECT 1 FROM refund_cases rc JOIN funding_lots fl ON fl.id=rc.funding_lot_id WHERE fl.external_account_id=$1 AND rc.status='open')
			OR EXISTS(SELECT 1 FROM invoice_allocations ia JOIN funding_lots fl ON fl.id=ia.funding_lot_id JOIN invoice_requests ir ON ir.id=ia.invoice_request_id WHERE fl.external_account_id=$1 AND ir.status='refund_attention'),
			EXISTS(SELECT 1 FROM eligibility_projection_jobs j WHERE j.external_account_id=$1)`, accountID).Scan(&unsafeRefund, &projectionJob); err != nil {
		return EligibilityFreeze{}, err
	}
	if unsafeRefund || projectionJob {
		return EligibilityFreeze{}, domain.ErrInvalidState
	}
	var evaluation string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(e.evaluation_status,'') FROM (
			SELECT b.id FROM balance_reconciliation_checkpoints b
			JOIN source_account_eligibility_state eas ON eas.external_account_id=b.external_account_id
			WHERE b.external_account_id=$1 AND b.checkpoint_kind='reconciliation' AND b.as_of<=eas.finalized_through
			ORDER BY b.as_of DESC,b.id DESC LIMIT 1
		) latest
		LEFT JOIN LATERAL (SELECT evaluation_status FROM balance_checkpoint_evaluations e
			WHERE e.checkpoint_id=latest.id ORDER BY e.projection_version DESC LIMIT 1) e ON true`, accountID).Scan(&evaluation)
	if errors.Is(err, pgx.ErrNoRows) {
		return EligibilityFreeze{}, domain.ErrInvalidState
	}
	if err != nil {
		return EligibilityFreeze{}, err
	}
	if evaluation != "matched" && evaluation != "positive_classified_non_cash" {
		return EligibilityFreeze{}, domain.ErrInvalidState
	}
	before := item
	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		UPDATE eligibility_freezes SET status='resolved',resolved_at=$1,resolved_by=$2,
			resolution_evidence_hash=$3,resolution_evidence_ciphertext=$4,
			resolution_note_hash=$5,resolution_note_ciphertext=$6,
			resolution_version=resolution_version+1,updated_at=$1
		WHERE id=$7 AND status='open' AND resolution_version=$8
		RETURNING status,resolved_at,resolution_version,resolution_evidence_hash,resolution_note_hash`, now, actor.ID,
		in.EvidenceHash, in.EvidenceCiphertext, in.NoteHash, in.NoteCiphertext, in.FreezeID, in.ExpectedVersion).Scan(&item.Status, &item.ResolvedAt, &item.ResolutionVersion, &item.EvidenceHash, &item.NoteHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return EligibilityFreeze{}, domain.ErrVersionConflict
	}
	if err != nil {
		return EligibilityFreeze{}, err
	}
	var remaining int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&remaining); err != nil {
		return EligibilityFreeze{}, err
	}
	if remaining == 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at) SELECT external_account_id,finalized_through,'queued',now() FROM source_account_eligibility_state WHERE external_account_id=$1 ON CONFLICT(external_account_id) DO NOTHING`, accountID); err != nil {
			return EligibilityFreeze{}, err
		}
		command, updateErr := tx.Exec(ctx, `UPDATE source_account_eligibility_state SET eligibility_status='active',projection_version=projection_version+1,updated_at=now() WHERE external_account_id=$1 AND eligibility_status='frozen'`, accountID)
		if updateErr != nil {
			return EligibilityFreeze{}, updateErr
		}
		if command.RowsAffected() != 1 {
			return EligibilityFreeze{}, domain.ErrVersionConflict
		}
		item.EligibilityStatus = "active"
	}
	actor.Reason = "eligibility freeze resolution evidence sha256:" + in.EvidenceHash
	if err = writeAudit(ctx, tx, actor, "eligibility.freeze.resolved", "eligibility_freeze", item.ID, before, map[string]any{"status": item.Status, "version": item.ResolutionVersion, "remaining_open": remaining, "account_status": item.EligibilityStatus}); err != nil {
		return EligibilityFreeze{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EligibilityFreeze{}, err
	}
	return item, nil
}

func (s *Store) ListEligibilitySummaries(ctx context.Context, principalID string) ([]EligibilitySummary, error) {
	if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(principalID)) {
		return nil, domain.ErrForbidden
	}
	rows, err := s.pool.Query(ctx, `
		SELECT si.id::text,si.source_type,si.name,ea.binding_status,
			COALESCE(eas.eligibility_status,'syncing'),COALESCE(eas.unit_code,''),
			COALESCE(l.available,0)::bigint,COALESCE(l.consumed,0)::bigint,
			COALESCE(l.unconsumed,0)::bigint,COALESCE(l.reserved,0)::bigint,COALESCE(l.issued,0)::bigint,
			COALESCE(c.legacy,eas.cutover_balance_units,0)::text,
			COALESCE(c.noncash,0)::text,
			EXISTS(SELECT 1 FROM eligibility_freezes f WHERE f.external_account_id=ea.id AND f.status='open'),
			EXISTS(SELECT 1 FROM eligibility_projection_jobs j WHERE j.external_account_id=ea.id)
		FROM external_accounts ea JOIN source_instances si ON si.id=ea.source_instance_id
		LEFT JOIN source_account_eligibility_state eas ON eas.external_account_id=ea.id
		LEFT JOIN LATERAL (
			SELECT sum(CASE WHEN fl.refund_frozen=FALSE THEN GREATEST(fl.consumed_cash_minor-fl.reserved_minor-fl.issued_minor,0) ELSE 0 END) available,
				sum(fl.consumed_cash_minor) consumed,
				sum(GREATEST(fl.verified_cash_minor-fl.consumed_cash_minor,0)) unconsumed,
				sum(fl.reserved_minor) reserved,sum(fl.issued_minor) issued
			FROM funding_lots fl WHERE fl.external_account_id=ea.id AND fl.currency='CNY'
				AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH') AND fl.verification_state='verified'
		) l ON true
		LEFT JOIN LATERAL (
			SELECT sum(service_units) FILTER (WHERE credit_kind='LEGACY_NON_INVOICEABLE') legacy,
				sum(service_units) FILTER (WHERE credit_kind<>'LEGACY_NON_INVOICEABLE') noncash
			FROM source_credit_events ce WHERE ce.external_account_id=ea.id
		) c ON true
		WHERE ea.invoice_user_id=$1 ORDER BY si.source_type,si.name,si.id`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EligibilitySummary{}
	for rows.Next() {
		var item EligibilitySummary
		if err = rows.Scan(&item.SourceInstanceID, &item.SourceType, &item.SourceName, &item.BindingStatus, &item.EligibilityStatus, &item.UnitCode, &item.AvailableMinor, &item.ConsumedMinor, &item.UnconsumedMinor, &item.ReservedMinor, &item.IssuedMinor, &item.LegacyServiceUnits, &item.NonCashServiceUnits, &item.HasOpenFreeze, &item.ProjectionPending); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
