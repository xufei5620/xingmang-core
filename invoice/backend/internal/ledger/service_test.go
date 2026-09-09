package ledger

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

func seedProfile(t *testing.T, service *Service, principal string) domain.InvoiceProfile {
	t.Helper()
	profile, err := service.SaveProfile(context.Background(), domain.InvoiceProfile{
		PrincipalID: principal, Type: domain.ProfileEnterprise, Title: "示例科技有限公司",
		TaxID: "91310000TEST000001", Email: "invoice@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("save profile: %v", err)
	}
	return profile
}

func seedLot(t *testing.T, service *Service, id, principal, source string, amount int64, verification domain.VerificationState) {
	t.Helper()
	err := service.AddFundingLot(context.Background(), domain.FundingLot{
		ID: id, PrincipalID: principal, SourceInstanceID: source, SourceType: domain.SourceSub2API,
		ExternalOrderID: "order-" + id, TradeNo: "trade-" + id, Currency: domain.CurrencyCNY,
		OriginalMinor: amount, CurrentCapMinor: amount, Verification: verification,
		SourceStatus: "COMPLETED", CompletedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("add lot: %v", err)
	}
}

func TestSubmitIsIdempotent(t *testing.T) {
	service := NewService()
	profile := seedProfile(t, service, "user-1")
	seedLot(t, service, "lot-1", "user-1", "sub2-main", 50_000, domain.VerificationVerified)
	input := SubmitInput{PrincipalID: "user-1", ProfileID: profile.ID, SourceInstanceID: "sub2-main", IdempotencyKey: "browser-request-1", Allocations: []AllocationInput{{FundingLotID: "lot-1", AmountMinor: 20_000}}}
	first, err := service.Submit(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent submit created two requests: %s and %s", first.ID, second.ID)
	}
	lots, err := service.ListFundingLots(context.Background(), "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := lots[0].ReservedMinor; got != 20_000 {
		t.Fatalf("reserved=%d, want 20000", got)
	}
}

func TestSubmitRejectsMinimumAmountAndSourceMixing(t *testing.T) {
	service := NewService()
	profile := seedProfile(t, service, "user-1")
	seedLot(t, service, "lot-a", "user-1", "sub2-main", 50_000, domain.VerificationVerified)
	seedLot(t, service, "lot-b", "user-1", "sub2-second", 50_000, domain.VerificationVerified)
	_, err := service.Submit(context.Background(), SubmitInput{PrincipalID: "user-1", ProfileID: profile.ID, SourceInstanceID: "sub2-main", IdempotencyKey: "small", Allocations: []AllocationInput{{FundingLotID: "lot-a", AmountMinor: 19_999}}}, "")
	if !errors.Is(err, domain.ErrMinimumAmount) {
		t.Fatalf("minimum: got %v", err)
	}
	_, err = service.Submit(context.Background(), SubmitInput{PrincipalID: "user-1", ProfileID: profile.ID, SourceInstanceID: "sub2-main", IdempotencyKey: "mixed", Allocations: []AllocationInput{{FundingLotID: "lot-a", AmountMinor: 20_000}, {FundingLotID: "lot-b", AmountMinor: 20_000}}}, "")
	if !errors.Is(err, domain.ErrSourceMixing) {
		t.Fatalf("source mixing: got %v", err)
	}
}

func TestNewAPICandidateMustBeVerified(t *testing.T) {
	service := NewService()
	profile := seedProfile(t, service, "user-1")
	seedLot(t, service, "lot-pending", "user-1", "newapi-main", 30_000, domain.VerificationPending)
	lot := service.lots["lot-pending"]
	lot.SourceType = domain.SourceNewAPI
	service.lots[lot.ID] = lot
	_, err := service.Submit(context.Background(), SubmitInput{PrincipalID: "user-1", ProfileID: profile.ID, SourceInstanceID: "newapi-main", IdempotencyKey: "pending", Allocations: []AllocationInput{{FundingLotID: "lot-pending", AmountMinor: 20_000}}}, "")
	if !errors.Is(err, domain.ErrUnverifiedPayment) {
		t.Fatalf("got %v", err)
	}
}

func TestNewAPICandidateCanBeManuallyVerifiedOnce(t *testing.T) {
	service := NewService()
	seedLot(t, service, "lot-pending", "user-1", "newapi-main", 0, domain.VerificationPending)
	lot := service.lots["lot-pending"]
	lot.SourceType = domain.SourceNewAPI
	service.lots[lot.ID] = lot

	verified, err := service.VerifyNewAPIPayment(context.Background(), lot.ID, 25_000, domain.CurrencyCNY)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Verification != domain.VerificationVerified || verified.CurrentCapMinor != 25_000 {
		t.Fatalf("unexpected verified lot: %+v", verified)
	}
	if _, err = service.VerifyNewAPIPayment(context.Background(), lot.ID, 25_000, domain.CurrencyCNY); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("second verification got %v", err)
	}
}

func TestConcurrentSubmitCannotOverReserve(t *testing.T) {
	service := NewService()
	profile := seedProfile(t, service, "user-1")
	seedLot(t, service, "lot-1", "user-1", "sub2-main", 100_000, domain.VerificationVerified)
	const workers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	var succeeded atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := service.Submit(context.Background(), SubmitInput{PrincipalID: "user-1", ProfileID: profile.ID, SourceInstanceID: "sub2-main", IdempotencyKey: "req-" + time.Now().Add(time.Duration(i)).Format(time.RFC3339Nano), Allocations: []AllocationInput{{FundingLotID: "lot-1", AmountMinor: 20_000}}}, "")
			if err == nil {
				succeeded.Add(1)
			} else if !errors.Is(err, domain.ErrInsufficientAmount) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if got := succeeded.Load(); got != 5 {
		t.Fatalf("successful submissions=%d, want 5", got)
	}
	lots, err := service.ListFundingLots(context.Background(), "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	lot := lots[0]
	if lot.ReservedMinor != 100_000 || lot.AvailableMinor() != 0 {
		t.Fatalf("lot after concurrency: %+v", lot)
	}
}

func TestManualIssueDoesNotReleaseWhenDocumentMissing(t *testing.T) {
	service := NewService()
	profile := seedProfile(t, service, "user-1")
	seedLot(t, service, "lot-1", "user-1", "sub2-main", 50_000, domain.VerificationVerified)
	request, err := service.Submit(context.Background(), SubmitInput{PrincipalID: "user-1", ProfileID: profile.ID, SourceInstanceID: "sub2-main", IdempotencyKey: "issue", Allocations: []AllocationInput{{FundingLotID: "lot-1", AmountMinor: 20_000}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	request, err = service.Review(context.Background(), "admin-1", request.ID, "approve", "ok", request.Version)
	if err != nil {
		t.Fatal(err)
	}
	request, err = service.ConfirmManualIssue(context.Background(), "admin-1", request.ID, request.Version)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.StatusIssuedAwaitingDocument {
		t.Fatalf("status=%s", request.Status)
	}
	lots, err := service.ListFundingLots(context.Background(), "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	lot := lots[0]
	if lot.ReservedMinor != 0 || lot.IssuedMinor != 20_000 {
		t.Fatalf("issued allocation was released: %+v", lot)
	}
}
