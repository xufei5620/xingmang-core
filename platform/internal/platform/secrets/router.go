package secrets

import (
	"context"
	"fmt"
)

// Router 按 scope 把引用路由到显式登记的 Provider。
// 未登记的 scope 直接报错——禁止跨 Provider 静默回退（规格 §18.1-5）。
type Router struct {
	byScope map[string]SecretProvider
}

func NewRouter(byScope map[string]SecretProvider) *Router {
	cp := make(map[string]SecretProvider, len(byScope))
	for k, v := range byScope {
		cp[k] = v
	}
	return &Router{byScope: cp}
}

func (r *Router) ID() string { return "router" }

func (r *Router) provider(ref CredentialRef) (SecretProvider, error) {
	p, ok := r.byScope[ref.Scope()]
	if !ok {
		return nil, fmt.Errorf("router: scope %q: %w", ref.Scope(), ErrUnknownScope)
	}
	return p, nil
}

func (r *Router) Resolve(ctx context.Context, ref CredentialRef, purpose string) (SecretValue, error) {
	p, err := r.provider(ref)
	if err != nil {
		return SecretValue{}, err
	}
	return p.Resolve(ctx, ref, purpose)
}

func (r *Router) Metadata(ctx context.Context, ref CredentialRef) (SecretMetadata, error) {
	p, err := r.provider(ref)
	if err != nil {
		return SecretMetadata{Ref: ref, Provider: r.ID()}, err
	}
	return p.Metadata(ctx, ref)
}
