package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestProjectionApplyAndRollbackContracts(t *testing.T) {
	adminURL := os.Getenv("SOURCE_AGENT_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("SOURCE_AGENT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	t.Run("sub2api", func(t *testing.T) {
		cleanupProjectionRollbackFixture(t, ctx, admin)
		defer cleanupProjectionRollbackFixture(t, ctx, admin)
		setupSub2RollbackFixture(t, ctx, admin, configuration.Database)
		assertProjectionRollback(t, ctx, admin, configuration, projectionRollbackCase{
			prefix:           "invoice_sub2api_",
			expectedViews:    12,
			readerRole:       "invoice_sub2api_identities_reader",
			dependencyView:   "invoice_sub2api_payment_projection_v1",
			rollbackContract: readContractFile(t, "sub2api-projection-rollback.postgresql.sql"),
			rawTable:         "settings",
			rawRows:          2,
		})
	})

	t.Run("newapi", func(t *testing.T) {
		cleanupProjectionRollbackFixture(t, ctx, admin)
		defer cleanupProjectionRollbackFixture(t, ctx, admin)
		setupNewAPIRollbackFixture(t, ctx, admin, configuration.Database)
		assertProjectionRollback(t, ctx, admin, configuration, projectionRollbackCase{
			prefix:           "invoice_newapi_",
			expectedViews:    11,
			readerRole:       "invoice_newapi_identities_reader",
			dependencyView:   "invoice_newapi_oidc_provider_contract_v1",
			rollbackContract: readContractFile(t, "newapi-projection-rollback.postgresql.sql"),
			rawTable:         "options",
			rawRows:          4,
		})
	})
}

type projectionRollbackCase struct {
	prefix           string
	expectedViews    int
	readerRole       string
	dependencyView   string
	rollbackContract string
	rawTable         string
	rawRows          int
}

func assertProjectionRollback(t *testing.T, ctx context.Context, admin *sql.DB, configuration *pgx.ConnConfig, fixture projectionRollbackCase) {
	t.Helper()
	for _, line := range strings.Split(fixture.rollbackContract, "\n") {
		statement := strings.ToUpper(strings.SplitN(line, "--", 2)[0])
		if strings.Contains(statement, "CASCADE") {
			t.Fatal("rollback contract contains a CASCADE statement")
		}
	}
	assertProjectionObjectCounts(t, ctx, admin, fixture.prefix, fixture.expectedViews, 6)
	assertRawRowCount(t, ctx, admin, fixture.rawTable, fixture.rawRows)

	dependency := pgx.Identifier{fixture.prefix + "rollback_dependency"}.Sanitize()
	if _, err := admin.ExecContext(ctx, `CREATE VIEW public.`+dependency+` AS SELECT * FROM public.`+pgx.Identifier{fixture.dependencyView}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, fixture.rollbackContract); err == nil {
		t.Fatal("rollback ignored an external RESTRICT dependency")
	}
	_, _ = admin.ExecContext(ctx, `ROLLBACK`)
	assertProjectionObjectCounts(t, ctx, admin, fixture.prefix, fixture.expectedViews+1, 6)
	if _, err := admin.ExecContext(ctx, `DROP VIEW public.`+dependency); err != nil {
		t.Fatal(err)
	}

	readerConfig := *configuration
	readerConfig.User = fixture.readerRole
	readerConfig.Password = "rollback_test_password"
	reader := stdlib.OpenDB(readerConfig)
	reader.SetMaxIdleConns(1)
	if err := pingIntegrationDatabase(ctx, reader); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, fixture.rollbackContract); err == nil {
		reader.Close()
		t.Fatal("rollback ignored an active source reader session")
	}
	_, _ = admin.ExecContext(ctx, `ROLLBACK`)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	waitForProjectionSessionsToClose(t, ctx, admin, fixture.prefix)
	assertProjectionObjectCounts(t, ctx, admin, fixture.prefix, fixture.expectedViews, 6)

	if _, err := admin.ExecContext(ctx, fixture.rollbackContract); err != nil {
		t.Fatalf("execute reviewed rollback: %v", err)
	}
	assertProjectionObjectCounts(t, ctx, admin, fixture.prefix, 0, 0)
	assertRawRowCount(t, ctx, admin, fixture.rawTable, fixture.rawRows)
	var publicTemporary bool
	if err := admin.QueryRowContext(ctx, `SELECT has_database_privilege('public',current_database(),'TEMP')`).Scan(&publicTemporary); err != nil || !publicTemporary {
		t.Fatalf("PUBLIC TEMP baseline was not restored: value=%t err=%v", publicTemporary, err)
	}
}

func setupSub2RollbackFixture(t *testing.T, ctx context.Context, admin *sql.DB, databaseName string) {
	t.Helper()
	createProjectionRoles(t, ctx, admin, "sub2api")
	statements := []string{
		`REVOKE TEMPORARY ON DATABASE ` + pgx.Identifier{databaseName}.Sanitize() + ` FROM PUBLIC`,
		`CREATE TABLE public.settings(key text primary key,value text not null,updated_at timestamptz not null)`,
		`CREATE TABLE public.users(id bigint primary key,balance numeric(20,8),deleted_at timestamptz,email text,password_hash text)`,
		`CREATE TABLE public.usage_logs(id bigint primary key,user_id bigint,billing_type smallint,actual_cost numeric(20,10),created_at timestamptz,content text,ip_address text)`,
		`CREATE TABLE public.promo_code_usages(id bigint primary key,user_id bigint,bonus_amount numeric(20,8),used_at timestamptz)`,
		`CREATE TABLE public.user_affiliate_ledger(id bigint primary key,user_id bigint,action text,amount numeric(20,8),created_at timestamptz)`,
		`CREATE TABLE public.redeem_codes(id bigint primary key,code text,type text,value numeric(20,8),status text,used_by bigint,used_at timestamptz)`,
		`CREATE TABLE public.payment_orders(id bigint primary key,user_id bigint,status text,order_type text,amount numeric(20,8),pay_amount numeric(20,8),refund_amount numeric(20,8),completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text,provider_snapshot jsonb,recharge_code text)`,
		`CREATE TABLE public.auth_identities(id bigint primary key,user_id bigint,provider_type text,provider_key text,provider_subject text,verified_at timestamptz,issuer text,created_at timestamptz,updated_at timestamptz,secret_value text)`,
		`INSERT INTO public.settings VALUES('BALANCE_RECHARGE_MULTIPLIER','1.00',now()),('RECHARGE_FEE_RATE','0.00',now())`,
	}
	execProjectionStatements(t, ctx, admin, statements)
	if _, err := admin.ExecContext(ctx, readContractFile(t, "sub2api-source-projection-grants.postgresql.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, readContractFile(t, "sub2api-economic-projection-grants.postgresql.sql")); err != nil {
		t.Fatal(err)
	}
}

func setupNewAPIRollbackFixture(t *testing.T, ctx context.Context, admin *sql.DB, databaseName string) {
	t.Helper()
	createProjectionRoles(t, ctx, admin, "newapi")
	statements := []string{
		`REVOKE TEMPORARY ON DATABASE ` + pgx.Identifier{databaseName}.Sanitize() + ` FROM PUBLIC`,
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
	}
	execProjectionStatements(t, ctx, admin, statements)
	if _, err := admin.ExecContext(ctx, readContractFile(t, "newapi-source-projection-grants.postgresql.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, readContractFile(t, "newapi-economic-projection-grants.postgresql.sql")); err != nil {
		t.Fatal(err)
	}
}

func createProjectionRoles(t *testing.T, ctx context.Context, admin *sql.DB, source string) {
	t.Helper()
	roles := []struct {
		name  string
		login bool
	}{
		{"invoice_" + source + "_payments_reader", false},
		{"invoice_" + source + "_identities_reader", true},
		{"invoice_" + source + "_payments_v3_reader", true},
		{"invoice_" + source + "_usage_reader", true},
		{"invoice_" + source + "_credits_reader", true},
		{"invoice_" + source + "_balances_reader", true},
	}
	for _, role := range roles {
		statement := `CREATE ROLE ` + pgx.Identifier{role.name}.Sanitize() + ` NOLOGIN CONNECTION LIMIT 0 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`
		if role.login {
			statement = `CREATE ROLE ` + pgx.Identifier{role.name}.Sanitize() + ` LOGIN PASSWORD 'rollback_test_password' CONNECTION LIMIT 2 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`
		}
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
}

func execProjectionStatements(t *testing.T, ctx context.Context, admin *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("projection fixture statement failed: %v\n%s", err, statement)
		}
	}
}

func assertProjectionObjectCounts(t *testing.T, ctx context.Context, admin *sql.DB, prefix string, expectedViews, expectedRoles int) {
	t.Helper()
	var views, roles int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_views WHERE schemaname='public' AND viewname LIKE $1`, prefix+"%").Scan(&views); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE $1`, prefix+"%").Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if views != expectedViews || roles != expectedRoles {
		t.Fatalf("projection object counts views=%d/%d roles=%d/%d", views, expectedViews, roles, expectedRoles)
	}
}

func assertRawRowCount(t *testing.T, ctx context.Context, admin *sql.DB, table string, expected int) {
	t.Helper()
	var count int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM public.`+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil || count != expected {
		t.Fatalf("raw table changed table=%s count=%d/%d err=%v", table, count, expected, err)
	}
}

func waitForProjectionSessionsToClose(t *testing.T, ctx context.Context, admin *sql.DB, prefix string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var sessions int
		if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename LIKE $1`, prefix+"%").Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if sessions == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection sessions did not close: %d", sessions)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func cleanupProjectionRollbackFixture(t *testing.T, ctx context.Context, admin *sql.DB) {
	t.Helper()
	for _, prefix := range []string{"invoice_sub2api_", "invoice_newapi_"} {
		rows, err := admin.QueryContext(ctx, `SELECT viewname FROM pg_views WHERE schemaname='public' AND viewname LIKE $1 ORDER BY viewname DESC`, prefix+"%")
		if err == nil {
			var views []string
			for rows.Next() {
				var view string
				if rows.Scan(&view) == nil {
					views = append(views, view)
				}
			}
			rows.Close()
			for _, view := range views {
				_, _ = admin.ExecContext(ctx, `DROP VIEW IF EXISTS public.`+pgx.Identifier{view}.Sanitize()+` CASCADE`)
			}
		}
		roleRows, err := admin.QueryContext(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE $1`, prefix+"%")
		if err == nil {
			var roles []string
			for roleRows.Next() {
				var role string
				if roleRows.Scan(&role) == nil {
					roles = append(roles, role)
				}
			}
			roleRows.Close()
			for _, role := range roles {
				quoted := pgx.Identifier{role}.Sanitize()
				_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+quoted)
				_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+quoted)
			}
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
