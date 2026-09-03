package postgresstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

type v3TestChain struct {
	sequence map[string]int64
	hash     map[string]string
}

type v3TestCycle struct {
	batchID, cycleID string
	ceiling          time.Time
	events           []SourceBatchEvent
}

func newV3TestChain() *v3TestChain {
	return &v3TestChain{sequence: map[string]int64{}, hash: map[string]string{}}
}

func testHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (chain *v3TestChain) commit(t *testing.T, store *Store, ctx context.Context, sourceID, stream,
	cycleID string, ceiling time.Time, events []SourceBatchEvent) v3TestCycle {
	return chain.commitPage(t, store, ctx, sourceID, stream, cycleID, ceiling, ceiling, true, events)
}

func (chain *v3TestChain) commitPage(t *testing.T, store *Store, ctx context.Context, sourceID, stream,
	cycleID string, publishedWatermark, ceiling time.Time, complete bool, events []SourceBatchEvent) v3TestCycle {
	return chain.commitPageWithSnapshotRows(t, store, ctx, sourceID, stream, cycleID,
		publishedWatermark, ceiling, complete, -1, events)
}

func (chain *v3TestChain) commitPageWithSnapshotRows(t *testing.T, store *Store, ctx context.Context, sourceID, stream,
	cycleID string, publishedWatermark, ceiling time.Time, complete bool, snapshotRows int64, events []SourceBatchEvent) v3TestCycle {
	t.Helper()
	chain.sequence[stream]++
	sequence := chain.sequence[stream]
	batchID := fmt.Sprintf("81%06d-0000-4000-8000-%012d", sequence, sequence)
	bodyHash := testHash(stream + cycleID + fmt.Sprint(sequence))
	input := SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: stream,
		BatchID: batchID, Sequence: sequence, BodyHash: bodyHash,
		PreviousBatchHash: chain.hash[stream], SigningKeyID: "test-key",
		SourceRuntimeVersion: "v3-test", SourceAgentVersion: "v3-agent-test",
		SourceCapturedAt: ceiling, ProjectionStatus: "healthy",
		StreamWatermarkAt: publishedWatermark, SourceCursor: fmt.Sprintf("%s:%d", stream, sequence),
		ScanCeilingAt: ceiling, ScanCeilingCursor: "ceiling:" + cycleID,
		ScanCycleID: cycleID, ScanComplete: complete, Events: events,
		Actor: AuditActor{Type: "source_connector", ID: sourceID, Reason: "v3 integration fixture"},
	}
	if stream == "balances" {
		input.ScanSnapshotID = testHash(cycleID)
		for _, event := range events {
			if event.EntityType == "balance_checkpoint" {
				input.ScanSnapshotRowCount++
			}
		}
		if snapshotRows >= 0 {
			input.ScanSnapshotRowCount = snapshotRows
		}
	}
	if _, err := store.CommitSourceBatch(ctx, input); err != nil {
		t.Fatalf("commit %s v3 cycle: %v", stream, err)
	}
	chain.hash[stream] = bodyHash
	return v3TestCycle{batchID: batchID, cycleID: cycleID, ceiling: ceiling, events: events}
}

func markV3CycleProcessed(t *testing.T, store *Store, ctx context.Context, sourceID, stream string, cycle v3TestCycle) {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `
		UPDATE source_ingest_events sie SET processing_status='processed',processed_at=now(),updated_at=now()
		FROM source_economic_scan_cycle_events m
		WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.scan_cycle_id=$3::uuid
			AND sie.source_instance_id=m.source_instance_id AND sie.stream_id=m.stream_id
			AND sie.event_id=m.event_id`, sourceID, stream, cycle.cycleID); err != nil {
		t.Fatal(err)
	}
	if err = tryPublishEconomicScanCyclesTx(ctx, tx, sourceID, stream,
		AuditActor{Type: "source_connector", ID: sourceID, Reason: "v3 integration cycle completed"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestV3ReceiverRejectsFinalizedCycleAppendAndAcceptsNewCycleAtSameCeiling(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000099"
	const oldCycleID = "82000000-0000-4000-8000-000000000001"
	const newCycleID = "82000000-0000-4000-8000-000000000002"
	ceiling := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','v3-cycle-revision-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "payments", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'payment_orders:10','usage_logs:0','credits:0','balance_snapshot:0',$2,$2,0,'test-key')`,
		sourceID, testHash("v3-cycle-revision-manifest"), ceiling.Add(-48*time.Hour), testHash("v3-cycle-revision-config")); err != nil {
		t.Fatal(err)
	}
	firstHash := testHash("finalized-cycle-first")
	first := SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "payments",
		BatchID: "83000000-0000-4000-8000-000000000001", Sequence: 1, BodyHash: firstHash,
		SigningKeyID: "test-key", SourceRuntimeVersion: "v3-test", SourceAgentVersion: "rc29",
		SourceCapturedAt: ceiling, ProjectionStatus: "healthy", StreamWatermarkAt: ceiling,
		SourceCursor: "payment_orders:10", ScanCeilingAt: ceiling, ScanCeilingCursor: "payment_orders:10",
		ScanCycleID: oldCycleID, ScanComplete: true,
		Actor: AuditActor{Type: "source_connector", ID: sourceID, Reason: "v3 finalized cycle regression"},
	}
	if _, err := store.CommitSourceBatch(ctx, first); err != nil {
		t.Fatal(err)
	}
	var oldStatus string
	if err := store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='payments' AND scan_cycle_id=$2::uuid`, sourceID, oldCycleID).Scan(&oldStatus); err != nil || oldStatus != "published" {
		t.Fatalf("first empty cycle status=%q err=%v", oldStatus, err)
	}
	legacyRetry := first
	legacyRetry.BatchID = "83000000-0000-4000-8000-000000000002"
	legacyRetry.Sequence = 2
	legacyRetry.PreviousBatchHash = firstHash
	legacyRetry.BodyHash = testHash("finalized-cycle-legacy-retry")
	legacyRetry.SourceAgentVersion = "rc30"
	if _, err := store.CommitSourceBatch(ctx, legacyRetry); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("append to finalized legacy cycle error=%v", err)
	}
	var sequence int64
	if err := store.pool.QueryRow(ctx, `SELECT sequence FROM source_ingest_state
		WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID).Scan(&sequence); err != nil || sequence != 1 {
		t.Fatalf("rejected legacy append advanced sequence=%d err=%v", sequence, err)
	}
	fresh := legacyRetry
	fresh.BatchID = "83000000-0000-4000-8000-000000000003"
	fresh.BodyHash = testHash("finalized-cycle-revision-salted")
	fresh.ScanCycleID = newCycleID
	if _, err := store.CommitSourceBatch(ctx, fresh); err != nil {
		t.Fatalf("new cycle at unchanged ceiling was rejected: %v", err)
	}
	var newStatus string
	if err := store.pool.QueryRow(ctx, `SELECT s.sequence,c.cycle_status
		FROM source_ingest_state s JOIN source_economic_scan_cycles c
		  ON c.source_instance_id=s.source_instance_id AND c.stream_id=s.stream_id
		WHERE s.source_instance_id=$1 AND s.stream_id='payments' AND c.scan_cycle_id=$2::uuid`, sourceID, newCycleID).Scan(&sequence, &newStatus); err != nil || sequence != 2 || newStatus != "published" {
		t.Fatalf("new cycle sequence=%d status=%q err=%v", sequence, newStatus, err)
	}
}

func TestV3FinalizedUsagePublishesConsumedCashAndAllowsPartialInvoices(t *testing.T) {
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	store, ctx := integrationStoreWithPolicyStart(t, fixtureNow.Add(-115*time.Minute))
	sourceID := "10000000-0000-4000-8000-000000000010"
	userID := "20000000-0000-4000-8000-000000000010"
	accountID := "30000000-0000-4000-8000-000000000010"
	profileID := "40000000-0000-4000-8000-000000000010"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','v3-ledger-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','v3-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'10','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveProfile(ctx, ProfileRecord{ID: profileID, PrincipalID: userID,
		Type: domain.ProfilePersonal, TitleCiphertext: []byte("encrypted-title"),
		EmailCiphertext: []byte("encrypted-email")}); err != nil {
		t.Fatal(err)
	}

	cutover := fixtureNow.Add(-2 * time.Hour)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000001",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: strings.Repeat("1", 64),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutover}
	checkpointEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000002",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: strings.Repeat("2", 64),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: cutover}
	cutoverCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"83000000-0000-4000-8000-000000000001", cutover, []SourceBatchEvent{manifestEvent, checkpointEvent})
	manifestHash := strings.Repeat("a", 64)
	configHash := strings.Repeat("b", 64)
	snapshotHash := testHash(cutoverCycle.cycleID)
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: snapshotHash,
		BaselineSnapshotHash: snapshotHash, BaselineRowCount: 1, SigningKeyID: "ignored-payload-key",
		CutoverAt: cutover, DatabaseClock: cutover, StreamWatermarkAt: cutover,
		ExternalEventID: manifestEvent.EventID, BatchID: cutoverCycle.batchID,
		ScanCycleID: cutoverCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "10", ExternalEventID: checkpointEvent.EventID,
		CheckpointID: snapshotHash + ":10", CheckpointKind: "cutover", BaselineMember: true, BalanceServiceUnits: "0",
		UnitCode: "SUB2_BALANCE_1E8", BaselineSnapshotID: snapshotHash, SourceSnapshotID: snapshotHash,
		SnapshotRowCount: "1",
		AsOf:             cutover, ObservedAt: cutover, StreamWatermarkAt: cutover,
		SourceCursor: "balance:10", SourceRevision: checkpointEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: cutoverCycle.batchID, ScanCycleID: cutoverCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", cutoverCycle)

	finalCeiling := cutover.Add(90 * time.Minute)
	paymentAt := cutover.Add(10 * time.Minute)
	paymentEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000003",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: strings.Repeat("3", 64),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: finalCeiling}
	secondPaymentEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000006",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: strings.Repeat("6", 64),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: finalCeiling}
	paymentCycle := chain.commit(t, store, ctx, sourceID, "payments",
		"83000000-0000-4000-8000-000000000002", finalCeiling, []SourceBatchEvent{paymentEvent, secondPaymentEvent})
	result, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "payment-10", Currency: domain.CurrencyCNY, OriginalMinor: 100_000,
			CurrentCapMinor: 100_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: paymentEvent.PayloadHash, CompletedAt: paymentAt, ObservedAt: finalCeiling},
		ExternalUserID: "10", EventKind: "payment", ExternalEventID: paymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: paymentAt, SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "100", WalletUnitCode: "SUB2_BALANCE_1E8",
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, CausalDomain: "sub2-ledger",
		CausalOrder: "1", SourceCursor: "payment:10", BatchID: paymentCycle.batchID,
		ScanCycleID: paymentCycle.cycleID, StreamWatermarkAt: finalCeiling,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	lotID := result.Lot.ID
	secondResult, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "payment-11", Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
			CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: secondPaymentEvent.PayloadHash, CompletedAt: cutover.Add(12 * time.Minute), ObservedAt: finalCeiling},
		ExternalUserID: "10", EventKind: "payment", ExternalEventID: secondPaymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: cutover.Add(12 * time.Minute), SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "100", WalletUnitCode: "SUB2_BALANCE_1E8",
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, CausalDomain: "sub2-ledger",
		CausalOrder: "2", SourceCursor: "payment:11", BatchID: paymentCycle.batchID,
		ScanCycleID: paymentCycle.cycleID, StreamWatermarkAt: finalCeiling,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	secondLotID := secondResult.Lot.ID
	markV3CycleProcessed(t, store, ctx, sourceID, "payments", paymentCycle)

	creditAt := cutover.Add(15 * time.Minute)
	creditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000005",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: strings.Repeat("5", 64),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: finalCeiling}
	creditCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"83000000-0000-4000-8000-000000000004", finalCeiling, []SourceBatchEvent{creditEvent})
	if err = store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: "10", ExternalEventID: creditEvent.EventID,
		ExternalCreditID: "bonus-10", EventTime: creditAt, ObservedAt: finalCeiling,
		StreamWatermarkAt: finalCeiling, ServiceUnits: "20", UnitCode: "SUB2_BALANCE_1E8",
		CreditKind: "BONUS", CausalDomain: "sub2-ledger", CausalOrder: "3",
		SourceCursor: "credit:10", SourceRevision: creditEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: creditCycle.batchID, ScanCycleID: creditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", creditCycle)

	usageAt := cutover.Add(20 * time.Minute)
	usageEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000004",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: strings.Repeat("4", 64),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: finalCeiling}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage",
		"83000000-0000-4000-8000-000000000003", finalCeiling, []SourceBatchEvent{usageEvent})
	if err = store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: "10", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "usage-10", EventTime: usageAt, ObservedAt: finalCeiling,
		StreamWatermarkAt: finalCeiling, ServiceUnits: "80", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", CausalDomain: "sub2-ledger", CausalOrder: "4",
		SourceCursor: "usage:10", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", usageCycle)

	matchingBalanceEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000009",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: strings.Repeat("9", 64),
		PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: finalCeiling}
	matchingBalanceCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"83000000-0000-4000-8000-000000000005", finalCeiling, []SourceBatchEvent{matchingBalanceEvent})
	if err = store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "10", ExternalEventID: matchingBalanceEvent.EventID,
		CheckpointID: strings.Repeat("9", 64) + ":10", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "140", UnitCode: "SUB2_BALANCE_1E8",
		SourceSnapshotID: testHash(matchingBalanceCycle.cycleID), SnapshotRowCount: "1", BaselineMember: true,
		AsOf: cutover.Add(70 * time.Minute), ObservedAt: finalCeiling, StreamWatermarkAt: finalCeiling,
		SourceCursor: "balance-matched:10", SourceRevision: matchingBalanceEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 2,
		BatchID: matchingBalanceCycle.batchID, ScanCycleID: matchingBalanceCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", matchingBalanceCycle)
	// This account is SIGNED_CUTOVER and the reconciliation checkpoint above
	// is unrelated to design 2.4; see processEligibilityWithoutReanchor's
	// doc comment for why the job queue is bypassed here.
	processEligibilityWithoutReanchor(t, store, ctx, accountID, finalCeiling, AuditActor{Type: "system", ID: "test-worker"})
	lot, err := store.GetFundingLot(ctx, lotID)
	if err != nil || lot.ConsumedCashMinor != 60_000 || lot.AvailableMinor() != 60_000 {
		t.Fatalf("projected lot=%+v err=%v", lot, err)
	}
	secondLot, err := store.GetFundingLot(ctx, secondLotID)
	if err != nil || secondLot.ConsumedCashMinor != 0 {
		t.Fatalf("cash FIFO consumed later lot first: lot=%+v err=%v", secondLot, err)
	}

	multiCycleID := "83000000-0000-4000-8000-000000000008"
	multiCeiling := finalCeiling.Add(2 * time.Minute)
	chain.commitPage(t, store, ctx, sourceID, "usage", multiCycleID, finalCeiling, multiCeiling, false, nil)
	var publishedWatermark time.Time
	if err = store.pool.QueryRow(ctx, `SELECT watermark_at FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='usage'`, sourceID).Scan(&publishedWatermark); err != nil || !publishedWatermark.Equal(finalCeiling) {
		t.Fatalf("intermediate page advanced watermark=%s err=%v", publishedWatermark, err)
	}
	chain.commitPage(t, store, ctx, sourceID, "usage", multiCycleID, multiCeiling, multiCeiling, true, nil)
	if err = store.pool.QueryRow(ctx, `SELECT watermark_at FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='usage'`, sourceID).Scan(&publishedWatermark); err != nil || !publishedWatermark.Equal(multiCeiling) {
		t.Fatalf("final scan page did not publish watermark=%s err=%v", publishedWatermark, err)
	}

	first, err := store.Submit(ctx, SubmitInput{PrincipalID: userID, ProfileID: profileID,
		SourceInstanceID: sourceID, IdempotencyKey: "partial-400", ProfileSnapshotCiphertext: []byte("encrypted"),
		Allocations: []AllocationInput{{FundingLotID: lotID, AmountMinor: 40_000}}})
	if err != nil || first.AmountMinor != 40_000 {
		t.Fatalf("first partial invoice=%+v err=%v", first, err)
	}
	second, err := store.Submit(ctx, SubmitInput{PrincipalID: userID, ProfileID: profileID,
		SourceInstanceID: sourceID, IdempotencyKey: "partial-200", ProfileSnapshotCiphertext: []byte("encrypted"),
		Allocations: []AllocationInput{{FundingLotID: lotID, AmountMinor: 20_000}}})
	if err != nil || second.AmountMinor != 20_000 {
		t.Fatalf("second partial invoice=%+v err=%v", second, err)
	}
	if _, err = store.Submit(ctx, SubmitInput{PrincipalID: userID, ProfileID: profileID,
		SourceInstanceID: sourceID, IdempotencyKey: "over-consumed", ProfileSnapshotCiphertext: []byte("encrypted"),
		Allocations: []AllocationInput{{FundingLotID: lotID, AmountMinor: 20_000}}}); err == nil {
		t.Fatal("invoice allocation exceeded finalized consumed cash")
	}
	issuedAdmin := "90000000-0000-4000-8000-000000000010"
	reviewed, err := store.ReviewRequest(ctx, issuedAdmin, first.ID, "approve", "", first.Version,
		AuditActor{Type: "admin", ID: issuedAdmin})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := store.ConfirmManualIssue(ctx, ConfirmIssueInput{AdminID: issuedAdmin,
		RequestID: first.ID, ExpectedVersion: reviewed.Request.Version, IssuerSettingRevision: 1,
		IssueSnapshotCiphertext: []byte("encrypted-issuer"), Actor: AuditActor{Type: "admin", ID: issuedAdmin}})
	if err != nil || issued.Request.Status != domain.StatusIssuedAwaitingDocument {
		t.Fatalf("fixture issue=%+v err=%v", issued, err)
	}

	// A previously unseen non-cash credit with an already-finalized event time
	// lowers the cash allocation. The replay must reject the pending request and
	// put issued exposure into refund attention instead of silently rewriting it.
	lateCreditEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000008",
		EntityType: "credit_event", Operation: "upsert", PayloadHash: strings.Repeat("8", 64),
		PayloadCiphertext: bytes.Repeat([]byte{8}, 32), ObservedAt: finalCeiling.Add(time.Minute)}
	lateCreditCycle := chain.commit(t, store, ctx, sourceID, "credits",
		"83000000-0000-4000-8000-000000000007", finalCeiling.Add(time.Minute), []SourceBatchEvent{lateCreditEvent})
	if err = store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: "10", ExternalEventID: lateCreditEvent.EventID,
		ExternalCreditID: "late-bonus-10", EventTime: cutover.Add(16 * time.Minute),
		ObservedAt: finalCeiling.Add(time.Minute), StreamWatermarkAt: finalCeiling.Add(time.Minute),
		ServiceUnits: "30", UnitCode: "SUB2_BALANCE_1E8", CreditKind: "BONUS",
		CausalDomain: "sub2-ledger", CausalOrder: "35", SourceCursor: "late-credit:10",
		SourceRevision: lateCreditEvent.PayloadHash, CutoverManifestHash: manifestHash,
		ConfigurationHash: configHash, SourceSequence: 2, BatchID: lateCreditCycle.batchID,
		ScanCycleID: lateCreditCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", lateCreditCycle)
	var issuedStatus, pendingStatus domain.RequestStatus
	if err = store.pool.QueryRow(ctx, `SELECT status FROM invoice_requests WHERE id=$1`, first.ID).Scan(&issuedStatus); err != nil || issuedStatus != domain.StatusRefundAttention {
		t.Fatalf("late reduction issued status=%s err=%v", issuedStatus, err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT status FROM invoice_requests WHERE id=$1`, second.ID).Scan(&pendingStatus); err != nil || pendingStatus != domain.StatusRejected {
		t.Fatalf("late reduction pending status=%s err=%v", pendingStatus, err)
	}
	if _, err = store.ConfirmManualIssue(ctx, ConfirmIssueInput{AdminID: issuedAdmin, RequestID: second.ID,
		ExpectedVersion: second.Version, IssuerSettingRevision: 1, IssueSnapshotCiphertext: []byte("encrypted-issuer")}); err == nil {
		t.Fatal("eligibility-frozen reservation was still issuable")
	}

	negativeEvent := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000007",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: strings.Repeat("7", 64),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: finalCeiling.Add(time.Minute)}
	negativeCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"83000000-0000-4000-8000-000000000006", finalCeiling.Add(time.Minute), []SourceBatchEvent{negativeEvent})
	if err = store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "10", ExternalEventID: negativeEvent.EventID,
		CheckpointID: strings.Repeat("d", 64) + ":10", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "0", BalanceNegative: true, UnitCode: "SUB2_BALANCE_1E8",
		SourceSnapshotID: testHash(negativeCycle.cycleID), SnapshotRowCount: "1", AsOf: cutover.Add(70 * time.Minute),
		ObservedAt: finalCeiling.Add(time.Minute), StreamWatermarkAt: finalCeiling.Add(time.Minute),
		SourceCursor: "balance-negative:10", SourceRevision: negativeEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 3,
		BatchID: negativeCycle.batchID, ScanCycleID: negativeCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", negativeCycle)
	processEligibilityWithoutReanchor(t, store, ctx, accountID, finalCeiling.Add(time.Minute), AuditActor{Type: "system", ID: "test-worker"})
	var status string
	if err = store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "frozen" {
		t.Fatalf("negative balance did not freeze account: status=%q err=%v", status, err)
	}
}

func TestEligibilityPolicyKeepsPreStartFundingNonInvoiceableAndIncludesExactBoundary(t *testing.T) {
	start := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	store, ctx := integrationStoreWithPolicyStart(t, start)
	sourceID := "11000000-0000-4000-8000-000000000011"
	userID := "21000000-0000-4000-8000-000000000011"
	accountID := "31000000-0000-4000-8000-000000000011"
	preLotID := "51000000-0000-4000-8000-000000000011"
	postLotID := "51000000-0000-4000-8000-000000000012"
	preUsageID := "61000000-0000-4000-8000-000000000011"
	postUsageID := "61000000-0000-4000-8000-000000000012"
	manifestHash := strings.Repeat("a", 64)
	configHash := strings.Repeat("b", 64)
	revision := strings.Repeat("c", 64)
	cutover := start.Add(-time.Hour)
	through := start.Add(10 * time.Minute)
	_, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-boundary','policy-test')`, sourceID)
	if err == nil {
		_, err = store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
			VALUES($1,'test','policy-user')`, userID)
	}
	if err == nil {
		_, err = store.pool.Exec(ctx, `INSERT INTO external_accounts(
				id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
			VALUES($1,$2,$3,'policy-user','test','verified')`, accountID, userID, sourceID)
	}
	if err == nil {
		_, err = store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
			source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
			projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
			credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
			baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'policy-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'p','u','c','b',$2,$2,1,'policy-key')`,
			sourceID, manifestHash, cutover, configHash)
	}
	if err == nil {
		_, err = store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
			external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
			cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$5,900)`,
			accountID, sourceID, cutover, manifestHash, cutover)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.pool.Exec(ctx, `INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,verified_cash_minor,consumed_cash_minor,
			eligibility_kind,eligibility_cutover_at,verification_state,source_status,
			source_revision_hash,completed_at,observed_at)
		VALUES
			($1,$2,$3,$4,'pre-policy','CNY',10000,10000,0,0,'LEGACY_NON_INVOICEABLE',NULL,
			 'verified','COMPLETED',$6,$7,$9),
			($8,$2,$3,$4,'at-policy','CNY',10000,10000,10000,0,'WALLET_CASH',$5,
			 'verified','COMPLETED',$6,$5,$9)`,
		preLotID, userID, accountID, sourceID, start, revision,
		start.Add(-10*time.Minute), postLotID, through)
	if err == nil {
		_, err = store.pool.Exec(ctx, `INSERT INTO funding_lot_consumption_state(funding_lot_id,cash_service_units)
			VALUES($1,100)`, postLotID)
	}
	if err == nil {
		_, err = store.pool.Exec(ctx, `INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES
			($1,$2,$3,'pre-usage','pre-usage',$4,50,'SUB2_BALANCE_1E8',$5,$6,
			 'wallet',FALSE,1,'pre-usage',$7,$8,$7),
			($9,$2,$3,'post-usage','post-usage',$10,120,'SUB2_BALANCE_1E8',$5,$6,
			 'wallet',TRUE,2,'post-usage',$7,$8,$7)`,
			preUsageID, sourceID, accountID, start.Add(-5*time.Minute), manifestHash,
			configHash, through, revision, postUsageID, start.Add(time.Minute))
	}
	if err != nil {
		t.Fatal(err)
	}
	classificationTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := getEligibilityAccountTx(ctx, classificationTx, accountID, true)
	if err == nil {
		err = applyPrePolicyWalletFundingTx(ctx, classificationTx, preLotID, accountID, account,
			SourceObservation{
				Lot: domain.FundingLot{SourceInstanceID: sourceID, SourceRevision: revision,
					CompletedAt: start.Add(-10 * time.Minute), ObservedAt: through},
				ExternalEventID: "pre-policy-payment", WalletUnitCode: "SUB2_BALANCE_1E8",
				CutoverManifestHash: manifestHash, ConfigurationHash: configHash,
				SourceSequence: 1, SourceCursor: "pre-policy-payment", StreamWatermarkAt: through,
			}, big.NewInt(100), nil, AuditActor{Type: "source_connector", ID: sourceID})
	}
	if err != nil {
		_ = classificationTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = classificationTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = reprojectEligibilityTx(ctx, tx, accountID, through,
		AuditActor{Type: "system", ID: "policy-test"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	postLot, err := store.GetFundingLot(ctx, postLotID)
	if err != nil || postLot.ConsumedCashMinor != 7_000 || postLot.AvailableMinor() != 7_000 {
		t.Fatalf("post-policy cash projection=%+v err=%v", postLot, err)
	}
	var preCashAllocations, postCashAllocations int
	if err = store.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE usage_event_id=$1 AND funding_lot_id IS NOT NULL),
			count(*) FILTER (WHERE usage_event_id=$2 AND funding_lot_id IS NOT NULL)
		FROM consumption_allocations WHERE usage_event_id IN ($1,$2)`, preUsageID, postUsageID).
		Scan(&preCashAllocations, &postCashAllocations); err != nil {
		t.Fatal(err)
	}
	if preCashAllocations != 0 || postCashAllocations != 1 {
		t.Fatalf("cash allocation crossed policy boundary: pre=%d post=%d", preCashAllocations, postCashAllocations)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO consumption_allocations(
		id,usage_event_id,funding_lot_id,allocation_order,service_units,cash_minor_delta,projection_version)
		VALUES('71000000-0000-4000-8000-000000000011',$1,$2,1,1,100,99)`, preUsageID, postLotID); err == nil {
		t.Fatal("pre-policy usage was accepted as cash consumption evidence")
	}
	otherUserID := "21000000-0000-4000-8000-000000000012"
	otherAccountID := "31000000-0000-4000-8000-000000000012"
	otherUsageID := "61000000-0000-4000-8000-000000000013"
	if _, err = store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','other-policy-user')`, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'other-policy-user',$4,'test','verified')`, otherAccountID, otherUserID,
		sourceID, "h1:"+strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`,
		otherAccountID, sourceID, cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO source_usage_events(
		id,source_instance_id,external_account_id,external_event_id,external_usage_id,
		event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
		billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
		source_revision_hash,observed_at)
		VALUES($1,$2,$3,'other-usage','other-usage',$4,1,'SUB2_BALANCE_1E8',$5,$6,
		'wallet',TRUE,1,'other-usage',$7,$8,$7)`, otherUsageID, sourceID, otherAccountID,
		start.Add(time.Minute), manifestHash, configHash, through, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO consumption_allocations(
		id,usage_event_id,funding_lot_id,allocation_order,service_units,cash_minor_delta,projection_version)
		VALUES('71000000-0000-4000-8000-000000000012',$1,$2,1,1,100,99)`, otherUsageID, postLotID); err == nil {
		t.Fatal("cross-account usage was accepted as cash consumption evidence")
	}
	tamperTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tamperTx.Exec(ctx, `UPDATE funding_lot_consumption_state SET
		consumed_service_units=71,cumulative_cash_numerator=710000,
		rounded_consumed_cash_minor=7100,rounding_remainder_numerator=0
		WHERE funding_lot_id=$1`, postLotID); err == nil {
		_, err = tamperTx.Exec(ctx, `UPDATE funding_lots SET consumed_cash_minor=7100 WHERE id=$1`, postLotID)
	}
	if err != nil {
		_ = tamperTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tamperTx.Commit(ctx); err == nil {
		t.Fatal("wallet consumed cash was inflated without matching usage allocations")
	}
	exactUsageID := "61000000-0000-4000-8000-000000000014"
	if _, err = store.pool.Exec(ctx, `INSERT INTO source_usage_events(
		id,source_instance_id,external_account_id,external_event_id,external_usage_id,
		event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
		billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
		source_revision_hash,observed_at)
		VALUES($1,$2,$3,'exact-policy-usage','exact-policy-usage',$4,1,'SUB2_BALANCE_1E8',$5,$6,
		'wallet',TRUE,3,'exact-policy-usage',$7,$8,$7)`, exactUsageID, sourceID, accountID,
		start, manifestHash, configHash, through, revision); err != nil {
		t.Fatalf("usage exactly at eligibility boundary was rejected: %v", err)
	}
	if _, err = store.pool.Exec(ctx, `
		UPDATE invoice_eligibility_policy
		SET eligibility_start_at=eligibility_start_at-interval '1 second',
			policy_version=policy_version+1,updated_by='forbidden-test'
		WHERE singleton_id=1`); err == nil {
		t.Fatal("financial ingestion did not make eligibility policy immutable")
	}
}

func TestSubmitRejectsLegacyNonCashAndRefundFrozenLotsAtSQLBoundary(t *testing.T) {
	store, ctx := integrationStore(t)
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET cutover_at=cutover_at+interval '1 minute'
		WHERE external_account_id='30000000-0000-4000-8000-000000000001'`); err == nil {
		t.Fatal("database allowed account eligibility cutover to be rewritten")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET cutover_balance_units=cutover_balance_units+1
		WHERE external_account_id='30000000-0000-4000-8000-000000000001'`); err == nil {
		t.Fatal("database allowed cutover balance to be rewritten")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalization_delay_seconds=finalization_delay_seconds+1
		WHERE external_account_id='30000000-0000-4000-8000-000000000001'`); err == nil {
		t.Fatal("database allowed finalization delay to be rewritten")
	}
	base := `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,verified_cash_minor,consumed_cash_minor,
			eligibility_kind,eligibility_cutover_at,refund_frozen,verification_state,
			source_status,source_revision_hash,completed_at,observed_at)
		VALUES($1,'20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000001',$2,'CNY',50000,50000,$3,$4,$5,$6,$7,
			'verified','COMPLETED','attack-fixture',now(),now())`
	fixtures := []struct {
		id, order, kind    string
		verified, consumed int64
		cutover            any
		refund             bool
	}{
		{"50000000-0000-4000-8000-000000000091", "legacy-attack", "LEGACY_NON_INVOICEABLE", 50_000, 50_000, nil, false},
		{"50000000-0000-4000-8000-000000000092", "noncash-attack", "NON_CASH", 0, 0, time.Now().UTC(), false},
		{"50000000-0000-4000-8000-000000000093", "refund-attack", "WALLET_CASH", 50_000, 50_000, time.Now().UTC(), true},
	}
	for _, fixture := range fixtures {
		if _, err := store.pool.Exec(ctx, base, fixture.id, fixture.order, fixture.verified,
			fixture.consumed, fixture.kind, fixture.cutover, fixture.refund); err != nil {
			t.Fatal(err)
		}
		_, err := store.Submit(ctx, SubmitInput{
			PrincipalID:      "20000000-0000-4000-8000-000000000001",
			ProfileID:        "40000000-0000-4000-8000-000000000001",
			SourceInstanceID: "10000000-0000-4000-8000-000000000001",
			IdempotencyKey:   "reject-" + fixture.order, ProfileSnapshotCiphertext: []byte("encrypted"),
			Allocations: []AllocationInput{{FundingLotID: fixture.id, AmountMinor: 20_000}},
		})
		if err == nil {
			t.Fatalf("%s lot was accepted for invoice allocation", fixture.kind)
		}
	}
}

func TestParkedIdentityFactCanPublishAndCatchUpAfterBinding(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := "10000000-0000-4000-8000-000000000001"
	accountID := "30000000-0000-4000-8000-000000000001"
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET runtime_version='v3-test' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "usage", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	var oldWatermark time.Time
	if err := store.pool.QueryRow(ctx, `SELECT watermark_at FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='usage'`, sourceID).Scan(&oldWatermark); err != nil {
		t.Fatal(err)
	}
	ceiling := oldWatermark.Add(time.Minute)
	event := SourceBatchEvent{EventID: "82000000-0000-4000-8000-000000000090",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: strings.Repeat("9", 64),
		PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: ceiling}
	chain := newV3TestChain()
	cycle := chain.commit(t, store, ctx, sourceID, "usage",
		"83000000-0000-4000-8000-000000000090", ceiling, []SourceBatchEvent{event})
	claims, err := store.ClaimUnprocessedSourceEvents(ctx, 10, ceiling.Add(time.Minute))
	if err != nil || len(claims) != 1 {
		t.Fatalf("initial parked claim=%+v err=%v", claims, err)
	}
	catchupKey := "h1:" + strings.Repeat("a", 64)
	if err = store.MarkSourceEventWaitingDependency(ctx, claims[0], "source_external_account",
		catchupKey, ceiling.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var cycleStatus string
	if err = store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='usage' AND scan_cycle_id=$2::uuid`,
		sourceID, cycle.cycleID).Scan(&cycleStatus); err != nil || cycleStatus != "published" {
		t.Fatalf("parked fact blocked durable cycle publication: status=%q err=%v", cycleStatus, err)
	}
	if _, err = store.RequeueSourceDependency(ctx, "source_external_account", catchupKey); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET eligibility_status='syncing',catchup_key_hmac=$1 WHERE external_account_id=$2`, catchupKey, accountID); err != nil {
		t.Fatal(err)
	}
	claims, err = store.ClaimUnprocessedSourceEvents(ctx, 10, ceiling.Add(time.Minute))
	if err != nil || len(claims) != 1 || claims[0].CatchupKeyHMAC != catchupKey {
		t.Fatalf("catch-up claim=%+v err=%v", claims, err)
	}
	if err = store.MarkSourceEventProcessed(ctx, claims[0], ceiling.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var eligibilityStatus string
	if err = store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&eligibilityStatus); err != nil || eligibilityStatus != "active" {
		t.Fatalf("published parked fact did not complete catch-up: status=%q err=%v", eligibilityStatus, err)
	}
}

func TestTruncatedBalanceSnapshotBlocksCycleAndDoesNotAdvanceWatermark(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := "10000000-0000-4000-8000-000000000001"
	if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET runtime_version='v3-test' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	var oldWatermark time.Time
	if err := store.pool.QueryRow(ctx, `SELECT watermark_at FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='balances'`, sourceID).Scan(&oldWatermark); err != nil {
		t.Fatal(err)
	}
	event := SourceBatchEvent{EventID: "8e000000-0000-4000-8000-000000000001",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: strings.Repeat("e", 64),
		PayloadCiphertext: bytes.Repeat([]byte{14}, 32), ObservedAt: oldWatermark.Add(time.Minute)}
	chain := newV3TestChain()
	cycle := chain.commitPageWithSnapshotRows(t, store, ctx, sourceID, "balances",
		"8f000000-0000-4000-8000-000000000001", oldWatermark.Add(time.Minute),
		oldWatermark.Add(time.Minute), true, 2, []SourceBatchEvent{event})
	// The signed batch claims two rows, while only one immutable checkpoint
	// event was durably mapped. Publication must fail closed.
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", cycle)
	var cycleStatus string
	if err := store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`, sourceID, cycle.cycleID).Scan(&cycleStatus); err != nil || cycleStatus != "blocked" {
		t.Fatalf("truncated balance cycle status=%q err=%v", cycleStatus, err)
	}
	var currentWatermark time.Time
	if err := store.pool.QueryRow(ctx, `SELECT watermark_at FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='balances'`, sourceID).Scan(&currentWatermark); err != nil || !currentWatermark.Equal(oldWatermark) {
		t.Fatalf("truncated balance cycle advanced watermark=%s old=%s err=%v", currentWatermark, oldWatermark, err)
	}
	var projectionStatus, eligibilityStatus string
	if err := store.pool.QueryRow(ctx, `SELECT projection_status FROM source_ingest_state
		WHERE source_instance_id=$1 AND stream_id='balances'`, sourceID).Scan(&projectionStatus); err != nil || projectionStatus != "blocked" {
		t.Fatalf("truncated balance cycle source status=%q err=%v", projectionStatus, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id='30000000-0000-4000-8000-000000000001'`).Scan(&eligibilityStatus); err != nil || eligibilityStatus != "frozen" {
		t.Fatalf("truncated balance cycle account status=%q err=%v", eligibilityStatus, err)
	}
	var openFreeze int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id='30000000-0000-4000-8000-000000000001'
			AND freeze_reason='SOURCE_GAP' AND status='open'`).Scan(&openFreeze); err != nil || openFreeze != 1 {
		t.Fatalf("truncated balance cycle open freeze=%d err=%v", openFreeze, err)
	}
}

func TestMissingUsageIsCaughtByLowerBalanceCheckpointWithoutIncreasingEligibility(t *testing.T) {
	store, ctx := integrationStore(t)
	lotID := "50000000-0000-4000-8000-000000000001"
	accountID := "30000000-0000-4000-8000-000000000001"
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE funding_lot_consumption_state SET cash_service_units=100,
		consumed_service_units=0,cumulative_cash_numerator=0,rounded_consumed_cash_minor=0,
		rounding_remainder_numerator=0,state_version=state_version+1 WHERE funding_lot_id=$1`, lotID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE funding_lots SET consumed_cash_minor=0,
		eligibility_revision=eligibility_revision+1 WHERE id=$1`, lotID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var manifestHash, configHash, unitCode string
	if err = store.pool.QueryRow(ctx, `SELECT manifest_hash,configuration_hash,unit_code
		FROM source_cutover_manifests WHERE source_instance_id='10000000-0000-4000-8000-000000000001'`).Scan(
		&manifestHash, &configHash, &unitCode); err != nil {
		t.Fatal(err)
	}
	var asOf time.Time
	if err = store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&asOf); err != nil {
		t.Fatal(err)
	}
	checkpointID := "88000000-0000-4000-8000-000000000001"
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,as_of,balance_service_units,balance_negative,unit_code,
			cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,'10000000-0000-4000-8000-000000000001',$2,
			'88000000-0000-4000-8000-000000000002',$3,'reconciliation',$4,90,FALSE,$5,
			$6,$7,'pending_finalization',1,'missing-usage-checkpoint',$4,$8,$4)`,
		checkpointID, accountID, strings.Repeat("e", 64)+":1", asOf, unitCode,
		manifestHash, configHash, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	// processEligibilityWithoutReanchor, not ProcessEligibilityProjectionJobs
	// (XM-INV-ANCHOR-BALANCE): this fixture's account is SIGNED_CUTOVER
	// (integrationStore's shared default, bootstrapped by
	// UpsertFundingLot's fixture path), and the checkpoint just inserted
	// above -- dated at/after the policy start -- is otherwise
	// indistinguishable from a design XM-INV-POLICY-ANCHOR 2.4 re-anchor
	// candidate to reanchorLegacyEligibilityAccountTx's lookup, which cares
	// only about checkpoint_kind/as_of, never about a test's own intent for
	// the row. Going through the real job queue here would silently
	// re-anchor the account to this exact checkpoint first (see
	// processEligibilityWithoutReanchor's own doc comment for this same
	// class of fixture/design-2.4 interaction), replacing what this test
	// wants to exercise -- a real checkpoint reporting less balance than
	// the ledger's own SIGNED_CUTOVER-boundary projection expects -- with
	// an unrelated fresh-POLICY_ANCHOR-anchor evaluation instead.
	processEligibilityWithoutReanchor(t, store, ctx, accountID, asOf, AuditActor{Type: "system", ID: "test-worker"})
	lot, err := store.GetFundingLot(ctx, lotID)
	// XM-INV-ELIG-AUTO-RECONCILE: a negative balance difference no longer
	// freezes the account (eligibility_freezes/manual queue) -- it downgrades
	// to the self-clearing not_invoiceable_pending_reconciliation state
	// instead. Still fails closed (AvailableMinor()==0): submit/issue still
	// require eligibility_status='active'.
	if err != nil || lot.EligibilityStatus != "not_invoiceable_pending_reconciliation" || lot.AvailableMinor() != 0 {
		t.Fatalf("missing usage did not fail closed: lot=%+v err=%v", lot, err)
	}
}

func TestConcurrentSubmitAndRefundNeverLeavesNormallyIssuableRequest(t *testing.T) {
	store, ctx := integrationStore(t)
	for i := 1; i <= 8; i++ {
		lotID := fmt.Sprintf("59000000-0000-4000-8000-%012d", i)
		orderID := fmt.Sprintf("concurrent-order-%d", i)
		lot := domain.FundingLot{ID: lotID, PrincipalID: "20000000-0000-4000-8000-000000000001",
			SourceInstanceID: "10000000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API,
			ExternalOrderID: orderID, Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
			CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: fmt.Sprintf("initial-%d", i), CompletedAt: time.Now().UTC().Add(-time.Minute),
			ObservedAt: time.Now().UTC()}
		if err := store.UpsertFundingLot(ctx, lot); err != nil {
			t.Fatal(err)
		}
		stored, err := store.GetFundingLot(ctx, lotID)
		if err != nil {
			t.Fatal(err)
		}
		stored.CurrentCapMinor = 0
		stored.SourceStatus = "REFUNDED"
		stored.SourceRevision = fmt.Sprintf("refund-%d", i)
		stored.ObservedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		start := make(chan struct{})
		type operationResult struct {
			operation string
			err       error
		}
		results := make(chan operationResult, 2)
		go func(iteration int) {
			<-start
			_, submitErr := store.Submit(ctx, SubmitInput{
				PrincipalID:               "20000000-0000-4000-8000-000000000001",
				ProfileID:                 "40000000-0000-4000-8000-000000000001",
				SourceInstanceID:          "10000000-0000-4000-8000-000000000001",
				IdempotencyKey:            fmt.Sprintf("concurrent-submit-%d", iteration),
				ProfileSnapshotCiphertext: []byte("encrypted"),
				Allocations:               []AllocationInput{{FundingLotID: lotID, AmountMinor: 20_000}},
			})
			results <- operationResult{operation: "submit", err: submitErr}
		}(i)
		go func(iteration int) {
			<-start
			_, refundErr := store.ObserveFundingLot(ctx, SourceObservation{
				Lot: stored, ExternalUserID: "u1", EventKind: "refund",
				ExternalEventID: fmt.Sprintf("concurrent-refund-%d", iteration), SchemaVersion: "test",
			}, AuditActor{Type: "source_connector", ID: "test"})
			results <- operationResult{operation: "refund", err: refundErr}
		}(i)
		close(start)
		firstResult, secondResult := <-results, <-results
		if firstResult.operation == "refund" && firstResult.err != nil || secondResult.operation == "refund" && secondResult.err != nil {
			t.Fatalf("refund side of concurrent race failed: first=%+v second=%+v", firstResult, secondResult)
		}
		refunded, err := store.GetFundingLot(ctx, lotID)
		if err != nil || !refunded.RefundFrozen || refunded.AvailableMinor() != 0 {
			t.Fatalf("refund race left lot available: lot=%+v err=%v", refunded, err)
		}
		var active int
		if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM invoice_requests
			WHERE invoice_user_id='20000000-0000-4000-8000-000000000001'
				AND idempotency_key=$1 AND status IN ('pending_review','needs_changes','approved','manual_issuing','issued_awaiting_document','issued')`,
			fmt.Sprintf("concurrent-submit-%d", i)).Scan(&active); err != nil || active != 0 {
			t.Fatalf("refund race left active request count=%d err=%v", active, err)
		}
	}
}

func TestUnknownPositiveCheckpointIsConservativelyPlacedBeforeIntervalUsage(t *testing.T) {
	store, ctx := integrationStore(t)
	lotID := "50000000-0000-4000-8000-000000000001"
	accountID := "30000000-0000-4000-8000-000000000001"
	sourceID := "10000000-0000-4000-8000-000000000001"
	var cutover, finalized time.Time
	var manifestHash, configHash, unitCode, snapshotHash string
	if err := store.pool.QueryRow(ctx, `
		SELECT eas.cutover_at,eas.finalized_through,scm.manifest_hash,scm.configuration_hash,
			scm.unit_code,scm.baseline_snapshot_hash
		FROM source_account_eligibility_state eas
		JOIN source_cutover_manifests scm ON scm.source_instance_id=eas.source_instance_id
		WHERE eas.external_account_id=$1`, accountID).Scan(&cutover, &finalized, &manifestHash,
		&configHash, &unitCode, &snapshotHash); err != nil {
		t.Fatal(err)
	}
	finalized = cutover.Add(time.Minute)
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$1 WHERE external_account_id=$2`, finalized, accountID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE funding_lot_consumption_state SET cash_service_units=100,
		consumed_service_units=50,cumulative_cash_numerator=5000000,
		rounded_consumed_cash_minor=50000,rounding_remainder_numerator=0,
		state_version=state_version+1 WHERE funding_lot_id=$1`, lotID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE funding_lots SET consumed_cash_minor=50000,
		eligibility_revision=eligibility_revision+1 WHERE id=$1`, lotID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,baseline_snapshot_id,baseline_member,as_of,balance_service_units,balance_negative,
			unit_code,cutover_manifest_hash,configuration_hash,reconciliation_status,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES('89000000-0000-4000-8000-000000000001',$1,$2,
			'89000000-0000-4000-8000-000000000002',$3,'cutover',$4,TRUE,$5,100,FALSE,
			$6,$7,$8,'cutover_baseline',1,'fixture-cutover',$5,$9,$5)`, sourceID, accountID,
		snapshotHash+":u1", snapshotHash, cutover, unitCode, manifestHash, configHash,
		strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	usageAt := cutover.Add(time.Second)
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES('89000000-0000-4000-8000-000000000003',$1,$2,'89000000-0000-4000-8000-000000000003',
			'fixture-usage-50',$3,50,$4,$5,$6,'wallet',TRUE,1,'fixture-usage',$7,$8,$7)`,
		sourceID, accountID, usageAt, unitCode, manifestHash, configHash, finalized,
		strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,as_of,balance_service_units,balance_negative,unit_code,
			cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES('89000000-0000-4000-8000-000000000004',$1,$2,
			'89000000-0000-4000-8000-000000000004',$3,'reconciliation',$4,70,FALSE,$5,
			$6,$7,'pending_finalization',2,'fixture-reconcile',$4,$8,$4)`, sourceID, accountID,
		strings.Repeat("3", 64)+":u1", finalized, unitCode, manifestHash, configHash,
		strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	// This fixture's account is SIGNED_CUTOVER (integrationStore's shared
	// default bootstrap) and the reconciliation checkpoint above happens to
	// be dated at/after integrationStore's own default policy start
	// (fixtureNow-24h) -- through the job queue,
	// reanchorLegacyEligibilityAccountTx's candidate lookup would mistake it
	// for a design-2.4 re-anchor candidate, which is unrelated to what this
	// test (predating that slice) actually exercises: unknown-positive
	// checkpoint placement. See processEligibilityWithoutReanchor's doc
	// comment for why the job queue is bypassed here.
	processEligibilityWithoutReanchor(t, store, ctx, accountID, finalized, AuditActor{Type: "system", ID: "test-worker"})
	lot, err := store.GetFundingLot(ctx, lotID)
	if err != nil || lot.ConsumedCashMinor != 30_000 {
		t.Fatalf("unknown positive was not conservatively applied before usage: lot=%+v err=%v", lot, err)
	}
}

func TestSubscriptionCashRequiresActualUsageEvidenceAndRefundPermanentlyFreezes(t *testing.T) {
	store, ctx := integrationStore(t)
	profileID := "40000000-0000-4000-8000-000000000001"
	userID := "20000000-0000-4000-8000-000000000001"

	t.Run("Sub2 verified subscription", func(t *testing.T) {
		sourceID := "10000000-0000-4000-8000-000000000001"
		if _, err := store.pool.Exec(ctx, `UPDATE source_instances SET runtime_version='v3-test' WHERE id=$1`, sourceID); err != nil {
			t.Fatal(err)
		}
		if err := store.ProvisionSourceStream(ctx, sourceID, "payments", AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
		var manifestHash, configHash, unitCode string
		var cutover time.Time
		if err := store.pool.QueryRow(ctx, `SELECT scm.manifest_hash,scm.configuration_hash,scm.unit_code,eas.cutover_at
			FROM source_cutover_manifests scm JOIN source_account_eligibility_state eas
				ON eas.source_instance_id=scm.source_instance_id
			WHERE scm.source_instance_id=$1 AND eas.external_account_id='30000000-0000-4000-8000-000000000001'`,
			sourceID).Scan(&manifestHash, &configHash, &unitCode, &cutover); err != nil {
			t.Fatal(err)
		}
		completed := cutover.Add(time.Minute)
		event := SourceBatchEvent{EventID: "8a000000-0000-4000-8000-000000000001",
			EntityType: "subscription_purchase", Operation: "upsert", PayloadHash: strings.Repeat("a", 64),
			PayloadCiphertext: bytes.Repeat([]byte{10}, 32), ObservedAt: completed}
		chain := newV3TestChain()
		cycle := chain.commit(t, store, ctx, sourceID, "payments",
			"8b000000-0000-4000-8000-000000000001", completed, []SourceBatchEvent{event})
		observed, err := store.ObserveFundingLot(ctx, SourceObservation{
			Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
				ExternalOrderID: "subscription-sub2", Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
				CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "subscription:verified",
				SourceRevision: event.PayloadHash, CompletedAt: completed, ObservedAt: completed},
			ExternalUserID: "u1", EventKind: "payment", ExternalEventID: event.EventID,
			SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: completed, SourceSequence: 1,
			EligibilityKind: domain.EligibilitySubscriptionCash, CashServiceUnits: "0", WalletUnitCode: unitCode,
			CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceCursor: "subscription-sub2",
			BatchID: cycle.batchID, ScanCycleID: cycle.cycleID, StreamWatermarkAt: completed,
		}, AuditActor{Type: "source_connector", ID: sourceID})
		if err != nil || observed.Lot.ConsumedCashMinor != 0 || observed.Lot.AvailableMinor() != 0 {
			t.Fatalf("subscription payment minted eligibility without usage: lot=%+v err=%v", observed.Lot, err)
		}
		_, err = store.Submit(ctx, SubmitInput{PrincipalID: userID, ProfileID: profileID,
			SourceInstanceID: sourceID, IdempotencyKey: "subscription-sub2-submit",
			ProfileSnapshotCiphertext: []byte("encrypted"),
			Allocations:               []AllocationInput{{FundingLotID: observed.Lot.ID, AmountMinor: 20_000}}})
		if !errors.Is(err, domain.ErrUnverifiedPayment) {
			t.Fatalf("subscription without usage was selectable: %v", err)
		}
		markV3CycleProcessed(t, store, ctx, sourceID, "payments", cycle)
		refunded := observed.Lot
		refunded.CurrentCapMinor = 0
		refunded.Verification = domain.VerificationFrozen
		refunded.SourceStatus = "REFUNDED"
		refunded.SourceRevision = strings.Repeat("b", 64)
		refunded.ObservedAt = completed.Add(time.Minute)
		refundResult, err := store.ObserveFundingLot(ctx, SourceObservation{
			Lot: refunded, ExternalUserID: "u1", EventKind: "refund",
			ExternalEventID: "subscription-sub2-refund", SchemaVersion: "test",
		}, AuditActor{Type: "source_connector", ID: sourceID})
		if err != nil || !refundResult.Lot.RefundFrozen || refundResult.Lot.AvailableMinor() != 0 ||
			len(refundResult.InvalidatedRequestIDs) != 0 {
			t.Fatalf("subscription refund did not permanently freeze lot: result=%+v err=%v", refundResult, err)
		}
	})

	t.Run("NewAPI pending subscription requires two reviewers", func(t *testing.T) {
		sourceID := "10000000-0000-4000-8000-000000000099"
		accountID := "30000000-0000-4000-8000-000000000099"
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name)
			VALUES($1,'newapi','subscription-newapi')`, sourceID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
			VALUES($1,$2,$3,'new-subscription','test','verified')`, accountID, userID, sourceID); err != nil {
			t.Fatal(err)
		}
		candidate := domain.FundingLot{ID: "50000000-0000-4000-8000-000000000099", PrincipalID: userID,
			SourceInstanceID: sourceID, SourceType: domain.SourceNewAPI, ExternalOrderID: "new-subscription-order",
			Currency: domain.CurrencyCNY, OriginalMinor: 30_000, CurrentCapMinor: 0,
			Verification: domain.VerificationPending, SourceStatus: "pending", SourceRevision: "candidate",
			CompletedAt: time.Now().UTC(), ObservedAt: time.Now().UTC()}
		if err := store.UpsertFundingLot(ctx, candidate); err != nil {
			t.Fatal(err)
		}
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `ALTER TABLE funding_lots
			DISABLE TRIGGER funding_lots_invoice_policy_guard`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM funding_lot_consumption_state WHERE funding_lot_id=$1`, candidate.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE funding_lots SET eligibility_kind='SUBSCRIPTION_CASH'
			WHERE id=$1`, candidate.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `ALTER TABLE funding_lots
			ENABLE TRIGGER funding_lots_invoice_policy_guard`); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		evidence := PaymentEvidence{Hash: strings.Repeat("c", 64), Ciphertext: bytes.Repeat([]byte{12}, 32)}
		review := func(admin string) (domain.FundingLot, error) {
			return store.ReviewNewAPIPaymentCandidate(ctx, ReviewPaymentCandidateInput{
				LotID: candidate.ID, Action: "verify", PaidMinor: 30_000, Currency: domain.CurrencyCNY,
				Evidence: evidence, ReasonCiphertext: bytes.Repeat([]byte{13}, 32), ReasonHash: strings.Repeat("d", 64),
				Actor: AuditActor{Type: "admin", ID: admin},
			})
		}
		firstReview, err := review("90000000-0000-4000-8000-000000000091")
		if err != nil || firstReview.ConsumedCashMinor != 0 {
			t.Fatalf("first reviewer minted subscription cash: lot=%+v err=%v", firstReview, err)
		}
		approved, err := review("90000000-0000-4000-8000-000000000092")
		if err != nil || approved.Verification != domain.VerificationVerified || approved.ConsumedCashMinor != 0 ||
			approved.AvailableMinor() != 0 {
			t.Fatalf("second reviewer minted eligibility without usage: lot=%+v err=%v", approved, err)
		}
		if _, err = store.Submit(ctx, SubmitInput{PrincipalID: userID, ProfileID: profileID,
			SourceInstanceID: sourceID, IdempotencyKey: "subscription-newapi-submit",
			ProfileSnapshotCiphertext: []byte("encrypted"),
			Allocations:               []AllocationInput{{FundingLotID: candidate.ID, AmountMinor: 20_000}}}); !errors.Is(err, domain.ErrUnverifiedPayment) {
			t.Fatalf("reviewed subscription without usage was selectable: %v", err)
		}
		if _, err = store.pool.Exec(ctx, `UPDATE funding_lots SET consumed_cash_minor=1 WHERE id=$1`, candidate.ID); err == nil {
			t.Fatal("database allowed subscription consumption without authoritative usage evidence")
		}
	})
}

func TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage(t *testing.T) {
	// A dedicated policy start and source instance/manifest, not
	// integrationStore's shared defaults: RegisterCutoverManifest requires
	// CutoverAt strictly before the *current* policy start, while this
	// test's own account cutover (2h after the manifest cutover, with
	// further offsets up to +2h30m past that) must be at/after policy start
	// (to bootstrap POLICY_ANCHOR, not be skipped as pre-policy) and still
	// at/before real wall-clock now (migration 0016's POLICY_ANCHOR
	// validation). integrationStore's default policy start (fixtureNow-24h)
	// combined with its shared manifest's cutover (pinned to essentially
	// "now" by UpsertFundingLot's immutable, one-time bootstrap) leaves no
	// window satisfying all three at once, so this test picks its own.
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-5 * time.Hour)
	store, ctx := integrationStoreWithPolicyStart(t, policyStart)
	sourceID := "10000000-0000-4000-8000-000000000077"
	userID := "20000000-0000-4000-8000-000000000077"
	accountID := "30000000-0000-4000-8000-000000000077"
	profileID := "40000000-0000-4000-8000-000000000077"
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','new-account-cutover-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'https://id.example','new-after-cutover')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'77',$4,'test','verified')`, accountID, userID, sourceID,
		"h1:"+strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(ctx, ProfileRecord{ID: profileID, PrincipalID: userID,
		Type: domain.ProfilePersonal, TitleCiphertext: []byte("encrypted-title"),
		EmailCiphertext: []byte("encrypted-email")}); err != nil {
		t.Fatal(err)
	}
	unitCode := "SUB2_BALANCE_1E8"
	configHash := testHash("new-account-cutover-config")
	globalCutover := policyStart.Add(-time.Minute)
	chain := newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000000",
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("new-account-cutover-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: globalCutover}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"8d000000-0000-4000-8000-000000000000", globalCutover, []SourceBatchEvent{manifestEvent})
	manifestHash := testHash("new-account-cutover-manifest")
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: unitCode, PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: testHash("new-account-cutover-baseline"),
		BaselineSnapshotHash: testHash("new-account-cutover-baseline"), BaselineRowCount: 0, SigningKeyID: "ignored-payload-key",
		CutoverAt: globalCutover, DatabaseClock: globalCutover, StreamWatermarkAt: globalCutover,
		ExternalEventID: manifestEvent.EventID, BatchID: manifestCycle.batchID,
		ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)
	accountCutover := globalCutover.Add(2 * time.Hour)
	// A baseline-member checkpoint for this (in truth, brand-new) account is
	// deliberately not exercised here: since XM-INV-POLICY-ANCHOR 2.1, a
	// baseline member's post-policy checkpoint bootstraps directly
	// (bootstrap_kind='POLICY_ANCHOR') instead of waiting for the signed
	// cutover row -- the old "must wait forever" guard this block used to
	// assert is exactly the behavior that design eliminates. That bootstrap
	// path (including its migration-0016 trigger validation) is covered by
	// TestPrePolicyReconciliationCheckpointIsIgnoredForAccountWithoutState;
	// this test's own focus (a genuinely new, non-baseline account's
	// POST_CUTOVER_REPLAY bootstrap and downstream subscription/wallet/usage
	// ordering) is unrelated to that and starts directly below.
	baselineEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000001",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: strings.Repeat("1", 64),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: accountCutover}
	baselineCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"8d000000-0000-4000-8000-000000000001", accountCutover, []SourceBatchEvent{baselineEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "77", ExternalEventID: baselineEvent.EventID,
		CheckpointID: strings.Repeat("2", 64) + ":77", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "100", UnitCode: unitCode, SourceSnapshotID: testHash(baselineCycle.cycleID),
		SnapshotRowCount: "1", BaselineMember: false,
		AsOf: accountCutover, ObservedAt: accountCutover, StreamWatermarkAt: accountCutover,
		SourceCursor: "new-account:77", SourceRevision: baselineEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 2,
		BatchID: baselineCycle.batchID, ScanCycleID: baselineCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", baselineCycle)
	// XM-INV-POLICY-ANCHOR 2.1 superseded POST_CUTOVER_REPLAY here: a
	// non-baseline account's post-policy checkpoint now bootstraps directly
	// via POLICY_ANCHOR, same as a baseline member's would (see the doc
	// comment above) -- no code path creates a new POST_CUTOVER_REPLAY row
	// any more.
	var bootstrapKind string
	if err := store.pool.QueryRow(ctx, `SELECT bootstrap_kind FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&bootstrapKind); err != nil || bootstrapKind != "POLICY_ANCHOR" {
		t.Fatalf("new account bootstrap kind=%q err=%v", bootstrapKind, err)
	}
	var prematureJobs int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, accountID).Scan(&prematureJobs); err != nil || prematureJobs != 0 {
		t.Fatalf("balance-first replay queued before four-stream finalization: jobs=%d err=%v", prematureJobs, err)
	}
	if _, err := store.ProcessEligibilityProjectionJobs(ctx, 10, accountCutover.Add(time.Minute),
		AuditActor{Type: "system", ID: "balance-first-worker"}); err != nil {
		t.Fatal(err)
	}
	var prematureFreezes int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&prematureFreezes); err != nil || prematureFreezes != 0 {
		t.Fatalf("balance-first replay froze before facts arrived: freezes=%d err=%v", prematureFreezes, err)
	}

	preSubscriptionEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000002",
		EntityType: "subscription_purchase", Operation: "upsert", PayloadHash: strings.Repeat("3", 64),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: accountCutover.Add(time.Minute)}
	preWalletEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000003",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: strings.Repeat("4", 64),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: accountCutover.Add(time.Minute)}
	preGlobalSubscriptionEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000006",
		EntityType: "subscription_purchase", Operation: "upsert", PayloadHash: strings.Repeat("7", 64),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: accountCutover.Add(time.Minute)}
	preCycle := chain.commit(t, store, ctx, sourceID, "payments",
		"8d000000-0000-4000-8000-000000000002", accountCutover.Add(time.Minute),
		[]SourceBatchEvent{preSubscriptionEvent, preWalletEvent, preGlobalSubscriptionEvent})
	preCompleted := globalCutover.Add(time.Hour)
	subscription, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "new-user-subscription", Currency: domain.CurrencyCNY, OriginalMinor: 30_000,
			CurrentCapMinor: 30_000, Verification: domain.VerificationVerified, SourceStatus: "subscription:verified",
			SourceRevision: preSubscriptionEvent.PayloadHash, CompletedAt: preCompleted, ObservedAt: accountCutover.Add(time.Minute)},
		ExternalUserID: "77", EventKind: "payment", ExternalEventID: preSubscriptionEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: preCompleted, SourceSequence: 1,
		EligibilityKind: domain.EligibilitySubscriptionCash, CashServiceUnits: "0", WalletUnitCode: unitCode,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceCursor: "new-subscription:77",
		BatchID: preCycle.batchID, ScanCycleID: preCycle.cycleID, StreamWatermarkAt: accountCutover.Add(time.Minute),
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil || subscription.Lot.ConsumedCashMinor != 0 || subscription.Lot.AvailableMinor() != 0 {
		t.Fatalf("post-global subscription minted eligibility without usage: lot=%+v err=%v", subscription.Lot, err)
	}
	preWallet, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "new-user-pre-wallet", Currency: domain.CurrencyCNY, OriginalMinor: 50_000,
			CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: preWalletEvent.PayloadHash, CompletedAt: preCompleted, ObservedAt: accountCutover.Add(time.Minute)},
		ExternalUserID: "77", EventKind: "payment", ExternalEventID: preWalletEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: preCompleted, SourceSequence: 1,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "100", WalletUnitCode: unitCode,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceCursor: "new-pre-wallet:77",
		BatchID: preCycle.batchID, ScanCycleID: preCycle.cycleID, StreamWatermarkAt: accountCutover.Add(time.Minute),
	}, AuditActor{Type: "source_connector", ID: sourceID})
	// Under the pre-2.1 POST_CUTOVER_REPLAY bootstrap, a POST_CUTOVER_REPLAY
	// account's cutover_at was pinned to the source's own global cutover, so
	// a wallet payment completed anywhere after that (even before this
	// specific account's later per-account boundary) was retained,
	// unclassified, for later replay. XM-INV-POLICY-ANCHOR 2.1 (as it
	// originally shipped) instead pinned a POLICY_ANCHOR account's
	// cutover_at to the anchoring checkpoint's own as_of (accountCutover
	// here, 2h after globalCutover), so a wallet payment completed before
	// that but after globalCutover (like preWallet's, only 1h after
	// globalCutover) fell into a dead zone: not in scope for this account
	// yet, left at its unclassified default (LEGACY_NON_INVOICEABLE,
	// frozen).
	//
	// XM-INV-ELIG-POLICY-START-ANCHOR (design XM-INV-ELIG-SIMPLIFY section
	// 3(D)) closes exactly that dead zone: cutover_at is now unconditionally
	// the global policy start, so applyFundingObservationEligibilityTx's
	// wallet-cash branch (keyed off account.CutoverAt) now finds preWallet's
	// completed_at (policyStart+59m) after account.CutoverAt (policyStart)
	// and classifies it normally -- verified, WALLET_CASH, active, fully
	// available pending consumption -- instead of leaving it at the
	// unclassified default. This is the intended behavior, not a regression:
	// it is the same "a real in-window cash payment must not be permanently
	// excluded" guarantee policy_start_anchor_integration_test.go's own
	// TestPolicyStartBootstrapIncludesInWindowCashFundingLot exercises for a
	// checkpoint-triggered bootstrap, here exercised for a funding-lot
	// observation arriving after the account has already bootstrapped.
	// domain.EligibilitySubscriptionCash is unaffected (it keys off
	// account.GlobalCutoverAt, untouched by either slice), which is exactly
	// why the subscription checked just above this still passes unmodified.
	// AvailableMinor() is 0 here, not VerifiedCashMinor: it tracks
	// ConsumedCashMinor (cash already recognized by an actual usage
	// allocation) minus what is reserved/issued, not merely "verified" --
	// no projection has run yet for this freshly-observed lot, so nothing
	// has been recognized against it yet regardless of eligibility kind.
	if err != nil || preWallet.Lot.EligibilityKind != domain.EligibilityWalletCash ||
		preWallet.Lot.EligibilityStatus != "active" || preWallet.Lot.VerifiedCashMinor != 50_000 ||
		preWallet.Lot.ConsumedCashMinor != 0 || preWallet.Lot.AvailableMinor() != 0 {
		t.Fatalf("post-global, post-policy-start wallet payment was not classified normally: lot=%+v err=%v", preWallet.Lot, err)
	}
	preGlobalSubscription, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "new-user-pre-global-subscription", Currency: domain.CurrencyCNY, OriginalMinor: 30_000,
			CurrentCapMinor: 30_000, Verification: domain.VerificationVerified, SourceStatus: "subscription:verified",
			SourceRevision: preGlobalSubscriptionEvent.PayloadHash, CompletedAt: globalCutover,
			ObservedAt: accountCutover.Add(time.Minute)},
		ExternalUserID: "77", EventKind: "payment", ExternalEventID: preGlobalSubscriptionEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: globalCutover, SourceSequence: 1,
		EligibilityKind: domain.EligibilitySubscriptionCash, CashServiceUnits: "0", WalletUnitCode: unitCode,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceCursor: "pre-global-subscription:77",
		BatchID: preCycle.batchID, ScanCycleID: preCycle.cycleID, StreamWatermarkAt: accountCutover.Add(time.Minute),
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil || preGlobalSubscription.Lot.EligibilityKind != domain.EligibilityLegacyNonInvoiceable || preGlobalSubscription.Lot.AvailableMinor() != 0 {
		t.Fatalf("pre-global subscription became eligible: lot=%+v err=%v", preGlobalSubscription.Lot, err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "payments", preCycle)
	if _, err = store.Submit(ctx, SubmitInput{PrincipalID: userID, ProfileID: profileID,
		SourceInstanceID: sourceID, IdempotencyKey: "new-account-subscription",
		ProfileSnapshotCiphertext: []byte("encrypted"),
		Allocations:               []AllocationInput{{FundingLotID: subscription.Lot.ID, AmountMinor: 20_000}}}); !errors.Is(err, domain.ErrUnverifiedPayment) {
		t.Fatalf("subscription without usage was selectable after bootstrap: %v", err)
	}

	finalCeiling := accountCutover.Add(30 * time.Minute)
	postPaymentAt := accountCutover.Add(5 * time.Minute)
	postPaymentEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000004",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: strings.Repeat("5", 64),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: finalCeiling}
	postPaymentCycle := chain.commit(t, store, ctx, sourceID, "payments",
		"8d000000-0000-4000-8000-000000000003", finalCeiling, []SourceBatchEvent{postPaymentEvent})
	postPayment, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "new-user-post-wallet", Currency: domain.CurrencyCNY, OriginalMinor: 100_000,
			CurrentCapMinor: 100_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: postPaymentEvent.PayloadHash, CompletedAt: postPaymentAt, ObservedAt: finalCeiling},
		ExternalUserID: "77", EventKind: "payment", ExternalEventID: postPaymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: postPaymentAt, SourceSequence: 2,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "100", WalletUnitCode: unitCode,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, CausalDomain: "new-account-ledger",
		CausalOrder: "1", SourceCursor: "new-post-wallet:77", BatchID: postPaymentCycle.batchID,
		ScanCycleID: postPaymentCycle.cycleID, StreamWatermarkAt: finalCeiling,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "payments", postPaymentCycle)
	usageEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000005",
		EntityType: "usage_event", Operation: "upsert", PayloadHash: strings.Repeat("6", 64),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: finalCeiling}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage",
		"8d000000-0000-4000-8000-000000000004", finalCeiling, []SourceBatchEvent{usageEvent})
	// 100 units, not the pre-2.1 fixture's 150: postPayment alone provides
	// 100 units of eligible cash now that preWallet no longer becomes
	// cash-eligible (see the comment above), so 150 units would exceed
	// available cash and freeze the account (USAGE_EXCEEDS_LEDGER,
	// pre-existing and unrelated to this slice) instead of exercising what
	// this section actually targets: post-bootstrap wallet usage becoming
	// eligible and getting consumed.
	if err = store.ObserveUsageEvent(ctx, UsageObservation{SourceInstanceID: sourceID, ExternalUserID: "77",
		ExternalEventID: usageEvent.EventID, ExternalUsageID: "new-account-usage", EventTime: accountCutover.Add(10 * time.Minute),
		ObservedAt: finalCeiling, StreamWatermarkAt: finalCeiling, ServiceUnits: "100", UnitCode: unitCode,
		BillingScope: "wallet", CausalDomain: "new-account-ledger", CausalOrder: "2",
		SourceCursor: "new-usage:77", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", usageCycle)
	chain.commit(t, store, ctx, sourceID, "credits", "8d000000-0000-4000-8000-000000000005", finalCeiling, nil)
	matchingBalanceEvent := SourceBatchEvent{EventID: "8c000000-0000-4000-8000-000000000007",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: strings.Repeat("8", 64),
		PayloadCiphertext: bytes.Repeat([]byte{8}, 32), ObservedAt: finalCeiling}
	matchingBalanceCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"8d000000-0000-4000-8000-000000000006", finalCeiling, []SourceBatchEvent{matchingBalanceEvent})
	// 100, not the pre-XM-INV-ANCHOR-BALANCE fixture's 50 (XM-INV-ANCHOR-BALANCE):
	// this account's own anchor checkpoint (100 units at accountCutover) is
	// now correctly treated as a trusted opening non-cash credit -- see the
	// postPaymentLot assertion below -- so it is available to, and fully
	// consumed by, the 100-unit usage above before that usage ever reaches
	// postPayment's cash. ExpectedBalance at this as_of is therefore
	// 0 (anchor credit, fully drained) + 100 (postPayment's cash,
	// untouched) = 100, which is what a real reconciliation checkpoint at
	// this point would report; a hardcoded "50" no longer reconciles and
	// freezes UNKNOWN_NEGATIVE_BALANCE instead of matching.
	if err = store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "77", ExternalEventID: matchingBalanceEvent.EventID,
		CheckpointID: strings.Repeat("8", 64) + ":77", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "100", UnitCode: unitCode,
		SourceSnapshotID: testHash(matchingBalanceCycle.cycleID), SnapshotRowCount: "1", BaselineMember: false,
		AsOf: accountCutover.Add(15 * time.Minute), ObservedAt: finalCeiling, StreamWatermarkAt: finalCeiling,
		SourceCursor: "new-account-matched:77", SourceRevision: matchingBalanceEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 3,
		BatchID: matchingBalanceCycle.batchID, ScanCycleID: matchingBalanceCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", matchingBalanceCycle)
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"})
	if err != nil || processed == 0 {
		t.Fatalf("new-account post-bootstrap projection processed=%d err=%v", processed, err)
	}
	// 0, not the pre-XM-INV-ANCHOR-BALANCE fixture's 100_000 (XM-INV-ANCHOR-BALANCE):
	// the account's own 100-unit anchor is now a real, trusted non-cash
	// credit (design XM-INV-POLICY-ANCHOR 2.1's "entirely non-invoiceable"
	// opening balance, finally honored end-to-end) and fully absorbs the
	// 100-unit usage above, so postPayment's real paid cash is never
	// touched -- exactly the non-invoiceable-opening-balance guarantee this
	// slice fixes, not a regression.
	postPaymentLot, err := store.GetFundingLot(ctx, postPayment.Lot.ID)
	if err != nil || postPaymentLot.ConsumedCashMinor != 0 {
		t.Fatalf("post-bootstrap usage unexpectedly spent real cash instead of the account's own non-cash anchor: lot=%+v err=%v", postPaymentLot, err)
	}
	// preWallet stays LEGACY_NON_INVOICEABLE (established above) and
	// contributes no eligible cash, so it must remain untouched by this
	// projection, not partially consumed.
	preWalletLot, err := store.GetFundingLot(ctx, preWallet.Lot.ID)
	if err != nil || preWalletLot.ConsumedCashMinor != 0 {
		t.Fatalf("non-cash-eligible pre-account-cutover payment was unexpectedly consumed: lot=%+v err=%v", preWalletLot, err)
	}
}
