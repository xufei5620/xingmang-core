package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

func (s *Server) getSourceHealth(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "source synchronization status is unavailable")
		return
	}
	report, err := s.operations.SourceHealth(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "SOURCE_HEALTH_UNAVAILABLE", "source synchronization status is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) listSourceAccounts(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "source account status is unavailable")
		return
	}
	accounts, err := s.operations.ListExternalAccounts(r.Context(), principal(r).UserID, sessionPlatform(r))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DATA_UNAVAILABLE", "source account status is unavailable")
		return
	}
	items := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, map[string]any{
			"id": account.ID, "source_instance_id": account.SourceInstanceID,
			"source_type": account.SourceType, "source_name": account.SourceName,
			"external_user_id_masked": account.MaskedExternalUserID,
			"binding_status":          account.BindingStatus, "verified_at": account.VerifiedAt,
			"last_observed_at": account.LastObservedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listUserEligibilitySummary(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "invoice eligibility is unavailable")
		return
	}
	items, err := s.operations.ListUserEligibilitySummaries(r.Context(), principal(r).UserID, sessionPlatform(r))
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func eligibilityFreezeDTO(item postgresstore.EligibilityFreeze) map[string]any {
	scope := "account"
	if item.FundingLotID != "" {
		scope = "funding_lot"
	}
	dto := map[string]any{"id": item.ID, "principal_id": item.PrincipalID,
		"source_instance_id": item.SourceInstanceID, "source_type": item.SourceType, "source_name": item.SourceName,
		"funding_lot_id": item.FundingLotID, "scope": scope, "freeze_reason": item.FreezeReason,
		"status": item.Status, "eligibility_status": item.EligibilityStatus, "opened_at": item.OpenedAt,
		"version": item.ResolutionVersion}
	if !item.ResolvedAt.IsZero() {
		dto["resolved_at"] = item.ResolvedAt
	}
	return dto
}

func (s *Server) listEligibilityFreezes(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "eligibility freeze queue is unavailable")
		return
	}
	query := postgresstore.EligibilityFreezePageQuery{Limit: boundedQueryLimit(r, 100), Status: strings.TrimSpace(r.URL.Query().Get("status")), FreezeReason: strings.TrimSpace(r.URL.Query().Get("reason")), SourceInstanceID: strings.TrimSpace(r.URL.Query().Get("source_instance_id"))}
	if value := strings.TrimSpace(r.URL.Query().Get("before_opened_at")); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "invalid eligibility freeze cursor")
			return
		}
		query.BeforeOpenedAt = parsed
		query.BeforeID = strings.TrimSpace(r.URL.Query().Get("before_id"))
	}
	page, err := s.operations.ListEligibilityFreezesPage(r.Context(), query)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, eligibilityFreezeDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": page.HasMore, "next_before_opened_at": page.NextBeforeOpenedAt, "next_before_id": page.NextBeforeID})
}

func (s *Server) resolveEligibilityFreeze(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "eligibility freeze resolution is unavailable")
		return
	}
	var input struct {
		Version  int64  `json:"version"`
		Evidence string `json:"evidence_reference"`
		Note     string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Version <= 0 || strings.TrimSpace(input.Evidence) == "" || strings.TrimSpace(input.Note) == "" {
		writeError(w, http.StatusUnprocessableEntity, "ELIGIBILITY_RESOLUTION_EVIDENCE_REQUIRED", "version, evidence reference and note are required")
		return
	}
	item, err := s.operations.ResolveEligibilityFreeze(r.Context(), principal(r).UserID, r.PathValue("id"), input.Version, input.Evidence, input.Note)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eligibilityFreezeDTO(item))
}

func (s *Server) listPaymentCandidates(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "payment candidate queue is unavailable")
		return
	}
	query := postgresstore.PaymentCandidatePageQuery{Limit: boundedQueryLimit(r, 100)}
	if r.URL.Query().Get("include_verified") == "true" {
		query.States = []domain.VerificationState{
			domain.VerificationPending, domain.VerificationFrozen, domain.VerificationVerified,
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("before_observed_at")); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "invalid payment candidate cursor")
			return
		}
		query.BeforeObservedAt = parsed
		query.BeforeID = strings.TrimSpace(r.URL.Query().Get("before_id"))
	}
	switch strings.TrimSpace(r.URL.Query().Get("state")) {
	case "", "all":
		query.States = []domain.VerificationState{domain.VerificationPending, domain.VerificationFrozen}
	case "pending":
		query.States = []domain.VerificationState{domain.VerificationPending}
	case "frozen":
		query.States = []domain.VerificationState{domain.VerificationFrozen}
	case "verified":
		query.States = []domain.VerificationState{domain.VerificationVerified}
	default:
		writeError(w, http.StatusBadRequest, "INVALID_FILTER", "invalid payment candidate state")
		return
	}
	page, err := s.operations.ListPaymentCandidatesPage(r.Context(), query)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, lot := range page.Items {
		displayReference := strings.TrimSpace(lot.TradeNo)
		if displayReference == "" {
			displayReference = lot.ExternalOrderID
		}
		items = append(items, map[string]any{
			"id": lot.ID, "principal_id": lot.PrincipalID,
			"source_instance_id": lot.SourceInstanceID, "source_type": lot.SourceType,
			"source_order_id": lot.ExternalOrderID, "display_reference": displayReference,
			"quoted_minor": lot.OriginalMinor, "current_cap_minor": lot.CurrentCapMinor,
			"currency": lot.Currency, "verification": lot.Verification,
			"manual_review_stage": lot.ManualReviewStage,
			"source_status":       lot.SourceStatus, "observed_at": lot.ObservedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "has_more": page.HasMore,
		"next_before_observed_at": page.NextBeforeObservedAt,
		"next_before_id":          page.NextBeforeID,
	})
}

func (s *Server) applyNewAPIManualCap(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "manual payment adjustment is unavailable")
		return
	}
	var input struct {
		NewCapMinor int64  `json:"new_cap_minor"`
		Evidence    string `json:"evidence_reference"`
		Reason      string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.NewCapMinor < 0 || strings.TrimSpace(input.Evidence) == "" || strings.TrimSpace(input.Reason) == "" {
		writeError(w, http.StatusUnprocessableEntity, "EVIDENCE_REQUIRED", "non-negative remaining cap, evidence and reason are required")
		return
	}
	lot, err := s.operations.ApplyNewAPIManualCap(r.Context(), principal(r).UserID,
		r.PathValue("id"), input.NewCapMinor, input.Evidence, input.Reason)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lot)
}

func (s *Server) rejectNewAPIPayment(w http.ResponseWriter, r *http.Request) {
	s.reviewNewAPIPayment(w, r, "reject")
}

func (s *Server) freezeNewAPIPayment(w http.ResponseWriter, r *http.Request) {
	s.reviewNewAPIPayment(w, r, "freeze")
}

func (s *Server) reviewNewAPIPayment(w http.ResponseWriter, r *http.Request, action string) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "payment review is unavailable")
		return
	}
	var input struct {
		Evidence string `json:"evidence_reference"`
		Reason   string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Evidence) == "" || strings.TrimSpace(input.Reason) == "" {
		writeError(w, http.StatusUnprocessableEntity, "PAYMENT_REVIEW_EVIDENCE_REQUIRED", "evidence reference and review reason are required")
		return
	}
	var (
		lot domain.FundingLot
		err error
	)
	if action == "reject" {
		lot, err = s.operations.RejectNewAPIPaymentWithEvidence(r.Context(), principal(r).UserID, r.PathValue("id"), input.Evidence, input.Reason)
	} else {
		lot, err = s.operations.FreezeNewAPIPaymentWithEvidence(r.Context(), principal(r).UserID, r.PathValue("id"), input.Evidence, input.Reason)
	}
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lot)
}

func (s *Server) listRefundCases(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "refund queue is unavailable")
		return
	}
	query := postgresstore.RefundCasePageQuery{Limit: boundedQueryLimit(r, 100), Status: strings.TrimSpace(r.URL.Query().Get("status"))}
	if value := strings.TrimSpace(r.URL.Query().Get("before_opened_at")); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "invalid refund cursor")
			return
		}
		query.BeforeOpenedAt = parsed
		query.BeforeID = strings.TrimSpace(r.URL.Query().Get("before_id"))
	}
	page, err := s.operations.ListRefundCasesPage(r.Context(), query)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, refundCaseDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "has_more": page.HasMore,
		"next_before_opened_at": page.NextBeforeOpenedAt, "next_before_id": page.NextBeforeID,
	})
}

func (s *Server) resolveRefundCase(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "refund resolution is unavailable")
		return
	}
	var input struct {
		ResolutionStatus string `json:"resolution_status"`
		Evidence         string `json:"evidence_reference"`
		Note             string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.operations.ResolveRefundCase(r.Context(), principal(r).UserID, r.PathValue("id"), input.ResolutionStatus, input.Evidence, input.Note)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, refundCaseDTO(item))
}

func (s *Server) requeueInvoiceEmail(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "email requeue is unavailable")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	outbox, err := s.operations.RequeueEmail(r.Context(), r.PathValue("id"), principal(r).UserID, input.Reason)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": outbox.Status, "attempts": outbox.Attempts, "next_attempt_at": outbox.NextAttemptAt})
}

func (s *Server) getUserDeliveryState(w http.ResponseWriter, r *http.Request) {
	s.getDeliveryState(w, r, false)
}

func (s *Server) getAdminDeliveryState(w http.ResponseWriter, r *http.Request) {
	s.getDeliveryState(w, r, true)
}

func (s *Server) getDeliveryState(w http.ResponseWriter, r *http.Request, admin bool) {
	if s.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE", "invoice delivery status is unavailable")
		return
	}
	principalID := principal(r).UserID
	platform := sessionPlatform(r)
	if admin {
		principalID = ""
		platform = ""
	}
	state, err := s.operations.GetInvoiceDeliveryState(r.Context(), principalID, r.PathValue("id"), admin, platform)
	if err != nil {
		handleDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"document_available":   state.Document.ID != "",
		"invoice_number":       state.Document.InvoiceNumber,
		"issued_at":            state.Document.IssuedAt,
		"mail_status":          state.Outbox.Status,
		"mail_attempts":        state.Outbox.Attempts,
		"next_mail_attempt_at": state.Outbox.NextAttemptAt,
	})
}

func boundedQueryLimit(r *http.Request, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if err != nil || value <= 0 {
		return maximum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func refundCaseDTO(item postgresstore.RefundCase) map[string]any {
	return map[string]any{
		"id": item.ID, "request_id": item.RequestID, "funding_lot_id": item.FundingLotID,
		"source_revision":       item.SourceRevision,
		"observed_refund_minor": item.ObservedRefundMinor,
		"issued_exposure_minor": item.IssuedExposureMinor,
		"observed_cap_minor":    item.ObservedCapMinor, "status": item.Status,
		"resolved_by": item.ResolvedBy, "opened_at": item.OpenedAt,
		"updated_at": item.UpdatedAt, "resolved_at": item.ResolvedAt,
	}
}
