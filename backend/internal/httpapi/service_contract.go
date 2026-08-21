package httpapi

import (
	"context"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/postgresstore"
)

type requestPageService interface {
	ListRequestsPage(context.Context, application.RequestPageQuery) (application.RequestPage, error)
}

// InvoiceService is the HTTP application's persistence boundary. Both the
// local mock ledger and the PostgreSQL production service implement it; list
// failures are explicit and can never be misreported as an empty account.
type InvoiceService interface {
	MinimumRequestMinor() int64
	SetMinimumRequestMinor(int64) error
	SaveProfile(context.Context, domain.InvoiceProfile) (domain.InvoiceProfile, error)
	ListProfiles(context.Context, string) ([]domain.InvoiceProfile, error)
	ListFundingLots(context.Context, string) ([]domain.FundingLot, error)
	Submit(context.Context, ledger.SubmitInput) (domain.InvoiceRequest, error)
	Cancel(context.Context, string, string, int64) (domain.InvoiceRequest, error)
	Review(context.Context, string, string, string, string, int64) (domain.InvoiceRequest, error)
	BeginManualIssue(context.Context, string, string, int64) (domain.InvoiceRequest, error)
	ConfirmManualIssue(context.Context, string, string, int64) (domain.InvoiceRequest, error)
	AttachDocument(context.Context, string, domain.InvoiceDocument, int64) (domain.InvoiceRequest, domain.InvoiceDocument, domain.EmailOutbox, error)
	GetDocumentForRequest(context.Context, string, string) (domain.InvoiceDocument, error)
	GetRequest(context.Context, string, string, bool) (domain.InvoiceRequest, error)
	ListRequests(context.Context, string, bool) ([]domain.InvoiceRequest, error)
	VerifyNewAPIPaymentWithEvidence(context.Context, string, string, int64, string, string) (domain.FundingLot, error)
}

type OperationsService interface {
	SourceHealth(context.Context) (postgresstore.SourceHealthReport, error)
	ListExternalAccounts(context.Context, string) ([]postgresstore.ConnectedSourceAccount, error)
	ListPaymentCandidatesPage(context.Context, postgresstore.PaymentCandidatePageQuery) (postgresstore.PaymentCandidatePage, error)
	RejectNewAPIPaymentWithEvidence(context.Context, string, string, string, string) (domain.FundingLot, error)
	FreezeNewAPIPaymentWithEvidence(context.Context, string, string, string, string) (domain.FundingLot, error)
	ApplyNewAPIManualCap(context.Context, string, string, int64, string, string) (domain.FundingLot, error)
	ListRefundCasesPage(context.Context, postgresstore.RefundCasePageQuery) (postgresstore.RefundCasePage, error)
	ListEligibilityFreezesPage(context.Context, postgresstore.EligibilityFreezePageQuery) (postgresstore.EligibilityFreezePage, error)
	ResolveEligibilityFreeze(context.Context, string, string, int64, string, string) (postgresstore.EligibilityFreeze, error)
	ListUserEligibilitySummaries(context.Context, string) ([]application.UserEligibilitySummary, error)
	ResolveRefundCase(context.Context, string, string, string, string, string) (postgresstore.RefundCase, error)
	GetInvoiceDeliveryState(context.Context, string, string, bool) (postgresstore.InvoiceDeliveryState, error)
	RequeueEmail(context.Context, string, string, string) (domain.EmailOutbox, error)
	RecordAdminAudit(context.Context, string, string, string, string, string) error
}
