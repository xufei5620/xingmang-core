package httpapi

import (
	"net/http"
	"strings"

	"invoice-system/backend/internal/postgresstore"
)

// eligibilityLedgerDTO is the sole source of truth for this endpoint's wire
// shape (same convention as eligibilityFreezeDTO). Field set follows design
// doc 2026-09-03-xm-inv-eligibility-simplification-design.md section 3(E)
// exactly -- no per-item id field (only the page envelope's next_before_id
// carries the keyset cursor forward). threshold_minor/threshold_reached are
// computed here from the same admin-configurable minimum invoice amount that
// gates real submission (s.ledger.MinimumRequestMinor()) rather than a new
// constant, per the task brief.
func eligibilityLedgerDTO(item postgresstore.EligibilityLedgerEntry, thresholdMinor int64) map[string]any {
	dto := map[string]any{
		"source_instance_id":                 item.SourceInstanceID,
		"source_type":                        item.SourceType,
		"source_name":                        item.SourceName,
		"external_user_id":                   item.ExternalUserID,
		"recharged_since_policy_start_minor": item.RechargedSincePolicyStartMinor,
		"consumed_minor":                     item.ConsumedMinor,
		"invoiceable_minor":                  item.InvoiceableMinor,
		"threshold_minor":                    thresholdMinor,
		"threshold_reached":                  item.InvoiceableMinor >= thresholdMinor,
		"eligibility_status":                 item.EligibilityStatus,
		"block_reason":                       nil,
		"block_detail":                       nil,
		"block_since":                        nil,
	}
	if item.BlockReason != "" {
		dto["block_reason"] = item.BlockReason
		dto["block_detail"] = item.BlockDetail
		dto["block_since"] = item.BlockSince
	}
	return dto
}

func (s *Server) listEligibilityLedger(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "eligibility ledger is unavailable")
		return
	}
	query := postgresstore.EligibilityLedgerPageQuery{
		Limit:          boundedQueryLimit(r, 100),
		ExternalUserID: strings.TrimSpace(r.URL.Query().Get("external_user_id")),
		BeforeID:       strings.TrimSpace(r.URL.Query().Get("before_id")),
	}
	page, err := s.operations.ListEligibilityLedgerPage(r.Context(), query)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	thresholdMinor := s.ledger.MinimumRequestMinor()
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, eligibilityLedgerDTO(item, thresholdMinor))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": page.HasMore, "next_before_id": page.NextBeforeID})
}
