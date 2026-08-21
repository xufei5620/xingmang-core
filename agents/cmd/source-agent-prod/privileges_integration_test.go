package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"invoice-system/agents/sourceagent"
)

type projectionRoleFixture struct {
	role     string
	password string
	config   runConfig
}

func TestVerifyProjectionPrivilegesAllFourPostgresStreams(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*adminConfig)
	defer admin.Close()
	if err = pingIntegrationDatabase(ctx, admin); err != nil {
		t.Fatal(err)
	}
	fixtures := []projectionRoleFixture{
		{role: "src_test_sub2_pay", password: "src_test_sub2_pay_password", config: runConfig{SourceType: sourceagent.SourceSub2API, StreamID: "payments"}},
		{role: "src_test_sub2_id", password: "src_test_sub2_id_password", config: runConfig{SourceType: sourceagent.SourceSub2API, StreamID: "identities"}},
		{role: "src_test_new_pay", password: "src_test_new_pay_password", config: runConfig{SourceType: sourceagent.SourceNewAPI, StreamID: "payments"}},
		{role: "src_test_new_id", password: "src_test_new_id_password", config: runConfig{SourceType: sourceagent.SourceNewAPI, StreamID: "identities"}},
		{role: "src_test_sub2_pay_v3", password: "src_test_sub2_pay_v3_password", config: runConfig{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamPayments, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_sub2_usage", password: "src_test_sub2_usage_password", config: runConfig{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamUsage, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_sub2_credits", password: "src_test_sub2_credits_password", config: runConfig{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamCredits, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_sub2_balances", password: "src_test_sub2_balances_password", config: runConfig{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_new_pay_v3", password: "src_test_new_pay_v3_password", config: runConfig{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamPayments, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_new_usage", password: "src_test_new_usage_password", config: runConfig{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamUsage, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_new_credits", password: "src_test_new_credits_password", config: runConfig{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamCredits, ProtocolVersion: sourceagent.SchemaVersionV3}},
		{role: "src_test_new_balances", password: "src_test_new_balances_password", config: runConfig{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3}},
	}
	cleanupProjectionFixtures(t, admin, fixtures)
	defer cleanupProjectionFixtures(t, admin, fixtures)
	database := pgx.Identifier{adminConfig.Database}.Sanitize()

	statements := []string{
		`REVOKE TEMPORARY ON DATABASE ` + database + ` FROM PUBLIC`,
		`CREATE TABLE public.payment_orders (id bigint,user_id bigint,status text,order_type text,amount numeric,pay_amount numeric,refund_amount numeric,currency text,completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text,provider_snapshot jsonb,secret_value text)`,
		`CREATE VIEW public.invoice_sub2api_payment_projection_v1 WITH (security_barrier=true) AS SELECT id,user_id,status,order_type,amount,pay_amount,refund_amount,currency,completed_at,refund_at,created_at,updated_at,payment_type,provider_key FROM public.payment_orders`,
		`CREATE VIEW public.invoice_sub2api_payment_projection_health_v1 WITH (security_barrier=true) AS SELECT count(*)::bigint AS total_rows,count(*)::bigint AS exposed_cny_rows,0::bigint AS unsupported_known_non_cny_rows,0::bigint AS blocked_unknown_currency_rows FROM public.payment_orders`,
		`REVOKE ALL ON public.invoice_sub2api_payment_projection_v1 FROM PUBLIC`,
		`REVOKE ALL ON public.invoice_sub2api_payment_projection_health_v1 FROM PUBLIC`,
		`CREATE TABLE public.auth_identities (id bigint,user_id bigint,provider_type text,provider_key text,provider_subject text,verified_at timestamptz,issuer text,created_at timestamptz,updated_at timestamptz,secret_value text)`,
		`CREATE VIEW public.invoice_sub2api_oidc_binding_projection_v1 WITH (security_barrier=true) AS SELECT id,user_id,provider_type,provider_key,provider_subject,verified_at,issuer,created_at,updated_at FROM public.auth_identities WHERE provider_type='oidc' AND provider_key='https://auth.solov.cc/realms/solov' AND issuer='https://auth.solov.cc/realms/solov' AND verified_at IS NOT NULL`,
		`REVOKE ALL ON public.invoice_sub2api_oidc_binding_projection_v1 FROM PUBLIC`,
		`CREATE TABLE public.top_ups (id bigint,user_id bigint,amount bigint,money numeric,payment_method text,payment_provider text,create_time bigint,complete_time bigint,status text,trade_no text)`,
		`CREATE TABLE public.user_oauth_bindings (id bigint,user_id bigint,provider_id bigint,provider_user_id text,created_at timestamptz,secret_value text)`,
		`CREATE TABLE public.custom_oauth_providers (id bigint,slug text,enabled boolean,well_known text,authorization_endpoint text,token_endpoint text,user_info_endpoint text,client_secret text)`,
		`CREATE VIEW public.invoice_newapi_oidc_provider_contract_v1 WITH (security_barrier=true) AS SELECT id,slug,enabled,well_known,authorization_endpoint,token_endpoint,user_info_endpoint,true AS contract_ok FROM public.custom_oauth_providers WHERE slug='solov-sso'`,
		`CREATE VIEW public.invoice_newapi_oidc_binding_projection_v1 WITH (security_barrier=true) AS SELECT b.id,b.user_id,b.provider_id,b.provider_user_id,b.created_at,p.slug AS provider_slug FROM public.user_oauth_bindings b JOIN public.custom_oauth_providers p ON p.id=b.provider_id WHERE p.slug='solov-sso'`,
		`REVOKE ALL ON public.invoice_newapi_oidc_provider_contract_v1 FROM PUBLIC`,
		`REVOKE ALL ON public.invoice_newapi_oidc_binding_projection_v1 FROM PUBLIC`,
		`CREATE VIEW public.invoice_sub2api_payments_projection_v3 WITH (security_barrier=true) AS SELECT 1::bigint source_id,1::bigint user_id,now() event_time,'payment_orders'::text causal_domain,'payment_orders:1'::text source_cursor,'payment_order'::text entity_kind,'COMPLETED'::text status,'balance'::text order_type,'1'::text amount,'1'::text pay_amount,'CNY'::text currency,'0'::text refund_amount,'0'::text gateway_refund_amount,now() completed_at,NULL::timestamptz refund_at,now() created_at,now() updated_at,'epay'::text payment_type,'epay'::text provider_key,'100000000'::text wallet_cash_service_units,NULL::text paid_minor,NULL::text verification_state,false refunded`,
		`CREATE VIEW public.invoice_sub2api_payments_projection_health_v3 WITH (security_barrier=true) AS SELECT true contract_ok,''::text blocked_reason,repeat('a',64)::text configuration_hash`,
		`CREATE VIEW public.invoice_sub2api_usage_projection_v3 WITH (security_barrier=true) AS SELECT 1::bigint source_id,1::bigint user_id,now() event_time,'1'::text service_units,'usage_logs'::text causal_domain,'usage_logs:1'::text source_cursor`,
		`CREATE VIEW public.invoice_sub2api_usage_projection_health_v3 WITH (security_barrier=true) AS SELECT true contract_ok,''::text blocked_reason,0::bigint total_rows,0::bigint gap_count,repeat('a',64)::text configuration_hash`,
		`CREATE VIEW public.invoice_sub2api_credits_projection_v3 WITH (security_barrier=true) AS SELECT 1::bigint source_id,1::bigint user_id,now() event_time,'1'::text service_units,'bonus'::text credit_kind,'promo_code_usages'::text causal_domain,'promo_code_usages:1'::text source_cursor`,
		`CREATE VIEW public.invoice_sub2api_credits_projection_health_v3 WITH (security_barrier=true) AS SELECT true contract_ok,''::text blocked_reason,0::bigint total_rows,0::bigint gap_count,repeat('a',64)::text configuration_hash`,
		`CREATE VIEW public.invoice_sub2api_balance_projection_v3 WITH (security_barrier=true) AS SELECT 1::bigint user_id,'1'::text balance_service_units,false balance_negative`,
		`CREATE VIEW public.invoice_sub2api_cutover_contract_v3 WITH (security_barrier=true) AS SELECT 'sub2-v3'::text projection_contract,true contract_ok,repeat('a',64)::text configuration_hash,now() payments_event_at,'payment_orders:1'::text payments_cursor,now() usage_event_at,'usage_logs:1'::text usage_cursor,now() credits_event_at,'promo_code_usages:1;redeem_codes:1;user_affiliate_ledger:1'::text credits_cursor`,
		`CREATE VIEW public.invoice_newapi_payments_projection_v3 WITH (security_barrier=true) AS SELECT * FROM public.invoice_sub2api_payments_projection_v3`,
		`CREATE VIEW public.invoice_newapi_payments_projection_health_v3 WITH (security_barrier=true) AS SELECT * FROM public.invoice_sub2api_payments_projection_health_v3`,
		`CREATE VIEW public.invoice_newapi_usage_projection_v3 WITH (security_barrier=true) AS SELECT 1::bigint source_id,1::bigint user_id,now() event_time,'1'::text service_units,'logs'::text causal_domain,'logs:1'::text source_cursor`,
		`CREATE VIEW public.invoice_newapi_usage_projection_health_v3 WITH (security_barrier=true) AS SELECT * FROM public.invoice_sub2api_usage_projection_health_v3`,
		`CREATE VIEW public.invoice_newapi_credits_projection_v3 WITH (security_barrier=true) AS SELECT 1::bigint source_id,1::bigint user_id,now() event_time,'1'::text service_units,'bonus'::text credit_kind,'checkins'::text causal_domain,'checkins:1'::text source_cursor`,
		`CREATE VIEW public.invoice_newapi_credits_projection_health_v3 WITH (security_barrier=true) AS SELECT * FROM public.invoice_sub2api_credits_projection_health_v3`,
		`CREATE VIEW public.invoice_newapi_balance_projection_v3 WITH (security_barrier=true) AS SELECT * FROM public.invoice_sub2api_balance_projection_v3`,
		`CREATE VIEW public.invoice_newapi_cutover_contract_v3 WITH (security_barrier=true) AS SELECT 'new-v3'::text projection_contract,true contract_ok,repeat('a',64)::text configuration_hash,now() payments_event_at,'subscription_orders:1;top_ups:1'::text payments_cursor,now() usage_event_at,'logs:1'::text usage_cursor,now() credits_event_at,'checkins:1;redemptions:1'::text credits_cursor`,
	}
	for _, statement := range statements {
		if _, err = admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare projection schema: %v\n%s", err, statement)
		}
	}
	for _, fixture := range fixtures {
		role := pgx.Identifier{fixture.role}.Sanitize()
		if _, err = admin.ExecContext(ctx, fmt.Sprintf(
			`CREATE ROLE %s LOGIN PASSWORD '%s' CONNECTION LIMIT 2 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`,
			role, fixture.password)); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []string{
			`ALTER ROLE ` + role + ` SET default_transaction_read_only=on`,
			`GRANT CONNECT ON DATABASE ` + database + ` TO ` + role,
			`REVOKE CREATE ON SCHEMA public FROM ` + role,
			`GRANT USAGE ON SCHEMA public TO ` + role,
			`REVOKE TEMPORARY ON DATABASE ` + database + ` FROM ` + role,
		} {
			if _, err = admin.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		if err = grantExpectedProjection(ctx, admin, role, expectedProjectionColumns(fixture.config)); err != nil {
			t.Fatal(err)
		}
	}

	readers := make(map[string]*sql.DB, len(fixtures))
	for _, fixture := range fixtures {
		readerConfig := *adminConfig
		readerConfig.User = fixture.role
		readerConfig.Password = fixture.password
		readerConfig.RuntimeParams = map[string]string{"default_transaction_read_only": "on"}
		reader := stdlib.OpenDB(readerConfig)
		readers[fixture.role] = reader
		defer reader.Close()
		if err = pingIntegrationDatabase(ctx, reader); err != nil {
			t.Fatal(err)
		}
		if err = verifyProjectionPrivileges(ctx, reader, fixture.config); err != nil {
			t.Fatalf("exact least-privilege projection %s/%s was rejected: %v", fixture.config.SourceType, fixture.config.StreamID, err)
		}
	}

	reader := readers[fixtures[0].role]
	readerRole := pgx.Identifier{fixtures[0].role}.Sanitize()
	if _, err = admin.ExecContext(ctx, `GRANT SELECT (provider_snapshot) ON public.payment_orders TO `+readerRole); err != nil {
		t.Fatal(err)
	}
	if err = verifyProjectionPrivileges(ctx, reader, fixtures[0].config); err == nil {
		t.Fatal("extra sensitive SELECT grant was accepted")
	}
	if _, err = admin.ExecContext(ctx, `REVOKE SELECT (provider_snapshot) ON public.payment_orders FROM `+readerRole); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, `GRANT TEMPORARY ON DATABASE `+database+` TO `+readerRole); err != nil {
		t.Fatal(err)
	}
	if err = verifyProjectionPrivileges(ctx, reader, fixtures[0].config); err == nil {
		t.Fatal("effective TEMPORARY privilege was accepted")
	}
	if _, err = admin.ExecContext(ctx, `REVOKE TEMPORARY ON DATABASE `+database+` FROM `+readerRole); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, `ALTER ROLE `+readerRole+` CONNECTION LIMIT 3`); err != nil {
		t.Fatal(err)
	}
	if err = verifyProjectionPrivileges(ctx, reader, fixtures[0].config); err == nil {
		t.Fatal("unexpected source role connection limit was accepted")
	}
}

func pingIntegrationDatabase(ctx context.Context, database *sql.DB) error {
	deadline := time.Now().Add(20 * time.Second)
	var err error
	for {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err = database.PingContext(pingCtx)
		cancel()
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func grantExpectedProjection(ctx context.Context, admin *sql.DB, role string, fields map[string]struct{}) error {
	byTable := map[string][]string{}
	for field := range fields {
		parts := strings.Split(field, ".")
		if len(parts) != 3 {
			return fmt.Errorf("invalid expected projection field %q", field)
		}
		byTable[parts[0]+"."+parts[1]] = append(byTable[parts[0]+"."+parts[1]], parts[2])
	}
	for table, columns := range byTable {
		sort.Strings(columns)
		quoted := make([]string, len(columns))
		for index, column := range columns {
			quoted[index] = pgx.Identifier{column}.Sanitize()
		}
		statement := `GRANT SELECT (` + strings.Join(quoted, ",") + `) ON ` +
			pgx.Identifier(strings.Split(table, ".")).Sanitize() + ` TO ` + role
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func cleanupProjectionFixtures(t *testing.T, admin *sql.DB, fixtures []projectionRoleFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, fixture := range fixtures {
		role := pgx.Identifier{fixture.role}.Sanitize()
		var exists bool
		if err := admin.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, fixture.role).Scan(&exists); err == nil && exists {
			_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+role)
			_, _ = admin.ExecContext(ctx, `DROP ROLE `+role)
		}
	}
	for _, object := range []string{
		`VIEW IF EXISTS public.invoice_newapi_cutover_contract_v3`, `VIEW IF EXISTS public.invoice_newapi_balance_projection_v3`, `VIEW IF EXISTS public.invoice_newapi_credits_projection_health_v3`, `VIEW IF EXISTS public.invoice_newapi_credits_projection_v3`, `VIEW IF EXISTS public.invoice_newapi_usage_projection_health_v3`, `VIEW IF EXISTS public.invoice_newapi_usage_projection_v3`, `VIEW IF EXISTS public.invoice_newapi_payments_projection_health_v3`, `VIEW IF EXISTS public.invoice_newapi_payments_projection_v3`,
		`VIEW IF EXISTS public.invoice_sub2api_cutover_contract_v3`, `VIEW IF EXISTS public.invoice_sub2api_balance_projection_v3`, `VIEW IF EXISTS public.invoice_sub2api_credits_projection_health_v3`, `VIEW IF EXISTS public.invoice_sub2api_credits_projection_v3`, `VIEW IF EXISTS public.invoice_sub2api_usage_projection_health_v3`, `VIEW IF EXISTS public.invoice_sub2api_usage_projection_v3`, `VIEW IF EXISTS public.invoice_sub2api_payments_projection_health_v3`, `VIEW IF EXISTS public.invoice_sub2api_payments_projection_v3`,
		`VIEW IF EXISTS public.invoice_newapi_oidc_binding_projection_v1`,
		`VIEW IF EXISTS public.invoice_newapi_oidc_provider_contract_v1`,
		`VIEW IF EXISTS public.invoice_sub2api_oidc_binding_projection_v1`,
		`VIEW IF EXISTS public.invoice_sub2api_payment_projection_health_v1`,
		`VIEW IF EXISTS public.invoice_sub2api_payment_projection_v1`,
		`TABLE IF EXISTS public.payment_orders`, `TABLE IF EXISTS public.auth_identities`,
		`TABLE IF EXISTS public.top_ups`, `TABLE IF EXISTS public.user_oauth_bindings`,
		`TABLE IF EXISTS public.custom_oauth_providers`,
	} {
		_, _ = admin.ExecContext(ctx, `DROP `+object+` CASCADE`)
	}
	var database string
	if err := admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err == nil {
		_, _ = admin.ExecContext(ctx, `GRANT TEMPORARY ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO PUBLIC`)
	}
}
