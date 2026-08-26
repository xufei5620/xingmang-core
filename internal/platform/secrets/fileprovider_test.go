package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeSecretFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileProviderResolve(t *testing.T) {
	root := t.TempDir()
	writeSecretFile(t, root, "sub2api-prod/read-only-admin", "test-value-1\n")
	writeSecretFile(t, root, "alerting/telegram-primary", "test-value-2")
	writeSecretFile(t, root, "ops/empty", "\n")
	p := NewFileProvider(root)
	ctx := context.Background()

	v, err := p.Resolve(ctx, MustCredentialRef("secret://sub2api-prod/read-only-admin"), "test")
	if err != nil || v.Reveal() != "test-value-1" {
		t.Fatalf("尾部换行应去除: %q, %v", v.Reveal(), err)
	}
	v, err = p.Resolve(ctx, MustCredentialRef("secret://alerting/telegram-primary"), "test")
	if err != nil || v.Reveal() != "test-value-2" {
		t.Fatalf("无换行原样返回: %q, %v", v.Reveal(), err)
	}
	if _, err = p.Resolve(ctx, MustCredentialRef("secret://ops/missing"), "test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失应 ErrNotFound, got %v", err)
	}
	if _, err = p.Resolve(ctx, MustCredentialRef("secret://ops/empty"), "test"); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("空内容应 ErrEmptySecret, got %v", err)
	}
}

func TestFileProviderErrorHasNoPlaintext(t *testing.T) {
	root := t.TempDir()
	writeSecretFile(t, root, "ops/empty", "")
	p := NewFileProvider(root)
	_, err := p.Resolve(context.Background(), MustCredentialRef("secret://ops/empty"), "test")
	if err == nil {
		t.Fatal("应报错")
	}
	if got := err.Error(); got == "" {
		t.Fatal("错误信息不应为空")
	}
}

func TestFileProviderMetadata(t *testing.T) {
	root := t.TempDir()
	writeSecretFile(t, root, "ops/token", "test-value-3")
	p := NewFileProvider(root)
	ctx := context.Background()

	m, err := p.Metadata(ctx, MustCredentialRef("secret://ops/token"))
	if err != nil || !m.Available || m.Provider != p.ID() {
		t.Fatalf("Metadata = %+v, %v", m, err)
	}
	m, err = p.Metadata(ctx, MustCredentialRef("secret://ops/none"))
	if err != nil || m.Available {
		t.Fatalf("缺失时 Available=false 且 err=nil: %+v, %v", m, err)
	}
}

func TestDockerSecretFlatLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alerting__telegram-primary"), []byte("test-value-4"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewDockerSecretProviderAt(root)
	v, err := p.Resolve(context.Background(), MustCredentialRef("secret://alerting/telegram-primary"), "test")
	if err != nil || v.Reveal() != "test-value-4" {
		t.Fatalf("扁平布局 <scope>__<name>: %q, %v", v.Reveal(), err)
	}
}
