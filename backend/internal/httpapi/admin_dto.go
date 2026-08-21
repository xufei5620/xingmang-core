package httpapi

import (
	"strings"

	"invoice-system/backend/internal/domain"
)

type adminInvoiceRequestDTO struct {
	domain.InvoiceRequest
	PrincipalDisplayName string `json:"principal_display_name"`
	PrincipalEmail       string `json:"principal_email"`
	PaymentVerification  string `json:"payment_verification"`
}

func adminRequestDTOs(requests []domain.InvoiceRequest) []adminInvoiceRequestDTO {
	items := make([]adminInvoiceRequestDTO, 0, len(requests))
	for _, request := range requests {
		verification := "not_required"
		if request.SourceType == domain.SourceNewAPI {
			// A production request cannot reserve a New API candidate until the
			// independent evidence review has changed its lot to verified. A later
			// refund/freeze moves the request to refund_attention.
			verification = "passed"
			if request.Status == domain.StatusRefundAttention {
				verification = "failed"
			}
		}
		email := strings.TrimSpace(request.Profile.Email)
		items = append(items, adminInvoiceRequestDTO{
			InvoiceRequest: request, PrincipalDisplayName: maskedEmailName(email),
			PrincipalEmail: email, PaymentVerification: verification,
		})
	}
	return items
}
