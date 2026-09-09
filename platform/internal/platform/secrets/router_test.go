package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestRouterRoutesByScope(t *testing.T) {
	env, _ := NewEnvProvider(
		map[string]string{"secret://alerting/tg": "XM_TG"},
		WithLookup(fakeLookup(map[string]string{"XM_TG": "test-value-r"})),
	)
	r := NewRouter(map[string]SecretProvider{"alerting": env})
	v, err := r.Resolve(context.Background(), MustCredentialRef("secret://alerting/tg"), "t")
	if err != nil || v.Reveal() != "test-value-r" {
		t.Fatalf("Resolve = %q, %v", v.Reveal(), err)
	}
	if _, err := r.Resolve(context.Background(), MustCredentialRef("secret://sub2api-prod/x"), "t"); !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("未登记 scope 应 ErrUnknownScope（禁止静默回退）, got %v", err)
	}
	m, err := r.Metadata(context.Background(), MustCredentialRef("secret://alerting/tg"))
	if err != nil || !m.Available {
		t.Fatalf("Metadata 透传失败: %+v, %v", m, err)
	}
	if _, err := r.Metadata(context.Background(), MustCredentialRef("secret://nope/x")); !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("Metadata 未知 scope 也应报错, got %v", err)
	}
}
