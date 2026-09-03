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

	"invoice-system/backend/internal/testdb"
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
	// This fixture runs in a private schema. Migration 0013 deliberately binds
	// its production catalog assertion to public and has a dedicated public-
	// schema integration test below.
	migrations := migrationMapBeforeReadinessIndex(t)
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
	unknownGate := migrationMapBeforeReadinessIndex(t)
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
	broken := migrationMapBeforeReadinessIndex(t)
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
	all := migrationMapBeforeReadinessIndex(t)
	delete(all, "0009_consumption_eligibility_ledger.sql")
	delete(all, "0010_eligibility_freeze_operations.sql")
	delete(all, "0011_invoice_eligibility_policy.sql")
	delete(all, "0012_economic_projection_contract_v4.sql")
	// 0016 alters source_account_eligibility_state/eligibility_freezes, both
	// created by the excluded 0009 -- same reason 0010-0012 are excluded here.
	delete(all, "0016_policy_anchor.sql")
	// 0020 (XM-INV-ELIG-AUTO-RECONCILE) also alters
	// source_account_eligibility_state -- same reason as 0016 above.
	delete(all, "0020_eligibility_auto_reconcile.sql")
	// 0021 (XM-INV-ELIG-POLICY-START-ANCHOR) replaces
	// enforce_account_eligibility_cutover_contract() -- the same function
	// 0016 defines -- and is never meant to apply without 0016 already
	// having run first (a real sequential migration run always applies 0016
	// before 0021, by number order); excluded here for the same reason 0016
	// itself is.
	delete(all, "0021_policy_anchor_start_reanchor.sql")
	// 0022 (XM-INV-SCAN-CYCLE-SUPERSEDE) alters source_economic_scan_cycles,
	// also created by the excluded 0009 -- same reason as 0016/0020 above.
	delete(all, "0022_scan_cycle_supersede.sql")
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
	withNine := migrationMapBeforeReadinessIndex(t)
	delete(withNine, "0010_eligibility_freeze_operations.sql")
	delete(withNine, "0011_invoice_eligibility_policy.sql")
	delete(withNine, "0012_economic_projection_contract_v4.sql")
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

func TestEligibilityPolicyMigrationFailsClosedAndIsAtomic(t *testing.T) {
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
	schema := fmt.Sprintf("migrate_eligibility_policy_%d", time.Now().UnixNano())
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

	throughTen := migrationMapBeforeReadinessIndex(t)
	delete(throughTen, "0011_invoice_eligibility_policy.sql")
	delete(throughTen, "0012_economic_projection_contract_v4.sql")
	if err = UpFS(ctx, pool, throughTen); err != nil {
		t.Fatal(err)
	}
	const sourceID = "10000000-0000-4000-8000-000000000011"
	if _, err = pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-migration','policy-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,repeat('a',64),now(),now(),'policy-test','sub2api-economic-v3',repeat('b',64),
		'SUB2_BALANCE_1E8','p','u','c','b',repeat('c',64),repeat('c',64),1,'policy-key')`, sourceID); err != nil {
		t.Fatal(err)
	}

	all := migrationMapBeforeReadinessIndex(t)
	err = UpFS(ctx, pool, all)
	if err == nil || !strings.Contains(err.Error(), "empty pre-launch financial ledger") {
		t.Fatalf("populated-ledger migration error=%v", err)
	}
	var policyRelation *string
	if err = pool.QueryRow(ctx, `SELECT to_regclass('invoice_eligibility_policy')::text`).Scan(&policyRelation); err != nil {
		t.Fatal(err)
	}
	if policyRelation != nil {
		t.Fatal("failed eligibility policy migration left committed DDL")
	}
	var recorded int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
		WHERE name='0011_invoice_eligibility_policy.sql'`).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("failed eligibility policy migration record count=%d err=%v", recorded, err)
	}

	// Reset only this disposable migration fixture after proving the populated
	// ledger fails closed. Production source facts are permanently immutable.
	if _, err = pool.Exec(ctx, `ALTER TABLE source_cutover_manifests
		DISABLE TRIGGER source_cutover_manifests_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM source_cutover_manifests`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE source_cutover_manifests
		ENABLE TRIGGER source_cutover_manifests_immutable`); err != nil {
		t.Fatal(err)
	}
	throughEleven := migrationMapBeforeReadinessIndex(t)
	delete(throughEleven, "0012_economic_projection_contract_v4.sql")
	if err = UpFS(ctx, pool, throughEleven); err != nil {
		t.Fatal(err)
	}
	prePolicy := time.Date(2026, time.August, 31, 15, 59, 59, 999999000, time.UTC)
	if _, err = pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,repeat('a',64),$2,$2,'policy-test','sub2api-economic-v3',repeat('b',64),
		'SUB2_BALANCE_1E8','p','u','c','b',repeat('c',64),repeat('c',64),1,'policy-key')`, sourceID, prePolicy); err != nil {
		t.Fatal(err)
	}
	if contractErr := UpFS(ctx, pool, all); contractErr == nil ||
		!strings.Contains(contractErr.Error(), "economic projection v4 migration requires an empty pre-launch financial ledger") {
		t.Fatalf("populated v4 contract migration error=%v", contractErr)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
		WHERE name='0012_economic_projection_contract_v4.sql'`).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("failed v4 migration record count=%d err=%v", recorded, err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE source_cutover_manifests
		DISABLE TRIGGER source_cutover_manifests_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM source_cutover_manifests`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE source_cutover_manifests
		ENABLE TRIGGER source_cutover_manifests_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO source_ingest_state(
		source_instance_id,stream_id,sequence,last_batch_hash)
		VALUES($1,'payments',1,repeat('d',64))`, sourceID); err != nil {
		t.Fatal(err)
	}
	if stateErr := UpFS(ctx, pool, all); stateErr == nil ||
		!strings.Contains(stateErr.Error(), "economic projection v4 migration requires an empty pre-launch financial ledger") {
		t.Fatalf("advanced source-state v4 migration error=%v", stateErr)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
		WHERE name='0012_economic_projection_contract_v4.sql'`).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("advanced-state v4 migration record count=%d err=%v", recorded, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE source_ingest_state
		SET sequence=0,last_batch_hash=NULL WHERE source_instance_id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = UpFS(ctx, pool, all); err != nil {
		t.Fatal(err)
	}
	var start time.Time
	var version int64
	var paymentRequired, usageRequired bool
	if err = pool.QueryRow(ctx, `SELECT eligibility_start_at,policy_version,
		require_payment_at_or_after,require_usage_at_or_after
		FROM invoice_eligibility_policy WHERE singleton_id=1`).Scan(
		&start, &version, &paymentRequired, &usageRequired); err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC)
	if !start.UTC().Equal(wantStart) || version != 1 || !paymentRequired || !usageRequired {
		t.Fatalf("policy start=%s version=%d payment=%t usage=%t", start, version, paymentRequired, usageRequired)
	}
	for name, statement := range map[string]string{
		"update":   `UPDATE invoice_eligibility_policy SET updated_by='forbidden' WHERE singleton_id=1`,
		"delete":   `DELETE FROM invoice_eligibility_policy WHERE singleton_id=1`,
		"truncate": `TRUNCATE invoice_eligibility_policy`,
	} {
		if _, immutableErr := pool.Exec(ctx, statement); immutableErr == nil ||
			!strings.Contains(immutableErr.Error(), "immutable") {
			t.Fatalf("policy %s error=%v", name, immutableErr)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES('20000000-0000-4000-8000-000000000011','test','policy-request-user')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO invoice_profiles(
		id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified)
		VALUES('40000000-0000-4000-8000-000000000011',
		'20000000-0000-4000-8000-000000000011','personal',decode(repeat('11',16),'hex'),
		decode(repeat('22',16),'hex'),TRUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO invoice_requests(
		id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,
		currency,amount_minor,status,idempotency_key,eligibility_policy_start_at,eligibility_policy_version)
		VALUES('60000000-0000-4000-8000-000000000011','POLICY-MISMATCH',
		'20000000-0000-4000-8000-000000000011',$1,
		'40000000-0000-4000-8000-000000000011',decode(repeat('33',16),'hex'),
		'CNY',20000,'pending_review','policy-mismatch',$2,1)`, sourceID, wantStart.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "policy snapshot") {
		t.Fatalf("mismatched request policy snapshot error=%v", err)
	}

	postPolicyManifestInsert := `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,repeat('a',64),$2,$2,'policy-test','sub2api-economic-v4',repeat('b',64),
		'SUB2_BALANCE_1E8','p','u','c','b',repeat('c',64),repeat('c',64),1,'policy-key')`
	for _, invalidCutover := range []time.Time{wantStart, wantStart.Add(time.Microsecond)} {
		if _, boundaryErr := pool.Exec(ctx, postPolicyManifestInsert, sourceID, invalidCutover); boundaryErr == nil ||
			!strings.Contains(boundaryErr.Error(), "strictly before") {
			t.Fatalf("cutover %s boundary error=%v", invalidCutover, boundaryErr)
		}
	}
	if _, err = pool.Exec(ctx, postPolicyManifestInsert, sourceID, wantStart.Add(-time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE invoice_eligibility_policy
		SET policy_version=policy_version+1,updated_by='forbidden-after-ingestion'
		WHERE singleton_id=1`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("post-ingestion policy update error=%v", err)
	}
}

// TestPolicyAnchorMigrationValidatesBootstrapKindAndTriggerBoundary covers
// migration 0016: bootstrap_kind='POLICY_ANCHOR' is rejected before the
// migration (not a recognized value yet), and after it, the deferred
// cutover-boundary trigger enforces both of the new conditions -- a matching
// anchor checkpoint must exist (condition b), and the claimed cutover_at
// must be at/after the policy start (condition a).
func TestPolicyAnchorMigrationValidatesBootstrapKindAndTriggerBoundary(t *testing.T) {
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
	schema := fmt.Sprintf("migrate_policy_anchor_%d", time.Now().UnixNano())
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

	beforeAnchor := migrationMapBeforeReadinessIndex(t)
	delete(beforeAnchor, "0016_policy_anchor.sql")
	// 0021 replaces the same trigger function 0016 defines and is never
	// meant to apply without it -- see the identical exclusion's own
	// comment further up this file.
	delete(beforeAnchor, "0021_policy_anchor_start_reanchor.sql")
	if err = UpFS(ctx, pool, beforeAnchor); err != nil {
		t.Fatal(err)
	}

	sourceID := "10000000-0000-4000-8000-0000000000a1"
	userID := "20000000-0000-4000-8000-0000000000a1"
	accountID := "30000000-0000-4000-8000-0000000000a1"
	if _, err = pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','policy-anchor-migration-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','policy-anchor-migration-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'a1',$4,'test','verified')`, accountID, userID, sourceID,
		"h1:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	var policyStart time.Time
	if err = pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	manifestHash := testChecksum("policy-anchor-migration-manifest")
	configHash := testChecksum("policy-anchor-migration-config")
	snapHash := testChecksum("policy-anchor-migration-snapshot")
	cutoverAt := policyStart.Add(-10 * time.Hour)
	if _, err = pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutoverAt, configHash, snapHash); err != nil {
		t.Fatal(err)
	}

	anchorAsOf := policyStart.Add(1 * time.Hour)
	balanceUnits := "500"
	insertPolicyAnchorState := func(tx pgx.Tx, id string, at time.Time, units string) error {
		_, execErr := tx.Exec(ctx, `INSERT INTO source_account_eligibility_state(
			external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
			cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,
			eligibility_status)
			VALUES($1,$2,$3,'SUB2_BALANCE_1E8',$4::numeric,$5,'POLICY_ANCHOR',$3,900,'active')`,
			id, sourceID, at, units, manifestHash)
		return execErr
	}

	// (1) Pre-0016, POLICY_ANCHOR is not a recognized bootstrap_kind value at
	// all -- rejected by the (unchanged) CHECK constraint immediately.
	tx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	preErr := insertPolicyAnchorState(tx, accountID, anchorAsOf, balanceUnits)
	_ = tx.Rollback(ctx)
	if preErr == nil || !strings.Contains(preErr.Error(), "bootstrap_kind_check") {
		t.Fatalf("pre-0016 POLICY_ANCHOR insert error=%v", preErr)
	}

	all := migrationMapBeforeReadinessIndex(t)
	if err = UpFS(ctx, pool, all); err != nil {
		t.Fatal(err)
	}

	// (2) Post-0016, a state row inserted before its matching reconciliation
	// checkpoint (same transaction) succeeds: the trigger's own check is
	// deferred to COMMIT, by which point the checkpoint (inserted second,
	// required order -- see the migration file's own comment) exists too.
	tx2, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if err = insertPolicyAnchorState(tx2, accountID, anchorAsOf, balanceUnits); err != nil {
		t.Fatal(err)
	}
	if _, err = tx2.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
		as_of,balance_service_units,balance_negative,unit_code,
		cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
		source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES('40000000-0000-4000-8000-0000000000a1',$1,$2,'evt-a1','chk-a1',
		'reconciliation',TRUE,$3,1,$4,$5::numeric,FALSE,'SUB2_BALANCE_1E8',
		$6,$7,'cutover_baseline',1,'cur-a1',$4,$8,$4)`,
		sourceID, accountID, snapHash, anchorAsOf, balanceUnits, manifestHash, configHash, manifestHash); err != nil {
		t.Fatal(err)
	}
	if err = tx2.Commit(ctx); err != nil {
		t.Fatalf("valid POLICY_ANCHOR bootstrap rejected: %v", err)
	}
	var storedKind string
	if err = pool.QueryRow(ctx, `SELECT bootstrap_kind FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&storedKind); err != nil || storedKind != "POLICY_ANCHOR" {
		t.Fatalf("stored bootstrap_kind=%q err=%v", storedKind, err)
	}

	// (3) A claimed cutover_balance_units with no matching checkpoint at all
	// (condition b unmet) fails at commit.
	accountID2 := "30000000-0000-4000-8000-0000000000a2"
	if _, err = pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'a2',$4,'test','verified')`, accountID2, userID, sourceID,
		"h1:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	tx3, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if err = insertPolicyAnchorState(tx3, accountID2, anchorAsOf, "999"); err != nil {
		t.Fatal(err)
	}
	unanchoredErr := tx3.Commit(ctx)
	if unanchoredErr == nil || !strings.Contains(unanchoredErr.Error(), "policy anchor boundary is invalid") {
		t.Fatalf("unanchored POLICY_ANCHOR bootstrap error=%v", unanchoredErr)
	}

	// (4)-(8): the guarded re-anchor UPDATE exception (design 2.4, migration
	// 0016 part 3). accountID3 starts SIGNED_CUTOVER with a real, matching
	// post-policy checkpoint available to re-anchor to.
	accountID3 := "30000000-0000-4000-8000-0000000000a3"
	if _, err = pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'a3',$4,'test','verified')`, accountID3, userID, sourceID,
		"h1:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,
		eligibility_status)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,'SIGNED_CUTOVER',$3,900,'active')`,
		accountID3, sourceID, cutoverAt, manifestHash); err != nil {
		t.Fatal(err)
	}
	reanchorAsOf := policyStart.Add(2 * time.Hour)
	reanchorUnits := "250"
	if _, err = pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
		as_of,balance_service_units,balance_negative,unit_code,
		cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
		source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES('40000000-0000-4000-8000-0000000000a3',$1,$2,'evt-a3','chk-a3',
		'reconciliation',FALSE,$3,1,$4,$5::numeric,FALSE,'SUB2_BALANCE_1E8',
		$6,$7,'pending_finalization',1,'cur-a3',$4,$8,$4)`,
		sourceID, accountID3, snapHash, reanchorAsOf, reanchorUnits, manifestHash, configHash, manifestHash); err != nil {
		t.Fatal(err)
	}

	guardedReanchorUpdate := func(setGUC bool, newCutoverAt time.Time, newUnits, newBootstrapKind string) error {
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if setGUC {
			if _, execErr := tx.Exec(ctx, `SELECT set_config('invoice.policy_anchor_reanchor','on',true)`); execErr != nil {
				return execErr
			}
		}
		if _, execErr := tx.Exec(ctx, `UPDATE source_account_eligibility_state SET
			cutover_at=$2,cutover_balance_units=$3::numeric,bootstrap_kind=$4,finalized_through=$2
			WHERE external_account_id=$1`, accountID3, newCutoverAt, newUnits, newBootstrapKind); execErr != nil {
			return execErr
		}
		return tx.Commit(ctx)
	}

	// (4) No transaction-local GUC set: rejected as an immutable-boundary
	// violation -- the guard only lifts for this one transition, never as a
	// blanket allowance.
	if noGUCErr := guardedReanchorUpdate(false, reanchorAsOf, reanchorUnits, "POLICY_ANCHOR"); noGUCErr == nil ||
		!strings.Contains(noGUCErr.Error(), "trust boundary is immutable") {
		t.Fatalf("guarded UPDATE without GUC error=%v", noGUCErr)
	}

	// (5) GUC set but OLD.bootstrap_kind is already POLICY_ANCHOR (accountID,
	// bootstrapped in test 2 above), not one of the two legacy kinds:
	// rejected the same way -- the exception is one-shot, legacy-to-anchor
	// only, never anchor-to-anchor.
	reanchorAccountTx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if _, execErr := reanchorAccountTx.Exec(ctx, `SELECT set_config('invoice.policy_anchor_reanchor','on',true)`); execErr != nil {
		t.Fatal(execErr)
	}
	if _, execErr := reanchorAccountTx.Exec(ctx, `UPDATE source_account_eligibility_state SET
		cutover_at=$2,cutover_balance_units=$3::numeric,bootstrap_kind='POLICY_ANCHOR',finalized_through=$2
		WHERE external_account_id=$1`, accountID, reanchorAsOf, reanchorUnits); execErr != nil {
		t.Fatal(execErr)
	}
	nonLegacyOldErr := reanchorAccountTx.Commit(ctx)
	if nonLegacyOldErr == nil || !strings.Contains(nonLegacyOldErr.Error(), "trust boundary is immutable") {
		t.Fatalf("guarded UPDATE with non-legacy OLD kind error=%v", nonLegacyOldErr)
	}

	// (6) GUC set, OLD is legacy, but NEW.bootstrap_kind stays legacy (not
	// POLICY_ANCHOR): rejected the same way.
	if wrongNewKindErr := guardedReanchorUpdate(true, reanchorAsOf, reanchorUnits, "SIGNED_CUTOVER"); wrongNewKindErr == nil ||
		!strings.Contains(wrongNewKindErr.Error(), "trust boundary is immutable") {
		t.Fatalf("guarded UPDATE with non-POLICY_ANCHOR NEW kind error=%v", wrongNewKindErr)
	}

	// (7) GUC set, OLD legacy, NEW POLICY_ANCHOR, but the claimed values
	// match no checkpoint at all: rejected as an invalid policy anchor
	// boundary -- the GUC lifts the immutability rejection only, it never
	// weakens the POLICY_ANCHOR validation itself.
	if noMatchErr := guardedReanchorUpdate(true, reanchorAsOf, "999", "POLICY_ANCHOR"); noMatchErr == nil ||
		!strings.Contains(noMatchErr.Error(), "policy anchor boundary is invalid") {
		t.Fatalf("guarded UPDATE with unmatched values error=%v", noMatchErr)
	}

	// (8) GUC set, OLD legacy, NEW POLICY_ANCHOR, values matching the real
	// checkpoint above: accepted.
	if validErr := guardedReanchorUpdate(true, reanchorAsOf, reanchorUnits, "POLICY_ANCHOR"); validErr != nil {
		t.Fatalf("valid guarded re-anchor UPDATE rejected: %v", validErr)
	}
	var reanchoredKind string
	if err = pool.QueryRow(ctx, `SELECT bootstrap_kind FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID3).Scan(&reanchoredKind); err != nil || reanchoredKind != "POLICY_ANCHOR" {
		t.Fatalf("re-anchored bootstrap_kind=%q err=%v", reanchoredKind, err)
	}
}

func TestSourceReadinessActiveIndexMigrationCatalogContract(t *testing.T) {
	// testdb.URL rewrites the shared default "invoice_test" database to a
	// per-git-worktree database (created on first use), so concurrent
	// worktrees never race resetBeforeReadiness's DROP SCHEMA CASCADE
	// below. This is the only test in this file that resets the shared
	// "public" schema; every other test here already uses its own
	// uniquely-named schema per run and is not exposed to that race.
	databaseURL := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	beforeReadiness := migrationMapBeforeReadinessIndex(t)
	all := migrationMapFS(t)
	resetBeforeReadiness := func(st *testing.T) {
		st.Helper()
		if _, resetErr := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); resetErr != nil {
			st.Fatal(resetErr)
		}
		if resetErr := UpFS(ctx, pool, beforeReadiness); resetErr != nil {
			st.Fatal(resetErr)
		}
	}
	assertRecorded := func(st *testing.T, want int) {
		st.Helper()
		var recorded int
		recordErr := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
			WHERE name='0013_source_readiness_active_index.sql'`).Scan(&recorded)
		if recordErr != nil || recorded != want {
			st.Fatalf("readiness migration record count=%d want=%d err=%v", recorded, want, recordErr)
		}
	}
	assertExact := func(st *testing.T) {
		st.Helper()
		var exact bool
		exactErr := pool.QueryRow(ctx, sourceReadinessIndexCatalogContractSQL).Scan(&exact)
		if exactErr != nil || !exact {
			st.Fatalf("readiness index catalog exact=%t err=%v", exact, exactErr)
		}
	}
	assertMissing := func(st *testing.T) {
		st.Helper()
		var missing bool
		missingErr := pool.QueryRow(ctx, `SELECT pg_catalog.to_regclass(
			'public.source_ingest_events_readiness_active_idx') IS NULL`).Scan(&missing)
		if missingErr != nil || !missing {
			st.Fatalf("readiness index missing=%t err=%v", missing, missingErr)
		}
		assertRecorded(st, 0)
	}
	applyMustRequireConcurrent := func(st *testing.T) {
		st.Helper()
		applyErr := UpFS(ctx, pool, all)
		if applyErr == nil || !strings.Contains(applyErr.Error(),
			"must be prebuilt externally with CREATE INDEX CONCURRENTLY before migration") {
			st.Fatalf("populated missing-index migration error=%v", applyErr)
		}
		assertMissing(st)
	}

	t.Run("exact prebuilt avoids table lock", func(t *testing.T) {
		resetBeforeReadiness(t)
		if _, err = pool.Exec(ctx, `
			CREATE INDEX source_ingest_events_readiness_active_idx
			ON public.source_ingest_events(source_instance_id,stream_id,processing_status,created_at)
			WHERE processing_status IN ('queued','failed','processing','dead')`); err != nil {
			t.Fatal(err)
		}
		locker, acquireErr := pool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer locker.Release()
		lockTx, beginErr := locker.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer func() { _ = lockTx.Rollback(context.Background()) }()
		if _, lockErr := lockTx.Exec(ctx, `LOCK TABLE public.source_ingest_events IN ROW EXCLUSIVE MODE`); lockErr != nil {
			t.Fatal(lockErr)
		}
		shortCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		started := time.Now()
		applyErr := UpFS(shortCtx, pool, all)
		elapsed := time.Since(started)
		cancel()
		if applyErr != nil {
			t.Fatalf("exact prebuilt index blocked behind ROW EXCLUSIVE for %s: %v", elapsed, applyErr)
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("exact prebuilt migration exceeded short lock deadline: %s", elapsed)
		}
		assertRecorded(t, 1)
		assertExact(t)
	})

	t.Run("wrong same-name relation fails closed", func(t *testing.T) {
		resetBeforeReadiness(t)
		if _, err = pool.Exec(ctx, `
			CREATE INDEX source_ingest_events_readiness_active_idx
			ON public.source_ingest_events(source_instance_id,stream_id,processing_status,created_at)
			WHERE processing_status='dead'`); err != nil {
			t.Fatal(err)
		}
		applyErr := UpFS(ctx, pool, all)
		if applyErr == nil || !strings.Contains(applyErr.Error(), "source readiness active index catalog contract mismatch") {
			t.Fatalf("wrong same-name readiness index migration error=%v", applyErr)
		}
		assertRecorded(t, 0)
	})

	t.Run("missing on nonempty or physically used table fails closed", func(t *testing.T) {
		resetBeforeReadiness(t)
		if _, err = pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name)
			VALUES('10000000-0000-4000-8000-000000000013','sub2api','readiness-migration')`); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO source_ingest_state(source_instance_id,stream_id)
			VALUES('10000000-0000-4000-8000-000000000013','identities')`); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO source_ingest_batches(
			source_instance_id,stream_id,batch_id,sequence,body_hash,signing_key_id,record_count)
			VALUES('10000000-0000-4000-8000-000000000013','identities',
			'13000000-0000-4000-8000-000000000001',1,repeat('a',64),'migration-test',1)`); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO source_ingest_events(
			source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,
			payload_hash,payload_ciphertext,observed_at)
			VALUES('10000000-0000-4000-8000-000000000013','identities',
			'13000000-0000-4000-8000-000000000002','13000000-0000-4000-8000-000000000001',
			'identity_binding','upsert',repeat('b',64),decode(repeat('11',16),'hex'),now())`); err != nil {
			t.Fatal(err)
		}
		applyMustRequireConcurrent(t)
		if _, err = pool.Exec(ctx, `DELETE FROM source_ingest_events`); err != nil {
			t.Fatal(err)
		}
		var rows, physicalBytes int64
		if err = pool.QueryRow(ctx, `SELECT count(*),pg_catalog.pg_relation_size(
			'public.source_ingest_events'::pg_catalog.regclass) FROM source_ingest_events`).Scan(&rows, &physicalBytes); err != nil {
			t.Fatal(err)
		}
		if rows != 0 || physicalBytes <= 0 {
			t.Fatalf("deleted event fixture rows=%d physical_bytes=%d", rows, physicalBytes)
		}
		applyMustRequireConcurrent(t)
	})

	t.Run("missing on logically and physically empty install creates exact index", func(t *testing.T) {
		resetBeforeReadiness(t)
		var rows, physicalBytes int64
		if err = pool.QueryRow(ctx, `SELECT count(*),pg_catalog.pg_relation_size(
			'public.source_ingest_events'::pg_catalog.regclass) FROM source_ingest_events`).Scan(&rows, &physicalBytes); err != nil {
			t.Fatal(err)
		}
		if rows != 0 || physicalBytes != 0 {
			t.Fatalf("new-install event table rows=%d physical_bytes=%d", rows, physicalBytes)
		}
		if err = UpFS(ctx, pool, all); err != nil {
			t.Fatalf("empty-install readiness index creation failed: %v", err)
		}
		assertRecorded(t, 1)
		assertExact(t)
	})

	var serverVersion int
	if err = pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	t.Logf("source readiness index catalog contract passed on PostgreSQL server_version_num=%d", serverVersion)
}

const sourceReadinessIndexCatalogContractSQL = `
	SELECT EXISTS (
		SELECT 1
		FROM pg_catalog.pg_index AS i
		JOIN pg_catalog.pg_class AS idx ON idx.oid=i.indexrelid
		JOIN pg_catalog.pg_namespace AS idx_ns ON idx_ns.oid=idx.relnamespace
		JOIN pg_catalog.pg_am AS am ON am.oid=idx.relam
		JOIN pg_catalog.pg_class AS tbl ON tbl.oid=i.indrelid
		JOIN pg_catalog.pg_namespace AS tbl_ns ON tbl_ns.oid=tbl.relnamespace
		WHERE idx.relname='source_ingest_events_readiness_active_idx'
		  AND idx_ns.nspname='public'
		  AND idx.relkind='i'
		  AND am.amname='btree'
		  AND tbl.relname='source_ingest_events'
		  AND tbl_ns.nspname='public'
		  AND tbl.relkind='r'
		  AND i.indrelid='public.source_ingest_events'::pg_catalog.regclass
		  AND i.indnatts=4
		  AND i.indnkeyatts=4
		  AND i.indexprs IS NULL
		  AND pg_catalog.pg_get_indexdef(idx.oid,1,true)='source_instance_id'
		  AND pg_catalog.pg_get_indexdef(idx.oid,2,true)='stream_id'
		  AND pg_catalog.pg_get_indexdef(idx.oid,3,true)='processing_status'
		  AND pg_catalog.pg_get_indexdef(idx.oid,4,true)='created_at'
		  AND i.indpred IS NOT NULL
		  AND pg_catalog.regexp_replace(
				pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),
				'\s+','','g'
			  )='(processing_status=ANY(ARRAY[''queued''::text,''failed''::text,''processing''::text,''dead''::text]))'
		  AND NOT i.indisunique
		  AND NOT i.indisprimary
		  AND NOT i.indisexclusion
		  AND i.indisvalid
		  AND i.indisready
		  AND i.indislive
	)`

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

func migrationMapBeforeReadinessIndex(t *testing.T) fstest.MapFS {
	t.Helper()
	files := migrationMapFS(t)
	delete(files, "0013_source_readiness_active_index.sql")
	delete(files, "0014_balance_carry_forward_proof.sql")
	// 0019 (XM-INV-BALANCE-BLIP) ALTERs balance_checkpoint_evaluations and
	// balance_carry_forward_evaluations, the latter created by 0014 -- it
	// cannot apply on top of this deliberately-reduced set either.
	delete(files, "0019_balance_blip_repair.sql")
	return files
}
