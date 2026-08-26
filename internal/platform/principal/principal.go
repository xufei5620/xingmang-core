package principal

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Type 是身份类别（规格 §4.1，值逐字对齐规格）。
type Type string

const (
	TypeHuman       Type = "HUMAN"
	TypeService     Type = "SERVICE"
	TypeAI          Type = "AI"
	TypeServerAgent Type = "SERVER_AGENT"
)

var (
	// ErrInvalidType：身份类别非法。
	ErrInvalidType = errors.New("invalid principal type")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("missing required field")
	// ErrNoPrincipal：上下文中没有 Principal。
	ErrNoPrincipal = errors.New("no principal in context")
)

// ParseType 解析身份类别；只接受四个精确大写值。
func ParseType(s string) (Type, error) {
	switch Type(s) {
	case TypeHuman, TypeService, TypeAI, TypeServerAgent:
		return Type(s), nil
	default:
		return "", fmt.Errorf("principal type %q: %w", s, ErrInvalidType)
	}
}

// Principal 是人、服务、AI、Server Agent 的统一身份抽象（规格 §4.1）。
type Principal struct {
	ID                  string
	Type                Type
	IdentityZone        string // user / staff / machine
	Issuer              string
	Subject             string
	ClientID            string
	AuthenticationLevel string // 如 mfa、pwd；对应 ACR
	Environment         string
	Scopes              []string
}

// Validate 校验身份的必要字段。
func (p Principal) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("id: %w", ErrMissingField)
	}
	if _, err := ParseType(string(p.Type)); err != nil {
		return err
	}
	if p.Issuer == "" {
		return fmt.Errorf("issuer: %w", ErrMissingField)
	}
	if p.Environment == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	return nil
}

// HasScope 判断是否被授予某权限。
func (p Principal) HasScope(s string) bool { return slices.Contains(p.Scopes, s) }

// IsMachine 判断是否为机器身份（非人类）。
func (p Principal) IsMachine() bool { return p.Type != TypeHuman }

type ctxKey struct{}

// WithPrincipal 把身份放入上下文。
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext 取出身份。
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
