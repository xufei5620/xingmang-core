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
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"invoice-system/agents/sourceagent"
)

func TestEconomicProjectionContractsAgainstPostgres(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configuration, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*configuration)
	defer admin.Close()
	if err = pingIntegrationDatabase(ctx, admin); err != nil {
		t.Fatal(err)
	}
	cleanupEconomicContract(t, admin)
	defer cleanupEconomicContract(t, admin)
	database := pgx.Identifier{configuration.Database}.Sanitize()
	if _, err = admin.ExecContext(ctx, `REVOKE TEMPORARY ON DATABASE `+database+` FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"invoice_sub2api_payments_v3_reader", "invoice_sub2api_usage_reader", "invoice_sub2api_credits_reader", "invoice_sub2api_balances_reader"} {
		if _, err = admin.ExecContext(ctx, `CREATE ROLE `+pgx.Identifier{role}.Sanitize()+` LOGIN PASSWORD 'economic_test_password'`); err != nil {
			t.Fatal(err)
		}
	}
	statements := []string{
		`CREATE TABLE public.settings(key text primary key,value text not null,updated_at timestamptz not null)`,
		`CREATE TABLE public.users(id bigint primary key,balance numeric(20,8),deleted_at timestamptz,email text,password_hash text)`,
		`CREATE TABLE public.usage_logs(id bigint primary key,user_id bigint,billing_type smallint,actual_cost numeric(20,10),created_at timestamptz,content text,ip_address text)`,
		`CREATE TABLE public.promo_code_usages(id bigint primary key,user_id bigint,bonus_amount numeric(20,8),used_at timestamptz)`,
		`CREATE TABLE public.user_affiliate_ledger(id bigint primary key,user_id bigint,action text,amount numeric(20,8),created_at timestamptz)`,
		`CREATE TABLE public.redeem_codes(id bigint primary key,code text,type text,value numeric(20,8),status text,used_by bigint,used_at timestamptz)`,
		`CREATE TABLE public.payment_orders(id bigint primary key,user_id bigint,status text,order_type text,amount numeric(20,8),pay_amount numeric(20,8),refund_amount numeric(20,8),completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text,provider_snapshot jsonb,recharge_code text)`,
		`CREATE VIEW public.invoice_sub2api_payment_projection_v1 WITH(security_barrier=true) AS SELECT id,user_id,status,order_type,amount,pay_amount,refund_amount,'CNY'::text currency,completed_at,refund_at,created_at,updated_at,payment_type,provider_key FROM public.payment_orders`,
		`CREATE VIEW public.invoice_sub2api_payment_projection_health_v1 WITH(security_barrier=true) AS SELECT count(*)::bigint total_rows,count(*)::bigint exposed_cny_rows,0::bigint unsupported_known_non_cny_rows,0::bigint blocked_unknown_currency_rows FROM public.payment_orders`,
		`INSERT INTO public.settings VALUES('BALANCE_RECHARGE_MULTIPLIER','1.00',now()-interval '1 day'),('RECHARGE_FEE_RATE','0.00',now()-interval '1 day')`,
		`INSERT INTO public.users VALUES(1,10.00000000,NULL,'hidden@example.com','secret'),(2,-2.50000000,NULL,'negative@example.com','secret')`,
		`INSERT INTO public.usage_logs VALUES(1,1,0,0.000000005,now()-interval '10 minutes','secret content','192.0.2.1')`,
		`INSERT INTO public.redeem_codes VALUES
			(1,'CASHCODE','balance',10,'used',1,now()-interval '10 minutes'),
			(2,'ADMINNEG','admin_balance',-4,'used',1,now()-interval '10 minutes'),
			(3,'CONCURRENCYNEG','admin_concurrency',-2,'used',1,now()-interval '10 minutes'),
			(99992744,'BONUSCODE','balance',3,'used',1,now()-interval '9 minutes')`,
		`INSERT INTO public.payment_orders VALUES(1,1,'COMPLETED','balance',10,10,0,now()-interval '8 minutes',NULL,now()-interval '9 minutes',now()-interval '8 minutes','epay','easypay','{}','CASHCODE')`,
	}
	for _, statement := range statements {
		if _, err = admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare Sub2 fixture: %v\n%s", err, statement)
		}
	}
	sub2Contract := readContractFile(t, "sub2api-economic-projection-grants.postgresql.sql")
	assertEconomicContractRejectsMembership(t, ctx, admin, sub2Contract, "invoice_sub2api_usage_reader")
	if _, err = admin.ExecContext(ctx, sub2Contract); err != nil {
		t.Fatalf("apply Sub2 V3 contract: %v", err)
	}
	var units string
	if err = admin.QueryRowContext(ctx, `SELECT service_units FROM public.invoice_sub2api_usage_projection_v3 WHERE source_id=1`).Scan(&units); err != nil || units != "1" {
		t.Fatalf("numeric half boundary units=%q err=%v", units, err)
	}
	var negativeUnits string
	var negative bool
	if err = admin.QueryRowContext(ctx, `SELECT balance_service_units,balance_negative FROM public.invoice_sub2api_balance_projection_v3 WHERE user_id=2`).Scan(&negativeUnits, &negative); err != nil || negativeUnits != "0" || !negative {
		t.Fatalf("negative balance was not explicit units=%q negative=%t err=%v", negativeUnits, negative, err)
	}
	var creditCount int
	if err = admin.QueryRowContext(ctx, `SELECT count(*) FROM public.invoice_sub2api_credits_projection_v3`).Scan(&creditCount); err != nil || creditCount != 1 {
		t.Fatalf("cash fulfillment redeem leaked as bonus count=%d err=%v", creditCount, err)
	}
	var healthy bool
	var gaps int64
	if err = admin.QueryRowContext(ctx, `SELECT contract_ok,gap_count FROM public.invoice_sub2api_credits_projection_health_v3`).Scan(&healthy, &gaps); err != nil || !healthy || gaps == 0 {
		t.Fatalf("legitimate sequence gap blocked contract healthy=%t gaps=%d err=%v", healthy, gaps, err)
	}
	if _, err = admin.ExecContext(ctx, `INSERT INTO public.redeem_codes VALUES(4,'INVALIDBALANCE','balance',-1,'used',1,now()-interval '8 minutes')`); err != nil {
		t.Fatal(err)
	}
	var blockedReason string
	if err = admin.QueryRowContext(ctx, `SELECT contract_ok,blocked_reason FROM public.invoice_sub2api_credits_projection_health_v3`).Scan(&healthy, &blockedReason); err != nil || healthy || blockedReason != "credit_contract_invalid" {
		t.Fatalf("negative projected balance credit did not fail closed healthy=%t reason=%q err=%v", healthy, blockedReason, err)
	}
	if _, err = admin.ExecContext(ctx, `DELETE FROM public.redeem_codes WHERE id=4`); err != nil {
		t.Fatal(err)
	}
	var utcHash, tokyoHash string
	if _, err = admin.ExecContext(ctx, `SET TIME ZONE 'UTC'`); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRowContext(ctx, `SELECT configuration_hash FROM public.invoice_sub2api_usage_projection_health_v3`).Scan(&utcHash); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, `SET TIME ZONE 'Asia/Tokyo'`); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRowContext(ctx, `SELECT configuration_hash FROM public.invoice_sub2api_usage_projection_health_v3`).Scan(&tokyoHash); err != nil || utcHash != tokyoHash {
		t.Fatalf("configuration hash changed with timezone utc=%q tokyo=%q err=%v", utcHash, tokyoHash, err)
	}
	// Cutover is captured through the balances-only role in one RR/RO source
	// transaction, persisted encrypted, and cannot be overwritten.
	cutoverReaderConfig := *configuration
	cutoverReaderConfig.User = "invoice_sub2api_balances_reader"
	cutoverReaderConfig.Password = "economic_test_password"
	cutoverReader := stdlib.OpenDB(cutoverReaderConfig)
	defer cutoverReader.Close()
	if err = pingIntegrationDatabase(ctx, cutoverReader); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	writeKey := func(name string) string {
		path := filepath.Join(directory, name)
		key := make([]byte, 32)
		if _, keyErr := rand.Read(key); keyErr != nil {
			t.Fatal(keyErr)
		}
		if keyErr := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); keyErr != nil {
			t.Fatal(keyErr)
		}
		return path
	}
	manifestStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "manifest.enc"), Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: writeKey("cutover.key")}}
	snapshotStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "baseline.enc"), Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: writeKey("snapshot.key")}}
	captured, err := sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{SourceID: "10000000-0000-4000-8000-000000000001", SourceType: sourceagent.SourceSub2API, SourceRuntime: "0.1.179", SigningKeyID: "key-1", Manifest: manifestStore, Snapshot: snapshotStore})
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, err := sourceagent.LoadAndCheckCutover(ctx, manifestStore, snapshotStore, captured.SourceID, captured.SourceType, captured.SourceRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Rows) != 2 || !baseline.Rows[0].BaselineMember || !baseline.Rows[1].BaselineMember || !baseline.Rows[1].BalanceNegative {
		t.Fatalf("atomic cutover baseline mismatch: %#v", baseline.Rows)
	}
	balances := &sourceagent.BalanceDBConnector{DB: cutoverReader, Source: sourceagent.SourceSub2API, SourceID: captured.SourceID, Manifest: captured, Baseline: snapshotStore, Current: sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "current.enc"), Purpose: "balance_snapshot", Keys: snapshotStore.Keys}}
	balanceCursor := sourceagent.ScanCursor{}
	for pageIndex := 0; pageIndex < 3; pageIndex++ {
		balancePage, scanErr := balances.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: balanceCursor, Limit: 1})
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if pageIndex == 0 && (len(balancePage.Projections) != 1 || balancePage.Projections[0].EntityType != sourceagent.EntityCutoverManifest || balancePage.ScanComplete) {
			t.Fatalf("first balance page was not manifest-only: %#v", balancePage)
		}
		if balancePage.ScanSnapshotID != baseline.SnapshotID || balancePage.ScanSnapshotRowCount == nil || *balancePage.ScanSnapshotRowCount != 2 {
			t.Fatalf("balance snapshot transport metadata drifted on page %d: %#v", pageIndex, balancePage)
		}
		balanceCursor = balancePage.NextCursor
		if pageIndex < 2 && !balancePage.HasMore {
			t.Fatal("balance baseline ended before all pages")
		}
		if pageIndex == 2 && (balancePage.HasMore || !balancePage.ScanComplete || balancePage.NextCursor.WatermarkAt != baseline.AsOf) {
			t.Fatalf("balance final page did not publish snapshot: %#v", balancePage)
		}
	}
	if _, err = admin.ExecContext(ctx, `INSERT INTO public.users VALUES(3,1.00000000,NULL,'new@example.com','secret')`); err != nil {
		t.Fatal(err)
	}
	reconciliation, err := balances.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: balanceCursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if reconciliation.ScanSnapshotID == baseline.SnapshotID || reconciliation.ScanSnapshotRowCount == nil || *reconciliation.ScanSnapshotRowCount != 3 {
		t.Fatalf("reconciliation snapshot transport metadata invalid: %#v", reconciliation)
	}
	membership := map[string]bool{}
	for _, projection := range reconciliation.Projections {
		raw, _ := json.Marshal(projection.Payload)
		var payload sourceagent.BalanceCheckpointPayload
		if json.Unmarshal(raw, &payload) != nil {
			t.Fatal("decode reconciliation checkpoint")
		}
		membership[payload.ExternalUserID] = payload.BaselineMember
	}
	if !membership["1"] || !membership["2"] || membership["3"] {
		t.Fatalf("baseline membership proof drifted: %#v", membership)
	}
	if _, err = admin.ExecContext(ctx, `DELETE FROM public.users`); err != nil {
		t.Fatal(err)
	}
	emptyManifestStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "empty-manifest.enc"), Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: writeKey("empty-cutover.key")}}
	emptySnapshotStore := sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "empty-baseline.enc"), Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: writeKey("empty-snapshot.key")}}
	emptyManifest, err := sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{SourceID: "10000000-0000-4000-8000-000000000009", SourceType: sourceagent.SourceSub2API, SourceRuntime: "0.1.179", SigningKeyID: "key-1", Manifest: emptyManifestStore, Snapshot: emptySnapshotStore})
	if err != nil {
		t.Fatal(err)
	}
	emptyConnector := &sourceagent.BalanceDBConnector{DB: cutoverReader, Source: sourceagent.SourceSub2API, SourceID: emptyManifest.SourceID, Manifest: emptyManifest, Baseline: emptySnapshotStore, Current: sourceagent.EncryptedStateFile{Path: filepath.Join(directory, "empty-current.enc"), Purpose: "balance_snapshot", Keys: emptySnapshotStore.Keys}}
	emptyFirst, err := emptyConnector.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if emptyFirst.ScanSnapshotRowCount == nil || *emptyFirst.ScanSnapshotRowCount != 0 {
		t.Fatal("empty snapshot omitted explicit transport row count")
	}
	if len(emptyFirst.Projections) != 1 || emptyFirst.Projections[0].EntityType != sourceagent.EntityCutoverManifest || !emptyFirst.HasMore || emptyFirst.ScanComplete {
		t.Fatal("zero-user cutover did not emit manifest-only pending page")
	}
	emptyFinal, err := emptyConnector.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: emptyFirst.NextCursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyFinal.Projections) != 0 || emptyFinal.HasMore || !emptyFinal.ScanComplete {
		t.Fatal("zero-user cutover did not emit an empty completing page")
	}
	if _, err = sourceagent.CaptureCutover(ctx, cutoverReader, sourceagent.CutoverCaptureConfig{SourceID: captured.SourceID, SourceType: captured.SourceType, SourceRuntime: captured.SourceRuntime, SigningKeyID: "key-1", Manifest: manifestStore, Snapshot: snapshotStore}); err == nil {
		t.Fatal("cutover files were overwritten")
	}
	readerConfig := *configuration
	readerConfig.User = "invoice_sub2api_usage_reader"
	readerConfig.Password = "economic_test_password"
	reader := stdlib.OpenDB(readerConfig)
	defer reader.Close()
	if err = pingIntegrationDatabase(ctx, reader); err != nil {
		t.Fatal(err)
	}
	if err = verifyProjectionPrivileges(ctx, reader, runConfig{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamUsage, ProtocolVersion: sourceagent.SchemaVersionV3}); err != nil {
		t.Fatalf("least privilege V3 usage role: %v", err)
	}
	if _, err = reader.ExecContext(ctx, `SELECT email FROM public.users`); err == nil {
		t.Fatal("V3 usage role read users.email")
	}
}

func readContractFile(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func manifestHashForTest(t *testing.T, value sourceagent.CutoverManifest) string {
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

func assertEconomicContractRejectsMembership(t *testing.T, ctx context.Context, admin *sql.DB, contract, readerName string) {
	t.Helper()
	probeName := "invoice_economic_membership_probe"
	probe := pgx.Identifier{probeName}.Sanitize()
	reader := pgx.Identifier{readerName}.Sanitize()
	if _, err := admin.ExecContext(ctx, `CREATE ROLE `+probe+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `GRANT `+probe+` TO `+reader); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, contract); err == nil {
		t.Fatal("economic contract accepted reader membership in another role")
	}
	_, _ = admin.ExecContext(ctx, `ROLLBACK`)
	if _, err := admin.ExecContext(ctx, `REVOKE `+probe+` FROM `+reader); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `GRANT `+reader+` TO `+probe); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, contract); err == nil {
		t.Fatal("economic contract accepted reader granted to another role")
	}
	_, _ = admin.ExecContext(ctx, `ROLLBACK`)
	if _, err := admin.ExecContext(ctx, `REVOKE `+reader+` FROM `+probe); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `DROP ROLE `+probe); err != nil {
		t.Fatal(err)
	}
}

func cleanupEconomicContract(t *testing.T, admin *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	roles := []string{"invoice_economic_membership_probe", "invoice_sub2api_payments_v3_reader", "invoice_sub2api_usage_reader", "invoice_sub2api_credits_reader", "invoice_sub2api_balances_reader", "invoice_newapi_payments_v3_reader", "invoice_newapi_usage_reader", "invoice_newapi_credits_reader", "invoice_newapi_balances_reader"}
	for _, role := range roles {
		quoted := pgx.Identifier{role}.Sanitize()
		_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+quoted)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+quoted)
	}
	for _, view := range []string{
		"invoice_newapi_payments_projection_health_v3", "invoice_newapi_payments_projection_v3", "invoice_newapi_cutover_contract_v3",
		"invoice_newapi_balance_projection_v3", "invoice_newapi_credits_projection_health_v3", "invoice_newapi_credits_projection_v3",
		"invoice_newapi_usage_projection_health_v3", "invoice_newapi_usage_projection_v3", "invoice_newapi_wallet_config_contract_v3",
		"invoice_sub2api_payments_projection_health_v3", "invoice_sub2api_payments_projection_v3", "invoice_sub2api_cutover_contract_v3",
		"invoice_sub2api_balance_projection_v3", "invoice_sub2api_credits_projection_health_v3", "invoice_sub2api_credits_projection_v3",
		"invoice_sub2api_usage_projection_health_v3", "invoice_sub2api_usage_projection_v3", "invoice_sub2api_wallet_config_contract_v3",
		"invoice_sub2api_payment_projection_health_v1", "invoice_sub2api_payment_projection_v1",
	} {
		_, _ = admin.ExecContext(ctx, `DROP VIEW IF EXISTS public.`+pgx.Identifier{view}.Sanitize()+` CASCADE`)
	}
	for _, table := range []string{"subscription_orders", "top_ups", "redemptions", "checkins", "logs", "options", "payment_orders", "redeem_codes", "user_affiliate_ledger", "promo_code_usages", "usage_logs", "users", "settings"} {
		_, _ = admin.ExecContext(ctx, `DROP TABLE IF EXISTS public.`+pgx.Identifier{table}.Sanitize()+` CASCADE`)
	}
	var database string
	if admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database) == nil {
		_, _ = admin.ExecContext(ctx, `GRANT TEMPORARY ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO PUBLIC`)
	}
}
