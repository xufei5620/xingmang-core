package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"invoice-system/agents/sourceagent"
)

func TestNewAPIV3FullRescanCapturesOldIDTransitionsAndSafetyHorizon(t *testing.T) {
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
	for _, role := range []string{"invoice_newapi_payments_v3_reader", "invoice_newapi_usage_reader", "invoice_newapi_credits_reader", "invoice_newapi_balances_reader"} {
		if _, err = admin.ExecContext(ctx, `CREATE ROLE `+pgx.Identifier{role}.Sanitize()+` LOGIN PASSWORD 'economic_test_password'`); err != nil {
			t.Fatal(err)
		}
	}
	statements := []string{
		`CREATE TABLE public.options(key text primary key,value text not null)`,
		`CREATE TABLE public.users(id bigint primary key,quota bigint,deleted_at timestamptz,email text,password text)`,
		`CREATE TABLE public.logs(id bigint primary key,user_id bigint,created_at bigint,type int,quota bigint,content text,username text,token_name text,model_name text,ip text,request_id text,other text)`,
		`CREATE TABLE public.checkins(id bigint primary key,user_id bigint,checkin_date text,quota_awarded bigint,created_at bigint)`,
		`CREATE TABLE public.redemptions(id bigint primary key,user_id bigint,status int,quota bigint,redeemed_time bigint,used_user_id bigint,key text,name text,deleted_at timestamptz)`,
		`CREATE TABLE public.top_ups(id bigint primary key,user_id bigint,amount bigint,money numeric(20,6),trade_no text,payment_method text,payment_provider text,create_time bigint,complete_time bigint,status text)`,
		`CREATE TABLE public.subscription_orders(id bigint primary key,user_id bigint,plan_id bigint,money numeric(20,6),trade_no text,payment_method text,payment_provider text,status text,create_time bigint,complete_time bigint,provider_payload text)`,
		`INSERT INTO public.options VALUES('Price','1'),('TopupGroupRatio','{"default":1}'),('LogConsumeEnabled','true')`,
		`INSERT INTO public.users VALUES(1,1000000,NULL,'hidden@example.com','secret')`,
		`INSERT INTO public.logs VALUES(1,1,extract(epoch from now()-interval '10 minutes')::bigint,2,50,'secret','hidden','token','model','192.0.2.1','request','{}')`,
		`INSERT INTO public.redemptions VALUES(5,0,1,300,0,0,'secret-key','hidden',NULL)`,
		`INSERT INTO public.checkins VALUES(7,1,'2026-08-21',20,extract(epoch from now())::bigint)`,
		`INSERT INTO public.top_ups VALUES(1,1,100,100,'trade-1','epay','epay',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'success'),(2,1,100,100,'trade-2','epay','epay',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'failed'),(3,1,100,100,'trade-3','unknown','unknown',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'success'),(4,1,100,100,'trade-4','epay','epay',extract(epoch from now()-interval '30 minutes')::bigint,0,'pending')`,
		`INSERT INTO public.subscription_orders VALUES(1,1,1,200,'sub-1','epay','epay','success',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'secret payload')`,
	}
	for _, statement := range statements {
		if _, err = admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare New API fixture: %v\n%s", err, statement)
		}
	}
	if _, err = admin.ExecContext(ctx, readContractFile(t, "newapi-economic-projection-grants.postgresql.sql")); err != nil {
		t.Fatalf("apply New API V3 contract: %v", err)
	}
	var hash string
	if err = admin.QueryRowContext(ctx, `SELECT configuration_hash FROM public.invoice_newapi_usage_projection_health_v3`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	cutover := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	manifest := sourceagent.CutoverManifest{SchemaVersion: 1, SourceID: "10000000-0000-4000-8000-000000000002", SourceType: sourceagent.SourceNewAPI, SourceRuntime: "v1.0.0-rc.25", CutoverAt: cutover.Format(time.RFC3339Nano), DatabaseClock: cutover.Format(time.RFC3339Nano), ProjectionContract: "newapi-economic-rc25-v3", ConfigurationHash: hash, UnitCode: "NEWAPI_QUOTA", SigningKeyID: "key-1", BaselineSnapshotID: strings.Repeat("b", 64), BaselineRowCount: "1", HighWaters: map[string]sourceagent.SourceHighWater{sourceagent.StreamPayments: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "subscription_orders:1;top_ups:4"}, sourceagent.StreamUsage: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "logs:1"}, sourceagent.StreamCredits: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "checkins:0;redemptions:5"}, sourceagent.StreamBalances: {EventTime: cutover.Format(time.RFC3339Nano), Cursor: "balance_snapshot:0"}}}
	manifest.ManifestHash = manifestHashForTest(t, manifest)
	if _, err = admin.ExecContext(ctx, `UPDATE public.redemptions SET status=3,used_user_id=1,redeemed_time=extract(epoch from now()-interval '5 minutes')::bigint WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	credits := &sourceagent.EconomicDBConnector{DB: admin, Source: sourceagent.SourceNewAPI, Stream: sourceagent.StreamCredits, Manifest: manifest, SafetyDelay: time.Minute}
	page, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Projections) != 1 || page.Projections[0].EntityType != sourceagent.EntityCreditEvent || page.Projections[0].ExternalID == "checkins:7" {
		t.Fatalf("old-ID redemption transition/safety horizon failed: %#v", page.Projections)
	}
	finalPage, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: page.NextCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if finalPage.HasMore || !finalPage.ScanComplete || len(finalPage.Projections) != 0 || finalPage.NextCursor.WatermarkAt != finalPage.NextCursor.CeilingAt {
		t.Fatalf("exact-limit empty final page did not publish completeness: %#v", finalPage)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.top_ups SET status='success',complete_time=extract(epoch from now()-interval '5 minutes')::bigint WHERE id=4`); err != nil {
		t.Fatal(err)
	}
	payments := &sourceagent.PaymentV3DBConnector{DB: admin, Source: sourceagent.SourceNewAPI, Manifest: manifest, SafetyDelay: time.Minute}
	foundOldCompletion, foundFailed, foundUnknownWithoutUnits := false, false, false
	cursor := sourceagent.ScanCursor{}
	for pages := 0; pages < 10; pages++ {
		paymentPage, scanErr := payments.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Cursor: cursor, Limit: 2})
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		for _, projection := range paymentPage.Projections {
			if projection.ExternalID == "4" {
				foundOldCompletion = true
			}
			if projection.ExternalID == "2" {
				foundFailed = true
			}
			if projection.ExternalID == "3" {
				raw, _ := json.Marshal(projection.Payload)
				var payload sourceagent.PaymentCandidatePayload
				if json.Unmarshal(raw, &payload) != nil {
					t.Fatal("decode candidate")
				}
				foundUnknownWithoutUnits = payload.WalletCashServiceUnits == nil && payload.VerificationState == sourceagent.VerificationPendingManual
			}
		}
		cursor = paymentPage.NextCursor
		if !paymentPage.HasMore {
			break
		}
	}
	if !foundOldCompletion || foundFailed || !foundUnknownWithoutUnits {
		t.Fatalf("New API payment safety old=%t failed=%t unknown_pending=%t", foundOldCompletion, foundFailed, foundUnknownWithoutUnits)
	}
	if _, err = admin.ExecContext(ctx, `UPDATE public.options SET value='{"default":1.5}' WHERE key='TopupGroupRatio'`); err != nil {
		t.Fatal(err)
	}
	drift, err := credits.Scan(ctx, sourceagent.ScanRequest{Mode: sourceagent.ScanFull, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !drift.ReconcileBlocked || drift.NextCursor.WatermarkAt != manifest.CutoverAt {
		t.Fatal("configuration drift advanced or did not block economic watermark")
	}
}
