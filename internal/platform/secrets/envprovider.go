package secrets

import (
	"context"
	"fmt"
	"os"
)

// EnvProvider 是受限环境变量适配器：只有显式登记过的 ref→环境变量映射
// 才可解析（规格 ADR-014：环境变量只是内部适配实现，不属于 Connector 契约；
// 禁止 Connector 直接读取固定变量名）。
type EnvProvider struct {
	mapping map[string]string // ref.String() → env var name
	lookup  func(string) (string, bool)
}

type EnvOption func(*EnvProvider)

// WithLookup 注入环境查找函数（测试用；默认 os.LookupEnv）。
func WithLookup(fn func(string) (string, bool)) EnvOption {
	return func(p *EnvProvider) { p.lookup = fn }
}

// NewEnvProvider 构造适配器；登记表中的每个 key 必须是合法 CredentialRef，
// value 必须非空——登记表脏数据在构造期失败，而不是运行期。
func NewEnvProvider(mapping map[string]string, opts ...EnvOption) (*EnvProvider, error) {
	cp := make(map[string]string, len(mapping))
	for k, v := range mapping {
		if _, err := ParseCredentialRef(k); err != nil {
			return nil, fmt.Errorf("env provider 登记表: %w", err)
		}
		if v == "" {
			return nil, fmt.Errorf("env provider 登记表: %q 的环境变量名为空", k)
		}
		cp[k] = v
	}
	p := &EnvProvider{mapping: cp, lookup: os.LookupEnv}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

func (p *EnvProvider) ID() string { return "env" }

func (p *EnvProvider) Resolve(_ context.Context, ref CredentialRef, _ string) (SecretValue, error) {
	envName, ok := p.mapping[ref.String()]
	if !ok {
		return SecretValue{}, fmt.Errorf("env provider: %s: %w", ref, ErrUnmappedRef)
	}
	v, ok := p.lookup(envName)
	if !ok {
		return SecretValue{}, fmt.Errorf("env provider: %s: %w", ref, ErrNotFound)
	}
	if v == "" {
		return SecretValue{}, fmt.Errorf("env provider: %s: %w", ref, ErrEmptySecret)
	}
	return NewSecretValue([]byte(v)), nil
}

func (p *EnvProvider) Metadata(_ context.Context, ref CredentialRef) (SecretMetadata, error) {
	m := SecretMetadata{Ref: ref, Provider: p.ID()}
	if envName, ok := p.mapping[ref.String()]; ok {
		if v, ok := p.lookup(envName); ok && v != "" {
			m.Available = true
		}
	}
	return m, nil
}
