package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"invoice-system/agents/sourceagent"
)

func TestSub2APIBridgePreservesFinancialAndCutoverSemantics(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	configuration, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*configuration)
	defer admin.Close()
	cleanupBridgeFixture(t, ctx, admin)
	defer cleanupBridgeFixture(t, ctx, admin)
	setupBridgeFixture(t, ctx, admin, sourceagent.SourceSub2API)
	applyBridgeContracts(t, ctx, admin, sourceagent.SourceSub2API)

	var units string
	if err = admin.QueryRowContext(ctx, `SELECT service_units
		FROM invoice_bridge.sub2api_usage_v4('page',$1::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(service_units text)`,
		`{"cutover":"2000-01-01T00:00:00Z","horizon":"2100-01-01T00:00:00Z","limit":10,"usage_logs_position":0,"usage_logs_ceiling":1}`).Scan(&units); err != nil || units != "1" {
		t.Fatalf("numeric half-boundary units=%q err=%v", units, err)
	}

	var negativeUnits string
	var negative bool
	if err = admin.QueryRowContext(ctx, `SELECT balance_service_units,balance_negative
		FROM invoice_bridge.sub2api_balances_v4('rows','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(user_id bigint,balance_service_units text,balance_negative boolean)
		WHERE user_id=2`).Scan(&negativeUnits, &negative); err != nil || negativeUnits != "0" || !negative {
		t.Fatalf("negative balance units=%q negative=%t err=%v", negativeUnits, negative, err)
	}

	creditRequest := `{"cutover":"2000-01-01T00:00:00Z","horizon":"2100-01-01T00:00:00Z","limit":10,"promo_code_usages_position":0,"promo_code_usages_ceiling":1,"user_affiliate_ledger_position":0,"user_affiliate_ledger_ceiling":0,"redeem_codes_position":0,"redeem_codes_ceiling":99992744}`
	var creditCount int
	if err = admin.QueryRowContext(ctx, `SELECT count(*)
		FROM invoice_bridge.sub2api_credits_v4('page',$1::jsonb) bridge(payload)`, creditRequest).Scan(&creditCount); err != nil || creditCount != 2 {
		t.Fatalf("cash-backed redeem was not excluded credit_count=%d err=%v", creditCount, err)
	}
	var cashRedeemLeaked bool
	if err = admin.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM invoice_bridge.sub2api_credits_v4('page',$1::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(source_cursor text)
		WHERE source_cursor='redeem_codes:1')`, creditRequest).Scan(&cashRedeemLeaked); err != nil || cashRedeemLeaked {
		t.Fatalf("cash fulfillment redeem leaked as bonus=%t err=%v", cashRedeemLeaked, err)
	}
	var healthy bool
	var gaps int64
	if err = admin.QueryRowContext(ctx, `SELECT contract_ok,gap_count
		FROM invoice_bridge.sub2api_credits_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(contract_ok boolean,gap_count bigint)`).Scan(&healthy, &gaps); err != nil || !healthy || gaps == 0 {
		t.Fatalf("legitimate ID gaps blocked credits healthy=%t gaps=%d err=%v", healthy, gaps, err)
	}
	if _, err = admin.ExecContext(ctx, `INSERT INTO public.redeem_codes VALUES(4,'NEGATIVE','balance',-1,'used',1,now())`); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err = admin.QueryRowContext(ctx, `SELECT contract_ok,blocked_reason
		FROM invoice_bridge.sub2api_credits_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(contract_ok boolean,blocked_reason text)`).Scan(&healthy, &reason); err != nil || healthy || reason != "credit_contract_invalid" {
		t.Fatalf("invalid credit did not fail closed healthy=%t reason=%q err=%v", healthy, reason, err)
	}
	_, _ = admin.ExecContext(ctx, `DELETE FROM public.redeem_codes WHERE id=4`)

	var utcHash, tokyoHash string
	_, _ = admin.ExecContext(ctx, `SET TIME ZONE 'UTC'`)
	if err = readSub2HealthHash(ctx, admin).Scan(&utcHash); err != nil {
		t.Fatal(err)
	}
	_, _ = admin.ExecContext(ctx, `SET TIME ZONE 'Asia/Tokyo'`)
	if err = readSub2HealthHash(ctx, admin).Scan(&tokyoHash); err != nil || utcHash != tokyoHash {
		t.Fatalf("configuration hash changed by timezone utc=%q tokyo=%q err=%v", utcHash, tokyoHash, err)
	}
	_, _ = admin.ExecContext(ctx, `SET TIME ZONE 'UTC'`)
	if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET
		value=CASE key WHEN 'BALANCE_RECHARGE_MULTIPLIER' THEN '1.000000' ELSE '0.0000' END,
		updated_at=now()+interval '1 hour'
		WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')`); err != nil {
		t.Fatal(err)
	}
	var rewrittenHash string
	if err = readSub2HealthHash(ctx, admin).Scan(&rewrittenHash); err != nil || rewrittenHash != utcHash {
		t.Fatalf("equivalent Sub2 setting rewrite changed semantic hash before=%q after=%q err=%v", utcHash, rewrittenHash, err)
	}
	var timestampCrossed bool
	var rewrittenUnits sql.NullString
	if err = admin.QueryRowContext(ctx, `SELECT
		(SELECT max(updated_at) FROM public.settings WHERE key='BALANCE_RECHARGE_MULTIPLIER')>
		(SELECT completed_at FROM public.payment_orders WHERE id=1),wallet_cash_service_units
		FROM invoice_bridge.sub2api_payments_v4('page',
		'{"cutover":"2000-01-01T00:00:00Z","position_at":"2000-01-01T00:00:00Z","position_id":0,"ceiling_at":"2100-01-01T00:00:00Z","ceiling_id":1,"limit":10}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(source_id bigint,wallet_cash_service_units text)
		WHERE source_id=1`).Scan(&timestampCrossed, &rewrittenUnits); err != nil || !timestampCrossed ||
		!rewrittenUnits.Valid || rewrittenUnits.String != "1000000000" {
		t.Fatalf("same-value timestamp rewrite changed per-order wallet proof crossed=%t units=%#v err=%v", timestampCrossed, rewrittenUnits, err)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET value='0.01' WHERE key='RECHARGE_FEE_RATE'`); err != nil {
		t.Fatal(err)
	}
	var changedFeeHash string
	if err = readSub2HealthHash(ctx, admin).Scan(&changedFeeHash); err != nil || changedFeeHash == utcHash {
		t.Fatalf("real Sub2 fee change did not change semantic hash before=%q after=%q err=%v", utcHash, changedFeeHash, err)
	}
	for _, invalidFee := range []string{"-0.01", "101", "1.234"} {
		if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET value=$1 WHERE key='RECHARGE_FEE_RATE'`, invalidFee); err != nil {
			t.Fatal(err)
		}
		if err = admin.QueryRowContext(ctx, `SELECT contract_ok
			FROM invoice_bridge.sub2api_usage_v4('health','{}'::jsonb) bridge(payload)
			CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(contract_ok boolean)`).Scan(&healthy); err != nil || healthy {
			t.Fatalf("out-of-contract Sub2 fee %q did not fail closed healthy=%t err=%v", invalidFee, healthy, err)
		}
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET value='invalid' WHERE key='RECHARGE_FEE_RATE'`); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRowContext(ctx, `SELECT contract_ok
		FROM invoice_bridge.sub2api_usage_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(contract_ok boolean)`).Scan(&healthy); err != nil || healthy {
		t.Fatalf("invalid Sub2 fee did not fail closed healthy=%t err=%v", healthy, err)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET value=CASE key
		WHEN 'BALANCE_RECHARGE_MULTIPLIER' THEN '1.00' ELSE '0.00' END
		WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET value='2.0'
		WHERE key='BALANCE_RECHARGE_MULTIPLIER'`); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRowContext(ctx, `SELECT contract_ok
		FROM invoice_bridge.sub2api_usage_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(contract_ok boolean)`).Scan(&healthy); err != nil || healthy {
		t.Fatalf("non-unit Sub2 multiplier did not fail closed healthy=%t err=%v", healthy, err)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.settings SET value='1.00'
		WHERE key='BALANCE_RECHARGE_MULTIPLIER'`); err != nil {
		t.Fatal(err)
	}

	if _, err = admin.ExecContext(ctx, `INSERT INTO public.payment_orders VALUES
		(2,1,'COMPLETED','balance',10,10,0,0,now(),NULL,now(),now(),'stripe','stripe','{"schema_version":"2","provider_key":"stripe","currency":"USD"}',NULL),
		(3,1,'COMPLETED','balance',10,10,0,0,now(),NULL,now(),now(),'unknown','unknown','{}',NULL),
		(4,1,'COMPLETED','balance',10,10,0,0,now(),NULL,now(),now(),'epay','easypay','{"schema_version":"2","provider_key":"easypay","currency":"invalid"}',NULL)`); err != nil {
		t.Fatal(err)
	}
	var unsupported, blocked int64
	if err = admin.QueryRowContext(ctx, `SELECT unsupported_known_non_cny_rows,blocked_unknown_currency_rows
		FROM invoice_bridge.sub2api_payments_v4('legacy_health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(unsupported_known_non_cny_rows bigint,blocked_unknown_currency_rows bigint)`).Scan(&unsupported, &blocked); err != nil || unsupported != 1 || blocked != 1 {
		t.Fatalf("currency evidence health unsupported=%d blocked=%d err=%v", unsupported, blocked, err)
	}
	var ceilingID int64
	if err = admin.QueryRowContext(ctx, `SELECT source_id
		FROM invoice_bridge.sub2api_payments_v4('ceiling','{"horizon":"2100-01-01T00:00:00Z"}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(source_id bigint)`).Scan(&ceilingID); err != nil || ceilingID != 4 {
		t.Fatalf("fixed-CNY invalid-currency row missing from ceiling id=%d err=%v", ceilingID, err)
	}
	var fallbackPageCount int
	if err = admin.QueryRowContext(ctx, `SELECT count(*)
		FROM invoice_bridge.sub2api_payments_v4('page','{"cutover":"2000-01-01T00:00:00Z","position_at":"2000-01-01T00:00:00Z","position_id":0,"ceiling_at":"2100-01-01T00:00:00Z","ceiling_id":4,"limit":10}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(source_id bigint)
		WHERE source_id=4`).Scan(&fallbackPageCount); err != nil || fallbackPageCount != 1 {
		t.Fatalf("fixed-CNY invalid-currency row ceiling/page classification drifted count=%d err=%v", fallbackPageCount, err)
	}
	_, _ = admin.ExecContext(ctx, `DELETE FROM public.payment_orders WHERE id IN (2,3)`)
	if _, err = admin.ExecContext(ctx, `INSERT INTO public.payment_orders VALUES
		(5,1,'COMPLETED','balance',10,10.34,3.33,0,now(),NULL,now(),now(),'epay','easypay','{"schema_version":"2","provider_key":"easypay","currency":"CNY"}',NULL),
		(6,1,'COMPLETED','balance',20,10.34,3.33,0,now(),NULL,now(),now(),'epay','easypay','{"schema_version":"2","provider_key":"easypay","currency":"CNY"}',NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, expectation := range []struct {
		id    int
		valid bool
		units string
	}{{id: 5, valid: true, units: "1000000000"}, {id: 6, valid: false}} {
		var walletUnits sql.NullString
		if err = admin.QueryRowContext(ctx, `SELECT wallet_cash_service_units
			FROM invoice_bridge.sub2api_payments_v4('page',
			'{"cutover":"2000-01-01T00:00:00Z","position_at":"2000-01-01T00:00:00Z","position_id":0,"ceiling_at":"2100-01-01T00:00:00Z","ceiling_id":6,"limit":10}'::jsonb) bridge(payload)
			CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(source_id bigint,wallet_cash_service_units text)
			WHERE source_id=$1`, expectation.id).Scan(&walletUnits); err != nil || walletUnits.Valid != expectation.valid ||
			(expectation.valid && walletUnits.String != expectation.units) {
			t.Fatalf("per-order CNY fee proof id=%d units=%#v err=%v", expectation.id, walletUnits, err)
		}
	}
	_, _ = admin.ExecContext(ctx, `DELETE FROM public.payment_orders WHERE id IN (5,6)`)

	cutoverReader := openBridgeReader(t, configuration, sourceagent.SourceSub2API, sourceagent.StreamBalances)
	defer cutoverReader.Close()
	directory := t.TempDir()
	manifestStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "manifest.enc"), Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: writeBridgeTestKey(t, directory, "cutover.key")}}
	snapshotStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "baseline.enc"), Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: writeBridgeTestKey(t, directory, "snapshot.key")}}
	captured, err := sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{
		SourceID: "10000000-0000-4000-8000-000000000001", SourceType: sourceagent.SourceSub2API,
		SourceRuntime: "0.1.179", SigningKeyID: "key-1",
		EligibilityStartAt: time.Now().UTC().Add(time.Hour), Manifest: manifestStore, Snapshot: snapshotStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, err := sourceagent.LoadAndCheckCutover(ctx, manifestStore, snapshotStore, captured.SourceID, captured.SourceType, captured.SourceRuntime)
	if err != nil || len(baseline.Rows) != 2 || !baseline.Rows[1].BalanceNegative {
		t.Fatalf("encrypted cutover baseline=%#v err=%v", baseline.Rows, err)
	}
	for _, stream := range []string{sourceagent.StreamPayments, sourceagent.StreamUsage, sourceagent.StreamCredits, sourceagent.StreamBalances} {
		reader := openBridgeReader(t, configuration, sourceagent.SourceSub2API, stream)
		checkErr := sourceagent.CheckLiveEconomicContract(ctx, reader, sourceagent.SourceSub2API, stream, captured)
		_ = reader.Close()
		if checkErr != nil {
			t.Fatalf("live V3 check-db contract rejected stream=%s captured configuration: %v", stream, checkErr)
		}
	}
	if _, err = admin.ExecContext(ctx, `DELETE FROM public.payment_orders WHERE id=4`); err != nil {
		t.Fatal(err)
	}
	var lastVisiblePaymentAt time.Time
	if err = admin.QueryRowContext(ctx, `SELECT updated_at FROM public.payment_orders WHERE id=1`).Scan(&lastVisiblePaymentAt); err != nil {
		t.Fatal(err)
	}
	idleManifest := captured
	idleCutover := lastVisiblePaymentAt.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	idleManifest.CutoverAt = idleCutover
	idleManifest.DatabaseClock = idleCutover
	idleManifest.HighWaters = make(map[string]sourceagent.SourceHighWater, len(captured.HighWaters))
	for stream, highWater := range captured.HighWaters {
		highWater.EventTime = idleCutover
		idleManifest.HighWaters[stream] = highWater
	}
	idleManifest.ManifestHash = bridgeManifestHash(t, idleManifest)
	idleCursor := sourceagent.ScanCursor{
		Revision: 1, Version: 2, CutoverAt: idleCutover,
		WatermarkAt: lastVisiblePaymentAt.UTC().Format(time.RFC3339Nano), WatermarkCursor: "payment_orders:1",
		CeilingAt: lastVisiblePaymentAt.UTC().Format(time.RFC3339Nano), CeilingCursor: "payment_orders:1",
		PositionCursor: "payment_orders:1", UpdatedAt: lastVisiblePaymentAt.UTC().Format(time.RFC3339Nano), ID: 1, Completed: true,
	}
	payments := &sourceagent.PaymentV3DBConnector{DB: admin, Source: sourceagent.SourceSub2API, Manifest: idleManifest, SafetyDelay: time.Minute}
	var firstHorizonLower time.Time
	if err = admin.QueryRowContext(ctx, `SELECT transaction_timestamp()-interval '1 minute'`).Scan(&firstHorizonLower); err != nil {
		t.Fatal(err)
	}
	firstEmpty, err := payments.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanIncremental, Cursor: idleCursor, Limit: 10})
	if err != nil || !firstEmpty.ScanComplete || len(firstEmpty.Projections) != 0 {
		t.Fatalf("first Sub2API empty payment cycle page=%#v err=%v", firstEmpty, err)
	}
	var firstHorizonUpper time.Time
	if err = admin.QueryRowContext(ctx, `SELECT transaction_timestamp()-interval '1 minute'`).Scan(&firstHorizonUpper); err != nil {
		t.Fatal(err)
	}
	firstCeiling, err := time.Parse(time.RFC3339Nano, firstEmpty.ScanCeilingAt)
	if err != nil || firstCeiling.Before(firstHorizonLower) || firstCeiling.After(firstHorizonUpper) ||
		!firstCeiling.After(lastVisiblePaymentAt) || firstEmpty.ScanCeilingCursor != "payment_orders:1" ||
		firstEmpty.StreamWatermarkAt != firstEmpty.ScanCeilingAt {
		t.Fatalf("first idle cycle did not publish its source transaction horizon lower=%s ceiling=%s upper=%s watermark=%s err=%v",
			firstHorizonLower, firstEmpty.ScanCeilingAt, firstHorizonUpper, firstEmpty.StreamWatermarkAt, err)
	}
	committed := firstEmpty.NextCursor
	committed.Revision++ // Simulate the coordinator's successful cursor CAS.
	time.Sleep(10 * time.Millisecond)
	secondEmpty, err := payments.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanIncremental, Cursor: committed, Limit: 10})
	if err != nil || !secondEmpty.ScanComplete || len(secondEmpty.Projections) != 0 {
		t.Fatalf("second Sub2API empty payment cycle page=%#v err=%v", secondEmpty, err)
	}
	secondCeiling, err := time.Parse(time.RFC3339Nano, secondEmpty.ScanCeilingAt)
	if err != nil || !secondCeiling.After(firstCeiling) || firstEmpty.ScanCeilingCursor != secondEmpty.ScanCeilingCursor || secondEmpty.StreamWatermarkAt != secondEmpty.ScanCeilingAt {
		t.Fatalf("idle cycle did not advance the proven horizon while retaining its row ceiling first=%s/%s second=%s/%s err=%v",
			firstEmpty.ScanCeilingAt, firstEmpty.ScanCeilingCursor, secondEmpty.ScanCeilingAt, secondEmpty.ScanCeilingCursor, err)
	}
	if firstEmpty.ScanCycleID == secondEmpty.ScanCycleID {
		t.Fatal("successive committed Sub2API empty cycles reused the same scan cycle ID")
	}
	if _, err = sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{
		SourceID: captured.SourceID, SourceType: captured.SourceType, SourceRuntime: captured.SourceRuntime,
		SigningKeyID: "key-1", EligibilityStartAt: time.Now().UTC().Add(time.Hour),
		Manifest: manifestStore, Snapshot: snapshotStore,
	}); err == nil {
		t.Fatal("cutover state was overwritten")
	}
}

func TestNewAPIBridgePreservesTransitionSafetyAndConfigurationDrift(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	configuration, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*configuration)
	defer admin.Close()
	cleanupBridgeFixture(t, ctx, admin)
	defer cleanupBridgeFixture(t, ctx, admin)
	setupBridgeFixture(t, ctx, admin, sourceagent.SourceNewAPI)
	applyBridgeContracts(t, ctx, admin, sourceagent.SourceNewAPI)

	var hash string
	if err = admin.QueryRowContext(ctx, `SELECT configuration_hash
		FROM invoice_bridge.newapi_payments_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(configuration_hash text)`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.options SET value=CASE key
		WHEN 'QuotaPerUnit' THEN '500000.000' WHEN 'Price' THEN '1.0000'
		ELSE '{ "vip": 1.0, "default": 1, "new-group": 1.000 }' END
		WHERE key IN ('QuotaPerUnit','Price','TopupGroupRatio')`); err != nil {
		t.Fatal(err)
	}
	var equivalentHash string
	if err = admin.QueryRowContext(ctx, `SELECT configuration_hash
		FROM invoice_bridge.newapi_payments_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(configuration_hash text)`).Scan(&equivalentHash); err != nil || equivalentHash != hash {
		t.Fatalf("equivalent New API numeric/group configuration changed semantic hash before=%q after=%q err=%v", hash, equivalentHash, err)
	}
	cutover := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	manifest := sourceagent.CutoverManifest{SchemaVersion: 1,
		SourceID: "10000000-0000-4000-8000-000000000002", SourceType: sourceagent.SourceNewAPI,
		SourceRuntime: "v1.0.0-rc.25", CutoverAt: cutover.Format(time.RFC3339Nano), DatabaseClock: cutover.Format(time.RFC3339Nano),
		ProjectionContract: "newapi-economic-rc25-v4", ConfigurationHash: hash, UnitCode: "NEWAPI_QUOTA",
		SigningKeyID: "key-1", BaselineSnapshotID: strings.Repeat("b", 64), BaselineRowCount: "2",
		HighWaters: map[string]sourceagent.SourceHighWater{
			sourceagent.StreamPayments: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "subscription_orders:1;top_ups:4"},
			sourceagent.StreamUsage:    {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "logs:1"},
			sourceagent.StreamCredits:  {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "checkins:0;redemptions:5"},
			sourceagent.StreamBalances: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "balance_snapshot:0"},
		}}
	manifest.ManifestHash = bridgeManifestHash(t, manifest)
	for _, stream := range []string{sourceagent.StreamPayments, sourceagent.StreamUsage, sourceagent.StreamCredits, sourceagent.StreamBalances} {
		reader := openBridgeReader(t, configuration, sourceagent.SourceNewAPI, stream)
		checkErr := sourceagent.CheckLiveEconomicContract(ctx, reader, sourceagent.SourceNewAPI, stream, manifest)
		_ = reader.Close()
		if checkErr != nil {
			t.Fatalf("live V3 check-db contract rejected stream=%s equivalent configuration: %v", stream, checkErr)
		}
	}

	if _, err = admin.ExecContext(ctx, `UPDATE public.redemptions SET status=3,used_user_id=1,redeemed_time=extract(epoch from now()-interval '5 minutes')::bigint WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	credits := &sourceagent.EconomicDBConnector{DB: admin, Source: sourceagent.SourceNewAPI, Stream: sourceagent.StreamCredits, Manifest: manifest, SafetyDelay: time.Minute}
	creditPage, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 1})
	if err != nil || len(creditPage.Projections) != 1 || creditPage.Projections[0].ExternalID != "redemptions:5" {
		t.Fatalf("old-ID redemption transition/safety horizon projections=%#v err=%v", creditPage.Projections, err)
	}
	creditFinal, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: creditPage.NextCursor, Limit: 1})
	if err != nil || creditFinal.HasMore || !creditFinal.ScanComplete || len(creditFinal.Projections) != 0 {
		t.Fatalf("exact-limit completion page=%#v err=%v", creditFinal, err)
	}

	if _, err = admin.ExecContext(ctx, `UPDATE public.top_ups SET status='success',complete_time=extract(epoch from now()-interval '5 minutes')::bigint WHERE id=4`); err != nil {
		t.Fatal(err)
	}
	payments := &sourceagent.PaymentV3DBConnector{DB: admin, Source: sourceagent.SourceNewAPI, Manifest: manifest, SafetyDelay: time.Minute}
	foundOld, foundFailed, foundUnknownPending := false, false, false
	cursor := sourceagent.ScanCursor{}
	cycleID := ""
	for pages := 0; pages < 10; pages++ {
		page, scanErr := payments.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: cursor, Limit: 2})
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		for _, projection := range page.Projections {
			foundOld = foundOld || projection.ExternalID == "4"
			foundFailed = foundFailed || projection.ExternalID == "2"
			if projection.ExternalID == "3" {
				raw, _ := json.Marshal(projection.Payload)
				var payload sourceagent.PaymentCandidatePayload
				_ = json.Unmarshal(raw, &payload)
				foundUnknownPending = payload.WalletCashServiceUnits == nil && payload.VerificationState == sourceagent.VerificationPendingManual
			}
		}
		if cycleID == "" {
			cycleID = page.ScanCycleID
		} else if page.ScanCycleID != cycleID {
			t.Fatalf("New API multi-page payment cycle changed ID old=%q new=%q", cycleID, page.ScanCycleID)
		}
		cursor = page.NextCursor
		cursor.Revision++ // Simulate the coordinator CAS between pages.
		if !page.HasMore {
			break
		}
	}
	if !foundOld || foundFailed || !foundUnknownPending {
		t.Fatalf("New API payment safety old=%t failed=%t unknown_pending=%t", foundOld, foundFailed, foundUnknownPending)
	}

	var balanceUnits string
	var balanceNegative bool
	if err = admin.QueryRowContext(ctx, `SELECT balance_service_units,balance_negative
		FROM invoice_bridge.newapi_balances_v4('rows','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(user_id bigint,balance_service_units text,balance_negative boolean)
		WHERE user_id=2`).Scan(&balanceUnits, &balanceNegative); err != nil || balanceUnits != "0" || !balanceNegative {
		t.Fatalf("New API negative balance units=%q negative=%t err=%v", balanceUnits, balanceNegative, err)
	}

	if _, err = admin.ExecContext(ctx, `UPDATE public.options SET value='{"default":1.5}' WHERE key='TopupGroupRatio'`); err != nil {
		t.Fatal(err)
	}
	if liveErr := sourceagent.CheckLiveEconomicContract(ctx, admin, sourceagent.SourceNewAPI, sourceagent.StreamPayments, manifest); liveErr == nil {
		t.Fatal("live V3 check-db contract accepted real ratio drift")
	}
	drift, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 10})
	if err != nil || !drift.ReconcileBlocked || drift.NextCursor.WatermarkAt != manifest.CutoverAt {
		t.Fatalf("configuration drift advanced watermark page=%#v err=%v", drift, err)
	}
	_, _ = admin.ExecContext(ctx, `UPDATE public.options SET value='{"default":1}' WHERE key='TopupGroupRatio'`)
	_, invalidJSONErr := admin.ExecContext(ctx, `UPDATE public.options SET value='not-json' WHERE key='TopupGroupRatio'`)
	if invalidJSONErr != nil {
		t.Fatal(invalidJSONErr)
	}
	var invalidHealthy bool
	if queryErr := admin.QueryRowContext(ctx, `SELECT contract_ok
		FROM invoice_bridge.newapi_payments_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(contract_ok boolean)`).Scan(&invalidHealthy); queryErr == nil {
		t.Fatal("invalid New API group-ratio JSON did not fail closed")
	}
	_, _ = admin.ExecContext(ctx, `UPDATE public.options SET value='{"default":1}' WHERE key='TopupGroupRatio'`)
	_, _ = admin.ExecContext(ctx, `UPDATE public.options SET value='false' WHERE key='LogConsumeEnabled'`)
	usage := &sourceagent.EconomicDBConnector{DB: admin, Source: sourceagent.SourceNewAPI, Stream: sourceagent.StreamUsage, Manifest: manifest, SafetyDelay: time.Minute}
	blocked, err := usage.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 10})
	if err != nil || !blocked.ReconcileBlocked || len(blocked.Warnings) != 1 || blocked.Warnings[0] != "consume_logging_disabled" {
		t.Fatalf("disabled consume logging did not fail closed page=%#v err=%v", blocked, err)
	}
}

// TestSub2APIUsageReconcileRollingWindowAndCreditsSemanticsPreserved is the
// end-to-end (real bridge SQL) proof for XM-INV-AGENT-RESTART-GRACE part B:
// a completed ScanReconcile cycle's baseline, not the cutover manifest, is
// where the next reconcile starts; a legacy in-flight cycle left by a
// pre-upgrade binary is abandoned and restarted from that same baseline
// instead of resumed; and the credits stream's pre-existing "restart at zero
// every cycle" behavior is completely unaffected.
func TestSub2APIUsageReconcileRollingWindowAndCreditsSemanticsPreserved(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	configuration, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*configuration)
	defer admin.Close()
	cleanupBridgeFixture(t, ctx, admin)
	defer cleanupBridgeFixture(t, ctx, admin)
	setupBridgeFixture(t, ctx, admin, sourceagent.SourceSub2API)
	applyBridgeContracts(t, ctx, admin, sourceagent.SourceSub2API)

	var hash string
	if err = readSub2HealthHash(ctx, admin).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	cutover := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	manifest := sourceagent.CutoverManifest{SchemaVersion: 1,
		SourceID: "10000000-0000-4000-8000-000000000009", SourceType: sourceagent.SourceSub2API,
		SourceRuntime: "0.1.179", CutoverAt: cutover.Format(time.RFC3339Nano), DatabaseClock: cutover.Format(time.RFC3339Nano),
		ProjectionContract: sourceagent.ProjectionContractSub2APIV4, ConfigurationHash: hash, UnitCode: "SUB2_BALANCE_1E8",
		SigningKeyID: "key-1", BaselineSnapshotID: strings.Repeat("b", 64), BaselineRowCount: "2",
		HighWaters: map[string]sourceagent.SourceHighWater{
			sourceagent.StreamPayments: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "payment_orders:0"},
			sourceagent.StreamUsage:    {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "usage_logs:0"},
			sourceagent.StreamCredits:  {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "promo_code_usages:0;user_affiliate_ledger:0;redeem_codes:0"},
			sourceagent.StreamBalances: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "balance_snapshot:0"},
		}}
	manifest.ManifestHash = bridgeManifestHash(t, manifest)

	usage := &sourceagent.EconomicDBConnector{DB: admin, Source: sourceagent.SourceSub2API, Stream: sourceagent.StreamUsage, Manifest: manifest, SafetyDelay: time.Minute}

	// Cycle 1: the very first ScanReconcile ever still rewinds to the cutover
	// manifest and picks up the pre-existing fixture row (usage_logs id=1).
	first, err := usage.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanReconcile, Limit: 10})
	if err != nil || first.HasMore || !first.ScanComplete || len(first.Projections) != 1 ||
		first.NextCursor.PositionCursor != "usage_logs:1" {
		t.Fatalf("first reconcile cycle page=%#v cursor=%#v err=%v", first, first.NextCursor, err)
	}
	if !first.NextCursor.ReconcileWindowBounded || first.NextCursor.ReconcileBaselineCursor != first.NextCursor.WatermarkCursor {
		t.Fatalf("completed reconcile did not record a rolling-window baseline: %#v", first.NextCursor)
	}
	committed := first.NextCursor
	committed.Revision++

	// A second row appears after cycle 1 committed. The bug this fixes would
	// rewind cycle 2 all the way back to the cutover manifest and re-emit
	// usage_logs id=1 too; the fix must emit only the new row.
	if _, err = admin.ExecContext(ctx, `INSERT INTO public.usage_logs VALUES(2,1,0,0.00000001,now()-interval '9 minutes','secret','192.0.2.2')`); err != nil {
		t.Fatal(err)
	}
	second, err := usage.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanReconcile, Cursor: committed, Limit: 10})
	if err != nil || !second.ScanComplete || len(second.Projections) != 1 || second.Projections[0].ExternalID != "usage_logs:2" {
		t.Fatalf("second reconcile did not start from the rolling-window baseline (rewound to cutover instead): page=%#v err=%v", second, err)
	}

	// Simulate a legacy in-flight cycle left on disk by a pre-upgrade binary:
	// rewound to the cutover manifest, Completed=false, and -- because it
	// predates this field -- ReconcileWindowBounded is unset.
	legacyInFlight := second.NextCursor
	legacyInFlight.Revision++
	legacyInFlight.Completed = false
	legacyInFlight.ReconcileWindowBounded = false
	legacyInFlight.PositionCursor = "usage_logs:0"
	abandoned, err := usage.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanReconcile, Cursor: legacyInFlight, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(abandoned.Warnings) != 1 || abandoned.Warnings[0] != "legacy_reconcile_cycle_abandoned" {
		t.Fatalf("legacy in-flight cycle was not flagged as abandoned: %#v", abandoned)
	}
	if !abandoned.ScanComplete || len(abandoned.Projections) != 0 {
		// Both usage rows (ids 1 and 2) are already at or behind the rolling
		// window baseline, so restarting from the baseline finds nothing new.
		// Rewinding to the cutover manifest instead would have re-emitted both.
		t.Fatalf("abandoned legacy cycle did not restart from the rolling-window baseline: %#v", abandoned)
	}
	if !abandoned.NextCursor.ReconcileWindowBounded {
		t.Fatal("abandoned legacy cycle was not marked bounded for future restarts")
	}

	// Credits semantics are completely untouched by this change: every new
	// cycle restarts at position zero regardless of mode, so a second
	// ScanReconcile still re-scans and re-emits the same pre-existing rows
	// (the bonus-backed promo/affiliate credits; the cash-backed redeem is
	// excluded by the bridge's own financial-safety filter).
	credits := &sourceagent.EconomicDBConnector{DB: admin, Source: sourceagent.SourceSub2API, Stream: sourceagent.StreamCredits, Manifest: manifest, SafetyDelay: time.Minute}
	firstCredits, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanReconcile, Limit: 10})
	if err != nil || !firstCredits.ScanComplete || len(firstCredits.Projections) != 2 {
		t.Fatalf("first credits reconcile page=%#v err=%v", firstCredits, err)
	}
	committedCredits := firstCredits.NextCursor
	committedCredits.Revision++
	secondCredits, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanReconcile, Cursor: committedCredits, Limit: 10})
	if err != nil || !secondCredits.ScanComplete || len(secondCredits.Projections) != 2 {
		t.Fatalf("credits reconcile must still re-scan from zero every cycle (unchanged semantics): page=%#v err=%v", secondCredits, err)
	}
}

func readSub2HealthHash(ctx context.Context, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) *sql.Row {
	return database.QueryRowContext(ctx, `SELECT configuration_hash
		FROM invoice_bridge.sub2api_usage_v4('health','{}'::jsonb) bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) projected(configuration_hash text)`)
}

func writeBridgeTestKey(t *testing.T, directory, name string) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bridgeManifestHash(t *testing.T, value sourceagent.CutoverManifest) string {
	t.Helper()
	payload := value.Payload()
	payload.ManifestHash = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
