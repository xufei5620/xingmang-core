package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/postgresstore"
)

// fakeSourceFilterOperations is a narrow OperationsService double: it embeds
// a real *ledger.Service for InvoiceService (so it can back a full *Server),
// captures the query passed to the two handlers this file tests, and returns
// canned pages. Every other OperationsService method panics -- these tests
// exercise only listPaymentCandidates and listRefundCases, and a panic makes
// an unexpected call path loud instead of silently returning zero values.
type fakeSourceFilterOperations struct {
	*ledger.Service

	paymentCandidatesQuery postgresstore.PaymentCandidatePageQuery
	paymentCandidatesPage  postgresstore.PaymentCandidatePage

	refundCasesQuery postgresstore.RefundCasePageQuery
	refundCasesPage  postgresstore.RefundCasePage
}

func (f *fakeSourceFilterOperations) SourceHealth(context.Context) (postgresstore.SourceHealthReport, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ListExternalAccounts(context.Context, string) ([]postgresstore.ConnectedSourceAccount, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ListPaymentCandidatesPage(_ context.Context, in postgresstore.PaymentCandidatePageQuery) (postgresstore.PaymentCandidatePage, error) {
	f.paymentCandidatesQuery = in
	return f.paymentCandidatesPage, nil
}
func (f *fakeSourceFilterOperations) RejectNewAPIPaymentWithEvidence(context.Context, string, string, string, string) (domain.FundingLot, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) FreezeNewAPIPaymentWithEvidence(context.Context, string, string, string, string) (domain.FundingLot, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ApplyNewAPIManualCap(context.Context, string, string, int64, string, string) (domain.FundingLot, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ListRefundCasesPage(_ context.Context, in postgresstore.RefundCasePageQuery) (postgresstore.RefundCasePage, error) {
	f.refundCasesQuery = in
	return f.refundCasesPage, nil
}
func (f *fakeSourceFilterOperations) ListEligibilityFreezesPage(context.Context, postgresstore.EligibilityFreezePageQuery) (postgresstore.EligibilityFreezePage, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ResolveEligibilityFreeze(context.Context, string, string, int64, string, string) (postgresstore.EligibilityFreeze, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ListUserEligibilitySummaries(context.Context, string) ([]application.UserEligibilitySummary, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) ResolveRefundCase(context.Context, string, string, string, string, string) (postgresstore.RefundCase, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) GetInvoiceDeliveryState(context.Context, string, string, bool) (postgresstore.InvoiceDeliveryState, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) RequeueEmail(context.Context, string, string, string) (domain.EmailOutbox, error) {
	panic("unused in this test")
}
func (f *fakeSourceFilterOperations) RecordAdminAudit(context.Context, string, string, string, string, string) error {
	panic("unused in this test")
}

func sourceFilterTestServer(t *testing.T) (*Server, *fakeSourceFilterOperations) {
	t.Helper()
	fake := &fakeSourceFilterOperations{Service: ledger.NewService()}
	server, err := NewWithConfig(fake, Config{
		AuthMode: "mock", AdminIPAllowlist: []string{"127.0.0.1/32", "::1/128"},
		BreakGlassCIDRs: []string{"127.0.0.1/32", "::1/128"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, fake
}

func TestListPaymentCandidatesHandlerForwardsSourceInstanceFilter(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	fake.paymentCandidatesPage = postgresstore.PaymentCandidatePage{Items: []domain.FundingLot{
		{ID: "50000000-0000-4000-8000-000000000030", PrincipalID: "20000000-0000-4000-8000-000000000001",
			SourceInstanceID: "10000000-0000-4000-8000-000000000022", SourceType: domain.SourceNewAPI,
			ExternalOrderID: "candidate-a", Currency: domain.CurrencyCNY, OriginalMinor: 40_000,
			CurrentCapMinor: 40_000, Verification: domain.VerificationPending, SourceStatus: "candidate:success",
			ObservedAt: time.Now().UTC()},
	}}

	request := adminRequest("GET", "/api/v1/admin/payment-candidates?source_instance_id=10000000-0000-4000-8000-000000000022", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.paymentCandidatesQuery.SourceInstanceID != "10000000-0000-4000-8000-000000000022" {
		t.Fatalf("handler did not forward source_instance_id: query=%+v", fake.paymentCandidatesQuery)
	}
	var body struct {
		Items []struct {
			SourceInstanceID string `json:"source_instance_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].SourceInstanceID != "10000000-0000-4000-8000-000000000022" {
		t.Fatalf("unexpected response body: %s", recorder.Body.String())
	}

	// Omitting the parameter must leave the filter empty (unscoped), same as
	// before this field existed.
	unscopedRequest := adminRequest("GET", "/api/v1/admin/payment-candidates", "127.0.0.1", "")
	unscopedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(unscopedRecorder, unscopedRequest)
	if unscopedRecorder.Code != 200 {
		t.Fatalf("unscoped status=%d body=%s", unscopedRecorder.Code, unscopedRecorder.Body.String())
	}
	if fake.paymentCandidatesQuery.SourceInstanceID != "" {
		t.Fatalf("expected no source instance filter, got %q", fake.paymentCandidatesQuery.SourceInstanceID)
	}
}

func TestListRefundCasesHandlerForwardsSourceInstanceFilter(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	fake.refundCasesPage = postgresstore.RefundCasePage{Items: []postgresstore.RefundCase{
		{ID: "70000000-0000-4000-8000-000000000040", RequestID: "60000000-0000-4000-8000-000000000040",
			FundingLotID: "50000000-0000-4000-8000-000000000040", Status: "open",
			ObservedRefundMinor: 10_000, IssuedExposureMinor: 50_000, OpenedAt: time.Now().UTC()},
	}}

	request := adminRequest("GET", "/api/v1/admin/refund-cases?source_instance_id=10000000-0000-4000-8000-000000000024", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.refundCasesQuery.SourceInstanceID != "10000000-0000-4000-8000-000000000024" {
		t.Fatalf("handler did not forward source_instance_id: query=%+v", fake.refundCasesQuery)
	}
	var body struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "70000000-0000-4000-8000-000000000040" {
		t.Fatalf("unexpected response body: %s", recorder.Body.String())
	}

	unscopedRequest := adminRequest("GET", "/api/v1/admin/refund-cases", "127.0.0.1", "")
	unscopedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(unscopedRecorder, unscopedRequest)
	if unscopedRecorder.Code != 200 {
		t.Fatalf("unscoped status=%d body=%s", unscopedRecorder.Code, unscopedRecorder.Body.String())
	}
	if fake.refundCasesQuery.SourceInstanceID != "" {
		t.Fatalf("expected no source instance filter, got %q", fake.refundCasesQuery.SourceInstanceID)
	}
}
