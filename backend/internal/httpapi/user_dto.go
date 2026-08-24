package httpapi

import (
	"strings"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
)

// userInvoiceRequest is deliberately separate from the domain/admin model.
// In particular, issuer routing and revision identifiers remain server-side.
type userInvoiceRequest struct {
	ID               string                 `json:"id"`
	RequestNo        string                 `json:"request_no"`
	SourceInstanceID string                 `json:"source_instance_id"`
	SourceType       domain.SourceType      `json:"source_type"`
	Currency         string                 `json:"currency"`
	ServiceItem      string                 `json:"service_item"`
	AmountMinor      int64                  `json:"amount_minor"`
	Status           domain.RequestStatus   `json:"status"`
	Profile          domain.ProfileSnapshot `json:"profile"`
	Allocations      []userAllocation       `json:"allocations"`
	ReviewNote       string                 `json:"review_note,omitempty"`
	Version          int64                  `json:"version"`
	SubmittedAt      time.Time              `json:"submitted_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

type userAllocation struct {
	FundingLotID string `json:"funding_lot_id"`
	AmountMinor  int64  `json:"amount_minor"`
}

type userFundingLot struct {
	ID                string                   `json:"id"`
	Source            domain.SourceType        `json:"source"`
	SourceInstanceID  string                   `json:"source_instance_id"`
	SourceLabel       string                   `json:"source_label"`
	DisplayReference  string                   `json:"display_reference"`
	CompletedAt       time.Time                `json:"completed_at"`
	OriginalPaidMinor int64                    `json:"original_paid_minor"`
	ConsumedCashMinor int64                    `json:"consumed_cash_minor"`
	ReservedMinor     int64                    `json:"reserved_minor"`
	IssuedMinor       int64                    `json:"issued_minor"`
	AvailableMinor    int64                    `json:"available_minor"`
	EligibilityKind   string                   `json:"eligibility_kind"`
	Verification      domain.VerificationState `json:"verification"`
	RefundFrozen      bool                     `json:"refund_frozen"`
	EligibilityStatus string                   `json:"eligibility_status"`
	ReasonCode        string                   `json:"reason_code,omitempty"`
}

func userRequestDTOs(requests []domain.InvoiceRequest) []userInvoiceRequest {
	result := make([]userInvoiceRequest, 0, len(requests))
	for _, request := range requests {
		result = append(result, userRequestDTO(request))
	}
	return result
}

func userRequestDTO(request domain.InvoiceRequest) userInvoiceRequest {
	return userInvoiceRequest{
		ID:               request.ID,
		RequestNo:        request.RequestNo,
		SourceInstanceID: request.SourceInstanceID,
		SourceType:       request.SourceType,
		Currency:         request.Currency,
		ServiceItem:      request.ServiceItem,
		AmountMinor:      request.AmountMinor,
		Status:           request.Status,
		Profile:          request.Profile,
		Allocations:      userAllocationDTOs(request.Allocations),
		ReviewNote:       request.ReviewNote,
		Version:          request.Version,
		SubmittedAt:      request.SubmittedAt,
		UpdatedAt:        request.UpdatedAt,
	}
}

func userAllocationDTOs(allocations []domain.Allocation) []userAllocation {
	result := make([]userAllocation, 0, len(allocations))
	for _, allocation := range allocations {
		result = append(result, userAllocation{FundingLotID: allocation.FundingLotID, AmountMinor: allocation.AmountMinor})
	}
	return result
}

func userFundingLotDTOs(lots []domain.FundingLot) []userFundingLot {
	result := make([]userFundingLot, 0, len(lots))
	for _, lot := range lots {
		kind := "legacy"
		switch lot.EligibilityKind {
		case domain.EligibilityWalletCash:
			kind = "wallet"
		case domain.EligibilitySubscriptionCash:
			kind = "subscription"
		case domain.EligibilityNonCash:
			if !lot.CompletedAt.IsZero() && lot.CompletedAt.Before(adminsettings.RequiredEligibilityStartAt) {
				kind = "legacy"
			} else {
				kind = "noncash"
			}
		}
		label := "SoloV API"
		if lot.SourceType == domain.SourceNewAPI {
			label = "SoloV 模型平台"
		}
		reference := strings.ReplaceAll(lot.ID, "-", "")
		if len(reference) > 8 {
			reference = reference[len(reference)-8:]
		}
		reasonCode := ""
		if lot.RefundFrozen {
			reasonCode = "SOURCE_REFUND"
		} else if kind == "legacy" && !lot.CompletedAt.IsZero() && lot.CompletedAt.Before(adminsettings.RequiredEligibilityStartAt) {
			reasonCode = "BEFORE_ELIGIBILITY_START"
		} else if lot.EligibilityKind == domain.EligibilitySubscriptionCash {
			reasonCode = "SUBSCRIPTION_USAGE_UNSUPPORTED"
		} else if (lot.EligibilityKind == domain.EligibilityWalletCash ||
			lot.EligibilityKind == domain.EligibilitySubscriptionCash) &&
			lot.Verification == domain.VerificationVerified && lot.ConsumedCashMinor == 0 {
			reasonCode = "NO_POST_START_CONSUMPTION"
		} else if lot.EligibilityStatus == "syncing" {
			reasonCode = "LEDGER_SYNCING"
		} else if lot.EligibilityStatus == "source_unavailable" {
			reasonCode = "SOURCE_NOT_READY"
		} else if lot.EligibilityStatus != "active" {
			reasonCode = "LEDGER_FROZEN"
		}
		result = append(result, userFundingLot{
			ID: lot.ID, Source: lot.SourceType, SourceInstanceID: lot.SourceInstanceID,
			SourceLabel: label, DisplayReference: "PAY-" + strings.ToUpper(reference),
			CompletedAt: lot.CompletedAt, OriginalPaidMinor: lot.VerifiedCashMinor,
			ConsumedCashMinor: lot.ConsumedCashMinor, ReservedMinor: lot.ReservedMinor,
			IssuedMinor: lot.IssuedMinor, AvailableMinor: lot.AvailableMinor(),
			EligibilityKind: kind, Verification: lot.Verification, RefundFrozen: lot.RefundFrozen,
			EligibilityStatus: lot.EligibilityStatus, ReasonCode: reasonCode,
		})
	}
	return result
}
