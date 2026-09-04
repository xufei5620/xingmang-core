package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

// This file implements CR-0009's operator "用户账本" view's HTTP surface:
// GET /api/v1/admin/accounts/ledger (listAccountLedger) and
// GET /api/v1/admin/accounts/{external_account_id}/ledger
// (getAccountLedgerDetail). Both are pure GETs behind the same
// s.require("admin", ...) middleware as /admin/eligibility-freezes (admin
// role + IP allowlist; no MFA step-up or CSRF token, matching slice 4's own
// established posture for this read-only endpoint family).

// accountLedgerShanghai is loaded once: block_reason is server-generated
// Chinese prose (not a raw timestamp field the frontend can reformat
// itself), so this is the one place in the backend that actually converts a
// timestamp to Asia/Shanghai wall-clock time for display, per the task
// brief's own explicit requirement ("time in Asia/Shanghai"). Every other
// timestamp on this page's wire contract stays plain UTC ISO-8601 -- the
// frontend renders those in Asia/Shanghai itself, matching every other
// admin endpoint's existing convention.
var accountLedgerShanghai = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		// Should never happen (the Go toolchain always ships the IANA
		// database), but a fixed UTC+8 offset is a safe, defined fallback
		// rather than panicking a request handler over a missing tzdata
		// file.
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

func formatShanghai(t time.Time) string {
	return t.In(accountLedgerShanghai).Format("2006-01-02 15:04")
}

// formatYuanMinor renders a minor-unit (fen) CNY amount as a two-decimal
// yuan string using only integer arithmetic -- amounts are never floats
// anywhere in this system, including in generated prose (see the task
// brief's hard rule).
func formatYuanMinor(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

// accountLedgerFreezeReasonDescriptions documents design section 3(C)'s
// "保留清单" (the freeze_reason values still worth manual review after
// XM-INV-ELIG-AUTO-RECONCILE/XM-INV-ELIG-QUEUE-NARROW) with a human-readable
// Chinese description for each, used by buildAccountBlockReason below.
// block_state itself does NOT filter by this whitelist: per the task
// brief, "after slices 1-2 every remaining open freeze is a manual-review
// one" (those two slices stopped any new UNKNOWN_NEGATIVE_BALANCE/
// USAGE_EXCEEDS_LEDGER/generalized-LATE_FINALIZED_EVENT freeze from ever
// being created), so postgresstore.AccountBlockStateFrozenManualReview is
// driven by "any open eligibility_freezes row exists", not by this map --
// this map is kept anyway, as the brief directs, both to document the
// retained set and to give every possible freeze_reason (including the two
// legacy reasons a production row created before XM-INV-ELIG-QUEUE-NARROW's
// migration tool ran can still carry) a concrete description.
var accountLedgerFreezeReasonDescriptions = map[string]string{
	// Design 3(C) "保留清单" -- code paths that still open new freezes today.
	"SOURCE_REFUND":        "退款或红字冲正核实中",
	"LATE_FINALIZED_EVENT": "迟到入账事实导致已开票金额超过重新核算后的可消费金额（红冲）",
	"SOURCE_GAP":           "上游数据流缺口自愈失败",
	// Design 3(C) "明确不变" -- six data-integrity/migration-period
	// reasons, unaffected by the queue-narrowing slices.
	"EVENT_PAYLOAD_DRIFT":         "上游事件内容与既有记录不一致",
	"UNIT_MISMATCH":               "计量单位与既有记录不一致",
	"AMBIGUOUS_EVENT_ORDER":       "事件发生顺序存在歧义，无法确定先后",
	"STREAM_WATERMARK_REGRESSION": "数据流水位发生回退",
	"EVENT_DEAD":                  "存在无法处理的死信事件",
	"POLICY_ANCHOR_BLOCKED":       "策略起点重新锚定被现有发票敞口阻塞",
	// Legacy: a production row opened before XM-INV-ELIG-AUTO-RECONCILE/
	// XM-INV-ELIG-QUEUE-NARROW shipped (or before that slice's migration
	// tool has actually been run) can still be open with one of these two.
	"UNKNOWN_NEGATIVE_BALANCE": "历史负余额冻结记录（等待队列收窄迁移工具处理）",
	"USAGE_EXCEEDS_LEDGER":     "历史用量超账本冻结记录（等待队列收窄迁移工具处理）",
}

// accountLedgerEvaluationStatusDescriptions describes the two non-"good"
// balance_checkpoint_evaluations/balance_carry_forward_evaluations statuses
// that can drive not_invoiceable_pending_reconciliation purely by
// themselves (see postgresstore.accountBlockStateExpr's own "latest
// evaluation not good" branch) -- a fresher matched/
// positive_classified_non_cash/positive_blip_ignored evaluation would not
// reach this branch at all.
var accountLedgerEvaluationStatusDescriptions = map[string]string{
	"negative_frozen":   "上报余额与账本预期存在负向差额",
	"source_gap_frozen": "对账所需的上游数据存在缺口",
}

// buildAccountBlockReason is the sole place that turns
// postgresstore.AccountLedgerDetail's raw ingredients into the concrete
// Chinese sentence the wire contract's block_reason field requires (task
// brief item 3): named facts (time in Asia/Shanghai, amounts in 元 with two
// decimals, the checkpoint or freeze trigger), never a generic phrase like
// "数据异常". Returns "" for block_state=invoiceable (nothing to explain --
// the DTO layer maps that to a JSON null, matching every other unblocked
// field's own null convention on this page).
//
// Amounts: only figures already known to be true CNY minor units (funding
// -lot-derived invoiceable_now_minor/threshold_minor, or a frozen
// LATE_FINALIZED_EVENT lot's own issued_minor) are ever rendered in 元.
// A balance-checkpoint/carry-forward-proof discrepancy
// (LatestEvaluation{Expected,Difference}Units) is reported in its own raw
// service units, explicitly labeled as such -- source_account_eligibility_
// state.unit_code is an upstream-source-specific accounting unit (e.g.
// "SUB2_BALANCE_1E8" in this codebase's own fixtures) with no established,
// verified conversion factor to CNY anywhere in this system (per-lot
// exchange rates are computed individually inside
// funding_lot_consumption_state, not as one global constant), so inventing
// one here would be a fabricated precision this endpoint cannot stand
// behind -- consistent with this same design's own explicit principle
// elsewhere ("不假装非现金单位是人民币" for opening_balance_units/legacy/
// noncash in ListUserEligibilitySummaries). Flagged in
// docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md as a deliberate, narrower
// reading of the brief's "amounts in 元" instruction for this one sentence
// shape.
func buildAccountBlockReason(detail postgresstore.AccountLedgerDetail, thresholdMinor int64) string {
	switch detail.BlockState {
	case postgresstore.AccountBlockStateFrozenManualReview:
		return buildFrozenManualReviewReason(detail)
	case postgresstore.AccountBlockStateNotInvoiceablePendingReconciliation:
		return buildPendingReconciliationReason(detail)
	case postgresstore.AccountBlockStateSettling:
		// XM-INV-LEDGER-SETTLING-STATE: say what is actually happening. There
		// is no dispute to report here and no operator action to take -- the
		// figures simply are not final yet, and the next projection round
		// makes them so.
		return "本轮结算尚未完成：仍有投影任务在队列中，当前可开票金额可能未包含最新用量，结算完成后自动更新"
	case postgresstore.AccountBlockStateBelowThreshold:
		return fmt.Sprintf("当前可开票金额 %s 元未达到起票门槛 %s 元，还差 %s 元",
			formatYuanMinor(detail.InvoiceableNowMinor), formatYuanMinor(thresholdMinor),
			formatYuanMinor(thresholdMinor-detail.InvoiceableNowMinor))
	default:
		return ""
	}
}

func buildFrozenManualReviewReason(detail postgresstore.AccountLedgerDetail) string {
	if !detail.HasOpenFreeze {
		// The task brief's own named defensive edge case: eligibility_status
		// says 'frozen' but no open eligibility_freezes row backs it up
		// (should not happen by construction, but not assumed) -- still a
		// concrete, actionable fact, not "数据异常".
		return "账号状态为冻结，但未查到对应的开放冻结记录，需人工核查数据一致性后手动处理"
	}
	description := accountLedgerFreezeReasonDescriptions[detail.FreezeReason]
	if description == "" {
		description = "未在已知冻结原因清单中的原因"
	}
	sentence := fmt.Sprintf("%s（Asia/Shanghai）因%s被冻结（原因代码 %s，关联对象 %s:%s）",
		formatShanghai(detail.FreezeOpenedAt), description, detail.FreezeReason,
		detail.FreezeTriggerType, detail.FreezeTriggerID)
	if detail.FreezeReason == "LATE_FINALIZED_EVENT" && detail.FreezeLotIssuedMinor != nil {
		sentence += fmt.Sprintf("，该资金批次已开具发票 %s 元", formatYuanMinor(*detail.FreezeLotIssuedMinor))
	}
	return sentence + "，需人工核实后在“资格冻结”页签手动解除"
}

func buildPendingReconciliationReason(detail postgresstore.AccountLedgerDetail) string {
	if detail.PendingReconciliationReason != "" {
		description := accountLedgerFreezeReasonDescriptions[detail.PendingReconciliationReason]
		if description == "" {
			description = "自动降级为暂不可开票"
		}
		return fmt.Sprintf("%s（Asia/Shanghai）起%s（原因代码 %s，关联对象 %s:%s），等待下一次对账自动解除",
			formatShanghai(detail.PendingReconciliationSince), description, detail.PendingReconciliationReason,
			detail.PendingReconciliationTriggerType, detail.PendingReconciliationTriggerID)
	}
	if detail.LatestEvaluationStatus != "" {
		description := accountLedgerEvaluationStatusDescriptions[detail.LatestEvaluationStatus]
		if description == "" {
			description = "对账评估结果不是已确认对平"
		}
		if detail.LatestEvaluationExpectedUnits != "" || detail.LatestEvaluationDifferenceUnits != "" {
			return fmt.Sprintf("%s（Asia/Shanghai）%s %s 上报余额差额 %s（单位 %s，非人民币元），预期 %s，%s，等待下一次核对",
				formatShanghai(detail.LastCheckpointAt), detail.LatestEvaluationKind, detail.LatestEvaluationKey,
				detail.LatestEvaluationDifferenceUnits, detail.OpeningBalanceUnitCode, detail.LatestEvaluationExpectedUnits, description)
		}
		return fmt.Sprintf("%s（Asia/Shanghai）%s %s %s，等待下一次核对",
			formatShanghai(detail.LastCheckpointAt), detail.LatestEvaluationKind, detail.LatestEvaluationKey, description)
	}
	if detail.HasOpenFreeze {
		// Should not be reachable (an open freeze always resolves to
		// frozen_manual_review first), kept only as a last, still-concrete
		// fallback rather than a generic phrase.
		return "存在开放的冻结记录但未能读取其触发信息，需人工核查"
	}
	// No evaluation has ever been recorded for this account at all (task
	// brief's own named hard-rule shape: zero lots, zero evaluations, zero
	// projection jobs must still produce a defined, sensible row).
	return "该账号自纳入本系统以来尚未产生过任何对账检查点或结转证明记录，等待首次对账"
}

// accountLedgerListDTO is the sole source of truth for the list contract's
// wire shape -- CR-0009 "契约变化" list item field set, verbatim.
func accountLedgerListDTO(item postgresstore.AccountLedgerListEntry, thresholdMinor int64) map[string]any {
	dto := map[string]any{
		"external_account_id":         item.ExternalAccountID,
		"source_type":                 item.SourceType,
		"external_user_id":            item.ExternalUserID,
		"policy_start_at":             item.PolicyStartAt,
		"recharges_since_start_count": item.RechargesSinceStartCount,
		"recharges_since_start_minor": item.RechargesSinceStartMinor,
		"consumed_since_start_minor":  item.ConsumedSinceStartMinor,
		"invoiceable_now_minor":       item.InvoiceableNowMinor,
		"issued_minor":                item.IssuedMinor,
		"threshold_reached":           item.InvoiceableNowMinor >= thresholdMinor,
		"block_state":                 item.BlockState,
		"last_checkpoint_at":          nil,
	}
	if !item.LastCheckpointAt.IsZero() {
		dto["last_checkpoint_at"] = item.LastCheckpointAt
	}
	// account_email is present only when the account actually has a verified
	// address on file: an absent key is the honest shape for "none", and it
	// keeps the operator from reading an empty string as an empty mailbox.
	// It is additive to CR-0009's field set and identifies nothing on its own
	// -- external_user_id remains the identifier every filter and cursor uses
	// (XM-INV-LEDGER-ACCOUNT-EMAIL).
	if item.AccountEmail != "" {
		dto["account_email"] = item.AccountEmail
	}
	return dto
}

// accountLedgerDetailDTO is the sole source of truth for the detail
// contract's wire shape: every list field above plus CR-0009's own
// detail-only additions.
func accountLedgerDetailDTO(detail postgresstore.AccountLedgerDetail, thresholdMinor int64) map[string]any {
	dto := accountLedgerListDTO(detail.AccountLedgerListEntry, thresholdMinor)
	dto["opening_balance_units"] = map[string]any{
		"service_units": detail.OpeningBalanceServiceUnits,
		"unit_code":     detail.OpeningBalanceUnitCode,
	}
	recharges := make([]map[string]any, 0, len(detail.Recharges))
	for _, r := range detail.Recharges {
		recharges = append(recharges, map[string]any{
			"funding_lot_id":   r.FundingLotID,
			"completed_at":     r.CompletedAt,
			"amount_minor":     r.AmountMinor,
			"eligibility_kind": r.EligibilityKind,
			"refund_frozen":    r.RefundFrozen,
		})
	}
	dto["recharges_since_start"] = recharges
	timeline := make([]map[string]any, 0, len(detail.ConsumptionTimeline))
	for _, day := range detail.ConsumptionTimeline {
		timeline = append(timeline, map[string]any{"date": day.Date, "consumed_minor": day.ConsumedMinor})
	}
	dto["consumption_timeline"] = timeline
	dto["last_reconciled_at"] = nil
	if !detail.LastReconciledAt.IsZero() {
		dto["last_reconciled_at"] = detail.LastReconciledAt
	}
	dto["block_reason"] = nil
	if reason := buildAccountBlockReason(detail, thresholdMinor); reason != "" {
		dto["block_reason"] = reason
	}
	return dto
}

func (s *Server) listAccountLedger(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "account ledger is unavailable")
		return
	}
	query := postgresstore.AccountLedgerPageQuery{
		Limit:            boundedQueryLimit(r, 100),
		ExternalUserID:   strings.TrimSpace(r.URL.Query().Get("external_user_id")),
		SourceInstanceID: strings.TrimSpace(r.URL.Query().Get("source_instance_id")),
		Sort:             strings.TrimSpace(r.URL.Query().Get("sort")),
		ThresholdMinor:   s.ledger.MinimumRequestMinor(),
	}
	if value := strings.TrimSpace(r.URL.Query().Get("before_invoiceable_minor")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "invalid account ledger cursor")
			return
		}
		query.BeforeInvoiceableMinor = &parsed
	}
	query.BeforeID = strings.TrimSpace(r.URL.Query().Get("before_id"))
	page, err := s.operations.ListAccountLedgerPage(r.Context(), query)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, accountLedgerListDTO(item, query.ThresholdMinor))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "has_more": page.HasMore,
		"next_before_invoiceable_minor": page.NextBeforeInvoiceableMinor,
		"next_before_id":                page.NextBeforeID,
	})
}

func (s *Server) getAccountLedgerDetail(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "account ledger is unavailable")
		return
	}
	thresholdMinor := s.ledger.MinimumRequestMinor()
	detail, err := s.operations.GetAccountLedgerDetail(r.Context(), r.PathValue("external_account_id"), thresholdMinor)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountLedgerDetailDTO(detail, thresholdMinor))
}
