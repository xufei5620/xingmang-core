package postgresstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

type eligibilityOpsFixture struct {
	sourceID, userID, accountID, lotID, freezeID, checkpointID string
	manifestHash, configHash, unitCode                         string
	policy                                                     SourceFreshnessPolicy
}

func seedEligibilityOpsFixture(t *testing.T, store *Store, ctx context.Context) eligibilityOpsFixture {
	t.Helper()
	f := eligibilityOpsFixture{sourceID: "10000000-0000-4000-8000-000000000001", userID: "20000000-0000-4000-8000-000000000001", accountID: "30000000-0000-4000-8000-000000000001", lotID: "50000000-0000-4000-8000-000000000001", freezeID: "61000000-0000-4000-8000-000000000001", checkpointID: "62000000-0000-4000-8000-000000000001"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var cutover time.Time
	finalized := now
	var manifestHash, configHash, runtimeVersion, unitCode string
	revision := strings.Repeat("c", 64)
	if err := store.pool.QueryRow(ctx, `SELECT scm.manifest_hash,scm.configuration_hash,scm.cutover_at,scm.unit_code,si.runtime_version FROM source_cutover_manifests scm JOIN source_instances si ON si.id=scm.source_instance_id WHERE scm.source_instance_id=$1`, f.sourceID).Scan(&manifestHash, &configHash, &cutover, &unitCode, &runtimeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET enabled=TRUE WHERE id=$1`, f.sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if err := store.ProvisionSourceStream(ctx, f.sourceID, stream, AuditActor{Type: "system", ID: "ops-test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE source_ingest_state SET sequence=1,last_batch_hash=$1,source_runtime_version=$2,source_agent_version='ops-agent',projection_status='healthy',last_accepted_at=$3,last_nonempty_batch_at=$3,updated_at=$3 WHERE source_instance_id=$4 AND stream_id=$5`, testHash("ops-"+stream), runtimeVersion, now, f.sourceID, stream); err != nil {
			t.Fatal(err)
		}
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if _, err := store.pool.Exec(ctx, `UPDATE source_economic_stream_watermarks SET watermark_at=$3,source_sequence=1,source_cursor=$2||':1',configuration_hash=$4,updated_at=$3 WHERE source_instance_id=$1 AND stream_kind=$2`, f.sourceID, stream, now, configHash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET finalized_through=$2,eligibility_status='frozen',unit_code=$3 WHERE external_account_id=$1`, f.accountID, finalized, unitCode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(id,source_instance_id,external_account_id,external_event_id,checkpoint_id,checkpoint_kind,as_of,balance_service_units,expected_service_units,difference_service_units,unit_code,cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at) VALUES($1,$2,$3,'ops-checkpoint-event','ops-checkpoint','reconciliation',$4,0,0,0,'SUB2_BALANCE_1E8',$5,$6,'matched',1,'balance:1',$4,$7,$4)`, f.checkpointID, f.sourceID, f.accountID, finalized, manifestHash, configHash, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_checkpoint_evaluations(id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status) VALUES('63000000-0000-4000-8000-000000000001',$1,1,0,0,'matched')`, f.checkpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash) VALUES($1,$2,NULL,'UNKNOWN_NEGATIVE_BALANCE','balance_checkpoint','ops-checkpoint',$3)`, f.freezeID, f.accountID, revision); err != nil {
		t.Fatal(err)
	}
	f.manifestHash = manifestHash
	f.configHash = configHash
	f.unitCode = unitCode
	f.policy = SourceFreshnessPolicy{EconomicHeartbeatMaxAge: time.Hour, EconomicWatermarkMaxAge: time.Hour, IdentitiesMaxAge: time.Hour, Now: now}
	return f
}

func validFreezeResolution(f eligibilityOpsFixture) ResolveEligibilityFreezeInput {
	return ResolveEligibilityFreezeInput{FreezeID: f.freezeID, ExpectedVersion: 1, EvidenceHash: strings.Repeat("e", 64), EvidenceCiphertext: bytes.Repeat([]byte{7}, 32), NoteHash: strings.Repeat("f", 64), NoteCiphertext: bytes.Repeat([]byte{8}, 32), FreshnessPolicy: f.policy, Actor: AuditActor{Type: "admin", ID: "70000000-0000-4000-8000-000000000001", RequestID: "ops-resolve", Reason: "test"}}
}

func setOpsConsumedCash(t *testing.T, store *Store, ctx context.Context, lotID string, verified, consumed, cashUnits, consumedUnits, reserved, issued int64) {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	numerator := verified * consumedUnits
	remainder := numerator - consumed*cashUnits
	if _, err = tx.Exec(ctx, `UPDATE funding_lot_consumption_state SET cash_service_units=$1,consumed_service_units=$2,cumulative_cash_numerator=$3,rounded_consumed_cash_minor=$4,rounding_remainder_numerator=$5,state_version=state_version+1,updated_at=now() WHERE funding_lot_id=$6`, cashUnits, consumedUnits, numerator, consumed, remainder, lotID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE funding_lots SET eligibility_kind='WALLET_CASH',eligibility_cutover_at=now()-interval '2 hours',verified_cash_minor=$1,consumed_cash_minor=$2,reserved_minor=$3,issued_minor=$4,verification_state='verified',refund_frozen=FALSE,eligibility_revision=eligibility_revision+1,updated_at=now() WHERE id=$5`, verified, consumed, reserved, issued, lotID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEligibilityFreezeAdminPageAndSafeResolution(t *testing.T) {
	store, ctx := integrationStore(t)
	f := seedEligibilityOpsFixture(t, store, ctx)
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id) VALUES('61000000-0000-4000-8000-000000000002',$1,'SOURCE_GAP','source_stream','usage')`, f.accountID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListEligibilityFreezesPage(ctx, EligibilityFreezePageQuery{Limit: 1, Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.HasMore || page.NextBeforeID == "" {
		t.Fatalf("freeze keyset page=%+v", page)
	}
	next, err := store.ListEligibilityFreezesPage(ctx, EligibilityFreezePageQuery{Limit: 1, Status: "open", BeforeOpenedAt: page.NextBeforeOpenedAt, BeforeID: page.NextBeforeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.Items[0].PrincipalID != f.userID {
		t.Fatalf("freeze next page=%+v", next)
	}
	if _, err = store.ListEligibilityFreezesPage(ctx, EligibilityFreezePageQuery{Limit: 101, Status: "unsafe"}); err == nil {
		t.Fatal("unsafe freeze filter accepted")
	}
	// Keep only the target open freeze for the all-open-closed transition.
	if _, err = store.pool.Exec(ctx, `DELETE FROM eligibility_freezes WHERE id='61000000-0000-4000-8000-000000000002'`); err != nil {
		t.Fatal(err)
	}
	in := validFreezeResolution(f)
	if _, err = store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(id,source_instance_id,external_account_id,external_event_id,checkpoint_id,checkpoint_kind,as_of,balance_service_units,expected_service_units,difference_service_units,unit_code,cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at) SELECT '62000000-0000-4000-8000-000000000002',source_instance_id,external_account_id,'ops-checkpoint-event-2','ops-checkpoint-2',checkpoint_kind,as_of,balance_service_units,expected_service_units,difference_service_units,unit_code,cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence+1,'balance:2',stream_watermark_at,$2,observed_at FROM balance_reconciliation_checkpoints WHERE id=$1`, f.checkpointID, strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveEligibilityFreeze(ctx, in); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("unevaluated latest checkpoint resolve err=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO balance_checkpoint_evaluations(id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status) VALUES('63000000-0000-4000-8000-000000000002','62000000-0000-4000-8000-000000000002',1,0,0,'matched')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET projection_status='blocked' WHERE source_instance_id=$1 AND stream_id='usage'`, f.sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveEligibilityFreeze(ctx, in); !errors.Is(err, domain.ErrSourceUnavailable) {
		t.Fatalf("unready source resolve err=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_ingest_state SET projection_status='healthy' WHERE source_instance_id=$1 AND stream_id='usage'`, f.sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO eligibility_projection_jobs(external_account_id,requested_through) VALUES($1,now())`, f.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveEligibilityFreeze(ctx, in); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("pending job resolve err=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `DELETE FROM eligibility_projection_jobs WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE funding_lots SET refund_frozen=TRUE WHERE id=$1`, f.lotID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveEligibilityFreeze(ctx, in); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("refund-frozen lot resolve err=%v", err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE funding_lots SET refund_frozen=FALSE WHERE id=$1`, f.lotID); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveEligibilityFreeze(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "resolved" || resolved.ResolutionVersion != 2 || resolved.EligibilityStatus != "active" {
		t.Fatalf("resolved freeze=%+v", resolved)
	}
	var evidence, note []byte
	var accountStatus string
	var jobs int
	if err = store.pool.QueryRow(ctx, `SELECT resolution_evidence_ciphertext,resolution_note_ciphertext FROM eligibility_freezes WHERE id=$1`, f.freezeID).Scan(&evidence, &note); err != nil || !bytes.Equal(evidence, in.EvidenceCiphertext) || !bytes.Equal(note, in.NoteCiphertext) {
		t.Fatal("encrypted freeze evidence was not persisted")
	}
	if err = store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(&accountStatus); err != nil || accountStatus != "active" {
		t.Fatalf("account status=%s err=%v", accountStatus, err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_projection_jobs WHERE external_account_id=$1`, f.accountID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("reprojection jobs=%d err=%v", jobs, err)
	}
	if _, submitErr := store.Submit(ctx, SubmitInput{PrincipalID: f.userID, ProfileID: "40000000-0000-4000-8000-000000000001", SourceInstanceID: f.sourceID, IdempotencyKey: "during-freeze-reprojection", ProfileSnapshotCiphertext: []byte("encrypted-snapshot"), Allocations: []AllocationInput{{FundingLotID: f.lotID, AmountMinor: domain.MinimumRequestMinor}}}); !errors.Is(submitErr, domain.ErrUnverifiedPayment) {
		t.Fatalf("projection job did not block concurrent submit: %v", submitErr)
	}
	if duplicate, dupErr := store.ResolveEligibilityFreeze(ctx, in); dupErr != nil || duplicate.ID != resolved.ID {
		t.Fatalf("idempotent resolution=%+v err=%v", duplicate, dupErr)
	}
	changed := in
	changed.NoteHash = strings.Repeat("1", 64)
	if _, err = store.ResolveEligibilityFreeze(ctx, changed); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("changed duplicate err=%v", err)
	}
	// The PostgreSQL container clock can lead the Windows host by a few
	// milliseconds. Advance the worker clock so a just-queued job is
	// deterministically due instead of making this integration test flaky.
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), AuditActor{Type: "system", ID: "ops-worker", Reason: "test"})
	if err != nil || processed != 1 {
		t.Fatalf("process reprojection=%d err=%v", processed, err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(&accountStatus); err != nil || accountStatus != "active" {
		t.Fatalf("reprojected account status=%s err=%v", accountStatus, err)
	}
	setOpsConsumedCash(t, store, ctx, f.lotID, 100000, 50000, 10, 5, 0, 0)
	if _, err = store.Submit(ctx, SubmitInput{PrincipalID: f.userID, ProfileID: "40000000-0000-4000-8000-000000000001", SourceInstanceID: f.sourceID, IdempotencyKey: "post-freeze-resolution", ProfileSnapshotCiphertext: []byte("encrypted-snapshot"), Allocations: []AllocationInput{{FundingLotID: f.lotID, AmountMinor: domain.MinimumRequestMinor}}}); err != nil {
		t.Fatalf("active reprojected account could not submit: %v", err)
	}
	var audits int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='eligibility.freeze.resolved' AND object_id=$1`, f.freezeID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("resolution audits=%d err=%v", audits, err)
	}
}

func TestEligibilityFreezeSourceRefundNeverUsesGenericResolution(t *testing.T) {
	store, ctx := integrationStore(t)
	f := seedEligibilityOpsFixture(t, store, ctx)
	if _, err := store.pool.Exec(ctx, `UPDATE eligibility_freezes SET freeze_reason='SOURCE_REFUND' WHERE id=$1`, f.freezeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveEligibilityFreeze(ctx, validFreezeResolution(f)); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("SOURCE_REFUND generic resolution err=%v", err)
	}
}

func TestEligibilitySummaryKeepsCashMinorSeparateFromServiceUnits(t *testing.T) {
	store, ctx := integrationStore(t)
	f := seedEligibilityOpsFixture(t, store, ctx)
	if _, err := store.pool.Exec(ctx, `DELETE FROM eligibility_freezes WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET eligibility_status='active' WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	setOpsConsumedCash(t, store, ctx, f.lotID, 50000, 30000, 5, 3, 5000, 10000)
	manifestHash := f.manifestHash
	configHash := f.configHash
	revision := strings.Repeat("9", 64)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_credit_events(id,source_instance_id,external_account_id,external_event_id,external_credit_id,event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,credit_kind,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at) VALUES('64000000-0000-4000-8000-000000000001',$1,$2,'legacy-event','legacy-credit',now()-interval '2 hours',100,'SUB2_BALANCE_1E8',$3,$4,'LEGACY_NON_INVOICEABLE',0,'legacy:1',now()-interval '2 hours',$5,now()-interval '2 hours'),('64000000-0000-4000-8000-000000000002',$1,$2,'bonus-event','bonus-credit',now()-interval '1 hour',50,'SUB2_BALANCE_1E8',$3,$4,'BONUS',1,'bonus:1',now()-interval '1 hour',$5,now()-interval '1 hour')`, f.sourceID, f.accountID, manifestHash, configHash, revision); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListEligibilitySummaries(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("summaries=%+v", items)
	}
	item := items[0]
	if item.AvailableMinor != 15000 || item.ConsumedMinor != 30000 || item.UnconsumedMinor != 20000 || item.ReservedMinor != 5000 || item.IssuedMinor != 10000 {
		t.Fatalf("cash summary=%+v", item)
	}
	if item.LegacyServiceUnits != "100" || item.NonCashServiceUnits != "50" || item.UnitCode != "SUB2_BALANCE_1E8" {
		t.Fatalf("service-unit summary was converted or lost: %+v", item)
	}
}
