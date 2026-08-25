package backupverify

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/migrate"
)

func TestVerifyDocumentsAgainstRestoredPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("backup_verify_%d", time.Now().UnixNano())
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
	if err = migrate.Up(ctx, pool, privateSchemaMigrationsDir(t, schema)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := document.EncryptedLocalStore{
		Root: root, Scanner: document.ScannerFunc(func(context.Context, string) error { return nil }),
		Keyring: backupTestKeyring(),
	}
	stored, err := store.SavePDF(ctx, bytes.NewBufferString("%PDF-1.7\nrestored integration invoice\n%%EOF"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	seed := `
		INSERT INTO source_instances(id,source_type,name) VALUES('10000000-0000-4000-8000-000000000001','sub2api','backup-source');
		INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES('20000000-0000-4000-8000-000000000001','https://id.example','backup-user');
		INSERT INTO invoice_profiles(id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified)
		VALUES('30000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','personal','title','email',TRUE);
		INSERT INTO invoice_requests(
			id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,
			issuer_setting_revision,issue_snapshot_ciphertext,currency,issuer_code,service_item,
			amount_minor,status,idempotency_key,eligibility_policy_start_at,
			eligibility_policy_version,version,submitted_at,updated_at)
		VALUES(
			'40000000-0000-4000-8000-000000000001','BACKUP-1','20000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','snapshot',
			1,'immutable-issue-snapshot','CNY','default','技术服务',20000,'issued','backup-idempotency',
			(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),
			(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),2,$1,$1);
		INSERT INTO invoice_documents(
			id,invoice_request_id,invoice_number,object_key,object_version,sha256,size_bytes,
			mime_type,scan_status,uploaded_by,issued_at,created_at)
		VALUES('50000000-0000-4000-8000-000000000001','40000000-0000-4000-8000-000000000001',
			'BACKUP-FP-1',$2,$3,$4,$5,'application/pdf','clean',
			'90000000-0000-4000-8000-000000000001',$1,$1)`
	if _, err = pool.Exec(ctx, seed, pgx.QueryExecModeSimpleProtocol, now, stored.ObjectKey, stored.ObjectVersion, stored.SHA256, stored.SizeBytes); err != nil {
		t.Fatal(err)
	}
	result, err := VerifyDocuments(ctx, pool, root, backupTestKeyring(), 10)
	if err != nil || result.DatabaseDocuments != 1 || result.ArchivedObjects != 1 || result.DecryptedSamples != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	path := filepath.Join(root, filepath.FromSlash(stored.ObjectKey))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	orphanPath := filepath.Join(root, "issued", "orphan.pdf.enc")
	if err = os.WriteFile(orphanPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err = os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatal(err)
	}
	orphans, err := FindOrphanDocuments(ctx, pool, root, 24*time.Hour, now)
	if err != nil || len(orphans) != 1 || orphans[0].ObjectKey != "issued/orphan.pdf.enc" {
		t.Fatalf("orphans=%+v err=%v", orphans, err)
	}
	if err = os.Remove(orphanPath); err != nil {
		t.Fatal(err)
	}
	body[len(body)-8] ^= 0xff
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyDocuments(ctx, pool, root, backupTestKeyring(), 10); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("tampered archive error=%v", err)
	}
}

func privateSchemaMigrationsDir(t *testing.T, schema string) string {
	t.Helper()
	// 0013 is deliberately bound to the production public schema. This private
	// backup fixture does not exercise that separately tested contract.
	source := filepath.Join("..", "..", "migrations")
	target := t.TempDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") ||
			entry.Name() == "0013_source_readiness_active_index.sql" {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if entry.Name() == "0014_balance_carry_forward_proof.sql" {
			// Production 0014 is deliberately public-bound. Rebind only this
			// disposable private-schema copy while preserving its fixed search_path.
			rebound := strings.ReplaceAll(string(body), "public.", "")
			rebound = strings.ReplaceAll(rebound, "SET search_path=pg_catalog,public",
				"SET search_path=pg_catalog,"+pgx.Identifier{schema}.Sanitize())
			body = []byte(rebound)
		}
		if writeErr := os.WriteFile(filepath.Join(target, entry.Name()), body, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	return target
}
