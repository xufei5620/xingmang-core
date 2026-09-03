package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"invoice-system/backend/internal/domain"
)

// EligibilityLedgerEntry is one external account's admin-facing ledger row
// (design doc 2026-09-03-xm-inv-eligibility-simplification-design.md,
// section 3(E) -- XM-INV-USER-LEDGER-QUERY, slice 4). It is read-only: no
// method on this file ever writes to any table.
//
// ConsumedMinor/InvoiceableMinor deliberately use the identical formula
// EligibilitySummary.ConsumedMinor/AvailableMinor already uses
// (ListEligibilitySummaries's own LATERAL subquery, this file) rather than
// re-deriving it -- see eligibilityLedgerSelect's comment for why the SQL
// text is duplicated instead of extracted into a shared helper.
type EligibilityLedgerEntry struct {
	// externalAccountID is the store's own keyset cursor column. It is
	// deliberately unexported (and never surfaced by the HTTP DTO) --
	// design section 3(E)'s wire contract has no per-item id field; only
	// the page envelope's opaque next_before_id carries it forward.
	externalAccountID string

	SourceInstanceID string
	SourceType       domain.SourceType
	SourceName       string
	// ExternalUserID is the upstream platform's own (digital) user ID,
	// plain and unmasked -- identical posture to EligibilityFreeze's own
	// ExternalUserID field (see eligibilityFreezeDTO's comment in httpapi).
	ExternalUserID string

	RechargedSincePolicyStartMinor int64
	ConsumedMinor                  int64
	InvoiceableMinor               int64

	EligibilityStatus string

	// BlockReason/BlockDetail/BlockSince are empty/zero unless
	// EligibilityStatus is 'not_invoiceable_pending_reconciliation' (read
	// from source_account_eligibility_state's own plaintext columns) or
	// 'frozen' (read from the latest open eligibility_freezes row instead
	// -- never from the audit log, per design section 3(E)). A 'frozen'
	// account with no open eligibility_freezes row (defensive: should not
	// happen by construction, but not assumed) leaves all three empty
	// rather than erroring.
	BlockReason string
	BlockDetail string
	BlockSince  time.Time
}

type EligibilityLedgerPageQuery struct {
	Limit int
	// ExternalUserID filters to an exact match on external_accounts.
	// external_user_id, mirroring EligibilityFreezePageQuery's own field
	// (including its validation bounds) for the same disambiguation need.
	ExternalUserID string
	BeforeID       string
}

type EligibilityLedgerPage struct {
	Items        []EligibilityLedgerEntry
	HasMore      bool
	NextBeforeID string
}

// eligibilityLedgerSelect's two LATERAL subqueries mirror
// ListEligibilitySummaries's own "l" LATERAL subquery
// (eligibility_operations.go) verbatim for consumed/invoiceable (same
// filters: currency='CNY', eligibility_kind IN ('WALLET_CASH',
// 'SUBSCRIPTION_CASH'), verification_state='verified', refund_frozen
// excluded from the invoiceable sum only). They are not extracted into a
// shared helper: eligibility_operations.go is being touched by a concurrent
// slice in this same project (queue-narrowing repair tooling), so this file
// stays additive-only and duplicates the formula text instead, to avoid a
// merge collision on unrelated work. See docs/handoffs/XM-INV-USER-LEDGER-QUERY.md
// for this decision.
//
// recharged mirrors design section 3(E)'s own formula literally:
// completed_at>=account.cutover_at (inclusive), summed over verified_cash_minor
// for WALLET_CASH/SUBSCRIPTION_CASH lots -- no verification_state/refund_frozen
// filter, matching the design doc's wording exactly (unlike the internal
// projection query in consumption.go, which additionally requires
// completed_at>account.CutoverAt, exclusive, and is scoped per eligibility_kind
// separately). cutover_at is read as it stands today (design section 3(D),
// the policy-start re-anchor, is a later, independent slice not landed in
// this branch) -- see the handoff for the pre-(D) caveat this implies for
// legacy-bootstrapped accounts.
const eligibilityLedgerSelect = `
	SELECT ea.id::text,si.id::text,si.source_type,si.name,ea.external_user_id,
		eas.eligibility_status,
		COALESCE(r.recharged,0)::bigint,
		COALESCE(l.consumed,0)::bigint,
		COALESCE(l.available,0)::bigint,
		COALESCE(eas.pending_reconciliation_reason,''),
		COALESCE(eas.pending_reconciliation_detail,''),
		COALESCE(eas.pending_reconciliation_since,'epoch'::timestamptz),
		COALESCE(f.freeze_reason,''),
		COALESCE(f.trigger_object_type,''),
		COALESCE(f.trigger_object_id,''),
		COALESCE(f.opened_at,'epoch'::timestamptz)
	FROM external_accounts ea
	JOIN source_instances si ON si.id=ea.source_instance_id
	-- Deliberately an inner join, not LEFT JOIN: recharged_since_policy_start
	-- requires a real cutover_at, and there is no meaningful placeholder for
	-- an account that has not yet been bootstrapped into this table at all
	-- (a brief window between first observation and its first checkpoint's
	-- bootstrap, per design section 2.1 -- distinct from a bootstrapped
	-- account with zero funding lots, which this query does surface, at
	-- every amount 0). See docs/handoffs/XM-INV-USER-LEDGER-QUERY.md's "Not
	-- verified" section.
	JOIN source_account_eligibility_state eas ON eas.external_account_id=ea.id
	LEFT JOIN LATERAL (
		SELECT sum(fl.verified_cash_minor) recharged
		FROM funding_lots fl
		WHERE fl.external_account_id=ea.id AND fl.currency='CNY'
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
			AND fl.completed_at>=eas.cutover_at
	) r ON true
	LEFT JOIN LATERAL (
		SELECT sum(fl.consumed_cash_minor) consumed,
			sum(CASE WHEN fl.refund_frozen=FALSE THEN GREATEST(fl.consumed_cash_minor-fl.reserved_minor-fl.issued_minor,0) ELSE 0 END) available
		FROM funding_lots fl
		WHERE fl.external_account_id=ea.id AND fl.currency='CNY'
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH') AND fl.verification_state='verified'
	) l ON true
	LEFT JOIN LATERAL (
		SELECT ef.freeze_reason,ef.trigger_object_type,ef.trigger_object_id,ef.opened_at
		FROM eligibility_freezes ef
		WHERE ef.external_account_id=ea.id AND ef.status='open' AND eas.eligibility_status='frozen'
		ORDER BY ef.opened_at DESC,ef.id DESC LIMIT 1
	) f ON true`

var epochSentinel = time.Unix(0, 0).UTC()

func scanEligibilityLedgerEntry(row pgxRow) (EligibilityLedgerEntry, error) {
	var item EligibilityLedgerEntry
	var pendingReason, pendingDetail, freezeReason, triggerType, triggerID string
	var pendingSince, freezeOpenedAt time.Time
	err := row.Scan(&item.externalAccountID, &item.SourceInstanceID, &item.SourceType, &item.SourceName,
		&item.ExternalUserID, &item.EligibilityStatus,
		&item.RechargedSincePolicyStartMinor, &item.ConsumedMinor, &item.InvoiceableMinor,
		&pendingReason, &pendingDetail, &pendingSince,
		&freezeReason, &triggerType, &triggerID, &freezeOpenedAt)
	if err != nil {
		return item, err
	}
	switch item.EligibilityStatus {
	case "not_invoiceable_pending_reconciliation":
		item.BlockReason = pendingReason
		item.BlockDetail = pendingDetail
		if !pendingSince.Equal(epochSentinel) {
			item.BlockSince = pendingSince
		}
	case "frozen":
		if freezeReason != "" {
			item.BlockReason = freezeReason
			item.BlockDetail = fmt.Sprintf("trigger_object_type=%s trigger_object_id=%s", triggerType, triggerID)
			if !freezeOpenedAt.Equal(epochSentinel) {
				item.BlockSince = freezeOpenedAt
			}
		}
	}
	return item, nil
}

// ListEligibilityLedgerPage: one row per external account, admin-only, read
// only (see the design doc's own "Never read the audit log for this" --
// block_reason/block_detail/block_since always come from plaintext columns,
// never from audit_events' hashed-only records).
func (s *Store) ListEligibilityLedgerPage(ctx context.Context, in EligibilityLedgerPageQuery) (EligibilityLedgerPage, error) {
	if in.Limit <= 0 {
		in.Limit = 100
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.ExternalUserID != "" && (len(in.ExternalUserID) > 512 || strings.ContainsAny(in.ExternalUserID, "\r\n\x00")) {
		return EligibilityLedgerPage{}, errors.New("invalid external user id filter")
	}
	if in.BeforeID != "" && !eligibilityUUIDPattern.MatchString(in.BeforeID) {
		return EligibilityLedgerPage{}, errors.New("invalid eligibility ledger cursor")
	}
	query := eligibilityLedgerSelect + ` WHERE 1=1`
	args := []any{}
	add := func(clause string, value any) { args = append(args, value); query += fmt.Sprintf(clause, len(args)) }
	if in.ExternalUserID != "" {
		add(` AND ea.external_user_id=$%d`, in.ExternalUserID)
	}
	if in.BeforeID != "" {
		add(` AND ea.id<$%d::uuid`, in.BeforeID)
	}
	args = append(args, in.Limit+1)
	query += fmt.Sprintf(` ORDER BY ea.id DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return EligibilityLedgerPage{}, err
	}
	defer rows.Close()
	items := make([]EligibilityLedgerEntry, 0, in.Limit+1)
	for rows.Next() {
		item, scanErr := scanEligibilityLedgerEntry(rows)
		if scanErr != nil {
			return EligibilityLedgerPage{}, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return EligibilityLedgerPage{}, err
	}
	page := EligibilityLedgerPage{HasMore: len(items) > in.Limit}
	if page.HasMore {
		items = items[:in.Limit]
	}
	page.Items = items
	if page.HasMore && len(items) > 0 {
		page.NextBeforeID = items[len(items)-1].externalAccountID
	}
	return page, nil
}
