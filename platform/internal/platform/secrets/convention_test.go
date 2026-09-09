package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTestSecret(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}

func TestConventionEnvProviderResolvesByCredentialRef(t *testing.T) {
	values := map[string]string{
		"XM_FINANCE_SECRET_SUB2API__TOKEN_A":  "token-a",
		"XM_FINANCE_SECRET_NEWAPI__ACCOUNT_1": "session-b",
	}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	p, err := NewConventionEnvProvider("XM_FINANCE_SECRET_", []string{"sub2api", "newapi"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ref, want string
	}{
		{"secret://sub2api/token-a", "token-a"},
		{"secret://newapi/account-1", "session-b"},
	} {
		value, err := p.Resolve(context.Background(), MustCredentialRef(tc.ref), "test")
		if err != nil || value.Reveal() != tc.want {
			t.Fatalf("Resolve(%s) = %q, %v", tc.ref, value.Reveal(), err)
		}
	}
}

func TestConventionEnvProviderScopesAndEmptyValuesFailClosed(t *testing.T) {
	values := map[string]string{"XM_FINANCE_SECRET_SUB2API__TOKEN": ""}
	p, err := NewConventionEnvProvider("XM_FINANCE_SECRET_", []string{"sub2api"}, func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(context.Background(), MustCredentialRef("secret://sub2api/token"), "test"); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("空值应 ErrEmptySecret, got %v", err)
	}
	if _, err := p.Resolve(context.Background(), MustCredentialRef("secret://other/token"), "test"); !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("未白名单 scope 应 ErrUnknownScope, got %v", err)
	}
	if _, err := p.Resolve(context.Background(), MustCredentialRef("secret://sub2api/missing"), "test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失应 ErrNotFound, got %v", err)
	}
}

func TestConventionEnvProviderMetadataDoesNotRevealValue(t *testing.T) {
	p, err := NewConventionEnvProvider("XM_FINANCE_SECRET_", []string{"sub2api"}, func(string) (string, bool) {
		return "not-a-secret-for-output", true
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.Metadata(context.Background(), MustCredentialRef("secret://sub2api/token"))
	if err != nil || !m.Available || m.Provider != "env-convention" {
		t.Fatalf("Metadata = %+v, %v", m, err)
	}
}

func TestScopedFileProviderUsesNestedLayoutAndScopeAllowlist(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sub2api", "token")
	if err := writeTestSecret(path, "file-token\n"); err != nil {
		t.Fatal(err)
	}
	p, err := NewScopedFileProvider(root, []string{"sub2api"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := p.Resolve(context.Background(), MustCredentialRef("secret://sub2api/token"), "test")
	if err != nil || value.Reveal() != "file-token" {
		t.Fatalf("Resolve = %q, %v", value.Reveal(), err)
	}
	meta, err := p.Metadata(context.Background(), MustCredentialRef("secret://sub2api/token"))
	if err != nil || meta.Provider != "file-scoped" || !meta.Available {
		t.Fatalf("Metadata = %+v, %v", meta, err)
	}
	if _, err := p.Resolve(context.Background(), MustCredentialRef("secret://other/token"), "test"); !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("scope 越界应被拒, got %v", err)
	}
}
