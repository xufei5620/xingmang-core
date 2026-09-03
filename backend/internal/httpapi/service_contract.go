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
//
// XM-INV-PLATFORM-SCOPE: several methods gained a trailing domain.SourceType
// "platform" parameter (empty = unscoped). It is always sourced server-side
// from the authenticated session (httpapi.sessionPlatform), never from
// client-controlled input -- see docs/handoffs/XM-INV-PLATFORM-SCOPE.md for
// the CR-0003 rationale (a platform-password session must only ever see and
// operate on that platform's data). An OIDC/admin session's platform is
// always empty, so passing it through unconditionally is a no-op for them.
type InvoiceService interface {
	MinimumRequestMinor() int64
	SetMinimumRequestMinor(int64) error
	SaveProfile(context.Context, domain.InvoiceProfile) (domain.InvoiceProfile, error)
	ListProfiles(context.Context, string) ([]domain.InvoiceProfile, error)
	ListFundingLots(context.Context, string, domain.SourceType) ([]domain.FundingLot, error)
	Submit(context.Context, ledger.SubmitInput, domain.SourceType) (domain.InvoiceRequest, error)
	Cancel(context.Context, string, string, int64, domain.SourceType) (domain.InvoiceRequest, error)
	Review(context.Context, string, string, string, string, int64) (domain.InvoiceRequest, error)
	BeginManualIssue(context.Context, string, string, int64) (domain.InvoiceRequest, error)
	ConfirmManualIssue(context.Context, string, string, int64) (domain.InvoiceRequest, error)
	AttachDocument(context.Context, string, domain.InvoiceDocument, int64) (domain.InvoiceRequest, domain.InvoiceDocument, domain.EmailOutbox, error)
	GetDocumentForRequest(context.Context, string, string, domain.SourceType) (domain.InvoiceDocument, error)
	GetDocumentForRequestAsAdmin(context.Context, string) (domain.InvoiceDocument, error)
	GetRequest(context.Context, string, string, bool, domain.SourceType) (domain.InvoiceRequest, error)
	ListRequests(context.Context, string, bool, domain.SourceType) ([]domain.InvoiceRequest, error)
	VerifyNewAPIPaymentWithEvidence(context.Context, string, string, int64, string, string) (domain.FundingLot, error)
}

// OperationsService: see the XM-INV-PLATFORM-SCOPE note on InvoiceService --
// the same trailing domain.SourceType "platform" convention applies here.
type OperationsService interface {
	SourceHealth(context.Context) (postgresstore.SourceHealthReport, error)
	ListExternalAccounts(context.Context, string, domain.SourceType) ([]postgresstore.ConnectedSourceAccount, error)
	ListPaymentCandidatesPage(context.Context, postgresstore.PaymentCandidatePageQuery) (postgresstore.PaymentCandidatePage, error)
	RejectNewAPIPaymentWithEvidence(context.Context, string, string, string, string) (domain.FundingLot, error)
	FreezeNewAPIPaymentWithEvidence(context.Context, string, string, string, string) (domain.FundingLot, error)
	ApplyNewAPIManualCap(context.Context, string, string, int64, string, string) (domain.FundingLot, error)
	ListRefundCasesPage(context.Context, postgresstore.RefundCasePageQuery) (postgresstore.RefundCasePage, error)
	ListEligibilityFreezesPage(context.Context, postgresstore.EligibilityFreezePageQuery) (postgresstore.EligibilityFreezePage, error)
	ResolveEligibilityFreeze(context.Context, string, string, int64, string, string) (postgresstore.EligibilityFreeze, error)
	ListEligibilityLedgerPage(context.Context, postgresstore.EligibilityLedgerPageQuery) (postgresstore.EligibilityLedgerPage, error)
	ListUserEligibilitySummaries(context.Context, string, domain.SourceType) ([]application.UserEligibilitySummary, error)
	ResolveRefundCase(context.Context, string, string, string, string, string) (postgresstore.RefundCase, error)
	GetInvoiceDeliveryState(context.Context, string, string, bool, domain.SourceType) (postgresstore.InvoiceDeliveryState, error)
	RequeueEmail(context.Context, string, string, string) (domain.EmailOutbox, error)
	RecordAdminAudit(context.Context, string, string, string, string, string) error
}
