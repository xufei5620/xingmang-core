package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testChecksum(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestVerifyRecordedMigrationsRequiresExactSet(t *testing.T) {
	expected := map[string]string{
		"0001_init.sql": testChecksum("one"),
		"0002_next.sql": testChecksum("two"),
	}
	exact := []recordedMigration{
		{Name: "0001_init.sql", Checksum: expected["0001_init.sql"]},
		{Name: "0002_next.sql", Checksum: expected["0002_next.sql"]},
	}
	if err := verifyRecordedMigrations(expected, exact); err != nil {
		t.Fatal(err)
	}
	tests := map[string][]recordedMigration{
		"unknown_database_migration": append(append([]recordedMigration{}, exact...), recordedMigration{Name: "9999_unknown.sql", Checksum: testChecksum("unknown")}),
		"missing_bundled_migration":  exact[:1],
		"changed_checksum": {
			{Name: "0001_init.sql", Checksum: testChecksum("tampered")},
			{Name: "0002_next.sql", Checksum: expected["0002_next.sql"]},
		},
		"duplicate_database_row": append(append([]recordedMigration{}, exact...), exact[0]),
	}
	for name, recorded := range tests {
		t.Run(name, func(t *testing.T) {
			if err := verifyRecordedMigrations(expected, recorded); err == nil {
				t.Fatal("mismatch was accepted")
			}
		})
	}
}

func TestVerifyFSRejectsUnknownDatabaseMigration(t *testing.T) {
	databaseURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	deadline := time.Now().Add(10 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err = admin.Ping(pingCtx)
		cancel()
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("migrate_verify_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations := os.DirFS("../../migrations")
	if err = UpFS(ctx, pool, migrations); err != nil {
		t.Fatal(err)
	}
	if err = VerifyFS(ctx, pool, migrations); err != nil {
		t.Fatal(err)
	}
	if err = UpFS(ctx, pool, migrations); err != nil {
		t.Fatalf("idempotent concurrent-style startup failed: %v", err)
	}
	unknownChecksum := testChecksum("unknown production migration")
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES('9999_unknown.sql',$1)`, unknownChecksum); err != nil {
		t.Fatal(err)
	}
	if err = VerifyFS(ctx, pool, migrations); err == nil || !strings.Contains(err.Error(), "unknown migration") {
		t.Fatalf("unknown database migration error=%v", err)
	}
	unknownGate := migrationMapFS(t)
	unknownGate["9998_should_not_run.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE migration_unknown_gate_probe(id integer);\n")}
	if err = UpFS(ctx, pool, unknownGate); err == nil || !strings.Contains(err.Error(), "unknown migration") {
		t.Fatalf("UpFS unknown database migration error=%v", err)
	}
	var relation *string
	if err = pool.QueryRow(ctx, `SELECT to_regclass('migration_unknown_gate_probe')::text`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation != nil {
		t.Fatal("UpFS executed new DDL before rejecting an unknown database migration")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE name='9999_unknown.sql'`); err != nil {
		t.Fatal(err)
	}
	broken := migrationMapFS(t)
	broken["9998_atomic_failure.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE migration_atomic_probe(id integer);\nSELECT definitely_missing_column FROM migration_atomic_probe;\n")}
	if err = UpFS(ctx, pool, broken); err == nil || !strings.Contains(err.Error(), "9998_atomic_failure.sql") {
		t.Fatalf("broken migration error=%v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT to_regclass('migration_atomic_probe')::text`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation != nil {
		t.Fatal("failed migration left committed DDL")
	}
	var recorded int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE name='9998_atomic_failure.sql'`).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("failed migration record count=%d err=%v", recorded, err)
	}
}

func TestMigrationNamesSortsOnlySQLFiles(t *testing.T) {
	files := fstest.MapFS{
		"0002.sql": &fstest.MapFile{Data: []byte("two")},
		"README":   &fstest.MapFile{Data: []byte("ignored")},
		"0001.sql": &fstest.MapFile{Data: []byte("one")},
	}
	names, err := migrationNames(files)
	if err != nil || strings.Join(names, ",") != "0001.sql,0002.sql" {
		t.Fatalf("names=%v err=%v", names, err)
	}
}

func TestBundledMigrationsRejectsEmbeddedTransactionControl(t *testing.T) {
	files := fstest.MapFS{
		"0001.sql": &fstest.MapFile{Data: []byte("BEGIN;\nCREATE TABLE unsafe(id integer);\nCOMMIT;\n")},
	}
	if _, err := bundledMigrations(files); err == nil || !strings.Contains(err.Error(), "owns the transaction") {
		t.Fatalf("transaction control error=%v", err)
	}
}

func TestRepositoryMigrationsDelegateTransactionsToRunner(t *testing.T) {
	files, err := bundledMigrations(os.DirFS("../../migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 6 {
		t.Fatalf("bundled migrations=%d want at least 6", len(files))
	}
}

func TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure(t *testing.T) {
	databaseURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("migrate_consumption_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	all := migrationMapFS(t)
	delete(all, "0009_consumption_eligibility_ledger.sql")
	delete(all, "0010_eligibility_freeze_operations.sql")
	if err = UpFS(ctx, pool, all); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO source_instances(id,source_type,name) VALUES('10000000-0000-4000-8000-000000000001','sub2api','legacy')`,
		`INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES('20000000-0000-4000-8000-000000000001','https://id.example','legacy-user')`,
		`INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status) VALUES('30000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','legacy','test','verified')`,
		`INSERT INTO invoice_profiles(id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified) VALUES('40000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','personal',decode(repeat('11',16),'hex'),decode(repeat('22',16),'hex'),TRUE)`,
		`INSERT INTO funding_lots(id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,source_revision_hash,observed_at) VALUES('50000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','pending-lot','CNY',50000,50000,20000,0,'verified','COMPLETED','legacy-pending',now()),('50000000-0000-4000-8000-000000000002','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','issued-lot','CNY',50000,50000,0,20000,'verified','COMPLETED','legacy-issued',now())`,
		`INSERT INTO invoice_requests(id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,currency,amount_minor,status,idempotency_key,issuer_setting_revision,issue_snapshot_ciphertext) VALUES('60000000-0000-4000-8000-000000000001','LEGACY-PENDING','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','40000000-0000-4000-8000-000000000001',decode(repeat('33',16),'hex'),'CNY',20000,'pending_review','legacy-pending',NULL,NULL),('60000000-0000-4000-8000-000000000002','LEGACY-ISSUED','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','40000000-0000-4000-8000-000000000001',decode(repeat('44',16),'hex'),'CNY',20000,'issued','legacy-issued',1,decode(repeat('55',16),'hex'))`,
		`INSERT INTO invoice_allocations(id,invoice_request_id,funding_lot_id,amount_minor,source_revision_hash,allocation_state) VALUES('70000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000001','50000000-0000-4000-8000-000000000001',20000,'legacy-pending','reserved'),('70000000-0000-4000-8000-000000000002','60000000-0000-4000-8000-000000000002','50000000-0000-4000-8000-000000000002',20000,'legacy-issued','issued')`,
	}
	for _, statement := range statements {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	withNine := migrationMapFS(t)
	delete(withNine, "0010_eligibility_freeze_operations.sql")
	if err = UpFS(ctx, pool, withNine); err != nil {
		t.Fatal(err)
	}
	var pendingStatus, pendingAllocation, pendingKind string
	var pendingReserved int64
	if err = pool.QueryRow(ctx, `SELECT ir.status,ia.allocation_state,fl.eligibility_kind,fl.reserved_minor
		FROM invoice_requests ir JOIN invoice_allocations ia ON ia.invoice_request_id=ir.id
		JOIN funding_lots fl ON fl.id=ia.funding_lot_id WHERE ir.id='60000000-0000-4000-8000-000000000001'`).Scan(
		&pendingStatus, &pendingAllocation, &pendingKind, &pendingReserved); err != nil {
		t.Fatal(err)
	}
	if pendingStatus != "rejected" || pendingAllocation != "released" || pendingKind != "LEGACY_NON_INVOICEABLE" || pendingReserved != 0 {
		t.Fatalf("pending legacy migration status=%s allocation=%s kind=%s reserved=%d", pendingStatus, pendingAllocation, pendingKind, pendingReserved)
	}
	var issuedStatus, issuedKind string
	var verified, consumed, issued int64
	if err = pool.QueryRow(ctx, `SELECT ir.status,fl.eligibility_kind,fl.verified_cash_minor,
		fl.consumed_cash_minor,fl.issued_minor FROM invoice_requests ir
		JOIN invoice_allocations ia ON ia.invoice_request_id=ir.id JOIN funding_lots fl ON fl.id=ia.funding_lot_id
		WHERE ir.id='60000000-0000-4000-8000-000000000002'`).Scan(
		&issuedStatus, &issuedKind, &verified, &consumed, &issued); err != nil {
		t.Fatal(err)
	}
	if issuedStatus != "issued" || issuedKind != "LEGACY_NON_INVOICEABLE" || verified != issued || consumed != issued || issued != 20000 {
		t.Fatalf("issued legacy migration status=%s kind=%s verified=%d consumed=%d issued=%d", issuedStatus, issuedKind, verified, consumed, issued)
	}
}

func migrationMapFS(t *testing.T) fstest.MapFS {
	t.Helper()
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join("../../migrations", entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		files[entry.Name()] = &fstest.MapFile{Data: body}
	}
	return files
}
