package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func TestPlatformUsersSecretProviderDRehearsalNestedTOTP(t *testing.T) {
	// The manager mounts its owned staff-secrets directory at cfg.SecretRoot.
	// Call the actual production assembly; no provider mock or layout fallback.
	root := t.TempDir()
	const staffID = "12345678-1234-4234-8234-123456789012"
	const syntheticTOTP = "JBSWY3DPEHPK3PXP"
	ref := secrets.MustCredentialRef("secret://staff-totp/" + staffID)
	if err := os.WriteFile(filepath.Join(root, "staff-totp__"+staffID), []byte("obsolete-flat-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := platformUsersSecretProvider(root, "rehearsal", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := provider.Resolve(context.Background(), ref, "rehearsal-layout-test"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("the actual platform reader must not consume a flat Docker-secret fixture")
	}
	if err := os.Mkdir(filepath.Join(root, "staff-totp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staff-totp", staffID), []byte(syntheticTOTP+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := provider.Resolve(context.Background(), ref, "rehearsal-layout-test")
	if err != nil {
		t.Fatalf("actual platform reader rejected nested rehearsal TOTP: %v", err)
	}
	if value.Reveal() != syntheticTOTP {
		t.Fatal("nested rehearsal TOTP did not match; contents suppressed")
	}
}
