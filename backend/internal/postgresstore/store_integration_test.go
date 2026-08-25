package postgresstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/migrate"
)

func waitIntegrationPool(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL integration pool did not become reachable: %v", err)
}

func integrationStore(t *testing.T) (*Store, context.Context) {
	return integrationStoreWithPolicyStart(t, time.Now().UTC().Add(-24*time.Hour).Truncate(time.Second))
}

func integrationStoreWithPolicyStart(t *testing.T, fixturePolicyStart time.Time) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	waitIntegrationPool(t, pool)
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	fixturePolicyStart = fixturePolicyStart.UTC().Truncate(time.Second)
	// Tests use an isolated disposable schema and a movable boundary to exercise
	// T-1/T/T+1 cases. Production policy updates are permanently rejected.
	if _, err = pool.Exec(ctx, `ALTER TABLE invoice_eligibility_policy
		DISABLE TRIGGER invoice_eligibility_policy_guard`); err != nil {
		t.Fatalf("disable immutable policy guard in isolated fixture: %v", err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE invoice_eligibility_policy
		SET eligibility_start_at=$1,policy_version=policy_version+1,updated_by='integration-test'
		WHERE singleton_id=1`, fixturePolicyStart); err != nil {
		t.Fatalf("set pre-ingestion eligibility policy: %v", err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE invoice_eligibility_policy
		ENABLE TRIGGER invoice_eligibility_policy_guard`); err != nil {
		t.Fatalf("re-enable immutable policy guard in isolated fixture: %v", err)
	}
	seedSQL := `INSERT INTO admin_settings(singleton_id,issuer_name,service_item,minimum_request_minor,smtp_host,smtp_port,smtp_from,smtp_from_name,smtp_starttls,admin_cidrs,revision,updated_by) VALUES(1,'测试开票主体','技术服务',20000,'smtp.qq.com',587,'invoice@qq.com','发票中心',TRUE,ARRAY['127.0.0.1/32']::inet[],1,'integration-test'); INSERT INTO source_instances(id,source_type,name) VALUES('10000000-0000-4000-8000-000000000001','sub2api','test'); INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES('20000000-0000-4000-8000-000000000001','test','user'); INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status) VALUES('30000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','u1','test','verified')`
	if _, err = pool.Exec(ctx, seedSQL); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	_, err = store.SaveProfile(ctx, ProfileRecord{ID: "40000000-0000-4000-8000-000000000001", PrincipalID: "20000000-0000-4000-8000-000000000001", Type: domain.ProfileEnterprise, TitleCiphertext: []byte("encrypted-title"), EmailCiphertext: []byte("encrypted-email")})
	if err != nil {
		t.Fatal(err)
	}
	lot := domain.FundingLot{ID: "50000000-0000-4000-8000-000000000001", PrincipalID: "20000000-0000-4000-8000-000000000001", SourceInstanceID: "10000000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API, ExternalOrderID: "order-1", Currency: domain.CurrencyCNY, OriginalMinor: 100_000, CurrentCapMinor: 100_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED", SourceRevision: "r1", CompletedAt: time.Now(), ObservedAt: time.Now()}
	if err = store.UpsertFundingLot(ctx, lot); err != nil {
		t.Fatal(err)
	}
	var storedCompleted, storedCutover time.Time
	if err = store.pool.QueryRow(ctx, `SELECT completed_at,eligibility_cutover_at FROM funding_lots WHERE id=$1`, lot.ID).
		Scan(&storedCompleted, &storedCutover); err != nil {
		t.Fatal(err)
	}
	if !storedCompleted.After(storedCutover) {
		t.Fatalf("fixture payment boundary collapsed after PostgreSQL round-trip: completed=%s cutover=%s", storedCompleted, storedCutover)
	}
	return store, ctx
}

func setFixtureConsumedCash(t *testing.T, store *Store, ctx context.Context, lotID string, consumedMinor int64) {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `
		UPDATE funding_lot_consumption_state SET cash_service_units=1,consumed_service_units=1,
			cumulative_cash_numerator=$1::numeric,rounded_consumed_cash_minor=$1::bigint,
			rounding_remainder_numerator=0,state_version=state_version+1,updated_at=now()
		WHERE funding_lot_id=$2`, consumedMinor, lotID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE funding_lots SET consumed_cash_minor=$1,eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE id=$2 AND verified_cash_minor>=$1`, consumedMinor, lotID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitTransactionIdempotencyAndConcurrency(t *testing.T) {
	store, ctx := integrationStore(t)
	base := SubmitInput{PrincipalID: "20000000-0000-4000-8000-000000000001", ProfileID: "40000000-0000-4000-8000-000000000001", SourceInstanceID: "10000000-0000-4000-8000-000000000001", ProfileSnapshotCiphertext: []byte("encrypted-snapshot"), Allocations: []AllocationInput{{FundingLotID: "50000000-0000-4000-8000-000000000001", AmountMinor: 20_000}}}
	first := base
	first.IdempotencyKey = "same"
	a, err := store.Submit(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	var requestPolicyStart, currentPolicyStart time.Time
	var requestPolicyVersion, currentPolicyVersion int64
	if err = store.pool.QueryRow(ctx, `
		SELECT ir.eligibility_policy_start_at,ir.eligibility_policy_version,
			p.eligibility_start_at,p.policy_version
		FROM invoice_requests ir CROSS JOIN invoice_eligibility_policy p
		WHERE ir.id=$1`, a.ID).Scan(&requestPolicyStart, &requestPolicyVersion,
		&currentPolicyStart, &currentPolicyVersion); err != nil {
		t.Fatal(err)
	}
	if !requestPolicyStart.Equal(currentPolicyStart) || requestPolicyVersion != currentPolicyVersion {
		t.Fatal("invoice request did not snapshot the immutable eligibility policy")
	}
	b, err := store.Submit(ctx, first)
	if err != nil || a.ID != b.ID {
		t.Fatalf("idempotency: ids %s %s err=%v", a.ID, b.ID, err)
	}
	changedProfile := first
	changedProfile.ProfileID = "40000000-0000-4000-8000-000000000099"
	if _, err = store.Submit(ctx, changedProfile); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed profile with reused key: got %v", err)
	}
	const workers = 8
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := base
			in.IdempotencyKey = time.Now().Add(time.Duration(i)).Format(time.RFC3339Nano)
			_, err := store.Submit(ctx, in)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, domain.ErrInsufficientAmount) {
				t.Errorf("unexpected submit error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 4 {
		t.Fatalf("successes=%d want 4", successes.Load())
	}
	var reserved int64
	if err = store.pool.QueryRow(ctx, `SELECT reserved_minor FROM funding_lots WHERE id='50000000-0000-4000-8000-000000000001'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 100_000 {
		t.Fatalf("reserved=%d", reserved)
	}
}

func observeFixtureFundingCompletion(store *Store, ctx context.Context, lot domain.FundingLot,
	completedAt, observedAt time.Time, revision string, sequence int64) (ObservationResult, error) {
	lot.CompletedAt = completedAt
	lot.ObservedAt = observedAt
	lot.UpdatedAt = observedAt
	lot.SourceRevision = revision
	return store.ObserveFundingLot(ctx, SourceObservation{
		Lot: lot, ExternalUserID: "u1", EventKind: "payment",
		ExternalEventID: "completion-observation-" + revision[:8], SchemaVersion: "test",
		SourceUpdatedAt: observedAt, SourceSequence: sequence,
	}, AuditActor{Type: "source_connector", ID: lot.SourceInstanceID})
}

func TestFundingCompletionDriftPreservesOriginalAndInvalidatesPending(t *testing.T) {
	store, ctx := integrationStore(t)
	lot, err := store.GetFundingLot(ctx, "50000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	originalCompleted := lot.CompletedAt
	observed := lot.ObservedAt.Add(time.Minute)
	zeroRescan := lot
	if _, err = observeFixtureFundingCompletion(store, ctx, zeroRescan, time.Time{}, observed,
		strings.Repeat("a", 64), 1); err != nil {
		t.Fatalf("zero completion rescan changed a settled payment: %v", err)
	}
	lot, err = store.GetFundingLot(ctx, lot.ID)
	if err != nil || !lot.CompletedAt.Equal(originalCompleted) || lot.EligibilityStatus != "active" {
		t.Fatalf("zero rescan lot=%+v err=%v", lot, err)
	}
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID: "20000000-0000-4000-8000-000000000001", ProfileID: "40000000-0000-4000-8000-000000000001",
		SourceInstanceID: lot.SourceInstanceID, ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		IdempotencyKey: "completion-drift-pending",
		Allocations:    []AllocationInput{{FundingLotID: lot.ID, AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var policyStart time.Time
	if err = store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	_, err = observeFixtureFundingCompletion(store, ctx, lot, policyStart.Add(-time.Microsecond),
		observed.Add(time.Minute), strings.Repeat("b", 64), 2)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-boundary completion drift error=%v", err)
	}
	lot, err = store.GetFundingLot(ctx, lot.ID)
	if err != nil || !lot.CompletedAt.Equal(originalCompleted) || lot.EligibilityStatus != "frozen" || lot.AvailableMinor() != 0 {
		t.Fatalf("drifted lot remained usable: lot=%+v err=%v", lot, err)
	}
	record, err := store.GetRequestRecord(ctx, request.PrincipalID, request.ID, false)
	if err != nil || record.Request.Status != domain.StatusRejected {
		t.Fatalf("pending request was not invalidated: request=%+v err=%v", record.Request, err)
	}
	var evidenceEvents int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_events
		WHERE source_instance_id=$1 AND external_event_id LIKE 'completion-observation-%'`,
		lot.SourceInstanceID).Scan(&evidenceEvents); err != nil {
		t.Fatal(err)
	}
	if evidenceEvents != 2 {
		t.Fatalf("completion observations retained=%d want 2", evidenceEvents)
	}
}

func TestFundingCompletionSameSideDriftMarksIssuedAttention(t *testing.T) {
	store, ctx := integrationStore(t)
	lot, err := store.GetFundingLot(ctx, "50000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID: "20000000-0000-4000-8000-000000000001", ProfileID: "40000000-0000-4000-8000-000000000001",
		SourceInstanceID: lot.SourceInstanceID, ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		IdempotencyKey: "completion-drift-issued",
		Allocations:    []AllocationInput{{FundingLotID: lot.ID, AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminID := "90000000-0000-4000-8000-000000000001"
	reviewed, err := store.ReviewRequest(ctx, adminID, request.ID, "approve", "", request.Version,
		AuditActor{Type: "admin", ID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := store.ConfirmManualIssue(ctx, ConfirmIssueInput{
		AdminID: adminID, RequestID: request.ID, ExpectedVersion: reviewed.Request.Version,
		IssuerSettingRevision: 1, IssueSnapshotCiphertext: []byte("encrypted-issuer"),
		Actor: AuditActor{Type: "admin", ID: adminID},
	})
	if err != nil || issued.Request.Status != domain.StatusIssuedAwaitingDocument {
		t.Fatalf("issue result=%+v err=%v", issued.Request, err)
	}
	_, err = observeFixtureFundingCompletion(store, ctx, lot, lot.CompletedAt.Add(time.Minute),
		lot.ObservedAt.Add(time.Minute), strings.Repeat("c", 64), 1)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same-side completion drift error=%v", err)
	}
	record, err := store.GetRequestRecord(ctx, request.PrincipalID, request.ID, false)
	if err != nil || record.Request.Status != domain.StatusRefundAttention {
		t.Fatalf("issued request did not enter attention: request=%+v err=%v", record.Request, err)
	}
	page, err := store.ListRefundCasesPage(ctx, RefundCasePageQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].RequestID != request.ID {
		t.Fatalf("completion drift attention cases=%+v err=%v", page.Items, err)
	}
}

func TestSaveProfileMovesDefaultAtomically(t *testing.T) {
	store, ctx := integrationStore(t)
	second := ProfileRecord{ID: "40000000-0000-4000-8000-000000000002", PrincipalID: "20000000-0000-4000-8000-000000000001", Type: domain.ProfilePersonal, TitleCiphertext: []byte("encrypted-title-2"), EmailCiphertext: []byte("encrypted-email-2"), IsDefault: true}
	if _, err := store.SaveProfile(ctx, second); err != nil {
		t.Fatal(err)
	}
	first := ProfileRecord{ID: "40000000-0000-4000-8000-000000000001", PrincipalID: second.PrincipalID, Type: domain.ProfileEnterprise, TitleCiphertext: []byte("encrypted-title-1"), EmailCiphertext: []byte("encrypted-email-1"), IsDefault: true}
	if _, err := store.SaveProfile(ctx, first); err != nil {
		t.Fatal(err)
	}
	var defaults int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM invoice_profiles WHERE invoice_user_id=$1 AND is_default`, second.PrincipalID).Scan(&defaults); err != nil {
		t.Fatal(err)
	}
	if defaults != 1 {
		t.Fatalf("default profiles=%d want 1", defaults)
	}
}

func TestInvoiceRequestServiceItemIsDatabaseLocked(t *testing.T) {
	store, ctx := integrationStore(t)
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID:               "20000000-0000-4000-8000-000000000001",
		ProfileID:                 "40000000-0000-4000-8000-000000000001",
		SourceInstanceID:          "10000000-0000-4000-8000-000000000001",
		ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		IdempotencyKey:            "service-item-constraint",
		Allocations:               []AllocationInput{{FundingLotID: "50000000-0000-4000-8000-000000000001", AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE invoice_requests SET service_item='非技术服务' WHERE id=$1`, request.ID); err == nil {
		t.Fatal("database accepted a non-fixed invoice service item")
	}
}

func TestWorkflowCASMovesReservationToIssuedExactlyOnce(t *testing.T) {
	store, ctx := integrationStore(t)
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID:               "20000000-0000-4000-8000-000000000001",
		ProfileID:                 "40000000-0000-4000-8000-000000000001",
		SourceInstanceID:          "10000000-0000-4000-8000-000000000001",
		ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		IdempotencyKey:            "workflow-cas",
		Allocations:               []AllocationInput{{FundingLotID: "50000000-0000-4000-8000-000000000001", AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminID := "90000000-0000-4000-8000-000000000001"
	actor := AuditActor{Type: "admin", ID: adminID, Reason: "integration test"}
	var reviewSuccess atomic.Int64
	var reviewed RequestRecord
	var reviewedMu sync.Mutex
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, reviewErr := store.ReviewRequest(ctx, adminID, request.ID, "approve", "", request.Version, actor)
			if reviewErr == nil {
				reviewSuccess.Add(1)
				reviewedMu.Lock()
				reviewed = result
				reviewedMu.Unlock()
				return
			}
			if !errors.Is(reviewErr, domain.ErrVersionConflict) {
				t.Errorf("unexpected concurrent review error: %v", reviewErr)
			}
		}()
	}
	wg.Wait()
	if reviewSuccess.Load() != 1 {
		t.Fatalf("review successes=%d", reviewSuccess.Load())
	}
	var issueSuccess atomic.Int64
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, issueErr := store.ConfirmManualIssue(ctx, ConfirmIssueInput{
				AdminID: adminID, RequestID: request.ID, ExpectedVersion: reviewed.Request.Version,
				IssuerSettingRevision: 1, IssueSnapshotCiphertext: []byte("encrypted-immutable-issuer"),
				Actor: actor,
			})
			if issueErr == nil {
				issueSuccess.Add(1)
				return
			}
			if !errors.Is(issueErr, domain.ErrVersionConflict) {
				t.Errorf("unexpected concurrent issue error: %v", issueErr)
			}
		}()
	}
	wg.Wait()
	if issueSuccess.Load() != 1 {
		t.Fatalf("issue successes=%d", issueSuccess.Load())
	}
	var reserved, issued int64
	if err = store.pool.QueryRow(ctx, `SELECT reserved_minor,issued_minor FROM funding_lots WHERE id='50000000-0000-4000-8000-000000000001'`).Scan(&reserved, &issued); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || issued != 20_000 {
		t.Fatalf("reserved=%d issued=%d", reserved, issued)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE invoice_requests SET issue_snapshot_ciphertext='changed'::bytea WHERE id=$1`, request.ID); err == nil {
		t.Fatal("database allowed immutable issue snapshot to change")
	}
}

func TestConfirmManualIssueRejectsIssuerRevisionDriftAtomically(t *testing.T) {
	store, ctx := integrationStore(t)
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID:               "20000000-0000-4000-8000-000000000001",
		ProfileID:                 "40000000-0000-4000-8000-000000000001",
		SourceInstanceID:          "10000000-0000-4000-8000-000000000001",
		ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		IdempotencyKey:            "issuer-revision-drift",
		Allocations:               []AllocationInput{{FundingLotID: "50000000-0000-4000-8000-000000000001", AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminID := "90000000-0000-4000-8000-000000000001"
	reviewed, err := store.ReviewRequest(ctx, adminID, request.ID, "approve", "", request.Version,
		AuditActor{Type: "admin", ID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	var reservedBefore, issuedBefore, auditBefore int64
	var allocationBefore string
	if err = store.pool.QueryRow(ctx, `SELECT reserved_minor,issued_minor FROM funding_lots WHERE id=$1`, "50000000-0000-4000-8000-000000000001").Scan(&reservedBefore, &issuedBefore); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT allocation_state FROM invoice_allocations WHERE invoice_request_id=$1`, request.ID).Scan(&allocationBefore); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE object_type='invoice_request' AND object_id=$1`, request.ID).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	// Simulate the settings update winning after the application captured the
	// revision-1 snapshot but before the issue transaction starts.
	if _, err = store.pool.Exec(ctx, `UPDATE admin_settings SET issuer_name='新测试开票主体',revision=2,updated_by='concurrent-admin',updated_at=now() WHERE singleton_id=1 AND revision=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{
		AdminID: adminID, RequestID: request.ID, ExpectedVersion: reviewed.Request.Version,
		IssuerSettingRevision: 1, IssueSnapshotCiphertext: bytes.Repeat([]byte{0x42}, 32),
		Actor: AuditActor{Type: "admin", ID: adminID},
	}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("issuer revision drift error=%v", err)
	}
	after, err := store.GetRequestRecord(ctx, request.PrincipalID, request.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var reservedAfter, issuedAfter, auditAfter int64
	var allocationAfter string
	if err = store.pool.QueryRow(ctx, `SELECT reserved_minor,issued_minor FROM funding_lots WHERE id=$1`, "50000000-0000-4000-8000-000000000001").Scan(&reservedAfter, &issuedAfter); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT allocation_state FROM invoice_allocations WHERE invoice_request_id=$1`, request.ID).Scan(&allocationAfter); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE object_type='invoice_request' AND object_id=$1`, request.ID).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if after.Request.Status != reviewed.Request.Status || after.Request.Version != reviewed.Request.Version ||
		after.IssuerSettingRevision != 0 || len(after.IssueSnapshotCiphertext) != 0 ||
		reservedAfter != reservedBefore || issuedAfter != issuedBefore || allocationAfter != allocationBefore || auditAfter != auditBefore {
		t.Fatalf("revision mismatch changed state: request=%+v issuer_revision=%d snapshot=%d reserved=%d/%d issued=%d/%d allocation=%s/%s audits=%d/%d",
			after.Request, after.IssuerSettingRevision, len(after.IssueSnapshotCiphertext), reservedBefore, reservedAfter,
			issuedBefore, issuedAfter, allocationBefore, allocationAfter, auditBefore, auditAfter)
	}
}

func TestNewAPIPaymentVerificationRequiresEvidenceAndAuditsOnlyHash(t *testing.T) {
	store, ctx := integrationStore(t)
	_, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name)
		VALUES('10000000-0000-4000-8000-000000000002','newapi','newapi-test');
		INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES(
			'30000000-0000-4000-8000-000000000002',
			'20000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000002','new-user','manual','verified')`)
	if err != nil {
		t.Fatal(err)
	}
	lot := domain.FundingLot{
		ID:               "50000000-0000-4000-8000-000000000002",
		PrincipalID:      "20000000-0000-4000-8000-000000000001",
		SourceInstanceID: "10000000-0000-4000-8000-000000000002",
		SourceType:       domain.SourceNewAPI, ExternalOrderID: "new-order-1",
		Currency: domain.CurrencyCNY, OriginalMinor: 50_000, CurrentCapMinor: 0,
		Verification: domain.VerificationPending,
		SourceStatus: "success-unverified", SourceRevision: "new-r1",
		ObservedAt: time.Now().UTC(),
	}
	if err = store.UpsertFundingLot(ctx, lot); err != nil {
		t.Fatal(err)
	}
	adminID := "90000000-0000-4000-8000-000000000001"
	candidates, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{Limit: 100})
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].ID != lot.ID {
		t.Fatalf("candidate queue=%+v err=%v", candidates, err)
	}
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: strings.Repeat("9", 64), Ciphertext: bytes.Repeat([]byte{0x39}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x38}, 32), ReasonHash: strings.Repeat("8", 64),
		Actor: AuditActor{Type: "admin", ID: lot.PrincipalID},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("funding owner was allowed to verify their own payment: %v", err)
	}
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
		Actor: AuditActor{Type: "admin", ID: adminID},
	}); err == nil {
		t.Fatalf("empty evidence error=%v", err)
	}
	if frozen, freezeErr := store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "freeze",
		Evidence:         PaymentEvidence{Hash: strings.Repeat("a", 64), Ciphertext: bytes.Repeat([]byte{0x31}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x41}, 32),
		ReasonHash:       strings.Repeat("1", 64),
		Actor:            AuditActor{Type: "admin", ID: adminID},
	}); freezeErr != nil || frozen.Verification != domain.VerificationFrozen {
		t.Fatalf("frozen lot=%+v err=%v", frozen, freezeErr)
	}
	if rejected, rejectErr := store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "reject",
		Evidence:         PaymentEvidence{Hash: strings.Repeat("b", 64), Ciphertext: bytes.Repeat([]byte{0x32}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x42}, 32),
		ReasonHash:       strings.Repeat("2", 64),
		Actor:            AuditActor{Type: "admin", ID: adminID},
	}); rejectErr != nil || rejected.Verification != domain.VerificationFrozen {
		t.Fatalf("rejected lot=%+v err=%v", rejected, rejectErr)
	}
	evidence := "bank-reference-sensitive-value"
	evidenceHash := strings.Repeat("f", 64)
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 50_001, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: strings.Repeat("e", 64), Ciphertext: bytes.Repeat([]byte{0x45}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x43}, 32), ReasonHash: strings.Repeat("3", 64),
		Actor: AuditActor{Type: "admin", ID: adminID},
	}); !errors.Is(err, domain.ErrInsufficientAmount) {
		t.Fatalf("amount above signed candidate upper bound error=%v", err)
	}
	proposed, err := store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: evidenceHash, Ciphertext: bytes.Repeat([]byte{0x44}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x43}, 32),
		ReasonHash:       strings.Repeat("3", 64),
		Actor:            AuditActor{Type: "admin", ID: adminID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Verification != domain.VerificationFrozen || proposed.CurrentCapMinor != 0 || proposed.OriginalMinor != 50_000 {
		t.Fatalf("first reviewer minted entitlement: %+v", proposed)
	}
	if retried, retryErr := store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: evidenceHash, Ciphertext: bytes.Repeat([]byte{0x44}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x43}, 32),
		ReasonHash:       strings.Repeat("3", 64),
		Actor:            AuditActor{Type: "admin", ID: adminID},
	}); retryErr != nil || retried.Verification != domain.VerificationFrozen || retried.CurrentCapMinor != 0 {
		t.Fatalf("same administrator self-approved: lot=%+v error=%v", retried, retryErr)
	}
	candidates, err = store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{Limit: 100})
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].ManualReviewStage != "proposed" {
		t.Fatalf("proposed candidate queue=%+v err=%v", candidates, err)
	}
	secondAdminID := "90000000-0000-4000-8000-000000000002"
	verified, err := store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: evidenceHash, Ciphertext: bytes.Repeat([]byte{0x55}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x53}, 32), ReasonHash: strings.Repeat("3", 64),
		Actor: AuditActor{Type: "admin", ID: secondAdminID},
	})
	if err != nil || verified.Verification != domain.VerificationVerified || verified.CurrentCapMinor != 30_000 || verified.OriginalMinor != 50_000 {
		t.Fatalf("second-person verification lot=%+v err=%v", verified, err)
	}
	// This test exercises the independent-payment dual-control path. Under the
	// consumption eligibility rule a wallet top-up is still unavailable until
	// a signed usage fact consumes it; seed that postcondition explicitly.
	setFixtureConsumedCash(t, store, ctx, lot.ID, 30_000)
	verifiedPage, err := store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{
		Limit: 100, States: []domain.VerificationState{domain.VerificationVerified},
	})
	if err != nil || len(verifiedPage.Items) != 1 || verifiedPage.Items[0].ID != lot.ID || verifiedPage.Items[0].ManualReviewStage != "approved" {
		t.Fatalf("verified New API management page=%+v err=%v", verifiedPage, err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE payment_candidate_decisions SET paid_minor=50001 WHERE funding_lot_id=$1`, lot.ID); err == nil {
		t.Fatal("database allowed a decision above the signed candidate upper bound")
	}
	if _, err = store.pool.Exec(ctx, `UPDATE payment_candidate_decisions
		SET proposed_by='90000000-0000-4000-8000-000000000099' WHERE funding_lot_id=$1`, lot.ID); err == nil {
		t.Fatal("database allowed an approved decision reviewer to be rewritten")
	}
	if _, err = store.pool.Exec(ctx, `DELETE FROM payment_candidate_decisions WHERE funding_lot_id=$1`, lot.ID); err == nil {
		t.Fatal("database allowed an approved decision to be deleted")
	}
	if _, err = store.pool.Exec(ctx, `UPDATE payment_candidate_reviews SET admin_id='90000000-0000-4000-8000-000000000099'
		WHERE funding_lot_id=$1 AND review_action='verify_approve'`, lot.ID); err == nil {
		t.Fatal("database allowed immutable payment review evidence to be rewritten")
	}
	if _, err = store.pool.Exec(ctx, `UPDATE funding_lots SET current_cap_minor=31000 WHERE id=$1`, lot.ID); err == nil {
		t.Fatal("database allowed verified New API cap to diverge from dual-control decision")
	}
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 31_000, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: evidenceHash, Ciphertext: bytes.Repeat([]byte{0x44}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x43}, 32), ReasonHash: strings.Repeat("3", 64),
		Actor: AuditActor{Type: "admin", ID: adminID},
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same evidence with changed paid amount error=%v", err)
	}
	var reason string
	if err = store.pool.QueryRow(ctx, `
		SELECT reason FROM audit_events
		WHERE object_type='funding_lot' AND object_id=$1 AND action='funding_lot.manual_review.verify_approve'`, lot.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reason, evidence) || !strings.Contains(reason, "sha256:") || !strings.Contains(reason, evidenceHash) {
		t.Fatalf("unsafe evidence audit reason=%q", reason)
	}
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID:               "20000000-0000-4000-8000-000000000001",
		ProfileID:                 "40000000-0000-4000-8000-000000000001",
		SourceInstanceID:          "10000000-0000-4000-8000-000000000002",
		ProfileSnapshotCiphertext: []byte("encrypted-newapi-profile"),
		IdempotencyKey:            "newapi-reviewer-separation",
		Allocations:               []AllocationInput{{FundingLotID: lot.ID, AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReviewRequest(ctx, request.PrincipalID, request.ID, "approve", "", request.Version,
		AuditActor{Type: "admin", ID: request.PrincipalID}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("request owner was allowed to review their own invoice: %v", err)
	}
	issuerAdmin := "90000000-0000-4000-8000-000000000003"
	reviewed, err := store.ReviewRequest(ctx, issuerAdmin, request.ID, "approve", "", request.Version,
		AuditActor{Type: "admin", ID: issuerAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginManualIssue(ctx, adminID, request.ID, reviewed.Request.Version,
		AuditActor{Type: "admin", ID: adminID}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("proposal reviewer was allowed to issue: %v", err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{
		AdminID: secondAdminID, RequestID: request.ID, ExpectedVersion: reviewed.Request.Version,
		IssuerSettingRevision: 1, IssueSnapshotCiphertext: []byte("encrypted-issuer"),
		Actor: AuditActor{Type: "admin", ID: secondAdminID},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("approval reviewer was allowed to issue: %v", err)
	}
	begun, err := store.BeginManualIssue(ctx, issuerAdmin, request.ID, reviewed.Request.Version,
		AuditActor{Type: "admin", ID: issuerAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `
		UPDATE invoice_requests SET eligibility_policy_version=eligibility_policy_version+1
		WHERE id=$1`, request.ID); err == nil {
		t.Fatal("database allowed immutable request policy snapshot mutation")
	}
	// Exercise the independent issue-time defense by simulating physical
	// corruption in this disposable schema after proving normal SQL is blocked.
	if _, err = store.pool.Exec(ctx, `ALTER TABLE invoice_requests
		DISABLE TRIGGER invoice_requests_immutable_fields`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE invoice_requests
		SET eligibility_policy_version=eligibility_policy_version+1 WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `ALTER TABLE invoice_requests
		ENABLE TRIGGER invoice_requests_immutable_fields`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{
		AdminID: issuerAdmin, RequestID: request.ID, ExpectedVersion: begun.Request.Version,
		IssuerSettingRevision: 1, IssueSnapshotCiphertext: []byte("encrypted-issuer"),
		Actor: AuditActor{Type: "admin", ID: issuerAdmin},
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("policy snapshot drift was issuable: %v", err)
	}
	if _, err = store.pool.Exec(ctx, `ALTER TABLE invoice_requests
		DISABLE TRIGGER invoice_requests_immutable_fields`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE invoice_requests
		SET eligibility_policy_version=eligibility_policy_version-1 WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `ALTER TABLE invoice_requests
		ENABLE TRIGGER invoice_requests_immutable_fields`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{
		AdminID: issuerAdmin, RequestID: request.ID, ExpectedVersion: begun.Request.Version,
		IssuerSettingRevision: 1, IssueSnapshotCiphertext: []byte("encrypted-issuer"),
		Actor: AuditActor{Type: "admin", ID: issuerAdmin},
	}); err != nil {
		t.Fatalf("independent issuer failed: %v", err)
	}
	adjustedLot, err := store.GetFundingLot(ctx, lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	manualCeiling := int64(0)
	adjustedLot.CurrentCapMinor = manualCeiling
	adjustedLot.Verification = domain.VerificationFrozen
	adjustedLot.SourceStatus = "MANUAL_REFUND_OR_FREEZE"
	adjustedLot.SourceRevision = "manual-cap-r1"
	adjustedLot.ObservedAt = time.Now().UTC().Add(time.Minute)
	adjustmentResult, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: adjustedLot, ExternalUserID: "new-user", EventKind: "refund",
		ExternalEventID: "manual-newapi-cap-test", SchemaVersion: "manual-newapi-cap-v1",
		PayloadCiphertext:        bytes.Repeat([]byte{0x77}, 32),
		NewAPIManualCeilingMinor: &manualCeiling,
	}, AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000004", Reason: "manual refund evidence hash:test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(adjustmentResult.AttentionRequestIDs) != 1 || adjustmentResult.AttentionRequestIDs[0] != request.ID ||
		adjustmentResult.Lot.CurrentCapMinor != 0 || adjustmentResult.Lot.Verification != domain.VerificationFrozen {
		t.Fatalf("manual New API cap did not reuse refund workflow: %+v", adjustmentResult)
	}
	var observedRefund, issuedExposure int64
	if err = store.pool.QueryRow(ctx, `
		SELECT observed_refund_minor,issued_exposure_minor FROM refund_cases
		WHERE invoice_request_id=$1 AND funding_lot_id=$2 AND status='open'`, request.ID, lot.ID).
		Scan(&observedRefund, &issuedExposure); err != nil || observedRefund != 30_000 || issuedExposure != 20_000 {
		t.Fatalf("manual refund case refund=%d exposure=%d err=%v", observedRefund, issuedExposure, err)
	}
	var decisionCount int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM payment_candidate_decisions WHERE funding_lot_id=$1`, lot.ID).Scan(&decisionCount); err != nil || decisionCount != 1 {
		t.Fatalf("immutable approved payment decision was not retained: count=%d err=%v", decisionCount, err)
	}
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: lot.ID, Action: "verify", PaidMinor: 1, Currency: domain.CurrencyCNY,
		Evidence:         PaymentEvidence{Hash: strings.Repeat("8", 64), Ciphertext: bytes.Repeat([]byte{0x78}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x79}, 32), ReasonHash: strings.Repeat("7", 64),
		Actor: AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000005"},
	}); !errors.Is(err, domain.ErrInsufficientAmount) && !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("manual zero ceiling was restorable: %v", err)
	}
	candidates, err = store.ListPaymentCandidatesPage(ctx, PaymentCandidatePageQuery{Limit: 100})
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].OriginalMinor != 0 || candidates.Items[0].Verification != domain.VerificationFrozen {
		t.Fatalf("manually frozen lot queue=%+v err=%v", candidates, err)
	}
}

func TestPaymentCandidateDualControlCASConcurrent(t *testing.T) {
	store, ctx := integrationStore(t)
	_, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name)
		VALUES('10000000-0000-4000-8000-000000000022','newapi','newapi-cas');
		INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES('30000000-0000-4000-8000-000000000022',
			'20000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000022','new-cas','manual','verified')`)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id, order string) {
		t.Helper()
		if err := store.UpsertFundingLot(ctx, domain.FundingLot{
			ID: id, PrincipalID: "20000000-0000-4000-8000-000000000001",
			SourceInstanceID: "10000000-0000-4000-8000-000000000022",
			SourceType:       domain.SourceNewAPI, ExternalOrderID: order,
			Currency: domain.CurrencyCNY, OriginalMinor: 40_000, CurrentCapMinor: 0,
			Verification: domain.VerificationPending, SourceStatus: "candidate:success",
			SourceRevision: "cas-r1", ObservedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	input := func(lotID, adminID string) ReviewPaymentCandidateInput {
		return ReviewPaymentCandidateInput{
			LotID: lotID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
			Evidence:         PaymentEvidence{Hash: strings.Repeat("c", 64), Ciphertext: bytes.Repeat([]byte{0x61}, 32)},
			ReasonCiphertext: bytes.Repeat([]byte{0x62}, 32), ReasonHash: strings.Repeat("d", 64),
			Actor: AuditActor{Type: "admin", ID: adminID},
		}
	}

	lotID := "50000000-0000-4000-8000-000000000022"
	seed(lotID, "new-cas-order")
	admins := []string{
		"90000000-0000-4000-8000-000000000021",
		"90000000-0000-4000-8000-000000000022",
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(admins))
	for _, adminID := range admins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reviewErr := store.ReviewNewAPIPaymentCandidate(ctx, input(lotID, adminID))
			errs <- reviewErr
		}()
	}
	wg.Wait()
	close(errs)
	for reviewErr := range errs {
		if reviewErr != nil {
			t.Fatalf("concurrent distinct reviewer failed: %v", reviewErr)
		}
	}
	lot, err := store.GetFundingLot(ctx, lotID)
	if err != nil || lot.Verification != domain.VerificationVerified || lot.CurrentCapMinor != 30_000 || lot.OriginalMinor != 40_000 {
		t.Fatalf("dual-control CAS result=%+v err=%v", lot, err)
	}
	var state string
	var distinctReviewers int
	if err = store.pool.QueryRow(ctx, `
		SELECT d.state,count(DISTINCT r.admin_id)
		FROM payment_candidate_decisions d
		JOIN payment_candidate_reviews r ON r.funding_lot_id=d.funding_lot_id
		WHERE d.funding_lot_id=$1 AND r.review_action IN ('verify_propose','verify_approve')
		GROUP BY d.state`, lotID).Scan(&state, &distinctReviewers); err != nil || state != "approved" || distinctReviewers != 2 {
		t.Fatalf("decision state=%q reviewers=%d err=%v", state, distinctReviewers, err)
	}

	// Concurrent retries by one administrator remain a proposal and cannot
	// self-approve, regardless of request scheduling.
	sameAdminLotID := "50000000-0000-4000-8000-000000000023"
	seed(sameAdminLotID, "new-same-admin-order")
	sameAdmin := "90000000-0000-4000-8000-000000000023"
	errs = make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reviewErr := store.ReviewNewAPIPaymentCandidate(ctx, input(sameAdminLotID, sameAdmin))
			errs <- reviewErr
		}()
	}
	wg.Wait()
	close(errs)
	for reviewErr := range errs {
		if reviewErr != nil {
			t.Fatalf("same-admin idempotent retry failed: %v", reviewErr)
		}
	}
	lot, err = store.GetFundingLot(ctx, sameAdminLotID)
	if err != nil || lot.Verification != domain.VerificationFrozen || lot.CurrentCapMinor != 0 {
		t.Fatalf("same admin self-approved concurrently: lot=%+v err=%v", lot, err)
	}

	invalidatedLotID := "50000000-0000-4000-8000-000000000024"
	seed(invalidatedLotID, "new-invalidated-proposal")
	firstReviewer := "90000000-0000-4000-8000-000000000024"
	if proposed, proposeErr := store.ReviewNewAPIPaymentCandidate(ctx, input(invalidatedLotID, firstReviewer)); proposeErr != nil || proposed.Verification != domain.VerificationFrozen {
		t.Fatalf("seed proposal=%+v err=%v", proposed, proposeErr)
	}
	rejectingAdmin := "90000000-0000-4000-8000-000000000025"
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
		LotID: invalidatedLotID, Action: "reject",
		Evidence:         PaymentEvidence{Hash: strings.Repeat("e", 64), Ciphertext: bytes.Repeat([]byte{0x71}, 32)},
		ReasonCiphertext: bytes.Repeat([]byte{0x72}, 32), ReasonHash: strings.Repeat("f", 64),
		Actor: AuditActor{Type: "admin", ID: rejectingAdmin},
	}); err != nil {
		t.Fatal(err)
	}
	newReviewer := "90000000-0000-4000-8000-000000000026"
	afterReject, err := store.ReviewNewAPIPaymentCandidate(ctx, input(invalidatedLotID, newReviewer))
	if err != nil || afterReject.Verification != domain.VerificationFrozen || afterReject.CurrentCapMinor != 0 {
		t.Fatalf("old proposal survived rejection: lot=%+v err=%v", afterReject, err)
	}
	var currentProposer string
	if err = store.pool.QueryRow(ctx, `SELECT proposed_by::text FROM payment_candidate_decisions WHERE funding_lot_id=$1`, invalidatedLotID).Scan(&currentProposer); err != nil || currentProposer != newReviewer {
		t.Fatalf("proposal was not restarted after rejection: proposer=%q err=%v", currentProposer, err)
	}
	finalApprover := "90000000-0000-4000-8000-000000000027"
	approved, err := store.ReviewNewAPIPaymentCandidate(ctx, input(invalidatedLotID, finalApprover))
	if err != nil || approved.Verification != domain.VerificationVerified {
		t.Fatalf("fresh two-person review after rejection failed: lot=%+v err=%v", approved, err)
	}
	partialCeiling := int64(10_000)
	approved.CurrentCapMinor = partialCeiling
	approved.Verification = domain.VerificationFrozen
	approved.SourceStatus = "MANUAL_REFUND_OR_FREEZE"
	approved.SourceRevision = "manual-partial-r1"
	approved.ObservedAt = time.Now().UTC().Add(time.Minute)
	if _, err = store.ObserveFundingLot(ctx, SourceObservation{
		Lot: approved, ExternalUserID: "new-cas", EventKind: "refund",
		ExternalEventID: "manual-partial-cap", SchemaVersion: "manual-newapi-cap-v1",
		PayloadCiphertext: bytes.Repeat([]byte{0x7a}, 32), NewAPIManualCeilingMinor: &partialCeiling,
	}, AuditActor{Type: "admin", ID: rejectingAdmin}); err != nil {
		t.Fatal(err)
	}
	overCeiling := input(invalidatedLotID, "90000000-0000-4000-8000-000000000028")
	overCeiling.PaidMinor = 10_001
	if _, err = store.ReviewNewAPIPaymentCandidate(ctx, overCeiling); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("partial manual ceiling was bypassed: %v", err)
	}
	netProposal := input(invalidatedLotID, "90000000-0000-4000-8000-000000000028")
	netProposal.PaidMinor = partialCeiling
	if _, reviewErr := store.ReviewNewAPIPaymentCandidate(ctx, netProposal); !errors.Is(reviewErr, domain.ErrInvalidState) {
		t.Fatalf("permanently refunded lot was reviewable again: %v", reviewErr)
	}
	rescanned := domain.FundingLot{
		ID: invalidatedLotID, PrincipalID: "20000000-0000-4000-8000-000000000001",
		SourceInstanceID: "10000000-0000-4000-8000-000000000022",
		SourceType:       domain.SourceNewAPI, ExternalOrderID: "new-invalidated-proposal",
		Currency: domain.CurrencyCNY, OriginalMinor: 40_000, CurrentCapMinor: 0,
		Verification: domain.VerificationPending, SourceStatus: "candidate:success",
		SourceRevision: "source-rescan-after-refund", ObservedAt: time.Now().UTC().Add(2 * time.Minute),
	}
	if _, err = store.ObserveFundingLot(ctx, SourceObservation{
		Lot: rescanned, ExternalUserID: "new-cas", EventKind: "payment",
		ExternalEventID: "new-invalidated-proposal-rescan", SchemaVersion: "source-agent-v2",
	}, AuditActor{Type: "source_connector", ID: "10000000-0000-4000-8000-000000000022"}); err != nil {
		t.Fatal(err)
	}
	rescanned, err = store.GetFundingLot(ctx, invalidatedLotID)
	if err != nil || rescanned.CurrentCapMinor != partialCeiling || rescanned.Verification != domain.VerificationFrozen || !rescanned.RefundFrozen {
		t.Fatalf("source candidate cleared permanent refund freeze: lot=%+v err=%v", rescanned, err)
	}
}

func TestListHardCapsAndAdminKeysetPagination(t *testing.T) {
	store, ctx := integrationStore(t)
	_, err := store.pool.Exec(ctx, `
		INSERT INTO invoice_requests(
			id,request_no,invoice_user_id,source_instance_id,profile_id,
			profile_snapshot_ciphertext,currency,issuer_code,service_item,
			amount_minor,status,idempotency_key,version,eligibility_policy_start_at,
			eligibility_policy_version,submitted_at,updated_at)
		SELECT md5('page-request-'||gs::text)::uuid,'PAGE-'||gs,
			'20000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000001',
			'40000000-0000-4000-8000-000000000001',decode('01','hex'),
			'CNY','default','技术服务',20000,'pending_review','page-idem-'||gs,1,
			(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),
			(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),
			now()-(gs||' milliseconds')::interval,now()-(gs||' milliseconds')::interval
		FROM generate_series(1,205) gs;

		INSERT INTO invoice_profiles(
			id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified,is_default)
		SELECT md5('page-profile-'||gs::text)::uuid,
			'20000000-0000-4000-8000-000000000001','personal',decode('01','hex'),
			decode('02','hex'),TRUE,FALSE
		FROM generate_series(1,501) gs;

		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,
			currency,original_minor,current_cap_minor,verification_state,source_status,
			source_revision_hash,observed_at)
		SELECT md5('page-lot-'||gs::text)::uuid,
			'20000000-0000-4000-8000-000000000001',
			'30000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000001','page-order-'||gs,
			'CNY',20000,20000,'verified','COMPLETED','page-r1',now()
		FROM generate_series(1,500) gs`)
	if err != nil {
		t.Fatal(err)
	}
	userRecords, err := store.ListRequestRecords(ctx, "20000000-0000-4000-8000-000000000001", false, 9999)
	if err != nil || len(userRecords) != 200 {
		t.Fatalf("user request cap=%d err=%v", len(userRecords), err)
	}
	first, err := store.ListRequestRecordsPage(ctx, RequestPageQuery{Admin: true, Limit: 9999})
	if err != nil || len(first.Items) != 100 || !first.HasMore || first.NextBeforeID == "" {
		t.Fatalf("first page items=%d more=%v cursor=%q err=%v", len(first.Items), first.HasMore, first.NextBeforeID, err)
	}
	second, err := store.ListRequestRecordsPage(ctx, RequestPageQuery{
		Admin: true, Limit: 100, BeforeSubmittedAt: first.NextBeforeSubmittedAt, BeforeID: first.NextBeforeID,
	})
	if err != nil || len(second.Items) != 100 || !second.HasMore {
		t.Fatalf("second page items=%d more=%v err=%v", len(second.Items), second.HasMore, err)
	}
	third, err := store.ListRequestRecordsPage(ctx, RequestPageQuery{
		Admin: true, Limit: 100, BeforeSubmittedAt: second.NextBeforeSubmittedAt, BeforeID: second.NextBeforeID,
		Statuses: []domain.RequestStatus{domain.StatusPendingReview},
	})
	if err != nil || len(third.Items) != 5 || third.HasMore {
		t.Fatalf("third page items=%d more=%v err=%v", len(third.Items), third.HasMore, err)
	}
	profiles, err := store.ListProfileRecords(ctx, "20000000-0000-4000-8000-000000000001")
	if err != nil || len(profiles) != 500 {
		t.Fatalf("profile cap=%d err=%v", len(profiles), err)
	}
	lots, err := store.ListFundingLots(ctx, "20000000-0000-4000-8000-000000000001")
	if err != nil || len(lots) != 500 {
		t.Fatalf("funding lot cap=%d err=%v", len(lots), err)
	}
}

func TestRefundInvalidatesUnissuedRequestAndReleasesAllReservations(t *testing.T) {
	store, ctx := integrationStore(t)
	request, err := store.Submit(ctx, SubmitInput{
		PrincipalID:               "20000000-0000-4000-8000-000000000001",
		ProfileID:                 "40000000-0000-4000-8000-000000000001",
		SourceInstanceID:          "10000000-0000-4000-8000-000000000001",
		ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		IdempotencyKey:            "refund-before-issue",
		Allocations:               []AllocationInput{{FundingLotID: "50000000-0000-4000-8000-000000000001", AmountMinor: 20_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lot, err := store.GetFundingLot(ctx, "50000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	lot.CurrentCapMinor = 10_000
	lot.SourceStatus = "REFUNDED"
	lot.SourceRevision = "refund-r2"
	lot.ObservedAt = time.Now().UTC().Add(time.Minute)
	result, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: lot, ExternalUserID: "u1", EventKind: "refund",
		ExternalEventID: "order-1-refund", SchemaVersion: "test-v1",
	}, AuditActor{Type: "source_connector", ID: "connector-test", Reason: "refund observed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.InvalidatedRequestIDs) != 1 || result.InvalidatedRequestIDs[0] != request.ID || len(result.AttentionRequestIDs) != 0 {
		t.Fatalf("refund result=%+v", result)
	}
	var reserved, issued int64
	if err = store.pool.QueryRow(ctx, `SELECT reserved_minor,issued_minor FROM funding_lots WHERE id=$1`, lot.ID).Scan(&reserved, &issued); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || issued != 0 {
		t.Fatalf("reserved=%d issued=%d", reserved, issued)
	}
	stored, err := store.GetRequestRecord(ctx, request.PrincipalID, request.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Request.Status != domain.StatusRejected || stored.Request.Version != request.Version+1 {
		t.Fatalf("request=%+v", stored.Request)
	}
}

func TestSourceBatchReceiverSequenceReplayAndAtomicEvents(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := "10000000-0000-4000-8000-000000000001"
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET runtime_version='test-runtime' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	actor := AuditActor{Type: "source_connector", ID: sourceID, Reason: "verified mTLS batch"}
	if err := store.ProvisionSourceStream(ctx, sourceID, "payments", actor); err != nil {
		t.Fatal(err)
	}
	first := SourceBatchInput{
		SourceInstanceID: sourceID, StreamID: "payments",
		BatchID: "70000000-0000-4000-8000-000000000001", Sequence: 1,
		BodyHash: strings.Repeat("a", 64), SigningKeyID: "key-1", Actor: actor,
		SourceRuntimeVersion: "test-runtime", SourceAgentVersion: "test-agent",
		SourceCapturedAt: time.Now().UTC(), ProjectionStatus: "healthy",
		Events: []SourceBatchEvent{{
			EventID: "71000000-0000-4000-8000-000000000001", EntityType: "payment_order",
			PayloadHash: strings.Repeat("b", 64), PayloadCiphertext: bytes.Repeat([]byte{0x11}, 32),
			ObservedAt: time.Now().UTC(),
		}},
	}
	result, err := store.CommitSourceBatch(ctx, first)
	if err != nil || result.Duplicate || result.AcceptedRecords != 1 {
		t.Fatalf("first result=%+v err=%v", result, err)
	}
	mismatchedRuntime := first
	mismatchedRuntime.BatchID = "70000000-0000-4000-8000-000000000099"
	mismatchedRuntime.SourceRuntimeVersion = "unapproved-runtime"
	if _, err = store.CommitSourceBatch(ctx, mismatchedRuntime); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("unapproved runtime version error=%v", err)
	}
	duplicate, err := store.CommitSourceBatch(ctx, first)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate result=%+v err=%v", duplicate, err)
	}
	conflictingBatch := first
	conflictingBatch.BodyHash = strings.Repeat("c", 64)
	if _, err = store.CommitSourceBatch(ctx, conflictingBatch); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same batch id with different body error=%v", err)
	}
	second := SourceBatchInput{
		SourceInstanceID: sourceID, StreamID: "payments",
		BatchID: "70000000-0000-4000-8000-000000000002", Sequence: 2,
		BodyHash: strings.Repeat("d", 64), PreviousBatchHash: first.BodyHash,
		SigningKeyID: "key-2", Actor: actor, SourceRuntimeVersion: "test-runtime",
		SourceAgentVersion: "test-agent", SourceCapturedAt: time.Now().UTC(), ProjectionStatus: "healthy",
		Events: []SourceBatchEvent{{
			EventID: first.Events[0].EventID, EntityType: "payment_order",
			PayloadHash: strings.Repeat("e", 64), PayloadCiphertext: bytes.Repeat([]byte{0x22}, 32),
			ObservedAt: time.Now().UTC(),
		}},
	}
	if _, err = store.CommitSourceBatch(ctx, second); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("event id payload substitution error=%v", err)
	}
	var sequence int64
	if err = store.pool.QueryRow(ctx, `SELECT sequence FROM source_ingest_state WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID).Scan(&sequence); err != nil || sequence != 1 {
		t.Fatalf("sequence advanced on rolled-back conflict: sequence=%d err=%v", sequence, err)
	}
	second.Events[0].PayloadHash = first.Events[0].PayloadHash
	second.Events[0].PayloadCiphertext = first.Events[0].PayloadCiphertext
	if _, err = store.CommitSourceBatch(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT sequence FROM source_ingest_state WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatalf("sequence=%d err=%v", sequence, err)
	}
	var eventCount int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("event count=%d err=%v", eventCount, err)
	}
	claims, err := store.ClaimUnprocessedSourceEvents(ctx, 10, time.Now().UTC().Add(time.Minute))
	if err != nil || len(claims) != 1 {
		t.Fatalf("source event claims=%+v err=%v", claims, err)
	}
	if err = store.MarkSourceEventProcessed(ctx, claims[0], time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	health, err := store.SourceIngestHealth(ctx)
	if err != nil || health.Pending != 0 || health.Dead != 0 {
		t.Fatalf("source ingest health=%+v err=%v", health, err)
	}
	var agentTablesAbsent bool
	if err = store.pool.QueryRow(ctx, `SELECT to_regclass('source_agent_cursors') IS NULL AND to_regclass('source_agent_publish_state') IS NULL`).Scan(&agentTablesAbsent); err != nil || !agentTablesAbsent {
		t.Fatalf("invoice database unexpectedly contains agent-side state tables: absent=%v err=%v", agentTablesAbsent, err)
	}
}

func TestSourceFreshnessFailsClosedForSubmitAndFinalIssue(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := "10000000-0000-4000-8000-000000000001"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET runtime_version='approved-v1' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "admin", ID: "freshness-test"}); err != nil {
			t.Fatal(err)
		}
	}
	policy := SourceFreshnessPolicy{EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute, IdentitiesMaxAge: 15 * time.Minute, Now: now}
	input := SubmitInput{PrincipalID: "20000000-0000-4000-8000-000000000001",
		ProfileID: "40000000-0000-4000-8000-000000000001", SourceInstanceID: sourceID,
		ProfileSnapshotCiphertext: []byte("encrypted-snapshot"), IdempotencyKey: "freshness-submit",
		Allocations: []AllocationInput{{FundingLotID: "50000000-0000-4000-8000-000000000001", AmountMinor: 20_000}}, Freshness: policy}
	if _, err := store.Submit(ctx, input); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("cold source submit error=%v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE source_ingest_state SET sequence=1,last_batch_hash=repeat('a',64),
			source_runtime_version='approved-v1',source_agent_version='agent-v1',
			projection_status='healthy',last_accepted_at=$2,updated_at=$2
		WHERE source_instance_id=$1`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	request, err := store.Submit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE invoice_requests SET status='approved' WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET last_accepted_at=$3 WHERE source_instance_id=$1 AND stream_id=$2`,
		sourceID, "payments", now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{AdminID: "90000000-0000-4000-8000-000000000001",
		RequestID: request.ID, ExpectedVersion: request.Version, IssuerSettingRevision: 1,
		IssueSnapshotCiphertext: []byte("encrypted-issuer-snapshot"), Freshness: policy}); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("stale final issue error=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET last_accepted_at=$2 WHERE source_instance_id=$1`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{AdminID: "90000000-0000-4000-8000-000000000001",
		RequestID: request.ID, ExpectedVersion: request.Version, IssuerSettingRevision: 1,
		IssueSnapshotCiphertext: []byte("encrypted-issuer-snapshot"), Freshness: policy}); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "blocked-projection-submit"
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET projection_status='blocked' WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Submit(ctx, input); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("blocked projection submit error=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET projection_status='healthy',source_runtime_version='unapproved-v2' WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "runtime-mismatch-submit"
	if _, err = store.Submit(ctx, input); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("runtime mismatch submit error=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET source_runtime_version='approved-v1' WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,
			signing_key_id,record_count,source_runtime_version,source_agent_version,source_captured_at,projection_status)
		VALUES($1,'payments','74000000-0000-4000-8000-000000000001',1,repeat('a',64),'key',1,'approved-v1','agent-v1',$2,'healthy')`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at)
		VALUES($1,'payments','74100000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000001',
			'payment_order','upsert',repeat('b',64),decode(repeat('11',16),'hex'),$2)`, sourceID, now); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "pending-event-submit"
	if _, err = store.Submit(ctx, input); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("pending source event submit error=%v", err)
	}
}

func TestSourceRuntimeApprovalRequiresExpectedPreviousVersion(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := "10000000-0000-4000-8000-000000000088"
	actor := AuditActor{Type: "system", ID: "deployment-bootstrap", RequestID: "runtime-approval-test", Reason: "test controlled source upgrade"}
	created, err := store.ApproveSourceInstance(ctx, SourceInstanceRecord{ID: sourceID,
		SourceType: domain.SourceSub2API, Name: "controlled-source", RuntimeVersion: "0.1.178", Enabled: true}, nil, actor)
	if err != nil || created.RuntimeVersionRevision != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = store.ApproveSourceInstance(ctx, SourceInstanceRecord{ID: sourceID,
		SourceType: domain.SourceSub2API, Name: "controlled-source", RuntimeVersion: "0.1.179", Enabled: true}, nil, actor); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("unconditional runtime upgrade error=%v", err)
	}
	wrong := "0.1.177"
	if _, err = store.ApproveSourceInstance(ctx, SourceInstanceRecord{ID: sourceID,
		SourceType: domain.SourceSub2API, Name: "controlled-source", RuntimeVersion: "0.1.179", Enabled: true}, &wrong, actor); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("wrong previous runtime error=%v", err)
	}
	previous := "0.1.178"
	updated, err := store.ApproveSourceInstance(ctx, SourceInstanceRecord{ID: sourceID,
		SourceType: domain.SourceSub2API, Name: "controlled-source", RuntimeVersion: "0.1.179", Enabled: true}, &previous, actor)
	if err != nil || updated.RuntimeVersionRevision != 2 || updated.RuntimeVersion != "0.1.179" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}
