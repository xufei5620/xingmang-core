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

	if _, err = admin.ExecContext(ctx, `INSERT INTO public.payment_orders VALUES
		(2,1,'COMPLETED','balance',10,10,0,now(),NULL,now(),now(),'stripe','stripe','{"schema_version":"2","provider_key":"stripe","currency":"USD"}',NULL),
		(3,1,'COMPLETED','balance',10,10,0,now(),NULL,now(),now(),'unknown','unknown','{}',NULL),
		(4,1,'COMPLETED','balance',10,10,0,now(),NULL,now(),now(),'epay','easypay','{"schema_version":"2","provider_key":"easypay","currency":"invalid"}',NULL)`); err != nil {
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

	cutoverReader := openBridgeReader(t, configuration, sourceagent.SourceSub2API, sourceagent.StreamBalances)
	defer cutoverReader.Close()
	directory := t.TempDir()
	manifestStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "manifest.enc"), Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: writeBridgeTestKey(t, directory, "cutover.key")}}
	snapshotStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "baseline.enc"), Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: writeBridgeTestKey(t, directory, "snapshot.key")}}
	captured, err := sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{
		SourceID: "10000000-0000-4000-8000-000000000001", SourceType: sourceagent.SourceSub2API,
		SourceRuntime: "0.1.179", SigningKeyID: "key-1", Manifest: manifestStore, Snapshot: snapshotStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, err := sourceagent.LoadAndCheckCutover(ctx, manifestStore, snapshotStore, captured.SourceID, captured.SourceType, captured.SourceRuntime)
	if err != nil || len(baseline.Rows) != 2 || !baseline.Rows[1].BalanceNegative {
		t.Fatalf("encrypted cutover baseline=%#v err=%v", baseline.Rows, err)
	}
	if _, err = sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{
		SourceID: captured.SourceID, SourceType: captured.SourceType, SourceRuntime: captured.SourceRuntime,
		SigningKeyID: "key-1", Manifest: manifestStore, Snapshot: snapshotStore,
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
	cutover := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	manifest := sourceagent.CutoverManifest{SchemaVersion: 1,
		SourceID: "10000000-0000-4000-8000-000000000002", SourceType: sourceagent.SourceNewAPI,
		SourceRuntime: "v1.0.0-rc.25", CutoverAt: cutover.Format(time.RFC3339Nano), DatabaseClock: cutover.Format(time.RFC3339Nano),
		ProjectionContract: "newapi-economic-rc25-v3", ConfigurationHash: hash, UnitCode: "NEWAPI_QUOTA",
		SigningKeyID: "key-1", BaselineSnapshotID: strings.Repeat("b", 64), BaselineRowCount: "2",
		HighWaters: map[string]sourceagent.SourceHighWater{
			sourceagent.StreamPayments: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "subscription_orders:1;top_ups:4"},
			sourceagent.StreamUsage:    {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "logs:1"},
			sourceagent.StreamCredits:  {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "checkins:0;redemptions:5"},
			sourceagent.StreamBalances: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "balance_snapshot:0"},
		}}
	manifest.ManifestHash = bridgeManifestHash(t, manifest)

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
		cursor = page.NextCursor
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
	drift, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 10})
	if err != nil || !drift.ReconcileBlocked || drift.NextCursor.WatermarkAt != manifest.CutoverAt {
		t.Fatalf("configuration drift advanced watermark page=%#v err=%v", drift, err)
	}
	_, _ = admin.ExecContext(ctx, `UPDATE public.options SET value='{"default":1}' WHERE key='TopupGroupRatio'`)
	_, _ = admin.ExecContext(ctx, `UPDATE public.options SET value='false' WHERE key='LogConsumeEnabled'`)
	usage := &sourceagent.EconomicDBConnector{DB: admin, Source: sourceagent.SourceNewAPI, Stream: sourceagent.StreamUsage, Manifest: manifest, SafetyDelay: time.Minute}
	blocked, err := usage.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 10})
	if err != nil || !blocked.ReconcileBlocked || len(blocked.Warnings) != 1 || blocked.Warnings[0] != "consume_logging_disabled" {
		t.Fatalf("disabled consume logging did not fail closed page=%#v err=%v", blocked, err)
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
