package secrets

import (
	"context"
	"errors"
	"testing"
)

func fakeLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestEnvProviderResolve(t *testing.T) {
	p, err := NewEnvProvider(
		map[string]string{"secret://alerting/telegram-primary": "XM_ALERT_TG"},
		WithLookup(fakeLookup(map[string]string{"XM_ALERT_TG": "test-value-env"})),
	)
	if err != nil {
		t.Fatal(err)
	}
	v, err := p.Resolve(context.Background(), MustCredentialRef("secret://alerting/telegram-primary"), "test")
	if err != nil || v.Reveal() != "test-value-env" {
		t.Fatalf("Resolve = %q, %v", v.Reveal(), err)
	}
}

func TestEnvProviderUnmappedAndMissing(t *testing.T) {
	p, err := NewEnvProvider(
		map[string]string{"secret://alerting/telegram-primary": "XM_ALERT_TG"},
		WithLookup(fakeLookup(map[string]string{})),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.Resolve(ctx, MustCredentialRef("secret://other/name"), "t"); !errors.Is(err, ErrUnmappedRef) {
		t.Fatalf("未登记引用应 ErrUnmappedRef, got %v", err)
	}
	if _, err := p.Resolve(ctx, MustCredentialRef("secret://alerting/telegram-primary"), "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已登记但 env 缺失应 ErrNotFound, got %v", err)
	}
}

func TestEnvProviderRejectsBadMapping(t *testing.T) {
	if _, err := NewEnvProvider(map[string]string{"junk": "X"}); err == nil {
		t.Fatal("非法 ref key 应在构造时报错")
	}
	if _, err := NewEnvProvider(map[string]string{"secret://a/b": ""}); err == nil {
		t.Fatal("空环境变量名应在构造时报错")
	}
}

func TestEnvProviderMetadata(t *testing.T) {
	p, _ := NewEnvProvider(
		map[string]string{"secret://a/b": "XM_AB"},
		WithLookup(fakeLookup(map[string]string{"XM_AB": "test-value"})),
	)
	m, err := p.Metadata(context.Background(), MustCredentialRef("secret://a/b"))
	if err != nil || !m.Available || m.Provider != "env" {
		t.Fatalf("Metadata = %+v, %v", m, err)
	}
	m, _ = p.Metadata(context.Background(), MustCredentialRef("secret://a/nope"))
	if m.Available {
		t.Fatal("未登记应 Available=false")
	}
}
