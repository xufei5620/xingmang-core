package application

import (
	"errors"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/postgresstore"
)

// TestPlatformScopedSessionSeesOnlyItsOwnPlatformData is the mandatory
// PostgreSQL integration coverage for XM-INV-PLATFORM-SCOPE (CR-0003): one
// invoice_user with external accounts bound on BOTH Sub2API and New API
// (the multi-platform identity claim scenario from XM-INV-AUTOLOGIN --
// same person, two platforms, one invoice_user, never two), funding lots and
// one invoice request on each platform. A session scoped to one platform
// must only ever see and operate on that platform's rows; a session with no
// platform (OIDC administrator, or a legacy/OIDC user session) is unchanged.
func TestPlatformScopedSessionSeesOnlyItsOwnPlatformData(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)

	user, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://id.example", Subject: "platform-scope-user",
		Email: "scoped@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	const sub2ID = "f1000000-0000-4000-8000-000000000001"
	const newID = "f1000000-0000-4000-8000-000000000002"
	if _, err = store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: sub2ID, SourceType: domain.SourceSub2API, Name: "Sub2API-scope-test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: newID, SourceType: domain.SourceNewAPI, Name: "NewAPI-scope-test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Bind external accounts on BOTH platforms to the SAME invoice_user,
	// through the same BindExternalAccount path the identity
	// projection/platform-password claim flow uses (see
	// docs/handoffs/XM-INV-AUTOLOGIN.md) -- this is the exact scenario
	// CR-0003 says must still resolve to one invoice_user, with the platform
	// boundary enforced at read/write time instead of by splitting identity.
	if _, err = service.BindExternalAccount(ctx, postgresstore.ExternalAccountRecord{
		ID: "f3000000-0000-4000-8000-000000000001", PrincipalID: user.ID,
		SourceInstanceID: sub2ID, ExternalUserID: "sub2-scope-ext-1",
		BindingMethod: "platform_password_login", BindingStatus: "verified",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindExternalAccount(ctx, postgresstore.ExternalAccountRecord{
		ID: "f3000000-0000-4000-8000-000000000002", PrincipalID: user.ID,
		SourceInstanceID: newID, ExternalUserID: "new-scope-ext-1",
		BindingMethod: "platform_password_login", BindingStatus: "verified",
	}); err != nil {
		t.Fatal(err)
	}

	profile, err := service.SaveProfile(ctx, domain.InvoiceProfile{
		PrincipalID: user.ID, Type: domain.ProfileEnterprise, Title: "示例公司",
		TaxID: "91300000000000000X", Email: "scoped@example.com", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !profile.EmailVerified {
		t.Fatalf("profile email should have been server-verified against the user's own verified email: %+v", profile)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	sub2Lot := domain.FundingLot{
		ID: "f5000000-0000-4000-8000-000000000001", PrincipalID: user.ID,
		SourceInstanceID: sub2ID, SourceType: domain.SourceSub2API,
		ExternalOrderID: "sub2-scope-order-1", Currency: domain.CurrencyCNY,
		OriginalMinor: 100_000, CurrentCapMinor: 100_000,
		Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
		SourceRevision: "sub2-scope-r1", CompletedAt: now.Add(-time.Hour), ObservedAt: now,
	}
	if err = store.UpsertFundingLot(ctx, sub2Lot); err != nil {
		t.Fatal(err)
	}
	// New API funding requires a genuine two-person dual-control review
	// (enforced by a database trigger, independent of this task) before a
	// lot can become verified, and a wallet-cash lot may only carry a
	// nonzero consumed_cash_minor once verified (another check constraint).
	// Seed at zero, run the real dual-control review (first reviewer
	// proposes pending->frozen, a second, different reviewer confirms
	// ->verified), then backfill the consumed-cash fixture the same way
	// store_integration_test.go's setFixtureConsumedCash does for Sub2API --
	// none of this is platform-scoping-specific, it is simply what a
	// genuinely submittable New API lot requires.
	newLot := domain.FundingLot{
		ID: "f5000000-0000-4000-8000-000000000002", PrincipalID: user.ID,
		SourceInstanceID: newID, SourceType: domain.SourceNewAPI,
		ExternalOrderID: "new-scope-order-1", Currency: domain.CurrencyCNY,
		OriginalMinor: 100_000, CurrentCapMinor: 0,
		Verification: domain.VerificationPending, SourceStatus: "COMPLETED",
		SourceRevision: "new-scope-r1", CompletedAt: now.Add(-time.Hour), ObservedAt: now,
	}
	if err = store.UpsertFundingLot(ctx, newLot); err != nil {
		t.Fatal(err)
	}
	if _, err = service.VerifyNewAPIPaymentWithEvidence(ctx,
		"90000000-0000-4000-8000-000000000001", newLot.ID, 100_000, domain.CurrencyCNY,
		"scope-test-newapi-evidence"); err != nil {
		t.Fatal(err)
	}
	newLot, err = service.VerifyNewAPIPaymentWithEvidence(ctx,
		"90000000-0000-4000-8000-000000000002", newLot.ID, 100_000, domain.CurrencyCNY,
		"scope-test-newapi-evidence")
	if err != nil || newLot.Verification != domain.VerificationVerified || newLot.CurrentCapMinor != 100_000 {
		t.Fatalf("New API lot dual-control verification: lot=%+v err=%v", newLot, err)
	}
	consumedTx, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = consumedTx.Exec(ctx, `
		UPDATE funding_lot_consumption_state SET cash_service_units=1,consumed_service_units=1,
			cumulative_cash_numerator=$1::numeric,rounded_consumed_cash_minor=$1::bigint,
			rounding_remainder_numerator=0,state_version=state_version+1,updated_at=now()
		WHERE funding_lot_id=$2`, int64(100_000), newLot.ID); err != nil {
		_ = consumedTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err = consumedTx.Exec(ctx, `
		UPDATE funding_lots SET verified_cash_minor=$1,consumed_cash_minor=$1,
			eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE id=$2`, int64(100_000), newLot.ID); err != nil {
		_ = consumedTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = consumedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	newLot.ConsumedCashMinor = 100_000
	newLot.VerifiedCashMinor = 100_000

	// One invoice request per platform, each submitted with its own matching
	// session platform -- proving the scoping check does not reject a
	// legitimate same-platform submission.
	sub2Request, err := service.Submit(ctx, ledger.SubmitInput{
		PrincipalID: user.ID, ProfileID: profile.ID, SourceInstanceID: sub2ID,
		IdempotencyKey: "sub2-scope-submit-1",
		Allocations:    []ledger.AllocationInput{{FundingLotID: sub2Lot.ID, AmountMinor: domain.MinimumRequestMinor}},
	}, domain.SourceSub2API)
	if err != nil {
		t.Fatal(err)
	}
	newRequest, err := service.Submit(ctx, ledger.SubmitInput{
		PrincipalID: user.ID, ProfileID: profile.ID, SourceInstanceID: newID,
		IdempotencyKey: "new-scope-submit-1",
		Allocations:    []ledger.AllocationInput{{FundingLotID: newLot.ID, AmountMinor: domain.MinimumRequestMinor}},
	}, domain.SourceNewAPI)
	if err != nil {
		t.Fatal(err)
	}

	// Fully issue the New API request (with a real document) so the
	// cross-platform 404 checks below for /delivery and /document exercise
	// the actual issued-and-documented state, not just "not found because
	// not issued yet". A third, distinct admin ID is required here: an
	// admin who reviewed this lot's New API payment verification above is
	// forbidden (separation of duties, unrelated to platform scoping) from
	// also confirming the manual issue for a request drawing on it.
	adminID := "90000000-0000-4000-8000-000000000003"
	newRequest, err = service.Review(ctx, adminID, newRequest.ID, "approve", "", newRequest.Version)
	if err != nil {
		t.Fatal(err)
	}
	newRequest, err = service.ConfirmManualIssue(ctx, adminID, newRequest.ID, newRequest.Version)
	if err != nil {
		t.Fatal(err)
	}
	newDoc := domain.InvoiceDocument{
		RequestID: newRequest.ID, InvoiceNumber: "FP-SCOPE-0001",
		ObjectKey: "issued/scope-object.pdf", ObjectVersion: "v1",
		SHA256: strings.Repeat("b", 64), SizeBytes: 4321,
		MIME: "application/pdf", ScanStatus: "clean", IssuedAt: time.Now().UTC(),
	}
	newRequest, _, _, err = service.AttachDocument(ctx, adminID, newDoc, newRequest.Version)
	if err != nil {
		t.Fatal(err)
	}
	if newRequest.Status != domain.StatusIssued {
		t.Fatalf("New API request did not reach issued: status=%s", newRequest.Status)
	}

	// ---- Sub2API-scoped session: only Sub2API rows, cross-platform 404s ----
	lots, err := service.ListFundingLots(ctx, user.ID, domain.SourceSub2API)
	if err != nil || len(lots) != 1 || lots[0].ID != sub2Lot.ID {
		t.Fatalf("sub2api-scoped funding lots=%+v err=%v", lots, err)
	}
	accounts, err := service.ListExternalAccounts(ctx, user.ID, domain.SourceSub2API)
	if err != nil || len(accounts) != 1 || accounts[0].SourceType != domain.SourceSub2API {
		t.Fatalf("sub2api-scoped source accounts=%+v err=%v", accounts, err)
	}
	// ListUserEligibilitySummaries (application-layer) additionally requires
	// source-freshness Options this lightweight harness does not configure
	// (see ListFundingLots's own doc comment on that same gap); the
	// platform-filtering predicate under test lives in the store method it
	// wraps, so exercise that directly here -- the same pattern
	// eligibility_operations_integration_test.go already uses.
	summaries, err := store.ListEligibilitySummaries(ctx, user.ID, domain.SourceSub2API)
	if err != nil || len(summaries) != 1 || summaries[0].SourceType != domain.SourceSub2API {
		t.Fatalf("sub2api-scoped eligibility summaries=%+v err=%v", summaries, err)
	}
	requests, err := service.ListRequests(ctx, user.ID, false, domain.SourceSub2API)
	if err != nil || len(requests) != 1 || requests[0].ID != sub2Request.ID {
		t.Fatalf("sub2api-scoped requests=%+v err=%v", requests, err)
	}
	if _, err = service.GetRequest(ctx, user.ID, newRequest.ID, false, domain.SourceSub2API); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sub2api-scoped GET of the New API request should 404, got %v", err)
	}
	if _, err = service.GetInvoiceDeliveryState(ctx, user.ID, newRequest.ID, false, domain.SourceSub2API); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sub2api-scoped delivery state of the issued New API request should 404, got %v", err)
	}
	if _, err = service.GetDocumentForRequest(ctx, user.ID, newRequest.ID, domain.SourceSub2API); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sub2api-scoped document of the issued New API request should 404, got %v", err)
	}
	// Cancelling the other platform's request must also 404, even though its
	// actual status (issued) would otherwise fail with a different error --
	// the platform check must win so a scoped session can never learn
	// anything about a request outside its scope.
	if _, err = service.Cancel(ctx, user.ID, newRequest.ID, newRequest.Version, domain.SourceSub2API); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sub2api-scoped cancel of the New API request should 404, got %v", err)
	}
	// Submitting against a New API lot while scoped to Sub2API is rejected,
	// and rejected atomically (nothing is reserved/partially accepted).
	if _, err = service.Submit(ctx, ledger.SubmitInput{
		PrincipalID: user.ID, ProfileID: profile.ID, SourceInstanceID: newID,
		IdempotencyKey: "sub2-scope-cross-platform-attempt",
		Allocations:    []ledger.AllocationInput{{FundingLotID: newLot.ID, AmountMinor: domain.MinimumRequestMinor}},
	}, domain.SourceSub2API); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("sub2api-scoped submit against a New API lot should be rejected, got %v", err)
	}

	// ---- New API-scoped session: only New API rows, and legitimate
	// same-platform access to the issued request still works ----
	lots, err = service.ListFundingLots(ctx, user.ID, domain.SourceNewAPI)
	if err != nil || len(lots) != 1 || lots[0].ID != newLot.ID {
		t.Fatalf("newapi-scoped funding lots=%+v err=%v", lots, err)
	}
	accounts, err = service.ListExternalAccounts(ctx, user.ID, domain.SourceNewAPI)
	if err != nil || len(accounts) != 1 || accounts[0].SourceType != domain.SourceNewAPI {
		t.Fatalf("newapi-scoped source accounts=%+v err=%v", accounts, err)
	}
	summaries, err = store.ListEligibilitySummaries(ctx, user.ID, domain.SourceNewAPI)
	if err != nil || len(summaries) != 1 || summaries[0].SourceType != domain.SourceNewAPI {
		t.Fatalf("newapi-scoped eligibility summaries=%+v err=%v", summaries, err)
	}
	requests, err = service.ListRequests(ctx, user.ID, false, domain.SourceNewAPI)
	if err != nil || len(requests) != 1 || requests[0].ID != newRequest.ID {
		t.Fatalf("newapi-scoped requests=%+v err=%v", requests, err)
	}
	if _, err = service.GetRequest(ctx, user.ID, sub2Request.ID, false, domain.SourceNewAPI); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("newapi-scoped GET of the Sub2API request should 404, got %v", err)
	}
	if delivery, err := service.GetInvoiceDeliveryState(ctx, user.ID, newRequest.ID, false, domain.SourceNewAPI); err != nil || delivery.Document.ID == "" {
		t.Fatalf("newapi-scoped delivery state of its own issued request should succeed: state=%+v err=%v", delivery, err)
	}
	if doc, err := service.GetDocumentForRequest(ctx, user.ID, newRequest.ID, domain.SourceNewAPI); err != nil || doc.InvoiceNumber != "FP-SCOPE-0001" {
		t.Fatalf("newapi-scoped document of its own issued request should succeed: doc=%+v err=%v", doc, err)
	}
	if _, err = service.Cancel(ctx, user.ID, sub2Request.ID, sub2Request.Version, domain.SourceNewAPI); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("newapi-scoped cancel of the Sub2API request should 404, got %v", err)
	}

	// ---- Admin (unscoped) session sees both ----
	adminRequests, err := service.ListRequests(ctx, "", true, "")
	if err != nil || len(adminRequests) != 2 {
		t.Fatalf("admin should see both platforms' requests: requests=%+v err=%v", adminRequests, err)
	}

	// ---- Session without a platform (legacy/OIDC user) is unchanged ----
	unscopedLots, err := service.ListFundingLots(ctx, user.ID, "")
	if err != nil || len(unscopedLots) != 2 {
		t.Fatalf("unscoped session should see both platforms' lots: lots=%+v err=%v", unscopedLots, err)
	}
	unscopedRequests, err := service.ListRequests(ctx, user.ID, false, "")
	if err != nil || len(unscopedRequests) != 2 {
		t.Fatalf("unscoped session should see both platforms' requests: requests=%+v err=%v", unscopedRequests, err)
	}
	if _, err = service.GetRequest(ctx, user.ID, newRequest.ID, false, ""); err != nil {
		t.Fatalf("unscoped session should reach the New API request: err=%v", err)
	}
	if _, err = service.GetRequest(ctx, user.ID, sub2Request.ID, false, ""); err != nil {
		t.Fatalf("unscoped session should reach the Sub2API request: err=%v", err)
	}
}
