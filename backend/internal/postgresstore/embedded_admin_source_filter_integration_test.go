package postgresstore

import (
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// XM-INV-ADMIN-EMBED: the embedded admin console filters every admin list by
// one platform's source_instance_id. invoice-requests and eligibility-freezes
// already supported the filter; these two tests cover the newly added
// PaymentCandidatePageQuery.SourceInstanceID and RefundCasePageQuery.
// SourceInstanceID paths end to end against real PostgreSQL.

func TestListPaymentCandidatesPageFiltersBySourceInstance(t *testing.T) {
	store, ctx := integrationStore(t)
	_, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name)
		VALUES('10000000-0000-4000-8000-000000000022','newapi','newapi-a'),
		      ('10000000-0000-4000-8000-000000000023','newapi','newapi-b');
		INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES('30000000-0000-4000-8000-000000000022','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000022','new-a','manual','verified'),
		      ('30000000-0000-4000-8000-000000000023','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000023','new-b','manual','verified')`)
	if err != nil {
		t.Fatal(err)
	}
	// ObservedAt is captured immediately before each call (not hoisted into
	// shared literals) because UpsertFundingLot's fixture bootstrap derives
	// its cutover_at from time.Now() *inside* the call: a value captured
	// earlier and reused after a prior call's own round-trip can land before
	// that cutover and trip the finalized_through>=cutover_at check.
	lotA := domain.FundingLot{
		ID: "50000000-0000-4000-8000-000000000030", PrincipalID: "20000000-0000-4000-8000-000000000001",
		SourceInstanceID: "10000000-0000-4000-8000-000000000022", SourceType: domain.SourceNewAPI,
		ExternalOrderID: "candidate-a", Currency: domain.CurrencyCNY, OriginalMinor: 40_000, CurrentCapMinor: 0,
		Verification: domain.VerificationPending, SourceStatus: "candidate:success", SourceRevision: "cas-r1",
		ObservedAt: time.Now().UTC(),
	}
	if err := store.UpsertFundingLot(ctx, lotA); err != nil {
		t.Fatal(err)
	}
	lotB := domain.FundingLot{
		ID: "50000000-0000-4000-8000-000000000031", PrincipalID: "20000000-0000-4000-8000-000000000001",
		SourceInstanceID: "10000000-0000-4000-8000-000000000023", SourceType: domain.SourceNewAPI,
		ExternalOrderID: "candidate-b", Currency: domain.CurrencyCNY, OriginalMinor: 50_000, CurrentCapMinor: 0,
		Verification: domain.VerificationPending, SourceStatus: "candidate:success", SourceRevision: "cas-r1",
		ObservedAt: time.Now().UTC(),
	}
	if err := store.UpsertFundingLot(ctx, lotB); err != nil {
		t.Fatal(err)
	}

	pageA, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{
		Limit: 50, SourceInstanceID: "10000000-0000-4000-8000-000000000022",
	})
	if err != nil || len(pageA.Items) != 1 || pageA.Items[0].ID != lotA.ID {
		t.Fatalf("source-scoped page A items=%+v err=%v", pageA.Items, err)
	}

	pageB, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{
		Limit: 50, SourceInstanceID: "10000000-0000-4000-8000-000000000023",
	})
	if err != nil || len(pageB.Items) != 1 || pageB.Items[0].ID != lotB.ID {
		t.Fatalf("source-scoped page B items=%+v err=%v", pageB.Items, err)
	}

	unscoped, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{Limit: 50})
	if err != nil || len(unscoped.Items) != 2 {
		t.Fatalf("unscoped page items=%+v err=%v", unscoped.Items, err)
	}

	// A real, non-matching (sub2api) source instance combines with the
	// handler's hardcoded newapi-only filter to correctly return nothing,
	// not an error.
	mismatched, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{
		Limit: 50, SourceInstanceID: "10000000-0000-4000-8000-000000000001",
	})
	if err != nil || len(mismatched.Items) != 0 {
		t.Fatalf("mismatched-platform source page items=%+v err=%v", mismatched.Items, err)
	}

	if _, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{
		Limit: 50, SourceInstanceID: "not-a-uuid",
	}); err == nil {
		t.Fatal("expected an invalid source instance filter to be rejected")
	}
}

func TestListRefundCasesPageFiltersBySourceInstanceThroughFundingLots(t *testing.T) {
	store, ctx := integrationStore(t)
	_, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name)
		VALUES('10000000-0000-4000-8000-000000000024','sub2api','refund-src-b');
		INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES('30000000-0000-4000-8000-000000000024','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000024','refund-b','manual','verified');
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,
			currency,original_minor,current_cap_minor,verification_state,source_status,
			source_revision_hash,observed_at)
		VALUES
			('50000000-0000-4000-8000-000000000040','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','refund-order-a','CNY',50000,50000,'verified','COMPLETED','refund-r1',now()),
			('50000000-0000-4000-8000-000000000041','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000024','10000000-0000-4000-8000-000000000024','refund-order-b','CNY',60000,60000,'verified','COMPLETED','refund-r1',now());
		INSERT INTO invoice_requests(
			id,request_no,invoice_user_id,source_instance_id,profile_id,
			profile_snapshot_ciphertext,currency,issuer_code,service_item,
			amount_minor,status,idempotency_key,version,eligibility_policy_start_at,
			eligibility_policy_version,submitted_at,updated_at)
		VALUES
			('60000000-0000-4000-8000-000000000040','REFUND-SRC-A','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','40000000-0000-4000-8000-000000000001',decode('01','hex'),'CNY','default','技术服务',50000,'issued','refund-idem-a',1,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),now(),now()),
			('60000000-0000-4000-8000-000000000041','REFUND-SRC-B','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000024','40000000-0000-4000-8000-000000000001',decode('01','hex'),'CNY','default','技术服务',60000,'issued','refund-idem-b',1,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),now(),now());
		INSERT INTO refund_cases(
			id,invoice_request_id,funding_lot_id,source_revision_hash,
			observed_refund_minor,issued_exposure_minor,observed_cap_minor)
		VALUES
			('70000000-0000-4000-8000-000000000040','60000000-0000-4000-8000-000000000040','50000000-0000-4000-8000-000000000040','refund-r1',10000,50000,40000),
			('70000000-0000-4000-8000-000000000041','60000000-0000-4000-8000-000000000041','50000000-0000-4000-8000-000000000041','refund-r1',20000,60000,40000)`)
	if err != nil {
		t.Fatal(err)
	}

	pageA, err := store.ListRefundCasesPage(ctx, RefundCasePageQuery{
		Limit: 50, Status: "open", SourceInstanceID: "10000000-0000-4000-8000-000000000001",
	})
	if err != nil || len(pageA.Items) != 1 || pageA.Items[0].ID != "70000000-0000-4000-8000-000000000040" {
		t.Fatalf("source-scoped refund page A items=%+v err=%v", pageA.Items, err)
	}

	pageB, err := store.ListRefundCasesPage(ctx, RefundCasePageQuery{
		Limit: 50, Status: "open", SourceInstanceID: "10000000-0000-4000-8000-000000000024",
	})
	if err != nil || len(pageB.Items) != 1 || pageB.Items[0].ID != "70000000-0000-4000-8000-000000000041" {
		t.Fatalf("source-scoped refund page B items=%+v err=%v", pageB.Items, err)
	}

	unscoped, err := store.ListRefundCasesPage(ctx, RefundCasePageQuery{Limit: 50, Status: "open"})
	if err != nil || len(unscoped.Items) != 2 {
		t.Fatalf("unscoped refund page items=%+v err=%v", unscoped.Items, err)
	}

	if _, err := store.ListRefundCasesPage(ctx, RefundCasePageQuery{
		Limit: 50, Status: "open", SourceInstanceID: "not-a-uuid",
	}); err == nil {
		t.Fatal("expected an invalid source instance filter to be rejected")
	}
}
