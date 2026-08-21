package adminsettings

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"invoice-system/backend/internal/migrate"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func waitAdminSettingsPool(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL admin settings pool did not become reachable: %v", err)
}

func TestPostgresSettingsCASAuditAndEncryptedSecret(t *testing.T) {
	url := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminSettingsPool(t, pool)
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool)
	service := NewService(repo, testBox{})
	actor := Actor{ID: "admin-1", RequestID: "req-1"}
	settings, err := service.Update(ctx, validInput(), 0, actor)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminSettingsPool(t, pool)
	if _, err = service.Update(ctx, validInput(), 0, actor); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update=%v", err)
	}
	smtpInput := validInput()
	smtpInput.SMTPFrom = "updated@qq.com"
	settings, err = service.UpdateSMTP(ctx, smtpInput, SMTPSecretSet, "qq-auth-code", settings.Revision, Actor{ID: "admin-1", RequestID: "req-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.SMTPSecretConfigured {
		t.Fatal("secret flag false")
	}
	if settings.Revision != 2 || settings.SMTPFrom != "updated@qq.com" {
		t.Fatalf("SMTP transaction did not use one revision: %+v", settings)
	}
	var stored string
	if err = pool.QueryRow(ctx, `SELECT convert_from(smtp_secret_ciphertext,'UTF8') FROM admin_setting_secrets`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "qq-auth-code" {
		t.Fatal("SMTP secret stored in plaintext")
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE object_type='admin_settings'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("audits=%d", audits)
	}
}

func TestPostgresConcurrentInitializationUsesRevisionConflict(t *testing.T) {
	url := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewPostgresRepository(pool), testBox{})
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, updateErr := service.Update(ctx, validInput(), 0, Actor{ID: "admin", RequestID: fmt.Sprintf("init-%d", i)})
			errorsOut <- updateErr
		}(i)
	}
	close(start)
	wg.Wait()
	close(errorsOut)
	var succeeded, conflicted int
	for updateErr := range errorsOut {
		switch {
		case updateErr == nil:
			succeeded++
		case errors.Is(updateErr, ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected initialization error: %v", updateErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	var revision int64
	var audits int
	if err = pool.QueryRow(ctx, `SELECT revision FROM admin_settings WHERE singleton_id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='admin_settings.initialize'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || audits != 1 {
		t.Fatalf("revision=%d audits=%d", revision, audits)
	}
}
