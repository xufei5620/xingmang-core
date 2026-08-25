package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
)

type mutableSettings struct {
	mu    sync.RWMutex
	value adminsettings.Settings
}

func TestBaselineMemberReconciliationWaitsWithoutConsumingRetryBudget(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)
	sourceID := "10000000-0000-4000-8000-000000000078"
	userID := "20000000-0000-4000-8000-000000000078"
	accountID := "30000000-0000-4000-8000-000000000078"
	if _, err := store.Pool().Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','baseline-wait','wait-v3')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'https://id.example','baseline-wait')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'78',$4,'test','verified')`, accountID, userID, sourceID,
		"h1:"+strings.Repeat("8", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances",
		postgresstore.AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	manifestHash := strings.Repeat("a", 64)
	configHash := strings.Repeat("b", 64)
	snapshotHash := strings.Repeat("c", 64)
	if _, err := store.Pool().Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'wait-v3','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'wait-key')`, sourceID, manifestHash,
		now.Add(-25*time.Hour), configHash, snapshotHash); err != nil {
		t.Fatal(err)
	}
	member := true
	payload, err := json.Marshal(balanceCheckpointPayload{
		ExternalUserID: "78", CheckpointID: snapshotHash + ":78", CheckpointKind: "reconciliation",
		AsOf: now.Format(time.RFC3339Nano), BalanceServiceUnits: "100", UnitCode: "SUB2_BALANCE_1E8",
		SourceSnapshotID: snapshotHash, SnapshotRowCount: "1", BalanceNegative: false,
		BaselineMember: &member, SourceCursor: "balance:78", CutoverManifestHash: manifestHash,
		ConfigurationHash: configHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	payloadSum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(payloadSum[:])
	eventID := "78000000-0000-4000-8000-000000000001"
	batchID := "78000000-0000-4000-8000-000000000002"
	cycleID := "78000000-0000-4000-8000-000000000003"
	ciphertext, err := service.keys.Encrypt(payload, ingestEventAAD(sourceID, "balances", eventID, payloadHash))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "balances",
		BatchID: batchID, Sequence: 1, BodyHash: strings.Repeat("d", 64), SigningKeyID: "wait-key",
		SourceRuntimeVersion: "wait-v3", SourceAgentVersion: "wait-agent", SourceCapturedAt: now,
		ProjectionStatus: "healthy", StreamWatermarkAt: now, SourceCursor: "balance:78",
		ScanCeilingAt: now, ScanCeilingCursor: "balance-ceiling:78", ScanCycleID: cycleID,
		ScanComplete: true, ScanSnapshotID: snapshotHash, ScanSnapshotRowCount: 1,
		Events: []postgresstore.SourceBatchEvent{{EventID: eventID, EntityType: "balance_checkpoint",
			Operation: "upsert", PayloadHash: payloadHash, PayloadCiphertext: ciphertext, ObservedAt: now}},
		Actor: postgresstore.AuditActor{Type: "source_connector", ID: sourceID},
	}); err != nil {
		t.Fatal(err)
	}
	processor := SourceEventProcessor{Service: service, BatchSize: 10, Now: func() time.Time { return now.Add(time.Minute) }}
	processed, err := processor.RunOnce(ctx)
	if err != nil || processed != 1 {
		t.Fatalf("baseline member processor processed=%d err=%v", processed, err)
	}
	var status, dependencyKind, cycleStatus string
	var attempts int
	if err = store.Pool().QueryRow(ctx, `SELECT processing_status,dependency_kind,attempt_count
		FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2`,
		sourceID, eventID).Scan(&status, &dependencyKind, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "waiting_dependency" || dependencyKind != "source_eligibility_cutover" || attempts != 0 {
		t.Fatalf("baseline member status=%s dependency=%s attempts=%d", status, dependencyKind, attempts)
	}
	if err = store.Pool().QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`,
		sourceID, cycleID).Scan(&cycleStatus); err != nil || cycleStatus == "published" {
		t.Fatalf("waiting baseline member cycle status=%s err=%v", cycleStatus, err)
	}
	if processed, err = processor.RunOnce(ctx); err != nil || processed != 0 {
		t.Fatalf("waiting dependency consumed retry budget: processed=%d err=%v", processed, err)
	}
	var states int
	if err = store.Pool().QueryRow(ctx, `SELECT count(*) FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&states); err != nil || states != 0 {
		t.Fatalf("baseline member created conservative state count=%d err=%v", states, err)
	}
}

func waitForTestPool(t *testing.T, pool *pgxpool.Pool) {
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
	t.Fatalf("PostgreSQL test pool did not become reachable: %v", err)
}

func (s *mutableSettings) Get(context.Context) (adminsettings.Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value, nil
}

func (s *mutableSettings) set(value adminsettings.Settings) {
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
}

func integrationApplication(t *testing.T) (*Service, *postgresstore.Store, *mutableSettings, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	waitForTestPool(t, adminPool)
	t.Cleanup(adminPool.Close)
	schemaID, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "invoice_app_test_" + strings.ReplaceAll(schemaID, "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	waitForTestPool(t, pool)
	t.Cleanup(pool.Close)
	if err = migrate.Up(ctx, pool, privateSchemaMigrationsDir(t, schema)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin_settings(
		singleton_id,issuer_name,service_item,minimum_request_minor,smtp_host,smtp_port,
		smtp_from,smtp_from_name,smtp_starttls,admin_cidrs,revision,updated_by)
		VALUES(1,'测试开票主体','技术服务',20000,'smtp.qq.com',587,'invoice@qq.com',
		'发票中心',TRUE,ARRAY['127.0.0.1/32']::inet[],7,'application-integration-test')`); err != nil {
		t.Fatal(err)
	}
	testPolicyStart := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	// This disposable schema moves the boundary only to keep integration facts
	// near the test clock. Production policy updates are permanently rejected.
	if _, err = pool.Exec(ctx, `ALTER TABLE invoice_eligibility_policy
		DISABLE TRIGGER invoice_eligibility_policy_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE invoice_eligibility_policy
		SET eligibility_start_at=$1,policy_version=policy_version+1,
			updated_by='application-integration-test' WHERE singleton_id=1`,
		testPolicyStart); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE invoice_eligibility_policy
		ENABLE TRIGGER invoice_eligibility_policy_guard`); err != nil {
		t.Fatal(err)
	}
	settings := &mutableSettings{value: adminsettings.Settings{
		IssuerName: "测试开票主体", ServiceItem: domain.FixedServiceItem,
		MinimumRequestMinor: domain.MinimumRequestMinor, EligibilityStartAt: testPolicyStart,
		EligibilityPolicyVersion: 2, Revision: 7,
	}}
	store := postgresstore.New(pool)
	service, err := NewService(store, testKeys(), settings, Options{
		MinimumRequestMinor: domain.MinimumRequestMinor,
		DownloadBaseURL:     "https://invoice.example/",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, settings, ctx
}

func privateSchemaMigrationsDir(t *testing.T, schema string) string {
	t.Helper()
	// 0013 is deliberately bound to the production public schema. This private
	// application fixture does not exercise that separately tested contract.
	source := filepath.Join("..", "..", "migrations")
	target := t.TempDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") ||
			entry.Name() == "0013_source_readiness_active_index.sql" {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if entry.Name() == "0014_balance_carry_forward_proof.sql" {
			// Production 0014 is deliberately public-bound. Rebind only this
			// disposable private-schema copy while preserving its fixed search_path.
			rebound := strings.ReplaceAll(string(body), "public.", "")
			rebound = strings.ReplaceAll(rebound, "SET search_path=pg_catalog,public",
				"SET search_path=pg_catalog,"+pgx.Identifier{schema}.Sanitize())
			body = []byte(rebound)
		}
		if writeErr := os.WriteFile(filepath.Join(target, entry.Name()), body, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	return target
}

func TestPersistentApplicationEndToEndRefundAndOutbox(t *testing.T) {
	service, store, settings, ctx := integrationApplication(t)
	user, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://id.example", Subject: "subject-1",
		Email: "billing@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentUser, err := service.GetCurrentUser(ctx, user.ID)
	if err != nil || currentUser.ID != user.ID || currentUser.Email != "billing@example.com" || !currentUser.EmailVerified {
		t.Fatalf("current user=%+v err=%v", currentUser, err)
	}
	if err = service.RecordAdminAudit(ctx, "90000000-0000-4000-8000-000000000001", "smtp.test", "smtp", "smtp-test-request-1", "success"); err != nil {
		t.Fatal(err)
	}
	sourceID := "10000000-0000-4000-8000-000000000001"
	if _, err = store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: sourceID, SourceType: domain.SourceSub2API, Name: "Sub2API", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindExternalAccount(ctx, postgresstore.ExternalAccountRecord{
		ID: "30000000-0000-4000-8000-000000000001", PrincipalID: user.ID,
		SourceInstanceID: sourceID, ExternalUserID: "external-user-1",
		BindingMethod: "oidc_subject", BindingStatus: "verified",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	lot := domain.FundingLot{
		ID: "50000000-0000-4000-8000-000000000001", PrincipalID: user.ID,
		SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
		ExternalOrderID: "order-1", TradeNo: "trade-1", Currency: domain.CurrencyCNY,
		OriginalMinor: 100_000, CurrentCapMinor: 100_000,
		Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
		SourceRevision: "revision-1", CompletedAt: now.Add(-time.Hour), ObservedAt: now,
	}
	// Deterministic fixture bootstrap explicitly marks the lot consumed. A
	// legacy/V2 source observation is intentionally non-invoiceable now.
	if err = store.UpsertFundingLot(ctx, lot); err != nil {
		t.Fatal(err)
	}
	connected, err := service.ListExternalAccounts(ctx, user.ID)
	if err != nil || len(connected) != 1 || connected[0].BindingStatus != "verified" ||
		connected[0].MaskedExternalUserID == "external-user-1" || connected[0].LastObservedAt.IsZero() {
		t.Fatalf("connected source accounts=%+v err=%v", connected, err)
	}

	// The browser boolean is ignored: an address absent from verified_emails is
	// rejected even when the attacker submits email_verified=true.
	_, err = service.SaveProfile(ctx, domain.InvoiceProfile{
		PrincipalID: user.ID, Type: domain.ProfilePersonal, Title: "攻击者",
		Email: "attacker@example.com", EmailVerified: true,
	})
	if !errors.Is(err, ErrOIDCEmailUnverified) {
		t.Fatalf("forged email_verified=true error=%v", err)
	}
	profiles, err := service.ListProfiles(ctx, user.ID)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("unverified profile persisted: profiles=%d err=%v", len(profiles), err)
	}
	profile, err := service.SaveProfile(ctx, domain.InvoiceProfile{
		PrincipalID: user.ID, Type: domain.ProfileEnterprise, Title: "示例公司",
		TaxID: "91300000000000000X", Email: "billing@example.com",
		EmailVerified: false, IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !profile.EmailVerified {
		t.Fatal("repository-verified email was not reflected in output")
	}
	request, err := service.Submit(ctx, ledger.SubmitInput{
		PrincipalID: user.ID, ProfileID: profile.ID, SourceInstanceID: sourceID,
		IdempotencyKey: "submit-1", Allocations: []ledger.AllocationInput{{
			FundingLotID: lot.ID, AmountMinor: domain.MinimumRequestMinor,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminID := "90000000-0000-4000-8000-000000000001"
	request, err = service.Review(ctx, adminID, request.ID, "approve", "", request.Version)
	if err != nil {
		t.Fatal(err)
	}
	request, err = service.ConfirmManualIssue(ctx, adminID, request.ID, request.Version)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.StatusIssuedAwaitingDocument {
		t.Fatalf("status=%s", request.Status)
	}
	immutable, err := service.GetIssueSnapshot(ctx, adminID, request.ID)
	currentSettings, settingsErr := settings.Get(ctx)
	if err != nil || settingsErr != nil || immutable.IssuerName != "测试开票主体" ||
		immutable.SettingsRevision != 7 ||
		!immutable.EligibilityStartAt.Equal(currentSettings.EligibilityStartAt) ||
		immutable.EligibilityPolicyVersion != currentSettings.EligibilityPolicyVersion {
		t.Fatalf("issue snapshot=%+v err=%v", immutable, err)
	}
	settings.set(adminsettings.Settings{
		IssuerName: "新开票主体", ServiceItem: domain.FixedServiceItem,
		MinimumRequestMinor:      domain.MinimumRequestMinor,
		EligibilityStartAt:       currentSettings.EligibilityStartAt,
		EligibilityPolicyVersion: currentSettings.EligibilityPolicyVersion, Revision: 8,
	})
	stillImmutable, err := service.GetIssueSnapshot(ctx, adminID, request.ID)
	if err != nil || stillImmutable != immutable {
		t.Fatalf("historical issuer changed: before=%+v after=%+v err=%v", immutable, stillImmutable, err)
	}
	doc := domain.InvoiceDocument{
		RequestID: request.ID, InvoiceNumber: "FP-2026-0001",
		ObjectKey: "issued/object.pdf", ObjectVersion: "v1",
		SHA256: strings.Repeat("a", 64), SizeBytes: 1234,
		MIME: "application/pdf", ScanStatus: "clean", IssuedAt: time.Now().UTC(),
	}
	futureDoc := doc
	futureDoc.IssuedAt = time.Now().UTC().Add(10 * time.Minute)
	if _, _, _, futureErr := service.AttachDocument(ctx, adminID, futureDoc, request.Version); futureErr == nil {
		t.Fatal("future invoice issued_at was accepted")
	}
	request, savedDoc, queued, err := service.AttachDocument(ctx, adminID, doc, request.Version)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != domain.StatusIssued || queued.Status != "queued" {
		t.Fatalf("request=%s outbox=%s", request.Status, queued.Status)
	}
	// Exact retry with the stale pre-attach version is idempotent.
	_, retryDoc, retryOutbox, err := service.AttachDocument(ctx, adminID, doc, request.Version-1)
	if err != nil || retryDoc.ID != savedDoc.ID || retryOutbox.ID != queued.ID {
		t.Fatalf("attach retry doc=%s outbox=%s err=%v", retryDoc.ID, retryOutbox.ID, err)
	}
	adminDoc, err := service.GetDocumentForRequestAsAdmin(ctx, request.ID)
	if err != nil || adminDoc.ID != savedDoc.ID {
		t.Fatalf("admin document lookup doc=%s err=%v", adminDoc.ID, err)
	}
	messages, err := service.Claim(ctx, 10, now.Add(time.Minute))
	if err != nil || len(messages) != 1 {
		t.Fatalf("claimed=%d err=%v", len(messages), err)
	}
	if messages[0].Recipient != "billing@example.com" || !strings.Contains(messages[0].DownloadURL, request.ID) {
		t.Fatalf("message=%+v", messages[0])
	}
	link, parseErr := url.Parse(messages[0].DownloadURL)
	if parseErr != nil || link.Path != "/records" || link.Query().Get("request_id") != request.ID || len(link.Query()) != 1 {
		t.Fatalf("unsafe invoice UI link=%q err=%v", messages[0].DownloadURL, parseErr)
	}
	if err = messages[0].Validate(); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := service.Claim(ctx, 10, now.Add(12*time.Minute))
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID == messages[0].ID {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	if err = service.MarkSent(ctx, messages[0].ID, "stale-provider", now.Add(13*time.Minute)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale lease acknowledgement error=%v", err)
	}
	if err = service.MarkSent(ctx, reclaimed[0].ID, "provider-1", now.Add(13*time.Minute)); err != nil {
		t.Fatal(err)
	}
	delivery, err := service.GetInvoiceDeliveryStatus(ctx, user.ID, request.ID, false)
	if err != nil || delivery.DocumentID != savedDoc.ID || delivery.EmailStatus != "sent" {
		t.Fatalf("delivery state=%+v err=%v", delivery, err)
	}
	requeued, err := service.RequeueEmail(ctx, request.ID, adminID, "用户确认未收到，管理员重发")
	if err != nil || requeued.Status != "queued" || requeued.Attempts != 0 {
		t.Fatalf("requeued=%+v err=%v", requeued, err)
	}
	if _, err = service.RequeueEmail(ctx, request.ID, adminID, "重复点击"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("concurrent/duplicate requeue error=%v", err)
	}

	// Refund below already-issued value freezes the lot and marks the request.
	refundLot := lot
	refundLot.CurrentCapMinor = 10_000
	refundLot.SourceStatus = "REFUNDED"
	refundLot.SourceRevision = "revision-2"
	refundLot.ObservedAt = now.Add(3 * time.Minute)
	result, err := service.ObserveFundingLot(ctx, FundingObservation{
		Lot: refundLot, ExternalUserID: "external-user-1", EventKind: "refund",
		ExternalEventID: "order-1-refund", SchemaVersion: "sub2api-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Lot.Verification != domain.VerificationFrozen || len(result.AttentionRequestIDs) != 1 {
		t.Fatalf("refund result=%+v", result)
	}
	requests, err := service.ListRequests(ctx, user.ID, false)
	if err != nil || len(requests) != 1 || requests[0].Status != domain.StatusRefundAttention {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	refundCases, err := service.ListRefundCasesPage(ctx, postgresstore.RefundCasePageQuery{Limit: 100})
	if err != nil || len(refundCases.Items) != 1 || refundCases.Items[0].Status != "open" {
		t.Fatalf("refund cases=%+v err=%v", refundCases, err)
	}
	resolved, err := service.ResolveRefundCase(ctx, adminID, refundCases.Items[0].ID,
		"resolved_no_action", "manual-refund-review-1", "人工核对后确认本单无需红冲")
	if err != nil || resolved.Status != "resolved_no_action" {
		t.Fatalf("resolved case=%+v err=%v", resolved, err)
	}
	resolution, err := service.GetRefundCaseResolution(ctx, resolved.ID)
	if err != nil || resolution.EvidenceReference != "manual-refund-review-1" || resolution.Note != "人工核对后确认本单无需红冲" {
		t.Fatalf("refund resolution=%+v err=%v", resolution, err)
	}
	requests, err = service.ListRequests(ctx, user.ID, false)
	if err != nil || requests[0].Status != domain.StatusIssued {
		t.Fatalf("resolved request=%+v err=%v", requests, err)
	}
	issuedLot, err := store.GetFundingLot(ctx, lot.ID)
	if err != nil || issuedLot.IssuedMinor != domain.MinimumRequestMinor {
		t.Fatalf("refund resolution changed issued amount: lot=%+v err=%v", issuedLot, err)
	}
	if _, err = service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://id.example", Subject: "subject-1",
		Email: "rotated@example.com", EmailVerified: true, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RequeueEmail(ctx, request.ID, adminID, "旧快照邮箱已撤销"); !errors.Is(err, ErrOIDCEmailUnverified) {
		t.Fatalf("requeue to revoked snapshot email error=%v", err)
	}
	var auditCount int
	if err = store.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&auditCount); err != nil || auditCount < 8 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
}

func TestOIDCEmailRotationRevokesOnlyOIDCSource(t *testing.T) {
	service, _, _, ctx := integrationApplication(t)
	user, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://id.example", Subject: "email-rotation-user",
		Email: "old@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.RegisterChallengeVerifiedEmail(ctx, user.ID, "challenge@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://id.example", Subject: "email-rotation-user",
		Email: "new@example.com", EmailVerified: true, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	oldVerified, err := service.IsEmailVerified(ctx, user.ID, "old@example.com")
	if err != nil {
		t.Fatal(err)
	}
	newVerified, _ := service.IsEmailVerified(ctx, user.ID, "new@example.com")
	challengeVerified, _ := service.IsEmailVerified(ctx, user.ID, "challenge@example.com")
	if oldVerified || !newVerified || !challengeVerified {
		t.Fatalf("after rotation old=%v new=%v challenge=%v", oldVerified, newVerified, challengeVerified)
	}
	if _, err = service.SaveProfile(ctx, domain.InvoiceProfile{
		PrincipalID: user.ID, Type: domain.ProfilePersonal, Title: "旧邮箱",
		Email: "old@example.com", EmailVerified: true,
	}); !errors.Is(err, ErrOIDCEmailUnverified) {
		t.Fatalf("profile with revoked OIDC email error=%v", err)
	}
	if _, err = service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://id.example", Subject: "email-rotation-user",
		EmailVerified: false, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	newVerified, _ = service.IsEmailVerified(ctx, user.ID, "new@example.com")
	challengeVerified, _ = service.IsEmailVerified(ctx, user.ID, "challenge@example.com")
	if newVerified || !challengeVerified {
		t.Fatalf("after unverified login new=%v challenge=%v", newVerified, challengeVerified)
	}
}

func TestOutboxLeaseRejectsStaleAcknowledgement(t *testing.T) {
	service, _, _, ctx := integrationApplication(t)
	// This behavior is covered end-to-end above; assert malformed claim IDs are
	// rejected without needing to seed a second workflow.
	if err := service.MarkSent(ctx, "not-a-lease", "provider", time.Now()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale/malformed claim error=%v", err)
	}
	if got := fmt.Sprint(ErrEvidenceRequired); got == "" {
		t.Fatal("evidence error must remain explicit")
	}
}

func TestVerifiedSourceBatchEncryptsPayloadAndBindsMTLSIdentity(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)
	sourceID := "10000000-0000-4000-8000-000000000009"
	if _, err := store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: sourceID, SourceType: domain.SourceSub2API, Name: "batch-source", RuntimeVersion: "test-runtime", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "payments", postgresstore.AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	rawPayload := []byte(`{"external_order_id":"order-1"}`)
	batch := VerifiedSourceBatch{
		SourceInstanceID: sourceID, StreamID: "payments",
		BatchID: "70000000-0000-4000-8000-000000000009", Sequence: 1,
		BodyHash: strings.Repeat("a", 64), SigningKeyID: "ed25519-key-1",
		SourceRuntimeVersion: "test-runtime", SourceAgentVersion: "test-agent", SourceCapturedAt: time.Now().UTC(), ProjectionStatus: "healthy",
		Events: []VerifiedSourceBatchEvent{{
			EventID: "71000000-0000-4000-8000-000000000009", EntityType: "payment_order",
			PayloadHash: strings.Repeat("b", 64), Payload: rawPayload, ObservedAt: time.Now().UTC(),
		}},
	}
	if _, err := service.CommitVerifiedSourceBatch(ctx, "10000000-0000-4000-8000-000000000008", batch); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("mTLS/body source mismatch error=%v", err)
	}
	result, err := service.CommitVerifiedSourceBatch(ctx, sourceID, batch)
	if err != nil || result.Duplicate || result.AcceptedRecords != 1 {
		t.Fatalf("batch result=%+v err=%v", result, err)
	}
	duplicate, err := service.CommitVerifiedSourceBatch(ctx, sourceID, batch)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate batch=%+v err=%v", duplicate, err)
	}
	var ciphertext []byte
	if err = store.Pool().QueryRow(ctx, `SELECT payload_ciphertext FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ciphertext, rawPayload) || bytes.Contains(ciphertext, rawPayload) {
		t.Fatal("source event payload was stored in plaintext")
	}
}

func TestSourceBatchProcessorProjectsBindingsCandidatesLotsAndRefunds(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)
	user, err := service.EnsureUser(ctx, OIDCIdentity{
		Issuer: "https://central-id.example", Subject: "central-subject",
		Email: "sync-user@example.com", EmailVerified: true, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	sub2ID := "10000000-0000-4000-8000-000000000021"
	newAPIID := "10000000-0000-4000-8000-000000000022"
	for _, source := range []postgresstore.SourceInstanceRecord{
		{ID: sub2ID, SourceType: domain.SourceSub2API, Name: "sub2-sync", RuntimeVersion: "test-runtime", Enabled: true},
		{ID: newAPIID, SourceType: domain.SourceNewAPI, Name: "newapi-sync", RuntimeVersion: "test-runtime", Enabled: true},
	} {
		if _, err = store.UpsertSourceInstance(ctx, source); err != nil {
			t.Fatal(err)
		}
		for _, stream := range []string{"identities", "payments"} {
			if err = store.ProvisionSourceStream(ctx, source.ID, stream, postgresstore.AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000001"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	commit := func(sourceID, stream, batchID, bodyHash, previous string, sequence int64, eventID, entity string, payload []byte, payloadHash string) {
		t.Helper()
		_, commitErr := service.CommitVerifiedSourceBatch(ctx, sourceID, VerifiedSourceBatch{
			SourceInstanceID: sourceID, StreamID: stream, BatchID: batchID,
			Sequence: sequence, BodyHash: bodyHash, PreviousBatchHash: previous,
			SigningKeyID: "source-key-1", SourceRuntimeVersion: "test-runtime",
			SourceAgentVersion: "test-agent", SourceCapturedAt: now, ProjectionStatus: "healthy", Events: []VerifiedSourceBatchEvent{{
				EventID: eventID, EntityType: entity, Operation: "upsert",
				PayloadHash: payloadHash, Payload: payload, ObservedAt: now,
			}},
		})
		if commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	identityPayload := func(external string) []byte {
		return []byte(fmt.Sprintf(`{"external_user_id":%q,"provider_type":"oidc","provider_key":"central","provider_subject":"central-subject","issuer":"https://central-id.example","verified_at":%q,"user_status":"unknown","updated_at":%q}`,
			external, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	}
	commit(sub2ID, "identities", "72000000-0000-4000-8000-000000000001", strings.Repeat("1", 64), "", 1,
		"72100000-0000-4000-8000-000000000001", "identity_binding", identityPayload("3"), strings.Repeat("2", 64))
	commit(newAPIID, "identities", "72000000-0000-4000-8000-000000000002", strings.Repeat("3", 64), "", 1,
		"72100000-0000-4000-8000-000000000002", "identity_binding", identityPayload("7"), strings.Repeat("4", 64))
	// Sub2API amount is credited balance (including a recharge multiplier),
	// while pay_amount is the actual invoiceable gateway money.
	paymentBody := []byte(fmt.Sprintf(`{"external_order_id":"501","external_user_id":"3","status":"COMPLETED","order_type":"balance","amount":"600.00","pay_amount":"500.00","currency":"CNY","refund_amount":"0","gateway_refund_amount":"0","completed_at":%q,"created_at":%q,"updated_at":%q,"payment_type":"stripe","provider_key":"stripe","external_trade_ref_hmac":""}`,
		now.Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	commit(sub2ID, "payments", "72000000-0000-4000-8000-000000000003", strings.Repeat("5", 64), "", 1,
		"72100000-0000-4000-8000-000000000003", "payment_order", paymentBody, strings.Repeat("6", 64))
	candidateBody := []byte(fmt.Sprintf(`{"external_order_id":"701","external_user_id":"7","source_status":"success","order_type":"topup","quoted_amount":"300","observed_pay_amount":"300.00","verification_state":"pending_manual","verification_reason":"manual settlement evidence required","completed_at":%q,"created_at":%q,"observed_at":%q,"payment_type":"stripe","provider_key":"stripe"}`,
		now.Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	commit(newAPIID, "payments", "72000000-0000-4000-8000-000000000004", strings.Repeat("7", 64), "", 1,
		"72100000-0000-4000-8000-000000000004", "payment_candidate", candidateBody, strings.Repeat("8", 64))

	clock := now.Add(time.Minute)
	processor := SourceEventProcessor{Service: service, BatchSize: 100, Now: func() time.Time { return clock }}
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(6 * time.Minute)
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	connected, err := service.ListExternalAccounts(ctx, user.ID)
	if err != nil || len(connected) != 2 {
		t.Fatalf("projected bindings=%+v err=%v", connected, err)
	}
	lots, err := service.ListFundingLots(ctx, user.ID)
	if err != nil || len(lots) != 2 {
		t.Fatalf("projected lots=%+v err=%v", lots, err)
	}
	var sub2Lot domain.FundingLot
	for _, lot := range lots {
		if lot.SourceType == domain.SourceSub2API {
			sub2Lot = lot
		}
	}
	if sub2Lot.Verification != domain.VerificationVerified || sub2Lot.CurrentCapMinor != 50_000 {
		t.Fatalf("Sub2API projected lot=%+v", sub2Lot)
	}
	// V2 projection proves payment but not post-cutover consumption. This
	// workflow test seeds the consumed-cash fixture explicitly before testing
	// invoice/refund transitions.
	if err = store.UpsertFundingLot(ctx, sub2Lot); err != nil {
		t.Fatal(err)
	}
	sub2Lot, err = store.GetFundingLot(ctx, sub2Lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := service.ListPaymentCandidatesPage(ctx, postgresstore.PaymentCandidatePageQuery{Limit: 100})
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].ExternalOrderID != "701" || candidates.Items[0].TradeNo != "" || candidates.Items[0].CurrentCapMinor != 0 {
		t.Fatalf("New API candidates=%+v err=%v", candidates, err)
	}
	candidateID := candidates.Items[0].ID
	if _, err = service.FreezeNewAPIPaymentWithEvidence(ctx, "90000000-0000-4000-8000-000000000001", candidateID,
		"newapi-console-check-1", "等待支付通道回单"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RejectNewAPIPaymentWithEvidence(ctx, "90000000-0000-4000-8000-000000000001", candidateID,
		"newapi-console-check-2", "当前凭证不足，暂不认可"); err != nil {
		t.Fatal(err)
	}
	proposedCandidate, err := service.VerifyNewAPIPaymentWithEvidence(ctx,
		"90000000-0000-4000-8000-000000000001", candidateID, 30_000, domain.CurrencyCNY,
		"payment-provider-settlement-701")
	if err != nil || proposedCandidate.Verification != domain.VerificationFrozen || proposedCandidate.CurrentCapMinor != 0 {
		t.Fatalf("first reviewer granted entitlement: candidate=%+v err=%v", proposedCandidate, err)
	}
	verifiedCandidate, err := service.VerifyNewAPIPaymentWithEvidence(ctx,
		"90000000-0000-4000-8000-000000000002", candidateID, 30_000, domain.CurrencyCNY,
		"payment-provider-settlement-701")
	if err != nil || verifiedCandidate.Verification != domain.VerificationVerified || verifiedCandidate.CurrentCapMinor != 30_000 {
		t.Fatalf("dual-reviewed candidate=%+v err=%v", verifiedCandidate, err)
	}
	var evidenceCiphertext, reasonCiphertext []byte
	if err = store.Pool().QueryRow(ctx, `
		SELECT evidence_ref_ciphertext,review_reason_ciphertext
		FROM payment_candidate_reviews WHERE funding_lot_id=$1 AND review_action='freeze'`, candidateID).Scan(&evidenceCiphertext, &reasonCiphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(evidenceCiphertext, []byte("newapi-console-check-1")) || bytes.Contains(reasonCiphertext, []byte("等待支付通道回单")) {
		t.Fatal("payment review evidence or reason was stored in plaintext")
	}
	// A later New API scan remains a non-authoritative candidate and cannot
	// rewrite the amount that was independently reviewed above.
	newCandidateBody := []byte(fmt.Sprintf(`{"external_order_id":"701","external_user_id":"7","source_status":"success","order_type":"topup","quoted_amount":"999","observed_pay_amount":"999.00","verification_state":"pending_manual","verification_reason":"manual settlement evidence required","completed_at":%q,"created_at":%q,"observed_at":%q,"payment_type":"stripe","provider_key":"stripe"}`,
		now.Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano)))
	commit(newAPIID, "payments", "72000000-0000-4000-8000-000000000006", strings.Repeat("b", 64), strings.Repeat("7", 64), 2,
		"72100000-0000-4000-8000-000000000006", "payment_candidate", newCandidateBody, strings.Repeat("c", 64))
	clock = clock.Add(time.Minute)
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	verifiedCandidate, err = store.GetFundingLot(ctx, candidateID)
	if err != nil || verifiedCandidate.Verification != domain.VerificationVerified || verifiedCandidate.CurrentCapMinor != 30_000 {
		t.Fatalf("candidate rescan rewrote reviewed amount: lot=%+v err=%v", verifiedCandidate, err)
	}
	manualEvidence := "newapi-provider-refund-701"
	manualReason := "人工退款后仅剩 100.00 可开票净额"
	manualAdjusted, err := service.ApplyNewAPIManualCap(ctx,
		"90000000-0000-4000-8000-000000000003", candidateID, 10_000,
		manualEvidence, manualReason)
	if err != nil || manualAdjusted.Verification != domain.VerificationFrozen || manualAdjusted.CurrentCapMinor != 10_000 {
		t.Fatalf("manual New API cap=%+v err=%v", manualAdjusted, err)
	}
	// Exact network retry is idempotent even though the current cap now equals
	// the requested target; a different evidence tuple at the same cap is not.
	if retried, retryErr := service.ApplyNewAPIManualCap(ctx,
		"90000000-0000-4000-8000-000000000003", candidateID, 10_000,
		manualEvidence, manualReason); retryErr != nil || retried.CurrentCapMinor != 10_000 {
		t.Fatalf("manual cap idempotent retry=%+v err=%v", retried, retryErr)
	}
	var manualCiphertext []byte
	if err = store.Pool().QueryRow(ctx, `
		SELECT payload_ciphertext FROM source_events
		WHERE source_instance_id=$1 AND event_kind='refund'
		  AND schema_version='manual-newapi-cap-v1'`, newAPIID).Scan(&manualCiphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manualCiphertext, []byte(manualEvidence)) || bytes.Contains(manualCiphertext, []byte(manualReason)) {
		t.Fatal("manual New API adjustment evidence was stored in plaintext")
	}
	profile, err := service.SaveProfile(ctx, domain.InvoiceProfile{
		PrincipalID: user.ID, Type: domain.ProfilePersonal, Title: "同步用户",
		Email: "sync-user@example.com", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := service.Submit(ctx, ledger.SubmitInput{
		PrincipalID: user.ID, ProfileID: profile.ID, SourceInstanceID: sub2ID,
		IdempotencyKey: "sync-refund-request", Allocations: []ledger.AllocationInput{{
			FundingLotID: sub2Lot.ID, AmountMinor: 20_000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminID := "90000000-0000-4000-8000-000000000001"
	request, err = service.Review(ctx, adminID, request.ID, "approve", "", request.Version)
	if err != nil {
		t.Fatal(err)
	}
	request, err = service.ConfirmManualIssue(ctx, adminID, request.ID, request.Version)
	if err != nil {
		t.Fatal(err)
	}
	adjustmentBody := []byte(fmt.Sprintf(`{"external_order_id":"501","external_user_id":"3","adjustment_type":"refund","amount":"400.00","currency":"CNY","effective_at":%q,"source_status":"REFUNDED","source_updated_at":%q,"basis":"absolute_cumulative_gateway_refund"}`,
		now.Add(time.Minute).Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano)))
	commit(sub2ID, "payments", "72000000-0000-4000-8000-000000000005", strings.Repeat("9", 64), strings.Repeat("5", 64), 2,
		"72100000-0000-4000-8000-000000000005", "payment_adjustment", adjustmentBody, strings.Repeat("a", 64))
	clock = clock.Add(time.Minute)
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	requests, err := service.ListRequests(ctx, user.ID, false)
	if err != nil || len(requests) != 1 || requests[0].Status != domain.StatusRefundAttention {
		t.Fatalf("projected refund request=%+v err=%v", requests, err)
	}
	cases, err := service.ListRefundCasesPage(ctx, postgresstore.RefundCasePageQuery{Limit: 100})
	if err != nil || len(cases.Items) != 1 || cases.Items[0].ObservedRefundMinor != 40_000 {
		t.Fatalf("projected refund cases=%+v err=%v", cases, err)
	}
	health, err := service.SourceIngestHealth(ctx)
	if err != nil || health.Pending != 0 || health.Dead != 0 || !health.OldestPending.IsZero() {
		t.Fatalf("source ingest health=%+v err=%v", health, err)
	}
}

func TestSourceDependencyWaitRecoversStartupOrderAndMonotonicTombstones(t *testing.T) {
	service, store, _, ctx := integrationApplication(t)
	sourceID := "10000000-0000-4000-8000-000000000031"
	if _, err := store.UpsertSourceInstance(ctx, postgresstore.SourceInstanceRecord{
		ID: sourceID, SourceType: domain.SourceSub2API, Name: "dependency-source",
		RuntimeVersion: "test-runtime", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "identities"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, postgresstore.AuditActor{Type: "admin", ID: "90000000-0000-4000-8000-000000000001"}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	verifiedAt := base.Format(time.RFC3339Nano)
	identity := identityBindingPayload{ExternalUserID: "731", ProviderType: "oidc", ProviderKey: "central",
		ProviderSubject: "dependency-subject", Issuer: "https://central-id.example", VerifiedAt: &verifiedAt,
		UserStatus: "active", UpdatedAt: base.Format(time.RFC3339Nano)}
	payment := paymentOrderPayload{ExternalOrderID: "9101", ExternalUserID: "731", Status: "COMPLETED",
		OrderType: "recharge", Amount: "500", PayAmount: "500", Currency: "CNY", RefundAmount: "0",
		GatewayRefundAmount: "0", CompletedAt: &verifiedAt, CreatedAt: base.Format(time.RFC3339Nano),
		UpdatedAt: base.Format(time.RFC3339Nano), PaymentType: "easypay", ProviderKey: "easypay"}
	identityBody, _ := json.Marshal(identity)
	paymentBody, _ := json.Marshal(payment)
	commit := func(stream, batchID, bodyHash, previous string, sequence int64, eventID, entity, operation string, body []byte, payloadHash string, observed time.Time) {
		t.Helper()
		_, err := service.CommitVerifiedSourceBatch(ctx, sourceID, VerifiedSourceBatch{
			SourceInstanceID: sourceID, StreamID: stream, BatchID: batchID, Sequence: sequence,
			BodyHash: bodyHash, PreviousBatchHash: previous, SigningKeyID: "dependency-key",
			SourceRuntimeVersion: "test-runtime", SourceAgentVersion: "test-agent",
			SourceCapturedAt: observed, ProjectionStatus: "healthy",
			Events: []VerifiedSourceBatchEvent{{EventID: eventID, EntityType: entity, Operation: operation,
				PayloadHash: payloadHash, Payload: body, ObservedAt: observed}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	commit("payments", "73000000-0000-4000-8000-000000000001", strings.Repeat("1", 64), "", 1,
		"73100000-0000-4000-8000-000000000001", "payment_order", "upsert", paymentBody, strings.Repeat("2", 64), base)
	commit("identities", "73000000-0000-4000-8000-000000000002", strings.Repeat("3", 64), "", 1,
		"73100000-0000-4000-8000-000000000002", "identity_binding", "upsert", identityBody, strings.Repeat("4", 64), base)
	// Keep the claim horizon ahead of small Docker Desktop host/VM clock skew;
	// this test exercises dependency ordering rather than wall-clock sync.
	processor := SourceEventProcessor{Service: service, BatchSize: 100, Now: func() time.Time { return time.Now().UTC().Add(time.Minute) }}
	if processed, err := processor.RunOnce(ctx); err != nil || processed != 2 {
		t.Fatalf("initial dependency pass processed=%d err=%v", processed, err)
	}
	var waiting, attempts, unhealthy int
	var earliestRetry time.Time
	if err := store.Pool().QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE processing_status IN ('waiting_dependency','parked_identity')),
			COALESCE(sum(attempt_count),0),
			count(*) FILTER (WHERE processing_status IN ('queued','failed','processing','dead')),
			min(next_attempt_at) FILTER (WHERE processing_status IN ('waiting_dependency','parked_identity'))
		FROM source_ingest_events WHERE source_instance_id=$1`, sourceID).Scan(&waiting, &attempts, &unhealthy, &earliestRetry); err != nil {
		t.Fatal(err)
	}
	if waiting != 2 || attempts != 0 || unhealthy != 0 || earliestRetry.Before(time.Now().UTC().Add(11*time.Hour)) {
		t.Fatalf("waiting=%d attempts=%d unhealthy=%d earliest_retry=%s", waiting, attempts, unhealthy, earliestRetry)
	}
	if _, err := service.EnsureUser(ctx, OIDCIdentity{Issuer: "https://central-id.example", Subject: "dependency-subject",
		Email: "dependency@example.com", EmailVerified: true, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	lot, err := store.GetFundingLotByExternalOrder(ctx, sourceID, "9101")
	if err != nil || lot.CurrentCapMinor != 50_000 || lot.Verification != domain.VerificationVerified {
		rows, queryErr := store.Pool().Query(ctx, `
			SELECT entity_type,processing_status,COALESCE(processing_error,''),COALESCE(dependency_kind,''),attempt_count
			FROM source_ingest_events WHERE source_instance_id=$1 ORDER BY entity_type`, sourceID)
		var diagnostics []string
		if queryErr == nil {
			for rows.Next() {
				var entity, status, processingError, dependency string
				var attempt int
				_ = rows.Scan(&entity, &status, &processingError, &dependency, &attempt)
				diagnostics = append(diagnostics, fmt.Sprintf("%s:%s:%s:%s:%d", entity, status, processingError, dependency, attempt))
			}
			rows.Close()
		}
		t.Fatalf("dependency-resolved lot=%+v err=%v events=%v queryErr=%v", lot, err, diagnostics, queryErr)
	}

	confirmed := base.Add(2 * time.Hour)
	identityTombstone, _ := json.Marshal(tombstonePayload{ExternalID: "731", Reason: "repeated_complete_miss", ConfirmedAt: confirmed.Format(time.RFC3339Nano)})
	paymentTombstone, _ := json.Marshal(tombstonePayload{ExternalID: "9101", Reason: "repeated_complete_miss", ConfirmedAt: confirmed.Format(time.RFC3339Nano)})
	commit("identities", "73000000-0000-4000-8000-000000000003", strings.Repeat("5", 64), strings.Repeat("3", 64), 2,
		"73100000-0000-4000-8000-000000000003", "identity_binding", "tombstone", identityTombstone, strings.Repeat("6", 64), confirmed)
	commit("payments", "73000000-0000-4000-8000-000000000004", strings.Repeat("7", 64), strings.Repeat("1", 64), 2,
		"73100000-0000-4000-8000-000000000004", "payment_order", "tombstone", paymentTombstone, strings.Repeat("8", 64), confirmed)
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	lot, err = store.GetFundingLotByExternalOrder(ctx, sourceID, "9101")
	if err != nil || lot.CurrentCapMinor != 0 || lot.Verification != domain.VerificationFrozen {
		t.Fatalf("tombstoned lot=%+v err=%v", lot, err)
	}
	account, err := store.GetExternalAccountBySourceUser(ctx, sourceID, "731")
	if err != nil || account.BindingStatus != "revoked" {
		t.Fatalf("tombstoned account=%+v err=%v", account, err)
	}

	// Later delivery of older upserts (including dependency-wait reordering)
	// is audited and processed as a no-op despite its newer batch sequence.
	commit("identities", "73000000-0000-4000-8000-000000000005", strings.Repeat("9", 64), strings.Repeat("5", 64), 3,
		"73100000-0000-4000-8000-000000000005", "identity_binding", "upsert", identityBody, strings.Repeat("a", 64), base)
	commit("payments", "73000000-0000-4000-8000-000000000006", strings.Repeat("b", 64), strings.Repeat("7", 64), 3,
		"73100000-0000-4000-8000-000000000006", "payment_order", "upsert", paymentBody, strings.Repeat("c", 64), base)
	if _, err = processor.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	lot, _ = store.GetFundingLotByExternalOrder(ctx, sourceID, "9101")
	account, _ = store.GetExternalAccountBySourceUser(ctx, sourceID, "731")
	if lot.CurrentCapMinor != 0 || lot.Verification != domain.VerificationFrozen || account.BindingStatus != "revoked" {
		t.Fatalf("older events revived state: lot=%+v account=%+v", lot, account)
	}
	var staleAudits int
	if err = store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE action IN
		('funding_lot.stale_source_event_ignored','external_account.stale_source_event_ignored')
		AND actor_id=$1`, sourceID).Scan(&staleAudits); err != nil || staleAudits < 2 {
		t.Fatalf("stale audit count=%d err=%v", staleAudits, err)
	}
}
