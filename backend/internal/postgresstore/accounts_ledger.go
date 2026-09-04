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

// This file implements CR-0009's operator "用户账本" (user ledger) view --
// XM-INV-CR0009-LEDGER-VIEW, finalizing XM-INV-USER-LEDGER-QUERY (design
// doc 2026-09-03-xm-inv-eligibility-simplification-design.md, section 3(E))
// into the CR's own exact contract. It replaces the provisional
// GET /api/v1/admin/eligibility-ledger route (this file used to be named
// eligibility_ledger.go) with two read-only endpoints:
// GET /api/v1/admin/accounts/ledger (list, ListAccountLedgerPage) and
// GET /api/v1/admin/accounts/{external_account_id}/ledger (detail,
// GetAccountLedgerDetail). Both are strictly read-only: no method in this
// file ever writes to any table.

// Account block_state, CR-0009 "变更范围" item 3: a read-only composition of
// existing signals, no new judgement condition. See accountBlockStateExpr
// for the exact logic (kept in one place, shared by the list query, the
// detail query and the block_state-sort cursor lookup, so there is exactly
// one definition of "what state is this account in").
const (
	AccountBlockStateFrozenManualReview                  = "frozen_manual_review"
	AccountBlockStateNotInvoiceablePendingReconciliation = "not_invoiceable_pending_reconciliation"
	AccountBlockStateBelowThreshold                      = "below_threshold"
	AccountBlockStateInvoiceable                         = "invoiceable"
)

// accountLedgerRechargesLateral: recharges_since_start_count/minor, CR-0009
// "变更范围" item 2's own formula -- eligibility_kind IN (WALLET_CASH,
// SUBSCRIPTION_CASH), verification_state='verified', currency='CNY',
// completed_at>=eas.cutover_at (inclusive, design section 3(E)'s literal
// wording). verification_state='verified' is included here even though
// slice 4's own provisional eligibility_ledger.go omitted it for this exact
// sum -- corrected here to match design section 2's fuller formula and every
// other WALLET_CASH/SUBSCRIPTION_CASH formula in this codebase (see
// docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md for why this is flagged as a
// deliberate correction, not an unexplained behavior change).
const accountLedgerRechargesLateral = `
	LEFT JOIN LATERAL (
		SELECT count(*) cnt, sum(fl.verified_cash_minor) minor
		FROM funding_lots fl
		WHERE fl.external_account_id=ea.id AND fl.currency='CNY'
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
			AND fl.verification_state='verified'
			AND fl.completed_at>=eas.cutover_at
	) r ON true`

// accountLedgerConsumedInvoiceableLateral mirrors ListEligibilitySummaries's
// own "l" LATERAL (eligibility_operations.go) verbatim for
// consumed/invoiceable/issued -- same filters (currency='CNY',
// eligibility_kind IN (WALLET_CASH,SUBSCRIPTION_CASH),
// verification_state='verified'), refund_frozen excluded from the
// invoiceable sum only. No completed_at>=cutover_at filter here: unlike
// recharges above, every WALLET_CASH/SUBSCRIPTION_CASH lot is already
// guaranteed completed_at>=policy start by enforce_funding_lot_invoice_policy
// (migration 0016), so this sum is already "since start" by construction --
// adding the filter would be redundant, and ListEligibilitySummaries/the
// original ledger query never applied it either. Not extracted into a
// shared helper with eligibility_operations.go: that file hosts concurrent
// slices' own tooling, so this formula stays a documented, deliberate
// SQL-text duplicate (see docs/handoffs/XM-INV-USER-LEDGER-QUERY.md).
const accountLedgerConsumedInvoiceableLateral = `
	LEFT JOIN LATERAL (
		SELECT sum(fl.consumed_cash_minor) consumed,
			sum(CASE WHEN fl.refund_frozen=FALSE THEN GREATEST(fl.consumed_cash_minor-fl.reserved_minor-fl.issued_minor,0) ELSE 0 END) available,
			sum(fl.issued_minor) issued
		FROM funding_lots fl
		WHERE fl.external_account_id=ea.id AND fl.currency='CNY'
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH') AND fl.verification_state='verified'
	) l ON true`

// accountLedgerLatestEvaluationLateral mirrors queue_narrow_repair.go's
// latestBalanceEvaluationTx exactly (same UNION ALL of
// balance_reconciliation_checkpoints/balance_carry_forward_proofs, same "at
// or before finalized_through" bound, same as_of/source_sequence/id DESC
// ordering) as a per-row correlated LATERAL instead of a
// one-account-at-a-time tx helper, so it can drive a paginated list.
// Deliberately duplicated rather than reusing that function directly: it
// takes a pgx.Tx (transactional callers only) and this endpoint is a plain
// pool read across up to 100 rows -- see
// docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md.
const accountLedgerLatestEvaluationLateral = `
	LEFT JOIN LATERAL (
		SELECT ev.kind,ev.key,ev.as_of,ev.evaluation_status,ev.expected_units,ev.difference_units
		FROM (
			SELECT 'balance_checkpoint'::text kind,c.checkpoint_id key,c.as_of,c.source_sequence,c.id,
				(SELECT e.evaluation_status FROM balance_checkpoint_evaluations e WHERE e.checkpoint_id=c.id ORDER BY e.projection_version DESC LIMIT 1) evaluation_status,
				(SELECT e.expected_service_units::text FROM balance_checkpoint_evaluations e WHERE e.checkpoint_id=c.id ORDER BY e.projection_version DESC LIMIT 1) expected_units,
				(SELECT e.difference_service_units::text FROM balance_checkpoint_evaluations e WHERE e.checkpoint_id=c.id ORDER BY e.projection_version DESC LIMIT 1) difference_units
			FROM balance_reconciliation_checkpoints c
			WHERE c.external_account_id=ea.id AND c.checkpoint_kind='reconciliation' AND c.as_of<=eas.finalized_through
			UNION ALL
			SELECT 'balance_carry_forward_proof',p.proof_key,p.as_of,p.source_sequence,p.id,
				(SELECT e.evaluation_status FROM balance_carry_forward_evaluations e WHERE e.proof_id=p.id ORDER BY e.projection_version DESC LIMIT 1),
				(SELECT e.expected_service_units::text FROM balance_carry_forward_evaluations e WHERE e.proof_id=p.id ORDER BY e.projection_version DESC LIMIT 1),
				(SELECT e.difference_service_units::text FROM balance_carry_forward_evaluations e WHERE e.proof_id=p.id ORDER BY e.projection_version DESC LIMIT 1)
			FROM balance_carry_forward_proofs p
			WHERE p.external_account_id=ea.id AND p.as_of<=eas.finalized_through
		) ev
		WHERE ev.evaluation_status IS NOT NULL
		ORDER BY ev.as_of DESC, ev.source_sequence DESC, ev.id DESC LIMIT 1
	) latest_eval ON true`

// accountLedgerLastReconciledLateral is the same shape as
// accountLedgerLatestEvaluationLateral above, filtered to
// evaluation_status='matched' only -- "the last time books actually
// balanced", distinct from last_checkpoint_at above ("the last time the
// source reported anything at all", which can be a mismatch). Only
// consumed by the detail endpoint (last_reconciled_at is not part of the
// list contract).
const accountLedgerLastReconciledLateral = `
	LEFT JOIN LATERAL (
		SELECT ev.as_of
		FROM (
			SELECT c.as_of,c.source_sequence,c.id,
				(SELECT e.evaluation_status FROM balance_checkpoint_evaluations e WHERE e.checkpoint_id=c.id ORDER BY e.projection_version DESC LIMIT 1) evaluation_status
			FROM balance_reconciliation_checkpoints c
			WHERE c.external_account_id=ea.id AND c.checkpoint_kind='reconciliation' AND c.as_of<=eas.finalized_through
			UNION ALL
			SELECT p.as_of,p.source_sequence,p.id,
				(SELECT e.evaluation_status FROM balance_carry_forward_evaluations e WHERE e.proof_id=p.id ORDER BY e.projection_version DESC LIMIT 1)
			FROM balance_carry_forward_proofs p
			WHERE p.external_account_id=ea.id AND p.as_of<=eas.finalized_through
		) ev
		WHERE ev.evaluation_status='matched'
		ORDER BY ev.as_of DESC, ev.source_sequence DESC, ev.id DESC LIMIT 1
	) last_matched ON true`

// accountLedgerFreezeLateral: the latest open eligibility_freezes row (by
// opened_at, tie-broken by id -- matching slice 4's own precedent for
// "surface the latest, not merely any open row"), plus its linked funding
// lot's issued_minor when one exists (only ever non-NULL for the precise,
// funding_lot-scoped LATE_FINALIZED_EVENT red-reversal reason -- used to
// give that specific block_reason sentence a real CNY amount).
// has_open_freeze (in accountLedgerSignalsSelectList) is derived from this
// lateral's own freeze_reason column rather than a separate EXISTS, so
// there is exactly one query deciding "does this account have an open
// freeze".
const accountLedgerFreezeLateral = `
	LEFT JOIN LATERAL (
		SELECT ef.freeze_reason,ef.trigger_object_type,ef.trigger_object_id,ef.opened_at,fl2.issued_minor
		FROM eligibility_freezes ef
		LEFT JOIN funding_lots fl2 ON fl2.id=ef.funding_lot_id
		WHERE ef.external_account_id=ea.id AND ef.status='open'
		ORDER BY ef.opened_at DESC,ef.id DESC LIMIT 1
	) frz ON true`

// accountLedgerSignalsSelectList is the full set of raw, per-account signal
// columns shared by the list query, the detail query and the block_state
// -sort cursor-rank lookup -- exactly one definition of every fact
// block_state/block_reason are built from. Callers select whichever subset
// of these named columns they need from the "signals" CTE.
const accountLedgerSignalsSelectList = `
	ea.id::text AS account_id,
	si.source_type,
	ea.external_user_id,
	policy.eligibility_start_at AS policy_start_at,
	COALESCE(r.cnt,0)::int AS recharges_count,
	COALESCE(r.minor,0)::bigint AS recharges_minor,
	COALESCE(l.consumed,0)::bigint AS consumed_minor,
	COALESCE(l.available,0)::bigint AS invoiceable_minor,
	COALESCE(l.issued,0)::bigint AS issued_minor,
	(frz.freeze_reason IS NOT NULL) AS has_open_freeze,
	eas.eligibility_status,
	EXISTS(SELECT 1 FROM eligibility_projection_jobs j WHERE j.external_account_id=ea.id) AS has_projection_job,
	COALESCE(latest_eval.evaluation_status,'') AS latest_evaluation_status,
	latest_eval.kind AS latest_evaluation_kind,
	latest_eval.key AS latest_evaluation_key,
	latest_eval.as_of AS last_checkpoint_at,
	latest_eval.expected_units AS latest_evaluation_expected_units,
	latest_eval.difference_units AS latest_evaluation_difference_units,
	last_matched.as_of AS last_reconciled_at,
	frz.freeze_reason,
	frz.trigger_object_type AS freeze_trigger_type,
	frz.trigger_object_id AS freeze_trigger_id,
	frz.opened_at AS freeze_opened_at,
	frz.issued_minor AS freeze_lot_issued_minor,
	eas.pending_reconciliation_reason,
	eas.pending_reconciliation_trigger_type,
	eas.pending_reconciliation_trigger_id,
	eas.pending_reconciliation_since,
	eas.cutover_at,
	eas.cutover_balance_units::text AS opening_balance_units,
	eas.unit_code AS opening_balance_unit_code`

const accountLedgerFrom = `
	FROM external_accounts ea
	JOIN source_instances si ON si.id=ea.source_instance_id
	JOIN source_account_eligibility_state eas ON eas.external_account_id=ea.id
	CROSS JOIN invoice_eligibility_policy policy` +
	accountLedgerRechargesLateral +
	accountLedgerConsumedInvoiceableLateral +
	accountLedgerLatestEvaluationLateral +
	accountLedgerLastReconciledLateral +
	accountLedgerFreezeLateral

// accountBlockStateExpr and accountBlockStateRankExpr are the single
// definition of CR-0009 "变更范围" item 3's block_state logic, referencing
// accountLedgerSignalsSelectList's own output column names -- thresholdSQL
// is the already-formatted SQL text for the threshold_minor bind parameter
// (e.g. "$3"), so both expressions can be reused verbatim across the list
// query, the detail query and the block_state-sort cursor lookup with
// whatever placeholder number that particular query assigned it.
//
//   - frozen_manual_review: an open eligibility_freezes row, OR
//     eligibility_status='frozen' even with none (defensive -- "should not
//     happen by construction" per slice 4's own comment, but the task
//     brief's hard rule requires this exact shape to still produce a
//     sensible row rather than falling through to a wrong state).
//   - not_invoiceable_pending_reconciliation: eligibility_status is that
//     value, OR any row in eligibility_projection_jobs (that table only
//     ever holds queued/processing/failed rows -- see its own migration
//     comment -- so existence alone means "not yet finalized"), OR the
//     latest balance evaluation is not one of the three "good" outcomes
//     (matched/positive_classified_non_cash/positive_blip_ignored) --
//     including "no evaluation recorded at all" (latest_evaluation_status
//     COALESCEd to ” upstream, which is trivially NOT IN the good set),
//     matching ResolveEligibilityFreeze's own existing precedent of
//     treating "no evaluation yet" as unmatched, not as a free pass.
//   - below_threshold: none of the above, but invoiceable_minor is under
//     the real, admin-configurable minimum invoice amount.
//   - invoiceable: otherwise.
func accountBlockStateExpr(thresholdSQL string) string {
	return `CASE
		WHEN has_open_freeze OR eligibility_status='frozen' THEN '` + AccountBlockStateFrozenManualReview + `'
		WHEN eligibility_status='` + AccountBlockStateNotInvoiceablePendingReconciliation + `'
			OR has_projection_job
			OR latest_evaluation_status NOT IN ('matched','positive_classified_non_cash','positive_blip_ignored')
			THEN '` + AccountBlockStateNotInvoiceablePendingReconciliation + `'
		WHEN invoiceable_minor<` + thresholdSQL + ` THEN '` + AccountBlockStateBelowThreshold + `'
		ELSE '` + AccountBlockStateInvoiceable + `'
	END`
}

func accountBlockStateRankExpr(thresholdSQL string) string {
	return `CASE
		WHEN has_open_freeze OR eligibility_status='frozen' THEN 0
		WHEN eligibility_status='` + AccountBlockStateNotInvoiceablePendingReconciliation + `'
			OR has_projection_job
			OR latest_evaluation_status NOT IN ('matched','positive_classified_non_cash','positive_blip_ignored')
			THEN 1
		WHEN invoiceable_minor<` + thresholdSQL + ` THEN 2
		ELSE 3
	END`
}

// accountBlockStateRank is accountBlockStateRankExpr's own logic, mirrored
// in Go -- see ListAccountLedgerPage's own comment for why the
// block_state-sort cursor's rank is computed here instead of via a second
// SQL round trip re-evaluating the SQL CASE expression directly.
// TestAccountLedgerPageCoversEveryBlockStateAndFilters's own
// "keyset pagination (sort=block_state)" subtest exercises every block
// state produced by the SQL side (accountBlockStateExpr) through this same
// Go-side rank function across a real paginated walk, so a drift between
// the two would surface as an out-of-order page there.
func accountBlockStateRank(hasOpenFreeze bool, eligibilityStatus string, hasProjectionJob bool, latestEvaluationStatus string, invoiceableMinor, thresholdMinor int64) int {
	if hasOpenFreeze || eligibilityStatus == "frozen" {
		return 0
	}
	if eligibilityStatus == AccountBlockStateNotInvoiceablePendingReconciliation || hasProjectionJob ||
		(latestEvaluationStatus != "matched" && latestEvaluationStatus != "positive_classified_non_cash" && latestEvaluationStatus != "positive_blip_ignored") {
		return 1
	}
	if invoiceableMinor < thresholdMinor {
		return 2
	}
	return 3
}

// AccountLedgerListEntry is one row of the list contract (CR-0009 "契约
// 变化" list item shape, verbatim field-for-field).
type AccountLedgerListEntry struct {
	ExternalAccountID        string
	SourceType               domain.SourceType
	ExternalUserID           string
	PolicyStartAt            time.Time
	RechargesSinceStartCount int
	RechargesSinceStartMinor int64
	ConsumedSinceStartMinor  int64
	InvoiceableNowMinor      int64
	IssuedMinor              int64
	BlockState               string
	// LastCheckpointAt is the zero time.Time when the account has never had
	// a reconciliation checkpoint or carry-forward proof evaluated at all
	// (a real, defined, non-error shape -- see the hard rule in
	// docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md).
	LastCheckpointAt time.Time
	// AccountEmail is the account's latest verified email address, filled in
	// by the application layer (the store never holds the keyring) and empty
	// when the account has none on file. Display only: the upstream numeric
	// ExternalUserID above stays the identifier every filter and cursor uses
	// (XM-INV-LEDGER-ACCOUNT-EMAIL).
	AccountEmail string
}

type AccountLedgerPageQuery struct {
	Limit            int
	ExternalUserID   string
	SourceInstanceID string
	// Sort selects the primary ordering: "" (default, and the only value
	// besides "block_state") sorts by invoiceable_now_minor descending,
	// keyset on (invoiceable_now_minor,id) per CR-0009's own contract.
	// "block_state" sorts frozen_manual_review first, then
	// not_invoiceable_pending_reconciliation, then below_threshold, then
	// invoiceable (most-needs-attention first for an operator triage view
	// -- CR-0009 does not specify a direction, this is a deliberate
	// implementation choice, documented in the handoff), tie-broken by id
	// descending.
	Sort string
	// BeforeInvoiceableMinor+BeforeID together are the keyset cursor for
	// the default sort (both required together, or neither -- mirroring
	// every sibling endpoint's own before_*+before_id pairing convention).
	BeforeInvoiceableMinor *int64
	// BeforeID alone is the keyset cursor for sort=block_state: its rank is
	// re-derived server-side (see ListAccountLedgerPage) from the cursor
	// row itself, since block_state is computed, not a stored column, and
	// CR-0009's page envelope has no field to carry a rank value across
	// requests. Also the id half of the default sort's cursor above.
	BeforeID string
	// ThresholdMinor is the real, admin-configurable minimum invoice amount
	// (ledger.MinimumRequestMinor()) -- passed in so block_state's
	// below_threshold branch and the httpapi DTO layer's threshold_reached
	// use the identical value, never a re-derived or hardcoded one.
	ThresholdMinor int64
}

type AccountLedgerPage struct {
	Items   []AccountLedgerListEntry
	HasMore bool
	// NextBeforeInvoiceableMinor/NextBeforeID are always populated together
	// when HasMore, regardless of sort mode -- for sort=block_state,
	// NextBeforeInvoiceableMinor is still just that row's own
	// invoiceable_now_minor (informational; not required to round-trip the
	// block_state cursor, which only needs NextBeforeID).
	NextBeforeInvoiceableMinor *int64
	NextBeforeID               string
}

// AccountLedgerRecharge is one item of the detail contract's
// recharges_since_start[] array.
type AccountLedgerRecharge struct {
	FundingLotID    string
	CompletedAt     time.Time
	AmountMinor     int64
	EligibilityKind string
	RefundFrozen    bool
}

// AccountLedgerConsumptionDay is one bucket of the detail contract's
// consumption timeline -- see GetAccountLedgerDetail's own comment for why
// "by day" was chosen over "by usage checkpoint" (task brief item 2).
type AccountLedgerConsumptionDay struct {
	// Date is an Asia/Shanghai calendar date, "2006-01-02" -- the policy
	// itself is anchored to Asia/Shanghai day boundaries
	// (invoice_eligibility_policy.eligibility_start_at = 2026-09-01 00:00
	// Asia/Shanghai), so bucketing by that same calendar day (rather than a
	// UTC day) is what "daily consumption" means to an operator reading
	// this view.
	Date          string
	ConsumedMinor int64
}

// AccountLedgerDetail is the detail contract: AccountLedgerListEntry's own
// fields plus everything CR-0009 "契约变化" adds for the single-account
// view. Every Freeze*/PendingReconciliation*/LatestEvaluation* field below
// is a raw ingredient, not pre-formatted text -- httpapi/accounts_ledger.go's
// buildAccountBlockReason is the sole place that turns them into the
// concrete Chinese block_reason sentence the wire contract requires.
type AccountLedgerDetail struct {
	AccountLedgerListEntry

	OpeningBalanceServiceUnits string
	OpeningBalanceUnitCode     string
	Recharges                  []AccountLedgerRecharge
	ConsumptionTimeline        []AccountLedgerConsumptionDay
	// LastReconciledAt is the zero time.Time when no evaluation has ever
	// been 'matched' for this account.
	LastReconciledAt time.Time

	HasOpenFreeze        bool
	FreezeReason         string
	FreezeTriggerType    string
	FreezeTriggerID      string
	FreezeOpenedAt       time.Time
	FreezeLotIssuedMinor *int64

	PendingReconciliationReason      string
	PendingReconciliationTriggerType string
	PendingReconciliationTriggerID   string
	PendingReconciliationSince       time.Time

	LatestEvaluationKind            string
	LatestEvaluationKey             string
	LatestEvaluationStatus          string
	LatestEvaluationExpectedUnits   string
	LatestEvaluationDifferenceUnits string
}

// accountLedgerCursorFilter builds the two optional exact-match filters
// (external_user_id, source_instance_id) shared by the list query and the
// block_state-sort cursor-rank lookup, via the same add()-based dynamic
// placeholder convention every other list query in this package already
// uses (see eligibility_operations.go/eligibility_freezes handling).
func accountLedgerCursorFilter(add func(any) string, externalUserID, sourceInstanceID string) string {
	filter := ""
	if externalUserID != "" {
		filter += ` AND ea.external_user_id=` + add(externalUserID)
	}
	if sourceInstanceID != "" {
		filter += ` AND eas.source_instance_id=` + add(sourceInstanceID) + `::uuid`
	}
	return filter
}

func scanAccountLedgerListEntry(row pgxRow) (AccountLedgerListEntry, error) {
	var item AccountLedgerListEntry
	var lastCheckpointAt *time.Time
	err := row.Scan(&item.ExternalAccountID, &item.SourceType, &item.ExternalUserID, &item.PolicyStartAt,
		&item.RechargesSinceStartCount, &item.RechargesSinceStartMinor, &item.ConsumedSinceStartMinor,
		&item.InvoiceableNowMinor, &item.IssuedMinor, &item.BlockState, &lastCheckpointAt)
	if err != nil {
		return item, err
	}
	if lastCheckpointAt != nil {
		item.LastCheckpointAt = *lastCheckpointAt
	}
	return item, nil
}

// ListAccountLedgerPage: CR-0009 "变更范围" item 1,
// GET /api/v1/admin/accounts/ledger. One row per external account, admin
// -only, strictly read-only (never writes to any table, never reads
// audit_events).
func (s *Store) ListAccountLedgerPage(ctx context.Context, in AccountLedgerPageQuery) (AccountLedgerPage, error) {
	if in.Limit <= 0 {
		in.Limit = 100
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Sort != "" && in.Sort != "block_state" {
		return AccountLedgerPage{}, errors.New("invalid account ledger sort")
	}
	if in.ExternalUserID != "" && (len(in.ExternalUserID) > 512 || strings.ContainsAny(in.ExternalUserID, "\r\n\x00")) {
		return AccountLedgerPage{}, errors.New("invalid external user id filter")
	}
	if in.SourceInstanceID != "" && !eligibilityUUIDPattern.MatchString(in.SourceInstanceID) {
		return AccountLedgerPage{}, errors.New("invalid source instance filter")
	}
	if in.BeforeID != "" && !eligibilityUUIDPattern.MatchString(in.BeforeID) {
		return AccountLedgerPage{}, errors.New("invalid account ledger cursor")
	}
	blockStateSort := in.Sort == "block_state"
	if !blockStateSort && (in.BeforeInvoiceableMinor != nil) != (in.BeforeID != "") {
		return AccountLedgerPage{}, errors.New("both valid account ledger cursor fields are required")
	}

	args := []any{}
	add := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }

	thresholdSQL := add(in.ThresholdMinor)
	filterSQL := accountLedgerCursorFilter(add, in.ExternalUserID, in.SourceInstanceID)

	query := `WITH signals AS (
		SELECT ` + accountLedgerSignalsSelectList + `
		` + accountLedgerFrom + `
		WHERE 1=1` + filterSQL + `
	)
	SELECT account_id,source_type,external_user_id,policy_start_at,
		recharges_count,recharges_minor,consumed_minor,invoiceable_minor,issued_minor,
		` + accountBlockStateExpr(thresholdSQL) + ` AS block_state,
		last_checkpoint_at
	FROM signals`

	if blockStateSort {
		if in.BeforeID != "" {
			// The cursor's own rank is computed in Go from its raw signals,
			// not via a second SQL query re-running accountBlockStateRankExpr:
			// a SELECT whose only projected column is a bare CASE expression
			// (no real table column) referencing a CTE can fail Postgres
			// parameter-type inference under pgx's default extended
			// protocol ("could not determine data type of parameter",
			// SQLSTATE 42P18) even when every parameter carries an explicit
			// cast and the identical query text PREPAREs cleanly through
			// plain SQL -- verified directly against Postgres while
			// diagnosing this. Selecting the ordinary, typed signal columns
			// instead (as this query already does for the real page) sidesteps
			// the issue entirely, and keeps accountBlockStateRank (below) as
			// the single Go-side mirror of accountBlockStateRankExpr's SQL
			// logic. See docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md.
			//
			// This lookup uses its own independent, freshly-numbered
			// placeholder set (cursorArgs/cursorAdd) rather than reusing
			// the outer query's args/add: reusing them would either bind
			// $1 (threshold) without the query text ever referencing it
			// (itself a distinct "could not determine data type of
			// parameter $1" failure -- Postgres requires every bound
			// parameter to be referenced somewhere in the statement) or
			// require mismatched renumbering.
			cursorArgs := []any{}
			cursorAdd := func(value any) string {
				cursorArgs = append(cursorArgs, value)
				return fmt.Sprintf("$%d", len(cursorArgs))
			}
			cursorFilterSQL := accountLedgerCursorFilter(cursorAdd, in.ExternalUserID, in.SourceInstanceID)
			cursorRankQuery := `WITH signals AS (
				SELECT ` + accountLedgerSignalsSelectList + `
				` + accountLedgerFrom + `
				WHERE 1=1` + cursorFilterSQL + `
			)
			SELECT has_open_freeze,eligibility_status,has_projection_job,latest_evaluation_status,invoiceable_minor
			FROM signals WHERE account_id=` + cursorAdd(in.BeforeID)
			var hasOpenFreeze, hasProjectionJob bool
			var eligibilityStatus, latestEvaluationStatus string
			var invoiceableMinor int64
			err := s.pool.QueryRow(ctx, cursorRankQuery, cursorArgs...).Scan(
				&hasOpenFreeze, &eligibilityStatus, &hasProjectionJob, &latestEvaluationStatus, &invoiceableMinor)
			if errors.Is(err, pgx.ErrNoRows) {
				// A stale/unknown cursor: a defined, empty next page, not an
				// error (the account it pointed at is no longer in this
				// result set -- nothing to resume from).
				return AccountLedgerPage{}, nil
			}
			if err != nil {
				return AccountLedgerPage{}, err
			}
			rank := accountBlockStateRank(hasOpenFreeze, eligibilityStatus, hasProjectionJob, latestEvaluationStatus, invoiceableMinor, in.ThresholdMinor)
			rankSQL := add(rank)
			idSQL := add(in.BeforeID)
			query += ` WHERE (` + accountBlockStateRankExpr(thresholdSQL) + `)>` + rankSQL +
				` OR ((` + accountBlockStateRankExpr(thresholdSQL) + `)=` + rankSQL + ` AND account_id<` + idSQL + `)`
		}
		query += ` ORDER BY (` + accountBlockStateRankExpr(thresholdSQL) + `) ASC, account_id DESC`
	} else {
		if in.BeforeInvoiceableMinor != nil {
			minorSQL := add(*in.BeforeInvoiceableMinor)
			idSQL := add(in.BeforeID)
			query += ` WHERE (invoiceable_minor,account_id)<(` + minorSQL + `,` + idSQL + `)`
		}
		query += ` ORDER BY invoiceable_minor DESC, account_id DESC`
	}
	query += ` LIMIT ` + add(in.Limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return AccountLedgerPage{}, err
	}
	defer rows.Close()
	items := make([]AccountLedgerListEntry, 0, in.Limit+1)
	for rows.Next() {
		item, scanErr := scanAccountLedgerListEntry(rows)
		if scanErr != nil {
			return AccountLedgerPage{}, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return AccountLedgerPage{}, err
	}
	page := AccountLedgerPage{HasMore: len(items) > in.Limit}
	if page.HasMore {
		items = items[:in.Limit]
	}
	page.Items = items
	if page.HasMore && len(items) > 0 {
		last := items[len(items)-1]
		minor := last.InvoiceableNowMinor
		page.NextBeforeInvoiceableMinor = &minor
		page.NextBeforeID = last.ExternalAccountID
	}
	return page, nil
}

// GetAccountLedgerDetail: CR-0009 "变更范围" item 2,
// GET /api/v1/admin/accounts/{external_account_id}/ledger. An unknown
// account (malformed id, or a real id with no matching bootstrapped
// source_account_eligibility_state row) returns domain.ErrNotFound, mapped
// by the httpapi layer to the existing 404 error-envelope convention.
func (s *Store) GetAccountLedgerDetail(ctx context.Context, externalAccountID string, thresholdMinor int64) (AccountLedgerDetail, error) {
	if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(externalAccountID)) {
		return AccountLedgerDetail{}, domain.ErrNotFound
	}
	args := []any{externalAccountID}
	add := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	thresholdSQL := add(thresholdMinor)

	query := `WITH signals AS (
		SELECT ` + accountLedgerSignalsSelectList + `
		` + accountLedgerFrom + `
		WHERE ea.id=$1
	)
	SELECT account_id,source_type,external_user_id,policy_start_at,
		recharges_count,recharges_minor,consumed_minor,invoiceable_minor,issued_minor,
		` + accountBlockStateExpr(thresholdSQL) + ` AS block_state,
		last_checkpoint_at, last_reconciled_at,
		has_open_freeze, COALESCE(freeze_reason,''), COALESCE(freeze_trigger_type,''), COALESCE(freeze_trigger_id,''),
		freeze_opened_at, freeze_lot_issued_minor,
		COALESCE(pending_reconciliation_reason,''), COALESCE(pending_reconciliation_trigger_type,''),
		COALESCE(pending_reconciliation_trigger_id,''), pending_reconciliation_since,
		COALESCE(latest_evaluation_kind,''), COALESCE(latest_evaluation_key,''), latest_evaluation_status,
		COALESCE(latest_evaluation_expected_units,''), COALESCE(latest_evaluation_difference_units,''),
		cutover_at, opening_balance_units, opening_balance_unit_code
	FROM signals`

	var item AccountLedgerDetail
	var lastCheckpointAt, lastReconciledAt, freezeOpenedAt, pendingSince *time.Time
	var freezeLotIssuedMinor *int64
	var cutoverAt time.Time
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&item.ExternalAccountID, &item.SourceType, &item.ExternalUserID, &item.PolicyStartAt,
		&item.RechargesSinceStartCount, &item.RechargesSinceStartMinor, &item.ConsumedSinceStartMinor,
		&item.InvoiceableNowMinor, &item.IssuedMinor, &item.BlockState,
		&lastCheckpointAt, &lastReconciledAt,
		&item.HasOpenFreeze, &item.FreezeReason, &item.FreezeTriggerType, &item.FreezeTriggerID,
		&freezeOpenedAt, &freezeLotIssuedMinor,
		&item.PendingReconciliationReason, &item.PendingReconciliationTriggerType,
		&item.PendingReconciliationTriggerID, &pendingSince,
		&item.LatestEvaluationKind, &item.LatestEvaluationKey, &item.LatestEvaluationStatus,
		&item.LatestEvaluationExpectedUnits, &item.LatestEvaluationDifferenceUnits,
		&cutoverAt, &item.OpeningBalanceServiceUnits, &item.OpeningBalanceUnitCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountLedgerDetail{}, domain.ErrNotFound
	}
	if err != nil {
		return AccountLedgerDetail{}, err
	}
	if lastCheckpointAt != nil {
		item.LastCheckpointAt = *lastCheckpointAt
	}
	if lastReconciledAt != nil {
		item.LastReconciledAt = *lastReconciledAt
	}
	if freezeOpenedAt != nil {
		item.FreezeOpenedAt = *freezeOpenedAt
	}
	item.FreezeLotIssuedMinor = freezeLotIssuedMinor
	if pendingSince != nil {
		item.PendingReconciliationSince = *pendingSince
	}

	recharges, err := s.accountLedgerRecharges(ctx, externalAccountID, cutoverAt)
	if err != nil {
		return AccountLedgerDetail{}, err
	}
	item.Recharges = recharges

	timeline, err := s.accountLedgerConsumptionTimeline(ctx, externalAccountID)
	if err != nil {
		return AccountLedgerDetail{}, err
	}
	item.ConsumptionTimeline = timeline

	return item, nil
}

// accountLedgerRecharges: the detail contract's recharges_since_start[]
// array, same universe as accountLedgerRechargesLateral's own sum
// (eligibility_kind IN (WALLET_CASH,SUBSCRIPTION_CASH),
// verification_state='verified', currency='CNY', completed_at>=cutoverAt),
// ordered by completed_at ascending per CR-0009's own wording ("按
// completed_at 升序聚合"). refund_frozen lots are included (flagged via
// their own field), matching the sum above -- a refund-frozen lot's cash
// still genuinely arrived, it is simply not currently invoiceable.
func (s *Store) accountLedgerRecharges(ctx context.Context, externalAccountID string, cutoverAt time.Time) ([]AccountLedgerRecharge, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text,completed_at,verified_cash_minor,eligibility_kind,refund_frozen
		FROM funding_lots
		WHERE external_account_id=$1 AND currency='CNY'
			AND eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH') AND verification_state='verified'
			AND completed_at>=$2
		ORDER BY completed_at ASC, id ASC`, externalAccountID, cutoverAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountLedgerRecharge, 0)
	for rows.Next() {
		var item AccountLedgerRecharge
		if err = rows.Scan(&item.FundingLotID, &item.CompletedAt, &item.AmountMinor, &item.EligibilityKind, &item.RefundFrozen); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// accountLedgerConsumptionTimeline builds the detail contract's "consumed
// since start, by day" timeline (task brief item 2) from
// consumption_allocations joined to the usage event it consumed and the
// funding lot it drew cash from -- no new aggregate table (the brief asks
// which existing shape supports this without one: consumption_allocations
// already records every cash-consuming allocation's own cash_minor_delta,
// keyed to the usage event's event_time). Bucketed by Asia/Shanghai
// calendar day (see AccountLedgerConsumptionDay's own comment for why),
// restricted to the same WALLET_CASH/SUBSCRIPTION_CASH/verified/CNY
// universe every other amount on this page uses, and to usage events at or
// after the account's own policy start (a cash allocation before that date
// is structurally impossible -- enforce_funding_lot_invoice_policy already
// guarantees it -- this WHERE is defensive documentation of that
// invariant, not a load-bearing filter).
func (s *Store) accountLedgerConsumptionTimeline(ctx context.Context, externalAccountID string) ([]AccountLedgerConsumptionDay, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(u.event_time AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD') AS day,
			sum(ca.cash_minor_delta)::bigint AS consumed_minor
		FROM consumption_allocations ca
		JOIN source_usage_events u ON u.id=ca.usage_event_id
		JOIN funding_lots fl ON fl.id=ca.funding_lot_id
		WHERE fl.external_account_id=$1 AND fl.currency='CNY'
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH') AND fl.verification_state='verified'
			AND u.event_time>=(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1)
		GROUP BY day
		ORDER BY day ASC`, externalAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountLedgerConsumptionDay, 0)
	for rows.Next() {
		var item AccountLedgerConsumptionDay
		if err = rows.Scan(&item.Date, &item.ConsumedMinor); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
