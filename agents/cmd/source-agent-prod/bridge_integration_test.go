package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"invoice-system/agents/sourceagent"
)

const bridgeTestPassword = "bridge_test_password"

func TestBridgeV4ContractsAreDependencyFreeAndMigrationCompatible(t *testing.T) {
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
	if err = pingIntegrationDatabase(ctx, admin); err != nil {
		t.Fatal(err)
	}

	for _, source := range []string{sourceagent.SourceSub2API, sourceagent.SourceNewAPI} {
		t.Run(source, func(t *testing.T) {
			cleanupBridgeFixture(t, ctx, admin)
			defer cleanupBridgeFixture(t, ctx, admin)
			setupBridgeFixture(t, ctx, admin, source)
			applyBridgeContracts(t, ctx, admin, source)

			assertBridgeInventory(t, ctx, admin, source)
			assertBridgeHasNoRelationDependency(t, ctx, admin, source)
			assertReaderBoundaries(t, ctx, admin, configuration, source)
			assertBridgeSemantics(t, ctx, admin, configuration, source)
			if _, err = admin.ExecContext(ctx, `ALTER TABLE public.users ENABLE ROW LEVEL SECURITY`); err != nil {
				t.Fatal(err)
			}
			reader := openBridgeReader(t, configuration, source, sourceagent.StreamBalances)
			if err = verifyProjectionPrivileges(ctx, reader, runConfig{SourceType: source, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3}); err == nil {
				reader.Close()
				t.Fatal("runtime bridge gate accepted an upstream relation with unreviewed RLS")
			}
			reader.Close()
			if _, err = admin.ExecContext(ctx, `ALTER TABLE public.users DISABLE ROW LEVEL SECURITY`); err != nil {
				t.Fatal(err)
			}
			balancesFunction := pgx.Identifier{source + "_balances_v4"}.Sanitize()
			if _, err = admin.ExecContext(ctx, `CREATE OR REPLACE FUNCTION invoice_bridge.`+balancesFunction+`(operation text,request jsonb)
				RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
				AS $fake$ BEGIN RETURN; END $fake$`); err != nil {
				t.Fatal(err)
			}
			reader = openBridgeReader(t, configuration, source, sourceagent.StreamBalances)
			if err = verifyProjectionPrivileges(ctx, reader, runConfig{SourceType: source, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3}); err == nil {
				reader.Close()
				t.Fatal("runtime bridge gate accepted a modified security-definer function body")
			}
			reader.Close()
			if _, err = admin.ExecContext(ctx, readContractFile(t, source+"-economic-projection-grants.postgresql.sql")); err != nil {
				t.Fatalf("restore reviewed economic bridge after body-drift test: %v", err)
			}
			balancesReaderConfig := runConfig{SourceType: source, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3}
			if _, err = admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION invoice_bridge.`+balancesFunction+`(text,jsonb) TO PUBLIC`); err != nil {
				t.Fatal(err)
			}
			reader = openBridgeReader(t, configuration, source, sourceagent.StreamBalances)
			if err = verifyProjectionPrivileges(ctx, reader, balancesReaderConfig); err == nil {
				reader.Close()
				t.Fatal("runtime bridge gate accepted PUBLIC function EXECUTE")
			}
			reader.Close()
			if _, err = admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION invoice_bridge.`+balancesFunction+`(text,jsonb) FROM PUBLIC`); err != nil {
				t.Fatal(err)
			}
			if _, err = admin.ExecContext(ctx, `GRANT USAGE ON SCHEMA invoice_bridge TO PUBLIC`); err != nil {
				t.Fatal(err)
			}
			reader = openBridgeReader(t, configuration, source, sourceagent.StreamBalances)
			if err = verifyProjectionPrivileges(ctx, reader, balancesReaderConfig); err == nil {
				reader.Close()
				t.Fatal("runtime bridge gate accepted PUBLIC schema USAGE")
			}
			reader.Close()
			if _, err = admin.ExecContext(ctx, `REVOKE USAGE ON SCHEMA invoice_bridge FROM PUBLIC`); err != nil {
				t.Fatal(err)
			}
			fakeOwner := pgx.Identifier{"invoice_bridge_runtime_owner_probe"}.Sanitize()
			if _, err = admin.ExecContext(ctx, `CREATE ROLE `+fakeOwner+` NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 0;
				ALTER FUNCTION invoice_bridge.`+balancesFunction+`(text,jsonb) OWNER TO `+fakeOwner); err != nil {
				t.Fatal(err)
			}
			reader = openBridgeReader(t, configuration, source, sourceagent.StreamBalances)
			if err = verifyProjectionPrivileges(ctx, reader, balancesReaderConfig); err == nil {
				reader.Close()
				t.Fatal("runtime bridge gate accepted a wrong security-definer owner")
			}
			reader.Close()
			bridgeOwner := pgx.Identifier{"invoice_" + source + "_bridge_owner"}.Sanitize()
			if _, err = admin.ExecContext(ctx, `ALTER FUNCTION invoice_bridge.`+balancesFunction+`(text,jsonb) OWNER TO `+bridgeOwner+`; DROP ROLE `+fakeOwner); err != nil {
				t.Fatal(err)
			}

			// Idempotency is security-sensitive: source grants revoke the owner
			// first, then must restore the complete five-stream ACL and repair a
			// stale cross-function EXECUTE grant preserved by CREATE OR REPLACE.
			if _, err = admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION invoice_bridge.`+source+`_payments_v4(text,jsonb) TO `+pgx.Identifier{"invoice_" + source + "_identities_reader"}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			if _, err = admin.ExecContext(ctx, readContractFile(t, source+"-source-projection-grants.postgresql.sql")); err != nil {
				t.Fatalf("reapply source contract: %v", err)
			}
			assertReaderBoundaries(t, ctx, admin, configuration, source)
			assertBridgeSemantics(t, ctx, admin, configuration, source)

			assertUpstreamAlterAndRuntimeDrift(t, ctx, admin, configuration, source)
		})
	}
}

func TestBridgeV4StaticDatabaseCheckDoesNotRequireCutoverManifest(t *testing.T) {
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
	cleanupBridgeFixture(t, ctx, admin)
	defer cleanupBridgeFixture(t, ctx, admin)
	setupBridgeFixture(t, ctx, admin, sourceagent.SourceSub2API)
	applyBridgeContracts(t, ctx, admin, sourceagent.SourceSub2API)

	readerURL, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	readerURL.User = url.UserPassword("invoice_sub2api_usage_reader", bridgeTestPassword)
	temporary := t.TempDir()
	dsnPath := filepath.Join(temporary, "usage-reader.dsn")
	if err = os.WriteFile(dsnPath, []byte(readerURL.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dsnPath, 0o600); err != nil {
		t.Fatal(err)
	}
	values := validEnvironment(t, sourceagent.SourceSub2API, sourceagent.StreamUsage)
	values["SOURCE_SCHEMA_VERSION"] = sourceagent.SchemaVersionV3
	values["SOURCE_DB_DSN_FILE"] = dsnPath
	values["SOURCE_CUTOVER_MANIFEST_FILE"] = filepath.Join(temporary, "not-created-manifest.enc")
	values["SOURCE_CUTOVER_KEY_FILE"] = filepath.Join(temporary, "not-created-cutover.key")
	for name, value := range values {
		t.Setenv(name, value)
	}
	for _, name := range []string{"PGPASSWORD", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "DATABASE_URL", "SOURCE_DB_DSN"} {
		t.Setenv(name, "")
	}
	if err = checkDatabaseFromEnvironment(false); err != nil {
		t.Fatalf("static database check required a cutover manifest: %v", err)
	}
	if err = checkDatabaseFromEnvironment(true); err == nil || !strings.Contains(err.Error(), "load encrypted cutover manifest") {
		t.Fatalf("full database check did not require the V4 cutover manifest: %v", err)
	}
}

func TestBridgeV4InstallationFailsClosedWithoutVerifiedSourceBoundary(t *testing.T) {
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

	t.Run("economic-alone", func(t *testing.T) {
		cleanupBridgeFixture(t, ctx, admin)
		defer cleanupBridgeFixture(t, ctx, admin)
		setupBridgeFixture(t, ctx, admin, sourceagent.SourceNewAPI)
		if _, err = admin.ExecContext(ctx, readContractFile(t, "newapi-economic-projection-grants.postgresql.sql")); err == nil {
			t.Fatal("economic contract installed without the verified source bridge")
		}
		assertNoBridgeObjects(t, ctx, admin)
	})

	t.Run("preexisting-schema-object", func(t *testing.T) {
		cleanupBridgeFixture(t, ctx, admin)
		defer cleanupBridgeFixture(t, ctx, admin)
		setupBridgeFixture(t, ctx, admin, sourceagent.SourceNewAPI)
		if _, err = admin.ExecContext(ctx, `CREATE ROLE invoice_newapi_bridge_owner NOLOGIN`); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.ExecContext(ctx, `CREATE SCHEMA invoice_bridge; CREATE TABLE invoice_bridge.trap(value text)`); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.ExecContext(ctx, readContractFile(t, "newapi-source-projection-grants.postgresql.sql")); err == nil {
			t.Fatal("source contract accepted a preexisting schema object")
		}
		var rawGrants int
		if err = admin.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.column_privileges WHERE grantee='invoice_newapi_bridge_owner'`).Scan(&rawGrants); err != nil || rawGrants != 0 {
			t.Fatalf("failed installation left raw grants=%d err=%v", rawGrants, err)
		}
	})
}

func TestBridgeV4RollbackRejectsActiveSessionsAndPreservesApplicationData(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	for _, source := range []string{sourceagent.SourceSub2API, sourceagent.SourceNewAPI} {
		t.Run(source, func(t *testing.T) {
			cleanupBridgeFixture(t, ctx, admin)
			defer cleanupBridgeFixture(t, ctx, admin)
			setupBridgeFixture(t, ctx, admin, source)
			applyBridgeContracts(t, ctx, admin, source)
			rollback := readContractFile(t, source+"-bridge-v4-rollback.postgresql.sql")
			for _, line := range strings.Split(rollback, "\n") {
				if strings.Contains(strings.ToUpper(strings.SplitN(line, "--", 2)[0]), "CASCADE") {
					t.Fatal("bridge rollback contains CASCADE")
				}
			}
			sentinelTable := "settings"
			if source == sourceagent.SourceNewAPI {
				sentinelTable = "options"
			}
			var before int
			if err = admin.QueryRowContext(ctx, `SELECT count(*) FROM public.`+pgx.Identifier{sentinelTable}.Sanitize()).Scan(&before); err != nil {
				t.Fatal(err)
			}

			reader := openBridgeReader(t, configuration, source, sourceagent.StreamIdentities)
			if _, err = admin.ExecContext(ctx, rollback); err == nil {
				reader.Close()
				t.Fatal("rollback ignored an active source reader")
			}
			reader.Close()
			waitForBridgeSessions(t, ctx, admin, source)
			assertBridgeInventory(t, ctx, admin, source)
			identityRole := pgx.Identifier{"invoice_" + source + "_identities_reader"}.Sanitize()
			if _, err = admin.ExecContext(ctx, `GRANT SELECT(id) ON public.users TO `+identityRole); err != nil {
				t.Fatal(err)
			}
			if _, err = admin.ExecContext(ctx, rollback); err == nil {
				t.Fatal("rollback silently removed an unexpected raw SELECT grant")
			}
			assertBridgeInventory(t, ctx, admin, source)
			if _, err = admin.ExecContext(ctx, `REVOKE SELECT(id) ON public.users FROM `+identityRole); err != nil {
				t.Fatal(err)
			}
			ownedType := pgx.Identifier{"invoice_" + source + "_unexpected_enum"}.Sanitize()
			if _, err = admin.ExecContext(ctx, `CREATE TYPE public.`+ownedType+` AS ENUM ('sentinel'); ALTER TYPE public.`+ownedType+` OWNER TO `+identityRole); err != nil {
				t.Fatal(err)
			}
			if _, err = admin.ExecContext(ctx, rollback); err == nil {
				t.Fatal("rollback deleted an unknown role-owned type instead of failing closed")
			}
			var typeStillExists bool
			if err = admin.QueryRowContext(ctx, `SELECT to_regtype($1) IS NOT NULL`, "public.invoice_"+source+"_unexpected_enum").Scan(&typeStillExists); err != nil || !typeStillExists {
				t.Fatalf("unknown role-owned type was not preserved exists=%t err=%v", typeStillExists, err)
			}
			assertBridgeInventory(t, ctx, admin, source)
			if _, err = admin.ExecContext(ctx, `ALTER TYPE public.`+ownedType+` OWNER TO CURRENT_USER; DROP TYPE public.`+ownedType); err != nil {
				t.Fatal(err)
			}

			if _, err = admin.ExecContext(ctx, rollback); err != nil {
				t.Fatalf("execute bridge rollback: %v", err)
			}
			var schemaExists bool
			if err = admin.QueryRowContext(ctx, `SELECT to_regnamespace('invoice_bridge') IS NOT NULL`).Scan(&schemaExists); err != nil || schemaExists {
				t.Fatalf("bridge schema remains=%t err=%v", schemaExists, err)
			}
			var roles int
			if err = admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE $1`, "invoice_"+source+"_%").Scan(&roles); err != nil || roles != 0 {
				t.Fatalf("bridge roles remain=%d err=%v", roles, err)
			}
			var after int
			if err = admin.QueryRowContext(ctx, `SELECT count(*) FROM public.`+pgx.Identifier{sentinelTable}.Sanitize()).Scan(&after); err != nil || after != before {
				t.Fatalf("application sentinel changed before=%d after=%d err=%v", before, after, err)
			}
			var publicTemp bool
			if err = admin.QueryRowContext(ctx, `SELECT has_database_privilege('public',current_database(),'TEMP')`).Scan(&publicTemp); err != nil || !publicTemp {
				t.Fatalf("PUBLIC TEMP was not restored=%t err=%v", publicTemp, err)
			}
		})
	}
}

func setupBridgeFixture(t *testing.T, ctx context.Context, admin *sql.DB, source string) {
	t.Helper()
	for _, role := range []struct {
		name  string
		login bool
	}{
		{"payments_reader", false}, {"identities_reader", true}, {"payments_v3_reader", true},
		{"usage_reader", true}, {"credits_reader", true}, {"balances_reader", true},
	} {
		name := pgx.Identifier{"invoice_" + source + "_" + role.name}.Sanitize()
		statement := `CREATE ROLE ` + name + ` NOLOGIN CONNECTION LIMIT 0 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`
		if role.login {
			statement = `CREATE ROLE ` + name + ` LOGIN PASSWORD '` + bridgeTestPassword + `' CONNECTION LIMIT 2 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`
		}
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	statements := sub2BridgeFixtureStatements()
	if source == sourceagent.SourceNewAPI {
		statements = newAPIBridgeFixtureStatements()
	}
	for _, statement := range statements {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture statement failed: %v\n%s", err, statement)
		}
	}
}

func sub2BridgeFixtureStatements() []string {
	return []string{
		`CREATE TABLE public.settings(key text primary key,value text not null,updated_at timestamptz not null)`,
		`CREATE TABLE public.users(id bigint primary key,balance numeric(20,8),deleted_at timestamptz,email text,password_hash text)`,
		`CREATE TABLE public.usage_logs(id bigint primary key,user_id bigint,billing_type smallint,actual_cost numeric(20,10),created_at timestamptz,content text,ip_address text)`,
		`CREATE TABLE public.promo_code_usages(id bigint primary key,user_id bigint,bonus_amount numeric(20,8),used_at timestamptz)`,
		`CREATE TABLE public.user_affiliate_ledger(id bigint primary key,user_id bigint,action text,amount numeric(20,8),created_at timestamptz)`,
		`CREATE TABLE public.redeem_codes(id bigint primary key,code text,type text,value numeric(20,8),status text,used_by bigint,used_at timestamptz)`,
		`CREATE TABLE public.payment_orders(id bigint primary key,user_id bigint,status text,order_type text,amount numeric(20,8),pay_amount numeric(20,8),fee_rate numeric(10,4),refund_amount numeric(20,8),completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text,provider_snapshot jsonb,recharge_code text)`,
		`CREATE TABLE public.auth_identities(id bigint primary key,user_id bigint,provider_type text,provider_key text,provider_subject text,verified_at timestamptz,issuer text,metadata jsonb,created_at timestamptz,updated_at timestamptz,secret_value text)`,
		`INSERT INTO public.settings VALUES('BALANCE_RECHARGE_MULTIPLIER','1.00',now()-interval '1 day'),('RECHARGE_FEE_RATE','0.00',now()-interval '1 day')`,
		`INSERT INTO public.users VALUES(1,10,NULL,'hidden@example.com','secret'),(2,-2.5,NULL,'negative@example.com','secret')`,
		`INSERT INTO public.usage_logs VALUES(1,1,0,0.000000005,now()-interval '10 minutes','secret','192.0.2.1')`,
		`INSERT INTO public.promo_code_usages VALUES(1,1,2,now()-interval '9 minutes')`,
		`INSERT INTO public.redeem_codes VALUES(1,'CASH','balance',10,'used',1,now()-interval '10 minutes'),(99992744,'BONUS','balance',3,'used',1,now()-interval '9 minutes')`,
		`INSERT INTO public.payment_orders VALUES(1,1,'COMPLETED','balance',10,10,0,0,now()-interval '8 minutes',NULL,now()-interval '9 minutes',now()-interval '8 minutes','epay','easypay','{"schema_version":"2","provider_key":"easypay","currency":"CNY"}','CASH')`,
		`INSERT INTO public.auth_identities(id,user_id,provider_type,provider_key,provider_subject,verified_at,issuer,metadata,created_at,updated_at,secret_value) VALUES
			(1,1,'oidc','https://auth.solov.cc/realms/solov','subject-verified','2026-08-15T10:00:00Z','https://auth.solov.cc/realms/solov','{"email_verified":false}','2026-08-14T10:00:00Z','2026-08-20T10:00:01Z','hidden'),
			(2,1,'oidc','https://auth.solov.cc/realms/solov','subject-bound',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:02Z','hidden'),
			(3,1,'oidc','https://auth.solov.cc/realms/solov','subject-unverified',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":false}','2026-08-16T12:34:56Z','2026-08-20T10:00:03Z','hidden'),
			(4,1,'oidc','https://auth.solov.cc/realms/solov','subject-missing-claim',NULL,'https://auth.solov.cc/realms/solov','{}','2026-08-16T12:34:56Z','2026-08-20T10:00:04Z','hidden'),
			(5,1,'oidc','https://auth.solov.cc/realms/solov','subject-wrong-issuer',NULL,'https://invalid.example/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:05Z','hidden'),
			(6,1,'oidc','https://auth.solov.cc/realms/solov','',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:06Z','hidden'),
			(7,1,'oidc','https://auth.solov.cc/realms/solov','subject-string-claim',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":"true"}','2026-08-16T12:34:56Z','2026-08-20T10:00:07Z','hidden'),
			(8,1,'oidc','https://auth.solov.cc/realms/solov','   ',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:08Z','hidden'),
			(9,1,'github','https://auth.solov.cc/realms/solov','subject-wrong-type',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:09Z','hidden'),
			(10,1,'oidc','https://invalid.example/realms/solov','subject-wrong-key',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:10Z','hidden'),
			(11,1,'oidc','https://auth.solov.cc/realms/solov','subject-sql-null',NULL,'https://auth.solov.cc/realms/solov',NULL,'2026-08-16T12:34:56Z','2026-08-20T10:00:11Z','hidden'),
			(12,1,'oidc','https://auth.solov.cc/realms/solov','subject-json-null',NULL,'https://auth.solov.cc/realms/solov','null','2026-08-16T12:34:56Z','2026-08-20T10:00:12Z','hidden'),
			(13,1,'oidc','https://auth.solov.cc/realms/solov','subject-number-claim',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":1}','2026-08-16T12:34:56Z','2026-08-20T10:00:13Z','hidden'),
			(14,1,'oidc','https://auth.solov.cc/realms/solov','subject-nested-claim',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":{"value":true}}','2026-08-16T12:34:56Z','2026-08-20T10:00:14Z','hidden'),
			(15,1,'oidc','https://auth.solov.cc/realms/solov','subject-missing-created',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}',NULL,'2026-08-20T10:00:15Z','hidden'),
			(16,1,'oidc','https://auth.solov.cc/realms/solov',E'\t\n',NULL,'https://auth.solov.cc/realms/solov','{"email_verified":true}','2026-08-16T12:34:56Z','2026-08-20T10:00:16Z','hidden')`,
	}
}

func newAPIBridgeFixtureStatements() []string {
	return []string{
		`CREATE TABLE public.options(key text primary key,value text not null)`,
		`CREATE TABLE public.users(id bigint primary key,quota bigint,deleted_at timestamptz,email text,password text)`,
		`CREATE TABLE public.logs(id bigint primary key,user_id bigint,created_at bigint,type int,quota bigint,content text,username text,token_name text,model_name text,ip text,request_id text,other text)`,
		`CREATE TABLE public.checkins(id bigint primary key,user_id bigint,checkin_date text,quota_awarded bigint,created_at bigint)`,
		`CREATE TABLE public.redemptions(id bigint primary key,user_id bigint,status int,quota bigint,redeemed_time bigint,used_user_id bigint,key text,name text,deleted_at timestamptz)`,
		`CREATE TABLE public.top_ups(id bigint primary key,user_id bigint,amount bigint,money numeric(20,6),trade_no text,payment_method text,payment_provider text,create_time bigint,complete_time bigint,status text)`,
		`CREATE TABLE public.subscription_orders(id bigint primary key,user_id bigint,plan_id bigint,money numeric(20,6),trade_no text,payment_method text,payment_provider text,status text,create_time bigint,complete_time bigint,provider_payload text)`,
		`CREATE TABLE public.custom_oauth_providers(id bigint primary key,slug text,enabled boolean,well_known text,authorization_endpoint text,token_endpoint text,user_info_endpoint text,client_secret text)`,
		`CREATE TABLE public.user_oauth_bindings(id bigint primary key,user_id bigint,provider_id bigint,provider_user_id text,created_at timestamptz,secret_value text)`,
		`INSERT INTO public.options VALUES('QuotaPerUnit','500000'),('Price','1'),('TopupGroupRatio','{"default":1}'),('LogConsumeEnabled','true')`,
		`INSERT INTO public.users VALUES(1,1000000,NULL,'hidden@example.com','secret'),(2,-100,NULL,'negative@example.com','secret')`,
		`INSERT INTO public.logs VALUES(1,1,extract(epoch from now()-interval '10 minutes')::bigint,2,50,'secret','hidden','token','model','192.0.2.1','request','{}')`,
		`INSERT INTO public.checkins VALUES(7,1,'2026-08-21',20,extract(epoch from now())::bigint)`,
		`INSERT INTO public.redemptions VALUES(5,1,0,300,0,0,'secret-key','hidden',NULL)`,
		`INSERT INTO public.top_ups VALUES
			(1,1,100,100,'trade-1','epay','epay',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'success'),
			(2,1,100,100,'trade-2','epay','epay',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'failed'),
			(3,1,100,100,'trade-3','unknown','unknown',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'success'),
			(4,1,100,100,'trade-4','epay','epay',extract(epoch from now()-interval '30 minutes')::bigint,0,'pending')`,
		`INSERT INTO public.subscription_orders VALUES(1,1,1,200,'sub-secret','epay','epay','success',extract(epoch from now()-interval '12 minutes')::bigint,extract(epoch from now()-interval '10 minutes')::bigint,'secret')`,
		`INSERT INTO public.custom_oauth_providers VALUES(1,'solov-sso',true,'https://auth.solov.cc/realms/solov/.well-known/openid-configuration','https://auth.solov.cc/realms/solov/protocol/openid-connect/auth','https://auth.solov.cc/realms/solov/protocol/openid-connect/token','https://auth.solov.cc/realms/solov/protocol/openid-connect/userinfo','secret')`,
		`INSERT INTO public.user_oauth_bindings VALUES(1,1,1,'subject-1',now(),'hidden')`,
	}
}

func applyBridgeContracts(t *testing.T, ctx context.Context, admin *sql.DB, source string) {
	t.Helper()
	for _, name := range []string{source + "-source-projection-grants.postgresql.sql", source + "-economic-projection-grants.postgresql.sql"} {
		if _, err := admin.ExecContext(ctx, readContractFile(t, name)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

func assertBridgeInventory(t *testing.T, ctx context.Context, admin *sql.DB, source string) {
	t.Helper()
	var views, functions, roles int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_views WHERE schemaname='public' AND viewname LIKE $1`, "invoice_"+source+"_%").Scan(&views); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='invoice_bridge' AND p.proname LIKE $1`, source+"_%_v4").Scan(&functions); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE $1`, "invoice_"+source+"_%").Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if views != 0 || functions != 5 || roles != 7 {
		t.Fatalf("bridge inventory views=%d functions=%d roles=%d", views, functions, roles)
	}
}

func assertBridgeHasNoRelationDependency(t *testing.T, ctx context.Context, admin *sql.DB, source string) {
	t.Helper()
	var dependencies int
	if err := admin.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_depend d
		JOIN pg_proc p ON d.classid='pg_proc'::regclass AND d.objid=p.oid
		JOIN pg_namespace n ON n.oid=p.pronamespace
		JOIN pg_class c ON d.refclassid='pg_class'::regclass AND d.refobjid=c.oid
		WHERE n.nspname='invoice_bridge' AND p.proname LIKE $1`, source+"_%_v4").Scan(&dependencies); err != nil || dependencies != 0 {
		t.Fatalf("bridge relation dependencies=%d err=%v", dependencies, err)
	}
}

func assertReaderBoundaries(t *testing.T, ctx context.Context, admin *sql.DB, base *pgx.ConnConfig, source string) {
	t.Helper()
	for _, stream := range []string{sourceagent.StreamPayments, sourceagent.StreamUsage, sourceagent.StreamCredits, sourceagent.StreamBalances, sourceagent.StreamIdentities} {
		reader := openBridgeReader(t, base, source, stream)
		protocol := sourceagent.SchemaVersionV3
		if stream == sourceagent.StreamIdentities {
			protocol = sourceagent.SchemaVersionV2
		}
		if err := verifyProjectionPrivileges(ctx, reader, runConfig{SourceType: source, StreamID: stream, ProtocolVersion: protocol}); err != nil {
			reader.Close()
			t.Fatalf("%s/%s bridge privilege: %v", source, stream, err)
		}
		if _, err := reader.ExecContext(ctx, `SELECT id FROM public.users`); err == nil {
			reader.Close()
			t.Fatalf("%s/%s read a raw source table", source, stream)
		}
		reader.Close()
	}
	var broadOwnerGrants int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.role_table_grants WHERE grantee=$1 AND privilege_type='SELECT'`, "invoice_"+source+"_bridge_owner").Scan(&broadOwnerGrants); err != nil || broadOwnerGrants != 0 {
		t.Fatalf("bridge owner has broad table grants=%d err=%v", broadOwnerGrants, err)
	}
}

func assertBridgeSemantics(t *testing.T, ctx context.Context, admin *sql.DB, _ *pgx.ConnConfig, source string) {
	t.Helper()
	if source == sourceagent.SourceSub2API {
		for _, query := range []string{
			`SELECT count(*) FROM invoice_bridge.sub2api_payments_v4('health','{}'::jsonb)`,
			`SELECT count(*) FROM invoice_bridge.sub2api_identities_v4('page','{"provider_key":"https://auth.solov.cc/realms/solov","issuer":"https://auth.solov.cc/realms/solov","updated_at":"2000-01-01T00:00:00Z","id":0,"limit":10}'::jsonb)`,
			`SELECT count(*) FROM invoice_bridge.sub2api_usage_v4('health','{}'::jsonb)`,
			`SELECT count(*) FROM invoice_bridge.sub2api_credits_v4('health','{}'::jsonb)`,
			`SELECT count(*) FROM invoice_bridge.sub2api_balances_v4('contract','{}'::jsonb)`,
			`SELECT count(*) FROM invoice_bridge.sub2api_balances_v4('rows','{}'::jsonb)`,
		} {
			var count int
			if err := admin.QueryRowContext(ctx, query).Scan(&count); err != nil || count == 0 {
				t.Fatalf("Sub2 bridge operation failed after install/reapply count=%d err=%v query=%s", count, err, query)
			}
		}
		var units string
		err := admin.QueryRowContext(ctx, `SELECT service_units
			FROM invoice_bridge.sub2api_usage_v4('page',$1::jsonb) AS bridge(payload)
			CROSS JOIN LATERAL jsonb_to_record(bridge.payload) AS projected(service_units text)`,
			`{"cutover":"2000-01-01T00:00:00Z","horizon":"2100-01-01T00:00:00Z","limit":10,"usage_logs_position":0,"usage_logs_ceiling":1}`).Scan(&units)
		if err != nil || units != "1" {
			t.Fatalf("Sub2 usage units=%q err=%v", units, err)
		}
		assertSub2OIDCIdentityBridgeSemantics(t, ctx, admin)
		return
	}
	for _, query := range []string{
		`SELECT count(*) FROM invoice_bridge.newapi_payments_v4('health','{}'::jsonb)`,
		`SELECT count(*) FROM invoice_bridge.newapi_identities_v4('provider_contract','{"slug":"solov-sso"}'::jsonb)`,
		`SELECT count(*) FROM invoice_bridge.newapi_usage_v4('health','{}'::jsonb)`,
		`SELECT count(*) FROM invoice_bridge.newapi_credits_v4('health','{}'::jsonb)`,
		`SELECT count(*) FROM invoice_bridge.newapi_balances_v4('contract','{}'::jsonb)`,
		`SELECT count(*) FROM invoice_bridge.newapi_balances_v4('rows','{}'::jsonb)`,
	} {
		var count int
		if err := admin.QueryRowContext(ctx, query).Scan(&count); err != nil || count == 0 {
			t.Fatalf("New API bridge operation failed after install/reapply count=%d err=%v query=%s", count, err, query)
		}
	}
	var units string
	err := admin.QueryRowContext(ctx, `SELECT wallet_cash_service_units
		FROM invoice_bridge.newapi_payments_v4('page',$1::jsonb) AS bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) AS projected(wallet_cash_service_units text,causal_domain text)
		WHERE causal_domain='top_ups'`,
		`{"cutover":"2000-01-01T00:00:00Z","horizon":"2100-01-01T00:00:00Z","limit":10,"subscription_position":0,"subscription_ceiling":1,"topup_position":0,"topup_ceiling":1}`).Scan(&units)
	if err != nil || units != "50000000" {
		t.Fatalf("New API payment units=%q err=%v", units, err)
	}
}

func assertSub2OIDCIdentityBridgeSemantics(t *testing.T, ctx context.Context, admin *sql.DB) {
	t.Helper()
	rows, err := admin.QueryContext(ctx, `
		SELECT projected.id,projected.verified_at,
			(SELECT string_agg(key,',' ORDER BY key) FROM jsonb_object_keys(bridge.payload) AS key)
		FROM invoice_bridge.sub2api_identities_v4('page',$1::jsonb) AS bridge(payload)
		CROSS JOIN LATERAL jsonb_to_record(bridge.payload) AS projected(id bigint,verified_at timestamptz)
		ORDER BY projected.id`,
		`{"provider_key":"https://auth.solov.cc/realms/solov","issuer":"https://auth.solov.cc/realms/solov","updated_at":"2000-01-01T00:00:00Z","id":0,"limit":50}`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type projectedIdentity struct {
		id         int64
		verifiedAt time.Time
		keys       string
	}
	got := make([]projectedIdentity, 0, 2)
	for rows.Next() {
		var item projectedIdentity
		if err = rows.Scan(&item.id, &item.verifiedAt, &item.keys); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].id != 1 || got[1].id != 2 {
		t.Fatalf("Sub2 identity bridge exported IDs=%v, want only verified identity 1 and email-verified OIDC binding 2", got)
	}
	wantVerified := []time.Time{
		time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.August, 16, 12, 34, 56, 0, time.UTC),
	}
	const wantKeys = "created_at,id,issuer,provider_key,provider_subject,provider_type,updated_at,user_id,verified_at"
	for index, item := range got {
		if !item.verifiedAt.Equal(wantVerified[index]) || item.keys != wantKeys {
			t.Fatalf("Sub2 identity bridge proof time or output allowlist mismatch: got=%+v want_time=%s want_keys=%s", item, wantVerified[index], wantKeys)
		}
	}
}

func assertUpstreamAlterAndRuntimeDrift(t *testing.T, ctx context.Context, admin *sql.DB, base *pgx.ConnConfig, source string) {
	t.Helper()
	if source == sourceagent.SourceNewAPI {
		if _, err := admin.ExecContext(ctx, `ALTER TABLE public.top_ups ALTER COLUMN payment_provider TYPE varchar(50)`); err != nil {
			t.Fatalf("New API AutoMigrate-compatible ALTER was blocked: %v", err)
		}
		reader := openBridgeReader(t, base, source, sourceagent.StreamPayments)
		if _, err := reader.ExecContext(ctx, `SELECT * FROM invoice_bridge.newapi_payments_v4('health','{}'::jsonb)`); err != nil {
			reader.Close()
			t.Fatalf("bridge failed after compatible restart migration: %v", err)
		}
		reader.Close()
		if _, err := admin.ExecContext(ctx, `ALTER TABLE public.top_ups DROP COLUMN payment_provider`); err != nil {
			t.Fatalf("source column drop was blocked by bridge: %v", err)
		}
		reader = openBridgeReader(t, base, source, sourceagent.StreamPayments)
		_, err := reader.ExecContext(ctx, `SELECT * FROM invoice_bridge.newapi_payments_v4('page','{"cutover":"2000-01-01T00:00:00Z","horizon":"2100-01-01T00:00:00Z","limit":10,"subscription_position":0,"subscription_ceiling":1,"topup_position":0,"topup_ceiling":1}'::jsonb)`)
		reader.Close()
		if err == nil {
			t.Fatal("New API source drift did not fail the connector boundary closed")
		}
		if _, err = admin.ExecContext(ctx, readContractFile(t, "newapi-bridge-v4-rollback.postgresql.sql")); err != nil {
			t.Fatalf("New API bridge could not roll back after source column drift: %v", err)
		}
		return
	}
	if _, err := admin.ExecContext(ctx, `ALTER TABLE public.payment_orders ALTER COLUMN provider_key TYPE varchar(100)`); err != nil {
		t.Fatalf("Sub2API compatible ALTER was blocked: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `ALTER TABLE public.payment_orders DROP COLUMN provider_snapshot`); err != nil {
		t.Fatalf("Sub2API source column drop was blocked: %v", err)
	}
	reader := openBridgeReader(t, base, source, sourceagent.StreamPayments)
	_, err := reader.ExecContext(ctx, `SELECT * FROM invoice_bridge.sub2api_payments_v4('health','{}'::jsonb)`)
	reader.Close()
	if err == nil {
		t.Fatal("Sub2API source drift did not fail the connector boundary closed")
	}
	if _, err = admin.ExecContext(ctx, readContractFile(t, "sub2api-bridge-v4-rollback.postgresql.sql")); err != nil {
		t.Fatalf("Sub2API bridge could not roll back after source column drift: %v", err)
	}
}

func openBridgeReader(t *testing.T, base *pgx.ConnConfig, source, stream string) *sql.DB {
	t.Helper()
	config := *base
	config.User = "invoice_" + source + "_" + stream + "_reader"
	if stream == sourceagent.StreamPayments {
		config.User = "invoice_" + source + "_payments_v3_reader"
	}
	config.Password = bridgeTestPassword
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		database := stdlib.OpenDB(config)
		if lastErr = pingIntegrationDatabase(context.Background(), database); lastErr == nil {
			return database
		}
		database.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("connect %s/%s reader after bounded retries: %v", source, stream, lastErr)
	return nil
}

func cleanupBridgeFixture(t *testing.T, ctx context.Context, admin *sql.DB) {
	t.Helper()
	_, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS invoice_bridge CASCADE`)
	for _, source := range []string{sourceagent.SourceSub2API, sourceagent.SourceNewAPI} {
		for _, suffix := range []string{"bridge_owner", "payments_reader", "identities_reader", "payments_v3_reader", "usage_reader", "credits_reader", "balances_reader"} {
			role := pgx.Identifier{"invoice_" + source + "_" + suffix}.Sanitize()
			_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+role)
			_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+role)
		}
	}
	for _, table := range []string{"auth_identities", "user_oauth_bindings", "custom_oauth_providers", "subscription_orders", "top_ups", "redemptions", "checkins", "logs", "options", "payment_orders", "redeem_codes", "user_affiliate_ledger", "promo_code_usages", "usage_logs", "users", "settings"} {
		_, _ = admin.ExecContext(ctx, `DROP TABLE IF EXISTS public.`+pgx.Identifier{table}.Sanitize()+` CASCADE`)
	}
	var database string
	if admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database) == nil {
		_, _ = admin.ExecContext(ctx, `GRANT TEMPORARY ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO PUBLIC`)
	}
}

func waitForBridgeSessions(t *testing.T, ctx context.Context, admin *sql.DB, source string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var sessions int
		if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename LIKE $1`, "invoice_"+source+"_%").Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if sessions == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge sessions did not close: %d", sessions)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func assertNoBridgeObjects(t *testing.T, ctx context.Context, admin *sql.DB) {
	t.Helper()
	var count int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='invoice_bridge'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed contract left bridge functions=%d err=%v", count, err)
	}
}

func readContractFile(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration test")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func pingIntegrationDatabase(ctx context.Context, database *sql.DB) error {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return database.PingContext(pingCtx)
}
